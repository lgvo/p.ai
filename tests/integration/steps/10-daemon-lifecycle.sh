# shellcheck shell=bash
# Public lifecycle through the configured production daemon and real selected
# Git, runtime, and host packages. Runs as pdev in the disposable VM.
set -E
umask 077
step_dir="$P_TEST_TMP/step-10"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step10
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
daemon_pid=
sessions=()
obstacle=

inc() { timeout 45 incus --force-local --project user-1000 "$@"; }
rpc() { timeout 30 p api "$socket" "$@"; }
exec_p() {
  local uuid="$1"
  shift
  inc exec "p-$uuid" --user 1000 --group 1000 --cwd /workspace \
    --env HOME=/home/p --env GIT_SSH=/usr/libexec/p/git-ssh -- "$@"
}
stop_daemon() {
  if test -n "$daemon_pid"; then
    kill -TERM "$daemon_pid" 2>/dev/null || true
    for ((attempt=0; attempt<100; attempt++)); do
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
  if test -n "$obstacle"; then rm -f -- "$obstacle"; fi
  for uuid in "${sessions[@]}"; do
    inc delete --force "p-$uuid" >/dev/null 2>&1 || true
    rm -rf -- "${endpoint_prefix:?}/${uuid:?}"
  done
  rmdir "$endpoint_prefix" 2>/dev/null || true
}
on_error() {
  printf 'P_DAEMON_LIFECYCLE_FAIL line=%s status=%s\n' "$2" "$1" >&2

  # Public operation summaries identify the failing phase without exposing
  # request or evidence payloads from operation.inspect.
  local operations id detail
  if operations=$(timeout 5 p api "$socket" operation.list '{"v":1,"limit":20}' 2>/dev/null); then
    jq -cr '.result.operations[]? | {id,session_uuid,status,phase,
      diagnostic: ((.diagnostic // "")[:512])}' <<< "$operations" >&2 || true
  fi
  for id in "${retry_id:-}" "${main_id:-}" "${work_id:-}"; do
    if test -n "$id" && detail=$(timeout 5 p api "$socket" operation.inspect \
      "$(jq -nc --arg id "$id" '{v:1,id:$id}')" 2>/dev/null); then
      jq -cr '.result.operation | {id,session_uuid,status,phase,
        diagnostic: ((.diagnostic // "")[:512])}' <<< "$detail" >&2 || true
    fi
  done
  for log in daemon.err refused.err; do
    if test -f "$step_dir/$log"; then tail -c 2048 "$step_dir/$log" >&2 || true; fi
  done
  for uuid in "${sessions[@]}"; do
    inc info "p-$uuid" --show-log 2>&1 | tail -c 1024 >&2 || true
  done
}
trap cleanup EXIT
trap 'on_error "$?" "$LINENO"' ERR

activate() {
  local package="$1" target="$2" grant="$3" digest id
  digest=$(p plugins conformance "$package" | jq -er '.sha256 | select(type=="string" and length==64)')
  id=$(jq -er '.id | select(type=="string" and length>0)' "$package/plugin.json")
  jq -n --arg path "$package" --arg digest "$digest" --arg id "$id" --arg grant "$grant" \
    '{schema:"p.activation/v1",plugins:[{id:$id,path:$path,sha256:$digest,grants:[$grant],config:{}}]}' > "$target"
}
activate "$P_TEST_GIT_PLUGIN" "$step_dir/git.json" git.project
activate "$P_TEST_RUNTIME_PLUGIN" "$step_dir/runtime-one.json" runtime.incus
# The selected unit waits for a file supplied through the ordinary Incus file
# API. Its timeout gives a bounded, inspectable host activation failure.
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
runtime_id=$(jq -er '.plugins[0].id' "$step_dir/runtime.json")
host_id=$(jq -er '.plugins[1].id' "$step_dir/runtime.json")
git_id=$(jq -er '.plugins[0].id' "$step_dir/git.json")
incus_binary=$(command -v incus)
jq -n --arg state "$state" --arg git_activation "$step_dir/git.json" \
  --arg git_id "$git_id" --arg runtime_activation "$step_dir/runtime.json" \
  --arg runtime_id "$runtime_id" --arg host_id "$host_id" \
  --arg incus_binary "$incus_binary" --arg prefix "$endpoint_prefix" \
  --arg image "$fingerprint" \
  '{schema:"p.host/v1",state_dir:$state,
   git:{activation_path:$git_activation,source_plugin_id:$git_id,listen:"127.0.0.1:0"},
   runtime:{activation_path:$runtime_activation,runtime_plugin_id:$runtime_id,
    host_plugin_id:$host_id,incus_binary:$incus_binary,
    incus_user_socket:"/var/lib/incus/unix.socket.user",incus_project:"user-1000",
    endpoint_prefix:$prefix,disk_source_ceilings:["/var/lib/p-vm/endpoints","/var/lib/p-vm/grants"],
    base_image_fingerprint:$image,
    project_policy:{network:"none",filesystem_mounts:[],command:["/run/current-system/sw/bin/bash"]}}}' \
  > "$step_dir/host.json"
bash "$P_TEST_SOURCE/tests/integration/public-egress-host-config.sh" "$step_dir/host.json"

start_daemon() {
  : > "$step_dir/daemon.err"
  p daemon "$step_dir/host.json" > "$step_dir/daemon.out" 2> "$step_dir/daemon.err" &
  daemon_pid=$!
  local deadline=$((SECONDS+30))
  while ((SECONDS < deadline)); do
    if test -S "$socket" && timeout 3 p api "$socket" system.health |
      jq -e '.result.control_state=="ready"' >/dev/null 2>&1; then return; fi
    if ! kill -0 "$daemon_pid" 2>/dev/null; then
      echo 'configured daemon exited before readiness' >&2
      return 1
    fi
    sleep 0.1
  done
  echo 'configured daemon did not become ready' >&2
  return 1
}
expect_busy() {
  local response
  if response=$(rpc "$1" "$2" 2> "$step_dir/refused.err"); then
    echo "conflicting $1 unexpectedly accepted" >&2
    return 1
  fi
  jq -e '.error.kind=="busy" and .error.code == -32003' <<< "$response" >/dev/null
}
expect_unavailable() {
  local response
  if response=$(rpc "$1" "$2" 2> "$step_dir/refused.err"); then
    echo "unavailable $1 unexpectedly accepted" >&2
    return 1
  fi
  jq -e '.error.kind=="unavailable" and .error.code == -32004' <<< "$response" >/dev/null
}
operation_id() {
  jq -er '.result.operation.id | select(type=="string" and length==36)' <<< "$1"
}
session_uuid() {
  jq -er '.result.operation.session_uuid | select(type=="string" and length==36)' <<< "$1"
}
inspect_operation() {
  rpc operation.inspect "$(jq -nc --arg id "$1" '{v:1,id:$id}')"
}
inspect_session() {
  rpc session.inspect "$(jq -nc --arg uuid "$1" '{v:1,uuid:$uuid}')"
}
wait_operation() {
  local id="$1" expected="$2" result status deadline=$((SECONDS+160))
  while ((SECONDS < deadline)); do
    result=$(inspect_operation "$id")
    status=$(jq -er '.result.operation.status | select(type=="string" and length>0)' <<< "$result")
    if jq -e --arg id "$id" --arg status "$expected" \
      '.result.operation.id==$id and .result.operation.status==$status' <<< "$result" >/dev/null; then
      printf '%s\n' "$result"
      return
    fi
    if { test "$status" = blocked || test "$status" = completed; } && test "$status" != "$expected"; then
      echo "operation reached unexpected terminal status: $result" >&2
      return 1
    fi
    sleep 0.4
  done
  echo "operation did not reach $expected: $id" >&2
  return 1
}
wait_condition() {
  local uuid="$1" expected="$2" result condition last='no observation' deadline=$((SECONDS+80))
  while ((SECONDS < deadline)); do
    if result=$(inspect_session "$uuid" 2> "$step_dir/inspect.err"); then
      last=$(jq -cr '.result.session | {condition: .session_condition,
        diagnostic: (.diagnostic // "")[:512]}' <<< "$result")
      condition=$(jq -er '.result.session.session_condition | select(type=="string" and length>0)' <<< "$result")
      if jq -e --arg uuid "$uuid" --arg expected "$expected" \
        '.result.session.uuid==$uuid and .result.session.session_condition==$expected' \
        <<< "$result" >/dev/null; then printf '%s\n' "$result"; return; fi
      if test "$condition" = stopped && test "$expected" = ready; then
        echo "session stopped before readiness: $uuid $last" >&2
        return 1
      fi
    else
      last='session.inspect temporarily unavailable'
    fi
    sleep 0.4
  done
  echo "session did not reach $expected: $uuid $last" >&2
  return 1
}
release_on_running() {
  local uuid="$1" operation="${2:-}" detail deadline=$((SECONDS+80))
  : > "$step_dir/release.err"
  while ((SECONDS < deadline)); do
    if test -n "$operation" && detail=$(inspect_operation "$operation" 2>/dev/null); then
      if jq -e '.result.operation.status=="blocked"' <<< "$detail" >/dev/null; then
        echo 'creation blocked before runtime release:' >&2
        jq -cr '.result.operation | {id,session_uuid,status,phase,
          diagnostic: ((.diagnostic // "")[:512])}' <<< "$detail" >&2
        return 1
      fi
    fi
    if timeout 5 incus --force-local --project user-1000 list "p-$uuid" --format json 2>/dev/null |
      jq -e --arg name "p-$uuid" 'any(.[]; .name==$name and .status=="Running")' >/dev/null 2>&1; then
      if timeout 5 incus --force-local --project user-1000 file push --uid 0 --gid 0 --mode 0644 \
        "$step_dir/release" "p-$uuid/etc/p/test-release" 2> "$step_dir/release.err"; then return; fi
    fi
    sleep 0.3
  done
  echo "runtime did not accept release before deadline: $uuid" >&2
  if test -s "$step_dir/release.err"; then tail -c 1024 "$step_dir/release.err" >&2; fi
  return 1
}
check_session() {
  local result
  result=$(wait_condition "$1" ready)
  jq -e --arg uuid "$1" --arg project "$2" --arg branch "$3" \
    '.result.v==1 and .result.session.uuid==$uuid and .result.session.project==$project and
     .result.session.branch==$branch and .result.session.registry_state=="established" and
     .result.session.policy_condition=="current" and .result.session.session_condition=="ready" and
     (.result.session.policy_sha256|type=="string" and length==64)' <<< "$result" >/dev/null
}
check_identity() {
  test "$(rpc system.hello | jq -er '.result.instance_id | select(type=="string" and length==36)')" = "$instance_id"
  local result
  result=$(rpc system.capabilities)
  jq -e --arg server "$server_key" --arg client "$client_key" \
    '.result.lifecycle=="partial" and .result.git.host_public_key==$server and
     .result.git.client_public_key==$client and
     (.result.available|index("project.create")!=null and index("session.stop")!=null)' \
    <<< "$result" >/dev/null
  test "$(stat -c '%d:%i' "$endpoint_prefix/$main_uuid")" = "$main_endpoint_inode"
  test "$(stat -c '%d:%i' "$endpoint_prefix/$work_uuid")" = "$work_endpoint_inode"
  test -S "$endpoint_prefix/$main_uuid/git.sock"
  test -S "$endpoint_prefix/$work_uuid/session.sock"
}

printf 'release\n' > "$step_dir/release"
start_daemon
instance_id=$(rpc system.hello | jq -er '.result.instance_id | select(type=="string" and length==36)')
capabilities=$(rpc system.capabilities)
server_key=$(jq -er '.result.git.host_public_key | select(type=="string" and length>0)' <<< "$capabilities")
client_key=$(jq -er '.result.git.client_public_key | select(type=="string" and length>0)' <<< "$capabilities")
jq -e '.result.lifecycle=="partial" and
  (.result.available|index("project.create")!=null and index("operation.retry")!=null)' \
  <<< "$capabilities" >/dev/null

# A file at the selected bare-repository path blocks Git initialization before
# any session runtime work. Removing it permits exact public operation.retry.
if ! test -d "$state/repositories"; then mkdir -m 0700 "$state/repositories"; fi
obstacle="$state/repositories/$(printf %s retry-project | sha256sum | cut -d' ' -f1).git"
printf 'blocked\n' > "$obstacle"
retry_request='{"v":1,"key":"lifecycle-retry","project":"retry-project"}'
retry_created=$(rpc project.create "$retry_request")
retry_id=$(operation_id "$retry_created")
retry_uuid=$(session_uuid "$retry_created")
sessions+=("$retry_uuid")
retry_blocked=$(wait_operation "$retry_id" blocked)
jq -e '.result.operation.phase=="repo-pending" and .result.operation.committed==false and
  (.result.operation.diagnostic|type=="string" and length>0 and length<=900)' \
  <<< "$retry_blocked" >/dev/null
test "$(inc list --format json | jq --arg uuid "$retry_uuid" '[.[]|select(.name==("p-"+$uuid))]|length')" -eq 0
test "$(rpc project.create "$retry_request" | jq -er '.result.operation.id')" = "$retry_id"
expect_busy project.create '{"v":1,"key":"lifecycle-retry","project":"different"}'
rm "$obstacle"
obstacle=
retry_response=$(rpc operation.retry "$(jq -nc --arg id "$retry_id" '{v:1,id:$id}')")
test "$(operation_id "$retry_response")" = "$retry_id"
test "$(session_uuid "$retry_response")" = "$retry_uuid"
release_on_running "$retry_uuid" "$retry_id"
retry_done=$(wait_operation "$retry_id" completed)
jq -e --arg uuid "$retry_uuid" '.result.operation.session_uuid==$uuid and
  .result.operation.phase=="established" and .result.operation.committed==true' \
  <<< "$retry_done" >/dev/null
check_session "$retry_uuid" retry-project main
test "$(inc list --format json | jq --arg uuid "$retry_uuid" '[.[]|select(.name==("p-"+$uuid))]|length')" -eq 1
rpc project.branches '{"v":1,"project":"retry-project","limit":8}' |
  jq -e '.result.refs==[] and .result.next==""' >/dev/null

# Blank creation reserves unborn main. The first commit comes from the actual
# guest Git workspace and its assigned SSH principal.
project_request='{"v":1,"key":"lifecycle-main","project":"lifecycle"}'
main_created=$(rpc project.create "$project_request")
main_id=$(operation_id "$main_created")
main_uuid=$(session_uuid "$main_created")
sessions+=("$main_uuid")
release_on_running "$main_uuid" "$main_id"
wait_operation "$main_id" completed >/dev/null
check_session "$main_uuid" lifecycle main
test "$(rpc project.create "$project_request" | jq -er '.result.operation.id')" = "$main_id"
expect_busy project.create '{"v":1,"key":"lifecycle-main","project":"other"}'
test "$(exec_p "$main_uuid" git symbolic-ref -q HEAD)" = refs/heads/main
if exec_p "$main_uuid" git rev-parse --verify HEAD > "$step_dir/unexpected.out" 2> "$step_dir/refused.err"; then
  echo 'blank project received an artificial commit' >&2
  exit 1
fi
rpc project.branches '{"v":1,"project":"lifecycle","limit":8}' |
  jq -e '.result.refs==[] and .result.next==""' >/dev/null
expect_unavailable session.create '{"v":1,"key":"before-first-commit","project":"lifecycle","branch":"work","choice":"new","source":"refs/heads/main"}'
exec_p "$main_uuid" git config user.name P
exec_p "$main_uuid" git config user.email p@example.invalid
exec_p "$main_uuid" sh -c 'printf "first\n" > README'
exec_p "$main_uuid" git add README
exec_p "$main_uuid" git commit -qm first
exec_p "$main_uuid" git push origin HEAD:main
main_oid=$(exec_p "$main_uuid" git rev-parse HEAD)
rpc project.branches '{"v":1,"project":"lifecycle","limit":8}' |
  jq -e --arg oid "$main_oid" '.result.refs==[{"ref":"refs/heads/main","oid":$oid}]' >/dev/null

work_request='{"v":1,"key":"lifecycle-work","project":"lifecycle","branch":"work","choice":"new","source":"refs/heads/main"}'
work_created=$(rpc session.create "$work_request")
work_id=$(operation_id "$work_created")
work_uuid=$(session_uuid "$work_created")
sessions+=("$work_uuid")
release_on_running "$work_uuid" "$work_id"
work_done=$(wait_operation "$work_id" completed)
jq -e --arg oid "$main_oid" '.result.operation.evidence.captured_oid==$oid and
  .result.operation.phase=="established"' <<< "$work_done" >/dev/null
check_session "$work_uuid" lifecycle work
test "$(exec_p "$work_uuid" git rev-parse HEAD)" = "$main_oid"
test "$(exec_p "$work_uuid" git symbolic-ref -q HEAD)" = refs/heads/work
test "$(exec_p "$work_uuid" cat README)" = first
test "$(session_uuid "$(rpc session.create "$work_request")")" = "$work_uuid"
expect_busy session.create '{"v":1,"key":"lifecycle-work","project":"lifecycle","branch":"other","choice":"new","source":"refs/heads/main"}'
expect_busy session.create '{"v":1,"key":"assigned-branch","project":"lifecycle","branch":"work","choice":"existing"}'
rpc operation.list '{"v":1,"limit":20}' | jq -e --arg id "$work_id" \
  '.result.operations|any(.[]; .id==$id and .status=="completed")' >/dev/null
rpc session.list '{"v":1,"limit":8}' | jq -e --arg uuid "$work_uuid" \
  '.result.sessions|any(.[]; .uuid==$uuid and .session_condition=="ready")' >/dev/null
rpc project.branches '{"v":1,"project":"lifecycle","limit":8}' |
  jq -e --arg oid "$main_oid" \
    '.result.refs==[{"ref":"refs/heads/main","oid":$oid},{"ref":"refs/heads/work","oid":$oid}] and .result.next==""' >/dev/null
rpc system.inspect | jq -e '.result.projects==2 and .result.sessions==3 and .result.operations==3' >/dev/null

# Stop/Start retains the complete guest root, including unpushed Git work,
# user home, Nix store content, credentials, and the Incus instance identity.
exec_p "$work_uuid" git config user.name P
exec_p "$work_uuid" git config user.email p@example.invalid
exec_p "$work_uuid" sh -c 'printf "local\n" > local.txt; printf "dirty\n" > dirty; printf "private\n" > /home/p/private; printf "{}\n" > flake.nix'
exec_p "$work_uuid" git add local.txt flake.nix
exec_p "$work_uuid" git commit -qm local
local_oid=$(exec_p "$work_uuid" git rev-parse HEAD)
exec_p "$work_uuid" git checkout -q --detach
store_path=$(exec_p "$work_uuid" nix-store --add /workspace/README)
key_hash=$(inc file pull "p-$work_uuid/etc/p/git/identity" - | sha256sum | cut -d' ' -f1)
main_endpoint_inode=$(stat -c '%d:%i' "$endpoint_prefix/$main_uuid")
work_endpoint_inode=$(stat -c '%d:%i' "$endpoint_prefix/$work_uuid")
rpc session.stop "$(jq -nc --arg uuid "$work_uuid" '{v:1,uuid:$uuid}')" |
  jq -e '.result.session.session_condition=="stopped"' >/dev/null
inc list "p-$work_uuid" --format json | jq -e --arg name "p-$work_uuid" \
  'any(.[]; .name==$name and .status=="Stopped")' >/dev/null
rpc session.start "$(jq -nc --arg uuid "$work_uuid" '{v:1,uuid:$uuid}')" |
  jq -e '.result.session.session_condition=="starting"' >/dev/null
wait_condition "$work_uuid" ready >/dev/null
test "$(exec_p "$work_uuid" git rev-parse HEAD)" = "$local_oid"
test "$(exec_p "$work_uuid" git symbolic-ref -q HEAD || true)" = ''
test "$(exec_p "$work_uuid" cat dirty)" = dirty
test "$(exec_p "$work_uuid" cat /home/p/private)" = private
exec_p "$work_uuid" test -f flake.nix
exec_p "$work_uuid" test -e "$store_path"
test "$(inc file pull "p-$work_uuid/etc/p/git/identity" - | sha256sum | cut -d' ' -f1)" = "$key_hash"

stop_daemon
test ! -e "$socket"
start_daemon
check_identity
check_session "$main_uuid" lifecycle main
check_session "$work_uuid" lifecycle work
test "$(exec_p "$work_uuid" git ls-remote origin refs/heads/main | cut -f1)" = "$main_oid"
kill -KILL "$daemon_pid"
wait "$daemon_pid" 2>/dev/null || true
daemon_pid=
start_daemon
check_identity
check_session "$work_uuid" lifecycle work
test "$(exec_p "$work_uuid" git ls-remote origin refs/heads/main | cut -f1)" = "$main_oid"

# A registered private session key must block Start rather than silently
# rotate. Restore the same key, then exercise ordinary Start recovery.
rpc session.stop "$(jq -nc --arg uuid "$work_uuid" '{v:1,uuid:$uuid}')" |
  jq -e '.result.session.session_condition=="stopped"' >/dev/null
mv "$state/session_keys/$work_uuid" "$step_dir/work-key.saved"
if rpc session.start "$(jq -nc --arg uuid "$work_uuid" '{v:1,uuid:$uuid}')" \
  > "$step_dir/unexpected.out" 2> "$step_dir/refused.err"; then
  echo 'Start accepted a missing registered key' >&2
  exit 1
fi
test ! -e "$state/session_keys/$work_uuid"
inc list "p-$work_uuid" --format json | jq -e --arg name "p-$work_uuid" \
  'any(.[]; .name==$name and .status=="Stopped")' >/dev/null
mv "$step_dir/work-key.saved" "$state/session_keys/$work_uuid"
test "$(sha256sum "$state/session_keys/$work_uuid" | cut -d' ' -f1)" = "$key_hash"

# Remove the host gate while stopped, so the same trusted unit fails to
# activate. The watcher must stop the guest and publish a bounded diagnostic.
inc file delete "p-$work_uuid/etc/p/test-release"
rpc session.start "$(jq -nc --arg uuid "$work_uuid" '{v:1,uuid:$uuid}')" |
  jq -e '.result.session.session_condition=="starting"' >/dev/null
failed_start=$(wait_condition "$work_uuid" stopped)
jq -e '.result.session.diagnostic|type=="string" and length>0 and length<=1024' \
  <<< "$failed_start" >/dev/null
inc list "p-$work_uuid" --format json | jq -e --arg name "p-$work_uuid" \
  'any(.[]; .name==$name and .status=="Stopped")' >/dev/null
rpc session.start "$(jq -nc --arg uuid "$work_uuid" '{v:1,uuid:$uuid}')" |
  jq -e '.result.session.session_condition=="starting"' >/dev/null
release_on_running "$work_uuid"
wait_condition "$work_uuid" ready >/dev/null
test "$(exec_p "$work_uuid" git rev-parse HEAD)" = "$local_oid"
test "$(exec_p "$work_uuid" cat dirty)" = dirty
test "$(exec_p "$work_uuid" cat /home/p/private)" = private
stop_daemon
echo P_DAEMON_LIFECYCLE_PASS
