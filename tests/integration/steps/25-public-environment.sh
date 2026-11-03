# shellcheck shell=bash
# Public daemon composition: captured Git source, one image per source commit
# built natively and shared by the sessions started from that commit, its
# release with the commit's last session, and pre-host activation in
# persistent session roots.
set -E
umask 077
if test "${P_CACHE_OVERLAP_PROBE:-0}" = 1; then
  step_dir="$P_TEST_TMP/step-26"
  endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step26
else
  step_dir="$P_TEST_TMP/step-25"
  endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step25
fi
# While this file exists, the test-only Incus wrapper of step 26 appends the
# builder named by every image publish.
publish_count=/tmp/p-vm-publish-count-1000
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
daemon_pid=
watcher_pid=
p_instance=
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
  if test -n "$watcher_pid"; then
    kill "$watcher_pid" 2>/dev/null || true
    wait "$watcher_pid" 2>/dev/null || true
  fi
  rm -f -- "$publish_count"
  for uuid in "${sessions[@]}"; do
    inc delete --force "p-$uuid" >/dev/null 2>&1 || true
    rm -rf -- "${endpoint_prefix:?}/${uuid:?}"
  done
  # The stopped daemon no longer releases the commit images it built.
  if test -n "$p_instance"; then
    inc image list --format json 2>/dev/null |
      jq -r --arg instance "$p_instance" '.[] | select(.properties["p.instance"]==$instance) | .fingerprint' |
      while IFS= read -r image; do inc image delete "$image" >/dev/null 2>&1 || true; done || true
  fi
  rmdir "$endpoint_prefix" 2>/dev/null || true
}
on_error() {
  printf 'P_PUBLIC_ENVIRONMENT_FAIL line=%s status=%s\n' "$2" "$1" >&2
  for id in "${op_ids[@]:-}"; do
    if test -n "$id"; then
      rpc operation.inspect "$(jq -nc --arg id "$id" '{id:$id}')" 2>/dev/null |
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
jq -s '{schema:"p.activation/v1",plugins:([.[0].plugins[0],.[1].plugins[0]])}' \
  "$step_dir/runtime-one.json" "$step_dir/host-one.json" > "$step_dir/runtime.json"

base=$(cat "$P_TEST_RUNTIME_IMAGE_FILE")
test "${#base}" -eq 64
incus_binary=$(command -v incus)
if test "${P_CACHE_OVERLAP_PROBE:-0}" = 1; then
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
      environment:{system:"x86_64-linux",builder_storage_pool:"builders"}}}' > "$step_dir/host.json"
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
inspect_session() { rpc session.inspect "$(jq -nc --arg uuid "$1" '{uuid:$uuid}')"; }
wait_operation() {
  local id="$1" expected="$2" result status deadline=$((SECONDS+300))
  while ((SECONDS < deadline)); do
    result=$(rpc operation.wait "$(jq -nc --arg id "$id" '{id:$id,timeout:20}')")
    status=$(jq -er '.result.operation.status' <<< "$result")
    if test "$status" = "$expected"; then printf '%s\n' "$result"; return; fi
    if { test "$status" = blocked || test "$status" = completed || test "$status" = failed; } &&
      test "$status" != "$expected"; then
      echo "unexpected operation status: $result" >&2; return 1
    fi
    if test "$status" != running; then sleep 0.4; fi
  done
  echo "operation $id did not reach $expected" >&2
  return 1
}
# change_session METHOD UUID: METHOD, session.start or session.stop, records
# an operation of its kind for session UUID, which completes.
change_session() {
  local id
  id=$(rpc "$1" "$(jq -nc --arg uuid "$2" '{uuid:$uuid}')" | jq -er --arg kind "$1" \
    '.result | select(.changed==true and .operation.kind==$kind) | .operation.id') || return
  wait_operation "$id" completed >/dev/null
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
    '{key:$key,project:"public-environment",branch:$branch,choice:"new",source:$source}')")
  new_op=$(operation_id "$response")
  new_uuid=$(session_uuid "$response")
  op_ids+=("$new_op")
  sessions+=("$new_uuid")
}
# discard_owned UUID SUFFIX: discards session UUID, a dry run then a run with
# its change tag and key discard-SUFFIX, and checks that its runtime and key
# are gone.
discard_owned() {
  local uuid="$1" suffix="$2" plan tag reply discard_op
  plan=$(rpc session.discard "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')")
  tag=$(jq -er --arg target "session/$uuid" '.result | select(.plan.allowed==true and .plan.target==$target) |
    .change_tag | select(test("^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{64}$"))' <<< "$plan")
  reply=$(rpc session.discard "$(jq -nc --arg uuid "$uuid" --arg key "discard-$suffix" --arg tag "$tag" \
    '{uuid:$uuid,key:$key,if_match:$tag}')")
  discard_op=$(operation_id "$reply")
  op_ids+=("$discard_op")
  wait_operation "$discard_op" completed >/dev/null
  test "$(inc list --format json | jq --arg uuid "$uuid" '[.[]|select(.name==("p-"+$uuid))]|length')" -eq 0
  test ! -e "$state/session_keys/$uuid"
}
# The build reference P derives for a creation's build in one cycle
# (control.BuildRef): SHA-256 of "<operation>/<cycle>", its first 16 bytes as
# a version 4 UUID. A builder is named p-builder-<build reference>.
build_ref() {
  local sum
  sum=$(printf '%s/%s' "$1" "$2" | sha256sum | cut -c1-32)
  printf '%s-%s-4%s-%x%s-%s\n' "${sum:0:8}" "${sum:8:4}" "${sum:13:3}" \
    "$(( (16#${sum:16:1} & 3) | 8 ))" "${sum:17:3}" "${sum:20:12}"
}
# This P instance's Incus images of one commit of this project.
commit_images() {
  inc image list --format json | jq -c --arg oid "$1" --arg instance "$p_instance" \
    '[.[] | select(.properties["p.source_commit"]==$oid and .properties["p.instance"]==$instance and
      .properties["p.project_path"]=="public-environment")]'
}
no_builders() {
  inc list --format json | jq -e 'all(.[]; .name | startswith("p-builder-") | not)' >/dev/null
}
# A creation that built its commit's image in its first build cycle: the
# session's image view, the creation's evidence and the Incus image's labels
# agree, and the image is the commit's only one. Sets built_image.
assert_built() {
  local result="$1" view="$2" oid="$3" selection="$4" label="$5" op image
  op=$(jq -er '.result.operation.id' <<< "$result")
  image=$(jq -er '.result.session.image.image_fingerprint | select(length==64)' <<< "$view")
  jq -e --arg oid "$oid" --arg base "$base" --arg selection "$selection" \
    '.result.session.image | .state=="built" and .commit_oid==$oid and .system=="x86_64-linux" and
      .selection==$selection and .base_image_fingerprint==$base and .image_fingerprint!=$base' \
    <<< "$view" >/dev/null
  jq -e --arg image "$image" --arg label "$label" '.result.operation.evidence |
    .image_fingerprint==$image and .image.ref==$image and .image.built==true and .image.selection==$label and
    ((.image.material_sha256 // "") | test(if $label=="devshell" then "^[0-9a-f]{64}$" else "^$" end)) and
    .environment_builder=={cycle:1,state:"absent"}' <<< "$result" >/dev/null
  commit_images "$oid" | jq -e --arg image "$image" --arg base "$base" --arg label "$label" \
    --arg build "$(build_ref "$op" 1)" --argjson ev "$(jq -c '.result.operation.evidence' <<< "$result")" \
    'length==1 and .[0].fingerprint==$image and (.[0].properties |
      .["p.base_image"]==$base and .["p.system"]=="x86_64-linux" and .["p.selection"]==$label and
      .["p.build"]==$build and .["p.material"]==$ev.image.material_sha256)' >/dev/null
  built_image=$image
}
# A creation that reused the image another creation built for its commit,
# without any build cycle of its own.
assert_reused() {
  local result="$1" view="$2" oid="$3" image="$4"
  jq -e --arg oid "$oid" --arg image "$image" \
    '.result.session.image | .state=="reused" and .commit_oid==$oid and .image_fingerprint==$image' \
    <<< "$view" >/dev/null
  jq -e --arg image "$image" '.result.operation.evidence | .image_fingerprint==$image and
    .image.ref==$image and .image.built==false and .environment_builder=={cycle:0,state:"not-attempted"}' \
    <<< "$result" >/dev/null
}
# Records every builder instance seen until the stop file appears.
watch_builders() {
  local seen="$1" stop="$2" deadline=$((SECONDS+600))
  while test ! -e "$stop" && ((SECONDS < deadline)); do
    inc list --format json 2>/dev/null |
      jq -r '.[] | select(.name | startswith("p-builder-")) | .name' >> "$seen" 2>/dev/null || true
    sleep 0.3
  done
}

op_ids=()
bootstrap=$(rpc project.create '{"key":"public-environment-bootstrap","project":"public-environment"}')
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
# The hook runs once the creation has built C1's image.
deadline=$((SECONDS+300))
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
a_result=$(wait_operation "$a_op" completed)
a_view=$(wait_ready "$a_uuid")
# The first session from main@C1 builds C1's image: the commit's devShell and
# a clone of C1, which the session's workspace puts on its own branch.
assert_built "$a_result" "$a_view" "$valid_oid" devShells.x86_64-linux.default devshell
image_one=$built_image
test "$(exec_p "$a_uuid" git rev-parse HEAD)" = "$valid_oid"
test "$(exec_p "$a_uuid" git symbolic-ref HEAD)" = refs/heads/env-a
test "$(exec_p "$a_uuid" git for-each-ref --format='%(refname)')" = $'refs/heads/env-a\nrefs/remotes/origin/env-a'
test "$(exec_p "$a_uuid" git remote)" = origin
test "$(exec_p "$a_uuid" git remote get-url origin)" = ssh://git@p/public-environment
test "$(exec_p "$a_uuid" git rev-parse --symbolic-full-name '@{upstream}')" = refs/remotes/origin/env-a
test "$(exec_p "$a_uuid" git ls-remote origin refs/heads/env-a | cut -f1)" = "$valid_oid"
test "$(exec_p "$a_uuid" cat /home/p/hook-rev)" = "$valid_oid"
test "$(exec_p "$a_uuid" cat /home/p/hook-count)" = 1
test "$(exec_p "$a_uuid" cat /workspace/hook-written)" = local
wait_guest_file "$a_uuid" /home/p/pane-export
test "$(exec_p "$a_uuid" cat /home/p/pane-export)" = ready

# A second session from the same commit reuses its image: no builder runs.
: > "$step_dir/builders-b"
watch_builders "$step_dir/builders-b" "$step_dir/builders-b.stop" &
watcher_pid=$!
create_branch env-b env-b
b_op=$new_op b_uuid=$new_uuid
b_result=$(wait_operation "$b_op" completed)
b_view=$(wait_ready "$b_uuid")
: > "$step_dir/builders-b.stop"
wait "$watcher_pid"
watcher_pid=
if test -s "$step_dir/builders-b"; then
  printf 'a builder ran for a reused image: %s\n' "$(sort -u "$step_dir/builders-b" | head -c 512)" >&2
  exit 1
fi
assert_reused "$b_result" "$b_view" "$valid_oid" "$image_one"
jq -e --argjson a "$(jq -c '.result.operation.evidence.image' <<< "$a_result")" \
  '.result.operation.evidence.image | .selection==$a.selection and .material_sha256==$a.material_sha256' \
  <<< "$b_result" >/dev/null
test "$(commit_images "$valid_oid" | jq -r '[.[].fingerprint] | join(" ")')" = "$image_one"
test "$(exec_p "$b_uuid" git symbolic-ref HEAD)" = refs/heads/env-b
test "$(exec_p "$b_uuid" git rev-parse '@{upstream}')" = "$valid_oid"
test "$(exec_p "$b_uuid" cat /home/p/hook-count)" = 1
private_path=$(exec_p "$a_uuid" /run/current-system/sw/bin/bash -c \
  'printf unique-private-a > /workspace/private-a; nix-store --add /workspace/private-a')
test "${private_path#/nix/store/}" != "$private_path"
if exec_p "$b_uuid" nix path-info "$private_path" > "$step_dir/unexpected.out" 2>/dev/null; then
  echo 'private Nix store object crossed session roots' >&2; exit 1
fi
change_session session.stop "$a_uuid"
inspect_session "$a_uuid" | jq -e '.result.session.session_condition=="stopped"' >/dev/null
change_session session.start "$a_uuid"
wait_ready "$a_uuid" >/dev/null
test "$(exec_p "$a_uuid" cat /home/p/hook-count)" = 2
exec_p "$a_uuid" nix path-info "$private_path" >/dev/null
test "$(exec_p "$a_uuid" cat /workspace/private-a)" = unique-private-a

# Removing the commit's image outside P must not damage either running private
# root. The next creation from that commit finds the image gone and builds it
# again. Main remains a retained P ref after Discard; the bootstrap runtime has
# finished its source edits, and releasing its durable reservation leaves a
# builder slot under the four-container ceiling.
discard_owned "$boot_uuid" bootstrap-before-c
inc image delete "$image_one"
create_branch env-c env-c
c_op=$new_op c_uuid=$new_uuid
c_result=$(wait_operation "$c_op" completed)
c_view=$(wait_ready "$c_uuid")
assert_built "$c_result" "$c_view" "$valid_oid" devShells.x86_64-linux.default devshell
image_two=$built_image
test "$image_two" != "$image_one"
test "$(exec_p "$c_uuid" git rev-parse HEAD)" = "$valid_oid"
test "$(exec_p "$a_uuid" cat /home/p/hook-count)" = 2
exec_p "$a_uuid" nix path-info "$private_path" >/dev/null
test "$(exec_p "$b_uuid" cat /home/p/pane-export)" = ready

# A commit that changes only code, on C's branch.
printf 'code only\n' > "$step_dir/code.txt"
inc file push --uid 1000 --gid 1000 --mode 0644 "$step_dir/code.txt" "p-$c_uuid/workspace/code.txt"
exec_p "$c_uuid" git add code.txt
exec_p "$c_uuid" git -c user.name=P -c user.email=p@example.invalid commit -qm code-only
exec_p "$c_uuid" git push origin HEAD:refs/heads/env-c
code_oid=$(exec_p "$c_uuid" git rev-parse HEAD)
test "$(exec_p "$c_uuid" git diff --name-only "$valid_oid" "$code_oid")" = code.txt

if test "${P_CACHE_OVERLAP_PROBE:-0}" = 1; then
  # A and B have finished their private-root and reuse checks. Two creations
  # from the code-only commit at the same time build its image once: one
  # builds and the other reuses, and the wrapper sees one publish.
  discard_owned "$a_uuid" a-before-ef
  discard_owned "$b_uuid" b-before-ef
  : > "$publish_count"
  create_branch env-e env-e refs/heads/env-c
  e_op=$new_op e_uuid=$new_uuid
  create_branch env-f env-f refs/heads/env-c
  f_op=$new_op f_uuid=$new_uuid
  e_result=$(wait_operation "$e_op" completed)
  f_result=$(wait_operation "$f_op" completed)
  e_view=$(wait_ready "$e_uuid")
  f_view=$(wait_ready "$f_uuid")
  case "$(jq -r '.result.session.image.state' <<< "$e_view") $(jq -r '.result.session.image.state' <<< "$f_view")" in
    'built reused')
      built_op=$e_op built_result=$e_result built_view=$e_view reused_result=$f_result reused_view=$f_view ;;
    'reused built')
      built_op=$f_op built_result=$f_result built_view=$f_view reused_result=$e_result reused_view=$e_view ;;
    *) echo 'concurrent creations from one commit did not build exactly once' >&2; exit 1 ;;
  esac
  assert_built "$built_result" "$built_view" "$code_oid" devShells.x86_64-linux.default devshell
  image_three=$built_image
  assert_reused "$reused_result" "$reused_view" "$code_oid" "$image_three"
  test "$image_three" != "$image_two"
  test "$(cat "$publish_count")" = "p-builder-$(build_ref "$built_op" 1)"
  rm -f -- "$publish_count"
  no_builders
  test "$(exec_p "$e_uuid" git rev-parse HEAD)" = "$code_oid"
  test "$(exec_p "$f_uuid" git rev-parse HEAD)" = "$code_oid"
  test "$(exec_p "$c_uuid" cat /home/p/pane-export)" = ready
  test "$(exec_p "$e_uuid" cat /home/p/pane-export)" = ready
  test "$(exec_p "$f_uuid" cat /home/p/pane-export)" = ready
  echo P_ENVIRONMENT_CACHE_OVERLAP_PASS
  exit 0
fi

# The code-only commit gets an image of its own. B has finished its reuse
# checks; releasing it leaves a builder slot.
discard_owned "$b_uuid" b-before-code
create_branch env-code env-code refs/heads/env-c
code_op=$new_op code_uuid=$new_uuid
code_result=$(wait_operation "$code_op" completed)
code_view=$(wait_ready "$code_uuid")
assert_built "$code_result" "$code_view" "$code_oid" devShells.x86_64-linux.default devshell
image_code=$built_image
test "$image_code" != "$image_two"
test "$(exec_p "$code_uuid" git rev-parse HEAD)" = "$code_oid"
test "$(exec_p "$code_uuid" cat code.txt)" = 'code only'

# Once the last session started from C1 is gone, P deletes C1's image. The
# code-only commit's image stays while its session does.
discard_owned "$a_uuid" a-before-release
test "$(commit_images "$valid_oid" | jq -r '[.[].fingerprint] | join(" ")')" = "$image_two"
discard_owned "$c_uuid" c-before-release
release_deadline=$((SECONDS+120))
while true; do
  remaining=$(commit_images "$valid_oid" | jq -er 'length')
  if test "$remaining" -eq 0; then break; fi
  if ((SECONDS >= release_deadline)); then
    echo "image of $valid_oid not released after its last session" >&2; exit 1
  fi
  sleep 1
done
inc image list --format json | jq -e --arg image "$image_two" 'all(.[]; .fingerprint!=$image)' >/dev/null
test "$(commit_images "$code_oid" | jq -r '[.[].fingerprint] | join(" ")')" = "$image_code"

# A present invalid default is an error, and exact retry remains pinned to
# that captured commit even after its source branch advances to a valid
# absent-default.
printf '{ outputs = _: { devShells.x86_64-linux.default = 42; }; }\n' > "$step_dir/invalid.nix"
inc file push --uid 1000 --gid 1000 --mode 0644 "$step_dir/invalid.nix" "p-$code_uuid/workspace/flake.nix"
exec_p "$code_uuid" git add flake.nix
exec_p "$code_uuid" git -c user.name=P -c user.email=p@example.invalid commit -qm invalid-default
exec_p "$code_uuid" git push origin HEAD:refs/heads/env-code
invalid_oid=$(exec_p "$code_uuid" git rev-parse HEAD)
create_branch env-invalid env-invalid refs/heads/env-code
invalid_op=$new_op invalid_uuid=$new_uuid
invalid_result=$(wait_operation "$invalid_op" blocked)
jq -e --arg oid "$invalid_oid" --arg base "$base" \
  '.result.operation | .phase=="branch-assigned" and .reason=="image_build_failed" and
    (.diagnostic | contains("present default devShell invalid")) and
    (.evidence | .captured_oid==$oid and .image_fingerprint==$base and .image==null and
      .environment_builder=={cycle:1,state:"absent"})' \
  <<< "$invalid_result" >/dev/null
inspect_session "$invalid_uuid" | jq -e --arg oid "$invalid_oid" --arg base "$base" \
  '.result.session.image | .commit_oid==$oid and .selection=="pending" and .state=="pending" and
    .image_fingerprint==$base' >/dev/null
test "$(inc list --format json | jq --arg uuid "$invalid_uuid" '[.[]|select(.name==("p-"+$uuid))]|length')" -eq 0
no_builders
test "$(commit_images "$invalid_oid" | jq 'length')" -eq 0

printf '{ outputs = _: { packages.x86_64-linux.default = 42; }; }\n' > "$step_dir/absent-default.nix"
inc file push --uid 1000 --gid 1000 --mode 0644 "$step_dir/absent-default.nix" "p-$code_uuid/workspace/flake.nix"
exec_p "$code_uuid" git add flake.nix
exec_p "$code_uuid" git -c user.name=P -c user.email=p@example.invalid commit -qm absent-default
exec_p "$code_uuid" git push origin HEAD:refs/heads/env-code
absent_oid=$(exec_p "$code_uuid" git rev-parse HEAD)
rpc operation.retry "$(jq -nc --arg id "$invalid_op" '{id:$id}')" >/dev/null
retried=$(wait_operation "$invalid_op" blocked)
jq -e --arg oid "$invalid_oid" '.result.operation | .reason=="image_build_failed" and
  (.diagnostic | contains("present default devShell invalid")) and .evidence.captured_oid==$oid and
  .evidence.image==null and .evidence.environment_builder=={cycle:2,state:"absent"}' \
  <<< "$retried" >/dev/null
# A commit whose flake has no default devShell gets an image of the base
# packages and its code.
create_branch env-absent env-absent refs/heads/env-code
absent_op=$new_op absent_uuid=$new_uuid
absent_result=$(wait_operation "$absent_op" completed)
absent_view=$(wait_ready "$absent_uuid")
assert_built "$absent_result" "$absent_view" "$absent_oid" base no-default
test "$(exec_p "$absent_uuid" git rev-parse HEAD)" = "$absent_oid"
exec_p "$absent_uuid" test -f /workspace/flake.nix

echo P_PUBLIC_ENVIRONMENT_PASS
