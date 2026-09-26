# shellcheck shell=bash
# Explicit public repair after a fixture-caused runtime loss. No login occurs.
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
incus_real=$(command -v incus)
host_git=$(command -v git)

inc() { timeout 60 "$incus_real" --force-local --project user-1000 "$@"; }
rpc() { timeout 35 p api "$socket" "$@"; }
expect_busy() {
  local method="$1" params="$2" response status=0
  response=$(rpc "$method" "$params") || status=$?
  test "$status" -eq 1
  jq -e '.jsonrpc=="2.0" and .id==1 and (has("result")|not) and
    .error.code== -32003 and .error.kind=="busy" and
    (.error.message|type=="string" and length>0)' <<< "$response" >/dev/null
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
on_error() {
  printf 'P_MISSING_RUNTIME_REPAIR_FAIL line=%s status=%s\n' "$2" "$1" >&2
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
  PATH="$step_dir/bin:$PATH" p daemon "$step_dir/host.json" >> "$step_dir/daemon.out" 2>> "$step_dir/daemon.err" &
  daemon_pid=$!
  local deadline=$((SECONDS+30))
  while ((SECONDS < deadline)); do
    if test -S "$socket" && rpc system.health | jq -e '.result.control_state=="ready"' >/dev/null 2>&1; then return; fi
    if ! kill -0 "$daemon_pid" 2>/dev/null; then echo 'repair daemon exited' >&2; return 1; fi
    sleep 0.1
  done
  echo 'repair daemon health unavailable' >&2
  return 1
}
wait_operation() {
  local id="$1" expected="${2:-completed}" result status deadline=$((SECONDS+150))
  while ((SECONDS < deadline)); do
    result=$(rpc operation.inspect "$(jq -nc --arg id "$id" '{v:1,id:$id}')")
    status=$(jq -er '.result.operation.status' <<< "$result")
    if test "$status" = "$expected"; then printf '%s\n' "$result"; return; fi
    if test "$status" = blocked || test "$status" = failed || test "$status" = superseded; then
      echo "unexpected operation status: $result" >&2; return 1
    fi
    sleep 0.3
  done
  echo "operation $id did not complete" >&2
  return 1
}
wait_ready() {
  local id="$1" deadline=$((SECONDS+80))
  while ((SECONDS < deadline)); do
    if rpc session.inspect "$(jq -nc --arg uuid "$id" '{v:1,uuid:$uuid}')" |
      jq -e '.result.session.session_condition=="ready"' >/dev/null; then return; fi
    sleep 0.3
  done
  echo "session $id did not become ready" >&2
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
rpc system.capabilities | jq -e '.result.available |
  (index("session.repair.preview") != null and index("session.repair.confirm") != null)' >/dev/null
created=$(rpc project.create '{"v":1,"key":"repair-bootstrap","project":"missing-runtime-repair"}')
create_op=$(jq -er '.result.operation.id | select(length==36)' <<< "$created")
uuid=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created")
wait_operation "$create_op" >/dev/null
wait_ready "$uuid"
guest /run/current-system/sw/bin/git config user.name P
guest /run/current-system/sw/bin/git config user.email p@example.invalid
guest /run/current-system/sw/bin/bash -c \
  'printf retained > tracked; git add tracked; git commit -qm retained; git push origin HEAD:main'
main_oid=$(guest /run/current-system/sw/bin/git rev-parse HEAD)
created_b=$(rpc session.create '{"v":1,"key":"repair-sibling","project":"missing-runtime-repair","branch":"sibling","choice":"new","source":"refs/heads/main"}')
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
# Missing deterministic name must not yield permission to duplicate/adopt it.
rpc session.stop "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" >/dev/null
renamed="p-renamed-$uuid"
inc rename "p-$uuid" "$renamed"
renamed_identity=$(inc list "^$renamed$" --format json | jq -er '.[0].config["volatile.uuid"]')
rename_preview=$(rpc session.repair.preview "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')")
jq -e '.result.preview | .eligible==false and .blocked_reason=="runtime_absence_unverified" and
  (has("confirmation_token")|not)' <<< "$rename_preview" >/dev/null
for removal_kind in discard delete; do
  expect_busy session.removal.preview "$(jq -nc --arg uuid "$uuid" --arg kind "$removal_kind" \
    '{v:1,uuid:$uuid,kind:$kind,acknowledge_missing_runtime:true}')"
done
rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
  jq -e '.result.session.registry_state=="established"' >/dev/null
test "$(sha256sum "$state/session_keys/$uuid" | cut -d' ' -f1)" = "$key_digest"
inc list "^p-$uuid$" --format json | jq -e 'length==0' >/dev/null
test "$(inc list "^$renamed$" --format json | jq -er '.[0].config["volatile.uuid"]')" = "$renamed_identity"
inc rename "$renamed" "p-$uuid"
renamed=
rpc session.start "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" >/dev/null
wait_ready "$uuid"
test "$(guest /run/current-system/sw/bin/cat lost-local)" = lost-local
test "$(guest /run/current-system/sw/bin/cat /home/p/.codex/auth.json)" = lost-dummy

# This fixture-only external fault removes A's native runtime. Public repair
# must not silently recreate it through Start or disturb B.
inc delete --force "p-$uuid"
inc list "^p-$uuid$" --format json | jq -e 'length==0' >/dev/null
if start_reply=$(rpc session.start "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" 2> "$step_dir/start-missing.err"); then
  jq -e '.result.session.session_condition=="missing"' <<< "$start_reply" >/dev/null
fi
inc list "^p-$uuid$" --format json | jq -e 'length==0' >/dev/null
preview=$(rpc session.repair.preview "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')")
jq -e --arg uuid "$uuid" --arg tip "$main_oid" --arg image "$base_image" '
  .result.preview | .kind=="missing_runtime" and .session_uuid==$uuid and
  .branch=="main" and .assigned_tip==$tip and .image_fingerprint==$image and
  (.image_source_commit // "")=="" and .image_source_differs==true and
  .image_status=="present" and .runtime_local_loss=="unrecoverable" and
  .eligible==true and (.policy_sha256|length==64) and
  .incus_project=="user-1000" and .instance_name==("p-"+$uuid) and
  (.credential_fingerprint|length==64) and
  (.confirmation_token|test("^[0-9a-f]{32}$"))
' <<< "$preview" >/dev/null
token=$(jq -er '.result.preview.confirmation_token' <<< "$preview")
discard_preview=$(rpc session.removal.preview "$(jq -nc --arg uuid "$uuid" \
  '{v:1,uuid:$uuid,kind:"discard",acknowledge_missing_runtime:true}')")
discard_token=$(jq -er '.result.preview.confirmation_token' <<< "$discard_preview")
delete_preview=$(rpc session.removal.preview "$(jq -nc --arg uuid "$uuid" \
  '{v:1,uuid:$uuid,kind:"delete",acknowledge_missing_runtime:true}')")
delete_token=$(jq -er '.result.preview.confirmation_token' <<< "$delete_preview")
# A competing UUID identity appearing after preview invalidates admission.
# The fixture owns this stopped dummy instance and removes it explicitly.
competing="p-competing-$uuid"
inc init "$base_image" "$competing" --profile default \
  --config security.idmap.isolated=true --config security.privileged=false \
  --config security.nesting=false --config "user.p.session_uuid=$uuid"
competing_identity=$(inc list "^$competing$" --format json | jq -er '.[0].config["volatile.uuid"]')
expect_busy session.repair.confirm "$(jq -nc --arg uuid "$uuid" --arg token "$token" \
  '{v:1,key:"repair-stale-runtime",uuid:$uuid,confirmation_token:$token}')"
expect_busy session.discard "$(jq -nc --arg uuid "$uuid" --arg token "$discard_token" \
  '{v:1,key:"discard-stale-runtime",uuid:$uuid,confirmation_token:$token}')"
expect_busy session.delete "$(jq -nc --arg uuid "$uuid" --arg token "$delete_token" \
  '{v:1,key:"delete-stale-runtime",uuid:$uuid,confirmation_token:$token}')"
for removal_kind in discard delete; do
  expect_busy session.removal.preview "$(jq -nc --arg uuid "$uuid" --arg kind "$removal_kind" \
    '{v:1,uuid:$uuid,kind:$kind,acknowledge_missing_runtime:true}')"
done
rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
  jq -e '.result.session.registry_state=="established"' >/dev/null
test "$(sha256sum "$state/session_keys/$uuid" | cut -d' ' -f1)" = "$key_digest"
rpc operation.list '{"v":1,"limit":20}' | jq -e '.result.operations |
  all(.[]; .idempotency_key!="discard-stale-runtime" and .idempotency_key!="delete-stale-runtime")' >/dev/null
inc list "^p-$uuid$" --format json | jq -e 'length==0' >/dev/null
test "$(inc list "^$competing$" --format json | jq -er '.[0].config["volatile.uuid"]')" = "$competing_identity"
inc delete "$competing"
competing=
preview=$(rpc session.repair.preview "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')")
token=$(jq -er '.result.preview.confirmation_token' <<< "$preview")
confirmed=$(rpc session.repair.confirm "$(jq -nc --arg uuid "$uuid" --arg token "$token" \
  '{v:1,key:"repair-confirmed",uuid:$uuid,confirmation_token:$token}')")
repair_op=$(jq -er '.result.operation.id | select(length==36)' <<< "$confirmed")
done_op=$(wait_operation "$repair_op")
jq -e '.result.operation | .kind=="session.repair" and .status=="completed" and .phase=="completed"' <<< "$done_op" >/dev/null
wait_ready "$uuid"
inc list "^p-$uuid$" --format json | jq -e 'length==1 and .[0].status=="Running"' >/dev/null
test "$(guest /run/current-system/sw/bin/git rev-parse HEAD)" = "$main_oid"
test "$(guest /run/current-system/sw/bin/git branch --show-current)" = main
test "$(guest /run/current-system/sw/bin/cat tracked)" = retained
guest /run/current-system/sw/bin/bash -c 'test ! -e /home/p/.codex/auth.json && test ! -e /workspace/lost-local'
test "$(sha256sum "$state/session_keys/$uuid" | cut -d' ' -f1)" = "$key_digest"
test -S "$endpoint_prefix/$uuid/git.sock"
test "$(rpc session.repair.confirm "$(jq -nc --arg uuid "$uuid" --arg token "$token" \
  '{v:1,key:"repair-confirmed",uuid:$uuid,confirmation_token:$token}')" |
  jq -er '.result.operation.id')" = "$repair_op"
rpc project.branches '{"v":1,"project":"missing-runtime-repair","limit":8}' |
  jq -e --arg tip "$main_oid" '.result.refs | length==2 and
    any(.[]; .ref=="refs/heads/main" and .oid==$tip) and
    any(.[]; .ref=="refs/heads/sibling" and .oid==$tip)' >/dev/null
test "$(inc exec "p-$uuid_b" --user 1000 --group 1000 --cwd /workspace --env HOME=/home/p -- \
  /run/current-system/sw/bin/cat /home/p/.codex/auth.json)" = sibling-dummy
stop_daemon
start_daemon
wait_ready "$uuid"
test "$(guest /run/current-system/sw/bin/git rev-parse HEAD)" = "$main_oid"
test "$(sha256sum "$state/session_keys/$uuid" | cut -d' ' -f1)" = "$key_digest"
# Genuine absence remains eligible for both explicit removal outcomes. These
# fixture-caused losses occur after all repair/persistence assertions above.
discard_uuid=$uuid
inc delete --force "p-$uuid"
preview=$(rpc session.removal.preview "$(jq -nc --arg uuid "$uuid" \
  '{v:1,uuid:$uuid,kind:"discard",acknowledge_missing_runtime:true}')")
token=$(jq -er '.result.preview.confirmation_token' <<< "$preview")
removed=$(rpc session.discard "$(jq -nc --arg uuid "$uuid" --arg token "$token" \
  '{v:1,key:"missing-discard",uuid:$uuid,confirmation_token:$token}')")
wait_operation "$(jq -er '.result.operation.id' <<< "$removed")" >/dev/null
test ! -e "$state/session_keys/$discard_uuid"
test ! -e "$endpoint_prefix/$discard_uuid"
rpc session.list '{"v":1,"limit":8}' | jq -e --arg old "$discard_uuid" \
  '.result.sessions | all(.[];.uuid!=$old)' >/dev/null
rpc project.branches '{"v":1,"project":"missing-runtime-repair","limit":8}' |
  jq -e --arg tip "$main_oid" '.result.refs | any(.[];.ref=="refs/heads/main" and .oid==$tip)' >/dev/null
created=$(rpc session.create '{"v":1,"key":"missing-delete-create","project":"missing-runtime-repair","branch":"main","choice":"existing"}')
uuid=$(jq -er '.result.operation.session_uuid' <<< "$created")
test "$uuid" != "$discard_uuid"
wait_operation "$(jq -er '.result.operation.id' <<< "$created")" >/dev/null
wait_ready "$uuid"
inc delete --force "p-$uuid"
preview=$(rpc session.removal.preview "$(jq -nc --arg uuid "$uuid" \
  '{v:1,uuid:$uuid,kind:"delete",acknowledge_missing_runtime:true}')")
token=$(jq -er '.result.preview.confirmation_token' <<< "$preview")
printf '%s\n' "$uuid" > "$step_dir/delete-read-arm"
removed=$(rpc session.delete "$(jq -nc --arg uuid "$uuid" --arg token "$token" \
  '{v:1,key:"missing-delete",uuid:$uuid,confirmation_token:$token}')")
delete_op=$(jq -er '.result.operation.id' <<< "$removed")
pause_deadline=$((SECONDS+40))
while test ! -f "$step_dir/delete-read-paused" && ((SECONDS < pause_deadline)); do sleep 0.1; done
test -f "$step_dir/delete-read-paused"
rpc operation.inspect "$(jq -nc --arg id "$delete_op" '{v:1,id:$id}')" |
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
rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
  jq -e '.result.session.registry_state=="removing"' >/dev/null
rpc project.branches '{"v":1,"project":"missing-runtime-repair","limit":8}' |
  jq -e --arg tip "$main_oid" '.result.refs | any(.[];.ref=="refs/heads/main" and .oid==$tip)' >/dev/null
test "$(inc list "^$competing$" --format json | jq -er '.[0].config["volatile.uuid"]')" = "$competing_identity"
inc delete "$competing"
competing=
rpc operation.retry "$(jq -nc --arg id "$delete_op" '{v:1,id:$id}')" |
  jq -e --arg id "$delete_op" '.result.operation.id==$id' >/dev/null
wait_operation "$delete_op" >/dev/null
test ! -e "$state/session_keys/$uuid"
test ! -e "$endpoint_prefix/$uuid"
rpc project.branches '{"v":1,"project":"missing-runtime-repair","limit":8}' |
  jq -e --arg tip "$main_oid" '.result.refs | length==1 and .[0].ref=="refs/heads/sibling" and .[0].oid==$tip' >/dev/null
test "$(inc exec "p-$uuid_b" --user 1000 --group 1000 --cwd /workspace --env HOME=/home/p -- \
  /run/current-system/sw/bin/cat /home/p/.codex/auth.json)" = sibling-dummy
stop_daemon
start_daemon
rpc session.list '{"v":1,"limit":8}' | jq -e --arg sibling "$uuid_b" \
  '.result.sessions | length==1 and .[0].uuid==$sibling and .[0].registry_state=="established"' >/dev/null
test ! -e "$endpoint_prefix/$discard_uuid"
test ! -e "$endpoint_prefix/$uuid"
echo P_UUID_REMOVAL_ABSENCE_PASS
echo P_MISSING_RUNTIME_REPAIR_PASS
