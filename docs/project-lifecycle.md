# P — project lifecycle

How P creates, identifies, changes, inspects, and deletes projects and their
retained branches.

> **Status: design.** This document is authoritative for project identity,
> creation modes, origin association lifecycle, retained-branch operations,
> and project deletion. [Communication boundaries](communication-boundaries.md)
> owns Git and origin transport, ref authorization, refresh, and publication.
> [Session lifecycle](session-lifecycle.md) owns sessions created within a
> project.

## Contents

- [Purpose and boundaries](#purpose-and-boundaries)
- [Project identity and state](#project-identity-and-state)
- [Project creation](#project-creation)
- [Origin association](#origin-association)
- [Sources and session creation](#sources-and-session-creation)
- [Retained branches](#retained-branches)
- [Project deletion](#project-deletion)
- [Recovery and idempotency](#recovery-and-idempotency)
- [MVP boundary](#mvp-boundary)
- [Acceptance criteria](#acceptance-criteria)

## Purpose and boundaries

A project is the complete path of one bare repository on a P instance's Git
server. It is created explicitly from an SSH Git origin or as a blank P
repository. P does not discover, register, watch, or relocate host checkouts.
There is no `p .` command and no registered-source-location concept.

Repository content cannot create a project, choose its path, configure its
origin, or widen its trusted policy. Project lifecycle is a host RPC action;
Git carries the selected origin objects and later session commits.

## Project identity and state

The project path, such as `p` or `lgvo/p`, is its identity within one P
instance. Namespace components are path structure, not MVP accounts or policy
objects. A path:

- must be a valid normalized P Git-server repository path;
- is unique within the P instance;
- is selected explicitly by the user; and
- is immutable after project creation.

MVP has no project rename. A different path means a different project. An exact
repeat of an already completed creation request is idempotent. Reusing a path
with conflicting creation input is rejected rather than interpreted as rename
or replacement. The same explicit origin may be used by another project path;
the path, not a guessed normalized remote URL, is project identity.

A project is either `active` or `deleting`. `deleting` means a confirmed bulk
deletion has permanently closed the project while P is still ensuring that all
owned resources are absent. It cannot accept new sessions, ref updates,
attachments, origin operations, or policy reloads.

## Project creation

Project creation has two explicit modes.

### From origin

The request supplies the project path and a supported SSH Git URL. Before
committing the project record, P:

1. validates the structured path and URL;
2. contacts the origin non-interactively with trusted host OpenSSH authority;
3. obtains a successful advertised-ref observation, even when it contains no
   refs;
4. creates and verifies the P bare repository; and
5. records the project, origin identity, and fresh observation together.

An unreachable, unauthorized, invalid, or ambiguously authenticated origin
does not establish a project. Temporary repository material is reconciled
idempotently, and Retry repeats the same request. P never retains a
configured-but-never-contacted origin as a completed project.

A non-empty origin supplies committed branch and tag sources as described in
[origin communication](communication-boundaries.md#origin-communication). P
does not mirror an origin ref into an ordinary P branch merely because the
project was created.

### Blank

The request supplies only the project path. P creates an empty bare repository
whose symbolic `HEAD` names `refs/heads/main`, then atomically reserves one
bootstrap session UUID assigned to the unborn `main` branch.

The bootstrap session uses the immutable P base image because there is no
committed devShell source. Its empty workspace has P as its only Git remote.
The session principal may create only its reserved `refs/heads/main`; the first
commit and first push create that ref through ordinary `git-receive-pack`.
P creates no artificial root commit.

This is the only MVP exception to creation from committed source. Until `main`
has a commit, another session cannot be created because no committed source
exists. If the bootstrap session is discarded after a commit, `main` becomes a
retained branch. If it is deleted, or is discarded before a ref exists, the
project is blank again and may create another bootstrap session on unborn
`main`.

A successfully contacted but empty origin uses the same bootstrap rule while
retaining the configured origin for later explicit publication.

## Origin association

A project has zero or one configured origin. Origin addition, replacement, and
removal are explicit host actions. Sessions never receive the origin URL or
credentials, and changing the project origin never rewrites a session remote,
branch, or policy snapshot.

Adding or replacing an origin uses prepare-then-commit semantics:

1. validate and successfully contact the proposed SSH URL;
2. obtain a fresh advertised-ref observation;
3. under the project origin-operation lock, verify that the recorded old
   origin is still the expected value; and
4. commit the new URL and observation together.

Failure leaves the previous origin and completed observation unchanged. A
successful replacement makes every older observation inapplicable. Removing
the origin is explicit and makes the project local-only; all origin-dependent
features are then bypassed rather than reported broken.

The transport, URL forms, observation rules, and host credential boundary are
owned by [communication boundaries](communication-boundaries.md#origin-communication).

## Sources and session creation

After project creation, ordinary session creation is a separate lifecycle
operation. Its committed source may be:

- an existing assigned or retained P branch;
- another permitted P commit selector; or
- a branch or tag from a successful fresh origin observation.

Selecting an origin source fetches the required objects and creates the new
ordinary P session branch at the captured commit. Later origin movement does
not move that P branch. P has no local-checkout import path.

The TUI may guide project creation directly into session creation, but the API
operations and their idempotency remain separate. The blank-project bootstrap
is the sole exception because its initial unborn session establishes the first
possible committed source.

## Retained branches

Discard ends a session but preserves an existing assigned P branch as a
retained branch. A retained branch is a project Git resource, not a session. It
has no UUID assignment, runtime, attachment, agent condition, or policy
condition.

P lists each retained branch with its name and tip. Under the project/ref lock,
host actions may:

- select it as committed source for a new session, which receives a new UUID
  and a distinct new assigned branch;
- fetch it through the read-only host P credential;
- rename it by atomically creating the absent destination at the expected tip
  and deleting the expected source;
- publish its captured tip to one explicit origin branch using the normal
  create-or-fast-forward rule; or
- delete it after a Git-only loss review and confirmation.

Rename never affects an origin ref. Deletion reports the exact ref/tip, commits
that would lose their last retained P ref, and fresh origin-preservation
evidence or `unknown`. A changed tip makes confirmation stale. P never
reclaims, renames, publishes, or deletes retained branches automatically.

## Project deletion

**Delete project and all P data** is one bulk destructive action. It means that
the project, every session and attachment, every assigned and retained P ref,
the bare repository, credentials, runtimes, project-scoped cached environment
images, observations, and registry records will become absent.

It does not delete external filesystem-grant contents or retract events already
delivered to a configured handler. Those are outside project-state authority;
the preview names them and points to the handler's own log-retention policy.
The project deletion and its outcome may itself be emitted as an event.

### Preflight and confirmation

Before confirmation P obtains the project lock and produces one aggregate
report containing:

- every session and its condition;
- every live attachment that confirmation will terminate;
- the runtime-local loss report for every reachable session;
- missing or unreachable runtime facts;
- every assigned and retained P ref and the commits that lose their last P
  reference;
- fresh origin-preservation evidence, or `unknown` when refresh fails;
- session and project credentials;
- project-scoped builders, images, and other owned cache resources; and
- external mounts and already-delivered event-log records that P will not
  delete.

The confirmation is bound to the project path, origin identity, session UUIDs,
attachment set, runtime locators/states, workspace fingerprints, ref names and
tips, and requested complete-deletion outcome. A changed fact requires a new
report. The action is named and described as deletion, never as unregistering.

### Ensure absent

After confirmation P marks the project `deleting`, disables all project and
session authority, tears down the confirmed attachments, and attempts every
owned deletion. Each step has the same monotonic desired result: absent.
Already-absent resources are success; there is no rollback or reconstruction
of a resource deleted by an earlier attempt.

The operation reports each target as `deleted`, `already_absent`, `remaining`,
or `unreachable`. Partial success is expected. Retry runs the same authorized
ensure-absent operation and normally has fewer remaining targets. The project
disappears only after all owned resources are confirmed absent.

If machinery is unreachable, ordinary deletion remains incomplete. A separate
stronger Abandon action disables local authority and retains sufficient
project/session/Incus identity as orphan tombstones so later machinery can be
recognized and contained.

## Recovery and idempotency

Project creation and origin changes use operation identity and expected inputs
but do not require multi-authority rollback after their commit point. A retry
reconciles provisional resources and reasserts the same desired result.

Project deletion persists a minimal tombstone containing the confirmed project
identity, requested outcome, and known owned resource identifiers until all
targets are absent. It is not a phase machine: on restart or explicit Retry, P
re-inspects authorities and attempts every remaining deletion. The project
registry record and tombstone are removed last so partial external cleanup is
never forgotten.

## MVP boundary

MVP includes explicit project creation from one SSH origin or blank state,
atomic origin add/change, immutable project paths, one unborn-main bootstrap,
retained-branch management, and bulk idempotent project deletion.

MVP excludes `p .`, host checkout registration/import, project rename,
automatic origin mirroring, automatic branch reclamation, multi-origin
projects, and project migration between P instances.

## Acceptance criteria

The project lifecycle is supported only when tests prove:

1. exact repeated creation/add/change/delete requests are idempotent;
2. a failed initial origin contact leaves no established project and a failed
   replacement leaves the old origin usable;
3. blank and empty-origin projects create no artificial commit, reserve only
   unborn `main`, and permit only its bootstrap session to create that ref;
4. project paths cannot be renamed or confused by equal origins;
5. retained branches can be selected, fetched, renamed, published, and deleted
   without acquiring session authority;
6. retained-branch and project deletion previews become stale when any bound
   Git, workspace, runtime, attachment, or origin fact changes;
7. confirmed project deletion terminates only its listed attachments, disables
   new project authority, and converges through repeated ensure-absent calls;
8. partial deletion and daemon restart retain enough identity to find every
   remaining resource without recreating deleted state; and
9. abandonment preserves recognizable tombstones for unreachable machinery.
