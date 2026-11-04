package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	sourcememory "github.com/lgvo/p.ai/internal/sourcedriver/memory"
)

// retainedNamesFixture is a lifecycle over a real store with project app and
// an in-memory source driver. Its context is done, so an accepted operation
// is recorded but never processed.
func retainedNamesFixture(t *testing.T) (*lifecycle, *control.Store, *sourcememory.Driver) {
	t.Helper()
	store, err := control.OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err = store.CreateProject(context.Background(), "app", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	_, driver := memorySource(t)
	done, cancel := context.WithCancel(context.Background())
	cancel()
	l := &lifecycle{ctx: done, store: store, git: &gitCapability{driver: driver}, instanceID: "11111111-1111-4111-8111-111111111111"}
	return l, store, driver
}

// wantInvalidField fails the test unless err answers invalid naming field.
func wantInvalidField(t *testing.T, what string, err error, field string) {
	t.Helper()
	var refusal *control.Refusal
	if !errors.As(err, &refusal) || refusal.Kind != api.KindInvalid || refusal.Field != field {
		t.Fatalf("%s answered %v, want invalid %s", what, err, field)
	}
}

// wantNothingRecorded fails the test if key recorded an operation.
func wantNothingRecorded(t *testing.T, store *control.Store, what, key string) {
	t.Helper()
	if _, err := store.GetOperationByKey(context.Background(), key); !errors.Is(err, control.ErrNotFound) {
		t.Fatalf("%s recorded its key: %v", what, err)
	}
}

func TestRetainedRenameRefusesNamesTheSourceDriverRefusesBeforeRecording(t *testing.T) {
	l, store, _ := retainedNamesFixture(t)
	base := control.RetainedRenameRequest{Project: "app", OldBranch: "saved", NewBranch: "archive", ExpectedOldTip: strings.Repeat("a", 40)}
	for _, test := range []struct {
		field  string
		change func(*control.RetainedRenameRequest)
	}{
		{"old_branch", func(r *control.RetainedRenameRequest) { r.OldBranch = "-saved" }},
		{"new_branch", func(r *control.RetainedRenameRequest) { r.NewBranch = "HEAD" }},
		{"expected_old_tip", func(r *control.RetainedRenameRequest) { r.ExpectedOldTip = strings.Repeat("0", 40) }},
	} {
		r := base
		r.Key = test.field
		test.change(&r)
		_, err := l.RenameRetainedBranch(context.Background(), r)
		wantInvalidField(t, "a rename with an invalid "+test.field, err, test.field)
		wantNothingRecorded(t, store, "a rename with an invalid "+test.field, r.Key)
	}
}

func TestRetainedRenameReplaysARecordedRequestWhileTheSourceDriverIsDown(t *testing.T) {
	l, store, driver := retainedNamesFixture(t)
	ctx := context.Background()
	r := control.RetainedRenameRequest{Key: "rename", Project: "app", OldBranch: "saved", NewBranch: "archive", ExpectedOldTip: strings.Repeat("a", 40)}
	recorded, err := store.BeginRetainedRename(ctx, r, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	driver.Kill()
	if op, err := l.RenameRetainedBranch(ctx, r); err != nil || op.ID != recorded.ID {
		t.Fatalf("the replay with the source driver down answered %+v %v, want operation %s", op, err, recorded.ID)
	}
}

func TestRetainedDeleteRefusesANameTheSourceDriverRefusesBeforeRecording(t *testing.T) {
	l, store, _ := retainedNamesFixture(t)
	for _, req := range []api.ChangeRequest{{}, {Key: "delete", IfMatch: futureTag(time.Hour)}} {
		_, err := l.DeleteRetainedBranch(context.Background(), "app", "-saved", req)
		wantInvalidField(t, "a deletion of an invalid branch", err, "branch")
	}
	wantNothingRecorded(t, store, "a deletion of an invalid branch", "delete")
}

func TestRetainedDeleteReplaysARecordedRunWhileTheSourceDriverIsDown(t *testing.T) {
	l, store, driver := retainedNamesFixture(t)
	ctx := context.Background()
	recorded := control.RetainedDeleteRequest{Key: "delete", Project: "app", Branch: "saved", IfMatch: futureTag(time.Hour)}
	ev := control.RetainedDeleteEvidence{Project: "app", Branch: "saved", Tip: strings.Repeat("a", 40), ReviewSHA256: strings.Repeat("b", 64), InstanceUUID: l.instanceID}
	op, err := store.BeginRetainedDelete(ctx, recorded, ev, strings.Repeat("c", 64), func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	driver.Kill()
	result, err := l.DeleteRetainedBranch(ctx, "app", "saved", api.ChangeRequest{Key: recorded.Key, IfMatch: recorded.IfMatch})
	if err != nil || result.Operation == nil || result.Operation.ID != op.ID {
		t.Fatalf("the replay with the source driver down answered %+v %v, want operation %s", result, err, op.ID)
	}
}
