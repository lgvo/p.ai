# Local control API v1 foundation

This reference specifies the implemented host control connection and private
per-session RPC methods.
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
loopback port and the actual endpoint appears in `system.capabilities`.

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
immutable policy. The guest installs only that address, route, and the pinned
public resolvers. A `none` session receives no NIC even in a project that
permits the public bridge. A changed or missing public substrate makes the
captured public policy `invalid` and blocks Start.

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
This initial path is offline: it supplies no NIC, public substituters, host
store, filesystem grants, or credentials to the builder.

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
session. A registered key that is missing or changed blocks retry and Start
for repair; P never rotates it silently.

## Host framing and request envelope

Connect to `<state_dir>/control.sock` as the same local user. Each UTF-8 line
is one JSON-RPC 2.0 message, with a 64 KiB maximum including its newline. An
oversize or unterminated line receives a bounded parse error and closes the
connection. Up to 64 client connections and 16 concurrent requests per
connection are admitted. Requests have a 30-second deadline. The daemon
closes idle connections after two minutes and closes active connections on
shutdown.

System methods take exactly `{"v":1}` as `params`. Read-only project methods
take the bounded parameters below. Requests need a
string or integer `id`; duplicate in-flight IDs on one connection are refused.
An absent `id` is a notification, which receives no response. One response is
sent for each valid request, with the same ID, `jsonrpc:"2.0"`, and exactly one
of `result` or `error`. Responses may arrive out of order. JSON object keys
must be unique at every depth.

The CLI `p api <socket-path> <method> [json-params]` sends one request with
`{"v":1}` by default and writes the full JSON response envelope to stdout.
An RPC error exits nonzero. A connection failure writes a JSON object with
`error.kind:"transport"` and exits nonzero.

## Private session RPC

The daemon binds each `session.sock` to its directory's session UUID. The
socket accepts requests only while that session's registry state is
`established`; no parameter can select another session. It never forwards a
request to the host control connection. The [session RPC audience](communication-boundaries.md#session-rpc-audience)
defines the boundary.

Session RPC uses newline-delimited JSON-RPC 2.0 over one persistent Unix
connection. Each line must be UTF-8 and at most 64 KiB including the newline.
An oversized or unterminated line receives a bounded parse error and closes
the connection; an idle connection closes after two minutes. At most 16
session connections are admitted by the endpoint manager. Object keys must
be unique, and methods reject unknown parameter fields. Requests use a string
or integer `id`; notifications omit `id` and receive no response.

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
returns `invalid_request` and does not update status. Valid reports are limited
to 20 per second and 240 per minute per session; excess notifications are silently
dropped. Complete, size-bounded private RPC lines, including malformed JSON
and queries, share a separate limit of 100 per second and 600 per minute per
session. Oversized or unterminated frames fail framing before that limit is
checked. Reaching the limit closes the connection without replying to the
over-budget line. The [latest unattended
reduction](session-observability.md#latest-unattended-condition)
owns when an accepted report replaces or clears the durable value.

## Implemented methods

| Method | v1 result fields | Meaning |
|---|---|---|
| `system.hello` | `v`, `protocol`, `build_version`, `instance_id` | Durable instance UUID and protocol `p.control/v1`; build version is `development` unless set at link time. |
| `system.health` | `v`, `control_state` | `ready` means the SQLite control store responded. It makes no Git, Incus, systemd, or plugin readiness claim. |
| `system.capabilities` | `v`, `available`, `lifecycle`, optional `git` | Lists only implemented methods; lifecycle is `unavailable` without runtime configuration and `partial` with it. Configured `git` contains actual listener endpoint, SSH URL template, server public key, known-hosts entry, host client public key/key path, and selected plugin ID/digest. |
| `system.inspect` | `v`, `projects`, `sessions`, `operations`, optional `git` | Counts persisted control records and, when configured, reports the same Git connection details. Counts are not reconciled runtime or Git status. |

With trusted Git configuration, two more read-only methods are available:

| Method | Params | Result | Meaning |
|---|---|---|---|
| `project.list` | `{"v":1,"limit":1..100,"after":"optional/project"}` | `v`, `projects` (`path`, `registry_state`), `next` | Lists registered projects in bytewise path order. `next` is empty at the end. |
| `project.branches` | `{"v":1,"project":"project/path","limit":1..8,"after":"optional/refs/heads/name"}` | `v`, `project`, `refs` (`ref`, `oid`), `next` | Lists user-visible branches of an active registered project through the selected source-Git package and real Git. `next` is empty at the end. |

`rpc.cancel` takes `{"v":1,"id":<request-id>}`. It cancels a pending request
on the same connection. As a notification it has no reply. As a request it
returns `{"v":1,"cancel_requested":true}`; this only acknowledges the
request to cancel, including when the target already finished. A cancelled
target replies with `error.kind:"cancelled"` if it was still running.

With trusted Git configuration, active projects also support explicit origin
association and observation. These methods are available even when runtime
configuration is absent. Every request uses version 1 and rejects unknown or
duplicate fields.

| Method | Params | Result |
|---|---|---|
| `origin.change` | `{"v":1,"key":"idempotency-key","project":"team/app","kind":"set","expected_url":"","url":"ssh://host/repo"}`; use `kind:"remove"`, the current `expected_url`, and omit `url` to remove | `v`, `origin` state. A set contacts the proposed origin before commit; remove makes the project local-only. The old URL is an exact compare-and-swap precondition. |
| `origin.refresh` | `{"v":1,"project":"team/app"}` | `v`, current `origin` state after contact. Failed contact returns `status:"unknown"` and retains the prior completed observation as stale presentation data. |
| `origin.inspect` | Same as refresh | `v`, current `origin` state without contact. |
| `origin.sources` | `{"v":1,"project":"team/app","after":"optional-ref","limit":1..16}` | `v`, `project`, `origin_url`, `status`, `observation_status`, `observed_at`, `refs` (`ref`, `oid`, `commit_oid`), `next`. Refs are bytewise ordered and paged; `next` is empty at the end. |
| `origin.publication.preview` | `{"v":1,"project":"team/app","expected_origin_url":"ssh://host/repo","kind":"session","source":"session-UUID","source_oid":"<exact P tip>","destination_ref":"refs/heads/main"}`; `kind:"retained"` uses a P branch name in `source` | `v`, `preview` with project, origin URL, source kind/identity/ref/OID, destination ref and observed OID or absence, relation, and `workspace_status:"unknown"`. This contacts the origin afresh. |
| `origin.publish` | Same source fields as preview, plus `"key":"idempotency-key"` | `v`, `publication` with the preview and `status` (`created`, `advanced`, `satisfied`, `refused`, or `outcome_unknown`). This performs its own fresh refresh and comparison; only `absent` or `fast_forward` can issue one ordinary push. |
| `project.retained_branches` | `{"v":1,"project":"team/app","after":"optional-full-P-head-ref","limit":1..8}` | `v`, `branches` with `branch`, full `ref`, and tip `oid`, plus `next`. The cursor traverses P Git refs, so a page may be empty while `next` is nonempty if its refs are assigned to live sessions. |
| `project.retained.rename` | `{"v":1,"key":"idempotency-key","project":"team/app","old_branch":"saved","new_branch":"archive","expected_old_tip":"<exact P commit OID>"}` | `v`, `operation` (`kind:"project.retained.rename"`). The source must be an unassigned retained P branch at that exact commit, and the destination must be absent and unassigned. A completed key replays; changed inputs with the same key conflict. |
| `project.retained.delete.preview` | `{"v":1,"project":"team/app","branch":"saved"}` | `v`, `preview` with the unassigned P `ref`/`tip`, `branch_loss` (commits losing all P reachability, complete P refs, fresh origin preservation or explicit `unknown`), `confirmation_token`, and `expires_at`. It is read-only and needs no runtime inspection. |
| `project.retained.delete.confirm` | `{"v":1,"key":"idempotency-key","project":"team/app","branch":"saved","confirmation_token":"<32 lowercase hex>"}` | `v`, `operation` (`kind:"project.retained.delete"`). A completed exact key replays; changed key inputs conflict. |

An origin state has `project`, optional `url`, `status` (`local-only`, `fresh`,
or `unknown`), optional `observed_at`, `ref_count`, and optional bounded
`diagnostic`. `origin.sources` labels retained refs `stale` when current status
is `unknown`; they cannot authorize source selection or publication. A
completed `origin.change` key replays its committed result without contacting
the origin. Reusing a key with different inputs returns `busy`. Interrupted
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
origin is rejected before any contact or push. Reusing a key with another URL
is a conflict, including after restart or a pre-start failure. The action
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
branch requires the explicit replacement methods below.

| Method | Params | Result |
|---|---|---|
| `project.create` | `{"v":1,"key":"idempotency-key","project":"team/app"}`; optionally include `"url":"ssh://host/repo"` for origin mode | `v`, `operation`; blank mode reserves unborn `main`. Origin mode contacts the URL before committing the project and observation; an empty origin also reserves unborn `main`. |
| `session.create` | `{"v":1,"key":"idempotency-key","project":"team/app","branch":"work","choice":"existing"}` or `choice:"new"` with `source:"refs/heads/main"` or a committed object ID; origin mode uses `choice:"new"`, `origin_ref:"refs/heads/main"` or a tag, and `expected_commit_oid:"<observed commit>"` with no `source` | `v`, `operation`; origin mode requires a fresh observation and fetch, then captures the exact commit and origin identity for Retry. Blank session creation is unavailable. |
| `session.create.replace.preview` | `{"v":1,"old_uuid":"blocked-session-UUID","key":"new-idempotency-key","project":"team/app","branch":"work","choice":"existing"}`; a failed local new-branch creation also permits `choice:"new"` with a local `source` branch or committed OID and an absent target | `v`, `preview` with old immutable request/operation/UUID/source/policy/image, `old_branch` and `new_branch` observations, new request/source/policy/image/plugin selection, provisional resource facts, `eligible`, and `unsafe_reasons`. Eligible previews also return `confirmation_token` and `expires_at`. |
| `session.create.replace.confirm` | `{"v":1,"old_uuid":"blocked-session-UUID","key":"new-idempotency-key","confirmation_token":"<32 lowercase hex>"}` | `v`, new `session.create` operation; an exact accepted key/token replays after restart. |
| `operation.inspect` | `{"v":1,"id":"operation-UUID"}` | `v`, `operation` with status, phase, bounded diagnostic, and immutable evidence. |
| `operation.retry` | Same as inspect | `v`, `operation`; schedules supported blocked-operation recovery using its persisted exact intent. |
| `operation.list` | `{"v":1,"limit":1..20,"after":"optional-operation-UUID"}` | `v`, concise operation summaries (without request/evidence), `next` in bytewise ID order. |
| `session.inspect` | `{"v":1,"uuid":"session-UUID"}` | `v`, `session` with registry state, public session and policy conditions, `attached_count`, nullable `latest_unattended_condition`, and bounded diagnostic. |
| `session.list` | `{"v":1,"limit":1..8,"after":"optional-session-UUID"}` | `v`, `sessions` with the same fields as `session.inspect`, and `next` in bytewise UUID order. |
| `session.start` | `{"v":1,"uuid":"session-UUID"}` | `v`, `session` with `starting` while a daemon-owned watcher converges readiness; poll `session.inspect` for `ready` or `stopped` with diagnostic. |
| `session.stop` | Same as Start | `v`, `session` after the Incus stop and fresh observation; runtime filesystem is retained. Pending or confirmed attachments return `busy`. |
| `session.attach` | Same as Start | `v`, `token`, `expires_at`, and `spec` containing fixed `project`, `instance`, and `argv`. Starts a stopped runtime and waits for readiness. |

The replacement methods implement a bounded subset of
[Try again with changes](session-lifecycle.md#failure-cancellation-and-retry).
They accept a blocked local creation in `source-ready` or `branch-assigned`
before any builder, key, endpoint, principal, or native runtime effect. Fresh
native inventory and local checks must positively prove these UUID resources
absent. An existing-branch failure may select the same existing branch. A
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
`preserved_created` after successful old-ref inspection; resource fields report
`absent` only after the complete UUID absence proof. Unavailable inspection or
an unexpected old created tip yields an ineligible preview. Existing refs are
never deleted or reset by replacement, including when the old assignment moves
to another name. A new target must be unassigned and free of lifecycle guards;
the old reservation may be transferred to the same name.

The captured source, normalized policy, or request selection (branch, choice,
or local source selector) must have changed; changing only the key is
insufficient. The stored `ref_cas_intent` is a planned Git CAS, not evidence
that it ran. Replacement binds fresh ref facts and preserves any observed ref
without inferring cleanup authority from that marker. Later phases, unexpected
UUID resources, attachments, active workers, and unavailable native observations
remain ineligible. This path does not review or clean uncertain runtime effects
or workspace data.

Preview is read-only. Ineligible previews return reasons without a token.
Eligible tokens expire after two minutes and are held by the issuing daemon;
restart invalidates an unconsumed token. A blocked early local request stays
blocked across restart until explicit Retry or exact Create replay; startup
does not issue its planned branch CAS or UUID effects. Confirmation rechecks the exact
reviewed request, source and target/old ref facts, policy, image, plugin selection, old evidence, and
resource absence. Stale facts or an expired token return `busy` without
superseding the old request. Successful confirmation atomically marks the old
operation `superseded`, releases its session assignment, and creates one new
UUID, operation, and immutable request with the new key before scheduling its
worker. Evidence records `supersedes_operation_id`, `supersedes_uuid`, and a
confirmation-token hash. Repeating an accepted confirmation with the same key,
old UUID, and token returns that operation, including after restart. Reusing
its key with different confirmation inputs conflicts. Retrying the superseded
operation returns `busy`; exact replay of its original `session.create` key
returns the superseded operation without scheduling the old creation.

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
unrecognized workspace state blocks for repair. `ready`
requires the Incus instance running and `p-interactive.service` observed
active and running. A running instance alone reports `starting` while
readiness is unresolved.

## Workspace inspection foundation

This read-only foundation passed review and its selected VM gate; see
[implementation progress](implementation-progress.md). It is not a destructive
loss preview and cannot authorize Discard/Delete or workspace mutations.

The host method `workspace.inspect` accepts
`{"v":1,"key":"idempotency-key","uuid":"session-UUID"}` and returns
`v` and a durable `operation`. Poll `operation.inspect` with that operation's
ID. A successful result in `operation.evidence.result` contains `branch`, an
optional `head_oid`, `changes` (`code`, `path`), and `refs` (`name`, `oid`,
optional `upstream`). Status codes follow Git porcelain v1. Session RPC cannot
invoke this method.

Inspection requires an established, unattached session and spare container
capacity for one disposable helper. P records the helper identity before
creation, uses the pinned base with no network or session endpoints, and runs
fixed Git queries against a sanitized workspace copy. It never activates the
source. The helper enforces one CPU, 768 MiB memory, and 256 processes. The
current `dir` pool does not establish a disk quota; the copied data is bounded
and Git queries are fixed and read-only. A running source is temporarily
frozen; a stopped source stays stopped.
An unresolved operation holds a durable guard against Start, Stop, and Attach.
`operation.retry` reinspects the same accepted identities; an uncertain native
request is not permission to create a second helper or release the guard.

The initial scan is limited to 512 entries, 16 MiB total file contents, 2 MiB
per file, and depth 24. It refuses escapes, nested mounts, unsupported Git
configuration, linked worktrees, and other unsupported layouts explicitly.
An incomplete scan never produces a clean result. The separate loss-inspection
method below provides bounded linked-worktree and retention evidence.
[Removal previews and confirmation](#session-removal-preview) govern the
implemented Discard and Delete lifecycle methods.

## Workspace loss inspection

`workspace.loss.inspect` accepts `{"v":1,"key":"idempotency-key","uuid":"session-UUID"}`
and returns a durable operation. It passed review and selected VM29; see
[implementation progress](implementation-progress.md). It produces read-only
evidence and does not authorize removal.

On completion, `operation.evidence.result` has schema `p.workspace-loss/v1`:

| Field | Meaning |
|---|---|
| `worktrees` | Original runtime paths, branch/head when available, porcelain changes, and ignored-file `count` and `logical_bytes`. |
| `external_worktrees` | Empty when no linked worktree is found; worktree pointers outside the narrow runtime-owned paths refuse inspection, even when ordinary filesystem grants are present. |
| `local_refs` | Local ref names, object IDs, and available upstream names. |
| `local_only_commits` | Stored local commit objects not retained by the captured P branches, including unreferenced commits. |
| `p_refs` | Captured P branch names and object IDs used for retention comparison. |
| `fingerprint` | A hash binding source contents/metadata, runtime identity, policy, and retained-ref evidence. It is not a confirmation token. |
| `runtime_data_will_be_removed` | Warns that removal would also discard runtime processes and private filesystem state outside Git. |

The scan supports `/workspace` and up to eight Git-known linked worktrees at
`/workspace/<name>` or `/home/p/worktrees/<name>`, with checked literal Git
pointers and mount/ancestor evidence. Protected home roots, unsupported Git
layouts, and out-of-policy paths are unavailable. The aggregate copy retains
the foundation's 512-entry/16-MiB limits; commit inventory is limited to 256
and P branch inventory to 1,024. The final result must fit 12 KiB. Exceeding a
bound refuses the report rather than returning partial loss evidence.

## Session removal preview

`session.removal.preview` is a host-only, read-only method. The current patch
passed focused tests, retained review, and selected VM30; see
[implementation progress](implementation-progress.md). It does not remove a
session or authorize removal by itself.

For a reachable runtime, first complete `workspace.loss.inspect`, then call:

```json
{"v":1,"uuid":"session-UUID","kind":"discard","loss_operation_id":"completed-loss-operation-UUID"}
```

Use `kind:"delete"` to include branch-loss evidence. The completed loss
operation must belong to the same session, be no older than two minutes, and
match the current native identity, status, image, and P ref set. The preview
labels its runtime loss as a **snapshot** with the loss operation ID,
`observed_at`, and `fingerprint`; a resumed runtime may change afterward.

When Incus authoritatively reports the runtime absent, omit
`loss_operation_id` and pass `"acknowledge_missing_runtime":true`. The result
then has `runtime.condition:"missing"` and
`runtime.runtime_loss_unknown:true`. An unreachable Incus authority does not
qualify as an absent runtime. Missing-runtime preview/confirmation and durable
removal require a full confined-project inventory with no matching session
UUID. A renamed or competing instance produces a refusal without a token or
newly accepted action. The proof is repeated before the authority commit and
final row removal, including after a present-runtime deletion.

The result is `{"v":1,"preview":{...}}`. The preview identifies the session,
project, assigned ref and tip (tip omitted for an unborn branch), policy,
runtime identity or missing state, a short-lived `confirmation_token`, and
`expires_at`. A present runtime includes the complete bounded workspace loss
report. Delete also includes `branch_loss`: commits that lose P reachability,
the captured P refs, and origin preservation evidence. The origin object names
the observed URL and has status `local_only`, `observed`, or `unknown` plus
containing and unresolved refs. Origin comparison is evidence, not a removal
permission. Confirmation rechecks all facts under quiescence. Discard passed
selected VM31 and Delete passed selected VM32, using dummy Codex credential
files without authentication.

`session.discard` accepts
`{"v":1,"key":"idempotency-key","uuid":"session-UUID","confirmation_token":"32-hex-token"}`
and returns `{"v":1,"operation":{...}}`. It accepts only a Discard preview.
The durable operation reports `failed` at `stale` if fresh quiescent workspace
or P-ref facts differ before the removal commit point; it restores only a
source this operation froze and leaves the branch and session intact. A valid
confirmation commits removal, disables session authority, removes the exact
owned runtime and P-owned endpoint/key, and ends the assignment while retaining
the confirmed P branch. The same key and request replay the operation.
An uncertain native stop/delete stays guarded and blocked for targeted repair;
P does not retry a name-targeted request against a possible replacement.

`session.delete` accepts
`{"v":1,"key":"idempotency-key","uuid":"session-UUID","confirmation_token":"32-hex-token"}`
and returns `{"v":1,"operation":{...}}`. It accepts only a Delete preview.
The same pre-commit stale checks and exact runtime, endpoint, and key cleanup
apply. After runtime absence, it removes only the assigned P branch through an
expected-old-tip check; sibling P refs and origin refs remain. A mismatched or
uncertain branch effect stays guarded for targeted repair. The same key and
request replay the durable operation.

`session.rename` accepts
`{"v":1,"key":"idempotency-key","uuid":"session-UUID","new_branch":"renamed","expected_old_tip":"<40-hex P commit>"}`
and returns `{"v":1,"operation":{...}}`. The established session must have
the exact nonempty assigned P tip, an ordinary local Git branch, and an exact
running or stopped runtime. The new branch must pass Git validation and be
absent in P and the local workspace. The durable operation guards both P ref
names, quiesces the exact runtime, and creates the new P ref at the expected
old tip as its commit point. It then moves the local branch without changing
its tip or files, changes the assignment and principal policy, deletes the old
P ref by expected-tip check, and restores only a runtime it froze. Local commits
ahead of P, untracked files, private session state, and sibling refs remain.
The same key and exact request replay the operation. A proven refusal before
any effect may fail and release the guards; an uncertain Incus or Git effect
remains blocked for exact recovery or targeted repair. The current supported
workspace layout is a single ordinary worktree with loose branch refs; other
layouts return unavailable before the ref commit point.

`session.repair.prepare` accepts
`{"v":1,"key":"idempotency-key","uuid":"session-UUID"}` and returns a
durable `session.repair.prepare` operation. It applies when the recorded
derived image is missing and the pinned P base image remains available. It
pins the committed assigned P tip and resolves its environment key in a
restricted disposable builder. It does not create a session runtime or
authorize repair. An uncertain builder effect stays guarded; completion
requires exact builder absence and changes no accepted session image.

`session.repair.preview` accepts `{"v":1,"uuid":"session-UUID"}` or the same
request with `"preparation_operation_id":"operation-UUID"` after preparation
completes. It returns `{"v":1,"preview":{...}}`. The plan is
`kind:"missing_runtime"` and names the same UUID, project, assigned branch
and committed P tip, recorded image fingerprint and its `image_status`, policy
and registered credential fingerprints, exact Incus project and instance
name, original `image_source_commit`, `image_source_differs`, and
`runtime_local_loss:"unrecoverable"`. A blocked plan has `eligible:false` and
`blocked_reason`, with no confirmation token. A fresh exact-absent plan has a
short-lived `confirmation_token` and `expires_at`. A failed expected-name
Incus inspection is an RPC error, never a missing-runtime observation. The preview is
read-only. Eligibility requires an absence proof across the confined project's
native inventory; a same-UUID runtime under another name produces
`runtime_absence_unverified` without a token, as does an unavailable or
ambiguous full inventory after a successful expected-name inspection.
Confirmation rechecks that proof, and ordinary native init repeats it
before any creation effect marker. For a missing recorded image it requires
the exact completed
preparation and adds `preparation_operation_id` plus `environment`:
`source_commit`, `system`, `selection`, optional `environment_key`, pinned
base/recorded image fingerprints, optional recorded key, `identity_differs`,
`source_differs`, and `image_status` (`base`, `cache_hit`, or `needs_build`).
`candidate_image_fingerprint` appears only for an already available candidate
image.

`session.repair.confirm` accepts
`{"v":1,"key":"idempotency-key","uuid":"session-UUID","confirmation_token":"32-hex-token"}`
and returns `{"v":1,"operation":{...}}`. It accepts only that fresh eligible
missing-runtime plan, rechecks the exact P tip, policy, credential and image,
and reserves the session and ref before using the reviewed image and same
session key to recreate the runtime. It checks the registered private key
without creating or rotating it. A prepared confirmation binds the completed
preparation digest, selected key, tip, policy, and provenance. If needed, the
confirmed operation re-resolves that key, realizes and publishes an image
under a durable intent, then accepts the exact image before runtime init.
The accepted session image changes only when the same-UUID repair completes.
The repaired workspace records the image's actual source commit separately
from the reviewed committed P tip it checks out. Runtime-local files,
commits, processes and conversations cannot be recovered. The native init
attempt is durably marked before submission. An uncertain init or start stays
guarded; retry accepts only exact positive native identity and never issues a
second possibly delayed init. The same key and exact request replay the
operation. Other repair shapes remain unavailable
in this gate.

`session.ref.repair.preview` accepts
`{"v":1,"uuid":"session-UUID","loss_operation_id":"completed-workspace-loss-operation-UUID"}`
and returns `{"v":1,"preview":{...}}`. The preview reports
`kind:"missing_assigned_ref"`, exact `session_uuid`, `project`, `branch`,
`assigned_ref`, `assigned_ref_status`, `local_tip`, runtime status and
Incus UUID/generation, image and policy fingerprints, registered credential
fingerprint, loss operation/fingerprint, workspace `changes` and `ignored`
summary, and `unsafe_reasons`. An eligible bare-present local commit has a
short-lived `confirmation_token` and `expires_at`; `p_object_missing` is an
unsupported MVP repair case with no token. It means the inspected commit
exists only inside the runtime; this API does not transfer it into P's bare
repository or alter the runtime. Only one ordinary runtime-owned worktree on
the assigned branch is supported in this gate.

`session.ref.repair.confirm` accepts
`{"v":1,"key":"idempotency-key","uuid":"session-UUID","confirmation_token":"32-hex-token"}`
and returns a durable `session.ref.repair` operation. It rechecks the selected
runtime, principal, policy and absent P ref, freezes a Running source, verifies
the complete workspace bytes against the reviewed inspection, and attempts
only an absent-ref compare-and-swap at the local tip. A stopped source remains
stopped. Exact positive ref presence after an uncertain create can complete
forward; absence after an issued request cannot authorize a second attempt.

`session.principal.repair.preview` accepts `{"v":1,"uuid":"session-UUID"}`
and returns `{"v":1,"preview":{...}}`. The read-only preview reports
`kind:"session_git_principal"`, exact session/project/branch, old fingerprint,
assigned-ref presence and tip,
`registration_status` (`missing`, `revoked`, or `active`), host `key_status`
(`missing`, `mismatch`, `matching`, or `unsafe`), guest `guest_key_status`
and its bounded content digest, runtime status and exact Incus
UUID/generation, image and policy fingerprints, `unsafe_reasons`, and
`eligible`. An eligible preview has a short-lived `confirmation_token` and
`expires_at`; a running or changed runtime has no token.

`session.principal.repair.confirm` accepts
`{"v":1,"key":"idempotency-key","uuid":"session-UUID","confirmation_token":"32-hex-token"}`
and returns a durable `session.principal.repair` operation. It requires the
reviewed assignment, policy, old credential facts, and exact stopped runtime.
The operation stages one new key, disables old Git authority before installing
the replacement into the fixed stopped runtime, and preserves the same UUID.
An uncertain native file write remains guarded for exact positive recovery.

`session.record.repair.preview` accepts `{"v":1,"uuid":"session-UUID"}`
and returns `{"v":1,"preview":{...}}`. It reports the exact session/project/
branch, registered Git principal and active flag, current `runtime_status`
and `assigned_ref_status`, trusted Incus project and deterministic name,
`external_authority:"none_registered"` for the current MVP selection, and
`unsafe_reasons`. Only authoritative absence of both the exact runtime and
assigned P ref produces `eligible:true`, a short-lived `confirmation_token`,
and `expires_at`. Unreachable Incus or a same-UUID competing instance cannot
be reviewed as absence.

`session.record.repair.confirm` accepts
`{"v":1,"key":"idempotency-key","uuid":"session-UUID","confirmation_token":"32-hex-token"}`
and returns a durable `session.record.repair` operation. It rechecks both
absences under the session and P-ref guards, disables the session Git
principal with a durable `authority-disabled` phase, removes only that UUID's
local key and endpoint, and then removes the registry row. Reappearance or an
unknown readback blocks forward completion. It never deletes Git refs, an
Incus instance, an image, or external data.

## Explicit environment cache collection

This API passed review and its selected 7c3 VM gate; see
[implementation progress](implementation-progress.md). Collection is a host
action. Session RPC cannot invoke it.

| Method | Params | Result |
|---|---|---|
| `environment.cache.list` | `{"v":1,"project":"team/app","limit":1..8,"after":"optional-environment-key"}` | `v`, `images`, and `next` in environment-key order. |
| `environment.cache.preview` | `{"v":1,"project":"team/app","environment_key":"<64 hex digits>","limit":1..20}`; include the returned `confirmation_token` and `after` cursor for later related-session pages | `v`, `preview` containing `item`, `related_sessions`, `related_next`, `warning`, `confirmation_token`, and `expires_at`. |
| `environment.cache.collect` | `{"v":1,"key":"idempotency-key","confirmation_token":"<preview token>"}` | `v`, a durable `environment.collect` operation; poll `operation.inspect`. |

Each image item contains the project, environment key, exact fingerprint, base
fingerprint, creation time, optional last-use time, logical size in bytes,
`image_status` (`present` or `missing`), and related-session count. Last-use
records accepted session establishment, not inventory reads. Older entries may
lack last-use metadata; if their image is also missing, logical size can be
zero because no size observation was recorded. A preview lists
related session UUIDs, branches, and registry states. Follow `related_next`
with the same token to review the remaining sessions.

The two-minute preview binds the observed image generation, metadata,
presence, and related-session set. A changed or expired preview is refused
before a collection operation is accepted. Daemon restart invalidates unused
preview tokens. Accepted intent is durable: Retry targets only that reviewed
fingerprint and exact cache entry, without another confirmation. Repeating an
accepted idempotency key with its original token returns the recorded
operation, even after token expiry.

Collection removes the exact owned image and then its index entry. An
externally missing image permits index-only cleanup. Existing private instance
roots remain independent, but the preview warns that exact recreation may be
unavailable if a runtime is later lost and its branch environment has changed.
Collection is never a side effect of session Stop, Discard, or Delete.

An unresolved accepted collection blocks new creation and creation retries in
that project until exact cleanup completes; it does not block established
session inspection, Start, or Stop. A project admits only one unresolved
collection. Image ownership or metadata drift blocks destructive retry rather
than selecting another generation.

## Terminal attachment

Run `p attach /absolute/control.sock SESSION_UUID` on the P host, directly or
inside a client-initiated SSH terminal. The CLI restores local terminal settings
on exit and forwards terminal size changes. TUI navigation is not implemented.

`session.attach` returns a 30-second, one-use pending token after the selected
runtime WASI capability and native checks approve the fixed
`/usr/libexec/p/attach` command. The public spec grants no general Incus access.
The CLI transfers the token to a separate temporary `p attach-helper` process
through an inherited private socket; the token never enters argv.

The daemon admits `attachment.claim` only from a Unix peer running the exact
same executable in its fixed helper mode. The dedicated connection can then
call only `attachment.confirm` and `attachment.ping`; ordinary API callers
cannot assert presence. Claim returns the native launch details privately.
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

## Errors and current capability boundary

Errors have `{ "code": integer, "kind": string, "message": string }`.
Messages are bounded and contain no raw database diagnostics. The supported
kind/code pairs are `parse_error`/`-32700`, `invalid_request`/`-32600`,
`method_not_found`/`-32601`, `invalid_params`/`-32602`, `internal`/`-32603`,
`cancelled`/`-32001`, `unsupported_version`/`-32002`, `busy`/`-32003`, and
`unavailable`/`-32004`. Invalid request IDs are returned as JSON `null`.

Without trusted runtime configuration, lifecycle mutations and inspection
remain unavailable. Other methods under `project.`, `session.`, `origin.`,
`runtime.`, and `status.` still return `unavailable` unless listed above. The
implemented repair subset includes missing-runtime and bare-present
missing-assigned-ref plans. Other repair and abandonment plans remain
unavailable. `project.branches`
and `project.retained_branches` observe Git refs through the configured source
package; they do not mutate lifecycle state.
