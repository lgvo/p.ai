package daemon

import (
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimedriver"
)

func TestImagesGoAfterAnOperationOfTheProjectAndAtDaemonStart(t *testing.T) {
	for _, trigger := range []string{"operation", "start"} {
		t.Run(trigger, func(t *testing.T) {
			f := newImageFixture(t)
			op := f.create(t, "work", f.commits[0])
			if _, err := f.ensure(t, &op); err != nil {
				t.Fatal(err)
			}
			f.setRegistry(t, op.SessionUUID, "gone")
			op = f.load(t, op.ID)
			if err := f.l.store.AdvanceOperation(f.l.ctx, op.ID, "completed", "established", true, op.Evidence, "", ""); err != nil {
				t.Fatal(err)
			}
			f.l.working, f.l.queueSlots = map[string]bool{}, make(chan struct{}, 8)
			var err error
			if trigger == "operation" {
				err = f.l.enqueue(op.ID)
			} else {
				err = f.l.Recover()
			}
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(10 * time.Second)
			for len(f.driver.projectImages("app")) != 0 {
				if time.Now().After(deadline) {
					t.Fatal("unused image kept")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if _, found := f.row(t, f.commits[0]); found {
				t.Fatal("deleted image still indexed")
			}
		})
	}
}

func TestProjectDeletionDeletesEveryReviewedImageOfTheProject(t *testing.T) {
	f := newImageFixture(t)
	for i, commit := range f.commits {
		op := f.create(t, []string{"first", "second"}[i], commit)
		if _, err := f.ensure(t, &op); err != nil {
			t.Fatal(err)
		}
	}
	rows, more, err := f.l.store.ListCommitImages(f.l.ctx, "app", control.CommitImageKey{}, control.MaxProjectDeleteImages)
	if err != nil || more || len(rows) != 2 {
		t.Fatalf("project rows: %d %v %v", len(rows), more, err)
	}
	var claims []control.CommitImageClaim
	for _, row := range rows {
		claim, err := f.l.reviewCommitImage(f.l.ctx, row)
		if err != nil || !claim.ImagePresent || claim.ObservedSize != 1<<30 || len(claim.RelatedDigest) != 64 {
			t.Fatalf("review %+v: %v", claim, err)
		}
		claims = append(claims, claim)
	}
	for _, claim := range claims {
		resource := control.ProjectDeleteResource{Kind: "image", ID: claim.Image.Ref, Status: "remaining"}
		persisted := 0
		if err = f.l.deleteProjectImage(f.l.ctx, claim, &resource, func() error { persisted++; return nil }); err != nil {
			t.Fatal(err)
		}
		if !resource.DeleteIssued || persisted != 1 {
			t.Fatalf("delete not recorded as issued before it started: %+v", resource)
		}
		// A retry after the delete finds nothing left to do.
		if err = f.l.deleteProjectImage(f.l.ctx, claim, &resource, func() error { t.Fatal("deleted twice"); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if images := f.driver.projectImages("app"); len(images) != 0 {
		t.Fatalf("project images remain: %+v", images)
	}
	if rows, _, _ := f.l.store.ListCommitImages(f.l.ctx, "app", control.CommitImageKey{}, control.MaxProjectDeleteImages); len(rows) != 0 {
		t.Fatalf("project rows remain: %+v", rows)
	}
}

func TestProjectDeletionRefusesAnImageItDidNotReview(t *testing.T) {
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
	for name, change := range map[string]func(*control.CommitImageClaim, *control.ProjectDeleteResource){
		"reviewed absent":  func(c *control.CommitImageClaim, _ *control.ProjectDeleteResource) { c.ImagePresent = false },
		"confirmed absent": func(_ *control.CommitImageClaim, r *control.ProjectDeleteResource) { r.Status = "already_absent" },
		"another row":      func(c *control.CommitImageClaim, _ *control.ProjectDeleteResource) { c.Image.Size++ },
	} {
		t.Run(name, func(t *testing.T) {
			c, resource := claim, control.ProjectDeleteResource{Kind: "image", ID: claim.Image.Ref, Status: "remaining"}
			change(&c, &resource)
			if err := f.l.deleteProjectImage(f.l.ctx, c, &resource, func() error { return nil }); control.OperationReason(err) != api.ReasonChanged {
				t.Fatalf("unreviewed image: %v, reason %s", err, control.OperationReason(err))
			}
			if _, found, _ := f.driver.InspectImage(f.l.ctx, runtimedriver.ImageRef(claim.Image.Ref)); !found {
				t.Fatal("image deleted")
			}
		})
	}
}
