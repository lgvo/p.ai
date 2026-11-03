// Package sourcedriver is the contract between P's core and the code that
// owns P's repositories: how they are stored, and how Git runs on them and
// against an origin. Core names P's concepts (a project, a branch, a commit,
// an origin URL); a driver decides where repositories live and how Git runs.
// Core keeps lifecycle state, the journal, policy, change tags, Git
// authority, the SSH server and its keys, and the origin URL and
// observations. A driver never opens P's SQLite database and never reaches a
// runtime; what it needs arrives as an argument or a gate.
//
// Three ideas carry the contract:
//
//   - P's names, not Git's layout. Core never sees a repository path.
//   - Gates. A lifecycle mutation calls a core function once, after its
//     checks and right before its first effect, and core records its durable
//     intent there. If the gate fails, nothing happened; if the call fails
//     after it, the outcome is unknown. ServeGit's Open and Approve are core's
//     authorizations of a service and of a session push; they record no
//     intent, except that Approve consumes the unborn main grant.
//   - Exact identity. Every change names the state it expects: a head is
//     created only if absent and deleted only at its expected tip, a
//     repository is deleted only at its incarnation, a push to the origin
//     only creates or fast-forwards.
package sourcedriver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// Driver is the only code that creates, changes or reads P's repositories,
// and the only code that runs Git against them or against an origin.
//
// Every implementation acts only on P's repositories in its configured
// storage. It reads native state fresh on every call and claims nothing after
// an error. It never runs code or configuration a repository can change:
// hooks, filters, repository config, submodules, lazy fetch. It never prunes
// an object: P has no garbage collection, and a pending creation relies on an
// origin commit that no head holds yet. Every operation accepts every value
// CheckNames accepts and refuses others with ErrInvalid, and returns only
// names CheckNames accepts. A driver that cannot tell, for lack of access or
// reach, returns an error, never an absence.
type Driver interface {
	// Describe says which backend the driver is, which core pins at its first
	// start, and which SSH commands ServeGit serves.
	Describe() Description
	// Check proves that the driver's storage is private to P's account and
	// that its Git tools are present. Core calls it at start; every mutation
	// makes the same check first and refuses rather than proceed.
	Check(ctx context.Context) error

	// CheckNames checks values whose syntax is the backend's: branch and ref
	// names, origin URLs and commit IDs. It decides from each value and its
	// kind only, offline, the same way every time, and returns each value as
	// core uses it (see NameKind). The first value at fault is named by an
	// *InvalidName error, which matches ErrInvalid.
	CheckNames(ctx context.Context, names []Name) ([]Name, error)

	// InspectRepository reads the project's repository. Not Exists proves
	// that nothing is at the project's address, complete or partial.
	InspectRepository(ctx context.Context, project string) (Repository, error)
	// CreateRepository converges, so a repeated call is safe: it makes the
	// repository complete or not at all, keeps an existing one, and leaves its
	// default branch main.
	CreateRepository(ctx context.Context, project string) (Repository, error)
	// DeleteRepository deletes r, a repository at its incarnation, and only
	// that incarnation: another one is ErrConflict. It calls g right before
	// its first effect, and not at all when the repository is already absent,
	// which succeeds. Afterwards nothing is at the project's address.
	DeleteRepository(ctx context.Context, r Repository, g Gate) error

	// Branches returns every head of the project's repository as one
	// observation, in byte order of Name, each tip a commit present in the
	// repository, or an error; ErrLimit above MaxHeads heads.
	Branches(ctx context.Context, project string) ([]Head, error)
	// InspectBranch reads the head refs/heads/<name> exactly: its tip, or
	// false when neither a head nor a symbolic ref holds the name. A symbolic
	// ref there, dangling or not, is ErrConflict.
	InspectBranch(ctx context.Context, project, name string) (OID, bool, error)
	// CreateBranch creates the head only if nothing holds the name, a
	// dangling symbolic ref included, and only at a commit present in the
	// repository; an existing head is ErrConflict even at the same tip. It
	// calls g right before the effect.
	CreateBranch(ctx context.Context, project, name string, at OID, g Gate) error
	// DeleteBranch deletes the head only while it names exactly tip; a head
	// elsewhere, or none, is ErrConflict. It calls g right before the effect
	// and reads the head's absence back.
	DeleteBranch(ctx context.Context, project, name string, tip OID, g Gate) error

	// InspectCommit reports whether id is a commit in the project's
	// repository and whether some head reaches it. Not Present says nothing
	// about the origin or a workspace.
	InspectCommit(ctx context.Context, project string, id OID) (Commit, error)
	// LosingCommits returns the commits reachable from the removed heads and
	// from no other, with the head observation it used; nil removes every
	// head, and a removed name no head holds removes nothing. ErrLimit above
	// MaxLosingCommits commits.
	LosingCommits(ctx context.Context, project string, removed []string) (Loss, error)
	// Export writes a Git bundle of the commit and every commit it reaches,
	// as a session's clone would fetch them, into dir, a private directory
	// core made, and nothing outside it, within ExportDeadline. It leaves no
	// partial bundle. The commit must be reachable from a head.
	Export(ctx context.Context, project string, id OID, dir string) (Export, error)

	// RenameWorkspace computes the Git edits that rename a session's clone,
	// from the files given only; it has no effect. Core reads the files it
	// asks for with the runtime driver and writes the edits it returns.
	RenameWorkspace(ctx context.Context, r WorkspaceRename, files []WorkspaceFile) (WorkspaceEdits, error)

	// ServeGit serves one SSH exec command on a channel core authenticated.
	// It parses command, refuses one it cannot parse with ErrInvalid before
	// calling open, and calls open once before any Git runs. It returns only
	// when nothing the service started can still change a head.
	ServeGit(ctx context.Context, command string, stream Stream, open Open) error

	// ObserveOrigin contacts the origin afresh and returns at most
	// MaxOriginRefs heads and tags with their advertised IDs and peeled
	// targets, in byte order of Ref, unverified: it fetches nothing and checks
	// no object type. An empty list is a successful answer.
	ObserveOrigin(ctx context.Context, url string) ([]OriginRef, error)
	// FetchOrigin brings the observed ref's objects into the project's
	// repository, creating no head, and returns ref.Target, which it checks is
	// a commit: when OID equals Target, OID must be a commit; when they
	// differ, OID must be an annotated tag that peels to Target. A ref that
	// moved since the observation, or whose objects are not as observed, is
	// ErrConflict. Safe to repeat.
	FetchOrigin(ctx context.Context, project, url string, ref OriginRef) (OID, error)
	// ComparePublication reports how the destination relates to source, a
	// commit in the project's repository, from Git ancestry; another source
	// is ErrInvalid. dest is the destination as ObserveOrigin advertised it,
	// a head, or nil when the origin has none; a dest whose OID is not its
	// Target is ErrConflict. Equal IDs answer without contacting the origin;
	// otherwise it fetches only the destination's objects, as FetchOrigin
	// does, and creates no ref.
	ComparePublication(ctx context.Context, project, url string, source OID, dest *OriginRef) (Relation, error)
	// Publish pushes p.Commit to the origin's head p.Destination, only for an
	// absent or fast-forward relation (another is ErrInvalid), with no force
	// and no tags, and only while the project's head p.Branch still names
	// p.Commit (otherwise ErrConflict). It calls g right before the push. It
	// answers from the origin's answer: Refused only on its definite
	// rejection, and ErrUnknownOutcome when the answer is lost or does not
	// follow the relation after the push started. A push that could not
	// start is a plain error: nothing reached the origin.
	Publish(ctx context.Context, project string, p Publication, g Gate) (PublishStatus, error)

	// Settle says what became of an effect whose call returned
	// ErrUnknownOutcome, or of a service whose ServeGit call was lost. It uses
	// native state only, so its answer holds after a restart.
	Settle(ctx context.Context, e Effect) (Settlement, error)
}

// Description is what Describe reports.
type Description struct {
	// Backend names where the driver keeps P's repositories. Core pins it at
	// its first start: a driver of another backend would find none of the
	// instance's repositories.
	Backend string
	// Version is the driver's own version, for display.
	Version string
	// Commands are the first words of the SSH exec commands ServeGit serves.
	// Core's SSH server routes a command whose first word is one of them to
	// this driver, and holds no Git command word itself.
	Commands []string
}

// The bounds of the contract.
const (
	// MaxHeads bounds Branches.
	MaxHeads = 1024
	// MaxLosingCommits bounds LosingCommits.
	MaxLosingCommits = 256
	// MaxOriginRefs bounds ObserveOrigin.
	MaxOriginRefs = 1024
	// ExportDeadline bounds Export: the ceiling of P's Git service.
	ExportDeadline = 10 * time.Minute
	// MaxPlan bounds WorkspaceEdits.Plan.
	MaxPlan = 256
)

// OID is a commit ID or another object ID, as the backend writes it. Core
// only stores and compares it.
type OID string

// NameKind says which of P's fields a Name is, and so which rule checks it.
type NameKind string

const (
	// NameBranch is a P branch: the head refs/heads/<name> a session works on.
	NameBranch NameKind = "branch"
	// NameRenameBranch is a branch a rename creates, or a retained branch a
	// change names; its rule is stricter than NameBranch's.
	NameRenameBranch NameKind = "rename-branch"
	// NameOriginRef is a full ref of the origin, a head or a tag.
	NameOriginRef NameKind = "origin-ref"
	// NameCreationSource is the source a new branch starts from: a head ref
	// of P's repository or a commit ID. CheckNames returns it as a
	// NameBranch, with the branch's name, or as a NameCommit, so core picks
	// InspectBranch or InspectCommit.
	NameCreationSource NameKind = "creation-source"
	// NameDestination is the origin head a publication updates, as a full
	// ref. CheckNames returns it as a NameDestination whose Value is the
	// origin branch's name, which Publication.Destination takes.
	NameDestination NameKind = "publication-destination"
	// NameOriginURL is an origin's URL.
	NameOriginURL NameKind = "origin-url"
	// NameCommit is a commit ID.
	NameCommit NameKind = "commit"
)

// Name is one value of a field whose syntax the backend decides.
type Name struct {
	Kind  NameKind
	Value string
}

// InvalidName is CheckNames' refusal: Index is the position in its input of
// the first value at fault.
type InvalidName struct {
	Index int
	Name  Name
}

func (e *InvalidName) Error() string {
	return fmt.Sprintf("%s value %d is invalid", e.Name.Kind, e.Index)
}

func (e *InvalidName) Is(target error) bool { return target == ErrInvalid }

// Repository is a project's repository as the driver observed it.
type Repository struct {
	Project string
	Exists  bool
	// Incarnation is opaque and new whenever the repository is made again.
	Incarnation string
}

// Head is one head of a repository: refs/heads/<Name> at Tip.
type Head struct {
	Name string
	Tip  OID
}

// Commit is what the driver knows of an object ID.
type Commit struct {
	Present   bool // a commit object in P's repository
	Reachable bool // from some head
}

// Loss is what removing heads would lose.
type Loss struct {
	// Heads is the observation the answer used: every head, in byte order of
	// Name, the removed ones included.
	Heads []Head
	// Commits are reachable from the removed heads and from no remaining
	// head of the observation.
	Commits []OID
}

// Export is one exported commit: the bundle file at Bundle holds Commit and
// every commit it reaches, with Ref, its only ref, naming Commit.
type Export struct {
	Bundle string
	Ref    string
	Commit OID
}

// Goal is the state of a session's clone a rename asks RenameWorkspace for.
type Goal string

const (
	// GoalPrepared: the clone keeps a backup of what the rename changes,
	// written before P's new head is created. Only the backup is edited.
	GoalPrepared Goal = "prepared"
	// GoalRenamed: the clone is on the new branch.
	GoalRenamed Goal = "renamed"
	// GoalDone: the backup is gone, whatever the rest of the clone holds.
	GoalDone Goal = "done"
)

// WorkspaceRename is one call of a rename of a session's clone.
type WorkspaceRename struct {
	// ID is core's rename operation; it names the backup the clone keeps
	// until the rename is done.
	ID       string
	Old, New string // the clone's branch before and after
	Goal     Goal
	// Plan is empty, or the Plan an earlier call of this rename returned.
	Plan string
	// User is the session user's UID and GID, who owns the clone.
	User int
}

// WorkspaceFile is one path of the clone, relative to the workspace root, as
// the runtime driver read it or as an edit writes it.
type WorkspaceFile struct {
	Path string
	File File
}

// FileType is a workspace path's type.
type FileType string

const (
	FileAbsent    FileType = "absent"
	FileRegular   FileType = "file"
	FileDirectory FileType = "directory"
	FileSymlink   FileType = "symlink"
	FileOther     FileType = "other"
)

// File is one workspace path: its type, owner and permission bits, and for a
// regular file its exact bytes.
type File struct {
	Type FileType
	UID  int
	GID  int
	Mode uint32
	Data []byte
}

// WorkspaceEdits is RenameWorkspace's answer: either the files to read, or
// the edits that reach the goal.
type WorkspaceEdits struct {
	// Read names the paths to read, each with its size bound, before
	// calling again with them; when it is set, nothing else is.
	Read map[string]int64
	// Edits are the edits in order: a regular file to write, or an absent
	// File to remove. None means the goal holds.
	Edits []WorkspaceFile
	// Plan is opaque to core and the same on every call of one rename, at
	// most MaxPlan bytes. It is set whenever Read is not, except toward
	// GoalDone, where it is the Plan passed, which may be empty: removing
	// the backup needs none, and the backup that fixes it may be gone.
	Plan string
}

// WorkspaceMismatch is RenameWorkspace's refusal of a clone whose branch or
// upstream is not the one the rename names. It holds only validated names or
// fixed markers, never configuration contents, URLs or credentials.
type WorkspaceMismatch struct {
	Field, Expected, Actual string
}

func (e *WorkspaceMismatch) Error() string {
	return fmt.Sprintf("workspace %s mismatch: expected %q, actual %q", e.Field, e.Expected, e.Actual)
}

// Stream is the SSH channel of one exec command.
type Stream struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// ServiceKind is which Git service a command asks for.
type ServiceKind string

const (
	ServiceUpload  ServiceKind = "upload"
	ServiceReceive ServiceKind = "receive"
)

// Open is core's authorization of one service, called once before any Git
// runs: it checks the key's principal for the project and takes the lease,
// which core releases when ServeGit returns, or once Settle reports a lost
// service ended.
type Open func(kind ServiceKind, project string) (Service, Approve, error)

// Service is one Git service core authorized.
type Service struct {
	ID       string // core's name for it, for Settle
	Project  string
	Kind     ServiceKind
	Branch   string        // receive: the one head it may change
	MaxInput int64         // core's ceiling on the bytes the client sends
	Deadline time.Duration // core's ceiling on the service's duration
}

// Approve is core's authorization of a session's one head update, which the
// driver calls once, after its checks and before the update. Old is empty
// when the push creates the branch.
type Approve func(branch string, old, new OID) error

// OriginRef is one ref an origin advertised.
type OriginRef struct {
	Ref    string // refs/heads/* or refs/tags/*
	OID    OID    // the advertised object ID
	Target OID    // a tag's advertised peeled target, otherwise OID
}

// Relation is how a publication's destination relates to its source.
type Relation string

const (
	RelationAbsent              Relation = "absent"
	RelationEqual               Relation = "equal"
	RelationDestinationContains Relation = "destination_contains"
	RelationFastForward         Relation = "fast_forward"
	RelationDivergent           Relation = "divergent"
)

// Publication is one push to the origin.
type Publication struct {
	URL         string
	Branch      string // the P branch published; it must still name Commit
	Commit      OID
	Destination string   // the origin head's name, as CheckNames returned it
	Relation    Relation // absent or fast-forward, as ComparePublication answered
}

// PublishStatus is the origin's answer to a push.
type PublishStatus string

const (
	PublishCreated   PublishStatus = "created"
	PublishAdvanced  PublishStatus = "advanced"
	PublishSatisfied PublishStatus = "satisfied"
	PublishRefused   PublishStatus = "refused"
)

// Gate is a core function a driver calls once, right before its first
// effect. Core records its durable intent there.
type Gate func() error

// EffectKind names the effect an Effect describes.
type EffectKind string

const (
	EffectCreateBranch EffectKind = "create-branch"
	EffectDeleteBranch EffectKind = "delete-branch"
	EffectService      EffectKind = "service"
)

// Effect is an effect whose outcome core asks the driver to settle.
type Effect struct {
	Kind    EffectKind
	Project string
	Branch  string
	// Old and New are a head effect's state before and the state intended;
	// empty means absent.
	Old, New OID
	Service  string // a service: its ID
}

// Settlement is what became of an effect.
type Settlement int

const (
	// Unknown: the driver cannot tell yet. Core keeps its guard and never
	// sends the same effect again.
	Unknown Settlement = iota
	// Applied: for a head effect, the intended state holds exactly and
	// nothing the driver admitted can still change it; for a service, nothing
	// it started can still change a head.
	Applied
	// NotApplied: for a head effect, the prior state holds and nothing can
	// still change it. A service never settles NotApplied.
	NotApplied
)

func (s Settlement) String() string {
	switch s {
	case Applied:
		return "applied"
	case NotApplied:
		return "not-applied"
	}
	return "unknown"
}

var (
	// ErrUnknownOutcome: the effect may have happened; settle it.
	ErrUnknownOutcome = errors.New("the effect may or may not have happened")
	// ErrInvalid: a name, URL, commit ID or command breaks the backend's
	// syntax.
	ErrInvalid = errors.New("a value breaks the backend's syntax")
	// ErrConflict: a head or object is not in the expected state.
	ErrConflict = errors.New("a head or object is not in the expected state")
	// ErrNotFound: the project has no repository.
	ErrNotFound = errors.New("the project has no repository")
	// ErrLimit: a bound was exceeded.
	ErrLimit = errors.New("a bound was exceeded")
	// ErrUnavailable: the backend or the origin cannot be reached; nothing is
	// claimed.
	ErrUnavailable = errors.New("the backend or the origin cannot be reached")
)
