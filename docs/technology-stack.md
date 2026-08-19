# P — technology stack

The implementation-facing companion to [README.md](../README.md). This
document is authoritative for implementation choices, extension seams, and
dependency policy. [communication-boundaries.md](communication-boundaries.md)
owns the channel division; [environment-building.md](environment-building.md)
owns environment providers, build isolation, artifacts, and caching;
[runtime-isolation.md](runtime-isolation.md) owns runtime assembly, grants,
storage, networking, labels, and engine selection;
[session-lifecycle.md](session-lifecycle.md) owns runtime-operation semantics;
and
[session-observability.md](session-observability.md) owns runtime condition,
attachment presence, and reduction of unattended agent events.

**Convention:** **Decided** — settled. **Direction** — proposed, likely, changeable. **Open** — undecided.

---

## Contents

- [Language: Go](#language-go)
- [The governing principle: orchestrate, don't reimplement](#the-governing-principle-orchestrate-dont-reimplement)
- [The stack at a glance](#the-stack-at-a-glance)
- [Licensing policy](#licensing-policy)
- [Extension seams](#extension-seams)
- [Deliberate rigidities](#deliberate-rigidities)
- [Appendix A — build vs. buy, per component](#appendix-a--build-vs-buy-per-component)
- [Appendix B — dependency ledger](#appendix-b--dependency-ledger)

---

## Language: Go

**Decided.** Every piece P manages sits in Go's home territory, and the supporting evidence is that nearly every neighboring tool made the same choice for the same reasons:

| P needs | The Go story |
|---|---|
| A Git server over SSH with per-connection policy | The proven pattern: Gitea, Gitaly, gitolite's architecture, and Charm's Soft Serve — an SSH Git server in Go close to P's required shape |
| An fzf-shaped TUI | fzf is Go; bubbletea is close to an ecosystem template for list-with-preview; claude-squad ships exactly this |
| Container management | podman and docker *are* Go programs; their stable surfaces are native here |
| Model access without upstream secrets in sessions | Bifrost supplies the gateway, virtual keys, routing, and protocol handling; P supplies runtime policy and lifecycle |
| Static binaries for two architectures | `CGO_ENABLED=0` cross-compiles trivially; `buildGoModule` is one of nixpkgs' simplest builders |
| A solo developer who must actually ship | Compile speed is a v1 feature; P's failure mode is never shipping, not being slow |

Nothing in P is performance-critical in a way that distinguishes Go from Rust — the hot paths (git object transfer, container runtime, the agent itself) are all external processes.

**Toolchain:** latest stable Go (1.26 at time of writing), standard library preferred throughout, `log/slog` for logging, modules pinned in `go.mod`, vendored through the Nix build.

**P's own license — Direction:** Apache-2.0, for the patent grant and ecosystem match (most of our dependencies and all of the container ecosystem use it). MIT would also be fine. Decide before the repo goes public; nothing else here depends on the choice.

---

## The governing principle: orchestrate, don't reimplement

**Decided.** P is a control plane over battle-tested binaries — `git`, an
interactive host such as `tmux`, `podman`/`docker`, and `nix` — and it treats
their CLIs as its contract with them. P never reimplements what they do:

- **Git behavior is `git`'s.** P's server spawns `git-receive-pack`/`git-upload-pack` on bare repositories and enforces policy at the boundary (authentication middleware and ref rules), never by manipulating objects itself. This is the gitolite/Gitaly architecture, decades-proven, and it's what keeps the [hub-and-spoke story](../README.md#git-and-rpc) honest: every spoke talks to completely standard git.
- **Interactive hosting is replaceable.** The session command may be a shell,
  Claude Code, Codex, or custom argv. `tmux` is the default persistent host;
  `direct` is the minimal non-persistent implementation. P is not a terminal
  emulator and does not model panes.
- **Containers are the engine's.** Create/exec/stop/remove/events go through the engine's CLI with `--format json`.
- **Environment semantics belong to the selected provider.** Nix is first (`nix eval`, realization, and `print-dev-env`); Dockerfile is the first planned alternative. Both run through restricted provider-specific workers because their inputs come from the repository. P normalizes artifacts and never reimplements Nix or Dockerfile semantics.
- **Model APIs and configuration are Bifrost's.** P manages the persisted
  per-session virtual-key lifecycle and restricted route, but does not proxy,
  translate, configure, or version model APIs, providers, aliases, or routing.

Three payoffs. The dependency tree stays small enough to audit by hand. The behavior users observe is the behavior they already know from the underlying tools. And the language choice stays low-stakes — P is glue and policy, and glue rewrites cheaply if it ever must.

The cost, stated honestly: shelling out means parsing CLI output contracts, and those can move. Mitigation: prefer the subcommands with stable JSON output, and pin minimum versions of external binaries in the Nix devShell — P's own environment is declared the same way its sessions' are.

---

## The stack at a glance

| Component | Approach | Key dependencies |
|---|---|---|
| Daemon | Single Go binary, goroutine per backend/watcher | stdlib |
| Runtime registry | Per-instance SQLite; daemon is the only writer; migrations embedded in the binary | `database/sql`, `modernc.org/sqlite` (BSD-3, CGo-free) |
| API transport | NDJSON-RPC 2.0 on host and per-session Unix sockets; observability uses RPC notifications; client transport supports local Unix and client-initiated SSH-to-Unix | stdlib (`net`, `encoding/json`, `os/exec`) |
| TUI | bubbletea + bubbles + lipgloss; a pure client of the API | charmbracelet (MIT) |
| Fuzzy filter | sahilm/fuzzy | MIT |
| Git server | wish SSH server + middleware auth, spawning `git-receive-pack`/`upload-pack` on bare repos; ref policy in middleware + hooks | charmbracelet/wish (MIT), gliderlabs/ssh (BSD-3) |
| Git plumbing (read-only queries) | Shell out to `git` (`for-each-ref`, `rev-parse`); go-git only if in-process reads earn their keep | git binary; go-git (Apache-2.0) if adopted |
| Runtime backends | `Backend` Go interface; `local-container` shells out to the engine CLI on the daemon host | stdlib `os/exec` |
| Container engine | Podman preferred; rootless Docker after validation; common lifecycle/inspect/pause CLI subset with JSON output | none (CLI contract) |
| Runtime isolation | Normalized `SessionSpec`/`RuntimeManifest`, rootless engine policy, typed grants, stable labels, isolated workspace helper | backend + engine + grant-provider interfaces |
| Integration isolation | Project-controlled environment builds execute through `IsolationProvider`; fresh local worker container in v1 | backend + engine interfaces |
| Environment build | Provider-specific `EnvironmentBuilder` under `IsolationProvider`; Nix devShell first, Dockerfile next; normalized closure/OCI artifacts | nix binary; rootless BuildKit/Buildah-equivalent later |
| Interactive host | `InteractiveHost` seam around command preparation and attach argv; `tmux` default, `direct` minimal alternative | none (CLI contract) |
| Config | Trusted host configuration keyed by complete project path; no repository-controlled P schema in v1 | stdlib |
| Session observability | Runtime condition + live attachment count + one latest unattended agent condition | stdlib + backend interface |
| Model gateway | Bifrost external service owns configuration/inference; P persists one scoped virtual key per model-enabled session | Bifrost (Apache-2.0) |
| Notifications | Sink interface: desktop (`notify-send`), ntfy (HTTP POST), webhook, arbitrary command | stdlib |
| CLI parsing | stdlib `flag` + a small dispatcher — deliberately small surface | stdlib |
| Logging | `log/slog` | stdlib |
| Testing | stdlib `testing` + go-cmp for diffs; integration tests drive real git/tmux/podman in CI | go-cmp (BSD-3) |

The pattern to notice: **stdlib wherever it holds**, Charm's MIT ecosystem for the two genuinely hard UI problems (TUI, SSH server), one embedded state store, and everything else delegated to binaries. The direct dependency count for v1 should stay in the low teens.

---

## Licensing policy

**Decided.** P will be open source, so its dependency tree is part of its contract:

- **Allowed:** MIT, Apache-2.0, BSD-2/BSD-3, ISC, and equivalents. Everything in [Appendix B](#appendix-b--dependency-ledger) qualifies today.
- **Not allowed in the compiled binary:** GPL, LGPL, AGPL, SSPL, BUSL, or any copyleft/source-available license. This is not a judgment on those licenses — it keeps P's own licensing story one sentence long for every future user and contributor.
- **External binaries are exempt by nature.** P executes `git` (GPLv2) and `tmux` (ISC) as separate processes; process invocation is not linking, and every user already has them. The policy governs what's compiled *into* P.
- **Enforced, not promised:** `go-licenses` (Apache-2.0) runs in CI against the full transitive tree and fails the build on a disallowed license. Day one, not later.

---

## Extension seams

**Decided as an approach.** P is a personal tool first, but every place where "what the author uses" was chosen over "what everyone uses" gets a named interface, so the second implementation is an addition, not a rewrite. The test for each seam is the same one the FAQ applies to config: **nothing downstream of the interface may know which implementation it's talking to.** If backend code ever calls `nix eval`, or the TUI ever branches on an agent name, the seam has failed.

The seams, and what plugs into each:

### 1. Project configuration — "keep P policy outside the repository"

V1 has no repository-controlled P schema, custom flake output, or config-provider
interface. Trusted host configuration is keyed by the complete project path and
applies at project scope. It owns session defaults and external grants; the
branch-scoped namespace is reserved for later.

The repository contributes only its ordinary default Nix devShell. Environment
selection is therefore default devShell when present, then P's immutable
minimal substrate. A later Dockerfile provider is selected by trusted project
configuration, not by adding a P policy file to the repository.

### 2. Environment builders — "I already have a Dockerfile"

```go
type EnvironmentBuilder interface {
    Build(ctx context.Context, request EnvironmentRequest) (EnvironmentArtifact, error)
}
```

Nix devShell is the v1 implementation and emits a closure artifact for
`local-container`. Dockerfile is the first additional implementation and emits
a normalized OCI artifact. Builders consume immutable source through
`IsolationProvider`; the normalized manifest is the only provider-specific
output the backend sees. Provider and backend negotiate artifact kinds through
`EnvironmentPlan`/`EnvironmentTarget`, so adding Dockerfile changes neither the
TUI nor session lifecycle. The detailed contract is
[environment-building.md](environment-building.md#provider-model).

### 3. Runtime backends — "I want stronger local isolation"

```go
type Backend interface {
    EnvironmentTarget() EnvironmentTarget
    Create(ctx context.Context, spec SessionSpec, env EnvironmentArtifact) (RuntimeLocator, error)
    List(ctx context.Context) ([]SessionInfo, error)
    Inspect(ctx context.Context, id RuntimeLocator) (RuntimeInfo, error)
    Start(ctx context.Context, id RuntimeLocator) error
    Pause(ctx context.Context, id RuntimeLocator) error
    Resume(ctx context.Context, id RuntimeLocator) error
    WorkspaceStatus(ctx context.Context, id RuntimeLocator) (WorkspaceStatus, error)
    WorkspaceOperation(ctx context.Context, id RuntimeLocator, op WorkspaceOperation) error
    Attach(ctx context.Context, id RuntimeLocator) (AttachSpec, error) // returns how, never holds the TTY
    Stop(ctx context.Context, id RuntimeLocator) error
    Remove(ctx context.Context, id RuntimeLocator) error
    Events(ctx context.Context) (<-chan Event, error)
}
```

`RuntimeLocator` is an opaque backend handle, not P identity. The canonical
session identity in `SessionSpec`/`SessionInfo` is an immutable UUID with a
one-to-one `(project path, branch)` association. The mutable session name is the
branch ref name and is unique only inside that project repository. Creation
reserves that mapping in SQLite before backend assembly, and stable backend
labels carry the UUID. Placement is immutable in V1. A later backend change
requires a lifecycle recreation design and reconstructs only from committed
branch state. Backends declare and consume artifact capabilities; they do not
interpret project environment definitions or call Nix/Dockerfile builders. The
complete spec, manifest, storage, grant, label, and cleanup contract is
[runtime and isolation](runtime-isolation.md).

`local-container` is the only v1 implementation. `local-vm` can follow on an
ordinary host; `k8s` is implemented by a P instance deployed in the cluster and
managing pods there. There is deliberately no `ssh-container`: a daemon never
connects to another host to manage its runtimes. `Attach` returns an opaque
attach specification for the client transport to execute. The v1 backend must
support safe pause/resume because lifecycle rename and destructive preflight
quiesce a running workspace; a later backend without that capability permits
those workspace mutations only while stopped. `WorkspaceOperation` is the
closed P-owned interface defined in
[runtime and isolation](runtime-isolation.md#non-activating-workspace-access),
not a caller-supplied argv.

### 4. Container engines — "I use docker, not podman"

Inside the container backends, one small interface over the CLI subset P needs
(create/start/exec/pause/unpause/stop/rm/inspect/events, using JSON output where
available). Podman is first. Rootless Docker uses the same interface only after
its isolation conformance validation passes; a future engine is a table entry.

### 5. Agent status — "I use an agent you've never heard of"

The seam here is deliberately *not* a Go interface—it's the source-aware
[status protocol](session-observability.md#status-protocol): JSON-RPC
notifications on the per-session Unix socket, implementable by a helper or one
`nc`/`socat` line. While unattended, the latest valid mapped event replaces one
nullable condition; entering clears it, and events while attached are not
retained as status. Agent integrations (Claude Code hooks, Codex lifecycle
hooks, a pi extension) live in a `cookbook/` directory of opt-in configuration,
never in P core.

### 6. Notification sinks — "tell me some other way"

```go
type NotifySink interface {
    Notify(ctx context.Context, ev UnattendedCondition, redacted bool) error
}
```

Desktop, ntfy, and webhook ship in v1 — but the `command` sink (exec an arbitrary program with the event as JSON on stdin) is the universal escape hatch: any notification system anyone ever wants is a script away, with no P release required.

### 7. Model gateways — "I deploy model access differently"

Bifrost is an independently configured service for local and Kubernetes-hosted
P instances. P's seam obtains, persists, verifies, and revokes a virtual key
for each model-enabled session UUID using a Bifrost-owned project policy.
Inference does not traverse P: the client speaks the gateway's native interface. Phase one
exposes Bifrost's OpenAI-compatible surface; phase two adds its
Anthropic-compatible surface. Bifrost's Skills Repository and MCP Gateway are
valuable adjacent surfaces, but use separate route and grant boundaries from
inference. Envoy AI Gateway is an optional later Kubernetes data plane when P
needs Envoy-native ingress/security, distributed traffic policy, or
InferencePool routing across self-hosted model servers; Kubernetes alone does
not require replacing Bifrost. Portkey remains a possible managed/hybrid
implementation. See [model-gateway.md](model-gateway.md).

### 8. Interactive hosts — "I don't want tmux"

```go
type InteractiveHost interface {
    Prepare(ctx context.Context, exec RuntimeExecutor, id RuntimeLocator, command []string) error
    AttachArgv(id RuntimeLocator, command []string) []string
    Capabilities() InteractiveCapabilities
}
```

`tmux` is the default persistent implementation and `direct` proves the seam by
running the selected command for one attachment. The command itself is session
configuration: shell, agent TUI, or custom fixed argv. Persistence across
detach is a capability, not a universal P guarantee. Layouts, pane semantics,
and tool-specific bindings remain implementation details; P readiness and
status do not depend on inspecting them.

### 9. API clients — "I want to script it / build another frontend"

The TUI is a client of `p api`, which is a client of the socket ([Decided](FAQ.md#why-is-there-no-real-cli)). The JSON-RPC method set is the stable surface; it is versioned and documented. The daemon exposes only Unix sockets. A client transport either connects directly or initiates SSH to the daemon host and bridges its Unix socket; remote attachment uses a second client-initiated SSH channel.

```go
type ClientTransport interface {
    DialRPC(ctx context.Context) (io.ReadWriteCloser, error)
    Attach(ctx context.Context, spec AttachSpec) error
}
```

`UnixTransport` and `SSHUnixTransport` both ship for Linux clients and run the same protocol integration suite from day one. Later native macOS/Windows binaries reuse `SSHUnixTransport`. The daemon never initiates SSH and does not know which client transport was used.

### 10. Integrations and isolation providers — "evaluate this project safely"

Any component that evaluates or executes project-controlled material—including Nix realization and Dockerfile construction—is split into what should run and where it may run:

```go
type Integration interface {
    Plan(ctx context.Context, trigger Trigger) (RunSpec, error)
    Record(ctx context.Context, result RunResult) error
}

type IsolationProvider interface {
    Run(ctx context.Context, spec IsolationSpec) (RunResult, error)
}
```

`RunSpec` identifies an immutable Git commit and a P-owned provider operation.
Trusted host configuration resolves it to an `IsolationSpec` containing an
approved provider, resource limits, and named capabilities. V1 binds these
external grants only at project scope; the branch-scoped namespace is reserved
for later. Repository contents cannot select or widen that provider/profile
binding. No ambient daemon environment, host paths, engine socket, SSH agent,
credentials, or local-network access crosses the interface. Profiles that
inject secrets must pair them with endpoint-scoped egress; secret plus
unrestricted internet is rejected at configuration load.

The v1 `container` isolation provider creates an ephemeral local container distinct from the session. Future providers may use a local VM or a P deployment's cluster-native job mechanism. The daemon never directly executes the command. Built-in HTTP notification sinks do not execute project code; the optional `command` sink uses the same isolation boundary.

---

## Deliberate rigidities

**Decided.** Just as important as the seams are the places P refuses to abstract, because an abstraction there would cost coherence and buy nothing:

- **The git interchange is not a "VCS provider" — but note precisely what's rigid.** P depends on git's *object model and wire protocol*: the local server sessions push through and, when configured, the shared origin independent instances use to converge. It does **not** depend on the porcelain inside a session — and because nearly every serious VCS alternative keeps git interop for ecosystem reasons (jujutsu backs onto git storage and pushes to git remotes; Sapling clones git repos; GitButler sits on git), an agent driving jj inside a session works today with zero P changes. What a `VCSProvider` abstraction would have to cover — UUID-to-branch ownership, per-ref server-side authorization, transactional ref rename, reserved private namespaces, and optional origin publication — *is the design*, has one real implementation in the world, and would forbid P from using the git specifics its best ideas are built from. If a post-git interchange ever wins, porting P is a redesign, and a speculatively drawn abstraction wouldn't have saved it. The honest hedge is containment, not abstraction: everything git-server-shaped lives in one component.
- **Linux is not an "OS provider."** The execution instance is Linux. Other platforms may later run a thin client that connects to a Linux instance over SSH ([Direction](../README.md#macos-and-windows)).

The interactive host remains a small replaceable seam because persistence and
attach argv can vary without changing session or Git identity. Git is
different: replacing its object and ref model would redesign P.

Rigidity is a feature here: every seam in the previous section is credible precisely because P doesn't pretend everything is a seam.

---

## Appendix A — build vs. buy, per component

The decision record for every dependency-shaped choice. Format: options considered → decision → why. Licenses in parentheses.

### A1. TUI framework

- **Options:** bubbletea (MIT) · tview (MIT) · gocui (BSD-3) · raw termbox/tcell (Apache-2.0) · build our own event loop.
- **Decision: bubbletea**, with bubbles for the list/viewport components and lipgloss for styling.
- **Why:** the fzf-shaped overview (flat filtered list + preview + chorded actions) is bubbletea's most-trodden path — claude-squad is proof the exact shape ships with it. The Elm-style model/update/view architecture also keeps the TUI honestly *thin*: the model is API state, messages are API calls, which is exactly the [TUI-as-client constraint](FAQ.md#why-is-there-no-real-cli) we need to hold in review. tview is stronger for form-heavy apps, which P isn't. Building our own buys nothing but bugs — terminal handling is a solved problem with sharp edges.

### A2. Git server

- **Options:** embed Soft Serve (MIT) · go-git server-side (Apache-2.0) · a pure hooks-based setup on bare repos with sshd · **wish (MIT) + spawn `git-receive-pack`/`git-upload-pack`** · implement the pack protocol ourselves.
- **Decision: wish middleware around the real git binary**, with three initial enforcement points: middleware selects the registered project and host-or-session principal; upload-pack exposes user-visible branches while hiding protected namespaces and rejecting arbitrary object-ID wants; `update` hooks make the host principal read-only and restrict each session principal to its UUID-assigned current branch, fast-forward-only unless trusted host policy grants that branch a narrow rewrite exception. `refs/attempts/*` is reserved for future attempt refs; the separate P-owned `refs/p/*` namespace is reserved for a future checks protocol. Both are denied in v1.
- **Why:** Soft Serve is an application, not a library — embedding it means adopting its database and UI for a tenth of its features. go-git's server side is the least-exercised part of an otherwise good library, and P's per-principal ref and rewrite policy needs the `update` hook's exact semantics anyway, which the real binary gives us for free. System sshd + hooks (the pure gitolite pattern) works but splits P into "the daemon" plus "sshd config P must manage" — wish keeps the SSH server in-process, so per-session credentials are Go middleware, not authorized_keys surgery. Implementing pack protocol ourselves violates the governing principle for zero gain.
- **Credential carrier:** both caller classes use SSH. A per-session SSH key maps server-side to `(project, session UUID, current ref)` and its restricted write policy; a separate per-instance host SSH key maps to the single user's read-only principal for registered projects. P creates the host key at instance initialization, keeps it mode `0600` in host-side state, and configures the checkout-facing SSH alias plus `url.*.insteadOf` rewrite for `p://` display URLs; it is never mounted into sessions. HTTP smart protocol (`git http-backend` behind the daemon) remains the fallback carrier if SSH proves awkward.

### A3. Container engine access

- **Options:** podman Go bindings (Apache-2.0, very large dependency tree) · docker/moby client (Apache-2.0) · REST over the engine socket with stdlib · **shell out to the engine CLI with `--format json`**.
- **Decision: CLI shell-out**, behind the small engine interface (seam 4).
- **Why:** the podman bindings import a substantial slice of podman itself — hundreds of transitive dependencies to audit for a tool whose entire engine usage is create/start/exec/pause/unpause/stop/rm/inspect/events. The CLI with JSON output is the engine's stability contract with humans and is similar enough across podman/docker for P's subset. It is invoked only on the daemon host; remote execution over SSH is explicitly outside this interface. REST-over-socket is the fallback if CLI output contracts prove flaky in practice.
  Selection and no-fallback behavior are defined in
  [runtime and isolation](runtime-isolation.md#backend-and-engine-selection).

### A4. API transport / RPC

- **Options:** gRPC (Apache-2.0, brings protobuf toolchain) · sourcegraph/jsonrpc2 (MIT) · REST over local HTTP · **hand-rolled newline-delimited JSON-RPC 2.0 on stdlib**.
- **Decision: hand-rolled NDJSON-RPC.** ~200 lines: read line, dispatch on `method`, write line; notifications for status ingestion and streaming.
- **Why:** this is the rare case where "build" beats "buy"—the protocol is the spec third-party clients need anyway (seam 8), so owning the ~200 lines costs less than owning a dependency's abstractions. The daemon side is always a Unix socket; local, per-session, and SSH-bridged client paths carry the same protocol. Observability is a JSON-RPC notification rather than a second event wire format. gRPC's HTTP/2 machinery and codegen buy streaming expressible with one JSON line per event. Debuggability with `socat` is valuable during early implementation. jsonrpc2 remains the fallback if the hand-rolled version grows warts.

### A5. CLI argument parsing

- **Options:** cobra (Apache-2.0) · urfave/cli (MIT) · **stdlib `flag` + a 50-line dispatcher**.
- **Decision: stdlib.**
- **Why:** the surface is small (`p`, its project-scoped forms, `p attach`, `p api`, and `p daemon`) and [deliberately won't grow a flag-mirror of the TUI](FAQ.md#why-is-there-no-real-cli). cobra is built for hundred-command trees; adopting it invites the CLI sprawl the design explicitly rejects. The dependency's absence is load-bearing.

### A6. Fuzzy matching

- **Options:** port fzf's algorithm (fzf itself is MIT) · sahilm/fuzzy (MIT) · substring matching only.
- **Decision: sahilm/fuzzy**, the de-facto pairing with bubbletea.
- **Why:** small, no transitive deps, good-enough ranking for lists of dozens-to-hundreds of sessions. fzf's algorithm is better at scale P will never see. Plain substring would be acceptable v0 behavior, and the seam is one function — if ranking feels wrong in use, swapping is an afternoon.

### A7. Git read-side plumbing

- **Options:** go-git (Apache-2.0) for in-process ref/log reads · **shell out to `git for-each-ref` / `rev-parse` / `log --format`**.
- **Decision: shell out**, revisit only with profiling in hand.
- **Why:** consistency with the governing principle, and the read patterns (list refs for the overview, resolve a creation source) are single-digit-milliseconds through the binary. go-git is the pre-approved fallback if per-refresh process spawns ever show up in the overview's latency budget — it's a healthy, permissively-licensed library; we just don't need it yet.

### A8. Notifications

- **Options:** libnotify bindings (cgo) · beeep (BSD-2) · **shell out / plain HTTP per sink**.
- **Decision: no library.** Desktop = `notify-send` (Linux is the only v1 execution target); ntfy and webhook use `net/http`; the command sink runs through `IsolationProvider` rather than direct host `exec`.
- **Why:** each built-in sink is small against tools the platform already has, and the isolation seam prevents an arbitrary configured command from inheriting daemon authority. beeep is the fallback if a later native client owns cross-platform desktop delivery.

### A9. Project configuration

- **Options:** repository-controlled P schema · multiple config-provider
  languages · **trusted host configuration only**.
- **Decision:** V1 loads one validated trusted configuration keyed by project
  path. Nix is not P's configuration language; the environment builder invokes
  it only for the repository's ordinary devShell.
- **Why:** this removes a public schema and evaluator from the repository trust
  boundary. P-specific session behavior and grants remain under the machine
  owner's control.

### A10. Testing

- **Options:** testify (MIT) · ginkgo/gomega (MIT) · **stdlib `testing` + go-cmp (BSD-3)**.
- **Decision: stdlib + go-cmp.** Integration tests run against real `git`, the
  selected interactive hosts, `nix`, and `podman` in a Nix-provisioned CI
  environment. They exercise local and SSH-to-Unix client transports, every
  lifecycle crash/commit point, workspace quiescence, destructive confirmation,
  repair/orphan handling, read-only host Git access, reserved-namespace denial,
  origin refresh/publication races, integration/build capability denial,
  environment cache identity and activation, runtime mount/storage/label
  conformance, public-egress/local-network blocking, attachment presence, and
  latest-unattended-condition reduction. The provider conformance suite gains
  a rootless Dockerfile fixture when that provider is added.
- **Why:** table-driven stdlib tests age best in open-source Go projects and ask nothing of contributors. BDD frameworks add a dialect for no coverage gain at this scale.

### A11. Runtime registry

- **Options:** reconstruct every start from backend labels and workspace manifests · BoltDB (MIT) · SQLite through a CGo driver · **SQLite through `database/sql` and `modernc.org/sqlite` (BSD-3)**.
- **Decision: SQLite with embedded migrations, in WAL mode, with the daemon as the only writer.** The schema stores immutable session UUIDs, project/branch assignments, UUID-to-runtime relationships, normalized runtime manifests and grant snapshots, pending lifecycle operations, runtime condition, one nullable latest unattended condition, persisted session Bifrost keys, origin refresh generations, and other mutable bookkeeping. It never stores Git objects or substitutes for querying Git and the runtime backend.
- **Why:** uniqueness constraints and transactions express the session-UUID-to-logical-branch invariant and make interrupted creation, branch rename, and destructive operations recoverable. SQLite keeps the deployment to one local file and no server. `modernc.org/sqlite` is a [`database/sql` driver implemented without CGo](https://pkg.go.dev/modernc.org/sqlite), preserving the static-binary and cross-compilation plan. The tradeoff is a larger transitive dependency tree than the rest of P; pinning and the existing license gate apply to the complete tree.

### A12. Environment builders

- **Options:** make each backend interpret environment definitions · hard-code Nix into runtime creation · **provider-specific isolated builders that emit normalized artifacts consumed by backend capability**.
- **Decision:** Nix devShell first, producing closure artifacts for `local-container`; explicit Dockerfile next, producing normalized OCI. The backend advertises accepted artifact kinds and never evaluates either language.
- **Why:** Nix closure mounting gives the first implementation its fast cache-hit path without making Nix the architecture. Dockerfile has inherently different context and cache semantics, but both can converge on immutable manifests plus P's fixed runtime contract. Keeping build interpretation out of backends lets future OCI and VM providers compose without leaking into session identity, TUI, Git, or observability. See [environment-building.md](environment-building.md).

### A13. Model gateway

- **Options:** custom Go reverse proxy · LiteLLM · **Bifrost** · Envoy AI Gateway · Portkey · direct provider credentials inside sessions.
- **Decision:** Bifrost is an independently configured local and Kubernetes-capable gateway, behind P's small session-key lifecycle seam. Phase one exposes Bifrost's OpenAI-compatible interface; phase two adds its Anthropic-compatible interface. P implements neither protocol and does not duplicate Bifrost provider/model/routing configuration. Skills and MCP may reuse Bifrost through separately filtered surfaces.
- **Why:** Bifrost already provides virtual keys, aliases, provider credentials, routing, streaming, limits, usage accounting, a Skills Repository, and an MCP gateway in Go. Rebuilding those features would create security-sensitive compatibility and distribution projects unrelated to P's project/branch/session model. Its standalone and Helm deployment paths cover the expected progression from laptop to an ordinary Kubernetes-hosted P instance. LiteLLM is the fallback if compatibility evidence requires it. Envoy AI Gateway is reserved for a later need for Kubernetes-native shared ingress, distributed policy, or GPU-aware InferencePool routing—not as a prerequisite for Kubernetes. Portkey is a possible managed/hybrid choice. See [model-gateway.md](model-gateway.md).

---

## Appendix B — dependency ledger

Every direct dependency planned for v1, with license and role. CI enforces this table's spirit via `go-licenses`; this table is for humans.

| Dependency | License | Role | Blast radius if it vanished |
|---|---|---|---|
| `charmbracelet/bubbletea` | MIT | TUI runtime | Rewrite TUI layer on tcell; API layer untouched |
| `charmbracelet/bubbles` | MIT | List/viewport components | Reimplement two components |
| `charmbracelet/lipgloss` | MIT | TUI styling | Cosmetic |
| `charmbracelet/wish` | MIT | SSH server middleware (git server) | Fall back to gliderlabs/ssh directly |
| `gliderlabs/ssh` | BSD-3 | SSH server core (via wish) | golang.org/x/crypto/ssh directly |
| `golang.org/x/crypto` | BSD-3 | SSH primitives | None realistic (Go project) |
| `golang.org/x/term`, `x/sync` | BSD-3 | Terminal size, errgroup | Trivial to inline |
| `sahilm/fuzzy` | MIT | Filter ranking | One-function swap; substring fallback |
| `google/go-cmp` | BSD-3 | Test diffs | Tests only, never shipped |
| `google/go-licenses` | Apache-2.0 | CI license gate | CI only, never shipped |
| `modernc.org/sqlite` | BSD-3 | CGo-free SQLite driver for the per-instance runtime registry | Replace the driver or move registry access behind a process boundary; schema and repository layer remain |

**External services and binaries** (invoked or contacted, never linked — see [Licensing policy](#licensing-policy)): `git` (GPLv2), `tmux` (ISC), `podman` (Apache-2.0) or `docker` (Apache-2.0), `nix` (LGPL-2.1), Bifrost (Apache-2.0), `notify-send` (GPL, optional), `ssh` (BSD). All are process boundaries.

**Pre-approved fallbacks** (not dependencies today, vetted for license if needed): `go-git` (Apache-2.0), `sourcegraph/jsonrpc2` (MIT), `beeep` (BSD-2), `fsnotify` (BSD-3).

Rule of thumb going forward: a new direct dependency needs an Appendix A entry — what was considered, why the library beats the ~lines it replaces, and its license. The low teens is the soft ceiling; pressure against it is a design smell worth a second look.
