package gitservice

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/plugin"
)

func snapshotGit(t *testing.T, repo string, input []byte, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=P Test", "GIT_AUTHOR_EMAIL=p@example.test", "GIT_COMMITTER_NAME=P Test", "GIT_COMMITTER_EMAIL=p@example.test")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func snapshotFixture(t *testing.T) (*Backend, string) {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git unavailable")
	}
	state := t.TempDir()
	if err := os.Mkdir(filepath.Join(state, "repositories"), 0700); err != nil {
		t.Fatal(err)
	}
	b := &Backend{stateDir: state, gitPath: gitPath}
	repo, err := b.repository("app")
	if err != nil {
		t.Fatal(err)
	}
	snapshotGit(t, state, nil, "init", "--bare", "--initial-branch=main", repo)
	return b, repo
}

func snapshotBlob(t *testing.T, repo, content string) string {
	t.Helper()
	return snapshotGit(t, repo, []byte(content), "hash-object", "-w", "--stdin")
}

func snapshotTree(t *testing.T, repo string, entries ...string) string {
	t.Helper()
	return snapshotGit(t, repo, []byte(strings.Join(entries, "\x00")+"\x00"), "mktree", "-z")
}

func snapshotCommit(t *testing.T, repo, tree, parent string) string {
	t.Helper()
	args := []string{"commit-tree", tree}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	return snapshotGit(t, repo, []byte("commit\n"), args...)
}

func TestCommittedSnapshotPreservesGitTreeAfterBranchMoves(t *testing.T) {
	b, repo := snapshotFixture(t)
	flake := snapshotBlob(t, repo, "{ outputs = _: {}; }\n")
	attrs := snapshotBlob(t, repo, "flake.nix export-ignore\n")
	script := snapshotBlob(t, repo, "#!/bin/sh\nexit 0\n")
	link := snapshotBlob(t, repo, "scripts/build")
	sub := snapshotTree(t, repo, "100755 blob "+script+"\tbuild")
	firstTree := snapshotTree(t, repo,
		"100644 blob "+flake+"\tflake.nix",
		"100644 blob "+attrs+"\t.gitattributes",
		"040000 tree "+sub+"\tscripts",
		"120000 blob "+link+"\tbuild-link")
	first := snapshotCommit(t, repo, firstTree, "")
	snapshotGit(t, repo, nil, "update-ref", "refs/heads/main", first)
	secondTree := snapshotTree(t, repo, "100644 blob "+snapshotBlob(t, repo, "changed\n")+"\tflake.nix")
	second := snapshotCommit(t, repo, secondTree, first)
	snapshotGit(t, repo, nil, "update-ref", "refs/heads/main", second)
	s, err := b.captureCommit(context.Background(), "app", first)
	if err != nil {
		t.Fatal(err)
	}
	if s.CommitOID() != first || s.TreeOID() != firstTree || s.Entries() != 5 {
		t.Fatalf("snapshot identity: %+v", s)
	}
	if got, err := os.ReadFile(filepath.Join(s.Path(), "flake.nix")); err != nil || string(got) != "{ outputs = _: {}; }\n" {
		t.Fatalf("committed flake: %q %v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(s.Path(), ".gitattributes")); err != nil {
		t.Fatalf("attributes omitted: %v", err)
	}
	if target, err := os.Readlink(filepath.Join(s.Path(), "build-link")); err != nil || target != "scripts/build" {
		t.Fatalf("symlink: %q %v", target, err)
	}
	if info, err := os.Stat(filepath.Join(s.Path(), "scripts/build")); err != nil || info.Mode().Perm() != 0500 {
		t.Fatalf("executable mode: %v %v", info, err)
	}
	if info, err := os.Stat(s.Path()); err != nil || info.Mode().Perm() != 0500 {
		t.Fatalf("source mode: %v %v", info, err)
	}
	path := s.Path()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot cleanup: %v", err)
	}
	if _, err := os.Stat(repo); err != nil {
		t.Fatalf("cleanup touched repository: %v", err)
	}
}

func TestSnapshotRejectsSubmoduleAndEscapingLinkWithCleanup(t *testing.T) {
	b, repo := snapshotFixture(t)
	empty := snapshotTree(t, repo, "100644 blob "+snapshotBlob(t, repo, "ok")+"\tok")
	base := snapshotCommit(t, repo, empty, "")
	for name, entries := range map[string][]string{
		"submodule":     {"160000 commit " + base + "\tdependency"},
		"escaping link": {"120000 blob " + snapshotBlob(t, repo, "../outside") + "\tlink"},
		"large link":    {"120000 blob " + snapshotBlob(t, repo, strings.Repeat("a", int(SnapshotMaxSymlinkBytes)+1)) + "\tlink"},
		"control path":  {"100644 blob " + snapshotBlob(t, repo, "data") + "\tbad\nname"},
		"link cycle": {
			"120000 blob " + snapshotBlob(t, repo, "b") + "\ta",
			"120000 blob " + snapshotBlob(t, repo, "a") + "\tb",
		},
	} {
		t.Run(name, func(t *testing.T) {
			tree := snapshotTree(t, repo, entries...)
			commit := snapshotCommit(t, repo, tree, base)
			if s, err := b.captureCommit(context.Background(), "app", commit); err == nil || s != nil {
				t.Fatalf("accepted %s: %+v %v", name, s, err)
			}
			children, err := os.ReadDir(filepath.Join(b.stateDir, "source-snapshots"))
			if err != nil || len(children) != 0 {
				t.Fatalf("staging leaked: %v %v", children, err)
			}
		})
	}
}

func TestSnapshotPathBoundsAndCancellation(t *testing.T) {
	for _, name := range []string{"../x", "/x", "a/b", ".git", ".GIT", "bad\nname", "\xff"} {
		if safeSnapshotName(name) {
			t.Errorf("accepted unsafe name %q", name)
		}
	}
	for _, target := range []string{"/etc/passwd", "../../outside", "", "bad\nlink"} {
		if safeSnapshotLink("folder/link", target) {
			t.Errorf("accepted unsafe link %q", target)
		}
	}
	b, repo := snapshotFixture(t)
	tree := snapshotTree(t, repo, "100644 blob "+snapshotBlob(t, repo, "ok")+"\tfile")
	commit := snapshotCommit(t, repo, tree, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s, err := b.captureCommit(ctx, "app", commit); !errors.Is(err, context.Canceled) || s != nil {
		t.Fatalf("canceled capture: %+v %v", s, err)
	}
}

func TestSnapshotValidatesSymlinksAgainstCompletedTree(t *testing.T) {
	b, repo := snapshotFixture(t)
	outside := filepath.Join(b.stateDir, "source-snapshots", "outside")
	if err := os.Mkdir(filepath.Dir(outside), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("host-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	back := snapshotBlob(t, repo, "..")
	dir := snapshotTree(t, repo, "120000 blob "+back+"\tb")
	escape := snapshotBlob(t, repo, "dir/b/../outside")
	badTree := snapshotTree(t, repo, "120000 blob "+escape+"\ta", "040000 tree "+dir+"\tdir")
	badCommit := snapshotCommit(t, repo, badTree, "")
	if s, err := b.captureCommit(context.Background(), "app", badCommit); err == nil || s != nil {
		t.Fatalf("symlink chain escaped root: %+v %v", s, err)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "host-secret" {
		t.Fatalf("outside file touched: %q %v", data, err)
	}
	children, err := os.ReadDir(filepath.Dir(outside))
	if err != nil || len(children) != 1 {
		t.Fatalf("failed snapshot leaked: %v %v", children, err)
	}

	file := snapshotBlob(t, repo, "inside")
	up := snapshotBlob(t, repo, "../file")
	goodDir := snapshotTree(t, repo, "120000 blob "+up+"\tup")
	goodTree := snapshotTree(t, repo, "100644 blob "+file+"\tfile", "040000 tree "+goodDir+"\tdir")
	goodCommit := snapshotCommit(t, repo, goodTree, "")
	s, err := b.captureCommit(context.Background(), "app", goodCommit)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if data, err := os.ReadFile(filepath.Join(s.Path(), "dir", "up")); err != nil || string(data) != "inside" {
		t.Fatalf("safe parent link: %q %v", data, err)
	}
}

func TestCaptureCommittedSourceSelectedWASIAndReachability(t *testing.T) {
	b, repo := snapshotFixture(t)
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Skip("Go compiler unavailable")
	}
	pkgDir := t.TempDir()
	for _, name := range []string{"plugin.json", "p-git-ssh"} {
		data, err := os.ReadFile(filepath.Join("../../plugins/bundled/source-git", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pkgDir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	build := exec.Command(goPath, "build", "-o", filepath.Join(pkgDir, "git.wasm"), "./plugins/bundled/source-git")
	build.Dir = filepath.Join("..", "..")
	build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0", "GOTOOLCHAIN=local")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build selected source-Git WASI: %v: %s", err, out)
	}
	pkg, err := plugin.Conformance(pkgDir)
	if err != nil {
		t.Fatal(err)
	}
	b.selection = plugin.Active{Package: pkg, Grants: []string{"git.project"}}
	firstTree := snapshotTree(t, repo, "100644 blob "+snapshotBlob(t, repo, "first")+"\tflake.nix")
	first := snapshotCommit(t, repo, firstTree, "")
	secondTree := snapshotTree(t, repo, "100644 blob "+snapshotBlob(t, repo, "second")+"\tflake.nix")
	second := snapshotCommit(t, repo, secondTree, first)
	hiddenTree := snapshotTree(t, repo, "100644 blob "+snapshotBlob(t, repo, "hidden")+"\tflake.nix")
	hidden := snapshotCommit(t, repo, hiddenTree, "")
	snapshotGit(t, repo, nil, "update-ref", "refs/heads/main", first)
	snapshotGit(t, repo, nil, "update-ref", "refs/hidden/only", hidden)
	ctx := context.Background()
	s, err := b.CaptureCommittedSource(ctx, "app", plugin.GitSourceSelector{Kind: "branch", Value: "refs/heads/main"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.CommitOID() != first || s.TreeOID() != firstTree || s.Project() != "app" {
		t.Fatalf("wrong selected identity: %s %s %s", s.Project(), s.CommitOID(), s.TreeOID())
	}
	snapshotGit(t, repo, nil, "update-ref", "refs/heads/main", second)
	if data, err := os.ReadFile(filepath.Join(s.Path(), "flake.nix")); err != nil || string(data) != "first" {
		t.Fatalf("branch move changed captured bytes: %q %v", data, err)
	}
	ancestor, err := b.CaptureCommittedSource(ctx, "app", plugin.GitSourceSelector{Kind: "commit", Value: first})
	if err != nil {
		t.Fatalf("reachable captured commit: %v", err)
	}
	defer ancestor.Close()
	if ancestor.CommitOID() != first {
		t.Fatalf("reachable commit changed: %s", ancestor.CommitOID())
	}
	if rejected, err := b.CaptureCommittedSource(ctx, "app", plugin.GitSourceSelector{Kind: "commit", Value: hidden}); err == nil || rejected != nil {
		t.Fatalf("hidden-only commit accepted: %+v %v", rejected, err)
	}
	if rejected, err := b.CaptureCommittedSource(ctx, "app", plugin.GitSourceSelector{Kind: "branch", Value: "refs/heads/missing"}); err == nil || rejected != nil {
		t.Fatalf("missing branch accepted: %+v %v", rejected, err)
	}
}
