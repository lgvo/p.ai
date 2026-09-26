# shellcheck shell=bash
# Selected executable environment capability against the real restricted builder.
set -E
umask 077
step_dir="$P_TEST_TMP/step-23"
mkdir -m 0700 "$step_dir"
trap 'printf "P_BUILDER_ENV_WASI_FAIL line=%s status=%s\n" "$LINENO" "$?" >&2' ERR
git_digest=$(p plugins conformance "$P_TEST_GIT_PLUGIN" | jq -er '.sha256')
git_id=$(jq -er '.id' "$P_TEST_GIT_PLUGIN/plugin.json")
jq -n --arg path "$P_TEST_GIT_PLUGIN" --arg digest "$git_digest" --arg id "$git_id" \
  '{schema:"p.activation/v1",plugins:[{id:$id,path:$path,sha256:$digest,grants:["git.project"],config:{}}]}' \
  > "$step_dir/source-git.json"
env_digest=$(p plugins conformance "$P_TEST_ENV_PLUGIN" | jq -er '.sha256')
env_id=$(jq -er '.id' "$P_TEST_ENV_PLUGIN/plugin.json")
jq -n --arg path "$P_TEST_ENV_PLUGIN" --arg digest "$env_digest" --arg id "$env_id" \
  '{schema:"p.activation/v1",plugins:[{id:$id,path:$path,sha256:$digest,grants:["environment.nix"],config:{}}]}' \
  > "$step_dir/environment.json"
timeout 600 builder-fixture "$step_dir/source-git.json" "$(cat "$P_TEST_RUNTIME_IMAGE_FILE")" \
  "$step_dir/environment.json" nix-wasi
