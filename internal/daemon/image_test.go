package daemon

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimedriver"
)

func TestASessionFromANewCommitBuildsAnImageTaggedWithIt(t *testing.T) {
	f := newImageFixture(t)
	op := f.create(t, "work", f.commits[1])
	image, err := f.ensure(t, &op)
	if err != nil {
		t.Fatal(err)
	}
	if f.driver.buildCount() != 1 {
		t.Fatalf("builds: %d", f.driver.buildCount())
	}
	build := f.driver.builds[0]
	// refs/p/source is the one ref of the bundled source driver's export.
	if build.Ref.ID != control.BuildRef(op.ID, 1) || build.Source.Commit != f.commits[1] || build.Source.Ref != "refs/p/source" || !f.driver.bundles[0] {
		t.Fatalf("build of another source: %+v", build)
	}
	if _, err := os.Stat(build.Source.Bundle); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged bundle left behind: %v", err)
	}
	want := map[string]string{"project": "app", "source_commit": f.commits[1], "base_image": f.l.cfg.BaseImageFingerprint,
		"system": "x86_64-linux", "builder_version": "fake-builder/1", "build": build.Ref.ID}
	built := f.driver.projectImages("app")
	if len(built) != 1 || string(built[0].Ref) != image || !maps.Equal(built[0].Labels, want) {
		t.Fatalf("driver image %+v, want labels %v", built, want)
	}
	row, found := f.row(t, f.commits[1])
	if !found || row.Ref != image || row.Build != build.Ref.ID || !maps.Equal(row.Labels, built[0].Labels) || row.Selection != "devshell" || row.State != "ready" {
		t.Fatalf("index row %+v differs from the driver's image", row)
	}
	op = f.load(t, op.ID)
	ev := f.evidence(t, op.ID)
	if op.Phase != "image-ready" || ev.ImageFingerprint != image || ev.Image == nil || *ev.Image != (control.CreationImage{Ref: image, Built: true, Selection: "devshell", MaterialSHA256: row.MaterialSHA256}) ||
		*ev.EnvironmentBuilder != (control.CreationBuilderState{Cycle: 1, State: "absent"}) {
		t.Fatalf("creation did not record its image: %s %+v %+v", op.Phase, ev.Image, ev.EnvironmentBuilder)
	}
}

func TestASecondSessionFromTheSameCommitReusesItsImage(t *testing.T) {
	f := newImageFixture(t)
	first := f.create(t, "first", f.commits[0])
	image, err := f.ensure(t, &first)
	if err != nil {
		t.Fatal(err)
	}
	second := f.create(t, "second", f.commits[0])
	reused, err := f.ensure(t, &second)
	if err != nil || reused != image {
		t.Fatalf("second session image %s, want %s: %v", reused, image, err)
	}
	if f.driver.buildCount() != 1 {
		t.Fatalf("second session built again: %d builds", f.driver.buildCount())
	}
	ev := f.evidence(t, second.ID)
	if ev.Image == nil || ev.Image.Built || ev.Image.Ref != image || ev.EnvironmentBuilder.State != "not-attempted" {
		t.Fatalf("reuse not recorded: %+v %+v", ev.Image, ev.EnvironmentBuilder)
	}
}

func TestReleaseKeepsAnImageWhileASessionFromItsCommitExists(t *testing.T) {
	f := newImageFixture(t)
	op := f.create(t, "work", f.commits[0])
	image, err := f.ensure(t, &op)
	if err != nil {
		t.Fatal(err)
	}
	// A failed creation waiting for Retry, an established session and one
	// being removed all keep their commit's image.
	if err = f.l.store.AdvanceOperation(f.l.ctx, op.ID, "blocked", "image-ready", true, f.load(t, op.ID).Evidence, "internal", "fixture failure"); err != nil {
		t.Fatal(err)
	}
	for _, registry := range []string{"creating", "established", "removing"} {
		if registry != "creating" {
			f.setRegistry(t, op.SessionUUID, registry)
		}
		if err = f.l.releaseImages(f.l.ctx, "app"); err != nil {
			t.Fatal(err)
		}
		if _, found := f.row(t, f.commits[0]); !found || len(f.driver.projectImages("app")) != 1 {
			t.Fatalf("image released while a %s session started from its commit", registry)
		}
	}
	f.setRegistry(t, op.SessionUUID, "gone")
	if err = f.l.releaseImages(f.l.ctx, "app"); err != nil {
		t.Fatal(err)
	}
	if _, found := f.row(t, f.commits[0]); found || len(f.driver.projectImages("app")) != 0 {
		t.Fatalf("image %s kept after the last session from its commit was gone", image)
	}
}

func TestReleaseNeverDeletesWhileACreationHoldsTheCommitLock(t *testing.T) {
	f := newImageFixture(t)
	op := f.create(t, "work", f.commits[0])
	if _, err := f.ensure(t, &op); err != nil {
		t.Fatal(err)
	}
	f.setRegistry(t, op.SessionUUID, "established")
	f.setRegistry(t, op.SessionUUID, "removing")
	f.setRegistry(t, op.SessionUUID, "gone")
	release, err := f.l.lockCommit(f.l.ctx, "app", f.commits[0])
	if err != nil {
		t.Fatal(err)
	}
	if err = f.l.releaseImages(f.l.ctx, "app"); err != nil {
		t.Fatal(err)
	}
	if row, found := f.row(t, f.commits[0]); !found || row.State != "ready" || len(f.driver.projectImages("app")) != 1 {
		t.Fatal("release touched an image while a creation held its commit")
	}
	release()
	if err = f.l.releaseImages(f.l.ctx, "app"); err != nil {
		t.Fatal(err)
	}
	if _, found := f.row(t, f.commits[0]); found || len(f.driver.projectImages("app")) != 0 {
		t.Fatal("unused image kept after the commit's lock was free")
	}
}

func TestACreationNeverReusesAReleasingImage(t *testing.T) {
	f := newImageFixture(t)
	first := f.create(t, "first", f.commits[0])
	old, err := f.ensure(t, &first)
	if err != nil {
		t.Fatal(err)
	}
	f.setRegistry(t, first.SessionUUID, "gone")
	// Release marked the row and its delete's outcome is not known yet.
	row, _ := f.row(t, f.commits[0])
	if err = f.l.store.MarkCommitImageReleasing(f.l.ctx, row.Key, row.Ref); err != nil {
		t.Fatal(err)
	}
	second := f.create(t, "second", f.commits[0])
	image, err := f.ensure(t, &second)
	if err != nil {
		t.Fatal(err)
	}
	if image == old || f.driver.buildCount() != 2 {
		t.Fatalf("releasing image %s reused: got %s after %d builds", old, image, f.driver.buildCount())
	}
	if _, found, _ := f.driver.InspectImage(f.l.ctx, runtimedriver.ImageRef(old)); found {
		t.Fatal("the releasing image's delete was not finished")
	}
	if row, found := f.row(t, f.commits[0]); !found || row.Ref != image || row.State != "ready" {
		t.Fatalf("index row %+v does not name the new image", row)
	}
}

func TestAnUnknownBuildOutcomeStaysAttemptedUntilSettled(t *testing.T) {
	for _, settlement := range []runtimedriver.Settlement{runtimedriver.Applied, runtimedriver.Unknown} {
		t.Run(settlement.String(), func(t *testing.T) {
			f := newImageFixture(t)
			f.driver.unknown = true
			op := f.create(t, "work", f.commits[0])
			_, err := f.ensure(t, &op)
			if !errors.Is(err, runtimedriver.ErrUnknownOutcome) || control.OperationReason(err) != api.ReasonOutcomeUnknown {
				t.Fatalf("unknown outcome: %v, reason %s", err, control.OperationReason(err))
			}
			first := control.BuildRef(op.ID, 1)
			if b := f.evidence(t, op.ID).EnvironmentBuilder; *b != (control.CreationBuilderState{Cycle: 1, State: "attempted"}) {
				t.Fatalf("unknown outcome ended the attempt: %+v", b)
			}
			if _, found := f.row(t, f.commits[0]); found {
				t.Fatal("an unsettled build was indexed")
			}
			// Retry settles the build before anything else.
			f.driver.unknown = false
			f.driver.settlement = settlement
			op = f.load(t, op.ID)
			image, err := f.ensure(t, &op)
			if err != nil {
				t.Fatal(err)
			}
			if f.driver.builders[first] {
				t.Fatal("the unsettled build's builder was left behind")
			}
			ev := f.evidence(t, op.ID)
			row, _ := f.row(t, f.commits[0])
			switch settlement {
			case runtimedriver.Applied:
				if f.driver.buildCount() != 1 || row.Build != first || row.Ref != image || *ev.EnvironmentBuilder != (control.CreationBuilderState{Cycle: 1, State: "absent"}) {
					t.Fatalf("applied build not accepted: %d builds, row %+v, %+v", f.driver.buildCount(), row, ev.EnvironmentBuilder)
				}
			default:
				second := control.BuildRef(op.ID, 2)
				if f.driver.buildCount() != 2 || f.driver.builds[1].Ref.ID != second || second == first || row.Build != second || *ev.EnvironmentBuilder != (control.CreationBuilderState{Cycle: 2, State: "absent"}) {
					t.Fatalf("unsettled build not followed by a new cycle: %d builds, row %+v, %+v", f.driver.buildCount(), row, ev.EnvironmentBuilder)
				}
			}
		})
	}
}

// A build settled as not accepted is never settled again: once its builder
// is deleted, the driver would answer Applied for an image whose publication
// it never verified.
func TestABuildSettledAsNotAcceptedIsNeverAcceptedLater(t *testing.T) {
	f := newImageFixture(t)
	f.driver.unknown = true
	op := f.create(t, "work", f.commits[0])
	if _, err := f.ensure(t, &op); !errors.Is(err, runtimedriver.ErrUnknownOutcome) {
		t.Fatalf("unknown outcome: %v", err)
	}
	first := control.BuildRef(op.ID, 1)
	unverified := f.driver.projectImages("app")[0]
	// Retry settles the build as unknown, and P stops right after the
	// build's builder is deleted.
	f.driver.unknown, f.driver.settlement, f.driver.stopAfter = false, runtimedriver.Unknown, errors.New("P stopped")
	op = f.load(t, op.ID)
	if _, err := f.ensure(t, &op); err == nil {
		t.Fatal("the stop went unnoticed")
	}
	if b := f.evidence(t, op.ID).EnvironmentBuilder; *b != (control.CreationBuilderState{Cycle: 1, State: "abandoned"}) {
		t.Fatalf("leftovers deleted before the settlement was recorded: %+v", b)
	}
	// The image has the build's labels and no builder remains, which the
	// Incus driver settles as Applied.
	f.driver.stopAfter, f.driver.settlement, f.driver.settles, f.driver.deletes = nil, runtimedriver.Applied, 0, nil
	op = f.load(t, op.ID)
	image, err := f.ensure(t, &op)
	if err != nil {
		t.Fatal(err)
	}
	if f.driver.settles != 0 || !slices.Contains(f.driver.deletes, first) {
		t.Fatalf("recovery settled %d times and deleted leftovers of %v", f.driver.settles, f.driver.deletes)
	}
	row, _ := f.row(t, f.commits[0])
	ev := f.evidence(t, op.ID)
	second := control.BuildRef(op.ID, 2)
	if image == string(unverified.Ref) || row.Build != second || row.Ref != image || f.driver.buildCount() != 2 ||
		*ev.EnvironmentBuilder != (control.CreationBuilderState{Cycle: 2, State: "absent"}) {
		t.Fatalf("abandoned build accepted: image %s, row %+v, %d builds, %+v", image, row, f.driver.buildCount(), ev.EnvironmentBuilder)
	}
}

func TestADefiniteBuildFailureEndsTheCycleAbsent(t *testing.T) {
	f := newImageFixture(t)
	f.driver.fail = errors.New("devShell does not evaluate")
	op := f.create(t, "work", f.commits[0])
	if _, err := f.ensure(t, &op); control.OperationReason(err) != api.ReasonImageBuildFailed {
		t.Fatalf("failed build: %v, reason %s", err, control.OperationReason(err))
	}
	if b := f.evidence(t, op.ID).EnvironmentBuilder; *b != (control.CreationBuilderState{Cycle: 1, State: "absent"}) {
		t.Fatalf("definite failure left %+v", b)
	}
	f.driver.fail = nil
	op = f.load(t, op.ID)
	if _, err := f.ensure(t, &op); err != nil {
		t.Fatal(err)
	}
	if f.driver.buildCount() != 2 || f.driver.builds[1].Ref.ID != control.BuildRef(op.ID, 2) {
		t.Fatalf("Retry after a definite failure did not build in a new cycle: %d builds", f.driver.buildCount())
	}
}

// An export whose Git read fails answers failed, reason git: P's
// repository failed, the source is not missing. Either the captured commit
// is unreadable, or the only head that reaches it is.
func TestAnExportWhoseGitReadFailsAnswersFailedGit(t *testing.T) {
	for _, c := range []struct {
		name string
		// unreadable makes part of f's repository unreadable and returns
		// the commit to capture.
		unreadable func(t *testing.T, f *imageFixture) string
	}{
		{"the captured commit is unreadable", func(t *testing.T, f *imageFixture) string {
			corruptObject(t, f.repo, f.commits[1])
			return f.commits[1]
		}},
		{"the only head that reaches it is unreadable", func(t *testing.T, f *imageFixture) string {
			dispatchGit(t, f.repo, "update-ref", "-d", "refs/heads/main")
			corruptObject(t, f.repo, f.commits[1])
			return f.commits[0]
		}},
		{"the only head that reaches it is a malformed ref", func(t *testing.T, f *imageFixture) string {
			dispatchGit(t, f.repo, "update-ref", "-d", "refs/heads/main")
			if err := os.WriteFile(filepath.Join(f.repo, "refs", "heads", "next"), []byte("zzzz\n"), 0600); err != nil {
				t.Fatal(err)
			}
			return f.commits[0]
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newImageFixture(t)
			op := f.create(t, "work", c.unreadable(t, f))
			if _, err := f.ensure(t, &op); control.OperationReason(err) != api.ReasonGit {
				t.Fatalf("export: %v, reason %s", err, control.OperationReason(err))
			}
			if f.driver.buildCount() != 0 {
				t.Fatalf("built from an unreadable repository: %d builds", f.driver.buildCount())
			}
		})
	}
}

// corruptObject overwrites the loose object of id in repo with bytes Git
// cannot read.
func corruptObject(t *testing.T, repo, id string) {
	t.Helper()
	object := filepath.Join(repo, "objects", id[:2], id[2:])
	if err := os.Chmod(object, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(object, []byte("not a Git object"), 0600); err != nil {
		t.Fatal(err)
	}
}

// A captured commit no head reaches any more is a missing source, as a
// creation's source that names no such commit is.
func TestAnExportOfACommitNoHeadReachesAnswersTheSourceIsMissing(t *testing.T) {
	f := newImageFixture(t)
	loose := dispatchGit(t, f.repo, "-c", "user.name=P fixture", "-c", "user.email=p@example.test", "commit-tree", dispatchGit(t, f.repo, "mktree"), "-p", f.commits[1], "-m", "loose")
	op := f.create(t, "work", loose)
	_, err := f.ensure(t, &op)
	var refusal *control.Refusal
	if !errors.As(err, &refusal) || refusal.Kind != api.KindNotFound || refusal.Reason != api.ReasonSource {
		t.Fatalf("export of a commit no head reaches: %v", err)
	}
	if f.driver.buildCount() != 0 {
		t.Fatalf("built from a commit no head reaches: %d builds", f.driver.buildCount())
	}
}

func TestAnImageRemovedBeforeItsRuntimeIsCreatedIsRebuilt(t *testing.T) {
	f := newImageFixture(t)
	op := f.create(t, "work", f.commits[0])
	old, err := f.ensure(t, &op)
	if err != nil {
		t.Fatal(err)
	}
	op = f.load(t, op.ID)
	if err = f.l.store.AdvanceOperation(f.l.ctx, op.ID, "blocked", "principals-ready", true, op.Evidence, "internal", "fixture failure"); err != nil {
		t.Fatal(err)
	}
	delete(f.driver.images, runtimedriver.ImageRef(old))
	op = f.load(t, op.ID)
	image, err := f.ensure(t, &op)
	if err != nil {
		t.Fatal(err)
	}
	if image == old || f.driver.buildCount() != 2 {
		t.Fatalf("removed image %s not rebuilt: got %s", old, image)
	}
	if row, found := f.row(t, f.commits[0]); !found || row.Ref != image {
		t.Fatalf("index still names the removed image: %+v", row)
	}
	if ev := f.evidence(t, op.ID); ev.ImageFingerprint != image || ev.Image.Ref != image {
		t.Fatalf("creation kept the removed image: %+v", ev.Image)
	}
}

func TestReleaseDeletesALateBuildOnlyOnceNoCreationMayAcceptIt(t *testing.T) {
	f := newImageFixture(t)
	op := f.create(t, "work", f.commits[0])
	if _, err := f.ensure(t, &op); err != nil {
		t.Fatal(err)
	}
	// A build whose creation gave up on it published anyway.
	key := control.CommitImageKey{Project: "app", Commit: f.commits[0], BaseImage: f.l.cfg.BaseImageFingerprint, System: "x86_64-linux", BuilderVersion: "fake-builder/1"}
	late := runtimedriver.Image{Ref: runtimedriver.ImageRef(strings.Repeat("f", 64)), Labels: key.Labels(control.BuildRef(op.ID, 9)), Shell: f.driver.shell}
	f.driver.images[late.Ref] = late
	if err := f.l.releaseImages(f.l.ctx, "app"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := f.driver.InspectImage(f.l.ctx, late.Ref); !found {
		t.Fatal("late build deleted while a creation from its commit could still accept it")
	}
	f.setRegistry(t, op.SessionUUID, "established")
	if err := f.l.releaseImages(f.l.ctx, "app"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := f.driver.InspectImage(f.l.ctx, late.Ref); found {
		t.Fatal("late build kept after no creation could accept it")
	}
	if _, found := f.row(t, f.commits[0]); !found || len(f.driver.projectImages("app")) != 1 {
		t.Fatal("the indexed image of a commit in use went with the late build")
	}
}

func TestCommitLockSerializesOneCommitWithoutBlockingOthers(t *testing.T) {
	l := &lifecycle{}
	first, err := l.lockCommit(context.Background(), "team/a", "commit-one")
	if err != nil {
		t.Fatal(err)
	}
	other, err := l.lockCommit(context.Background(), "team/a", "commit-two")
	if err != nil {
		t.Fatal("another commit was blocked:", err)
	}
	other()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := l.lockCommit(ctx, "team/a", "commit-one"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("one commit was locked twice: %v", err)
	}
	if _, locked := l.tryLockCommit("team/a", "commit-one"); locked {
		t.Fatal("release took a held commit")
	}
	first()
	second, err := l.lockCommit(context.Background(), "team/a", "commit-one")
	if err != nil {
		t.Fatal(err)
	}
	second()
}
