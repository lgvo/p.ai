# One repair method that diagnoses the session

Status: proposed, 2026-11-02. Part of the [interface rework plan](REWORK-PLAN.md).

## Summary

An established session can need one of four repairs: its runtime is
missing, its assigned P ref is missing, its Git principal is missing or
revoked, or nothing is left of it but its record. A creation that failed has
two more ways out: cleanup and replacement.

**Today** the client picks the repair. Each repair has a preview and a
confirm method, and missing-runtime repair also has `session.repair.prepare`:
nine methods. The session view says only that a runtime is `missing`, which
fits two of the four repairs, and nothing about the ref or the principal, so
a client finds the right repair by trying previews. Step 5 gives each repair
one method called twice; with `session.repair.prepare`, five remain.

**Proposed:**

- One method, `session.repair {uuid}`, called twice as
  [state-hash-confirmation.md](state-hash-confirmation.md) decides. The dry
  run diagnoses the session and returns the plan of the repair that applies,
  named by `details.kind`. A plan that is not allowed says in its refusals
  what is needed first, such as a stopped session (`session_running`) or the
  image that `session.repair.prepare` builds (`recorded_image_missing`).
- The run starts that kind's operation, which does what that repair does
  after step 5.
- Today's rules already keep the four repairs apart: at most one can be
  allowed in any state. The diagnosis checks the runtime, then the ref, then
  the principal. That order decides only which refused plan a session with
  several problems shows.
- `session.repair.prepare` stays. Failed-creation cleanup and replacement
  stay separate methods: both can apply to the same failed creation, beside
  Retry, and the user chooses.

The repair methods go from five to two, and the risky-change methods from 11
to 8. Every repair runs the same checks and reaches the same result.

Two questions wait on the maintainer; see [Open questions](#open-questions).

## Today

### The repairs and when each applies

| Repair (preview `kind`) | Methods | Runtime | Assigned P ref | Git principal | Also required |
|---|---|---|---|---|---|
| Missing runtime (`missing_runtime`) | `session.repair.preview`, `.confirm`; `session.repair.prepare` | absent, proven by a full inventory | present | registered, active, host key matches | policy current and valid, plugin selection unchanged, no attachment, image present or prepared |
| Missing assigned ref (`missing_assigned_ref`) | `session.ref.repair.preview`, `.confirm`, after a `workspace.loss.inspect` | present, running or stopped | absent | registered, active, host key matches | the workspace's tip already in P's repository (else `p_object_missing`), policy current |
| Git principal (`session_git_principal`) | `session.principal.repair.preview`, `.confirm` | present and stopped | present | not intact: registration missing or revoked, or the key on the host or in the runtime missing or different | policy current |
| Unrecoverable record (`unrecoverable_session_record`) | `session.record.repair.preview`, `.confirm` | absent, proven by a full inventory | absent | any | no active operation, no attachment |

All four need an established session. The rules are in `previewRepair`
(repair.go), `PreviewRefRepair` (ref_repair.go), `principalRepairFacts`
(principal_repair.go) and `recordRepairFacts` (record_repair.go) in
`internal/daemon`, and in
[session lifecycle](../docs/session-lifecycle.md#repair) and the
[control API](../docs/control-api.md).

Failed-creation **cleanup** and **replacement** need a session that is still
`creating`, with its creation blocked in a supported phase
(`createCleanupFacts` in create_cleanup.go, `createReplaceFacts` in
create_replace.go). Cleanup ends the session and keeps its branch;
replacement supersedes the creation with a new create request that the
caller supplies.

Step 5 changes two of these rules: ref repair requires a stopped session and
reads only the assigned branch's tip, with no loss inspection, and cleanup of
an assembled failure needs no loss inspection
([plan-shape.md](plan-shape.md#by-category)).

### How a client chooses

The client must already know which repair fits. `session.inspect` reports
`session_condition: "missing"` for an absent runtime, which fits both
missing-runtime and record repair, and nothing about the assigned ref or the
principal (`SessionView` in `internal/api/types.go`). So a client tries a
preview and reads its refusals. Five codes only say that the repair tried
does not fit:

| Code | From | Means |
|---|---|---|
| `runtime_present` | missing runtime | the runtime exists: ref or principal repair |
| `assigned_branch_uncommitted` | missing runtime | the ref is absent too: record repair |
| `assigned_ref_present` | ref repair, record repair | the ref exists: another repair |
| `assigned_ref_missing` | principal repair | the ref is absent: ref repair |
| `principal_intact` | principal repair | nothing for this repair to do |

The TUI offers no repair; the [user guide](../docs/user-guide.md) sends users
to the CLI, where each repair is a `p api` call with hand-written JSON.

### Can two apply at once?

Not among the four repairs of an established session. Their rules split on
two facts:

| | Assigned ref present | Assigned ref absent |
|---|---|---|
| **Runtime absent** | Missing runtime | Unrecoverable record |
| **Runtime present** | Git principal, if the principal is not intact | Missing assigned ref |

So at most one repair can be allowed in any state. A session can still have
several problems; then one repair applies and refuses. Two such
combinations have no repair today:

- **Runtime and principal both broken.** Missing-runtime repair refuses with
  `credential_unavailable`, and principal repair needs a runtime to install
  the key into (`runtime_changed`).
- **Ref and principal both broken, runtime present.** Ref repair refuses with
  `credential_unavailable`, and principal repair refuses an absent ref
  (`assigned_ref_missing`).

I did not verify whether Discard or Delete can end such a session: both read
the assigned branch in their preview.

Failed creations differ. Cleanup and replacement both accept a creation
blocked in its early phases, so both can be allowed for the same session at
the same time, and `operation.retry` of the blocked creation is a third
choice. None of them overlaps with the four repairs, which need an
established session.

## The decision

### The dry run diagnoses

```text
session.repair {uuid}
  lock the session
  not established                  → none, refused: session_not_established
  inspect the runtime              → Incus unreachable: an error, no plan
  runtime absent
    assigned ref present           → missing_runtime
    assigned ref absent            → unrecoverable_session_record
  runtime present
    assigned ref absent            → missing_assigned_ref
    principal broken on the host   → session_git_principal
    session running                → none, refused: session_running
    key in the runtime not matching → session_git_principal
    otherwise                      → none, refused: nothing_to_repair
  then the chosen kind runs its own checks, which add refusals as after step 5
```

"Broken on the host" means today's principal facts: the registration is
missing or revoked, or the host key is missing, different or unsafe. "Not
matching" means the key in the stopped runtime is missing, invalid or
different. Today's `principal_intact` is the registration active and both
keys matching.

**Why this order.** Each check settles a fact the later repairs need:

1. **The runtime first.** Ref repair reads the branch tip from the stopped
   runtime, and principal repair installs the new key into it; neither can
   run without a runtime. Without one, the ref tells missing-runtime repair
   from record repair.
2. **The ref next.** Principal repair refuses to rotate a key without the P
   ref: "This repair never interprets an absent P ref as an authority to
   rotate a key" (principal_repair.go).
3. **The principal last.** It needs both.

Because the four repairs are disjoint, the order never hides a repair that
could run. For a session with several problems, it shows the plan whose
prerequisites come first.

The diagnosis and the chosen kind's plan are read under one session lock,
and every fact the diagnosis reads is in the change hash. If the state moves
to another kind between the dry run and the run, the tags differ and the run
is refused with `precondition_failed`.

### The plan

The envelope is [plan-shape.md](plan-shape.md)'s, with `action:
"session.repair"` and `target: "session/<uuid>"` for every kind. `details.kind`
names the kind, and the rest of `details` is that kind's details as
plan-shape.md maps them:

| `details.kind` | Details |
|---|---|
| `missing_runtime` | [missing-runtime mapping](plan-shape.md#sessionrepair-repairpreview) |
| `missing_assigned_ref` | [ref repair mapping](plan-shape.md#sessionrefrepair-refrepairpreview) |
| `session_git_principal` | [principal repair mapping](plan-shape.md#sessionprincipalrepair-principalrepairpreview) |
| `unrecoverable_session_record` | [record repair mapping](plan-shape.md#sessionrecordrepair-recordrepairpreview) |
| `none` | Nothing else: `details` is `{"kind": "none"}` |

No kind has `loss`. Missing-runtime and record repair list the runtime's
former contents in `gone` ([plan-shape.md](plan-shape.md#already-gone)). So
the CLI's `--force` never counts down for a repair.

Refusal codes change as follows; every other code stays as step 5 leaves it.

- **New:** `nothing_to_repair`. `session_not_established`, a code
  plan-shape.md already names for removal; step 5 settles the list.
- **Renamed:** principal repair's `runtime_not_stopped` becomes
  `session_running`: the maintainer decided on 2026-11-02 that ref repair
  and principal repair's access to the runtime refuse a running session with
  that code. Record repair's
  `runtime_absence_unavailable` becomes `runtime_absence_unverified`, the
  code missing-runtime repair uses for the same absence proof.
- **Gone:** the five codes in [How a client chooses](#how-a-client-chooses),
  and `runtime_changed` where it only meant an absent runtime. Principal and
  ref repair keep `runtime_changed` for a runtime whose identity changed.

An unreachable Incus is an error for every kind, as it is for missing-runtime
repair today and as plan-shape.md's rule for unreachable authorities says.
Record repair today answers it with a refused plan.

### The run

`session.repair {uuid, key, if_match}` replays the key, checks the expiry,
diagnoses and plans again, compares the tags, and starts the kind's
operation. The operation is the one step 5's method for that kind starts;
step 6 changes only which method admits it.

| `details.kind` | Operation | What it does |
|---|---|---|
| `missing_runtime` | `session.runtime.repair` (today `session.repair`) | Creates the runtime for the same UUID from the session's source-commit image with its registered key, starts it, and checks out the branch's latest committed tip ([commit-images.md](commit-images.md)) |
| `missing_assigned_ref` | `session.ref.repair` | Creates the absent P ref at the assigned branch's tip in the stopped workspace, by a create-only compare-and-swap; the workspace is untouched |
| `session_git_principal` | `session.principal.repair` | Disables the old principal, installs a new key in the stopped runtime, and leaves it stopped |
| `unrecoverable_session_record` | `session.record.repair` | Rechecks both absences, disables the principal, removes the key, the endpoint and the record |

The operation kind of missing-runtime repair is renamed, so that an
operation named `session.repair` is not read as "any repair". The other
three keep their names. A key replays its operation whatever its kind.

### `session.repair.prepare` stays

`session.repair.prepare {uuid, key}` builds the image of the session's
source commit, as [commit-images.md](commit-images.md) decides. It is
accepted only when the dry run's kind is `missing_runtime` and its only
refusal is `recorded_image_missing`. It destroys nothing, so it needs no
confirmation. Once it completes, a new dry run finds the image.

### Failed creations keep their own methods

`session.create.cleanup` and `session.create.replace` stay separate:

- Both can be allowed for the same failed creation, beside Retry. Picking
  one is the user's choice, not a fact a diagnosis can read.
- Replacement takes a new create request as input; `session.repair` takes
  only the session.
- They decide what becomes of a creation that never finished; the four
  repairs restore an established session, or remove its record when nothing
  else is left.

`session.repair` on a session that is still creating returns `none`, refused
with `session_not_established`; the CLI and TUI sentence for that code can
point to Retry, replacement and cleanup.

Two recoveries stay outside repair, as today: a blocked operation resumes
through `operation.retry`, and a branch or upstream mismatch is corrected by
hand.

## Before and after

| | Today | After step 5 | After step 6 |
|---|---|---|---|
| Missing runtime | `session.repair.preview`, `session.repair.confirm` | `session.repair` | `session.repair` |
| Its image | `session.repair.prepare` | `session.repair.prepare` | `session.repair.prepare` |
| Missing assigned ref | `session.ref.repair.preview`, `.confirm`, after `workspace.loss.inspect` | `session.ref.repair` | `session.repair` |
| Git principal | `session.principal.repair.preview`, `.confirm` | `session.principal.repair` | `session.repair` |
| Unrecoverable record | `session.record.repair.preview`, `.confirm` | `session.record.repair` | `session.repair` |
| Repair methods | 9 | 5 | 2 |
| Failed-creation methods | 4 | 2 | 2 |
| Risky-change methods | 23 | 11 | 8 |
| Who picks the repair | The client | The client | The daemon |

A ref repair, before:

```text
→ workspace.loss.inspect     {"uuid":"4f1c…","key":"l1"}           then wait for it
→ session.ref.repair.preview {"uuid":"4f1c…","loss_operation_id":"…"}
← {"preview":{"kind":"missing_assigned_ref",…,"eligible":true,"confirmation_token":"…"}}
→ session.ref.repair.confirm {"uuid":"4f1c…","key":"k1","confirmation_token":"…"}
```

After, with the session stopped:

```text
→ session.repair {"uuid":"4f1c…"}
← {"plan":{"action":"session.repair","target":"session/4f1c…","allowed":true,
     "refusals":[],"loss":[],"gone":[],
     "details":{"kind":"missing_assigned_ref","local_tip":"…",…}},
   "change_tag":"20261102T153012Z-<64 hex>"}
→ session.repair {"uuid":"4f1c…","key":"k1","if_match":"20261102T153012Z-<64 hex>"}
← {"operation":{"kind":"session.ref.repair",…}}
```

A missing runtime whose image is gone:

```text
→ session.repair         {"uuid":"4f1c…"}
← {"plan":{…,"allowed":false,"refusals":[{"code":"recorded_image_missing"}],
     "gone":[{"kind":"runtime_data","target":"session/4f1c…","certain":true}],
     "details":{"kind":"missing_runtime","image_status":"missing",…}}}
→ session.repair.prepare {"uuid":"4f1c…","key":"p1"}             then wait for it
→ session.repair         {"uuid":"4f1c…"}                         now allowed, with a tag
```

The code: the dry run's plan function becomes the diagnosis, which calls the
chosen kind's plan function; each kind keeps its plan and run in its own
file. The RPC layer has two repair handlers instead of five.

## What users and clients notice

- One method, and one CLI command, for every repair of an established
  session. The plan says which repair it is; the client needs to know
  nothing about the four kinds to offer it.
- The same repairs run with the same checks and outcomes, including the two
  combinations above that no repair fixes.
- The CLI's `p session repair` (step 7) prints the plan, `gone` as
  information, and one sentence per refusal code. On `recorded_image_missing`
  it can run `session.repair.prepare`, wait for it and dry-run again; step 7
  decides.
- Missing-runtime repair's operation is named `session.runtime.repair`.

## Departures from agreements

These change with this step:

- [Session lifecycle](../docs/session-lifecycle.md#repair): each repair
  shape as its own explicit plan, and "missing-ref repair uses its separate
  explicit plan".
- [Control API](../docs/control-api.md): the sections for the four repairs.
- [User guide](../docs/user-guide.md): the troubleshooting rows that name
  repair paths.

And in this rework:

- [state-hash-confirmation.md](state-hash-confirmation.md) lists ref,
  principal and record repair as methods of their own, 11 in all; after this
  step they are kinds of `session.repair`, 8 in all.
- [plan-shape.md](plan-shape.md) gives `details` one type per method;
  `session.repair` has one per kind. Its ref-repair example's `action`
  becomes `session.repair`, and its principal-repair mapping gains
  `session_running` in place of `runtime_not_stopped`.
- [runtime-driver.md](runtime-driver.md) has `SetGitKey` but no operation
  that reads the key in a stopped runtime. The diagnosis and principal
  repair's plan read it today (`InspectStoppedSessionIdentity` in
  `internal/runtimeincus`), so step 8 needs such a read, or principal repair
  stops checking that key.

## Options set aside

| Option | Why not |
|---|---|
| One method per repair, as after step 5 | The client must know the four repairs and which one fits; the session view does not tell it, so it tries them in turn. |
| A read-only `session.diagnose` beside the repair methods | Two calls and the same four methods; the dry run already is the diagnosis. |
| Every repair that applies, as a list of plans | At most one can be allowed in any state, so the list holds one runnable plan at most, plus refused plans waiting on it. One plan and a fixed order say the same with one shape and one tag. |
| A `kind` parameter, so the client asks for a repair | Lets a client ask for a repair that does not apply, and brings back the guessing. The daemon already knows. |
| Cleanup and replacement under `session.repair` | Both can be allowed for the same failed creation, beside Retry: a choice for the user, not for a diagnosis. Replacement also needs a new create request. |
| Building a missing image inside the run, dropping `session.repair.prepare` | Set aside in [plan-shape.md](plan-shape.md#sessionrepair-repairpreview): the run only creates the runtime. The image belongs to the commit, and building it destroys nothing. |
| One operation kind for every repair | Each repair has its own phases and recovery, and the journal's registry describes each operation kind once. |

## Open questions

1. **What does a running session show when nothing is found?** The dry run
   cannot read the Git key inside a running runtime, because P reads a
   session's storage only while it is stopped.
   - (a) `none`, refused with `session_running`: stop the session so P can
     check its key.
   - (b) `none`, refused with `nothing_to_repair`, the key in the runtime
     unchecked.

   **Recommendation: (a).** `nothing_to_repair` would claim a check P did not
   make. The cost is a stop and a second dry run for a healthy session.

2. **Does the TUI get a Repair action?** It has none today, and the user
   guide sends repairs to the CLI. With one method, the TUI needs one action
   on a session that shows the plan the way it shows a removal plan after
   step 5, then confirms. **Recommendation: yes, in step 7 with
   `p session repair`, so both use one sentence per refusal code; step 6
   stays an API change.**

## How it will be checked

- Unit tests for the diagnosis, one per line of its sketch: a creating
  session; an unreachable Incus; each cell of the runtime-and-ref table; a
  principal broken on the host, running and stopped; a different key in a
  stopped runtime; a healthy session, running and stopped.
- The two combinations without a repair keep today's refusal,
  `credential_unavailable`.
- A tag from one kind's plan is refused with `precondition_failed` after the
  state moves to another kind, for example when the ref reappears.
- The run starts the operation that matches `details.kind`; a key replays
  its operation whatever the kind, also after a restart.
- `session.repair.prepare` is refused unless the kind is `missing_runtime`
  and its only refusal is `recorded_image_missing`.
- No plan carries a code this document removes.
- Today's preview tests for the four repairs call `session.repair` instead,
  with the same states and outcomes.
- VM scenarios 34, 38, 39, 40 and 42 call `session.repair` and keep their
  outcome checks; 34 stays a known failure until step 4. Scenarios 45 to 47,
  49 and 51 keep the failed-creation methods.
