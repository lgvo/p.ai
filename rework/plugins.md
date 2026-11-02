# Native plugins where they make sense

Status: proposed, 2026-11-02. Part of the [interface rework plan](REWORK-PLAN.md).

## Summary

**Today** P ships six plugin packages. Before this step, steps 1 and 8
fold two of them, the WASI runtime and environment modules, into the
[runtime driver](runtime-driver.md). Of the other four, source-git is a WASI
module, file-log is a manifest that names an append core performs, and
tmux-host and codex-adapter are files P installs into sessions. A WASI module
runs in wazero with no ambient authority and reaches the host only through a
broker that core reauthorizes, so it cannot exceed its grant. It also decides
almost nothing.

**Decided by the maintainer on 2026-11-02:**

- P's WASI plugins give way to native executables speaking JSON-RPC, like the
  runtime driver, where that makes sense.
- The runtime and environment plugins are gone, folded into the driver.
- Contracts may break, and P targets fresh installs: an old activation file is
  simply invalid.
- No package cache for now; one may come later, as a plugin or something
  similar.
- source-git becomes a source driver: one interface for all source and Git
  work, replaceable like the runtime driver, designed in
  [source-driver.md](source-driver.md). No WASI source module remains.

**Proposed:**

- **tmux-host's files move to the Incus driver.** Session hosts are no longer
  selected separately.
- **The event handler becomes the one executable plugin capability:** a
  native process the daemon starts and talks to over the driver's protocol.
  The bundled file-log becomes a small executable.
- **The Codex adapter stays a package of files**, handed to the driver in
  `Session.Assets`.
- Host configuration selects a plugin by path and SHA-256, as it selects the
  drivers. Every start checks the package's owner, modes and links before its
  digest. The activation file, grant lists, WASI, the brokers and wazero go.

An executable plugin gains the authority of P's account. The maintainer
accepted this on 2026-11-02 and keeps OS confinement as a later improvement
([Trust](#trust)).

## Before and after

```text
Before (as step 8 leaves it)
internal/daemon
├─ gitservice ─┬─ plugin.RunSourceGit ─► git.wasm in wazero ─► broker ─► git   9 command kinds
│              └─ git directly                                                 reads, snapshots
├─ events ─────┬─ file-log manifest ─► core appends the file
│              └─ handler.wasm in wazero ─► broker ─► core appends the file
└─ asset plans: tmux-host, source-git's p-git-ssh, codex-adapter ─► the driver's guest assembly

After
internal/daemon
├─ source driver (source-driver.md)                     all source and Git work
├─ events ─► p-event-file, or the selected handler      one process, JSON-RPC over a socket pair
└─ runtimedriver.Driver ─► p-runtime-incus              also holds the tmux host files
     └─ Session.Assets: the codex-adapter package
```

| | Today | After |
|---|---|---|
| Bundled packages | Six | Two: file-log and codex-adapter |
| Executable plugins | WASI modules in wazero, one short-lived instance per call | Native processes, one long-lived process per selected plugin |
| What contains them | The WASI sandbox and the broker | Trusted selection, protected files and the digest; the OS account P runs as |
| How they talk to P | One JSON command on stdin, broker calls, one JSON result on stdout | JSON-RPC 2.0 over a Unix socket pair, as the driver does |
| Selection | Host configuration names an activation file and a plugin ID; the file holds path, digest, grants and config | Host configuration holds path, digest and config |
| Grants | Manifest requests, matched exactly by the activation | None: the capability defines what P sends and accepts |

## Each plugin

runtime-incus and environment-nix are gone by steps 8 and 1. The other four
follow.

### source-git becomes the source driver

By the maintainer's decision of 2026-11-02, P gets a source driver: one
interface for all source and Git work, replaceable like the runtime driver,
designed in [source-driver.md](source-driver.md). For this document that
means three things. No WASI source-git module, broker or package remains, and
`plugin.RunSourceGit` goes. The source driver is a native executable,
selected, pinned and launch-checked like the runtime driver and executable
plugins ([Trust](#trust)). And `p-git-ssh` stops being a plugin file;
source-driver.md places it.

### tmux-host's files move to the Incus driver

The two units and `p-attach` define how the session host runs: they call
runtime-kit and tmux, and attachment compares the guest's files with the
selected package's bytes (`verifyAttachAssets` in
internal/runtimeincus/attach.go). A replacement package can therefore change
unit settings or attach options today. By the maintainer's decision, how a
runtime runs the session host is the runtime's own business
([runtime-driver.md](runtime-driver.md)); a Kubernetes driver would not run
systemd units at all. So the three files join the Incus driver, and
`runtime.host_plugin_id` and the session's host pin go. Customizing the host,
which a replacement package allows today, then means changing the driver or
a setting it offers. This answers what runtime-driver.md left to this
document: session hosts are not selected separately.

### The event handler becomes an executable plugin

file-log declares a `declarative` entry, and core's `appendFile`
(internal/plugin/filelog.go) does the checks, append and rotation. A WASI
handler (plugins/examples/filter-log) may only choose whether core appends;
with no sockets it cannot notify anything, so the handlers the documents
anticipate (notification, webhook, metrics, in
[session observability](../docs/session-observability.md#typed-p-events))
cannot be written in it.

Here a native executable makes sense: P starts it, sends it reduced events
and never waits on it for a decision. The bundled file-log becomes
`p-event-file`, built from today's append code, with the same checks, file and
rotation (open question 1 weighs keeping it in core). The example becomes a
native filter. Core keeps event reduction and validation, the 64-event queue,
the 2-second deadline and the dropped-events diagnostic; a handler failure
stays diagnostic and never rolls back state.

### The Codex adapter stays a package of files

P never runs it: Codex's hooks run it inside the session, and it sends
`status.report` to `/run/p/session.sock`. Its grant `agent.status.report` is
checked only at selection, while the socket accepts reports from any process
in the session
([communication boundaries](../docs/communication-boundaries.md#session-rpc-audience)),
so the session container is what contains it. Only the delivery changes: core
hands the verified file to the driver in `Session.Assets`, and the Incus
driver installs it at today's `/usr/libexec/p/codex-adapter`. The grant list
goes; the creation pin and runtime-kit's byte check stay.

**The session RPC keeps its per-message `v`.** [wire-types.md](wire-types.md)
left this to step 9. The adapter opens one connection per report and sends one
notification, which gets no reply, and any process in the session may use the
socket. The plugin protocol has a hello because P starts the process and owns
the connection; P starts nothing that talks to the session socket.

## The plugin model after the change

| Kind | Capabilities | Who runs it |
|---|---|---|
| Executable | `event-handler` | The daemon, as one long-lived process |
| Files | `agent-adapter` | The session, after the driver installs the files |

The runtime driver and the source driver use the same mechanism but keep
their own configuration blocks, in the forms
[runtime-driver.md](runtime-driver.md) and [source-driver.md](source-driver.md)
settle.

### Protocol

The driver's mechanism, in one package the driver client and plugin clients
share:

- the daemon starts the entry with no inherited environment or arguments and
  one end of a Unix socket pair, and speaks JSON-RPC 2.0 over it in both
  directions with P's strict JSON framing;
- in the hello, P names the protocol version, the capability and the plugin's
  config, and the plugin answers with its version; a mismatch or a refused
  config stops the start;
- a plugin calls P only through its capability's methods, and `event-handler`
  defines none;
- stdout and stderr are bounded and go only to the daemon's log, never to API
  clients.

The `event-handler` capability, as a sketch:

| Method | Sent by | Params | Result |
|---|---|---|---|
| hello | P | protocol, capability `event-handler`, config | protocol, plugin ID and version |
| `event.handle` | P | one `p.event/v1` envelope, as today | `handled` or `skipped` |

Events go one at a time, in order, each with today's 2-second deadline.

### Lifecycle

- **Start.** At daemon start, after the launch checks ([Trust](#trust)). A
  failed start refuses daemon startup, as an invalid event selection does
  today.
- **Crash.** A plugin that exits or misses a deadline is restarted with
  backoff, after fresh launch checks. Events that arrive meanwhile are dropped
  and counted, as a full queue drops them today. A failed check leaves it
  stopped, with a diagnostic. Nothing needs settling: P depends on no effect
  of an event call.
- **Stop.** The daemon closes the socket, waits briefly, then kills the
  process group.

### Packages, selection and installation

`p.plugin/v2` keeps v1's flat directory, digest rule and strict parsing. The
manifest keeps `schema`, `id`, `version`, `description` and `capability`, plus
`entry` for an executable capability, which conformance requires to name a
regular file in the package, or `assets` for a package of files. `api`,
`placement`, `runtime` and `requests` go. The 8 MiB per-file and 16 MiB
per-package limits were sized for WASI modules; step 9 resizes them.

Host configuration selects each plugin in its role's block, in the driver's
shape `{path, sha256, config}`:

```json
"events": {"path": "/absolute/share/p/plugins/file-log", "sha256": "<package digest>",
           "config": {"path": "/absolute/private/events.ndjson", "max_bytes": 4194304}}
```

`runtime.agent_adapter` takes the same shape without `config`.
`p.activation/v1` goes: without grants, it would repeat host configuration in
a second private file.

The digest covers names, sizes and bytes, not modes, so installation sets
them: `p plugins install` and `update` stage the entry 0500, every other file
0400 and the package directory 0500, all owned by P's account, each file with
one link.

| Command | After |
|---|---|
| `p plugins list`, `conformance` | Unchanged: validate packages and print digests; never run code |
| `p plugins defaults` | Prints the host-configuration blocks for the bundled file-log and Codex adapter; the NixOS `bundledActivation` option writes them into the host configuration it generates |
| `p plugins install`, `update`, `remove` | Offline, an exact approved digest, `STATE/plugins/packages/SHA256`, now with the modes above. Removal is refused while host configuration selects the package or a session's creation pins it; only the agent adapter is pinned now |
| `p plugins run-event HOST_JSON EVENT` | Starts the handler that the host configuration's `events` block selects, with its digest and config, after the launch checks; sends one event and prints the result. A private host configuration holding only `state_dir` and `events` is enough to try a package. Replaces `emit` and today's `run-event`, which read an activation file |
| `p plugins activate`, `plan-assets` | Gone, with the activation file and with core-chosen guest paths |

### Conformance

- **Static:** `p plugins conformance`, as today, plus the `entry` rule.
- **Behavioral:** a conformance suite for `event-handler`, like the driver's,
  run against the bundled `p-event-file` and the separately built example:
  the hello and its refusals; the launch checks; in-order delivery; the
  deadline; a crash, a restart and the counted drops; malformed or oversized
  replies refused; output kept from clients.

## Trust

**Selection** is unchanged: only trusted host configuration names a plugin,
by path and SHA-256, for the daemon and for `p plugins run-event` alike.
Repositories, requests, sessions and agents never do.

**Launch checks.** Today the WASI runner executes the bytes it hashed
(`selectedModule` in internal/plugin/wasi_sandbox.go). A native start runs a
file by name, so the file must be one only root or P's account can change.
Today's checks do not ensure that: `CheckTrustedAncestors`
(internal/control/config.go) starts at the parent of the path it is given, and
`Conformance` (internal/plugin/package.go) checks no file's owner, mode or
link count. Before every start, and before hashing, the daemon therefore
requires:

- the package directory itself and its ancestors: owned by root or P's
  account and not group- or world-writable, with today's exception for a
  root-owned sticky ancestor;
- every file: regular, owned by root or P's account, not group- or
  world-writable, and with one link, unless root owns it and no one may write
  it (the Nix store hard-links identical read-only files), so no writable
  alias elsewhere shares its bytes;
- the entry: its owner's execute bit.

It then hashes the package and starts the entry it checked. Between check and
start only root or P's account can change the bytes, and both already hold
more than a plugin gains. A managed install meets these rules by construction;
the bundled catalog in the Nix store meets them as root-owned, read-only
files. The runtime driver and the source driver apply the same launch checks
to their executables.

**What a plugin gains.** An executable plugin runs as P's account inside the
daemon's service sandbox. On the NixOS service (nix/module.nix) that means no
capabilities and `NoNewPrivileges`, but write access to P's state directory,
membership in the `incus` group and no network restriction; an owner-run
daemon gives it the owner's account.

| Who | Still contained by | No longer contained |
|---|---|---|
| Agents | The session runtime and the session socket's narrow methods | — |
| The Codex adapter | The session it runs in | — |
| Executable plugins | Trusted selection, the launch checks and the digest; P sends only reduced, redacted events and the plugin's config; no reply changes P's state | The process can read and write P's state (SQLite, Git repositories, the Git server and client keys, other plugins' managed files), open the Incus user socket and use the network. WASI prevented all of it |

**What changes in PROJECT.md.** Its "Bound delegated authority" signal,
"Agents and plugins cannot exceed their declared grants", then holds for
agents and the Codex adapter but not for executable plugins: no grant is
narrower than P's account. The same reaches the extension goal's "operate
autonomously within those grants", the project model's "an activated plugin
may operate autonomously only within them", PRODUCT.md's "agent-authored
plugins remain within user-approved capabilities" and the glossary. The
runtime driver already makes this trade for one component ("The driver is
trusted code").

**Decided by the maintainer on 2026-11-02:** executable plugins are trusted
code. Selection by trusted host configuration, digest pinning and the launch
checks contain them; there is no sandbox. Confining them at the OS level stays
an open improvement for later, kept visible
([below](#later-improvement-confining-executable-plugins)).

### Proposed wording

These replacements say what P enforces. PROJECT.md, PRODUCT.md and the
glossary change in step 9's commit, together with the code, not before.

| Where | Today | Proposed |
|---|---|---|
| PROJECT.md, "Bound delegated authority" | Developers can let agents and plugins work independently while their capabilities, publication authority, and potential destructive effects remain explicit and contained. | Developers can let agents and plugins work independently while their capabilities, publication authority, and potential destructive effects remain explicit; agents and the plugins that run inside sessions also remain contained. |
| Its success signal | Agents and plugins cannot exceed their declared grants, failures do not silently widen authority, and destructive actions explain what will be lost before proceeding. | Agents, and plugins that run inside sessions, cannot exceed the session's grants. A plugin runs on the host only when trusted host configuration selects its exact, protected package; it is trusted code with P's authority, and P sends it only what its capability defines. Failures do not silently widen authority, and destructive actions explain what will be lost before proceeding. |
| "Make extension a product capability", success signal | … Trusted developer configuration controls its activation and capabilities, after which it can operate autonomously within those grants. | … Trusted developer configuration controls which exact package is activated for which capability, after which it can operate autonomously: inside a session within the session's grants, or on the host as trusted code. |
| Project model | Trusted developer configuration controls plugin activation and grants; an activated plugin may operate autonomously only within them. | Trusted developer configuration controls plugin activation by exact package; an activated plugin may operate autonomously, inside a session within its grants or on the host as trusted code with P's authority. |
| PRODUCT.md, success signals | agent-authored plugins remain within user-approved capabilities and can be disabled or replaced without compromising P's authoritative state; | agent-authored plugins run only when the user selects their exact package, stay within the session's capabilities when they run inside a session, and can be disabled or replaced without compromising P's authoritative state; |
| Glossary, "Plugin", boundaries | A plugin may provide a first- or third-party runtime, internal or external session service, host-side capability, integration, or workflow. It is not P core. | A plugin may provide a host-side capability, an internal or external session service, an integration, or a workflow. It is not P core or a driver. |
| Glossary, "Plugin", rules | … Registration does not activate a plugin or grant authority. Trusted developer configuration controls activation, implementation selection, and grants; an activated plugin may operate autonomously only within them. | … Registration does not activate a plugin. Trusted developer configuration activates an exact package for one capability. A plugin that runs inside a session has the session's grants and no more; an executable plugin runs on the host as trusted code with P's authority. |
| Glossary, "Grant", rules | A session or plugin cannot grant itself authority. Each grant has explicit scope and is enforced by the authority that owns the capability. | The same, followed by: Executable plugins hold no grants; trusted configuration selects them, and they run with P's authority. |
| Glossary, "Host-side capability" | Examples: A runtime provider or event handler. Rules: It receives only the authority granted for its declared role and does not become a session service merely because its result affects one. | Examples: An event handler. Rules: An executable plugin that provides it runs as trusted code with P's authority, and P sends it only what its capability defines. It does not become a session service merely because its result affects one. |

## Later improvement: confining executable plugins

Not designed, and not part of step 9. When native plugins land, step 9's
commit records the gap as a debt entry in [docs/debts](../docs/debts/), listed
in [DEBT.md](../DEBT.md); the gap exists only from then on.

**What it would protect.** P's state (SQLite, the Git repositories, the Git
server and client keys, staged packages), the Incus user socket, and the
network. A confined event handler would reach only its own configured files
and the destinations its job needs. Confinement would bring grants back in a
new form: a plugin's config would have to name what the confinement allows.

| Mechanism | NixOS service | Owner-run daemon | Unknown |
|---|---|---|---|
| A separate account, without P's state or the `incus` group | The daemon cannot switch accounts under `NoNewPrivileges` and no capabilities. It fits only if the NixOS module runs each plugin as a service of its own account and the daemon connects to it rather than starting it. | A fixed sudo rule, as the egress proofs already use. | How the digest and launch checks reach a process the daemon does not start. |
| Landlock rules applied before the plugin starts | Fits: unprivileged, and the service already sets `NoNewPrivileges`, which Landlock requires. | Fits, with no-new-privileges set for the child. | Whether Landlock on P's kernels can deny connecting to a pathname Unix socket such as Incus's; its TCP rules restrict ports, not addresses. A process can only restrict itself, so it probably needs a small launcher. |
| systemd sandboxing of the plugin process: a transient unit with `ProtectSystem`, `InaccessiblePaths`, `IPAddressDeny` and the like | Only if P's system account may start units, which it cannot without a polkit rule. | `systemd-run --user`, if the owner has a user manager. | Which protections a user manager can apply, and whether a unit per plugin fits the daemon's supervision. |

None of this was tried. The Incus user socket is the hardest part, because
P's account reaches it through group membership.

## What users notice

- A default installation writes the same events file and installs the Codex
  adapter at the same path.
- Host configuration: `git.activation_path`, `git.source_plugin_id` and
  `runtime.host_plugin_id` go; `events` and `runtime.agent_adapter` take a path, a digest and config
  instead of an activation file and a plugin ID.
- `p plugins defaults` covers two packages; `activate`, `plan-assets` and
  `emit` go; `run-event` reads its selection from host configuration.
- A WASI event handler no longer runs; it must be rebuilt as a native
  executable.
- Fresh installs only, by the maintainer's decision: an activation file is
  invalid, and nothing migrates.

## What is removed

- The WASI runner, brokers and activation code in internal/plugin, and
  `github.com/tetratelabs/wazero`, which only internal/plugin imports.
- `p.command/v1`, `p.command-result/v1`, `p.broker/v1`, `p.asset-plan/v1`,
  `p.activation/v1`, the `p_broker_v1.call` import, and grants.
- The source-git and tmux-host packages, the WASI example and fixtures, and
  the `wasip1` builds.
- The host fields of `CreationSelection`.

## Departures from agreements

- [Plugin contract](../docs/plugin-contract.md): "WASI is the selected
  executable boundary … Merely running a native executable in a separate
  process does not meet this contract" is reversed, and "Packages never
  receive … unrestricted filesystem or network access" holds for executable
  plugins only as far as P's account is limited. Capabilities shrink to
  `event-handler` and `agent-adapter` with no grants; the declarative and WASI
  kinds, their ABI sections and trusted activation go; asset destinations
  become the driver's.
- [PROJECT.md](../PROJECT.md) and [product direction](../docs/PRODUCT.md): the
  lines replaced in [Proposed wording](#proposed-wording); the first-party
  composition becomes the Incus driver, the source driver, file-log and the
  Codex adapter.
- [MVP status](../docs/mvp-status.md) and
  [technology stack](../docs/technology-stack.md): the tmux host and Git
  source as WASI or asset plugins ("Product direction requires tmux support to be a bundled
  internal-session-service plugin"), the event-handler seam, and wazero.
- [Control API](../docs/control-api.md) and [glossary](../GLOSSARY.md): the
  plugin selections in host configuration; "Plugin", "Grant" and "Host-side
  capability", as in [Proposed wording](#proposed-wording).

## Effects on other rework documents

- [runtime-driver.md](runtime-driver.md): "`p plugins defaults` lists four
  packages instead of six" becomes two; "tmux from the interactive-host
  package" becomes the driver's own files;
  `Session.Assets` carries only the agent adapter. "Repositories, requests
  and plugins never reach it" holds for the driver's socket pair, but an executable plugin running as P's
  account can open the Incus user socket. Whether a session's creation pins
  the driver's digest is not yet said there.
- [wire-types.md](wire-types.md): its open question 1 is answered; the session
  RPC keeps `v`.
- [REWORK-PLAN.md](REWORK-PLAN.md): the Plugins row and step 9 expect the
  WASI plugins to become JSON-RPC executables. Here source-git becomes the
  source driver instead, and the one executable plugin is the event handler,
  which was declarative rather than WASI.
- [source-driver.md](source-driver.md): its driver follows the selection,
  pinning and launch checks of [Trust](#trust).

## Options set aside

| Option | Why not |
|---|---|
| Keep WASI for event handlers | A WASI handler can only append or skip; with no sockets it cannot notify anything. |
| Session hosts as a package the Incus driver loads | The session host is the runtime's own business by the maintainer's decision, and a host package contract shared by drivers would tie them all to systemd units. |
| Keep `p.activation/v1` as a separate file | Without grants it repeats host configuration. |
| One process per event, as with WASI | A start per event, and no connection a handler can keep, for example to a webhook. |
| Execute verified bytes from memory (`memfd_create` and `fexecve`) | The launch checks already leave only root and P's account able to change the file. |
| Package the runtime driver like a plugin | Step 9 does not need it: the driver's settled `{path, sha256}` form works with the same launch checks. It can come later. |
| A package cache | Deferred by the maintainer; it may come later as a plugin. |
| OS confinement of executable plugins now | Decided by the maintainer: a later improvement ([above](#later-improvement-confining-executable-plugins)). |
| hashicorp/go-plugin | Set aside for the driver: MPL-2.0 and gRPC. |

## Open questions

1. **Does the bundled file-log stay a plugin?**
   - (a) A small executable, `p-event-file`. *Recommended:* a default
     installation then runs the protocol other handlers use, which is how
     PRODUCT.md asks first-party functionality to prove its contracts. The
     cost is one process and one more binary.
   - (b) Core writes the file from host configuration, and nothing in a
     default installation exercises the plugin protocol.

## How it will be checked

- **Unit tests:** the `event-handler` conformance suite; the launch
  checks refuse a group- or world-writable package directory or file, a
  foreign-owned one, a second link to a file P's account owns, and an entry
  without its execute bit, and accept root-owned read-only files with several
  links; the same checks on both drivers' executables; staging writes the entry
  0500, other files 0400 and the directory 0500; conformance refuses an
  executable capability without `entry`; host configuration rejects the old
  activation fields; removal is refused while a package is selected or
  pinned.
- **VM scenarios:** 01 and 13 (file-log through the CLI and the daemon) with
  the executable; 03 with the native example; 27 (Codex
  adapter) unchanged; 48 (bundled distribution) with two packages; 55 (NixOS
  service) with the selections in host configuration. 54 (plugin management)
  installs the file-log package with `p plugins install`, then handles an
  event through `p plugins run-event` and through the daemon, both with a
  host configuration that selects the staged package. New: kill the handler
  while the daemon runs and see later events delivered after the restart with
  the drop logged; a changed digest or a loosened mode refuses start.
- **Boundaries:** go.mod has no wazero, and no `wasip1` build remains.
- **Documents:** step 9's commit applies the proposed wording and adds the
  confinement debt entry.
