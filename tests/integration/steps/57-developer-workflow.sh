# shellcheck shell=bash
# Real provisioned notes application through the CLI's task commands and
# confined session tools.
set -E
umask 077
step_dir="$P_TEST_TMP/step-57"
mkdir -m 0700 "$step_dir"
socket=/var/lib/p-service55/control.sock
export P_SOCKET=$socket
uuid=
uuid_a=
uuid_b=
uuid_sibling=
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
inc() { timeout 60 incus --force-local --project user-1000 "$@"; }
service_control() { /run/wrappers/bin/sudo -n "$P_TEST_SERVICE_CONTROL_BINARY" "$1"; }
# The task commands wait for the operations they start and repeat a call the
# daemon answers retry only where a second call cannot act twice. A creation
# from a commit with no image first builds the commit's image.
cli() { timeout 420 p "$@"; }
guest() {
 inc exec "p-$uuid" --user 1000 --group 1000 --cwd /workspace \
  --env HOME=/home/p --env GIT_SSH=/usr/libexec/p/git-ssh \
  --env XDG_RUNTIME_DIR=/run/user/1000 \
  --env DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus -- "$@"
}
sql() { guest psql -h /home/p/.local/state/p-notes/socket -d notes -Atc "$1"; }
# An action returns once the session's service manager has queued it; the
# inventory shows when it is done.
action() { cli service "$2" "$uuid" "p-project-notes-$1.service" >/dev/null; }
start_unit() { action "$1" start; wait_unit "$1" running; }
stop_unit() { action "$1" stop; wait_unit "$1" stopped; }
condition() { cli session show "$uuid" --json | jq -e --arg condition "$1" '.session.session_condition==$condition' >/dev/null; }
wait_health() {
 local deadline=$((SECONDS+45))
 while ! p status --json 2>/dev/null | jq -e '.control_state=="ready"' >/dev/null 2>&1; do
  test "$SECONDS" -lt "$deadline"; sleep 0.2
 done
}
wait_ready() {
 local deadline=$((SECONDS+60))
 while ! condition ready; do
  test "$SECONDS" -lt "$deadline"; sleep 0.2
 done
}
# Stop and start wait for their operations; each must change the session.
stop_session() {
 cli session stop "$uuid" --json |
  jq -e '.changed==true and .operation.kind=="session.stop" and .operation.status=="completed"' >/dev/null
 condition stopped
}
start_session() {
 cli session start "$uuid" --json |
  jq -e '.changed==true and .operation.kind=="session.start" and .operation.status=="completed"' >/dev/null
}
# created prints the session a creation's completed operation made.
created() { jq -er '.operation | select(.status=="completed") | .session_uuid'; }
wait_sql() {
 local query=$1 expected=$2 deadline=$((SECONDS+30))
 while test "$(sql "$query")" != "$expected"; do
  test "$SECONDS" -lt "$deadline"; sleep 0.1
 done
}
wait_unit() {
 # The notes database may take its whole 45-second start timeout.
 local unit=$1 expected=$2 deadline=$((SECONDS+60)) observed
 while ((SECONDS<deadline)); do
  observed=$(cli service list "$uuid" --json)
  # An installed unit the user manager unloaded reports stopped.
  if jq -e --arg name "p-project-notes-$unit.service" --arg state "$expected" \
   '.services|any(.name==$name and .state==$state)' <<< "$observed" >/dev/null; then return; fi
  sleep 0.1
 done
 printf '%s\n' "$observed" >&2
 return 1
}
pids() { guest systemctl --user show p-project-notes-{db,web,worker}.service -p MainPID --value; }
main_pid() { guest systemctl --user show "p-project-notes-$1.service" -p MainPID --value; }
# A queued restart is done once the unit runs again under a new main process.
wait_restarted() {
 local unit=$1 old=$2 pid deadline=$((SECONDS+60))
 while ((SECONDS<deadline)); do
  pid=$(main_pid "$unit")
  if test "$pid" != 0 && test "$pid" != "$old"; then wait_unit "$unit" running; return; fi
  sleep 0.1
 done
 printf 'p-project-notes-%s.service kept main PID %s after restart\n' "$unit" "$old" >&2
 return 1
}
seed_sample() {
 guest mkdir -p examples/notes
 python3 - "$P_TEST_SOURCE/examples/notes" "$step_dir/notes-source.tar" <<'PY_ARCHIVE'
import pathlib, sys, tarfile
with tarfile.open(sys.argv[2], "w") as archive:
    archive.add(sys.argv[1], arcname=".", filter=lambda entry:
                None if "__pycache__" in pathlib.Path(entry.name).parts or entry.name.endswith(".pyc") else entry)
PY_ARCHIVE
 inc file push --uid 1000 --gid 1000 --mode 0600 "$step_dir/notes-source.tar" "p-$uuid/tmp/notes57-source.tar"
 guest python3 -c 'import tarfile; tarfile.open("/tmp/notes57-source.tar").extractall("/workspace/examples/notes", filter="data")'
 guest chmod -R u+w examples/notes
 guest rm /tmp/notes57-source.tar
}
setup() {
 guest python3 examples/notes/install.py
 start_unit db
 guest python3 examples/notes/manage.py migrate
 start_unit web
 start_unit worker
 guest python3 examples/notes/client.py health | jq -e '.database=="ready"' >/dev/null
}
# Without a terminal a removal does not ask: --force runs it after the
# countdown its loss starts; a reviewed plan runs by its change tag.
remove_session() {
 local kind=$1 tag
 stop_session
 if test "$kind" = discard; then
  cli session discard "$uuid" --force --json | jq -e '.plan.allowed and (.plan.loss|length>0) and .operation.status=="completed"' >/dev/null
 else
  tag=$(cli session "$kind" "$uuid" --dry-run)
  cli session "$kind" "$uuid" --if-match "$tag" --json | jq -e '.operation.status=="completed"' >/dev/null
 fi
 inc list "^p-$uuid$" --format json | jq -e 'length==0' >/dev/null
}
cleanup() {
 service_control stop || true
 for owned in "$uuid_a" "$uuid_b" "$uuid_sibling"; do
  if test -n "$owned"; then inc delete --force "p-$owned" >/dev/null 2>&1 || true; fi
 done
}
on_error() {
 printf 'P_NOTES_WORKFLOW_FAIL line=%s status=%s\n' "$2" "$1" >&2
 service_control diagnostic >&2 || true
 if test -n "$uuid"; then
  p session show "$uuid" --json >&2 || true
  p service list "$uuid" --json >&2 || true
  guest systemctl --user --no-pager status p-project-notes-{db,web,worker}.service >&2 || true
  guest journalctl --user --no-pager -n 20 -u p-project-notes-db -u p-project-notes-web -u p-project-notes-worker >&2 || true
 fi
}
trap cleanup EXIT
trap 'on_error "$?" "$LINENO"' ERR
service_control start
wait_health
uuid=$(cli project create notes57 --json | created)
uuid_a=$uuid
wait_ready
# No installed project units is a successful empty inventory. systemctl's
# list-unit-files command may return exit 1 with [], rather than exit 0.
cli service list "$uuid" --json | jq -e '(.services // [])==[]' >/dev/null
if empty_units=$(guest /usr/libexec/p/systemctl --user list-unit-files --type=service --no-pager --output=json 'p-project-*.service'); then
 empty_status=0
else empty_status=$?; fi
test "$empty_status" -eq 0 || test "$empty_status" -eq 1
jq -e '.==[]' <<< "$empty_units" >/dev/null
printf 'P_NOTES_EMPTY_SERVICES_PASS native-exit=%s\n' "$empty_status"
# An unborn bootstrap has no committed source for another stream.
if refused=$(cli session create notes57:too-soon --from main --json); then
 echo 'accepted another stream before the first commit' >&2; exit 1
fi
jq -e 'has("error") and (has("operation")|not)' <<< "$refused" >/dev/null

seed_sample
guest git config user.name P
guest git config user.email p@example.invalid
guest git add examples/notes
guest git commit -qm 'Add real PostgreSQL notes sample'
guest git push origin HEAD:main
sample_oid=$(guest git rev-parse HEAD)
printf 'P_NOTES_TOOLS image=%s\n' "$(cat "$P_TEST_RUNTIME_IMAGE_FILE")"
guest postgres --version
guest python3 -c 'import sys, psycopg; print(sys.version); print("Psycopg", psycopg.__version__)'
guest python3 examples/notes/tests.py
setup
cli service list "$uuid" --json | jq -e '.services|[.[]|select(.name|startswith("p-project-notes-"))]|length==3' >/dev/null
# Session-local socket has SQL access and no TCP listener.
test "$(sql 'SHOW listen_addresses')" = ''
inc list "^p-$uuid$" --format json | jq -e 'length==1 and ([.[0].expanded_devices[]|select(.type=="nic")]|length)==0' >/dev/null

# Mix active web/database and inactive worker, then resume a durable job.
stop_unit worker
guest python3 examples/notes/client.py add 'pending before worker resume' | jq -e '.status=="pending"' >/dev/null
test "$(sql 'SELECT count(*) FROM jobs WHERE completed_at IS NULL')" = 1
start_unit worker
wait_sql 'SELECT word_count FROM notes WHERE id=1' 4
# Real unit-specific logs via the public interface.
for unit in db web worker; do
 cli service logs "$uuid" "p-project-notes-$unit.service" --json | jq -e '.logs|length>0' >/dev/null
done
cli service logs "$uuid" p-project-notes-worker.service --json | jq -e '.logs|contains("processed job=")' >/dev/null
# Kill the actual worker service and inspect its failed state alongside active web.
guest systemctl --user kill --signal=KILL --kill-whom=main p-project-notes-worker.service
wait_unit worker failed
wait_unit web running
guest systemctl --user reset-failed p-project-notes-worker.service
start_unit worker
worker_pid=$(main_pid worker)
action worker restart
wait_restarted worker "$worker_pid"

# Interrupted open transaction leaves the pending job claimable on restart.
stop_unit worker
guest python3 examples/notes/client.py add 'interrupted transaction recovers' >/dev/null
guest bash -euc 'python3 examples/notes/worker.py --once --hold-before-commit 30 > /tmp/notes57-worker.log 2>&1 & echo $! > /tmp/notes57-worker.pid'
deadline=$((SECONDS+20))
while ! guest python3 -c 'from pathlib import Path; import sys; sys.exit(0 if "transaction open" in Path("/tmp/notes57-worker.log").read_text() else 1)'; do test "$SECONDS" -lt "$deadline"; sleep 0.1; done
# shellcheck disable=SC2016 # The private worker PID is read inside the guest.
guest bash -euc 'kill -KILL "$(cat /tmp/notes57-worker.pid)"'
start_unit worker
wait_sql 'SELECT count(*) FROM jobs WHERE completed_at IS NULL' 0
test "$(sql "SELECT count(*) FROM notes WHERE status='processed'")" = 2

# Dependency stop and failure both stop web/worker. Explicit recovery retains rows.
stop_unit db
wait_unit web stopped
wait_unit worker stopped
if guest python3 examples/notes/client.py add 'cannot be accepted'; then exit 1; fi
start_unit db
guest python3 examples/notes/manage.py ready
start_unit web
start_unit worker
test "$(sql 'SELECT count(*) FROM notes')" = 2
guest systemctl --user kill --signal=KILL --kill-whom=all p-project-notes-db.service
wait_unit db failed
wait_unit web stopped
wait_unit worker stopped
guest systemctl --user reset-failed p-project-notes-db.service
setup
test "$(sql 'SELECT count(*) FROM notes')" = 2

# CLI detach/re-entry ends only the temporary lease; all four processes persist.
before_pids=$(pids)
before_tmux=$(guest tmux -S /run/p-interactive/tmux.sock display-message -p '#{pid}')
python3 "$P_TEST_SOURCE/tests/integration/notes-attach.py" "$socket" "$uuid"
test "$(guest cat notes-attachment57)" = cli-attachment
test "$(guest sed -n 1p notes-user-bus57)" = /run/user/1000
test "$(guest sed -n 2p notes-user-bus57)" = unix:path=/run/user/1000/bus
test "$(guest cat notes-shell-install57.status)" = 0
test "$(guest cat notes-shell-services57.status)" = 0
python3 "$P_TEST_SOURCE/tests/integration/notes-attach.py" "$socket" "$uuid"
test "$(pids)" = "$before_pids"
test "$(guest tmux -S /run/p-interactive/tmux.sock display-message -p '#{pid}')" = "$before_tmux"

# Edit/fail/fix: a wrong word-count implementation fails the real HTTP/SQL test.
stop_unit worker
guest cp examples/notes/worker.py /tmp/notes57-worker-source
# Replace only the calculation line; tests cover the externally stored result.
guest python3 -c 'from pathlib import Path; p=Path("examples/notes/worker.py"); p.write_text(p.read_text().replace("len(job[\"body\"].split())", "0"))'
if guest python3 examples/notes/tests.py NotesTests.test_http_and_word_count > "$step_dir/expected-failing-test.log" 2>&1; then
 echo 'incorrect word count unexpectedly passed' >&2; exit 1
fi
guest cp /tmp/notes57-worker-source examples/notes/worker.py
guest python3 examples/notes/tests.py NotesTests.test_http_and_word_count
start_unit worker

# Exact committed source excludes the source session's private home/unpushed work.
guest bash -euc 'printf private-A > /home/p/notes-private; printf dirty-A > notes-dirty; printf local-only > local-only; git add local-only; git commit -qm local-only'
local_oid=$(guest git rev-parse HEAD)
uuid_b=$(cli session create notes57:feature-notes --from main --json | created)
uuid=$uuid_b
wait_ready
test "$(guest git rev-parse HEAD)" = "$sample_oid"
guest test ! -e local-only
guest test ! -e notes-dirty
guest test ! -e /home/p/notes-private
guest test ! -e /home/p/.local/state/p-notes/cluster
setup
test "$(sql 'SELECT count(*) FROM notes')" = 0
guest python3 examples/notes/client.py add 'only B' >/dev/null
wait_sql 'SELECT count(*) FROM jobs WHERE completed_at IS NULL' 0
# Same addresses work in two private runtime namespaces; schema is independent.
uuid=$uuid_a
sql 'ALTER TABLE notes ADD COLUMN session_a_label text'
test "$(sql 'SELECT count(*) FROM notes')" = 2
uuid=$uuid_b
test "$(sql 'SELECT count(*) FROM notes')" = 1
test "$(sql "SELECT count(*) FROM information_schema.columns WHERE table_name='notes' AND column_name='session_a_label'")" = 0
if refused=$(cli session create notes57:feature-notes --json); then exit 1; fi
jq -e 'has("error")' <<< "$refused" >/dev/null

# Stop/start preserves local/dirty/private/database/unit state but ends old processes.
uuid=$uuid_a
stop_unit worker
guest python3 examples/notes/client.py add 'pending across stop start' >/dev/null
stop_session
inc list "^p-$uuid$" --format json | jq -e 'length==1 and .[0].status=="Stopped"' >/dev/null
service_control restart
wait_health
condition stopped
start_session
wait_ready
test "$(guest git rev-parse HEAD)" = "$local_oid"
test "$(guest cat notes-dirty)" = dirty-A
test "$(guest cat /home/p/notes-private)" = private-A
wait_unit db stopped
setup
wait_sql 'SELECT count(*) FROM jobs WHERE completed_at IS NULL' 0
test "$(sql 'SELECT count(*) FROM notes')" = 3
# Daemon restart preserves existing runtime/process/principal/source identities.
before_pids=$(pids)
before_tmux=$(guest tmux -S /run/p-interactive/tmux.sock display-message -p '#{pid}')
key_digest=$(sha256sum "/var/lib/p-service55/session_keys/$uuid" | cut -d ' ' -f1)
service_control restart
wait_health
wait_ready
test "$(pids)" = "$before_pids"
test "$(guest tmux -S /run/p-interactive/tmux.sock display-message -p '#{pid}')" = "$before_tmux"
test "$(sha256sum "/var/lib/p-service55/session_keys/$uuid" | cut -d ' ' -f1)" = "$key_digest"
test "$(guest git rev-parse HEAD)" = "$local_oid"

# Rename needs the session stopped; it retains UUID/data and adjusts the
# ordinary branch/upstream. The command expects the branch's tip in P.
uuid=$uuid_b
cli branch list notes57 --json | jq -e --arg oid "$sample_oid" 'any(.ref=="refs/heads/feature-notes" and .oid==$oid)' >/dev/null
if refused=$(cli session rename "$uuid" retained-notes --json); then
 echo 'renamed a running session' >&2; exit 1
fi
jq -e '.error.message=="conflict" and .error.data.reason=="session_not_stopped" and (has("operation")|not)' <<< "$refused" >/dev/null
stop_session
cli session rename "$uuid" retained-notes --json | jq -e '.operation.status=="completed"' >/dev/null
start_session
wait_ready
test "$(guest git branch --show-current)" = retained-notes
test "$(guest git config branch.retained-notes.merge)" = refs/heads/retained-notes
wait_unit db stopped
setup
test "$(sql 'SELECT count(*) FROM notes')" = 1

# Discard keeps the pushed source, then existing-branch assignment has fresh state.
old_b=$uuid_b
remove_session discard
cli branch list notes57 --retained --json | jq -e --arg oid "$sample_oid" \
 'any(.branch=="retained-notes" and .oid==$oid)' >/dev/null
uuid_b=$(cli session create notes57:retained-notes --json | created)
uuid=$uuid_b
wait_ready
test "$uuid_b" != "$old_b"
test "$(guest git rev-parse HEAD)" = "$sample_oid"
guest test ! -e /home/p/.local/state/p-notes/cluster
setup
test "$(sql 'SELECT count(*) FROM notes')" = 0
test "$(sql 'SELECT count(*) FROM jobs')" = 0
remove_session delete
cli branch list notes57 --json | jq -e 'all(.ref!="refs/heads/retained-notes")' >/dev/null
uuid_b=
# A sibling project/database must survive the reviewed aggregate deletion.
uuid_sibling=$(cli project create notes57-sibling --json | created)
uuid=$uuid_sibling
wait_ready
seed_sample
guest git add examples/notes
guest git -c user.name=P -c user.email=p@example.invalid commit -qm sibling-sample
guest git push origin HEAD:main
setup
guest python3 examples/notes/client.py add 'sibling survives' >/dev/null
wait_sql 'SELECT count(*) FROM jobs WHERE completed_at IS NULL' 0
uuid=$uuid_a
test "$(sql 'SELECT count(*) FROM notes')" = 3
stop_session
tag=$(cli project delete notes57 --dry-run --json | jq -er 'select(.plan.allowed==true) | .change_tag')
cli project delete notes57 --if-match "$tag" --json | jq -e '.operation.status=="completed"' >/dev/null
inc list "^p-$uuid$" --format json | jq -e 'length==0' >/dev/null
uuid_a=
uuid=$uuid_sibling
test "$(sql 'SELECT body FROM notes')" = 'sibling survives'
guest python3 examples/notes/client.py health | jq -e '.database=="ready"' >/dev/null
stop_session
cli project delete notes57-sibling --force --json | jq -e '.operation.status=="completed"' >/dev/null
inc list "^p-$uuid$" --format json | jq -e 'length==0' >/dev/null
uuid_sibling=
printf 'P_NOTES_WORKFLOW_PASS SQL HTTP workers readiness recovery branches isolation attach StopStart daemon-restart rename discard reassignment delete\n'
