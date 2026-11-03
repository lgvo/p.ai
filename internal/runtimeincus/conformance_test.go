package runtimeincus

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/runtimedriver/conformance"
)

// The Incus driver implements the sessions, inventory and services groups in
// this migration step; later steps add the others.
func TestConformance(t *testing.T) {
	conformance.Run(t, []conformance.Group{conformance.Sessions, conformance.Inventory, conformance.Services}, newIncusFixture)
}

// incusFixture is the Incus driver over a stubbed Incus whose host has the
// public egress substrate. Its controls act on the world as an
// administrator, a crash or the guest would.
type incusFixture struct {
	t               *testing.T
	world           *incusWorld
	driver          *Driver
	sessions, slots int
}

func newIncusFixture(t *testing.T) conformance.Fixture {
	w := newIncusWorld(t)
	w.public = testEgressSubstrate()
	return &incusFixture{t: t, world: w, driver: w.driver()}
}

func (f *incusFixture) Driver() runtimedriver.Driver { return f.driver }

// Restart serves Incus to a new driver only: the old one's commands and
// file requests reach nothing.
func (f *incusFixture) Restart() {
	f.world.restart()
	f.driver = f.world.driver()
}

func (f *incusFixture) Session(o conformance.SessionOptions) runtimedriver.Session {
	f.sessions++
	s := testSessionSpec(f.t)
	s.Ref.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", f.sessions)
	s.Endpoints = "/var/lib/p-vm/endpoints/" + s.Ref.ID
	if o.Grant {
		s.Grants = []runtimedriver.Grant{testGrant()}
	}
	if o.PublicEgress {
		// P reserves one of the bridge's four guest addresses per session.
		s.Network.PublicIPv4 = fmt.Sprintf("10.233.0.%d/24", 10+f.slots%publicEgressAddresses)
		f.slots++
	}
	return s
}

func (f *incusFixture) Sessions() conformance.SessionControls    { return f }
func (f *incusFixture) Recovery() conformance.RecoveryControls   { return nil }
func (f *incusFixture) Deletion() conformance.DeletionControls   { return nil }
func (f *incusFixture) Inventory() conformance.InventoryControls { return f }
func (f *incusFixture) Workspace() conformance.WorkspaceControls { return nil }
func (f *incusFixture) Terminals() conformance.TerminalControls  { return nil }
func (f *incusFixture) Services() conformance.ServiceControls    { return f }
func (f *incusFixture) Images() conformance.ImageControls        { return nil }

// runtime runs change on the instance that carries the reference, at any
// name, under the world's lock.
func (f *incusFixture) runtime(r runtimedriver.Ref, change func(*worldInstance)) {
	f.t.Helper()
	w := f.world
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, in := range w.instances {
		if in.config["user.p.session_uuid"] == r.ID {
			change(in)
			return
		}
	}
	f.t.Fatalf("no instance carries %s", r.ID)
}

// Sessions.

func (f *incusFixture) Initializations(r runtimedriver.Ref) int {
	f.world.mu.Lock()
	defer f.world.mu.Unlock()
	return f.world.initialized[r.ID]
}

func (f *incusFixture) Transitions(r runtimedriver.Ref) int {
	f.world.mu.Lock()
	defer f.world.mu.Unlock()
	return f.world.transitions[r.ID]
}

func (f *incusFixture) RenameAway(r runtimedriver.Ref) {
	f.runtime(r, func(in *worldInstance) { in.name = "renamed-" + r.ID })
}

func (f *incusFixture) Drift(r runtimedriver.Ref, d conformance.Drift) {
	f.runtime(r, func(in *worldInstance) {
		switch d {
		case conformance.DriftLabels:
			in.config["user.p.project_path"] = "team/other"
		case conformance.DriftImage:
			other := strings.Repeat("b", 64)
			in.config["user.p.image_fingerprint"], in.config["volatile.base_image"] = other, other
		case conformance.DriftDevices:
			in.devices["data"] = map[string]string{"type": "disk", "source": "/var/lib/p-vm/grants/other", "path": "/mnt/data"}
		default:
			f.t.Fatalf("unknown drift %d", d)
		}
	})
}

func (f *incusFixture) FailHostOnNextStart(r runtimedriver.Ref, diagnostic string) {
	f.runtime(r, func(in *worldInstance) { in.failNextStart = diagnostic })
}

func (f *incusFixture) UnreadyHost(r runtimedriver.Ref, diagnostic string) {
	f.runtime(r, func(in *worldInstance) {
		in.host, in.boot, in.journal = "activating/start-post/success", "", diagnostic
	})
}

// PublicAddress is the address of the runtime's NIC on the egress bridge,
// with the bridge's prefix length as P's specs write it.
func (f *incusFixture) PublicAddress(r runtimedriver.Ref) string {
	address := ""
	f.runtime(r, func(in *worldInstance) {
		if nic := in.devices[publicNICName]; nic != nil {
			_, bits, _ := strings.Cut(f.world.public.BridgeIPv4, "/")
			address = nic["ipv4.address"] + "/" + bits
		}
	})
	return address
}

// WriteRootFile writes the file through the instance's file API, owned by
// the session user, as the session could.
func (f *incusFixture) WriteRootFile(r runtimedriver.Ref, path string, data []byte) {
	if err := f.files(r).put(f.t.Context(), path, guestFile{typ: "file", uid: 1000, gid: 1000, mode: 0644, data: data}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *incusFixture) ReadRootFile(r runtimedriver.Ref, path string) ([]byte, bool) {
	file, found, err := f.files(r).get(f.t.Context(), path)
	if err != nil {
		f.t.Fatal(err)
	}
	return file.data, found && file.typ == "file"
}

// files is the file API of the instance that carries the reference.
func (f *incusFixture) files(r runtimedriver.Ref) *unixFileAPI {
	name := ""
	f.runtime(r, func(in *worldInstance) { name = in.name })
	f.world.mu.Lock()
	defer f.world.mu.Unlock()
	return &unixFileAPI{socket: f.world.socket, project: "user-1000", instance: name}
}

// Inventory.

// otherInstance is another P instance sharing the Incus project.
const otherInstance = "99999999-9999-4999-8999-999999999999"

// Capacity is the project's limits.containers.
func (f *incusFixture) Capacity() int {
	f.world.mu.Lock()
	defer f.world.mu.Unlock()
	limit, err := strconv.Atoi(f.world.containerLimit)
	if err != nil {
		f.t.Fatal(err)
	}
	return limit
}

func (f *incusFixture) AddUnlabeled() string {
	f.world.mu.Lock()
	defer f.world.mu.Unlock()
	return f.world.addInstance(fmt.Sprintf("foreign-%d", len(f.world.instances)), "Stopped", nil).name
}

// AddOtherInstance adds a stopped container that another P instance labeled
// with r and labels under the keys that P instance writes.
func (f *incusFixture) AddOtherInstance(r runtimedriver.Ref, labels runtimedriver.Labels) string {
	keys := map[runtimedriver.Kind]map[string]string{
		runtimedriver.KindSession: {"ref": "user.p.session_uuid", runtimedriver.LabelProject: "user.p.project_path", "instance": "user.p.instance_uuid"},
		runtimedriver.KindBuild:   {"ref": "user.p.builder_request_uuid", runtimedriver.LabelProject: "user.p.builder_project_path", "instance": "user.p.instance"},
	}[r.Kind]
	config := map[string]string{keys["ref"]: r.ID, keys["instance"]: otherInstance}
	for label, value := range labels {
		key, ok := keys[label]
		if !ok || label == "ref" || label == "instance" {
			f.t.Fatalf("the fixture cannot store label %s", label)
		}
		config[key] = value
	}
	f.world.mu.Lock()
	defer f.world.mu.Unlock()
	return f.world.addInstance(fmt.Sprintf("foreign-%d", len(f.world.instances)), "Stopped", config).name
}

// Terminate leaves the runtime listed while its delete stops it, as Incus
// shows an instance whose delete is in progress.
func (f *incusFixture) Terminate(r runtimedriver.Ref) {
	f.runtime(r, func(in *worldInstance) { in.status = "Stopping" })
}

// PartialInventory truncates the next listing of the whole project part way.
func (f *incusFixture) PartialInventory() {
	f.world.mu.Lock()
	defer f.world.mu.Unlock()
	f.world.partialList = true
}

// Services.

// AddService loads the user unit p-project-<base>.service in the state's
// active state, as installed.
func (f *incusFixture) AddService(r runtimedriver.Ref, svc conformance.NativeService) string {
	name := "p-project-" + svc.Base + ".service"
	states := map[runtimedriver.ServiceState][2]string{
		runtimedriver.ServiceStopped: {"inactive", "dead"}, runtimedriver.ServiceStarting: {"activating", "start"},
		runtimedriver.ServiceRunning: {"active", "running"}, runtimedriver.ServiceStopping: {"deactivating", "stop"},
		runtimedriver.ServiceFailed: {"failed", "failed"},
	}
	state, ok := states[svc.State]
	if !ok {
		f.t.Fatalf("unknown service state %q", svc.State)
	}
	f.runtime(r, func(in *worldInstance) {
		in.units = append(in.units, worldUnit{name: name, description: svc.Description, active: state[0], sub: state[1], failing: svc.FailsToStart})
		in.unitFiles = append(in.unitFiles, name)
	})
	return name
}

func (f *incusFixture) Progress(r runtimedriver.Ref, name string) {
	f.runtime(r, func(in *worldInstance) {
		for i := range in.units {
			if in.units[i].name == name {
				in.units[i].settle()
			}
		}
	})
}

func (f *incusFixture) SetLogs(r runtimedriver.Ref, name string, entries []string) {
	f.runtime(r, func(in *worldInstance) {
		for i := range in.units {
			if in.units[i].name == name {
				in.units[i].logs = strings.Join(entries, "\n") + "\n"
			}
		}
	})
}

func (f *incusFixture) Reached(r runtimedriver.Ref) []string {
	var reached []string
	f.runtime(r, func(in *worldInstance) { reached = slices.Clone(in.reached) })
	return reached
}

func (f *incusFixture) BreakManager(r runtimedriver.Ref) {
	f.runtime(r, func(in *worldInstance) { in.managerBroken = true })
}

func TestConformanceFixtureRestartLeavesTheOldDriverNoReach(t *testing.T) {
	f := newIncusFixture(t).(*incusFixture)
	s := f.Session(conformance.SessionOptions{})
	old := f.Driver()
	created, err := old.Create(t.Context(), s, "", func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	f.Restart()
	if _, err := old.Start(t.Context(), s); err == nil || f.Transitions(s.Ref) != 0 {
		t.Fatalf("the old driver still starts runtimes: %v, %d transitions", err, f.Transitions(s.Ref))
	}
	if got, err := f.Driver().Inspect(t.Context(), s); err != nil || got.Incarnation != created.Incarnation {
		t.Fatalf("the new driver does not see the runtime: %+v %v", got, err)
	}
}
