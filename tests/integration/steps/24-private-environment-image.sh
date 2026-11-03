# shellcheck shell=bash
# A commit image built end to end by the Incus image driver, two private roots
# from it, and its delete.
set -E
umask 077
trap 'printf "P_BUILDER_PRIVATE_IMAGE_FAIL line=%s status=%s\n" "$LINENO" "$?" >&2' ERR
timeout 1200 builder-fixture "$(cat "$P_TEST_RUNTIME_IMAGE_FILE")" nix-image
