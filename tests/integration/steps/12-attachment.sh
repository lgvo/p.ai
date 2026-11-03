# shellcheck shell=bash
# Real configured daemon, selected WASI runtime/host, and two Incus guests.
set -E
umask 077
step_dir="$P_TEST_TMP/step-12"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step12
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user

activate() {
  local package="$1" target="$2" grant="$3" digest id
  digest=$(p plugins conformance "$package" | jq -er '.sha256 | select(type=="string" and length==64)')
  id=$(jq -er '.id | select(type=="string" and length>0)' "$package/plugin.json")
  jq -n --arg path "$package" --arg digest "$digest" --arg id "$id" --arg grant "$grant" \
    '{schema:"p.activation/v1",plugins:[{id:$id,path:$path,sha256:$digest,grants:[$grant],config:{}}]}' > "$target"
}
activate "$P_TEST_GIT_PLUGIN" "$step_dir/git.json" git.project
activate "$P_TEST_SOURCE/plugins/bundled/tmux-host" "$step_dir/runtime.json" session.asset.install
p plugins activate "$step_dir/git.json" | jq -e '.[0].capability=="source-git"' >/dev/null
p plugins activate "$step_dir/runtime.json" | jq -e 'length==1' >/dev/null
fingerprint=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
test "${#fingerprint}" -eq 64
host_id=$(jq -er '.plugins[0].id' "$step_dir/runtime.json")
git_id=$(jq -er '.plugins[0].id' "$step_dir/git.json")
incus_binary=$(command -v incus)
driver=$(bash "$P_TEST_SOURCE/tests/integration/runtime-driver-config.sh" "$incus_binary")
jq -n --arg state "$state" --arg git_activation "$step_dir/git.json" \
  --arg git_id "$git_id" --arg runtime_activation "$step_dir/runtime.json" \
  --arg host_id "$host_id" \
  --argjson driver "$driver" --arg prefix "$endpoint_prefix" \
  --arg image "$fingerprint" \
  '{schema:"p.host/v1",state_dir:$state,
   git:{activation_path:$git_activation,source_plugin_id:$git_id,listen:"127.0.0.1:0",driver:{name:"git",config:{}}},
   runtime:{activation_path:$runtime_activation,
    host_plugin_id:$host_id,driver:$driver,
    endpoint_prefix:$prefix,disk_source_ceilings:["/var/lib/p-vm/endpoints","/var/lib/p-vm/grants"],
    base_image_fingerprint:$image,
    project_policy:{network:"none",filesystem_mounts:[],command:["/run/current-system/sw/bin/bash"]}}}' \
  > "$step_dir/host.json"
bash "$P_TEST_SOURCE/tests/integration/public-egress-host-config.sh" "$step_dir/host.json"

# The driver owns daemon and guest cleanup, including failures. A final host
# cleanup catches an interruption before it can start and keeps the VM tidy.
trap 'rmdir "$endpoint_prefix" 2>/dev/null || true' EXIT
attachment-fixture "$step_dir/host.json" "$state/control.sock" "$endpoint_prefix" "$step_dir"
echo P_ATTACHMENT_PASS
