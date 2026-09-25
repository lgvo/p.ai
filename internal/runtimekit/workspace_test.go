package runtimekit

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestWorkspaceBootstrapKeepsUnbornMainAndRetainedFiles(t *testing.T) {
	base := t.TempDir()
	remote := filepath.Join(base, "remote.git")
	work := filepath.Join(base, "work")
	gitTest(t, base, "init", "--bare", remote)
	if err := os.Mkdir(work, 0755); err != nil {
		t.Fatal(err)
	}
	c := WorkspaceConfig{Schema: "p.workspace/v1", Repository: "app", Branch: "main"}
	if err := initWorkspaceRemote(c, work, "git", remote); err != nil {
		t.Fatal(err)
	}
	if got := gitTest(t, work, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Fatalf("HEAD=%s", got)
	}
	if err := os.WriteFile(filepath.Join(work, "retained"), []byte("user change"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := initWorkspaceRemote(c, work, "git", remote); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(work, "retained")); err != nil || string(data) != "user change" {
		t.Fatal("retained file changed", err)
	}
	if err := os.WriteFile(filepath.Join(work, "flake.nix"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "add", "flake.nix")
	gitTest(t, work, "-c", "user.name=P", "-c", "user.email=p@example.invalid", "commit", "-m", "user flake")
	gitTest(t, work, "checkout", "-b", "local-work")
	gitTest(t, work, "remote", "set-url", "origin", filepath.Join(base, "later-remote.git"))
	if err := initWorkspaceRemote(c, work, "git", remote); err != nil {
		t.Fatalf("later user flake blocked Start: %v", err)
	}
	if got := gitTest(t, work, "symbolic-ref", "HEAD"); got != "refs/heads/local-work" {
		t.Fatalf("retained HEAD reset to %s", got)
	}
	if data, err := os.ReadFile(filepath.Join(work, "retained")); err != nil || string(data) != "user change" {
		t.Fatalf("retained dirty file changed: %v", err)
	}
	if err := os.Remove(filepath.Join(work, ".git", "p-initialized")); err != nil {
		t.Fatal(err)
	}
	if err := initWorkspaceRemote(c, work, "git", remote); err == nil || !strings.Contains(err.Error(), "partial") {
		t.Fatalf("partial workspace accepted: %v", err)
	}
}

func TestWorkspaceCapturedOIDAndRemoteMovement(t *testing.T) {
	base := t.TempDir()
	remote := filepath.Join(base, "remote.git")
	seed := filepath.Join(base, "seed")
	gitTest(t, base, "init", "--bare", remote)
	gitTest(t, base, "init", "-b", "work", seed)
	if err := os.WriteFile(filepath.Join(seed, "tracked"), []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, seed, "add", "tracked")
	gitTest(t, seed, "-c", "user.name=P", "-c", "user.email=p@example.invalid", "commit", "-m", "first")
	gitTest(t, seed, "remote", "add", "origin", remote)
	gitTest(t, seed, "push", "origin", "work")
	oid := gitTest(t, seed, "rev-parse", "HEAD")
	c := WorkspaceConfig{Schema: "p.workspace/v1", Repository: "app", Branch: "work", InitialOID: oid}
	work := filepath.Join(base, "work")
	if err := os.Mkdir(work, 0755); err != nil {
		t.Fatal(err)
	}
	if err := initWorkspaceRemote(c, work, "git", remote); err != nil {
		t.Fatal(err)
	}
	if got := gitTest(t, work, "rev-parse", "HEAD"); got != oid {
		t.Fatalf("wrong captured tip: %s", got)
	}
	if got := gitTest(t, work, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); got != "origin/work" {
		t.Fatalf("upstream=%s", got)
	}
	if err := os.WriteFile(filepath.Join(seed, "tracked"), []byte("second"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, seed, "add", "tracked")
	gitTest(t, seed, "-c", "user.name=P", "-c", "user.email=p@example.invalid", "commit", "-m", "second")
	gitTest(t, seed, "push", "origin", "work")
	moved := filepath.Join(base, "moved")
	if err := os.Mkdir(moved, 0755); err != nil {
		t.Fatal(err)
	}
	if err := initWorkspaceRemote(c, moved, "git", remote); err == nil || !strings.Contains(err.Error(), "moved") {
		t.Fatalf("moved tip accepted: %v", err)
	}
}

func TestWorkspaceConfigRejectsInvalidBranches(t *testing.T) {
	for _, raw := range []string{
		`{"schema":"p.workspace/v1","repository":"app","branch":"feature"}`,
		`{"schema":"p.workspace/v1","repository":"../app","branch":"main"}`,
		`{"schema":"p.workspace/v1","repository":"app","branch":"main","initial_oid":"deadbeef"}`,
		`{"schema":"p.workspace/v1","repository":"app","branch":"main","branch":"main"}`,
	} {
		if _, err := ParseWorkspaceConfig([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestWorkspaceRefusesCapturedRootFlake(t *testing.T) {
	base := t.TempDir()
	remote := filepath.Join(base, "remote.git")
	seed := filepath.Join(base, "seed")
	work := filepath.Join(base, "work")
	gitTest(t, base, "init", "--bare", remote)
	gitTest(t, base, "init", "-b", "main", seed)
	if err := os.WriteFile(filepath.Join(seed, "flake.nix"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, seed, "add", "flake.nix")
	gitTest(t, seed, "-c", "user.name=P", "-c", "user.email=p@example.invalid", "commit", "-m", "flake")
	gitTest(t, seed, "remote", "add", "origin", remote)
	gitTest(t, seed, "push", "origin", "main")
	oid := gitTest(t, seed, "rev-parse", "HEAD")
	if err := os.Mkdir(work, 0755); err != nil {
		t.Fatal(err)
	}
	c := WorkspaceConfig{Schema: "p.workspace/v1", Repository: "app", Branch: "main", InitialOID: oid}
	if err := initWorkspaceRemote(c, work, "git", remote); err == nil || !strings.Contains(err.Error(), "devShell") {
		t.Fatalf("captured flake silently booted base: %v", err)
	}
	for _, selection := range []string{"base-no-flake", "base-no-default", "devshell"} {
		accepted := c
		accepted.Schema = "p.workspace/v2"
		accepted.EnvironmentCommitOID = oid
		accepted.EnvironmentSelection = selection
		if selection == "devshell" {
			accepted.EnvironmentKey = strings.Repeat("a", 64)
		}
		raw, err := json.Marshal(accepted)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseWorkspaceConfig(raw); err != nil {
			t.Fatalf("accepted resolution config %s: %v", selection, err)
		}
		fresh := filepath.Join(base, selection)
		if err := os.Mkdir(fresh, 0755); err != nil {
			t.Fatal(err)
		}
		err = initWorkspaceRemote(accepted, fresh, "git", remote)
		if selection == "base-no-flake" {
			if err == nil || !strings.Contains(err.Error(), "devShell") {
				t.Fatalf("unresolved flake accepted as no-flake: %v", err)
			}
			continue
		}
		if err != nil || gitTest(t, fresh, "rev-parse", "HEAD") != oid {
			t.Fatalf("accepted %s did not initialize captured commit: %v", selection, err)
		}
		if err := os.WriteFile(filepath.Join(fresh, "user-change"), []byte("retained"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := initWorkspaceRemote(accepted, fresh, "git", remote); err != nil {
			t.Fatalf("retained %s start: %v", selection, err)
		}
		if got, err := os.ReadFile(filepath.Join(fresh, "user-change")); err != nil || string(got) != "retained" {
			t.Fatalf("retained workspace changed: %q %v", got, err)
		}
	}
	invalid := c
	invalid.Schema = "p.workspace/v2"
	invalid.EnvironmentCommitOID = strings.Repeat("b", 40)
	invalid.EnvironmentSelection = "base-no-default"
	raw, _ := json.Marshal(invalid)
	if _, err := ParseWorkspaceConfig(raw); err == nil {
		t.Fatal("resolution for a different commit accepted")
	}
	for _, selection := range []string{"base-no-flake", "base-no-default", "devshell"} {
		plain, plainRemote, plainWork, _ := workspaceFixture(t, false)
		plain.Schema = "p.workspace/v2"
		plain.EnvironmentCommitOID = plain.InitialOID
		plain.EnvironmentSelection = selection
		if selection == "devshell" {
			plain.EnvironmentKey = strings.Repeat("a", 64)
		}
		err := initWorkspaceRemote(plain, plainWork, "git", plainRemote)
		if selection == "base-no-flake" {
			if err != nil {
				t.Fatalf("accepted absent flake rejected: %v", err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "expected a captured root flake") {
			t.Fatalf("forged %s accepted without flake: %v", selection, err)
		}
	}
}

func TestRepairWorkspaceUsesCurrentTipWithRecordedImageProvenance(t *testing.T) {
	for _, tc := range []struct {
		name, firstFile, firstContents, nextFile, nextContents, selection, key string
	}{
		{"base-image-flake-added", "tracked", "base\n", "flake.nix", "{}\n", "base-no-flake", ""},
		{"devshell-image-flake-removed", "flake.nix", "{}\n", "tracked", "later\n", "devshell", strings.Repeat("a", 64)},
		{"devshell-image-flake-changed", "flake.nix", "{ old = true; }\n", "flake.nix", "{ new = true; }\n", "devshell", strings.Repeat("a", 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			remote := filepath.Join(base, "remote.git")
			seed := filepath.Join(base, "seed")
			work := filepath.Join(base, "work")
			gitTest(t, base, "init", "--bare", remote)
			gitTest(t, base, "init", "-b", "main", seed)
			if err := os.WriteFile(filepath.Join(seed, tc.firstFile), []byte(tc.firstContents), 0600); err != nil {
				t.Fatal(err)
			}
			gitTest(t, seed, "add", ".")
			gitTest(t, seed, "-c", "user.name=P", "-c", "user.email=p@example.invalid", "commit", "-m", "image source")
			source := gitTest(t, seed, "rev-parse", "HEAD")
			if tc.firstFile == "flake.nix" && tc.nextFile != "flake.nix" {
				gitTest(t, seed, "rm", "flake.nix")
			}
			if err := os.WriteFile(filepath.Join(seed, tc.nextFile), []byte(tc.nextContents), 0600); err != nil {
				t.Fatal(err)
			}
			gitTest(t, seed, "add", ".")
			gitTest(t, seed, "-c", "user.name=P", "-c", "user.email=p@example.invalid", "commit", "-m", "current assigned tip")
			current := gitTest(t, seed, "rev-parse", "HEAD")
			gitTest(t, seed, "remote", "add", "origin", remote)
			gitTest(t, seed, "push", "origin", "main")
			cfg := WorkspaceConfig{Schema: "p.workspace/v3", Repository: "app", Branch: "main", InitialOID: current,
				EnvironmentCommitOID: source, EnvironmentSelection: tc.selection, EnvironmentKey: tc.key,
				ImageFingerprint: strings.Repeat("b", 64)}
			raw, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseWorkspaceConfig(raw); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(work, 0755); err != nil {
				t.Fatal(err)
			}
			if err := initWorkspaceRemote(cfg, work, "git", remote); err != nil {
				t.Fatalf("repair workspace: %v", err)
			}
			if got := gitTest(t, work, "rev-parse", "HEAD"); got != current {
				t.Fatalf("repaired tip=%s, want %s", got, current)
			}
			if data, err := os.ReadFile(filepath.Join(work, tc.nextFile)); err != nil || string(data) != tc.nextContents {
				t.Fatalf("current bytes unavailable: %q %v", data, err)
			}
			cfg.ImageFingerprint = ""
			bad, _ := json.Marshal(cfg)
			if _, err := ParseWorkspaceConfig(bad); err == nil {
				t.Fatal("repair without recorded image identity accepted")
			}
			cfg.ImageFingerprint = strings.Repeat("b", 64)
			cfg.EnvironmentCommitOID = "not-an-oid"
			bad, _ = json.Marshal(cfg)
			if _, err := ParseWorkspaceConfig(bad); err == nil {
				t.Fatal("invalid image source commit accepted")
			}
		})
	}
}
