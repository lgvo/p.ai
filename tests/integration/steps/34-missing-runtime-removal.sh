# shellcheck shell=bash
# Discard and Delete after a fixture-caused runtime loss. No login occurs.
set -E
umask 077
step_dir="$P_TEST_TMP/step-34"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step34
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
daemon_pid=
uuid=
uuid_b=
renamed=
competing=
readiness_session=
diagnosed_session=
incus_real=$(command -v incus)
host_git=$(command -v git)

inc() { timeout 60 "$incus_real" --force-local --project user-1000 "$@"; }
rpc() { timeout 35 p api "$socket" "$@"; }
# expect_refusal METHOD PARAMS KIND REASON: the request is refused with the
# error kind KIND, its code and REASON about this test's session.
expect_refusal() {
  local method="$1" params="$2" kind="$3" reason="$4" response status=0
  response=$(rpc "$method" "$params") || status=$?
  test "$status" -eq 1
  jq -e --arg kind "$kind" --arg reason "$reason" --arg target "session/$uuid" '
    {conflict: -32011, precondition_failed: -32013} as $codes |
    .jsonrpc=="2.0" and .id==2 and (has("result")|not) and
    .error.message==$kind and .error.code==$codes[$kind] and .error.data.reason==$reason and
    (.error.message!="conflict" or .error.data.target==$target)' <<< "$response" >/dev/null
}
tag_pattern='^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{64}$'
# expect_refused_plan ACTION CODE: the dry run of session.ACTION, discard or
# delete, of this test's session returns a plan that is not allowed, refused
# with CODE about the session itself, and no change tag.
expect_refused_plan() {
  rpc "session.$1" "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" | jq -e --arg action "session.$1" \
    --arg target "session/$uuid" --arg code "$2" '.result | (has("change_tag")|not) and (.plan |
      .action==$action and .target==$target and .allowed==false and .refusals==[{code:$code}])' >/dev/null
}
# plan_missing ACTION: the dry run of session.ACTION of this test's session,
# whose runtime is missing, returns an allowed plan with a change tag. The
# plan states the runtime missing and its data already gone, not lost by the
# removal; it prints the plan.
plan_missing() {
  local result
  result=$(rpc "session.$1" "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')") || return
  jq -e --arg action "session.$1" --arg target "session/$uuid" --arg tag "$tag_pattern" '
    .result | (.change_tag | test($tag)) and (.plan | .action==$action and .target==$target and
      .allowed==true and .refusals==[] and
      .gone==[{kind:"runtime_data",target:$target,certain:true}] and
      all(.loss[]; .kind!="runtime_data") and
      (.details.runtime | .condition=="missing" and
        ((has("incus_uuid") or has("original_status") or has("runtime_data_lost") or
          has("runtime_loss_unknown")) | not)))' <<< "$result" >/dev/null || return
  printf '%s\n' "$result"
}
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
  touch "$step_dir/delete-read-release"
  stop_daemon
  if test -n "$renamed"; then inc delete --force "$renamed" >/dev/null 2>&1 || true; fi
  if test -n "$competing"; then inc delete --force "$competing" >/dev/null 2>&1 || true; fi
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
diagnose_session() {
  local id="$1" output="$step_dir/readiness-diagnostic.out"
  if test -z "$id" || test "$diagnosed_session" = "$id"; then return; fi
  diagnosed_session="$id"
  printf 'P_MISSING_RUNTIME_REMOVAL_DIAGNOSTIC session=%s\n' "$id" >&2
  # Whitelist public state and confined identity; never dump config, keys or
  # process environment. Each read has its own deadline and bounded output.
  if timeout 15 p api "$socket" session.inspect \
    "$(jq -nc --arg uuid "$id" '{uuid:$uuid}')" > "$output" 2>/dev/null; then
    jq -c '.result.session | {uuid,registry_state,session_condition,policy_condition,
      diagnostic:(.diagnostic // "")[:2048]}' "$output" >&2 || true
  else
    echo 'public session inspection unavailable' >&2
  fi
  if timeout 15 "$incus_real" --force-local --project user-1000 list --format json \
    > "$output" 2>/dev/null; then
    jq -c --arg uuid "$id" '[.[] | select(.name==("p-"+$uuid) or
      .config["user.p.session_uuid"]==$uuid) | {name,status,
      session_uuid:.config["user.p.session_uuid"],
      native_uuid:.config["volatile.uuid"]}] | {count:length,instances:.[0:8]}' \
      "$output" >&2 || true
  else
    echo 'confined instance inspection unavailable' >&2
  fi
  if timeout 15 "$incus_real" --force-local --project user-1000 exec "p-$id" \
    --mode non-interactive -- /usr/libexec/p/systemctl show \
    p-session.target p-interactive.service nix-daemon.service \
    --property=Id,LoadState,ActiveState,SubState,Result,ExecMainCode,ExecMainStatus,Job \
    --no-pager > "$output" 2>/dev/null; then
    echo 'session host unit state:' >&2
    tail -c 4096 "$output" >&2 || true
  else
    echo 'session host unit inspection unavailable (instance may be stopped)' >&2
  fi
  # PID 1's unit events expose transitions without collecting application
  # output. The public inspection above supplies bounded runtime diagnostics.
  if timeout 15 "$incus_real" --force-local --project user-1000 exec "p-$id" \
    --mode non-interactive -- /usr/libexec/p/journalctl -b -n 24 --no-pager \
    --output=json _PID=1 -u p-interactive.service -u p-session.target \
    > "$output" 2>/dev/null; then
    echo 'session host system events:' >&2
    jq -c '{time:.__REALTIME_TIMESTAMP,unit:(.UNIT // ._SYSTEMD_UNIT),
      message:(.MESSAGE // "")[:512]}' "$output" | tail -c 8192 >&2 || true
  else
    echo 'session host system events unavailable (instance may be stopped)' >&2
  fi
}
on_error() {
  printf 'P_MISSING_RUNTIME_REMOVAL_FAIL line=%s status=%s\n' "$2" "$1" >&2
  diagnose_session "${readiness_session:-$uuid}"
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
activate "$P_TEST_SOURCE/plugins/bundled/tmux-host" "$step_dir/runtime.json" session.asset.install
activate "$P_TEST_SOURCE/plugins/bundled/codex-adapter" "$step_dir/agent.json" agent.status.report

base_image=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
test "${#base_image}" -eq 64
jq -n --arg state "$state" --arg git_activation "$step_dir/git.json" \
  --arg git_id "$(jq -er '.plugins[0].id' "$step_dir/git.json")" \
  --arg runtime_activation "$step_dir/runtime.json" \
  --arg host_id "$(jq -er '.plugins[0].id' "$step_dir/runtime.json")" \
  --arg agent_activation "$step_dir/agent.json" \
  --arg agent_id "$(jq -er '.plugins[0].id' "$step_dir/agent.json")" \
  --arg incus_binary "$incus_real" --arg prefix "$endpoint_prefix" --arg image "$base_image" \
  '{schema:"p.host/v1",state_dir:$state,
    git:{activation_path:$git_activation,source_plugin_id:$git_id,listen:"127.0.0.1:0",driver:{name:"git",config:{}}},
    runtime:{activation_path:$runtime_activation,
      host_plugin_id:$host_id,
      agent_adapter:{activation_path:$agent_activation,plugin_id:$agent_id},
      driver:{name:"incus",config:{binary:$incus_binary,
      user_socket:"/var/lib/incus/unix.socket.user",project:"user-1000"}},
      endpoint_prefix:$prefix,disk_source_ceilings:["/var/lib/p-vm/endpoints","/var/lib/p-vm/grants"],
      base_image_fingerprint:$image,
      project_policy:{network:"none",filesystem_mounts:[],command:["/run/current-system/sw/bin/bash"]}}}' > "$step_dir/host.json"
bash "$P_TEST_SOURCE/tests/integration/public-egress-host-config.sh" "$step_dir/host.json"
start_daemon() {
  PATH="$step_dir/bin:$PATH" p daemon "$step_dir/host.json" >> "$step_dir/daemon.out" 2>> "$step_dir/daemon.err" &
  daemon_pid=$!
  local deadline=$((SECONDS+30))
  while ((SECONDS < deadline)); do
    if test -S "$socket" && rpc system.health | jq -e '.result.control_state=="ready"' >/dev/null 2>&1; then return; fi
    if ! kill -0 "$daemon_pid" 2>/dev/null; then echo 'removal daemon exited' >&2; return 1; fi
    sleep 0.1
  done
  echo 'removal daemon health unavailable' >&2
  return 1
}
wait_operation() {
  local id="$1" expected="${2:-completed}" result status deadline=$((SECONDS+150))
  while ((SECONDS < deadline)); do
    result=$(rpc operation.wait "$(jq -nc --arg id "$id" '{id:$id,timeout:20}')")
    status=$(jq -er '.result.operation.status' <<< "$result")
    if test "$status" = "$expected"; then printf '%s\n' "$result"; return; fi
    if test "$status" = blocked || test "$status" = failed || test "$status" = superseded ||
      test "$status" = completed; then
      echo "unexpected operation status: $result" >&2; return 1
    fi
    if test "$status" != running; then sleep 0.3; fi
  done
  echo "operation $id did not complete" >&2
  return 1
}
# change_session METHOD UUID: METHOD, session.start or session.stop, records
# an operation of its kind for session UUID, which completes.
change_session() {
  local id
  id=$(rpc "$1" "$(jq -nc --arg uuid "$2" '{uuid:$uuid}')" | jq -er --arg kind "$1" \
    '.result | select(.changed==true and .operation.kind==$kind) | .operation.id') || return
  wait_operation "$id" >/dev/null
}
wait_ready() {
  local id="$1" deadline=$((SECONDS+80))
  readiness_session="$id"
  while ((SECONDS < deadline)); do
    if rpc session.inspect "$(jq -nc --arg uuid "$id" '{uuid:$uuid}')" |
      jq -e '.result.session.session_condition=="ready"' >/dev/null; then
      readiness_session=
      return
    fi
    sleep 0.3
  done
  echo "session $id did not become ready" >&2
  diagnose_session "$id"
  return 1
}
# Pause only Delete's read after durable secrets cleanup, before its ref effect
# marker. The broker clears PATH, so use shell builtins and absolute utilities.
mkdir -m 0700 "$step_dir/bin"
printf '#!%s\n' "$(command -v bash)" > "$step_dir/bin/git"
printf 'host_git=%q\nfixture=%q\nsleep_binary=%q\n' "$host_git" "$step_dir" "$(command -v sleep)" >> "$step_dir/bin/git"
cat >> "$step_dir/bin/git" <<'WRAPPER'
set -euo pipefail
if test "$#" -eq 6 && test "$3" = show-ref && test "$4" = --verify &&
   test "$5" = --quiet && test "$6" = refs/heads/main && test -f "$fixture/delete-read-arm"; then
  read -r wanted < "$fixture/delete-read-arm"
  if test -n "$wanted" && test ! -e "$fixture/state/session_keys/$wanted"; then
    : > "$fixture/delete-read-paused"
    for ((attempt=0; attempt<800; attempt++)); do
      if test -f "$fixture/delete-read-release"; then break; fi
      "$sleep_binary" 0.05
    done
    test -f "$fixture/delete-read-release" || exit 124
  fi
fi
exec "$host_git" "$@"
WRAPPER
chmod 0700 "$step_dir/bin/git"
start_daemon
created=$(rpc project.create '{"key":"removal-bootstrap","project":"missing-runtime-removal"}')
create_op=$(jq -er '.result.operation.id | select(length==36)' <<< "$created")
uuid=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created")
wait_operation "$create_op" >/dev/null
wait_ready "$uuid"
guest /run/current-system/sw/bin/git config user.name P
guest /run/current-system/sw/bin/git config user.email p@example.invalid
guest /run/current-system/sw/bin/bash -c \
  'printf retained > tracked; git add tracked; git commit -qm retained; git push origin HEAD:main'
main_oid=$(guest /run/current-system/sw/bin/git rev-parse HEAD)
created_b=$(rpc session.create '{"key":"removal-sibling","project":"missing-runtime-removal","branch":"sibling","choice":"new","source":"refs/heads/main"}')
create_b_op=$(jq -er '.result.operation.id | select(length==36)' <<< "$created_b")
uuid_b=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created_b")
wait_operation "$create_b_op" >/dev/null
wait_ready "$uuid_b"
key_digest=$(sha256sum "$state/session_keys/$uuid" | cut -d' ' -f1)
guest /usr/libexec/p/codex-adapter init
guest /run/current-system/sw/bin/bash -c \
  'printf lost-dummy > /home/p/.codex/auth.json; chmod 0600 /home/p/.codex/auth.json; printf lost-local > lost-local'
inc exec "p-$uuid_b" --user 1000 --group 1000 --cwd /workspace --env HOME=/home/p -- \
  /usr/libexec/p/codex-adapter init
inc exec "p-$uuid_b" --user 1000 --group 1000 --cwd /workspace --env HOME=/home/p -- \
  /run/current-system/sw/bin/bash -c 'printf sibling-dummy > /home/p/.codex/auth.json; chmod 0600 /home/p/.codex/auth.json'

# An external rename leaves the same UUID elsewhere in the confined project.
# Missing deterministic name must not yield permission to duplicate/adopt it:
# the renamed container still carries the session's identity.
change_session session.stop "$uuid"
renamed="p-renamed-$uuid"
inc rename "p-$uuid" "$renamed"
renamed_identity=$(inc list "^$renamed$" --format json | jq -er '.[0].config["volatile.uuid"]')
for removal_kind in discard delete; do
  expect_refused_plan "$removal_kind" runtime_present
done
rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" |
  jq -e '.result.session.registry_state=="established"' >/dev/null
test "$(sha256sum "$state/session_keys/$uuid" | cut -d' ' -f1)" = "$key_digest"
inc list "^p-$uuid$" --format json | jq -e 'length==0' >/dev/null
test "$(inc list "^$renamed$" --format json | jq -er '.[0].config["volatile.uuid"]')" = "$renamed_identity"
inc rename "$renamed" "p-$uuid"
renamed=
change_session session.start "$uuid"
wait_ready "$uuid"
test "$(guest /run/current-system/sw/bin/cat lost-local)" = lost-local
test "$(guest /run/current-system/sw/bin/cat /home/p/.codex/auth.json)" = lost-dummy

# This fixture-only external fault removes A's native runtime. Start must not
# silently recreate it or disturb B: it is refused, or its operation fails.
inc delete --force "p-$uuid"
inc list "^p-$uuid$" --format json | jq -e 'length==0' >/dev/null
if start_reply=$(rpc session.start "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" 2> "$step_dir/start-missing.err"); then
  start_id=$(jq -er '.result | select(.changed==true and .operation.kind=="session.start") | .operation.id' <<< "$start_reply")
  wait_operation "$start_id" failed >/dev/null
fi
inc list "^p-$uuid$" --format json | jq -e 'length==0' >/dev/null
# A missing runtime needs no acknowledgement: both plans state it and are
# allowed. Nothing of the session's branch leaves P: its sibling holds it.
discard_plan=$(plan_missing discard)
jq -e '.result.plan | .loss==[] and (.details|has("branch_loss")|not)' <<< "$discard_plan" >/dev/null
discard_tag=$(jq -er '.result.change_tag' <<< "$discard_plan")
delete_plan=$(plan_missing delete)
jq -e --arg tip "$main_oid" '.result.plan | .loss==[] and
  (.details | .assigned_tip==$tip and .branch_loss.commits_losing_p_reachability==[])' <<< "$delete_plan" >/dev/null
delete_tag=$(jq -er '.result.change_tag' <<< "$delete_plan")
# A competing UUID identity appearing after the dry runs invalidates their
# tags: each run plans again, is refused as changed and records nothing.
# The fixture owns this stopped dummy instance and removes it explicitly.
competing="p-competing-$uuid"
inc init "$base_image" "$competing" --profile default \
  --config security.idmap.isolated=true --config security.privileged=false \
  --config security.nesting=false --config "user.p.session_uuid=$uuid"
competing_identity=$(inc list "^$competing$" --format json | jq -er '.[0].config["volatile.uuid"]')
expect_refusal session.discard "$(jq -nc --arg uuid "$uuid" --arg tag "$discard_tag" \
  '{uuid:$uuid,key:"discard-stale-runtime",if_match:$tag}')" precondition_failed changed
expect_refusal session.delete "$(jq -nc --arg uuid "$uuid" --arg tag "$delete_tag" \
  '{uuid:$uuid,key:"delete-stale-runtime",if_match:$tag}')" precondition_failed changed
for removal_kind in discard delete; do
  expect_refused_plan "$removal_kind" runtime_present
done
rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" |
  jq -e '.result.session.registry_state=="established"' >/dev/null
test "$(sha256sum "$state/session_keys/$uuid" | cut -d' ' -f1)" = "$key_digest"
rpc operation.list '{"limit":20}' | jq -e '.result.operations |
  all(.[]; .idempotency_key!="discard-stale-runtime" and .idempotency_key!="delete-stale-runtime")' >/dev/null
inc list "^p-$uuid$" --format json | jq -e 'length==0' >/dev/null
test "$(inc list "^$competing$" --format json | jq -er '.[0].config["volatile.uuid"]')" = "$competing_identity"
inc delete "$competing"
competing=
# Genuine absence remains eligible for both explicit removal outcomes.
discard_uuid=$uuid
tag=$(plan_missing discard | jq -er '.result.change_tag')
removed=$(rpc session.discard "$(jq -nc --arg uuid "$uuid" --arg tag "$tag" \
  '{uuid:$uuid,key:"missing-discard",if_match:$tag}')")
wait_operation "$(jq -er '.result.operation.id' <<< "$removed")" >/dev/null
test ! -e "$state/session_keys/$discard_uuid"
test ! -e "$endpoint_prefix/$discard_uuid"
rpc session.list '{"limit":8}' | jq -e --arg old "$discard_uuid" \
  '.result.sessions | all(.[];.uuid!=$old)' >/dev/null
rpc project.branches '{"project":"missing-runtime-removal","limit":8}' |
  jq -e --arg tip "$main_oid" '.result.refs | any(.[];.ref=="refs/heads/main" and .oid==$tip)' >/dev/null
created=$(rpc session.create '{"key":"missing-delete-create","project":"missing-runtime-removal","branch":"main","choice":"existing"}')
uuid=$(jq -er '.result.operation.session_uuid' <<< "$created")
test "$uuid" != "$discard_uuid"
wait_operation "$(jq -er '.result.operation.id' <<< "$created")" >/dev/null
wait_ready "$uuid"
inc delete --force "p-$uuid"
tag=$(plan_missing delete | jq -er '.result.change_tag')
printf '%s\n' "$uuid" > "$step_dir/delete-read-arm"
removed=$(rpc session.delete "$(jq -nc --arg uuid "$uuid" --arg tag "$tag" \
  '{uuid:$uuid,key:"missing-delete",if_match:$tag}')")
delete_op=$(jq -er '.result.operation.id' <<< "$removed")
pause_deadline=$((SECONDS+40))
while test ! -f "$step_dir/delete-read-paused" && ((SECONDS < pause_deadline)); do sleep 0.1; done
test -f "$step_dir/delete-read-paused"
rpc operation.inspect "$(jq -nc --arg id "$delete_op" '{id:$id}')" |
  jq -e '.result.operation | .status=="running" and .phase=="secrets-absent" and .committed==true' >/dev/null
stop_daemon
competing="p-recovery-$uuid"
inc init "$base_image" "$competing" --profile default \
  --config security.idmap.isolated=true --config security.privileged=false \
  --config security.nesting=false --config "user.p.session_uuid=$uuid"
competing_identity=$(inc list "^$competing$" --format json | jq -er '.[0].config["volatile.uuid"]')
rm "$step_dir/delete-read-arm"
touch "$step_dir/delete-read-release"
start_daemon
blocked=$(wait_operation "$delete_op" blocked)
jq -e '.result.operation | .phase=="secrets-absent" and .committed==true' <<< "$blocked" >/dev/null
rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" |
  jq -e '.result.session.registry_state=="removing"' >/dev/null
rpc project.branches '{"project":"missing-runtime-removal","limit":8}' |
  jq -e --arg tip "$main_oid" '.result.refs | any(.[];.ref=="refs/heads/main" and .oid==$tip)' >/dev/null
test "$(inc list "^$competing$" --format json | jq -er '.[0].config["volatile.uuid"]')" = "$competing_identity"
inc delete "$competing"
competing=
rpc operation.retry "$(jq -nc --arg id "$delete_op" '{id:$id}')" |
  jq -e --arg id "$delete_op" '.result.operation.id==$id' >/dev/null
wait_operation "$delete_op" >/dev/null
test ! -e "$state/session_keys/$uuid"
test ! -e "$endpoint_prefix/$uuid"
rpc project.branches '{"project":"missing-runtime-removal","limit":8}' |
  jq -e --arg tip "$main_oid" '.result.refs | length==1 and .[0].ref=="refs/heads/sibling" and .[0].oid==$tip' >/dev/null
test "$(inc exec "p-$uuid_b" --user 1000 --group 1000 --cwd /workspace --env HOME=/home/p -- \
  /run/current-system/sw/bin/cat /home/p/.codex/auth.json)" = sibling-dummy
stop_daemon
start_daemon
rpc session.list '{"limit":8}' | jq -e --arg sibling "$uuid_b" \
  '.result.sessions | length==1 and .[0].uuid==$sibling and .[0].registry_state=="established"' >/dev/null
test ! -e "$endpoint_prefix/$discard_uuid"
test ! -e "$endpoint_prefix/$uuid"
echo P_UUID_REMOVAL_ABSENCE_PASS
echo P_MISSING_RUNTIME_REMOVAL_PASS
