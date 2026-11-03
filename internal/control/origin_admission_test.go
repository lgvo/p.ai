package control

import (
	"context"
	"testing"

	"github.com/lgvo/p.ai/internal/api"
)

// TestOriginOperationsRecheckTheProjectWhenTheyRecord: a project deleted, or
// being deleted, between a request's checks and its recording gets no
// operation.
func TestOriginOperationsRecheckTheProjectWhenTheyRecord(t *testing.T) {
	for _, test := range []struct {
		name, change string
		kind         api.ErrorKind
	}{
		{"being deleted", `UPDATE projects SET registry_state='deleting' WHERE path='app'`, api.KindConflict},
		{"deleted", `DELETE FROM projects WHERE path='app'`, api.KindNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, _, _, r := publicationFixture(t)
			ctx := context.Background()
			mustExec(t, s, `DELETE FROM sessions`)
			mustExec(t, s, `DELETE FROM project_origins`)
			r.Kind, r.Source = "retained", "saved"
			_, err := s.BeginPublication(ctx, r, testHash, func(context.Context) (string, error) {
				mustExec(t, s, test.change)
				return "saved", nil
			})
			if a := answerError(ctx, err); a == nil || a.Kind() != test.kind {
				t.Fatalf("answered %v, want %s", err, test.kind)
			}
			var n int
			if err = s.db.QueryRow(`SELECT count(*) FROM operations WHERE idempotency_key=?`, r.Key).Scan(&n); err != nil || n != 0 {
				t.Fatalf("recorded %d operations (%v)", n, err)
			}
		})
	}
}

// TestIdenticalRequestRecordedMeanwhileIsReturned: two identical
// publications race; the one that records second returns the first one's
// operation instead of refusing its key.
func TestIdenticalRequestRecordedMeanwhileIsReturned(t *testing.T) {
	s, _, _, r := publicationFixture(t)
	ctx := context.Background()
	var first api.Operation
	second, err := s.BeginPublication(ctx, r, testHash, func(ctx context.Context) (string, error) {
		var e error
		first, e = s.BeginPublication(ctx, r, testHash, func(context.Context) (string, error) { return "work", nil })
		return "work", e
	})
	if err != nil || first.ID == "" || second.ID != first.ID {
		t.Fatalf("second %+v first %+v %v", second, first, err)
	}
}

// TestKeyRecordedMeanwhileForAnotherRequestIsReused: a publication whose key
// another request recorded while it was admitted answers key_reused, not the
// session_held that other operation now causes.
func TestKeyRecordedMeanwhileForAnotherRequestIsReused(t *testing.T) {
	s, _, _, r := publicationFixture(t)
	ctx := context.Background()
	other := r
	other.DestinationRef = "refs/heads/other"
	_, err := s.BeginPublication(ctx, r, testHash, func(ctx context.Context) (string, error) {
		if _, e := s.BeginPublication(ctx, other, testHash, func(context.Context) (string, error) { return "work", nil }); e != nil {
			t.Fatal(e)
		}
		return "", Retry(api.ReasonSessionHeld, "")
	})
	if a := answerError(ctx, err); a == nil || a.Kind() != api.KindInvalid || a.Reason() != api.ReasonKeyReused {
		t.Fatalf("answered %v", err)
	}
}
