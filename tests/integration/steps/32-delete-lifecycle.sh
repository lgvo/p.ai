# shellcheck shell=bash
# Confirmed public Delete with guarded P ref and runtime cleanup.
# All files are disposable fixtures; no authentication is used.
set -E
umask 077
step_dir="$P_TEST_TMP/step-32"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step32
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
daemon_pid=
uuid=
uuid_b=
op_ids=()
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
    inc resume "p-$uuid" >/dev/null 2>&1 || true
    inc delete --force "p-$uuid" >/dev/null 2>&1 || true
    rm -rf -- "${endpoint_prefix:?}/${uuid:?}"
  fi
  if test -n "$uuid_b"; then
    inc delete --force "p-$uuid_b" >/dev/null 2>&1 || true
    rm -rf -- "${endpoint_prefix:?}/${uuid_b:?}"
  fi
  for op in "${op_ids[@]}"; do
    inc delete --force "p-workspace-$op" >/dev/null 2>&1 || true
  done
  rmdir "$endpoint_prefix" 2>/dev/null || true
}
on_error() {
  printf 'P_DELETE_LIFECYCLE_FAIL line=%s status=%s\n' "$2" "$1" >&2
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
activate "$P_TEST_SOURCE/plugins/bundled/codex-adapter" "$step_dir/agent.json" agent.status.report
jq -s '{schema:"p.activation/v1",plugins:([.[0].plugins[0],.[1].plugins[0]])}' \
  "$step_dir/runtime-one.json" "$step_dir/host-one.json" > "$step_dir/runtime.json"

base=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
test "${#base}" -eq 64
jq -n --arg state "$state" --arg git_activation "$step_dir/git.json" \
  --arg git_id "$(jq -er '.plugins[0].id' "$step_dir/git.json")" \
  --arg runtime_activation "$step_dir/runtime.json" \
  --arg runtime_id "$(jq -er '.plugins[0].id' "$step_dir/runtime.json")" \
  --arg host_id "$(jq -er '.plugins[1].id' "$step_dir/runtime.json")" \
  --arg agent_activation "$step_dir/agent.json" \
  --arg agent_id "$(jq -er '.plugins[0].id' "$step_dir/agent.json")" \
  --arg incus_binary "$incus_real" --arg prefix "$endpoint_prefix" --arg image "$base" \
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
rpc system.capabilities | jq -e '.result.available |
  (index("session.removal.preview") != null and index("session.delete") != null)' >/dev/null

created=$(rpc project.create '{"v":1,"key":"delete-bootstrap","project":"delete-lifecycle"}')
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


expect_refusal() {
  local method="$1" params="$2" kind="$3" response
  if response=$(rpc "$method" "$params" 2> "$step_dir/refused.err"); then
    echo "unexpectedly accepted $method" >&2; return 1
  fi
  jq -e --arg kind "$kind" '.error.kind==$kind' <<< "$response" >/dev/null
}
source_running() {
  inc list "^p-$uuid$" --format json | jq -e --arg name "p-$uuid" \
    'length==1 and .[0].name==$name and .[0].status=="Running"' >/dev/null
}
inspect_loss() {
  local key="$1" response
  response=$(rpc workspace.loss.inspect "$(jq -nc --arg key "$key" --arg uuid "$uuid" '{v:1,key:$key,uuid:$uuid}')")
  loss_op=$(jq -er '.result.operation.id | select(length==36)' <<< "$response")
  op_ids+=("$loss_op")
  wait_operation "$loss_op" completed >/dev/null
}
preview_delete() {
  rpc session.removal.preview "$(jq -nc --arg uuid "$uuid" --arg op "$loss_op" \
    '{v:1,uuid:$uuid,kind:"delete",loss_operation_id:$op}')"
}
delete_confirm() {
  local key="$1" token="$2"
  rpc session.delete "$(jq -nc --arg key "$key" --arg uuid "$uuid" --arg token "$token" \
    '{v:1,key:$key,uuid:$uuid,confirmation_token:$token}')"
}

guest /run/current-system/sw/bin/git config user.name P
guest /run/current-system/sw/bin/git config user.email p@example.invalid
guest /run/current-system/sw/bin/bash -c \
  'printf base > tracked; git add tracked; git commit -qm base; git push origin HEAD:main'
base_oid=$(guest /run/current-system/sw/bin/git rev-parse HEAD)
created_b=$(rpc session.create '{"v":1,"key":"delete-sibling","project":"delete-lifecycle","branch":"sibling","choice":"new","source":"refs/heads/main"}')
create_b_op=$(jq -er '.result.operation.id | select(length==36)' <<< "$created_b")
uuid_b=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created_b")
wait_operation "$create_b_op" completed >/dev/null
test -f "$state/session_keys/$uuid"
test -f "$state/session_keys/$uuid_b"
inc exec "p-$uuid_b" --user 1000 --group 1000 --cwd /workspace --env HOME=/home/p -- \
  /usr/libexec/p/codex-adapter init
inc exec "p-$uuid_b" --user 1000 --group 1000 --cwd /workspace --env HOME=/home/p -- \
  /run/current-system/sw/bin/bash -c 'printf sibling-dummy > /home/p/.codex/auth.json; chmod 0600 /home/p/.codex/auth.json'
guest /usr/libexec/p/codex-adapter init
guest /run/current-system/sw/bin/bash -c \
  'printf doomed-dummy > /home/p/.codex/auth.json; chmod 0600 /home/p/.codex/auth.json; printf unique > tracked; git add tracked; git commit -qm unique; git push origin HEAD:main'
unique_oid=$(guest /run/current-system/sw/bin/git rev-parse HEAD)
inspect_loss delete-stale-report
stale_preview=$(preview_delete)
stale_token=$(jq -er '.result.preview.confirmation_token | select(length==32)' <<< "$stale_preview")
jq -e --arg unique "$unique_oid" --arg base "$base_oid" '
  .result.preview | .kind=="delete" and .assigned_ref=="refs/heads/main" and
  .assigned_tip==$unique and .runtime.condition=="present" and
  (.branch_loss | .commits_losing_p_reachability==[$unique] and
    any(.p_refs[]; .name=="refs/heads/sibling" and .oid==$base) and
    .origin.status=="local_only" and .origin.containing_branches==[])
' <<< "$stale_preview" >/dev/null
# Changed assigned tip requires another review before any irreversible point.
guest /run/current-system/sw/bin/bash -c \
  'printf after > tracked; git add tracked; git commit -qm after-preview; git push origin HEAD:main'
final_oid=$(guest /run/current-system/sw/bin/git rev-parse HEAD)
# Confirmation outcome assertions are aligned with reviewed public schema
# before this fixture becomes eligible for a VM run.
stale_response=$(delete_confirm delete-stale "$stale_token")
stale_op=$(jq -er '.result.operation.id | select(length==36)' <<< "$stale_response")
stale_result=$(wait_operation "$stale_op" failed)
jq -e '.result.operation | .kind=="session.delete" and .phase=="stale" and
  .committed==false' <<< "$stale_result" >/dev/null
source_running
test "$(guest /run/current-system/sw/bin/cat /home/p/.codex/auth.json)" = doomed-dummy
test -f "$state/session_keys/$uuid"
test -S "$endpoint_prefix/$uuid/git.sock"
test "$(guest /run/current-system/sw/bin/git ls-remote origin refs/heads/main | cut -f1)" = "$final_oid"
expect_refusal session.delete "$(jq -nc --arg uuid "$uuid" --arg token "$stale_token" \
  '{v:1,key:"delete-stale-reused",uuid:$uuid,confirmation_token:$token}')" busy
inspect_loss delete-fresh-report
fresh_preview=$(preview_delete)
fresh_token=$(jq -er '.result.preview.confirmation_token | select(length==32)' <<< "$fresh_preview")
jq -e --arg final "$final_oid" --arg unique "$unique_oid" --arg base "$base_oid" '
  .result.preview | .assigned_tip==$final and
  (.branch_loss | (.commits_losing_p_reachability|sort)==([$final,$unique]|sort) and
    any(.p_refs[]; .name=="refs/heads/sibling" and .oid==$base) and
    .origin.status=="local_only")
' <<< "$fresh_preview" >/dev/null
# A Discard review cannot be used as a Delete authorization.
discard_preview=$(rpc session.removal.preview "$(jq -nc --arg uuid "$uuid" --arg op "$loss_op" \
  '{v:1,uuid:$uuid,kind:"discard",loss_operation_id:$op}')")
discard_token=$(jq -er '.result.preview.confirmation_token | select(length==32)' <<< "$discard_preview")
expect_refusal session.delete "$(jq -nc --arg uuid "$uuid" --arg token "$discard_token" \
  '{v:1,key:"delete-wrong-action",uuid:$uuid,confirmation_token:$token}')" busy
completed_response=$(delete_confirm delete-confirmed "$fresh_token")
delete_op=$(jq -er '.result.operation.id | select(length==36)' <<< "$completed_response")
completed_result=$(wait_operation "$delete_op" completed)
jq -e '.result.operation | .kind=="session.delete" and .phase=="deleted" and
  .status=="completed" and .committed==true' <<< "$completed_result" >/dev/null
replayed=$(delete_confirm delete-confirmed "$fresh_token")
test "$(jq -er '.result.operation.id' <<< "$replayed")" = "$delete_op"
inc list "^p-$uuid$" --format json | jq -e 'length==0' >/dev/null
test ! -e "$endpoint_prefix/$uuid"
test ! -e "$state/session_keys/$uuid"
expect_refusal session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" unavailable
expect_refusal session.start "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" unavailable
rpc project.branches '{"v":1,"project":"delete-lifecycle","limit":8}' |
  jq -e --arg base "$base_oid" '.result.refs | length==1 and
    .[0].ref=="refs/heads/sibling" and .[0].oid==$base' >/dev/null
rpc project.retained_branches '{"v":1,"project":"delete-lifecycle","limit":8}' |
  jq -e '.result.branches==[]' >/dev/null
inc list "^p-$uuid_b$" --format json | jq -e 'length==1 and .[0].status=="Running"' >/dev/null
test -f "$state/session_keys/$uuid_b"
test "$(inc exec "p-$uuid_b" --user 1000 --group 1000 --cwd /workspace --env HOME=/home/p -- \
  /run/current-system/sw/bin/cat /home/p/.codex/auth.json)" = sibling-dummy
rpc session.inspect "$(jq -nc --arg uuid "$uuid_b" '{v:1,uuid:$uuid}')" |
  jq -e '.result.session.session_condition=="ready"' >/dev/null
# No login occurred. Only A's dummy private home/key was removed by public Delete.
echo P_DELETE_LIFECYCLE_PASS
