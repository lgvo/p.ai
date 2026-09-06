# P — product landscape and design space

- Status: supporting product research, not an authoritative product decision
- Research cutoff: 2026-09-02
- Decision owner: the governing user through [`PRODUCT.md`](PRODUCT.md)
- Coverage: product positions, prior-art inventory, differentiation, and validation wedges
- Supersedes: the prior-art inventory dated 2026-08-20

## Conclusions

### Recommended product position

P should combine two central forms of value: making concurrent agent-driven
development streams governable and letting developers reshape P's workflows
through plugins authored by developers or agents. It should not become another
generic multi-agent dashboard, worktree manager, sandbox service, Git workflow
tool, or extension marketplace.

The research found delegated authority to be one of the least-converged parts
of the landscape, but the governing user did not adopt “authority-centric
control plane” as P's central product identity. Authority enforcement is an
essential property that makes concurrency and adaptation trustworthy.

The adopted product proposition in [`PRODUCT.md`](PRODUCT.md) is:

> P is an open-source, self-contained, extensible control plane for an
> individual developer's concurrent agent-driven development. It makes those
> streams governable and lets the developer reshape P's workflows through
> plugins while preserving familiar tools, independent operation, and secure
> developer-controlled activation.

This section records the governing user's decision after reviewing the
research; [`PRODUCT.md`](PRODUCT.md) remains authoritative.

### What P can provide that is different

No individual mechanism is unique. The potential difference is the composition
of two user-facing capabilities—governable concurrent work and agent-driven
workflow adaptation—with boundaries that keep both trustworthy:

1. Durable Git project, session, and branch identity rather than disposable
   chat or process identity.
2. A cross-project view of concurrent streams, their attention needs, and their
   available lifecycle actions.
3. Agent-authored workflow plugins using supported contracts rather than core
   modifications or private forks.
4. Extensions that remain inert until developer activation and cannot approve
   or expand their own grants.
5. Heterogeneous and replaceable agents rather than ownership of the agent loop.
6. Confined execution rather than treating a worktree as a security boundary.
7. An honest authority view that distinguishes requested, effective, enforced,
   cooperative, and uncovered access.
8. Session-scoped local Git authority, no P-supplied origin credentials inside
   the runtime, and explicit host-side publication.
9. A complete open-source core whose lifecycle does not require another P
   instance or mandatory P-hosted service.

No mature reviewed product clearly documents this complete combination. That
is a material research null result, not proof of global uniqueness or demand.
Handler overlaps on local agent-neutral isolation; Agentplane overlaps on
Git-native authority and evidence; vendor platforms can enforce strong
boundaries inside their own stacks; T3 Code, Cline Kanban, Agent Deck, and
Conductor overlap on cockpit UX.

### Recommended validation sequence

Validation progresses from personal use to a reproducible public experience
and only then to broader evidence:

```text
1. Personal MVP and sustained dogfooding
   Use P for real concurrent work and let that use expose wrong assumptions.
        |
        v
2. First coherent public version
   Center the experience on governable concurrent branch sessions.
   Use an agent and P's supported plugin path to change a real workflow.
   Turn the working path into documentation another developer can follow.
        |
        v
3. External reproduction and continued use
   Observe whether others can repeat, understand, retain, and adapt the workflow.
        |
        v
4. Broader plugin and workflow evidence
   Compare further adaptations only after real use identifies the pressure.
```

The first public version and plugin guide belong to one coherent product proof:
the concurrent-session experience is the entry point, while the guide makes
agent-driven adaptation visible, testable, and reproducible. The exact workflow
change remains deliberately unresolved until personal use supplies a real need.
Successful completion proves viability and documentability, not external demand
or long-term maintainability.

Publication authority remains part of the concurrent-session design rather
than a separate later feature.

### What is not recommended

- Do not lead with a generic worktree/Kanban cockpit; that category is crowded.
- Do not reposition P as a sandbox wrapper; isolation is necessary machinery,
  not the complete user job.
- Do not make PR review, model routing, MCP connectivity, or marketplace supply
  the product thesis; all have strong substitutes.
- Do not adopt Jujutsu merely because it retains conflicted states. It does not
  detect semantic contradiction and would reopen P's settled Git model.
- Do not make eBPF a product moat. It is Linux-specific enforcement and
  observability machinery with incomplete coverage in current agent-oriented
  implementations.
- Do not prescribe WASM, MCP, OAuth, eBPF, or a trace substrate before the
  required authority semantics identify a concrete need.

### Confidence and the decisive unknown

Confidence is:

- **High** that parallel sessions, worktrees, cockpit UX, diff/PR review,
  multi-vendor launching, sandboxes, and basic extension mechanisms are crowded
  or commoditizing.
- **Medium-high** that P's composition of governable streams, agent-driven
  adaptation, and explicit authority is less served than those categories.
- **Medium** that governed extension admission is a coherent companion
  differential.
- **Low-to-medium** that enough individual developers currently experience this
  problem frequently enough to adopt a persistent governor.

The decisive competitor remains the status quo: Git, terminals, worktrees,
native agent permissions, scripts, hooks, CI, and manual publication. The next
evidence should measure repeated behavior, bypasses, comprehension, and
retention—not feature interest or successful demos.

## Why these conclusions follow

The reasoning runs backward from the recommendation to five landscape findings:

| Landscape finding | Consequence for P |
| --- | --- |
| Dashboards, parallel sessions, worktrees, diffs, notifications, resume, and PR buttons appear across vendor-native and independent products | These are required interface capabilities, not a defensible position |
| Containers, OS sandboxes, and managed microVMs are established execution infrastructure | P should consume and constrain an execution substrate rather than become a sandbox company |
| Multi-agent launch surfaces and ACP-like protocols are reducing adapter friction | “Supports several agents” is not a durable moat; common governance across them may be |
| Permission prompts, agent rules, runtime confinement, credentials, Git publication, and plugin activation are represented differently across products | A common and honest authority model is the least-converged layer |
| Open-source projects and companies can disappear, continue locally, or move active development private | Independent operation and modification rights have concrete continuity value, but do not guarantee security or maintenance |

The resulting argument is:

```text
concurrency mechanics are crowded
  -> do not differentiate on running more agents

authority remains fragmented across those mechanics
  -> make effective authority explicit and enforced within both product promises

local work and remote publication have different consequences
  -> grant and mediate them separately

extension creation, installation, activation, and escalation are often blurred
  -> give extensions an explicit developer-owned admission lifecycle

the demand frequency is not yet measured
  -> validate the narrow end-to-end workflow before expanding the architecture
```

Alternative positions lose for different reasons:

- The **generic cockpit** is near and legible but competes on execution quality
  and distribution against many existing products.
- The **isolation-first wrapper** has a genuine security job but omits durable
  stream, publication, attention, and adaptation governance.
- The **Git/evidence governor** is comprehensible but competes with CI, hooks,
  branch protection, pull requests, and an emerging Agentplane.
- The **extension governor** addresses a growing surface but has weaker evidence
  of recurrent individual use when separated from running streams.
- A **Jujutsu or reversible-trace substrate** offers valuable mechanisms at much
  greater implementation and migration distance without proving the product
  demand.

The comparison below preserves the decision rather than presenting a dense
pseudo-quantitative scorecard. “Distance” means distance to a credible product
validation, not engineering difficulty alone.

| Position | Primary advantage | Main weakness | Distance |
| --- | --- | --- | --- |
| Status-quo assemblage | Familiar, composable, recoverable, and vendor-independent | The developer manually reconstructs attention and authority across tools | Baseline |
| Generic cockpit | Obvious recurrent UX problem and quick to demonstrate | Crowded; differentiation depends mostly on polish and distribution | Near |
| Isolation wrapper | Can provide strong, agent-neutral containment | Does not solve durable work, attention, publication, or adaptation as a whole | Medium |
| Git and evidence governor | Agent-neutral, legible, and built on durable artifacts | CI, hooks, branch protection, pull requests, and Agentplane are strong substitutes | Near |
| Extension governor | Coherent lifecycle for safe adaptation | Recurrent individual demand is not established when separated from running work | Medium |
| Alternative VCS or trace substrate | Strong conflict retention, history, or replay primitives | Reopens the product model and carries high migration and implementation cost | Far |
| **Combined P direction** | **Best combination of concurrency, adaptation, interoperability, continuity, and honest enforcement** | **Demand and the comprehensibility of heterogeneous enforcement remain unproven** | **Medium** |

The evidence behind each step, the alternatives that could reverse it, and the
project-by-project comparisons follow below.

## P's current comparison baseline

The design space must compare alternatives with P's actual organizing model,
not with a generic “multi-agent dashboard”:

- A **project** is a Git repository.
- A **branch** is a durable piece of work and the state that can cross machines.
- A **session** has an immutable UUID and owns exactly one logical Git branch.
  Its mutable name is the branch's real ref name within the complete project
  repository path.
- A **runtime** is disposable execution attached to the session, not its
  identity. MVP uses one unprivileged Incus system container in a confined
  local project. The configured shell or agent and its default tmux host are
  replaceable interactive mechanisms.
- Every session begins from committed source and owns a real branch. Renaming
  the branch must not replace the session UUID or silently rebind the runtime.
- One P instance permits at most one session owner for a session branch and at
  most one Incus instance for that session UUID.
- The instance-local registry records session/branch/runtime relationships,
  cross-authority lifecycle intent, latest unattended condition, and mediated
  credentials. Incus remains authoritative for its own instances, images,
  storage, and operations.
- Each P instance operates independently. Instances do not discover or address
  one another; when configured, an ordinary shared `origin` is the only MVP
  convergence path. An origin-less project remains local.
- Runtime roots, processes, tmux state, conversations, and uncommitted files do
  not travel between instances.
- The principal overview is active sessions across Git projects—not a generic
  branch browser, task database, process list, or fleet of federated daemons.

This baseline explains why products with similar screens may still represent
different product positions.

## Relevant prior art

The market is not one product category. The important alternatives organize
work around different units and enforce different boundaries.

| Category and examples | Organizing unit | Primary strength | Structural limit |
| --- | --- | --- | --- |
| Status quo — Git, tmux, scripts, hooks, CI, pull requests | terminal, process, branch, worktree | Continuity, composability, familiar recovery, no new control plane | The developer manually reconstructs attention, identity, and authority |
| Vendor-native — Codex, Claude Code, GitHub Copilot, Cursor, Devin, Jules | thread, task, cloud session | Integrated UX, approvals, review, background execution, managed environments | Agent and authority semantics are vendor-specific; service dependence varies |
| Local cockpit — T3 Code, Cline Kanban, Agent Deck, Claude Squad, Nimbalyst, Vibe Kanban, Conductor | card, session, workspace, worktree | Launch, status, terminals, diffs, resume, notifications, Git handoff | Usually delegates containment and permissions to the agent or host |
| Sandbox infrastructure — Handler, container-use, Clawker, OpenHands runtimes, E2B, Daytona | container, VM, sandbox | Environment lifecycle, reproducibility, process or VM isolation | Does not inherently model durable streams, human attention, or publication |
| Git/evidence governance — Agentplane, Shep, branch protection, CI | task, plan, change record, branch/PR | Explicit workflow, verification evidence, review, recovery | Can become process-heavy; runtime and credentials may remain outside its model |
| Extensible hosts — Claude Code, Codex, OpenCode, Cline, goose, OpenHands | agent session and tools | Skills, hooks, MCP, tools, subagents, custom behavior | Activation and runtime authority differ by host and are often broad |
| Protocols — ACP, MCP, Agent Skills | client-agent or agent-tool link | Reduces adapter and packaging friction | Communication and discovery do not create a shared authority model |
| Security/trace — ai-jail, ActPlane/Actime, Shepherd | process tree, policy, trace | Specialized isolation, observation, or replay | Narrower than a daily multi-stream product; several remain research-stage |
| Alternative VCS — Jujutsu | change, operation, workspace | Operation log, snapshots, first-class conflicts, workspaces | Does not detect semantic contradiction; migration and Git interop add complexity |

### Primary comparators

These profiles emphasize operational distinctions rather than exhaustive feature
checklists. “Worktree” means checkout separation only, never a process or
credential security boundary.

#### Codex CLI, app, and cloud

- **Category and deployment:** Apache-2.0 local CLI plus proprietary managed
  experiences, organized around threads, projects, and tasks.
- **Closest overlap:** parallel worktrees, diffs, review, approvals, sandboxed
  execution, skills, plugins, MCP, and app-server integration.
- **Material difference:** local approval policy is distinct from OS sandbox
  policy, while cloud execution and some higher-level experiences remain inside
  OpenAI's stack rather than an agent-neutral, independently operated governor.
- **Evidence value:** polished proof that parallel threads, review, and extension
  packaging are baseline capabilities ([Codex app](https://openai.com/index/introducing-the-codex-app/),
  [CLI](https://github.com/openai/codex)).

#### Claude Code

- **Category and deployment:** proprietary local and remote agent environment,
  organized around sessions, subagents, and worktrees.
- **Closest overlap:** allow/ask/deny rules, sandboxing, managed policy, Git
  workflows, and plugins containing skills, hooks, subagents, MCP/LSP servers,
  and executable behavior.
- **Material difference:** authority and extension semantics remain
  Claude-specific; ordinary Git authority still depends on available commands
  and credentials, and bypass modes exist.
- **Evidence value:** strong comparator for permission and plugin UX; Anthropic
  warns that plugin code may execute with user privileges
  ([permissions](https://code.claude.com/docs/en/permissions),
  [plugins](https://code.claude.com/docs/en/discover-plugins)).

#### GitHub Copilot agents

- **Category and deployment:** proprietary managed service organized around a
  task, branch, and pull request.
- **Closest overlap:** ephemeral execution environments, network controls,
  multi-agent supervision, custom agents, skills, MCP, and a managed
  branch/commit/push/PR review loop.
- **Material difference:** the platform owns the execution and publication path;
  self-hosted runners require operator-provided controls, and the system is not
  independently operable.
- **Evidence value:** strong managed comparator for publication governance and
  agent management ([agent management](https://docs.github.com/en/copilot/concepts/agents/cloud-agent/agent-management),
  [risks and mitigations](https://docs.github.com/en/copilot/concepts/agents/cloud-agent/risks-and-mitigations)).

#### T3 Code

- **Category and deployment:** MIT-licensed local cockpit organized around a
  thread on a branch.
- **Closest overlap:** multi-agent launch, review, and commit/push/PR workflow.
- **Material difference:** containment relies substantially on installed agents
  and the local environment; provider integrations were fixed, with no external
  driver mechanism reported in July 2026.
- **Evidence value:** polished, high-visibility cockpit comparator. Direct user
  reports document fork/rebase cost and “two control planes” for unsupported
  agents, but not population demand ([project](https://t3.codes/),
  [driver request](https://github.com/pingdotgg/t3code/issues/4027),
  [generic-driver discussion](https://github.com/pingdotgg/t3code/discussions/7290)).

#### Cline Kanban

- **Category and deployment:** Apache-2.0 local research preview organized
  around a card and ephemeral worktree.
- **Closest overlap:** task dispatch, attention state, terminals, diffs,
  checkpointing, dependencies, and Git handoff across Cline, Claude, and Codex.
- **Material difference:** worktrees provide checkout separation only;
  experimental permission bypass is used, selected ignored files may be
  symlinked, and Commit/Open PR can be delegated back to the agent by a dynamic
  prompt.
- **Evidence value:** unusually clear evidence that cockpit UX and worktree
  automation are crowded, and that a visible control need not be a host-owned
  authority boundary ([README](https://github.com/cline/kanban)).

#### Agent Deck

- **Category and deployment:** open-source local TUI/web control surface built
  around tmux-backed sessions.
- **Closest overlap:** dense navigation, global search, status watchers,
  notifications, attachment, recovery, and remote access across agent CLIs.
- **Material difference:** agents remain host processes; dangerous modes and
  ordinary ambient Git authority remain possible, so it provides a weak common
  authority boundary.
- **Evidence value:** the closest interaction and attention reference, including
  lessons about stable session identity and isolated tmux server identity
  ([repository](https://github.com/asheshgoplani/agent-deck)).

#### Handler

- **Category and deployment:** MIT-licensed local/self-hosted sandbox and agent
  terminal using Docker or Firecracker.
- **Closest overlap:** agent-neutral isolated environments, worktree/fork
  integration, presets, environment variables, ports, custom images, and MCP.
- **Material difference:** reviewed product materials do not document P's
  session-identity or ref-scoped local-write and explicit-publication topology.
- **Evidence value:** the closest open comparator on agent-neutral local
  isolation, but not the same complete governance model
  ([project](https://handler.dev/)).

#### container-use

- **Category and deployment:** open-source local container workflow organized
  around branch-backed environments.
- **Closest overlap:** isolated development environments and explicit
  accept/apply transitions back to the developer's workspace.
- **Material difference:** it is environment workflow infrastructure rather than
  a persistent multi-stream attention and authority governor.
- **Evidence value:** shows that containers, branches, and review transitions are
  established mechanisms
  ([environment workflow](https://container-use.com/environment-workflow)).

#### Clawker

- **Category and deployment:** open-source local agent sessions in containers.
- **Closest overlap:** host-side network policy, container isolation, and
  agent-specific environment support.
- **Material difference:** some Git and agent credentials can be forwarded and
  agents can retain paths to origin; runtime credential possession therefore
  remains a decisive difference from P's mediated publication boundary.
- **Evidence value:** demonstrates both the value and limits of adding network
  policy to container isolation
  ([internals](https://docs.clawker.dev/container-internals)).

#### OpenHands

- **Category and deployment:** MIT core with local, self-hosted, and managed
  modes, organized around conversations, workspaces, and runtimes.
- **Closest overlap:** interchangeable execution backends plus plugins, agents,
  hooks, commands, and MCP.
- **Material difference:** enforcement depends on the chosen backend—direct
  execution can expose the host while Docker or remote runtimes can isolate—and
  OpenHands owns more of the agent loop than P intends to.
- **Evidence value:** strong open agent-platform comparator, but reviewed
  documentation did not show one uniform extension-capability model
  ([plugins](https://docs.openhands.dev/sdk/guides/plugins),
  [runtime](https://docs.openhands.dev/openhands/usage/architecture/runtime)).

#### Agentplane

- **Category and deployment:** MIT-licensed, local-first, pre-1.0 Git workflow
  governor organized around tasks, semantic episodes, and change records.
- **Closest overlap:** explicit authority, approval boundaries, state
  fingerprints, supervisor-observed receipts, durable evidence, and constrained
  recipes or blueprints.
- **Material difference:** enforcement varies by runner and its workflow can be
  more process-shaped than P's interactive branch-session model.
- **Evidence value:** the closest conceptual overlap on Git-native governance
  and constrained adaptation, but its 76 stars and 9 forks at the research
  cutoff and visibly early maturity make it conceptual prior art—not demand
  validation ([repository](https://github.com/basilisk-labs/agentplane),
  [workflow](https://agentplane.org/docs/user/workflow/)).

#### OpenCode

- **Category and deployment:** MIT-licensed local extensible agent host,
  organized around sessions and agents.
- **Closest overlap:** provider choice, allow/ask/deny permissions, server/SDK
  integration, and project plugins.
- **Material difference:** ambient host authority remains relevant; JavaScript
  or TypeScript under `.opencode/plugins/` loads automatically and configured
  packages install at startup.
- **Evidence value:** a useful counterexample to P's intended
  inert-until-authorized extension lifecycle
  ([plugin documentation](https://opencode.ai/docs/plugins/)).

#### E2B

- **Category and deployment:** Apache-2.0 SDK and infrastructure with a
  commercial service, organized around a sandbox or computer.
- **Closest overlap:** isolated, agent-neutral remote execution and custom
  environments.
- **Material difference:** Git publication and human workstream governance must
  be supplied by surrounding orchestration; self-hosting remains infrastructure,
  not a small local control surface.
- **Evidence value:** proves that sandbox infrastructure is already a distinct,
  occupied product category ([repository](https://github.com/e2b-dev/e2b)).

#### Daytona

- **Category and deployment:** sandbox infrastructure whose active core moved
  from a public to a private codebase in 2026.
- **Closest overlap:** isolated execution environments exposed through APIs and
  environment configuration.
- **Material difference:** it does not supply P's stream, attention, extension,
  and publication governance; continued parity no longer follows from the
  remaining public source.
- **Evidence value:** a concrete reminder that source rights preserve a fork
  point but do not guarantee maintenance
  ([repository notice](https://github.com/daytonaio/daytona)).

#### Jujutsu

- **Category and deployment:** Apache-2.0 local version-control system organized
  around changes, operations, and workspaces; it can use a Git backend.
- **Closest overlap:** automatic snapshots, an operation log, multiple
  workspaces, and first-class conflicted changes that can be retained and
  resolved later.
- **Material difference:** it is neither a process security boundary nor a
  detector of clean textual merges with contradictory semantics; adopting it
  would also add Git-interop and user-migration complexity.
- **Evidence value:** valuable concurrency mechanism and later experiment, not
  a reason to reopen P's settled Git model
  ([conflicts](https://docs.jj-vcs.dev/latest/conflicts/),
  [Git compatibility](https://docs.jj-vcs.dev/latest/git-compatibility/)).

#### Shepherd and ActPlane/Actime

- **Category and deployment:** open research-stage retained-execution and
  process-policy systems.
- **Closest overlap:** OS grants, retained proposals, selective application,
  execution traces, and kernel-observed effect policies.
- **Material difference:** neither is a complete daily stream/publication
  product; Actime documents that secret-egress and file-sink rules are not
  enforceable by its released engine.
- **Evidence value:** important design references for future enforcement and
  recovery, but early or partly unenforceable today
  ([Shepherd](https://github.com/shepherd-agents/shepherd),
  [Actime](https://github.com/eunomia-bpf/actime)).

#### P's reference point

- **Category and deployment:** intended Apache-2.0, independently operated
  control plane organized as Git project → session UUID ↔ branch.
- **Authority model:** one unprivileged confined runtime per session; explicit
  grants; honest reporting of effective and enforced coverage; a session-scoped
  local Git server; assigned-branch writes; no supplied origin authority; and an
  explicit host publication transition.
- **Extensibility:** replaceable mechanisms remain subordinate to core-owned
  discovery, activation, grants, observation, and revocation.
- **Evidence status:** this is an unimplemented design. Its differentiation and
  demand remain hypotheses to validate, not established advantages.

### Adjacent and additional comparators

The primary profiles are intentionally selective. The following projects
preserve additional useful coverage without giving every early tool equal
competitive weight:

- **[Claude Squad](https://github.com/smtg-ai/claude-squad)** demonstrates the
  small Go-TUI-over-tmux form, with worktree sessions for several terminal
  agents. Its `--autoyes` mode also illustrates that unattended operation can
  weaken rather than strengthen authority.
- **[Nimbalyst](https://github.com/Nimbalyst/nimbalyst)**, successor to Crystal,
  adds desktop review, cross-session context, and mobile interaction. It is
  evidence that worktree managers already have credible attention stories.
- **[Conductor](https://www.conductor.build/)** spans polished local workspaces
  and isolated managed microVMs, with queued messages, persistent terminals,
  Git/PR state, and several underlying agent CLIs. Local and cloud modes must be
  compared separately.
- **[Superset](https://github.com/superset-sh/superset)** combines parallel
  worktrees, monitoring, notifications, diffs, terminals, previews, remote
  hosts, and SDK/MCP control. Its ELv2 license makes it source-available rather
  than an open-source substitute.
- **[cmux](https://github.com/manaflow-ai/cmux)** provides agent-aware panes,
  attention rings, notifications, Git/PR metadata, and SSH workspaces.
- **[dmux](https://github.com/standardagents/dmux)** is a small MIT terminal
  manager for worktrees, tmux panes, and merge/PR completion flows.
- **[Hive](https://github.com/anomalyco/hive)** is a worktree-first desktop
  workspace with cross-project visibility, visual diffs, and cross-worktree
  context for multiple agents.
- **[ADHDev](https://adhf.dev/)** connects browser or phone clients to local
  CLI/IDE agents, approval requests, diffs, and worktree dispatch. Its
  peer-to-peer convergence model is broader than P's independent instances.
- **[Shep](https://shep.bot/)** maps features to branches, worktrees, and agent
  sessions and automates commit, push, draft-PR, CI, retry, and merge flow. It
  demonstrates that branch-oriented orchestration and a local registry are not
  differentiators; its ordinary user Git authority differs from P's runtime
  publication boundary.
- **[Omnara](https://github.com/omnara-ai/omnara)** is an Apache-2.0 platform
  with durable agent state, multiple sandbox providers, user machines/VMs,
  cloud or Docker deployment, RBAC, skills/MCP, dashboard, Slack, and API
  interaction. Its durable unit is managed agent state rather than a Git
  branch with disposable runtime.
- **[Coder Agents](https://coder.com/docs/ai-coder/agents)** keeps the agent loop
  and model credentials in its control plane, provisions isolated user
  workspaces, and can enforce egress and identity policy. It is enterprise and
  vertically integrated, but proves that credential mediation and server-side
  authority are not unique ideas.
- **[AgentsMesh](https://github.com/AgentsMesh/AgentsMesh)** schedules
  worktree-backed agent pods across self-hosted runners with web, desktop, and
  mobile clients. Its federated central-service topology is intentionally the
  opposite of independent P instances, and its BSL-1.1 terms are
  source-available rather than open source at the cutoff.

Agent Deck remains the closest interaction reference rather than the closest
authority model. Its dense navigation, global search, attention delivery,
attachment, recovery, and remote surfaces are established UX patterns. Its
[release history](https://github.com/asheshgoplani/agent-deck/releases) also
reinforces two implementation lessons: durable session identity must not be
silently rebound, and tmux fleet operations need an isolated server identity
rather than broad process matching.

### Open-source and continuity distinctions

The [Open Source Definition](https://opensource.org/osd) requires rights to use,
inspect, modify, and redistribute; visible source alone is not equivalent. For
example, Superset describes itself as source-available under Elastic License
2.0, which permits substantial use and modification but restricts offering the
software itself as a managed service ([ELv2 FAQ](https://www.elastic.co/licensing/elastic-license/faq)).

Open source matters to P because it can make the complete core inspectable,
adaptable, and continuable without permission from a service owner. It does
not prove that boundaries are correctly implemented, that users understand
grants, or that a community will maintain a fork. Two 2026 cases illustrate
both sides:

- Vibe Kanban's company shut down while the local product continued as a
  community-maintained open-source project; its remote services disappeared
  ([shutdown notice](https://vibekanban.com/blog/shutdown)).
- Daytona's public code remained forkable after active core development moved
  private, but continued parity and maintenance no longer followed from source
  availability alone ([repository notice](https://github.com/daytonaio/daytona)).

P should therefore claim independent operation and modification rights, not
“open source is secure” or “open source guarantees continuity.”

## Candidate validation wedges

“Wedge” here means a focused entry or validation workflow, not a restriction on
the eventual product scope. The governing user confirmed that the first public
version should pair the concurrent-session experience with a reproducible guide
for changing a real workflow through an agent-authored plugin. The exact guide
plugin remains unresolved; the ranking below supplies candidates rather than an
adopted release choice.

### 1. Public-entry experience: governable concurrent branch sessions

**What it proves:** one developer can run several real coding-agent streams and
answer:

- What is each stream doing and which one needs attention?
- Which project, branch/ref, runtime, and agent belong to it?
- What can it actually access, and which bounds are enforced?
- What may be stopped, resumed, revoked, discarded, or explicitly published?

The differentiating proof is not the dashboard. It is that a stream can perform
useful local work in a confined runtime, write only through its scoped Git path,
remain unable to use origin credentials, and require a developer-owned
publication transition. Status and attach complete the workflow without
becoming the product thesis.

**Strongest substitutes:** terminals or tmux, Git worktrees, each agent's native
permissions, Cline Kanban, Conductor, and Claude Squad.

**Success signals:**

- repeated use with multiple simultaneous real streams, not a scripted demo;
- users correctly predict what a stream can and cannot do;
- an attempted cross-stream or remote-publication action is actually blocked;
- users consult the authority and attention state before intervening;
- work remains recoverable through ordinary local artifacts when P is stopped.

**Main uncertainty:** whether enough individual developers maintain concurrent
streams often enough for the setup and confinement cost to beat the familiar
terminal/worktree assemblage.

**Rank-change conditions:**

- target users rarely maintain concurrent streams;
- setup and confinement cost exceeds the terminal/worktree status quo;
- users immediately bypass the boundary for normal work;
- the authority presentation is misunderstood or ignored;
- explicit publication adds ceremony without preventing real mistakes.

### 2. Leading plugin-guide candidate: governed new-agent adapter

**What it proves:** after the control-plane boundary works, an already trusted
coding agent can author an adapter for one unsupported, locally installed
coding agent. The adapter remains inert until the developer reviews its source,
version, provenance, and requested capabilities. The developer grants only the
process and communication authority needed, activates it for a bounded scope,
uses the new agent beside existing streams, inspects activity, revokes it, and
later reviews an update.

```text
author or import
  -> inert artifact
  -> inspect code, version, provenance, and requested authority
  -> developer activation and grant
  -> observable bounded use
  -> revocation
  -> update review, with reauthorization for expanded authority
```

This is the research's strongest current plugin-guide candidate because
adaptation directly changes what P can govern. Two T3 Code reports provide
concrete existence evidence: one user faced a fork with continuing rebase cost;
another described operating “two control planes for one machine.” They do not
establish frequency.

**Strongest substitutes:** run the unsupported agent separately, maintain a
private fork, wait for upstream support, contribute a provider upstream, or use
an ACP-compatible configuration when available.

The adapter's authority and the child coding agent's authority must remain
separate. An adapter should not inherit arbitrary shell, network, credential,
or filesystem access merely because the child agent needs a project runtime.

**Success signals:** the developer routes real work through the new adapter
beside another agent, correctly explains the grant, rejects an intentionally
unnecessary capability, successfully revokes it, and notices an
authority-expanding update.

**Main uncertainty:** unsupported-agent demand may be episodic, and lifecycle,
streaming, authentication, cancellation, and resumption differences may make a
nominally thin adapter hard for an agent to author and a human to review.

**Rank-change conditions:**

- ACP or another interface reduces new-agent support to inert configuration;
- unsupported-agent demand is rare among target users;
- useful adapters require effectively unrestricted host authority;
- agent-authored adapters require enough expert repair that authorship is not a
  credible user workflow.

### 3. Cross-agent repository check and review-evidence extension

**What it proves:** an agent can author a repository-specific extension that
runs explicitly approved checks and records evidence consistently across
heterogeneous streams. The developer reviews exact commands, paths, and effects
before activation.

This problem is likely more recurrent and the grant easier to understand than
an agent adapter. It is a weaker differentiation test because scripts, task
runners, hooks, CI, Claude hooks, and Agentplane recipes are strong substitutes.
The extension may provide evidence to the developer; it must not make P the
semantic authority on whether a change is correct or complete.

**Strongest substitutes:** a checked-in script such as `make check`, task
runners, Git hooks, CI, agent-native hooks, and Agentplane recipes.

**Success signal:** users keep one governed mechanism active across different
agents because its shared grant, evidence, and review surface reduce real
coordination cost relative to those substitutes.

**Main uncertainty:** the feature may be only a better wrapper around a project
script rather than a distinct reason to adopt P.

**Rank-change condition:** move it ahead of the adapter if unsupported-agent
demand is rare and users repeatedly need a cross-agent acceptance policy; move
it down if users cannot articulate value beyond nicer packaging for `make
check`.

### 4. Repository development-environment steward

**What it proves:** an agent can author bounded setup, readiness, run, log, and
teardown behavior for each stream's development environment. This can make
concurrent work much more usable when worktrees or runtimes need distinct
services and ports.

**Strongest substitutes:** package scripts, devcontainers, Docker Compose, and
existing cockpit setup/teardown hooks.

**Success signal:** developers repeatedly use per-stream environments without
granting arbitrary shell access, leaking secrets, confusing ports, or leaving
orphaned processes behind.

**Main uncertainty:** the capability envelope expands quickly into processes,
environment variables, ports, files, and secrets, potentially defeating a
legible least-privilege grant.

**Rank-change condition:** raise it if environment lifecycle repeatedly blocks
parallel work and a useful least-privilege grant remains understandable; defer
it if normal repositories immediately require an unrestricted-shell escape
hatch. It becomes attractive after P has proven narrower grants.

### 5. Later situational wedges

- **Scoped repository-host connector:** good permission-comprehension test, but
  OAuth, remote APIs, and provider churn can dominate the product learning.
- **API migration or codemod validator:** useful for batch consistency and
  demonstrates concurrent application of a temporary tool, but is less aligned
  with the individual-developer scope, has mature language-specific substitutes,
  and should not prescribe WASM/MCP before the plugin boundary is selected.
- **Read-only observability or notifications:** safe and easy onboarding, but
  too weak to validate consequential delegated authority.
- **Conflict-aware integration assistance:** worth later study, including
  Jujutsu experiments, but no VCS can automatically reconcile cleanly merged
  yet semantically contradictory agent decisions.

## Rejected first positions and wedges

- **Another worktree/Kanban manager:** supporting UX, not a defensible thesis.
- **A sandbox company first:** crowded infrastructure category and a material
  expansion away from P's control-plane job.
- **Marketplace first:** confounds value with supply and is unnecessary for a
  private agent-authored extension.
- **PR review as the product:** already supplied by Git hosts, vendors, and local
  managers; it says little about who could publish.
- **Jujutsu as an immediate foundation:** changes P's settled Git mental model
  and improves conflict handling without solving semantic contradiction.
- **eBPF as the product moat:** implementation-specific, platform-dependent, and
  currently incomplete for several information-flow policies.
- **Full reversible execution tracing first:** valuable research substrate but
  high implementation distance and weak evidence for the target user's first
  job.
- **Multi-repository enterprise migration first:** a credible later scenario,
  not the strongest entry for the established individual-developer product.

## Contradictions, cautions, and evidence gaps

### Adoption and demand

Broad surveys disagree about coding-agent adoption because dates, samples, and
definitions differ. More importantly, no reviewed prevalence study measures
individual developers routinely supervising three or more heterogeneous coding
agents. Product supply and vendor investment do not fill that gap.

Vibe Kanban reported thousands of daily users but predominantly free usage and
no compelling business model. This is evidence that cockpit UX can attract
interest without proving a durable differentiated product. Because P is open
source, the commercial conclusion is not directly binding; retention and
maintenance value still matter.

### Productivity

METR's 2025 randomized study of 16 experienced open-source developers across
246 tasks found that early-2025 AI tools increased completion time by 19% while
participants expected a 20% speedup. The result is narrow in population and
time; it supports skepticism about perceived speed, not a universal claim that
agents slow developers ([METR](https://metr.org/blog/2025-07-10-early-2025-ai-experienced-os-dev-study/)).

P should validate reduced supervision and authority-management cost rather than
assume that maximizing concurrent agent count increases output.

### Coordination and conflict

CooperBench found that coding agents working cooperatively averaged 30% lower
success than performing the paired tasks individually; particular leading
models showed roughly a 50% gap. The benchmark studies agent cooperation, not
whether a human-governed control plane improves outcomes
([CooperBench](https://arxiv.org/abs/2601.13295)).

Worktrees avoid simultaneous edits in one checkout but cannot prevent textual
or semantic integration conflicts. Jujutsu makes unresolved conflicts durable
and postponable; it does not eliminate them or detect contradictory designs.

### Permissions and comprehension

Permission prompts are not enforcement, and approval is not comprehension. A
classic Android study found only 17% of participants attended to install-time
permissions and only 3% answered all comprehension questions correctly. Its
population and mobile context do not transfer numerically to developers; it is
a methodological reason to test objective comprehension instead of counting
clicks ([Felt et al.](https://research.google/pubs/android-permissions-user-attention-comprehension-and-behavior/)).

### Extension risk

SkillHarm demonstrates fixed-payload and self-mutating poisoning against agent
skills, with 879 generated attack samples across 71 skills. It supports treating
skills as an attack surface, not the claim that every skill auto-executes or
that one sandbox makes the threat inert
([SkillHarm](https://arxiv.org/abs/2606.02540)).

There is no controlled study of developer comprehension of grants for
agent-authored extensions, no reliable measure of how often individuals execute
third-party skills, and no evidence that source visibility alone causes users
to detect malicious behavior.

### Enforcement and architecture

Containers, OS sandboxes, microVMs, capability runtimes, and kernel policy each
have different coverage and failure modes. The broad Gemini report's claim of
absolute eBPF enforcement is contradicted by Actime's documented unenforceable
secret-egress and file-sink rules. Likewise, MCP authentication and a WIT
interface do not themselves enforce P's rule that an extension cannot approve
or expand its own authority.

The architecture should be selected only after the required authority semantics
are explicit and testable.

### Competitive uncertainty

Agentplane demonstrates conceptual convergence but has weak public adoption and
product-maturity signals. It should constrain novelty claims, not be treated as
a validated competitor. Conversely, polished vendor and cockpit products are
credible substitutes even when their governance model differs, because users
may prefer convenience to P's stronger boundary.

Standards can change wedge value. ACP may turn some agent adapters into
configuration; vendor extension manifests may converge on better activation and
grant semantics. That would reduce adapter novelty while expanding the number
of components P's combined control plane can govern.

## Conditions that would change the recommendation

Prefer the status quo or a simpler cockpit if research shows that target users:

- rarely run concurrent durable agent streams;
- understand and accept native agent permissions separately;
- do not encounter cross-stream authority or publication mistakes;
- value zero setup more than a common enforcement boundary.

Move extension governance earlier if agent-authored or imported extensions
become routine and users experience real activation, update, or revocation
surprises. Move it later if extension authoring remains rare or every useful
extension requires broad host authority.

Reconsider the differentiated position if a widely adopted vendor-neutral
control plane ships a demonstrably host-enforced cross-agent authority model,
separate publication authority, independent local continuity, and governed
extension activation together.

Revisit P's Git substrate only if empirical use shows that the settled model
cannot support acceptable conflict handling or recovery and a Jujutsu-backed
prototype demonstrably improves the workflow without sacrificing P's Git-facing
continuity.

## Recommended validation work

No prototype, benchmark, or user study was performed during this exploration.
The next evidence should come from behavior rather than another feature survey:

1. Use P for sustained real concurrent work and record where the governing user
   returns to terminals, bypasses a boundary, misunderstands state, or cannot
   adapt the workflow cleanly.
2. Select a real workflow pressure from that use and have an agent implement it
   through P's supported plugin path. Exercise testing, review, activation,
   bounded use, revocation, and an authority-expanding update where relevant.
3. Turn the validated path into the public plugin guide while keeping the exact
   example subordinate to the general authoring and governance workflow.
4. Share the coherent concurrent-session experience and guide, then observe
   whether unfamiliar developers can reproduce, understand, retain, and modify
   the workflow without the governing user's help.
5. Only then compare the experience with terminals, worktrees, native
   permissions, scripts, hooks, forks, and manual Git publication, and test
   additional plugin candidates such as the unsupported-agent adapter or
   repository check extension.
6. Measure continued use, bypass behavior, recovery, and permission
   comprehension; do not treat installation, successful generation, completion
   of the tutorial, or an Allow click as evidence of recurring value.

## Appendices

### Appendix A — Research scope and methodology

The research asked:

1. What product and approach categories already address concurrent coding-agent
   work?
2. What could P provide that is materially different from open-source,
   source-available, and proprietary alternatives?
3. Which initial product and adaptation wedges best test that difference?

It covered local, self-hosted, and managed products. Open source was an
evaluation criterion and possible differential, not an inclusion filter or
security guarantee.

The binding product direction remained [`PRODUCT.md`](PRODUCT.md): P is for an
individual developer; the developer remains the governor; streams have durable
branch and session identity; authority is explicit and bounded; the complete
core operates independently; and extensions cannot activate or expand their own
authority. This analysis may recommend validation but cannot silently revise
that direction.

The comparison re-grounded itself in `PROJECT.md`, `PRODUCT.md`,
`mvp-status.md`, `missing-pieces.md`, the previous prior-art inventory, and the
Apache-2.0 license. It checked current first-party product documentation,
repositories, specifications, and primary empirical research through the
cutoff. The previous inventory's still-useful project and mechanism coverage
was incorporated here so this document can stand alone as its successor.

Four independently produced deep-research reports were supplied:

- a focused Gemini report recommending multi-repository API migration through
  a generated WASM/MCP validator;
- a focused ChatGPT report recommending an agent-authored adapter for an
  unsupported coding agent, with a repository acceptance gate as fallback;
- a broad Gemini report recommending an eBPF/OS sandbox plus Jujutsu-backed
  orchestrator;
- a broad ChatGPT report recommending an authority-centric local governor,
  followed by extension admission and publication controls.

The reports were treated as evidence inputs, not votes. Material assertions
were sample-checked against primary sources. Architecture-first recommendations
were reduced to candidate approaches until justified by the product comparison.
Direct user reports established that a problem exists, not how common it is.

The research deliberately did not:

- choose a plugin protocol, sandbox implementation, or policy language;
- treat Git worktrees as process or security isolation;
- make P responsible for an agent's semantic judgment or behavior;
- prioritize marketplaces, federation, teams, or enterprise administration;
- assume that more concurrent agents necessarily increase productivity;
- treat product features, marketing, repository stars, or project activity as
  proof of demand.

Options were compared on problem recurrence, differentiation, coherence between
concurrency and adaptation, authority enforceability and comprehension,
advantage over substitutes, interoperability, independent operation,
open-source continuity, usefulness without marketplace scale,
individual-developer fit, reversibility, implementation distance, and evidence
quality. Ratings are ordinal judgments, not measured market scores.

### Appendix B — Terms and evaluation conventions

- **Work stream**: durable work state, one or more execution sessions, its Git
  identity, and its current delegation. It is not synonymous with a process,
  chat, worktree, or card.
- **Requested authority**: what a stream, agent, or extension asks to do.
- **Effective authority**: what it can actually do, including ambient access.
- **Enforced authority**: the subset constrained by a mechanism outside the
  governed principal's control.
- **Cooperative constraint**: an instruction or convention the principal can
  technically bypass.
- **Publication**: crossing from local work to a remote or shared Git surface.
  Editing, committing, pushing, opening a pull request, and merging are distinct
  powers.
- **Adaptation**: changing P's behavior through a replaceable extension. Agent
  instructions alone do not demonstrate a governed extension boundary.

The earlier prior-art vocabulary remains a useful compact test: a control plane
may **know** about work, **place** it into an environment, or **govern** that
environment's authority. A dashboard that knows and a sandbox that places do
not necessarily govern publication or extension activation.

Git worktrees provide multiple working trees attached to one repository and
share substantial repository metadata. They are concurrency mechanics, not
process, network, port, credential, or runtime isolation
([Git documentation](https://git-scm.com/docs/git-worktree)).

### Appendix C — Secondary infrastructure and mechanism findings

#### Linux confinement mechanisms

A container is an orchestration of kernel mechanisms rather than one security
primitive. Namespaces limit what a process can see; mounts and LSM policy limit
object access; Linux capabilities split privileged root operations; seccomp
filters syscalls; cgroups group processes and limit resources; and network
policy controls connectivity.

eBPF is an optional programmable observation or enforcement mechanism attached
to particular kernel hooks. It does not create a container or complete sandbox
and only covers the hooks and cases implemented. Agent-oriented eBPF work is
relevant future evidence, but Actime currently reports that some secret-egress
and file-sink rules cannot be enforced by its released ActPlane engine
([Actime](https://github.com/eunomia-bpf/actime)).

Linux capabilities such as `CAP_NET_ADMIN`, `CAP_SYS_PTRACE`, or the broad
`CAP_SYS_ADMIN` govern privileged kernel operations. They are not equivalent to
P-level capabilities such as “may update this session ref” or “may invoke this
service.” P's grants require several enforcement mechanisms, including Git and
credential mediation.

#### Scale indicators that do not establish product demand

- GitSkills found 3,797,117 `SKILL.md` file occurrences across 282,200 public
  repositories in July 2026, representing 1,877,981 distinct contents. This
  demonstrates artifact proliferation, not how often developers install,
  execute, trust, or update third-party skills
  ([GitSkills](https://arxiv.org/abs/2608.10906)).
- TraceLab collected 4,265 Claude Code and Codex sessions from 43 developers,
  containing 357,161 model steps and 432,510 tool calls. It demonstrates that
  real sessions can be long-lived and operationally complex, but does not
  measure the prevalence of concurrent supervision or prove that a governance
  cockpit improves outcomes ([TraceLab](https://arxiv.org/abs/2606.30560)).
- Repository stars, feature presence, vendor investment, and self-reported user
  counts were treated as maturity or existence signals only. Agentplane's low
  public traction constrains its weight as a competitor; Vibe Kanban's reported
  usage and shutdown show that usage alone does not establish a sustainable or
  differentiated position.

#### Execution-provider references

**[Incus](https://linuxcontainers.org/incus/docs/main/)** remains the selected
MVP substrate, not P's product identity. It supplies system containers and VMs,
images, storage pools, projects, operations/events, exec, and a local API.
[Confined user projects](https://linuxcontainers.org/incus/docs/main/howto/projects_confine/)
let the machine owner bound which instances, devices, paths, and networks P may
control. Incus also documents that unrestricted administrative access is
[host-root-equivalent](https://linuxcontainers.org/incus/docs/main/explanation/security/),
which is why P must use only the confined project and socket.

Incus does not supply P's project/session/branch identity, ref authorization,
lifecycle intent, status model, extension grants, TUI, or publication boundary.
MVP uses local Incus only; remote Incus servers and clusters do not imply P
daemon federation.

Potential future providers include Incus VMs, Kubernetes,
[E2B](https://github.com/e2b-dev/e2b),
[Fly Sprites](https://fly.io/sprites/), [exe.dev](https://exe.dev/docs/what-is-exe),
and [Vercel Sandbox](https://vercel.com/docs/sandbox). A provider is eligible
only if it preserves P's instance, Git, network, credential, status, and
publication boundaries.

#### Model gateways and protocols

Model routing is commodity infrastructure:

- **[Bifrost](https://github.com/maximhq/bifrost)** is the current post-MVP
  direction. P consumes filtered runtime-facing interfaces and keeps gateway
  administration host/control-plane-only.
- **[LiteLLM](https://docs.litellm.ai/)** is a broad compatibility fallback
  with a larger Python service footprint.
- **[Envoy AI Gateway](https://aigateway.envoyproxy.io/)** becomes relevant if
  distributed ingress, cluster identity, traffic policy, or self-hosted model
  fleets justify it; Kubernetes alone does not require it.
- **[Portkey](https://portkey.ai/docs/product/ai-gateway)** is a managed/hybrid
  reference rather than the local default.

The staged gateway design remains in [`model-gateway.md`](model-gateway.md).
ACP, MCP, and Agent Skills should likewise be reused where they fit. They
standardize communication or packaging; P owns the activation, authority,
publication, and continuity semantics they do not define.

#### Early watchlist and historical entries

The following projects provide useful general evidence but do not materially
drive the recommendation:

- **[Harness](https://github.com/majiayu000/harness):** early Claude/Codex fleet
  orchestration with worktrees, policy, network allowlists, review, GitHub
  automation, and telemetry.
- **[Ephemeral Sandbox](https://github.com/Ephemeral-AI-Lab/ephemeral-sandbox):**
  isolated workspace sessions with provenance, observation, and all-or-nothing
  publication of a resolved change set.
- **[Celln](https://github.com/sympozium-ai/celln):** pre-alpha KVM cells with
  invocation-level authority, read-only tools, and no ambient network.
- **[Sculptor](https://github.com/imbue-ai/sculptor):** experimental desktop
  parallel-agent worktrees, review/PR flow, and container/remote work.
- **[ccmux](https://github.com/epilande/ccmux):** tmux agent picker with
  notifications, worktrees, previews, and handoff.
- **[VibePod](https://github.com/VibePod/vibepod-cli):** local Podman/Docker
  agent containers with traffic and usage analytics.
- **[GitButler](https://github.com/gitbutlerapp/gitbutler):** adjacent history
  tooling for parallel or stacked branches, undo, and forge integration.
- **[Sketch](https://sketch.dev/):** discontinued and redirected to exe.dev;
  historical evidence for container-per-agent plus Git output.
- **Catnip:** the former `wandb/catnip` repository returned 404 during the
  earlier review; cached descriptions were insufficient for active comparison.

### Appendix D — Sources consulted

#### Project sources

- `PROJECT.md`
- `docs/PRODUCT.md`
- `docs/mvp-status.md`
- `docs/missing-pieces.md`
- `LICENSE`

#### Product, project, and standards sources

- [Git worktrees](https://git-scm.com/docs/git-worktree)
- [OpenAI Codex app](https://openai.com/index/introducing-the-codex-app/) and
  [Codex CLI](https://github.com/openai/codex)
- [Claude Code worktrees](https://code.claude.com/docs/en/worktrees),
  [permissions](https://code.claude.com/docs/en/permissions), and
  [plugins](https://code.claude.com/docs/en/discover-plugins)
- [GitHub agent management](https://docs.github.com/en/copilot/concepts/agents/cloud-agent/agent-management)
- [T3 Code](https://t3.codes/) and its
  [Cortex](https://github.com/pingdotgg/t3code/issues/4027) and
  [generic-driver](https://github.com/pingdotgg/t3code/discussions/7290) reports
- [Cline Kanban](https://github.com/cline/kanban)
- [Agent Deck](https://github.com/asheshgoplani/agent-deck)
- [Claude Squad](https://github.com/smtg-ai/claude-squad),
  [Nimbalyst](https://github.com/Nimbalyst/nimbalyst),
  [Conductor](https://www.conductor.build/),
  [Superset](https://github.com/superset-sh/superset),
  [cmux](https://github.com/manaflow-ai/cmux),
  [dmux](https://github.com/standardagents/dmux), and
  [Hive](https://github.com/anomalyco/hive)
- [Handler](https://handler.dev/)
- [container-use](https://container-use.com/environment-workflow) and
  [Clawker](https://docs.clawker.dev/container-internals)
- [Shep](https://shep.bot/),
  [Omnara](https://github.com/omnara-ai/omnara),
  [Coder Agents](https://coder.com/docs/ai-coder/agents), and
  [AgentsMesh](https://github.com/AgentsMesh/AgentsMesh)
- [OpenHands plugins](https://docs.openhands.dev/sdk/guides/plugins) and
  [runtime](https://docs.openhands.dev/openhands/usage/architecture/runtime)
- [Agentplane](https://github.com/basilisk-labs/agentplane)
- [OpenCode plugins](https://opencode.ai/docs/plugins/)
- [Incus](https://linuxcontainers.org/incus/docs/main/),
  [Bifrost](https://github.com/maximhq/bifrost),
  [LiteLLM](https://docs.litellm.ai/), and
  [Envoy AI Gateway](https://aigateway.envoyproxy.io/)
- [E2B](https://github.com/e2b-dev/e2b) and
  [Daytona](https://github.com/daytonaio/daytona)
- [Jujutsu conflicts](https://docs.jj-vcs.dev/latest/conflicts/) and
  [concurrency](https://docs.jj-vcs.dev/latest/technical/concurrency/)
- [Shepherd](https://github.com/shepherd-agents/shepherd) and
  [Actime](https://github.com/eunomia-bpf/actime)
- [MCP](https://modelcontextprotocol.io/) and
  [ACP](https://agentclientprotocol.com/get-started/introduction)
- [Open Source Definition](https://opensource.org/osd) and
  [Elastic License 2.0 FAQ](https://www.elastic.co/licensing/elastic-license/faq)

#### Empirical and security sources

- [METR early-2025 developer productivity study](https://metr.org/blog/2025-07-10-early-2025-ai-experienced-os-dev-study/)
- [TraceLab](https://arxiv.org/abs/2606.30560)
- [CooperBench](https://arxiv.org/abs/2601.13295)
- [GitSkills](https://arxiv.org/abs/2608.10906)
- [SkillHarm](https://arxiv.org/abs/2606.02540)
- [Human-AI synergy in agentic code review](https://arxiv.org/abs/2603.15911)
- [Android permission comprehension](https://research.google/pubs/android-permissions-user-attention-comprehension-and-behavior/)
- [OWASP Agentic Skills Top 10](https://owasp.org/www-project-agentic-skills-top-10/)

#### Independent reports

The four supplied reports are retained as research inputs in the conversation
that produced this carrier. They did not provide durable standalone artifacts
in the repository. Their material recommendations and disputed claims are
represented in this synthesis rather than appended as parallel conclusions.

### Appendix E — Research history

- **2026-09-03 — governing-user direction integrated.** The governing user did
  not adopt the research's authority-centric framing as P's central identity.
  Concurrent-stream governance and agent-driven workflow adaptation remain the
  two central forms of value; authority is an essential supporting property.
  Validation now begins with personal use, followed by a public
  concurrent-session experience that includes a reproducible agent-authored
  plugin guide. The exact guide plugin remains unresolved.
- **2026-09-03 — reordered for decision-first reading.** Conclusions,
  differentiation, wedge order, confidence, and the causal comparison moved to
  the front. Project-by-project prior art now follows the argument, while
  methodology, terminology, general mechanism notes, scale indicators,
  infrastructure references, and the early watchlist live in appendices. The
  research conclusion did not change.
- **2026-09-02 — restarted broad exploration.** The preceding exploration had
  incorrectly narrowed the decision to a first validation use case. This run
  expanded the scope to product categories, open and proprietary alternatives,
  differentiation, product positions, and then initial wedges. The material
  conclusion is that cockpit, worktree, sandbox, review, multi-agent launch, and
  extension mechanics are individually crowded. P's best-supported position is
  the developer-owned composition of durable streams, honest enforced
  authority, separate publication, independent operation, and governed
  adaptation. Confidence is high in the commodity assessment, medium-high in
  the structural differentiation, and low-to-medium in current demand among
  individual developers.
