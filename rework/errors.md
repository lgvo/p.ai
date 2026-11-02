# Errors that name the next step

Status: proposed, 2026-11-02. Part of the [interface rework plan](REWORK-PLAN.md).

## Summary

**Today** the control API answers with nine error kinds
([control API](../docs/control-api.md#errors-and-current-capability-boundary)).
Two of them cover situations that need different next steps:

- `busy` (-32003) means a held session lock, a key reused for another
  request, a preview token that expired or went stale, a state that forbids
  the request, too many concurrent requests, and exhausted session capacity.
- `unavailable` (-32004) means a record that does not exist, a capability
  that is not configured, an optional lifecycle interface that is missing,
  and every other failure.

So clients read the message text: project deletion passes its error's text
through, capacity explains itself in its message, and the TUI and the
attachment client show the message to the user. Operations cut their
diagnostics at 512, 900 or 1024 bytes, and list summaries cut them again at
256 or 512.

**Proposed:**

- The error kinds name the client's next step: `retry`, `conflict`,
  `capacity`, `precondition_failed`, `precondition_required`, `not_found`,
  `not_configured`, `invalid`, `failed`, plus JSON-RPC's protocol kinds and
  `internal`. One kind, `conflict`, is added to the plan's list, because
  usage shows a state that forbids a request is neither a transient retry
  nor a failure.
- An error carries no text. `data` holds a `reason` code and the facts the
  next step needs, such as the `target` to wait for. The daemon logs the raw
  cause; clients write sentences from codes.
- A panic in a request handler, recovered since groundwork step 0c,
  answers `internal` with reason `panic`.
- Decided by the maintainer on 2026-11-02: blocked and failed operations
  carry a `reason` code beside their diagnostic, and the diagnostic keeps
  only detail, no advice. A panic in operation work is recovered too, and the
  operation recorded `blocked` with reason `internal`.
- Every diagnostic is cut once, at 1024 bytes, by one primitive that every
  writer calls.

**What users notice:** the TUI and CLI show their own sentences for errors
instead of the daemon's. Scripts that test `.error.kind` test
`.error.message` (or `.error.code`) and `.error.data.reason`. Diagnostics of
rename and of operations in lists are no longer cut shorter than 1024 bytes.

## Before and after

| Situation | Today | After |
|---|---|---|
| The session lock is held by another request, or an operation is running on the session | `busy` | `retry`; `target` names the operation when there is one |
| Too many concurrent requests on a connection | `busy` | `retry` |
| The request deadline passed | `cancelled` | `retry`, reason `deadline` |
| The client cancelled with `rpc.cancel` | `cancelled` | `cancelled` |
| The state forbids it: an attachment is open, the session is not established, a blocked operation holds the session, the operation was superseded | `busy` | `conflict`, with a reason and the `target` |
| Session capacity exhausted | `busy`, explained in its message | `capacity` |
| A preview token expired, its facts changed, or a value the client expected (such as `expected_old_tip`) no longer holds; from step 5, a change tag | `busy` | `precondition_failed`, reason `expired` or `changed` |
| A run call with a `key` and no `if_match` (step 5) | — | `precondition_required` |
| An idempotency key already recorded for another request | `busy` | `invalid`, reason `key_reused` |
| A record does not exist | `unavailable`, "record was not found" | `not_found`, with the `target` |
| A capability is not configured | `unavailable`, three different messages | `not_configured`; the reason names the capability |
| Incus, Git, the origin or a plugin failed or could not be observed | `unavailable`, "lifecycle authority is unavailable" | `failed`; the reason names the authority |
| The control store failed, a result could not be encoded, a handler panicked | `internal` on system methods, `unavailable` on lifecycle methods; `internal` for a panic once step 0c lands | `internal`, with reason `store`, `encoding` or `panic` |

The same refusal, before and after:

```text
Before
← {"error":{"code":-32003,"kind":"busy","message":"lifecycle request conflicts with current authority"}}

After
← {"error":{"code":-32010,"message":"retry","data":{"reason":"session_held","target":"operation/9b0e…"}}}
```

## The kinds

| Kind | Code | The client's next step | Reasons, for example |
|---|---|---|---|
| `parse_error` | -32700 | Fix the client: the line is not JSON | — |
| `invalid_request` | -32600 | Fix the client: not a valid request | `hello_required`, `duplicate_id` |
| `method_not_found` | -32601 | The protocol has no such method | — |
| `invalid` | -32602 | Fix the params | `param` (with `field`), `key_reused` |
| `internal` | -32603 | Nothing to do but report it; the daemon logged the cause | `panic`, `store`, `encoding` |
| `cancelled` | -32001 | Nothing: the client cancelled | — |
| `unsupported_version` | -32002 | Use a client that speaks a protocol in `supported` | `protocol` |
| `retry` | -32010 | Send the same request again, after the `target` operation leaves `running` if one is named | `session_held`, `too_many_requests`, `deadline` |
| `conflict` | -32011 | Change the named state first: detach, Retry, wait for a person | `attachment_active`, `session_not_established`, `operation_blocked`, `operation_superseded` |
| `capacity` | -32012 | Discard or delete a session to free a slot | `session_capacity` |
| `precondition_failed` | -32013 | Read the state again and decide again: for a risky change, a new dry run ([state-hash-confirmation.md](state-hash-confirmation.md)) | `expired`, `changed` |
| `precondition_required` | -32014 | Send the change tag in `if_match` | `if_match_missing` |
| `not_found` | -32015 | Stop: the named record does not exist | The kind of record: `session`, `operation`, `project`, `branch`, `service` |
| `not_configured` | -32016 | Configure the named capability on the host | The capability: `git`, `runtime`, `services` |
| `failed` | -32017 | Check the named authority, then send the request again | The authority: `runtime`, `git`, `origin`, `plugin` |

The codes for P's own kinds are new numbers in JSON-RPC's range for server
errors (-32000 to -32099). `busy` (-32003) and `unavailable` (-32004) go and
their numbers stay unused, so a script that still tests them fails instead of
matching a kind with another meaning.

**Why `conflict` joins the list.** Today's `ErrConflict` reaches clients from
three kinds of situations: something holds the session for a moment, a
preview went stale, or the state forbids the request until something else
happens. The first is `retry` and the second `precondition_failed`. The third
is neither: sending the request again fails the same way until the user
detaches a terminal or retries a blocked operation, and nothing failed.
Step 5 turns these conditions into refusals inside the plan for the seven
risky changes
([plan-shape.md](plan-shape.md#fields-that-do-not-fit-the-envelope), item 2),
using the same codes; the others, such as stopping a session with a terminal
attached, keep answering `conflict`.

**Why the deadline is `retry`.** The daemon gave up, not the client, and
sending the same request again is safe: a change that was recorded before the
deadline comes back through its idempotency key. Start and stop take no key:
sent again during an unfinished start or stop, they return that operation,
and on a session already ready or stopped they answer `changed: false`
([waiting.md](waiting.md#start-and-stop-as-operations)). A cancellation the
client asked for stays `cancelled`. Today `ServeConn` also replaces a result
the handler did return with the error once the deadline has passed
(internal/control/rpc.go), so a recorded change can be reported as failed;
after, a returned result is sent, and only an error caused by the context
becomes `retry` or `cancelled`.

**Why `retry` and `conflict` differ by the holder's status.** An unfinished
operation that holds the session gives `retry` while it is `running`: it will
leave `running` without anyone acting, and from step 4 the client can wait
for it with `operation.wait` ([waiting.md](waiting.md)). A `blocked` one gives `conflict`
with reason `operation_blocked`: it waits for a person.

## The error object

JSON-RPC 2.0 requires `code` and `message`. P sets `message` to the kind's
name, which is an identifier, not a sentence, and drops the non-standard
`kind` member. `data` is present whenever the error has a reason, which every
kind but `parse_error`, `method_not_found` and `cancelled` does:

| Field | Type | Meaning |
|---|---|---|
| `reason` | string | A stable snake_case code. Error reasons and plan refusal codes ([plan-shape.md](plan-shape.md#refusals)) share one namespace, so a client keeps one sentence per code. |
| `target` | string, optional | What the error is about, in [target form](plan-shape.md#targets), plus `operation/<id>` for an operation. |
| `field` | string, optional | For `invalid`: the request field at fault, when one is. |
| `supported` | array of strings, optional | For `unsupported_version`: the protocols the daemon speaks. |

No field holds text from an error message. The daemon logs the cause it
mapped, with the method and request ID, so the daemon's log shows what the
reply leaves out.

`internal/api` declares every reason as a constant, and the
[control API](../docs/control-api.md) lists them. A client that meets a
reason it does not know falls back to its sentence for the kind.

## Mapping today's errors

The daemon keeps returning Go errors; one mapper turns them into `api.Error`
by three rules, applied in this order:

1. **A context error.** `cancelled` when the client cancelled or closed the
   connection; `retry`, reason `deadline`, when the server's deadline passed.
2. **A typed error answers its own kind and reason.** The sentinels become
   typed errors where the condition is found:

   | Today | After |
   |---|---|
   | `ErrInvalid`, and the handlers' decode checks | `invalid`, reason `param` with `field`, from the request type's `Validate` |
   | `ErrConflict`: a lock or a running operation holds the target | `retry`, naming the operation when there is one |
   | `ErrConflict`: a blocked operation, or a state that forbids the request | `conflict`, with the state as reason |
   | `ErrConflict`: a key recorded for another request | `invalid`, reason `key_reused` |
   | `ErrConflict`: a token, change tag or expected value (`expected_old_tip`, `expected_url`, `expected_commit_oid`, `source_oid`) that no longer holds | `precondition_failed`, reason `expired` or `changed` |
   | `*sessionCapacityError`, and other full tables (attachments, public egress addresses) | `capacity`, with the table as reason |
   | `ErrNotFound` | `not_found` with the target; `not_configured` where it stands for a missing capability |
   | A failure from an adapter of an outside authority: the runtime plugin and `runtimeincus`, the [source driver](source-driver.md) (`gitservice` and the source-Git plugin until step 9) | `failed`, with the authority as reason |

3. **Anything untyped answers `internal`** and is logged with its cause:
   SQLite, file and encoding errors. A condition that nobody typed then shows
   as `internal` in the log, instead of hiding as "lifecycle authority is
   unavailable".

The fixed answers outside the mapper:

| Site | Today | After |
|---|---|---|
| `parseRequest`, `ReadFrame` (internal/control/rpc.go) | `parse_error`, `invalid_request`, `unsupported_version` for `jsonrpc` other than 2.0, `invalid_params` for missing params | The same kinds; missing params mean `{}` |
| `ServeConn`: duplicate in-flight ID; too many concurrent requests; a handler returning both or neither of result and error | `invalid_request`; `busy`; `internal` | `invalid_request`, `duplicate_id`; `retry`, `too_many_requests`; `internal`, `encoding` |
| The attachment helper's connection calling another method, or `attachment.claim` from an untrusted connection | `busy`; `invalid_params` | `invalid_request` |
| `unconfiguredMethod` and `methodTable.unavailable` | `unavailable` with three messages, or "record was not found" | `not_configured` with the capability for a method of the protocol; `method_not_found` for any other name ([wire-types.md](wire-types.md#the-method-table)) |
| `projectDeleteRPC` and rpc_services.go's fixed texts | `busy` with the error's text, or `unavailable` with fixed advice | The rules above; the texts go to the log |
| The session RPC (session_rpc.go) | Its own `unavailable`, `internal`, `invalid_request`, `method_not_found` | `not_found` for a session that is not established; the other kinds unchanged, in the new shape |

### What today's mapping gets wrong

These representative cases show why the rules above are needed; the step
types each site of the same pattern.

- **`lifecycleRPC` tests `ErrConflict` before anything else**
  (rpc_lifecycle.go). Sites that return `errors.Join(cause, ErrConflict)`,
  such as the removal preview's recheck (removal_preview.go), answer `busy`
  when the cause is a missing record, a cancellation or an Incus failure.
  Rule 1 and typed causes fix the order.
- **`classifyWrite`** (store.go) turns every SQLite constraint failure into
  `ErrConflict` and drops the trigger's text, which says whether a key was
  reused, a branch is reserved or the project is being deleted. Each trigger
  failure maps to its own kind and reason instead.
- **Plain errors with another meaning** reach the default branch as an
  authority failure: "session Git principal missing; repair required"
  (lifecycle.go) and "interactive host is not ready" (runtimeincus/attach.go)
  are `conflict`; "P refs changed during branch loss preview"
  (gitservice/removal_preview.go) is `retry`; "removal preview exceeds
  bounded control response" is `conflict`, `bound_exceeded`; "public egress
  substrate unavailable" (lifecycle.go) is `not_configured`.
- **A recorded operation reported as an error.** When the daemon is shutting
  down, `enqueue` returns the context's error after the operation is
  recorded (lifecycle.go), and the request answers an error. After, the
  request returns the operation, and the resume rule runs it at the next
  start ([waiting.md](waiting.md#resuming-after-a-restart)).
- **A local-only project asked to publish** answers `busy`, as the code reads
  (not run): the URL check in `publicationContext` (publication_api.go) runs
  before the local-only check. After, it answers `conflict`,
  `origin_not_set`.

## Panics

Groundwork step 0c ([plan](REWORK-PLAN.md#steps)) recovers a panic in a
request handler before this step: it answers `internal` with today's error
kinds, logs the panic, and keeps serving. Making that safe is part of 0c: a
recovered panic between `Lock` and `Unlock` would leave a mutex held, and in
internal/daemon 48 of the 57 `l.mu.Lock()` calls are not followed by `defer`,
some on request paths, so 0c converts the request paths' lock sites to
release by `defer`.

Step 3 adds two things:

- **The answer's shape:** `internal` with reason `panic`, in the error object
  above, with the method, request ID, panic value and stack in the log.
- **Panics in operation workers,** decided by the maintainer on 2026-11-02.
  0c covers request handlers only, and a worker panic still ends the daemon.
  Simply recovering it would leave the operation `running`, so the 1-second
  scheduler would run it again every second. Instead the daemon recovers,
  logs, and records the operation `blocked` with reason `internal`, keeping
  its guards, so it waits for Retry like any blocked operation
  ([waiting.md](waiting.md#resuming-after-a-restart)). Worker code takes
  `l.mu` without `defer` as well, so recovering there needs the same
  conversion in the workers' lock sites.

## A reason beside the diagnostic

Decided by the maintainer on 2026-11-02: an operation that ends `blocked` or
`failed` carries a `reason` code beside its diagnostic, from step 3. Today
diagnostics mix raw detail with P's own advice, such as rename's "correct Git
manually" (checked by scenario 33) and project deletion's "Retry/investigate
that read before a new project review"; that advice is message text, which
the maintainer ruled out of the API. Clients write the advice from the
reason, and the diagnostic keeps only the detail. Every site that blocks or
fails an operation names its reason.

## One diagnostic limit

An operation's `diagnostic` is the bounded detail of why it blocked or
failed, shown verbatim. Today the largest limit is 1024 bytes:
`AdvanceOperation` (internal/control/store.go) refuses longer diagnostics, and
the start diagnostic is cut at 1024 before `SetSessionStartDiagnostic` stores
it in `metadata`. Workers cut their own at 512 or 900 bytes before writing,
and `SummarizeOperation` cuts again, to 256 (512 for rename), for
`operation.list`.

**After: one limit, 1024 bytes,** `api.MaxDiagnosticBytes`, applied by one
primitive that step 3 adds: a function that bounds a diagnostic at a UTF-8
character boundary (today's `d[:limit]` can split a character). Every
writer of a diagnostic calls it: `AdvanceOperation`, the store's other
methods that write an operation's diagnostic, the per-resource diagnostics
inside project deletion's evidence, and `SetSessionStartDiagnostic`. Workers
stop cutting, and `SummarizeOperation` stops cutting again, so `operation.list`
shows the same diagnostic as `operation.inspect`. Step 4's journal write path
([waiting.md](waiting.md#one-journal-write-path)) calls the same primitive.
The start diagnostic stays in `metadata` until step 4 makes start an
operation; then the session view reads it from the start operation.

Why 1024: it is the largest of today's limits, so no diagnostic gets shorter,
and the store already refuses anything longer. Three cases constrain it:

- Rename needs at least 512 bytes to keep both branch names visible; scenario
  33 checks both.
- A page of 20 operations at the limit adds 20 KiB, inside the 63 KiB that
  `boundedLifecyclePage` allows, and that function already shortens a page
  that would not fit.
- Project deletion's evidence holds about 13 resource diagnostics (at most
  four sessions with two resources each, after
  [commit images](commit-images.md) at most one image per session, and the
  repository) under a 24 KiB evidence bound (project_delete_store.go). I did
  not measure the rest of that evidence, so the step checks that it fits at
  the new limit, or raises the bound.

## Options set aside

| Option | Why not |
|---|---|
| One kind per Go sentinel | `ErrConflict` alone stands for three next steps: retry, review again, change the state first. |
| `capacity` as a `conflict` reason | Its next step, freeing a slot, applies to every creation and needs no target; a kind of its own lets clients say so without knowing reasons. |
| `precondition_failed` for any state that forbids a request | It means that something the client sent as a condition, a change tag or an expected value such as `expected_old_tip`, no longer holds, as HTTP's 412 does for `If-Match`. A state the client stated nothing about is `conflict`. |
| Reuse -32003 and -32004 for new kinds | A script testing the old codes would silently match a different meaning. |

## How it will be checked

- A unit test for each kind and reason: the condition that raises it, the
  code, `message` equal to the kind's name, and `data` holding only the
  documented fields. A test asserts that every reason constant in
  `internal/api` appears in docs/control-api.md.
- Unit tests for the splits that matter most: a session held by a running
  operation gives `retry` with that operation as `target`; held by a blocked
  one, `conflict`, `operation_blocked`; a reused key gives `invalid`,
  `key_reused`; a stale preview gives `precondition_failed`; an unknown
  session gives `not_found` with its target; a method of an unconfigured
  capability gives `not_configured` with the capability as reason; Incus
  unreachable gives `failed`, reason `runtime`; the server deadline gives `retry`,
  `deadline`.
- A test that no error reply contains text from a Go error: every error the
  mapper produces is checked against the documented fields.
- 0c's panic test, extended: a handler that panics is answered `internal`
  with reason `panic`. A worker that panics leaves its operation `blocked`
  with reason `internal` and its guards in place, the daemon keeps serving,
  and the scheduler does not run the operation again.
- Diagnostic tests: the primitive cuts at 1024 bytes on a character
  boundary; a store test writes an over-long diagnostic through each method
  that takes one, the start diagnostic included, and reads back at most 1024
  bytes; `operation.list` shows the full
  diagnostic; a page of 20
  operations at the limit fits one frame; scenario 33's rename diagnostic
  still holds both names, and the scenario checks the operation's reason
  instead of the advice text.
- Every site that blocks or fails an operation records a reason, and no
  diagnostic carries advice.
- TUI and CLI tests: each kind and reason has a sentence; an unknown reason
  falls back to the kind's sentence.
- The test clients move to the new kinds and keep their outcome checks: the
  copied step-script helpers (`expect_busy`, `expect_error`,
  `expect_refusal`); the retry loops that treat `busy` as transient (steps 55,
  57 and 58, tui-drive.py, notes-attach.py, notes-tui-fixture.py), which
  become `retry` loops; the three clients that match message text (the
  attachment fixture's "lifecycle request conflicts with current authority",
  and the `busy:` screen prefixes in tui_notes_driver.py and tui-drive.py);
  and the tui-mock, which maps unknown kinds to -32003.
