# P — V1 missing pieces

Complete tracker for work that remains before P V1 can be claimed.

> **Status: non-normative work tracker, reviewed 2026-08-20.** Subject design
> documents remain authoritative. This file cannot introduce product rules.

## Decisions before a complete implementation plan

### Project and unassigned-ref lifecycle

- [ ] Define what `p .` discovers before registration.
- [ ] Define project path selection, bare-repository creation, and initial
  committed-source seeding.
- [ ] Define repeated registration and multiple/relocated source locations.
- [ ] Decide whether project rename exists in V1; define project removal and
  loss reporting.
- [ ] Define listing, source selection, rename, deletion, and publication
  posture for branches retained by discard.

This belongs in a project/repository lifecycle authority.

### Initial TUI vertical slice

- [ ] Define project/session navigation, filtering, selection, and preview.
- [ ] Define registration and session creation flows.
- [ ] Define environment/lifecycle progress, failures, repair, and orphan
  presentation.
- [ ] Define terminal handoff/return for direct and tmux attachments.
- [ ] Select the initial actions, key bindings, help, confirmations, and
  smallest useful milestone.

The TUI must remain a client of the same RPC surface as `p api`.

## Contracts to define with implementation

### Trusted configuration and state

- [ ] Choose configuration/state paths, file format, permissions, and reload
  behavior.
- [ ] Define instance fields: P identity, listeners, confined Incus
  socket/project, base-image identity, Bifrost, and defaults.
- [ ] Define project fields: interactive host/command, network, filesystem
  grants, runtime-owned directories, resources, and optional model policy.
- [ ] Reject repository-, branch-, and session-scoped V1 policy fields.
- [ ] Define SQLite schema, uniqueness constraints, WAL/backup/migration, and
  newer-schema refusal.
- [ ] Define session, assignment, policy snapshot, operation, orphan,
  credential, environment-index, origin-observation, expected/current
  startup readiness, and latest-condition records. Do not add artifact leases or
  duplicate Incus/Git state.
- [ ] Define bounded diagnostic/log retention and copied-state safeguards.

### RPC and client transport

- [ ] Define hello/version negotiation, initial host/session method catalogs,
  schemas, stable errors, limits, and cancellation.
- [ ] Define cross-authority operation subscriptions, reconnect, and
  idempotency.
- [ ] Define structured `AttachSpec`, pending-to-confirmed attachment token
  handshake, Unix socket permissions/peer checks, and Linux SSH-to-Unix bridge.
- [ ] Keep terminal bytes, source trees, and arbitrary commands outside
  lifecycle RPC.

### Git server and repository plumbing

- [ ] Define repository layout, Wish listener/host key, P URLs, and key storage.
- [ ] Implement fixed Git service dispatch without a shell.
- [ ] Implement principal/ref ownership, hidden/reserved namespaces,
  arbitrary-object restrictions, lifecycle guards, and default force-push
  denial.
- [ ] Implement exact committed-source import from registered locations.
- [ ] Implement serialized origin refresh and explicit idempotent
  create-or-fast-forward publication using host SSH credentials.

### Incus runtime backend

- [ ] Define the confined local Incus connection/configuration contract and
  startup verification. Reject administrative or remote authority.
- [ ] Define the CLI JSON or official-client mapping and pinned Incus version.
- [ ] Implement deterministic UUID instance/builder names, P metadata,
  ownership verification, operations, events, and duplicate detection.
- [ ] Implement private root layout, standalone clone, fixed endpoints,
  runtime-owned directories, and named filesystem grants.
- [ ] Implement unprivileged container baseline, resources, `none` networking,
  and gated `public-egress`.
- [ ] Implement start/pause/resume/stop/remove/inspect, generation-bound
  startup-readiness markers/projection, and stable `stop_required` for Start on
  a running `not_ready` instance.
- [ ] Make marker transitions write-temp, file-`fsync`, atomic-rename, and
  directory-`fsync` in a P-owned path unwritable by the session user, shell
  hook, or interactive command.
- [ ] Implement the closed non-activating workspace inspection/operation
  surface used by rename, preflight, and repair.

### Nix environment images

- [ ] Pin/build the Incus-native P base image per supported architecture.
- [ ] Define and validate the per-builder/per-session local Nix daemon,
  build-user, socket, sandbox, startup, and failure posture.
- [ ] Define `EnvironmentRequest`, plan, project-scoped key, target, opaque
  `EnvironmentHandle`, activation, and bounded diagnostic schemas.
- [ ] Implement committed default-devShell discovery, pure lock posture, and
  base-only fallback.
- [ ] Implement a disposable restricted Incus builder with public
  substituter/fetch policy and no ambient authority.
- [ ] Pin and validate the experimental `nix print-dev-env --json` contract,
  including schema, quoting/types, functions, hooks, failure behavior, and
  equivalence fixtures.
- [ ] Realize the devShell, capture activation, add its GC root, verify Nix
  database/store coherence, remove the materialized checkout/unreachable build
  paths, account for source-derived closure paths, and publish the private
  Incus image.
- [ ] Index project-scoped environment key to fingerprint; implement cache hit/miss,
  in-memory concurrent build sharing, explicit collection, and labeled builder
  cleanup. Do not add session leases.
- [ ] Validate that sessions may add private Nix paths without modifying the
  image or another session.

### Lifecycle and recovery

- [ ] Implement create/rename/discard/delete as persisted cross-authority
  workflows with documented commit points and locks.
- [ ] Implement start/stop by observing Incus operation/state without a second
  P phase machine.
- [ ] Implement pending/confirmed attach/detach leases, durable startup
  readiness, fresh `InteractiveHost` checks, clear-on-confirmed-entry status,
  and a trusted helper that retains the reachable lease through teardown and
  binds tmux/direct channel lifetime to client/carrier loss.
- [ ] Implement creation retry's persisted `stopping-partial-runtime` phase in
  the same operation before recording and starting a new startup generation.
- [ ] Implement destructive preflight/fingerprints, repair, abandonment,
  orphan recognition, and credential cleanup.
- [ ] Make missing-instance repair compare the recorded image identity with a
  freshly resolved current-branch environment when the cached image is gone.
- [ ] Implement explicit image-cache inspection/collection separately from
  session lifecycle.

### Interactive hosts, observability, gateway, and notifications

- [ ] Implement `direct` and UUID-isolated tmux hosts.
- [ ] Implement runtime condition, current-generation startup readiness,
  confirmed live attachments, latest unattended condition, subscriptions, and
  notification reduction.
- [ ] Pin Bifrost and implement secure management credential plus per-session
  virtual-key ensure/persist/deliver/verify/revoke/cleanup.
- [ ] Configure and validate Bifrost's native boundary: admin authentication,
  mandatory virtual keys for inference, approved OpenAI-compatible
  inference/model discovery, and authorization rejection of dashboard,
  management, governance, logs, MCP, Skills, and every other non-V1 route.
- [ ] Pin and inventory the complete Bifrost route surface; fail model access
  closed for unclassified routes or an unvalidated version/configuration.
- [ ] Run native Bifrost authorization as the first model-access spike. Do not
  implement the optional milestone unless real session keys pass every positive
  and negative probe; failure must not block non-model P functionality.
- [ ] Select/implement the first notification sinks and redaction behavior.
- [ ] Version/package verified Claude Code and Codex status recipes.

### Security and operations

- [ ] Threat-model daemon, Git, origin, Incus, builder, session, Bifrost,
  clients, and notification crossings.
- [ ] Test path/symlink races, malicious refs/URLs, argument injection,
  oversized RPC, malformed external output, and hostile repository content.
- [ ] Verify secrets never appear in metadata, arguments, logs, RPC, TUI,
  SQLite diagnostics, images, or support output.
- [ ] Define resource/concurrency bounds, shutdown, disk-full/corruption,
  upgrade, backup/restore, and external-service failures.

## Required development validations

The evidence plan is [development validations](development-validations.md);
the real Nix workload is in
[Nix project workflow validation](nix-project-workflow-validation.md).

- [ ] Confined Incus access cannot reach administrative, other-project, host,
  or disallowed device/path authority.
- [ ] Incus instance/image/storage/operation/event behavior matches the pinned
  adapter on supported Linux/storage-driver combinations.
- [ ] `none` and `public-egress` enforce the documented IPv4/IPv6 destination
  policy.
- [ ] Nix resolution, build, activation, cached image, and private session
  store deltas work on the homelab repository and both supported architectures.
- [ ] Lifecycle crash tests converge without duplicate instances or ref loss.
- [ ] Git tests cover principals, ref guards, origin races, force denial, and
  explicit publication retry.
- [ ] Unix and SSH-to-Unix reconnect plus pending/confirmed attachment and
  lease-loss/client-death channel-teardown behavior passes, including helper
  SIGKILL and direct-command termination.
- [ ] Durable startup readiness and current-generation failure behavior pass
  across daemon restart and operation-record expiry, including
  `stop_required` followed by a new generation after Stop → Start.
- [ ] Bifrost session-key lifecycle plus positive/negative route probes pass.
- [ ] Claude Code/Codex mappings match pinned event traces.
- [ ] Cold, substituted, image-hit, and session-start performance/storage
  measurements are recorded without universal speed/deduplication claims.

## Documentation and release work

- [ ] Publish implemented RPC/configuration references and examples.
- [ ] Document Incus prerequisite/confinement setup, installation, initial
  setup, backup/restore, upgrades, troubleshooting, and cache management.
- [ ] Publish security assumptions, supported versions/storage/network
  profiles, limitations, and validation evidence.
- [ ] Refresh dated prior art before release positioning.
- [ ] Remove tracker entries only when design, implementation, validation, or
  user documentation actually exists.

## Explicitly outside V1

- native macOS/Windows daemon/runtime support;
- daemon federation, runtime migration, remote Incus, or Incus clustering;
- Incus VMs, Kubernetes, raw Podman/Docker, Dockerfile/OCI/devcontainer
  environment providers;
- branch-specific grants or in-place policy widening;
- services, attempts, and checks;
- published ports, host/LAN access, privileged containers, and devices;
- build secrets, private flake inputs, remote builders, and KVM;
- automatic origin publication, force-publication, or automatic
  session/branch/image/orphan reclamation;
- multi-user scheduling/authorization; and
- Anthropic-compatible gateway use, MCP, Skills, Agent/Code Mode, and Envoy.

## Completion rule

V1 is complete only when the applicable design acceptance criteria pass, every
claimed integration has pinned validation evidence, installation and recovery
are documented, and this tracker has no unresolved required item.
