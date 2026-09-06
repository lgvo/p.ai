# P — MVP status

Current snapshot of P's design and implementation readiness.

> **Status: non-normative snapshot, reviewed 2026-09-02.**
> [Project guidance](../PROJECT.md) owns enduring direction; subject design
> documents remain authoritative for detailed behavior.
> [Missing pieces](missing-pieces.md) tracks implementation work.

## Executive state

P has a coherent MVP behavior and lifecycle model but no production
implementation. Project, session, runtime, environment, communication, and
observability contracts are assigned to explicit owner documents; the gateway
owner retains a post-MVP design. The newly confirmed plugin composition model
requires the implementation architecture to be reconciled before work begins.

The exact TUI layout, navigation, key map, and smallest initial screen remain
open pending a prototype. Plugin authoring, installation, composition, and
approval interactions also remain open pending their dedicated design.

Concrete schemas, adapters, tests, packaging, and real-machine evidence remain
implementation work. They should narrow unsupported claims without reopening
the product model unless evidence disproves an invariant.

[Product direction](PRODUCT.md) requires MVP to prove P's composable plugin
model through secure first-party defaults for Incus runtime support, the tmux
persistent host, Git source and session access, Nix environment preparation,
structured file-event logging, and the Codex adapter. The usable public
interfaces and this basic composition are sufficient for MVP; a separate
agent-authored plugin is not a release gate. The present Go interfaces,
systemd contract, and event handler describe behavioral inputs rather than an
approved public plugin architecture. Plugin packaging, process model,
isolation, transport, compatibility, capabilities,
installation/approval UX, and composition remain unresolved and require the
technology design to be reconciled before implementation.

## Settled model

### Instance, projects, and Git

- One P instance owns one daemon, SQLite registry, P Git server, and confined
  local Incus execution project. Instances do not federate.
- Projects are created explicitly from an SSH origin or as blank local
  repositories. P does not register/import host checkout locations.
- Origin create/add/change commits only after successful contact. Removing an
  origin explicitly makes the project local-only.
- A blank project or contacted empty origin receives one bootstrap session on
  unborn `main`; its first push creates the ref. Later sessions start from
  committed P/origin source and own a new real branch.
- A session has an immutable UUID and one `(project, branch)` assignment.
  Rename changes the P/workspace branch while retaining identity.
- Session Git updates are unconditionally fast-forward-only. The host P key is
  read-only; the daemon alone mutates P refs. Rewriting already-recorded P
  history is an outside host Git operation.
- Retained branches are first-class project resources with
  list/source/fetch/rename/fast-forward-publish/loss-preview/delete operations.
- **Delete project and all P data** uses aggregate preflight, confirmed live-
  attachment termination, a minimal durable tombstone, and idempotent
  ensure-absent retry. It has no rollback/recovery-mode state machine.

### Runtime, environment, and policy

- Incus system containers are the only MVP runtime. Each session has a private
  root, `/nix`, workspace, home, credentials, and narrow endpoints.
- A restricted builder realizes a committed default Nix devShell into a
  verified project-scoped Incus image. No devShell—or bootstrap without a
  commit—uses the P base image.
- Every base image implements the systemd contract: `p-session.target` starts
  `p-interactive.service`; the service activates the environment and supervises
  one persistent host; `/usr/libexec/p/attach` connects temporary terminals.
- Tmux is the default persistent host. Detach/switch/transport loss does not
  affect it. Host clean/fail exit is journaled and stops the container; Start
  launches it again.
- Trusted project policy is snapshotted immutably at creation. Comparison is
  `current`, `outdated`, or `invalid`; outdated warns and offers guided
  recreation, invalid blocks Start, and P never mutates grants live.
- Network starts at `none`; public egress and filesystem access require typed
  trusted grants. Repositories cannot widen authority.

### Lifecycle, status, and events

- Create, Start, Attach/Detach, Rename, Stop, Discard, Delete, Repair, Abandon,
  retry, and restart reconciliation have defined outcomes.
- Exact Retry preserves the failed creation's immutable request and operation
  identity while cleaning verified partial derived resources. **Try again with
  changes** is one integrated superseding Create with new identity, not retry.
- The public status model has four independent facts:
  `session_condition`, `attached_count`,
  `latest_unattended_condition`, and `policy_condition`.
- Session conditions are `creating`, `starting`, `ready`, `stopped`, `missing`,
  `unreachable`, `discarding`, and `deleting`. There is no generic public
  `removing` or separate runtime/startup-readiness pair.
- Confirming the first attachment clears unattended condition. Failed attach
  does not; reports while attached are not retained as unattended status.
- P emits typed, versioned reduced events through one `EventHandler` seam. MVP
  appends redacted NDJSON locally. Handler failure never rolls back operations,
  and events are not state truth, replay, acknowledgement, or notification
  protocol.

### Communication and agent support

- Git carries source; NDJSON-RPC carries control/status; attachment carries a
  fixed validated entrypoint and terminal bytes; event handlers receive
  reduced P events.
- MVP clients use the local Unix transport. P's client-initiated SSH-to-Unix
  transport is post-MVP; Git and origin operations may still use SSH.
- Codex is the only supported MVP agent adapter. It runs as an ordinary command
  and the user authenticates it within the session's private home; P does not
  inject or manage host Codex or OpenAI credentials. Networked use requires the
  project's validated `public-egress` grant.
- Bifrost model-gateway integration is post-MVP.
- P MVP does not orchestrate project services. Checks and attempts remain
  reserved future concepts.

## Intentionally deferred interaction decision

The production TUI is a thin RPC client, but its exact layout, navigation,
keys, and initial slice will be chosen after a fixture-backed prototype tests:

- cross-project overview density and status/policy warnings;
- project/session creation and changed-settings replacement;
- attach, detach, and switching;
- retained-branch actions; and
- destructive previews, project-wide deletion, partial progress, and retry.

This deferral covers presentation only. It does not defer the underlying RPC or
lifecycle semantics.

## Evidence still required

- confined Incus operation without administrative authority;
- Nix image/private-root correctness across claimed hosts/storage drivers;
- network isolation, filesystem-grant ceilings, and endpoint containment;
- systemd host supervision, diagnostics, container shutdown, and attachment
  teardown across client crash and daemon restart;
- lifecycle and bulk project-deletion convergence at every crash point;
- Git principal/ref enforcement and origin race/unknown-outcome behavior;
- immutable-policy comparison and guided recreation;
- Codex hook mappings, session-local authentication behavior, and the NDJSON
  event handler; and
- performance/capacity evidence for supported configurations.

See [development validations](development-validations.md) for the gated tests.

## Recommended implementation order

1. reconcile and implement the trusted-core, plugin-framework, state,
   configuration, RPC, events, and fake-plugin foundations;
2. implement project/Git/origin and session lifecycle skeletons through the
   selected plugin contracts;
3. implement and validate Incus, the systemd base-image contract, and Nix
   environment images;
4. integrate attachment, observability, policy comparison, and recovery;
5. integrate and validate the Codex adapter and session-local authentication;
   and
6. prototype the TUI, record the chosen interaction contract, then implement
   progressively complete product slices.
