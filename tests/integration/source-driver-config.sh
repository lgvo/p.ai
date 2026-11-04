# shellcheck shell=bash
# Print the git.driver object of a step's host configuration: the packaged
# p-source-git, pinned by its resolved path (the launch checks refuse a
# symbolic link) and its SHA-256. The driver inherits no PATH, only HOME and
# SSH_AUTH_SOCK, so its config names the git and ssh found on this PATH,
# resolved the same way.
set -euo pipefail
test "$#" -eq 0
driver=$(readlink -f -- "$(command -v p-source-git)")
digest=$(sha256sum -- "$driver" | cut -d' ' -f1)
git=$(readlink -f -- "$(command -v git)")
ssh=$(readlink -f -- "$(command -v ssh)")
test -f "$driver"
[[ $digest =~ ^[0-9a-f]{64}$ ]]
test -x "$git"
test -x "$ssh"
jq -cn --arg path "$driver" --arg sha256 "$digest" --arg git "$git" --arg ssh "$ssh" \
  '{path:$path,sha256:$sha256,config:{git:$git,ssh:$ssh}}'
