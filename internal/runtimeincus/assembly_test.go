package runtimeincus

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lgvo/p.ai/internal/nixenv"
	"github.com/lgvo/p.ai/internal/plugin"
	"github.com/lgvo/p.ai/internal/runtimekit"
	"golang.org/x/crypto/ssh"
)

func TestStoppedAssemblyRequiresExactPrivateActivationMaterial(t *testing.T) {
	_, result := imageResultFixture(t)
	manifest, err := json.Marshal(result.Material)
	if err != nil {
		t.Fatal(err)
	}
	makeFiles := func() *memoryImageFiles {
		files := map[string]guestFile{}
		for _, path := range []string{"/", "/etc", "/etc/p", nixenv.AttrsDir} {
			files[path] = guestFile{typ: "directory", uid: 0, gid: 0, mode: 0755}
		}
		for path, data := range map[string][]byte{
			builderActivationPath:              []byte(result.Material.Script),
			nixenv.AttrsDir + "/.attrs.sh":     []byte(result.Material.AttrsSH),
			nixenv.AttrsDir + "/.attrs.json":   []byte(result.Material.AttrsJSON),
			nixenv.AttrsDir + "/material.json": manifest,
		} {
			files[path] = guestFile{typ: "file", uid: 0, gid: 0, mode: 0444, data: data}
		}
		return &memoryImageFiles{files: files}
	}
	if err := verifyStoppedDevShellImage(context.Background(), makeFiles(), result.MaterialDigest); err != nil {
		t.Fatalf("valid image material refused: %v", err)
	}
	for _, changed := range []struct {
		name string
		edit func(*memoryImageFiles)
	}{
		{"missing manifest", func(f *memoryImageFiles) { delete(f.files, nixenv.AttrsDir+"/material.json") }},
		{"tampered script", func(f *memoryImageFiles) {
			v := f.files[builderActivationPath]
			v.data = []byte("bad")
			f.files[builderActivationPath] = v
		}},
		{"symlink ancestor", func(f *memoryImageFiles) {
			v := f.files[nixenv.AttrsDir]
			v.typ = "symlink"
			f.files[nixenv.AttrsDir] = v
		}},
		{"symlink script", func(f *memoryImageFiles) {
			v := f.files[builderActivationPath]
			v.typ = "symlink"
			f.files[builderActivationPath] = v
		}},
		{"writable script", func(f *memoryImageFiles) {
			v := f.files[builderActivationPath]
			v.mode = 0666
			f.files[builderActivationPath] = v
		}},
	} {
		t.Run(changed.name, func(t *testing.T) {
			files := makeFiles()
			changed.edit(files)
			if err := verifyStoppedDevShellImage(context.Background(), files, result.MaterialDigest); err == nil {
				t.Fatal("unsafe image material accepted")
			}
		})
	}
}

type memoryFiles struct {
	files  map[string]guestFile
	fail   error
	writes int
}

func (m *memoryFiles) get(_ context.Context, path string) (guestFile, bool, error) {
	if m.fail != nil {
		return guestFile{}, false, m.fail
	}
	f, ok := m.files[path]
	return f, ok, nil
}
func (m *memoryFiles) put(_ context.Context, path string, f guestFile) error {
	m.writes++
	m.files[path] = f
	return nil
}

func TestGuestInstallDistinguishesMissingFromErrorsAndMutation(t *testing.T) {
	ctx := context.Background()
	want := guestFile{typ: "file", uid: 0, gid: 0, mode: 0644, data: []byte("expected")}
	m := &memoryFiles{files: map[string]guestFile{}}
	if err := ensureGuestFile(ctx, m, "/etc/p/session.json", want); err != nil || m.writes != 1 {
		t.Fatalf("install: %v writes=%d", err, m.writes)
	}
	if err := ensureGuestFile(ctx, m, "/etc/p/session.json", want); err != nil || m.writes != 1 {
		t.Fatalf("idempotent: %v writes=%d", err, m.writes)
	}
	m.files["/etc/p/session.json"] = guestFile{typ: "file", uid: 1000, gid: 1000, mode: 0644, data: []byte("expected")}
	if err := ensureGuestFile(ctx, m, "/etc/p/session.json", want); err == nil || m.writes != 1 {
		t.Fatalf("owner mutation accepted: %v", err)
	}
	m.fail = errors.New("permission denied")
	delete(m.files, "/etc/p/session.json")
	if err := ensureGuestFile(ctx, m, "/etc/p/session.json", want); err == nil || !strings.Contains(err.Error(), "permission denied") || m.writes != 1 {
		t.Fatalf("transport error became absence: %v", err)
	}
}

func TestGuestEtcBootstrapAndUnsafeExistingState(t *testing.T) {
	ctx := context.Background()
	root := guestFile{typ: "directory", uid: 0, gid: 0, mode: 0755}
	for _, tc := range []struct {
		name       string
		files      map[string]guestFile
		fail       error
		wantWrites int
		wantError  string
	}{
		{"fresh image", map[string]guestFile{"/": root}, nil, 1, ""},
		{"safe existing etc", map[string]guestFile{"/etc": {typ: "directory", uid: 0, gid: 0, mode: 0555}}, nil, 0, ""},
		{"missing image root", map[string]guestFile{}, nil, 0, "observed absent"},
		{"unsafe image root", map[string]guestFile{"/": {typ: "directory", uid: 0, gid: 0, mode: 0777}}, nil, 0, "mode=0777"},
		{"existing symlink", map[string]guestFile{"/etc": {typ: "symlink", uid: 0, gid: 0, mode: 0777}}, nil, 0, "type=symlink"},
		{"existing wrong owner", map[string]guestFile{"/etc": {typ: "directory", uid: 1000, gid: 0, mode: 0755}}, nil, 0, "uid=1000"},
		{"existing writable", map[string]guestFile{"/etc": {typ: "directory", uid: 0, gid: 0, mode: 0775}}, nil, 0, "mode=0775"},
		{"inspection failure", map[string]guestFile{"/": root}, errors.New("permission denied"), 0, "permission denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &memoryFiles{files: tc.files, fail: tc.fail}
			err := ensureGuestEtc(ctx, m)
			if tc.wantError == "" && err != nil || tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("ensureGuestEtc() = %v, want error containing %q", err, tc.wantError)
			}
			if m.writes != tc.wantWrites {
				t.Fatalf("writes = %d, want %d", m.writes, tc.wantWrites)
			}
			if tc.wantWrites == 1 {
				got := m.files["/etc"]
				if got.typ != "directory" || got.uid != 0 || got.gid != 0 || got.mode != 0755 {
					t.Fatalf("created /etc = %#v", got)
				}
			}
		})
	}
}

func assemblyFixture(t *testing.T) Assembly {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ssh.NewPublicKey(private.Public())
	if err != nil {
		t.Fatal(err)
	}
	file := func(role, destination string, mode uint32) plugin.AssetFile {
		data := []byte(role + "\n")
		h := sha256.Sum256(data)
		return plugin.AssetFile{Role: role, Destination: destination, Mode: mode, SHA256: hex.EncodeToString(h[:]), Data: data}
	}
	return Assembly{
		HostAssets: plugin.AssetPlan{Schema: "p.asset-plan/v1", PackageID: "org.p.tmux-host", PackageSHA256: strings.Repeat("a", 64), Scope: "internal-session", Files: []plugin.AssetFile{
			file("p-session.target", "/etc/systemd/system/p-session.target", 0644), file("p-interactive.service", "/etc/systemd/system/p-interactive.service", 0644), file("p-attach", "/usr/libexec/p/attach", 0555),
		}},
		SourceAssets:  plugin.AssetPlan{Schema: "p.asset-plan/v1", PackageID: "org.p.git", PackageSHA256: strings.Repeat("b", 64), Scope: "internal-session", Files: []plugin.AssetFile{file("p-git-ssh", "/usr/libexec/p/git-ssh", 0555)}},
		SessionConfig: runtimekit.Config{Schema: "p.runtime-session/v1", Activation: "base", Command: []string{"/bin/bash", "-l"}},
		Workspace:     runtimekit.WorkspaceConfig{Schema: "p.workspace/v1", Repository: "team/app", Branch: "main"},
		Identity:      pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), ServerPublicKey: string(bytes.TrimSpace(ssh.MarshalAuthorizedKey(pub))),
	}
}

func TestAssemblyFixedPlanAndGeneratedSSH(t *testing.T) {
	a := assemblyFixture(t)
	files, err := validateAssembly(a)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]guestFile{}
	for _, f := range files {
		paths[f.path] = f.file
	}
	if len(paths) != 9 || paths["/etc/p/git/identity"].uid != 1000 || paths["/etc/p/git/identity"].mode != 0400 || !bytes.Contains(paths["/etc/p/git/ssh_config"].data, []byte("ProxyCommand /usr/libexec/p/runtime-kit git-stream")) {
		t.Fatalf("unsafe assembly files: %#v", paths)
	}
	a.HostAssets.Files[0].Data = []byte("tampered")
	if _, err := validateAssembly(a); err == nil {
		t.Fatal("changed verified asset accepted")
	}
	a = assemblyFixture(t)
	a.SourceAssets.Files[0].Destination = "/tmp/git-ssh"
	if _, err := validateAssembly(a); err == nil {
		t.Fatal("package-selected target accepted")
	}
	a = assemblyFixture(t)
	a.SessionConfig.Activation = "devshell"
	if _, err := validateAssembly(a); err == nil {
		t.Fatal("unimplemented devShell accepted")
	}
}

func TestAssemblyPinsOptionalAgentAssetBeforeAnyGuestEffect(t *testing.T) {
	a := assemblyFixture(t)
	data := []byte("#!/bin/sh\nexit 0\n")
	h := sha256.Sum256(data)
	sha := hex.EncodeToString(h[:])
	a.AgentAssets = &plugin.AssetPlan{Schema: "p.asset-plan/v1", PackageID: "org.p.codex-adapter", PackageSHA256: strings.Repeat("c", 64), Scope: "internal-session", Files: []plugin.AssetFile{{Role: "p-codex-adapter", Destination: "/usr/libexec/p/codex-adapter", Mode: 0555, SHA256: sha, Data: data}}}
	a.SessionConfig = runtimekit.Config{Schema: "p.runtime-session/v3", Activation: "base", AgentSHA256: sha, Command: []string{"/bin/bash", "-l"}}
	files, err := validateAssembly(a)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, f := range files {
		if f.path == "/etc/p/assets/p-codex-adapter" {
			found = f.file.typ == "file" && f.file.uid == 0 && f.file.gid == 0 && f.file.mode == 0555 && bytes.Equal(f.file.data, data)
		}
	}
	if !found {
		t.Fatal("selected adapter not installed at fixed root-owned target")
	}
	a.AgentAssets.Files[0].Destination = "/workspace/agent"
	if _, err := validateAssembly(a); err == nil {
		t.Fatal("plugin-selected adapter destination accepted")
	}
	a.AgentAssets.Files[0].Destination = "/usr/libexec/p/codex-adapter"
	a.AgentAssets.Files[0].Data = []byte("changed")
	if _, err := validateAssembly(a); err == nil {
		t.Fatal("changed adapter bytes accepted")
	}
	a.AgentAssets.Files[0].Data = data
	a.SessionConfig.AgentSHA256 = strings.Repeat("d", 64)
	if _, err := validateAssembly(a); err == nil {
		t.Fatal("runtime config accepted a different adapter digest")
	}
}

func TestAssemblyAcceptsOpenSSHSessionKey(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("OpenSSH unavailable")
	}
	path := filepath.Join(t.TempDir(), "session")
	cmd := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v: %s", err, out)
	}
	identity, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	a := assemblyFixture(t)
	a.Identity = identity
	if _, err := validateAssembly(a); err != nil {
		t.Fatalf("OpenSSH session key refused: %v", err)
	}
}

func TestAssemblyRejectsWorkspaceFromAnotherSessionBeforeIncus(t *testing.T) {
	a := assemblyFixture(t)
	f := &fakeIncus{instance: true, status: "Stopped"}
	b := fakeBackend(f)
	s := testSession()
	s.ProjectPath = "other"
	s.AssignedBranch = "main"
	if _, err := b.Assemble(context.Background(), s, a); err == nil || !strings.Contains(err.Error(), "bound session intent") {
		t.Fatalf("foreign workspace accepted: %v", err)
	}
	if len(f.mutations) != 0 {
		t.Fatalf("Incus mutated on binding mismatch: %v", f.mutations)
	}
}

func TestIncusFileAPIRequiresExplicit404ForAbsence(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "incus.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var status atomic.Int32
	status.Store(http.StatusNotFound)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/1.0/instances/p-session/files" || r.URL.Query().Get("project") != "confined" || r.URL.Query().Get("path") != "/etc/p/session.json" {
			t.Errorf("unscoped file request: %s", r.URL)
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(int(status.Load()))
	})}
	go server.Serve(listener)
	defer server.Close()
	api := &unixFileAPI{socket: socket, project: "confined", instance: "p-session"}
	_, exists, err := api.get(context.Background(), "/etc/p/session.json")
	if err != nil || exists {
		t.Fatalf("404: exists=%t err=%v", exists, err)
	}
	status.Store(http.StatusForbidden)
	_, exists, err = api.get(context.Background(), "/etc/p/session.json")
	if err == nil || exists {
		t.Fatalf("403 treated as absence: exists=%t err=%v", exists, err)
	}
}

func TestDanglingGuestSymlinkCannotBecomeMissingFile(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "incus.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var posts atomic.Int32
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			w.Header().Set("X-Incus-type", "symlink")
			w.Header().Set("X-Incus-uid", "0")
			w.Header().Set("X-Incus-gid", "0")
			w.Header().Set("X-Incus-mode", "0777")
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
		case http.MethodPost:
			posts.Add(1)
			w.WriteHeader(http.StatusOK)
		}
	})}
	go server.Serve(listener)
	defer server.Close()
	api := &unixFileAPI{socket: socket, project: "confined", instance: "p-session"}
	err = ensureGuestFile(context.Background(), api, "/etc/p/session.json", guestFile{typ: "file", uid: 0, gid: 0, mode: 0644, data: []byte("trusted")})
	if err == nil || !strings.Contains(err.Error(), "differs from trusted assembly") || posts.Load() != 0 {
		t.Fatalf("dangling symlink overwritten: err=%v posts=%d", err, posts.Load())
	}
}
