# P implementation rework plan

Status: **reviewed; step 0 in progress**. This file is the working plan for the
implementation rework requested on 2026-11-01. It is not a design authority:
[PROJECT.md](PROJECT.md) and its authority map still own behavior, and this
rework changes no documented contract. Decisions taken along the way wait for
the maintainer on [TO-DECIDE.md](TO-DECIDE.md); problems deliberately left are
in [DEBT.md](DEBT.md).

## Fixed interfaces

The rework keeps these unchanged. A change to any of them is a break and goes
on TO-DECIDE.md before it is made.

| Interface | Owner | What stays fixed |
|---|---|---|
| Control API | [control-api.md](docs/control-api.md) | Method names, params, results, error codes and kinds, `system.capabilities` lists and their order, framing limits, session socket protocol |
| Plugin contract | [plugin-contract.md](docs/plugin-contract.md) | Package format and digest, activation, capability/grant table, WASI ABI and broker methods, bounds, asset plans |
| CLI | [user-guide.md](docs/user-guide.md) | `p` subcommands, arguments, output and exit codes |
| TUI workflows | [development-validations.md](docs/development-validations.md#live-terminal-browser) | Every screen, key and RPC call; no TUI layout, navigation, control, feedback or terminal-handoff change |
| Durable state | `internal/control` migrations | SQLite schema and migrations 1–21, evidence JSON shapes; no new migration |

## Current system

Sizes are production lines; coverage is Go statement coverage on 2026-11-01.

| Module | Lines | Owns | Writes | Depends on | Coverage |
|---|---|---|---|---|---|
| `cmd/p` | 426 | CLI entry; wires nothing beyond config→store→`daemon.Serve` | — | attachment, control, daemon, gitservice, plugin, tui | — |
| `internal/control` | 12,049 | Four roles in one package: control-API wire types and client `Call`; trusted host config, policy and grants; SQLite store (21 migrations, 146 exported `Store` methods); RPC server (framing, a 779-line handler closure, origin/publication/attachment/service handlers, session socket protocol); plus in-memory attachment counters and status rate limits | All 15 tables; `operations` from 17 files, `sessions` from 9, `git_ref_guards` from 13 | plugin (Git ref and attach types) | 70.6% |
| `internal/daemon` | 11,563 | Lifecycle: one `lifecycle` struct (39 fields, 166 methods), 13 durable operation kinds (preview → confirm → worker), recovery and a 1 s poll, endpoints, events, Git capability, composition in `Serve` | Through `control.Store` (119 methods); `session_keys/` files in 7 files; endpoint sockets; Git repo removal | control, runtimeincus, gitservice, plugin, environmentnix, nixenv, runtimekit | 18.7% |
| `internal/runtimeincus` | 8,702 | Every native Incus effect: sessions, helpers, builders, images, confinement, egress proofs, guest files, workspace freeze/rename/export | Incus instances, images, guest files | control (DTOs), gitservice (snapshot type), plugin, nixenv, runtimekit | 57.4% |
| `internal/gitservice` | 2,888 | Bare repositories, Wish SSH server, pre-receive hook, snapshots, origin fetch/publish | Repositories, hooks, snapshots; SQLite through `*control.Store` | control, plugin | 69.6% |
| `internal/plugin` | 3,066 | Package validation, activation, four WASI runners, file-log, asset plans, managed packages | Event log file, managed package registry | — | 75.2% |
| `internal/tui` | 2,936 | Bubble Tea client | — | control (wire types, `Call`) | — |
| `internal/runtimekit` + `cmd/p-runtime-kit` | 2,214 | Guest-side root helper | Guest files, mounts, network | nixenv | 50.2% |
| `internal/attachment` | 883 | `p attach` client and helper, Incus exec websocket | — | control (wire types, `Call`) | 61.6% |
| `internal/nixenv`, `internal/environmentnix` | 932 | Nix dev-env parsing; resolve/realize/publish pipeline | — (through runtimeincus) | nixenv; environmentnix → runtimeincus, plugin | 85% / 78% |

Interfaces other code relies on: the control API (57 host methods and 3
session-socket methods), the plugin contract (4 WASI runners, 23 broker
methods), the CLI (`p api|tui|attach|daemon|version|git-hook|attach-helper`
and `p plugins defaults|conformance|list|activate|plan-assets|emit|run-event|install|update|remove`),
and the TUI workflows covered by `just tui-tests`. Integration fixtures under
`tests/integration/cmd` also use internal packages and move with them.

## What is worth reworking, ranked by impact

1. **The durable operation has no single owner.** Its rules live in per-kind
   `Store` methods, a generic `AdvanceOperation` (`internal/control/store.go`),
   SQL triggers, and the daemon. Kinds, phases and statuses are bare strings (17
   kinds, 55 phases, `"running"` 131 times in control). The kind list is kept by
   hand in 7 places: `Retry`'s allow-list, the `process` if-chain,
   `unfinishedOperations`, `registryCondition` (all in
   `internal/daemon/lifecycle.go`) and three SQL kind lists in control. The
   skeleton is copied: 14 `Unfinished*` queries, 14 key-replay preambles and 17
   `INSERT INTO operations` sites in control; 10 replay blocks, 9 preview-token
   copies, 13 `block*` and 8 `advance*` helpers in daemon. The copies have
   drifted (diagnostic bounds 512/900/1024, nine kinds' workers emit no
   progress events, three kinds resume blocked work after a restart and the rest
   wait for Retry).
2. **The destructive workers cannot be unit-tested.** `lifecycle` holds a
   concrete `*runtimeincus.Backend` (49 methods, ~205 calls), `*control.Store`
   and `*gitCapability`. Every `process*` worker except two is at 0% unit
   coverage; discard, delete, rename, repair and project delete are checked only
   by the 58 serial VM scenarios.
3. **`internal/control` mixes four roles.** Clients (TUI, attachment, CLI) and
   adapters (runtimeincus) import the SQLite store package to reach wire types,
   and link `modernc.org/sqlite` for it. `Operation` is both a store record and
   a wire type.
4. **RPC dispatch is a decorator chain of closures with type assertions.**
   `StateHandlerWithLifecycle` is one 779-line closure; 22 optional `*API`
   interfaces are discovered by 48 type assertions. The `system.capabilities`
   list is assembled separately from dispatch by mutating
   `map[string]any`. Renaming a daemon method silently removes an RPC method:
   only one compile-time interface check exists.
5. **The WASI runner is written four times** (`internal/plugin/wasi.go`,
   `runtime.go`, `environment.go`, `sourcegit.go`): lease, slots, digest
   recheck, scope nonce, wazero setup, broker host function, bounded output and
   strict result decoding are repeated, with schema constants defined four
   times.
6. **Native sub-protocols are copied in the daemon**: helper VM (3 copies),
   freeze/thaw/rollback (4), builder VM (3), activation-schema ladder (2), and
   `session_keys/` file handling in 7 files with the same mode/owner check
   repeated 5 times.
7. **Adapters reach up or sideways.** runtimeincus returns control types
   (`ServiceResult`) and copies `PublicEgressConfig` back and forth;
   gitservice holds `*control.Store` and checks daemon phases
   (`internal/gitservice/source.go`, `restore_assigned_ref.go`); runtimeincus
   imports gitservice for the snapshot type.
8. **runtimeincus has no Incus client layer**: 7 full `incus list` scans with
   the same validation loop, 7 hand-built `incus exec` argument lists, 6
   HTTP-over-socket clients, metadata keys as literals (`user.p.session_uuid`
   17 times). Three inventory checks hard-code a 4-instance bound.
9. **Two routes to Incus.** The WASI runtime plugin covers 9 command kinds; the
   daemon makes ~205 direct backend calls, including session deletion, so
   `runtime.delete` is never invoked in production.
10. Long functions (16 over 150 lines; `processDiscard` 422), and 21
    copy-pasted migration blocks.

## Designs compared

**A. Incremental restructuring in place.** Keep the module set. Deduplicate
inside each package: journal helpers in control, a kind registry and preview
book in daemon, the handler closure split into functions, one WASI core.
Smallest diffs. It leaves findings 3, 4 and 7 in place: clients still depend on
the store package, config/store/transport stay in one module, capability
discovery stays type-assertion based, and adapters keep importing control.
Warning signs left: information leakage (wire = record = config types in one
package), reachable internals, hand-synced list (capabilities).

**B. Rework behind the same interfaces.** Re-cut the modules by
responsibility while every fixed interface above stays as it is, migrating one
module at a time so each step builds and passes:

| Module | Responsibility | Depends on |
|---|---|---|
| `internal/api` | Control-API contract for clients: wire types, `RPCError`, framing limits, `Call`, socket selection | plugin (the attach spec, a plugin-contract type), strictjson |
| `internal/strictjson` | Closed-schema JSON decoding used at every authority boundary (RPC params, trusted config, native proofs) | — |
| `internal/server` | Control-API server: framing, one method table that drives dispatch and `system.capabilities`, handlers, session socket protocol | api, control, plugin (Git ref types), strictjson; declares the lifecycle interfaces it needs |
| `internal/control` | Durable control state and trusted configuration; an operation journal is the only writer of `operations` rows, with typed kinds and statuses | api, strictjson, plugin |
| `internal/daemon` | Lifecycle: one operation-kind registry (worker, Retry and recovery rules, removal condition), one session-key store; composition in `Serve` | everything below it |
| `internal/plugin` | Unchanged contract; one WASI invocation core under the four runners | — |
| adapters | runtimeincus with one Incus inventory helper; gitservice behind a narrow store interface, without daemon phase knowledge | runtimeincus: api, control (public-egress configuration), gitservice (source snapshot), plugin, nixenv, runtimekit, strictjson; gitservice: control (store types and errors), plugin |

Each module gets one responsibility and each piece of state one owner; the
capability list comes from the dispatch table; clients stop depending on the
store. Trusted configuration stays in control (step 2 was dropped).
Cost: import churn in every package and larger reviews. Warning signs
left: the daemon still calls the concrete runtime backend (finding 2), and
native sub-protocols stay copied (finding 6); both are recorded as debt.

**C. Rewrite on a declarative operation engine** (considered, rejected). Move
all 13 workers onto one phase-table engine with ports for every native effect.
It fixes findings 1, 2 and 6 at once, but the workers' recovery rules are
pinned only by VM scenarios; rewriting them together cannot leave each step
green and reviewable and risks the destructive paths.

**Chosen: B**, because it removes the causes behind findings 1, 4 and 5, most
of 3, and part of 7 (gitservice stops reading daemon phases but keeps control's
types; runtimeincus keeps control's public-egress configuration and
gitservice's snapshot type), instead of trimming their copies, and its steps are mechanical moves or
behavior-preserving consolidations that the compiler, the unit suites and the
VM suite can check one at a time.

## Rules for every step

- One commit per step on `feature/cli-first-mvp`; never push.
- No behavior change: what the fixed interfaces show stays the same, including
  drifted behavior (bounds, error kinds, which kinds resume after restart). A
  drift worth removing goes on TO-DECIDE.md instead.
- No second path: callers move to the new shape and the old one is removed in
  the same step.
- No new dependency and no new migration.
- Checks before each commit: `go vet ./...`, `gofmt -l`, `just unit-tests`
  (Go, Python, VM-selection), `just tui-tests`, `just check`, `just build`.
  Steps that change daemon, control or adapter code paths also run the full VM
  suite (`./dev/test-vm`, serial) on an exported copy of the step before it is
  committed. Step 1 is exempt: it moves declarations between packages without
  changing any statement, which the compiler and unit suites check. Run checks
  with `LD_LIBRARY_PATH` unset.
- VM baseline (2026-11-01, unchanged code at a9fce20): scenario
  `34-missing-runtime-repair.sh` fails every time, so a full run stops there.
  Steps are compared against a baseline run of the other 57 scenarios; the
  scenario 34 failure is investigated separately and reported, not fixed here.
- Each step is reviewed in rounds before its commit.
- Moved tests state the same behavior at the new interface.

## Migration steps

| Step | Change | Findings | VM suite |
|---|---|---|---|
| 0 | Characterization tests | — | no |
| 1 | `internal/api` | 3 | no |
| 2 | dropped: `internal/config` (see below) | 3, 7 | — |
| 3 | `internal/server` with one method table | 3, 4 | yes |
| 4 | Operation journal in control | 1 | yes |
| 5 | Operation-kind registry in daemon | 1 | yes |
| 6 | One session-key store in daemon | 6 | yes |
| 7 | One WASI invocation core in plugin | 5 | yes |
| 8 | Adapter boundaries: runtimeincus inventory and imports, gitservice store interface | 7, 8 | yes |

**0. Characterization tests.** Pin behavior that will move and is thin on
coverage, checked by break and restore against today's code:
- exact `system.capabilities` lists (control only, with Git, with a lifecycle
  that implements every optional API and one that implements none);
- every lifecycle method's parameter validation, routing to its API with the
  decoded arguments, result envelope, and error mapping; absent optional APIs;
  attachment helper methods that are dispatched but not advertised; the answers
  to methods of an unconfigured capability and to unknown methods;
- `Retry` eligibility for each kind and status, and the set and order of work
  `Recover` schedules, observed through the worker queue;
- `registryCondition` for each removal kind;
- the unfinished-operation set for each kind and status, and the shared key
  namespace's replay/conflict rule across kinds.

Steps 6–8 touch code whose coverage is measured when they start; each adds its
own characterization tests first, in its own commit, where coverage is thin.

**1. `internal/api`.** Move the contract that clients use out of control:
`Operation`, `SessionView`, `EnvironmentView`, `OperationSummary`,
`ProjectSummary`, `UnattendedCondition`, `ServiceResult`, `ProjectService`,
`ValidProjectServiceUnit`, `PendingAttachment`, `AttachmentLaunch`,
`WorkspaceInspectionRequiresFreshRequest`, `RPCError`, `ProtocolVersion`,
`MaxFrameBytes`, `Call`, `ClientSocket`, `DefaultControlSocket`, and the
framing that `Call` shares with the server (`ReadFrame`). The strict JSON
decoding that `Call`, the server, configuration and the store share
(`strictDecode`, `RejectDuplicateKeys`) moves to `internal/strictjson`, since
it is not part of the client contract. Its copies in plugin, nixenv,
runtimekit and runtimeincus stay as they are.
control, daemon and adapters use them from api. tui and attachment import api
and no longer import control; runtimeincus no longer needs control for service
results. cmd/p keeps control only where it starts the daemon and manages
packages offline (it opens the store); its client commands use api.
Server-side types stay in control and move with the server in step 3:
`Session`, `ReserveSessionRequest` with `ValidSessionCreateRequest` (moving it
would also move the identifier validators used at about a hundred control call
sites, for no client), the removal-preview types, `SummarizeOperation` and
`CreationEnvironmentView` (they decode creation evidence). The tui-mock
fixture is a server double and keeps importing control for these.

**2. `internal/config` — dropped after measurement.** The configuration code
calls the store's identifier validators (`validProject`, `validFingerprint`),
its digest and policy normalization, and returns errors wrapping
`control.ErrInvalid` that the daemon maps to `invalid_params`. Moving it out
cleanly first needs a home below both packages for P's identifier formats and
sentinel errors, which touches about 300 call sites in control alone. That
cost is out of proportion to what it removes (one runtimeincus import and one
role of control), so configuration stays in control; recorded as debt.

**3. `internal/server`.** Move the RPC server, handlers and session socket
protocol out of control; the tui-mock fixture moves to `server.ServeConn`.
Replace the closure chain with one table of methods. Each entry has its
dispatch availability, whether `system.capabilities` advertises it, parameter
decoding and call. Advertisement is separate from dispatch because they differ
today: `attachment.claim`, `attachment.confirm` and `attachment.ping` are
dispatched but not advertised. `system.capabilities` lists the advertised
available entries in today's order. Today's fallbacks stay: an unconfigured
method answers as it does now (for example `invalid_params` when its params are
not exactly `{"v":1}`, `unavailable` for known prefixes, `method_not_found`
otherwise). The daemon asserts at compile time that it implements every
lifecycle interface the server declares.

**4. Operation journal.** Typed `Kind` and `Status` constants (string-valued,
so wire and SQL values are unchanged). One unexported journal in control
inserts operation rows, applies the shared key replay/conflict rule, lists
unfinished operations for a set of kinds, and performs status/phase updates;
the per-kind store methods call it. The 14 `Unfinished*` methods become one
call taking kinds, keeping each kind's order. SQL triggers stay as they are.

**5. Operation-kind registry.** One table in daemon lists each kind's
worker, Retry eligibility, recovery rule and removal condition; `Retry`,
`process`, `Recover`, the poll and `registryCondition` read it. One helper
replaces the ten idempotent-replay blocks. Preview tokens are not consolidated:
each preview family has its own TTL, capacity (32, 64 or 128) and capacity
error, and reaching a family's cap needs eligible previews whose facts come
from native Incus and Git state, so their behavior cannot be characterized
first; the copies are recorded as debt.

**6. Session-key store.** One component owns `session_keys/`: create, check
mode and owner, read, stage and rotate, and exact removal without following
links. The seven call sites use it.

**7. WASI invocation core.** One function in plugin performs lease, slot,
deadline, package re-snapshot and digest check, wazero instantiation with the
broker import and memory checks, bounded I/O and the exit check; the four
runners keep their own command and scope construction and their typed result
decoding (event requests have no scope, and the environment result accepts
fewer fields), and supply their broker handler and limits. Schema constants are
defined once. The ABI and every bound stay as documented.

**8. Adapter boundaries.** runtimeincus: one instance-inventory helper for the
seven `incus list` scans, each caller keeping its bound and checks; metadata
keys as constants. Its control import stays for public-egress configuration
(step 2 was dropped). gitservice: depends on a narrow store
interface instead of `*control.Store`; the daemon passes the creation and
ref-repair checks that today read daemon phases inside gitservice.

## Not in this rework

Recorded in [DEBT.md](DEBT.md): worker testability behind narrow runtime
interfaces (finding 2), copied native sub-protocols (finding 6), long worker
functions, migration readability, runtimeincus Incus exec/HTTP client
consolidation beyond inventory, and the copied preview-token code. On [TO-DECIDE.md](TO-DECIDE.md): unifying the
drifted operation behaviors, routing native effects through the runtime plugin
(finding 9), and the 4-instance inventory bound.

## Progress

| Step | State | Commit | Review | VM suite |
|---|---|---|---|---|
| Plan | reviewed: settled | — | Codex, 3 rounds: challenge 3 findings accepted, 2 cuts; normal 1: 4 accepted, re-raised part settled by cutting the preview book; normal 2: 1 accepted | baseline running |
| 0 | done | 7061a8b | Codex, 2 rounds: 5 + 1 findings accepted, 2 cuts; last fix verified by break-and-restore at the round limit | not needed (tests only) |
| 1 | done | step 1 commit | Codex, 1 round: 1 finding accepted (plan wording) | exempt (declarations moved) |
| 2 | dropped (see step 2) | — | — | — |
| 3–8 | not started | — | — | — |
