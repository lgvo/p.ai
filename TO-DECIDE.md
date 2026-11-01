# To decide

What waits on the maintainer's decision. Agents add entries below, and remove each once its
decision is applied.

## Handed over by agents

### Target structure of the implementation rework

- Where: REWORK-PLAN.md, "Designs compared"
- What: The rework re-cuts the modules by responsibility (`internal/api`, `internal/config`,
  `internal/server`, an operation journal in `internal/control`, an operation-kind registry in
  `internal/daemon`, one WASI core in `internal/plugin`) behind the unchanged control API,
  plugin contract, CLI and TUI. It decides how every later step is shaped.
- Chosen: design B, rework behind the same interfaces, migrated one module per commit. Set
  aside: A, deduplication inside today's packages (leaves clients on the store package and
  type-assertion dispatch); C, a rewrite of all 13 workers on one phase-table engine (their
  recovery rules are pinned only by VM scenarios, so it cannot stay green step by step).
- Needs from you: confirm or reverse
- From: the agent, on "complete rework of P's implementation", 2026-11-01; coding-practice
  design moment (at least two structurally different designs).

### Drifted operation behaviors are kept as they are

- Where: internal/daemon (worker `block*`/`advance*` helpers, `recordProgress` callers,
  `Recover`, `processProjectDelete`, `processCreateCleanup`, preview capacity errors) and
  internal/control (`Unfinished*` ordering)
- What: Copies of the operation skeleton have diverged: diagnostic bounds are 512, 900 or
  1024 bytes; the workers of nine kinds (rename, ref/principal/record repair, both
  retained-branch operations, both workspace inspections, environment collection) emit no
  `operation.progress` events, only the `running` event that Retry emits; project delete, failed-create cleanup and eligible creations resume blocked work
  after a restart while the other kinds wait for Retry; some unfinished lists order by `created_at`, others by
  `rowid`; a full preview book answers `busy` (-32003) for some kinds and `unavailable`
  (-32004) for others.
  Unifying any of these changes what clients or event handlers observe.
- Chosen: the rework keeps each kind's current behavior; step 5 of REWORK-PLAN.md makes the
  Retry and recovery rules explicit per kind in one registry. Set aside: unifying during the
  rework.
- Needs from you: confirm or reverse (say which behaviors to unify, if any)
- From: the agent, on the rework request, 2026-11-01; rework rule "no behavior change".

### Native Incus effects bypass the runtime plugin

- Where: internal/daemon (direct `l.runtime.*` calls on `*runtimeincus.Backend`), plugin
  contract "WASI runtime ABI"
- What: The WASI runtime plugin covers 9 command kinds, but the daemon makes about 205 direct
  backend calls: session deletion, workspace helpers, freeze/thaw, branch rename in the
  guest, identity install, builders and images. `runtime.delete` is never invoked in
  production. A replacement runtime plugin therefore cannot govern these effects, and
  docs/plugin-contract.md and docs/technology-stack.md do not say so.
- Chosen: the rework leaves both routes as they are (changing them changes the plugin
  contract's practical scope). Set aside: routing deletion and other effects through the
  plugin; documenting the bypass.
- Needs from you: confirm or reverse
- From: the agent, while mapping the system for the rework, 2026-11-01.

### Four-instance bound in Incus inventory checks

- Where: internal/runtimeincus `ConfirmSessionRuntimeAbsent`, `checkPublicAddressCollision`,
  and the project-deletion inventory
- What: These three checks refuse when the confined Incus project lists more than 4
  instances, while session capacity accepts `limits.containers` from 2 to 64. Project
  deletion's bound of four sessions is documented in docs/control-api.md; the other two are
  not. A host configured above 4 containers would see record repair and public-session
  creation or Start refuse as "inventory unavailable".
- Chosen: kept as is; the rework moves the bound into each caller of one inventory helper
  without changing it. Set aside: raising the two undocumented bounds to the capacity limit.
- Needs from you: confirm or reverse
- From: the agent, while mapping the system for the rework, 2026-11-01.

### A panic in an RPC handler stops the daemon

- Where: internal/control `ServeConn` (moves to internal/server in step 3)
- What: Request handlers run without `recover`, so a panic in any handler ends the daemon
  process. systemd restarts it, and durable recovery resumes work, but every connection and
  attachment lease drops.
- Chosen: kept as is during the rework. Set aside: recovering per request and answering an
  internal error.
- Needs from you: confirm or reverse
- From: the agent, while mapping the system for the rework, 2026-11-01.

### Debt is recorded in DEBT.md and docs/debts/

- Where: DEBT.md, docs/debts/
- What: The rework leaves known problems unfixed on purpose; they need one place to live.
  The project had no debt record, and docs/AGENTS.md governs what lives under docs/.
- Chosen: DEBT.md at the root with kind files under docs/debts/, as records, not design
  documents. Set aside: listing them in docs/missing-pieces.md or in REWORK-PLAN.md.
- Needs from you: confirm or reverse
- From: the agent, on the rework request, 2026-11-01; coding-practice technical-debt record.

### Configuration and the RPC server stay in the store package

- Where: REWORK-PLAN.md, steps 2 and 3b; internal/control config.go, rpc*.go, attachment.go,
  session_rpc.go
- What: The reviewed plan moved trusted configuration (step 2) and the RPC server (step 3b)
  out of `internal/control`. Measured at the start of each step, both first need a new home
  for identifier validators and sentinel errors shared with the store (about 300 call sites in
  control, more in the daemon), so both were deferred and recorded as debt
  (docs/debts/structure.md, "Trusted configuration and the RPC server live in the store
  package"). The method table (step 3a) was done inside control.
- Chosen: defer steps 2 and 3b and do the higher-value steps 4–8 first. Set aside: moving
  them with duplicated validators (two copies of one rule), or adding the identifier package
  within this rework.
- Needs from you: confirm or reverse
- From: the agent, on the rework request, 2026-11-01; coding-practice "keep it small".

### Fix the Start readiness race that fails VM scenario 34

- Where: docs/debts/state-safety.md, "Start reports ready while its watcher still holds the
  session lock"; internal/daemon lifecycle.go `StartSession`, `InspectSession`
- What: A pre-existing product race makes `34-missing-runtime-repair.sh` fail on most runs, so
  a full VM pass stops there. The fix changes when clients see `ready` (they see `starting`
  slightly longer), which the rework deliberately does not do.
- Chosen: not fixed in the rework; each VM gate runs the other 57 scenarios and compares them
  with a baseline. Set aside: fixing it now (a behavior change), or changing only the test to
  retry after `ready` (leaves the product race).
- Needs from you: allow the fix (it is ready to make as a separate change), or decide otherwise
- From: the agent, on the rework request, 2026-11-01; find-root-cause trace by a subagent,
  reviewed by Codex (settled).

### Fix the notes TUI driver for tree-style session rows

- Where: docs/debts/checks.md, "Notes TUI scenario expects the old session-row format";
  tests/integration/tui_notes_driver.py
- What: VM scenario 58 fails on every run because its driver still expects the session-row
  text from before 0dfc4ad's browser layout. The product's layout is the reviewed one; only
  the driver is stale. The fix touches test code outside the rework.
- Chosen: not fixed in the rework; VM gates compare scenario 58 with its failing baseline. Set
  aside: fixing the driver now.
- Needs from you: allow the fix (four matcher changes in the driver), or decide otherwise
- From: the agent, on the rework request, 2026-11-01; find-root-cause trace by a subagent.
