# shellcheck shell=bash
# Runs inside the disposable product-test VM as the confined pdev account.
# The fixture seeds synthetic registry/SSH authority before the production
# daemon opens SQLite. This does not exercise public lifecycle mutations.
umask 077
set -E
step_dir="$P_TEST_TMP/step-07"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
activation="$step_dir/activation.json"
config="$step_dir/host.json"
fixture_pid=
daemon_pid=
server_key_backup=
activation_backup=

stop_fixture() {
  if test -n "${fixture_pid:-}"; then
    kill -TERM "$fixture_pid" 2>/dev/null || true
    for ((attempt=0; attempt<50; attempt++)); do
      if ! kill -0 "$fixture_pid" 2>/dev/null; then break; fi
      sleep 0.1
    done
    if kill -0 "$fixture_pid" 2>/dev/null; then kill -KILL "$fixture_pid" 2>/dev/null || true; fi
    wait "$fixture_pid" 2>/dev/null || true
    fixture_pid=
  fi
}
stop_daemon() {
  if test -n "${daemon_pid:-}"; then
    kill -TERM "$daemon_pid" 2>/dev/null || true
    for ((attempt=0; attempt<50; attempt++)); do
      if ! kill -0 "$daemon_pid" 2>/dev/null; then break; fi
      sleep 0.1
    done
    if kill -0 "$daemon_pid" 2>/dev/null; then kill -KILL "$daemon_pid" 2>/dev/null || true; fi
    wait "$daemon_pid" 2>/dev/null || true
    daemon_pid=
  fi
}
cleanup() {
  stop_daemon
  stop_fixture
  if test -n "$server_key_backup" && test -f "$server_key_backup"; then
    mv -f "$server_key_backup" "$state/git_server_key"
  fi
  if test -n "$activation_backup" && test -f "$activation_backup"; then
    mv -f "$activation_backup" "$activation"
  fi
}
on_error() {
  status="$1"
  line="$2"
  printf 'P_DAEMON_GIT_FAIL line=%s status=%s\n' "$line" "$status" >&2
  for log in fixture.err daemon.err refused.err expected.err; do
    if test -f "$step_dir/$log"; then
      printf '%s (last 2 KiB):\n' "$log" >&2
      tail -c 2048 "$step_dir/$log" >&2 || true
    fi
  done
}
trap cleanup EXIT
trap 'on_error "$?" "$LINENO"' ERR

package_digest=$(p plugins conformance "$P_TEST_GIT_PLUGIN" | jq -er '.sha256')
package_id=$(jq -er '.id' "$P_TEST_GIT_PLUGIN/plugin.json")
jq -n --arg path "$P_TEST_GIT_PLUGIN" --arg digest "$package_digest" --arg id "$package_id" \
  '{schema:"p.activation/v1",plugins:[{id:$id,path:$path,sha256:$digest,grants:["git.project"],config:{}}]}' > "$activation"
p plugins activate "$activation" | jq -e '.[0].capability == "source-git"' >/dev/null

# Seed the same state directory through the fixture, then stop it so the
# production daemon is the only SQLite writer and Git listener.
ssh-keygen -q -t ed25519 -N '' -f "$step_dir/fixture-server" >/dev/null
ssh-keygen -q -t ed25519 -N '' -f "$step_dir/session" >/dev/null
git-fixture serve "$state" "$activation" 127.0.0.1:0 "$step_dir/fixture-server" \
  > "$step_dir/fixture-ready.json" 2> "$step_dir/fixture.err" &
fixture_pid=$!
for ((attempt=0; attempt<100; attempt++)); do
  if jq -e '.schema == "p.git-fixture/v1" and .control' "$step_dir/fixture-ready.json" >/dev/null 2>&1; then
    break
  fi
  if ! kill -0 "$fixture_pid" 2>/dev/null; then
    echo 'Git seed fixture exited before readiness' >&2
    exit 1
  fi
  sleep 0.1
done
fixture_socket=$(jq -er '.control' "$step_dir/fixture-ready.json")
fixture() { timeout 15 git-fixture call "$fixture_socket" "$1"; }
fixture '{"schema":"p.git-fixture/v1","kind":"ping"}' | jq -e '.result.status == "ready"' >/dev/null
repo=$(fixture '{"schema":"p.git-fixture/v1","kind":"project","project":"app"}' | jq -er '.result.repository_path')
fixture '{"schema":"p.git-fixture/v1","kind":"project","project":"other"}' | jq -e '.ok' >/dev/null
fixture '{"schema":"p.git-fixture/v1","kind":"project","project":"z-last"}' | jq -e '.ok' >/dev/null
tree=$(git -C "$repo" hash-object -t tree -w --stdin < /dev/null)
main_oid=$(env GIT_AUTHOR_NAME=P GIT_AUTHOR_EMAIL=p@example.invalid \
  GIT_COMMITTER_NAME=P GIT_COMMITTER_EMAIL=p@example.invalid \
  git -C "$repo" commit-tree "$tree" -m seed)
git -C "$repo" update-ref refs/heads/main "$main_oid"
git -C "$repo" update-ref refs/heads/work "$main_oid"
for number in {1..9}; do
  printf -v branch 'b%02d' "$number"
  git -C "$repo" update-ref "refs/heads/$branch" "$main_oid"
done
session_result=$(fixture "$(jq -nc --arg key "$step_dir/session.pub" \
  '{schema:"p.git-fixture/v1",kind:"seed-session",key:"daemon-git-work",project:"app",branch:"work",choice:"existing",public_key:$key}')")
session_uuid=$(jq -er '.result.session_uuid' <<< "$session_result")
test -n "$session_uuid"
stop_fixture
test ! -e "$fixture_socket"

jq -n --arg state "$state" --arg activation "$activation" --arg id "$package_id" \
  '{schema:"p.host/v1",state_dir:$state,git:{activation_path:$activation,source_plugin_id:$id,listen:"127.0.0.1:0"}}' > "$config"

rpc() { timeout 15 p api "$socket" "$@"; }
start_daemon() {
  : > "$step_dir/daemon.err"
  p daemon "$config" > "$step_dir/daemon.out" 2> "$step_dir/daemon.err" &
  daemon_pid=$!
  deadline=$((SECONDS + 20))
  while ((SECONDS < deadline)); do
    if test -S "$socket" && timeout 2 p api "$socket" system.health | jq -e '.result.control_state == "ready"' >/dev/null 2>&1; then
      return
    fi
    if ! kill -0 "$daemon_pid" 2>/dev/null; then
      echo 'configured daemon exited before readiness' >&2
      exit 1
    fi
    sleep 0.05
  done
  echo 'configured daemon did not become ready' >&2
  exit 1
}
expect_refused_start() {
  reason="$1"
  if timeout 8 p daemon "$config" > "$step_dir/refused.out" 2> "$step_dir/refused.err"; then
    echo "daemon unexpectedly started with $reason" >&2
    exit 1
  else
    status=$?
  fi
  if test "$status" -eq 124 || test -e "$socket"; then
    echo "daemon did not refuse $reason before RPC" >&2
    exit 1
  fi
  if ! grep -Fq "$reason" "$step_dir/refused.err"; then
    echo "daemon refused startup for the wrong reason: $reason" >&2
    exit 1
  fi
}
set_git_key() {
  export GIT_SSH_COMMAND="ssh -i $1 -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=$step_dir/known_hosts -o BatchMode=yes -o ConnectTimeout=5"
}
git_net() { timeout 30 git "$@"; }
check_restarted_git() {
  restarted=$(rpc system.capabilities)
  jq -e --arg server "$(jq -er '.result.git.host_public_key' <<< "$capabilities")" \
    --arg client "$(jq -er '.result.git.client_public_key' <<< "$capabilities")" \
    --arg key "$state/git_client_key" \
    '.result.git.host_public_key == $server and .result.git.client_public_key == $client and
     .result.git.client_key_path == $key' <<< "$restarted" >/dev/null
  printf '%s\n' "$(jq -er '.result.git.known_hosts' <<< "$restarted")" > "$step_dir/known_hosts"
  restarted_endpoint=$(jq -er '.result.git.endpoint' <<< "$restarted")
  restarted_url="ssh://git@127.0.0.1:${restarted_endpoint##*:}/app"
  set_git_key "$state/git_client_key"
  git_net -C "$step_dir/host-clone" fetch "$restarted_url" main >/dev/null
  test "$(git -C "$step_dir/host-clone" rev-parse FETCH_HEAD)" = "$main_oid"
}

start_daemon
test "$(stat -c '%a' "$state/git_server_key")" = 600
test "$(stat -c '%a' "$state/git_client_key")" = 600
instance_id=$(rpc system.hello | jq -er '.result.instance_id')
capabilities=$(rpc system.capabilities)
jq -e --arg id "$package_id" --arg digest "$package_digest" --arg key "$state/git_client_key" \
  '.result.lifecycle == "unavailable" and
   (.result.available | index("project.list") != null and index("project.branches") != null) and
   .result.git.source_plugin_id == $id and .result.git.source_sha256 == $digest and
   .result.git.client_key_path == $key and
   ([.result.git.endpoint, .result.git.known_hosts, .result.git.client_public_key] |
     all(.[]; type == "string" and length > 0))' <<< "$capabilities" >/dev/null
git_info=$(jq -c '.result.git' <<< "$capabilities")
rpc system.inspect | jq -e --argjson git "$git_info" \
  '.result.projects == 3 and .result.sessions == 1 and .result.operations == 1 and .result.git == $git' >/dev/null

page1=$(rpc project.list '{"v":1,"limit":2}')
jq -e '.result.projects == [{"path":"app","registry_state":"active"},{"path":"other","registry_state":"active"}] and .result.next == "other"' <<< "$page1" >/dev/null
page2=$(rpc project.list '{"v":1,"limit":2,"after":"other"}')
jq -e '.result.projects == [{"path":"z-last","registry_state":"active"}] and .result.next == ""' <<< "$page2" >/dev/null
branches1=$(rpc project.branches '{"v":1,"project":"app","limit":8}')
jq -e --arg oid "$main_oid" \
  '.result.project == "app" and (.result.refs | length) == 8 and
   .result.refs[0] == {"ref":"refs/heads/b01","oid":$oid} and
   .result.next == "refs/heads/b08"' <<< "$branches1" >/dev/null
branches2=$(rpc project.branches '{"v":1,"project":"app","limit":8,"after":"refs/heads/b08"}')
jq -e --arg oid "$main_oid" \
  '.result.refs == [{"ref":"refs/heads/b09","oid":$oid},{"ref":"refs/heads/main","oid":$oid},{"ref":"refs/heads/work","oid":$oid}] and .result.next == ""' <<< "$branches2" >/dev/null

endpoint=$(jq -er '.result.git.endpoint' <<< "$capabilities")
port=${endpoint##*:}
printf '%s\n' "$(jq -er '.result.git.known_hosts' <<< "$capabilities")" > "$step_dir/known_hosts"
url="ssh://git@127.0.0.1:$port/app"
set_git_key "$state/git_client_key"
git_net clone "$url" "$step_dir/host-clone" >/dev/null 2> "$step_dir/expected.err"
git_net -C "$step_dir/host-clone" fetch origin main >/dev/null
git -C "$step_dir/host-clone" checkout -qB main origin/main
git -C "$step_dir/host-clone" config user.name P
git -C "$step_dir/host-clone" config user.email p@example.invalid
printf 'host change\n' > "$step_dir/host-clone/host.txt"
git -C "$step_dir/host-clone" add host.txt
git -C "$step_dir/host-clone" commit -qm host-change
if git_net -C "$step_dir/host-clone" push origin HEAD:main > "$step_dir/unexpected.out" 2> "$step_dir/expected.err"; then
  echo 'host principal unexpectedly pushed' >&2
  exit 1
fi
test "$(git -C "$repo" rev-parse refs/heads/main)" = "$main_oid"

set_git_key "$step_dir/session"
git_net clone "$url" "$step_dir/session-clone" >/dev/null 2> "$step_dir/expected.err"
git -C "$step_dir/session-clone" checkout -qB work origin/work
git -C "$step_dir/session-clone" config user.name P
git -C "$step_dir/session-clone" config user.email p@example.invalid
printf 'session change\n' > "$step_dir/session-clone/work.txt"
git -C "$step_dir/session-clone" add work.txt
git -C "$step_dir/session-clone" commit -qm session-change
git_net -C "$step_dir/session-clone" push origin HEAD:work >/dev/null
work_oid=$(git -C "$step_dir/session-clone" rev-parse HEAD)
test "$(git -C "$repo" rev-parse refs/heads/work)" = "$work_oid"
rpc project.branches '{"v":1,"project":"app","limit":8,"after":"refs/heads/main"}' | \
  jq -e --arg oid "$work_oid" '.result.refs == [{"ref":"refs/heads/work","oid":$oid}]' >/dev/null

stop_daemon
test ! -e "$socket"
start_daemon
test "$(rpc system.hello | jq -er '.result.instance_id')" = "$instance_id"
rpc system.inspect | jq -e '.result.projects == 3 and .result.sessions == 1 and .result.operations == 1' >/dev/null
check_restarted_git
stop_daemon

# Crash recovery must retain the instance and pinned SSH identity too.
start_daemon
kill -KILL "$daemon_pid"
wait "$daemon_pid" 2>/dev/null || true
daemon_pid=
test -S "$socket"
start_daemon
test "$(rpc system.hello | jq -er '.result.instance_id')" = "$instance_id"
check_restarted_git
stop_daemon

server_key_backup="$step_dir/git_server_key.saved"
mv "$state/git_server_key" "$server_key_backup"
expect_refused_start 'pinned Git server key is missing'
mv "$server_key_backup" "$state/git_server_key"
server_key_backup=
ssh-keygen -q -t ed25519 -N '' -f "$step_dir/replacement" >/dev/null
server_key_backup="$step_dir/git_server_key.saved"
mv "$state/git_server_key" "$server_key_backup"
cp "$step_dir/replacement" "$state/git_server_key"
chmod 0600 "$state/git_server_key"
expect_refused_start 'Git server identity'
rm "$state/git_server_key"
mv "$server_key_backup" "$state/git_server_key"
server_key_backup=

activation_backup="$step_dir/activation.saved"
mv "$activation" "$activation_backup"
jq '.plugins[0].sha256 = ("0" * 64)' "$activation_backup" > "$activation"
expect_refused_start 'Git activation'
mv "$activation_backup" "$activation"
activation_backup=
start_daemon
test "$(rpc system.hello | jq -er '.result.instance_id')" = "$instance_id"
stop_daemon
echo P_DAEMON_GIT_COMPOSITION_PASS
