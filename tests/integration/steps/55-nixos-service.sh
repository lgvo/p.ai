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
 if test -n "$op"; then rpc operation.inspect "$(jq -nc --arg id "$op" '{id:$id}')" >&2 || true; fi
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
  response=$(rpc operation.wait "$(jq -nc --arg id "$id" '{id:$id,timeout:20}')")
  status=$(jq -er '.result.operation.status' <<< "$response")
  if test "$status" = completed; then return; fi
  if test "$status" = blocked || test "$status" = failed; then jq -c '.result.operation|{id,status,phase,diagnostic}' <<< "$response" >&2; return 1; fi
  if test "$status" != running; then sleep 0.2; fi
 done
 return 1
}
wait_ready() {
 local deadline=$((SECONDS+40))
 while ((SECONDS<deadline)); do
  if rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" |
   jq -e '.result.session.session_condition=="ready"' >/dev/null; then return; fi
  sleep 0.2
 done
 return 1
}
stop_when_idle() {
 local response status stop deadline=$((SECONDS+40))
 while ((SECONDS<deadline)); do
  if response=$(timeout 150 p api "$socket" session.stop "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')"); then
   stop=$(jq -er '.result | select(.changed==true and .operation.kind=="session.stop") | .operation.id' <<< "$response")
   wait_completed "$stop"
   rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" | jq -e '.result.session.session_condition=="stopped"' >/dev/null
   return
  else
   status=$?
   if ! test "$status" -eq 1 || ! jq -e '.error.message=="retry" and .error.code == -32010 and .error.data.reason=="session_held" and (has("result")|not)' <<< "$response" >/dev/null; then
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
p plugins activate "$state/activation.json" | jq -e 'length==5' >/dev/null
activation_sha=$(sha256sum "$state/activation.json" | cut -d ' ' -f1)
rpc system.hello | jq -e '.result.capabilities|index("runtime")!=null' >/dev/null
created=$(rpc project.create '{"key":"nixos-service55-create","project":"nixos-service55"}')
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
rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" | jq -e '.result.session.session_condition=="stopped"' >/dev/null
start=$(rpc session.start "$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')" |
 jq -er '.result | select(.changed==true and .operation.kind=="session.start") | .operation.id')
wait_completed "$start"
wait_ready
test "$(guest git rev-parse HEAD)" = "$tip"
test "$(guest cat /home/p/service-private)" = dummy-private
test "$(guest cat /home/p/service-credential)" = dummy-credential
stop_when_idle
plan=$(rpc project.delete '{"project":"nixos-service55"}')
tag=$(jq -er '.result | select(.plan.allowed==true) | .change_tag' <<< "$plan")
removed=$(rpc project.delete "$(jq -nc --arg tag "$tag" '{key:"service55-delete",project:"nixos-service55",if_match:$tag}')")
op=$(jq -er '.result.operation.id' <<< "$removed")
wait_completed "$op"
inc list "^p-$uuid$" --format json | jq -e 'length==0' >/dev/null
test ! -e "$state/session_keys/$uuid"
test -s "$state/events.ndjson"
printf 'P_NIXOS_SERVICE_PASS nonroot private-config Git restart StopStart exact-cleanup\n'
