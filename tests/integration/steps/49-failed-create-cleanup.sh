# shellcheck shell=bash
# Actual immutable invalid Nix resolution, explicit old-UUID cleanup and
# separate corrected Create. No authenticated external credential is involved.
set -E
umask 077
step_dir="$P_TEST_TMP/step-49"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step49
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
unset SSH_AUTH_SOCK GIT_SSH GIT_SSH_COMMAND GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM GIT_CONFIG_COUNT GIT_DIR GIT_WORK_TREE
daemon_pid=
competing=
p_instance=
sessions=()
inc() { timeout 45 incus --force-local --project user-1000 "$@"; }
rpc() { timeout 30 p api "$socket" "$@"; }
exec_p() {
  local uuid="$1"
  shift
  inc exec "p-$uuid" --user 1000 --group 1000 --cwd /workspace \
    --env HOME=/home/p --env GIT_SSH=/usr/libexec/p/git-ssh -- "$@"
}
stop_process() {
  local pid="$1" attempt
  if test -z "$pid"; then return; fi
  kill -TERM "$pid" 2>/dev/null || true
  for ((attempt=0; attempt<100; attempt++)); do
    if ! kill -0 "$pid" 2>/dev/null; then break; fi
    sleep 0.1
  done
  if kill -0 "$pid" 2>/dev/null; then kill -KILL "$pid" 2>/dev/null || true; fi
  wait "$pid" 2>/dev/null || true
}
cleanup() {
  # Release a paused child before stopping its daemon, including failure paths.
  touch "$step_dir/cleanup-release"
  stop_process "$daemon_pid"
  for uuid in "${sessions[@]}"; do
    inc delete --force "p-$uuid" >/dev/null 2>&1 || true
    rm -rf -- "${endpoint_prefix:?}/${uuid:?}"
  done
  if test -n "$competing"; then inc delete --force "$competing" >/dev/null 2>&1 || true; fi
  # The stopped daemon no longer releases the commit image it built.
  if test -n "$p_instance"; then
    inc image list --format json 2>/dev/null |
      jq -r --arg instance "$p_instance" '.[] | select(.properties["p.instance"]==$instance) | .fingerprint' |
      while IFS= read -r image; do inc image delete "$image" >/dev/null 2>&1 || true; done || true
  fi
  rmdir "$endpoint_prefix" 2>/dev/null || true
}
on_error() {
  printf 'P_FAILED_CREATE_CLEANUP_FAIL line=%s status=%s\n' "$2" "$1" >&2
  if test -n "${plan:-}"; then
    jq -c '.result | {allowed:.plan.allowed,refusals:((.plan.refusals // [])[:8]),
      has_change_tag:has("change_tag"),old_status:.plan.details.old_status,
      old_phase:.plan.details.old_phase,provisional:.plan.details.provisional}' \
      <<< "$plan" | head -c 2048 >&2 || true
    printf '\n' >&2
  fi
  timeout 5 p api "$socket" operation.list '{"limit":20}' >&2 || true
  tail -c 2048 "$step_dir/daemon.err" >&2 || true
}
trap cleanup EXIT
trap 'on_error "$?" "$LINENO"' ERR
# expect_error KIND REASON METHOD PARAMS: the request fails with the error
# kind (its message), the kind's code and the reason.
expect_error() {
  local kind="$1" reason="$2" method="$3" params="$4" code response status
  case "$kind" in
    invalid) code=-32602 ;;
    retry) code=-32010 ;;
    conflict) code=-32011 ;;
    precondition_failed) code=-32013 ;;
    not_found) code=-32015 ;;
    *) echo "unsupported expected error: $kind" >&2; return 1 ;;
  esac
  status=0
  response=$(rpc "$method" "$params" 2> "$step_dir/api.err") || status=$?
  if test "$status" -ne 1 || ! jq -e --arg kind "$kind" --arg reason "$reason" --argjson code "$code" \
    '.jsonrpc=="2.0" and .id==2 and (has("result")|not) and
     .error.message==$kind and .error.code==$code and .error.data.reason==$reason' \
    <<< "$response" >/dev/null; then
    jq -c --arg method "$method" --arg expected_kind "$kind" --arg expected_reason "$reason" \
      --argjson expected_code "$code" --argjson actual_status "$status" \
      '{method:$method,expected_kind:$expected_kind,expected_reason:$expected_reason,expected_code:$expected_code,
        actual_status:$actual_status,actual_kind:(.error.message // null),
        actual_code:(.error.code // null),actual_data:(.error.data // null)}' \
      <<< "$response" >&2 || printf 'error response malformed for %s\n' "$method" >&2
    return 1
  fi
}
operation_id() { jq -er '.result.operation.id | select(type=="string" and length==36)' <<< "$1"; }
session_uuid() { jq -er '.result.operation.session_uuid | select(type=="string" and length==36)' <<< "$1"; }
inspect_operation() { rpc operation.inspect "$(jq -nc --arg id "$1" '{id:$id}')"; }
inspect_session() { rpc session.inspect "$(jq -nc --arg uuid "$1" '{uuid:$uuid}')"; }
branches() { rpc project.branches "$(jq -nc --arg project "$1" '{project:$project,limit:8}')"; }
wait_operation() {
  local id="$1" expected="$2" result status deadline=$((SECONDS+170))
  while ((SECONDS < deadline)); do
    result=$(rpc operation.wait "$(jq -nc --arg id "$id" '{id:$id,timeout:20}')")
    status=$(jq -er '.result.operation.status' <<< "$result")
    if test "$status" = "$expected"; then printf '%s\n' "$result"; return; fi
    if { test "$status" = blocked || test "$status" = completed; } && test "$status" != "$expected"; then
      echo 'unexpected operation state' >&2
      jq -c '.result.operation | {status,phase,diagnostic:((.diagnostic // "")[:400])}' <<< "$result" >&2
      return 1
    fi
    if test "$status" != running; then sleep 0.4; fi
  done
  echo "operation $id did not reach $expected" >&2
  return 1
}
wait_ready() {
  local uuid="$1" result deadline=$((SECONDS+80))
  while ((SECONDS < deadline)); do
    result=$(inspect_session "$uuid")
    if jq -e --arg uuid "$uuid" \
      '.result.session.uuid==$uuid and .result.session.registry_state=="established" and
       .result.session.session_condition=="ready"' <<< "$result" >/dev/null; then return; fi
    sleep 0.4
  done
  echo "session $uuid did not become ready" >&2
  return 1
}
# A creation from a commit builds the commit's image before its runtime runs.
release_on_running() {
  local uuid="$1" id="$2" deadline=$((SECONDS+300)) detail
  while ((SECONDS < deadline)); do
    detail=$(inspect_operation "$id")
    if jq -e '.result.operation.status=="blocked"' <<< "$detail" >/dev/null; then
      echo 'creation blocked before runtime release' >&2
      jq -c '.result.operation | {status,phase,diagnostic:((.diagnostic // "")[:400])}' <<< "$detail" >&2
      return 1
    fi
    if inc list "p-$uuid" --format json 2>/dev/null |
      jq -e --arg name "p-$uuid" 'any(.[]; .name==$name and .status=="Running")' >/dev/null 2>&1; then
      if inc file push --uid 0 --gid 0 --mode 0644 \
        "$step_dir/release" "p-$uuid/etc/p/test-release" 2> "$step_dir/release.err"; then return; fi
    fi
    sleep 0.3
  done
  echo "runtime $uuid did not accept release" >&2
  return 1
}
start_daemon() {
  : > "$step_dir/daemon.err"
  p daemon "$step_dir/host.json" > "$step_dir/daemon.out" 2> "$step_dir/daemon.err" &
  daemon_pid=$!
  local deadline=$((SECONDS+30))
  while ((SECONDS < deadline)); do
    if test -S "$socket" && timeout 3 p api "$socket" system.health |
      jq -e '.result.control_state=="ready"' >/dev/null 2>&1; then return; fi
    if ! kill -0 "$daemon_pid" 2>/dev/null; then echo 'daemon exited before readiness' >&2; return 1; fi
    sleep 0.1
  done
  echo 'daemon did not become ready' >&2
  return 1
}
fingerprint=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
test "${#fingerprint}" -eq 64
host_incus=$(command -v incus)
incus_binary="$step_dir/incus-wrapper"
printf '#!%s\n' "$(command -v bash)" > "$incus_binary"
printf 'host_incus=%q\nfixture=%q\n' "$host_incus" "$step_dir" >> "$incus_binary"
cat >> "$incus_binary" <<'NATIVE_WRAPPER'
set -euo pipefail
# Fixture-owned read-only outage, scoped to the reviewed old UUID. Project,
# profile and sibling checks still reach real Incus during daemon startup.
if test "$#" -eq 7 && test "$4" = list && test "$6" = --format && test "$7" = json &&
 test -f "$fixture/cleanup-unavailable"; then
 read -r old_uuid < "$fixture/cleanup-unavailable"
 if test "$5" = "^p-$old_uuid$"; then
  printf '%s\n' "$old_uuid" > "$fixture/cleanup-unavailable-seen"
  printf 'fixture native observation unavailable\n' >&2
  exit 125
 fi
fi
exec "$host_incus" "$@"
NATIVE_WRAPPER
chmod 0700 "$incus_binary"
# The gated driver's host unit holds each start before host readiness until
# the scenario pushes /etc/p/test-release into the guest.
driver=$(bash "$P_TEST_SOURCE/tests/integration/runtime-driver-config.sh" "$incus_binary" "$P_TEST_GATED_RUNTIME_DRIVER")
# The source driver runs the fixture's Git wrapper, which is made below,
# before the daemon starts.
source_driver=$(bash "$P_TEST_SOURCE/tests/integration/source-driver-config.sh" |
  jq -c --arg git "$step_dir/bin/git" '.config.git=$git')
jq -n --arg state "$state" --argjson source_driver "$source_driver" \
  --argjson driver "$driver" --arg prefix "$endpoint_prefix" \
  --arg image "$fingerprint" \
  '{schema:"p.host/v1",state_dir:$state,
   git:{listen:"127.0.0.1:0",driver:$source_driver},
   runtime:{driver:$driver,
    endpoint_prefix:$prefix,disk_source_ceilings:["/var/lib/p-vm/endpoints","/var/lib/p-vm/grants"],
    base_image_fingerprint:$image,
    project_policy:{network:"none",filesystem_mounts:[],command:["/run/current-system/sw/bin/bash"]}}}' \
  > "$step_dir/host.json"
jq '.runtime.environment={system:"x86_64-linux",builder_storage_pool:"builders"}' "$step_dir/host.json" > "$step_dir/host-env.json"
mv "$step_dir/host-env.json" "$step_dir/host.json"
bash "$P_TEST_SOURCE/tests/integration/public-egress-host-config.sh" "$step_dir/host.json"


# The source driver's Git is this wrapper, which runs the host's Git. The
# driver runs its Git with PATH set to the wrapper's directory, so the
# wrapper uses shell builtins and the absolute utilities resolved here.
host_git=$(readlink -f -- "$(command -v git)")
mkdir -m 0700 "$step_dir/bin"
printf '#!%s\n' "$(command -v bash)" > "$step_dir/bin/git"
printf 'host_git=%q\nfixture=%q\nsleep_binary=%q\np_binary=%q\njq_binary=%q\n'  "$host_git" "$step_dir" "$(command -v sleep)" "$(command -v p)" "$(command -v jq)" >> "$step_dir/bin/git"
cat >> "$step_dir/bin/git" <<'WRAPPER'
set -euo pipefail
# The source driver reads a head as --git-dir=REPO show-ref --verify --quiet REF.
if test "$#" -eq 5 && test "${1%%=*}" = --git-dir && test "$2" = show-ref && test "$3" = --verify &&
 test "$4" = --quiet && test "$5" = refs/heads/work && test -f "$fixture/cleanup-arm"; then
 read -r uuid < "$fixture/cleanup-arm"
 # Read the standard public operation view while the worker is outside its
 # SQLite transaction. The pause requires an already durable cleanup phase.
 if "$p_binary" api "$fixture/state/control.sock" operation.list '{"limit":20}' |  "$jq_binary" -e --arg uuid "$uuid" '.result.operations | any(.[];
  .kind=="session.create.cleanup" and .session_uuid==$uuid and .phase=="local-complete" and .status=="running")' >/dev/null; then
  : > "$fixture/cleanup-paused"
  for ((attempt=0; attempt<800; attempt++)); do
   if test -f "$fixture/cleanup-release"; then break; fi
   "$sleep_binary" 0.05
  done
  test -f "$fixture/cleanup-release" || exit 124
 fi
fi
exec "$host_git" "$@"
WRAPPER
chmod 0700 "$step_dir/bin/git"
printf 'release\n' > "$step_dir/release"
start_daemon
p_instance=$(rpc system.hello | jq -er '.result.instance_id | select(length==36)')
rpc system.hello | jq -e '.result.capabilities | index("runtime") != null' >/dev/null
created=$(rpc project.create '{"key":"project","project":"replacement"}')
sibling=$(session_uuid "$created")
sessions+=("$sibling")
release_on_running "$sibling" "$(operation_id "$created")"
wait_operation "$(operation_id "$created")" completed >/dev/null
wait_ready "$sibling"
exec_p "$sibling" git -c user.name=P -c user.email=p@example.invalid commit --allow-empty -qm first
exec_p "$sibling" git push origin HEAD:refs/heads/main
first=$(exec_p "$sibling" git rev-parse HEAD)
repo="$state/repositories/$(printf %s replacement | sha256sum | cut -d' ' -f1).git"
"$host_git" -C "$repo" update-ref refs/heads/source "$first"
export GIT_AUTHOR_NAME=P GIT_AUTHOR_EMAIL=p@example.invalid
export GIT_COMMITTER_NAME=P GIT_COMMITTER_EMAIL=p@example.invalid
second=$("$host_git" -C "$repo" commit-tree "$("$host_git" -C "$repo" rev-parse 'refs/heads/main^{tree}')" -p "$first" -m changed)
third=$("$host_git" -C "$repo" commit-tree "$("$host_git" -C "$repo" rev-parse 'refs/heads/main^{tree}')" -p "$second" -m stale)
key_before=$(sha256sum "$state/session_keys/$sibling" | cut -d' ' -f1)
exec_p "$sibling" /run/current-system/sw/bin/bash -c 'printf dummy-private > /home/p/replacement-private; chmod 0600 /home/p/replacement-private; printf dirty > sibling-dirty; mkdir -p /home/p/.config/p-fixture; printf fixture-credential > /home/p/.config/p-fixture/credential; chmod 0600 /home/p/.config/p-fixture/credential'


# replace_params BRANCH CHOICE SOURCE: a replacement's dry-run params.
plan_replace() { rpc session.create.replace "$1"; }
replace_params() {
  jq -nc --arg uuid "$old_uuid" --arg branch "$1" --arg choice "$2" --arg source "$3" \
    '{old_uuid:$uuid,project:"replacement",branch:$branch,choice:$choice} +
      (if $choice=="new" then {source:$source} else {} end)'
}
change_tag() { jq -er '.result.change_tag | select(test("^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{64}$"))' <<< "$1"; }
# No build runs whenever this is called, so no builder may exist at all: the
# old creation's builders are named by build references derived from its
# operation and cycle.
assert_old_effects_absent() {
  test ! -e "$state/session_keys/$old_uuid"
  test ! -e "$endpoint_prefix/$old_uuid"
  inc list --format json | jq -e --arg name "p-$old_uuid" \
    'all(.[];.name!=$name and (.name|startswith("p-builder-")|not) and
      (.config["user.p.session_uuid"] // "")!=($name|ltrimstr("p-")) and
      .config["user.p.builder_request_uuid"]==null)' >/dev/null
}
# This P instance's images, as a sorted list of [fingerprint, source commit,
# selection].
instance_images() {
  inc image list --format json | jq -c --arg instance "$p_instance" \
    '[.[] | select(.properties["p.instance"]==$instance) |
      [.fingerprint, .properties["p.source_commit"], .properties["p.selection"]]] | sort'
}

# The invalid devShell is a real local committed flake evaluated by the
# native environment build in its real confined Incus builder.
printf '{ outputs = _: { devShells.x86_64-linux.default = 42; }; }\n' > "$step_dir/flake.nix"
blob=$("$host_git" -C "$repo" hash-object -w "$step_dir/flake.nix")
tree=$(printf '100644 blob %s\tflake.nix\n' "$blob" | "$host_git" -C "$repo" mktree)
bad_tip=$("$host_git" -C "$repo" commit-tree "$tree" -p "$first" -m invalid-immutable-Nix)
"$host_git" -C "$repo" update-ref refs/heads/work "$bad_tip"
old_request='{"key":"bad-nix","project":"replacement","branch":"work","choice":"existing"}'
created=$(rpc session.create "$old_request")
old_uuid=$(session_uuid "$created")
old_id=$(operation_id "$created")
sessions+=("$old_uuid")
blocked=$(wait_operation "$old_id" blocked)
jq -e --arg base "$fingerprint" '.result.operation | .phase=="branch-assigned" and .committed==true and
 .reason=="image_build_failed" and (.diagnostic | contains("present default devShell invalid")) and
 .evidence.environment!=null and .evidence.image==null and .evidence.image_fingerprint==$base and
 .evidence.runtime_init_state=="not-attempted" and .evidence.environment_builder=={cycle:1,state:"absent"}' <<< "$blocked" >/dev/null
assert_old_effects_absent
# The failed build left no image.
test "$(instance_images)" = '[]'
images_before=$(inc image list --format json | jq -c '[.[].fingerprint]|sort')
# Corrected committed source still cannot be integrated replacement of builder history.
"$host_git" -C "$repo" update-ref refs/heads/work "$second" "$bad_tip"
plan=$(plan_replace "$(replace_params work existing '')")
jq -e '.result | .plan.allowed==false and (has("change_tag")|not)' <<< "$plan" >/dev/null
"$host_git" -C "$repo" update-ref refs/heads/work "$bad_tip" "$second"
# plan_cleanup: the cleanup's dry run. cleanup_params TAG: its run's params,
# the same plus the key and the dry run's change tag.
plan_cleanup() { rpc session.create.cleanup "$(jq -nc --arg uuid "$old_uuid" '{uuid:$uuid}')"; }
cleanup_params() { jq -nc --arg uuid "$old_uuid" --arg tag "$1" '{uuid:$uuid,key:"cleanup-bad-nix",if_match:$tag}'; }
plan=$(plan_cleanup)
jq -e --arg oid "$bad_tip" --arg uuid "$old_uuid" '.result | (.change_tag|test("^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{64}$")) and
 (.plan | .action=="session.create.cleanup" and .target==("session/"+$uuid) and
 .allowed==true and .refusals==[] and .loss==[] and .gone==[] and (.details |
 .assigned_branch.oid==$oid and .environment_builder=={cycle:1,state:"absent"} and
 .provisional.runtime=="absent" and .provisional.builder=="absent" and
 .provisional.runtime_local=="unavailable" and .external_mounts=="preserved" and .shared_images=="preserved" and
 (has("runtime")|not)))' <<< "$plan" >/dev/null
tag=$(change_tag "$plan")
# Each change below makes the run stale: it plans again, is refused and
# records nothing.
"$host_git" -C "$repo" update-ref refs/heads/work "$third" "$bad_tip"
expect_error precondition_failed changed session.create.cleanup "$(cleanup_params "$tag")"
test "$("$host_git" -C "$repo" rev-parse refs/heads/work)" = "$third"
"$host_git" -C "$repo" update-ref refs/heads/work "$bad_tip" "$third"
printf preserve > "$state/session_keys/$old_uuid"
expect_error precondition_failed changed session.create.cleanup "$(cleanup_params "$tag")"
test "$(cat "$state/session_keys/$old_uuid")" = preserve
rm "$state/session_keys/$old_uuid"
competing="p-recovery-$old_uuid"
inc init "$fingerprint" "$competing" --profile default  --config security.idmap.isolated=true --config security.privileged=false  --config security.nesting=false --config "user.p.session_uuid=$old_uuid"
identity=$(inc list "^$competing$" --format json | jq -er '.[0].config["volatile.uuid"]')
expect_error precondition_failed changed session.create.cleanup "$(cleanup_params "$tag")"
test "$(inc list "^$competing$" --format json | jq -er '.[0].config["volatile.uuid"]')" = "$identity"
inc delete "$competing"
competing=
stop_process "$daemon_pid"
daemon_pid=
start_daemon
# A tag past its expiry is refused before planning.
expect_error precondition_failed expired session.create.cleanup "$(cleanup_params "20000101T000000Z-$(printf '%064d' 0)")"
inspect_operation "$old_id" | jq -e '.result.operation | .status=="blocked" and .evidence.environment_builder.cycle==1' >/dev/null
rpc operation.list '{"limit":20}' | jq -e '.result.operations | all(.[]; .kind!="session.create.cleanup")' >/dev/null
plan=$(plan_cleanup)
tag=$(change_tag "$plan")
refs_before=$("$host_git" -C "$repo" for-each-ref --format='%(refname) %(objectname)')
printf '%s\n' "$old_uuid" > "$step_dir/cleanup-arm"
ran=$(rpc session.create.cleanup "$(cleanup_params "$tag")")
cleanup_id=$(operation_id "$ran")
test "$(session_uuid "$ran")" = "$old_uuid"
pause_deadline=$((SECONDS+45))
while test ! -f "$step_dir/cleanup-paused" && ((SECONDS < pause_deadline)); do sleep 0.1; done
if test ! -f "$step_dir/cleanup-paused"; then
 inspect_operation "$cleanup_id" | jq -c '.result.operation|{status,phase,committed,diagnostic:((.diagnostic // "")[:400])}' >&2
 echo 'durable local-complete cleanup pause not reached' >&2
 exit 1
fi
inspect_operation "$cleanup_id" | jq -e '.result.operation | .status=="running" and .phase=="local-complete" and .committed==true and .evidence.local_complete==true' >/dev/null
inspect_operation "$old_id" | jq -e '.result.operation.status=="superseded"' >/dev/null
test "$(operation_id "$(rpc session.create.cleanup "$(cleanup_params "$tag")")")" = "$cleanup_id"
old_replay_status=0
old_replay=$(rpc session.create "$old_request") || old_replay_status=$?
if test "$old_replay_status" -ne 0; then
 jq -c --argjson status "$old_replay_status" '{status:$status,kind:.error.message,
   code:.error.code,data:.error.data}' <<< "$old_replay" >&2 || true
 echo 'superseded Create replay failed during cleanup checkpoint' >&2
 exit 1
fi
test "$(operation_id "$old_replay")" = "$old_id"
expect_error conflict operation_superseded operation.retry "$(jq -nc --arg id "$old_id" '{id:$id}')"
stop_process "$daemon_pid"
daemon_pid=
# Inject a read-only unavailable native observation after the durable crash.
# No real daemon/Incus outage is claimed: the wrapper exits before forwarding
# only this old UUID's inspect. All other native reads/mutations remain real.
rm "$step_dir/cleanup-arm"
touch "$step_dir/cleanup-release"
printf '%s\n' "$old_uuid" > "$step_dir/cleanup-unavailable"
start_daemon
blocked=$(wait_operation "$cleanup_id" blocked)
jq -e '.result.operation | .phase=="local-complete" and .evidence.local_complete==true' <<< "$blocked" >/dev/null
test "$(cat "$step_dir/cleanup-unavailable-seen")" = "$old_uuid"
inspect_session "$old_uuid" | jq -e '.result.session.registry_state=="removing"' >/dev/null
inspect_operation "$old_id" | jq -e '.result.operation.status=="superseded"' >/dev/null
assert_old_effects_absent
test "$("$host_git" -C "$repo" for-each-ref --format='%(refname) %(objectname)')" = "$refs_before"
test "$(operation_id "$(rpc session.create.cleanup "$(cleanup_params "$tag")")")" = "$cleanup_id"
echo P_FAILED_CREATE_CLEANUP_UNAVAILABLE_PRESERVED
# Once the observation returns, a newly competing UUID must still retain the
# removing record and P refs. Explicit Retry cannot delete fixture machinery.
competing="p-recovery-$old_uuid"
inc init "$fingerprint" "$competing" --profile default \
  --config security.idmap.isolated=true --config security.privileged=false \
  --config security.nesting=false --config "user.p.session_uuid=$old_uuid"
identity=$(inc list "^$competing$" --format json | jq -er '.[0].config["volatile.uuid"]')
rm "$step_dir/cleanup-unavailable"
rpc operation.retry "$(jq -nc --arg id "$cleanup_id" '{id:$id}')" | jq -e --arg id "$cleanup_id" '.result.operation.id==$id' >/dev/null
blocked=$(wait_operation "$cleanup_id" blocked)
jq -e '.result.operation | .phase=="local-complete" and .evidence.local_complete==true' <<< "$blocked" >/dev/null
inspect_session "$old_uuid" | jq -e '.result.session.registry_state=="removing"' >/dev/null
test "$("$host_git" -C "$repo" for-each-ref --format='%(refname) %(objectname)')" = "$refs_before"
test "$(inc list "^$competing$" --format json | jq -er '.[0].config["volatile.uuid"]')" = "$identity"
test "$(operation_id "$(rpc session.create.cleanup "$(cleanup_params "$tag")")")" = "$cleanup_id"
inc delete "$competing"
competing=
rpc operation.retry "$(jq -nc --arg id "$cleanup_id" '{id:$id}')" | jq -e --arg id "$cleanup_id" '.result.operation.id==$id' >/dev/null
wait_operation "$cleanup_id" completed >/dev/null
rpc session.list '{"limit":8}' | jq -e --arg uuid "$old_uuid" '.result.sessions|all(.[];.uuid!=$uuid)' >/dev/null
assert_old_effects_absent
test "$("$host_git" -C "$repo" for-each-ref --format='%(refname) %(objectname)')" = "$refs_before"
test "$(inc image list --format json | jq -c '[.[].fingerprint]|sort')" = "$images_before"
# The next Create is explicit, separate, and captures the corrected commit.
"$host_git" -C "$repo" update-ref refs/heads/work "$second" "$bad_tip"
next_request='{"key":"corrected-create","project":"replacement","branch":"work","choice":"existing"}'
created=$(rpc session.create "$next_request")
new_uuid=$(session_uuid "$created")
new_id=$(operation_id "$created")
sessions+=("$new_uuid")
test "$new_uuid" != "$old_uuid"
release_on_running "$new_uuid" "$new_id"
wait_operation "$new_id" completed >/dev/null
wait_ready "$new_uuid"
test "$(exec_p "$new_uuid" git rev-parse HEAD)" = "$second"
# The corrected commit has no flake: its creation built the commit's own image
# from the base packages and its code, the only image this run added.
new_image=$(inspect_session "$new_uuid" | jq -er --arg oid "$second" --arg base "$fingerprint" \
  '.result.session.image | select(.commit_oid==$oid and .selection=="base" and .state=="built" and
    .base_image_fingerprint==$base and .image_fingerprint!=$base) | .image_fingerprint')
test "$(instance_images)" = "$(jq -nc --arg image "$new_image" --arg oid "$second" '[[$image,$oid,"no-flake"]]')"
images_after=$(jq -c --arg image "$new_image" '. + [$image] | sort' <<< "$images_before")
test "$(inc image list --format json | jq -c '[.[].fingerprint]|sort')" = "$images_after"
new_key=$(sha256sum "$state/session_keys/$new_uuid" | cut -d' ' -f1)
stop_process "$daemon_pid"
daemon_pid=
start_daemon
test "$(operation_id "$(rpc session.create.cleanup "$(cleanup_params "$tag")")")" = "$cleanup_id"
test "$(operation_id "$(rpc session.create "$old_request")")" = "$old_id"
test "$(operation_id "$(rpc session.create "$next_request")")" = "$new_id"
assert_old_effects_absent
test "$(sha256sum "$state/session_keys/$new_uuid" | cut -d' ' -f1)" = "$new_key"
test "$(sha256sum "$state/session_keys/$sibling" | cut -d' ' -f1)" = "$key_before"
test "$(exec_p "$sibling" cat /home/p/replacement-private)" = dummy-private
test "$(exec_p "$sibling" cat /workspace/sibling-dirty)" = dirty
test "$(exec_p "$sibling" cat /home/p/.config/p-fixture/credential)" = fixture-credential
test "$("$host_git" -C "$repo" rev-parse refs/heads/main)" = "$first"
test "$(inc image list --format json | jq -c '[.[].fingerprint]|sort')" = "$images_after"
rpc session.list '{"limit":8}' | jq -e '.result.sessions|length==2 and all(.[];.registry_state=="established")' >/dev/null
echo P_FAILED_CREATE_CLEANUP_PASS
