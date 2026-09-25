package runtimeincus

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lgvo/p.ai/internal/nixenv"
)

type memoryImageFiles struct {
	files   map[string]guestFile
	deletes []string
}

func (m *memoryImageFiles) get(_ context.Context, path string) (guestFile, bool, error) {
	f, ok := m.files[path]
	if ok && f.typ == "directory" {
		f.mode &= 0777 // pinned Incus file HEAD returns FileMode.Perm()
	}
	return f, ok, nil
}
func (m *memoryImageFiles) getBounded(ctx context.Context, path string, size int64) (guestFile, bool, error) {
	f, ok, err := m.get(ctx, path)
	if ok && int64(len(f.data)) > size {
		return guestFile{}, false, errors.New("bound")
	}
	return f, ok, err
}
func (m *memoryImageFiles) getSymlinkBounded(ctx context.Context, path string, size int64) (guestFile, bool, error) {
	return m.getBounded(ctx, path, size)
}
func (m *memoryImageFiles) fullDirectoryMode(_ context.Context, path string) (guestFile, error) {
	f, ok := m.files[path]
	if !ok || f.typ != "directory" {
		return guestFile{}, errors.New("not a real directory")
	}
	return f, nil
}
func (m *memoryImageFiles) lstat(_ context.Context, path string) (guestFile, error) {
	f, ok := m.files[path]
	if !ok {
		return guestFile{}, errors.New("missing path")
	}
	return f, nil
}
func (m *memoryImageFiles) chmodDirectory(_ context.Context, path string, mode int) error {
	f, ok := m.files[path]
	if !ok || f.typ != "directory" {
		return errors.New("not a directory")
	}
	f.mode = mode
	m.files[path] = f
	return nil
}
func (m *memoryImageFiles) put(_ context.Context, path string, f guestFile) error {
	if _, exists := m.files[path]; exists {
		return errors.New("overwrite")
	}
	m.files[path] = f
	return nil
}
func (m *memoryImageFiles) deleteTree(_ context.Context, path string) error {
	m.deletes = append(m.deletes, path)
	for name := range m.files {
		if name == path || strings.HasPrefix(name, path+"/") {
			delete(m.files, name)
		}
	}
	return nil
}

func imageResultFixture(t *testing.T) (Builder, BuilderNixResult) {
	t.Helper()
	r := testBuilder()
	h := sha256.Sum256([]byte(builderNixPolicy))
	key := nixenv.KeyInputs{
		AdapterVersion: nixenv.AdapterVersion, NixVersion: nixenv.NixVersion,
		System: "x86_64-linux", ProjectPath: r.ProjectPath,
		BaseFingerprint: r.BaseImageFingerprint, RuntimeKitContract: "p.runtime-session/v2",
		CommittedInputsDigest: strings.Repeat("a", 64), DerivationPath: testDrv,
		NixPolicyDigest: hex.EncodeToString(h[:]), ImageFormatVersion: builderImageFormatKey,
	}
	keyDigest, err := key.Digest()
	if err != nil {
		t.Fatal(err)
	}
	material := nixenv.Material{
		Schema: nixenv.MaterialSchema, NixVersion: nixenv.NixVersion,
		AdapterVersion: nixenv.AdapterVersion, Script: "# fixed activation\ntrue\n",
		CaptureSHA256: strings.Repeat("b", 64),
	}
	digest, err := material.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return r, BuilderNixResult{
		Selection: BuilderNixSelection{Builder: r, System: "x86_64-linux", DevShellAttr: "devShells.x86_64-linux.default",
			DerivationPath: testDrv, SourceNarHash: testNAR, CommittedInputsDigest: key.CommittedInputsDigest,
			KeyInputs: key, KeyDigest: keyDigest},
		Material: material, MaterialDigest: digest,
		CaptureStorePath: "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-capture-env",
	}
}

func TestBuilderSmokeUsesOnlyFixedUnprivilegedDisposableCopy(t *testing.T) {
	r := testBuilder()
	f := &fakeBuilderIncus{request: r, exists: true, status: "Running", rootLimit: true}
	b := nixBuilderBackend(t, f, nil)
	var commands [][]string
	dirty := false
	b.runBuilder = func(_ context.Context, _ string, argv, _ []string) ([]byte, error) {
		if len(argv) < 5 || argv[3] != "exec" || !slices.Contains(argv, "--user") || !slices.Contains(argv, "1000") || !slices.Contains(argv, "--group") || !slices.Contains(argv, "--cwd") || !slices.Contains(argv, "/workspace") {
			t.Fatalf("smoke copy escaped fixed UID/cwd: %q", argv)
		}
		at := slices.Index(argv, "--")
		if at < 0 {
			t.Fatalf("missing guest command: %q", argv)
		}
		guest := slices.Clone(argv[at+1:])
		commands = append(commands, guest)
		if guest[0] == "/run/current-system/sw/bin/ls" && dirty {
			return []byte(" \n"), nil
		}
		return nil, nil
	}
	if err := b.prepareBuilderSmokeWorkspace(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"/run/current-system/sw/bin/ls", "-A", "/workspace"},
		{"/run/current-system/sw/bin/cp", "-R", "-P", "--", builderSource + "/.", "/workspace/"},
		{"/run/current-system/sw/bin/chmod", "-R", "u+rwX", "--", "/workspace"},
	}
	if !slices.EqualFunc(commands, want, slices.Equal) {
		t.Fatalf("unexpected smoke copy commands: %q", commands)
	}
	commands = nil
	dirty = true
	if err := b.prepareBuilderSmokeWorkspace(context.Background(), r); err == nil || len(commands) != 1 {
		t.Fatalf("dirty workspace copied over: %q %v", commands, err)
	}
	commands = nil
	f.foreign = true
	if err := b.prepareBuilderSmokeWorkspace(context.Background(), r); err == nil || len(commands) != 0 {
		t.Fatalf("foreign builder reached copy: %q %v", commands, err)
	}
}

func TestBuilderImageRejectsChangedResultAndMetadata(t *testing.T) {
	r, result := imageResultFixture(t)
	if err := validateBuilderImageResult(r, result); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*BuilderNixResult){
		func(x *BuilderNixResult) { x.Material.Script += "malicious" },
		func(x *BuilderNixResult) { x.Selection.KeyDigest = strings.Repeat("0", 64) },
		func(x *BuilderNixResult) {
			x.Selection.KeyInputs.ImageFormatVersion = imageContract
			digest, err := x.Selection.KeyInputs.Digest()
			if err != nil {
				t.Fatal(err)
			}
			x.Selection.KeyDigest = digest
		},
		func(x *BuilderNixResult) { x.Selection.Builder.ProjectPath = "other/project" },
		func(x *BuilderNixResult) { x.CaptureStorePath = "/tmp/forged-env" },
	} {
		bad := result
		change(&bad)
		if err := validateBuilderImageResult(r, bad); err == nil {
			t.Fatalf("accepted changed image result: %+v", bad)
		}
	}
	props := builderImageProperties(r, result, "00000000-0000-4000-8000-000000000000", "user-1000")
	args := builderImagePublishArgs(r, props)
	if !slices.Equal(args[:4], []string{"publish", builderName(r), "--compression", "none"}) ||
		!slices.Contains(args, "p.compression=none") || slices.Contains(args, "--public") ||
		slices.Contains(args, "--alias") || slices.Contains(args, "--reuse") {
		t.Fatalf("publish relied on host compression/default alias policy: %q", args)
	}
	base := builderImageJSON{Fingerprint: r.BaseImageFingerprint, Type: "container", Properties: map[string]string{"os": "NixOS", "release": "25.11"}}
	merged, err := expectedBuilderImageProperties([]builderImageJSON{base}, r.BaseImageFingerprint, props)
	if err != nil || len(merged) != len(props)+2 || merged["os"] != "NixOS" {
		t.Fatalf("base metadata not merged exactly: %+v %v", merged, err)
	}
	base.Properties["p.foreign"] = "unsafe"
	if _, err := expectedBuilderImageProperties([]builderImageJSON{base}, r.BaseImageFingerprint, props); err == nil {
		t.Fatal("accepted reserved P label inherited from base")
	}
	delete(base.Properties, "p.foreign")
	if _, err := expectedBuilderImageProperties(nil, r.BaseImageFingerprint, props); err == nil {
		t.Fatal("accepted missing pinned base metadata")
	}
	good := builderImageJSON{Fingerprint: strings.Repeat("c", 64), Type: "container", Architecture: "x86_64", Project: "user-1000", Size: 1024, Properties: merged}
	if err := checkPublishedBuilderImage(good, merged, "x86_64-linux", "user-1000"); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*builderImageJSON){
		func(x *builderImageJSON) { x.Public = true },
		func(x *builderImageJSON) { x.Project = "other" },
		func(x *builderImageJSON) { x.Architecture = "aarch64" },
		func(x *builderImageJSON) {
			x.Properties = maps.Clone(merged)
			x.Properties["os"] = "foreign"
		},
		func(x *builderImageJSON) {
			x.Properties = maps.Clone(merged)
			x.Properties["p.foreign"] = "injected"
		},
		func(x *builderImageJSON) { x.Properties = map[string]string{"p.material": result.MaterialDigest} },
		func(x *builderImageJSON) {
			x.Aliases = []struct {
				Name string `json:"name"`
			}{{Name: "mutable"}}
		},
	} {
		bad := good
		mutate(&bad)
		if err := checkPublishedBuilderImage(bad, merged, "x86_64-linux", "user-1000"); err == nil {
			t.Fatalf("accepted changed image metadata: %+v", bad)
		}
	}
}

func TestBuilderImageInstallVerifiesProfileAndRootOwnedMaterial(t *testing.T) {
	_, result := imageResultFixture(t)
	base := map[string]guestFile{
		"/":                                  {typ: "directory", uid: 0, gid: 0, mode: 0755},
		"/etc":                               {typ: "directory", uid: 0, gid: 0, mode: 0755},
		"/home/p/.p-devshell-capture":        {typ: "symlink", uid: 1000, gid: 1000, mode: 0777, data: []byte(".p-devshell-capture-1-link")},
		"/home/p/.p-devshell-capture-1-link": {typ: "symlink", uid: 1000, gid: 1000, mode: 0777, data: []byte(result.CaptureStorePath)},
		"/nix/var/nix/gcroots":               {typ: "directory", uid: 0, gid: 0, mode: 0755},
	}
	copyFiles := func() *memoryImageFiles {
		m := &memoryImageFiles{files: map[string]guestFile{}}
		for key, value := range base {
			m.files[key] = value
		}
		return m
	}
	m := copyFiles()
	if err := installBuilderImageFiles(context.Background(), m, result); err != nil {
		t.Fatal(err)
	}
	if err := verifyBuilderImageFiles(context.Background(), m, result); err != nil {
		t.Fatal(err)
	}
	m.files[builderActivationPath] = guestFile{typ: "file", uid: 1000, gid: 1000, mode: 0644, data: []byte(result.Material.Script)}
	if err := verifyBuilderImageFiles(context.Background(), m, result); err == nil {
		t.Fatal("accepted writable/user-owned material")
	}
	m = copyFiles()
	m.files["/home/p/.p-devshell-capture-1-link"] = guestFile{typ: "symlink", uid: 1000, gid: 1000, mode: 0777, data: []byte("/nix/store/other-env")}
	if err := installBuilderImageFiles(context.Background(), m, result); err == nil {
		t.Fatal("accepted changed captured environment target")
	}
	if _, exists := m.files[builderGCPath]; exists {
		t.Fatal("installed GC root after failed provenance")
	}
}

func TestBuilderImageScrubRefusesSymlinkRootsAndAncestors(t *testing.T) {
	ctx := context.Background()
	m := &memoryImageFiles{files: map[string]guestFile{
		"/":            {typ: "directory", uid: 0, gid: 0, mode: 0755},
		"/opt":         {typ: "directory", uid: 0, gid: 0, mode: 0755},
		"/opt/p":       {typ: "symlink", uid: 0, gid: 0, mode: 0777, data: []byte("/outside")},
		"/opt/p/build": {typ: "directory", uid: 0, gid: 0, mode: 0755},
	}}
	if err := scrubBuilderImage(ctx, m); err == nil || len(m.deletes) != 0 {
		t.Fatalf("unsafe ancestor reached recursive delete: %v %q", err, m.deletes)
	}
	m.files["/opt/p"] = guestFile{typ: "directory", uid: 0, gid: 0, mode: 0755}
	m.files["/opt/p/build"] = guestFile{typ: "symlink", uid: 0, gid: 0, mode: 0777, data: []byte("/outside")}
	if err := scrubBuilderImage(ctx, m); err == nil || len(m.deletes) != 0 {
		t.Fatalf("symlink root reached recursive delete: %v %q", err, m.deletes)
	}
}

func TestBuilderImageScrubPreservesStickyModeBeyondHTTPPermBits(t *testing.T) {
	ctx := context.Background()
	m := &memoryImageFiles{files: map[string]guestFile{
		"/":                {typ: "directory", uid: 0, gid: 0, mode: 0755},
		"/tmp":             {typ: "directory", uid: 0, gid: 0, mode: 01777},
		"/tmp/p-hook-link": {typ: "symlink", uid: 1000, gid: 1000, mode: 0777, data: []byte("/etc/p/devshell/activate.sh")},
	}}
	if f, _, _ := m.get(ctx, "/tmp"); f.mode != 0777 {
		t.Fatal("test adapter did not model Incus HTTP permission-bit loss")
	}
	if err := scrubBuilderPath(ctx, m, "/tmp", 0, 0, 01777, true); err != nil {
		t.Fatal(err)
	}
	if got := m.files["/tmp"]; got.mode != 01777 || len(m.deletes) != 1 || m.deletes[0] != "/tmp" {
		t.Fatalf("sticky directory was not recreated through SFTP: %+v deletes=%q", got, m.deletes)
	}
	m.files["/tmp"] = guestFile{typ: "directory", uid: 0, gid: 0, mode: 0777}
	m.deletes = nil
	if err := scrubBuilderPath(ctx, m, "/tmp", 0, 0, 01777, true); err == nil || len(m.deletes) != 0 {
		t.Fatalf("accepted missing sticky bit: err=%v deletes=%q", err, m.deletes)
	}
}

func TestBuilderImageStickySetstatAndLstatUseConfinedSFTP(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "incus.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var changed atomic.Bool
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/1.0/instances/p-image/sftp" || r.URL.Query().Get("project") != "test" || r.Header.Get("Upgrade") != "sftp" {
			t.Errorf("unconfined SFTP request: %s", r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		conn, reader, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_, _ = fmt.Fprint(conn, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: sftp\r\n\r\n")
		if _, err := readSFTPPacket(reader); err != nil {
			t.Error(err)
			return
		}
		_ = writePacket(conn, []byte{0, 0, 0, 5, 2, 0, 0, 0, 3})
		req, err := readSFTPPacket(reader)
		if err != nil || len(req) < 9 {
			t.Error("missing SFTP operation", err)
			return
		}
		switch req[0] {
		case 9: // SETSTAT of the fixed /tmp directory.
			if len(req) != 21 || string(req[9:13]) != "/tmp" ||
				binary.BigEndian.Uint32(req[13:17]) != 4 || binary.BigEndian.Uint32(req[17:21]) != 01777 {
				t.Errorf("wrong sticky SETSTAT packet: %x", req)
			}
			changed.Store(true)
			_ = writePacket(conn, []byte{0, 0, 0, 9, 101, 0, 0, 0, 1, 0, 0, 0, 0})
		case 7: // LSTAT returns full POSIX file type and sticky bits.
			if string(req[9:]) != "/tmp" || !changed.Load() {
				t.Errorf("LSTAT not scoped after SETSTAT: %x", req)
			}
			attrs := make([]byte, 25)
			binary.BigEndian.PutUint32(attrs[:4], 21)
			attrs[4] = 105
			binary.BigEndian.PutUint32(attrs[5:9], 1)
			binary.BigEndian.PutUint32(attrs[9:13], 6)
			binary.BigEndian.PutUint32(attrs[21:25], 0041777)
			_ = writePacket(conn, attrs)
		default:
			t.Errorf("unexpected SFTP operation %d", req[0])
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); _ = listener.Close() })
	api := &unixFileAPI{socket: socket, project: "test", instance: "p-image"}
	if err := api.chmodDirectory(context.Background(), "/tmp", 01777); err != nil {
		t.Fatal(err)
	}
	got, err := api.fullDirectoryMode(context.Background(), "/tmp")
	if err != nil || got.mode != 01777 || got.uid != 0 || got.gid != 0 {
		t.Fatalf("full sticky mode lost: %+v %v", got, err)
	}
}
