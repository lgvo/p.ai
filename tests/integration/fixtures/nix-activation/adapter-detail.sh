#!/usr/bin/env bash
# Source in the same Bash process as activation to inspect shell-local state.
# Captured variables are defined by the generated activation script.
# shellcheck disable=SC2154
set -eu
variant=${1:?variant}
test "$plain" = "unexported with spaces and 'single' quote"
[[ $(declare -p plain) == 'declare -- '* ]]
test "$(fixture_function 'two words')" = 'function:two words'
test "${fixture_array[0]}" = 'one word'
test "${fixture_array[1]}" = 'quote'\''"two'
test "${fixture_array[2]}" = last
test "${fixture_assoc['space key']}" = "a'b"
test "${fixture_assoc[plain]}" = 'double "quote"'
if test "$variant" = structured; then
  test "$NIX_ATTRS_SH_FILE" = /etc/p/devshell/.attrs.sh
  test "$NIX_ATTRS_JSON_FILE" = /etc/p/devshell/.attrs.json
  test -f "$NIX_ATTRS_SH_FILE"
  test -f "$NIX_ATTRS_JSON_FILE"
  test "${outputs[out]}" = /workspace/outputs/out
  test "${outputs[dev]}" = /workspace/outputs/dev
else
  test -z "${NIX_ATTRS_SH_FILE-}"
  test -z "${NIX_ATTRS_JSON_FILE-}"
  test "$out" = /workspace/outputs/out
  test "$dev" = /workspace/outputs/dev
fi
