# shellcheck shell=bash
# Run the public environment scenario with its concurrent branch: two
# creations from one commit at the same time build its image once, counted by
# a trusted test-only Incus wrapper that records every image publish.
set -euo pipefail
P_CACHE_OVERLAP_PROBE=1 bash -euo pipefail "$P_TEST_SOURCE/tests/integration/steps/25-public-environment.sh"
