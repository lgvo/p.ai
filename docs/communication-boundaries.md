# P — communication boundaries

What crosses Git, RPC, SSH, attachment, model-gateway, build, and notification
channels.

> **Status: design.** This document is authoritative for protocol division,
> audiences, and trust boundaries. Git carries source history. RPC carries
> control and status. Neither substitutes for the other.
> [session-lifecycle.md](session-lifecycle.md) owns the operations that use
> these channels, while [runtime-isolation.md](runtime-isolation.md) owns the
> runtime-side exposure and isolation of endpoints.

## Contents

- [The rule](#the-rule)
- [SSH roles](#ssh-roles)
- [Control RPC](#control-rpc)
- [P Git data plane](#p-git-data-plane)
- [Session creation and branch identity](#session-creation-and-branch-identity)
- [Branch rename](#branch-rename)
- [Origin communication](#origin-communication)
- [Destructive operations](#destructive-operations)
- [Image-build communication](#image-build-communication)
- [Future attempts and Git-triggered checks](#future-attempts-and-git-triggered-checks)
- [Attachment](#attachment)
- [Model-gateway traffic](#model-gateway-traffic)
- [Notification delivery](#notification-delivery)
- [Listeners](#listeners)
- [Failures, retries, and versioning](#failures-retries-and-versioning)
- [V1 boundary](#v1-boundary)

## The rule

| Channel | Carries | Does not carry |
|---|---|---|
| P Git | commits, objects, session branches, read-only host fetches | lifecycle, runtime state, credentials, status |
| Origin Git | configured SSH refresh and explicit publication | P registry, runtime state, P-private policy |
| Host RPC | lifecycle, configuration, status, subscriptions | source trees or arbitrary command execution |
| Session RPC | identity, capabilities, latest agent condition, narrow act-on-self calls | other sessions, publication, host control |
| Attachment | one validated interactive argv and terminal byte stream | lifecycle API or source transfer |
| Bifrost HTTP | approved model discovery and inference under a mandatory virtual key | P lifecycle; session keys are rejected by Bifrost administration and every non-V1 surface |
| Incus builder | immutable commit input, bounded Nix capabilities, environment-image publication | ambient host authority or session identity |
| Notification sink | selected status transition | source contents by default |

Git already defines source transfer and ref integrity. JSON-RPC expresses the
actions and events that are not source history.

## SSH roles

SSH is a carrier used in three independent places:

1. A remote Linux client initiates SSH to bridge the daemon's Unix RPC socket
   and run a validated attachment command on that instance.
2. The P Git listener authenticates host and session principals before running
   fixed `git-upload-pack` or `git-receive-pack` argv.
3. Host-side origin operations use the host user's existing OpenSSH identity.

The daemon never initiates SSH to another P host and P instances never form a
control-plane network.

## Control RPC

The daemon exposes newline-delimited JSON-RPC 2.0 over a Unix socket. One line
is one request, response, or notification. Long operations return an operation
ID and publish phase updates; arbitrary stdout is never multiplexed into JSON.

### Host RPC audience

The local user, TUI, and `p api` share the complete lifecycle surface:

| Area | Examples |
|---|---|
| System | hello, health, protocol and build versions |
| Projects | register, inspect, reload trusted project configuration |
| Sessions | list, create, attach, rename, stop, discard, delete, repair, abandon |
| Remotes | configure, refresh, inspect origin state, explicitly publish |
| Observability | runtime condition, startup readiness, confirmed attachment presence, latest unattended condition, subscriptions |
| Configuration | validated effective configuration and diagnostics |

RPC accepts identifiers and structured data, not repository contents or
client-selected shell commands.

### Session RPC audience

Each runtime receives a private per-session Unix socket bound server-side to
its immutable session UUID. Its allowed surface is deliberately narrow:

| Allowed | Denied |
|---|---|
| query its UUID, current branch name, and effective project capabilities | list or inspect other sessions |
| report the latest source-scoped agent condition | invoke any lifecycle operation |
| receive method/version errors for its own calls | publish to `origin` or change credentials, runtime, network policy, or mounts |

The socket authenticates by runtime placement; a request cannot supply a
different session UUID.

### Observability over RPC

Host clients query the four-field model from
[session-observability.md](session-observability.md): runtime condition,
startup readiness, confirmed attachment count, and latest unattended
condition.

Session adapters send `status.report` notifications. There is no mark-seen API,
attention collection, participant inventory, or status history in v1.

## P Git data plane

Each complete project path identifies one bare repository in the P instance.
Every session has an immutable UUID and exactly one logical branch address:

```text
session UUID ↔ (project path, refs/heads/<branch>)
```

The same branch name may exist in different projects. The ref name may change
through rename; ownership by the UUID does not. Identity and assignment
lifetime are defined by
[session lifecycle](session-lifecycle.md#identity-and-retained-state).

### Session principal

Each session receives a distinct SSH key bound to `(project, session UUID,
current ref)`.

- It may read ordinary `refs/heads/*` in its project.
- It may push only its assigned current branch.
- Push is fast-forward-only unless trusted host policy explicitly grants a
  narrow rewrite exception; force-push is denied by default.
- It cannot write tags, another session branch, `refs/attempts/*`, `refs/p/*`,
  or other reserved namespaces.
- Hidden/private namespaces are not advertised and arbitrary object-ID fetches
  are disabled.

### Host principal

The per-instance host SSH key is read-only on the P Git server.

- It may clone and fetch every user-visible branch, including retained
  unassigned branches.
- It cannot create, rename, update, force-push, or delete P-server refs.
- It never enters a session runtime.

Host-side work that needs to update P becomes a session lifecycle operation,
not a second writer to an existing branch.

### Daemon authority

Only the daemon may create, rename, guard, or delete P refs. It invokes real
Git plumbing and server hooks; P does not implement the Git wire protocol or
duplicate Git objects in SQLite.

## Session creation and branch identity

Creation is the lifecycle operation defined in
[session-lifecycle.md](session-lifecycle.md#create). RPC carries a structured
committed-source selection and desired new branch; it does not carry source
files. Git carries the commit objects and the new ref.

If the committed source exists only in a registered host checkout, the create
operation asks the daemon to import that exact commit and its required Git
objects into the bare P repository using local Git plumbing. The RPC carries
only the selector, not source contents; the daemon reads no working-tree state,
and the read-only host P principal gains no write authority. An explicit commit
must be reachable from a registered source known to the project.

Dirty host-checkout state is never copied or committed by P. The user commits
desired changes before selecting them as a source.

## Branch rename

Rename uses host RPC for intent and progress, daemon Git authority for the P
refs, and a quiesced Incus workspace for the local branch/upstream change.
Git pushes to the affected refs are guarded while the operation is incomplete.
The transaction and recovery rules are defined in
[session lifecycle](session-lifecycle.md#rename).

No rename request, result, or recovery step renames or deletes an `origin` ref.
Publishing the new name is a separate explicit origin operation.

## Origin communication

An origin is optional. A project has zero or one configured origin. Projects
without one are local-only and bypass every origin-dependent operation.

### Origin identity and configuration

The configured origin is a trusted host-side project setting, not a remote
copied from a session workspace. Registration may propose a remote discovered
in a registered checkout, but P records it only after the user selects it. Its
identity is the recorded remote URL; `origin` is only P's presentation name.

V1 accepts SSH Git URLs, including `ssh://` and conventional
`user@host:path` forms. HTTPS credentials, embedded URL credentials, local
filesystem remotes, and arbitrary transport helpers are not supported. A local
repository is registered as a source location rather than disguised as an
origin.

Changing or removing the origin is explicit. A URL change immediately marks
the last origin observation inapplicable; P makes no source, preservation, or
publication claim about the new origin until a successful refresh. It never
rewrites session remotes because sessions have only their P upstream.

### Host SSH and Git runner

Every origin operation runs on the daemon host as the P user's account and uses
that user's existing OpenSSH configuration, private-key files, known-hosts
policy, and SSH agent when available. P stores no copy of those credentials and
never mounts them into a runtime or build worker.

The origin runner:

- invokes real Git with structured argv and the recorded URL;
- is non-interactive (`BatchMode`/no Git terminal prompt), so a missing key,
  passphrase agent, or host-key decision fails with an actionable diagnostic;
- ignores repository-controlled Git configuration, hooks, credential helpers,
  upload-pack overrides, and environment variables outside a small host SSH
  allowlist; and
- may use trusted user OpenSSH features, including host aliases and proxy
  configuration, because that file is part of the selected host authority.

No origin URL or branch value is interpolated into a shell command.

### Origin observation

A refresh observes the origin's currently advertised branches and tags and
fetches the objects needed for the requested comparison or source selection.
It never updates, deletes, merges, or checks out an ordinary P branch.

SQLite may record the current bounded observation:

```text
project and origin URL identity
successful completion time
advertised ref names and object IDs needed by the current view
result status and bounded diagnostic
```

P creates no persistent origin-generation or publication refs in the project
bare repository. Fetched objects are cache, not durable evidence. P serializes
origin operations and P-controlled Git garbage collection for a project so an
active comparison cannot lose objects underneath it. When origin source
selection succeeds, the newly created ordinary P branch retains the selected
commit.

A failed or interrupted refresh leaves the last completed observation only as
stale presentation data and marks current origin state unknown. It is not used
to authorize source selection, publication, or a destructive-preservation
claim.

There is no ahead/behind or “published” truth without a successful refresh.
Every correctness-sensitive operation consumes its fresh observation while
holding the per-project origin-operation lock; it does not preserve historical
origin snapshots.

### Refresh triggers

V1 performs a refresh:

- once when an origin is initially configured; failure does not undo project
  registration;
- on explicit user request;
- before resolving a source explicitly selected from current origin state;
- before every P-managed publication; and
- before destructive preflight makes origin-preservation claims.

Origin source selection and publication require their refresh to succeed. If
it fails, the user may still select an exact commit already reachable in P, but
P does not describe that commit as current origin state. Destructive preflight
may continue after refresh failure only by displaying origin preservation as
`unknown`, as defined by session lifecycle.

Background refresh is an optional best-effort convenience. It may update the
overview, but its presence, interval, or success is never a correctness
dependency and it does not authorize publication or deletion. A
correctness-sensitive operation performs its own refresh rather than relying
on a wall-clock freshness threshold.

Concurrent origin operations for one project are serialized. A waiting
read-only refresh may reuse the result of the in-progress required refresh,
but publication and destructive preflight evaluate their own current
observation after acquiring the lock.

### Origin sources

After a successful refresh, an advertised origin branch or tag is committed
source input. Creation copies its exact object ID into a newly created P
session branch. The session receives neither an origin ref nor an origin
remote, and later origin movement does not move the P branch.

P never imports dirty origin state—Git has none—and never turns a failed fetch
into a partially created session source. Registered host checkout commits and
ordinary P refs remain separate source paths.

### Publication preconditions

Publication is an explicit, idempotent host RPC action with this meaning:

```text
ensure origin/<destination> contains <captured P source commit>
```

It is permitted only when:

- the source belongs to an established session with no conflicting lifecycle
  operation and a stable assigned P branch;
- the P source ref exists and its exact object ID has been captured;
- the request supplies one explicit destination under
  `refs/heads/<branch>`; and
- a new origin refresh successfully observes that destination as absent or at
  an exact object ID.

The TUI may suggest the current session branch name, but the structured request
always contains the complete destination. The confirmation/preview names the
origin URL, project, source P ref and object ID, destination ref, observed
destination object ID or absence, and fast-forward relation.

The destination must pass Git branch-ref validation.

Publication uses only the P branch tip. Uncommitted files and commits still
ahead only in the runtime workspace are not included; when P can observe them,
the preview warns about them but never commits or pushes them implicitly.

### Publication update rule

After the required refresh:

- an absent destination is created with one explicit refspec;
- a destination equal to the source already satisfies the request;
- a destination descending from the source already contains the requested
  work and satisfies the request;
- a destination that is an ancestor of the source receives one normal
  fast-forward push; and
- a divergent destination is refused with the observed object IDs.

Git on the origin is the final authority and atomically accepts or rejects the
non-force update if the destination races after P's refresh. P never sends
tags, multiple refs, deletion refspecs, a wildcard, or a force update. The
trusted rewrite exception for a session's assigned P ref does not apply to
origin.

Any definite failure is reported without another action. If transport loss
makes the outcome unknowable, P reports `outcome unknown`; it does not retry,
refresh, or reconcile automatically. A later explicit publication request
starts from a fresh observation and applies the same idempotent rules. No
publication-specific recovery record or protected Git ref is required.

P does not rename/delete an old origin branch after session rename and never
automatically publishes after create, push to P, rename, detach, stop, discard,
or delete. A user who intentionally rewrites an origin branch does so with
ordinary Git outside P and then refreshes P's observation.

The manual equivalent always remains available: fetch the session branch
through the read-only host P credential, then push it to origin with normal
user Git.

## Destructive operations

Destructive preflight and lifecycle intent travel over host RPC. Workspace loss
is obtained through non-activating Incus inspection; branch loss comes from
Git reachability and optionally observed origin branches. Runtime files and Git
objects never travel inside the RPC request.

Discard, delete, missing/unreachable behavior, confirmation fingerprints,
credential cleanup, and abandonment are defined in
[session lifecycle](session-lifecycle.md#destructive-preflight).

## Image-build communication

Project-controlled evaluation and builds run through the isolation boundary in
[environment-building.md](environment-building.md).

- The daemon passes an immutable commit snapshot and validated Nix build plan
  to a disposable builder in the confined Incus project.
- The builder receives a private root and bounded scratch space; it receives no
  session identity, filesystem grant, or Incus socket.
- Public substituter/fetch egress may be allowed; host/LAN/private access and
  ambient credentials are denied.
- After verification and scrubbing, P asks Incus to publish the builder as a
  private image. Incus returns the fingerprint and owns the bytes; RPC carries
  only progress, bounded diagnostics, cache metadata, and the fingerprint.

## Future attempts and Git-triggered checks

Checks and attempts are deferred and keep separate reserved namespaces:

| Reserved namespace | Future purpose |
|---|---|
| `refs/attempts/*` | session-owned approach refs, if attempts are later designed |
| `refs/p/*` | P-owned check requests, results, and protocol metadata, if checks are later designed |

V1 denies writes to both and exposes no scheduler, result protocol, promotion
flow, lifecycle rule, or configuration for either feature.

A future design may give attempts their own immutable ID, ref, RPC status, and
bounded runtime. It must preserve the existing division: Git holds input/output
commits while RPC carries scheduling and status. A future Git-triggered check
must observe accepted ref changes after Git commits them; it must not overload
magic pushes as a lifecycle API. Exact layouts below either reserved namespace
remain undefined until that feature enters implementation scope.

## Attachment

Attachment is an RPC decision followed by client-side execution:

1. the client calls the attach method;
2. the daemon validates or starts the runtime as allowed;
3. the configured `InteractiveHost` produces fixed inner argv for the selected
   command only after a fresh host check succeeds independently of startup
   readiness;
4. the Incus backend wraps it into a structured `AttachSpec` for that runtime;
5. the daemon returns the spec with a short-lived, one-use pending attachment
   token;
6. the local client invokes the trusted host helper directly, or a remote Linux
   client invokes it through client-initiated SSH, transferring the token over
   private control input rather than argv;
7. the helper opens a dedicated attachment RPC connection, establishes the
   interactive channel, and confirms the token on that connection;
8. the daemon promotes it to a helper-owned attachment lease; and
9. the helper retains the lease until channel teardown completes whenever the
   daemon remains reachable, then closes it.

A failed channel establishment or expired pending token never increments
attachment presence or clears unattended status. Only confirmation does so.
The helper binds the channel to both lease and client carrier independently of
client cooperation. Client crash/SIGKILL, SSH/network loss, or carrier closure
starts teardown. If daemon restart removes the lease first, the helper starts
teardown immediately and establishes no new lease or channel until it finishes.
Tmux detaches only that client while retaining its server/session. Direct's
runtime wrapper terminates and waits for the command on exec-channel loss, so
abrupt client/helper death cannot leave an uncounted live direct command. No V1
RPC re-registers an existing channel.

The interactive command may be `bash`, Claude Code, Codex, or custom argv.
`tmux` is the default persistent interactive host; `direct` is the minimal
non-persistent implementation. P never returns a shell string. Persistence
across detach is an interactive-host capability, not session identity.
Start, lease, and status-clear ordering are defined in
[session lifecycle](session-lifecycle.md#attach-and-detach).

## Model-gateway traffic

Bifrost is an independently configured service and the authority for provider
credentials, models, routing, MCP, budgets, and virtual-key policy.

- P stores the Bifrost endpoint plus the session virtual-key ID and token.
- P delivers that token only to its session runtime and redacts it from logs,
  diagnostics, operation records, and ordinary RPC responses.
- The service may be network-reachable, but every inference request requires a
  valid session virtual key. That key succeeds only for approved inference and
  filtered model discovery and is rejected for dashboard, management,
  governance, logs, MCP, skills, and every other non-V1 route.
- P verifies the pinned Bifrost route/authentication boundary and fails model
  access closed when configuration, version, positive probes, negative probes,
  or route inventory are not validated.
- Projects without model grants receive no key and do not depend on Bifrost
  readiness.
- Session discard or deletion follows the revocation and cleanup rules in
  [session lifecycle](session-lifecycle.md#credential-and-image-cache-cleanup).

Upstream provider credentials never enter P state or session configuration.

## Notification delivery

Notification sinks receive selected transitions from the latest unattended
condition. P sends no semantic notification while attached. Remote bodies are
redacted by default and do not carry source files, Git objects, credentials, or
arbitrary RPC payloads.

## Listeners

| Listener | Exposure | Purpose |
|---|---|---|
| Host control RPC | user-owned Unix socket | TUI and `p api` |
| Per-session RPC | private mounted Unix socket | identity and status reports |
| P Git SSH | host loopback plus explicit runtime path | fixed Git services |
| Bifrost inference | explicit runtime path when granted | model APIs under virtual key |
| Bifrost administration | authenticated with a host-only credential; session keys rejected | native Bifrost configuration |

No Incus socket, host SSH agent, host control socket, or general LAN
route is mounted into a session.

## Failures, retries, and versioning

| Operation | Rule |
|---|---|
| Read-only RPC | client may retry after reconnect |
| Cross-authority mutating RPC | operation/idempotency key plus persisted P phase |
| Incus-owned mutation | query the Incus operation and current state; do not duplicate its phases |
| Status notification | latest valid receive wins while unattended |
| Git push | normal Git atomic/ref semantics; explicit retry |
| Origin refresh | interrupted refresh leaves current state unknown; the next explicit operation refreshes again |
| Origin publish | idempotent ensure operation; report failure or unknown outcome and wait for explicit retry |
| Start on running `not_ready` | return stable `stop_required`; recovery is Stop → Start with a new startup generation |
| Attachment | the helper retains a reachable lease through teardown; client/carrier/lease loss triggers transport-bound teardown, and daemon-restart lease loss permits no new helper channel before completion |

RPC and status payloads carry explicit version fields. Git protocol behavior is
Git's; P versions its ref-policy implementation and stored operation schema.
Migrations are forward-only and startup fails clearly when state is newer than
the binary.

## V1 boundary

V1 includes local Unix RPC and client-initiated SSH-to-Unix RPC for Linux
clients, P Git over SSH, session-scoped Git principals, read-only host Git
access, committed-state session creation, transactional branch rename,
SSH origin refresh, explicit fast-forward-only origin publication, per-session
status sockets, durable current-generation startup readiness, structured
pending-to-confirmed attachment, optional Bifrost inference under the validated
native authentication boundary, and notification sinks.

Native macOS/Windows clients, attempts, checks, and additional network/backend
claims remain later work or gated validations.
