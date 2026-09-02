# P — session lifecycle

How P creates, starts, attaches, renames, stops, discards, deletes, repairs,
and abandons sessions without confusing intended state with Git, runtime, or
credential facts.

> **Status: design.** This document is authoritative for session identity,
> lifecycle operations, concurrency, destructive-loss analysis, and recovery.
> [communication-boundaries.md](communication-boundaries.md) owns what crosses
> Git, RPC, SSH, and attachment channels;
> [environment-building.md](environment-building.md) owns cached Incus
> environment images and activation; [runtime-isolation.md](runtime-isolation.md)
> owns Incus placement, grants, storage, systemd hosting, attachment execution,
> metadata, and isolated workspace access; and
> [session-observability.md](session-observability.md) owns agent-event
> reduction and presentation of session and policy condition.

## Contents

- [Purpose](#purpose)
- [Identity and retained state](#identity-and-retained-state)
- [Authorities](#authorities)
- [Registry state, session condition, and operations](#registry-state-session-condition-and-operations)
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
- [Credential and image-cache cleanup](#credential-and-image-cache-cleanup)
- [Operation recovery summary](#operation-recovery-summary)
- [RPC and presentation](#rpc-and-presentation)
- [MVP boundary](#mvp-boundary)
- [Acceptance criteria](#acceptance-criteria)

## Purpose

A session spans authorities with different failure behavior:

- SQLite records P's identity, intent, and progress;
- Git owns commits and refs;
- Incus owns instances, runtime operations, writable roots, and cached images;
- P's Git authorization and activated external-service plugins own their
  provider-specific credentials; and
- live RPC connections own attachment presence.

No transaction can atomically commit across all of them. P persists workflow
intent when an ordered change crosses authorities. A mutation owned by one
queryable authority—such as Incus start or stop—uses that authority's
operation and current state rather than a duplicate P workflow.

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

Namespaces have no separate lifecycle or authorization meaning in MVP. Within
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

The branch name is the session's human-facing name. It is unique with its
project path, not globally across the instance, and there is no separate
mutable display name. Rename changes the branch component while preserving the
UUID and project.

A session owns at most one Incus instance. Its deterministic Incus name and
locator are runtime machinery, not session identity. Stable Incus metadata
carries the P instance, project path, and session UUID so reconciliation can
find machinery when a stored locator is missing or stale.

### Assignment lifetime

The UUID-to-branch assignment begins when creation reserves the session and
ends only when discard or delete completes. Stop and rename preserve it.

Discard retains the branch as an ordinary retained project branch. A later
session may select that branch as committed source, but it receives a new UUID and
must create a distinct new branch. A retained branch still occupies its Git
name in that project until it is renamed or deleted.

### Committed source and blank bootstrap

There are no scratch sessions. Creation always selects committed source and
creates a new branch immediately. If the user supplies no branch name, P
suggests a timestamp and adds a collision suffix when necessary.

The sole exception is the first session of a blank or successfully contacted
empty-origin project. [Project lifecycle](project-lifecycle.md#blank) reserves
that session on unborn `main`; its first commit and push create the assigned
ref. It is an explicit project bootstrap, not an untracked scratch mode.

The selected commit is creation input, not durable `session.base_commit`
state. Once the new branch exists, Git ancestry and its current tip are the
source authority.

## Authorities

| Fact | Authority | SQLite's role |
|---|---|---|
| Session UUID, branch assignment, intended operation | SQLite | authoritative |
| Commits, branch existence, branch tip | P bare Git repository | indexes expected ref and observed tip |
| Workspace files and local refs | Incus instance root | records no duplicate file state |
| Instance existence, state, root storage, and runtime operation | Incus | records project and deterministic instance name |
| Cached environment-image bytes | Incus image store | indexes project-scoped environment key and fingerprint |
| P Git write permission | P Git authorization using current SQLite assignment | records principal and revocation state |
| Post-MVP external-service principal validity | Configured service | records only the protected binding and cleanup state required by its plugin contract |
| Interactive-host readiness and failure | `p-interactive.service` and persistent system journal | records only bounded operation/last-start diagnostics |
| Pending/confirmed attachment presence | Live host RPC connection | not persisted |
| Agent condition | Session observability reducer | stores only its defined latest unattended value |

Reconciliation never replaces a fresh authority query with a cached SQLite
observation. Conversely, discovering an Incus instance does not invent a
session row: unmatched P metadata is orphan machinery until its UUID is
matched to an active session or abandonment record.

## Registry state, session condition, and operations

P keeps three different concepts separate.

### Registry state

The session row has one coarse registry state:

| State | Meaning |
|---|---|
| `creating` | Identity and branch are reserved, but creation has not reached ready. |
| `established` | Creation completed; reduced session condition determines whether it is usable. |
| `removing` | Internal recovery state: an authorized discard/delete is ensuring its action-specific result. It is never the public session condition. |

There is no terminal registry state. Completed discard/delete removes the
session row. Minimal cleanup or orphan tombstones are separate records and do
not re-create a session.

### Session condition

The public session condition is the reconciled projection defined in
[session observability](session-observability.md#session-condition). Registry
state supplies `creating`, `discarding`, and `deleting`; Incus plus current
`p-interactive.service` inspection supplies `starting`, `ready`, `stopped`,
`missing`, or `unreachable` for an established session. It is not a second
independently mutable lifecycle field.

Activation or persistent-host failure shuts the container down, so P does not
retain a separate `running/not_ready` condition. The stopped session may carry
a bounded latest Start diagnostic without manufacturing another lifecycle
state.

### Operation record

Mutating RPC requests may share one generic operation mechanism for identity,
locking, progress, and bounded diagnostics. Only cross-authority workflows
persist resumable P phases. Their record contains at least:

```text
operation ID and idempotency key
session UUID, when allocated
project path
operation kind and requested outcome
current P workflow phase and commit-point status, when applicable
expected Git ref names and object IDs
expected Incus project, instance name, image fingerprint, and P metadata
confirmation fingerprint, when destructive
bounded diagnostic and last error
created and updated times
```

Operation status is `running`, `blocked`, `failed`, `unknown`, `superseded`, or
`completed`. An unresolved creation remains non-terminal `blocked` while its
session references it. Completed, safely cancelled, and superseded results
have bounded retention; they do not become permanent domain history or
manufacture session/agent condition.

The same idempotency key with the same request returns the existing operation.
Reusing it with different input is rejected.

Attach and detach are the live exception: the RPC connection and its pending
token or confirmed lease provide their identity, so P does not journal them as
durable operations.
Any start required by attach is still a normal Incus-owned start request; P may
expose its generic operation handle without persisting a second phase machine.

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
| Discard/delete | Refused during any other mutation and while a pending or confirmed attachment exists. |

Git ref guards are independent from lifecycle locks. During rename and removal,
the P Git server rejects updates to affected refs even from the otherwise valid
session principal. Guard intent is persisted with the operation and restored
before the Git listener accepts connections after daemon restart.

An operation that must inspect or mutate the workspace uses Incus
quiescence:

- a stopped runtime is already quiescent;
- a running runtime is paused without discarding processes;
- attachment streams may stall during a short pause but their leases remain;
- the runtime is resumed only after verification and ref guards are released;
  and
- a future backend without safe pause support may permit workspace mutation
  only while stopped.

If the daemon fails while a runtime is paused, reconciliation sees the
persisted phase and either completes the operation or safely resumes the
runtime. P never relies on an in-memory `defer` to unpause it.

Quiescence does not authorize the daemon to run Git inside its ambient host
context. The Incus backend uses the closed, non-activating workspace interface in
[runtime and isolation](runtime-isolation.md#non-activating-workspace-access).
A future backend that cannot provide that isolation cannot implement rename,
destructive preflight, or the corresponding repairs.

## Create

### Create meaning and preconditions

Create allocates a new UUID, creates a new session-owned branch at committed
source, prepares its environment and credentials, assembles one runtime, and
makes it attachable.

The request names:

- one existing project;
- committed source reachable from the P repository or a freshly observed
  origin ref;
- a new valid Git branch name, or no name to request a timestamp suggestion;
  and
- no session-specific grant override.

The target `refs/heads/<branch>` must not exist in that project. When the
selected source is a
named branch, the new session branch name must differ from that source name;
selecting `main` therefore creates another branch at `main`'s commit rather
than assigning `main` itself. Dirty checkout state is never creation input.

The unborn-`main` bootstrap is created only as part of explicit blank/empty-
origin project creation. It has no source object ID, uses the P base image, and
may create only the already reserved `refs/heads/main` on its first push.

MVP has no repository-controlled P configuration. Creation snapshots the
trusted project-scoped P configuration and evaluates only the ordinary Nix
devShell from the selected commit. The default devShell is used when present;
otherwise the substrate-only environment applies.

### Create phases

| Phase | Required result |
|---|---|
| `reserved` | Persist operation, UUID, project, branch assignment, and trusted project-configuration snapshot. |
| `source-ready` | Resolve/import the committed source and record its exact object ID. |
| `branch-created` | Atomically create the absent P branch at that object ID. This is creation's commit point. |
| `environment-ready` | Resolve or build a verified Incus environment image and record its fingerprint. |
| `principals-ready` | Create/activate the session Git principal and any bindings required by the selected MVP plugins. |
| `runtime-created` | Create exactly one Incus instance named for the UUID from that image and record its locator. |
| `workspace-ready` | Create a standalone clone of the assigned branch with its P upstream and install only session-scoped endpoints. |
| `systemd-ready` | Start the container; require activation and `p-interactive.service` readiness. Failure records diagnostics and returns the container to stopped. |
| `established` | Verify branch, workspace, runtime, credentials, and required capabilities; set registry state established. |

Environment realization may be shared with another operation. Once published,
the immutable image is an Incus cache entry; the session's instance and later
phases remain specific to its UUID.

### Failure, cancellation, and retry

Before `branch-created`, cancellation may remove the reservation after imported
temporary source material is reconciled. No session branch or runtime is lost.

After `branch-created`, P never silently loses the request, session identity,
or branch. A failed creation remains visible in `creating` state with one
durable `blocked` operation. Systemd has already stopped a container whose
activation or host startup failed.

Retry means the exact immutable request: the same operation, UUID, branch,
source commit, normalized policy snapshot, and idempotency key. It verifies
the reserved branch is unchanged, ensures partial runtime/credential resources
are absent or safe to reuse, and reconstructs the desired result without
creating retry chains. Repeating Retry cannot accumulate instances,
principals, operations, or refs.

If repository content or settings must change, **Try again with changes** is
one replacement action, not Retry. It records a new request, UUID, source,
policy snapshot, operation ID, and idempotency key; explicitly supersedes the
failed creation; cleans its verified provisional resources and unchanged
branch; and may reuse the desired branch name. Unexpected commits, workspace
changes, or resource identity invoke the integrated loss review rather than
silently deleting them. The user does not perform preparatory Discard/Delete
bookkeeping.

Reconciliation verifies every observed result. It reuses a valid immutable
image and safely matching resources, treats absence as a clean rebuild point,
and blocks ambiguous ownership rather than creating duplicates.

Create does not implicitly open an attachment. The TUI may implement
“create and enter” as create followed by attach after creation reaches
`established`.

## Start

### Meaning

Start makes an established stopped Incus instance ready again. It reuses the
same UUID, branch assignment, instance, writable filesystem, workspace, home,
credentials, and parent environment image.

Start does not reevaluate the branch's current devShell or apply changed host
project configuration. It compares the immutable snapshot with current policy,
warns when it is `outdated`, and revalidates safety-critical mount/confinement
facts. `invalid` blocks Start. **Recreate with current policy** is a separate
guided Discard-and-Create operation: it preserves the old assigned branch as a
retained source, creates a new UUID and distinctly named assigned branch from
its captured tip, and applies the current policy snapshot. It never mutates the
old session in place or silently deletes its retained branch.

Stopping a container terminates its processes. Start therefore boots systemd,
reruns environment activation and the shell hook, and starts a fresh
`p-interactive.service`. P does not promise that a host process, agent process,
terminal state, or in-memory conversation survives stop. Tool-owned state in
the retained filesystem may let the tool resume itself.

### Rules

- Starting a runtime whose systemd persistent host is already active and
  attachable succeeds idempotently.
- A stopped runtime must be reachable and match the session UUID labels.
- `missing` requires repair/recreation; start does not silently construct a
  replacement.
- `unreachable` blocks start because P cannot exclude a duplicate runtime.
- Missing required Git/session credentials block start and produce a repair
  plan. Post-MVP external-service plugins define their own health and
  degradation evidence through their lifecycle contract. A missing cached
  parent image does not affect an existing instance; it matters only if the
  instance must be recreated.
- Attachability requires `p-interactive.service` active/ready and the fixed
  attachment command present under trusted ownership.

Incus owns the start operation and instance state; systemd owns activation,
host lifetime, and diagnostics. If contact is interrupted, P inspects both.
Activation or host failure shuts down the container and returns the session to
`stopped` with the bounded diagnostic; another Start retries normally. P does
not maintain a second durable Incus phase workflow or startup-generation
marker.

MVP has no separate restart operation. Restart is an explicit stop followed by
start.

## Attach and detach

Attach is a host RPC decision followed by client-side execution of a structured
`AttachSpec`; terminal bytes do not pass through lifecycle JSON-RPC.

The daemon, client, and trusted host attachment helper perform a two-stage
handshake:

1. verifies an established session with no conflicting operation;
2. starts it when stopped and waits for `p-interactive.service` readiness;
3. freshly verifies the systemd unit, trusted fixed attach command, and current
   Incus ownership;
4. asks the Incus backend for the fixed attachment argv;
5. creates a short-lived, one-use pending token bound to the session and
   authenticated host request and returns that token plus the structured spec;
6. the client invokes the trusted helper locally or through client-initiated
   SSH and transfers the token through its private control stream, never argv;
7. the helper opens the dedicated attachment RPC connection, establishes the
   interactive channel, and confirms the token on that connection; and
8. the daemon atomically promotes it to an active lease owned by the helper
   connection, increments confirmed attachment presence, and clears the latest
   unattended condition only on the confirmed zero-to-one transition.

A failed start or spec construction creates no token. A failed channel launch,
expired token, or connection closure before confirmation removes pending state.
None of those failures increments attachment presence, suppresses agent events,
or clears unattended status. Multiple confirmed attachments are allowed when
the interactive host supports them.

The trusted helper owns both the confirmed lease and the interactive channel;
the ordinary client is not relied upon for cleanup. On client exit, SIGKILL,
attachment-transport loss, or client-machine loss, the helper/transport binding
tears down the channel. While the daemon remains reachable, the helper retains
the confirmed lease until teardown completes and closes it afterward. If the
daemon restarts or the lease connection disappears first, the helper begins
teardown immediately and establishes no new lease or channel until teardown
finishes. MVP cannot re-register an existing channel.

Teardown ends only that temporary attach client. The systemd-owned persistent
host and configured command remain alive, so detach and session switching do
not stop the runtime. Clean or failed persistent-host exit, not attachment
loss, causes systemd to shut down the container.

## Rename

### Rename meaning and preconditions

Rename changes the actual session-owned Git branch while preserving the UUID,
project, Incus instance, workspace contents, environment image, credentials,
attachment leases, and selected plugin bindings. It never renames or deletes
an origin ref.

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
- workspace, home, private Nix additions, and other runtime-owned data;
- session Git principal and selected plugin bindings; and
- latest unattended agent condition.

It terminates runtime processes, including tmux and interactive agents. A
pending or confirmed attachment makes ordinary stop fail with an attachment
diagnostic; the pending token expires quickly, while an active user must detach
first. This prevents one client from unexpectedly closing another client's
terminal.

Stop requests Incus's bounded graceful shutdown and uses its documented forced
termination after the timeout. It never removes the instance. Already
stopped succeeds idempotently. Missing or unreachable machinery is not reported
as stopped and is handled through repair.

An interrupted stop is resolved from the Incus operation and current instance
state: running means retry is available; stopped means success. P does not
infer success from RPC connection loss or maintain a second durable stop
workflow.

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

If Incus authoritatively reports the runtime missing, P states that
runtime-local state is already unavailable and may continue cleanup after
explicit acknowledgement. If Incus is unreachable, normal discard and
delete are blocked because loss cannot be determined; abandonment is the only
override.

### Branch loss

Delete additionally reports:

- the exact assigned P ref and tip to be deleted;
- commits that would cease to be reachable from any retained P ref;
- freshly observed origin branches that contain the tip or otherwise retain
  those commits; and
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
```

Execution revalidates this token under the lifecycle lock. A mismatch never
falls through to a generic “are you sure”; it requires review of the updated
facts.

## Discard

Discard ends the session and removes its runtime while retaining the current P
branch as an ordinary retained source branch.

After a valid destructive confirmation, discard completes forward through:

1. guard the assigned P ref against further writes;
2. quiesce and revalidate the runtime/workspace fingerprint;
3. if the confirmation is still valid, mark the registry and operation
   `removing` and disable the session's P Git and session-RPC authority;
4. persist the runtime-removal commit point, then request Incus removal;
5. request each participating plugin's required credential revocation and
   remove P-owned local secret copies;
6. remove other session credentials;
7. verify that the P branch still exists at the guarded expected tip;
8. end the UUID-to-branch assignment and remove the session row; and
9. release the guard and leave the branch visible as a retained project
   branch.

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

Delete never deletes or renames an origin ref, external mount contents, cached
environment images, or another P ref containing the same
commits.

Once runtime removal begins, delete completes forward even if branch deletion
temporarily fails. The session remains `removing`, its Git principal stays
disabled, and reconciliation retries or requests repair; it does not restore a
runtime around a branch the user authorized P to delete.

## Reconciliation and repair

### Automatic reconciliation

The daemon reconciles at startup, after Incus event-stream gaps, and when an
operation loses contact with an authority. It obtains the relevant locks and:

- queries Git refs and expected object IDs;
- lists and inspects Incus instance metadata and locators;
- checks a cached image only when an operation needs to create an instance;
- validates local Git authorization state;
- asks participating plugins for required principal and resource state; and
- resumes an already authorized operation according to its recorded phase.

Automatic reconciliation may finish an operation the user already authorized,
restore a missing local authorization record without widening it, or update an
observed condition. It never deletes an unconfirmed branch, removes a runtime
outside a committed removal operation, resets a workspace, or creates a second
runtime while the original may exist.

### Repair

Repair is an explicit mutation plan produced from current diagnostics. It is
not a generic “make it work” command and accepts no arbitrary shell command.

Supported MVP repair shapes are:

| Inconsistency | Repair behavior |
|---|---|
| Incomplete authorized operation | Resume its defined forward/rollback path. |
| Stale runtime locator, one matching labeled runtime | Relink after UUID/project verification. |
| Missing runtime, assigned P branch intact | Reuse the recorded image when present. If absent, resolve the current committed P branch and show whether its environment identity differs before the user authorizes recreation for the same UUID; disclose that prior runtime-local state is unavailable. |
| Runtime exists, assigned P ref missing, assigned local branch intact | Offer guarded restoration of the P ref at the inspected local tip; never do it silently. |
| Missing/revoked session Git principal | Rotate/reissue the UUID-scoped principal and update only its runtime. |
| Workspace branch/upstream mismatch | Report exact refs and require a targeted plan; never reset, clean, or force-push automatically. |
| Session row has neither runtime nor branch | Offer removal of the unrecoverable registry record after confirmation. |

Repairing a missing runtime uses committed P-branch state. It cannot recover
uncommitted files, unpushed commits, processes, terminal state, or conversations
that existed only in the missing runtime. It also cannot promise the original
environment after both that instance and cached image are gone: the branch may
now define a different devShell. P presents that difference and never silently
substitutes the new image during start or automatic reconciliation.

## Abandonment and orphans

Abandonment is the explicit override for unreachable Incus. It means P
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
project, last branch, Incus project/instance metadata, requested outcome, pending
credential identifiers, and timestamps needed for recognition and cleanup.

When Incus becomes reachable, P matches machinery by instance and session
UUID labels. A matching runtime is never adopted by a new session. P attempts
to stop it as containment without deleting its files, marks it orphaned, and
presents explicit inspect/remove cleanup. If stopping fails, the overview keeps
an urgent orphan warning.

The tombstone remains until:

- Incus authoritatively confirms the runtime absent;
- every external principal is revoked or confirmed absent;
- any selected branch deletion has completed.

An explicit “forget tombstone” action is allowed only with a warning that P can
no longer recognize or revoke later reappearing machinery. Age alone never
expires a tombstone.

## Credential and image-cache cleanup

Local P authorization follows the SQLite assignment. Marking removal or
abandonment immediately prevents the session key from pushing even if a private
key file survives in unreachable machinery.

Every selected plugin's MVP lifecycle contract owns provider-specific
revocation and ensure-absent behavior. P coordinates those operations and, if
an external authority is unavailable, retains only the protected cleanup
handle required for idempotent retry. A post-MVP Bifrost plugin applies that
rule to its session virtual key. Upstream provider credentials never enter P's
lifecycle state.

Environment images are immutable Incus cache entries, not session-owned
resources. Session lifecycle never leases or deletes them. Cache inspection and
explicit deletion belong to environment building; a missing image is simply a
cache miss the next time P needs to create or repair an instance.

## Operation recovery summary

| Operation | Commit point | Before commit point | At/after commit point |
|---|---|---|---|
| Create | New P branch created, or unborn bootstrap reserved | Cancel/clean reservation when safe | Retain one blocked request; Retry cleans verified partial resources and reconstructs the same immutable request; changed input explicitly supersedes it with a new creation |
| Start | None in P; Incus/systemd own the operation | No duplicate P workflow | Inspect Incus and `p-interactive.service`; success is ready, failure returns the container to stopped and another Start retries |
| Attach | Pending token confirmed and promoted | Expire pending token without presence/status clear | Helper retains the lease through teardown while reachable; lease loss starts immediate transport-bound teardown before that helper can attach again |
| Rename | New P ref created | Release reservation and retain old mapping | Complete forward to new ref under guards |
| Stop | None in P; Incus owns the operation | No duplicate P workflow | Inspect Incus; report stopped or expose retry |
| Discard | Runtime removal committed | Revalidate or cancel without loss | Complete runtime/credential cleanup; retain branch |
| Delete | Runtime removal committed | Revalidate or cancel without loss | Complete cleanup and confirmed branch deletion |
| Repair | Plan-specific mutation persisted | Reinspect/cancel | Complete only the displayed repair plan |
| Abandon | Tombstone persisted and local authority disabled | No control-plane deletion | Complete reachable cleanup; retain tombstone until resolved |

No client blindly retries an ambiguous durable mutation with a new
idempotency key. It queries the existing operation and lets reconciliation
determine the observed result.

## RPC and presentation

Lifecycle methods evolve with implementation, but each exposes the
same concepts:

- operation and idempotency IDs;
- current phase and bounded diagnostic;
- registry state, observed session and policy condition, project, branch, and
  UUID;
- destructive preflight facts and confirmation fingerprint; and
- explicit repair or retry actions permitted by the current facts.

Attach returns a short-lived one-use pending token and structured `AttachSpec`.
The trusted host helper redeems it on a dedicated attachment RPC connection;
confirmation promotes that connection to the active lease, which the helper
retains until channel teardown completes while the daemon remains reachable.

The TUI does not implement lifecycle rules. It presents daemon-provided plans
and invokes the same RPC surface available to `p api`.

Transitional and failed operations remain visible. P never collapses
`unreachable`, `missing`, `discarding`, `deleting`, pending credential
revocation, and orphaned machinery into a generic stopped or deleted label.

## MVP boundary

MVP includes:

- committed-source creation plus the one unborn-main project bootstrap;
- one UUID-to-project/branch assignment and one runtime per session;
- start, attach/detach, transactional rename, and stop;
- discard, delete, destructive preflight, repair, and abandonment;
- persisted cross-authority operations, per-session/ref locking, Incus
  quiescence, and restart reconciliation;
- local Git-principal and selected-plugin cleanup; and
- orphan recognition without automatic age-based deletion.

MVP does not include runtime migration, branch-specific grants, automatic
reclamation, service lifecycle, attempts, checks, session cloning, or recovery
of state that never reached a retained Git ref or external mount.

## Acceptance criteria

The lifecycle design is implemented when integration tests prove:

1. every crash point in create, rename, discard, and delete converges to the
   documented result without duplicate runtimes or silent ref loss;
2. retrying creation verifies and cleans partial resources, reconstructs the
   same immutable request without duplicate identities, and a changed source
   uses an explicitly superseding new request;
3. `(project, branch)` uniqueness allows equal branch names in different
   projects and rejects collisions in one project;
4. start preserves writable runtime state but does not claim process or tmux
   continuity across stop;
5. failed attachment before confirmation does not increment presence or clear
   unattended status, while confirmation performs both atomically;
6. rename preserves UUID, workspace files, local-ahead commits, credentials,
   attachments, and any initially running processes while changing both Git
   refs safely;
7. destructive confirmation becomes stale when Git or workspace facts change;
8. discard preserves the assigned branch as retained and delete removes only
   the confirmed P ref;
9. missing and unreachable runtimes take different cleanup paths;
10. repair never creates a duplicate runtime, resets a workspace, or widens a
   credential;
11. abandonment recognizes a later runtime by UUID, prevents adoption, and
    retains its tombstone until runtime and credential cleanup are resolved;
12. daemon restart drops pending attachment tokens and active leases; each
    helper finishes temporary-client teardown while systemd preserves the
    persistent host, and operation reconciliation resumes;
13. abrupt client death or transport loss ends only its temporary attach
    client, while a reachable daemon retains attachment presence until teardown
    completes;
14. no automatic cleanup deletes a branch, runtime, or orphan record based only
    on age;
15. activation or persistent-host failure leaves the container stopped with a
    bounded inspectable diagnostic and an ordinary Start retry; and
16. host exit shuts down the container while detach or switching sessions does
    not.
