# shellcheck shell=bash
# Public Delete, planned by a dry run and then run with its change tag, of a
# running, unattached session: P stops and deletes the exact runtime, removes
# its credentials and its guarded P ref, without looking inside the runtime
# or reading the origin. All files are disposable fixtures; no
# authentication is used.
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
  touch "$step_dir/stop-release"
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
  printf 'P_DELETE_LIFECYCLE_FAIL line=%s status=%s\n' "$2" "$1" >&2
  for op in "${op_ids[@]}"; do
    rpc operation.inspect "$(jq -nc --arg id "$op" '{id:$id}')" 2>/dev/null |
      jq -cr '.result.operation | {id,status,phase,diagnostic:(.diagnostic // "")[:512]}' >&2 || true
  done
  tail -c 1024 "$step_dir/source-effects" >&2 || true
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
activate "$P_TEST_SOURCE/plugins/bundled/codex-adapter" "$step_dir/agent.json" agent.status.report

# Record P's Incus effects on the armed source and hold its stop until the
# fixture releases it, so the removal is observed at stop-issued. Incus runs
# with PATH=/usr/bin:/bin, so use shell builtins and absolute utilities.
incus_wrapper="$step_dir/incus-wrapper"
printf '#!%s\n' "$(command -v bash)" > "$incus_wrapper"
printf 'incus_real=%q\nfixture=%q\nsleep_binary=%q\n' "$incus_real" "$step_dir" "$(command -v sleep)" >> "$incus_wrapper"
cat >> "$incus_wrapper" <<'NATIVE_WRAPPER'
set -euo pipefail
if test "$#" -ge 5 && test -f "$fixture/removal-arm"; then
 read -r armed < "$fixture/removal-arm"
 if test "$5" = "$armed"; then
  case "$4" in start|stop|restart|pause|resume|delete)
   printf '%s %s\n' "$4" "$5" >> "$fixture/source-effects" ;;
  esac
  if test "$4" = stop; then
   : > "$fixture/stop-paused"
   for ((attempt=0; attempt<800; attempt++)); do
    if test -f "$fixture/stop-release"; then break; fi
    "$sleep_binary" 0.05
   done
   test -f "$fixture/stop-release" || exit 124
  fi
 fi
fi
exec "$incus_real" "$@"
NATIVE_WRAPPER
chmod 0700 "$incus_wrapper"

base=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
test "${#base}" -eq 64
driver=$(bash "$P_TEST_SOURCE/tests/integration/runtime-driver-config.sh" "$incus_wrapper")
jq -n --arg state "$state" \
  --arg agent_activation "$step_dir/agent.json" \
  --arg agent_id "$(jq -er '.plugins[0].id' "$step_dir/agent.json")" \
  --argjson driver "$driver" --arg prefix "$endpoint_prefix" --arg image "$base" \
  '{schema:"p.host/v1",state_dir:$state,
    git:{listen:"127.0.0.1:0",driver:{name:"git",config:{}}},
    runtime:{agent_adapter:{activation_path:$agent_activation,plugin_id:$agent_id},
      driver:$driver,
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

created=$(rpc project.create '{"key":"delete-bootstrap","project":"delete-lifecycle"}')
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
source_running() {
  inc list "^p-$uuid$" --format json | jq -e --arg name "p-$uuid" \
    'length==1 and .[0].name==$name and .[0].status=="Running"' >/dev/null
}
tag_pattern='^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{64}$'
# plan_delete: the dry run of session.delete of the session sets its plan and
# its change tag.
plan_delete() {
  delete_plan=$(rpc session.delete "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')")
  delete_tag=$(jq -er --arg tag "$tag_pattern" '.result.change_tag | select(test($tag))' <<< "$delete_plan")
}
# delete_run KEY TAG: runs the delete with idempotency key KEY and change tag
# TAG.
delete_run() {
  local key="$1" tag="$2"
  rpc session.delete "$(jq -nc --arg key "$key" --arg uuid "$uuid" --arg tag "$tag" \
    '{uuid:$uuid,key:$key,if_match:$tag}')"
}
# no_operation KEY: no operation is recorded with idempotency key KEY.
no_operation() {
  rpc operation.list '{"limit":20}' | jq -e --arg key "$1" \
    '.result.operations | all(.[]; .idempotency_key!=$key)' >/dev/null
}

guest /run/current-system/sw/bin/git config user.name P
guest /run/current-system/sw/bin/git config user.email p@example.invalid
guest /run/current-system/sw/bin/bash -c \
  'printf base > tracked; git add tracked; git commit -qm base; git push origin HEAD:main'
base_oid=$(guest /run/current-system/sw/bin/git rev-parse HEAD)
created_b=$(rpc session.create '{"key":"delete-sibling","project":"delete-lifecycle","branch":"sibling","choice":"new","source":"refs/heads/main"}')
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
# The plan lists the commits leaving P's repository. It does not look inside
# the running container or compare anything with an origin.
plan_delete
stale_tag=$delete_tag
jq -e --arg uuid "$uuid" --arg unique "$unique_oid" --arg base "$base_oid" '
  .result.plan | .action=="session.delete" and .target==("session/"+$uuid) and
  .allowed==true and .refusals==[] and .gone==[] and
  .loss==[{kind:"runtime_data",target:("session/"+$uuid),certain:true},
    {kind:"p_commits",target:"project/delete-lifecycle:branch/main",count:1,sample:[$unique],certain:true}] and
  (.details | .assigned_ref=="refs/heads/main" and .assigned_tip==$unique and
    (has("assigned_ref_missing")|not) and
    .runtime.condition=="present" and .runtime.original_status=="Running" and
    (.runtime | (has("loss") or has("loss_operation_id") or has("fingerprint") or
      has("observed_at") or has("runtime_data_lost") or has("runtime_loss_unknown")) | not) and
    (.branch_loss | .assigned_ref=="refs/heads/main" and .assigned_tip==$unique and
      .commits_losing_p_reachability==[$unique] and
      any(.p_refs[]; .name=="refs/heads/sibling" and .oid==$base) and
      (has("origin")|not)))
' <<< "$delete_plan" >/dev/null
# A changed assigned tip requires another dry run before any irreversible
# point: the run plans again, is refused as changed and records nothing.
guest /run/current-system/sw/bin/bash -c \
  'printf after > tracked; git add tracked; git commit -qm after-preview; git push origin HEAD:main'
final_oid=$(guest /run/current-system/sw/bin/git rev-parse HEAD)
expect_refusal session.delete "$(jq -nc --arg uuid "$uuid" --arg tag "$stale_tag" \
  '{uuid:$uuid,key:"delete-stale",if_match:$tag}')" precondition_failed changed
no_operation delete-stale
source_running
test "$(guest /run/current-system/sw/bin/cat /home/p/.codex/auth.json)" = doomed-dummy
test -f "$state/session_keys/$uuid"
test -S "$endpoint_prefix/$uuid/git.sock"
test "$(guest /run/current-system/sw/bin/git ls-remote origin refs/heads/main | cut -f1)" = "$final_oid"
# A refused tag is not spent, but its state stays changed: sent again, it is
# refused the same way.
expect_refusal session.delete "$(jq -nc --arg uuid "$uuid" --arg tag "$stale_tag" \
  '{uuid:$uuid,key:"delete-stale-reused",if_match:$tag}')" precondition_failed changed
no_operation delete-stale-reused
plan_delete
fresh_tag=$delete_tag
jq -e --arg uuid "$uuid" --arg final "$final_oid" --arg unique "$unique_oid" --arg base "$base_oid" '
  .result.plan | .allowed==true and .details.assigned_tip==$final and
  .loss[0]=={kind:"runtime_data",target:("session/"+$uuid),certain:true} and
  (.loss[1] | .kind=="p_commits" and .target=="project/delete-lifecycle:branch/main" and
    .count==2 and (.sample|sort)==([$final,$unique]|sort) and .certain==true) and
  (.details.branch_loss | (.commits_losing_p_reachability|sort)==([$final,$unique]|sort) and
    any(.p_refs[]; .name=="refs/heads/sibling" and .oid==$base) and
    (has("origin")|not))
' <<< "$delete_plan" >/dev/null
# A Discard plan cannot authorize a Delete: the change tag of the same
# session's discard dry run does not run its delete.
discard_plan=$(rpc session.discard "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')")
jq -e --arg uuid "$uuid" '.result.plan | .action=="session.discard" and .target==("session/"+$uuid)' \
  <<< "$discard_plan" >/dev/null
discard_tag=$(jq -er --arg tag "$tag_pattern" '.result.change_tag | select(test($tag))' <<< "$discard_plan")
test "$discard_tag" != "$fresh_tag"
expect_refusal session.delete "$(jq -nc --arg uuid "$uuid" --arg tag "$discard_tag" \
  '{uuid:$uuid,key:"delete-wrong-action",if_match:$tag}')" precondition_failed changed
no_operation delete-wrong-action
source_running
# P stops the running, unattached session itself, then deletes it.
printf 'p-%s\n' "$uuid" > "$step_dir/removal-arm"
completed_response=$(delete_run delete-confirmed "$fresh_tag")
delete_op=$(jq -er '.result.operation.id | select(length==36)' <<< "$completed_response")
op_ids+=("$delete_op")
pause_deadline=$((SECONDS+40))
while test ! -f "$step_dir/stop-paused" && ((SECONDS < pause_deadline)); do sleep 0.1; done
test -f "$step_dir/stop-paused"
rpc operation.inspect "$(jq -nc --arg id "$delete_op" '{id:$id}')" |
  jq -e '.result.operation | .status=="running" and .phase=="stop-issued" and .committed==true' >/dev/null
source_running
touch "$step_dir/stop-release"
completed_result=$(wait_operation "$delete_op" completed)
jq -e '.result.operation | .kind=="session.delete" and .phase=="deleted" and
  .status=="completed" and .committed==true' <<< "$completed_result" >/dev/null
# The exact source was stopped, then deleted: never paused or restarted.
printf 'stop p-%s\ndelete p-%s\n' "$uuid" "$uuid" > "$step_dir/source-effects-expected"
cmp "$step_dir/source-effects-expected" "$step_dir/source-effects"
replayed=$(delete_run delete-confirmed "$fresh_tag")
test "$(jq -er '.result.operation.id' <<< "$replayed")" = "$delete_op"
inc list "^p-$uuid$" --format json | jq -e 'length==0' >/dev/null
test ! -e "$endpoint_prefix/$uuid"
test ! -e "$state/session_keys/$uuid"
expect_refusal session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" not_found session
expect_refusal session.start "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" not_found session
rpc project.branches '{"project":"delete-lifecycle","limit":8}' |
  jq -e --arg base "$base_oid" '.result.refs | length==1 and
    .[0].ref=="refs/heads/sibling" and .[0].oid==$base' >/dev/null
rpc project.retained_branches '{"project":"delete-lifecycle","limit":8}' |
  jq -e '.result.branches==[]' >/dev/null
inc list "^p-$uuid_b$" --format json | jq -e 'length==1 and .[0].status=="Running"' >/dev/null
test -f "$state/session_keys/$uuid_b"
test "$(inc exec "p-$uuid_b" --user 1000 --group 1000 --cwd /workspace --env HOME=/home/p -- \
  /run/current-system/sw/bin/cat /home/p/.codex/auth.json)" = sibling-dummy
rpc session.inspect "$(jq -nc --arg uuid "$uuid_b" '{uuid:$uuid}')" |
  jq -e '.result.session.session_condition=="ready"' >/dev/null
# No login occurred. Only A's dummy private home/key was removed by public Delete.
echo P_DELETE_LIFECYCLE_PASS
