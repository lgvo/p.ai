# shellcheck shell=bash
# Selected public repair after a derived image and its one runtime are lost.
# Codex state here is dummy fixture data; no login or external credential use.
set -E
umask 077
step_dir="$P_TEST_TMP/step-38"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step38
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
daemon_pid=
target_uuid=
sibling_uuid=
op_ids=()
incus_real=$(command -v incus)

inc() { timeout 90 "$incus_real" --force-local --project user-1000 "$@"; }
rpc() { timeout 45 p api "$socket" "$@"; }
guest() {
  local uuid="$1"
  shift
  inc exec "p-$uuid" --user 1000 --group 1000 --cwd /workspace \
    --env HOME=/home/p --env NIX_REMOTE=daemon --env GIT_SSH=/usr/libexec/p/git-ssh -- "$@"
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
  if test -n "$target_uuid"; then
    inc delete --force "p-$target_uuid" >/dev/null 2>&1 || true
    rm -rf -- "${endpoint_prefix:?}/${target_uuid:?}"
  fi
  if test -n "$sibling_uuid"; then
    inc delete --force "p-$sibling_uuid" >/dev/null 2>&1 || true
    rm -rf -- "${endpoint_prefix:?}/${sibling_uuid:?}"
  fi
  rmdir "$endpoint_prefix" 2>/dev/null || true
}
on_error() {
  printf 'P_MISSING_IMAGE_REPAIR_FAIL line=%s status=%s\n' "$2" "$1" >&2
  local id
  for id in "${op_ids[@]}"; do
    rpc operation.inspect "$(jq -nc --arg id "$id" '{v:1,id:$id}')" 2>/dev/null |
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
activate "$P_TEST_SOURCE/plugins/bundled/codex-adapter" "$step_dir/agent.json" agent.status.report
activate "$P_TEST_ENV_PLUGIN" "$step_dir/environment.json" environment.nix
jq -s '{schema:"p.activation/v1",plugins:([.[0].plugins[0],.[1].plugins[0]])}' \
  "$step_dir/runtime-one.json" "$step_dir/host-one.json" > "$step_dir/runtime.json"

base_image=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
test "${#base_image}" -eq 64
# Keep this expansion in the guest shell, after the selected devShell setup.
# shellcheck disable=SC2016
pane_command='printf "%s" "$NATIVE_VALUE" > /home/p/pane-export; exec /run/current-system/sw/bin/sleep infinity'
jq -n --arg state "$state" --arg git_activation "$step_dir/git.json" \
  --arg git_id "$(jq -er '.plugins[0].id' "$step_dir/git.json")" \
  --arg runtime_activation "$step_dir/runtime.json" \
  --arg runtime_id "$(jq -er '.plugins[0].id' "$step_dir/runtime.json")" \
  --arg host_id "$(jq -er '.plugins[1].id' "$step_dir/runtime.json")" \
  --arg agent_activation "$step_dir/agent.json" \
  --arg agent_id "$(jq -er '.plugins[0].id' "$step_dir/agent.json")" \
  --arg environment_activation "$step_dir/environment.json" \
  --arg environment_id "$(jq -er '.plugins[0].id' "$step_dir/environment.json")" \
  --arg incus_binary "$incus_real" --arg prefix "$endpoint_prefix" \
  --arg image "$base_image" --arg pane_command "$pane_command" \
  '{schema:"p.host/v1",state_dir:$state,
    git:{activation_path:$git_activation,source_plugin_id:$git_id,listen:"127.0.0.1:0"},
    runtime:{activation_path:$runtime_activation,runtime_plugin_id:$runtime_id,
      host_plugin_id:$host_id,agent_adapter:{activation_path:$agent_activation,plugin_id:$agent_id},
      incus_binary:$incus_binary,incus_user_socket:"/var/lib/incus/unix.socket.user",
      incus_project:"user-1000",endpoint_prefix:$prefix,
      disk_source_ceilings:["/var/lib/p-vm/endpoints","/var/lib/p-vm/grants"],
      base_image_fingerprint:$image,
      project_policy:{network:"none",filesystem_mounts:[],
        command:["/run/current-system/sw/bin/bash","-c",$pane_command]},
      environment:{activation_path:$environment_activation,plugin_id:$environment_id,
        system:"x86_64-linux",builder_storage_pool:"builders"}}}' > "$step_dir/host.json"
bash "$P_TEST_SOURCE/tests/integration/public-egress-host-config.sh" "$step_dir/host.json"
start_daemon() {
  p daemon "$step_dir/host.json" >> "$step_dir/daemon.out" 2>> "$step_dir/daemon.err" &
  daemon_pid=$!
  local deadline=$((SECONDS+30))
  while ((SECONDS < deadline)); do
    if test -S "$socket" && rpc system.health | jq -e '.result.control_state=="ready"' >/dev/null 2>&1; then return; fi
    if ! kill -0 "$daemon_pid" 2>/dev/null; then echo 'repair daemon exited' >&2; return 1; fi
    sleep 0.1
  done
  echo 'repair daemon health unavailable' >&2
  return 1
}
operation_id() { jq -er '.result.operation.id | select(length==36)' <<< "$1"; }
inspect_operation() { rpc operation.inspect "$(jq -nc --arg id "$1" '{v:1,id:$id}')"; }
wait_operation() {
  local id="$1" result status deadline=$((SECONDS+360))
  while ((SECONDS < deadline)); do
    result=$(inspect_operation "$id")
    status=$(jq -er '.result.operation.status' <<< "$result")
    if test "$status" = completed; then printf '%s\n' "$result"; return; fi
    if test "$status" = blocked || test "$status" = failed || test "$status" = superseded || test "$status" = unknown; then
      echo "unexpected operation status: $result" >&2; return 1
    fi
    sleep 0.4
  done
  echo "operation $id did not complete" >&2
  return 1
}
wait_ready() {
  local uuid="$1" deadline=$((SECONDS+180))
  while ((SECONDS < deadline)); do
    if rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
      jq -e '.result.session.session_condition=="ready"' >/dev/null; then return; fi
    sleep 0.4
  done
  echo "session $uuid did not become ready" >&2
  return 1
}
wait_pane_export() {
  local uuid="$1" expected="$2" actual deadline=$((SECONDS+30))
  while ((SECONDS < deadline)); do
    actual=$(guest "$uuid" cat /home/p/pane-export 2>/dev/null) || actual=
    if test "$actual" = "$expected"; then return; fi
    sleep 0.2
  done
  echo "guest $uuid did not activate expected environment value" >&2
  return 1
}

start_daemon
rpc system.capabilities | jq -e '.result.available |
  (index("session.repair.prepare") != null and index("session.repair.preview") != null and
   index("session.repair.confirm") != null)' >/dev/null
created=$(rpc project.create '{"v":1,"key":"repair-image-bootstrap","project":"missing-image-repair"}')
create_op=$(operation_id "$created")
sibling_uuid=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created")
op_ids+=("$create_op")
wait_operation "$create_op" >/dev/null
wait_ready "$sibling_uuid"
guest "$sibling_uuid" git config user.name P
guest "$sibling_uuid" git config user.email p@example.invalid
guest "$sibling_uuid" /usr/libexec/p/codex-adapter init
guest "$sibling_uuid" /run/current-system/sw/bin/bash -c \
  'printf sibling-dummy > /home/p/.codex/auth.json; chmod 0600 /home/p/.codex/auth.json'
sibling_key_digest=$(sha256sum "$state/session_keys/$sibling_uuid" | cut -d' ' -f1)

# Seed a real derivation on the retained P main branch, then create exactly
# one derived-image session. The bootstrap sibling remains on its base root.
cat > "$step_dir/flake.nix" <<'EOF'
{
  outputs = { self }: {
    devShells.x86_64-linux.default = builtins.derivation {
      name = "p-repair-shell-fixture";
      system = "x86_64-linux";
      builder = "/run/current-system/sw/bin/bash";
      args = [ "-c" "printf built > \"$out\"" ];
      outputs = [ "out" ];
      stdenv = ./stdenv;
      NATIVE_VALUE = "old";
      NATIVE_REV = self.rev;
      shellHook = ''
        export NATIVE_HOOK=ready
      '';
    };
  };
}
EOF
printf 'export NATIVE_VALUE=old\n' > "$step_dir/setup"
printf 'retained\n' > "$step_dir/README"
guest "$sibling_uuid" mkdir -p stdenv
for file in README flake.nix; do
  inc file push --uid 1000 --gid 1000 --mode 0644 "$step_dir/$file" "p-$sibling_uuid/workspace/$file"
done
inc file push --uid 1000 --gid 1000 --mode 0644 "$step_dir/setup" "p-$sibling_uuid/workspace/stdenv/setup"
guest "$sibling_uuid" git add README flake.nix stdenv/setup
guest "$sibling_uuid" git commit -qm repair-source-old
guest "$sibling_uuid" git push origin HEAD:main
main_oid=$(guest "$sibling_uuid" git rev-parse HEAD)
created=$(rpc session.create '{"v":1,"key":"repair-image-target","project":"missing-image-repair","branch":"target","choice":"new","source":"refs/heads/main"}')
target_op=$(operation_id "$created")
target_uuid=$(jq -er '.result.operation.session_uuid | select(length==36)' <<< "$created")
op_ids+=("$target_op")
wait_operation "$target_op" >/dev/null
wait_ready "$target_uuid"
wait_pane_export "$target_uuid" old
guest "$target_uuid" git config user.name P
guest "$target_uuid" git config user.email p@example.invalid
old_view=$(rpc session.inspect "$(jq -nc --arg uuid "$target_uuid" '{v:1,uuid:$uuid}')")
old_image=$(jq -er '.result.session.environment.image_fingerprint | select(length==64)' <<< "$old_view")
old_key=$(jq -er '.result.session.environment.environment_key | select(length==64)' <<< "$old_view")
jq -e --arg base "$base_image" --arg tip "$main_oid" --arg image "$old_image" '
  .result.session.environment | .selection=="devShells.x86_64-linux.default" and
  .commit_oid==$tip and .base_image_fingerprint==$base and .image_fingerprint==$image and
  .image_fingerprint!=$base' <<< "$old_view" >/dev/null
target_key_digest=$(sha256sum "$state/session_keys/$target_uuid" | cut -d' ' -f1)
guest "$target_uuid" /usr/libexec/p/codex-adapter init
guest "$target_uuid" /run/current-system/sw/bin/bash -c \
  'printf lost-dummy > /home/p/.codex/auth.json; chmod 0600 /home/p/.codex/auth.json; printf lost-local > /workspace/lost-local'

# Change a derivation input on the retained target branch. The old running
# root still uses its recorded old image, while preparation must select a new
# key from the currently committed P tip.
guest "$target_uuid" /run/current-system/sw/bin/sed -i 's/NATIVE_VALUE = "old"/NATIVE_VALUE = "new"/' flake.nix
guest "$target_uuid" /run/current-system/sw/bin/sed -i 's/NATIVE_VALUE=old/NATIVE_VALUE=new/' stdenv/setup
guest "$target_uuid" git add flake.nix stdenv/setup
guest "$target_uuid" git commit -qm repair-source-new
guest "$target_uuid" git push origin HEAD:target
new_tip=$(guest "$target_uuid" git rev-parse HEAD)
test "$new_tip" != "$main_oid"
test "$(guest "$target_uuid" git status --porcelain)" = '?? lost-local'

# Fault injection is limited to one runtime and its derived image. The base
# image and sibling are positively observed before and after deletion.
inc delete --force "p-$target_uuid"
inc image delete "$old_image"
inc image info "$base_image" >/dev/null
inc list "^p-$target_uuid$" --format json | jq -e 'length==0' >/dev/null
inc image list --format json | jq -e --arg image "$old_image" 'all(.[]; .fingerprint!=$image)' >/dev/null
inc list "^p-$sibling_uuid$" --format json | jq -e 'length==1 and .[0].status=="Running"' >/dev/null
if start_reply=$(rpc session.start "$(jq -nc --arg uuid "$target_uuid" '{v:1,uuid:$uuid}')" 2> "$step_dir/start-missing.err"); then
  jq -e '.result.session.session_condition=="missing"' <<< "$start_reply" >/dev/null
fi
inc list "^p-$target_uuid$" --format json | jq -e 'length==0' >/dev/null
blocked=$(rpc session.repair.preview "$(jq -nc --arg uuid "$target_uuid" '{v:1,uuid:$uuid}')")
jq -e --arg image "$old_image" '.result.preview | .eligible==false and
  .blocked_reason=="recorded_image_missing" and .image_fingerprint==$image and
  .image_status=="missing" and (has("confirmation_token")|not)' <<< "$blocked" >/dev/null

prepare_reply=$(rpc session.repair.prepare "$(jq -nc --arg uuid "$target_uuid" \
  '{v:1,key:"repair-image-prepare",uuid:$uuid}')")
prepare_op=$(operation_id "$prepare_reply")
op_ids+=("$prepare_op")
prepared=$(wait_operation "$prepare_op")
jq -e '.result.operation | .kind=="session.repair.prepare" and .phase=="completed"' <<< "$prepared" >/dev/null
inc list "^p-$target_uuid$" --format json | jq -e 'length==0' >/dev/null
test "$(rpc session.repair.prepare "$(jq -nc --arg uuid "$target_uuid" \
  '{v:1,key:"repair-image-prepare",uuid:$uuid}')" |
  jq -er '.result.operation.id | select(length==36)')" = "$prepare_op"

preview=$(rpc session.repair.preview "$(jq -nc --arg uuid "$target_uuid" --arg id "$prepare_op" \
  '{v:1,uuid:$uuid,preparation_operation_id:$id}')")
jq -e --arg uuid "$target_uuid" --arg tip "$new_tip" --arg source "$main_oid" --arg old "$old_image" \
  --arg base "$base_image" --arg key "$old_key" --arg prepare "$prepare_op" '
  .result.preview | .kind=="missing_runtime" and .session_uuid==$uuid and
  .project=="missing-image-repair" and .incus_project=="user-1000" and
  .instance_name==("p-"+$uuid) and .branch=="target" and .assigned_tip==$tip and
  .image_source_commit==$source and .image_source_differs==true and
  .image_fingerprint==$old and
  .image_status=="missing" and .runtime_local_loss=="unrecoverable" and
  .preparation_operation_id==$prepare and .eligible==true and
  (.confirmation_token|test("^[0-9a-f]{32}$")) and
  .environment.source_commit==$tip and .environment.selection=="devshell" and
  .environment.system=="x86_64-linux" and
  .environment.recorded_image_fingerprint==$old and
  .environment.base_image_fingerprint==$base and
  .environment.recorded_environment_key==$key and
  (.environment.environment_key|test("^[0-9a-f]{64}$")) and
  .environment.environment_key!=$key and .environment.identity_differs==true and
  .environment.source_differs==true and .environment.image_status=="needs_build" and
  (.environment|has("candidate_image_fingerprint")|not)' <<< "$preview" >/dev/null
token=$(jq -er '.result.preview.confirmation_token' <<< "$preview")
new_key=$(jq -er '.result.preview.environment.environment_key' <<< "$preview")
confirmed=$(rpc session.repair.confirm "$(jq -nc --arg uuid "$target_uuid" --arg token "$token" \
  '{v:1,key:"repair-image-confirm",uuid:$uuid,confirmation_token:$token}')")
repair_op=$(operation_id "$confirmed")
op_ids+=("$repair_op")
done_op=$(wait_operation "$repair_op")
jq -e '.result.operation | .kind=="session.repair" and .status=="completed" and .phase=="completed"' <<< "$done_op" >/dev/null
wait_ready "$target_uuid"
wait_pane_export "$target_uuid" new
inc list "^p-$target_uuid$" --format json | jq -e 'length==1 and .[0].status=="Running"' >/dev/null
new_view=$(rpc session.inspect "$(jq -nc --arg uuid "$target_uuid" '{v:1,uuid:$uuid}')")
jq -e --arg uuid "$target_uuid" '.result.session.uuid==$uuid' <<< "$new_view" >/dev/null
new_image=$(jq -er '.result.session.environment.image_fingerprint | select(length==64)' <<< "$new_view")
jq -e --arg base "$base_image" --arg image "$new_image" --arg key "$new_key" --arg tip "$new_tip" '
  .result.session.environment | .selection=="devShells.x86_64-linux.default" and .environment_key==$key and
  .image_fingerprint==$image and .base_image_fingerprint==$base and .commit_oid==$tip' <<< "$new_view" >/dev/null
test "$new_image" != "$old_image"
inc image info "$new_image" >/dev/null
test "$(guest "$target_uuid" git rev-parse HEAD)" = "$new_tip"
test "$(guest "$target_uuid" git branch --show-current)" = target
test "$(guest "$target_uuid" cat README)" = retained
guest "$target_uuid" /run/current-system/sw/bin/bash -c \
  'test ! -e /home/p/.codex/auth.json && test ! -e /workspace/lost-local'
test "$(sha256sum "$state/session_keys/$target_uuid" | cut -d' ' -f1)" = "$target_key_digest"
test -S "$endpoint_prefix/$target_uuid/git.sock"
test "$(rpc session.repair.confirm "$(jq -nc --arg uuid "$target_uuid" --arg token "$token" \
  '{v:1,key:"repair-image-confirm",uuid:$uuid,confirmation_token:$token}')" |
  jq -er '.result.operation.id | select(length==36)')" = "$repair_op"
rpc project.branches '{"v":1,"project":"missing-image-repair","limit":8}' |
  jq -e --arg main "$main_oid" --arg target "$new_tip" '.result.refs | length==2 and
    any(.[]; .ref=="refs/heads/main" and .oid==$main) and
    any(.[]; .ref=="refs/heads/target" and .oid==$target)' >/dev/null
test "$(guest "$sibling_uuid" cat /home/p/.codex/auth.json)" = sibling-dummy
test "$(sha256sum "$state/session_keys/$sibling_uuid" | cut -d' ' -f1)" = "$sibling_key_digest"
inc image info "$base_image" >/dev/null
stop_daemon
start_daemon
wait_ready "$target_uuid"
wait_pane_export "$target_uuid" new
restarted=$(rpc session.inspect "$(jq -nc --arg uuid "$target_uuid" '{v:1,uuid:$uuid}')")
jq -e --arg image "$new_image" --arg key "$new_key" '
  .result.session.environment | .image_fingerprint==$image and .environment_key==$key' <<< "$restarted" >/dev/null
test "$(guest "$target_uuid" git rev-parse HEAD)" = "$new_tip"
test "$(guest "$sibling_uuid" cat /home/p/.codex/auth.json)" = sibling-dummy
echo P_MISSING_IMAGE_REPAIR_PASS
