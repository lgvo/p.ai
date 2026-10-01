# shellcheck shell=bash
# Interrupt real first-boot Git fetch before atomic workspace publication, then
# retry the same selected assembly through ordinary Incus Start. Synthetic
# fixture identities exercise the runtime broker, not public session creation.
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
  if test -n "$name"; then
    inc delete --force "$name" >/dev/null 2>&1 || true
    rm -rf -- "${endpoint_prefix:?}/${name:?}"
  fi
  for pid in "${pids[@]}"; do kill "$pid" 2>/dev/null || true; done
  for pid in "${pids[@]}"; do wait "$pid" 2>/dev/null || true; done
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
activate_package "$P_TEST_RUNTIME_PLUGIN" "$step_dir/runtime.json" runtime.incus
activate_package "$P_TEST_GIT_PLUGIN" "$step_dir/git-server.json" git.project

# The package copy changes only the selected session asset. Its new digest is
# pinned by the normal asset plan. Git init and remote configuration finish;
# the first SSH fetch then waits at the wrapper before contacting the server.
mkdir -m 0700 "$step_dir/source-package"
cp "$P_TEST_GIT_PLUGIN/"* "$step_dir/source-package/"
rm "$step_dir/source-package/p-git-ssh"
cat > "$step_dir/source-package/p-git-ssh" <<'SH'
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
chmod 0755 "$step_dir/source-package/p-git-ssh"
activate_package "$step_dir/source-package" "$step_dir/source.json" git.project
activate_package "$P_TEST_SOURCE/plugins/bundled/tmux-host" "$step_dir/host.json" session.asset.install

fingerprint=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
test "${#fingerprint}" -eq 64
ssh-keygen -q -t ed25519 -N '' -f "$step_dir/server"
ssh-keygen -q -t ed25519 -N '' -f "$step_dir/session"
mkdir -m 0700 "$step_dir/git-state"
git-fixture serve "$step_dir/git-state" "$step_dir/git-server.json" 127.0.0.1:0 "$step_dir/server" \
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
  --arg session "$session" --arg image "$fingerprint" --arg endpoint "$endpoint" \
  --arg oid "$initial_oid" --arg identity "$step_dir/session" \
  --arg server "$step_dir/server.pub" --arg host "$step_dir/host.json" \
  --arg source "$step_dir/source.json" \
  '{instance_uuid:$instance,session_uuid:$session,image:$image,endpoint:$endpoint,
    repository:"retry",branch:"main",initial_oid:$oid,host_activation:$host,
    source_activation:$source,identity_file:$identity,server_public_key_file:$server}' \
  > "$step_dir/request.json"
request_digest=$(sha256sum "$step_dir/request.json" | cut -d' ' -f1)
invoke() { timeout 130 runtime-fixture "$step_dir/runtime.json" "$1" "$step_dir/request.json"; }
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
wait_host() {
  local state deadline=$((SECONDS+60))
  while ((SECONDS < deadline)); do
    if state=$(invoke runtime.observe-host 2> "$step_dir/observe.err"); then
      if jq -e '.status=="Running" and .host_ready==true and .host_unit=="active/running"' <<< "$state" >/dev/null; then return; fi
      if jq -e '.status=="Stopped"' <<< "$state" >/dev/null; then
        echo 'Host stopped before readiness' >&2
        return 1
      fi
    fi
    sleep 0.2
  done
  echo 'Host did not become ready' >&2
  return 1
}
start_after_stop() {
  local output deadline=$((SECONDS+45))
  while ((SECONDS < deadline)); do
    if output=$(invoke runtime.start 2> "$step_dir/start.err"); then
      jq -e '.status=="Running"' <<< "$output" >/dev/null
      return
    fi
    # This exact Incus rejection precedes any start effect. The guest can
    # report Stopped before its own stop operation releases the Incus lock.
    if ! grep -Fq 'Instance is busy running a "stop" operation' "$step_dir/start.err"; then
      cat "$step_dir/start.err" >&2
      return 1
    fi
    sleep 0.2
  done
  echo 'Incus stop operation did not release its lock' >&2
  return 1
}
start_expect_self_shutdown() {
  local output deadline=$((SECONDS+45))
  while ((SECONDS < deadline)); do
    if output=$(invoke runtime.start 2> "$step_dir/start.err"); then
      jq -e '.status=="Running"' <<< "$output" >/dev/null
      return
    fi
    if grep -Fq 'Instance is busy running a "stop" operation' "$step_dir/start.err"; then
      sleep 0.2
      continue
    fi
    # A fast failing ExecStartPre can stop the guest before the broker's
    # post-start inspect. The failed Incus postcondition is expected here.
    if grep -Fq 'Incus state postcondition failed' "$step_dir/start.err"; then return; fi
    cat "$step_dir/start.err" >&2
    return 1
  done
  echo 'Incus stop operation did not release its lock' >&2
  return 1
}
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

invoke runtime.create | jq -e '.status=="Stopped"' >/dev/null
invoke runtime.assemble | jq -e '.status=="Stopped"' >/dev/null
start_after_stop
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
test "$(sha256sum "$step_dir/request.json" | cut -d' ' -f1)" = "$request_digest"
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
start_expect_self_shutdown
wait_state Stopped
file_after_stop pull "$name/workspace/unexpected" "$step_dir/preserved"
test "$(cat "$step_dir/preserved")" = unexpected
failed_host=$(invoke runtime.observe-host)
jq -e '.status=="Stopped" and .diagnostic_available==true and (.diagnostic | contains("partial or unexpected workspace"))' \
  <<< "$failed_host" >/dev/null
file_after_stop pull "$name/.p-workspace-init/tree/.git/config" "$step_dir/partial-after-refusal"
cmp "$step_dir/partial-config" "$step_dir/partial-after-refusal"
file_after_stop delete "$name/workspace/unexpected"
printf 'release\n' > "$step_dir/test-init-release"
file_after_stop push --uid 0 --gid 0 --mode 0644 \
  "$step_dir/test-init-release" "$name/etc/p/test-init-release"
start_after_stop
wait_host
test "$(sha256sum "$step_dir/request.json" | cut -d' ' -f1)" = "$request_digest"
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
invoke runtime.stop | jq -e '.status=="Stopped"' >/dev/null
start_after_stop
wait_host
test "$(exec_p git rev-parse HEAD)" = "$published_oid"
test "$(exec_p cat dirty)" = dirty
test "$(exec_p cat /home/p/private)" = private
inc exec "$name" -- test ! -e /.p-workspace-init/tree
test "$(inc list "$name" --format json | jq --arg n "$name" '[.[]|select(.name==$n)]|length')" -eq 1
invoke runtime.stop | jq -e '.status=="Stopped"' >/dev/null
echo P_WORKSPACE_RETRY_PASS
