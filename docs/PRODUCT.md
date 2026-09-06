# P — product direction

This document is authoritative for P's product vision, target users, value,
strategy, current strategic bets, intended outcomes, strategic boundaries,
assumptions, risks, and open strategic questions. Project purpose, goals,
non-goals, tenets, and documentation ownership remain in
[project guidance](../PROJECT.md). Detailed behavior and implementation remain
with the subject authorities mapped there.

## Product identity and scope

P is an open-source, self-contained, extensible control plane for an individual
developer running concurrent streams of agent-driven software development. It
makes those streams governable and lets the developer reshape P's workflows
through plugins and integrations authored by developers or agents. Security
and explicit authority make both forms of value trustworthy rather than
constituting a separate product category.

The developer is the governing user. Agents may operate development streams
and author or test extensions, but they do not acquire authority to activate
plugins or grant capabilities. This direction governs the P product rather
than the repositories, agents, infrastructure products, or external services
that P composes.

## Vision

An individual developer can safely delegate many streams of development and
reshape P around their own workflow by asking an agent to build an extension,
without maintaining a fork, rebuilding P core, weakening default security, or
depending on a centralized service.

## Target users and problems

The primary user is an individual developer who uses coding agents for enough
concurrent work that terminals, processes, workspaces, credentials, and
handoffs become difficult to account for.

The current product direction addresses three related problems:

- concurrent agent work is difficult to see, resume, and govern reliably;
- fixed workflow tools make developers conform to the tool or maintain
  brittle private patches and scripts; and
- extensibility commonly becomes unrestricted code execution, complicated
  setup, or permissions that users cannot meaningfully evaluate.

The first problem is represented throughout the current design and motivated
the project. The latter two are confirmed direction from the governing user,
not yet observed behavior from a broader user population.

## Value proposition

Compared with manually combining terminals, containers, source control,
services, scripts, and credentials—or modifying the orchestrator itself—P
provides one place to govern concurrent development streams and change how
that control plane works through agent-authored plugins.

P preserves the familiar interfaces of the systems it composes. The user gets
adaptability without making a P-specific representation, an unrestricted
extension process, or a centralized P service the price of adoption.

Because the core product and bundled defaults are open source, developers can
inspect and modify the control plane without vendor permission and retain a
path to operate it if its original distributor disappears. This is a
meaningful difference from proprietary control planes, but source availability
alone does not supply P's governance or security.

## Product strategy

P pursues that value through an integrated set of choices:

- Keep a small trusted P core responsible for identity, durable control state,
  lifecycle orchestration, policy and grant enforcement, plugin activation,
  recovery, and user confirmation.
- Have P core perform lifecycle work through typed plugin interfaces without
  delegating its authoritative lifecycle or security decisions to plugins.
- Use one plugin framework for discovery, compatibility, activation, grants,
  and lifecycle participation, with capability-specific contracts rather than
  pretending every implementation has the same authority or behavior.
- Make first-party functionality prove the same contracts available to other
  implementations. The current bundled composition and implementation scope
  remain in the [MVP snapshot](mvp-status.md), not in product direction.
- Keep P core and the bundled first-party implementations required for a
  complete, independently operable installation open source. Optional
  proprietary capabilities remain integrations rather than prerequisites.
- Make agent authoring, testing, validation, and explanation of plugins a
  primary product workflow. Registration alone never activates a plugin or
  grants it authority.
- Provide secure first-party defaults automatically so ordinary setup does
  not require manually assembling P or understanding its complete security
  architecture.
- Optimize first for personal workflow adaptation. Portability and sharing
  matter, but marketplace growth is not the current strategy.

### Service composition

P distinguishes capabilities by where they operate and what crosses the
session boundary:

- An internal session service runs within the session's isolation boundary and
  is supervised through the runtime's systemd contract.
- An external session service runs outside the session and is provided through
  an explicitly granted network endpoint with service-native, session-scoped
  authentication. It may be local to the P instance or remotely operated.
- A host-side capability participates in lifecycle work but is not exposed to
  the session.

External services retain authority over their protocols, data, and native
authorization. P governs whether a session receives access, provisions and
revokes the binding, validates its effective scope, and injects it without
turning the binding into general network access.

### Security posture

Security is a product property rather than a configuration mode. P denies
undeclared capabilities, requires explicit escalation, prevents agents and
plugins from self-authorizing, explains requested authority before activation,
and validates effective service scope.

Ease of operation means making those protections the automatic path: setup
establishes secure defaults, failures are actionable, and deliberate advanced
use remains possible without silent containment downgrades.

## Strategic bets and priorities

The current direction rests on four ordered bets:

1. A developer who uses P for real concurrent work can make those streams more
   governable than the terminal, process, and worktree status quo.
2. First-party capabilities built through public plugin contracts can keep the
   extension model useful and honest without making normal setup feel assembled
   from parts.
3. An agent can turn a developer's workflow request into a reviewable,
   maintainable plugin more easily than the developer can maintain a fork or
   bespoke automation.
4. Security enforcement, guided setup, and clear capability explanations can
   make concurrent work and adaptation trustworthy without requiring the user
   to assemble the underlying authorities manually.

Concurrent-stream governance and agent-driven adaptation are both central to
the product direction. Security and a truthful composition boundary constrain
how P delivers them and take priority over the breadth of plugin types or
integrations.

### Validation sequence

The first evidence comes from the governing user operating P for sustained real
work. The personal MVP may be rough or specific to that workflow, but it must
exercise the secure first-party composition and reveal whether concurrent
sessions and the plugin contracts remain useful under ordinary use. Detailed
MVP scope and implementation state remain in the
[MVP snapshot](mvp-status.md).

The first coherent version shared publicly centers on the concurrent-session
experience and includes an end-to-end guide for changing a real P workflow
through a plugin. The guide uses the supported authoring path and an agent to
create or adapt the plugin, then covers testing, developer review, activation,
bounded use, and validation of the resulting workflow change. The validated
path becomes the documentation another user can follow.

Completing that guide demonstrates technical viability, useful public
contracts, and documentability. It does not by itself demonstrate external
demand, ease for unfamiliar users, or long-term maintainability. Those claims
require later evidence from people who reproduce, retain, and adapt the
workflow without depending on the governing user's knowledge.

## Product outcomes and success signals

The strategy is working when:

- the governing user repeatedly relies on P for real concurrent work rather
  than returning to manual terminal and process reconstruction;
- another developer can follow the public guide to produce and validate a real
  workflow change through a plugin without modifying P core;
- developers continue adapting P through plugins instead of maintaining forks
  or private patches after the first guided example;
- agent-authored plugins remain within user-approved capabilities and can be
  disabled or replaced without compromising P's authoritative state;
- a default installation becomes operational securely without requiring the
  user to select and connect plugins manually;
- first-party implementations can be replaced without changing authoritative
  project or session identity and lifecycle semantics;
- users can understand what authority a plugin or service requests before
  granting it; and
- external services remain usable through familiar protocols without gaining
  ambient access to the session or host.

The first signal is sustained personal use. Reproduction by unfamiliar users
is stronger evidence for the public experience and plugin promise. These are
initially qualitative signals; there is not yet enough usage evidence to set
truthful numerical targets.

## Strategic boundaries

- P does not optimize first for a centralized plugin marketplace or an
  ecosystem-growth loop; personal adaptation is the current strategic use.
- Security enforcement, grants, durable identity, lifecycle authority, and
  user authorization do not become replaceable plugin policy.
- Generic network access is not a substitute for declared external service
  bindings.
- Optional proprietary plugins, services, and integrations may extend P, but
  the complete core product does not depend on them or on a proprietary hosted
  service.
- Team and enterprise administration do not drive current product choices over
  the individual-developer experience.

## Evidence and assumptions

Confirmed direction comes from the governing user's decisions and the
established project guidance. Existing design work supports the need for
concurrent-stream governance, explicit authority, isolation, familiar
interfaces, and agent-driven adaptation. The external landscape shows that
basic concurrency mechanics and extension mechanisms are crowded while their
composition remains fragmented. It does not establish demand for P's combined
experience.

There is not yet product-usage evidence. Sustained use by the governing user is
the first intended evidence and can invalidate workflow and implementation
assumptions. It cannot establish broader demand. A reproducible public plugin
guide can then test whether the adaptation path is transferable before wider
behavioral evidence exists.

Open-source status is confirmed direction and is already reflected by the
repository's Apache 2.0 license and product summary. Whether source availability
materially changes adoption or trust remains unobserved.

The direction therefore retains these assumptions:

- individual developers have meaningful workflow differences that plugins can
  address better than scripts, forks, or fixed configuration;
- access to P's source and permission to modify it materially improve trust,
  adaptability, and continuity for target users;
- coding agents can author reliable extensions against stable, inspectable
  contracts;
- users can make informed activation decisions when capability requests and
  consequences are presented clearly;
- first-party implementations can use public contracts without making normal
  setup feel assembled from parts; and
- the underlying host and service security prerequisites can be validated and
  presented without requiring specialist knowledge.

The comparison to Pi is directional evidence for the desired ease of
agent-driven customization, not observed evidence about P users or a validated
feature comparison.

## Strategic risks

- The plugin abstraction could be too narrow for meaningful adaptation or so
  broad that it stops enforcing useful boundaries.
- Agent-authored code could undermine the security promise if isolation and
  capability enforcement are superficial.
- Permission requests could become approval theater rather than informed
  control.
- Requiring every first-party capability to prove a public contract could
  create premature abstractions or lowest-common-denominator behavior.
- Compatibility obligations could make P difficult to evolve.
- Open-source availability may be easy for alternatives to match and may not
  differentiate P unless its governance and adaptation model is meaningfully
  better.
- Incus and other underlying security prerequisites could remain difficult to
  operate despite a guided experience.
- Agent-generated plugins could be difficult for users to evaluate, debug, or
  maintain.
- A successful plugin tutorial could be mistaken for evidence of recurring
  user value when it proves only that one documented path works.
- A public version centered on concurrent sessions could be perceived as
  another cockpit if the workflow-adaptation value remains difficult to
  discover or reproduce.

## Open strategic questions

- Which real workflow change makes the public plugin guide useful enough to
  demonstrate value beyond bundled defaults without predetermining the plugin
  architecture? Resolving this requires pressure from personal use followed by
  observed authoring and validation.
- Can an unfamiliar developer reproduce the guide, understand the resulting
  change and authority, and continue using or modifying the plugin without the
  governing user's help? Resolving this will strengthen or weaken the public
  adaptation promise.
- What behavior demonstrates that users prefer adaptation over scripts, forks,
  or abandoning the tool? Resolving this will strengthen or weaken the value
  proposition and requires usage evidence.
- When should portable sharing become a strategic investment rather than only
  a supported property? Resolving this would change priorities and requires
  repeated evidence of cross-user reuse.
- What evidence shows that capability explanations and secure setup are
  understandable? Resolving this will calibrate the security and ease promise
  and requires usability and security validation.

Plugin packaging, execution isolation, transport, manifests, compatibility,
installation, composition configuration, and service-binding protocols remain
downstream design questions rather than product-direction decisions.
