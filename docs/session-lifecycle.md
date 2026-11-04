# P — session lifecycle

How P creates, starts, attaches, renames, stops, discards, and deletes
sessions without confusing intended state with Git, runtime, or
credential facts.

> **Status: design.** This document is authoritative for session identity,
> lifecycle operations, concurrency, destructive-loss analysis, and recovery.
> [communication-boundaries.md](communication-boundaries.md) owns what crosses
> Git, RPC, SSH, and attachment channels;
> [environment-building.md](environment-building.md) owns commit images, their
> lifetime, and activation; [runtime-isolation.md](runtime-isolation.md)
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
- [Credential and image cleanup](#credential-and-image-cleanup)
- [Operation recovery summary](#operation-recovery-summary)
- [RPC and presentation](#rpc-and-presentation)
- [MVP boundary](#mvp-boundary)
- [Acceptance criteria](#acceptance-criteria)

## Purpose

A session spans authorities with different failure behavior:

- SQLite records P's identity, intent, and progress;
- Git owns commits and refs;
- Incus owns instances, runtime operations, writable roots, and commit images;
- P's Git authorization and activated external-service plugins own their
  provider-specific credentials; and
- the running daemon owns attachment presence through the leases on the
  terminals it relays.

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
| Commits, branch existence, branch tip | The source driver's backend: P's repository of the project, which core reads and changes only through the source driver | indexes expected ref and observed tip |
| Workspace files and local refs | The runtime's private root: the Incus instance root | records no duplicate file state |
| Runtime existence, state, root storage, and runtime operation | The runtime driver's backend: the Incus instance | records the runtime's incarnation where a removal or rename reviewed it; no instance name or Incus project |
| Commit image bytes, reference and labels | The runtime driver's backend: the Incus image store | indexes (project, commit, base image, system, builder version) to the image reference |
| P Git write permission | Core's Git authorization, which the source driver's Git service asks to open each service and to approve each head update, using current SQLite assignment | records principal and revocation state |
| Post-MVP external-service principal validity | Configured service | records only the protected binding and cleanup state required by its plugin contract |
| Interactive-host readiness and failure | `p-interactive.service` and persistent system journal | records only bounded operation/last-start diagnostics |
| Pending attachments and attachment presence | The running daemon's pending tokens and terminal leases; the runtime driver's completion witness ends a lease | not persisted |
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
`missing`, or `unreachable` for an established session, except that an
unfinished start or stop operation supplies `starting` or `stopping`. It is not a second
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
expected runtime incarnation, image fingerprint, and P metadata
change hash, for a risky change
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

Attach and detach are the live exception: the pending token and the daemon's
lease on the relayed terminal provide their identity, so P does not journal
them as durable operations.

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
| Stop | Refused during create completion, rename, or removal, and while an attachment is pending or attached ([Stop](#stop)). |
| Publish | Refused unless the session is established and its branch assignment is stable. |
| Discard/delete | Refused during any other mutation and while an attachment is pending or holds a lease, including one whose terminal's end is not yet proven. A running session is accepted; P stops it as part of the removal. |

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
host context. Rename reads and writes the clone's Git administration files
only through the runtime driver's workspace operations on the stopped session
at the incarnation it recorded
([runtime and isolation](runtime-isolation.md#non-activating-workspace-access)).
A driver that cannot provide them cannot implement rename. Which files to read
and which edits to make is the source driver's to decide (`RenameWorkspace`),
from the files core read for it; the two drivers never reach each other.

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
- **New branch:** the destination must be a branch name the source driver
  accepts and absent from P's repository, or the user may request a
  timestamp suggestion. Source must resolve to a committed object reachable
  from a head of the P repository, or a freshly observed origin ref. When
  source is a named branch, the destination must differ from it. Only this
  path creates a branch ref.

The source driver checks the request's branch, source, origin ref and
expected commit ID (`CheckNames`) after P looks up the request's idempotency
key and before it records anything: a replayed request is answered from P's
records alone, and a value the driver refuses answers `invalid` naming its
field. The driver also classifies the source as a branch or a commit ID; P
reads a branch's tip with `InspectBranch` and a commit with `InspectCommit`,
and a failed read answers `failed`, reason `git`, never a missing source.

Dirty checkout state is never creation input. Claiming an existing branch does
not reset its tip, rewrite its history, or restore an old session's runtime.

The unborn-`main` bootstrap is created only as part of explicit blank/empty-
origin project creation. It has no source object ID, uses the P base image, and
may create only the already reserved `refs/heads/main` on its first push.
The first update's approval consumes a durable, single-use creation grant
before Git commits the ref: after the source driver's pre-receive check, core's
`Approve` consumes it. Opening a receive connection or a dry-run push does not
consume it. If the daemon fails after
that check but before the ref outcome is known, P keeps the grant consumed
rather than permitting a second, potentially unrelated creation. If `main` is
then absent, that session can no longer create it. An established bootstrap
session can use its pending grant while `main` remains unborn; while the
project has no P refs, it can also be discarded or deleted.

MVP has no repository-controlled P configuration. Creation snapshots the
trusted project-scoped P configuration and evaluates only the ordinary Nix
devShell from the selected commit. The default devShell is used when present;
otherwise the substrate-only environment applies.

Creation pins the selected agent-adapter package, if one is selected, by ID
and digest, and a Retry requires the same package. It pins no session host
package: the session host is the runtime driver's own. It pins no source
package: the source driver is part of P, whose backend P pins once for the
instance.

### Create phases

| Phase | Required result |
|---|---|
| `reserved` | Persist operation, UUID, project, branch assignment, and trusted project-configuration snapshot. |
| `source-ready` | Existing branch: capture and verify its committed tip. New branch: resolve/import the selected source. Record the exact object ID and whether the ref predates this request. |
| `branch-assigned` | Existing branch: verify its expected tip and exclusive assignment without changing the ref. New branch: atomically create the absent ref at the captured object ID and assign it. This is creation's commit point. |
| `image-ready` | Reuse the image indexed for the source commit, or build one from a Git bundle of the commit and index it; record its reference. Without `runtime.environment`, creation skips this phase and the runtime uses the P base image. |
| `principals-ready` | Create/activate the session Git principal and any bindings required by the selected MVP plugins. |
| `runtime-created` | Create exactly one Incus instance named for the UUID from that image and record its locator. |
| `workspace-ready` | Put `/workspace` on the assigned branch with its P upstream, starting from the commit image's clone, and install only session-scoped endpoints. Without a commit image, create a standalone clone of the assigned branch from P. |
| `systemd-ready` | Start the container; require activation and `p-interactive.service` readiness. Failure records diagnostics and returns the container to stopped. |
| `established` | Verify branch, workspace, runtime, credentials, and required capabilities; set registry state established. |

A new branch's creation is recorded as sent right before the source driver
gets it. A creation found sent, after a lost answer, a Retry or a restart, is
settled first (`Settle`): one that applied goes on once the branch is exact,
one that never applied is sent again, and while the driver cannot tell the
creation stays blocked with reason `outcome_unknown`, whatever the branch
shows. Its replacement and cleanup wait for that settlement too: until the
driver can tell, their plans are refused with `outcome_unknown`.

Every session started from one commit is created from that commit's image. A
per-commit lock serializes lookup, build and acceptance, so concurrent
creations from one commit build at most one image and the others reuse it. The
image holds nothing of one session; the session's instance and later phases
remain specific to its UUID ([environment building](environment-building.md#lookup-concurrency-and-image-lifetime)).

Before it records or sends the runtime init, the driver's `Create` proves that
nothing in its whole scope carries the session UUID. A missing deterministic
name is not enough: a renamed runtime is never treated as permission to create
a duplicate.

An init whose outcome is unknown is never sent again: the reply was lost, or
the creation finds its recorded init attempt and no runtime at the session's
address. P asks the driver to settle the init. When the driver settles it as
applied, the runtime exists and P completes it at its incarnation with
`Create`, which never initializes another; any other answer keeps the creation
blocked.

### Failure, cancellation, and retry

Before `branch-assigned`, cancellation may remove the reservation after imported
temporary source material is reconciled. A pre-existing branch is always
preserved; releasing its reservation does not delete or reset the ref.

After `branch-assigned`, P never silently loses the request, session identity,
or branch. A failed creation remains visible in `creating` state with one
durable `blocked` operation. A container whose activation or host startup
failed is already stopped: systemd shuts it down, or P stops it, and P records
the failure once it observes the container stopped. A stop that P does not
observe taking effect within 45 seconds is named in the diagnostic
(`stopping failed`).

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
refs, sessions, shared commit images and external mount contents. Its
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
container must already be stopped, have an incarnation the driver reports, and
be the only object in the driver's complete inventory carrying the session
UUID, with none carrying the creation's build reference.
Origin/bootstrap, environment-builder/publication, replacement history and
uncertain earlier init are outside that path. P does not look inside the
container: the plan states that the container and everything in it will be
lost. A dry run leaves the session `creating`, preserves its
branch/runtime/private files, and grants no deletion authority. Unsupported or
changed facts are a refusal that preserves resources; they do not convert the
failed creation into an established session.

The host API supports explicit cleanup of a blocked local committed-source
Create before runtime init was dispatched, including an invalid immutable Nix
resolution after its exact owned builder was removed and positively verified
absent. Both runtime and builder must be absent: the driver's `Inspect` finds
no runtime at the session's address, and its complete inventory holds nothing
carrying the session UUID or the latest build's reference. The durable builder
cycle must distinguish a settled removal from an unobserved init attempt; historical or uncertain evidence cannot
be upgraded by an absence observation. A cycle recorded `abandoned`, settled as
not accepted while its leftovers may remain, permits cleanup only after
recovery deletes them and records the cycle `absent`. An affirmative cycle-zero
`not-attempted` builder record permits cleanup after a read-only preflight
failure; fixing that preflight also permits exact Retry. The assigned P ref
and its reviewed tip, sibling refs/sessions, shared images and external mount
contents are preserved.
Reviewed P Git keys, principals and endpoint sockets may be removed. Runtime-
local state is reported unavailable on this early path. P selects the path
from the creator's phase. For the narrow stopped assembled case above, the
plan names the exact stopped container and lists it as `runtime_data` loss:
deletion removes all private runtime state, including the workspace, home and
agent credentials. P does not enumerate or hash their contents. A creation
that already recorded its commit image, origin/bootstrap ambiguity and other
unsupported failures remain outside cleanup. Preserve those
resources and use exact Retry only when its captured request can safely resume.
Unreachable Incus never authorizes forgetting an accepted cleanup.

Use the trusted host control socket and the old UUID from `operation.inspect`.
First inspect the immutable failure, then make a dry run of the cleanup and
review its entire plan:

```sh
socket=/path/to/state/control.sock
old_uuid=OLD_SESSION_UUID
p api "$socket" operation.inspect '{"id":"OLD_CREATE_OPERATION_UUID"}'
request=$(jq -nc --arg uuid "$old_uuid" '{uuid:$uuid}')
p api "$socket" session.create.cleanup "$request" > cleanup-plan.json
jq '.result.plan' cleanup-plan.json
```

The same dry run serves the early and the assembled path.

Proceed only when `allowed` is true and the ref and resource identities match
what you intend to preserve/remove. A plan that is not allowed lists its
`refusals` and has no change tag. Run the plan: send the same request with a
fresh cleanup key and the returned change tag, then wait for the returned
operation ID:

```sh
tag=$(jq -er '.result.change_tag' cleanup-plan.json)
p api "$socket" session.create.cleanup \
  "$(jq -c --arg tag "$tag" '. + {key:"reviewed-cleanup-1",if_match:$tag}' <<< "$request")"
p api "$socket" operation.wait '{"id":"CLEANUP_OPERATION_UUID","timeout":20}'
# If accepted cleanup blocks, inspect its diagnostic, correct the observed
# condition, and resume this same intent:
p api "$socket" operation.retry '{"id":"CLEANUP_OPERATION_UUID"}'
```

A change tag expires two minutes after its dry run, and a run with an expired
or changed plan answers `precondition_failed` and records nothing: make and
review a new dry run. Replaying an accepted run with the same UUID, key and
tag returns its operation, even after restart. Early cleanup disables old
session authority and retains a durable ref guard and resource identities
until completion. A new or
unreachable native identity leaves it incomplete. Retry never changes the
approved losses or restores the superseded Create.

An accepted assembled run first installs a reversible precommit guard while
the original creator stays blocked/creating, then rechecks the planned facts
before source deletion. A changed container identity (another incarnation or
image) or a changed branch fails the cleanup at `stale`: P releases the
cleanup guards and preserves the container, the branch and the original
creator. Make a new dry run and run it with a fresh key. The guard keeps
the creator's Retry, replay, reconciliation or stale worker from advancing,
including after daemon restart. Ambiguous effects keep their guard.
Authority retirement occurs atomically at native deletion admission; from that
point Retry/restart only ensures the confirmed resources absent. An uncertain
delete outcome is settled through the runtime driver and never permits another
deletion: a delete the driver settles as applied lets the cleanup go on to
prove the runtime absent. Otherwise its diagnostic retains the unresolved
identity for manual Incus investigation.

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
credentials, and parent image.

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
  and degradation evidence through their lifecycle contract. A missing parent
  image does not affect an existing instance; it matters only to a creation
  that needs it.
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

Attach is a host RPC decision followed by a terminal that the daemon opens
through the runtime driver and relays to the trusted host attachment helper;
terminal bytes do not pass through lifecycle JSON-RPC.

The daemon, client, and helper perform a two-stage handshake. The daemon:

1. verifies an established session with no conflicting operation;
2. requires it ready; it starts nothing, so the client starts a stopped session
   and waits for its start operation first, as `p attach` does;
3. checks through the runtime driver that the runtime runs complete with its
   host ready;
4. creates a short-lived, one-use pending token bound to the session and
   authenticated host request and returns only that token;
5. the client invokes the trusted helper locally or through client-initiated
   SSH and transfers the token through its private control stream, never argv;
6. the helper opens its own control connection and claims the token with the
   client terminal's size;
7. the daemon locks and checks the session again and has the runtime driver
   open a terminal on the fixed attach command (`OpenTerminal`), whose
   completion witness the driver arms before the attach starts; and
8. the daemon increments attachment presence, clears the latest unattended
   condition only on the zero-to-one transition, answers the claim, and from
   then on relays the terminal over the claim's connection.

A failed readiness check creates no token. A failed terminal open, an
expired token, or a helper that hangs up while its terminal opens removes the
pending state. None of those failures increments attachment presence,
suppresses agent events, or clears unattended status. A terminal the claim
opened, or whose failed open the runtime driver cannot prove ended, keeps the
attachment's lease until its end is proven or settled, or the session stops.
An open lost with the runtime driver's connection names no terminal to
settle, so its lease waits for the session to stop.
Multiple attachments are allowed when the interactive host supports them.

The runtime driver owns the native channel and its witness; the daemon owns
the token, the lease and the relay; the helper only carries bytes and has no
runtime authority. The ordinary client is not relied upon for cleanup: on
client exit, SIGKILL, carrier or helper loss, or client-machine loss, the
helper's connection closes and the daemon closes the terminal. The daemon keeps
the lease, and the attachment's presence, until the witness proves the attached
process ended. If the runtime driver fails while the daemon stays up, the
terminal ends unproven: the helper tears down, input and resizes stop and are
not replayed, and the lease stays, so discard and delete stay refused. That
lease ends when the driver settles the terminal as ended, which the daemon asks
every few seconds, or when the session [stops](#stop). A daemon restart ends
attachments. MVP cannot re-register an existing terminal.

Teardown ends only that temporary attach client. The systemd-owned persistent
host and configured command remain alive, so detach and session switching do
not stop the runtime. Clean or failed persistent-host exit, not attachment
loss, causes systemd to shut down the container.

## Rename

### Rename meaning and preconditions

Rename changes the actual session-owned Git branch while preserving the UUID,
project, Incus instance, workspace contents, parent image, credentials,
and selected plugin bindings. It never renames or deletes an origin ref.

The new branch name must pass the source driver's name check (`CheckNames`,
before anything is recorded; a replayed request is answered without it) and be
absent from the project's `refs/heads/*`. The session must be established and
stopped, its current P ref and runtime must be reachable, and its workspace
must contain the assigned local branch. A session in any status other than
stopped is refused with `conflict`, reason `session_not_stopped`, before an
operation starts; P never freezes it. P does not reset, clean, commit, or
switch unrelated work to make rename pass.

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
| `workspace-prepared` | Verify the exact runtime is still stopped; write the backup of the workspace branch and configuration that the source driver plans, and record its plan of the local change. A session started since the request fails as stale before any effect. |
| `new-ref-created` | Atomically create the new P ref at the expected old P tip. This is rename's commit point. |
| `workspace-renamed` | Rename the local branch without changing its tip or files; update upstream/push configuration. |
| `assignment-updated` | Change SQLite and server-side principal policy to the new branch address. |
| `old-ref-deleted` | Delete the old P ref with an expected-old-tip check only after the new mapping verifies. |
| `completed` | Remove the workspace backup; release guards and reservation. |

The workspace branch may contain commits ahead of the P ref. Renaming preserves
them locally; the next permitted push fast-forwards the new P ref. The operation
records the expected P tip and the source driver's plan, which fixes the
workspace tip, so verification does not mistake this ordinary ahead state for
corruption. Every later answer of the source driver for the rename must return
that plan.

### Rename recovery

Before a native or Git effect is attempted, a proven refusal may release the
reservation and retain the old mapping. An attempted backup write or
new-ref creation with an unknown outcome keeps both guards until its exact
outcome is established. Each workspace edit the source driver returns, a write
or a removal, passes a gate that records the rename's phase and the edit's
path. One whose reply was lost is settled through the runtime driver at once
and never sent again: the rename goes on only when the runtime driver finds the
file exactly as the edit intended, and otherwise it stays blocked. A recovery
that finds an edit recorded goes on only when the source driver asks for no
further edit toward that phase's goal and the runtime driver settles the
recorded edit applied for the file now there; an edit still missing is reported
unresolved rather than written again. Every attempt after the backup's removal
was sent first settles that removal. The new-ref-create-issued phase can
already have created the new P ref, so it cannot roll back on an immediate
absence observation; only the source driver's settlement of the creation,
with the new ref then exact, moves it forward. At and after a verified
`new-ref-created`, recovery completes forward to the new name. Start and attach
remain blocked while the operation is unresolved.

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

It terminates runtime processes, including tmux and interactive agents. An
attachment that is pending, opening its terminal, or attached refuses a stop
with `conflict`, reason `attachment_active`; the pending token expires quickly,
while an active user must detach first. This prevents one client from
unexpectedly closing another client's terminal.

An attachment whose terminal ended unproven, because the runtime driver failed,
does not refuse Stop: no client holds that terminal any more. A Stop that leaves
the runtime stopped, or that finds it already stopped, ends that attachment's
lease and presence, since stopping ends any process the attachment left. Otherwise a lease that the driver never
settles would refuse Stop, discard and delete for as long as the daemon runs.

Stop waits until the guest can take a graceful shutdown, requests one from
Incus, and stops the instance by force if it is still running after Incus's
30-second graceful timeout
([runtime isolation](runtime-isolation.md#reconciliation-and-cleanup)).
It never removes the instance. Already
stopped succeeds idempotently. Missing or unreachable machinery is not reported
as stopped. Unreachable Incus must be restored; a missing runtime is an
[inconsistent session](#inconsistent-sessions).

An interrupted stop is resolved from the Incus operation and current instance
state: running means retry is available; stopped means success. P does not
infer success from RPC connection loss or maintain a second durable stop
workflow.

## Destructive preflight

Discard and delete are risky changes
([control API](control-api.md#risky-changes)): a dry run returns their plan,
which states what they lose, and a separate run carries out that plan. P does
not look inside the container to state the loss: it does not read the
workspace, activate the environment, run the shell hook, prepare the
interactive host, or execute repository-controlled commands. A running session
needs no Stop first; P stops it as part of the removal. An attachment that is
pending or holds a lease, including one whose terminal's end is not yet proven,
refuses the plan with `attachment_active`, so there is no change tag to run.

### Runtime-local loss

P states runtime-local loss by category. When the session's runtime exists,
the plan names its exact identity, the runtime driver's incarnation and the
image, with the driver's name for it for display, and lists one `runtime_data`
loss item: the container and everything in it will be lost:

- uncommitted changes and files outside Git;
- local refs and commits not pushed to P;
- runtime processes, writable layer, home, temporary files, and runtime-owned
  volumes; and
- runtime-local credentials, such as agent authentication files.

External project mounts are not deleted by P; writes already made through them
remain external state. Commits already in P's repository are reported as
[branch loss](#branch-loss) when Delete removes them.

P does not enumerate files, count commits or hash workspace contents, so
changes made inside a running session after the dry run do not make its plan
stale. The plan records whether the runtime was running or stopped, and the
runtime driver's name for it, for information only: a Start or Stop between the
dry run and the run does not make it stale either, nor does a runtime the driver
now names differently. A replaced runtime (another incarnation) does.

If Incus authoritatively reports the runtime missing, the plan states that
runtime-local state is already lost: it lists the runtime's contents in
`gone`, not in `loss`, and running the plan proves the caller saw that. The
missing-runtime dry run and run, the local authority commit, and final record
removal require the proof of absence: the driver's `Inspect` finds nothing at
the runtime's address and its complete inventory holds no object carrying the
session UUID, whichever P instance labeled it. A renamed or competing runtime
refuses ordinary removal; it is neither adopted nor deleted through the
missing-name path. If Incus is unavailable, P reports the unavailable
authority and returns no plan. An already-accepted removal remains incomplete
with its session identity, durable operation and existing authorization
restrictions intact. Retry or reconciliation may resume that same operation
when Incus returns. P never claims successful deletion, forgets uncertain
resources or creates replacement machinery while existence is uncertain.
Manual Incus investigation is supported; MVP has no abandonment override.

### Branch loss

Delete additionally reports:

- the exact assigned P ref and tip to be deleted;
- commits that would cease to be reachable from any retained P ref; and
- the captured P refs.

Discard and delete contact and compare no origin: whether work is published
there is the user's concern. Commits that leave P's repository are reported as
lost whether or not the origin has them.

Removal accepts a missing assigned P ref, whether it was deleted outside P or
is a blank project's unborn first branch. The plan then marks the ref
missing and has no tip. Delete has nothing in P's repository to remove, and
Discard has no branch to retain, so it ends like a Delete. Commits that
existed only in the runtime are part of its runtime-local loss.

### Change tag

The dry run returns, with an allowed plan, a change tag that expires two
minutes later. The tag binds a hash of the planned change and the facts it was
planned from:

```text
session UUID and operation kind
project and assigned ref
P ref object ID, or the missing assigned ref
runtime identity (incarnation, image) or the missing runtime
delete's branch loss
```

The run plans again under the lifecycle lock and records the operation only
when that gives the same tag; otherwise it answers `precondition_failed` and
records nothing. Execution revalidates these facts once more before the
removal commit point. A mismatch never falls through to a generic “are you
sure”: it fails the operation as stale, and the user reviews a new dry run of
the updated facts.

## Discard

Discard ends the session and removes its runtime while retaining the current P
branch as an ordinary retained source branch.

After an accepted run, discard completes forward through:

1. guard the assigned P ref against further writes;
2. revalidate the assigned tip, runtime identity, and missing-runtime proof;
3. if the planned facts still hold, mark the registry and operation
   `removing` and disable the session's P Git and session-RPC authority;
4. persist the runtime-removal commit point; record the stop and stop a
   running runtime as [Stop](#stop) does, sending the stop again when a
   recovery finds it recorded, since stopping is idempotent; then record the
   delete and ask the runtime driver to delete the runtime at its reviewed
   incarnation. A delete whose outcome is unknown is settled through the
   driver and never sent again: only a delete the driver settles as applied
   lets the removal go on to prove the runtime absent, and any other answer
   keeps it blocked;
5. request each participating plugin's required credential revocation and
   remove P-owned local secret copies;
6. remove other session credentials;
7. verify that the P branch still exists at the guarded expected tip, unless
   the assigned ref was already missing at the dry run;
8. end the UUID-to-branch assignment and remove the session row; and
9. release the guard and leave the branch visible as a retained project
   branch.

Changed facts discovered in step 2 fail the operation as stale, release the
guard and leave the established session and authorities unchanged.

Once the runtime-removal commit point is persisted, discard never recreates the
runtime to roll back. Reconciliation completes cleanup forward. Failure to
revoke a required key retains the disabled session identity and durable cleanup
operation until revocation is verified; it never recreates a removed runtime.

## Delete

Delete performs the same runtime and credential removal as discard and also
deletes the assigned P branch.

After runtime removal verifies, P:

1. checks that the guarded branch still has the planned object ID;
2. records the deletion as issued and has the source driver delete that head
   only while it names the planned object ID (`DeleteBranch`);
3. ends the assignment and removes the session row; and
4. retains bounded operation diagnostics after all required cleanup verifies.

When the assigned ref was already missing at the dry run, steps 1 and 2 have
nothing to delete: P proves the branch still absent and sends no deletion. A
deletion whose outcome was lost is never sent again; the delete goes on only
once the source driver settles it as applied (`Settle`), also on a Retry.

Delete never deletes or renames an origin ref, external mount contents, or
another P ref containing the same commits. It never deletes an image itself;
once no session started from its commit remains, the daemon's release step
deletes that commit's image ([image cleanup](#credential-and-image-cleanup)).

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
- checks a commit image only when an operation needs to create an instance;
- validates local Git authorization state;
- asks participating plugins for required principal and resource state; and
- resumes an already authorized running operation according to its recorded
  phase, in the order the journal accepted them. A blocked operation, of any
  kind, records a condition its worker could not resolve alone; a restart is
  no evidence that it changed, so it waits for `operation.retry`.

Automatic reconciliation may finish an operation the user already authorized,
restore a missing local authorization record without widening it, or update an
observed condition. At startup it also runs the image release step once for
each project ([image cleanup](#credential-and-image-cleanup)). It never
deletes an unconfirmed branch, removes a runtime outside a committed removal
operation, resets a workspace, or creates a second runtime while the original
may exist.

### Inconsistent sessions

P has no session repair. When a session's authorities disagree in a way that
automatic reconciliation must not resolve, P reports the condition and refuses
the actions that depend on the missing fact. In most cases the user corrects
the outside fact, or discards the session and creates a new one on the
retained branch:

| Inconsistency | What happens and what to do |
|---|---|
| Incomplete authorized operation | Reconciliation resumes a running one, and `operation.retry` a blocked one, along its defined forward/rollback path. |
| Stale runtime locator | Outside MVP: instance names are derived from the session UUID, and no mutable locator is stored. P reports the missing expected runtime; a same-UUID runtime under another name blocks removal, and P does not adopt it. |
| Missing runtime, assigned P branch intact | Start refuses. Discard the session: its plan lists the runtime's contents as already gone ([destructive preflight](#destructive-preflight)). Then create a new session on the retained branch. Delete instead when the branch should go too. |
| Missing, revoked, or changed session Git principal or key | Start refuses. Discard, then create a new session on the retained branch. Discard removes the runtime, so rescue uncommitted work from it by hand first. |
| Runtime exists, assigned P ref missing | Discard or Delete the session; with no ref both end it the same way and leave no branch. Commits that exist only in the runtime are lost with it, so rescue them by hand first; P does not import them. Then create a new session on another branch. |
| Session row has neither runtime nor branch | Delete removes the record; Discard ends the same way. |
| Workspace branch/upstream mismatch | Show expected and actual branch/upstream values and block actions that depend on the assigned branch. The user or agent corrects Git manually; P rechecks on the next attempt. P performs no automatic checkout or reset. |

A new session on the retained branch starts from its committed tip. It cannot
recover uncommitted files, unpushed commits, processes, terminal state, or
conversations that existed only in the old runtime. It starts from the image
of the branch's current commit, which is a different image when the old session
moved the branch; when no session from that commit remains, P builds it again.

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

## Credential and image cleanup

Local P authorization follows the SQLite assignment. Marking removal
immediately prevents the session key from pushing even if a private
key file survives in unreachable machinery.

Every selected plugin's MVP lifecycle contract owns provider-specific
revocation and ensure-absent behavior. P coordinates those operations and, if
an external authority is unavailable, retains only the protected cleanup
handle required for idempotent retry. A post-MVP Bifrost plugin applies that
rule to its session virtual key. Upstream provider credentials never enter P's
lifecycle state.

A commit image is immutable and shared by every session started from its
commit; no session owns it. An image is kept while some session started from
its commit exists, in any registry state: `creating` (including a failed
creation waiting for Retry), `established` or `removing`. Discard and Delete
never delete an image themselves. Once the last session from a commit is gone,
the daemon's release step, which runs after each operation of the project and
at daemon start, deletes that commit's image; an operation never waits for it
or fails because of it. Deleting an image never affects a runtime created from
it, and a missing image is rebuilt the next time a creation needs it.
[Environment building](environment-building.md#lookup-concurrency-and-image-lifetime)
owns the release rule and its mechanics.

## Operation recovery summary

| Operation | Commit point | Before commit point | At/after commit point |
|---|---|---|---|
| Create | Existing P branch assigned, new branch created and assigned, or unborn bootstrap reserved | Cancel/clean reservation when safe; preserve pre-existing branch | Retain one blocked request; Retry cleans verified partial resources and reconstructs the same immutable request; changed input explicitly supersedes it with a new creation |
| Start | None; the `session.start` operation records the request | Nothing to undo: Incus/systemd own the start | The worker starts the runtime and waits for `p-interactive.service`; success completes the operation once ready, failure records it failed with the diagnostic and returns the container to stopped, and another Start records a new operation |
| Attach | Pending token claimed and its terminal opened | Expire pending token without presence/status clear | The daemon keeps the lease until the runtime driver proves the terminal ended, settles it as ended, or the session stops; a daemon restart ends it with no re-registration |
| Rename | New P ref created | Release reservation and retain old mapping | Complete forward to new ref under guards |
| Stop | None; the `session.stop` operation records the request | Nothing to undo: Incus owns the stop | The worker stops the runtime; the operation completes once it is stopped, or fails with the diagnostic |
| Discard | Runtime removal committed | Revalidate or cancel without loss | Complete runtime/credential cleanup; retain branch |
| Delete | Runtime removal committed | Revalidate or cancel without loss | Complete cleanup and confirmed branch deletion |

No client blindly retries an ambiguous durable mutation with a new
idempotency key. It queries the existing operation and lets reconciliation
determine the observed result.

## RPC and presentation

Lifecycle methods evolve with implementation, but each exposes the
same concepts:

- operation and idempotency IDs;
- current phase, and once blocked or failed a reason code and a diagnostic of
  at most 1024 bytes;
- registry state, observed session and policy condition, project, branch, and
  UUID;
- a risky change's plan and change tag; and
- retry, cleanup, or removal actions permitted by the current facts.

Attach returns a short-lived one-use pending token and no runtime details. The
trusted host helper claims it on its own control connection; the daemon opens
the terminal, counts the attachment, and relays the terminal over that
connection, keeping the lease until the terminal's end is proven
([attach and detach](#attach-and-detach)).

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
5. an attachment whose terminal failed to open does not increment presence or
   clear unattended status, while a claim that opened it performs both
   atomically;
6. rename of a stopped session preserves UUID, workspace files, local-ahead
   commits, and credentials while changing both Git refs safely, and rename
   refuses a running session;
7. a removal's change tag stops matching when the assigned tip, the runtime
   identity, or delete's branch loss changes, and still matches when the
   session is only started or stopped;
8. discard preserves the assigned branch as retained, delete removes only the
   confirmed P ref, both stop a running session themselves, and both proceed
   when the assigned ref is already missing;
9. missing and unreachable runtimes take different cleanup paths;
10. Incus unavailability leaves cleanup incomplete with identity, durable
    operation state and authorization restrictions retained; when it returns,
    Retry/reconciliation resume only confirmed work without duplicates or
    adoption of unfamiliar machinery;
11. daemon restart drops pending attachment tokens and active leases and ends
    their terminals, while systemd preserves the persistent host, and
    operation reconciliation resumes;
12. abrupt client death or transport loss ends only its temporary attach
    client, while the daemon retains attachment presence until the runtime
    driver proves that client ended; a runtime driver failure keeps the
    presence until the driver settles the terminal or the session stops;
13. no automatic cleanup deletes a branch, runtime, or orphan record based only
    on age;
14. activation or persistent-host failure leaves the container stopped with a
    bounded inspectable diagnostic and an ordinary Start retry;
15. host exit shuts down the container while detach or switching sessions does
    not; and
16. branch/upstream mismatch diagnostics show expected and actual values,
    block dependent actions, and recheck manually corrected Git on the next
    attempt without automatic checkout/reset.
