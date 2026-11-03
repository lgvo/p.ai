# shellcheck shell=bash
# Discard and Delete of sessions whose assigned P ref was deleted outside P,
# then a new session on a branch of the project. All Codex data is
# disposable dummy fixture content; no login or external credential is used.
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
doomed_uuid=
sibling_uuid=
fresh_uuid=
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
  for target in "$uuid" "$doomed_uuid" "$sibling_uuid" "$fresh_uuid"; do
    if test -n "$target"; then
      inc delete --force "p-$target" >/dev/null 2>&1 || true
      rm -rf -- "${endpoint_prefix:?}/${target:?}"
    fi
  done
  rmdir "$endpoint_prefix" 2>/dev/null || true
}
on_error() {
  printf 'P_MISSING_REF_REMOVAL_FAIL line=%s status=%s\n' "$2" "$1" >&2
  local id
  for id in "${op_ids[@]}"; do
    rpc operation.inspect "$(jq -nc --arg id "$id" '{id:$id}')" 2>/dev/null |
      jq -cr '.result.operation | {id,kind,status,phase,diagnostic:(.diagnostic // "")[:512]}' >&2 || true
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
    if ! kill -0 "$daemon_pid" 2>/dev/null; then echo 'missing ref daemon exited' >&2; return 1; fi
    sleep 0.1
  done
  echo 'missing ref daemon health unavailable' >&2
  return 1
}
operation_id() { jq -er '.result.operation.id | select(length==36)' <<< "$1"; }
wait_operation() {
  local id="$1" response status deadline=$((SECONDS+180))
  while ((SECONDS < deadline)); do
    response=$(rpc operation.inspect "$(jq -nc --arg id "$id" '{id:$id}')")
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
    if rpc session.inspect "$(jq -nc --arg uuid "$target" '{uuid:$uuid}')" |
      jq -e '.result.session.session_condition=="ready"' >/dev/null; then return; fi
    sleep 0.3
  done
  echo "session $target did not become ready" >&2
  return 1
}
expect_refusal() {
  local method="$1" params="$2" kind="$3" response
  if response=$(rpc "$method" "$params" 2> "$step_dir/refused.err"); then
    echo "unexpectedly accepted $method" >&2; return 1
  fi
  jq -e --arg kind "$kind" '.error.kind==$kind' <<< "$response" >/dev/null
}
bare_ref() {
  GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null \
    "$git_real" --git-dir="$bare" rev-parse --verify --quiet "refs/heads/$1"
}
# Fixture-only loss: remove just one assigned P ref from P's own bare repo.
# The sibling ref retains the commit object; the runtime and local branch stay.
delete_p_ref() {
  local branch="$1" target="$2"
  GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null \
    "$git_real" --git-dir="$bare" update-ref -d "refs/heads/$branch" "$tip"
  if bare_ref "$branch" > "$step_dir/absent.out" 2>&1; then echo 'fault did not remove P ref' >&2; return 1; fi
  test "$(guest "$target" git rev-parse HEAD)" = "$tip"
  test "$(guest "$target" git branch --show-current)" = "$branch"
  test "$(guest "$target" git ls-remote origin "refs/heads/$branch")" = ""
}
# Both reviews of a session without its P ref are allowed and say so. P does
# not look inside the running container; it states that everything in it is
# lost.
assert_missing_ref_previews() {
  local target="$1" branch="$2"
  discard_preview=$(rpc session.removal.preview "$(jq -nc --arg uuid "$target" '{uuid:$uuid,kind:"discard"}')")
  delete_preview=$(rpc session.removal.preview "$(jq -nc --arg uuid "$target" '{uuid:$uuid,kind:"delete"}')")
  for preview in "$discard_preview" "$delete_preview"; do
    jq -e --arg uuid "$target" --arg branch "$branch" '
      .result.preview | .session_uuid==$uuid and .project=="missing-ref-removal" and
      .branch==$branch and .assigned_ref==("refs/heads/"+$branch) and
      .assigned_ref_missing==true and (has("assigned_tip")|not) and
      .runtime.condition=="present" and .runtime.original_status=="Running" and
      .runtime.runtime_data_lost==true and (.runtime|has("runtime_loss_unknown")|not) and
      (.confirmation_token|test("^[0-9a-f]{32}$"))' <<< "$preview" >/dev/null
  done
  jq -e '.result.preview | .kind=="discard" and .branch_loss==null' <<< "$discard_preview" >/dev/null
  jq -e --arg branch "$branch" --arg tip "$tip" '.result.preview | .kind=="delete" and
    (.branch_loss | .assigned_ref==("refs/heads/"+$branch) and (has("assigned_tip")|not) and
      .commits_losing_p_reachability==[] and (has("origin")|not) and
      all(.p_refs[]; .name!=("refs/heads/"+$branch)) and
      any(.p_refs[]; .name=="refs/heads/keep" and .oid==$tip))' <<< "$delete_preview" >/dev/null
}
# The session record, its runtime, key and endpoints are gone, and no ref of
# its branch exists in P's repository.
assert_removed() {
  local target="$1" branch="$2"
  inc list "^p-$target$" --format json | jq -e 'length==0' >/dev/null
  test ! -e "$state/session_keys/$target"
  test ! -e "$endpoint_prefix/$target"
  expect_refusal session.inspect "$(jq -nc --arg uuid "$target" '{uuid:$uuid}')" unavailable
  rpc session.list '{"limit":8}' | jq -e --arg old "$target" \
    '.result.sessions | all(.[]; .uuid!=$old)' >/dev/null
  if bare_ref "$branch" > "$step_dir/removed-ref.out" 2>&1; then echo 'removal left a P ref' >&2; return 1; fi
  rpc project.branches '{"project":"missing-ref-removal","limit":8}' |
    jq -e --arg ref "refs/heads/$branch" '.result.refs | all(.[]; .ref!=$ref)' >/dev/null
  rpc project.retained_branches '{"project":"missing-ref-removal","limit":8}' |
    jq -e --arg branch "$branch" '.result.branches | all(.[]; .branch!=$branch)' >/dev/null
}

start_daemon
created=$(rpc project.create '{"key":"missing-ref-bootstrap","project":"missing-ref-removal"}')
create_op=$(operation_id "$created")
uuid=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created")
op_ids+=("$create_op")
wait_operation "$create_op" >/dev/null
wait_ready "$uuid"
guest "$uuid" git config user.name P
guest "$uuid" git config user.email p@example.invalid
guest "$uuid" /run/current-system/sw/bin/bash -c \
  'printf retained > tracked; git add tracked; git commit -qm retained; git push origin HEAD:main'
tip=$(guest "$uuid" git rev-parse HEAD)
create_on() {
  local key="$1" branch="$2" response op
  response=$(rpc session.create "$(jq -nc --arg key "$key" --arg branch "$branch" \
    '{key:$key,project:"missing-ref-removal",branch:$branch,choice:"new",source:"refs/heads/main"}')")
  op=$(operation_id "$response")
  new_uuid=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$response")
  op_ids+=("$op")
  wait_operation "$op" >/dev/null
  wait_ready "$new_uuid"
}
create_on missing-ref-doomed doomed
doomed_uuid=$new_uuid
create_on missing-ref-keep keep
sibling_uuid=$new_uuid
guest "$uuid" /usr/libexec/p/codex-adapter init
guest "$uuid" /run/current-system/sw/bin/bash -c \
  'printf dummy-secret > /home/p/.codex/auth.json; chmod 0600 /home/p/.codex/auth.json; printf changed > tracked; printf untracked > new-file'
guest "$sibling_uuid" /usr/libexec/p/codex-adapter init
guest "$sibling_uuid" /run/current-system/sw/bin/bash -c \
  'printf sibling-dummy > /home/p/.codex/auth.json; chmod 0600 /home/p/.codex/auth.json'
sibling_key_digest=$(sha256sum "$state/session_keys/$sibling_uuid" | cut -d' ' -f1)
repo_hash=$(printf %s missing-ref-removal | sha256sum | cut -d' ' -f1)
bare="$state/repositories/$repo_hash.git"
test "$(bare_ref main)" = "$tip"
test "$(bare_ref doomed)" = "$tip"

# Discard of the running bootstrap session after its main ref was deleted:
# with no branch to keep it ends like a delete.
delete_p_ref main "$uuid"
assert_missing_ref_previews "$uuid" main
token=$(jq -er '.result.preview.confirmation_token' <<< "$discard_preview")
removed=$(rpc session.discard "$(jq -nc --arg uuid "$uuid" --arg token "$token" \
  '{key:"missing-ref-discard",uuid:$uuid,confirmation_token:$token}')")
discard_op=$(operation_id "$removed")
op_ids+=("$discard_op")
discarded=$(wait_operation "$discard_op")
jq -e '.result.operation | .kind=="session.discard" and .phase=="discarded" and
  .committed==true' <<< "$discarded" >/dev/null
assert_removed "$uuid" main
discarded_uuid=$uuid
uuid=

# Delete of the running doomed session after its ref was deleted: nothing of
# its branch is left in P's repository to remove.
delete_p_ref doomed "$doomed_uuid"
assert_missing_ref_previews "$doomed_uuid" doomed
token=$(jq -er '.result.preview.confirmation_token' <<< "$delete_preview")
removed=$(rpc session.delete "$(jq -nc --arg uuid "$doomed_uuid" --arg token "$token" \
  '{key:"missing-ref-delete",uuid:$uuid,confirmation_token:$token}')")
delete_op=$(operation_id "$removed")
op_ids+=("$delete_op")
deleted=$(wait_operation "$delete_op")
jq -e '.result.operation | .kind=="session.delete" and .phase=="deleted" and
  .committed==true' <<< "$deleted" >/dev/null
assert_removed "$doomed_uuid" doomed
doomed_uuid=

# The sibling is untouched; the retained commit survives on its branch.
rpc project.branches '{"project":"missing-ref-removal","limit":8}' |
  jq -e --arg tip "$tip" '.result.refs | length==1 and
    .[0].ref=="refs/heads/keep" and .[0].oid==$tip' >/dev/null
rpc project.retained_branches '{"project":"missing-ref-removal","limit":8}' |
  jq -e '.result.branches==[]' >/dev/null
test "$(guest "$sibling_uuid" cat /home/p/.codex/auth.json)" = sibling-dummy
test "$(sha256sum "$state/session_keys/$sibling_uuid" | cut -d' ' -f1)" = "$sibling_key_digest"
inc list "^p-$sibling_uuid$" --format json | jq -e 'length==1 and .[0].status=="Running"' >/dev/null

# Delete or discard, then create replaces the former missing-ref repair: a
# new session on the freed branch name starts from the committed work.
created=$(rpc session.create '{"key":"missing-ref-recreate","project":"missing-ref-removal","branch":"main","choice":"new","source":"refs/heads/keep"}')
recreate_op=$(operation_id "$created")
fresh_uuid=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created")
op_ids+=("$recreate_op")
test "$fresh_uuid" != "$discarded_uuid"
wait_operation "$recreate_op" >/dev/null
wait_ready "$fresh_uuid"
test "$(guest "$fresh_uuid" git rev-parse HEAD)" = "$tip"
test "$(guest "$fresh_uuid" git branch --show-current)" = main
test "$(guest "$fresh_uuid" cat tracked)" = retained
guest "$fresh_uuid" test ! -e new-file
guest "$fresh_uuid" git config user.name P
guest "$fresh_uuid" git config user.email p@example.invalid
guest "$fresh_uuid" /run/current-system/sw/bin/bash -c \
  'printf recreated > tracked; git add tracked; git commit -qm recreated; git push origin HEAD:main'
recreated_tip=$(guest "$fresh_uuid" git rev-parse HEAD)
test "$(bare_ref main)" = "$recreated_tip"
rpc project.branches '{"project":"missing-ref-removal","limit":8}' |
  jq -e --arg tip "$tip" --arg main "$recreated_tip" '.result.refs | length==2 and
    any(.[]; .ref=="refs/heads/main" and .oid==$main) and
    any(.[]; .ref=="refs/heads/keep" and .oid==$tip)' >/dev/null
echo P_MISSING_REF_REMOVAL_PASS
