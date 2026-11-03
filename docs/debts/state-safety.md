# State-safety risks

## Store transitions check and act in separate transactions

- Where: internal/control retained_delete_store.go `IssueRetainedDelete` and the transition
  methods of rename_store.go
- Problem: Transition methods read the operation in one transaction, run a verification,
  then update it with `WHERE id=?` in another; only the process-wide Git authority lock
  keeps them safe, and `AdvanceOperation` does not take that lock.
  `IssueRetainedDelete` ignores `RowsAffected` before running its effect.
- Evidence: retained_delete_store.go (read, then guarded update, then `effect()`);
  20 of 26 `UPDATE operations` sites filter only on `id` and 3 check `RowsAffected`.
- Risk: a concurrent writer outside the lock could interleave between check and update.
  No failure has been observed.
- Fix: update with the expected status and phase in the `WHERE` clause and check
  `RowsAffected`, inside the transaction that verified.
- Fix when: any new writer of `operations` outside the Git authority lock.
- Left by: the agent, by rule (a problem outside the change; the rework keeps behavior).
- Added 2026-11-01, updated 2026-11-02, 3 cases:
  - 2026-11-01: "complete rework of P's implementation" found it while mapping control.
  - 2026-11-02: "remove session repair" deleted record_repair_store.go,
    principal_repair_store.go and the other repair stores, which held 11 of the 36
    `UPDATE operations` sites; the remaining transition methods are unchanged.
  - 2026-11-02: "deletions without inspection" changed the phases `FailRenamePrecommit` and
    `CompleteRename` accept; both still read, verify and update in separate steps.

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
- Added 2026-11-01, updated 2026-11-02, 2 cases:
  - 2026-11-01: "complete rework of P's implementation" found it while mapping control.
  - 2026-11-02: "deletions without inspection" changed session removal, the "Fix when" of
    this entry, and left it: the interface rework's step 4 keeps only a session's newest
    finished start or stop, which replaces the diagnostic rows (rework/waiting.md).

## Removing a running session blocks while another Incus operation runs on it

- Where: internal/runtimeincus discard.go `StopForDiscardExact` and `DeleteForDiscardExact`
  (`workspaceResourceBusy`); the TUI's services poll, which runs `incus exec` on the
  selected session every few seconds
- Problem: Discard or Delete of a running session checks that no Incus operation is active
  on the container before its forced stop and refuses otherwise. A services read that
  started just before the removal was accepted is such an operation, so the removal
  blocks with "discard source has active Incus operation" instead of waiting for it.
- Evidence: VM scenario 58 (notes TUI) failed once this way in step 3's gate on
  2026-11-03 and passed on rerun; the TUI was observing the selected session's
  services, the likely holder.
- Risk: a removal from the TUI blocks now and then and needs Retry; scenario 58 is flaky.
- Fix: wait for a bounded time for the container's active operations to end before
  refusing, or make a change hold the session's reads out until its effect is issued.
- Fix when: the next change to removal, or when scenario 58 fails this way again.
- Left by: the agent, by rule (a problem outside the change).
- Added 2026-11-03, 1 case:
  - 2026-11-03: "errors that name the next step" met it in the VM gate.

## Project deletion evidence admits fewer images than the review's cap

- Where: internal/control project_delete.go `MaxProjectDeleteImages`,
  `maxProjectDeleteReviewBytes` and `ProjectDeleteReviewFits`; internal/daemon
  project_delete.go `boundProjectDeleteReview`
- Problem: The review accepts up to 20 commit images, but the evidence a deletion is
  confirmed with must encode within 24 KiB, which four sessions and eight images fill when
  the project, branch and builder-version names are as long as P accepts. The review
  refuses a project whose evidence would not fit with `conflict`, reason `bound_exceeded`;
  the confirmation no longer refuses it as invalid.
- Evidence: control's `TestProjectDeleteEvidenceFitsAtTheDiagnosticLimit` measures eight
  images with four sessions at those names; each commit image claim then encodes to about
  1.7 KiB, and its resource to about 0.1 KiB.
- Risk: a project with more images than its evidence can record cannot be deleted through
  P until release deletes the unused ones. A project keeps one image per commit its
  sessions started from, so this takes more images than sessions: images of an older base
  image or builder version kept for a commit that still has sessions, or release held up
  by an unreachable driver.
- Fix: keep image claims smaller, such as the index key and reference with the rest read
  back from the index, or let the evidence bound alone decide and drop the separate cap.
- Fix when: a project deletion is refused this way, or the deletion evidence next changes.
- Left by: the agent, by rule (a problem outside the change).
- Added 2026-11-03, updated 2026-11-03, 2 cases:
  - 2026-11-03: "one diagnostic limit" found it while checking that the evidence fits at
    the new diagnostic limit.
  - 2026-11-03: "one image per source commit", moved onto step 3, replaced environment
    images with commit images, made the review refuse what the evidence cannot record,
    and measured eight images at four sessions with the longest names.

## A builder admitted after its creation's reply was lost is never collected

- Where: internal/runtimeincus driver.go `BuildImage` and `Delete`; internal/daemon image.go
  `buildImage` and `settleBuild`
- Problem: When `incus init` of a builder loses its reply, the build's leftover removal
  finds no builder and the cycle ends `absent`; the next attempt uses a new build reference.
  Incus can still admit the lost request later and create the old builder, stopped. Nothing
  lists builders to find it: the driver's `Inventory`, through which core is meant to delete
  such a late object exactly ([runtime-driver.md](../../rework/runtime-driver.md#settling-uncertain-effects)),
  lands in step 8 (M3).
- Evidence: `Delete` looks for the reference's builder by name and in the instance
  inventory once; the daemon records `absent` when that finds nothing. Before this step the
  same case left the creation blocked ("builder init outcome unknown").
- Risk: a stopped orphan container holds one slot of the Incus project's container limit and
  its storage until someone deletes it by hand. It never starts, so it runs no code.
- Fix: list builders labeled with this P instance in the driver's `Inventory` and delete
  those whose build reference no unfinished creation holds.
- Fix when: step 8's M3 (`Inventory`), or the first time such an orphan is seen.
- Left by: the agent, by rule (behavior left untested: needs the driver's inventory, which
  the plan places in a later step).
- Added 2026-11-02, 1 case:
  - 2026-11-02: "one image per source commit" gave each build attempt its own reference
    and review found the late-admission case.
