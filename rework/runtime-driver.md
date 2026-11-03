# One runtime driver for every runtime effect

Status: proposed, 2026-11-02. Part of the [interface rework plan](REWORK-PLAN.md).

## Summary

**Today** P reaches Incus along three routes. A WASI runtime plugin
(`plugins/bundled/runtime-incus/main.go`) receives one of nine command kinds
and asks a native broker for the matching effect; core re-reads Incus
afterwards, so the module decides nothing. Core uses it from 16 places and
never for `runtime.delete`. Everything else, deletion included, is 195 direct
daemon calls to 43 methods of `*runtimeincus.Backend`. The attachment helper
opens its own Incus exec channel. All three speak Incus, and P logic that has
nothing to do with Incus lives in the Incus adapter too.

**Decided by the maintainer on 2026-11-02:**

- One owner for runtime effects: a native runtime driver that every runtime
  call goes through, replaceable like a plugin. The WASI runtime plugin goes.
  The test: "I could for example replace an Incus backend with a k8s backend."
- A driver is a separate executable, pinned by path and SHA-256 in trusted
  host configuration, speaking a versioned JSON-RPC protocol over a Unix
  socket pair. The Go interface comes first.
- Images are runtime-specific: an image only has to work with the runtime
  that built it. How a runtime runs the session host (tmux) and project
  services is its own business; the Incus driver keeps systemd for both.
- P reads or changes a session's workspace only while the session is
  stopped, and never freezes it. Deletions state loss by category without
  looking inside the container ([plan-shape.md](plan-shape.md#by-category)).
  `workspace.inspect` is removed.
- The whole private root of a session survives stop and start.
- Builders get public internet access only. No package cache for now.
- P targets fresh installs: no upgrade path.

**Proposed:** a `Driver` of 20 operations in P's terms, in six groups. The
driver owns how sessions and images work: the image and its build, the guest,
the session host, project services, terminals, devices, network, and the
proofs of its own boundary. Core keeps lifecycle state, the journal, policy,
Git authority and the proofs of absence. Git effects go through the
[source driver](source-driver.md), which also computes the Git edits that
rename makes in a stopped workspace; core reads and writes those files through
the workspace operations. The WASI environment plugin folds into the driver's
image build.

**What users notice:** no workflow changes from the driver. Host
configuration has a `runtime.driver` block instead of `runtime_plugin_id` and the
Incus settings, and `p plugins defaults` lists four packages instead of six.
With no helper slot kept free (`checkSessionCapacity`), one more session fits
under the Incus project's container limit.

```text
Before
internal/daemon ─┬─ plugin.RunRuntime ─► runtime.wasm ─► broker ─┐
                 ├─ 195 direct calls ─────────────────────────────┼─► runtimeincus.Backend ─► Incus
                 └─ environmentnix ─► environment.wasm ─► broker ─┘
internal/attachment (helper) ─► Incus exec websocket

After
internal/daemon ─► runtimedriver.Driver ─► p-runtime-incus ─► Incus
internal/attachment (helper) ─► daemon ─► Driver.OpenTerminal
```

## Who does what

| Area | Today | Core after | Driver after, from today's code |
|---|---|---|---|
| Boundary | `CheckConfinement` and the egress proofs, before every mutation | — | `Check`, `Describe` (backend.go, egress.go, egress_firewall.go) |
| Capacity and absence | Seven `incus list` scans; one slot kept for a helper | Admission and the proofs, from `Inspect` and `Inventory`; no helper slot | `Inventory`, one reader |
| Session runtimes | `incus init`, devices, start, stop | Lifecycle, gates, locks | `Inspect`, `Create`, `Start`, `Stop`, `Delete` (backend.go, discard.go) |
| Guest and Git key | Core's guest file plan over the file API | The session spec | Inside `Create` (assembly.go) |
| Session host | Core reads `systemctl show` and the journal | — | Inside `Start` and `Inspect` (host_observe.go, the daemon's readiness wait) |
| Project services | `systemctl --user` through the WASI plugin | The API methods, the lock, the ready check | `Services`, `ServiceAction`, `ServiceLogs` (services.go) |
| Terminals | The helper's own Incus exec channel | Token, lease, relay to the helper | `OpenTerminal` (attach.go, internal/attachment/incus.go) |
| Workspace | Rename edits Git files in a stopped or frozen runtime | Reading and writing those files, stopped sessions only; the source driver computes the edits | Workspace operations and the stopped-storage check |
| Network | An egress address per session, reserved in SQLite | The reservation | Attaching the container to it, the egress proofs |
| Endpoints and grants | Sockets on the host; grants captured under trusted ceilings | Both, unchanged | Their delivery into the session |
| Images | Core and the WASI environment plugin drive builder containers; `incus publish` | The index and release rule ([commit-images.md](commit-images.md)) | `BuildImage` with public egress, the image operations (builder*.go, environmentnix, nixenv) |
| Uncertain effects | Positive outcomes only, never a reused identity | The guards | `Settle` (`workspaceResourceBusy`, `ReconcileBuilderPublication`) |
| Freezing, loss inspection, `workspace.inspect`, helper containers | Pause, export, helper, Git analysis | **Go** before the driver: `workspace.inspect` in step 0d, the rest in 0e | **Go** (workspace_loss_*.go, workspace_snapshot.go, workspace_sftp_*.go, workspace_git.go, workspace_helper.go) |

The driver is trusted code: it holds container authority. Only trusted host
configuration selects it; repositories, requests and plugins never reach it.
The Incus driver ships as the executable `p-runtime-incus`. Its Nix steps
become a Go package that another driver can reuse. Rename's Git edits
(workspace_rename.go with `safeGitConfig`) leave the Incus code: from M4 core
runs them over the workspace operations, and in step 9 the source driver
takes them ([source-driver.md](source-driver.md#renaming-a-sessions-clone)).

## The driver interface

Three ideas carry it:

- **P's concepts, not the guest's layout.** Core names a session, its
  command, branch, key, endpoints, grants and network; the driver decides
  where they go. The one guest place core knows is the workspace, the Git
  clone whose files core reads and writes for a rename while the session is
  stopped; the source driver says which files.
- **Gates.** A mutation takes a gate: a core function the driver calls once,
  after every check and right before the first effect. Core records its
  durable intent there. If the gate fails, nothing happened; if the call
  fails after it, the outcome is unknown.
- **Settling.** An unknown outcome is resolved by `Settle` and by operations
  that are idempotent by reference, never by sending a non-idempotent effect
  again.

The sketch is illustrative. Step 1 landed its image part as
`runtimedriver.Images` (internal/runtimedriver/driver.go): `Describe`, the
four image operations, `Settle` for a build or an image delete, and `Delete`
for a build's leftovers. A build has no incarnation, so step 1's `Delete`
takes none: `Delete(ctx, r Ref, g Gate)`.

```go
package runtimedriver // internal/runtimedriver

// Driver is the only code that creates, changes, reads or deletes runtime
// machinery.
type Driver interface {
	// Host
	Describe() Capabilities
	Check(ctx context.Context) error                 // prove the boundary and the egress substrate
	Inventory(ctx context.Context) (Inventory, error) // every object in scope, or an error

	// Sessions
	Inspect(ctx context.Context, s Session) (Observation, error)
	Create(ctx context.Context, s Session, at Incarnation, g Gate) (Observation, error) // at empty: initialize; set: only complete that runtime
	Start(ctx context.Context, s Session) (Observation, error)                          // returns with the host ready, or stopped with a diagnostic
	Stop(ctx context.Context, s Session) (Observation, error)
	Delete(ctx context.Context, r Ref, at Incarnation, g Gate) error // a session at its incarnation, or a build's leftovers, which have none
	OpenTerminal(ctx context.Context, t Target, size Size) (Terminal, error)

	// The workspace of a stopped session; paths relative to its root
	ReadWorkspace(ctx context.Context, t Target, path string, limit int64) (File, error)
	WriteWorkspace(ctx context.Context, t Target, path string, f File, g Gate) error
	RemoveWorkspace(ctx context.Context, t Target, path string, g Gate) error

	// Images
	BuildImage(ctx context.Context, b Build, g Gate) (Image, error)
	Images(ctx context.Context) ([]Image, error)
	InspectImage(ctx context.Context, ref ImageRef) (Image, bool, error)
	DeleteImage(ctx context.Context, img Image, g Gate) error

	// What became of a call that returned ErrUnknownOutcome
	Settle(ctx context.Context, e Effect) (Settlement, error)

	// Project services in a running session (services-plugin.md)
	Services(ctx context.Context, t Target) ([]Service, error)
	ServiceAction(ctx context.Context, t Target, name string, a Action) error // start, stop, restart
	ServiceLogs(ctx context.Context, t Target, name string) (Logs, error)
}

type Ref struct {
	Kind Kind   // session or build
	ID   string // session UUID or build request UUID
}

type Session struct {
	Ref       Ref
	Labels    Labels    // P's identity facts, stored with the runtime, returned verbatim
	Image     ImageRef  // the commit image; empty for a blank project's base image
	Command   []string  // the interactive command the session host runs
	Workspace Workspace // branch and commit to check out from P's Git endpoint
	GitKey    []byte    // the session's P Git key
	Endpoints string    // host directory holding session.sock and git.sock
	Grants    []Grant
	Network   Network // none, or public-egress with the address P reserved
	Assets    []Asset // session assets core selected, such as the agent adapter
}

type Build struct {
	Ref     Ref          // kind build, new for every attempt
	Labels  Labels       // the image's identity: project, commit, base, system, builder version, and the build's ID
	Source  SourceExport // a Git bundle of the commit, as the source driver's Export wrote it
	Network Network      // the builder's: public egress at the address P reserved, or none
}

type Image struct {
	Ref    ImageRef
	Labels Labels
	Shell  Shell // what the build found at the commit
	Size   int64 // bytes, as the backend reports them
}

type Shell struct {
	Selection      Selection // no-flake, no-default or devshell: whether the commit's default devShell is in the image
	MaterialSHA256 string    // the devShell's activation material in the image
}

type Target struct {
	Session Session
	At      Incarnation // empty means any; otherwise it must match
}

type Observation struct {
	State       State       // absent, stopped, running, other
	Incarnation Incarnation // opaque; same across start and stop; new on re-create or restore
	Complete    bool        // false only after an interrupted Create
	Host        HostState   // ready or not, with a bounded diagnostic
}

type Effect struct {
	Kind     EffectKind // create, start, stop, delete, write, remove, build, delete-image, terminal
	Target   Target
	Build    Build
	Image    Image
	Path     string
	Content  File // write: what must now be there
	Terminal string
}

type Settlement int // Applied, NotApplied, Unknown

type Terminal interface {
	io.ReadWriteCloser
	ID() string
	Resize(Size) error
	Ended() <-chan Completion // Proven is false when only the channel closed
}

type Service struct {
	Name, Description string
	State             ServiceState // stopped, starting, running, stopping, failed
	Detail            string       // the driver's words, for display only
}

type Capabilities struct {
	ImageBuilder    string   // the image builder's version; part of every image's identity
	System          string   // for example x86_64-linux
	Networks        []string // none, public-egress
	EgressAddresses int      // 4 for Incus; 0 when the backend assigns addresses
	Services        bool     // false: the services operations return ErrUnsupported
}

type Gate func() error

var (
	ErrUnknownOutcome = errors.New("the effect may have happened; settle it")
	ErrMismatch       = errors.New("an object differs from the spec; it is never adopted")
	ErrUnavailable    = errors.New("the backend cannot be reached; nothing is claimed")
	ErrUnsupported    = errors.New("the driver does not offer this operation")
	ErrNoService      = errors.New("no such project service in this session")
)
```

### What each operation guarantees

| Operation | The driver must |
|---|---|
| All | Act only in its configured scope, on objects that carry this P instance's labels; never adopt, change or delete anything else. Read native state fresh on every call. After an error, claim nothing. Check its boundary before every mutation and refuse rather than widen. |
| `Inspect` | Report `absent` only when its direct address for the reference finds nothing; report `ErrMismatch` for an object that carries the reference but differs from the spec. |
| `Create` | With no incarnation: look through the whole scope first, so a natively renamed runtime blocks creation; call the gate; initialize; then install everything the spec names and leave the runtime stopped. With an incarnation: never initialize; complete that stopped runtime, keeping identical guest files, installing missing ones and refusing different ones. |
| `Start`, `Stop` | Converge on the state and do nothing when it holds. `Start` re-checks grants, endpoints and the egress substrate and returns once the session host is ready; if the host fails, the runtime ends stopped and `Start` returns the bounded diagnostic. When the host exits, the runtime stops and is never restarted. `Stop` is graceful and bounded. |
| `Delete` | Act only on the given incarnation; stop a running runtime first. After success the reference is absent and its private storage gone. For a build, which has no incarnation: remove what it left behind, such as its builder, and never an image. |
| `Inventory` | List every object in scope, P's or not, including ones being deleted, with labels as stored, within a stated bound; an error rather than a partial list. |
| Workspace operations | Act only on a stopped session of the given incarnation. Never run code the session or its repository can change; never follow symlinks; report a path absent, or its type, owner and mode, and read and write exact owner, mode and bytes, within bounds; reach only the session's private storage. |
| `OpenTerminal` | Attach the session user to the session host with a PTY. Arm a completion witness before the attach starts, or fail; report `Proven` completion only from it. |
| `BuildImage` | Call the gate once, after every check and right before the builder is initialized; core records the build attempted there, so a failed gate leaves no builder. Build an image of the commit for this runtime, label it, and leave no builder behind on success or definite failure. The builder gets the commit's Git bundle, which it clones into `/workspace`, the build's network and nothing else. |
| Images | Store labels immutably and return them verbatim. `DeleteImage` acts on the exact reference and labels and never affects runtimes created from the image. |
| `Settle` | `Applied` only when the intended outcome is present exactly and nothing admitted on the target is still running; `NotApplied` only when the backend proves the effect can never be admitted later; otherwise `Unknown`. For `create`, the intended outcome is the initialized runtime at the reference, complete or not. Use native state only, so the answer holds after a restart. |
| Services | Act only on the target session's project services, at its incarnation, while its host is ready, with the session user's authority; never on the runtime's own units. Return a complete, bounded inventory or an error; queue actions and return once queued, with progress shown in the inventory; bound the logs. The full table is in [services-plugin.md](services-plugin.md#guarantees). |

Core proves a runtime gone only with both `Inspect` reporting absent and a
complete `Inventory` holding nothing labeled with its reference, since an
external rename can move a runtime away from its address.

### Properties every driver proves

| Property | The contract (how Incus meets it) |
|---|---|
| Isolation | Code in a runtime, root included, cannot gain host privilege, reach the driver's authority, see another runtime, or use a device P did not grant; `Check` proves it from live state. (Unprivileged container, isolated ID map, no nesting or `raw.*`, confined project.) |
| Network `none` | Nothing is reachable except the two P endpoints. (No NIC.) |
| Network `public-egress`, for sessions and builders | Outbound TCP 80 and 443 and DNS to the configured resolvers only; host, private, CGNAT, link-local, metadata, multicast, sibling runtimes and the driver's API denied over IPv4 and IPv6; proven from live state before each create and start, by a check the daemon's account cannot forge. (Managed bridge, ACL and nftables read by two fixed sudo commands.) |
| Private state | Everything the session writes is kept: the whole private root survives stop and start, belongs to this runtime only, and is gone after `Delete`. (The instance root.) |
| Stopped storage | Workspace operations reach only the session's private root: never a grant, the endpoint directory, a host path, another runtime's storage or a guest-made mount, at, below or above the path; a driver that cannot tell refuses. (A stopped instance's file API sees only its storage volume; the mountinfo parser in workspace_mounts.go stays for drivers that read through a live mount namespace.) |
| Session host and services | The runtime runs the session's command in a persistent host that terminals attach to, reports it ready or not ready with a bounded diagnostic, and runs project services its own way. (systemd, tmux from the interactive-host package, user units.) |
| Identity | Labels stored and returned exactly; an incarnation stable across start and stop, new on re-create or restore. (`user.p.*` keys, `volatile.uuid` and its generation, image properties.) |
| Endpoints and grants | The two sockets appear read-only at `/opt/p/endpoints`, and each grant at `/mnt/p/<name>` with its flags, in that session only; a driver refuses what it cannot honor exactly. (Disk devices within the project's ceilings.) |

### Settling uncertain effects

Incus can admit a request that was queued before it registered an operation,
after the client gave up, so state alone does not settle an effect. The
contract keeps today's rules:

1. **A guard until settled.** The gate's record stays until the effect
   settles: the creation's init state, the build's states and publication
   claim, rename's write marker, the delete mark.
2. **Only a positive outcome settles**: the intended object or state, seen
   exactly on two observations 250 ms apart with no Incus operation active
   on it. The Incus driver never returns `NotApplied` for a create or a build.
3. **A non-idempotent effect is never sent again.** A fresh `Create`, a
   build, a delete and a workspace write are settled, never resent; a new
   build gets a new reference, and a delayed admission on the old one shows
   up in `Inventory` or `Images`, where core accepts or deletes
   it exactly. Operations that are idempotent by reference may be called
   again: `Create` at an exact incarnation, `Start`, `Stop`.

**Recovering a partial `Create`.** `Create` initializes and then assembles.
If the driver dies in between, or between two guest writes, core settles the
`create` effect. `Unknown` keeps the creation blocked, as today. `Applied`
means the runtime exists; core reads its incarnation and calls `Create` again
at that incarnation, which completes it and can never initialize a second
one. An uncertain guest write is found identical and kept, or found
different and refused, which leaves the creation for cleanup. Whether an
interrupted Incus file write can leave a partial file was not verified.

### Terminals

Today the helper process owns the channel and its completion witness, and
keeps the daemon lease until the witness proves the guest process ended. After:

- The driver owns the native channel and the witness. The daemon owns the
  token and the lease and relays the terminal to the helper, which keeps
  only the client side and has no Incus access.
- If the driver fails while the daemon stays up, its terminals end unproven.
  The helper tears down; input and resize stop and are not replayed; the
  lease stays, so discard and delete stay refused. The lease ends when the
  restarted driver settles the terminal as ended, or when the session stops
  or is deleted.
- A daemon restart ends attachments, as today.

### Project services

The three operations, their five states and their guarantees are designed in
[services-plugin.md](services-plugin.md). How a runtime defines and runs
services is its own business; the Incus driver keeps systemd user units and
today's services.go, with a mapping to P's states. Service actions are fire
and observe, by the maintainer's decision: they return once the runtime has
queued them (`systemctl --no-block` on Incus), and the inventory shows their
progress.

### Public-egress addresses

A session with `network: public-egress` gets one of a fixed number of IP
addresses on P's egress bridge; Incus has four, the bridge's /24 with last
byte 10 to 13 (`PublicEgressGuestIPv4`). The bridge runs no DHCP. P reserves
the address in SQLite in the same transaction that reserves the session,
before the container exists (`session_public_addresses`), so two sessions
never share one, and Incus ties each container to its address
(`security.ipv4_filtering`). The driver declares how many addresses it has,
P puts the chosen one in the spec, and the driver attaches the container to
it. A backend that assigns addresses itself declares none.

## Kubernetes

The maintainer's replaceability test is a Kubernetes driver. Nothing was
prototyped; this follows Kubernetes' documented APIs. Runtime-specific images
help: a Kubernetes driver need not run systemd, and may run services as a
process manager or as containers in the session's Pod.

The contract stays open to it by allowing:

- network `none` as "nothing reachable", and addresses the backend assigns;
- endpoint and grant delivery by any means that keeps their guest paths and
  their one-session scope;
- settlement by the backend's own rule, with `Unknown` when it cannot decide;
- images that work only with their runtime, built any way;
- `absent` meaning gone, not terminating;
- file access and user switching through P-owned programs from the image.

Open feasibility limits:

- **Persistence.** The whole private root must survive, but a Pod's own
  filesystem does not survive the Pod. VM-backed Pods, a root on a volume or
  a narrower rule is revisited when a Kubernetes driver is needed.
- **Egress proofs.** NetworkPolicy enforcement depends on the CNI plugin, so
  API objects prove nothing.
- **Endpoints and grants** are host Unix sockets and host paths: `hostPath`
  works only on the daemon's node.
- **Settlement.** Whether the API server's request deadline bounds every
  delayed write is not verified.
- **Stopped workspaces** need a short-lived Pod per operation.

## How a driver is replaced

The maintainer chose a separate executable over a driver compiled into P:
replacing it needs no rebuild of P, as PROJECT.md's extension goal asks. A
driver crash turns calls in flight into unknown outcomes, which the
restarted driver settles. Core always talks to `runtimedriver.Driver`; the
protocol client is one implementation of it.

The protocol: JSON-RPC 2.0 over a Unix socket pair with P's strict JSON
framing, both directions so the driver can call a gate; one long-lived
driver process started by the daemon; a hello that agrees on the version,
refusing startup on a mismatch; each terminal on its own connection; host
configuration `runtime.driver: {path, sha256, config}`, beside the source
driver's `git.driver`.

**Launch checks.** The path and digest alone do not prove that only root or
P's account can change the file between the check and the start:
`CheckTrustedAncestors` (internal/control/config.go) starts at the file's
parent, and nothing checks the file's owner, mode or link count. Before every
start, and before hashing, the daemon applies the launch checks of
[plugins.md](plugins.md#trust): the executable's directory and its ancestors
owned by root or P's account and not group- or world-writable, with today's
exception for a root-owned sticky ancestor; the executable a regular file
owned by root or P's account, not group- or world-writable, with one link
unless root owns it and no one may write it; and its owner's execute bit
set. It then hashes the file and starts the file it checked.

## Images and step 1

The driver owns the image: [commit-images.md](commit-images.md)'s build,
storage, lookup and deletion go through it; core keeps the index and the
release rule.

**The WASI environment plugin folds into `BuildImage`.** Each of its two
commands makes one broker call, `environment.resolve` or
`environment.realize`, and core checks everything afterwards, so it decides
as little as the runtime plugin did. With runtime-specific images its Nix
steps are part of how the Incus driver builds. No reason to keep it separate
was found.

**Builders get public internet access only**, as
[environment building](../docs/environment-building.md) already designs in
"Realize" and "Security boundary": public substituters and fetches, with a
session's destination isolation. Today's builder has no NIC; step 0g gives
it that access before the driver exists, and the code later moves into
`BuildImage`. Builds fetch from the public caches directly; a package cache
may come later, as a plugin.

**Step 1 comes first, as decided.** It creates `internal/runtimedriver` with
`BuildImage`, the image operations and `Settle` for builds, folds the
environment plugin in, and writes commit images against them. Its builder
already clones a Git bundle, which gitservice writes until the source driver's
`Export` takes over. The rest of step 8 stays last.

## Migration

Each step is a commit that leaves `go vet`, the unit tests, the TUI tests,
`nix flake check`, `nix build` and the VM scenarios green on a fresh install.
The groundwork steps have already run: 0d removed the `workspace.inspect`
method; 0e removed the loss inspection, and with it the workspace helpers,
their Git analysis and their slot in admission, removed freezing, and made
rename stopped-only and let removal accept a missing branch ref; 0h removed
session repair; 0g gave builders public egress.

| Step | What | Removed |
|---|---|---|
| M1 | The full interface, an in-memory driver and a conformance suite; the Incus driver in process over `runtimeincus`, with the guest file plan, the host readiness wait and services.go as driver code. Host configuration gets `runtime.driver: {name, config}`. | — |
| M2 | The 16 `plugin.RunRuntime` calls go to the driver: inspect, create, start and stop to the session operations, `runtime.services` to the services operations. Attachment keeps today's spec until M5. | The WASI runtime plugin, its broker, capability and grant; `runtime_plugin_id`; the daemon's `serviceBroker` |
| M3 | `Check`, `Describe`, `Inventory`; core computes the proofs, with one inventory bound instead of today's per-caller 4 or 64. | The proof scans |
| M4 | The remaining direct calls: `Delete`, the workspace operations with the stopped-storage check, which rename's Git edits use from core, `Settle`, and `Create` at an incarnation for recovering a partial create. | The other `l.runtime` calls |
| M5 | `OpenTerminal`; the daemon relays the terminal and the helper loses Incus access. | internal/attachment/incus.go, `ValidateAttachmentOperation`, the Incus fields of `AttachmentLaunch` |
| M6 | The daemon holds only `runtimedriver.Driver`; `runtimeincus` moves to `internal/runtimedriver/incus`, and a test fails if another package imports it or runs `incus`. | `lifecycle.runtime` |
| M7 | The protocol and the `p-runtime-incus` executable, with supervision and the conformance suite over the protocol; `path` and `sha256` replace `name`. | `name` |

## Departures from agreements

- [Plugin contract](../docs/plugin-contract.md): the `runtime` and
  `environment` capabilities, their grants and WASI ABI sections, and two of
  the six bundled packages go. An activation file with a runtime or
  environment entry is invalid. The interactive-host package's units and
  attach program become inputs of the Incus driver's session host; whether
  hosts stay selectable, and the rule that "merely running a native
  executable in a separate process does not meet this contract", are left to
  the plugin rework.
- [Runtime isolation](../docs/runtime-isolation.md): Incus becomes the Incus
  driver, and the properties above get their own section. Network `none`
  means nothing reachable rather than "no general NIC". The systemd host
  contract and "Project user services" become the Incus driver's.
  "Non-activating workspace access" shrinks to the workspace operations on a
  stopped session, with no helper.
- [Technology stack](../docs/technology-stack.md): `RuntimeBackend` and
  `EnvironmentBuilder`, never implemented, give way to `Driver` and
  `BuildImage`; "Product direction requires Incus support to become a bundled
  runtime plugin" is reversed.
- [Environment building](../docs/environment-building.md): the build becomes
  the driver's, the environment plugin goes, builders get public egress.
- [Session lifecycle](../docs/session-lifecycle.md): the authorities table
  names the driver; quiescence and the loss inspections go
  ([plan-shape.md](plan-shape.md#by-category)).
- [Communication boundaries](../docs/communication-boundaries.md): attachment
  steps 4 and 7, image-build communication.
- [Control API](../docs/control-api.md): the host configuration's `runtime`
  object and environment selection; the "Workspace inspection foundation"
  and "Workspace loss inspection" sections.
- [Glossary](../GLOSSARY.md): a "Runtime driver" entry; "Plugin" and
  "Host-side capability" no longer name a runtime provider; an internal
  session service is supervised by the runtime, not "through the runtime's
  systemd contract".
- [PROJECT.md](../PROJECT.md): no text changes. Destructive actions explain
  their loss by category, by the maintainer's decision; the runtime stays
  replaceable without rebuilding P.

## Options set aside

| Option | Why not |
|---|---|
| By the maintainer's decisions | Keeping the WASI runtime or environment plugin, a driver compiled into P, a list of persistent paths, freezing, `workspace.inspect`, a package cache now. |
| An interface at Incus's level | Fails the Kubernetes test. |
| Generic guest file and command operations (`Run`), with core driving assembly, host readiness, the Nix steps, and services by building `systemctl` commands | Core would depend on one guest layout, but images are runtime-specific. |
| A stronger `Inspect` instead of `Settle` | An empty operation list does not exclude a request Incus has not registered yet, and what settles depends on the effect. |
| The driver hands out egress addresses | The reservation would leave the session's SQLite transaction, so creations could race for an address. |
| hashicorp/go-plugin | MPL-2.0 is not on P's [licensing list](../docs/technology-stack.md#licensing-policy), and it adds gRPC next to P's JSON-RPC. |
| CRI as the protocol | Pods and containers only: no image build, no stopped-storage access, no Incus. |

## How it will be checked

- **Conformance suite** (`internal/runtimedriver/conformance`), against the
  in-memory driver and the Incus driver with its stubbed runner:
  - `Create`: no effect before the gate or after a failing one; a renamed
    runtime found by `Inventory` blocks it; `ErrMismatch` for drift; driver
    death after initialization, and between two guest writes including an
    uncertain one, recovers by settling and calling `Create` at the
    incarnation, which never initializes again;
  - settlement, from workspace_recovery_test.go's delayed-admission cases
    applied to sessions and builds: a create or build that lands after an
    absent observation stays `Unknown` and never authorizes a second one;
    `Applied` needs the exact outcome with nothing in flight; the same
    answers after a driver restart;
  - stopped storage, from workspace_mounts_test.go: a foreign mount at, below
    or above the workspace, escaped names included, is refused, and
    `/workspace-sibling` is not confused with it; a marker in a grant source
    or the endpoint directory is never returned;
  - a terminating object counts as present; a partial inventory is refused;
    `Delete` is refused for another incarnation and stops a running runtime
    first; a file written anywhere in the root survives stop and start; a
    session gets exactly its egress address; a builder reaches public
    destinations and nothing local; a host failure leaves the runtime
    stopped with a diagnostic;
  - the services cases in [services-plugin.md](services-plugin.md#how-it-will-be-checked).
- **Workers** on the in-memory driver: create, discard, delete, rename and
  project deletion, each with an unknown outcome after the gate; rename
  refused with `session_running`; discard and delete refused while a
  terminal is attached.
- **Attachment**, from helper_test.go: the lease is kept until proven
  completion; daemon loss closes the channel without re-registering; a failed
  launch never confirms. New: a driver failure ends terminals unproven and
  keeps the lease until settled or stopped.
- **Boundaries**: only the Incus driver imports `runtimeincus` or runs
  `incus`; internal/attachment has no Incus code; the daemon refuses to start
  a driver whose file or directory is owned by another account or is group-
  or world-writable, whose file has a second link while writable or lacks
  the owner's execute bit, or whose SHA-256 or protocol version differs; a
  driver killed in a call yields `ErrUnknownOutcome` and a settlement.
- **VM scenarios** pass or match the plan's known failures, except those the
  decisions change (freezing, inspection, scenario 28). Scenario 06 runs the
  conformance suite against real Incus. New scenarios kill the driver during
  creation and during an attachment. Create, start and attach times are
  reported before and after.
