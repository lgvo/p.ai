# Structure

## Lifecycle workers depend on concrete runtime, store and Git types

- Where: internal/daemon `lifecycle` (fields `store`, `git`, `runtime`)
- Problem: Workers call a concrete `*runtimeincus.Backend` (16 methods, about 45 calls),
  `*control.Store` and `*gitCapability`, so no worker can run against a stand-in.
- Evidence: daemon statement coverage is 18.7%; every `process*` worker except
  `processCreateCleanup` and `processRetainedRename` is at 0%.
- Risk: discard, delete, rename and project delete are checked only by the serial VM
  suite; a regression in a recovery branch surfaces late or not at all.
- Fix: give the workers narrow interfaces grouped by native sub-protocol (builder,
  discard), then unit-test each worker's recovery branches.
- Fix when: a worker's recovery logic changes, or before a second runtime backend.
- Left by: the agent, by rule (a restructuring not pursued in the implementation rework).
- Added 2026-11-01, updated 2026-11-03, 9 cases:
  - 2026-11-01: "complete rework of P's implementation" mapped the daemon and left this out
    of scope because the workers' behavior is pinned only by VM scenarios.
  - 2026-11-02: "fix the Start readiness race" could not run `InspectSession` or Start's
    watcher against a stand-in runtime; its unit tests cover `observedCondition` and
    `endStartWatcher`, and VM scenarios such as 34 cover the whole Start-then-act sequence.
  - 2026-11-02: "remove `workspace.inspect` and `environment.cache.*`" removed
    `processEnvironmentCollection` and the read-only inspection path of
    `processWorkspaceInspect`, which took the runtime from 49 methods and 204 calls to 43
    and 195; the loss inspection's recovery branches are still reachable only in VM.
  - 2026-11-02: "remove session repair" removed the five repair workers, which took the
    runtime from 43 methods and 195 calls to 35 and 123; the missing-runtime Discard and
    Delete paths it kept are still checked only by VM scenario 34.
  - 2026-11-02: "Stop is not refused while a read of the session holds its lock" could
    not hold a `runtime.services` read in flight against a stand-in runtime; its unit tests
    pause the services request at the confinement check instead, and VM scenario 58 covers
    the read against a real runtime.
  - 2026-11-02: "deletions without inspection" deleted `processWorkspaceInspect` and the
    helper and freeze phases of `processDiscard`, `processRename` and
    `processAssembledCreateCleanup`, which took the runtime from 36 methods and 125 calls
    (counted as `l.runtime.<Method>` in the daemon's non-test files) to 23 and 61. Removing
    a running session, stopped-only rename and removal with a missing branch ref are
    unit-tested only through `discardSourceMatches`, `renameSourceStopped` and the store
    and Git layers; VM scenarios cover the workers.
  - 2026-11-02: "fold the environment plugin into the image build" made `buildEnvironment`
    call `ResolveBuilderNix` and `RealizeBuilderNix` on the concrete runtime itself, two
    more methods and calls than through environmentnix; only the publish goes through the
    narrow `gatedPublisher` interface, so the build path is still checked only in VM.
  - 2026-11-02: "one image per source commit" replaced `buildEnvironment` with the image
    step (image.go, image_release.go), which calls the narrow `runtimedriver.Images` and is
    unit-tested on an in-memory driver; that took the runtime from 25 methods and 63 calls to
    16 and 45.
  - 2026-11-03: "risky changes confirmed with change tags" changed `processProjectDelete`'s
    recovery, which meets "Fix when": it now stops a running session's container through the
    concrete runtime's `StopForDiscardExact` before deleting it, and never issues that stop
    twice. The runtime still has 16 methods and 45 calls. The new branches are decided in
    `projectDeleteRuntimeDecision`, which needs no runtime, but the stop itself still runs
    only against a real one. The plans of discard, delete, project deletion and failed-creation
    cleanup and replacement read the concrete runtime too, so their unit tests reach only the
    refusals found before that read; the VM scenarios cover the rest. Retained-branch
    deletion's plan reads only Git and is unit-tested end to end.

## Long worker functions

- Where: internal/daemon `processProjectDelete`, `processCreation`, `processDiscard`,
  `processRename`, `processAssembledCreateCleanup`
- Problem: Seven daemon functions exceed 150 lines (`processProjectDelete` 323).
- Evidence: measured on 2026-11-01 while mapping the daemon and control packages.
- Risk: recovery branches are hard to read and review; reviewers miss interactions.
- Fix: split each worker by phase.
- Fix when: together with the worker interfaces above.
- Left by: the agent, by rule (a restructuring not pursued in the implementation rework).
- Added 2026-11-01, updated 2026-11-03, 7 cases:
  - 2026-11-01: "complete rework of P's implementation" measured the function lengths.
  - 2026-11-02: "collapse the 21 migrations into one schema file" cut internal/control
    `openStore` from 367 lines of migrations to 90, which takes it out of this entry.
  - 2026-11-02: "remove `workspace.inspect` and `environment.cache.*`" cut
    `processWorkspaceInspect` from 254 lines to 222; sixteen daemon functions still
    exceed 150 lines.
  - 2026-11-02: "remove session repair" removed eight of them (`processRepair`,
    `repairImageAdvanceToReady`, `processRepairPrepare`, `previewRepair`,
    `processRefRepair`, `ConfirmRepair`, `PreviewRefRepair`, `processPrincipalRepair`);
    eight daemon functions still exceed 150 lines.
  - 2026-11-02: "deletions without inspection" deleted `processWorkspaceInspect` (222) and
    cut `processDiscard` from 422 lines to 241, `processAssembledCreateCleanup` from 302 to
    175 and `processRename` from 248 to 201; seven daemon functions still exceed 150 lines,
    and the copied helper and freeze sequences across workers are gone.
  - 2026-11-03: "one image per source commit", moved onto step 3, put project deletion's
    image delete in `deleteProjectImage` and creation's image step in `ensureImage`:
    `processProjectDelete` went from 328 lines to 298 and `processCreation` from 300 to 285.
  - 2026-11-03: "risky changes confirmed with change tags" grew `processProjectDelete` by 15
    lines, to stop a running session's container before deleting it.

## Exclusion triggers read evidence JSON fields by their Go tag names

- Where: internal/control schema.sql (`active_session_operation`,
  `active_assembled_cleanup`, `assembled_cleanup_guards`, the `assembled_cleanup_*`
  triggers, `assembled_cleanup_requires_creator`); commit_images.go `commitSessionsWhere`
- Problem: SQL invariants read JSON evidence fields by name (`$.review.runtime`,
  `$.review.old_operation_id`, `$.review.old_request_sha256`, `$.runtime_absent`,
  `$.local_complete`, `$.captured_oid`) that mirror Go struct tags.
- Evidence: tags in internal/control create_cleanup.go (`CreateCleanupEvidence`) and
  lifecycle_store.go (`CreationEvidence`), and in internal/api details.go
  (`CreateCleanupDetails`, the cleanup evidence's `review`).
- Risk: renaming an evidence field's tag silently disables an exclusion trigger.
- Fix: a test that breaks each JSON-keyed trigger when its field is renamed.
- Fix when: any change to an evidence struct those triggers read.
- Left by: the agent, by rule (a restructuring not pursued in the implementation rework).
- Added 2026-11-01, updated 2026-11-03, 5 cases:
  - 2026-11-01: "complete rework of P's implementation" kept migrations unchanged.
  - 2026-11-02: "collapse the 21 migrations into one schema file" replaced the migrations
    with schema.sql, which ends the half of this entry about seeing the effective schema;
    the JSON-keyed triggers are unchanged.
  - 2026-11-02: "deletions without inspection" removed the loss inspection's index arm, its
    admission trigger and its `$.creator_operation_id` and `$.runtime_init_state` reads, and
    renamed the guards that remain to `assembled_cleanup_*`; they still read the cleanup's
    evidence by tag name.
  - 2026-11-02: "one image per source commit" changed `CreationEvidence` (removed
    `builder_tree_oid` and `environment_state`, added `image`), which meets "Fix when",
    without adding the test; its commit-image queries, which decide whether an image is
    still in use and guard its release, read `$.captured_oid` in SQL, so renaming that
    tag would let release delete images that sessions' commits still use.
  - 2026-11-03: "risky changes confirmed with change tags" changed `CreateCleanupEvidence`,
    which meets "Fix when", without adding the test: its `review` is now the cleanup plan's
    details, `api.CreateCleanupDetails` in internal/api details.go, which dropped `uuid`,
    `eligible`, `unsafe_reasons`, `loss_warnings` and the token fields. The fields the
    triggers read keep their JSON names (`review.runtime`, `review.old_operation_id`,
    `review.old_request_sha256`).

## Incus exec and HTTP access is hand-built in many places

- Where: internal/runtimeincus (services.go, builder.go, builder_nix.go, host_observe.go,
  attach.go, builder_architecture.go, assembly.go, sftp_stat.go, graceful_stop.go) and
  internal/attachment incus.go
- Problem: The `incus exec` argument lists and the HTTP-over-socket clients are written
  separately, each with its own environment and limits.
- Evidence: listed while mapping the adapters on 2026-11-01.
- Risk: a confinement fix to exec or socket access misses a copy.
- Fix: one exec builder and one socket client in runtimeincus.
- Fix when: the next change to how P reaches Incus.
- Left by: the agent, by rule (a restructuring not pursued in the implementation rework;
  step 8 consolidates only the instance inventory).
- Added 2026-11-01, updated 2026-11-02, 5 cases:
  - 2026-11-01: "complete rework of P's implementation" scoped step 8 to the inventory.
  - 2026-11-02: "deletions without inspection" deleted workspace_git.go and
    workspace_helper.go with the workspace helpers, and their exec and file-API clients.
  - 2026-11-02: "give builders public internet access only" added an argument list,
    the root runtime-kit exec in builder.go `prepareBuilderNetwork`, and changed the Nix
    argv in builder_nix.go; the runtime driver's image build (step 1) takes both.
  - 2026-11-02: "wait for the guest's systemd before a graceful stop" (step 0j) builds
    one more file-API client by hand, in graceful_stop.go `awaitGuestSystemd`.
  - 2026-11-02: "one image per source commit" put the build behind the runtime driver's
    image operations (`runtimeincus.Driver`, in process) but kept both argument lists
    hand-built: `prepareBuilderNetwork` is unchanged, and builder_nix.go
    `builderGuestExecEnv` now also runs the builder's Git commands with an extra
    environment list. It added one more HTTP-over-socket client, builder.go `putStream`,
    which streams the source bundle into the builder: a change to how P reaches Incus that
    meets "Fix when", with consolidation outside the step.

## Trusted configuration and the RPC server live in the store package

- Where: internal/control config.go, grants.go, public_egress.go, rpc.go, rpc_lifecycle.go,
  rpc_services.go, attachment.go, session_rpc.go
- Problem: Host configuration, project policy, filesystem grants, public-egress configuration
  and the control-API server share control with the SQLite store and its sentinel errors, so
  adapters such as runtimeincus import control to read public-egress configuration, and the
  server cannot be read or tested apart from persistence.
- Evidence: config.go calls the store's `validProject`, `validFingerprint`, `digest` and
  `normalizedObject`; `ProjectPolicySnapshot` errors wrap `control.ErrInvalid`, which the
  server answers as `invalid`. The request bounds moved to `internal/api`, but control's
  `validProject`, `validUUID` and siblings only delegate to them, and the server's error
  mapping (errors.go) and the `Refusal` type that adapters such as runtimeincus and
  gitservice return live in control.
- Risk: configuration and persistence change together; an adapter that needs one trusted
  setting depends on the whole store package.
- Fix: give P's identifier formats, digest and sentinel errors a package below control, then
  move configuration and the server to their own packages on top of it.
- Fix when: identifier validation is consolidated (the same formats are also copied in
  gitservice, plugin and runtimeincus), or the next change to host configuration.
- Left by: the agent, by rule (a restructuring not pursued: steps 2 and 3b of REWORK-PLAN.md
  were deferred after measuring their cost).
- Added 2026-11-01, updated 2026-11-03, 6 cases:
  - 2026-11-01: "complete rework of P's implementation" deferred steps 2 and 3b after
    measurement.
  - 2026-11-02: "deletions without inspection" removed the `workspace.loss.inspect` handler
    and the inspection parameters from rpc_lifecycle.go; the handlers still use the store's
    validators.
  - 2026-11-02: "wire types" moved the identifier formats into `internal/api`, where each
    request type's `Validate` checks them; the server stays in control and registers its
    handlers there.
  - 2026-11-02: "fold the environment plugin into the image build" removed
    `activation_path` and `plugin_id` from `runtime.environment`, a change to host
    configuration that meets "Fix when"; the move was outside that removal.
  - 2026-11-02: "one image per source commit" replaced the server's
    `CreationEnvironmentView` in rpc_lifecycle.go with `CreationImageView`, which reads
    creation evidence from the store package as before; the server stayed in control.
  - 2026-11-03: "errors that name the next step" added control's `Refusal` and error
    mapping; runtimeincus and gitservice return refusals from control too.

## Operation status updates and kind lists are still written per site

- Where: internal/control (26 `UPDATE operations` statements across the `*_store.go` files,
  each run through `journalTx.update`; `SessionRemovalKind` in removal_kind.go lists kinds in
  SQL; commit_images.go `commitSessionsWhere` and the `session_creation` index name
  `session.create` in SQL; schema.sql triggers name kinds and phases)
- Problem: Inserts, the key rule, the unfinished listing and the reporting of status changes
  have one owner in operation_journal.go, but each kind's store methods still write their own
  `UPDATE` statement, and a query keeps its own list of which kinds remove a session.
- Evidence: 20 of the 26 updates filter only on `id` (see the state-safety entry on
  check-then-act); the kind lists repeat the constants in operation_journal.go by hand.
- Risk: adding a kind or a status rule needs coordinated edits that nothing checks.
- Fix: one journal transition function that checks the expected status and phase, and kind
  lists derived from the journal's kind table.
- Fix when: the next new operation kind, or together with the check-then-act fix.
- Left by: the agent, by rule (a restructuring not pursued: step 4 of REWORK-PLAN.md was
  scoped to inserts, the key rule and the unfinished listing).
- Added 2026-11-01, updated 2026-11-03, 8 cases:
  - 2026-11-01: "complete rework of P's implementation" scoped step 4.
  - 2026-11-02: "collapse the 21 migrations into one schema file" moved the triggers that
    name kinds and phases from the migrations into schema.sql, unchanged.
  - 2026-11-02: "remove `workspace.inspect` and `environment.cache.*`" had to drop
    `workspace.inspect` by hand from `ActiveWorkspaceInspect`, the project-deletion snapshot
    query and a schema trigger, and `environment.collect` from an index and five triggers.
  - 2026-11-02: "remove session repair" had to drop the five repair kinds by hand from
    `ActiveWorkspaceInspect`, `SessionRemovalKind`, `PendingEnvironmentPublication`,
    `relatedEnvironmentRows` and two schema triggers, besides the journal's kind table.
  - 2026-11-02: "deletions without inspection" had to drop `workspace.loss.inspect` by hand
    from `ActiveWorkspaceInspect` (now `ActiveSessionChange`), the project-deletion snapshot
    query, a schema index, view and four triggers, and the daemon's dispatch tests, besides
    the journal's kind table; the assembled cleanup's phases are listed again in a trigger.
  - 2026-11-02: "one image per source commit" names the `session.create` kind by hand in
    SQL twice more: `commitSessionsWhere` in commit_images.go, which finds the sessions
    started from a commit, and the `session_creation` index in schema.sql that serves it.
  - 2026-11-03: "a reason beside the diagnostic" added the `reason` column to every status
    update by hand, in each kind's store methods, and one more update, `BlockAfterPanic`.
  - 2026-11-03: "waiting" ran every insert and update through one journal transaction that
    reports status changes, and replaced `ActiveSessionChange`'s kind list with one
    admission check over every kind; the statements themselves stay per site.

## Incus instance inventory and metadata keys are written per caller

- Where: internal/runtimeincus create_replace.go, project_delete.go, runtime_absence.go,
  workspace_creator.go, session_capacity.go, egress.go `checkPublicAddressCollision`; the
  `user.p.*` metadata keys across the package
- Problem: Six functions each run `incus list --format json`, decode, bound and validate
  the instances with their own rules, and metadata keys such as `user.p.session_uuid`
  (8 times in runtimeincus) are string literals.
- Evidence: listed while mapping the adapters on 2026-11-01; the bounds differ (4 or 64), see
  TO-DECIDE.md "Four-instance bound in Incus inventory checks".
- Risk: an inventory or confinement fix reaches one caller and misses another; a mistyped key
  silently matches nothing.
- Fix: one inventory reader returning validated instances under a caller-given bound, and the
  metadata keys as constants; characterize each caller first.
- Fix when: the next change to an inventory check or to P's Incus metadata.
- Left by: the agent, by rule (a restructuring not pursued: step 8 of REWORK-PLAN.md was
  dropped after measurement).
- Added 2026-11-01, updated 2026-11-03, 5 cases:
  - 2026-11-01: "complete rework of P's implementation" dropped step 8.
  - 2026-11-02: "remove session repair" moved `ConfirmSessionRuntimeAbsent`, which removal
    keeps using, from record_repair.go to runtime_absence.go unchanged.
  - 2026-11-02: "deletions without inspection" deleted workspace_helper.go and its
    `CheckWorkspaceHelperCapacity` inventory read; six callers remain.
  - 2026-11-02: "give builders public internet access only" made
    `checkPublicAddressCollision` serve builders too (it now takes the instance to skip and
    the address); its inventory reading and bound are unchanged.
  - 2026-11-03: "wait out abandoned execs and unrendered instances" (step 0k) taught the
    single-instance read in backend.go `Backend.inspect` to list again while Incus shows its
    placeholder for an instance it cannot render (status `Error`, no configuration); five of
    the six inventory readers still refuse that placeholder and session_capacity.go counts it
    without its builder labels, so a check that meets an instance's poweroff refuses, or
    counts a builder as a session, where it could wait.

