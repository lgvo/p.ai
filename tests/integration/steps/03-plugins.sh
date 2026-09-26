# Runs inside the disposable product-test VM as the confined pdev account.
umask 077
step_dir="$P_TEST_TMP/step-03"
mkdir -m 0700 "$step_dir"
filter_pkg="$step_dir/filter"
cp -R "$P_TEST_WASI_FILTER" "$filter_pkg"
chmod -R u+w "$filter_pkg"
mkdir -m 0700 "$step_dir/private"
log_file="$step_dir/private/events.ndjson"
filter_activation="$step_dir/filter-activation.json"

step03_expect_failure() {
  expected="$1"
  shift
  if "$@" > "$step_dir/unexpected-output.txt" 2> "$step_dir/diagnostic.txt"; then
    echo "command unexpectedly succeeded: $*" >&2
    exit 1
  fi
  if ! grep -Fq -- "$expected" "$step_dir/diagnostic.txt"; then
    cat "$step_dir/diagnostic.txt" >&2
    echo "missing expected diagnostic: $expected" >&2
    exit 1
  fi
}

step03_expect_failure_one_of() {
  first="$1"
  second="$2"
  shift 2
  if "$@" > "$step_dir/unexpected-output.txt" 2> "$step_dir/diagnostic.txt"; then
    echo "command unexpectedly succeeded: $*" >&2
    exit 1
  fi
  if ! grep -Fq -- "$first" "$step_dir/diagnostic.txt" &&
     ! grep -Fq -- "$second" "$step_dir/diagnostic.txt"; then
    cat "$step_dir/diagnostic.txt" >&2
    echo "missing expected diagnostic: $first or $second" >&2
    exit 1
  fi
}

digest=$(p plugins conformance "$filter_pkg" | jq -er '.sha256')
jq -n --arg pkg "$filter_pkg" --arg digest "$digest" --arg log "$log_file" \
  '{schema:"p.activation/v1",plugins:[{id:"org.p.example.filterlog",path:$pkg,sha256:$digest,grants:["event.file.append"],config:{path:$log,max_bytes:4096}}]}' \
  > "$filter_activation"
p plugins activate "$filter_activation" | jq -e '.[0].id == "org.p.example.filterlog"' >/dev/null

jq -n '{schema:"p.event/v1",id:"event-3a",kind:"session.condition_changed",occurred_at:"2026-09-23T00:00:00Z",instance:"p-vm",fields:{condition:"stopped"}}' > "$step_dir/stopped.json"
jq -n '{schema:"p.event/v1",id:"event-3b",kind:"session.condition_changed",occurred_at:"2026-09-23T00:00:01Z",instance:"p-vm",fields:{condition:"ready"}}' > "$step_dir/ready.json"
timeout 10 p plugins run-event "$filter_activation" "$step_dir/stopped.json" | jq -e '.schema == "p.command-result/v1" and .status == "skipped"' >/dev/null
test ! -e "$log_file"
timeout 10 p plugins run-event "$filter_activation" "$step_dir/ready.json" | jq -e '.schema == "p.command-result/v1" and .status == "appended"' >/dev/null
jq -e '.id == "event-3b" and .fields.condition == "ready"' "$log_file" >/dev/null
test "$(stat -c '%a' "$log_file")" = 600

jq '.plugins[0].grants=["runtime.incus"]' "$filter_activation" > "$step_dir/wrong-grant.json"
step03_expect_failure 'grants must exactly match' p plugins activate "$step_dir/wrong-grant.json"
printf '\n' >> "$filter_pkg/filter.wasm"
step03_expect_failure 'package digest mismatch' timeout 10 p plugins run-event "$filter_activation" "$step_dir/ready.json"
if grep -Fq -- "$log_file" "$step_dir/diagnostic.txt"; then
  echo "plugin diagnostic leaked private log path" >&2
  exit 1
fi
test "$(wc -l < "$log_file")" -eq 1

# The second immutable fixture deliberately attempts effects outside WASI and
# malformed broker calls. It is compiled from inspectable Go source before the
# VM boots, with no guest compiler or module downloads.
hostile_pkg="$step_dir/hostile"
cp -R "$P_TEST_WASI_HOSTILE" "$hostile_pkg"
chmod -R u+w "$hostile_pkg"
hostile_log="$step_dir/private/hostile.ndjson"
hostile_activation="$step_dir/hostile-activation.json"
hostile_digest=$(p plugins conformance "$hostile_pkg" | jq -er '.sha256')
jq -n --arg pkg "$hostile_pkg" --arg digest "$hostile_digest" --arg log "$hostile_log" \
  '{schema:"p.activation/v1",plugins:[{id:"org.p.example.filterlog",path:$pkg,sha256:$digest,grants:["event.file.append"],config:{path:$log,max_bytes:4096}}]}' \
  > "$hostile_activation"
p plugins activate "$hostile_activation" | jq -e --arg digest "$hostile_digest" '.[0].sha256 == $digest' >/dev/null

step03_hostile_event() {
  kind="$1"
  field="$2"
  value="$3"
  output="$4"
  jq -n --arg kind "$kind" --arg field "$field" --arg value "$value" \
    '{schema:"p.event/v1",id:"hostile-vm",kind:$kind,occurred_at:"2026-09-23T00:00:02Z",instance:"p-vm",fields:{($field):$value}}' > "$output"
}

step03_hostile_event session.condition_changed condition ready "$step_dir/host-probe.json"
env P_PLUGIN_PROBE_SECRET=must-not-inherit timeout 10 p plugins run-event "$hostile_activation" "$step_dir/host-probe.json" \
  | jq -e '.schema == "p.command-result/v1" and .status == "skipped"' >/dev/null
test ! -e "$hostile_log"

step03_hostile_event session.attachment_changed count 1 "$step_dir/unknown-broker.json"
step03_expect_failure 'broker method refused' timeout 10 p plugins run-event "$hostile_activation" "$step_dir/unknown-broker.json"
step03_hostile_event session.attachment_changed count 2 "$step_dir/repeated-broker.json"
step03_expect_failure 'broker request exceeds limit' timeout 10 p plugins run-event "$hostile_activation" "$step_dir/repeated-broker.json"
step03_hostile_event session.condition_changed condition starting "$step_dir/extra-broker-field.json"
step03_expect_failure 'broker method refused' timeout 10 p plugins run-event "$hostile_activation" "$step_dir/extra-broker-field.json"
step03_hostile_event session.unattended_changed unattended_condition running "$step_dir/invalid-request-pointer.json"
step03_expect_failure 'broker request outside module memory' timeout 10 p plugins run-event "$hostile_activation" "$step_dir/invalid-request-pointer.json"
step03_hostile_event session.condition_changed condition missing "$step_dir/invalid-reply-pointer.json"
step03_expect_failure 'broker response outside module memory' timeout 10 p plugins run-event "$hostile_activation" "$step_dir/invalid-reply-pointer.json"
step03_hostile_event session.condition_changed condition stopped "$step_dir/false-result.json"
step03_expect_failure 'invalid plugin result' timeout 10 p plugins run-event "$hostile_activation" "$step_dir/false-result.json"
step03_hostile_event session.policy_changed policy_condition current "$step_dir/oversized-stdout.json"
step03_expect_failure 'plugin output exceeds limit' timeout 10 p plugins run-event "$hostile_activation" "$step_dir/oversized-stdout.json"
step03_hostile_event session.policy_changed policy_condition outdated "$step_dir/oversized-stderr.json"
step03_expect_failure 'plugin output exceeds limit' timeout 10 p plugins run-event "$hostile_activation" "$step_dir/oversized-stderr.json"
step03_hostile_event session.condition_changed condition creating "$step_dir/memory-limit.json"
step03_expect_failure_one_of 'plugin output exceeds limit' 'plugin execution failed' timeout 10 p plugins run-event "$hostile_activation" "$step_dir/memory-limit.json"
step03_hostile_event operation.progress phase running "$step_dir/time-limit.json"
step03_expect_failure 'plugin execution timed out or was cancelled' timeout 10 p plugins run-event "$hostile_activation" "$step_dir/time-limit.json"
test ! -e "$hostile_log"

asset_pkg="$step_dir/assets"
mkdir -m 0700 "$asset_pkg"
cat > "$asset_pkg/plugin.json" <<'JSON'
{"schema":"p.plugin/v1","id":"org.p.example.host","version":"1.0.0","api":"1.0","capability":"interactive-host","placement":"internal-session","description":"VM asset planning fixture","runtime":{"kind":"assets"},"requests":["session.asset.install"],"assets":["p-session.target","p-interactive.service","p-attach"]}
JSON
printf '[Unit]\nDescription=P fixture\n' > "$asset_pkg/p-session.target"
printf '[Unit]\nDescription=P fixture service\n' > "$asset_pkg/p-interactive.service"
printf '#!/bin/sh\nexit 0\n' > "$asset_pkg/p-attach"
asset_digest=$(p plugins conformance "$asset_pkg" | jq -er '.sha256')
asset_activation="$step_dir/asset-activation.json"
jq -n --arg pkg "$asset_pkg" --arg digest "$asset_digest" \
  '{schema:"p.activation/v1",plugins:[{id:"org.p.example.host",path:$pkg,sha256:$digest,grants:["session.asset.install"],config:{}}]}' > "$asset_activation"
p plugins activate "$asset_activation" | jq -e '.[0].capability == "interactive-host"' >/dev/null
p plugins plan-assets "$asset_activation" org.p.example.host | jq -e '
  .schema == "p.asset-plan/v1" and .scope == "internal-session" and (.files|length) == 3 and
  (.files|map({role,destination,mode})|sort_by(.role)) ==
  ([{"role":"p-attach","destination":"/usr/libexec/p/attach","mode":365},
    {"role":"p-interactive.service","destination":"/etc/systemd/system/p-interactive.service","mode":420},
    {"role":"p-session.target","destination":"/etc/systemd/system/p-session.target","mode":420}]|sort_by(.role))
' >/dev/null

jq '.placement="host"' "$asset_pkg/plugin.json" > "$step_dir/invalid-manifest.json"
mv "$step_dir/invalid-manifest.json" "$asset_pkg/plugin.json"
step03_expect_failure 'requires internal-session placement' p plugins conformance "$asset_pkg"
jq '.placement="internal-session" | .assets[2]="../other"' "$asset_pkg/plugin.json" > "$step_dir/invalid-manifest.json"
mv "$step_dir/invalid-manifest.json" "$asset_pkg/plugin.json"
step03_expect_failure 'unsafe relative path' p plugins conformance "$asset_pkg"

echo P_PLUGIN_EXECUTION_ASSETS_PASS
