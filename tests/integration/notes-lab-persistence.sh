#!/usr/bin/env bash
# Run as the notes lab owner, once before and once after a clean VM relaunch.
set -euo pipefail
umask 077
record="$HOME/.local/state/p-notes-lab-check.json"
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
rpc() { timeout 150 p api "$@"; }
inc() { timeout 60 incus --force-local --project user-1000 "$@"; }
params() { jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}'; }
guest() {
  inc exec --force-noninteractive --disable-stdin "p-$uuid" --user 1000 --group 1000 --cwd /workspace \
    --env HOME=/home/p --env GIT_SSH=/usr/libexec/p/git-ssh \
    --env XDG_RUNTIME_DIR=/run/user/1000 \
    --env DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus -- "$@"
}
action() {
  rpc session.service.action "$(jq -nc --arg uuid "$uuid" --arg unit "p-project-notes-$1.service" --arg action "$2" \
    '{v:1,uuid:$uuid,unit:$unit,action:$action}')" >/dev/null
}
sql() { guest psql -h /home/p/.local/state/p-notes/socket -d notes -Atc "$1"; }
wait_ready() {
  local deadline=$((SECONDS+180))
  until rpc session.inspect "$(params)" | jq -e '.result.session.session_condition=="ready"' >/dev/null; do
    test "$SECONDS" -lt "$deadline"; sleep .2
  done
}
setup() {
  guest python3 examples/notes/install.py
  action db start
  action web start
  action worker start
  guest python3 examples/notes/client.py health
}
case "${1-}" in
  prepare)
    test ! -e "$record"
    created=$(rpc project.create '{"v":1,"key":"notes-lab-persistence","project":"notes-persistence"}')
    uuid=$(jq -er '.result.operation.session_uuid' <<< "$created")
    wait_ready
    guest bash -euc 'mkdir -p examples/notes; cp -R /etc/p-notes-example/. examples/notes; chmod -R u+w examples/notes; git add examples/notes; git -c user.name=P -c user.email=p@example.invalid commit -qm notes; git push origin HEAD:main'
    pushed=$(guest git rev-parse HEAD)
    setup
    guest python3 examples/notes/client.py add 'retained across full lab reboot'
    deadline=$((SECONDS+30))
    until test "$(sql 'SELECT count(*) FROM jobs WHERE completed_at IS NULL')" = 0; do test "$SECONDS" -lt "$deadline"; sleep .2; done
    action worker stop
    guest python3 examples/notes/client.py add 'queued across full lab reboot'
    guest bash -euc 'printf persistent-private > /home/p/persistence-marker; printf local-commit > persistence-local; git add persistence-local; git -c user.name=P -c user.email=p@example.invalid commit -qm local-only; printf dirty-retained > persistence-dirty'
    local_oid=$(guest git rev-parse HEAD)
    mkdir -p "$(dirname "$record")"
    jq -n --arg uuid "$uuid" --arg pushed "$pushed" --arg local_oid "$local_oid" \
      --arg boot "$(cat /proc/sys/kernel/random/boot_id)" \
      --arg principal "$(sha256sum "/var/lib/p-demo/session_keys/$uuid" | cut -d ' ' -f1)" \
      '{uuid:$uuid,pushed:$pushed,local_oid:$local_oid,boot:$boot,principal:$principal}' > "$record"
    printf 'P_NOTES_LAB_PREPARED uuid=%s record=%s\n' "$uuid" "$record"
    ;;
  check)
    uuid=$(jq -er '.uuid' "$record")
    test "$(cat /proc/sys/kernel/random/boot_id)" != "$(jq -er '.boot' "$record")"
    rpc session.inspect "$(params)" > /tmp/p-notes-lab-inspect.json
    condition=$(jq -er '.result.session.session_condition' /tmp/p-notes-lab-inspect.json)
    if test "$condition" = stopped; then rpc session.start "$(params)" >/dev/null; fi
    wait_ready
    test "$(guest git rev-parse HEAD)" = "$(jq -er '.local_oid' "$record")"
    test "$(guest cat persistence-dirty)" = dirty-retained
    test "$(guest cat /home/p/persistence-marker)" = persistent-private
    test "$(sha256sum "/var/lib/p-demo/session_keys/$uuid" | cut -d ' ' -f1)" = "$(jq -er '.principal' "$record")"
    for unit in db web worker; do
      test "$(guest systemctl --user show "p-project-notes-$unit.service" -p MainPID --value)" = 0
    done
    action db start
    test "$(sql 'SELECT count(*) FROM notes')" = 2
    test "$(sql 'SELECT count(*) FROM jobs WHERE completed_at IS NULL')" = 1
    rpc project.branches '{"v":1,"project":"notes-persistence","limit":8}' \
      | jq -e --arg oid "$(jq -er '.pushed' "$record")" '.result.refs|any(.ref=="refs/heads/main" and .oid==$oid)' >/dev/null
    action web start
    action worker start
    deadline=$((SECONDS+30))
    until test "$(sql 'SELECT count(*) FROM jobs WHERE completed_at IS NULL')" = 0; do test "$SECONDS" -lt "$deadline"; sleep .2; done
    guest python3 examples/notes/client.py health
    guest python3 examples/notes/client.py list
    printf 'P_NOTES_LAB_RELAUNCH_PASS uuid=%s identities source private-files units SQL pending-job-recovery\n' "$uuid"
    ;;
  *) echo 'Usage: bash /etc/p-notes-persistence-check.sh prepare|check' >&2; exit 2 ;;
esac
