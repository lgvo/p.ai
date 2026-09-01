# P

P is a local-first control plane for isolated development sessions, organized
by Git projects and branches.

It gives one developer a terminal overview of work across repositories, starts
each piece of work in its own runtime, and keeps source movement explicit. P is
open source under the [Apache License 2.0](LICENSE), runs on machines the user
controls, and can grow from a laptop to a larger Linux or Kubernetes host
without changing its project model.

> **Status: design.** The design documents under [`docs/`](docs/) are
> authoritative for their subjects. This README is the product summary.

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
not separate accounts or policy objects in V1. Trusted host configuration is
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

A runtime is the execution machinery currently attached to a session UUID. V1
uses one unprivileged Incus system container per session in a pre-provisioned,
confined local Incus user project. Incus is the only V1 runtime backend. Incus
VMs and Kubernetes placement are later options, not different session models.

Every image implements the same systemd contract. `p-session.target` starts
`p-interactive.service`, which launches and supervises one persistent
interactive host; tmux is the default, while another long-running host such as
screen or zellij can be selected by trusted configuration. The root-owned
`/usr/libexec/p/attach` entrypoint attaches a temporary terminal channel to
that host.

Detach, SSH loss, and switching sessions close only the temporary attachment.
If the persistent host exits, systemd captures its result and the container
stops. A later Start launches the configured host again. P does not model panes
or infer agent status from terminal contents.

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
latest. V1 has no seen/unseen history, attention set, participant inventory,
project-service orchestration, or terminal heuristic pretending to know agent
intent.

Agent integrations translate native lifecycle hooks into the generic
`status.report` JSON-RPC notification (the protocol message type) on the
private session RPC socket. Claude Code, Codex, and other tools remain ordinary
interactive commands; no P-specific agent wrapper is required.

Separately, P exposes typed, versioned reduced events to configured handlers.
V1 provides a redacted local NDJSON log handler. Events are an extension seam,
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

The control API is newline-delimited JSON-RPC 2.0 over Unix sockets. Linux
clients support both direct local Unix connections and client-initiated
SSH-to-Unix bridging from day one. Attachment uses a separate validated argv
path through a trusted host helper that binds lease, terminal carrier, and
channel lifetime; the daemon never initiates SSH and never returns a shell
string.

Git carries commits, objects, and refs. RPC carries lifecycle, configuration,
runtime state, attachment leases, status, and subscriptions. The complete
division is in [communication boundaries](docs/communication-boundaries.md).

## Environments and isolation

Every session begins from a P-owned Incus base image containing Nix, a shell,
basic userland, Git and SSH, CA certificates, tmux, and P's runtime helper. The
project environment is selected in V1 as:

1. the project's ordinary default Nix `devShell`, when present; or
2. the minimal substrate alone.

The V1 Nix builder consumes immutable committed source in a disposable,
restricted Incus builder. It realizes the devShell and publishes a verified
private Incus image with a coherent Nix store/database and activation material.
Each session receives its own writable Incus root on top, including a private
`/nix`, workspace, and home. Sessions do not mount the host Nix store/daemon or
share a writable store. A session may build additional private Nix paths; they
survive stop/start and disappear with the session. Cache behavior, physical
deduplication, and build time remain project/storage-driver dependent.

The `EnvironmentBuilder` interface remains reusable, but Dockerfile and OCI
providers are not specified for V1.

V1 external grants are project-scoped under `{project}/*`. The namespace
`{project}/{branch}` is reserved for later branch-specific grants. A session
created from any source receives only its project's trusted mounts, model
access, and network policy.

Repositories cannot configure P session behavior or request host mounts,
devices, credentials, published ports, the Incus socket, the SSH agent, or
routes to the host and local network. Trusted host configuration may grant
named filesystem mounts, network posture, and model access through typed
isolation interfaces.

The implementation baseline has no general network. A project may select a
validated public-egress profile, but it still cannot reach the host,
LAN/private or link-local networks, metadata endpoints, gateways, sibling
instances, or Incus administration. P Git, private session RPC, and explicitly
granted model inference use narrow session endpoints. P V1 does not declare,
supervise, health-check, or restart project services.

See [environment building](docs/environment-building.md) for Nix image
building, activation, and caching, and
[runtime and isolation](docs/runtime-isolation.md) for runtime assembly,
storage, mounts, network policy, labels, and cleanup.

## Model access

Bifrost is an optional independently configured gateway. It owns provider
credentials, models, aliases, routing, limits, usage, MCP, skills, and
virtual-key policy. P does not proxy or reimplement model APIs.

For a model-enabled project, P obtains and persists one Bifrost virtual key per
session UUID, exposes it only to that runtime, redacts it everywhere else, and
revokes it when the session is discarded or deleted. Projects without model
access do not depend on Bifrost readiness.

Initial creation validates configured model access before the session becomes
established. After that, a Bifrost outage degrades model calls only: Start and
Attach do not probe Bifrost or prevent use of Git and the terminal.

Bifrost's native authentication is the V1 enforcement boundary: administrative
authentication remains enabled, sessions receive no administrative credential,
and every inference request requires the session key. P validates that the key
works only for approved inference/model discovery and is rejected by every
administrative and other non-V1 route; an unvalidated version, configuration,
or route inventory fails model access closed.

V1 validates Bifrost's OpenAI-compatible surface with Codex, using OpenRouter
and a local model as initial upstreams. Anthropic-compatible access for Claude
Code follows in phase two. Bifrost remains suitable when a P instance moves to
Kubernetes; Envoy AI Gateway is a later option only if cluster-native shared
ingress, distributed policy, or inference-fleet routing justifies it.

See [model gateway](docs/model-gateway.md).

## Configuration

V1 has one P configuration authority: trusted host configuration keyed by the
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

SQLite stores mutable instance bookkeeping: projects, immutable
session UUIDs, project/branch assignments, the configured Incus instance
relationship, cross-authority lifecycle phases, reduced session/policy
conditions, unattended condition, credentials, event diagnostics, and small
authority indexes. Git remains
authoritative for code and refs; Incus remains authoritative for instances,
images, storage, and runtime operations; Bifrost remains
authoritative for gateway policy.

## Requirements

V1 targets Linux and expects:

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

The daemon and execution backends remain Linux-only in V1. A user can run the
TUI on a Linux host over an ordinary SSH login, or use the Linux client's
day-one SSH-to-Unix transport from another Linux machine. Native macOS and
Windows client binaries are later packaging work built on the same transport
and RPC protocol; they do not introduce a remote-runtime backend.

## Non-goals

- P is not a multi-user scheduler or agent orchestration framework.
- P instances do not federate or replicate control-plane state.
- P does not migrate running instances, terminals, processes, conversations,
  or uncommitted files.
- P does not invent source commits or snapshot dirty host work.
- P does not automatically publish to origin.
- P does not give session runtimes origin credentials or general host/LAN
  access.
- P does not manage application services in V1.
- Checks and attempts are reserved future ideas, not V1 APIs.

## V1 scope

V1 closes the loop for one user on Linux:

- create explicit SSH-origin or blank projects and create sessions from
  committed sources, with one unborn-`main` bootstrap exception;
- enforce immutable UUID-to-branch ownership and transactional rename;
- build cached Nix environment images and run unprivileged local Incus
  containers;
- enter a systemd-supervised persistent interactive host through a fixed
  attach entrypoint;
- use a thin TUI and complete RPC lifecycle surface locally or over
  SSH-to-Unix;
- report session condition, confirmed attachment presence, the latest
  unattended agent condition, and policy condition;
- fetch from and explicitly publish to an optional origin;
- enforce project-scoped external grants and Git credentials;
- optionally provide model access through per-session Bifrost keys;
- emit reduced typed events to a local file handler; and
- reconcile SQLite, Git, credentials, and runtime state after interruption.

Implementation uncertainties that need real-machine evidence are tracked in
[development validations](docs/development-validations.md). They gate only the
affected milestone or capability unless evidence disproves a core invariant.

The current documentation/readiness snapshot is in
[V1 status](docs/v1-status.md), and the complete remaining-work tracker is in
[missing pieces](docs/missing-pieces.md).

The implementation choices and build-vs-buy decisions are in
[technology stack](docs/technology-stack.md). The [FAQ](docs/FAQ.md) explains
the main tradeoffs, and [prior art](docs/prior-art.md) records neighboring
projects without defining P's behavior.

Repository-specific terms are defined in the [glossary](GLOSSARY.md).
