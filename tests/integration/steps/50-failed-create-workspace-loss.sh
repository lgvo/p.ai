# shellcheck shell=bash
# Read-only stopped loss inspection of a real assembly-ready startup failure.
# Dummy private/credential fixtures only; no authentication or deletion RPC.
set -E
umask 077
step_dir="$P_TEST_TMP/step-50"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step50
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
daemon_pid=
uuid=
sibling=
op_ids=()
unset SSH_AUTH_SOCK GIT_SSH GIT_SSH_COMMAND GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM GIT_CONFIG_COUNT GIT_DIR GIT_WORK_TREE
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
  if test -n "$sibling"; then
    inc delete --force "p-$sibling" >/dev/null 2>&1 || true
    rm -rf -- "${endpoint_prefix:?}/${sibling:?}"
  fi
  for op in "${op_ids[@]}"; do
    inc delete --force "p-workspace-$op" >/dev/null 2>&1 || true
  done
  rmdir "$endpoint_prefix" 2>/dev/null || true
}
on_error() {
  printf 'P_FAILED_CREATE_WORKSPACE_LOSS_FAIL line=%s status=%s\n' "$2" "$1" >&2
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

incus_wrapper="$step_dir/incus-wrapper"
printf '#!%s\n' "$(command -v bash)" > "$incus_wrapper"
printf 'incus_real=%q\nfixture=%q\n' "$incus_real" "$step_dir" >> "$incus_wrapper"
cat >> "$incus_wrapper" <<'NATIVE_WRAPPER'
set -euo pipefail
if test "$#" -ge 5 && test -f "$fixture/protect-source"; then
 read -r source < "$fixture/protect-source"
 if test "$5" = "$source"; then
  case "$4" in start|stop|restart|pause|resume|delete|init)
   printf '%s %s\n' "$4" "$5" >> "$fixture/source-effects" ;;
  esac
 fi
fi
# Fixture-only failure before forwarding one exact helper delete. Actual
# failed startup, helper creation/boot/analysis and all source reads remain real.
if test "$#" -eq 5 && test "$4" = delete && test -f "$fixture/retain-helper"; then
 read -r helper < "$fixture/retain-helper"
 if test "$5" = "$helper" || { test "$helper" = pending && [[ "$5" == p-workspace-* ]]; }; then
  printf '%s\n' "$5" > "$fixture/helper-delete-seen"
  printf 'fixture helper deletion observation unavailable\n' >&2
  exit 125
 fi
fi
exec "$incus_real" "$@"
NATIVE_WRAPPER
chmod 0700 "$incus_wrapper"

base=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
test "${#base}" -eq 64
jq -n --arg state "$state" --arg git_activation "$step_dir/git.json" \
  --arg git_id "$(jq -er '.plugins[0].id' "$step_dir/git.json")" \
  --arg runtime_activation "$step_dir/runtime.json" \
  --arg runtime_id "$(jq -er '.plugins[0].id' "$step_dir/runtime.json")" \
  --arg host_id "$(jq -er '.plugins[1].id' "$step_dir/runtime.json")" \
  --arg incus_binary "$incus_wrapper" --arg prefix "$endpoint_prefix" --arg image "$base" \
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

wait_operation() {
  local id="$1" want="$2" result status deadline=$((SECONDS+170))
  while ((SECONDS < deadline)); do
    result=$(rpc operation.inspect "$(jq -nc --arg id "$id" '{v:1,id:$id}')")
    status=$(jq -er '.result.operation.status' <<< "$result")
    if test "$status" = "$want"; then printf '%s\n' "$result"; return; fi
    if test "$status" = blocked || test "$status" = failed || test "$status" = completed; then
      jq -c '.result.operation|{id,status,phase,diagnostic:((.diagnostic // "")[:512])}' <<< "$result" >&2
      return 1
    fi
    sleep 0.3
  done
  echo "operation $id did not reach $want" >&2
  return 1
}
expect_busy() {
  local method="$1" params="$2" response status=0
  response=$(rpc "$method" "$params" 2> "$step_dir/api.err") || status=$?
  test "$status" -eq 1
  jq -e '.jsonrpc=="2.0" and .id==1 and (has("result")|not) and
    .error.kind=="busy" and .error.code == -32003' <<< "$response" >/dev/null
}
inspect_creator() { rpc operation.inspect "$(jq -nc --arg id "$creator" '{v:1,id:$id}')"; }
assert_source() {
  inc list "^p-$uuid$" --format json | jq -e --arg native "$native_uuid" --arg generation "$generation" \
    'length==1 and .[0].status=="Stopped" and
     .[0].config["volatile.uuid"]==$native and .[0].config["volatile.uuid.generation"]==$generation and
     .[0].expanded_config["security.idmap.isolated"]=="true" and
     (.[] | .expanded_config["security.nesting"] // "false")=="false"' >/dev/null
  inspect_creator | jq -e --arg uuid "$uuid" --arg oid "$tip" \
    '.result.operation | .session_uuid==$uuid and .status=="blocked" and .phase=="assembly-ready" and
     .evidence.runtime_init_state=="attempted" and .evidence.captured_oid==$oid' >/dev/null
  rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
    jq -e --arg uuid "$uuid" '.result.session | .uuid==$uuid and .branch=="work" and .registry_state=="creating"' >/dev/null
  test ! -e "$step_dir/source-effects"
  test "$(git -C "$repo" for-each-ref --format='%(refname) %(objectname)')" = "$refs_before"
  test "$(sha256sum "$state/session_keys/$uuid" | cut -d' ' -f1)" = "$key_before"
  for path in workspace/tracked workspace/untracked workspace/ignored.bin workspace/.git/config \
    workspace/.git/HEAD workspace/.git/refs/heads/work workspace/.git/hooks/post-checkout \
    home/p/p-loss-private home/p/p-loss-dummy-credential etc/p/git/identity; do
    rm -f -- "$step_dir/after"
    inc file pull "p-$uuid/$path" "$step_dir/after"
    cmp "$step_dir/before-${path//\//-}" "$step_dir/after"
  done
  inc file pull "p-$sibling/workspace/tracked" "$step_dir/sibling-after"
  cmp "$step_dir/sibling-before" "$step_dir/sibling-after"
}

# Establish and commit source with a working host first. The failed creator
# uses the same selected asset packages and a newly captured false command.
created=$(rpc project.create '{"v":1,"key":"bootstrap","project":"failed-create-loss"}')
uuid=$(jq -er '.result.operation.session_uuid|select(length==36)' <<< "$created")
bootstrap=$(jq -er '.result.operation.id|select(length==36)' <<< "$created")
wait_operation "$bootstrap" completed >/dev/null
guest /run/current-system/sw/bin/bash -c 'printf committed > tracked; printf "ignored.bin\n" > .gitignore'
guest /run/current-system/sw/bin/git add tracked .gitignore
guest /run/current-system/sw/bin/git -c user.name=P -c user.email=p@example.invalid commit -qm source
guest /run/current-system/sw/bin/git push origin HEAD:refs/heads/main
tip=$(guest /run/current-system/sw/bin/git rev-parse HEAD)
inc file pull "p-$uuid/workspace/tracked" "$step_dir/sibling-before"
sibling=$uuid
uuid=
stop_daemon
jq '.runtime.project_policy.command=["/run/current-system/sw/bin/false"]' "$step_dir/host.json" > "$step_dir/host-failed.json"
mv "$step_dir/host-failed.json" "$step_dir/host.json"
start_daemon
request='{"v":1,"key":"failed-startup","project":"failed-create-loss","branch":"work","choice":"new","source":"refs/heads/main"}'
created=$(rpc session.create "$request")
uuid=$(jq -er '.result.operation.session_uuid|select(length==36)' <<< "$created")
creator=$(jq -er '.result.operation.id|select(length==36)' <<< "$created")
blocked=$(wait_operation "$creator" blocked)
jq -e --arg tip "$tip" '.result.operation | .kind=="session.create" and .phase=="assembly-ready" and
 .committed==true and (.diagnostic|type=="string" and length>0) and
 .evidence.runtime_init_state=="attempted" and .evidence.captured_oid==$tip and
 .evidence.environment==null and .evidence.environment_state==null' <<< "$blocked" >/dev/null
inc list "^p-$uuid$" --format json | jq -e 'length==1 and .[0].status=="Stopped"' >/dev/null
native_uuid=$(inc list "^p-$uuid$" --format json | jq -er '.[0].config["volatile.uuid"]')
generation=$(inc list "^p-$uuid$" --format json | jq -er '.[0].config["volatile.uuid.generation"]')
# Real stopped Incus file API edits preserve a workspace and private/dummy
# credential sentinels without launching the failed command or authenticating.
printf dirty > "$step_dir/tracked"
printf untracked > "$step_dir/untracked"
printf ignored > "$step_dir/ignored"
printf dummy-private > "$step_dir/private"
printf dummy-credential > "$step_dir/credential"
printf '#!/bin/sh\nexit 99\n' > "$step_dir/hook"
inc file push --uid 1000 --gid 1000 --mode 0644 "$step_dir/tracked" "p-$uuid/workspace/tracked"
inc file push --uid 1000 --gid 1000 --mode 0644 "$step_dir/untracked" "p-$uuid/workspace/untracked"
inc file push --uid 1000 --gid 1000 --mode 0644 "$step_dir/ignored" "p-$uuid/workspace/ignored.bin"
inc file push --uid 1000 --gid 1000 --mode 0600 "$step_dir/private" "p-$uuid/home/p/p-loss-private"
inc file push --uid 1000 --gid 1000 --mode 0600 "$step_dir/credential" "p-$uuid/home/p/p-loss-dummy-credential"
inc file push --uid 1000 --gid 1000 --mode 0755 "$step_dir/hook" "p-$uuid/workspace/.git/hooks/post-checkout"
# Executable Git settings and includes are deliberately unsupported. First
# prove read-only refusal and exact helper cleanup, then restore this reviewed
# inert config for the successful analysis below. The executable hook file
# stays as a sentinel; inspection never runs it.
inc file pull "p-$uuid/workspace/.git/config" "$step_dir/git-config-good"
cp "$step_dir/git-config-good" "$step_dir/git-config"
printf '\n[core]\n\tfsmonitor = /workspace/.git/hooks/post-checkout\n[include]\n\tpath = /home/p/p-loss-dummy-credential\n' >> "$step_dir/git-config"
inc file push --uid 1000 --gid 1000 --mode 0644 "$step_dir/git-config" "p-$uuid/workspace/.git/config"
for path in workspace/tracked workspace/untracked workspace/ignored.bin workspace/.git/config \
  workspace/.git/HEAD workspace/.git/refs/heads/work workspace/.git/hooks/post-checkout \
  home/p/p-loss-private home/p/p-loss-dummy-credential etc/p/git/identity; do
  inc file pull "p-$uuid/$path" "$step_dir/before-${path//\//-}"
done
repo="$state/repositories/$(printf %s failed-create-loss | sha256sum | cut -d' ' -f1).git"
refs_before=$(git -C "$repo" for-each-ref --format='%(refname) %(objectname)')
key_before=$(sha256sum "$state/session_keys/$uuid" | cut -d' ' -f1)
printf 'p-%s\n' "$uuid" > "$step_dir/protect-source"
assert_source
unsupported=$(rpc workspace.loss.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,key:"failed-create-loss-unsupported-config",uuid:$uuid}')")
unsupported_op=$(jq -er '.result.operation.id|select(length==36)' <<< "$unsupported")
op_ids+=("$unsupported_op")
refused=$(wait_operation "$unsupported_op" failed)
jq -e '.result.operation | .phase=="cleaned" and
 (.diagnostic|contains("Git core behavior unsupported")) and .evidence.result==null' <<< "$refused" >/dev/null
inc list "^p-workspace-$unsupported_op$" --format json | jq -e 'length==0' >/dev/null
assert_source
# Restore only the explicitly reviewed config fixture, then update the one
# expected config snapshot. Workspace/private/credential/ref baselines stay.
inc file push --uid 1000 --gid 1000 --mode 0644 "$step_dir/git-config-good" "p-$uuid/workspace/.git/config"
cp "$step_dir/git-config-good" "$step_dir/before-workspace-.git-config"
assert_source
# Block the helper's first cleanup after actual isolated analysis, retaining
# durable guard evidence across restart. The source is never activated.
# Helper UUID is allocated by the API; arm all helper deletion until its exact
# identity is available, then narrow the wrapper to that identity below.
printf pending > "$step_dir/retain-helper"
# The wrapper obtains the exact helper ID only from the native argv and stores
# it before returning its fixture failure (see setup wrapper).
inspected=$(rpc workspace.loss.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,key:"failed-create-loss",uuid:$uuid}')")
loss_op=$(jq -er '.result.operation.id|select(length==36)' <<< "$inspected")
op_ids+=("$loss_op")
printf 'p-workspace-%s\n' "$loss_op" > "$step_dir/retain-helper"
loss_blocked=$(wait_operation "$loss_op" blocked)
jq -e --arg creator "$creator" --arg native "$native_uuid" --arg generation "$generation" \
  '.result.operation | .phase=="analyzed" and .committed==true and
   .evidence.creator_operation_id==$creator and .evidence.source_incus_uuid==$native and
   .evidence.source_generation==$generation and .evidence.original_status=="Stopped" and
   (.evidence.creator_request_sha256|test("^[0-9a-f]{64}$")) and
   (.evidence.creator_evidence_sha256|test("^[0-9a-f]{64}$"))' <<< "$loss_blocked" >/dev/null
test "$(cat "$step_dir/helper-delete-seen")" = "p-workspace-$loss_op"
inc list "^p-workspace-$loss_op$" --format json |
 jq -e --arg owner "$uuid" 'length==1 and .[0].status=="Stopped" and
 .[0].config["user.p.workspace_owner"]==$owner and
 .[0].expanded_config["security.idmap.isolated"]=="true" and
 (.[] | .expanded_config["security.nesting"] // "false")=="false" and
 (.[] | .expanded_devices | keys)==["root"]' >/dev/null
assert_source
expect_busy operation.retry "$(jq -nc --arg id "$creator" '{v:1,id:$id}')"
expect_busy session.create "$request"
stop_daemon
start_daemon
expect_busy operation.retry "$(jq -nc --arg id "$creator" '{v:1,id:$id}')"
expect_busy session.create "$request"
assert_source
# Exact inspection retry completes cleanup; it does not promote or delete the
# creator, mutate the assigned ref, or consume private/dummy credential state.
rm "$step_dir/retain-helper"
rpc operation.retry "$(jq -nc --arg id "$loss_op" '{v:1,id:$id}')" >/dev/null
result=$(wait_operation "$loss_op" completed)
jq -e --arg tip "$tip" '.result.operation.evidence.result |
 .schema=="p.workspace-loss/v1" and (.fingerprint|test("^[0-9a-f]{64}$")) and
 (.worktrees|length)==1 and .worktrees[0].path=="/workspace" and
 .worktrees[0].branch=="work" and .worktrees[0].head_oid==$tip and
 any(.worktrees[0].changes[];.path=="tracked" and .code==" M") and
 any(.worktrees[0].changes[];.path=="untracked" and .code=="??") and
 .worktrees[0].ignored.count==1 and .worktrees[0].ignored.logical_bytes==7 and
 .local_only_commits==[] and any(.p_refs[];.name=="refs/heads/work" and .oid==$tip)' <<< "$result" >/dev/null
inc list "^p-workspace-$loss_op$" --format json | jq -e 'length==0' >/dev/null
assert_source
# Exact-key inspection replay retains one completed operation/helper identity.
replayed=$(rpc workspace.loss.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,key:"failed-create-loss",uuid:$uuid}')")
jq -e --arg id "$loss_op" '.result.operation.id==$id and .result.operation.status=="completed"' <<< "$replayed" >/dev/null
printf 'P_FAILED_CREATE_WORKSPACE_LOSS_PASS\n'
