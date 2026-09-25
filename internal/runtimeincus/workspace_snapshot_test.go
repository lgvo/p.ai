package runtimeincus

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fakeWorkspaceReader struct {
	files    map[string]guestFile
	children map[string][]string
	links    map[string]string
	reads    []string
}

func (f *fakeWorkspaceReader) lstat(_ context.Context, name string) (guestFile, error) {
	f.reads = append(f.reads, "lstat:"+name)
	v, ok := f.files[name]
	if !ok {
		return guestFile{}, errors.New("missing")
	}
	return v, nil
}
func (f *fakeWorkspaceReader) listDirectory(_ context.Context, name string, max int) ([]string, error) {
	f.reads = append(f.reads, "list:"+name)
	v := f.children[name]
	if len(v) > max {
		return nil, errors.New("over bound")
	}
	return v, nil
}
func (f *fakeWorkspaceReader) getBounded(_ context.Context, name string, max int64) (guestFile, bool, error) {
	f.reads = append(f.reads, "get:"+name)
	v, ok := f.files[name]
	if !ok || int64(len(v.data)) > max {
		return guestFile{}, false, errors.New("file unavailable")
	}
	return v, true, nil
}
func (f *fakeWorkspaceReader) readlinkBounded(_ context.Context, name string, max int64) ([]byte, error) {
	f.reads = append(f.reads, "readlink:"+name)
	v, ok := f.links[name]
	if !ok || int64(len(v)) > max {
		return nil, errors.New("link unavailable")
	}
	return []byte(v), nil
}

func validWorkspaceReader() *fakeWorkspaceReader {
	file := func(data string) guestFile {
		return guestFile{typ: "file", uid: 1000, gid: 1000, mode: 0644, data: []byte(data)}
	}
	dir := guestFile{typ: "directory", uid: 1000, gid: 1000, mode: 0755}
	return &fakeWorkspaceReader{
		files: map[string]guestFile{
			"/workspace": dir, "/workspace/.config": dir, "/workspace/.config/settings": file("x"),
			"/workspace/.git": dir, "/workspace/.git/config": file("[core]\n repositoryformatversion = 0\n bare = false\n"),
			"/workspace/.git/HEAD": file("ref: refs/heads/main\n"), "/workspace/readme": file("hello"),
		},
		children: map[string][]string{"/workspace": {"readme", ".git", ".config"}, "/workspace/.config": {"settings"}, "/workspace/.git": {"HEAD", "config"}},
		links:    map[string]string{},
	}
}

func TestWorkspaceExportLstatsBeforeOpenAndFindsGitAfterEarlierDotEntry(t *testing.T) {
	f := validWorkspaceReader()
	snapshot, err := exportWorkspaceFiles(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{}
	for _, entry := range snapshot.Entries {
		paths = append(paths, entry.Path)
	}
	if !reflect.DeepEqual(paths, []string{"", ".config", ".config/settings", ".git", ".git/HEAD", ".git/config", "readme"}) {
		t.Fatal(paths)
	}
	for i, call := range f.reads {
		if strings.HasPrefix(call, "list:") {
			name := strings.TrimPrefix(call, "list:")
			if i == 0 || f.reads[i-1] != "lstat:"+name {
				t.Fatalf("directory opened without immediate LSTAT: %v", f.reads)
			}
		}
		if strings.HasPrefix(call, "get:") {
			name := strings.TrimPrefix(call, "get:")
			if i == 0 || f.reads[i-1] != "lstat:"+name {
				t.Fatalf("file opened without LSTAT: %v", f.reads)
			}
		}
	}
}

func TestWorkspaceExportRefusesEscapesAndUnsupportedLayouts(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*fakeWorkspaceReader)
	}{
		{"absolute symlink", func(f *fakeWorkspaceReader) {
			f.files["/workspace/escape"] = guestFile{typ: "symlink", uid: 1000, gid: 1000, mode: 0777}
			f.links["/workspace/escape"] = "/etc/shadow"
			f.children["/workspace"] = append(f.children["/workspace"], "escape")
		}},
		{"parent escape", func(f *fakeWorkspaceReader) {
			f.files["/workspace/escape"] = guestFile{typ: "symlink", uid: 1000, gid: 1000, mode: 0777}
			f.links["/workspace/escape"] = "../../etc"
			f.children["/workspace"] = append(f.children["/workspace"], "escape")
		}},
		{"Git indirection", func(f *fakeWorkspaceReader) {
			f.files["/workspace/.git/commondir"] = guestFile{typ: "file", uid: 1000, gid: 1000, mode: 0644, data: []byte("/elsewhere")}
			f.children["/workspace/.git"] = append(f.children["/workspace/.git"], "commondir")
		}},
		{"nested worktree", func(f *fakeWorkspaceReader) {
			f.files["/workspace/nested"] = guestFile{typ: "directory", uid: 1000, gid: 1000, mode: 0755}
			f.files["/workspace/nested/.git"] = guestFile{typ: "file", uid: 1000, gid: 1000, mode: 0644, data: []byte("gitdir: /elsewhere")}
			f.children["/workspace"] = append(f.children["/workspace"], "nested")
			f.children["/workspace/nested"] = []string{".git"}
		}},
		{"entry count", func(f *fakeWorkspaceReader) {
			for i := 0; i < workspaceMaxEntries; i++ {
				f.children["/workspace"] = append(f.children["/workspace"], "x")
			}
		}},
		{"unsafe child", func(f *fakeWorkspaceReader) { f.children["/workspace"] = append(f.children["/workspace"], "../secret") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := validWorkspaceReader()
			tc.edit(f)
			if snapshot, err := exportWorkspaceFiles(context.Background(), f); err == nil {
				t.Fatalf("unsupported snapshot accepted: %+v", snapshot)
			}
		})
	}
}

func TestWorkspaceLossTreeExportAllowsOnlyVerifiedLinkedLayout(t *testing.T) {
	f := validWorkspaceReader()
	file := func(data string) guestFile {
		return guestFile{typ: "file", uid: 1000, gid: 1000, mode: 0644, data: []byte(data)}
	}
	dir := guestFile{typ: "directory", uid: 1000, gid: 1000, mode: 0755}
	f.files["/workspace/.git/worktrees"] = dir
	f.files["/workspace/.git/worktrees/side"] = dir
	f.files["/workspace/.git/worktrees/side/gitdir"] = file("/home/p/worktrees/side/.git\n")
	f.files["/workspace/.git/worktrees/side/commondir"] = file("../..\n")
	f.files["/workspace/.git/worktrees/side/HEAD"] = file("ref: refs/heads/side\n")
	f.children["/workspace/.git"] = append(f.children["/workspace/.git"], "worktrees")
	f.children["/workspace/.git/worktrees"] = []string{"side"}
	f.children["/workspace/.git/worktrees/side"] = []string{"gitdir", "commondir", "HEAD"}
	f.files["/home"] = guestFile{typ: "directory", uid: 0, gid: 0, mode: 0755}
	f.files["/home/p"] = guestFile{typ: "directory", uid: 1000, gid: 1000, mode: 0700}
	f.files["/home/p/worktrees"] = guestFile{typ: "directory", uid: 1000, gid: 1000, mode: 0700}
	f.files["/home/p/worktrees/side"] = dir
	f.files["/home/p/worktrees/side/.git"] = file("gitdir: /workspace/.git/worktrees/side\n")
	f.files["/home/p/worktrees/side/tracked"] = file("side")
	f.children["/home/p/worktrees/side"] = []string{".git", "tracked"}
	if _, err := exportWorkspaceFiles(context.Background(), f); err == nil {
		t.Fatal("v1 inspector accepted linked worktree")
	}
	main, err := exportWorkspaceFilesPolicy(context.Background(), f, true)
	if err != nil {
		t.Fatal(err)
	}
	links, err := parseWorkspaceLinkedTrees(main)
	if err != nil || len(links) != 1 {
		t.Fatalf("linked inventory: %+v %v", links, err)
	}
	linked, err := exportWorkspaceTree(context.Background(), f, links[0].Root, true, "file")
	if err != nil || len(linked.Entries) != 3 {
		t.Fatalf("linked copy: %+v %v", linked, err)
	}
	if err := verifyLinkedGitFile(links[0], linked.Entries[1]); err != nil {
		t.Fatal(err)
	}
	f.files["/home/p/worktrees"] = guestFile{typ: "symlink", uid: 1000, gid: 1000, mode: 0777}
	if _, err := exportWorkspaceTree(context.Background(), f, links[0].Root, true, "file"); err == nil {
		t.Fatal("linked copy followed parent symlink")
	}
}

func TestWorkspaceLossProtectedGitRootRefusesBeforePrivateHomeRead(t *testing.T) {
	reader := validWorkspaceReader()
	reader.files["/home/p/.codex/auth.json"] = guestFile{typ: "file", uid: 1000, gid: 1000, mode: 0600, data: []byte("dummy private credential")}
	main := linkedLayout("/home/p")
	if _, err := exportKnownLinkedTrees(context.Background(), reader, main, nil); err == nil {
		t.Fatal("protected Git-known root accepted")
	}
	if len(reader.reads) != 0 {
		t.Fatalf("source path opened before protected-root refusal: %v", reader.reads)
	}
}

func TestWorkspaceGitConfigRejectsExecutableControlsAndSanitizesIdentity(t *testing.T) {
	base := "[core]\n repositoryformatversion = 0\n bare = false\n[remote \"origin\"]\n url = ssh://example.invalid/repo\n fetch = +refs/heads/*:refs/remotes/origin/*\n[user]\n name = Alice\n email = a@example.invalid\n[branch \"main\"]\n remote = origin\n merge = refs/heads/main\n"
	out, err := safeGitConfig([]byte(base))
	if err != nil || strings.Contains(string(out), "ssh://") || strings.Contains(string(out), "Alice") || !strings.Contains(string(out), "merge = refs/heads/main") {
		t.Fatalf("safe config: %q %v", out, err)
	}
	for _, malicious := range []string{
		base + "[include]\n path = /tmp/evil\n", base + "[filter \"evil\"]\n clean = /tmp/evil\n",
		base + "[core]\n fsmonitor = /tmp/evil\n", base + "[diff]\n external = /tmp/evil\n",
		base + "[core]\n worktree = /tmp/evil\n",
	} {
		if _, err := safeGitConfig([]byte(malicious)); err == nil {
			t.Fatalf("executable config accepted: %q", malicious)
		}
	}
	if _, err := sanitizeWorkspaceSnapshot(WorkspaceSnapshot{Entries: []WorkspaceEntry{{Path: "", Type: "directory"}, {Path: ".git", Type: "directory"}, {Path: ".git/config", Type: "file", Data: []byte(base)}, {Path: ".git/HEAD", Type: "file", Data: []byte("ref: refs/heads/main\n")}, {Path: ".gitattributes", Type: "file", Data: []byte("*.txt filter=evil\n")}}}); err == nil {
		t.Fatal("attributes filter semantics accepted")
	}
}

func TestWorkspaceSafeGitConfigPreservesStatusAndUpstream(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal("Git is required by the package check inputs")
	}
	root := t.TempDir()
	home := t.TempDir()
	run := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command(git, append([]string{"-C", root}, args...)...)
		cmd.Env = []string{"HOME=" + home, "XDG_CONFIG_HOME=" + home, "PATH=" + filepath.Dir(git),
			"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_COUNT=0",
			"GIT_CONFIG_PARAMETERS=", "GIT_ATTR_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
			"GIT_OPTIONAL_LOCKS=0", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Git %v: %v: %s", args, err, out)
		}
		return out
	}
	run("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(root, "tracked"), []byte("one\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", "tracked")
	run("commit", "-m", "seed")
	run("remote", "add", "origin", "https://example.invalid/repo")
	run("update-ref", "refs/remotes/origin/main", "HEAD")
	run("config", "branch.main.remote", "origin")
	run("config", "branch.main.merge", "refs/heads/main")
	run("config", "core.filemode", "false")
	if err := os.Chmod(filepath.Join(root, "tracked"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "new"), []byte("two\n"), 0644); err != nil {
		t.Fatal(err)
	}
	statusArgs := []string{"status", "--porcelain=v1", "-z", "--no-renames", "--ignore-submodules=all", "--untracked-files=all", "--ignored=matching"}
	beforeStatus := run(statusArgs...)
	beforeUpstream := run("for-each-ref", "--format=%(upstream)", "refs/heads/main")
	if strings.TrimSpace(string(beforeUpstream)) != "refs/remotes/origin/main" {
		t.Fatalf("original upstream not established: %q", beforeUpstream)
	}
	configPath := filepath.Join(root, ".git", "config")
	original, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	sanitized, err := safeGitConfig(original)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, sanitized, 0600); err != nil {
		t.Fatal(err)
	}
	afterStatus := run(statusArgs...)
	afterUpstream := run("for-each-ref", "--format=%(upstream)", "refs/heads/main")
	if !reflect.DeepEqual(beforeStatus, afterStatus) || !reflect.DeepEqual(beforeUpstream, afterUpstream) {
		t.Fatalf("sanitized config changed Git meaning: status %q -> %q, upstream %q -> %q", beforeStatus, afterStatus, beforeUpstream, afterUpstream)
	}
	if strings.Contains(string(sanitized), "example.invalid") || strings.Contains(string(sanitized), "Test") {
		t.Fatalf("sanitized config retained remote URL or identity: %q", sanitized)
	}
}
