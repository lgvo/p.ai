# shellcheck shell=bash
# Installed production catalog, actual CLI/daemon composition, no authentication.
set -E
umask 077
step_dir="$P_TEST_TMP/step-48"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
log_file="$state/events.ndjson"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step48
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
daemon_pid=
sessions=()
inc() { timeout 90 incus --force-local --project user-1000 "$@"; }
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
  for target in "${sessions[@]}"; do
    inc delete --force "p-$target" >/dev/null 2>&1 || true
    rm -rf -- "${endpoint_prefix:?}/${target:?}"
  done
  rmdir "$endpoint_prefix" 2>/dev/null || true
}
on_error() {
  printf 'P_BUNDLED_DISTRIBUTION_FAIL line=%s status=%s\n' "$2" "$1" >&2
  tail -c 2048 "$step_dir/daemon.err" >&2 || true
}
trap cleanup EXIT
trap 'on_error "$?" "$LINENO"' ERR

p version > "$step_dir/version.json"
jq -e '.version=="0.1.0-dev" and .go_version=="go1.26.7" and
  .control_api_version==1 and .plugin_api_version=="1.0" and
  any(.dependencies[]; .path=="modernc.org/sqlite" and .version=="v1.39.1") and
  any(.dependencies[]; .path=="github.com/tetratelabs/wazero" and .version=="v1.12.0")' \
  "$step_dir/version.json" >/dev/null
catalog="$(dirname "$(dirname "$(readlink -f "$(command -v p)")")")/share/p/plugins"
p plugins defaults "$log_file" > "$step_dir/activation.json"
jq -e --arg catalog "$catalog" --arg log "$log_file" '
  .schema=="p.activation/v1" and (.plugins|length)==6 and
  all(.plugins[]; (.path|startswith($catalog+"/")) and (.sha256|length)==64 and (.grants|length)==1) and
  any(.plugins[]; .id=="org.p.filelog" and .config.path==$log)' "$step_dir/activation.json" >/dev/null
p plugins list "$catalog" | jq -e '(.packages|length)==6 and (.rejected|length)==0' >/dev/null
p plugins activate "$step_dir/activation.json" | jq -e 'length==6' >/dev/null
p plugins plan-assets "$step_dir/activation.json" org.p.tmux-host > "$step_dir/host-plan.json"
p plugins plan-assets "$step_dir/activation.json" org.p.codex-adapter > "$step_dir/agent-plan.json"
test ! -e "$log_file"
image=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
jq -n --arg state "$state" --arg activation "$step_dir/activation.json" \
  --arg incus_binary "$(command -v incus)" --arg prefix "$endpoint_prefix" --arg image "$image" \
  '{schema:"p.host/v1",state_dir:$state,
    git:{activation_path:$activation,source_plugin_id:"org.p.git",listen:"127.0.0.1:0"},
    runtime:{activation_path:$activation,runtime_plugin_id:"org.p.runtime.incus",
      host_plugin_id:"org.p.tmux-host",agent_adapter:{activation_path:$activation,plugin_id:"org.p.codex-adapter"},
      incus_binary:$incus_binary,incus_user_socket:"/var/lib/incus/unix.socket.user",
      incus_project:"user-1000",endpoint_prefix:$prefix,
      disk_source_ceilings:["/var/lib/p-vm/endpoints","/var/lib/p-vm/grants"],base_image_fingerprint:$image,
      environment:{activation_path:$activation,plugin_id:"org.p.environment.nix",
        system:"x86_64-linux",builder_storage_pool:"builders"},
      project_policy:{network:"none",filesystem_mounts:[],command:["/run/current-system/sw/bin/bash"]}},
    events:{activation_path:$activation,plugin_id:"org.p.filelog"}}' > "$step_dir/host.json"
bash "$P_TEST_SOURCE/tests/integration/public-egress-host-config.sh" "$step_dir/host.json"
start_daemon() {
  p daemon "$step_dir/host.json" >> "$step_dir/daemon.out" 2>> "$step_dir/daemon.err" &
  daemon_pid=$!
  local deadline=$((SECONDS+30))
  while ((SECONDS < deadline)); do
    if test -S "$socket" && rpc system.health | jq -e '.result.control_state=="ready"' >/dev/null 2>&1; then return; fi
    if ! kill -0 "$daemon_pid" 2>/dev/null; then return 1; fi
    sleep 0.1
  done
  return 1
}
wait_operation() {
  local id="$1" response status deadline=$((SECONDS+300))
  while ((SECONDS < deadline)); do
    response=$(rpc operation.inspect "$(jq -nc --arg id "$id" '{v:1,id:$id}')")
    status=$(jq -er '.result.operation.status' <<< "$response")
    if test "$status" = completed; then printf '%s\n' "$response"; return; fi
    if test "$status" = blocked || test "$status" = failed || test "$status" = unknown; then
      echo "distribution operation did not complete: $response" >&2; return 1
    fi
    sleep 0.3
  done
  return 1
}
wait_ready() {
  local target="$1" deadline=$((SECONDS+90))
  while ((SECONDS < deadline)); do
    if rpc session.inspect "$(jq -nc --arg uuid "$target" '{v:1,uuid:$uuid}')" |
      jq -e '.result.session.session_condition=="ready"' >/dev/null; then return; fi
    sleep 0.3
  done
  return 1
}
start_daemon
created=$(rpc project.create '{"v":1,"key":"distribution-bootstrap","project":"bundled-distribution"}')
boot_uuid=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created")
sessions+=("$boot_uuid")
wait_operation "$(jq -er '.result.operation.id' <<< "$created")" >/dev/null
wait_ready "$boot_uuid"
guest "$boot_uuid" git config user.name P
guest "$boot_uuid" git config user.email p@example.invalid
guest "$boot_uuid" /run/current-system/sw/bin/bash -c 'printf "retained source\n" > source.txt; git add source.txt; git commit -qm initial; git push origin HEAD:main'
oid=$(guest "$boot_uuid" git rev-parse HEAD)
created=$(rpc session.create '{"v":1,"key":"distribution-committed","project":"bundled-distribution","branch":"composed","choice":"new","source":"refs/heads/main"}')
uuid=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created")
sessions+=("$uuid")
result=$(wait_operation "$(jq -er '.result.operation.id' <<< "$created")")
environment_digest=$(jq -er '.plugins[] | select(.id=="org.p.environment.nix") | .sha256' "$step_dir/activation.json")
jq -e --arg digest "$environment_digest" --arg oid "$oid" '.result.operation.evidence |
  .environment.module_id=="org.p.environment.nix" and .environment.module_sha256==$digest and
  .captured_oid==$oid and .environment_state!=null and (.environment_state.flake_present // false)==false and
  (.environment_state.key // "")==""' <<< "$result" >/dev/null
wait_ready "$uuid"
rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
  jq -e --arg oid "$oid" --arg image "$image" '.result.session.environment |
    .commit_oid==$oid and .selection=="base" and .cache=="none" and .image_fingerprint==$image' >/dev/null
test "$(guest "$uuid" git rev-parse HEAD)" = "$oid"
guest "$uuid" /run/current-system/sw/bin/test -x /usr/libexec/p/codex-adapter
# Adapter data is an explicit event fixture, not authenticated Codex execution.
prior_sequence=$(rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
  jq -er '.result.session.latest_unattended_condition.receive_sequence // 0')
notify='{"type":"agent-turn-complete","thread-id":"b5f6c1c2-1111-2222-3333-444455556666","turn-id":"turn-1","cwd":"/workspace","input-messages":[],"last-assistant-message":"DUMMY FIXTURE"}'
guest "$uuid" /usr/libexec/p/codex-adapter notify "$notify"
deadline=$((SECONDS+10))
while ((SECONDS < deadline)); do
  if rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
    jq -e --argjson prior "$prior_sequence" '.result.session.latest_unattended_condition |
      .condition=="idle" and .source=="codex/thread/b5f6c1c2-1111-2222-3333-444455556666" and
      .reason=="turn complete" and .adapter=="codex" and .adapter_version=="0.151.0" and
      .receive_sequence>$prior' >/dev/null; then break; fi
  sleep 0.2
done
rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
  jq -e --argjson prior "$prior_sequence" '.result.session.latest_unattended_condition |
    .condition=="idle" and .source=="codex/thread/b5f6c1c2-1111-2222-3333-444455556666" and
    .reason=="turn complete" and .adapter=="codex" and .adapter_version=="0.151.0" and
    .receive_sequence>$prior' >/dev/null
echo P_BUNDLED_CODEX_EVENT_FIXTURE_PASS
rpc session.stop "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
  jq -e '.result.session.session_condition=="stopped"' >/dev/null
stop_daemon
start_daemon
repo_hash=$(printf %s bundled-distribution | sha256sum | cut -d' ' -f1)
test "$(GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null git --git-dir="$state/repositories/$repo_hash.git" rev-parse refs/heads/main)" = "$oid"
rpc session.start "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" >/dev/null
wait_ready "$uuid"
test "$(guest "$uuid" git rev-parse HEAD)" = "$oid"
jq -se --arg uuid "$uuid" 'any(.[]; .schema=="p.event/v1" and .session==$uuid and .kind=="session.condition_changed")' "$log_file" >/dev/null
if grep -Fq 'DUMMY FIXTURE' "$log_file"; then echo 'raw event payload escaped' >&2; exit 1; fi
echo P_BUNDLED_DISTRIBUTION_PASS
