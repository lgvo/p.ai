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
# A source seed may have finished before an interrupted service setup and
# left the session stopped: its start records an operation, waited for here.
params=$(jq -nc --arg uuid "$uuid" '{uuid:$uuid}')
observed=$(timeout 20 p api session.inspect "$params")
deadline=$((SECONDS+180))
if test "$(jq -er '.result.session.session_condition' <<< "$observed")" = stopped; then
  started=$(timeout 20 p api session.start "$params")
  if operation=$(jq -er '.result.operation.id' <<< "$started"); then
    status=running
    while test "$status" = running; do
      if ((SECONDS >= deadline)); then echo 'Notes session start did not end.' >&2; exit 1; fi
      status=$(timeout 30 p api operation.wait "$(jq -nc --arg id "$operation" '{id:$id,timeout:20}')" |
        jq -er '.result.operation.status')
    done
    if test "$status" != completed; then echo "Notes session start ended $status." >&2; exit 1; fi
  fi
fi
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
