# shellcheck shell=bash
# Selected stopped-runtime principal repair. Codex files contain dummy data only.
set -E
umask 077
step_dir="$P_TEST_TMP/step-40"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step40
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
incus_real=$(command -v incus)
daemon_pid=
uuid=
sibling_uuid=
inc() { timeout 90 "$incus_real" --force-local --project user-1000 "$@"; }
rpc() { timeout 45 p api "$socket" "$@"; }
bare_git() { GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null git --git-dir="$bare" "$@"; }
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
      inc delete --force "p-$target" >/dev/null 2>&1 || true
      rm -rf -- "${endpoint_prefix:?}/${target:?}"
    fi
  done
  rmdir "$endpoint_prefix" 2>/dev/null || true
}
on_error() {
  printf 'P_PRINCIPAL_REPAIR_FAIL line=%s status=%s\n' "$2" "$1" >&2
  if test -n "${preview:-}"; then
    jq -c '.result.preview | {registration_status,key_status,guest_key_status,
      runtime_status,assigned_ref_status,eligible,unsafe_reasons}' <<< "$preview" >&2 || true
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
    if ! kill -0 "$daemon_pid" 2>/dev/null; then echo 'principal repair daemon exited' >&2; return 1; fi
    sleep 0.1
  done
  echo 'principal repair daemon health unavailable' >&2
  return 1
}
operation_id() { jq -er '.result.operation.id | select(length==36)' <<< "$1"; }
wait_operation() {
  local id="$1" response status deadline=$((SECONDS+180))
  while ((SECONDS < deadline)); do
    response=$(rpc operation.inspect "$(jq -nc --arg id "$id" '{v:1,id:$id}')")
    status=$(jq -er '.result.operation.status' <<< "$response")
    if test "$status" = completed; then printf '%s\n' "$response"; return; fi
    if test "$status" = blocked || test "$status" = failed || test "$status" = unknown; then
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
    . | select(length==1 and .[0].name==$name and .[0].status=="Stopped") |
    .[0] | {name,status,uuid:.config["volatile.uuid"],generation:.config["volatile.uuid.generation"]} |
    select((.uuid|test("^[0-9a-f-]{36}$")) and (.generation|test("^[0-9a-f-]{36}$")))'
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
  (index("session.principal.repair.preview") != null and
   index("session.principal.repair.confirm") != null)' >/dev/null
created=$(rpc project.create '{"v":1,"key":"principal-bootstrap","project":"principal-repair"}')
create_op=$(operation_id "$created")
uuid=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created")
wait_operation "$create_op" >/dev/null
wait_ready "$uuid"
guest "$uuid" git config user.name P
guest "$uuid" git config user.email p@example.invalid
guest "$uuid" /run/current-system/sw/bin/bash -c \
  'printf retained > tracked; git add tracked; git commit -qm retained; git push origin HEAD:main'
tip=$(guest "$uuid" git rev-parse HEAD)
created=$(rpc session.create '{"v":1,"key":"principal-sibling","project":"principal-repair","branch":"sibling","choice":"new","source":"refs/heads/main"}')
sibling_op=$(operation_id "$created")
sibling_uuid=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created")
wait_operation "$sibling_op" >/dev/null
wait_ready "$sibling_uuid"
guest "$uuid" /usr/libexec/p/codex-adapter init
guest "$uuid" /run/current-system/sw/bin/bash -c \
  'printf dummy-secret > /home/p/.codex/auth.json; chmod 0600 /home/p/.codex/auth.json; printf dirty > tracked'
guest "$sibling_uuid" /usr/libexec/p/codex-adapter init
guest "$sibling_uuid" /run/current-system/sw/bin/bash -c \
  'printf sibling-dummy > /home/p/.codex/auth.json; chmod 0600 /home/p/.codex/auth.json'
sibling_key_digest=$(sha256sum "$state/session_keys/$sibling_uuid" | cut -d' ' -f1)
tracked_digest=$(guest "$uuid" sha256sum tracked | cut -d' ' -f1)

rpc session.stop "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
  jq -e '.result.session.session_condition=="stopped"' >/dev/null
source_before=$(source_identity)
healthy=$(rpc session.principal.repair.preview "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')")
jq -e '.result.preview | .eligible==false and (.unsafe_reasons|index("principal_intact")!=null)' <<< "$healthy" >/dev/null
old_fingerprint=$(jq -er '.result.preview.old_fingerprint | select(length==64)' <<< "$healthy")
old_key_digest=$(sha256sum "$state/session_keys/$uuid" | cut -d' ' -f1)
cp "$state/session_keys/$uuid" "$step_dir/old-session-key"
rm "$state/session_keys/$uuid"
# A historical project.create record cannot turn a deleted committed P ref
# into an unborn branch for credential repair. Restore the fixture ref after
# proving refusal so the selected scenario remains only a key fault.
repo_hash=$(printf %s principal-repair | sha256sum | cut -d' ' -f1)
bare="$state/repositories/$repo_hash.git"
test "$(bare_git rev-parse --verify refs/heads/main)" = "$tip"
bare_git update-ref -d refs/heads/main "$tip"
missing_ref=$(rpc session.principal.repair.preview "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')")
jq -e '.result.preview | .assigned_ref_status=="missing" and .eligible==false and
  (.unsafe_reasons|index("assigned_ref_missing")!=null) and
  (.confirmation_token//"")==""' <<< "$missing_ref" >/dev/null
bare_git update-ref refs/heads/main "$tip" 0000000000000000000000000000000000000000
preview=$(rpc session.principal.repair.preview "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')")
jq -e --arg uuid "$uuid" --arg tip "$tip" --arg old "$old_fingerprint" '
  .result.preview | .kind=="session_git_principal" and .session_uuid==$uuid and
  .project=="principal-repair" and .branch=="main" and
  .assigned_ref_status=="present" and .assigned_tip==$tip and
  .old_fingerprint==$old and .registration_status=="active" and
  .key_status=="missing" and .guest_key_status=="matching" and
  (.guest_key_sha256|length==64) and .runtime_status=="Stopped" and
  .incus_project=="user-1000" and .instance_name==("p-"+$uuid) and
  (.incus_uuid|test("^[0-9a-f-]{36}$")) and (.generation|test("^[0-9a-f-]{36}$")) and
  (.image_fingerprint|length==64) and (.policy_sha256|length==64) and
  .unsafe_reasons==[] and .eligible==true and
  (.confirmation_token|test("^[0-9a-f]{32}$"))' <<< "$preview" >/dev/null
token=$(jq -er '.result.preview.confirmation_token' <<< "$preview")
test "$(source_identity)" = "$source_before"
test ! -e "$state/session_keys/$uuid"
expect_busy session.principal.repair.confirm "$(jq -nc --arg uuid "$sibling_uuid" --arg token "$token" \
  '{v:1,key:"principal-wrong-session",uuid:$uuid,confirmation_token:$token}')"
confirmed=$(rpc session.principal.repair.confirm "$(jq -nc --arg uuid "$uuid" --arg token "$token" \
  '{v:1,key:"principal-confirmed",uuid:$uuid,confirmation_token:$token}')")
repair_op=$(operation_id "$confirmed")
repaired=$(wait_operation "$repair_op")
jq -e '.result.operation | .kind=="session.principal.repair" and .status=="completed" and
  .phase=="completed" and .committed==true' <<< "$repaired" >/dev/null
test "$(source_identity)" = "$source_before"
new_key_digest=$(sha256sum "$state/session_keys/$uuid" | cut -d' ' -f1)
test "$new_key_digest" != "$old_key_digest"
inc file pull "p-$uuid/etc/p/git/identity" "$step_dir/guest-key"
test "$(sha256sum "$step_dir/guest-key" | cut -d' ' -f1)" = "$new_key_digest"
test "$(sha256sum "$state/session_keys/$sibling_uuid" | cut -d' ' -f1)" = "$sibling_key_digest"
test "$(rpc session.principal.repair.confirm "$(jq -nc --arg uuid "$uuid" --arg token "$token" \
  '{v:1,key:"principal-confirmed",uuid:$uuid,confirmation_token:$token}')" |
  jq -er '.result.operation.id')" = "$repair_op"
# The live P Git server accepts the replacement identity and rejects the
# saved old identity at the same exact project endpoint.
capabilities=$(rpc system.capabilities)
git_endpoint=$(jq -er '.result.git.endpoint | select(startswith("127.0.0.1:"))' <<< "$capabilities")
printf '%s\n' "$(jq -er '.result.git.known_hosts' <<< "$capabilities")" > "$step_dir/known_hosts"
git_url="ssh://git@127.0.0.1:${git_endpoint##*:}/principal-repair"
git_probe_key() {
  local key="$1" ssh_command
  ssh_command="ssh -F /dev/null -i $key -o IdentitiesOnly=yes -o IdentityAgent=none -o StrictHostKeyChecking=yes -o UserKnownHostsFile=$step_dir/known_hosts -o BatchMode=yes -o ConnectTimeout=5"
  GIT_SSH_COMMAND="$ssh_command" timeout 30 git ls-remote "$git_url" refs/heads/main
}
test "$(git_probe_key "$state/session_keys/$uuid" | cut -f1)" = "$tip"
if git_probe_key "$step_dir/old-session-key" > "$step_dir/old-key.out" 2> "$step_dir/old-key.err"; then
  echo 'revoked old Git key authenticated after principal repair' >&2; exit 1
fi
grep -Eq 'Permission denied|publickey' "$step_dir/old-key.err"
test "$(git_probe_key "$state/session_keys/$uuid" | cut -f1)" = "$tip"
rm "$step_dir/old-session-key"
rpc project.branches '{"v":1,"project":"principal-repair","limit":8}' |
  jq -e --arg tip "$tip" '.result.refs | length==2 and
    any(.[]; .ref=="refs/heads/main" and .oid==$tip) and
    any(.[]; .ref=="refs/heads/sibling" and .oid==$tip)' >/dev/null

rpc session.start "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" >/dev/null
wait_ready "$uuid"
test "$(guest "$uuid" sha256sum tracked | cut -d' ' -f1)" = "$tracked_digest"
test "$(guest "$uuid" cat /home/p/.codex/auth.json)" = dummy-secret
test "$(guest "$sibling_uuid" cat /home/p/.codex/auth.json)" = sibling-dummy
guest "$uuid" /run/current-system/sw/bin/bash -c \
  'printf new > repaired; git add repaired; git commit -qm repaired; git push origin HEAD:main'
new_tip=$(guest "$uuid" git rev-parse HEAD)
rpc project.branches '{"v":1,"project":"principal-repair","limit":8}' |
  jq -e --arg tip "$new_tip" 'any(.result.refs[]; .ref=="refs/heads/main" and .oid==$tip)' >/dev/null
stop_daemon
start_daemon
wait_ready "$uuid"
test "$(sha256sum "$state/session_keys/$uuid" | cut -d' ' -f1)" = "$new_key_digest"
test "$(guest "$uuid" cat /home/p/.codex/auth.json)" = dummy-secret
test "$(guest "$sibling_uuid" cat /home/p/.codex/auth.json)" = sibling-dummy
echo P_PRINCIPAL_REPAIR_PASS
