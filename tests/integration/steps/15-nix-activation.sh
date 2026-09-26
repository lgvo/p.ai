# shellcheck shell=bash
# Pinned Nix activation compatibility against actual guest-generated JSON.
set -E
umask 077
exec {diagnostic_fd}>&2
stage=initialization
reported=false
cleanup() {
  if test -n "${name:-}"; then
    timeout 120 incus --force-local --project user-1000 delete --force "$name" \
      >/dev/null 2>&1 || true
  fi
}
report_failure() {
  local status="$1" line="$2" command="$3" log count=0
  trap - ERR
  set +e
  reported=true
  command=${command//$'\n'/ }
  printf 'P_NIX_ACTIVATION_FAIL stage=%s line=%s status=%s command=%.200s\n' \
    "$stage" "$line" "$status" "$command" >&"$diagnostic_fd"
  # Keep the relevant host output visible after the VM deletes P_TEST_TMP.
  for log in "${step_dir:-/nonexistent}"/*.err "${step_dir:-/nonexistent}"/*.out; do
    if test -f "$log"; then
      printf 'P_NIX_ACTIVATION_LOG %s: ' "$(basename "$log")" >&"$diagnostic_fd"
      tail -c 1024 "$log" >&"$diagnostic_fd"
      printf '\n' >&"$diagnostic_fd"
      count=$((count + 1))
      if test "$count" -ge 20; then break; fi
    fi
  done
  if test -n "${name:-}"; then
    # The guest shell expands these paths after Incus exec.
    # shellcheck disable=SC2016
    timeout 8 incus --force-local --project user-1000 exec "$name" -- bash -c '
      for log in /workspace/build-*.err /workspace/capture-*.err \
        /workspace/develop-*.err /workspace/adapter-*.err \
        /workspace/invalid.err /workspace/lock.err \
        /workspace/failing-build.err; do
        if test -f "$log"; then printf "%s: " "$log"; tail -c 1024 "$log"; fi
      done' 2>&1 | tail -c 4096 >&"$diagnostic_fd" || true
    timeout 8 incus --force-local --project user-1000 info "$name" --show-log \
      2>&1 | tail -c 2048 >&"$diagnostic_fd" || true
  fi
}
on_error() {
  local status="$1"
  report_failure "$@"
  exit "$status"
}
on_exit() {
  local status="$1"
  trap - EXIT
  if test "$status" -ne 0 && test "$reported" != true; then
    report_failure "$status" exit 'explicit exit or shell expansion'
  fi
  cleanup
  exit "$status"
}
trap 'on_exit "$?"' EXIT
trap 'on_error "$?" "$LINENO" "$BASH_COMMAND"' ERR
step_dir="$P_TEST_TMP/step-15"
mkdir -m 0700 "$step_dir"
seed="$P_TEST_SOURCE/tests/integration/fixtures/nix-activation"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
name="p-nix-activation-$(cat /proc/sys/kernel/random/uuid)"
inc() { timeout 120 incus --force-local --project user-1000 "$@"; }
stage=read-image
fingerprint=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
test "${#fingerprint}" -eq 64
stage=launch
inc launch "$fingerprint" "$name" > "$step_dir/launch.out" 2> "$step_dir/launch.err"
stage=verify-instance
inc list "$name" --format json | jq -e --arg name "$name" '
  [.[] | select(.name == $name)] | length == 1' >/dev/null
inc list "$name" --format json | jq -e --arg name "$name" '
  .[] | select(.name == $name) |
  .expanded_config["security.idmap.isolated"] == "true" and
  .expanded_config["security.privileged"] != "true" and
  .expanded_config["security.nesting"] != "true" and
  (.expanded_devices | keys == ["root"]) and
  (.expanded_devices.root | .type == "disk" and .path == "/" and
    (.source // "") == "" and (.pool | type == "string" and length > 0))' >/dev/null
daemon_ready=false
stage=await-nix-daemon
for ((attempt=0; attempt<50; attempt++)); do
  if inc exec "$name" -- test -S /nix/var/nix/daemon-socket/socket 2>/dev/null; then
    daemon_ready=true
    break
  fi
  sleep 0.1
done
test "$daemon_ready" = true
stage=verify-nix-isolation-policy
inc exec "$name" -- nix --extra-experimental-features nix-command config show --json \
  > "$step_dir/nix-config.json"
jq -e '.sandbox.value == false' "$step_dir/nix-config.json" >/dev/null
stage='seed-guest'
inc exec "$name" -- mkdir -p /var/tmp/p-nix-seed /usr/local/bin /etc/p/devshell
inc exec "$name" -- chown 1000:1000 /var/tmp/p-nix-seed
for file in flake.nix probe.sh adapter-detail.sh guest.sh; do
  inc file push --uid 1000 --gid 1000 --mode 0644 "$seed/$file" \
    "$name/var/tmp/p-nix-seed/$file"
done
inc file push --uid 1000 --gid 1000 --mode 0644 "$seed/stdenv/setup" \
  "$name/var/tmp/p-nix-seed/setup"
inc file push --uid 0 --gid 0 --mode 0755 \
  "$P_TEST_NIX_FIXTURE/bin/nix-activation-fixture" \
  "$name/usr/local/bin/nix-activation-fixture"
guest() {
  inc exec "$name" --user 1000 --group 1000 --cwd /workspace \
    --env HOME=/home/p -- bash /var/tmp/p-nix-seed/guest.sh "$@"
}
stage=guest-setup
guest setup > "$step_dir/setup.out" 2> "$step_dir/setup.err"
expect_driver_failure() {
  local expected="$1"
  shift
  if inc exec "$name" -- /usr/local/bin/nix-activation-fixture "$@" \
    > "$step_dir/unexpected.out" 2> "$step_dir/rejected.err"; then
    echo "Nix activation adapter accepted invalid input: $*" >&2
    return 1
  fi
  test -s "$step_dir/rejected.err"
  test "$(wc -c < "$step_dir/rejected.err")" -lt 65536
  grep -Eiq "$expected" "$step_dir/rejected.err"
}
for variant in default structured; do
  stage="capture-$variant"
  guest capture "$variant" > "$step_dir/capture-$variant.out" \
    2> "$step_dir/capture-$variant.err"
  stage="validate-capture-$variant"
  inc file pull "$name/workspace/capture-$variant.json" "$step_dir/capture-$variant.json"
  jq -e '
    (keys | sort) == (["bashFunctions", "variables"] | sort) or
    (keys | sort) == (["bashFunctions", "structuredAttrs", "variables"] | sort)' \
    "$step_dir/capture-$variant.json" >/dev/null
  jq -e '
    (.variables.QUOTED.type == "exported" or .variables.QUOTED.type == "var") and
    .variables.plain.type == "var" and
    .variables.fixture_array.type == "array" and
    .variables.fixture_assoc.type == "associative" and
    (.bashFunctions.fixture_function | type == "string") and
    (.variables.SHOULD_UNSET == null)' \
    "$step_dir/capture-$variant.json" >/dev/null
  if test "$variant" = structured; then
    jq -e '.variables.outputs.type == "associative" and
      (.structuredAttrs[".attrs.sh"] | type == "string") and
      (.structuredAttrs[".attrs.json"] | type == "string")' \
      "$step_dir/capture-$variant.json" >/dev/null
  else
    jq -e '.variables.QUOTED.type == "exported" and
      .structuredAttrs == null and
      (.variables.outputs.type == "exported" or .variables.outputs.type == "var")' \
      "$step_dir/capture-$variant.json" >/dev/null
  fi
  stage="render-$variant"
  inc exec "$name" -- /usr/local/bin/nix-activation-fixture 2.34.8 \
    "/workspace/capture-$variant.json" /etc/p/devshell \
    > "$step_dir/render-$variant.out" 2> "$step_dir/render-$variant.err"
  inc exec "$name" -- test -f /etc/p/devshell/activation.sh
  test "$(inc exec "$name" -- stat -c %u:%g /etc/p/devshell/activation.sh)" = 0:0
  inc file pull "$name/etc/p/devshell/material.json" "$step_dir/material-$variant.json"
  capture_digest=$(sha256sum "$step_dir/capture-$variant.json")
  capture_digest=${capture_digest%% *}
  if test "$variant" = structured; then
    old_out=$(jq -er '.variables.outputs.value.out' "$step_dir/capture-$variant.json")
    jq -e --arg digest "$capture_digest" --arg old "$old_out" '
      .capture_sha256 == $digest and .has_structured_attrs == true and
      (.script | contains("/workspace/outputs/out") and (contains($old) | not)) and
      (.attrs_json | contains("/workspace/outputs/out") and (contains($old) | not))' \
      "$step_dir/material-$variant.json" >/dev/null
  else
    old_out=$(jq -er '.variables.out.value' "$step_dir/capture-$variant.json")
    jq -e --arg digest "$capture_digest" --arg old "$old_out" '
      .capture_sha256 == $digest and .has_structured_attrs == false and
      (.script | contains("/workspace/outputs/out") and (contains($old) | not))' \
      "$step_dir/material-$variant.json" >/dev/null
  fi
  stage="compare-$variant"
  guest compare "$variant" > "$step_dir/compare-$variant.out" \
    2> "$step_dir/compare-$variant.err"
  inc file pull "$name/workspace/adapter-$variant.txt" "$step_dir/adapter-$variant.txt"
  inc file pull "$name/workspace/develop-$variant.txt" "$step_dir/develop-$variant.txt"
  cmp "$step_dir/adapter-$variant.txt" "$step_dir/develop-$variant.txt"
  if test "$variant" = structured; then
    grep -Fx 'structured=yes' "$step_dir/develop-$variant.txt" >/dev/null
  else
    grep -Fx 'structured=no' "$step_dir/develop-$variant.txt" >/dev/null
  fi
done

# Mutations start from the actual capture and must fail closed in the guest.
stage=reject-invalid-capture
jq '.unrecognized = true' "$step_dir/capture-default.json" > "$step_dir/unknown-field.json"
jq '.variables.QUOTED.type = "unknown"' "$step_dir/capture-default.json" > "$step_dir/unknown-type.json"
printf '{"variables":' > "$step_dir/malformed.json"
for type in unknown-field unknown-type malformed; do
  inc file push --uid 0 --gid 0 --mode 0644 "$step_dir/$type.json" \
    "$name/var/tmp/$type.json"
  case "$type" in
    unknown-field) expected='unknown JSON field' ;;
    unknown-type) expected='unsupported variable type' ;;
    malformed) expected='EOF|unexpected end|invalid JSON' ;;
  esac
  expect_driver_failure "$expected" 2.34.8 "/var/tmp/$type.json" /etc/p/devshell
done
expect_driver_failure 'unsupported Nix version' 2.35.0 /workspace/capture-default.json /etc/p/devshell
stage=negative-cases
guest negative > "$step_dir/negative.out" 2> "$step_dir/negative.err"
echo P_NIX_ACTIVATION_PASS
