# P

P is a local-first control plane for isolated development sessions, organized
by Git projects and branches.

It gives one developer a terminal overview of work across repositories, starts
each piece of work in its own runtime, and keeps source movement explicit. P is
open source, runs on machines the user controls, and can grow from a laptop to
a larger Linux or Kubernetes host without changing its project model.

> **Status: design.** The design documents under [`docs/`](docs/) are
> authoritative for their subjects. This README is the product summary.

## The idea

Agentic development creates more concurrent workspaces than a person can track
comfortably. Isolation solves interference between them, but it does not answer
what exists, what needs attention, where the durable work is, or how to resume
it elsewhere.

P supplies that missing control plane:

- one flat TUI across registered Git projects;
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
registry, Git server, projects, sessions, and local runtime providers. The
daemon manages only runtimes belonging to that instance; it never connects to
another machine to manage containers or VMs.

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
state.

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
session branch.

Renaming a session renames its actual P-server and workspace refs while keeping
the UUID, runtime, credentials, and interactive state. The operation is
journaled so reconciliation can finish an interrupted rename. It never renames
or deletes an origin branch.

There are no scratch sessions and no stored `base_commit` state. Every session
starts from an existing committed source—such as a branch, tag, or reachable
commit—and immediately owns a newly created session branch. P never snapshots
a dirty host checkout; commit the desired state first.

### Runtime and interactive command

A runtime is the backend machinery currently attached to a session UUID. V1
uses one local rootless container per session. Local VMs and a Kubernetes
backend are later provider options, not different session models.

The program entered inside the runtime may be `bash`, Claude Code, Codex, or
custom fixed argv. An `InteractiveHost` plugin controls how that program is
started and attached:

- `tmux` is the default persistent implementation;
- `direct` is the minimal non-persistent implementation; and
- another host such as zellij can be added later.

P does not model panes or infer readiness and agent status from tmux. Terminal
persistence is a host capability, not session identity.

## A normal workflow

Register the current repository and open the overview:

```console
$ p .
$ p
```

Creating a session selects a committed source and a project. P then reserves a
UUID and new branch name in that project, creates the P-server branch, issues its
credentials, assembles the runtime and workspace, prepares the interactive
host, and reports ready. The normal TUI flow then attaches as a separate
lifecycle action.

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
| Discard | removed | removed | retained as an unassigned source branch | removed |
| Delete | removed | removed | removed | removed |

Before discard or delete, P inspects reachable running or stopped workspaces
without activating the environment or starting the interactive command. If a
runtime is missing, P says its local state is already unavailable and offers
cleanup. If the backend is unreachable, ordinary destruction is refused; an
explicit abandonment override requires stronger confirmation and records the
unknown machinery so it can later be recognized as orphaned.

Delete separately itemizes Git refs and commits that lose their last retained
reference. A configured origin is refreshed before P makes claims about what
survives there. Local-only projects use retained P refs alone.

The UUID↔branch invariant applies while the ref is assigned to a session.
Discard releases that assignment and leaves an ordinary P branch; continuing
from it creates a new UUID and a new session-owned branch.

Creation, start, attachment, rename, stop, destructive preflight, repair,
abandonment, and crash recovery are specified in
[session lifecycle](docs/session-lifecycle.md).

## Overview and status

The main UI is an fzf-shaped, filterable list of sessions across projects with
a preview pane and keyboard actions. It is a client of the same RPC surface
available to scripts; business logic does not live in the TUI.

Each row is derived from only three facts:

- authoritative runtime condition;
- live attachment count; and
- one nullable latest condition reported while the session was unattended.

Entering a previously unattended session clears that latest condition. While
the user is attached, semantic agent events are neither retained as status nor
notified. Leaving begins empty, and the next event becomes the latest. V1 has
no seen/unseen history, attention set, participant inventory, service
supervision, or terminal heuristic pretending to know agent intent.

Agent integrations translate native lifecycle hooks into the generic
`status.report` notification on the private session RPC socket. Claude Code,
Codex, and other tools remain ordinary interactive commands; no P-specific
agent wrapper is required.

See [session observability](docs/session-observability.md) for the reducer and
adapter rules.

## Git and RPC

### Hub and spoke — decided

Each P instance has one bare Git repository per project. Session runtimes and
ordinary host checkouts are spokes around that local hub. The session runtime
gets a scoped SSH key that can write only its UUID-assigned current branch,
fast-forward-only by default. Force-push is blocked unless trusted instance
policy grants a narrow exception.

The instance's dedicated host P key is read-only: it can clone and fetch all
user-visible branches, including retained unassigned branches, but cannot
mutate P refs. The daemon alone owns branch creation, rename, deletion, and
protected tracking namespaces. Host fetch and explicit publication against
origin use the user's normal OpenSSH credentials, not P session credentials.

The control API is newline-delimited JSON-RPC 2.0 over Unix sockets. Linux
clients support both direct local Unix connections and client-initiated
SSH-to-Unix bridging from day one. Attachment uses a separate validated argv
path; the daemon never initiates SSH and never returns a shell string.

Git carries commits, objects, and refs. RPC carries lifecycle, configuration,
runtime state, attachment leases, status, and subscriptions. The complete
division is in [communication boundaries](docs/communication-boundaries.md).

## Environments and isolation

Every session receives a minimal default substrate containing a shell, basic
userland, Git and SSH, CA certificates, and P's session helper. The project
environment is selected in V1 as:

1. the project's ordinary default Nix `devShell`, when present; or
2. the minimal substrate alone.

Nix is the first environment builder. A Dockerfile is the first planned
alternative and will be selected through trusted project configuration.
Builders consume immutable committed source inside isolated workers and emit
normalized artifacts; runtime backends consume those artifacts without
interpreting Nix or Dockerfile syntax. Cache behavior and build time remain
project-dependent and are measured during development rather than promised as
one universal threshold.

V1 external grants are project-scoped under `{project}/*`. The namespace
`{project}/{branch}` is reserved for later branch-specific grants. A session
created from any source receives only its project's trusted mounts, model
access, and network policy.

Repositories cannot configure P session behavior or request host mounts,
devices, credentials, published ports, the engine socket, the SSH agent, or
routes to the host and local network. Trusted host configuration may grant
named filesystem mounts, network posture, and model access through typed
isolation interfaces.

By default sessions may use outbound public internet but cannot reach the host,
LAN/private or link-local networks, metadata endpoints, gateways, or sibling
containers. P Git, the private session RPC socket, and explicitly granted model
inference are narrow service-boundary exceptions. P v1 does not declare,
supervise, health-check, or restart project services.

See [environment building](docs/environment-building.md) for providers,
artifacts, activation, and caching, and
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

V1 validates Bifrost's OpenAI-compatible surface with Codex, using OpenRouter
and a local model as initial upstreams. Anthropic-compatible access for Claude
Code follows in phase two. Bifrost remains suitable when a P instance moves to
Kubernetes; Envoy AI Gateway is a later option only if cluster-native shared
ingress, distributed policy, or inference-fleet routing justifies it.

See [model gateway](docs/model-gateway.md).

## Configuration

V1 has one P configuration authority: trusted host configuration keyed by the
complete project path. It supplies project-scoped session defaults and external
grants. There is no branch/session override and no custom P repository schema
or flake output.

The repository contributes only its ordinary pure Nix devShell. P uses the
default devShell, when present, to realize the session environment and
dependencies; otherwise it uses the minimal substrate. The default interactive
command is Bash through tmux. Registration never generates a project file.

SQLite stores mutable instance bookkeeping: project registrations, immutable
session UUIDs, project/branch assignments, backend locators, pending lifecycle
phases, runtime condition, latest unattended condition, credentials, and
reconciliation metadata. Git remains authoritative for code and refs; the
runtime backend remains authoritative for machinery; Bifrost remains
authoritative for gateway policy.

## Requirements

V1 targets Linux and expects:

- Git and OpenSSH;
- Nix;
- rootless Podman, or Docker after its conformance validation; and
- tmux for the default persistent interactive host.

The `direct` host does not require tmux.

### macOS and Windows

The daemon and execution backends remain Linux-only in V1. A user can run the
TUI on a Linux host over an ordinary SSH login, or use the Linux client's
day-one SSH-to-Unix transport from another Linux machine. Native macOS and
Windows client binaries are later packaging work built on the same transport
and RPC protocol; they do not introduce a remote-runtime backend.

## Non-goals

- P is not a multi-user scheduler or agent orchestration framework.
- P instances do not federate or replicate control-plane state.
- P does not migrate running containers, terminals, processes, conversations,
  or uncommitted files.
- P does not invent source commits or snapshot dirty host work.
- P does not automatically publish to origin.
- P does not give session runtimes origin credentials or general host/LAN
  access.
- P does not manage application services in V1.
- Checks and attempts are reserved future ideas, not V1 APIs.

## V1 scope

V1 closes the loop for one user on Linux:

- register Git projects and create sessions from committed sources;
- enforce immutable UUID-to-branch ownership and transactional rename;
- build Nix environments in isolation and run rootless local containers;
- enter a shell or agent through pluggable interactive hosting;
- use a thin TUI and complete RPC lifecycle surface locally or over
  SSH-to-Unix;
- report runtime condition, attachment presence, and the latest unattended
  agent condition;
- fetch from and explicitly publish to an optional origin;
- enforce project-scoped external grants and Git credentials;
- optionally provide model access through per-session Bifrost keys; and
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
