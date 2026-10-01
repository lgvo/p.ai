# shellcheck shell=bash
# Real Nix realization/cache cleanup using offline fixture source, no login.
P_PROJECT_DELETE_CACHED=1
export P_PROJECT_DELETE_CACHED
source "$P_TEST_SOURCE/tests/integration/steps/52-project-delete.sh"
