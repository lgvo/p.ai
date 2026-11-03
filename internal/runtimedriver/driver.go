// Package runtimedriver is the contract between P's core and the code that
// owns runtime machinery. Core names P's concepts; a driver decides how its
// backend realizes them: the image and its build, the session's runtime and
// guest, the session host, project services, terminals, devices, network,
// and the proofs of its own boundary. Core keeps lifecycle state, the
// journal, policy, Git authority and the proofs of absence.
//
// Three ideas carry the contract:
//
//   - P's concepts, not the guest's layout. Core names a session, its
//     command, branch, key, endpoints, grants and network; the driver decides
//     where they go. The one guest place core knows is the workspace, whose
//     files core reads and writes only while the session is stopped.
//   - Gates. A mutation takes a gate: a core function the driver calls once,
//     after every check and right before the first effect. Core records its
//     durable intent there. If the gate fails, nothing happened; if the call
//     fails after it, the outcome is unknown.
//   - Settling. An unknown outcome is resolved by Settle and by operations
//     that are idempotent by reference, never by sending a non-idempotent
//     effect again.
package runtimedriver

import (
	"context"
	"errors"
	"io"
)

// Driver is the only code that creates, changes, reads or deletes runtime
// machinery. Every implementation acts only in its configured scope, on
// objects that carry this P instance's labels, and never adopts, changes or
// deletes anything else. It reads native state fresh on every call, claims
// nothing after an error, and checks its boundary before every mutation,
// refusing rather than widening it.
type Driver interface {
	Images

	// Check proves the driver's boundary and its egress substrate from live
	// state.
	Check(ctx context.Context) error
	// Inventory lists every object in scope, P's or not, including ones
	// being deleted, with labels as stored, within the bound it states. It
	// returns an error rather than a partial list.
	Inventory(ctx context.Context) (Inventory, error)

	// Inspect reads the runtime of s. It reports StateAbsent only when its
	// direct address for s.Ref finds nothing, and ErrMismatch for an object
	// that carries the reference but differs from the spec.
	Inspect(ctx context.Context, s Session) (Observation, error)
	// Create with no incarnation looks through the whole scope first, so a
	// natively renamed runtime blocks it, calls g, initializes the runtime,
	// installs everything s names and leaves it stopped. With an incarnation
	// it never initializes: it completes that stopped runtime, keeping
	// identical guest files, installing missing ones and refusing different
	// ones; g is not called and may be nil.
	Create(ctx context.Context, s Session, at Incarnation, g Gate) (Observation, error)
	// Start converges on a running runtime whose session host is ready and
	// does nothing when that holds. It re-checks grants, endpoints and the
	// egress substrate. If the host fails, the runtime ends stopped and Start
	// returns the bounded diagnostic in a *HostError.
	Start(ctx context.Context, s Session) (Observation, error)
	// Stop converges on a stopped runtime, gracefully and within a bound.
	Stop(ctx context.Context, s Session) (Observation, error)
	// OpenTerminal attaches the session user to the session host with a
	// PTY. It arms a completion witness before the attach starts, or fails.
	OpenTerminal(ctx context.Context, t Target, size Size) (Terminal, error)

	// ReadWorkspace, WriteWorkspace and RemoveWorkspace reach the workspace
	// of a stopped session at t.At, with paths relative to the workspace
	// root. They never run code the session or its repository can change,
	// never follow symlinks, and reach only the session's private storage.
	// ReadWorkspace reports a missing path as a File of type FileAbsent.
	ReadWorkspace(ctx context.Context, t Target, path string, limit int64) (File, error)
	WriteWorkspace(ctx context.Context, t Target, path string, f File, g Gate) error
	RemoveWorkspace(ctx context.Context, t Target, path string, g Gate) error

	// Services lists the project services of a running session whose host
	// is ready: a complete, bounded inventory sorted by name, or an error.
	Services(ctx context.Context, t Target) ([]Service, error)
	// ServiceAction queues a start, stop or restart and returns once the
	// runtime accepted it; the inventory shows its progress.
	ServiceAction(ctx context.Context, t Target, name string, a Action) error
	// ServiceLogs returns a bounded recent log tail of one service.
	ServiceLogs(ctx context.Context, t Target, name string) (Logs, error)
}

// Images is the part of the driver that commit images use: the image
// operations, Describe, Settle, and Delete for a build's leftovers.
type Images interface {
	// Describe says what the driver offers.
	Describe() Capabilities
	// BuildImage builds an image of b's commit for this runtime and labels it
	// with b.Labels. It calls g once, after every check and right before its
	// first effect; if g fails, nothing happened. On success, and on any error
	// other than ErrUnknownOutcome, it leaves nothing of the build behind.
	BuildImage(ctx context.Context, b Build, g Gate) (Image, error)
	// Images lists every image in scope that carries this P instance's labels.
	Images(ctx context.Context) ([]Image, error)
	// InspectImage reads one image by its reference. It reports false when the
	// reference finds nothing, and ErrMismatch for an image whose labels are
	// not this P instance's.
	InspectImage(ctx context.Context, ref ImageRef) (Image, bool, error)
	// DeleteImage deletes exactly img: the same reference with the same labels.
	// It calls g right before the delete and succeeds when the image is gone,
	// including when it was already gone. Runtimes created from the image are
	// never affected.
	DeleteImage(ctx context.Context, img Image, g Gate) error
	// Settle says what became of an effect whose call returned
	// ErrUnknownOutcome, from native state only, so the answer holds after a
	// restart.
	Settle(ctx context.Context, e Effect) (Settlement, error)
	// Delete removes the object r names. For a session it acts only on the
	// runtime at incarnation at, stops it first when it runs, and succeeds
	// once the reference is absent and its private storage gone. For a build
	// at is empty: it removes what the build left behind, such as its
	// builder, and never deletes an image. It calls g right before its first
	// effect, and not at all when there is nothing to remove.
	Delete(ctx context.Context, r Ref, at Incarnation, g Gate) error
}

// Capabilities describe a driver.
type Capabilities struct {
	// ImageBuilder names how the driver builds images. It is part of every
	// image's identity: a change builds new images even for a commit that
	// has one.
	ImageBuilder string
	// Networks are the session networks the driver offers: NetworkNone and,
	// when its egress substrate is configured, NetworkPublicEgress.
	Networks []string
	// EgressAddresses is how many public-egress addresses core may reserve;
	// 0 when the backend assigns addresses itself.
	EgressAddresses int
	// Services is false when the services operations return ErrUnsupported.
	Services bool
}

// The networks a runtime can have.
const (
	NetworkNone         = "none"
	NetworkPublicEgress = "public-egress"
)

// Kind says what a Ref names.
type Kind string

const (
	KindSession Kind = "session"
	KindBuild   Kind = "build"
)

// Ref names one driver-side object by P's identity for it: a session's UUID,
// or the UUID core chose for one build attempt (a new build gets a new
// reference).
type Ref struct {
	Kind Kind
	ID   string
}

// Incarnation is the driver's opaque identity for one runtime at a
// reference: the same across start and stop, new when the runtime is
// re-created or restored. Empty means any, where a call accepts that.
type Incarnation string

// ImageRef is the driver's own reference to an image, opaque to core (an
// image fingerprint on Incus).
type ImageRef string

// Labels are P's identity facts for an object. The driver stores them with
// the object, immutably, and returns them verbatim.
type Labels map[string]string

// The labels core gives commit images and session runtimes. The driver may
// store them under its own names; it returns them under these.
const (
	LabelProject        = "project"
	LabelSourceCommit   = "source_commit"
	LabelBaseImage      = "base_image"
	LabelSystem         = "system"
	LabelBuilderVersion = "builder_version"
	LabelBuild          = "build"
)

// Session is core's spec of one session's runtime. The driver decides where
// each part goes in the guest.
type Session struct {
	Ref    Ref    // kind session
	Labels Labels // LabelProject: the P project the session belongs to
	// Image is the image the runtime is created from: the commit image of
	// the session's commit, or the base image when there is none.
	Image     ImageRef
	Command   []string  // the interactive command the session host runs
	Workspace Workspace // what the workspace holds
	// GitKey is the session's private P Git key (PEM), and GitHostKey the
	// public host key of P's Git endpoint that the session pins, as an
	// authorized_keys line.
	GitKey     []byte
	GitHostKey string
	// Endpoints is the host directory holding session.sock and git.sock; the
	// runtime shows them read-only at /opt/p/endpoints.
	Endpoints string
	Grants    []Grant // each shown at /mnt/p/<name> with its flags
	Network   Network
	Assets    []Asset // the session assets core selected
}

// Workspace is the Git clone the session works in: the branch P assigned the
// session and the commit it starts from, from P's Git endpoint. An empty
// Commit means an unborn branch.
type Workspace struct {
	Repository string // the P project whose repository it clones
	Branch     string
	Commit     string
}

// Grant is a host path core captured under the trusted ceilings for one
// session, with the identity it had when captured.
type Grant struct {
	Name       string
	Source     string
	Type       string // file or directory
	Access     string // read-only or read-write
	Executable bool
	Device     uint64
	Inode      uint64
	OwnerUID   uint32
	OwnerGID   uint32
}

// Network is a runtime's network: none, or public egress at the address core
// reserved for it.
type Network struct {
	PublicIPv4 string // empty means no network
}

// Asset is one file of a session asset package core selected, named by its
// role. The driver installs it where its session host expects that role.
type Asset struct {
	Role   string // such as p-attach, p-git-ssh or p-codex-adapter
	Mode   uint32
	Data   []byte
	SHA256 string // hex SHA-256 of Data
}

// Target names a session's runtime at an incarnation.
type Target struct {
	Session Session
	At      Incarnation // empty means any; otherwise it must match
}

// State is a runtime's state.
type State string

const (
	StateAbsent  State = "absent"
	StateStopped State = "stopped"
	StateRunning State = "running"
	StateOther   State = "other" // starting, stopping, frozen or anything else
)

// Observation is the runtime as Inspect, Create, Start or Stop saw it.
type Observation struct {
	State       State
	Incarnation Incarnation
	// Complete is false only for a runtime whose Create was interrupted;
	// Create at its incarnation completes it.
	Complete bool
	Host     HostState
}

// HostState is the session host of a running runtime, or the last words of
// a stopped one's.
type HostState struct {
	Ready bool
	// Unknown is true for a running runtime whose host could not be read.
	Unknown bool
	// Diagnostic is the host's bounded, printable account of itself, such as
	// why it is not ready or why it ended; empty when there is none.
	Diagnostic string
}

// HostError is Start's error when the session host did not become ready.
// The runtime has been stopped, unless stopping it failed too.
type HostError struct{ Diagnostic string }

func (e *HostError) Error() string { return e.Diagnostic }

// Inventory is every object in the driver's scope.
type Inventory struct {
	// Capacity is how many runtimes, sessions and builders together, the
	// scope admits; 0 when the backend states no limit.
	Capacity int
	Objects  []Object
}

// Object is one runtime or builder in scope, P's or not.
type Object struct {
	Name string // the backend's own name, for diagnostics only
	// Ref is this P instance's reference read from the object's labels; it
	// is empty when the object carries none.
	Ref Ref
	// Labels are the P labels stored with the object, verbatim, whichever
	// P instance wrote them.
	Labels      Labels
	Incarnation Incarnation
	State       State
}

// Size is a terminal's size in character cells.
type Size struct{ Width, Height int }

// Terminal is an attached PTY on the session host. Read returns the
// terminal's output and Write sends input. Close ends the attachment.
type Terminal interface {
	io.ReadWriteCloser
	// ID names the attachment for Settle.
	ID() string
	Resize(Size) error
	// Ended delivers the attachment's end once: Proven only when the
	// driver's witness saw the guest process end, false when only the
	// channel closed.
	Ended() <-chan Completion
}

// Completion is how a terminal ended.
type Completion struct{ Proven bool }

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

// ServiceState is P's closed set of project service states.
type ServiceState string

const (
	ServiceStopped  ServiceState = "stopped"
	ServiceStarting ServiceState = "starting"
	ServiceRunning  ServiceState = "running"
	ServiceStopping ServiceState = "stopping"
	ServiceFailed   ServiceState = "failed"
)

// Service is one project service of a session.
type Service struct {
	Name        string // the driver's name for it; opaque to core
	Description string
	State       ServiceState
	Detail      string // the driver's words, for display only
}

// Action is what ServiceAction asks of a service.
type Action string

const (
	ActionStart   Action = "start"
	ActionStop    Action = "stop"
	ActionRestart Action = "restart"
)

// The bounds of the services operations.
const (
	MaxServices    = 64
	MaxLogEntries  = 100
	MaxLogBytes    = 16000
	MaxServiceName = 128
)

// Logs is a service's recent log tail, newest last, valid UTF-8: at most
// MaxLogEntries entries, cut to MaxLogBytes. Truncated says the byte bound
// cut it.
type Logs struct {
	Text      string
	Truncated bool
}

// Build asks for an image of one source commit.
type Build struct {
	Ref     Ref          // kind build, new for every attempt
	Labels  Labels       // the image's identity, LabelBuild equal to Ref.ID
	Source  SourceExport // the commit, as a Git bundle core staged
	Network Network      // the builder's network
}

// SourceExport is a Git bundle of one commit and every commit it reaches,
// with one ref naming the commit. Core owns the file and removes it after the
// build. The builder clones it into the image's workspace.
type SourceExport struct {
	Bundle string // host path of the bundle file
	Ref    string // the bundle's one ref
	Commit string // the commit it names
}

// Image is one built image.
type Image struct {
	Ref    ImageRef
	Labels Labels
	Shell  Shell // what the build found at the commit
	Size   int64 // bytes the image occupies, as the backend reports it
}

// Shell is the devShell outcome a build recorded in its image.
type Shell struct {
	Selection Selection
	// MaterialSHA256 identifies the activation material in the image; set
	// only for SelectionDevShell.
	MaterialSHA256 string
}

// Selection is which environment an image activates.
type Selection string

const (
	SelectionNoFlake   Selection = "no-flake"   // the commit has no flake.nix: base packages only
	SelectionNoDefault Selection = "no-default" // its flake has no default devShell for the system
	SelectionDevShell  Selection = "devshell"   // its default devShell, realized into the image
)

// Gate is a core function the driver calls once, after every check and right
// before the first effect. Core records its durable intent there.
type Gate func() error

// EffectKind names the effect an Effect describes.
type EffectKind string

const (
	EffectCreate      EffectKind = "create"
	EffectStart       EffectKind = "start"
	EffectStop        EffectKind = "stop"
	EffectDelete      EffectKind = "delete"
	EffectWrite       EffectKind = "write"
	EffectRemove      EffectKind = "remove"
	EffectBuild       EffectKind = "build"
	EffectDeleteImage EffectKind = "delete-image"
	EffectTerminal    EffectKind = "terminal"
)

// Effect is a call whose outcome core asks the driver to settle.
type Effect struct {
	Kind     EffectKind
	Target   Target // create, start, stop, delete, write, remove, terminal
	Build    Build  // build
	Image    Image  // delete-image
	Path     string // write, remove
	Content  File   // write: what must now be there
	Terminal string // terminal: the Terminal's ID
}

// Settlement is what became of an effect.
type Settlement int

const (
	// Unknown: the backend cannot tell yet. Core keeps its guard and never
	// sends the same non-idempotent effect again.
	Unknown Settlement = iota
	// Applied: the intended outcome is present exactly and nothing admitted
	// on the target is still running. For create, the intended outcome is
	// the initialized runtime at the reference, complete or not; for a
	// terminal, that the attachment ended.
	Applied
	// NotApplied: the backend proves the effect can never be admitted later.
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
	ErrUnknownOutcome = errors.New("the effect may or may not have happened")
	ErrMismatch       = errors.New("an object differs from the spec; it is never adopted")
	ErrUnavailable    = errors.New("the backend cannot be reached; nothing is claimed")
	ErrUnsupported    = errors.New("the driver does not offer this operation")
	ErrNoService      = errors.New("no such project service in this session")
	ErrNotReady       = errors.New("the session host is not ready")
)
