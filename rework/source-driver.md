# One source driver for every Git effect

Status: proposed, 2026-11-02. Part of the [interface rework plan](REWORK-PLAN.md).

## Summary

**Today** P's Git work has no single owner. `internal/gitservice` keeps a
bare repository per project under `STATE/repositories`, serves them through a
Wish SSH server, and runs `git` itself for reads, loss analysis, snapshots and
the transport. Nine command kinds go through the WASI source-git module
(plugins/bundled/source-git), which decides nothing: each broker call runs Git
with arguments core chose, and core checks the outcome. The daemon makes 79
direct calls to 16 methods of `*gitservice.Backend`, plus calls through a few
narrow interfaces, and removes a project's repository itself (`os.RemoveAll`
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

**Proposed:** a `sourcedriver.Driver` of 18 operations in P's terms, in seven
groups: host, repositories, heads, commits, the Git service, origin, settling.
The driver owns how P's repositories are stored and how Git runs on them:
layout and safety checks, objects, heads and their compare-and-swap, the Git
service behind P's endpoint with its pre-receive check, the origin transport,
and the commit export for image builds. Core keeps lifecycle state, the
journal, policy, change tags, Git authority in SQLite, the SSH server and its
keys, the origin URL and observations, and the Git edits that rename and ref
repair make in a stopped workspace. The driver ships as `p-source-git`, started
and checked like `p-runtime-incus`.

Five questions wait on the maintainer ([below](#open-questions)); the first two
confirm that the SSH server and the workspace edits stay in core.

**What users notice:** no workflow changes. The `git` block of host
configuration keeps `listen` and gets `driver: {path, sha256, config}` instead
of `activation_path` and `source_plugin_id`. `system.inspect` names the driver
instead of the source plugin, and `p plugins defaults` no longer lists
source-git. Origin access still uses the daemon account's SSH agent and
configuration.

```text
Before (after steps 0d and 0e)
internal/daemon ─┬─ gitservice ─┬─ plugin.RunSourceGit ─► git.wasm ─► broker ─► git   9 command kinds
                 │              ├─ git directly                                        reads, loss, snapshots
                 │              └─ Wish SSH ─► upload-pack, receive-pack ─► p git-hook
                 ├─ os.RemoveAll(repository)                                           project deletion
                 └─ control.Store: principals, leases, guards, unborn grants           gitservice reads it too
session ─► git.sock ─► daemon proxy ─► Wish SSH

After
internal/daemon ─┬─ sourcedriver.Driver ─► p-source-git ─► git, ssh
                 ├─ SSH server: keys, principal, lease ─► Driver.ServeGit
                 └─ control.Store: principals, leases, guards, unborn grants
session ─► git.sock ─► daemon proxy ─► SSH server
```

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
| Create only if absent: assignment, origin capture, renames, ref repair | Four gitservice methods (WASI `git.branch.create`); two read SQLite | `CreateBranch`; the SQLite checks move to core |
| Delete only at the expected tip | `DeleteAssignedBranchExact` (WASI) | `DeleteBranch` |
| Check a new branch name | `git check-ref-format` in gitservice | Core's name rule |
| **Commits** | | |
| Resolve a creation source; is a commit present and reachable from a head | `CaptureLocalSource`, `ObserveSource` (WASI), `PCommitPresent` | `InspectBranch`, `InspectCommit`; the source rules stay in core |
| Every head with its tip; the commits a deletion removes (`p_commits`) | `AssignedBranchSnapshot`, `BranchRemovalLoss`, `ProjectRemovalLoss` | `Branches`, `LosingCommits` |
| The commit for the image builder | `CapturePinnedCommit` → `SourceSnapshot` → the Incus builder | `Export`, which core hands to `BuildImage` |
| **P's Git endpoint** | | |
| Transport limits, 1 GiB and 10 minutes | WASI `git.transport.plan`, which may only tighten them | Core passes them as ceilings |
| `upload-pack` and `receive-pack` with today's restrictions | `handle` in gitservice | `ServeGit` |
| Pre-receive check (one update, of the allowed head, to a commit, fast-forward), then approval, which consumes the unborn `main` grant | `p git-hook`, hook.go | The check: driver. Approval and grant: core, as `Approve` |
| **Origin** | | |
| URL rule: SSH forms only | `ValidateOriginURL` in gitservice | Core |
| Observe, fetch, compare, push | `OriginScope`, three of them through WASI | `ObserveOrigin`, `FetchOrigin`, `ComparePublication`, `Publish` |
| **Gone before the driver** | | |
| Workspace inspection and loss analysis; `RetainedLossRefs`; origin checks before deletions (`CompareOriginRemoval`) | gitservice, runtimeincus helpers | **Go** in 0d and 0e. `RetainedLossRefs` is assumed from [plan-shape.md](plan-shape.md#sessionrefrepair-refrepairpreview); 0e has not landed |

## Who does what

The driver owns how P's repositories exist and how Git runs on them. Core
owns what P decides: which head a session may update, when a head is created
or deleted and on what evidence, the guards that keep sessions out meanwhile,
and every durable record of intent. The driver never opens P's SQLite
database; what it needs arrives as an argument or a gate.

| Core | Source driver | Runtime driver |
|---|---|---|
| Lifecycle, journal, policy, change tags, loss items | Repository layout and safety | Runtimes, images, the guest |
| Git authority in SQLite; the source and name rules | Heads and their compare-and-swap; reachability and loss queries | — |
| `git.sock`, the SSH server, its host key, the host client key, session keys | Upload and receive services; the pre-receive check | Delivering `git.sock`, the session key and the guest's Git client files |
| Origin URL, observations, publication records, the origin lock | The origin transport | — |
| The staging directory of a build | `Export` into it | `BuildImage` from it |
| Git edits in a stopped workspace | — | The workspace file operations |

The two drivers never talk to each other. They meet in two places, and core
sits between them in both:

- **The builder's input.** Core creates a private staging directory, asks the
  source driver to `Export` the commit into it, passes it to `BuildImage` as
  `Build.Source`, and removes it afterwards. What the export holds is
  [open question 3](#open-questions).
- **The session's endpoint.** Core creates `git.sock` and serves it; the
  runtime driver makes it reachable in the guest, with the key and the SSH
  client files; the source driver serves Git on the stream core authenticated.

```text
guest ssh ─► runtime-kit git-stream ─► /run/p/git.sock ─► daemon proxy ─► SSH server ─────────► Driver.ServeGit
             runtime driver's files    runtime driver      core            core: key, lease      upload-pack, receive-pack,
                                       delivers it                         ▲                     pre-receive check
                                                                           └── Approve(update) ◄─┘
```

## The driver interface

Three ideas carry it:

- **P's names, not Git's layout.** Core names a project, a branch, a commit
  and an origin URL; the driver decides where repositories live and how Git
  runs. Core never sees a repository path.
- **Gates**, as in [runtime-driver.md](runtime-driver.md#the-driver-interface):
  a mutation calls a core function once, after its checks and right before its
  first effect, and core records its durable intent there. `ServeGit`'s gate is
  `Approve`, called once per session push.
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
	Describe() Description
	Check(ctx context.Context) error // prove the storage boundary and the Git tools

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
	Export(ctx context.Context, project string, id OID, dir string) (Export, error)    // into a directory core made

	// The Git service behind P's endpoint, on a stream core authenticated
	ServeGit(ctx context.Context, s Service, stream Stream, approve Approve) error

	// Origin
	ObserveOrigin(ctx context.Context, url string) ([]OriginRef, error)
	FetchOrigin(ctx context.Context, project, url string, ref OriginRef) (OID, error) // objects only, no head
	ComparePublication(ctx context.Context, project, url string, source OID, dest *OriginRef) (Relation, error)
	Publish(ctx context.Context, project string, p Publication, g Gate) (PublishStatus, error)

	// What became of a call that returned ErrUnknownOutcome, or of a lost ServeGit
	Settle(ctx context.Context, e Effect) (Settlement, error)
}

type OID string // a full object ID: 40 hexadecimal digits, or 64

type Description struct {
	Backend string // the storage's identity, such as "git-files" (open question 4)
	Version string
}

type Repository struct {
	Project     string
	Exists      bool
	Incarnation string // opaque; new when the repository is made again
}

type Head struct {
	Name string // without refs/heads/
	Tip  OID
}

type Commit struct {
	Present   bool // a commit object in P's repository
	Reachable bool // from some head
}

type Loss struct {
	Heads   []Head // the observation the answer belongs to
	Commits []OID  // reachable from the removed heads and from no other; at most 256
}

type Export struct {
	Commit, Tree OID
	Entries      int
	Bytes        int64
}

type Service struct {
	ID       string        // core's name for this service, for Settle
	Project  string
	Kind     ServiceKind   // upload or receive
	Branch   string        // receive: the one head it may change
	MaxInput int64         // core's ceiling, 1 GiB
	Deadline time.Duration // core's ceiling, 10 minutes
}

type Stream interface { // the SSH channel
	io.ReadWriter
	CloseWrite() error
	Stderr() io.Writer
}

type Update struct {
	Branch   string
	Old, New OID // Old empty: the push creates the branch
}

type Approve func(Update) error // core's gate for a session's one head update

type OriginRef struct {
	Ref    string // refs/heads/* or refs/tags/*
	OID    OID    // what the ref names: a commit, or an annotated tag
	Commit OID    // the commit it peels to
}

type Relation int // absent, equal, destination-contains, fast-forward, divergent

type Publication struct {
	URL          string
	SourceBranch string   // must still name Source when the push starts
	Source       OID
	Destination  string   // refs/heads/*
	Relation     Relation // absent or fast-forward
}

type PublishStatus int // created, advanced, satisfied, refused

type Effect struct {
	Kind     EffectKind // create-branch, delete-branch, or service
	Project  string
	Branch   string
	Old, New OID    // a head effect: the state before, and the state intended
	Service  string // a service: its ID; Applied means it can no longer write
}

type Settlement int // Applied, NotApplied, Unknown

type Gate func() error

var (
	ErrUnknownOutcome = errors.New("the effect may have happened; settle it")
	ErrConflict       = errors.New("a head or object is not in the state the call expects")
	ErrNotFound       = errors.New("no such repository")
	ErrLimit          = errors.New("a bound was exceeded; nothing is claimed")
	ErrUnavailable    = errors.New("the backend or the origin cannot be reached; nothing is claimed")
)
```

### What each operation guarantees

| Operation | The driver must |
|---|---|
| All | Act only on P's repositories in its configured storage. Read native state fresh on every call; after an error, claim nothing. Never run code or configuration a repository can change: hooks, filters, repository config, submodules, lazy fetch. Never prune an object: P has no garbage collection, and a pending creation relies on an origin commit that no head holds yet. Accept every branch name P's rule allows and every URL P's origin rule allows. |
| `Check` | Prove that its storage is private to P's account and that its Git tools are present, at start and before every mutation; refuse rather than proceed. |
| `CreateRepository` | Converge, so a repeated call is safe: make the repository complete or not at all, keep an existing one, and leave its default branch `main`, as `git.project.ensure` does. |
| `DeleteRepository` | Act only on the given incarnation; afterwards the project's repository is absent. Safe to call again at the same incarnation. |
| `Branches` | Return every head as one observation, in byte order, each tip a commit present in P's repository, or an error; `ErrLimit` above 1024 heads. The bundled driver reads twice and compares, as today's loss code does. |
| `InspectBranch` | Report the exact head, absent, or `ErrConflict` when a symbolic ref holds the name. |
| `CreateBranch` | Create the head only if nothing holds the name, a dangling symbolic ref included, and only at a commit present in P's repository; an existing head is `ErrConflict` even at the same tip. Call the gate before the effect. |
| `DeleteBranch` | Delete the head only while it names exactly `tip`; call the gate before the effect; read absence back. |
| `InspectCommit` | Report whether the object is a commit in P's repository, and whether some head reaches it. |
| `LosingCommits` | Return the commits reachable from the removed heads and from no other, with the head observation used; `ErrLimit` above 256. |
| `Export` | Write one commit's content into the given directory and nothing outside it. In today's tree form ([open question 3](#open-questions)): stay within today's bounds (20,000 entries, 256 MiB, 4 KiB paths and link targets); apply no filters or export rules; refuse submodules, unsafe names and links that leave the tree. |
| `ServeGit` | Upload: advertise only heads and send only objects they reach. Receive: accept exactly one update, of `refs/heads/<Branch>`, to a commit, a fast-forward or, from an empty old value, a creation; refuse deletions; keep pushed objects apart until checked; call `approve` once, after the checks and before the update; move the head by compare-and-swap from the old value. Bound input and time by the ceilings. Return only when nothing the service started can still change a head. |
| `ObserveOrigin` | Contact the origin afresh; return at most 1024 heads and tags, with annotated tags peeled; an empty list is a successful answer. |
| `FetchOrigin` | Bring in the commit the observed ref names, creating no head; `ErrConflict` if the origin ref moved since the observation. Safe to repeat. |
| `ComparePublication` | Fetch only the destination's objects, create no ref, and report the relation from Git ancestry. |
| `Publish` | Push only for an absent or fast-forward relation, the exact commit to one head, with no force and no tags, and only while `SourceBranch` still names it. Call the gate before the push. Return `refused` only on the origin's definite rejection, and `ErrUnknownOutcome` when the answer is lost after the push started. |
| `Settle` | For a head effect: `Applied` only when the intended state holds exactly and nothing the driver admitted can still change it; `NotApplied` only when the prior state holds and nothing can still change it; otherwise `Unknown`. For a service: `Applied` once nothing it started can still change a head, `Unknown` until then, never `NotApplied`. Answers hold after a restart. |

### What absent proves

A driver that cannot tell, for lack of access or reach, returns an error,
never an absence.

| Answer | Proves |
|---|---|
| `InspectRepository`: not `Exists` | Nothing is at the project's address, complete or partial |
| `InspectBranch`: absent | Neither a head nor a symbolic ref holds the name |
| `InspectCommit`: not `Present` | The object is not a commit in P's repository. It says nothing about the origin or a workspace; ref repair refuses with `p_object_missing` |
| `ObserveOrigin`: no such ref | The origin did not advertise it in this observation |
| `LosingCommits`: none | Every commit of the removed heads stays reachable from a remaining head of the observation |

### The Git service and ref guards

Guards stay core's. `AcquireGitLease` refuses a receive service for a guarded
branch before `ServeGit` is called, and every service holds Git authority's
read lock until it is over, so a guard can be set only while no service can
write. The driver adds one rule: a session's head changes only inside
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

### Settling uncertain effects

The runtime driver's rules apply to P's repositories:

1. **A guard until settled.** The gate's record stays until the effect settles:
   creation's `RefCASIntent`; rename's `new-ref-create-issued` and
   `old-ref-delete-issued`; retained rename's and retained delete's issued
   phases; ref repair's `ref-create-issued`; session deletion's
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

The mechanism is the runtime driver's
([runtime-driver.md](runtime-driver.md#how-a-driver-is-replaced)), reused:

- host configuration `git: {listen, driver: {path, sha256, config}}`; only
  trusted host configuration selects it;
- before every start, the launch checks of [plugins.md](plugins.md#trust),
  then the digest; the daemon starts the file it checked;
- one long-lived process, JSON-RPC 2.0 over a Unix socket pair in both
  directions, since gates and `Approve` are calls from the driver; a hello that
  agrees on the version and returns `Describe`'s answer; each `ServeGit`
  stream on its own connection, as each terminal is;
- a crash turns calls in flight into unknown outcomes; the daemon kills the
  driver's process group, starts it again after fresh checks, and settles;
- a failed first start refuses daemon startup, as an invalid Git selection
  does today.

**What the driver holds.** Read and write access to every project's
repository, which is all the source P keeps; and the daemon account's origin
credentials: the SSH agent, `~/.ssh` configuration and known hosts, with
network access to origins. That is not new authority: gitservice holds it
inside the daemon today, and a WASI module never did. To reach origins the
driver needs `HOME` and `SSH_AUTH_SOCK`, which today's runner passes through
(`originEnv` in origin.go), so the daemon passes exactly those two variables;
plugins.md starts executables with no inherited environment. With no `PATH`,
the bundled driver's package pins `git` and `ssh`.

P never hands the driver its SQLite database, session keys, the SSH server's
host key or the Incus socket. Running as P's account with no sandbox, it could
open them ([plugins.md](plugins.md#trust)); core trusts its answers about P's
repositories, which is what a driver is for.

**The hook.** The pre-receive script in the driver's storage executes the
driver, not `p git-hook`, and talks to the driver over a per-push socket, as
it talks to the daemon today.

## Migration

Each step is a commit that leaves `go vet`, the unit tests, the TUI tests,
`nix flake check`, `nix build` and the VM scenarios green on a fresh install.
The steps assume 0d, 0e and step 8 have landed ([open question 5](#open-questions)).

| Step | What | Removed |
|---|---|---|
| S1 | `internal/sourcedriver`: the interface, an in-memory driver and a conformance suite; the bundled driver in process (`internal/sourcedriver/git`), built from gitservice's broker bodies and its direct Git code. Host configuration gets `git.driver: {name, config}`. | — |
| S2 | Repositories, heads and commits go through the driver: `EnsureBare`, `InspectBranchRef`, `CaptureLocalSource`, the four create methods, `DeleteAssignedBranchExact`, `ListRefsPage` behind `control.GitReader`, `PCommitPresent`, the loss methods, and project deletion's `os.RemoveAll`. The SQLite checks of `CreateCapturedOriginBranch` and `CreateRestoredAssignedRefExact` move to the daemon; `ValidateRenameBranch` becomes P's name rule. | Five command kinds; `RepositoryPath` |
| S3 | Origin: core keeps the per-project lock and calls `ObserveOrigin`, `FetchOrigin`, `ComparePublication` and `Publish`; control's origin and publication interfaces adapt to them. | `OriginScope`; three command kinds |
| S4 | `Export` replaces `CapturePinnedCommit`; core owns the staging directory and hands it to `BuildImage`. | `SourceSnapshot`, `CaptureCommittedSource` |
| S5 | The SSH server and its stream authorization leave gitservice for a core package that takes the lease and calls `ServeGit`; the hook approval becomes `Approve`. | `git.transport.plan`; the source-git package, `plugin.RunSourceGit` and the Git brokers; `activation_path` and `source_plugin_id` in `git`; the source pin in `CreationSelection`; `p-git-ssh` becomes a runtime driver file |
| S6 | The daemon holds only `sourcedriver.Driver`; a test fails if another package imports the bundled driver or runs `git` or `ssh` on the host. | `internal/gitservice` |
| S7 | `p-source-git` over step 8's protocol package, with supervision and the conformance suite over the protocol; `path` and `sha256` replace `name`; the hook executes the driver; `HOME` and `SSH_AUTH_SOCK` passed. | `name`; `p git-hook` |

Placement is [open question 5](#open-questions): recommended as the first
part of step 9.

## Integration checklist

These documents change when the step lands:

- [Plugin contract](../docs/plugin-contract.md): the `source-git` capability,
  the `git.project` grant, the WASI source-Git ABI and the `p-git-ssh` asset
  role go.
- [Technology stack](../docs/technology-stack.md): "Git server" keeps Wish in
  core around the driver's services; the source-git paragraph and "read-side
  queries shell out to Git" become the bundled driver's; "no generic VCS
  provider" stays, since this is a Git driver.
- [Communication boundaries](../docs/communication-boundaries.md): the
  services run in the driver and the approval is `Approve`; core decides and
  guards, the driver performs; the origin runner is the driver's.
- [Project lifecycle](../docs/project-lifecycle.md) and
  [session lifecycle](../docs/session-lifecycle.md): repositories are created
  and deleted through the driver, deletion by incarnation; the authorities
  table names the driver; creation pins no source package.
- [Control API](../docs/control-api.md): the `git` block; `system.inspect`'s
  `source_plugin_id` and `source_sha256` give way to the driver's backend,
  version and digest.
- [Glossary](../GLOSSARY.md): a "Source driver" entry.
- [Product direction](../docs/PRODUCT.md): "first-party implementations can be
  replaced" covers Git source again.

Already aligned: [REWORK-PLAN.md](REWORK-PLAN.md) (the Source row, step 9),
[plugins.md](plugins.md) (source-git, the environment exception),
[runtime-driver.md](runtime-driver.md) (Git authority, `Build.Source`,
`runtime.driver`), [commit-images.md](commit-images.md) (the image's clone),
[errors.md](errors.md) and [waiting.md](waiting.md).

## Options set aside

| Option | Why not |
|---|---|
| By the maintainer's decisions | Folding source-git into core (no replaceable owner); keeping a WASI source plugin (it decides nothing, and a replacement could change only page sizes and limits); checking the origin before deletions. |
| One driver for runtime and source | Replacing one backend would replace both, though a Kubernetes runtime and a forge are independent choices. The authorities differ: container control against repositories and origin credentials. One crash would make both kinds of calls unknown. |
| An interface at Git's command level (`Run(args)`) | Core would depend on a bare repository on disk; fails the forge test. |
| A Git library linked into core | Not replaceable without rebuilding P, against the maintainer's decision. |
| A generic version-control provider | Git's model is P's interchange ([technology stack](../docs/technology-stack.md)). |
| One driver calling the other, for the build's input or the session's stream | Core already holds the staging directory and the stream, so neither driver needs the other. |
| The driver serves SSH | Open question 1. |

## Open questions

1. **Does the SSH server stay in core?**
   - (a) Core keeps it and hands `ServeGit` an authenticated stream.
     *Recommended:* P's identities (the pinned host key, the host client key,
     session keys and principals) do not change with the storage, the lease
     and guards are SQLite's, and a forge driver would run no SSH at all. The
     driver still owns every byte that reaches a repository.
   - (b) The driver serves SSH and asks core to authenticate each key and
     authorize each stream. Every driver then reimplements P's SSH
     restrictions, and each connection crosses the process boundary twice
     before any Git runs.
2. **Do the workspace Git edits stay in core?** Rename rewrites `.git/HEAD`,
   `.git/config` and head files in the session's clone, and ref repair reads
   its branch tip, through the runtime driver's operations on a stopped
   session.
   - (a) Core, as runtime-driver.md says. *Recommended:* the clone belongs to
     the session and is made by the guest's Git, so it looks the same whatever
     stores P's repositories. A forge driver would carry on-disk ref editing it
     otherwise never needs, and every file operation would pass from one
     driver through core to the other.
   - (b) The source driver, through core's relay of the workspace operations;
     then no Git-format code stays in core.
3. **What does `Export` give the image build?** commit-images.md puts in the
   image "a clone of P's repository at the commit, detached"; runtime-driver.md
   gives the build "the commit's files", which today's snapshot holds. A tree of
   files cannot become a clone with the commit's identity.
   - (a) A Git bundle of the commit, which the builder clones into
     `/workspace`. *Recommended:* one standard artifact any Git engine can
     produce, bounded like today's snapshot, and checked out inside the
     isolated builder instead of on the host. A bundle carries the commit's
     history; whether the image keeps all of it is commit-images.md's
     question. snapshot.go's host-side tree checks then go.
   - (b) Today's tree, with `workspace-ready` fetching the commit over P's
     endpoint. Nothing changes here, but commit-images.md's "no clone" at
     session start no longer holds.
4. **What pins the driver to an instance?** A driver of another backend would
   find none of the instance's repositories.
   - (a) Core records `Describe().Backend` at the first start and refuses to
     start with another backend while any project exists; a new digest of the
     same backend is an upgrade. *Recommended,* like today's pinned Git server
     key (`git_server_fingerprint`).
   - (b) No pin, since installs are fresh: a changed backend fails later,
     project by project.

   Under either, sessions no longer pin a source package at creation;
   today's pin refuses a changed package with "repair required".
5. **Where does it fit in the plan?**
   - (a) Step 9, first: S1 to S7, then plugins.md's work. *Recommended:* step 9
     already removes the source-git module, S7 reuses step 8's protocol, S5
     moves `p-git-ssh` into the runtime driver, and wazero can leave once both
     are done.
   - (b) A new step between 8 and 9: the same order, and every "step 9" in the
     other documents renumbered.
   - (c) S1 to S6 before step 8, since they do not need the protocol: earlier,
     but S4 and S5 would convert code that step 8 then moves again.

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
    commits is `ErrLimit`; the export bounds, submodules, unsafe names,
    case-folded collisions and escaping links, from snapshot_test.go;
  - the service: upload advertises only heads and refuses a want by ID;
    receive refuses a second update, another head, a deletion, a non-commit
    and a non-fast-forward; `Approve` is called once, after the checks and
    before the update, and a refusal leaves the head; a first push to an empty
    repository needs approval; input and time ceilings hold;
  - origin, from origin_test.go and origin_publication_test.go: advertisement
    parsing and bounds; a fetch creates no head and refuses a moved ref; the
    five relations; `created`, `advanced`, `satisfied`, `refused`, and
    `ErrUnknownOutcome` for a lost answer;
  - settling: a driver killed after the gate of a head creation or deletion
    answers `Applied` or `NotApplied` after restart, never by sending the
    effect again;
  - overlapping pushes: two receive services of one session, both approved,
    move a head A→B→C, and the driver is killed before core hears that either
    ended; after the restart `Settle` reports both services ended, both leases
    are released, neither update is sent again, and core reads the head as
    Git left it.
- **Workers** on the in-memory driver: session creation (local and origin),
  session and retained rename, retained deletion, discard, delete, ref repair
  (`p_object_missing` included), project deletion and publication, each with an
  unknown outcome after the gate; a guard waits until every service of a
  failed driver is reported ended.
- **Boundaries:** only the bundled driver runs `git` or `ssh` on the host or
  opens its storage; the bundled driver imports no SQLite code; the launch
  checks refuse as they do for the runtime driver; only `HOME` and
  `SSH_AUTH_SOCK` reach the driver's environment.
- **VM scenarios** 04, 07, 08, 14, 16 to 20, 31 to 33, 39, 43, 44 and 52 keep
  their outcomes with no source package, except that 19 (committed snapshot)
  follows open question 3; 48 and 54 no longer see source-git.
  New: kill the driver during a session push after approval and during a head
  creation, then check that every lease is released, the creation is settled,
  and nothing is pushed twice.
