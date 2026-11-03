# The shape of a plan

Status: proposed, 2026-11-02. Part of the [interface rework plan](REWORK-PLAN.md).
It defines the `plan` that a dry run returns in
[state-hash-confirmation.md](state-hash-confirmation.md).

## Summary

A dry run of a risky change returns a plan, and every plan has a `loss` list.
[state-hash-confirmation.md](state-hash-confirmation.md) says that much but not
what a plan holds. Today the ten preview types in `internal/control` for these
changes answer the same two questions, "can this run?" and "what will I lose?",
each under its own field names. A client must know all ten shapes to answer
them, and the CLI's `--force` loss warning and the TUI need both answers for
every change.

**Proposed:** every risky change returns one envelope around its own typed
details:

- `allowed` and `refusals` say whether the change can run now, and why not.
- `loss` lists what the change would destroy, by category. Each item has a
  `kind` from a short fixed list, a count and a sample where P has them, and
  `certain: false` when P cannot rule the loss out.
- `details` holds everything else today's preview shows, typed per method.

The CLI warning and the TUI read only `allowed`, `refusals` and `loss`; they
never need a method's details to decide whether to warn or what to print.

No question waits on the maintainer.

## Before and after

| A client asks | Today | After |
|---|---|---|
| Can it run? | `eligible` in 6 of 10 previews. Removal, project deletion and retained-branch deletion have no field: a returned preview is always allowed, and every refusal is an RPC error. Publication shows `relation: "divergent"`, and only the run reports `status: "refused"`. | `allowed` |
| Why not? | `blocked_reason` (one string, missing-runtime repair) or `unsafe_reasons` (a list, five previews) | `refusals` |
| What will I lose? | Four fields: `runtime.loss`, `branch_loss`, `loss_warnings`, `warnings` | `loss` |
| What is already gone? | `runtime.runtime_loss_unknown` or `runtime_local_loss: "unrecoverable"`, after the caller passed `acknowledge_missing_runtime` or `acknowledge_missing` | `gone` |
| What exactly will it do? | Every other field | `details` |
| How do I confirm? | `confirmation_token` and `expires_at` (nine previews), or nothing (publication) | `change_tag`, beside the plan |

Before, a session deletion preview, abridged:

```json
{"kind": "delete", "session_uuid": "4f1c…", "project": "team/app", "branch": "work",
 "runtime": {"condition": "present", "loss": {"schema": "p.workspace-loss/v1",
   "worktrees": [{"path": "/workspace", "changes": [{"code": " M", "path": "src/app.go"}, …], …}],
   "local_only_commits": [], "runtime_data_will_be_removed": true, …}, …},
 "branch_loss": {"commits_losing_p_reachability": ["9f2c…", "81ab…"],
   "origin": {"status": "unknown", "reason": "origin_refresh_unavailable", …}, …},
 "confirmation_token": "<32 hex>", "expires_at": "…"}
```

and a publication to a divergent destination, which says "not allowed" only
through `relation`:

```json
{"project": "team/app", "destination_ref": "refs/heads/main", "relation": "divergent", …}
```

After:

```json
{"plan": {
   "action": "session.delete",
   "target": "session/4f1c…",
   "allowed": true,
   "refusals": [],
   "loss": [
     {"kind": "runtime_data", "target": "session/4f1c…", "certain": true},
     {"kind": "p_commits", "target": "project/team/app:branch/work", "count": 2,
      "sample": ["9f2c…", "81ab…"], "certain": true}
   ],
   "details": {"project": "team/app", "branch": "work", "runtime": {"condition": "present", …}, "branch_loss": {…}, …}
 },
 "change_tag": "20261102T153012Z-<64 hex>"}
```

```json
{"plan": {
   "action": "origin.publish",
   "target": "project/team/app:origin-branch/main",
   "allowed": false,
   "refusals": [{"code": "destination_divergent"}],
   "loss": [],
   "details": {"project": "team/app", "destination_ref": "refs/heads/main", …}
 }}
```

## The reply

A dry run, a call with neither `key` nor `if_match`, has three outcomes, as
[state-hash-confirmation.md](state-hash-confirmation.md#the-flow) decided:

| Outcome | Reply |
|---|---|
| The change is allowed | `{"plan": {…}, "change_tag": "…"}` |
| The change is not allowed | `{"plan": {…}}` with `allowed: false` and its refusals. No `change_tag`. |
| An error, such as invalid params, not found, or an unreachable authority | An error. No plan and no `change_tag`. |

## The envelope

| Field | Type | Meaning |
|---|---|---|
| `action` | string | The method name, such as `session.delete`. The same value as `action` in the change hash. |
| `target` | string | What the change acts on. Forms below. |
| `allowed` | bool | Whether the change can run now. It is `true` exactly when `refusals` is empty; the shared code in `internal/control/change.go` sets it, so the two cannot disagree. |
| `refusals` | array of refusals, never null | Why the change cannot run. Empty when allowed. |
| `loss` | array of loss items, never null | What the change would destroy. Empty means nothing is lost. |
| `gone` | array of loss items, never null | What is already lost before the change runs, such as the contents of a runtime that is missing. Information only: the change destroys none of it. |
| `details` | object, one type per method | Everything else the preview shows today. |

`details` keeps every field of today's preview except these, which leave it:
the field naming the target (`session_uuid`, `uuid`, `old_uuid`), the removal
`kind` (now in `action`), `eligible`, `blocked_reason`, `unsafe_reasons`,
`confirmation_token`, `expires_at`, the fixed prose texts, and the fields that
came from a workspace inspection (see [By category](#by-category) and the
mappings). The raw evidence that loss items summarize, `branch_loss`, stays in
`details` whole.

### Targets

| Target | Form | Example | Used by |
|---|---|---|---|
| Session | `session/<uuid>` | `session/4f1c…` | Discard, delete, create replacement (the old UUID), create cleanup |
| Project | `project/<path>` | `project/team/app` | Project deletion |
| P branch | `project/<path>:branch/<name>` | `project/team/app:branch/saved` | Retained-branch deletion; `p_commits` loss items |
| Origin branch | `project/<path>:origin-branch/<name>` | `project/team/app:origin-branch/main` | Publication; `<name>` is the branch name the source driver's `CheckNames` returns for `destination_ref` ([source-driver.md](source-driver.md#names-urls-and-commit-ids)) |

A project path has segments of `[A-Za-z0-9._-]` (`validProject` in
`internal/control/store.go`), and Git refuses `:` in branch names, so the first
`:` always ends the project path.

### Refusals

A refusal is an object:

| Field | Type | Meaning |
|---|---|---|
| `code` | string | A stable snake_case code. Today's `unsafe_reasons` codes keep their names, such as `old_creation_not_blocked` and `request_unchanged`. |
| `target` | string, optional | What the refusal is about when it is not the plan's target, in target form: for example the one session that blocks a project deletion. |

A refusal carries no message text, by the maintainer's decision of
2026-11-02 that messages stay out of the API: the CLI and TUI hold one
sentence per code, including any hint such as what to do next. Raw error text
behind a refusal goes to the daemon log, not into the plan.

These codes change:

- The two `unsafe_reasons` tied to the loss inspection were gone after step
  0e, which removed the inspection:
  `assembled_workspace_requires_dedicated_loss_inspection; preserve_or_retry_exact_request`
  went, and the plan lists an existing creator container as `runtime_data`
  loss instead; `creator_bound_completed_loss_or_exact_stopped_runtime_unavailable: <error>`
  became `exact_stopped_runtime_unavailable: <error>`. Step 5 dropped the
  error text from that code, so a creator container that P cannot identify
  exactly is refused with `exact_stopped_runtime_unavailable`, and the error
  text goes to the daemon log.
- Publication gains `destination_divergent`, from `relation: "divergent"`.

### A plan that is not allowed

A plan that is not allowed still lists `loss` and `details`, holding what P
found before it stopped. They can be incomplete, because planning may stop
at its first refusal. So only an allowed plan's `loss`
is complete, and a client must not read an empty `loss` in a refused plan as
"nothing would be lost".

### Already gone

When a session's runtime is missing, its contents (uncommitted changes, files
outside Git, commits only in the runtime) are lost before any change runs. P
counts a runtime as missing only after Incus reports it absent and a full
inventory of P's Incus project finds no instance with that session's UUID; an
unreachable Incus refuses the change instead
([control-api.md](../docs/control-api.md#session-removal-and-rename)).

The maintainer decided on 2026-11-02 that this is not loss: nothing is left
to save, so the `--force` countdown would protect nothing. The plan lists it
in `gone` instead, as a `runtime_data` item with `certain: true`, and the CLI
and TUI print it as information. The parameters that make a caller
acknowledge a missing runtime today, `acknowledge_missing_runtime` on removal
and `acknowledge_missing` on project deletion, are removed: the change tag
binds a plan that says the runtime is missing, so running it proves the caller
saw that.

### What the change hash covers

The change hash's `plan` is the reply's `plan` object, as `encoding/json`
encodes it, less one fact shown for information only (below). That holds
because a plan carries no token, expiry or prose: those leave the plan.
Project deletion's details list commit images, in `images`
(`api.CommitImageClaim`), with no timestamp. A timestamp reaches the hash
only inside a digest, and as a fact rather than an observation time: for
example each commit image row's `created_at`, in project deletion's
`state_sha256`, which tells a re-created image from the one the user
reviewed.

The fact left out is a removal's `runtime.original_status`, which the plan
shows for information only (`Change.Shown` in
internal/control/change.go). P stops a running container itself, so a Start
or Stop between the dry run and the run keeps a removal's tag. Project
deletion's tag does change when a session is started or stopped: its
`state_sha256` is a digest of the project's stored state, which covers the
project's operations, and starting or stopping a session records one
(`projectDeleteSnapshot` in internal/control/project_delete_store.go).

## Loss items

### By category

Deletions do not look inside the container, by the maintainer's decision of
2026-11-02. Session discard and delete, project deletion and create cleanup
state loss by category: whenever a session's container exists, the plan lists
one `runtime_data` item, certain, for the container and everything in it.
`p_commits` still comes from P's repository. So `workspace.loss.inspect`, its
`p.workspace-loss/v1` report, the `loss_operation_id` and `loss_operations`
inputs, and the two-minute freshness rule for a loss inspection leave the
deletion plans; no plan in this document uses them. Discard and delete no
longer require the session stopped: P stops the container as part of deleting
it. An attached terminal still refuses them, with `attachment_active`.

By the same decision, the plan meets PROJECT.md's success signal,
"destructive actions explain what will be lost before proceeding", by
category rather than by file list. The loss-analysis section of
[session lifecycle](../docs/session-lifecycle.md#runtime-local-loss), which
lists changed files, ignored-file summaries and local-only commits, changes
when step 5 lands.

A second decision of the same day: P reads or changes a session's workspace
only while the session is stopped, and never freezes it. Rename requires the
session stopped and refuses a running one with `session_running`.

### Fields

| Field | Type | Meaning |
|---|---|---|
| `kind` | string | One of the kinds below. |
| `target` | string | Where the lost thing lives, in target form: the session for its container, the branch or project for P commits. It can differ from the plan's target; a project deletion lists losses per session. |
| `count` | integer, optional | How many. Omitted when P does not count them. An item with a count of zero is not listed. |
| `sample` | array of strings, optional | At most five commit IDs, in the source list's order. The full list stays in `details`. |
| `certain` | bool | `true`: P saw that this exists and the change destroys it: a container that exists, or commits in P's repository. `false`: P cannot rule the loss out. P never checks other repositories, including the origin: whether work is published there is the user's concern. |
| `reason` | string | Required when `certain` is false: why P cannot tell. |

### Kinds

Three kinds cover everything the plans report as loss. The CLI lines are
illustrative; the CLI owns its wording.

| Kind | What is lost | From today's field | CLI line |
|---|---|---|---|
| `runtime_data` | The session's container and everything in it: uncommitted changes, files outside Git, commits not pushed to P, processes, home, temporary files, runtime-local credentials. P does not look inside. | The container's presence (`runtime.condition: "present"`). It replaces today's `runtime.loss` report and the runtime parts of `loss_warnings` and `warnings`. A missing runtime (`runtime_loss_unknown`, `runtime_local_loss`) is listed in `gone`, not `loss` | In `loss`: `the container of session 4f1c… and everything in it: uncommitted changes, files outside Git, commits not pushed to P; P does not look inside`. In `gone`: `the runtime is already missing; whatever it held is gone` |
| `p_commits` | Commits that leave P's repository: no remaining branch there keeps them | `branch_loss.commits_losing_p_reachability` | `2 commits on branch work will be removed from P's repository; P does not check whether the origin has them` |
| `unreachable_objects` | Git objects in P's repository that no branch reaches | Project deletion's first `warnings` text | `Git objects in P's repository that no branch reaches; P does not count them` |

Not loss: P-owned authority that a change retires, such as Git principals,
session keys and endpoint sockets. It stays in `details`, for example
`provisional` in create cleanup and `credentials` in project deletion.

Cached environment images are not loss either, by the maintainer's decision
of 2026-11-02: an image is a cache, so losing it costs a rebuild, not work.
Project deletion removes every session, so nothing is left that needs exact
recreation. The commit images it deletes, which replaced cached environment
images in step 1, are listed in `images` in `details`
([commit-images.md](commit-images.md)).

Deletions do not check the origin, by the maintainer's decision of
2026-11-02: whether work is published there is the user's concern. Session
deletion, retained-branch deletion and project deletion list the commits that
leave P's repository, always as certain loss, and no longer contact the origin
during the dry run. Today's `branch_loss.origin` (status, containing branches,
unresolved refs) is removed from their details, and `CompareOriginRemoval`
(`internal/gitservice/removal_preview.go`) is no longer called for them. This
departs from [project lifecycle](../docs/project-lifecycle.md), which asks the
deletion report for "fresh origin-preservation evidence"; that document
changes when the deletion step lands.

### Certain and uncertain loss

`certain: false` comes from today's "unknown" values:

| Today | Loss item |
|---|---|
| "not enumerated" in project deletion's `warnings` | `unreachable_objects`, `certain: false`, `reason: "not_enumerated"` |

Two "unknown" or "unavailable" values are not loss, and stay in `details` only:

- `runtime_local: "unavailable"` in create replacement and early create
  cleanup: no runtime was ever initialized, so there is nothing to lose.
- `workspace_status: "unknown"` in publication: publishing reads P's
  committed tip and destroys nothing in the workspace.

### Order

Items are ordered by target, in the order `details` lists targets (sessions
first, then the project or branch), and within one target by the order of the
kinds table. A fixed order keeps the change hash stable.

## Mapping per change

Each table lists only the fields that move, change or go. Every other field
of the preview type stays in `details` with its name and content.

### `session.discard` and `session.delete` (`RemovalPreview`)

| Today | After |
|---|---|
| `kind` | `action` |
| `session_uuid` | `target`: `session/<uuid>` |
| `runtime.condition: "present"` | `runtime_data`, certain |
| `loss_operation_id` (param); `runtime.loss_operation_id`, `observed_at`, `fingerprint`, `loss` | Removed: P no longer inspects the container; see [By category](#by-category) |
| `runtime.runtime_loss_unknown` (missing runtime) | `gone`: `runtime_data` |
| `acknowledge_missing_runtime` (param) | Removed; see [Already gone](#already-gone) |
| `branch_loss.commits_losing_p_reachability` (delete) | `p_commits`, target `project/<path>:branch/<branch>` |
| `confirmation_token`, `expires_at` | Removed |
| Not allowed | Refusal codes from today's conflict errors, from step 5; see [Fields that do not fit](#fields-that-do-not-fit-the-envelope), item 2. A running session is not refused: P stops it as part of deleting it. An attached terminal is refused with `attachment_active`. |

Discard and delete share one details type; discard has no `branch_loss`, so it
has no `p_commits`. From step 0e, removal accepts a missing assigned ref
([repairs.md](repairs.md)): delete then lists no `p_commits`, and discard has
no branch to keep.

### `session.create.replace` (`CreateReplacePreview`)

| Today | After |
|---|---|
| `old_uuid` | `target`: `session/<old_uuid>` |
| `eligible`, `unsafe_reasons` | `allowed`, `refusals` |
| Loss | None: replacement preserves every existing ref and does not review or clean workspace data. |
| `new_request.key` | Removed from the plan: the dry run leaves `key` out, and the run's `key` is both the replacement's and the new creation's idempotency key ([state-hash-confirmation.md](state-hash-confirmation.md#the-api)) |
| `confirmation_token`, `expires_at` | Removed |

### `session.create.cleanup` (`CreateCleanupPreview`)

| Today | After |
|---|---|
| `uuid` | `target` |
| `eligible`, `unsafe_reasons` | `allowed`, `refusals`; the two codes tied to the loss inspection change, as above |
| `loss_operation_id` (param); `runtime.loss_operation_id`, `observed_at`, `fingerprint`, `loss` (assembled path) | Removed: the plan no longer needs an inspection. When the creator's container exists, the plan lists it as one `runtime_data` item, as for discard. |
| `loss_warnings` | Removed. The first two texts are the `runtime_data` item; the third is `provisional` (principal, key and endpoints retired; `assigned_ref: "preserved_existing"`) and `shared_images: "preserved"` in details. |
| Loss on the early path | None: runtime initialization was never attempted |
| `confirmation_token`, `expires_at` | Removed |

### `project.delete` (`ProjectDeletePreview`)

| Today | After |
|---|---|
| `project` | `target`: `project/<path>`; also kept in details |
| Each session whose container exists | `runtime_data`, certain, target `session/<uuid>` |
| `loss_operations` (param); each session's `runtime.loss_operation_id`, `observed_at`, `fingerprint`, `loss` | Removed: P no longer inspects the containers; see [By category](#by-category) |
| Each missing session (`runtime_loss_unknown`) | `gone`: `runtime_data`, target `session/<uuid>` |
| `acknowledge_missing` (param) | Removed; see [Already gone](#already-gone) |
| `branch_loss.commits_losing_p_reachability` (every commit, since every ref goes) | `p_commits`, target `project/<path>` |
| `warnings[0]` | Removed: `p_commits`, `unreachable_objects` (always listed, `not_enumerated`) and `runtime_data` |
| `warnings[1]` | Removed: it lists what deletion keeps, the same on every call. The CLI and TUI state it. |
| `cache_images` | Replaced in step 1 by `images`, the commit images in the project's index (`api.CommitImageClaim`), which have no `confirmation_digest` or `confirmation_expires_at` |
| `confirmation_token`, `expires_at` | Removed |
| Not allowed | Refusal codes from today's conflict errors, from step 5; see [Fields that do not fit](#fields-that-do-not-fit-the-envelope), item 2. |

`attachments` is always empty today, because an attached session refuses the
preview.

### `project.retained.delete` (`RetainedDeletePreview`)

| Today | After |
|---|---|
| `project`, `branch` | `target`: `project/<path>:branch/<branch>`; both also kept in details |
| `branch_loss.commits_losing_p_reachability` | `p_commits`, same target |
| `confirmation_token`, `expires_at` | Removed |
| Not allowed | Refusal codes from today's conflict errors, from step 5; see [Fields that do not fit](#fields-that-do-not-fit-the-envelope), item 2. |

### `origin.publish` (`PublicationPreview`)

| Today | After |
|---|---|
| `project`, `destination_ref` | `target`: `project/<path>:origin-branch/<name>`; both also kept in details |
| `relation: "divergent"` | `allowed: false`, refusal `destination_divergent`. Today the run records a completed `refused` result under the publish key; a divergent destination no longer does, because no run starts without a tag. If the destination diverges between the dry run and the run, the run fails with `precondition_failed`. Nothing is pushed in either case. A push that the origin's server rejects still completes the operation with publication status `refused`. |
| `relation` `absent`, `fast_forward`, `equal` or `destination_contains` | `allowed: true`; for the last two the run records `satisfied` without pushing, as today |
| Loss | None: a push only creates or fast-forwards a branch |

## Fields that do not fit the envelope

1. **Fixed prose**: `warnings` (project deletion) and `loss_warnings` (create
   cleanup). By the maintainer's decision of 2026-11-02, no message text is
   part of the API. What these sentences say will be lost becomes loss items.
   What they say is kept (external grant contents, shared and unindexed
   images, unrelated projects, delivered handler logs) is the same on every
   call, so it carries no information the plan needs: the CLI and TUI state
   it in their own words. This changes one sentence in
   [project lifecycle](../docs/project-lifecycle.md#project-deletion), which
   has the preview point to the handler's own log-retention policy; the CLI
   and TUI do that instead.
2. **Refusals raised as errors.** Removal, project deletion and
   retained-branch deletion refused with RPC errors before step 5. Removal
   answered every such case with the same `busy` "lifecycle request conflicts
   with current authority" until step 3 typed each as `conflict` with its own
   reason. By the maintainer's decision of 2026-11-02 they answer
   like create cleanup and replacement: each "not now, do this first" condition becomes a
   refusal code in an `allowed: false` plan, for example `attachment_active`,
   `session_not_established` and `runtime_unsettled`. Step 5 made the change
   for every risky change: each `conflict` met while planning ends planning
   and becomes the plan's refusal (`Change.plan` in
   internal/control/change.go). So a plan's refusal codes are the conflict
   reasons it meets, plus the codes that only plans use, which the
   [refusals table](../docs/control-api.md#refusals) lists.
   Errors stay for invalid
   input, unknown records, unreachable authorities, and a session held for
   the moment by another request or a running operation (`retry`); methods
   that are not risky changes, such as stopping a session with a terminal
   attached, keep answering a forbidding state with `conflict`
   ([errors.md](errors.md)). Publication gains
   `destination_divergent` the same way.
3. **Frame size.** Removal and project deletion previews are checked against
   the 64 KiB frame less 1 KiB today. Without the workspace loss report, up to
   12 KiB per session, their plans shrink. Loss items add a bounded amount, at
   most five samples per item, and the frame check moves to the whole reply.

## Go shape

The envelope, the refusal and the loss item become wire types in
`internal/api`: a generic `Plan[D]` around each method's details type,
`Refusal` and `Loss`, and each risky change's result is a
`ChangeResult[D]` holding either the plan and its tag or the operation
([wire-types.md](wire-types.md#the-types)). The loss summaries are written once in the
daemon, one per source: a container that exists, a missing runtime, and a
branch loss. The shared code sets
`allowed` from `refusals`.

## Options set aside

| Option | Why not |
|---|---|
| Per-method plans with no common envelope | Every client must learn seven shapes to answer "allowed?" and "what is lost?"; the CLI warning and the TUI would each need seven renderers. |
| One generic plan with untyped details (`map[string]any` or raw JSON) | Loses the compile-time checks that shared wire types (step 2) exist to give, and the details cannot be documented as types. |
| Loss as prose lines, like today's `warnings` | Prose cannot be hashed stably, clients cannot tell a certain loss from a possible one, and translation or rewording would change the hash. |
| List the container's contents before deleting it (today's workspace loss inspection: changed files, ignored files, commits only in the runtime) | Set aside by the maintainer on 2026-11-02: deletions state loss by category. The inspection needs a helper container, pauses a running source, expires after two minutes and is checked again while deleting. |
| A list of what a change keeps, in the envelope or in `details` | Only project deletion and create cleanup state it today, always the same text. It reassures rather than protects, so it carries no information the plan needs; the CLI and TUI state it. |
| `refusals` as bare code strings, as `unsafe_reasons` are | Project deletion needs to name the session that blocks it; a refusal object carries that `target`. |
| `allowed` alone, or `refusals` alone | `allowed` is the one bit every client checks; `refusals` explain it. The shared code derives one from the other, so they cannot disagree. |
| A missing runtime's contents as an uncertain `loss` item | Set aside by the maintainer on 2026-11-02: nothing is left to save, so a countdown would protect nothing. They go in `gone`. |
| Check the origin before deleting, and list only commits it lacks | Set aside by the maintainer on 2026-11-02: whether work is published to the origin is the user's concern. The dry run stays local and never depends on the origin being reachable. |
| One loss item per commit | A deletion can remove up to 256 commits from P's repository; a count and a sample of five serve the warning, and the full list stays in `branch_loss`. |

## Open questions

None. Project deletion stops running sessions itself, as discard and delete
do, and still refuses attached terminals with `attachment_active`; confirmed
by the maintainer on 2026-11-02. Today it requires every session stopped and
detached; that requirement served its loss proofs, which are gone.

## How it will be checked

- For each change, a unit test that compares today's preview with the new
  plan for the same state and finds every field in `details`, the envelope or
  a loss item, except the fields this document removes.
- Unit tests for the shared loss summaries: each kind, each certainty row
  above, zero counts that produce no item, the sample bound, and a stable
  order.
- Unit tests for the deletions: a dry run for a session whose container
  exists lists one certain `runtime_data` item and makes no call into the
  container; a running session is planned and stopped as part of the run; an
  attached terminal is refused with `attachment_active`.
- Unit tests for removal with the assigned ref missing: discard and delete
  are allowed, and delete lists no `p_commits`.
- CLI tests: `--force` reads only `allowed`, `refusals`, `loss` and `gone`,
  and prints one line per item; a plan whose only items are in `gone` runs
  without a countdown; a plan that is not allowed is refused with or without
  `--force`.
