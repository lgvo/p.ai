# Runs inside the disposable product-test VM as the confined pdev account.
umask 077
control_dir="$P_TEST_TMP/control"
mkdir -m 0700 "$control_dir"
state_dir="$control_dir/state"
config="$control_dir/host.json"
socket="$state_dir/control.sock"
jq -n --arg state_dir "$state_dir" '{schema:"p.host/v1",state_dir:$state_dir}' > "$config"
chmod 0600 "$config"

daemon_pid=""
cleanup_control_daemon() {
  if [ -n "$daemon_pid" ]; then
    kill -TERM "$daemon_pid" 2>/dev/null || true
    wait "$daemon_pid" 2>/dev/null || true
    daemon_pid=""
  fi
}
trap cleanup_control_daemon EXIT

start_control_daemon() {
  p daemon "$config" > "$control_dir/daemon.out" 2> "$control_dir/daemon.err" &
  daemon_pid=$!
  for ((attempt=0; attempt<200; attempt++)); do
    if [ -S "$socket" ] && p api "$socket" system.health | jq -e '.result.control_state == "ready"' >/dev/null 2>&1; then
      return
    fi
    if ! kill -0 "$daemon_pid" 2>/dev/null; then
      cat "$control_dir/daemon.err" >&2
      echo "control daemon exited before readiness" >&2
      exit 1
    fi
    sleep 0.05
  done
  cat "$control_dir/daemon.err" >&2
  echo "control daemon did not become ready" >&2
  exit 1
}

expect_rpc_error() {
  expected="$1"
  shift
  if response=$(p api "$socket" "$@"); then
    echo "RPC unexpectedly succeeded: $*" >&2
    exit 1
  fi
  jq -e --arg kind "$expected" '.error.kind == $kind' <<< "$response" >/dev/null
}

start_control_daemon
test "$(stat -c '%a' "$state_dir")" = 700
test "$(stat -c '%a' "$socket")" = 600
test -f "$state_dir/control.sqlite"
identity=$(p api "$socket" system.hello | jq -er '.result.instance_id')
test -n "$identity"
p api "$socket" system.capabilities | jq -e '.result.lifecycle == "unavailable" and (.result.available | index("system.hello")) != null' >/dev/null
expect_rpc_error unavailable session.create '{"v":1}'
expect_rpc_error method_not_found not.a.method '{"v":1}'
expect_rpc_error invalid_params system.hello '{"v":2}'
p api "$socket" system.inspect | jq -e '.result.projects == 0 and .result.sessions == 0 and .result.operations == 0' >/dev/null

raw_response=$(printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":' | timeout 3 socat - "UNIX-CONNECT:$socket")
jq -e '.error.kind == "parse_error"' <<< "$raw_response" >/dev/null
raw_response=$(printf '%s\n' '{"jsonrpc":"9.0","id":2,"method":"system.hello","params":{"v":1}}' | timeout 3 socat - "UNIX-CONNECT:$socket")
jq -e '.error.kind == "unsupported_version"' <<< "$raw_response" >/dev/null
raw_response=$(printf '%s\n' '{"jsonrpc":"2.0","id":true,"method":"system.hello","params":{"v":1}}' | timeout 3 socat - "UNIX-CONNECT:$socket")
jq -e '.error.kind == "invalid_request" and .id == null' <<< "$raw_response" >/dev/null

if timeout 5 p daemon "$config" > "$control_dir/second.out" 2> "$control_dir/second.err"; then
  echo "second daemon unexpectedly acquired the state directory" >&2
  exit 1
fi
if ! grep -Fq 'another daemon owns this state directory' "$control_dir/second.err"; then
  cat "$control_dir/second.err" >&2
  echo "second daemon failed for the wrong reason" >&2
  exit 1
fi
test "$(p api "$socket" system.hello | jq -er '.result.instance_id')" = "$identity"

cleanup_control_daemon
test ! -e "$socket"
start_control_daemon
test "$(p api "$socket" system.hello | jq -er '.result.instance_id')" = "$identity"
p api "$socket" system.inspect | jq -e '.result.projects == 0 and .result.sessions == 0 and .result.operations == 0' >/dev/null

# A crash leaves the socket inode behind. The single writer lock is released
# by the kernel, and the next daemon must replace only its owned stale socket.
kill -KILL "$daemon_pid"
wait "$daemon_pid" 2>/dev/null || true
daemon_pid=""
test -S "$socket"
start_control_daemon
test "$(p api "$socket" system.hello | jq -er '.result.instance_id')" = "$identity"
p api "$socket" system.inspect | jq -e '.result.projects == 0 and .result.sessions == 0 and .result.operations == 0' >/dev/null
cleanup_control_daemon
echo P_CONTROL_PLANE_PASS
