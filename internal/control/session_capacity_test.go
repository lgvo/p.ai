package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestSessionAdmissionReservesHelperAndCountsRowlessProjectIntent(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	observed := []CapacityReservation{}
	observe := func(_ context.Context, reservations []CapacityReservation) (CapacityObservation, error) {
		observed = append([]CapacityReservation(nil), reservations...)
		return CapacityObservation{Limit: 4, PhysicalCount: 0, Occupied: map[string]bool{}}, nil
	}
	for i := 0; i < 3; i++ {
		req := BlankProjectRequest{Key: "capacity-" + string(rune('a'+i)), Project: "team/capacity-" + string(rune('a'+i)), URL: "ssh://example.invalid/repo"}
		if _, err := s.BeginBlankProjectWithCapacity(ctx, req, json.RawMessage(`{"network":"none"}`), strings.Repeat("d", 64), testSelection(), observe); err != nil {
			t.Fatalf("reservation %d: %v", i, err)
		}
		if len(observed) != i {
			t.Fatalf("reservation %d saw %d prior intents", i, len(observed))
		}
		for _, reservation := range observed {
			if reservation.Kind != "project.create" || !validUUID(reservation.SessionUUID) {
				t.Fatal("rowless project intent was not counted")
			}
		}
	}
	_, err := s.BeginBlankProjectWithCapacity(ctx, BlankProjectRequest{Key: "capacity-d", Project: "team/capacity-d"}, json.RawMessage(`{"network":"none"}`), strings.Repeat("d", 64), testSelection(), observe)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("fourth session consumed helper slot: %v", err)
	}
	var capacity *sessionCapacityError
	if !errors.As(err, &capacity) {
		t.Fatalf("admission refusal lost its capacity classification: %v", err)
	}
	rpcErr := lifecycleRPC(err)
	if rpcErr.Code != -32003 || rpcErr.Kind != "busy" || !strings.Contains(rpcErr.Message, "one slot for loss inspection") || !strings.Contains(rpcErr.Message, "Discard or Delete") || !strings.Contains(rpcErr.Message, "Stop retains") {
		t.Fatalf("capacity refusal did not explain the remedy: %+v", rpcErr)
	}
	var mutations int
	if err := s.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM operations WHERE idempotency_key='capacity-d') + (SELECT count(*) FROM projects WHERE path='team/capacity-d') + (SELECT count(*) FROM sessions WHERE project_path='team/capacity-d')`).Scan(&mutations); err != nil {
		t.Fatal(err)
	}
	if mutations != 0 {
		t.Fatal("capacity refusal accepted durable mutation")
	}
	if ordinary := lifecycleRPC(ErrConflict); ordinary.Message != "lifecycle request conflicts with current authority" {
		t.Fatalf("ordinary conflict gained a capacity diagnosis: %+v", ordinary)
	}
}

func TestSessionAdmissionCountsUnknownDurableSessionAndForeignOccupant(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if _, err := s.BeginBlankProject(ctx, BlankProjectRequest{Key: "legacy", Project: "team/legacy"}, json.RawMessage(`{"network":"none"}`), strings.Repeat("d", 64), testSelection()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO projects(path,registry_state,policy_json,policy_sha256) VALUES('team/orphan','active','{}',?)`, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO sessions(uuid,project_path,branch,registry_state,policy_json,policy_sha256) VALUES(?, 'team/orphan','main','established','{}',?)`, "12345678-1234-1234-1234-123456789abc", strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	observe := func(_ context.Context, reservations []CapacityReservation) (CapacityObservation, error) {
		if len(reservations) != 2 {
			t.Fatalf("lost an unknown durable session: %+v", reservations)
		}
		return CapacityObservation{Limit: 4, PhysicalCount: 1, Occupied: map[string]bool{}}, nil
	}
	if _, err := s.BeginBlankProjectWithCapacity(ctx, BlankProjectRequest{Key: "next", Project: "team/next"}, json.RawMessage(`{"network":"none"}`), strings.Repeat("d", 64), testSelection(), observe); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign physical occupant plus unknown reservations admitted: %v", err)
	}
}
