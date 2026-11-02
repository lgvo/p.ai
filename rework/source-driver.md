# One source driver for every Git effect

Status: proposed, 2026-11-02. Part of the [interface rework plan](REWORK-PLAN.md).

## Summary

**Today** P's Git work has no single owner. `internal/gitservice` keeps a
bare repository per project under `STATE/repositories`, serves them through a
Wish SSH server, and runs `git` itself for reads, loss analysis, snapshots and
the transport. Nine command kinds go through the WASI source-git module
(plugins/bundled/source-git), which decides nothing: each broker call runs Git
with arguments core chose, and core checks the outcome. The daemon calls
gitservice directly and removes a project's repository itself (`os.RemoveAll`
in project_delete_process.go). gitservice in turn reads operations and
sessions from SQLite to authorize two kinds of head creation. Git authority
(principals, leases, ref guards, unborn grants) lives in `control.Store`.

**Decided by the maintainer on 2026-11-02:**

- One source driver that all source and Git work goes through, replaceable
  like the [runtime driver](runtime-driver.md). Today's Go Git code becomes its
  default implementation. This replaces the proposal in
  [plugins.md](plugins.md) to fold source-git into core.
- Executable plugins and drivers are trusted code: selection, digest pinning
  and launch checks, no sandbox. OS confinement may come later.
- Deletions do not check the origin: whether work is published is the user's
  concern.
- Steps 0d and 0e remove workspace inspection and loss inspection before the
  drivers land.
- Contracts may break; fresh installs only; users see the same end result
  unless a decision says otherwise.
- The SSH server stays in core as reusable SSH code: it authenticates and
  authorizes keys, then hands the authenticated stream to a named service.
  Git, through `ServeGit`, is the first service and for now the only one.
- The Git edits that rename makes in a stopped session's clone move to the
  driver, so that Git code lives there. Core relays: the driver never reaches
  a runtime.
- `Export` gives the image build a Git bundle of the commit, which the builder
  clones into `/workspace`; snapshot.go's host-side tree checks go.
- Core records `Describe().Backend` at the first start and refuses another
  backend while any project exists; a new digest of the same backend is an
  upgrade.
- The driver lands as the first part of step 9 (S1 to S7), then plugins.md's
  work.
- Git's rules move to the driver too: branch and origin ref names, origin
  URLs, commit IDs, and the SSH command after its first word. Core stores and
  compares names, URLs and commit IDs as bounded strings; the driver refuses
  invalid ones as `invalid`. Git in the guest (runtime-kit's workspace setup,
  git-stream, `p-git-ssh`, the builder's clone) stays with the runtime driver.

**Proposed:** a `sourcedriver.Driver` in P's terms, with operations for the
host, Git's syntax, repositories, heads, commits, a session's clone, the Git
service, the origin and settling. The driver owns how P's repositories are
stored and how Git runs on them: layout and safety checks, objects, heads and
their compare-and-swap, the Git service behind P's endpoint with its
pre-receive check, the origin transport, the commit export for image builds,
and the Git edits of a renamed session's clone. Core keeps lifecycle state,
the journal, policy, change tags, Git authority in SQLite, the SSH server and
its keys, and the origin URL and observations. The driver ships as
`p-source-git`, started and checked like `p-runtime-incus`.

**What users notice:** no workflow changes. The `git` block of host
configuration keeps `listen` and gets `driver: {path, sha256, config}` instead
of `activation_path` and `source_plugin_id`. `system.inspect` names the driver
instead of the source plugin, and `p plugins defaults` no longer lists
source-git. Origin access still uses the daemon account's SSH agent and
configuration.

## What changes owner

Only effects whose owner or guarantee changes; the rest stays where
[Who does what](#who-does-what) puts it.

| Effect | Today | After |
|---|---|---|
| **Repositories** | | |
| Create with HEAD on `main`; check the repository before every use | `EnsureBare` (WASI `git.project.ensure`), `checkRepository` in gitservice | `CreateRepository`; the checks inside every call |
| Delete, pinned by local identity, after a `DeleteIssued` marker | `os.RemoveAll` in the daemon (project_delete_process.go) | `DeleteRepository` at an incarnation |
| **Heads** | | |
| List heads; read one exactly, a symbolic ref counting as present | `ListRefsPage` (WASI), `InspectBranchRef` | `Branches`, `InspectBranch` |
| Create only if absent: assignment, origin capture, renames | Three gitservice methods once 0h removes ref repair's (WASI `git.branch.create`); one reads SQLite | `CreateBranch`; the SQLite checks move to core |
| Delete only at the expected tip | `DeleteAssignedBranchExact` (WASI) | `DeleteBranch` |
| Git's syntax: branch and origin ref names, the creation source, commit IDs | `validBranch` and `validOID` in control, `plugin.ValidGitOriginRef`, `git check-ref-format` in gitservice | `CheckNames`, before P records anything; core keeps length and byte bounds |
| **Commits** | | |
| Resolve a creation source; is a commit present and reachable from a head | `CaptureLocalSource`, `ObserveSource` (WASI), `PCommitPresent` | `InspectBranch`, `InspectCommit`; the source rules stay in core |
| Every head with its tip; the commits a deletion removes (`p_commits`) | `AssignedBranchSnapshot`, `BranchRemovalLoss`, `ProjectRemovalLoss` | `Branches`, `LosingCommits` |
| The commit for the image builder | `CapturePinnedCommit` → `SourceSnapshot`, a tree checked on the host → the Incus builder | `Export`: a Git bundle, which core hands to `BuildImage` and the builder clones; the host-side checks go in step 1 |
| **A session's clone** | | |
| Rename's Git edits: `.git/HEAD`, `.git/config`, the head and reflog files, a backup until done | workspace_rename.go in runtimeincus, with `safeGitConfig` | `RenameWorkspace` computes them; core reads and writes the files through the runtime driver |
| **P's Git endpoint** | | |
| Parse the SSH command: which service, which repository | `parseCommand` in gitservice | Core picks the service by the first word; `ServeGit` parses the rest |
| Transport limits, 1 GiB and 10 minutes | WASI `git.transport.plan`, which may only tighten them | Core passes them as ceilings |
| `upload-pack` and `receive-pack` with today's restrictions | `handle` in gitservice | `ServeGit` |
| Pre-receive check (one update, of the allowed head, to a commit, fast-forward), then approval, which consumes the unborn `main` grant | `p git-hook`, hook.go | The check: driver. Approval and grant: core, as `Approve` |
| **Origin** | | |
| URL rule: SSH forms only | `ValidateOriginURL` in gitservice | `CheckNames` |
| Observe, fetch, compare, push | `OriginScope`, three of them through WASI | `ObserveOrigin`, `FetchOrigin`, `ComparePublication`, `Publish` |
| **Gone before the driver** | | |
| Workspace inspection and loss analysis; `RetainedLossRefs`; origin checks before deletions (`CompareOriginRemoval`) | gitservice, runtimeincus helpers | **Go** in 0d, 0e and 0h: `RetainedLossRefs` serves only the loss inspection (0e) and ref repair (0h) |

## Who does what

The driver owns how P's repositories exist and how Git runs on them. Core
owns what P decides: which head a session may update, when a head is created
or deleted and on what evidence, the guards that keep sessions out meanwhile,
and every durable record of intent. The driver never opens P's SQLite
database and never reaches a runtime; what it needs arrives as an argument or
a gate.

| Core | Source driver | Runtime driver |
|---|---|---|
| Lifecycle, journal, policy, change tags, loss items | Repository layout and safety | Runtimes, images, the guest |
| Git authority in SQLite; the source rules; the bounds of names, URLs and commit IDs | Git's syntax; heads and their compare-and-swap; reachability and loss queries | — |
| `git.sock`, the SSH server, its host key, the host client key, session keys | Upload and receive services; the pre-receive check | Delivering `git.sock`, the session key and the guest's Git client files |
| Origin URL, observations, publication records, the origin lock | The origin transport | — |
| The staging directory of a build | `Export` into it | `BuildImage` from it |
| Rename's phases and gates; reading and writing the clone's files | Rename's Git edits | The workspace file operations |

The two drivers never talk to each other. They meet in three places, and core
sits between them in each:

- **The builder's input.** Core creates a private staging directory, asks the
  source driver to `Export` a Git bundle of the commit into it, passes it to
  `BuildImage` as `Build.Source`, and removes it afterwards. The builder
  clones the bundle into `/workspace`.
- **The session's endpoint.** Core creates `git.sock` and serves it with its
  SSH server, which authenticates the key and picks the service by the
  command's first word. Git, the only service, is registered for
  `git-upload-pack` and `git-receive-pack`: `ServeGit` parses the rest of the
  command and calls core's `open`, which checks the key's principal for that
  project, takes the lease, and returns the service's limits and `Approve`.
  The runtime driver makes `git.sock` reachable in the guest, with the key and
  the SSH client files.
- **A renamed session's clone.** Core reads the files `RenameWorkspace` asks
  for through the runtime driver's workspace operations, passes them in, and
  writes the edits it returns ([below](#renaming-a-sessions-clone)).

```text
guest ssh ─► runtime-kit git-stream ─► /run/p/git.sock ─► daemon proxy ─► SSH server ────► Driver.ServeGit
             runtime driver's files    runtime driver      core            core: key,       parses the command;
                                       delivers it                         first word       upload-pack, receive-pack,
                                                                                            pre-receive check
                                                                           core: lease ◄─── open(kind, project)
                                                                           core: grant ◄─── Approve(update)
```

## The driver interface

Three ideas carry it:

- **P's names, not Git's layout.** Core names a project, a branch, a commit
  and an origin URL; the driver decides where repositories live and how Git
  runs. Core never sees a repository path.
- **Gates**, as in [runtime-driver.md](runtime-driver.md#the-driver-interface):
  a lifecycle mutation calls a core function once, after its checks and right
  before its first effect, and core records its durable intent there.
  `ServeGit`'s `open` and `Approve` are the exceptions: core's authorization
  of a service and of a session push, each called once, which record no
  intent. `Approve`'s one durable effect is today's: a first push to an empty
  repository consumes the unborn `main` grant.
- **Exact identity.** Every change names the state it expects: a head is
  created only if absent and deleted only at its expected tip, a repository is
  deleted only at its incarnation, a push to the origin only creates or
  fast-forwards.

The sketch is illustrative.

```go
package sourcedriver // internal/sourcedriver

// Driver is the only code that creates, changes or reads P's repositories,
// and the only code that runs Git against them or against an origin.
type Driver interface {
	// Host
	Describe() Description           // the backend's identity, which core pins, and its version
	Check(ctx context.Context) error // prove the storage boundary and the Git tools

	// Git's syntax, which core does not know
	CheckNames(ctx context.Context, names []Name) ([]Name, error) // ErrInvalid names the first one at fault

	// Repositories, one per project
	InspectRepository(ctx context.Context, project string) (Repository, error)
	CreateRepository(ctx context.Context, project string) (Repository, error) // converges; default branch main
	DeleteRepository(ctx context.Context, r Repository, g Gate) error         // that incarnation only

	// Heads: refs/heads/<name>, and nothing else
	Branches(ctx context.Context, project string) ([]Head, error)                  // every head, one observation
	InspectBranch(ctx context.Context, project, name string) (OID, bool, error)    // a symbolic ref there is ErrConflict
	CreateBranch(ctx context.Context, project, name string, at OID, g Gate) error  // only if absent
	DeleteBranch(ctx context.Context, project, name string, tip OID, g Gate) error // only at that tip

	// Commits
	InspectCommit(ctx context.Context, project string, id OID) (Commit, error)
	LosingCommits(ctx context.Context, project string, removed []string) (Loss, error) // nil removes every head
	Export(ctx context.Context, project string, id OID, dir string) (Export, error)    // a Git bundle, into a directory core made

	// A session's clone, whose files core reads and writes through the runtime driver
	RenameWorkspace(ctx context.Context, r WorkspaceRename, files []WorkspaceFile) (WorkspaceEdits, error)

	// The Git service behind P's endpoint, on the SSH channel core authenticated
	ServeGit(ctx context.Context, command string, stream Stream, open Open) error

	// Origin
	ObserveOrigin(ctx context.Context, url string) ([]OriginRef, error)
	FetchOrigin(ctx context.Context, project, url string, ref OriginRef) (OID, error) // objects only, no head
	ComparePublication(ctx context.Context, project, url string, source OID, dest *OriginRef) (Relation, error)
	Publish(ctx context.Context, project string, p Publication, g Gate) (PublishStatus, error)

	// What became of a call that returned ErrUnknownOutcome, or of a lost ServeGit:
	// Applied, NotApplied or Unknown
	Settle(ctx context.Context, e Effect) (Settlement, error)
}

type OID string // a commit ID; core only stores and compares it

type Name struct {
	Kind  NameKind // branch, renamed or retained branch, origin ref, creation source, origin URL or commit ID
	Value string   // within core's bounds
}

type Repository struct {
	Project     string
	Exists      bool
	Incarnation string // opaque; new when the repository is made again
}

type Commit struct {
	Present   bool // a commit object in P's repository
	Reachable bool // from some head
}

type WorkspaceRename struct {
	ID       string // core's rename operation; names the backup the clone keeps until done
	Old, New string // branch names
	Goal     Goal   // prepared (backup written), renamed, or done (backup removed)
	Plan     string // empty, or the Plan an earlier call of this rename returned
}

type WorkspaceFile struct { // a path in the clone, as the runtime driver read it
	Path string
	File File // absent, or its type, owner, mode and bytes
}

type WorkspaceEdits struct {
	Read  map[string]int64 // paths to read, each within its size bound, before calling again; nothing else is set
	Edits []WorkspaceFile  // otherwise the edits, in order, an absent File removing; none: the goal holds
	Plan  string           // opaque to core; the same on every call of one rename
}

// Open is core's authorization of one service, called once before any Git
// runs: it checks the key's principal for the project and takes the lease.
type Open func(kind ServiceKind, project string) (Service, Approve, error)

type Service struct {
	ID       string        // core's name for this service, for Settle
	Project  string
	Kind     ServiceKind   // upload or receive
	Branch   string        // receive: the one head it may change
	MaxInput int64         // core's ceiling, 1 GiB
	Deadline time.Duration // core's ceiling, 10 minutes
}

// Approve is core's authorization of a session's one head update.
// Old is empty when the push creates the branch.
type Approve func(branch string, old, new OID) error

type OriginRef struct { // as advertised; ObserveOrigin checks no object type
	Ref    string // refs/heads/* or refs/tags/*
	OID    OID    // the advertised object ID
	Target OID    // a tag's advertised peeled target, otherwise OID; FetchOrigin checks it is a commit
}

type Effect struct {
	Kind     EffectKind // create-branch, delete-branch, or service
	Project  string
	Branch   string
	Old, New OID    // a head effect: the state before, and the state intended
	Service  string // a service: its ID; Applied means it can no longer write
}

type Gate func() error // a lifecycle gate: core records its durable intent
```

The errors are `ErrUnknownOutcome` (the effect may have happened; settle it),
`ErrInvalid` (a name, URL, commit ID or command breaks Git's syntax),
`ErrConflict` (a head or object is not in the expected state), `ErrNotFound`
(no such repository), `ErrLimit` (a bound was exceeded) and `ErrUnavailable`
(the backend or the origin cannot be reached).

### What each operation guarantees

| Operation | The driver must |
|---|---|
| All | Act only on P's repositories in its configured storage. Read native state fresh on every call; after an error, claim nothing. Never run code or configuration a repository can change: hooks, filters, repository config, submodules, lazy fetch. Never prune an object: P has no garbage collection, and a pending creation relies on an origin commit that no head holds yet. Accept every value `CheckNames` accepts, refuse others with `ErrInvalid`, and return only names it accepts. |
| `Check` | Prove that its storage is private to P's account and that its Git tools are present, at start and before every mutation; refuse rather than proceed. |
| `CheckNames` | Decide from each value and its kind only, offline, the same way every time. Return each value as core uses it: a creation source as a branch or a commit ID, so core picks `InspectBranch` or `InspectCommit` without reading Git's syntax. The bundled driver keeps today's rule for each field: `validBranch`, or `ValidateRenameBranch` with `git check-ref-format` for renamed and retained branches; `ValidGitOriginRef`; a creation source that is a head ref or a commit ID; `ValidateOriginURL`; `validOID`. |
| `CreateRepository` | Converge, so a repeated call is safe: make the repository complete or not at all, keep an existing one, and leave its default branch `main`, as `git.project.ensure` does. |
| `DeleteRepository` | Act only on the given incarnation; afterwards the project's repository is absent. Safe to call again at the same incarnation. |
| `Branches` | Return every head as one observation, in byte order, each tip a commit present in P's repository, or an error; `ErrLimit` above 1024 heads. The bundled driver reads twice and compares, as today's loss code does. |
| `InspectBranch` | Report the exact head, absent, or `ErrConflict` when a symbolic ref holds the name. |
| `CreateBranch` | Create the head only if nothing holds the name, a dangling symbolic ref included, and only at a commit present in P's repository; an existing head is `ErrConflict` even at the same tip. Call the gate before the effect. |
| `DeleteBranch` | Delete the head only while it names exactly `tip`; call the gate before the effect; read absence back. |
| `InspectCommit` | Report whether the object is a commit in P's repository, and whether some head reaches it. |
| `LosingCommits` | Return the commits reachable from the removed heads and from no other, with the head observation used; `ErrLimit` above 256. |
| `Export` | Write a Git bundle of the commit and every commit it reaches, as a session's clone fetches today, into the given directory and nothing outside it, within core's deadline (10 minutes, the ceiling of P's Git service); leave no partial bundle. No byte bound: unlike today's tree, the bundle packs objects P's repository already stores. |
| `RenameWorkspace` | Decide from the files given only. Ask only for the workspace root and paths in its Git directory, and edit only the latter. Refuse what today's rename refuses, with today's diagnostics (workspace_rename.go), such as HEAD off the old branch, an upstream other than the old branch's, packed refs, worktrees, a Git lock, a file the session user does not own or others can write, or a config `safeGitConfig` rejects. Toward `prepared`, edit only the backup; toward `done`, remove only the backup, whatever the rest of the clone holds; toward `renamed`, order the edits so the clone is valid after each, and refuse a file that is neither as the backup holds it nor as the goal needs it. Return no edits only when the goal holds exactly, and the same `Plan` on every call of one rename. |
| `ServeGit` | Parse the command and call `open` once, before any Git runs; refuse a command it cannot parse with `ErrInvalid`. Upload: advertise only heads and send only objects they reach. Receive: accept exactly one update, of `refs/heads/<Branch>`, to a commit, a fast-forward or, from an empty old value, a creation; refuse deletions; keep pushed objects apart until checked; call the `Approve` that `open` returned once, after the checks and before the update; move the head by compare-and-swap from the old value. Bound input and time by the ceilings. Return only when nothing the service started can still change a head. |
| `ObserveOrigin` | Contact the origin afresh; return at most 1024 heads and tags with their advertised IDs and peeled targets, unverified: it fetches nothing and checks no object type. An empty list is a successful answer. |
| `FetchOrigin` | Bring in the observed ref's objects, creating no head; `ErrConflict` if the ref moved since the observation. When `OID` equals `Target`, as for a branch or a lightweight tag, refuse unless that object is a commit; when they differ, refuse unless `OID` is an annotated tag that peels to `Target` and `Target` is a commit. Safe to repeat. |
| `ComparePublication` | Fetch only the destination's objects, create no ref, and report the relation from Git ancestry: absent, equal, destination-contains, fast-forward or divergent. |
| `Publish` | Push only for an absent or fast-forward relation, the exact commit to one head, with no force and no tags, and only while the publication's source branch still names it. Call the gate before the push. Return `created`, `advanced` or `satisfied` from the origin's answer, `refused` only on its definite rejection, and `ErrUnknownOutcome` when the answer is lost after the push started. |
| `Settle` | For a head effect: `Applied` only when the intended state holds exactly and nothing the driver admitted can still change it; `NotApplied` only when the prior state holds and nothing can still change it; otherwise `Unknown`. For a service: `Applied` once nothing it started can still change a head, `Unknown` until then, never `NotApplied`. Answers hold after a restart. |

### What absent proves

A driver that cannot tell, for lack of access or reach, returns an error,
never an absence.

| Answer | Proves |
|---|---|
| `InspectRepository`: not `Exists` | Nothing is at the project's address, complete or partial |
| `InspectBranch`: absent | Neither a head nor a symbolic ref holds the name |
| `InspectCommit`: not `Present` | The object is not a commit in P's repository. It says nothing about the origin or a workspace |
| `ObserveOrigin`: no such ref | The origin did not advertise it in this observation |
| `LosingCommits`: none | Every commit of the removed heads stays reachable from a remaining head of the observation |

### Names, URLs and commit IDs

Core only stores and compares branch names, origin refs, creation sources,
origin URLs and commit IDs. It keeps the bounds SQLite and its logs need,
which are not Git's syntax: valid UTF-8 without ASCII control characters, and
a length per field (200 bytes for a P branch, 523 for an origin ref, 255 for a
creation source, 2048 for a URL, 64 for a commit ID), checked by the request
types' `Validate` ([wire-types.md](wire-types.md)). Each handler passes the
fields today's handlers check with a Git rule to `CheckNames` right after its
replay lookup, so a recorded request is still answered from SQLite alone, and
before it records anything. A bad value is refused `invalid`, with its field
([errors.md](errors.md)).

### The Git service and ref guards

Guards stay core's. `AcquireGitLease`, inside `open`, refuses a receive
service for a guarded branch before any Git runs, and every service holds Git
authority's read lock until it is over, so a guard can be set only while no
service can write. The driver adds one rule: a session's head changes only inside
`ServeGit`, and only after `Approve`. `Approve` rechecks that the update names
the allowed head, and consumes the unborn `main` grant when the old value is
empty (`ConsumeGitUnbornGrant`, durable even if the push then fails, as today).

**A lease ends when its service can no longer write**, not when P learns what
the push did. Leases are shared and not ordered per branch, and one session
may hold eight streams, so two approved pushes can move a head A→B→C before
core hears that the first ended; whether "A→B" applied is then neither
answerable nor needed. A session push records no intent in core: the head is
the record, and lifecycle code reads heads afresh under its guard. Today's
`handle` likewise releases its lease when `receive-pack` exits, whatever the
outcome.

- When `ServeGit` returns, its service is over and core releases the lease.
- When the call is lost, because the driver died or its connection dropped,
  core keeps the lease until `Settle` reports that service ended. The bundled
  driver runs `receive-pack` in the driver's process group, which the daemon
  kills before a restart, so the restarted driver reports every earlier
  service ended at once.
- No push is replayed: the session's Git client sees a failed push and
  decides for itself.

The head compare-and-swap remains the authority, as today: lifecycle code
creates and deletes heads at exact values, so a push that a guard missed makes
the lifecycle change fail with a conflict instead of losing work.

### Renaming a session's clone

Rename's phases, gates and guards stay core's, as in rename.go; only the Git
knowledge moves. At each workspace phase, core asks `RenameWorkspace` for that
phase's goal: the backup before P's new head is created (`prepared`), the
renamed clone after it (`renamed`), and the backup's removal (`done`), once
P's old head is deleted or when rename rolls back before creating the new
one. Core reads the paths the driver asks for with the runtime driver's
`ReadWorkspace`, calls again with them, and applies each returned edit with
`WriteWorkspace` or `RemoveWorkspace` under the phase's gate. It then reads and calls once more: no edits left is the postcondition,
and the `Plan` must equal the one recorded at `workspace-prepared`, as today's
`WorkspaceTip` and `ConfigAfterSHA256` must.

- The workspace operations act only on a stopped session at its incarnation,
  so rename stays stopped-only.
- `RenameWorkspace` has no effect, so it takes no gate and needs no settling.
- Each write's gate records the edit's path. After a write whose outcome was
  lost, the issued phase advances only when `RenameWorkspace` returns no edits
  and the runtime driver settles that write `Applied` for the file now there;
  otherwise it stays blocked. No write is sent again.

### Settling uncertain effects

The runtime driver's rules apply to P's repositories:

1. **A guard until settled.** The gate's record stays until the effect settles:
   creation's `RefCASIntent`; rename's `new-ref-create-issued` and
   `old-ref-delete-issued`; retained rename's and retained delete's issued
   phases; session deletion's
   `branch-delete-issued`; project deletion's `DeleteIssued`; publication's
   `attempted`.
2. **Only a positive outcome settles.** An issued phase advances only on
   exact presence or absence, as today.
3. **An effect is never sent again blindly.** Head creations and deletions
   settle through `Settle`. `CreateRepository`, `DeleteRepository` and
   `FetchOrigin` are idempotent by reference and may be called again. A Git
   service settles only as ended. A push to the origin is never settled or
   resent: `outcome_unknown` stays the answer for its key, as today.

## The replaceability test: a forge

The runtime driver's test is Kubernetes. The source driver's is a Forgejo
instance holding P's repositories, while sessions still reach them only
through P's endpoint. Nothing was prototyped; this follows Git's protocol and
Forgejo's documented REST API.

The contract allows a forge driver:

- a repository incarnation that is the forge's repository ID;
- compare-and-swap through Git's push protocol, which carries the expected
  old value, where the REST API takes none;
- origin commits held under a private ref, as long as `Branches` and
  `ServeGit` show only heads;
- `ServeGit` by relay to the forge;
- `RenameWorkspace` taken from the bundled driver, since a session's clone
  does not depend on where P's repositories live;
- the forge's own limits on names, refused by `CheckNames` before P records
  anything;
- `Settle` answering `Unknown`.

Unresolved blockers:

- **Approval timing.** `Approve` must come after the pushed objects are
  checked and before the head moves. A relay cannot tell whether a commit
  fast-forwards until the forge has the objects, and forge-side hooks are an
  administrator setting.
- **What sessions may read.** A forge advertises its own refs and has its own
  rules for wants; a relay can filter the advertisement but cannot check a
  want's reachability without the objects.
- **Garbage collection.** The forge collects on its own schedule, which can
  remove an origin commit that no ref holds yet.
- **Reachability.** `InspectCommit.Reachable` and `LosingCommits` need the
  whole head set; the REST API compares two refs at a time, so a driver
  probably needs a local mirror.
- **Proofs.** A "not found" may hide a repository the account cannot see, and
  a relayed push may not be provably over. Core needs both proofs: absence
  before it records a deletion, and a service's end before it releases the
  lease, which blocks every guard change on the instance until then.

## Trust and launch

Selection, launch checks, protocol, supervision and crash handling are the
runtime driver's ([runtime-driver.md](runtime-driver.md#how-a-driver-is-replaced),
[plugins.md](plugins.md#trust)), with host configuration
`git: {listen, driver: {path, sha256, config}}` and each `ServeGit` stream on
its own connection, as each terminal is. What is specific to the source
driver:

- **Credentials.** It holds read and write access to every repository and the
  daemon account's origin access: SSH agent, `~/.ssh` configuration, known
  hosts and network. gitservice holds the same inside the daemon today. The
  daemon passes it exactly `HOME` and `SSH_AUTH_SOCK`, as `originEnv` does
  today; its package pins `git` and `ssh`, since it inherits no `PATH`. P
  never hands it SQLite, session keys, the SSH host key or the Incus socket,
  though as P's account with no sandbox it could open them.
- **The hook.** The pre-receive script in the driver's storage executes the
  driver instead of `p git-hook`, and talks to it over a per-push socket, as
  it talks to the daemon today.
- **The backend pin**, as decided in the Summary: a driver of another backend
  would find none of the instance's repositories. Sessions no longer pin a
  source package at creation.

## Migration

Each step is a commit that leaves `go vet`, the unit tests, the TUI tests,
`nix flake check`, `nix build` and the VM scenarios green on a fresh install.
The steps are the first part of step 9 and assume 0d, 0e, step 1 and step 8
have landed. Step 1 already gives the builder a Git bundle, written by
gitservice, and step 8 already runs rename's Git edits in core over the
runtime driver's workspace operations.

| Step | What | Removed |
|---|---|---|
| S1 | `internal/sourcedriver`: the interface, an in-memory driver and a conformance suite; the bundled driver in process (`internal/sourcedriver/git`), built from gitservice's broker bodies, its direct Git code, core's rename Git edits and today's name and commit ID rules. Host configuration gets `git.driver: {name, config}`. | — |
| S2 | Repositories, heads and commits go through the driver: `EnsureBare`, `InspectBranchRef`, `CaptureLocalSource`, the three create methods, `DeleteAssignedBranchExact`, `ListRefsPage` behind `control.GitReader`, `PCommitPresent`, the loss methods, and project deletion's `os.RemoveAll`. The SQLite checks of `CreateCapturedOriginBranch` move to the daemon. Handlers check requests' names and commit IDs with `CheckNames` after their replay lookup, and core's checks keep only its bounds. Rename's workspace phases call `RenameWorkspace`. | Five command kinds; `RepositoryPath`; core's rename Git edits and `safeGitConfig`; the Git syntax in control's `validBranch` and `validOID`; `ValidateRenameBranch` |
| S3 | Origin: core keeps the per-project lock and calls `ObserveOrigin`, `FetchOrigin`, `ComparePublication` and `Publish`; control's origin and publication interfaces adapt to them. `CheckNames` takes the URL rule. | `OriginScope`; three command kinds; `ValidateOriginURL` |
| S4 | `Export` writes the bundle; core owns the staging directory and hands it to `BuildImage`. | gitservice's bundle code |
| S5 | The SSH server leaves gitservice for a core package that authenticates keys and picks a service by the command's first word; `ServeGit` parses the rest and calls core's `open`, which takes the lease, and the hook approval becomes `Approve`. Core pins the backend instead of a source package per session. | `git.transport.plan`; the source-git package, `plugin.RunSourceGit` and the Git brokers; `activation_path` and `source_plugin_id` in `git`; the source pin in `CreationSelection`; `p-git-ssh` becomes a runtime driver file; `parseCommand`; `ValidGitOriginRef` |
| S6 | The daemon holds only `sourcedriver.Driver`; a test fails if another package imports the bundled driver or runs `git` or `ssh` on the host. | `internal/gitservice` |
| S7 | `p-source-git` over step 8's protocol package, with supervision and the conformance suite over the protocol; `path` and `sha256` replace `name`; the hook executes the driver; `HOME` and `SSH_AUTH_SOCK` passed. | `name`; `p git-hook` |

## Integration checklist

These documents change when the step lands:

- [Plugin contract](../docs/plugin-contract.md): the `source-git` capability,
  the `git.project` grant, the WASI source-Git ABI and the `p-git-ssh` asset
  role go.
- [Technology stack](../docs/technology-stack.md): "Git server" keeps Wish in
  core as P's SSH server, with Git its one service; the source-git paragraph and "read-side
  queries shell out to Git" become the bundled driver's; "no generic VCS
  provider" stays, since this is a Git driver.
- [Communication boundaries](../docs/communication-boundaries.md): the
  services run in the driver and the approval is `Approve`; core decides and
  guards, the driver performs; the origin runner is the driver's.
- [Project lifecycle](../docs/project-lifecycle.md) and
  [session lifecycle](../docs/session-lifecycle.md): repositories are created
  and deleted through the driver, deletion by incarnation; the authorities
  table names the driver; creation pins no source package; rename's clone
  edits come from the driver; the driver checks branch names.
- [Control API](../docs/control-api.md): the `git` block; `system.inspect`'s
  `source_plugin_id` and `source_sha256` give way to the driver's backend,
  version and digest.
- [Glossary](../GLOSSARY.md): a "Source driver" entry.
- [Product direction](../docs/PRODUCT.md): "first-party implementations can be
  replaced" covers Git source again.

Already aligned: [REWORK-PLAN.md](REWORK-PLAN.md) (the Source row, step 9),
[plugins.md](plugins.md) (source-git, the environment exception),
[runtime-driver.md](runtime-driver.md) (Git authority, `Build.Source`, rename's
Git edits, `runtime.driver`), [commit-images.md](commit-images.md) (the bundle
and the image's clone), [errors.md](errors.md), [wire-types.md](wire-types.md)
and [cli.md](cli.md) (`CheckNames` refusals), and [waiting.md](waiting.md).

## Options set aside

Choices the maintainer has not made; the decisions in the Summary exclude the
rest.

| Option | Why not |
|---|---|
| One driver for runtime and source | Replacing one backend would replace both, though a Kubernetes runtime and a forge are independent choices. The authorities differ: container control against repositories and origin credentials. One crash would make both kinds of calls unknown. |
| An interface at Git's command level (`Run(args)`) | Core would depend on a bare repository on disk; fails the forge test. |
| One driver calling the other, for the build's input or the session's stream | Core already holds the staging directory and the stream, so neither driver needs the other. |
| Each operation refuses invalid names itself, relying on the read each admission makes before recording | No new operation, but an origin change would contact the origin at admission to check its URL, and a value only compared, such as an expected tip, would answer `precondition_failed` instead of `invalid`. |
| For rename, the source driver edits the clone itself, through file operations core relays to the runtime driver | A file interface calling back into core, and several rename operations instead of one that only computes. The source driver would send writes whose lost outcomes only the runtime driver can settle, and a lost source driver call would leave core unsure which writes were sent. |

## How it will be checked

- **Conformance suite** (`internal/sourcedriver/conformance`), against the
  in-memory driver and the bundled driver on real Git in a temporary
  directory, from today's gitservice tests:
  - repositories: creation converges and leaves `main`; deletion refuses
    another incarnation and is safe to repeat; an unsafe storage directory
    fails `Check`;
  - heads: creation refuses an existing head at the same tip and a dangling
    symbolic ref; deletion refuses a moved tip; no effect before the gate or
    after a failing one; more than 1024 heads is `ErrLimit`; a head moved
    between two reads is an error, not an observation;
  - commits: missing, present and reachable commits; more than 256 losing
    commits is `ErrLimit`; an export is a bundle that clones to exactly the
    commit, with its history, nothing is written outside the directory, and
    an export cut off by its deadline leaves nothing;
  - a session's clone, from workspace_rename_test.go and on fixture files:
    today's refusals and diagnostics; the edits in order; no edits once the
    goal holds, and the same `Plan` on every call; a file in neither state
    refused; `done` removes only the backup, from a clone in any state; nothing
    asked for or edited outside the workspace root and its Git directory;
  - names, from the tests of `validBranch`, `ValidateRenameBranch`,
    `ValidGitOriginRef`, `ValidateOriginURL` and `validOID`: today's accepted
    and refused values of each kind; the first value at fault is named; every
    operation refuses what `CheckNames` refuses;
  - the service: a command that does not parse is refused before `open`, and
    `open` is called once before any Git runs; upload advertises only heads
    and refuses a want by ID; receive refuses a second update, another head, a
    deletion, a non-commit and a non-fast-forward; `Approve` is called once,
    after the checks and before the update, and a refusal leaves the head; a
    first push to an empty repository needs approval; input and time ceilings
    hold;
  - origin, from origin_test.go and origin_publication_test.go: advertisement
    parsing and bounds; a lightweight tag on a commit and an annotated tag
    peeling to a commit both fetch; a tag whose target is not a commit is
    observed as advertised, and `FetchOrigin` refuses it; a fetch creates no
    head and refuses a moved ref; the five relations; `created`, `advanced`, `satisfied`, `refused`, and
    `ErrUnknownOutcome` for a lost answer;
  - settling: a driver killed after the gate of a head creation or deletion
    answers `Applied` or `NotApplied` after restart, never by sending the
    effect again;
  - overlapping pushes: two receive services of one session, both approved,
    move a head A→B→C, and the driver is killed before core hears that either
    ended; after the restart `Settle` reports both services ended, both leases
    are released, neither update is sent again, and core reads the head as
    Git left it.
- **Requests:** a handler refuses an invalid branch, origin ref, creation
  source, URL or commit ID with `invalid` and its field before recording
  anything, and replays a recorded request while the driver is down.
- **Workers** on the in-memory driver: session creation (local and origin),
  session and retained rename, retained deletion, discard, delete, project
  deletion and publication, each with an
  unknown outcome after the gate; a guard waits until every service of a
  failed driver is reported ended; after a lost workspace write, a rename
  advances only when `RenameWorkspace` returns no edits and the runtime driver
  settles the write `Applied`, and writes nothing again.
- **Boundaries:** only the bundled driver runs `git` or `ssh` on the host or
  opens its storage; the bundled driver imports no SQLite or runtime driver
  code; the launch checks refuse as they do for the runtime driver; only
  `HOME` and `SSH_AUTH_SOCK` reach the driver's environment; the daemon
  refuses to start with another backend while a project exists.
- **VM scenarios** 04, 07, 08, 14, 16 to 20, 31 to 33, 43, 44 and 52 keep
  their outcomes with no source package, 19 as step 1 changed it (the bundle
  instead of the tree); 48 and 54 no longer see source-git.
  New: kill the driver during a session push after approval and during a head
  creation, then check that every lease is released, the creation is settled,
  and nothing is pushed twice.
