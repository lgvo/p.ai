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
p_instance=
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
  # The stopped daemon no longer releases the commit image it built.
  if test -n "$p_instance"; then
    inc image list --format json 2>/dev/null |
      jq -r --arg instance "$p_instance" '.[] | select(.properties["p.instance"]==$instance) | .fingerprint' |
      while IFS= read -r built; do inc image delete "$built" >/dev/null 2>&1 || true; done || true
  fi
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
  .control_protocol=="p.control/v2" and .plugin_schema=="p.plugin/v2" and
  .event_handler_protocol=="p.event-handler/v1" and
  any(.dependencies[]; .path=="modernc.org/sqlite" and .version=="v1.39.1") and
  all(.dependencies[]; .path!="github.com/tetratelabs/wazero")' \
  "$step_dir/version.json" >/dev/null
package_root="$(dirname "$(dirname "$(readlink -f "$(command -v p)")")")"
catalog="$package_root/share/p/plugins"
# Inspect the installed artifacts, rather than a separately built candidate.
test -s "$package_root/share/p/LICENSE"
test -s "$package_root/share/p/licenses/modules/modernc.org/sqlite/LICENSE"
test -s "$package_root/share/p/licenses/vendor/modernc.org/memory/LICENSE-MMAP-GO"
test -s "$package_root/share/p/licenses/vendor/modernc.org/libc/honnef.co/go/netdb/LICENSE"
test ! -e "$package_root/share/p/licenses/modules/github.com/tetratelabs/wazero"
test -s "$package_root/share/p/licenses/go/LICENSE"
test -s "$package_root/share/p/licenses/go/PATENTS"
test "$(cat "$package_root/share/p/licenses/go/VERSION")" = 1.26.7
for program in "$package_root/bin/p" "$catalog/file-log/p-event-file"; do
  readelf -l "$program" > "$step_dir/p-program-headers"
  readelf -d "$program" > "$step_dir/p-dynamic-section"
  ! grep -q INTERP "$step_dir/p-program-headers"
  ! grep -q NEEDED "$step_dir/p-dynamic-section"
done
# The two bundled packages, selected for host configuration by path and
# digest; the file-log's entry is the built handler, not a command.
test ! -e "$package_root/bin/p-event-file"
p plugins defaults "$log_file" > "$step_dir/defaults.json"
p plugins list "$catalog" > "$step_dir/catalog.json"
jq -e '(.packages|length)==2 and (.rejected|length)==0' "$step_dir/catalog.json" >/dev/null
jq -e --arg catalog "$catalog" --arg log "$log_file" --slurpfile listed "$step_dir/catalog.json" '
  ($listed[0].packages | map({key: .manifest.id, value: .sha256}) | from_entries) as $digest |
  (keys|sort)==["events","runtime"] and (.runtime|keys)==["agent_adapter"] and
  .events.path==($catalog+"/file-log") and .events.sha256==$digest["org.p.filelog"] and
  .events.config=={path:$log,max_bytes:4194304} and
  .runtime.agent_adapter=={path:($catalog+"/codex-adapter"),sha256:$digest["org.p.codex-adapter"]}' \
  "$step_dir/defaults.json" >/dev/null
test ! -e "$log_file"
image=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
driver=$(bash "$P_TEST_SOURCE/tests/integration/runtime-driver-config.sh" "$(command -v incus)")
source_driver=$(bash "$P_TEST_SOURCE/tests/integration/source-driver-config.sh")
jq -n --arg state "$state" --argjson source_driver "$source_driver" --slurpfile defaults "$step_dir/defaults.json" \
  --argjson driver "$driver" --arg prefix "$endpoint_prefix" --arg image "$image" \
  '{schema:"p.host/v1",state_dir:$state,
    git:{listen:"127.0.0.1:0",driver:$source_driver},
    runtime:{agent_adapter:$defaults[0].runtime.agent_adapter,
      driver:$driver,endpoint_prefix:$prefix,
      disk_source_ceilings:["/var/lib/p-vm/endpoints","/var/lib/p-vm/grants"],base_image_fingerprint:$image,
      environment:{system:"x86_64-linux",builder_storage_pool:"builders"},
      project_policy:{network:"none",filesystem_mounts:[],command:["/run/current-system/sw/bin/bash"]}},
    events:$defaults[0].events}' > "$step_dir/host.json"
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
    response=$(rpc operation.wait "$(jq -nc --arg id "$id" '{id:$id,timeout:20}')")
    status=$(jq -er '.result.operation.status' <<< "$response")
    if test "$status" = completed; then printf '%s\n' "$response"; return; fi
    if test "$status" = blocked || test "$status" = failed || test "$status" = unknown; then
      echo "distribution operation did not complete: $response" >&2; return 1
    fi
    if test "$status" != running; then sleep 0.3; fi
  done
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
  local target="$1" deadline=$((SECONDS+90))
  while ((SECONDS < deadline)); do
    if rpc session.inspect "$(jq -nc --arg uuid "$target" '{uuid:$uuid}')" |
      jq -e '.result.session.session_condition=="ready"' >/dev/null; then return; fi
    sleep 0.3
  done
  return 1
}
start_daemon
p_instance=$(rpc system.hello | jq -er '.result.instance_id | select(length==36)')
created=$(rpc project.create '{"key":"distribution-bootstrap","project":"bundled-distribution"}')
boot_uuid=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created")
sessions+=("$boot_uuid")
wait_operation "$(jq -er '.result.operation.id' <<< "$created")" >/dev/null
wait_ready "$boot_uuid"
# shellcheck disable=SC2016 # Evaluate paths inside the container.
guest "$boot_uuid" /run/current-system/sw/bin/bash -eu -c '
  root=$(dirname "$(dirname "$(readlink -f /usr/libexec/p/runtime-kit)")")
  test -s "$root/share/p/LICENSE"
  test -s "$root/share/p/licenses/go/LICENSE"
  test -s "$root/share/p/licenses/go/PATENTS"
  test "$(cat "$root/share/p/licenses/go/VERSION")" = 1.26.7
'
guest "$boot_uuid" /run/current-system/sw/bin/cat /usr/libexec/p/runtime-kit > "$step_dir/runtime-kit"
readelf -l "$step_dir/runtime-kit" > "$step_dir/runtime-program-headers"
readelf -d "$step_dir/runtime-kit" > "$step_dir/runtime-dynamic-section"
! grep -q INTERP "$step_dir/runtime-program-headers"
! grep -q NEEDED "$step_dir/runtime-dynamic-section"
echo P_INSTALLED_STATIC_LICENSE_NOTICES_PASS
guest "$boot_uuid" git config user.name P
guest "$boot_uuid" git config user.email p@example.invalid
guest "$boot_uuid" /run/current-system/sw/bin/bash -c 'printf "retained source\n" > source.txt; git add source.txt; git commit -qm initial; git push origin HEAD:main'
oid=$(guest "$boot_uuid" git rev-parse HEAD)
created=$(rpc session.create '{"key":"distribution-committed","project":"bundled-distribution","branch":"composed","choice":"new","source":"refs/heads/main"}')
uuid=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created")
sessions+=("$uuid")
result=$(wait_operation "$(jq -er '.result.operation.id' <<< "$created")")
# Main has no flake: the session starts from an image of main's commit built
# from the base packages and the commit's code.
jq -e --arg oid "$oid" --arg base "$image" '.result.operation.evidence |
  .captured_oid==$oid and .image.selection=="no-flake" and .image.built==true and
  .image_fingerprint==.image.ref and .image_fingerprint!=$base' <<< "$result" >/dev/null
wait_ready "$uuid"
rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" |
  jq -e --arg oid "$oid" --arg base "$image" '.result.session.image |
    .commit_oid==$oid and .selection=="base" and .state=="built" and
    .base_image_fingerprint==$base and .image_fingerprint!=$base' >/dev/null
test "$(guest "$uuid" git rev-parse HEAD)" = "$oid"
guest "$uuid" /run/current-system/sw/bin/test -x /usr/libexec/p/codex-adapter
# Adapter data is an explicit event fixture, not authenticated Codex execution.
prior_sequence=$(rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" |
  jq -er '.result.session.latest_unattended_condition.receive_sequence // 0')
notify='{"type":"agent-turn-complete","thread-id":"b5f6c1c2-1111-2222-3333-444455556666","turn-id":"turn-1","cwd":"/workspace","input-messages":[],"last-assistant-message":"DUMMY FIXTURE"}'
guest "$uuid" /usr/libexec/p/codex-adapter notify "$notify"
deadline=$((SECONDS+10))
while ((SECONDS < deadline)); do
  if rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" |
    jq -e --argjson prior "$prior_sequence" '.result.session.latest_unattended_condition |
      .condition=="idle" and .source=="codex/thread/b5f6c1c2-1111-2222-3333-444455556666" and
      .reason=="turn complete" and .adapter=="codex" and .adapter_version=="0.151.0" and
      .receive_sequence>$prior' >/dev/null; then break; fi
  sleep 0.2
done
rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" |
  jq -e --argjson prior "$prior_sequence" '.result.session.latest_unattended_condition |
    .condition=="idle" and .source=="codex/thread/b5f6c1c2-1111-2222-3333-444455556666" and
    .reason=="turn complete" and .adapter=="codex" and .adapter_version=="0.151.0" and
    .receive_sequence>$prior' >/dev/null
echo P_BUNDLED_CODEX_EVENT_FIXTURE_PASS
change_session session.stop "$uuid"
rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" |
  jq -e '.result.session.session_condition=="stopped"' >/dev/null
stop_daemon
start_daemon
repo_hash=$(printf %s bundled-distribution | sha256sum | cut -d' ' -f1)
test "$(GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null git --git-dir="$state/repositories/$repo_hash.git" rev-parse refs/heads/main)" = "$oid"
change_session session.start "$uuid"
wait_ready "$uuid"
test "$(guest "$uuid" git rev-parse HEAD)" = "$oid"
jq -se --arg uuid "$uuid" 'any(.[]; .schema=="p.event/v1" and .session==$uuid and .kind=="session.condition_changed")' "$log_file" >/dev/null
if grep -Fq 'DUMMY FIXTURE' "$log_file"; then echo 'raw event payload escaped' >&2; exit 1; fi
echo P_BUNDLED_DISTRIBUTION_PASS
