# shellcheck shell=bash
# Selected Codex adapter asset, auth-free event mapping, and private state.
set -E
umask 077
step_dir="$P_TEST_TMP/step-27"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step27
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
daemon_pid=
uuid=
uuid_b=

inc() { timeout 60 incus --force-local --project user-1000 "$@"; }
rpc() { timeout 30 p api "$socket" "$@"; }
guest() {
  inc exec "p-$uuid" --user 1000 --group 1000 --cwd /workspace --env HOME=/home/p -- "$@"
}
stop_daemon() {
  if test -n "$daemon_pid"; then
    kill -TERM "$daemon_pid" 2>/dev/null || true
    for ((attempt=0; attempt<100; attempt++)); do
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
  if test -n "$uuid_b"; then
    inc delete --force "p-$uuid_b" >/dev/null 2>&1 || true
    rm -rf -- "${endpoint_prefix:?}/${uuid_b:?}"
  fi
  rmdir "$endpoint_prefix" 2>/dev/null || true
}
on_error() {
  printf 'P_CODEX_ADAPTER_FAIL line=%s status=%s\n' "$2" "$1" >&2
  tail -c 2048 "$step_dir/daemon.err" >&2 || true
  if test -n "$uuid"; then inc info "p-$uuid" --show-log 2>&1 | tail -c 1024 >&2 || true; fi
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
p plugins activate "$step_dir/agent.json" | jq -e '.[0].capability=="agent-adapter"' >/dev/null
image=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
test "${#image}" -eq 64
jq -n --arg state "$state" --arg git_activation "$step_dir/git.json" \
  --arg git_id "$(jq -er '.plugins[0].id' "$step_dir/git.json")" \
  --arg runtime_activation "$step_dir/runtime.json" \
  --arg runtime_id "$(jq -er '.plugins[0].id' "$step_dir/runtime.json")" \
  --arg host_id "$(jq -er '.plugins[1].id' "$step_dir/runtime.json")" \
  --arg agent_activation "$step_dir/agent.json" \
  --arg agent_id "$(jq -er '.plugins[0].id' "$step_dir/agent.json")" \
  --arg incus_binary "$(command -v incus)" --arg prefix "$endpoint_prefix" --arg image "$image" \
  '{schema:"p.host/v1",state_dir:$state,
    git:{activation_path:$git_activation,source_plugin_id:$git_id,listen:"127.0.0.1:0"},
    runtime:{activation_path:$runtime_activation,runtime_plugin_id:$runtime_id,
      host_plugin_id:$host_id,agent_adapter:{activation_path:$agent_activation,plugin_id:$agent_id},
      incus_binary:$incus_binary,incus_user_socket:"/var/lib/incus/unix.socket.user",
      incus_project:"user-1000",endpoint_prefix:$prefix,
      disk_source_ceilings:["/var/lib/p-vm/endpoints","/var/lib/p-vm/grants"],base_image_fingerprint:$image,
      project_policy:{network:"none",filesystem_mounts:[],command:["/run/current-system/sw/bin/bash"]}}}' \
  > "$step_dir/host.json"
bash "$P_TEST_SOURCE/tests/integration/public-egress-host-config.sh" "$step_dir/host.json"

p daemon "$step_dir/host.json" > "$step_dir/daemon.out" 2> "$step_dir/daemon.err" &
daemon_pid=$!
deadline=$((SECONDS+30))
while ((SECONDS < deadline)); do
  if test -S "$socket" && rpc system.health | jq -e '.result.control_state=="ready"' >/dev/null 2>&1; then break; fi
  if ! kill -0 "$daemon_pid" 2>/dev/null; then echo 'Codex daemon exited' >&2; exit 1; fi
  sleep 0.1
done
test -S "$socket"

created=$(rpc project.create '{"v":1,"key":"codex-bootstrap","project":"codex-adapter"}')
op=$(jq -er '.result.operation.id | select(length==36)' <<< "$created")
uuid=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created")
inspect() { rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')"; }
deadline=$((SECONDS+150))
while ((SECONDS < deadline)); do
  result=$(rpc operation.inspect "$(jq -nc --arg id "$op" '{v:1,id:$id}')")
  status=$(jq -er '.result.operation.status' <<< "$result")
  if test "$status" = completed; then break; fi
  if test "$status" = blocked; then echo "Codex creation blocked: $result" >&2; exit 1; fi
  sleep 0.4
done
test "$status" = completed
deadline=$((SECONDS+80))
while ((SECONDS < deadline)); do
  if inspect | jq -e '.result.session.session_condition=="ready"' >/dev/null; then break; fi
  sleep 0.4
done
inspect | jq -e '.result.session.session_condition=="ready"' >/dev/null

test "$(inc exec "p-$uuid" -- /run/current-system/sw/bin/stat -c '%u:%g:%a' /etc/p/assets/p-codex-adapter)" = 0:0:555
test "$(inc exec "p-$uuid" -- /run/current-system/sw/bin/stat -c '%u:%g:%a' /etc/p/session.json)" = 0:0:644
inc file pull "p-$uuid/etc/p/session.json" "$step_dir/session.json"
jq -e '.schema=="p.runtime-session/v3" and (.agent_sha256|length==64)' "$step_dir/session.json" >/dev/null
inc file pull "p-$uuid/etc/p/assets/p-codex-adapter" "$step_dir/installed-adapter"
test "$(sha256sum "$step_dir/installed-adapter" | cut -d ' ' -f1)" = "$(jq -er '.agent_sha256' "$step_dir/session.json")"
test "$(inc exec "p-$uuid" -- /run/current-system/sw/bin/readlink /usr/libexec/p/codex-adapter)" = /etc/p/assets/p-codex-adapter
test "$(inc exec "p-$uuid" -- /run/current-system/sw/bin/stat -c '%u:%g:%a' /var/empty)" = 0:0:555
guest /run/current-system/sw/bin/test ! -e /home/p/.codex
guest /usr/libexec/p/codex-adapter init
test "$(guest /run/current-system/sw/bin/stat -c '%u:%g:%a' /home/p/.codex)" = 1000:1000:700
guest /run/current-system/sw/bin/test ! -e /home/p/.codex/tmp/arg0
case "$(guest /run/current-system/sw/bin/codex --version)" in
  'codex-cli 0.151.0'|'codex 0.151.0') ;;
  *) echo 'wrong guest Codex version' >&2; exit 1 ;;
esac
guest /run/current-system/sw/bin/python3 --version >/dev/null
inc list "p-$uuid" --format json | jq -e --arg name "p-$uuid" \
  'length==1 and .[0].name==$name and
   .[0].expanded_config["security.nesting"]!="true" and
   (.[0].expanded_devices|to_entries|all(.value.type!="nic"))' >/dev/null

# A dummy file stands in for future per-session authentication. No login,
# credential import, model request, or host Codex invocation occurs here.
printf '{"dummy":"credential sentinel"}\n' > "$step_dir/auth.json"
inc file push --uid 1000 --gid 1000 --mode 0600 "$step_dir/auth.json" "p-$uuid/home/p/.codex/auth.json"
guest /usr/libexec/p/codex-adapter init
inc file pull "p-$uuid/home/p/.codex/auth.json" "$step_dir/auth-after.json"
cmp "$step_dir/auth.json" "$step_dir/auth-after.json"
inc file pull "p-$uuid/home/p/.codex/hooks.json" "$step_dir/hooks.json"
inc file pull "p-$uuid/home/p/.codex/config.toml" "$step_dir/config.toml"
jq -e '.hooks | has("UserPromptSubmit") and has("PreToolUse") and has("PermissionRequest") and (has("Stop")|not)' "$step_dir/hooks.json" >/dev/null
grep -q '^cli_auth_credentials_store = "file"$' "$step_dir/config.toml"
grep -q '^notify = \["/usr/libexec/p/codex-adapter", "notify"\]$' "$step_dir/config.toml"

# A second selected private root must not inherit A's session-local dummy
# credential file. Both roots still use the same host-selected asset policy.
created_b=$(rpc project.create '{"v":1,"key":"codex-bootstrap-b","project":"codex-adapter-b"}')
op_b=$(jq -er '.result.operation.id | select(length==36)' <<< "$created_b")
uuid_b=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created_b")
test "$uuid_b" != "$uuid"
deadline=$((SECONDS+150))
while ((SECONDS < deadline)); do
  result_b=$(rpc operation.inspect "$(jq -nc --arg id "$op_b" '{v:1,id:$id}')")
  status_b=$(jq -er '.result.operation.status' <<< "$result_b")
  if test "$status_b" = completed; then break; fi
  if test "$status_b" = blocked; then echo "Codex B creation blocked: $result_b" >&2; exit 1; fi
  sleep 0.4
done
test "$status_b" = completed
inc exec "p-$uuid_b" --user 1000 --group 1000 --cwd /workspace --env HOME=/home/p -- \
  /run/current-system/sw/bin/test ! -e /home/p/.codex
inc exec "p-$uuid_b" --user 1000 --group 1000 --cwd /workspace --env HOME=/home/p -- \
  /usr/libexec/p/codex-adapter init
test "$(inc exec "p-$uuid_b" -- /run/current-system/sw/bin/stat -c '%u:%g:%a' /home/p/.codex)" = 1000:1000:700
inc exec "p-$uuid_b" --user 1000 --group 1000 --cwd /workspace --env HOME=/home/p -- \
  /run/current-system/sw/bin/test ! -e /home/p/.codex/tmp/arg0
inc exec "p-$uuid_b" --user 1000 --group 1000 --cwd /workspace --env HOME=/home/p -- \
  /run/current-system/sw/bin/test ! -e /home/p/.codex/auth.json
inc file pull "p-$uuid/home/p/.codex/auth.json" "$step_dir/auth-after-b.json"
cmp "$step_dir/auth.json" "$step_dir/auth-after-b.json"

thread='b5f6c1c2-1111-2222-3333-444455556666'
child='c5f6c1c2-1111-2222-3333-444455556666'
hook_event() {
  local event="$1" file="$2" child_id="${3-}"
  jq -n --arg event "$event" --arg session "$thread" --arg child "$child_id" \
    '{session_id:$session,turn_id:"turn-1",cwd:"/workspace",transcript_path:null,
      hook_event_name:$event,model:"fixture",permission_mode:"default"} |
     if $event=="UserPromptSubmit" then .prompt="PRIVATE DUMMY PROMPT" else
       .tool_name="Bash" | .tool_input=["PRIVATE DUMMY TOOL INPUT"] |
       if $event=="PreToolUse" then .tool_use_id="tool-1" else . end end |
     if $child!="" then .agent_id=$child | .agent_type="worker" else . end' > "$file"
}
emit_hook() {
  local file="$1"
  inc file push --uid 1000 --gid 1000 --mode 0600 "$file" "p-$uuid/home/p/event.json"
  guest /run/current-system/sw/bin/bash -c '/usr/libexec/p/codex-adapter hook < /home/p/event.json'
}
assert_latest() {
  local condition="$1" source="$2" reason="$3" deadline=$((SECONDS+10))
  while ((SECONDS < deadline)); do
    if inspect | jq -e --arg condition "$condition" --arg source "$source" --arg reason "$reason" \
      '.result.session.latest_unattended_condition |
       .condition==$condition and .source==$source and .reason==$reason and
       .adapter=="codex" and .adapter_version=="0.151.0"' >/dev/null; then return; fi
    sleep 0.2
  done
  echo "Codex status not observed: $condition $source" >&2
  return 1
}
hook_event UserPromptSubmit "$step_dir/event.json"
emit_hook "$step_dir/event.json"
assert_latest running "codex/session/$thread" 'prompt submitted'
hook_event PreToolUse "$step_dir/event.json" "$child"
emit_hook "$step_dir/event.json"
assert_latest running "codex/thread/$child" 'tool starting'
notify=$(jq -nc --arg child "$child" \
  '{type:"agent-turn-complete","thread-id":$child,"turn-id":"turn-1",cwd:"/workspace",
    "input-messages":["PRIVATE DUMMY PROMPT"],"last-assistant-message":"PRIVATE DUMMY ANSWER"}')
guest /usr/libexec/p/codex-adapter notify "$notify"
assert_latest idle "codex/thread/$child" 'turn complete'
hook_event PermissionRequest "$step_dir/event.json"
emit_hook "$step_dir/event.json"
assert_latest attention "codex/session/$thread" 'permission requested'
if inspect | grep -q 'PRIVATE DUMMY'; then echo 'raw Codex payload escaped to public status' >&2; exit 1; fi
prior_sequence=$(inspect | jq -er '.result.session.latest_unattended_condition.receive_sequence')
printf '{"session_id":"%s","turn_id":"turn-1","cwd":"/workspace","transcript_path":null,"hook_event_name":"PermissionRequest","hook_event_name":"PermissionRequest","model":"fixture","permission_mode":"default","tool_name":"Bash","tool_input":{}}\n' "$thread" > "$step_dir/event.json"
emit_hook "$step_dir/event.json"
assert_latest attention "codex/session/$thread" 'permission requested'
test "$(inspect | jq -er '.result.session.latest_unattended_condition.receive_sequence')" = "$prior_sequence"
hook_event Stop "$step_dir/event.json"
emit_hook "$step_dir/event.json"
test "$(inspect | jq -er '.result.session.latest_unattended_condition.receive_sequence')" = "$prior_sequence"

rpc session.stop "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
  jq -e '.result.session.session_condition=="stopped"' >/dev/null
rpc session.start "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" >/dev/null
deadline=$((SECONDS+80))
while ((SECONDS < deadline)); do
  if inspect | jq -e '.result.session.session_condition=="ready"' >/dev/null; then break; fi
  sleep 0.4
done
inspect | jq -e '.result.session.session_condition=="ready"' >/dev/null
inc file pull "p-$uuid/home/p/.codex/auth.json" "$step_dir/auth-restarted.json"
cmp "$step_dir/auth.json" "$step_dir/auth-restarted.json"
guest /usr/libexec/p/codex-adapter init
inc file pull "p-$uuid/home/p/.codex/hooks.json" "$step_dir/hooks-restarted.json"
cmp "$step_dir/hooks.json" "$step_dir/hooks-restarted.json"
inc file pull "p-$uuid/home/p/.codex/config.toml" "$step_dir/config-restarted.toml"
cmp "$step_dir/config.toml" "$step_dir/config-restarted.toml"
printf 'P_CODEX_ADAPTER_PASS\n'
