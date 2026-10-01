#!/usr/bin/env bash
# Seed once through P, then prepare the session-owned sample services.
set -euo pipefail
umask 077
state=/var/lib/p-demo
if test -e "$state/lab-notes-ready"; then exit 0; fi
export P_SOCKET="$state/control.sock"
export P_LAB_PROJECT=notes P_LAB_RECORD=lab-notes.json
export P_LAB_SEED=lab-notes-seed P_LAB_KEY=p-lab-notes-v1
bash "$P_LAB_REPOSITORY_SEEDER"
uuid=$(jq -er '.session_uuid' "$state/lab-notes.json")
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
guest() {
  timeout 150 incus --force-local --project user-1000 exec \
    --force-noninteractive --disable-stdin "p-$uuid" --user 1000 --group 1000 --cwd /workspace \
    --env HOME=/home/p --env XDG_RUNTIME_DIR=/run/user/1000 \
    --env DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus -- "$@"
}
# A source seed may have finished before an interrupted service setup.
params=$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}')
observed=$(timeout 20 p api session.inspect "$params")
if test "$(jq -er '.result.session.session_condition' <<< "$observed")" = stopped; then
  timeout 20 p api session.start "$params" >/dev/null
fi
deadline=$((SECONDS+180))
until timeout 20 p api session.inspect "$params" | jq -e '.result.session.session_condition=="ready"' >/dev/null; do
  if ((SECONDS >= deadline)); then echo 'Notes session did not become ready.' >&2; exit 1; fi
  sleep 1
done
guest python3 examples/notes/install.py
guest systemctl --user enable --now p-project-notes-db.service p-project-notes-web.service p-project-notes-worker.service
# Type=exec confirms process execution before the HTTP listener is ready.
deadline=$((SECONDS+45))
until guest python3 examples/notes/client.py health >/dev/null 2>&1; do
  if ((SECONDS >= deadline)); then echo 'Notes HTTP/database did not become healthy.' >&2; exit 1; fi
  sleep .2
done
guest python3 examples/notes/client.py health
touch "$state/lab-notes-ready"
echo "P_LAB_NOTES_READY project=notes branch=main session=$uuid"
