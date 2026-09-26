package runtimeincus

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

const testUUID = "550e8400-e29b-41d4-a716-446655440000"
const testImage = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type runtimeGrantTestAncestor struct{ os.FileInfo }

func (runtimeGrantTestAncestor) Mode() os.FileMode { return os.ModeDir | 0555 }
func (runtimeGrantTestAncestor) IsDir() bool       { return true }
func (runtimeGrantTestAncestor) Sys() any          { return &syscall.Stat_t{Uid: 0, Gid: 0, Ino: 1} }

func runtimeGrantTestLstat(root string) func(string) (os.FileInfo, error) {
	return func(path string) (os.FileInfo, error) {
		info, err := os.Lstat(path)
		if err != nil || path == root || strings.HasPrefix(path, root+string(os.PathSeparator)) {
			return info, err
		}
		return runtimeGrantTestAncestor{info}, nil
	}
}

func testSession() Session {
	return Session{InstanceUUID: "11111111-1111-4111-8111-111111111111", SessionUUID: testUUID, ProjectPath: "/source/repo.git", ContractVersion: "1", ImageFingerprint: testImage}
}

type fakeIncus struct {
	instance              bool
	status                string
	altered               bool
	unsafeProfile         bool
	unsafeExpandedDevice  bool
	unsafeExpandedConfig  bool
	diskPaths             string
	failCreateAfterEffect bool
	endpointSource        string
	endpointDeviceAdded   bool
	grant                 *FilesystemGrant
	grantDeviceAdded      bool
	alterGrant            bool
	staleGrantLists       int
	imageArchitecture     string
	socket                string
	mutations             []string
}

func (f *fakeIncus) run(_ context.Context, _ string, argv, env []string) ([]byte, error) {
	socket := f.socket
	if socket == "" {
		socket = "/var/lib/incus/unix.socket.user"
	}
	if len(argv) < 4 || !slices.Equal(argv[:3], []string{"--force-local", "--project", "user-1000"}) || !slices.Contains(env, "INCUS_SOCKET="+socket) {
		return nil, errors.New("unconfined invocation")
	}
	args := argv[3:]
	switch args[0] {
	case "project":
		paths := f.diskPaths
		if paths == "" {
			paths = "/var/lib/p-vm/endpoints"
		}
		return json.Marshal([]projectJSON{{Name: "user-1000", Config: map[string]string{"restricted": "true", "restricted.containers.privilege": "isolated", "restricted.containers.nesting": "block", "restricted.containers.lowlevel": "block", "restricted.devices.nic": "block", "restricted.devices.gpu": "block", "restricted.devices.disk": "allow", "features.profiles": "true", "features.networks": "false", "restricted.devices.disk.paths": paths}}})
	case "profile":
		devices := map[string]map[string]string{"root": {"type": "disk", "path": "/", "pool": "default"}}
		if f.unsafeProfile {
			devices["eth0"] = map[string]string{"type": "nic"}
		}
		return json.Marshal([]profileJSON{{Name: "default", Config: map[string]string{"security.idmap.isolated": "true"}, Devices: devices}})
	case "image":
		arch := f.imageArchitecture
		if arch == "" {
			arch = "x86_64"
		}
		return json.Marshal([]map[string]string{{"fingerprint": testImage, "type": "container", "architecture": arch}})
	case "list":
		if !f.instance {
			return []byte(`[]`), nil
		}
		s := testSession()
		config := map[string]string{"user.p.instance_uuid": s.InstanceUUID, "user.p.session_uuid": s.SessionUUID, "user.p.project_path": s.ProjectPath, "user.p.contract_version": s.ContractVersion, "user.p.image_fingerprint": s.ImageFingerprint}
		config["volatile.base_image"] = s.ImageFingerprint
		if f.altered {
			config["user.p.session_uuid"] = "different"
		}
		expanded := map[string]string{}
		for key, value := range config {
			expanded[key] = value
		}
		expanded["security.idmap.isolated"] = "true"
		if f.unsafeExpandedConfig {
			expanded["security.privileged"] = "true"
		}
		devices := map[string]map[string]string{"root": {"type": "disk", "path": "/", "pool": "default"}}
		instanceDevices := map[string]map[string]string{}
		if f.endpointDeviceAdded {
			endpoint := map[string]string{"type": "disk", "path": endpointStagingPath, "source": f.endpointSource, "readonly": "true", "propagation": "private", "shift": "false"}
			instanceDevices["p-endpoint"] = endpoint
			devices["p-endpoint"] = endpoint
		}
		showGrant := f.grant != nil && f.grantDeviceAdded && f.staleGrantLists == 0
		if f.grantDeviceAdded && f.staleGrantLists > 0 {
			f.staleGrantLists--
		}
		if showGrant {
			device := grantDevice(*f.grant)
			if f.alterGrant {
				device["readonly"] = "false"
			}
			instanceDevices[grantDeviceName(f.grant.Name)] = device
			devices[grantDeviceName(f.grant.Name)] = device
		}
		if f.unsafeExpandedDevice {
			devices["eth0"] = map[string]string{"type": "nic", "network": "incusbr0"}
		}
		return json.Marshal([]instanceJSON{{Name: "p-" + testUUID, Type: "container", Status: f.status, Config: config, ExpandedConfig: expanded, Devices: instanceDevices, ExpandedDevices: devices, Profiles: []string{"default"}}})
	case "config":
		if f.grant != nil && len(args) > 4 && args[4] == grantDeviceName(f.grant.Name) {
			device := grantDevice(*f.grant)
			want := []string{"config", "device", "add", "p-" + testUUID, grantDeviceName(f.grant.Name), "disk"}
			for _, key := range []string{"source", "path", "readonly", "propagation", "shift", "recursive", "raw.mount.options"} {
				if value, ok := device[key]; ok {
					want = append(want, key+"="+value)
				}
			}
			if !slices.Equal(args, want) {
				return nil, errors.New("unsafe grant device command")
			}
			f.grantDeviceAdded = true
			f.mutations = append(f.mutations, "grant")
			return nil, nil
		}
		want := []string{"config", "device", "add", "p-" + testUUID, "p-endpoint", "disk", "source=" + f.endpointSource, "path=" + endpointStagingPath, "readonly=true", "propagation=private", "shift=false"}
		if f.endpointSource == "" || !slices.Equal(args, want) {
			return nil, errors.New("unsafe endpoint device command")
		}
		f.endpointDeviceAdded = true
		f.mutations = append(f.mutations, "endpoint")
		return nil, nil
	case "init":
		f.mutations = append(f.mutations, "init")
		f.instance = true
		f.status = "Stopped"
		if f.failCreateAfterEffect {
			return nil, errors.New("connection dropped")
		}
		return nil, nil
	case "start":
		f.mutations = append(f.mutations, "start")
		f.status = "Running"
		return nil, nil
	case "stop":
		f.mutations = append(f.mutations, "stop")
		f.status = "Stopped"
		return nil, nil
	case "delete":
		f.mutations = append(f.mutations, "delete")
		f.instance = false
		return nil, nil
	}
	return nil, errors.New("unexpected command")
}

func TestFileGrantOmitsRejectedRecursiveOption(t *testing.T) {
	grant := FilesystemGrant{Name: "notice", Source: "/var/lib/p-vm/grants/pdev/notice", Type: "file", Access: "read-only", Device: 1, Inode: 2}
	device := grantDevice(grant)
	if _, ok := device["recursive"]; ok {
		t.Fatal("file grant supplied recursive option")
	}
	if !validGrantDevice(device, grant) {
		t.Fatal("exact file device rejected")
	}
	changed := maps.Clone(device)
	changed["recursive"] = "false"
	if validGrantDevice(changed, grant) {
		t.Fatal("file device with unsupported recursive option accepted")
	}
	want := []string{"config", "device", "add", "p-" + testUUID, "p-grant-notice", "disk",
		"source=/var/lib/p-vm/grants/pdev/notice", "path=/mnt/p/notice", "readonly=true", "propagation=private", "shift=false", "raw.mount.options=noexec,nosuid,nodev"}
	if got := grantDeviceAddArgs("p-"+testUUID, grant); !slices.Equal(got, want) {
		t.Fatalf("file add argv: %v", got)
	}
	f := &fakeIncus{instance: true, status: "Stopped", grant: &grant, grantDeviceAdded: true, diskPaths: "/var/lib/p-vm/grants"}
	b := fakeBackend(f)
	b.config.DiskSourceCeilings = []string{"/var/lib/p-vm/grants"}
	s := testSession()
	s.Grants = []FilesystemGrant{grant}
	if observed, err := b.InspectCreated(context.Background(), s); err != nil || !observed.MountedGrants[grant.Name] {
		t.Fatalf("exact file observation: %+v %v", observed, err)
	}
}

func fakeBackend(f *fakeIncus) *Backend {
	return &Backend{config: Config{Binary: "/bin/incus", UserSocket: "/var/lib/incus/unix.socket.user", Project: "user-1000", DiskSourceCeilings: []string{"/var/lib/p-vm/endpoints"}}, run: f.run, validate: func(Config) error { return nil }}
}

func TestLifecycleUsesObservedIncusState(t *testing.T) {
	f := &fakeIncus{failCreateAfterEffect: true}
	b := fakeBackend(f)
	s := testSession()
	ctx := context.Background()
	got, err := b.Create(ctx, s)
	if err != nil || !got.Exists || got.Status != "Stopped" {
		t.Fatalf("create recovery: %+v, %v", got, err)
	}
	if _, err = b.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	got, err = b.Start(ctx, s)
	if err != nil || got.Status != "Running" {
		t.Fatalf("start: %+v, %v", got, err)
	}
	got, err = b.Stop(ctx, s)
	if err != nil || got.Status != "Stopped" {
		t.Fatalf("stop: %+v, %v", got, err)
	}
	got, err = b.Delete(ctx, s)
	if err != nil || got.Exists {
		t.Fatalf("delete: %+v, %v", got, err)
	}
	if !slices.Equal(f.mutations, []string{"init", "start", "stop", "delete"}) {
		t.Fatalf("unexpected mutations: %v", f.mutations)
	}
}

func TestExactFilesystemGrantDeviceAndSourceSwap(t *testing.T) {
	root := t.TempDir()
	lstat := runtimeGrantTestLstat(root)
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(source)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	localGrant := FilesystemGrant{Name: "data", Source: source, Type: "directory", Access: "read-only", Device: uint64(stat.Dev), Inode: stat.Ino, OwnerUID: stat.Uid, OwnerGID: stat.Gid}
	if err := inspectRuntimeGrantSourceWith(localGrant, lstat); err != nil {
		t.Fatal(err)
	}
	grant := localGrant
	grant.Source = "/var/lib/p-vm/grants/pdev/step36/data"
	f := &fakeIncus{instance: true, status: "Stopped", grant: &grant, grantDeviceAdded: true, diskPaths: "/var/lib/p-vm/grants"}
	b := fakeBackend(f)
	b.config.DiskSourceCeilings = []string{"/var/lib/p-vm/grants"}
	s := testSession()
	s.Grants = []FilesystemGrant{grant}
	created, err := b.Inspect(context.Background(), s)
	if err != nil || !created.GrantsMounted {
		t.Fatalf("exact grant not observed: %+v %v", created, err)
	}
	f.alterGrant = true
	if _, err := b.Inspect(context.Background(), s); err == nil {
		t.Fatal("changed readonly grant accepted")
	}
	f.alterGrant = false
	if err := os.Rename(source, source+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := inspectRuntimeGrantSourceWith(localGrant, lstat); err == nil {
		t.Fatal("swapped local source accepted")
	}
	if _, err := b.Start(context.Background(), s); err == nil {
		t.Fatal("swapped grant source started")
	}
	if len(f.mutations) != 0 {
		t.Fatalf("source swap caused mutation: %v", f.mutations)
	}
}

func TestGrantPostMutationObservationRequiresExactList(t *testing.T) {
	grant := FilesystemGrant{Name: "data", Source: "/var/lib/p-vm/grants/pdev/data", Type: "directory", Access: "read-only", Device: 1, Inode: 2}
	f := &fakeIncus{instance: true, status: "Stopped", grant: &grant, grantDeviceAdded: true, staleGrantLists: 1, diskPaths: "/var/lib/p-vm/grants"}
	b := fakeBackend(f)
	b.config.DiskSourceCeilings = []string{"/var/lib/p-vm/grants"}
	s := testSession()
	s.Grants = []FilesystemGrant{grant}
	first, err := b.InspectCreated(context.Background(), s)
	if err != nil || first.MountedGrants["data"] {
		t.Fatalf("stale first listing falsely accepted: %+v %v", first, err)
	}
	second, err := b.InspectCreated(context.Background(), s)
	if err != nil || !second.MountedGrants["data"] {
		t.Fatalf("later exact listing not recognized: %+v %v", second, err)
	}
}

func TestConfinementAndIdentityDenials(t *testing.T) {
	ctx := context.Background()
	s := testSession()
	f := &fakeIncus{unsafeProfile: true}
	_, err := fakeBackend(f).Create(ctx, s)
	if err == nil || len(f.mutations) != 0 {
		t.Fatalf("unsafe profile accepted: %v", err)
	}
	f = &fakeIncus{instance: true, status: "Stopped", altered: true}
	_, err = fakeBackend(f).Delete(ctx, s)
	if err == nil || len(f.mutations) != 0 {
		t.Fatalf("foreign identity deleted: %v", err)
	}
	for _, test := range []struct {
		name    string
		fixture fakeIncus
	}{
		{"inherited NIC", fakeIncus{instance: true, status: "Stopped", unsafeExpandedDevice: true}},
		{"inherited privilege", fakeIncus{instance: true, status: "Stopped", unsafeExpandedConfig: true}},
	} {
		f = &test.fixture
		_, err = fakeBackend(f).Delete(ctx, s)
		if err == nil || len(f.mutations) != 0 {
			t.Fatalf("%s reached mutation: %v", test.name, err)
		}
	}
	for _, paths := range []string{"/", "/etc,/var/lib/p-vm/endpoints", "/var/lib/p-vm/endpoints,/srv/extra"} {
		f = &fakeIncus{diskPaths: paths}
		_, err = fakeBackend(f).Create(ctx, s)
		if err == nil || len(f.mutations) != 0 {
			t.Fatalf("broad or undeclared disk ceiling %q accepted: %v", paths, err)
		}
	}
	f = &fakeIncus{}
	s.ImageFingerprint = "alias"
	_, err = fakeBackend(f).Create(ctx, s)
	if err == nil || !strings.Contains(err.Error(), "fingerprint") || len(f.mutations) != 0 {
		t.Fatalf("image alias accepted: %v", err)
	}
}

func TestDiskCeilingConfigurationAndAncestors(t *testing.T) {
	if err := validateDiskCeilings(Config{}); err == nil {
		t.Fatal("empty disk ceiling accepted")
	}
	for _, path := range []string{"/", "/etc", "/var", "/var/lib/incus", "/home/user", "/tmp/data"} {
		c := Config{DiskSourceCeilings: []string{path}}
		if err := validateDiskCeilings(c); err == nil {
			t.Fatalf("sensitive ceiling %q accepted", path)
		}
	}
	good := Config{EndpointPrefix: "/var/lib/p-vm/endpoints/pdev", DiskSourceCeilings: []string{"/var/lib/p-vm/endpoints"}}
	if err := validateDiskCeilings(good); err != nil {
		t.Fatalf("safe ceiling refused: %v", err)
	}
	base, err := os.MkdirTemp("", "ancestor-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	base, err = filepath.Abs(base)
	if err != nil {
		t.Fatal(err)
	}
	rootInfo, err := os.Stat("/")
	if err != nil {
		t.Fatal(err)
	}
	rootUID := fileUID(rootInfo)
	leaf := filepath.Join(base, "leaf")
	if err = os.WriteFile(leaf, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err = validateAncestorsWithRoot(leaf, rootUID); err != nil {
		t.Fatalf("trusted ancestor refused: %v", err)
	}
	if err = os.Chmod(base, 0777); err != nil {
		t.Fatal(err)
	}
	if err = validateAncestorsWithRoot(leaf, rootUID); err == nil {
		t.Fatal("writable ancestor accepted")
	}
	if err = os.Chmod(base, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(leaf, filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	if err = validateAncestorsWithRoot(filepath.Join(base, "link"), rootUID); err == nil {
		t.Fatal("symbolic-link endpoint accepted")
	}
}

func TestNixStoreAncestorExceptionIsExecutableOnly(t *testing.T) {
	mode := os.ModeDir | os.ModeSticky | 0775
	if !allowWritableAncestor("/nix/store", mode, 0, 0, true) {
		t.Fatal("root-owned sticky Nix store refused for executable")
	}
	for _, tc := range []struct {
		path       string
		mode       os.FileMode
		uid        uint32
		executable bool
	}{
		{"/nix/store", mode, 0, false},
		{"/nix/store", os.ModeDir | 0775, 0, true},
		{"/nix/store", mode, 1000, true},
		{"/other/store", mode, 0, true},
		{"/nix/store", os.ModeDir | os.ModeSticky | 0777, 0, true},
	} {
		if allowWritableAncestor(tc.path, tc.mode, tc.uid, 0, tc.executable) {
			t.Fatalf("unsafe Nix store exception accepted: %+v", tc)
		}
	}
	if !allowWritableAncestor("/tmp", os.ModeDir|os.ModeSticky|0777, 0, 0, false) {
		t.Fatal("root-owned sticky tmp refused")
	}
}

func TestEffectiveSecurityAndEndpointDeviceOptions(t *testing.T) {
	if safeSecurity(map[string]string{"security.unknown": ""}) {
		t.Fatal("unknown empty security key accepted")
	}
	source := "/var/lib/p-vm/endpoints/pdev/session"
	d := map[string]string{"type": "disk", "path": endpointStagingPath, "source": source, "readonly": "true", "propagation": "private", "shift": "false"}
	if !validEndpointDevice(d, source) {
		t.Fatal("fixed endpoint device refused")
	}
	for _, target := range []string{"/run/p", "/opt/p/other", "/tmp/endpoints"} {
		d["path"] = target
		if validEndpointDevice(d, source) {
			t.Fatalf("unsafe endpoint target %q accepted", target)
		}
	}
	d["path"] = endpointStagingPath
	d["shift"] = "true"
	if validEndpointDevice(d, source) {
		t.Fatal("shifted endpoint device accepted")
	}
	d["shift"] = "false"
	d["raw.mount.options"] = "bind"
	if validEndpointDevice(d, source) {
		t.Fatal("extra device option accepted")
	}
}

func TestEndpointBoundaryBlocksChangedSocket(t *testing.T) {
	prefix, err := os.MkdirTemp("", "endpoint-prefix-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(prefix)
	prefix, err = filepath.Abs(prefix)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(prefix, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(prefix, "session")
	if err = os.Mkdir(source, 0755); err != nil {
		t.Fatal(err)
	}
	var listeners []net.Listener
	for _, name := range []string{"session.sock", "git.sock"} {
		path := filepath.Join(source, name)
		listener, e := net.Listen("unix", path)
		if e != nil {
			if errors.Is(e, syscall.EPERM) {
				t.Skip("sandbox disallows Unix socket creation")
			}
			t.Fatal(e)
		}
		listeners = append(listeners, listener)
		if e = os.Chmod(path, 0666); e != nil {
			t.Fatal(e)
		}
	}
	defer func() {
		for _, listener := range listeners {
			listener.Close()
		}
	}()
	f := &fakeIncus{instance: true, status: "Stopped"}
	b := fakeBackend(f)
	b.config.EndpointPrefix = prefix
	s := testSession()
	s.EndpointSource = source
	if err = b.validateEndpoint(source); err != nil {
		t.Fatalf("valid endpoint: %v", err)
	}
	f.endpointSource = source
	if _, err := (Scoped{Backend: b, Session: s}).Inspect(context.Background()); err == nil {
		t.Fatal("ordinary scoped inspection accepted incomplete endpoint")
	}
	pending, err := (Scoped{Backend: b, Session: s}).InspectCreated(context.Background())
	if err != nil || !pending.Exists || pending.Status != "Stopped" {
		t.Fatalf("exact pending instance observation: %+v %v", pending, err)
	}
	// The native Create path delegates this exact stopped repair to
	// ensureEndpoint after its confinement check; this fixture exercises the
	// endpoint mutation under a real local Unix socket without changing the
	// production disk-ceiling policy to permit /tmp.
	got, err := b.ensureEndpoint(context.Background(), s, Observation{Status: "Stopped"})
	if err != nil || !got.EndpointMounted || !slices.Equal(f.mutations, []string{"endpoint"}) {
		t.Fatalf("fixed endpoint device: %+v, %v, %v", got, err, f.mutations)
	}
	f.mutations = nil
	if err = os.Chmod(filepath.Join(source, "git.sock"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Start(context.Background(), s); err == nil || len(f.mutations) != 0 {
		t.Fatalf("changed endpoint reached Incus: %v, %v", err, f.mutations)
	}
	if err = os.Chmod(filepath.Join(source, "git.sock"), 0666); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(source, filepath.Join(prefix, "alias")); err != nil {
		t.Fatal(err)
	}
	if err = b.validateEndpoint(filepath.Join(prefix, "alias")); err == nil {
		t.Fatal("symlink endpoint accepted")
	}
}
