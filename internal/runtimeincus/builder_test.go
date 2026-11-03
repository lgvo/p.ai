package runtimeincus

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func testBuilder() Builder {
	return Builder{RequestUUID: testUUID, ProjectPath: "team/repo", CommitOID: strings.Repeat("a", 40), TreeOID: strings.Repeat("b", 40), BaseImageFingerprint: testImage, ContractVersion: "1"}
}

type fakeBuilderIncus struct {
	base             fakeIncus
	request          Builder
	exists           bool
	status           string
	rootLimit        bool
	foreign          bool
	unsafeNIC        bool
	unsafeEnv        bool
	poolDriver       string
	poolStatus       string
	rootPool         string
	hostArchitecture string
	socket           string
	public           *PublicEgressConfig // the host's proven public-egress substrate, if any
	publicNIC        map[string]string
	mutations        []string
}

func (f *fakeBuilderIncus) run(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
	socket := f.socket
	if socket == "" {
		socket = "/var/lib/incus/unix.socket.user"
	}
	if len(argv) < 4 || !slices.Equal(argv[:3], []string{"--force-local", "--project", "user-1000"}) || !slices.Contains(env, "INCUS_SOCKET="+socket) {
		return nil, errors.New("unconfined builder invocation")
	}
	args := argv[3:]
	switch args[0] {
	case "project":
		if f.public == nil {
			return f.base.run(ctx, binary, argv, env)
		}
		return json.Marshal([]projectJSON{{Name: "user-1000", Config: map[string]string{"restricted": "true", "restricted.containers.privilege": "isolated", "restricted.containers.nesting": "block", "restricted.containers.lowlevel": "block", "restricted.devices.nic": "managed", "restricted.networks.access": f.public.Network, "restricted.devices.gpu": "block", "restricted.devices.disk": "allow", "features.profiles": "true", "features.networks": "false", "restricted.devices.disk.paths": "/var/lib/p-vm/endpoints"}}})
	case "network":
		if f.public == nil || !slices.Equal(args, []string{"network", "list", "--format", "json"}) {
			return nil, errors.New("unexpected network query")
		}
		return json.Marshal([]egressNetworkJSON{{Name: f.public.Network, Project: "default", Type: "bridge", Status: "Created", Managed: true, Config: map[string]string{}}})
	case "profile", "image":
		return f.base.run(ctx, binary, argv, env)
	case "storage":
		if !slices.Equal(args, []string{"storage", "list", "--format", "json"}) {
			return nil, errors.New("unexpected storage query")
		}
		driver, status := f.poolDriver, f.poolStatus
		if driver == "" {
			driver = "btrfs"
		}
		if status == "" {
			status = "Created"
		}
		return json.Marshal([]map[string]string{{"name": "builders", "driver": driver, "status": status}})
	case "list":
		if !f.exists {
			return []byte("[]"), nil
		}
		r := f.request
		config := map[string]string{
			"user.p.builder_request_uuid": r.RequestUUID, "user.p.builder_project_path": r.ProjectPath,
			"user.p.builder_commit_oid": r.CommitOID, "user.p.builder_tree_oid": r.TreeOID,
			"user.p.builder_contract_version": r.ContractVersion, "user.p.builder_base_image": r.BaseImageFingerprint,
			"volatile.base_image": r.BaseImageFingerprint, "limits.cpu": builderCPU,
			"limits.memory": builderMemory, "limits.processes": builderProcesses,
		}
		if f.foreign {
			config["user.p.builder_request_uuid"] = "foreign"
		}
		expanded := map[string]string{}
		for k, v := range config {
			expanded[k] = v
		}
		expanded["security.idmap.isolated"] = "true"
		if f.unsafeEnv {
			expanded["environment.NIX_CONFIG"] = "accept-flake-config = true"
		}
		pool := f.rootPool
		if pool == "" {
			pool = "builders"
		}
		root := map[string]string{"type": "disk", "path": "/", "pool": pool}
		devices := map[string]map[string]string{}
		if f.rootLimit {
			root["size"] = builderRootSize
			devices["root"] = map[string]string{"type": "disk", "path": "/", "pool": pool, "size": builderRootSize}
		}
		expandedDevices := map[string]map[string]string{"root": root}
		if f.publicNIC != nil {
			devices[publicNICName] = f.publicNIC
			expandedDevices[publicNICName] = f.publicNIC
		}
		if f.unsafeNIC {
			expandedDevices["eth0"] = map[string]string{"type": "nic"}
		}
		return json.Marshal([]instanceJSON{{Name: builderName(r), Type: "container", Status: f.status, Config: config, ExpandedConfig: expanded, Devices: devices, ExpandedDevices: expandedDevices, Profiles: []string{"default"}}})
	case "init":
		if len(args) < 4 || args[1] != f.request.BaseImageFingerprint || args[2] != builderName(f.request) || !slices.Contains(args, "root,size="+builderRootSize) || !slices.Contains(args, "--storage") || !slices.Contains(args, "builders") || !slices.Contains(args, "security.privileged=false") || !slices.Contains(args, "security.nesting=false") || !slices.Contains(args, "limits.memory="+builderMemory) {
			return nil, errors.New("unsafe builder init")
		}
		f.exists, f.status, f.rootLimit = true, "Stopped", true
		f.mutations = append(f.mutations, "init")
	case "config":
		if len(args) > 6 && slices.Equal(args[:6], []string{"config", "device", "add", builderName(f.request), publicNICName, "nic"}) {
			f.publicNIC = map[string]string{"type": "nic"}
			for _, pair := range args[6:] {
				key, value, ok := strings.Cut(pair, "=")
				if !ok {
					return nil, errors.New("malformed NIC option")
				}
				f.publicNIC[key] = value
			}
			f.mutations = append(f.mutations, "public-nic")
			return nil, nil
		}
		if !slices.Equal(args, []string{"config", "device", "override", builderName(f.request), "root", "size=" + builderRootSize}) {
			return nil, errors.New("unsafe builder root override")
		}
		f.rootLimit = true
		f.mutations = append(f.mutations, "root")
	case "start":
		if args[1] != builderName(f.request) {
			return nil, errors.New("wrong start target")
		}
		f.status = "Running"
		f.mutations = append(f.mutations, "start")
	case "stop":
		if args[1] != builderName(f.request) {
			return nil, errors.New("wrong stop target")
		}
		f.status = "Stopped"
		f.mutations = append(f.mutations, "stop")
	case "delete":
		if args[1] != builderName(f.request) {
			return nil, errors.New("wrong delete target")
		}
		f.exists = false
		f.mutations = append(f.mutations, "delete")
	default:
		return nil, errors.New("unexpected builder command")
	}
	return nil, nil
}

func builderBackend(f *fakeBuilderIncus) *Backend {
	b := fakeBackend(&f.base)
	b.config.BuilderStoragePool = "builders"
	b.run = f.run
	return b
}

// publicBuilderBackend serves a host whose Incus project and live packet
// filters prove the public-egress substrate, as a configured host does.
func publicBuilderBackend(t *testing.T, b *Backend, f *fakeBuilderIncus) *Backend {
	t.Helper()
	c := testEgressSubstrate()
	b.config.PublicEgress, f.public = c, c
	bridge := egressNetworkJSON{Name: c.Network, Project: "default", Type: "bridge", Status: "Created", Managed: true, Config: map[string]string{
		"ipv4.address": c.BridgeIPv4, "ipv4.dhcp": "false", "ipv4.nat": "true", "ipv4.routing": "true", "ipv4.firewall": "true",
		"ipv6.address": "none", "dns.mode": "none", "security.acls": c.ACL,
		"security.acls.default.ingress.action": "reject", "security.acls.default.egress.action": "reject",
	}}
	acl := egressACLJSON{Name: c.ACL, Config: map[string]string{}, Ingress: []egressACLRuleJSON{}, Egress: expectedEgressRules(c)}
	b.proofRun = func(_ context.Context, _ string, argv []string, _ []string) ([]byte, error) {
		if slices.Contains(argv, c.BridgeProofBinary) {
			return json.Marshal(egressPublicProofJSON{Network: bridge, ACL: acl})
		}
		return firewallFixture(t), nil
	}
	return b
}

func TestPublicBuilderGetsExactlyItsSessionAddressNIC(t *testing.T) {
	r := testBuilder()
	r.PublicIPv4 = "10.233.0.11/24"
	f := &fakeBuilderIncus{request: r}
	b := publicBuilderBackend(t, builderBackend(f), f)
	ctx := context.Background()
	got, err := b.CreateBuilder(ctx, r)
	if err != nil || !got.Exists || !got.Ready {
		t.Fatalf("create: %+v %v", got, err)
	}
	want := map[string]string{
		"type": "nic", "name": "eth0", "network": "p-public-v1", "ipv4.address": "10.233.0.11", "ipv6.address": "none",
		"security.acls": "p-public-v1-acl", "security.acls.default.ingress.action": "reject", "security.acls.default.egress.action": "reject",
		"security.mac_filtering": "true", "security.ipv4_filtering": "true", "security.ipv6_filtering": "true",
	}
	if !maps.Equal(f.publicNIC, want) || !slices.Equal(f.mutations, []string{"init", "public-nic"}) {
		t.Fatalf("builder NIC differs from a public session's: %v mutations=%v", f.publicNIC, f.mutations)
	}
	if _, err := b.CreateBuilder(ctx, r); err != nil || len(f.mutations) != 2 {
		t.Fatalf("ready public builder was changed again: %v %v", err, f.mutations)
	}
	offline := r
	offline.PublicIPv4 = ""
	if _, err := b.InspectBuilder(ctx, offline); err == nil {
		t.Fatal("NIC accepted on a builder without public egress")
	}
	f.publicNIC["security.acls"] = ""
	if _, err := b.InspectBuilder(ctx, r); err == nil {
		t.Fatal("public NIC without the egress ACL accepted")
	}
	f.publicNIC["security.acls"] = "p-public-v1-acl"
	unreserved := r
	unreserved.PublicIPv4 = "10.233.0.14/24"
	if _, err := b.InspectBuilder(ctx, unreserved); err == nil {
		t.Fatal("builder address outside the reserved slots accepted")
	}
	f.publicNIC = nil
	if got, err := b.InspectBuilder(ctx, r); err == nil && got.Ready {
		t.Fatal("public builder without its NIC reported ready")
	}
}

func TestBuilderCreateAndVerifiedCleanup(t *testing.T) {
	r := testBuilder()
	f := &fakeBuilderIncus{request: r}
	b := builderBackend(f)
	ctx := context.Background()
	got, err := b.CreateBuilder(ctx, r)
	if err != nil || !got.Exists || !got.Ready || got.Status != "Stopped" {
		t.Fatalf("create: %+v %v", got, err)
	}
	if _, err := b.CreateBuilder(ctx, r); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.mutations, []string{"init"}) {
		t.Fatalf("unexpected creation: %v", f.mutations)
	}
	f.status = "Running"
	f.socket = (&fakeGuest{t: t, instance: builderName(r), systemdUp: true}).serveFiles()
	f.base.socket = f.socket
	b.config.UserSocket = f.socket
	got, err = b.DeleteBuilder(ctx, r)
	if err != nil || got.Exists {
		t.Fatalf("cleanup: %+v %v", got, err)
	}
	got, err = b.DeleteBuilder(ctx, r)
	if err != nil || got.Exists {
		t.Fatalf("idempotent cleanup: %+v %v", got, err)
	}
	if !slices.Equal(f.mutations, []string{"init", "stop", "delete"}) {
		t.Fatalf("unexpected cleanup: %v", f.mutations)
	}
}

func TestBuilderRefusesForeignAndUnsafeInstance(t *testing.T) {
	r := testBuilder()
	for _, tc := range []struct {
		name              string
		foreign, nic, env bool
	}{
		{"foreign", true, false, false}, {"extra NIC", false, true, false}, {"inherited Nix config", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeBuilderIncus{request: r, exists: true, status: "Stopped", rootLimit: true, foreign: tc.foreign, unsafeNIC: tc.nic, unsafeEnv: tc.env}
			b := builderBackend(f)
			if _, err := b.DeleteBuilder(context.Background(), r); err == nil {
				t.Fatal("accepted unowned or unsafe builder")
			}
			if len(f.mutations) != 0 {
				t.Fatalf("mutated unknown builder: %v", f.mutations)
			}
		})
	}
	r.RequestUUID = "bad"
	if _, err := builderBackend(&fakeBuilderIncus{request: testBuilder()}).CreateBuilder(context.Background(), r); err == nil {
		t.Fatal("accepted invalid request")
	}
}

func TestBuilderRequiresProvenPoolBeforeMutation(t *testing.T) {
	r := testBuilder()
	for _, tc := range []struct {
		name, configured, driver, status, rootPool string
	}{
		{name: "no configured pool"},
		{name: "dir silently skips quota", configured: "builders", driver: "dir"},
		{name: "pool not created", configured: "builders", status: "Pending"},
		{name: "wrong effective pool", configured: "builders", rootPool: "default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeBuilderIncus{request: r, poolDriver: tc.driver, poolStatus: tc.status, rootPool: tc.rootPool}
			b := builderBackend(f)
			b.config.BuilderStoragePool = tc.configured
			_, err := b.CreateBuilder(context.Background(), r)
			if err == nil {
				t.Fatal("accepted unproven builder root quota")
			}
			if tc.rootPool == "" && len(f.mutations) != 0 {
				t.Fatalf("mutated before pool proof: %v", f.mutations)
			}
		})
	}
}

func TestBuilderReadlinkPacketPreservesLiteralTarget(t *testing.T) {
	target := []byte("../flake.nix")
	packet := make([]byte, 1+4+4+4+len(target)+4+len(target)+4)
	packet[0] = 104 // SSH_FXP_NAME
	binary.BigEndian.PutUint32(packet[1:5], 1)
	binary.BigEndian.PutUint32(packet[5:9], 1)
	binary.BigEndian.PutUint32(packet[9:13], uint32(len(target)))
	copy(packet[13:], target)
	offset := 13 + len(target)
	binary.BigEndian.PutUint32(packet[offset:offset+4], uint32(len(target)))
	copy(packet[offset+4:], target)
	got, err := parseSFTPReadlink(packet, int64(len(target)))
	if err != nil || !bytes.Equal(got, target) {
		t.Fatalf("literal target: %q %v", got, err)
	}
	if _, err := parseSFTPReadlink(packet, int64(len(target)-1)); err == nil {
		t.Fatal("accepted oversized target")
	}
	packet[5] = 2
	if _, err := parseSFTPReadlink(packet, int64(len(target))); err == nil {
		t.Fatal("accepted wrong count")
	}
}

func TestBuilderSFTPSealsDirectoryAndReadsLiteralLink(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "incus.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var headCount, getCount, setstatCount, readlinkCount int
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/1.0/instances/p-builder-test/sftp" && r.URL.Path != "/1.0/instances/p-builder-test/files" || r.URL.Query().Get("project") != "test" {
			t.Error("unconfined file request")
		}
		if r.Method == http.MethodHead {
			headCount++
			if r.URL.Query().Get("path") != "/opt/p/build/source/link" {
				t.Error("wrong symlink HEAD")
			}
			w.Header().Set("X-Incus-type", "symlink")
			w.Header().Set("X-Incus-uid", "0")
			w.Header().Set("X-Incus-gid", "0")
			w.Header().Set("X-Incus-mode", "0777")
			return
		}
		if r.Header.Get("Upgrade") != "sftp" {
			getCount++
			t.Error("followed symlink with HTTP GET")
			return
		}
		h, ok := w.(http.Hijacker)
		if !ok {
			t.Error("cannot hijack")
			return
		}
		conn, reader, err := h.Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		fmt.Fprint(conn, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: sftp\r\n\r\n")
		init, err := readSFTPPacket(reader)
		if err != nil || init[0] != 1 {
			t.Error("invalid init")
			return
		}
		if err := writePacket(conn, []byte{0, 0, 0, 5, 2, 0, 0, 0, 3}); err != nil {
			t.Error(err)
			return
		}
		request, err := readSFTPPacket(reader)
		if err != nil || len(request) < 13 {
			t.Error("invalid request")
			return
		}
		switch request[0] {
		case 9:
			setstatCount++
			if binary.BigEndian.Uint32(request[5:9]) != uint32(len(builderSource)) || string(request[9:9+len(builderSource)]) != builderSource || binary.BigEndian.Uint32(request[9+len(builderSource):13+len(builderSource)]) != 4 || binary.BigEndian.Uint32(request[13+len(builderSource):17+len(builderSource)]) != 0555 {
				t.Error("unexpected chmod packet")
			}
			_ = writePacket(conn, []byte{0, 0, 0, 9, 101, 0, 0, 0, 1, 0, 0, 0, 0})
		case 19:
			readlinkCount++
			if string(request[9:]) != "/opt/p/build/source/link" {
				t.Error("wrong readlink path")
			}
			target := []byte("flake.nix")
			packet := make([]byte, 1+4+4+4+len(target)+4+len(target)+4)
			packet[0] = 104
			binary.BigEndian.PutUint32(packet[1:5], 1)
			binary.BigEndian.PutUint32(packet[5:9], 1)
			binary.BigEndian.PutUint32(packet[9:13], uint32(len(target)))
			copy(packet[13:], target)
			offset := 13 + len(target)
			binary.BigEndian.PutUint32(packet[offset:offset+4], uint32(len(target)))
			copy(packet[offset+4:], target)
			wire := make([]byte, 4+len(packet))
			binary.BigEndian.PutUint32(wire[:4], uint32(len(packet)))
			copy(wire[4:], packet)
			_ = writePacket(conn, wire)
		default:
			t.Error("unexpected SFTP operation")
		}
	})}
	go server.Serve(listener)
	defer server.Close()
	api := &unixFileAPI{socket: socket, project: "test", instance: "p-builder-test"}
	if err := api.chmodDirectory(context.Background(), builderSource, 0555); err != nil {
		t.Fatal(err)
	}
	f, exists, err := api.getSymlinkBounded(context.Background(), builderSource+"/link", 9)
	if err != nil || !exists || string(f.data) != "flake.nix" {
		t.Fatalf("literal symlink: %+v %v", f, err)
	}
	if headCount != 2 || getCount != 0 || setstatCount != 1 || readlinkCount != 1 {
		t.Fatalf("unexpected API use: head=%d get=%d setstat=%d readlink=%d", headCount, getCount, setstatCount, readlinkCount)
	}
}

type testSnapshot struct {
	path    string
	entries int
	bytes   int64
}

func (s testSnapshot) Path() string { return s.path }
func (s testSnapshot) Entries() int { return s.entries }
func (s testSnapshot) Bytes() int64 { return s.bytes }

type memoryBuilderFiles map[string]guestFile

func (m memoryBuilderFiles) get(_ context.Context, path string) (guestFile, bool, error) {
	f, ok := m[path]
	return f, ok, nil
}
func (m memoryBuilderFiles) put(_ context.Context, path string, f guestFile) error {
	if current, exists := m[path]; exists && current.typ == "directory" {
		return nil
	} // pinned HTTP POST does not chmod existing dirs
	f.data = bytes.Clone(f.data)
	m[path] = f
	return nil
}
func (m memoryBuilderFiles) chmodDirectory(_ context.Context, path string, mode int) error {
	f, exists := m[path]
	if !exists || f.typ != "directory" {
		return errors.New("not a directory")
	}
	f.mode = mode
	m[path] = f
	return nil
}

func TestBuilderSourceTransferPreservesModesAndLinks(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { _ = os.Chmod(root, 0700) })
	if err := os.WriteFile(filepath.Join(root, "flake.nix"), []byte("source"), 0400); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "run"), []byte("#!"), 0500); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("flake.nix", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0500); err != nil {
		t.Fatal(err)
	}
	api := memoryBuilderFiles{
		"/": {typ: "directory", mode: 0755},
	}
	s := testSnapshot{root, 3, int64(len("source") + len("#!") + len("flake.nix"))}
	if err := transferBuilderSource(context.Background(), api, s); err != nil {
		t.Fatal(err)
	}
	if api[builderSource].mode != 0555 || api[builderSource+"/run"].mode != 0555 || string(api[builderSource+"/link"].data) != "flake.nix" {
		t.Fatalf("transfer lost modes or link: %+v", api)
	}
	for _, path := range []string{"/opt", "/opt/p", "/opt/p/build", builderSource, builderSource + "/flake.nix", builderSource + "/run"} {
		f := api[path]
		if f.uid != 0 || f.gid != 0 || f.mode&0022 != 0 || f.mode&0004 == 0 || f.typ == "directory" && f.mode&0001 == 0 {
			t.Fatalf("guest cannot safely read immutable source %s: %+v", path, f)
		}
	}
	if err := transferBuilderSource(context.Background(), api, s); err == nil {
		t.Fatal("adopted existing source")
	}
	for _, ancestor := range []string{"/opt", "/opt/p"} {
		unsafe := memoryBuilderFiles{
			"/":      {typ: "directory", mode: 0755},
			"/opt":   {typ: "directory", mode: 0755},
			"/opt/p": {typ: "directory", mode: 0755},
		}
		f := unsafe[ancestor]
		f.mode = 0700
		unsafe[ancestor] = f
		if err := transferBuilderSource(context.Background(), unsafe, s); err == nil {
			t.Fatalf("accepted unreadable source ancestor %s", ancestor)
		}
		if _, exists := unsafe["/opt/p/build"]; exists {
			t.Fatalf("mutated source tree after rejecting %s", ancestor)
		}
	}
}

func TestBuilderSourceRejectsSymlinkEscape(t *testing.T) {
	if err := validateBuilderLinks(context.Background(), map[string]string{"a": "../outside"}); err == nil {
		t.Fatal("accepted escape")
	}
	if err := validateBuilderLinks(context.Background(), map[string]string{"a": "b", "b": "a"}); err == nil {
		t.Fatal("accepted cycle")
	}
}

func TestBuilderUsesLogicalProjectIdentity(t *testing.T) {
	for _, project := range []string{"app", "team/repo", "a.git/b"} {
		r := testBuilder()
		r.ProjectPath = project
		if err := validateBuilder(r); err != nil {
			t.Errorf("valid logical identity %q: %v", project, err)
		}
	}
	for _, project := range []string{"/source/repo", "../repo", "team/../repo", "team//repo", "team/", strings.Repeat("a", 101), "team/雪"} {
		r := testBuilder()
		r.ProjectPath = project
		if err := validateBuilder(r); err == nil {
			t.Errorf("unsafe identity accepted: %q", project)
		}
	}
}

func TestBuilderInitGatePreflightFailureThenCorrectedExactRetry(t *testing.T) {
	for _, name := range []string{"image-missing", "confinement", "storage"} {
		t.Run(name, func(t *testing.T) {
			r := testBuilder()
			f := &fakeBuilderIncus{request: r}
			b := builderBackend(f)
			missing := name == "image-missing"
			if name == "confinement" {
				f.base.unsafeProfile = true
			}
			if name == "storage" {
				f.poolStatus = "Unavailable"
			}
			b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
				if missing && len(argv) > 4 && argv[3] == "image" && argv[4] == "list" {
					return []byte("[]"), nil
				}
				return f.run(ctx, binary, argv, env)
			}
			gates := 0
			gate := func() error {
				gates++
				if len(f.mutations) != 0 {
					t.Fatal("marker followed native init")
				}
				return nil
			}
			if _, e := b.CreateBuilderWithGate(context.Background(), r, gate); e == nil || gates != 0 || len(f.mutations) != 0 {
				t.Fatalf("read-only preflight crossed dispatch: gates=%d native=%v err=%v", gates, f.mutations, e)
			}
			// Correct only the preflight condition. The UUID/source/image request is
			// exactly the original; one gate immediately precedes one native init.
			missing = false
			f.base.unsafeProfile = false
			f.poolStatus = "Created"
			got, e := b.CreateBuilderWithGate(context.Background(), r, gate)
			if e != nil || !got.Exists || !got.Ready || gates != 1 || !slices.Equal(f.mutations, []string{"init"}) {
				t.Fatalf("corrected exact retry: %+v gates=%d native=%v err=%v", got, gates, f.mutations, e)
			}
			if _, e = b.CreateBuilderWithGate(context.Background(), r, func() error { t.Fatal("existing builder issued another init marker"); return nil }); e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestBuilderInitGatePersistenceFailureAndLostReply(t *testing.T) {
	r := testBuilder()
	f := &fakeBuilderIncus{request: r}
	b := builderBackend(f)
	refused := errors.New("fixture marker persistence failed")
	if _, e := b.CreateBuilderWithGate(context.Background(), r, func() error { return refused }); !errors.Is(e, refused) || len(f.mutations) != 0 {
		t.Fatal("native init ran after gate refusal", e, f.mutations)
	}
	attempts := 0
	b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
		if len(argv) > 3 && argv[3] == "init" {
			return nil, errors.New("fixture init reply/outcome lost")
		}
		return f.run(ctx, binary, argv, env)
	}
	if _, e := b.CreateBuilderWithGate(context.Background(), r, func() error { attempts++; return nil }); e == nil || attempts != 1 || f.exists {
		t.Fatal("unknown init lost dispatch marker")
	}
	// Core refuses a second absent outcome using its durable attempted marker.
	if _, e := b.CreateBuilderWithGate(context.Background(), r, func() error { return refused }); !errors.Is(e, refused) || attempts != 1 {
		t.Fatal("lost reply admitted another init")
	}
}

func TestBuilderPolicyNamesItsPublicEgressSubstrate(t *testing.T) {
	offline := BuilderPolicyDigest("")
	public := BuilderPolicyDigest(strings.Repeat("a", 64))
	changed := BuilderPolicyDigest(strings.Repeat("b", 64))
	if offline == public || public == changed {
		t.Fatal("a creation could resume under another builder network")
	}
}

func TestInterruptedPublicBuilderCreationCanBeCleanedUpOrFinished(t *testing.T) {
	r := testBuilder()
	r.PublicIPv4 = "10.233.0.11/24"
	f := &fakeBuilderIncus{request: r}
	b := publicBuilderBackend(t, builderBackend(f), f)
	ctx := context.Background()
	run := b.run
	b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
		if len(argv) > 5 && argv[3] == "config" && argv[5] == "add" {
			return nil, errors.New("fixture lost the NIC request")
		}
		return run(ctx, binary, argv, env)
	}
	if _, err := b.CreateBuilder(ctx, r); err == nil {
		t.Fatal("creation without its NIC succeeded")
	}
	b.run = run
	got, err := b.InspectBuilder(ctx, r)
	if err != nil || !got.Exists || got.Ready {
		t.Fatalf("owned builder awaiting its NIC: %+v %v", got, err)
	}
	if _, err := b.StartBuilder(ctx, r); err == nil {
		t.Fatal("builder without its NIC started")
	}
	if got, err := b.CreateBuilder(ctx, r); err != nil || !got.Ready {
		t.Fatalf("interrupted creation not finished: %+v %v", got, err)
	}
	f.publicNIC = nil
	if got, err := b.DeleteBuilder(ctx, r); err != nil || got.Exists {
		t.Fatalf("owned builder awaiting its NIC not removed: %+v %v", got, err)
	}
}
