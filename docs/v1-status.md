# P — V1 status

Current snapshot of how complete and internally settled the P v1 design is.

> **Status: non-normative snapshot, reviewed 2026-08-19.** Subject-specific
> design documents remain authoritative. Update this file when a major decision
> changes or a meaningful implementation milestone lands. Track individual
> remaining items in [missing pieces](missing-pieces.md).

## Executive state

P has a coherent final product model and unusually detailed lifecycle and
security boundaries. The repository is still in the design phase: it contains
documentation but no production Go implementation.

The design is ready to become an implementation plan after three areas are
closed or explicitly scoped:

1. the V1 Nix artifact and writable-store model;
2. project registration plus retained/unassigned branch lifecycle; and
3. the initial TUI vertical slice.

Only the first is a core execution-architecture conflict. Most other remaining
work is concrete implementation, schema definition, and real-machine
validation rather than product discovery.

## Document authority

The nearest [AGENTS.md](AGENTS.md) establishes these roles:

| Document class | Role |
|---|---|
| Subject design | Normative authority for that subject |
| [README](../README.md) | Product summary and design-document index |
| [FAQ](FAQ.md) | Explanations and tradeoffs, not contracts |
| [PR](PR.md) | Working-backwards product narrative |
| [Prior art](prior-art.md) | Dated external landscape, non-normative |
| [Development validations](development-validations.md) | Evidence gates that run alongside development |
| [Nix workflow validation](nix-project-workflow-validation.md) | Homelab/Nix experiment plan, not a product decision |
| [Missing pieces](missing-pieces.md) | Remaining-work tracker, not a second specification |

The old pre-implementation clarification tracker was removed after its accepted
decisions were incorporated into the subject authorities. Open work now lives
in `missing-pieces.md`.

## Settled product model

### Instance and cross-machine behavior

- One P instance has one daemon, SQLite registry, Git server, and set of local
  runtime providers.
- A daemon manages only machinery belonging to its own instance.
- Instances do not discover, address, or synchronize with one another.
- A configured ordinary Git origin is the only V1 cross-machine source
  handoff. A project without one is intentionally local-only.

Authority: [README](../README.md#instance) and
[communication boundaries](communication-boundaries.md#ssh-roles).

### Projects, sessions, and branches

- A project is the complete repository path on the P Git server.
- A session has an immutable UUID and owns exactly one logical `(project,
  branch)` assignment until discard or delete.
- The mutable human name is the real branch name; it is unique only inside its
  project.
- Every session begins from committed source and immediately owns a new branch.
- There are no scratch sessions and no durable `session.base_commit` field.
- Rename changes the real Git ref while preserving UUID and runtime identity.
- The runtime workspace is a standalone clone; linked host worktrees are not
  the session storage model.

Authority: [session lifecycle](session-lifecycle.md#identity-and-retained-state)
and [runtime isolation](runtime-isolation.md#runtime-owned-storage).

### Git and origin authority

- Each project has one P bare repository.
- A session principal reads ordinary project branches and writes only its
  assigned branch, fast-forward-only by default.
- The host P principal is read-only.
- The daemon alone creates, renames, guards, and deletes P refs.
- Sessions receive no configured origin remote or user origin credential.
- Origin refresh/publication runs host-side with the user's normal OpenSSH
  authority.
- Publication is explicit, single-ref, and create-or-fast-forward only.

Authority: [communication boundaries](communication-boundaries.md#p-git-data-plane)
and [origin communication](communication-boundaries.md#origin-communication).

### Lifecycle and recovery

- Create, start, attach/detach, rename, stop, discard, delete, repair, and
  abandonment have defined semantics.
- Durable mutations use persisted operations, commit points, idempotency, and
  authority reconciliation.
- Stop retains runtime-owned state but terminates processes.
- Discard removes the runtime/session and retains an unassigned branch.
- Delete also deletes the assigned P branch after loss confirmation.
- Missing and unreachable runtimes follow different recovery paths.
- P never automatically reclaims sessions, branches, or orphan records.

Authority: [session lifecycle](session-lifecycle.md).

### Runtime and grants

- `local-container` is the only V1 runtime backend.
- Podman is preferred; rootless Docker requires the same conformance evidence.
- Project-scoped trusted host policy is snapshotted at session creation.
- Repositories cannot request P grants or session behavior.
- Fixed runtime paths separate workspace, home, P endpoints, runtime kit,
  external grants, and runtime-owned data.
- V1 supports named filesystem mounts, `public-egress`/`none`, fixed P Git and
  session RPC, and optional Bifrost inference.
- Privileged mode, devices, published ports, host/LAN access, ambient host
  credentials, and engine sockets are outside V1.

Authority: [runtime isolation](runtime-isolation.md).

### Communication and presentation

- Git carries source objects, commits, and refs.
- NDJSON-RPC over Unix sockets carries lifecycle, configuration, operation
  progress, status, and subscriptions.
- Attachment carries validated argv and terminal bytes separately from RPC.
- Linux clients support local Unix and client-initiated SSH-to-Unix transport.
- The TUI is a thin RPC client, not a lifecycle authority.
- The overview uses runtime condition, live attachment count, and one nullable
  latest unattended agent condition.
- Entering clears the unattended condition; events while attached are not
  retained or notified.

Authority: [communication boundaries](communication-boundaries.md),
[session observability](session-observability.md), and
[technology stack](technology-stack.md).

### Interactive hosting and model access

- The configured command may be Bash, Codex, Claude Code, or fixed custom argv.
- Tmux is the default persistent interactive host; `direct` is the minimal
  non-persistent implementation.
- P does not model panes, conversations, subagents, or services.
- Bifrost is an optional independent model gateway.
- A model-enabled session receives one UUID-bound virtual key; upstream keys
  remain in Bifrost.
- OpenAI-compatible Codex access is phase one; Anthropic-compatible Claude Code
  access is phase two.

Authority: [environment building](environment-building.md#environment-activation)
and [model gateway](model-gateway.md).

## Current unresolved areas

### Blocking design decisions

| Subject | Current state | Consequence |
|---|---|---|
| Nix session execution | Closure-only authority, project-store validation proposal, and private layered-image/store candidate differ | Cannot finalize environment/runtime implementation plan |
| Project/ref lifecycle | Identity is settled; registration, project removal, and unassigned-branch management are incomplete | `p .` and post-discard management lack a full contract |
| Initial TUI slice | Framework and thin-client rule are settled; screens/actions/terminal handoff are not | UI implementation has no bounded first milestone |

### Implementation-owned contracts

The exact configuration schema, SQLite schema, RPC catalog, `AttachSpec`, state
layout, engine command mapping, Git listener setup, artifact manifest, and
dependency pins are intentionally not final prose contracts yet. They should be
defined with their first implementation and recorded in generated or concise
reference documentation.

### Evidence gates

Rootless networking, isolated Nix realization, session-socket restart,
Bifrost, agent hooks, dependency pins, and runtime-engine conformance remain
unvalidated. These gates block only their affected support claims, as defined
in [development validations](development-validations.md).

## Document health

- The README, FAQ, communication, lifecycle, runtime, observability, model, and
  technology documents agree on the core project/session/Git model.
- The lifecycle and runtime authorities are mature enough to guide interfaces
  and tests.
- The environment authority must be reconciled after the Nix decision.
- The broad Nix workflow validation is useful but is not yet a practical
  executable guide.
- Prior art is explicitly dated 2026-08-13 and must be refreshed before using
  volatile product claims in a later release.
- P's own license remains a direction—Apache-2.0 preferred, MIT acceptable—not
  a final decision.

## Recommended resume order

1. Resolve the Nix image/store decision using the homelab workflow as the
   acceptance fixture.
2. Incorporate that result into environment building, runtime isolation,
   technology stack, README, FAQ, and validations.
3. Define project registration and retained-ref lifecycle.
4. Write the initial TUI interaction contract and choose the first vertical
   slice.
5. Convert [missing pieces](missing-pieces.md) into an ordered implementation
   plan with parallel validation tracks.
6. Implement the state/RPC/Git skeleton before attaching real environments and
   runtimes.

## Snapshot conclusion

The product idea is not broadly unclear. The identity, Git authority,
lifecycle, isolation, transport, status, and gateway boundaries are settled.
The immediate work is to close one environment architecture decision, fill two
product-operation gaps, and then translate the existing contracts into a
deliberately narrow first implementation.
