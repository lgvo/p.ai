# shellcheck shell=bash
# Public Discard, planned by a dry run and then run with its change tag, of a
# running, unattached session: P stops and deletes the exact runtime and
# removes its credentials without looking inside it. All files are disposable
# fixtures; no authentication is used.
set -E
umask 077
step_dir="$P_TEST_TMP/step-31"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step31
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
  printf 'P_DISCARD_LIFECYCLE_FAIL line=%s status=%s\n' "$2" "$1" >&2
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
activate "$P_TEST_SOURCE/plugins/bundled/tmux-host" "$step_dir/runtime.json" session.asset.install
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
  --arg runtime_activation "$step_dir/runtime.json" \
  --arg host_id "$(jq -er '.plugins[0].id' "$step_dir/runtime.json")" \
  --arg agent_activation "$step_dir/agent.json" \
  --arg agent_id "$(jq -er '.plugins[0].id' "$step_dir/agent.json")" \
  --argjson driver "$driver" --arg prefix "$endpoint_prefix" --arg image "$base" \
  '{schema:"p.host/v1",state_dir:$state,
    git:{listen:"127.0.0.1:0",driver:{name:"git",config:{}}},
    runtime:{activation_path:$runtime_activation,
      host_plugin_id:$host_id,
      agent_adapter:{activation_path:$agent_activation,plugin_id:$agent_id},
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

created=$(rpc project.create '{"key":"discard-bootstrap","project":"discard-lifecycle"}')
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
# plan_discard: the dry run of session.discard of the session sets its plan
# and its change tag.
plan_discard() {
  discard_plan=$(rpc session.discard "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')")
  discard_tag=$(jq -er '.result.change_tag | select(test("^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{64}$"))' <<< "$discard_plan")
}
# discard KEY TAG: runs the discard with idempotency key KEY and change tag
# TAG.
discard() {
  local key="$1" tag="$2"
  rpc session.discard "$(jq -nc --arg key "$key" --arg uuid "$uuid" --arg tag "$tag" \
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
main_oid=$(guest /run/current-system/sw/bin/git rev-parse HEAD)
created_b=$(rpc session.create '{"key":"discard-sibling","project":"discard-lifecycle","branch":"sibling","choice":"new","source":"refs/heads/main"}')
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
  'printf doomed-dummy > /home/p/.codex/auth.json; chmod 0600 /home/p/.codex/auth.json; printf dirty > tracked'

# The plan of a running session does not look inside it: it states that the
# container and everything in it is lost.
plan_discard
jq -e --arg uuid "$uuid" --arg oid "$main_oid" '
  .result.plan | .action=="session.discard" and .target==("session/"+$uuid) and
  .allowed==true and .refusals==[] and .gone==[] and
  .loss==[{kind:"runtime_data",target:("session/"+$uuid),certain:true}] and
  (.details | .project=="discard-lifecycle" and .branch=="main" and
    .assigned_ref=="refs/heads/main" and .assigned_tip==$oid and
    (has("assigned_ref_missing")|not) and (has("branch_loss")|not) and
    .runtime.condition=="present" and .runtime.original_status=="Running" and
    .runtime.name==("p-"+$uuid) and
    (.runtime | (has("loss") or has("loss_operation_id") or has("fingerprint") or
      has("observed_at") or has("runtime_data_lost") or has("runtime_loss_unknown")) | not))
' <<< "$discard_plan" >/dev/null
stale_tag=$discard_tag
# A changed assigned P tip makes the old change tag stale: the run plans
# again, is refused as changed and records no operation.
guest /run/current-system/sw/bin/bash -c \
  'printf tip > tracked; git add tracked; git commit -qm after-preview; git push origin HEAD:main'
main_oid=$(guest /run/current-system/sw/bin/git rev-parse HEAD)
expect_refusal session.discard "$(jq -nc --arg uuid "$uuid" --arg tag "$stale_tag" \
  '{uuid:$uuid,key:"discard-stale",if_match:$tag}')" precondition_failed changed
no_operation discard-stale
source_running
test "$(guest /run/current-system/sw/bin/cat tracked)" = tip
test "$(guest /run/current-system/sw/bin/cat /home/p/.codex/auth.json)" = doomed-dummy
test -f "$state/session_keys/$uuid"
test -f "$state/session_keys/$uuid_b"
test "$(guest /run/current-system/sw/bin/git ls-remote origin refs/heads/main | cut -f1)" = "$main_oid"
test -S "$endpoint_prefix/$uuid/git.sock"
test -S "$endpoint_prefix/$uuid/session.sock"
rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" |
  jq -e '.result.session.registry_state=="established"' >/dev/null
inc list "^p-$uuid_b$" --format json | jq -e 'length==1 and .[0].status=="Running"' >/dev/null
# A refused tag is not spent, but its state stays changed: sent again, it is
# refused the same way.
expect_refusal session.discard "$(jq -nc --arg uuid "$uuid" --arg tag "$stale_tag" \
  '{uuid:$uuid,key:"discard-stale-reused",if_match:$tag}')" precondition_failed changed
no_operation discard-stale-reused
# A real push proves the refused runs left the P ref and Git principal usable.
guest /run/current-system/sw/bin/bash -c 'printf after-stale > tracked; git add tracked; git commit -qm after-stale; git push origin HEAD:main'
main_oid=$(guest /run/current-system/sw/bin/git rev-parse HEAD)
test "$(guest /run/current-system/sw/bin/git ls-remote origin refs/heads/main | cut -f1)" = "$main_oid"

# Changed container contents do not make a tag stale: P does not look
# inside. P stops the running, unattached session itself, then deletes it.
plan_discard
fresh_tag=$discard_tag
guest /run/current-system/sw/bin/bash -c 'printf other > tracked; printf new > untracked'
printf 'p-%s\n' "$uuid" > "$step_dir/removal-arm"
completed_response=$(discard discard-confirmed "$fresh_tag")
discard_op=$(jq -er '.result.operation.id | select(length==36)' <<< "$completed_response")
op_ids+=("$discard_op")
pause_deadline=$((SECONDS+40))
while test ! -f "$step_dir/stop-paused" && ((SECONDS < pause_deadline)); do sleep 0.1; done
test -f "$step_dir/stop-paused"
rpc operation.inspect "$(jq -nc --arg id "$discard_op" '{id:$id}')" |
  jq -e '.result.operation | .status=="running" and .phase=="stop-issued" and .committed==true' >/dev/null
source_running
touch "$step_dir/stop-release"
completed_result=$(wait_operation "$discard_op" completed)
jq -e '.result.operation | .kind=="session.discard" and .phase=="discarded" and
  .status=="completed" and .committed==true' <<< "$completed_result" >/dev/null
# The exact source was stopped, then deleted: never paused or restarted.
printf 'stop p-%s\ndelete p-%s\n' "$uuid" "$uuid" > "$step_dir/source-effects-expected"
cmp "$step_dir/source-effects-expected" "$step_dir/source-effects"
# The same key and tag return the same operation; the same key with another
# tag is another request.
replayed=$(discard discard-confirmed "$fresh_tag")
test "$(jq -er '.result.operation.id' <<< "$replayed")" = "$discard_op"
expect_refusal session.discard "$(jq -nc --arg uuid "$uuid" --arg tag "$stale_tag" \
  '{uuid:$uuid,key:"discard-confirmed",if_match:$tag}')" invalid key_reused
inc list "^p-$uuid$" --format json | jq -e 'length==0' >/dev/null
test ! -e "$endpoint_prefix/$uuid"
test ! -e "$state/session_keys/$uuid"
test -f "$state/session_keys/$uuid_b"
expect_refusal session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" not_found session
expect_refusal session.start "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" not_found session
rpc project.retained_branches '{"project":"discard-lifecycle","limit":8}' |
  jq -e --arg oid "$main_oid" '.result.branches | length==1 and
    .[0].branch=="main" and .[0].oid==$oid' >/dev/null
inc list "^p-$uuid_b$" --format json | jq -e 'length==1 and .[0].status=="Running"' >/dev/null
test "$(inc exec "p-$uuid_b" --user 1000 --group 1000 --cwd /workspace --env HOME=/home/p -- \
  /run/current-system/sw/bin/cat /home/p/.codex/auth.json)" = sibling-dummy
rpc session.inspect "$(jq -nc --arg uuid "$uuid_b" '{uuid:$uuid}')" |
  jq -e '.result.session.session_condition=="ready"' >/dev/null
# The old dummy was inside the removed runtime's private home; no host copy
# or new runtime was created. This is fixture cleanup evidence, not login.
echo P_DISCARD_LIFECYCLE_PASS
