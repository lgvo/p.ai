# shellcheck shell=bash
# The source driver p-source-git, the daemon's supervised child, killed with
# SIGKILL twice, each time after its Git landed an effect and before the
# driver answered: once after a session's push was approved and its head
# moved, and once after a session creation's head was created. The
# supervisor restarts it. The restarted driver ends the dead driver's Git
# service, so that its lease is released: the next creation's admission and
# a discard wait for every lease. The creation settles through the restarted
# driver and completes, after p operation retry when it blocked. Git ran the
# push's receive-pack and the head creation once each. Runs as pdev in the
# disposable VM; every file is a disposable fixture.
set -E
umask 077
step_dir="$P_TEST_TMP/step-60"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step60
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
incus_real=$(command -v incus)
project=source-restart
daemon_pid=
push_pid=
create_pid=
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
  printf 'P60 %ss: %s\n' "$SECONDS" "$*"
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
  # A held Git command exits.
  : > "$step_dir/hold-release"
  local pid
  for pid in "$push_pid" "$create_pid"; do
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
# account's processes and the fixture's logs.
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
  for log in daemon.err git-log held push.out push.err create.err retry.err; do
    if test -f "$step_dir/$log"; then
      printf '%s (last 2048 bytes):\n' "$log"
      tail -c 2048 "$step_dir/$log" | tr -cd '[:print:]\n' || true
    fi
  done
}
on_error() {
  printf 'P_SOURCE_DRIVER_RESTART_FAIL line=%s status=%s\n' "$2" "$1" >&2
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
  printf 'P_SOURCE_DRIVER_RESTART_FAIL watchdog at %ss\n' "$SECONDS" >&2
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
watchdog $((SECONDS+540)) &
watchdog_pid=$!

# The driver's Git is a wrapper that logs each receive-pack and each
# creation of the head held. Once the fixture arms it, it holds the next of
# a kind: it runs Git to its end, records its process and Git's status, and
# waits until the fixture releases it before it exits with that status, so
# the driver's call is still waiting when the driver dies. Released, a held
# command only exits. The driver runs its Git with PATH set to the wrapper's
# directory, so use shell builtins and absolute utilities.
git_wrapper="$step_dir/git-bin/git"
mkdir -m 0700 "$step_dir/git-bin"
printf '#!%s\n' "$(command -v bash)" > "$git_wrapper"
printf 'git_real=%q\nfixture=%q\nsleep_binary=%q\nrm_binary=%q\n' \
  "$(readlink -f -- "$(command -v git)")" "$step_dir" "$(command -v sleep)" "$(command -v rm)" >> "$git_wrapper"
cat >> "$git_wrapper" <<'GIT_WRAPPER'
set -uo pipefail
kind=
update=false held=false delete=false
for arg in "$@"; do
 case $arg in
  receive-pack) kind=receive ;;
  update-ref) update=true ;;
  refs/heads/held) held=true ;;
  -d) delete=true ;;
 esac
done
if $update && $held && ! $delete; then kind=create; fi
if test -z "$kind"; then exec "$git_real" "$@"; fi
printf '%s\n' "$kind" >> "$fixture/git-log"
if ! "$rm_binary" -- "$fixture/arm-$kind" 2>/dev/null; then exec "$git_real" "$@"; fi
status=0
"$git_real" "$@" || status=$?
printf '%s %s %s\n' "$kind" "$$" "$status" > "$fixture/held"
for ((attempt=0; attempt<2400; attempt++)); do
 if test -f "$fixture/hold-release"; then break; fi
 "$sleep_binary" 0.05
done
exit "$status"
GIT_WRAPPER
chmod 0700 "$git_wrapper"
: > "$step_dir/git-log"

base_image=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
test "${#base_image}" -eq 64
driver=$(bash "$P_TEST_SOURCE/tests/integration/runtime-driver-config.sh" "$incus_real")
source_driver=$(bash "$P_TEST_SOURCE/tests/integration/source-driver-config.sh" |
  jq -c --arg git "$git_wrapper" '.config.git=$git')
driver_path=$(jq -er '.path' <<< "$source_driver")
jq -n --arg state "$state" --argjson source_driver "$source_driver" \
  --argjson driver "$driver" --arg prefix "$endpoint_prefix" --arg image "$base_image" \
  '{schema:"p.host/v1",state_dir:$state,
    git:{listen:"127.0.0.1:0",driver:$source_driver},
    runtime:{driver:$driver,
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
# source driver. The hook the driver's receive-pack runs is the same
# executable, but receive-pack's child.
driver_pids() {
  local proc stat ppid
  for proc in /proc/[0-9]*; do
    read -r stat 2>/dev/null < "$proc/stat" || continue
    read -r _ ppid _ <<< "${stat##*) }"
    test "$ppid" = "$daemon_pid" || continue
    if test "$(readlink -- "$proc/exe" 2>/dev/null)" = "$driver_path"; then printf '%s\n' "${proc#/proc/}"; fi
  done
}
# await_driver OLD: prints the PID of the one running source driver once it
# is not OLD, within 30 seconds.
await_driver() {
  local old="$1" pids deadline=$((SECONDS+30))
  while ((SECONDS < deadline)); do
    mapfile -t pids < <(driver_pids)
    if test "${#pids[@]}" -eq 1 && test "${pids[0]}" != "$old"; then printf '%s\n' "${pids[0]}"; return; fi
    sleep 0.1
  done
  echo "no source driver other than ${old:-none} runs" >&2
  return 1
}
# await_log TEXT COUNT: the daemon's log says TEXT on COUNT lines within 30
# seconds.
await_log() {
  local deadline=$((SECONDS+30))
  while ((SECONDS < deadline)); do
    if test "$(grep -cF -- "$1" "$step_dir/daemon.err")" -ge "$2"; then return; fi
    sleep 0.1
  done
  echo "daemon log lacks $2 lines saying: $1" >&2
  return 1
}
# await_held KIND WAITER: waits until the Git wrapper holds a command of
# KIND, while WAITER, the process that leads to it, runs; then sets
# held_pid, the wrapper's process, and held_status, Git's exit status.
await_held() {
  local kind deadline=$((SECONDS+180))
  until test -s "$step_dir/held" && read -r kind held_pid held_status < "$step_dir/held" && test "$kind" = "$1"; do
    if ((SECONDS >= deadline)); then echo "Git never held a $1" >&2; return 1; fi
    if ! kill -0 "$2" 2>/dev/null; then echo "what leads to a $1 ended before Git held it" >&2; return 1; fi
    sleep 0.1
  done
  rm -f -- "$step_dir/held"
}
# running PID: the process runs, and has not ended unreaped.
running() {
  local stat
  read -r stat 2>/dev/null < "/proc/$1/stat" || return 1
  [[ ${stat##*) } != Z* ]]
}
# await_gone PID WHAT: the process ends within 30 seconds.
await_gone() {
  local deadline=$((SECONDS+30))
  while running "$1"; do
    if ((SECONDS >= deadline)); then echo "$2 still runs" >&2; return 1; fi
    sleep 0.1
  done
}
# count KIND: how many Git commands of KIND the wrapper saw.
count() { grep -cFx -- "$1" "$step_dir/git-log" || true; }
# tip BRANCH: P's head BRANCH of the project, or nothing.
tip() {
  rpc project.branches "$(jq -nc --arg project "$project" '{project:$project,limit:8}')" |
    jq -er --arg ref "refs/heads/$1" '[.result.refs[] | select(.ref==$ref) | .oid] | .[0] // ""'
}
inspect() { rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')"; }

stage 'start the daemon'
start_daemon
first_driver=$(await_driver "")
stage 'create the project and push main'
cli project create "$project" --json > "$step_dir/project.json"
bootstrap=$(jq -er '.operation | select(.status=="completed") | .session_uuid | select(length==36)' "$step_dir/project.json")
sessions+=("$bootstrap")
guest "$bootstrap" /run/current-system/sw/bin/git config user.name P
guest "$bootstrap" /run/current-system/sw/bin/git config user.email p@example.invalid
guest "$bootstrap" /run/current-system/sw/bin/bash -c \
  'printf base > tracked; git add tracked; git commit -qm base; git push -q origin HEAD:main'
base=$(guest "$bootstrap" /run/current-system/sw/bin/git rev-parse HEAD)
test "$(tip main)" = "$base"

# Killed after a push was approved and its head moved: receive-pack ended,
# and ServeGit still waits for its process, which the wrapper holds.
stage 'push, held after receive-pack'
guest "$bootstrap" /run/current-system/sw/bin/bash -c 'printf next > tracked; git commit -qam next'
next=$(guest "$bootstrap" /run/current-system/sw/bin/git rev-parse HEAD)
receives=$(count receive)
: > "$step_dir/arm-receive"
guest "$bootstrap" /run/current-system/sw/bin/git push origin HEAD:main \
  > "$step_dir/push.out" 2> "$step_dir/push.err" &
push_pid=$!
await_held receive "$push_pid"
test "$held_status" -eq 0
test "$(tip main)" = "$next"
kill -0 "$push_pid"
stage "kill driver $first_driver during the push"
kill -KILL "$first_driver"
second_driver=$(await_driver "$first_driver")
await_log "source driver $driver_path restarted" 1
printf 'driver %s killed during the push; restarted as %s\n' "$first_driver" "$second_driver"
# The restarted driver ended the dead driver's service, Git's process group,
# which the kill left running; the service's lease ends only after.
await_gone "$held_pid" "the dead driver's receive-pack"
push_status=0
wait "$push_pid" || push_status=$?
push_pid=
printf 'push during the kill: git push exited %s\n' "$push_status"
# Nothing replayed the push: main moved once, to the pushed commit.
test "$(count receive)" -eq "$((receives + 1))"
test "$(tip main)" = "$next"

# Killed after a creation's head was created: the update-ref ended, and
# CreateBranch had not answered. The creation's admission waits for every
# lease, the killed push's too.
stage 'create a session, held after its head creation'
: > "$step_dir/arm-create"
timeout -k 5 240 p session create "$project:held" --from main --json --socket "$socket" \
  > "$step_dir/create.json" 2> "$step_dir/create.err" &
create_pid=$!
await_held create "$create_pid"
test "$held_status" -eq 0
test "$(tip held)" = "$next"
create_op=$(rpc operation.list '{"limit":20}' | jq -c '[.result.operations[] |
  select(.kind=="session.create" and .status=="running")] | select(length==1) | .[0]')
uuid=$(jq -er '.session_uuid | select(length==36)' <<< "$create_op")
create_op=$(jq -er '.id' <<< "$create_op")
sessions+=("$uuid")
stage "kill driver $second_driver during the head creation"
kill -KILL "$second_driver"
third_driver=$(await_driver "$second_driver")
await_log "source driver $driver_path restarted" 2
printf 'driver %s killed during the head creation; restarted as %s\n' "$second_driver" "$third_driver"
# The held update-ref was the driver's child, killed with its process group.
await_gone "$held_pid" "the dead driver's update-ref"

# The creation's head effect had an unknown outcome when the driver died.
# It settles through the restarted driver, and the creation completes, or
# blocks until p operation retry completes it.
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
# The head was created once, never sent again, at the source's commit.
test "$(count create)" -eq 1
test "$(tip held)" = "$next"
inspect | jq -e '.result.session | .session_condition=="ready" and .registry_state=="established" and .branch=="held"' >/dev/null
test "$(guest "$uuid" /run/current-system/sw/bin/git rev-parse --abbrev-ref HEAD)" = held

# The restarted driver serves pushes.
stage 'push from the created session'
guest "$uuid" /run/current-system/sw/bin/git config user.name P
guest "$uuid" /run/current-system/sw/bin/git config user.email p@example.invalid
guest "$uuid" /run/current-system/sw/bin/bash -c 'printf held > tracked; git commit -qam held; git push -q origin HEAD:held'
held_tip=$(guest "$uuid" /run/current-system/sw/bin/git rev-parse HEAD)
test "$(tip held)" = "$held_tip"
test "$(tip main)" = "$next"
test "$(count receive)" -eq "$((receives + 2))"

# The discard's admission waits for every lease. Discard keeps the branch.
stage 'discard the session'
plan=$(rpc session.discard "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')")
tag=$(jq -er '.result | select(.plan.allowed==true) | .change_tag' <<< "$plan")
discard=$(rpc session.discard "$(jq -nc --arg uuid "$uuid" --arg tag "$tag" '{uuid:$uuid,key:"source-restart-discard",if_match:$tag}')")
discard_op=$(jq -er '.result.operation.id | select(length==36)' <<< "$discard")
cli operation wait "$discard_op" --json | jq -e '.operation | .kind=="session.discard" and .status=="completed"' >/dev/null
inc list "^p-$uuid$" --format json | jq -e 'length==0' >/dev/null
test "$(tip held)" = "$held_tip"
test "$(driver_pids)" = "$third_driver"
kill -0 "$daemon_pid"
stop_daemon
echo P_SOURCE_DRIVER_RESTART_PASS
