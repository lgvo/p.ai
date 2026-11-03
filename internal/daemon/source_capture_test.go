package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/sourcedriver"
	sourcememory "github.com/lgvo/p.ai/internal/sourcedriver/memory"
)

// failingReads is an in-memory source driver whose reads of commits and of
// one head fail, as a corrupt or unreadable repository's would.
type failingReads struct {
	*sourcememory.Driver
	commits bool   // InspectCommit fails
	head    string // InspectBranch of this head fails
}

var errUnreadable = fmt.Errorf("the repository cannot be read: %w", sourcedriver.ErrUnavailable)

func (d *failingReads) InspectCommit(ctx context.Context, project string, id sourcedriver.OID) (sourcedriver.Commit, error) {
	if d.commits {
		return sourcedriver.Commit{}, errUnreadable
	}
	return d.Driver.InspectCommit(ctx, project, id)
}

func (d *failingReads) InspectBranch(ctx context.Context, project, name string) (sourcedriver.OID, bool, error) {
	if name == d.head {
		return "", false, errUnreadable
	}
	return d.Driver.InspectBranch(ctx, project, name)
}

// captureFixture is project app's repository in an in-memory source driver:
// main at tip, taken at tip, linked a symbolic ref to main, orphan a commit no
// head reaches, and tree an object that is not a commit.
type captureFixture struct {
	world  *sourcememory.World
	driver *sourcememory.Driver
	tip    sourcedriver.OID
	orphan sourcedriver.OID
	tree   sourcedriver.OID
}

func newCaptureFixture(t *testing.T) captureFixture {
	t.Helper()
	world, driver := memorySource(t)
	if _, err := driver.CreateRepository(context.Background(), "app"); err != nil {
		t.Fatal(err)
	}
	f := captureFixture{world: world, driver: driver, tip: world.Commit("app"), orphan: world.Commit("app"), tree: world.Tree("app")}
	world.SetHeads("app", []string{"main", "taken"}, f.tip)
	world.SetSymbolic("app", "linked", "main")
	return f
}

func branchSource(name string) sourcedriver.Name {
	return sourcedriver.Name{Kind: sourcedriver.NameBranch, Value: name}
}

func commitSource(id sourcedriver.OID) sourcedriver.Name {
	return sourcedriver.Name{Kind: sourcedriver.NameCommit, Value: string(id)}
}

// A local creation's capture keeps today's answers: the commit of the
// classified source, or the refusal that names what is wrong.
func TestLocalCaptureAnswers(t *testing.T) {
	f := newCaptureFixture(t)
	l := &lifecycle{git: &gitCapability{driver: f.driver}}
	newBranch := control.ReserveSessionRequest{Project: "app", Branch: "work", Choice: "new"}
	existing := control.ReserveSessionRequest{Project: "app", Branch: "taken", Choice: "existing"}
	for _, test := range []struct {
		name   string
		req    control.ReserveSessionRequest
		source sourcedriver.Name
		want   control.CapturedSource
	}{
		{"a branch source", newBranch, branchSource("main"), control.CapturedSource{OID: string(f.tip)}},
		{"a commit a head reaches", newBranch, commitSource(f.tip), control.CapturedSource{OID: string(f.tip)}},
		{"an existing branch", existing, sourcedriver.Name{}, control.CapturedSource{OID: string(f.tip), Existed: true}},
	} {
		got, err := l.captureLocalSource(context.Background(), test.req, test.source)
		if err != nil || got != test.want {
			t.Errorf("%s captured %+v %v, want %+v", test.name, got, err, test.want)
		}
	}
	for _, test := range []struct {
		name           string
		req            control.ReserveSessionRequest
		source         sourcedriver.Name
		kind           api.ErrorKind
		reason, target string
	}{
		{"a new branch a head holds", control.ReserveSessionRequest{Project: "app", Branch: "taken", Choice: "new"}, branchSource("main"), api.KindConflict, api.ReasonBranchExists, "project/app:branch/taken"},
		{"a new branch a symbolic ref holds", control.ReserveSessionRequest{Project: "app", Branch: "linked", Choice: "new"}, branchSource("main"), api.KindConflict, api.ReasonBranchExists, "project/app:branch/linked"},
		{"an existing branch no head holds", control.ReserveSessionRequest{Project: "app", Branch: "work", Choice: "existing"}, sourcedriver.Name{}, api.KindNotFound, api.ReasonBranch, "project/app:branch/work"},
		{"a missing source branch", newBranch, branchSource("absent"), api.KindNotFound, api.ReasonSource, ""},
		{"a symbolic source branch", newBranch, branchSource("linked"), api.KindNotFound, api.ReasonSource, ""},
		{"a commit no head reaches", newBranch, commitSource(f.orphan), api.KindNotFound, api.ReasonSource, ""},
		{"an object that is not a commit", newBranch, commitSource(f.tree), api.KindNotFound, api.ReasonSource, ""},
		{"a missing commit", newBranch, commitSource("cccccccccccccccccccccccccccccccccccccccc"), api.KindNotFound, api.ReasonSource, ""},
	} {
		_, err := l.captureLocalSource(context.Background(), test.req, test.source)
		wantRefusal(t, test.name, err, test.kind, test.reason, test.target)
	}
}

// An existing branch that moves between the capture's two reads answers
// retry, refs_moved: a new request reads it again.
func TestLocalCaptureOfAMovingBranchIsRetried(t *testing.T) {
	f := newCaptureFixture(t)
	moving := &movingHead{Driver: f.driver, world: f.world, branch: "taken", tip: f.world.Commit("app", f.tip), moveAt: 2}
	l := &lifecycle{git: &gitCapability{driver: moving}}
	_, err := l.captureLocalSource(context.Background(), control.ReserveSessionRequest{Project: "app", Branch: "taken", Choice: "existing"}, sourcedriver.Name{})
	wantRefusal(t, "a branch moving during the capture", err, api.KindRetry, api.ReasonRefsMoved, "project/app:branch/taken")
}

// A read of the source that fails answers failed, reason git: a repository
// that cannot be read is not a missing source.
func TestLocalCaptureOfAnUnreadableSourceFailsWithGit(t *testing.T) {
	f := newCaptureFixture(t)
	newBranch := control.ReserveSessionRequest{Project: "app", Branch: "work", Choice: "new"}
	for _, test := range []struct {
		name   string
		driver *failingReads
		source sourcedriver.Name
	}{
		{"an unreadable source branch", &failingReads{Driver: f.driver, head: "main"}, branchSource("main")},
		{"an unreadable source commit", &failingReads{Driver: f.driver, commits: true}, commitSource(f.tip)},
		{"an unreadable branch tip", &failingReads{Driver: f.driver, commits: true}, branchSource("main")},
	} {
		l := &lifecycle{git: &gitCapability{driver: test.driver}}
		_, err := l.captureLocalSource(context.Background(), newBranch, test.source)
		wantRefusal(t, test.name, err, api.KindFailed, api.ReasonGit, "")
		if !errors.Is(err, errUnreadable) {
			t.Errorf("%s lost its cause: %v", test.name, err)
		}
	}
}

// A new creation has the source driver check its Git names before anything
// is recorded: a value it refuses answers invalid naming its field.
func TestSessionCreationRefusesNamesTheSourceDriverRefusesBeforeRecording(t *testing.T) {
	ctx := context.Background()
	store, err := control.OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	policy := control.ProjectPolicy{Network: "none", FilesystemMounts: []control.FilesystemGrant{}, Command: []string{"/bin/sh"}}
	snapshot, _, err := control.ProjectPolicySnapshot(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CreateProject(ctx, "app", snapshot); err != nil {
		t.Fatal(err)
	}
	f := newCaptureFixture(t)
	l := &lifecycle{ctx: ctx, store: store, git: &gitCapability{driver: f.driver}, cfg: control.RuntimeConfig{ProjectPolicies: map[string]control.ProjectPolicy{"app": policy}}}
	oid := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, test := range []struct {
		field string
		req   control.ReserveSessionRequest
	}{
		{"branch", control.ReserveSessionRequest{Branch: "work.lock", Choice: "existing"}},
		{"source", control.ReserveSessionRequest{Branch: "work", Choice: "new", Source: "main"}},
		{"source", control.ReserveSessionRequest{Branch: "work", Choice: "new", Source: "refs/heads/a..b"}},
		{"origin_ref", control.ReserveSessionRequest{Branch: "work", Choice: "new", OriginRef: "refs/heads/a..b", ExpectedCommitOID: oid}},
		{"expected_commit_oid", control.ReserveSessionRequest{Branch: "work", Choice: "new", OriginRef: "refs/heads/main", ExpectedCommitOID: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}},
	} {
		req := test.req
		req.Key, req.Project = "create-"+test.field, "app"
		_, err := l.CreateSession(ctx, req)
		var refusal *control.Refusal
		if !errors.As(err, &refusal) || refusal.Kind != api.KindInvalid || refusal.Field != test.field {
			t.Errorf("an invalid %s answered %v, want invalid %s", test.field, err, test.field)
		}
		if _, err = store.GetOperationByKey(ctx, req.Key); !errors.Is(err, control.ErrNotFound) {
			t.Errorf("an invalid %s recorded its key: %v", test.field, err)
		}
	}
}
