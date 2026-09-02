# P — project guidance

This document is authoritative for P's enduring purpose, goals, non-goals,
project model, and documentation authority. MVP scope and implementation status
remain in [the MVP snapshot](docs/mvp-status.md); detailed behavior remains with
the subject owners in the [authority map](#authority-map).

## Purpose

P is a self-contained, extensible control plane for running and governing
concurrent streams of agent-driven software development. Each instance
composes familiar developer interfaces—such as Git, containers, terminals,
and service protocols—and exposes stable plugin contracts so developers and
agents can inspect, adapt, and extend it without depending on a centralized P
service or replacing existing tools.

Self-contained means that an instance independently owns its control state and
core lifecycle. It may use optional external integrations, but it does not
require another P instance or a mandatory P-hosted service.

## Goals

### Make concurrent development streams governable

Developers can account for, inspect, enter, and act on many concurrent human-
or agent-driven development streams without reconstructing their state from
terminals and processes.

**Success signal:** Every P-managed stream exposes trustworthy condition,
attention signals, relevant authority, and available lifecycle actions across
projects.

### Bound delegated authority

Developers can let agents and plugins work independently while their
capabilities, publication authority, and potential destructive effects remain
explicit and contained.

**Success signal:** Agents and plugins cannot exceed their declared grants,
failures do not silently widen authority, and destructive actions explain what
will be lost before proceeding.

### Preserve interoperability and continuity

Developers and agents can produce, inspect, retain, and continue work through
familiar development interfaces both inside and outside P.

**Success signal:** Durable work is accessible through common tools and
protocols rather than trapped in a P-specific representation or dependent on P
control-state federation.

### Operate independently

A developer can operate a complete P instance without another P instance or a
mandatory centralized P service.

**Success signal:** Each instance owns its control state and core lifecycle.
Failure or absence of an optional integration degrades only the capability
that integration provides.

### Make extension a product capability

Developers and agents can add integrations and workflows through stable,
documented plugin contracts without forking or rebuilding P core.

**Success signal:** A plugin can be authored, tested, discovered, and installed
independently. Trusted developer configuration controls its activation and
capabilities, after which it can operate autonomously within those grants.

## Non-goals

### Agent behavior remains outside P core

P governs development streams—their environments, identity, lifecycle, and
capabilities—but does not define the agents operating within them. Reasoning,
conversations, task planning, tool selection, subagent coordination, and
completion judgment belong to the agent or an optional plugin. P may launch
agents and receive status from them without making their internal behavior
part of its core model.

### P does not replace the development ecosystem

Established systems retain authority for the concepts they already own. P
composes, constrains, and presents those systems rather than replacing them
with proprietary equivalents. P adds its own lifecycle and governance
semantics only where those systems do not provide them.

### Federation is not P's core operating model

Independent operation never depends on a centralized P service, another P
instance, or cross-instance consensus. Future multi-instance coordination may
exist as an optional capability or plugin, but it cannot become the authority
or prerequisite for an instance's core lifecycle.

These boundaries do not exclude future multi-user operation, additional
runtime environments, service orchestration, richer event handling, or
optional multi-instance tools.

## Tenets

**Unless you know better ones.** No separate project tenets are currently
warranted. The purpose, goals, non-goals, detailed contracts, and existing
technology principle already settle the recurring choices evidenced so far.
Add a tenet only when a consequential recurring choice remains genuinely
ambiguous after applying those authorities and its governing users confirm the
direction and accepted cost.

## Project model

The canonical terms below are defined in the [glossary](GLOSSARY.md).

- A developer governs a self-contained P instance and its trusted
  configuration.
- A P instance owns its registry, project/session relationships, lifecycle
  intent, and plugin activation.
- A project represents one body of source work and may connect to an ordinary
  external origin.
- A session represents one durable development stream. It has an assigned
  branch and associated runtime while it exists.
- A runtime is replaceable execution machinery. An attachment temporarily
  connects a user to its persistent interactive host without defining session
  identity.
- Agents operate inside governed environments but retain ownership of their
  reasoning and workflows.
- Developers or agents may author and test plugins. Trusted developer
  configuration controls plugin activation and grants; an activated plugin may
  operate autonomously only within them.
- Existing systems retain authority for source, execution, process
  supervision, terminal behavior, and optional external services. P composes
  and governs their relationships.
- Independent P instances may exchange durable work through ordinary external
  systems without sharing control-plane authority.

## Authority map

| Subject | Authority | Scope |
|---|---|---|
| Project guidance | This document | Enduring purpose, goals, non-goals, tenet status, project model, and authority ownership |
| Product direction | [docs/PRODUCT.md](docs/PRODUCT.md) | Product vision, target users, value, strategy, bets, outcomes, strategic boundaries, assumptions, risks, and open strategic questions |
| Domain language | [GLOSSARY.md](GLOSSARY.md) | Canonical P terms and boundaries |
| Product summary | [README.md](README.md) | Human-facing summary; not a competing contract |
| Documentation rules | [docs/AGENTS.md](docs/AGENTS.md) | Normative ownership and document roles under `docs/` |
| Project lifecycle | [docs/project-lifecycle.md](docs/project-lifecycle.md) | Project identity, creation, origins, retained branches, and project deletion |
| Session lifecycle | [docs/session-lifecycle.md](docs/session-lifecycle.md) | Session identity, operations, loss analysis, and recovery |
| Runtime and isolation | [docs/runtime-isolation.md](docs/runtime-isolation.md) | Runtime placement, systemd hosting, grants, storage, networking, attachment execution, and cleanup |
| Communication | [docs/communication-boundaries.md](docs/communication-boundaries.md) | Git, RPC, SSH, attachment, build, gateway, and event-handler channel boundaries |
| Observability | [docs/session-observability.md](docs/session-observability.md) | Public conditions, attachment presence, status reduction, and P events |
| Environments | [docs/environment-building.md](docs/environment-building.md) | Nix selection, isolated realization, activation, image caching, and collection |
| Post-MVP model gateway | [docs/model-gateway.md](docs/model-gateway.md) | Retained Bifrost boundary, policy, principal lifecycle, and evolution |
| Implementation choices | [docs/technology-stack.md](docs/technology-stack.md) | Stack choices, internal seams, dependencies, and unresolved plugin contract |
| Validation | [docs/development-validations.md](docs/development-validations.md) | Evidence gates; product behavior remains with its subject owner |
| Current state and work | [docs/mvp-status.md](docs/mvp-status.md) and [docs/missing-pieces.md](docs/missing-pieces.md) | Non-normative snapshot and implementation tracker |
| Explanation and context | [docs/FAQ.md](docs/FAQ.md), [docs/PR.md](docs/PR.md), and [docs/prior-art.md](docs/prior-art.md) | Tradeoffs, product narrative, and external landscape; non-normative |

## Open directions

Open directions preserve possibilities; they are not commitments, requirements,
or approved plans.

- MVP must prove the first-class plugin goal through a basic working
  composition of secure first-party implementations. Packaging, process
  model, transport, compatibility, capability manifests, installation, and
  approval UX remain unresolved.
- Exact TUI layout, navigation, keys, and initial slice remain pending a
  prototype.
- Additional runtime environments, multi-user operation, service
  orchestration, richer event handlers, and optional multi-instance
  coordination may be explored while preserving the confirmed guidance.

The repository currently contains design rather than a production
implementation. [Development validations](docs/development-validations.md)
identify the evidence required before support claims are made.
