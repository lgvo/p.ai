# shellcheck shell=bash
# Quiescent aggregate project deletion. Private/credential bytes are dummy
# sentinels; outages and competing identities below are explicit test injection.
set -E
umask 077
step_dir="$P_TEST_TMP/step-52"
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step52
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
daemon_pid=
main=
work=
unrelated=
first=
second=
competing=
parked=
op_ids=()
unset SSH_AUTH_SOCK GIT_SSH GIT_SSH_COMMAND GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM GIT_CONFIG_COUNT GIT_DIR GIT_WORK_TREE
incus_real=$(command -v incus)
inc() { timeout 60 "$incus_real" --force-local --project user-1000 "$@"; }
rpc() {
 local response status=0
 response=$(timeout 170 p api "$socket" "$@") || status=$?
 printf '%s\n' "$response"
 if test "$status" -ne 0; then
  printf 'P_PROJECT_DELETE_RPC_FAIL method=%s status=%s\n' "$1" "$status" >&2
  jq -c '{error,operation:(.result.operation // {} | {id,status,phase})}' <<< "$response" | head -c 2048 >&2
  printf '\n' >&2
 fi
 return "$status"
}
guest() {
 local uuid="$1"; shift
 inc exec "p-$uuid" --user 1000 --group 1000 --cwd /workspace \
  --env HOME=/home/p --env GIT_SSH=/usr/libexec/p/git-ssh -- "$@"
}
stop_daemon() {
 if test -n "$daemon_pid"; then
  kill -TERM "$daemon_pid" 2>/dev/null || true
  for ((i=0;i<100;i++)); do if ! kill -0 "$daemon_pid" 2>/dev/null; then break; fi; sleep 0.1; done
  if kill -0 "$daemon_pid" 2>/dev/null; then kill -KILL "$daemon_pid" 2>/dev/null || true; fi
  wait "$daemon_pid" 2>/dev/null || true
  daemon_pid=
 fi
}
cleanup() {
 stop_daemon
 for id in "$main" "$work" "$unrelated"; do
  if test -n "$id"; then inc delete --force "p-$id" >/dev/null 2>&1 || true; rm -rf -- "${endpoint_prefix:?}/${id:?}"; fi
 done
 for name in "$parked" "$competing"; do if test -n "$name"; then inc delete --force "$name" >/dev/null 2>&1 || true; fi; done
 for op in "${op_ids[@]}"; do inc delete --force "p-workspace-$op" >/dev/null 2>&1 || true; done
 # Clean only this fixture's isolated loss helpers after a failed run.
 while IFS= read -r name; do
  if test -n "$name"; then inc delete --force "$name" >/dev/null 2>&1 || true; fi
 done < <(inc list --format json 2>/dev/null | jq -r '.[ ]|select((.config["user.p.project_path"] // "")=="bulk-delete" and (.name|startswith("p-workspace-")))|.name' 2>/dev/null || true)
 rmdir "$endpoint_prefix" 2>/dev/null || true
}
on_error() {
 printf 'P_PROJECT_DELETE_FAIL line=%s status=%s\n' "$2" "$1" >&2
 for op in "${op_ids[@]}"; do rpc operation.inspect "$(jq -nc --arg id "$op" '{v:1,id:$id}')" 2>/dev/null |
  jq -cr '.result.operation|{id,status,phase,diagnostic,evidence}' >&2 || true; done
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
if test "$#" -ge 5 && test -f "$fixture/confirmed-first"; then
 read -r first < "$fixture/confirmed-first"
 read -r second < "$fixture/confirmed-second"
 if test "$5" = "$first" || test "$5" = "$second"; then
  case "$4" in start|stop|restart|pause|resume|init|delete)
   printf '%s %s\n' "$4" "$5" >> "$fixture/source-effects" ;;
  esac
 fi
 # Inject only after the actual first source DELETE has been dispatched.
 if test "$4" = delete && test "$5" = "$first" && test -f "$fixture/outage-arm"; then
  printf '%s\n' "$second" > "$fixture/native-unavailable"
 fi
fi
if test "$#" -eq 7 && test "$4" = list && test "$6" = --format && test "$7" = json && test -f "$fixture/native-unavailable"; then
 read -r second < "$fixture/native-unavailable"
 if test "$5" = "^$second$"; then
  : > "$fixture/native-unavailable-seen"
  printf 'fixture source observation unavailable\n' >&2
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
if test "${P_PROJECT_DELETE_CACHED:-0}" = 1; then
 activate "$P_TEST_ENV_PLUGIN" "$step_dir/environment.json" environment.nix
 jq --arg path "$step_dir/environment.json" \
  --arg id "$(jq -er '.plugins[0].id' "$step_dir/environment.json")" \
  '.runtime.environment={activation_path:$path,plugin_id:$id,system:"x86_64-linux",builder_storage_pool:"builders"}' \
  "$step_dir/host.json" > "$step_dir/host.next.json"
 mv "$step_dir/host.next.json" "$step_dir/host.json"
fi
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
rpc system.capabilities | jq -e '.result.available|index("project.delete.preview")!=null and index("project.delete.confirm")!=null' >/dev/null
wait_operation() {
 local id="$1" want="$2" result status deadline=$((SECONDS+190))
 while ((SECONDS<deadline)); do
  result=$(rpc operation.inspect "$(jq -nc --arg id "$id" '{v:1,id:$id}')")
  status=$(jq -er '.result.operation.status' <<< "$result")
  if test "$status" = "$want"; then printf '%s\n' "$result"; return; fi
  if test "$status" = blocked || test "$status" = failed || test "$status" = completed; then jq -c '.result.operation|{id,status,phase,diagnostic}' <<< "$result" >&2; return 1; fi
  sleep 0.3
 done
 return 1
}
expect_busy() {
 local response status=0
 response=$(rpc "$1" "$2" 2> "$step_dir/api.err") || status=$?
 if ! test "$status" -eq 1 || ! jq -e '.error.kind=="busy" and .error.code == -32003 and (has("result")|not)' <<< "$response" >/dev/null; then
  printf 'P_PROJECT_DELETE_EXPECT_BUSY_FAIL method=%s status=%s\n' "$1" "$status" >&2
  jq -c '{error,operation:(.result.operation // {} | {id,status,phase})}' <<< "$response" | head -c 2048 >&2
  printf '\n' >&2
  return 1
 fi
}
losses() {
 local tag="$1" id uuid op result after=
 loss_main=
 loss_work=
 for uuid in "$main" "$work"; do
  result=$(rpc workspace.loss.inspect "$(jq -nc --arg key "$tag-$uuid" --arg uuid "$uuid" '{v:1,key:$key,uuid:$uuid}')")
  op=$(jq -er '.result.operation.id' <<< "$result")
  op_ids+=("$op")
  wait_operation "$op" completed >/dev/null
  inc list "^p-workspace-$op$" --format json | jq -e 'length==0' >/dev/null
  if test "$uuid" = "$main"; then loss_main=$op; else loss_work=$op; fi
 done
 # Every implicit fresh confirmation read must clean its own helper too.
 : > "$step_dir/inspect-ids"
 while true; do
  id=$(rpc operation.list "$(jq -nc --arg after "$after" '{v:1,limit:20,after:$after}')")
  jq -e '.result.operations|type=="array"' <<< "$id" >/dev/null
  jq -r '.result.operations[]|select(.project=="bulk-delete" and .kind=="workspace.loss.inspect")|.id' <<< "$id" >> "$step_dir/inspect-ids"
  after=$(jq -er '.result.next|select(type=="string")' <<< "$id")
  if test -z "$after"; then break; fi
 done
 while IFS= read -r id; do
  inc list "^p-workspace-$id$" --format json | jq -e 'length==0' >/dev/null
 done < "$step_dir/inspect-ids"
}
preview() {
 rpc project.delete.preview "$(jq -nc --arg main "$main" --arg work "$work" --arg a "$loss_main" --arg b "$loss_work" '{v:1,project:"bulk-delete",loss_operations:{($main):$a,($work):$b}}')"
}
assert_project_active() {
 rpc project.list '{"v":1,"limit":100}' | jq -e '.result.projects|any(.path=="bulk-delete" and .registry_state=="active")' >/dev/null
 for uuid in "$main" "$work"; do
  rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" | jq -e '.result.session.registry_state=="established" and .result.session.session_condition=="stopped"' >/dev/null
  test -f "$state/session_keys/$uuid"
 done
 test -d "$repo"
}
created=$(rpc project.create '{"v":1,"key":"bulk-bootstrap","project":"bulk-delete"}')
main=$(jq -er '.result.operation.session_uuid' <<< "$created")
bootstrap=$(jq -er '.result.operation.id' <<< "$created")
op_ids+=("$bootstrap")
wait_operation "$bootstrap" completed >/dev/null
guest "$main" /run/current-system/sw/bin/bash -c 'printf committed > tracked; printf "ignored.bin\n" > .gitignore'
if test "${P_PROJECT_DELETE_CACHED:-0}" = 1; then
 # Fixture source, actual public P builder/realization/import/indexing path.
 cat > "$step_dir/flake.nix" <<'FLAKE'
{
 outputs = { self }: {
  devShells.x86_64-linux.default = builtins.derivation {
   name = "p-project-delete-shell-fixture";
   system = "x86_64-linux";
   builder = "/run/current-system/sw/bin/bash";
   args = [ "-c" "printf built > \"$out\"" ];
   outputs = [ "out" ];
   stdenv = ./stdenv;
   NATIVE_VALUE = "ok";
   NATIVE_REV = self.rev;
   shellHook = ''export NATIVE_HOOK=ready'';
  };
 };
}
FLAKE
 printf 'export NATIVE_VALUE=ok\n' > "$step_dir/setup"
 guest "$main" /run/current-system/sw/bin/mkdir -p stdenv
 inc file push --uid 1000 --gid 1000 --mode 0644 "$step_dir/flake.nix" "p-$main/workspace/flake.nix"
 inc file push --uid 1000 --gid 1000 --mode 0644 "$step_dir/setup" "p-$main/workspace/stdenv/setup"
 guest "$main" /run/current-system/sw/bin/git add flake.nix stdenv/setup
fi
guest "$main" /run/current-system/sw/bin/git add tracked .gitignore
guest "$main" /run/current-system/sw/bin/git -c user.name=P -c user.email=p@example.invalid commit -qm source
guest "$main" /run/current-system/sw/bin/git push origin HEAD:refs/heads/main
tip=$(guest "$main" /run/current-system/sw/bin/git rev-parse HEAD)
created=$(rpc session.create '{"v":1,"key":"bulk-work","project":"bulk-delete","branch":"work","choice":"new","source":"refs/heads/main"}')
work=$(jq -er '.result.operation.session_uuid' <<< "$created")
work_creator=$(jq -er '.result.operation.id' <<< "$created")
op_ids+=("$work_creator")
wait_operation "$work_creator" completed >/dev/null
if test "${P_PROJECT_DELETE_CACHED:-0}" = 1; then
 view=$(rpc session.inspect "$(jq -nc --arg uuid "$work" '{v:1,uuid:$uuid}')")
 cache_image=$(jq -er '.result.session.environment|select(.cache=="miss")|.image_fingerprint|select(length==64)' <<< "$view")
 test "$cache_image" != "$base"
 cache_key=$(jq -er '.result.session.environment.environment_key|select(length==64)' <<< "$view")
 rpc environment.cache.list '{"v":1,"project":"bulk-delete","limit":8}' | \
  jq -e --arg image "$cache_image" --arg key "$cache_key" '.result.images|length==1 and .[0].fingerprint==$image and .[0].environment_key==$key' >/dev/null
 inc image list --format json | jq -e --arg image "$cache_image" \
  'any(.fingerprint==$image and .properties["p.project_path"]=="bulk-delete")' >/dev/null
fi
created=$(rpc project.create '{"v":1,"key":"unrelated-bootstrap","project":"bulk-other"}')
unrelated=$(jq -er '.result.operation.session_uuid' <<< "$created")
other_creator=$(jq -er '.result.operation.id' <<< "$created")
op_ids+=("$other_creator")
wait_operation "$other_creator" completed >/dev/null
guest "$unrelated" /run/current-system/sw/bin/bash -c 'printf unrelated-private > /home/p/p-bulk-other'
for uuid in "$main" "$work"; do
 rpc session.stop "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" >/dev/null
 printf 'private-%s\n' "$uuid" > "$step_dir/private-$uuid"
 printf 'dummy-credential-%s\n' "$uuid" > "$step_dir/dummy-$uuid"
 inc file push "$step_dir/private-$uuid" "p-$uuid/home/p/p-bulk-private" --uid 1000 --gid 1000 --mode 0600
 inc file push "$step_dir/dummy-$uuid" "p-$uuid/home/p/p-bulk-dummy-credential" --uid 1000 --gid 1000 --mode 0600
 inc list "^p-$uuid$" --format json | jq -e 'length==1 and .[0].status=="Stopped" and .[0].expanded_config["security.idmap.isolated"]=="true" and (.[0].expanded_config["security.nesting"] // "false")=="false"' >/dev/null
 done
repo_name=$(printf %s bulk-delete | sha256sum | cut -d' ' -f1)
repo="$state/repositories/$repo_name.git"
other_repo_name=$(printf %s bulk-other | sha256sum | cut -d' ' -f1)
other_repo="$state/repositories/$other_repo_name.git"
other_key=$(sha256sum "$state/session_keys/$unrelated" | cut -d' ' -f1)
git -C "$repo" update-ref refs/heads/retained "$tip"
losses initial
review=$(preview)
token=$(jq -er '.result.preview.confirmation_token' <<< "$review")
jq -e --arg main "$main" --arg work "$work" '.result.preview|.outcome=="delete_project_and_all_p_data" and (.sessions|length)==2 and .attachments==[] and (.branch_loss.p_refs|length)==3 and (.branch_loss.commits_losing_p_reachability|length)==1 and .branch_loss.origin.status=="local_only" and (.sessions|all(.runtime.original_status=="Stopped" and (.runtime.loss.fingerprint|length)==64)) and (.warnings|any(contains("private"))) and (.warnings|any(contains("credentials")))' <<< "$review" >/dev/null
# A new retained ref changes aggregate review and refuses before retirement.
git -C "$repo" update-ref refs/heads/changed-after-preview "$tip"
expect_busy project.delete.confirm "$(jq -nc --arg token "$token" '{v:1,key:"stale-ref",project:"bulk-delete",confirmation_token:$token}')"
assert_project_active
losses after-ref
review=$(preview)
token=$(jq -er '.result.preview.confirmation_token' <<< "$review")
printf changed-workspace > "$step_dir/dirty"
inc file push "$step_dir/dirty" "p-$work/workspace/tracked" --uid 1000 --gid 1000 --mode 0644
expect_busy project.delete.confirm "$(jq -nc --arg token "$token" '{v:1,key:"stale-bytes",project:"bulk-delete",confirmation_token:$token}')"
assert_project_active
for uuid in "$main" "$work"; do
 rm -f -- "$step_dir/pulled"
 inc file pull "p-$uuid/home/p/p-bulk-private" "$step_dir/pulled"
 cmp "$step_dir/private-$uuid" "$step_dir/pulled"
 rm -f -- "$step_dir/pulled"
 inc file pull "p-$uuid/home/p/p-bulk-dummy-credential" "$step_dir/pulled"
 cmp "$step_dir/dummy-$uuid" "$step_dir/pulled"
done
losses after-bytes
review=$(preview)
token=$(jq -er '.result.preview.confirmation_token' <<< "$review")
first=$(jq -er '.result.preview.sessions[0].session.uuid' <<< "$review")
if test "${P_PROJECT_DELETE_CACHED:-0}" = 1; then
 jq -e --arg image "$cache_image" '.result.preview.cache_images|length==1 and .[0].image.fingerprint==$image' <<< "$review" >/dev/null
fi
second=$(jq -er '.result.preview.sessions[1].session.uuid' <<< "$review")
printf 'p-%s\n' "$first" > "$step_dir/confirmed-first"
printf 'p-%s\n' "$second" > "$step_dir/confirmed-second"
: > "$step_dir/outage-arm"
confirmed=$(rpc project.delete.confirm "$(jq -nc --arg token "$token" '{v:1,key:"bulk-confirm",project:"bulk-delete",confirmation_token:$token}')")
deletion=$(jq -er '.result.operation.id' <<< "$confirmed")
op_ids+=("$deletion")
result=$(wait_operation "$deletion" blocked)
jq -e --arg first "$first" --arg second "$second" '.result.operation|.kind=="project.delete" and .committed==true and .phase=="ensure-absent" and (.evidence.resources|any(.kind=="runtime" and .id==$first and .status=="deleted")) and (.evidence.resources|any(.kind=="runtime" and .id==$second and .status=="unreachable" and .delete_issued!=true))' <<< "$result" >/dev/null
test -f "$step_dir/native-unavailable-seen"
inc list "^p-$first$" --format json | jq -e 'length==0' >/dev/null
test ! -e "$state/session_keys/$first"
test -f "$state/session_keys/$second"
test -d "$repo"
stop_daemon
start_daemon
rpc project.list '{"v":1,"limit":100}' | jq -e '.result.projects|any(.path=="bulk-delete" and .registry_state=="deleting")' >/dev/null
for uuid in "$main" "$work"; do rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" | jq -e '.result.session.registry_state=="removing"' >/dev/null; done
expect_busy session.create '{"v":1,"key":"closed-new","project":"bulk-delete","branch":"closed","choice":"new","source":"refs/heads/main"}'
expect_busy workspace.loss.inspect "$(jq -nc --arg uuid "$second" '{v:1,key:"closed-read",uuid:$uuid}')"
rpc session.create '{"v":1,"key":"bulk-work","project":"bulk-delete","branch":"work","choice":"new","source":"refs/heads/main"}' | jq -e '.result.operation.status=="completed"' >/dev/null
# Explicit fixture competing name; the original exact runtime is parked and
# preserved. Neither fixture mutation is performed by P's deletion worker.
parked="p-bulk-parked-$second"
inc move "p-$second" "$parked"
competing="p-$second"
inc init "$base" "$competing" --profile default --config security.idmap.isolated=true --config security.nesting=false
rm -f -- "$step_dir/native-unavailable" "$step_dir/outage-arm"
rpc operation.retry "$(jq -nc --arg id "$deletion" '{v:1,id:$id}')" >/dev/null
wait_operation "$deletion" blocked >/dev/null
inc list "^$competing$" --format json | jq -e 'length==1' >/dev/null
inc list "^$parked$" --format json | jq -e 'length==1 and .[0].status=="Stopped"' >/dev/null
test -f "$state/session_keys/$second"
test -d "$repo"
inc delete "$competing"
competing=
inc move "$parked" "p-$second"
parked=
rpc operation.retry "$(jq -nc --arg id "$deletion" '{v:1,id:$id}')" >/dev/null
wait_operation "$deletion" completed >/dev/null
rpc project.list '{"v":1,"limit":100}' | jq -e '(.result.projects|any(.path=="bulk-delete")|not) and (.result.projects|any(.path=="bulk-other" and .registry_state=="active"))' >/dev/null
for uuid in "$main" "$work"; do
 inc list "^p-$uuid$" --format json | jq -e 'length==0' >/dev/null
 test ! -e "$state/session_keys/$uuid"
 test ! -e "$endpoint_prefix/$uuid"
done
test ! -e "$repo"
if test "${P_PROJECT_DELETE_CACHED:-0}" = 1; then
 inc image list --format json | jq -e --arg image "$cache_image" 'all(.fingerprint!=$image)' >/dev/null
 rpc environment.cache.list '{"v":1,"project":"bulk-delete","limit":8}' | jq -e '.result.images==[]' >/dev/null
 printf 'P_PROJECT_DELETE_CACHED_IMAGE_PASS\n'
fi
test -d "$other_repo"
test "$(sha256sum "$state/session_keys/$unrelated" | cut -d' ' -f1)" = "$other_key"
test "$(guest "$unrelated" /run/current-system/sw/bin/cat /home/p/p-bulk-other)" = unrelated-private
inc image list --format json | jq -e --arg base "$base" 'any(.fingerprint==$base)' >/dev/null
expect_busy project.create '{"v":1,"key":"bulk-bootstrap","project":"bulk-delete"}'
expect_busy session.create '{"v":1,"key":"bulk-work","project":"bulk-delete","branch":"work","choice":"new","source":"refs/heads/main"}'
rpc project.delete.confirm "$(jq -nc --arg token "$token" '{v:1,key:"bulk-confirm",project:"bulk-delete",confirmation_token:$token}')" | jq -e --arg id "$deletion" '.result.operation.id==$id and .result.operation.status=="completed"' >/dev/null
test "$(wc -l < "$step_dir/source-effects")" -eq 2
test "$(awk '$1!="delete"{n++} END{print n+0}' "$step_dir/source-effects")" -eq 0
# No unaccounted native helper remains, including internal fresh confirmations.
inc list --format json | jq -e --arg project bulk-delete 'all((.config["user.p.project_path"] // "")!=$project and (.config["user.p.builder_project_path"] // "")!=$project)' >/dev/null
printf 'P_PROJECT_DELETE_STALE_PRESERVED\nP_PROJECT_DELETE_RESTART_IDENTITY_PRESERVED\nP_PROJECT_DELETE_PASS\n'
