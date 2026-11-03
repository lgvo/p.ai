package runtimekit

import (
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
	if got := gitTest(t, work, "config", "branch.main.remote"); got != "origin" {
		t.Fatalf("bootstrap remote=%s", got)
	}
	if got := gitTest(t, work, "config", "branch.main.merge"); got != "refs/heads/main" {
		t.Fatalf("bootstrap merge=%s", got)
	}
	if got := gitTest(t, work, "for-each-ref", "--format=%(refname)"); got != "" {
		t.Fatalf("bootstrap invented refs=%s", got)
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
	gitTest(t, work, "config", "branch.main.merge", "refs/heads/manual-choice")
	if err := initWorkspaceRemote(c, work, "git", remote); err != nil {
		t.Fatalf("later user flake blocked Start: %v", err)
	}
	if got := gitTest(t, work, "symbolic-ref", "HEAD"); got != "refs/heads/local-work" {
		t.Fatalf("retained HEAD reset to %s", got)
	}
	if got := gitTest(t, work, "config", "branch.main.merge"); got != "refs/heads/manual-choice" {
		t.Fatalf("ordinary Start repaired user upstream=%s", got)
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

func TestWorkspaceConfigV2NeedsTheCapturedCommitAndADevShellSelection(t *testing.T) {
	oid := strings.Repeat("a", 40)
	for _, selection := range []string{"no-flake", "no-default", "devshell"} {
		raw := `{"schema":"p.workspace/v2","repository":"team/app","branch":"feature","initial_oid":"` + oid + `","environment_selection":"` + selection + `"}`
		c, err := ParseWorkspaceConfig([]byte(raw))
		if err != nil {
			t.Fatalf("refused %s: %v", raw, err)
		}
		if want := (WorkspaceConfig{Schema: "p.workspace/v2", Repository: "team/app", Branch: "feature", InitialOID: oid, EnvironmentSelection: selection}); c != want {
			t.Fatalf("parsed %+v, want %+v", c, want)
		}
	}
	for _, raw := range []string{
		`{"schema":"p.workspace/v2","repository":"app","branch":"main","environment_selection":"no-flake"}`,
		`{"schema":"p.workspace/v2","repository":"app","branch":"main","initial_oid":"` + oid + `"}`,
		`{"schema":"p.workspace/v2","repository":"app","branch":"main","initial_oid":"` + oid + `","environment_selection":"base-no-flake"}`,
		`{"schema":"p.workspace/v2","repository":"app","branch":"main","initial_oid":"` + oid + `","environment_selection":"devshell","environment_key":"` + strings.Repeat("b", 64) + `"}`,
		`{"schema":"p.workspace/v2","repository":"app","branch":"main","initial_oid":"` + oid + `","environment_selection":"no-default","environment_commit_oid":"` + oid + `"}`,
		`{"schema":"p.workspace/v1","repository":"app","branch":"main","initial_oid":"` + oid + `","environment_selection":"no-flake"}`,
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
}

func TestWorkspaceCloneFromPIsOnlyForV1(t *testing.T) {
	c, remote, work, _ := workspaceFixture(t, false)
	c.Schema = "p.workspace/v2"
	c.EnvironmentSelection = "no-flake"
	if err := initWorkspaceRemote(c, work, "git", remote); err == nil {
		t.Fatal("v2 workspace cloned from P")
	}
	if err := emptyWorkspace(work); err != nil {
		t.Fatal(err)
	}
}
