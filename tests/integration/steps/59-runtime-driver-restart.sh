# shellcheck shell=bash
# The runtime driver p-runtime-incus, the daemon's supervised child, killed
# with SIGKILL twice against real Incus: while a session creation is in
# flight after its runtime exists, and while a terminal is attached. The
# supervisor restarts it; the creation completes, after p operation retry
# when it blocked, with one Incus instance; the attachment's lease refuses
# removal until the restarted driver settles its terminal ended. Runs as pdev
# in the disposable VM; every file is a disposable fixture.
set -E
umask 077
step_dir="$P_TEST_TMP/step-59"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step59
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
incus_real=$(command -v incus)
daemon_pid=
create_pid=
attach_pid=
watchdog_pid=
uuid=
sessions=()

# Every command is bounded: one that outlives its time is killed.
inc() { timeout -k 5 60 "$incus_real" --force-local --project user-1000 "$@"; }
rpc() { timeout -k 5 35 p api "$socket" "$@"; }
cli() { timeout -k 5 240 p "$@" --socket "$socket"; }
# stage TEXT: says what the scenario does next, and remembers it for a
# failure's diagnostics.
stage() {
  printf 'P59 %ss: %s\n' "$SECONDS" "$*"
  printf '%s\n' "$*" > "$step_dir/stage"
}
guest() {
  local session="$1"
  shift
  inc exec "p-$session" --user 1000 --group 1000 --cwd /workspace \
    --env HOME=/home/p --env GIT_SSH=/usr/libexec/p/git-ssh -- "$@"
}
stop_daemon() {
  if test -n "$daemon_pid"; then
    kill -TERM "$daemon_pid" 2>/dev/null || true
    for ((i=0; i<100; i++)); do
      if ! kill -0 "$daemon_pid" 2>/dev/null; then break; fi
      sleep 0.1
    done
    if kill -0 "$daemon_pid" 2>/dev/null; then kill -KILL "$daemon_pid" 2>/dev/null || true; fi
    wait "$daemon_pid" 2>/dev/null || true
    daemon_pid=
  fi
}
cleanup() {
  : > "$step_dir/finished"
  if test -n "$watchdog_pid"; then kill "$watchdog_pid" 2>/dev/null || true; wait "$watchdog_pid" 2>/dev/null || true; fi
  exec 9>&- 2>/dev/null || true
  # A held Incus command exits without running.
  : > "$step_dir/hold-drop"
  : > "$step_dir/hold-release"
  local pid
  for pid in "$attach_pid" "$create_pid"; do
    if test -n "$pid"; then
      kill -TERM "$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
    fi
  done
  stop_daemon
  for session in "${sessions[@]}"; do
    inc delete --force "p-$session" >/dev/null 2>&1 || true
    rm -rf -- "${endpoint_prefix:?}/${session:?}"
  done
  rmdir "$endpoint_prefix" 2>/dev/null || true
}
# diagnose prints, on standard error, the last stage, the operations, this
# account's processes, Incus's operations and the fixture's logs.
diagnose() {
  printf 'last stage: %s\n' "$(cat "$step_dir/stage" 2>/dev/null)"
  local operations proc stat
  if operations=$(timeout -k 2 5 p api "$socket" operation.list '{"limit":20}' 2>/dev/null); then
    jq -cr '.result.operations[]? | {id,kind,session_uuid,status,phase,
      diagnostic: ((.diagnostic // "")[:512])}' <<< "$operations" || true
  fi
  echo 'processes (pid, parent, state, command):'
  for proc in /proc/[0-9]*; do
    [[ -O $proc ]] || continue
    read -r stat 2>/dev/null < "$proc/stat" || continue
    read -r state ppid _ <<< "${stat##*) }"
    printf '%s %s %s %s\n' "${proc#/proc/}" "$ppid" "$state" \
      "$(tr '\0\n' '  ' < "$proc/cmdline" 2>/dev/null | cut -c1-200)"
  done
  echo 'Incus operations:'
  timeout -k 2 10 "$incus_real" --force-local --project user-1000 operation list --format csv 2>&1 | head -20 || true
  for log in daemon.err create.err retry.err inits first.out first.err second.out second.err; do
    if test -f "$step_dir/$log"; then
      printf '%s (last 2048 bytes):\n' "$log"
      tail -c 2048 "$step_dir/$log" | tr -cd '[:print:]\n' || true
    fi
  done
}
on_error() {
  printf 'P_RUNTIME_DRIVER_RESTART_FAIL line=%s status=%s\n' "$2" "$1" >&2
  diagnose >&2
}
# watchdog LIMIT: once SECONDS reaches LIMIT before the scenario finished,
# reports the scenario failed with its diagnostics, and kills the shell's
# children so that the stuck command fails with its line.
watchdog() {
  trap - ERR
  set +e
  while ((SECONDS < $1)); do
    sleep 1
    if test -f "$step_dir/finished"; then return; fi
  done
  printf 'P_RUNTIME_DRIVER_RESTART_FAIL watchdog at %ss\n' "$SECONDS" >&2
  diagnose >&2
  local proc stat ppid
  for proc in /proc/[0-9]*; do
    read -r stat 2>/dev/null < "$proc/stat" || continue
    read -r _ ppid _ <<< "${stat##*) }"
    if test "$ppid" = "$$" && test "${proc#/proc/}" != "$BASHPID"; then kill -KILL "${proc#/proc/}" 2>/dev/null; fi
  done
}
trap cleanup EXIT
trap 'on_error "$?" "$LINENO"' ERR
watchdog $((SECONDS+480)) &
watchdog_pid=$!

activate() {
  local package="$1" target="$2" grant="$3" digest id
  digest=$(p plugins conformance "$package" | jq -er '.sha256 | select(type=="string" and length==64)')
  id=$(jq -er '.id | select(type=="string" and length>0)' "$package/plugin.json")
  jq -n --arg path "$package" --arg digest "$digest" --arg id "$id" --arg grant "$grant" \
    '{schema:"p.activation/v1",plugins:[{id:$id,path:$path,sha256:$digest,grants:[$grant],config:{}}]}' > "$target"
}
activate "$P_TEST_GIT_PLUGIN" "$step_dir/git.json" git.project
activate "$P_TEST_SOURCE/plugins/bundled/tmux-host" "$step_dir/runtime.json" session.asset.install

# The driver runs from a byte-identical copy owned by this account, which
# passes the launch checks while its owner may execute it. Removing that bit
# keeps the supervisor's restarts failing, so the driver stays down.
driver_path="$step_dir/driver/p-runtime-incus"
mkdir -m 0700 "$step_dir/driver"
cp -- "$(readlink -f -- "$(command -v p-runtime-incus)")" "$driver_path"
chmod 0500 "$driver_path"

# The driver's Incus client records each init and holds the first endpoint
# device addition after the fixture arms it, until the fixture releases it:
# the runtime then exists in Incus and its Create is in flight. Released with
# drop, the held command exits without running. Incus runs with
# PATH=/usr/bin:/bin, so use shell builtins and absolute utilities.
incus_wrapper="$step_dir/incus-wrapper"
printf '#!%s\n' "$(command -v bash)" > "$incus_wrapper"
printf 'incus_real=%q\nfixture=%q\nsleep_binary=%q\nrm_binary=%q\n' \
  "$incus_real" "$step_dir" "$(command -v sleep)" "$(command -v rm)" >> "$incus_wrapper"
cat >> "$incus_wrapper" <<'NATIVE_WRAPPER'
set -euo pipefail
# Incus's arguments: --force-local --project PROJECT COMMAND...
if test "$#" -ge 6 && test "$4" = init; then
 printf 'init %s\n' "$6" >> "$fixture/inits"
fi
if test "$#" -ge 8 && test "$4 $5 $6 $8" = "config device add p-endpoint" && test -f "$fixture/hold-arm"; then
 "$rm_binary" -f -- "$fixture/hold-arm"
 printf '%s\n' "$7" > "$fixture/held"
 for ((attempt=0; attempt<1200; attempt++)); do
  if test -f "$fixture/hold-release"; then break; fi
  "$sleep_binary" 0.05
 done
 if test -f "$fixture/hold-drop" || test ! -f "$fixture/hold-release"; then exit 75; fi
fi
exec "$incus_real" "$@"
NATIVE_WRAPPER
chmod 0700 "$incus_wrapper"

base=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
test "${#base}" -eq 64
driver=$(bash "$P_TEST_SOURCE/tests/integration/runtime-driver-config.sh" "$incus_wrapper" |
  jq -c --arg path "$driver_path" '.path=$path')
test "$(sha256sum -- "$driver_path" | cut -d' ' -f1)" = "$(jq -er '.sha256' <<< "$driver")"
jq -n --arg state "$state" --arg git_activation "$step_dir/git.json" \
  --arg git_id "$(jq -er '.plugins[0].id' "$step_dir/git.json")" \
  --arg runtime_activation "$step_dir/runtime.json" \
  --arg host_id "$(jq -er '.plugins[0].id' "$step_dir/runtime.json")" \
  --argjson driver "$driver" --arg prefix "$endpoint_prefix" --arg image "$base" \
  '{schema:"p.host/v1",state_dir:$state,
    git:{activation_path:$git_activation,source_plugin_id:$git_id,listen:"127.0.0.1:0",driver:{name:"git",config:{}}},
    runtime:{activation_path:$runtime_activation,host_plugin_id:$host_id,driver:$driver,
      endpoint_prefix:$prefix,disk_source_ceilings:["/var/lib/p-vm/endpoints","/var/lib/p-vm/grants"],
      base_image_fingerprint:$image,
      project_policy:{network:"none",filesystem_mounts:[],command:["/run/current-system/sw/bin/bash"]}}}' \
  > "$step_dir/host.json"
bash "$P_TEST_SOURCE/tests/integration/public-egress-host-config.sh" "$step_dir/host.json"

start_daemon() {
  p daemon "$step_dir/host.json" >> "$step_dir/daemon.out" 2>> "$step_dir/daemon.err" &
  daemon_pid=$!
  local deadline=$((SECONDS+30))
  while ((SECONDS < deadline)); do
    if test -S "$socket" && rpc system.health | jq -e '.result.control_state=="ready"' >/dev/null 2>&1; then return; fi
    if ! kill -0 "$daemon_pid" 2>/dev/null; then echo 'daemon exited before readiness' >&2; return 1; fi
    sleep 0.1
  done
  echo 'daemon did not become ready' >&2
  return 1
}
# driver_pids prints the PID of each child of the daemon that runs the
# driver: the supervisor starts the checked file by its descriptor, so the
# process's executable is the copy.
driver_pids() {
  local proc stat ppid
  for proc in /proc/[0-9]*; do
    read -r stat 2>/dev/null < "$proc/stat" || continue
    read -r _ ppid _ <<< "${stat##*) }"
    test "$ppid" = "$daemon_pid" || continue
    if test "$(readlink -- "$proc/exe" 2>/dev/null)" = "$driver_path"; then printf '%s\n' "${proc#/proc/}"; fi
  done
}
# await_driver OLD [SECONDS]: prints the PID of the one running driver once
# it is not OLD.
await_driver() {
  local old="$1" pids deadline=$((SECONDS+${2:-30}))
  while ((SECONDS < deadline)); do
    mapfile -t pids < <(driver_pids)
    if test "${#pids[@]}" -eq 1 && test "${pids[0]}" != "$old"; then printf '%s\n' "${pids[0]}"; return; fi
    sleep 0.1
  done
  echo "no runtime driver other than ${old:-none} runs" >&2
  return 1
}
# await_log TEXT [COUNT]: the daemon's log says TEXT on COUNT lines (1 when
# not given), within 30 seconds.
await_log() {
  local deadline=$((SECONDS+30))
  while ((SECONDS < deadline)); do
    if test "$(grep -cF -- "$1" "$step_dir/daemon.err")" -ge "${2:-1}"; then return; fi
    sleep 0.1
  done
  echo "daemon log lacks ${2:-1} lines saying: $1" >&2
  return 1
}
inspect() { rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')"; }
# await_attached COUNT [SECONDS]: the session's attachment count reaches
# COUNT, within 30 seconds when not given.
await_attached() {
  local deadline=$((SECONDS+${2:-30}))
  while ((SECONDS < deadline)); do
    if inspect | jq -e --argjson n "$1" '.result.session.attached_count==$n' >/dev/null 2>&1; then return; fi
    sleep 0.2
  done
  echo "attached_count did not reach $1" >&2
  return 1
}
# removal_plan METHOD: the dry run of session.discard or session.delete.
removal_plan() { rpc "$1" "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')"; }
# attach NAME: p attach on a 80x24 terminal whose input is the fixture's
# descriptor 9 and whose output collects in NAME.out.
attach() {
  mkfifo "$step_dir/$1.input"
  cat "$step_dir/$1.input" | timeout -k 5 120 script -q -e \
    -c "stty rows 24 cols 80; exec p attach $socket $uuid" "$step_dir/$1.log" \
    > "$step_dir/$1.out" 2> "$step_dir/$1.err" &
  attach_pid=$!
  exec 9> "$step_dir/$1.input"
}
# run_in_terminal NAME LINE TEXT: types LINE and waits for the terminal to
# show TEXT, which LINE prints and does not contain.
run_in_terminal() {
  printf '%s\r' "$2" >&9
  local deadline=$((SECONDS+30))
  while ((SECONDS < deadline)); do
    if grep -aFq -- "$3" "$step_dir/$1.out"; then return; fi
    if ! kill -0 "$attach_pid" 2>/dev/null; then echo 'the attachment ended before its output' >&2; return 1; fi
    sleep 0.1
  done
  echo "the terminal did not show $3" >&2
  return 1
}
# end_attach: waits for the attachment client to exit and sets attach_status.
# The input's writer closes before the wait, which waits for the whole
# pipeline: its reader ends only at the end of the input.
end_attach() {
  local deadline=$((SECONDS+30))
  while kill -0 "$attach_pid" 2>/dev/null && ((SECONDS < deadline)); do sleep 0.1; done
  if kill -0 "$attach_pid" 2>/dev/null; then echo 'the attachment client did not exit' >&2; return 1; fi
  exec 9>&-
  attach_status=0
  wait "$attach_pid" || attach_status=$?
  attach_pid=
}

stage 'start the daemon'
start_daemon
first_driver=$(await_driver "")
stage 'create the project and push main'
cli project create driver-restart --json > "$step_dir/project.json"
bootstrap=$(jq -er '.operation | select(.status=="completed") | .session_uuid | select(length==36)' "$step_dir/project.json")
sessions+=("$bootstrap")
guest "$bootstrap" /run/current-system/sw/bin/git config user.name P
guest "$bootstrap" /run/current-system/sw/bin/git config user.email p@example.invalid
guest "$bootstrap" /run/current-system/sw/bin/bash -c \
  'printf base > tracked; git add tracked; git commit -qm base; git push -q origin HEAD:main'

# Killed while a creation is in flight: its runtime exists in Incus and its
# Create, holding at the endpoint device, has not answered.
stage 'create a session, held at its endpoint device'
: > "$step_dir/hold-arm"
timeout -k 5 240 p session create driver-restart:work --from main --json --socket "$socket" \
  > "$step_dir/create.json" 2> "$step_dir/create.err" &
create_pid=$!
deadline=$((SECONDS+120))
until test -s "$step_dir/held"; do
  if ((SECONDS >= deadline)); then echo 'the creation never reached its endpoint device' >&2; exit 1; fi
  if ! kill -0 "$create_pid" 2>/dev/null; then echo 'p session create ended before the runtime existed' >&2; exit 1; fi
  sleep 0.1
done
read -r held < "$step_dir/held"
uuid=${held#p-}
[[ $uuid =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]]
sessions+=("$uuid")
inc list "^p-$uuid$" --format json | jq -e --arg uuid "$uuid" \
  'length==1 and .[0].status=="Stopped" and .[0].config["user.p.session_uuid"]==$uuid' >/dev/null
create_op=$(rpc operation.list '{"limit":20}' | jq -er --arg uuid "$uuid" \
  '[.result.operations[] | select(.kind=="session.create" and .session_uuid==$uuid and .status=="running")] |
   select(length==1) | .[0].id')
stage "kill driver $first_driver during the creation"
kill -KILL "$first_driver"
second_driver=$(await_driver "$first_driver")
await_log "runtime driver $driver_path restarted" 1
printf 'driver %s killed during the creation; restarted as %s\n' "$first_driver" "$second_driver"
# The killed driver's Incus command never ran.
: > "$step_dir/hold-drop"
: > "$step_dir/hold-release"

# The creation's init outcome was unknown when the driver died. It completes,
# or blocks until p operation retry completes it.
stage 'wait for p session create'
create_status=0
wait "$create_pid" || create_status=$?
create_pid=
case $create_status in
  0) result="$step_dir/create.json" ;;
  4)
    jq -e --arg id "$create_op" '.operation | .id==$id and .status=="blocked"' "$step_dir/create.json" >/dev/null
    jq -c '.operation | {status,phase,reason,diagnostic}' "$step_dir/create.json"
    grep -Fq "p operation retry $create_op" "$step_dir/create.err"
    stage "retry $create_op"
    cli operation retry "$create_op" --json > "$step_dir/retry.json" 2> "$step_dir/retry.err"
    result="$step_dir/retry.json"
    ;;
  *) echo "p session create exited $create_status" >&2; exit 1 ;;
esac
jq -e --arg id "$create_op" --arg uuid "$uuid" \
  '.operation | .id==$id and .kind=="session.create" and .status=="completed" and .session_uuid==$uuid' "$result" >/dev/null
printf 'creation after the driver was killed: p session create exited %s\n' "$create_status"
stage 'check the created session'
# Exactly one Incus instance carries the session, and its init was sent once.
inc list --format json | jq -e --arg uuid "$uuid" \
  '[.[] | select(.config["user.p.session_uuid"]==$uuid)] | length==1 and .[0].name==("p-"+$uuid)' >/dev/null
test "$(grep -cFx "init p-$uuid" "$step_dir/inits")" -eq 1
inspect | jq -e '.result.session | .session_condition=="ready" and .registry_state=="established" and .branch=="work"' >/dev/null
test "$(guest "$uuid" /run/current-system/sw/bin/git rev-parse --abbrev-ref HEAD)" = work
stage 'stop the session'
cli session stop "$uuid" > /dev/null
inspect | jq -e '.result.session.session_condition=="stopped"' >/dev/null
stage 'start the session'
cli session start "$uuid" > /dev/null
inspect | jq -e '.result.session.session_condition=="ready"' >/dev/null

# Killed while a terminal is attached, with restarts failing their launch
# checks: the attachment client ends, and the lease stays while the driver
# is down, refusing removal.
stage 'attach'
attach first
await_attached 1
# shellcheck disable=SC2016 # The session's shell expands it.
run_in_terminal first 'echo P59_FIRST_$((40+19))' P59_FIRST_59
stage "kill driver $second_driver while attached"
chmod 0400 "$driver_path"
kill -KILL "$second_driver"
end_attach
printf 'driver %s killed while attached; p attach exited %s\n' "$second_driver" "$attach_status"
test "$attach_status" -ne 0
grep -aFq 'without proof' "$step_dir/first.out"
stage 'check the lease while the driver is down'
await_log "runtime driver $driver_path did not restart"
grep -Fq "$driver_path lacks its owner's execute bit" "$step_dir/daemon.err"
test -z "$(driver_pids)"
inspect | jq -e '.result.session | .attached_count==1 and .session_condition=="unreachable"' >/dev/null
for method in session.discard session.delete; do
  removal_plan "$method" | jq -e --arg method "$method" \
    '.result.plan | .action==$method and .allowed==false and ([.refusals[].code]==["attachment_active"])' >/dev/null
done
inspect | jq -e '.result.session.attached_count==1' >/dev/null

# Restarted, the driver settles the terminal ended; removal is allowed and a
# new attachment works.
stage 'let the driver restart'
chmod 0500 "$driver_path"
third_driver=$(await_driver "" 60)
test "$third_driver" != "$second_driver"
await_log "runtime driver $driver_path restarted" 2
printf 'driver restarted as %s\n' "$third_driver"
await_attached 0 45
inspect | jq -e '.result.session.session_condition=="ready"' >/dev/null
for method in session.discard session.delete; do
  removal_plan "$method" | jq -e '.result.plan | .allowed==true and .refusals==[]' >/dev/null
done
stage 'attach again'
attach second
await_attached 1
# shellcheck disable=SC2016 # The session's shell expands it.
run_in_terminal second 'echo P59_AGAIN_$((50+9))' P59_AGAIN_59
printf '\002d' >&9
end_attach
test "$attach_status" -eq 0
await_attached 0

stage 'discard the session'
# The discard runs to its end through the restarted driver.
plan=$(removal_plan session.discard)
tag=$(jq -er '.result | select(.plan.allowed==true) | .change_tag' <<< "$plan")
discard=$(rpc session.discard "$(jq -nc --arg uuid "$uuid" --arg tag "$tag" '{uuid:$uuid,key:"driver-restart-discard",if_match:$tag}')")
discard_op=$(jq -er '.result.operation.id | select(length==36)' <<< "$discard")
cli operation wait "$discard_op" --json | jq -e '.operation | .kind=="session.discard" and .status=="completed"' >/dev/null
inc list "^p-$uuid$" --format json | jq -e 'length==0' >/dev/null
test "$(driver_pids)" = "$third_driver"
kill -0 "$daemon_pid"
stop_daemon
echo P_RUNTIME_DRIVER_RESTART_PASS
