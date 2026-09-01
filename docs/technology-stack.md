# P — technology stack

The implementation-facing companion to [README.md](../README.md). This
document is authoritative for implementation choices, extension seams, and
dependency policy. Subject documents remain authoritative for protocol,
environment, runtime, lifecycle, observability, and gateway behavior.

**Convention:** **Decided** is settled for V1. **Direction** is intentionally
changeable. **Open** still needs a decision.

## Stack at a glance

| Component | V1 choice | Boundary |
|---|---|---|
| Language | Go, latest pinned stable toolchain | one daemon/client binary |
| State | SQLite through `database/sql` and `modernc.org/sqlite` | P identity, policy snapshots, operations, and indexes only |
| API | NDJSON-RPC 2.0 over Unix streams | direct Unix or client-initiated SSH-to-Unix |
| TUI | Bubble Tea, Bubbles, Lip Gloss, `sahilm/fuzzy` after prototype validation | API client; exact interaction contract remains deferred |
| Git server | Wish SSH middleware around real `git-upload-pack` and `git-receive-pack` | P refs remain Git authority |
| Runtime | local Incus, one confined user project | Incus owns instances, images, storage, state, and operations |
| Session type | unprivileged Incus system container | one instance per session UUID |
| Environment | default Nix devShell built into a private Incus image | Nix owns realization; Incus owns cached bytes |
| Interactive host | systemd unit plus fixed attach command; tmux by default | systemd owns readiness/lifetime |
| Session observability | session/policy condition, confirmed attachments, latest unattended condition | reconciled authorities plus small live/persisted state |
| Model gateway | independently configured Bifrost | P owns only session-key lifecycle and endpoint grant |
| Events | typed handlers; V1 structured file-log handler | receives reduced P events after state changes |
| Testing | stdlib `testing` plus `go-cmp`; real Git, Incus, Nix, and tmux integration tests | fake seams for unit tests, real authorities for conformance |

Linux is the V1 daemon and runtime platform. Linux clients support local Unix
and SSH-to-Unix transports from day one. The daemon does not initiate SSH and
does not manage a remote host as a runtime.

## Governing principle

P orchestrates existing authorities instead of reproducing them:

- Git owns objects, refs, ancestry, transport results, and origin state.
- Incus owns runtime and image operations, instance state, root storage, and
  runtime inspection.
- Nix owns evaluation, realization, store validity, and activation semantics.
- Bifrost owns provider/model routing and inference protocol compatibility.
- systemd and the selected persistent host own readiness, process lifetime,
  logs, and terminal persistence behavior.
- SQLite owns P's session identity, branch assignment, normalized policy,
  cross-authority workflow intent, and small authority indexes.

An operation becomes a durable P workflow only when its ordered outcome crosses
authorities. P does not mirror an Incus start/stop operation, Git push result,
or image byte store in SQLite.

P prefers stable structured CLI output while implementation is young and uses
a library/API client only when it materially reduces ambiguity or security
risk. Every external version is pinned by the development environment and
validated before support is claimed.

## Runtime and Incus access

`RuntimeBackend` remains the reusable placement seam. Incus is its only V1
implementation:

```go
type RuntimeBackend interface {
    Create(context.Context, SessionSpec, EnvironmentHandle) (RuntimeLocator, error)
    List(context.Context) ([]RuntimeInfo, error)
    Inspect(context.Context, RuntimeLocator) (RuntimeInfo, error)
    Start(context.Context, RuntimeLocator) (RuntimeOperation, error)
    Pause(context.Context, RuntimeLocator) (RuntimeOperation, error)
    Resume(context.Context, RuntimeLocator) (RuntimeOperation, error)
    WorkspaceStatus(context.Context, RuntimeLocator) (WorkspaceStatus, error)
    WorkspaceOperation(context.Context, RuntimeLocator, WorkspaceOperation) error
    Attach(context.Context, RuntimeLocator) (AttachSpec, error)
    Stop(context.Context, RuntimeLocator) (RuntimeOperation, error)
    Remove(context.Context, RuntimeLocator) (RuntimeOperation, error)
    Events(context.Context) (<-chan RuntimeEvent, error)
}
```

`SessionSpec` contains backend-neutral session and policy input. The separate
`EnvironmentHandle` carries a target kind/contract version, content identity,
and opaque locator; only the Incus adapter interprets V1's locator as an image
fingerprint.

The Incus adapter is always bound to the configured local confined user
project. It uses deterministic instance names and P metadata, passes structured
argv, and queries Incus operations/state after interrupted calls. It never:

- accepts an arbitrary Incus remote or project from a repository/session;
- uses the full administrative socket;
- passes any Incus socket into a builder or session; or
- implements Incus clustering or remote-instance management.

The first implementation may use the Incus CLI with JSON output. Moving to the
official Go client is an internal adapter decision if CLI contracts prove
insufficient; it must not widen authority. P runs under a user that has access
only to the confined socket/project. Configuration that also grants that user
unrestricted Incus administration is unsupported.

There is no Podman/Docker engine adapter in V1. Incus VMs are a later instance
type behind the same backend when their contract is validated. A Kubernetes P
deployment is a later separate backend/placement implementation; an SSH host is
never a backend.

## Environment builder

The environment seam separates project environment interpretation from session
lifecycle:

```go
type EnvironmentBuilder interface {
    Resolve(context.Context, EnvironmentRequest) (EnvironmentPlan, error)
    Build(context.Context, EnvironmentPlan, EnvironmentTarget) (EnvironmentHandle, error)
}
```

V1 has one implementation: resolve the committed repository's conventional
default Nix devShell in a disposable restricted Incus builder and publish a
verified private Incus system image. The image contains a coherent initial Nix
store/database and activation material. Each session receives a private
writable Incus root derived from it.

The interface remains reusable, but V1 does not specify Dockerfile, OCI, or a
format-negotiation framework. A future provider must produce an image form
accepted by its runtime backend without changing project/session/Git identity.

## Other extension seams

### Systemd interactive host

V1 uses an operating-system contract rather than a Go `InteractiveHost`
plugin. Every supported session image supplies:

```text
p-session.target
p-interactive.service
/usr/libexec/p/attach
```

The unit owns endpoint/environment activation, the configured command,
persistent-host readiness, cgroup lifetime, and journald diagnostics. The
fixed attach command connects one Incus PTY to the already-running host. Tmux
is the shipped default. Another implementation is a packaging/conformance
choice that replaces the trusted unit and attach command without changing
lifecycle code. There is no V1 `direct` host.

### Client transport

```go
type ClientTransport interface {
    DialRPC(context.Context) (io.ReadWriteCloser, error)
    Attach(context.Context, AttachSpec) error
}
```

`UnixTransport` and `SSHUnixTransport` carry the same RPC and structured attach
specification. Terminal bytes stay outside JSON-RPC.

Attachment uses a trusted host helper shared by local and SSH transports. The
ordinary client receives a short-lived pending token and structured spec, then
invokes the helper and transfers the token over private stdin/control framing,
never argv. The helper opens a dedicated attachment RPC connection, establishes
the Incus channel, confirms the token, bridges terminal bytes directly, and
owns the confirmed lease.

While the daemon remains reachable, the helper retains that lease until
temporary-client teardown completes. Client/SSH/carrier loss starts teardown
independently of client cooperation. Lease loss caused by daemon restart also
starts teardown immediately, and the helper cannot establish another lease or
channel before it finishes. The systemd-owned host persists throughout
detachment. V1 has no existing-channel re-registration method.

### Agent status

Agent adapters send source-aware JSON-RPC notifications to the per-session Unix
socket. This wire protocol—not a Go agent interface—is the seam. P stores only
the latest unattended condition defined by session observability.

### Model gateway

P asks Bifrost's management surface for one virtual key per enabled session,
persists it securely, and installs the session endpoint/key. Sessions use the
OpenAI-compatible inference surface in phase one; Anthropic compatibility is a
later phase. Skills and MCP require separate grants/routes. P never stores
upstream provider credentials or duplicates Bifrost routing configuration.

The pinned Bifrost service, not a P inference proxy, enforces the V1 data-plane
boundary. Administrative authentication is enabled, every inference request
requires a valid virtual key, and the real session key must be rejected by the
complete inventoried non-V1 route surface. Model access fails closed until
positive and negative probes validate the effective configuration and pinned
version during initial session creation. After the session is established,
Start and Attach do not probe Bifrost; gateway failure degrades only model
discovery and inference.

### Event handlers

```go
type EventHandler interface {
    Handle(context.Context, Event) error
}
```

Handlers receive versioned, bounded, redacted P events only after authoritative
state changes. Handler failure is diagnostic and cannot roll back state. V1's
`FileEventHandler` appends newline-delimited JSON to a trusted-host-configured
path with restrictive permissions and ordinary bounded rotation. P does not
persist an event outbox, retry/acknowledgement state, or a second authoritative
event history. Future logging, notification, webhook, or metrics handlers reuse
this interface.

### Future isolated integrations

P retains a typed isolation boundary for any future host-side component that
must execute project-controlled content:

```go
type IsolationProvider interface {
    Run(context.Context, IsolationSpec) (RunResult, error)
}
```

The V1 Nix builder is the only required implementation and uses a disposable
Incus builder instance. Repository content cannot choose the Incus project,
mounts, network profile, credentials, or capabilities. Services, checks, and
attempts are future protocol ideas and do not create V1 implementation work.

## Project configuration

V1 uses trusted host configuration keyed by complete P project path. It owns
project-scoped session defaults and grants. The repository contributes only its
ordinary default Nix devShell; it does not contain a P schema, select a backend,
configure event handlers, or widen authority. Branch-scoped policy is reserved
for later.

## SQLite boundary

SQLite runs in WAL mode with embedded migrations and the daemon as its only
writer. It stores only facts P owns or needs to index, including:

- project paths, immutable session UUIDs, and UUID-to-branch assignments;
- normalized project-policy snapshots;
- configured Incus project and deterministic UUID-to-instance relationship;
- project-scoped environment key to opaque V1 `EnvironmentHandle` index;
- cross-authority lifecycle operations and cleanup/orphan records;
- protected session credential material/identifiers;
- latest bounded Start/systemd diagnostic and current reconciled session/policy
  projection, never as Incus/systemd authority;
- one nullable latest unattended condition; and
- the latest bounded origin observation, never as Git authority.

It does not store Git objects, Nix store records, Incus image/instance bytes,
pending/active attachment presence, or a second copy of authority-owned
operation state.

## Git server

Wish middleware authenticates host and session SSH keys and invokes the real
Git service commands on bare repositories. Server policy enforces:

- a session principal may update only its currently assigned branch;
- all session updates are fast-forward only; V1 has no force-push exception;
- the per-instance host principal is read-only;
- lifecycle ref guards temporarily deny affected refs; and
- reserved future namespaces for attempts/checks remain denied in V1.

P does not implement the pack protocol. Read-side queries shell out to Git;
`go-git` remains an optional fallback only if profiling justifies it.

## API and TUI

The daemon exposes NDJSON-RPC 2.0 over Unix streams. A small stdlib dispatcher
owns framing, method/version errors, request IDs, cancellation, notifications,
and bounded diagnostics. The stable method/event surface is documented in
[communication boundaries](communication-boundaries.md).

The TUI is a pure client. Bubble Tea, Bubbles, Lip Gloss, and `sahilm/fuzzy`
remain the preferred implementation set, but exact layout, navigation, keys,
and the first vertical slice require a prototype before becoming a V1
interaction contract. Lifecycle, authorization, and recovery decisions remain
daemon-owned and equally available through `p api`.

## Testing and version policy

Unit tests use stdlib `testing`, table-driven fixtures, fake interfaces, and
`go-cmp`. Integration/conformance tests exercise real pinned versions of Git,
Incus, Nix, tmux, Bifrost where feasible, both client transports, and SQLite
crash recovery.

Support is claimed only after the relevant validation in
[development validations](development-validations.md) passes. In particular,
the configured Incus project/storage/network combination must prove confinement,
workspace inspection, lifecycle operations, cached-image correctness, systemd
host readiness/exit behavior, confirmed attachment semantics, and no
host/private-network access.

## Licensing policy

P is licensed under **Apache-2.0**.
Compiled dependencies must use permissive licenses such as MIT, Apache-2.0,
BSD, or ISC. CI runs `go-licenses` over the full transitive tree. GPL/LGPL tools
such as Git and Nix remain separate processes, not linked dependencies.

Planned direct dependencies:

| Dependency | License | Role |
|---|---|---|
| `charmbracelet/bubbletea` | MIT | TUI runtime |
| `charmbracelet/bubbles` | MIT | TUI components |
| `charmbracelet/lipgloss` | MIT | TUI styling |
| `charmbracelet/wish` | MIT | Git SSH server middleware |
| `gliderlabs/ssh`, `golang.org/x/crypto` | BSD-3 | SSH implementation |
| `sahilm/fuzzy` | MIT | filtering |
| `modernc.org/sqlite` | BSD-3 | CGo-free SQLite driver |
| `google/go-cmp` | BSD-3 | test diffs |
| `google/go-licenses` | Apache-2.0 | CI license gate |

External services/binaries are `git`, `incus`, `systemd`, `tmux`, `ssh`, and
Bifrost. The pinned `nix` binary runs inside P's builder
and session images rather than being a host runtime dependency. The Incus Go
client is not a planned direct dependency until the CLI adapter demonstrates a
concrete limitation.

A new direct dependency requires a short decision record: alternatives,
benefit over local code/process invocation, license, and authority impact.

## Deliberate rigidities

- Git's object, ref, and wire model is P's interchange; there is no generic VCS
  provider.
- Linux is the execution platform, not an OS provider.
- Incus is the only V1 runtime implementation, despite the retained backend
  interface.
- Nix devShell-to-Incus-image is the only V1 environment implementation,
  despite the retained builder interface.
- P instances coordinate across machines only through a shared Git origin;
  daemon federation and runtime migration are out of scope.
