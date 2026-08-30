# P — V1 status

Current snapshot of P's design and implementation readiness.

> **Status: non-normative snapshot, reviewed 2026-08-20.** Subject design
> documents remain authoritative. [Missing pieces](missing-pieces.md) tracks
> remaining work.

## Executive state

P has a coherent V1 architecture but no production implementation. The former
runtime/environment conflict is resolved: one confined local Incus project is
the V1 runtime boundary, unprivileged Incus system containers are sessions, and
the committed default Nix devShell becomes a cached private Incus image with a
private writable root per session.

Two product-operation areas still need bounded contracts before a complete V1
implementation plan:

1. project registration/removal and retained unassigned-ref lifecycle; and
2. the first TUI vertical slice and interaction contract.

Concrete schemas, adapters, tests, and real-machine evidence should be defined
alongside implementation. They do not require reopening the product model.

## Settled model

### Instance and machines

- One P instance owns one daemon, SQLite registry, P Git server, and one local
  confined Incus execution project.
- A daemon never manages Incus on another host and P instances do not
  communicate directly.
- Shared ordinary Git origin is the only V1 cross-machine handoff. No origin
  means local-only and all origin behavior is bypassed.

### Projects, sessions, and Git

- A project is the complete repository path on the P Git server.
- A session has an immutable UUID and owns one `(project, branch)` assignment.
- Every session starts from committed source and immediately creates a new real
  branch; there are no scratch sessions or durable base-commit field.
- Rename changes the branch ref while retaining the UUID and instance.
- A session SSH key may read project branches and update only its assigned
  branch, fast-forward-only by default.
- The host P key is read-only. The daemon alone creates, guards, renames, and
  deletes P refs.
- Origin refresh/publication uses the host user's existing SSH credentials.
  Publication is explicit, one-ref, create-or-fast-forward, and idempotent on
  explicit retry. P maintains no publication ledger or hidden tracking ref.

### Runtime and Nix environment

- Incus is the only V1 `RuntimeBackend`; Podman, Docker, remote Incus, Incus
  clustering, VMs, and Kubernetes are not V1 runtime implementations.
- Incus owns instances, runtime state/operations, storage, and cached images.
  SQLite only owns P identity/policy/workflows and indexes Incus identifiers.
- P uses a pre-provisioned confined Incus user project and must not receive the
  host-root-equivalent administrative socket.
- A disposable Incus builder realizes the committed conventional default Nix
  devShell and publishes a verified immutable, project-scoped Incus system
  image. V1 does not reuse it across projects because retained Nix paths may
  contain committed source-derived content.
- Each session receives a private writable instance root derived from that
  image, including its own `/nix`, workspace, and home. No host or shared
  writable Nix store/daemon is mounted.
- Existing sessions may build additional private Nix paths. They do not mutate
  the cached image or another session.
- No Dockerfile/OCI environment provider is specified in V1, while the
  `EnvironmentBuilder` interface remains reusable.

### Policy and isolation

- Trusted host configuration is keyed by complete project path and snapshotted
  at creation. Repository content cannot configure P or widen authority.
- V1 policy is project-scoped. Branch-scoped grants are later.
- `none` networking is the baseline; `public-egress` is enabled only after
  proving host/LAN/private/metadata/sibling/Incus access remains blocked.
- Named filesystem grants must fit within both P validation and the confined
  Incus project's path ceiling.
- Sessions receive only their P Git key, private session RPC endpoint, optional
  Bifrost key/endpoint, and explicit filesystem grants. No Incus socket, host
  SSH agent, origin credential, upstream model credential, privileged mode,
  device, or published port enters a session.

### Lifecycle and communication

- Create, start, attach/detach, rename, stop, discard, delete, repair, and
  abandonment have defined outcomes.
- Cross-authority changes use persisted P workflows. Incus-owned start/stop
  use Incus operation/state; Git publication uses Git's atomic result and
  explicit idempotent retry.
- Stop retains the private instance root but terminates its processes.
- Discard removes the session/instance but retains an unassigned P branch;
  delete additionally deletes the confirmed P branch.
- Git carries source. NDJSON-RPC carries control/status. Attachment carries a
  validated command and terminal stream. Bifrost HTTP carries model traffic.
- Linux clients support direct Unix and client-initiated SSH-to-Unix transport
  from day one. The daemon does not initiate SSH.

### Presentation and integrations

- The TUI is a thin RPC client.
- Tmux is the default interactive host; `direct` proves tmux is replaceable.
- Observability is runtime condition, current-generation startup readiness,
  confirmed live attachment count, and one nullable latest unattended agent
  condition. Confirming the first attachment clears the latter; a failed attach
  does not.
- Bifrost remains independently configured. P persists/revokes one virtual key
  for each enabled session; OpenAI-compatible inference is phase one and
  Anthropic-compatible inference is later. Native Bifrost authentication must
  require a session key for every inference request and reject that key on
  every administrative or other non-V1 route; an unvalidated boundary fails
  model access closed.
- Services are removed from V1. Checks and attempts remain only future ideas at
  the Git/RPC boundary.

## Still unresolved before the complete implementation plan

| Subject | Missing contract |
|---|---|
| Project/repository lifecycle | registration and seeding, source locations, project removal/rename posture, retained unassigned-ref selection/rename/delete/publication |
| Initial TUI slice | first screen/navigation, creation and progress flow, terminal handoff, core actions, errors, and smallest useful milestone |

The exact configuration schema, SQLite migrations, RPC catalog, `AttachSpec`,
Incus CLI/API mapping, base-image build, state layout, dependency pins, and
operator procedures are implementation-owned contracts tracked in
[missing pieces](missing-pieces.md).

## Evidence still required

- confined Incus user access without administrative authority;
- instance lifecycle, pause/inspection, metadata, storage, and orphan behavior;
- correct cached Nix image plus private per-session store deltas on real
  `x86_64-linux` and `aarch64-linux` hosts;
- `none` and gated `public-egress` network policy over IPv4 and IPv6;
- lifecycle crash recovery and Git authorization/race behavior;
- Unix/SSH-to-Unix reconnect, pending/confirmed attachment behavior, and
  helper-bound teardown after lease, client, helper, SSH, or network loss;
- durable current-generation startup readiness across restart and diagnostic
  expiry, including `stop_required` recovery for a running `not_ready` runtime;
- creation retry recovery through persisted `stopping-partial-runtime` before
  another startup generation, plus atomic/spoof-resistant marker writes;
- Bifrost session-key and positive/negative route restrictions; and
- versioned Claude Code/Codex event mapping.

These validations run alongside development and block only the claims they
cover. See [development validations](development-validations.md).

## Recommended resume order

1. define project/repository lifecycle;
2. define the first TUI vertical slice;
3. turn the remaining implementation contracts into milestones;
4. build state/RPC/Git skeleton plus the Incus adapter;
5. build and validate the Nix environment-image path; and
6. integrate the TUI, lifecycle recovery, Bifrost, and notifications in
   progressively complete slices.

P's identity, authority, runtime, environment, transport, lifecycle, and
security posture are sufficiently clear to begin implementation. The remaining
design gaps are bounded product workflows, not a disputed core architecture.
