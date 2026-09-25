# shellcheck shell=bash
# Reuse the proven public environment fixture with its explicit collection
# branch, including a trusted test-only Incus client cutover after deletion.
set -euo pipefail
P_COLLECTION_PROBE=1 bash -euo pipefail "$P_TEST_SOURCE/tests/integration/steps/25-public-environment.sh"
