package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// unreadableImages is an image driver that cannot read its images.
type unreadableImages struct{ *fakeImages }

func (unreadableImages) Images(context.Context) ([]runtimedriver.Image, error) {
	return nil, errors.New("Incus did not answer")
}

func (unreadableImages) InspectImage(context.Context, runtimedriver.ImageRef) (runtimedriver.Image, bool, error) {
	return runtimedriver.Image{}, false, errors.New("Incus did not answer")
}

// panickingImages is an image driver whose image list panics.
type panickingImages struct{ *fakeImages }

func (panickingImages) Images(context.Context) ([]runtimedriver.Image, error) {
	panic("image list panicked")
}

func TestACreationWhoseImageBuilderChangedBlocksWithItsReason(t *testing.T) {
	f := newImageFixture(t)
	op := f.create(t, "work", f.commits[0])
	f.l.cfg.Environment.BuilderStoragePool = "other-builders"
	_, err := f.ensure(t, &op)
	if control.OperationReason(err) != api.ReasonImageBuilderChanged {
		t.Fatalf("changed builder selection: %v, reason %s", err, control.OperationReason(err))
	}
	if f.driver.buildCount() != 0 {
		t.Fatal("a build ran under a changed builder selection")
	}
}

func TestAnImageTheDriverCannotReadIsARuntimeFailure(t *testing.T) {
	f := newImageFixture(t)
	first := f.create(t, "first", f.commits[0])
	if _, err := f.ensure(t, &first); err != nil {
		t.Fatal(err)
	}
	row, _ := f.row(t, f.commits[0])
	f.l.driver = unreadableImages{f.driver}
	second := f.create(t, "second", f.commits[0])
	if _, err := f.ensure(t, &second); control.OperationReason(err) != api.ReasonRuntime {
		t.Fatalf("reuse of an unreadable image: %v, reason %s", err, control.OperationReason(err))
	}
	claim, err := f.l.reviewCommitImage(f.l.ctx, row)
	var refusal *control.Refusal
	if !errors.As(err, &refusal) || refusal.Kind != api.KindFailed || refusal.Reason != api.ReasonRuntime {
		t.Fatalf("review of an unreadable image: %+v, %v", claim, err)
	}
}

func TestProjectDeletionOfAnImageTheDriverCannotReadIsARuntimeFailure(t *testing.T) {
	f := newImageFixture(t)
	op := f.create(t, "work", f.commits[0])
	if _, err := f.ensure(t, &op); err != nil {
		t.Fatal(err)
	}
	row, _ := f.row(t, f.commits[0])
	claim, err := f.l.reviewCommitImage(f.l.ctx, row)
	if err != nil {
		t.Fatal(err)
	}
	resource := control.ProjectDeleteResource{Kind: "image", ID: claim.Image.Ref, Status: "remaining"}
	f.l.driver = unreadableImages{f.driver}
	if err = f.l.deleteProjectImage(f.l.ctx, claim, &resource, func() error { return nil }); control.OperationReason(err) != api.ReasonRuntime {
		t.Fatalf("unreadable image: %v, reason %s", err, control.OperationReason(err))
	}
}

func TestAPanicInImageReleaseLeavesLaterReleasesRunning(t *testing.T) {
	f := newImageFixture(t)
	op := f.create(t, "work", f.commits[0])
	if _, err := f.ensure(t, &op); err != nil {
		t.Fatal(err)
	}
	f.setRegistry(t, op.SessionUUID, "gone")
	released := func() bool {
		running := true
		f.l.withLock(func() { _, running = f.l.releases["app"] })
		return !running
	}
	wait := func(done func() bool, what string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !done() {
			if time.Now().After(deadline) {
				t.Fatal(what)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	f.l.driver = panickingImages{f.driver}
	f.l.startRelease("app")
	wait(released, "the panicked release never ended")
	f.l.driver = f.driver
	f.l.startRelease("app")
	wait(func() bool { return len(f.driver.projectImages("app")) == 0 }, "a release after the panic kept the unused image")
}

func TestAProjectDeletionPlanIsRefusedWhenItsEvidenceCannotBeRecorded(t *testing.T) {
	project := strings.Repeat("p", 100) + "/" + strings.Repeat("q", 100) + "/" + strings.Repeat("r", 53)
	claims := func(n int) []control.CommitImageClaim {
		var all []control.CommitImageClaim
		for i := range n {
			key := control.CommitImageKey{Project: project, Commit: fmt.Sprintf("%064d", i), BaseImage: strings.Repeat("b", 64), System: "x86_64-linux", BuilderVersion: strings.Repeat("v", 128)}
			build := "550e8400-e29b-41d4-a716-446655440000"
			all = append(all, control.CommitImageClaim{Image: control.CommitImage{Key: key, Ref: fmt.Sprintf("%064x", i), Build: build, Labels: key.Labels(build),
				Selection: "devshell", MaterialSHA256: strings.Repeat("d", 64), Size: 1 << 30, State: "ready"}, ImagePresent: true, ObservedSize: 1 << 30, RelatedDigest: strings.Repeat("c", 64)})
		}
		return all
	}
	l := &lifecycle{instanceID: "11111111-1111-4111-8111-111111111111", cfg: control.RuntimeConfig{BaseImageFingerprint: strings.Repeat("a", 64)}}
	review := func(images []control.CommitImageClaim) error {
		return l.boundProjectDeleteEvidence(api.ProjectDeleteDetails{Project: project, Images: control.CommitImageClaimViews(images)}, images)
	}
	if err := review(claims(1)); err != nil {
		t.Fatalf("a review of one image refused: %v", err)
	}
	// Twenty images fit a response frame, but not the evidence the deletion
	// would run with.
	err := review(claims(control.MaxProjectDeleteImages))
	var refusal *control.Refusal
	if !errors.As(err, &refusal) || refusal.Kind != api.KindConflict || refusal.Reason != api.ReasonBoundExceeded || refusal.Target != api.TargetProject(project) {
		t.Fatalf("a review whose evidence cannot be recorded: %v", err)
	}
}
