# shellcheck shell=bash
# Retained branch Rename with real daemon RPC, local SSH origin, selected
# WASI Git and a disposable confined session. No external credentials.
set -E
umask 077
step_dir="$P_TEST_TMP/step-43"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step43
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
account_ssh=/home/pdev/.ssh
fixture_ssh="$step_dir/ssh"
mkdir -m 0700 "$fixture_ssh"
made_account_ssh=false
if test ! -d "$account_ssh"; then mkdir -m 0700 "$account_ssh"; made_account_ssh=true; fi
if test -e "$account_ssh/config" || test -L "$account_ssh/config"; then
  echo 'origin creation fixture requires an unused pdev SSH config' >&2
  exit 1
fi
unset SSH_AUTH_SOCK GIT_SSH GIT_SSH_COMMAND GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM GIT_CONFIG_COUNT GIT_DIR GIT_WORK_TREE
daemon_pid=
origin_pid=
sessions=()
inc() { timeout 45 incus --force-local --project user-1000 "$@"; }
rpc() { timeout 30 p api "$socket" "$@"; }
exec_p() {
  local uuid="$1"
  shift
  inc exec "p-$uuid" --user 1000 --group 1000 --cwd /workspace \
    --env HOME=/home/p --env GIT_SSH=/usr/libexec/p/git-ssh -- "$@"
}
stop_process() {
  local pid="$1" attempt
  if test -z "$pid"; then return; fi
  kill -TERM "$pid" 2>/dev/null || true
  for ((attempt=0; attempt<100; attempt++)); do
    if ! kill -0 "$pid" 2>/dev/null; then break; fi
    sleep 0.1
  done
  if kill -0 "$pid" 2>/dev/null; then kill -KILL "$pid" 2>/dev/null || true; fi
  wait "$pid" 2>/dev/null || true
}
cleanup() {
  stop_process "$daemon_pid"
  stop_process "$origin_pid"
  for uuid in "${sessions[@]}"; do
    inc delete --force "p-$uuid" >/dev/null 2>&1 || true
    rm -rf -- "${endpoint_prefix:?}/${uuid:?}"
  done
  rmdir "$endpoint_prefix" 2>/dev/null || true
  rm -f -- "$account_ssh/config"
  if test "$made_account_ssh" = true; then rmdir "$account_ssh" 2>/dev/null || true; fi
}
on_error() {
  printf 'P_RETAINED_RENAME_FAIL line=%s status=%s\n' "$2" "$1" >&2
  local result
  if result=$(timeout 5 p api "$socket" operation.list '{"v":1,"limit":20}' 2>/dev/null); then
    jq -cr '.result.operations[]? | {id,session_uuid,status,phase,diagnostic:((.diagnostic // "")[:400])}' \
      <<< "$result" >&2 || true
  fi
  for log in daemon.err origin.err api.err; do
    if test -f "$step_dir/$log"; then tail -c 2048 "$step_dir/$log" >&2 || true; fi
  done
}
trap cleanup EXIT
trap 'on_error "$?" "$LINENO"' ERR

expect_error() {
  local kind="$1" method="$2" params="$3" code response status
  case "$kind" in
    busy) code=-32003 ;;
    unavailable) code=-32004 ;;
    invalid_params) code=-32602 ;;
    *) echo "unsupported expected error: $kind" >&2; return 1 ;;
  esac
  status=0
  response=$(rpc "$method" "$params" 2> "$step_dir/api.err") || status=$?
  if test "$status" -ne 1; then
    echo "expected CLI exit 1 for $method/$kind, got $status: $response" >&2
    return 1
  fi
  jq -e --arg kind "$kind" --argjson code "$code" \
    '.jsonrpc=="2.0" and .id==1 and (has("result")|not) and
     .error.kind==$kind and .error.code==$code and (.error.message|type=="string" and length>0)' \
    <<< "$response" >/dev/null
}
operation_id() { jq -er '.result.operation.id | select(type=="string" and length==36)' <<< "$1"; }
session_uuid() { jq -er '.result.operation.session_uuid | select(type=="string" and length==36)' <<< "$1"; }
inspect_operation() { rpc operation.inspect "$(jq -nc --arg id "$1" '{v:1,id:$id}')"; }
inspect_session() { rpc session.inspect "$(jq -nc --arg uuid "$1" '{v:1,uuid:$uuid}')"; }
branches() { rpc project.branches "$(jq -nc --arg project "$1" '{v:1,project:$project,limit:8}')"; }
wait_operation() {
  local id="$1" expected="$2" result status deadline=$((SECONDS+170))
  while ((SECONDS < deadline)); do
    result=$(inspect_operation "$id")
    status=$(jq -er '.result.operation.status' <<< "$result")
    if test "$status" = "$expected"; then printf '%s\n' "$result"; return; fi
    if { test "$status" = blocked || test "$status" = completed; } && test "$status" != "$expected"; then
      echo "unexpected operation state: $result" >&2
      return 1
    fi
    sleep 0.4
  done
  echo "operation $id did not reach $expected" >&2
  return 1
}
wait_ready() {
  local uuid="$1" result deadline=$((SECONDS+80))
  while ((SECONDS < deadline)); do
    result=$(inspect_session "$uuid")
    if jq -e --arg uuid "$uuid" \
      '.result.session.uuid==$uuid and .result.session.registry_state=="established" and
       .result.session.session_condition=="ready"' <<< "$result" >/dev/null; then return; fi
    sleep 0.4
  done
  echo "session $uuid did not become ready" >&2
  return 1
}
release_on_running() {
  local uuid="$1" id="$2" deadline=$((SECONDS+80)) detail
  while ((SECONDS < deadline)); do
    detail=$(inspect_operation "$id")
    if jq -e '.result.operation.status=="blocked"' <<< "$detail" >/dev/null; then
      echo "creation blocked before runtime release: $detail" >&2
      return 1
    fi
    if inc list "p-$uuid" --format json 2>/dev/null |
      jq -e --arg name "p-$uuid" 'any(.[]; .name==$name and .status=="Running")' >/dev/null 2>&1; then
      if inc file push --uid 0 --gid 0 --mode 0644 \
        "$step_dir/release" "p-$uuid/etc/p/test-release" 2> "$step_dir/release.err"; then return; fi
    fi
    sleep 0.3
  done
  echo "runtime $uuid did not accept release" >&2
  return 1
}
start_daemon() {
  : > "$step_dir/daemon.err"
  p daemon "$step_dir/host.json" > "$step_dir/daemon.out" 2> "$step_dir/daemon.err" &
  daemon_pid=$!
  local deadline=$((SECONDS+30))
  while ((SECONDS < deadline)); do
    if test -S "$socket" && timeout 3 p api "$socket" system.health |
      jq -e '.result.control_state=="ready"' >/dev/null 2>&1; then return; fi
    if ! kill -0 "$daemon_pid" 2>/dev/null; then echo 'daemon exited before readiness' >&2; return 1; fi
    sleep 0.1
  done
  echo 'daemon did not become ready' >&2
  return 1
}
activate() {
  local package="$1" target="$2" grant="$3" digest id
  digest=$(p plugins conformance "$package" | jq -er '.sha256 | select(type=="string" and length==64)')
  id=$(jq -er '.id | select(type=="string" and length>0)' "$package/plugin.json")
  jq -n --arg path "$package" --arg digest "$digest" --arg id "$id" --arg grant "$grant" \
    '{schema:"p.activation/v1",plugins:[{id:$id,path:$path,sha256:$digest,grants:[$grant],config:{}}]}' > "$target"
}
activate "$P_TEST_GIT_PLUGIN" "$step_dir/git.json" git.project
activate "$P_TEST_RUNTIME_PLUGIN" "$step_dir/runtime-one.json" runtime.incus
mkdir -m 0700 "$step_dir/host-package"
cp "$P_TEST_SOURCE/plugins/bundled/tmux-host/"* "$step_dir/host-package/"
sed '/^\[Install\]$/i ExecStartPost=/run/current-system/sw/bin/timeout 20 /run/current-system/sw/bin/bash -c "until test -e /etc/p/test-release; do sleep 0.1; done"' \
  "$step_dir/host-package/p-interactive.service" > "$step_dir/service-gated"
mv "$step_dir/service-gated" "$step_dir/host-package/p-interactive.service"
activate "$step_dir/host-package" "$step_dir/host-one.json" session.asset.install
jq -s '{schema:"p.activation/v1",plugins:([.[0].plugins[0],.[1].plugins[0]])}' \
  "$step_dir/runtime-one.json" "$step_dir/host-one.json" > "$step_dir/runtime.json"
p plugins activate "$step_dir/git.json" | jq -e '.[0].capability=="source-git"' >/dev/null
p plugins activate "$step_dir/runtime.json" | jq -e 'length==2' >/dev/null
fingerprint=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
test "${#fingerprint}" -eq 64
git_id=$(jq -er '.plugins[0].id' "$step_dir/git.json")
runtime_id=$(jq -er '.plugins[0].id' "$step_dir/runtime.json")
host_id=$(jq -er '.plugins[1].id' "$step_dir/runtime.json")
incus_binary=$(command -v incus)
jq -n --arg state "$state" --arg git_activation "$step_dir/git.json" --arg git_id "$git_id" \
  --arg runtime_activation "$step_dir/runtime.json" --arg runtime_id "$runtime_id" \
  --arg host_id "$host_id" --arg binary "$incus_binary" --arg prefix "$endpoint_prefix" \
  --arg image "$fingerprint" \
  '{schema:"p.host/v1",state_dir:$state,
   git:{activation_path:$git_activation,source_plugin_id:$git_id,listen:"127.0.0.1:0"},
   runtime:{activation_path:$runtime_activation,runtime_plugin_id:$runtime_id,
    host_plugin_id:$host_id,incus_binary:$binary,
    incus_user_socket:"/var/lib/incus/unix.socket.user",incus_project:"user-1000",
    endpoint_prefix:$prefix,disk_source_ceilings:["/var/lib/p-vm/endpoints","/var/lib/p-vm/grants"],
    base_image_fingerprint:$image,
    project_policy:{network:"none",filesystem_mounts:[],command:["/run/current-system/sw/bin/bash"]}}}' \
  > "$step_dir/host.json"
bash "$P_TEST_SOURCE/tests/integration/public-egress-host-config.sh" "$step_dir/host.json"

empty="$step_dir/empty.git"
full="$step_dir/full.git"
git init --bare -q "$empty"
git init --bare -q "$full"
export GIT_AUTHOR_NAME=P GIT_AUTHOR_EMAIL=p@example.invalid
export GIT_COMMITTER_NAME=P GIT_COMMITTER_EMAIL=p@example.invalid
tree=$(git -C "$full" hash-object -t tree -w --stdin < /dev/null)
first=$(git -C "$full" commit-tree "$tree" -m first)
divergent=$(git -C "$full" commit-tree "$tree" -p "$first" -m divergent)
git -C "$full" update-ref refs/heads/main "$first"
git -C "$full" update-ref refs/heads/divergent "$divergent"
ssh-keygen -q -t ed25519 -N '' -f "$step_dir/origin-host" >/dev/null
ssh-keygen -q -t ed25519 -N '' -f "$fixture_ssh/id_origin" >/dev/null
trace="$step_dir/origin.trace"
lost_marker="$step_dir/drop-accepted-response"
: > "$trace"
origin-fixture serve-publish "$step_dir/origin-host" "$fixture_ssh/id_origin.pub" \
  "$empty" "$full" "$empty" "$trace" "$lost_marker" \
  > "$step_dir/origin-ready.json" 2> "$step_dir/origin.err" &
origin_pid=$!
for ((attempt=0; attempt<100; attempt++)); do
  if jq -e '.address | type=="string"' "$step_dir/origin-ready.json" >/dev/null 2>&1; then break; fi
  if ! kill -0 "$origin_pid" 2>/dev/null; then echo 'origin server exited' >&2; exit 1; fi
  sleep 0.1
done
port=$(jq -er '.address | split(":")[-1] | tonumber' "$step_dir/origin-ready.json")
awk '{print "fixture-origin " $1 " " $2}' "$step_dir/origin-host.pub" > "$fixture_ssh/known_hosts"
cat > "$account_ssh/config" <<CONFIG
Host origin-fixture
  HostName 127.0.0.1
  Port $port
  User pdev
  HostKeyAlias fixture-origin
  UserKnownHostsFile $fixture_ssh/known_hosts
  StrictHostKeyChecking yes
  IdentityFile $fixture_ssh/id_origin
  IdentitiesOnly yes
  IdentityAgent none
  BatchMode yes
  ConnectTimeout 2
CONFIG
full_url=pdev@origin-fixture:full.git
printf 'release\n' > "$step_dir/release"
start_daemon
rpc system.capabilities | jq -e '.result.available |
  index("origin.publication.preview")!=null and index("origin.publish")!=null and
  index("project.retained_branches")!=null' >/dev/null
created=$(rpc project.create "$(jq -nc --arg url "$full_url" \
  '{v:1,key:"create-project",project:"publication",url:$url}')")
wait_operation "$(operation_id "$created")" completed >/dev/null
created=$(rpc session.create "$(jq -nc --arg oid "$first" \
  '{v:1,key:"create-session",project:"publication",branch:"work",choice:"new",
    origin_ref:"refs/heads/main",expected_commit_oid:$oid}')")
uuid=$(session_uuid "$created")
id=$(operation_id "$created")
sessions+=("$uuid")
release_on_running "$uuid" "$id"
wait_operation "$id" completed >/dev/null
wait_ready "$uuid"
exec_p "$uuid" git -c user.name=P -c user.email=p@example.invalid commit --allow-empty -qm publish
source=$(exec_p "$uuid" git rev-parse HEAD)
exec_p "$uuid" git push origin HEAD:refs/heads/work
# An unpushed workspace commit must never become publication source authority.
exec_p "$uuid" git -c user.name=P -c user.email=p@example.invalid commit --allow-empty -qm unpushed
unpushed=$(exec_p "$uuid" git rev-parse HEAD)
test "$unpushed" != "$source"
repo="$state/repositories/$(printf %s publication | sha256sum | cut -d' ' -f1).git"
test "$(git -C "$repo" rev-parse refs/heads/work)" = "$source"
# Seed retained heads as fixture preconditions. Retention through session deletion
# has its own destructive-lifecycle gate; this only tests listing/publication.
git -C "$repo" update-ref refs/heads/a-retained "$source"
git -C "$repo" update-ref refs/heads/z-retained "$source"
retained() { rpc project.retained_branches "$(jq -nc --arg after "$1" '{v:1,project:"publication",after:$after,limit:1}')"; }
retained '' | jq -e --arg oid "$source" '.result |
  .branches==[{branch:"a-retained",ref:"refs/heads/a-retained",oid:$oid}] and .next=="refs/heads/a-retained"' >/dev/null
retained refs/heads/a-retained | jq -e '.result | .branches==[] and .next=="refs/heads/work"' >/dev/null
retained refs/heads/work | jq -e --arg oid "$source" '.result |
  .branches==[{branch:"z-retained",ref:"refs/heads/z-retained",oid:$oid}] and .next==""' >/dev/null

# The origin is a separate repository. Rename must never rename or push its refs.
origin_before=$(git -C "$full" for-each-ref --format='%(refname) %(objectname)')
empty_before=$(git -C "$empty" for-each-ref --format='%(refname) %(objectname)')
work_before=$(git -C "$repo" rev-parse refs/heads/work)
key_before=$(sha256sum "$state/session_keys/$uuid" | cut -d' ' -f1)
exec_p "$uuid" /run/current-system/sw/bin/bash -c 'printf dummy-private > /home/p/rename-private; chmod 0600 /home/p/rename-private'
request_rename() {
  jq -nc --arg key "$1" --arg old "$2" --arg new "$3" --arg oid "$4" \
    '{v:1,key:$key,project:"publication",old_branch:$old,new_branch:$new,expected_old_tip:$oid}'
}
capabilities=$(rpc system.capabilities)
jq -e '.result.available | index("project.retained.rename")!=null' <<< "$capabilities" >/dev/null
# Ref and assignment preconditions are checked before any durable native effect.
expect_error busy project.retained.rename "$(request_rename stale-tip a-retained archive "$first")"
expect_error busy project.retained.rename "$(request_rename assigned work archive "$source")"
git -C "$repo" update-ref refs/heads/archive "$source" 0000000000000000000000000000000000000000
expect_error busy project.retained.rename "$(request_rename occupied a-retained archive "$source")"
git -C "$repo" update-ref -d refs/heads/archive "$source"
git -C "$repo" update-ref refs/heads/a-retained "$first" "$source"
expect_error busy project.retained.rename "$(request_rename moved a-retained archive "$source")"
git -C "$repo" update-ref refs/heads/a-retained "$source" "$first"

rename_request=$(request_rename rename-retained a-retained archive "$source")
renamed=$(rpc project.retained.rename "$rename_request")
rename_id=$(operation_id "$renamed")
completed=$(wait_operation "$rename_id" completed)
jq -e '.result.operation | .kind=="project.retained.rename" and .phase=="completed" and .committed==true and .project=="publication" and .session_uuid==null' <<< "$completed" >/dev/null
if git -C "$repo" show-ref --verify --quiet refs/heads/a-retained; then
  echo 'retained source survived rename' >&2; exit 1
fi
test "$(git -C "$repo" rev-parse refs/heads/archive)" = "$source"
test "$(git -C "$repo" rev-parse refs/heads/z-retained)" = "$source"
test "$(git -C "$repo" rev-parse refs/heads/work)" = "$work_before"
test "$(git -C "$full" for-each-ref --format='%(refname) %(objectname)')" = "$origin_before"
test "$(git -C "$empty" for-each-ref --format='%(refname) %(objectname)')" = "$empty_before"
test "$(sha256sum "$state/session_keys/$uuid" | cut -d' ' -f1)" = "$key_before"
test "$(exec_p "$uuid" cat /home/p/rename-private)" = dummy-private
test "$(exec_p "$uuid" git rev-parse HEAD)" = "$unpushed"
inspect_session "$uuid" | jq -e --arg uuid "$uuid" '.result.session | .uuid==$uuid and .branch=="work" and .registry_state=="established"' >/dev/null
rpc project.retained_branches '{"v":1,"project":"publication","limit":8}' |
  jq -e --arg tip "$source" '.result.branches | any(.[]; .branch=="archive" and .oid==$tip) and all(.[]; .branch!="a-retained")' >/dev/null
# The original key is a pure replay after both effects, including restart.
test "$(operation_id "$(rpc project.retained.rename "$rename_request")")" = "$rename_id"
stop_process "$daemon_pid"
daemon_pid=
start_daemon
rpc operation.inspect "$(jq -nc --arg id "$rename_id" '{v:1,id:$id}')" |
  jq -e '.result.operation | .status=="completed" and .phase=="completed"' >/dev/null
test "$(operation_id "$(rpc project.retained.rename "$rename_request")")" = "$rename_id"
expect_error busy project.retained.rename "$(request_rename rename-retained archive a-retained "$source")"
test "$(git -C "$repo" rev-parse refs/heads/archive)" = "$source"
test "$(git -C "$full" for-each-ref --format='%(refname) %(objectname)')" = "$origin_before"
echo P_RETAINED_RENAME_PASS
