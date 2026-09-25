# shellcheck shell=bash
# Exact trusted project policy, immutable session snapshots and restart drift.
set -E
umask 077
step_dir="$P_TEST_TMP/step-35"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step35
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
daemon_pid=
uuid_a=
uuid_b=
uuid_new=
incus_real=$(command -v incus)

inc() { timeout 60 "$incus_real" --force-local --project user-1000 "$@"; }
rpc() { timeout 35 p api "$socket" "$@"; }
guest_a() {
  inc exec "p-$uuid_a" --user 1000 --group 1000 --cwd /workspace \
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
  stop_daemon
  for id in "$uuid_a" "$uuid_b" "$uuid_new"; do
    if test -n "$id"; then
      inc delete --force "p-$id" >/dev/null 2>&1 || true
      rm -rf -- "${endpoint_prefix:?}/$id"
    fi
  done
  rmdir "$endpoint_prefix" 2>/dev/null || true
}
on_error() {
  printf 'P_PROJECT_POLICY_FAIL line=%s status=%s\n' "$2" "$1" >&2
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
base_image=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
test "${#base_image}" -eq 64
jq -n --arg state "$state" --arg git_activation "$step_dir/git.json" \
  --arg git_id "$(jq -er '.plugins[0].id' "$step_dir/git.json")" \
  --arg runtime_activation "$step_dir/runtime.json" \
  --arg runtime_id "$(jq -er '.plugins[0].id' "$step_dir/runtime.json")" \
  --arg host_id "$(jq -er '.plugins[1].id' "$step_dir/runtime.json")" \
  --arg incus_binary "$incus_real" --arg prefix "$endpoint_prefix" --arg image "$base_image" \
  '{schema:"p.host/v1",state_dir:$state,
    git:{activation_path:$git_activation,source_plugin_id:$git_id,listen:"127.0.0.1:0"},
    runtime:{activation_path:$runtime_activation,runtime_plugin_id:$runtime_id,
      host_plugin_id:$host_id,incus_binary:$incus_binary,
      incus_user_socket:"/var/lib/incus/unix.socket.user",incus_project:"user-1000",
      endpoint_prefix:$prefix,disk_source_ceilings:["/var/lib/p-vm/endpoints","/var/lib/p-vm/grants"],
      base_image_fingerprint:$image,
      project_policies:{
        "policy-a":{network:"none",filesystem_mounts:[],command:["/run/current-system/sw/bin/bash"]},
        "policy-b":{network:"none",command:["/run/current-system/sw/bin/bash"]}
      }}}' > "$step_dir/host.json"
bash "$P_TEST_SOURCE/tests/integration/public-egress-host-config.sh" "$step_dir/host.json"
start_daemon() {
  p daemon "$step_dir/host.json" >> "$step_dir/daemon.out" 2>> "$step_dir/daemon.err" &
  daemon_pid=$!
  local deadline=$((SECONDS+30))
  while ((SECONDS < deadline)); do
    if test -S "$socket" && rpc system.health | jq -e '.result.control_state=="ready"' >/dev/null 2>&1; then return; fi
    if ! kill -0 "$daemon_pid" 2>/dev/null; then echo 'policy daemon exited' >&2; return 1; fi
    sleep 0.1
  done
  echo 'policy daemon health unavailable' >&2
  return 1
}
wait_operation() {
  local id="$1" result status deadline=$((SECONDS+150))
  while ((SECONDS < deadline)); do
    result=$(rpc operation.inspect "$(jq -nc --arg id "$id" '{v:1,id:$id}')")
    status=$(jq -er '.result.operation.status' <<< "$result")
    if test "$status" = completed; then return; fi
    if test "$status" = blocked || test "$status" = failed || test "$status" = superseded; then
      echo "unexpected operation status: $result" >&2; return 1
    fi
    sleep 0.3
  done
  echo "operation $id did not complete" >&2
  return 1
}
inspect() { rpc session.inspect "$(jq -nc --arg uuid "$1" '{v:1,uuid:$uuid}')"; }
wait_ready() {
  local id="$1" deadline=$((SECONDS+80))
  while ((SECONDS < deadline)); do
    if inspect "$id" | jq -e '.result.session.session_condition=="ready"' >/dev/null; then return; fi
    sleep 0.3
  done
  echo "session $id did not become ready" >&2
  return 1
}
update_config() {
  local filter="$1"
  jq "$filter" "$step_dir/host.json" > "$step_dir/host.next"
  mv -- "$step_dir/host.next" "$step_dir/host.json"
}
start_daemon
if rpc project.create '{"v":1,"key":"unconfigured-project","project":"policy-c"}' > "$step_dir/unconfigured.out" 2>&1; then
  echo 'unconfigured exact project accepted' >&2
  exit 1
fi
created_a=$(rpc project.create '{"v":1,"key":"policy-a-bootstrap","project":"policy-a"}')
op_a=$(jq -er '.result.operation.id | select(length==36)' <<< "$created_a")
uuid_a=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created_a")
wait_operation "$op_a"
wait_ready "$uuid_a"
created_b=$(rpc project.create '{"v":1,"key":"policy-b-bootstrap","project":"policy-b"}')
op_b=$(jq -er '.result.operation.id | select(length==36)' <<< "$created_b")
uuid_b=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created_b")
wait_operation "$op_b"
wait_ready "$uuid_b"
old_runtime_config=$(inc file pull "p-$uuid_a/etc/p/session.json" -)
jq -e '.command==["/run/current-system/sw/bin/bash"]' <<< "$old_runtime_config" >/dev/null
test "$(inc exec "p-$uuid_a" -- /run/current-system/sw/bin/stat -c '%u:%g:%a' /etc/p/session.json)" = 0:0:644
old_sha=$(inspect "$uuid_a" | jq -er '.result.session.policy_sha256 | select(length==64)')
inspect "$uuid_a" | jq -e '.result.session.policy_condition=="current"' >/dev/null
inspect "$uuid_b" | jq -e '.result.session.policy_condition=="current"' >/dev/null
guest_a /run/current-system/sw/bin/git config user.name P
guest_a /run/current-system/sw/bin/git config user.email p@example.invalid
guest_a /run/current-system/sw/bin/bash -c \
  'printf main > tracked; git add tracked; git commit -qm main; git push origin HEAD:main'
main_oid=$(guest_a /run/current-system/sw/bin/git rev-parse HEAD)

stop_daemon
update_config '.runtime.project_policies["policy-a"].command=["/run/current-system/sw/bin/bash","-l"]'
start_daemon
inspect "$uuid_a" | jq -e --arg old "$old_sha" '
  .result.session | .policy_sha256==$old and .policy_condition=="outdated"' >/dev/null
inspect "$uuid_b" | jq -e '.result.session.policy_condition=="current"' >/dev/null
test "$(inc file pull "p-$uuid_a/etc/p/session.json" -)" = "$old_runtime_config"
created_new=$(rpc session.create '{"v":1,"key":"policy-a-new","project":"policy-a","branch":"work","choice":"new","source":"refs/heads/main"}')
new_op=$(jq -er '.result.operation.id | select(length==36)' <<< "$created_new")
uuid_new=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created_new")
wait_operation "$new_op"
wait_ready "$uuid_new"
inspect "$uuid_new" | jq -e --arg old "$old_sha" '
  .result.session | .policy_condition=="current" and .policy_sha256!=$old' >/dev/null
inc file pull "p-$uuid_new/etc/p/session.json" - |
  jq -e '.command==["/run/current-system/sw/bin/bash","-l"]' >/dev/null
test "$(inc exec "p-$uuid_new" --user 1000 --group 1000 --cwd /workspace --env HOME=/home/p -- \
  /run/current-system/sw/bin/git rev-parse HEAD)" = "$main_oid"
rpc session.stop "$(jq -nc --arg uuid "$uuid_a" '{v:1,uuid:$uuid}')" |
  jq -e '.result.session.session_condition=="stopped"' >/dev/null
stop_daemon
update_config 'del(.runtime.project_policies["policy-a"])'
start_daemon
inspect "$uuid_a" | jq -e '.result.session.policy_condition=="invalid"' >/dev/null
inspect "$uuid_new" | jq -e '.result.session.policy_condition=="invalid"' >/dev/null
inspect "$uuid_b" | jq -e '.result.session.policy_condition=="current"' >/dev/null
if rpc session.start "$(jq -nc --arg uuid "$uuid_a" '{v:1,uuid:$uuid}')" > "$step_dir/invalid-start.out" 2>&1; then
  echo 'Start accepted missing trusted project policy' >&2
  exit 1
fi
inc list "^p-$uuid_a$" --format json | jq -e 'length==1 and .[0].status=="Stopped"' >/dev/null
stop_daemon
update_config '.runtime.project_policies["policy-a"]={network:"none",filesystem_mounts:[],command:["/run/current-system/sw/bin/bash","-l"]}'
start_daemon
inspect "$uuid_a" | jq -e --arg old "$old_sha" '
  .result.session | .policy_sha256==$old and .policy_condition=="outdated"' >/dev/null
inspect "$uuid_new" | jq -e '.result.session.policy_condition=="current"' >/dev/null
inspect "$uuid_b" | jq -e '.result.session.policy_condition=="current"' >/dev/null
echo P_PROJECT_POLICY_PASS
