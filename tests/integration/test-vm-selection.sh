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
if flock -n "$P_TEST_GLOBAL_LOCK" true; then
  echo 'Build ran without the checkout lock' >&2
  exit 1
fi
printf x >> "$P_TEST_BUILD_CALLS"
call=$(wc -c < "$P_TEST_BUILD_CALLS")
if [[ ${P_TEST_BLOCK_BUILD:-} == 1 ]]; then
  : > "$P_TEST_BLOCK_READY"
  while [[ ! -e "$P_TEST_RELEASE_BUILD" ]]; do sleep 0.05; done
fi
while (( $# )); do
  if [[ $1 == --argstr && ${2:-} == selectedStepsText ]]; then
    printf '%s' "$3" > "$P_TEST_SELECTION_CAPTURE"
  fi
  if [[ $1 == --argstr && ${2:-} == outerHostIPv4Text ]]; then
    printf '%s' "$3" > "$P_TEST_HOST_CAPTURE"
  fi
  if [[ $1 == --argstr && ${2:-} == outerHostLANIPv4Text ]]; then
    printf '%s' "$3" > "$P_TEST_LAN_CAPTURE"
  fi
  shift
done
cp "$P_TEST_SELECTION_CAPTURE" "$P_TEST_SELECTION_CAPTURE.$call"
cp "$P_TEST_HOST_CAPTURE" "$P_TEST_HOST_CAPTURE.$call"
printf '%s\n' "$P_TEST_RUNNER"
EOF
cat > "$fixture/bin/ip" <<'EOF'
#!/usr/bin/env bash
[[ ${P_TEST_IP_FAIL:-0} != 1 ]] || exit 1
if [[ $* == '-o -4 address show' ]]; then
  printf '1: lo    inet 127.0.0.1/8 scope host lo\n2: eth0    inet 198.41.0.7/24 scope global eth0\n'
elif [[ $* == '-o -4 route show table all scope link type unicast' ]]; then
  [[ ${P_TEST_ROUTE_FAIL:-0} != 1 ]] || exit 1
  printf '198.41.0.0/24 dev eth0 proto kernel scope link src 198.41.0.7\n198.42.0.0/24 dev eth1 table 100 scope link\n'
else
  exit 1
fi
EOF
cat > "$fixture/runner/bin/p-vm-integration-test" <<'EOF'
#!/usr/bin/env bash
if flock -n "$P_TEST_GLOBAL_LOCK" true; then
  echo 'Guest ran without the checkout lock' >&2
  exit 1
fi
printf x >> "$P_TEST_RUNNER_CALLS"
if [[ ${P_TEST_FAIL_RUNNER_CALL:-0} == "$(wc -c < "$P_TEST_RUNNER_CALLS")" ]]; then exit 17; fi
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
chmod +x "$fixture/bin/nix-build" "$fixture/bin/id" "$fixture/bin/ip" "$fixture/runner/bin/p-vm-integration-test"
export PATH="$fixture/bin:$PATH"
export P_TEST_BUILD_CALLS="$fixture/build-calls"
export P_TEST_SELECTION_CAPTURE="$fixture/selection"
export P_TEST_HOST_CAPTURE="$fixture/host-addresses"
export P_TEST_LAN_CAPTURE="$fixture/host-lan"
export P_TEST_RUNNER="$fixture/runner"
export P_TEST_RUNNER_CALLS="$fixture/runner-calls"
export P_TEST_GLOBAL_LOCK="$repo/.cache/p-vm/integration.lock"
export P_VM_LOG_DIR="$fixture/logs"

"$repo/dev/test-vm" > "$fixture/grouped-full-output"
mapfile -t first_group < "$P_TEST_SELECTION_CAPTURE.1"
mapfile -t last_group < "$P_TEST_SELECTION_CAPTURE.3"
[[ ${#first_group[@]} -eq 37 && ${first_group[0]} == 01-* && ${first_group[-1]} == 36-* ]]
[[ $(cat "$P_TEST_SELECTION_CAPTURE.2") == 37-public-egress.sh ]]
[[ ${#last_group[@]} -eq 17 && ${last_group[0]} == 38-* && ${last_group[-1]} == 55-* ]]
[[ ! -s "$P_TEST_HOST_CAPTURE.1" && ! -s "$P_TEST_HOST_CAPTURE.3" ]]
[[ $(cat "$P_TEST_HOST_CAPTURE.2") == $'127.0.0.1\n198.41.0.7' ]]
[[ ! -s "$P_TEST_HOST_CAPTURE" && ! -s "$P_TEST_LAN_CAPTURE" ]]
[[ $(wc -c < "$P_TEST_BUILD_CALLS") -eq 3 ]]
[[ $(wc -c < "$P_TEST_RUNNER_CALLS") -eq 3 ]]
grep -Fxq P_PRODUCT_INTEGRATION_PASS "$fixture/grouped-full-output"

"$repo/dev/test-vm" --step 13-daemon-events.sh --step 12-attachment.sh > /dev/null
[[ $(cat "$P_TEST_SELECTION_CAPTURE") == $'12-attachment.sh\n13-daemon-events.sh' ]]
[[ $(wc -c < "$P_TEST_BUILD_CALLS") -eq 4 ]]
[[ ! -s "$P_TEST_HOST_CAPTURE" ]]
[[ ! -s "$P_TEST_LAN_CAPTURE" ]]

"$repo/dev/test-vm" --step 37-public-egress.sh > /dev/null
[[ $(cat "$P_TEST_HOST_CAPTURE") == $'127.0.0.1\n198.41.0.7' ]]
if P_TEST_IP_FAIL=1 "$repo/dev/test-vm" --step 37-public-egress.sh > /dev/null 2>&1; then
  echo 'Expected unavailable host inventory to fail closed' >&2
  exit 1
fi
if P_TEST_ROUTE_FAIL=1 "$repo/dev/test-vm" --step 37-public-egress.sh > /dev/null 2>&1; then
  echo 'Expected unavailable LAN inventory to fail closed' >&2
  exit 1
fi

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
[[ $(wc -c < "$P_TEST_BUILD_CALLS") -eq 5 ]]
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
[[ $(wc -c < "$P_TEST_BUILD_CALLS") -eq 6 ]]
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
[[ $(wc -c < "$P_TEST_BUILD_CALLS") -eq 7 ]]
: > "$P_TEST_RELEASE_RUNNER"
wait "$first_pid"
first_pid=

# Mixed requests also isolate public routing and never emit the full marker.
: > "$P_TEST_BUILD_CALLS"
: > "$P_TEST_RUNNER_CALLS"
"$repo/dev/test-vm" --step 55-nixos-service.sh --step 37-public-egress.sh --step 36-filesystem-grants.sh > "$fixture/grouped-selected-output"
[[ $(wc -c < "$P_TEST_BUILD_CALLS") -eq 3 ]]
[[ $(cat "$P_TEST_SELECTION_CAPTURE.1") == 36-filesystem-grants.sh ]]
[[ $(cat "$P_TEST_SELECTION_CAPTURE.2") == 37-public-egress.sh ]]
[[ $(cat "$P_TEST_SELECTION_CAPTURE.3") == 55-nixos-service.sh ]]
[[ ! -s "$P_TEST_HOST_CAPTURE.1" && -s "$P_TEST_HOST_CAPTURE.2" && ! -s "$P_TEST_HOST_CAPTURE.3" ]]
if grep -Fxq P_PRODUCT_INTEGRATION_PASS "$fixture/grouped-selected-output"; then exit 1; fi

# A failed second guest stops the checkpoint before a third build or pass marker.
: > "$P_TEST_BUILD_CALLS"
: > "$P_TEST_RUNNER_CALLS"
status=0
P_TEST_FAIL_RUNNER_CALL=2 "$repo/dev/test-vm" > "$fixture/grouped-failure-output" 2>&1 || status=$?
[[ $status -eq 17 ]]
[[ $(wc -c < "$P_TEST_BUILD_CALLS") -eq 2 ]]
[[ $(wc -c < "$P_TEST_RUNNER_CALLS") -eq 2 ]]
if grep -Fxq P_PRODUCT_INTEGRATION_PASS "$fixture/grouped-failure-output"; then exit 1; fi

export P_TEST_SOURCE="$fixture/source"
export P_TEST_EXECUTED="$fixture/executed"
export P_TEST_CHILD_STDIN="$fixture/child-stdin"
for name in 01-a.sh 02-b.sh; do
  cat > "$P_TEST_SOURCE/tests/integration/steps/$name" <<'EOF'
cat >> "$P_TEST_CHILD_STDIN"
printf '%s\n' "${BASH_SOURCE[0]##*/}" >> "$P_TEST_EXECUTED"
EOF
done
unset P_TEST_SELECTED_STEPS
bash "$repo/tests/integration/run.sh" > "$fixture/full-output"
[[ $(cat "$P_TEST_EXECUTED") == $'01-a.sh\n02-b.sh' ]]
[[ ! -s "$P_TEST_CHILD_STDIN" ]]
grep -Fxq 'P_PRODUCT_INTEGRATION_PASS' "$fixture/full-output"
if grep -Fq 'P_PRODUCT_INTEGRATION_SELECTED_PASS' "$fixture/full-output"; then exit 1; fi

: > "$P_TEST_EXECUTED"
export P_TEST_SELECTED_STEPS=$'02-b.sh\n01-a.sh'
bash "$repo/tests/integration/run.sh" > "$fixture/selected-output"
[[ $(cat "$P_TEST_EXECUTED") == $'02-b.sh\n01-a.sh' ]]
[[ ! -s "$P_TEST_CHILD_STDIN" ]]
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
