# P — V1 status

Current snapshot of P's design and implementation readiness.

> **Status: non-normative snapshot, reviewed 2026-09-01.** Subject design
> documents remain authoritative. [Missing pieces](missing-pieces.md) tracks
> implementation work.

## Executive state

P has a coherent V1 architecture and no production implementation. Project,
session, runtime, environment, communication, observability, and gateway
lifecycle contracts are now assigned to explicit owner documents. The only
product interaction deliberately left open is the exact TUI layout,
navigation, key map, and smallest initial screen; those choices require a
prototype rather than more architecture speculation.

Concrete schemas, adapters, tests, packaging, and real-machine evidence remain
implementation work. They should narrow unsupported claims without reopening
the product model unless evidence disproves an invariant.

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

- Incus system containers are the only V1 runtime. Each session has a private
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
- Network starts at `none`; public egress and filesystem/model access require
  typed trusted grants. Repositories cannot widen authority.

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
- P emits typed, versioned reduced events through one `EventHandler` seam. V1
  appends redacted NDJSON locally. Handler failure never rolls back operations,
  and events are not state truth, replay, acknowledgement, or notification
  protocol.

### Communication and gateway

- Git carries source; NDJSON-RPC carries control/status; attachment carries a
  fixed validated entrypoint and terminal bytes; Bifrost HTTP carries model
  inference; event handlers receive reduced P events.
- Linux clients use local Unix or client-initiated SSH-to-Unix transports. The
  daemon never initiates SSH.
- Bifrost remains independently configured and owns provider credentials,
  routing, policy, and usage. Initial model-enabled creation provisions and
  validates its per-session key. After establishment, an outage degrades model
  access only; Start and Attach do not probe Bifrost.
- P V1 does not orchestrate project services. Checks and attempts remain
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
  teardown across crash/restart/SSH loss;
- lifecycle and bulk project-deletion convergence at every crash point;
- Git principal/ref enforcement and origin race/unknown-outcome behavior;
- immutable-policy comparison and guided recreation;
- Bifrost positive/negative route enforcement plus established-outage behavior;
- agent-hook mappings and the NDJSON event handler; and
- performance/capacity evidence for supported configurations.

See [development validations](development-validations.md) for the gated tests.

## Recommended implementation order

1. build state, configuration, RPC, events, and fake-adapter foundations;
2. implement project/Git/origin and session lifecycle skeletons;
3. implement and validate Incus, the systemd base-image contract, and Nix
   environment images;
4. integrate attachment, observability, policy comparison, and recovery;
5. validate Bifrost and optional model access; and
6. prototype the TUI, record the chosen interaction contract, then implement
   progressively complete product slices.
