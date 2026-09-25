# shellcheck shell=bash
# Runs inside the disposable VM as the confined pdev account.
set -euo pipefail
test "$(id -un)" = pdev
export P_TEST_TMP
P_TEST_TMP=$(mktemp -d -t p-product-test.XXXXXXXX)
trap 'rm -rf -- "$P_TEST_TMP"' EXIT
echo "P product integration: $(uname -srmo)"
run_step() {
  local step=$1
  test -f "$step"
  echo "Running $(basename "$step")"
  bash -euo pipefail "$step"
}
if [[ -n ${P_TEST_SELECTED_STEPS:-} ]]; then
  while IFS= read -r step_name; do
    run_step "$P_TEST_SOURCE/tests/integration/steps/$step_name"
  done <<< "$P_TEST_SELECTED_STEPS"
  echo "P_PRODUCT_INTEGRATION_SELECTED_PASS ${P_TEST_SELECTED_STEPS//$'\n'/,}"
else
  for step in "$P_TEST_SOURCE"/tests/integration/steps/*.sh; do
    run_step "$step"
  done
  echo P_PRODUCT_INTEGRATION_PASS
fi
