# P — public plugin contract

This document owns plugin packaging, discovery, selection, the launch checks,
the event-handler protocol, the agent adapter's files, and the minimum
conformance workflow. [Technology stack](technology-stack.md) owns
implementation dependencies and capability behavior. [Product
direction](PRODUCT.md) owns the reason for the extension model. This contract
is **v2**: the `p.plugin/v2` package, selection by path and digest in trusted
host configuration, the executable `event-handler` capability with its bundled
file-log, and the `agent-adapter` package of files are implemented. P rejects
an unimplemented capability rather than running it.

## Authority boundary

P core owns instance/project/session identity, policy snapshots, grants,
confirmation, durable operation intent, reconciliation, and recovery. A
package never decides whether a project, session, ref, endpoint, host path, or
destructive effect is authorized, and nothing a plugin answers changes P's
state.

There are two kinds of plugin, and they are contained differently:

| Kind | Capability | Who runs it | What contains it |
|---|---|---|---|
| Executable | `event-handler` | The daemon, as one long-lived process | Trusted selection, the launch checks and the package digest; P sends it only reduced, redacted events and its own config |
| Files | `agent-adapter` | The session, after the runtime driver installs the files | The session it runs in, and its narrow session socket |

**An executable plugin is trusted code.** It runs as P's account inside the
daemon's service sandbox, with no sandbox of its own: it can read and write
P's state directory, open the Incus user socket and use the network, as P can.
Only trusted host configuration selects one, by its exact package; the launch
checks make sure only root or P's account can change the files it runs
from. Running a plugin in a separate process does not by itself limit what it
can do. Confining executable plugins at the operating-system level is a later
improvement, recorded as debt
([state-safety risks](debts/state-safety.md#executable-plugins-run-with-ps-authority)).

Registration and discovery are read-only. Agents and developers can create and
test packages, including outside this repository, without rebuilding P. Only
trusted host configuration selects a package for a capability; repositories,
requests, sessions and agents never do. The bundled packages follow the same
manifest, digest, selection and launch checks as third-party packages.

## Package `p.plugin/v2`

A package is a directory of regular files (no nested directories) with
`plugin.json` plus its entry or its assets. `p plugins conformance
/absolute/package/path` checks the package and prints its content SHA-256. The
digest covers every regular file's relative name encoded as UTF-8, a NUL byte,
an unsigned 8-byte big-endian file size, and exactly that many file bytes in
sorted filename order; it covers no mode or owner. Symlinks, special files,
oversize files, unsafe relative paths and undeclared files fail. A package
holds at most 128 files; each file is limited to 32 MiB, the manifest to
64 KiB, and the whole package to 64 MiB, which admits a native executable.

The manifest has `schema` (`p.plugin/v2`), a reverse-domain `id`, a semantic
`version` with three numeric components, a `description` of 1 to 512
characters and a `capability`, plus `entry` for an executable capability or
`assets` for a package of files. Unknown fields fail: a manifest of an earlier
schema, with `api`, `placement`, `runtime` or `requests`, is refused. Version
alone never selects an upgrade; a changed package requires a new approved
digest.

| Capability | Kind | Manifest names | Required files |
|---|---|---|---|
| `event-handler` | Executable | `entry`: the program the daemon starts | The entry, a regular file of the package |
| `agent-adapter` | Files | `assets`: exactly `["p-codex-adapter"]` | `p-codex-adapter` |

There are no grants: the capability defines what P sends and accepts. Each
capability is selected at most once per instance.

## Selection

Trusted host configuration selects each plugin in its role's block, in the
drivers' shape: the package directory's absolute clean `path`, its package
digest as `sha256`, and, for an executable plugin, its own `config`, which only
the plugin reads ([control API](control-api.md#daemon-configuration-and-command)):

```json
"events": {"path": "/absolute/share/p/plugins/file-log", "sha256": "<package digest>",
           "config": {"path": "/absolute/private/events.ndjson", "max_bytes": 4194304}}
```

`runtime.agent_adapter` takes `path` and `sha256` and no config. There is no
activation file: an `activation_path` or `plugin_id` in host configuration is
an unknown field and refuses it.

## Launch checks

The daemon starts an executable plugin's entry by name, so before every start,
and before it hashes anything, it requires:

- the package directory and each of its ancestors: a directory, not a
  symbolic link, owned by root or P's account and writable by neither group nor
  others, unless root owns it and it is sticky (as `/tmp` or `/nix/store`);
- every file of the package: regular, owned by root or P's account, writable
  by neither group nor others, and with one link, unless root owns it and no
  one may write it (the Nix store hard-links identical read-only files), so no
  writable alias elsewhere shares its bytes;
- the entry: its owner's execute bit.

It then hashes the package from the files it checked, compares the digest with
the selection, and starts the entry it checked, by its open descriptor. Between
check and start only root or P's account can change the bytes, and both
already hold more than a plugin gains. The bundled catalog in the Nix store
meets these rules as root-owned, read-only files, and a staged package meets
them by construction ([below](#managed-package-commands)). The runtime driver
and the source driver apply the same rules to their executables
([runtime isolation](runtime-isolation.md#driver-executable)).

The agent adapter is never started by P: core reads its files once, from the
bytes it hashed, and refuses a package whose digest differs from the
selection.

## The event-handler protocol

`event-handler` is the one executable capability. The daemon starts the
selected handler at its start, after the launch checks, and talks to it over
the drivers' protocol: JSON-RPC 2.0 over a Unix socket pair, one
newline-terminated JSON frame per message, decoded strictly, each frame at most
16 MiB.

- **Start.** The entry runs with no arguments but its own path, no inherited
  environment, `/dev/null` as input, its connection on descriptor 3, and its
  standard output and error going to the daemon's own standard error, its log,
  never to API clients. It runs in a process group of its own. A handler that
  does not start refuses the daemon's start.
- **Hello.** P sends the protocol version, `p.event-handler/v1`, the handler's
  config and the capability, and the handler answers with the version it
  agreed and its plugin ID and version, which must be its package's:

  ```json
  {"jsonrpc":"2.0","id":1,"method":"hello","params":{"protocol":"p.event-handler/v1","config":{"path":"/private/events.ndjson","max_bytes":4194304},"core":{"capability":"event-handler"}}}
  {"jsonrpc":"2.0","id":1,"result":{"protocol":"p.event-handler/v1","capabilities":{"id":"org.p.filelog","version":"0.2.0"}}}
  ```

  A handler that speaks another version, refuses its config or answers as
  another package does not start. Nothing is sent before the hello.
- **Events.** P sends `event.handle` with one `p.event/v1` envelope as its
  parameters, and the handler answers `{"status":"handled"}` or
  `{"status":"skipped"}`. Any other answer is refused. The handler calls
  nothing back: `event-handler` defines no method of P's.

  ```json
  {"jsonrpc":"2.0","id":2,"method":"event.handle","params":{"schema":"p.event/v1","id":"e-1","kind":"session.condition_changed","occurred_at":"2026-09-23T00:00:00Z","instance":"p-one","fields":{"condition":"ready"}}}
  {"jsonrpc":"2.0","id":2,"result":{"status":"handled"}}
  ```

Core reduces and validates each event before it sends it: the bounded
`p.event/v1` envelope, at most 4 KiB, with one typed field per supported event
kind and enumerated condition values; event reduction and secret stripping are
core's. Events go one at a time, in order, from a queue of 64, each with a
2-second deadline. P never waits on a handler for a decision, and a handler's
failure is diagnostic: it never reverses a committed state change.

- **Crash or missed deadline.** A handler that exits, drops its connection or
  misses an event's deadline is killed with its process group and started
  again with a bounded backoff, after fresh launch checks. Events that arrive
  while it is down are dropped and counted, as a full queue drops them, and
  the drop is logged. Nothing needs settling: P depends on no effect of an
  event call.
- **Changed package.** A restart whose launch checks fail leaves the handler
  stopped until the daemon restarts, with a diagnostic; later events are
  dropped.
- **Stop.** The daemon closes the connection, waits up to five seconds for the
  handler to exit, then kills its process group.

The protocol is built on the drivers' (internal/driverwire); errors keep their
kinds across the connection as the drivers' do.

### The bundled file-log

`p-event-file`, the entry of the bundled `org.p.filelog` package, appends each
event it is sent as one JSON line to a private host file. Its config is
`{"path": "/private/events.ndjson", "max_bytes": 10485760}`: an absolute clean
path and a rotation size from 1 KiB to 100 MiB. The path comes only from
trusted configuration. The log directory must already exist, be owned by P's
effective user, and be private (`0700`); no path component may be a symlink.
Writable ancestors are rejected except a root-owned sticky directory such as
`/tmp`. The handler opens the final file relative to its checked directory
descriptor, rejects hard links and foreign ownership, uses mode `0600`, and
retains at most one prior segment when rotating. It handles every event.

### The example filter

The inspectable source at `plugins/examples/filter-log/main.go` is a native
handler built apart from P: it appends only the events that say a session
became ready, to its own configured file, and skips the rest. Build it with
`go build -o filter-log ./plugins/examples/filter-log`, place `filter-log`
beside its `plugin.json` in a package directory of its own, run `p plugins
conformance` to print its digest, select it in a host configuration's
`events` block, and try it with `p plugins run-event`.

### Conformance

`p plugins conformance` checks a package statically. The behavioral suite
(internal/eventhandler/conformance) runs a handler as the daemon starts it:
its hello and the refusals of another version, another capability, an event
before the hello and a config it does not take; the launch checks; events in
order; a missed deadline; and a crash, its restart and the event refused while
the handler is down. It runs against the bundled `p-event-file` and the
example filter.

## The agent adapter

An `agent-adapter` package holds exactly `p-codex-adapter`. The name is a
role; a package cannot choose host or session destinations. At daemon start
core reads the selected package, refuses it unless its digest matches the
selection, and holds the verified file: 1 byte to 1 MiB, with mode `0555`.
Core hands it to the runtime driver in each new session's `Session.Assets`,
and the driver decides where the role goes; the Incus driver installs it
root-owned at `/usr/libexec/p/codex-adapter` when it assembles a stopped
session. A session's creation pins the adapter's ID and digest. The adapter
runs inside the session, where Codex's hooks call it, and reports to the
session socket like any process of the session.

## Discovery and diagnostics

`p plugins list /absolute/catalog/path` scans immediate package directories
and reports valid packages and rejections. It selects nothing.
The NixOS distribution ships the two first-party packages under
`share/p/plugins` beside its `bin/p`: the file-log handler, whose entry
`p-event-file` is built with P, and the Codex adapter, each with only its
declared files. Incus support is P's first-party runtime driver, the
`p-runtime-incus` executable beside `bin/p`, not a package: host configuration
pins it by path and SHA-256 as `runtime.driver`
([control API](control-api.md)), and the daemon applies the launch checks
before it starts it
([runtime isolation](runtime-isolation.md#driver-executable)). The driver holds
the tmux session host, its systemd units and attach program, as its own files.
Git source is likewise P's first-party source driver, the `p-source-git`
executable beside `bin/p`, pinned as `git.driver` and checked the same way
before every start.
`p version` reports the P and Go versions, the control protocol
(`control_protocol`, such as `p.control/v2`), the package schema
(`plugin_schema`, `p.plugin/v2`), the event-handler protocol
(`event_handler_protocol`) and the linked module versions as JSON.

`p plugins defaults /absolute/private/events.ndjson [absolute-catalog-path]`
prints the host configuration blocks that select the bundled packages: an
`events` block for the file-log, writing that event log with a 4 MiB rotation
limit, and a `runtime` block with `agent_adapter` for the Codex adapter, each
with its package's path and digest. Without a catalog override it uses the
installed catalog relative to the resolved executable. It validates the
packages but reads no project configuration, writes no files and starts no
code. The host owner reviews the blocks and puts them in host configuration;
the NixOS module's `bundledActivation` option writes them into the host
configuration it generates, unless settings select their own.

`p plugins run-event HOST_JSON EVENT_JSON` starts the event handler that the
host configuration's `events` block selects, with its digest and config, after
the launch checks; sends it the event; prints `{"status": …, "event_id": …}`;
and stops it. A private host configuration holding only `state_dir` and
`events` is enough to try a package. It is a diagnostic command, not a daemon
or lifecycle API.

### Managed package commands

The CLI-first manager operates offline, using a private trusted host configuration
and the instance's exclusive store lock. Stop that daemon before management;
ordinary sessions are not automatically stopped or discarded. Commands are:

```sh
p plugins install HOST_JSON PACKAGE APPROVED_SHA256
p plugins update HOST_JSON OLD_SHA256 PACKAGE APPROVED_SHA256
p plugins remove HOST_JSON SHA256
```

Obtain and review the manifest and digest with `p plugins conformance PACKAGE`
before approving it. Install retains the exact opened validated bytes in
`STATE/plugins/packages/SHA256` and publishes them atomically, owned by P's
account, each file with one link: the entry `0500`, every other file `0400`
and the package directory `0500`, which meets the launch checks. It prints the
package and its selection, `path` and `sha256`. Update stages a new approved
digest with the same plugin identity and retains the old version. Neither
command selects it: explicitly edit the private host configuration's role
block, supply the capability's config, and restart the daemon.

The protected registry belongs to one persistent instance and state directory;
another instance cannot select its staged packages. The daemon leases the
packages its host configuration selects throughout its lifetime, and each run
of `p plugins run-event` or read of the agent adapter also takes a lease and
honors disable markers.

Removal is refused while host configuration selects the package or a
session's creation pins it; only the agent adapter is pinned. Existing
session/operation evidence, unresolved provenance or a live package lease
refuse removal; unfinished origin work uses no package. Indexed
commit images do not refuse removal: no package builds them. Finish
supported lifecycle cleanup or recovery first; unknown holders require
investigation and preserve the package. The manager rechecks
the selection and dependencies under the exclusive package lease, syncs a
disable marker and exact cleanup receipt before deleting bytes. Interrupted
removal permits Retry for missing approved files and unchanged owned survivors;
changed identities, bytes or unfamiliar survivors refuse without recursive
deletion. Missing registry metadata remains a managed-path error and cannot
rebind retained packages to another instance. The disabled digest cannot
silently reinstall or be selected again; a new
approved digest is required. Already installed session assets and credentials
remain under their session lifecycle authority and are not deleted by package
management.
