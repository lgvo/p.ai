# shellcheck shell=bash
# Confirmed cleanup after actual assembly-ready startup failure. P does not
# look inside the exact stopped container: the review states it lost by
# category. Dummy private/credential fixtures only; no authentication.
set -E
umask 077
step_dir="$P_TEST_TMP/step-51"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step51
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
daemon_pid=
competing=
next_uuid=
uuid=
sibling=
op_ids=()
unset SSH_AUTH_SOCK GIT_SSH GIT_SSH_COMMAND GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM GIT_CONFIG_COUNT GIT_DIR GIT_WORK_TREE
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
  touch "$step_dir/completion-release" "$step_dir/stale-release"
  stop_daemon
  if test -n "$uuid"; then
    inc delete --force "p-$uuid" >/dev/null 2>&1 || true
    rm -rf -- "${endpoint_prefix:?}/${uuid:?}"
  fi
  if test -n "$sibling"; then
    inc delete --force "p-$sibling" >/dev/null 2>&1 || true
    rm -rf -- "${endpoint_prefix:?}/${sibling:?}"
  fi
  touch "$step_dir/completion-release"
  if test -n "$competing"; then inc delete --force "$competing" >/dev/null 2>&1 || true; fi
  if test -n "$next_uuid"; then
    inc delete --force "p-$next_uuid" >/dev/null 2>&1 || true
    rm -rf -- "${endpoint_prefix:?}/${next_uuid:?}"
  fi
  rmdir "$endpoint_prefix" 2>/dev/null || true
}
on_error() {
  printf 'P_ASSEMBLED_FAILED_CREATE_CLEANUP_FAIL line=%s status=%s\n' "$2" "$1" >&2
  for op in "${op_ids[@]}"; do
    rpc operation.inspect "$(jq -nc --arg id "$op" '{id:$id}')" 2>/dev/null |
      jq -cr '.result.operation | {id,status,phase,diagnostic:(.diagnostic // "")[:512]}' >&2 || true
  done
  tail -c 3072 "$step_dir/daemon.err" >&2 || true
}
trap cleanup EXIT
trap 'on_error "$?" "$LINENO"' ERR

incus_wrapper="$step_dir/incus-wrapper"
printf '#!%s\n' "$(command -v bash)" > "$incus_wrapper"
printf 'incus_real=%q\nfixture=%q\n' "$incus_real" "$step_dir" >> "$incus_wrapper"
cat >> "$incus_wrapper" <<'NATIVE_WRAPPER'
set -euo pipefail
if test "$#" -ge 5 && test -f "$fixture/protect-source"; then
 read -r source < "$fixture/protect-source"
 if test "$5" = "$source"; then
  case "$4" in start|stop|restart|pause|resume|delete|init)
   printf '%s %s\n' "$4" "$5" >> "$fixture/source-effects" ;;
  esac
 fi
fi
# Fixture-only read outage for the retired source after local-complete crash.
# Incus deletion is real. This wrapper never deletes the competing identity
# added by the fixture after restart.
if test "$#" -eq 7 && test "$4" = list && test "$6" = --format && test "$7" = json && test -f "$fixture/native-unavailable"; then
 read -r source < "$fixture/native-unavailable"
 if test "$5" = "^$source$"; then
  printf '%s\n' "$source" > "$fixture/native-unavailable-seen"
  printf 'fixture native observation unavailable\n' >&2
  exit 125
 fi
fi
exec "$incus_real" "$@"
NATIVE_WRAPPER
chmod 0700 "$incus_wrapper"

base=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
test "${#base}" -eq 64
driver=$(bash "$P_TEST_SOURCE/tests/integration/runtime-driver-config.sh" "$incus_wrapper")
# The source driver runs the fixture's Git wrapper, which is made below,
# before the daemon starts.
source_driver=$(bash "$P_TEST_SOURCE/tests/integration/source-driver-config.sh" |
  jq -c --arg git "$step_dir/bin/git" '.config.git=$git')
jq -n --arg state "$state" --argjson source_driver "$source_driver" \
  --argjson driver "$driver" --arg prefix "$endpoint_prefix" --arg image "$base" \
  '{schema:"p.host/v1",state_dir:$state,
    git:{listen:"127.0.0.1:0",driver:$source_driver},
    runtime:{driver:$driver,
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
# The source driver's Git is this wrapper, which runs the host's Git. The
# driver runs its Git with PATH set to the wrapper's directory, so the
# wrapper uses shell builtins and the absolute utilities resolved here.
host_git=$(readlink -f -- "$(command -v git)")
mkdir -m 0700 "$step_dir/bin"
printf '#!%s\n' "$(command -v bash)" > "$step_dir/bin/git"
printf 'host_git=%q\nfixture=%q\np_binary=%q\njq_binary=%q\nsleep_binary=%q\n' "$host_git" "$step_dir" "$(command -v p)" "$(command -v jq)" "$(command -v sleep)" >> "$step_dir/bin/git"
cat >> "$step_dir/bin/git" <<'GIT_WRAPPER'
set -euo pipefail
# Fixture-only pause of the cleanup worker's first precommit ref read, so the
# test can move the assigned branch after the run was accepted. The source
# driver reads a head as --git-dir=REPO show-ref --verify --quiet REF.
if test -f "$fixture/stale-arm" && test "$#" -eq 5 && test "${1%%=*}" = --git-dir && test "$2" = show-ref && test "$3" = --verify && test "$4" = --quiet && test "$5" = refs/heads/work; then
 read -r session_uuid < "$fixture/stale-arm"
 if "$p_binary" api "$fixture/state/control.sock" operation.list '{"limit":20}' |
 "$jq_binary" -e --arg uuid "$session_uuid" '.result.operations|any(.[];.kind=="session.create.cleanup" and .session_uuid==$uuid and .phase=="validated" and .status=="running")' >/dev/null; then
  : > "$fixture/stale-paused"
  for ((attempt=0;attempt<800;attempt++)); do
   if test -f "$fixture/stale-release"; then break; fi
   "$sleep_binary" 0.05
  done
  test -f "$fixture/stale-release" || exit 124
 fi
fi
if test -f "$fixture/completion-arm" && test "$#" -eq 5 && test "${1%%=*}" = --git-dir && test "$2" = show-ref && test "$3" = --verify && test "$4" = --quiet && test "$5" = refs/heads/work; then
 read -r cleanup_id < "$fixture/completion-arm"
 if "$p_binary" api "$fixture/state/control.sock" operation.inspect "$("$jq_binary" -nc --arg id "$cleanup_id" '{id:$id}')" |
 "$jq_binary" -e '.result.operation.kind=="session.create.cleanup" and .result.operation.phase=="local-complete" and .result.operation.status=="running"' >/dev/null; then
  : > "$fixture/completion-paused"
  for ((attempt=0;attempt<800;attempt++)); do
   if test -f "$fixture/completion-release"; then break; fi
   "$sleep_binary" 0.05
  done
  test -f "$fixture/completion-release" || exit 124
 fi
fi
exec "$host_git" "$@"
GIT_WRAPPER
chmod 0700 "$step_dir/bin/git"
start_daemon
rpc system.hello | jq -e '.result.capabilities | index("runtime") != null' >/dev/null
# Loss inspection is not part of the protocol.
if loss=$(rpc workspace.loss.inspect '{}' 2> "$step_dir/api.err"); then
  echo 'workspace.loss.inspect unexpectedly answered' >&2
  exit 1
fi
jq -e '.error.message=="method_not_found" and .error.code==-32601' <<< "$loss" >/dev/null

wait_operation() {
  local id="$1" want="$2" result status deadline=$((SECONDS+170))
  while ((SECONDS < deadline)); do
    result=$(rpc operation.wait "$(jq -nc --arg id "$id" '{id:$id,timeout:20}')")
    status=$(jq -er '.result.operation.status' <<< "$result")
    if test "$status" = "$want"; then printf '%s\n' "$result"; return; fi
    if test "$status" = blocked || test "$status" = failed || test "$status" = completed; then
      jq -c '.result.operation|{id,status,phase,diagnostic:((.diagnostic // "")[:512])}' <<< "$result" >&2
      return 1
    fi
    if test "$status" != running; then sleep 0.3; fi
  done
  echo "operation $id did not reach $want" >&2
  return 1
}
# expect_error CODE KIND REASON METHOD PARAMS: the request fails with the
# error kind (its message), its code and the reason.
expect_error() {
  local code="$1" kind="$2" reason="$3" method="$4" params="$5" response status=0
  response=$(rpc "$method" "$params" 2> "$step_dir/api.err") || status=$?
  test "$status" -eq 1
  jq -e --arg kind "$kind" --arg reason "$reason" --argjson code "$code" \
    '.jsonrpc=="2.0" and .id==2 and (has("result")|not) and
    .error.message==$kind and .error.code==$code and .error.data.reason==$reason' <<< "$response" >/dev/null
}
inspect_creator() { rpc operation.inspect "$(jq -nc --arg id "$creator" '{id:$id}')"; }
assert_source() {
  inc list "^p-$uuid$" --format json | jq -e --arg native "$native_uuid" --arg generation "$generation" \
    'length==1 and .[0].status=="Stopped" and
     .[0].config["volatile.uuid"]==$native and .[0].config["volatile.uuid.generation"]==$generation and
     .[0].expanded_config["security.idmap.isolated"]=="true" and
     (.[] | .expanded_config["security.nesting"] // "false")=="false"' >/dev/null
  inspect_creator | jq -e --arg uuid "$uuid" --arg oid "$tip" \
    '.result.operation | .session_uuid==$uuid and .status=="blocked" and .phase=="assembly-ready" and
     .evidence.runtime_init_state=="attempted" and .evidence.captured_oid==$oid' >/dev/null
  rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" |
    jq -e --arg uuid "$uuid" '.result.session | .uuid==$uuid and .branch=="work" and .registry_state=="creating"' >/dev/null
  test ! -e "$step_dir/source-effects"
  test "$(git -C "$repo" for-each-ref --format='%(refname) %(objectname)')" = "$refs_before"
  test "$(sha256sum "$state/session_keys/$uuid" | cut -d' ' -f1)" = "$key_before"
  for path in workspace/tracked workspace/untracked workspace/ignored.bin workspace/.git/config \
    workspace/.git/HEAD workspace/.git/refs/heads/work workspace/.git/hooks/post-checkout \
    home/p/p-loss-private home/p/p-loss-dummy-credential etc/p/git/identity; do
    rm -f -- "$step_dir/after"
    inc file pull "p-$uuid/$path" "$step_dir/after"
    cmp "$step_dir/before-${path//\//-}" "$step_dir/after"
  done
  inc file pull "p-$sibling/workspace/tracked" "$step_dir/sibling-after"
  cmp "$step_dir/sibling-before" "$step_dir/sibling-after"
}

# Establish and commit source with a working host first. The failed creator
# uses the same selected asset packages and a newly captured false command.
created=$(rpc project.create '{"key":"bootstrap","project":"failed-create-loss"}')
uuid=$(jq -er '.result.operation.session_uuid|select(length==36)' <<< "$created")
bootstrap=$(jq -er '.result.operation.id|select(length==36)' <<< "$created")
wait_operation "$bootstrap" completed >/dev/null
guest /run/current-system/sw/bin/bash -c 'printf committed > tracked; printf "ignored.bin\n" > .gitignore'
guest /run/current-system/sw/bin/git add tracked .gitignore
guest /run/current-system/sw/bin/git -c user.name=P -c user.email=p@example.invalid commit -qm source
guest /run/current-system/sw/bin/git push origin HEAD:refs/heads/main
tip=$(guest /run/current-system/sw/bin/git rev-parse HEAD)
inc file pull "p-$uuid/workspace/tracked" "$step_dir/sibling-before"
sibling=$uuid
uuid=
stop_daemon
jq '.runtime.project_policy.command=["/run/current-system/sw/bin/false"]' "$step_dir/host.json" > "$step_dir/host-failed.json"
mv "$step_dir/host-failed.json" "$step_dir/host.json"
start_daemon
request='{"key":"failed-startup","project":"failed-create-loss","branch":"work","choice":"new","source":"refs/heads/main"}'
created=$(rpc session.create "$request")
uuid=$(jq -er '.result.operation.session_uuid|select(length==36)' <<< "$created")
creator=$(jq -er '.result.operation.id|select(length==36)' <<< "$created")
blocked=$(wait_operation "$creator" blocked)
jq -e --arg tip "$tip" '.result.operation | .kind=="session.create" and .phase=="assembly-ready" and
 .committed==true and (.diagnostic|type=="string" and length>0) and
 .evidence.runtime_init_state=="attempted" and .evidence.captured_oid==$tip and
 .evidence.environment==null and .evidence.image==null' <<< "$blocked" >/dev/null
inc list "^p-$uuid$" --format json | jq -e 'length==1 and .[0].status=="Stopped"' >/dev/null
native_uuid=$(inc list "^p-$uuid$" --format json | jq -er '.[0].config["volatile.uuid"]')
generation=$(inc list "^p-$uuid$" --format json | jq -er '.[0].config["volatile.uuid.generation"]')
# Real stopped Incus file API edits preserve a workspace and private/dummy
# credential sentinels without launching the failed command or authenticating.
printf dirty > "$step_dir/tracked"
printf untracked > "$step_dir/untracked"
printf ignored > "$step_dir/ignored"
printf dummy-private > "$step_dir/private"
printf dummy-credential > "$step_dir/credential"
printf '#!/bin/sh\nexit 99\n' > "$step_dir/hook"
inc file push --uid 1000 --gid 1000 --mode 0644 "$step_dir/tracked" "p-$uuid/workspace/tracked"
inc file push --uid 1000 --gid 1000 --mode 0644 "$step_dir/untracked" "p-$uuid/workspace/untracked"
inc file push --uid 1000 --gid 1000 --mode 0644 "$step_dir/ignored" "p-$uuid/workspace/ignored.bin"
inc file push --uid 1000 --gid 1000 --mode 0600 "$step_dir/private" "p-$uuid/home/p/p-loss-private"
inc file push --uid 1000 --gid 1000 --mode 0600 "$step_dir/credential" "p-$uuid/home/p/p-loss-dummy-credential"
inc file push --uid 1000 --gid 1000 --mode 0755 "$step_dir/hook" "p-$uuid/workspace/.git/hooks/post-checkout"
for path in workspace/tracked workspace/untracked workspace/ignored.bin workspace/.git/config \
  workspace/.git/HEAD workspace/.git/refs/heads/work workspace/.git/hooks/post-checkout \
  home/p/p-loss-private home/p/p-loss-dummy-credential etc/p/git/identity; do
  inc file pull "p-$uuid/$path" "$step_dir/before-${path//\//-}"
done
repo="$state/repositories/$(printf %s failed-create-loss | sha256sum | cut -d' ' -f1).git"
refs_before=$(git -C "$repo" for-each-ref --format='%(refname) %(objectname)')
key_before=$(sha256sum "$state/session_keys/$uuid" | cut -d' ' -f1)
printf 'p-%s\n' "$uuid" > "$step_dir/protect-source"
assert_source
# plan_cleanup: the cleanup's dry run. run_params KEY TAG: its run's params,
# the same plus the key and the dry run's change tag.
plan_cleanup() {
  rpc session.create.cleanup "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')"
}
run_params() {
  jq -nc --arg uuid "$uuid" --arg key "$1" --arg tag "$2" '{uuid:$uuid,key:$key,if_match:$tag}'
}
change_tag() { jq -er '.result.change_tag | select(test("^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{64}$"))' <<< "$1"; }
assert_retained() {
  test "$(git -C "$repo" for-each-ref --format='%(refname) %(objectname)')" = "$refs_before"
  test "$(inc image list --format json | jq -c '[.[].fingerprint]|sort')" = "$images_before"
  inc file pull "p-$sibling/workspace/tracked" "$step_dir/sibling-retained"
  cmp "$step_dir/sibling-before" "$step_dir/sibling-retained"
  inc list "^p-$sibling$" --format json | jq -e 'length==1 and .[0].status=="Running"' >/dev/null
}
# This daemon has no image builders configured: no builder may exist at all.
assert_old_effects_absent() {
  test ! -e "$state/session_keys/$uuid"
  test ! -e "$endpoint_prefix/$uuid"
  inc list --format json | jq -e --arg uuid "$uuid" \
    'all(.[]; .name!=("p-"+$uuid) and (.name|startswith("p-builder-")|not) and
     (.config["user.p.session_uuid"] // "")!=$uuid and
     (.expanded_config["user.p.session_uuid"] // "")!=$uuid)' >/dev/null
}
images_before=$(inc image list --format json | jq -c '[.[].fingerprint]|sort')
# The removed loss inspection parameter is refused by strict decoding.
expect_error -32602 invalid param session.create.cleanup "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid,loss_operation_id:$uuid}')"
# P picks the assembled path itself and names the exact stopped container,
# whose data the plan lists as lost without looking inside.
plan=$(plan_cleanup)
jq -e --arg creator "$creator" --arg native "$native_uuid" --arg generation "$generation" --arg uuid "$uuid" --arg image "$base" \
 '.result | (.change_tag|test("^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{64}$")) and
 (.plan | .action=="session.create.cleanup" and .target==("session/"+$uuid) and
 .allowed==true and .refusals==[] and .gone==[] and
 .loss==[{kind:"runtime_data",target:("session/"+$uuid),certain:true}] and (.details |
 .old_operation_id==$creator and .old_phase=="assembly-ready" and
 (.old_request_sha256|test("^[0-9a-f]{64}$")) and
 .runtime.condition=="present" and .runtime.original_status=="Stopped" and
 .runtime.incarnation==($native+"/"+$generation) and .runtime.name==("p-"+$uuid) and
 .runtime.image_fingerprint==$image and
 (.runtime | has("runtime_data_lost") or has("loss") or has("loss_operation_id") or has("fingerprint") or has("observed_at") | not) and
 .provisional.runtime=="present" and .provisional.runtime_local=="will_be_removed" and
 .provisional.assigned_ref=="preserved_existing" and .shared_images=="preserved" and .external_mounts=="preserved"))' <<< "$plan" >/dev/null
tag=$(change_tag "$plan")
assert_source
# A branch moved between the dry run and the run makes the run stale: it
# plans again, is refused, and records no operation.
moved=$(git -C "$repo" -c user.name=P -c user.email=p@example.invalid commit-tree "$(git -C "$repo" rev-parse "$tip^{tree}")" -p "$tip" -m moved-after-review)
git -C "$repo" update-ref refs/heads/work "$moved" "$tip"
expect_error -32013 precondition_failed changed session.create.cleanup "$(run_params moved-before-run "$tag")"
rpc operation.list '{"limit":20}' | jq -e '.result.operations | all(.[]; .kind!="session.create.cleanup")' >/dev/null
git -C "$repo" update-ref refs/heads/work "$tip" "$moved"
assert_source
# A branch moved after the run was accepted but before the cleanup committed
# fails it as stale and preserves everything.
plan=$(plan_cleanup)
jq -e '.result.plan.allowed==true' <<< "$plan" >/dev/null
tag=$(change_tag "$plan")
printf '%s\n' "$uuid" > "$step_dir/stale-arm"
ran=$(rpc session.create.cleanup "$(run_params stale-cleanup "$tag")")
stale_id=$(jq -er '.result.operation.id|select(length==36)' <<< "$ran")
op_ids+=("$stale_id")
deadline=$((SECONDS+100))
while test ! -f "$step_dir/stale-paused" && ((SECONDS<deadline)); do sleep 0.1; done
test -f "$step_dir/stale-paused"
rpc operation.inspect "$(jq -nc --arg id "$stale_id" '{id:$id}')" |
 jq -e '.result.operation | .status=="running" and .phase=="validated" and .committed==false' >/dev/null
git -C "$repo" update-ref refs/heads/work "$moved" "$tip"
touch "$step_dir/stale-release"
stale_result=$(wait_operation "$stale_id" failed)
jq -e '.result.operation | .phase=="stale" and .committed==false and .reason=="changed" and
 (.diagnostic|contains("confirmation stale; source preserved"))' <<< "$stale_result" >/dev/null
rm "$step_dir/stale-arm"
git -C "$repo" update-ref refs/heads/work "$tip" "$moved"
assert_source
expect_error -32011 conflict operation_not_retryable operation.retry "$(jq -nc --arg id "$stale_id" '{id:$id}')"
# A new dry run, tag and key are the supported settled-stale recovery
# procedure. The same original identity is retained.
plan=$(plan_cleanup)
jq -e '.result.plan.allowed==true' <<< "$plan" >/dev/null
tag=$(change_tag "$plan")
# P does not look inside the container: workspace bytes changed after the
# dry run do not refuse the cleanup.
printf changed-after-review > "$step_dir/changed"
inc file push --uid 1000 --gid 1000 --mode 0644 "$step_dir/changed" "p-$uuid/workspace/tracked"
printf 'pending\n' > "$step_dir/completion-arm"
ran_status=0
ran=$(rpc session.create.cleanup "$(run_params cleanup-reviewed "$tag")") || ran_status=$?
if test "$ran_status" -ne 0; then
  jq -c --argjson status "$ran_status" \
    '{method:"session.create.cleanup",status:$status,error:{code:.error.code,kind:.error.message,data:.error.data}}' \
    <<< "$ran" >&2 || true
fi
test "$ran_status" -eq 0
cleanup_id=$(jq -er '.result.operation.id|select(length==36)' <<< "$ran")
op_ids+=("$cleanup_id")
printf '%s\n' "$cleanup_id" > "$step_dir/completion-arm"
params=$(run_params cleanup-reviewed "$tag")
deadline=$((SECONDS+100))
while test ! -f "$step_dir/completion-paused" && ((SECONDS<deadline)); do sleep 0.1; done
test -f "$step_dir/completion-paused"
rpc operation.inspect "$(jq -nc --arg id "$cleanup_id" '{id:$id}')" |
 jq -e '.result.operation | .status=="running" and .phase=="local-complete" and .committed==true and
 .evidence.runtime_absent==true and .evidence.local_complete==true' >/dev/null
inspect_creator | jq -e '.result.operation | .status=="superseded" and .phase=="superseded"' >/dev/null
rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" |
 jq -e '.result.session.registry_state=="removing"' >/dev/null
expect_error -32011 conflict operation_superseded operation.retry "$(jq -nc --arg id "$creator" '{id:$id}')"
replayed=$(rpc session.create "$request")
jq -e --arg id "$creator" '.result.operation.id==$id and .result.operation.status=="superseded"' <<< "$replayed" >/dev/null
assert_old_effects_absent
assert_retained
test "$(cat "$step_dir/source-effects")" = "delete p-$uuid"
# Actual crash at durable local-complete; injected read-only outage after
# restart must retain removing identity, exact cleanup intent and ref guard.
stop_daemon
rm "$step_dir/completion-arm"
touch "$step_dir/completion-release"
printf 'p-%s\n' "$uuid" > "$step_dir/native-unavailable"
start_daemon
blocked=$(wait_operation "$cleanup_id" blocked)
jq -e '.result.operation | .phase=="local-complete" and .committed==true and
 .evidence.runtime_absent==true and .evidence.local_complete==true' <<< "$blocked" >/dev/null
test "$(cat "$step_dir/native-unavailable-seen")" = "p-$uuid"
rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" |
 jq -e '.result.session.registry_state=="removing"' >/dev/null
assert_old_effects_absent
assert_retained
replayed=$(rpc session.create.cleanup "$params")
jq -e --arg id "$cleanup_id" '.result.operation.id==$id' <<< "$replayed" >/dev/null
expect_error -32011 conflict operation_superseded operation.retry "$(jq -nc --arg id "$creator" '{id:$id}')"
printf 'P_ASSEMBLED_CLEANUP_NATIVE_UNAVAILABLE_PRESERVED\n'
# A newly competing session UUID blocks exact absence recovery. It is fixture
# machinery: cleanup must retain it and never adopt/delete by its name.
competing="p-recovery-$uuid"
inc init "$base" "$competing" --profile default --config security.idmap.isolated=true \
 --config security.privileged=false --config security.nesting=false --config "user.p.session_uuid=$uuid"
competing_uuid=$(inc list "^$competing$" --format json | jq -er '.[0].config["volatile.uuid"]')
rm "$step_dir/native-unavailable"
rpc operation.retry "$(jq -nc --arg id "$cleanup_id" '{id:$id}')" >/dev/null
wait_operation "$cleanup_id" blocked >/dev/null
test "$(inc list "^$competing$" --format json | jq -er '.[0].config["volatile.uuid"]')" = "$competing_uuid"
assert_retained
inc delete "$competing"
competing=
rpc operation.retry "$(jq -nc --arg id "$cleanup_id" '{id:$id}')" >/dev/null
wait_operation "$cleanup_id" completed >/dev/null
assert_old_effects_absent
assert_retained
rpc session.list '{"limit":8}' | jq -e --arg uuid "$uuid" '.result.sessions|all(.[];.uuid!=$uuid)' >/dev/null
test "$(cat "$step_dir/source-effects")" = "delete p-$uuid"
# Corrected Create is explicit and separate, claims the retained branch, and
# obtains a new UUID. No old Create replay may reinitialize its source.
stop_daemon
jq '.runtime.project_policy.command=["/run/current-system/sw/bin/bash"]' "$step_dir/host.json" > "$step_dir/host-corrected.json"
mv "$step_dir/host-corrected.json" "$step_dir/host.json"
start_daemon
replayed=$(rpc session.create "$request")
jq -e --arg id "$creator" '.result.operation.id==$id and .result.operation.status=="superseded"' <<< "$replayed" >/dev/null
created=$(rpc session.create '{"key":"corrected-create","project":"failed-create-loss","branch":"work","choice":"existing"}')
next_uuid=$(jq -er '.result.operation.session_uuid|select(length==36)' <<< "$created")
next_id=$(jq -er '.result.operation.id|select(length==36)' <<< "$created")
test "$next_uuid" != "$uuid"
wait_operation "$next_id" completed >/dev/null
rpc session.inspect "$(jq -nc --arg uuid "$next_uuid" '{uuid:$uuid}')" |
 jq -e '.result.session.registry_state=="established" and .result.session.session_condition=="ready"' >/dev/null
assert_old_effects_absent
assert_retained
test "$(cat "$step_dir/source-effects")" = "delete p-$uuid"
printf 'P_ASSEMBLED_FAILED_CREATE_CLEANUP_PASS\n'
