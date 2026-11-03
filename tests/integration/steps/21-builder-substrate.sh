# shellcheck shell=bash
# Native restricted builder only: the commit's bundle, its clone and real quota.
set -E
umask 077
trap 'printf "P_BUILDER_SUBSTRATE_FAIL line=%s status=%s\n" "$LINENO" "$?" >&2' ERR
timeout 420 builder-fixture "$(cat "$P_TEST_RUNTIME_IMAGE_FILE")"
