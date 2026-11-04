# Session repair is removed

Status: decided by the maintainer, 2026-11-02. Part of the
[interface rework plan](REWORK-PLAN.md).

## The decision

The maintainer asked: "Can we just drop repair at all and just delete ->
create manually?" and decided on 2026-11-02 to drop session repair
entirely. Discard or delete, then create a new session on the retained
branch, replaces every repair.

Today P has four repairs of an established session, each with a preview and
a confirm method, plus `session.repair.prepare` to rebuild a lost image. The
client must know which repair fits, and each one carries its own guards,
recovery and image paths, for states that are rare. A new session on the
branch starts from the same committed work a repair would restore.

Failed-creation cleanup and replacement stay: they are not repairs, and
neither is `operation.retry`.

## What each former repair becomes

| Former repair | The session's state | Now |
|---|---|---|
| Missing runtime | The container is gone; the branch is intact | Discard, then create a session on the branch. The container's contents were already lost; the plan lists them in `gone`. |
| Missing assigned ref | The branch ref is gone; the container is there | Discard or delete, which are the same with no ref, then create. Commits only in the container are lost unless rescued first; the plan lists the container as `runtime_data` loss. |
| Git principal | The session's Git key is broken: its registration missing or revoked, or the key missing or different | Discard, then create. Uncommitted work in the container must be rescued by hand first. |
| Unrecoverable record | Neither container nor branch is left | Delete removes the record. |

## Prerequisites

1. **Removal accepts a missing branch ref**, in groundwork step 0e. Today
   both discard and delete refuse it: their previews read the branch through
   `AssignedBranchSnapshot` and `BranchRemovalLoss`, which answer "assigned
   P ref unavailable" (internal/gitservice/removal_preview.go), except for a
   blank project's first session. With no ref, delete has nothing in P's
   repository to remove and discard has no branch to keep, so discard acts
   as a delete. The plan states the loss by category: the container and
   everything in it, including commits that exist only there.
2. **Removal does not depend on the session's Git principal.** This already
   holds for a missing or revoked registration and a missing host key:
   removal checks no principal (removal_preview.go), disables any principal
   it finds (`commitRemoval` in internal/control/discard_store.go) and
   tolerates an absent key (`removeSessionKeyAt` in
   internal/daemon/session_keys.go). A host key file with an unexpected
   owner, mode or link count still stops the key removal; principal repair
   refused that case too (`host_key_unsafe`).

## What is removed

Groundwork step 0h removed, in 02612f6:

- the four repair kinds: missing runtime, missing assigned ref, Git
  principal and unrecoverable record;
- their nine methods: `session.repair.preview`, `session.repair.confirm`,
  `session.repair.prepare`, and the preview and confirm methods of
  `session.ref.repair`, `session.principal.repair` and
  `session.record.repair`;
- their operation kinds: `session.repair`, `session.repair.prepare`,
  `session.ref.repair`, `session.principal.repair` and
  `session.record.repair`;
- the image rebuild paths: preparation's builder run, the image publication
  inside the repair run, and the accepted repair image
  (`AcceptedRepairImage`);
- the code: repair.go, repair_prepare.go, repair_image.go, ref_repair.go,
  principal_repair.go and record_repair.go in internal/daemon; the matching
  `*_store.go` files in internal/control; runtimeincus/principal_repair.go;
  `CreateRestoredAssignedRefExact` in internal/gitservice;
- VM scenarios 38, 39, 40 and 42, which tested only the repairs. Scenario 34
  keeps its missing-runtime discard and delete checks, as
  `34-missing-runtime-removal.sh`.

These changed with it: [session lifecycle](../session-lifecycle.md#inconsistent-sessions),
whose "Repair" section became "Inconsistent sessions", the repair sections of
the [control API](../control-api.md), the troubleshooting rows of the
[user guide](../user-guide.md), and the record-removal sentence in
[session observability](../session-observability.md).

## How it will be checked

- Unit tests: discard and delete are allowed for a session whose branch ref
  is missing, list its container as `runtime_data` loss and no `p_commits`,
  and leave no ref behind; discard and delete are allowed for a session
  whose registration is revoked or whose host key is missing.
- A VM scenario of step 0e removes a session whose branch ref was deleted
  outside P, then creates a new session on a branch. Its fault setup comes
  from the removed scenario 39, which deleted the assigned ref with
  `git update-ref -d` in P's repository.
- No method, operation kind or code path named above remains.
