# P — project guidance

This document is authoritative for P's enduring purpose, goals, non-goals,
project model, and documentation authority. MVP scope and implementation status
remain in [the MVP snapshot](docs/archive/mvp-status.md); detailed behavior remains with
the subject owners in the [authority map](#authority-map).

## Purpose

P is an open-source, self-contained, extensible control plane for running and
governing concurrent streams of agent-driven software development. Each
instance composes familiar developer interfaces—such as Git, containers,
terminals, and service protocols—and exposes stable plugin contracts so
developers and agents can inspect, adapt, and extend it without depending on a
centralized P service or replacing existing tools.

Self-contained means that an instance independently owns its control state and
core lifecycle. It may use optional external integrations, but it does not
require another P instance or a mandatory P-hosted service.

Open-source means that P core and the bundled first-party implementations
required for a complete, independently operable instance remain available
under an open-source license. Optional proprietary integrations may add
capabilities, but they cannot become prerequisites for P's core lifecycle.
[The repository license](LICENSE) owns the current license terms.

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
explicit; agents and the plugins that run inside sessions also remain
contained.

**Success signal:** Agents, and plugins that run inside sessions, cannot exceed
the session's grants. A plugin runs on the host only when trusted host
configuration selects its exact, protected package; it is trusted code with
P's authority, and P sends it only what its capability defines. Failures do
not silently widen authority, and destructive actions explain what will be
lost before proceeding.

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
independently. Trusted developer configuration controls which exact package is
activated for which capability, after which it can operate autonomously:
inside a session within the session's grants, or on the host as trusted code.

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
  configuration controls plugin activation by exact package; an activated
  plugin may operate autonomously, inside a session within its grants or on
  the host as trusted code with P's authority.
- Existing systems retain authority for source, execution, process
  supervision, terminal behavior, and optional external services. P composes
  and governs their relationships.
- Independent P instances may exchange durable work through ordinary external
  systems without sharing control-plane authority.

## Authority map

| Subject | Authority | Scope |
|---|---|---|
| Project guidance | This document | Enduring purpose, goals, non-goals, tenet status, project model, and authority ownership |
| Licensing | [LICENSE](LICENSE) | Current license terms for P's source distribution |
| Product direction | [docs/PRODUCT.md](docs/PRODUCT.md) | Product vision, target users, value, strategy, bets, outcomes, strategic boundaries, assumptions, risks, and open strategic questions |
| Domain language | [GLOSSARY.md](GLOSSARY.md) | Canonical P terms and boundaries |
| Product summary | [README.md](README.md) | Human-facing summary; not a competing contract |
| Documentation rules | [docs/AGENTS.md](docs/AGENTS.md) | Normative ownership and document roles under `docs/` |
| Architecture | [docs/architecture/README.md](docs/architecture/README.md) | Current system structure: major parts, responsibilities, boundaries, permitted dependencies, state ownership and cross-boundary guarantees |
| TUI change and validation workflow | [docs/tui-validation-workflow.md](docs/tui-validation-workflow.md) | How TUI changes and experience-validation claims are reviewed, validated and recorded |
| Archived design record | [docs/archive/README.md](docs/archive/README.md) | Design, status and plan documents and the interface rework record as of 471a3eb; historical evidence, not authority |

## Open directions

Open directions preserve possibilities; they are not commitments, requirements,
or approved plans.

- MVP must prove the first-class plugin goal through a basic working
  composition of secure first-party implementations. The package and
  activation contract is defined in [plugin contract](docs/archive/plugin-contract.md);
  the default composition, executable methods and managed CLI installation
  have automated validation. Authenticated Codex acceptance remains a manual
  gate; authenticated testing never blocks the browser's fixture-free lifecycle validation.
- The [session-browser prototype](.prototype/tui-options/DECISIONS.md) records
  the reviewed layout and controls guiding the live API-client browser. The
  [user guide](docs/archive/user-guide.md) distinguishes implemented navigation from
  prototype-only agent inventory and in-terminal management popups.
- Additional runtime environments, multi-user operation, service
  orchestration, richer event handlers, and optional multi-instance
  coordination may be explored while preserving the confirmed guidance.

The repository contains a CLI-first control plane with serial NixOS/Incus VM
validation within its documented support boundary.
[Implementation progress](docs/archive/implementation-progress.md) records the evidence
and pending manual authenticated Codex acceptance.
[Development validations](docs/archive/development-validations.md) identify
the evidence required before support claims are made.
