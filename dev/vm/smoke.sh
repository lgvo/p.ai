# Executed by writeShellApplication as the confined pdev account.
if [ "$(id -un)" != pdev ]; then
  echo "Run this test as pdev, not the VM administrator." >&2
  exit 1
fi

fail() { echo "FAIL: $*" >&2; exit 1; }
deny() {
  local output
  if output=$("$@" 2>&1); then
    fail "undeclared authority was accepted: $*"
  fi
  # Do not count an unreachable daemon or malformed request as denial evidence.
  if ! grep -Eiq 'not (allowed|authorized|permitted)|forbidden|permission denied|restricted|not have permission' <<<"$output"; then
    fail "expected an authorization rejection from '$*', got: $output"
  fi
  echo "PASS denied: $*"
}
wait_ready() {
  local attempt
  for ((attempt = 0; attempt < 90; attempt++)); do
    if incus exec "$1" -- systemctl is-active multi-user.target >/dev/null 2>&1; then
      return
    fi
    sleep 1
  done
  incus info "$1" --show-log >&2
  fail "container $1 did not reach multi-user.target"
}

echo "Testing $(uname -srmo)"
incus version
test ! -w /var/lib/incus/unix.socket || fail "administrative socket is writable"
incus project list --format json | jq -e 'map(.name) == ["user-1000"]'
deny incus project create p-escape-probe
deny incus config set core.https_address :8443

# Unique resources ensure reruns never remove a developer's existing instances.
probe="p-smoke-$(date +%s)-$$"
first="$probe-a"
second="$probe-b"
cleanup() {
  incus delete --force "$first" >/dev/null 2>&1 || true
  incus delete --force "$second" >/dev/null 2>&1 || true
}
trap cleanup EXIT

incus init p-lab-base "$first"
incus init p-lab-base "$second"
deny incus config set "$first" security.privileged=true
deny incus config set "$first" security.nesting=true
deny incus config set "$first" raw.lxc=lxc.apparmor.profile=unconfined
deny incus config device add "$first" forbidden disk source=/etc path=/host-etc
deny incus config device add "$first" forbidden nic nictype=bridged parent=incusbr0 name=eth0

incus start "$first" "$second"
wait_ready "$first"
wait_ready "$second"
for instance in "$first" "$second"; do
  # Expand these expressions inside the container, not in the VM shell.
  # shellcheck disable=SC2016
  incus exec "$instance" -- sh -eu -c '
    test "$(ls /sys/class/net)" = lo
    test ! -S /var/lib/incus/unix.socket
    test ! -S /var/lib/incus/unix.socket.user
    test "$(stat -c %u /workspace)" = 1000
    systemctl is-active nix-daemon.socket
  '
  incus list "^$instance$" --format json | jq -e '
    length == 1 and (.[0] |
      .expanded_config["security.idmap.isolated"] == "true" and
      ([.expanded_devices[] | select(.type == "disk")] | length == 1) and
      ([.expanded_devices[] | select(.type == "nic")] | length == 0))'
done

incus exec "$first" --user 1000 --group 100 -- sh -eu -c '
  printf "retained\n" > /workspace/probe
  git -C /workspace init -b main
  git -C /workspace -c user.name=Smoke -c user.email=smoke@example.invalid add probe
  git -C /workspace -c user.name=Smoke -c user.email=smoke@example.invalid commit -m probe
  tmux -L p-smoke new-session -d -s work "sleep 3600"
  tmux -L p-smoke has-session -t work
  nix-store --add /workspace/probe > /workspace/store-path
'
incus exec "$second" -- test ! -e /workspace/probe
store_path=$(incus exec "$first" -- cat /workspace/store-path)
incus exec "$second" -- test ! -e "$store_path"
incus exec "$first" --user 1000 --group 100 -- tmux -L p-smoke has-session -t work
incus stop "$first" --timeout 30
incus start "$first"
wait_ready "$first"
incus exec "$first" -- grep -qx retained /workspace/probe
incus exec "$first" -- test -e "$store_path"
if incus exec "$first" --user 1000 --group 100 -- tmux -L p-smoke has-session -t work 2>/dev/null; then
  fail "old tmux process survived container stop/start"
fi
incus delete --force "$first"
incus exec "$second" -- systemctl is-active multi-user.target
echo "PASS: confined Incus, two private containers, Git, tmux, private Nix store, stop/start, removal"
