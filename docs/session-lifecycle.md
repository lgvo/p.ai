# P — session lifecycle

How P creates, starts, attaches, renames, stops, discards, deletes, repairs,
and abandons sessions without confusing intended state with Git, runtime, or
credential facts.

> **Status: design.** This document is authoritative for session identity,
> lifecycle operations, concurrency, destructive-loss analysis, and recovery.
> [communication-boundaries.md](communication-boundaries.md) owns what crosses
> Git, RPC, SSH, and attachment channels;
> [environment-building.md](environment-building.md) owns environment artifacts
> and activation; [runtime-isolation.md](runtime-isolation.md) owns runtime
> assembly, grants, storage, labels, and isolated workspace access; and
> [session-observability.md](session-observability.md) owns agent-event
> reduction and presentation of runtime condition.

## Contents

- [Purpose](#purpose)
- [Identity and retained state](#identity-and-retained-state)
- [Authorities](#authorities)
- [Registry state, runtime condition, and operations](#registry-state-runtime-condition-and-operations)
- [Concurrency and quiescence](#concurrency-and-quiescence)
- [Create](#create)
- [Start](#start)
- [Attach and detach](#attach-and-detach)
- [Rename](#rename)
- [Stop](#stop)
- [Destructive preflight](#destructive-preflight)
- [Discard](#discard)
- [Delete](#delete)
- [Reconciliation and repair](#reconciliation-and-repair)
- [Abandonment and orphans](#abandonment-and-orphans)
- [Credential and artifact cleanup](#credential-and-artifact-cleanup)
- [Operation recovery summary](#operation-recovery-summary)
- [RPC and presentation](#rpc-and-presentation)
- [V1 boundary](#v1-boundary)
- [Acceptance criteria](#acceptance-criteria)

## Purpose

A session spans authorities with different failure behavior:

- SQLite records P's identity, intent, and progress;
- Git owns commits and refs;
- the runtime backend owns execution machinery and writable runtime state;
- the environment store owns immutable artifacts;
- P's Git authorization and Bifrost own separate credentials; and
- live RPC connections own attachment presence.

No transaction can atomically commit across all of them. P therefore persists
the intended operation before external mutation, gives every mutation an
idempotent verification step, and reconciles against the real authority after
interruption.

The operation journal is a recovery mechanism. The operation definitions in
this document—not the journal alone—determine what P is allowed to do.

## Identity and retained state

### Project and branch address

A project is the complete repository path on the P Git server. The path may be
a single component or contain optional namespace components:

```text
p
lgvo/p
```

Namespaces have no separate lifecycle or authorization meaning in v1. Within
one project, Git identifies a branch by its normal branch name. The complete
branch address is therefore:

```text
(project path, branch name)
```

Two projects may both contain `feature/parser`. Git's per-repository ref
uniqueness prevents two refs with the same branch name in one project.

### Session identity

Every session has an immutable UUID and, while the session exists, owns exactly
one branch address:

```text
session UUID ↔ (project path, refs/heads/<branch>)
```

The branch name is the session's human-facing name. It is not globally unique
outside its project and there is no separate mutable display name. Rename
changes the branch component while preserving the UUID and project.

A session owns at most one runtime. The runtime locator is replaceable backend
machinery, not session identity. Stable backend labels carry the P instance,
project path, and session UUID so reconciliation can find machinery when a
stored locator is missing or stale.

### Assignment lifetime

The UUID-to-branch assignment begins when creation reserves the session and
ends only when discard or delete completes. Stop and rename preserve it.

Discard retains the branch as an ordinary unassigned project ref. A later
session may select that ref as committed source, but it receives a new UUID and
must create a distinct new branch. An unassigned ref still occupies its Git
name in that project until it is renamed or deleted.

### No scratch or active base state

There are no scratch sessions. Creation always selects committed source and
creates a new branch immediately. If the user supplies no branch name, P
suggests a timestamp and adds a collision suffix when necessary.

The selected commit is creation input, not durable `session.base_commit`
state. Once the new branch exists, Git ancestry and its current tip are the
source authority.

## Authorities

| Fact | Authority | SQLite's role |
|---|---|---|
| Session UUID, branch assignment, intended operation | SQLite | authoritative |
| Commits, branch existence, branch tip | P bare Git repository | indexes expected ref and observed tip |
| Workspace files, local refs, processes, runtime existence | Runtime backend and workspace Git repository | records locator and last observation |
| Environment bytes | Nix/container artifact store | records identity and lease |
| P Git write permission | P Git authorization using current SQLite assignment | records principal and revocation state |
| Bifrost policy and key validity | Bifrost | records key ID, protected token, and cleanup state |
| Attachment presence | Live host RPC connection | not persisted |
| Agent condition | Session observability reducer | stores only its defined latest unattended value |

Reconciliation never replaces a fresh authority query with a cached SQLite
observation. Conversely, discovering machinery does not invent a session row:
an unassigned labeled runtime is an orphan until its UUID is matched to an
active session or abandonment record.

## Registry state, runtime condition, and operations

P keeps three different concepts separate.

### Registry state

The session row has one coarse registry state:

| State | Meaning |
|---|---|
| `creating` | Identity and branch are reserved, but creation has not reached ready. |
| `established` | Creation completed; runtime condition determines whether it is usable. |
| `removing` | An authorized discard/delete is completing forward. |

There is no terminal registry state. Completed discard/delete removes the
session row. Minimal cleanup or orphan tombstones are separate records and do
not re-create a session.

### Runtime condition

Runtime condition reports the most recent reconciled runtime fact described in
[session-observability.md](session-observability.md#runtime-condition). It is a
projection, not a second independently mutable lifecycle field: `creating` and
`removing` come directly from registry state; for an established session,
backend inspection yields `running`, `stopped`, `missing`, or `unreachable`.
A running backend whose activation failed may therefore be reported as running
while the failed start/create operation explains why the session is not
attachable.

### Operation record

Every durable mutating lifecycle request has an operation ID and idempotency
key. Its persisted record contains at least:

```text
operation ID and idempotency key
session UUID, when allocated
project path
operation kind and requested outcome
current phase and whether its commit point was crossed
expected Git ref names and object IDs
expected runtime locator and backend labels
confirmation fingerprint, when destructive
bounded diagnostic and last error
created and updated times
```

Operation status is `running`, `blocked`, `failed`, or `completed`. A failed
operation is retained for inspection and retry; it does not manufacture a
runtime condition or agent condition.

The same idempotency key with the same request returns the existing operation.
Reusing it with different input is rejected.

Attach and detach are the live exception: the RPC connection and its attachment
lease provide their identity, so P does not journal them as durable operations.
Any start required by attach is still a normal persisted start operation.

## Concurrency and quiescence

At most one mutating lifecycle operation may hold a session's lifecycle lock.
Creation acquires the target project/ref reservation before a session lock
exists. Rename and ref deletion also acquire a project/ref lock.

Read-only inspection remains available during operations and reports the
operation and phase. These actions are refused while a conflicting lifecycle
operation is incomplete:

| Action | Conflict rule |
|---|---|
| Start or attach | Refused during create completion, rename, removal, repair mutation, or abandonment. |
| Rename | Refused during start, stop, removal, repair mutation, or abandonment. |
| Stop | Refused during create completion, rename, removal, repair mutation, or abandonment. |
| Publish | Refused unless the session is established and its branch assignment is stable. |
| Discard/delete | Refused during any other mutation and while an attachment lease exists. |

Git ref guards are independent from lifecycle locks. During rename and removal,
the P Git server rejects updates to affected refs even from the otherwise valid
session principal. Guard intent is persisted with the operation and restored
before the Git listener accepts connections after daemon restart.

An operation that must inspect or mutate the workspace uses backend
quiescence:

- a stopped runtime is already quiescent;
- a running runtime is paused without discarding processes;
- attachment streams may stall during a short pause but their leases remain;
- the runtime is resumed only after verification and ref guards are released;
  and
- a backend without safe pause support permits workspace mutation only while
  stopped.

If the daemon fails while a runtime is paused, reconciliation sees the
persisted phase and either completes the operation or safely resumes the
runtime. P never relies on an in-memory `defer` to unpause it.

Quiescence does not authorize the daemon to run Git inside its ambient host
context. The backend uses the closed, non-activating workspace interface in
[runtime and isolation](runtime-isolation.md#non-activating-workspace-access).
A backend that cannot provide that isolation cannot implement V1 rename,
destructive preflight, or the corresponding repairs.

## Create

### Create meaning and preconditions

Create allocates a new UUID, creates a new session-owned branch at committed
source, prepares its environment and credentials, assembles one runtime, and
makes it attachable.

The request names:

- one existing project;
- committed source reachable from the P repository, a refreshed origin ref, or
  a registered source location;
- a new valid Git branch name, or no name to request a timestamp suggestion;
  and
- no session-specific grant override.

The target `refs/heads/<branch>` must not exist in that project. Active session
names need no separate global uniqueness check. When the selected source is a
named branch, the new session branch name must differ from that source name;
selecting `main` therefore creates another branch at `main`'s commit rather
than assigning `main` itself. Dirty checkout state is never creation input.

V1 has no repository-controlled P configuration. Creation snapshots the
trusted project-scoped P configuration and evaluates only the ordinary Nix
devShell from the selected commit. The default devShell is used when present;
otherwise the substrate-only environment applies.

### Create phases

| Phase | Required result |
|---|---|
| `reserved` | Persist operation, UUID, project, branch assignment, and trusted project-configuration snapshot. |
| `source-ready` | Resolve/import the committed source and record its exact object ID. |
| `branch-created` | Atomically create the absent P branch at that object ID. This is creation's commit point. |
| `environment-ready` | Acquire a verified environment artifact and lease. |
| `principals-ready` | Create/activate the session Git principal and optional Bifrost key. |
| `runtime-created` | Create exactly one labeled runtime and record its locator. |
| `workspace-ready` | Create a standalone clone of the assigned branch with its P upstream and install only session-scoped endpoints. |
| `activation-ready` | Activate the environment successfully inside the runtime. |
| `interactive-ready` | Prepare the configured interactive host and command. |
| `established` | Verify branch, workspace, runtime, credentials, and required capabilities; set registry state established. |

Environment realization may be shared with another operation, but the session's
artifact lease and later phases remain specific to its UUID.

### Failure, cancellation, and retry

Before `branch-created`, cancellation may remove the reservation after imported
temporary source material is reconciled. No session branch, environment lease,
or runtime is lost.

After `branch-created`, P never silently deletes the branch or session record.
A failed creation remains visible in `creating` state with its failed phase.
The user may retry the same operation, discard while retaining the branch, or
delete after the normal loss confirmation.

Reconciliation verifies each recorded result before continuing. It reuses a
valid artifact, principal, or correctly labeled partial runtime and removes a
conflicting partial resource only when removal cannot destroy user-created
runtime state. Ambiguous runtime creation blocks for repair rather than
creating a second runtime.

Create does not implicitly open an attachment. The TUI may implement
“create and enter” as create followed by attach after creation reaches
`established`.

## Start

### Meaning

Start makes an established stopped runtime ready again. It reuses the same
UUID, branch assignment, runtime locator, writable filesystem, workspace,
home, credentials, and recorded environment artifact.

Start does not reevaluate the branch's current devShell or reload changed host
project configuration. V1 does not apply a different environment or grant
snapshot to an existing session; any later runtime-recreation operation needs
its own lifecycle contract rather than occurring through stop/start.

Stopping a container terminates its processes. Start therefore reruns the
runtime launcher, environment activation, shell hook, and interactive-host
preparation. P does not promise that tmux, an agent process, terminal state, or
in-memory conversation survives stop. Tool-owned state stored in the retained
filesystem may allow the tool itself to resume.

### Rules

- Starting an already ready runtime succeeds idempotently.
- A stopped runtime must be reachable and match the session UUID labels.
- `missing` requires repair/recreation; start does not silently construct a
  replacement.
- `unreachable` blocks start because P cannot exclude a duplicate runtime.
- Missing recorded artifacts or credentials block start and produce a repair
  plan; P does not substitute a new environment silently.
- Startup is attachable only after activation and interactive preparation
  succeed.

An interrupted start is reconciled by inspecting the backend and readiness
marker. If the backend started but activation failed, the operation reports
that phase and permits retry or stop; it does not claim the session is ready.

V1 has no separate restart operation. Restart is an explicit stop followed by
start.

## Attach and detach

Attach is a host RPC decision followed by client-side execution of a structured
`AttachSpec`; terminal bytes do not pass through lifecycle JSON-RPC.

The daemon:

1. verifies an established session with no conflicting operation;
2. starts it and waits for readiness when it is stopped;
3. verifies the current runtime readiness marker, refusing a running runtime
   whose latest activation/preparation failed;
4. asks the interactive host and runtime backend for fixed attachment argv;
5. creates a connection-bound attachment lease;
6. clears the latest unattended condition only on the zero-to-one lease
   transition; and
7. returns the structured spec to the still-connected client.

A failed start or spec construction creates no lease and does not clear status.
Multiple attachments are allowed when the interactive host supports them.

Client exit or RPC transport closure ends its lease. Detach does not stop the
runtime or interactive command. Daemon restart drops leases; clients reconnect
through the ordinary attach flow. Persistence after detach belongs to the
interactive host: tmux provides it, while `direct` ends its command with the
attachment.

## Rename

### Rename meaning and preconditions

Rename changes the actual session-owned Git branch while preserving the UUID,
project, runtime, workspace contents, environment artifact, credentials,
attachment leases, and Bifrost principal. It never renames or deletes an origin
ref.

The new branch name must pass Git validation and be absent from the project's
`refs/heads/*`. The session must be established, its current P ref and runtime
must be reachable, and its workspace must contain the assigned local branch.
P does not reset, clean, commit, or switch unrelated work to make rename pass.

### Rename phases

Under the lifecycle and project/ref locks:

| Phase | Required result |
|---|---|
| `reserved` | Persist old/new refs and expected old P tip; reserve the new name. |
| `guarded` | Reject pushes to both old and new refs. |
| `quiesced` | Pause a running runtime, or verify it is stopped. |
| `new-ref-created` | Atomically create the new P ref at the expected old P tip. This is rename's commit point. |
| `workspace-renamed` | Rename the local branch without changing its tip or files; update upstream/push configuration. |
| `assignment-updated` | Change SQLite and server-side principal policy to the new branch address. |
| `old-ref-deleted` | Delete the old P ref with an expected-old-tip check only after the new mapping verifies. |
| `completed` | Release guards and reservation; resume a runtime paused by this operation. |

The workspace branch may contain commits ahead of the P ref. Renaming preserves
them locally; the next permitted push fast-forwards the new P ref. The operation
records both server and workspace tips so verification does not mistake this
ordinary ahead state for corruption.

### Rename recovery

Before `new-ref-created`, reconciliation may release the reservation and retain
the old mapping. At and after `new-ref-created`, it completes forward to the new
name. The guards remain active and start/attach remain blocked until the new
mapping verifies or explicit repair resolves an authority conflict.

If another process altered a guarded ref or the workspace mapping despite
quiescence, P does not guess. It leaves the runtime stopped/paused as safely as
possible and presents the observed refs and repair choices.

Publishing under the new name is a later explicit origin operation.

## Stop

Stop halts execution without ending the session or branch assignment. It
retains:

- UUID and established registry row;
- runtime machinery and writable filesystem;
- workspace, home, runtime-owned data, and artifact lease;
- session Git and Bifrost principals; and
- latest unattended agent condition.

It terminates runtime processes, including tmux and interactive agents. A live
attachment makes ordinary stop fail with an attachment diagnostic; the user
must detach first. This prevents one client from unexpectedly closing another
client's terminal.

Stop requests the backend's bounded graceful shutdown and uses its documented
forced termination after the timeout. It never removes the runtime. Already
stopped succeeds idempotently. Missing or unreachable machinery is not reported
as stopped and is handled through repair.

An interrupted stop is resolved by backend inspection: running means retry is
available; stopped means complete. P does not infer success from RPC connection
loss.

## Destructive preflight

Discard and delete require a fresh loss report. Inspection does not activate
the environment, run the shell hook, prepare the interactive host, or execute
repository-controlled commands.

### Runtime-local loss

For a reachable runtime, P reports:

- staged and unstaged tracked changes in every runtime-owned Git worktree known
  to the session workspace repository;
- untracked files;
- ignored-file presence with bounded count and size summaries;
- local refs and commits not reachable from the assigned P branch or another
  retained P ref;
- the fact that runtime processes, writable layer, home, temporary files, and
  runtime-owned volumes will be removed; and
- external project mounts that are not deleted by P, while noting that writes
  already made through them remain external state; a worktree located on such
  a mount is identified as external rather than mounted into the inspection
  helper.

P does not promise to discover unrelated nested repositories or arbitrary data
semantics. It clearly labels the runtime filesystem outside Git as disposable
even when a bounded scan finds nothing notable.

For a running unattached runtime, P pauses it, inspects it, records a workspace
fingerprint, and resumes it while the user reviews the report. Execution
revalidates under quiescence. If the assigned P tip, workspace refs/status, or
runtime locator changed, the confirmation is stale and P returns a new report.

If the backend authoritatively reports the runtime missing, P states that
runtime-local state is already unavailable and may continue cleanup after
explicit acknowledgement. If the backend is unreachable, normal discard and
delete are blocked because loss cannot be determined; abandonment is the only
override.

### Branch loss

Delete additionally reports:

- the exact assigned P ref and tip to be deleted;
- commits that would cease to be reachable from any retained P ref;
- refreshed origin refs that contain the tip or otherwise retain those commits;
  and
- `unknown` origin preservation when refresh fails.

An origin comparison is evidence, not permission and not a publication ledger.
Unknown upstream preservation does not silently block an explicitly confirmed
delete, but it is presented prominently. Local-only projects omit origin
comparison.

### Confirmation token

The preflight result produces a short-lived confirmation token bound to:

```text
session UUID and operation kind
project and assigned ref
P ref object ID
runtime locator and observed state
workspace fingerprint or explicit missing-runtime acknowledgement
origin refresh generation/status when relevant
```

Execution revalidates this token under the lifecycle lock. A mismatch never
falls through to a generic “are you sure”; it requires review of the updated
facts.

## Discard

Discard ends the session and removes its runtime while retaining the current P
branch as an ordinary unassigned source ref.

After a valid destructive confirmation, discard completes forward through:

1. guard the assigned P ref against further writes;
2. quiesce and revalidate the runtime/workspace fingerprint;
3. if the confirmation is still valid, mark the registry and operation
   `removing` and disable the session's P Git and session-RPC authority;
4. persist the runtime-removal commit point, then request backend removal;
5. request Bifrost-key revocation when present and remove local secret copies;
6. release the environment-artifact lease and remove other session credentials;
7. verify that the P branch still exists at the guarded expected tip;
8. end the UUID-to-branch assignment and remove the session row; and
9. release the guard and leave the branch visible as an unassigned project
   ref.

A stale confirmation discovered in step 2 releases the guard, resumes a runtime
paused by the preflight, and leaves the established session and authorities
unchanged.

Once the runtime-removal commit point is persisted, discard never recreates the
runtime to roll back. Reconciliation completes cleanup forward. Failure to
revoke an external key creates a cleanup tombstone; it does not keep a removed
runtime or session alive.

## Delete

Delete performs the same runtime and credential removal as discard and also
deletes the assigned P branch.

After runtime removal verifies, P:

1. checks that the guarded branch still has the confirmed object ID;
2. atomically deletes that ref with an expected-old-value check;
3. ends the assignment and removes the session row; and
4. retains only required cleanup/orphan tombstones and bounded operation
   diagnostics.

Delete never deletes or renames an origin ref, external mount contents, shared
environment bytes still leased elsewhere, or another P ref containing the same
commits.

Once runtime removal begins, delete completes forward even if branch deletion
temporarily fails. The session remains `removing`, its Git principal stays
disabled, and reconciliation retries or requests repair; it does not restore a
runtime around a branch the user authorized P to delete.

## Reconciliation and repair

### Automatic reconciliation

The daemon reconciles at startup, after backend event-stream gaps, and when an
operation loses contact with an authority. It obtains the relevant locks and:

- queries Git refs and expected object IDs;
- lists/inspects runtime labels and locators;
- checks artifact presence and leases;
- validates local Git authorization state;
- checks Bifrost principal state when required; and
- resumes an already authorized operation according to its recorded phase.

Automatic reconciliation may finish an operation the user already authorized,
restore a missing local authorization record without widening it, or update an
observed condition. It never deletes an unconfirmed branch, removes a runtime
outside a committed removal operation, resets a workspace, or creates a second
runtime while the original may exist.

### Repair

Repair is an explicit mutation plan produced from current diagnostics. It is
not a generic “make it work” command and accepts no arbitrary shell command.

Supported v1 repair shapes are:

| Inconsistency | Repair behavior |
|---|---|
| Incomplete authorized operation | Resume its defined forward/rollback path. |
| Stale runtime locator, one matching labeled runtime | Relink after UUID/project verification. |
| Missing runtime, assigned P branch intact | Recreate one runtime for the same UUID from the current P branch and recorded environment/configuration; disclose that prior runtime-local state is unavailable. |
| Runtime exists, assigned P ref missing, assigned local branch intact | Offer guarded restoration of the P ref at the inspected local tip; never do it silently. |
| Missing/revoked session Git principal | Rotate/reissue the UUID-scoped principal and update only its runtime. |
| Missing Bifrost principal for a model-enabled session | Re-establish the UUID-scoped principal under the recorded project policy. |
| Workspace branch/upstream mismatch | Report exact refs and require a targeted plan; never reset, clean, or force-push automatically. |
| Session row has neither runtime nor branch | Offer removal of the unrecoverable registry record after confirmation. |

Repairing a missing runtime uses committed P-branch state. It cannot recover
uncommitted files, unpushed commits, processes, terminal state, or conversations
that existed only in the missing runtime.

## Abandonment and orphans

Abandonment is the explicit override for an unreachable backend. It means P
cannot inspect or remove the expected runtime and the user authorizes control-
plane cleanup despite unknown runtime-local loss.

The confirmation names:

- last known runtime locator and labels;
- unknown workspace/process/filesystem loss;
- branch retention or deletion according to the selected discard/delete
  outcome;
- immediately disabled local P authorities; and
- external credential revocation that may remain pending.

P first persists an abandonment tombstone and disables P Git/session-RPC
authority. It then completes the requested discard or delete for authorities
it can reach. The tombstone contains no secret and retains only the UUID,
project, last branch, backend/locator labels, requested outcome, pending
credential identifiers, and timestamps needed for recognition and cleanup.

When the backend becomes reachable, P matches machinery by instance and session
UUID labels. A matching runtime is never adopted by a new session. P attempts
to stop it as containment without deleting its files, marks it orphaned, and
presents explicit inspect/remove cleanup. If stopping fails, the overview keeps
an urgent orphan warning.

The tombstone remains until:

- the backend authoritatively confirms the runtime absent;
- every external principal is revoked or confirmed absent;
- the environment-artifact lease held for possibly running machinery can be
  released safely;
- any selected branch deletion completed.

An explicit “forget tombstone” action is allowed only with a warning that P can
no longer recognize or revoke later reappearing machinery and will release the
remaining bookkeeping and artifact lease. Forgetting may therefore break an
unknown runtime that still uses that artifact. Age alone never expires a
tombstone.

## Credential and artifact cleanup

Local P authorization follows the SQLite assignment. Marking removal or
abandonment immediately prevents the session key from pushing even if a private
key file survives in unreachable machinery.

Bifrost revocation is idempotent by session UUID/key ID. If Bifrost is
unavailable, P removes the token from active session state after runtime
removal, retains only the protected revocation handle required by Bifrost, and
retries through a cleanup tombstone. Upstream provider credentials never enter
this lifecycle.

Environment artifacts are immutable shared inputs. Create acquires a lease;
discard/delete releases it only after runtime removal. Stop, start, rename, and
attach preserve it. Releasing a lease does not itself collect shared bytes;
artifact collection remains an explicit operation defined by environment
building. Abandonment retains the lease in its tombstone until the runtime is
confirmed absent or the user explicitly accepts the consequences of forgetting
the tombstone.

## Operation recovery summary

| Operation | Commit point | Before commit point | At/after commit point |
|---|---|---|---|
| Create | New P branch created | Cancel/clean reservation when safe | Retain creating session and branch; retry, discard, or delete explicitly |
| Start | Backend start requested after intent persisted | Remain stopped | Inspect backend/readiness; finish or report failed phase |
| Attach | Live lease created | No attachment/status clear | Lease follows live connection; detach on closure |
| Rename | New P ref created | Release reservation and retain old mapping | Complete forward to new ref under guards |
| Stop | Backend stop requested after intent persisted | Remain running | Inspect backend; complete stopped or expose retry |
| Discard | Runtime removal committed | Revalidate or cancel without loss | Complete runtime/credential cleanup; retain branch |
| Delete | Runtime removal committed | Revalidate or cancel without loss | Complete cleanup and confirmed branch deletion |
| Repair | Plan-specific mutation persisted | Reinspect/cancel | Complete only the displayed repair plan |
| Abandon | Tombstone persisted and local authority disabled | No control-plane deletion | Complete reachable cleanup; retain tombstone until resolved |

No client blindly retries an ambiguous durable mutation with a new
idempotency key. It queries the existing operation and lets reconciliation
determine the observed result.

## RPC and presentation

Persisted lifecycle methods evolve with implementation, but each exposes the
same concepts:

- operation and idempotency IDs;
- current phase and bounded diagnostic;
- registry state, observed runtime condition, project, branch, and UUID;
- destructive preflight facts and confirmation fingerprint; and
- explicit repair or retry actions permitted by the current facts.

Attach instead returns its connection-bound lease and structured `AttachSpec`;
detach is represented by closing that live connection.

The TUI does not implement lifecycle rules. It presents daemon-provided plans
and invokes the same RPC surface available to `p api`.

Transitional and failed operations remain visible. P never collapses
`unreachable`, `missing`, failed activation, pending credential revocation, and
orphaned machinery into a generic stopped or deleted label.

## V1 boundary

V1 includes:

- committed-source creation with no scratch state;
- one UUID-to-project/branch assignment and one runtime per session;
- start, attach/detach, transactional rename, and stop;
- discard, delete, destructive preflight, repair, and abandonment;
- persisted operations, per-session/ref locking, backend quiescence, and
  restart reconciliation;
- local Git-principal revocation and optional Bifrost-principal cleanup; and
- orphan recognition without automatic age-based deletion.

V1 does not include runtime migration, branch-specific grants, automatic
reclamation, service lifecycle, attempts, checks, session cloning, or recovery
of state that never reached a retained Git ref or external mount.

## Acceptance criteria

The lifecycle design is implemented when integration tests prove:

1. every crash point in create, rename, discard, and delete converges to the
   documented result without duplicate runtimes or silent ref loss;
2. `(project, branch)` uniqueness allows equal branch names in different
   projects and rejects collisions in one project;
3. start preserves writable runtime state but does not claim process or tmux
   continuity across stop;
4. failed attach does not create a lease or clear unattended status;
5. rename preserves UUID, workspace files, local-ahead commits, credentials,
   attachments, and any initially running processes while changing both Git
   refs safely;
6. destructive confirmation becomes stale when Git or workspace facts change;
7. discard retains an unassigned branch and delete removes only the confirmed
   P ref;
8. missing and unreachable runtimes take different cleanup paths;
9. repair never creates a duplicate runtime, resets a workspace, or widens a
   credential;
10. abandonment recognizes a later runtime by UUID, prevents adoption, and
    retains its tombstone until runtime and credential cleanup are resolved;
11. daemon restart drops only live attachment leases and resumes persisted
    operation reconciliation; and
12. no automatic cleanup deletes a branch, runtime, or orphan record based only
    on age.
