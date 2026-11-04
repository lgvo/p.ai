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
| Language | Go, latest pinned stable toolchain | one daemon/client binary, `p`, and the two driver executables, `p-runtime-incus` and `p-source-git` |
| State | SQLite through `database/sql` and `modernc.org/sqlite` | P identity, policy snapshots, operations, and indexes only |
| API | NDJSON-RPC 2.0 over Unix streams | direct local Unix for MVP; SSH-to-Unix later |
| TUI | Bubble Tea v2 and Lip Gloss v2 | Live API client; built-in subsequence filtering |
| Git server | P's SSH server on Wish in core; Git, served by the source driver over real `git-upload-pack` and `git-receive-pack`, its one service | P refs remain Git authority |
| Runtime | local Incus, one confined user project, reached through the supervised `p-runtime-incus` driver | Incus owns instances, images, storage, state, and operations |
| Session type | unprivileged Incus system container | one instance per session UUID |
| Environment | one private Incus image per source commit: its default Nix devShell and a clone of the commit | Nix owns realization; Incus owns image bytes; SQLite indexes images by commit |
| Interactive host | the Incus driver's own systemd unit and fixed attach command, with tmux | systemd owns readiness/lifetime |
| Session observability | session/policy condition, confirmed attachments, latest unattended condition | reconciled authorities plus small live/persisted state |
| Agent | Codex running as an ordinary session command | bundled status adapter; authentication remains in the session's private home |
| Model gateway | post-MVP independently configured Bifrost | retained design; not an MVP dependency |
| Events | typed handlers; MVP structured file-log handler | receives reduced P events after state changes |
| Testing | stdlib `testing` plus `go-cmp`; real Git, Incus, Nix, and tmux integration tests | fake seams for unit tests, real authorities for conformance |

MVP installation is supported only on NixOS with local Incus system
containers. Other Linux distributions and container engines are outside the
installation support gate. MVP clients use the local Unix transport.
Client-initiated SSH-to-Unix and native remote clients
are post-MVP. The daemon does not initiate SSH or manage a remote host as a
runtime.

The MVP distribution includes pinned packages, service configuration and
diagnostics for that host. Backup/restore and software upgrade/rollback are
outside MVP delivery gates. This does not remove transactional lifecycle
recovery or persistence requirements; [project lifecycle](project-lifecycle.md)
owns retained local Git data and [session lifecycle](session-lifecycle.md)
owns Stop/Start and daemon-restart recovery.

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

The runtime driver, `internal/runtimedriver`, is the seam that owns runtime
machinery: images and their builds, session runtimes, the session host,
project services, terminals, devices, network, and the proofs of its own
boundary. Core addresses it in P's terms, through the operations in
[`internal/runtimedriver/driver.go`](../internal/runtimedriver/driver.go): the
host (`Describe`, `Check`, `Inventory`), sessions (`Inspect`, `Create`,
`Start`, `Stop`, `Delete`, `HostLog`, `OpenTerminal`), the workspace of a stopped session
(`ReadWorkspace`, `WriteWorkspace`, `RemoveWorkspace`), images (`BuildImage`,
`Images`, `InspectImage`, `DeleteImage`), `Settle` for an effect whose outcome
is unknown, and project services (`Services`, `ServiceAction`, `ServiceLogs`).

`Session` is core's spec of one runtime: the session reference and project,
the image, the interactive command, the workspace's repository, branch and
commit, the session's Git key and P's pinned Git host key, the endpoint
directory, the grants, the network and the selected agent adapter's files. The
driver decides where each part goes in the guest; the only guest place core
knows is the workspace of a stopped session. A mutation takes a gate, a core
function the driver calls right before its first effect, where core records
its durable intent. An effect whose outcome is unknown is settled from native
state, never sent again. An incarnation names one runtime at a reference: the
same across stop and start, new when the runtime is re-created.

The driver is a separate executable. Host configuration pins it by path and
SHA-256 in `runtime.driver`, with that driver's own settings
([control API](control-api.md)). The only driver is the Incus driver
(`internal/runtimedriver/incus`), which the `p-runtime-incus` executable
shipped with P serves. The daemon starts the executable after its launch
checks, supervises it, and restarts it when it dies
([driver executable](runtime-isolation.md#driver-executable)); it holds only
`runtimedriver.Driver`, which the protocol client implements.

The driver protocol (`internal/runtimedriver/protocol`) is JSON-RPC 2.0 over a
Unix socket pair the daemon creates, one newline-terminated JSON frame of at
most 16 MiB per message, decoded with P's strict JSON rules. Requests go both
ways: core calls the driver's operations, and the driver calls core's gates.
It begins with a hello, in which core sends the protocol version
(`p.runtime-driver/v1`), the driver's `config` and the settings core owns that
the driver needs, and the driver answers with its agreed version and
capabilities or refuses; a version mismatch on the first start refuses daemon
start. Each terminal has its own connection, a socket pair whose far end core
passes with the open request; it carries the terminal's bytes only. A call
whose connection is lost ends with an unknown outcome if it is an effect that
may have happened, which the restarted driver settles, and as unavailable
otherwise. Errors keep their kinds across the connection.

An in-memory driver (`internal/runtimedriver/memory`) and a conformance suite
(`internal/runtimedriver/conformance`) show that a driver keeps the contract;
the Incus driver runs the suite over a stubbed Incus, and the memory driver
runs it again behind the protocol. Every runtime effect goes
through the driver: core reaches images, sessions, terminals, removal,
rename's workspace edits, project services, the boundary check and the
inventory through it, and computes admission and the proofs of absence from
`Inspect` and `Inventory`. Core records runtimes by the driver's opaque
incarnation and shows the driver's own name for a runtime only for display.
A boundary test fails if code other than `cmd/p-runtime-incus` and the Incus
VM test fixtures imports the Incus package, or if other non-test code outside
the driver names the `incus` client or the Incus sockets.

The Incus driver is always bound to the configured local confined user
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
type behind the same driver when their contract is validated. A Kubernetes P
deployment is a later separate driver; an SSH host is never a runtime.

## Runtime driver images

Images go through the image part of the runtime driver, `internal/runtimedriver`,
which separates building and storing images from session lifecycle: its
`Images` part (`Describe`, `BuildImage`, `Images`, `InspectImage`,
`DeleteImage`, `Settle`, and `Delete` for a build's leftovers, never an image).

Core names P's concepts: a build gets a new build reference, the image's
labels (project, source commit, base image, system, builder version, build)
and a Git bundle of the commit, which the source driver exported. The driver
builds, stores, lists and deletes images, calls core's gate right before each
mutation, and settles an effect whose outcome is unknown from native state.
Each image records its devShell outcome beside its labels. Core keeps the
commit-image index and the rule for deleting images
([environment building](environment-building.md)).

MVP has one implementation, the Incus driver, which runs in the
`p-runtime-incus` executable. It clones the commit in a disposable restricted
Incus builder, resolves and realizes the conventional default Nix devShell
natively, and publishes a verified private Incus system image. The image
contains a coherent initial Nix store/database, activation material and the
clone. Each session receives a private writable Incus root derived from it. No
plugin takes part.
The runtime driver is not a plugin contract: trusted host configuration pins
its executable, and core reaches it through the Go interface carried over the
versioned driver protocol.

An image only has to work with the runtime that built it. MVP does not specify
Dockerfile, OCI, or a format-negotiation framework.

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
`InteractiveHost` interface. How a runtime runs the session host is the
runtime driver's own business: the Incus driver holds the tmux host's systemd
units and attach program as its own files, installs them in each session, and
checks them before each attachment; no plugin or host setting selects them.
Every supported session image links them at:

```text
p-session.target
p-interactive.service
/usr/libexec/p/attach
```

The required behavior remains: the unit owns endpoint/environment activation,
the configured command, persistent-host readiness, cgroup lifetime, and
journald diagnostics. The fixed attach command connects one Incus PTY to the
already-running host. Tmux is the Incus driver's host. Another implementation
is a change to the driver that replaces the trusted unit and attach command
without changing lifecycle code. There is no MVP `direct` host.

### Client transport

```go
type ClientTransport interface {
    DialRPC(context.Context) (io.ReadWriteCloser, error)
}
```

`UnixTransport` carries MVP RPC, including the attachment claim and the
terminal the daemon then relays on the claim's connection. The retained
`SSHUnixTransport` direction carries the same protocol after MVP. Terminal
bytes travel as terminal frames after the claim, outside JSON-RPC.

Attachment uses a trusted host helper. The ordinary local client receives only
a short-lived pending token, then invokes the helper and transfers the token
over private stdin/control framing, never argv. The helper claims the token on
its own control connection; the daemon opens the terminal through the runtime
driver and relays it over that connection, and the helper carries bytes
between it and the client. Neither the client nor the helper has runtime
authority. A later SSH transport must preserve this contract.

The daemon keeps the attachment's lease until the runtime driver proves the
attached process ended. Client or carrier loss closes the helper's connection
and the daemon closes the terminal, independently of client cooperation. A
daemon restart ends attachments. The systemd-owned host persists throughout
detachment. MVP has no existing-terminal re-registration method.

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

SQLite runs in WAL mode with the daemon as its only writer. One embedded
schema creates the state in a new state directory; P does not migrate state
and refuses a database of another schema version. It stores only facts P owns
or needs to index, including:

- project paths, immutable session UUIDs, and UUID-to-branch assignments;
- normalized project-policy snapshots;
- configured Incus project and deterministic UUID-to-instance relationship;
- the commit-image index: (project, source commit, base image, system, builder
  version) to the runtime driver's image reference, with its release state;
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

P's SSH server, `internal/sshserver`, is core's: Wish supplies the server and
the host key, and core authenticates each key as an active Git principal with
user `git`. A narrow session channel refuses environment, PTY, shell,
subsystem and forwarding requests and accepts only an exec whose first word a
service declared; it hands that authenticated stream to the service. Git is
the one service: it is registered for the command words the source driver
declares in `Describe()`, so core holds no Git command word, and the driver's
`ServeGit` parses the rest of the command and runs real Git on P's
repositories. `ServeGit` calls core once to open the service, which takes the
lease of Git authority and sets the 1 GiB and 10-minute ceilings, and once to
approve the one head update a push makes after its pre-receive check, which
consumes the unborn `main` grant for a creation. Server policy enforces:

- a session principal may update only its currently assigned branch;
- all session updates are fast-forward only; MVP has no force-push exception;
- the per-instance host principal is read-only;
- lifecycle ref guards temporarily deny affected refs; and
- reserved future namespaces for attempts/checks remain denied in MVP.

P does not implement the pack protocol. The bundled driver's read-side
queries shell out to Git; `go-git` remains an optional fallback only if
profiling justifies it.

The source driver, `internal/sourcedriver`, is the seam that owns P's
repositories and every Git effect, as the runtime driver owns runtime
machinery. Core addresses it in P's terms through the operations in
[`internal/sourcedriver/driver.go`](../internal/sourcedriver/driver.go): the
host (`Describe`, `Check`), Git's syntax for the names P stores
(`CheckNames`), repositories, heads with compare-and-swap, commits (including
the commit `Export` for image builds), the Git edits that rename a session's
clone (`RenameWorkspace`), the Git service behind P's endpoint (`ServeGit`),
the origin, and `Settle` for an effect whose outcome is unknown.
The driver is a separate executable, as the runtime driver is: host
configuration pins it by path and SHA-256 in `git.driver`, with that driver's
own settings ([control API](control-api.md)), and core records the driver's
backend at its first start and refuses to start with another backend while
any project exists. The only driver is the Git driver,
`internal/sourcedriver/git`, which the `p-source-git` executable shipped with
P serves over the state directory. The daemon starts, checks and supervises
it as it does the runtime driver
([driver executable](runtime-isolation.md#driver-executable)); it holds only
`sourcedriver.Driver`, which the protocol client implements, and no other
code runs `git` or `ssh` on the host.

The source driver protocol (`internal/sourcedriver/protocol`,
`p.source-driver/v1`) shares its mechanism with the runtime driver's in
`internal/driverwire`; what it carries, the `ServeGit` streams included, is in
[communication boundaries](communication-boundaries.md#daemon-and-source-driver).
An in-memory driver (`internal/sourcedriver/memory`) and a conformance suite
(`internal/sourcedriver/conformance`) show that a driver keeps the contract;
the Git driver runs the suite on real Git, and both run it again behind the
protocol.
P's repositories, heads, commits, the commit export for image builds, Git's
syntax for names and origin URLs, the Git edits of a renamed session's clone,
and the origin's observation, fetch, comparison and push go through the
driver.
Core holds each project's origin lock and decides what an origin operation
may do; the driver performs it. Core makes each build's private staging
directory, the driver exports the commit's Git bundle into it, and core
removes it after the build.
The Git service runs in the driver too: its pre-receive hook,
`p-source-git git-hook`, checks the proposed ref update and asks the driver
over a per-push socket, and core's lease is held until
`ServeGit` returns from a served or refused service, or, after a lost call,
until `Settle` reports the service ended. The session's Git SSH wrapper, `/usr/libexec/p/git-ssh`, is a file of
the runtime driver, which installs it in every session.

## API and TUI

The daemon exposes NDJSON-RPC 2.0 over Unix streams. A small stdlib dispatcher
owns framing, method/version errors, request IDs, cancellation, notifications,
and bounded diagnostics. The stable method/event surface is documented in
[communication boundaries](communication-boundaries.md).

The TUI is a pure client implemented with Bubble Tea v2 and Lip Gloss v2.
The reviewed prototype guides its responsive session browser; the user guide
owns its implemented keys and limitations. Lifecycle, authorization, and
recovery decisions remain daemon-owned and equally available through `p api`.

## Testing and version policy

Unit tests use stdlib `testing`, table-driven fixtures, fake interfaces, and
`go-cmp`. MVP integration/conformance tests exercise real pinned versions of
Git, Incus, Nix, tmux, the Codex adapter, the local client transport, and
SQLite crash recovery. Bifrost and the SSH client transport have post-MVP
gates.

Support is claimed only after the relevant validation in
[development validations](development-validations.md) passes. In particular,
the configured Incus project/storage/network combination must prove confinement,
stopped-workspace access, lifecycle operations, commit-image correctness,
systemd host readiness/exit behavior, confirmed attachment semantics, and no
host/private-network access.

## Licensing policy

P is licensed under **Apache-2.0**.
Compiled dependencies must use permissive licenses such as MIT, Apache-2.0,
BSD, or ISC. Nix package builds run pinned `go-licenses` checks over the
transitive native CLI/runtime, runtime driver and bundled WASI target graphs,
using offline vendored sources where dependencies are required. The allowed
identifiers are `Apache-2.0`, `MIT`, `BSD-2-Clause`, `BSD-3-Clause`, and
`ISC`; scanner warnings remain visible. Host CLI, runtime driver and runtime
kit use `CGO_ENABLED=0` so their ELF binaries do not introduce a dynamically
linked libc boundary. Installed native packages include dependency notices
under `share/p/licenses/modules` and Go
standard-library notices, including its vendored dependencies and patent grant,
from the pinned compiler source under `share/p/licenses/go`. The CLI also
retains the vendor tree's license, notice, author and patent files, with original
relative paths, under `share/p/licenses/vendor`; this includes supplementary
and nested attributions not automatically saved by the scanner. Bundled WASI
packages have corresponding notices under `share/p/licenses/plugins`; their
flat activation package roots stay unchanged. All bundled packages retain P's
license in that separate notice tree. GPL/LGPL tools
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
- Incus is the only MVP runtime implementation, despite the runtime driver
  interface.
- Nix devShell-to-Incus-image is the only MVP image build, despite the
  runtime driver interface.
- P instances coordinate across machines only through a shared Git origin;
  daemon federation and runtime migration are out of scope.
