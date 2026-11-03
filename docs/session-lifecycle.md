# P — session lifecycle

How P creates, starts, attaches, renames, stops, discards, and deletes
sessions without confusing intended state with Git, runtime, or
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
- [Concurrency](#concurrency)
- [Create](#create)
- [Start](#start)
- [Attach and detach](#attach-and-detach)
- [Rename](#rename)
- [Stop](#stop)
- [Destructive preflight](#destructive-preflight)
- [Discard](#discard)
- [Delete](#delete)
- [Reconciliation](#reconciliation)
- [Abandonment and orphans (post-MVP)](#abandonment-and-orphans-post-mvp)
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
session may claim that same branch with a new UUID, or use it as the source for
a different new branch. A branch already assigned to any session, including a
stopped or failed session, cannot be assigned to a second session. Open or
start the existing session to continue its branch.

### Committed source and blank bootstrap

There are no scratch sessions. An ordinary session either uses an existing
unassigned branch at its committed tip, or creates a new branch from selected
committed source. Only the new-branch path asks for a source. If a new branch
name is omitted, P suggests a timestamp and adds a collision suffix when
necessary.

The sole exception is the first session of a blank or successfully contacted
empty-origin project. [Project lifecycle](project-lifecycle.md#blank) reserves
that session on unborn `main`; its first commit and push create the assigned
ref. It is an explicit project bootstrap, not an untracked scratch mode.

The existing branch's captured tip or the new branch's selected source commit
is creation input, not durable `session.base_commit` state. Git ancestry and
the assigned branch's current tip remain the source authority.

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
session row. Unmatched P metadata is unfamiliar machinery requiring manual
Incus investigation; P never silently adopts it or deletes uncertain resources.

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
session row. Unresolved cleanup retains the session identity and durable
operation state until its required absence checks succeed. Abandonment
tombstones are outside MVP.

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

## Concurrency

At most one mutating lifecycle operation may hold a session's lifecycle lock.
Creation acquires the target project/ref reservation before a session lock
exists. Rename and ref deletion also acquire a project/ref lock.

A project-service inventory or journal read does not take the lock, so it
never refuses a lifecycle request. A request that would take the lock while
such reads run waits for them to end, and no such read starts while the lock
is held. Several reads may run at once.

Read-only inspection remains available during operations and reports the
operation and phase. These actions are refused while a conflicting lifecycle
operation is incomplete:

| Action | Conflict rule |
|---|---|
| Start or attach | Refused during create completion, rename, or removal. |
| Rename | Refused unless the session is stopped, and during start, stop, or removal. |
| Stop | Refused during create completion, rename, or removal. |
| Publish | Refused unless the session is established and its branch assignment is stable. |
| Discard/delete | Refused during any other mutation and while a pending or confirmed attachment exists. A running session is accepted; P stops it as part of the removal. |

Git ref guards are independent from lifecycle locks. During rename and removal,
the P Git server rejects updates to affected refs even from the otherwise valid
session principal. Guard intent is persisted with the operation and restored
before the Git listener accepts connections after daemon restart.

P reads or changes a session's workspace only while the session is stopped,
and never freezes a running session. Rename therefore requires a stopped
session, and Start is refused while a rename runs. Removal does not read the
workspace at all: P states its loss by category
([destructive preflight](#destructive-preflight)) and stops a running runtime
itself after the removal commit point.

Workspace access does not authorize the daemon to run Git inside its ambient
host context. The Incus backend uses the closed, non-activating workspace
interface in
[runtime and isolation](runtime-isolation.md#non-activating-workspace-access).
A future backend that cannot provide that isolation cannot implement rename.

## Create

### Create meaning and preconditions

Create allocates a new UUID, assigns one branch, prepares its environment and
credentials, assembles one runtime, and makes it attachable. **Creating a
session does not necessarily create a branch.**

The user flow is **Project → Branch → Policy**. The Branch step has two paths:

| Branch choice | User supplies | Result |
|---|---|---|
| Use an existing branch | The branch name; no separate source | Assign that same unassigned P branch to the new session, starting at its captured tip |
| Create a new branch | A new branch name and a committed source | Create the new P branch at that source commit and assign it to the new session |

For example, selecting an unassigned `feature/login` creates a session on
`feature/login`. It does not create a second branch or ask what to start from.
Selecting **Create new branch**, choosing `main` as source, and then naming
the branch `feature/logout` creates a session on `feature/logout` at `main`'s
captured tip. The new-branch interaction order is Source → Branch name → Policy.

Both paths name one existing project and use its trusted policy. They do not
accept session-specific grant overrides. Their branch preconditions differ:

- **Existing branch:** the P branch must exist, have a committed tip, and have
  no session assignment or competing reservation. `main` is eligible under
  the same rules as any other branch. A branch with a session must be opened
  through that session, even if the session is stopped. No source parameter
  is needed: P captures the chosen branch's tip under the project/ref lock.
- **New branch:** the destination must be a valid, absent Git branch name,
  or the user may request a timestamp suggestion. Source must resolve to a
  committed object reachable from the P repository or a freshly observed
  origin ref. When source is a named branch, the destination must differ from
  it. Only this path creates a branch ref.

Dirty checkout state is never creation input. Claiming an existing branch does
not reset its tip, rewrite its history, or restore an old session's runtime.

The unborn-`main` bootstrap is created only as part of explicit blank/empty-
origin project creation. It has no source object ID, uses the P base image, and
may create only the already reserved `refs/heads/main` on its first push.
The first update's server-side pre-receive check consumes a durable,
single-use creation grant before Git commits the ref. Opening a receive
connection or a dry-run push does not consume it. If the daemon fails after
that check but before the ref outcome is known, P keeps the grant consumed
rather than permitting a second, potentially unrelated creation. If `main` is
then absent, that session can no longer create it. An established bootstrap
session can use its pending grant while `main` remains unborn; while the
project has no P refs, it can also be discarded or deleted.

MVP has no repository-controlled P configuration. Creation snapshots the
trusted project-scoped P configuration and evaluates only the ordinary Nix
devShell from the selected commit. The default devShell is used when present;
otherwise the substrate-only environment applies.

### Create phases

| Phase | Required result |
|---|---|
| `reserved` | Persist operation, UUID, project, branch assignment, and trusted project-configuration snapshot. |
| `source-ready` | Existing branch: capture and verify its committed tip. New branch: resolve/import the selected source. Record the exact object ID and whether the ref predates this request. |
| `branch-assigned` | Existing branch: verify its expected tip and exclusive assignment without changing the ref. New branch: atomically create the absent ref at the captured object ID and assign it. This is creation's commit point. |
| `environment-ready` | Resolve or build a verified Incus environment image and record its fingerprint. |
| `principals-ready` | Create/activate the session Git principal and any bindings required by the selected MVP plugins. |
| `runtime-created` | Create exactly one Incus instance named for the UUID from that image and record its locator. |
| `workspace-ready` | Create a standalone clone of the assigned branch with its P upstream and install only session-scoped endpoints. |
| `systemd-ready` | Start the container; require activation and `p-interactive.service` readiness. Failure records diagnostics and returns the container to stopped. |
| `established` | Verify branch, workspace, runtime, credentials, and required capabilities; set registry state established. |

Environment realization may be shared with another operation. Once published,
the immutable image is an Incus cache entry; the session's instance and later
phases remain specific to its UUID.

Before it records or sends the runtime init, creation proves that the complete
confined-project inventory holds no runtime carrying the session UUID. A
missing deterministic name is not enough: a renamed runtime is never treated as
permission to create a duplicate.

### Failure, cancellation, and retry

Before `branch-assigned`, cancellation may remove the reservation after imported
temporary source material is reconciled. A pre-existing branch is always
preserved; releasing its reservation does not delete or reset the ref.

After `branch-assigned`, P never silently loses the request, session identity,
or branch. A failed creation remains visible in `creating` state with one
durable `blocked` operation. Systemd has already stopped a container whose
activation or host startup failed.

Retry means the exact immutable request: the same operation, UUID, branch
choice (existing or new), captured tip/source commit, normalized policy
snapshot, and idempotency key. It verifies
the reserved branch is unchanged, ensures partial runtime/credential resources
are absent or safe to reuse, and reconstructs the desired result without
creating retry chains. Repeating Retry cannot accumulate instances,
principals, operations, or refs.

If repository content or settings must change, **Try again with changes**
records a new request, UUID, source, policy snapshot, operation ID and
idempotency key, and explicitly supersedes the failed creation. The supported
MVP integrated paths cover reviewed early failures with no UUID-scoped effects
and the reviewed local Git-principal/key/endpoint cleanup path with affirmative
evidence that native initialization was not dispatched. They preserve existing
P refs; an unchanged ref created by the failed request may be explicitly reused.
Exact Retry retains the original identity and request.

MVP does not require one automatic replacement action for every failure.
Other cases may refuse integrated replacement, explaining the unsupported or
uncertain condition and the available cleanup path. A supported fallback is
reviewed cleanup followed by a separate new Create. The implemented failed-create
cleanup below retains the branch; ordinary established-session Discard also
retains it, while Delete requires explicit branch-loss review. Cleanup must
show runtime/workspace and local-credential losses, obtain explicit confirmation,
revalidate identity and ownership, persist recovery intent, and preserve other
refs, sessions, shared environment images and external mount contents. Its
exact CLI procedure must be documented and integration-validated for each
supported failure shape; refusal alone does not satisfy this gate.

An uncertain native init outcome, competing identity, unreachable authority or
unverifiable workspace may prevent safe cleanup. P explains the unresolved
condition and leaves uncertain resources intact. It does not guess ownership,
reset commits or start another runtime to conceal the failure.

### Confirmed failed-create cleanup

Cleanup covers an assembled failed runtime only on a narrow path. The
original local committed-source Create must be blocked at `assembly-ready`,
with the captured base image and immutable plugin/policy selection unchanged
and a standalone policy: network `none` and no filesystem grants. Its
container must already be stopped, have its exact native UUID/generation, and
be the sole matching owned runtime in the full inventory.
Origin/bootstrap, environment-builder/publication, replacement history and
uncertain earlier init are outside that path. P does not look inside the
container: the preview states that the container and everything in it will be
lost. Previewing leaves the session `creating`, preserves its
branch/runtime/private files, and grants no deletion authority. Unsupported or
changed facts are a refusal that preserves resources; they do not convert the
failed creation into an established session.

The host API supports explicit cleanup of a blocked local committed-source
Create before runtime init was dispatched, including an invalid immutable Nix
resolution after its exact owned builder was removed and positively verified
absent. Both runtime and builder must be absent under the complete native
identity inventory. The durable builder cycle must distinguish a settled
removal from an unobserved init attempt; historical or uncertain evidence cannot
be upgraded by an absence observation. An affirmative cycle-zero
`not-attempted` builder record permits cleanup even with a captured source tree
after a read-only preflight failure; fixing that preflight also permits exact
Retry. The assigned P ref and its reviewed tip, sibling refs/sessions, shared
images and external mount contents are preserved.
Reviewed P Git keys, principals and endpoint sockets may be removed. Runtime-
local state is reported unavailable on this early path. P selects the path
from the creator's phase. For the narrow stopped assembled case above, the
preview reports the exact stopped container with `runtime_data_lost:true` and
warns that deletion removes all private runtime state, including the
workspace, home and agent credentials. P does not enumerate or hash their
contents. Environment publication/cache history, origin/bootstrap
ambiguity and other unsupported failures remain outside cleanup. Preserve those
resources and use exact Retry only when its captured request can safely resume.
Unreachable Incus never authorizes forgetting an accepted cleanup.

Use the trusted host control socket and the old UUID from `operation.inspect`.
First inspect the immutable failure and review the entire preview:

```sh
socket=/path/to/state/control.sock
old_uuid=OLD_SESSION_UUID
p api "$socket" operation.inspect '{"id":"OLD_CREATE_OPERATION_UUID"}'
p api "$socket" session.create.cleanup.preview \
  "$(jq -nc --arg uuid "$old_uuid" '{uuid:$uuid}')" > cleanup-preview.json
jq '.result.preview' cleanup-preview.json
```

The same preview call serves the early and the assembled path.

Proceed only when `eligible` is true and the ref and resource identities match
what you intend to preserve/remove. Copy the returned token into an explicit
confirmation with a fresh cleanup key, then poll its returned operation ID:

```sh
token=$(jq -er '.result.preview.confirmation_token' cleanup-preview.json)
p api "$socket" session.create.cleanup.confirm \
  "$(jq -nc --arg uuid "$old_uuid" --arg token "$token" \
      '{uuid:$uuid,key:"reviewed-cleanup-1",confirmation_token:$token}')"
p api "$socket" operation.inspect '{"id":"CLEANUP_OPERATION_UUID"}'
# If accepted cleanup blocks, inspect its diagnostic, correct the observed
# condition, and resume this same intent:
p api "$socket" operation.retry '{"id":"CLEANUP_OPERATION_UUID"}'
```

Unconsumed tokens expire after two minutes or daemon restart; obtain and review
a fresh preview. Exact accepted confirmation replay uses the same UUID, key and
token, even after restart. Early cleanup disables old session authority and
retains a durable ref guard and resource identities until completion. A new or
unreachable native identity leaves it incomplete. Retry never changes the
approved losses or restores the superseded Create.

Assembled confirmation first installs a reversible precommit guard while the
original creator stays blocked/creating, then rechecks the reviewed facts
before source deletion. A changed container identity (Incus UUID, generation
or image) or a changed branch fails the cleanup at `stale`: P releases the
cleanup guards and preserves the container, the branch and the original
creator. Repeat the preview and confirmation with fresh keys. The guard keeps
the creator's Retry, replay, reconciliation or stale worker from advancing,
including after daemon restart. Ambiguous effects keep their guard.
Authority retirement occurs atomically at native deletion admission; from that
point Retry/restart only ensures the confirmed resources absent. An uncertain
delete outcome never permits another name-only deletion. Its diagnostic retains
the unresolved identity for manual Incus investigation.

Only after `status:"completed", phase:"cleaned"` may a separate Create reuse the
preserved branch. Correct the source with ordinary Git, commit and push it to P,
then submit a new immutable request. For example, after updating `work`:

```sh
p api "$socket" session.create \
  '{"key":"corrected-create-1","project":"team/app","branch":"work","choice":"existing"}'
```

This new request captures the current committed source and policy and receives
its own UUID. Cleanup performs no branch deletion, reset, checkout, or new
native creation.

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
- `missing` blocks start; start does not construct a replacement. See
  [inconsistent sessions](#inconsistent-sessions).
- `unreachable` blocks start because P cannot exclude a duplicate runtime.
- Missing, revoked, or changed Git/session credentials block start; P never
  reissues them, and the refusal directs the user to discard the session and
  create a new one. Post-MVP external-service plugins define their own health
  and degradation evidence through their lifecycle contract. A missing cached
  parent image does not affect an existing instance; it matters only to a
  creation that needs it.
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
and selected plugin bindings. It never renames or deletes an origin ref.

The new branch name must pass Git validation and be absent from the project's
`refs/heads/*`. The session must be established and stopped, its current P ref
and runtime must be reachable, and its workspace must contain the assigned
local branch. A session in any status other than stopped is refused with
`busy` before an operation starts; P never freezes it. P does not reset,
clean, commit, or switch unrelated work to make rename pass.

The workspace must be on the assigned branch with `origin` tracking that
branch in P. A mismatch is a precommit refusal with bounded expected/actual
branch or upstream values; remote URLs and credentials are omitted. Correct
Git inside the session after inspecting the work: switch back to the assigned
branch when safe, and set `branch.<assigned>.remote` to `origin` and
`branch.<assigned>.merge` to `refs/heads/<assigned>`. P performs no checkout,
reset or configuration repair. Submit a new Rename request after correcting a
failed precommit attempt; exact replay of its old key still returns that failed
operation. The next attempt rechecks the corrected workspace.

### Rename phases

Under the lifecycle and project/ref locks:

| Phase | Required result |
|---|---|
| `reserved` | Persist old/new refs and expected old P tip; reserve the new name. |
| `guarded` | Reject pushes to both old and new refs. |
| `workspace-prepared` | Verify the exact runtime is still stopped; back up the workspace branch and configuration and record the planned local change. A session started since the request fails as stale before any effect. |
| `new-ref-created` | Atomically create the new P ref at the expected old P tip. This is rename's commit point. |
| `workspace-renamed` | Rename the local branch without changing its tip or files; update upstream/push configuration. |
| `assignment-updated` | Change SQLite and server-side principal policy to the new branch address. |
| `old-ref-deleted` | Delete the old P ref with an expected-old-tip check only after the new mapping verifies. |
| `completed` | Remove the workspace backup; release guards and reservation. |

The workspace branch may contain commits ahead of the P ref. Renaming preserves
them locally; the next permitted push fast-forwards the new P ref. The operation
records both server and workspace tips so verification does not mistake this
ordinary ahead state for corruption.

### Rename recovery

Before a native or Git effect is attempted, a proven refusal may release the
reservation and retain the old mapping. An attempted backup write or
new-ref creation with an unknown outcome keeps both guards until its exact
outcome is established. The new-ref-create-issued phase can already have
created the new P ref, so it cannot roll back on an immediate absence
observation. At and after a verified `new-ref-created`,
recovery completes forward to the new name. Start and attach remain blocked
while the operation is unresolved.

If another process altered a guarded ref or the workspace mapping despite the
guards, P does not guess. It leaves the runtime stopped and reports the
observed refs for manual investigation.

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
as stopped. Unreachable Incus must be restored; a missing runtime is an
[inconsistent session](#inconsistent-sessions).

An interrupted stop is resolved from the Incus operation and current instance
state: running means retry is available; stopped means success. P does not
infer success from RPC connection loss or maintain a second durable stop
workflow.

## Destructive preflight

Discard and delete state what they lose before confirmation. P does not look
inside the container to do so: it does not read the workspace, activate the
environment, run the shell hook, prepare the interactive host, or execute
repository-controlled commands. A running session needs no Stop first; P stops
it as part of the removal. A pending or confirmed attachment refuses both the
preview and the confirmation.

### Runtime-local loss

P states runtime-local loss by category. When the session's runtime exists,
the preview names its exact identity (Incus UUID, generation and image) and
states that the container and everything in it will be lost:

- uncommitted changes and files outside Git;
- local refs and commits not pushed to P;
- runtime processes, writable layer, home, temporary files, and runtime-owned
  volumes; and
- runtime-local credentials, such as agent authentication files.

External project mounts are not deleted by P; writes already made through them
remain external state. Commits already in P's repository are reported as
[branch loss](#branch-loss) when Delete removes them.

P does not enumerate files, count commits or hash workspace contents, so
changes made inside a running session after the preview do not make it stale.
The preview records whether the runtime was running or stopped for information
only: a Start or Stop between preview and confirmation does not make it stale
either. A replaced runtime (changed Incus UUID or generation) does.

If Incus authoritatively reports the runtime missing, P states that
runtime-local state is already unavailable and may continue cleanup after
explicit acknowledgement. Missing-runtime preview and confirmation, the local
authority commit, and final record removal require full confined-project
inventory proof that no runtime carries the session UUID. A renamed or
competing runtime blocks ordinary removal; it is neither adopted nor deleted
through the missing-name path. If Incus is unavailable, P reports the unavailable
authority and cannot issue a new removal confirmation. An already-confirmed
removal remains incomplete with its session identity, durable operation and
existing authorization restrictions intact. Retry or reconciliation may resume
that same operation when Incus returns. P never claims successful deletion,
forgets uncertain resources or creates replacement machinery while existence
is uncertain. Manual Incus investigation is supported; MVP has no abandonment
override.

### Branch loss

Delete additionally reports:

- the exact assigned P ref and tip to be deleted;
- commits that would cease to be reachable from any retained P ref; and
- the captured P refs.

Discard and delete contact and compare no origin: whether work is published
there is the user's concern. Commits that leave P's repository are reported as
lost whether or not the origin has them.

Removal accepts a missing assigned P ref, whether it was deleted outside P or
is a blank project's unborn first branch. The preview then marks the ref
missing and has no tip. Delete has nothing in P's repository to remove, and
Discard has no branch to retain, so it ends like a Delete. Commits that
existed only in the runtime are part of its runtime-local loss.

### Confirmation token

The preflight result produces a short-lived confirmation token bound to:

```text
session UUID and operation kind
project and assigned ref
P ref object ID, or the missing assigned ref
runtime identity (Incus UUID, generation, image) or explicit missing-runtime acknowledgement
delete's branch-loss review
```

Execution revalidates these facts under the lifecycle lock before the removal
commit point. A mismatch never falls through to a generic “are you sure”; it
fails the operation as stale and requires review of the updated facts.

## Discard

Discard ends the session and removes its runtime while retaining the current P
branch as an ordinary retained source branch.

After a valid destructive confirmation, discard completes forward through:

1. guard the assigned P ref against further writes;
2. revalidate the assigned tip, runtime identity, and missing-runtime proof;
3. if the confirmation is still valid, mark the registry and operation
   `removing` and disable the session's P Git and session-RPC authority;
4. persist the runtime-removal commit point, force-stop a running runtime,
   then request Incus removal;
5. request each participating plugin's required credential revocation and
   remove P-owned local secret copies;
6. remove other session credentials;
7. verify that the P branch still exists at the guarded expected tip, unless
   the assigned ref was already missing at preview;
8. end the UUID-to-branch assignment and remove the session row; and
9. release the guard and leave the branch visible as a retained project
   branch.

A stale confirmation discovered in step 2 releases the guard and leaves the
established session and authorities unchanged.

Once the runtime-removal commit point is persisted, discard never recreates the
runtime to roll back. Reconciliation completes cleanup forward. Failure to
revoke a required key retains the disabled session identity and durable cleanup
operation until revocation is verified; it never recreates a removed runtime.

## Delete

Delete performs the same runtime and credential removal as discard and also
deletes the assigned P branch.

After runtime removal verifies, P:

1. checks that the guarded branch still has the confirmed object ID;
2. atomically deletes that ref with an expected-old-value check;
3. ends the assignment and removes the session row; and
4. retains bounded operation diagnostics after all required cleanup verifies.

When the assigned ref was already missing at preview, steps 1 and 2 have
nothing to delete.

Delete never deletes or renames an origin ref, external mount contents, cached
environment images, or another P ref containing the same
commits.

Once runtime removal begins, delete completes forward even if branch deletion
temporarily fails. The session remains `removing`, its Git principal stays
disabled, and reconciliation retries or leaves the operation blocked with its
diagnostic for manual investigation; it does not restore a runtime around a
branch the user authorized P to delete.

## Reconciliation

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

### Inconsistent sessions

P has no session repair. When a session's authorities disagree in a way that
automatic reconciliation must not resolve, P reports the condition and refuses
the actions that depend on the missing fact. In most cases the user corrects
the outside fact, or discards the session and creates a new one on the
retained branch:

| Inconsistency | What happens and what to do |
|---|---|
| Incomplete authorized operation | Reconciliation or `operation.retry` resumes its defined forward/rollback path. |
| Stale runtime locator | Outside MVP: instance names are derived from the session UUID, and no mutable locator is stored. P reports the missing expected runtime; a same-UUID runtime under another name blocks removal, and P does not adopt it. |
| Missing runtime, assigned P branch intact | Start refuses. Discard with the explicit missing-runtime acknowledgement described in [destructive preflight](#destructive-preflight), then create a new session on the retained branch. Delete accepts the same acknowledgement when the branch should go too. |
| Missing, revoked, or changed session Git principal or key | Start refuses. Discard, then create a new session on the retained branch. Discard removes the runtime, so rescue uncommitted work from it by hand first. |
| Runtime exists, assigned P ref missing | Discard or Delete the session; with no ref both end it the same way and leave no branch. Commits that exist only in the runtime are lost with it, so rescue them by hand first; P does not import them. Then create a new session on another branch. |
| Session row has neither runtime nor branch | Delete with the missing-runtime acknowledgement removes the record; Discard ends the same way. |
| Workspace branch/upstream mismatch | Show expected and actual branch/upstream values and block actions that depend on the assigned branch. The user or agent corrects Git manually; P rechecks on the next attempt. P performs no automatic checkout or reset. |

A new session on the retained branch starts from its committed tip. It cannot
recover uncommitted files, unpushed commits, processes, terminal state, or
conversations that existed only in the old runtime. Its environment comes from
the branch's committed devShell, which may differ from the old session's image.

## Abandonment and orphans (post-MVP)

The following design is outside MVP. MVP retains incomplete cleanup and its
identity while Incus is unavailable, supports manual investigation, and never
offers abandonment, abandonment tombstones or an orphan-cleanup/forget workflow.

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

Local P authorization follows the SQLite assignment. Marking removal
immediately prevents the session key from pushing even if a private
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
cache miss the next time a creation needs it.

## Operation recovery summary

| Operation | Commit point | Before commit point | At/after commit point |
|---|---|---|---|
| Create | Existing P branch assigned, new branch created and assigned, or unborn bootstrap reserved | Cancel/clean reservation when safe; preserve pre-existing branch | Retain one blocked request; Retry cleans verified partial resources and reconstructs the same immutable request; changed input explicitly supersedes it with a new creation |
| Start | None in P; Incus/systemd own the operation | No duplicate P workflow | Inspect Incus and `p-interactive.service`; success is ready, failure returns the container to stopped and another Start retries |
| Attach | Pending token confirmed and promoted | Expire pending token without presence/status clear | Helper retains the lease through teardown while reachable; lease loss starts immediate transport-bound teardown before that helper can attach again |
| Rename | New P ref created | Release reservation and retain old mapping | Complete forward to new ref under guards |
| Stop | None in P; Incus owns the operation | No duplicate P workflow | Inspect Incus; report stopped or expose retry |
| Discard | Runtime removal committed | Revalidate or cancel without loss | Complete runtime/credential cleanup; retain branch |
| Delete | Runtime removal committed | Revalidate or cancel without loss | Complete cleanup and confirmed branch deletion |

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
- destructive preflight facts and confirmation token; and
- retry, cleanup, or removal actions permitted by the current facts.

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
- discard, delete, and destructive preflight;
- persisted cross-authority operations, per-session/ref locking, workspace
  changes only on a stopped session, and restart reconciliation;
- local Git-principal and selected-plugin cleanup; and
- identity checks and duplicate prevention for missing or manually changed
  containers, without adopting unfamiliar machinery.

MVP does not include runtime migration, branch-specific grants, automatic
reclamation, service lifecycle, attempts, checks, session cloning, or recovery
of state that never reached a retained Git ref or external mount. Dedicated
branch/upstream mismatch repair, explicit abandonment, abandonment tombstones
and their orphan-cleanup/forget workflow are also outside MVP.
Backup/restore and software upgrade/rollback are outside MVP; operation-level
crash recovery remains required. Stop/Start and daemon restarts preserve local
Git repositories and the documented retained runtime data, without protection
against disk loss or deliberate deletion.

## Acceptance criteria

The lifecycle design is implemented when integration tests prove:

1. every crash point in create, rename, discard, and delete converges to the
   documented result without duplicate runtimes or silent ref loss;
2. retrying creation verifies and cleans partial resources, reconstructs the
   same immutable request without duplicate identities. Reviewed existing-branch
   and local new-branch failures support integrated supersession; supported
   complex failures use explicitly confirmed cleanup followed by separate
   Create. Unsafe uncertainty is explained and preserved, and cleanup never
   deletes a pre-existing branch;
3. `(project, branch)` uniqueness allows equal branch names in different
   projects and rejects collisions in one project;
4. start preserves writable runtime state but does not claim process or tmux
   continuity across stop;
5. failed attachment before confirmation does not increment presence or clear
   unattended status, while confirmation performs both atomically;
6. rename of a stopped session preserves UUID, workspace files, local-ahead
   commits, and credentials while changing both Git refs safely, and rename
   refuses a running session;
7. destructive confirmation becomes stale when the assigned tip, the runtime
   identity, or delete's branch-loss review changes, and stays valid when the
   session is only started or stopped;
8. discard preserves the assigned branch as retained, delete removes only the
   confirmed P ref, both stop a running session themselves, and both proceed
   when the assigned ref is already missing;
9. missing and unreachable runtimes take different cleanup paths;
10. Incus unavailability leaves cleanup incomplete with identity, durable
    operation state and authorization restrictions retained; when it returns,
    Retry/reconciliation resume only confirmed work without duplicates or
    adoption of unfamiliar machinery;
11. daemon restart drops pending attachment tokens and active leases; each
    helper finishes temporary-client teardown while systemd preserves the
    persistent host, and operation reconciliation resumes;
12. abrupt client death or transport loss ends only its temporary attach
    client, while a reachable daemon retains attachment presence until teardown
    completes;
13. no automatic cleanup deletes a branch, runtime, or orphan record based only
    on age;
14. activation or persistent-host failure leaves the container stopped with a
    bounded inspectable diagnostic and an ordinary Start retry;
15. host exit shuts down the container while detach or switching sessions does
    not; and
16. branch/upstream mismatch diagnostics show expected and actual values,
    block dependent actions, and recheck manually corrected Git on the next
    attempt without automatic checkout/reset.
