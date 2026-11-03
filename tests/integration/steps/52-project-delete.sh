# shellcheck shell=bash
# Aggregate deletion of a project with two stopped sessions and one running
# session, which the deletion stops itself. P does not look inside the
# containers or contact an origin: the plan lists each container's data as
# lost. Private/credential bytes are dummy sentinels; outages and competing
# identities below are explicit test injection.
set -E
umask 077
if test "${P_PROJECT_DELETE_CACHED:-0}" = 1; then
 step_dir="$P_TEST_TMP/step-53"
 endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step53
else
 step_dir="$P_TEST_TMP/step-52"
 endpoint_prefix=/var/lib/p-vm/endpoints/pdev/step52
fi
mkdir -m 0700 "$step_dir"
state="$step_dir/state"
mkdir -m 0700 "$state"
socket="$state/control.sock"
mkdir -m 0700 "$endpoint_prefix"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
daemon_pid=
main=
work=
live=
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
 for id in "$main" "$work" "$live" "$unrelated"; do
  if test -n "$id"; then inc delete --force "p-$id" >/dev/null 2>&1 || true; rm -rf -- "${endpoint_prefix:?}/${id:?}"; fi
 done
 for name in "$parked" "$competing"; do if test -n "$name"; then inc delete --force "$name" >/dev/null 2>&1 || true; fi; done
 rmdir "$endpoint_prefix" 2>/dev/null || true
}
on_error() {
 printf 'P_PROJECT_DELETE_FAIL line=%s status=%s\n' "$2" "$1" >&2
 if test -n "${plan:-}"; then
  jq -c '.result | {allowed:.plan.allowed,refusals:.plan.refusals,loss:.plan.loss,gone:.plan.gone,
   has_change_tag:has("change_tag"),sessions:[.plan.details.sessions[]? |
    {uuid:.session.uuid,condition,runtime:.runtime.condition,status:.runtime.original_status}]}' \
   <<< "$plan" 2>/dev/null | head -c 2048 >&2 || true
  printf '\n' >&2
 fi
 for op in "${op_ids[@]}"; do rpc operation.inspect "$(jq -nc --arg id "$op" '{id:$id}')" 2>/dev/null |
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
activate "$P_TEST_SOURCE/plugins/bundled/tmux-host" "$step_dir/runtime.json" session.asset.install

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
  --arg host_id "$(jq -er '.plugins[0].id' "$step_dir/runtime.json")" \
  --arg incus_binary "$incus_wrapper" --arg prefix "$endpoint_prefix" --arg image "$base" \
  '{schema:"p.host/v1",state_dir:$state,
    git:{activation_path:$git_activation,source_plugin_id:$git_id,listen:"127.0.0.1:0"},
    runtime:{activation_path:$runtime_activation,
      host_plugin_id:$host_id,driver:{name:"incus",config:{binary:$incus_binary,
      user_socket:"/var/lib/incus/unix.socket.user",project:"user-1000"}},
      endpoint_prefix:$prefix,disk_source_ceilings:["/var/lib/p-vm/endpoints","/var/lib/p-vm/grants"],
      base_image_fingerprint:$image,
      project_policy:{network:"none",filesystem_mounts:[],command:["/run/current-system/sw/bin/bash"]}}}' > "$step_dir/host.json"
bash "$P_TEST_SOURCE/tests/integration/public-egress-host-config.sh" "$step_dir/host.json"
if test "${P_PROJECT_DELETE_CACHED:-0}" = 1; then
 jq '.runtime.environment={system:"x86_64-linux",builder_storage_pool:"builders"}' \
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
rpc system.hello | jq -e '.result.capabilities|index("runtime")!=null' >/dev/null
# Loss inspection is not part of the protocol.
if loss=$(rpc workspace.loss.inspect '{}' 2> "$step_dir/api.err"); then
 echo 'workspace.loss.inspect unexpectedly answered' >&2
 exit 1
fi
jq -e '.error.message=="method_not_found" and .error.code==-32601' <<< "$loss" >/dev/null
wait_operation() {
 local id="$1" want="$2" result status deadline=$((SECONDS+190))
 while ((SECONDS<deadline)); do
  result=$(rpc operation.wait "$(jq -nc --arg id "$id" '{id:$id,timeout:20}')")
  status=$(jq -er '.result.operation.status' <<< "$result")
  if test "$status" = "$want"; then printf '%s\n' "$result"; return; fi
  if test "$status" = blocked || test "$status" = failed || test "$status" = completed; then jq -c '.result.operation|{id,status,phase,diagnostic}' <<< "$result" >&2; return 1; fi
  if test "$status" != running; then sleep 0.3; fi
 done
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
# expect_refused METHOD PARAMS KIND [REASON]: the request is refused with the
# error kind KIND, its code and, when given, REASON.
expect_refused() {
 local response status=0
 response=$(rpc "$1" "$2" 2> "$step_dir/api.err") || status=$?
 if ! test "$status" -eq 1 || ! jq -e --arg kind "$3" --arg reason "${4:-}" '
   {invalid: -32602, retry: -32010, conflict: -32011, precondition_failed: -32013, not_found: -32015} as $codes |
   .error.message==$kind and .error.code==$codes[$kind] and ($reason=="" or .error.data.reason==$reason) and
   (has("result")|not)' <<< "$response" >/dev/null; then
  printf 'P_PROJECT_DELETE_EXPECT_REFUSED_FAIL method=%s status=%s want=%s/%s\n' "$1" "$status" "$3" "${4:-}" >&2
  jq -c '{error,operation:(.result.operation // {} | {id,status,phase})}' <<< "$response" | head -c 2048 >&2
  printf '\n' >&2
  return 1
 fi
}
# plan_delete: the deletion's dry run. run_params KEY TAG: its run's params,
# the same plus the key and the dry run's change tag.
plan_delete() {
 rpc project.delete '{"project":"bulk-delete"}'
}
run_params() {
 jq -nc --arg key "$1" --arg tag "$2" '{project:"bulk-delete",key:$key,if_match:$tag}'
}
change_tag() { jq -er '.result.change_tag | select(test("^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{64}$"))' <<< "$1"; }
assert_project_active() {
 rpc project.list '{"limit":100}' | jq -e '.result.projects|any(.path=="bulk-delete" and .registry_state=="active")' >/dev/null
 for uuid in "$main" "$work"; do
  rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" | jq -e '.result.session.registry_state=="established" and .result.session.session_condition=="stopped"' >/dev/null
  test -f "$state/session_keys/$uuid"
 done
 # The running session stays running until the deletion stops it.
 rpc session.inspect "$(jq -nc --arg uuid "$live" '{uuid:$uuid}')" | jq -e '.result.session.registry_state=="established"' >/dev/null
 inc list "^p-$live$" --format json | jq -e 'length==1 and .[0].status=="Running"' >/dev/null
 test -f "$state/session_keys/$live"
 test -d "$repo"
}
created=$(rpc project.create '{"key":"bulk-bootstrap","project":"bulk-delete"}')
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
created=$(rpc session.create '{"key":"bulk-work","project":"bulk-delete","branch":"work","choice":"new","source":"refs/heads/main"}')
work=$(jq -er '.result.operation.session_uuid' <<< "$created")
work_creator=$(jq -er '.result.operation.id' <<< "$created")
op_ids+=("$work_creator")
wait_operation "$work_creator" completed >/dev/null
if test "${P_PROJECT_DELETE_CACHED:-0}" = 1; then
 # The work session starts from the image its creation built for main's commit.
 view=$(rpc session.inspect "$(jq -nc --arg uuid "$work" '{uuid:$uuid}')")
 commit_image=$(jq -er --arg oid "$tip" --arg base "$base" '.result.session.image |
  select(.state=="built" and .commit_oid==$oid and .selection=="devShells.x86_64-linux.default" and
   .base_image_fingerprint==$base) | .image_fingerprint | select(length==64)' <<< "$view")
 test "$commit_image" != "$base"
 inc image list --format json | jq -e --arg image "$commit_image" --arg oid "$tip" \
  'any(.[]; .fingerprint==$image and .properties["p.project_path"]=="bulk-delete" and
   .properties["p.source_commit"]==$oid and .properties["p.selection"]=="devshell")' >/dev/null
fi
# A third session stays running: the deletion stops it itself.
created=$(rpc session.create '{"key":"bulk-live","project":"bulk-delete","branch":"live","choice":"new","source":"refs/heads/main"}')
live=$(jq -er '.result.operation.session_uuid' <<< "$created")
live_creator=$(jq -er '.result.operation.id' <<< "$created")
op_ids+=("$live_creator")
wait_operation "$live_creator" completed >/dev/null
created=$(rpc project.create '{"key":"unrelated-bootstrap","project":"bulk-other"}')
unrelated=$(jq -er '.result.operation.session_uuid' <<< "$created")
other_creator=$(jq -er '.result.operation.id' <<< "$created")
op_ids+=("$other_creator")
wait_operation "$other_creator" completed >/dev/null
guest "$unrelated" /run/current-system/sw/bin/bash -c 'printf unrelated-private > /home/p/p-bulk-other'
for uuid in "$main" "$work"; do
 change_session session.stop "$uuid"
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
# The removed loss inspection parameter is refused by strict decoding.
expect_refused project.delete '{"project":"bulk-delete","loss_operations":{}}' invalid param
plan=$(plan_delete)
tag=$(change_tag "$plan")
# Every container's data is lost, the stopped ones' and the running one's,
# with the project's only commit and its unreachable Git objects.
jq -e --arg main "$main" --arg work "$work" --arg live "$live" --arg tip "$tip" '.result.plan |
 .action=="project.delete" and .target=="project/bulk-delete" and .allowed==true and .refusals==[] and .gone==[] and
 .loss==([.details.sessions[].session.uuid | {kind:"runtime_data",target:("session/"+.),certain:true}] +
  [{kind:"p_commits",target:"project/bulk-delete",count:1,sample:[$tip],certain:true},
   {kind:"unreachable_objects",target:"project/bulk-delete",certain:false,reason:"not_enumerated"}]) and
 (.details | .outcome=="delete_project_and_all_p_data" and (.sessions|length)==3 and
  ([.sessions[].session.uuid]|sort)==([$main,$work,$live]|sort) and .attachments==[] and
  (.branch_loss.p_refs|length)==4 and .branch_loss.commits_losing_p_reachability==[$tip] and
  (.branch_loss|has("origin")|not) and (has("warnings")|not) and
  (.sessions|all(.runtime|.condition=="present" and .incus_project=="user-1000" and (.incus_uuid|length)>0 and (.generation|length)>0 and (.image_fingerprint|length)==64 and (has("runtime_data_lost") or has("loss") or has("loss_operation_id") or has("fingerprint") or has("observed_at")|not))) and
  ([.sessions[] | select(.session.uuid==$live) | .condition=="running" and .runtime.original_status=="Running"]==[true]) and
  ([.sessions[] | select(.session.uuid!=$live) | .condition=="stopped" and .runtime.original_status=="Stopped"]==[true,true]))' <<< "$plan" >/dev/null
# A new retained ref changes the plan: the run is stale, records no
# operation, and nothing is retired.
git -C "$repo" update-ref refs/heads/changed-after-preview "$tip"
expect_refused project.delete "$(run_params stale-ref "$tag")" precondition_failed changed
rpc operation.list '{"limit":20}' | jq -e '.result.operations | all(.[]; .kind!="project.delete")' >/dev/null
assert_project_active
for uuid in "$main" "$work"; do
 rm -f -- "$step_dir/pulled"
 inc file pull "p-$uuid/home/p/p-bulk-private" "$step_dir/pulled"
 cmp "$step_dir/private-$uuid" "$step_dir/pulled"
 rm -f -- "$step_dir/pulled"
 inc file pull "p-$uuid/home/p/p-bulk-dummy-credential" "$step_dir/pulled"
 cmp "$step_dir/dummy-$uuid" "$step_dir/pulled"
done
plan=$(plan_delete)
tag=$(change_tag "$plan")
# first and second are the stopped sessions, in the order the deletion
# removes them.
first=$(jq -er '[.result.plan.details.sessions[] | select(.condition=="stopped")][0].session.uuid' <<< "$plan")
if test "${P_PROJECT_DELETE_CACHED:-0}" = 1; then
 jq -e --arg image "$commit_image" --arg oid "$tip" '.result.plan.details | (has("cache_images")|not) and
  (.images|length)==1 and (.images[0] | .image_present==true and .image.ref==$image and
   .image.key.project=="bulk-delete" and .image.key.commit_oid==$oid and .image.state=="ready")' <<< "$plan" >/dev/null
else
 jq -e '.result.plan.details | .images==[] and (has("cache_images")|not)' <<< "$plan" >/dev/null
fi
second=$(jq -er '[.result.plan.details.sessions[] | select(.condition=="stopped")][1].session.uuid' <<< "$plan")
# P does not look inside the containers: workspace bytes changed after the
# dry run do not refuse the run.
printf changed-workspace > "$step_dir/dirty"
inc file push "$step_dir/dirty" "p-$work/workspace/tracked" --uid 1000 --gid 1000 --mode 0644
printf 'p-%s\n' "$first" > "$step_dir/confirmed-first"
printf 'p-%s\n' "$second" > "$step_dir/confirmed-second"
: > "$step_dir/outage-arm"
# The run returns its operation at once (within p api's ten-second
# deadline); the deletion runs afterwards.
ran=$(rpc project.delete "$(run_params bulk-confirm "$tag")")
jq -e '.result|(has("plan")|not) and (has("change_tag")|not) and (.operation|.kind=="project.delete" and .status=="running" and .phase=="ensure-absent" and .committed==true)' <<< "$ran" >/dev/null
deletion=$(jq -er '.result.operation.id' <<< "$ran")
op_ids+=("$deletion")
result=$(wait_operation "$deletion" blocked)
jq -e --arg first "$first" --arg second "$second" --arg live "$live" '.result.operation|.kind=="project.delete" and .committed==true and .phase=="ensure-absent" and (.evidence.resources|any(.kind=="runtime" and .id==$first and .status=="deleted")) and (.evidence.resources|any(.kind=="runtime" and .id==$second and .status=="unreachable" and .delete_issued!=true)) and (.evidence.resources|any(.kind=="runtime" and .id==$live and .status=="deleted" and .stop_issued==true))' <<< "$result" >/dev/null
test -f "$step_dir/native-unavailable-seen"
inc list "^p-$first$" --format json | jq -e 'length==0' >/dev/null
test ! -e "$state/session_keys/$first"
# The running session was stopped, then deleted, by the deletion itself.
inc list "^p-$live$" --format json | jq -e 'length==0' >/dev/null
test ! -e "$state/session_keys/$live"
test -f "$state/session_keys/$second"
test -d "$repo"
stop_daemon
start_daemon
rpc project.list '{"limit":100}' | jq -e '.result.projects|any(.path=="bulk-delete" and .registry_state=="deleting")' >/dev/null
for uuid in "$main" "$work" "$live"; do rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" | jq -e '.result.session.registry_state=="removing"' >/dev/null; done
expect_refused session.create '{"key":"closed-new","project":"bulk-delete","branch":"closed","choice":"new","source":"refs/heads/main"}' conflict project_deleting
expect_refused session.start "$(jq -nc --arg uuid "$second" '{uuid:$uuid}')" conflict session_not_established
rpc session.create '{"key":"bulk-work","project":"bulk-delete","branch":"work","choice":"new","source":"refs/heads/main"}' | jq -e '.result.operation.status=="completed"' >/dev/null
# Explicit fixture competing name; the original exact runtime is parked and
# preserved. Neither fixture mutation is performed by P's deletion worker.
parked="p-bulk-parked-$second"
inc move "p-$second" "$parked"
competing="p-$second"
inc init "$base" "$competing" --profile default --config security.idmap.isolated=true --config security.nesting=false
rm -f -- "$step_dir/native-unavailable" "$step_dir/outage-arm"
rpc operation.retry "$(jq -nc --arg id "$deletion" '{id:$id}')" >/dev/null
wait_operation "$deletion" blocked >/dev/null
inc list "^$competing$" --format json | jq -e 'length==1' >/dev/null
inc list "^$parked$" --format json | jq -e 'length==1 and .[0].status=="Stopped"' >/dev/null
test -f "$state/session_keys/$second"
test -d "$repo"
inc delete "$competing"
competing=
inc move "$parked" "p-$second"
parked=
rpc operation.retry "$(jq -nc --arg id "$deletion" '{id:$id}')" >/dev/null
wait_operation "$deletion" completed >/dev/null
rpc project.list '{"limit":100}' | jq -e '(.result.projects|any(.path=="bulk-delete")|not) and (.result.projects|any(.path=="bulk-other" and .registry_state=="active"))' >/dev/null
for uuid in "$main" "$work" "$live"; do
 inc list "^p-$uuid$" --format json | jq -e 'length==0' >/dev/null
 test ! -e "$state/session_keys/$uuid"
 test ! -e "$endpoint_prefix/$uuid"
done
test ! -e "$repo"
if test "${P_PROJECT_DELETE_CACHED:-0}" = 1; then
 # Project deletion deleted the project's commit image.
 inc image list --format json | jq -e --arg image "$commit_image" \
  'all(.[]; .fingerprint!=$image and (.properties["p.project_path"] // "")!="bulk-delete")' >/dev/null
 printf 'P_PROJECT_DELETE_CACHED_IMAGE_PASS\n'
fi
test -d "$other_repo"
test "$(sha256sum "$state/session_keys/$unrelated" | cut -d' ' -f1)" = "$other_key"
test "$(guest "$unrelated" /run/current-system/sw/bin/cat /home/p/p-bulk-other)" = unrelated-private
inc image list --format json | jq -e --arg base "$base" 'any(.fingerprint==$base)' >/dev/null
# The deleted project's request keys are retired: reusing one is refused.
expect_refused project.create '{"key":"bulk-bootstrap","project":"bulk-delete"}' invalid key_reused
expect_refused session.create '{"key":"bulk-work","project":"bulk-delete","branch":"work","choice":"new","source":"refs/heads/main"}' invalid key_reused
rpc project.delete "$(run_params bulk-confirm "$tag")" | jq -e --arg id "$deletion" '.result.operation.id==$id and .result.operation.status=="completed"' >/dev/null
test "$(wc -l < "$step_dir/source-effects")" -eq 2
test "$(awk '$1!="delete"{n++} END{print n+0}' "$step_dir/source-effects")" -eq 0
# No native resource of the deleted project remains.
inc list --format json | jq -e --arg project bulk-delete 'all((.config["user.p.project_path"] // "")!=$project and (.config["user.p.builder_project_path"] // "")!=$project)' >/dev/null
printf 'P_PROJECT_DELETE_STALE_PRESERVED\nP_PROJECT_DELETE_RESTART_IDENTITY_PRESERVED\nP_PROJECT_DELETE_PASS\n'
