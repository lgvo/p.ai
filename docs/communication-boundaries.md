# P — communication boundaries

What crosses Git, RPC, SSH, attachment, model-gateway, build, and event-handler
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
- [Daemon and runtime driver](#daemon-and-runtime-driver)
- [Image-build communication](#image-build-communication)
- [Future attempts and Git-triggered checks](#future-attempts-and-git-triggered-checks)
- [Attachment](#attachment)
- [Post-MVP model-gateway traffic](#post-mvp-model-gateway-traffic)
- [Event delivery](#event-delivery)
- [Listeners](#listeners)
- [Failures, retries, and versioning](#failures-retries-and-versioning)
- [MVP boundary](#mvp-boundary)

## The rule

| Channel | Carries | Does not carry |
|---|---|---|
| P Git | commits, objects, session branches, read-only host fetches | lifecycle, runtime state, credentials, status |
| Origin Git | configured SSH refresh and explicit publication | P registry, runtime state, P-private policy |
| Host RPC | lifecycle, configuration, status, subscriptions | source trees or arbitrary command execution |
| Session RPC | identity, capabilities, latest agent condition, narrow act-on-self calls | other sessions, publication, host control |
| Attachment | one fixed attach entrypoint's terminal byte stream, relayed by the daemon | lifecycle API, source transfer, or runtime authority |
| Runtime driver | the daemon's calls of driver operations in P's terms, the driver's gate calls back, and each terminal's bytes on that terminal's own connection | client or session connections; lifecycle and policy decisions, which stay in core |
| Bifrost HTTP (post-MVP) | approved model discovery and inference under a mandatory virtual key | P lifecycle; not an MVP session channel |
| Incus builder | a Git bundle of one source commit, bounded Nix capabilities, commit-image publication | ambient host authority or session identity |
| Event handler | typed, versioned reduced P events | source contents, credentials, or source-of-truth state |

Git already defines source transfer and ref integrity. JSON-RPC expresses the
actions and events that are not source history.

## SSH roles

SSH has three independent potential roles:

1. After MVP, a remote Linux client may initiate SSH to bridge the daemon's
   Unix RPC socket and run a validated attachment command on that instance.
2. The P Git listener authenticates host and session principals before running
   fixed `git-upload-pack` or `git-receive-pack` argv.
3. Host-side origin operations use the host user's existing OpenSSH identity.

The daemon never initiates SSH to another P host and P instances never form a
control-plane network.

## Control RPC

The daemon exposes newline-delimited JSON-RPC 2.0 over a Unix socket. One line
is one request, response, or notification. Long operations return an operation
ID and publish phase updates; arbitrary stdout is never multiplexed into JSON.
The [local control API reference](control-api.md) fixes the current control
protocol, `p.control/v2`, which each connection agrees with `system.hello`, and
its request, response, method, error, and host configuration schemas.

### Host RPC audience

The local user, the TUI, the CLI's task commands and `p api` share the complete
lifecycle surface:

| Area | Examples |
|---|---|
| System | hello, health, protocol and build versions |
| Projects | create, inspect, configure/remove origin, delete all P-owned data |
| Sessions | list, create, attach, rename, stop, discard, delete |
| Remotes | configure, refresh, inspect origin state, explicitly publish |
| Observability | session condition, attachment count, latest unattended condition, policy condition, subscriptions |
| Configuration | validated effective configuration and diagnostics |

RPC accepts identifiers and structured data, not repository contents or
client-selected shell commands.

### Session RPC audience

Each runtime receives `/run/p/session.sock`, a private per-session Unix socket
bound server-side to its immutable session UUID. Its allowed surface is
deliberately narrow:

| Allowed | Denied |
|---|---|
| query its UUID, current branch name, and effective project capabilities | list or inspect other sessions |
| report the latest source-scoped agent condition | invoke any lifecycle operation |
| receive method/version errors for its own calls | publish to `origin` or change credentials, runtime, network policy, or mounts |

The socket authenticates by runtime placement; a request cannot supply a
different session UUID. The host socket mount and its filesystem permissions
follow [runtime isolation](runtime-isolation.md#filesystem-grants).

### Observability over RPC

Host clients query the four independent facts from
[session-observability.md](session-observability.md): session condition,
confirmed attachment count, latest unattended condition, and policy condition.

Session adapters send `status.report` JSON-RPC notifications (the protocol
message type). There is no mark-seen API, attention collection, participant
inventory, or status history in MVP.

MVP ships and validates only the Codex adapter. Codex runs as an ordinary
command, and the user authenticates it within the session's private home. P
does not carry host Codex or OpenAI credentials into the session. Other agents
may run as commands but have no supported status adapter in MVP. Networked
Codex use relies on the project's validated `public-egress` grant rather than a
P-provided model endpoint.

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
current ref)`. Its fixed stream helper connects SSH to `/run/p/git.sock`;
credential placement and ownership follow
[runtime isolation](runtime-isolation.md#credentials).

- It may read ordinary `refs/heads/*` in its project.
- It may push only its assigned current branch.
- Every push is fast-forward-only. P has no force-push exception.
- It cannot write tags, another session branch, `refs/attempts/*`, `refs/p/*`,
  or other reserved namespaces.
- Hidden/private namespaces are not advertised and arbitrary object-ID fetches
  are disabled.

The endpoint manager admits at most eight Git proxy connections per session
and 64 across the instance. The listener's session UUID determines admission;
SSH still authenticates the Git principal. Each proxy has an independent
ten-minute lifetime, including connection setup, matching the Git SSH service
duration limit. Client input EOF permits the upstream response to drain.
Upstream EOF, the lifetime deadline, daemon shutdown, or context cancellation
closes both proxy directions and releases its connection slots.

### Host principal

The per-instance host SSH key is read-only on the P Git server.

- It may clone and fetch every user-visible branch, including retained
  branches.
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
project and branch choice: either an existing unassigned P branch, with no
separate source, or a new branch name plus committed-source selection. It does
not carry source files. Git carries commit objects and refs; only the
new-branch path creates a ref. The existing-branch path captures the branch's
tip and assigns it without rewriting Git history.

Committed source comes from an ordinary P ref or from a freshly contacted
configured origin. The bootstrap exception for a new blank/empty project has
an unborn `main` and no source commit. P does not register, inspect, or import
host checkouts. Project creation, origin association, bootstrap, and retained
branch behavior are defined in
[project-lifecycle.md](project-lifecycle.md).

## Branch rename

Rename uses host RPC for intent and progress, daemon Git authority for the P
refs, and the runtime driver's workspace operations on the stopped session for
the local branch/upstream change. Git pushes to the affected refs are guarded while the operation is
incomplete.
The transaction and recovery rules are defined in
[session lifecycle](session-lifecycle.md#rename).

No rename request, result, or recovery step renames or deletes an `origin` ref.
Publishing the new name is a separate explicit origin operation.

## Origin communication

An origin is optional. A project has zero or one configured origin. Projects
without one are local-only and bypass every origin-dependent operation.

### Origin identity and configuration

The configured origin is a trusted host-side project setting, not a remote
copied from a session workspace. Its identity is the recorded remote URL;
`origin` is only P's presentation name.

The source driver decides which URLs are valid, before P records a request
that names one. The bundled Git driver accepts SSH Git URLs, including
`ssh://` and conventional `user@host:path` forms. HTTPS credentials, embedded
URL credentials, local filesystem remotes, and arbitrary transport helpers are
not supported.

Changing or removing the origin is explicit. A URL change immediately marks
the last origin observation inapplicable; P makes no source or publication
claim about the new origin until a successful refresh. It never
rewrites session remotes because sessions have only their P upstream.

### Host SSH and Git runner

Core decides and guards every origin operation: it holds the project's origin
lock, keeps the URL and observations, checks what an observation shows, and
records an operation's intent before its effect. The source driver performs
it: it observes, fetches from and pushes to the origin ([source
driver](technology-stack.md#git-server)). Core bounds each call to an origin:
it ends 35 seconds after it is made, its wait included, and at most two run at
once across the daemon, so a stalled origin holds no operation without end; a
push cut off after it started ends `outcome_unknown`. The operation runs on
the daemon host as the P user's account and uses that user's existing OpenSSH
configuration, private-key files, known-hosts policy, and SSH agent when
available. P stores no copy of those credentials and never mounts them into a
runtime or build worker.

The origin runner is the source driver's. The bundled Git driver's runner:

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
bare repository. Fetched objects are cache, not durable evidence. Core
serializes a project's origin operations under its origin lock, which a future
P-controlled Git garbage collection must also take, so an active comparison
cannot lose objects underneath it. When origin source selection succeeds, the
newly created ordinary P branch retains the selected commit.

An origin-source Create may be blocked after it records the captured commit
but before it creates the ordinary P branch. The source driver never prunes an
object: the bundled Git driver disables implicit Git garbage collection and
maintenance in the Git operations that can trigger them, including
`receive-pack`, so that commit remains available across daemon restart and
exact Retry. MVP runs no explicit P-controlled garbage collection. A future
explicit collector must hold the project's origin lock and preserve commits
captured by pending creation operations until their ordinary branch exists or
the intent is explicitly reconciled. It must not add protected or
origin-generation refs to do so.

A failed or interrupted refresh leaves the last completed observation only as
stale presentation data and marks current origin state unknown. It is not used
to authorize source selection or publication.

There is no ahead/behind or “published” truth without a successful refresh.
Every correctness-sensitive operation consumes its fresh observation while
holding the per-project origin-operation lock; it does not preserve historical
origin snapshots.

### Refresh triggers

MVP performs a refresh:

- before an origin is initially recorded for a new or existing project; a
  failed contact leaves the project unchanged;
- on explicit user request;
- before resolving a source explicitly selected from current origin state; and
- before every P-managed publication: at its dry run, at its run, and again in
  its worker.

Origin source selection and publication require their refresh to succeed. If
it fails, the user may still select an exact commit already reachable in P, but
P does not describe that commit as current origin state. Deletions contact no
origin: session discard and delete, retained-branch deletion and project
deletion report the commits that leave P's repository whether or not the
origin has them ([project lifecycle](project-lifecycle.md#retained-branches)).

Background refresh is an optional best-effort convenience. It may update the
overview, but its presence, interval, or success is never a correctness
dependency and it does not authorize publication or deletion. A
correctness-sensitive operation performs its own refresh rather than relying
on a wall-clock freshness threshold.

Concurrent origin operations for one project are serialized. A waiting
read-only refresh may reuse the result of the in-progress required refresh,
but publication evaluates its own current observation after acquiring the
lock.

### Origin sources

After a successful refresh, an advertised origin branch or tag is committed
source input. Creation copies its exact object ID into a newly created P
session branch. The session receives neither an origin ref nor an origin
remote, and later origin movement does not move the P branch.

P never turns a failed fetch into a partially created session source. Ordinary
P refs remain the other committed-source path.

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
always contains the complete destination. The publication's plan, which its
dry run returns, names the origin URL, project, source P ref and object ID,
destination ref, observed destination object ID or absence, and fast-forward
relation.

The destination must pass Git branch-ref validation.

Publication uses only the P branch tip. Uncommitted files and commits still
ahead only in the runtime workspace are not included; when P can observe them,
the plan warns about them but never commits or pushes them implicitly.

### Publication update rule

After the required refresh:

- an absent destination is created with one explicit refspec;
- a destination equal to the source already satisfies the request;
- a destination descending from the source already contains the requested
  work and satisfies the request;
- a destination that is an ancestor of the source receives one normal
  fast-forward push; and
- a divergent destination is refused: the dry run's plan carries the refusal
  `destination_divergent` and the observed object IDs, and has no change tag
  to run. A destination that diverges after the run was accepted fails the
  operation without a push.

Git on the origin is the final authority and atomically accepts or rejects the
non-force update if the destination races after P's refresh. P never sends
tags, multiple refs, deletion refspecs, a wildcard, or a force update. No
P-controlled ref update uses force.

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

Destructive plans and lifecycle intent travel over host RPC, as the dry run
and the run of a [risky change](control-api.md#risky-changes). Runtime loss is
stated by category from the runtime's presence and identity; P does not read
the workspace. The plan and the durable evidence identify the runtime only by
the runtime driver's opaque incarnation, and show the driver's own name for it
for display; they carry no Incus project or instance name. Branch loss comes
from Git reachability in P's repository; no deletion contacts the origin.
Runtime files and Git objects never travel inside the RPC request.

Session discard/delete, missing/unreachable behavior, change tags and
credential cleanup are defined in
[session lifecycle](session-lifecycle.md#destructive-preflight).
Unavailable Incus keeps confirmed cleanup incomplete and resumable; explicit
abandonment and its associated tombstone/orphan-cleanup/forget workflow are
outside MVP.
Whole-project deletion is defined in
[project lifecycle](project-lifecycle.md#project-deletion).

## Daemon and runtime driver

The daemon reaches every runtime effect through the runtime driver, an
executable it starts and supervises
([runtime isolation](runtime-isolation.md#driver-executable)). They talk over
one Unix socket pair the daemon creates for each start of the driver, not over
a listener:

- Each message is a JSON-RPC 2.0 request, notification or response in one
  newline-terminated frame of at most 16 MiB, decoded strictly: no duplicate,
  unknown or inexact keys.
- The daemon calls the driver's operations in P's terms. The driver calls one
  method back, the gate of the call it serves, right before that call's first
  effect; the daemon answers by running core's gate, which records its durable
  intent. A notification cancels a call the daemon no longer waits for.
- The connection begins with a hello. The daemon sends the protocol version,
  `p.runtime-driver/v1`, the driver's `config` from host configuration and the
  settings core owns that the driver needs: the P instance ID, the endpoint
  prefix, the disk-source ceilings, the builder storage pool and the
  public-egress substrate. The driver answers with the version it agreed and
  its capabilities, or refuses; nothing else is sent before it. The driver
  takes no configuration from its environment or arguments.
- Each terminal has a connection of its own: a socket pair whose far end the
  daemon passes with the open request (`SCM_RIGHTS`). It carries the
  terminal's bytes only, input one way and output the other; resizes and the
  terminal's end travel as calls on the main connection.
- Errors keep their kinds across the connection: unknown outcome, mismatch,
  unavailable, unsupported, no service, not ready, a host failure with its
  diagnostic, a terminal whose end is unproven, with its ID, cancellation and
  an expired deadline. A call whose own context ended also carries that end,
  whichever came first.

When the connection is lost, a read in flight ends as unavailable, and so does
an effect that took a gate that never ran; any other effect in flight ends
with an unknown outcome, which core settles through the restarted driver and
never sends again. A call never returns while its gate runs: a gate that runs
when the connection is lost or the call is cancelled is waited for, and the
call then ends by what the gate decided. The daemon restarts the driver when
it exits or its connection is lost.

## Image-build communication

Project-controlled evaluation and builds run through the isolation boundary in
[environment-building.md](environment-building.md).

- Core makes a private directory under P's state for the build, and the
  source driver's `Export` writes the source commit into it as a Git bundle:
  the commit and every commit it reaches, under one ref, `refs/p/source` in
  the bundled Git driver. Its temporary bare repository borrows P's objects
  through its alternates and holds that ref, so P's repository is only read.
  The export takes no lock: P prunes no object, so none the export reads can
  disappear underneath it. Core removes the directory after the build.
- The runtime driver copies the bundle through the confined Incus file API
  into the stopped builder, root-owned and read-only. The builder clones it
  into its own `/workspace` and evaluates the commit from that clone; no tree
  of the commit is checked out on the host.
- The builder receives a private root and bounded scratch space; it receives no
  session identity, filesystem grant, or Incus socket.
- With the host's public egress, the builder reaches public substituters and
  fetches; host/LAN/private access and ambient credentials are denied.
- After verification and scrubbing, the driver asks Incus to publish the
  builder as a private image labeled with the commit's identity. Incus returns
  the fingerprint and owns the bytes; RPC carries only progress, bounded
  diagnostics, image metadata, and the fingerprint.

## Future attempts and Git-triggered checks

Checks and attempts are deferred and keep separate reserved namespaces:

| Reserved namespace | Future purpose |
|---|---|
| `refs/attempts/*` | session-owned approach refs, if attempts are later designed |
| `refs/p/*` | P-owned check requests, results, and protocol metadata, if checks are later designed |

MVP denies writes to both and exposes no scheduler, result protocol, promotion
flow, lifecycle rule, or configuration for either feature.

A future design may give attempts their own immutable ID, ref, RPC status, and
bounded runtime. It must preserve the existing division: Git holds input/output
commits while RPC carries scheduling and status. A future Git-triggered check
must observe accepted ref changes after Git commits them; it must not overload
magic pushes as a lifecycle API. Exact layouts below either reserved namespace
remain undefined until that feature enters implementation scope.

## Attachment

Attachment is an RPC decision followed by a terminal that the daemon relays:

1. the client calls the attach method;
2. the daemon validates or starts the runtime as allowed;
3. the daemon checks through the runtime driver that the session's runtime
   runs complete with its host ready;
4. the daemon returns a short-lived, one-use pending attachment token and no
   runtime details;
5. the local client invokes the trusted host helper directly and transfers the
   token over private control input rather than argv;
6. the helper opens its own control connection and claims the token with the
   client terminal's size;
7. the daemon checks the session again, has the runtime driver open a terminal
   (`OpenTerminal`) on the fixed in-container `/usr/libexec/p/attach`
   entrypoint, passing the driver one end of a new socket pair that becomes
   the terminal's connection, counts the attachment in the session's
   presence, and answers the claim;
8. the claim's connection then carries the terminal: the daemon relays
   between it and the terminal's connection to the driver, the terminal's
   output to the helper and the helper's input to the terminal, and passes the
   helper's resizes to the driver as calls on its main connection; and
9. the daemon keeps the attachment's lease until the runtime driver proves
   that the attached process ended.

The runtime driver owns the native channel and its completion witness; the
daemon owns the token, the lease and the relay; the helper carries bytes
between the client and the daemon and has no runtime or Incus authority.

A failed terminal open or expired pending token never increments attachment
presence or clears unattended status. Only a claim that opened the terminal
does so. Client crash/SIGKILL or carrier closure ends the helper, which closes
its connection; the daemon then closes the terminal. If the runtime driver
fails while the daemon stays up, its terminals end without that proof: the
helper tears down, input and resizes stop and are not replayed, and the lease
stays, so discard and delete stay refused until the driver settles the
terminal as ended (`Settle`) or the session stops. If the driver's connection
is lost while the terminal opens, the claim fails without counting presence;
the open's outcome is unknown and names no terminal to settle, so the lease
stays, refusing discard and delete but not Stop, until the session stops. A
daemon restart ends attachments. No MVP RPC re-registers an existing terminal.

The fixed attach program connects the terminal to the persistent host selected
by trusted image/project configuration; tmux is the default. Attachment loss,
detach, or switching sessions ends only the temporary client channel. The
persistent host remains supervised by systemd, and its exit stops the
container. P never returns a shell string. Runtime mechanics are defined in
[runtime isolation](runtime-isolation.md#runtime-process-model), while
Start, lease, and status-clear ordering are defined in
[session lifecycle](session-lifecycle.md#attach-and-detach).

## Post-MVP model-gateway traffic

Bifrost is outside MVP. The retained design treats it as an independently
configured service and the authority for provider credentials, models,
routing, MCP, budgets, and virtual-key policy.

- P stores the Bifrost endpoint plus the session virtual-key ID and token.
- P delivers that token only to its session runtime and redacts it from logs,
  diagnostics, operation records, and ordinary RPC responses.
- The service may be network-reachable, but every inference request requires a
  valid session virtual key. That key succeeds only for approved inference and
  filtered model discovery and is rejected for dashboard, management,
  governance, logs, MCP, skills, and every other non-inference route.
- P verifies the pinned Bifrost route/authentication boundary and fails model
  access closed when configuration, version, positive probes, negative probes,
  or route inventory are not validated during initial model-enabled creation.
- After establishment, Start and Attach perform no Bifrost probe. Gateway
  failure degrades model discovery and inference without blocking Git,
  terminal attachment, RPC, or runtime existence.
- Projects without model grants receive no key and do not depend on Bifrost
  readiness.
- Session discard or deletion follows the revocation and cleanup rules in
  [session lifecycle](session-lifecycle.md#credential-and-image-cleanup).

These rules do not describe MVP Codex authentication, which remains inside the
session's private home and outside P's credential management.

## Event delivery

P reduces authoritative state changes into typed, versioned events and invokes
configured event handlers. The MVP handler appends redacted NDJSON to a local
log file. Handler failure is diagnostic and never rolls back the operation
that emitted the event. Event delivery is not source-of-truth state, an outbox,
an acknowledgement protocol, or a session-facing notification contract.
Reduction semantics are owned by
[session-observability.md](session-observability.md#typed-p-events); the implementation
interface is owned by
[technology-stack.md](technology-stack.md#event-handlers).

## Listeners

| Listener | Exposure | Purpose |
|---|---|---|
| Host control RPC | user-owned Unix socket | TUI, task commands and `p api` |
| Per-session RPC | private mounted Unix socket | identity and status reports |
| P Git SSH | host loopback plus explicit runtime path | fixed Git services |
| Bifrost inference (post-MVP) | explicit runtime path when granted | model APIs under virtual key |
| Bifrost administration (post-MVP) | authenticated with a host-only credential; session keys rejected | native Bifrost configuration |

No Incus socket, host SSH agent, host control socket, or general LAN
route is mounted into a session. The runtime driver has no listener: the
daemon gives it its connection and each terminal's as socket pairs.

## Failures, retries, and versioning

| Operation | Rule |
|---|---|
| Read-only RPC | client may retry after reconnect |
| Cross-authority mutating RPC | operation/idempotency key plus persisted P phase |
| Runtime mutation | the runtime driver settles an unknown outcome from native state (on Incus, its operations and current state); core never resends a non-idempotent effect or duplicates the backend's phases |
| Runtime driver connection | a lost connection ends an effect in flight with an unknown outcome, unless its gate never ran, and a read as unavailable; the daemon restarts the driver with a bounded backoff, and calls fail as unavailable until it is back; the first start's failed launch check, digest or protocol version refuses daemon start |
| Agent status report | latest valid receive wins while unattended |
| Git push | normal Git atomic/ref semantics; explicit retry |
| Origin refresh | interrupted refresh leaves current state unknown; the next explicit operation refreshes again |
| Origin publish | idempotent ensure operation; report failure or unknown outcome and wait for explicit retry |
| Start | idempotently ensure the systemd session target is active; on failed activation preserve diagnostics and stop the container |
| Attachment | the daemon keeps the lease until the runtime driver proves the terminal ended, settles it as ended, or the session stops; client, carrier or helper loss closes the terminal; a daemon restart ends attachments with no re-registration |

RPC and status payloads carry explicit version fields. Git protocol behavior is
Git's; P versions its ref-policy implementation and stored operation schema.
The daemon and the runtime driver agree the driver protocol's version in the
hello and refuse any other. P supports fresh installs only: it creates its
state schema in a new state directory, never migrates it, and startup fails
clearly when the stored schema version is not the binary's.

## MVP boundary

MVP includes local Unix RPC for the Linux client, P Git over SSH,
session-scoped Git principals, read-only host Git access, committed-state
session creation, transactional branch rename, SSH origin refresh, explicit
fast-forward-only origin publication, per-session status sockets,
systemd-defined host readiness, pending-to-claimed attachment relayed by the
daemon,
the Codex status adapter with session-local authentication, and the local
NDJSON event handler.

Client-initiated SSH-to-Unix, native macOS/Windows clients, Bifrost integration,
additional agent adapters, attempts, checks, and additional network/backend
claims remain later work or gated validations.
