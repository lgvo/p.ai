# shellcheck shell=bash
# Declarative installed service, real sessions, no Codex authentication.
set -E
state=/var/lib/p-service55
socket="$state/control.sock"
uuid=
op=
step_dir="$P_TEST_TMP/step-55"
mkdir -m 0700 "$step_dir"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
unset SSH_AUTH_SOCK GIT_SSH GIT_SSH_COMMAND GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM GIT_CONFIG_COUNT GIT_DIR GIT_WORK_TREE
inc() { timeout 60 incus --force-local --project user-1000 "$@"; }
service_control() { /run/wrappers/bin/sudo -n "$P_TEST_SERVICE_CONTROL_BINARY" "$1"; }
rpc() {
 local response status
 if response=$(timeout 150 p api "$socket" "$@"); then
  printf '%s\n' "$response"
 else
  status=$?
  printf 'service55 RPC failed method=%s status=%s\n' "$1" "$status" >&2
  jq -c '.error' <<< "$response" >&2 || true
  return "$status"
 fi
}
guest() {
 inc exec "p-$uuid" --user 1000 --group 1000 --cwd /workspace \
  --env HOME=/home/p --env GIT_SSH=/usr/libexec/p/git-ssh -- "$@"
}
cleanup() {
 service_control stop || true
 if test -n "$uuid"; then inc delete --force "p-$uuid" >/dev/null 2>&1 || true; fi
}
on_error() {
 printf 'P_NIXOS_SERVICE_FAIL line=%s status=%s\n' "$2" "$1" >&2
 service_control diagnostic >&2 || true
 if test -n "$op"; then rpc operation.inspect "$(jq -nc --arg id "$op" '{v:1,id:$id}')" >&2 || true; fi
}
trap cleanup EXIT
trap 'on_error "$?" "$LINENO"' ERR
wait_health() {
 local deadline=$((SECONDS+40))
 while ((SECONDS<deadline)); do
  if test -S "$socket" && rpc system.health | jq -e '.result.control_state=="ready"' >/dev/null 2>&1; then return; fi
  sleep 0.2
 done
 service_control diagnostic >&2
 return 1
}
wait_completed() {
 local id="$1" response status deadline=$((SECONDS+150))
 while ((SECONDS<deadline)); do
  response=$(rpc operation.inspect "$(jq -nc --arg id "$id" '{v:1,id:$id}')")
  status=$(jq -er '.result.operation.status' <<< "$response")
  if test "$status" = completed; then return; fi
  if test "$status" = blocked || test "$status" = failed; then jq -c '.result.operation|{id,status,phase,diagnostic}' <<< "$response" >&2; return 1; fi
  sleep 0.2
 done
 return 1
}
wait_ready() {
 local deadline=$((SECONDS+40))
 while ((SECONDS<deadline)); do
  if rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
   jq -e '.result.session.session_condition=="ready"' >/dev/null; then return; fi
  sleep 0.2
 done
 return 1
}
stop_when_idle() {
 local response status deadline=$((SECONDS+40))
 while ((SECONDS<deadline)); do
  if response=$(timeout 150 p api "$socket" session.stop "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')"); then
   jq -e '.result.session.session_condition=="stopped"' <<< "$response" >/dev/null
   return
  else
   status=$?
   if ! test "$status" -eq 1 || ! jq -e '.error.kind=="busy" and .error.code == -32003 and (has("result")|not)' <<< "$response" >/dev/null; then
    jq -c '.error' <<< "$response" >&2 || true
    return 1
   fi
  fi
  sleep 0.2
 done
 echo 'service55 Stop remained busy after Start' >&2
 return 1
}
wait_health
test "$(systemctl show p.service -p User --value)" = pdev
test "$(systemctl show p.service -p Group --value)" = users
test "$(systemctl show p.service -p NoNewPrivileges --value)" = yes
test "$(systemctl show p.service -p ProtectSystem --value)" = strict
test "$(stat -c '%a:%u' "$state")" = "700:$(id -u)"
test "$(stat -c '%a:%u:%h' "$state/host.json")" = "600:$(id -u):1"
test "$(stat -c '%a:%u' "$state/activation.json")" = "600:$(id -u)"
p plugins activate "$state/activation.json" | jq -e 'length==6' >/dev/null
activation_sha=$(sha256sum "$state/activation.json" | cut -d ' ' -f1)
rpc system.capabilities | jq -e '.result.available|index("project.create")!=null and index("project.delete.confirm")!=null' >/dev/null
created=$(rpc project.create '{"v":1,"key":"nixos-service55-create","project":"nixos-service55"}')
op=$(jq -er '.result.operation.id' <<< "$created")
uuid=$(jq -er '.result.operation.session_uuid' <<< "$created")
wait_completed "$op"
wait_ready
inc list "^p-$uuid$" --format json | jq -e 'length==1 and .[0].expanded_config["security.idmap.isolated"]=="true" and .[0].expanded_config["security.nesting"]=="false" and ([.[0].expanded_devices[]|select(.type=="nic")]|length)==0' >/dev/null
guest /run/current-system/sw/bin/bash -c 'printf persistent > service-tracked; printf dummy-private > /home/p/service-private; printf dummy-credential > /home/p/service-credential'
guest git add service-tracked
guest git -c user.name=P -c user.email=p@example.invalid commit -qm service55
guest git push origin HEAD:refs/heads/main
tip=$(guest git rev-parse HEAD)
key_sha=$(sha256sum "$state/session_keys/$uuid" | cut -d ' ' -f1)
host_pid=$(guest tmux -S /run/p-interactive/tmux.sock display-message -p '#{pid}')
service_control restart
wait_health
wait_ready
test "$(guest tmux -S /run/p-interactive/tmux.sock display-message -p '#{pid}')" = "$host_pid"
test "$(sha256sum "$state/session_keys/$uuid" | cut -d ' ' -f1)" = "$key_sha"
test "$(sha256sum "$state/activation.json" | cut -d ' ' -f1)" = "$activation_sha"
stop_when_idle
service_control restart
wait_health
rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" | jq -e '.result.session.session_condition=="stopped"' >/dev/null
rpc session.start "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" >/dev/null
wait_ready
test "$(guest git rev-parse HEAD)" = "$tip"
test "$(guest cat /home/p/service-private)" = dummy-private
test "$(guest cat /home/p/service-credential)" = dummy-credential
stop_when_idle
loss=$(rpc workspace.loss.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,key:"service55-loss",uuid:$uuid}')")
op=$(jq -er '.result.operation.id' <<< "$loss")
wait_completed "$op"
review=$(rpc project.delete.preview "$(jq -nc --arg uuid "$uuid" --arg loss "$op" '{v:1,project:"nixos-service55",loss_operations:{($uuid):$loss}}')")
token=$(jq -er '.result.preview.confirmation_token' <<< "$review")
removed=$(rpc project.delete.confirm "$(jq -nc --arg token "$token" '{v:1,key:"service55-delete",project:"nixos-service55",confirmation_token:$token}')")
op=$(jq -er '.result.operation.id' <<< "$removed")
wait_completed "$op"
inc list "^p-$uuid$" --format json | jq -e 'length==0' >/dev/null
test ! -e "$state/session_keys/$uuid"
test -s "$state/events.ndjson"
printf 'P_NIXOS_SERVICE_PASS nonroot private-config Git restart StopStart exact-cleanup\n'
