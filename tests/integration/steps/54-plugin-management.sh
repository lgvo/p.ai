# shellcheck shell=bash
# Protected managed staging and offline removal, real selected daemon/session.
# The staged file-log package handles events through p plugins run-event and
# through the daemon, each with a host configuration that selects it.
# No authentication; private/credential sentinels are dummy fixture bytes.
set -E
umask 077
step_dir="$P_TEST_TMP/step-54"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step54
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
unset SSH_AUTH_SOCK GIT_SSH GIT_SSH_COMMAND GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM GIT_CONFIG_COUNT GIT_DIR GIT_WORK_TREE
daemon_pid=
uuid=
inc() { timeout 60 incus --force-local --project user-1000 "$@"; }
rpc() { timeout 30 p api "$socket" "$@"; }
stop_daemon() {
 if test -n "$daemon_pid"; then
  kill -TERM "$daemon_pid" 2>/dev/null || true
  for ((i=0;i<100;i++)); do if ! kill -0 "$daemon_pid" 2>/dev/null; then break; fi; sleep 0.1; done
  if kill -0 "$daemon_pid" 2>/dev/null; then kill -KILL "$daemon_pid" 2>/dev/null || true; fi
  wait "$daemon_pid" 2>/dev/null || true
  daemon_pid=
 fi
}
cleanup() {
 stop_daemon
 if test -n "$uuid"; then inc delete --force "p-$uuid" >/dev/null 2>&1 || true; rm -rf -- "${endpoint_prefix:?}/${uuid:?}"; fi
 rmdir "$endpoint_prefix" 2>/dev/null || true
 # The outer runner removes this disposable fixture tree. Change only our
 # recorded exact directories after assertions; production packages stay 0500.
 local report digest path
 for report in agent-stage event-stage update; do
  if ! test -f "$step_dir/$report.json" || test -L "$step_dir/$report.json"; then
   continue
  fi
  digest=$(jq -er '.package.sha256' "$step_dir/$report.json") || continue
  path=$(jq -er '.package.path' "$step_dir/$report.json") || continue
  [[ "$digest" =~ ^[0-9a-f]{64}$ ]] || continue
  test "$path" = "$state/plugins/packages/$digest" || continue
  if test -d "$path" && ! test -L "$path" && test "$(stat -c %u "$path")" = "$(id -u)"; then
   chmod u+w -- "$path"
  fi
 done
}
on_error() { printf 'P_PLUGIN_MANAGEMENT_FAIL line=%s status=%s\n' "$2" "$1" >&2; tail -c 2048 "$step_dir/daemon.err" >&2 || true; }
trap cleanup EXIT
trap 'on_error "$?" "$LINENO"' ERR
expect_failure() {
 local expected="$1"; shift
 if "$@" > "$step_dir/unexpected.out" 2> "$step_dir/refusal.err"; then printf 'unexpected package action success\n' >&2; return 1; fi
 if ! grep -Fq -- "$expected" "$step_dir/refusal.err"; then head -c 2048 "$step_dir/refusal.err" >&2; return 1; fi
}
jq -n --arg state "$state" '{schema:"p.host/v1",state_dir:$state}' > "$step_dir/offline.json"
stage() {
 local source="$1" output="$2" digest path
 digest=$(p plugins conformance "$source" | jq -er '.sha256')
 p plugins install "$step_dir/offline.json" "$source" "$digest" > "$output"
 jq -e --arg sha "$digest" '.package.sha256==$sha and .selection.sha256==$sha and .selection.path==.package.path and .explicit_trusted_selection_required==true' "$output" >/dev/null
 path=$(jq -er '.package.path' "$output")
 test "$(stat -c '%a:%u:%h' "$path")" = "500:$(id -u):2"
 test "$(stat -c '%a:%u:%h' "$path/plugin.json")" = "400:$(id -u):1"
}
stage "$P_TEST_SOURCE/plugins/bundled/codex-adapter" "$step_dir/agent-stage.json"
test "$(stat -c '%a' "$(jq -er '.package.path' "$step_dir/agent-stage.json")/p-codex-adapter")" = 400
package_root="$(dirname "$(dirname "$(readlink -f "$(command -v p)")")")"
cp -R "$package_root/share/p/plugins/file-log" "$step_dir/event-source"
chmod -R u+w "$step_dir/event-source"
event_source="$step_dir/event-source"
event_digest=$(p plugins conformance "$event_source" | jq -er '.sha256')
expect_failure 'explicitly approved digest' p plugins install "$step_dir/offline.json" "$event_source" "$(printf '%064d' 0)"
stage "$event_source" "$step_dir/event-stage.json"
old_path=$(jq -er '.package.path' "$step_dir/event-stage.json")
# Staging sets the modes the launch checks need: the entry may be started.
test "$(stat -c '%a:%h' "$old_path/p-event-file")" = 500:1
# Source-author edits are not a selection or an update of the staged bytes.
jq '.description="Explicitly reviewed plugin update"' "$event_source/plugin.json" > "$step_dir/source-next.json"
mv "$step_dir/source-next.json" "$event_source/plugin.json"
test "$(p plugins conformance "$old_path" | jq -er '.sha256')" = "$event_digest"
new_digest=$(p plugins conformance "$event_source" | jq -er '.sha256')
test "$new_digest" != "$event_digest"
expect_failure 'opened package bytes differ' p plugins update "$step_dir/offline.json" "$event_digest" "$event_source" "$event_digest"
# Host configuration selects the staged packages by the printed selections,
# adding only the handler's config, reviewed here by the host owner.
log="$step_dir/events.ndjson"
jq --arg log "$log" '.selection|.config={path:$log,max_bytes:1048576}' "$step_dir/event-stage.json" > "$step_dir/events.json"
# A private host configuration holding only state_dir and events is enough
# to try the staged handler.
jq -n --arg state "$state" --slurpfile events "$step_dir/events.json" \
 '{schema:"p.host/v1",state_dir:$state,events:$events[0]}' > "$step_dir/offline-events.json"
jq -n '{schema:"p.event/v1",id:"event-54",kind:"session.condition_changed",occurred_at:"2026-09-23T00:00:00Z",instance:"p-vm",fields:{condition:"ready"}}' > "$step_dir/event.json"
p plugins run-event "$step_dir/offline-events.json" "$step_dir/event.json" | jq -e '.status=="handled" and .event_id=="event-54"' >/dev/null
jq -se 'length==1 and .[0].id=="event-54"' "$log" >/dev/null
image=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
driver=$(bash "$P_TEST_SOURCE/tests/integration/runtime-driver-config.sh" "$(command -v incus)")
source_driver=$(bash "$P_TEST_SOURCE/tests/integration/source-driver-config.sh")
jq -n --arg state "$state" --argjson source_driver "$source_driver" --slurpfile agent "$step_dir/agent-stage.json" \
 --slurpfile events "$step_dir/events.json" --arg prefix "$endpoint_prefix" --argjson driver "$driver" --arg image "$image" \
 '{schema:"p.host/v1",state_dir:$state,git:{listen:"127.0.0.1:0",driver:$source_driver},
 events:$events[0],
 runtime:{agent_adapter:$agent[0].selection,driver:$driver,endpoint_prefix:$prefix,
 disk_source_ceilings:["/var/lib/p-vm/endpoints","/var/lib/p-vm/grants"],base_image_fingerprint:$image,
 project_policy:{network:"none",filesystem_mounts:[],command:["/run/current-system/sw/bin/bash"]}}}' > "$step_dir/host.json"
bash "$P_TEST_SOURCE/tests/integration/public-egress-host-config.sh" "$step_dir/host.json"
start_daemon() {
 p daemon "$step_dir/host.json" >> "$step_dir/daemon.out" 2>> "$step_dir/daemon.err" & daemon_pid=$!
 local deadline=$((SECONDS+30))
 while ((SECONDS<deadline)); do
  if test -S "$socket" && rpc system.health | jq -e '.result.control_state=="ready"' >/dev/null 2>&1; then return; fi
  if ! kill -0 "$daemon_pid" 2>/dev/null; then return 1; fi
  sleep 0.1
 done
 return 1
}
wait_completed() {
 local id="$1" deadline=$((SECONDS+120)) response
 while ((SECONDS<deadline)); do
  response=$(rpc operation.wait "$(jq -nc --arg id "$id" '{id:$id,timeout:20}')")
  if jq -e '.result.operation.status=="completed"' <<< "$response" >/dev/null; then return; fi
  if jq -e '.result.operation.status=="blocked" or .result.operation.status=="failed"' <<< "$response" >/dev/null; then jq -c '.result.operation|{status,phase,diagnostic}' <<< "$response" >&2; return 1; fi
  if jq -e '.result.operation.status!="running"' <<< "$response" >/dev/null; then sleep 0.2; fi
 done
 return 1
}
start_daemon
expect_failure 'daemon stopped' p plugins remove "$step_dir/offline.json" "$event_digest"
create=$(rpc project.create '{"key":"managed-bootstrap","project":"managed-plugins"}')
uuid=$(jq -er '.result.operation.session_uuid' <<< "$create")
wait_completed "$(jq -er '.result.operation.id' <<< "$create")"
# The daemon delivers the creation's events to the staged handler.
delivered() { jq -se --arg uuid "$uuid" 'any(.[]; .session==$uuid and .kind=="operation.progress" and .fields.phase=="completed")' "$log" >/dev/null; }
for ((i=0;i<100;i++)); do if delivered; then break; fi; sleep 0.1; done
delivered
stop=$(rpc session.stop "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" |
 jq -er '.result | select(.changed==true and .operation.kind=="session.stop") | .operation.id')
wait_completed "$stop"
rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" | jq -e '.result.session.session_condition=="stopped"' >/dev/null
printf 'managed-private-dummy\n' > "$step_dir/private"
printf 'managed-credential-dummy\n' > "$step_dir/credential"
inc file push "$step_dir/private" "p-$uuid/home/p/managed-private" --uid 1000 --gid 1000 --mode 0600
inc file push "$step_dir/credential" "p-$uuid/home/p/.managed-credential" --uid 1000 --gid 1000 --mode 0600
key_sha=$(sha256sum "$state/session_keys/$uuid" | cut -d ' ' -f1)
inc list "^p-$uuid$" --format json | jq -e 'length==1 and .[0].expanded_config["security.idmap.isolated"]=="true" and (.[0].expanded_config["security.nesting"] // "false")=="false"' >/dev/null
stop_daemon
expect_failure 'selected in host configuration' p plugins remove "$step_dir/host.json" "$event_digest"
agent_digest=$(jq -er '.package.sha256' "$step_dir/agent-stage.json")
expect_failure 'durable session/operation/cache depends' p plugins remove "$step_dir/offline.json" "$agent_digest"
test -d "$(jq -er '.package.path' "$step_dir/agent-stage.json")"
p plugins update "$step_dir/host.json" "$event_digest" "$event_source" "$new_digest" > "$step_dir/update.json"
jq -e --arg old "$event_digest" --arg new "$new_digest" '.previous_sha256==$old and .package.sha256==$new and .explicit_trusted_selection_required' "$step_dir/update.json" >/dev/null
# Staging did not select the update: host configuration is unchanged.
test "$(jq -er '.events.sha256' "$step_dir/host.json")" = "$event_digest"
jq --slurpfile update "$step_dir/update.json" '.events.path=$update[0].selection.path | .events.sha256=$update[0].selection.sha256' \
 "$step_dir/host.json" > "$step_dir/host.next.json"
mv "$step_dir/host.next.json" "$step_dir/host.json"
start_daemon
rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" | jq -e '.result.session.session_condition=="stopped"' >/dev/null
stop_daemon
p plugins remove "$step_dir/host.json" "$event_digest" | jq -e '.status=="removed" and .new_invocations=="disabled"' >/dev/null
p plugins remove "$step_dir/host.json" "$event_digest" | jq -e '.status=="removed"' >/dev/null
test ! -e "$old_path"
expect_failure 'permanently disabled' p plugins run-event "$step_dir/offline-events.json" "$step_dir/event.json"
# A different instance cannot select a managed package from this registry.
jq --arg state "$step_dir/foreign-state" '.state_dir=$state | {schema,state_dir,events}' "$step_dir/host.json" > "$step_dir/foreign.json"
expect_failure 'another P instance' timeout 15 p daemon "$step_dir/foreign.json"
test "$(sha256sum "$state/session_keys/$uuid" | cut -d ' ' -f1)" = "$key_sha"
inc file pull "p-$uuid/home/p/managed-private" "$step_dir/private-after"
inc file pull "p-$uuid/home/p/.managed-credential" "$step_dir/credential-after"
cmp "$step_dir/private" "$step_dir/private-after"
cmp "$step_dir/credential" "$step_dir/credential-after"
test -s "$log"
new_path=$(jq -er '.package.path' "$step_dir/update.json")
test -d "$new_path"
test "$(p plugins conformance "$new_path" | jq -er '.sha256')" = "$new_digest"
inc list "^p-$uuid$" --format json | jq -e 'length==1 and .[0].status=="Stopped"' >/dev/null
printf 'P_PLUGIN_MANAGEMENT_OK staging update explicit-selection offline-removal leases durable-dependency preservation\n'
