# shellcheck shell=bash
# Real configured daemon, the runtime driver's session host, and two Incus
# guests.
set -E
umask 077
step_dir="$P_TEST_TMP/step-12"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step12
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user

fingerprint=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
test "${#fingerprint}" -eq 64
incus_binary=$(command -v incus)
driver=$(bash "$P_TEST_SOURCE/tests/integration/runtime-driver-config.sh" "$incus_binary")
source_driver=$(bash "$P_TEST_SOURCE/tests/integration/source-driver-config.sh")
jq -n --arg state "$state" --argjson source_driver "$source_driver" \
  --argjson driver "$driver" --arg prefix "$endpoint_prefix" \
  --arg image "$fingerprint" \
  '{schema:"p.host/v1",state_dir:$state,
   git:{listen:"127.0.0.1:0",driver:$source_driver},
   runtime:{driver:$driver,
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
