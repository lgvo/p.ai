# shellcheck shell=bash
# Reuse the proven public environment fixture with its cache-overlap branch:
# two same-key creations whose builders overlap behind a trusted test-only
# Incus publish barrier.
set -euo pipefail
P_CACHE_OVERLAP_PROBE=1 bash -euo pipefail "$P_TEST_SOURCE/tests/integration/steps/25-public-environment.sh"
