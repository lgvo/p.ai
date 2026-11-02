# Waiting for changes

Status: proposed, 2026-11-02. Part of the [interface rework plan](REWORK-PLAN.md).

## Summary

**Today** a client learns that a change finished in three different ways:

- Most changes record a durable operation and return it at once; the client
  polls `operation.inspect`.
- `session.start` returns the session as `starting` and leaves a background
  watcher holding the session's lock; the client polls `session.inspect`.
  `session.stop` stops the runtime inside the request.
- `project.delete.confirm` waits inside the request for one fresh workspace
  loss inspection per session, and the server cuts every request at 30
  seconds.

The start watcher caused VM scenario 34's known failure: the scenario saw
`ready`, acted, and got `busy` because the watcher still held the lock.
Groundwork step 0b ([plan](REWORK-PLAN.md#steps)) fixes that race narrowly:
the session reads `starting` until the watcher ends, and the watcher releases
the lock under the mutex that inspection reads (`observedCondition` and
`endStartWatcher` in internal/daemon/lifecycle.go). Operation kinds also
differ in what progress events they emit, in whether a blocked operation
resumes when the daemon restarts, and in the order recovery lists them.

**Proposed:**

- Every change returns an operation, including start and stop. Service
  actions stay the exception: they return once queued
  ([services-plugin.md](services-plugin.md#guarantees)).
- `operation.wait {id, timeout}` returns the operation as soon as it is no
  longer running, or when the timeout passes; the timeout fits inside the
  request deadline.
- A start operation completes only after it has released the session, and
  the session reads `starting` until then, which keeps scenario 34's race
  closed by construction, for stop as well.
- Project deletion returns its operation at once, once step 0e has removed
  its inspections; nothing waits inside the request.
- One journal write path emits the same progress events for every kind and
  wakes waiters. At restart the daemon resumes running operations, and a
  blocked operation waits for Retry. Unfinished operations list in the order
  they were accepted.

**What users notice:** in the TUI, a started session becomes attachable as
soon as it is ready instead of at the next 3-second refresh. A session being
stopped reads `stopping`. A blocked project creation, session creation,
creation cleanup or project deletion no longer retries once by itself when
the daemon restarts; the user presses Retry, as for every other kind already.

## Before and after

| Change | Today returns | After |
|---|---|---|
| `project.create`, `session.create`, `session.rename`, `project.retained.rename`, `operation.retry`, `session.repair.prepare`, each confirm (discard, delete, the four repairs, create replacement, create cleanup, retained-branch deletion) | An operation | Unchanged |
| `session.start` | The session as `starting`; a watcher holds the lock until the host is ready | An operation of kind `session.start` |
| `session.stop` | The session, after stopping the runtime inside the request | An operation of kind `session.stop` |
| `project.delete.confirm` (`project.delete` after step 5) | An operation, after up to 30 seconds of fresh loss inspections inside the request | The operation, at once (from step 0e) |
| `origin.change`, `origin.publish` | The origin state or publication result, after contacting or pushing to the origin inside the request | Operations; see open question 3 |
| `session.attach` | A pending attachment, after starting the session and waiting for it inside the request | A pending attachment for a ready session; it no longer starts one |
| `session.service.action` | The result after systemd's job finished | Returns once queued, not an operation ([services-plugin.md](services-plugin.md)) |
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
← {"operation":{"id":"9b0e…","kind":"session.start","status":"running",…}}
→ operation.wait {"id":"9b0e…","timeout":25}
← {"operation":{"id":"9b0e…","kind":"session.start","status":"completed",…}}
```

## `operation.wait`

| | |
|---|---|
| Params | `id`, the operation; `timeout`, whole seconds from 1 to 25 |
| Result | `operation`, as `operation.inspect` returns it |
| Returns | As soon as the status is not `running`: `completed`, `failed`, `superseded` or `blocked`. Otherwise when the timeout passes, with the status still `running`. |
| Errors | `not_found` for an unknown ID; `invalid` for a timeout outside 1 to 25 ([errors.md](errors.md)) |

The server keeps its 30-second request deadline (`ServeConn` in
`internal/control/rpc.go`); a 25-second timeout leaves time to answer before
it. A client that needs longer calls again; `rpc.cancel` ends a wait early.
A wait returns on `blocked` because a blocked operation needs a person or a
Retry, not more time.

The daemon does not poll SQLite for a wait. The journal write path described
below wakes the waiters of an operation after each committed status change.
Waiters live in the daemon's memory: a restart closes their connections, and
the client waits again on the new daemon.

`operation.inspect` stays: it answers at once, which is what lists and the
TUI's operations page need.

## Start and stop as operations

`session.start`:

1. **Admission, in the request:** the checks `StartSession` makes today
   before it starts its watcher (internal/daemon/lifecycle.go): the session
   is established, its policy is current, its Git principal is registered
   and active, its key and endpoints exist, and its runtime exists. A failed
   check is an error and records nothing.
2. **Record** a `session.start` operation, return it, and enqueue it.
3. **The worker** holds the session, starts the runtime and waits for the
   host, as `startOwned` and `awaitOwnedHost` do today; from step 8 it calls
   the runtime driver's `Start`, which "returns once the session host is
   ready" ([runtime-driver.md](runtime-driver.md#guarantees)).
4. **The worker releases the session, then records the outcome:**
   `completed` when the host is ready, or `failed` with the diagnostic when
   the runtime ended stopped.

Starting a session that is already ready records an operation that completes
at once. Starting a session whose start is unfinished returns that unfinished
operation. `session.stop` follows the same steps with today's stop checks
(established, no pending or confirmed attachment) and the runtime stop.

While a start or stop operation is unfinished, `session.inspect` and
`session.list` report `starting` or `stopping` from the journal, whatever the
runtime shows, and every request that needs the session answers `retry` with
the operation as its target ([errors.md](errors.md)). `stopping` is a new
condition value; `session.condition_changed` events and the file-log handler's
allowed values (`internal/plugin/filelog.go`) gain it. Without it, a client
that sees the runtime stopped could act while the stop operation still holds
the session, the same race as scenario 34's.

`session.attach` no longer starts the session. It accepts a ready session,
and answers `retry` naming the unfinished start, or `conflict` for a session
that is not ready ([errors.md](errors.md)). `p attach` and the TUI start the
session, wait for the operation and then attach, so users see the same
result. The wait loops in `AttachSession` (internal/daemon/attachment.go)
go.

The journal's existing rule, one unfinished operation per session (the
`active_session_operation` index in internal/control/store.go), now covers
start and stop: a rename, repair or removal of a session that is starting is
refused with `retry`, as the held lock refuses it today.

### Scenario 34's race

Before step 0b, as the state-safety debt entry recorded it until 0b removed
the entry:

```text
session.start          watcher goroutine takes the session lock; reply: starting
session.inspect …      the runtime reports the host ready, so: ready
session.repair.preview lockSession finds the lock held: busy   (158 ms before the release, in the debt's trace)
watcher                clears its starting flag, then releases the lock
```

Before 0b `InspectSession` turned only `stopped` into `starting` while
the watcher ran, never `ready`, and the watcher cleared its flag before it
released the lock. Step 0b closes both gaps for start. Stop has no
watcher today because it stops the runtime inside the request; as an
operation it needs the same guarantee.

After:

```text
session.start          records operation S; its worker holds the session
operation.wait S …     S is running; session.inspect says starting
worker                 host ready; releases the session; records S completed
operation.wait S       returns S completed; session.inspect now says ready
session.repair         the session is free
```

A client that waits for the operation, or polls until `ready`, never meets a
held session: both become true only after the release.

## Project deletion

`project.delete.confirm` already records a `project.delete` operation and
enqueues it (`ConfirmProjectDelete` in internal/daemon/project_delete.go).
Before that, it starts one `workspace.loss.inspect` operation for each
session whose runtime is present and polls each every 100 ms until it
completes, then compares the fresh loss with the reviewed one. The server
cuts the request at 30 seconds, although `p api` gives this one method a
two-minute deadline (cmd/p/main.go); a slower inspection answers `cancelled`
while the inspections continue.

The maintainer removed the workspace loss inspection from deletions on
2026-11-02 ([plan-shape.md](plan-shape.md#by-category)). Without it the
confirm checks the reviewed state, records the operation and returns it. The
worker, `processProjectDelete`, does the deletion as today, and the client
waits with `operation.wait`. The two-minute special case in `p api` goes.

Groundwork step 0e ([plan](REWORK-PLAN.md#steps)) removes these inspections
together with the other deletion inspections: both the preview's
`loss_operations` parameter and the confirm's inspection loop. Removing both
at once keeps the preview from binding an inspection that the confirm no
longer rechecks. After 0e the confirm already returns its operation at once;
step 4 adds `operation.wait` for the client, and step 5 reshapes the preview
into the plan.

## One journal write path

Today 36 `UPDATE operations` statements in 15 files of internal/control and
one insert (`insertOperation`) write the journal, and each worker decides
whether to emit an event.

Step 4 routes every status change through one journal function that reports
each committed change to the daemon. The daemon then emits the progress event
and wakes the operation's waiters. Phase changes inside a kind emit nothing:
the event's `phase` reduces the status and is, as
[session observability](../docs/session-observability.md#typed-p-events)
says, "not the journal's internal workflow phase names".

### Progress events

Today `operation.progress` comes from 8 of 17 kinds: project and session
creation, discard and delete, create cleanup (both paths), repair and repair
preparation (`completed` only), and project deletion. Environment collection,
rename, retained-branch rename and deletion, ref, principal and record
repair, and both workspace inspections emit nothing. Only creation emits
`accepted`, and nothing emits `cancelled`.

After, every kind emits the same events, one per status change, with the
`phase` values the file-log handler already accepts:

| Journal change | `phase` |
|---|---|
| Recorded | `accepted` |
| `blocked` to `running` (Retry) | `running` |
| To `blocked`, `failed` or `completed` | The same word |
| To `superseded` | `cancelled` |

### Resuming after a restart

**The rule: at start, the daemon resumes every `running` operation; a
`blocked` operation waits for `operation.retry`.**

Today `Recover` (internal/daemon/lifecycle.go) enqueues running and blocked
operations, except where a kind's `resumeAtStart` declines
(internal/daemon/operation_kinds.go). What happens to a blocked operation then
depends on its worker:

| Kinds | Blocked at restart |
|---|---|
| `project.create`, `session.create` (except blocked early local creations, `deferBlockedCreationUntilRetry`), `project.delete`, `session.create.cleanup` | Resumed once |
| The other 13 kinds; their workers return unless the status is `running` | Wait for Retry |

Why this rule:

- 13 of 17 kinds already follow it, and the 1-second `schedule` loop already
  re-dispatches only running work, so the daemon has one rule at start and
  while it runs.
- `blocked` means the worker met a condition it cannot resolve alone and
  recorded a diagnostic for a person. A restart is no evidence that the
  condition changed. The creation code states the risk for early creations:
  "Restart must not turn a failed planned branch CAS into fresh UUID effects
  while the user is preparing replacement" (internal/daemon/creation_work.go).
  After step 5 a resumed worker would also change the state under a dry run
  the user is reviewing.
- In the scenarios I checked that restart the daemon (17, 34, 42 to 45, 47
  and 49 to 52), every operation blocked after a restart is followed by an
  explicit `operation.retry`; scenario 45 checks that a blocked creation stays
  blocked across a restart. No scenario relies on a blocked operation
  resuming by itself.

The `unknown` status is allowed by the journal's schema but never written;
the step removes it.

### Order of unfinished operations

**The order: the order the journal accepted them, across all kinds.** Each
operation gets a sequence number when recorded (SQLite `AUTOINCREMENT`).
`UnfinishedOperations` returns unfinished operations in that order, and
`operation.list` pages in it too, keeping the operation ID as its cursor.

Today `unfinishedGroups` (internal/control/operation_journal.go) lists 13 kind
groups in a fixed order, each sorted by `created_at` or by `rowid`, because
"each group keeps the order its listing has always used" when bfbd3eb merged
the per-kind listings.

Why acceptance order:

- No kind depends on another being recovered first. The journal allows one
  unfinished operation per session (`active_session_operation`); project
  deletion is admitted only when no operation of the project is unfinished
  (`projectDeleteSnapshot` in internal/control/project_delete_store.go);
  session creation needs an active project (`beginSessionCreateCaptured`);
  Git ref guards serialize work on one branch.
- Acceptance order is the order in which requests filled the worker queue
  (`enqueue` appends), so recovery refills it close to how it stood.
- Neither of today's keys gives it. `created_at` is RFC 3339 text with
  trailing zeros dropped, so `…:12Z` sorts after `…:12.5Z`. `rowid` can be
  reused after the newest row is deleted, and project deletion deletes rows.

## The TUI's refresh

The TUI re-reads `project.list`, `session.list` and `operation.list` every 3
seconds (`tickCmd` in internal/tui/model.go). That stays. Its progress page
changes: it calls `operation.wait` instead of polling `operation.inspect` or
`session.inspect` on each tick, so it moves on as soon as an operation
finishes.

A change feed is out of scope and would not remove the refresh: a session's
condition is read from the runtime when something asks (`InspectSession`
observes the host on every `session.list`), so the daemon itself learns that
a container stopped, or that a service changed state, only by looking. The
Services page keeps its refresh, which shows fire-and-observe service actions
progressing.

The TUI loads every operation on each refresh and refuses more than 4096
records in all (`loadInventory` in internal/tui/client.go). Start and stop
operations would add two rows per start and stop; open question 2 bounds
them.

## Departures from current docs

- [Control API](../docs/control-api.md#implemented-methods): `session.start`
  ("poll `session.inspect`"), `session.stop`, `session.attach` ("Starts a
  stopped runtime and waits for readiness"), `project.delete.confirm` ("Fresh
  loss and identity checks"), and the new `operation.wait`.
- [Session lifecycle](../docs/session-lifecycle.md): "A blocked early local
  request … stays blocked across restart" becomes the rule for every kind;
  "Attach and detach are the live exception … Any start required by attach"
  loses its last sentence, since attach no longer starts.
- [Session observability](../docs/session-observability.md#typed-p-events):
  the `stopping` condition, and which journal changes emit
  `operation.progress`.

## Options set aside

| Option | Why not |
|---|---|
| Keep start returning `starting`, with step 0b's fix of the watcher | Fixes scenario 34, but keeps a second way to wait, keeps `AttachSession`'s wait loops, and leaves start and stop the only changes without an operation, against the decision. |
| Start and stop as operations held only in memory | No journal rows to bound, but a second kind of operation that `operation.retry`, the resume rule and the recovery order do not cover. |
| `operation.wait` without a timeout, or with one longer than the request deadline | The server cuts every request at 30 seconds; a longer wait would need a second deadline rule. |
| `operation.wait` with timeout 0 instead of `operation.inspect` | One method fewer, but a read that may block holds a request slot, and the TUI's lists read operations at once. |
| Resume blocked work once at every restart, for every kind | Turns restarts into retries nobody asked for, including early creations that today deliberately stay blocked. |
| Keep the kind-grouped recovery order | No kind depends on it, and it needs a table entry for every new kind. |
| A change feed for the TUI | The daemon would still poll the runtime to fill it; see [The TUI's refresh](#the-tuis-refresh). |

## Open questions

1. **Do start and stop take an idempotency key?** Recommendation: no. Both
   converge and are safe to repeat, and a repeated start while one is
   unfinished returns that operation. The journal's required key column
   holds the operation's own ID.
2. **How long are finished start and stop operations kept?** Recommendation:
   only the newest finished start or stop of each session; recording a new
   one deletes the older. Otherwise every start and stop adds a row that the
   TUI loads on every refresh, until it reaches its 4096-record limit. The
   session view takes a failed start's diagnostic from that newest
   operation, replacing the `session.start.diag:<uuid>` metadata rows, which
   are never deleted today
   ([state-safety debt](../docs/debts/state-safety.md#per-session-state-outlives-its-session)).
3. **Do `origin.change` and `origin.publish` become operations in step 4?**
   Recommendation: yes, as the "every change returns an operation" decision
   reads. Both contact the origin inside the request today, with origin
   deadlines of 35 seconds (internal/plugin/sourcegit.go,
   internal/gitservice/origin_publication.go) inside a 30-second request,
   and a push cut by the request deadline stays `attempted` and replays
   `outcome_unknown` for its key (`PublishPublication` in
   internal/control/publication_api.go).
   As operations, `outcome_unknown` becomes a `failed` operation with that
   reason, never retried, as today; their request tables (`origin_requests`,
   `publication_requests`) fold into the journal.
4. **May the session condition gain `stopping`?** Recommendation: yes. It is
   a new value users see for a few seconds, and without it the stop
   operation reopens scenario 34's race for stop; see
   [Start and stop as operations](#start-and-stop-as-operations).

## How it will be checked

- Unit tests for `operation.wait`: it returns at once for a finished
  operation, on the commit that leaves `running` (including `blocked`), and
  at the timeout with `running`; it refuses a timeout outside 1 to 25 and an
  unknown ID; `rpc.cancel` ends it; no SQLite reads while it waits.
- Unit tests for start and stop: a start operation never reads `completed`
  while the worker holds the session, and `session.inspect` never reports
  `ready` while a start operation is unfinished; a request needing the session gets `retry` naming the operation
  until then; a second start returns the unfinished operation; a host that
  fails gives `failed` with its diagnostic; stop with an attachment is
  refused; `session.attach` refuses a session that is not ready.
- A unit test that every kind emits the events in the table above, from one
  path, with no event on a phase change.
- Unit tests for recovery: a running operation of every kind resumes at
  start; a blocked one of every kind does not until Retry; unfinished
  operations list in acceptance order across kinds, including after a row is
  deleted.
- From step 0e, a unit test that `project.delete.confirm` returns its
  operation without starting an inspection; step 4 keeps it.
- VM scenario 34 passes; the scenarios move from polling `operation.inspect`
  and `session.inspect` (`wait_operation` in 30 step scripts, `wait_ready` in
  22) to `operation.wait`, keeping their outcome checks. Scenarios 47, 49, 51
  and 52 keep their Retry after a restart.
