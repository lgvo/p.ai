# Runs inside the disposable product-test VM as the confined pdev account.
# The example filter, a native event handler built apart from P, through
# the CLI; and the conformance rules of an agent-adapter package.
umask 077
step_dir="$P_TEST_TMP/step-03"
mkdir -m 0700 "$step_dir"
filter_pkg="$step_dir/filter"
cp -R "$P_TEST_EVENT_FILTER" "$filter_pkg"
chmod -R u+w "$filter_pkg"
mkdir -m 0700 "$step_dir/private"
log_file="$step_dir/private/events.ndjson"
host="$step_dir/host.json"

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

digest=$(p plugins conformance "$filter_pkg" | jq -er 'select(.manifest.id == "org.p.example.filterlog" and .manifest.entry == "filter-log") | .sha256')
jq -n --arg state "$step_dir/state" --arg pkg "$filter_pkg" --arg digest "$digest" --arg log "$log_file" \
  '{schema:"p.host/v1",state_dir:$state,events:{path:$pkg,sha256:$digest,config:{path:$log,max_bytes:4096}}}' > "$host"

jq -n '{schema:"p.event/v1",id:"event-3a",kind:"session.condition_changed",occurred_at:"2026-09-23T00:00:00Z",instance:"p-vm",fields:{condition:"stopped"}}' > "$step_dir/stopped.json"
jq -n '{schema:"p.event/v1",id:"event-3b",kind:"session.condition_changed",occurred_at:"2026-09-23T00:00:01Z",instance:"p-vm",fields:{condition:"ready"}}' > "$step_dir/ready.json"
timeout 10 p plugins run-event "$host" "$step_dir/stopped.json" | jq -e '.status == "skipped"' >/dev/null
test ! -e "$log_file"
timeout 10 p plugins run-event "$host" "$step_dir/ready.json" | jq -e '.status == "handled"' >/dev/null
jq -e '.id == "event-3b" and .fields.condition == "ready"' "$log_file" >/dev/null
test "$(stat -c '%a' "$log_file")" = 600

# A changed entry no longer matches the pinned digest: the handler never
# starts, and the refusal names no private path from its config.
printf '\n' >> "$filter_pkg/filter-log"
step03_expect_failure 'failed its launch checks' timeout 10 p plugins run-event "$host" "$step_dir/ready.json"
step03_expect_failure 'not the pinned' timeout 10 p plugins run-event "$host" "$step_dir/ready.json"
if grep -Fq -- "$log_file" "$step_dir/diagnostic.txt"; then
  echo "plugin diagnostic leaked private log path" >&2
  exit 1
fi
test "$(wc -l < "$log_file")" -eq 1

# An entry without its execute bit is refused, whatever its digest.
cp -R "$P_TEST_EVENT_FILTER" "$step_dir/unexecutable"
chmod -R u+w "$step_dir/unexecutable"
chmod u-x "$step_dir/unexecutable/filter-log"
jq --arg pkg "$step_dir/unexecutable" '.events.path=$pkg' "$host" > "$step_dir/unexecutable.json"
step03_expect_failure "lacks its owner's execute bit" timeout 10 p plugins run-event "$step_dir/unexecutable.json" "$step_dir/ready.json"

# An executable capability names its entry; a package of files names none.
no_entry="$step_dir/no-entry"
cp -R "$P_TEST_EVENT_FILTER" "$no_entry"
chmod -R u+w "$no_entry"
jq 'del(.entry)' "$no_entry/plugin.json" > "$step_dir/manifest.json"
mv "$step_dir/manifest.json" "$no_entry/plugin.json"
step03_expect_failure 'event-handler requires an entry' p plugins conformance "$no_entry"

asset_pkg="$step_dir/assets"
mkdir -m 0700 "$asset_pkg"
cat > "$asset_pkg/plugin.json" <<'JSON'
{"schema":"p.plugin/v2","id":"org.p.example.adapter","version":"1.0.0","description":"VM agent adapter fixture","capability":"agent-adapter","assets":["p-codex-adapter"]}
JSON
printf '#!/bin/sh\nexit 0\n' > "$asset_pkg/p-codex-adapter"
p plugins conformance "$asset_pkg" | jq -e '.manifest.capability == "agent-adapter" and (.sha256|length) == 64' >/dev/null
jq '.entry="p-codex-adapter"' "$asset_pkg/plugin.json" > "$step_dir/invalid-manifest.json"
mv "$step_dir/invalid-manifest.json" "$asset_pkg/plugin.json"
step03_expect_failure 'holds files and names no entry' p plugins conformance "$asset_pkg"
jq 'del(.entry) | .assets[0]="../other"' "$asset_pkg/plugin.json" > "$step_dir/invalid-manifest.json"
mv "$step_dir/invalid-manifest.json" "$asset_pkg/plugin.json"
step03_expect_failure 'unsafe relative path' p plugins conformance "$asset_pkg"
jq '.assets[0]="p-codex-adapter" | .requests=["agent.status.report"]' "$asset_pkg/plugin.json" > "$step_dir/invalid-manifest.json"
mv "$step_dir/invalid-manifest.json" "$asset_pkg/plugin.json"
step03_expect_failure 'unknown field' p plugins conformance "$asset_pkg"

echo P_PLUGIN_EXECUTION_ASSETS_PASS
