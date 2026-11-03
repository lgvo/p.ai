# shellcheck shell=bash
# Removal plans (the dry runs of session.discard and session.delete) and
# session admission capacity; no removal runs.
# All files are disposable fixtures; no authentication is used.
set -E
umask 077
step_dir="$P_TEST_TMP/step-30"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step30
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
daemon_pid=
uuid=
uuid_keep=
uuid_third=
uuid_fourth=
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
    inc delete --force "p-$uuid" >/dev/null 2>&1 || true
    rm -rf -- "${endpoint_prefix:?}/${uuid:?}"
  fi
  for other in "$uuid_keep" "$uuid_third" "$uuid_fourth"; do
    if test -n "$other"; then
      inc delete --force "p-$other" >/dev/null 2>&1 || true
      rm -rf -- "${endpoint_prefix:?}/${other:?}"
    fi
  done
  rmdir "$endpoint_prefix" 2>/dev/null || true
}
on_error() {
  printf 'P_REMOVAL_PREVIEW_FAIL line=%s status=%s\n' "$2" "$1" >&2
  for op in "${op_ids[@]}"; do
    rpc operation.inspect "$(jq -nc --arg id "$op" '{id:$id}')" 2>/dev/null |
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

base=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
test "${#base}" -eq 64
jq -n --arg state "$state" --arg git_activation "$step_dir/git.json" \
  --arg git_id "$(jq -er '.plugins[0].id' "$step_dir/git.json")" \
  --arg runtime_activation "$step_dir/runtime.json" \
  --arg runtime_id "$(jq -er '.plugins[0].id' "$step_dir/runtime.json")" \
  --arg host_id "$(jq -er '.plugins[1].id' "$step_dir/runtime.json")" \
  --arg incus_binary "$incus_real" --arg prefix "$endpoint_prefix" --arg image "$base" \
  '{schema:"p.host/v1",state_dir:$state,
    git:{activation_path:$git_activation,source_plugin_id:$git_id,listen:"127.0.0.1:0"},
    runtime:{activation_path:$runtime_activation,runtime_plugin_id:$runtime_id,
      host_plugin_id:$host_id,driver:{name:"incus",config:{binary:$incus_binary,
      user_socket:"/var/lib/incus/unix.socket.user",project:"user-1000"}},
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
rpc system.hello | jq -e '.result.capabilities | index("runtime") != null' >/dev/null

created=$(rpc project.create '{"key":"preview-bootstrap","project":"removal-preview"}')
create_op=$(jq -er '.result.operation.id | select(length==36)' <<< "$created")
uuid=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created")
wait_operation() {
  local id="$1" want="$2" result status deadline=$((SECONDS+150))
  while ((SECONDS < deadline)); do
    result=$(rpc operation.wait "$(jq -nc --arg id "$id" '{id:$id,timeout:20}')")
    status=$(jq -er '.result.operation.status' <<< "$result")
    if test "$status" = "$want"; then printf '%s\n' "$result"; return; fi
    if test "$status" = blocked || test "$status" = failed || test "$status" = completed; then
      echo "unexpected operation status: $result" >&2; return 1
    fi
    if test "$status" != running; then sleep 0.3; fi
  done
  echo "operation $id did not reach $want" >&2
  return 1
}
wait_operation "$create_op" completed >/dev/null
deadline=$((SECONDS+80))
while ((SECONDS < deadline)); do
  if rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" |
    jq -e '.result.session.session_condition=="ready"' >/dev/null; then break; fi
  sleep 0.3
done
rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" |
  jq -e '.result.session.session_condition=="ready"' >/dev/null

# change_session METHOD UUID: METHOD, session.start or session.stop, records
# an operation of its kind for session UUID, which completes.
change_session() {
  local id
  id=$(rpc "$1" "$(jq -nc --arg uuid "$2" '{uuid:$uuid}')" | jq -er --arg kind "$1" \
    '.result | select(.changed==true and .operation.kind==$kind) | .operation.id') || return
  wait_operation "$id" completed >/dev/null
}
# expect_refusal METHOD PARAMS KIND [REASON]: the request is refused with the
# error kind KIND, its code and, when given, REASON.
expect_refusal() {
  local method="$1" params="$2" kind="$3" reason="${4:-}" response
  if response=$(rpc "$method" "$params" 2> "$step_dir/refused.err"); then
    echo "unexpectedly accepted $method" >&2; return 1
  fi
  jq -e --arg kind "$kind" --arg reason "$reason" '
    {method_not_found: -32601, invalid: -32602, retry: -32010, conflict: -32011,
     capacity: -32012, precondition_failed: -32013, precondition_required: -32014,
     not_found: -32015} as $codes |
    .error.message==$kind and .error.code==$codes[$kind] and
    ($reason=="" or .error.data.reason==$reason)' <<< "$response" >/dev/null
}
expect_capacity_refusal() {
  expect_refusal "$1" "$2" capacity session_capacity
}
source_running() {
  inc list "^p-$uuid$" --format json | jq -e --arg name "p-$uuid" \
    'length==1 and .[0].name==$name and .[0].status=="Running"' >/dev/null
}
# dry_run UUID ACTION: the dry run of session.ACTION, discard or delete, of
# session UUID: its plan and, when the plan is allowed, its change tag.
dry_run() {
  rpc "session.$2" "$(jq -nc --arg uuid "$1" '{uuid:$uuid}')"
}
tag_pattern='^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{64}$'
# Loss inspection and the separate preview are gone: their methods are
# unknown, and the former loss and acknowledgement parameters are refused by
# the strict request decoder.
expect_refusal workspace.loss.inspect '{}' method_not_found
expect_refusal session.removal.preview "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid,kind:"discard"}')" \
  method_not_found
expect_refusal session.discard "$(jq -nc --arg uuid "$uuid" --arg op "$create_op" \
  '{uuid:$uuid,loss_operation_id:$op}')" invalid param
# A run needs the change tag of a dry run: a key alone is refused as a missing
# precondition, a tag alone as a request without its key. Neither removes
# anything.
expect_refusal session.discard "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid,key:"preview-untagged"}')" \
  precondition_required if_match_missing
expect_refusal session.delete "$(jq -nc --arg uuid "$uuid" --arg tag "20991231T000000Z-$(printf '%064d' 0)" \
  '{uuid:$uuid,if_match:$tag}')" invalid param
# Empty bootstrap can be planned before any P branch tip exists: its unborn
# first branch has no P ref, and the plan says so.
unborn_discard=$(dry_run "$uuid" discard)
jq -e --arg uuid "$uuid" --arg tag "$tag_pattern" '
  .result | (.change_tag | test($tag)) and (has("confirmation_token") or has("expires_at") | not) and
  (.plan | .action=="session.discard" and .target==("session/"+$uuid) and
    .allowed==true and .refusals==[] and .gone==[] and
    .loss==[{kind:"runtime_data",target:("session/"+$uuid),certain:true}] and
    (.details | .assigned_ref=="refs/heads/main" and (has("assigned_tip")|not) and
      .assigned_ref_missing==true and (has("branch_loss")|not) and
      (has("kind") or has("session_uuid") | not) and
      .runtime.condition=="present" and .runtime.original_status=="Running"))
' <<< "$unborn_discard" >/dev/null
unborn_delete=$(dry_run "$uuid" delete)
jq -e --arg uuid "$uuid" '.result.plan | .action=="session.delete" and .allowed==true and
  .loss==[{kind:"runtime_data",target:("session/"+$uuid),certain:true}] and
  (.details | .assigned_ref_missing==true and (has("assigned_tip")|not) and
    (.branch_loss | .assigned_ref=="refs/heads/main" and (has("assigned_tip")|not) and
      .commits_losing_p_reachability==[] and .p_refs==[] and (has("origin")|not)))' <<< "$unborn_delete" >/dev/null
source_running

# Admission needs room for one more container only: four sessions fill the
# project's four-container limit, and a fifth session or project is refused.
guest /run/current-system/sw/bin/git config user.name P
guest /run/current-system/sw/bin/git config user.email p@example.invalid
guest /run/current-system/sw/bin/bash -c 'printf retained > tracked; git add tracked; git commit -qm retained; git push origin HEAD:main'
retained_oid=$(guest /run/current-system/sw/bin/git rev-parse HEAD)
create_branch() {
  local key="$1" branch="$2" response
  response=$(rpc session.create "$(jq -nc --arg key "$key" --arg branch "$branch" \
    '{key:$key,project:"removal-preview",branch:$branch,choice:"new",source:"refs/heads/main"}')")
  new_uuid=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$response")
  local op
  op=$(jq -er '.result.operation.id | select(length==36)' <<< "$response")
  op_ids+=("$op")
  wait_operation "$op" completed >/dev/null
}
create_branch preview-keep keep
uuid_keep=$new_uuid
create_branch preview-third third
uuid_third=$new_uuid
create_branch preview-fourth fourth
uuid_fourth=$new_uuid

expect_capacity_refusal session.create '{"key":"preview-overflow","project":"removal-preview","branch":"overflow","choice":"new","source":"refs/heads/main"}'
expect_capacity_refusal project.create '{"key":"preview-overflow-project","project":"preview-overflow"}'
# An exact replay does not need another reservation at the admission limit.
replayed=$(rpc session.create '{"key":"preview-fourth","project":"removal-preview","branch":"fourth","choice":"new","source":"refs/heads/main"}')
test "$(jq -er '.result.operation.session_uuid' <<< "$replayed")" = "$uuid_fourth"
inc project list --format json | jq -e \
  'map(select(.name=="user-1000")) | length==1 and .[0].config["limits.containers"]=="4"' >/dev/null
inc list --format json | jq -e 'length==4' >/dev/null

# main now has a commit whose last P branch would disappear on Delete. The
# plans do not look inside the running container: they state that it and
# everything in it is lost, and leave it untouched.
guest /run/current-system/sw/bin/bash -c 'printf unique > tracked; git add tracked; git commit -qm unique; git push origin HEAD:main'
unique_oid=$(guest /run/current-system/sw/bin/git rev-parse HEAD)
guest /run/current-system/sw/bin/bash -c 'printf dirty > tracked; mkdir -m 0700 /home/p/.codex; printf dummy-auth-only > /home/p/.codex/auth.json; chmod 0600 /home/p/.codex/auth.json'
discard_plan=$(dry_run "$uuid" discard)
delete_plan=$(dry_run "$uuid" delete)
for result in "$discard_plan" "$delete_plan"; do
  jq -e --arg uuid "$uuid" --arg oid "$unique_oid" --arg image "$base" --arg tag "$tag_pattern" '
    .result | (.change_tag | test($tag)) and (.plan |
    .target==("session/"+$uuid) and .allowed==true and .refusals==[] and .gone==[] and
    .loss[0]=={kind:"runtime_data",target:("session/"+$uuid),certain:true} and
    (.details | .project=="removal-preview" and
      .branch=="main" and .assigned_ref=="refs/heads/main" and .assigned_tip==$oid and
      (has("assigned_ref_missing")|not) and (.policy_sha256|length)==64 and
      .runtime.condition=="present" and .runtime.original_status=="Running" and
      .runtime.incus_project=="user-1000" and .runtime.instance_name==("p-"+$uuid) and
      (.runtime.incus_uuid|length)==36 and (.runtime.generation|length)==36 and
      .runtime.image_fingerprint==$image and
      (.runtime | (has("loss") or has("loss_operation_id") or has("fingerprint") or
        has("observed_at") or has("runtime_data_lost") or has("runtime_loss_unknown")) | not)))
  ' <<< "$result" >/dev/null
done
jq -e --arg uuid "$uuid" '.result.plan | .action=="session.discard" and (.details|has("branch_loss")|not) and
  .loss==[{kind:"runtime_data",target:("session/"+$uuid),certain:true}]' <<< "$discard_plan" >/dev/null
jq -e --arg oid "$unique_oid" --arg retained "$retained_oid" '
  .result.plan | .action=="session.delete" and
  (.loss | length==2 and .[1]=={kind:"p_commits",target:"project/removal-preview:branch/main",
    count:1,sample:[$oid],certain:true}) and
  (.details.branch_loss | .assigned_ref=="refs/heads/main" and .assigned_tip==$oid and
    .commits_losing_p_reachability==[$oid] and (.p_refs|length)==4 and
    any(.p_refs[]; .name=="refs/heads/keep" and .oid==$retained) and
    (has("origin")|not))
' <<< "$delete_plan" >/dev/null
test "$(jq -er '.result.change_tag' <<< "$discard_plan")" != \
  "$(jq -er '.result.change_tag' <<< "$delete_plan")"
# Acknowledging a missing runtime is no longer a parameter: a plan states a
# missing runtime itself, and the request decoder refuses the old member.
expect_refusal session.delete "$(jq -nc --arg uuid "$uuid_keep" \
  '{uuid:$uuid,acknowledge_missing_runtime:true}')" invalid param
source_running
test "$(guest /run/current-system/sw/bin/cat /home/p/.codex/auth.json)" = dummy-auth-only
test "$(guest /run/current-system/sw/bin/cat tracked)" = dirty

# A stopped session is planned the same way; its status is informational.
change_session session.stop "$uuid"
rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" | \
  jq -e '.result.session.session_condition=="stopped"' >/dev/null
stopped_plan=$(dry_run "$uuid" discard)
jq -e --arg uuid "$uuid" '.result.plan | .allowed==true and
  (.details.runtime | .condition=="present" and .original_status=="Stopped") and
  .loss==[{kind:"runtime_data",target:("session/"+$uuid),certain:true}]' <<< "$stopped_plan" >/dev/null
inc list "^p-$uuid$" --format json | jq -e 'length==1 and .[0].status=="Stopped"' >/dev/null
change_session session.start "$uuid"
deadline=$((SECONDS+80))
while ((SECONDS < deadline)); do
  if rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" | \
    jq -e '.result.session.session_condition=="ready"' >/dev/null; then break; fi
  sleep 0.3
done
rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" | \
  jq -e '.result.session.session_condition=="ready"' >/dev/null
# A changed P tip is planned afresh: a new dry run reports it.
guest /run/current-system/sw/bin/bash -c 'git add tracked; git commit -qm changed-after-preview; git push origin HEAD:main'
changed_oid=$(guest /run/current-system/sw/bin/git rev-parse HEAD)
changed_plan=$(dry_run "$uuid" delete)
jq -e --arg oid "$changed_oid" --arg unique "$unique_oid" '.result.plan |
  .details.assigned_tip==$oid and
  (.details.branch_loss.commits_losing_p_reachability|sort)==([$oid,$unique]|sort) and
  any(.loss[]; .kind=="p_commits" and .target=="project/removal-preview:branch/main" and
    .count==2 and (.sample|sort)==([$oid,$unique]|sort) and .certain==true)' \
  <<< "$changed_plan" >/dev/null
source_running

# Fixture-only removal simulates authoritative native absence. The plan
# states the runtime missing and its contents already gone, not lost by the
# removal, needs no acknowledgement, and leaves P's session row/ref intact.
# This is not public cleanup evidence.
inc delete --force "p-$uuid_third"
missing_plan=$(dry_run "$uuid_third" delete)
jq -e --arg uuid "$uuid_third" --arg oid "$retained_oid" --arg tag "$tag_pattern" '
  .result | (.change_tag | test($tag)) and (.plan |
  .action=="session.delete" and .target==("session/"+$uuid) and .allowed==true and .refusals==[] and
  .gone==[{kind:"runtime_data",target:("session/"+$uuid),certain:true}] and .loss==[] and
  (.details | .runtime.condition=="missing" and
    (.runtime | (has("incus_uuid") or has("original_status") or has("runtime_data_lost") or
      has("runtime_loss_unknown") or has("loss")) | not) and
    .assigned_ref=="refs/heads/third" and .assigned_tip==$oid and
    .branch_loss.commits_losing_p_reachability==[] and (.branch_loss|has("origin")|not)))
' <<< "$missing_plan" >/dev/null
rpc session.inspect "$(jq -nc --arg uuid "$uuid_third" '{uuid:$uuid}')" | \
  jq -e --arg uuid "$uuid_third" '.result.session.uuid==$uuid' >/dev/null
test "$(guest /run/current-system/sw/bin/git ls-remote origin refs/heads/third | cut -f1)" = "$retained_oid"
test "$(guest /run/current-system/sw/bin/cat /home/p/.codex/auth.json)" = dummy-auth-only
inc list "^p-$uuid_keep$" --format json | jq -e 'length==1 and .[0].status=="Running"' >/dev/null
inc list --format json | jq -e 'length==3' >/dev/null
echo P_REMOVAL_PREVIEW_PASS
