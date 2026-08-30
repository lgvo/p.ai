# P — restart prompt and project handoff

> **Status: temporary handoff, 2026-08-30.** Use this file as the initial
> prompt for a new conversation. It summarizes the current project and pending
> review; it is not a product-design authority. The subject documents named
> below remain authoritative. Remove this file after its pending decisions have
> been incorporated.

## Instructions for the next conversation

Work with me on the P design in this repository. Read the nearest `AGENTS.md`
before acting. In particular, when I ask a question, answer it without changing
files. Do not commit unless I explicitly ask.

We are reviewing the remaining design issues one at a time. For each item:

1. explain the problem in concrete terms;
2. give the simplest recommended resolution;
3. explain why;
4. identify realistic alternatives and their tradeoffs; and
5. wait for my decision before changing documents.

Simplification means removing unnecessary work, state, protocols, and duplicate
contracts. It does not mean deleting useful reusable interfaces or closing off
reasonable future implementations.

The working tree contains a large coherent documentation rewrite that has not
yet been committed. Preserve it. The current branch is
`docs/working-backwards`, based on commit `c0f0935`.

## What P is

P is a local-first, open-source control plane for isolated development
sessions, organized around Git projects and branches. It is intended for one
developer using machines they control. Its primary interface is a terminal UI
that shows work across repositories, makes attachment quick, and surfaces the
latest supported agent signal received while the user was away.

P is not an agent orchestrator, task graph, hosted control plane, or machine
federation system. Agents, shells, and other interactive programs remain
ordinary commands running inside isolated sessions.

The product model is:

```text
P instance
  ├─ Git project: complete repository path
  │    ├─ session UUID ↔ one assigned branch
  │    │    └─ one local isolated runtime
  │    └─ retained unassigned Git branches
  ├─ SQLite registry
  ├─ local P Git server
  └─ one confined local Incus execution project
```

## Settled design decisions

### Instances and machines

- One P instance owns one daemon, SQLite registry, Git server, projects,
  sessions, and configured local Incus execution project.
- A daemon does not connect to another host to manage runtimes.
- P instances do not discover, address, synchronize, or federate with one
  another.
- When two machines share the same ordinary Git origin, work moves by explicit
  Git publication from one instance and fetch on the other.
- A project with no origin is local-only. Every origin-dependent feature is
  bypassed rather than reported as broken.
- Linux is the V1 daemon and runtime platform.
- Linux clients support direct Unix-socket and client-initiated SSH-to-Unix
  communication from day one. Native macOS and Windows clients are later, but
  use the same communication model.
- SSH is client transport, Git transport, and host-origin transport. It is not
  a runtime backend, and the daemon never initiates SSH to another P host.

### Projects, branches, and sessions

- A project is the complete repository path on the P Git server, such as `p`
  or `lgvo/p`. Optional path namespaces are supported but are not separate V1
  account or policy objects.
- A session has an immutable UUID and owns exactly one `(project, branch)`
  assignment while it exists.
- The actual Git branch name is the human-facing session name. There is no
  separate mutable display-name identity.
- `(project, branch)` is unique within a P instance. Equal branch names in
  different projects are valid.
- There are no scratch sessions.
- Creation always selects committed source and immediately creates a new real
  branch. If no useful name is supplied, P suggests a timestamp with collision
  handling.
- A source branch such as `main` may seed a session, but the session must own a
  newly named branch rather than assigning `main` itself.
- There is no durable `session.base_commit`. Once creation makes the branch,
  Git ancestry and its current tip are authoritative.
- Rename preserves the UUID and changes the real P-server and workspace branch
  refs. It never renames or deletes an origin branch.
- One session owns at most one runtime. The V1 runtime is tagged by the session
  UUID, not by the mutable branch name.

### Git authority and publication

- Each P project is a bare Git repository and is the source of truth for
  session commits and refs.
- Every session receives a distinct SSH key scoped to its UUID, project, and
  assigned branch.
- A session may read ordinary branches in its project and write only its
  assigned branch.
- The host P Git key is read-only. The daemon alone creates, guards, renames,
  and deletes P refs.
- Session runtimes receive no origin remote or origin credentials from P.
- Host-side origin refresh and publication use the user's existing OpenSSH
  credentials and configuration.
- Publication is an explicit host-authorized RPC action. It is not proof of
  human presence because `p api` may automate it.
- V1 publication creates or fast-forwards one explicit origin branch. It never
  force-pushes, deletes, renames, or automatically updates origin refs.
- Publication relies on fresh origin observation plus Git's normal atomic
  result. There is no hidden publication ref, generation namespace, or
  publication ledger.
- If an outcome is unknown because transport was lost, P reports it and waits
  for another explicit request. The later request fetches current origin state
  and safely evaluates the idempotent desired result again.
- Checks and attempts are future ideas only. Their possible Git/RPC division
  is reserved, but they have no V1 scheduler, protocol, or lifecycle.

### Runtime and isolation

- Incus is the only V1 runtime backend and is suitable for both the MVP and V1.
- P uses a pre-provisioned confined local Incus user project. P must not have
  the host-root-equivalent administrative Incus socket.
- Each session is one unprivileged Incus system container with a private root,
  `/workspace`, `/home/p`, `/nix`, runtime state, and credentials.
- Incus owns instances, images, storage, state, and runtime operations. SQLite
  stores P intent and indexes, not duplicate Incus truth.
- Incus VMs and Kubernetes are future placement options. Remote Incus,
  clustering, raw Docker/Podman backends, and SSH-host backends are not V1.
- Runtime networking starts with `none`. An optional `public-egress` profile
  must allow outbound public traffic while denying host, LAN/private,
  link-local, metadata, sibling, Incus, and undeclared service access over IPv4
  and IPv6.
- P Git, session RPC, and optional Bifrost access use narrow session endpoints;
  they do not imply general host-network access.
- Host filesystem integrations are typed grants in trusted host
  configuration, constrained by both P validation and the Incus project
  ceiling. Repositories cannot request mounts or other host authority.
- V1 policy is project-scoped. Branch-specific `{project}/{branch}` policy is
  reserved for later.
- Services are outside P V1. A process may run services inside a session, but P
  does not declare, supervise, restart, health-check, or model them.

### Nix environments

- V1 uses only the repository's conventional pure default Nix devShell. Nix is
  for building the container environment and dependencies, not for defining P
  session instances or P policy.
- When no default devShell exists, P uses its immutable base image without
  guessing a toolchain.
- A disposable restricted Incus builder consumes immutable committed source,
  realizes the devShell, captures validated activation material, and publishes
  a private immutable Incus environment image.
- The cached image contains a coherent starting `/nix` store/database. Each
  session receives a private writable root derived from it.
- Sessions never mount the host Nix store or daemon and never share a writable
  Nix database.
- Later Nix builds inside a session remain private to that session, survive
  stop/start, and disappear with its runtime.
- Environment images are project-scoped because retained Nix store paths may
  contain committed source-derived material.
- Physical sharing and size depend on the Incus storage driver. The design
  makes no universal deduplication or cold-build performance claim.
- `nix print-dev-env --json` is experimental and therefore has a named,
  pinned compatibility gate.
- `EnvironmentBuilder` and opaque `EnvironmentHandle` remain reusable seams,
  but Dockerfile, OCI, devcontainer, and other providers are not specified in
  V1.

### Lifecycle

- Create, start, attach/detach, rename, stop, destructive preflight, discard,
  delete, repair, and abandonment have dedicated contracts.
- Cross-authority changes use persisted workflow intent only where recovery
  needs ordered phases. Incus-owned start/stop operations are not duplicated as
  P workflow engines.
- Stop retains the instance and writable state but terminates its processes,
  including tmux and agents.
- Discard removes runtime and session identity but retains the P branch as an
  ordinary unassigned ref.
- Delete also removes the confirmed assigned P branch.
- P never automatically reclaims sessions, branches, images, or orphan
  records based only on age.
- Destructive preflight examines runtime-local and Git loss without activating
  repository code.
- Missing and unreachable runtimes are different. Missing means local runtime
  state is already unavailable; unreachable blocks ordinary destruction.
- Abandonment is the explicit override for unreachable machinery and leaves a
  tombstone so later machinery can be recognized and contained.
- A running established runtime whose startup activation failed is
  `not_ready`. Another Start returns stable `stop_required`; recovery is
  explicit Stop followed by Start with a new startup generation.
- During creation, startup readiness remains `inactive`. Activation failure is
  represented by the durable creation operation. Retry first stops a running
  partial runtime through `stopping-partial-runtime`, then records a new
  generation in the same creation operation.
- Startup markers are written by P-owned code into a path the session user,
  shell hook, and interactive command cannot modify. Each transition uses
  write, file `fsync`, atomic rename, and directory `fsync`.

### Interactive hosting and attachment

- Tmux is the default persistent `InteractiveHost`; `direct` is the minimal
  non-persistent implementation. P session identity does not depend on tmux.
- The interactive command may be Bash, Claude Code, Codex, or fixed custom
  argv.
- Attachment is a host RPC decision followed by a structured attachment
  channel. Terminal bytes do not pass through lifecycle JSON-RPC.
- The daemon first returns a short-lived one-use pending token and structured
  `AttachSpec`.
- A trusted host helper establishes the channel, confirms the token over a
  dedicated RPC connection, and then owns the confirmed attachment lease.
- Only confirmed attachment increments presence and clears the latest
  unattended condition.
- While the daemon is reachable, the helper retains its lease until teardown
  finishes. Client, carrier, SSH, helper, or lease loss initiates teardown
  without relying on cooperative client cleanup.
- Tmux teardown removes only the affected attachment. Direct must terminate
  and wait for its command when the exec channel disappears.
- A fresh `InteractiveHost.Check` occurs before attachment and is independent
  of startup readiness.

### Observability

The overview intentionally uses four independent facts:

```text
runtime_condition
startup_readiness
attached_count
latest_unattended_condition
```

- Runtime condition comes from registry state plus current Incus inspection.
- Startup readiness is generation-bound and records whether startup
  preparation succeeded. It does not claim that later child processes remain
  alive.
- Attachment presence counts confirmed live helper leases, not requested
  attachments.
- The latest unattended condition is one nullable last-write-wins semantic
  agent report, not a history, attention set, or authoritative current agent
  state.
- Confirming the first attachment clears it. While attached, semantic events
  are validated but are neither retained as overview status nor notified.
- P does not inspect terminal output, tmux panes, or process names to infer
  agent meaning.
- Claude Code and Codex adapters are versioned event mappings that require real
  trace validation.

### Communication

- Git carries commits, objects, and refs.
- Host RPC carries project/session lifecycle, configuration, status, and
  subscriptions.
- Session RPC carries self-identity, effective capabilities, and bounded
  status reports. It cannot publish or control lifecycle.
- Attachment carries one validated argv path and terminal bytes.
- Bifrost HTTP carries model traffic.
- Notification sinks receive selected reduced unattended transitions.
- The daemon does not use RPC as a source-transfer protocol and does not return
  shell strings or arbitrary command execution.

### Model gateway

- Bifrost is the selected independently configured local gateway. P does not
  implement or proxy model APIs.
- Bifrost owns provider credentials, models, aliases, routing, limits, usage,
  MCP, Skills, and virtual-key policy.
- P stores only the endpoint plus one persisted virtual key per enabled session
  UUID and manages that principal's lifecycle.
- OpenRouter is the initial hosted upstream. Local services such as Ollama or
  vLLM may sit behind Bifrost without granting sessions general LAN access.
- Phase one exposes Bifrost's OpenAI-compatible API and validates Codex. The
  Anthropic-compatible API and Claude Code are phase two.
- Sessions receive no administrative or upstream provider credential.
- V1 relies on validated native Bifrost authorization: every inference request
  requires the session key, and the same key must be rejected by dashboard,
  management, governance, logs, MCP, Skills, and all other non-V1 routes.
- This native-enforcement assumption is an early feasibility gate. If the
  pinned Bifrost release cannot enforce it, optional model access remains
  disabled. Git, runtime, RPC, TUI, and projects without model grants continue.
- An L7 proxy is not V1 unless separately designed.
- Bifrost can remain the first Kubernetes gateway. Envoy AI Gateway is a later
  option for shared cluster ingress, distributed policy, or inference-fleet
  routing, not a requirement merely because Kubernetes is used.

### License

- P is Apache-2.0 licensed. The repository currently contains an untracked
  standard `LICENSE` file that belongs with the documentation changes.

## Document authority map

- `README.md`: product summary; not normative.
- `docs/PR.md`: working-backwards product narrative; not normative.
- `docs/FAQ.md`: explanations and tradeoffs; not normative.
- `docs/prior-art.md`: dated external landscape and positioning; not
  normative.
- `docs/session-lifecycle.md`: session identity, operations, concurrency,
  destructive behavior, and recovery.
- `docs/communication-boundaries.md`: Git/RPC/SSH/attachment/build/gateway
  channel division and audiences.
- `docs/runtime-isolation.md`: Incus placement, grants, storage, networking,
  launcher, runtime helper, and backend guarantees.
- `docs/environment-building.md`: Nix environment selection, isolated build,
  activation, images, cache, and collection.
- `docs/session-observability.md`: status fields, event reduction,
  presentation, and notifications.
- `docs/model-gateway.md`: Bifrost policy, principals, surface, and future
  gateway direction.
- `docs/technology-stack.md`: implementation choices, reusable interfaces,
  dependencies, and licensing policy.
- `docs/development-validations.md`: validation gates to execute alongside
  development.
- `docs/nix-project-workflow-validation.md`: manual Incus/Nix experiment using
  the real homelab repository.
- `docs/v1-status.md`: non-normative readiness snapshot.
- `docs/missing-pieces.md`: non-normative remaining-work tracker.

## Known missing design documents

Two product areas were already identified as missing before a complete V1
implementation plan:

1. **Project/repository lifecycle:** registration, repository creation and
   initial seeding, registered source locations, repeated registration,
   project removal/rename posture, and lifecycle of branches retained after
   discard.
2. **Initial TUI vertical slice:** first screen and navigation, creation and
   progress flow, attachment handoff and return, errors, confirmations, key
   actions, and the smallest useful milestone.

Implementation-owned details such as the configuration schema, SQLite schema,
RPC catalog, `AttachSpec`, exact Incus adapter, base-image build, installation,
backup/restore, and dependency pins remain tracked in `docs/missing-pieces.md`.
They should be defined with implementation rather than expanded into speculative
design frameworks.

## Latest whole-document review

The last review found that the core architecture is coherent, local Markdown
links and anchors are valid, code fences are balanced, and `git diff --check`
passes. It also found the following issues. No fixes for these findings have
yet been applied.

### 1. Normative duplication

**Problem:** detailed attachment, startup, and gateway contracts are repeated
across several design documents even though `docs/AGENTS.md` says each
normative rule should have one owner. This increases maintenance and has
already allowed semantic drift.

**Recommendation already presented, not yet approved:** retain the current
document set, but give each rule one owner:

- lifecycle owns ordering, transitions, locks, and commit points;
- runtime owns backend, launcher, helper, tmux, and direct guarantees;
- communication owns channel division and audiences;
- observability owns field meaning and reduction;
- environment owns environment/image behavior;
- gateway owns Bifrost behavior; and
- technology stack owns choices and interfaces.

Other design documents should summarize and link. Do not create a large new
contract document or consistency framework. Apply this cleanup incrementally
while resolving the concrete issues below, then make one final duplication
pass.

### 2. Direct has two startup models

**Problem:** `runtime-isolation.md` and `environment-building.md` say startup
starts the configured interactive command, while `technology-stack.md` says
`direct` runs its command for one attachment. Creation itself explicitly does
not attach.

**Simplest recommendation:** make behavior host-specific. Tmux preparation
creates its UUID-owned server/session and starts the command during startup.
Direct preparation only validates launchability; its attachment argv starts
the command for that channel. Direct startup readiness therefore means ready
to launch, not command already running.

**Alternative:** start direct's command during session startup and attach to it
later through some persistence mechanism. That recreates part of tmux and
defeats the purpose of the minimal direct implementation.

### 3. Failed creation retention and retry

**Problem:** a failed post-commit creation remains `creating`, must expose its
phase, retries the same operation, and may be replaced by discard/delete. But
operation status includes terminal `failed`, terminal records have bounded
retention, and destructive operations are blocked while another mutation is
incomplete. The operation cannot expire because it is the only durable retry
and diagnostic authority for that active session.

**Simplest recommendation:** an unresolved post-commit creation failure is
`blocked`, not terminal. The operation remains durable while the session row
references it. Retry changes the same operation back to running. Discard or
delete explicitly supersedes it under the lifecycle lock and starts the chosen
removal workflow. Only completed, safely cancelled, or superseded records enter
terminal retention.

**Alternative:** create a new operation for every retry. That requires parent
operation chains and another source of truth for which retry owns the creating
session; it is unnecessary.

### 4. Bifrost availability versus startup readiness

**Problem:** the gateway document says a model-enabled session is not ready
until its key and boundary are verified, but also says gateway failure affects
model access rather than attachment. Startup readiness includes endpoint
validation and is required for attachment, so it is unclear whether a later
Bifrost outage makes the whole session `not_ready`.

**Simplest recommendation:** distinguish creation-time capability provisioning
from runtime startup readiness. Initial key creation may block creation for a
project that requires model access. The pinned Bifrost boundary is an
instance/integration gate. After establishment, Bifrost unavailability degrades
only model access; it does not rewrite startup readiness or prevent attachment.
Normal Start should not probe Bifrost as part of its readiness marker.

**Alternative:** make model availability mandatory for every Start and attach
of a model-enabled session. This is stricter but unnecessarily makes a shell,
Git workspace, and local tools unavailable during a gateway outage.

### 5. Direct attachment fencing after daemon restart

**Problem:** after daemon restart the old helper detects lease loss and tears
down its direct exec channel, but the restarted daemon has forgotten the old
lease. A different client could receive a new direct attachment before the old
runtime wrapper has actually terminated.

**Simplest recommendation:** direct uses a runtime-side single-owner lock or
equivalent observable wrapper state. A fresh `InteractiveHost.Check` refuses or
waits while an earlier direct wrapper is still alive. The wrapper disappears
when its exec channel is torn down. Tmux does not need this exclusivity because
its capability model may permit multiple attachments.

**Alternative:** tolerate a short overlap after daemon restart. That weakens
the meaning of direct as one command per attachment and complicates status, so
it is not recommended.

### 6. Grant revocation and revalidation

**Problem:** project policy is snapshotted for a session's lifetime, while the
grant abstraction says every grant describes revocation. V1 has no operation
for live mount/network/model-policy narrowing, and config reload applies only
to new sessions.

**Simplest recommendation:** explicitly make an established session's policy
snapshot immutable in V1. Start still revalidates that snapshotted mount
sources and the Incus confinement ceiling remain valid and fails closed if
they do not. Deliberate revocation of an established session requires removing
that session. Live policy reconfiguration is later work.

**Alternative:** add an in-place policy-update lifecycle. That requires
quiescence, loss reporting, rollback, and backend-specific mutation semantics;
it is disproportionate for V1.

### 7. Force-push exception has no owner

**Problem:** some documents say a trusted instance or host policy may grant a
narrow session force-push exception, but the V1 configuration contract and
remaining-work tracker define no such field or lifecycle.

**Simplest recommendation:** block session force-push unconditionally in V1
and reserve a trusted exception for later.

**Alternative:** keep the exception in V1, but then define its exact project
configuration field, snapshot semantics, audit/preview behavior, and server
authorization rules.

### 8. Minor clarifications

- Explicitly map `runtime_condition == unreachable` to
  `startup_readiness == unknown`; the current readiness table could also be
  read as `inactive`.
- Fix “If the Incus cannot be reached” to “If Incus cannot be reached” in the
  FAQ.
- After the substantive findings are resolved, update `docs/v1-status.md` so
  it does not claim that project lifecycle and TUI are the only open product
  contracts prematurely.

## Recommended continuation order

Continue the review one item at a time in this order:

1. finish the decision on normative ownership;
2. decide the direct startup/attachment contract;
3. decide failed-creation operation retention and supersession;
4. decide whether Bifrost outages affect startup readiness;
5. define direct restart fencing;
6. define V1 grant revocation/revalidation posture;
7. decide whether force-push exceptions are entirely deferred;
8. apply the minor clarifications;
9. update the authoritative owner for every accepted decision and reduce
   duplicate normative text elsewhere;
10. review every document again for contradictions and mechanical errors;
11. update `v1-status.md` and `missing-pieces.md`; and
12. then return to the missing project lifecycle and initial TUI documents
    before producing the implementation plan.

After each accepted documentation change, preserve the user's existing work,
run `git diff --check`, validate local Markdown links/anchors and code fences,
and inspect the whole document set again. Do not commit until explicitly asked.

## Current working-tree state

At the time this handoff was written, these existing files were modified:

```text
README.md
docs/FAQ.md
docs/PR.md
docs/communication-boundaries.md
docs/development-validations.md
docs/environment-building.md
docs/missing-pieces.md
docs/model-gateway.md
docs/nix-project-workflow-validation.md
docs/prior-art.md
docs/runtime-isolation.md
docs/session-lifecycle.md
docs/session-observability.md
docs/technology-stack.md
docs/v1-status.md
```

`LICENSE` was untracked and contains the standard Apache License 2.0 text.
This handoff file is also new. These changes belong together as the current
documentation state unless later review deliberately separates them.
