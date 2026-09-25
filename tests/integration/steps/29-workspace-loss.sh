# shellcheck shell=bash
# Read-only loss evidence for Git-known worktrees and retained P commits.
# All files are disposable fixtures; no authentication is used.
set -E
umask 077
step_dir="$P_TEST_TMP/step-29"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step29
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
daemon_pid=
uuid=
op_ids=()
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
  for op in "${op_ids[@]}"; do
    inc delete --force "p-workspace-$op" >/dev/null 2>&1 || true
  done
  rmdir "$endpoint_prefix" 2>/dev/null || true
}
on_error() {
  printf 'P_WORKSPACE_LOSS_FAIL line=%s status=%s\n' "$2" "$1" >&2
  for op in "${op_ids[@]}"; do
    rpc operation.inspect "$(jq -nc --arg id "$op" '{v:1,id:$id}')" 2>/dev/null |
      jq -cr '.result.operation | {id,status,phase,diagnostic:(.diagnostic // "")[:512]}' >&2 || true
  done
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
jq -s '{schema:"p.activation/v1",plugins:([.[0].plugins[0],.[1].plugins[0]])}' \
  "$step_dir/runtime-one.json" "$step_dir/host-one.json" > "$step_dir/runtime.json"

base=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
test "${#base}" -eq 64
jq -n --arg state "$state" --arg git_activation "$step_dir/git.json" \
  --arg git_id "$(jq -er '.plugins[0].id' "$step_dir/git.json")" \
  --arg runtime_activation "$step_dir/runtime.json" \
  --arg runtime_id "$(jq -er '.plugins[0].id' "$step_dir/runtime.json")" \
  --arg host_id "$(jq -er '.plugins[1].id' "$step_dir/runtime.json")" \
  --arg incus_binary "$incus_real" --arg prefix "$endpoint_prefix" --arg image "$base" \
  '{schema:"p.host/v1",state_dir:$state,
    git:{activation_path:$git_activation,source_plugin_id:$git_id,listen:"127.0.0.1:0"},
    runtime:{activation_path:$runtime_activation,runtime_plugin_id:$runtime_id,
      host_plugin_id:$host_id,incus_binary:$incus_binary,
      incus_user_socket:"/var/lib/incus/unix.socket.user",incus_project:"user-1000",
      endpoint_prefix:$prefix,disk_source_ceilings:["/var/lib/p-vm/endpoints","/var/lib/p-vm/grants"],
      base_image_fingerprint:$image,
      project_policy:{network:"none",filesystem_mounts:[],command:["/run/current-system/sw/bin/bash"]}}}' > "$step_dir/host.json"
bash "$P_TEST_SOURCE/tests/integration/public-egress-host-config.sh" "$step_dir/host.json"
start_daemon() {
  p daemon "$step_dir/host.json" >> "$step_dir/daemon.out" 2>> "$step_dir/daemon.err" &
  daemon_pid=$!
  printf '%s\n' "$daemon_pid" > "$step_dir/daemon.pid"
  local deadline=$((SECONDS+30))
  while ((SECONDS < deadline)); do
    if test -S "$socket" && rpc system.health | jq -e '.result.control_state=="ready"' >/dev/null 2>&1; then return; fi
    if ! kill -0 "$daemon_pid" 2>/dev/null; then echo 'workspace daemon exited' >&2; return 1; fi
    sleep 0.1
  done
  echo 'workspace daemon health unavailable' >&2
  return 1
}
start_daemon
rpc system.capabilities | jq -e '.result.available | index("workspace.loss.inspect") != null' >/dev/null

created=$(rpc project.create '{"v":1,"key":"loss-bootstrap","project":"workspace-loss"}')
create_op=$(jq -er '.result.operation.id | select(length==36)' <<< "$created")
uuid=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created")
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
wait_operation "$create_op" completed >/dev/null
deadline=$((SECONDS+80))
while ((SECONDS < deadline)); do
  if rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
    jq -e '.result.session.session_condition=="ready"' >/dev/null; then break; fi
  sleep 0.3
done
rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
  jq -e '.result.session.session_condition=="ready"' >/dev/null
# The pinned base must not route SFTP READDIR's UID/GID display-name lookups
# to a process frozen with the source. Inspect generated guest state before
# the first workspace freeze; do not stop or mask a live session service.
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
# Empty bootstrap is an ordinary cleanup candidate, even before its first push.
empty_response=$(rpc workspace.loss.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,key:"loss-unborn",uuid:$uuid}')")
empty_op=$(jq -er '.result.operation.id | select(length==36)' <<< "$empty_response")
op_ids+=("$empty_op")
empty_result=$(wait_operation "$empty_op" completed)
jq -e '.result.operation.evidence.result |
  .schema=="p.workspace-loss/v1" and .external_worktrees==[] and
  .local_refs==[] and .local_only_commits==[] and .p_refs==[] and
  (.worktrees|length)==1 and .worktrees[0].path=="/workspace" and
  .worktrees[0].branch=="main" and .worktrees[0].head_oid=="" and
  .worktrees[0].changes==[] and .worktrees[0].ignored.count==0 and
  .worktrees[0].ignored.logical_bytes==0' <<< "$empty_result" >/dev/null
inc list "^p-workspace-$empty_op$" --format json | jq -e 'length==0' >/dev/null
rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
  jq -e '.result.session.session_condition=="ready"' >/dev/null
guest /run/current-system/sw/bin/git config user.name P
guest /run/current-system/sw/bin/git config user.email p@example.invalid
guest /run/current-system/sw/bin/bash -c 'printf initial > tracked; printf "ignored.bin\n" > .gitignore'
guest /run/current-system/sw/bin/git add tracked .gitignore
guest /run/current-system/sw/bin/git commit -qm initial
guest /run/current-system/sw/bin/git push origin HEAD:refs/heads/main
retained_oid=$(guest /run/current-system/sw/bin/git rev-parse HEAD)
guest /run/current-system/sw/bin/mkdir -m 0700 /home/p/worktrees
guest /run/current-system/sw/bin/git worktree add -b side /home/p/worktrees/side
guest /run/current-system/sw/bin/bash -c 'printf unpublished > local-commit'
guest /run/current-system/sw/bin/git add local-commit
guest /run/current-system/sw/bin/git commit -qm unpublished
local_oid=$(guest /run/current-system/sw/bin/git rev-parse HEAD)
# A stored commit without any ref or reflog must still appear in loss evidence.
tree_oid=$(guest /run/current-system/sw/bin/git rev-parse 'HEAD^{tree}')
dangling_oid=$(guest /run/current-system/sw/bin/git commit-tree "$tree_oid" -p "$retained_oid" -m fixture-unreferenced)
guest /run/current-system/sw/bin/bash -c \
  'printf dirty-main > tracked; printf new-main > untracked; printf abc > ignored.bin'
guest /run/current-system/sw/bin/bash -c \
  'printf dirty-side > /home/p/worktrees/side/tracked; printf new-side > /home/p/worktrees/side/untracked; printf abcde > /home/p/worktrees/side/ignored.bin'

inspect_loss() {
  local key="$1" response
  response=$(rpc workspace.loss.inspect "$(jq -nc --arg key "$key" --arg uuid "$uuid" '{v:1,key:$key,uuid:$uuid}')")
  new_loss_op=$(jq -er '.result.operation.id | select(length==36)' <<< "$response")
  op_ids+=("$new_loss_op")
}
source_status() {
  inc list "^p-$uuid$" --format json | jq -er --arg name "p-$uuid" '. | select(length==1 and .[0].name==$name) | .[0].status'
}
assert_helper_absent() {
  inc list "^p-workspace-$1$" --format json | jq -e 'length==0' >/dev/null
}
assert_loss() {
  jq -e --arg local "$local_oid" --arg retained "$retained_oid" --arg dangling "$dangling_oid" '
    .result.operation.evidence.result |
    .schema=="p.workspace-loss/v1" and
    (.fingerprint | test("^[0-9a-f]{64}$")) and
    .runtime_data_will_be_removed==true and
    .external_worktrees==[] and
    (.worktrees|length)==2 and
    all(.worktrees[]; .location=="runtime") and
    any(.worktrees[]; .path=="/workspace" and .branch=="main" and .head_oid==$local and
      any(.changes[]; .path=="tracked" and .code==" M") and
      any(.changes[]; .path=="untracked" and .code=="??") and
      .ignored.count==1 and .ignored.logical_bytes==3) and
    any(.worktrees[]; .path=="/home/p/worktrees/side" and .branch=="side" and .head_oid==$retained and
      any(.changes[]; .path=="tracked" and .code==" M") and
      any(.changes[]; .path=="untracked" and .code=="??") and
      .ignored.count==1 and .ignored.logical_bytes==5) and
    any(.local_refs[]; .name=="refs/heads/main" and .oid==$local) and
    any(.local_refs[]; .name=="refs/heads/side" and .oid==$retained) and
    (.local_only_commits | sort)==([$local,$dangling]|sort) and
    (.p_refs | length)==1 and .p_refs[0].name=="refs/heads/main" and .p_refs[0].oid==$retained
  ' >/dev/null
}

inspect_loss loss-running
running_op=$new_loss_op
running_result=$(wait_operation "$running_op" completed)
assert_loss <<< "$running_result"
fingerprint=$(jq -er '.result.operation.evidence.result.fingerprint' <<< "$running_result")
test "$(source_status)" = Running
assert_helper_absent "$running_op"

# New bytes with identical path/status/count must change the source fingerprint.
guest /run/current-system/sw/bin/bash -c 'printf other-main > tracked'
inspect_loss loss-changed-bytes
changed_op=$new_loss_op
changed_result=$(wait_operation "$changed_op" completed)
assert_loss <<< "$changed_result"
changed_fingerprint=$(jq -er '.result.operation.evidence.result.fingerprint' <<< "$changed_result")
test "$changed_fingerprint" != "$fingerprint"
test "$(source_status)" = Running
assert_helper_absent "$changed_op"

rpc session.stop "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
  jq -e '.result.session.session_condition=="stopped"' >/dev/null
inspect_loss loss-stopped
stopped_op=$new_loss_op
stopped_result=$(wait_operation "$stopped_op" completed)
assert_loss <<< "$stopped_result"
test "$(source_status)" = Stopped
assert_helper_absent "$stopped_op"
rpc session.start "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" >/dev/null
deadline=$((SECONDS+80))
while ((SECONDS < deadline)); do
  if rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
    jq -e '.result.session.session_condition=="ready"' >/dev/null; then break; fi
  sleep 0.3
done
rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
  jq -e '.result.session.session_condition=="ready"' >/dev/null

# A forged Git-known worktree parent must not cause the private home to be
# copied into a helper. The credential sentinel is a dummy fixture only.
guest /run/current-system/sw/bin/mkdir -m 0700 /home/p/.codex
guest /run/current-system/sw/bin/bash -c \
  'umask 077; printf "dummy-private-credential\n" > /home/p/.codex/auth.json'
inc file pull "p-$uuid/workspace/.git/worktrees/side/gitdir" "$step_dir/gitdir-good"
guest /run/current-system/sw/bin/bash -c 'printf "/home/p/.git\n" > .git/worktrees/side/gitdir'
guest /run/current-system/sw/bin/bash -c 'printf "gitdir: /workspace/.git/worktrees/side\n" > /home/p/.git'
inspect_loss loss-protected-parent
protected_op=$new_loss_op
protected_result=$(wait_operation "$protected_op" failed)
jq -e '.result.operation.phase=="cleaned" and
  (.result.operation.diagnostic | contains("Git worktree root outside closed runtime paths"))' <<< "$protected_result" >/dev/null
test "$(source_status)" = Running
assert_helper_absent "$protected_op"
inc file pull "p-$uuid/home/p/.codex/auth.json" "$step_dir/auth-after"
printf 'dummy-private-credential\n' > "$step_dir/auth-expected"
cmp "$step_dir/auth-expected" "$step_dir/auth-after"
inc file push --uid 1000 --gid 1000 --mode 0644 "$step_dir/gitdir-good" "p-$uuid/workspace/.git/worktrees/side/gitdir"
guest /run/current-system/sw/bin/rm /home/p/.git

# Source branch and dirty bytes survive inspection; fixture teardown below
# is not evidence of public Discard/Delete or credential cleanup.
test "$(guest /run/current-system/sw/bin/git rev-parse HEAD)" = "$local_oid"
test "$(guest /run/current-system/sw/bin/cat tracked)" = other-main
test "$(guest /run/current-system/sw/bin/cat /home/p/worktrees/side/tracked)" = dirty-side
printf 'P_WORKSPACE_LOSS_PASS\n'
