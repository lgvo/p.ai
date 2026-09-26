# shellcheck shell=bash
# Production image and selected host assets, assembled by a fixture only.
# Public project/session creation and attachment leases are later steps.
set -E
umask 077
step_dir="$P_TEST_TMP/step-05"
mkdir -m 0700 "$step_dir"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
names=("p-host-$BASHPID-a" "p-host-$BASHPID-b")
endpoint_prefix=/var/lib/p-vm/endpoints/pdev
pids=()
inc() { timeout 45 incus --force-local --project user-1000 "$@"; }
exec_p() {
  local name="$1"
  shift
  inc exec "$name" --user 1000 --group 1000 --cwd /workspace \
    --env HOME=/home/p --env GIT_SSH=/usr/libexec/p/git-ssh -- "$@"
}
cleanup() {
  for name in "${names[@]}"; do inc delete --force "$name" >/dev/null 2>&1 || true; done
  for pid in "${pids[@]}"; do kill "$pid" 2>/dev/null || true; done
  for pid in "${pids[@]}"; do wait "$pid" 2>/dev/null || true; done
  for name in "${names[@]}"; do rm -rf -- "${endpoint_prefix:?}/${name:?}"; done
}
diagnostic() {
  printf 'P_RUNTIME_HOST_FAIL line=%s status=%s\n' "$2" "$1" >&2
  for name in "${names[@]}"; do
    stat -c 'HOST_ENDPOINT %n uid=%u gid=%g mode=%a type=%F' "$endpoint_prefix" \
      "$endpoint_prefix/$name" "$endpoint_prefix/$name/session.sock" "$endpoint_prefix/$name/git.sock" >&2 || true
    inc info "$name" --show-log 2>&1 | tail -c 2048 >&2 || true
    inc exec "$name" -- journalctl -u p-interactive.service -n 20 --no-pager 2>&1 | tail -c 3072 >&2 || true
    inc console "$name" --show-log 2>&1 | tail -c 4096 >&2 || true
    # File access works while stopped; never reboot merely to read diagnostics.
    machine_id=$(inc file pull "$name/etc/machine-id" - 2>/dev/null) || continue
    if [[ "$machine_id" =~ ^[0-9a-f]{32}$ ]]; then
      for journal_name in system user-1000; do
        if inc file pull "$name/var/log/journal/$machine_id/$journal_name.journal" "$step_dir/$name-$journal_name.journal"; then
          journalctl --file "$step_dir/$name-$journal_name.journal" -u p-interactive.service -n 30 --no-pager | tail -c 4096 >&2 || true
        fi
      done
    fi
  done
  tail -c 1024 "$step_dir/git.err" >&2 || true
}
trap cleanup EXIT
trap 'diagnostic "$?" "$LINENO"' ERR
expect_failure() {
  if "$@" >"$step_dir/unexpected.out" 2>"$step_dir/expected.err"; then
    printf 'Unexpected runtime success: %s\n' "$*" >&2
    return 1
  fi
}
wait_state() {
  local name="$1" expected="$2" state=unknown deadline=$((SECONDS+45))
  while test "$SECONDS" -lt "$deadline"; do
    state=$(timeout 5 incus --force-local --project user-1000 list "$name" --format json | jq -r --arg n "$name" '.[] | select(.name==$n) | .status')
    if test "$state" = "$expected"; then return 0; fi
    sleep 0.1
  done
  printf 'Instance %s failed to reach %s (last %s)\n' "$name" "$expected" "$state" >&2
  return 1
}
run_after_self_shutdown() {
  local name="$1" action="$2" output state command_status deadline retry
  shift 2
  wait_state "$name" Stopped
  deadline=$((SECONDS+45))
  while test "$SECONDS" -lt "$deadline"; do
    state=$(timeout 5 incus --force-local --project user-1000 list "$name" --format json \
      | jq -r --arg n "$name" '.[] | select(.name==$n) | .status')
    if test "$state" = Stopped; then
      if output=$(inc "$@" 2>&1); then
        if test -n "$output"; then printf '%s\n' "$output"; fi
        return 0
      else
        command_status=$?
      fi
      # The guest's stop hook releases its private Incus operation lock only
      # after device and storage cleanup. These exact errors reject the new
      # operation before it can make a change; no other failure is replayed.
      retry=false
      if test "$command_status" -eq 1; then
        case "$action:$output" in
          'start:Error: The instance is already running'|\
          'start:Error: Failed to create instance start operation: Instance is busy running a "stop" operation'|\
          'update:Error: Failed to create instance update operation: Instance is busy running a "stop" operation')
            retry=true ;;
        esac
      fi
      if test "$retry" != true; then
        printf 'Instance %s %s failed (status %s): %s\n' "$name" "$action" "$command_status" "$output" >&2
        return 1
      fi
    fi
    sleep 0.1
  done
  printf 'Instance %s stayed unavailable for %s after self-shutdown (last state %s)\n' "$name" "$action" "$state" >&2
  return 1
}
start_after_self_shutdown() {
  run_after_self_shutdown "$1" start start "$1"
}
wait_ready() {
  local name="$1" deadline=$((SECONDS+45))
  while test "$SECONDS" -lt "$deadline"; do
    if timeout 5 incus --force-local --project user-1000 exec "$name" -- systemctl is-active --quiet p-interactive.service > "$step_dir/last-ready-probe" 2>&1; then return 0; fi
    if timeout 5 incus --force-local --project user-1000 list "$name" --format json \
      | jq -e --arg n "$name" 'any(.[]; .name==$n and .status=="Stopped")' >/dev/null; then return 1; fi
    sleep 0.1
  done
  return 1
}
push_root() {
  local name="$1" source="$2" destination="$3" mode="$4"
  inc file push --create-dirs --uid 0 --gid 0 --mode "$mode" "$source" "$name$destination"
}
activate_package() {
  local package="$1" target="$2" grant="$3" digest id
  digest=$(p plugins conformance "$package" | jq -er '.sha256')
  id=$(jq -er '.id' "$package/plugin.json")
  jq -n --arg path "$package" --arg digest "$digest" --arg id "$id" --arg grant "$grant" \
    '{schema:"p.activation/v1",plugins:[{id:$id,path:$path,sha256:$digest,grants:[$grant],config:{}}]}' > "$target"
}
host_package="$P_TEST_SOURCE/plugins/bundled/tmux-host"
activate_package "$host_package" "$step_dir/host.json" session.asset.install
p plugins plan-assets "$step_dir/host.json" "$(jq -er '.plugins[0].id' "$step_dir/host.json")" > "$step_dir/assets.json"
activate_package "$P_TEST_GIT_PLUGIN" "$step_dir/git.json" git.project
p plugins plan-assets "$step_dir/git.json" "$(jq -er '.plugins[0].id' "$step_dir/git.json")" > "$step_dir/git-assets.json"
fingerprint=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
test "${#fingerprint}" -eq 64
inc image list --format json | jq -e --arg f "$fingerprint" 'any(.[]; .fingerprint==$f)' >/dev/null

for key in server a b; do ssh-keygen -q -t ed25519 -N '' -f "$step_dir/$key"; done
mkdir -m 0700 "$step_dir/git-state"
git-fixture serve "$step_dir/git-state" "$step_dir/git.json" 127.0.0.1:0 "$step_dir/server" \
  > "$step_dir/git-ready.json" 2> "$step_dir/git.err" &
pids+=("$!")
for ((attempt=0; attempt<100; attempt++)); do
  if jq -e '.control and .listen' "$step_dir/git-ready.json" >/dev/null 2>&1; then break; fi
  sleep 0.1
done
git_control=$(jq -er '.control' "$step_dir/git-ready.json")
git_address=$(jq -er '.listen' "$step_dir/git-ready.json")
printf 'p %s\n' "$(cat "$step_dir/server.pub")" > "$step_dir/known_hosts"
cat > "$step_dir/ssh_config" <<'EOF'
Host p
  HostName p
  User git
  HostKeyAlias p
  IdentityFile /etc/p/git/identity
  IdentitiesOnly yes
  IdentityAgent none
  UserKnownHostsFile /etc/p/git/known_hosts
  GlobalKnownHostsFile /dev/null
  StrictHostKeyChecking yes
  BatchMode yes
  ConnectTimeout 5
  ProxyCommand /usr/libexec/p/runtime-kit git-stream
EOF
cat > "$step_dir/session.json" <<'EOF'
{"schema":"p.runtime-session/v1","activation":"base","command":["/run/current-system/sw/bin/bash","--noprofile","--norc"]}
EOF

index=0
for label in a b; do
  name="${names[$index]}"
  endpoint="$endpoint_prefix/$name"
  mkdir -m 0755 "$endpoint"
  socat "UNIX-LISTEN:$endpoint/session.sock,fork,mode=666" EXEC:cat &
  pids+=("$!")
  socat "UNIX-LISTEN:$endpoint/git.sock,fork,mode=666" "TCP:$git_address" &
  pids+=("$!")
  for ((attempt=0; attempt<100; attempt++)); do
    if test -S "$endpoint/session.sock" && test -S "$endpoint/git.sock"; then break; fi
    sleep 0.1
  done
  test -S "$endpoint/session.sock" && test -S "$endpoint/git.sock"
  git-fixture call "$git_control" "$(jq -nc --arg project "host-$label" '{schema:"p.git-fixture/v1",kind:"project",project:$project}')" | jq -e '.ok' >/dev/null
  git-fixture call "$git_control" "$(jq -nc --arg project "host-$label" --arg key "$step_dir/$label.pub" '{schema:"p.git-fixture/v1",kind:"seed-session",key:$project,project:$project,branch:"main",choice:"blank",public_key:$key}')" | jq -e '.ok' >/dev/null

  inc init "$fingerprint" "$name"
  inc config device add "$name" p-endpoint disk "source=$endpoint" path=/opt/p/endpoints readonly=true shift=false propagation=private
  # Closed image-specific staging map; verified files keep the public paths.
  while IFS=$'\t' read -r role digest mode; do
    test "$(sha256sum "$host_package/$role" | cut -d' ' -f1)" = "$digest"
    case "$role" in p-session.target|p-interactive.service|p-attach) ;; *) exit 1;; esac
    push_root "$name" "$host_package/$role" "/etc/p/assets/$role" "$(printf '%04o' "$mode")"
  done < <(jq -r '.files[] | [.role,.sha256,.mode] | @tsv' "$step_dir/assets.json")
  test "$(sha256sum "$P_TEST_GIT_PLUGIN/p-git-ssh" | cut -d' ' -f1)" = "$(jq -er '.files[] | select(.role=="p-git-ssh") | .sha256' "$step_dir/git-assets.json")"
  push_root "$name" "$P_TEST_GIT_PLUGIN/p-git-ssh" /etc/p/assets/p-git-ssh 0555
  push_root "$name" "$step_dir/ssh_config" /etc/p/git/ssh_config 0644
  push_root "$name" "$step_dir/known_hosts" /etc/p/git/known_hosts 0644
  inc file push --uid 1000 --gid 1000 --mode 0400 "$step_dir/$label" "$name/etc/p/git/identity"
  push_root "$name" "$step_dir/session.json" /etc/p/session.json 0644
  jq -n --arg repository "host-$label" '{schema:"p.workspace/v1",repository:$repository,branch:"main"}' > "$step_dir/$label-workspace.json"
  push_root "$name" "$step_dir/$label-workspace.json" /etc/p/workspace.json 0644
  stat -c 'HOST_ENDPOINT %n uid=%u gid=%g mode=%a type=%F' "$endpoint_prefix" \
    "$endpoint" "$endpoint/session.sock" "$endpoint/git.sock"
  inc start "$name"
  wait_ready "$name"
  test "$(inc list "$name" --format json | jq -er --arg n "$name" '.[] | select(.name==$n) | .expanded_devices["p-endpoint"].source')" = "$endpoint"
  for path in /opt/p/endpoints /run/p; do
    # Exactly one mount, read-only and private in the instance namespace.
    inc exec "$name" -- cat /proc/self/mountinfo | awk -v target="$path" '
      $5 == target {
        count++
        if ($6 !~ /(^|,)ro(,|$)/) bad=1
        for (i=7; i<=NF && $i != "-"; i++)
          if ($i ~ /^(shared:|master:|propagate_from:|unbindable)/) bad=1
        if (i>NF) bad=1
      }
      END { exit !(count == 1 && !bad) }
    '
    for socket in session git; do
      test "$(inc exec "$name" -- stat -c '%d:%i' "$path/$socket.sock")" = "$(stat -c '%d:%i' "$endpoint/$socket.sock")"
    done
    expect_failure exec_p "$name" touch "$path/forbidden"
  done
  test "$(inc exec "$name" -- stat -c '%u:%g:%a' /etc/p/git/identity)" = 1000:1000:400
  test "$(inc exec "$name" -- stat -c '%u:%a' /etc/p/git/ssh_config)" = 0:644
  exec_p "$name" git config core.sshCommand /usr/libexec/p/git-ssh
  exec_p "$name" git config user.name P
  exec_p "$name" git config user.email p@example.invalid
  # Expansion belongs to the shell inside the container.
  # shellcheck disable=SC2016
  exec_p "$name" sh -c 'printf "%s\n" "$1" > /workspace/identity; printf "%s\n" "$1" > /home/p/private' sh "$label"
  exec_p "$name" git add identity
  exec_p "$name" git commit -qm first
  exec_p "$name" git push origin HEAD:main
  expect_failure exec_p "$name" sh -c 'echo change >> /etc/systemd/system/p-interactive.service'
  expect_failure exec_p "$name" /usr/libexec/p/attach unwanted
  index=$((index+1))
done

a="${names[0]}" b="${names[1]}"
expect_failure exec_p "$a" git ls-remote ssh://git@p/host-b
expect_failure exec_p "$b" git ls-remote ssh://git@p/host-a
test "$(exec_p "$a" cat /workspace/identity)" = a
test "$(exec_p "$b" cat /workspace/identity)" = b
expect_failure exec_p "$b" test -e "$endpoint_prefix/$a"
store_path=$(exec_p "$a" nix-store --add /workspace/identity)
expect_failure exec_p "$b" test -e "$store_path"
main_pid=$(inc exec "$a" -- systemctl show -p MainPID --value p-interactive.service)
test "$main_pid" -gt 1
test "$(inc exec "$a" -- cat "/proc/$main_pid/comm")" = 'tmux: server'
# Explicit PTY transport, fixed attach command, then tmux's normal detach key.
{ sleep 1; printf '\002d'; sleep 1; } | timeout 20 script -q -e \
  -c "incus --force-local --project user-1000 exec $a --user 1000 --group 1000 --cwd /workspace --env TERM=xterm --mode=interactive -- /usr/libexec/p/attach" \
  "$step_dir/attach.log" >/dev/null
test "$(inc exec "$a" -- systemctl show -p MainPID --value p-interactive.service)" = "$main_pid"
inc exec "$a" -- systemctl is-active --quiet p-interactive.service
inc stop "$a"
wait_state "$a" Stopped
inc start "$a"
wait_ready "$a"
test "$(exec_p "$a" cat /home/p/private)" = a
exec_p "$a" test -e "$store_path"
exec_p "$a" git fetch origin main
# Poweroff may end the exec transport before it returns; observed state decides.
exec_p "$a" /usr/libexec/p/tmux -S /run/p-interactive/tmux.sock kill-session -t =p || true
start_after_self_shutdown "$a"
wait_ready "$a"
inc exec "$a" -- systemctl kill --signal=SIGKILL --kill-whom=main p-interactive.service || true
start_after_self_shutdown "$a"
wait_ready "$a"
inc exec "$a" -- journalctl -u p-interactive.service --no-pager | grep -E 'signal=KILL|status=9/KILL' >/dev/null
inc stop "$a"
wait_state "$a" Stopped
printf '%s\n' '{"schema":"p.runtime-session/v1","activation":"base","command":["/no/such/p-command"]}' > "$step_dir/bad-session.json"
push_root "$a" "$step_dir/bad-session.json" /etc/p/session.json 0644
inc start "$a"
wait_state "$a" Stopped
push_root "$a" "$step_dir/session.json" /etc/p/session.json 0644
start_after_self_shutdown "$a"
wait_ready "$a"
inc exec "$a" -- journalctl -u p-interactive.service --no-pager | grep -E 'tmux (did not become attachable|lifetime setup|startup)' >/dev/null
inc stop "$a"
wait_state "$a" Stopped
inc config device set "$a" p-endpoint readonly=false
inc start "$a"
wait_state "$a" Stopped
run_after_self_shutdown "$a" update config device set "$a" p-endpoint readonly=true
start_after_self_shutdown "$a"
wait_ready "$a"
inc exec "$a" -- journalctl -u p-interactive.service --no-pager | grep -F 'endpoints require one read-only mount' >/dev/null
test "$(exec_p "$a" cat /workspace/identity)" = a
test "$(exec_p "$b" cat /workspace/identity)" = b
echo P_RUNTIME_HOST_PASS
