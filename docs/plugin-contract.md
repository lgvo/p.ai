# P — public plugin contract

This document owns plugin packaging, discovery, compatibility, execution
containment, trusted activation, grants, and the minimum conformance workflow.
[Technology stack](technology-stack.md) owns implementation dependencies and
capability behavior. [Product direction](PRODUCT.md) owns the reason for the
extension model. This contract is **v1 foundation**: package/activation schemas,
the file-log operation, bounded WASI event, source-Git, runtime, and closed environment commands,
and trusted stopped-session asset assembly are implemented. Capability-specific
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
| `runtime` | `host` | `runtime.incus` |
| `interactive-host` | `internal-session` | `session.asset.install` |
| `source-git` | `hybrid` | `git.project` |
| `environment` | `host` | `environment.nix` |
| `event-handler` | `host` | `event.file.append` |
| `agent-adapter` | `internal-session` | `agent.status.report` |

Each capability is selected at most once per instance for MVP. The grant
names are typed broker verbs, not operating-system privileges or permission to
run arbitrary host commands. Runtime, Git, and Nix broker implementations must
also enforce operation-specific scope and the [runtime grant ceiling](runtime-isolation.md)
before their public methods become active. A package can request only grants
assigned to its capability. Trusted activation grants must match the package's
requests exactly; adding a grant requires manifest and trusted-config review.

`runtime.kind` is one of `declarative`, `wasi-command`, or `assets`. The only
current declarative operation is `event.file.append`; it is available only to
an `event-handler`. `wasi-command` names a WebAssembly entry in the package;
its implemented methods are the event handler, source-Git, and runtime methods
described below. Asset packages
carry fixed files for the session's systemd/attach or adapter contract.
Core validates activation and typed asset plans before installing their fixed
paths in an owned, stopped session through the confined runtime adapter.

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

### WASI source-Git ABI

The `source-git` capability uses `hybrid` placement, the `git.project` grant,
and a `wasi-command` entry. Trusted source-Git config is absent or `{}`; no
other config fields are accepted. Its module implements source-operation sequencing
through the closed broker methods below. Core owns authorization, operation
intent, repository locations, credentials, and ref guards. The broker invokes
real Git; Git owns objects, refs, ancestry, and its wire protocol. SSH
authentication and stream handling remain trusted substrate. A declarative
selector for a compiled-in backend does not implement this executable
contract. These schemas govern linkage; live session installation remains
later runtime work.

The module uses the same WASI Preview 1 command and `p_broker_v1.call` import
as an event handler. Core supplies one `p.command/v1` JSON object and newline.
Every request has `schema`, `kind`, `scope`, and `project`. `scope` is an opaque
invocation token of at most 64 bytes; `project` is the logical project path,
never a filesystem path. The additional fields depend on `kind`:

| Kind | Additional fields | Operation scope |
|---|---|---|
| `git.project.ensure` | `initial_head`: `refs/heads/main` | Ensure the authorized blank bare repository exists with the required symbolic HEAD |
| `git.refs.list` | `limit`: integer from 1 through 8 | Read the next page of user-visible branch refs from the cursor held by core |
| `git.transport.plan` | `service`: `upload` or `receive`; `ceilings`: positive integer `max_input_bytes` and `max_duration_ms` | Prepare the already authenticated, authorized SSH Git stream |
| `git.source.observe` | `source`: `{ "kind": "branch", "value": "refs/heads/<name>" }` or `{ "kind": "commit", "value": "<full object ID>" }` | Capture a committed source selected by core from an ordinary P branch or a full commit ID reachable from an ordinary P head |
| `git.branch.create` | `branch`: valid ordinary P branch name; `commit_oid`: captured full commit ID | Create only the absent ordinary P branch at that commit, under core's create intent and project/ref lock |
| `git.branch.delete` | `branch`: valid ordinary P branch name; optional `commit_oid`: confirmed old tip (omitted for an unborn absent ref) | Delete only the assigned P branch under core's durable Delete intent and ref guard, using an expected-old-tip compare-and-swap; an unborn branch must remain absent |
| `git.origin.observe` | None | Contact the core-selected SSH URL and capture one complete advertised branch/tag observation, including an empty one |
| `git.origin.fetch` | `origin_ref`: one advertised `refs/heads/<name>` or `refs/tags/<name>`; `commit_oid`: its captured peeled commit ID | Fetch that exact core-selected observation into the P object cache, without writing a P ref |
| `git.origin.publish` | `source_ref`: exact ordinary P head; `commit_oid`: captured source tip; `destination_ref`: one explicit origin head | Invoke one normal, non-force origin push only after core's same-scope observation and comparison authorize an absent destination or fast-forward |

For example, an initialization request is:

```json
{"schema":"p.command/v1","kind":"git.project.ensure","scope":"invocation-token","project":"example","initial_head":"refs/heads/main"}
```

The only successful command result is
`{"schema":"p.command-result/v1","status":"ready"}`. A module can instead
return `status` `refused` with an optional `message` of at most 256 bytes.
Unknown fields fail. Neither result supplies authoritative refs, OIDs,
authorization decisions, or native command arguments.

Every broker request has `schema` `p.broker/v1`, `method`, and the current
`scope` token, plus only the fields listed below. Scope is bound to the host
invocation and cannot be transferred or reused. Each successful reply has
`schema` `p.broker/v1` and `status` `ok`, plus the listed result fields:

| Broker method | Allowed command kind | Request fields beyond the envelope | Reply fields beyond the envelope |
|---|---|---|---|
| `git.repo.inspect` | `git.project.ensure` | None | `exists`: boolean; `head`: symbolic ref, or empty for an absent repository |
| `git.repo.init` | `git.project.ensure` | None | None |
| `git.repo.set-head` | `git.project.ensure` | None | None |
| `git.refs.next` | `git.refs.list` | `page_size`: integer from 1 through 8 | `refs`: array of `{ "ref": "refs/heads/main", "oid": "<Git object ID>" }`; `exhausted`: boolean; `page_complete`: boolean |
| `git.transport.prepare` | `git.transport.plan` | Optional positive integer `max_input_bytes` and `max_duration_ms`, each at most its core ceiling | None |
| `git.source.observe` | `git.source.observe` | None | `commit_oid`: observed full commit ID |
| `git.branch.create` | `git.branch.create` | None | None |
| `git.branch.delete` | `git.branch.delete` | None | None |
| `git.origin.observe` | `git.origin.observe` | None | None |
| `git.origin.fetch` | `git.origin.fetch` | None | None |
| `git.origin.publish` | `git.origin.publish` | None | None |

Source and destination fields are supplied by core in the command, never in a
broker request. The module may sequence an operation but cannot substitute a
selector, ref, OID, repository, or native Git argument. A source observation
accepts an existing P branch only when its current tip is a commit. A commit
selector must be a full object ID naming a commit reachable from an ordinary
`refs/heads/` ref at observation time. Unborn and missing branches, non-commit
objects, and commits reachable only through tags or hidden refs are refused.
Reading an existing branch never mutates its ref. A moved source is a new
observation; the captured ID is the input to later creation.

Branch creation verifies that the captured ID still names a commit reachable
from an ordinary P head, then calls native Git's atomic `update-ref` with the
all-zero old ID. It never resets an existing ref. An existing destination is a
conflict even when its tip equals the captured ID; only core's durable create
intent may determine whether that effect is an idempotent replay. A failed
module invocation does not undo a completed Git update; core reconciles its
intent against Git before retrying. These methods do not authorize an origin
fetch or session lifecycle transition.

Core supplies all repository locations and the requested HEAD. Initialization
creates only the operation's authorized bare repository; setting HEAD uses
the command's `initial_head`. Existing incompatible repositories are refused.
The module can inspect before initializing and can recognize an already
completed operation. It cannot choose another repository, HEAD, principal,
endpoint, executable, argv, environment, hook, or credential. Initialization
and HEAD mutation each run at most once per invocation. Core verifies the
bare-repository and symbolic-HEAD postconditions before accepting `ready`.
A module failure does not roll back completed Git effects; core's operation
intent and reconciliation own recovery.

For ref listing, core holds the caller's cursor and accumulates the actual Git
observations. Each `git.refs.next` advances that cursor in bytewise ref-name
order and returns at most the smaller of `page_size` and the remaining
command limit. `exhausted` means Git has no further visible branch refs;
`page_complete` means the requested limit or exhaustion has been reached.
Further reads after completion fail. Core accepts `ready` only at completion
and returns its accumulated observations and continuation cursor to the
caller. The module controls query batching without being able to fabricate
Git results. A changed repository between queries is an ordinary new Git
observation, not a stable snapshot; destructive decisions must re-inspect
under their own core operation scope.

Each `git.refs.next` runs a fresh native Git query restricted to
`refs/heads/`. For the MVP, ref listing supports at most 1,024 heads per
repository. A query observes at most 1,025 heads, using the extra entry to
detect overflow, and retains at most 300 KiB of native stdout within the
command deadline. Exceeding either bound fails the operation without returning
a partial page. Core filters this bounded observation against its bytewise
cursor; it does not reuse a prefetched observation across broker calls.
Native query diagnostics count attempted Git ref queries, including failures.

Transport preparation reserves exactly one plan for the invocation's existing
authorized stream. Omitted limits use core's ceilings; supplied limits can
only tighten them. The module cannot change the requested service or the
stream. Preparation does not start Git. Core starts the native service only
after a valid `ready` result, fresh authorization, and acquisition of the
assignment/ref-guard lease held through the Git process. Malformed output,
refusal, failed execution, invalid scope, or failed postconditions discard
the plan. Git pack bytes flow directly between SSH and Git outside the WASI
JSON channel, with the accepted byte and lifetime limits enforced by core.

Source commands retain the event runner's strict parser, API version, digest
snapshot, absent ambient authority, 2-second invocation deadline, four-command
concurrency ceiling, 16 MiB linear memory, and 4 KiB limits on each input,
output, diagnostic, broker request, and broker reply. They permit at most 16
broker calls. Wrong-scope or wrong-command methods fail the invocation; no
failure falls back to a native plugin. The separate Git stream lifetime starts
after command completion and is bounded by the prepared plan.

Origin commands use a separate two-command pool and a 35-second total deadline
including queueing, package digest verification, WASI execution, and native
Git/OpenSSH. They retain the same 16 MiB memory, 4 KiB JSON/diagnostic limits,
and 16-call maximum. The broker's origin methods accept no guest-supplied URL,
ref, OID, argv, environment, or filesystem path. `git.origin.observe` returns
no refs through the guest reply; core returns its own captured array of at
most 1,024 branch/tag entries. Each entry has `ref`, advertised `oid`, and
`commit_oid` (the advertised peeled OID for an annotated tag). Native
`ls-remote` stdout is limited to 300 KiB and stderr to 4 KiB. An empty
advertisement is successful. Bad syntax, duplicates, mixed object formats,
overflow, timeout, or SSH failure rejects the whole observation. Fetch must
use an entry from a successful observation in the same held project origin
scope; it rejects a moved ref or a target that does not validate as a commit.
A failed fetch invalidates that scope's observation, requiring another
successful observation before another fetch. Waiting to acquire the project's
origin lock is controlled by the caller context and occurs before the
35-second plugin invocation. Native command cancellation kills its process
group and allows at most 250 ms to close pipes retained by escaped children.

Publication preview is a native core comparison under that same origin/GC
scope: it checks the exact P head tip and commit type, fetches an observed
destination into the object cache without writing a ref, and returns only
`absent`, `equal`, `destination_contains`, `fast_forward`, or `divergent` with
the URL, refs, and object IDs. The scoped preview is consumed once. Equal,
destination-contains, and divergent relations do not invoke the publication
command. For absent and fast-forward relations, the package may call the
closed `git.origin.publish` broker method once. Its request carries no URL,
ref, OID, argv, or path; core pushes only the captured OID to the one selected
full destination head. The typed result is `created`, `advanced`, `satisfied`,
`refused`, or `outcome_unknown`. A transport or post-call failure that could
have followed acceptance is `outcome_unknown` and is never retried here.
Session assignment, confirmation, and idempotency remain host lifecycle work;
this substrate alone does not expose a public publication action.
The host uses a temporary bare transport repository without project-controlled
Git configuration. Its temporary `FETCH_HEAD` verifies the exact fetched tip;
the P repository receives only objects, with no ordinary or tracking ref.
The per-project lock is the seam future P-controlled garbage collection must
use; this method does not itself implement garbage collection.

The hybrid package can additionally declare exactly the session asset role
`p-git-ssh`, installed as `/usr/libexec/p/git-ssh` with mode `0555` and the same
1-byte-to-1-MiB asset bound as other session assets. No other source-Git asset
role or destination is accepted. This entry runs inside the session boundary
and invokes ordinary SSH using core-generated `/etc/p/git/ssh_config`; core
owns the endpoint, host-key pin, session key reference, provisioning, and
revocation. Package content contains no credential. Planning this role does
not install it; session installation and workspace wiring require later
runtime validation.

Conformance must exercise a separately built module through ordinary digest
selection without rebuilding core. A replacement that reads one ref per
broker call instead of the bundled batch size must produce the same real Git
page through a different observed call sequence. A replacement that lowers
the receive byte budget must reject a real over-budget push that the bundled
module permits within core's ceiling. Changed package IDs or declarative
limits alone are insufficient evidence. Invalid modules must never start a
transport, and every existing principal, assignment, fast-forward, and ref-
guard check still applies to both packages.

Build the bundled entry with `GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go build
-o git.wasm ./plugins/bundled/source-git`. Stage only `plugin.json`,
`git.wasm`, and the declared `p-git-ssh` asset in the flat package directory.
The independently compiled fixture at
`internal/plugin/testdata/git-alternate` stages only its own `plugin.json`
and `git.wasm`; its executable code selects one-ref queries and a 64 KiB
receive ceiling. Source files are authoring material, not declared package
files. These fixtures require real broker and transport validation before
support is claimed.

### WASI runtime ABI

The `runtime` capability uses `host` placement, the `runtime.incus` grant, and
a `wasi-command` entry. Trusted config is absent or `{}`. Core binds each
invocation to exactly one session and to the configured confined local Incus
user socket and project. The module sees no socket, instance name, image
locator, device, host path, native argv, or administrative credential.

The module receives one newline-terminated `p.command/v1` JSON object with
`kind` and an opaque `scope` token. `kind` is one of `runtime.inspect`,
`runtime.create`, `runtime.assemble`, `runtime.start`, `runtime.observe-host`,
`runtime.attach`, `runtime.stop`, or `runtime.delete`.
It returns `p.command-result/v1` with `status` `ready`, or `refused` and an
optional message of at most 256 bytes. Unknown fields fail. A `ready` result
does not supply authoritative state; core re-inspects Incus and checks the
requested postcondition.

The module uses the WASI Preview 1 `_start` entry and only the
`p_broker_v1.call` host import. Every broker request has `schema`
`p.broker/v1`, the current `scope`, and one closed `method` from the same eight
names. It has no further fields. A successful response has `schema`
`p.broker/v1`, `status` `ok`, `exists` (boolean), and `state` (Incus instance
status when present). `runtime.observe-host` also returns bounded native
`host_unit`, `host_ready`, `diagnostic`, and `diagnostic_available` fields.
`runtime.inspect`, `runtime.observe-host`, and `runtime.attach` are read only.
`runtime.attach` must be called exactly once for its matching invocation, after
inspection. It checks native ownership, systemd readiness, and the selected
root-owned unit and adapter assets through the image's fixed links. The native
broker retains the structured fixed spec outside WASI; the module cannot select
argv, sockets, or targets. Core repeats native checks before returning the spec.
Each mutating method is
permitted only for the matching command kind, after an inspection and at most
once per invocation. The broker methods operate solely on the core-bound
session. A module cannot change the target or turn an inspect into a delete.

`runtime.assemble` receives no file path or bytes from the module. Native
assembly installs only core-selected verified host, source, and optional agent asset plans,
bounded session/workspace config, and the one session's Git material
through the confined Incus file API on its owned stopped instance. Existing
files must be identical or assembly refuses them. `runtime.observe-host`
uses fixed native systemd and journal reads. Incus `Running` alone is never
host readiness; a stopped journal that is absent, oversized, or unreadable
produces an unavailable diagnostic without starting the instance.

Core derives the instance name from the session UUID, requires immutable P
identity metadata and a pinned image fingerprint, checks the confined project
and default profile before mutations, and reads Incus state after operations.
The native adapter is the sole owner of Incus calls. Interrupted calls are
reconciled by inspecting Incus, without replaying a create blindly. The
module may recognize a completed operation and return `ready` after its
inspection without another mutation. Replacement modules may change this
sequencing while remaining within the same closed effect set.

Runtime commands have a 120-second total deadline, two dedicated concurrent
slots, 16 MiB linear-memory limit, 4 KiB input/output/diagnostic/broker
request/reply limits, and at most four broker calls. The longer deadline
includes native Incus operations; it does not occupy event or Git command
slots. No directories, environment variables, or network sockets are passed
to the module. A timeout leaves native outcome unknown until fresh inspection.

Build the bundled module with `GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go build
-o runtime.wasm ./plugins/bundled/runtime-incus` and stage only
`plugin.json` and `runtime.wasm` in its flat package directory. Source files
are authoring material, not package files.

### WASI environment ABI

The executable `environment` capability uses `host` placement, the exact
`environment.nix` grant, and a `wasi-command` entry. Trusted config is absent
or `{}`. Core binds the module to one verified restricted builder, one system,
and its sealed committed source. The module receives no source or host path,
URL, builder identity, derivation, Nix argv, Nix configuration, or activation
material. [Environment building](environment-building.md) owns the Nix
selection and isolation policy.

Core invokes two separate WASI Preview 1 `_start` commands. Each receives one
newline-terminated JSON object with exactly `schema: "p.command/v1"`, an opaque
32-character `scope`, and `kind` equal to `environment.resolve` or
`environment.realize`. Each command may call `p_broker_v1.call` exactly once
with the same `scope`, `schema: "p.broker/v1"`, and `method` exactly equal to
the command kind; no further request fields are permitted. A successful
broker reply is exactly `{"schema":"p.broker/v1","status":"ok"}`. The
module returns one `p.command-result/v1` object whose only fields are
`schema` and `status`, with status `ready` or `refused`. `ready` requires one
successful matching broker call. Unknown fields, wrong scope or kind,
repeated effects, malformed output, or a nonzero exit fail the stage without
native fallback.

The resolve broker invokes the native sealed-source Nix resolver and retains
its selection only after a valid `ready` result. Core can inspect that native
selection and its environment key before deciding whether to invoke realize;
a base-only selection never realizes. The realize broker uses exactly that
accepted selection, repeats native identity and derivation checks, and keeps
the captured activation material outside WASI. A refusal or malformed result
after a native effect discards the pending selection or material; a new
pipeline is required for retry. Module output never supplies a selection,
key, derivation, or material.

Each stage has a seven-minute deadline including queueing, package digest
verification, and its native broker call, with two concurrent slots. The
runner limits linear memory to 16 MiB, module entry to 8 MiB, each command,
result, diagnostic, and broker request/reply to 4 KiB, and broker calls to
one. No directories, inherited environment, network sockets, credentials, or
host command imports are passed to WASI. The native Nix batch currently
allows only closed offline inputs and a five-minute per-command ceiling;
public fetch/substitution policy remains pending. After an accepted realization,
core verifies activation, prepares and publishes the private Incus image, and
records the project-scoped cache identity. Publication has a separate durable
attempt and recovery boundary; WASI cannot choose an image or publish target.

Build the bundled module with `GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go build
-o environment.wasm ./plugins/bundled/environment-nix` and stage only
`plugin.json` and `environment.wasm` in its flat package directory.

### Session asset plans

An `interactive-host` asset package must contain exactly `p-session.target`,
`p-interactive.service`, and `p-attach`. An `agent-adapter` asset package must
contain exactly `p-codex-adapter`. These names are roles; a package cannot
choose host or session destinations. `p plugins plan-assets <activation.json>
<plugin-id>` emits a `p.asset-plan/v1` summary with the package digest and
per-file role, destination, mode, and SHA-256. Core holds verified bytes in
the in-memory plan for a later session installer:

| Role | Fixed session destination | Mode |
|---|---|---|
| `p-session.target` | `/etc/systemd/system/p-session.target` | `0644` |
| `p-interactive.service` | `/etc/systemd/system/p-interactive.service` | `0644` |
| `p-attach` | `/usr/libexec/p/attach` | `0555` |
| `p-codex-adapter` | `/usr/libexec/p/codex-adapter` | `0555` |

Plans are limited to `internal-session` placement. Each asset must contain
1 byte to 1 MiB. A changed package, wrong capability, missing or extra role,
or wrong grant is rejected. Planning does not install files; later runtime
work must write under session scope and verify ownership and destination
safety.

## Discovery and trusted activation

`p plugins list /absolute/catalog/path` scans immediate package directories
and reports valid packages and rejections. It does not activate anything.
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
the committed transition. Install, update, removal, and automatic default
selection remain to be implemented. Update requires staging a new digest and
explicit trusted selection; removal disables new calls before retiring assets
or credentials. A plugin failure returns a bounded diagnostic to core and
never changes identity, policy, or committed lifecycle state.
