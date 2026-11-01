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
