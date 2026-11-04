# Waiting for changes

Status: proposed, 2026-11-02. Part of the [interface rework plan](REWORK-PLAN.md).

## Summary

**Today** a client learns that a change finished in three different ways:

- Most changes record a durable operation and return it at once; the client
  polls `operation.inspect`.
- `session.start` returns the session as `starting` and leaves a background
  watcher holding the session's lock; the client polls `session.inspect`.
  `session.stop` stops the runtime inside the request.
- `project.delete.confirm` waits inside the request for fresh workspace loss
  inspections, and the server cuts every request at 30 seconds.

A client that acts as soon as a change looks finished can still meet the
session's lock: VM scenario 34 failed this way after Start, which groundwork
step 0b fixes narrowly, and session creation shows its session established
while its worker still holds the lock. Operation kinds also differ in the
progress events they emit, in whether blocked work resumes after a restart,
and in the order recovery lists them.

**Proposed:**

- Every change returns an operation, including start and stop. Service
  actions stay the exception: they return once queued
  ([services-plugin.md](services-plugin.md#guarantees)).
- `operation.wait {id, timeout}` returns the operation as soon as it is no
  longer running, or when the timeout passes, inside the request deadline.
- An operation is never seen finished while its worker still holds the
  session: the worker's hold ends before the journal records the end.
- One journal write path emits the same progress events for every kind and
  wakes waiters. At restart the daemon resumes running operations, and a
  blocked operation waits for Retry. Unfinished operations list in the order
  they were accepted.

**Decided by the maintainer on 2026-11-02:** a session keeps only its newest
finished start or stop operation; `origin.change` and `origin.publish` become
operations in step 4; the session condition gains `stopping`.

**What users notice:** in the TUI, a started or created session is attached
as soon as it is ready instead of at the next 3-second refresh. A session
being stopped reads `stopping`. A blocked project creation, session creation,
creation cleanup or project deletion no longer retries once by itself when
the daemon restarts; the user presses Retry, as for every other kind already.

## Before and after

| Change | Today returns | After |
|---|---|---|
| `project.create`, `session.create`, `session.rename`, `project.retained.rename`, `operation.retry`, each confirm (discard, delete, create replacement, create cleanup, retained-branch deletion) | An operation | Unchanged |
| `session.start` | The session as `starting`; a watcher holds the lock until the host is ready | An operation of kind `session.start`, or `changed: false` when there is nothing to start |
| `session.stop` | The session, after stopping the runtime inside the request | An operation of kind `session.stop`, or `changed: false` when there is nothing to stop |
| `project.delete.confirm` (`project.delete` after step 5) | An operation, after fresh loss inspections inside the request | The operation, at once; groundwork step 0e removes the inspections |
| `origin.change`, `origin.publish` | The origin state or publication result, after contacting or pushing to the origin inside the request | Operations ([below](#origin-change-and-publish-as-operations)) |
| `session.attach` | A pending attachment, after starting the session and waiting for it inside the request | A pending attachment for a ready session; it no longer starts one |
| `session.service.action` | The result after systemd's job finished | Returns once queued, not an operation |
| `origin.refresh` | The refreshed observation | Unchanged: a read that refreshes P's cached view, not a change |

Waiting for a start, before:

```text
→ session.start   {"v":1,"uuid":"4f1c…"}
← {"v":1,"session":{…,"session_condition":"starting"}}
→ session.inspect {"v":1,"uuid":"4f1c…"}   repeated until "ready"
```

After:

```text
→ session.start  {"uuid":"4f1c…"}
← {"changed":true,"operation":{"id":"9b0e…","kind":"session.start","status":"running",…}}
→ operation.wait {"id":"9b0e…","timeout":20}
← {"operation":{"id":"9b0e…","kind":"session.start","status":"completed",…}}
```

Starting it again once it is ready records nothing:

```text
→ session.start  {"uuid":"4f1c…"}
← {"changed":false,"session":{…,"session_condition":"ready"}}
```

## `operation.wait`

| | |
|---|---|
| Params | `id`, the operation; `timeout`, whole seconds from 1 to 25 |
| Result | `operation`, as `operation.inspect` returns it |
| Returns | As soon as the status is not `running`: `completed`, `failed`, `superseded` or `blocked`. Otherwise when the timeout passes, with the status still `running`. |
| Errors | `not_found` for an unknown ID; `invalid` for a timeout outside 1 to 25 ([errors.md](errors.md)) |

The server keeps its 30-second request deadline (`ServeConn` in
internal/control/rpc.go), and 25 seconds leaves time to answer before it. A
wait returns on `blocked` because a blocked operation needs a person or a
Retry, not more time. `rpc.cancel` ends a wait early.

**A client's own budget must cover the whole call:** connecting, the hello,
the timeout and the reply. The TUI gives every call 25 seconds from before it
connects (`callContext` in internal/tui/client.go, which `api.Call` applies
to the socket), so the TUI waits with a timeout of 20. `p api` sets its
deadline to the timeout plus 5 seconds instead of its 10-second default.

**A wait that returns `running` is a result, not an error.** The client
waits again. In the TUI, a call error clears the intent to attach once the
operation completes (`attachOnComplete`, internal/tui/model.go); a wait that
returns `running` keeps it.

The daemon does not poll SQLite for a wait: the journal write path below
wakes the waiters of an operation after each committed status change.
Waiters live in the daemon's memory; after a restart the client waits again.

`operation.inspect` stays: it answers at once, which is what lists and the
TUI's operations page need.

## An operation ends only after its worker lets go

**The rule: a worker that holds a session releases it before the journal
records its operation's end.** Work that would compete with an unfinished
operation on the same session goes through one admission check of the
journal: it answers `retry` naming the operation while it is `running`, and
`conflict` with reason `operation_blocked` once it is `blocked`, since a
blocked operation waits for Retry ([errors.md](errors.md)). So between the
release and the recorded end, competing work is held back by the journal, not
by the lock: P's store has one SQLite connection, and the transaction that
records the end holds it, so the admission check waits for that commit and
then admits the work. Once `operation.wait` returns the end, nothing of that
operation holds the session.

The check refuses competing work only. Requests that act on the unfinished
operation itself pass it, as today:

- a start or stop while one of the same kind is unfinished returns that
  operation with `changed: false`, and a replay of the same idempotency key
  returns the existing operation;
- `operation.retry` resumes it;
- the replacement and cleanup of a blocked creation, both dry run and run,
  which require that creation to be blocked
  ([state-hash-confirmation.md](state-hash-confirmation.md#the-api);
  `create_replace_store.go` and `create_cleanup_store.go` in
  internal/control).

Today two workers break the rule:

- **Start's watcher** reported `ready` while it still held the lock, which
  failed VM scenario 34 ("`busy`" from `session.repair.preview`). Groundwork
  step 0b fixes it narrowly: the session reads `starting` until the watcher
  ends, and the watcher releases the lock under the mutex that inspection
  reads.
- **Session creation** holds the session's lock for the whole worker
  (`lockCreationOperation` and its deferred release in `processCreation`,
  internal/daemon/lifecycle.go). `CompleteCreation`
  (internal/control/lifecycle_store.go) makes the session `established` and
  the operation `completed` in one transaction; the worker then calls
  `InspectSession`, and only then releases the lock. The TUI attaches as soon
  as it sees the creation completed (internal/tui/workflows.go), so it can
  meet the lock in that window.

Step 4 makes the rule structural instead of fixing each worker: a worker's
session hold belongs to its operation, and the one journal function that
records an operation's end releases the hold first, then commits. Workers no
longer release session locks themselves. The cleanup workers that take a
session's lock slot (`processCreateCleanup`, the replacement cleanup in
internal/daemon/create_replace_cleanup.go) follow the same path.

## Start and stop as operations

`session.start`:

1. **Admission, in the request:** the checks `StartSession` makes today
   before it starts its watcher: the session is established, its policy is
   current, its Git principal is registered and active, its key and
   endpoints exist, and its runtime exists. A failed check is an error and
   records nothing.
2. **Record** a `session.start` operation, return it with `changed: true`,
   and enqueue it.
3. **The worker** holds the session, starts the runtime and waits for the
   host, as `startOwned` and `awaitOwnedHost` do today; from step 8 it calls
   the runtime driver's `Start`, which "returns once the session host is
   ready" ([runtime-driver.md](runtime-driver.md#guarantees)).
4. **The end:** `completed` when the host is ready, or `failed` with the
   diagnostic when the runtime ended stopped, recorded after the hold ends.

`session.stop` follows the same steps with today's stop checks
(established, no pending or confirmed attachment) and the runtime stop.

**A repeat changes nothing, and says so** (maintainer, 2026-11-02). Both
methods answer `{changed, operation?, session?}`
([wire-types.md](wire-types.md#the-types)):

| The session | `session.start` / `session.stop` answers |
|---|---|
| Already ready (start) or already stopped (stop) | Records nothing: `{"changed": false, "session": {…the current view…}}` |
| An operation of the same kind is unfinished | `{"changed": false, "operation": {…that operation…}}` |
| Otherwise, once admitted | Records the new operation: `{"changed": true, "operation": {…}}` |

So start and stop take no idempotency key: they converge, are safe to send
again, and the reply says whether anything happened. The journal's required
key column holds the operation's own ID.

While a start or stop operation is unfinished, `session.inspect` and
`session.list` report `starting` or `stopping` from the journal, whatever the
runtime shows. `stopping` is a new condition value, decided by the
maintainer on 2026-11-02; `session.condition_changed` events and the
file-log handler's allowed values (internal/plugin/filelog.go) gain it.
Since step 9's plugin part 2, core's event validation holds those values
(internal/eventhandler/event.go). Without `stopping`, a client that sees the
runtime stopped could act while the stop operation is unfinished.

**Only the newest finished start or stop is kept** (maintainer, 2026-11-02).
Recording a new start or stop deletes the session's older finished one, so
each start and stop does not add a row that the TUI loads on every refresh
until it reaches its 4096-record limit. Only real starts and stops count,
since a repeat records nothing. The session view takes a failed start's
diagnostic from that newest operation, replacing the
`session.start.diag:<uuid>` metadata rows, which are never deleted today.
That settles half of the
[state-safety debt](../../debts/state-safety.md#per-session-state-outlives-its-session)
"Per-session state outlives its session"; the in-memory status rate maps,
which only project deletion clears, remain, and the entry stays for them.

`session.attach` no longer starts the session. It accepts a ready session,
and answers `retry` naming an unfinished start, or `conflict` for a session
that is not ready. `p attach` and the TUI start the session, wait for the
operation and then attach, so users see the same result. The wait loops in
`AttachSession` (internal/daemon/attachment.go) go.

The journal's existing rule, one unfinished operation per session (the
`active_session_operation` index in internal/control/store.go), now covers
start and stop: a rename or removal of a session that is starting is
refused through the same admission check.

## Origin change and publish as operations

Decided by the maintainer on 2026-11-02: `origin.change` and `origin.publish`
become operations in step 4, as "every change returns an operation" reads.
Before step 4 both contacted the origin inside the request, with origin
deadlines of 35 seconds inside a 30-second request, and a push cut by the
request deadline stayed `attempted` and replayed `outcome_unknown` for its
key (`PublishPublication` in internal/control/publication_api.go, which step
4 replaced with the operation's worker, `RunPublication`). Those deadlines
were internal/plugin/sourcegit.go's and
internal/gitservice/origin_publication.go's. Since step 9's S3 the origin
calls are the [source driver](source-driver.md)'s, and core bounds each at
35 seconds (`Store.CallOrigin` in internal/control/origin_lock.go). As
operations, `outcome_unknown` becomes a `failed` operation with that reason,
never retried, as today; their request tables (`origin_requests`,
`publication_requests`) fold into the journal.

## One journal write path

Today 36 `UPDATE operations` statements in internal/control and one insert
write the journal, and each worker decides whether to emit an event. Step 4
routes every status change through one journal function. After each commit
it reports the change to the daemon, which emits the progress event and wakes
the operation's waiters. The same function records an operation's end after
releasing its session hold.

### Progress events

Today `operation.progress` comes from 8 of 17 kinds, each emitting its own
subset. After, every kind emits one event per status change, with the
`phase` values the file-log handler already accepts. Phase changes inside a
kind emit nothing: as
[session observability](../session-observability.md#typed-p-events)
says, these are "not the journal's internal workflow phase names".

| Journal change | `phase` |
|---|---|
| Recorded | `accepted` |
| `blocked` to `running` (Retry) | `running` |
| To `blocked`, `failed` or `completed` | The same word |
| To `superseded` | `cancelled` |

### Resuming after a restart

**The rule: at start, the daemon resumes every `running` operation; a
`blocked` operation waits for `operation.retry`.**

Why this rule:

- Most kinds already behave so. Today `Recover`
  (internal/daemon/lifecycle.go) enqueues blocked operations too, but 13 of
  17 kinds' workers return unless the status is `running`; only project and
  session creation, project deletion and creation cleanup resume blocked work
  once. The 1-second `schedule` loop already re-dispatches only running work,
  so the daemon gets one rule at start and while it runs.
- `blocked` means the worker met a condition it cannot resolve alone and
  recorded a diagnostic for a person; a restart is no evidence that it
  changed. The creation code states the risk: "Restart must not turn a
  failed planned branch CAS into fresh UUID effects while the user is
  preparing replacement" (internal/daemon/creation_work.go).
- In the VM scenarios I checked that restart the daemon (17, 34, 42 to 45,
  47 and 49 to 52), every operation blocked after a restart is followed by an
  explicit `operation.retry`.

The `unknown` status is allowed by the journal's schema but never written;
the step removes it.

### Order of unfinished operations

**The order: the order the journal accepted them, across all kinds.** Each
operation gets a sequence number when recorded (SQLite `AUTOINCREMENT`, never
reused). `UnfinishedOperations` returns unfinished operations in that order.
`operation.list` pages in it too, with an opaque cursor that carries the
sequence number: `next` is a string the client passes back as `after`
unchanged. Today the cursor is the last operation's ID, compared with
`id > ?` (`ListOperations` in internal/control/lifecycle_store.go); a
sequence cannot be looked up from an ID whose row is gone, and rows do go:
project deletion deletes its project's operations, and recording a start or
stop deletes the session's older finished one.

Why acceptance order:

- No kind depends on another being recovered first. The journal allows one
  unfinished operation per session (`active_session_operation`); project
  deletion is admitted only when no operation of the project is unfinished
  (`projectDeleteSnapshot` in internal/control/project_delete_store.go);
  session creation needs an active project; Git ref guards serialize work on
  one branch.
- Acceptance order is the order in which requests filled the worker queue
  (`enqueue` appends), so recovery refills it close to how it stood. It
  replaces today's per-kind groups (`unfinishedGroups` in
  internal/control/operation_journal.go).

## The TUI's refresh

The TUI re-reads `project.list`, `session.list` and `operation.list` every 3
seconds (`tickCmd` in internal/tui/model.go). That stays. Its progress page
calls `operation.wait` instead of polling `operation.inspect` or
`session.inspect` on each tick, so it moves on as soon as an operation
finishes.

A change feed is out of scope and would not remove the refresh: a session's
condition is read from the runtime when something asks (`InspectSession`
observes the host on every `session.list`), so the daemon learns that a
container stopped, or that a service changed state, only by looking.

The TUI loads every operation on each refresh and refuses more than 4096
records in all (`loadInventory` in internal/tui/client.go). Keeping only the
newest finished start or stop per session bounds the rows they add.

## Departures from current docs

- [Control API](../control-api.md#implemented-methods): `session.start`
  ("poll `session.inspect`"), `session.stop`, `session.attach` ("Starts a
  stopped runtime and waits for readiness"), `operation.list`'s cursor, and
  the new `operation.wait`.
- [Session lifecycle](../session-lifecycle.md): "A blocked early local
  request … stays blocked across restart" becomes the rule for every kind;
  "Attach and detach are the live exception … Any start required by attach"
  loses its last sentence, since attach no longer starts.
- [Session observability](../session-observability.md#typed-p-events):
  the `stopping` condition, and which journal changes emit
  `operation.progress`.
- [Control API](../control-api.md): `origin.change` and `origin.publish`
  return operations, and a publication's `outcome_unknown` is a failed
  operation's reason.
- [State-safety debt](../../debts/state-safety.md#per-session-state-outlives-its-session):
  the start diagnostic rows go; the entry narrows to the status rate maps.

## Options set aside

| Option | Why not |
|---|---|
| Fix each worker's release order, as step 0b does for start | Every new worker that holds a session could break it again; one journal function that ends operations makes it hold for all. |
| Start and stop as operations held only in memory | No journal rows to bound, but a second kind of operation that `operation.retry`, the resume rule and the recovery order do not cover. |
| `operation.wait` with timeout 0 instead of `operation.inspect` | One method fewer, but a read that may block holds a request slot, and the TUI's lists read operations at once. |
| Resume blocked work once at every restart, for every kind | Turns restarts into retries nobody asked for, including early creations that today deliberately stay blocked. |
| Keep the kind-grouped recovery order | No kind depends on it, and it needs a table entry for every new kind. |

## How it will be checked

- Unit tests for `operation.wait`: it returns at once for a finished
  operation, on the commit that leaves `running` (including `blocked`), and
  at the timeout with `running`; it refuses a timeout outside 1 to 25 and an
  unknown ID; `rpc.cancel` ends it; it reads no SQLite while it waits.
- A deterministic test of the end rule, for creation, start and stop: a test
  hook pauses the worker right after its end is committed; a client that
  follows the operation with `operation.wait` then starts or attaches to the
  session (creation) or renames it (start, stop) without
  meeting the session's lock. With the hook placed before the commit
  instead, the same requests wait for the commit and are then admitted,
  since the journal's transaction holds the store's one SQLite connection
  (`TestOperationEndsOnlyAfterItsWorkerLetsGo` in
  internal/daemon/journal_test.go).
- A unit test of the shared admission check: competing work on a session
  held by a `running` operation gets `retry` naming it, and held by a
  `blocked` one, `conflict` with reason `operation_blocked`, for every method
  that needs the session. In the same test the permitted paths pass: a
  start or stop during an unfinished one of the same kind returns it with
  `changed: false`, a key replay returns the existing operation,
  `operation.retry` resumes a blocked one, and the dry run and run of a
  blocked creation's replacement and cleanup succeed.
- Unit tests for start and stop: `session.inspect` never reports `ready`
  while a start operation is unfinished, nor `stopped` while a stop
  operation is; a start on a ready session, or a stop on a stopped one,
  records no row and answers `changed: false` with the current view; a start
  or stop during an unfinished one of the same kind returns that operation
  with `changed: false`; otherwise a new operation comes with
  `changed: true`; a host that fails gives `failed` with its diagnostic; stop with an attachment is
  refused; `session.attach` refuses a session that is not ready; recording a
  start or stop deletes the session's older finished one, and a failed
  start's diagnostic shows in the session view with no `session.start.diag`
  row written.
- Unit tests for origin operations: `origin.change` and `origin.publish`
  return operations; a key replays its operation; a push whose outcome is
  unknown ends `failed` with reason `outcome_unknown` and is never retried.
- TUI tests: a wait that returns `running` keeps `attachOnComplete` and
  waits again; the TUI's wait timeout plus its connection fits its call
  budget.
- A unit test that every kind emits the events in the table above, from one
  path, with no event on a phase change.
- Unit tests for recovery and order: a running operation of every kind
  resumes at start; a blocked one does not until Retry; unfinished
  operations list in acceptance order across kinds; `operation.list`
  continues correctly after the last operation of the previous page is
  deleted.
- The VM scenarios move from polling `operation.inspect`
  and `session.inspect` (`wait_operation`, `wait_ready`) to
  `operation.wait`, keeping their outcome checks. Scenarios 47, 49, 51 and
  52 keep their Retry after a restart.
