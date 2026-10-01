package gitservice

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/plugin"
)

func TestOriginPublicationRealGit(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git unavailable")
	}
	goPath := "/nix/store/4l04h8656as4i291mpcg38wz9m4k8acp-go-1.26.7/bin/go"
	if _, err := os.Stat(goPath); err != nil {
		t.Skip("pinned Go unavailable")
	}
	root := t.TempDir()
	env := []string{"PATH=" + filepath.Dir(git), "HOME=" + root, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	run := func(args ...string) string {
		t.Helper()
		c := exec.Command(git, args...)
		c.Env = env
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	work, remote := filepath.Join(root, "work"), filepath.Join(root, "origin.git")
	run("-c", "init.defaultBranch=main", "init", "-q", work)
	run("init", "--bare", "-q", remote)
	commit := func(name string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(work, "file"), []byte(name+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		run("-C", work, "add", "file")
		run("-C", work, "-c", "user.name=P", "-c", "user.email=p@example.test", "commit", "-qm", name)
		return run("-C", work, "rev-parse", "HEAD")
	}
	base := commit("base")
	source := commit("source")
	child := commit("child")
	run("-C", work, "checkout", "-q", "-b", "diverge", base)
	divergent := commit("divergent")
	run("-C", work, "checkout", "-q", "main")
	run("-C", work, "reset", "--hard", "-q", source)
	run("-C", work, "push", "-q", remote, base+":refs/heads/base", source+":refs/heads/source", child+":refs/heads/child", divergent+":refs/heads/divergent")
	pkgDir := filepath.Join(root, "source-package")
	if err := os.Mkdir(pkgDir, 0700); err != nil {
		t.Fatal(err)
	}
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
		t.Fatalf("build WASI: %v %s", err, out)
	}
	pkg, err := plugin.Conformance(pkgDir)
	if err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(root, "shim")
	if err := os.Mkdir(shim, 0700); err != nil {
		t.Fatal(err)
	}
	uncertain := filepath.Join(root, "uncertain")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	script := "#!/bin/sh\ncase \"$*\" in\n *git-upload-pack*) exec " + quote(git) + " upload-pack " + quote(remote) + " ;;\n *git-receive-pack*) " + quote(git) + " receive-pack " + quote(remote) + "; code=$?; [ -e " + quote(uncertain) + " ] && exit 55; exit $code ;;\n *) exit 70 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(shim, "ssh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shim+":"+filepath.Dir(goPath)+":"+os.Getenv("PATH"))
	t.Setenv("HOME", root)
	b := &Backend{stateDir: root, gitPath: git, selection: plugin.Active{Package: pkg, Grants: []string{"git.project"}}}
	project := "team/app"
	pRepo, err := b.repository(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(pRepo), 0700); err != nil {
		t.Fatal(err)
	}
	run("init", "--bare", "-q", pRepo)
	run("-C", work, "push", "-q", pRepo, source+":refs/heads/publish")
	run("-C", pRepo, "config", "core.sshCommand", "false")
	run("-C", pRepo, "config", "url.file:///nonexistent/.insteadOf", "git@fixture:")
	run("-C", pRepo, "config", "push.followTags", "true")
	url := "git@fixture:repo.git"
	put := func(ref, oid string) { t.Helper(); run("--git-dir="+remote, "update-ref", ref, oid) }
	read := func(ref string) string { t.Helper(); return run("--git-dir="+remote, "rev-parse", ref) }
	if err := b.WithOrigin(context.Background(), project, func(scope *OriginScope) error {
		if _, e := scope.PreviewPublication(context.Background(), "refs/heads/publish", source, "refs/heads/no-observation"); e == nil {
			t.Fatal("preview accepted without same-scope observation")
		}
		if _, e := scope.Observe(context.Background(), url); e != nil {
			return e
		}
		for _, destination := range []string{"refs/tags/tag", "refs/heads/*", "refs/heads/a:refs/heads/b", "refs/heads/"} {
			if _, e := scope.PreviewPublication(context.Background(), "refs/heads/publish", source, destination); e == nil {
				t.Fatalf("accepted destination %q", destination)
			}
		}
		if _, e := scope.PreviewPublication(context.Background(), "refs/heads/publish", child, "refs/heads/not-tip"); e == nil {
			t.Fatal("preview accepted source different from P tip")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	action := func(destination, relation, status string, raced string) {
		t.Helper()
		err := b.WithOrigin(context.Background(), project, func(scope *OriginScope) error {
			if _, e := scope.Observe(context.Background(), url); e != nil {
				return e
			}
			p, e := scope.PreviewPublication(context.Background(), "refs/heads/publish", source, destination)
			if e != nil {
				return e
			}
			if p.Relation != relation {
				t.Fatalf("%s: relation %q, want %q", destination, p.Relation, relation)
			}
			if raced != "" {
				put(destination, raced)
			}
			result, e := scope.PublishPublication(context.Background(), p)
			if e != nil {
				return e
			}
			if result.Status != status {
				t.Fatalf("%s: status %q, want %q", destination, result.Status, status)
			}
			if _, e := scope.PublishPublication(context.Background(), p); e == nil {
				t.Fatal("preview reused")
			}
			return nil
		})
		if err != nil {
			t.Fatalf("%s: %v", destination, err)
		}
	}
	action("refs/heads/new", "absent", "created", "")
	if read("refs/heads/new") != source {
		t.Fatal("created wrong ref")
	}
	action("refs/heads/new", "equal", "satisfied", "")
	put("refs/heads/contains", child)
	action("refs/heads/contains", "destination_contains", "satisfied", "")
	if read("refs/heads/contains") != child {
		t.Fatal("contains ref moved")
	}
	put("refs/heads/behind", base)
	action("refs/heads/behind", "fast_forward", "advanced", "")
	if read("refs/heads/behind") != source {
		t.Fatal("fast-forward failed")
	}
	put("refs/heads/diverged", divergent)
	action("refs/heads/diverged", "divergent", "refused", "")
	if read("refs/heads/diverged") != divergent {
		t.Fatal("divergent ref moved")
	}
	put("refs/heads/raced", base)
	action("refs/heads/raced", "fast_forward", "refused", divergent)
	if read("refs/heads/raced") != divergent {
		t.Fatal("raced ref overwritten")
	}
	action("refs/heads/raced-same", "absent", "satisfied", source)
	if read("refs/heads/raced-same") != source {
		t.Fatal("raced same-tip ref changed")
	}
	if err := os.WriteFile(uncertain, []byte("yes"), 0600); err != nil {
		t.Fatal(err)
	}
	action("refs/heads/uncertain", "absent", "outcome_unknown", "")
	if read("refs/heads/uncertain") != source {
		t.Fatal("uncertain fixture did not accept before dropping response")
	}
	if got := run("--git-dir="+pRepo, "for-each-ref", "--format=%(refname)"); got != "refs/heads/publish" {
		t.Fatalf("publication changed P refs: %s", got)
	}
}

func TestOriginPushFailureBeforeProcessStartIsDefinite(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git unavailable")
	}
	remove, err := exec.LookPath("rm")
	if err != nil {
		t.Fatal("rm unavailable for process-start fixture")
	}
	root := t.TempDir()
	shim := filepath.Join(root, "git-once")
	quotedGit := "'" + strings.ReplaceAll(git, "'", "'\\''") + "'"
	quotedRemove := "'" + strings.ReplaceAll(remove, "'", "'\\''") + "'"
	// The scratch repository init succeeds, then the executable disappears
	// before the push starts. There can be no remote acceptance in this case.
	script := "#!/bin/sh\n" + quotedGit + " \"$@\" || exit $?\n" + quotedRemove + " -- \"$0\" || exit $?\n"
	if err := os.WriteFile(shim, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	b := &Backend{stateDir: root, gitPath: shim}
	attempted := false
	status, err := b.pushOrigin(context.Background(), root, "git@fixture:repo.git", OriginPublicationPreview{
		SourceOID:      strings.Repeat("a", 40),
		DestinationRef: "refs/heads/main",
		Relation:       "absent",
	}, &attempted)
	if err == nil || !strings.Contains(err.Error(), "could not start") || attempted || status != "" {
		t.Fatalf("process-start failure: status=%q attempted=%t err=%v", status, attempted, err)
	}
	if _, err := os.Stat(shim); !os.IsNotExist(err) {
		t.Fatalf("git wrapper was not removed before push: %v", err)
	}
}
