# P

P is a self-contained, extensible control plane for concurrent streams of
agent-driven development, organized by Git projects and branches.

It gives one developer a terminal overview of work across repositories, starts
each piece of work in its own runtime, and keeps source movement explicit. P is
open source under the [Apache License 2.0](LICENSE), runs on machines the user
controls, and can grow from a laptop to a larger Linux or Kubernetes host
without changing its project model.

> **Status: design.** [PROJECT.md](PROJECT.md) owns P's enduring project
> guidance, and [product direction](docs/PRODUCT.md) owns P's product strategy.
> The design documents under [`docs/`](docs/) are authoritative for their
> subjects. This README is the product summary.

## The idea

Agentic development creates more concurrent workspaces than a person can track
comfortably. Isolation solves interference between them, but it does not answer
what exists, what needs attention, where the durable work is, or how to resume
it elsewhere.

P supplies that missing control plane:

- one flat TUI across explicit Git projects;
- one isolated runtime for each active session;
- one real Git branch owned by each session;
- explicit status from agent hooks rather than terminal guessing;
- a local Git server as the source of truth for session code; and
- explicit publication to a shared `origin` when one exists.

P does not coordinate agents or machines with a new distributed protocol. Git
carries source. A small RPC API carries lifecycle and status.

## Concepts

### Instance

A P instance is one independent control plane with its own daemon, SQLite
registry, Git server, projects, sessions, and one configured local Incus
execution project. The daemon manages only machinery belonging to that
instance; it never connects to another machine to manage runtimes.

Instances do not discover, address, or synchronize with one another. Two
instances working on the same project converge through the project's ordinary
shared Git origin, just as a laptop and desktop do today.

### Project

A project is the complete path of one repository on the P Git server, such as
`p` or `lgvo/p`. Optional namespace components are ordinary path structure,
not separate accounts or policy objects in MVP. Trusted host configuration is
keyed by that project path; repositories contain no P-specific configuration.

An origin is optional. A project without one is intentionally local-only; P
bypasses fetch, comparison, and publication instead of reporting a broken sync
state. Projects are created explicitly from an SSH origin or as blank local
repositories. P never infers or imports the host's current checkout.

### Session

A session is P's durable identity for one piece of branch work. P allocates an
immutable UUID and maintains a strict invariant:

```text
session UUID ↔ one logical Git branch
```

The session's mutable human name is the real Git branch name. Its complete
address is `(project path, branch name)`, so two projects may use the same
branch name. Creation accepts a name or suggests a timestamp. Common source
names such as `main` can seed a session but cannot themselves be the new
session branch outside the bootstrap exception.

Renaming a session renames its actual P-server and workspace refs while keeping
the UUID, runtime, credentials, and interactive state. The operation is
journaled so reconciliation can finish an interrupted rename. It never renames
or deletes an origin branch.

There are no scratch sessions and no stored `base_commit` state. Ordinarily a
session starts from an existing committed source and immediately owns a newly
created session branch. The single bootstrap exception is a new blank project
or a successfully contacted empty origin: its first session owns an unborn
`main`, and the first push creates that ref. Later sessions require committed
source. P never reads or snapshots a host checkout.

### Runtime and persistent interactive host

A runtime is the execution machinery currently attached to a session UUID. MVP
uses one unprivileged Incus system container per session in a pre-provisioned,
confined local Incus user project. Incus is the only MVP runtime backend. Incus
VMs and Kubernetes placement are later options, not different session models.

Every image implements the same systemd contract. `p-session.target` starts
`p-interactive.service`, which launches and supervises one persistent
interactive host; tmux is the default, while another long-running host such as
screen or zellij can be selected by trusted configuration. The root-owned
`/usr/libexec/p/attach` entrypoint attaches a temporary terminal channel to
that host.

Detach, attachment-transport loss, and switching sessions close only the
temporary attachment. If the persistent host exits, systemd captures its
result and the container stops. A later Start launches the configured host
again. P does not model panes or infer agent status from terminal contents.

## A normal workflow

Open the overview, create a project explicitly from an SSH origin or as a blank
repository, and then create a session. The exact TUI layout and key map will be
chosen through a prototype rather than fixed in the architecture documents.

Creating an ordinary session selects a committed source and a project. P then
reserves a UUID and new branch name in that project, creates the P-server
branch, issues its credentials, assembles the runtime and workspace, prepares
the interactive host through systemd, and reports ready. The bootstrap session
instead begins on unborn `main`. The normal TUI flow then attaches as a
separate lifecycle action.

Inside the session, ordinary Git commits and pushes update only that assigned
P branch. When the work matters elsewhere, publish it explicitly to a selected
origin branch. P never automatically pushes to origin.

To continue on another machine:

1. publish the session branch to the shared origin, or fetch it from P into a
   normal checkout and push it with ordinary Git;
2. fetch the origin on the other machine's P instance; and
3. create a new session from that committed branch.

Only committed Git content moves. The runtime, uncommitted files, processes,
terminal state, and agent conversation do not.

## Lifecycle

P never reclaims sessions automatically. The three destructive levels are:

| Action | Runtime | Working copy | P branch | Session record |
|---|---|---|---|---|
| Stop | stopped and restartable | retained | retained | retained |
| Discard | removed | removed | retained as a project source branch | removed |
| Delete | removed | removed | removed | removed |

Before discard or delete, P inspects reachable running or stopped workspaces
without activating the environment or starting the interactive command. If an
instance is missing, P says its local state is already unavailable and offers
cleanup. If Incus is unreachable, ordinary destruction is refused; an explicit
abandonment override requires stronger confirmation and records the unknown
machinery so it can later be recognized as orphaned.

Delete separately itemizes Git refs and commits that lose their last retained
reference. A configured origin is refreshed before P makes claims about what
survives there. Local-only projects use retained P refs alone.

The UUID↔branch invariant applies while the ref is assigned to a session.
Discard releases that assignment and leaves an ordinary P branch; continuing
from it creates a new UUID and a new session-owned branch.

Creation, start, attachment, rename, stop, destructive preflight, repair,
abandonment, and crash recovery are specified in
[session lifecycle](docs/session-lifecycle.md).

A separate project-level operation, **Delete project and all P data**, previews
all sessions, retained branches, credentials, runtimes, and attachments that
will be removed. Confirmation authorizes termination of the listed live
attachments. P records a minimal durable tombstone and idempotently ensures
each listed resource is absent; if some cleanup fails, the user retries and P
reports the smaller remainder. Project creation, retained branches, origins,
and whole-project deletion are specified in
[project lifecycle](docs/project-lifecycle.md).

## Overview and status

The main UI will be a thin, filterable client of the same RPC surface available
to scripts; business logic does not live in the TUI. Exact layout, navigation,
keys, and initial slice are deferred until a prototype can test them.

Each session presentation is derived from only four facts:

- session condition (`creating`, `starting`, `ready`, `stopped`, `missing`,
  `unreachable`, `discarding`, or `deleting`);
- confirmed live attachment count;
- one nullable latest condition reported while the session was unattended;
  and
- policy condition (`current`, `outdated`, or `invalid`).

Successfully establishing and confirming the first attachment to a previously
unattended session clears that latest condition. Merely requesting an attach
does not. While the user is attached, semantic agent reports are not retained
as status. Leaving begins empty, and the next report becomes the
latest. MVP has no seen/unseen history, attention set, participant inventory,
project-service orchestration, or terminal heuristic pretending to know agent
intent.

The bundled Codex adapter translates native lifecycle hooks into the generic
`status.report` JSON-RPC notification (the protocol message type) on the
private session RPC socket. Codex remains an ordinary interactive command; no
P-specific agent wrapper is required. Other agents may run as commands, but
their hooks, status reporting, and compatibility are not supported in MVP.

Separately, P exposes typed, versioned reduced events to configured handlers.
MVP provides a redacted local NDJSON log handler. Events are an extension seam,
not another state authority or a built-in notification protocol.

See [session observability](docs/session-observability.md) for the reducer and
adapter rules.

## Git and RPC

### Hub and spoke — decided

Each P instance has one bare Git repository per project. Session runtimes and
ordinary host checkouts are spokes around that local hub. The session runtime
gets a scoped SSH key that can write only its UUID-assigned current branch,
fast-forward-only. P has no force-push exception. Rebasing unpublished local
commits is ordinary Git; rewriting history already recorded on a P branch is
performed outside P by fetching it to a host checkout and creating the desired
new history.

The instance's dedicated host P key is read-only: it can clone and fetch all
user-visible branches, including retained branches, but cannot
mutate P refs. The daemon alone owns branch creation, rename, guards, and
deletion. Host fetch and explicit publication against origin use the user's
normal OpenSSH credentials, not P session credentials.

The control API is newline-delimited JSON-RPC 2.0 over Unix sockets. MVP
clients connect directly to the local Unix socket. Attachment uses a separate
validated argv path through a trusted host helper that binds lease, terminal
carrier, and channel lifetime. A client-initiated SSH-to-Unix transport is
post-MVP; the daemon never initiates SSH or returns a shell string.

Git carries commits, objects, and refs. RPC carries lifecycle, configuration,
runtime state, attachment leases, status, and subscriptions. The complete
division is in [communication boundaries](docs/communication-boundaries.md).

## Environments and isolation

Every session begins from a P-owned Incus base image containing Nix, a shell,
basic userland, Git and SSH, CA certificates, tmux, and P's runtime helper. The
project environment is selected in MVP as:

1. the project's ordinary default Nix `devShell`, when present; or
2. the minimal substrate alone.

The MVP Nix builder consumes immutable committed source in a disposable,
restricted Incus builder. It realizes the devShell and publishes a verified
private Incus image with a coherent Nix store/database and activation material.
Each session receives its own writable Incus root on top, including a private
`/nix`, workspace, and home. Sessions do not mount the host Nix store/daemon or
share a writable store. A session may build additional private Nix paths; they
survive stop/start and disappear with the session. Cache behavior, physical
deduplication, and build time remain project/storage-driver dependent.

The `EnvironmentBuilder` interface remains reusable, but Dockerfile and OCI
providers are not specified for MVP.

MVP external grants are project-scoped under `{project}/*`. The namespace
`{project}/{branch}` is reserved for later branch-specific grants. A session
created from any source receives only its project's trusted mounts and network
policy.

Repositories cannot configure P session behavior or request host mounts,
devices, credentials, published ports, the Incus socket, the SSH agent, or
routes to the host and local network. Trusted host configuration may grant
named filesystem mounts and network posture through typed isolation
interfaces. Post-MVP external service plugins may add separately typed service
bindings.

The implementation baseline has no general network. A project may select a
validated public-egress profile, but it still cannot reach the host,
LAN/private or link-local networks, metadata endpoints, gateways, sibling
instances, or Incus administration. P Git and private session RPC use narrow
session endpoints. P MVP does not declare, supervise, health-check, or restart
project services.

See [environment building](docs/environment-building.md) for Nix image
building, activation, and caching, and
[runtime and isolation](docs/runtime-isolation.md) for runtime assembly,
storage, mounts, network policy, labels, and cleanup.

## Model access

MVP supports Codex as its sole validated agent integration. Codex runs inside
the isolated session as an ordinary command, and the user authenticates it
within that session's private home. P does not copy, inject, or manage the
host's Codex or OpenAI credentials. The session-local authentication survives
Stop and Start and disappears with Discard or Delete.

Codex use that requires network access uses the project's explicitly selected,
validated `public-egress` grant. P supplies no model endpoint in MVP.

Managed, session-scoped model access through Bifrost is post-MVP. Its retained
design is in [model gateway](docs/model-gateway.md).

## Extensibility

Plugins are how P composes replaceable capabilities, not only optional
integrations around a fixed core. P core retains identity, durable state,
lifecycle orchestration, policy, grants, recovery, activation, and user
confirmation while invoking typed plugin contracts for the selected
implementations.

MVP must prove that model through secure first-party defaults, including Incus
runtime support, a tmux persistent host, Git source and session access, Nix
environment preparation, the structured file-event handler, and the Codex
adapter. Ordinary setup selects and connects those defaults automatically.
Developers and agents can author and test alternatives, but registration does
not activate a plugin or grant it authority; trusted developer configuration
controls both. The usable public interfaces and their basic first-party
composition are sufficient for MVP; a separate agent-authored plugin is not a
release gate.

Internal session services run inside the session boundary under systemd.
External session services retain their native network protocols and security
mechanisms while P provides explicitly granted, session-scoped access.
Host-side capabilities participate in lifecycle work without being exposed to
the session.

The concrete plugin architecture is not designed yet. Packaging, process
model, isolation, transport, compatibility, capability manifests,
installation, composition, and approval UX remain open. See
[product direction](docs/PRODUCT.md),
[project guidance](PROJECT.md#make-extension-a-product-capability), and the
[implementation tracker](docs/missing-pieces.md#plugin-contract).

## Configuration

MVP has one P configuration authority: trusted host configuration keyed by the
complete project path. It supplies project-scoped session defaults and external
grants. Creation records an immutable policy snapshot for the session. Later
configuration changes make that snapshot visibly `outdated`; invalid policy
blocks Start, while an ordinary outdated snapshot remains usable with a
warning and a guided **Recreate with current policy** action. There is no live
grant mutation, branch/session override, or custom P repository schema/flake
output.

The repository contributes only its ordinary pure Nix devShell. P uses the
default devShell, when present, to realize the session environment and
dependencies; otherwise it uses the minimal substrate. The default interactive
command is Bash through tmux. Project creation never generates a project file.

SQLite stores mutable instance bookkeeping: projects, immutable session UUIDs,
project/branch assignments, the configured Incus instance relationship,
cross-authority lifecycle phases, reduced session/policy conditions,
unattended condition, credentials, event diagnostics, and small authority
indexes. Git remains
authoritative for code and refs; Incus remains authoritative for instances,
images, storage, and runtime operations.

## Requirements

MVP targets Linux and expects:

- Git and OpenSSH;
- a locally initialized Incus daemon and confined user project;
- a verified P base image containing the pinned Nix runtime/build toolchain;
  and
- systemd and tmux in the base image for the default persistent interactive
  host contract.

Nix executes inside isolated builder and session instances. Host Nix is not a
runtime installation dependency; a developer or distribution process may use
it to build the P base image.

### macOS and Windows

The daemon, execution backends, and local client remain Linux-only in MVP. A
user may use an ordinary SSH login to the Linux host and run the local TUI
there, but P's client-initiated SSH-to-Unix transport and native macOS or
Windows clients are post-MVP. They do not introduce a remote-runtime backend.

## MVP boundaries

The enduring project non-goals are authoritative in
[PROJECT.md](PROJECT.md#non-goals). MVP additionally has these current scope
boundaries:

- MVP is not a multi-user scheduler or agent orchestration framework.
- P instances do not federate or replicate control-plane state.
- MVP does not migrate running instances, terminals, processes, conversations,
  or uncommitted files.
- P does not invent source commits or snapshot dirty host work.
- P does not automatically publish to origin.
- P does not give session runtimes origin credentials or general host/LAN
  access.
- P does not manage application services in MVP.
- Checks and attempts are reserved future ideas, not MVP APIs.

## MVP scope

MVP closes the loop for one user on Linux:

- create explicit SSH-origin or blank projects and create sessions from
  committed sources, with one unborn-`main` bootstrap exception;
- enforce immutable UUID-to-branch ownership and transactional rename;
- build cached Nix environment images and run unprivileged local Incus
  containers;
- enter a systemd-supervised persistent interactive host through a fixed
  attach entrypoint;
- use a thin TUI and complete RPC lifecycle surface over the local Unix socket;
- report session condition, confirmed attachment presence, the latest
  unattended agent condition, and policy condition;
- fetch from and explicitly publish to an optional origin;
- enforce project-scoped external grants and Git credentials;
- run Codex with authentication owned by the session's private home;
- emit reduced typed events to a local file handler; and
- reconcile SQLite, Git, credentials, and runtime state after interruption.

Implementation uncertainties that need real-machine evidence are tracked in
[development validations](docs/development-validations.md). They gate only the
affected milestone or capability unless evidence disproves a core invariant.

The current documentation/readiness snapshot is in
[MVP status](docs/mvp-status.md), and the complete remaining-work tracker is in
[missing pieces](docs/missing-pieces.md).

The implementation choices and build-vs-buy decisions are in
[technology stack](docs/technology-stack.md). The [FAQ](docs/FAQ.md) explains
the main tradeoffs, and [prior art](docs/prior-art.md) records neighboring
projects without defining P's behavior.

Repository-specific terms are defined in the [glossary](GLOSSARY.md).
