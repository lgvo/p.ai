package gitservice

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
)

func exportDirectory(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "staging")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func directoryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestExportBundleHoldsExactlyTheCommitAndItsHistory(t *testing.T) {
	b, repo := bareFixture(t)
	flake := bareBlob(t, repo, "{ outputs = _: {}; }\n")
	script := bareBlob(t, repo, "#!/bin/sh\nexit 0\n")
	root := bareCommit(t, repo, bareTree(t, repo, "100644 blob "+flake+"\tflake.nix"), "")
	exported := bareCommit(t, repo, bareTree(t, repo,
		"100644 blob "+flake+"\tflake.nix",
		"100644 blob "+bareBlob(t, repo, "flake.nix export-ignore\n")+"\t.gitattributes",
		"040000 tree "+bareTree(t, repo, "100755 blob "+script+"\tbuild")+"\tscripts",
		"120000 blob "+bareBlob(t, repo, "scripts/build")+"\tbuild-link"), root)
	later := bareCommit(t, repo, bareTree(t, repo, "100644 blob "+bareBlob(t, repo, "later\n")+"\tflake.nix"), exported)
	bareGit(t, repo, nil, "update-ref", "refs/heads/main", later)
	refs, objects := bareGit(t, repo, nil, "for-each-ref"), bareGit(t, repo, nil, "count-objects", "-v")
	dir := exportDirectory(t)

	bundle, err := b.ExportBundle(context.Background(), "app", exported, dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Bundle{Path: filepath.Join(dir, "source.bundle"), Ref: "refs/p/source", Commit: exported}); bundle != want {
		t.Fatalf("bundle = %+v, want %+v", bundle, want)
	}
	if heads := bareGit(t, dir, nil, "bundle", "list-heads", bundle.Path); heads != exported+" refs/p/source" {
		t.Fatalf("bundle heads: %q", heads)
	}
	clone := filepath.Join(t.TempDir(), "clone")
	gitTest(t, nil, "clone", "--quiet", "--revision=refs/p/source", bundle.Path, clone)
	if head := bareGit(t, clone, nil, "rev-parse", "HEAD"); head != exported {
		t.Fatalf("clone HEAD %s, want %s", head, exported)
	}
	if got, want := bareGit(t, clone, nil, "ls-tree", "-r", "HEAD"), bareGit(t, repo, nil, "ls-tree", "-r", exported); got != want {
		t.Fatalf("clone tree:\n%s\nwant:\n%s", got, want)
	}
	if got, want := bareGit(t, clone, nil, "rev-list", "--all"), bareGit(t, repo, nil, "rev-list", exported); got != want {
		t.Fatalf("clone history:\n%s\nwant:\n%s", got, want)
	}
	if got := directoryNames(t, dir); len(got) != 1 || got[0] != "source.bundle" {
		t.Fatalf("staging directory holds %v", got)
	}
	if bareGit(t, repo, nil, "for-each-ref") != refs || bareGit(t, repo, nil, "count-objects", "-v") != objects {
		t.Fatal("export wrote P's repository")
	}

	written, err := os.ReadFile(bundle.Path)
	if err != nil {
		t.Fatal(err)
	}
	bareGit(t, repo, nil, "update-ref", "refs/heads/main", root)
	if after, err := os.ReadFile(bundle.Path); err != nil || !bytes.Equal(after, written) {
		t.Fatalf("bundle changed after the branch moved: %v", err)
	}
}

func TestExportBundleRefusesWhatIsNotAnOrdinaryCommitAndLeavesNothing(t *testing.T) {
	b, repo := bareFixture(t)
	blob := bareBlob(t, repo, "data\n")
	tree := bareTree(t, repo, "100644 blob "+blob+"\tfile")
	reachable := bareCommit(t, repo, tree, "")
	bareGit(t, repo, nil, "update-ref", "refs/heads/main", reachable)
	hidden := bareCommit(t, repo, bareTree(t, repo, "100644 blob "+bareBlob(t, repo, "hidden\n")+"\tfile"), "")
	bareGit(t, repo, nil, "update-ref", "refs/hidden/only", hidden)
	refs, objects := bareGit(t, repo, nil, "for-each-ref"), bareGit(t, repo, nil, "count-objects", "-v")
	for _, tc := range []struct {
		name, project, commit string
		invalid               bool
	}{
		{"invalid project", "../app", reachable, true},
		{"abbreviated commit", "app", reachable[:12], true},
		{"upper-case commit", "app", strings.ToUpper(reachable), true},
		{"zero commit", "app", strings.Repeat("0", 40), true},
		{"missing commit", "app", strings.Repeat("f", 40), false},
		{"tree", "app", tree, false},
		{"blob", "app", blob, false},
		{"hidden-only commit", "app", hidden, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := exportDirectory(t)
			if err := os.WriteFile(filepath.Join(dir, "existing"), []byte("kept"), 0600); err != nil {
				t.Fatal(err)
			}
			bundle, err := b.ExportBundle(context.Background(), tc.project, tc.commit, dir)
			if err == nil || bundle != (Bundle{}) || tc.invalid && !errors.Is(err, control.ErrInvalid) {
				t.Fatalf("export = %+v, %v", bundle, err)
			}
			if got := directoryNames(t, dir); !reflect.DeepEqual(got, []string{"existing"}) {
				t.Fatalf("staging directory holds %v", got)
			}
			if bareGit(t, repo, nil, "for-each-ref") != refs || bareGit(t, repo, nil, "count-objects", "-v") != objects {
				t.Fatal("refused export wrote P's repository")
			}
		})
	}
}

func TestExportBundleRequiresAPrivateStagingDirectory(t *testing.T) {
	b, repo := bareFixture(t)
	commit := bareCommit(t, repo, bareTree(t, repo, "100644 blob "+bareBlob(t, repo, "data\n")+"\tfile"), "")
	bareGit(t, repo, nil, "update-ref", "refs/heads/main", commit)
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T) (dir, kept string)
	}{
		{"relative", func(t *testing.T) (string, string) { return "staging", "" }},
		{"not clean", func(t *testing.T) (string, string) {
			dir := exportDirectory(t)
			return dir + "/../staging", dir
		}},
		{"missing", func(t *testing.T) (string, string) { return filepath.Join(t.TempDir(), "missing"), "" }},
		{"group readable", func(t *testing.T) (string, string) {
			dir := exportDirectory(t)
			if err := os.Chmod(dir, 0750); err != nil {
				t.Fatal(err)
			}
			return dir, dir
		}},
		{"symlink", func(t *testing.T) (string, string) {
			target := exportDirectory(t)
			link := filepath.Join(t.TempDir(), "link")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			return link, target
		}},
		{"file", func(t *testing.T) (string, string) {
			file := filepath.Join(t.TempDir(), "file")
			if err := os.WriteFile(file, nil, 0600); err != nil {
				t.Fatal(err)
			}
			return file, ""
		}},
		{"bundle already present", func(t *testing.T) (string, string) {
			dir := exportDirectory(t)
			if err := os.WriteFile(filepath.Join(dir, "source.bundle"), []byte("kept"), 0600); err != nil {
				t.Fatal(err)
			}
			return dir, dir
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, kept := tc.prepare(t)
			var before []string
			if kept != "" {
				before = directoryNames(t, kept)
			}
			if bundle, err := b.ExportBundle(context.Background(), "app", commit, dir); err == nil || bundle != (Bundle{}) {
				t.Fatalf("export = %+v, %v", bundle, err)
			}
			if kept == "" {
				return
			}
			if got := directoryNames(t, kept); !reflect.DeepEqual(got, before) {
				t.Fatalf("staging directory holds %v, had %v", got, before)
			}
			if data, err := os.ReadFile(filepath.Join(kept, "source.bundle")); err == nil && string(data) != "kept" {
				t.Fatal("existing bundle replaced")
			}
		})
	}
}

func TestExportBundleFailureAfterStagingLeavesNothing(t *testing.T) {
	b, repo := bareFixture(t)
	// A commit whose tree P does not hold passes every check before Git packs
	// the bundle, so the export fails with its temporary repository in place.
	broken := bareGit(t, repo, []byte("tree "+strings.Repeat("1", 40)+"\nauthor P <p@example.test> 0 +0000\ncommitter P <p@example.test> 0 +0000\n\nbroken\n"), "hash-object", "-t", "commit", "-w", "--stdin")
	bareGit(t, repo, nil, "update-ref", "refs/heads/broken", broken)
	refs := bareGit(t, repo, nil, "for-each-ref")
	dir := exportDirectory(t)
	if bundle, err := b.ExportBundle(context.Background(), "app", broken, dir); err == nil || bundle != (Bundle{}) {
		t.Fatalf("export = %+v, %v", bundle, err)
	}
	if got := directoryNames(t, dir); len(got) != 0 {
		t.Fatalf("staging directory holds %v", got)
	}
	if bareGit(t, repo, nil, "for-each-ref") != refs {
		t.Fatal("failed export wrote P's refs")
	}
}
