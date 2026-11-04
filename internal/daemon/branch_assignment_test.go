package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver"
	sourcememory "github.com/lgvo/p.ai/internal/sourcedriver/memory"
)

// lostHeads is a source driver whose next head effects can lose their
// answers, as a driver cut off while Git ran would, and whose settlements
// can stay unknown, as while an effect it admitted may still land.
type lostHeads struct {
	sourcedriver.Driver
	// lose makes the next lose head effects answer ErrUnknownOutcome after
	// they ran, and run after, a native change, before each answer.
	lose  int
	after func()
	// drop makes the next head effect answer ErrUnknownOutcome after its
	// gate, without running.
	drop bool
	// unavailable makes the next head effect fail before its gate, as a
	// driver that cannot be reached does.
	unavailable bool
	// settleDown makes the next Settle fail, as a driver that is down would.
	settleDown bool
	// unsettled makes the next unsettled Settles answer Unknown.
	unsettled int
	// effects counts the head effects sent to the driver.
	effects int
}

func (d *lostHeads) Settle(ctx context.Context, e sourcedriver.Effect) (sourcedriver.Settlement, error) {
	switch {
	case d.settleDown:
		d.settleDown = false
		return sourcedriver.Unknown, fmt.Errorf("the driver is down: %w", sourcedriver.ErrUnavailable)
	case d.unsettled > 0:
		d.unsettled--
		return sourcedriver.Unknown, nil
	}
	return d.Driver.Settle(ctx, e)
}

func (d *lostHeads) CreateBranch(ctx context.Context, project, name string, at sourcedriver.OID, g sourcedriver.Gate) error {
	return d.effect(g, func(g sourcedriver.Gate) error { return d.Driver.CreateBranch(ctx, project, name, at, g) })
}

func (d *lostHeads) DeleteBranch(ctx context.Context, project, name string, tip sourcedriver.OID, g sourcedriver.Gate) error {
	return d.effect(g, func(g sourcedriver.Gate) error { return d.Driver.DeleteBranch(ctx, project, name, tip, g) })
}

func (d *lostHeads) effect(g sourcedriver.Gate, run func(sourcedriver.Gate) error) error {
	if d.unavailable {
		d.unavailable = false
		return fmt.Errorf("the backend cannot be reached: %w", sourcedriver.ErrUnavailable)
	}
	d.effects++
	lost := fmt.Errorf("the answer was lost: %w", sourcedriver.ErrUnknownOutcome)
	switch {
	case d.drop:
		d.drop = false
		if err := g(); err != nil {
			return err
		}
		return lost
	case d.lose > 0:
		d.lose--
		if err := run(g); err != nil {
			return err
		}
		if d.after != nil {
			d.after()
		}
		return lost
	}
	return run(g)
}

// assignmentFixture is a lifecycle over a real store with project app and an
// in-memory source driver whose repository holds main at tip.
type assignmentFixture struct {
	t      *testing.T
	ctx    context.Context
	l      *lifecycle
	world  *sourcememory.World
	source *lostHeads
	tip    sourcedriver.OID
}

func newAssignmentFixture(t *testing.T) *assignmentFixture {
	t.Helper()
	ctx := context.Background()
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
	world.SetHead("app", "main", tip)
	source := &lostHeads{Driver: driver}
	return &assignmentFixture{t: t, ctx: ctx, l: &lifecycle{ctx: ctx, store: store, git: &gitCapability{driver: source}}, world: world, source: source, tip: tip}
}

// restart gives the fixture a new source driver over the same backend, as a
// daemon restart does; the lost-answer controls start over.
func (f *assignmentFixture) restart() {
	f.source = &lostHeads{Driver: sourcememory.NewDriver(f.world)}
	f.l.git.driver = f.source
}

// head is the tip of head branch, empty when nothing holds it.
func (f *assignmentFixture) head(branch string) sourcedriver.OID {
	f.t.Helper()
	tip, _, err := f.source.InspectBranch(f.ctx, "app", branch)
	if err != nil {
		f.t.Fatal(err)
	}
	return tip
}

var assignmentSelection = control.CreationSelection{}

// creation records creation req with captured as its captured source, as
// its reservation does, and returns the operation as the worker finds it.
func (f *assignmentFixture) creation(req control.ReserveSessionRequest, captured control.CapturedSource) *api.Operation {
	f.t.Helper()
	op, _, err := f.l.store.BeginSessionCreateCaptured(f.ctx, req, strings.Repeat("d", 64), assignmentSelection, func(context.Context, control.ReserveSessionRequest) (control.CapturedSource, error) {
		return captured, nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return &op
}

// load is operation id as a resumed worker reads it.
func (f *assignmentFixture) load(id string) *api.Operation {
	f.t.Helper()
	op, err := f.l.store.GetOperation(f.ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return &op
}

// local is a local creation's request of a new branch from main.
func local(key, branch string) control.ReserveSessionRequest {
	return control.ReserveSessionRequest{Key: key, Project: "app", Branch: branch, Choice: "new", Source: "refs/heads/main"}
}

// A new branch is created once: a creation that runs again settles the one
// it sent and goes on. A head that holds the branch elsewhere before the
// creation was sent is refused and left alone.
func TestBranchAssignmentCreatesTheNewBranchOnce(t *testing.T) {
	f := newAssignmentFixture(t)
	tip := string(f.tip)
	op := f.creation(local("create", "work"), control.CapturedSource{OID: tip})
	if err := f.l.ensureBranchAssigned(f.ctx, op, local("create", "work"), tip); err != nil {
		t.Fatal(err)
	}
	if err := f.l.ensureBranchAssigned(f.ctx, f.load(op.ID), local("create", "work"), tip); err != nil {
		t.Fatal(err)
	}
	if f.source.effects != 1 || f.head("work") != f.tip {
		t.Fatalf("work at %q after %d effects, want %s after 1", f.head("work"), f.source.effects, tip)
	}
	moved := f.world.Commit("app", f.tip)
	f.world.SetHead("app", "taken", moved)
	taken := f.creation(local("taken", "taken"), control.CapturedSource{OID: tip})
	err := f.l.ensureBranchAssigned(f.ctx, taken, local("taken", "taken"), tip)
	wantRefusal(t, "a new branch a head holds elsewhere", err, api.KindPreconditionFailed, api.ReasonChanged, "")
	if f.source.effects != 1 || f.head("taken") != moved {
		t.Fatalf("taken at %q after %d effects", f.head("taken"), f.source.effects)
	}
}

// An existing branch is assigned as captured and never changed; a moved one
// is refused.
func TestBranchAssignmentKeepsAnExistingBranch(t *testing.T) {
	f := newAssignmentFixture(t)
	f.world.SetHead("app", "kept", f.tip)
	req := control.ReserveSessionRequest{Key: "existing", Project: "app", Branch: "kept", Choice: "existing"}
	op := f.creation(req, control.CapturedSource{OID: string(f.tip), Existed: true})
	if err := f.l.ensureBranchAssigned(f.ctx, op, req, string(f.tip)); err != nil || f.source.effects != 0 || f.head("kept") != f.tip {
		t.Fatalf("an existing branch: %v, kept at %q after %d effects", err, f.head("kept"), f.source.effects)
	}
	moved := f.world.Commit("app", f.tip)
	f.world.SetHead("app", "kept", moved)
	err := f.l.ensureBranchAssigned(f.ctx, op, req, string(f.tip))
	wantRefusal(t, "a moved existing branch", err, api.KindPreconditionFailed, api.ReasonChanged, "")
	if f.source.effects != 0 || f.head("kept") != moved {
		t.Fatalf("kept at %q after %d effects", f.head("kept"), f.source.effects)
	}
}

// A local source must still be reachable from a head when the branch is
// created, as gitservice's broker required: a commit no head reaches answers
// not_found source and creates nothing.
func TestBranchAssignmentNeedsAHeadToReachALocalSource(t *testing.T) {
	f := newAssignmentFixture(t)
	orphan := string(f.world.Commit("app"))
	op := f.creation(local("create", "work"), control.CapturedSource{OID: orphan})
	err := f.l.ensureBranchAssigned(f.ctx, op, local("create", "work"), orphan)
	wantRefusal(t, "an orphan commit", err, api.KindNotFound, api.ReasonSource, "")
	if f.source.effects != 0 || f.head("work") != "" {
		t.Fatalf("work at %q after %d effects", f.head("work"), f.source.effects)
	}
}

// The creation is recorded as sent right before the source driver gets it.
func TestBranchAssignmentRecordsTheCreationBeforeSendingIt(t *testing.T) {
	f := newAssignmentFixture(t)
	op := f.creation(local("create", "work"), control.CapturedSource{OID: string(f.tip)})
	var recorded bool
	f.source.lose, f.source.after = 1, func() {
		ev, err := control.Evidence(*f.load(op.ID))
		recorded = err == nil && ev.BranchCreateIssued
	}
	if err := f.l.ensureBranchAssigned(f.ctx, op, local("create", "work"), string(f.tip)); err != nil || !recorded {
		t.Fatalf("a creation recorded as sent: %t, %v", recorded, err)
	}
}

// A creation whose answer was lost is settled, never sent blindly: applied
// goes on, not applied fails with reason git and leaves no head, and a head
// neither absent nor at the commit leaves the outcome unknown.
func TestBranchAssignmentSettlesALostCreation(t *testing.T) {
	t.Run("applied", func(t *testing.T) {
		f := newAssignmentFixture(t)
		op := f.creation(local("create", "work"), control.CapturedSource{OID: string(f.tip)})
		f.source.lose = 1
		if err := f.l.ensureBranchAssigned(f.ctx, op, local("create", "work"), string(f.tip)); err != nil || f.source.effects != 1 || f.head("work") != f.tip {
			t.Fatalf("a lost answer of an applied creation: %v, work at %q after %d effects", err, f.head("work"), f.source.effects)
		}
	})
	t.Run("not applied", func(t *testing.T) {
		f := newAssignmentFixture(t)
		op := f.creation(local("create", "work"), control.CapturedSource{OID: string(f.tip)})
		f.source.drop = true
		err := f.l.ensureBranchAssigned(f.ctx, op, local("create", "work"), string(f.tip))
		wantRefusal(t, "a creation that did not happen", err, api.KindFailed, api.ReasonGit, "")
		if f.source.effects != 1 || f.head("work") != "" {
			t.Fatalf("work at %q after %d effects", f.head("work"), f.source.effects)
		}
	})
	t.Run("unknown", func(t *testing.T) {
		f := newAssignmentFixture(t)
		op := f.creation(local("create", "work"), control.CapturedSource{OID: string(f.tip)})
		moved := f.world.Commit("app", f.tip)
		f.source.lose, f.source.after = 1, func() { f.world.SetHead("app", "work", moved) }
		err := f.l.ensureBranchAssigned(f.ctx, op, local("create", "work"), string(f.tip))
		wantRefusal(t, "a creation the driver cannot settle", err, api.KindFailed, api.ReasonOutcomeUnknown, "")
		if f.source.effects != 1 || f.head("work") != moved {
			t.Fatalf("work at %q after %d effects", f.head("work"), f.source.effects)
		}
	})
}

// A Retry or a restart that finds the creation sent settles it first: one
// that never applied is sent again, one that applied goes on without being
// sent again, and while the driver cannot settle it the creation stays
// blocked whatever the head shows.
func TestBranchAssignmentRecoveryOfASentCreationSettlesItFirst(t *testing.T) {
	for _, test := range []struct {
		name string
		// lost loses the first creation's answer: lose after it applied, or
		// drop it before.
		lost func(*lostHeads)
		// unsettled is how many settlements stay unknown after the restart.
		unsettled int
		// blocked is the recovery's refusal reason, empty when it goes on.
		blocked string
		effects int
		head    bool
	}{
		{"absent, not applied", func(d *lostHeads) { d.drop = true }, 0, "", 2, true},
		{"absent, unknown", func(d *lostHeads) { d.drop = true }, 1, api.ReasonOutcomeUnknown, 1, false},
		{"present, applied", func(d *lostHeads) { d.lose, d.settleDown = 1, true }, 0, "", 1, true},
		{"present, unknown", func(d *lostHeads) { d.lose, d.settleDown = 1, true }, 1, api.ReasonOutcomeUnknown, 1, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newAssignmentFixture(t)
			req := local("create", "work")
			op := f.creation(req, control.CapturedSource{OID: string(f.tip)})
			test.lost(f.source)
			if err := f.l.ensureBranchAssigned(f.ctx, op, req, string(f.tip)); err == nil {
				t.Fatal("a lost creation went on")
			}
			effects := f.source.effects
			f.restart()
			f.source.unsettled = test.unsettled
			err := f.l.ensureBranchAssigned(f.ctx, f.load(op.ID), req, string(f.tip))
			if test.blocked == "" && err != nil {
				t.Fatalf("recovery: %v", err)
			}
			if test.blocked != "" {
				wantRefusal(t, "recovery", err, api.KindFailed, test.blocked, "")
			}
			if effects+f.source.effects != test.effects || (f.head("work") == f.tip) != test.head {
				t.Fatalf("work at %q after %d effects, want %d", f.head("work"), effects+f.source.effects, test.effects)
			}
		})
	}
}

// An origin creation's head is authorized by its recorded operation alone:
// the commit, fetched from the origin, needs no head to reach it, but the
// operation must be the pending creation that recorded that commit and that
// origin ref, with its session still creating.
func TestBranchAssignmentChecksTheRecordedOriginCreation(t *testing.T) {
	f := newAssignmentFixture(t)
	fetched := string(f.world.Commit("app"))
	origin := func(key, branch string) control.ReserveSessionRequest {
		return control.ReserveSessionRequest{Key: key, Project: "app", Branch: branch, Choice: "new", OriginRef: "refs/heads/main", ExpectedCommitOID: fetched}
	}
	fromOrigin := control.CapturedSource{OID: fetched, OriginURL: "ssh://origin.example/app.git", OriginRef: "refs/heads/main"}
	req := origin("origin", "work")
	op := f.creation(req, fromOrigin)
	other := f.creation(control.ReserveSessionRequest{Key: "other", Project: "app", Branch: "other", Choice: "existing"}, control.CapturedSource{OID: string(f.tip), Existed: true})
	established := origin("established", "established")
	establishedOp := f.creation(established, fromOrigin)
	if err := f.l.store.AdvanceSessionRegistry(f.ctx, establishedOp.SessionUUID, "creating", "established"); err != nil {
		t.Fatal(err)
	}
	advanced := origin("advanced", "advanced")
	advancedOp := f.creation(advanced, fromOrigin)
	if err := f.l.store.AdvanceOperation(f.ctx, advancedOp.ID, "running", "branch-assigned", true, advancedOp.Evidence, "", ""); err != nil {
		t.Fatal(err)
	}
	// The recorded evidence names another origin ref than the request.
	retagged := origin("retagged", "retagged")
	retaggedOp := f.creation(retagged, fromOrigin)
	ev, err := control.Evidence(*retaggedOp)
	if err != nil {
		t.Fatal(err)
	}
	ev.OriginRef = "refs/tags/v1"
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.l.store.AdvanceOperation(f.ctx, retaggedOp.ID, "running", retaggedOp.Phase, false, raw, "", ""); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		op     *api.Operation
		req    control.ReserveSessionRequest
		commit string
	}{
		{"another operation", other, req, fetched},
		{"another commit", op, req, string(f.tip)},
		{"a session no longer creating", establishedOp, established, fetched},
		{"a creation past its source", f.load(advancedOp.ID), advanced, fetched},
		{"evidence of another origin ref", f.load(retaggedOp.ID), retagged, fetched},
	} {
		if err := f.l.ensureBranchAssigned(f.ctx, test.op, test.req, test.commit); err == nil || f.source.effects != 0 {
			t.Fatalf("%s created the head after %d effects: %v", test.name, f.source.effects, err)
		}
	}
	if err := f.l.ensureBranchAssigned(f.ctx, op, req, fetched); err != nil || f.head("work") != sourcedriver.OID(fetched) {
		t.Fatalf("the recorded origin creation: %v, work at %q", err, f.head("work"))
	}
}

// noRuntimes is the fake image driver, which creates no session runtime: a
// creation stops there with a definite failure.
type noRuntimes struct{ *fakeImages }

func (noRuntimes) Create(context.Context, runtimedriver.Session, runtimedriver.Incarnation, runtimedriver.Gate) (runtimedriver.Observation, error) {
	return runtimedriver.Observation{}, errors.New("this test creates no runtime")
}

// A new branch's creation with an image to build goes from its source
// through its image on its first run: the evidence the image step records
// keeps the branch's creation recorded as sent.
func TestNewBranchCreationBuildsItsImageOnItsFirstRun(t *testing.T) {
	f := newImageFixture(t)
	l := f.l
	ctx := l.ctx
	l.driver = noRuntimes{f.driver}
	selection := imageTestSelection()
	l.cfg.ProjectPolicies = map[string]control.ProjectPolicy{"app": {Network: "none", FilesystemMounts: []control.FilesystemGrant{}, Command: []string{"/bin/sh"}}}
	prefix, err := os.MkdirTemp("/tmp", "p-creation-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(prefix) })
	l.cfg.EndpointPrefix = prefix
	if l.endpoints, err = newEndpointManager(prefix, "127.0.0.1:1", l.store); err != nil {
		t.Fatal(err)
	}
	req := control.ReserveSessionRequest{Key: "fresh", Project: "app", Branch: "fresh", Choice: "new", Source: "refs/heads/next"}
	op, _, err := l.store.BeginSessionCreateCaptured(ctx, req, l.cfg.BaseImageFingerprint, selection, func(context.Context, control.ReserveSessionRequest) (control.CapturedSource, error) {
		return control.CapturedSource{OID: f.commits[1]}, nil
	}, l.environmentIntent())
	if err != nil {
		t.Fatal(err)
	}
	l.processCreation(op)
	after := f.load(t, op.ID)
	ev, err := control.Evidence(after)
	if err != nil || phaseRank(after.Phase) < phaseRank("image-ready") || ev.Image == nil || !ev.BranchCreateIssued {
		t.Fatalf("the creation's first run ended %s at %s (%s: %s), image %+v, branch creation sent %t: %v", after.Status, after.Phase, after.Reason, after.Diagnostic, ev.Image, ev.BranchCreateIssued, err)
	}
}
