package control

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/api"
)

// wantAnswer checks that err answers kind with reason and target.
func wantAnswer(t *testing.T, what string, err error, kind api.ErrorKind, reason, target string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: accepted, want %s %s", what, kind, reason)
	}
	got := answerError(context.Background(), err)
	want := api.NewError(kind, reason)
	want.Data.Target = target
	if reason == api.ReasonKeyReused {
		want.Data.Field = "key"
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: answered %v, want %v (cause %v)", what, got, want, err)
	}
}

func TestCreationRefusalsAnswerTheirKind(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	policy := json.RawMessage(`{"network":"none"}`)
	image := strings.Repeat("d", 64)
	first, err := s.BeginBlankProject(ctx, BlankProjectRequest{Key: "blank", Project: "team/app"}, policy, image, testSelection())
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.BeginBlankProject(ctx, BlankProjectRequest{Key: "blank", Project: "other"}, policy, image, testSelection())
	wantAnswer(t, "reused key", err, api.KindInvalid, api.ReasonKeyReused, "")

	_, err = s.BeginBlankProject(ctx, BlankProjectRequest{Key: "second", Project: "team/app"}, policy, image, testSelection())
	wantAnswer(t, "creation while another runs", err, api.KindRetry, api.ReasonProjectHeld, api.TargetOperation(first.ID))
	if err = s.AdvanceOperation(ctx, first.ID, "blocked", first.Phase, false, first.Evidence, "internal", "blocked for the test"); err != nil {
		t.Fatal(err)
	}
	_, err = s.BeginBlankProject(ctx, BlankProjectRequest{Key: "second", Project: "team/app"}, policy, image, testSelection())
	wantAnswer(t, "creation while another is blocked", err, api.KindConflict, api.ReasonOperationBlocked, api.TargetOperation(first.ID))

	if err = s.CreateProject(ctx, "team/existing", policy); err != nil {
		t.Fatal(err)
	}
	_, err = s.BeginBlankProject(ctx, BlankProjectRequest{Key: "third", Project: "team/existing"}, policy, image, testSelection())
	wantAnswer(t, "existing project", err, api.KindConflict, api.ReasonProjectExists, "project/team/existing")

	capture := func(context.Context, ReserveSessionRequest) (string, bool, error) {
		return strings.Repeat("a", 40), false, nil
	}
	work := func(key, project string) ReserveSessionRequest {
		return ReserveSessionRequest{Key: key, Project: project, Branch: "work", Choice: "new", Source: "refs/heads/main"}
	}
	_, _, err = s.BeginSessionCreate(ctx, work("s1", "team/missing"), image, testSelection(), capture)
	wantAnswer(t, "missing project", err, api.KindNotFound, api.ReasonProject, "project/team/missing")
	if _, _, err = s.BeginSessionCreate(ctx, work("s2", "team/existing"), image, testSelection(), capture); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.BeginSessionCreate(ctx, work("s3", "team/existing"), image, testSelection(), capture)
	wantAnswer(t, "assigned branch", err, api.KindConflict, api.ReasonBranchAssigned, "project/team/existing:branch/work")

	originOp, err := s.BeginOriginChange(ctx, OriginChange{Key: "origin-key", Project: "team/existing", Kind: "remove"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteOriginChange(ctx, originOp, nil); err != nil {
		t.Fatal(err)
	}
	_, err = s.BeginOriginChange(ctx, OriginChange{Key: "origin-key", Project: "team/existing", Kind: "set", ProposedURL: "ssh://host/other"})
	wantAnswer(t, "origin key", err, api.KindInvalid, api.ReasonKeyReused, "")
	_, _, err = s.BeginSessionCreate(ctx, work("origin-key", "team/existing"), image, testSelection(), capture)
	wantAnswer(t, "creation with an origin request's key", err, api.KindInvalid, api.ReasonKeyReused, "")
}
