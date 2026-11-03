package control

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
)

// TestRemovalAdmissionRefusalsNameTheirNextStep checks the answers a stale or
// mismatched removal confirmation gets from the store's admission.
func TestRemovalAdmissionRefusalsNameTheirNextStep(t *testing.T) {
	ctx := context.Background()
	s, ev, first := setupDiscardStore(t)
	if err := s.FailStaleDiscard(ctx, first.ID, "first review stale"); err != nil {
		t.Fatal(err)
	}
	session := "550e8400-e29b-41d4-a716-446655440000"
	request := func(key string) DiscardRequest {
		return DiscardRequest{Key: key, UUID: session, TokenSHA256: strings.Repeat("9", 64)}
	}
	expired := ev
	expired.PreviewExpiresAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	changed := ev
	changed.PolicySHA256 = strings.Repeat("e", 64)
	for _, test := range []struct {
		name   string
		req    DiscardRequest
		ev     DiscardEvidence
		kind   api.ErrorKind
		reason string
		target string
	}{
		{"expired review", request("expired"), expired, api.KindPreconditionFailed, api.ReasonExpired, ""},
		{"changed policy", request("changed"), changed, api.KindPreconditionFailed, api.ReasonChanged, ""},
		{"key of another request", DiscardRequest{Key: "discard-1", UUID: session, TokenSHA256: strings.Repeat("8", 64)}, ev, api.KindInvalid, api.ReasonKeyReused, ""},
		{"missing session", DiscardRequest{Key: "missing", UUID: "660e8400-e29b-41d4-a716-446655440000", TokenSHA256: strings.Repeat("9", 64)}, ev,
			api.KindNotFound, api.ReasonSession, "session/660e8400-e29b-41d4-a716-446655440000"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := s.BeginDiscard(ctx, test.req, test.ev)
			answer := answerError(ctx, err)
			if answer.Kind() != test.kind || answer.Reason() != test.reason || answer.Target() != test.target {
				t.Fatalf("answered %v (%v), want %s %s %s", answer, err, test.kind, test.reason, test.target)
			}
		})
	}
}

// TestProjectDeleteRefusalsNameTheirNextStep checks the answers of a project
// deletion that the project's state or a stale review refuses.
func TestProjectDeleteRefusalsNameTheirNextStep(t *testing.T) {
	ctx := context.Background()
	answer := func(err error) *api.Error { return answerError(ctx, err) }
	t.Run("missing project", func(t *testing.T) {
		s, _ := openTestStore(t)
		_, _, err := s.ProjectDeleteSnapshot(ctx, "absent")
		if a := answer(err); a.Kind() != api.KindNotFound || a.Reason() != api.ReasonProject || a.Target() != "project/absent" {
			t.Fatalf("missing project answered %v (%v)", a, err)
		}
	})
	t.Run("an unfinished operation holds the project", func(t *testing.T) {
		s, _, _, _ := projectDeleteFixture(t)
		op, _, err := s.ReserveSession(ctx, ReserveSessionRequest{Key: "create-late", Project: "bulk", Branch: "late", Choice: "new", Source: "refs/heads/seed"})
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = s.ProjectDeleteSnapshot(ctx, "bulk")
		if a := answer(err); a.Kind() != api.KindRetry || a.Reason() != api.ReasonProjectHeld || a.Target() != "operation/"+op.ID {
			t.Fatalf("running operation answered %v (%v)", a, err)
		}
		if err = s.AdvanceOperation(ctx, op.ID, "blocked", op.Phase, false, op.Evidence, "blocked"); err != nil {
			t.Fatal(err)
		}
		_, _, err = s.ProjectDeleteSnapshot(ctx, "bulk")
		if a := answer(err); a.Kind() != api.KindConflict || a.Reason() != api.ReasonOperationBlocked || a.Target() != "operation/"+op.ID {
			t.Fatalf("blocked operation answered %v (%v)", a, err)
		}
	})
	t.Run("a session that is not established", func(t *testing.T) {
		s, _, _, ev := projectDeleteFixture(t)
		uuid := ev.Sessions[0].Session.UUID
		if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET registry_state='removing' WHERE uuid=?`, uuid); err != nil {
			t.Fatal(err)
		}
		_, _, err := s.ProjectDeleteSnapshot(ctx, "bulk")
		if a := answer(err); a.Kind() != api.KindConflict || a.Reason() != api.ReasonSessionNotEstablished || a.Target() != "session/"+uuid {
			t.Fatalf("removing session answered %v (%v)", a, err)
		}
	})
	t.Run("a project being deleted", func(t *testing.T) {
		s, _, req, ev := projectDeleteFixture(t)
		acceptProjectDelete(t, s, req, ev)
		_, _, err := s.ProjectDeleteSnapshot(ctx, "bulk")
		if a := answer(err); a.Kind() != api.KindConflict || a.Reason() != api.ReasonProjectDeleting || a.Target() != "project/bulk" {
			t.Fatalf("deleting project answered %v (%v)", a, err)
		}
	})
	t.Run("stale and reused confirmations", func(t *testing.T) {
		s, _, req, ev := projectDeleteFixture(t)
		begin := func(r ProjectDeleteRequest, e ProjectDeleteEvidence, expires time.Time) error {
			_, err := s.BeginProjectDelete(ctx, r, e, expires.UTC().Format(time.RFC3339Nano), func(context.Context) error { return nil })
			return err
		}
		if a := answer(begin(req, ev, time.Now().Add(-time.Second))); a.Kind() != api.KindPreconditionFailed || a.Reason() != api.ReasonExpired {
			t.Fatalf("expired review answered %v", a)
		}
		changed := ev
		changed.StateSHA256 = strings.Repeat("0", 64)
		if a := answer(begin(req, changed, time.Now().Add(time.Minute))); a.Kind() != api.KindPreconditionFailed || a.Reason() != api.ReasonChanged {
			t.Fatalf("changed review answered %v", a)
		}
		acceptProjectDelete(t, s, req, ev)
		reused := req
		reused.TokenSHA256 = strings.Repeat("b", 64)
		if a := answer(begin(reused, ev, time.Now().Add(time.Minute))); a.Kind() != api.KindInvalid || a.Reason() != api.ReasonKeyReused {
			t.Fatalf("reused key answered %v", a)
		}
	})
}

// TestRemovalAdmissionNamesTheOperationHoldingTheSession checks that a second
// removal of a session whose first removal is unfinished names that
// operation: retry while it runs, conflict once it is blocked.
func TestRemovalAdmissionNamesTheOperationHoldingTheSession(t *testing.T) {
	ctx := context.Background()
	s, ev, first := setupDiscardStore(t)
	second := DiscardRequest{Key: "discard-second", UUID: "550e8400-e29b-41d4-a716-446655440000", TokenSHA256: strings.Repeat("7", 64)}
	_, err := s.BeginDiscard(ctx, second, ev)
	if a := answerError(ctx, err); a.Kind() != api.KindRetry || a.Reason() != api.ReasonSessionHeld || a.Target() != "operation/"+first.ID {
		t.Fatalf("a running holder answered %v (%v)", a, err)
	}
	if err = s.AdvanceOperation(ctx, first.ID, "blocked", first.Phase, first.Committed, first.Evidence, "blocked"); err != nil {
		t.Fatal(err)
	}
	_, err = s.BeginDiscard(ctx, second, ev)
	if a := answerError(ctx, err); a.Kind() != api.KindConflict || a.Reason() != api.ReasonOperationBlocked || a.Target() != "operation/"+first.ID {
		t.Fatalf("a blocked holder answered %v (%v)", a, err)
	}
}

// TestConstraintFailuresAnswerTheirRefusal checks classifyWrite against the
// text SQLite gives for the unique indexes on operations.
func TestConstraintFailuresAnswerTheirRefusal(t *testing.T) {
	ctx := context.Background()
	s, _, first := setupDiscardStore(t)
	rec := operationRecord{ID: "op-collide", Key: "collide", Kind: "session.delete", Project: first.Project, Session: first.SessionUUID,
		Request: []byte(`{}`), Phase: "guard-pending", Now: time.Now().UTC().Format(time.RFC3339Nano)}
	err := classifyWrite(insertOperation(ctx, s.db, rec))
	if a := answerError(ctx, err); a.Kind() != api.KindRetry || a.Reason() != api.ReasonSessionHeld {
		t.Fatalf("a second active operation on the session answered %v (%v)", a, err)
	}
}
