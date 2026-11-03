package runtimeincus

import (
	"context"
	"encoding/binary"
	"encoding/json"
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
	"github.com/lgvo/p.ai/internal/runtimedriver"
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
		Selection: BuilderNixSelection{Builder: r, System: "x86_64-linux", Selection: runtimedriver.SelectionDevShell,
			DevShellAttr: "devShells.x86_64-linux.default", DerivationPath: testDrv, SourceNarHash: testNAR},
		Material: material, MaterialDigest: digest, CaptureStorePath: testCapture,
	}
}

func testImageLabels(r Builder) runtimedriver.Labels {
	return runtimedriver.Labels{
		runtimedriver.LabelProject: r.ProjectPath, runtimedriver.LabelSourceCommit: r.CommitOID,
		runtimedriver.LabelBaseImage: r.BaseImageFingerprint, runtimedriver.LabelSystem: "x86_64-linux",
		runtimedriver.LabelBuilderVersion: ImageBuilderVersion, runtimedriver.LabelBuild: r.RequestUUID,
	}
}

func TestBuilderImageRejectsChangedResultAndMetadata(t *testing.T) {
	r, result := imageResultFixture(t)
	if err := validateBuilderImageResult(r, result); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*BuilderNixResult){
		func(x *BuilderNixResult) { x.Material.Script += "malicious" },
		func(x *BuilderNixResult) { x.Selection.Selection = runtimedriver.SelectionNoDefault },
		func(x *BuilderNixResult) { x.Selection.SourceNarHash = "" },
		func(x *BuilderNixResult) { x.Selection.Builder.ProjectPath = "other/project" },
		func(x *BuilderNixResult) { x.CaptureStorePath = "/tmp/forged-env" },
	} {
		bad := result
		change(&bad)
		if err := validateBuilderImageResult(r, bad); err == nil {
			t.Fatalf("accepted changed image result: %+v", bad)
		}
	}
	shell := runtimedriver.Shell{Selection: runtimedriver.SelectionDevShell, MaterialSHA256: result.MaterialDigest}
	props := commitImageLabels(testImageLabels(r), shell, result.CaptureStorePath, testInstance, "user-1000")
	args := commitImagePublishArgs(r, props)
	if !slices.Equal(args[:4], []string{"publish", builderName(r), "--compression", "none"}) ||
		!slices.Contains(args, "p.compression=none") || !slices.Contains(args, "p.source_commit="+r.CommitOID) ||
		!slices.Contains(args, "p.build="+r.RequestUUID) || !slices.Contains(args, "p.builder_version="+ImageBuilderVersion) ||
		slices.Contains(args, "--public") || slices.Contains(args, "--alias") || slices.Contains(args, "--reuse") {
		t.Fatalf("publish relied on host compression/default alias policy or lost a label: %q", args)
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
		return &memoryImageFiles{files: maps.Clone(base)}
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

// builtTree is a stopped builder after a build: scratch, logs, the bundle,
// the public resolver and the workspace clone.
func builtTree(commit string) *memoryImageFiles {
	dir := func(uid, mode int) guestFile { return guestFile{typ: "directory", uid: uid, gid: uid, mode: mode} }
	file := func(uid, mode int, data string) guestFile {
		return guestFile{typ: "file", uid: uid, gid: uid, mode: mode, data: []byte(data)}
	}
	return &memoryImageFiles{files: map[string]guestFile{
		"/": dir(0, 0755), "/etc": dir(0, 0755), "/etc/resolv.conf": file(0, 0644, "nameserver 127.0.0.1\n"),
		"/home": dir(0, 0755), "/home/p": dir(1000, 0700), "/home/p/.cache": dir(1000, 0755),
		"/tmp": dir(0, 01777), "/tmp/hook-output": file(1000, 0644, "x"),
		"/var": dir(0, 0755), "/var/tmp": dir(0, 01777), "/var/log": dir(0, 0755), "/var/log/journal": dir(0, 0755),
		"/opt": dir(0, 0755), "/opt/p": dir(0, 0755), builderBuildDir: dir(0, 0755), builderBundle: file(0, 0444, "bundle"),
		"/nix": dir(0, 0755), "/nix/var": dir(0, 0755), "/nix/var/log": dir(0, 0755), "/nix/var/log/nix": dir(0, 0755),
		"/nix/var/nix": dir(0, 0755), "/nix/var/nix/gcroots": dir(0, 0755), "/nix/var/nix/gcroots/per-user": dir(0, 0755),
		"/nix/var/nix/gcroots/per-user/p": dir(1000, 0755), "/nix/var/nix/profiles": dir(0, 0755), "/nix/var/nix/profiles/per-user": dir(0, 0755),
		"/workspace": dir(1000, 0755), "/workspace/.git": dir(1000, 0755), "/workspace/.git/HEAD": file(1000, 0644, commit+"\n"),
		"/workspace/flake.nix": file(1000, 0644, "{ outputs = _: {}; }\n"),
	}}
}

func TestBuilderImageFinalScrubKeepsWorkspace(t *testing.T) {
	ctx := context.Background()
	commit := strings.Repeat("a", 40)
	m := builtTree(commit)
	if err := scrubBuilderImage(ctx, m); err != nil {
		t.Fatal(err)
	}
	for _, kept := range []string{"/workspace", "/workspace/.git/HEAD", "/workspace/flake.nix"} {
		if _, ok := m.files[kept]; !ok {
			t.Fatalf("final scrub removed the clone's %s", kept)
		}
	}
	for _, gone := range []string{"/home/p/.cache", "/tmp/hook-output", "/var/log/journal", builderBuildDir, builderBundle, "/nix/var/nix/gcroots/per-user/p"} {
		if _, ok := m.files[gone]; ok {
			t.Fatalf("final scrub kept %s", gone)
		}
	}
	if slices.Contains(m.deletes, builderWorkspace) {
		t.Fatalf("final scrub deleted the workspace: %q", m.deletes)
	}
	if got := m.files["/etc/resolv.conf"]; got.typ != "symlink" || string(got.data) != "/etc/static/resolv.conf" {
		t.Fatalf("builder resolver kept: %+v", got)
	}
	if err := verifyBuilderWorkspace(ctx, m, commit); err != nil {
		t.Fatalf("scrubbed image lost its clone: %v", err)
	}
	// Before collection the workspace goes too, with whatever a hook rooted
	// there, and comes back empty and the builder user's.
	m = builtTree(commit)
	if err := scrubBuilderScratch(ctx, m, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.files["/workspace/.git/HEAD"]; ok || m.files["/workspace"].uid != 1000 || m.files["/workspace"].mode != 0755 {
		t.Fatalf("scratch scrub kept the clone or lost the workspace: %+v", m.files["/workspace"])
	}
	if _, ok := m.files[builderBundle]; !ok {
		t.Fatal("scratch scrub removed the bundle the second clone needs")
	}
}

func TestBuilderImageScrubWithoutANixBuildLog(t *testing.T) {
	ctx := context.Background()
	commit := strings.Repeat("a", 40)
	// A commit with nothing to realize: Nix never made its log tree.
	m := builtTree(commit)
	delete(m.files, "/nix/var/log")
	delete(m.files, "/nix/var/log/nix")
	if err := scrubBuilderImage(ctx, m); err != nil {
		t.Fatalf("image without a Nix log tree refused: %v", err)
	}
	if _, ok := m.files["/nix/var/log"]; ok || slices.Contains(m.deletes, "/nix/var/log/nix") {
		t.Fatalf("scrub made or touched a Nix log tree: %q", m.deletes)
	}
	if _, ok := m.files[builderBundle]; ok {
		t.Fatal("scrub kept the bundle")
	}
	for name, edit := range map[string]func(*memoryImageFiles){
		"writable log tree": func(m *memoryImageFiles) {
			m.files["/nix/var/log"] = guestFile{typ: "directory", uid: 0, gid: 0, mode: 0777}
		},
		"user-owned log tree": func(m *memoryImageFiles) {
			m.files["/nix/var/log"] = guestFile{typ: "directory", uid: 1000, gid: 1000, mode: 0755}
		},
		"linked log tree": func(m *memoryImageFiles) {
			m.files["/nix/var/log"] = guestFile{typ: "symlink", uid: 0, gid: 0, mode: 0777, data: []byte("/outside")}
		},
		"log tree without nix": func(m *memoryImageFiles) { delete(m.files, "/nix/var/log/nix") },
		"unsafe Nix ancestor": func(m *memoryImageFiles) {
			m.files["/nix/var"] = guestFile{typ: "directory", uid: 0, gid: 0, mode: 0777}
		},
	} {
		m := builtTree(commit)
		edit(m)
		if err := scrubBuilderImage(ctx, m); err == nil || slices.Contains(m.deletes, "/nix/var/log/nix") {
			t.Fatalf("%s: scrub accepted or deleted below it: %v %q", name, err, m.deletes)
		}
	}
}

func TestBuilderImageWorkspaceIsTheDetachedClone(t *testing.T) {
	ctx := context.Background()
	commit := strings.Repeat("a", 40)
	if err := verifyBuilderWorkspace(ctx, builtTree(commit), commit); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*memoryImageFiles){
		"attached HEAD": func(m *memoryImageFiles) {
			m.files["/workspace/.git/HEAD"] = guestFile{typ: "file", uid: 1000, gid: 1000, mode: 0644, data: []byte("ref: refs/heads/main\n")}
		},
		"other commit": func(m *memoryImageFiles) {
			m.files["/workspace/.git/HEAD"] = guestFile{typ: "file", uid: 1000, gid: 1000, mode: 0644, data: []byte(strings.Repeat("c", 40) + "\n")}
		},
		"root-owned clone": func(m *memoryImageFiles) {
			m.files["/workspace/.git"] = guestFile{typ: "directory", uid: 0, gid: 0, mode: 0755}
		},
		"linked .git": func(m *memoryImageFiles) {
			m.files["/workspace/.git"] = guestFile{typ: "symlink", uid: 1000, gid: 1000, mode: 0777, data: []byte("/opt/elsewhere")}
		},
		"writable workspace": func(m *memoryImageFiles) {
			m.files["/workspace"] = guestFile{typ: "directory", uid: 1000, gid: 1000, mode: 0777}
		},
		"initialized marker": func(m *memoryImageFiles) {
			m.files["/workspace/.git/p-initialized"] = guestFile{typ: "file", uid: 1000, gid: 1000, mode: 0644}
		},
	} {
		m := builtTree(commit)
		edit(m)
		if err := verifyBuilderWorkspace(ctx, m, commit); err == nil {
			t.Fatalf("%s: accepted a workspace that is not the session-neutral clone", name)
		}
	}
}

func TestBuilderImageScrubRefusesSymlinkRootsAndAncestors(t *testing.T) {
	ctx := context.Background()
	m := builtTree(strings.Repeat("a", 40))
	m.files["/opt/p"] = guestFile{typ: "symlink", uid: 0, gid: 0, mode: 0777, data: []byte("/outside")}
	if err := scrubBuilderImage(ctx, m); err == nil || slices.Contains(m.deletes, builderBuildDir) {
		t.Fatalf("unsafe ancestor reached recursive delete: %v %q", err, m.deletes)
	}
	m = builtTree(strings.Repeat("a", 40))
	m.files[builderBuildDir] = guestFile{typ: "symlink", uid: 0, gid: 0, mode: 0777, data: []byte("/outside")}
	if err := scrubBuilderImage(ctx, m); err == nil || slices.Contains(m.deletes, builderBuildDir) {
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

func TestBuilderImageScrubRestoresImageResolverLink(t *testing.T) {
	ctx := context.Background()
	dir := guestFile{typ: "directory", uid: 0, gid: 0, mode: 0755}
	link := guestFile{typ: "symlink", uid: 0, gid: 0, mode: 0777, data: []byte("/etc/static/resolv.conf")}
	withResolver := func(resolver guestFile) *memoryImageFiles {
		return &memoryImageFiles{files: map[string]guestFile{"/": dir, "/etc": dir, "/etc/resolv.conf": resolver}}
	}
	public := withResolver(guestFile{typ: "file", uid: 0, gid: 0, mode: 0644, data: []byte("nameserver 127.0.0.1\n")})
	if err := scrubBuilderResolver(ctx, public); err != nil {
		t.Fatal(err)
	}
	if got := public.files["/etc/resolv.conf"]; got.typ != "symlink" || got.uid != 0 || string(got.data) != "/etc/static/resolv.conf" {
		t.Fatalf("public builder's loopback resolver kept in the image: %+v", got)
	}
	offline := withResolver(link)
	if err := scrubBuilderResolver(ctx, offline); err != nil || len(offline.deletes) != 0 {
		t.Fatalf("image resolver link changed: %v %q", err, offline.deletes)
	}
	for name, changed := range map[string]guestFile{
		"other resolver": {typ: "file", uid: 0, gid: 0, mode: 0644, data: []byte("nameserver 10.0.0.1\n")},
		"user-owned":     {typ: "file", uid: 1000, gid: 1000, mode: 0644, data: []byte("nameserver 127.0.0.1\n")},
		"other link":     {typ: "symlink", uid: 0, gid: 0, mode: 0777, data: []byte("/tmp/resolv.conf")},
		"directory":      dir,
	} {
		m := withResolver(changed)
		if err := scrubBuilderResolver(ctx, m); err == nil || len(m.deletes) != 0 {
			t.Fatalf("%s resolver accepted or deleted: %v %q", name, err, m.deletes)
		}
	}
}

// publishIncus serves a stopped builder and an image list that gains the
// published image once the publish command ran.
func publishIncus(t *testing.T, f *fakeBuilderIncus, publish func(args []string) ([]byte, error)) (*Backend, *[]builderImageJSON) {
	t.Helper()
	b := builderBackend(f)
	images := []builderImageJSON{{Fingerprint: testImage, Type: "container", Architecture: "x86_64", Size: 1, Properties: map[string]string{"os": "NixOS"}}}
	b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
		switch {
		case slices.Equal(argv[3:], []string{"image", "list", "--format", "json"}):
			return json.Marshal(images)
		case argv[3] == "publish":
			out, err := publish(argv[3:])
			if err == nil {
				properties := map[string]string{"os": "NixOS"}
				for _, pair := range argv[7:] {
					key, value, _ := strings.Cut(pair, "=")
					properties[key] = value
				}
				images = append(images, builderImageJSON{Fingerprint: strings.Repeat("e", 64), Type: "container", Architecture: "x86_64", Project: "user-1000", Size: 4096, Properties: properties})
			}
			return out, err
		}
		return f.run(ctx, binary, argv, env)
	}
	return b, &images
}

func TestCommitImagePublishVerifiesWhatIncusReports(t *testing.T) {
	r, result := imageResultFixture(t)
	f := &fakeBuilderIncus{request: r, exists: true, status: "Stopped", rootLimit: true}
	b, _ := publishIncus(t, f, func(args []string) ([]byte, error) {
		return []byte("Instance published with fingerprint: " + strings.Repeat("e", 64) + "\n"), nil
	})
	shell := runtimedriver.Shell{Selection: runtimedriver.SelectionDevShell, MaterialSHA256: result.MaterialDigest}
	labels := commitImageLabels(testImageLabels(r), shell, result.CaptureStorePath, testInstance, "user-1000")
	publishing := false
	image, err := b.publishCommitImage(context.Background(), r, labels, "x86_64-linux", &publishing)
	if err != nil || !publishing || image.Fingerprint != strings.Repeat("e", 64) || image.Size != 4096 || image.Properties["os"] != "NixOS" {
		t.Fatalf("publish: %+v publishing=%t %v", image, publishing, err)
	}
	got, err := b.commitImage(image)
	if err != nil || !maps.Equal(got.Labels, testImageLabels(r)) || got.Shell != shell {
		t.Fatalf("published image reads back differently: %+v %v", got, err)
	}
}

func TestCommitImagePublishFailureLeavesAnUnknownOutcome(t *testing.T) {
	r, result := imageResultFixture(t)
	shell := runtimedriver.Shell{Selection: runtimedriver.SelectionDevShell, MaterialSHA256: result.MaterialDigest}
	labels := commitImageLabels(testImageLabels(r), shell, result.CaptureStorePath, testInstance, "user-1000")
	for name, publish := range map[string]func([]string) ([]byte, error){
		"client lost the reply": func([]string) ([]byte, error) { return nil, errors.New("connection reset") },
		"no exact fingerprint":  func([]string) ([]byte, error) { return []byte("Instance published\n"), nil },
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeBuilderIncus{request: r, exists: true, status: "Stopped", rootLimit: true}
			b, _ := publishIncus(t, f, publish)
			publishing := false
			_, err := b.publishCommitImage(context.Background(), r, labels, "x86_64-linux", &publishing)
			if err == nil || !publishing {
				t.Fatalf("publish failure: publishing=%t %v", publishing, err)
			}
			if err := b.failedBuild(r, err, publishing); !errors.Is(err, runtimedriver.ErrUnknownOutcome) || !f.exists || len(f.mutations) != 0 {
				t.Fatalf("publish failure settled as definite or removed the builder: %v %v", err, f.mutations)
			}
		})
	}
	f := &fakeBuilderIncus{request: r, exists: true, status: "Running", rootLimit: true}
	b, _ := publishIncus(t, f, func([]string) ([]byte, error) { t.Fatal("published a running builder"); return nil, nil })
	publishing := false
	if _, err := b.publishCommitImage(context.Background(), r, labels, "x86_64-linux", &publishing); err == nil || publishing {
		t.Fatalf("running builder reached publish: publishing=%t %v", publishing, err)
	}
}
