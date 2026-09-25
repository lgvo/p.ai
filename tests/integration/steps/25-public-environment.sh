# shellcheck shell=bash
# Public daemon composition: captured Git source, selected WASI Nix policy,
# private image cache, and pre-host activation in persistent session roots.
set -E
umask 077
if test "${P_COLLECTION_PROBE:-0}" = 1; then
  step_dir="$P_TEST_TMP/step-26"
  endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step26
else
  step_dir="$P_TEST_TMP/step-25"
  endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step25
fi
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
daemon_pid=
sessions=()

inc() { timeout 60 incus --force-local --project user-1000 "$@"; }
rpc() { timeout 45 p api "$socket" "$@"; }
exec_p() {
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
  rm -f -- /tmp/p-vm-collection-cutover-1000 /tmp/p-vm-collection-publish-barrier-1000 /tmp/p-vm-collection-overlap-1000
  for uuid in "${sessions[@]}"; do
    inc delete --force "p-$uuid" >/dev/null 2>&1 || true
    rm -rf -- "${endpoint_prefix:?}/${uuid:?}"
  done
  rmdir "$endpoint_prefix" 2>/dev/null || true
}
on_error() {
  printf 'P_PUBLIC_ENVIRONMENT_FAIL line=%s status=%s\n' "$2" "$1" >&2
  for id in "${op_ids[@]:-}"; do
    if test -n "$id"; then
      rpc operation.inspect "$(jq -nc --arg id "$id" '{v:1,id:$id}')" 2>/dev/null |
        jq -cr '.result.operation | {id,status,phase,diagnostic:(.diagnostic // "")[:512]}' >&2 || true
    fi
  done
  tail -c 3072 "$step_dir/daemon.err" >&2 || true
}
trap cleanup EXIT
trap 'on_error "$?" "$LINENO"' ERR

activate() {
  local package="$1" target="$2" grant="$3" digest id
  digest=$(p plugins conformance "$package" | jq -er '.sha256')
  id=$(jq -er '.id' "$package/plugin.json")
  jq -n --arg path "$package" --arg digest "$digest" --arg id "$id" --arg grant "$grant" \
    '{schema:"p.activation/v1",plugins:[{id:$id,path:$path,sha256:$digest,grants:[$grant],config:{}}]}' > "$target"
}
activate "$P_TEST_GIT_PLUGIN" "$step_dir/git.json" git.project
activate "$P_TEST_RUNTIME_PLUGIN" "$step_dir/runtime-one.json" runtime.incus
activate "$P_TEST_SOURCE/plugins/bundled/tmux-host" "$step_dir/host-one.json" session.asset.install
activate "$P_TEST_ENV_PLUGIN" "$step_dir/environment.json" environment.nix
jq -s '{schema:"p.activation/v1",plugins:([.[0].plugins[0],.[1].plugins[0]])}' \
  "$step_dir/runtime-one.json" "$step_dir/host-one.json" > "$step_dir/runtime.json"

base=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
test "${#base}" -eq 64
incus_binary=$(command -v incus)
if test "${P_COLLECTION_PROBE:-0}" = 1; then
  test -x "$P_TEST_INCUS_WRAPPER"
  incus_binary=$P_TEST_INCUS_WRAPPER
fi
# Keep the variable reference for the guest shell, not this fixture shell.
# shellcheck disable=SC2016
pane_command='printf "%s" "$NATIVE_HOOK" > /home/p/pane-export; exec /run/current-system/sw/bin/sleep infinity'
jq -n --arg state "$state" --arg git_activation "$step_dir/git.json" \
  --arg git_id "$(jq -er '.plugins[0].id' "$step_dir/git.json")" \
  --arg runtime_activation "$step_dir/runtime.json" \
  --arg runtime_id "$(jq -er '.plugins[0].id' "$step_dir/runtime.json")" \
  --arg host_id "$(jq -er '.plugins[1].id' "$step_dir/runtime.json")" \
  --arg environment_activation "$step_dir/environment.json" \
  --arg environment_id "$(jq -er '.plugins[0].id' "$step_dir/environment.json")" \
  --arg incus_binary "$incus_binary" --arg prefix "$endpoint_prefix" \
  --arg image "$base" --arg pane_command "$pane_command" \
  '{schema:"p.host/v1",state_dir:$state,
    git:{activation_path:$git_activation,source_plugin_id:$git_id,listen:"127.0.0.1:0"},
    runtime:{activation_path:$runtime_activation,runtime_plugin_id:$runtime_id,
      host_plugin_id:$host_id,incus_binary:$incus_binary,
      incus_user_socket:"/var/lib/incus/unix.socket.user",incus_project:"user-1000",
      endpoint_prefix:$prefix,disk_source_ceilings:["/var/lib/p-vm/endpoints","/var/lib/p-vm/grants"],
      base_image_fingerprint:$image,
      project_policy:{network:"none",filesystem_mounts:[],
        command:["/run/current-system/sw/bin/bash","-c",$pane_command]},
      environment:{activation_path:$environment_activation,plugin_id:$environment_id,
        system:"x86_64-linux",builder_storage_pool:"builders"}}}' > "$step_dir/host.json"
bash "$P_TEST_SOURCE/tests/integration/public-egress-host-config.sh" "$step_dir/host.json"

p daemon "$step_dir/host.json" > "$step_dir/daemon.out" 2> "$step_dir/daemon.err" &
daemon_pid=$!
deadline=$((SECONDS+30))
while ((SECONDS < deadline)); do
  if test -S "$socket" && rpc system.health | jq -e '.result.control_state=="ready"' >/dev/null 2>&1; then break; fi
  if ! kill -0 "$daemon_pid" 2>/dev/null; then echo 'public daemon exited' >&2; exit 1; fi
  sleep 0.1
done
test -S "$socket"
p_instance=$(rpc system.hello | jq -er '.result.instance_id | select(length==36)')

operation_id() { jq -er '.result.operation.id | select(type=="string" and length==36)' <<< "$1"; }
session_uuid() { jq -er '.result.operation.session_uuid | select(type=="string" and length==36)' <<< "$1"; }
inspect_operation() { rpc operation.inspect "$(jq -nc --arg id "$1" '{v:1,id:$id}')"; }
inspect_session() { rpc session.inspect "$(jq -nc --arg uuid "$1" '{v:1,uuid:$uuid}')"; }
wait_operation() {
  local id="$1" expected="$2" result status deadline=$((SECONDS+300))
  while ((SECONDS < deadline)); do
    result=$(inspect_operation "$id")
    status=$(jq -er '.result.operation.status' <<< "$result")
    if test "$status" = "$expected"; then printf '%s\n' "$result"; return; fi
    if { test "$status" = blocked || test "$status" = completed; } && test "$status" != "$expected"; then
      echo "unexpected operation status: $result" >&2; return 1
    fi
    sleep 0.4
  done
  echo "operation $id did not reach $expected" >&2
  return 1
}
wait_ready() {
  local uuid="$1" result deadline=$((SECONDS+160))
  while ((SECONDS < deadline)); do
    result=$(inspect_session "$uuid")
    if jq -e '.result.session.session_condition=="ready"' <<< "$result" >/dev/null; then
      printf '%s\n' "$result"; return
    fi
    sleep 0.4
  done
  echo "session $uuid did not become ready" >&2
  return 1
}
wait_guest_file() {
  local uuid="$1" path="$2" deadline=$((SECONDS+20))
  while ((SECONDS < deadline)); do
    if exec_p "$uuid" test -f "$path" >/dev/null 2>&1; then return; fi
    sleep 0.2
  done
  echo "guest file $path missing in $uuid" >&2
  return 1
}
create_branch() {
  local key="$1" branch="$2" source="${3:-refs/heads/main}" response
  response=$(rpc session.create "$(jq -nc --arg key "$key" --arg branch "$branch" --arg source "$source" \
    '{v:1,key:$key,project:"public-environment",branch:$branch,choice:"new",source:$source}')")
  new_op=$(operation_id "$response")
  new_uuid=$(session_uuid "$response")
  op_ids+=("$new_op")
  sessions+=("$new_uuid")
}
discard_owned() {
  local uuid="$1" suffix="$2" inspected loss_op preview token reply discard_op
  inspected=$(rpc workspace.loss.inspect "$(jq -nc --arg uuid "$uuid" --arg key "loss-$suffix" \
    '{v:1,key:$key,uuid:$uuid}')")
  loss_op=$(operation_id "$inspected")
  op_ids+=("$loss_op")
  wait_operation "$loss_op" completed >/dev/null
  preview=$(rpc session.removal.preview "$(jq -nc --arg uuid "$uuid" --arg id "$loss_op" \
    '{v:1,uuid:$uuid,kind:"discard",loss_operation_id:$id}')")
  token=$(jq -er '.result.preview.confirmation_token | select(length==32)' <<< "$preview")
  reply=$(rpc session.discard "$(jq -nc --arg uuid "$uuid" --arg key "discard-$suffix" --arg token "$token" \
    '{v:1,key:$key,uuid:$uuid,confirmation_token:$token}')")
  discard_op=$(operation_id "$reply")
  op_ids+=("$discard_op")
  wait_operation "$discard_op" completed >/dev/null
  test "$(inc list --format json | jq --arg uuid "$uuid" '[.[]|select(.name==("p-"+$uuid))]|length')" -eq 0
  test ! -e "$state/session_keys/$uuid"
}

op_ids=()
bootstrap=$(rpc project.create '{"v":1,"key":"public-environment-bootstrap","project":"public-environment"}')
boot_op=$(operation_id "$bootstrap")
boot_uuid=$(session_uuid "$bootstrap")
op_ids+=("$boot_op")
sessions+=("$boot_uuid")
wait_operation "$boot_op" completed >/dev/null
wait_ready "$boot_uuid" >/dev/null
exec_p "$boot_uuid" git config user.name P
exec_p "$boot_uuid" git config user.email p@example.invalid

cat > "$step_dir/flake.nix" <<'EOF'
{
  outputs = { self }: {
    devShells.x86_64-linux.default = builtins.derivation {
      name = "p-public-shell-fixture";
      system = "x86_64-linux";
      builder = "/run/current-system/sw/bin/bash";
      args = [ "-c" "printf built > \"$out\"" ];
      outputs = [ "out" ];
      stdenv = ./stdenv;
      NATIVE_VALUE = "ok";
      NATIVE_REV = self.rev;
      shellHook = ''
        export NATIVE_HOOK=ready
        /run/current-system/sw/bin/nix store info --json >/dev/null
        test "$(cat README)" = tracked
        printf local > /workspace/hook-written
        printf '%s' "$NATIVE_REV" > /home/p/hook-rev
        if test -f /home/p/hook-count; then
          _p_count=$(cat /home/p/hook-count)
        else
          _p_count=0
        fi
        printf '%s\n' "$((_p_count + 1))" > /home/p/hook-count
        if test "$_p_count" -eq 0 && test -d /workspace/.git &&
           test "$(git -C /workspace symbolic-ref HEAD)" = refs/heads/env-a; then
          : > /home/p/hook-in-progress
          _p_gate_deadline=$((SECONDS + 45))
          _p_gate_released=0
          while ((SECONDS < _p_gate_deadline)); do
            if test "$(cat /home/p/hook-gate-release 2>/dev/null)" = release; then
              _p_gate_released=1
              break
            fi
            /run/current-system/sw/bin/sleep 0.1
          done
          test "$_p_gate_released" -eq 1
          rm /home/p/hook-in-progress /home/p/hook-gate-release
        fi
      '';
    };
  };
}
EOF
printf 'export NATIVE_VALUE=ok\n' > "$step_dir/setup"
printf 'tracked\n' > "$step_dir/README"
exec_p "$boot_uuid" mkdir -p stdenv
for file in README flake.nix; do
  inc file push --uid 1000 --gid 1000 --mode 0644 "$step_dir/$file" "p-$boot_uuid/workspace/$file"
done
inc file push --uid 1000 --gid 1000 --mode 0644 "$step_dir/setup" "p-$boot_uuid/workspace/stdenv/setup"
exec_p "$boot_uuid" git add README flake.nix stdenv/setup
exec_p "$boot_uuid" git commit -qm valid-flake
exec_p "$boot_uuid" git push origin HEAD:main
valid_oid=$(exec_p "$boot_uuid" git rev-parse HEAD)

create_branch env-a env-a
a_op=$new_op a_uuid=$new_uuid
hook_seen=0
deadline=$((SECONDS+160))
while ((SECONDS < deadline)); do
  if timeout 5 incus --force-local --project user-1000 file pull \
    "p-$a_uuid/home/p/hook-in-progress" "$step_dir/hook-progress" \
    >"$step_dir/hook-pull.out" 2>"$step_dir/hook-pull.err"; then
    hook_seen=1
    break
  fi
  sleep 0.2
done
if test "$hook_seen" -ne 1; then
  printf 'initial env-a hook gate not observed; last bounded file pull error: ' >&2
  tail -c 512 "$step_dir/hook-pull.err" >&2 || true
  printf '\n' >&2
  exit 1
fi
exec_p "$a_uuid" test ! -S /run/p-interactive/tmux.sock
printf 'release\n' > "$step_dir/hook-gate-release"
inc file push --uid 1000 --gid 1000 --mode 0644 "$step_dir/hook-gate-release" \
  "p-$a_uuid/home/p/hook-gate-release"
wait_operation "$a_op" completed >/dev/null
a_view=$(wait_ready "$a_uuid")
image_one=$(jq -er '.result.session.environment.image_fingerprint | select(length==64)' <<< "$a_view")
key_one=$(jq -er '.result.session.environment.environment_key | select(length==64)' <<< "$a_view")
jq -e --arg base "$base" --arg oid "$valid_oid" --arg image "$image_one" \
  '.result.session.environment | .selection=="devShells.x86_64-linux.default" and
    .cache=="miss" and .commit_oid==$oid and .base_image_fingerprint==$base and
    .image_fingerprint==$image and .image_fingerprint!=$base' <<< "$a_view" >/dev/null
test "$(exec_p "$a_uuid" git rev-parse HEAD)" = "$valid_oid"
test "$(exec_p "$a_uuid" cat /home/p/hook-rev)" = "$valid_oid"
test "$(exec_p "$a_uuid" cat /home/p/hook-count)" = 1
test "$(exec_p "$a_uuid" cat /workspace/hook-written)" = local
wait_guest_file "$a_uuid" /home/p/pane-export
test "$(exec_p "$a_uuid" cat /home/p/pane-export)" = ready

create_branch env-b env-b
b_op=$new_op b_uuid=$new_uuid
wait_operation "$b_op" completed >/dev/null
b_view=$(wait_ready "$b_uuid")
jq -e --arg image "$image_one" --arg key "$key_one" \
  '.result.session.environment | .cache=="hit" and .image_fingerprint==$image and .environment_key==$key' \
  <<< "$b_view" >/dev/null
test "$(exec_p "$b_uuid" cat /home/p/hook-count)" = 1
test "$(inc image list --format json | jq --arg image "$image_one" '[.[]|select(.fingerprint==$image)]|length')" -eq 1
private_path=$(exec_p "$a_uuid" /run/current-system/sw/bin/bash -c \
  'printf unique-private-a > /workspace/private-a; nix-store --add /workspace/private-a')
test "${private_path#/nix/store/}" != "$private_path"
if exec_p "$b_uuid" nix path-info "$private_path" > "$step_dir/unexpected.out" 2>/dev/null; then
  echo 'private Nix store object crossed session roots' >&2; exit 1
fi
rpc session.stop "$(jq -nc --arg uuid "$a_uuid" '{v:1,uuid:$uuid}')" |
  jq -e '.result.session.session_condition=="stopped"' >/dev/null
rpc session.start "$(jq -nc --arg uuid "$a_uuid" '{v:1,uuid:$uuid}')" >/dev/null
wait_ready "$a_uuid" >/dev/null
test "$(exec_p "$a_uuid" cat /home/p/hook-count)" = 2
exec_p "$a_uuid" nix path-info "$private_path" >/dev/null
test "$(exec_p "$a_uuid" cat /workspace/private-a)" = unique-private-a

# Removing only the cached source image must not damage either running private
# root. The next same-key creation is a verified miss and publishes a new one.
# Main remains a retained P ref after Discard; the bootstrap runtime has
# finished its source edits, and releasing its durable reservation leaves a
# builder slot under the four-container ceiling.
discard_owned "$boot_uuid" bootstrap-before-c
inc image delete "$image_one"
create_branch env-c env-c
c_op=$new_op c_uuid=$new_uuid
wait_operation "$c_op" completed >/dev/null
c_view=$(wait_ready "$c_uuid")
image_two=$(jq -er '.result.session.environment.image_fingerprint | select(length==64)' <<< "$c_view")
test "$image_two" != "$image_one"
jq -e --arg key "$key_one" '.result.session.environment | .cache=="miss" and .environment_key==$key' \
  <<< "$c_view" >/dev/null
test "$(exec_p "$a_uuid" cat /home/p/hook-count)" = 2
exec_p "$a_uuid" nix path-info "$private_path" >/dev/null
test "$(exec_p "$b_uuid" cat /home/p/pane-export)" = ready
if test "${P_COLLECTION_PROBE:-0}" = 1; then
  cache_page=$(rpc environment.cache.list '{"v":1,"project":"public-environment","limit":8}')
  jq -e --arg image "$image_two" --arg key "$key_one" \
    '.result.images | length==1 and .[0].fingerprint==$image and .[0].environment_key==$key and .[0].image_status=="present"' \
    <<< "$cache_page" >/dev/null
  stale_preview=$(rpc environment.cache.preview "$(jq -nc --arg key "$key_one" \
    '{v:1,project:"public-environment",environment_key:$key,limit:8}')")
  stale_token=$(jq -er '.result.preview.confirmation_token | select(length==32)' <<< "$stale_preview")
  # A keeps the private-root proof live; B has finished its cache-hit proof.
  discard_owned "$b_uuid" b-before-d
  test "$(exec_p "$a_uuid" git ls-remote origin refs/heads/env-a | cut -f1)" = "$valid_oid"
  create_branch env-d env-d refs/heads/env-a
  d_op=$new_op d_uuid=$new_uuid
  wait_operation "$d_op" completed >/dev/null
  d_view=$(wait_ready "$d_uuid")
  jq -e --arg image "$image_two" '.result.session.environment | .cache=="hit" and .image_fingerprint==$image' <<< "$d_view" >/dev/null
  if stale_collect=$(rpc environment.cache.collect "$(jq -nc --arg token "$stale_token" \
    '{v:1,key:"collect-stale-preview",confirmation_token:$token}')" 2> "$step_dir/stale-collect.err"); then
    echo 'accepted stale cache collection preview after established use' >&2; exit 1
  fi
  jq -e '.error.kind=="busy" and .error.code == -32003' <<< "$stale_collect" >/dev/null
  rpc operation.list '{"v":1,"limit":20}' |
    jq -e 'all(.result.operations[]; .idempotency_key!="collect-stale-preview")' >/dev/null
  fresh_preview=$(rpc environment.cache.preview "$(jq -nc --arg key "$key_one" \
    '{v:1,project:"public-environment",environment_key:$key,limit:8}')")
  jq -e --arg image "$image_two" '.result.preview.item | .fingerprint==$image and .image_status=="present" and .related_count>=2' \
    <<< "$fresh_preview" >/dev/null
  fresh_token=$(jq -er '.result.preview.confirmation_token | select(length==32)' <<< "$fresh_preview")
  printf '%s %s\n' "$image_two" "$daemon_pid" > /tmp/p-vm-collection-cutover-1000
  chmod 0600 /tmp/p-vm-collection-cutover-1000
  # A crash can sever the confirming RPC after its durable operation commits.
  # Recover the exact idempotency key after restart if that reply is lost.
  collect_reply=$(rpc environment.cache.collect "$(jq -nc --arg token "$fresh_token" \
    '{v:1,key:"collect-present-image",confirmation_token:$token}')" 2>/dev/null || true)
  collect_op=$(operation_id "$collect_reply" 2>/dev/null || true)
  if test -n "$collect_op"; then op_ids+=("$collect_op"); fi
  deadline=$((SECONDS+120))
  while ((SECONDS < deadline)); do
    daemon_state=$(ps -o stat= -p "$daemon_pid" 2>/dev/null || true)
    if test -z "$daemon_state" || [[ "$daemon_state" == Z* ]]; then break; fi
    sleep 0.2
  done
  daemon_state=$(ps -o stat= -p "$daemon_pid" 2>/dev/null || true)
  if test -n "$daemon_state" && [[ "$daemon_state" != Z* ]]; then echo 'daemon survived collection cutover' >&2; exit 1; fi
  wait "$daemon_pid" 2>/dev/null || true
  daemon_pid=
  test ! -e /tmp/p-vm-collection-cutover-1000
  test "$(inc image list --format json | jq --arg image "$image_two" '[.[]|select(.fingerprint==$image)]|length')" -eq 0
  p daemon "$step_dir/host.json" >> "$step_dir/daemon.out" 2>> "$step_dir/daemon.err" &
  daemon_pid=$!
  deadline=$((SECONDS+30))
  while ((SECONDS < deadline)); do
    if test -S "$socket" && rpc system.health | jq -e '.result.control_state=="ready"' >/dev/null 2>&1; then break; fi
    if ! kill -0 "$daemon_pid" 2>/dev/null; then echo 'collection recovery daemon exited' >&2; exit 1; fi
    sleep 0.1
  done
  if test -z "$collect_op"; then
    collect_op=$(rpc operation.list '{"v":1,"limit":20}' | jq -er \
      '.result.operations[] | select(.idempotency_key=="collect-present-image") | .id')
    op_ids+=("$collect_op")
  fi
  wait_operation "$collect_op" completed >/dev/null
  jq -e '.result.images | length==0' <<< "$(rpc environment.cache.list '{"v":1,"project":"public-environment","limit":8}')" >/dev/null
  exec_p "$a_uuid" nix path-info "$private_path" >/dev/null
  test "$(exec_p "$c_uuid" cat /home/p/pane-export)" = ready
  test "$(exec_p "$d_uuid" cat /home/p/pane-export)" = ready
  # A's private-root check and C's same-image collection check have passed.
  # Release both reservations so D and two same-key builders can overlap.
  discard_owned "$a_uuid" a-before-ef
  discard_owned "$c_uuid" c-before-ef
  printf 'pending\n' > /tmp/p-vm-collection-publish-barrier-1000
  chmod 0600 /tmp/p-vm-collection-publish-barrier-1000
  create_branch env-e env-e refs/heads/env-a
  e_op=$new_op e_uuid=$new_uuid
  create_branch env-f env-f refs/heads/env-a
  f_op=$new_op f_uuid=$new_uuid
  printf '%s %s\n' "$e_op" "$f_op" > /tmp/p-vm-collection-publish-barrier-1000
  wait_operation "$e_op" completed >/dev/null
  wait_operation "$f_op" completed >/dev/null
  test -f /tmp/p-vm-collection-overlap-1000
  test ! -e /tmp/p-vm-collection-publish-barrier-1000
  e_view=$(wait_ready "$e_uuid")
  f_view=$(wait_ready "$f_uuid")
  image_three=$(jq -er '.result.session.environment.image_fingerprint | select(length==64)' <<< "$e_view")
  test "$image_three" != "$image_two"
  jq -e --arg image "$image_three" --arg key "$key_one" \
    '.result.session.environment | .image_fingerprint==$image and .environment_key==$key' <<< "$f_view" >/dev/null
  test "$(jq -r '.result.session.environment.cache' <<< "$e_view") $(jq -r '.result.session.environment.cache' <<< "$f_view")" = 'miss hit' ||
    test "$(jq -r '.result.session.environment.cache' <<< "$e_view") $(jq -r '.result.session.environment.cache' <<< "$f_view")" = 'hit miss'
  test "$(inc image list --format json | jq --arg key "$key_one" --arg instance "$p_instance" \
    '[.[]|select(.properties["p.environment_key"]==$key and .properties["p.instance"]==$instance and .properties["p.project_path"]=="public-environment")]|length')" -eq 1
  inc image delete "$image_three"
  missing_preview=$(rpc environment.cache.preview "$(jq -nc --arg key "$key_one" \
    '{v:1,project:"public-environment",environment_key:$key,limit:8}')")
  jq -e --arg image "$image_three" '.result.preview.item | .fingerprint==$image and .image_status=="missing" and .related_count>=2' \
    <<< "$missing_preview" >/dev/null
  missing_token=$(jq -er '.result.preview.confirmation_token | select(length==32)' <<< "$missing_preview")
  missing_reply=$(rpc environment.cache.collect "$(jq -nc --arg token "$missing_token" \
    '{v:1,key:"collect-missing-image",confirmation_token:$token}')")
  missing_op=$(operation_id "$missing_reply")
  op_ids+=("$missing_op")
  wait_operation "$missing_op" completed >/dev/null
  jq -e '.result.images | length==0' <<< "$(rpc environment.cache.list '{"v":1,"project":"public-environment","limit":8}')" >/dev/null
  test "$(exec_p "$d_uuid" cat /home/p/pane-export)" = ready
  test "$(exec_p "$e_uuid" cat /home/p/pane-export)" = ready
  test "$(exec_p "$f_uuid" cat /home/p/pane-export)" = ready
  echo P_ENVIRONMENT_CACHE_COLLECTION_PASS
  exit 0
fi

# A present invalid default is an error, and exact retry remains pinned to
# that captured commit even after main advances to a valid absent-default.
# A and B have finished their private-root/cache checks. Release both before
# recreating the retained main assignment for the source changes below.
discard_owned "$c_uuid" c-before-defaults
discard_owned "$a_uuid" a-before-defaults
discard_owned "$b_uuid" b-before-defaults
recreated=$(rpc session.create '{"v":1,"key":"public-environment-recreate-main","project":"public-environment","branch":"main","choice":"existing"}')
boot_op=$(operation_id "$recreated")
boot_uuid=$(session_uuid "$recreated")
op_ids+=("$boot_op")
sessions+=("$boot_uuid")
wait_operation "$boot_op" completed >/dev/null
wait_ready "$boot_uuid" >/dev/null
exec_p "$boot_uuid" git config user.name P
exec_p "$boot_uuid" git config user.email p@example.invalid
printf '{ outputs = _: { devShells.x86_64-linux.default = 42; }; }\n' > "$step_dir/invalid.nix"
inc file push --uid 1000 --gid 1000 --mode 0644 "$step_dir/invalid.nix" "p-$boot_uuid/workspace/flake.nix"
exec_p "$boot_uuid" git add flake.nix
exec_p "$boot_uuid" git commit -qm invalid-default
exec_p "$boot_uuid" git push origin HEAD:main
invalid_oid=$(exec_p "$boot_uuid" git rev-parse HEAD)
create_branch env-invalid env-invalid
invalid_op=$new_op invalid_uuid=$new_uuid
invalid_result=$(wait_operation "$invalid_op" blocked)
jq -e --arg oid "$invalid_oid" --arg base "$base" \
  '.result.operation | .phase=="branch-assigned" and
    (.diagnostic | contains("present default devShell invalid")) and
    (.evidence | .captured_oid==$oid and .image_fingerprint==$base and .environment_state==null)' \
  <<< "$invalid_result" >/dev/null
test "$(inc list --format json | jq --arg uuid "$invalid_uuid" '[.[]|select(.name==("p-"+$uuid))]|length')" -eq 0

printf '{ outputs = _: { packages.x86_64-linux.default = 42; }; }\n' > "$step_dir/absent-default.nix"
inc file push --uid 1000 --gid 1000 --mode 0644 "$step_dir/absent-default.nix" "p-$boot_uuid/workspace/flake.nix"
exec_p "$boot_uuid" git add flake.nix
exec_p "$boot_uuid" git commit -qm absent-default
exec_p "$boot_uuid" git push origin HEAD:main
absent_oid=$(exec_p "$boot_uuid" git rev-parse HEAD)
rpc operation.retry "$(jq -nc --arg id "$invalid_op" '{v:1,id:$id}')" >/dev/null
retried=$(wait_operation "$invalid_op" blocked)
jq -e --arg oid "$invalid_oid" '.result.operation |
  (.diagnostic | contains("present default devShell invalid")) and .evidence.captured_oid==$oid' \
  <<< "$retried" >/dev/null
create_branch env-absent env-absent
absent_op=$new_op absent_uuid=$new_uuid
wait_operation "$absent_op" completed >/dev/null
absent_view=$(wait_ready "$absent_uuid")
jq -e --arg oid "$absent_oid" --arg base "$base" \
  '.result.session.environment | .commit_oid==$oid and .selection=="base" and
    .cache=="none" and .image_fingerprint==$base' <<< "$absent_view" >/dev/null
test "$(exec_p "$absent_uuid" git rev-parse HEAD)" = "$absent_oid"
exec_p "$absent_uuid" test -f /workspace/flake.nix

echo P_PUBLIC_ENVIRONMENT_PASS
