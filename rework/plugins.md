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

**Proposed:**

- **source-git folds into core.** It decides nothing, and as a native process
  it would hold write access to every repository. Its `p-git-ssh` file moves
  to the Incus driver.
- **tmux-host's files move to the Incus driver.** Session hosts are no longer
  selected separately.
- **The event handler becomes the one executable plugin capability:** a
  native process the daemon starts and talks to over the driver's protocol.
  The bundled file-log becomes a small executable.
- **The Codex adapter stays a package of files**, handed to the driver in
  `Session.Assets`.
- Host configuration selects a plugin by path and SHA-256, as it selects the
  driver. Every start checks the package's owner, modes and links before its
  digest. The activation file, grant lists, WASI, the brokers and wazero go.

An executable plugin gains the authority of P's account. That departs from
PROJECT.md and waits on the maintainer ([Trust](#trust), open question 2).

## Before and after

```text
Before (as step 8 leaves it)
internal/daemon
├─ gitservice ─┬─ plugin.RunSourceGit ─► git.wasm in wazero ─► broker ─► git   9 command kinds
│              └─ git directly                                                 reads, snapshots, loss analysis
├─ events ─────┬─ file-log manifest ─► core appends the file
│              └─ handler.wasm in wazero ─► broker ─► core appends the file
└─ asset plans: tmux-host, source-git's p-git-ssh, codex-adapter ─► the driver's guest assembly

After
internal/daemon
├─ gitservice ─► git                                    all Git work
├─ events ─► p-event-file, or the selected handler      one process, JSON-RPC over a socket pair
└─ runtimedriver.Driver ─► p-runtime-incus              also holds the tmux host files and git-ssh
     └─ Session.Assets: the codex-adapter package
removed: wazero, the brokers, p.command/v1, p.broker/v1, p.asset-plan/v1, p.activation/v1, grants
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

| Package | What it decides | Its authority today | After |
|---|---|---|---|
| runtime-incus, environment-nix | Nothing | Broker calls | Gone in steps 8 and 1 |
| source-git | Ref page size, tighter transport limits, refusal | Broker calls on one project; core checks every result | Folds into core; `p-git-ssh` moves to the Incus driver |
| tmux-host | How the session host runs, within runtime-kit's contract | Root-owned units in the session | Files of the Incus driver |
| file-log | Nothing: core does the work | None of its own | A bundled executable plugin |
| codex-adapter | How Codex events map to P's conditions | The session's | Stays a package of files |

### source-git folds into core

The module (plugins/bundled/source-git/main.go) sequences nine command kinds.
Each broker call runs Git with arguments core chose, and core checks every
postcondition; the module only picks the ref page size (1 to 8), tightens
transport limits or refuses. Most Git work already skips it: reads
(`InspectBranchRef`, 26 daemon call sites), snapshots and loss analysis run
Git directly, and project deletion removes the repository itself. As a native
process it would decide the same nothing, with write access to every
repository.

The nine operations become ordinary `gitservice` code built from today's
broker bodies. The deadlines and limits on native Git and SSH stay; the limits
on the module (memory, message sizes, call counts, the package recheck) go.
The `git` block of host configuration keeps only `listen`.

`p-git-ssh` runs `ssh -F /etc/p/git/ssh_config`, and runtime-kit points
`GIT_SSH` at it. It is how the guest reaches P's Git endpoint, which the
driver's `Session` already carries, so it becomes a file of the Incus driver.

### tmux-host's files move to the Incus driver

The units run `/usr/libexec/p/runtime-kit` subcommands and `p-attach` runs
`/usr/libexec/p/tmux`, links from P's base image (runtime/image.nix) that
runtime-kit checks (`ValidateFixedAssets`). A replacement host must match
runtime-kit and the image, so it cannot change on its own; the driver contract
already makes the session host the driver's business, and one host exists.
The three files join the Incus driver, and `runtime.host_plugin_id` and the
session's host pin go. This answers what [runtime-driver.md](runtime-driver.md)
left to this document: session hosts are not selected separately.

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
rotation (open question 3 weighs keeping it in core). The example becomes a
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

The runtime driver uses the same mechanism but keeps its own `driver` block,
in the form [runtime-driver.md](runtime-driver.md) settles.

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

The digest covers names, sizes and bytes, not modes, so `p plugins install`
and `update` must set modes themselves. Today `StagePackage`
(internal/plugin/managed.go) writes every file 0400, which leaves an entry
that cannot run. Step 9 changes it to write the entry 0500, every other file
0400 and the package directory 0500, all owned by P's account, each file with
one link.

| Command | After |
|---|---|
| `p plugins list`, `conformance` | Unchanged: validate packages and print digests; never run code |
| `p plugins defaults` | Prints the host-configuration blocks for the bundled file-log and Codex adapter; the NixOS `bundledActivation` option writes them into the host configuration it generates |
| `p plugins install`, `update`, `remove` | Offline, an exact approved digest, `STATE/plugins/packages/SHA256`, now with the modes above. Removal is refused while host configuration selects the package or a session's creation pins it; only the agent adapter is pinned now |
| `p plugins run-event PACKAGE CONFIG EVENT` | Runs the launch checks, starts the handler once, sends one event and prints the result; replaces `emit` and today's `run-event` |
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
by path and SHA-256. Repositories, requests, sessions and agents never do.

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
files. The same checks apply to the runtime driver's executable:
[runtime-driver.md](runtime-driver.md) pins it by path and SHA-256 but states
no owner, mode or link check, so it has the same gap.

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

**The decision required.** PROJECT.md's "Bound delegated authority" signal,
"Agents and plugins cannot exceed their declared grants", then holds for
agents and the Codex adapter but not for executable plugins: no grant is
narrower than P's account. The same reaches the extension goal's "operate
autonomously within those grants", the project model's "an activated plugin
may operate autonomously only within them", and PRODUCT.md's "agent-authored
plugins remain within user-approved capabilities": for an executable plugin,
the owner's approval is all or nothing. The runtime driver already makes this
trade for one component ("The driver is trusted code"). Open question 2 asks
the maintainer to confirm it.

## What users notice

- A default installation writes the same events file, installs the Codex
  adapter at the same path, and runs Git with the same behavior and limits.
- Host configuration: `git` keeps only `listen`; `runtime.host_plugin_id`
  goes; `events` and `runtime.agent_adapter` take a path, a digest and config
  instead of an activation file and a plugin ID.
- `p plugins defaults` covers two packages; `activate`, `plan-assets` and
  `emit` go; `run-event` takes a package.
- A package whose directory or files are writable by others, foreign-owned or
  hard-linked does not start.
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
- The source and host fields of `CreationSelection`.

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
  lines quoted under [Trust](#trust); the first-party composition becomes the
  Incus driver, file-log and the Codex adapter; "first-party implementations
  can be replaced" no longer covers Git source.
- [MVP status](../docs/mvp-status.md) and
  [technology stack](../docs/technology-stack.md): the tmux host and Git
  source as plugins ("Product direction requires tmux support to be a bundled
  internal-session-service plugin"), the event-handler seam, and wazero.
- [Control API](../docs/control-api.md) and [glossary](../GLOSSARY.md): the
  plugin selections in host configuration; "Plugin", "Grant" and "Host-side
  capability".

## Effects on other rework documents

- [runtime-driver.md](runtime-driver.md): "`p plugins defaults` lists four
  packages instead of six" becomes two; "tmux from the interactive-host
  package" becomes the driver's own files, and `p-git-ssh` joins them;
  `Session.Assets` carries only the agent adapter. Its executable needs the
  launch checks above. "Repositories, requests and plugins never reach it"
  holds for the driver's socket pair, but an executable plugin running as P's
  account can open the Incus user socket. Whether a session's creation pins
  the driver's digest is not yet said there.
- [wire-types.md](wire-types.md): its open question 1 is answered; the session
  RPC keeps `v`.
- [REWORK-PLAN.md](REWORK-PLAN.md): the Plugins row and step 9 expect the
  WASI plugins to become JSON-RPC executables. Here source-git folds into core
  instead, and the one executable plugin is the event handler, which was
  declarative rather than WASI.

## Options set aside

| Option | Why not |
|---|---|
| Keep WASI for event handlers | A WASI handler can only append or skip; with no sockets it cannot notify anything. |
| source-git as a native executable, or a source driver for all Git work | Open question 1. |
| Session hosts as a package the Incus driver loads | They are bound to runtime-kit and P's base image, and a host contract would tie every driver to systemd. |
| Keep `p.activation/v1` as a separate file | Without grants it repeats host configuration. |
| One process per event, as with WASI | A start per event, and no connection a handler can keep, for example to a webhook. |
| Execute verified bytes from memory (`memfd_create` and `fexecve`) | The launch checks already leave only root and P's account able to change the file. |
| Package the runtime driver like a plugin | Step 9 does not need it: the driver's settled `{path, sha256}` form works with the same launch checks. It can come later. |
| A package cache | Deferred by the maintainer; it may come later as a plugin. |
| hashicorp/go-plugin | Set aside for the driver: MPL-2.0 and gRPC. |

## Open questions

1. **Does source-git fold into core?**
   - (a) Fold it into `gitservice`. *Recommended:* the module decides nothing
     core does not check again, most Git work already skips it, and nothing
     else would gain write access to P's repositories.
   - (b) A native executable with today's nine methods: replaceable without
     rebuilding P, but a replacement can change only page sizes and limits.
   - (c) A source driver for all Git work: worth it only for a second source
     system, which P's model does not plan.
2. **Are executable plugins trusted code?**
   - (a) Yes. PROJECT.md, PRODUCT.md and the glossary then say what P
     enforces: agents and the Codex adapter cannot exceed their grants; an
     executable plugin runs only when trusted configuration selects its exact,
     protected digest, and P gives it only what its capability defines.
     *Recommended* for step 9: the only executable plugin left is an event
     handler, and a sandbox can later wrap the process start without changing
     the protocol. Revisit when an executable plugin from outside P is wanted.
   - (b) Contain executable plugins at the OS level before step 9 lands, and
     keep the signal. Nothing is designed: under `NoNewPrivileges` the daemon
     cannot switch accounts itself, and whether Landlock on P's kernels can
     deny connecting to a pathname Unix socket such as Incus's was not
     verified.
3. **Does the bundled file-log stay a plugin?**
   - (a) A small executable, `p-event-file`. *Recommended:* a default
     installation then runs the protocol other handlers use, which is how
     PRODUCT.md asks first-party functionality to prove its contracts. The
     cost is one process and one more binary.
   - (b) Core writes the file from host configuration, and nothing in a
     default installation exercises the plugin protocol.

## How it will be checked

- **Unit tests:** gitservice's nine operations without a plugin, with the same
  refusals and limits; the `event-handler` conformance suite; the launch
  checks refuse a group- or world-writable package directory or file, a
  foreign-owned one, a second link to a file P's account owns, and an entry
  without its execute bit, and accept root-owned read-only files with several
  links; the same checks on the driver's executable; staging writes the entry
  0500, other files 0400 and the directory 0500; conformance refuses an
  executable capability without `entry`; host configuration rejects the old
  activation fields; removal is refused while a package is selected or
  pinned.
- **VM scenarios:** 01 and 13 (file-log through the CLI and the daemon) with
  the executable; 03 with the native example; 04, 07, 08, 14 and 16 to 20 (Git
  and origin) with unchanged outcomes and no source package; 27 (Codex
  adapter) unchanged; 48 (bundled distribution) with two packages; 55 (NixOS
  service) with the selections in host configuration. 54 (plugin management)
  installs the file-log package with `p plugins install`, then handles an
  event through `p plugins run-event` against the staged package and through
  a daemon that selects it. New: kill the handler while the daemon runs and
  see later events delivered after the restart with the drop logged; a
  changed digest or a loosened mode refuses start.
- **Boundaries:** go.mod has no wazero, and no `wasip1` build remains.
