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
  driver. The activation file, grant lists, WASI, the brokers and wazero go.

**The cost:** an executable plugin is trusted code with the authority of P's
account, which covers P's state, Git repositories and keys, and its Incus
project. PROJECT.md's "Agents and plugins cannot exceed their declared grants"
then holds for agents and for the Codex adapter, but not for executable
plugins. That departure waits on the maintainer (open question 2).

**What users notice:** a default installation behaves as before: the same
events file, the same Codex adapter, the same Git behavior. Host configuration
and the `p plugins` commands change shape, and a third-party WASI event
handler must be rebuilt as a native executable.

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
| What contains them | The WASI sandbox and the broker | Trusted selection and the digest; the OS account P runs as |
| How they talk to P | One JSON command on stdin, broker calls, one JSON result on stdout | JSON-RPC 2.0 over a Unix socket pair, as the driver does |
| Selection | Host configuration names an activation file and a plugin ID; the file holds path, digest, grants and config | Host configuration holds path, digest and config |
| Grants | Manifest requests, matched exactly by the activation | None: the capability defines what P sends and accepts |

## Each plugin

| Package | What it does | What it decides | Its authority | After |
|---|---|---|---|---|
| runtime-incus | Sequences nine runtime commands | Nothing | Broker calls on one session | Gone in step 8 |
| environment-nix | Runs two Nix steps | Nothing | One broker call per step | Gone in step 1 |
| source-git | Sequences nine Git command kinds; ships `p-git-ssh` | Ref page size, tighter transport limits, refusal | Broker calls on one project; core checks every result | Folds into core; `p-git-ssh` moves to the Incus driver |
| tmux-host | Two systemd units and the attach program | How the session host runs, within runtime-kit's contract | Root-owned units in the session | Files of the Incus driver |
| file-log | Names core's file append | Nothing: core does the work | None of its own | A bundled executable plugin |
| codex-adapter | A program Codex's hooks run in the session; reports status to the session socket | How Codex events map to P's conditions | The session's | Stays a package of files |

### source-git folds into core

- `internal/gitservice` sends nine command kinds through `plugin.RunSourceGit`:
  project ensure, ref listing, transport planning, source observation, branch
  create and delete, and origin observe, fetch and publish. The module
  (plugins/bundled/source-git/main.go) checks the command and makes up to 16
  broker calls. Each runs native Git with arguments core chose, and core
  checks every postcondition. The module's only freedoms are the ref page size
  (1 to 8), tighter transport limits, and refusing; the git-alternate fixture
  exercises exactly those.
- Most Git work already bypasses it. The daemon has 26 call sites of
  `InspectBranchRef`; snapshots (`CapturePinnedCommit`), loss analysis
  (`AssignedBranchSnapshot`, `BranchRemovalLoss`, `RetainedLossRefs`,
  `ProjectRemovalLoss`) and `PCommitPresent` run Git directly; project
  deletion removes the repository itself (project_delete_process.go).
- As a native process it would decide the same nothing, with write access to
  every repository and to the Git keys in P's state.

The nine operations become ordinary `gitservice` code, with today's broker
bodies as their implementation. The deadlines and the limits on native Git
and SSH stay: 1,024 heads and 300 KiB per ref query, 2 seconds for local
commands, 35 seconds and two concurrent operations for origin commands, and
the transport ceilings. The limits on the module go: 16 MiB of memory, 4 KiB
messages, 16 broker calls, the four-slot pool shared with WASI event
handlers, and the package recheck on every call (`revalidate`). The `git`
block of host configuration keeps only `listen`, and a session's creation
evidence stops pinning a source package.

`p-git-ssh` is two lines that run `ssh -F /etc/p/git/ssh_config`, and
runtime-kit points `GIT_SSH` at it. It is how the guest reaches P's Git
endpoint, which the driver's `Session` already carries (`GitKey`,
`Endpoints`, `Workspace`), so it becomes a file of the Incus driver.

### tmux-host's files move to the Incus driver

- The units run `/usr/libexec/p/runtime-kit` subcommands, and `p-attach` runs
  `/usr/libexec/p/tmux`. Both links come from P's base image
  (runtime/image.nix), and runtime-kit checks that the units are the image's
  fixed links (`ValidateFixedAssets`). A replacement host has to match
  runtime-kit and the image, so it cannot change on its own.
- The driver contract makes the session host the driver's business. A
  Kubernetes driver would not run systemd units at all.
- One host exists: tmux.

The three files join the Incus driver, and `runtime.host_plugin_id` and the
session's host pin go. A different session host means a different driver, or
a setting of one. This answers what [runtime-driver.md](runtime-driver.md)
left to this document: session hosts are not selected separately.

### The event handler becomes an executable plugin

- file-log declares `runtime.kind: declarative` with the entry
  `event.file.append`. Core's `appendFile` (internal/plugin/filelog.go) does
  the private-path checks, the append and the rotation; the package holds no
  code.
- A WASI handler, such as plugins/examples/filter-log, may make one broker
  call with no arguments, after which core appends its own event to the
  configured file. The handler only chooses between appending and skipping.
  WASI gives it no sockets, so the handlers the documents anticipate
  (notification, webhook, metrics, in
  [session observability](../docs/session-observability.md#typed-p-events))
  cannot be written in it.

Here a native executable makes sense: P starts it, sends it reduced events
and never waits on it for a decision. The bundled file-log becomes
`p-event-file`, a small executable built from today's append code, with the
same checks, file and rotation. It writes as P's account, as the daemon does
today. Open question 3 weighs keeping the append in core instead. The example
becomes a native filter that appends ready events to its own configured file.

Core keeps event reduction and validation (`Event.Validate`), the 64-event
queue, the 2-second deadline and the dropped-events diagnostic. A handler
failure stays diagnostic and never rolls back state.

### The Codex adapter stays a package of files

- It is one file, `p-codex-adapter`, a Python program. P never runs it:
  Codex's hooks run it inside the session, and it sends `status.report` to
  `/run/p/session.sock`.
- Its grant `agent.status.report` is checked only when the package is
  selected (lifecycle.go, assets.go). The session socket accepts a report
  from any process in the session, because it authenticates by placement
  ([communication boundaries](../docs/communication-boundaries.md#session-rpc-audience)).
  The session container is what contains it.
- Creation pins its ID and digest, and runtime-kit checks the bytes in the
  guest (`ValidateFixedAssets`).

Only the delivery changes: core hands the verified file to the driver in
`Session.Assets`, and the Incus driver installs it where it is today,
`/usr/libexec/p/codex-adapter`, the path the user guide tells users to run.
The grant list goes; the pin stays.

**The session RPC keeps its per-message `v`.** [wire-types.md](wire-types.md)
left to this step whether the session socket moves to a hello. It does not.
The adapter opens one connection per report and sends one `status.report`
notification, which gets no reply (`report` in p-codex-adapter), and any
process in the session may use the socket. The plugin protocol has a hello
because P starts the process and owns the connection; P starts nothing that
talks to the session socket.

## The plugin model after the change

| Kind | Capabilities | Who runs it | What contains it |
|---|---|---|---|
| Executable | `event-handler` | The daemon, as one long-lived process | Trusted selection and the digest; the OS account P runs as |
| Files | `agent-adapter` | The session, after the driver installs the files | The session's runtime boundary |

The runtime driver uses the same mechanism as an executable plugin but keeps
its own `driver` block, because P cannot run without one.

### Trust

- Only trusted host configuration selects a plugin, by path and SHA-256.
  Repositories, requests, sessions and agents never do. This is unchanged.
- The daemon checks the package digest before every start and refuses a
  changed package. The package must sit below trusted ancestors, as host
  configuration must (`CheckTrustedAncestors`): no group- or world-writable or
  foreign-owned directory, so only root or P's account can swap a file
  between the check and the start. The digest covers the package's own bytes,
  not an interpreter a script names or the libraries it loads.
- An executable plugin starts with no inherited environment or arguments; its
  config arrives in the hello. It runs as P's account inside the daemon's
  service sandbox. On the NixOS service (nix/module.nix) that means no
  capabilities, `NoNewPrivileges`, a read-only system apart from P's state
  directory and the endpoint prefix, membership in the `incus` group, and no
  network restriction. Under an owner-run daemon a plugin has the
  owner's account.
- A package of files never runs on the host.

### Protocol

The driver's mechanism, in one package that the driver client and the plugin
clients share:

- the daemon starts the executable with one end of a Unix socket pair and
  speaks JSON-RPC 2.0 over it, in both directions, with P's strict JSON
  framing;
- in the hello, P names the protocol version, the capability and the
  plugin's config, and the plugin answers with its own version. A version
  mismatch or a refused config stops the start;
- a plugin calls P only through the methods its capability defines, and
  `event-handler` defines none;
- stdout and stderr are bounded and go only to the daemon's log; today's
  rule that plugin output never reaches API clients stays.

The `event-handler` capability, as a sketch:

| Method | Sent by | Params | Result |
|---|---|---|---|
| hello | P | protocol, capability `event-handler`, config | protocol, plugin ID and version |
| `event.handle` | P | one `p.event/v1` envelope, as today | `handled` or `skipped` |

Events go one at a time, in order, each with today's 2-second deadline.

### Lifecycle

- **Start.** At daemon start, after the digest check. A failed start refuses
  daemon startup, as an invalid event selection does today.
- **Crash.** A plugin that exits or misses a deadline is restarted with
  backoff, after a fresh digest check. Events that arrive in the meantime are
  dropped and counted, as a full queue drops them today. A changed digest
  leaves the plugin stopped, with a diagnostic. Unlike the driver, nothing
  needs settling: P depends on no effect of an event call.
- **Stop.** The daemon closes the socket, waits briefly, then kills the
  process group.

### Packages and selection

`p.plugin/v2` keeps v1's flat directory, digest rule and strict parsing. The
manifest keeps `schema`, `id`, `version`, `description` and `capability`,
plus `entry` for an executable or `assets` for a package of files. `api`,
`placement`, `runtime` and `requests` go, because the capability and the
hello now carry what they said. The limits of 8 MiB per file and 16 MiB per
package were sized for WASI modules; step 9 resizes them for native
executables.

Host configuration selects each plugin in its role's block, in the driver's
shape `{path, sha256, config}`:

```json
"events": {"path": "/absolute/share/p/plugins/file-log", "sha256": "<package digest>",
           "config": {"path": "/absolute/private/events.ndjson", "max_bytes": 4194304}}
```

`runtime.agent_adapter` takes the same shape without `config`.
`p.activation/v1` goes: without grants, it would repeat host configuration in
a second private file.

### Commands

| Command | After |
|---|---|
| `p plugins list`, `p plugins conformance` | Unchanged: validate packages and print digests; never run code |
| `p plugins defaults` | Prints the host-configuration blocks for the bundled file-log and Codex adapter; the NixOS `bundledActivation` option writes them into the host configuration it generates |
| `p plugins install`, `update`, `remove` | Unchanged: offline, an exact approved digest, `STATE/plugins/packages/SHA256`. Removal is refused while host configuration selects the package or a session's creation pins it; only the agent adapter is pinned now |
| `p plugins run-event PACKAGE CONFIG EVENT` | Starts the handler once, sends one event and prints the result; replaces `emit` and today's `run-event` |
| `p plugins activate`, `p plugins plan-assets` | Gone, with the activation file and with core-chosen guest paths |

### Conformance

- **Static:** `p plugins conformance`, as today.
- **Behavioral:** a conformance suite for `event-handler`, like the driver's,
  run against the bundled `p-event-file` and the separately built example:
  the hello and its refusals (version, capability, config); start refused for
  a changed digest; in-order delivery; the deadline; a crash, a restart and
  the counted drops; malformed or oversized replies refused; output bounded
  and kept from clients.
- VM scenario 03, which today proves that a separately built WASI handler runs
  without rebuilding P, proves the same with the native example.

### A future package cache

[Commit images](commit-images.md) leave a package cache for later, possibly
as a plugin. Here it would be a new executable capability, started and
supervised like any other. Two things would need design then. Builders reach
only public destinations, so the cache needs a way in; an endpoint socket the
driver mounts into the builder, as sessions get `session.sock` and `git.sock`,
would avoid a network exception. And every image would be built from what it
serves, which makes it as trusted as the driver. Nothing here is built for it.

## What PROJECT.md's plugin goals mean now

"Bound delegated authority" has the success signal "Agents and plugins cannot
exceed their declared grants, failures do not silently widen authority, and
destructive actions explain what will be lost before proceeding."

| Who | What still contains it | What no longer does |
|---|---|---|
| Agents | The session runtime and the session socket's narrow methods, unchanged | — |
| The Codex adapter | The session it runs in: it has the session's authority and no more | — |
| Executable plugins | Selection only by trusted host configuration; the pinned digest; P sends only reduced, redacted events and the plugin's config; no reply changes P's state, and failures are diagnostic | The OS. The process can read and write P's state directory (SQLite, Git repositories, the Git server and client keys), open the Incus user socket as a member of `incus`, and use the network. WASI prevented all of it |

So "cannot exceed their declared grants" stops holding for executable
plugins: no grant is narrower than P's account. "Failures do not silently
widen authority" still holds for everything P itself does. The same change
reaches the extension goal's "operate autonomously within those grants", the
project model's "an activated plugin may operate autonomously only within
them", and PRODUCT.md's "agent-authored plugins remain within user-approved
capabilities": for an executable plugin, the owner's approval is all or
nothing. PRODUCT.md names this risk: "Agent-authored code could undermine the
security promise if isolation and capability enforcement are superficial."

This is a departure for the maintainer to confirm (open question 2). The
runtime driver already makes the same trade for one component: "The driver is
trusted code: it holds container authority."

## What users notice

- A default installation writes the same events file, installs the Codex
  adapter at the same path, and runs Git with the same behavior and limits.
- Host configuration: `git` keeps only `listen`; `runtime.host_plugin_id`
  goes; `events` and `runtime.agent_adapter` take a path, a digest and config
  instead of an activation file and a plugin ID.
- `p plugins defaults` covers two packages; `activate`, `plan-assets` and
  `emit` go; `run-event` takes a package.
- A WASI event handler no longer runs; it must be rebuilt as a native
  executable.

## Existing installations

None are supported: P targets fresh installs, by the maintainer's decision of
2026-11-02. An activation file is invalid, and creation evidence that pins
source or host packages is not read.

## What is removed

- In internal/plugin: the WASI runner and brokers (wasi.go, wasi_sandbox.go,
  sourcegit.go) and activation.go. runtime.go and environment.go go earlier,
  in steps 8 and 1. filelog.go's append code moves to `p-event-file`; event
  validation stays in core.
- `github.com/tetratelabs/wazero`, which only internal/plugin imports, with
  its row in the technology stack's licensing table and its "WASI runner
  decision".
- The schemas `p.command/v1`, `p.command-result/v1`, `p.broker/v1`,
  `p.asset-plan/v1` and `p.activation/v1`; the `p_broker_v1.call` import;
  grants and their exact-match check.
- plugins/bundled/source-git and tmux-host as packages; the WASI example; the
  WASI fixtures in internal/plugin/testdata (git-alternate, git-hostile,
  git-repeat, wasi-hostile, wasi-misbehave); the `wasip1` builds in
  dev/plugin-packages.nix and dev/integration.nix.
- The source and host fields of `CreationSelection`, and gitservice's package
  recheck.

## Departures from agreements

- [Plugin contract](../docs/plugin-contract.md): "Packages never receive an
  Incus administrative socket, a Git write credential, host project
  configuration, or unrestricted filesystem or network access" holds for an
  executable plugin only as far as P's account is limited: it reaches the
  Git keys, the Incus user socket and the network. "WASI is the selected
  executable boundary … Merely running a native executable in a separate
  process does not meet this contract. No runtime fallback to an
  unconstrained process is allowed" is reversed. The
  capability table shrinks to `event-handler` and `agent-adapter`, with no
  grants; the `declarative` and `wasi-command` kinds, the WASI event-handler
  and source-Git sections, and trusted activation go; asset roles keep only
  the agent adapter, and their destinations become the driver's.
- [PROJECT.md](../PROJECT.md): the signals of "Bound delegated authority" and
  "Make extension a product capability", the project model's plugin sentence,
  and the open direction's "basic working composition of secure first-party
  implementations", which becomes the Incus driver, file-log and the Codex
  adapter.
- [Product direction](../docs/PRODUCT.md): the lines quoted above, and
  "first-party implementations can be replaced", which no longer covers Git
  source.
- [MVP status](../docs/mvp-status.md): the composition that names the tmux
  host and Git source as plugins.
- [Technology stack](../docs/technology-stack.md): "Public plugin contract";
  "Systemd interactive host", including "Product direction requires tmux
  support to be a bundled internal-session-service plugin"; "Event handlers";
  wazero.
- [Control API](../docs/control-api.md): the `git`, `events` and
  `runtime.agent_adapter` configuration and `host_plugin_id`.
- [Glossary](../GLOSSARY.md): "Plugin" (no runtime provider; "within them"),
  "Grant" (plugins hold none), "Host-side capability" (examples).
- [Session observability](../docs/session-observability.md) and
  [communication boundaries](../docs/communication-boundaries.md): event
  delivery goes to a handler process. What crosses the channel is unchanged,
  and the event-handler row describes what P sends, not what a handler can
  reach.

## Effects on other rework documents

- [runtime-driver.md](runtime-driver.md): "`p plugins defaults` lists four
  packages instead of six" becomes two; "tmux from the interactive-host
  package" becomes the driver's own files; `p-git-ssh` joins them;
  `Session.Assets` carries only the agent adapter. "Repositories, requests
  and plugins never reach it" holds for the driver's socket pair, but an
  executable plugin running as P's account can open the Incus user socket
  directly. Whether a session's creation pins the driver's digest, as it pins
  the runtime package today, is not yet said there.
- [commit-images.md](commit-images.md): "possibly as a plugin" fits the
  package-cache section above.
- [wire-types.md](wire-types.md): its open question 1 is answered above; the
  session RPC keeps `v`.
- [REWORK-PLAN.md](REWORK-PLAN.md): the Plugins row and step 9 expect the
  WASI plugins to become JSON-RPC executables. Here source-git folds into
  core instead, and the one executable plugin is the event handler, which
  was declarative rather than WASI.

## Options set aside

| Option | Why not |
|---|---|
| Keep WASI for event handlers | A WASI handler can only append or skip; with no sockets it cannot notify anything. |
| source-git as a native executable with today's nine methods | It decides nothing and would gain write access to every repository (open question 1). |
| A source driver covering all Git work, as the runtime driver covers runtimes | Large, and P has no second source system to plug in: Git owns source. |
| Session hosts as a package the Incus driver loads | They are bound to runtime-kit and P's base image, and a host contract would tie every driver to systemd. |
| Keep `p.activation/v1` as a separate file | Without grants it repeats host configuration. |
| One process per event, as with WASI | A start per event, and no connection a handler can keep, for example to a webhook. |
| Execute from memory (`memfd_create` and `fexecve`) to close the gap between check and start | The trusted-ancestor rule leaves only root and P's account able to swap the file, and both can do more than that already. |
| OS containment of executable plugins now | Open question 2. |
| hashicorp/go-plugin | Set aside for the driver: MPL-2.0 and gRPC. |

## Open questions

1. **Does source-git fold into core?**
   - (a) Fold it into `gitservice`. *Recommended:* the module decides nothing
     core does not check again, most Git work already bypasses it, and
     nothing else would gain write access to P's repositories.
   - (b) A native executable with today's nine methods. It stays replaceable
     without rebuilding P, but a replacement can change only page sizes and
     limits.
   - (c) A source driver for all Git work. Worth it only for a second source
     system, which P's model does not plan.
2. **Are executable plugins trusted code?** This is PROJECT.md's departure.
   - (a) Yes: selection and the digest are what contains them. PROJECT.md,
     PRODUCT.md and the glossary then say what P enforces: agents and the
     Codex adapter cannot exceed their grants; an executable plugin runs only
     when trusted configuration selects its exact digest, and P gives it
     only what its capability defines. *Recommended* for step 9: the only executable plugin
     left is an event handler, and a sandbox can later wrap the process start
     without changing the protocol. Revisit when an executable plugin from
     outside P is wanted.
   - (b) Contain executable plugins at the OS level before step 9 lands, and
     keep the signal. Nothing is designed: under `NoNewPrivileges` the daemon
     cannot switch accounts itself, and whether Landlock on P's kernels can
     deny connecting to a pathname Unix socket such as Incus's was not
     verified.
3. **Does the bundled file-log stay a plugin?**
   - (a) A small executable, `p-event-file`. *Recommended:* a default
     installation then runs the protocol other handlers use, which is how
     PRODUCT.md asks first-party functionality to prove its contracts, and
     scenario 48 checks it. The cost is one process and one more binary.
   - (b) Core writes the file from host configuration, and the executable
     protocol serves only other handlers. Nothing in a default installation
     would exercise it.
4. **Is the runtime driver a package too?**
   - (a) Yes: the `driver` block's `path` names a package directory, and
     `sha256` is its package digest. The driver gets a manifest with the
     capability `runtime-driver`, and `conformance`, `install`, `update`,
     `remove` and `defaults` serve it as they serve plugins. *Recommended:*
     one pin rule and one installation path.
   - (b) No: the driver stays a single executable pinned by its file digest,
     beside packages pinned by directory digest.

## How it will be checked

- **Unit tests:** gitservice's nine operations without a plugin, through the
  existing gitservice and source-git tests minus the module-specific ones
  (alternate page sizes, hostile modules), with the same refusals and limits;
  the `event-handler` conformance suite; host configuration rejects the old
  activation fields; the `p plugins defaults` output loads as host
  configuration; removal is refused while a package is selected or pinned.
- **VM scenarios:** 01 and 13 (file-log through the CLI and the daemon) with
  the executable; 03 with the native example; 04, 07, 08, 14 and 16 to 20 (Git
  and origin) with unchanged outcomes and no source package; 27 (Codex
  adapter) unchanged; 48 (bundled distribution) with two packages; 54 (plugin
  management); 55 (NixOS service) with the selections in host configuration.
  New: kill the handler while the daemon runs, see later events delivered
  after the restart and the drop logged; a changed package digest refuses
  start.
- **Boundaries:** go.mod has no wazero, and no `wasip1` build remains.
