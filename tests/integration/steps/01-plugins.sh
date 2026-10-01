# Runs inside the disposable product-test VM as the confined pdev account.
umask 077
plugin_dir="$P_TEST_TMP/file-log"
cp -R "$P_TEST_SOURCE/plugins/bundled/file-log" "$plugin_dir"
chmod -R u+w "$plugin_dir"
mkdir -m 0700 "$P_TEST_TMP/private"
log_file="$P_TEST_TMP/private/events.ndjson"
activation="$P_TEST_TMP/activation.json"
event_file="$P_TEST_TMP/event.json"

expect_failure_contains() {
  expected="$1"
  shift
  diagnostic="$P_TEST_TMP/diagnostic.txt"
  if "$@" >"$P_TEST_TMP/unexpected-output.txt" 2>"$diagnostic"; then
    echo "command unexpectedly succeeded: $*" >&2
    exit 1
  fi
  if ! grep -Fq -- "$expected" "$diagnostic"; then
    echo "missing expected diagnostic '$expected':" >&2
    cat "$diagnostic" >&2
    exit 1
  fi
}

digest=$(p plugins conformance "$plugin_dir" | jq -er '.sha256')
test "$(p plugins list "$P_TEST_TMP" | jq -r '.packages[0].manifest.id')" = org.p.filelog
jq -n \
  --arg pkg "$plugin_dir" --arg digest "$digest" --arg log "$log_file" \
  '{schema:"p.activation/v1",plugins:[{id:"org.p.filelog",path:$pkg,sha256:$digest,grants:["event.file.append"],config:{path:$log,max_bytes:1024}}]}' \
  > "$activation"
test "$(p plugins activate "$activation" | jq -r '.[0].capability')" = event-handler
cat > "$event_file" <<'JSON'
{"schema":"p.event/v1","id":"event-1","kind":"session.condition_changed","occurred_at":"2026-09-23T00:00:00Z","instance":"p-test","fields":{"condition":"ready"}}
JSON
p plugins emit "$activation" "$event_file" | jq -e '.status == "appended"' >/dev/null
test "$(stat -c '%a' "$log_file")" = 600
jq -e '.kind == "session.condition_changed" and .fields.condition == "ready"' "$log_file" >/dev/null

jq '.plugins[0].grants=[]' "$activation" > "$P_TEST_TMP/missing.json"
expect_failure_contains 'grants must exactly match' p plugins activate "$P_TEST_TMP/missing.json"
jq '.plugins[0].grants += ["runtime.incus"]' "$activation" > "$P_TEST_TMP/extra.json"
expect_failure_contains 'grants must exactly match' p plugins activate "$P_TEST_TMP/extra.json"

printf 'changed\n' > "$plugin_dir/extra"
expect_failure_contains 'package digest mismatch' p plugins activate "$activation"
rm "$plugin_dir/extra"

bad_api="$P_TEST_TMP/bad-api"
cp -R "$plugin_dir" "$bad_api"
chmod -R u+w "$bad_api"
jq '.api="2.0"' "$bad_api/plugin.json" > "$P_TEST_TMP/changed-manifest.json"
mv "$P_TEST_TMP/changed-manifest.json" "$bad_api/plugin.json"
expect_failure_contains 'unsupported plugin API' p plugins conformance "$bad_api"

bad_link="$P_TEST_TMP/bad-link"
cp -R "$plugin_dir" "$bad_link"
ln -s /etc/passwd "$bad_link/extra"
expect_failure_contains 'symlink in package' p plugins conformance "$bad_link"

for ((index=0; index<20; index++)); do
  p plugins emit "$activation" "$event_file" >/dev/null
done
test -f "$log_file.1"
test "$(stat -c '%s' "$log_file")" -le 1024
test "$(stat -c '%s' "$log_file.1")" -le 1024

rm "$log_file"
ln -s "$P_TEST_TMP/private/target" "$log_file"
expect_failure_contains 'too many levels of symbolic links' p plugins emit "$activation" "$event_file"
rm "$log_file"
printf 'private\n' > "$P_TEST_TMP/private/target"
ln "$P_TEST_TMP/private/target" "$log_file"
expect_failure_contains 'unlinked elsewhere' p plugins emit "$activation" "$event_file"
rm "$log_file"
chmod 0755 "$P_TEST_TMP/private"
expect_failure_contains 'log directory must be private' p plugins emit "$activation" "$event_file"
chmod 0700 "$P_TEST_TMP/private"

echo P_PLUGIN_FOUNDATION_PASS
