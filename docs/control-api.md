# Local control API

This reference specifies the implemented host control connection and private
per-session RPC methods. The host connection speaks the control protocol
`p.control/v2`, which each connection agrees once with
[`system.hello`](#connection-agreement).
[Communication boundaries](communication-boundaries.md#control-rpc) owns the
audience and channel boundary. Runtime configuration enables the creation,
inspection, Start, Stop, and attachment subset described below.

## Daemon configuration and command

Run `p daemon /absolute/path/host.json`. The host-owned file is a private
regular file with one link, owned by the daemon user and mode `0600` or
stricter. Its ancestors may not be symlinks or writable by group/other, except
root-owned sticky directories such as `/tmp`. Its maximum size is 64 KiB.
Unknown fields, duplicate JSON keys, and extra JSON values fail startup.

```json
{"schema":"p.host/v1","state_dir":"/absolute/private/p-state"}
```

`state_dir` is created with mode `0700` if absent and must be owned by the
daemon user. It contains `control.sqlite`, SQLite WAL files, `daemon.lock`, and
`control.sock`. The lock admits one daemon per state directory. The socket is
mode `0600` inside the private directory. With no `git` object, the daemon
keeps the control foundation only.

An optional trusted Git selection activates the real source-Git package and
its host Git service:

```json
{"schema":"p.host/v1","state_dir":"/absolute/private/p-state","git":{"activation_path":"/absolute/private/activation.json","source_plugin_id":"org.p.git","listen":"127.0.0.1:2222"}}
```

The activation file must be an owner-owned private regular file under trusted
ancestors. Its selected source package must have the named ID, `source-git`
capability, WASI command runtime, and exact `git.project` grant. The approved
package digest is checked during loading and again at each invocation. Changes
to the activation file take effect after daemon restart. `listen` is a numeric
loopback TCP address with an explicit port; port `0` asks the OS for a free
loopback port and `system.inspect` reports the actual endpoint.

The state directory holds persistent `git_server_key` and `git_client_key`
Ed25519 private files at mode `0600`. The daemon registers the latter as its
read-only host Git principal. SQLite pins the server public-key fingerprint;
the daemon refuses startup if its private file is lost or replaced. A changed
or revoked client principal, corrupt key, or unsafe file also prevents startup.
RPC exposes public keys, a known-hosts entry,
an SSH URL template, and the local path to the client key; private key bytes
never enter RPC.

An optional trusted `runtime` object activates the lifecycle subset. It
requires Git configuration. The daemon verifies the confined Incus project
and selected package digests before serving RPC. `runtime.project_policies`
selects a policy only by the exact complete P project path. An absent path has
no fallback and cannot admit new project or session creation. The older
`runtime.project_policy` remains a singleton compatibility mode only when the
map is absent; supplying both fields is invalid. Requests and repository files
cannot add networking, mounts, commands, or other grants.

```json
{
  "schema": "p.host/v1",
  "state_dir": "/absolute/private/p-state",
  "git": {"activation_path":"/absolute/private/git-activation.json","source_plugin_id":"org.p.git","listen":"127.0.0.1:2222"},
  "runtime": {
    "activation_path": "/absolute/private/runtime-activation.json",
    "runtime_plugin_id": "org.p.runtime.incus",
    "host_plugin_id": "org.p.tmux-host",
    "incus_binary": "/absolute/trusted/incus",
    "incus_user_socket": "/absolute/confined/unix.socket.user",
    "incus_project": "p-confined",
    "endpoint_prefix": "/absolute/private/p-endpoints",
    "disk_source_ceilings": ["/absolute/private/p-endpoints", "/absolute/private/p-grants"],
    "base_image_fingerprint": "<64 lowercase hex digits>",
    "project_policy": {"network":"none","filesystem_mounts":[{"name":"data","source":"/absolute/private/p-grants/data","type":"directory","access":"read-only","executable":false}],"command":["/run/current-system/sw/bin/bash"]}
  }
}
```

The exact-path form replaces the single `project_policy` field with, for
example:

```json
"project_policies": {
  "team/app": {"network":"none","filesystem_mounts":[],"command":["/run/current-system/sw/bin/bash"]},
  "team/tool": {"network":"none","filesystem_mounts":[],"command":["/run/current-system/sw/bin/sh"]}
}
```

Policies require `network:"none"` or `network:"public-egress"` and a validated
absolute interactive command. Public egress requires a separate trusted host
substrate, for example:

```json
"public_egress": {
  "network": "p-public-v1",
  "acl": "p-public-v1-acl",
  "bridge_ipv4": "10.233.0.1/24",
  "dns": ["1.1.1.1", "9.9.9.9"],
  "sudo_binary": "/run/wrappers/bin/sudo",
  "nft_binary": "/nix/store/<pinned-nftables>/bin/nft",
  "bridge_proof_binary": "/nix/store/<pinned-proof>/bin/p-public-network-proof"
}
```

The machine owner preconfigures that exact managed bridge, network ACL, and
root-owned nftables INPUT/FORWARD rules. P checks their live state through its
confined Incus socket and two exact read-only sudo commands before creation or
Start. Incus redacts bridge configuration and denies ACL reads to the confined
project principal. The fixed, zero-argument proof helper reads only the named
network and ACL through the root-owned admin socket and returns their bounded,
validated configuration and rules.
P never receives that socket or network-edit permission.
The bridge has no DHCP, DNS service, or IPv6; P reserves a distinct static IPv4
address for each public session and records the substrate digest in the
immutable policy. The guest installs only that address and route, plus a
root-owned loopback resolver. The `dns` pair pins the image's DNS-over-HTTPS
bootstrap addresses; the fixed resolver verifies the Cloudflare/Quad9 TLS
names and uses TCP 443 without host DNS or plaintext fallback. A `none` session
receives no NIC or running resolver even in a project that
permits the public bridge. A changed or missing public substrate makes the
captured public policy `invalid` and blocks Start. With this substrate,
environment builders get the same network, whatever the project's policy
([environment building](environment-building.md#security-boundary)).

Up to eight filesystem grants may name an exact canonical host file
or directory under a configured, confined Incus disk ceiling. Each grant has a
portable unique `name`, `type` (`file` or `directory`), `access` (`read-only` or
`read-write`), and explicit `executable` boolean. The guest target is always
`/mnt/p/<name>`; requests cannot select a source or target. Host configuration
cannot provide `source_identity`. P rejects symlinked, overlapping, protected,
or unsafe source paths, captures the source device, inode, and owner in the
immutable policy, and revalidates them before Start. Incus gets only exact
private, nonpropagating disk devices; the guest verifies the effective mount
flags and source identity before workspace initialization or interactive code.
Discard and Delete remove the session device and runtime, never the host grant
source or its contents.

Creation stores the canonical effective policy and digest in the
project/session record. A later
valid change to the exact configured policy makes an older session's
`policy_condition` `outdated` without mutating its runtime. Removing its map
entry or losing the trusted confinement authority makes the condition
`invalid` and blocks Start; restoring the entry re-enables comparison. A new
session captures the newly configured policy.

An optional `runtime.environment` object selects the environment plugin and
restricted builder pool. Offline public session composition passed its pinned
VM gate; see [implementation progress](implementation-progress.md).

```json
{"activation_path":"/absolute/private/environment-activation.json","plugin_id":"org.p.environment.nix","system":"x86_64-linux","builder_storage_pool":"p-builders"}
```

The selected module must be an activated `environment` WASI package with exactly
the `environment.nix` grant and empty configuration. The activation file uses
the same trusted host-file checks as other selections. The system is explicitly
`x86_64-linux` or `aarch64-linux` and must match the observed Incus host and
pinned base architecture before selection. Current VM evidence covers x86_64;
aarch64 support still requires its corresponding validation. The builder pool
must satisfy the native adapter's btrfs quota checks.
With `public_egress` configured, the builder gets the public-egress network
and the public binary cache, using its session's address slot while it builds
([environment building](environment-building.md#security-boundary));
without it, the builder gets no NIC or substituters. It never gets a host
store, filesystem grants, or credentials.

Creation records the module and configuration digests, system, base image,
builder policy, and captured commit before effects. Retry requires the same
selection and never recaptures a moving branch or origin. An interrupted image
publication is reconciled against its durable attempt before proceeding.
Concurrent requests for the same project/environment key share the resulting
verified image. An unresolved prior publication blocks another publication for
that key until its original operation is reconciled; it cannot silently replace
the indexed image. Concurrent creation passed the selected 7c3 VM gate below.
Omitting `runtime.environment` retains the base-image creation path.

An optional `runtime.agent_adapter` object selects one session asset package:

```json
{"activation_path":"/absolute/private/agent-activation.json","plugin_id":"org.p.codex-adapter"}
```

The named package must have the `agent-adapter` capability, asset runtime,
exact `agent.status.report` grant, and empty configuration. The activation file
uses the same private host-file checks as other selections. Creation pins the
selected package ID and digest; Retry requires that same selection.
Stopped assembly installs its verified asset at `/usr/libexec/p/codex-adapter`.
Repository contents and session requests cannot select the adapter. Omitting
the object installs no adapter.

The bundled adapter targets Codex `0.151.0`. Authentication-free fixture checks
and authenticated acceptance are separate gates; see
[implementation progress](implementation-progress.md#manual-codex-acceptance--pending-user-validation).
P never obtains Codex credentials from the host. The user initializes and
authenticates inside each session's private home.

An optional trusted `events` object selects one activated `event-handler`
package for daemon delivery:

```json
{"schema":"p.host/v1","state_dir":"/absolute/private/p-state","events":{"activation_path":"/absolute/private/events-activation.json","plugin_id":"org.p.filelog"}}
```

The activation file follows the same private host-file and trusted-ancestor
checks as the Git and runtime selections. The named package must have the
`event-handler` capability and exact `event.file.append` grant. Its digest is
checked on each invocation. The daemon queues at most 64 reduced events and
delivers them with one worker and a two-second invocation deadline. If a native
filesystem call remains blocked at that deadline, the worker retires and drops
its remaining queue; at most that one call can remain in flight. Its final file
effect may be unknown. Full queues, handler failures, and shutdown drop events
without changing committed session or operation state. There is no replay or
retry after restart.
Committed status reports and confirmed attachment transitions enter the queue
in reducer order. The queue is best effort and may drop either event when full.

`endpoint_prefix` is daemon-owned mode `0700`. Each session receives one direct
child directory mode `0755` with exactly `git.sock` and `session.sock` mode
`0666`. The Git socket bridges to this daemon's SSH listener. The session
socket is mounted privately into its matching runtime as `/run/p/session.sock`;
it serves only the [private session methods](#private-session-rpc).
`session_keys/<UUID>` in the state directory holds one private Ed25519 key per
session. A registered key that is missing or changed blocks retry and Start.
P never rotates it; the refusal directs the user to discard the session and
create a new one, as described in
[session lifecycle](session-lifecycle.md#inconsistent-sessions).

## Host framing and request envelope

Connect to `<state_dir>/control.sock` as the same local user. Each UTF-8 line
is one JSON-RPC 2.0 message, with a 64 KiB maximum including its newline. An
oversize or unterminated line receives a bounded parse error and closes the
connection. Up to 64 client connections and 16 concurrent requests per
connection are admitted. Requests have a 30-second deadline. The daemon
closes idle connections after two minutes and closes active connections on
shutdown.

Every request carries a `params` object. Each method has one request type and
one result type in `internal/api`: `api.Methods()` in
`internal/api/method.go` lists every method with both types, and each request
type's `Validate` in `internal/api/requests.go` states that method's bounds,
even where two methods take the same fields. The daemon decodes `params`
strictly into the method's request type and then runs `Validate`; an unknown
or duplicate field or a value out of bounds answers `invalid` with reason
`param` and, when one field is at fault, its name in `field`. No request or
result carries a version, so a `v` field is an unknown field. `system.health`
and `system.inspect` take `{}`, and a request without `params` means `{}`; the
other methods take the parameters below. Requests need a
string or integer `id`; duplicate in-flight IDs on one connection are refused.
An absent `id` is a notification, which receives no response. One response is
sent for each valid request, with the same ID, `jsonrpc:"2.0"`, and exactly one
of `result` or `error`. Responses may arrive out of order. JSON object keys
must be unique at every depth.

The CLI `p api [socket-path] <method> [json-params]` connects, sends
`system.hello` itself, then sends one request and writes that request's full
JSON response envelope to stdout; the envelope's `id` is `2`. `json-params`
defaults to `{}`, or to `{"protocol":"p.control/v2"}` for `system.hello`, so
`p api system.hello` prints the daemon's hello result. An RPC error exits
nonzero. A daemon that refuses `p api`'s own hello has its error object
written as `{"error":{…}}`; any other connection failure writes
`{"error":{"message":"transport","detail":"<text>"}}`. Both exit nonzero.

The host client commands `p api`, `p tui`, and `p attach` select their control
socket in this order: an explicit positional socket argument, a nonempty
`P_SOCKET` environment variable, then `/var/lib/p/control.sock`. An empty or
unset `P_SOCKET` uses the standard location. Selected paths must be absolute;
an invalid override fails without falling back to another instance. For an
owner-run daemon in another state directory, export
`P_SOCKET=/absolute/private/state/control.sock` once, then use the ordinary
commands. The NixOS service module configures this variable for its declared
state directory. Socket selection does not change the same-owner access rule
or the daemon's private socket permissions.

## Connection agreement

The first request on every host control connection must be `system.hello`.
It agrees the control protocol for the rest of the connection and reports what
this daemon serves:

```text
→ {"jsonrpc":"2.0","id":1,"method":"system.hello","params":{"protocol":"p.control/v2"}}
← {"jsonrpc":"2.0","id":1,"result":{"protocol":"p.control/v2","build_version":"development","instance_id":"<instance UUID>","capabilities":["git","runtime","services"]}}
```

| Result field | Meaning |
|---|---|
| `protocol` | The agreed protocol, `p.control/v2`. |
| `build_version` | The daemon's build version; `development` unless set at link time. |
| `instance_id` | The instance's durable UUID. |
| `capabilities` | The parts of the daemon that host configuration enabled, in the order of the table below. Without Git it is `[]`. |

| Capability | Present when | Methods it serves |
|---|---|---|
| `git` | The host configuration has a `git` object | `project.list`, `project.branches`, `project.retained_branches` and the six `origin.` methods |
| `runtime` | The host configuration also has a `runtime` object, which requires `git` | Project and session creation; session inspection, listing, Start, Stop, attachment, removal and rename; operations; failed-creation replacement and cleanup; retained-branch rename and deletion; project deletion; the attachment helper's methods |
| `services` | `runtime` is present; the daemon always serves project services with a runtime | `session.services`, `session.service.action` and `session.service.logs` |

Every daemon serves `system.hello`, `system.health`, `system.inspect` and
`rpc.cancel`. The protocol, not the configuration, fixes which methods exist:
`api.Methods()` names the capability that serves each one. A method whose
capability is not configured answers `not_configured` whatever its params,
with the capability as its reason:
`{"code":-32016,"message":"not_configured","data":{"reason":"runtime"}}`. A
name outside the protocol answers `method_not_found`.

The connection follows these rules:

- **Hello first.** Until a hello succeeds, any other request answers
  `invalid_request` with reason `hello_required`:
  `{"code":-32600,"message":"invalid_request","data":{"reason":"hello_required"}}`.
  A notification before a hello, including a hello without an `id`, is
  ignored. Errors in the line or its envelope (framing, parse, the `jsonrpc`
  version, the request ID, missing `params`) are answered with or without a
  hello, because the daemon checks them first.
- **One protocol.** A hello naming another protocol answers
  `unsupported_version` with reason `protocol` and `supported`, the protocols
  the daemon speaks:
  `{"code":-32002,"message":"unsupported_version","data":{"reason":"protocol","supported":["p.control/v2"]}}`.
  A hello whose params are not exactly `{"protocol":"…"}` with a 1 to 64 byte
  value, such as `{}` or `{"v":1}`, answers `invalid` with reason `param`.
- **Pipelining.** The daemon answers the hello before it reads the next line,
  so a client may write the hello and its first request together.
- **Repeats.** A repeated hello on the same connection is answered again.

The TUI and `p attach` connect with `api.Dial`, which sends the hello; the
attachment helper sends its own hello on its lease connection. These clients
call each method through `api.Do`, which runs the request's `Validate` before
sending. Only `p api` sends untyped params, through `api.CallRaw`.

## Private session RPC

The daemon binds each `session.sock` to its directory's session UUID. The
socket accepts requests only while that session's registry state is
`established`, and otherwise answers `not_found` with reason `session` and the
session as `target`; no parameter can select another session. Errors take the
[error shape](#errors) of the control socket. It never forwards a
request to the host control connection. The [session RPC audience](communication-boundaries.md#session-rpc-audience)
defines the boundary.

Session RPC uses newline-delimited JSON-RPC 2.0 over one persistent Unix
connection. Each line must be UTF-8 and at most 64 KiB including the newline.
An oversized or unterminated line receives a bounded parse error and closes
the connection; an idle connection closes after two minutes. The endpoint
manager admits at most four connections per session UUID and 16 across the
instance, including idle connections. Closing a connection releases both
limits. Recreating a session listener preserves its UUID admission limit.
Object keys must be unique, and methods reject unknown parameter fields.
Requests use a string
or integer `id`; notifications omit `id` and receive no response.
Session RPC has no `system.hello`: it keeps its per-request version, and every
request and result carries `"v":1`. Whether it adopts the
[connection agreement](#connection-agreement) is left to the plugin contract.

| Method | Params | Result |
|---|---|---|
| `session.identity` | `{"v":1}` | `v`, the socket-bound `uuid`, and current assigned `branch`. |
| `session.capabilities` | `{"v":1}` | `v`, `uuid`, `branch`, immutable `policy_sha256`, and `effective_capabilities`: `network`, `filesystem_mounts`, `source_git:true`, and `status_report:true`. The network and mounts come from this session's captured policy. |
| `status.report` | `{"v":1,"condition":"attention","adapter":"codex","adapter_version":"0.151.0","source":"codex/session/b5f6c1c2-1111-2222-3333-444455556666","reason":"permission requested"}` | Notification only: omit `id`. It has no response or caller-selected UUID. |

`status.report` accepts the five condition values defined in the
[session status protocol](session-observability.md#session-status-protocol).
`adapter` (1–128 UTF-8 bytes) and `adapter_version` (1–64 bytes) are required;
`source` (up to 128 bytes) and `reason` (up to 256 bytes) are optional. Supplied
strings must consist of printable Unicode characters. Unknown versions,
conditions, fields, and duplicate keys are rejected. A report with an `id`
returns `invalid_request` with reason `envelope` and does not update status. Valid reports are limited
to 20 per second and 240 per minute per session; excess notifications are silently
dropped. Complete, size-bounded private RPC lines, including malformed JSON
and queries, share a separate limit of 100 per second and 600 per minute per
session. Oversized or unterminated frames fail framing before that limit is
checked. Reaching the limit closes the connection without replying to the
over-budget line. The [latest unattended
reduction](session-observability.md#latest-unattended-condition)
owns when an accepted report replaces or clears the durable value.

## Implemented methods

| Method | Result fields | Meaning |
|---|---|---|
| `system.hello` | `protocol`, `build_version`, `instance_id`, `capabilities` | Agrees protocol `p.control/v2` for the connection and reports the configured capabilities; see [connection agreement](#connection-agreement). |
| `system.health` | `control_state` | `ready` means the SQLite control store responded. It makes no Git, Incus, systemd, or plugin readiness claim. |
| `system.inspect` | `projects`, `sessions`, `operations`, optional `git` | Counts persisted control records. Counts are not reconciled runtime or Git status. With Git configured, `git` contains the actual listener endpoint, SSH URL template, server public key, known-hosts entry, host client public key and key path, and selected plugin ID and digest. |

With trusted Git configuration, two more read-only methods are available:

| Method | Params | Result | Meaning |
|---|---|---|---|
| `project.list` | `{"limit":1..100,"after":"optional/project"}` | `projects` (`path`, `registry_state`), `next` | Lists registered projects in bytewise path order. `next` is empty at the end. |
| `project.branches` | `{"project":"project/path","limit":1..8,"after":"optional/refs/heads/name"}` | `project`, `refs` (`ref`, `oid`), `next` | Lists user-visible branches of an active registered project through the selected source-Git package and real Git. `next` is empty at the end. |

`rpc.cancel` takes `{"id":<request-id>}`. It cancels a pending request
on the same connection. As a notification it has no reply. As a request it
returns `{"cancel_requested":true}`; this only acknowledges the
request to cancel, including when the target already finished. A cancelled
target answers `cancelled` if it was still running, unless its handler had
already returned a result, which is sent.

With trusted Git configuration, active projects also support explicit origin
association and observation. The origin methods and
`project.retained_branches` need only the `git` capability, so they are
available even when runtime configuration is absent. The retained-branch
rename and deletion and the project deletion methods in this table need the
`runtime` capability. Every request rejects unknown or duplicate fields.

| Method | Params | Result |
|---|---|---|
| `origin.change` | `{"key":"idempotency-key","project":"team/app","kind":"set","expected_url":"","url":"ssh://host/repo"}`; use `kind:"remove"`, the current `expected_url`, and omit `url` to remove | `origin` state. A set contacts the proposed origin before commit; remove makes the project local-only. The old URL is an exact compare-and-swap precondition. |
| `origin.refresh` | `{"project":"team/app"}` | current `origin` state after contact. Failed contact returns `status:"unknown"` and retains the prior completed observation as stale presentation data. |
| `origin.inspect` | Same as refresh | current `origin` state without contact. |
| `origin.sources` | `{"project":"team/app","after":"optional-ref","limit":1..16}` | `project`, `origin_url`, `status`, `observation_status`, `observed_at`, `refs` (`ref`, `oid`, `commit_oid`), `next`. Refs are bytewise ordered and paged; `next` is empty at the end. |
| `origin.publication.preview` | `{"project":"team/app","expected_origin_url":"ssh://host/repo","kind":"session","source":"session-UUID","source_oid":"<exact P tip>","destination_ref":"refs/heads/main"}`; `kind:"retained"` uses a P branch name in `source` | `preview` with project, origin URL, source kind/identity/ref/OID, destination ref and observed OID or absence, relation, and `workspace_status:"unknown"`. This contacts the origin afresh. |
| `origin.publish` | Same source fields as preview, plus `"key":"idempotency-key"` | `publication` with the preview and `status` (`created`, `advanced`, `satisfied`, `refused`, or `outcome_unknown`). This performs its own fresh refresh and comparison; only `absent` or `fast_forward` can issue one ordinary push. |
| `project.retained_branches` | `{"project":"team/app","after":"optional-full-P-head-ref","limit":1..8}` | `branches` with `branch`, full `ref`, and tip `oid`, plus `next`. The cursor traverses P Git refs, so a page may be empty while `next` is nonempty if its refs are assigned to live sessions. |
| `project.retained.rename` | `{"key":"idempotency-key","project":"team/app","old_branch":"saved","new_branch":"archive","expected_old_tip":"<exact P commit OID>"}` | `operation` (`kind:"project.retained.rename"`). The source must be an unassigned retained P branch at that exact commit, and the destination must be absent and unassigned. A completed key replays; changed inputs with the same key answer `invalid` with reason `key_reused`. |
| `project.retained.delete.preview` | `{"project":"team/app","branch":"saved"}` | `preview` with the unassigned P `ref`/`tip`, `branch_loss` (commits losing all P reachability, complete P refs, fresh origin preservation or explicit `unknown`), `confirmation_token`, and `expires_at`. It is read-only and needs no runtime inspection. |
| `project.retained.delete.confirm` | `{"key":"idempotency-key","project":"team/app","branch":"saved","confirmation_token":"<32 lowercase hex>"}` | `operation` (`kind:"project.retained.delete"`). A completed exact key replays; changed key inputs answer `invalid` with reason `key_reused`. |
| `project.delete.preview` | `{"project":"team/app","acknowledge_missing":[]}` | aggregate `preview` for whole-project deletion, including sessions, runtime loss by category, all P branch loss, exact credentials/repository/cache identities, preserved external mounts, warnings, token and expiry. It contacts no origin. |
| `project.delete.confirm` | `{"key":"idempotency-key","project":"team/app","confirmation_token":"<32 lowercase hex>"}` | durable `operation` (`kind:"project.delete"`), returned at once. A fresh preview must match the reviewed one before atomic authority closure. Completed exact confirmation replays without deleting again. |

An origin state has `project`, optional `url`, `status` (`local-only`, `fresh`,
or `unknown`), optional `observed_at`, `ref_count`, and optional bounded
`diagnostic`. `origin.sources` labels retained refs `stale` when current status
is `unknown`; they cannot authorize source selection or publication. A
completed `origin.change` key replays its committed result without contacting
the origin. Reusing a key with different inputs answers `invalid` with reason
`key_reused`, and an `expected_url` that no longer holds answers
`precondition_failed` with reason `changed`. Interrupted
preparations retry the same contact and compare-and-swap. Origin URL and SSH
credentials stay on the host.

Retained branch rename guards both P names while it creates the destination
with an absent-ref compare-and-swap and deletes only the exact old source tip.
The durable phases are `reserved`, `new-ref-create-issued`, `new-ref-created`,
`old-ref-delete-issued`, `old-ref-deleted`, and `completed`. A stale input
before the first issued effect fails as `stale` and releases both guards. An
uncertain issued effect stays blocked and guarded until exact positive
reconciliation; retry never repeats an uncertain Git command. The operation
does not change session assignment, runtime, credential, or origin refs.

Retained branch deletion is Git-only. Confirmation reobserves the complete
reviewed loss report and origin identity under ref authority, then reserves the
unassigned source branch. Immediately before the selected Git effect, it
rechecks the report and records `ref-delete-issued`; only an exact old-tip
compare-and-swap may delete the P ref. Changed review facts fail `stale` before
that marker. An uncertain issued deletion remains guarded and is never
reissued; exact ref absence permits completion. It does not delete origin
refs, sibling refs, sessions, runtimes, images, or credentials.

Publication requires an established session with no unfinished lifecycle
operation or ref guard, or a retained branch with no live session assignment.
The exact `source_oid` must still be that P branch's tip. The preview is
informational; `origin.publish` requires the same `expected_origin_url` that
the developer reviewed. The URL is persisted with the request key, and a changed
origin answers `precondition_failed` with reason `changed` before any contact or
push. Reusing a key with another URL answers `invalid` with reason
`key_reused`, including after restart or a pre-start failure. The action
rechecks facts under the project origin lock and excludes assignment/ref
mutations through the shared Git authority lock. Publication keys share one namespace with creation and origin-change
keys. A completed key replays its recorded outcome without contacting Git. If
a push may have started but the daemon lost its result, the key replays
`outcome_unknown` and never starts another push. A new explicit request and key
can perform another fresh comparison. Runtime workspace divergence is currently
unobservable at this API boundary, so the preview reports `unknown`; only the
committed P tip is eligible for publication.

With trusted runtime configuration, the following lifecycle subset is also
available. A create response returns a durable operation immediately. The
daemon continues creation under its own context. Clients inspect the
operation until it is `completed` or `blocked`. `operation.retry` resumes the
same captured request, UUID, source commit, policy, and selected plugin
digests. It reuses the recorded image when available. A verified cache miss
before an instance exists may rebuild from the same captured source; an
uncertain instance or publication cannot trigger blind recreation. A changed
request needs a new idempotency key. Reusing a blocked creation's assigned
branch requires the explicit replacement methods below, or supported confirmed
failed-create cleanup followed by a separate Create.

| Method | Params | Result |
|---|---|---|
| `project.create` | `{"key":"idempotency-key","project":"team/app"}`; optionally include `"url":"ssh://host/repo"` for origin mode | `operation`; blank mode reserves unborn `main`. Origin mode contacts the URL before committing the project and observation; an empty origin also reserves unborn `main`. |
| `session.create` | `{"key":"idempotency-key","project":"team/app","branch":"work","choice":"existing"}` or `choice:"new"` with `source:"refs/heads/main"` or a committed object ID; origin mode uses `choice:"new"`, `origin_ref:"refs/heads/main"` or a tag, and `expected_commit_oid:"<observed commit>"` with no `source` | `operation`; origin mode requires a fresh observation and fetch, then captures the exact commit and origin identity for Retry. Blank session creation is unavailable. |
| `session.create.replace.preview` | `{"old_uuid":"blocked-session-UUID","key":"new-idempotency-key","project":"team/app","branch":"work","choice":"existing"}`; a failed local new-branch creation also permits `choice:"new"` with a local `source` branch or committed OID and an absent target | `preview` with old immutable request/operation/UUID/source/policy/image, `old_branch` and `new_branch` observations, new request/source/policy/image/plugin selection, provisional resource facts, `eligible`, and `unsafe_reasons`. Eligible previews also return `confirmation_token` and `expires_at`. |
| `session.create.replace.confirm` | `{"old_uuid":"blocked-session-UUID","key":"new-idempotency-key","confirmation_token":"<32 lowercase hex>"}` | new `session.create` operation; an exact accepted key/token replays after restart. |
| `session.create.cleanup.preview` | `{"uuid":"blocked-session-UUID"}` | `preview` with the old immutable request and evidence digest, assigned ref, image, tracked builder state, provisional resources, `external_mounts:"preserved"`, `shared_images:"preserved"`, eligibility and reasons; an eligible preview includes a two-minute confirmation token. |
| `session.create.cleanup.confirm` | `{"uuid":"blocked-session-UUID","key":"cleanup-idempotency-key","confirmation_token":"<32 lowercase hex>"}` | one `session.create.cleanup` operation on the old UUID; exact accepted key/token replay works during cleanup and after restart. |
| `operation.inspect` | `{"id":"operation-UUID"}` | `operation` with status, phase, a `reason` and `diagnostic` once it is blocked or failed, and immutable evidence. |
| `operation.retry` | Same as inspect | `operation`; schedules supported blocked-operation recovery using its persisted exact intent. |
| `operation.list` | `{"limit":1..20,"after":"optional-operation-UUID"}` | concise operation summaries (without request/evidence, with the same `reason` and `diagnostic`), `next` in bytewise ID order. |
| `session.inspect` | `{"uuid":"session-UUID"}` | `session` with registry state, public session and policy conditions, `attached_count`, nullable `latest_unattended_condition`, and bounded diagnostic. |
| `session.list` | `{"limit":1..8,"after":"optional-session-UUID"}` | `sessions` with the same fields as `session.inspect`, and `next` in bytewise UUID order. Concurrently removed sessions may be omitted; follow a nonempty `next` even when `sessions` is empty. |
| `session.start` | `{"uuid":"session-UUID"}` | `session` with `starting` while a daemon-owned watcher converges readiness; poll `session.inspect` for `ready` or `stopped` with diagnostic. The watcher holds the session until it ends, and inspection reports `starting` until then, so a lifecycle request sent after `ready` is not refused with `retry` by that watcher. |
| `session.stop` | Same as Start | `session` after the Incus stop and fresh observation; runtime filesystem is retained. Pending or confirmed attachments answer `conflict` with reason `attachment_active`. |
| `session.attach` | Same as Start | `token`, `expires_at`, and `spec` containing fixed `project`, `instance`, and `argv`. Starts a stopped runtime and waits for readiness. |

Session listing reads a complete registry page before inspecting its sessions.
An inspection reporting a missing record is omitted only after confirming that
the exact session UUID has been removed from the registry. Missing authority
for a surviving session and other inspection errors remain errors. The cursor
belongs to the original registry page and remains valid after such omissions.

Every operation and operation summary carries `intent`: what the change's
caller named, recorded when the daemon accepted it. It never holds a guard
read from live state, such as an expected tip, URL or commit, or a
confirmation token. Its fields, each present only when the kind has it, are
`project`; `session`, the target session or, for a creation, the new one;
`branch`, the P branch named or held by the session at acceptance;
`new_branch` for a rename; `choice`, `source` and `origin_ref` for a session
creation; `replaces_session` and `replaces_branch` for a replacement, naming
the retired creation and the branch it held; and `url` for a project created
with an origin. A client that repeats a change with the same key can compare
it without reading live state.

An operation that ends `blocked` or `failed` carries `reason`, the code of why
it stopped, beside `diagnostic`, its detail; any other operation carries no
reason. A superseded operation keeps a diagnostic naming the operation that
replaced or cleaned it up. The reason is one of the error reasons above, such as `runtime`,
`git`, `origin`, `plugin`, `changed` or `policy_invalid`, or one that only
operations record:

| `reason` | Meaning |
|---|---|
| `internal` | The operation's work failed in a way P did not classify, or panicked; the diagnostic holds the detail, and the daemon logs a panic's stack. A panicked operation is recorded `blocked`, keeps its guards and waits for `operation.retry`. |
| `outcome_unknown` | The operation issued an effect, such as an Incus delete or a Git ref change, and cannot tell whether it happened. P does not issue it again; the diagnostic names the resource to check before Retry. |
| `workspace_mismatch` | A Rename refused before any change: the workspace is not on its assigned branch, or its branch tracks another upstream. The diagnostic names the expected and actual values. |

The diagnostic holds detail only, never advice: clients say what to do from the
reason. Every diagnostic P records, an operation's, a session's start
diagnostic and each resource's in project deletion evidence, keeps at most
1024 bytes, cut at a character boundary, so a Rename mismatch keeps both
validated branch or upstream names. `operation.list` shows the same diagnostic
as `operation.inspect`. Operation lists still enforce the control-frame limit
and return a continuation when a page must be shortened.

The replacement methods implement a bounded subset of
[Try again with changes](session-lifecycle.md#failure-cancellation-and-retry).
They accept a blocked local creation in `source-ready` or `branch-assigned`
before UUID resources exist, or a base-image local creation in `principals-ready`
with its exact reviewed P Git key, active principal, and endpoint sockets present,
and affirmative durable `runtime_init_state:"not-attempted"` evidence. Ordinary
session creation records that state before the native boundary and changes it
to `attempted` immediately before dispatch through the trusted broker gate.
Dispatch provenance and approved cleanup completion are monotonic in SQLite;
a stale Retry/status update cannot erase either checkpoint. A missing runtime
after an attempted init cannot prove that a delayed request
will not materialize; replacement refuses it. Historical `principals-ready`
evidence without this tracked state is also ineligible. Exact Retry refuses a
second absent-runtime init after an uncertain attempt, while an observed exact
owned instance may continue without another init.
Builder tree/publication evidence, environment effects, and later phases are
outside this slice. Fresh full native inventory must positively prove both the
runtime UUID and creation builder absent; unavailable or ambiguous observations
are ineligible. An existing-branch failure may select the same existing branch. A
local-source new-branch failure may select an absent target with `choice:"new"`
from freshly observed committed local source, including the same desired name.
If the failed request's ref is present at its captured commit, the replacement
may explicitly select that same preserved branch with `choice:"existing"`.
A different absent target also leaves the old ref intact. Origin-backed
requests and existing targets unrelated to the failed assignment are outside
this slice.

`old_branch` and `new_branch` report the full `ref`, `observed`, `exists`, and
optional `oid`. `observed:false` means inspection was unavailable, not absence.
The selected source package applies ordinary Create's local committed-source
rules: branch selectors capture their current commit, and commit selectors
must be reachable from an ordinary P head. No missing objects are imported.
`provisional.assigned_ref` reports `absent`, `preserved_existing`, or
`preserved_created` after successful old-ref inspection. Runtime and builder
fields report `absent` only after the complete native UUID absence proof.
`runtime_local:"unavailable"` explicitly reports that no runtime-local workspace
or private state is available for inspection. Key, principal and endpoint fields
report `absent` for the no-resource path, or `present` with `provisional.cleanup`
for the reviewed local-resource path. That object names the old UUID, operation
and image fingerprint, the key's public fingerprint, and device/inode/owner/mode
identities for the private key directory/file, endpoint prefix/directory, and
exact `git.sock` and `session.sock` entries. It contains no private key bytes.
Wrong fingerprints, symlinks, unsafe metadata, extra principals or endpoint
contents are refused and preserved. Unavailable inspection or an unexpected old created tip yields an ineligible preview. Existing refs are
never deleted or reset by replacement, including when the old assignment moves
to another name. A new target must be unassigned and free of lifecycle guards;
the old reservation may be transferred to the same name.

The captured source, normalized policy, or request selection (branch, choice,
or local source selector) must have changed; changing only the key is
insufficient. The stored `ref_cas_intent` is a planned Git CAS, not evidence
that it ran. Replacement binds fresh ref facts and preserves any observed ref
without inferring cleanup authority from that marker. Beyond the reviewed
base-image `principals-ready` checkpoint, later phases, unexpected
UUID resources, attachments, active workers, and unavailable native observations
remain ineligible. This path does not review or clean uncertain runtime effects
or workspace data.

Preview is read-only. Ineligible previews return reasons without a token.
Eligible tokens expire after two minutes and are held by the issuing daemon;
restart invalidates an unconsumed token. A blocked early local request, including
base-image `principals-ready`, stays blocked across restart until explicit Retry or exact Create replay; startup
does not resume branch assignment or native creation. Established endpoint
consumers may be reopened during recovery. Confirmation rechecks the exact
reviewed request, source and target/old ref facts, policy, image, plugin selection,
old evidence, resource identity, and native absence. Stale facts or an expired
token answer `precondition_failed` with reason `changed` or `expired` without
superseding the old request. Successful confirmation atomically marks the old
operation `superseded`, releases its session assignment, and creates one new
UUID, operation, and immutable request with the new key before scheduling its
worker. For reviewed local resources the same transaction retires the exact old
Git principal and records a new `replacement-cleanup` phase. Its durable
`replacement_cleanup` evidence retains every reviewed old identity after the
old session row is retired. The new worker serializes with the old UUID and
rechecks full native absence before local cleanup and at each recovery boundary.
It removes only the reviewed key and exact endpoint entries, then atomically
records `replacement_cleanup.completed:true` and `source-ready`. It creates no
new UUID resource or native effect until that checkpoint. Partial cleanup may
resume with reviewed entries legitimately absent; substituted entries and newly
appearing native identities block with the old cleanup evidence retained.
Running cleanup recovers after restart; a blocked cleanup can resume by exact
Create replay or Retry of the same new operation. Evidence records
`supersedes_operation_id`, `supersedes_uuid`, and a
confirmation-token hash. Repeating an accepted confirmation with the same key,
old UUID, and token returns that operation, including after restart. Reusing
its key with different confirmation inputs answers `invalid` with reason
`key_reused`. Retrying the superseded operation answers `conflict` with reason
`operation_superseded`; exact replay of its original `session.create` key
returns the superseded operation without scheduling the old creation.

Confirmed failed-create cleanup is a separate fallback for a blocked, local
committed-source Create in `source-ready`, `branch-assigned`, or
`principals-ready`. It preserves the assigned P ref at its reviewed tip and
all other refs. It creates no new session or runtime. In addition to the early
paths above, it supports an immutable Nix selection/resolution failure when
`environment_builder` durably records a settled `absent` cycle: the worker
recorded its init attempt, positively observed the exact owned builder, removed
it, and verified full native absence. A captured tree with affirmative cycle-zero
`not-attempted` evidence also permits cleanup after a builder preflight failure.
The dispatch gate runs after confinement, storage and pinned-image preflight;
correcting such a failure permits exact Retry without a new request. An
attempted-but-unobserved builder, historical builder evidence without this
tracking, environment publication or
cache state and origin requests are ineligible. Later runtime/workspace phases
require the separate narrow assembled path below; they cannot use early absence
evidence. Early cleanup requires runtime initialization to affirmatively remain
`not-attempted`. Native
absence alone never settles a possibly delayed init.

Preview reports runtime-local state as `unavailable`, not clean or empty.
Keys/principals/endpoints must either be absent or match the same reviewed local
identity checks used by integrated replacement. Confirmation rechecks the
preview, including its ref tip, old evidence, principal, local entries and full
native inventory. Stale or unavailable facts answer `precondition_failed` with
reason `changed` (`expired` for the token) and preserve the old request; a Git
failure answers `failed`. Early-path accepted confirmation atomically supersedes the old Create,
records a forward cleanup operation and ref guard on its existing UUID, changes its
registry from `creating` to `removing`, and disables its Git principal and
session RPC authority. Durable evidence retains the reviewed old identities.

The cleanup worker serializes with creation on that UUID. It proves full native
absence before deleting only reviewed local entries, then records
`local-complete` before final revalidation and registry removal. This checkpoint
and its immutable review cannot regress through Retry. Partial cleanup permits
already removed reviewed entries; substituted entries or new native identities
block cleanup. Incus unavailability leaves the accepted operation incomplete,
with its identity, disabled authority and ref guard retained. Restart reconciles
forward; `operation.retry` resumes the same cleanup intent. No ref, shared image
or external mount content is removed. Only a completed `cleaned` operation
releases the assignment for a separate corrected `session.create` with a new
key and UUID. The superseded original Create remains inspectable and exact
replay cannot schedule its worker.

`session.create.cleanup.preview` takes only `{"uuid":"session-UUID"}`;
P selects the path from the creator's state. A creator blocked at an early
phase takes the early path above. Any other creator takes the narrow stopped
assembled path, which requires a blocked, committed, `assembly-ready` local
base-image creator with a standalone policy (network `none`, no filesystem
grants) and its exact stopped container. A creator outside that boundary is
refused with `unsafe_reasons`; an unavailable exact stopped container gives
`exact_stopped_runtime_unavailable: <error>`.

P does not look inside the container. The assembled preview's `runtime` has
`condition:"present"`, `incus_project`, `instance_name`, `incus_uuid`,
`generation`, `image_fingerprint`, `original_status:"Stopped"` and
`runtime_data_lost:true`: the container and everything in it are lost,
including private home and credential files, whose contents P neither
enumerates nor hashes. The preview also binds `old_request_sha256`, includes
the three `loss_warnings` texts, and preserves the assigned P ref.
Confirmation uses the same existing UUID/key/token shape. A durable precommit
cleanup guard keeps the original creator blocked while the worker rechecks the
reviewed facts. A changed container identity (Incus UUID, generation or image)
or a changed branch fails the cleanup at `stale` and releases its guards; the
container, the branch and the original creator remain. A new
preview/confirmation requires new keys.

Only after those facts match does cleanup atomically retire the original
creator/session authority and admit deletion of the exact native source.
Subsequent recovery proceeds toward verified runtime and local-resource absence.
Unknown deletion effects or an unavailable/competing native identity remain
guarded and incomplete; recovery cannot reissue a name-only deletion or forget
the identity. The selected assembled-cleanup integration gate is separate from
earlier no-init cleanup evidence. Other unsupported
failures preserve resources and explain the unresolved condition.
See the [CLI procedure](session-lifecycle.md#confirmed-failed-create-cleanup).

Each session view returns all four [public status facts](session-observability.md#status-model).
When a session was created with environment selection, its view also includes
`environment`: the captured `commit_oid`, `system`, `selection`, `cache`, optional
`environment_key`, `base_image_fingerprint`, and `image_fingerprint`.
Operation-list summaries expose the
same projection; `operation.inspect` returns the detailed durable evidence.
`selection` is `pending`, `base`, or `devShells.<system>.default`. `cache` is
`pending`, `none` for a base selection, `publishing`, `hit`, or `miss`.
`image_fingerprint` is creation's currently recorded runtime image, initially
the base and then the accepted environment image. These fields report
creation's recorded selection; they do not imply that
an image still exists in the cache or that the current workspace is unchanged.

`latest_unattended_condition` is `null` or an object with `condition`,
`adapter`, `adapter_version`, optional `source` and `reason`, plus daemon-assigned
`received_at` and `receive_sequence`. `attached_count` counts confirmed live
helper connections. The list methods may return fewer than the requested limit to
keep the encoded JSON-RPC response within 64 KiB; use the returned `next`
cursor until it is empty, including when a short page has a nonempty cursor.

Without `runtime.environment`, creation uses the pinned base image and refuses
a selected committed root `flake.nix` during first workspace initialization.
With environment selection, a trusted `p.workspace/v2` configuration binds the
accepted result to the captured commit. It permits a realized default devShell
or a valid flake without that default, and preserves the distinction from a
commit without a flake. Invalid defaults fail without fallback. The offline
public VM gate covers these selection and activation cases.
Retry does not delete the assigned branch or reset an unexpected workspace;
unrecognized workspace state leaves the creation blocked. `ready`
requires the Incus instance running and `p-interactive.service` observed
active and running. A running instance alone reports `starting` while
readiness is unresolved.

## Project services

The production terminal browser adds these host-only methods, served with the
`services` capability. Each request takes a session `uuid`. Private session RPC
does not expose them.

| Method | Additional params | Result |
|---|---|---|
| `session.services` | None | `uuid`, `observed_at`, and `services`, each with `name`, `description`, `state` and `detail`; an empty array means no units observed. |
| `session.service.action` | `name`, `action:"start"`, `"stop"` or `"restart"` | `uuid`, `name`, `observed_at`; inspect inventory for subsequent state. |
| `session.service.logs` | `name` | `uuid`, `name`, optional `logs` (omitted when empty), optional `logs_truncated`, `observed_at`. |

`state` is P's: `stopped` (no process: never started, stopped or exited),
`starting` (started, not ready yet), `running`, `stopping`, or `failed` (the
last run failed; it stays until the next start). `detail` is the runtime's own
words for the state, for display only; on Incus it is systemd's active and sub
state, such as `active (running)`, or `installed` for an installed unit that
the user manager has not loaded.

Only session-user `p-project-*.service` names are admitted. No host/system unit,
P infrastructure unit, arbitrary command, credential path, journal path, native
Incus target, or free-form systemctl action can be supplied. An established,
ready owned runtime is required.
The daemon holds its session/workspace guards, uses selected WASI
`runtime.services`, executes fixed commands under guest UID/GID 1000, and checks
native identity/generation before accepting results. An action holds the
session's lifecycle lock and answers `retry` with reason `session_held` while
another lifecycle request holds it. The inventory and logs reads answer
`retry` while a lifecycle request holds the lock, but never cause a `retry`
answer: a request that takes the lock waits for running reads to end. A session
that is not ready answers `conflict` with reason `session_not_running`. A
failure of the session's user service manager, including an action on a unit it
does not know, answers `failed` with reason `services`; a failure of Incus
answers `failed` with reason `runtime`. See the
[runtime service boundary](runtime-isolation.md#project-user-services).

Inventory combines loaded user units with installed user unit files, at most
64 distinct project units. An installed unit with no loaded observation is
`stopped` with detail `installed`: the user manager unloads only inactive
units. Unavailable or oversized inventory fails rather than returning a
misleading clean subset. Logs reads request at most 100 recent journal entries
and bound the encoded result; they may return a truncated tail. Actions are synchronous and not durable operation receipts;
after an uncertain transport outcome inspect the unit before repeating.

Origin-backed `session.create` additionally accepts optional
`expected_origin_url`, bound alongside `expected_commit_oid` to the source the
user reviewed. The TUI supplies it. A changed current URL refuses before fetch;
the SQLite reservation also rejects a captured URL mismatch. Exact replay keeps
the original request/URL and does not recontact a changed origin. Older callers
that omit this field retain their existing API behavior.

## Session removal preview

`session.removal.preview` is a host-only, read-only method. The current patch
passed focused tests, retained review, and selected VM30; see
[implementation progress](implementation-progress.md). It does not remove a
session or authorize removal by itself. Call it with:

```json
{"uuid":"session-UUID","kind":"discard"}
```

Use `kind:"delete"` to include branch-loss evidence. P does not inspect the
container before removal; there is no workspace loss inspection method. A
present runtime may be running or stopped; any other native status answers
`conflict` with reason `runtime_unsettled`. A pending or confirmed attachment
answers `conflict` with reason `attachment_active`. Another active change to
the session (discard, delete, rename or failed-create cleanup) answers `retry`
with reason `session_held` while it runs and `conflict` with reason
`operation_blocked` once it is blocked, both naming the operation.

When Incus authoritatively reports the runtime absent, pass
`"acknowledge_missing_runtime":true`. The result then has
`runtime.condition:"missing"` and `runtime.runtime_loss_unknown:true`. The
acknowledgement with a present runtime answers `precondition_failed` with
reason `changed`; a runtime that is missing and not acknowledged answers
`conflict` with reason `runtime_missing`. An unreachable Incus
authority does not qualify as an absent runtime. Missing-runtime
preview/confirmation and durable removal require a full confined-project
inventory with no matching session UUID. A renamed or competing instance
produces a refusal without a token or newly accepted action. The proof is
repeated before the authority commit and final row removal, including after a
present-runtime deletion.

The result is `{"preview":{...}}`. The preview identifies the session,
project, assigned ref and tip, policy, runtime identity or missing state, a
short-lived `confirmation_token`, and `expires_at`; the token expires after two
minutes. A present runtime has `runtime.condition:"present"`, `incus_uuid`,
`generation`, `image_fingerprint`, an informational `original_status`
(`Running` or `Stopped`) and `runtime_data_lost:true`: the container and
everything in it are lost, including uncommitted changes, files outside Git,
commits not pushed to P, the home directory and runtime-local credentials.
When the assigned P ref is missing, because it was deleted outside P or is a
blank project's unborn first branch, the preview omits the tip and has
`assigned_ref_missing:true`. Delete also includes `branch_loss`: the
`assigned_ref`, its `assigned_tip` when present, the
`commits_losing_p_reachability` (empty for a missing ref) and the captured
`p_refs`. Discard and delete contact and compare no origin. Discard passed
selected VM31 and Delete passed selected VM32, using dummy Codex credential
files without authentication.

`session.discard` accepts
`{"key":"idempotency-key","uuid":"session-UUID","confirmation_token":"32-hex-token"}`
and returns `{"operation":{...}}`. It accepts only a Discard preview.
Before the removal commit point, a changed assigned tip, a replaced container
(changed Incus UUID or generation) or a reappearing missing runtime
fails the operation at `stale`, releases its guard, and leaves the branch and
session intact. A Start or Stop between preview and
confirmation does not make it stale. A valid confirmation commits removal,
disables session authority, force-stops a running container, removes the exact
owned runtime and P-owned endpoint/key, and ends the assignment while retaining
the confirmed P branch. With a missing assigned ref there is no branch to
retain, so Discard ends like a Delete. A revoked or missing Git principal
registration and a missing host key file do not stop removal; a host key file
with an unexpected owner, mode or link count still blocks the key removal.
While the operation runs, Start, Stop, Attach and service requests answer
`retry` naming it, and `conflict` with reason `operation_blocked` once it is
blocked. The same key and request replay the operation. An uncertain native
stop/delete stays guarded and blocked for investigation; P does not retry a
name-targeted request against a possible replacement.

`session.delete` accepts
`{"key":"idempotency-key","uuid":"session-UUID","confirmation_token":"32-hex-token"}`
and returns `{"operation":{...}}`. It accepts only a Delete preview.
The same pre-commit stale checks apply, and a changed `branch_loss` review is
also stale. The exact runtime, endpoint, and key cleanup is the same. After
runtime absence, it removes only the assigned P branch through an
expected-old-tip check; sibling P refs and origin refs remain. With a missing
assigned ref it has nothing to remove in P's repository. A mismatched or
uncertain branch effect stays guarded for investigation. The same key and
request replay the durable operation.

`session.rename` accepts
`{"key":"idempotency-key","uuid":"session-UUID","new_branch":"renamed","expected_old_tip":"<40-hex P commit>"}`
and returns `{"operation":{...}}`. The established session must be
stopped, with the exact nonempty assigned P tip, an ordinary local Git branch,
and its exact stopped runtime; a session in any other status answers `conflict`
with reason `session_not_stopped`, and a moved P tip answers
`precondition_failed` with reason `changed`.
The new branch must pass Git validation and be absent in P and the local
workspace. The durable operation guards both P ref names, prepares the
workspace change on the stopped runtime, and creates the new P ref at the
expected old tip as its commit point. It then moves the local branch without
changing its tip or files, changes the assignment and principal policy, and
deletes the old P ref by expected-tip check. P never freezes the runtime.
Start is refused while the rename runs, and a session started between the
request and the work fails as stale before any effect. Local commits ahead of
P, untracked files, private session state, and sibling refs remain. The same
key and exact request replay the operation. A proven refusal before any
effect may fail and release the guards; an uncertain Incus or Git effect
remains blocked for exact recovery or investigation. The current supported
workspace layout is a single ordinary worktree with loose branch refs; other
layouts return unavailable before the ref commit point.

## Aggregate project deletion

The implemented aggregate boundary requires at most four established, stopped,
detached sessions. Project deletion does not stop sessions itself: an
attached, starting or running session answers `conflict` (reason
`attachment_active` or `session_not_stopped`) or `retry` (`session_held`),
naming the session. P does not look inside
the containers. Each present session's `sessions[].runtime` has
`condition:"present"`, `incus_project`, `instance_name`, `incus_uuid`,
`generation`, `image_fingerprint`, `original_status:"Stopped"` and
`runtime_data_lost:true`. List each positively missing session UUID in
`acknowledge_missing`: the preview explicitly reports unknown runtime loss and
still requires full native absence proof. Listing a present session answers
`precondition_failed` with reason `changed`; listing a UUID that is not a
missing session of the project answers `invalid` with field
`acknowledge_missing`. Unknown native identity or unavailable Incus cannot be
acknowledged into deletion authority.

At most twenty indexed, exactly owned environment images and bounded Git/history
and report inventories are accepted. The preview covers every assigned and
retained P ref and the union of commits losing P reachability. It contacts no
origin, and its `branch_loss` has no origin evidence. Its expiry is at most two
minutes. Changed facts or an expired preview need a new preview/token/key.
Refused preparation directs the caller to Stop/detach, supported
failed-creation cleanup or identity investigation; P does not delete uncertain
resources.

After reviewing the complete preview, call `project.delete.confirm` with its
token and a new key. A fresh preview must match the reviewed one in full
before P atomically sets the project to `deleting`, sessions to `removing`,
and principals inactive. The call returns the operation at once. Poll
`operation.inspect`; its incomplete evidence contains
independent `runtime`, `local`, `image` and `repository` resources with `remaining`,
`unreachable`, `deleted` or `already_absent` status. Existing `operation.retry`
resumes the same confirmed outcome; it does not grant replacement machinery.

An issued source DELETE is never dispatched again. Available name and full
inventory checks may positively prove absence after a crash; a still-present,
renamed, competing or uncertain source keeps cleanup blocked for investigation.
Owned indexed images are deleted only after runtime absence. The repository and
registry disappear last. Shared/base images, other projects, external grant
contents and delivered logs remain. Completion clears bulky claims and old raw
requests/evidence, retaining a minimal completed result and immutable retired
key/operation-ID/kind/request-digest receipts. Replaying old Create, origin or
publication keys refuses instead of recreating deleted authority.

## Terminal attachment

Run `p attach SESSION_UUID` on the P host using the configured control socket,
or `p attach /absolute/control.sock SESSION_UUID` to select it explicitly.
Run it directly or inside a client-initiated SSH terminal. The CLI restores local
terminal settings on exit and forwards terminal size changes.

`session.attach` returns a 30-second, one-use pending token after the selected
runtime WASI capability and native checks approve the fixed
`/usr/libexec/p/attach` command. The public spec grants no general Incus access.
The CLI transfers the token to a separate temporary `p attach-helper` process
through an inherited private socket; the token never enters argv.

The helper opens its own connection and agrees the protocol with
`system.hello` before claiming. The daemon admits `attachment.claim` only from
a Unix peer running the exact
same executable in its fixed helper mode. The dedicated connection can then
call only `attachment.confirm` and `attachment.ping`; any other method except
`rpc.cancel`, including another hello, answers `invalid_request` with reason
`connection`. Ordinary API callers
cannot assert presence. Claim takes `{"token":"…"}` and returns the native
launch details privately as `socket` and `spec`. `attachment.confirm` takes
`{"operation":"/1.0/operations/<UUID>"}`, `attachment.ping` takes `{}`, and
both return `{}`.
Confirmation follows successful native PTY establishment, and repeats current
ownership, readiness, and fixed asset checks. Tokens cannot be replayed or
reconfirmed. The daemon permits at most eight pending/active attachments per
session and 128 overall. Claimed pending state remains reserved through failed
launch teardown even if its confirmation deadline expires.

The helper owns Incus's data and control WebSockets and monitors the lease
connection continuously. For the pinned Incus 7.4 implementation, a matching
control ping/pong proves that `instance.Exec` has succeeded: Incus starts its
control reader only after that call returns. Before connecting the exec sockets,
the helper opens Incus's native operation
wait and receives its flushed response headers. It retains that completion
witness even after Incus expires the operation record. Confirmation also checks
the operation's running state, interactive flag, exact command, and session
scope in the daemon. A completed native operation before confirmation is
rejected. Terminal bytes do not provide readiness or
semantic status. Client/carrier loss closes the control WebSocket, which makes
Incus kill only the temporary exec command. The helper waits for native
operation completion before closing a reachable lease. Daemon connection loss
starts teardown immediately and permits no re-registration. Attachment lifetime
and unattended reduction remain owned by [session observability](session-observability.md).

## Errors

An error answers with a kind that names the client's next step and a reason
code, never with text. The daemon logs the cause it mapped, with the method
and request ID, so the daemon's log shows what the answer leaves out. Clients
write their own sentence for each kind and reason; P's TUI and CLI keep them in
`internal/errtext`.

```text
← {"jsonrpc":"2.0","id":7,"error":{"code":-32010,"message":"retry","data":{"reason":"session_held","target":"operation/9b0e…"}}}
```

The error object has JSON-RPC's `code` and `message`; `message` is the kind's
name, an identifier rather than a sentence. `data` is present whenever the
error has a reason:

| Field | Meaning |
|---|---|
| `reason` | A stable snake_case code from the tables below. A client that meets a reason it does not know uses its sentence for the kind. |
| `target` | What the error is about, when it names one: `session/<uuid>`, `operation/<id>`, `project/<path>` or `project/<path>:branch/<name>`. A project path never contains `:`, so the first `:` ends it. |
| `field` | For `invalid`, the request field at fault when one is. |
| `supported` | For `unsupported_version`, the protocols the daemon speaks. |

Invalid request IDs are returned as JSON `null`.

### Kinds

| Kind | Code | The client's next step |
|---|---|---|
| `parse_error` | -32700 | Fix the client: the line is not JSON, not UTF-8, or not a complete frame of at most 64 KiB. |
| `invalid_request` | -32600 | Fix the client: the line is not a valid request for this connection. |
| `method_not_found` | -32601 | The protocol has no such method. |
| `invalid` | -32602 | Fix the params. |
| `internal` | -32603 | Nothing to do but report it; the daemon logged the cause. |
| `cancelled` | -32001 | Nothing: the client cancelled the request or closed the connection. |
| `unsupported_version` | -32002 | Use a client that speaks a protocol in `supported`. |
| `retry` | -32010 | Send the same request again; when `target` names an operation, after it leaves `running`. |
| `conflict` | -32011 | Change the named state first: detach, stop or start the session, retry the blocked operation, or wait for a person. Sending the same request again fails the same way until then. |
| `capacity` | -32012 | Free a slot: discard or delete a session. |
| `precondition_failed` | -32013 | Read the state again and decide again: for a confirmation, a new preview. |
| `precondition_required` | -32014 | Send the precondition the change needs. No method answers it yet. |
| `not_found` | -32015 | Stop: the named record does not exist. |
| `not_configured` | -32016 | Configure the named capability on the host. |
| `failed` | -32017 | Check the named authority, then send the request again. |

The codes `-32003` and `-32004` are not used: a client that still tests them
fails instead of matching a kind with another meaning.

A request whose handler panics answers `internal` with reason `panic`; a
notification gets no response. The daemon logs the panic with its stack and
the request ID and keeps serving: the connection stays open for later
requests, and other connections are not affected. The private session RPC
behaves the same way. A panic in an operation's work is recovered too: the
operation is recorded `blocked` with reason `internal`, keeps its guards and
waits for `operation.retry`; the scheduler does not run it again.

Once a request's context ends, an error its handler returns answers as the
context's: `cancelled` when the client cancelled it or closed the connection,
and `retry` with reason `deadline` when the daemon's 30-second deadline
passed. A result the handler returned is still sent. Sending the same request
again after `deadline` is safe: a change recorded before the deadline replays
through its idempotency key. When the daemon stops while a request records an
operation, the request still returns the operation, and the next start resumes
it.

### Reasons

| Kind | Reason | Meaning |
|---|---|---|
| `invalid_request` | `hello_required` | The connection sent a request before a successful `system.hello`. |
| `invalid_request` | `duplicate_id` | Another request with the same `id` is in flight on the connection. |
| `invalid_request` | `envelope` | The line is JSON but not a valid request object: a member other than `jsonrpc`, `id`, `method` and `params`, a repeated member, an `id` that is not a string of at most 128 bytes or an integer, or a missing `method`. On the session RPC, also a `status.report` with an `id`. |
| `invalid_request` | `connection` | This connection may not call the method: the attachment helper's lease connection calls anything but `attachment.confirm` and `attachment.ping`, or a connection other than the trusted helper's calls `attachment.claim`. |
| `unsupported_version` | `protocol` | The hello named a protocol the daemon does not speak; `supported` lists the ones it does. |
| `unsupported_version` | `jsonrpc` | The request's `jsonrpc` member is not `"2.0"`. |
| `invalid` | `param` | A param is missing, unknown, of the wrong type or out of bounds; `field` names it when one does. |
| `invalid` | `key_reused` | The idempotency `key` is recorded for another request, or was retired with a deleted project. |
| `internal` | `panic` | The daemon's code panicked. |
| `internal` | `store` | The control store failed. |
| `internal` | `encoding` | The result could not be encoded, or the handler returned both or neither of a result and an error. |
| `internal` | `unexpected` | A failure the daemon did not classify. |
| `retry` | `session_held` | Another request or a running operation holds the session; `target` names the operation when there is one. |
| `retry` | `too_many_requests` | The connection has 16 requests in flight. |
| `retry` | `deadline` | The daemon's 30-second deadline passed. |
| `retry` | `project_held` | Another request or a running operation holds the project: another creation of it, a deletion review or confirmation, or any running operation in it for `project.delete.preview`. `target` names the operation when there is one. |
| `retry` | `refs_moved` | P's branches moved while the daemon read them. |
| `retry` | `too_many_previews` | The daemon holds its limit of unexpired reviews of that kind; one expires within two minutes. |
| `conflict` | `attachment_active` | A terminal is attached to the session, or an attachment is pending. |
| `conflict` | `session_not_established` | The session is still being created, is being removed, or was never created. |
| `conflict` | `session_not_stopped` | The change needs a stopped session: Rename, and project deletion for each of its sessions. |
| `conflict` | `session_not_running` | The request needs a ready session: attachment and project services. |
| `conflict` | `runtime_missing` | The session's container does not exist; for a removal, acknowledge it in the preview. |
| `conflict` | `runtime_unsettled` | The session's container is neither running nor stopped. |
| `conflict` | `runtime_present` | A container of the session exists where the request expected none, such as a missing runtime acknowledged in a preview. |
| `conflict` | `operation_blocked` | A blocked operation holds the target and waits for `operation.retry`; `target` names it. |
| `conflict` | `operation_superseded` | `operation.retry` of an operation that a replacement or cleanup superseded. |
| `conflict` | `operation_not_retryable` | `operation.retry` of an operation that cannot run again, such as a cleanup that found its review stale. |
| `conflict` | `operation_unfinished` | An unfinished origin change, publication or branch guard holds the project. |
| `conflict` | `policy_invalid` | The project's policy no longer holds on this host: its stored snapshot changed, a filesystem grant changed, or the public egress configuration changed. |
| `conflict` | `principal_missing` | The session's Git identity or key is missing, revoked or changed; discard the session and create a new one. |
| `conflict` | `public_egress_unassigned` | A session on the public network holds no address slot. |
| `conflict` | `branch_exists` | The branch name is already taken. |
| `conflict` | `branch_assigned` | A session holds the branch. |
| `conflict` | `branch_reserved` | An unfinished lifecycle operation reserves the branch. |
| `conflict` | `project_exists` | The project already exists. |
| `conflict` | `project_deleting` | The project is being deleted. |
| `conflict` | `origin_not_set` | The project has no origin. |
| `conflict` | `cleanup_active` | A failed creation's assembled cleanup holds the session. |
| `conflict` | `bound_exceeded` | The answer would exceed the response bound, such as a removal or project deletion preview with too many facts. |
| `capacity` | `session_capacity` | Every session slot is taken. Discard or Delete a session to free one; Stop keeps its slot. The [runtime contract](runtime-isolation.md#incus-boundary) owns capacity accounting. |
| `capacity` | `attachment_capacity` | Every pending or active attachment slot is taken: eight per session, 128 in all. |
| `capacity` | `public_address_slots` | Every public egress address is assigned. |
| `precondition_failed` | `expired` | The confirmation or attachment token expired, was already used, or is unknown, for example after a daemon restart. |
| `precondition_failed` | `changed` | What the request reviewed or expected no longer holds: the facts behind a confirmation token, an `expected_old_tip`, `expected_url`, `expected_origin_url`, `expected_commit_oid` or `source_oid`, or a token issued for another target. |
| `not_found` | `session` | The session does not exist; `target` names it. |
| `not_found` | `operation` | The operation does not exist. |
| `not_found` | `project` | The project does not exist. |
| `not_found` | `branch` | The P branch does not exist. |
| `not_found` | `source` | The source ref or commit to create from does not exist, is unborn, or is not reachable from a P branch. |
| `not_configured` | `git`, `runtime`, `services` | The capability is not configured; see the [connection agreement](#connection-agreement). |
| `not_configured` | `public_egress` | The session's policy needs public egress, which this host does not configure. |
| `not_configured` | `project_policy` | The host configuration has no policy for the project. |
| `failed` | `runtime` | Incus or the runtime plugin failed or could not be observed. |
| `failed` | `services` | The session's user service manager is unavailable or refused the command, for example for a unit it does not know. |
| `failed` | `git` | P's Git repository or the source-Git plugin failed. |
| `failed` | `origin` | The origin could not be contacted, or refused an observation, fetch or push. |
| `failed` | `plugin` | A selected plugin failed its recheck before use. |

### Capability boundary

Without trusted runtime configuration, lifecycle mutations and inspection
are not served: each method of the protocol whose capability is not
configured answers `not_configured` with the capability as its reason, and
any other name answers `method_not_found`.
P has no session repair methods; [session lifecycle](session-lifecycle.md#inconsistent-sessions)
describes what to do instead. Branch/upstream mismatch correction is manual,
with P rechecking on the next attempt. Abandonment and its associated
tombstone/orphan-cleanup/forget workflow are outside MVP. `project.branches`
and `project.retained_branches` observe Git refs through the configured source
package; they do not mutate lifecycle state.
