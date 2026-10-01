# shellcheck shell=bash
# Real configured daemon, selected WASI packages, two isolated Incus sessions,
# and private RPC sent by a test-only client running inside each guest.
set -E
umask 077
step_dir="$P_TEST_TMP/step-11"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step11
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
daemon_pid=
sessions=()

inc() { timeout 45 incus --force-local --project user-1000 "$@"; }
rpc() { timeout 30 p api "$socket" "$@"; }
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
  for uuid in "${sessions[@]}"; do
    inc delete --force "p-$uuid" >/dev/null 2>&1 || true
    rm -rf -- "${endpoint_prefix:?}/${uuid:?}"
  done
  rmdir "$endpoint_prefix" 2>/dev/null || true
}
on_error() {
  printf 'P_SESSION_STATUS_FAIL line=%s status=%s\n' "$2" "$1" >&2
  for log in daemon.err guest.err; do
    if test -f "$step_dir/$log"; then tail -c 2048 "$step_dir/$log" >&2 || true; fi
  done
  for uuid in "${sessions[@]}"; do
    inc info "p-$uuid" --show-log 2>&1 | tail -c 1024 >&2 || true
  done
}
trap cleanup EXIT
trap 'on_error "$?" "$LINENO"' ERR

activate() {
  local package="$1" target="$2" grant="$3" digest id
  digest=$(p plugins conformance "$package" | jq -er '.sha256 | select(type=="string" and length==64)')
  id=$(jq -er '.id | select(type=="string" and length>0)' "$package/plugin.json")
  jq -n --arg path "$package" --arg digest "$digest" --arg id "$id" --arg grant "$grant" \
    '{schema:"p.activation/v1",plugins:[{id:$id,path:$path,sha256:$digest,grants:[$grant],config:{}}]}' > "$target"
}
activate "$P_TEST_GIT_PLUGIN" "$step_dir/git.json" git.project
activate "$P_TEST_RUNTIME_PLUGIN" "$step_dir/runtime-one.json" runtime.incus
activate "$P_TEST_SOURCE/plugins/bundled/tmux-host" "$step_dir/host-one.json" session.asset.install
jq -s '{schema:"p.activation/v1",plugins:([.[0].plugins[0],.[1].plugins[0]])}' \
  "$step_dir/runtime-one.json" "$step_dir/host-one.json" > "$step_dir/runtime.json"
p plugins activate "$step_dir/git.json" | jq -e '.[0].capability=="source-git"' >/dev/null
p plugins activate "$step_dir/runtime.json" | jq -e 'length==2' >/dev/null
fingerprint=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
test "${#fingerprint}" -eq 64
runtime_id=$(jq -er '.plugins[0].id' "$step_dir/runtime.json")
host_id=$(jq -er '.plugins[1].id' "$step_dir/runtime.json")
git_id=$(jq -er '.plugins[0].id' "$step_dir/git.json")
incus_binary=$(command -v incus)
jq -n --arg state "$state" --arg git_activation "$step_dir/git.json" \
  --arg git_id "$git_id" --arg runtime_activation "$step_dir/runtime.json" \
  --arg runtime_id "$runtime_id" --arg host_id "$host_id" \
  --arg incus_binary "$incus_binary" --arg prefix "$endpoint_prefix" \
  --arg image "$fingerprint" \
  '{schema:"p.host/v1",state_dir:$state,
   git:{activation_path:$git_activation,source_plugin_id:$git_id,listen:"127.0.0.1:0"},
   runtime:{activation_path:$runtime_activation,runtime_plugin_id:$runtime_id,
    host_plugin_id:$host_id,incus_binary:$incus_binary,
    incus_user_socket:"/var/lib/incus/unix.socket.user",incus_project:"user-1000",
    endpoint_prefix:$prefix,disk_source_ceilings:["/var/lib/p-vm/endpoints","/var/lib/p-vm/grants"],
    base_image_fingerprint:$image,
    project_policy:{network:"none",filesystem_mounts:[],command:["/run/current-system/sw/bin/bash"]}}}' \
  > "$step_dir/host.json"
bash "$P_TEST_SOURCE/tests/integration/public-egress-host-config.sh" "$step_dir/host.json"

start_daemon() {
  : > "$step_dir/daemon.err"
  p daemon "$step_dir/host.json" > "$step_dir/daemon.out" 2> "$step_dir/daemon.err" &
  daemon_pid=$!
  local deadline=$((SECONDS+30))
  while ((SECONDS < deadline)); do
    if test -S "$socket" && rpc system.health |
      jq -e '.result.control_state=="ready"' >/dev/null 2>&1; then return; fi
    if ! kill -0 "$daemon_pid" 2>/dev/null; then
      echo 'configured daemon exited before readiness' >&2
      return 1
    fi
    sleep 0.1
  done
  echo 'configured daemon did not become ready' >&2
  return 1
}
inspect() { rpc session.inspect "$(jq -nc --arg uuid "$1" '{v:1,uuid:$uuid}')"; }
wait_operation() {
  local id="$1" result status deadline=$((SECONDS+160))
  while ((SECONDS < deadline)); do
    result=$(rpc operation.inspect "$(jq -nc --arg id "$id" '{v:1,id:$id}')")
    status=$(jq -er '.result.operation.status' <<< "$result")
    if test "$status" = completed; then return; fi
    if test "$status" = blocked; then echo "operation blocked: $result" >&2; return 1; fi
    sleep 0.4
  done
  echo "operation did not complete: $id" >&2
  return 1
}
wait_ready() {
  local uuid="$1" result condition last='no observation' deadline=$((SECONDS+80))
  while ((SECONDS < deadline)); do
    if result=$(inspect "$uuid" 2> "$step_dir/inspect.err"); then
      last="$result"
      condition=$(jq -er '.result.session.session_condition' <<< "$result")
      if test "$condition" = ready; then return; fi
      if test "$condition" = stopped; then echo "session stopped before readiness: $result" >&2; return 1; fi
    fi
    sleep 0.4
  done
  echo "session did not become ready: $uuid $last" >&2
  return 1
}
create_project() {
  local key="$1" project="$2" result op uuid
  result=$(rpc project.create "$(jq -nc --arg key "$key" --arg project "$project" '{v:1,key:$key,project:$project}')")
  op=$(jq -er '.result.operation.id' <<< "$result")
  uuid=$(jq -er '.result.operation.session_uuid | select(type=="string" and length==36)' <<< "$result")
  sessions+=("$uuid")
  wait_operation "$op"
  wait_ready "$uuid"
  created_uuid="$uuid"
}

# The image deliberately has no general-purpose Unix socket client. The
# statically linked fixture runs as p in the guest and dials /run/p/session.sock.
guest_encoded() {
  local uuid="$1" encoded="$2" mode="${3-}"
  local args=()
  if test -n "$mode"; then args+=("$mode"); fi
  inc exec "p-$uuid" --user 1000 --group 1000 --cwd /workspace \
    --env HOME=/home/p -- /home/p/session-fixture /run/p/session.sock "$encoded" "${args[@]}"
}
guest_line() {
  local uuid="$1" line="$2"
  guest_encoded "$uuid" "$(printf '%s\n' "$line" | base64 -w0)"
}
guest_unterminated() {
  local uuid="$1" line="$2" mode="${3-}"
  guest_encoded "$uuid" "$(printf '%s' "$line" | base64 -w0)" "$mode"
}
guest_invalid_utf8() {
  local uuid="$1"
  guest_encoded "$uuid" "$( { printf '%s' '{"jsonrpc":"2.0","method":"status.report","params":{"v":1,"source":"';
    printf '\377'; printf '%s\n' '","condition":"idle","adapter":"codex","adapter_version":"1"}}'; } | base64 -w0)"
}
query() {
  local uuid="$1" id="$2" method="$3" params="${4-}"
  if test -z "$params"; then params='{"v":1}'; fi
  guest_line "$uuid" "$(jq -nc --arg id "$id" --arg method "$method" --argjson params "$params" \
    '{jsonrpc:"2.0",id:$id,method:$method,params:$params}')"
}
report() {
  local uuid="$1" condition="$2" reason="$3" source="${4:-codex/main}" response
  response=$(guest_line "$uuid" "$(jq -nc --arg condition "$condition" --arg reason "$reason" --arg source "$source" \
    '{jsonrpc:"2.0",method:"status.report",params:{v:1,source:$source,condition:$condition,
      reason:$reason,adapter:"codex",adapter_version:"1"}}')")
  test -z "$response" || { echo "notification received a response: $response" >&2; return 1; }
}
latest() { inspect "$1" | jq -c '.result.session.latest_unattended_condition'; }
assert_latest() {
  local uuid="$1" condition="$2" reason="$3" result
  result=$(inspect "$uuid")
  jq -e --arg uuid "$uuid" --arg condition "$condition" --arg reason "$reason" \
    '.result.session | .uuid==$uuid and .session_condition=="ready" and
     .policy_condition=="current" and .attached_count==0 and
     .latest_unattended_condition.condition==$condition and
     .latest_unattended_condition.reason==$reason and
     .latest_unattended_condition.adapter=="codex" and
     .latest_unattended_condition.adapter_version=="1" and
     (.latest_unattended_condition.receive_sequence|type=="number" and .>0) and
     (.latest_unattended_condition.received_at|type=="string" and length>0)' \
    <<< "$result" >/dev/null
}
assert_preserved() {
  local uuid="$1" expected="$2" actual
  actual=$(latest "$uuid")
  test "$actual" = "$expected" || { echo "status changed after invalid report: $actual" >&2; return 1; }
}

start_daemon
instance_id=$(rpc system.hello | jq -er '.result.instance_id')
create_project status-a status-a
uuid_a="$created_uuid"
create_project status-b status-b
uuid_b="$created_uuid"
test "$uuid_a" != "$uuid_b"
for uuid in "$uuid_a" "$uuid_b"; do
  inc file push --uid 1000 --gid 1000 --mode 0755 \
    "$(command -v session-fixture)" "p-$uuid/home/p/session-fixture"
  inc exec "p-$uuid" --user 1000 --group 1000 -- sh -c 'test -S /run/p/session.sock'
  test -S "$endpoint_prefix/$uuid/session.sock"
  jq -e --arg uuid "$uuid" '.result.v==1 and .result.uuid==$uuid and .result.branch=="main"' \
    <<< "$(query "$uuid" own session.identity)" >/dev/null
  jq -e --arg uuid "$uuid" \
    '.result.v==1 and .result.uuid==$uuid and .result.branch=="main" and
     (.result.policy_sha256|type=="string" and length==64) and
     .result.effective_capabilities=={network:"none",filesystem_mounts:[],source_git:true,status_report:true}' \
    <<< "$(query "$uuid" caps session.capabilities)" >/dev/null
  jq -e '.result.session | .session_condition=="ready" and .attached_count==0 and
    .latest_unattended_condition==null and .policy_condition=="current"' \
    <<< "$(inspect "$uuid")" >/dev/null
done

# An RPC caller cannot choose another UUID or escape to host methods.
for method in session.inspect session.list session.start system.inspect project.branches; do
  jq -e --arg id "$method" '.id==$id and .error.kind=="method_not_found"' \
    <<< "$(query "$uuid_a" "$method" "$method")" >/dev/null
done
jq -e '.error.kind=="invalid_params"' \
  <<< "$(query "$uuid_a" cross session.identity "$(jq -nc --arg uuid "$uuid_b" '{v:1,uuid:$uuid}')")" >/dev/null
jq -e '.error.kind=="invalid_params"' \
  <<< "$(query "$uuid_b" cross session.capabilities "$(jq -nc --arg uuid "$uuid_a" '{v:1,uuid:$uuid}')")" >/dev/null

report "$uuid_a" attention 'permission requested'
assert_latest "$uuid_a" attention 'permission requested'
first_a=$(latest "$uuid_a")
test "$(latest "$uuid_b")" = null
report "$uuid_b" idle 'second guest'
assert_latest "$uuid_b" idle 'second guest'
first_b=$(latest "$uuid_b")
report "$uuid_a" running 'work resumed'
assert_latest "$uuid_a" running 'work resumed'
second_a=$(latest "$uuid_a")
test "$(jq -r '.receive_sequence' <<< "$second_a")" -gt "$(jq -r '.receive_sequence' <<< "$first_a")"
test "$(jq -r '.receive_sequence' <<< "$first_b")" -gt "$(jq -r '.receive_sequence' <<< "$first_a")"
assert_preserved "$uuid_b" "$first_b"
rpc session.list '{"v":1,"limit":8}' | jq -e --arg a "$uuid_a" --arg b "$uuid_b" \
  --argjson latest_a "$second_a" --argjson latest_b "$first_b" \
  '.result.sessions | any(.[]; .uuid==$a and .attached_count==0 and
    .session_condition=="ready" and .policy_condition=="current" and
    .latest_unattended_condition==$latest_a) and
   any(.[]; .uuid==$b and .attached_count==0 and .latest_unattended_condition==$latest_b)' >/dev/null
page1=$(rpc session.list '{"v":1,"limit":1}')
cursor=$(jq -er '.result.next | select(type=="string" and length==36)' <<< "$page1")
page2=$(rpc session.list "$(jq -nc --arg after "$cursor" '{v:1,limit:1,after:$after}')")
jq -n --arg a "$uuid_a" --arg b "$uuid_b" --argjson first "$page1" --argjson second "$page2" \
  --argjson latest_a "$second_a" --argjson latest_b "$first_b" \
  '($first.result.sessions + $second.result.sessions) as $sessions |
   ($sessions | length)==2 and ($second.result.next=="") and
   (($sessions | map(.uuid) | sort)==([$a,$b] | sort)) and
   all($sessions[]; .attached_count==0 and .session_condition=="ready" and
     .policy_condition=="current" and
     (if .uuid==$a then .latest_unattended_condition==$latest_a
      else .latest_unattended_condition==$latest_b end))' | jq -e '. == true' >/dev/null

# Each invalid report is followed by a query on the same session's bound
# endpoint. The one-shot client closes after processing before the host reads
# the durable row.
bad_reports=(
  '{"jsonrpc":"2.0","method":"status.report","params":{"v":2,"source":"codex/main","condition":"idle","adapter":"codex","adapter_version":"1"}}'
  '{"jsonrpc":"2.0","method":"status.report","params":{"v":1,"source":"codex/main","condition":"completed","adapter":"codex","adapter_version":"1"}}'
  "$(jq -nc --arg uuid "$uuid_b" '{jsonrpc:"2.0",method:"status.report",params:{v:1,uuid:$uuid,source:"codex/main",condition:"idle",adapter:"codex",adapter_version:"1"}}')"
  '{"jsonrpc":"2.0","method":"status.report","params":{"v":1,"source":"codex/main","condition":"idle","adapter":"codex","adapter":"other","adapter_version":"1"}}'
)
for bad in "${bad_reports[@]}"; do
  response=$(guest_line "$uuid_a" "$bad")
  test -z "$response" || { echo "invalid notification received a response: $response" >&2; exit 1; }
  barrier=$(query "$uuid_a" barrier session.identity)
  jq -e --arg uuid "$uuid_a" '.result.uuid==$uuid' <<< "$barrier" >/dev/null
  assert_preserved "$uuid_a" "$second_a"
  assert_preserved "$uuid_b" "$first_b"
done
response=$(guest_line "$uuid_a" '{"jsonrpc":"2.0","id":19,"method":"status.report","params":{"v":1,"source":"codex/main","condition":"idle","adapter":"codex","adapter_version":"1"}}')
jq -e '.id==19 and .error.kind=="invalid_request" and .error.code == -32600' <<< "$response" >/dev/null
assert_preserved "$uuid_a" "$second_a"
assert_preserved "$uuid_b" "$first_b"
long_reason=$(printf '%0257d' 0)
bad=$(jq -nc --arg reason "$long_reason" '{jsonrpc:"2.0",method:"status.report",params:{v:1,source:"codex/main",condition:"idle",reason:$reason,adapter:"codex",adapter_version:"1"}}')
response=$(guest_line "$uuid_a" "$bad")
test -z "$response"
assert_preserved "$uuid_a" "$second_a"
guest_invalid_utf8 "$uuid_a" | jq -e '.error.kind=="parse_error"' >/dev/null
assert_preserved "$uuid_a" "$second_a"
guest_unterminated "$uuid_a" '{"jsonrpc":"2.0","method":"status.report"' |
  jq -e '.error.kind=="parse_error"' >/dev/null
assert_preserved "$uuid_a" "$second_a"
oversize=$(printf '%066000d' 0)
guest_unterminated "$uuid_a" "$oversize" expect-parse-error |
  jq -e '.jsonrpc=="2.0" and .id==null and .error.code == -32700 and
    .error.kind=="parse_error"' >/dev/null
assert_preserved "$uuid_a" "$second_a"

# A paced burst stays below the 100-attempts/second connection limit while
# exceeding the 240-valid-reports/minute semantic budget. Restart first to
# begin a fresh in-memory rate window without losing either durable value.
stop_daemon
start_daemon
assert_preserved "$uuid_a" "$second_a"
assert_preserved "$uuid_b" "$first_b"
before_seq=$(jq -r '.receive_sequence' <<< "$(latest "$uuid_a")")
burst=$(jq -nc '{jsonrpc:"2.0",method:"status.report",params:{v:1,source:"burst",condition:"unknown",adapter:"codex",adapter_version:"1"}}')
burst_input=$(for ((i=0; i<300; i++)); do printf '%s\n' "$burst"; done | base64 -w0)
guest_encoded "$uuid_a" "$burst_input" pace-75ms > "$step_dir/burst.out"
test ! -s "$step_dir/burst.out"
after_seq=$(latest "$uuid_a" | jq -r '.receive_sequence')
test "$after_seq" -gt "$before_seq"
test "$after_seq" -lt "$((before_seq+300))"
latest "$uuid_a" | jq -e '.condition=="unknown" and .source=="burst"' >/dev/null
query "$uuid_a" after-burst session.identity | jq -e --arg uuid "$uuid_a" '.result.uuid==$uuid' >/dev/null
assert_preserved "$uuid_b" "$first_b"

# The latest value and its receive metadata survive both daemon shutdown paths
# and an ordinary Stop/Start of the owning guest.
stable_a=$(latest "$uuid_a")
stable_b=$(latest "$uuid_b")
stop_daemon
test ! -e "$socket"
start_daemon
test "$(rpc system.hello | jq -er '.result.instance_id')" = "$instance_id"
assert_preserved "$uuid_a" "$stable_a"
assert_preserved "$uuid_b" "$stable_b"
query "$uuid_a" after-graceful session.identity | jq -e --arg uuid "$uuid_a" '.result.uuid==$uuid' >/dev/null
kill -KILL "$daemon_pid"
wait "$daemon_pid" 2>/dev/null || true
daemon_pid=
start_daemon
assert_preserved "$uuid_a" "$stable_a"
assert_preserved "$uuid_b" "$stable_b"
query "$uuid_b" after-kill session.identity | jq -e --arg uuid "$uuid_b" '.result.uuid==$uuid' >/dev/null
rpc session.stop "$(jq -nc --arg uuid "$uuid_a" '{v:1,uuid:$uuid}')" |
  jq -e '.result.session.session_condition=="stopped"' >/dev/null
assert_preserved "$uuid_a" "$stable_a"
rpc session.start "$(jq -nc --arg uuid "$uuid_a" '{v:1,uuid:$uuid}')" |
  jq -e '.result.session.session_condition=="starting"' >/dev/null
wait_ready "$uuid_a"
assert_preserved "$uuid_a" "$stable_a"
assert_preserved "$uuid_b" "$stable_b"
query "$uuid_a" after-start session.identity | jq -e --arg uuid "$uuid_a" '.result.uuid==$uuid' >/dev/null
stop_daemon
echo P_SESSION_STATUS_PASS
