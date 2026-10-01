# shellcheck shell=bash
# Disposable both-absent fault: no real Codex login or external credential.
set -E
umask 077
step_dir="$P_TEST_TMP/step-42"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step42
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
incus_real=$(command -v incus)
daemon_pid=
uuid=
sibling_uuid=
inc() { timeout 90 "$incus_real" --force-local --project user-1000 "$@"; }
rpc() { timeout 45 p api "$socket" "$@"; }
bare_git() { GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null git --git-dir="$bare" "$@"; }
guest() {
  local target="$1"
  shift
  inc exec "p-$target" --user 1000 --group 1000 --cwd /workspace \
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
  for target in "$uuid" "$sibling_uuid"; do
    if test -n "$target"; then
      inc delete --force "p-$target" >/dev/null 2>&1 || true
      rm -rf -- "${endpoint_prefix:?}/${target:?}"
    fi
  done
  rmdir "$endpoint_prefix" 2>/dev/null || true
}
on_error() {
  printf 'P_UNRECOVERABLE_RECORD_FAIL line=%s status=%s\n' "$2" "$1" >&2
  if test -n "${preview:-}"; then
    jq -c '.result.preview | {runtime_status,assigned_ref_status,principal_fingerprint,
      external_authority,eligible,unsafe_reasons}' <<< "$preview" >&2 || true
  fi
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
    if ! kill -0 "$daemon_pid" 2>/dev/null; then echo 'record repair daemon exited' >&2; return 1; fi
    sleep 0.1
  done
  echo 'record repair daemon health unavailable' >&2
  return 1
}
operation_id() { jq -er '.result.operation.id | select(length==36)' <<< "$1"; }
wait_operation() {
  local id="$1" response status deadline=$((SECONDS+180))
  while ((SECONDS < deadline)); do
    response=$(rpc operation.inspect "$(jq -nc --arg id "$id" '{v:1,id:$id}')")
    status=$(jq -er '.result.operation.status' <<< "$response")
    if test "$status" = completed; then printf '%s\n' "$response"; return; fi
    if test "$status" = blocked || test "$status" = failed || test "$status" = unknown; then
      echo "unexpected operation status: $response" >&2; return 1
    fi
    sleep 0.3
  done
  echo "operation $id did not complete" >&2
  return 1
}
wait_ready() {
  local target="$1" deadline=$((SECONDS+90))
  while ((SECONDS < deadline)); do
    if rpc session.inspect "$(jq -nc --arg uuid "$target" '{v:1,uuid:$uuid}')" |
      jq -e '.result.session.session_condition=="ready"' >/dev/null; then return; fi
    sleep 0.3
  done
  echo "session $target did not become ready" >&2
  return 1
}
expect_busy() {
  local response
  if response=$(rpc "$1" "$2" 2> "$step_dir/refused.err"); then
    echo "unexpectedly accepted $1" >&2; return 1
  fi
  jq -e '.error.kind=="busy"' <<< "$response" >/dev/null
}

start_daemon
rpc system.capabilities | jq -e '.result.available |
  (index("session.record.repair.preview") != null and index("session.record.repair.confirm") != null)' >/dev/null
created=$(rpc project.create '{"v":1,"key":"record-bootstrap","project":"unrecoverable-record"}')
uuid=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created")
wait_operation "$(operation_id "$created")" >/dev/null
wait_ready "$uuid"
guest "$uuid" git config user.name P
guest "$uuid" git config user.email p@example.invalid
guest "$uuid" /run/current-system/sw/bin/bash -c \
  'printf retained > tracked; git add tracked; git commit -qm retained; git push origin HEAD:main'
tip=$(guest "$uuid" git rev-parse HEAD)
created=$(rpc session.create '{"v":1,"key":"record-sibling","project":"unrecoverable-record","branch":"sibling","choice":"new","source":"refs/heads/main"}')
sibling_uuid=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created")
wait_operation "$(operation_id "$created")" >/dev/null
wait_ready "$sibling_uuid"
guest "$sibling_uuid" /usr/libexec/p/codex-adapter init
guest "$sibling_uuid" /run/current-system/sw/bin/bash -c \
  'printf sibling-dummy > /home/p/.codex/auth.json; chmod 0600 /home/p/.codex/auth.json'
sibling_key=$(sha256sum "$state/session_keys/$sibling_uuid" | cut -d' ' -f1)
old_key=$(sha256sum "$state/session_keys/$uuid" | cut -d' ' -f1)
test -S "$endpoint_prefix/$uuid/git.sock"
repo_hash=$(printf %s unrecoverable-record | sha256sum | cut -d' ' -f1)
bare="$state/repositories/$repo_hash.git"
test "$(bare_git rev-parse --verify refs/heads/main)" = "$tip"

# Controlled external fault. Leave the sibling ref and P bare object intact.
inc delete --force "p-$uuid"
bare_git update-ref -d refs/heads/main "$tip"
if bare_git rev-parse --verify refs/heads/main >/dev/null 2>&1; then
  echo 'assigned P ref fault failed' >&2; exit 1
fi
inc list "^p-$uuid$" --format json | jq -e 'length==0' >/dev/null
preview=$(rpc session.record.repair.preview "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')")
jq -e --arg uuid "$uuid" '
  .result.preview | .kind=="unrecoverable_session_record" and .session_uuid==$uuid and
  .project=="unrecoverable-record" and .branch=="main" and
  .runtime_status=="missing" and .assigned_ref_status=="missing" and
  .incus_project=="user-1000" and .instance_name==("p-"+$uuid) and
  (.principal_fingerprint|length==64) and .principal_active==true and
  .external_authority=="none_registered" and .unsafe_reasons==[] and
  .eligible==true and (.confirmation_token|test("^[0-9a-f]{32}$"))' <<< "$preview" >/dev/null
token=$(jq -er '.result.preview.confirmation_token' <<< "$preview")
expect_busy session.record.repair.confirm "$(jq -nc --arg uuid "$sibling_uuid" --arg token "$token" \
  '{v:1,key:"record-wrong-session",uuid:$uuid,confirmation_token:$token}')"
# Reappearing assigned ref makes the preview stale before any cleanup.
bare_git update-ref refs/heads/main "$tip" 0000000000000000000000000000000000000000
expect_busy session.record.repair.confirm "$(jq -nc --arg uuid "$uuid" --arg token "$token" \
  '{v:1,key:"record-stale",uuid:$uuid,confirmation_token:$token}')"
test -f "$state/session_keys/$uuid"
bare_git update-ref -d refs/heads/main "$tip"
preview=$(rpc session.record.repair.preview "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')")
token=$(jq -er '.result.preview.confirmation_token' <<< "$preview")
confirmed=$(rpc session.record.repair.confirm "$(jq -nc --arg uuid "$uuid" --arg token "$token" \
  '{v:1,key:"record-confirmed",uuid:$uuid,confirmation_token:$token}')")
repair_op=$(operation_id "$confirmed")
repaired=$(wait_operation "$repair_op")
jq -e '.result.operation | .kind=="session.record.repair" and .phase=="removed" and
  .status=="completed" and .committed==true' <<< "$repaired" >/dev/null
test ! -e "$state/session_keys/$uuid"
test ! -e "$endpoint_prefix/$uuid"
if absent_row=$(rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" 2> "$step_dir/absent-row.err"); then
  echo 'unrecoverable session row survived repair' >&2; exit 1
fi
jq -e '.error.kind=="unavailable"' <<< "$absent_row" >/dev/null
test "$(sha256sum "$state/session_keys/$sibling_uuid" | cut -d' ' -f1)" = "$sibling_key"
test "$old_key" != "$sibling_key"
inc list "^p-$uuid$" --format json | jq -e 'length==0' >/dev/null
if bare_git rev-parse --verify refs/heads/main >/dev/null 2>&1; then
  echo 'record repair recreated assigned P ref' >&2; exit 1
fi
test "$(bare_git rev-parse --verify refs/heads/sibling)" = "$tip"
test "$(guest "$sibling_uuid" cat /home/p/.codex/auth.json)" = sibling-dummy
inc image list --format json | jq -e --arg image "$base_image" 'any(.[]; .fingerprint==$image)' >/dev/null
test "$(rpc session.record.repair.confirm "$(jq -nc --arg uuid "$uuid" --arg token "$token" \
  '{v:1,key:"record-confirmed",uuid:$uuid,confirmation_token:$token}')" |
  jq -er '.result.operation.id')" = "$repair_op"
stop_daemon
start_daemon
rpc operation.inspect "$(jq -nc --arg id "$repair_op" '{v:1,id:$id}')" |
  jq -e '.result.operation | .status=="completed" and .phase=="removed"' >/dev/null
test "$(bare_git rev-parse --verify refs/heads/sibling)" = "$tip"
test "$(guest "$sibling_uuid" cat /home/p/.codex/auth.json)" = sibling-dummy
echo P_UNRECOVERABLE_RECORD_PASS
