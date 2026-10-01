# shellcheck shell=bash
# Major native effects of the notes journey, encoded after live LLM review.
set -E
umask 077
step_dir="$P_TEST_TMP/step-58"
mkdir -m 0700 "$step_dir"
socket=/var/lib/p-service55/control.sock
project=notes58
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
service_control() { /run/wrappers/bin/sudo -n "$P_TEST_SERVICE_CONTROL_BINARY" "$1"; }
inc() { timeout 60 incus --force-local --project user-1000 "$@"; }
rpc() {
 local response status deadline=$((SECONDS+40))
 while true; do
  if response=$(timeout 150 p api "$socket" "$@"); then
   printf '%s\n' "$response"; return
  else status=$?; fi
  # Only observations retry an explicit authority conflict. Mutations keep
  # their normal idempotency and confirmation semantics.
  case "$1" in
   system.health|project.list|session.list|operation.inspect)
    if test "$SECONDS" -lt "$deadline" && jq -e '.error.kind=="busy"' <<< "$response" >/dev/null 2>&1; then
     sleep 0.1; continue
    fi ;;
  esac
  printf '%s\n' "$response"
  return "$status"
 done
}
cleanup() {
 service_control stop || true
 if test -f "$step_dir/owned-uuids"; then
  while IFS= read -r owned; do
   # Only recorded UUIDs from this disposable test are eligible for cleanup.
   if [[ $owned =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]]; then
    inc delete --force "p-$owned" >/dev/null 2>&1 || true
   fi
  done < "$step_dir/owned-uuids"
 fi
}
on_error() {
 printf 'P_NOTES_TUI_FAIL line=%s status=%s\n' "$2" "$1" >&2
 service_control diagnostic >&2 || true
 rpc session.list '{"v":1,"limit":8}' >&2 || true
 if test -f "$step_dir/owned-uuids"; then
  while IFS= read -r owned; do
   rpc session.inspect "$(jq -nc --arg uuid "$owned" '{v:1,uuid:$uuid}')" >&2 || true
   rpc session.services "$(jq -nc --arg uuid "$owned" '{v:1,uuid:$uuid}')" >&2 || true
  done < "$step_dir/owned-uuids"
 fi
}
trap cleanup EXIT
trap 'on_error "$?" "$LINENO"' ERR
service_control start
deadline=$((SECONDS+40))
while ! rpc system.health | jq -e '.result.control_state=="ready"' >/dev/null 2>&1; do
 test "$SECONDS" -lt "$deadline"; sleep 0.2
done
rpc project.list '{"v":1,"limit":100}' | jq -e --arg project "$project" '.result.projects|all(.path!=$project)' >/dev/null

# Navigation belongs to the reviewed driver. Fixture setup and API/native
# observations establish actual SQL, unit, Git and attachment effects.
python3 "$P_TEST_SOURCE/tests/integration/tui-drive.py" notes "$socket" "$step_dir/uuid"
test -s "$step_dir/uuid"
test -s "$step_dir/owned-uuids"
rpc session.list '{"v":1,"limit":8}' | jq -e --arg project "$project" '.result.sessions|all(.project!=$project)' >/dev/null
rpc project.branches "$(jq -nc --arg project "$project" '{v:1,project:$project,limit:8}')" | jq -e '.result.refs==[]' >/dev/null
while IFS= read -r owned; do
 [[ $owned =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]]
 inc list "^p-$owned$" --format json | jq -e 'length==0' >/dev/null
done < "$step_dir/owned-uuids"

# Whole-project deletion remains an owner API action after the TUI has
# removed the streams. No fabricated preview tokens or privileged cleanup.
preview=$(rpc project.delete.preview "$(jq -nc --arg project "$project" '{v:1,project:$project,loss_operations:{}}')")
token=$(jq -er '.result.preview.confirmation_token' <<< "$preview")
removed=$(rpc project.delete.confirm "$(jq -nc --arg project "$project" --arg token "$token" '{v:1,key:"notes58-project-delete",project:$project,confirmation_token:$token}')")
op=$(jq -er '.result.operation.id' <<< "$removed")
deadline=$((SECONDS+100))
while true; do
 operation=$(rpc operation.inspect "$(jq -nc --arg id "$op" '{v:1,id:$id}')")
 status=$(jq -er '.result.operation.status' <<< "$operation")
 if test "$status" = completed; then break; fi
 test "$status" != failed && test "$status" != blocked
 test "$SECONDS" -lt "$deadline"; sleep 0.2
done
rpc project.list '{"v":1,"limit":100}' | jq -e --arg project "$project" '.result.projects|all(.path!=$project)' >/dev/null
printf 'P_NOTES_TUI_PASS real-PTY sample-application native-services SQL branches attach persistence reviewed-cleanup\n'
