# shellcheck shell=bash
# Real public base-image Create blocked by read-only native Create preflight before
# native effect, followed by reviewed local P Git key/endpoint cleanup recovery.
set -E
umask 077
step_dir="$P_TEST_TMP/step-47"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step47
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
unset SSH_AUTH_SOCK GIT_SSH GIT_SSH_COMMAND GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM GIT_CONFIG_COUNT GIT_DIR GIT_WORK_TREE
daemon_pid=
competing=
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
  # Release a paused child before stopping its daemon, including failure paths.
  touch "$step_dir/cleanup-release"
  stop_process "$daemon_pid"
  for uuid in "${sessions[@]}"; do
    inc delete --force "p-$uuid" >/dev/null 2>&1 || true
    rm -rf -- "${endpoint_prefix:?}/${uuid:?}"
  done
  if test -n "$competing"; then inc delete --force "$competing" >/dev/null 2>&1 || true; fi
  rmdir "$endpoint_prefix" 2>/dev/null || true
}
on_error() {
  printf 'P_CREATE_REPLACE_CLEANUP_FAIL line=%s status=%s\n' "$2" "$1" >&2
  if test -n "${preview:-}"; then
    jq -c '.result.preview | {eligible,old_status,old_phase,
      unsafe_reasons:((.unsafe_reasons // [])[:8]),provisional}' \
      <<< "$preview" | head -c 2048 >&2 || true
    printf '\n' >&2
  fi
  timeout 5 p api "$socket" operation.list '{"v":1,"limit":20}' >&2 || true
  tail -c 2048 "$step_dir/daemon.err" >&2 || true
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
    jq -c --arg method "$method" --arg expected_kind "$kind" --argjson expected_code "$code" \
      --argjson actual_status "$status" \
      '{method:$method,expected_kind:$expected_kind,expected_code:$expected_code,
        actual_status:$actual_status,actual_kind:(.error.kind // null),
        actual_code:(.error.code // null),actual_message:((.error.message // "")[:256])}' \
      <<< "$response" >&2 || printf 'error response malformed for %s\n' "$method" >&2
    return 1
  fi
  if ! jq -e --arg kind "$kind" --argjson code "$code" \
    '.jsonrpc=="2.0" and .id==1 and (has("result")|not) and
     .error.kind==$kind and .error.code==$code and (.error.message|type=="string" and length>0)' \
    <<< "$response" >/dev/null; then
    jq -c --arg method "$method" --arg expected_kind "$kind" --argjson expected_code "$code" \
      --argjson actual_status "$status" \
      '{method:$method,expected_kind:$expected_kind,expected_code:$expected_code,
        actual_status:$actual_status,actual_kind:(.error.kind // null),
        actual_code:(.error.code // null),actual_message:((.error.message // "")[:256])}' \
      <<< "$response" >&2 || printf 'error response malformed for %s\n' "$method" >&2
    return 1
  fi
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
      echo 'unexpected operation state' >&2
      jq -c '.result.operation | {status,phase,diagnostic:((.diagnostic // "")[:400])}' <<< "$result" >&2
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
      echo 'creation blocked before runtime release' >&2
      jq -c '.result.operation | {status,phase,diagnostic:((.diagnostic // "")[:400])}' <<< "$detail" >&2
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
  PATH="$step_dir/bin:$PATH" p daemon "$step_dir/host.json" > "$step_dir/daemon.out" 2> "$step_dir/daemon.err" &
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
host_incus=$(command -v incus)
incus_binary="$step_dir/incus-wrapper"
printf '#!%s\n' "$(command -v bash)" > "$incus_binary"
printf 'host_incus=%q\nfixture=%q\nsleep_binary=%q\n' "$host_incus" "$step_dir" "$(command -v sleep)" >> "$incus_binary"
cat >> "$incus_binary" <<'WRAPPER'
set -euo pipefail
if test "$#" -ge 5 && test "$4" = image && test "$5" = list && test -f "$fixture/image-fault"; then
  read -r sibling < "$fixture/image-fault"
  for candidate in "$fixture/state/session_keys/"*; do
    if test -f "$candidate" && test "${candidate##*/}" != "$sibling"; then
      # A real newly generated P Git key precedes this read-only preflight.
      # No BeforeCreate dispatch gate or Incus init command has run.
      printf '%s\n' "${candidate##*/}" > "$fixture/image-fault-seen"
      exit 125
    fi
  done
fi
if test "$#" -ge 4 && test "$4" = list && test -f "$fixture/cleanup-arm"; then
  read -r old_uuid < "$fixture/cleanup-arm"
  if test -n "$old_uuid" && test ! -e "$fixture/state/session_keys/$old_uuid"; then
    : > "$fixture/cleanup-paused"
    for ((attempt=0; attempt<800; attempt++)); do
      if test -f "$fixture/cleanup-release"; then break; fi
      "$sleep_binary" 0.05
    done
    test -f "$fixture/cleanup-release" || exit 124
  fi
fi
exec "$host_incus" "$@"
WRAPPER
chmod 0700 "$incus_binary"
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


host_git=$(command -v git)
mkdir -m 0700 "$step_dir/bin"
printf 'release\n' > "$step_dir/release"
start_daemon
rpc system.capabilities | jq -e '.result.available |
  index("session.create.replace.preview")!=null and index("session.create.replace.confirm")!=null' >/dev/null
created=$(rpc project.create '{"v":1,"key":"project","project":"replacement"}')
sibling=$(session_uuid "$created")
sessions+=("$sibling")
release_on_running "$sibling" "$(operation_id "$created")"
wait_operation "$(operation_id "$created")" completed >/dev/null
wait_ready "$sibling"
exec_p "$sibling" git -c user.name=P -c user.email=p@example.invalid commit --allow-empty -qm first
exec_p "$sibling" git push origin HEAD:refs/heads/main
first=$(exec_p "$sibling" git rev-parse HEAD)
repo="$state/repositories/$(printf %s replacement | sha256sum | cut -d' ' -f1).git"
"$host_git" -C "$repo" update-ref refs/heads/source "$first"
export GIT_AUTHOR_NAME=P GIT_AUTHOR_EMAIL=p@example.invalid
export GIT_COMMITTER_NAME=P GIT_COMMITTER_EMAIL=p@example.invalid
second=$("$host_git" -C "$repo" commit-tree "$("$host_git" -C "$repo" rev-parse 'refs/heads/main^{tree}')" -p "$first" -m changed)
third=$("$host_git" -C "$repo" commit-tree "$("$host_git" -C "$repo" rev-parse 'refs/heads/main^{tree}')" -p "$second" -m stale)
key_before=$(sha256sum "$state/session_keys/$sibling" | cut -d' ' -f1)
exec_p "$sibling" /run/current-system/sw/bin/bash -c 'printf dummy-private > /home/p/replacement-private; chmod 0600 /home/p/replacement-private; printf dirty > sibling-dirty'


preview_replace() { rpc session.create.replace.preview "$1"; }
confirmation_params() {
  jq -nc --arg uuid "$old_uuid" --arg key "$1" --arg token "$2" \
    '{v:1,old_uuid:$uuid,key:$key,confirmation_token:$token}'
}
confirm_replace() { rpc session.create.replace.confirm "$(confirmation_params "$1" "$2")"; }
preview_params() {
  jq -nc --arg uuid "$old_uuid" --arg key "$1" --arg branch "$2" --arg choice "$3" --arg source "$4" \
    '{v:1,old_uuid:$uuid,key:$key,project:"replacement",branch:$branch,choice:$choice} +
      (if $choice=="new" then {source:$source} else {} end)'
}
assert_old_effects_absent() {
  test ! -e "$state/session_keys/$old_uuid"
  test ! -e "$endpoint_prefix/$old_uuid"
  inc list --format json | jq -e --arg name "p-$old_uuid" --arg id "$old_id" \
    'all(.[];.name!=$name and .name!=("p-builder-"+$id) and
      (.config["user.p.session_uuid"] // "")!=($name|ltrimstr("p-")) and
      (.config["user.p.builder_request_uuid"] // "")!=$id)' >/dev/null
}

"$host_git" -C "$repo" update-ref refs/heads/work "$first"
# Fail the native pinned-image read after principal registration but before
# the durable native dispatch gate. Production evidence must prove no attempt.
printf '%s\n' "$sibling" > "$step_dir/image-fault"
old_request='{"v":1,"key":"old-key-endpoint","project":"replacement","branch":"work","choice":"existing"}'
created=$(rpc session.create "$old_request")
old_uuid=$(session_uuid "$created")
old_id=$(operation_id "$created")
sessions+=("$old_uuid")
blocked=$(wait_operation "$old_id" blocked)
test "$(cat "$step_dir/image-fault-seen")" = "$old_uuid"
rm "$step_dir/image-fault" "$step_dir/image-fault-seen"
jq -e '.result.operation | .phase=="principals-ready" and .committed==true and
 .evidence.environment==null and .evidence.builder_tree_oid==null and
 .evidence.runtime_init_state=="not-attempted"' <<< "$blocked" >/dev/null
test -f "$state/session_keys/$old_uuid"
test -S "$endpoint_prefix/$old_uuid/git.sock"
test -S "$endpoint_prefix/$old_uuid/session.sock"
inc list --format json | jq -e --arg uuid "$old_uuid" --arg id "$old_id" \
 'all(.[];.name!=("p-"+$uuid) and .name!=("p-builder-"+$id) and
  (.config["user.p.session_uuid"] // "")!=$uuid and (.config["user.p.builder_request_uuid"] // "")!=$id)' >/dev/null
# Changed committed source keeps the same existing branch and preserves every ref.
"$host_git" -C "$repo" update-ref refs/heads/work "$second" "$first"
params=$(preview_params replace-key-endpoint work existing '')
preview=$(preview_replace "$params")
jq -e --arg uuid "$old_uuid" --arg id "$old_id" --arg image "$fingerprint" --arg oid "$second" \
 '.result.preview | .eligible==true and .old_phase=="principals-ready" and
  .new_captured_oid==$oid and .provisional.runtime=="absent" and .provisional.builder=="absent" and
  .provisional.runtime_local=="unavailable" and .provisional.session_key=="present" and
  .provisional.endpoint=="present" and .provisional.principal=="present" and
  (.provisional.cleanup | .old_uuid==$uuid and .old_operation_id==$id and .old_image_fingerprint==$image and
    (.key_fingerprint|length==64) and .key.mode==33152 and .endpoint_directory.mode==16877)' <<< "$preview" >/dev/null
token=$(jq -er '.result.preview.confirmation_token' <<< "$preview")
# Same inode with another real P Git key must fail exact public-fingerprint proof.
cp "$state/session_keys/$old_uuid" "$step_dir/reviewed-key-backup"
cat "$state/session_keys/$sibling" > "$state/session_keys/$old_uuid"
expect_error busy session.create.replace.confirm "$(confirmation_params replace-key-endpoint "$token")"
preview_bad=$(preview_replace "$params")
jq -e '.result.preview | .eligible==false and (has("confirmation_token")|not)' <<< "$preview_bad" >/dev/null
cat "$step_dir/reviewed-key-backup" > "$state/session_keys/$old_uuid"
rm "$step_dir/reviewed-key-backup"
chmod 0644 "$state/session_keys/$old_uuid"
expect_error busy session.create.replace.confirm "$(confirmation_params replace-key-endpoint "$token")"
chmod 0600 "$state/session_keys/$old_uuid"
printf preserve > "$endpoint_prefix/$old_uuid/unexpected"
expect_error busy session.create.replace.confirm "$(confirmation_params replace-key-endpoint "$token")"
test "$(cat "$endpoint_prefix/$old_uuid/unexpected")" = preserve
rm "$endpoint_prefix/$old_uuid/unexpected"
"$host_git" -C "$repo" update-ref refs/heads/work "$third" "$second"
expect_error busy session.create.replace.confirm "$(confirmation_params replace-key-endpoint "$token")"
"$host_git" -C "$repo" update-ref refs/heads/work "$second" "$third"
# A competing UUID introduced after preview refuses acceptance without cleanup.
competing="p-recovery-$old_uuid"
inc init "$fingerprint" "$competing" --profile default \
 --config security.idmap.isolated=true --config security.privileged=false \
 --config security.nesting=false --config "user.p.session_uuid=$old_uuid"
identity=$(inc list "^$competing$" --format json | jq -er '.[0].config["volatile.uuid"]')
expect_error busy session.create.replace.confirm "$(confirmation_params replace-key-endpoint "$token")"
test -f "$state/session_keys/$old_uuid"
test "$(inc list "^$competing$" --format json | jq -er '.[0].config["volatile.uuid"]')" = "$identity"
inc delete "$competing"
competing=
# Unconsumed review is invalidated by restart. The blocked old creation stays
# dormant with its real key; fresh preview binds the reopened socket identities.
old_key=$(sha256sum "$state/session_keys/$old_uuid" | cut -d' ' -f1)
stop_process "$daemon_pid"
daemon_pid=
start_daemon
expect_error busy session.create.replace.confirm "$(confirmation_params replace-key-endpoint "$token")"
test "$(sha256sum "$state/session_keys/$old_uuid" | cut -d' ' -f1)" = "$old_key"
inspect_operation "$old_id" | jq -e '.result.operation | .status=="blocked" and .phase=="principals-ready"' >/dev/null
preview=$(preview_replace "$params")
token=$(jq -er '.result.preview.confirmation_token' <<< "$preview")
refs_before=$("$host_git" -C "$repo" for-each-ref --format='%(refname) %(objectname)')
printf '%s\n' "$old_uuid" > "$step_dir/cleanup-arm"
confirmed=$(confirm_replace replace-key-endpoint "$token")
new_id=$(operation_id "$confirmed")
new_uuid=$(session_uuid "$confirmed")
sessions+=("$new_uuid")
test "$new_id" != "$old_id"
test "$new_uuid" != "$old_uuid"
pause_deadline=$((SECONDS+45))
while test ! -f "$step_dir/cleanup-paused" && ((SECONDS < pause_deadline)); do sleep 0.1; done
if test ! -f "$step_dir/cleanup-paused"; then
  inspect_operation "$new_id" | jq -c '.result.operation | {status,phase,committed,diagnostic:((.diagnostic // "")[:400])}' >&2
  echo 'fixture never reached approved key cleanup checkpoint' >&2
  exit 1
fi
inspect_operation "$new_id" | jq -e --arg old "$old_uuid" '.result.operation |
 .status=="running" and .phase=="replacement-cleanup" and .committed==false and
 .evidence.replacement_cleanup.old_uuid==$old and (.evidence.replacement_cleanup.completed // false)==false' >/dev/null
test ! -e "$state/session_keys/$old_uuid"
test -d "$endpoint_prefix/$old_uuid"
test ! -e "$state/session_keys/$new_uuid"
test ! -e "$endpoint_prefix/$new_uuid"
inspect_operation "$old_id" | jq -e '.result.operation.status=="superseded"' >/dev/null
expect_error busy operation.retry "$(jq -nc --arg id "$old_id" '{v:1,id:$id}')"
test "$(operation_id "$(rpc session.create "$old_request")")" = "$old_id"
test "$(operation_id "$(confirm_replace replace-key-endpoint "$token")")" = "$new_id"
stop_process "$daemon_pid"
daemon_pid=
# Recover the durable unfinished cleanup against a newly competing old UUID.
# Only fixture-owned machinery is removed after P proves it refuses to touch it.
competing="p-recovery-$old_uuid"
inc init "$fingerprint" "$competing" --profile default \
 --config security.idmap.isolated=true --config security.privileged=false \
 --config security.nesting=false --config "user.p.session_uuid=$old_uuid"
identity=$(inc list "^$competing$" --format json | jq -er '.[0].config["volatile.uuid"]')
rm "$step_dir/cleanup-arm"
touch "$step_dir/cleanup-release"
start_daemon
blocked=$(wait_operation "$new_id" blocked)
jq -e --arg uuid "$old_uuid" '.result.operation | .phase=="replacement-cleanup" and .committed==false and
 .evidence.replacement_cleanup.old_uuid==$uuid and (.evidence.replacement_cleanup.completed // false)==false' <<< "$blocked" >/dev/null
test "$(inc list "^$competing$" --format json | jq -er '.[0].config["volatile.uuid"]')" = "$identity"
test ! -e "$state/session_keys/$new_uuid"
test ! -e "$endpoint_prefix/$new_uuid"
test -d "$endpoint_prefix/$old_uuid"
test "$("$host_git" -C "$repo" for-each-ref --format='%(refname) %(objectname)')" = "$refs_before"
test "$(operation_id "$(confirm_replace replace-key-endpoint "$token")")" = "$new_id"
test "$(operation_id "$(rpc session.create "$old_request")")" = "$old_id"
inc delete "$competing"
competing=
rpc operation.retry "$(jq -nc --arg id "$new_id" '{v:1,id:$id}')" | jq -e --arg id "$new_id" '.result.operation.id==$id' >/dev/null
release_on_running "$new_uuid" "$new_id"
completed=$(wait_operation "$new_id" completed)
wait_ready "$new_uuid"
jq -e --arg id "$old_id" --arg uuid "$old_uuid" '.result.operation |
 .evidence.supersedes_operation_id==$id and .evidence.supersedes_uuid==$uuid and
 .evidence.replacement_cleanup.completed==true' <<< "$completed" >/dev/null
assert_old_effects_absent
test "$("$host_git" -C "$repo" for-each-ref --format='%(refname) %(objectname)')" = "$refs_before"
test "$(exec_p "$new_uuid" git rev-parse HEAD)" = "$second"
new_key=$(sha256sum "$state/session_keys/$new_uuid" | cut -d' ' -f1)
stop_process "$daemon_pid"
daemon_pid=
start_daemon
test "$(operation_id "$(confirm_replace replace-key-endpoint "$token")")" = "$new_id"
test "$(operation_id "$(rpc session.create "$old_request")")" = "$old_id"
expect_error busy operation.retry "$(jq -nc --arg id "$old_id" '{v:1,id:$id}')"
assert_old_effects_absent
test "$(sha256sum "$state/session_keys/$new_uuid" | cut -d' ' -f1)" = "$new_key"
test "$(sha256sum "$state/session_keys/$sibling" | cut -d' ' -f1)" = "$key_before"
test "$(exec_p "$sibling" cat /home/p/replacement-private)" = dummy-private
test "$(exec_p "$sibling" cat /workspace/sibling-dirty)" = dirty
test "$("$host_git" -C "$repo" rev-parse refs/heads/main)" = "$first"
rpc session.list '{"v":1,"limit":8}' | jq -e '.result.sessions | length==2 and all(.[];.registry_state=="established")' >/dev/null
rpc operation.list '{"v":1,"limit":20}' | jq -e '.result.operations | [.[]|select(.kind=="session.create")]|length==2' >/dev/null
inc list --format json | jq -e --arg old "p-$old_uuid" --arg new "p-$new_uuid" 'all(.[];.name!=$old) and ([.[]|select(.name==$new)]|length==1)' >/dev/null
echo P_CREATE_REPLACE_CLEANUP_PASS
