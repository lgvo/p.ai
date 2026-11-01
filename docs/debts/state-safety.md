# State-safety risks

## Store transitions check and act in separate transactions

- Where: internal/control retained_delete_store.go `IssueRetainedDelete` and the transition
  methods of rename_store.go, record_repair_store.go, principal_repair_store.go
- Problem: Twelve transition methods read the operation in one transaction, run a
  verification, then update it with `WHERE id=?` in another; only the process-wide Git
  authority lock keeps them safe, and `AdvanceOperation` does not take that lock.
  `IssueRetainedDelete` ignores `RowsAffected` before running its effect.
- Evidence: retained_delete_store.go (read, then guarded update, then `effect()`);
  27 of 36 `UPDATE operations` sites filter only on `id` and 3 check `RowsAffected`.
- Risk: a concurrent writer outside the lock could interleave between check and update.
  No failure has been observed.
- Fix: update with the expected status and phase in the `WHERE` clause and check
  `RowsAffected`, inside the transaction that verified.
- Fix when: any new writer of `operations` outside the Git authority lock.
- Left by: the agent, by rule (a problem outside the change; the rework keeps behavior).
- Added 2026-11-01, 1 case:
  - 2026-11-01: "complete rework of P's implementation" found it while mapping control.

## Per-session state outlives its session

- Where: internal/control lifecycle_store.go `SetSessionStartDiagnostic` (`metadata` rows
  `session.start.diag:<uuid>`) and session_status.go rate maps
- Problem: Start diagnostics are never deleted from `metadata`, and the in-memory status
  rate maps are cleared only on project deletion, not on Discard or Delete.
- Evidence: no `DELETE` of `session.start.diag:` keys anywhere; rate maps cleared only in
  project_delete_store.go.
- Risk: slow growth of `metadata` and memory with session churn.
- Fix: delete both with the session row.
- Fix when: session churn becomes routine, or the next change to session removal.
- Left by: the agent, by rule (a problem outside the change).
- Added 2026-11-01, 1 case:
  - 2026-11-01: "complete rework of P's implementation" found it while mapping control.

## Start reports ready while its watcher still holds the session lock

- Where: internal/daemon lifecycle.go `StartSession` (the watcher goroutine that keeps the
  session lock) and `InspectSession` (the `starting` condition); readiness.go `awaitHostReady`
- Problem: `session.inspect` reports `ready` from its own observation while Start's background
  watcher still owns the session's lock, and `lockSession` refuses instead of waiting. A client
  that acts as soon as it sees `ready`, as docs/control-api.md tells it to poll for, can get
  `busy` from any lock-taking method for up to a few seconds.
- Evidence: VM scenario `34-missing-runtime-repair.sh` fails at its `session.repair.preview`
  with `{"code":-32003,"kind":"busy"}` on unchanged code (a9fce20), 4 of 5 runs; a daemon trace
  shows the preview refused 158 ms before the watcher released the lock. The race dates from
  70caec2 (2026-09-25); recent commits do not touch it.
- Risk: lifecycle requests that follow Start fail spuriously; the full VM suite stops at
  scenario 34, so later scenarios do not run in a full pass.
- Fix: report `starting` while the watcher is active even when the host looks ready, and
  release the lock under the same mutex that clears the watcher's flag; add a unit test that
  inspect never reports `ready` while the watcher runs.
- Fix when: before the next full VM acceptance run.
- Left by: the agent, by rule (a problem outside the change, traced with find-root-cause; the
  fix changes observable behavior, which the rework does not).
- Added 2026-11-01, 1 case:
  - 2026-11-01: "complete rework of P's implementation" found scenario 34 failing at baseline;
    root cause traced in a disposable copy (scratchpad rootcause34-work/brief.md).
