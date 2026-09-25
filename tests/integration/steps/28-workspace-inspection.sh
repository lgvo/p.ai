# shellcheck shell=bash
# Read-only WorkspaceOperation: stopped/frozen export, isolated Git helper,
# hostile repository refusal, and durable recovery after an owned freeze.
set -E
umask 077
step_dir="$P_TEST_TMP/step-28"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step28
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
daemon_pid=
uuid=
op_ids=()
incus_real=$(command -v incus)
rm_real=$(command -v rm)
sleep_real=$(command -v sleep)

inc() { timeout 60 "$incus_real" --force-local --project user-1000 "$@"; }
rpc() { timeout 35 p api "$socket" "$@"; }
guest() {
  inc exec "p-$uuid" --user 1000 --group 1000 --cwd /workspace --env HOME=/home/p -- "$@"
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
  stop_daemon
  if test -n "$uuid"; then
    inc resume "p-$uuid" >/dev/null 2>&1 || true
    inc delete --force "p-$uuid" >/dev/null 2>&1 || true
    rm -rf -- "${endpoint_prefix:?}/${uuid:?}"
  fi
  for op in "${op_ids[@]}"; do
    inc delete --force "p-workspace-$op" >/dev/null 2>&1 || true
  done
  rmdir "$endpoint_prefix" 2>/dev/null || true
}
on_error() {
  printf 'P_WORKSPACE_INSPECTION_FAIL line=%s status=%s\n' "$2" "$1" >&2
  for op in "${op_ids[@]}"; do
    rpc operation.inspect "$(jq -nc --arg id "$op" '{v:1,id:$id}')" 2>/dev/null |
      jq -cr '.result.operation | {id,status,phase,diagnostic:(.diagnostic // "")[:512]}' >&2 || true
  done
  tail -c 3072 "$step_dir/daemon.err" >&2 || true
}
trap cleanup EXIT
trap 'on_error "$?" "$LINENO"' ERR

activate() {
  local package="$1" target="$2" grant="$3" digest id
  digest=$(p plugins conformance "$package" | jq -er '.sha256 | select(length==64)')
  id=$(jq -er '.id' "$package/plugin.json")
  jq -n --arg path "$package" --arg digest "$digest" --arg id "$id" --arg grant "$grant" \
    '{schema:"p.activation/v1",plugins:[{id:$id,path:$path,sha256:$digest,grants:[$grant],config:{}}]}' > "$target"
}
activate "$P_TEST_GIT_PLUGIN" "$step_dir/git.json" git.project
activate "$P_TEST_RUNTIME_PLUGIN" "$step_dir/runtime-one.json" runtime.incus
activate "$P_TEST_SOURCE/plugins/bundled/tmux-host" "$step_dir/host-one.json" session.asset.install
jq -s '{schema:"p.activation/v1",plugins:([.[0].plugins[0],.[1].plugins[0]])}' \
  "$step_dir/runtime-one.json" "$step_dir/host-one.json" > "$step_dir/runtime.json"

# The wrapper forwards every normal call. Its one-use pause cutover performs
# the real Incus pause, then kills only this fixture daemon before it can
# advance SQLite. A separate recovery gate holds only operation inventory so
# Start/Attach can be tested while the durable freeze-intent is unresolved.
cat > "$step_dir/incus-wrapper" <<EOF
#!/bin/sh
set -eu
if test "\${4-}" = pause && test -f '$step_dir/cutover'; then
  '$incus_real' "\$@"
  '$rm_real' -f -- '$step_dir/cutover'
  printf 'paused\\n' > '$step_dir/cutover-done'
  read -r daemon_pid < '$step_dir/daemon.pid'
  kill -KILL "\$daemon_pid"
  exit 1
fi
if test "\${4-}" = operation && test "\${5-}" = list && test -f '$step_dir/recovery-gate'; then
  printf 'waiting\\n' > '$step_dir/recovery-waiting'
  n=0
  while test -f '$step_dir/recovery-gate' && test "\$n" -lt 450; do
    '$sleep_real' 0.1
    n=\$((n + 1))
  done
  test ! -f '$step_dir/recovery-gate' || exit 124
fi
exec '$incus_real' "\$@"
EOF
chmod 0700 "$step_dir/incus-wrapper"

base=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
test "${#base}" -eq 64
jq -n --arg state "$state" --arg git_activation "$step_dir/git.json" \
  --arg git_id "$(jq -er '.plugins[0].id' "$step_dir/git.json")" \
  --arg runtime_activation "$step_dir/runtime.json" \
  --arg runtime_id "$(jq -er '.plugins[0].id' "$step_dir/runtime.json")" \
  --arg host_id "$(jq -er '.plugins[1].id' "$step_dir/runtime.json")" \
  --arg incus_binary "$step_dir/incus-wrapper" --arg prefix "$endpoint_prefix" --arg image "$base" \
  '{schema:"p.host/v1",state_dir:$state,
    git:{activation_path:$git_activation,source_plugin_id:$git_id,listen:"127.0.0.1:0"},
    runtime:{activation_path:$runtime_activation,runtime_plugin_id:$runtime_id,
      host_plugin_id:$host_id,incus_binary:$incus_binary,
      incus_user_socket:"/var/lib/incus/unix.socket.user",incus_project:"user-1000",
      endpoint_prefix:$prefix,disk_source_ceilings:["/var/lib/p-vm/endpoints","/var/lib/p-vm/grants"],
      base_image_fingerprint:$image,
      project_policy:{network:"none",filesystem_mounts:[],command:["/run/current-system/sw/bin/bash"]}}}' > "$step_dir/host.json"
bash "$P_TEST_SOURCE/tests/integration/public-egress-host-config.sh" "$step_dir/host.json"
start_daemon() {
  p daemon "$step_dir/host.json" >> "$step_dir/daemon.out" 2>> "$step_dir/daemon.err" &
  daemon_pid=$!
  printf '%s\n' "$daemon_pid" > "$step_dir/daemon.pid"
  local deadline=$((SECONDS+30))
  while ((SECONDS < deadline)); do
    if test -S "$socket" && rpc system.health | jq -e '.result.control_state=="ready"' >/dev/null 2>&1; then return; fi
    if ! kill -0 "$daemon_pid" 2>/dev/null; then echo 'workspace daemon exited' >&2; return 1; fi
    sleep 0.1
  done
  echo 'workspace daemon health unavailable' >&2
  return 1
}
start_daemon
rpc system.capabilities | jq -e '.result.available | index("workspace.inspect") != null' >/dev/null

created=$(rpc project.create '{"v":1,"key":"workspace-bootstrap","project":"workspace-read"}')
create_op=$(jq -er '.result.operation.id | select(length==36)' <<< "$created")
uuid=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created")
wait_operation() {
  local id="$1" want="$2" result status deadline=$((SECONDS+150))
  while ((SECONDS < deadline)); do
    result=$(rpc operation.inspect "$(jq -nc --arg id "$id" '{v:1,id:$id}')")
    status=$(jq -er '.result.operation.status' <<< "$result")
    if test "$status" = "$want"; then printf '%s\n' "$result"; return; fi
    if test "$status" = blocked || test "$status" = failed || test "$status" = completed; then
      echo "unexpected operation status: $result" >&2; return 1
    fi
    sleep 0.3
  done
  echo "operation $id did not reach $want" >&2
  return 1
}
wait_operation "$create_op" completed >/dev/null
deadline=$((SECONDS+80))
while ((SECONDS < deadline)); do
  if rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
    jq -e '.result.session.session_condition=="ready"' >/dev/null; then break; fi
  sleep 0.3
done
rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
  jq -e '.result.session.session_condition=="ready"' >/dev/null
# The pinned base must not route SFTP READDIR's UID/GID display-name lookups
# to a process frozen with the source. Inspect generated guest state before
# the first workspace freeze; do not stop or mask a live session service.
inc file pull "p-$uuid/etc/nsswitch.conf" "$step_dir/nsswitch.conf"
grep -Eq '^passwd:[[:space:]]+files[[:space:]]*$' "$step_dir/nsswitch.conf"
grep -Eq '^group:[[:space:]]+files[[:space:]]*$' "$step_dir/nsswitch.conf"
grep -Eq '^shadow:[[:space:]]+files[[:space:]]*$' "$step_dir/nsswitch.conf"
grep -Eq '^hosts:[[:space:]]+files[[:space:]]+dns[[:space:]]*$' "$step_dir/nsswitch.conf"
test "$(guest /run/current-system/sw/bin/id -u root)" = 0
test "$(guest /run/current-system/sw/bin/id -u p)" = 1000
test "$(guest /run/current-system/sw/bin/id -u nixbld1)" -gt 1000
inc exec "p-$uuid" -- /run/current-system/sw/bin/test ! -e /run/nscd/socket
inc exec "p-$uuid" -- /run/current-system/sw/bin/test ! -L /run/nscd/socket
nscd_unit=$(inc exec "p-$uuid" -- /run/current-system/sw/bin/systemctl show nscd.service \
  --no-pager -p LoadState -p ActiveState)
grep -Fxq 'LoadState=not-found' <<< "$nscd_unit"
grep -Fxq 'ActiveState=inactive' <<< "$nscd_unit"
guest /run/current-system/sw/bin/git config user.name P
guest /run/current-system/sw/bin/git config user.email p@example.invalid
guest /run/current-system/sw/bin/bash -c 'printf tracked > tracked'
guest /run/current-system/sw/bin/git add tracked
guest /run/current-system/sw/bin/git commit -qm initial
head_oid=$(guest /run/current-system/sw/bin/git rev-parse HEAD)

inspect_workspace() {
  local key="$1" response id
  response=$(rpc workspace.inspect "$(jq -nc --arg key "$key" --arg uuid "$uuid" '{v:1,key:$key,uuid:$uuid}')")
  id=$(jq -er '.result.operation.id | select(length==36)' <<< "$response")
  op_ids+=("$id")
  new_workspace_op=$id
}
source_status() {
  inc list "^p-$uuid$" --format json | jq -er --arg name "p-$uuid" '. | select(length==1 and .[0].name==$name) | .[0].status'
}
assert_helper_absent() {
  inc list "^p-workspace-$1$" --format json | jq -e 'length==0' >/dev/null
}
inspect_workspace workspace-running
running_op=$new_workspace_op
running_result=$(wait_operation "$running_op" completed)
jq -e --arg oid "$head_oid" '.result.operation.evidence.result |
  .branch=="main" and .head_oid==$oid and (.changes|length)==0 and
  any(.refs[]; .name=="refs/heads/main" and .oid==$oid)' <<< "$running_result" >/dev/null
test "$(source_status)" = Running
assert_helper_absent "$running_op"

rpc session.stop "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
  jq -e '.result.session.session_condition=="stopped"' >/dev/null
test "$(source_status)" = Stopped
inspect_workspace workspace-stopped
stopped_op=$new_workspace_op
stopped_result=$(wait_operation "$stopped_op" completed)
jq -e --arg oid "$head_oid" '.result.operation.evidence.result |
  .branch=="main" and .head_oid==$oid and (.changes|length)==0' <<< "$stopped_result" >/dev/null
test "$(source_status)" = Stopped
assert_helper_absent "$stopped_op"

rpc session.start "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" >/dev/null
deadline=$((SECONDS+80))
while ((SECONDS < deadline)); do
  if rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
    jq -e '.result.session.session_condition=="ready"' >/dev/null; then break; fi
  sleep 0.3
done
rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
  jq -e '.result.session.session_condition=="ready"' >/dev/null
test "$(source_status)" = Running

# Original repository controls are never executed inside the helper. An
# include, fsmonitor, and hook all point to a retained sentinel script.
printf '#!/bin/sh\nprintf executed > /home/p/git-control-ran\n' > "$step_dir/evil"
inc file push --uid 1000 --gid 1000 --mode 0755 "$step_dir/evil" "p-$uuid/home/p/evil"
inc file pull "p-$uuid/workspace/.git/config" "$step_dir/config-good"
guest /run/current-system/sw/bin/bash -c \
  'printf "\n[include]\n path = /home/p/evil\n[core]\n fsmonitor = /home/p/evil\n" >> .git/config'
inc file push --uid 1000 --gid 1000 --mode 0755 "$step_dir/evil" "p-$uuid/workspace/.git/hooks/post-index-change"
inspect_workspace workspace-hostile-git
hostile_op=$new_workspace_op
hostile_result=$(wait_operation "$hostile_op" failed)
jq -e '.result.operation.diagnostic | test("Git|config|unsupported")' <<< "$hostile_result" >/dev/null
guest /run/current-system/sw/bin/test ! -e /home/p/git-control-ran
test "$(source_status)" = Running
assert_helper_absent "$hostile_op"
inc file push --uid 1000 --gid 1000 --mode 0644 "$step_dir/config-good" "p-$uuid/workspace/.git/config"
guest /run/current-system/sw/bin/ln -s /etc/shadow /workspace/escaped
inspect_workspace workspace-escape
escape_op=$new_workspace_op
escape_result=$(wait_operation "$escape_op" failed)
jq -e '.result.operation.diagnostic | test("symlink|escape")' <<< "$escape_result" >/dev/null
test "$(source_status)" = Running
assert_helper_absent "$escape_op"
guest /run/current-system/sw/bin/rm /workspace/escaped
guest /run/current-system/sw/bin/test ! -e /home/p/git-control-ran

# The source is actually frozen before the daemon dies. Recovery is held at
# the native operation inventory, so both Start and Attach must reject from
# the durable workspace guard rather than daemon-local memory.
: > "$step_dir/cutover"
inspect_workspace workspace-crash-recovery
crash_op=$new_workspace_op
deadline=$((SECONDS+80))
while ((SECONDS < deadline)); do
  if test -f "$step_dir/cutover-done" && ! kill -0 "$daemon_pid" 2>/dev/null; then break; fi
  sleep 0.2
done
test -f "$step_dir/cutover-done"
test "$(source_status)" = Frozen
wait "$daemon_pid" 2>/dev/null || true
daemon_pid=
: > "$step_dir/recovery-gate"
start_daemon
deadline=$((SECONDS+30))
while ((SECONDS < deadline)); do
  if test -f "$step_dir/recovery-waiting"; then break; fi
  sleep 0.2
done
test -f "$step_dir/recovery-waiting"
helper_name="p-workspace-$crash_op"
inc list "^$helper_name$" --format json | jq -e --arg name "$helper_name" \
  'length==1 and .[0].name==$name and .[0].status=="Running" and
   (.[0].devices|length==0) and
   (.[0].expanded_devices|keys)==["root"] and
   .[0].expanded_devices.root.type=="disk" and
   .[0].expanded_devices.root.path=="/" and
   .[0].config["limits.cpu"]=="1" and
   .[0].config["limits.memory"]=="768MiB" and
   .[0].config["limits.processes"]=="256" and
   .[0].expanded_config["limits.cpu"]=="1" and
   .[0].expanded_config["limits.memory"]=="768MiB" and
   .[0].expanded_config["limits.processes"]=="256" and
   .[0].expanded_config["security.nesting"]!="true"' >/dev/null
helper_unit=$(inc exec "$helper_name" -- /run/current-system/sw/bin/systemctl show p-interactive.service \
  --no-pager -p LoadState -p ActiveState)
test "$(wc -l <<< "$helper_unit")" -eq 2
grep -Fxq 'LoadState=not-found' <<< "$helper_unit"
grep -Fxq 'ActiveState=inactive' <<< "$helper_unit"
for forbidden in /etc/p/session.json /etc/p/workspace.json /etc/p/git /etc/p/devshell \
  /opt/p/endpoints /run/p-interactive/tmux.sock /home/p/.ssh; do
  inc exec "$helper_name" -- /run/current-system/sw/bin/test ! -e "$forbidden"
  inc exec "$helper_name" -- /run/current-system/sw/bin/test ! -L "$forbidden"
done
start_refusal=$(rpc session.start "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" || true)
jq -e '.error.kind=="busy" and .error.code==-32003' <<< "$start_refusal" >/dev/null
attach_refusal=$(rpc session.attach "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" || true)
jq -e '.error.kind=="busy" and .error.code==-32003' <<< "$attach_refusal" >/dev/null
rm -f -- "$step_dir/recovery-gate"
crash_result=$(wait_operation "$crash_op" completed)
jq -e --arg oid "$head_oid" '.result.operation.evidence.result |
  .branch=="main" and .head_oid==$oid' <<< "$crash_result" >/dev/null
test "$(source_status)" = Running
assert_helper_absent "$crash_op"
guest /run/current-system/sw/bin/test ! -e /home/p/git-control-ran
printf 'P_WORKSPACE_INSPECTION_PASS\n'
