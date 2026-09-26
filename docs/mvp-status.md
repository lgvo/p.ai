# P — MVP status

Current snapshot of P's design and implementation readiness.

> **Status: non-normative snapshot, updated 2026-09-26.**
> [Project guidance](../PROJECT.md) owns enduring direction; subject design
> documents remain authoritative for detailed behavior.
> [Missing pieces](missing-pieces.md) tracks implementation work.
> [Implementation plan](implementation-plan.md) proposes testable delivery
> checkpoints from a real terminal/Git session through the documented MVP.

## Executive state

P's CLI-first implementation has started. Package validation, trusted plugin
activation, bounded WASI event handlers, session asset plans, the file-log
broker, SQLite state, Git/SSH, the assembled base runtime, and the executable
Incus plugin have passed independent review, unit tests, and the packaged VM
suite. Public CLI/RPC creation, captured-source sessions, exact retry, daemon
restart, and retained Stop/Start now pass against real base-image containers.
Private status RPC, durable unattended projections, attachment leases, daemon
events, origin-backed creation, and explicit publication also pass in the VM.
Restricted offline devShell realization, the selected environment WASI plugin,
and native private-image publication pass their gates. Offline public devShell
creation, project cache reuse/rebuild, activation, and private-state persistence
also pass. Explicit cache collection, interrupted-cleanup recovery, and
concurrent cache reuse pass their selected VM gate. The selected Codex adapter
passes authentication-free event, private initialization, dummy credential
isolation, and Stop/Start VM checks. The full serial VM checkpoint through
test 28 passed these gates together, including bounded, non-activating workspace
inspection and interrupted-pause recovery. Bounded loss reports for Git-known
runtime worktrees, retained commits, and fingerprints passed selected test 29.
Selected later VM checks also cover Discard/Delete, typed grants, guarded
repairs, retained-branch rename/delete, and three early failed-creation
replacement paths. The selected public-network VM now passes real DoH,
hostname HTTPS, Nix fetch and public-to-private DNS/HTTPS redirect denials
under the configured Incus and outer-VM restrictions. The final full-suite
checkpoint remains pending.
Authenticated Codex acceptance is reserved for
the user's final manual test; automated tests use fixtures and dummy files.
The [progress record](implementation-progress.md) tracks each step's
implementation, review, and serialized VM validation. Subject contracts remain
assigned to their owner documents; the gateway retains a post-MVP design.

The fixture-backed TUI prototype now has a reviewed session-browser direction:
Resource topology, a session picker, prefix-driven terminal navigation, and
agent/service inspection pages. [Current interaction decisions](../.prototype/tui-options/DECISIONS.md)
record that direction and its integration limits. Production TUI implementation
and reconciliation of the agent/service extensions remain open. Installed
first-party plugin composition passed selected VM48; plugin management and
remaining approval interactions stay open beyond the
[package and activation contract](plugin-contract.md).

Concrete schemas, adapters, tests, packaging, and real-machine evidence remain
implementation work. They should narrow unsupported claims without reopening
the product model unless evidence disproves an invariant.

A [disposable NixOS/Incus lab](../dev/vm/README.md) now provides a pinned
container fixture and an automated VM smoke test for runtime infrastructure.
The separate product suite runs P's daemon and CLI against real Incus
containers. Its current checkpoint covers the gates listed above; it does not
establish the full MVP.

[Product direction](PRODUCT.md) requires MVP to prove P's composable plugin
model through secure first-party defaults for Incus runtime support, the tmux
persistent host, Git source and session access, Nix environment preparation,
the structured file-log handler, and the Codex adapter. The usable public
interfaces and this basic composition are sufficient for MVP; a separate
agent-authored plugin is not a release gate. The [plugin contract](plugin-contract.md)
now owns packaging, activation, compatibility, and the selected executable
boundary. Event-handler, source-Git, runtime and environment executable methods
and host/agent assets are validated. Selected VM48 exercised the production
package's exact six-plugin catalog and CLI default activation through real
session creation, fixture status reporting and Stop/Start with daemon restart.
Plugin installation/update/removal and NixOS service installation remain open;
this evidence does not establish the complete plugin MVP or authenticated
Codex execution.

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
  an existing unassigned P branch, or create a new branch from committed
  P/origin source. Only new-branch creation asks for a source.
- A session has an immutable UUID and one `(project, branch)` assignment.
  Rename changes the P/workspace branch while retaining identity.
- Session Git updates are unconditionally fast-forward-only. The host P key is
  read-only; the daemon alone mutates P refs. Rewriting already-recorded P
  history is an outside host Git operation.
- Retained branches are first-class project resources with
  list/source/fetch/rename/fast-forward-publish/loss-preview/delete operations.
- **Delete project and all P data** uses aggregate preflight, confirmed live-
  attachment termination, a minimal durable deletion record, and idempotent
  ensure-absent retry. It has no rollback/recovery-mode state machine.

### Runtime, environment, and policy

- MVP installation targets NixOS with Incus only. Backup/restore and software
  upgrade/rollback are outside delivery gates. Normal Stop/Start and daemon
  restart retain P's local Git repositories; disk loss and deliberate deletion
  have no protection claim.
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

- Create, Start, Attach/Detach, Rename, Stop, Discard, Delete, supported Repair,
  retry, and restart reconciliation have defined outcomes.
- Branch/upstream mismatches must show expected and actual values and block
  dependent actions. Expected/actual diagnostics and manual-correction/recheck
  acceptance remain pending. A dedicated mismatch repair or automatic
  checkout/reset is outside MVP.
- Incus unavailability leaves cleanup incomplete with identity, durable
  confirmed operation and authorization restrictions retained. Retry/reconciliation
  may resume when it returns; uncertain resources are never forgotten or replaced.
  Explicit abandonment and its tombstone/orphan-cleanup/forget workflow are outside
  MVP. Manual investigation preserves existing identity and duplicate checks.
- Missing assigned-ref repair is supported when the inspected local commit
  object is already in P's bare repository. If it exists only in the runtime,
  P reports `p_object_missing` and leaves the runtime untouched; automatic
  object transfer is outside MVP.
- A renamed Incus instance cannot be relinked in MVP. Its expected name is
  derived from the session UUID; P stores no mutable runtime locator and does
  not adopt a renamed instance.
- Exact Retry preserves the failed creation's immutable request and operation
  identity while cleaning verified partial derived resources. **Try again with
  changes** has validated integrated early-failure paths with a new identity.
  Complex cases may refuse with a documented, validated cleanup-then-Create
  path. Uncertain resources remain intact when safe cleanup cannot proceed;
  VM49 validates bounded local committed-creation cleanup after a settled
  builder failure. Broader assembled-runtime cleanup acceptance remains pending.
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

## Current prototype interaction direction

The [runnable prototype](../.prototype/tui-options/README.md) and
[decision record](../.prototype/tui-options/DECISIONS.md) capture the reviewed
layout and controls as of 2026-09-09. The browser uses branch labels, a roughly
65% session-list split, exact project selection, fuzzy search, a prefix popup
inside fake terminals, and dedicated Agents/Services/journal pages.

This is local fixture-backed Go code, not a production RPC client. Real
attachment, multi-agent inventory/preview sourcing, and project-service
management need their own integration contracts. The authoritative MVP still
retains a single unattended agent signal and excludes project-service
orchestration. The prototype's presentation does not silently revise those
boundaries.

Creation now follows Project → Branch → Policy. An existing unassigned branch
goes directly to Policy; creating a new branch asks for its source and then its name.
The mock then boots and enters the session. Replacement/retry, policy,
retained-branch, and destructive-operation screens remain older probes
requiring further review in the new browser.
The production TUI must remain a thin client of the subject-owned RPC and
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
6. continue from the recorded TUI prototype decisions, resolve integration
   boundaries, and implement progressively complete product slices.
