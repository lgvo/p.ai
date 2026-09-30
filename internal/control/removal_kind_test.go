package control

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSessionRemovalKindUsesCommittedCurrentAuthority(t *testing.T) {
	for _, kind := range []string{"session.discard", "session.delete", "session.create.cleanup", "session.record.repair", "project.delete"} {
		for _, status := range []string{"running", "blocked", "unknown"} {
			t.Run(kind+"/"+status, func(t *testing.T) {
				s, _ := openTestStore(t)
				ctx := context.Background()
				if err := s.CreateProject(ctx, "app", []byte(`{}`)); err != nil {
					t.Fatal(err)
				}
				_, session, err := s.BeginSessionCreate(ctx, ReserveSessionRequest{Key: "create", Project: "app", Branch: "work", Choice: "existing"}, strings.Repeat("d", 64), testSelection(), func(context.Context, ReserveSessionRequest) (string, bool, error) {
					return strings.Repeat("a", 40), true, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				create, err := s.CreationForSession(ctx, session.UUID)
				if err != nil {
					t.Fatal(err)
				}
				if err = s.CompleteCreation(ctx, create.ID); err != nil {
					t.Fatal(err)
				}
				insertRemovalKind(t, s, session.UUID, kind, status, true, "active")
				if err = s.AdvanceSessionRegistry(ctx, session.UUID, "established", "removing"); err != nil {
					t.Fatal(err)
				}
				if kind == "project.delete" {
					if _, err = s.db.ExecContext(ctx, `UPDATE projects SET registry_state='deleting' WHERE path='app'`); err != nil {
						t.Fatal(err)
					}
				}
				got, err := s.SessionRemovalKind(ctx, session.UUID)
				if err != nil || got != kind {
					t.Fatalf("intent=%s want%s err%v", got, kind, err)
				}
			})
		}
	}
}
func insertRemovalKind(t *testing.T, s *Store, uuid, kind, status string, committed bool, key string) {
	t.Helper()
	var id any = uuid
	if kind == "project.delete" {
		id = nil
	}
	_, err := s.db.ExecContext(context.Background(), `INSERT INTO operations(id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,status,phase,committed,evidence_json,created_at,updated_at) VALUES(?,?,?,'app',?,'{}',?,?,'fixture',?,'{}','now','now')`, key, key, kind, id, strings.Repeat("a", 64), status, committed)
	if err != nil {
		t.Fatal(err)
	}
}
func TestSessionRemovalKindRefusesDormantAndUnavailableAuthority(t *testing.T) {
	for i, state := range []string{"completed", "uncommitted", "missing", "ambiguous", "project-precedence", "project-owner-missing"} {
		t.Run(state, func(t *testing.T) {
			s, _ := openTestStore(t)
			ctx := context.Background()
			if err := s.CreateProject(ctx, "app", []byte(`{}`)); err != nil {
				t.Fatal(err)
			}
			uuid := fmt.Sprintf("550e8400-e29b-41d4-a716-%012d", i)
			if _, err := s.db.ExecContext(ctx, `INSERT INTO sessions(uuid,project_path,branch,registry_state,policy_json,policy_sha256) VALUES(?,'app','work','established','{}',?)`, uuid, strings.Repeat("a", 64)); err != nil {
				t.Fatal(err)
			}
			switch state {
			case "completed":
				insertRemovalKind(t, s, uuid, "session.discard", "completed", true, "old")
			case "uncommitted":
				insertRemovalKind(t, s, uuid, "session.discard", "running", false, "pending")
			case "ambiguous":
				// Exercise refusal against inconsistent journal state normally prevented
				// by the active-operation uniqueness constraint.
				if _, err := s.db.ExecContext(ctx, `DROP INDEX active_session_operation`); err != nil {
					t.Fatal(err)
				}
				insertRemovalKind(t, s, uuid, "session.discard", "blocked", true, "one")
				insertRemovalKind(t, s, uuid, "session.delete", "running", true, "two")
			case "project-owner-missing":
				insertRemovalKind(t, s, uuid, "session.discard", "blocked", true, "subordinate")
				if _, err := s.db.ExecContext(ctx, `UPDATE projects SET registry_state='deleting' WHERE path='app'`); err != nil {
					t.Fatal(err)
				}
			case "project-precedence":
				insertRemovalKind(t, s, uuid, "session.discard", "blocked", true, "subordinate")
				insertRemovalKind(t, s, uuid, "project.delete", "running", true, "project")
				if _, err := s.db.ExecContext(ctx, `UPDATE projects SET registry_state='deleting' WHERE path='app'`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET registry_state='removing' WHERE uuid=?`, uuid); err != nil {
				t.Fatal(err)
			}
			got, err := s.SessionRemovalKind(ctx, uuid)
			if state == "project-precedence" {
				if err != nil || got != "project.delete" {
					t.Fatalf("project authority not selected: %s %v", got, err)
				}
			} else if state == "ambiguous" {
				if !errors.Is(err, ErrConflict) {
					t.Fatalf("ambiguous intent accepted: %s %v", got, err)
				}
			} else if !errors.Is(err, ErrNotFound) {
				t.Fatalf("missing committed intent inferred: %s %v", got, err)
			}
		})
	}
}
