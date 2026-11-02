# Structure

## Lifecycle workers depend on concrete runtime, store and Git types

- Where: internal/daemon `lifecycle` (fields `store`, `git`, `runtime`)
- Problem: Workers call a concrete `*runtimeincus.Backend` (35 methods, about 123 calls),
  `*control.Store` and `*gitCapability`, so no worker can run against a stand-in.
- Evidence: daemon statement coverage is 18.7%; every `process*` worker except
  `processCreateCleanup` and `processRetainedRename` is at 0%.
- Risk: discard, delete, rename and project delete are checked only by the serial VM
  suite; a regression in a recovery branch surfaces late or not at all.
- Fix: give the workers narrow interfaces grouped by native sub-protocol (builder,
  discard), then unit-test each worker's recovery branches.
- Fix when: a worker's recovery logic changes, or before a second runtime backend.
- Left by: the agent, by rule (a restructuring not pursued in the implementation rework).
- Added 2026-11-01, updated 2026-11-02, 6 cases:
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

## Long worker functions

- Where: internal/daemon `processProjectDelete`, `processCreation`, `processDiscard`,
  `processRename`, `processAssembledCreateCleanup`
- Problem: Seven daemon functions exceed 150 lines (`processProjectDelete` 323).
- Evidence: measured on 2026-11-01 while mapping the daemon and control packages.
- Risk: recovery branches are hard to read and review; reviewers miss interactions.
- Fix: split each worker by phase.
- Fix when: together with the worker interfaces above.
- Left by: the agent, by rule (a restructuring not pursued in the implementation rework).
- Added 2026-11-01, updated 2026-11-02, 5 cases:
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

## Exclusion triggers read evidence JSON fields by their Go tag names

- Where: internal/control schema.sql (`active_session_operation`,
  `active_assembled_cleanup`, `assembled_cleanup_guards`, the `assembled_cleanup_*`
  triggers, `assembled_cleanup_requires_creator`)
- Problem: SQL invariants read JSON evidence fields by name (`$.review.runtime`,
  `$.review.old_operation_id`, `$.runtime_absent`) that mirror Go struct tags.
- Evidence: tags in create_cleanup.go and removal_preview.go.
- Risk: renaming an evidence field's tag silently disables an exclusion trigger.
- Fix: a test that breaks each JSON-keyed trigger when its field is renamed.
- Fix when: any change to an evidence struct those triggers read.
- Left by: the agent, by rule (a restructuring not pursued in the implementation rework).
- Added 2026-11-01, updated 2026-11-02, 3 cases:
  - 2026-11-01: "complete rework of P's implementation" kept migrations unchanged.
  - 2026-11-02: "collapse the 21 migrations into one schema file" replaced the migrations
    with schema.sql, which ends the half of this entry about seeing the effective schema;
    the JSON-keyed triggers are unchanged.
  - 2026-11-02: "deletions without inspection" removed the loss inspection's index arm, its
    admission trigger and its `$.creator_operation_id` and `$.runtime_init_state` reads, and
    renamed the guards that remain to `assembled_cleanup_*`; they still read the cleanup's
    evidence by tag name.

## Incus exec and HTTP access is hand-built in many places

- Where: internal/runtimeincus (services.go, builder.go, builder_nix.go, host_observe.go,
  attach.go, builder_architecture.go, assembly.go, sftp_stat.go) and internal/attachment
  incus.go
- Problem: The `incus exec` argument lists and the HTTP-over-socket clients are written
  separately, each with its own environment and limits.
- Evidence: listed while mapping the adapters on 2026-11-01.
- Risk: a confinement fix to exec or socket access misses a copy.
- Fix: one exec builder and one socket client in runtimeincus.
- Fix when: the next change to how P reaches Incus.
- Left by: the agent, by rule (a restructuring not pursued in the implementation rework;
  step 8 consolidates only the instance inventory).
- Added 2026-11-01, updated 2026-11-02, 3 cases:
  - 2026-11-01: "complete rework of P's implementation" scoped step 8 to the inventory.
  - 2026-11-02: "deletions without inspection" deleted workspace_git.go and
    workspace_helper.go with the workspace helpers, and their exec and file-API clients.
  - 2026-11-02: "give builders public internet access only" added an argument list,
    the root runtime-kit exec in builder.go `prepareBuilderNetwork`, and changed the Nix
    argv in builder_nix.go; the runtime driver's image build (step 1) takes both.

## Preview confirmation tokens are issued by copied code

- Where: internal/daemon `PreviewRemoval`, `PreviewCreateReplace`, `PreviewRetainedDelete`,
  `PreviewCreateCleanup`, `PreviewProjectDelete`
- Problem: Each preview family issues its token, prunes expired entries, enforces its own
  capacity (32 or 128) and checks the frame size with its own copy of the code and its own
  map on `lifecycle`.
- Evidence: five preview maps on `lifecycle`; capacity errors differ (`ErrConflict` answers
  `busy`, a plain error answers `unavailable`).
- Risk: a fix to token issue or expiry reaches one family and misses the others.
- Fix: one preview-book type with an instance per family carrying its TTL, capacity and error.
- Fix when: the previews' facts can be produced in unit tests (with the worker interfaces
  above), so each family's saturation can be characterized first.
- Left by: the agent, by rule (a restructuring not pursued; the plan review cut it because its
  behavior cannot be characterized before the change).
- Added 2026-11-01, updated 2026-11-02, 5 cases:
  - 2026-11-01: "complete rework of P's implementation" cut it from step 5 after review.
  - 2026-11-02: "recover from a panic in a request handler" had to make the same lock change
    in each copy: the token read in all ten, the token delete in nine, the insert in seven.
  - 2026-11-02: "remove `workspace.inspect` and `environment.cache.*`" removed
    `PreviewEnvironmentCollection` and its map; nine families remain.
  - 2026-11-02: "remove session repair" removed the four repair preview families and their
    maps; five families remain.
  - 2026-11-02: "deletions without inspection" removed the loss inspection's expiry bound
    from `PreviewRemoval` and `PreviewProjectDelete`; each family still issues its own token.

## Trusted configuration and the RPC server live in the store package

- Where: internal/control config.go, grants.go, public_egress.go, rpc.go, rpc_lifecycle.go,
  rpc_services.go, attachment.go, session_rpc.go
- Problem: Host configuration, project policy, filesystem grants, public-egress configuration
  and the control-API server share control with the SQLite store and its sentinel errors, so
  adapters such as runtimeincus import control to read public-egress configuration, and the
  server cannot be read or tested apart from persistence.
- Evidence: config.go calls the store's `validProject`, `validFingerprint`, `digest` and
  `normalizedObject`; `ProjectPolicySnapshot` errors wrap `control.ErrInvalid`, which the
  daemon's lifecycle errors map to `invalid_params`. The RPC handlers call the unexported
  `validUUID`, `validProject`, `validHexToken` and `validFingerprint` and match the unexported
  `sessionCapacityError`.
- Risk: configuration and persistence change together; an adapter that needs one trusted
  setting depends on the whole store package.
- Fix: give P's identifier formats, digest and sentinel errors a package below control, then
  move configuration and the server to their own packages on top of it.
- Fix when: identifier validation is consolidated (the same formats are also copied in
  gitservice, plugin and runtimeincus), or the next change to host configuration.
- Left by: the agent, by rule (a restructuring not pursued: steps 2 and 3b of REWORK-PLAN.md
  were deferred after measuring their cost).
- Added 2026-11-01, updated 2026-11-02, 2 cases:
  - 2026-11-01: "complete rework of P's implementation" deferred steps 2 and 3b after
    measurement.
  - 2026-11-02: "deletions without inspection" removed the `workspace.loss.inspect` handler
    and the inspection parameters from rpc_lifecycle.go; the handlers still use the store's
    validators.

## Operation status updates and kind lists are still written per site

- Where: internal/control (25 `UPDATE operations` statements across the `*_store.go` files;
  `ActiveSessionChange` in session_change.go and `SessionRemovalKind` in removal_kind.go
  list kinds in SQL; schema.sql triggers name kinds and phases)
- Problem: Inserts, the key rule and the unfinished listing have one owner in
  operation_journal.go, but status and phase changes are still written by each kind's store
  methods, and two queries keep their own lists of which kinds guard or remove a session.
- Evidence: 20 of the 25 updates filter only on `id` (see the state-safety entry on
  check-then-act); the kind lists repeat the constants in operation_journal.go by hand.
- Risk: adding a kind or a status rule needs coordinated edits that nothing checks.
- Fix: one journal transition function that checks the expected status and phase, and kind
  lists derived from the journal's kind table.
- Fix when: the next new operation kind, or together with the check-then-act fix.
- Left by: the agent, by rule (a restructuring not pursued: step 4 of REWORK-PLAN.md was
  scoped to inserts, the key rule and the unfinished listing).
- Added 2026-11-01, updated 2026-11-02, 5 cases:
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
- Added 2026-11-01, updated 2026-11-02, 4 cases:
  - 2026-11-01: "complete rework of P's implementation" dropped step 8.
  - 2026-11-02: "remove session repair" moved `ConfirmSessionRuntimeAbsent`, which removal
    keeps using, from record_repair.go to runtime_absence.go unchanged.
  - 2026-11-02: "deletions without inspection" deleted workspace_helper.go and its
    `CheckWorkspaceHelperCapacity` inventory read; six callers remain.
  - 2026-11-02: "give builders public internet access only" made
    `checkPublicAddressCollision` serve builders too (it now takes the instance to skip and
    the address); its inventory reading and bound are unchanged.

