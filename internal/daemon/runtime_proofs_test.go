package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/runtimedriver/memory"
)

// proofWorld is the in-memory driver's scope with the lifecycle that builds
// specs for it.
type proofWorld struct {
	t      *testing.T
	world  *memory.World
	driver *memory.Driver
	l      *lifecycle
}

const proofBuilder = "proof-builder"

func newProofWorld(t *testing.T) *proofWorld {
	world := memory.NewWorld(memory.Config{Capacity: 8, Capabilities: runtimedriver.Capabilities{ImageBuilder: proofBuilder}})
	driver := memory.NewDriver(world)
	return &proofWorld{t: t, world: world, driver: driver,
		l: &lifecycle{ctx: t.Context(), driver: driver, git: testGitCapability(), cfg: control.RuntimeConfig{EndpointPrefix: "/run/p-endpoints"}}}
}

// spec is the runtime part of a session's spec, as the proofs give it.
func (p *proofWorld) spec(uuid, project string) runtimedriver.Session {
	return p.l.runtimeSpec(uuid, project, string(p.world.BaseImage()), nil, "")
}

// create creates the runtime of spec, stopped and complete.
func (p *proofWorld) create(spec runtimedriver.Session) runtimedriver.Observation {
	p.t.Helper()
	full := spec
	full.Command, full.GitHostKey, full.Workspace.Branch, full.GitKey = []string{"/bin/sh"}, p.l.git.info.HostPublicKey, "main", testSessionKey
	o, err := p.driver.Create(p.t.Context(), full, "", func() error { return nil })
	if err != nil {
		p.t.Fatal(err)
	}
	return o
}

// leaveBuilder starts a build of the commit whose publication never lands,
// so that its builder stays in scope.
func (p *proofWorld) leaveBuilder(build, project, commit string) {
	p.t.Helper()
	p.world.Delay(runtimedriver.EffectBuild)
	b := runtimedriver.Build{
		Ref: runtimedriver.Ref{Kind: runtimedriver.KindBuild, ID: build},
		Labels: runtimedriver.Labels{runtimedriver.LabelProject: project, runtimedriver.LabelSourceCommit: commit,
			runtimedriver.LabelBaseImage: string(p.world.BaseImage()), runtimedriver.LabelSystem: "x86_64-linux",
			runtimedriver.LabelBuilderVersion: proofBuilder, runtimedriver.LabelBuild: build},
		Source: runtimedriver.SourceExport{Bundle: "/state/" + build + ".bundle", Ref: "refs/p/export", Commit: commit},
	}
	if _, err := p.driver.BuildImage(p.t.Context(), b, func() error { return nil }); !errors.Is(err, runtimedriver.ErrUnknownOutcome) {
		p.t.Fatalf("delayed BuildImage = %v, want ErrUnknownOutcome", err)
	}
}

// failingCheck is a driver whose boundary is not proven.
type failingCheck struct{ runtimedriver.Driver }

var errBoundary = errors.New("the boundary changed")

func (failingCheck) Check(context.Context) error { return errBoundary }

const (
	proofSession = "11111111-1111-4111-8111-111111111111"
	proofOther   = "22222222-2222-4222-8222-222222222222"
	proofBuild   = "33333333-3333-4333-8333-333333333333"
)

// answersPresent reports whether a removal answers err as a runtime of the
// session present where its absence was to be proven.
func answersPresent(err error) bool {
	var refusal *control.Refusal
	return errors.As(runtimeFailure(err, "session/x"), &refusal) && refusal.Kind == api.KindConflict && refusal.Reason == api.ReasonRuntimePresent
}

func TestARuntimeIsProvenAbsentWhenNothingCarriesItsReference(t *testing.T) {
	p := newProofWorld(t)
	p.create(p.spec(proofOther, "app"))
	if err := proveRuntimeAbsent(t.Context(), p.driver, p.spec(proofSession, "app")); err != nil {
		t.Fatalf("absence with only another session's runtime in scope: %v", err)
	}
}

func TestARuntimeAtItsAddressIsNotAbsent(t *testing.T) {
	p := newProofWorld(t)
	s := p.spec(proofSession, "app")
	p.create(s)
	if err := proveRuntimeAbsent(t.Context(), p.driver, s); !answersPresent(err) {
		t.Fatalf("a present runtime: %v, want runtime present", err)
	}
}

func TestARuntimeRenamedAwayFromItsAddressIsNotAbsent(t *testing.T) {
	p := newProofWorld(t)
	s := p.spec(proofSession, "app")
	p.create(s)
	p.world.RenameAway(s.Ref)
	if o, err := p.driver.Inspect(t.Context(), s); err != nil || o.State != runtimedriver.StateAbsent {
		t.Fatalf("Inspect after the rename = %+v, %v; want absent at the address", o, err)
	}
	if err := proveRuntimeAbsent(t.Context(), p.driver, s); !answersPresent(err) {
		t.Fatalf("a renamed runtime: %v, want runtime present", err)
	}
}

func TestAnObjectAnotherInstanceLabeledWithTheReferenceBlocksAbsence(t *testing.T) {
	p := newProofWorld(t)
	s := p.spec(proofSession, "app")
	p.world.AddOtherInstance(s.Ref, runtimedriver.Labels{runtimedriver.LabelProject: "app"})
	if err := proveRuntimeAbsent(t.Context(), p.driver, s); !answersPresent(err) {
		t.Fatalf("another instance's object carries the reference: %v, want runtime present", err)
	}
}

func TestAPartialInventoryOrAnUnprovenBoundaryProvesNothing(t *testing.T) {
	p := newProofWorld(t)
	s := p.spec(proofSession, "app")
	p.world.PartialInventory()
	if err := proveRuntimeAbsent(t.Context(), p.driver, s); err == nil || answersPresent(err) {
		t.Fatalf("a partial inventory: %v, want a runtime failure", err)
	}
	if err := proveRuntimeAbsent(t.Context(), failingCheck{p.driver}, s); !errors.Is(err, errBoundary) {
		t.Fatalf("absence with an unproven boundary = %v, want the check's error", err)
	}
}

func TestAFailedCreationIsAbsentOnlyWithoutItsBuilder(t *testing.T) {
	p := newProofWorld(t)
	s := p.spec(proofSession, "app")
	p.leaveBuilder(proofOther, "app", "0123456789abcdef0123456789abcdef01234567")
	if err := proveFailedCreationAbsent(t.Context(), p.driver, s, proofBuild); err != nil {
		t.Fatalf("another build's builder blocked the creation's absence: %v", err)
	}
	p.leaveBuilder(proofBuild, "app", "0123456789abcdef0123456789abcdef01234567")
	if err := proveFailedCreationAbsent(t.Context(), p.driver, s, proofBuild); err == nil {
		t.Fatal("a failed creation was proven absent while its builder remains")
	}
}

func TestAFailedCreationIsNotAbsentWhileItsRuntimeIs(t *testing.T) {
	p := newProofWorld(t)
	s := p.spec(proofSession, "app")
	p.create(s)
	p.world.RenameAway(s.Ref)
	if err := proveFailedCreationAbsent(t.Context(), p.driver, s, proofBuild); err == nil {
		t.Fatal("a failed creation was proven absent while its renamed runtime remains")
	}
}

func TestAnAssembledSourceIsTheStoppedRuntimeTheReviewSaw(t *testing.T) {
	p := newProofWorld(t)
	s := p.spec(proofSession, "app")
	at := p.create(s).Incarnation
	if err := proveAssembledSource(t.Context(), p.driver, s, proofBuild, at); err != nil {
		t.Fatalf("the reviewed stopped runtime: %v", err)
	}
	if err := proveAssembledSource(t.Context(), p.driver, s, proofBuild, "another-incarnation"); err == nil {
		t.Fatal("a runtime of another incarnation was accepted")
	}
	if err := proveAssembledSource(t.Context(), p.driver, s, proofBuild, ""); err == nil {
		t.Fatal("an unrecorded incarnation was accepted")
	}
	if err := proveAssembledSource(t.Context(), failingCheck{p.driver}, s, proofBuild, at); !errors.Is(err, errBoundary) {
		t.Fatalf("an unproven boundary = %v, want the check's error", err)
	}
}

func TestARunningAssembledSourceIsRefused(t *testing.T) {
	p := newProofWorld(t)
	s := p.spec(proofSession, "app")
	at := p.create(s).Incarnation
	full := s
	full.Command, full.GitHostKey, full.Workspace.Branch = []string{"/bin/sh"}, p.l.git.info.HostPublicKey, "main"
	if _, err := p.driver.Start(t.Context(), full); err != nil {
		t.Fatal(err)
	}
	if err := proveAssembledSource(t.Context(), p.driver, s, proofBuild, at); err == nil {
		t.Fatal("a running runtime was accepted as the stopped source")
	}
}

func TestACompetingIdentityOrABuilderBlocksTheAssembledSource(t *testing.T) {
	p := newProofWorld(t)
	s := p.spec(proofSession, "app")
	at := p.create(s).Incarnation
	p.world.AddOtherInstance(s.Ref, runtimedriver.Labels{runtimedriver.LabelProject: "app"})
	if err := proveAssembledSource(t.Context(), p.driver, s, proofBuild, at); err == nil {
		t.Fatal("a second object carrying the session's reference was accepted")
	}
	p = newProofWorld(t)
	at = p.create(s).Incarnation
	p.leaveBuilder(proofBuild, "app", "0123456789abcdef0123456789abcdef01234567")
	if err := proveAssembledSource(t.Context(), p.driver, s, proofBuild, at); err == nil {
		t.Fatal("the creation's builder was accepted next to its source")
	}
}

func TestAProjectIsReviewedWhenOnlyReviewedRuntimesAndImagesCarryIt(t *testing.T) {
	p := newProofWorld(t)
	s := p.spec(proofSession, "app")
	at := p.create(s).Incarnation
	img := p.world.SeedImage(runtimedriver.Labels{runtimedriver.LabelProject: "app"}, runtimedriver.Shell{Selection: runtimedriver.SelectionNoFlake})
	p.create(p.spec(proofOther, "other"))
	p.world.SeedImage(runtimedriver.Labels{runtimedriver.LabelProject: "other"}, runtimedriver.Shell{Selection: runtimedriver.SelectionNoFlake})
	runtimes := map[runtimedriver.Ref]runtimedriver.Incarnation{s.Ref: at}
	images := map[runtimedriver.ImageRef]bool{img.Ref: true}
	if err := proveProjectReviewed(t.Context(), p.driver, "app", runtimes, images); err != nil {
		t.Fatalf("the reviewed project: %v", err)
	}
	if err := proveProjectReviewed(t.Context(), p.driver, "app", map[runtimedriver.Ref]runtimedriver.Incarnation{s.Ref: "another-incarnation"}, images); err == nil {
		t.Fatal("a runtime at another incarnation than reviewed was accepted")
	}
	if err := proveProjectReviewed(t.Context(), p.driver, "app", runtimes, nil); err == nil {
		t.Fatal("an unreviewed image of the project was accepted")
	}
	if err := proveProjectReviewed(t.Context(), p.driver, "app", nil, images); err == nil {
		t.Fatal("an unreviewed runtime of the project was accepted")
	}
	if err := proveProjectReviewed(t.Context(), failingCheck{p.driver}, "app", runtimes, images); !errors.Is(err, errBoundary) {
		t.Fatalf("an unproven boundary = %v, want the check's error", err)
	}
}

func TestABuilderOrAnotherInstancesObjectLabeledWithTheProjectBlocksItsDeletion(t *testing.T) {
	p := newProofWorld(t)
	p.leaveBuilder(proofBuild, "app", "0123456789abcdef0123456789abcdef01234567")
	if err := proveProjectReviewed(t.Context(), p.driver, "app", nil, nil); err == nil {
		t.Fatal("a builder of the project was accepted")
	}
	p = newProofWorld(t)
	p.world.AddOtherInstance(runtimedriver.Ref{Kind: runtimedriver.KindSession, ID: proofOther}, runtimedriver.Labels{runtimedriver.LabelProject: "app"})
	if err := proveProjectReviewed(t.Context(), p.driver, "app", nil, nil); err == nil {
		t.Fatal("another instance's runtime labeled with the project was accepted")
	}
}

// Images are this instance's alone: another instance's image of a project
// with the same name is not this deletion's to review.
func TestAnotherInstancesImageDoesNotBlockAProjectDeletion(t *testing.T) {
	p := newProofWorld(t)
	p.world.AddOtherInstanceImage() // labeled with the project "other"
	if err := proveProjectReviewed(t.Context(), p.driver, "other", nil, nil); err != nil {
		t.Fatalf("another instance's image blocked the deletion: %v", err)
	}
}

// renameDuringInventory renames the runtime of ref away from its address
// just before it lists the scope.
type renameDuringInventory struct {
	runtimedriver.Driver
	world *memory.World
	ref   runtimedriver.Ref
}

func (d renameDuringInventory) Inventory(ctx context.Context) (runtimedriver.Inventory, error) {
	d.world.RenameAway(d.ref)
	return d.Driver.Inventory(ctx)
}

func TestAnAssembledSourceRenamedAwayDuringTheProofIsRefused(t *testing.T) {
	p := newProofWorld(t)
	s := p.spec(proofSession, "app")
	at := p.create(s).Incarnation
	renaming := renameDuringInventory{Driver: p.driver, world: p.world, ref: s.Ref}
	if err := proveAssembledSource(t.Context(), renaming, s, proofBuild, at); err == nil {
		t.Fatal("a source renamed away between its observations was accepted")
	}
}
