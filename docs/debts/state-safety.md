# State-safety risks

## Store transitions check and act in separate transactions

- Where: internal/control retained_delete_store.go `IssueRetainedDelete` and the transition
  methods of rename_store.go
- Problem: Transition methods read the operation in one transaction, run a verification,
  then update it with `WHERE id=?` in another; only the process-wide Git authority lock
  keeps them safe, and `AdvanceOperation` does not take that lock.
  `IssueRetainedDelete` ignores `RowsAffected` before running its effect.
- Evidence: retained_delete_store.go (read, then guarded update, then `effect()`);
  20 of 25 `UPDATE operations` sites filter only on `id` and 2 check `RowsAffected`.
- Risk: a concurrent writer outside the lock could interleave between check and update.
  No failure has been observed.
- Fix: update with the expected status and phase in the `WHERE` clause and check
  `RowsAffected`, inside the transaction that verified.
- Fix when: any new writer of `operations` outside the Git authority lock.
- Left by: the agent, by rule (a problem outside the change; the rework keeps behavior).
- Added 2026-11-01, updated 2026-11-02, 2 cases:
  - 2026-11-01: "complete rework of P's implementation" found it while mapping control.
  - 2026-11-02: "remove session repair" deleted record_repair_store.go,
    principal_repair_store.go and the other repair stores, which held 11 of the 36
    `UPDATE operations` sites; the remaining transition methods are unchanged.

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
