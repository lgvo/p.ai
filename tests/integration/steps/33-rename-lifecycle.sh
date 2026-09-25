# shellcheck shell=bash
# Public Rename across real Incus and Git. Credentials are dummy private files.
set -E
umask 077
step_dir="$P_TEST_TMP/step-33"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step33
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
daemon_pid=
uuid=
uuid_b=
incus_real=$(command -v incus)

inc() { timeout 60 "$incus_real" --force-local --project user-1000 "$@"; }
rpc() { timeout 35 p api "$socket" "$@"; }
guest() {
  inc exec "p-$uuid" --user 1000 --group 1000 --cwd /workspace \
    --env HOME=/home/p --env GIT_SSH=/usr/libexec/p/git-ssh -- "$@"
}
stop_daemon() {
  if test -n "$daemon_pid"; then
    kill -TERM "$daemon_pid" 2>/dev/null || true
    for ((i=0; i<100; i++)); do
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
  if test -n "$uuid"; then
    inc resume "p-$uuid" >/dev/null 2>&1 || true
    inc delete --force "p-$uuid" >/dev/null 2>&1 || true
    rm -rf -- "${endpoint_prefix:?}/${uuid:?}"
  fi
  if test -n "$uuid_b"; then
    inc delete --force "p-$uuid_b" >/dev/null 2>&1 || true
    rm -rf -- "${endpoint_prefix:?}/${uuid_b:?}"
  fi
  rmdir "$endpoint_prefix" 2>/dev/null || true
}
on_error() {
  printf 'P_RENAME_LIFECYCLE_FAIL line=%s status=%s\n' "$2" "$1" >&2
  tail -c 3072 "$step_dir/daemon.err" >&2 || true
}
trap cleanup EXIT
trap 'on_error "$?" "$LINENO"' ERR

activate() {
  local package="$1" target="$2" grant="$3" digest id
  digest=$(p plugins conformance "$package" | jq -er '.sha256 | select(length==64)')
  id=$(jq -er '.id' "$package/plugin.json")
  jq -n --arg path "$package" --arg digest "$digest" --arg id "$id" --arg grant "$grant" \
    '{schema:"p.activation/v1",plugins:[{id:$id,path:$path,sha256:$digest,grants:[$grant],config:{}}]}' > "$target"
}
activate "$P_TEST_GIT_PLUGIN" "$step_dir/git.json" git.project
activate "$P_TEST_RUNTIME_PLUGIN" "$step_dir/runtime-one.json" runtime.incus
activate "$P_TEST_SOURCE/plugins/bundled/tmux-host" "$step_dir/host-one.json" session.asset.install
activate "$P_TEST_SOURCE/plugins/bundled/codex-adapter" "$step_dir/agent.json" agent.status.report
jq -s '{schema:"p.activation/v1",plugins:([.[0].plugins[0],.[1].plugins[0]])}' \
  "$step_dir/runtime-one.json" "$step_dir/host-one.json" > "$step_dir/runtime.json"

base_image=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
test "${#base_image}" -eq 64
jq -n --arg state "$state" --arg git_activation "$step_dir/git.json" \
  --arg git_id "$(jq -er '.plugins[0].id' "$step_dir/git.json")" \
  --arg runtime_activation "$step_dir/runtime.json" \
  --arg runtime_id "$(jq -er '.plugins[0].id' "$step_dir/runtime.json")" \
  --arg host_id "$(jq -er '.plugins[1].id' "$step_dir/runtime.json")" \
  --arg agent_activation "$step_dir/agent.json" \
  --arg agent_id "$(jq -er '.plugins[0].id' "$step_dir/agent.json")" \
  --arg incus_binary "$incus_real" --arg prefix "$endpoint_prefix" --arg image "$base_image" \
  '{schema:"p.host/v1",state_dir:$state,
    git:{activation_path:$git_activation,source_plugin_id:$git_id,listen:"127.0.0.1:0"},
    runtime:{activation_path:$runtime_activation,runtime_plugin_id:$runtime_id,
      host_plugin_id:$host_id,
      agent_adapter:{activation_path:$agent_activation,plugin_id:$agent_id},
      incus_binary:$incus_binary,
      incus_user_socket:"/var/lib/incus/unix.socket.user",incus_project:"user-1000",
      endpoint_prefix:$prefix,disk_source_ceilings:["/var/lib/p-vm/endpoints","/var/lib/p-vm/grants"],
      base_image_fingerprint:$image,
      project_policy:{network:"none",filesystem_mounts:[],command:["/run/current-system/sw/bin/bash"]}}}' > "$step_dir/host.json"
bash "$P_TEST_SOURCE/tests/integration/public-egress-host-config.sh" "$step_dir/host.json"
start_daemon() {
  p daemon "$step_dir/host.json" >> "$step_dir/daemon.out" 2>> "$step_dir/daemon.err" &
  daemon_pid=$!
  local deadline=$((SECONDS+30))
  while ((SECONDS < deadline)); do
    if test -S "$socket" && rpc system.health | jq -e '.result.control_state=="ready"' >/dev/null 2>&1; then return; fi
    if ! kill -0 "$daemon_pid" 2>/dev/null; then echo 'rename daemon exited' >&2; return 1; fi
    sleep 0.1
  done
  echo 'rename daemon health unavailable' >&2
  return 1
}
wait_operation() {
  local id="$1" want="$2" result status deadline=$((SECONDS+150))
  while ((SECONDS < deadline)); do
    result=$(rpc operation.inspect "$(jq -nc --arg id "$id" '{v:1,id:$id}')")
    status=$(jq -er '.result.operation.status' <<< "$result")
    if test "$status" = "$want"; then printf '%s\n' "$result"; return; fi
    if test "$status" = blocked || test "$status" = failed || test "$status" = completed; then
      echo "unexpected operation status: $result" >&2; return 1
    fi
    sleep 0.3
  done
  echo "operation $id did not reach $want" >&2
  return 1
}
start_daemon
rpc system.capabilities | jq -e '.result.available | index("session.rename") != null' >/dev/null
created=$(rpc project.create '{"v":1,"key":"rename-bootstrap","project":"rename-lifecycle"}')
create_op=$(jq -er '.result.operation.id | select(length==36)' <<< "$created")
uuid=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created")
wait_operation "$create_op" completed >/dev/null
guest /run/current-system/sw/bin/git config user.name P
guest /run/current-system/sw/bin/git config user.email p@example.invalid
guest /run/current-system/sw/bin/bash -c \
  'printf base > tracked; git add tracked; git commit -qm base; git push origin HEAD:main'
base_oid=$(guest /run/current-system/sw/bin/git rev-parse HEAD)
created_b=$(rpc session.create '{"v":1,"key":"rename-sibling","project":"rename-lifecycle","branch":"sibling","choice":"new","source":"refs/heads/main"}')
create_b_op=$(jq -er '.result.operation.id | select(length==36)' <<< "$created_b")
uuid_b=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created_b")
wait_operation "$create_b_op" completed >/dev/null
key_digest=$(sha256sum "$state/session_keys/$uuid" | cut -d' ' -f1)
guest /usr/libexec/p/codex-adapter init
guest /run/current-system/sw/bin/bash -c \
  'printf dummy-rename > /home/p/.codex/auth.json; chmod 0600 /home/p/.codex/auth.json; printf ahead > tracked; git add tracked; git commit -qm ahead; printf untracked > private-note'
ahead_oid=$(guest /run/current-system/sw/bin/git rev-parse HEAD)
guest /run/current-system/sw/bin/bash -c \
  'setsid /run/current-system/sw/bin/sleep 300 > /tmp/rename-sleep.log 2>&1 < /dev/null & echo $! > /tmp/rename.pid'
sleep_pid=$(guest /run/current-system/sw/bin/cat /tmp/rename.pid)
guest /run/current-system/sw/bin/kill -0 "$sleep_pid"

request=$(jq -nc --arg uuid "$uuid" --arg base "$base_oid" \
  '{v:1,key:"rename-confirmed",uuid:$uuid,new_branch:"renamed",expected_old_tip:$base}')
renamed=$(rpc session.rename "$request")
rename_op=$(jq -er '.result.operation.id | select(length==36)' <<< "$renamed")
done_op=$(wait_operation "$rename_op" completed)
jq -e '.result.operation | .kind=="session.rename" and .status=="completed" and .committed==true' <<< "$done_op" >/dev/null
test "$(guest /run/current-system/sw/bin/git branch --show-current)" = renamed
test "$(guest /run/current-system/sw/bin/git rev-parse HEAD)" = "$ahead_oid"
test "$(guest /run/current-system/sw/bin/cat tracked)" = ahead
test "$(guest /run/current-system/sw/bin/cat private-note)" = untracked
test "$(guest /run/current-system/sw/bin/cat /home/p/.codex/auth.json)" = dummy-rename
guest /run/current-system/sw/bin/kill -0 "$sleep_pid"
test "$(sha256sum "$state/session_keys/$uuid" | cut -d' ' -f1)" = "$key_digest"
test -S "$endpoint_prefix/$uuid/git.sock"
inc list "^p-$uuid$" --format json | jq -e 'length==1 and .[0].status=="Running"' >/dev/null
rpc project.branches '{"v":1,"project":"rename-lifecycle","limit":8}' |
  jq -e --arg base "$base_oid" '.result.refs | length==2 and
    any(.[]; .ref=="refs/heads/renamed" and .oid==$base) and
    any(.[]; .ref=="refs/heads/sibling" and .oid==$base)' >/dev/null
guest /run/current-system/sw/bin/git push origin HEAD:renamed
if guest /run/current-system/sw/bin/git push origin HEAD:main > "$step_dir/old-push.out" 2>&1; then
  echo 'old assigned branch accepted a push after Rename' >&2
  exit 1
fi
rpc project.branches '{"v":1,"project":"rename-lifecycle","limit":8}' |
  jq -e --arg ahead "$ahead_oid" '.result.refs |
    any(.[]; .ref=="refs/heads/renamed" and .oid==$ahead) and
    all(.[]; .ref!="refs/heads/main")' >/dev/null
test "$(rpc session.rename "$request" | jq -er '.result.operation.id')" = "$rename_op"

stop_daemon
start_daemon
deadline=$((SECONDS+80))
while ((SECONDS < deadline)); do
  if rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
    jq -e '.result.session.session_condition=="ready" and .result.session.branch=="renamed"' >/dev/null; then
    break
  fi
  sleep 0.3
done
rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
  jq -e '.result.session.session_condition=="ready" and .result.session.branch=="renamed"' >/dev/null
guest /run/current-system/sw/bin/kill -0 "$sleep_pid"
guest /run/current-system/sw/bin/git push origin HEAD:renamed
test "$(guest /run/current-system/sw/bin/cat /home/p/.codex/auth.json)" = dummy-rename
test -f "$state/session_keys/$uuid_b"
inc list "^p-$uuid_b$" --format json | jq -e 'length==1 and .[0].status=="Running"' >/dev/null
echo P_RENAME_LIFECYCLE_PASS
