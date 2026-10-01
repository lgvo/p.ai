# Notes: a real application in a P session

This sample accepts notes over HTTP and queues word-count work in PostgreSQL.
Its database, web application and worker are separate session-user services,
visible in P's Services view. Each session owns its rows and jobs in
`$HOME/.local/state/p-notes/`; Git carries only source. All HTTP requests run
inside the session at `127.0.0.1:8000`. PostgreSQL listens on a private Unix
socket, with peer authentication and its TCP listener disabled.

## Provision tools and copy the sample

From the host checkout, `just lab-public` supplies the pinned notes runtime
with PostgreSQL, Python/Psycopg and libpq. Both `p-ai/main` and `notes/main`
are seeded in this lab. Open `p tui`, select `notes/main` and press Enter:
its source is committed in `examples/notes`, and its database, web and worker
services are installed, enabled and started on first provisioning. Run
`python3 examples/notes/client.py health` or `add 'my first note'` inside it.
`just lab-notes` and `./dev/demo-vm --notes` are compatibility aliases for this
same public lab and disk.

To practice creating a separate blank project, enter its bootstrap session
and copy the provisioned sample:

```sh
mkdir -p /workspace/examples/notes
cp -R /etc/p-notes-example/. /workspace/examples/notes
chmod -R u+w /workspace/examples/notes
cd /workspace
git config user.name 'Notes Developer'
git config user.email notes@example.invalid
git add examples/notes
git commit -m 'Add notes application'
git push origin HEAD:main
```

For CLI/API navigation, follow the [project creation walkthrough](../../docs/user-guide.md#create-your-first-project-and-commit)
and [new/retained stream examples](../../docs/user-guide.md#create-another-stream),
using the sample commit above in place of the generic first file. The same
owner API operations back the TUI controls.

The first push establishes `main`. Another stream needs committed source;
creation from this branch excludes this session's dirty files, local-only
commits, home, database and job queue. If importing a repository, use a
repository containing the committed `examples/notes` subtree and select an
explicit source for its first session.

The configured runtime contains tools and no application data. It preserves
the ordinary production base image. A committed default devShell selecting
these tools is a separate, still pending offline builder milestone; this
sample does not declare a flake or claim devShell realization.

For a disposable import/publication exercise, the outer notes-lab owner can
run `bash /etc/p-notes-origin-fixture.sh setup`. It creates a localhost SSH
origin containing this sample and prints its URL and exact main OID. Create
an imported project using that URL, then explicitly assign `main` from the
observed source. The fixture refuses an existing owner SSH configuration and
uses its own temporary keys. Publication still requires the documented
[owner API](../../docs/user-guide.md#publish-work-to-the-external-origin).
After the exercise, `bash /etc/p-notes-origin-fixture.sh cleanup` removes only
the unchanged fixture configuration, server and repository. It does not
remove P's imported project or sessions; review those losses separately.

## Install, test and run

Run these commands inside `/workspace` in each new session:

```sh
python3 -c 'import psycopg; print(psycopg.__version__)'
postgres --version
python3 examples/notes/tests.py
python3 examples/notes/install.py
systemctl --user start p-project-notes-db.service
python3 examples/notes/manage.py ready
python3 examples/notes/manage.py migrate
systemctl --user start p-project-notes-web.service p-project-notes-worker.service
python3 examples/notes/client.py health
python3 examples/notes/client.py add 'hello private notes'
python3 examples/notes/client.py list
```

Health prints `{"status": "ready", "database": "ready"}`. The new note initially
has `status: pending`; after the worker runs, listing shows `status: processed`
and `word_count: 3`. Inspect real SQL and each service's logs:

```sh
psql -h "$HOME/.local/state/p-notes/socket" -d notes -c 'SELECT * FROM notes'
psql -h "$HOME/.local/state/p-notes/socket" -d notes -c 'SELECT * FROM jobs'
journalctl --user -u p-project-notes-db.service -n 20 --no-pager
journalctl --user -u p-project-notes-web.service -n 20 --no-pager
journalctl --user -u p-project-notes-worker.service -n 20 --no-pager
```

The installer resolves actual Python/PostgreSQL executables and checks the
selected interpreter's Psycopg import. Override with `--python /path/python3`
and `--postgres-bin /path/bin` when needed. It writes three units and reloads
the user manager. Reinstallation preserves enablement and data; it never
starts or enables services. Optional `systemctl --user enable UNIT` is an
explicit developer choice. Without enablement, repeat the service startup
commands after Session Stop/Start or lab shutdown/relaunch.

Database startup initializes an absent cluster, waits at most 25 seconds for
real SQL, and applies schema version 1 before dependent units start. Repeated
setup keeps rows and jobs. An incompatible PostgreSQL major, incomplete setup,
unknown schema or damaged cluster produces an error and preserves existing
files. Inspect and migrate uncertain data explicitly; setup never erases it.
Setup and HTTP health also check the required version-1 tables, columns and
types. Missing or incompatible objects fail clearly without automatic repair;
additional columns are allowed and retained.
`P_NOTES_STATE=/short/private/path` overrides the data root for direct commands;
`install.py --state /short/private/path` sets the same override in all units.
The socket lives under that root and must fit PostgreSQL's Unix path limit.

## Edit, fail a test, fix and run

In `examples/notes/worker.py`, temporarily replace
`count = len(job["body"].split())` with `count = 0`. Run:

```sh
python3 examples/notes/tests.py NotesTests.test_http_and_word_count
```

The stored result test fails because three words produced zero. Restore the
calculation, rerun the test, then restart the worker in Services or with
`systemctl --user restart p-project-notes-worker.service`. The full test suite
uses its own temporary cluster, SQL schema and HTTP port; it leaves the running
application's data untouched and removes only its temporary fixtures.

## Pause work and recover dependencies

Stop the worker in Services or run:

```sh
systemctl --user stop p-project-notes-worker.service
python3 examples/notes/client.py add 'queued while worker stopped'
python3 examples/notes/client.py list
systemctl --user start p-project-notes-worker.service
```

The web application remains active. The note and queued job commit together;
worker start finishes the pending note. The worker locks a pending row and
updates the note and job in one transaction. An interrupted transaction rolls
back, allowing the same job to resume. Concurrent workers skip locked rows.
This ensures one durable stored result per note, without claiming exactly-once
execution or external effects.

The database's `Requires`, `BindsTo` and `After` relationships stop both web and
worker when the database stops or fails. Recovery explicitly starts dependents:

```sh
systemctl --user stop p-project-notes-db.service
systemctl --user status p-project-notes-web.service p-project-notes-worker.service
systemctl --user start p-project-notes-db.service
python3 examples/notes/manage.py ready
python3 examples/notes/manage.py migrate
systemctl --user start p-project-notes-web.service p-project-notes-worker.service
python3 examples/notes/client.py health
python3 examples/notes/client.py list
```

HTTP connection attempts fail while the web service is stopped. If a web
process is running during an outage, it returns HTTP 503; it never reports
HTTP success before committing. A lost connection during commit can leave the
write outcome uncertain: inspect notes before retrying.

For a controlled mixed service screen, keep database and web active, then fail
the worker:

```sh
systemctl --user kill --signal=KILL --kill-whom=main p-project-notes-worker.service
systemctl --user status p-project-notes-worker.service
journalctl --user -u p-project-notes-worker.service -n 20 --no-pager
systemctl --user reset-failed p-project-notes-worker.service
systemctl --user start p-project-notes-worker.service
```

Services deliberately have no automatic restart loop. A failed worker stays
visible until recovery. A database failure can be reproduced with the same
`kill` command using `--kill-whom=all p-project-notes-db.service`; reset its
failed state, start the database, check readiness, and explicitly start web
and worker. PostgreSQL performs recovery on the retained private cluster.

## Continue in P

Detach with tmux's `Ctrl-b d` and enter again. Detach closes the attachment;
tmux and all three services continue. Stop the session to end its processes;
Start retains its source, local commits, dirty files, installed units and SQL
data. Explicitly start the three units again, then verify pending work resumes.
A daemon restart retains running session processes; a VM shutdown ends them.

Create another session on a new branch from pushed `main`. Install and start
its services with the same commands. It has an empty database and independent
port 8000/socket namespace. A schema change made only in session A, such as
`ALTER TABLE notes ADD COLUMN session_a_label text`, leaves B unchanged.
Commit source changes and push the assigned branch to retain them in P.
Publication to an external origin is an explicit owner action.

Rename preserves session UUID and private data. Discard removes the session's
cluster, notes, jobs and private home while retaining the pushed branch.
Creating a session from that existing retained branch creates a new UUID and
an empty database. Session Delete also removes its assigned P branch. Review
fresh loss information and confirmation before either action; verify sibling
sessions still have their own notes. Whole-project deletion uses the owner API.

## Automated evidence and remaining gates

From the host checkout, `just notes-tests` runs real PostgreSQL tests using the
pinned sample shell. Thirteen tests cover SQL/HTTP results, atomic note/job
insertion, concurrency, transaction interruption, restart persistence, private
socket/TCP settings, repeat setup, incompatible/damaged state, unknown schema,
required schema objects/types, compatible extra columns and installer behavior.

Native selection `57-developer-workflow.sh` uses this exact sample and public
owner APIs for bootstrap, new/existing branches, services/journals, mixed
failure/recovery, CLI attach/detach, edit/fail/fix, private databases/schema,
Stop/Start, daemon restart, rename, Discard/reassignment/Delete and final
project cleanup. It also runs the Python tests inside a fresh confined session.
The [evidence index](../../docs/llm-interactive-review/tui/README.md#authorized-journey-matrix)
maps the native and adaptive TUI journeys separately, including the corrected
13-test attached suite and all three service journals. A separate dedicated
persistent lab passed clean shutdown/relaunch, retained source/private
files/units/SQL data and resumed its pending job after explicit service startup.
See the [validation workflow](../../docs/development-validations.md#three-service-developer-workflow)
for the separate evidence requirements. Existing origin/import/publication/retry
gates (VM17, VM20 and creation recovery gates) test those contracts; they do not
by themselves establish a sample-source origin journey; the evidence index
records the actual sample import and explicit publication instead. Major TUI
selection `58-notes-tui.sh` checks reviewed integration effects after live
navigation. Exact native results, failures, fresh review and the aggregate
checkpoint are recorded in
[implementation progress](../../docs/implementation-progress.md#tui-and-three-service-developer-workflows--2026-09-30).
The committed default-devShell extension remains a separate pending milestone.
