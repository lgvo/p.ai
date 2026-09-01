# P — remaining implementation work

Concrete work still required to implement and validate the V1 design.

> **Status: tracker, not authority.** Subject documents own behavior. This list
> points to them and must not introduce a competing contract.

## Design readiness

The cross-document lifecycle decisions needed to begin implementation are now
owned by the design set:

- [project lifecycle](project-lifecycle.md) owns explicit project creation,
  origin association, blank/empty bootstrap, retained branches, and bulk
  project deletion;
- [session lifecycle](session-lifecycle.md) owns session operations, exact
  Retry, replacement creation, destructive preflight, and recovery;
- [runtime and isolation](runtime-isolation.md) owns Incus, systemd, the
  persistent interactive host, attachment entrypoint, policy snapshots, and
  grants;
- [communication boundaries](communication-boundaries.md) owns Git, RPC, SSH,
  origin, attachment, build, gateway, and event-handler channel division;
- [session observability](session-observability.md) owns the four public facts,
  status reduction, attachment presence, and typed P events;
- [environment building](environment-building.md) owns Nix selection,
  realization, activation, and image caching;
- [model gateway](model-gateway.md) owns Bifrost provisioning and enforcement;
  and
- [technology stack](technology-stack.md) owns implementation interfaces and
  library/tool choices.

The exact TUI layout, navigation, key map, and smallest useful initial screen
remain intentionally undecided until a prototype supplies interaction
evidence. That prototype may change presentation, but not owner-document
lifecycle semantics.

## Foundation

- [ ] Scaffold the Go module, package boundaries, daemon entrypoint, thin
  client/TUI entrypoint, migration runner, and release metadata.
- [ ] Implement structured configuration loading and validation with trusted
  instance/project authority and immutable session policy snapshots.
- [ ] Implement SQLite migrations, transaction helpers, operation/idempotency
  records, minimal project-deletion tombstones, and restart reconciliation.
- [ ] Implement typed errors, bounded/redacted diagnostics, structured logging,
  and the versioned `EventHandler` interface with the NDJSON file handler.
- [ ] Establish fake Git, runtime, environment, gateway, and clock adapters for
  deterministic lifecycle tests.

## Projects, Git, and origin

- [ ] Implement explicit project creation from a validated SSH origin or as a
  blank repository; never infer a host checkout.
- [ ] Implement contact-before-commit origin association, explicit origin
  change/removal, refresh, and local-only behavior.
- [ ] Implement blank/empty-origin bootstrap on unborn `main`, including first
  push and the committed-source requirement for later sessions.
- [ ] Provision one bare repository per project, hidden/reserved namespaces,
  per-session principals, the read-only host principal, and unconditional
  fast-forward-only update hooks.
- [ ] Implement retained-branch list/source/fetch/rename/fast-forward-publish/
  loss-preview/delete operations.
- [ ] Implement explicit origin publication with fresh observation, one
  destination ref, no force, and unknown-outcome reporting.
- [ ] Implement **Delete project and all P data** with aggregate preflight,
  confirmed attachment termination, minimal tombstone, ensure-absent retry,
  and abandonment for unreachable resources.

## Runtime and environments

- [ ] Implement the confined Incus backend, deterministic labels/names,
  non-activating inspection helper, endpoint mounts, and orphan recognition.
- [ ] Build the pinned base image with systemd, Nix, Git/SSH, tmux, the runtime
  kit, `p-session.target`, `p-interactive.service`, and the root-owned
  `/usr/libexec/p/attach` entrypoint.
- [ ] Implement systemd activation/host supervision, journal diagnostic
  capture, and container shutdown on clean or failed persistent-host exit.
- [ ] Implement the Nix environment builder, compatibility gate, immutable
  image cache, private session roots, explicit cache collection, and blank
  bootstrap base-image path.
- [ ] Implement typed filesystem/network/model grants and policy comparison as
  `current`, `outdated`, or `invalid`, including guided recreation.
- [ ] Complete every relevant real-machine gate in
  [development validations](development-validations.md).

## Session lifecycle and observability

- [ ] Implement Create with committed source plus the one bootstrap exception,
  exact immutable Retry, and **Try again with changes** as a superseding new
  creation.
- [ ] Implement Start, Attach/Detach, Rename, Stop, Discard, Delete, Repair,
  Abandon, and startup/restart reconciliation exactly as owned by
  [session lifecycle](session-lifecycle.md).
- [ ] Implement pending-to-confirmed attachment leases whose loss tears down
  only the temporary transport and never the persistent host.
- [ ] Implement the four independent public facts: `session_condition`,
  `attached_count`, `latest_unattended_condition`, and `policy_condition`.
- [ ] Implement versioned agent adapters and clear-on-confirmed-first-entry
  reduction without terminal/process heuristics or retained status history.
- [ ] Emit reduced lifecycle/status/policy events through `EventHandler`;
  handler failure must not roll back authoritative operations.

## Gateway

- [ ] Pin and validate a Bifrost release and route inventory before enabling
  model access.
- [ ] Implement idempotent per-session key ensure/persist/deliver/revoke with
  positive inference probes and negative administrative/non-V1 probes during
  initial creation.
- [ ] Preserve established Start/Attach behavior during a Bifrost outage while
  reporting model access as degraded.
- [ ] Validate OpenAI-compatible Codex use first; keep Anthropic-compatible
  clients, MCP, Skills, Agent Mode, and Code Mode behind their later gates.

## TUI and clients

- [ ] Build a throwaway interaction prototype against fixture RPC data.
- [ ] Test project/session overview density, creation and **Try again with
  changes**, policy warnings/diffs, retained branches, destructive previews,
  bulk project deletion progress/retry, and attach/switch/detach behavior.
- [ ] Record the chosen initial layout/navigation/key map only after testing;
  then implement the thin production TUI without moving business logic into it.
- [ ] Implement local Unix and client-initiated SSH-to-Unix RPC transports plus
  the trusted attachment helper.

## Release closure

- [ ] Turn every acceptance criterion and development validation into an
  automated test, recorded integration result, or explicit support gate.
- [ ] Pin dependency/protocol versions and prove upgrade behavior.
- [ ] Add packaging, install/upgrade/rollback guidance, service definitions,
  backup/restore, and diagnostics documentation.
- [ ] Re-read all summaries after implementation evidence and update any claim
  that proved narrower than the design.
