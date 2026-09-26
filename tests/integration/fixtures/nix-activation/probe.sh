#!/usr/bin/env bash
# Run as a child of either nix develop or the rendered activation. Only
# exported values survive the command boundary in both cases.
set -eu
test ! "${SHOULD_UNSET+x}"
test -f /tmp/p-nix-hook-marker
test "$(cat /tmp/p-nix-hook-marker)" = hook
test "$HOOK_VALUE" = 'hook ran'
test "$HOOK_OUT" = /workspace/outputs/out
test "$QUOTED" = 'double "quote", single '\''quote'\'', colon: and spaces'
test -z "${NIX_ATTRS_SH_FILE-}" || test -f "$NIX_ATTRS_SH_FILE"
test -z "${NIX_ATTRS_JSON_FILE-}" || test -f "$NIX_ATTRS_JSON_FILE"
printf 'QUOTED=%q\nHOOK_OUT=%q\nHOOK_VALUE=%q\n' \
  "$QUOTED" "$HOOK_OUT" "$HOOK_VALUE"
printf 'quoted-declaration=%s\n' "$(declare -p QUOTED)"
if test -n "${NIX_ATTRS_SH_FILE-}"; then
  printf 'structured=yes\n'
else
  printf 'structured=no\n'
fi
