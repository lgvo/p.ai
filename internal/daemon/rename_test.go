package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

func TestRenameRequiresAStoppedSession(t *testing.T) {
	stopped := runtimeincus.Observation{Exists: true, Status: "Stopped", IncusUUID: "22222222-2222-4222-8222-222222222222", Generation: "33333333-3333-4333-8333-333333333333"}
	if !renameSourceStopped(stopped) {
		t.Fatal("a stopped session was refused")
	}
	for _, status := range []string{"Running", "Frozen", "Error"} {
		o := stopped
		o.Status = status
		if renameSourceStopped(o) {
			t.Fatalf("a %s session was accepted", status)
		}
	}
	ev := control.RenameEvidence{IncusUUID: stopped.IncusUUID, Generation: stopped.Generation}
	if !renameSourceExact(stopped, ev) {
		t.Fatal("the recorded stopped runtime was refused")
	}
	started := stopped
	started.Status = "Running"
	replaced := stopped
	replaced.IncusUUID = "44444444-4444-4444-8444-444444444444"
	for name, o := range map[string]runtimeincus.Observation{"started": started, "replaced": replaced} {
		if renameSourceExact(o, ev) {
			t.Fatalf("a %s runtime was taken for the recorded one", name)
		}
	}
}

// wantRefusal fails unless err is a refusal of kind with reason and target.
func TestRenameOfASessionThatIsNotStoppedIsAConflict(t *testing.T) {
	const session = "11111111-1111-4111-8111-111111111111"
	stopped := runtimeincus.Observation{Exists: true, Status: "Stopped", IncusUUID: "22222222-2222-4222-8222-222222222222", Generation: "33333333-3333-4333-8333-333333333333"}
	if err := renameSourceRefusal(stopped, session); err != nil {
		t.Fatalf("a stopped session was refused: %v", err)
	}
	for _, status := range []string{"Running", "Frozen"} {
		o := stopped
		o.Status = status
		wantRefusal(t, status, renameSourceRefusal(o, session), api.KindConflict, api.ReasonSessionNotStopped, "session/"+session)
	}
	wantRefusal(t, "missing", renameSourceRefusal(runtimeincus.Observation{}, session), api.KindConflict, api.ReasonRuntimeMissing, "session/"+session)
}

// retainedFixture is a lifecycle over a real store and Git backend with
// project app, whose branches saved and taken point at tip. Its context is
// done, so an accepted operation is recorded but never processed.
func retainedFixture(t *testing.T) (l *lifecycle, store *control.Store, repo, tip string) {
	t.Helper()
	store, err := control.OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err = store.CreateProject(context.Background(), "app", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	backend, tip := dispatchGitFixture(t, store)
	if repo, err = backend.RepositoryPath("app"); err != nil {
		t.Fatal(err)
	}
	for _, branch := range []string{"saved", "taken", "held"} {
		dispatchGit(t, repo, "update-ref", "refs/heads/"+branch, tip)
	}
	done, cancel := context.WithCancel(context.Background())
	cancel()
	return &lifecycle{ctx: done, store: store, git: &gitCapability{backend: backend}}, store, repo, tip
}

// holdBranch records a creation that holds branch of project app.
func holdBranch(t *testing.T, store *control.Store, branch string) {
	t.Helper()
	selection := control.CreationSelection{HostID: "fixture", HostSHA256: strings.Repeat("b", 64), SourceID: "fixture", SourceSHA256: strings.Repeat("c", 64)}
	_, _, err := store.BeginSessionCreate(context.Background(), control.ReserveSessionRequest{Key: "hold-" + branch, Project: "app", Branch: branch, Choice: "existing"}, strings.Repeat("d", 64), selection,
		func(context.Context, control.ReserveSessionRequest) (string, bool, error) {
			return strings.Repeat("e", 40), true, nil
		})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRetainedRenameRefusalsNameTheirKind(t *testing.T) {
	l, store, repo, tip := retainedFixture(t)
	ctx := context.Background()
	holdBranch(t, store, "held")
	dispatchGit(t, repo, "symbolic-ref", "refs/heads/linked", "refs/heads/saved")
	base := control.RetainedRenameRequest{Key: "rename", Project: "app", OldBranch: "saved", NewBranch: "archive", ExpectedOldTip: tip}
	for _, test := range []struct {
		name   string
		change func(*control.RetainedRenameRequest)
		kind   api.ErrorKind
		reason string
		target string
	}{
		{"missing old branch", func(r *control.RetainedRenameRequest) { r.OldBranch = "absent" }, api.KindNotFound, api.ReasonBranch, "project/app:branch/absent"},
		{"changed tip", func(r *control.RetainedRenameRequest) { r.ExpectedOldTip = strings.Repeat("b", 40) }, api.KindPreconditionFailed, api.ReasonChanged, ""},
		{"taken new name", func(r *control.RetainedRenameRequest) { r.NewBranch = "taken" }, api.KindConflict, api.ReasonBranchExists, "project/app:branch/taken"},
		{"new name a symbolic ref holds", func(r *control.RetainedRenameRequest) { r.NewBranch = "linked" }, api.KindConflict, api.ReasonBranchExists, "project/app:branch/linked"},
		{"branch a session holds", func(r *control.RetainedRenameRequest) { r.OldBranch = "held" }, api.KindConflict, api.ReasonBranchAssigned, "project/app:branch/held"},
	} {
		r := base
		r.Key = test.name
		test.change(&r)
		_, err := l.RenameRetainedBranch(ctx, r)
		wantRefusal(t, test.name, err, test.kind, test.reason, test.target)
	}
	op, err := l.RenameRetainedBranch(ctx, base)
	if err != nil || op.Kind != control.KindRetainedRename {
		t.Fatalf("rename: %+v %v", op, err)
	}
	reused := base
	reused.NewBranch = "elsewhere"
	_, err = l.RenameRetainedBranch(ctx, reused)
	wantRefusal(t, "reused key", err, api.KindInvalid, api.ReasonKeyReused, "")
	_, err = l.RenameRetainedBranch(ctx, control.RetainedRenameRequest{Key: "guarded", Project: "app", OldBranch: "taken", NewBranch: "archive", ExpectedOldTip: tip})
	wantRefusal(t, "reserved new name", err, api.KindConflict, api.ReasonBranchReserved, "project/app:branch/archive")
}

// TestRetainedDeleteDryRunAndRun checks a retained branch's deletion through
// its two calls on real Git: the dry run plans it, lists the commits that
// leave P as certain loss and changes nothing; the run with its tag records
// the deletion once; a changed branch, an expired tag or a missing tag
// refuse the run; refusals are in the plan, and unknown records are errors.
func TestRetainedDeleteDryRunAndRun(t *testing.T) {
	l, store, repo, tip := retainedFixture(t)
	l.instanceID = "11111111-1111-4111-8111-111111111111"
	ctx := context.Background()
	holdBranch(t, store, "held")
	tree := dispatchGit(t, repo, "mktree")
	own := dispatchGit(t, repo, "-c", "user.name=P fixture", "-c", "user.email=p@example.test", "commit-tree", tree, "-p", tip, "-m", "only on saved")
	dispatchGit(t, repo, "update-ref", "refs/heads/saved", own)
	dryRun := func(branch string) (api.ChangeResult[api.RetainedDeleteDetails], error) {
		return l.DeleteRetainedBranch(ctx, "app", branch, api.ChangeRequest{})
	}

	plan, err := dryRun("saved")
	p := plan.Plan
	if err != nil || p == nil || !p.Allowed || plan.ChangeTag == "" || p.Action != "project.retained.delete" || p.Target != "project/app:branch/saved" ||
		p.Details.Tip != own || p.Details.Ref != "refs/heads/saved" || p.Details.BranchLoss == nil ||
		!reflect.DeepEqual(p.Loss, []api.Loss{{Kind: api.LossPCommits, Target: "project/app:branch/saved", Count: 1, Sample: []string{own}, Certain: true}}) || len(p.Gone) != 0 {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	if raw, _ := json.Marshal(p.Details); strings.Contains(string(raw), "origin") {
		t.Fatalf("the plan reads the origin: %s", raw)
	}
	if again, err := dryRun("saved"); err != nil || !reflect.DeepEqual(again.Plan, p) {
		t.Fatalf("a second dry run of the same state: %+v %v", again, err)
	}
	if got := dispatchGit(t, repo, "rev-parse", "refs/heads/saved"); got != own {
		t.Fatalf("the dry run moved the branch to %s", got)
	}
	if _, err = store.GetOperationByKey(ctx, "delete"); !errors.Is(err, control.ErrNotFound) {
		t.Fatalf("the dry run recorded an operation: %v", err)
	}

	held, err := dryRun("held")
	if err != nil || held.Plan.Allowed || held.ChangeTag != "" || !reflect.DeepEqual(held.Plan.Refusals, []api.Refusal{{Code: api.ReasonBranchAssigned}}) {
		t.Fatalf("an assigned branch's plan: %+v %v", held, err)
	}
	_, err = dryRun("absent")
	wantRefusal(t, "missing branch", err, api.KindNotFound, api.ReasonBranch, "project/app:branch/absent")
	_, err = l.DeleteRetainedBranch(ctx, "gone", "saved", api.ChangeRequest{})
	wantRefusal(t, "missing project", err, api.KindNotFound, api.ReasonProject, "project/gone")

	_, err = l.DeleteRetainedBranch(ctx, "app", "saved", api.ChangeRequest{Key: "delete"})
	if !reflect.DeepEqual(err, api.PreconditionRequired()) {
		t.Fatalf("a run without its tag answered %v", err)
	}
	_, err = l.DeleteRetainedBranch(ctx, "app", "saved", api.ChangeRequest{Key: "delete", IfMatch: futureTag(-time.Second)})
	wantRefusal(t, "an expired tag", err, api.KindPreconditionFailed, api.ReasonExpired, "")
	_, err = l.DeleteRetainedBranch(ctx, "app", "taken", api.ChangeRequest{Key: "delete", IfMatch: plan.ChangeTag})
	wantRefusal(t, "the tag of another branch", err, api.KindPreconditionFailed, api.ReasonChanged, "")
	dispatchGit(t, repo, "update-ref", "refs/heads/saved", tip)
	_, err = l.DeleteRetainedBranch(ctx, "app", "saved", api.ChangeRequest{Key: "delete", IfMatch: plan.ChangeTag})
	wantRefusal(t, "a moved branch", err, api.KindPreconditionFailed, api.ReasonChanged, "")
	if _, err = store.GetOperationByKey(ctx, "delete"); !errors.Is(err, control.ErrNotFound) {
		t.Fatalf("a refused run recorded an operation: %v", err)
	}

	dispatchGit(t, repo, "update-ref", "refs/heads/saved", own)
	run := api.ChangeRequest{Key: "delete", IfMatch: plan.ChangeTag}
	result, err := l.DeleteRetainedBranch(ctx, "app", "saved", run)
	if err != nil || result.Operation == nil || result.Plan != nil || result.Operation.Kind != control.KindRetainedDelete || result.Operation.Phase != "guarded" {
		t.Fatalf("run: %+v %v", result, err)
	}
	var recorded control.RetainedDeleteRequest
	if err = json.Unmarshal(result.Operation.Request, &recorded); err != nil || recorded != (control.RetainedDeleteRequest{Key: "delete", Project: "app", Branch: "saved", IfMatch: plan.ChangeTag}) {
		t.Fatalf("recorded request %s: %v", result.Operation.Request, err)
	}
	if replay, err := l.DeleteRetainedBranch(ctx, "app", "saved", run); err != nil || replay.Operation.ID != result.Operation.ID {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	other, _ := dryRun("taken")
	_, err = l.DeleteRetainedBranch(ctx, "app", "taken", api.ChangeRequest{Key: "delete", IfMatch: other.ChangeTag})
	wantRefusal(t, "the key of another run", err, api.KindInvalid, api.ReasonKeyReused, "")
	// The recorded deletion guards its branch until it ends: another plan of
	// it is refused.
	guarded, err := dryRun("saved")
	if err != nil || guarded.Plan.Allowed || guarded.ChangeTag != "" || !reflect.DeepEqual(guarded.Plan.Refusals, []api.Refusal{{Code: api.ReasonBranchReserved}}) {
		t.Fatalf("a guarded branch's plan: %+v %v", guarded, err)
	}
}
