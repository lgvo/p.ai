# Interface rework plan

Status: drafting, 2026-11-02. The earlier implementation rework is finished;
its record is [../REWORK-PLAN.md](../REWORK-PLAN.md).

## Goal

Make P's interfaces smaller and make each one say what it means: the control
API, the plugin contract, the CLI, and the Go interfaces behind them.
Contracts may break. The end result for users must stay the same. P targets
fresh installs only: there is no upgrade path, migration or compatibility
code for existing installations or old settings (maintainer, 2026-11-02).

"The same end result" means:

- every workflow that works today still works, from the TUI, the CLI or the API;
- the same safety checks run, and the same changes are refused;
- the same durable outcomes land in SQLite, Git and the runtime;
- except where the maintainer decided otherwise, as each decision document
  records.

The evidence is the unit tests, the TUI tests and the VM scenarios. A scenario
moves to the new calls but keeps its outcome checks.

## Before and after

Each row is one design decision. Its document holds the details; "to write"
means the document does not exist yet.

| Area | Before | After | Decision |
|---|---|---|---|
| Risky changes | A preview method and a confirm method per change, 23 in all. The daemon keeps each preview in memory under a random token. | HTTP-style conditional requests, one method per change, called twice: the first call is a dry run that returns a change tag (expiry plus a hash of the change), and the second runs it with `if_match` set to that tag. 11 methods in all, and nothing is stored. Every plan shares one envelope: allowed, refusals, loss, details. | [state-hash-confirmation.md](state-hash-confirmation.md), [plan-shape.md](plan-shape.md) |
| Errors | Nine JSON-RPC error kinds. `busy` and `unavailable` each cover several situations, so clients read the message text. | Each kind names the next step: retry, precondition failed, precondition required, capacity, not found, not configured, invalid, failed. A panic in a request handler answers `internal` and is logged instead of stopping the daemon. One diagnostic size limit for every operation kind (today 512, 900 or 1024 bytes). | to write |
| Wire types | The daemon builds about 40 results as loose maps, and the TUI writes its own struct shapes. Every request carries `"v":1`. | One request type and one result type per method in `internal/api`, shared by the daemon, TUI, CLI and tests. The version is agreed once, at `system.hello`. | to write |
| Waiting | Start and stop return a session view and tell clients to poll. Project deletion blocks inside the request, and the server cuts every request at 30 seconds. | Every change returns an operation. `operation.wait` returns when the operation finishes. Every operation kind emits the same progress events and follows one rule for resuming blocked work after a restart; unfinished operations list in one order. Service actions are the exception: they return once queued, and the service list shows their progress. | to write |
| Images | An image holds one environment's Nix packages, named by what the devShell resolves to; the code is cloned at session start. Images pile up until deleted by hand (`environment.cache.*`, used by nothing but the VM tests) or with the project. | One image per source commit, built from everything at that commit and tagged with it. Sessions from the same commit share it and drift on their own. An image is kept while a session started from its commit exists; no cache API. | [commit-images.md](commit-images.md) |
| Repairs | The client picks among four repair families. | `session.repair` diagnoses the session and returns the plan that applies. | to write |
| CLI | Deletion and recovery only through `p api` with hand-written JSON, idempotency keys and copied tokens. | Task commands such as `p session delete` and `p session repair` that preview, show and confirm, with `--force`. | `--force` in [state-hash-confirmation.md](state-hash-confirmation.md); the rest to write |
| Runtime | The WASI runtime plugin decides nothing, about 200 Incus calls bypass it, and core never calls `runtime.delete`. | One owner for runtime effects, decided by the maintainer on 2026-11-02: a native Go runtime driver that every runtime effect goes through, replaceable like a plugin (for example a Kubernetes driver instead of Incus). Images, the session host and project services are the driver's internals; the contract exposes only operations. The WASI runtime plugin goes. | [runtime-driver.md](runtime-driver.md), services in [services-plugin.md](services-plugin.md) |
| Services | Project services are systemd user units; core runs `systemctl` through the WASI runtime plugin's `runtime.services` broker, and the API carries systemd's state names. | Three runtime-driver operations (list, action, logs) with P's own states and a display-only detail; each driver implements services internally. On Incus nothing changes for users or agents. API renames (`unit` → `name`, `session.service.journal` → `session.service.logs`) land in step 2; the move lands in step 8. | [services-plugin.md](services-plugin.md) |
| Plugins | The remaining executable plugins are WASI modules driven through brokers (source-git, Nix environment). | Native executables speaking JSON-RPC, like the runtime driver, where that makes sense; the Nix environment folds into the runtime driver's image build. Decided by the maintainer on 2026-11-02. | to write |
| Schema | P's SQLite state is built by replaying 21 migrations; the current schema exists only as their sum. | One schema file for fresh installs, edited directly by later steps; no migrations. Follows from fresh installs only, confirmed by the maintainer on 2026-11-02. | this plan |

## Steps

Each step lands as one commit, after its decision document is settled. The 0 steps need no decision document: each rests only on a decision already made, and each lands as its own commit. Step 8 is the exception: it lands as the runtime driver's migration steps, one commit each.

| Step | What | Why in this order |
|---|---|---|
| 0a | Fix the notes TUI test driver so VM scenario 58 passes. | The VM gate should fail only for known reasons before interfaces start moving. |
| 0b | Fix the Start readiness race behind VM scenario 34: report ready only after the start watcher releases the session lock. | A bug fix that needs no design; with 0a it gives a clean VM baseline. Step 4 later makes start an operation. |
| 0c | Recover from a panic in a request handler: answer `internal`, log, keep serving. | Decided; uses today's error kinds. |
| 0d | Remove the `workspace.inspect` method and the code only it uses, and `environment.cache.*`. | Decided; nothing but VM scenarios calls them. Images pile up until step 1, which does not matter on a fresh install. |
| 0e | Deletions without inspection: no loss inspection or origin check before discard, delete or project deletion; P stops running sessions itself; no freezing, so rename and ref repair require a stopped session. With the loss inspection, the workspace helpers lose their last consumers, so they go too, with their Git analysis and their capacity reservation. | Decided; removes the loss-inspection path and about 45 Incus calls, so step 5 has less to convert. |
| 0f | Collapse the 21 migrations into one schema file. | Follows from fresh installs only; later steps edit the schema directly. |
| 0g | Give builders public internet access only, as designed in docs/environment-building.md. | Decided; makes real devShells buildable; the code moves into the driver later mostly unchanged. |
| 1 | Images | Introduces the runtime driver's image operations (`BuildImage`, `Settle` for publishing), writes commit images against them, folds the Nix environment plugin into the image build, and moves builder public access (from 0g) into it. Needs [runtime-driver.md](runtime-driver.md) settled first. |
| 2 | Wire types | Every later step changes method shapes. With shared types, a changed shape fails to compile at each caller instead of failing in a VM run. |
| 3 | Errors | Small, and steps 4 and 5 use the new retry and precondition kinds. |
| 4 | Waiting | Makes project deletion an operation, which step 5 needs. |
| 5 | Risky changes | Needs the shared types (2), the precondition errors (3) and project deletion as an operation (4). |
| 6 | Repairs | Builds on the single repair method from step 5. |
| 7 | CLI | Builds on steps 4 to 6. |
| 8 | Runtime | The largest step, and independent of the API. Last of the API-independent work, so it blocks nothing else. Lands as the driver's migration steps, one commit each. |
| 9 | Plugins | Replaces the remaining WASI plugins with JSON-RPC executables where that makes sense, reusing the runtime driver's protocol from step 8. |

Every step:

- changes the code, its tests and the document that owns the subject
  (`docs/control-api.md`, `docs/session-lifecycle.md`,
  `docs/plugin-contract.md`, `docs/user-guide.md`, and for steps 1 and 8 also
  `docs/runtime-isolation.md`, `docs/environment-building.md`,
  `docs/technology-stack.md`, `docs/communication-boundaries.md` and
  `GLOSSARY.md`) together;
- passes `go vet`, the unit tests, the TUI tests, `nix flake check` and
  `nix build`, and the VM scenarios pass or match the known failures below;
- is reviewed in rounds before its commit, and is never pushed.

## Known VM failures

| Scenario | Cause | Resolved by |
|---|---|---|
| 34, missing-runtime repair | Start reports `ready` while its watcher still holds the session lock. | Step 0b |
| 37, public egress | Depends on an external redirect service that now refuses the probe. | Stays a known failure |
| 58, notes TUI | The driver expects the session-row format from before 0dfc4ad. | Step 0a |

## Not in this plan

Moving configuration and the RPC server out of `internal/control` (steps 2
and 3b of the earlier rework) stays recorded as debt in
[docs/debts/structure.md](../docs/debts/structure.md).

## Waits on you

Nothing right now.
