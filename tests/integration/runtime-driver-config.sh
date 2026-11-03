# shellcheck shell=bash
# Print the runtime.driver object of a step's host configuration: the packaged
# p-runtime-incus, pinned by its resolved path (the launch checks refuse a
# symbolic link) and its SHA-256, configured for the VM's confined Incus with
# the Incus binary given as the only argument. The driver runs with an empty
# environment, so its config names the journalctl found on this PATH.
set -euo pipefail
test "$#" -eq 1
incus_binary=$1
executable=$(command -v p-runtime-incus)
driver=$(readlink -f -- "$executable")
digest=$(sha256sum -- "$driver" | cut -d' ' -f1)
journalctl=$(command -v journalctl)
test -n "$incus_binary"
test -f "$driver"
[[ $digest =~ ^[0-9a-f]{64}$ ]]
[[ $journalctl == /* ]]
jq -cn --arg path "$driver" --arg sha256 "$digest" --arg binary "$incus_binary" \
  --arg journalctl "$journalctl" \
  '{path:$path,sha256:$sha256,config:{binary:$binary,
    user_socket:"/var/lib/incus/unix.socket.user",project:"user-1000",
    journalctl:$journalctl}}'
