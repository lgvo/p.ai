# Structure

## Lifecycle workers depend on concrete runtime, store and Git types

- Where: internal/daemon `lifecycle` (fields `store`, `git`, `runtime`)
- Problem: Workers call a concrete `*runtimeincus.Backend` (43 methods, about 195 calls),
  `*control.Store` and `*gitCapability`, so no worker can run against a stand-in.
- Evidence: daemon statement coverage is 18.7%; every `process*` worker except
  `processCreateCleanup` and `processRetainedRename` is at 0%.
- Risk: discard, delete, rename, repair and project delete are checked only by the serial
  VM suite; a regression in a recovery branch surfaces late or not at all.
- Fix: give the workers narrow interfaces grouped by native sub-protocol (helper VM,
  freeze/thaw, builder, discard), then unit-test each worker's recovery branches.
- Fix when: a worker's recovery logic changes, or before a second runtime backend.
- Left by: the agent, by rule (a restructuring not pursued in the implementation rework).
- Added 2026-11-01, updated 2026-11-02, 3 cases:
  - 2026-11-01: "complete rework of P's implementation" mapped the daemon and left this out
    of scope because the workers' behavior is pinned only by VM scenarios.
  - 2026-11-02: "fix the Start readiness race" could not run `InspectSession` or Start's
    watcher against a stand-in runtime; its unit tests cover `observedCondition` and
    `endStartWatcher`, and VM scenarios such as 34 cover the whole Start-then-act sequence.
  - 2026-11-02: "remove `workspace.inspect` and `environment.cache.*`" removed
    `processEnvironmentCollection` and the read-only inspection path of
    `processWorkspaceInspect`, which took the runtime from 49 methods and 204 calls to 43
    and 195; the loss inspection's recovery branches are still reachable only in VM.

## Native sub-protocols are copied across workers

- Where: internal/daemon discard_process.go, workspace_inspect.go, assembled_cleanup.go,
  rename.go, ref_repair.go, environment.go, repair_prepare.go, repair_image.go, repair.go
- Problem: The helper-VM sequence (3 copies), freeze/thaw with stale rollback (4 copies),
  builder-VM sequence (3 copies) and the activation-schema ladder (2 copies, lifecycle.go
  and repair.go) are written separately in each worker.
- Evidence: `rollbackStaleDiscard`, `rollbackRenamePrecommit`, `rollbackRefRepair` and
  `failWorkspaceInspect` each restore a freeze; `buildEnvironment`, `processRepairPrepare`
  and `repairImageAdvanceToReady` each drive the builder phases.
- Risk: a safety fix applied to one copy misses the others.
- Fix: one component per sub-protocol with its durable phases, used by every worker.
- Fix when: any of these sequences changes, or together with the worker interfaces above.
- Left by: the agent, by rule (a restructuring not pursued in the implementation rework).
- Added 2026-11-01, updated 2026-11-02, 2 cases:
  - 2026-11-01: "complete rework of P's implementation" found the copies while mapping.
  - 2026-11-02: "remove `workspace.inspect` and `environment.cache.*`" removed the
    non-exact freeze and thaw from `processWorkspaceInspect`; `failWorkspaceInspect` still
    restores its own freeze.

## Long worker functions

- Where: internal/daemon `processDiscard`, `process`, `processProjectDelete`,
  `processAssembledCreateCleanup`, `processRepair`, `processWorkspaceInspect`,
  `processRename`
- Problem: Sixteen daemon functions exceed 150 lines (`processDiscard` 422).
- Evidence: measured on 2026-11-01 while mapping the daemon and control packages.
- Risk: recovery branches are hard to read and review; reviewers miss interactions.
- Fix: split each worker by phase once its sub-protocols are shared components.
- Fix when: together with the sub-protocol components above.
- Left by: the agent, by rule (a restructuring not pursued in the implementation rework).
- Added 2026-11-01, updated 2026-11-02, 3 cases:
  - 2026-11-01: "complete rework of P's implementation" measured the function lengths.
  - 2026-11-02: "collapse the 21 migrations into one schema file" cut internal/control
    `openStore` from 367 lines of migrations to 90, which takes it out of this entry.
  - 2026-11-02: "remove `workspace.inspect` and `environment.cache.*`" cut
    `processWorkspaceInspect` from 254 lines to 222; sixteen daemon functions still
    exceed 150 lines.

## Exclusion triggers read evidence JSON fields by their Go tag names

- Where: internal/control schema.sql (`active_session_operation`,
  `active_creator_loss_inspection`, `creation_read_guards`,
  `workspace_loss_read_requires_established`, the `creator_loss_*` triggers,
  `assembled_cleanup_requires_creator`)
- Problem: SQL invariants read JSON evidence fields by name (`$.creator_operation_id`,
  `$.runtime_init_state`, `$.review.old_operation_id`) that mirror Go struct tags.
- Evidence: tags in workspace_operation.go, create_cleanup.go, lifecycle_store.go.
- Risk: renaming an evidence field's tag silently disables an exclusion trigger.
- Fix: a test that breaks each JSON-keyed trigger when its field is renamed.
- Fix when: any change to an evidence struct those triggers read.
- Left by: the agent, by rule (a restructuring not pursued in the implementation rework).
- Added 2026-11-01, updated 2026-11-02, 2 cases:
  - 2026-11-01: "complete rework of P's implementation" kept migrations unchanged.
  - 2026-11-02: "collapse the 21 migrations into one schema file" replaced the migrations
    with schema.sql, which ends the half of this entry about seeing the effective schema;
    the JSON-keyed triggers are unchanged.

## Incus exec and HTTP access is hand-built in many places

- Where: internal/runtimeincus (services.go, builder_nix.go, host_observe.go,
  workspace_git.go, workspace_helper.go, attach.go, builder_architecture.go, assembly.go,
  sftp_stat.go) and internal/attachment incus.go
- Problem: Seven `incus exec` argument lists and six HTTP-over-socket clients are written
  separately, each with its own environment and limits.
- Evidence: listed while mapping the adapters on 2026-11-01.
- Risk: a confinement fix to exec or socket access misses a copy.
- Fix: one exec builder and one socket client in runtimeincus.
- Fix when: the next change to how P reaches Incus.
- Left by: the agent, by rule (a restructuring not pursued in the implementation rework;
  step 8 consolidates only the instance inventory).
- Added 2026-11-01, 1 case:
  - 2026-11-01: "complete rework of P's implementation" scoped step 8 to the inventory.

## Preview confirmation tokens are issued by copied code

- Where: internal/daemon `PreviewRemoval`, `PreviewRecordRepair`, `PreviewPrincipalRepair`,
  `PreviewRefRepair`, `previewRepair`, `PreviewCreateReplace`, `PreviewRetainedDelete`,
  `PreviewCreateCleanup`, `PreviewProjectDelete`
- Problem: Each preview family issues its token, prunes expired entries, enforces its own
  capacity (32 or 128) and checks the frame size with its own copy of the code and its own
  map on `lifecycle`.
- Evidence: nine preview maps on `lifecycle`; capacity errors differ (`ErrConflict` answers
  `busy`, a plain error answers `unavailable`).
- Risk: a fix to token issue or expiry reaches one family and misses the others.
- Fix: one preview-book type with an instance per family carrying its TTL, capacity and error.
- Fix when: the previews' facts can be produced in unit tests (with the worker interfaces
  above), so each family's saturation can be characterized first.
- Left by: the agent, by rule (a restructuring not pursued; the plan review cut it because its
  behavior cannot be characterized before the change).
- Added 2026-11-01, updated 2026-11-02, 3 cases:
  - 2026-11-01: "complete rework of P's implementation" cut it from step 5 after review.
  - 2026-11-02: "recover from a panic in a request handler" had to make the same lock change
    in each copy: the token read in all ten, the token delete in nine, the insert in seven.
  - 2026-11-02: "remove `workspace.inspect` and `environment.cache.*`" removed
    `PreviewEnvironmentCollection` and its map; nine families remain.

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
- Added 2026-11-01, 1 case:
  - 2026-11-01: "complete rework of P's implementation" deferred steps 2 and 3b after
    measurement.

## Operation status updates and kind lists are still written per site

- Where: internal/control (36 `UPDATE operations` statements across the `*_store.go` files;
  `ActiveWorkspaceInspect` in workspace_operation.go and `SessionRemovalKind` in
  removal_kind.go list kinds in SQL; schema.sql triggers name kinds and phases)
- Problem: Inserts, the key rule and the unfinished listing have one owner in
  operation_journal.go, but status and phase changes are still written by each kind's store
  methods, and two queries keep their own lists of which kinds guard or remove a session.
- Evidence: 27 of the 36 updates filter only on `id` (see the state-safety entry on
  check-then-act); the kind lists repeat the constants in operation_journal.go by hand.
- Risk: adding a kind or a status rule needs coordinated edits that nothing checks.
- Fix: one journal transition function that checks the expected status and phase, and kind
  lists derived from the journal's kind table.
- Fix when: the next new operation kind, or together with the check-then-act fix.
- Left by: the agent, by rule (a restructuring not pursued: step 4 of REWORK-PLAN.md was
  scoped to inserts, the key rule and the unfinished listing).
- Added 2026-11-01, updated 2026-11-02, 3 cases:
  - 2026-11-01: "complete rework of P's implementation" scoped step 4.
  - 2026-11-02: "collapse the 21 migrations into one schema file" moved the triggers that
    name kinds and phases from the migrations into schema.sql, unchanged.
  - 2026-11-02: "remove `workspace.inspect` and `environment.cache.*`" had to drop
    `workspace.inspect` by hand from `ActiveWorkspaceInspect`, the project-deletion snapshot
    query and a schema trigger, and `environment.collect` from an index and five triggers.

## Incus instance inventory and metadata keys are written per caller

- Where: internal/runtimeincus create_replace.go, project_delete.go, record_repair.go,
  workspace_creator.go, session_capacity.go, egress.go `checkPublicAddressCollision`,
  workspace_helper.go; the `user.p.*` metadata keys across the package
- Problem: Seven functions each run `incus list --format json`, decode, bound and validate
  the instances with their own rules, and metadata keys such as `user.p.session_uuid`
  (17 times) are string literals.
- Evidence: listed while mapping the adapters on 2026-11-01; the bounds differ (4 or 64), see
  TO-DECIDE.md "Four-instance bound in Incus inventory checks".
- Risk: an inventory or confinement fix reaches one caller and misses another; a mistyped key
  silently matches nothing.
- Fix: one inventory reader returning validated instances under a caller-given bound, and the
  metadata keys as constants; characterize each caller first.
- Fix when: the next change to an inventory check or to P's Incus metadata.
- Left by: the agent, by rule (a restructuring not pursued: step 8 of REWORK-PLAN.md was
  dropped after measurement).
- Added 2026-11-01, 1 case:
  - 2026-11-01: "complete rework of P's implementation" dropped step 8.

## Discard recovery still accepts evidence written before schema 12

- Where: internal/control discard_store.go `commitRemoval`, `failStaleRemoval`,
  `completeRemoval`; internal/daemon discard_process.go `processDiscard`
- Problem: Recovery reads discard evidence without an `action` field as a discard, a reading
  kept for databases created before schema 12. P supports fresh installs only and refuses
  state of another schema version, so no such evidence can exist.
- Evidence: the `ev.Action == ""` checks in those functions; the test
  `TestSchemaElevenDiscardEvidenceRecoversValidatedAndSecretsAbsent` exercises them.
- Risk: a compatibility path that cannot be reached still widens what recovery accepts and
  must be kept in step with every change to discard evidence.
- Fix: require `action` in discard evidence and remove the checks and their test.
- Fix when: the next change to discard evidence or recovery (step 0e of the interface rework).
- Left by: the agent, by rule (a problem outside the change).
- Added 2026-11-02, 1 case:
  - 2026-11-02: "collapse the 21 migrations into one schema file" found it while removing
    the migration tests.
