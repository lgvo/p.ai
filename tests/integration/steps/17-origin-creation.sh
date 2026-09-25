# shellcheck shell=bash
# Origin-backed creation through the public daemon API, real OpenSSH origin,
# selected source Git, and confined Incus runtime. Runs as pdev in the VM.
set -E
umask 077
step_dir="$P_TEST_TMP/step-17"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step17
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
  printf 'P_ORIGIN_CREATION_FAIL line=%s status=%s\n' "$2" "$1" >&2
  local result id
  if result=$(timeout 5 p api "$socket" operation.list '{"v":1,"limit":20}' 2>/dev/null); then
    jq -cr '.result.operations[]? | {id,session_uuid,status,phase,diagnostic:((.diagnostic // "")[:400])}' \
      <<< "$result" >&2 || true
  fi
  for id in "${failed_id:-}" "${full_id:-}" "${empty_id:-}" "${branch_id:-}" "${tag_id:-}" "${existing_id:-}"; do
    if test -n "$id" && result=$(timeout 5 p api "$socket" operation.inspect \
      "$(jq -nc --arg id "$id" '{v:1,id:$id}')" 2>/dev/null); then
      jq -cr '.result.operation | {id,session_uuid,status,phase,diagnostic:((.diagnostic // "")[:400])}' \
        <<< "$result" >&2 || true
    fi
  done
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
# An origin project has no session until contact establishes an empty origin.
# Observe the asynchronous assignment before trying to release its host gate.
wait_bootstrap_uuid() {
  local id="$1" result uuid status deadline=$((SECONDS+50))
  while ((SECONDS < deadline)); do
    result=$(inspect_operation "$id")
    if uuid=$(session_uuid "$result"); then printf '%s\n' "$uuid"; return; fi
    status=$(jq -er '.result.operation.status' <<< "$result")
    if test "$status" != running; then
      echo "empty-origin creation ended before bootstrap assignment: $result" >&2
      return 1
    fi
    sleep 0.2
  done
  echo "empty-origin bootstrap was not assigned: $id" >&2
  return 1
}
inspect_session() { rpc session.inspect "$(jq -nc --arg uuid "$1" '{v:1,uuid:$uuid}')"; }
branches() { rpc project.branches "$(jq -nc --arg project "$1" '{v:1,project:$project,limit:8}')"; }
sources() { rpc origin.sources "$(jq -nc --arg project "$1" '{v:1,project:$project,limit:16}')"; }
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
retry_until_completed() {
  local id="$1" result status deadline=$((SECONDS+90))
  while ((SECONDS < deadline)); do
    result=$(inspect_operation "$id")
    status=$(jq -er '.result.operation.status' <<< "$result")
    if test "$status" = completed; then printf '%s\n' "$result"; return; fi
    if test "$status" = blocked; then
      test "$(rpc operation.retry "$(jq -nc --arg id "$id" '{v:1,id:$id}')" |
        jq -er '.result.operation.id')" = "$id"
    fi
    sleep 0.4
  done
  echo "operation $id did not complete after Retry" >&2
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
tree=$(git -C "$full" hash-object -t tree -w --stdin < /dev/null)
first=$(env GIT_AUTHOR_NAME=P GIT_AUTHOR_EMAIL=p@example.invalid \
  GIT_COMMITTER_NAME=P GIT_COMMITTER_EMAIL=p@example.invalid \
  git -C "$full" commit-tree "$tree" -m first)
second=$(env GIT_AUTHOR_NAME=P GIT_AUTHOR_EMAIL=p@example.invalid \
  GIT_COMMITTER_NAME=P GIT_COMMITTER_EMAIL=p@example.invalid \
  git -C "$full" commit-tree "$tree" -p "$first" -m second)
git -C "$full" update-ref refs/heads/main "$first"
env GIT_COMMITTER_NAME=P GIT_COMMITTER_EMAIL=p@example.invalid \
  git -C "$full" tag -a -m release v1 "$first"
tag_oid=$(git -C "$full" rev-parse refs/tags/v1)
ssh-keygen -q -t ed25519 -N '' -f "$step_dir/origin-host" >/dev/null
ssh-keygen -q -t ed25519 -N '' -f "$fixture_ssh/id_origin" >/dev/null
origin-fixture serve "$step_dir/origin-host" "$fixture_ssh/id_origin.pub" \
  "$empty" "$full" "$empty" "$step_dir/origin.trace" \
  > "$step_dir/origin-ready.json" 2> "$step_dir/origin.err" &
origin_pid=$!
: > "$step_dir/origin.trace"
for ((attempt=0; attempt<100; attempt++)); do
  if jq -e '.address | type=="string"' "$step_dir/origin-ready.json" >/dev/null 2>&1; then break; fi
  if ! kill -0 "$origin_pid" 2>/dev/null; then echo 'origin server exited' >&2; exit 1; fi
  sleep 0.1
done
port=$(jq -er '.address | split(":")[-1] | tonumber' "$step_dir/origin-ready.json")
awk '{print "fixture-origin " $1 " " $2}' "$step_dir/origin-host.pub" > "$fixture_ssh/known_hosts"
write_ssh_config() {
  local later_port="$1" key="$2"
  cat > "$account_ssh/config" <<CONFIG
Host origin-fixture
  HostName 127.0.0.1
  Port $port
  User pdev
  HostKeyAlias fixture-origin
  UserKnownHostsFile $fixture_ssh/known_hosts
  StrictHostKeyChecking yes
  IdentityFile $key
  IdentitiesOnly yes
  IdentityAgent none
  BatchMode yes
  ConnectTimeout 2
Host origin-later
  HostName 127.0.0.1
  Port $later_port
  User pdev
  HostKeyAlias fixture-origin
  UserKnownHostsFile $fixture_ssh/known_hosts
  StrictHostKeyChecking yes
  IdentityFile $key
  IdentitiesOnly yes
  IdentityAgent none
  BatchMode yes
  ConnectTimeout 1
CONFIG
}
write_ssh_config 1 "$fixture_ssh/id_origin"
full_url=pdev@origin-later:full.git
empty_url=ssh://pdev@origin-fixture/empty.git
printf 'release\n' > "$step_dir/release"
start_daemon
rpc system.capabilities | jq -e '.result.available | index("project.create")!=null and index("session.create")!=null' >/dev/null

# The unavailable URL leaves a durable blocked request but no visible project.
full_request=$(jq -nc --arg url "$full_url" '{v:1,key:"origin-full",project:"origin-full",url:$url}')
failed=$(rpc project.create "$full_request")
failed_id=$(operation_id "$failed")
test "$(jq -r '.result.operation.session_uuid // ""' <<< "$failed")" = ''
wait_operation "$failed_id" blocked | jq -e \
  '.result.operation.phase=="repo-pending" and .result.operation.committed==false and
   (.result.operation.diagnostic|length>0)' >/dev/null
rpc project.list '{"v":1,"limit":100}' | jq -e '.result.projects==[]' >/dev/null
expect_error busy project.create '{"v":1,"key":"origin-full","project":"other"}'
stop_process "$daemon_pid"
daemon_pid=
start_daemon
test "$(inspect_operation "$failed_id" | jq -er '.result.operation.status')" = blocked
write_ssh_config "$port" "$fixture_ssh/id_origin"
retry=$(rpc operation.retry "$(jq -nc --arg id "$failed_id" '{v:1,id:$id}')")
test "$(operation_id "$retry")" = "$failed_id"
full_id="$failed_id"
retry_until_completed "$full_id" | jq -e \
  '.result.operation.phase=="established" and .result.operation.committed==true and
   (.result.operation.session_uuid // "")==""' >/dev/null
rpc project.list '{"v":1,"limit":100}' | jq -e '.result.projects==[{path:"origin-full",registry_state:"active"}]' >/dev/null
rpc origin.inspect '{"v":1,"project":"origin-full"}' | jq -e --arg url "$full_url" \
  '.result.origin | .url==$url and .status=="fresh" and .ref_count==2' >/dev/null
sources origin-full | jq -e --arg first "$first" --arg tag "$tag_oid" \
  '.result.refs==[{ref:"refs/heads/main",oid:$first,commit_oid:$first},
                 {ref:"refs/tags/v1",oid:$tag,commit_oid:$first}]' >/dev/null
branches origin-full | jq -e '.result.refs==[]' >/dev/null
test "$(rpc project.create "$full_request" | jq -er '.result.operation.id')" = "$full_id"

# A moved ref rejects the observed OID before a UUID or branch is reserved.
git -C "$full" update-ref refs/heads/main "$second"
stale_request=$(jq -nc --arg oid "$first" \
  '{v:1,key:"stale-source",project:"origin-full",branch:"stale",choice:"new",
    origin_ref:"refs/heads/main",expected_commit_oid:$oid}')
expect_error busy session.create "$stale_request"
expect_error busy session.create "$(jq -nc --arg oid "$first" \
  '{v:1,key:"missing-source",project:"origin-full",branch:"missing",choice:"new",
    origin_ref:"refs/heads/absent",expected_commit_oid:$oid}')"
rpc system.inspect | jq -e '.result.sessions==0 and .result.operations==1' >/dev/null
branches origin-full | jq -e '.result.refs==[]' >/dev/null

# Capture the moved branch. The gated host makes this operation durably blocked
# after source capture, so restart and Retry exercise the recorded evidence.
branch_request=$(jq -nc --arg oid "$second" \
  '{v:1,key:"branch-source",project:"origin-full",branch:"from-main",choice:"new",
    origin_ref:"refs/heads/main",expected_commit_oid:$oid}')
branch_created=$(rpc session.create "$branch_request")
branch_id=$(operation_id "$branch_created")
branch_uuid=$(session_uuid "$branch_created")
sessions+=("$branch_uuid")
inspect_operation "$branch_id" | jq -e --arg oid "$second" --arg url "$full_url" \
  '.result.operation | .evidence.captured_oid==$oid and .evidence.origin_ref=="refs/heads/main" and
   .evidence.origin_url==$url and .request.expected_commit_oid==$oid' >/dev/null
wait_operation "$branch_id" blocked | jq -e --arg oid "$second" \
  '.result.operation | .committed==true and .evidence.captured_oid==$oid and
   (.phase=="assembly-ready" or .phase=="runtime-created")' >/dev/null
branches origin-full | jq -e --arg oid "$second" \
  '.result.refs==[{ref:"refs/heads/from-main",oid:$oid}]' >/dev/null

# An annotated tag captures its peeled commit, while its tag object remains
# only in the origin observation. The P repository has only assigned branches.
tag_request=$(jq -nc --arg oid "$first" \
  '{v:1,key:"tag-source",project:"origin-full",branch:"from-tag",choice:"new",
    origin_ref:"refs/tags/v1",expected_commit_oid:$oid}')
tag_created=$(rpc session.create "$tag_request")
tag_id=$(operation_id "$tag_created")
tag_uuid=$(session_uuid "$tag_created")
sessions+=("$tag_uuid")
release_on_running "$tag_uuid" "$tag_id"
wait_operation "$tag_id" completed | jq -e --arg oid "$first" \
  '.result.operation.evidence | .captured_oid==$oid and .origin_ref=="refs/tags/v1" and
   .branch_existed==false' >/dev/null
wait_ready "$tag_uuid"
test "$(exec_p "$tag_uuid" git rev-parse HEAD)" = "$first"
test "$(exec_p "$tag_uuid" git symbolic-ref -q HEAD)" = refs/heads/from-tag
branches origin-full | jq -e --arg branch "$second" --arg tag "$first" \
  '.result.refs==[{ref:"refs/heads/from-main",oid:$branch},{ref:"refs/heads/from-tag",oid:$tag}]' >/dev/null
repo="$state/repositories/$(printf %s origin-full | sha256sum | cut -d' ' -f1).git"
test -d "$repo"
test "$(git -C "$repo" for-each-ref --format='%(refname)' | wc -l)" -eq 2
repo_config=$(git -C "$repo" config --list)
if grep -q '^remote\.' <<< "$repo_config"; then
  echo 'P repository gained an external remote' >&2
  exit 1
fi

# Origin replacement does not rewrite the captured source. Once credentials
# disappear, an exact replay and Retry still use the same operation and OID.
replacement=$(rpc origin.change "$(jq -nc --arg old "$full_url" --arg url "$empty_url" \
  '{v:1,key:"replace-after-capture",project:"origin-full",kind:"set",expected_url:$old,url:$url}')")
jq -e --arg url "$empty_url" '.result.origin | .url==$url and .status=="fresh" and .ref_count==0' \
  <<< "$replacement" >/dev/null
expect_error busy session.create "$(jq -nc --arg oid "$first" \
  '{v:1,key:"after-replacement",project:"origin-full",branch:"new-after",choice:"new",
    origin_ref:"refs/tags/v1",expected_commit_oid:$oid}')"
write_ssh_config 1 "$step_dir/missing-key"
trace_before=$(wc -l < "$step_dir/origin.trace")
stop_process "$daemon_pid"
daemon_pid=
start_daemon
test "$(rpc session.create "$branch_request" | jq -er '.result.operation.id')" = "$branch_id"
test "$(wc -l < "$step_dir/origin.trace")" -eq "$trace_before"
rpc system.inspect | jq -e '.result.sessions==2 and .result.operations==3' >/dev/null
retry=$(rpc operation.retry "$(jq -nc --arg id "$branch_id" '{v:1,id:$id}')")
test "$(operation_id "$retry")" = "$branch_id"
release_on_running "$branch_uuid" "$branch_id"
wait_operation "$branch_id" completed | jq -e --arg oid "$second" --arg url "$full_url" \
  '.result.operation.evidence | .captured_oid==$oid and .origin_url==$url' >/dev/null
wait_ready "$branch_uuid"
test "$(exec_p "$branch_uuid" git rev-parse HEAD)" = "$second"
test "$(exec_p "$branch_uuid" git symbolic-ref -q HEAD)" = refs/heads/from-main
test "$(wc -l < "$step_dir/origin.trace")" -eq "$trace_before"
expect_error busy session.create "$(jq -nc --arg oid "$first" \
  '{v:1,key:"branch-source",project:"origin-full",branch:"from-main",choice:"new",
    origin_ref:"refs/heads/main",expected_commit_oid:$oid}')"

# Empty-origin creation gets exactly one unborn-main bootstrap grant and no
# artificial commit. Its first guest push creates the only main ref.
write_ssh_config "$port" "$fixture_ssh/id_origin"
empty_request=$(jq -nc --arg url "$empty_url" '{v:1,key:"origin-empty",project:"origin-empty",url:$url}')
empty_created=$(rpc project.create "$empty_request")
empty_id=$(operation_id "$empty_created")
empty_uuid=$(wait_bootstrap_uuid "$empty_id")
sessions+=("$empty_uuid")
release_on_running "$empty_uuid" "$empty_id"
wait_operation "$empty_id" completed >/dev/null
wait_ready "$empty_uuid"
test "$(rpc project.create "$empty_request" | jq -er '.result.operation.session_uuid')" = "$empty_uuid"
expect_error busy project.create "$(jq -nc --arg url "$empty_url" \
  '{v:1,key:"second-empty-bootstrap",project:"origin-empty",url:$url}')"
rpc system.inspect | jq -e '.result.projects==2 and .result.sessions==3 and .result.operations==4' >/dev/null
rpc origin.inspect '{"v":1,"project":"origin-empty"}' | jq -e --arg url "$empty_url" \
  '.result.origin | .url==$url and .status=="fresh" and .ref_count==0' >/dev/null
branches origin-empty | jq -e '.result.refs==[]' >/dev/null
test "$(exec_p "$empty_uuid" git symbolic-ref -q HEAD)" = refs/heads/main
if exec_p "$empty_uuid" git rev-parse --verify HEAD > "$step_dir/unexpected.out" 2> "$step_dir/api.err"; then
  echo 'empty origin gained an artificial root commit' >&2
  exit 1
fi
expect_error unavailable session.create '{"v":1,"key":"before-first-push","project":"origin-empty","branch":"early","choice":"new","source":"refs/heads/main"}'
exec_p "$empty_uuid" git config user.name P
exec_p "$empty_uuid" git config user.email p@example.invalid
exec_p "$empty_uuid" sh -c 'printf "first\n" > README'
exec_p "$empty_uuid" git add README
exec_p "$empty_uuid" git commit -qm first
exec_p "$empty_uuid" git push origin HEAD:main
empty_oid=$(exec_p "$empty_uuid" git rev-parse HEAD)
branches origin-empty | jq -e --arg oid "$empty_oid" \
  '.result.refs==[{ref:"refs/heads/main",oid:$oid}]' >/dev/null
test "$(git -C "$empty" for-each-ref --format='%(refname)' | wc -l)" -eq 0

# Seed one retained P ref as a fixture precondition, then exercise the public
# existing-branch path. It selects P's committed tip without origin contact.
git -C "$repo" update-ref refs/heads/retained "$first"
trace_before=$(wc -l < "$step_dir/origin.trace")
existing_request='{"v":1,"key":"existing-p-ref","project":"origin-full","branch":"retained","choice":"existing"}'
existing_created=$(rpc session.create "$existing_request")
existing_id=$(operation_id "$existing_created")
existing_uuid=$(session_uuid "$existing_created")
sessions+=("$existing_uuid")
release_on_running "$existing_uuid" "$existing_id"
wait_operation "$existing_id" completed | jq -e --arg oid "$first" \
  '.result.operation.evidence | .captured_oid==$oid and .branch_existed==true and
   (has("origin_url")|not) and (has("origin_ref")|not)' >/dev/null
wait_ready "$existing_uuid"
test "$(exec_p "$existing_uuid" git rev-parse HEAD)" = "$first"
test "$(exec_p "$existing_uuid" git symbolic-ref -q HEAD)" = refs/heads/retained
test "$(wc -l < "$step_dir/origin.trace")" -eq "$trace_before"
test "$(rpc session.create "$existing_request" | jq -er '.result.operation.id')" = "$existing_id"
rpc system.inspect | jq -e '.result.projects==2 and .result.sessions==4 and .result.operations==5' >/dev/null

# The runtime sees only its P remote. Host origin authority never enters the
# workspace, and the runtime Git identity differs from the host origin key.
host_origin_key_sha=$(sha256sum "$fixture_ssh/id_origin" | cut -d' ' -f1)
for uuid in "${sessions[@]}"; do
  remote=$(exec_p "$uuid" git config --get remote.origin.url)
  project=origin-full
  if test "$uuid" = "$empty_uuid"; then project=origin-empty; fi
  if test "$remote" != "ssh://git@p/$project"; then
    echo "runtime $uuid has unexpected Git remote: $remote" >&2
    exit 1
  fi
  if exec_p "$uuid" sh -c 'test -e /home/p/.ssh/id_origin || test -e /workspace/.ssh/id_origin'; then
    echo "origin key leaked into runtime $uuid" >&2; exit 1
  fi
  guest_key_sha=$(exec_p "$uuid" cat /etc/p/git/identity | sha256sum | cut -d' ' -f1)
  if test "$guest_key_sha" = "$host_origin_key_sha"; then
    echo "host origin key installed as runtime Git identity $uuid" >&2; exit 1
  fi
  guest_config=$(exec_p "$uuid" cat /etc/p/session.json)
  case "$guest_config" in *origin-fixture*|*origin-later*|*empty.git*|*full.git*)
    echo "external origin leaked into session config $uuid" >&2; exit 1 ;; esac
done
echo P_ORIGIN_CREATION_PASS
