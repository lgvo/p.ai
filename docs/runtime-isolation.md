# P — runtime and isolation

How P turns a branch-backed session and a cached Nix environment image into one
isolated Incus instance.

> **Status: design.** This document is authoritative for runtime placement,
> Incus authority, storage, grants, networking, fixed paths, the systemd
> interactive-host contract, attachment execution, and runtime cleanup.
> [Environment building](environment-building.md) owns cached environment
> images. [Session lifecycle](session-lifecycle.md) owns user-visible
> operations and loss decisions.

## Contents

- [The rule](#the-rule)
- [Incus boundary](#incus-boundary)
- [Configuration authority](#configuration-authority)
- [Session specification](#session-specification)
- [Runtime identity and authority](#runtime-identity-and-authority)
- [Storage](#storage)
- [Fixed paths](#fixed-paths)
- [Grant model](#grant-model)
- [Filesystem grants](#filesystem-grants)
- [Network contract](#network-contract)
- [Container baseline](#container-baseline)
- [Runtime process model](#runtime-process-model)
- [Credentials](#credentials)
- [Attachment](#attachment)
- [Non-activating workspace access](#non-activating-workspace-access)
- [Reconciliation and cleanup](#reconciliation-and-cleanup)
- [Failure posture](#failure-posture)
- [MVP boundary](#mvp-boundary)
- [Acceptance criteria](#acceptance-criteria)

## The rule

Incus is the sole MVP runtime backend. P owns session intent and policy; Incus
owns instance existence, state, root storage, images, and runtime operations.
P never duplicates an Incus operation state machine in SQLite.

Each MVP session is one unprivileged Incus system container named from its
immutable session UUID. The instance receives only:

- one immutable cached environment-image fingerprint;
- its private writable instance root;
- the normalized project policy captured at creation; and
- narrow P endpoints and credentials scoped to that session.

Repository content may execute inside the builder and session, but it cannot
select an Incus project, modify Incus policy, attach a device, add a host path,
or gain the Incus socket.

## Incus boundary

Incus is installed and initialized outside P. One configured, confined Incus
project is the execution boundary for one P instance. Incus projects are not P
projects: P project identity remains the complete Git repository path.

The machine owner provisions the Incus project, storage pool, allowed network
resources, and the narrow host-path prefix used for P endpoints and approved
filesystem grants. P verifies the required restrictions at startup but does
not initialize Incus, create storage pools, enable clustering, expose the
remote API, or widen project restrictions.

P connects only through the confined Incus user socket. It must not receive
the full administrative socket, which is host-root-equivalent, and it never
passes either socket into a builder or session. Running P as a user that also
has unrestricted Incus administration is an unsupported isolation posture.

MVP uses local Incus only. Incus remote servers and clusters do not turn other
machines into backends of this P daemon. A future P deployment may define a
different placement contract without changing session identity.

## Configuration authority

P-specific policy is trusted host configuration keyed by the complete P
project path. MVP grants remain project-scoped; repository files cannot request
P behavior or authority.

Trusted configuration may select:

- the interactive command and persistent interactive-host implementation;
- `none` or validated `public-egress` networking;
- named runtime-owned data/cache directories;
- named filesystem grants within the Incus project's pre-authorized path
  ceiling.

Instance configuration selects the confined Incus socket/project and the P
base-image identity. A project cannot select a different Incus authority.

Creation normalizes and validates policy, records the effective snapshot and
digest, and uses it for the lifetime of the session. A later configuration
change applies to new sessions and makes an older snapshot `outdated`; it does
not mutate the established runtime. Start revalidates safety-critical external
facts such as mount-source identity and the current Incus project ceiling. A
snapshot that can no longer be applied safely is `invalid` and blocks Start.
MVP has no in-place policy mutation.

## Session specification

`SessionSpec` is backend-neutral desired input for exactly one runtime:

```text
schema and runtime-contract version
P instance UUID
session UUID
project path and assigned branch
initial branch object ID, absent only for a blank-project bootstrap
interactive command and persistent-host selection
normalized project-policy snapshot and digest
runtime-owned directory plan
session endpoint descriptors
stable P metadata
```

It contains structured values, never shell fragments. It contains no origin
credential, Incus socket, host SSH agent, upstream model credential, or
administrative endpoint.

The initial object ID prevents workspace creation from racing branch movement.
Its sole absence case is the reserved unborn `main` branch of a blank or empty-
origin project; that workspace is initialized without a commit and only its
assigned principal may create the first ref. After creation, the assigned P
branch is source authority; it is not stored as a durable `base_commit`
concept.

Runtime creation receives the environment separately as an opaque
`EnvironmentHandle`:

```text
target kind and contract version
opaque immutable locator
content identity/digest
```

`SessionSpec` and lifecycle code do not interpret the locator. The MVP Incus
adapter accepts target kind `incus-system-image` and interprets the opaque
locator as an Incus image fingerprint. SQLite separately indexes the
project-scoped environment key and records the MVP handle needed for repair.
This keeps the reusable backend seam honest without specifying formats for
hypothetical providers.

## Runtime identity and authority

The Incus instance name is deterministic from the session UUID, for example:

```text
p-550e8400-e29b-41d4-a716-446655440000
```

P writes stable, non-secret Incus metadata containing the P instance UUID,
session UUID, project path, and runtime-contract version. The branch name is
display metadata, not an ownership key, because rename may change it.

| Fact | Authority | SQLite role |
|---|---|---|
| Session UUID and branch assignment | SQLite | authoritative |
| P refs and commits | P bare repository | records expected address only |
| Incus instance, state, root disk, image parent, and operations | Incus | records configured project and instance name |
| Workspace files and local Git state | Incus instance root | records no duplicate file state |
| Cached environment image bytes | Incus image store | indexes project-scoped environment key and opaque MVP handle |
| Git and selected-plugin credential validity | issuing service | records identifier and protected material needed by the session |

P may cache the latest observation for presentation, but every lifecycle
decision queries Incus. An Incus operation ID may be referenced while a P
operation waits; P does not reproduce its phases or treat its own cache as
runtime truth.

## Storage

An Incus instance root contains all session-private writable state:

| Path/state | Stop/start | Discard | Owner |
|---|---|---|---|
| `/workspace` standalone clone | retained | removed | instance root |
| `/home/p` | retained | removed | instance root |
| `/nix` database, profiles, and new store paths | retained | removed | instance root |
| `/tmp` and runtime-local configuration | retained | removed | instance root |
| persistent systemd journal and P-owned diagnostics | retained | removed | instance root |
| `/var/p/<name>` runtime-owned data/cache | retained | removed | instance root |
| Cached image base and initial Nix store | shared immutable parent | retained as cache | Incus image store |
| External filesystem grant | remains external | never removed by P | host owner |

The cached image contains the prepared Nix closure and database. Incus creates
a private writable instance root from it. Nix store paths remain immutable by
Nix semantics, while new paths and database updates belong only to that
session. Sessions never share a writable `/nix` and never mount the host Nix
store or daemon.

Copy-on-write efficiency depends on the configured Incus storage driver. P
records logical image/root sizes and reports storage-driver facts rather than
claiming universal physical deduplication.

The workspace is a standalone clone with its complete `.git` metadata inside
the instance root. P never uses a linked host worktree or mounts a host common
Git directory. Additional worktrees created by tools must remain in the
instance root or an explicit external grant.

## Fixed paths

| Path | Purpose |
|---|---|
| `/workspace` | assigned branch working repository |
| `/home/p` | persistent session home |
| `/nix` | cached image store plus private session additions |
| `/run/p` | session-specific P endpoints and credential files |
| `/opt/p` | P runtime kit and trusted systemd/attachment support |
| `/mnt/p/<grant-name>` | explicit external filesystem grant |
| `/var/p/<name>` | declared runtime-owned data/cache directory |

The cached image may not replace these paths with conflicting mounts or
declarations. `/run/p` is supplied from a P-owned per-session endpoint
directory; `/workspace` is populated after instance creation and is never an
image layer.

## Grant model

The reusable grant boundary remains typed and named. A grant implementation
must describe what crosses into the runtime, its access mode, revocation, and
what survives discard. Incus project restrictions are an upper bound: a P
project grant may narrow them but cannot widen them.

MVP implements only:

- named filesystem grants;
- fixed P Git and session-RPC endpoints;
- `none` or validated `public-egress` networking.

No MVP grant exposes the Incus socket, host namespaces, arbitrary host
services, privileged mode, raw Incus/LXC configuration, devices, published
ports, or ambient host credentials.

## Filesystem grants

A filesystem grant contains:

```text
unique portable name
canonical absolute host source
file or directory
read-only or explicit read-write
non-executable or explicit executable
fixed target /mnt/p/<name>
```

P resolves symlinks and rejects dangling, cyclic, overlapping, or changing
sources. It rejects the host root, whole home, P/Incus state and sockets,
credential trees, host Nix state, and other broad control paths.

The source must also fall within a path prefix already authorized by the
confined Incus project. P failing that check is a configuration error; P never
falls back to the admin socket to attach it. Mounts are private and
non-propagating, and P never deletes their host contents.

The `/run/p` endpoint mount follows the same upper-bound rule but is generated
and owned by P rather than selected by project configuration.

## Network contract

MVP retains two project profiles:

| Profile | Behavior |
|---|---|
| `none` | no general NIC; only mounted/session-scoped P endpoints |
| `public-egress` | outbound public internet plus the same narrow endpoints, with host/private destinations denied |

`none` is the implementation baseline. P Git and session RPC use Unix-stream
endpoints under `/run/p`; fixed helpers bridge Git's SSH stream and any
session-facing HTTP capability without exposing an arbitrary host address.

`public-egress` may be enabled only after the configured Incus network, ACL,
DNS, and routing combination proves that it denies the host, LAN/private,
carrier-grade NAT, link-local, metadata, multicast, sibling-instance, Incus
API, and undeclared service destinations over IPv4 and IPv6. Incus defaults are
not evidence of this P policy.

There is no unsolicited inbound access, host network mode, published port, or
general host route. A public forge may be reachable, but the runtime still
receives no origin remote or origin credential.

## Container baseline

Every MVP instance is an Incus system container that:

- is unprivileged with an isolated user-ID mapping;
- runs the fixed unprivileged session user for workspace and interactive work;
- has no nesting, privileged mode, raw LXC configuration, host PID/IPC/network
  namespace, device passthrough, or security-profile disablement;
- receives only its managed root disk and validated P devices/mounts;
- has resource limits supplied by trusted instance/project policy; and
- cannot access the Incus daemon or user socket.

The root Incus daemon remains part of the trusted host computing base. P's
compromise boundary is the confined user project, which must prevent P from
turning its runtime authority into arbitrary host-root authority.

## Runtime process model

An Incus system container boots systemd and has a small explicit hierarchy:

1. `p-session.target` orders only the P-owned base services required by a
   session;
2. a session-local Nix daemon owns that instance's private `/nix` database and
   store writes and accepts only the fixed session user;
3. `p-interactive.service` validates fixed paths/endpoints, applies the
   recorded activation, runs the project shell hook, and starts the configured
   command inside the selected persistent interactive host as the session
   user; and
4. the shell/agent may start its own processes inside the same instance.

The Nix daemon is inside the unprivileged Incus user namespace. It is not the
host daemon, has no host store/socket, and has authority only over this
instance's private root and validated network profile. Nix build sandboxing
remains enabled subject to the pinned Incus/Nix validation.

The instance does not run the P control daemon, Git server, an Incus
client/daemon, or SSH server. Systemd is the container's ordinary service
manager, not a P service-orchestration feature. P MVP owns only the base units
and the one persistent interactive host; it does not declare, health-check,
restart, or present project services.

`p-interactive.service` is the runtime contract. It must:

- remain inactive until endpoint validation and environment activation
  succeed;
- become active only when the persistent interactive host is attachable;
- let systemd track the real host lifetime through its service/cgroup even
  when the host implementation forks;
- use `Restart=no`; and
- request normal container shutdown after clean or failed persistent-host exit.

The unit and its root-owned configuration come only from the P base image and
trusted session assembly. Repository content and the session user cannot
replace them. The unit writes bounded status and failure output to the
persistent system journal. P observes Incus state plus current systemd unit
state; there is no second startup-readiness marker or generation protocol.

Starting the retained instance reruns activation and starts a fresh persistent
host. A failure records the systemd/Incus diagnostic and shuts the container
down, leaving the session normally `stopped` and immediately retryable. Clean
host exit likewise shuts down every process in the container. Detachment does
not stop the host or container.

## Credentials

The session receives only:

- its UUID-bound P Git private key and known-host entry;
- its private session-RPC endpoint;
- explicitly named secret-bearing filesystem grants.

It never receives host-origin SSH authority, the Incus socket, host Codex or
OpenAI credentials, event-handler configuration, or another session's
material. The user may authenticate Codex inside the session's private home;
P does not read, copy, or manage those tool-owned files. P-provisioned secrets
are written only under the session endpoint directory with restrictive
ownership and are omitted from instance metadata, logs, SQLite diagnostics,
and TUI previews.

## Attachment

Every supported persistent host supplies the same two runtime artifacts:

- `p-interactive.service`, which systemd owns for startup, readiness, lifetime,
  and logs; and
- the fixed root-owned `/usr/libexec/p/attach`, which connects one PTY to the
  already-running host.

The attach command is a short-lived adapter, not a daemon or supervisor. P
opens a structured Incus exec operation as the fixed session user with an
explicit PTY, working directory, and argv containing only
`/usr/libexec/p/attach`. The adapter replaces itself with fixed host-specific
argv, such as a tmux attach client addressed by a root-owned socket/session
descriptor. It accepts no client-selected command, socket, session name, or
shell string.

Tmux is the shipped MVP default. Another persistent host is supported only when
its systemd unit and attach command pass the same conformance contract.

The client never receives general Incus credentials. A future remote P client
may initiate SSH to the P host and execute the daemon-approved attachment path;
it still does not connect to Incus directly.

The returned spec is not attachment presence. Lifecycle first verifies the
current systemd unit and fixed attach path, issues a pending token, and returns
the structured spec. The trusted host helper establishes the channel,
confirms the token on its dedicated attachment RPC connection, and owns the
resulting lease.

The helper binds channel lifetime to the lease and terminal carrier without
depending on client cleanup. While the daemon is reachable, it retains the
confirmed lease until channel teardown completes. Client crash, SIGKILL,
attachment-transport loss, or client-machine loss starts teardown. If daemon
restart removes the lease first, teardown begins immediately and that helper
establishes no new lease or channel until it finishes. Teardown ends only the
temporary attach client; the systemd-owned persistent host and configured
interactive command continue. MVP does not re-register an already-running
attachment channel.

## Non-activating workspace access

Loss inspection and Git repair must not run repository hooks, activation, or
the interactive command. The Incus backend therefore exposes a closed
`WorkspaceOperation` set.

An implementation may use Incus file/storage facilities or a P-owned isolated
helper attached only to the stopped/frozen instance storage. It must:

- operate under the lifecycle lock;
- expose only the workspace and explicitly required runtime-owned paths;
- receive no external grants, session credentials, network, Incus socket, or
  project activation;
- disable Git hooks, helpers, monitors, and ambient Git configuration;
- accept enumerated operations with structured arguments; and
- return bounded structured results.

The same non-activating mechanism may read the fixed P-owned systemd journal
and diagnostic paths from a stopped instance. It does not start the instance
or treat logs as lifecycle authority.

The MVP set covers Git status/ref inspection, branch/upstream rename, and the
targeted ref operations required by lifecycle repair. It never accepts
arbitrary argv, reset, clean, automatic commit, or force-push.

## Reconciliation and cleanup

P lists and inspects instances only in its configured confined Incus project.
It matches deterministic names and stable P metadata to the P instance and
session UUID. It never adopts or deletes by a human branch name.

For an active session:

- zero matching instances is `missing`;
- one verified instance supplies current runtime state;
- a name/metadata conflict is an ownership error requiring repair; and
- machinery belonging to another P instance is never touched.

Start, stop, exec, and delete outcomes come from Incus state and Incus
operations. After connection loss P inspects Incus; it does not infer success
from its own RPC failure or replay Incus operations blindly.

Discard removes the verified Incus instance, its private root, endpoint
directory, and session credentials while retaining the P branch. Cached images
are independent cache objects and are never deleted as a side effect of
discard.

## Failure posture

| Failure | Required result |
|---|---|
| Incus user socket unavailable | Runtime state is `unreachable`; do not create duplicates or claim stop/removal |
| Configured Incus project missing or unrestricted | Refuse runtime creation with an actionable host-setup error |
| Image fingerprint missing | Existing instances continue. New creation rebuilds from its exact committed request; missing-instance repair follows lifecycle and never substitutes silently. |
| Instance absent | Report `missing`; defined repair may recreate only from committed P state |
| Instance metadata conflicts | Refuse adoption, attachment, or deletion |
| Incus operation interrupted | Query Incus operation and instance state; do not duplicate its workflow |
| Filesystem grant exceeds Incus restriction | Reject; never use administrative access as fallback |
| `public-egress` cannot be proved | Offer `none` or fail creation; never widen connectivity |
| Creation activation/host startup fails | Systemd records the failure and shuts down the partial container; the durable creation request remains retryable from its immutable input |
| Established Start activation/host startup fails | Systemd records the failure and shuts down the container; report `stopped` with the bounded diagnostic and permit an ordinary Start retry |
| Anchor exits after readiness | Systemd shuts down the container; reconciliation reports `stopped` and never claims the vanished host remains ready |
| Fixed attach command or unit check fails while Incus is running | Refuse attachment, report the observed unit/adapter condition, and let systemd complete shutdown or require repair when the contract is inconsistent |

## MVP boundary

MVP includes:

- one local, confined Incus project per P instance;
- one unprivileged Incus system container per session;
- immutable environment-image fingerprint plus private writable instance root;
- persistent workspace, home, and private Nix state across stop/start;
- project-scoped filesystem grants within the Incus project ceiling;
- `none` and validation-gated `public-egress` profiles;
- systemd-owned persistent interactive hosting with tmux as the shipped
  default;
- one fixed attachment command and the confirmed attachment handshake; and
- Incus-authoritative inspection, operations, and cleanup.

MVP excludes Incus administration, remote Incus servers, clustering, Incus
VMs, live migration, snapshots/backups as P features, raw Podman/Docker
backends, Kubernetes, privileged instances, devices, published ports, and
general host/LAN access.

## Acceptance criteria

The backend is supported only when tests prove:

1. P operates through the confined user project and cannot use the admin
   socket or attach an unapproved host path/device;
2. two sessions receive distinct UUID-named instances, private roots,
   workspaces, homes, Nix database changes, and credentials;
3. the cached image remains immutable, new session paths never appear in
   another session, and physical sharing/copying matches the recorded storage
   driver behavior;
4. start/stop state and operation outcome come from Incus and survive a P
   daemon restart without duplicate instances;
5. discard removes only the verified instance/private root and retains the P
   branch and cached image;
6. sessions cannot reach Incus, host credentials, undeclared mounts, sibling
   instances, or disallowed network destinations;
7. attachment uses fixed structured argv, never exposes general Incus
   authority, and failed launch before confirmation never becomes attachment
   presence;
8. the non-activating workspace interface cannot execute repository-controlled
   hooks or arbitrary commands; and
9. systemd reports the persistent host ready only after endpoint validation,
   activation, and attachability succeed;
10. activation or host failure records bounded journal diagnostics, stops the
    container, and permits an ordinary Start retry;
11. host exit shuts down the container even while the P daemon is restarting,
    while detach or client/attachment-transport loss ends only the temporary
    attach client;
12. daemon restart reconstructs session condition from Incus and current
    systemd state without a custom readiness marker; and
13. the unit and fixed attach command cannot be replaced by the repository,
    session user, shell hook, or interactive command.
