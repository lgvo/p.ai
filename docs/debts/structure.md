# Structure

## Lifecycle workers depend on the concrete store

- Where: internal/daemon `lifecycle` (field `store`)
- Problem: Workers call the concrete `*control.Store`, so no worker can run against a
  stand-in store.
- Evidence: daemon statement coverage is 65.8% (2026-11-03); the workers run in unit tests
  only over a real SQLite store.
- Risk: a store failure in a recovery branch is checked only where a real store can be made
  to fail, and otherwise only by the serial VM suite.
- Fix: give the workers a narrow store interface, then unit-test each worker's recovery
  branches.
- Fix when: a worker's recovery logic changes.
- Left by: the agent, by rule (a restructuring not pursued in the implementation rework).
- Added 2026-11-01, updated 2026-11-03, 22 cases:
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
  - 2026-11-03: "the daemon reaches sessions and services through the runtime driver" moved
    creation's runtime steps, Inspect, Start, Stop and the services methods to
    `runtimedriver.Driver` and unit-tested them on the in-memory driver; the removal,
    rename, capacity and attachment workers still call the backend, now through
    `NativeSession` for the session spec, which took the runtime to 18 methods and 47 calls.
  - 2026-11-03: "core computes the proofs from the driver's inventory" moved admission and
    the proofs of absence to `Inspect` and `Inventory` (runtime_proofs.go,
    session_capacity.go, unit-tested on the in-memory driver) and the policy decisions'
    confinement check to `Check`; removal, rename and attachment still call the backend,
    which took the runtime to 11 methods and 26 calls. The workers that call the proofs are
    still checked only by VM scenarios.
  - 2026-11-03: "removal, rename's workspace edits and creation recovery through the runtime
    driver" moved discard, delete, project deletion and assembled cleanup to the driver's
    `Inspect`, `Stop`, `Delete` and `Settle`, and rename's Git edits into core over the
    workspace operations; only attachment calls the backend, 3 methods and 3 calls. Those
    workers are now unit-tested on the in-memory driver with a real store and Git
    repository (runtime_removal_test.go, rename_test.go), which needed no store or Git
    stand-in.
  - 2026-11-03: "step 9 S2: rename's workspace phases over `RenameWorkspace`" moved the
    rename worker's heads and clone edits onto the source driver (`git.driver`); its unit
    tests now run on the in-memory source and runtime drivers with a real store, and no
    Git repository.
  - 2026-11-03: "step 9 S2: repositories, heads and commits through the source driver"
    moved creation's repository and branch assignment, retained rename and deletion,
    discard, delete and project deletion onto `git.driver`; retained rename and deletion,
    branch assignment and settling a lost head effect are unit-tested on the in-memory
    source driver, and `processRetainedDelete` is no longer at 0%. The origin, the image
    builder's bundle and P's SSH endpoint still use the concrete `*gitservice.Backend`.
  - 2026-11-03: "step 9 S3: the origin through the source driver" moved origin project
    creation, creation from an origin ref and the origin API's change, refresh and
    publication workers onto `git.driver`, with the per-project origin lock in the store;
    they are unit-tested on the in-memory source driver. The image builder's bundle and P's
    SSH endpoint still use the concrete `*gitservice.Backend`.
  - 2026-11-03: "step 9 S4: the commit export through the source driver" moved the image
    build's bundle onto `git.driver`'s `Export`; the image step's unit tests run it on the
    bundled driver over a real repository. Only P's SSH endpoint still uses gitservice.
  - 2026-11-03: "the daemon relays terminals" moved attachment to the driver's
    `OpenTerminal`, so no worker calls the Incus backend any more and the `runtime` field
    is gone; the store and Git half of this entry remains.
  - 2026-11-03: "core holds only the runtime driver" removed the daemon's Incus settings
    field and moved building the driver into one daemon file, so `lifecycle` holds only
    `runtimedriver.Driver` and the runtime half of this entry is gone; the title, "Fix" and
    "Fix when" now name only the store and Git.
  - 2026-11-03: "the runtime driver as its own executable" (step 8, M7) made `newLifecycle`
    start the driver executable through `supervise.Start` instead of building the Incus
    driver, and changed the claim's recovery from a connection lost while a terminal opens;
    the store and Git fields are unchanged.
  - 2026-11-03: "step 9 S5: P's SSH server in core" moved P's SSH endpoint to
    internal/sshserver, whose Git service runs the driver's `ServeGit`; gitservice is gone,
    and the Git service's lease, approval and settling of a lost service are unit-tested on
    the in-memory source driver with a real store.
  - 2026-11-03: "step 9 S6: the daemon holds only the source driver" moved building the
    bundled Git driver into one daemon file (source_setup.go), which returns it as
    `sourcedriver.Driver` with its shutdown function, and added a boundary test that no other
    code but named fixtures imports it, runs `git` or `ssh` on the host or builds paths into
    its storage.
    Workers reach Git only through that interface: most worker tests use the in-memory
    source driver, and a few fixtures still run the bundled driver on a real repository
    (`gitSource`). The Git half of this entry is gone; the title, "Problem", "Risk" and "Fix"
    now name only the store.
  - 2026-11-03: "step 9 P1: the session host moves into the Incus driver" removed the
    lifecycle's host package plan, so a session's spec carries only the selected agent
    adapter, which a new unit test on the in-memory driver checks; the store field is
    unchanged.

## Long worker functions

- Where: internal/daemon `processProjectDelete`, `processCreation`, `processDiscard`,
  `processRename`, `processAssembledCreateCleanup`
- Problem: Seven daemon functions exceed 150 lines (`processProjectDelete` 323).
- Evidence: measured on 2026-11-01 while mapping the daemon and control packages.
- Risk: recovery branches are hard to read and review; reviewers miss interactions.
- Fix: split each worker by phase.
- Fix when: together with the worker interfaces above.
- Left by: the agent, by rule (a restructuring not pursued in the implementation rework).
- Added 2026-11-01, updated 2026-11-03, 14 cases:
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
  - 2026-11-03: "the daemon reaches sessions and services through the runtime driver" moved
    the runtime part of `processCreation` into `createRuntime`, cutting it from 285 lines to
    205; it still exceeds 150 lines.
  - 2026-11-03: "core computes the proofs from the driver's inventory" replaced the proof
    calls in `processProjectDelete`, `processDiscard` and `processAssembledCreateCleanup`
    one for one; their lengths are unchanged.
  - 2026-11-03: "removal, rename's workspace edits and creation recovery through the runtime
    driver" moved rename's Git edits to workspace_rename.go and the delete and its
    settlement to runtime_removal.go; resuming an issued stop and settling an issued delete
    took `processDiscard` from 239 lines to 252, `processAssembledCreateCleanup` from 170 to
    181, `processRename` from 201 to 219 and `processProjectDelete` from 310 to 305
    (counted from `func` to the closing brace).
  - 2026-11-03: "step 9 S2: rename's workspace phases over `RenameWorkspace`" took
    `processRename` from 219 lines to 214: the source driver computes the clone's edits,
    and core settles the one edit its gate recorded.
  - 2026-11-03: "step 9 S2: repositories, heads and commits through the source driver"
    moved the repository's deletion into `deleteProjectRepository`, taking
    `processProjectDelete` from 305 lines to 282, and settling a resumed branch deletion
    took `processDiscard` from 252 to 258; `processCreation` stays at 199.
  - 2026-11-03: "step 9 S3: the origin through the source driver" moved origin project
    creation into `createOriginProject`, taking `processCreation` from 205 lines to 194
    (counted from `func` to the closing brace, as awk counts them; the 199 above was
    counted otherwise); it still exceeds 150 lines.
  - 2026-11-03: "core holds only the runtime driver" replaced the recorded Incus identity
    with the driver's incarnation in `processDiscard`, `processProjectDelete` and
    `processAssembledCreateCleanup` line for line; their lengths are unchanged.

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
- Added 2026-11-01, updated 2026-11-03, 7 cases:
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
  - 2026-11-03: "core holds only the runtime driver" replaced the Incus fields of
    `api.RemovalRuntime`, the object at `$.review.runtime`, with `name` and `incarnation`,
    which meets "Fix when", without adding the test; the triggers read only that the object
    is there, so they still hold.
  - 2026-11-03: "step 9 S5: the source-git package and its selection go" removed
    `source_id` and `source_sha256` from `CreationEvidence`'s `selection`, which meets
    "Fix when", without adding the test; no trigger reads `$.selection`.

## Incus exec and HTTP access is hand-built in many places

- Where: internal/runtimedriver/incus (driver_services.go, driver_workspace.go, builder.go,
  builder_nix.go, host_observe.go, attach.go, builder_architecture.go, assembly.go,
  sftp_stat.go, graceful_stop.go, driver_terminal.go)
- Problem: The `incus exec` argument lists and the HTTP-over-socket clients are written
  separately, each with its own environment and limits.
- Evidence: listed while mapping the adapters on 2026-11-01.
- Risk: a confinement fix to exec or socket access misses a copy.
- Fix: one exec builder and one socket client in the Incus driver.
- Fix when: the next change to how P reaches Incus.
- Left by: the agent, by rule (a restructuring not pursued in the implementation rework;
  step 8 consolidates only the instance inventory).
- Added 2026-11-01, updated 2026-11-03, 10 cases:
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
  - 2026-11-03: "removal, rename and creation recovery through the driver" gave the driver
    workspace operations over the stopped instance's file API that add SFTP REMOVE, RMDIR
    and a directory listing on `openIncusSFTP`, and generalized `writeSFTPRequest`; no new
    client, and consolidation stays outside the step.
  - 2026-11-03: "the daemon relays terminals" moved the attach exec's HTTP-over-socket and
    websocket client from internal/attachment into runtimeincus driver_terminal.go, still
    hand-built; internal/attachment no longer reaches Incus.
  - 2026-11-03: "core holds only the runtime driver" moved the package to
    internal/runtimedriver/incus; no client changed.
  - 2026-11-03: "the runtime driver as its own executable" (step 8, M7) moved the Incus
    driver into its own executable, `p-runtime-incus`, which the daemon starts with an empty
    environment, so host_observe.go `trustedJournalctl` now needs the driver config's
    optional `journalctl`; no client changed, and consolidation stays outside the step.
  - 2026-11-03: "step 9 P1: the session host moves into the Incus driver" made attach.go
    `verifyAttachAssets` compare the guest's files with the driver's own host files
    instead of the session's assets; no client changed, and consolidation stays outside
    the step.

## Trusted configuration and the RPC server live in the store package

- Where: internal/control config.go, grants.go, public_egress.go, rpc.go, rpc_lifecycle.go,
  rpc_services.go, attachment.go, session_rpc.go
- Problem: Host configuration, project policy, filesystem grants, public-egress configuration
  and the control-API server share control with the SQLite store and its sentinel errors, so
  adapters such as the Incus driver import control to read public-egress configuration, and the
  server cannot be read or tested apart from persistence.
- Evidence: config.go calls the store's `validProject`, `validFingerprint`, `digest` and
  `normalizedObject`; `ProjectPolicySnapshot` errors wrap `control.ErrInvalid`, which the
  server answers as `invalid`. The request bounds moved to `internal/api`, but control's
  `validProject`, `validUUID` and siblings only delegate to them, and the server's error
  mapping (errors.go) and the `Refusal` type live in control.
- Risk: configuration and persistence change together; an adapter that needs one trusted
  setting depends on the whole store package.
- Fix: give P's identifier formats, digest and sentinel errors a package below control, then
  move configuration and the server to their own packages on top of it.
- Fix when: identifier validation is consolidated (the same formats are also copied in
  the bundled source driver, plugin and the Incus driver), or the next change to host configuration.
- Left by: the agent, by rule (a restructuring not pursued: steps 2 and 3b of REWORK-PLAN.md
  were deferred after measuring their cost).
- Added 2026-11-01, updated 2026-11-03, 16 cases:
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
  - 2026-11-03: "the runtime driver interface" moved the Incus settings into
    `runtime.driver`, whose `config` control passes through unread, a change to host
    configuration that meets "Fix when"; the move was outside that step.
  - 2026-11-03: "core computes the proofs from the driver's inventory" made the server
    report the services capability only when the lifecycle's `ServicesOffered` says the
    driver offers project services (rpc.go, rpc_lifecycle.go, rpc_services.go); the server
    stayed in control.
  - 2026-11-03: "step 9 S2: repositories, heads and commits through the source driver"
    made rpc.go page `project.branches` and `project.retained_branches` over the source
    driver's `Branches` and map its errors (`HeadsFailure`); the server stayed in control.
  - 2026-11-03: "per-session state outlives its session" made `ServeSessionConn`
    (session_rpc.go) release a removed session's status state when its connection ends,
    through the store; the server stayed in control.
  - 2026-11-03: "core holds only the runtime driver" made the Incus driver answer an
    instance Incus cannot render with its own `ErrUnavailable` instead of control's
    `Failed` refusal; the driver still imports control for the public-egress configuration
    (`PublicEgressConfig.Validate`, `PublicEgressDeniedCIDRs`).
  - 2026-11-03: "the runtime driver as its own executable" (step 8, M7) replaced
    `runtime.driver.name` with the driver executable's `path` and `sha256`, a change to
    host configuration that meets "Fix when"; the move was outside that step. The Incus
    driver now takes core's settings from the hello but still imports control for the
    public-egress checks, so `p-runtime-incus` links the store package and its SQLite
    driver.
  - 2026-11-03: "step 9 S5: the source-git package and its selection go" removed
    `activation_path` and `source_plugin_id` from `git`, a change to host configuration
    that meets "Fix when"; the move was outside that removal. gitservice, which copied
    the identifier formats and returned control's refusals, is gone.
  - 2026-11-03: "step 9 P1: the session host moves into the Incus driver" removed
    `activation_path` and `host_plugin_id` from `runtime`, a change to host configuration
    that meets "Fix when"; the move was outside that removal.
  - 2026-11-03: "the source driver as its own executable" (step 9, S7) replaced
    `git.driver.name` with the driver executable's `path` and `sha256`, which both drivers'
    blocks now share as `DriverConfig`, a change to host configuration that meets "Fix
    when"; the move was outside that step. `p-source-git` takes its storage from the hello
    and imports no control code.
  - 2026-11-03: "step 9 plugin part 2: the event handler as an executable plugin"
    replaced the `events` and `runtime.agent_adapter` activation-file selections with
    `path` and `sha256`, a change to host configuration that meets "Fix when"; the move
    was outside that step. The selections' checks share `validatePinned` with
    `DriverConfig` in config.go.

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

## Incus metadata keys are written per caller, and the inventory refuses an unrendered instance

- Where: internal/runtimedriver/incus backend.go `initSession` and `validIdentity`, builder.go
  `createBuilder` and `validBuilderIdentity`, driver.go `builderLeftover` and
  `carriesBuild`, inventory.go `sessionLabelKeys`, `builderLabelKeys` and
  `instanceInventory`, driver_delete.go `sessionRuntime`
- Problem: The `user.p.*` metadata keys are string literals where an instance is labeled
  and where it is checked, and inventory.go lists the same keys again to read them back as
  the driver's labels. `instanceInventory` refuses the placeholder Incus lists for an
  instance it cannot render, where `Backend.inspect` lists again until it renders.
- Evidence: listed while mapping the adapters on 2026-11-01; on 2026-11-03 the session and
  builder keys are written in `incus init` argv and read back in four other places, and
  VM scenario 51 showed the placeholder while Incus cleaned up after a guest's poweroff.
- Risk: a mistyped or renamed key silently matches nothing, so a labeled runtime or builder
  goes missing from the inventory that capacity and the proofs of absence read; capacity,
  the proofs and Create refuse while an instance powers off, where they could wait.
- Fix: the metadata keys as constants, one table per object kind that both labeling and
  reading use; the inventory waits out the placeholder as `Backend.inspect` does.
- Fix when: the next change to P's Incus metadata or to the inventory reader.
- Left by: the agent, by rule (a restructuring not pursued: step 8 of REWORK-PLAN.md was
  dropped after measurement).
- Added 2026-11-01, updated 2026-11-03, 8 cases:
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
  - 2026-11-03: "core computes the proofs from the driver's inventory" removed the five
    scans and made `instanceInventory` (inventory.go) the one reader of the project's
    instances, with one bound of 64 for `Inventory`, Create's identity check, the builds'
    reads and `checkPublicAddressCollision`, which ends the per-caller inventory half of
    this entry and the miscounted builder; the metadata keys are still literals,
    inventory.go's label tables are a second list of them, and `instanceInventory` still
    refuses the placeholder of an instance Incus cannot render.
  - 2026-11-03: "removal, rename and creation recovery through the driver" gave the driver
    a session `Delete` whose `sessionRuntime` reads `user.p.instance_uuid` as one more
    literal.
  - 2026-11-03: "core holds only the runtime driver" moved the package to
    internal/runtimedriver/incus; the keys are unchanged.

## The CLI parses branch names and commit IDs by the bundled driver's rules

- Where: internal/api validate.go `ValidBranch` and `ValidOID`; their callers in cmd/p,
  names.go `parseBranchName` and `sourceRef`, risky.go (`--to` of a publication)
- Problem: Core leaves the syntax of branch names and commit IDs to the source driver
  (`CheckNames`), but the CLI still parses its arguments by the bundled Git driver's rules
  (gitnames), through `api.ValidBranch` and `api.ValidOID`: it refuses a branch name that
  rule refuses, and takes an argument of a commit ID's form as a commit, not a branch.
- Evidence: step 9 S2 removed every use of the two functions from core and kept them for
  cmd/p only.
- Risk: with a source driver of another backend, the CLI refuses locally a branch the
  driver accepts, or reads a branch whose name looks like a commit ID as that commit.
- Fix: the CLI sends names as given and shows the daemon's `invalid` answer with its field,
  and says which of a commit or a branch it means; then `ValidBranch` and `ValidOID` go.
- Fix when: a source driver of another backend, or the next change to the CLI's argument
  parsing.
- Left by: the agent, by rule (a shortcut the change takes: S2 moves core's checks, and
  cmd/p is another package's).
- Added 2026-11-03, 1 case:
  - 2026-11-03: "step 9 S2: Git's syntax leaves core" made the request types bound names
    only and the handlers ask `CheckNames`; the CLI keeps the bundled driver's rules.

## Production code carries seams only tests use

- Where: internal/sourcedriver/git seams.go (`Driver.seams`: `betweenReads`,
  `effectLanded`, `exportStaged`, `repositoryMoved`); internal/control store.go
  (`Store.originCallTimeout`)
- Problem: The bundled source driver carries four hooks that only tests set, empty in
  production, and the store carries the origin call bound as a field only so that control's
  tests can lower it; both depart from the rule that production code carries nothing only
  tests use.
- Evidence: production never sets `seams` (its `take*` functions return nil there); the
  driver's tests set them (heads_test.go, which also serves its conformance fixture,
  commits_test.go, deletion_test.go) to act between two head reads, after a head effect
  lands, once an export is staged and after a deletion moved a repository.
  `openStore` sets `originCallTimeout` to 35 s, and origin_test.go and
  publication_api_test.go set it to 50 ms.
- Risk: test-only paths live in shipped code, where a later change can call them or let
  them drift from what production does; a reader cannot tell the production behavior
  from the code alone.
- Fix: inject these through constructors or test-only build tags, or test the behavior
  another way.
- Fix when: the next change to those reads or to the origin bound.
- Left by: the agent, by rule (a departure from the coding-practice rule "production code
  carries nothing only tests use", taken in step 9 S1 and S3).
- Added 2026-11-03, 1 case:
  - 2026-11-03: "step 9: the source driver" (S1 added the driver's seams, S3 the store's
    origin call bound) recorded the departure without removing it.
