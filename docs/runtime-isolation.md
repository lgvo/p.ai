# P — runtime and isolation

How P turns a session and an environment artifact into isolated, persistent
runtime machinery without giving repository code ambient host authority.

> **Status: design.** This document is authoritative for runtime specifications,
> effective grants, storage, engine selection, backend labels, network
> isolation, and non-activating workspace access.
> [session-lifecycle.md](session-lifecycle.md) owns when machinery is created,
> started, inspected, stopped, repaired, or removed;
> [environment-building.md](environment-building.md) owns environment artifacts
> and activation; [communication-boundaries.md](communication-boundaries.md)
> owns RPC, Git, and gateway channels; and
> [model-gateway.md](model-gateway.md) owns Bifrost policy and key behavior.

## Contents

- [The rule](#the-rule)
- [Configuration authority and grant scope](#configuration-authority-and-grant-scope)
- [Session specification](#session-specification)
- [Runtime manifest and authorities](#runtime-manifest-and-authorities)
- [Backend and engine selection](#backend-and-engine-selection)
- [Runtime-owned storage](#runtime-owned-storage)
- [Fixed runtime paths](#fixed-runtime-paths)
- [Grant model](#grant-model)
- [Filesystem mounts](#filesystem-mounts)
- [Network contract](#network-contract)
- [Baseline container isolation](#baseline-container-isolation)
- [Credentials and environment](#credentials-and-environment)
- [Non-activating workspace access](#non-activating-workspace-access)
- [Policy changes and recreation](#policy-changes-and-recreation)
- [Reconciliation and cleanup](#reconciliation-and-cleanup)
- [Failure posture](#failure-posture)
- [V1 boundary](#v1-boundary)
- [Acceptance criteria](#acceptance-criteria)

## The rule

A runtime receives only the immutable environment artifact and the normalized
project policy captured for its session. It does not inherit the daemon's
filesystem, environment, credentials, namespaces, sockets, or network merely
because both run on the same machine.

P distinguishes four kinds of runtime input:

| Input | Owner | Lifetime |
|---|---|---|
| Immutable environment and P substrate | Artifact store | leased while a runtime may use it |
| Runtime-owned writable storage | Runtime backend | session runtime creation through removal |
| Explicit external grants | Trusted host configuration and grant provider | fixed by the recorded policy snapshot |
| Narrow P service endpoints | P, Git, and Bifrost authorities | session-scoped and revocable |

Repository contents may influence environment building and later execute during
activation, but they cannot add or widen any external grant.

## Configuration authority and grant scope

V1 reads P-specific runtime policy only from trusted host configuration keyed
by the complete project path:

```text
projects.<project-path>
```

The effective grant namespace is `{project}/*`. Every session in the project
receives the same project policy. `{project}/{branch}` remains reserved for a
future branch-specific layer and is not read in V1. There is no session override,
repository P file, custom P flake output, or inference from repository paths.

Trusted project configuration may select:

- the interactive command and interactive host;
- the V1 runtime engine when more than one supported engine is installed;
- `public-egress` or `none` network posture;
- named per-session runtime-owned cache/data directories;
- named filesystem mounts;
- optional model access through a Bifrost-owned policy reference.

The separate P Git rewrite exception is enforced at the Git server boundary in
[communication boundaries](communication-boundaries.md#session-principal); it
is not a runtime grant.

It cannot request privileged containers, host namespaces, devices, published
ports, a general host/LAN route, or ambient host credentials. Those are outside
the V1 grant model rather than hidden advanced settings.

Creation normalizes and validates this policy before constructing machinery,
then records an immutable snapshot and digest. A runtime never consults current
project configuration to discover additional authority.

## Session specification

`SessionSpec` is the backend-independent desired input for exactly one runtime.
Its conceptual fields are:

```text
schema and runtime-contract version
P instance ID
session UUID
project path and assigned P branch ref
expected branch object ID for initial checkout
environment-artifact and P-substrate identities
runtime-kit compatibility version
backend and engine selection
interactive command and host configuration
normalized grant snapshot and digest
runtime-owned storage plan
session endpoint descriptors
stable backend labels
```

The spec contains structured values, never shell fragments. It contains no
origin credential, upstream model credential, container-engine socket, SSH
agent socket, or host control socket. Secret session material is delivered
through its dedicated endpoint or protected file after the backend creates the
runtime; it is not serialized into ordinary diagnostics.

The expected initial object ID protects workspace creation from a branch moving
between resolution and clone. It is creation input, not durable
`session.base_commit` state. After creation, the assigned P branch is source
authority.

The instance ID is a random immutable UUID created with the instance state. It
is not a hostname or filesystem path and survives daemon upgrades and an
intentional state restore. A copied state directory must not operate
concurrently as a second instance with the same ID.

## Runtime manifest and authorities

After creation, SQLite records a `RuntimeManifest` containing the intended
effective runtime facts:

```text
manifest schema
session UUID, project path, and current assigned branch
backend, engine, opaque runtime locator, and stable labels
environment artifact, substrate, and runtime-kit identities
policy snapshot digest and normalized non-secret grants
runtime-owned storage locators
interactive command/host identity
session endpoint and credential identifiers, never raw diagnostics
creation and last-reconciliation times
```

The manifest is the desired assembly record, not proof of external reality:

| Fact | Authority |
|---|---|
| Session identity, assignment, intended manifest | SQLite |
| Runtime existence, state, labels, and engine resources | Runtime backend and engine |
| Files and local Git state | Runtime-owned storage |
| Environment bytes | Artifact store |
| P Git refs and commits | P Git repository |
| Credential validity | Its issuing authority |

Reconciliation compares the manifest to fresh authority queries. It does not
mark a runtime healthy merely because the stored locator still exists.

## Backend and engine selection

`local-container` is the only V1 backend. The backend is an instance-local
provider; it never opens SSH to manage a remote host. Local VMs and Kubernetes
are later backends implementing this same normalized contract.

Podman is the preferred V1 engine and rootless Docker is supported after its
validation passes. Engine choice is deterministic:

1. an explicit trusted project selection wins;
2. otherwise the instance's configured default wins;
3. otherwise one detected supported rootless engine is selected; and
4. if both are detected without configuration, Podman is selected.

The selected engine, resolved executable, supported-version result, and engine
identity are recorded in the manifest. P does not fall back to another engine
after a create/start failure, and an existing runtime is never silently moved
between engines. If the recorded engine is unavailable, the runtime is
unreachable until it returns or an explicit later recreation path is chosen.

Detection is read-only. P never installs, starts, reconfigures, or switches a
container engine on the user's behalf.

Container-engine support is independent from environment-provider support.
Rootless Docker may run the V1 Nix/substrate artifact after conformance even
though the Dockerfile environment provider remains a later implementation.

## Runtime-owned storage

Every runtime has an explicit storage plan. No anonymous engine volume may be
created by an image declaration or runtime default.

| Storage | Stop/start | Discard/delete | Notes |
|---|---|---|---|
| Workspace repository | retained | removed | mounted at `/workspace`; standalone clone of the assigned P branch plus local work |
| Session home | retained | removed | mounted at `/home/p`; agent/tool state and user files |
| Runtime writable layer | retained | removed | includes runtime-local configuration and `/tmp` |
| Declared runtime-owned cache/data | retained | removed | named and labeled to the session UUID |
| Immutable environment artifact | leased | lease released after removal | shared bytes are collected separately |
| External filesystem mount | remains external | never removed by P | writes already made remain on the host source |

Workspace, home, and named runtime-owned storage carry the stable instance and
session labels. Their paths or volume names derive from the UUID, not the
mutable branch name. Rename therefore changes Git refs and manifest metadata
without renaming storage.

The workspace is a standalone clone with its complete Git metadata inside the
runtime-owned workspace storage. P does not use a linked host worktree whose
`.git` file points outside `/workspace`; mounting that worktree alone would
break Git, while exposing its common host repository would violate the
workspace boundary. Tools inside a session may create additional worktrees
only within runtime-owned or explicitly granted storage.

P creates storage with the session user's ownership and restrictive host-side
permissions before untrusted activation runs. Removal addresses only resources
whose labels and recorded locators both match the instance and session UUID.
Name similarity is never sufficient authority to delete storage.

Trusted project configuration may declare runtime-owned cache/data names. Each
name creates a separate per-session directory at `/var/p/<name>`; it is never
shared with another session, has no host source, is always writable by the
session user, and follows the same stop/remove lifetime as home. Names use the
same portable validation as filesystem-grant names. Images and repository code
cannot declare additional volumes or change their targets.

## Fixed runtime paths

P reserves a small portable layout across runtime backends:

| Path | Purpose |
|---|---|
| `/workspace` | assigned branch working repository |
| `/home/p` | session user's persistent home |
| `/run/p` | private session endpoints and protected credential files |
| `/opt/p` | read-only P runtime kit and launcher |
| `/mnt/p/<grant-name>` | explicit external filesystem grants |
| `/var/p/<name>` | declared per-session runtime-owned cache/data |

The environment artifact may additionally expose its provider-owned immutable
paths, such as `/nix/store`. Neither a project image nor activation may replace
the reserved layout. `/tmp` is writable runtime-local state and follows the
writable-layer lifetime.

## Grant model

A grant is a typed, named capability produced from trusted project policy. A
`GrantProvider` validates the requested type and emits a normalized runtime
grant plus a cleanup handle. It does not receive arbitrary repository commands.

Conceptually:

```go
type GrantProvider interface {
    Resolve(context.Context, ProjectGrant) (RuntimeGrant, error)
    Verify(context.Context, RuntimeGrant) error
    Revoke(context.Context, RuntimeGrant) error
}
```

The interface exists so a later host integration can supply a narrowly
isolated filesystem, credential, device-like, or service capability without
teaching the runtime backend its private implementation. Every provider must
declare what is mounted or reachable, whether it is writable, how it is
revoked, and what external state survives session removal.

V1 implements only:

- named filesystem mounts;
- the fixed P Git and session-RPC endpoints;
- optional Bifrost inference with a session virtual key; and
- the selected `none` or `public-egress` network profile.

Devices, arbitrary local services, published ports, privileged capabilities,
and general secret injection remain unsupported even if a future provider
could model them.

## Filesystem mounts

A V1 filesystem grant has these normalized fields:

```text
unique grant name
canonical absolute host source
source type: file or directory
access: read-only (default) or read-write
execution: denied (default) or allowed
fixed runtime target: /mnt/p/<grant-name>
provider/version and snapshot diagnostic
```

Rules:

- the source must exist when the policy snapshot is created;
- P resolves symlink components and records the canonical source; dangling or
  cyclic resolution is rejected;
- the target is derived from a portable grant name and cannot be supplied as
  an arbitrary container path;
- duplicate names, overlapping targets, and a file/directory type change are
  rejected;
- mounts use private/non-propagating semantics and always deny device and
  set-user-ID behavior;
- read-write and executable access each require an explicit trusted setting;
- the container image, devShell, shell hook, and interactive command cannot
  change mount definitions; and
- start verifies the canonical source and type before exposing it again.

P rejects sources that would expose the host root, the user's entire home,
P's state/configuration/credential trees, container-engine storage or sockets,
the host control socket, SSH-agent sockets, or the user's SSH credential tree.
A narrower explicitly named credential directory for a tool may be mounted,
but P presents it as a readable secret-bearing grant rather than an ordinary
project file share.

Filesystem grants are deliberately external. P does not snapshot, migrate,
roll back, or delete their contents. Destructive preflight names every attached
grant and reminds the user that writes through a read-write grant already
survive outside the runtime.

## Network contract

The V1 project policy chooses one of two profiles:

| Profile | Behavior |
|---|---|
| `public-egress` | outbound public internet plus the narrow P endpoints granted below |
| `none` | no general internet; only required P Git and session-RPC endpoints, plus Bifrost when model access is granted |

`public-egress` is the default. It does not mean host-network access. Both
profiles deny unsolicited inbound connectivity, published ports, host network
mode, the host and engine gateways, loopback services outside the runtime,
private/LAN, carrier-grade NAT, link-local and multicast ranges, metadata
endpoints, sibling runtimes, and container-engine administration over IPv4 and
IPv6. DNS answers and redirects do not bypass address filtering.

P creates only these service-boundary exceptions:

- the assigned project's P Git listener;
- the private per-session RPC socket; and
- Bifrost inference/model discovery when the project has model access.

An exception is destination- and protocol-scoped and does not expose the rest
of the host or service management surface. Bifrost dashboard/administration,
P host RPC, and arbitrary host ports remain unreachable. Public egress may make
a public forge address network-reachable, but P supplies no origin remote or
host-origin credential to the runtime.

The exact rootless enforcement mechanism is backend-specific and gated by the
real-machine validation. A backend that cannot prove this profile may offer
network `none`; it may not quietly degrade `public-egress` into unrestricted
rootless networking.

## Baseline container isolation

Every V1 runtime uses a rootless engine and:

- runs as the fixed unprivileged session user;
- adds no Linux capability and enables no privileged mode;
- uses private PID, IPC, mount, user, and network namespaces;
- denies host devices, host cgroups, host process access, and security-profile
  disablement;
- enables `no-new-privileges` and the engine's supported default seccomp and
  mandatory-access-control profile;
- exposes no engine socket, host SSH agent, host control socket, host Nix
  daemon, or undeclared ambient host path; and
- accepts no image-declared volume, port, health-check, entrypoint, or user
  override that conflicts with P's runtime contract.

Rootless execution is not treated as a complete sandbox. Network filtering,
mount validation, endpoint scoping, isolated builds, and the workspace helper
remain separate required controls.

## Credentials and environment

The runtime process environment is constructed from fixed P variables, the
normalized environment artifact, and explicit session capability descriptors.
P does not inherit the daemon's environment wholesale.

The session receives only:

- its branch-scoped P Git private key and known-host entry;
- its private session-RPC endpoint;
- its Bifrost virtual key when model access is granted; and
- the contents of any explicit filesystem grant, including a grant clearly
  identified as secret-bearing.

It never receives the host-origin SSH key or agent, upstream model keys,
notification credentials, daemon/admin credentials, or another session's
material. Secret files use restrictive ownership and are excluded from normal
manifest, log, preview, and RPC serialization.

## Non-activating workspace access

Lifecycle inspection and Git-ref repair must observe runtime-owned storage
without executing repository code in the daemon or activating the session.
The backend therefore implements a closed `WorkspaceOperation` set through a
P-owned isolated helper.

The helper:

- runs only while the lifecycle operation holds the required lock and the
  runtime is stopped or safely paused;
- mounts the selected workspace and only individually validated runtime-owned
  paths for additional Git worktrees known to that workspace repository; it
  never mounts the entire session home merely to discover work;
- receives no external filesystem grants, session credentials, engine socket,
  unrelated host paths, project environment, shell hook, or interactive
  command;
- disables Git hooks, filesystem monitors, credential helpers, and ambient
  system/global/repository Git configuration;
- has no network, except a separately declared read-only path to the assigned
  P Git repository when a defined repair needs it;
- accepts an enumerated operation and structured arguments, never arbitrary
  argv; and
- returns bounded structured status plus diagnostics.

The V1 set covers workspace/ref status, branch rename/upstream update, and the
targeted ref inspection/restoration needed by lifecycle repair. It never runs
`reset`, `clean`, automatic commit, arbitrary checkout, or force-push.

A backend that cannot provide this boundary cannot implement V1 rename,
destructive preflight, or workspace repair. The daemon must not replace it by
opening the runtime filesystem directly in its ambient context.

## Policy changes and recreation

Stop/start, attach, and rename retain the recorded `SessionSpec` and grant
snapshot. A change to trusted project configuration applies to future sessions,
not existing runtime machinery.

V1 has no general in-place grant update or voluntary runtime-recreation
operation. Repairing a missing runtime recreates it only from its recorded
artifact and policy snapshot. To adopt changed policy in V1, the user ends the
old session and creates a new one from retained committed state. A later
recreation operation may preserve the UUID only if lifecycle design defines
its loss report, commit point, and rollback behavior first.

## Reconciliation and cleanup

The backend lists resources by stable labels and then verifies their recorded
locator and kind. Labels are:

```text
p.managed = true
p.instance = <immutable instance ID>
p.kind = runtime | workspace | home | data
p.session = <session UUID>
p.project = <complete project path>
p.contract = <runtime-contract version>
```

The branch name is not a reconciliation key because rename changes it. A
backend may expose it as display metadata, but P never adopts or deletes
machinery based on that value.

Labels and ownership metadata live in the engine/backend control plane or
host-side P metadata and are not writable through the runtime's mounted view.
An in-runtime file named like a marker never grants cleanup authority.

Exactly one matching runtime may be linked to an active session. Zero is
`missing`; more than one is an ambiguity requiring repair. Resources matching
another instance ID are never adopted. A UUID found only in an abandonment
tombstone follows the containment/orphan rules in session lifecycle.

Cleanup removes the runtime and each recorded runtime-owned resource using
expected labels. External mounts are detached but their sources are untouched.
Credential revocation and environment-artifact lease release follow their own
authorities after backend removal reaches its lifecycle commit point.

## Failure posture

| Failure | Required result |
|---|---|
| Invalid or unavailable engine selection | Fail before runtime creation; never fall back silently |
| Mount source missing or changed type | Fail creation/start with the named grant; do not omit it |
| Mount source resolves to a denied host path | Reject policy before creating machinery |
| Requested public-egress profile cannot be enforced | Fail or offer explicit network `none`; never widen access |
| Required P endpoint cannot be isolated | Session is not ready |
| Optional Bifrost unavailable for a model-enabled project | Session is not ready; projects without the grant are unaffected |
| Partial runtime/storage creation | Reconcile by UUID labels and lifecycle phase; never create a duplicate |
| Manifest/engine observation differs | Report the exact drift and require defined repair |
| Cleanup cannot verify ownership labels | Refuse deletion and retain a cleanup diagnostic/tombstone |

## V1 boundary

V1 includes:

- one `local-container` runtime per session with Podman preferred and rootless
  Docker supported after validation;
- immutable project-scoped policy snapshots;
- runtime-owned workspace, home, writable layer, and named cache/data storage;
- named read-only-by-default filesystem grants under `/mnt/p`;
- `public-egress` and `none` network profiles with narrow P/Bifrost exceptions;
- fixed session Git/RPC/model credentials;
- stable labels, runtime manifests, reconciliation, and guarded cleanup; and
- isolated, non-activating workspace operations.

V1 excludes branch/session-specific grants, in-place policy mutation,
voluntary runtime recreation, devices, privileged capabilities, published
ports, host/LAN access, arbitrary local services, general secret providers,
runtime migration, local VMs, and Kubernetes backends.

## Acceptance criteria

The runtime contract is implemented when integration tests prove:

1. the same normalized `SessionSpec` produces equivalent observable isolation
   under every claimed V1 engine;
2. stop/start retains all runtime-owned storage while discard/delete removes
   only resources labeled for that instance and session;
3. rename changes no storage identity and reconciliation still finds the
   runtime by UUID;
4. read-only is the mount default, read-write/execute require explicit policy,
   and denied host paths cannot be exposed through symlink resolution;
5. external mount contents survive runtime removal and are clearly itemized by
   destructive preflight;
6. public internet works under `public-egress` while host, private, metadata,
   sibling, engine, and management endpoints remain unreachable over IPv4 and
   IPv6;
7. network `none` retains only the explicitly granted P service endpoints;
8. no ambient daemon, host-origin, provider, notification, or other-session
   credential is present in the runtime environment, mounts, image metadata,
   or diagnostics; explicit secret-bearing grants remain itemized exceptions;
9. a configuration change does not mutate an existing runtime, and a missing
   runtime repair uses the recorded snapshot;
10. engine ambiguity and failure never cause silent fallback or duplicate
    runtime creation;
11. workspace status/rename/repair executes without activation, external
    grants, ambient Git configuration, or arbitrary argv; and
12. daemon restart can relink exactly one labeled runtime and reports missing,
    duplicate, foreign-instance, and abandoned machinery distinctly.
