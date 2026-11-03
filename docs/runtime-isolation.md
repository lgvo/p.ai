# P — runtime and isolation

How P turns a branch-backed session and the image of its source commit into
one isolated Incus instance.

> **Status: design.** This document is authoritative for runtime placement,
> Incus authority, storage, grants, networking, fixed paths, the systemd
> interactive-host contract, attachment execution, and runtime cleanup.
> [Environment building](environment-building.md) owns commit images and
> their lifetime. [Session lifecycle](session-lifecycle.md) owns user-visible
> operations and loss decisions.

## Contents

- [The rule](#the-rule)
- [Runtime driver](#runtime-driver)
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

- one immutable image: its source commit's image, or the P base image when
  the host configures no `runtime.environment` and for a blank project's
  bootstrap session;
- its private writable instance root;
- the normalized project policy captured at creation; and
- narrow P endpoints and credentials scoped to that session.

Repository content may execute inside the builder and session, but it cannot
select an Incus project, modify Incus policy, attach a device, add a host path,
or gain the Incus socket.

## Runtime driver

Runtime effects belong to one runtime driver, `internal/runtimedriver`: trusted
code that owns runtime machinery and that core addresses in P's terms
([technology stack](technology-stack.md#runtime-and-incus-access)). Host
configuration selects it as `runtime.driver`. The only driver is the Incus
driver (`internal/runtimedriver/incus`), which this document describes. It
runs inside the daemon process; the daemon builds it in one place and holds
nothing of it but `runtimedriver.Driver`. A boundary test fails if any other
Go code, the Incus VM test fixtures aside, imports it, or if other non-test
code names the `incus` client or the Incus sockets. Every driver
proves these properties; the conformance suite
(`internal/runtimedriver/conformance`) checks those a stubbed backend can show.

| Property | The contract | How the Incus driver meets it |
|---|---|---|
| Isolation | Code in a runtime, root included, cannot gain host privilege, reach the driver's authority, see another runtime, or use a device P did not grant; `Check` proves it from live state. | Unprivileged container, isolated ID map, no nesting or `raw.*`, confined project ([Incus boundary](#incus-boundary), [container baseline](#container-baseline)). |
| Network `none` | Nothing is reachable except the two P endpoints. | No NIC. |
| Network `public-egress` | Only the outbound destinations the [network contract](#network-contract) allows, proven from live state before each create and start by a check the daemon's account cannot forge. | Managed bridge, ACL and nftables read by two fixed sudo commands. |
| Private state | The whole private root survives stop and start, belongs to this runtime only, and is gone after `Delete`. | The instance root ([storage](#storage)). |
| Stopped storage | Workspace operations reach only the session's private root, never a grant, the endpoint directory, a host path or another runtime's storage. | A stopped instance's file API sees only its storage volume. |
| Session host and services | The runtime runs the session's command in a persistent host that terminals attach to, reports it ready or not with a bounded diagnostic, and runs project services its own way. | systemd, tmux from the interactive-host package, user units ([runtime process model](#runtime-process-model), [project user services](#project-user-services)). |
| Identity | Labels stored and returned exactly; an incarnation stable across stop and start, new on re-create or restore. | `user.p.*` keys, `volatile.uuid` and its generation, image properties. |
| Endpoints and grants | The two sockets appear read-only at `/opt/p/endpoints` and each grant at `/mnt/p/<name>` with its flags, in that session only; a driver refuses what it cannot honor exactly. | Disk devices within the project's ceilings ([filesystem grants](#filesystem-grants)). |

Core reaches images, sessions, terminals, project services, the boundary
check, the inventory, removal and the workspace through the driver, and
computes admission and the proofs of absence from the driver's `Inspect` and
`Inventory`.

A proof of absence runs inside a boundary `Check` has just proven. A runtime
is gone only when `Inspect` finds nothing at its address and a complete
inventory holds no object carrying its reference, whichever P instance
labeled it, since an external rename can move a runtime away from its
address. The Incus driver's inventory is one read of the whole Incus project,
at most 64 instances, P's or not. More instances, an instance that is not a
container, or P labels that its expanded configuration does not repeat
exactly make it an error, never a shorter list.

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

Session creation and project bootstrap are admitted only when one more
container fits beneath the configured Incus project container limit, which the
driver's inventory states as its capacity; P accepts a limit of at most 64.
Admission counts every object the inventory lists and the durable session or
creation reservations that no listed object holds, including unresolved
attempts. A reservation's runtime holds its slot when the driver's `Inspect`
of its spec finds it, and a pending session's builder when it carries the
creation's build reference with its project, commit and base image labels;
neither is counted twice. Every other container counts as a separate
occupant, foreign ones included. A replacement creation needs no room beyond
the reservations already counted. The reservation check and new durable intent must be
serialized. This admission rule does not grant exclusive ownership of spare
Incus capacity: external activity or older instances can still fill the
project, and P never raises the ceiling or removes unrelated instances to make
room.

### NixOS service installation

The root flake exports the locked CLI, production runtime image, image metadata
and fingerprint, plus `nixosModules.default`. The module runs one local
`p.service` as a persistent non-root account in the confined `incus` group;
root or administrative Incus membership refuses evaluation. The machine owner
still provisions Incus, the restricted user project/profile, storage pools,
allowed disk paths and imported base image. Enabling P does not widen them.

The hardened service accepts only `network: none` policies and no public-egress
substrate, so its image builders stay offline too. Its no-new-privileges
and empty capability boundary prevent the scoped privileged network proofs
needed for public egress. Public-egress
operation remains available through the separately owner-run daemon and its
validated, fixed-argument proof commands; the service module refuses that
configuration without weakening its hardening.

`services.p.settings` supplies trusted non-secret host configuration. Schema and
state directory are fixed by the module; a private, singly-linked, daemon-owned
`STATE/host.json` is generated at each service start. Edit declarative settings
rather than that generated file. `services.p.bundledActivation = true` approves
generating the bundled selection once at `STATE/activation.json`; existing
activation remains unchanged across restarts and can be explicitly updated by
the trusted owner. Configuration and activation are validated by the normal
daemon boundaries before runtime work. Service Stop or restart retains the
private state and native sessions; normal lifecycle cleanup retains its existing
authority and recovery rules. The module supplies no backup, restore or software
upgrade/rollback behavior.
The module sets the client environment's `P_SOCKET` for its configured state
directory; [the control API reference](control-api.md#host-framing-and-request-envelope)
owns endpoint selection and overrides.

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

Instance configuration selects the runtime driver, whose Incus settings name
the confined Incus socket and project, and the P base-image identity. A project
cannot select a different Incus authority.

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

Runtime creation receives the image separately, as the reference the runtime
driver's image operations returned for the session's commit image
([environment building](environment-building.md#runtime-driver-image-operations)).
Lifecycle code does not interpret the reference; the Incus driver's reference
is an image fingerprint. SQLite indexes each commit image by project, commit,
base image, system and builder version, and creation records the reference its
runtime was created from.

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
| Commit image bytes, reference and labels | the runtime driver's backend: the Incus image store | indexes (project, commit, base image, system, builder version) to the image reference |
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
| Commit image: base, initial Nix store, and the clone at the commit | shared immutable parent | kept while another session from its commit exists, then deleted by release | Incus image store |
| External filesystem grant | remains external | never removed by P | host owner |

The commit image contains the prepared Nix closure and database and a clone of
the commit at `/workspace`. Incus creates a private writable instance root from
it. Nix store paths remain immutable by
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
| `/nix` | image store plus private session additions |
| `/opt/p/endpoints` | fixed Incus staging mount for session-specific P Unix sockets |
| `/run/p` | read-only private bind of the staged sockets used by session processes |
| `/etc/p/git` | session-specific P Git credentials and trusted SSH configuration in the private instance root |
| `/opt/p` | P runtime kit and trusted systemd/attachment support |
| `/mnt/p/<grant-name>` | explicit external filesystem grant |
| `/var/p/<name>` | declared runtime-owned data/cache directory |

The image may not replace these paths with conflicting mounts or
declarations. Incus mounts the P-owned per-session socket directory read-only
at `/opt/p/endpoints` because NixOS activation recreates `/run` as tmpfs during
boot. Before the interactive host starts, a root-owned fixed helper validates
that exact staging mount and its two sockets, then makes a private read-only
bind of the same directory at `/run/p`. Both paths must refer to the same
socket objects; neither mount is writable or shared. Trusted session assembly
installs `/etc/p/git` through the confined
Incus file API after instance creation; those files survive stop/start in the
private instance root. Stopped-instance assembly reuses identical root-owned
files and refuses unexpected existing files.

A fixed first-boot helper puts `/workspace` on the assigned branch through only
the session's P Git SSH key. With a commit image, the image already holds a
clone detached at the captured commit, with no branch or remote, and the helper
adopts it (workspace v2). It runs only as uid/gid `1000:1000` in `/workspace`,
with the same Git restrictions as a first clone. It checks that HEAD is at the
captured commit and that the commit's root flake matches the image's devShell
selection; adds `origin` as `ssh://git@p/<repository>`; fetches the assigned
branch and requires it still at the captured commit; creates and checks out
that branch with `origin` as its upstream; and writes the initialization guard
last. Each step accepts the state its own earlier run left, so an interrupted
adoption is repeated from the start; once the guard exists, Start changes
nothing. Without a commit image, when the host configures no
`runtime.environment` or for a blank project's bootstrap, the helper clones the
assigned branch from P (workspace v1), which refuses a captured root
`flake.nix` because there is no image to activate it.

A first clone from P is transactional. A root supervisor owns
`/.p-workspace-init`, mode `0700`, and creates an unpublished staging tree
inside it. A child with a private mount namespace binds that tree at
`/workspace`, then runs all Git commands as uid/gid `1000:1000` with only the
session's Git credentials. The staging parent remains inaccessible to the
session user. Neither the bind nor its mount propagation reaches the normal
runtime namespace. After the child exits successfully, the supervisor flushes
the checkout and its initialization guard, atomically renames the tree over
an empty `/workspace`, and flushes both parent directories.

An interrupted initialization leaves either an empty `/workspace` and private
scratch, or a complete published checkout. Retry can discard only unpublished
scratch under the verified root-private parent. Unexpected scratch entries or
an unsafe parent block cleanup. A nonempty `/workspace` without a valid
initialization guard is preserved and blocks creation. A published checkout
keeps ordinary user changes, including dirty files, detached HEAD, remotes,
and later flake inputs. The guard records completed initialization only;
systemd remains the authority for interactive-host readiness. The supervisor
adds no Incus operation journal or duplicate lifecycle phase state.

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

P validates every source path component and rejects symlinks, dangling,
overlapping, or changing sources. It rejects the host root, whole home,
P/Incus state and sockets, credential trees, host Nix state, and other broad
control paths.

The source must also fall within a path prefix already authorized by the
confined Incus project. P failing that check is a configuration error; P never
falls back to the admin socket to attach it. Mounts are private and
non-propagating, and P never deletes their host contents.

The endpoint source follows the same upper-bound rule but is generated and
owned by P rather than selected by project configuration. Its sole Incus
device target is `/opt/p/endpoints`; the public runtime endpoint path is
`/run/p` after the fixed helper prepares it. The source contains only
`session.sock` and `git.sock`, is mounted read-only and private without UID
shifting, and is attached only to its assigned runtime. On the host, the
P-daemon-owned socket directory has mode `0755` below an unmounted,
P-daemon-owned `0700` ancestor. Each socket has mode `0666` so the runtime's
isolated UID mapping can connect. The private host ancestor prevents other
host users from traversing to the sockets; sibling runtimes receive no mount
of this directory or its ancestors. A runtime verifies the read-only mount
and fixed socket contents rather than requiring a mapped owner on host files.

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

The public-session image uses a root-configured, loopback-only DNS forwarder.
Its fixed Cloudflare and Quad9 upstreams use DNS over HTTPS on TCP 443 with
literal bootstrap addresses `1.1.1.1` and `9.9.9.9` and certificate validation
for `cloudflare-dns.com` and `dns.quad9.net`. It does not use host/LAN DNS,
system-resolver bootstrap, downloaded resolver lists, or plaintext external
port-53 fallback. DoH redirects are refused so a provider response cannot
introduce a different resolver host. Ordinary applications use the session's loopback DNS endpoint;
the resolver's only upstream transport is HTTPS. A `none` session does not start
this resolver or gain a public NIC.

There is no unsolicited inbound access, host network mode, published port, or
general host route. A public forge may be reachable, but the runtime still
receives no origin remote or origin credential.

An image builder is not a session, yet it gets this `public-egress`
network whenever the host configures the substrate, whatever its project's
session policy: the same NIC, ACL and proofs, its creating session's address
slot, and the same guest resolver, configured by the runtime kit as root after
each boot. Without the substrate a builder has no NIC.
[Environment building](environment-building.md#security-boundary) owns the
builder's network.

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
instance's private root and validated network profile. Incus is P's required
isolation boundary for Nix evaluation, builds, and sessions. Nix build
sandboxing is disabled inside P-managed containers. Builds use the instance's
private filesystem and permitted network access without an additional
per-build namespace sandbox. The container baseline above, including disabled
nesting, still applies.

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

## Project user services

The production browser extends the CLI-first baseline with session-local user
service inspection and control. Project services are `p-project-*.service`
units installed by the session user in its systemd user manager. The base image
enables lingering for user `p` (UID/GID 1000); the manager and all project
processes remain inside the same unprivileged container and captured grants.
This adds neither system-unit privileges nor host port publication.
Interactive terminals inherit the same session-user endpoint through
`XDG_RUNTIME_DIR=/run/user/1000` and
`DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus`, so ordinary
`systemctl --user` commands address their own manager. Host startup restores
these runtime-owned values after devShell activation before starting tmux.

Host-only service RPC binds the exact established session, excludes
rename/removal conflicts, and calls the runtime driver's services operations at
the runtime's current incarnation. A service action holds the
session's lifecycle lock. An inventory or journal read never overlaps a
holder of that lock: it is refused while the lock is held, and a request that
takes the lock waits for running reads to end
([concurrency](session-lifecycle.md#concurrency)). Core checks only that a
service name is printable and at most 128 bytes. The Incus driver admits only
`p-project-*.service` units the session has, refusing any other name before
its service manager is reached, and executes fixed systemctl/journalctl argv
with UID/GID 1000 and the fixed `/run/user/1000` user-manager endpoint. It
checks its boundary before an action, and the existing Incus ownership,
security, filesystem, resource and network checks remain. The native
UUID/generation is rechecked after observation/action; unavailable or changed
authority never yields a success claim.

Loaded unit observations and installed unit files are bounded to 64 distinct
project units. An installed unit the user manager has not loaded is `stopped`,
since the manager unloads only inactive units. Journals are bounded recent
tails. User service content has ordinary workspace-user authority and remains
subject to Incus isolation. Root/system/P infrastructure units are never
project services. Older images without a usable user manager report
unavailable and are not automatically modified. A service action returns once
systemd has queued it (`systemctl --no-block`); the inventory shows its
progress.

## Credentials

The session receives only:

- its UUID-bound P Git private key and known-host entry;
- its private session-RPC endpoint;
- explicitly named secret-bearing filesystem grants.

It never receives host-origin SSH authority, the Incus socket, host Codex or
OpenAI credentials, event-handler configuration, or another session's
material. The user may authenticate Codex inside the session's private home;
P does not read, copy, or manage those tool-owned files. P installs the session
Git material under `/etc/p/git` in the private instance root: a root-owned
`0755` directory, root-owned `ssh_config` and `known_hosts` files readable by
`p` and not writable by it, and the `p`-owned `0400` private key `identity`.
These files are never placed on the host socket mount. P-provisioned secrets
are omitted from instance metadata, logs, SQLite diagnostics, and TUI previews.

## Attachment

Every supported persistent host supplies the same two runtime artifacts:

- `p-interactive.service`, which systemd owns for startup, readiness, lifetime,
  and logs; and
- the fixed root-owned `/usr/libexec/p/attach`, which connects one PTY to the
  already-running host.

The attach command is a short-lived adapter, not a daemon or supervisor. The
Incus driver's `OpenTerminal` first checks that the runtime is at the
requested incarnation, running, with its host ready, and that the image's
links to the unit and attach program resolve to the selected host package's
assets, unchanged. It then opens a structured Incus exec operation as the
fixed session user with an explicit PTY, working directory, and argv
containing only `/usr/libexec/p/attach`. Before the exec starts it arms its
completion witness, a wait on Incus's exec operation, and it keeps that
witness after Incus expires the operation record. The adapter replaces itself
with fixed host-specific argv, such as a tmux attach client addressed by a
root-owned socket/session descriptor. It accepts no client-selected command,
socket, session name, or shell string.

Tmux is the shipped MVP default. Another persistent host is supported only when
its systemd unit and attach command pass the same conformance contract.

The client and the attachment helper never receive Incus credentials or any
runtime detail: the daemon opens the terminal through the runtime driver and
relays it over the helper's control connection. A future remote P client may
initiate SSH to the P host and execute the daemon-approved attachment path; it
still does not connect to Incus directly.

A pending token is not attachment presence. Lifecycle first checks through the
driver that the host is ready and issues a pending token. When the trusted host
helper claims it, the daemon checks again, opens the terminal, counts the
attachment, and relays the terminal. The driver owns the native channel and its
witness; the daemon owns the token and the lease.

The daemon binds the terminal's lifetime to the helper's connection without
depending on client cleanup: client crash, SIGKILL, helper or carrier loss, or
client-machine loss closes the connection, and the daemon closes the terminal.
It keeps the attachment's lease until the witness proves the attached process
ended. If the driver fails while the daemon stays up, the terminal ends
unproven: the helper tears down, input and resizes stop and are not replayed,
and the lease stays, so discard and delete stay refused. It ends when the
driver settles the terminal as ended, which the daemon asks every few seconds,
or when the session stops; such a lease does not refuse Stop, and a Stop that
leaves the runtime stopped ends it. A daemon restart ends attachments; MVP does
not re-register an already-running terminal. Teardown ends only the temporary
attach client; the systemd-owned persistent host and configured interactive
command continue.

## Non-activating workspace access

P reads or changes a session's workspace only while the session is stopped,
and never freezes a running session. Deletions do not read the workspace at
all: they state loss by category
([session lifecycle](session-lifecycle.md#runtime-local-loss)). Rename's
workspace Git changes must not run repository hooks, activation, or the
interactive command. Core therefore makes them itself, under the lifecycle
lock, through the driver's three workspace operations, `ReadWorkspace`,
`WriteWorkspace` and `RemoveWorkspace`, with paths relative to the workspace
root; P creates no helper container for them. The operations:

- act only on the stopped runtime at the incarnation the rename recorded (on
  Incus, the instance UUID and its generation), and refuse a running one;
- reach only the session's private storage, never following a symlink at or
  above the path ([stopped storage](#runtime-driver));
- start no process in the runtime and use no external grant, session
  credential, network, or project activation;
- read and write exact type, owner, mode and bytes within a bound; and
- call the caller's gate right before a write or a removal, so that core
  records its intent first. One whose outcome is unknown is settled through
  `Settle`, never sent again.

On Incus they use the file API of the stopped instance. Core reads only the
clone's Git administration files and never runs Git hooks, helpers, monitors,
or ambient Git configuration.

The P base image uses static local account lookup (`files` for passwd/group)
and does not run nscd/nsncd. Hostname lookup uses `files dns`; additional NSS
modules and dynamic account providers are outside this substrate contract.
This image configuration does not change the host's name services.

The Incus driver reads the fixed P-owned systemd journal and diagnostic paths
of a stopped instance through the same file API (`HostLog`). It does not start
the instance or treat logs as lifecycle authority.

Rename's reads and edits cover only the clone's HEAD, branch ref, reflog,
upstream configuration and its own backup file. P never runs arbitrary argv,
reset, clean, automatic commit, or force-push in a workspace.

## Reconciliation and cleanup

P lists and inspects instances only in its configured confined Incus project.
It matches deterministic names and stable P metadata to the P instance and
session UUID. It never adopts or deletes by a human branch name.

For an active session:

- zero matching instances is `missing`;
- one verified instance supplies current runtime state;
- a name/metadata conflict is an ownership error requiring manual Incus
  investigation; and
- machinery belonging to another P instance is never touched.

Start, stop, exec, and delete outcomes come from Incus state and Incus
operations. After connection loss P inspects Incus; it does not infer success
from its own RPC failure or replay Incus operations blindly.

P stops a running session or builder container in three steps:

1. It waits, at most 30 seconds, until the guest's systemd has created
   `/run/systemd/private` on that boot's `/run`, read through the Incus file
   API. Incus sends a graceful stop to the container's init once, and the
   image's init loses it until systemd is up: before then it is the NixOS
   stage-2 script or systemd's own start. If the wait ends first, P goes on.
2. It requests Incus's graceful stop (`incus stop --timeout 30`), which Incus
   gives up after 30 seconds.
3. If the container is still not stopped, it stops it by force
   (`incus stop --force`).

P then checks that the container is stopped. A failed start, stop or delete
keeps Incus's own bounded error text.

While Incus cannot render an instance, as during its cleanup after a poweroff
the guest started, it lists a placeholder with status `Error` and no
configuration. P lists the instance again every 100 milliseconds, for at most
5 seconds, before it checks the instance's identity. The driver reports a
placeholder that persists as the instance's state unavailable, never as an
identity mismatch, and the daemon answers the request `failed` with reason
`runtime`.

Discard removes the verified Incus instance, its private root, endpoint
directory, and session credentials while retaining the P branch. After the
removal commit point, discard and delete stop a running instance in the three
steps above, through the driver's `Stop`, then delete it through the driver's
`Delete` at the reviewed incarnation. Before `Delete` inspects the instance, it
polls Incus's operation list, for at most 15 seconds, until it shows no
unfinished operation on the instance: an exec whose client was killed before
it connected stays listed for up to 10 seconds. An operation still unfinished
after the wait blocks the removal until Retry. A delete whose outcome is
unknown is settled through `Settle` and never sent again. Discard never
deletes an image itself; once no session started from the session's commit
remains, the daemon's release step deletes that commit's image
([environment building](environment-building.md#lookup-concurrency-and-image-lifetime)).

Confirmed failed-creation cleanup of an assembled runtime deletes only the
exact stopped instance it reviewed: native deletion targets the reviewed
incarnation (on Incus, the instance's UUID and generation) and owned inventory
after atomic authority retirement. Unknown
delete admission or effects retain the source identity and cleanup guard
until the driver settles the delete as applied; nothing authorizes another
deletion. The supported creation/policy
boundary belongs to
[session lifecycle](session-lifecycle.md#confirmed-failed-create-cleanup).

## Failure posture

| Failure | Required result |
|---|---|
| Incus user socket unavailable | Runtime state is `unreachable`; do not create duplicates or claim stop/removal |
| Configured Incus project missing or unrestricted | Refuse runtime creation with an actionable host-setup error |
| Commit image missing | Existing instances continue. The next creation from that commit forgets the index row and builds a new image from the commit. |
| Instance absent | Report `missing`; do not recreate the session's runtime. The user discards or deletes the session and creates a new one; see [session lifecycle](session-lifecycle.md#inconsistent-sessions) |
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
- an immutable commit image plus private writable instance root;
- persistent workspace, home, and private Nix state across stop/start;
- project-scoped filesystem grants within the Incus project ceiling;
- `none` and validation-gated `public-egress` profiles;
- systemd-owned persistent interactive hosting with tmux as the shipped
  default;
- one fixed attachment command, whose terminal the daemon relays after a
  one-use claim; and
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
3. the commit image remains immutable, new session paths never appear in
   another session, and physical sharing/copying matches the recorded storage
   driver behavior;
4. start/stop state and operation outcome come from Incus and survive a P
   daemon restart without duplicate instances;
5. discard removes only the verified instance/private root and retains the P
   branch; it deletes no image itself, and release deletes the commit's image
   only once no session from that commit remains;
6. sessions cannot reach Incus, host credentials, undeclared mounts, sibling
   instances, or disallowed network destinations;
7. attachment uses fixed structured argv, gives the client and helper no Incus
   authority, and a terminal that failed to open never becomes attachment
   presence;
8. the non-activating workspace interface acts only on a stopped instance and
   cannot execute repository-controlled hooks or arbitrary commands; and
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
