# shellcheck shell=bash
# Real provisioned notes application through owner API and confined session tools.
set -E
umask 077
step_dir="$P_TEST_TMP/step-57"
mkdir -m 0700 "$step_dir"
socket=/var/lib/p-service55/control.sock
uuid=
uuid_a=
uuid_b=
uuid_sibling=
op=
export INCUS_SOCKET=/var/lib/incus/unix.socket.user
inc() { timeout 60 incus --force-local --project user-1000 "$@"; }
service_control() { /run/wrappers/bin/sudo -n "$P_TEST_SERVICE_CONTROL_BINARY" "$1"; }
rpc() {
 local response status deadline=$((SECONDS+40)) read_only=false
 case "$1" in system.health|session.inspect|session.services|session.service.journal|operation.inspect) read_only=true ;; esac
 while true; do
  if response=$(timeout 150 p api "$socket" "$@"); then
   printf '%s\n' "$response"; return
  else status=$?; fi
  # Start/reconciliation watchers briefly retain lifecycle authority after
  # readiness is observed. Retry only explicit busy observations, never hide
  # unavailable/invalid responses or redispatch mutations here.
  if test "$read_only" = true && test "$SECONDS" -lt "$deadline" && \
   jq -e '.error.kind=="busy"' <<< "$response" >/dev/null 2>&1; then sleep 0.1; continue; fi
  if test "$read_only" = true; then
   printf 'P_NOTES_RPC_FAILURE method=%s status=%s\n' "$1" "$status" >&2
   jq -c ' .error' <<< "$response" >&2 || true
  fi
  printf '%s\n' "$response"
  return "${status:-1}"
 done
}
params() { jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid}'; }
guest() {
 inc exec "p-$uuid" --user 1000 --group 1000 --cwd /workspace \
  --env HOME=/home/p --env GIT_SSH=/usr/libexec/p/git-ssh \
  --env XDG_RUNTIME_DIR=/run/user/1000 \
  --env DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus -- "$@"
}
sql() { guest psql -h /home/p/.local/state/p-notes/socket -d notes -Atc "$1"; }
action() {
 rpc session.service.action "$(jq -nc --arg uuid "$uuid" --arg unit "p-project-notes-$1.service" --arg action "$2" \
  '{v:1,uuid:$uuid,unit:$unit,action:$action}')" >/dev/null
}
wait_health() {
 local deadline=$((SECONDS+45))
 while ! rpc system.health | jq -e '.result.control_state=="ready"' >/dev/null 2>&1; do
  test "$SECONDS" -lt "$deadline"; sleep 0.2
 done
}
wait_completed() {
 local id=$1 result status deadline=$((SECONDS+160))
 while ((SECONDS<deadline)); do
  result=$(rpc operation.inspect "$(jq -nc --arg id "$id" '{v:1,id:$id}')")
  status=$(jq -er '.result.operation.status' <<< "$result")
  if test "$status" = completed; then return; fi
  if test "$status" = failed || test "$status" = blocked; then printf '%s\n' "$result" >&2; return 1; fi
  sleep 0.2
 done
 return 1
}
wait_ready() {
 local deadline=$((SECONDS+60))
 while ! rpc session.inspect "$(params)" | jq -e '.result.session.session_condition=="ready"' >/dev/null; do
  test "$SECONDS" -lt "$deadline"; sleep 0.2
 done
}
stop_session() {
 local deadline=$((SECONDS+45)) response
 while ! response=$(rpc session.stop "$(params)"); do
  jq -e '.error.kind=="busy"' <<< "$response" >/dev/null
  test "$SECONDS" -lt "$deadline"; sleep 0.2
 done
}
wait_sql() {
 local query=$1 expected=$2 deadline=$((SECONDS+30))
 while test "$(sql "$query")" != "$expected"; do
  test "$SECONDS" -lt "$deadline"; sleep 0.1
 done
}
wait_unit() {
 local unit=$1 expected=$2 deadline=$((SECONDS+30)) observed native
 while ((SECONDS<deadline)); do
  observed=$(rpc session.services "$(params)")
  if jq -e --arg unit "p-project-notes-$unit.service" --arg state "$expected" \
   '.result.services|any(.unit==$unit and .active_state==$state)' <<< "$observed" >/dev/null; then return; fi
  # Inactive units can be garbage-collected by the user manager. The API
  # honestly exposes installed-but-not-loaded units as unknown/not-loaded.
  # Native observation establishes inactivity without inventing an API state.
  if test "$expected" = inactive && jq -e --arg unit "p-project-notes-$unit.service" \
   '.result.services|any(.unit==$unit and .active_state=="unknown" and .sub_state=="not-loaded")' <<< "$observed" >/dev/null; then
   native=$(guest systemctl --user show "p-project-notes-$unit.service" -p ActiveState --value)
   if test "$native" = inactive; then
    printf 'P_NOTES_SERVICE_OBSERVED unit=%s API=unknown/not-loaded native=inactive\n' "$unit"
    return
   fi
  fi
  sleep 0.1
 done
 printf '%s\n' "$observed" >&2
 return 1
}
pids() { guest systemctl --user show p-project-notes-{db,web,worker}.service -p MainPID --value; }
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
 action db start
 guest python3 examples/notes/manage.py migrate
 action web start
 action worker start
 guest python3 examples/notes/client.py health | jq -e '.database=="ready"' >/dev/null
}
loss() {
 local key=$1 response
 response=$(rpc workspace.loss.inspect "$(jq -nc --arg uuid "$uuid" --arg key "$key" '{v:1,uuid:$uuid,key:$key}')")
 loss_op=$(jq -er '.result.operation.id' <<< "$response")
 wait_completed "$loss_op"
}
remove_session() {
 local kind=$1 key=$2 response token
 stop_session
 loss "$key-loss"
 response=$(rpc session.removal.preview "$(jq -nc --arg uuid "$uuid" --arg kind "$kind" --arg loss "$loss_op" \
  '{v:1,uuid:$uuid,kind:$kind,loss_operation_id:$loss}')")
 token=$(jq -er '.result.preview.confirmation_token' <<< "$response")
 response=$(rpc "session.$kind" "$(jq -nc --arg uuid "$uuid" --arg key "$key" --arg token "$token" \
  '{v:1,uuid:$uuid,key:$key,confirmation_token:$token}')")
 op=$(jq -er '.result.operation.id' <<< "$response")
 wait_completed "$op"
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
  rpc session.inspect "$(params)" >&2 || true
  rpc session.services "$(params)" >&2 || true
  guest systemctl --user --no-pager status p-project-notes-{db,web,worker}.service >&2 || true
  guest journalctl --user --no-pager -n 20 -u p-project-notes-db -u p-project-notes-web -u p-project-notes-worker >&2 || true
 fi
}
trap cleanup EXIT
trap 'on_error "$?" "$LINENO"' ERR
service_control start
wait_health
created=$(rpc project.create '{"v":1,"key":"notes57-bootstrap","project":"notes57"}')
uuid=$(jq -er '.result.operation.session_uuid' <<< "$created")
uuid_a=$uuid
op=$(jq -er '.result.operation.id' <<< "$created")
wait_completed "$op"
wait_ready
# No installed project units is a successful empty inventory. systemctl's
# list-unit-files command may return exit 1 with [], rather than exit 0.
rpc session.services "$(params)" | jq -e '(.result.services // [])==[]' >/dev/null
if empty_units=$(guest /usr/libexec/p/systemctl --user list-unit-files --type=service --no-pager --output=json 'p-project-*.service'); then
 empty_status=0
else empty_status=$?; fi
test "$empty_status" -eq 0 || test "$empty_status" -eq 1
jq -e '.==[]' <<< "$empty_units" >/dev/null
printf 'P_NOTES_EMPTY_SERVICES_PASS native-exit=%s\n' "$empty_status"
# An unborn bootstrap has no committed source for another stream.
if refused=$(rpc session.create '{"v":1,"key":"notes57-unborn","project":"notes57","branch":"too-soon","choice":"new","source":"refs/heads/main"}'); then
 echo 'accepted another stream before the first commit' >&2; exit 1
fi
jq -e 'has("error") and (has("result")|not)' <<< "$refused" >/dev/null

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
rpc session.services "$(params)" | jq -e '.result.services|[.[]|select(.unit|startswith("p-project-notes-"))]|length==3' >/dev/null
# Session-local socket has SQL access and no TCP listener.
test "$(sql 'SHOW listen_addresses')" = ''
inc list "^p-$uuid$" --format json | jq -e 'length==1 and ([.[0].expanded_devices[]|select(.type=="nic")]|length)==0' >/dev/null

# Mix active web/database and inactive worker, then resume a durable job.
action worker stop
wait_unit worker inactive
guest python3 examples/notes/client.py add 'pending before worker resume' | jq -e '.status=="pending"' >/dev/null
test "$(sql 'SELECT count(*) FROM jobs WHERE completed_at IS NULL')" = 1
action worker start
wait_sql 'SELECT word_count FROM notes WHERE id=1' 4
# Real unit-specific journals via the public interface.
for unit in db web worker; do
 rpc session.service.journal "$(jq -nc --arg uuid "$uuid" --arg unit "p-project-notes-$unit.service" '{v:1,uuid:$uuid,unit:$unit}')" \
  | jq -e '.result.journal|length>0' >/dev/null
done
rpc session.service.journal "$(jq -nc --arg uuid "$uuid" '{v:1,uuid:$uuid,unit:"p-project-notes-worker.service"}')" \
 | jq -e '.result.journal|contains("processed job=")' >/dev/null
# Kill the actual worker service and inspect its failed state alongside active web.
guest systemctl --user kill --signal=KILL --kill-whom=main p-project-notes-worker.service
wait_unit worker failed
wait_unit web active
guest systemctl --user reset-failed p-project-notes-worker.service
action worker start
worker_pid=$(guest systemctl --user show p-project-notes-worker.service -p MainPID --value)
action worker restart
test "$(guest systemctl --user show p-project-notes-worker.service -p MainPID --value)" != "$worker_pid"

# Interrupted open transaction leaves the pending job claimable on restart.
action worker stop
guest python3 examples/notes/client.py add 'interrupted transaction recovers' >/dev/null
guest bash -euc 'python3 examples/notes/worker.py --once --hold-before-commit 30 > /tmp/notes57-worker.log 2>&1 & echo $! > /tmp/notes57-worker.pid'
deadline=$((SECONDS+20))
while ! guest python3 -c 'from pathlib import Path; import sys; sys.exit(0 if "transaction open" in Path("/tmp/notes57-worker.log").read_text() else 1)'; do test "$SECONDS" -lt "$deadline"; sleep 0.1; done
# shellcheck disable=SC2016 # The private worker PID is read inside the guest.
guest bash -euc 'kill -KILL "$(cat /tmp/notes57-worker.pid)"'
action worker start
wait_sql 'SELECT count(*) FROM jobs WHERE completed_at IS NULL' 0
test "$(sql "SELECT count(*) FROM notes WHERE status='processed'")" = 2

# Dependency stop and failure both stop web/worker. Explicit recovery retains rows.
action db stop
wait_unit web inactive
wait_unit worker inactive
if guest python3 examples/notes/client.py add 'cannot be accepted'; then exit 1; fi
action db start
guest python3 examples/notes/manage.py ready
action web start
action worker start
test "$(sql 'SELECT count(*) FROM notes')" = 2
guest systemctl --user kill --signal=KILL --kill-whom=all p-project-notes-db.service
wait_unit db failed
wait_unit web inactive
wait_unit worker inactive
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
action worker stop
guest cp examples/notes/worker.py /tmp/notes57-worker-source
# Replace only the calculation line; tests cover the externally stored result.
guest python3 -c 'from pathlib import Path; p=Path("examples/notes/worker.py"); p.write_text(p.read_text().replace("len(job[\"body\"].split())", "0"))'
if guest python3 examples/notes/tests.py NotesTests.test_http_and_word_count > "$step_dir/expected-failing-test.log" 2>&1; then
 echo 'incorrect word count unexpectedly passed' >&2; exit 1
fi
guest cp /tmp/notes57-worker-source examples/notes/worker.py
guest python3 examples/notes/tests.py NotesTests.test_http_and_word_count
action worker start

# Exact committed source excludes the source session's private home/unpushed work.
guest bash -euc 'printf private-A > /home/p/notes-private; printf dirty-A > notes-dirty; printf local-only > local-only; git add local-only; git commit -qm local-only'
local_oid=$(guest git rev-parse HEAD)
created=$(rpc session.create '{"v":1,"key":"notes57-feature","project":"notes57","branch":"feature-notes","choice":"new","source":"refs/heads/main"}')
uuid_b=$(jq -er '.result.operation.session_uuid' <<< "$created")
op=$(jq -er '.result.operation.id' <<< "$created")
wait_completed "$op"
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
if refused=$(rpc session.create '{"v":1,"key":"notes57-duplicate-branch","project":"notes57","branch":"feature-notes","choice":"existing"}'); then exit 1; fi
jq -e 'has("error")' <<< "$refused" >/dev/null

# Stop/start preserves local/dirty/private/database/unit state but ends old processes.
uuid=$uuid_a
action worker stop
guest python3 examples/notes/client.py add 'pending across stop start' >/dev/null
stop_session
inc list "^p-$uuid$" --format json | jq -e 'length==1 and .[0].status=="Stopped"' >/dev/null
service_control restart
wait_health
rpc session.inspect "$(params)" | jq -e '.result.session.session_condition=="stopped"' >/dev/null
rpc session.start "$(params)" >/dev/null
wait_ready
test "$(guest git rev-parse HEAD)" = "$local_oid"
test "$(guest cat notes-dirty)" = dirty-A
test "$(guest cat /home/p/notes-private)" = private-A
wait_unit db inactive
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

# Rename retains UUID/data and adjusts the ordinary branch/upstream.
uuid=$uuid_b
renamed=$(rpc session.rename "$(jq -nc --arg uuid "$uuid" --arg oid "$sample_oid" '{v:1,key:"notes57-rename",uuid:$uuid,new_branch:"retained-notes",expected_old_tip:$oid}')")
op=$(jq -er '.result.operation.id' <<< "$renamed")
wait_completed "$op"
test "$(guest git branch --show-current)" = retained-notes
test "$(guest git config branch.retained-notes.merge)" = refs/heads/retained-notes
test "$(sql 'SELECT count(*) FROM notes')" = 1

# Discard keeps the pushed source, then existing-branch assignment has fresh state.
old_b=$uuid_b
remove_session discard notes57-discard
rpc project.retained_branches '{"v":1,"project":"notes57","limit":8}' | jq -e --arg oid "$sample_oid" \
 '.result.branches|any(.branch=="retained-notes" and .oid==$oid)' >/dev/null
created=$(rpc session.create '{"v":1,"key":"notes57-existing","project":"notes57","branch":"retained-notes","choice":"existing"}')
uuid_b=$(jq -er '.result.operation.session_uuid' <<< "$created")
op=$(jq -er '.result.operation.id' <<< "$created")
wait_completed "$op"
uuid=$uuid_b
wait_ready
test "$uuid_b" != "$old_b"
test "$(guest git rev-parse HEAD)" = "$sample_oid"
guest test ! -e /home/p/.local/state/p-notes/cluster
setup
test "$(sql 'SELECT count(*) FROM notes')" = 0
test "$(sql 'SELECT count(*) FROM jobs')" = 0
remove_session delete notes57-delete
rpc project.branches '{"v":1,"project":"notes57","limit":8}' | jq -e '.result.refs|all(.ref!="refs/heads/retained-notes")' >/dev/null
uuid_b=
# A sibling project/database must survive the reviewed aggregate deletion.
created=$(rpc project.create '{"v":1,"key":"notes57-sibling-project","project":"notes57-sibling"}')
uuid_sibling=$(jq -er '.result.operation.session_uuid' <<< "$created")
op=$(jq -er '.result.operation.id' <<< "$created")
wait_completed "$op"
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
loss notes57-final-loss
review=$(rpc project.delete.preview "$(jq -nc --arg uuid "$uuid" --arg loss "$loss_op" '{v:1,project:"notes57",loss_operations:{($uuid):$loss}}')")
token=$(jq -er '.result.preview.confirmation_token' <<< "$review")
removed=$(rpc project.delete.confirm "$(jq -nc --arg token "$token" '{v:1,key:"notes57-project-delete",project:"notes57",confirmation_token:$token}')")
op=$(jq -er '.result.operation.id' <<< "$removed")
wait_completed "$op"
inc list "^p-$uuid$" --format json | jq -e 'length==0' >/dev/null
uuid_a=
uuid=$uuid_sibling
test "$(sql 'SELECT body FROM notes')" = 'sibling survives'
guest python3 examples/notes/client.py health | jq -e '.database=="ready"' >/dev/null
stop_session
loss notes57-sibling-final-loss
review=$(rpc project.delete.preview "$(jq -nc --arg uuid "$uuid" --arg loss "$loss_op" '{v:1,project:"notes57-sibling",loss_operations:{($uuid):$loss}}')")
token=$(jq -er '.result.preview.confirmation_token' <<< "$review")
removed=$(rpc project.delete.confirm "$(jq -nc --arg token "$token" '{v:1,key:"notes57-sibling-project-delete",project:"notes57-sibling",confirmation_token:$token}')")
op=$(jq -er '.result.operation.id' <<< "$removed")
wait_completed "$op"
inc list "^p-$uuid$" --format json | jq -e 'length==0' >/dev/null
uuid_sibling=
printf 'P_NOTES_WORKFLOW_PASS SQL HTTP workers readiness recovery branches isolation attach StopStart daemon-restart rename discard reassignment delete\n'
