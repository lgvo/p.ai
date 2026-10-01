package runtimeincus

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWorkspaceChangesAcceptRealGitIgnoredDirectory(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal("Git is required by the package check inputs")
	}
	root, home := t.TempDir(), t.TempDir()
	run := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command(git, append([]string{"-C", root}, args...)...)
		cmd.Env = []string{
			"HOME=" + home, "XDG_CONFIG_HOME=" + home, "PATH=" + filepath.Dir(git),
			"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_COUNT=0",
			"GIT_CONFIG_PARAMETERS=", "GIT_ATTR_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid",
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Git %v: %v: %s", args, err, out)
		}
		return out
	}
	run("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("__pycache__/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", ".gitignore")
	run("commit", "-m", "Ignore generated Python cache")
	cache := filepath.Join(root, "examples", "notes", "__pycache__")
	if err := os.MkdirAll(cache, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "db.pyc"), []byte("sample-owned cache fixture"), 0644); err != nil {
		t.Fatal(err)
	}
	status := run("status", "--porcelain=v1", "-z", "--no-renames", "--ignore-submodules=all", "--untracked-files=all", "--ignored=matching")
	if !bytes.Equal(status, []byte("!! examples/notes/__pycache__/\x00")) {
		t.Fatalf("real Git ignored-directory output changed: %q", status)
	}
	changes, err := parseWorkspaceChanges(status)
	if err != nil {
		t.Fatal(err)
	}
	want := []WorkspaceChange{{Code: "!!", Path: "examples/notes/__pycache__"}}
	if !reflect.DeepEqual(changes, want) {
		t.Fatalf("changes: got %#v, want %#v", changes, want)
	}
}

func TestWorkspaceChangesIgnoredDirectoryKeepsPathBoundary(t *testing.T) {
	for _, entry := range []string{
		"!! /tmp/", "!! ../outside/", "!! cache/../../outside/", "!! ./cache/",
		"!! cache//", "!! cache/../outside/", "!! /", "!! ./", "!! ../",
		"?? cache/", " M cache/", "!! cache/\n/",
	} {
		t.Run(entry, func(t *testing.T) {
			if changes, err := parseWorkspaceChanges([]byte(entry + "\x00")); err == nil {
				t.Fatalf("unsupported path accepted: %#v", changes)
			}
		})
	}
	changes, err := parseWorkspaceChanges([]byte("!! ignored-file\x00?? regular-file\x00"))
	if err != nil || !reflect.DeepEqual(changes, []WorkspaceChange{{Code: "!!", Path: "ignored-file"}, {Code: "??", Path: "regular-file"}}) {
		t.Fatalf("ordinary file paths changed: %#v, %v", changes, err)
	}
}
