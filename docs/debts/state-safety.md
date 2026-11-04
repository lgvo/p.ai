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
- Added 2026-11-01, updated 2026-11-03, 4 cases:
  - 2026-11-01: "complete rework of P's implementation" found it while mapping control.
  - 2026-11-02: "remove session repair" deleted record_repair_store.go,
    principal_repair_store.go and the other repair stores, which held 11 of the 36
    `UPDATE operations` sites; the remaining transition methods are unchanged.
  - 2026-11-02: "deletions without inspection" changed the phases `FailRenamePrecommit` and
    `CompleteRename` accept; both still read, verify and update in separate steps.
  - 2026-11-03: "waiting" ran these updates through the journal's one write path, which
    reports the status changes they make; their `WHERE` clauses and steps are unchanged.

## Project deletion evidence admits fewer images than the review's cap

- Where: internal/control project_delete.go `MaxProjectDeleteImages`,
  `maxProjectDeleteReviewBytes` and `ProjectDeleteReviewFits`; internal/daemon
  project_delete.go `planProjectDelete`
- Problem: The review accepts up to 20 commit images, but the evidence a deletion is
  confirmed with must encode within 24 KiB, which four sessions and eight images fill when
  the project, branch and builder-version names are as long as P accepts. The dry run
  refuses a project whose evidence would not fit: its plan is not allowed, with the refusal
  `bound_exceeded`, so the run never meets it as invalid.
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
- Added 2026-11-03, updated 2026-11-03, 4 cases:
  - 2026-11-03: "one diagnostic limit" found it while checking that the evidence fits at
    the new diagnostic limit.
  - 2026-11-03: "one image per source commit", moved onto step 3, replaced environment
    images with commit images, made the review refuse what the evidence cannot record,
    and measured eight images at four sessions with the longest names.
  - 2026-11-03: "risky changes confirmed with change tags" changed the deletion evidence,
    which meets "Fix when", without the fix: each resource gained `stop_issued`, and
    `planProjectDelete` refuses a plan that does not fit with `bound_exceeded` where
    `boundProjectDeleteReview` answered `conflict`. The 24 KiB bound and the cap of twenty
    images are unchanged.
  - 2026-11-03: "step 9 S2: repositories, heads and commits through the source driver"
    changed the deletion evidence again, which meets "Fix when", without the fix: the
    repository's two directory identities became its incarnation, a string. Eight images
    still fit at four sessions with the longest names.

## A builder admitted after its creation's reply was lost is never collected

- Where: internal/runtimedriver/incus driver.go `BuildImage` and `Delete`; internal/daemon image.go
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
- Added 2026-11-02, updated 2026-11-03, 4 cases:
  - 2026-11-02: "one image per source commit" gave each build attempt its own reference
    and review found the late-admission case.
  - 2026-11-03: "core computes the proofs from the driver's inventory" (step 8, M3) made
    `Inventory` list builders with their build references, which meets "Fix when"; collecting
    orphans was outside that step and the next.
  - 2026-11-03: "step 9 S4: the commit export through the source driver" moved `buildImage`'s
    export onto the source driver and left its builders as they were: an orphan builder is
    still never collected.
  - 2026-11-03: "core holds only the runtime driver" moved the driver to
    internal/runtimedriver/incus unchanged; orphaned builders are still not collected.

## A hung driver is never restarted

- Where: internal/driverwire/launch launch.go `Supervisor.supervise`; internal/driverwire
  client.go `Client.Call`
- Problem: The supervisor restarts a driver, the runtime driver or the source driver, only
  when its process exits or its connection is lost. A driver that stops answering while it
  keeps its connection open is not detected: nothing checks that it still answers, and each
  call waits until its own context ends.
- Evidence: `supervise` waits only on the client's `Done` and the process's exit; `Call`
  returns only on an answer, a lost connection or its context's end. The connection ends on
  its own only when the driver reads nothing for 30 seconds while core writes to it
  (`writeWait`). A worker's driver calls run on the daemon's context, which ends only when
  the daemon stops.
- Risk: a hung runtime driver stalls every runtime operation, and keeps the session locks
  those operations hold, until the operator restarts the daemon; requests answer only when
  their clients give up. A hung source driver does the same to every Git effect, and its
  Git services keep their leases, which keep lifecycle guards waiting.
- Fix: a liveness check on the driver connection, a periodic call with a deadline, that
  kills a driver that misses it, so the supervisor restarts it and calls in flight end as
  after a crash.
- Fix when: a hung driver is seen, or the next change to driver supervision.
- Left by: the maintainer's orchestrator, which accepted it for step 8's M7 (a shortcut the
  change takes).
- Added 2026-11-03, updated 2026-11-03, 3 cases:
  - 2026-11-03: "the runtime driver as its own executable" (step 8, M7) restarts a driver
    that exits or drops its connection and leaves a hung one undetected.
  - 2026-11-03: "the source driver as its own executable" (step 9, S7) supervises
    `p-source-git` with the same launch code, shared with the runtime driver in
    internal/driverwire, and leaves a hung source driver undetected too.
  - 2026-11-03: "step 9 plugin part 2: the event handler as an executable plugin" gave
    launch `Config.Open` and `StopAfterFailedCheck` for the event handler, which is
    restarted when it misses an event's deadline (internal/eventhandler/supervise
    `Supervisor.Handle`); a hung driver is still not detected.

## Executable plugins run with P's authority

- Where: internal/eventhandler/supervise supervise.go `Start`; internal/driverwire/launch
  launch.go `startChecked`; nix/module.nix (the service's sandbox)
- Problem: An executable plugin, the event handler, runs as P's account inside the
  daemon's service sandbox with no confinement of its own. It can read and write P's state
  (SQLite, the Git repositories, the Git server and client keys, staged packages), open the
  Incus user socket and use the network. The WASI runner it replaced allowed none of this.
- Evidence: launch starts the entry with no environment and in a process group of its own,
  but with the daemon's account, groups and namespaces. The NixOS service gives that account
  write access to P's state directory, membership in the `incus` group and no network
  restriction.
- Risk: a selected handler that is malicious or compromised, or a bug in one, can change
  P's authoritative state or any session's runtime through Incus, or send data off the
  host. Only trusted selection, the package digest and the launch checks keep an unreviewed
  package from running.
- Fix: confine the handler before it runs: a separate account without P's state or the
  `incus` group, Landlock rules applied by a small launcher, or systemd sandboxing of the
  process, which rework/plugins.md ("Later improvement") weighs. Confinement brings grants
  back in a new form: a plugin's config names what it may reach.
- Fix when: an executable plugin other than the bundled file-log is to be selected, or the
  next change to how the daemon starts plugins.
- Left by: the maintainer, who decided on 2026-11-02 that executable plugins are trusted
  code and kept OS confinement as a later improvement.
- Added 2026-11-03, 1 case:
  - 2026-11-03: "step 9 plugin part 2: the event handler as an executable plugin" replaced
    the WASI runner and its broker with a native process the daemon starts.

## A driver's or plugin's output is not bounded

- Where: internal/driverwire/launch launch.go `startChecked`
- Problem: launch gives every process it starts, the runtime driver, the source driver and
  the event handler, the daemon's own standard error as its standard output and error, with
  no bound. rework/plugins.md ("Protocol") requires a plugin's output to be bounded.
- Evidence: `startChecked` sets the child's descriptors 1 and 2 to `os.Stderr`; nothing
  reads, limits or rate-limits what the child writes there.
- Risk: a driver or handler that loops on a message fills the daemon's log (the journal on
  the NixOS service) and can hide P's own diagnostics.
- Fix: give the child a pipe that a bounded, rate-limited copier drains into the daemon's
  log, as the WASI runner bounded a module's output.
- Fix when: a driver or plugin floods the log, or the next change to how launch starts a
  process.
- Left by: the agent, by rule (a shortcut the change takes: step 9 plugin part 2 kept the
  launch code it shares with the drivers).
- Added 2026-11-04, 1 case:
  - 2026-11-04: "step 9 plugin part 2: the event handler as an executable plugin" starts
    the handler with launch, whose output goes unbounded to the daemon's log; the WASI
    runner it replaced bounded a module's output to 4 KiB.
