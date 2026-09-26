# shellcheck shell=bash
# Real failed existing-branch Create and explicit replacement. The Git wrapper
# pauses only this disposable daemon's later worker read, after source capture.
set -E
umask 077
step_dir="$P_TEST_TMP/step-45"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step45
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
unset SSH_AUTH_SOCK GIT_SSH GIT_SSH_COMMAND GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM GIT_CONFIG_COUNT GIT_DIR GIT_WORK_TREE
daemon_pid=
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
  touch "$step_dir/git-release"
  stop_process "$daemon_pid"
  for uuid in "${sessions[@]}"; do
    inc delete --force "p-$uuid" >/dev/null 2>&1 || true
    rm -rf -- "${endpoint_prefix:?}/${uuid:?}"
  done
  rmdir "$endpoint_prefix" 2>/dev/null || true
}
on_error() {
  printf 'P_CREATE_REPLACE_FAIL line=%s status=%s\n' "$2" "$1" >&2
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


# Resolve host Git before installing the fixture PATH wrapper. Host ref changes
# always use this absolute executable, so they cannot consume the pause counter.
host_git=$(command -v git)
sleep_binary=$(command -v sleep)
mkdir -m 0700 "$step_dir/bin"
printf '#!%s\n' "$(command -v bash)" > "$step_dir/bin/git"
printf 'host_git=%q\nfixture=%q\nsleep_binary=%q\n' "$host_git" "$step_dir" "$sleep_binary" >> "$step_dir/bin/git"
cat >> "$step_dir/bin/git" <<'WRAPPER'
set -euo pipefail
if test -e "$fixture/git-arm" && test "$#" -eq 6 &&
   test "$3" = show-ref && test "$4" = --verify &&
   test "$5" = --quiet && test "$6" = refs/heads/work; then
  count=0
  if test -f "$fixture/git-count"; then read -r count < "$fixture/git-count"; fi
  count=$((count+1))
  printf '%s\n' "$count" > "$fixture/git-count"
  if test "$count" -eq 2; then
    # First quiet read is public Create's capture. Second is its worker's
    # ensureBranchAssigned. Pause before reading the advanced destination.
    : > "$fixture/git-paused"
    deadline=$((SECONDS+30))
    until test -e "$fixture/git-release"; do
      if ((SECONDS >= deadline)); then exit 128; fi
      "$sleep_binary" 0.05
    done
  fi
fi
exec "$host_git" "$@"
WRAPPER
chmod 0700 "$step_dir/bin/git"
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
"$host_git" -C "$repo" update-ref refs/heads/work "$first"
export GIT_AUTHOR_NAME=P GIT_AUTHOR_EMAIL=p@example.invalid
export GIT_COMMITTER_NAME=P GIT_COMMITTER_EMAIL=p@example.invalid
second=$("$host_git" -C "$repo" commit-tree "$("$host_git" -C "$repo" rev-parse 'refs/heads/main^{tree}')" -p "$first" -m changed)
third=$("$host_git" -C "$repo" commit-tree "$("$host_git" -C "$repo" rev-parse 'refs/heads/main^{tree}')" -p "$second" -m stale)
key_before=$(sha256sum "$state/session_keys/$sibling" | cut -d' ' -f1)
exec_p "$sibling" /run/current-system/sw/bin/bash -c 'printf dummy-private > /home/p/replacement-private; chmod 0600 /home/p/replacement-private; printf dirty > sibling-dirty'

touch "$step_dir/git-arm"
old_request='{"v":1,"key":"old-create","project":"replacement","branch":"work","choice":"existing"}'
created=$(rpc session.create "$old_request")
old_uuid=$(session_uuid "$created")
old_id=$(operation_id "$created")
# Assert the public Create captured the original source before advancing it.
jq -e --arg oid "$first" '.result.operation.evidence.captured_oid==$oid' <<< "$created" >/dev/null
report_git_pause_timeout() {
  local count=missing paused=false released=false detail
  if test -f "$step_dir/git-count"; then
    read -r count < "$step_dir/git-count" || true
    count=${count:0:20}
  fi
  if test -f "$step_dir/git-paused"; then paused=true; fi
  if test -f "$step_dir/git-release"; then released=true; fi
  printf 'P_CREATE_REPLACE_GIT_WAIT count=%s paused=%s released=%s\n' \
    "$count" "$paused" "$released" >&2
  if detail=$(timeout 5 p api "$socket" operation.inspect \
    "$(jq -nc --arg id "$old_id" '{v:1,id:$id}')" 2>/dev/null); then
    jq -c '.result.operation | {status,phase,committed,diagnostic:((.diagnostic // "")[:400])}' \
      <<< "$detail" | head -c 1024 >&2 || true
    printf '\n' >&2
  fi
}
deadline=$((SECONDS+20))
until test -f "$step_dir/git-paused"; do
  if ((SECONDS >= deadline)); then
    echo 'worker never reached the fixture Git pause' >&2
    report_git_pause_timeout
    exit 1
  fi
  sleep 0.05
done
"$host_git" -C "$repo" update-ref refs/heads/work "$second" "$first"
touch "$step_dir/git-release"
blocked=$(wait_operation "$old_id" blocked)
rm "$step_dir/git-arm"
jq -e --arg oid "$first" '.result.operation | .kind=="session.create" and .phase=="source-ready" and .committed==false and .evidence.captured_oid==$oid and (.diagnostic|contains("assigned existing branch changed"))' <<< "$blocked" >/dev/null
test ! -e "$state/session_keys/$old_uuid"
test ! -e "$endpoint_prefix/$old_uuid"
inc list --format json | jq -e --arg name "p-$old_uuid" 'all(.[];.name!=$name)' >/dev/null

preview_replace() {
  rpc session.create.replace.preview "$(jq -nc --arg uuid "$old_uuid" --arg key "$1" \
    '{v:1,old_uuid:$uuid,key:$key,project:"replacement",branch:"work",choice:"existing"}')"
}
confirm_replace() {
  rpc session.create.replace.confirm "$(jq -nc --arg uuid "$old_uuid" --arg key "$1" --arg token "$2" \
    '{v:1,old_uuid:$uuid,key:$key,confirmation_token:$token}')"
}
# Unsupported new-branch replacement receives an inspectable refusal, no token.
rpc session.create.replace.preview "$(jq -nc --arg uuid "$old_uuid" \
  '{v:1,old_uuid:$uuid,key:"unsupported",project:"replacement",branch:"other",choice:"new",source:"refs/heads/main"}')" |
  jq -e '.result.preview | .eligible==false and (.unsafe_reasons|index("unsupported_replacement_choice")!=null) and (has("confirmation_token")|not)' >/dev/null
preview=$(preview_replace changed-create)
jq -e --arg uuid "$old_uuid" --arg id "$old_id" --arg old "$first" --arg new "$second" '
 .result.preview | .eligible==true and .old_uuid==$uuid and .old_operation_id==$id and
 .old_request.key=="old-create" and .old_captured_oid==$old and .new_captured_oid==$new and
 .old_policy_sha256==.new_policy_sha256 and .new_request.key=="changed-create" and
 .provisional=={assigned_ref:"preserved_existing",runtime:"absent",builder:"absent",session_key:"absent",endpoint:"absent",principal:"absent"} and
 (.confirmation_token|test("^[0-9a-f]{32}$"))' <<< "$preview" >/dev/null
token=$(jq -er '.result.preview.confirmation_token' <<< "$preview")
# Positive no-effect facts are rechecked at confirmation, including unexpected keys.
printf unexpected > "$state/session_keys/$old_uuid"
expect_error busy session.create.replace.confirm "$(jq -nc --arg uuid "$old_uuid" --arg token "$token" '{v:1,old_uuid:$uuid,key:"changed-create",confirmation_token:$token}')"
test "$(cat "$state/session_keys/$old_uuid")" = unexpected
rm "$state/session_keys/$old_uuid"
# A moved tip invalidates the token before supersession or a new reservation.
"$host_git" -C "$repo" update-ref refs/heads/work "$third" "$second"
expect_error busy session.create.replace.confirm "$(jq -nc --arg uuid "$old_uuid" --arg token "$token" '{v:1,old_uuid:$uuid,key:"changed-create",confirmation_token:$token}')"
inspect_operation "$old_id" | jq -e '.result.operation.status=="blocked"' >/dev/null
"$host_git" -C "$repo" update-ref refs/heads/work "$second" "$third"
# Unconsumed previews expire on daemon restart. The real blocked request remains.
stop_process "$daemon_pid"
daemon_pid=
start_daemon
expect_error busy session.create.replace.confirm "$(jq -nc --arg uuid "$old_uuid" --arg token "$token" '{v:1,old_uuid:$uuid,key:"changed-create",confirmation_token:$token}')"
preview=$(preview_replace changed-create)
token=$(jq -er '.result.preview.confirmation_token' <<< "$preview")
confirmed=$(confirm_replace changed-create "$token")
new_id=$(operation_id "$confirmed")
new_uuid=$(session_uuid "$confirmed")
sessions+=("$new_uuid")
test "$new_uuid" != "$old_uuid"
test "$new_id" != "$old_id"
release_on_running "$new_uuid" "$new_id"
completed=$(wait_operation "$new_id" completed)
wait_ready "$new_uuid"
jq -e --arg uuid "$old_uuid" --arg id "$old_id" --arg oid "$second" '
 .result.operation | .idempotency_key=="changed-create" and .evidence.supersedes_uuid==$uuid and
 .evidence.supersedes_operation_id==$id and .evidence.captured_oid==$oid' <<< "$completed" >/dev/null
inspect_operation "$old_id" | jq -e '.result.operation | .status=="superseded" and .phase=="superseded"' >/dev/null
expect_error busy operation.retry "$(jq -nc --arg id "$old_id" '{v:1,id:$id}')"
test "$(operation_id "$(rpc session.create "$old_request")")" = "$old_id"
test "$(operation_id "$(confirm_replace changed-create "$token")")" = "$new_id"
test "$(exec_p "$new_uuid" git rev-parse HEAD)" = "$second"
test "$("$host_git" -C "$repo" rev-parse refs/heads/work)" = "$second"
test "$("$host_git" -C "$repo" rev-parse refs/heads/main)" = "$first"
test "$(sha256sum "$state/session_keys/$sibling" | cut -d' ' -f1)" = "$key_before"
test "$(exec_p "$sibling" cat /home/p/replacement-private)" = dummy-private
test "$(exec_p "$sibling" cat /workspace/sibling-dirty)" = dirty
test ! -e "$state/session_keys/$old_uuid"
test ! -e "$endpoint_prefix/$old_uuid"
new_key=$(sha256sum "$state/session_keys/$new_uuid" | cut -d' ' -f1)
rpc session.list '{"v":1,"limit":8}' | jq -e --arg old "$old_uuid" --arg new "$new_uuid" --arg sibling "$sibling" '
 .result.sessions | length==2 and all(.[];.uuid!=$old) and any(.[];.uuid==$new) and any(.[];.uuid==$sibling)' >/dev/null
rpc operation.list '{"v":1,"limit":20}' | jq -e '.result.operations | [.[]|select(.kind=="session.create")]|length==2' >/dev/null
inc list --format json | jq -e --arg old "p-$old_uuid" --arg new "p-$new_uuid" 'all(.[];.name!=$old) and ([.[]|select(.name==$new)]|length==1)' >/dev/null
stop_process "$daemon_pid"
daemon_pid=
start_daemon
test "$(operation_id "$(confirm_replace changed-create "$token")")" = "$new_id"
test "$(operation_id "$(rpc session.create "$old_request")")" = "$old_id"
inspect_operation "$old_id" | jq -e '.result.operation.status=="superseded"' >/dev/null
wait_ready "$new_uuid"
test "$(sha256sum "$state/session_keys/$new_uuid" | cut -d' ' -f1)" = "$new_key"
test "$("$host_git" -C "$repo" rev-parse refs/heads/work)" = "$second"
echo P_CREATE_REPLACE_PASS
