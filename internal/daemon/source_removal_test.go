package daemon

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// losingHeads puts the fixture's source driver behind lostHeads, whose head
// effects can lose their answers.
func (f *sessionFixture) losingHeads() *lostHeads {
	source := &lostHeads{Driver: f.l.git.driver}
	f.l.git.driver = source
	return source
}

// A session delete whose plan found its branch absent proves it still
// absent and deletes no head.
func TestSessionDeleteOfAnAbsentBranchDeletesNoHead(t *testing.T) {
	f := newSessionFixture(t)
	source := f.losingHeads()
	dispatchGit(t, f.repo, "update-ref", "-d", "refs/heads/work")
	dry, err := f.l.DeleteSession(f.ctx, f.session.UUID, api.ChangeRequest{})
	if err != nil || dry.Plan == nil || !dry.Plan.Details.AssignedRefMissing || dry.Plan.Details.AssignedTip != "" {
		t.Fatalf("plan of a delete whose branch is absent: %+v %v", dry.Plan, err)
	}
	op := f.confirmRemoval("delete")
	f.l.processDiscard(op)
	if done := f.operation(op.ID); done.Status != "completed" || done.Phase != "deleted" || source.effects != 0 || f.branchExists() {
		t.Fatalf("delete of an absent branch ended %s at %s (%s) after %d head effects", done.Status, done.Phase, done.Diagnostic, source.effects)
	}
}

// A session delete's branch deletion whose answer was lost is settled, never
// sent again: applied completes the delete, even once a retry finds the
// driver back; not applied keeps it blocked with the branch.
func TestSessionDeleteSettlesALostBranchDeletion(t *testing.T) {
	t.Run("applied", func(t *testing.T) {
		f := newSessionFixture(t)
		source := f.losingHeads()
		source.lose = 1
		op := f.confirmRemoval("delete")
		f.l.processDiscard(op)
		if done := f.operation(op.ID); done.Status != "completed" || done.Phase != "deleted" || source.effects != 1 || f.branchExists() {
			t.Fatalf("a lost answer of a branch deletion: %s at %s (%s) after %d head effects", done.Status, done.Phase, done.Diagnostic, source.effects)
		}
	})
	t.Run("applied once the driver is back", func(t *testing.T) {
		f := newSessionFixture(t)
		source := f.losingHeads()
		source.lose, source.settleDown = 1, true
		op := f.confirmRemoval("delete")
		f.l.processDiscard(op)
		blocked := f.operation(op.ID)
		if blocked.Status != "blocked" || blocked.Phase != "branch-delete-issued" || blocked.Reason != api.ReasonOutcomeUnknown {
			t.Fatalf("a branch deletion the driver could not settle: %s at %s (%s)", blocked.Status, blocked.Phase, blocked.Reason)
		}
		f.l.processDiscard(f.retry(op.ID))
		if done := f.operation(op.ID); done.Status != "completed" || done.Phase != "deleted" || source.effects != 1 || f.branchExists() {
			t.Fatalf("a retried branch deletion: %s at %s (%s) after %d head effects", done.Status, done.Phase, done.Diagnostic, source.effects)
		}
	})
	t.Run("not applied", func(t *testing.T) {
		f := newSessionFixture(t)
		source := f.losingHeads()
		source.drop = true
		op := f.confirmRemoval("delete")
		f.l.processDiscard(op)
		f.l.processDiscard(f.retry(op.ID))
		if blocked := f.operation(op.ID); blocked.Status != "blocked" || blocked.Phase != "branch-delete-issued" || blocked.Reason != api.ReasonGit || source.effects != 1 || !f.branchExists() {
			t.Fatalf("a branch deletion that did not happen: %s at %s (%s) after %d head effects", blocked.Status, blocked.Phase, blocked.Reason, source.effects)
		}
	})
}

// watchedRepositories is a source driver that runs beforeDelete once a
// repository deletion's gate passed, right before the deletion.
type watchedRepositories struct {
	sourcedriver.Driver
	beforeDelete func()
}

func (d *watchedRepositories) DeleteRepository(ctx context.Context, r sourcedriver.Repository, g sourcedriver.Gate) error {
	return d.Driver.DeleteRepository(ctx, r, func() error {
		if err := g(); err != nil {
			return err
		}
		d.beforeDelete()
		return nil
	})
}

// A session delete whose branch deletion cannot reach the source driver
// blocks with reason git.
func TestSessionDeleteWithTheSourceDriverUnreachableBlocksWithGit(t *testing.T) {
	f := newSessionFixture(t)
	source := f.losingHeads()
	source.unavailable = true
	op := f.confirmRemoval("delete")
	f.l.processDiscard(op)
	if blocked := f.operation(op.ID); blocked.Status != "blocked" || blocked.Reason != api.ReasonGit || !f.branchExists() {
		t.Fatalf("a branch deletion with the driver unreachable: %s at %s (%s)", blocked.Status, blocked.Phase, blocked.Reason)
	}
}

// sentCreation records a creation of a new branch fresh from work, blocked at
// source-ready after it sent the branch's creation, which never applied.
func (f *sessionFixture) sentCreation() api.Operation {
	f.t.Helper()
	req := control.ReserveSessionRequest{Key: "fresh", Project: "app", Branch: "fresh", Choice: "new", Source: "refs/heads/work"}
	op, _, err := f.l.store.BeginSessionCreateCaptured(f.ctx, req, f.l.cfg.BaseImageFingerprint, f.l.selection(), func(context.Context, control.ReserveSessionRequest) (control.CapturedSource, error) {
		return control.CapturedSource{OID: f.tip}, nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
	ev, err := control.Evidence(op)
	if err != nil {
		f.t.Fatal(err)
	}
	ev.BranchCreateIssued = true
	raw, err := json.Marshal(ev)
	if err != nil {
		f.t.Fatal(err)
	}
	if err = f.l.store.AdvanceOperation(f.ctx, op.ID, "blocked", "source-ready", false, raw, api.ReasonOutcomeUnknown, "the branch's creation is unresolved"); err != nil {
		f.t.Fatal(err)
	}
	return op
}

// refusedUnsettled reports whether refusals hold outcome_unknown.
func refusedUnsettled(refusals []api.Refusal) bool {
	for _, r := range refusals {
		if r.Code == api.ReasonOutcomeUnknown {
			return true
		}
	}
	return false
}

// A creation at source-ready that sent its branch's creation keeps its
// guard until the source driver settles that creation: while the driver
// cannot tell, or cannot be reached, a replacement or a cleanup of it is
// refused with outcome_unknown; once it settles, applied or not, they plan
// as before.
func TestSentCreationIsSettledBeforeItsReplacementOrCleanup(t *testing.T) {
	f := newSessionFixture(t)
	source := f.losingHeads()
	op := f.sentCreation()
	choices := api.CreationChoices{Project: "app", Branch: "fresh", Choice: "new", Source: "refs/heads/work"}
	plans := func() (replace, cleanup []api.Refusal) {
		t.Helper()
		r, err := f.l.ReplaceCreation(f.ctx, op.SessionUUID, choices, api.ChangeRequest{})
		if err != nil || r.Plan == nil {
			t.Fatalf("replacement plan: %+v %v", r, err)
		}
		c, err := f.l.CleanupCreation(f.ctx, op.SessionUUID, api.ChangeRequest{})
		if err != nil || c.Plan == nil {
			t.Fatalf("cleanup plan: %+v %v", c, err)
		}
		return r.Plan.Refusals, c.Plan.Refusals
	}
	for _, test := range []struct {
		name      string
		arrange   func()
		unsettled bool
	}{
		{"unknown", func() { source.unsettled = 2 }, true},
		{"unreachable", func() { source.settleDown = true; source.unsettled = 1 }, true},
		{"not applied", func() {}, false},
		{"applied", func() { dispatchGit(t, f.repo, "update-ref", "refs/heads/fresh", f.tip) }, false},
	} {
		test.arrange()
		replace, cleanup := plans()
		if refusedUnsettled(replace) != test.unsettled || refusedUnsettled(cleanup) != test.unsettled {
			t.Errorf("%s: replacement refused %v, cleanup refused %v; want outcome_unknown %t", test.name, replace, cleanup, test.unsettled)
		}
	}
	if source.effects != 0 {
		t.Fatalf("planning sent %d head effects", source.effects)
	}
}

// projectDeleteEvidence is the recorded evidence of project deletion op.
func (f *sessionFixture) projectDeleteEvidence(id string) control.ProjectDeleteEvidence {
	f.t.Helper()
	var ev control.ProjectDeleteEvidence
	if err := json.Unmarshal(f.operation(id).Evidence, &ev); err != nil {
		f.t.Fatal(err)
	}
	return ev
}

// repository is project app's repository as the source driver observes it.
func (f *sessionFixture) repository() sourcedriver.Repository {
	f.t.Helper()
	repo, err := f.l.git.driver.InspectRepository(f.ctx, "app")
	if err != nil {
		f.t.Fatal(err)
	}
	return repo
}

// Project deletion records the repository's incarnation in its plan and
// deletes the repository at that incarnation only, recording the delete
// before it is sent: a repository made again since the plan is not the
// reviewed one and stays.
func TestProjectDeletionDeletesTheRepositoryAtItsReviewedIncarnation(t *testing.T) {
	t.Run("reviewed", func(t *testing.T) {
		f := newSessionFixture(t)
		if _, err := f.driver.Stop(f.ctx, f.spec()); err != nil {
			t.Fatal(err)
		}
		reviewed := f.repository()
		dry, err := f.l.DeleteProject(f.ctx, "app", api.ChangeRequest{})
		if err != nil || dry.Plan == nil || dry.Plan.Details.RepositoryIncarnation != reviewed.Incarnation {
			t.Fatalf("plan of the project's deletion: %+v %v, want incarnation %s", dry.Plan, err, reviewed.Incarnation)
		}
		op := f.confirmProjectDelete()
		if ev := f.projectDeleteEvidence(op.ID); ev.RepositoryIncarnation != reviewed.Incarnation {
			t.Fatalf("the deletion recorded incarnation %q, want %s", ev.RepositoryIncarnation, reviewed.Incarnation)
		}
		var atDelete control.ProjectDeleteResource
		f.l.git.driver = &watchedRepositories{Driver: f.l.git.driver, beforeDelete: func() {
			ev := f.projectDeleteEvidence(op.ID)
			atDelete = ev.Resources[len(ev.Resources)-1]
		}}
		f.l.processProjectDelete(op)
		if done := f.operation(op.ID); done.Status != "completed" || f.repository().Exists || !atDelete.DeleteIssued {
			t.Fatalf("project deletion ended %s (%s), repository resource at the delete %+v", done.Status, done.Diagnostic, atDelete)
		}
	})
	t.Run("made again", func(t *testing.T) {
		f := newSessionFixture(t)
		if _, err := f.driver.Stop(f.ctx, f.spec()); err != nil {
			t.Fatal(err)
		}
		op := f.confirmProjectDelete()
		if err := f.l.git.driver.DeleteRepository(f.ctx, f.repository(), func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		again, err := f.l.git.driver.CreateRepository(f.ctx, "app")
		if err != nil {
			t.Fatal(err)
		}
		f.l.processProjectDelete(op)
		blocked := f.operation(op.ID)
		if blocked.Status != "blocked" || blocked.Reason != api.ReasonChanged || f.repository() != again {
			t.Fatalf("project deletion of a repository made again: %s (%s), repository %+v, want %+v", blocked.Status, blocked.Reason, f.repository(), again)
		}
		if f.runtime().State != runtimedriver.StateAbsent {
			t.Fatal("the session's runtime was kept")
		}
	})
}
