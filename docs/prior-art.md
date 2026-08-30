# P — prior art and positioning

How P relates to tools that already exist. This document has two jobs: keep the
design honest about what is commodity, and direct effort toward the combination
that is actually specific to P.

> **Landscape snapshot: 2026-08-20.** Individual products in this area change
> quickly. Consequential claims link to first-party material; status labels are
> dated so staleness is visible rather than implicit. This document is
> non-normative; P's design documents define product behavior.

## Scope and reading frame

P is local-first, open-source tooling intended to run on machines the user
controls and to expand through replaceable, instance-local execution
mechanisms. Managed vendor platforms matter as strategic context, but they are
not the primary comparison set. V1 selects a confined local Incus project as
its sole runtime substrate; a future Incus VM or Kubernetes provider could
place work for that same instance. An SSH host or a second P daemon is not a
backend. The user selects another P instance by
connecting its TUI to that instance, and v1 moves work between instances only
through the project's ordinary shared `origin` when one is configured. Without
one, the project is intentionally local-only.

The comparison uses three verbs:

- **Knows:** inventories live work, reports state, surfaces blocked agents, and
  supports fast attachment.
- **Places:** creates an isolated runtime on a selected backend.
- **Governs:** constrains what the runtime may write and, when a shared origin
  exists, requires a separate host-authorized action before work reaches it.

“Governance” here means enforced authority, not merely a diff viewer, merge
button, CI result, or forge branch-protection rule.

## P's organizing model

The model matters because several tools have a similar screen while organizing
fundamentally different things:

- A **project** is a Git repository.
- A **branch** is a durable piece of work and the state that crosses machines.
- A **session** is an immutable UUID that owns exactly one logical Git branch;
  the mutable session name is that branch's real ref name, addressed together
  with the complete project repository path.
- A **runtime** is one unprivileged Incus system container tagged to the
  session UUID in V1. The configured interactive command may be a shell or
  agent; tmux is the default persistent host, not session identity.
- Every session starts from committed source and owns a real branch
  immediately. A timestamp provides an initial name when the user has no better
  one, and transactional rename changes the Git ref without replacing the UUID
  or runtime.
- One P instance permits at most one session owner for a session branch and at
  most one Incus instance for that session UUID.
- A small instance-local SQLite registry records the UUID-to-branch and Incus
  relationships, project/branch assignments, cross-authority lifecycle intent,
  latest unattended condition, and credentials. Incus metadata supports
  discovery and repair; Incus remains authoritative for its instances, images,
  storage, and operations.
- P instances do not discover, address, or synchronize with one another. Each
  has a local Git server. Independent machines converge only through a
  configured ordinary shared `origin`: publish from one instance, fetch on the
  other, then create a fresh runtime. Origin-less projects remain local to
  their instance.
- Runtime state does not travel. Incus roots, tmux, processes,
  conversations, and uncommitted files remain instance-local.

This makes the overview a list of **active sessions across Git projects**,
not a branch browser, task database, process list, or federation of P servers.

## Interface prior art

### Agent Deck — the closest surface, not the same model

**[Agent Deck](https://github.com/asheshgoplani/agent-deck)** remains the
closest reference for the intended terminal experience. Its current scope is
substantially broader than the old “tmux picker” description: groups and global
search, agent-state detection, notifications, attachment and recovery, Git
worktrees, comprehensive session forks, optional Docker sandboxes, a web UI,
cost tracking, a phone-controlled conductor, and SSH-managed instances.

That similarity should guide interaction design, not architecture. Agent Deck's
primary identity and grouping remain registered sessions and groups; its
worktrees, Docker mode, remote-instance aggregation, and finish flow are
features around those sessions. P's durable identity is a session UUID with a
one-to-one Git branch association, organized under projects, with an
instance-local runtime and a policy-enforcing Git server between that runtime
and `origin`. P should study Agent Deck's
density, navigation, fork/recovery affordances, and intervention flow without
inheriting its grouping, federation, credential-sharing, or Git-authority
model.

Its recent [release history](https://github.com/asheshgoplani/agent-deck/releases)
also contains two operational lessons P should adopt directly: session/project
identity must not be silently rebound after creation, and tmux fleet operations
must target an isolated server identity rather than broad process-name or argv
matching. Those lessons reinforce P's immutable session UUID and the default
tmux host's UUID-specific server identity; P core still remains independent of
tmux topology.

### Other local and source-available control surfaces

- **[claude-squad](https://github.com/smtg-ai/claude-squad)** demonstrates the
  basic Go-TUI-over-tmux shape with agent-agnostic worktree sessions. It is
  strong evidence that fast overview and attach are table stakes.
- **[Nimbalyst](https://github.com/Nimbalyst/nimbalyst)**, the active successor
  to Crystal, adds visual review and mobile notification/response. The old
  Crystal-only description is stale; it should not be used to support a claim
  that worktree managers lack an attention story.
- **[Conductor](https://conductor.build)** has expanded beyond its original Mac
  worktree application into managed cloud sandboxes, notifications, API access,
  and mobile control. It is significant product/interface evidence, but not an
  open-source, user-controlled substitute for P.
- **[Superset](https://github.com/superset-sh/superset)** combines parallel
  worktree agents, cross-workspace monitoring, attention notifications, diff
  review, terminals, previews, automation, remote hosts, and SDK/MCP control.
  Its repository is under Elastic License 2.0, so it is source-available rather
  than a drop-in answer to P's open-source goal.
- **[T3 Code](https://github.com/pingdotgg/t3code)** drives several installed
  agent CLIs through a local server and desktop/web/mobile clients. It is good
  prior art for agent-agnostic remote control. Driving already-authenticated
  host CLIs is not by itself the same as P's session credential boundary.
- **[cmux](https://github.com/manaflow-ai/cmux)** is useful interface evidence:
  agent-aware panes, attention rings, a notification center, branch/PR metadata,
  and SSH workspaces in a terminal application.
- **[dmux](https://github.com/standardagents/dmux)** is a small MIT-licensed
  terminal manager that creates Git worktrees and opens coding agents in tmux
  panes, with merge/PR completion flows. It is useful evidence for keeping
  creation, navigation, attachment, and cleanup terse; its organizing unit and
  Git authority remain ordinary worktree sessions.
- **[Hive](https://github.com/anomalyco/hive)** is an MIT-licensed,
  worktree-first desktop workspace for Claude Code, OpenCode, and Codex, with
  cross-project visibility, visual diffs, and cross-worktree context. It is
  relevant interface and review prior art, while P's branch-tagged runtime and
  server-enforced Git authority remain different.
- **[ADHDev](https://adhf.dev/)** attaches a browser or phone to local CLI and
  IDE agents, surfaces diffs and approval requests, and can dispatch work across
  isolated worktrees on user-owned machines. Its peer-to-peer remote-control
  and mobile attention flows are relevant client prior art; its repo-mesh and
  automatic convergence model are intentionally broader than independent P
  instances joined only by ordinary Git.

The lesson is direct: a multi-agent overview, notifications, and one-keystroke
attachment are no longer differentiators. P needs them to be excellent and
trustworthy, but should build them plainly.

## Repository and workspace mechanisms

### container-use

**[container-use](https://github.com/dagger/container-use)** creates a fresh
container and Git branch per environment. It records command/file history,
shows live logs, permits terminal intervention, resolves and redacts secrets,
and offers explicit merge, staged-apply, or discard outcomes. See its
[environment workflow](https://container-use.com/environment-workflow) and
[secret handling](https://container-use.com/secrets).

This is meaningful overlap with P's placement mechanism and human acceptance
boundary. It should not be summarized as “an agent auto-commits directly into
your repository.” The architectural difference is authority and organizing
layer: container-use exposes environment lifecycle through MCP/CLI, while P's
human-owned control plane organizes project branches, assigns a single runtime,
enforces server-side ref scope, and supplies the runtime no configured or
credentialed publication path to `origin`.

### Clawker

**[Clawker](https://clawker.dev/)** is a self-hosted local Docker sandbox for
coding agents. It builds project images, starts and attaches to per-agent
containers, and applies a deny-by-default egress firewall. Its
[container model](https://docs.clawker.dev/container-internals) is strong prior
art for local image/runtime ergonomics and network containment.

Clawker intentionally forwards selected Git and agent-harness credentials into
the container. P's default is different: no configured or credentialed origin
path inside the runtime, a separate ref-scoped principal for the local P Git
server, and host-side publication only when the project has an origin. Clawker
therefore belongs in the primary security/execution comparison, but it is not
the project→branch→runtime control plane or complete-session overview P is
designing.

### Worktree-oriented tools

Worktree managers such as claude-squad, Nimbalyst, Superset, Conductor, and
Sculptor provide cheap parallel file trees and increasingly sophisticated
review and notification surfaces. Their modes differ, so “all are single
machine” or “none notify” is no longer accurate.

The durable distinction is narrower: a worktree alone isolates files, not
processes, ports, installed packages, credentials, or the rest of the host.
Some products add remote or managed sandboxes, at which point they should be
compared mode-by-mode rather than dismissed by category.

## Self-hosted execution and control planes

### Handler

**[Handler](https://handler.dev/)** is the strongest newly identified
open-source local execution/control-plane comparator. It is MIT-licensed and
self-hosted, supports multiple terminal agents, gives each agent a persistent
tmux-backed Docker container or Firecracker VM, and combines a visual command
centre, live terminals and metrics, worktree/snapshot forking, Git integration,
custom Dockerfiles, MCP configuration, and port forwarding. Linux receives the
full Docker-plus-Firecracker feature set; macOS uses Docker.

Handler validates much of P's desired product surface and local-isolation
direction. The remaining distinction is structural: Handler organizes
sandboxes and agent configurations, whereas P organizes durable project
branches, permits one runtime for a branch per instance, and enforces a
runtime-specific Git write boundary with no `origin` credentials inside the
runtime. P should treat Handler as a primary comparator, not merely backend
inspiration.

### Shep

**[Shep](https://shep.bot/)** is a local-first, MIT-licensed orchestrator that
maps features to branches, worktrees, and agent sessions; records its state in
SQLite; and drives the workflow through commits, pushes, draft pull requests,
CI observation, agent retries, review, and merge. It is important evidence that
project/branch-oriented orchestration and a local registry are not by
themselves differentiators.

Shep deliberately automates the forge workflow using the user's ordinary Git
authority. P instead makes the runtime's Git authority the central enforced
boundary: the runtime can update only its permitted local refs, has no
P-supplied configured or credentialed publication path to `origin`, and
requires a separate host-authorized publication action. The public forge may
still be network-reachable through ordinary internet egress. Worktrees also do
not provide P's process, credential, or network isolation.

### Omnara

**[Omnara](https://github.com/omnara-ai/omnara)** is no longer accurately
described as an attention-only SaaS relay. As of this review it presents an
Apache-2.0 open-source platform with durable agent state, sandbox providers,
user-owned machines and VMs, projects and RBAC, cloud or Docker deployment,
models/tools/skills/MCP, dashboard, Slack, and API interaction.

It is now a serious self-hostable execution/state control-plane comparator.
Its durable unit is managed agent state and its machine abstraction; P's is a
Git project/branch with a disposable runtime and an origin publication path.
That difference in authority and grouping is more useful than pretending
Omnara only solves notifications.

### Coder Agents

**[Coder Agents](https://coder.com/docs/ai-coder/agents)** runs the agent loop in
the control plane, provisions isolated user workspaces, keeps model credentials
out of those workspaces, can restrict egress, attaches user identity, and
enforces permissions server-side. It is enterprise-oriented and vertically
integrated rather than a TUI peer, but it is important evidence that credential
mediation and server-side agent authority are not unique ideas. Older
[Coder Tasks](https://coder.com/docs/ai-coder/tasks) is moving to extended
support and should not be presented as Coder's long-term model.

### AgentsMesh

**[AgentsMesh](https://github.com/AgentsMesh/AgentsMesh)** runs agent pods with
dedicated Git worktrees and branches across self-hosted runners, then exposes
them through web, desktop, and iOS clients. Its scheduling, terminal streaming,
attention, and multi-machine control-plane/data-plane split are relevant scale
references.

It is the opposite topology from P v1: runners join a federated fleet, pods can
collaborate through platform channels, and a central service schedules across
machines. P instances never communicate; projects that cross machines share
only an ordinary upstream Git repository. AgentsMesh is also BSL-1.1 with
production use requiring a
commercial license until its stated 2030 change date, so it is source-available
rather than an open-source dependency candidate for P today.

## Execution-provider references

### Incus — selected V1 substrate

**[Incus](https://linuxcontainers.org/incus/docs/main/)** supplies the runtime
mechanics P should reuse: system containers and VMs, images, storage pools,
projects, operations/events, exec, and local API access. Its
[confined user projects](https://linuxcontainers.org/incus/docs/main/howto/projects_confine/)
are especially relevant because they let the machine owner define an upper
bound on the instances, devices, paths, and networks P may control. Incus also
documents that unrestricted administrative access is
[host-root-equivalent](https://linuxcontainers.org/incus/docs/main/explanation/security/),
which is why P must use only the confined user project/socket.

Incus replaces V1 engine/image/storage lifecycle implementation; it does not
replace P's Git project/branch identity, per-session authorization, lifecycle
intent, status, TUI, or origin publication. P uses local Incus only in V1:
remote servers and clusters do not create daemon federation.

The following are design references for future isolation providers. They are
not automatically P backends: using one must preserve the rule that a P daemon
manages only its own instance and never turns a remote host into a hidden second
instance.

- [Daytona](https://github.com/daytonaio/daytona)
- [E2B](https://github.com/e2b-dev/E2B)
- [Fly Sprites](https://fly.io/sprites/)
- [exe.dev](https://exe.dev/docs/what-is-exe)
- [Vercel Sandbox](https://vercel.com/docs/sandbox)
- Incus VMs, Kubernetes, and—only if later justified—raw Podman/Docker
  runtimes

P should evaluate a provider by provisioning and teardown, attach/exec,
networking, storage and snapshots, observability, secret mediation, egress
policy, native image building, and whether it can reach this P instance's Git
and status endpoints without gaining host/LAN access. Adopting one must not
change project/branch identity, origin publication, instance boundaries, or the
sessions-only overview. A managed sandbox API may be useful implementation
research without being eligible under those constraints.

## Model-gateway references

Model routing and protocol compatibility are commodity infrastructure, not a P
differentiator:

- **[Bifrost](https://github.com/maximhq/bifrost)** is the selected first
  local and Kubernetes-capable implementation. Its provider integrations,
  virtual keys, aliases, routing, streaming, limits, accounting, dashboard,
  Skills Repository, and MCP gateway cover substantially more than P's small
  runtime-principal lifecycle seam. P uses only filtered session-facing
  surfaces and keeps Bifrost administration host/control-plane-only. Agent and
  Code Mode are execution features layered on MCP, not requirements for using
  it; interactive coding harnesses retain their own loop initially.
- **[LiteLLM](https://docs.litellm.ai/)** is the compatibility fallback and a
  useful reference, but brings a larger Python service footprint.
- **[Envoy AI Gateway](https://aigateway.envoyproxy.io/)** overlaps in provider
  translation, routing, limits, telemetry, and MCP aggregation, but its center
  of gravity is Envoy/Gateway API infrastructure: horizontally scaled ingress,
  cluster identity and authorization, distributed traffic policy, and
  InferencePool endpoint selection for self-hosted model fleets. It is a
  natural optional evolution for those requirements, not a requirement for a
  P instance merely because that instance runs in Kubernetes. Bifrost may run
  there first, sit behind Envoy later, or be replaced only for the inference
  data plane while remaining an optional skills service.
- **[Portkey](https://portkey.ai/docs/product/ai-gateway)** is relevant for
  optional managed/hybrid deployments, not the local v1 default.

P exposes Bifrost's OpenAI-compatible interface first and adds its
Anthropic-compatible interface in a second phase. Bifrost owns both APIs; P
owns only gateway-principal lifecycle, trusted model grants, attribution, and
which independently filtered skills/MCP surfaces a runtime may reach. The
[full gateway design](model-gateway.md) records the Bifrost capability map and
the staged Bifrost-first/optional-Envoy Kubernetes shapes.

## Vendor platforms — strategic context

Managed platforms have made parallel isolated tasks, attention surfaces,
review, and Git handoff commodity:

- [GitHub Copilot cloud agent / Agent HQ](https://docs.github.com/en/copilot/concepts/agents/cloud-agent/about-cloud-agent)
  uses ephemeral environments, limited writable Git authority, signed commits,
  firewall controls, deferred workflows, and human PR review. Its
  [risk documentation](https://docs.github.com/en/enterprise-cloud@latest/copilot/concepts/agents/cloud-agent/risks-and-mitigations)
  is particularly relevant to P's governance claims.
- [OpenAI Codex cloud](https://developers.openai.com/codex/cloud/) and the
  [Codex app](https://developers.openai.com/codex/app/features/) provide
  parallel isolated tasks, project/task management, review, scheduling, and
  remote control.
- [Google Jules](https://jules.google/docs/tasks-repos) runs parallel tasks in
  short-lived VMs with notifications, plan approval, live diffs, and explicit
  [branch/PR publication](https://jules.google/docs/code/).
- [Cursor Background Agents](https://docs.cursor.com/background-agent) run in
  isolated remote VMs with status, follow-ups, takeover, branches, and GitHub
  pushes.
- [Ona Agent](https://ona.com/docs/ona/agents/overview) offers isolated
  environments, customer runners, command policy, audit logging, long-running
  tasks, and PR automation.

P should not compete with this category on the number of agent features. Its
reason to exist is user-owned, agent-agnostic infrastructure with Git authority
that remains understandable without adopting a vendor's agent platform.

## Early watchlist and historical entries

These projects are relevant but should not carry the central positioning claim:

- **[Harness](https://github.com/majiayu000/harness):** early Claude/Codex fleet
  orchestration with execution policy, network allowlists, isolated worktrees,
  review, GitHub automation, and telemetry. Its policy layer is not P's ref
  authorization, and its container mode has different credential choices.
- **[Ephemeral Sandbox](https://github.com/Ephemeral-AI-Lab/ephemeral-sandbox):**
  early isolated workspace sessions with provenance, observability, and
  all-or-nothing publication of a resolved change set.
- **[Celln](https://github.com/sympozium-ai/celln):** pre-alpha, single-host x86
  KVM cells with per-invocation authority, read-only tools, and no ambient
  network. It is adjacent execution-sandbox research, not a durable work
  control plane.
- **[Sculptor](https://github.com/imbue-ai/sculptor):** experimental desktop
  parallel-agent worktrees, review/PR flow, and container/remote backend work.
- **[ccmux](https://github.com/epilande/ccmux):** tmux agent picker with
  actionable notifications, worktrees, previews, and handoff.
- **[VibePod](https://github.com/VibePod/vibepod-cli):** local Podman/Docker
  agent containers with traffic and usage analytics.
- **[GitButler](https://github.com/gitbutlerapp/gitbutler):** adjacent Git
  history tooling for parallel/stacked branches, undo, and forge integration;
  not an execution control plane.

Historical or currently unverifiable:

- **[Sketch](https://sketch.dev/):** discontinued; the site points to exe.dev.
  Keep it as historical evidence of container-per-agent plus Git output, not as
  an active product row.
- **[vibe-kanban](https://www.vibekanban.com/blog/shutdown):** Bloop shut down
  in April 2026; the local open-source project was left to the community while
  hosted/remote services were removed.
- **Catnip:** the former `wandb/catnip` repository returned 404 during the
  2026-08-13 review. Cached descriptions are insufficient to assert its current
  status, so it is excluded from the active comparison.

## Comparison

This table records architecture, not feature-count scores. “Hard Git boundary”
means the runtime's write authority is enforced outside the runtime;
“publication gate” describes the separate authority used when work is sent to
a shared external repository. It does not claim proof of human presence.

| System | Organizing unit | Runtime placement / isolation | Attention surface | Hard Git boundary | Publication gate | Deployment |
|---|---|---|---|---|---|---|
| Agent Deck | registered session/group | local/SSH-managed tmux; worktree + optional Docker modes | TUI/web/phone + notifications | no P-like ref server; Docker can share host tool auth | ordinary user Git/worktree finish | local, open source |
| dmux | worktree/agent pane | local worktree + tmux | terminal panes | no | merge/PR flow | local, MIT |
| Hive | project/worktree workspace | local worktree | desktop workspace + diffs/context | no P-like ref server | ordinary user Git/review | local, MIT |
| claude-squad | worktree/agent session | local worktree | TUI | no | ordinary user Git | local, open source |
| Nimbalyst | worktree/task | local workspace; product-specific remote modes | desktop/mobile review + response | no P-like ref server | review flow | local app + companion mobile service |
| Superset | workspace/worktree | local + remote hosts/workspaces | rich app + notifications | no P-like ref server | review flow | self-hosted/source-available (ELv2) |
| Handler | sandbox/agent | local Docker or Firecracker VM; worktree/snapshot forks | visual command centre + persistent terminal | sandbox boundary, not P ref authorization | human-directed Git workflow | local, MIT |
| Shep | feature → branch/worktree/session | local worktree | local web dashboard + CLI | no; uses ordinary user/agent Git authority | PR review by default; optional auto-merge | local, MIT |
| container-use | environment + Git branch | container per environment | logs + terminal intervention | environment/history boundary, not P topology | merge/apply/discard | open source/self-hosted |
| Clawker | project/agent sandbox | local Docker container | CLI attach/management | container/network boundary; forwards selected Git credentials | ordinary user/agent Git | local, open source |
| Omnara | managed agent + durable state | provider sandbox or user machine/VM | dashboard/Slack/API | control-plane permissions, different model | workflow-dependent | Apache-2.0; cloud or Docker |
| Coder Agents | agent + user workspace | provisioned isolated workspace | web/control-plane UI | server identity and permission policy | workflow-dependent | self-hosted/enterprise |
| AgentsMesh | pod/runner/ticket | worktree pod scheduled across a runner fleet | web/desktop/iOS console | platform credentials/permissions, different model | workflow-dependent | self-hostable, BSL-1.1 |
| Vendor cloud agents | task/agent | managed container/VM | web/app/mobile varies | often restricted agent authority | usually branch/PR review | managed service |
| **P (design)** | **Git project → session UUID ↔ branch** | **one unprivileged system container in a confined local Incus project; cached Nix image + private root** | **sessions-only TUI + latest-unattended status + notifications** | **session-scoped local Git server; assigned branch only; read-only host principal; no supplied origin authority** | **explicit host push every time when an origin exists; bypassed when local-only** | **open source, user-controlled** |

## What P specifically combines

No individual mechanism below is novel. The defensible claim is the exact
combination:

1. Git repository projects and durable branches as the organizing model, with
   a sessions-only operational view over disposable runtimes.
2. One immutable session UUID per logical branch, with one confined local Incus
   instance and a replaceable interactive host; an unprivileged system
   container and tmux are the V1 defaults for those separate roles.
3. A local Git hub that authenticates each session and restricts it to its
   assigned branch while giving the host only read access.
4. No `origin` credentials or path inside the runtime; when an origin is
   configured, every update to it is an explicit host-side publication.
5. Independent P instances that never federate and use a configured ordinary
   origin as their only v1 convergence mechanism; origin-less projects remain
   local-only.
6. Agent-agnostic latest-unattended status and credential mediation around a
   runtime in which the human or agent otherwise remains sovereign.

This is deliberately narrower than “nobody else governs agents.” GitHub and
Coder enforce agent authority; container-use and Jules have human acceptance
boundaries; several systems mediate credentials. The P-specific claim is how
repository/branch identity, runtime placement, local ref authorization, and
origin publication compose in a self-hosted tool.

## Implications for effort

- **Reuse Agent Deck-class interface lessons.** Dense status, fast search,
  attachment, recovery, and notifications are solved interaction problems. P's
  screen should feel familiar even though its grouping model differs. Runtime
  identity must remain immutable, and tmux cleanup must address one isolated
  server/runtime identity rather than matching process names or argv globally.
- **Reuse Incus instead of rebuilding runtime mechanics.** Confined local Incus
  is V1; Incus VMs or a later Kubernetes backend can implement the retained
  runtime seam. SSH selects another P instance—it never makes that host a
  backend of the current daemon. Third-party sandbox APIs remain research until
  they can satisfy the instance, Git, network, and credential boundaries.
- **Spend design and review effort on the authority boundary.** Immutable
  session identity, transactional branch rename, project/branch assignment
  uniqueness, Git hooks, origin refresh semantics, explicit publication, and
  destructive lifecycle behavior are where P can fail dangerously.
- **Keep status integrations small and agent-agnostic.** Vendor and open-source
  dashboards will keep improving. P should consume hooks/protocol events rather
  than wrap or orchestrate agents.
- **Avoid universal market claims.** “Everyone auto-pushes,” “nobody enforces
  Git policy,” “worktree tools never notify,” and “credential mediation is
  unique” are all already false.
