# shellcheck shell=bash
# Public Rename across real Incus and Git. P changes a session's workspace
# only while the session is stopped: a running session is refused, a stopped
# one is renamed through the Incus file API without starting it.
# Credentials are dummy private files.
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
# expect_error KIND CODE REASON TARGET METHOD PARAMS: the request is refused
# with that kind, code, reason and target, and nothing else.
expect_error() {
  local kind="$1" code="$2" reason="$3" target="$4" method="$5" params="$6" response status=0
  response=$(rpc "$method" "$params") || status=$?
  test "$status" -eq 1
  jq -e --arg kind "$kind" --argjson code "$code" --arg reason "$reason" --arg target "$target" \
    '.jsonrpc=="2.0" and .id==2 and (has("result")|not) and
    .error.code==$code and .error.message==$kind and
    .error.data.reason==$reason and .error.data.target==$target' <<< "$response" >/dev/null
}
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
    result=$(rpc operation.inspect "$(jq -nc --arg id "$id" '{id:$id}')")
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
wait_ready() {
  local deadline=$((SECONDS+80))
  while ((SECONDS < deadline)); do
    if rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" |
      jq -e '.result.session.session_condition=="ready"' >/dev/null; then return; fi
    sleep 0.3
  done
  echo "session $uuid did not become ready" >&2
  return 1
}
stop_session() {
  rpc session.stop "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" |
    jq -e '.result.session.session_condition=="stopped"' >/dev/null
  inc list "^p-$uuid$" --format json | jq -e 'length==1 and .[0].status=="Stopped"' >/dev/null
}
start_session() {
  rpc session.start "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" >/dev/null
  wait_ready
}
start_daemon
rpc system.hello | jq -e '.result.capabilities | index("runtime") != null' >/dev/null
created=$(rpc project.create '{"key":"rename-bootstrap","project":"rename-lifecycle"}')
create_op=$(jq -er '.result.operation.id | select(length==36)' <<< "$created")
uuid=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created")
wait_operation "$create_op" completed >/dev/null
wait_ready
# The pinned base uses static local account lookup and runs no nscd (runtime
# substrate contract for reading a stopped workspace). Inspect generated
# guest state; do not stop or mask a live session service.
inc file pull "p-$uuid/etc/nsswitch.conf" "$step_dir/nsswitch.conf"
grep -Eq '^passwd:[[:space:]]+files[[:space:]]*$' "$step_dir/nsswitch.conf"
grep -Eq '^group:[[:space:]]+files[[:space:]]*$' "$step_dir/nsswitch.conf"
grep -Eq '^shadow:[[:space:]]+files[[:space:]]*$' "$step_dir/nsswitch.conf"
grep -Eq '^hosts:[[:space:]]+files[[:space:]]+dns[[:space:]]*$' "$step_dir/nsswitch.conf"
test "$(guest /run/current-system/sw/bin/id -u root)" = 0
test "$(guest /run/current-system/sw/bin/id -u p)" = 1000
test "$(guest /run/current-system/sw/bin/id -u nixbld1)" -gt 1000
inc exec "p-$uuid" -- /run/current-system/sw/bin/test ! -e /run/nscd/socket
inc exec "p-$uuid" -- /run/current-system/sw/bin/test ! -L /run/nscd/socket
nscd_unit=$(inc exec "p-$uuid" -- /run/current-system/sw/bin/systemctl show nscd.service \
  --no-pager -p LoadState -p ActiveState)
grep -Fxq 'LoadState=not-found' <<< "$nscd_unit"
grep -Fxq 'ActiveState=inactive' <<< "$nscd_unit"
test "$(guest /run/current-system/sw/bin/git config branch.main.remote)" = origin
test "$(guest /run/current-system/sw/bin/git config branch.main.merge)" = refs/heads/main
guest /run/current-system/sw/bin/git config user.name P
guest /run/current-system/sw/bin/git config user.email p@example.invalid
guest /run/current-system/sw/bin/bash -c \
  'printf base > tracked; git add tracked; git commit -qm base; git push origin HEAD:main'
base_oid=$(guest /run/current-system/sw/bin/git rev-parse HEAD)
created_b=$(rpc session.create '{"key":"rename-sibling","project":"rename-lifecycle","branch":"sibling","choice":"new","source":"refs/heads/main"}')
create_b_op=$(jq -er '.result.operation.id | select(length==36)' <<< "$created_b")
uuid_b=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created_b")
wait_operation "$create_b_op" completed >/dev/null
key_digest=$(sha256sum "$state/session_keys/$uuid" | cut -d' ' -f1)
guest /usr/libexec/p/codex-adapter init
guest /run/current-system/sw/bin/bash -c \
  'printf dummy-rename > /home/p/.codex/auth.json; chmod 0600 /home/p/.codex/auth.json; printf ahead > tracked; git add tracked; git commit -qm ahead; printf untracked > private-note'
ahead_oid=$(guest /run/current-system/sw/bin/git rev-parse HEAD)
guest /run/current-system/sw/bin/bash -c 'printf dirty > tracked; printf staged > staged-note; git add staged-note'
status_digest=$(guest /run/current-system/sw/bin/bash -c 'git status --porcelain=v1 -z | sha256sum' | cut -d' ' -f1)

# The running session's assignment, P refs and private work are unchanged.
assert_unrenamed() {
  local config="$1" branch="$2"
  test "$(guest /run/current-system/sw/bin/sha256sum /workspace/.git/config | cut -d' ' -f1)" = "$config"
  test "$(guest /run/current-system/sw/bin/git rev-parse HEAD)" = "$ahead_oid"
  test "$(guest /run/current-system/sw/bin/git branch --show-current)" = "$branch"
  test "$(sha256sum "$state/session_keys/$uuid" | cut -d' ' -f1)" = "$key_digest"
  test "$(guest /run/current-system/sw/bin/cat private-note)" = untracked
  test "$(guest /run/current-system/sw/bin/cat tracked)" = dirty
  test "$(guest /run/current-system/sw/bin/cat staged-note)" = staged
  test "$(guest /run/current-system/sw/bin/bash -c 'git status --porcelain=v1 -z | sha256sum' | cut -d' ' -f1)" = "$status_digest"
  test "$(guest /run/current-system/sw/bin/cat /home/p/.codex/auth.json)" = dummy-rename
  # shellcheck disable=SC2016 # The loop expands inside the guest shell.
  guest /run/current-system/sw/bin/bash -c 'test ! -e .git/refs/heads/renamed; for backup in .git/p-rename-*.json; do test ! -e "$backup"; done'
  rpc project.branches '{"project":"rename-lifecycle","limit":8}' |
    jq -e --arg base "$base_oid" '.result.refs | length==2 and
      any(.[]; .ref=="refs/heads/main" and .oid==$base) and
      any(.[]; .ref=="refs/heads/sibling" and .oid==$base)' >/dev/null
  rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" |
    jq -e '.result.session.branch=="main" and .result.session.registry_state=="established"' >/dev/null
}

# A running session is refused at the request (conflict, session_not_stopped):
# no operation starts, and its branch, P ref and workspace stay as they were.
running_config=$(guest /run/current-system/sw/bin/sha256sum /workspace/.git/config | cut -d' ' -f1)
expect_error conflict -32011 session_not_stopped "session/$uuid" session.rename "$(jq -nc --arg uuid "$uuid" --arg base "$base_oid" \
  '{key:"rename-running",uuid:$uuid,new_branch:"renamed",expected_old_tip:$base}')"
rpc operation.list '{"limit":20}' | jq -e '.result.operations |
  all(.[]; .idempotency_key!="rename-running")' >/dev/null
inc list "^p-$uuid$" --format json | jq -e 'length==1 and .[0].status=="Running"' >/dev/null
assert_unrenamed "$running_config" main

# Refuse changed upstream/HEAD of the stopped workspace before any Git
# mutation and preserve private work. Manual Git correction is the supported
# path. P never starts the session itself.
assert_mismatch_refusal() {
  local key="$1" expected="$2" actual="$3" op result before_config before_branch
  before_config=$(guest /run/current-system/sw/bin/sha256sum /workspace/.git/config | cut -d' ' -f1)
  before_branch=$(guest /run/current-system/sw/bin/git branch --show-current)
  stop_session
  result=$(rpc session.rename "$(jq -nc --arg uuid "$uuid" --arg base "$base_oid" --arg key "$key" \
    '{key:$key,uuid:$uuid,new_branch:"renamed",expected_old_tip:$base}')")
  op=$(jq -er '.result.operation.id' <<< "$result")
  result=$(wait_operation "$op" failed)
  printf 'P_RENAME_MISMATCH_OBSERVED %s\n' "$(jq -c '.result.operation | {status,phase,reason,diagnostic}' <<< "$result")"
  jq -e --arg expected "$expected" --arg actual "$actual" \
    '.result.operation | .committed==false and .phase=="stale" and .reason=="workspace_mismatch" and
      (.diagnostic | contains($expected) and contains($actual) and (contains("correct Git")|not))' \
    <<< "$result" >/dev/null
  inc list "^p-$uuid$" --format json | jq -e 'length==1 and .[0].status=="Stopped"' >/dev/null
  start_session
  assert_unrenamed "$before_config" "$before_branch"
}
guest /run/current-system/sw/bin/git config branch.main.merge refs/heads/sibling
assert_mismatch_refusal rename-upstream-mismatch origin:refs/heads/main origin:refs/heads/sibling
guest /run/current-system/sw/bin/git config branch.main.merge refs/heads/main
guest /run/current-system/sw/bin/git switch -c manual-other
assert_mismatch_refusal rename-branch-mismatch refs/heads/main refs/heads/manual-other
guest /run/current-system/sw/bin/git switch main
test "$(guest /run/current-system/sw/bin/sha256sum /workspace/.git/config | cut -d' ' -f1)" = "$running_config"

# Rename of the stopped session renames its branch, P ref and workspace and
# leaves the session stopped.
stop_session
request=$(jq -nc --arg uuid "$uuid" --arg base "$base_oid" \
  '{key:"rename-confirmed",uuid:$uuid,new_branch:"renamed",expected_old_tip:$base}')
renamed=$(rpc session.rename "$request")
rename_op=$(jq -er '.result.operation.id | select(length==36)' <<< "$renamed")
done_op=$(wait_operation "$rename_op" completed)
jq -e '.result.operation | .kind=="session.rename" and .status=="completed" and .committed==true' <<< "$done_op" >/dev/null
inc list "^p-$uuid$" --format json | jq -e 'length==1 and .[0].status=="Stopped"' >/dev/null
rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" |
  jq -e '.result.session.branch=="renamed" and .result.session.session_condition=="stopped"' >/dev/null
start_session
test "$(guest /run/current-system/sw/bin/git branch --show-current)" = renamed
test "$(guest /run/current-system/sw/bin/git rev-parse HEAD)" = "$ahead_oid"
test "$(guest /run/current-system/sw/bin/git config branch.renamed.merge)" = refs/heads/renamed
test "$(guest /run/current-system/sw/bin/cat tracked)" = dirty
test "$(guest /run/current-system/sw/bin/cat staged-note)" = staged
test "$(guest /run/current-system/sw/bin/bash -c 'git status --porcelain=v1 -z | sha256sum' | cut -d' ' -f1)" = "$status_digest"
test "$(guest /run/current-system/sw/bin/cat private-note)" = untracked
test "$(guest /run/current-system/sw/bin/cat /home/p/.codex/auth.json)" = dummy-rename
# shellcheck disable=SC2016 # The loop expands inside the guest shell.
guest /run/current-system/sw/bin/bash -c 'test ! -e .git/refs/heads/main; for backup in .git/p-rename-*.json; do test ! -e "$backup"; done'
test "$(sha256sum "$state/session_keys/$uuid" | cut -d' ' -f1)" = "$key_digest"
test -S "$endpoint_prefix/$uuid/git.sock"
echo P_RENAME_MISMATCH_MANUAL_CORRECTION_PASS
rpc project.branches '{"project":"rename-lifecycle","limit":8}' |
  jq -e --arg base "$base_oid" '.result.refs | length==2 and
    any(.[]; .ref=="refs/heads/renamed" and .oid==$base) and
    any(.[]; .ref=="refs/heads/sibling" and .oid==$base)' >/dev/null
guest /run/current-system/sw/bin/git push origin HEAD:renamed
if guest /run/current-system/sw/bin/git push origin HEAD:main > "$step_dir/old-push.out" 2>&1; then
  echo 'old assigned branch accepted a push after Rename' >&2
  exit 1
fi
rpc project.branches '{"project":"rename-lifecycle","limit":8}' |
  jq -e --arg ahead "$ahead_oid" '.result.refs |
    any(.[]; .ref=="refs/heads/renamed" and .oid==$ahead) and
    all(.[]; .ref!="refs/heads/main")' >/dev/null
test "$(rpc session.rename "$request" | jq -er '.result.operation.id')" = "$rename_op"

stop_daemon
start_daemon
deadline=$((SECONDS+80))
while ((SECONDS < deadline)); do
  if rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" |
    jq -e '.result.session.session_condition=="ready" and .result.session.branch=="renamed"' >/dev/null; then
    break
  fi
  sleep 0.3
done
rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" |
  jq -e '.result.session.session_condition=="ready" and .result.session.branch=="renamed"' >/dev/null
guest /run/current-system/sw/bin/git push origin HEAD:renamed
test "$(guest /run/current-system/sw/bin/cat /home/p/.codex/auth.json)" = dummy-rename
test -f "$state/session_keys/$uuid_b"
inc list "^p-$uuid_b$" --format json | jq -e 'length==1 and .[0].status=="Running"' >/dev/null
echo P_RENAME_LIFECYCLE_PASS
