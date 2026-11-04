# Project glossary

Canonical domain language for this repository.

## Terms

### Agent adapter

A plugin capability made of files that the runtime driver installs in each
session, which turn an agent's own hooks into status reports to P.

- **Related:** [Plugin](#plugin), [unattended status](#unattended-status).
- **Boundaries:** P never executes it; the agent runs it inside the session as
  the session user. It is not a host-side capability.
- **Rules:** Trusted host configuration selects one package by path and
  digest, and each creation records the selection. The runtime driver installs
  the files root-owned and read-only, and the session verifies their digest
  before its host starts. The adapter reaches P only through the session's
  status socket, which can change nothing but that session's unattended
  status.

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

### Gate

A core callback that a driver calls at most once for an effect, after its last
check and right before the effect starts, so that core records its durable
intent first.

- **Related:** [Unknown outcome](#unknown-outcome),
  [runtime driver](#runtime-driver), [source driver](#source-driver).
- **Rules:** If the gate refuses, nothing happened, whatever the driver
  answers. If the call is lost or fails after the gate passed, the effect's
  outcome is unknown. A gate called after its call has ended is refused.

### Git authority

Core's control over which keys may read P's repositories and which branch
changes may happen: Git principals, ref guards and unborn grants in P's
durable control state, and an in-memory lock.

- **Related:** [Git principal](#git-principal), [ref guard](#ref-guard),
  [unborn grant](#unborn-grant), [source driver](#source-driver).
- **Boundaries:** Core keeps it. The source driver, which owns the
  repositories, asks core to open each Git service and to approve the one head
  update a push makes.
- **Rules:** Every Git service holds a read lease on the lock while it runs.
  Every lifecycle change to principals, guards or grants, and every
  publication, takes the write side, so a guard takes effect only once no push
  is in flight. Leases do not survive a daemon restart.

### Git principal

An SSH key that P's Git endpoint accepts, with its role and, for a session's
key, its project and session.

- **Related:** [Git authority](#git-authority),
  [assigned branch](#assigned-branch).
- **Rules:** A session principal may fetch its own project and push only its
  assigned branch, by fast-forward. It becomes active when its creation
  reaches its `principals-ready` phase, and inactive when a discard or delete
  of the session commits or a deletion of its project is accepted. The one
  host principal may only fetch, from any active project.
  P's Git endpoint refuses every other key.

### Grant

A named capability authorized by trusted developer configuration and made
available across a P isolation or extension boundary.

- **Rules:** A session or plugin cannot grant itself authority. Each grant has
  explicit scope and is enforced by the authority that owns the capability.
  Executable plugins hold no grants; trusted configuration selects them, and
  they run with P's authority.

### Hold

What keeps competing work off a session while a change to it is in progress.

- **Related:** [Operation](#operation).
- **Rules:** The durable hold is the session's unfinished operation: at most
  one running or blocked operation holds a session, except that an assembled
  creation cleanup shares its session with the blocked creation it cleans up.
  It survives a daemon restart. The in-memory hold is a per-session slot that
  a request takes or is refused with `session_held`, and that some workers
  keep for their whole run; a restart loses it. A worker's in-memory holds are
  released just before its operation's end is recorded.

### Host-side capability

A plugin-provided capability that participates in P lifecycle work outside a
session without being exposed to that session.

- **Example:** An event handler.
- **Rules:** An executable plugin that provides it runs as trusted code with
  P's authority, and P sends it only what its capability defines. It does not
  become a session service merely because its result affects one.

### Idempotency key

The key, 1 to 128 bytes, that a client chooses for one keyed change, so that
it can send the request again after a lost reply without making a second
change.

- **Related:** [Operation](#operation), [exact retry](#exact-retry),
  [change tag](#change-tag).
- **Rules:** Creating a project or a session, renaming, changing an origin and
  the run of every risky change carry one; start and stop do not. The same key
  with the same request returns the recorded operation; with another request
  it is refused as `key_reused`. Keys share one namespace across kinds, and a
  deleted project's keys stay refused for good.

### Incarnation

A driver's opaque identity for one runtime or one repository as it now
exists, which changes when that object is made again.

- **Related:** [Runtime driver](#runtime-driver),
  [source driver](#source-driver).
- **Rules:** A runtime keeps its incarnation across stop and start and gets a
  new one when it is re-created or restored; a repository gets a new one
  whenever it is made again. A destructive effect, such as deleting a runtime
  or a repository, acts only on the incarnation core reviewed.

### Internal session service

A service that runs inside a session's isolation boundary and is supervised
by the runtime, through the runtime driver.

- **Rules:** It operates with the session's effective grants and receives no
  ambient host authority from the plugin that provides it.

### Launch checks

The checks the daemon makes before every start or restart of a child
executable: the runtime driver, the source driver and the event handler.

- **Related:** [Runtime driver](#runtime-driver),
  [source driver](#source-driver), [event handler](#event-handler).
- **Rules:** Trusted host configuration pins each by absolute path and
  SHA-256: of the executable for a driver, of the whole package for the event
  handler. The checks refuse an executable whose files or directories anyone
  but root or P's own account could replace, compare what they hashed with the
  pin, and run that same open file, so the bytes checked are the bytes run. A
  failed first start stops the daemon from starting.

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

### Operation

P's durable record of one change to projects, sessions, branches or origins,
written before the change's first effect.

- **Related:** [Operation journal](#operation-journal),
  [idempotency key](#idempotency-key), [hold](#hold),
  [risky change](#risky-change).
- **Boundaries:** An origin refresh, a project service action and the release
  of unused commit images record no operation.
- **Rules:** An operation is `running`, `blocked`, `failed`, `superseded` or
  `completed`; the last three are final. It records its phase, whether it has
  passed its irreversible point, and the evidence its worker needs to resume.
  A blocked operation waits for `operation.retry`, or, for a creation, the same
  request sent again with its key. Interrupting a client never stops an
  operation.

### Operation journal

The durable list of P's operations, which one write path changes, recording
each operation and each of its status changes.

- **Related:** [Operation](#operation).
- **Rules:** A restarted daemon resumes every `running` operation at its
  recorded phase. Each committed status change wakes the clients waiting on
  that operation and emits an `operation.progress` event. Finished operations
  are kept, except that recording a session's start or stop removes that
  session's finished starts and stops, and completing a project deletion
  removes the project's other operations but keeps their keys refused.

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
  the session container. The runtime driver provides the host: the Incus
  driver runs tmux from its own systemd units and attach program, and no
  plugin provides or selects a host.
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

- **Boundaries:** A plugin may provide a host-side capability, an internal or
  external session service, an integration, or a workflow. It is not P core or
  a driver.
- **Rules:** Developers or agents may author, register, and test plugins.
  Registration does not activate a plugin. Trusted developer configuration
  activates an exact package for one capability. A plugin that runs inside a
  session has the session's grants and no more; an executable plugin runs on
  the host as trusted code with P's authority.

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

### Project service

An internal session service that the session's own work installs as a
systemd user unit named `p-project-*.service`.

- **Related:** [Internal session service](#internal-session-service).
- **Boundaries:** It is never the session's persistent interactive host or one
  of P's own units.
- **Rules:** P lists, starts, stops and restarts it, and reads its journal,
  only through the runtime driver, as the session user, with no more authority
  than the session's own work. An action is queued and returns at once, and it
  records no operation.

### Ref guard

A durable mark that an operation sets on a branch it is changing, which makes
P's Git endpoint refuse every push to that branch.

- **Related:** [Git authority](#git-authority),
  [assigned branch](#assigned-branch).
- **Rules:** Discard and delete set one before their irreversible point;
  renames, retained-branch changes and creation cleanup set theirs inside
  their own transactions. A guard takes effect only once no push is in
  flight. A blocked operation keeps its guards.

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
- **Boundaries:** It is part of trusted P, not a plugin. Repositories,
  requests, and plugins never reach it through P, though an executable
  plugin, which runs with P's authority, can open the Incus user socket
  itself. Its contract covers images and their builds, session runtimes, the
  workspace of a stopped session, terminals and project services; host
  configuration pins its executable by path and SHA-256 as `runtime.driver`.
  Its Incus implementation, `internal/runtimedriver/incus`, runs as the
  `p-runtime-incus` executable shipped with P; the daemon holds nothing of it
  but the driver interface.
  Every runtime effect goes through it: images, sessions, their removal, the
  workspace edits of a rename, terminals, project services, the boundary check
  and the inventory that admission and the proofs of absence read. The
  session's persistent interactive host is its own, not a plugin's.
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

The trusted executable that owns P's repositories and every Git effect for P
core, which the daemon starts, supervises and addresses in P's terms rather
than Git's layout over the source driver protocol.

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
  configuration pins its executable by path and SHA-256 as `git.driver`. Its
  Git implementation, `internal/sourcedriver/git`, runs as the `p-source-git`
  executable shipped with P; the daemon holds nothing of it but the driver
  interface. The daemon reaches P's repositories and Git only through the
  driver; no other P code runs `git` or `ssh` on the host.
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

- **Boundaries:** Attachment presence, the latest
  [unattended status](#unattended-status), and policy condition remain
  independent facts.
- **Avoid:** Separate public `runtime_condition` and `startup_readiness` fields.

### Unattended status

The latest status report that P accepted from within a session while no one
was attached to it.

- **Related:** [Agent adapter](#agent-adapter),
  [session condition](#session-condition), [attachment](#attachment).
- **Boundaries:** It is an observation, not an assertion of the agent's
  current state, and it is not a session condition. Any process in the
  session can send a report under any adapter name; P binds a report only to
  its session.
- **Rules:** Its condition is `running`, `attention`, `idle`, `failed` or
  `unknown`, or `none` when nothing is stored. P keeps only the latest report
  for each session and ignores reports while an attachment is open; confirming
  an attachment clears it. Every stored report emits a
  `session.unattended_changed` event.

### Unborn grant

The one permission to create `main` in a project that has no commits yet,
held by the project's bootstrap session until its first push consumes it.

- **Related:** [Bootstrap session](#bootstrap-session),
  [Git authority](#git-authority).
- **Boundaries:** It is part of Git authority, not a [grant](#grant) from
  trusted configuration.
- **Rules:** A blank project, or one created from an empty origin, gets a
  pending grant for its bootstrap session; that session's first push may
  create `main` only by consuming it. Discarding or deleting the bootstrap
  session removes it.

### Unknown outcome

The state of an effect whose driver call was lost after the effect may have
started, so P cannot tell whether it happened.

- **Related:** [Gate](#gate), [runtime driver](#runtime-driver),
  [source driver](#source-driver).
- **Rules:** A lost call whose gate never ran, or refused, changed nothing. A
  lost effect whose gate passed, or that takes no gate, has an unknown outcome
  until P resolves it; the restarted driver's `Settle` answers applied, not applied or
  unknown from fresh observation. Which effects P settles, and which it sends
  again or fails instead, is open question Q2 of the
  [architecture account](docs/architecture/README.md#open-questions).
