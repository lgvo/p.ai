# P — public plugin contract

This document owns plugin packaging, discovery, compatibility, execution
containment, trusted activation, grants, and the minimum conformance workflow.
[Technology stack](technology-stack.md) owns implementation dependencies and
capability behavior. [Product direction](PRODUCT.md) owns the reason for the
extension model. This contract is **v1 foundation**: package/activation schemas,
the file-log operation, bounded WASI event commands, and
trusted stopped-session asset assembly are implemented. Capability-specific
support claims remain subject to their recorded validation gates. P rejects an unimplemented method rather
than executing it with ambient authority.

## Authority boundary

P core owns instance/project/session identity, policy snapshots, grants,
confirmation, durable operation intent, reconciliation, and recovery. A package
may propose capability work, but it never decides whether a project, session,
ref, endpoint, host path, or destructive effect is authorized. The broker
validates each operation against the trusted activation and the current core
operation context. Packages never receive an Incus administrative socket, a
Git write credential, host project configuration, or unrestricted filesystem
or network access.

Registration/discovery is read-only. Agents and developers can create and test
packages, including outside this repository, without rebuilding P. Only
trusted developer configuration activates a selected package and exact grant
set. Repository contents cannot select or activate packages. The bundled
defaults follow the same manifest, digest, activation, and broker checks as
third-party packages; ordinary setup will supply their trusted selections.

## Package `p.plugin/v1`

A package is a directory of regular files (no nested directories) with
`plugin.json` plus any declared assets or a WebAssembly entry.
`p plugins conformance /absolute/package/path` checks the
schema and prints the content SHA-256. The digest covers every regular file's
relative name encoded as UTF-8, a NUL byte, an unsigned 8-byte big-endian
file size, and exactly that many file bytes in sorted filename order. Symlinks,
special files, oversize files, unsafe relative paths, undeclared files, and
undeclared runtime kinds fail. Each file is limited to 8 MiB, the manifest to
64 KiB, and the full package to 16 MiB. An
installation process must stage the exact validated bytes in a protected,
content-addressed location before activation; until that process exists,
activation must revalidate the package digest on each invocation boundary.

Required manifest fields are `schema`, reverse-domain `id`, semantic
`version`, `api`, `capability`, `placement`, `description`, `runtime`, and
`requests`. Optional `assets` names files in the package. Unknown fields fail.
`api` is `1.0`: a different API version fails closed. Package versions use
three numeric components; version alone never selects an upgrade. A changed
package requires a new approved digest.

The v1 capability and placement pairs are:

| Capability | Placement | Declared broker grant |
|---|---|---|
| `event-handler` | `host` | `event.file.append` |
| `agent-adapter` | `internal-session` | `agent.status.report` |

Each capability is selected at most once per instance for MVP. The grant
names are typed broker verbs, not operating-system privileges or permission to
run arbitrary host commands. A package can request only grants
assigned to its capability. Trusted activation grants must match the package's
requests exactly; adding a grant requires manifest and trusted-config review.

`runtime.kind` is one of `declarative`, `wasi-command`, or `assets`. The only
current declarative operation is `event.file.append`; it is available only to
an `event-handler`. `wasi-command` names a WebAssembly entry in the package;
its implemented method is the event handler's, described below. Asset packages
carry the agent adapter's fixed file. Core validates activation and the typed
asset plan, then hands the file to the runtime driver, which installs it in an
owned, stopped session. The session host's systemd units and attach program
are no package: they are the Incus driver's own files
([runtime isolation](runtime-isolation.md#attachment)).

WASI is the selected executable boundary. The runner must instantiate a
WebAssembly module with **no preopened directories, inherited environment,
network sockets, host credentials, or direct host command execution**. It must
bound memory, execution time, input/output, and outstanding broker requests.
Its only host effects are versioned typed broker calls, each reauthorized by
core. Merely running a native executable in a separate process does not meet
this contract. No runtime fallback to an unconstrained process is allowed.
The capability method envelopes and broker argument schemas for the remaining
bundled capabilities will be fixed before linking each implementation.

### WASI event-handler ABI

The module is a WASI Preview 1 command with a `_start` entry. It receives one
JSON object and newline on stdin. For example, its `p.command/v1` request is:

```json
{"schema":"p.command/v1","kind":"event.handle","event":{"schema":"p.event/v1","id":"e-1","kind":"session.condition_changed","occurred_at":"2026-09-23T00:00:00Z","instance":"p-one","fields":{"condition":"ready"}}}
```

The event is already reduced and validated by core. The module writes exactly
one JSON `p.command-result/v1` object to stdout with `status` `appended` or
`skipped` and optional `message` up to 256 bytes. `appended` requires one
successful broker append; `skipped` requires no broker call. Unknown fields,
malformed JSON, failed exit, and inconsistent status fail the invocation. A
valid filtered result is:

```json
{"schema":"p.command-result/v1","status":"skipped"}
```

The sole v1 host import is `p_broker_v1.call`, with WebAssembly signature
`(i32 request_ptr, i32 request_len, i32 reply_ptr, i32 reply_capacity) -> i32`.
Pointers and lengths address module linear memory. It returns the reply byte
count or `-1` on refusal. The command must export its linear memory as
`memory`, as required by the WASI command ABI; a missing export is refused.
The request is exactly
`{"schema":"p.broker/v1","method":"event.file.append"}`; success replies
with `{"schema":"p.broker/v1","status":"ok"}`. The method has no
guest-supplied arguments: core appends its own validated event to the path in
trusted activation config. Unknown methods, extra fields, invalid memory
ranges, and repeated calls fail invocation. This is an operation-scoped grant
check, not permission to read or choose a host file.

The 2-second invocation deadline includes queueing, digest checking, and
execution. At most four commands run concurrently. Guest linear memory is
limited to 16 MiB and the module entry to 8 MiB. Stdin, stdout, stderr, and
broker request/reply are each limited to 4 KiB. No directories are preopened,
no environment or argv is inherited, and no network or host command import is
provided. Plugin stderr is bounded and never returned as a host diagnostic.
The executed module bytes are captured while the package digest is computed
and compared with trusted activation. Broker append is a diagnostic side
effect, not a transaction with the command result.

The inspectable source at `plugins/examples/filter-log/main.go` builds with
`GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go build -o filter.wasm
./plugins/examples/filter-log`. Place `filter.wasm` beside its `plugin.json` in
a flat package, run `p plugins conformance`, select that digest in trusted
activation, then use `p plugins run-event` for a structured diagnostic. The
example appends only ready-condition events.

### Session asset plans

An `agent-adapter` asset package must contain exactly `p-codex-adapter`. The
name is a role; a package cannot choose host or session destinations.
`p plugins plan-assets <activation.json> <plugin-id>` emits a
`p.asset-plan/v1` summary with the package digest and per-file role,
destination, mode, and SHA-256. Core holds verified bytes in the in-memory
plan for the runtime driver:

| Role | Fixed session destination | Mode |
|---|---|---|
| `p-codex-adapter` | `/usr/libexec/p/codex-adapter` | `0555` |

Plans are limited to `internal-session` placement. Each asset must contain
1 byte to 1 MiB. A changed package, wrong capability, missing or extra role,
or wrong grant is rejected. Planning does not install files: core hands the
planned file to the runtime driver, which installs it root-owned at its
destination when it assembles a stopped session.

## Discovery and trusted activation

`p plugins list /absolute/catalog/path` scans immediate package directories
and reports valid packages and rejections. It does not activate anything.
The NixOS distribution ships the two first-party packages under
`share/p/plugins` beside its `bin/p`: the file-log handler and the Codex
adapter, each with only its declared files. Incus support is P's first-party
runtime driver, the `p-runtime-incus` executable beside `bin/p`, not a package:
host configuration pins it by path and SHA-256 as `runtime.driver`
([control API](control-api.md)), and the daemon checks the file's ownership,
mode and links before it starts it
([runtime isolation](runtime-isolation.md#driver-executable)). The driver holds
the tmux session host, its systemd units and attach program, as its own files.
Git source is P's built-in source driver, not a package.
`p version` reports the P and Go versions, the control protocol
(`control_protocol`, such as `p.control/v2`), the plugin API version and the
linked module versions as JSON.

`p plugins defaults /absolute/private/events.ndjson [absolute-catalog-path]`
returns a `p.activation/v1` selection with each validated package's digest and
exact grant. Without a catalog override it uses the installed catalog relative
to the resolved executable. It reads no project configuration, writes no files,
executes no plugin and activates no authority. The explicit event-log path
uses a 4 MiB rotation limit. The host owner reviews and saves this output in
a private trusted activation file before selecting its roles in host
configuration. Package digests are still rechecked on activation and invocation.
Trusted activation is a host-owned JSON file with schema `p.activation/v1` and
a `plugins` array. Each selection contains `id`, absolute `path`, exact
`sha256`, `grants`, and capability-specific `config`. The file must be regular
and not group/world writable. P rejects duplicate plugin IDs, duplicate
capabilities, ID/digest mismatches, missing grants, extra grants, unsupported
runtimes, and invalid config. This file must reside outside session and project
write boundaries; installation and daemon startup must verify its ownership
and path ancestry before using it as a persistent authority source.

The implemented file-log config is `{ "path": "/private/events.ndjson",
"max_bytes": 10485760 }`. The path comes only from trusted configuration.
The log directory must already exist, be owned by P's effective user, and be
private (`0700`); no path component may be a symlink. Writable ancestors are
rejected except a root-owned sticky directory such as `/tmp`. The broker opens
the final file relative to its checked directory descriptor, rejects hard
links and foreign ownership, uses mode `0600`, and retains at most one prior
segment when rotating. The handler accepts only the bounded, reduced
`p.event/v1` envelope, with one typed field per supported event kind and
enumerated condition values; event reduction and secret stripping remain core
responsibilities.
Handler failure is diagnostic and never reverses a committed state change.

For example, after `p plugins conformance plugins/bundled/file-log` prints a
digest, the trusted selection is:

```json
{
  "schema": "p.activation/v1",
  "plugins": [{
    "id": "org.p.filelog",
    "path": "/absolute/path/to/plugins/bundled/file-log",
    "sha256": "<digest printed by conformance>",
    "grants": ["event.file.append"],
    "config": {"path": "/private/events.ndjson", "max_bytes": 10485760}
  }]
}
```

`p plugins activate /absolute/trusted-activation.json` validates and prints a
redacted activation summary. `p plugins emit <activation.json> <event.json>`
and `p plugins run-event <activation.json> <event.json>` exercise the selected
event handler; they are diagnostic commands, not daemon or lifecycle APIs.
Daemon delivery selects an event handler by trusted host configuration and
rechecks its package digest for each invocation. Handler calls are bounded and
best effort; a full queue or failed call loses that event without rolling back
the committed transition. Plugin management remains separate from the read-only
bundled selection command. Update requires staging a new digest and explicit
trusted selection; removal disables new calls before retiring assets or
credentials. A plugin failure returns a bounded diagnostic to core and
never changes identity, policy, or committed lifecycle state.

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
`STATE/plugins/packages/SHA256`, publishes them atomically, and prints the
package and selection values. Update stages a new approved digest with the same
plugin identity and retains the old version. Neither command activates it:
explicitly edit the private trusted activation and host role selection, supply
the capability's required configuration, validate, and restart the daemon.

The protected registry belongs to one persistent instance and state directory;
another instance cannot select its staged packages. The daemon holds shared
package leases throughout its lifetime, including cached asset plans, and
invocation snapshots also acquire leases and honor disable markers.

Removal requires trusted deselection and no durable dependency. Existing
session/operation evidence, unresolved provenance or a live package lease
refuse removal; unfinished origin work uses no package. Indexed
commit images do not refuse removal: no package builds them. Finish
supported lifecycle cleanup or recovery first; unknown holders require
investigation and preserve the package. The manager rechecks
trusted selection and dependencies under the exclusive package lease, syncs a
disable marker and exact cleanup receipt before deleting bytes. Interrupted
removal permits Retry for missing approved files and unchanged owned survivors;
changed identities, bytes or unfamiliar survivors refuse without recursive
deletion. Missing registry metadata remains a managed-path error and cannot
rebind retained packages to another instance. The disabled digest cannot
silently reinstall or reactivate; a new
approved digest is required. Already installed session assets and credentials
remain under their session lifecycle authority and are not deleted by package
management.
