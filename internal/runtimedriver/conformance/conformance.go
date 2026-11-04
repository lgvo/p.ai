// Package conformance is the suite every runtime driver must pass. A driver
// package runs it from its own tests with a Fixture: the driver under test
// and native controls that act on the backend's state directly, the way an
// administrator, a crash or a slow backend would.
//
// The cases are grouped. A backend claims the groups it implements and
// implements only the native controls of those groups; the suite runs the
// groups it is given. Cases that only a real backend can show, such as the
// egress proofs, are VM scenarios and not part of this suite.
package conformance

import (
	"io"
	"testing"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// Group is a set of cases about one part of the contract.
type Group string

const (
	// Sessions: Inspect, Create, Start, Stop and host failure.
	Sessions Group = "sessions"
	// Recovery: an interrupted or delayed Create, settled and completed at
	// its incarnation.
	Recovery Group = "recovery"
	// Deletion: Delete of a session and its settlement. It also uses the
	// workspace operations and Inventory.
	Deletion Group = "deletion"
	// Inventory: every object in scope, with the capacity.
	Inventory Group = "inventory"
	// Workspace: the workspace operations on a stopped session.
	Workspace Group = "workspace"
	// Terminals: OpenTerminal, its completion and its settlement.
	Terminals Group = "terminals"
	// Services: the project services operations.
	Services Group = "services"
	// Images: BuildImage, the image operations and the settlement of builds.
	Images Group = "images"
)

// AllGroups is every group, for a backend that implements the whole
// contract.
var AllGroups = []Group{Sessions, Recovery, Deletion, Inventory, Workspace, Terminals, Services, Images}

// Fixture is one backend under test, with native state of its own that no
// other fixture shares.
//
// Every case uses Driver, Restart, Session and the SessionControls. The
// other control accessors are called only by the cases of their group; a
// backend returns nil from those of groups it does not claim. No case holds
// more than four runtimes in scope at once.
type Fixture interface {
	// Driver is the driver under test. After Restart it is the new driver.
	Driver() runtimedriver.Driver
	// Restart replaces the driver with a new one over the same native
	// state, as after the driver process died: calls in flight are lost and
	// return an error, nothing the old driver started goes on, and its
	// terminals end unproven.
	Restart()
	// Session returns a valid spec for a new session, whose reference no
	// object in scope carries.
	Session(o SessionOptions) runtimedriver.Session

	Sessions() SessionControls
	Recovery() RecoveryControls
	Deletion() DeletionControls
	Inventory() InventoryControls
	Workspace() WorkspaceControls
	Terminals() TerminalControls
	Services() ServiceControls
	Images() ImageControls
}

// SessionOptions shape the spec Fixture.Session returns.
type SessionOptions struct {
	// Grant gives the session one grant of a host directory that exists.
	Grant bool
	// PublicEgress gives the session a public-egress address no other
	// session holds. Asked only of a driver that offers public egress.
	PublicEgress bool
}

// SessionControls act on the native state of session runtimes. Every group
// uses them, so every backend implements them.
type SessionControls interface {
	// Initializations counts the runtimes the backend initialized for the
	// reference, at any name. On Incus: the `incus init` requests that
	// created an instance carrying the reference.
	Initializations(r runtimedriver.Ref) int
	// Transitions counts the starts and stops the backend performed on the
	// runtime of the reference. On Incus: the `incus start` and
	// `incus stop` requests that reached its instance.
	Transitions(r runtimedriver.Ref) int
	// RenameAway moves the runtime of the reference away from the driver's
	// address for it, keeping its labels and incarnation, as an
	// administrator's rename would. On Incus: `incus rename` to a name the
	// driver does not use.
	RenameAway(r runtimedriver.Ref)
	// Drift changes the runtime of the reference so that it still carries
	// the reference but differs from its spec in the way d names.
	Drift(r runtimedriver.Ref, d Drift)
	// FailHostOnNextStart makes the session host of the reference fail on
	// the runtime's next start, leaving diagnostic as its last words. On
	// Incus: `p-interactive.service` ends failed after the instance starts,
	// and its journal holds diagnostic.
	FailHostOnNextStart(r runtimedriver.Ref, diagnostic string)
	// UnreadyHost makes the session host of the running runtime of the
	// reference report that it is not ready, with diagnostic. On Incus:
	// `p-interactive.service` is activating, and its journal holds
	// diagnostic.
	UnreadyHost(r runtimedriver.Ref, diagnostic string)
	// PublicAddress reads the public IPv4 address the runtime of the
	// reference is attached to, or "" when it has no network. On Incus: its
	// NIC's ipv4.address.
	PublicAddress(r runtimedriver.Ref) string
	// WriteRootFile writes data to a regular file at path, an absolute
	// guest path outside the workspace, in the private root of the runtime
	// of the reference, as the session could. On Incus: the instance's file
	// API, for example on /home/p/marker.
	WriteRootFile(r runtimedriver.Ref, path string, data []byte)
	// ReadRootFile reads the regular file at path, an absolute guest path,
	// in the private root of the runtime of the reference; false when it is
	// absent. On Incus: the instance's file API.
	ReadRootFile(r runtimedriver.Ref, path string) ([]byte, bool)
}

// Drift is one way a runtime can differ from its spec.
type Drift int

const (
	// DriftLabels changes the value of one of the session's labels. On
	// Incus: a `user.p.*` key such as the project path.
	DriftLabels Drift = iota
	// DriftImage makes the runtime's image another one. On Incus: the
	// image fingerprint keys.
	DriftImage
	// DriftDevices adds a device the spec does not name. On Incus: a disk
	// device.
	DriftDevices
)

// RecoveryControls interrupt and delay Create.
type RecoveryControls interface {
	// DieInCreate makes the driver die at point inside the next Create: it
	// stops there as if its process died and effects nothing more. That
	// call returns only after Restart, with an error. The channel closes
	// once the driver reached point.
	DieInCreate(point CreatePoint) <-chan struct{}
	// DelayCreate makes the backend admit the next Create's initialization
	// late: the call passes its gate and returns ErrUnknownOutcome while no
	// observation shows anything. land admits it, and the runtime then
	// exists as initialized, with nothing installed. On Incus: `incus init`
	// fails at the client and lands later, before Incus lists an operation
	// for it.
	DelayCreate() (land func())
	// Hold keeps an operation of the backend running on the runtime of the
	// reference until release is called. On Incus: an operation listed as
	// running on the instance.
	Hold(r runtimedriver.Ref) (release func())
}

// CreatePoint is where DieInCreate stops the driver.
type CreatePoint int

const (
	// AfterInit: the runtime is initialized and nothing is installed yet.
	AfterInit CreatePoint = iota
	// BetweenGuestWrites: one guest file is written, the next is not.
	BetweenGuestWrites
	// DuringGuestWrite: a guest file write was sent and the driver died
	// before its answer; the file landed whole.
	DuringGuestWrite
	// DuringPartialGuestWrite: as DuringGuestWrite, but only part of the
	// file landed.
	DuringPartialGuestWrite
)

// DeletionControls observe and delay Delete.
type DeletionControls interface {
	// DeletedWhileRunning reports whether the backend deleted the runtime
	// of the reference while it was running. On Incus: an `incus delete`
	// reached the instance while its status was Running.
	DeletedWhileRunning(r runtimedriver.Ref) bool
	// DelayDelete makes the backend admit the next Delete's removal late:
	// the call passes its gate and returns ErrUnknownOutcome with the
	// runtime still in place. land admits the removal.
	DelayDelete() (land func())
}

// InventoryControls add objects to the scope and disturb its listing.
type InventoryControls interface {
	// Capacity is the capacity the backend states for the scope; 0 for no
	// limit. On Incus: the project's limits.containers.
	Capacity() int
	// AddUnlabeled adds an object to the scope that carries no P labels
	// and returns its native name.
	AddUnlabeled() string
	// AddOtherInstance adds an object to the scope that another P instance
	// labeled with r and labels, and returns its native name.
	AddOtherInstance(r runtimedriver.Ref, labels runtimedriver.Labels) string
	// Terminate starts the deletion of the runtime of the reference and
	// keeps it in progress: the object stays, terminating.
	Terminate(r runtimedriver.Ref)
	// PartialInventory makes the backend's next listing of the scope
	// incomplete. On Incus: the listing fails or is truncated part way.
	PartialInventory()
}

// WorkspaceControls act on the stopped storage of a session as its guest or
// an administrator could have left it.
type WorkspaceControls interface {
	// Symlink leaves a symbolic link at path, relative to the workspace of
	// the runtime of the reference, pointing at target, as the session
	// could have made it.
	Symlink(r runtimedriver.Ref, path, target string)
	// ForeignMount makes a mount that is not the runtime's private storage
	// appear at guestPath, an absolute guest path, as the driver's view of
	// the stopped runtime would see it. On a backend that reads through a
	// live mount namespace it is a mount there; on Incus, a disk device at
	// that path.
	ForeignMount(r runtimedriver.Ref, guestPath string)
	// Markers puts a file named "marker" in the source of each of s's
	// grants and in its endpoint directory, and returns the file's
	// contents.
	Markers(s runtimedriver.Session) []byte
	// SetRoot gives the workspace root of the runtime of the reference an
	// owner and permission bits, as the session could have. On Incus: the
	// owner and mode of /workspace in the instance's storage.
	SetRoot(r runtimedriver.Ref, uid, gid int, mode uint32)
}

// TerminalControls reach the guest side of attachments.
type TerminalControls interface {
	// Guest is the guest side of the attachment the terminal id names.
	Guest(id string) GuestTerminal
}

// GuestTerminal is the session host's end of one attachment. Read returns
// what the client wrote, and what Write writes the client reads.
type GuestTerminal interface {
	io.ReadWriter
	// Size is the PTY's current size, as the guest sees it.
	Size() runtimedriver.Size
	// EndProcess ends the attached guest process.
	EndProcess()
	// DropChannel closes only the native channel of the attachment; the
	// guest process keeps running. On Incus: the exec websocket drops.
	DropChannel()
}

// ServiceControls act on the service manager of a running session.
type ServiceControls interface {
	// AddService adds a project service to the session of the reference
	// and returns the driver's name for it. On Incus: the unit
	// p-project-<base>.service, loaded with the state's active state.
	AddService(r runtimedriver.Ref, svc NativeService) string
	// Progress finishes the queued job of the named service: starting
	// becomes running, or failed when the service fails to start; stopping
	// becomes stopped.
	Progress(r runtimedriver.Ref, name string)
	// SetLogs replaces the named service's log with entries, oldest first,
	// one per line.
	SetLogs(r runtimedriver.Ref, name string, entries []string)
	// Reached lists every service name the session's service manager was
	// asked to act on or to read the log of. On Incus: the unit names in
	// the `systemctl` and `journalctl` commands that ran.
	Reached(r runtimedriver.Ref) []string
	// BreakManager makes the session's service manager unreachable.
	BreakManager(r runtimedriver.Ref)
}

// NativeService is a project service as AddService defines it.
type NativeService struct {
	Base         string // the name the service is defined with
	Description  string
	State        runtimedriver.ServiceState
	FailsToStart bool
}

// ImageControls give builds and act on the images in scope.
type ImageControls interface {
	// Build returns a valid build of a new reference.
	Build() runtimedriver.Build
	// DelayBuild makes the backend admit the next build's publication
	// late: the call passes its gate, leaves its builder, and returns
	// ErrUnknownOutcome while no image is observable. land publishes the
	// image as the build would have. On Incus: `incus publish` fails at the
	// client and lands later.
	DelayBuild() (land func())
	// AddOtherInstanceImage adds an image to the scope that another P
	// instance labeled, and returns its reference.
	AddOtherInstanceImage() runtimedriver.ImageRef
}

// testCase is one rule of the contract, checked on a fresh fixture.
type testCase struct {
	rule  string
	check func(t *testing.T, f Fixture)
}

// Run runs every case of the groups the fixture's backend claims, each as a
// subtest named by the rule it checks, on a fixture of its own.
func Run(t *testing.T, groups []Group, newFixture func(t *testing.T) Fixture) {
	cases := map[Group][]testCase{
		Sessions:  sessionCases,
		Recovery:  recoveryCases,
		Deletion:  deletionCases,
		Inventory: inventoryCases,
		Workspace: workspaceCases,
		Terminals: terminalCases,
		Services:  serviceCases,
		Images:    imageCases,
	}
	for _, g := range groups {
		group, ok := cases[g]
		if !ok {
			t.Fatalf("unknown conformance group %q", g)
		}
		t.Run(string(g), func(t *testing.T) {
			for _, c := range group {
				t.Run(c.rule, func(t *testing.T) { c.check(t, newFixture(t)) })
			}
		})
	}
}
