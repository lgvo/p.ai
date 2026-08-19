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
| Bifrost HTTP | model discovery and inference under a virtual key | P lifecycle or Bifrost administration from sessions |
| Build worker | immutable commit input, bounded build capabilities, artifacts | ambient host authority or runtime identity |
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
| Observability | runtime condition, attachment presence, latest unattended condition, subscriptions |
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
| receive method/version errors for its own calls | publish to `origin` or change credentials, backend, network policy, or mounts |

The socket authenticates by runtime placement; a request cannot supply a
different session UUID.

### Observability over RPC

Host clients query the three-field model from
[session-observability.md](session-observability.md): runtime condition, live
attachment count, and latest unattended condition.

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
- It cannot write tags, another session branch, origin-tracking refs,
  `refs/attempts/*`, `refs/p/*`, or other reserved namespaces.
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

Only the daemon may create, rename, delete, or write protected tracking refs.
It invokes real Git plumbing and server hooks; P does not implement the Git
wire protocol or duplicate Git objects in SQLite.

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
refs, and a quiesced backend workspace for the local branch/upstream change.
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
the old tracking generation inapplicable; P makes no source, preservation, or
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

### Tracking state

A refresh maps the origin's advertised branches and tags into a new
generation-specific daemon-owned namespace in the P bare repository. Session
principals and the read-only host P principal cannot see or write protected
tracking generations. Refresh never updates, deletes, merges, or checks out an
ordinary P branch.

SQLite records only observation metadata:

```text
project and origin URL identity
refresh generation
successful completion time
advertisement/result status and bounded diagnostic
```

Git remains authoritative for the protected tracking refs and objects. P first
finishes and verifies the new generation, then atomically changes SQLite's
current-generation pointer. That pointer change is the refresh commit point.
Refs absent from the new advertisement are absent from the new logical view;
older generation namespaces may be collected after they lose all readers.
Publication operations and unexpired destructive-confirmation tokens retain a
generation lease until they complete or expire.

A failed or interrupted refresh before the pointer change leaves the previous
successful generation intact and marks current origin state stale/unknown. Its
incomplete generation is cleanup state and is never presented. After the
pointer change, interruption is reconciled as a successful new generation even
if old-generation cleanup remains.

There is no ahead/behind or “published” truth without a successful refresh.
Comparisons always name the generation and exact object IDs they used.

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
dependency and it does not authorize publication or deletion. P uses a new
operation-specific generation for the required triggers above rather than a
wall-clock freshness threshold.

Concurrent refreshes for one project coalesce. Publication waits for or starts
a new required generation; it never races a second ref update against the same
tracking namespace.

### Origin sources

After a successful refresh, an advertised origin branch or tag is committed
source input. Creation copies its exact object ID into a newly created P
session branch. The session receives neither the tracking ref nor an origin
remote, and later origin movement does not move the P branch.

P never imports dirty origin state—Git has none—and never turns a failed fetch
into a partially created session source. Registered host checkout commits and
ordinary P refs remain separate source paths.

### Publication preconditions

Publication is always an explicit host RPC action. It is permitted only when:

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

The destination must pass Git branch-ref validation. A destination already at
the captured source object succeeds without sending a push.

Publication uses only the P branch tip. Uncommitted files and commits still
ahead only in the runtime workspace are not included; when P can observe them,
the preview warns about them but never commits or pushes them implicitly.

### Publication update rule

V1 publication creates an absent destination or fast-forwards an existing
destination. It pushes one explicit source-object-to-destination refspec and
never sends tags, multiple refs, deletion refspecs, or a wildcard.

If the destination is not an ancestor of the captured source object, P refuses
publication and shows the divergent object IDs. P exposes no force-publication
operation in V1. The trusted rewrite exception for a session's assigned P ref
does not apply to origin. A user who intentionally rewrites an origin branch
does so with ordinary Git outside P and then refreshes P's observation.

A remote race or branch-protection rule may still reject a valid preview. P
reports the remote result and refreshes before offering another attempt. It
does not rename/delete an old origin branch after session rename and never
automatically publishes after create, push to P, rename, detach, stop, discard,
or delete.

### Ambiguous publication

P never blindly retries when transport loss makes a push result ambiguous. It
records the attempted source/destination object IDs and refreshes:

- destination equal to the attempted source is presented as satisfying the
  requested update, without claiming which writer produced it;
- destination at another object is presented as not completed/conflicted; and
- refresh failure leaves the operation outcome unknown and requires a later
  explicit refresh.

This operation record is recovery state, not a permanent publication ledger.
Once current Git refs establish the result, ordinary origin tracking remains
the only publication evidence.

The manual equivalent always remains available: fetch the session branch
through the read-only host P credential, then push it to origin with normal
user Git.

## Destructive operations

Destructive preflight and lifecycle intent travel over host RPC. Workspace loss
is obtained through a non-activating backend inspection; branch loss comes from
Git reachability and optional refreshed origin refs. Runtime files and Git
objects never travel inside the RPC request.

Discard, delete, missing/unreachable behavior, confirmation fingerprints,
credential cleanup, and abandonment are defined in
[session lifecycle](session-lifecycle.md#destructive-preflight).

## Image-build communication

Project-controlled evaluation and builds run through the isolation boundary in
[environment-building.md](environment-building.md).

- The daemon passes an immutable commit snapshot and validated job description.
- The worker receives bounded scratch/output paths and provider-specific cache
  capabilities.
- Public artifact egress may be allowed; host/LAN/private access and ambient
  credentials are denied.
- The worker returns normalized artifact metadata and diagnostics, never
  lifecycle authority.

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
   command;
4. the backend wraps it into a structured `AttachSpec` for that runtime;
5. the daemon opens a connection-bound attachment lease only after the spec is
   ready;
6. the local client executes it, or a remote Linux client runs it through a
   client-initiated SSH channel; and
7. client exit or transport closure ends the lease.

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
- Sessions reach inference/model discovery only; dashboard and management
  routes remain unreachable from runtime networks.
- Projects without model grants receive no key and do not depend on Bifrost
  readiness.
- Session discard or deletion follows the revocation and cleanup rules in
  [session lifecycle](session-lifecycle.md#credential-and-artifact-cleanup).

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
| Bifrost administration | host-only/authenticated | native Bifrost configuration |

No container-engine socket, host SSH agent, host control socket, or general LAN
route is mounted into a session.

## Failures, retries, and versioning

| Operation | Rule |
|---|---|
| Read-only RPC | client may retry after reconnect |
| Durable mutating RPC | operation/idempotency key plus persisted phase |
| Status notification | latest valid receive wins while unattended |
| Git push | normal Git atomic/ref semantics; explicit retry |
| Origin refresh | generation pointer is the commit point; retain the prior generation before it, complete cleanup after it |
| Origin publish | no blind retry after ambiguous outcome; refresh refs first |
| Attachment | reconnect performs normal attach flow; host capability determines continuity |

RPC and status payloads carry explicit version fields. Git protocol behavior is
Git's; P versions its ref-policy implementation and stored operation schema.
Migrations are forward-only and startup fails clearly when state is newer than
the binary.

## V1 boundary

V1 includes local Unix RPC and client-initiated SSH-to-Unix RPC for Linux
clients, P Git over SSH, session-scoped Git principals, read-only host Git
access, committed-state session creation, transactional branch rename,
SSH origin refresh, explicit fast-forward-only origin publication, per-session
status sockets, structured attachment, optional Bifrost inference, and
notification sinks.

Native macOS/Windows clients, attempts, checks, and additional network/backend
claims remain later work or gated validations.
