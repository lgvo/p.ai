package daemon

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/sourcedriver"
	sourcememory "github.com/lgvo/p.ai/internal/sourcedriver/memory"
)

// retainedWorkFixture is a lifecycle over a real store with project app and
// an in-memory source driver whose heads saved and taken are at tip, for
// running retained branch work.
type retainedWorkFixture struct {
	t      *testing.T
	ctx    context.Context
	l      *lifecycle
	world  *sourcememory.World
	source *lostHeads
	tip    sourcedriver.OID
}

func newRetainedWorkFixture(t *testing.T) *retainedWorkFixture {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	store, err := control.OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err = store.CreateProject(ctx, "app", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	world, driver := memorySource(t)
	if _, err = driver.CreateRepository(ctx, "app"); err != nil {
		t.Fatal(err)
	}
	tip := world.Commit("app")
	world.SetHeads("app", []string{"saved", "taken"}, tip)
	source := &lostHeads{Driver: driver}
	l := &lifecycle{ctx: ctx, store: store, git: &gitCapability{driver: source}, instanceID: "11111111-1111-4111-8111-111111111111"}
	return &retainedWorkFixture{t: t, ctx: ctx, l: l, world: world, source: source, tip: tip}
}

func (f *retainedWorkFixture) operation(id string) api.Operation {
	f.t.Helper()
	op, err := f.l.store.GetOperation(f.ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return op
}

func (f *retainedWorkFixture) head(branch string) sourcedriver.OID {
	f.t.Helper()
	tip, _, err := f.source.InspectBranch(f.ctx, "app", branch)
	if err != nil {
		f.t.Fatal(err)
	}
	return tip
}

// retry is the user's Retry of a blocked operation, as the worker then
// finds it.
func (f *retainedWorkFixture) retry(id string) api.Operation {
	f.t.Helper()
	op := f.operation(id)
	if err := f.l.store.AdvanceOperation(f.ctx, id, "running", op.Phase, op.Committed, op.Evidence, "", ""); err != nil {
		f.t.Fatal(err)
	}
	return f.operation(id)
}

// rename records the rename of retained branch saved to archive.
func (f *retainedWorkFixture) rename() api.Operation {
	f.t.Helper()
	op, err := f.l.store.BeginRetainedRename(f.ctx, control.RetainedRenameRequest{Key: "rename", Project: "app", OldBranch: "saved", NewBranch: "archive", ExpectedOldTip: string(f.tip)}, func(context.Context) error { return nil })
	if err != nil {
		f.t.Fatal(err)
	}
	return op
}

// A retained rename creates the new head and deletes the old one, each
// recorded by its gate before the effect; lost answers of effects that
// happened are settled and the rename goes on.
func TestRetainedRenameRecordsEachHeadEffectAndSettlesLostAnswers(t *testing.T) {
	for _, lost := range []int{0, 2} {
		f := newRetainedWorkFixture(t)
		op := f.rename()
		phases := []string{}
		f.source.after = func() { phases = append(phases, f.operation(op.ID).Phase) }
		f.source.lose = lost
		f.l.processRetainedRename(op)
		if done := f.operation(op.ID); done.Status != "completed" || f.head("saved") != "" || f.head("archive") != f.tip || f.source.effects != 2 {
			t.Fatalf("%d lost answers: rename ended %s at %s (%s), saved at %q, archive at %q, after %d effects", lost, done.Status, done.Phase, done.Diagnostic, f.head("saved"), f.head("archive"), f.source.effects)
		}
		if want := []string{"new-ref-create-issued", "old-ref-delete-issued"}[:lost]; !slices.Equal(phases, want) {
			t.Fatalf("the effects ran at phases %v, want %v", phases, want)
		}
	}
}

// A creation whose gate recorded it and which never happened blocks the
// rename at its issued phase, and is never sent again.
func TestRetainedRenameNeverSendsALostCreationAgain(t *testing.T) {
	f := newRetainedWorkFixture(t)
	op := f.rename()
	f.source.drop = true
	f.l.processRetainedRename(op)
	blocked := f.operation(op.ID)
	if blocked.Status != "blocked" || blocked.Phase != "new-ref-create-issued" || blocked.Reason != api.ReasonGit || f.head("archive") != "" {
		t.Fatalf("a lost creation: %s at %s (%s), archive at %q", blocked.Status, blocked.Phase, blocked.Reason, f.head("archive"))
	}
	f.l.processRetainedRename(f.retry(op.ID))
	if blocked = f.operation(op.ID); blocked.Status != "blocked" || blocked.Phase != "new-ref-create-issued" || f.source.effects != 1 {
		t.Fatalf("a retried lost creation: %s at %s after %d effects", blocked.Status, blocked.Phase, f.source.effects)
	}
}

// A new name a head took before the creation fails the rename as stale:
// nothing was created.
func TestRetainedRenameWhoseNewNameWasTakenFailsAsStale(t *testing.T) {
	f := newRetainedWorkFixture(t)
	op := f.rename()
	other := f.world.Commit("app")
	f.world.SetHead("app", "archive", other)
	f.l.processRetainedRename(op)
	if failed := f.operation(op.ID); failed.Status != "failed" || failed.Phase != "stale" || f.head("archive") != other || f.head("saved") != f.tip {
		t.Fatalf("a taken new name: %s at %s, archive at %q, saved at %q", failed.Status, failed.Phase, f.head("archive"), f.head("saved"))
	}
}

// deletion records the deletion of retained branch saved at the fixture's
// tip, guarded, as its run records it.
func (f *retainedWorkFixture) deletion() api.Operation {
	f.t.Helper()
	review, err := f.l.freshRetainedDeleteReview(f.ctx, "app", "saved")
	if err != nil {
		f.t.Fatal(err)
	}
	digest, err := deleteReviewDigest(review)
	if err != nil {
		f.t.Fatal(err)
	}
	ev := control.RetainedDeleteEvidence{Project: "app", Branch: "saved", Tip: string(f.tip), ReviewSHA256: digest, InstanceUUID: f.l.instanceID}
	op, err := f.l.store.BeginRetainedDelete(f.ctx, control.RetainedDeleteRequest{Key: "delete", Project: "app", Branch: "saved", IfMatch: futureTag(time.Hour)}, ev, strings.Repeat("c", 64), func(context.Context) error { return nil })
	if err != nil {
		f.t.Fatal(err)
	}
	return op
}

// A retained deletion whose answer was lost after the head went completes
// once the source driver settles it; one that never happened blocks and is
// never sent again.
func TestRetainedDeleteSettlesALostDeletion(t *testing.T) {
	t.Run("applied", func(t *testing.T) {
		f := newRetainedWorkFixture(t)
		op := f.deletion()
		f.source.lose = 1
		f.l.processRetainedDelete(op)
		if done := f.operation(op.ID); done.Status != "completed" || f.head("saved") != "" || f.source.effects != 1 {
			t.Fatalf("a lost answer of a deletion: %s at %s (%s), saved at %q after %d effects", done.Status, done.Phase, done.Diagnostic, f.head("saved"), f.source.effects)
		}
	})
	t.Run("not applied", func(t *testing.T) {
		f := newRetainedWorkFixture(t)
		op := f.deletion()
		f.source.drop = true
		f.l.processRetainedDelete(op)
		blocked := f.operation(op.ID)
		if blocked.Status != "blocked" || blocked.Phase != "ref-delete-issued" || f.head("saved") != f.tip {
			t.Fatalf("a lost deletion: %s at %s, saved at %q", blocked.Status, blocked.Phase, f.head("saved"))
		}
		f.l.processRetainedDelete(f.retry(op.ID))
		if blocked = f.operation(op.ID); blocked.Status != "blocked" || f.head("saved") != f.tip || f.source.effects != 1 {
			t.Fatalf("a retried lost deletion: %s at %s, saved at %q after %d effects", blocked.Status, blocked.Phase, f.head("saved"), f.source.effects)
		}
	})
}

// A retained rename that finds an effect issued settles it before it goes
// on, even when the heads already show it applied: while the source driver
// cannot settle it the rename stays blocked at that phase, and once the
// driver settles it applied the rename completes, sending nothing again.
func TestRetainedRenameRecoverySettlesEachIssuedEffect(t *testing.T) {
	for _, test := range []struct {
		phase string
		// lose is how many effects lose their answers; the last of them
		// cannot be settled at once.
		lose int
	}{
		{"new-ref-create-issued", 1},
		{"old-ref-delete-issued", 2},
	} {
		t.Run(test.phase, func(t *testing.T) {
			f := newRetainedWorkFixture(t)
			op := f.rename()
			lost := 0
			f.source.lose, f.source.after = test.lose, func() {
				if lost++; lost == test.lose {
					f.source.settleDown = true
				}
			}
			f.l.processRetainedRename(op)
			if blocked := f.operation(op.ID); blocked.Status != "blocked" || blocked.Phase != test.phase || blocked.Reason != api.ReasonOutcomeUnknown {
				t.Fatalf("an effect the driver could not settle: %s at %s (%s)", blocked.Status, blocked.Phase, blocked.Reason)
			}
			f.source.unsettled = 1
			f.l.processRetainedRename(f.retry(op.ID))
			if blocked := f.operation(op.ID); blocked.Status != "blocked" || blocked.Phase != test.phase || blocked.Reason != api.ReasonOutcomeUnknown {
				t.Fatalf("a recovery the driver still could not settle: %s at %s (%s)", blocked.Status, blocked.Phase, blocked.Reason)
			}
			f.l.processRetainedRename(f.retry(op.ID))
			if done := f.operation(op.ID); done.Status != "completed" || f.head("saved") != "" || f.head("archive") != f.tip || f.source.effects != 2 {
				t.Fatalf("a settled recovery: %s at %s (%s) after %d effects", done.Status, done.Phase, done.Diagnostic, f.source.effects)
			}
		})
	}
}

// A retained deletion that finds its deletion issued settles it before it
// completes, even when the branch is already gone.
func TestRetainedDeleteRecoverySettlesTheIssuedDeletion(t *testing.T) {
	f := newRetainedWorkFixture(t)
	op := f.deletion()
	f.source.lose, f.source.settleDown = 1, true
	f.l.processRetainedDelete(op)
	if blocked := f.operation(op.ID); blocked.Status != "blocked" || blocked.Phase != "ref-delete-issued" || blocked.Reason != api.ReasonOutcomeUnknown || f.head("saved") != "" {
		t.Fatalf("a deletion the driver could not settle: %s at %s (%s)", blocked.Status, blocked.Phase, blocked.Reason)
	}
	f.source.unsettled = 1
	f.l.processRetainedDelete(f.retry(op.ID))
	if blocked := f.operation(op.ID); blocked.Status != "blocked" || blocked.Reason != api.ReasonOutcomeUnknown {
		t.Fatalf("a recovery the driver still could not settle: %s at %s (%s)", blocked.Status, blocked.Phase, blocked.Reason)
	}
	f.l.processRetainedDelete(f.retry(op.ID))
	if done := f.operation(op.ID); done.Status != "completed" || f.source.effects != 1 {
		t.Fatalf("a settled recovery: %s at %s (%s) after %d effects", done.Status, done.Phase, done.Diagnostic, f.source.effects)
	}
}

// A source driver that cannot be reached when a retained branch's head is to
// change blocks the operation with reason git.
func TestRetainedWorkWithTheSourceDriverDownBlocksWithGit(t *testing.T) {
	t.Run("rename", func(t *testing.T) {
		f := newRetainedWorkFixture(t)
		op := f.rename()
		f.source.Driver.(*sourcememory.Driver).Kill()
		f.l.processRetainedRename(op)
		if blocked := f.operation(op.ID); blocked.Status != "blocked" || blocked.Reason != api.ReasonGit {
			t.Fatalf("a rename with the driver down: %s at %s (%s)", blocked.Status, blocked.Phase, blocked.Reason)
		}
	})
	t.Run("delete", func(t *testing.T) {
		f := newRetainedWorkFixture(t)
		op := f.deletion()
		f.source.unavailable = true
		f.l.processRetainedDelete(op)
		if blocked := f.operation(op.ID); blocked.Status != "blocked" || blocked.Phase != "ref-delete-issued" || blocked.Reason != api.ReasonGit {
			t.Fatalf("a deletion with the driver unreachable: %s at %s (%s)", blocked.Status, blocked.Phase, blocked.Reason)
		}
	})
}
