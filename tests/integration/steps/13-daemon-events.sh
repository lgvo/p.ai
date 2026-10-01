# shellcheck shell=bash
# Real configured daemon, selected host event handler, Incus session RPC,
# and the ordinary trusted attachment helper in the disposable product VM.
set -E
umask 077
step_dir="$P_TEST_TMP/step-13"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
private="$step_dir/private"
mkdir -m 0700 "$state" "$private"
socket="$state/control.sock"
log_file="$private/events.ndjson"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step13
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
daemon_pid=
attach_pid=
uuid=

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
  exec 9>&- 2>/dev/null || true
  if test -n "$attach_pid"; then
    kill -TERM "$attach_pid" 2>/dev/null || true
    wait "$attach_pid" 2>/dev/null || true
  fi
  stop_daemon
  if test -n "$uuid"; then
    inc delete --force "p-$uuid" >/dev/null 2>&1 || true
    rm -rf -- "${endpoint_prefix:?}/${uuid:?}"
  fi
  rmdir "$endpoint_prefix" 2>/dev/null || true
}
on_error() {
  printf 'P_DAEMON_EVENTS_FAIL line=%s status=%s\n' "$2" "$1" >&2
  for log in daemon.err attach.err; do
    if test -f "$step_dir/$log"; then
      printf '%s (last 2048 bytes, redacted):\n' "$log" >&2
      tail -c 2048 "$step_dir/$log" |
        LC_ALL=C sed -E 's/[[:xdigit:]]{64}/[redacted]/g; s/REDUCT_SECRET|credential-marker|diagnostic-marker|raw-rpc-marker/[redacted]/g' >&2 || true
    fi
  done
  if test -f "$step_dir/attach.out"; then
    printf 'attach.out bytes=%s\n' "$(wc -c < "$step_dir/attach.out")" >&2
    if grep -Eq 'invalid private attachment initiation|invalid terminal size' "$step_dir/attach.out"; then
      printf 'attach.out contains attachment initiation or terminal size diagnostic\n' >&2
    fi
  fi
}
on_exit() {
  local status=$?
  trap - EXIT ERR
  if test "$status" -ne 0; then on_error "$status" "${error_line:-exit}"; fi
  cleanup
}
trap on_exit EXIT
trap 'error_line=$LINENO' ERR

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
event_package="$step_dir/file-log"
cp -R "$P_TEST_SOURCE/plugins/bundled/file-log" "$event_package"
chmod -R u+w "$event_package"
cp "$event_package/plugin.json" "$step_dir/pristine-plugin.json"
event_digest=$(p plugins conformance "$event_package" | jq -er '.sha256 | select(type=="string" and length==64)')
jq -n --arg path "$event_package" --arg digest "$event_digest" --arg log "$log_file" \
  '{schema:"p.activation/v1",plugins:[{id:"org.p.filelog",path:$path,sha256:$digest,
   grants:["event.file.append"],config:{path:$log,max_bytes:1048576}}]}' > "$step_dir/events.json"
p plugins activate "$step_dir/events.json" | jq -e '.[0].capability=="event-handler"' >/dev/null
fingerprint=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
test "${#fingerprint}" -eq 64
runtime_id=$(jq -er '.plugins[0].id' "$step_dir/runtime.json")
host_id=$(jq -er '.plugins[1].id' "$step_dir/runtime.json")
git_id=$(jq -er '.plugins[0].id' "$step_dir/git.json")
incus_binary=$(command -v incus)
jq -n --arg state "$state" --arg git_activation "$step_dir/git.json" \
  --arg git_id "$git_id" --arg runtime_activation "$step_dir/runtime.json" \
  --arg runtime_id "$runtime_id" --arg host_id "$host_id" \
  --arg event_activation "$step_dir/events.json" --arg incus_binary "$incus_binary" \
  --arg prefix "$endpoint_prefix" --arg image "$fingerprint" \
  '{schema:"p.host/v1",state_dir:$state,
   git:{activation_path:$git_activation,source_plugin_id:$git_id,listen:"127.0.0.1:0"},
   runtime:{activation_path:$runtime_activation,runtime_plugin_id:$runtime_id,
    host_plugin_id:$host_id,incus_binary:$incus_binary,
    incus_user_socket:"/var/lib/incus/unix.socket.user",incus_project:"user-1000",
    endpoint_prefix:$prefix,disk_source_ceilings:["/var/lib/p-vm/endpoints","/var/lib/p-vm/grants"],
    base_image_fingerprint:$image,
    project_policy:{network:"none",filesystem_mounts:[],command:["/run/current-system/sw/bin/bash"]}},
   events:{activation_path:$event_activation,plugin_id:"org.p.filelog"}}' > "$step_dir/host.json"
bash "$P_TEST_SOURCE/tests/integration/public-egress-host-config.sh" "$step_dir/host.json"

start_daemon() {
  : > "$step_dir/daemon.err"
  p daemon "$step_dir/host.json" > "$step_dir/daemon.out" 2> "$step_dir/daemon.err" &
  daemon_pid=$!
  local deadline=$((SECONDS+30))
  while ((SECONDS < deadline)); do
    if test -S "$socket" && rpc system.health |
      jq -e '.result.control_state=="ready"' >/dev/null 2>&1; then return; fi
    if ! kill -0 "$daemon_pid" 2>/dev/null; then echo 'event daemon exited before readiness' >&2; return 1; fi
    sleep 0.1
  done
  echo 'event daemon did not become ready' >&2
  return 1
}
inspect() { rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')"; }
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
wait_condition() {
  local expected="$1" result condition deadline=$((SECONDS+80))
  while ((SECONDS < deadline)); do
    result=$(inspect)
    condition=$(jq -er '.result.session.session_condition' <<< "$result")
    if test "$condition" = "$expected"; then return; fi
    sleep 0.4
  done
  echo "session did not reach $expected: $result" >&2
  return 1
}
wait_event() {
  local kind="$1" field="$2" value="$3" deadline=$((SECONDS+30))
  while ((SECONDS < deadline)); do
    if test -f "$log_file" && jq -se --arg kind "$kind" --arg field "$field" --arg value "$value" \
      'any(.[]; .kind==$kind and .fields[$field]==$value)' "$log_file" >/dev/null 2>&1; then return; fi
    sleep 0.1
  done
  echo "missing event: $kind $field=$value" >&2
  return 1
}
event_count() { if test -f "$log_file"; then wc -l < "$log_file"; else echo 0; fi; }
guest_report() {
  local condition="$1" reason="$2" response
  response=$(inc exec "p-$uuid" --user 1000 --group 1000 --cwd /workspace \
    --env HOME=/home/p -- /home/p/session-fixture /run/p/session.sock \
    "$(jq -nc --arg condition "$condition" --arg reason "$reason" \
      '{jsonrpc:"2.0",method:"status.report",params:{v:1,source:"codex/main",
       condition:$condition,reason:$reason,adapter:"codex",adapter_version:"1"}}' | base64 -w0)")
  test -z "$response"
}
assert_envelopes() {
  local instance="$1" line
  test -s "$log_file"
  test "$(tail -c 1 "$log_file" | od -An -tu1 | tr -d '[:space:]')" = 10
  while IFS= read -r line; do
    test "$(printf '%s' "$line" | wc -c)" -le 4096
    jq -se 'length==1 and (.[0]|type=="object")' <<< "$line" >/dev/null
  done < "$log_file"
  jq -se --arg instance "$instance" --arg uuid "$uuid" '
    length>0 and ([.[].id]|unique|length)==length and all(.[]; . as $e |
      ((keys - ["schema","id","kind","occurred_at","instance","project","session","branch","fields"])|length)==0 and
      (has("schema") and has("id") and has("kind") and has("occurred_at") and has("instance") and has("project") and has("session") and has("fields")) and
      $e.schema=="p.event/v1" and ($e.id|type=="string" and length>0 and length<=256) and
      ($e.occurred_at|type=="string" and length>0 and length<=64 and test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T")) and
      $e.instance==$instance and $e.project=="events-project" and $e.session==$uuid and
      ($e.branch=="main" or ($e.kind=="operation.progress" and $e.fields.phase=="accepted" and ($e|has("branch")|not))) and
      ($e|tojson|length)<=4096 and
      ($e.fields|type=="object" and length==1) and
      (if $e.kind=="operation.progress" then ($e.fields|keys)==["phase"] and (["accepted","running","blocked","completed","failed","cancelled"]|index($e.fields.phase))!=null
       elif $e.kind=="session.condition_changed" then ($e.fields|keys)==["condition"] and (["creating","starting","ready","stopped","missing","unreachable","discarding","deleting"]|index($e.fields.condition))!=null
       elif $e.kind=="session.policy_changed" then ($e.fields|keys)==["policy_condition"] and (["current","outdated","invalid"]|index($e.fields.policy_condition))!=null
       elif $e.kind=="session.attachment_changed" then ($e.fields|keys)==["count"] and ($e.fields.count|test("^(0|[1-9][0-9]*)$") and (tonumber<=1000000))
       elif $e.kind=="session.unattended_changed" then ($e.fields|keys)==["unattended_condition"] and (["none","running","attention","idle","failed","unknown"]|index($e.fields.unattended_condition))!=null
       else false end))' "$log_file" >/dev/null
  test "$(stat -c '%a' "$log_file")" = 600
  test "$(stat -c '%h' "$log_file")" = 1
  test "$(wc -c < "$log_file")" -le 1048576
  if grep -Eq 'REDUCT_SECRET|credential-marker|diagnostic-marker|status\.report|session\.identity|raw-rpc-marker' "$log_file"; then
    echo 'raw or secret input reached event log' >&2
    return 1
  fi
}

start_daemon
instance_id=$(rpc system.hello | jq -er '.result.instance_id')
result=$(rpc project.create '{"v":1,"key":"events-project","project":"events-project"}')
op=$(jq -er '.result.operation.id' <<< "$result")
uuid=$(jq -er '.result.operation.session_uuid | select(type=="string" and length==36)' <<< "$result")
wait_operation "$op"
wait_condition ready
original_policy_sha=$(inspect | jq -er '.result.session.policy_sha256 | select(type=="string" and length==64)')
wait_event operation.progress phase completed
wait_event session.condition_changed condition ready
inc file push --uid 1000 --gid 1000 --mode 0755 \
  "$(command -v session-fixture)" "p-$uuid/home/p/session-fixture"
test -S "$endpoint_prefix/$uuid/session.sock"

# Repository content can name a different handler, but daemon selection is
# supplied only by the private host config and activation file above.
# shellcheck disable=SC2016 # $1 is expanded by the guest shell.
inc exec "p-$uuid" --user 1000 --group 1000 --cwd /workspace -- \
  sh -c 'printf "%s\n" "$1" > /workspace/p.host.json' sh \
  '{"schema":"p.host/v1","events":{"plugin_id":"org.p.unselected"}}'

# A report's arbitrary reason is reduced before delivery. The first live
# attachment emits count=1 and a subsequent clear, in reducer commit order.
guest_report attention 'REDUCT_SECRET credential-marker diagnostic-marker raw-rpc-marker'
wait_event session.unattended_changed unattended_condition attention
jq -e '.result.session.latest_unattended_condition.condition=="attention"' <<< "$(inspect)" >/dev/null
mkfifo "$step_dir/attach.input"
cat "$step_dir/attach.input" | timeout 35 script -q -e \
  -c "stty rows 24 cols 80; exec p attach $socket $uuid" "$step_dir/attach.log" > "$step_dir/attach.out" 2> "$step_dir/attach.err" &
attach_pid=$!
exec 9> "$step_dir/attach.input"
deadline=$((SECONDS+25))
while ((SECONDS < deadline)); do
  if inspect | jq -e '.result.session.attached_count==1 and .result.session.latest_unattended_condition==null' >/dev/null 2>&1; then break; fi
  if ! kill -0 "$attach_pid" 2>/dev/null; then echo 'attachment ended before confirmation' >&2; exit 1; fi
  sleep 0.1
done
inspect | jq -e '.result.session.attached_count==1 and .result.session.latest_unattended_condition==null' >/dev/null
wait_event session.attachment_changed count 1
wait_event session.unattended_changed unattended_condition none
before_attached_report=$(event_count)
guest_report idle 'REDUCT_SECRET while-attached'
test "$(event_count)" -eq "$before_attached_report"
inspect | jq -e '.result.session.latest_unattended_condition==null' >/dev/null
printf '\002d' >&9
exec 9>&-
wait "$attach_pid"
attach_pid=
deadline=$((SECONDS+25))
while ((SECONDS < deadline)); do
  if inspect | jq -e '.result.session.attached_count==0' >/dev/null 2>&1; then break; fi
  sleep 0.1
done
inspect | jq -e '.result.session.attached_count==0 and .result.session.latest_unattended_condition==null' >/dev/null
wait_event session.attachment_changed count 0
guest_report running 'REDUCT_SECRET after-release'
wait_event session.unattended_changed unattended_condition running

# The NDJSON sequence itself is the handler call boundary. A single worker
# preserves ordering for these paced reducer commits.
jq -se '
  [to_entries[] | select(.value.kind=="session.unattended_changed" and .value.fields.unattended_condition=="attention") | .key] as $attention |
  [to_entries[] | select(.value.kind=="session.attachment_changed" and .value.fields.count=="1") | .key] as $attach |
  [to_entries[] | select(.value.kind=="session.unattended_changed" and .value.fields.unattended_condition=="none") | .key] as $clear |
  [to_entries[] | select(.value.kind=="session.attachment_changed" and .value.fields.count=="0") | .key] as $release |
  [to_entries[] | select(.value.kind=="session.unattended_changed" and .value.fields.unattended_condition=="running") | .key] as $resume |
  ($attention|length)==1 and ($attach|length)==1 and ($clear|length)==1 and ($release|length)==1 and ($resume|length)==1 and
  $attention[0]<$attach[0] and $attach[0]<$clear[0] and $clear[0]<$release[0] and $release[0]<$resume[0] and
  ([.[] | select(.kind=="session.unattended_changed" and .fields.unattended_condition=="idle")]|length)==0' "$log_file" >/dev/null
assert_envelopes "$instance_id"

# Startup recovers authoritative sessions but does not replay old event rows.
count_before_restart=$(event_count)
stop_daemon
start_daemon
test "$(rpc system.hello | jq -er '.result.instance_id')" = "$instance_id"
inspect | jq -e '.result.session.session_condition=="ready" and .result.session.latest_unattended_condition.condition=="running"' >/dev/null
sleep 3
test "$(event_count)" -eq "$count_before_restart"

# Trusted host policy changes across restart produce a newly reduced event.
stop_daemon
jq '.runtime.project_policy.command=["/run/current-system/sw/bin/sh"]' "$step_dir/host.json" > "$step_dir/host.next.json"
mv "$step_dir/host.next.json" "$step_dir/host.json"
start_daemon
inspect | jq -e --arg sha "$original_policy_sha" \
  '.result.session.policy_condition=="outdated" and .result.session.policy_sha256==$sha' >/dev/null
wait_event session.policy_changed policy_condition outdated
assert_envelopes "$instance_id"

# The original policy restores the session's Start eligibility and records
# the changed comparison after another trusted daemon restart.
stop_daemon
jq '.runtime.project_policy.command=["/run/current-system/sw/bin/bash"]' "$step_dir/host.json" > "$step_dir/host.next.json"
mv "$step_dir/host.next.json" "$step_dir/host.json"
start_daemon
inspect | jq -e --arg sha "$original_policy_sha" \
  '.result.session.policy_condition=="current" and .result.session.policy_sha256==$sha' >/dev/null
wait_event session.policy_changed policy_condition current

# An unsafe final log object makes the broker fail after commit. The daemon
# reports a fixed diagnostic and the normal Stop still reaches authoritative state.
mv "$log_file" "$step_dir/events-before-failure.ndjson"
mkdir -m 0700 "$log_file"
rpc session.stop "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
  jq -e '.result.session.session_condition=="stopped"' >/dev/null
inspect | jq -e '.result.session.session_condition=="stopped"' >/dev/null
deadline=$((SECONDS+10))
while ((SECONDS < deadline)); do
  if grep -Fq 'event handler delivery failed' "$step_dir/daemon.err"; then break; fi
  sleep 0.1
done
grep -Fq 'event handler delivery failed' "$step_dir/daemon.err"
test "$(wc -c < "$step_dir/daemon.err")" -le 4096
if grep -Eq 'REDUCT_SECRET|credential-marker|diagnostic-marker|raw-rpc-marker|events\.ndjson' "$step_dir/daemon.err"; then
  echo 'handler diagnostic leaked event input or private path' >&2
  exit 1
fi
rmdir "$log_file"
mv "$step_dir/events-before-failure.ndjson" "$log_file"

# A package changed after activation is refused on the next daemon event.
count_before_tamper=$(event_count)
printf '\n' >> "$event_package/plugin.json"
rpc session.start "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
  jq -e '.result.session.session_condition=="starting"' >/dev/null
wait_condition ready
sleep 3
test "$(event_count)" -eq "$count_before_tamper"
cp "$step_dir/pristine-plugin.json" "$event_package/plugin.json"

# The selected WASI example runs through the same daemon seam. It writes
# only the ready-condition transition to its separately configured log.
stop_daemon
filter_digest=$(p plugins conformance "$P_TEST_WASI_FILTER" | jq -er '.sha256 | select(type=="string" and length==64)')
filter_log="$private/filter.ndjson"
jq -n --arg path "$P_TEST_WASI_FILTER" --arg digest "$filter_digest" --arg log "$filter_log" \
  '{schema:"p.activation/v1",plugins:[{id:"org.p.example.filterlog",path:$path,sha256:$digest,
    grants:["event.file.append"],config:{path:$log,max_bytes:1048576}}]}' > "$step_dir/filter-events.json"
jq --arg path "$step_dir/filter-events.json" \
  '.events={activation_path:$path,plugin_id:"org.p.example.filterlog"}' \
  "$step_dir/host.json" > "$step_dir/host.next.json"
mv "$step_dir/host.next.json" "$step_dir/host.json"
start_daemon
rpc session.stop "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
  jq -e '.result.session.session_condition=="stopped"' >/dev/null
rpc session.start "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
  jq -e '.result.session.session_condition=="starting"' >/dev/null
wait_condition ready
deadline=$((SECONDS+30))
while ((SECONDS < deadline)); do
  if test -f "$filter_log" && jq -se 'length>0 and all(.[]; .kind=="session.condition_changed" and .fields=={condition:"ready"})' "$filter_log" >/dev/null 2>&1; then break; fi
  sleep 0.1
done
jq -se --arg uuid "$uuid" --arg instance "$instance_id" \
  'length>0 and all(.[]; .schema=="p.event/v1" and .kind=="session.condition_changed" and .fields=={condition:"ready"} and .session==$uuid and .instance==$instance)' "$filter_log" >/dev/null
test "$(stat -c '%a' "$filter_log")" = 600

# Invalid trusted selection is refused before the daemon becomes ready.
stop_daemon
jq '.events.plugin_id="org.p.unselected"' "$step_dir/host.json" > "$step_dir/host.invalid.json"
if timeout 15 p daemon "$step_dir/host.invalid.json" > "$step_dir/invalid.out" 2> "$step_dir/invalid.err"; then
  echo 'daemon accepted an unselected event handler' >&2
  exit 1
fi
grep -Fq 'selected event handler unavailable' "$step_dir/invalid.err"
jq '.plugins[0].grants=["runtime.incus"]' "$step_dir/events.json" > "$step_dir/events.invalid.json"
jq --arg path "$step_dir/events.invalid.json" '.events.activation_path=$path' "$step_dir/host.json" > "$step_dir/host.invalid-grant.json"
if timeout 15 p daemon "$step_dir/host.invalid-grant.json" > "$step_dir/grant.out" 2> "$step_dir/grant.err"; then
  echo 'daemon accepted an ungranted event handler' >&2
  exit 1
fi
grep -Eq 'grants must exactly match|selected event handler unavailable' "$step_dir/grant.err"
echo P_DAEMON_EVENTS_PASS
