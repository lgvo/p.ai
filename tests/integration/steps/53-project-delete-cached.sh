# shellcheck shell=bash
# A commit image with real Nix realization from offline fixture source, deleted
# with its project; no login.
P_PROJECT_DELETE_CACHED=1
export P_PROJECT_DELETE_CACHED
source "$P_TEST_SOURCE/tests/integration/steps/52-project-delete.sh"
