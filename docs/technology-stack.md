# P — technology stack

The implementation-facing companion to [README.md](../README.md). This
document is authoritative for implementation choices, extension seams, and
dependency policy. Subject documents remain authoritative for protocol,
environment, runtime, lifecycle, observability, and gateway behavior.

**Convention:** **Decided** is settled for MVP. **Direction** is intentionally
changeable. **Open** still needs a decision.

The [public plugin package and activation contract](plugin-contract.md) now
governs linkage. The interfaces and operating-system contracts below supply
capability behavior; public method schemas for each remaining implementation
must be fixed before that capability is linked. A bounded WASI event-handler
runner and typed session asset plans are now available.

## Stack at a glance

| Component | MVP choice | Boundary |
|---|---|---|
| Language | Go, latest pinned stable toolchain | one daemon/client binary |
| State | SQLite through `database/sql` and `modernc.org/sqlite` | P identity, policy snapshots, operations, and indexes only |
| API | NDJSON-RPC 2.0 over Unix streams | direct local Unix for MVP; SSH-to-Unix later |
| TUI | Bubble Tea, Bubbles, Lip Gloss, `sahilm/fuzzy` after prototype validation | API client; exact interaction contract remains deferred |
| Git server | Wish SSH middleware around real `git-upload-pack` and `git-receive-pack` | P refs remain Git authority |
| Runtime | local Incus, one confined user project | Incus owns instances, images, storage, state, and operations |
| Session type | unprivileged Incus system container | one instance per session UUID |
| Environment | default Nix devShell built into a private Incus image | Nix owns realization; Incus owns cached bytes |
| Interactive host | systemd unit plus fixed attach command; tmux by default | systemd owns readiness/lifetime |
| Session observability | session/policy condition, confirmed attachments, latest unattended condition | reconciled authorities plus small live/persisted state |
| Agent | Codex running as an ordinary session command | bundled status adapter; authentication remains in the session's private home |
| Model gateway | post-MVP independently configured Bifrost | retained design; not an MVP dependency |
| Events | typed handlers; MVP structured file-log handler | receives reduced P events after state changes |
| Testing | stdlib `testing` plus `go-cmp`; real Git, Incus, Nix, and tmux integration tests | fake seams for unit tests, real authorities for conformance |

Linux is the MVP daemon, runtime, and client platform. MVP clients use the
local Unix transport. Client-initiated SSH-to-Unix and native remote clients
are post-MVP. The daemon does not initiate SSH or manage a remote host as a
runtime.

## Governing principle

P orchestrates existing authorities instead of reproducing them:

- Git owns objects, refs, ancestry, transport results, and origin state.
- Incus owns runtime and image operations, instance state, root storage, and
  runtime inspection.
- Nix owns evaluation, realization, store validity, and activation semantics.
- A post-MVP Bifrost deployment owns provider/model routing and inference
  protocol compatibility.
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

`RuntimeBackend` remains the reusable placement seam. Incus is its only MVP
implementation. Product direction requires Incus support to become a bundled
runtime plugin, but this interface has not yet been reviewed or approved as
that public contract:

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
and opaque locator; only the Incus adapter interprets MVP's locator as an image
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

There is no Podman/Docker engine adapter in MVP. Incus VMs are a later instance
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

MVP has one implementation: resolve the committed repository's conventional
default Nix devShell in a disposable restricted Incus builder and publish a
verified private Incus system image. The image contains a coherent initial Nix
store/database and activation material. Each session receives a private
writable Incus root derived from it.

Product direction requires this Nix implementation to be the bundled MVP
environment plugin. `EnvironmentBuilder` is a design input; the package and
activation surface is defined by [plugin contract](plugin-contract.md), while
the public Nix method schema still needs to be fixed before linkage.

The interface remains reusable, but MVP does not specify Dockerfile, OCI, or a
format-negotiation framework. A future provider must produce an image form
accepted by its runtime backend without changing project/session/Git identity.

## Other extension seams

The interfaces in this document currently structure P core and allow
implementation substitution. Product direction now requires capability-
specific public plugin contracts for the bundled defaults. These existing
interfaces are design inputs, not automatically the approved public surface.

### Public plugin contract

[Plugin contract](plugin-contract.md) owns packaging, discovery,
compatibility, execution containment, activation, and grants. Its public
package and activation schemas are implemented for the declarative file-log
handler, the WASI event-handler method, and fixed session asset plans. Other
WASI capability methods and live session asset installation remain pending. Current Go
interfaces remain behavioral design inputs; they are not an alternate linkage
path for bundled implementations.

### Systemd interactive host

The current MVP behavior uses an operating-system contract rather than a Go
`InteractiveHost` interface. Product direction requires tmux support to be a
bundled internal-session-service plugin; the follow-up design must decide how
plugin selection supplies this contract. Every supported session image
currently supplies:

```text
p-session.target
p-interactive.service
/usr/libexec/p/attach
```

The required behavior remains: the unit owns endpoint/environment activation,
the configured command, persistent-host readiness, cgroup lifetime, and
journald diagnostics. The fixed attach command connects one Incus PTY to the
already-running host. Tmux is the shipped default. Another implementation is a
packaging/conformance choice that replaces the trusted unit and attach command
without changing lifecycle code. There is no MVP `direct` host.

### Client transport

```go
type ClientTransport interface {
    DialRPC(context.Context) (io.ReadWriteCloser, error)
    Attach(context.Context, AttachSpec) error
}
```

`UnixTransport` carries MVP RPC and the structured attachment specification.
The retained `SSHUnixTransport` direction carries the same protocol after MVP.
Terminal bytes stay outside JSON-RPC.

Attachment uses a trusted host helper. The ordinary local client receives a
short-lived pending token and structured spec, then invokes the helper and
transfers the token over private stdin/control framing, never argv. The helper
opens a dedicated attachment RPC connection, establishes the Incus channel,
confirms the token, bridges terminal bytes directly, and owns the confirmed
lease. A later SSH transport must preserve this contract.

While the daemon remains reachable, the helper retains that lease until
temporary-client teardown completes. Client/carrier loss starts teardown
independently of client cooperation. Lease loss caused by daemon restart also
starts teardown immediately, and the helper cannot establish another lease or
channel before it finishes. The systemd-owned host persists throughout
detachment. MVP has no existing-channel re-registration method.

### Agent status

Agent adapters send source-aware JSON-RPC notifications to the per-session Unix
socket. This wire protocol—not a Go agent interface—is the seam. P stores only
the latest unattended condition defined by session observability. MVP ships and
validates only the Codex adapter. Other agents may run as ordinary commands but
have no supported semantic-status integration.

Codex authentication is session-local in MVP. The user authenticates within
the session's private home; P does not copy, inject, or manage host Codex or
OpenAI credentials. Those session files survive Stop and Start and are removed
with Discard or Delete. Networked Codex use requires the project's explicitly
selected, validated `public-egress` grant; P supplies no MVP model endpoint.

### Post-MVP model gateway

P asks Bifrost's management surface for one virtual key per enabled session,
persists it securely, and installs the session endpoint/key. Sessions use the
OpenAI-compatible inference surface in phase one; Anthropic compatibility is a
later phase. Skills and MCP require separate grants/routes. P never stores
upstream provider credentials or duplicates Bifrost routing configuration.

The retained design uses the pinned Bifrost service, not a P inference proxy,
as its data-plane boundary. It requires administrative authentication, a
virtual key for every inference request, rejection of that key across the
complete non-inference route surface, and positive and negative probes before
model access is enabled. These requirements do not apply to MVP's independent
session-local Codex authentication.

### Event handlers

Core dispatches a reduced `p.event/v1` value through the selected plugin with
`plugin.DispatchEvent`. The bundled `org.p.filelog` package implements the
declarative `event.file.append` operation; a selected WASI event-handler package
uses the same dispatcher and broker grant. Handlers receive versioned, bounded,
redacted P events only after authoritative state changes. Handler failure is
diagnostic and cannot roll back state. The file-log operation appends
newline-delimited JSON to a trusted-host-configured path with restrictive
permissions and ordinary bounded rotation. P does not persist an event outbox,
retry/acknowledgement state, or a second authoritative event history. Future
logging, notification, webhook, or metrics handlers can extend this plugin
seam with their own validated capability methods.

The file-log handler is a bundled host-side declarative plugin through the
[public package and activation contract](plugin-contract.md). Its broker
operation is the first working capability. The daemon selects the handler from
trusted host configuration and delivers reduced events from a bounded in-memory
queue after state changes.

### Future isolated integrations

P retains a typed isolation boundary for any future host-side component that
must execute project-controlled content:

```go
type IsolationProvider interface {
    Run(context.Context, IsolationSpec) (RunResult, error)
}
```

The MVP Nix builder is the only required implementation and uses a disposable
Incus builder instance. Repository content cannot choose the Incus project,
mounts, network profile, credentials, or capabilities. Services, checks, and
attempts are future protocol ideas and do not create MVP implementation work.

## Project configuration

MVP uses trusted host configuration keyed by complete P project path. It owns
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
- project-scoped environment key to opaque MVP `EnvironmentHandle` index;
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

Wish supplies the SSH server and key authentication. A narrow session-channel
handler rejects environment, PTY, subsystem, forwarding, and commands other
than the two fixed Git services before invoking real Git on bare repositories.
Server policy enforces:

- a session principal may update only its currently assigned branch;
- all session updates are fast-forward only; MVP has no force-push exception;
- the per-instance host principal is read-only;
- lifecycle ref guards temporarily deny affected refs; and
- reserved future namespaces for attempts/checks remain denied in MVP.

P does not implement the pack protocol. Read-side queries shell out to Git;
`go-git` remains an optional fallback only if profiling justifies it.

The bundled `source-git` WASI package uses the versioned broker operations in
[plugin contract](plugin-contract.md#wasi-source-git-abi) to sequence bare
repository initialization, bounded ref reads, and native transport plans. The
broker alone chooses repository paths and Git argv. A per-stream private hook
callback checks the proposed ref update at Git's pre-receive boundary; the
SQLite assignment/ref-guard lease remains held through `receive-pack`.
The optional session helper is a digest-bound asset plan; live installation
belongs to the runtime lifecycle.

## API and TUI

The daemon exposes NDJSON-RPC 2.0 over Unix streams. A small stdlib dispatcher
owns framing, method/version errors, request IDs, cancellation, notifications,
and bounded diagnostics. The stable method/event surface is documented in
[communication boundaries](communication-boundaries.md).

The TUI is a pure client. Bubble Tea, Bubbles, Lip Gloss, and `sahilm/fuzzy`
remain the preferred implementation set, but exact layout, navigation, keys,
and the first vertical slice require a prototype before becoming an MVP
interaction contract. Lifecycle, authorization, and recovery decisions remain
daemon-owned and equally available through `p api`.

## Testing and version policy

Unit tests use stdlib `testing`, table-driven fixtures, fake interfaces, and
`go-cmp`. MVP integration/conformance tests exercise real pinned versions of
Git, Incus, Nix, tmux, the Codex adapter, the local client transport, and
SQLite crash recovery. Bifrost and the SSH client transport have post-MVP
gates.

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
| `charm.land/wish/v2` v2.0.3 | MIT | Git SSH server; selected for compatibility with pinned Go 1.26.7 |
| `charm.land/ssh` v0.4.3, `golang.org/x/crypto` v0.57.0 | BSD-3 | SSH protocol implementation |
| `sahilm/fuzzy` | MIT | filtering |
| `modernc.org/sqlite` | BSD-3 | CGo-free SQLite driver |
| `tetratelabs/wazero` | Apache-2.0 | bounded WASI plugin runner |
| `google/go-cmp` | BSD-3 | test diffs |
| `google/go-licenses` | Apache-2.0 | CI license gate |

MVP external binaries are `git`, `incus`, `systemd`, `tmux`, `ssh`, Codex,
Python, and the pinned `nix` binary. SSH remains necessary for the P Git service and origin
access, not for P client transport. Nix runs inside P's builder and session
images rather than being a host runtime dependency. Bifrost is a post-MVP
external service. The Incus Go client is not a planned direct dependency until
the CLI adapter demonstrates a concrete limitation.

A new direct dependency requires a short decision record: alternatives,
benefit over local code/process invocation, license, and authority impact.

**SQLite driver decision (step 2):** `modernc.org/sqlite v1.39.1` is pinned in
`go.mod` under its BSD-3 license. It supplies transactions and WAL through
`database/sql` without CGo or a separate `sqlite3` process. The alternatives
were a CGo-linked SQLite driver or CLI calls; either would add a native build
or process boundary without improving P's single-writer state contract. This
driver opens only the daemon's private control database and adds no Git,
runtime, network, or plugin authority.

**Codex adapter interpreter decision (step 10a):** the pinned Nixpkgs Python
package (PSF license) runs the small bundled adapter as the session user.
Its standard library supplies strict JSON parsing, private file creation,
bounded subprocess execution, and timed Unix-socket transport without a new
host service. Alternatives were Bash plus `jq`, a native Go asset, or a
Codex-specific runtime-kit helper. Python keeps the versioned mapping inside
the selected plugin and below the one-MiB asset limit, with no additional
third-party Python modules. The fixed guest interpreter runs in isolated mode;
this adds no host filesystem, Incus, credential, or cross-session authority.
Codex itself is pinned to `0.151.0`; fixture checks do not satisfy the pending
authenticated acceptance gate.

**WASI runner decision (step 3a):** `github.com/tetratelabs/wazero v1.12.0`
is pinned in `go.mod`; its pinned module contains an Apache-2.0 `LICENSE`.
Wazero supplies an in-process WebAssembly interpreter with an explicit memory
limit, context cancellation, WASI Preview 1 imports, and typed host functions.
Alternatives were a native plugin process with OS sandboxing or a locally
written WebAssembly interpreter. A native process would require a separate
syscall and namespace policy to remove ambient filesystem and network
authority; writing an interpreter would add a larger unreviewed execution
surface. Wazero receives no host filesystem, network, environment, or command
imports. The only new authority is the explicitly granted, operation-scoped
broker call; the interpreter does not own P identity or policy.

## Deliberate rigidities

- Git's object, ref, and wire model is P's interchange; there is no generic VCS
  provider.
- Linux is the execution platform, not an OS provider.
- Incus is the only MVP runtime implementation, despite the retained backend
  interface.
- Nix devShell-to-Incus-image is the only MVP environment implementation,
  despite the retained builder interface.
- P instances coordinate across machines only through a shared Git origin;
  daemon federation and runtime migration are out of scope.
