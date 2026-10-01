#!/usr/bin/env bash
# Native assertions for a test-owned consolidated lab disk; prepare writes a note.
set -euo pipefail
umask 077
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
record="$HOME/.local/state/p-lab-public-check.json"
rpc() { timeout 30 p api "$@"; }
uuid=$(jq -er '.session_uuid' /var/lib/p-demo/lab-notes.json)
params=$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')
guest() {
  timeout 60 incus --force-local --project user-1000 exec \
    --force-noninteractive --disable-stdin "p-$uuid" --user 1000 --group 1000 --cwd /workspace \
    --env HOME=/home/p --env XDG_RUNTIME_DIR=/run/user/1000 \
    --env DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus -- "$@"
}
sql() { guest psql -h /home/p/.local/state/p-notes/socket -d notes -Atc "$1"; }
systemctl is-active --quiet p-lab-repository p-lab-notes
test -e /var/lib/p-demo/lab-notes-ready
rpc project.list '{"v":1,"limit":8}' | jq -e '.result.projects | length==2 and any(.path=="p-ai") and any(.path=="notes")' >/dev/null
rpc session.list '{"v":1,"limit":8}' | jq -e --arg uuid "$uuid" \
  '.result.sessions | length==2 and any(.project=="p-ai" and .branch=="main") and any(.uuid==$uuid and .project=="notes" and .branch=="main")' >/dev/null
case "${1-}" in
  prepare) test ! -e "$record" ;;
  check)
    test "$(jq -er '.uuid' "$record")" = "$uuid"
    test "$(jq -er '.boot' "$record")" != "$(cat /proc/sys/kernel/random/boot_id)"
    if test "$(rpc session.inspect "$params" | jq -er '.result.session.session_condition')" = stopped; then
      rpc session.start "$params" >/dev/null
    fi
    ;;
  *) echo 'Usage: bash /etc/p-lab-public-check.sh prepare|check (test-owned disk only)' >&2; exit 2 ;;
esac
deadline=$((SECONDS+90))
until rpc session.inspect "$params" | jq -e '.result.session.session_condition=="ready"' >/dev/null; do
  test "$SECONDS" -lt "$deadline"; sleep .2
done
until guest python3 examples/notes/client.py health >/dev/null 2>&1; do
  test "$SECONDS" -lt "$deadline"; sleep .2
done
rpc session.services "$params" | jq -e \
  '[.result.services[] | select(.unit|startswith("p-project-notes-"))] | length==3 and all(.active_state=="active")' >/dev/null
source=$(guest git rev-parse HEAD)
p_uuid=$(jq -er '.session_uuid' /var/lib/p-demo/lab-repository.json)
if test "$1" = prepare; then
  guest python3 examples/notes/client.py add 'consolidated lab persists'
  guest bash -euc 'printf private-retained > /home/p/lab-check-private; printf source-edit-retained >> examples/notes/README.md'
  mkdir -p "$(dirname "$record")"
  jq -n --arg uuid "$uuid" --arg p_uuid "$p_uuid" --arg source "$source" \
    --arg boot "$(cat /proc/sys/kernel/random/boot_id)" \
    '{uuid:$uuid,p_uuid:$p_uuid,source:$source,boot:$boot}' > "$record"
else
  test "$p_uuid" = "$(jq -er '.p_uuid' "$record")"
  test "$source" = "$(jq -er '.source' "$record")"
  test "$(guest cat /home/p/lab-check-private)" = private-retained
  test "$(guest tail -c 20 examples/notes/README.md)" = source-edit-retained
fi
deadline=$((SECONDS+30))
until test "$(sql "SELECT count(*) FROM notes WHERE body='consolidated lab persists' AND word_count=3")" = 1; do
  test "$SECONDS" -lt "$deadline"; sleep .2
done
test "$(sql 'SELECT count(*) FROM jobs WHERE completed_at IS NULL')" = 0
guest python3 examples/notes/client.py health
printf 'P_LAB_PUBLIC_%s_PASS p-ai notes PostgreSQL web worker identity source private-data\n' "$1"
