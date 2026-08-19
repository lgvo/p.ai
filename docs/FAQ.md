# P — FAQ

Explanations and tradeoffs behind P's design. This document is not the
authority for protocol or lifecycle contracts; each answer links to the
relevant design document when details matter.

## Product and scope

### What problem is P solving?

P makes concurrent development work visible and safe to leave running. It
organizes isolated runtimes around Git projects and branches, shows them in one
terminal UI, reports the latest agent condition received while the user is
away, and controls how committed work leaves the local instance.

It is primarily a bookkeeping and policy layer. Containers, tmux, agents, Git,
Nix, and Bifrost keep doing their own jobs.

### Who is P for?

V1 is for one developer operating Linux machines they control. It favors an
open-source workstation/laptop experience that can later expand to local VMs
or a P instance running in Kubernetes. Multi-user scheduling, a hosted control
plane, and instance federation are not V1 goals.

### Does one P instance manage another machine?

No. A daemon manages only runtimes belonging to its own instance. Remote use is
a client transport: the client initiates SSH and bridges the instance's Unix
socket or runs its validated attachment argv. The daemon never SSHes outward to
manage a remote container.

See [communication boundaries](communication-boundaries.md#ssh-roles).

### How do two machines share work?

Through the project's normal Git origin. Publish a session branch from one
instance, fetch it on the other, and create a new session there. The instances
do not exchange runtime or registry state. If the project has no origin, it is
local-only and has no P-managed cross-machine handoff.

Refresh and publication behavior is defined in
[communication boundaries](communication-boundaries.md#origin-communication).

### Why not federate P instances?

Git already solves the durable part of cross-machine work. Federation would add
identity, availability, conflict, discovery, and security problems while still
being unable to move processes, terminal state, or agent conversations. V1
keeps each failure domain local and uses a shared origin only when the user has
already chosen one.

### Why is there no real CLI?

There is a small launcher and scripting entrypoint, but not a second flag-heavy
product surface. The TUI and `p api` are clients of the same versioned RPC
methods, so lifecycle logic has one implementation. A thin command may open the
TUI, attach, run the daemon, or pass a structured API request; it does not mirror
every TUI interaction as a growing command tree.

This also makes alternate frontends possible without turning terminal key
bindings into an API. See [technology stack](technology-stack.md).

### Does P launch agents?

P launches the configured interactive command inside the session. That command
may be a shell, Claude Code, Codex, or custom argv. P does not orchestrate the
agent's conversation, tool loop, subagents, or task graph.

An agent may create worktrees, subprocesses, or subagents inside the sandbox.
Only the session-owned branch is P's handoff boundary, so the agent must
consolidate any result it wants to retain there.

### Does P manage development services?

No, not in V1. A user or agent may run a database, server, or watcher inside the
session, but P does not declare, supervise, health-check, restart, or model it.
The V1 readiness boundary stops at an available runtime, successful environment
activation, and an attachable interactive command.

## Sessions and branches

### What exactly is a session?

A session is an immutable P UUID that owns exactly one logical Git branch. Its
current human name is the actual branch ref name, and its runtime is the
replaceable execution machinery associated with the UUID.

This separates stable identity from mutable presentation without inventing a
second name unrelated to Git. The detailed invariant and rename transaction are
in [session lifecycle](session-lifecycle.md#identity-and-retained-state).

### How is a session branch identified?

By `(project path, branch name)`, just like a repository path plus branch on an
ordinary Git server. Two projects may both have `feature/parser`; Git prevents
duplicate branch refs within one project. The TUI and RPC therefore keep the
project and branch as separate structured fields instead of relying on a
globally unique branch name.

Ordinary source branches such as `main` are inputs to session creation, not the
new session-owned branch. Users can provide a different meaningful name or
accept a timestamp suggestion.

### Why is there no scratch session?

A separate scratch identity created a promotion protocol, different Git
permissions, and awkward rename semantics. It also made the runtime's durable
identity depend on whether the user had named an exploration yet.

Instead, every creation allocates a UUID and real branch immediately. A
timestamp is enough when the work has no good name yet, and a later rename
changes the Git ref without replacing the session.

### Why isn't `session.base_commit` stored?

The selected commit matters only while creating the branch. Once the branch
exists, Git already records its complete ancestry and current tip. Retaining a
second active base field would create an authority that could drift from Git.

### Can P start from `main`?

Yes. `main`, another branch, a tag, or an explicit reachable commit may be the
committed source. P creates a new session-owned branch at that commit; it never
turns the source ref itself into the session branch.

### Why doesn't my dirty checkout come along?

P needs a reproducible source that the local Git server, build worker, and
runtime can all identify. Automatically committing or quarantining a dirty tree
would make P an author of source history and introduce a second handoff path.
Commit the desired state first, then create the session from that commit.

### What does rename do?

It creates the new P ref at the old tip, updates the workspace branch and
upstream, updates SQLite, verifies the new mapping, and deletes the old P ref.
The UUID, runtime locator, interactive host, and credentials stay attached to
the same session. Origin refs are untouched; publishing the new name is a
separate choice. The transaction and crash recovery are defined in
[session lifecycle](session-lifecycle.md#rename).

### Can two sessions use the same branch?

Not within the same project on one P instance. An assigned project/branch has
exactly one UUID owner. Different projects may use the same branch name, and a
discarded branch has no UUID owner. It may seed a new session, but that session
owns a different newly created branch. Independent instances may each create
work from the same origin branch because they do not coordinate; ordinary Git
divergence rules apply when publishing.

### Can a session force-push?

Not by default. Its SSH principal can write only its assigned branch, and the
server accepts fast-forward updates. Trusted host policy may grant a narrow
rewrite exception for that branch; repository contents and the session itself
cannot enable it.

### What are stop, discard, and delete?

- Stop preserves the runtime and working copy in restartable form.
- Discard removes the runtime, credentials, and session record but retains the
  P branch as an ordinary unassigned source branch.
- Delete removes the runtime, session record, and session-owned P branch.

Stop preserves writable files and identity, not processes: starting again
reruns activation and interactive-host preparation.

P inspects runtime-local and Git-ref loss before destructive actions. If the
backend cannot be reached, ordinary discard/delete is refused because P cannot
truthfully itemize local loss. The explicit abandon path is stronger and leaves
an orphan record. See
[destructive preflight](session-lifecycle.md#destructive-preflight).

### How do I continue after discard?

Create a new session from the retained branch, with a new UUID and a new
session-owned branch name. The old runtime, uncommitted files, terminal state,
and agent conversation are not restored.

Discard therefore does not violate UUID↔branch ownership: it ends the
assignment before retaining the ref as an ordinary project branch.

### Does P clean up automatically?

No. Automatic reclamation risks deleting uncommitted work based on an age or
activity guess. P reports state and lets the user choose stop, discard, or
delete.

## Git and publication

### Why is the P Git server the source of truth?

Every development tool already understands commits, refs, fetch, and push. A
local Git server gives sessions a standard durable handoff while allowing P to
enforce per-session write authority. SQLite tracks lifecycle relationships; it
does not duplicate source objects or publication history.

### What can a session read and write?

It may read ordinary project branches and write only the branch assigned to its
UUID. It cannot write tags, another session branch, origin-tracking refs, or
reserved `refs/p/*` and `refs/attempts/*` namespaces. Private namespaces are
hidden and arbitrary object-ID fetches are disabled.

Ordinary project branches are not a confidentiality boundary; their history is
shared project source.

### What can the host P key do?

Only clone and fetch user-visible P branches. The host principal is read-only,
so it cannot bypass session lifecycle by editing refs directly. The daemon owns
P ref mutation.

The host's dealings with origin are separate and use the user's normal SSH key,
agent, and OpenSSH configuration. P never copies that credential into a
session.

### Why not let sessions push directly to origin?

Because public internet access should not imply publication authority. Sessions
receive neither an origin remote nor the user's origin credential. This keeps
publication a host-side, explicit decision and limits a compromised agent to
its local P branch.

### Does P synchronize with origin?

P refreshes when an origin is configured, when asked, before selecting current
origin source, before publication, and before making destructive-preservation
claims. Optional background refresh is only a convenience. P does not
automatically push, continuously mirror, retry an ambiguous publication, or
maintain a private “published once” flag. Git refs from a completed refresh
remain the authority. See
[origin communication](communication-boundaries.md#origin-communication).

### Can P force-publish to origin?

No. V1 creates an absent destination or fast-forwards one explicit origin
branch. Divergence is shown and refused. An intentional origin rewrite remains
an ordinary host Git operation followed by a P refresh; the P-branch rewrite
exception does not grant origin authority.

### What if there is no origin?

The project is local-only. P skips refresh, ahead/behind comparison,
publication, and upstream-survival claims. Those features are absent rather
than failed.

## Runtime, environment, and isolation

### Why containers first?

A rootless local container gives each session a separate workspace and process
boundary while remaining cheap enough for workstation use. A local VM can add
stronger isolation later, and Kubernetes can place runtimes for a P instance in
a cluster. The backend interface keeps those choices outside session and Git
identity.

### Is there an SSH-container backend?

No. SSH is a client transport, not a runtime provider. A remote machine runs
its own P instance; its daemon manages its own local backend.

### Why Nix first, and what about Dockerfiles?

Nix devShells provide reproducible toolchains and a useful closure/cache model
for the first implementation. They are not assumed to build quickly for every
project: cold, substituted, and warm behavior depends on the project and cache.

Environment building is a provider seam. An explicit Dockerfile is the first
planned alternative and emits a normalized OCI artifact against the same
runtime contract. See [environment building](environment-building.md).

### What happens when a project has no default devShell?

P uses an immutable minimal substrate with a shell, basic userland, Git and
SSH, CA certificates, and its session helper. It does not guess a language
toolchain or generate a config file merely to register the repository.

### Where do mounts and other host grants live?

Only in trusted host configuration keyed by the complete project path.
Repositories contain no P-specific configuration and cannot request host
paths, credentials, devices, published ports, local-network routes, or
engine/SSH-agent access.

V1 grants are project-scoped under `{project}/*`. A future
`{project}/{branch}` scope is reserved but not implemented, so every new
session—including one created from an exploratory commit—receives only its
project's policy.

Mount normalization, fixed targets, lifetime, and cleanup are defined in
[runtime and isolation](runtime-isolation.md#filesystem-mounts).

### What network access does a session have?

The default `public-egress` profile allows outbound public internet for packages
and documentation. Routes to the host, private/LAN and link-local networks,
metadata endpoints, gateways, and sibling containers are blocked. P Git, the
private session socket, and a granted Bifrost inference route are narrow
exceptions at explicit service boundaries. A project may instead select the
`none` profile.

Network isolation is one of the real-machine validations tracked in
[development validations](development-validations.md).
The normative profiles and service exceptions are in
[runtime and isolation](runtime-isolation.md#network-contract).

## Status and attachment

### How does P know an agent needs attention?

An adapter maps the agent's native hooks into a generic `status.report` event.
While nobody is attached, each valid event replaces one
`latest_unattended_condition`. It may say running, attention, idle, failed, or
unknown and include short source/reason metadata.

This is intentionally the latest report, not a proof of the agent's complete
internal state. See [session observability](session-observability.md).

### What happens when I enter a session?

The first live attachment clears the stored unattended condition. While any
attachment remains, semantic agent events are not retained as overview status
and do not notify: the user is already inside the session. When the last
attachment leaves, P starts empty and tracks the next event.

### Why no seen/unseen state or attention history?

V1 only needs to surface the latest thing reported while the user was away.
Observation cursors, causal attention IDs, multi-participant reduction, and
resolution history add machinery without making ambiguous agent hooks
authoritative. Entering already provides a simple useful acknowledgement.

### What if an agent has no adapter?

P still reports authoritative runtime condition and attachment presence. The
semantic condition remains empty rather than being guessed from process names,
terminal output, or tmux panes.

### Why is tmux replaceable?

P needs a safe way to start and attach to one interactive argv, not tmux's
grouping model. Tmux is a good default because it preserves the interactive
program across detach, but the direct host demonstrates that session identity,
Git, readiness, and status do not depend on it.

## API and credentials

### What travels over Git and what travels over RPC?

Git carries source objects, commits, and refs. RPC carries project/session
lifecycle, configuration results, runtime state, attachments, status, and
subscriptions. Model inference has its own Bifrost HTTP route. Terminal bytes
use the attachment path.

The full channel table is in
[communication boundaries](communication-boundaries.md#the-rule).

### Why Unix sockets instead of a network API?

The control plane is instance-local. Unix permissions and peer identity protect
local access; a remote client delegates authentication and encryption to SSH
while bridging the same socket. P therefore does not need to expose control RPC
on TCP or invent another remote authentication scheme in V1.

### What can a session ask P to do?

Its private socket can report status and query its own UUID, current branch,
and effective capabilities. It cannot list other sessions, publish, control
lifecycle, change grants, or access the host control socket. Runtime placement
is its identity; it cannot claim another UUID in a payload.

### Which SSH credentials exist?

- each session has a P-Git key restricted to its UUID-owned branch;
- the instance has a read-only host P-Git key; and
- host-origin operations use the user's existing SSH credentials.

The three paths are deliberately separate. Neither host key enters a runtime.

## Model gateway

### Why Bifrost instead of passing provider keys into sessions?

Bifrost already supplies protocol compatibility, provider connections, aliases,
routing, virtual keys, limits, usage, MCP, and skills. A per-session virtual key
gives revocation and attribution without exposing an OpenRouter, OpenAI, or
local-provider secret to the runtime.

P persists that key with the session UUID and manages its lifecycle, but
Bifrost owns configuration and policy. P does not become an inference proxy.

### Why OpenAI-compatible first?

It provides the initial Codex path while keeping the integration small.
Bifrost—not P—implements the API surface. Anthropic-compatible access and
Claude Code validation follow in phase two; until then Claude Code may use its
own in-session subscription login or an explicitly trusted secret-bearing
filesystem grant.

### Can sessions use OpenRouter or local models?

Yes, when Bifrost is configured for them. OpenRouter is the initial hosted
upstream, and Ollama/vLLM-like endpoints can be exposed as permitted aliases.
The session sees only the catalog allowed by its virtual key and cannot select
an arbitrary host/LAN URL.

### Are Bifrost Skills and MCP part of V1 inference?

No. They are promising adjacent capabilities, but inference, skill serving, and
MCP tool access require separate routes and grants. Bifrost Agent Mode and Code
Mode are not required for ordinary agent clients and remain later evaluations.

### Do we need Envoy AI Gateway for Kubernetes?

Not initially. Bifrost can run as the gateway inside Kubernetes. Envoy becomes
interesting if a deployment needs Kubernetes-native shared ingress, workload
identity, distributed quotas, or inference-pool routing. The staged comparison
is in [model gateway](model-gateway.md#kubernetes-evolution).

## What remains intentionally later?

Native macOS/Windows clients, Dockerfile environments, local VMs, Kubernetes
runtimes, branch-specific grants, checks, attempts, richer MCP/skills
integration, and multi-user operation are later capabilities. The uncertain
parts of already-scoped development are listed separately in
[development validations](development-validations.md), so one spike does not
silently block unrelated implementation.
