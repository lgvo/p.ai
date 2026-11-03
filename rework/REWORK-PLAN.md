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
| Risky changes | A preview method and a confirm method per change, 23 in all, and 13 once groundwork steps 0d and 0h removed the cache and repair methods. The daemon keeps each preview in memory under a random token. | HTTP-style conditional requests, one method per change, called twice: the first call is a dry run that returns a change tag (expiry plus a hash of the change), and the second runs it with `if_match` set to that tag. 7 methods in all, and nothing is stored. Every plan shares one envelope: allowed, refusals, loss, details. | [state-hash-confirmation.md](state-hash-confirmation.md), [plan-shape.md](plan-shape.md) |
| Errors | Nine JSON-RPC error kinds. `busy` and `unavailable` each cover several situations, so clients read the message text. | Each kind names the next step: retry, conflict, precondition failed, precondition required, capacity, not found, not configured, invalid, failed. A panic in a request handler answers `internal` and is logged instead of stopping the daemon. Blocked and failed operations carry a `reason` code beside their diagnostic, and a panic in operation work leaves the operation `blocked` with reason `internal`. One diagnostic size limit for every operation kind (today 512, 900 or 1024 bytes). | [errors.md](errors.md) |
| Wire types | The daemon builds about 40 results as loose maps, and the TUI writes its own struct shapes. Every request carries `"v":1`. | One request type and one result type per method in `internal/api`, shared by the daemon, TUI, CLI and tests. The version is agreed once, at `system.hello`; the in-session socket keeps `"v":1` until step 9 decides. | [wire-types.md](wire-types.md) |
| Waiting | Start and stop return a session view and tell clients to poll. Project deletion blocks inside the request, and the server cuts every request at 30 seconds. | Every change returns an operation. `operation.wait` returns when the operation finishes. Every operation kind emits the same progress events and follows one rule for resuming blocked work after a restart; unfinished operations list in one order. Service actions are the exception: they return once queued, and the service list shows their progress. Origin change and publish become operations; a session keeps only its newest finished start or stop; the session condition gains `stopping`. | [waiting.md](waiting.md) |
| Images | An image holds one environment's Nix packages, named by what the devShell resolves to; the code is cloned at session start. Images pile up until deleted by hand (`environment.cache.*`, used by nothing but the VM tests) or with the project. | One image per source commit, built from everything at that commit and tagged with it. Sessions from the same commit share it and drift on their own. An image is kept while a session started from its commit exists; no cache API. | [commit-images.md](commit-images.md) |
| Repairs | The client picks among four repair families. | Removed by the maintainer on 2026-11-02: discard or delete, then create a new session on the retained branch. Removal accepts a missing branch ref. | [repairs.md](repairs.md) |
| CLI | Deletion and recovery only through `p api` with hand-written JSON, idempotency keys and copied tokens. | Task commands such as `p session delete` and `p origin publish` that preview, show and confirm, with `--force`. | [cli.md](cli.md), `--force` in [state-hash-confirmation.md](state-hash-confirmation.md) |
| Runtime | The WASI runtime plugin decides nothing, most Incus calls bypass it, and core never calls `runtime.delete`. | One owner for runtime effects, decided by the maintainer on 2026-11-02: a native Go runtime driver that every runtime effect goes through, replaceable like a plugin (for example a Kubernetes driver instead of Incus). Images, the session host and project services are the driver's internals; the contract exposes only operations. The WASI runtime plugin goes. | [runtime-driver.md](runtime-driver.md), services in [services-plugin.md](services-plugin.md) |
| Services | Project services are systemd user units; core runs `systemctl` through the WASI runtime plugin's `runtime.services` broker, and the API carries systemd's state names. | Three runtime-driver operations (list, action, logs) with P's own states and a display-only detail; each driver implements services internally. On Incus nothing changes for users or agents. API renames (`unit` → `name`, `session.service.journal` → `session.service.logs`) land in step 2; the move lands in step 8. | [services-plugin.md](services-plugin.md) |
| Plugins | The remaining executable plugins are WASI modules driven through brokers (source-git, Nix environment). | Native executables speaking JSON-RPC, like the runtime driver, where that makes sense; the Nix environment folds into the runtime driver's image build. Decided by the maintainer on 2026-11-02. Executable plugins are trusted code, contained by trusted selection, digest pinning and launch checks, with no sandbox; OS confinement is a later improvement (maintainer, 2026-11-02). | [plugins.md](plugins.md) |
| Source | Git work is split between Go code in `internal/gitservice` and a WASI source-git plugin that decides almost nothing. | One source driver for all source and Git work, replaceable like the runtime driver (a native executable, pinned and launch-checked). Decided by the maintainer on 2026-11-02. Core keeps the SSH server, as a layer that hands authenticated streams to named services, Git the only one; rename's workspace Git edits and Git's syntax (branch and ref names, origin URLs, commit IDs, the SSH command) are the driver's, and core keeps only length and byte bounds (maintainer, 2026-11-02). | [source-driver.md](source-driver.md) |
| Schema | P's SQLite state is built by replaying 21 migrations; the current schema exists only as their sum. | One schema file for fresh installs, edited directly by later steps; no migrations. Follows from fresh installs only, confirmed by the maintainer on 2026-11-02. | this plan |

## Steps

Each step lands as one commit, or a few where it splits cleanly (steps 1 to 4 and 7 took two or three each), after its decision document is settled. The 0 steps need no decision document: each rests only on a decision already made, and each lands as its own commit. Steps 8 and 9 are the exceptions: step 8 landed as the runtime driver's seven migration steps (M1–M7) and step 9 lands as the source driver's (S1–S7) and then the plugin changes, one commit each.

| Step | What | Why in this order |
|---|---|---|
| 0a | **Done** (51d7ac3). Fix the notes TUI test driver so VM scenario 58 passes. | The VM gate should fail only for known reasons before interfaces start moving. |
| 0b | **Done** (804680e). Fix the Start readiness race behind VM scenario 34: report ready only after the start watcher releases the session lock. | A bug fix that needs no design; with 0a it gives a clean VM baseline. Step 4 later makes start an operation. |
| 0c | **Done** (150ad6b). Recover from a panic in a request handler: answer `internal`, log, keep serving. | Decided; uses today's error kinds. |
| 0d | **Done** (dfedb9a). Remove the `workspace.inspect` method and the code only it uses, and `environment.cache.*`. | Decided; nothing but VM scenarios calls them. Images pile up until step 1, which does not matter on a fresh install. |
| 0h | **Done** (02612f6). Remove session repair: its code, its nine methods and its operation kinds ([repairs.md](repairs.md)). | Decided; needs no design. Before 0e, which would otherwise convert ref repair from freezing to stopped-only and adapt its VM scenario only for 0h to delete it. The cost is a few commits in which a session with a missing branch ref cannot be removed, acceptable with fresh installs only and no users. |
| 0i | **Done** (2406b5a). Stop is not refused while a read of the session holds its lock (scenario 58's flake). | A bug fix found by step 0h's VM gate; makes later gates reliable. |
| 0j | **Done** (756b401, 4e221ba). A graceful stop waits for the guest's systemd and forces after the timeout. | A bug fix traced from VM flakes in scenarios 06 and 49; makes later gates reliable. |
| 0k | **Done** (e073c75, 7cfe56a). A removal waits out the Incus execs that a cancelled read leaves behind, and inspection waits out the placeholder Incus answers while it cleans up after a guest powers off. | Bug fixes traced from VM flakes in scenarios 58 and 51; make later gates reliable. |
| 0l | **Done** (3e695d5). Fix the eleven findings that the TUI reviews of steps 3 to 5 left open, such as Retry offered for an operation that is not blocked, and a removal run answered `changed` or `expired` that did not show the current plan. | Left open by earlier TUI reviews; fixed after step 5, the last step those reviews covered. |
| 0m | **Done** (42f2052). Serve VM scenario 37's HTTPS redirect from the test VM instead of httpbin.org, which now refuses redirects to internal addresses. | Scenario 37 was the last known VM failure; since 0m the gate expects every scenario to pass. |
| 0n | **Done** (e0c5b1f). The observer's PTY expansion test waits, with a deadline, for the redraw instead of reading the frame a fixed tenth of a second after the resize. | A TUI test that failed on a loaded machine, recorded as debt; makes later checks reliable. |
| 0o | **Done** (16a4e84). Release a session's in-memory status state, its attachment counts and its status and attempt budgets, when its row is deleted, not only at project deletion. | Recorded as debt: the state grew with every discarded, deleted, cleaned-up or replaced session. |
| 0p | **Done** (aa28dfa, 9c46d03). Two more TUI fix passes. The first fixes the four smaller findings 0l left, such as no cue that a long page continues, and makes the removal review state losses in errtext's words, as the CLI does. The second names a created session by its Create before entering it, and makes every review that scrolls end each screen with how many lines are below, so a Discard at 48x16 shows its loss, or a cue to it, on its first screen. | Left open by the pass before each, and the loss sentences by step 7b. |
| 0e | **Done** (f7dca2e). Deletions without inspection: no loss inspection or origin check before discard, delete or project deletion; P stops running sessions itself; no freezing, so rename requires a stopped session. With the loss inspection, the workspace helpers lose their last consumers, so they go too, with their Git analysis and their capacity reservation. Removal accepts a missing branch ref: delete then has nothing in P's repository to remove, and discard acts as a delete ([repairs.md](repairs.md)). | Decided; removes the loss-inspection path and its Incus calls, so step 5 has less to convert. |
| 0f | **Done** (8ae2d6d). Collapse the 21 migrations into one schema file. | Follows from fresh installs only; later steps edit the schema directly. |
| 0g | **Done** (8fe88c8). Give builders public internet access only, as designed in docs/environment-building.md. | Decided; makes real devShells buildable; the code moves into the driver later mostly unchanged. |
| 1 | **Done** (a86f76c, 7b6c210). Images | Introduces the runtime driver's image operations (`BuildImage`, `Settle` for publishing), writes commit images against them, folds the Nix environment plugin into the image build, gives the builder a Git bundle of the commit to clone instead of a tree checked on the host (gitservice writes it until the source driver's `Export`, S4), and moves builder public access (from 0g) into it. Needs [runtime-driver.md](runtime-driver.md) settled first. Runs in parallel with the API steps, in a second worktree (lane B). |
| 2 | **Done** (26ad1ff, 564daf1, d0d98c9). Wire types | Every later step changes method shapes. With shared types, a changed shape fails to compile at each caller instead of failing in a VM run. |
| 3 | **Done** (dfd1e74, 389310d; da9d917 recorded a debt its review found). Errors | Small, and steps 4 and 5 use the new retry and precondition kinds. |
| 4 | **Done** (0036066, 6da1084, 3d737c0). Waiting | Start and stop become operations and clients wait with `operation.wait`. `project.delete.confirm` already records an operation; the wait inside it, for fresh loss inspections, goes in 0e, so the confirm returns its operation at once, which step 5 needs. |
| 5 | **Done** (b6785bb). Risky changes | Needs the shared types (2), the precondition errors (3) and `operation.wait` (4), with which clients follow the operation each run returns. |
| 6 | Dropped | Session repair is removed in 0h ([repairs.md](repairs.md)). |
| 7 | **Done** (7a 6f3162f, 7b 2b14a2b). CLI | Builds on steps 4 and 5. |
| 8 | **Done** (M1 cbf5383, M2 ef7eb3b, M3 ca73691, M4 52d02e4, M5 758a654, M6 484a083, M7 4208028; [runtime-driver.md](runtime-driver.md#migration)). Runtime | The largest step, and independent of the API. Last of the API-independent work, so it blocks nothing else. Landed as the driver's migration steps, one commit each. |
| 9 | Source driver, then plugins | The source driver lands first, as its migration steps S1 to S7, one commit each ([source-driver.md](source-driver.md#migration)); then the plugin changes replace the remaining WASI plugins with JSON-RPC executables where that makes sense. Both reuse the runtime driver's protocol from step 8. |

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

None remain since step 0m: every VM scenario is expected to pass. The table
keeps the failures earlier gates met and what resolved each.

| Scenario | Cause | Resolved by |
|---|---|---|
| 34, `34-missing-runtime-removal` | Start reported `ready` while its watcher still held the session lock. | Fixed in step 0b (804680e); renamed from the missing-runtime repair scenario in 0h |
| 37, public egress | Depended on an external redirect service that now refuses the probe. | Fixed in step 0m (42f2052): the test VM serves the redirect |
| 51, `51-assembled-failed-create-cleanup`, flaky | Inspection read the placeholder Incus lists while it cleans up after a guest's poweroff as an identity mismatch, so a failed startup's stop gave up and the creation was blocked before its container stopped. | Fixed in step 0k (7cfe56a) |
| 58, notes TUI | The driver expected the session-row format from before 0dfc4ad. | Fixed in step 0a (51d7ac3) |
| 58, notes TUI, flaky | Stop was refused while the TUI's services poll held the session lock. | Fixed in step 0i (2406b5a) |
| 58, notes TUI, flaky | A services read the TUI cancelled left an Incus exec running; the discard worker met it and blocked until Retry. | Fixed in step 0k (e073c75) |

## Not in this plan

Moving configuration and the RPC server out of `internal/control` (steps 2
and 3b of the earlier rework) stays recorded as debt in
[docs/debts/structure.md](../docs/debts/structure.md).

## Waits on you

Nothing right now. The maintainer answered every open question on
2026-11-02, and each document records its answer.
