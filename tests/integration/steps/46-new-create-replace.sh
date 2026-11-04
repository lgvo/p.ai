# shellcheck shell=bash
# Real failed local new-branch Creates before and after a Git CAS. Disposable
# command faults preserve all refs and happen before UUID resource effects.
set -E
umask 077
step_dir="$P_TEST_TMP/step-46"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step46
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
unset SSH_AUTH_SOCK GIT_SSH GIT_SSH_COMMAND GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM GIT_CONFIG_COUNT GIT_DIR GIT_WORK_TREE
daemon_pid=
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
  touch "$step_dir/git-release"
  stop_process "$daemon_pid"
  for uuid in "${sessions[@]}"; do
    inc delete --force "p-$uuid" >/dev/null 2>&1 || true
    rm -rf -- "${endpoint_prefix:?}/${uuid:?}"
  done
  rmdir "$endpoint_prefix" 2>/dev/null || true
}
on_error() {
  printf 'P_NEW_CREATE_REPLACE_FAIL line=%s status=%s\n' "$2" "$1" >&2
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
release_on_running() {
  local uuid="$1" id="$2" deadline=$((SECONDS+80)) detail
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
  PATH="$step_dir/bin:$PATH" p daemon "$step_dir/host.json" > "$step_dir/daemon.out" 2> "$step_dir/daemon.err" &
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
incus_binary=$(command -v incus)
# The gated driver's host unit holds each start before host readiness until
# the scenario pushes /etc/p/test-release into the guest.
driver=$(bash "$P_TEST_SOURCE/tests/integration/runtime-driver-config.sh" "$incus_binary" "$P_TEST_GATED_RUNTIME_DRIVER")
jq -n --arg state "$state" \
  --argjson driver "$driver" --arg prefix "$endpoint_prefix" \
  --arg image "$fingerprint" \
  '{schema:"p.host/v1",state_dir:$state,
   git:{listen:"127.0.0.1:0",driver:{name:"git",config:{}}},
   runtime:{driver:$driver,
    endpoint_prefix:$prefix,disk_source_ceilings:["/var/lib/p-vm/endpoints","/var/lib/p-vm/grants"],
    base_image_fingerprint:$image,
    project_policy:{network:"none",filesystem_mounts:[],command:["/run/current-system/sw/bin/bash"]}}}' \
  > "$step_dir/host.json"
bash "$P_TEST_SOURCE/tests/integration/public-egress-host-config.sh" "$step_dir/host.json"


# Resolve every executable before the source driver narrows Git's PATH to this
# wrapper's directory. The driver creates a head as --git-dir=REPO update-ref
# --no-deref REF NEW ZERO.
host_git=$(command -v git)
mkdir -m 0700 "$step_dir/bin"
printf '#!%s\n' "$(command -v bash)" > "$step_dir/bin/git"
printf 'host_git=%q\nfixture=%q\n' "$host_git" "$step_dir" >> "$step_dir/bin/git"
cat >> "$step_dir/bin/git" <<'WRAPPER'
set -euo pipefail
if test "$#" -eq 6 && test "${1%%=*}" = --git-dir && test "$2" = update-ref && test "$3" = --no-deref; then
  if test "$4" = refs/heads/work && test -f "$fixture/git-fault-before"; then
    # Public worker attempted its planned zero-old CAS, but no Git effect ran.
    : > "$fixture/git-fault-seen"
    exit 128
  fi
  if test "$4" = refs/heads/retained && test -f "$fixture/git-fault-after"; then
    # Execute the real local Git CAS, then simulate loss of its success reply.
    # The ref is retained; no UUID endpoint/key/runtime effect has occurred.
    "$host_git" "$@"
    : > "$fixture/git-fault-seen"
    exit 128
  fi
fi
exec "$host_git" "$@"
WRAPPER
chmod 0700 "$step_dir/bin/git"
printf 'release\n' > "$step_dir/release"
start_daemon
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
exec_p "$sibling" /run/current-system/sw/bin/bash -c 'printf dummy-private > /home/p/replacement-private; chmod 0600 /home/p/replacement-private; printf dirty > sibling-dirty'


# replace_params BRANCH CHOICE SOURCE: a replacement's dry-run params.
# run_params KEY TAG: the run's, the dry run's $params plus the key, which is
# also the new creation's, and the dry run's change tag.
plan_replace() { rpc session.create.replace "$1"; }
replace_params() {
  jq -nc --arg uuid "$old_uuid" --arg branch "$1" --arg choice "$2" --arg source "$3" \
    '{old_uuid:$uuid,project:"replacement",branch:$branch,choice:$choice} +
      (if $choice=="new" then {source:$source} else {} end)'
}
run_params() { jq -c --arg key "$1" --arg tag "$2" '. + {key:$key,if_match:$tag}' <<< "$params"; }
run_replace() { rpc session.create.replace "$(run_params "$1" "$2")"; }
change_tag() { jq -er '.result.change_tag | select(test("^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{64}$"))' <<< "$1"; }
# This daemon has no image builders configured: no builder may exist at all.
assert_old_effects_absent() {
  test ! -e "$state/session_keys/$old_uuid"
  test ! -e "$endpoint_prefix/$old_uuid"
  inc list --format json | jq -e --arg name "p-$old_uuid" \
    'all(.[];.name!=$name and (.name|startswith("p-builder-")|not) and
      (.config["user.p.session_uuid"] // "")!=($name|ltrimstr("p-")) and
      .config["user.p.builder_request_uuid"]==null)' >/dev/null
}

# First public failure happens before Git executes its planned branch CAS.
touch "$step_dir/git-fault-before"
old_request='{"key":"old-absent","project":"replacement","branch":"work","choice":"new","source":"refs/heads/source"}'
created=$(rpc session.create "$old_request")
old_uuid=$(session_uuid "$created")
old_id=$(operation_id "$created")
blocked=$(wait_operation "$old_id" blocked)
test -f "$step_dir/git-fault-seen"
rm "$step_dir/git-fault-before" "$step_dir/git-fault-seen"
jq -e --arg oid "$first" '.result.operation | .phase=="source-ready" and .committed==false and
  .evidence.ref_cas_intent==true and .evidence.branch_existed==false and .evidence.captured_oid==$oid' <<< "$blocked" >/dev/null
if "$host_git" -C "$repo" show-ref --verify --quiet refs/heads/work; then echo 'before-CAS fault created a ref' >&2; exit 1; fi
assert_old_effects_absent
"$host_git" -C "$repo" update-ref refs/heads/source "$second" "$first"
params=$(replace_params work new refs/heads/source)
plan=$(plan_replace "$params")
jq -e --arg old "$first" --arg new "$second" '.result | (.change_tag|test("^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{64}$")) and
 (.plan | .allowed==true and .refusals==[] and .loss==[] and .gone==[] and (.details |
 .old_request.choice=="new" and .old_captured_oid==$old and .new_captured_oid==$new and
 .new_request=={project:"replacement",branch:"work",choice:"new",source:"refs/heads/source"} and
 .old_branch=={ref:"refs/heads/work",observed:true,exists:false} and .new_branch=={ref:"refs/heads/work",observed:true,exists:false} and
 .provisional.assigned_ref=="absent" and .provisional.runtime=="absent" and
 .provisional.builder=="absent" and .provisional.session_key=="absent" and
 .provisional.endpoint=="absent" and .provisional.principal=="absent"))' <<< "$plan" >/dev/null
tag=$(change_tag "$plan")
# Both source changes and target occupancy make the run stale: it plans
# again, is refused and records nothing.
"$host_git" -C "$repo" update-ref refs/heads/source "$third" "$second"
expect_error precondition_failed changed session.create.replace "$(run_params replace-absent "$tag")"
"$host_git" -C "$repo" update-ref refs/heads/source "$second" "$third"
"$host_git" -C "$repo" update-ref refs/heads/work "$first"
expect_error precondition_failed changed session.create.replace "$(run_params replace-absent "$tag")"
test "$("$host_git" -C "$repo" rev-parse refs/heads/work)" = "$first"
# Remove only the disposable host-injected stale-test precondition. P's refusal
# preserved it; replacement itself never deletes or resets an existing ref.
"$host_git" -C "$repo" update-ref -d refs/heads/work "$first"
assert_old_effects_absent
# The daemon keeps nothing between the dry run and the run: a tag planned
# before a restart runs after it.
plan=$(plan_replace "$params")
tag=$(change_tag "$plan")
stop_process "$daemon_pid"
daemon_pid=
start_daemon
assert_old_effects_absent
# A tag past its expiry is refused before planning.
expect_error precondition_failed expired session.create.replace \
  "$(run_params replace-absent "20000101T000000Z-$(printf '%064d' 0)")"
ran=$(run_replace replace-absent "$tag")
jq -e '.result.operation | .kind=="session.create" and .idempotency_key=="replace-absent"' <<< "$ran" >/dev/null
new_id=$(operation_id "$ran")
new_uuid=$(session_uuid "$ran")
sessions+=("$new_uuid")
test "$new_id" != "$old_id"
test "$new_uuid" != "$old_uuid"
release_on_running "$new_uuid" "$new_id"
completed=$(wait_operation "$new_id" completed)
wait_ready "$new_uuid"
jq -e --arg old "$old_id" --arg oid "$second" '.result.operation | .evidence.supersedes_operation_id==$old and
 .evidence.captured_oid==$oid and .evidence.ref_cas_intent==true and .evidence.branch_existed==false' <<< "$completed" >/dev/null
inspect_operation "$old_id" | jq -e '.result.operation.status=="superseded"' >/dev/null
expect_error conflict operation_superseded operation.retry "$(jq -nc --arg id "$old_id" '{id:$id}')"
test "$(operation_id "$(run_replace replace-absent "$tag")")" = "$new_id"
test "$(operation_id "$(rpc session.create "$old_request")")" = "$old_id"
test "$("$host_git" -C "$repo" rev-parse refs/heads/work)" = "$second"
test "$(exec_p "$new_uuid" git rev-parse HEAD)" = "$second"
first_replacement=$new_uuid
first_key=$(sha256sum "$state/session_keys/$new_uuid" | cut -d' ' -f1)

# Second public failure executes a real CAS and loses its reply. Its preserved
# ref is explicitly selected as existing; this changed choice is a new request.
touch "$step_dir/git-fault-after"
old_request='{"key":"old-preserved","project":"replacement","branch":"retained","choice":"new","source":"refs/heads/source"}'
created=$(rpc session.create "$old_request")
old_uuid=$(session_uuid "$created")
old_id=$(operation_id "$created")
blocked=$(wait_operation "$old_id" blocked)
test -f "$step_dir/git-fault-seen"
rm "$step_dir/git-fault-after" "$step_dir/git-fault-seen"
jq -e --arg oid "$second" '.result.operation | .phase=="source-ready" and .committed==false and
 .evidence.ref_cas_intent==true and .evidence.captured_oid==$oid' <<< "$blocked" >/dev/null
test "$("$host_git" -C "$repo" rev-parse refs/heads/retained)" = "$second"
assert_old_effects_absent
params=$(replace_params retained existing '')
plan=$(plan_replace "$params")
jq -e --arg oid "$second" '.result | (.change_tag|test("^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{64}$")) and
 (.plan | .allowed==true and .refusals==[] and (.details | .old_request.choice=="new" and
 .new_request=={project:"replacement",branch:"retained",choice:"existing"} and .old_captured_oid==$oid and .new_captured_oid==$oid and
 .old_branch=={ref:"refs/heads/retained",observed:true,exists:true,oid:$oid} and .new_branch==.old_branch and
 .provisional.assigned_ref=="preserved_created"))' <<< "$plan" >/dev/null
tag=$(change_tag "$plan")
"$host_git" -C "$repo" update-ref refs/heads/retained "$third" "$second"
expect_error precondition_failed changed session.create.replace "$(run_params replace-preserved "$tag")"
test "$("$host_git" -C "$repo" rev-parse refs/heads/retained)" = "$third"
"$host_git" -C "$repo" update-ref refs/heads/retained "$second" "$third"
printf unexpected > "$state/session_keys/$old_uuid"
expect_error precondition_failed changed session.create.replace "$(run_params replace-preserved "$tag")"
test "$(cat "$state/session_keys/$old_uuid")" = unexpected
rm "$state/session_keys/$old_uuid"
plan=$(plan_replace "$params")
tag=$(change_tag "$plan")
refs_before=$("$host_git" -C "$repo" for-each-ref --format='%(refname) %(objectname)')
ran=$(run_replace replace-preserved "$tag")
new_id=$(operation_id "$ran")
new_uuid=$(session_uuid "$ran")
sessions+=("$new_uuid")
release_on_running "$new_uuid" "$new_id"
wait_operation "$new_id" completed >/dev/null
wait_ready "$new_uuid"
test "$("$host_git" -C "$repo" for-each-ref --format='%(refname) %(objectname)')" = "$refs_before"
test "$(exec_p "$new_uuid" git rev-parse HEAD)" = "$second"
inspect_operation "$old_id" | jq -e '.result.operation.status=="superseded"' >/dev/null
test "$(operation_id "$(run_replace replace-preserved "$tag")")" = "$new_id"
test "$(operation_id "$(rpc session.create "$old_request")")" = "$old_id"
new_key=$(sha256sum "$state/session_keys/$new_uuid" | cut -d' ' -f1)
stop_process "$daemon_pid"
daemon_pid=
start_daemon
test "$(operation_id "$(run_replace replace-preserved "$tag")")" = "$new_id"
test "$(operation_id "$(rpc session.create "$old_request")")" = "$old_id"
assert_old_effects_absent
test "$("$host_git" -C "$repo" for-each-ref --format='%(refname) %(objectname)')" = "$refs_before"
test "$(sha256sum "$state/session_keys/$new_uuid" | cut -d' ' -f1)" = "$new_key"
test "$(sha256sum "$state/session_keys/$first_replacement" | cut -d' ' -f1)" = "$first_key"
test "$(sha256sum "$state/session_keys/$sibling" | cut -d' ' -f1)" = "$key_before"
test "$(exec_p "$sibling" cat /home/p/replacement-private)" = dummy-private
test "$(exec_p "$sibling" cat /workspace/sibling-dirty)" = dirty
test "$("$host_git" -C "$repo" rev-parse refs/heads/main)" = "$first"
rpc session.list '{"limit":8}' | jq -e '.result.sessions | length==3 and all(.[];.registry_state=="established")' >/dev/null
rpc operation.list '{"limit":20}' | jq -e '.result.operations | [.[]|select(.kind=="session.create")]|length==4' >/dev/null
inc list --format json | jq -e --arg old "p-$old_uuid" --arg new "p-$new_uuid" 'all(.[];.name!=$old) and ([.[]|select(.name==$new)]|length==1)' >/dev/null
echo P_NEW_CREATE_REPLACE_PASS
