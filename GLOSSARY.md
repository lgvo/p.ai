# Project glossary

Canonical domain language for this repository.

## Terms

### Assigned branch

The one Git branch assigned to a session while that session exists.

- **Related:** [Session](#session), [retained branch](#retained-branch).
- **Rules:** Its complete address is `(project path, branch name)`. The session
  principal may update it only by fast-forward. Discard releases the assignment;
  Delete removes the branch.

### Attachment

A temporary terminal from one client to a session's persistent interactive
host, which the runtime driver opens and the daemon relays.

- **Boundaries:** It is not the persistent host and does not own container
  lifetime.
- **Rules:** An attachment counts in `attached_count` from the claim that
  opened its terminal until the runtime driver proves or settles the
  terminal's end, or the session stops. Detach, switching, or transport loss
  ends the attachment without stopping the host.

### Bootstrap session

The first session reserved on unborn `main` for a new blank project or a
successfully contacted empty origin.

- **Rules:** It is the only MVP session without a committed source. Its first
  push creates `main`; no artificial commit is created by P.

### Change hash

The daemon's name for one planned risky change: a SHA-256 of the change's
action, target and inputs, the P instance, and its plan.

- **Related:** [Change tag](#change-tag), [plan](#plan).
- **Boundaries:** It never leaves the daemon. The operation journal records it
  as the exact change that ran.
- **Rules:** The same state gives the same change hash; any changed fact in
  the plan changes it, except the facts a plan shows for information only,
  such as whether a removal's container was running and the runtime driver's
  name for it.

### Change tag

The value that a dry run returns with an allowed plan and that the run sends
back to carry out exactly that plan: an expiry followed by a hash that binds
the expiry to the change hash.

- **Related:** [Change hash](#change-hash), [dry run](#dry-run),
  [risky change](#risky-change).
- **Rules:** It expires two minutes after its dry run. A run whose fresh plan
  gives another tag is refused and records nothing. The daemon stores nothing
  between the dry run and the run.
- **Avoid:** `confirmation token`.

### Commit image

The immutable image sessions started from one source commit are created from:
the P base, that commit's Nix development environment, and a clone of the
commit in the workspace.

- **Related:** [Session](#session), [runtime driver](#runtime-driver).
- **Boundaries:** It holds nothing that belongs to one session: no UUID,
  branch, credentials, endpoints, or home. A session's later work stays in its
  runtime and never changes the image.
- **Rules:** P keeps one image per project, source commit, base image, system,
  and builder version, builds it in an isolated builder, and deletes it once
  no session started from its commit exists, or with its project. Images apply
  only when trusted host configuration enables image building; otherwise a
  session starts from the P base image.
- **Avoid:** `environment image` and `cached image` for this concept.

### Dry run

The first call of a risky change, which plans the change and returns its plan,
and its change tag when the plan is allowed, without starting an operation or
destroying anything.

- **Related:** [Plan](#plan), [risky change](#risky-change).
- **Rules:** A call without an idempotency key and change tag is the dry run.
  The second call, the run, repeats its parameters with both.
- **Avoid:** `preview` for this call.

### Event handler

A host-side capability that receives versioned, reduced P events after domain
state has been updated.

- **Boundaries:** It does not receive arbitrary raw agent payloads and is not a
  source of session truth.
- **Rules:** Handler failure cannot roll back session state. MVP provides a
  structured file-log handler and no durable delivery queue.

### Exact retry

Re-execution of one failed creation request with the same operation, UUID,
branch, source, policy snapshot, and idempotency key.

- **Rules:** P cleans or safely reuses verified partial derived resources and
  rebuilds the same request. Retry does not form a new operation chain.

### External session service

A service outside a session that is made available through an explicitly
granted network endpoint and service-native, session-scoped authentication.

- **Boundaries:** The service may be local to the P instance or remote;
  external describes its position outside the session boundary.
- **Rules:** The service retains authority over its protocol, data, and native
  authorization. P governs provisioning, scoped binding, validation,
  injection, and revocation of session access.

### Grant

A named capability authorized by trusted developer configuration and made
available across a P isolation or extension boundary.

- **Rules:** A session or plugin cannot grant itself authority. Each grant has
  explicit scope and is enforced by the authority that owns the capability.

### Host-side capability

A plugin-provided capability that participates in P lifecycle work outside a
session without being exposed to that session.

- **Example:** An event handler.
- **Rules:** It receives only the authority granted for its declared role and
  does not become a session service merely because its result affects one.

### Internal session service

A service that runs inside a session's isolation boundary and is supervised
by the runtime, through the runtime driver.

- **Rules:** It operates with the session's effective grants and receives no
  ambient host authority from the plugin that provides it.

### Loss item

One category of what a risky change destroys, or of what is already gone
before it runs.

- **Related:** [Plan](#plan).
- **Boundaries:** It states loss by category, such as a session's container
  and everything in it or the commits that leave P's repository; P does not
  look inside a container to list files. Deleting a cache, such as a commit
  image, is not loss.
- **Rules:** A loss that P cannot rule out is marked uncertain. What is
  already lost, such as a missing runtime's contents, is listed apart from the
  loss the change causes.

### Origin

The optional ordinary Git remote configured for a project and accessed only by
trusted host-side P operations.

- **Boundaries:** It is not another P instance and is never installed in a
  session workspace by P.
- **Rules:** Origin publication is explicit and create-or-fast-forward only.

### P core

The trusted part of P that owns identity, durable control state, lifecycle
orchestration, policy and grant enforcement, plugin activation, recovery, and
user confirmation.

- **Boundaries:** P core invokes plugins through typed contracts but does not
  delegate its authoritative lifecycle or security decisions to them.

### P instance

One independent P control plane with its own daemon, registry, Git server,
projects, sessions, and configured local Incus execution project.

- **Rules:** P instances do not discover, address, synchronize, or federate
  with one another.

### Persistent interactive host

The systemd-managed internal session service that anchors a running session
and accepts temporary attachments, such as tmux.

- **Boundaries:** A temporary attachment client is not the persistent
  interactive host.
- **Rules:** Detachment leaves the host alive. Host exit or failure shuts down
  the session container. MVP ships tmux as the default implementation.
- **Avoid:** `direct`, which is not an MVP host.

### Plan

What a risky change would do now: whether it can run and why not, its loss
items, what is already gone, and the change's own details.

- **Related:** [Dry run](#dry-run), [refusal](#refusal),
  [loss item](#loss-item), [change tag](#change-tag).
- **Rules:** A plan is allowed exactly when it has no refusals, and only an
  allowed plan's loss is complete. A run carries out only the plan its change
  tag names.
- **Avoid:** `preview` for the returned plan.

### Plugin

An independently authored, installable implementation of one or more stable,
documented P capability contracts.

- **Boundaries:** A plugin may provide a first- or third-party internal or
  external session service, host-side capability, integration, or workflow. It
  is not P core and does not provide a runtime; the
  [runtime driver](#runtime-driver) does.
- **Rules:** Developers or agents may author, register, and test plugins.
  Registration does not activate a plugin or grant authority. Trusted
  developer configuration controls activation, implementation selection, and
  grants; an activated plugin may operate autonomously only within them.

### Policy condition

The comparison between a session's immutable effective-policy snapshot and the
project's current trusted policy.

- **Rules:** `current` matches, `outdated` warns and remains usable, and
  `invalid` means the snapshot can no longer be applied safely and blocks
  Start.

### Project

The complete path of one bare repository on a P instance's Git server,
together with its trusted project policy and optional origin.

- **Rules:** Project paths are immutable in MVP. Projects are created explicitly
  from an SSH origin or as blank P repositories; host checkouts are not
  registered source locations.
- **Boundaries:** An Incus project is an infrastructure confinement boundary,
  not a P project.

### Refusal

A stable code, in a plan, for why a risky change cannot run now.

- **Related:** [Plan](#plan).
- **Boundaries:** A refusal is part of a plan, not an error: the dry run still
  returns the plan. It carries no message text; clients hold one sentence per
  code.
- **Rules:** Refusal codes share one namespace with error reasons.

### Replacement creation

A new Create request presented as **Try again with changes** after a failed
creation.

- **Rules:** It has a new UUID, operation, source/policy input, and idempotency
  key; supersedes the old request; and integrates safe cleanup and loss review
  so the user does not manually discard/delete first.

### Retained branch

An ordinary unassigned P branch preserved after its session is discarded.

- **Boundaries:** It has no runtime, attachment, policy, or agent status.
- **Rules:** It may be assigned to a new session on that same branch, or used
  as source when creating a different new branch. It may also be listed,
  renamed, published, or deleted after loss review.
- **Avoid:** `unassigned ref` when the user-facing concept is a branch.

### Risky change

A change that can lose work or replace something: discarding or deleting a
session, deleting a project or a retained branch, publishing to an origin, or
cleaning up or replacing a failed creation.

- **Related:** [Dry run](#dry-run), [plan](#plan), [change tag](#change-tag).
- **Rules:** Each risky change is one method called twice: a dry run that
  returns its plan, then a run that carries out that plan only while its
  change tag still matches.

### Runtime

The replaceable execution machinery associated with a session.

- **Boundaries:** A runtime is not session identity, an attachment, or an
  agent. MVP realizes it as one Incus system container, but the concept does not
  depend on that implementation.

### Runtime driver

The trusted executable that owns runtime machinery for P core, which the daemon
starts, supervises and addresses in P's terms rather than its backend's over
the driver protocol.

- **Related:** [Runtime](#runtime), [commit image](#commit-image).
- **Boundaries:** It is part of trusted P, not a plugin; repositories,
  requests, and plugins never reach it. Its contract covers images and their
  builds, session runtimes, the workspace of a stopped session, terminals and
  project services; host configuration pins its executable by path and
  SHA-256 as `runtime.driver`. Its Incus implementation,
  `internal/runtimedriver/incus`, runs as the `p-runtime-incus` executable
  shipped with P; the daemon holds nothing of it but the driver interface.
  Every runtime effect goes through it: images, sessions, their removal, the
  workspace edits of a rename, terminals, project services, the boundary check
  and the inventory that admission and the proofs of absence read.
- **Rules:** It acts only on objects carrying its P instance's labels, stores
  those labels immutably, and claims nothing after an error. Core identifies a
  runtime by the driver's opaque incarnation, which stays the same across stop
  and start and changes when the runtime is re-created or restored, and shows
  the driver's own name for a runtime only for display. The daemon starts the
  driver only after its launch checks and pinned digest pass, agrees the
  protocol version with it, and restarts it when it dies; an effect whose call
  was lost with the driver's connection is settled through the restarted
  driver, never sent again.

### Source driver

The trusted code that owns P's repositories and every Git effect for P core,
addressed in P's terms rather than Git's layout.

- **Related:** [Runtime driver](#runtime-driver), [origin](#origin).
- **Boundaries:** It is part of trusted P, not a plugin. Its contract covers
  repositories, heads and their compare-and-swap, commits and their export for
  image builds, Git's syntax for the names P stores, the Git edits that rename
  a session's clone, the Git service behind P's endpoint, and the origin
  transport. Core keeps lifecycle state, Git authority (principals, leases,
  guards and grants), the SSH server and its keys, the origin URL and
  observations, and each project's origin lock; the driver never opens P's
  database or reaches a runtime. Core's SSH server authenticates a session's
  key and hands the stream to the driver's Git service, which asks core to
  open the service and to approve the one head update a push makes. Host
  configuration selects it as `git.driver`; its Git implementation,
  `internal/sourcedriver/git`, runs inside the P daemon, which holds nothing
  of it but the driver interface. The daemon reaches P's repositories and Git
  only through the driver; no other code runs `git` or `ssh` on the host.
  Core pins the driver's backend at its first start and refuses another while
  any project exists; sessions pin no source package.
- **Rules:** It acts only on P's repositories in its configured storage,
  changes a head only at an exact expected state, and claims nothing after an
  error.

### Session

P's durable identity for one piece of branch work, represented by an immutable
UUID and exactly one assigned branch while the session exists.

- **Boundaries:** A session is not an agent, task, terminal, tmux server, or
  container identity.

### Session condition

The single public lifecycle condition describing whether a session is being
created, started, is ready, stopped, missing, unreachable, discarding, or
deleting.

- **Boundaries:** Attachment presence, latest unattended agent condition, and
  policy condition remain independent facts.
- **Avoid:** Separate public `runtime_condition` and `startup_readiness` fields.
