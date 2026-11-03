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
{"schema":"p.host/v1","state_dir":"/absolute/private/p-state","git":{"activation_path":"/absolute/private/activation.json","source_plugin_id":"org.p.git","listen":"127.0.0.1:2222","driver":{"name":"git","config":{}}}}
```

The activation file must be an owner-owned private regular file under trusted
ancestors. Its selected source package must have the named ID, `source-git`
capability, WASI command runtime, and exact `git.project` grant. The approved
package digest is checked during loading and again at each invocation. Changes
to the activation file take effect after daemon restart. `listen` is a numeric
loopback TCP address with an explicit port; port `0` asks the OS for a free
loopback port and `system.inspect` reports the actual endpoint.

`git.driver` names the source driver P uses and carries that driver's own
settings in `config`, which only the driver reads. P has one driver, `git`,
built into P, which keeps P's repositories in the state directory; its
`config` is an empty object, and the driver refuses any setting. Loading the
host configuration checks only that the driver is `git` and that `config` is
an object. At daemon start the driver refuses unknown settings and checks
that its storage is private to the daemon user and that Git runs.

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
  "git": {"activation_path":"/absolute/private/git-activation.json","source_plugin_id":"org.p.git","listen":"127.0.0.1:2222","driver":{"name":"git","config":{}}},
  "runtime": {
    "activation_path": "/absolute/private/runtime-activation.json",
    "host_plugin_id": "org.p.tmux-host",
    "driver": {
      "name": "incus",
      "config": {
        "binary": "/absolute/trusted/incus",
        "user_socket": "/absolute/confined/unix.socket.user",
        "project": "p-confined"
      }
    },
    "endpoint_prefix": "/absolute/private/p-endpoints",
    "disk_source_ceilings": ["/absolute/private/p-endpoints", "/absolute/private/p-grants"],
    "base_image_fingerprint": "<64 lowercase hex digits>",
    "project_policy": {"network":"none","filesystem_mounts":[{"name":"data","source":"/absolute/private/p-grants/data","type":"directory","access":"read-only","executable":false}],"command":["/run/current-system/sw/bin/bash"]}
  }
}
```

`runtime.driver` names the runtime driver P uses and carries that driver's own
settings in `config`, which only the driver reads. P has one driver, `incus`,
built into P. Its `config` holds exactly three settings: `binary`, the absolute
path of the trusted Incus client; `user_socket`, the absolute path of the
confined `unix.socket.user` that P connects through; and `project`, the
confined Incus project that holds P's containers. Loading the host
configuration checks only that the driver is `incus` and that `config` is an
object. At daemon start the Incus driver refuses unknown or missing settings
and paths that are not absolute and clean, checks the client, socket and
project on the live host, and refuses a disk-source ceiling at, above or below
the user socket, so no grant can reach it.

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
image builders get the same network, whatever the project's policy
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

An optional `runtime.environment` object turns on commit images and selects
the system and restricted builder pool their builds use. A session then starts
from the image of its source commit, which holds the commit's devShell and a
clone of the commit; the runtime driver builds a missing one
([environment building](environment-building.md)). VM evidence is recorded in
[implementation progress](implementation-progress.md).

```json
{"system":"x86_64-linux","builder_storage_pool":"p-builders"}
```

The system is explicitly `x86_64-linux` or `aarch64-linux` and must match the
observed Incus host and pinned base architecture before selection. Current VM
evidence covers x86_64; aarch64 support still requires its corresponding
validation. The builder pool must satisfy the Incus driver's btrfs quota
checks.
With `public_egress` configured, the builder gets the public-egress network
and the public binary cache, using its session's address slot while it builds
([environment building](environment-building.md#security-boundary));
without it, the builder gets no NIC or substituters. It never gets a host
store, filesystem grants, or credentials.

Creation records the system, base image, builder pool and policy, the
driver's builder version (`image_builder`), and the captured commit before
effects. Retry requires the same selection and never recaptures a moving
branch or origin. A creation whose recorded selection no longer matches the
host blocks with reason `image_builder_changed`, a failed build with
`image_build_failed`, and a build whose outcome is unknown with
`outcome_unknown`. A build whose outcome is unknown is settled against its
durable attempt before another is sent, and every new attempt gets a new build
reference. A build settled as not accepted is recorded `abandoned` before its
leftovers are deleted and `absent` after; recovery from `abandoned` deletes the
leftovers and never settles that build again. Concurrent creations from one
commit build at most one image: the later one reuses the image the first
indexed. VM scenarios 25 and 26 cover commit images in the daemon.
Without `runtime.environment`, sessions use the pinned base image and the
runtime kit clones the workspace from P at first start.

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

The CLI's task commands, such as `p session create` and `p operation wait`,
are the clients for ordinary work; `p help` lists them and the
[user guide](user-guide.md) walks through them. Each opens a connection per
call, sends the hello, and calls through the request types, so a request's
`Validate` refuses bad arguments before anything is sent. A change makes its
own idempotency key, `cli-` and 32 hex digits, and `--key KEY` repeats a
change whose reply was lost: the command first pages `operation.list` for the
operation that key recorded and, when its `kind` and `intent` match the
command, reports it without sending anything. A change waits for its
operation with `operation.wait`, asking for 20 seconds within a 25-second
budget for the whole call; any other call has 35 seconds, the request
deadline and time to answer. A command repeats a call that answered `retry`
only where a second call cannot act twice: after `session_held` or
`project_held` naming an operation, once that operation leaves `running`;
after any other `retry` refusal, for up to 10 seconds; and after
`deadline`, for reads, keyed changes, start and stop, never for a service
action or `operation.retry`. Their exit status is 0 done, 1 failed, 2 invalid
arguments, 3 not run, nothing changed, and 4 not finished.

The commands for the seven [risky changes](#risky-changes) ask first:
`p session discard`, `delete` and `cleanup`, `p session create --replace`,
`p project delete`, `p branch delete` and `p origin publish`. Each makes the
dry run, prints the plan on standard error with a sentence per refusal, what
is already gone and a warning listing every loss item, and only when the plan
is allowed asks `[y/N]` on a terminal. The run repeats the dry run's params
with a new key and the change tag; a run refused with `precondition_failed`
prints the new plan and asks again. `--force` runs without asking, after a
10-second countdown when the plan's `loss` is not empty, and stops at a
refused run; so does a run under `--key`, so one key never goes with two
tags. `--dry-run` prints the plan and its change tag, and `--if-match TAG`
runs the plan that tag names with no dry run. Without a terminal and without
`--force`, a command prints the plan and exits 3.

`p api [socket-path] <method> [json-params]` stays for protocol work: it
makes no keys, does not wait, and repeats nothing. It connects, sends
`system.hello` itself, then sends one request and writes that request's full
JSON response envelope to stdout; the envelope's `id` is `2`. `json-params`
defaults to `{}`, or to `{"protocol":"p.control/v2"}` for `system.hello`, so
`p api system.hello` prints the daemon's hello result. An RPC error exits
nonzero. A daemon that refuses `p api`'s own hello has its error object
written as `{"error":{…}}`; any other connection failure writes
`{"error":{"message":"transport","detail":"<text>"}}`. Both exit 1, and a
command line it cannot use exits 2.

The client commands select their control socket in this order: `--socket`,
or for `p api`, `p tui` and `p attach` an explicit positional socket
argument, then a nonempty `P_SOCKET` environment variable, then
`/var/lib/p/control.sock`. An empty or
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
| `git` | The host configuration has a `git` object | `project.list`, `project.branches`, `project.retained_branches`, the five `origin.` methods, and the operation methods `operation.inspect`, `operation.list`, `operation.wait` and `operation.retry` |
| `runtime` | The host configuration also has a `runtime` object, which requires `git` | Project and session creation; session inspection, listing, Start, Stop, attachment, removal and rename; failed-creation replacement and cleanup; retained-branch rename and deletion; project deletion; the attachment helper's methods |
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

The TUI and the task commands connect with `api.Dial`, which sends the hello;
the attachment helper sends its own hello on its lease connection. These clients
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
association and observation. The origin methods, `project.retained_branches`
and the operation methods (`operation.inspect`, `operation.list`,
`operation.wait`, `operation.retry`) need only the `git` capability, so they
are available even when runtime configuration is absent: a daemon without a
runtime runs the journal's origin operations. The retained-branch
rename and deletion and the project deletion methods in this table need the
`runtime` capability. Every request rejects unknown or duplicate fields.
`origin.publish`, `project.retained.delete` and `project.delete` are
[risky changes](#risky-changes): each is called twice, first as a dry run that
returns a plan and its change tag, then with `key` and `if_match` to run it.
Their result is a `ChangeResult`: `plan` and `change_tag` for a dry run,
`operation` for a run.

| Method | Params | Result |
|---|---|---|
| `origin.change` | `{"key":"idempotency-key","project":"team/app","kind":"set","expected_url":"","url":"ssh://host/repo"}`; use `kind:"remove"`, the current `expected_url`, and omit `url` to remove | `operation` of kind `origin.change`, returned at once. Its worker contacts the proposed origin of a set before it commits; a remove makes the project local-only. The old URL is an exact compare-and-swap precondition. Read the resulting state with `origin.inspect` once the operation completes. |
| `origin.refresh` | `{"project":"team/app"}` | current `origin` state after contact. Failed contact returns `status:"unknown"` and retains the prior completed observation as stale presentation data. |
| `origin.inspect` | Same as refresh | current `origin` state without contact. |
| `origin.sources` | `{"project":"team/app","after":"optional-ref","limit":1..16}` | `project`, `origin_url`, `status`, `observation_status`, `observed_at`, `refs` (`ref`, `oid`, `commit_oid`), `next`. Refs are bytewise ordered and paged; `next` is empty at the end. |
| `origin.publish` | `{"project":"team/app","expected_origin_url":"ssh://host/repo","kind":"session","source":"session-UUID","source_oid":"<exact P tip>","destination_ref":"refs/heads/main"}`; `kind:"retained"` uses a P branch name in `source`. The run adds `"key":"idempotency-key","if_match":"<change tag>"` | Dry run: `plan` on target `project/<path>:origin-branch/<name>`, whose `details` hold the project, origin URL, source kind/identity/ref/OID, destination ref and observed OID or absence, `relation`, and `workspace_status:"unknown"`, and `change_tag`. It contacts the origin afresh. A `divergent` relation refuses the plan with `destination_divergent`. Run: `operation` of kind `origin.publish`, returned at once. Its worker performs its own fresh refresh and comparison; only `absent` or `fast_forward` can issue one ordinary push. The operation's evidence holds `publication`: the plan's details as `preview`, and `status` (`created`, `advanced`, `satisfied`, `refused` when the origin's server rejected the push, or `outcome_unknown`). |
| `project.retained_branches` | `{"project":"team/app","after":"optional-full-P-head-ref","limit":1..8}` | `branches` with `branch`, full `ref`, and tip `oid`, plus `next`. The cursor traverses P Git refs, so a page may be empty while `next` is nonempty if its refs are assigned to live sessions. |
| `project.retained.rename` | `{"key":"idempotency-key","project":"team/app","old_branch":"saved","new_branch":"archive","expected_old_tip":"<exact P commit OID>"}` | `operation` (`kind:"project.retained.rename"`). The source must be an unassigned retained P branch at that exact commit, and the destination must be absent and unassigned. A completed key replays; changed inputs with the same key answer `invalid` with reason `key_reused`. |
| `project.retained.delete` | `{"project":"team/app","branch":"saved"}`; the run adds `key` and `if_match` | Dry run: `plan` on target `project/<path>:branch/<name>`, whose `details` hold the `project`, `branch`, unassigned P `ref` and `tip`, and `branch_loss` (commits losing all P reachability and the complete P refs), and `change_tag`. It reads only P's Git repository and contacts no origin. A branch a session holds refuses the plan with `branch_assigned`, and one an unfinished operation guards with `branch_reserved`. Run: `operation` (`kind:"project.retained.delete"`). A completed exact run replays; the same key with other params or another tag answers `invalid` with reason `key_reused`. |
| `project.delete` | `{"project":"team/app"}`; the run adds `key` and `if_match` | Dry run: one `plan` on target `project/<path>` for whole-project deletion, including sessions, runtime loss by category, all P branch loss, and exact credential, repository and image identities and preserved external mounts in its `details`, and `change_tag`. It contacts no origin. Run: durable `operation` (`kind:"project.delete"`), returned at once. A completed exact run replays without deleting again. See [project deletion](#project-deletion). |

An origin state has `project`, optional `url`, `status` (`local-only`, `fresh`,
or `unknown`), optional `observed_at`, `ref_count`, and optional bounded
`diagnostic`. `origin.sources` labels retained refs `stale` when current status
is `unknown`; they cannot authorize source selection or publication. An
`origin.change` key replays its operation without contacting the origin.
Reusing a key with different inputs answers `invalid` with reason
`key_reused`, and an `expected_url` that no longer holds answers
`precondition_failed` with reason `changed` and records nothing. A failed
contact ends the operation `failed` with reason `origin`; an origin changed
between the request and its worker ends it `failed` with reason `changed`.
A failed origin change is not retried: send a new request with a new key. A
change interrupted by a daemon restart runs again at start, with the same
contact and compare-and-swap. Origin URL and SSH credentials stay on the
host.

Retained branch rename guards both P names while it creates the destination
with an absent-ref compare-and-swap and deletes only the exact old source tip.
The durable phases are `reserved`, `new-ref-create-issued`, `new-ref-created`,
`old-ref-delete-issued`, `old-ref-deleted`, and `completed`. A stale input
before the first issued effect fails as `stale` and releases both guards. An
uncertain issued effect stays blocked and guarded until exact positive
reconciliation; retry never repeats an uncertain Git command. The operation
does not change session assignment, runtime, credential, or origin refs.

Retained branch deletion is Git-only and contacts no origin: the commits that
leave P's repository are certain `p_commits` loss whether or not the origin
has them. A branch assigned to a session refuses the plan with
`branch_assigned`; a missing branch answers `not_found` with reason `branch`.
The run plans again under ref authority, then reserves the unassigned source
branch (phase `guarded`). Immediately before the selected Git effect, the
worker rechecks the tip and the branch loss and records `ref-delete-issued`;
only an exact old-tip compare-and-swap may delete the P ref. Changed facts fail
the operation `stale` before that marker. An uncertain issued deletion remains
guarded and is never reissued; exact ref absence permits completion. It does
not delete origin refs, sibling refs, sessions, runtimes, images, or
credentials.

Publication requires an established session with no unfinished operation or
ref guard, or a retained branch with no live session assignment. The exact
`source_oid` must still be that P branch's tip, and the project's origin must
still be `expected_origin_url`. Both calls check these facts: an origin URL
that changed answers `precondition_failed` with reason `changed` before any
contact, a source tip that moved answers the same once the origin is
observed, and neither records anything. A
local-only project refuses the plan with `origin_not_set`. A destination that
has commits the source lacks refuses the plan with `destination_divergent`, so
the dry run returns no change tag and nothing can run. The run contacts the
origin again while it plans again, and records the operation only if that
gives the same change tag. The URL is persisted with the request key; reusing
a key with another URL or tag answers `invalid` with reason `key_reused`,
including after restart. A session's publication holds the session while it
runs. Its worker rechecks the facts under the project origin lock and excludes
assignment/ref mutations through the shared Git authority lock; one that no
longer holds ends the operation `failed` with its reason. A destination that
diverged after the run was accepted ends it `failed` with reason `changed`,
without a push. Publication keys share one namespace with every other
operation's key. A key replays its operation without contacting Git.
Before the push, the operation records phase `push-issued`. A push whose
outcome is unknown, or a daemon that stopped after `push-issued`, ends the
operation `failed` with reason `outcome_unknown`, and it never starts another
push; a push that failed before starting ends it `failed` with reason
`origin`, its evidence holding the plan's details and no status. Neither is
retried: a new dry run and a run with a new key perform another fresh
comparison. Runtime workspace divergence is currently unobservable at this API
boundary, so the plan reports `workspace_status:"unknown"`; only the committed
P tip is eligible for publication.

With trusted runtime configuration, the following lifecycle subset is also
available. A create response returns a durable operation immediately. The
daemon continues creation under its own context. Clients follow the
operation with `operation.wait` until it is no longer `running`.
`operation.retry` resumes the
same captured request, UUID, source commit, policy, and selected plugin
digests. It reuses the recorded image when available. If that image is gone
before an instance exists, Retry rebuilds it from the same captured commit; an
uncertain instance or build cannot trigger blind recreation. A changed
request needs a new idempotency key. Reusing a blocked creation's assigned
branch requires the explicit replacement below, or supported confirmed
failed-create cleanup followed by a separate Create. Both are
[risky changes](#risky-changes), as are `session.discard` and `session.delete`:
a dry run returns their plan and change tag, and a run with `key` and
`if_match` returns the operation.

| Method | Params | Result |
|---|---|---|
| `project.create` | `{"key":"idempotency-key","project":"team/app"}`; optionally include `"url":"ssh://host/repo"` for origin mode | `operation`; blank mode reserves unborn `main`. Origin mode contacts the URL before committing the project and observation; an empty origin also reserves unborn `main`. |
| `session.create` | `{"key":"idempotency-key","project":"team/app","branch":"work","choice":"existing"}` or `choice:"new"` with `source:"refs/heads/main"` or a committed object ID; origin mode uses `choice:"new"`, `origin_ref:"refs/heads/main"` or a tag, and `expected_commit_oid:"<observed commit>"` with no `source` | `operation`; origin mode requires a fresh observation and fetch, then captures the exact commit and origin identity for Retry. Blank session creation is unavailable. |
| `session.create.replace` | `{"old_uuid":"blocked-session-UUID","project":"team/app","branch":"work","choice":"existing"}`; a failed local new-branch creation also permits `choice:"new"` with a local `source` branch or committed OID and an absent target. The run adds `"key":"new-idempotency-key","if_match":"<change tag>"` | Dry run: `plan` on target `session/<old_uuid>`, whose `details` hold the old operation, status, phase, immutable request, captured source, evidence digest, policy and image, the `old_branch` and `new_branch` observations, the `new_request` without a key, the new source, policy, image and plugin selection, and the provisional resource facts, and `change_tag`. Run: the new `session.create` operation, whose idempotency key is the run's `key`; an exact accepted run replays after restart. |
| `session.create.cleanup` | `{"uuid":"blocked-session-UUID"}`; the run adds `"key":"cleanup-idempotency-key","if_match":"<change tag>"` | Dry run: `plan` on target `session/<uuid>`, whose `details` hold the old immutable request and evidence digest, assigned ref, image, tracked builder state, provisional resources, `external_mounts:"preserved"` and `shared_images:"preserved"`, and `change_tag`. Run: one `session.create.cleanup` operation on the old UUID; an exact accepted run replays during cleanup and after restart. |
| `operation.inspect` | `{"id":"operation-UUID"}` | `operation` with status, phase, a `reason` and `diagnostic` once it is blocked or failed, and immutable evidence. |
| `operation.wait` | `{"id":"operation-UUID","timeout":1..25}`, whole seconds | `operation`, as inspect returns it, as soon as its status is not `running`, or when the timeout passes with it still `running`. See [Waiting for an operation](#waiting-for-an-operation). |
| `operation.retry` | Same as inspect | `operation`; schedules supported blocked-operation recovery using its persisted exact intent. |
| `operation.list` | `{"limit":1..20,"after":"optional-cursor"}` | concise operation summaries (without request/evidence, with the same `reason` and `diagnostic`) in the order the journal accepted them, and `next`, an opaque cursor: pass it back as `after` unchanged. It stays valid when the operation it follows is deleted. `next` is empty at the end. |
| `session.inspect` | `{"uuid":"session-UUID"}` | `session` with registry state, public session and policy conditions, `attached_count`, nullable `latest_unattended_condition`, and bounded diagnostic. |
| `session.list` | `{"limit":1..8,"after":"optional-session-UUID"}` | `sessions` with the same fields as `session.inspect`, and `next` in bytewise UUID order. Concurrently removed sessions may be omitted; follow a nonempty `next` even when `sessions` is empty. |
| `session.start` | `{"uuid":"session-UUID"}` | `changed`, with `operation` or `session`: a new `session.start` operation (`changed:true`), which completes once the session host is ready or fails with the runtime's `reason` and `diagnostic`; the unfinished start of the session (`changed:false`); or, for a session already ready, its view and nothing recorded (`changed:false`). See [Start and stop](#start-and-stop). |
| `session.stop` | Same as Start | The same shape: a new `session.stop` operation, which completes once the runtime is stopped; the unfinished stop; or, for a stopped session, its view. The runtime filesystem is retained. Pending or confirmed attachments answer `conflict` with reason `attachment_active`. |
| `session.attach` | Same as Start | `token`, `expires_at`, and `spec` containing fixed `project`, `instance`, and `argv`, for a ready session. It starts nothing: a session that is not ready answers `conflict` with reason `session_not_running`, and one with an unfinished start `retry` naming it. Start the session and wait for its operation first; `p attach` does. |
| `session.discard` | `{"uuid":"session-UUID"}`; the run adds `key` and `if_match` | Dry run: `plan` on target `session/<uuid>` for removing the session and its runtime while keeping its branch, and `change_tag`. Run: `operation` of kind `session.discard`. See [session removal](#session-removal-and-rename). |
| `session.delete` | Same as Discard | The same, for removing the session's assigned P branch too; the operation's kind is `session.delete`. |

Session listing reads a complete registry page before inspecting its sessions.
An inspection reporting a missing record is omitted only after confirming that
the exact session UUID has been removed from the registry. Missing authority
for a surviving session and other inspection errors remain errors. The cursor
belongs to the original registry page and remains valid after such omissions.

Every operation and operation summary carries `intent`: what the change's
caller named, recorded when the daemon accepted it. It never holds a guard
read from live state, such as an expected tip, URL or commit, or a change
tag. Its fields, each present only when the kind has it, are
`project`; `session`, the target session or, for a creation, the new one;
`branch`, the P branch named or held by the session at acceptance;
`new_branch` for a rename; `choice`, `source` and `origin_ref` for a session
creation; `replaces_session` and `replaces_branch` for a replacement, naming
the retired creation and the branch it held; `url` for a project created
with an origin and for an origin change's set; and `destination` for a
publication, beside the `session` or the `branch` it publishes. A client that
repeats a change with the same key can compare it without reading live state.

### Start and stop

`session.start` and `session.stop` record operations of kinds `session.start`
and `session.stop`, and answer `{"changed":…,"operation":…}` or
`{"changed":…,"session":…}`:

| The session | Answer |
|---|---|
| Already ready (start) or already stopped (stop) | `changed:false` and the current `session` view; nothing is recorded |
| A start (or stop) of it is unfinished | `changed:false` and that `operation` |
| Otherwise, once admitted | `changed:true` and the new `operation` |

So start and stop take no idempotency key: sent again they converge, and the
answer says whether anything happened. Their operation's
`idempotency_key` is its own ID. Before recording, start checks what the
session needs to run (established, its policy current, its Git principal
registered and active, its key, endpoints and runtime), and stop that no
attachment is pending or confirmed; a failed check records nothing. A
session keeps only its newest finished start or stop: recording one deletes
the older finished one. While a start or stop is unfinished,
`session.inspect` and `session.list` report the session `starting` or
`stopping`, whatever the runtime shows, and other work on the session is
refused as for any unfinished operation. A failed start's diagnostic is the
session's `diagnostic` while it is not ready.

### Waiting for an operation

`operation.wait` returns as soon as the operation is `completed`, `failed`,
`superseded` or `blocked`: a blocked operation needs a person or a Retry, not
more time. Otherwise it returns when its timeout passes, with the operation
still `running`; that answer is a result, not an error, and the client waits
again. The daemon wakes a wait when the journal commits the operation's next
status, not by polling, and `rpc.cancel` ends a wait early. An unknown ID
answers `not_found`; a timeout outside 1 to 25 answers `invalid` with field
`timeout`. The 25-second limit leaves time to answer inside the server's
30-second request deadline. A client's own deadline must cover the whole call:
connecting, the hello, the timeout and the reply. `p api` gives a wait its
timeout plus 5 seconds instead of its usual 10, and the TUI, whose calls each
have 25 seconds, waits with a timeout of 20.

An operation is never seen finished while its worker still holds its session:
the worker's hold ends just before the journal records the operation's end.
Work on a session that would compete with an unfinished operation on it
answers `retry` with reason `session_held` naming the operation while it
runs, and `conflict` with reason `operation_blocked` once it is blocked.
Requests that act on that operation itself pass: a replay of its key,
`operation.retry`, and the replacement or cleanup of a blocked creation. So
once a wait returns an operation's end, a request on its session meets
neither its worker nor its journal entry.

At start the daemon resumes every `running` operation, in the order the
journal accepted them; a `blocked` operation of any kind waits for
`operation.retry`, at start as while the daemon runs. The journal emits one
`operation.progress` event per status change of every operation
([session observability](session-observability.md#typed-p-events)).

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
| `image_build_failed` | A creation's build of the image of its source commit failed, for example because the commit declares an invalid default devShell or its realization failed. The diagnostic says why. Retry builds again from the same commit, so a fault in the commit needs a session from a corrected one. |
| `image_builder_changed` | A creation's image work found the host's image builder configuration (system, base image, builder pool, builder policy or the driver's image builder version) different from the one the creation recorded. Retry runs only once the configuration is restored. |

The diagnostic holds detail only, never advice: clients say what to do from the
reason. Every diagnostic P records, an operation's, a session's start
diagnostic and each resource's in project deletion evidence, keeps at most
1024 bytes, cut at a character boundary, so a Rename mismatch keeps both
validated branch or upstream names. `operation.list` shows the same diagnostic
as `operation.inspect`. Operation lists still enforce the control-frame limit
and return a continuation when a page must be shortened.

### Failed-creation replacement and cleanup

`session.create.replace` implements a bounded subset of
[Try again with changes](session-lifecycle.md#failure-cancellation-and-retry).
It accepts a blocked local creation in `source-ready` or `branch-assigned`
before UUID resources exist, or a base-image local creation in `principals-ready`
with its exact reviewed P Git key, active principal, and endpoint sockets present,
and affirmative durable `runtime_init_state:"not-attempted"` evidence. Ordinary
session creation records that state before the native boundary and changes it
to `attempted` immediately before dispatch through the runtime driver's gate.
Dispatch provenance and approved cleanup completion are monotonic in SQLite;
a stale Retry/status update cannot erase either checkpoint. A missing runtime
after an attempted init cannot prove that a delayed request
will not materialize; replacement refuses it. Historical `principals-ready`
evidence without this tracked state is also refused. Exact Retry refuses a
second absent-runtime init after an uncertain attempt, while an observed exact
owned instance may continue without another init.
An attempted or settled image build, an accepted commit image, and later
phases are outside this slice. Fresh full native inventory must positively prove both the
runtime UUID and creation builder absent; unavailable or ambiguous observations
refuse the plan. An existing-branch failure may select the same existing branch. A
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
contents are refused and preserved. Unavailable inspection or an unexpected
old created tip yields a plan that is not allowed. Existing refs are
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
refuse the plan. This path does not review or clean uncertain runtime effects
or workspace data, so its plan lists no loss. A plan that is not allowed lists
its refusals, the codes in the [refusals table](#refusals), and has no change
tag.

The dry run is read-only. A blocked creation, like every
blocked operation, stays blocked across restart until explicit Retry or exact
Create replay; startup does not resume branch assignment or native creation. Established endpoint
consumers may be reopened during recovery. The run plans again under the
session lock, and once more under the Git authority just before the handoff
transaction. Each time it rechecks the exact request, source and target/old
ref facts, policy, image, plugin selection, old evidence, resource identity,
and native absence. Changed facts or an
expired change tag answer `precondition_failed` with reason `changed` or
`expired` without superseding the old request. An accepted run atomically
marks the old operation `superseded`, releases its session assignment, and
creates one new UUID, operation, and immutable `session.create` request whose
idempotency key is the run's `key`, before scheduling its worker. For
reviewed local resources the same transaction retires the exact old
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
`supersedes_operation_id`, `supersedes_uuid`, and `replacement_tag`, the
run's change tag. Repeating an accepted run with the same params, key and tag
returns that operation, including after restart. Reusing its key with other
params or another tag answers `invalid` with reason `key_reused`. Retrying
the superseded operation answers `conflict` with reason
`operation_superseded`; exact replay of its original `session.create` key
returns the superseded operation without scheduling the old creation.

Confirmed failed-create cleanup is a separate fallback for a blocked, local
committed-source Create in `source-ready`, `branch-assigned`, or
`principals-ready`. It preserves the assigned P ref at its reviewed tip and
all other refs. It creates no new session or runtime. In addition to the early
paths above, it supports a failed image build, such as an immutable Nix
selection/resolution failure, when `environment_builder` durably records a
settled `absent` cycle: the build's gate recorded the attempt, and the runtime
driver then proved the build's builder deleted, when the build failed
definitely or when Retry settled an unknown outcome. Affirmative cycle-zero
`not-attempted` evidence also permits cleanup after a failure before the
build's gate, such as a builder preflight failure. The gate runs after
confinement, storage and pinned-image preflight; correcting such a failure
permits exact Retry without a new request. These refuse the plan: a build still
`attempted`, whose outcome is unknown; a build recorded `abandoned`, whose
leftovers may remain until recovery deletes them; an accepted commit image; and
origin requests. Later runtime/workspace phases
require the separate narrow assembled path below; they cannot use early absence
evidence. Early cleanup requires runtime initialization to affirmatively remain
`not-attempted`. Native
absence alone never settles a possibly delayed init.

The early path's plan reports runtime-local state as `unavailable`, not clean
or empty, and lists no loss.
Keys/principals/endpoints must either be absent or match the same reviewed local
identity checks used by integrated replacement. The run plans again, under the
session lock and once more under the Git authority, including the ref tip, old
evidence, principal, local entries and full native inventory. Changed or
unavailable facts answer `precondition_failed` with reason `changed`
(`expired` for an expired change tag) and preserve the old request; a Git
failure answers `failed`. An accepted early-path run atomically supersedes the old Create,
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

`session.create.cleanup` takes only `uuid` besides `key` and `if_match`;
P selects the path from the creator's state. A creator blocked at an early
phase takes the early path above. Any other creator takes the narrow stopped
assembled path, which requires a blocked, committed, `assembly-ready` local
base-image creator with a standalone policy (network `none`, no filesystem
grants) and its exact stopped container. A creator outside that boundary gets
a plan that is not allowed, with the cleanup's codes from the
[refusals table](#refusals). A creator container that P cannot identify
exactly refuses the plan with `exact_stopped_runtime_unavailable`; the
daemon's log has the cause.

P does not look inside the container. The assembled plan's `details.runtime`
has `condition:"present"`, `incus_project`, `instance_name`, `incus_uuid`,
`generation`, `image_fingerprint` and `original_status:"Stopped"`, and its
`loss` lists one `runtime_data` item: the container and everything in it are
lost, including private home and credential files, whose contents P neither
enumerates nor hashes. The plan also binds `old_request_sha256` and preserves
the assigned P ref. Its run takes the same `uuid`, `key` and `if_match`. A
durable precommit cleanup guard keeps the original creator blocked while the
worker rechecks the planned facts. A changed container identity (Incus UUID,
generation or image) or a changed branch fails the cleanup at `stale` and
releases its guards; the container, the branch and the original creator
remain. A new dry run and a run with a new key are then needed.

Only after those facts match does cleanup atomically retire the original
creator/session authority and admit deletion of the exact native source.
Subsequent recovery proceeds toward verified runtime and local-resource absence.
Unknown deletion effects or an unavailable/competing native identity remain
guarded and incomplete; recovery cannot reissue a name-only deletion or forget
the identity. The selected assembled-cleanup integration gate is separate from
earlier no-init cleanup evidence. Other unsupported
failures preserve resources and explain the unresolved condition.
See the [CLI procedure](session-lifecycle.md#confirmed-failed-create-cleanup).

### Session views

Each session view returns all four [public status facts](session-observability.md#status-model).
When a session was created with environment selection, its view also includes
`image`, an `api.ImageView` of the commit image it is created from: the
captured `commit_oid`, `system`, `selection`, `state`,
`base_image_fingerprint`, and `image_fingerprint`. Operation-list summaries
carry the same `image`; `operation.inspect` returns the detailed durable
evidence. `selection` is `pending` until the image is ready, then `base` for an
image without a default devShell or `devShells.<system>.default`. `state` is
`pending`, `building` while the creation's build is `attempted`, `built` when
this creation built the image, or `reused` when it reused the image indexed for
the commit.
`image_fingerprint` is creation's currently recorded runtime image, initially
the base and then the commit image. These fields report creation's record;
they do not imply that the current workspace is unchanged.

`latest_unattended_condition` is `null` or an object with `condition`,
`adapter`, `adapter_version`, optional `source` and `reason`, plus daemon-assigned
`received_at` and `receive_sequence`. `attached_count` counts confirmed live
helper connections. The list methods may return fewer than the requested limit to
keep the encoded JSON-RPC response within 64 KiB; use the returned `next`
cursor until it is empty, including when a short page has a nonempty cursor.

Without `runtime.environment`, creation uses the pinned base image, its
trusted `p.workspace/v1` configuration clones the assigned branch from P at
first start, and it refuses a selected committed root `flake.nix` there.
With environment selection, the commit image already holds a clone of the
captured commit, and a trusted `p.workspace/v2` configuration names that commit
and the image's selection: `no-flake`, `no-default` or `devshell`. The runtime
kit adopts the clone only when its HEAD is at the commit and the commit's root
flake matches the selection, then puts it on the assigned branch with P as its
upstream. Invalid defaults fail the image build without fallback.
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

A `name` must be printable UTF-8 of 1 to 128 bytes, or the request answers
`invalid` with field `name`. The runtime driver decides which names are project
services: any other name answers `not_found` with reason `service` and the
session as `target`, without reaching the session's service manager. On Incus
only session-user `p-project-*.service` units are project services. No
host/system unit, P infrastructure unit, arbitrary command, credential path,
journal path, native Incus target, or free-form systemctl action can be
supplied. An established session whose runtime runs with a ready session host
is required.
The daemon holds its session/workspace guards and calls the runtime driver at
the runtime's current incarnation; the Incus driver executes fixed commands
under guest UID/GID 1000 and checks native identity/generation before
accepting results. An action holds the
session's lifecycle lock and answers `retry` with reason `session_held` while
another lifecycle request holds it. The inventory and logs reads answer
`retry` while a lifecycle request holds the lock, but never cause a `retry`
answer: a request that takes the lock waits for running reads to end. A read
runs its runtime commands to their end, for at most 30 seconds, even after its
client cancels or disconnects, so a request that waits for it never meets an
Incus exec the read left behind. A session
that is not ready answers `conflict` with reason `session_not_running`. A
failure of the session's user service manager answers `failed` with reason
`services`; a failure of Incus answers `failed` with reason `runtime`. See the
[runtime service boundary](runtime-isolation.md#project-user-services).

Inventory combines loaded user units with installed user unit files, at most
64 distinct project units. An installed unit with no loaded observation is
`stopped` with detail `installed`: the user manager unloads only inactive
units. Unavailable or oversized inventory fails rather than returning a
misleading clean subset. Logs reads request at most 100 recent journal entries
and bound the encoded result; they may return a truncated tail. An action
returns once the runtime has queued it (on Incus, `systemctl --no-block`); the
inventory then shows the service `starting` or `stopping` and later its
result. Actions are not durable operations; starting a running service or
stopping a stopped one does nothing.

Origin-backed `session.create` additionally accepts optional
`expected_origin_url`, bound alongside `expected_commit_oid` to the source the
user reviewed. The TUI supplies it. A changed current URL refuses before fetch;
the SQLite reservation also rejects a captured URL mismatch. Exact replay keeps
the original request/URL and does not recontact a changed origin. Older callers
that omit this field retain their existing API behavior.

## Risky changes

Seven methods make changes that can lose work or replace something:

| Method | Params besides `key` and `if_match` | Plan `target` | Described in |
|---|---|---|---|
| `session.discard` | `uuid` | `session/<uuid>` | [Session removal](#session-removal-and-rename) |
| `session.delete` | `uuid` | `session/<uuid>` | [Session removal](#session-removal-and-rename) |
| `session.create.replace` | `old_uuid`, `project`, `branch`, `choice`, and optional `source`, `origin_ref` and `expected_commit_oid` | `session/<old_uuid>` | [Failed-creation replacement and cleanup](#failed-creation-replacement-and-cleanup) |
| `session.create.cleanup` | `uuid` | `session/<uuid>` | [Failed-creation replacement and cleanup](#failed-creation-replacement-and-cleanup) |
| `project.delete` | `project` | `project/<path>` | [Project deletion](#project-deletion) |
| `project.retained.delete` | `project`, `branch` | `project/<path>:branch/<branch>` | [Implemented methods](#implemented-methods) |
| `origin.publish` | `expected_origin_url`, `project`, `kind`, `source`, `source_oid`, `destination_ref` | `project/<path>:origin-branch/<name>`, where `<name>` is `destination_ref` without `refs/heads/` | [Implemented methods](#implemented-methods) |

Each is one method called twice. The first call, a **dry run**, sends the
params without `key` and `if_match`. The daemon plans the change and returns
its **plan**, with a **change tag** when the plan is allowed. The second call,
the **run**, sends the same params plus an idempotency `key` and the change tag
in `if_match`. The daemon plans the change again and records its operation only
if that gives the same change tag. It stores nothing between the two calls, so
the run must repeat the dry run's params exactly.

```text
→ session.delete  {"uuid":"4f1c…"}
← {"plan":{"action":"session.delete","target":"session/4f1c…","allowed":true,"refusals":[],"loss":[…],"gone":[],"details":{…}},"change_tag":"20261103T153012Z-<64 hex>"}
→ session.delete  {"uuid":"4f1c…","key":"k1","if_match":"20261103T153012Z-<64 hex>"}
← {"operation":{…}}
```

The two fields decide what a call is:

| The call has | Result |
|---|---|
| Neither `key` nor `if_match` | Dry run: `{"plan":{…},"change_tag":"…"}` when the plan is allowed, or `{"plan":{…}}` with `allowed:false`, its `refusals` and no `change_tag` |
| `key` and `if_match` | Run: `{"operation":{…}}`, the operation it recorded |
| `key` but no `if_match` | `precondition_required` with reason `if_match_missing` |
| `if_match` but no `key` | `invalid` with reason `param` and field `key` |

`key` is 1 to 128 bytes. An `if_match` that is not a change tag answers
`invalid` with field `if_match`. The result type of every risky change is a
`ChangeResult`, which holds either `plan` and `change_tag` or `operation`.

A dry run starts no operation and destroys nothing. It reads P's control store,
P's Git repository and, for a session's runtime, Incus. Publication's dry run
also contacts the origin, which refreshes P's observation of it as
`origin.refresh` does.

### The run

A run is answered in this order:

1. When its `key` already recorded an operation for the same params and tag,
   the run returns that operation, also after the tag expired and after a
   daemon restart. A `key` recorded for other params, another tag or another
   request answers `invalid` with reason `key_reused`.
2. Once the tag's expiry has passed, the run answers `precondition_failed` with
   reason `expired`.
3. The daemon plans the change again while it holds what the change acts on.
   A plan that is no longer allowed, or that gives another tag, answers
   `precondition_failed` with reason `changed`.
4. The daemon records the operation and returns it. Recording checks the
   planned facts again.

A refused run records nothing. Its error carries no plan and no tag: make a new
dry run to see the current plan. The daemon does not mark a tag as used, so
another run with the same tag and a new key plans again and meets the state
the first run left.

### Change tag and change hash

A change tag is an expiry, a dash, and 64 lowercase hex digits:

```text
20261103T153012Z-9f2c4e…
```

The expiry is two minutes after the dry run, in UTC to the second, in ISO 8601
basic format (`YYYYMMDDTHHMMSSZ`); only the daemon's clock sets and checks it.
The hex digits are the SHA-256 of the expiry and the change's **change hash**.
The change hash is the SHA-256 of one JSON object: the recipe name
`p.plan/v1`, the daemon's `instance_id`, the action, the target, the caller's
other params, and the plan, less the facts it shows for information only (a
removal's `original_status`). The same state gives the same change hash, and
any other changed fact in the plan changes it. Because the action, the target and the
instance are hashed, a tag for one change cannot run another. Editing the
expiry breaks the tag, because rebuilding it needs the change hash, which the
daemon never returns. The tag is not a secret; it keeps every run close to a
recent dry run.

The operation journal records each run's change hash with its operation: the
exact change that ran. No reply shows it. The operation's `request` holds the
run's params with `key` and `if_match`. A replacement's new `session.create`
operation records the creation request under the run's key, and its evidence
holds the tag as `replacement_tag`.

### The plan

| Field | Type | Meaning |
|---|---|---|
| `action` | string | The method name, such as `session.delete`. |
| `target` | string | What the change acts on, in target form. |
| `allowed` | boolean | Whether the change can run now: `true` exactly when `refusals` is empty. |
| `refusals` | array, never null | Why the change cannot run now. |
| `loss` | array, never null | What the change would destroy, by category. An empty list means nothing is lost. |
| `gone` | array, never null | What is already lost before the change runs, such as the contents of a missing runtime. The change destroys none of it. |
| `details` | object | Everything else the change would do and the facts it was planned from, typed per method. |

A target names what a plan, a refusal, a loss item or an error is about:
`session/<uuid>`, `operation/<id>`, `project/<path>`,
`project/<path>:branch/<name>` for a P branch, or
`project/<path>:origin-branch/<name>` for a branch of the project's origin. A
project path never contains `:`, so the first `:` ends it.

A refusal is `{"code":"…"}`, with a `target` when it is about something other
than the plan's target, such as the session that blocks a project deletion. A
refusal carries no text; the daemon's log has the cause. Refusal codes share
one namespace with error reasons. Every `conflict` reason that a change meets
while it plans is a refusal code in its plan rather than an error: for example
`attachment_active`, `session_not_established`, `runtime_unsettled`,
`runtime_present`, `operation_blocked` (with target `operation/<id>`),
`operation_unfinished`, `branch_assigned`, `branch_reserved`,
`project_deleting`, `origin_not_set` and `bound_exceeded`. The
[refusals table](#refusals) lists the codes that only plans use.

Planning may stop at its first refusal, so a plan that is not allowed can have
incomplete `loss` and `details`. Only an allowed plan's `loss` is complete: an
empty `loss` in a plan that is not allowed does not mean nothing would be
lost.

A loss item, in `loss` or in `gone`, has these fields:

| Field | Type | Meaning |
|---|---|---|
| `kind` | string | One of the kinds below. |
| `target` | string | Where the lost thing lives, in target form. It can differ from the plan's target. |
| `count` | integer, optional | How many, where P counts them. An item with a count of zero is not listed. |
| `sample` | array of strings, optional | At most five commit IDs, in the order of the full list in `details`. |
| `certain` | boolean | `true` when P saw that the thing exists and the change destroys it; `false` when P cannot rule the loss out. |
| `reason` | string, optional | Why P cannot tell, when `certain` is `false`. |

| Kind | What is lost | Target |
|---|---|---|
| `runtime_data` | A session's container and everything in it: uncommitted changes, files outside Git, commits not pushed to P, processes, home, temporary files and runtime-local credentials. P does not look inside. Always certain; listed in `gone` for a missing runtime. | `session/<uuid>` |
| `p_commits` | Commits that leave P's repository because no remaining branch keeps them, with `count` and `sample`. Always certain: P does not check whether the origin has them. | `project/<path>:branch/<name>`, or `project/<path>` for project deletion |
| `unreachable_objects` | Git objects in P's repository that no branch reaches. Project deletion always lists it, with `certain:false` and `reason:"not_enumerated"`. | `project/<path>` |

Loss items follow the order of the targets in `details`, sessions first and
then the branch or project, and within one target the order of this table.

Errors keep their meaning for risky changes and never carry a plan or a tag:
`invalid`, `not_found`, `retry` (such as `session_held` while a running
operation holds the session), `failed` (such as `runtime`, `git` or `origin`),
`internal`, and `precondition_failed` with reason `changed` when a guard the
caller sent, such as publication's `expected_origin_url` or `source_oid`, no
longer holds. A dry run whose reply would exceed the 64 KiB frame less 1 KiB
answers `conflict` with reason `bound_exceeded`.

## Session removal and rename

`session.discard` removes a session and its runtime and keeps its assigned P
branch; `session.delete` removes the branch too. Both are
[risky changes](#risky-changes) on target `session/<uuid>`, and the dry run
takes `{"uuid":"session-UUID"}`. P does not inspect the container before
removal, and a running session needs no Stop: P stops it as part of the
removal.

The plan is not allowed, with one of these refusals:

- `attachment_active` while a terminal is attached or an attachment is
  pending;
- `session_not_established` for a session that is being created or removed;
- `runtime_unsettled` for a present runtime that is neither running nor
  stopped;
- `operation_blocked`, naming the operation, while a blocked operation holds
  the session; or
- `runtime_present` when Incus reports the session's instance absent but
  another instance carries the session's UUID.

Another unfinished operation on the session that is still running, such as its
creation, a discard, delete, rename or failed-create cleanup, answers `retry`
with reason `session_held` naming the operation. An unreachable Incus answers
`failed` with reason `runtime`: it does not count as an absent runtime.

When Incus reports the runtime absent, P also checks that a full inventory of
its confined Incus project holds no instance carrying the session's UUID. The
plan then has `runtime.condition:"missing"`, lists the runtime's contents as
one `runtime_data` item in `gone`, and is allowed: running it proves the caller
saw that the runtime is gone. A renamed or competing instance is neither
adopted nor deleted; it refuses the plan with `runtime_present`. The absence
proof is repeated before the authority commit and the final row removal,
including after a present-runtime deletion.

The plan's `details`:

| Field | Meaning |
|---|---|
| `project`, `branch` | The session's project and assigned branch. |
| `assigned_ref`, `assigned_tip` | The full assigned ref and its tip. When the ref is missing, because it was deleted outside P or is a blank project's unborn first branch, `assigned_tip` is omitted and `assigned_ref_missing` is `true`. |
| `policy_sha256` | The digest of the session's captured policy. |
| `runtime` | `condition` (`present` or `missing`), `incus_project` and `instance_name`. A present runtime adds `incus_uuid`, `generation`, `image_fingerprint` and `original_status` (`Running` or `Stopped`). |
| `branch_loss` | Delete only: the `assigned_ref`, its `assigned_tip` when present, the `commits_losing_p_reachability` (empty for a missing ref) and `p_refs`, every P ref with its `name` and `oid`. |

`loss` lists a present runtime as one `runtime_data` item. Delete adds one
`p_commits` item on `project/<path>:branch/<branch>` when commits leave P's
repository. Discard and delete contact and compare no origin.
`original_status` is shown for information only and is not in the change
hash: a Start or Stop between the dry run and the run keeps the change tag.

The run, `{"uuid":"session-UUID","key":"idempotency-key","if_match":"<change tag>"}`,
returns `{"operation":{...}}` of kind `session.discard` or `session.delete`.
Before the removal commit point, a changed assigned tip, a replaced container
(changed Incus UUID or generation) or a reappearing missing runtime
fails the operation at `stale`, releases its guard, and leaves the branch and
session intact. An accepted run commits removal, disables session authority,
stops a running container, removes the exact owned runtime and P-owned
endpoint/key, and ends the assignment while retaining the planned P branch.
With a missing assigned ref there is no branch to retain, so Discard ends like
a Delete. A revoked or missing Git principal registration and a missing host
key file do not stop removal; a host key file with an unexpected owner, mode or
link count still blocks the key removal. While the operation runs, Start,
Stop, Attach and service requests answer `retry` naming it, and `conflict`
with reason `operation_blocked` once it is blocked. The same key and request
replay the operation. A stop whose outcome was lost is sent again on Retry,
since stopping is idempotent. A delete whose outcome was lost is never sent
again: it stays guarded and blocked, with reason `outcome_unknown`, until the
runtime driver settles it as applied.

Delete applies the same pre-commit stale checks, and a changed `branch_loss`
is also stale. The exact runtime, endpoint, and key cleanup is the same. After
runtime absence, it removes only the assigned P branch through an
expected-old-tip check; sibling P refs and origin refs remain. With a missing
assigned ref it has nothing to remove in P's repository. A mismatched or
uncertain branch effect stays guarded for investigation.

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
effect may fail and release the guards; an uncertain workspace or Git effect
remains blocked for exact recovery or investigation. A workspace write or
removal whose outcome was lost is never sent again: rename goes on only once
the runtime driver settles it as applied, also on a retry or after a restart.
The current supported workspace layout is a single ordinary worktree with loose
branch refs; other layouts return unavailable before the ref commit point.

## Project deletion

`project.delete` deletes a project and everything P keeps of it: every session
and its runtime, every assigned and retained P ref, the bare repository,
credentials and the project's commit images. It is a
[risky change](#risky-changes) on target `project/<path>`, and the dry run
takes `{"project":"team/app"}`. P does not look inside the containers and
contacts no origin.

The implemented boundary accepts at most four sessions, each established and
without an attachment. P stops running sessions itself: the run's worker stops
a running session's container, then deletes it. The plan is not allowed, with
one of these refusals:

- `attachment_active`, naming the session, while a terminal is attached to it
  or an attachment is pending;
- `session_not_established`, naming the session, for an incomplete creation or
  a session being removed; a failed creation needs supported cleanup first;
- `runtime_unsettled`, naming the session, for a present runtime that is
  neither running nor stopped;
- `runtime_present`, naming the session, when Incus reports its instance
  absent but another instance carries its UUID;
- `operation_blocked`, naming the operation, while a blocked operation holds
  the project;
- `operation_unfinished` while a branch guard holds the project;
- `project_deleting` once the project is being deleted; or
- `bound_exceeded` for more than four sessions, or more images or facts than
  the deletion's evidence can record.

A running operation in the project, or another `project.delete` call on it,
answers `retry` with reason `project_held`; a session that another request
holds answers `retry` with reason `session_held`. An unreachable Incus, or an
instance or image labeled with the project that the plan does not name,
answers `failed` with reason `runtime`. A missing runtime is planned as for
[session removal](#session-removal-and-rename): after the full inventory
proof, its session has `condition:"missing"` and the runtime's contents are
listed in `gone`. A refused plan directs the caller to detach, supported
failed-creation cleanup or identity investigation; P does not delete uncertain
resources.

The plan's `details` hold `project`, `outcome`
(`delete_project_and_all_p_data`), `state_sha256`, `project_policy_sha256`,
`external_mounts_preserved` (the filesystem grants whose contents P keeps),
`sessions`, `attachments` (always empty, because an attachment refuses the
plan), `branch_loss`, `images`, `repository_identity` and
`repository_parent_identity`. Each entry of `sessions` has the `session`
record, its `condition` (`running`, `stopped` or `missing`), its `runtime` as
for session removal, and `credentials`: the exact identities of its key and
endpoint entries. `branch_loss` covers every assigned and retained P ref and
the union of the commits that lose P reachability; it has no origin evidence.
`loss` lists one `runtime_data` item per present session, one `p_commits` item
on `project/<path>` when commits leave P's repository, and always
`unreachable_objects`.

At most twenty indexed commit images and bounded Git/history and report
inventories are accepted. The plan is also refused with `bound_exceeded` when
the evidence the deletion would run with exceeds 24 KiB, so the effective
image limit depends on the sessions and names: with four sessions and the
longest project, branch and builder-version names P accepts, eight images fit.
`images` lists each commit image the project's index holds as an
`api.CommitImageClaim`: its index row (`image`: the image's key, reference,
build, labels as a list of `{name, value}`, selection, material digest, size
and state), whether the runtime driver found the image (`image_present`), its
observed size (`observed_size`), and a digest of the sessions started from its
commit (`related_digest`).

The run, `{"project":"team/app","key":"idempotency-key","if_match":"<change tag>"}`,
plans the deletion again under the project's locks and once more under the Git
authority. The transaction that retires the project then compares the
project's stored state with the planned one; any difference answers
`precondition_failed` with reason `changed` and records nothing. P then
atomically sets the project to `deleting`, sessions to `removing`, and
principals inactive. The call returns the operation at once. Follow it with
`operation.wait`; its incomplete evidence contains independent `runtime`,
`local`, `image` and `repository` resources with `remaining`, `unreachable`,
`deleted` or `already_absent` status. Existing `operation.retry` resumes the
same confirmed outcome; it does not grant replacement machinery.

A stop or a delete of a session's container is recorded as issued before it
starts. A stop is sent again while the container still runs, since stopping is
idempotent; an issued delete is never dispatched again. Only once the runtime
driver settles it as applied, even when the container already looks absent,
may available name and full inventory checks positively prove absence after a
crash; a still-present, renamed, competing or uncertain source keeps cleanup
blocked for investigation. Indexed commit images are deleted exactly, through
the runtime driver, only after runtime absence, and their index rows with
them. The repository and registry disappear last. The base image, other
projects and their images, external grant contents and delivered logs remain.
Completion clears bulky claims and old raw requests/evidence, retaining a
minimal completed result and immutable retired
key/operation-ID/kind/request-digest receipts. Replaying old Create, origin or
publication keys refuses instead of recreating deleted authority.

## Terminal attachment

Run `p attach SESSION` on the P host using the configured control socket, or
`p attach /absolute/control.sock SESSION` to select it explicitly. `SESSION` is
a session UUID, or `PROJECT:BRANCH` for the session that holds that branch.
Run it directly or inside a client-initiated SSH terminal. The CLI first
starts a session that is not ready and waits for its start operation, since
`session.attach` starts nothing. It restores local terminal settings on exit
and forwards terminal size changes.

`session.attach` returns a 30-second, one-use pending token after native checks
approve the fixed `/usr/libexec/p/attach` command. The public spec grants no
general Incus access.
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
| `target` | What the error is about, when it names one: `session/<uuid>`, `operation/<id>`, `project/<path>`, `project/<path>:branch/<name>` or `project/<path>:origin-branch/<name>`. A project path never contains `:`, so the first `:` ends it. |
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
| `conflict` | -32011 | Change the named state first: detach, stop or start the session, retry the blocked operation, or wait for a person. Sending the same request again fails the same way until then. A [risky change](#risky-changes) answers such a state with a refusal in its plan instead. |
| `capacity` | -32012 | Free a slot: discard or delete a session. |
| `precondition_failed` | -32013 | Read the state again and decide again: for a risky change, make a new dry run. |
| `precondition_required` | -32014 | Send the precondition the change needs: a risky change's run needs the dry run's change tag in `if_match`. |
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
| `retry` | `project_held` | Another request or a running operation holds the project: another creation of it, another `project.delete` call on it, or, for `project.delete`, any running operation in it. `target` names the operation when there is one. |
| `retry` | `refs_moved` | P's branches moved while the daemon read them. |
| `conflict` | `attachment_active` | A terminal is attached to the session, or an attachment is pending. |
| `conflict` | `session_not_established` | The session is still being created, is being removed, or was never created. |
| `conflict` | `session_not_stopped` | The change needs a stopped session: Rename. |
| `conflict` | `session_not_running` | The request needs a ready session: attachment and project services. |
| `conflict` | `runtime_missing` | The session's container does not exist. A removal plans a missing runtime instead and lists its contents in `gone`. |
| `conflict` | `runtime_unsettled` | The session's container is neither running nor stopped. |
| `conflict` | `runtime_present` | A container of the session exists where P had to prove none does, such as another instance carrying the UUID of a session whose own instance is absent. |
| `conflict` | `operation_blocked` | A blocked operation holds the target and waits for `operation.retry`; `target` names it. |
| `conflict` | `operation_superseded` | `operation.retry` of an operation that a replacement or cleanup superseded. |
| `conflict` | `operation_not_retryable` | `operation.retry` of an operation that cannot run again, such as a cleanup that found its review stale. |
| `conflict` | `operation_unfinished` | A branch guard holds the project. |
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
| `conflict` | `bound_exceeded` | The answer would exceed the response bound, such as a dry run whose plan does not fit in one reply, or a project deletion with more sessions, images or facts than its evidence can record. |
| `capacity` | `session_capacity` | Every session slot is taken. Discard or Delete a session to free one; Stop keeps its slot. The [runtime contract](runtime-isolation.md#incus-boundary) owns capacity accounting. |
| `capacity` | `attachment_capacity` | Every pending or active attachment slot is taken: eight per session, 128 in all. |
| `capacity` | `public_address_slots` | Every public egress address is assigned. |
| `precondition_failed` | `expired` | The change tag's expiry, two minutes after its dry run, has passed; or the attachment token expired, was already used, or is unknown, for example after a daemon restart. |
| `precondition_failed` | `changed` | What the request reviewed or expected no longer holds: the plan behind a change tag, which changed, is no longer allowed, or is for another change or target; or an `expected_old_tip`, `expected_url`, `expected_origin_url`, `expected_commit_oid` or `source_oid`. |
| `precondition_required` | `if_match_missing` | A risky change's run sent `key` without `if_match`. |
| `not_found` | `session` | The session does not exist; `target` names it. |
| `not_found` | `operation` | The operation does not exist. |
| `not_found` | `project` | The project does not exist. |
| `not_found` | `branch` | The P branch does not exist. |
| `not_found` | `source` | The source ref or commit to create from does not exist, is unborn, or is not reachable from a P branch. |
| `not_found` | `service` | The session has no project service of that name; `target` names the session. |
| `not_configured` | `git`, `runtime`, `services` | The capability is not configured; see the [connection agreement](#connection-agreement). |
| `not_configured` | `public_egress` | The session's policy needs public egress, which this host does not configure. |
| `not_configured` | `project_policy` | The host configuration has no policy for the project. |
| `failed` | `runtime` | Incus or the runtime driver failed or could not be observed. |
| `failed` | `services` | The session's user service manager is unavailable or refused the command. |
| `failed` | `git` | P's Git repository or the source-Git plugin failed. |
| `failed` | `origin` | The origin could not be contacted, or refused an observation, fetch or push. |
| `failed` | `plugin` | A selected plugin failed its recheck before use. |

### Refusals

A [risky change](#risky-changes)'s plan that is not allowed lists its
refusals. Refusal codes share one namespace with the reasons above, so a client
keeps one sentence per code. Every `conflict` reason a risky change meets while
it plans is a refusal code in its plan, such as `attachment_active`,
`operation_blocked` or `bound_exceeded`. These codes appear only in plans:

| Change | Code | Meaning |
|---|---|---|
| `origin.publish` | `destination_divergent` | The origin branch has commits the source lacks; P only creates or fast-forwards a branch. |
| `session.create.replace` | `unsupported_replacement_choice` | The new request is not a replacement this creation allows. |
| `session.create.replace` | `old_creation_not_blocked` | The creation is not blocked. |
| `session.create.replace` | `old_effect_not_proven_absent` | The creation reached a step whose effects P cannot prove absent. |
| `session.create.replace` | `current_policy_unavailable` | The project's current policy is unavailable. |
| `session.create.replace` | `new_source_or_branch_unavailable` | The new source or branch could not be read, or changed while P read it. |
| `session.create.replace` | `target_assignment_unavailable` | Another session or an unfinished operation holds a branch the replacement needs. |
| `session.create.replace` | `request_unchanged` | The new request would create the same session again. |
| `session.create.replace` | `provisional_resource_unavailable` | The creation's provisional resources are not as P left them, or are in use. |
| `session.create.cleanup` | `blocked_local_creation_required` | The creation is not a blocked creation of this session. |
| `session.create.cleanup` | `blocked_stopped_standalone_base_image_assembly_ready_creator_required` | The assembled creation is not the stopped base-image creator that a cleanup removes. |
| `session.create.cleanup` | `active_workspace_or_cleanup_guard` | Other work holds the session. |
| `session.create.cleanup` | `native_or_builder_dispatch_unsettled_or_publication_origin_unsupported` | A runtime or image build may have started, or the creation is from the origin. |
| `session.create.cleanup` | `creation_worker_or_attachment_active` | The creation's worker or an attachment is active. |
| `session.create.cleanup` | `assigned_P_ref_unavailable` | The session's branch is missing or could not be read. |
| `session.create.cleanup` | `assignment_or_lifecycle_guard_unavailable` | Another session or an unfinished operation holds the branch. |
| `session.create.cleanup` | `local_credentials_or_endpoints_unverified` | The session's key, Git identity or endpoints are not exactly as P made them. |
| `session.create.cleanup` | `exact_stopped_runtime_unavailable` | The creator's container is not the exact stopped one P made; the daemon's log has the cause. |
| `session.create.cleanup` | `native_runtime_or_builder_present_ambiguous_or_unreachable` | A container or builder of the creation exists, is ambiguous, or could not be checked. |

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
