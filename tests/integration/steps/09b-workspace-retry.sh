# shellcheck shell=bash
# Interrupt real first-boot Git fetch before atomic workspace publication, then
# retry the same session through an ordinary driver Start. Synthetic fixture
# identities exercise the runtime driver, not public session creation.
set -E
umask 077
step_dir="$P_TEST_TMP/step-09b"
mkdir -m 0700 "$step_dir"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
endpoint_prefix=/var/lib/p-vm/endpoints/pdev
pids=()
name=
inc() { timeout 45 incus --force-local --project user-1000 "$@"; }
cleanup() {
  if test -n "$name"; then inc delete --force "$name" >/dev/null 2>&1 || true; fi
  for pid in "${pids[@]}"; do kill "$pid" 2>/dev/null || true; done
  for pid in "${pids[@]}"; do wait "$pid" 2>/dev/null || true; done
  if test -n "$name"; then rm -rf -- "${endpoint_prefix:?}/${name:?}"; fi
}
on_error() {
  printf 'P_WORKSPACE_RETRY_FAIL line=%s status=%s\n' "$2" "$1" >&2
  if test -n "$name"; then
    inc info "$name" --show-log 2>&1 | tail -c 2048 >&2 || true
    inc exec "$name" -- journalctl -u p-interactive.service -n 25 --no-pager 2>&1 | tail -c 3072 >&2 || true
  fi
  tail -c 2048 "$step_dir/git.err" >&2 || true
  tail -c 1024 "$step_dir/start.err" >&2 || true
}
trap cleanup EXIT
trap 'on_error "$?" "$LINENO"' ERR
activate_package() {
  local package="$1" target="$2" grant="$3" digest id
  digest=$(p plugins conformance "$package" | jq -er '.sha256')
  id=$(jq -er '.id' "$package/plugin.json")
  jq -n --arg path "$package" --arg digest "$digest" --arg id "$id" --arg grant "$grant" \
    '{schema:"p.activation/v1",plugins:[{id:$id,path:$path,sha256:$digest,grants:[$grant],config:{}}]}' > "$target"
}

# A gated copy of the driver's Git SSH wrapper. Git init and remote
# configuration finish; the first SSH fetch then waits at the wrapper before
# contacting the server.
cat > "$step_dir/gated-git-ssh" <<'SH'
#!/bin/sh
attempt=0
while [ ! -e /etc/p/test-init-release ]; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 600 ]; then
    exit 78
  fi
  sleep 0.1
done
exec /usr/bin/ssh -F /etc/p/git/ssh_config "$@"
SH
activate_package "$P_TEST_SOURCE/plugins/bundled/tmux-host" "$step_dir/host.json" session.asset.install

fingerprint=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
test "${#fingerprint}" -eq 64
ssh-keygen -q -t ed25519 -N '' -f "$step_dir/server"
ssh-keygen -q -t ed25519 -N '' -f "$step_dir/session"
mkdir -m 0700 "$step_dir/git-state"
git-fixture serve "$step_dir/git-state" 127.0.0.1:0 "$step_dir/server" \
  > "$step_dir/git-ready.json" 2> "$step_dir/git.err" &
pids+=("$!")
for ((attempt=0; attempt<100; attempt++)); do
  if jq -e '.control and .listen' "$step_dir/git-ready.json" >/dev/null 2>&1; then break; fi
  if ! kill -0 "${pids[0]}" 2>/dev/null; then echo 'Git fixture exited before readiness' >&2; exit 1; fi
  sleep 0.1
done
git_control=$(jq -er '.control' "$step_dir/git-ready.json")
git_address=$(jq -er '.listen' "$step_dir/git-ready.json")
fixture() { timeout 15 git-fixture call "$git_control" "$1"; }
repo=$(fixture '{"schema":"p.git-fixture/v1","kind":"project","project":"retry"}' | jq -er '.result.repository_path')
export GIT_AUTHOR_NAME=P GIT_AUTHOR_EMAIL=p@example.invalid
export GIT_COMMITTER_NAME=P GIT_COMMITTER_EMAIL=p@example.invalid
blob=$(printf 'seed\n' | git -C "$repo" hash-object -w --stdin)
tree=$(printf '100644 blob %s\tREADME\n' "$blob" | git -C "$repo" mktree)
initial_oid=$(git -C "$repo" commit-tree "$tree" -m seed)
git -C "$repo" update-ref refs/heads/main "$initial_oid"
session_result=$(fixture "$(jq -nc --arg key "$step_dir/session.pub" \
  '{schema:"p.git-fixture/v1",kind:"seed-session",key:"retry-main",project:"retry",branch:"main",choice:"existing",public_key:$key}')")
session=$(jq -er '.result.session_uuid' <<< "$session_result")
name="p-$session"
endpoint="$endpoint_prefix/$name"
mkdir -m 0755 "$endpoint"
socat "UNIX-LISTEN:$endpoint/session.sock,fork,mode=666" EXEC:cat & pids+=("$!")
socat "UNIX-LISTEN:$endpoint/git.sock,fork,mode=666" "TCP:$git_address" & pids+=("$!")
for ((attempt=0; attempt<100; attempt++)); do
  if test -S "$endpoint/session.sock" && test -S "$endpoint/git.sock"; then break; fi
  sleep 0.1
done
test -S "$endpoint/session.sock" && test -S "$endpoint/git.sock"
jq -n --arg instance 11111111-1111-4111-8111-111111111111 \
  --arg session "$session" --arg image "$fingerprint" --arg endpoints "$endpoint" \
  --arg commit "$initial_oid" --arg key "$step_dir/session" \
  --arg server "$step_dir/server.pub" --arg host "$step_dir/host.json" \
  '{instance_uuid:$instance,session_uuid:$session,project:"retry",branch:"main",commit:$commit,
    image:$image,endpoints:$endpoints,host_activation:$host,
    git_key_file:$key,git_host_key_file:$server}' \
  > "$step_dir/session.json"
spec_digest=$(sha256sum "$step_dir/session.json" | cut -d' ' -f1)
invoke() { timeout 190 runtime-fixture "$1" "$step_dir/session.json" "${@:2}"; }
exec_p() {
  inc exec "$name" --user 1000 --group 1000 --cwd /workspace \
    --env HOME=/home/p --env GIT_SSH=/usr/libexec/p/git-ssh -- "$@"
}
wait_state() {
  local expected="$1" state deadline=$((SECONDS+60))
  while ((SECONDS < deadline)); do
    state=$(inc list "$name" --format json | jq -r --arg n "$name" '.[]|select(.name==$n)|.status')
    if test "$state" = "$expected"; then return; fi
    sleep 0.2
  done
  printf 'Instance did not reach %s (last %s)\n' "$expected" "$state" >&2
  return 1
}
# The guest can report Stopped before its own stop operation releases the
# Incus lock; the file API waits for that release.
file_after_stop() {
  local output deadline=$((SECONDS+45))
  while ((SECONDS < deadline)); do
    if output=$(inc file "$@" 2>&1); then return; fi
    if [[ "$output" != *'Instance is busy running a "stop" operation'* ]]; then
      printf '%s\n' "$output" >&2
      return 1
    fi
    sleep 0.2
  done
  echo 'Incus stop operation did not release its file lock' >&2
  return 1
}
# Start returns once the session host is ready. When the host fails, the
# fixture exits 3 and the runtime ends stopped.
start_ready() {
  invoke start 2> "$step_dir/start.err" | jq -e '.state=="running" and .host.ready' >/dev/null
}
start_host_failure() {
  local status=0
  invoke start > "$step_dir/start.out" 2> "$step_dir/start.err" || status=$?
  if test "$status" -ne 3; then
    printf 'Start ended with status %s, not a host failure\n' "$status" >&2
    return 1
  fi
}

invoke create | jq -e '.state=="stopped" and .complete and .gated' >/dev/null
# The driver installs its wrapper at create only; the stopped runtime takes
# the gated copy in its place, root-owned and executable as the guest checks.
file_after_stop push --uid 0 --gid 0 --mode 0555 \
  "$step_dir/gated-git-ssh" "$name/etc/p/assets/p-git-ssh"
# The gated fetch holds the host, so Start waits in the background.
invoke start > "$step_dir/first-start.json" 2> "$step_dir/start.err" &
start_pid=$!
pids+=("$start_pid")
wait_state Running

# .git exists only in the root-private scratch tree while SSH is gated. Kill
# systemd's exact root ExecStartPre supervisor before it can publish that tree.
control_pid=
command_line=
for ((attempt=0; attempt<100; attempt++)); do
  control_pid=
  command_line=
  if inc exec "$name" -- test -d /.p-workspace-init/tree/.git 2>/dev/null && \
     control_pid=$(inc exec "$name" -- systemctl show -p ControlPID --value p-interactive.service 2>/dev/null) && \
     [[ "$control_pid" =~ ^[1-9][0-9]*$ ]]; then
    if command_line=$(inc exec "$name" -- cat "/proc/$control_pid/cmdline" 2>/dev/null | tr '\0' ' ') &&
       [[ "$command_line" == *'/usr/libexec/p/runtime-kit init-workspace '* ]]; then break; fi
  fi
  sleep 0.1
done
if [[ ! "$control_pid" =~ ^[1-9][0-9]*$ ]] ||
   [[ "$command_line" != *'/usr/libexec/p/runtime-kit init-workspace '* ]]; then
  echo 'Workspace scratch and root init-workspace supervisor were not observed together' >&2
  exit 1
fi
# shellcheck disable=SC2016
inc exec "$name" -- sh -c 'set -e; entries=$(ls -A /workspace); test -z "$entries"'
exec_p test ! -r /.p-workspace-init/tree
inc exec "$name" -- kill -KILL "$control_pid"
wait_state Stopped
# The killed first boot is the host failure the waiting Start reports.
start_status=0
wait "$start_pid" || start_status=$?
test "$start_status" -eq 3
test ! -s "$step_dir/first-start.json"
test "$(sha256sum "$step_dir/session.json" | cut -d' ' -f1)" = "$spec_digest"
file_after_stop pull "$name/.p-workspace-init/tree/.git/config" "$step_dir/partial-config"
grep -F 'ssh://git@p/retry' "$step_dir/partial-config" >/dev/null
if inc file pull "$name/workspace/.git/p-initialized" - > /dev/null 2>&1; then
  echo 'Killed initialization published a workspace' >&2
  exit 1
fi
if inc file pull "$name/workspace/README" - > /dev/null 2>&1; then
  echo 'Killed initialization exposed checkout contents' >&2
  exit 1
fi

# An unexpected file in the unpublished destination must survive a failed
# Start. Remove only this test file, then allow the original fetch and retry.
printf 'unexpected\n' > "$step_dir/unexpected"
file_after_stop push --uid 1000 --gid 1000 --mode 0644 \
  "$step_dir/unexpected" "$name/workspace/unexpected"
start_host_failure
wait_state Stopped
file_after_stop pull "$name/workspace/unexpected" "$step_dir/preserved"
test "$(cat "$step_dir/preserved")" = unexpected
invoke inspect | jq -e '.state=="stopped" and (.host.ready|not)' >/dev/null
invoke hostlog | jq -e '.log | contains("partial or unexpected workspace")' >/dev/null
file_after_stop pull "$name/.p-workspace-init/tree/.git/config" "$step_dir/partial-after-refusal"
cmp "$step_dir/partial-config" "$step_dir/partial-after-refusal"
file_after_stop delete "$name/workspace/unexpected"
printf 'release\n' > "$step_dir/test-init-release"
file_after_stop push --uid 0 --gid 0 --mode 0644 \
  "$step_dir/test-init-release" "$name/etc/p/test-init-release"
start_ready
test "$(sha256sum "$step_dir/session.json" | cut -d' ' -f1)" = "$spec_digest"
test "$(exec_p git rev-parse HEAD)" = "$initial_oid"
test "$(exec_p git symbolic-ref -q HEAD)" = refs/heads/main
test "$(exec_p cat README)" = seed
inc exec "$name" -- test ! -e /.p-workspace-init/tree
test "$(inc list "$name" --format json | jq --arg n "$name" '[.[]|select(.name==$n)]|length')" -eq 1
test "$(exec_p git rev-parse refs/remotes/origin/main)" = "$initial_oid"
exec_p git config user.name P
exec_p git config user.email p@example.invalid
exec_p sh -c 'printf "published\n" > published.txt'
exec_p git add published.txt
exec_p git commit -qm published
published_oid=$(exec_p git rev-parse HEAD)
exec_p git push origin HEAD:main
fixture '{"schema":"p.git-fixture/v1","kind":"refs","project":"retry","limit":8}' |
  jq -e --arg oid "$published_oid" '.ok==true and (.result.refs | length)==1 and .result.refs[0].ref=="refs/heads/main" and .result.refs[0].oid==$oid' >/dev/null

# A later ordinary Start must preserve user files, local Git state, and home.
exec_p sh -c 'printf "dirty\n" > dirty; printf "private\n" > /home/p/private'
invoke stop | jq -e '.state=="stopped"' >/dev/null
start_ready
test "$(exec_p git rev-parse HEAD)" = "$published_oid"
test "$(exec_p cat dirty)" = dirty
test "$(exec_p cat /home/p/private)" = private
inc exec "$name" -- test ! -e /.p-workspace-init/tree
test "$(inc list "$name" --format json | jq --arg n "$name" '[.[]|select(.name==$n)]|length')" -eq 1
invoke stop | jq -e '.state=="stopped"' >/dev/null
echo P_WORKSPACE_RETRY_PASS
