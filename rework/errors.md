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
attachment client show the message to the user. A panic in a request handler
stops the daemon. Operations cut their diagnostics at 512, 900 or 1024
bytes, and list summaries cut them again at 256 or 512.

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
- A panic in a request handler is recovered, logged, and answered as
  `internal`.
- Every operation's diagnostic is cut once, at 1024 bytes, where the journal
  writes it.

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
| The control store failed, a result could not be encoded, a handler panicked | `internal` on system methods, `unavailable` on lifecycle methods; a panic stops the daemon | `internal` |

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
| `conflict` | -32011 | Change the named state first: detach, Retry or repair, wait for a person | `attachment_active`, `session_not_established`, `operation_blocked`, `operation_superseded` |
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
detaches a terminal, retries a blocked operation or repairs the session, and
nothing failed. Step 5 turns these conditions into refusals inside the plan
for the eleven risky changes
([plan-shape.md](plan-shape.md#fields-that-do-not-fit-the-envelope), item 2),
using the same codes; the others, such as stopping a session with a terminal
attached, keep answering `conflict`.

**Why the deadline is `retry`.** The daemon gave up, not the client, and
sending the same request again is safe: a change that was recorded before the
deadline comes back through its idempotency key, or for start and stop
because a repeated call returns the unfinished operation
([waiting.md](waiting.md#start-and-stop-as-operations)). A cancellation the
client asked for stays `cancelled`. Today `ServeConn` also replaces a result
the handler did return with the error once the deadline has passed
(internal/control/rpc.go), so a recorded change can be reported as failed;
after, a returned result is sent, and only an error caused by the context
becomes `retry` or `cancelled`.

**Why `retry` and `conflict` differ by the holder's status.** An unfinished
operation that holds the session gives `retry` while it is `running`: it will
leave `running` without anyone acting, and the client can wait for it with
`operation.wait` ([waiting.md](waiting.md)). A `blocked` one gives `conflict`
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

In Go, the daemon keeps returning errors and one mapper turns them into
`api.Error`: the sentinels `ErrConflict`, `ErrNotFound` and `ErrInvalid`
become typed errors that carry their kind, reason and target, so the reason
is set where the condition is found, not guessed in the mapper.

## Mapping today's error sites

### The mappers and their branches

| Site | Today | After |
|---|---|---|
| `lifecycleRPC`, `ErrInvalid` | `invalid_params`, "invalid lifecycle request" | `invalid` |
| `lifecycleRPC`, `*sessionCapacityError` | `busy` with its own message | `capacity`, reason `session_capacity` |
| `lifecycleRPC`, `ErrConflict` | `busy`, "lifecycle request conflicts with current authority" | `retry`, `conflict`, `precondition_failed` or `invalid` (`key_reused`), by where it is raised; see below |
| `lifecycleRPC`, `ErrNotFound` | `unavailable`, "record was not found" | `not_found` with the target, or `not_configured` where it stands for a missing capability |
| `lifecycleRPC`, context cancelled or deadline | `cancelled` | `cancelled` for `rpc.cancel` or a closed connection; `retry`, reason `deadline`, for the server's deadline |
| `lifecycleRPC`, any other error | `unavailable`, "lifecycle authority is unavailable" | The kind its type carries: `failed` with the authority as reason from the adapters, the condition's own kind elsewhere, `internal` when nobody typed it; see below |
| `projectDeleteRPC`, `ErrConflict` | `busy` with the error's text, cut at 900 bytes | As `lifecycleRPC`; the text goes to the log |
| `projectDeleteRPC`, any other error | `unavailable` with fixed advice to stop sessions and obtain fresh inspections | `failed` with the authority as reason; the advice goes, with the inspections ([plan-shape.md](plan-shape.md#by-category)) |
| `internalRPC`, store errors | `internal`, "control state is unavailable" | `internal`, reason `store` |
| `rpc_services.go`, errors that are not sentinels | `unavailable`, naming a ready runtime, a user manager and a "compatible selected runtime package" | `failed`, reason `runtime`; the driver's `ErrNoService` becomes `not_found`, reason `service` ([services-plugin.md](services-plugin.md#guarantees)) |

### Direct constructions

| Site | Today | After |
|---|---|---|
| The decode checks: 56 `errorRPC(-32602, …)` calls in seven files of internal/control, and 7 `lifecycleRPC(ErrInvalid)` in attachment.go | `invalid_params` with a message per handler | `invalid` from the request type's `Validate`, with `field` ([wire-types.md](wire-types.md#the-types)) |
| `parseRequest` and `ReadFrame` (rpc.go) | `parse_error`, `invalid_request`, `unsupported_version` for `jsonrpc` other than 2.0, `invalid_params` for missing params | Unchanged kinds; missing params mean `{}` |
| Duplicate in-flight ID (`ServeConn`) | `invalid_request` | `invalid_request`, reason `duplicate_id` |
| Too many concurrent requests (`ServeConn`) | `busy` | `retry`, reason `too_many_requests` |
| A handler returning both or neither of result and error | `internal` | `internal`, reason `encoding` |
| The dedicated attachment connection calling another method (`ServeConn`) | `lifecycleRPC(ErrConflict)`, so `busy` | `invalid_request` |
| `attachment.claim` from an untrusted connection | `invalid_params` | `invalid_request` |
| `unconfiguredMethod`: `project.list` or `project.branches` without Git | `unavailable`, "Git capability is not configured" | `not_configured`, reason `git` |
| `unconfiguredMethod`: other `project.`, `session.`, `origin.`, `runtime.`, `status.` names | `unavailable`, "capability is not implemented" | `not_configured` for a method of the protocol; `method_not_found` for any other name |
| `methodTable.unavailable` (optional lifecycle interface missing) and the two in-handler fallbacks (`PreparedRepairPreviewAPI`, `AssembledCreateCleanupAPI`) | `lifecycleRPC(ErrNotFound)`: "record was not found" | Gone: the daemon implements every optional interface ([wire-types.md](wire-types.md#the-method-table)) |
| `project.branches` for an inactive project | `unavailable`, "project is unavailable" | `not_found`, target `project/<path>` |
| `project.branches` when Git cannot list refs | `unavailable`, "Git refs are unavailable" | `failed`, reason `git` |
| A page or origin source too large for one frame (`boundedLifecyclePage`, origin_api.go) | `internal` | `internal`, reason `encoding` |
| Session RPC (session_rpc.go): session not established, policy unreadable, `status.report` with an ID, unknown method | `unavailable`, `internal`, `invalid_request`, `method_not_found` | `not_found` (target the session), `internal`, `invalid_request`, `method_not_found`, in the new shape |

### What the sentinels stand for

A classification of the errors that reach a handler on a request path, not
inside operation workers, gives these groups. Counts are approximate: one
refusal often joins several conditions with `||`.

| What happened | Sites | Today | After |
|---|---|---|---|
| An in-memory lock is held: `lockSession`'s default branch (internal/daemon/lifecycle.go), project deletion's locks | 1 source, 27 acquisitions | `busy` | `retry`, `session_held` |
| An unfinished operation, ref guard or workspace inspection holds the session, branch or project: `checkNoWorkspaceInspect`, the `active_session_operation` index, `git_ref_guards`, project deletion's unfinished-work check | ≈50 | `busy` | `retry` naming the operation while it runs; `conflict`, `operation_blocked`, once it is blocked. The inspections go by earlier decisions. |
| An idempotency key recorded for another request: `keyRecordsRequest`, `replayRequest`, the retired-key check, the origin and publication key checks, the key triggers | ≈60, 5 mechanisms | `busy` | `invalid`, `key_reused` |
| A preview token missing or expired, a confirm whose recheck differs, or a value the client expected that no longer holds (`expected_old_tip`, `expected_url`, `expected_commit_oid`, `source_oid`), or a loss proof older than two minutes | ≈145 | `busy` | `precondition_failed`, `expired` or `changed`. Tokens and loss proofs go in step 5. |
| The state forbids it: not established, attached, superseded, branch assigned or existing, project exists or not active, host not ready | ≈115 | `busy` | `conflict` with a reason per state |
| Session capacity | 1 construction, 5 callers | `busy` with its message | `capacity`, `session_capacity` |
| Other full tables: attachments (128, 8 per session), public egress addresses, the preview stores | 13 | `busy`, or `unavailable` for three preview stores | `capacity` with its own reason; the preview stores go in step 5 |
| A record does not exist | ≈50 | `unavailable`, or `busy` where joined with `ErrConflict` | `not_found` |
| A capability or host setting is missing: no project policy, a local-only origin asked for `origin_ref`, the type-assertion fallbacks, `unconfiguredMethod` | ≈25 | `busy` or `unavailable` | `not_configured`, or `conflict`, `origin_not_set`, for a project without an origin |
| Incus, the runtime plugin, Git, the origin or a plugin failed | ≈130 | `unavailable` | `failed` with the authority |
| SQLite, file or random-source errors, and corrupt stored data | ≈270 | `unavailable`, "lifecycle authority is unavailable" | `internal` |

Three mechanisms decide most of today's answers, and step 3 replaces each:

- **`lifecycleRPC` tests `ErrConflict` first** (rpc_lifecycle.go:911-922). About
  20 sites return `errors.Join(cause, ErrConflict)`, so a missing record, a
  cancellation or an Incus failure behind them answers `busy`. After, the
  mapper tests the context first, then the typed error nearest the cause.
- **`classifyWrite`** (store.go) turns any SQLite constraint failure into
  `ErrConflict`, at 52 call sites fed by 61 triggers whose text says which
  case it is (a key reused, a branch reserved, the project being deleted), and
  the mapper drops that text. After, each trigger's failure maps to its kind
  and reason.
- **Errors that are not sentinels** reach the default branch. Besides real
  authority failures and store errors, they include conditions with another
  meaning: "session Git principal missing; repair required" and "… revoked"
  (lifecycle.go), "registered session key missing" and "… changed"
  (session_keys.go), "interactive host is not ready" (runtimeincus/attach.go),
  which are `conflict`; "P refs changed during branch loss preview"
  (gitservice/removal_preview.go) and "endpoint manager closed", which are
  `retry`; the size bounds of previews and inventories, such as "removal
  preview exceeds bounded control response", which are `conflict`,
  `bound_exceeded`; "public egress substrate unavailable"
  (lifecycle.go), which is `not_configured`.

After, the adapters that call outside authorities (the runtime plugin and
`runtimeincus`, `gitservice`, the source-Git plugin) return errors typed
`failed` with their authority, every condition above is typed where it is
found, and an error nobody typed answers `internal`. A misclassified
condition then shows as `internal` in the log instead of hiding as an
authority failure.

Two smaller fixes come with the mapping:

- After an operation is recorded, `enqueue` returns the context's error when
  the daemon is shutting down (lifecycle.go), and 19 request paths then
  answer an error for an operation they accepted. After, they return the
  operation; the resume rule runs it at the next start
  ([waiting.md](waiting.md#resuming-after-a-restart)).
- As the code reads (not run), a local-only project asked to publish answers
  `busy`: the URL check in `publicationContext` (publication_api.go) runs
  first, so the local-only check after it is never reached. After, it answers
  `conflict`, `origin_not_set`.

## Panics in request handlers

Today `ServeConn` (internal/control/rpc.go) runs each request in its own
goroutine with no `recover`, so a panic in any handler ends the daemon, and
with it every session's endpoints and running operations' workers.

After, the request goroutine defers one recovery that:

1. logs the method, the request ID, the panic value and the stack;
2. answers the request with `internal`, reason `panic`;
3. lets the goroutine's other deferred steps run as today: forget the request
   ID, cancel its context, free its slot.

The daemon keeps serving. The same recovery wraps the inline paths of the
read loop (`parseRequest`, `rpc.cancel`, hello), the connection's own cleanup
(`owner.close`, which releases attachments), and the session RPC's
`SessionReplyWithCommit`.

**Recovery is only safe where locks are released by `defer`.** Today a panic
ends the process, so no lock can stay held. Once panics are recovered, a
panic between `Lock` and `Unlock` leaves the mutex locked, and every later
request that needs it waits until its deadline. In internal/daemon, 48 of the
57 `l.mu.Lock()` calls are not followed by `defer`, including on request
paths:

- `sessionLockSlot` (lifecycle.go), run by every `lockSession`;
- `StartSession`'s update of `startActive` (lifecycle.go);
- the expiry loops of the seven preview stores, such as
  removal_preview.go, which unlock early inside the loop;
- `ConfirmAttachment` and `releaseAttachment` (attachment.go); the second
  calls the store and the attachment callback while holding `l.mu`.

`endpoints.mu` is held the same way in assembled_cleanup.go and
create_replace_cleanup.go. Every `lockSession`, project-deletion lock and Git
authority lock on a request path is already released by `defer`.

Step 3 moves the request paths' critical sections under `l.mu` and
`endpoints.mu` into small functions that unlock by `defer`, and the panic test
below panics inside one of them. Worker code is covered by open question 2.

## One diagnostic limit

An operation's `diagnostic` is the bounded detail of why it blocked or
failed, shown verbatim. Today each worker cuts its own:

| Limit | Where |
|---|---|
| 1024 bytes | `AdvanceOperation` (internal/control/store.go) refuses longer ones; create cleanup (internal/daemon/create_cleanup.go); the start diagnostic (`StartSession`, stored by `SetSessionStartDiagnostic`) |
| 900 bytes | Creation's `fail` (internal/daemon/lifecycle.go), project deletion (project_delete_process.go), environment collection, principal repair, retained-branch rename, and `projectDeleteRPC`'s message |
| 512 bytes | Discard and delete (discard_process.go, discard_store.go), rename (rename_store.go), assembled cleanup, repair preparation, and each resource of a project deletion |
| 256 bytes, 512 for rename | `SummarizeOperation`, again, for `operation.list` |

**After: one limit, 1024 bytes,** `api.MaxDiagnosticBytes`. The journal's one
write path ([waiting.md](waiting.md#one-journal-write-path)) applies it to
every operation's diagnostic and every diagnostic inside its evidence, and
cuts at a UTF-8 character boundary; today's `d[:limit]` can split a
character. Workers stop cutting. `operation.list` shows the same diagnostic
as `operation.inspect`, and the session view's start diagnostic is its start
operation's ([waiting.md](waiting.md#start-and-stop-as-operations)).

Why 1024: it is the largest of today's limits, so no diagnostic gets shorter,
and the store already refuses anything longer. Rename needs at least 512 to
keep both branch names visible (scenario 33 checks both). A page of 20
operations at the limit adds 20 KiB, inside the 63 KiB that
`boundedLifecyclePage` allows, and that function already shortens a page that
would not fit. Project deletion's evidence holds about 13 resource
diagnostics (at most four sessions with two resources each, after
[commit images](commit-images.md) at most one image per session, and the
repository) under a 24 KiB evidence bound (project_delete_store.go); I did not
measure the rest of that evidence, so the step checks that it fits at the new
limit, or raises the bound.

## Options set aside

| Option | Why not |
|---|---|
| Keep `busy` and `unavailable`, and add reason codes | The kind would still not say what to do; every client would branch on reasons to find out. |
| One kind per Go sentinel | `ErrConflict` alone stands for three next steps: retry, review again, change the state first. |
| Message text beside the codes | Set aside by the maintainer on 2026-11-02: clients write sentences from codes. |
| The raw cause in `data` | It carries Incus, Git and file-system text into the API, which is message text by another name; the daemon log keeps it. |
| `capacity` as a `conflict` reason | Its next step, freeing a slot, applies to every creation and needs no target; a kind of its own lets clients say so without knowing reasons. |
| `precondition_failed` for any state that forbids a request | It means that something the client sent as a condition, a change tag or an expected value such as `expected_old_tip`, no longer holds, as HTTP's 412 does for `If-Match`. A state the client stated nothing about is `conflict`. |
| Reuse -32003 and -32004 for new kinds | A script testing the old codes would silently match a different meaning. |
| Let systemd restart the daemon after a panic | Set aside by the maintainer: a panic answers `internal` and the daemon keeps running. A restart also ends attachments and in-flight requests. |
| A diagnostic limit per kind | Set aside by the maintainer: one limit for every kind. |

## Open questions

1. **Do blocked and failed operations get a `reason` code beside their
   diagnostic?** Recommendation: yes, in this step. Diagnostics today mix
   raw detail with P's own advice, such as rename's "correct Git manually"
   (checked by scenario 33) and project deletion's "Retry/investigate that
   read before a new project review"; that advice is message text, which the
   maintainer ruled out of the API. With a `reason`, clients write the advice
   from the code and the diagnostic keeps only the detail. The cost is a
   reason at every site that blocks or fails an operation.
2. **Are panics in operation workers recovered too?** The decision covers
   request handlers. A worker panic also ends the daemon today, and simply
   recovering it would leave the operation `running`, so the 1-second
   scheduler would run it again every second. Recommendation: recover, log,
   and record the operation `blocked` with reason `internal`, keeping its
   guards, so it waits for Retry like any blocked operation
   ([waiting.md](waiting.md#resuming-after-a-restart)).

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
- A panic test: a handler that panics is answered `internal`, `panic`; the
  stack is logged; the next request on the same connection and on a new one
  is answered; a handler that panics while holding the lifecycle mutex does
  not block the next request.
- Diagnostic tests: every journal write cuts at 1024 bytes on a character
  boundary; `operation.list` shows the full diagnostic; a page of 20
  operations at the limit fits one frame; scenario 33's rename diagnostic
  still holds both names.
- TUI and CLI tests: each kind and reason has a sentence; an unknown reason
  falls back to the kind's sentence.
- The test clients move to the new kinds and keep their outcome checks.
  Today they test `busy` and `unavailable` through copied helpers:
  `expect_busy` (27 uses in 8 step scripts), `expect_error` (54 uses in 9,
  which fails on any kind but `busy`, `unavailable` and `invalid_params`),
  `expect_refusal` (14 uses in 3) and a few inline checks. Seven retry loops
  (steps 55, 57 and 58, tui-drive.py, notes-attach.py,
  notes-tui-fixture.py) treat `busy` as transient and become `retry` loops.
  Three clients match message text and must move to reasons: the attachment
  fixture's `"lifecycle request conflicts with current authority"`
  (tests/integration/cmd/attachment-fixture/main.go), and the `busy:`
  screen prefixes in tui_notes_driver.py and tui-drive.py. The tui-mock maps
  unknown kinds to -32003 and uses the `api` kinds instead.
