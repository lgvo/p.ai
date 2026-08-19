# P — V1 missing pieces

Complete tracker for work that remains before P v1 can be claimed.

> **Status: non-normative work tracker, reviewed 2026-08-19.** The named design
> document remains authoritative for each subject. This file records decisions,
> implementation, validation, and documentation still needed; it must not be
> used to introduce a conflicting product rule. See [V1 status](v1-status.md)
> for the current design-readiness summary.

## Status vocabulary

- **Decision:** product or architecture choice that must update its design
  authority before implementation depends on it.
- **Contract:** precise schema or interface to define alongside the first
  implementation without reopening settled behavior.
- **Implementation:** code and tests required by an existing design.
- **Validation:** real-tool or real-machine evidence required before claiming
  the affected capability.
- **Documentation:** user or operator material derived from implemented
  behavior.

## Decisions before the implementation plan

### 1. Settle the V1 Nix runtime and artifact model

**State: decision required.** This is the only unresolved core execution
architecture.

The current documents contain two explicit models, while recent design work
introduced a third candidate:

1. [Environment building](environment-building.md#closure-packaging) makes a
   read-only devShell closure beside the P substrate the normal V1 artifact.
   The backend does not call Nix.
2. [Nix workflow validation](nix-project-workflow-validation.md#recommended-starting-hypothesis)
   recommends testing a project-scoped Nix daemon/store so Nix-native projects
   can build after workspace changes.
3. The current candidate from discussion is a Nix-built layered OCI image with
   Nix, the registered initial devShell closure, and a private writable
   container-overlay Nix store. Docker/Podman share the immutable image layers;
   a session retains only its writable delta until removal.

Decide and document:

- [ ] whether running Nix commands after workspace changes is a V1 session
  requirement;
- [ ] whether the local-container Nix artifact is a closure mount, a
  self-contained OCI image, or supports both in a defined order;
- [ ] whether writable Nix state is per-session or project-scoped;
- [ ] the authority and lifetime of the Nix store database, profiles, roots,
  logs, and garbage collection;
- [ ] what survives stop/start, runtime repair, discard, and delete;
- [ ] what happens when `flake.nix` or `flake.lock` changes inside an existing
  session;
- [ ] whether an existing session may realize new paths without rebuilding its
  environment artifact;
- [ ] how shared image/closure bytes and private/session or project deltas are
  measured and presented;
- [ ] the exact unprivileged-user and Nix-sandbox posture; and
- [ ] equivalent behavior under every claimed rootless engine.

After the decision, update
[environment building](environment-building.md),
[runtime isolation](runtime-isolation.md),
[technology stack](technology-stack.md), the README/FAQ, and the Nix workflow
validation before treating the model as final.

### 2. Define project and unassigned-ref lifecycle

**State: decision required.** Project identity is settled, but the operations
around it are not.

Define:

- [ ] what `p .` discovers and displays before registration;
- [ ] how the complete P project path is selected or derived;
- [ ] how an initial P bare repository is created and seeded;
- [ ] how repeated registration of the same checkout behaves;
- [ ] whether and how several registered source locations map to one project;
- [ ] source-location relocation and removal;
- [ ] project rename support, or an explicit V1 prohibition;
- [ ] project removal preconditions and loss reporting;
- [ ] listing and source selection for branches retained by discard;
- [ ] rename and deletion operations for unassigned P branches;
- [ ] whether an unassigned branch can be published through P or only after a
  new session is created from it; and
- [ ] how protected tracking generations and retained objects are collected
  when a project is removed.

This belongs in a project/repository lifecycle authority or an explicitly
scoped addition to [session lifecycle](session-lifecycle.md) and
[communication boundaries](communication-boundaries.md).

### 3. Define the initial TUI vertical slice

**State: decision required for product implementation, not for the underlying
domain model.** The TUI framework and thin-client rule are settled, but the
interaction contract is not.

Define:

- [ ] the project/session navigation hierarchy and default screen;
- [ ] filtering, sorting, selection, and preview fields;
- [ ] project registration flow;
- [ ] session creation flow: project, committed source, suggested/new branch,
  and effective policy review;
- [ ] environment-build and lifecycle progress presentation;
- [ ] start, stop, attach, rename, discard, delete, repair, and publish actions;
- [ ] destructive confirmation views supplied from daemon loss reports;
- [ ] how the TUI yields and restores the terminal for Bash, tmux, Codex,
  Claude Code, or another interactive command;
- [ ] failed/transitional operation and orphan presentation;
- [ ] initial key bindings and discoverable help; and
- [ ] the smallest useful release slice before every V1 operation has a rich
  screen.

The TUI must remain a client of the same RPC surface used by `p api`; lifecycle
rules stay in the daemon.

## Contracts to define during implementation

These do not require new product concepts, but the first consumer cannot be
implemented safely without a concrete contract.

### Trusted configuration

- [ ] Choose the host configuration file location and format.
- [ ] Define the complete V1 project fields, types, defaults, and validation.
- [ ] Define instance-level paths, listeners, default engine, and Bifrost
  endpoint configuration.
- [ ] Define configuration reload diagnostics and snapshot serialization.
- [ ] Reject unknown, conflicting, unsafe, and branch/session-scoped V1 fields.
- [ ] Provide an effective-configuration inspection view.

### State and persistence

- [ ] Define the state-directory layout and permissions.
- [ ] Define the SQLite schema, uniqueness constraints, WAL/backup behavior,
  migrations, and newer-schema refusal.
- [ ] Define operation, cleanup-tombstone, orphan, credential, artifact-lease,
  origin-generation, and latest-condition records.
- [ ] Define recovery behavior for a copied state directory with the same
  instance UUID.
- [ ] Define bounded diagnostic/log retention without automatic session or
  branch deletion.

### RPC and client transport

- [ ] Implement and document protocol hello/version negotiation.
- [ ] Define the initial host RPC method catalog and schemas.
- [ ] Define the session RPC method catalog, including `status.report`.
- [ ] Define stable error codes, identifier encoding, pagination, limits, and
  bounded diagnostics.
- [ ] Define long-operation subscriptions, cancellation, reconnect, and
  idempotency behavior.
- [ ] Define the complete structured `AttachSpec`.
- [ ] Define Unix-socket permissions and peer checks.
- [ ] Define the Linux SSH-to-Unix bridge and remote attachment command.
- [ ] Keep terminal bytes and arbitrary commands out of lifecycle RPC.

### Git server and repository plumbing

- [ ] Define repository/state paths and bare-repository initialization.
- [ ] Define the Wish listener address, SSH host-key lifecycle, and advertised
  P URLs.
- [ ] Define host/session public-key registration and private-key storage.
- [ ] Implement fixed `git-upload-pack`/`git-receive-pack` dispatch without a
  shell.
- [ ] Implement hidden namespace and arbitrary-object-want restrictions.
- [ ] Implement branch ownership, ref guards, fast-forward policy, and the
  project-scoped rewrite exception.
- [ ] Define the `p://` display URL and checkout-facing SSH rewrite safely.
- [ ] Implement immutable committed-source import from registered locations.
- [ ] Implement protected origin tracking generations and their leases.
- [ ] Implement explicit origin publication and ambiguous-result recovery.

### Environment artifacts

- [ ] Define the concrete `EnvironmentPlan`, `EnvironmentArtifact`, manifest,
  compatibility, and build-key schemas.
- [ ] Pin the substrate contents, layout, versioning, and build process.
- [ ] Define the isolated worker job format and artifact return boundary.
- [ ] Implement pure committed-source devShell discovery and lock policy.
- [ ] Implement Nix realization, activation capture, packaging, smoke
  verification, and quarantine.
- [ ] Implement single-flight builds, leases, progress, cancellation, restart
  reconciliation, and explicit collection.
- [ ] Record exact inputs and tool versions needed to reproduce a build.
- [ ] Add substrate-only behavior when no default devShell exists.
- [ ] Add the Dockerfile provider only after the V1 Nix path; it is not part of
  the initial implementation slice.

### Runtime backend and engine adapter

- [ ] Define normalized engine CLI commands and JSON field mappings.
- [ ] Implement deterministic engine detection with no silent fallback.
- [ ] Implement `SessionSpec`, `RuntimeManifest`, stable labels, and ownership
  verification.
- [ ] Implement explicit standalone workspace clone, home, writable layer, and
  named data/cache storage.
- [ ] Implement the fixed runtime paths and protected session endpoints.
- [ ] Implement rootless baseline isolation and prohibit conflicting image
  metadata.
- [ ] Implement `none` networking and then gated `public-egress`.
- [ ] Implement project filesystem grants with canonicalization and fixed
  targets.
- [ ] Implement pause/resume, stop/start retention, inspect, guarded removal,
  events, and duplicate-runtime detection.
- [ ] Implement the non-activating closed workspace helper used by rename,
  preflight, and repair.
- [ ] Define readiness markers and activation/interactive-host failure states.

### Interactive hosts and attachment

- [ ] Implement the `direct` host as the minimal conformance implementation.
- [ ] Implement UUID-isolated tmux hosting without broad process matching.
- [ ] Define when the interactive command starts for create, start, and attach.
- [ ] Define attach/detach exit behavior and multiple-attachment capability.
- [ ] Preserve the rule that stop terminates processes even when tmux is used.

### Notifications and integrations

- [ ] Define trusted notification configuration and redaction defaults.
- [ ] Implement desktop, ntfy, and webhook sinks.
- [ ] Run the optional command sink only through the isolation provider.
- [ ] Define sink delivery bookkeeping without introducing seen/unseen state.
- [ ] Version and package inspectable Claude Code and Codex hook recipes after
  their validation passes.

### Bifrost integration

- [ ] Pin a supported Bifrost release and management API behavior.
- [ ] Define secure storage for its management credential and session virtual
  keys.
- [ ] Implement ensure, verify, persist, deliver, revoke, and cleanup by
  session UUID.
- [ ] Enforce inference/model-discovery-only network exposure.
- [ ] Validate OpenAI-compatible Codex use through OpenRouter and one local
  endpoint.
- [ ] Keep Anthropic-compatible Claude Code support in phase two.
- [ ] Keep MCP, Skills Repository, Agent Mode, and Code Mode outside the
  initial inference implementation.

### Security and operational hardening

- [ ] Threat-model every crossing among daemon, builder, runtime, Git server,
  origin runner, Bifrost, notification sinks, and clients.
- [ ] Test path traversal, symlink races, malicious refs/URLs, argument
  injection, oversized RPC lines, malformed engine JSON, and hostile project
  metadata.
- [ ] Verify secret redaction in process arguments, environments, logs, RPC,
  TUI previews, operation records, and support output.
- [ ] Define resource and concurrency bounds for RPC clients, builds, runtime
  events, Git operations, logs, and notifications.
- [ ] Define graceful daemon shutdown, interrupted upgrade, disk-full, corrupt
  state, and partial external-service failure behavior.
- [ ] Add backup/restore tests that preserve the instance UUID and prevent two
  active copies of one state directory.

## Domain implementation still required

The designs exist, but no production implementation currently exists for:

- [ ] instance initialization and daemon lifecycle;
- [ ] project registry and repository lifecycle;
- [ ] session create/start/attach/rename/stop/discard/delete;
- [ ] destructive preflight and confirmation fingerprints;
- [ ] reconciliation, targeted repair, abandonment, and orphan cleanup;
- [ ] artifact and credential cleanup sequencing;
- [ ] authoritative runtime-condition reduction;
- [ ] attachment leases and clear-on-enter behavior;
- [ ] latest unattended condition storage, subscription, and notification;
- [ ] origin configuration, refresh, source selection, comparison, and
  publication;
- [ ] the Bubble Tea TUI and small `p`/`p api`/`p daemon` launcher surface;
- [ ] diagnostics, structured logs, operational inspection, and support
  bundles; and
- [ ] packaging, installation, upgrade, backup, and release procedures.

## Required development validations

The full gates live in [development validations](development-validations.md).
All evidence must record pinned versions and host/kernel context.

- [ ] Rootless Podman public egress works while host, LAN/private, metadata,
  sibling, and engine access remain denied over IPv4 and IPv6.
- [ ] Rootless Docker passes the same runtime/network conformance suite before
  support is claimed.
- [ ] Isolated Nix evaluation, realization, activation capture, and packaging
  work without ambient host authority.
- [ ] The selected writable Nix runtime/store model passes the complete
  [Nix project workflow validation](nix-project-workflow-validation.md) on the
  real homelab repository.
- [ ] Session RPC reconnects across daemon restart without retaining a stale
  socket inode or attachment lease.
- [ ] Bifrost virtual-key lifecycle and restricted routes work against the
  pinned release.
- [ ] Claude Code and Codex hook mappings match captured versioned traces.
- [ ] Git, Nix, Podman, Docker, tmux, Go, Bubble Tea, Wish, and Bifrost versions
  and parsed output contracts are pinned.
- [ ] Runtime storage, mounts, labels, pause/resume, cleanup, and the isolated
  workspace helper pass engine conformance.
- [ ] Lifecycle crash tests cover every documented commit point and recovery
  path.
- [ ] Git tests cover principal scope, hidden namespaces, arbitrary wants,
  rename guards, origin races, and ambiguous publication.
- [ ] Performance evidence covers cold, substituted, and warm Nix paths on
  representative `x86_64-linux` and `aarch64-linux` systems.

## Documentation and release work

- [ ] Write the practical standalone-clone and Nix-built-container guide after
  the Nix runtime model is selected.
- [ ] Publish the implemented RPC reference and examples.
- [ ] Publish the trusted configuration reference and minimal examples.
- [ ] Document installation, initial setup, backup/restore, upgrades, and
  troubleshooting.
- [ ] Document security assumptions, known limitations, and validation
  evidence for each supported engine.
- [ ] Record supported dependency versions and release compatibility policy.
- [ ] Decide P's own Apache-2.0 versus MIT license before public release.
- [ ] Refresh the dated prior-art snapshot when positioning claims are used for
  a release.
- [ ] Remove resolved entries from this tracker only after their authoritative
  design, implementation, test, or user documentation exists.

## Explicitly outside V1

Do not turn these into blockers for the first release:

- native macOS or Windows daemons/backends;
- daemon-initiated remote runtime management or P-instance federation;
- local VM and Kubernetes runtime backends;
- Dockerfile and devcontainer environment providers;
- branch-specific grants and in-place runtime-policy mutation;
- general service declaration/supervision;
- attempts and Git-triggered checks;
- published ports, host/LAN access, privileged containers, and devices;
- automatic origin publication or force-publication;
- automatic session, branch, cache, or orphan reclamation;
- session migration, cloning, or recovery of uncommitted missing-runtime state;
- multi-user scheduling and authorization;
- Anthropic-compatible Bifrost integration before phase two; and
- MCP, skills, Agent Mode, Code Mode, or Envoy integration.

## V1 completion rule

V1 is complete only when the applicable acceptance criteria in the
authoritative design documents pass, every claimed backend/integration has its
validation evidence, installation and recovery are documented, and this file
contains no unresolved V1 decision or required implementation item.
