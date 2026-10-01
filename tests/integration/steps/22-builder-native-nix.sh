# shellcheck shell=bash
# Closed offline native Nix selection, realization and activation capture.
set -E
umask 077
step_dir="$P_TEST_TMP/step-22"
mkdir -m 0700 "$step_dir"
trap 'printf "P_BUILDER_NATIVE_NIX_FAIL line=%s status=%s\n" "$LINENO" "$?" >&2' ERR
digest=$(p plugins conformance "$P_TEST_GIT_PLUGIN" | jq -er '.sha256')
id=$(jq -er '.id' "$P_TEST_GIT_PLUGIN/plugin.json")
jq -n --arg path "$P_TEST_GIT_PLUGIN" --arg digest "$digest" --arg id "$id" \
  '{schema:"p.activation/v1",plugins:[{id:$id,path:$path,sha256:$digest,grants:["git.project"],config:{}}]}' \
  > "$step_dir/source-git.json"
timeout 600 builder-fixture "$step_dir/source-git.json" "$(cat "$P_TEST_RUNTIME_IMAGE_FILE")" nix
