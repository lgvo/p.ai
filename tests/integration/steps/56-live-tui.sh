# shellcheck shell=bash
# Real TUI/PTY and session-user services through the installed confined daemon.
set -E
umask 077
step_dir="$P_TEST_TMP/step-56"
mkdir -m 0700 "$step_dir"
socket=/var/lib/p-service55/control.sock
project=tui56
uuid=
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
service_control() { /run/wrappers/bin/sudo -n "$P_TEST_SERVICE_CONTROL_BINARY" "$1"; }
rpc() { timeout 40 p api "$socket" "$@"; }
inc() { timeout 60 incus --force-local --project user-1000 "$@"; }
guest() {
 inc exec "p-$uuid" --user 1000 --group 1000 --cwd /workspace \
  --env HOME=/home/p --env GIT_SSH=/usr/libexec/p/git-ssh \
  --env XDG_RUNTIME_DIR=/run/user/1000 \
  --env DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus -- "$@"
}
cleanup() { service_control stop || true; }
on_error() {
 printf 'P_LIVE_TUI_FAIL line=%s status=%s\n' "$2" "$1" >&2
 service_control diagnostic >&2 || true
 if test -n "$uuid"; then
  rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" >&2 || true
  rpc session.services "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" >&2 || true
  guest /usr/libexec/p/systemctl --user --no-pager show p-project-demo.service \
   --property=ActiveState,SubState,MainPID,Result >&2 || true
 fi
}
trap cleanup EXIT
trap 'on_error "$?" "$LINENO"' ERR
service_control start
deadline=$((SECONDS+40))
while ! rpc system.health | jq -e '.result.control_state=="ready"' >/dev/null 2>&1; do
 test "$SECONDS" -lt "$deadline"
 sleep 0.2
done

# The driver sends real terminal keys; backend observations verify effects.
python3 "$P_TEST_SOURCE/tests/integration/tui-terminal-test.py"
python3 "$P_TEST_SOURCE/tests/integration/tui-drive.py" create "$socket" "$step_dir/uuid"
uuid=$(cat "$step_dir/uuid")
test -n "$uuid"
observed_marker=$(guest head -c 128 /workspace/tui-attachment)
if test "$observed_marker" != native-terminal; then
 printf 'Unexpected native terminal marker: %q\n' "$observed_marker" >&2
 exit 1
fi
rpc session.inspect "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
 jq -e '.result.session.session_condition=="ready" and .result.session.attached_count==0' >/dev/null

# A newly installed inactive user unit must appear without inventing its state.
guest /run/current-system/sw/bin/bash -euc '
 mkdir -p /home/p/.config/systemd/user
 cat > /home/p/.config/systemd/user/p-project-demo.service <<UNIT
[Unit]
Description=TUI native project service
[Service]
ExecStart=/run/current-system/sw/bin/bash -c "echo P_TUI_PROJECT_JOURNAL; exec /run/current-system/sw/bin/sleep infinity"
UNIT
 systemctl --user daemon-reload
'
rpc session.services "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" |
 jq -e '.result.services|any(.unit=="p-project-demo.service")' >/dev/null
python3 "$P_TEST_SOURCE/tests/integration/tui-drive.py" services "$socket" "$step_dir/uuid"

# Infrastructure names and free-form native actions never reach the manager.
for unit in p-interactive.service p-session.target sshd.service ../p-project-demo.service; do
 if output=$(rpc session.service.action "$(jq -nc --arg uuid "$uuid" --arg unit "$unit" '{v:1,uuid:$uuid,unit:$unit,action:"stop"}')"); then
  echo "accepted infrastructure unit $unit" >&2; exit 1
 fi
 jq -e '.error.kind=="invalid_params" and (has("result")|not)' <<< "$output" >/dev/null
done
inc list "^p-$uuid$" --format json |
 jq -e 'length==1 and .[0].expanded_config["security.idmap.isolated"]=="true" and .[0].expanded_config["security.nesting"]=="false" and ([.[0].expanded_devices[]|select(.type=="nic")]|length)==0' >/dev/null

rpc session.stop "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')" >/dev/null
python3 "$P_TEST_SOURCE/tests/integration/tui-drive.py" remove "$socket" "$step_dir/uuid"
inc list "^p-$uuid$" --format json | jq -e 'length==0' >/dev/null
preview=$(rpc project.delete.preview '{"v":1,"project":"tui56","loss_operations":{}}')
token=$(jq -er '.result.preview.confirmation_token' <<< "$preview")
removed=$(rpc project.delete.confirm "$(jq -nc --arg token "$token" '{v:1,key:"tui56-final-delete",project:"tui56",confirmation_token:$token}')")
op=$(jq -er '.result.operation.id' <<< "$removed")
deadline=$((SECONDS+100))
while ! rpc operation.inspect "$(jq -nc --arg id "$op" '{v:1,id:$id}')" | jq -e '.result.operation.status=="completed"' >/dev/null; do
 test "$SECONDS" -lt "$deadline"; sleep 0.2
done
inc list "^p-$uuid$" --format json | jq -e 'length==0' >/dev/null
printf 'P_LIVE_TUI_PASS real-PTY create attach detach navigation user-services journal denial default-No reviewed-delete cleanup\n'
