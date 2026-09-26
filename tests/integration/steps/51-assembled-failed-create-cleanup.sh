# shellcheck shell=bash
# Confirmed cleanup after actual assembly-ready startup failure.
# Dummy private/credential fixtures only; no authentication.
set -E
umask 077
step_dir="$P_TEST_TMP/step-51"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step51
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
daemon_pid=
competing=
next_uuid=
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
  touch "$step_dir/completion-release"
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
  touch "$step_dir/completion-release"
  if test -n "$competing"; then inc delete --force "$competing" >/dev/null 2>&1 || true; fi
  if test -n "$next_uuid"; then
    inc delete --force "p-$next_uuid" >/dev/null 2>&1 || true
    rm -rf -- "${endpoint_prefix:?}/${next_uuid:?}"
  fi
  rmdir "$endpoint_prefix" 2>/dev/null || true
}
on_error() {
  printf 'P_ASSEMBLED_FAILED_CREATE_CLEANUP_FAIL line=%s status=%s\n' "$2" "$1" >&2
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
# Fixture-only read outage for the retired source after local-complete crash.
# Incus deletion and helper effects are real. This wrapper never deletes the
# competing identity added by the fixture after restart.
if test "$#" -eq 7 && test "$4" = list && test "$6" = --format && test "$7" = json && test -f "$fixture/native-unavailable"; then
 read -r source < "$fixture/native-unavailable"
 if test "$5" = "^$source$"; then
  printf '%s\n' "$source" > "$fixture/native-unavailable-seen"
  printf 'fixture native observation unavailable\n' >&2
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
  PATH="$step_dir/bin:$PATH" p daemon "$step_dir/host.json" >> "$step_dir/daemon.out" 2>> "$step_dir/daemon.err" &
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
host_git=$(command -v git)
mkdir -m 0700 "$step_dir/bin"
printf '#!%s\n' "$(command -v bash)" > "$step_dir/bin/git"
printf 'host_git=%q\nfixture=%q\np_binary=%q\njq_binary=%q\nsleep_binary=%q\n' "$host_git" "$step_dir" "$(command -v p)" "$(command -v jq)" "$(command -v sleep)" >> "$step_dir/bin/git"
cat >> "$step_dir/bin/git" <<'GIT_WRAPPER'
set -euo pipefail
if test -f "$fixture/completion-arm" && test "$#" -eq 6 && test "$3" = show-ref && test "$4" = --verify && test "$5" = --quiet && test "$6" = refs/heads/work; then
 read -r cleanup_id < "$fixture/completion-arm"
 if "$p_binary" api "$fixture/state/control.sock" operation.inspect "$("$jq_binary" -nc --arg id "$cleanup_id" '{v:1,id:$id}')" |
 "$jq_binary" -e '.result.operation.kind=="session.create.cleanup" and .result.operation.phase=="local-complete" and .result.operation.status=="running"' >/dev/null; then
  : > "$fixture/completion-paused"
  for ((attempt=0;attempt<800;attempt++)); do
   if test -f "$fixture/completion-release"; then break; fi
   "$sleep_binary" 0.05
  done
  test -f "$fixture/completion-release" || exit 124
 fi
fi
exec "$host_git" "$@"
GIT_WRAPPER
chmod 0700 "$step_dir/bin/git"
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
inspect_loss() {
  local key="$1" response
  response=$(rpc workspace.loss.inspect "$(jq -nc --arg uuid "$uuid" --arg key "$key" '{v:1,uuid:$uuid,key:$key}')")
  current_loss=$(jq -er '.result.operation.id|select(length==36)' <<< "$response")
  op_ids+=("$current_loss")
  wait_operation "$current_loss" completed >/dev/null
}
preview_cleanup() {
  rpc session.create.cleanup.preview "$(jq -nc --arg uuid "$uuid" --arg loss "$current_loss" '{v:1,uuid:$uuid,loss_operation_id:$loss}')"
}
confirm_params() {
  jq -nc --arg uuid "$uuid" --arg key "$1" --arg token "$2" '{v:1,uuid:$uuid,key:$key,confirmation_token:$token}'
}
assert_retained() {
  test "$(git -C "$repo" for-each-ref --format='%(refname) %(objectname)')" = "$refs_before"
  test "$(inc image list --format json | jq -c '[.[].fingerprint]|sort')" = "$images_before"
  inc file pull "p-$sibling/workspace/tracked" "$step_dir/sibling-retained"
  cmp "$step_dir/sibling-before" "$step_dir/sibling-retained"
  inc list "^p-$sibling$" --format json | jq -e 'length==1 and .[0].status=="Running"' >/dev/null
}
assert_old_effects_absent() {
  test ! -e "$state/session_keys/$uuid"
  test ! -e "$endpoint_prefix/$uuid"
  inc list --format json | jq -e --arg uuid "$uuid" --arg creator "$creator" \
    'all(.[]; .name!=("p-"+$uuid) and .name!=("p-builder-"+$creator) and
     (.config["user.p.session_uuid"] // "")!=$uuid and
     (.expanded_config["user.p.session_uuid"] // "")!=$uuid)' >/dev/null
}
images_before=$(inc image list --format json | jq -c '[.[].fingerprint]|sort')
inspect_loss reviewed-loss
preview=$(preview_cleanup)
jq -e --arg creator "$creator" --arg native "$native_uuid" --arg generation "$generation" --arg loss "$current_loss" \
 '.result.preview | .eligible==true and .old_operation_id==$creator and .old_phase=="assembly-ready" and
 .runtime.condition=="present" and .runtime.original_status=="Stopped" and .runtime.incus_uuid==$native and
 .runtime.generation==$generation and .runtime.loss_operation_id==$loss and
 .runtime.loss.schema=="p.workspace-loss/v1" and (.loss_warnings|length)==3 and
 any(.loss_warnings[];contains("private")) and any(.loss_warnings[];contains("credentials")) and
 .provisional.runtime=="present" and .provisional.runtime_local=="will_be_removed" and
 .provisional.assigned_ref=="preserved_existing" and .shared_images=="preserved" and .external_mounts=="preserved"' <<< "$preview" >/dev/null
token=$(jq -er '.result.preview.confirmation_token' <<< "$preview")
# Alter actual stopped workspace bytes after preview. Confirmation may accept
# reversible intent, but its fresh helper must refuse before source deletion.
printf changed-after-review > "$step_dir/changed"
inc file push --uid 1000 --gid 1000 --mode 0644 "$step_dir/changed" "p-$uuid/workspace/tracked"
cp "$step_dir/changed" "$step_dir/before-workspace-tracked"
confirmed=$(rpc session.create.cleanup.confirm "$(confirm_params stale-cleanup "$token")")
stale_id=$(jq -er '.result.operation.id|select(length==36)' <<< "$confirmed")
op_ids+=("$stale_id")
stale_result=$(wait_operation "$stale_id" failed)
jq -e '.result.operation | .phase=="stale" and .committed==false and
 (.diagnostic|contains("review fresh loss"))' <<< "$stale_result" >/dev/null
inc list "^p-workspace-$stale_id$" --format json | jq -e 'length==0' >/dev/null
assert_source
expect_busy operation.retry "$(jq -nc --arg id "$stale_id" '{v:1,id:$id}')"
# A new explicit loss operation, preview, token and key are the supported
# settled-stale recovery procedure. The same original identity is retained.
inspect_loss refreshed-loss
preview=$(preview_cleanup)
jq -e '.result.preview.eligible==true' <<< "$preview" >/dev/null
token=$(jq -er '.result.preview.confirmation_token' <<< "$preview")
printf 'pending\n' > "$step_dir/completion-arm"
confirmed_status=0
confirmed=$(rpc session.create.cleanup.confirm "$(confirm_params cleanup-reviewed "$token")") || confirmed_status=$?
if test "$confirmed_status" -ne 0; then
  jq -c --argjson status "$confirmed_status" \
    '{method:"session.create.cleanup.confirm",status:$status,error:{code:.error.code,kind:.error.kind,message:(.error.message // "")[:512]}}' \
    <<< "$confirmed" >&2 || true
fi
test "$confirmed_status" -eq 0
cleanup_id=$(jq -er '.result.operation.id|select(length==36)' <<< "$confirmed")
op_ids+=("$cleanup_id")
printf '%s\n' "$cleanup_id" > "$step_dir/completion-arm"
params=$(confirm_params cleanup-reviewed "$token")
deadline=$((SECONDS+100))
while test ! -f "$step_dir/completion-paused" && ((SECONDS<deadline)); do sleep 0.1; done
test -f "$step_dir/completion-paused"
rpc operation.inspect "$(jq -nc --arg id "$cleanup_id" '{v:1,id:$id}')" |
 jq -e '.result.operation | .status=="running" and .phase=="local-complete" and .committed==true and
 .evidence.runtime_absent==true and .evidence.local_complete==true' >/dev/null
inspect_creator | jq -e '.result.operation | .status=="superseded" and .phase=="superseded"' >/dev/null
rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
 jq -e '.result.session.registry_state=="removing"' >/dev/null
expect_busy operation.retry "$(jq -nc --arg id "$creator" '{v:1,id:$id}')"
replayed=$(rpc session.create "$request")
jq -e --arg id "$creator" '.result.operation.id==$id and .result.operation.status=="superseded"' <<< "$replayed" >/dev/null
assert_old_effects_absent
assert_retained
test "$(cat "$step_dir/source-effects")" = "delete p-$uuid"
# Actual crash at durable local-complete; injected read-only outage after
# restart must retain removing identity, exact cleanup intent and ref guard.
stop_daemon
rm "$step_dir/completion-arm"
touch "$step_dir/completion-release"
printf 'p-%s\n' "$uuid" > "$step_dir/native-unavailable"
start_daemon
blocked=$(wait_operation "$cleanup_id" blocked)
jq -e '.result.operation | .phase=="local-complete" and .committed==true and
 .evidence.runtime_absent==true and .evidence.local_complete==true' <<< "$blocked" >/dev/null
test "$(cat "$step_dir/native-unavailable-seen")" = "p-$uuid"
rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
 jq -e '.result.session.registry_state=="removing"' >/dev/null
assert_old_effects_absent
assert_retained
replayed=$(rpc session.create.cleanup.confirm "$params")
jq -e --arg id "$cleanup_id" '.result.operation.id==$id' <<< "$replayed" >/dev/null
expect_busy operation.retry "$(jq -nc --arg id "$creator" '{v:1,id:$id}')"
printf 'P_ASSEMBLED_CLEANUP_NATIVE_UNAVAILABLE_PRESERVED\n'
# A newly competing session UUID blocks exact absence recovery. It is fixture
# machinery: cleanup must retain it and never adopt/delete by its name.
competing="p-recovery-$uuid"
inc init "$base" "$competing" --profile default --config security.idmap.isolated=true \
 --config security.privileged=false --config security.nesting=false --config "user.p.session_uuid=$uuid"
competing_uuid=$(inc list "^$competing$" --format json | jq -er '.[0].config["volatile.uuid"]')
rm "$step_dir/native-unavailable"
rpc operation.retry "$(jq -nc --arg id "$cleanup_id" '{v:1,id:$id}')" >/dev/null
wait_operation "$cleanup_id" blocked >/dev/null
test "$(inc list "^$competing$" --format json | jq -er '.[0].config["volatile.uuid"]')" = "$competing_uuid"
assert_retained
inc delete "$competing"
competing=
rpc operation.retry "$(jq -nc --arg id "$cleanup_id" '{v:1,id:$id}')" >/dev/null
wait_operation "$cleanup_id" completed >/dev/null
assert_old_effects_absent
assert_retained
rpc session.list '{"v":1,"limit":8}' | jq -e --arg uuid "$uuid" '.result.sessions|all(.[];.uuid!=$uuid)' >/dev/null
for helper_op in "${op_ids[@]}"; do
 inc list "^p-workspace-$helper_op$" --format json | jq -e 'length==0' >/dev/null
done
test "$(cat "$step_dir/source-effects")" = "delete p-$uuid"
# Corrected Create is explicit and separate, claims the retained branch, and
# obtains a new UUID. No old Create replay may reinitialize its source.
stop_daemon
jq '.runtime.project_policy.command=["/run/current-system/sw/bin/bash"]' "$step_dir/host.json" > "$step_dir/host-corrected.json"
mv "$step_dir/host-corrected.json" "$step_dir/host.json"
start_daemon
replayed=$(rpc session.create "$request")
jq -e --arg id "$creator" '.result.operation.id==$id and .result.operation.status=="superseded"' <<< "$replayed" >/dev/null
created=$(rpc session.create '{"v":1,"key":"corrected-create","project":"failed-create-loss","branch":"work","choice":"existing"}')
next_uuid=$(jq -er '.result.operation.session_uuid|select(length==36)' <<< "$created")
next_id=$(jq -er '.result.operation.id|select(length==36)' <<< "$created")
test "$next_uuid" != "$uuid"
wait_operation "$next_id" completed >/dev/null
rpc session.inspect "$(jq -nc --arg uuid "$next_uuid" '{v:1,uuid:$uuid}')" |
 jq -e '.result.session.registry_state=="established" and .result.session.session_condition=="ready"' >/dev/null
assert_old_effects_absent
assert_retained
test "$(cat "$step_dir/source-effects")" = "delete p-$uuid"
printf 'P_ASSEMBLED_FAILED_CREATE_CLEANUP_PASS\n'
