# shellcheck shell=bash
# Explicit public repair after a fixture-caused runtime loss. No login occurs.
set -E
umask 077
step_dir="$P_TEST_TMP/step-34"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step34
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
daemon_pid=
uuid=
uuid_b=
incus_real=$(command -v incus)

inc() { timeout 60 "$incus_real" --force-local --project user-1000 "$@"; }
rpc() { timeout 35 p api "$socket" "$@"; }
guest() {
  inc exec "p-$uuid" --user 1000 --group 1000 --cwd /workspace \
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
  if test -n "$uuid"; then
    inc delete --force "p-$uuid" >/dev/null 2>&1 || true
    rm -rf -- "${endpoint_prefix:?}/${uuid:?}"
  fi
  if test -n "$uuid_b"; then
    inc delete --force "p-$uuid_b" >/dev/null 2>&1 || true
    rm -rf -- "${endpoint_prefix:?}/${uuid_b:?}"
  fi
  rmdir "$endpoint_prefix" 2>/dev/null || true
}
on_error() {
  printf 'P_MISSING_RUNTIME_REPAIR_FAIL line=%s status=%s\n' "$2" "$1" >&2
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
activate "$P_TEST_SOURCE/plugins/bundled/codex-adapter" "$step_dir/agent.json" agent.status.report
jq -s '{schema:"p.activation/v1",plugins:([.[0].plugins[0],.[1].plugins[0]])}' \
  "$step_dir/runtime-one.json" "$step_dir/host-one.json" > "$step_dir/runtime.json"

base_image=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
test "${#base_image}" -eq 64
jq -n --arg state "$state" --arg git_activation "$step_dir/git.json" \
  --arg git_id "$(jq -er '.plugins[0].id' "$step_dir/git.json")" \
  --arg runtime_activation "$step_dir/runtime.json" \
  --arg runtime_id "$(jq -er '.plugins[0].id' "$step_dir/runtime.json")" \
  --arg host_id "$(jq -er '.plugins[1].id' "$step_dir/runtime.json")" \
  --arg agent_activation "$step_dir/agent.json" \
  --arg agent_id "$(jq -er '.plugins[0].id' "$step_dir/agent.json")" \
  --arg incus_binary "$incus_real" --arg prefix "$endpoint_prefix" --arg image "$base_image" \
  '{schema:"p.host/v1",state_dir:$state,
    git:{activation_path:$git_activation,source_plugin_id:$git_id,listen:"127.0.0.1:0"},
    runtime:{activation_path:$runtime_activation,runtime_plugin_id:$runtime_id,
      host_plugin_id:$host_id,
      agent_adapter:{activation_path:$agent_activation,plugin_id:$agent_id},
      incus_binary:$incus_binary,
      incus_user_socket:"/var/lib/incus/unix.socket.user",incus_project:"user-1000",
      endpoint_prefix:$prefix,disk_source_ceilings:["/var/lib/p-vm/endpoints","/var/lib/p-vm/grants"],
      base_image_fingerprint:$image,
      project_policy:{network:"none",filesystem_mounts:[],command:["/run/current-system/sw/bin/bash"]}}}' > "$step_dir/host.json"
bash "$P_TEST_SOURCE/tests/integration/public-egress-host-config.sh" "$step_dir/host.json"
start_daemon() {
  p daemon "$step_dir/host.json" >> "$step_dir/daemon.out" 2>> "$step_dir/daemon.err" &
  daemon_pid=$!
  local deadline=$((SECONDS+30))
  while ((SECONDS < deadline)); do
    if test -S "$socket" && rpc system.health | jq -e '.result.control_state=="ready"' >/dev/null 2>&1; then return; fi
    if ! kill -0 "$daemon_pid" 2>/dev/null; then echo 'repair daemon exited' >&2; return 1; fi
    sleep 0.1
  done
  echo 'repair daemon health unavailable' >&2
  return 1
}
wait_operation() {
  local id="$1" result status deadline=$((SECONDS+150))
  while ((SECONDS < deadline)); do
    result=$(rpc operation.inspect "$(jq -nc --arg id "$id" '{v:1,id:$id}')")
    status=$(jq -er '.result.operation.status' <<< "$result")
    if test "$status" = completed; then printf '%s\n' "$result"; return; fi
    if test "$status" = blocked || test "$status" = failed || test "$status" = superseded; then
      echo "unexpected operation status: $result" >&2; return 1
    fi
    sleep 0.3
  done
  echo "operation $id did not complete" >&2
  return 1
}
wait_ready() {
  local id="$1" deadline=$((SECONDS+80))
  while ((SECONDS < deadline)); do
    if rpc session.inspect "$(jq -nc --arg uuid "$id" '{v:1,uuid:$uuid}')" |
      jq -e '.result.session.session_condition=="ready"' >/dev/null; then return; fi
    sleep 0.3
  done
  echo "session $id did not become ready" >&2
  return 1
}
start_daemon
rpc system.capabilities | jq -e '.result.available |
  (index("session.repair.preview") != null and index("session.repair.confirm") != null)' >/dev/null
created=$(rpc project.create '{"v":1,"key":"repair-bootstrap","project":"missing-runtime-repair"}')
create_op=$(jq -er '.result.operation.id | select(length==36)' <<< "$created")
uuid=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created")
wait_operation "$create_op" >/dev/null
wait_ready "$uuid"
guest /run/current-system/sw/bin/git config user.name P
guest /run/current-system/sw/bin/git config user.email p@example.invalid
guest /run/current-system/sw/bin/bash -c \
  'printf retained > tracked; git add tracked; git commit -qm retained; git push origin HEAD:main'
main_oid=$(guest /run/current-system/sw/bin/git rev-parse HEAD)
created_b=$(rpc session.create '{"v":1,"key":"repair-sibling","project":"missing-runtime-repair","branch":"sibling","choice":"new","source":"refs/heads/main"}')
create_b_op=$(jq -er '.result.operation.id | select(length==36)' <<< "$created_b")
uuid_b=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created_b")
wait_operation "$create_b_op" >/dev/null
wait_ready "$uuid_b"
key_digest=$(sha256sum "$state/session_keys/$uuid" | cut -d' ' -f1)
guest /usr/libexec/p/codex-adapter init
guest /run/current-system/sw/bin/bash -c \
  'printf lost-dummy > /home/p/.codex/auth.json; chmod 0600 /home/p/.codex/auth.json; printf lost-local > lost-local'
inc exec "p-$uuid_b" --user 1000 --group 1000 --cwd /workspace --env HOME=/home/p -- \
  /usr/libexec/p/codex-adapter init
inc exec "p-$uuid_b" --user 1000 --group 1000 --cwd /workspace --env HOME=/home/p -- \
  /run/current-system/sw/bin/bash -c 'printf sibling-dummy > /home/p/.codex/auth.json; chmod 0600 /home/p/.codex/auth.json'

# This fixture-only external fault removes A's native runtime. Public repair
# must not silently recreate it through Start or disturb B.
inc delete --force "p-$uuid"
inc list "^p-$uuid$" --format json | jq -e 'length==0' >/dev/null
if start_reply=$(rpc session.start "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" 2> "$step_dir/start-missing.err"); then
  jq -e '.result.session.session_condition=="missing"' <<< "$start_reply" >/dev/null
fi
inc list "^p-$uuid$" --format json | jq -e 'length==0' >/dev/null
preview=$(rpc session.repair.preview "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')")
jq -e --arg uuid "$uuid" --arg tip "$main_oid" --arg image "$base_image" '
  .result.preview | .kind=="missing_runtime" and .session_uuid==$uuid and
  .branch=="main" and .assigned_tip==$tip and .image_fingerprint==$image and
  (.image_source_commit // "")=="" and .image_source_differs==true and
  .image_status=="present" and .runtime_local_loss=="unrecoverable" and
  .eligible==true and (.policy_sha256|length==64) and
  .incus_project=="user-1000" and .instance_name==("p-"+$uuid) and
  (.credential_fingerprint|length==64) and
  (.confirmation_token|test("^[0-9a-f]{32}$"))
' <<< "$preview" >/dev/null
token=$(jq -er '.result.preview.confirmation_token' <<< "$preview")
confirmed=$(rpc session.repair.confirm "$(jq -nc --arg uuid "$uuid" --arg token "$token" \
  '{v:1,key:"repair-confirmed",uuid:$uuid,confirmation_token:$token}')")
repair_op=$(jq -er '.result.operation.id | select(length==36)' <<< "$confirmed")
done_op=$(wait_operation "$repair_op")
jq -e '.result.operation | .kind=="session.repair" and .status=="completed" and .phase=="completed"' <<< "$done_op" >/dev/null
wait_ready "$uuid"
inc list "^p-$uuid$" --format json | jq -e 'length==1 and .[0].status=="Running"' >/dev/null
test "$(guest /run/current-system/sw/bin/git rev-parse HEAD)" = "$main_oid"
test "$(guest /run/current-system/sw/bin/git branch --show-current)" = main
test "$(guest /run/current-system/sw/bin/cat tracked)" = retained
guest /run/current-system/sw/bin/bash -c 'test ! -e /home/p/.codex/auth.json && test ! -e /workspace/lost-local'
test "$(sha256sum "$state/session_keys/$uuid" | cut -d' ' -f1)" = "$key_digest"
test -S "$endpoint_prefix/$uuid/git.sock"
test "$(rpc session.repair.confirm "$(jq -nc --arg uuid "$uuid" --arg token "$token" \
  '{v:1,key:"repair-confirmed",uuid:$uuid,confirmation_token:$token}')" |
  jq -er '.result.operation.id')" = "$repair_op"
rpc project.branches '{"v":1,"project":"missing-runtime-repair","limit":8}' |
  jq -e --arg tip "$main_oid" '.result.refs | length==2 and
    any(.[]; .ref=="refs/heads/main" and .oid==$tip) and
    any(.[]; .ref=="refs/heads/sibling" and .oid==$tip)' >/dev/null
test "$(inc exec "p-$uuid_b" --user 1000 --group 1000 --cwd /workspace --env HOME=/home/p -- \
  /run/current-system/sw/bin/cat /home/p/.codex/auth.json)" = sibling-dummy
stop_daemon
start_daemon
wait_ready "$uuid"
test "$(guest /run/current-system/sw/bin/git rev-parse HEAD)" = "$main_oid"
test "$(sha256sum "$state/session_keys/$uuid" | cut -d' ' -f1)" = "$key_digest"
echo P_MISSING_RUNTIME_REPAIR_PASS
