package protocol_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/runtimedriver/conformance"
	"github.com/lgvo/p.ai/internal/runtimedriver/memory"
	"github.com/lgvo/p.ai/internal/runtimedriver/protocol"
)

var capabilities = runtimedriver.Capabilities{
	ImageBuilder:    "memory/1",
	Networks:        []string{runtimedriver.NetworkNone, runtimedriver.NetworkPublicEgress},
	EgressAddresses: 4,
	Services:        true,
}

const capacity = 64

// The conformance suite holds over the protocol: the in-memory driver
// behind a server, and the client in front of it.
func TestConformance(t *testing.T) {
	conformance.Run(t, conformance.AllGroups, newFixture(capabilities))
}

func TestConformanceWithoutServices(t *testing.T) {
	without := capabilities
	without.Services = false
	conformance.Run(t, []conformance.Group{conformance.Services}, newFixture(without))
}

// pair is a connected pair of Unix stream sockets: core's end and the
// driver's.
func pair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	conns := make([]*net.UnixConn, 2)
	for i, fd := range fds {
		f := os.NewFile(uintptr(fd), "pair")
		c, err := net.FileConn(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		conns[i] = c.(*net.UnixConn)
	}
	return conns[0], conns[1]
}

// served is a driver served on one end of a socket pair, with a client
// connected to it on the other.
type served struct {
	client *protocol.Client
	driver *net.UnixConn // the driver's end, to lose the connection with
	done   chan error    // Serve's return
}

// serve serves d with hello h and connects a client.
func serve(t *testing.T, d runtimedriver.Driver, h protocol.Hello) served {
	t.Helper()
	core, driverEnd := pair(t)
	s := served{driver: driverEnd, done: make(chan error, 1)}
	go func() {
		s.done <- protocol.Serve(context.Background(), driverEnd, func(context.Context, protocol.Hello) (runtimedriver.Driver, error) { return d, nil })
	}()
	client, err := protocol.Connect(t.Context(), core, h)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	s.client = client
	t.Cleanup(func() { client.Close() })
	return s
}

var anyHello = protocol.Hello{Config: json.RawMessage(`{}`)}

// fixture is a memory World with one memory driver over it, served over
// the protocol. Restart kills the driver and its connection, as the death
// of its process would, and serves a new driver over the same World.
type fixture struct {
	t      *testing.T
	world  *memory.World
	driver *memory.Driver
	served served
	next   int
}

func newFixture(c runtimedriver.Capabilities) func(*testing.T) conformance.Fixture {
	return func(t *testing.T) conformance.Fixture {
		f := &fixture{t: t, world: memory.NewWorld(memory.Config{Capacity: capacity, Capabilities: c})}
		f.connect()
		t.Cleanup(f.kill)
		return f
	}
}

func (f *fixture) connect() {
	f.driver = memory.NewDriver(f.world)
	f.served = serve(f.t, f.driver, anyHello)
}

func (f *fixture) kill() {
	f.driver.Kill()
	f.served.driver.Close()
	<-f.served.done
}

func (f *fixture) Driver() runtimedriver.Driver { return f.served.client }

func (f *fixture) Restart() {
	f.kill()
	f.connect()
}

func (f *fixture) Session(o conformance.SessionOptions) runtimedriver.Session {
	f.next++
	asset := []byte("#!/bin/sh\nexec adapter\n")
	sum := sha256.Sum256(asset)
	s := runtimedriver.Session{
		Ref:        runtimedriver.Ref{Kind: runtimedriver.KindSession, ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", f.next)},
		Labels:     runtimedriver.Labels{runtimedriver.LabelProject: "app"},
		Image:      f.world.BaseImage(),
		Command:    []string{"/bin/sh", "-l"},
		Workspace:  runtimedriver.Workspace{Repository: "app", Branch: fmt.Sprintf("p/session-%d", f.next)},
		GitKey:     []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nfixture\n-----END OPENSSH PRIVATE KEY-----\n"),
		GitHostKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFixtureFixtureFixtureFixtureFixtureFixture p",
		Endpoints:  f.t.TempDir(),
		Assets:     []runtimedriver.Asset{{Role: "p-codex-adapter", Mode: 0o555, Data: asset, SHA256: hex.EncodeToString(sum[:])}},
	}
	if o.Grant {
		dir := f.t.TempDir()
		info, err := os.Stat(dir)
		if err != nil {
			f.t.Fatal(err)
		}
		st := info.Sys().(*syscall.Stat_t)
		s.Grants = []runtimedriver.Grant{{Name: "data", Source: dir, Type: "directory", Access: "read-write",
			Device: uint64(st.Dev), Inode: st.Ino, OwnerUID: st.Uid, OwnerGID: st.Gid}}
	}
	if o.PublicEgress {
		s.Network.PublicIPv4 = fmt.Sprintf("192.0.2.%d", f.next)
	}
	return s
}

func (f *fixture) Sessions() conformance.SessionControls    { return f }
func (f *fixture) Recovery() conformance.RecoveryControls   { return f }
func (f *fixture) Deletion() conformance.DeletionControls   { return f }
func (f *fixture) Inventory() conformance.InventoryControls { return f }
func (f *fixture) Workspace() conformance.WorkspaceControls { return f }
func (f *fixture) Terminals() conformance.TerminalControls  { return f }
func (f *fixture) Services() conformance.ServiceControls    { return f }
func (f *fixture) Images() conformance.ImageControls        { return f }

func (f *fixture) Initializations(r runtimedriver.Ref) int { return f.world.Initializations(r) }
func (f *fixture) Transitions(r runtimedriver.Ref) int     { return f.world.Transitions(r) }
func (f *fixture) RenameAway(r runtimedriver.Ref)          { f.world.RenameAway(r) }

func (f *fixture) Drift(r runtimedriver.Ref, d conformance.Drift) {
	f.world.Drift(r, map[conformance.Drift]memory.Drift{
		conformance.DriftLabels:  memory.DriftLabels,
		conformance.DriftImage:   memory.DriftImage,
		conformance.DriftDevices: memory.DriftDevices,
	}[d])
}

func (f *fixture) FailHostOnNextStart(r runtimedriver.Ref, diagnostic string) {
	f.world.FailHostOnNextStart(r, diagnostic)
}

func (f *fixture) UnreadyHost(r runtimedriver.Ref, diagnostic string) {
	f.world.UnreadyHost(r, diagnostic)
}

func (f *fixture) PublicAddress(r runtimedriver.Ref) string { return f.world.PublicAddress(r) }

func (f *fixture) WriteRootFile(r runtimedriver.Ref, path string, data []byte) {
	f.world.WriteRootFile(r, path, data)
}

func (f *fixture) ReadRootFile(r runtimedriver.Ref, path string) ([]byte, bool) {
	return f.world.ReadRootFile(r, path)
}

func (f *fixture) DieInCreate(p conformance.CreatePoint) <-chan struct{} {
	return f.world.DieInCreate(map[conformance.CreatePoint]memory.CreatePoint{
		conformance.AfterInit:               memory.AfterInit,
		conformance.BetweenGuestWrites:      memory.BetweenGuestWrites,
		conformance.DuringGuestWrite:        memory.DuringGuestWrite,
		conformance.DuringPartialGuestWrite: memory.DuringPartialGuestWrite,
	}[p])
}

func (f *fixture) DelayCreate() func()             { return f.world.Delay(runtimedriver.EffectCreate).Land }
func (f *fixture) Hold(r runtimedriver.Ref) func() { return f.world.Hold(r) }

func (f *fixture) DeletedWhileRunning(r runtimedriver.Ref) bool {
	return f.world.DeletedWhileRunning(r)
}
func (f *fixture) DelayDelete() func() { return f.world.Delay(runtimedriver.EffectDelete).Land }

func (f *fixture) Capacity() int                 { return capacity }
func (f *fixture) AddUnlabeled() string          { return f.world.AddUnlabeled() }
func (f *fixture) Terminate(r runtimedriver.Ref) { f.world.Terminate(r) }
func (f *fixture) PartialInventory()             { f.world.PartialInventory() }

func (f *fixture) AddOtherInstance(r runtimedriver.Ref, labels runtimedriver.Labels) string {
	return f.world.AddOtherInstance(r, labels)
}

func (f *fixture) Symlink(r runtimedriver.Ref, path, target string) { f.world.Symlink(r, path, target) }
func (f *fixture) ForeignMount(r runtimedriver.Ref, guestPath string) {
	f.world.ForeignMount(r, guestPath)
}
func (f *fixture) SetRoot(r runtimedriver.Ref, uid, gid int, mode uint32) {
	f.world.SetRoot(r, uid, gid, mode)
}

func (f *fixture) Markers(s runtimedriver.Session) []byte {
	marker := []byte("a marker outside the private root\n")
	dirs := []string{s.Endpoints}
	for _, g := range s.Grants {
		dirs = append(dirs, g.Source)
	}
	for _, dir := range dirs {
		if err := os.WriteFile(filepath.Join(dir, "marker"), marker, 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
	return marker
}

func (f *fixture) Guest(id string) conformance.GuestTerminal { return f.world.Terminal(id) }

func (f *fixture) AddService(r runtimedriver.Ref, svc conformance.NativeService) string {
	return f.world.AddService(r, svc.Base, svc.Description, svc.State, svc.FailsToStart)
}

func (f *fixture) Progress(r runtimedriver.Ref, name string) { f.world.Progress(r, name) }

func (f *fixture) SetLogs(r runtimedriver.Ref, name string, entries []string) {
	f.world.SetLogs(r, name, entries)
}

func (f *fixture) Reached(r runtimedriver.Ref) []string { return f.world.Reached(r) }
func (f *fixture) BreakManager(r runtimedriver.Ref)     { f.world.BreakServiceManager(r) }

func (f *fixture) Build() runtimedriver.Build {
	f.next++
	id := fmt.Sprintf("00000000-0000-4000-9000-%012d", f.next)
	commit := fmt.Sprintf("%040x", f.next)
	return runtimedriver.Build{
		Ref: runtimedriver.Ref{Kind: runtimedriver.KindBuild, ID: id},
		Labels: runtimedriver.Labels{
			runtimedriver.LabelProject: "app", runtimedriver.LabelSourceCommit: commit,
			runtimedriver.LabelBaseImage: string(f.world.BaseImage()), runtimedriver.LabelSystem: "x86_64-linux",
			runtimedriver.LabelBuilderVersion: capabilities.ImageBuilder, runtimedriver.LabelBuild: id,
		},
		Source: runtimedriver.SourceExport{Bundle: filepath.Join(f.t.TempDir(), "source.bundle"), Ref: "refs/heads/main", Commit: commit},
	}
}

func (f *fixture) DelayBuild() func() { return f.world.Delay(runtimedriver.EffectBuild).Land }

func (f *fixture) AddOtherInstanceImage() runtimedriver.ImageRef {
	return f.world.AddOtherInstanceImage()
}
