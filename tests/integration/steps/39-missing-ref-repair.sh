# shellcheck shell=bash
# Selected bare-present missing-ref repair. All Codex data is disposable dummy
# fixture content; no login or external credential is used.
set -E
umask 077
step_dir="$P_TEST_TMP/step-39"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step39
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
daemon_pid=
uuid=
sibling_uuid=
op_ids=()
incus_real=$(command -v incus)
git_real=$(command -v git)

inc() { timeout 90 "$incus_real" --force-local --project user-1000 "$@"; }
rpc() { timeout 45 p api "$socket" "$@"; }
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
      inc resume "p-$target" >/dev/null 2>&1 || true
      inc delete --force "p-$target" >/dev/null 2>&1 || true
      rm -rf -- "${endpoint_prefix:?}/${target:?}"
    fi
  done
  for id in "${op_ids[@]}"; do
    inc delete --force "p-workspace-$id" >/dev/null 2>&1 || true
  done
  rmdir "$endpoint_prefix" 2>/dev/null || true
}
on_error() {
  printf 'P_MISSING_REF_REPAIR_FAIL line=%s status=%s\n' "$2" "$1" >&2
  local id
  if test -n "${preview:-}"; then
    jq -c '.result.preview | {kind,session_uuid,project,branch,assigned_ref,
      assigned_ref_status,local_tip,runtime_status,incus_project,instance_name,
      incus_uuid,generation,policy_sha256,credential_fingerprint,
      loss_operation_id,loss_fingerprint,changes:(.changes // [] | map({path,code})[:4]),
      eligible,unsafe_reasons,blocked_reason}' <<< "$preview" >&2 || true
  fi
  for id in "${op_ids[@]}"; do
    rpc operation.inspect "$(jq -nc --arg id "$id" '{v:1,id:$id}')" 2>/dev/null |
      jq -cr '.result.operation | {id,status,phase,diagnostic:(.diagnostic // "")[:512]}' >&2 || true
  done
  if test -n "${loss_op:-}"; then
    rpc operation.inspect "$(jq -nc --arg id "$loss_op" '{v:1,id:$id}')" 2>/dev/null |
      jq -c '.result.operation.evidence | {instance_uuid,image_fingerprint,
        source_incus_uuid,source_generation,original_status,
        result:(.result | {schema,fingerprint,worktrees:(.worktrees|length)})}' >&2 || true
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
    if ! kill -0 "$daemon_pid" 2>/dev/null; then echo 'ref repair daemon exited' >&2; return 1; fi
    sleep 0.1
  done
  echo 'ref repair daemon health unavailable' >&2
  return 1
}
operation_id() { jq -er '.result.operation.id | select(length==36)' <<< "$1"; }
wait_operation() {
  local id="$1" response status deadline=$((SECONDS+180))
  while ((SECONDS < deadline)); do
    response=$(rpc operation.inspect "$(jq -nc --arg id "$id" '{v:1,id:$id}')")
    status=$(jq -er '.result.operation.status' <<< "$response")
    if test "$status" = completed; then printf '%s\n' "$response"; return; fi
    if test "$status" = blocked || test "$status" = failed || test "$status" = superseded || test "$status" = unknown; then
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
source_identity() {
  inc list "^p-$uuid$" --format json | jq -cer --arg name "p-$uuid" '
    . | select(length==1 and .[0].name==$name and .[0].status=="Running") |
    .[0] | {name,status,uuid:.config["volatile.uuid"],generation:.config["volatile.uuid.generation"]} |
    select((.uuid|test("^[0-9a-f-]{36}$")) and (.generation|test("^[0-9a-f-]{36}$")))'
}
bare_ref() {
  GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null \
    "$git_real" --git-dir="$bare" rev-parse --verify refs/heads/main
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
  (index("workspace.loss.inspect") != null and index("session.ref.repair.preview") != null and
   index("session.ref.repair.confirm") != null)' >/dev/null
created=$(rpc project.create '{"v":1,"key":"ref-repair-bootstrap","project":"missing-ref-repair"}')
create_op=$(operation_id "$created")
uuid=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created")
op_ids+=("$create_op")
wait_operation "$create_op" >/dev/null
wait_ready "$uuid"
guest "$uuid" git config user.name P
guest "$uuid" git config user.email p@example.invalid
guest "$uuid" /run/current-system/sw/bin/bash -c \
  'printf retained > tracked; printf "ignored.bin\n" > .gitignore; git add tracked .gitignore; git commit -qm retained; git push origin HEAD:main'
tip=$(guest "$uuid" git rev-parse HEAD)
created=$(rpc session.create '{"v":1,"key":"ref-repair-sibling","project":"missing-ref-repair","branch":"sibling","choice":"new","source":"refs/heads/main"}')
sibling_op=$(operation_id "$created")
sibling_uuid=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created")
op_ids+=("$sibling_op")
wait_operation "$sibling_op" >/dev/null
wait_ready "$sibling_uuid"
guest "$uuid" /usr/libexec/p/codex-adapter init
guest "$uuid" /run/current-system/sw/bin/bash -c \
  'printf dummy-secret > /home/p/.codex/auth.json; chmod 0600 /home/p/.codex/auth.json; printf changed > tracked; printf untracked > new-file; printf abc > ignored.bin'
guest "$sibling_uuid" /usr/libexec/p/codex-adapter init
guest "$sibling_uuid" /run/current-system/sw/bin/bash -c \
  'printf sibling-dummy > /home/p/.codex/auth.json; chmod 0600 /home/p/.codex/auth.json'
key_digest=$(sha256sum "$state/session_keys/$uuid" | cut -d' ' -f1)
sibling_key_digest=$(sha256sum "$state/session_keys/$sibling_uuid" | cut -d' ' -f1)
tracked_digest=$(guest "$uuid" sha256sum tracked | cut -d' ' -f1)
identity_before=$(source_identity)
repo_hash=$(printf %s missing-ref-repair | sha256sum | cut -d' ' -f1)
bare="$state/repositories/$repo_hash.git"
test "$(bare_ref)" = "$tip"

# Fixture-only loss: remove just the assigned P ref from P's own bare repo.
# The sibling ref retains the commit object; the runtime and local branch stay.
GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null \
  "$git_real" --git-dir="$bare" update-ref -d refs/heads/main "$tip"
if bare_ref > "$step_dir/absent.out" 2>&1; then echo 'fault did not remove P ref' >&2; exit 1; fi
test "$(guest "$uuid" git rev-parse HEAD)" = "$tip"
test "$(guest "$uuid" git branch --show-current)" = main
test "$(guest "$uuid" git ls-remote origin refs/heads/main)" = ""
test "$(source_identity)" = "$identity_before"

inspected=$(rpc workspace.loss.inspect "$(jq -nc --arg uuid "$uuid" \
  '{v:1,key:"ref-repair-loss",uuid:$uuid}')")
loss_op=$(operation_id "$inspected")
op_ids+=("$loss_op")
loss_result=$(wait_operation "$loss_op")
jq -e --arg tip "$tip" '
  .result.operation.evidence.result |
  .schema=="p.workspace-loss/v1" and .runtime_data_will_be_removed==true and
  (.worktrees|length)==1 and .worktrees[0].path=="/workspace" and
  .worktrees[0].branch=="main" and .worktrees[0].head_oid==$tip and
  any(.local_refs[]; .name=="refs/heads/main" and .oid==$tip) and
  (.p_refs|length)==1 and .p_refs[0].name=="refs/heads/sibling" and .p_refs[0].oid==$tip and
  .local_only_commits==[]' <<< "$loss_result" >/dev/null
inc list "^p-workspace-$loss_op$" --format json | jq -e 'length==0' >/dev/null
test "$(source_identity)" = "$identity_before"

preview_request=$(jq -nc --arg uuid "$uuid" --arg loss "$loss_op" \
  '{v:1,uuid:$uuid,loss_operation_id:$loss}')
preview=$(rpc session.ref.repair.preview "$preview_request")
jq -e --arg uuid "$uuid" --arg tip "$tip" --arg loss "$loss_op" '
  .result.preview | .kind=="missing_assigned_ref" and .session_uuid==$uuid and
  .project=="missing-ref-repair" and .branch=="main" and
  .assigned_ref=="refs/heads/main" and .assigned_ref_status=="missing" and
  .local_tip==$tip and .runtime_status=="Running" and
  .incus_project=="user-1000" and .instance_name==("p-"+$uuid) and
  (.incus_uuid|test("^[0-9a-f-]{36}$")) and (.generation|test("^[0-9a-f-]{36}$")) and
  (.policy_sha256|length==64) and (.credential_fingerprint|length==64) and
  .loss_operation_id==$loss and (.loss_fingerprint|length==64) and
  any(.changes[]; .path=="tracked" and .code==" M") and
  any(.changes[]; .path=="new-file" and .code=="??") and
  .ignored.count==1 and .ignored.logical_bytes==3 and
  .unsafe_reasons==[] and .eligible==true and
  (.confirmation_token|test("^[0-9a-f]{32}$"))' <<< "$preview" >/dev/null
stale_token=$(jq -er '.result.preview.confirmation_token' <<< "$preview")

# A competing exact ref creation after preview must refuse confirmation before
# any runtime mutation. Remove the fixture ref again for a fresh review.
GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null \
  "$git_real" --git-dir="$bare" update-ref refs/heads/main "$tip" "0000000000000000000000000000000000000000"
expect_busy session.ref.repair.confirm "$(jq -nc --arg uuid "$uuid" --arg token "$stale_token" \
  '{v:1,key:"ref-repair-stale",uuid:$uuid,confirmation_token:$token}')"
test "$(source_identity)" = "$identity_before"
GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null \
  "$git_real" --git-dir="$bare" update-ref -d refs/heads/main "$tip"
preview=$(rpc session.ref.repair.preview "$preview_request")
token=$(jq -er '.result.preview.confirmation_token | select(length==32)' <<< "$preview")
confirmed=$(rpc session.ref.repair.confirm "$(jq -nc --arg uuid "$uuid" --arg token "$token" \
  '{v:1,key:"ref-repair-confirmed",uuid:$uuid,confirmation_token:$token}')")
repair_op=$(operation_id "$confirmed")
op_ids+=("$repair_op")
repaired=$(wait_operation "$repair_op")
jq -e '.result.operation | .kind=="session.ref.repair" and .phase=="completed" and
  .status=="completed" and .committed==true' <<< "$repaired" >/dev/null
test "$(bare_ref)" = "$tip"
test "$(source_identity)" = "$identity_before"
test "$(guest "$uuid" git rev-parse HEAD)" = "$tip"
test "$(guest "$uuid" git branch --show-current)" = main
test "$(guest "$uuid" sha256sum tracked | cut -d' ' -f1)" = "$tracked_digest"
test "$(guest "$uuid" cat new-file)" = untracked
test "$(guest "$uuid" cat ignored.bin)" = abc
test "$(guest "$uuid" cat /home/p/.codex/auth.json)" = dummy-secret
test "$(sha256sum "$state/session_keys/$uuid" | cut -d' ' -f1)" = "$key_digest"
test -S "$endpoint_prefix/$uuid/git.sock"
test "$(guest "$sibling_uuid" cat /home/p/.codex/auth.json)" = sibling-dummy
test "$(sha256sum "$state/session_keys/$sibling_uuid" | cut -d' ' -f1)" = "$sibling_key_digest"
rpc project.branches '{"v":1,"project":"missing-ref-repair","limit":8}' |
  jq -e --arg tip "$tip" '.result.refs | length==2 and
    any(.[]; .ref=="refs/heads/main" and .oid==$tip) and
    any(.[]; .ref=="refs/heads/sibling" and .oid==$tip)' >/dev/null
test "$(rpc session.ref.repair.confirm "$(jq -nc --arg uuid "$uuid" --arg token "$token" \
  '{v:1,key:"ref-repair-confirmed",uuid:$uuid,confirmation_token:$token}')" |
  jq -er '.result.operation.id')" = "$repair_op"
stop_daemon
start_daemon
wait_ready "$uuid"
test "$(source_identity)" = "$identity_before"
test "$(bare_ref)" = "$tip"
test "$(guest "$uuid" cat /home/p/.codex/auth.json)" = dummy-secret
test "$(sha256sum "$state/session_keys/$uuid" | cut -d' ' -f1)" = "$key_digest"
echo P_MISSING_REF_REPAIR_BARE_PRESENT_PASS
