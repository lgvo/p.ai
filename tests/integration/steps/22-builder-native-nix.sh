# shellcheck shell=bash
# Closed offline native Nix selection, realization and activation capture,
# from a clone of the commit's bundle.
set -E
umask 077
trap 'printf "P_BUILDER_NATIVE_NIX_FAIL line=%s status=%s\n" "$LINENO" "$?" >&2' ERR
timeout 600 builder-fixture "$(cat "$P_TEST_RUNTIME_IMAGE_FILE")" nix
