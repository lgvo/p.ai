package gitservice

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
)

func TestSelectedSourceLocalCapturePreservesRefsAndVerifiesChoice(t *testing.T) {
	goPath, e := exec.LookPath("go")
	if e != nil {
		t.Fatal(e)
	}
	gitPath, e := exec.LookPath("git")
	if e != nil {
		t.Fatal(e)
	}
	state := t.TempDir()
	packageDir := filepath.Join(state, "source-package")
	if e = os.Mkdir(packageDir, 0700); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"plugin.json", "p-git-ssh"} {
		data, e := os.ReadFile(filepath.Join("../../plugins/bundled/source-git", name))
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(packageDir, name), data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	build := exec.Command(goPath, "build", "-o", filepath.Join(packageDir, "git.wasm"), "../../plugins/bundled/source-git")
	build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0", "GOTOOLCHAIN=local")
	if out, e := build.CombinedOutput(); e != nil {
		t.Fatalf("source package: %v %s", e, out)
	}
	pkg, e := plugin.Conformance(packageDir)
	if e != nil {
		t.Fatal(e)
	}
	b := &Backend{stateDir: state, gitPath: gitPath, selection: plugin.Active{Package: pkg, Grants: []string{"git.project"}}}
	if e = os.Mkdir(filepath.Join(state, "repositories"), 0700); e != nil {
		t.Fatal(e)
	}
	repo, e := b.repository("app")
	if e != nil {
		t.Fatal(e)
	}
	gitTest(t, nil, "init", "--bare", "--initial-branch=main", repo)
	author := []string{"GIT_AUTHOR_NAME=P", "GIT_AUTHOR_EMAIL=p@example.invalid", "GIT_COMMITTER_NAME=P", "GIT_COMMITTER_EMAIL=p@example.invalid"}
	tree := gitTest(t, nil, "-C", repo, "mktree")
	first := gitTest(t, author, "-C", repo, "commit-tree", tree, "-m", "first")
	second := gitTest(t, author, "-C", repo, "commit-tree", tree, "-p", first, "-m", "second")
	hidden := gitTest(t, author, "-C", repo, "commit-tree", tree, "-m", "hidden")
	gitTest(t, nil, "-C", repo, "update-ref", "refs/heads/main", first)
	gitTest(t, nil, "-C", repo, "update-ref", "refs/tags/hidden", hidden)
	req := control.ReserveSessionRequest{Key: "new", Project: "app", Branch: "work", Choice: "new", Source: "refs/heads/main"}
	ctx := context.Background()
	capture, e := b.CaptureLocalSource(ctx, req)
	if e != nil || capture.OID != first || capture.Existed {
		t.Fatalf("initial capture: %+v %v", capture, e)
	}
	gitTest(t, nil, "-C", repo, "update-ref", "refs/heads/main", second, first)
	capture, e = b.CaptureLocalSource(ctx, req)
	if e != nil || capture.OID != second || capture.Existed {
		t.Fatalf("fresh replacement capture: %+v %v", capture, e)
	}
	before := gitTest(t, nil, "-C", repo, "for-each-ref", "--format=%(refname) %(objectname)")
	for _, tc := range []struct {
		name, source, choice string
		want                 string
	}{
		{name: "reachable-commit", source: first, choice: "new", want: first},
		{name: "hidden-only", source: hidden, choice: "new"},
		{name: "non-source-tag", source: "refs/tags/hidden", choice: "new"},
		{name: "existing-main", choice: "existing", want: second},
		{name: "occupied-main", source: second, choice: "new"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := req
			r.Source = tc.source
			r.Choice = tc.choice
			if tc.name == "existing-main" || tc.name == "occupied-main" {
				r.Branch = "main"
			}
			got, e := b.CaptureLocalSource(ctx, r)
			if tc.want == "" {
				if e == nil {
					t.Fatal("unsafe source admitted")
				}
			} else if e != nil || got.OID != tc.want || got.Existed != (tc.choice == "existing") {
				t.Fatalf("capture: %+v %v", got, e)
			}
		})
	}
	if after := gitTest(t, nil, "-C", repo, "for-each-ref", "--format=%(refname) %(objectname)"); after != before {
		t.Fatalf("capture changed refs: %s", after)
	}

	// Real selected-module CreateBranch faults before and after its native CAS.
	// The broker sees an error after a successful CAS, but never resets that ref.
	bash, e := exec.LookPath("bash")
	if e != nil {
		t.Fatal(e)
	}
	shim := filepath.Join(state, "git-fault")
	script := fmt.Sprintf("#!%s\nif [ \"$#\" -eq 7 ] && [ \"$3\" = update-ref ]; then\n if [ \"$5\" = refs/heads/fault-before ]; then exit 128; fi\n if [ \"$5\" = refs/heads/fault-after ]; then '%s' \"$@\"; exit 128; fi\nfi\nexec '%s' \"$@\"\n", bash, gitPath, gitPath)
	if e = os.WriteFile(shim, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	b.gitPath = shim
	if e = b.CreateBranch(ctx, "app", "fault-before", second); e == nil {
		t.Fatal("before-CAS fault succeeded")
	}
	if _, exists, e := b.InspectBranchRef(ctx, "app", "fault-before"); e != nil || exists {
		t.Fatalf("before-CAS fault produced ref: %v %v", exists, e)
	}
	if e = b.CreateBranch(ctx, "app", "fault-after", second); !errors.Is(e, control.ErrConflict) {
		t.Fatalf("successful CAS with failed reply: %v", e)
	}
	if got, exists, e := b.InspectBranchRef(ctx, "app", "fault-after"); e != nil || !exists || got != second {
		t.Fatalf("after-CAS ref lost: %s %v %v", got, exists, e)
	}
	existing := control.ReserveSessionRequest{Key: "replacement", Project: "app", Branch: "fault-after", Choice: "existing"}
	if got, e := b.CaptureLocalSource(ctx, existing); e != nil || got.OID != second || !got.Existed {
		t.Fatalf("preserved failed ref existing capture: %+v %v", got, e)
	}
}
