# Runs inside the disposable product-test VM as the confined pdev account.
# The bundled file-log event handler through the CLI: conformance, a host
# configuration that selects the package, p plugins run-event, the launch
# checks and the handler's own checks of its log file.
umask 077
package_root="$(dirname "$(dirname "$(readlink -f "$(command -v p)")")")"
plugin_dir="$P_TEST_TMP/file-log"
cp -R "$package_root/share/p/plugins/file-log" "$plugin_dir"
chmod -R u+w "$plugin_dir"
mkdir -m 0700 "$P_TEST_TMP/private"
log_file="$P_TEST_TMP/private/events.ndjson"
host="$P_TEST_TMP/host.json"
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
test "$(p plugins list "$P_TEST_TMP" | jq -r '.packages[0].manifest | .id + " " + .entry')" = "org.p.filelog p-event-file"
select_events() {
  jq -n --arg state "$P_TEST_TMP/state" --arg pkg "$1" --arg digest "$2" --arg log "$log_file" \
    '{schema:"p.host/v1",state_dir:$state,events:{path:$pkg,sha256:$digest,config:{path:$log,max_bytes:1024}}}' > "$host"
}
select_events "$plugin_dir" "$digest"
cat > "$event_file" <<'JSON'
{"schema":"p.event/v1","id":"event-1","kind":"session.condition_changed","occurred_at":"2026-09-23T00:00:00Z","instance":"p-test","fields":{"condition":"ready"}}
JSON
p plugins run-event "$host" "$event_file" | jq -e '.status == "handled" and .event_id == "event-1"' >/dev/null
test "$(stat -c '%a' "$log_file")" = 600
jq -e '.kind == "session.condition_changed" and .fields.condition == "ready"' "$log_file" >/dev/null

# An activation file selects nothing: host configuration refuses its fields.
jq --arg activation "$P_TEST_TMP/activation.json" '.events={activation_path:$activation,plugin_id:"org.p.filelog"}' "$host" > "$P_TEST_TMP/old.json"
expect_failure_contains 'unknown field' p plugins run-event "$P_TEST_TMP/old.json" "$event_file"

# The launch checks: a changed package, a loosened mode, a second link.
printf 'changed\n' > "$plugin_dir/extra"
expect_failure_contains 'failed its launch checks' p plugins run-event "$host" "$event_file"
rm "$plugin_dir/extra"
chmod g+w "$plugin_dir/p-event-file"
expect_failure_contains 'p-event-file is group- or world-writable' p plugins run-event "$host" "$event_file"
chmod g-w "$plugin_dir/p-event-file"
ln "$plugin_dir/plugin.json" "$P_TEST_TMP/manifest-alias"
expect_failure_contains 'plugin.json has another link while it can be written' p plugins run-event "$host" "$event_file"
rm "$P_TEST_TMP/manifest-alias"
other_digest=$(printf '%064d' 0)
select_events "$plugin_dir" "$other_digest"
expect_failure_contains "not the pinned $other_digest" p plugins run-event "$host" "$event_file"
select_events "$plugin_dir" "$digest"

old_schema="$P_TEST_TMP/old-schema"
cp -R "$plugin_dir" "$old_schema"
jq '.schema="p.plugin/v1"' "$old_schema/plugin.json" > "$P_TEST_TMP/changed-manifest.json"
mv "$P_TEST_TMP/changed-manifest.json" "$old_schema/plugin.json"
expect_failure_contains 'unsupported manifest schema "p.plugin/v1"' p plugins conformance "$old_schema"

bad_link="$P_TEST_TMP/bad-link"
cp -R "$plugin_dir" "$bad_link"
ln -s /etc/passwd "$bad_link/extra"
expect_failure_contains 'symlink in package' p plugins conformance "$bad_link"

for ((index=0; index<20; index++)); do
  p plugins run-event "$host" "$event_file" >/dev/null
done
test -f "$log_file.1"
test "$(stat -c '%s' "$log_file")" -le 1024
test "$(stat -c '%s' "$log_file.1")" -le 1024

# The handler's own checks of its log file reach the CLI as its refusal.
rm "$log_file"
ln -s "$P_TEST_TMP/private/target" "$log_file"
expect_failure_contains 'too many levels of symbolic links' p plugins run-event "$host" "$event_file"
rm "$log_file"
printf 'private\n' > "$P_TEST_TMP/private/target"
ln "$P_TEST_TMP/private/target" "$log_file"
expect_failure_contains 'unlinked elsewhere' p plugins run-event "$host" "$event_file"
rm "$log_file"
chmod 0755 "$P_TEST_TMP/private"
expect_failure_contains 'log directory must be private' p plugins run-event "$host" "$event_file"
chmod 0700 "$P_TEST_TMP/private"

echo P_PLUGIN_FOUNDATION_PASS
