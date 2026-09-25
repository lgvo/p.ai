#!/usr/bin/env bash
set -euo pipefail
repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
fixture=$(mktemp -d -t p-vm-selection.XXXXXXXX)
cleanup() {
  : > "$fixture/release-build"
  : > "$fixture/release-runner"
  if [[ -n ${first_pid:-} ]]; then wait "$first_pid" 2>/dev/null || :; fi
  rm -rf -- "$fixture"
}
trap cleanup EXIT
mkdir -p "$fixture/bin" "$fixture/runner/bin" "$fixture/source/tests/integration/steps"

cat > "$fixture/bin/nix-build" <<'EOF'
#!/usr/bin/env bash
printf x >> "$P_TEST_BUILD_CALLS"
if [[ ${P_TEST_BLOCK_BUILD:-} == 1 ]]; then
  : > "$P_TEST_BLOCK_READY"
  while [[ ! -e "$P_TEST_RELEASE_BUILD" ]]; do sleep 0.05; done
fi
while (( $# )); do
  if [[ $1 == --argstr && ${2:-} == selectedStepsText ]]; then
    printf '%s' "$3" > "$P_TEST_SELECTION_CAPTURE"
    break
  fi
  shift
done
printf '%s\n' "$P_TEST_RUNNER"
EOF
cat > "$fixture/runner/bin/p-vm-integration-test" <<'EOF'
#!/usr/bin/env bash
if [[ ${P_TEST_BLOCK_RUNNER:-} == 1 ]]; then
  : > "$P_TEST_RUNNER_READY"
  while [[ ! -e "$P_TEST_RELEASE_RUNNER" ]]; do sleep 0.05; done
fi
echo mock-runner
EOF
cat > "$fixture/bin/id" <<'EOF'
#!/usr/bin/env bash
if [[ $1 == -un ]]; then echo pdev; else /usr/bin/id "$@"; fi
EOF
chmod +x "$fixture/bin/nix-build" "$fixture/bin/id" "$fixture/runner/bin/p-vm-integration-test"
export PATH="$fixture/bin:$PATH"
export P_TEST_BUILD_CALLS="$fixture/build-calls"
export P_TEST_SELECTION_CAPTURE="$fixture/selection"
export P_TEST_RUNNER="$fixture/runner"
export P_VM_LOG_DIR="$fixture/logs"

"$repo/dev/test-vm" > /dev/null
[[ ! -s "$P_TEST_SELECTION_CAPTURE" ]]
[[ $(wc -c < "$P_TEST_BUILD_CALLS") -eq 1 ]]

"$repo/dev/test-vm" --step 13-daemon-events.sh --step 12-attachment.sh > /dev/null
[[ $(cat "$P_TEST_SELECTION_CAPTURE") == $'12-attachment.sh\n13-daemon-events.sh' ]]
[[ $(wc -c < "$P_TEST_BUILD_CALLS") -eq 2 ]]

for args in missing duplicate unknown injection mixed_help; do
  case $args in
    missing) set -- --step ;;
    duplicate) set -- --step 12-attachment.sh --step 12-attachment.sh ;;
    unknown) set -- --step nonexistent.sh ;;
    injection) set -- --step "\$(touch $fixture/injected)" ;;
    mixed_help) set -- --step nonexistent.sh --help ;;
  esac
  if "$repo/dev/test-vm" "$@" > /dev/null 2>&1; then
    echo "Expected $args to fail" >&2
    exit 1
  fi
done
[[ $(wc -c < "$P_TEST_BUILD_CALLS") -eq 2 ]]
[[ ! -e "$fixture/injected" ]]

export P_TEST_BLOCK_READY="$fixture/build-ready"
export P_TEST_RELEASE_BUILD="$fixture/release-build"
P_TEST_BLOCK_BUILD=1 "$repo/dev/test-vm" --step 12-attachment.sh > "$fixture/first-output" &
first_pid=$!
for (( attempt=0; attempt<100; attempt++ )); do
  [[ -e "$P_TEST_BLOCK_READY" ]] && break
  sleep 0.05
done
[[ -e "$P_TEST_BLOCK_READY" ]]
if "$repo/dev/test-vm" > "$fixture/second-output" 2>&1; then
  echo 'Expected the full run to reject a concurrent selected run' >&2
  exit 1
fi
grep -Fq 'Another VM integration run is active' "$fixture/second-output"
[[ $(wc -c < "$P_TEST_BUILD_CALLS") -eq 3 ]]
: > "$P_TEST_RELEASE_BUILD"
wait "$first_pid"
first_pid=

export P_TEST_RUNNER_READY="$fixture/runner-ready"
export P_TEST_RELEASE_RUNNER="$fixture/release-runner"
P_TEST_BLOCK_RUNNER=1 "$repo/dev/test-vm" --step 12-attachment.sh > "$fixture/first-output" &
first_pid=$!
for (( attempt=0; attempt<100; attempt++ )); do
  [[ -e "$P_TEST_RUNNER_READY" ]] && break
  sleep 0.05
done
[[ -e "$P_TEST_RUNNER_READY" ]]
if "$repo/dev/test-vm" --step 13-daemon-events.sh > "$fixture/second-output" 2>&1; then
  echo 'Expected a selected run to reject a concurrent selected run' >&2
  exit 1
fi
grep -Fq 'Another VM integration run is active' "$fixture/second-output"
[[ $(wc -c < "$P_TEST_BUILD_CALLS") -eq 4 ]]
: > "$P_TEST_RELEASE_RUNNER"
wait "$first_pid"
first_pid=

export P_TEST_SOURCE="$fixture/source"
export P_TEST_EXECUTED="$fixture/executed"
for name in 01-a.sh 02-b.sh; do
  cat > "$P_TEST_SOURCE/tests/integration/steps/$name" <<'EOF'
printf '%s\n' "${BASH_SOURCE[0]##*/}" >> "$P_TEST_EXECUTED"
EOF
done
unset P_TEST_SELECTED_STEPS
bash "$repo/tests/integration/run.sh" > "$fixture/full-output"
[[ $(cat "$P_TEST_EXECUTED") == $'01-a.sh\n02-b.sh' ]]
grep -Fxq 'P_PRODUCT_INTEGRATION_PASS' "$fixture/full-output"
if grep -Fq 'P_PRODUCT_INTEGRATION_SELECTED_PASS' "$fixture/full-output"; then exit 1; fi

: > "$P_TEST_EXECUTED"
export P_TEST_SELECTED_STEPS=$'02-b.sh\n01-a.sh'
bash "$repo/tests/integration/run.sh" > "$fixture/selected-output"
[[ $(cat "$P_TEST_EXECUTED") == $'02-b.sh\n01-a.sh' ]]
grep -Fxq 'P_PRODUCT_INTEGRATION_SELECTED_PASS 02-b.sh,01-a.sh' "$fixture/selected-output"
if grep -Fxq 'P_PRODUCT_INTEGRATION_PASS' "$fixture/selected-output"; then exit 1; fi

export P_TEST_SELECTED_STEPS=missing.sh
if bash "$repo/tests/integration/run.sh" > "$fixture/missing-output" 2>&1; then
  echo 'Expected a missing selected step to fail' >&2
  exit 1
fi
if grep -Fq 'P_PRODUCT_INTEGRATION_SELECTED_PASS' "$fixture/missing-output"; then exit 1; fi

rm "$P_TEST_SOURCE/tests/integration/steps/01-a.sh" "$P_TEST_SOURCE/tests/integration/steps/02-b.sh"
unset P_TEST_SELECTED_STEPS
if bash "$repo/tests/integration/run.sh" > "$fixture/empty-output" 2>&1; then
  echo 'Expected an empty full suite to fail' >&2
  exit 1
fi
if grep -Fq 'P_PRODUCT_INTEGRATION_PASS' "$fixture/empty-output"; then exit 1; fi

# Exercise the exact marker check that the Nix runner embeds.
source "$repo/dev/vm/verify-markers.sh"
printf 'P_VM_SMOKE_PASS\r\nP_PRODUCT_INTEGRATION_SELECTED_PASS 12-attachment.sh\r\n' > "$fixture/vm-log"
vm_log_has_marker "$fixture/vm-log" P_VM_SMOKE_PASS
vm_log_has_marker "$fixture/vm-log" 'P_PRODUCT_INTEGRATION_SELECTED_PASS 12-attachment.sh'
if vm_log_has_marker "$fixture/vm-log" P_PRODUCT_INTEGRATION_PASS; then exit 1; fi
printf 'P_VM_SMOKE_PASS\nP_PRODUCT_INTEGRATION_PASS\n' > "$fixture/vm-log"
vm_log_has_marker "$fixture/vm-log" P_VM_SMOKE_PASS
vm_log_has_marker "$fixture/vm-log" P_PRODUCT_INTEGRATION_PASS
if vm_log_has_marker "$fixture/vm-log" 'P_PRODUCT_INTEGRATION_SELECTED_PASS 12-attachment.sh'; then exit 1; fi
printf 'PREFIX_P_VM_SMOKE_PASS\nP_PRODUCT_INTEGRATION_SELECTED_PASS 12-attachment.sh_EXTRA\n' > "$fixture/vm-log"
if vm_log_has_marker "$fixture/vm-log" P_VM_SMOKE_PASS; then exit 1; fi
if vm_log_has_marker "$fixture/vm-log" 'P_PRODUCT_INTEGRATION_SELECTED_PASS 12-attachment.sh'; then exit 1; fi
echo 'VM selection unit checks passed.'
