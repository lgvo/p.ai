package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
)

// These tests state the shared operation-journal rules: which unfinished
// operations recovery lists and in which order, and how one
// idempotency key namespace answers a request of another kind or another
// request of the same kind.

var journalKinds = []string{
	"project.create", "session.create", "environment.collect", "project.delete",
	"workspace.inspect", "workspace.loss.inspect",
	"session.discard", "session.delete", "session.create.cleanup",
	"session.rename", "project.retained.rename", "project.retained.delete",
	"session.repair", "session.repair.prepare",
	"session.ref.repair", "session.principal.repair", "session.record.repair",
}

// Each kind gets one row per status. Rows are inserted A..F, so rowid order is
// A, B, C while created_at order is B, C, A.
var journalRows = []struct{ name, status string }{
	{"A", "running"}, {"B", "blocked"}, {"C", "unknown"},
	{"D", "failed"}, {"E", "completed"}, {"F", "superseded"},
}
var journalCreated = map[string]int{"A": 3, "B": 1, "C": 2, "D": 4, "E": 5, "F": 6}

func dropOperationInsertTriggers(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='trigger' AND tbl_name='operations' AND sql LIKE '%BEFORE INSERT%'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	rows.Close()
	for _, name := range names {
		if _, err = s.db.ExecContext(ctx, `DROP TRIGGER `+name); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUnfinishedOperationsSelectKindsStatusesAndOrder(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "team/app", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	// Rows of every kind without their sessions, sources or reviews.
	dropOperationInsertTriggers(t, s)
	for i, kind := range journalKinds {
		for _, row := range journalRows {
			created := fmt.Sprintf("2026-01-01T00:%02d:%02dZ", i, journalCreated[row.name])
			// Collections and project deletions allow one active row per project.
			project := "team/" + strings.ReplaceAll(kind, ".", "-") + "-" + row.name
			if _, err := s.db.ExecContext(ctx, `INSERT INTO operations(id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,status,phase,committed,evidence_json,created_at,updated_at)
			 VALUES(?,?,?,?,NULL,'{}','x',?,'forged',0,NULL,?,?)`, kind+":"+row.name, kind+":"+row.name, kind, project, row.status, created, created); err != nil {
				t.Fatal(err)
			}
		}
	}
	byCreated := func(kinds ...string) []string {
		var ids []string
		for _, kind := range kinds {
			ids = append(ids, kind+":B", kind+":C", kind+":A")
		}
		return ids
	}
	byRowid := func(kind string) []string { return []string{kind + ":A", kind + ":B", kind + ":C"} }
	// Recovery order: creations, collections, project deletions, workspace
	// inspections, removals, renames, retained renames and deletes, repairs,
	// repair preparations, then ref, principal and record repairs. Each group
	// keeps its own order.
	var want []string
	for _, group := range [][]string{
		byCreated("project.create", "session.create"),
		byCreated("environment.collect"),
		byCreated("project.delete"),
		byCreated("workspace.inspect", "workspace.loss.inspect"),
		byCreated("session.discard", "session.delete", "session.create.cleanup"),
		byCreated("session.rename"),
		byRowid("project.retained.rename"),
		byRowid("project.retained.delete"),
		byCreated("session.repair"),
		byCreated("session.repair.prepare"),
		byRowid("session.ref.repair"),
		byRowid("session.principal.repair"),
		byRowid("session.record.repair"),
	} {
		want = append(want, group...)
	}
	ops, err := s.UnfinishedOperations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, op := range ops {
		got = append(got, op.ID)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unfinished operations:\n got %q\nwant %q", got, want)
	}
}

// unfinishedOf lists the unfinished operations of kinds, in recovery order.
func unfinishedOf(ctx context.Context, s *Store, kinds ...string) ([]api.Operation, error) {
	ops, err := s.UnfinishedOperations(ctx)
	if err != nil {
		return nil, err
	}
	var selected []api.Operation
	for _, op := range ops {
		for _, kind := range kinds {
			if op.Kind == kind {
				selected = append(selected, op)
			}
		}
	}
	return selected, nil
}

// forgeKeyHolder records a finished operation holding key, as an earlier
// request of kind with the given encoded request and digest would have left it.
func forgeKeyHolder(t *testing.T, s *Store, key, kind, project, session, request, requestSHA string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(context.Background(), `INSERT INTO operations(id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,status,phase,committed,evidence_json,created_at,updated_at)
	 VALUES(?,?,?,?,NULLIF(?,''),?,?,'completed','completed',1,NULL,?,?)`, "holder-"+key, key, kind, project, session, request, requestSHA, now, now); err != nil {
		t.Fatal(err)
	}
}

func operationCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRowContext(context.Background(), `SELECT count(*) FROM operations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// keyCase is one Begin path: the request it records for a key, and the call.
type keyCase struct {
	store            *Store
	project, session string
	request          func(key string) any
	begin            func(key string, observe func(context.Context) error) error
}

// encodedRequest returns the request JSON a Begin method records, and its digest.
func encodedRequest(t *testing.T, request any) (string, string) {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw), digest(raw)
}

func TestIdempotencyKeyRefusesAnotherKindOrRequest(t *testing.T) {
	future := time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)
	for _, test := range []struct {
		name string
		kind string
		// Principal repair observes native state before it reads the key.
		observesFirst bool
		fixture       func(t *testing.T) keyCase
	}{
		{"rename", "session.rename", false, func(t *testing.T) keyCase {
			s, _, r, e := renameStoreFixture(t)
			request := func(key string) RenameRequest { r.Key = key; return r }
			return keyCase{s, "app", renameTestUUID, func(key string) any { return request(key) }, func(key string, observe func(context.Context) error) error {
				_, err := s.BeginRename(context.Background(), request(key), e, observe)
				return err
			}}
		}},
		{"retained delete", "project.retained.delete", false, func(t *testing.T) keyCase {
			s, _, r, e := retainedDeleteFixture(t)
			request := func(key string) RetainedDeleteRequest { r.Key = key; return r }
			return keyCase{s, "app", "", func(key string) any { return request(key) }, func(key string, observe func(context.Context) error) error {
				_, err := s.BeginRetainedDelete(context.Background(), request(key), e, func(ctx context.Context, _ RetainedDeleteEvidence) error { return observe(ctx) })
				return err
			}}
		}},
		{"repair", "session.repair", false, func(t *testing.T) keyCase {
			s, _, r, e := repairStoreFixture(t)
			request := func(key string) RepairRequest { r.Key = key; return r }
			return keyCase{s, e.Project, renameTestUUID, func(key string) any { return request(key) }, func(key string, observe func(context.Context) error) error {
				_, err := s.BeginRepair(context.Background(), request(key), e, observe, repairCapacity)
				return err
			}}
		}},
		{"repair preparation", "session.repair.prepare", false, func(t *testing.T) keyCase {
			s, _, uuid, prep := repairPreparationFixture(t)
			request := func(key string) any {
				return struct {
					Key  string `json:"key"`
					UUID string `json:"uuid"`
				}{key, uuid}
			}
			return keyCase{s, prep.Project, uuid, request, func(key string, observe func(context.Context) error) error {
				_, err := s.BeginRepairPreparation(context.Background(), key, uuid, prep, observe, repairCapacity)
				return err
			}}
		}},
		{"principal repair", "session.principal.repair", true, func(t *testing.T) keyCase {
			s, _, _, prior := repairStoreFixture(t)
			req := PrincipalRepairRequest{UUID: renameTestUUID, TokenSHA256: strings.Repeat("c", 64)}
			ev := PrincipalRepairEvidence{Project: prior.Project, Branch: prior.Branch, PolicySHA256: prior.PolicySHA256,
				AssignedTip: prior.AssignedTip, RefPresent: true,
				GuestKeyStatus: "matching", GuestKeySHA256: strings.Repeat("d", 64),
				OldFingerprint: prior.CredentialFingerprint, OldActive: true, InstanceUUID: prior.InstanceUUID,
				IncusProject: prior.IncusProject, InstanceName: prior.InstanceName,
				IncusUUID:  "11111111-1111-4111-8111-111111111111",
				Generation: "22222222-2222-4222-8222-222222222222", ImageFingerprint: prior.ImageFingerprint,
				ExpiresAt: future}
			request := func(key string) PrincipalRepairRequest { req.Key = key; return req }
			return keyCase{s, prior.Project, renameTestUUID, func(key string) any { return request(key) }, func(key string, observe func(context.Context) error) error {
				_, err := s.BeginPrincipalRepair(context.Background(), request(key), ev, observe)
				return err
			}}
		}},
		{"record repair", "session.record.repair", false, func(t *testing.T) keyCase {
			s, _, _, prior := repairStoreFixture(t)
			ev := RecordRepairEvidence{Project: prior.Project, Branch: prior.Branch,
				Principal: prior.CredentialFingerprint, PrincipalActive: true,
				ExternalAuthority: "none_registered",
				PolicySHA256:      prior.PolicySHA256, InstanceUUID: prior.InstanceUUID,
				IncusProject: prior.IncusProject, InstanceName: prior.InstanceName,
				ExpiresAt: future}
			req := RecordRepairRequest{UUID: renameTestUUID, TokenSHA256: strings.Repeat("a", 64)}
			request := func(key string) RecordRepairRequest { req.Key = key; return req }
			return keyCase{s, prior.Project, renameTestUUID, func(key string) any { return request(key) }, func(key string, observe func(context.Context) error) error {
				_, err := s.BeginRecordRepair(context.Background(), request(key), ev, observe)
				return err
			}}
		}},
		{"ref repair", "session.ref.repair", false, func(t *testing.T) keyCase {
			s, _, _, prior := repairStoreFixture(t)
			lossRaw, _ := json.Marshal(WorkspaceInspectEvidence{Result: json.RawMessage(`{"schema":"p.workspace-loss/v1"}`)})
			ev := RefRepairEvidence{Project: prior.Project, Branch: prior.Branch, Tip: prior.AssignedTip,
				PolicySHA256: prior.PolicySHA256, CredentialFingerprint: prior.CredentialFingerprint,
				InstanceUUID: prior.InstanceUUID, IncusProject: prior.IncusProject, InstanceName: prior.InstanceName,
				ImageFingerprint: prior.ImageFingerprint, IncusUUID: "11111111-1111-4111-8111-111111111111",
				Generation: "22222222-2222-4222-8222-222222222222", OriginalStatus: "Running",
				LossOperationID: "77777777-7777-4777-8777-777777777777", LossFingerprint: strings.Repeat("a", 64), LossResultSHA256: digest(lossRaw),
				PreviewExpiresAt: future}
			req := RefRepairRequest{UUID: renameTestUUID, TokenSHA256: strings.Repeat("c", 64)}
			request := func(key string) RefRepairRequest { req.Key = key; return req }
			return keyCase{s, prior.Project, renameTestUUID, func(key string) any { return request(key) }, func(key string, observe func(context.Context) error) error {
				_, err := s.BeginRefRepair(context.Background(), request(key), ev, observe)
				return err
			}}
		}},
		{"session create", "session.create", false, func(t *testing.T) keyCase {
			s, _ := openTestStore(t)
			if err := s.CreateProject(context.Background(), "app", []byte(`{}`)); err != nil {
				t.Fatal(err)
			}
			req := ReserveSessionRequest{Project: "app", Branch: "work", Choice: "existing"}
			selection := CreationSelection{RuntimeID: "runtime", RuntimeSHA256: strings.Repeat("a", 64), HostID: "host", HostSHA256: strings.Repeat("b", 64), SourceID: "source", SourceSHA256: strings.Repeat("c", 64)}
			request := func(key string) ReserveSessionRequest { req.Key = key; return req }
			return keyCase{s, "app", "", func(key string) any { return request(key) }, func(key string, observe func(context.Context) error) error {
				_, _, err := s.BeginSessionCreateCaptured(context.Background(), request(key), strings.Repeat("d", 64), selection, func(ctx context.Context, _ ReserveSessionRequest) (CapturedSource, error) {
					return CapturedSource{}, observe(ctx)
				})
				return err
			}}
		}},
		{"legacy reservation", "session.create", false, func(t *testing.T) keyCase {
			s, _ := openTestStore(t)
			if err := s.CreateProject(context.Background(), "app", []byte(`{}`)); err != nil {
				t.Fatal(err)
			}
			request := func(key string) ReserveSessionRequest {
				return ReserveSessionRequest{Key: key, Project: "app", Branch: "work", Choice: "existing"}
			}
			return keyCase{s, "app", "", func(key string) any { return request(key) }, func(key string, _ func(context.Context) error) error {
				_, _, err := s.ReserveSession(context.Background(), request(key))
				return err
			}}
		}},
	} {
		for _, holder := range []struct {
			name string
			// The holder records another kind with this exact request, or this
			// kind with another request.
			anotherKind bool
		}{{"another kind", true}, {"another request", false}} {
			t.Run(test.name+" "+holder.name, func(t *testing.T) {
				c := test.fixture(t)
				kind, session, request, sha := test.kind, c.session, "{}", "another-request"
				if holder.anotherKind {
					kind, session = "project.retained.rename", ""
					request, sha = encodedRequest(t, c.request("shared-key"))
				}
				forgeKeyHolder(t, c.store, "shared-key", kind, c.project, session, request, sha)
				before := operationCount(t, c.store)
				observed := false
				observe := func(context.Context) error { observed = true; return nil }
				if err := c.begin("shared-key", observe); !errors.Is(err, ErrConflict) {
					t.Fatalf("answer %v, want a conflict", err)
				}
				if observed != test.observesFirst {
					t.Fatalf("observed native state: %v", observed)
				}
				if after := operationCount(t, c.store); after != before {
					t.Fatalf("operations %d -> %d", before, after)
				}
			})
		}
	}
}

func TestEnvironmentCollectionKeyRefusesAnotherKindOrRequest(t *testing.T) {
	for _, holder := range []struct {
		name        string
		anotherKind bool
	}{{"another kind", true}, {"another request", false}} {
		t.Run(holder.name, func(t *testing.T) {
			s, claim := environmentCollectionKeyFixture(t)
			kind, request, sha := "environment.collect", "{}", "another-request"
			if holder.anotherKind {
				kind = "project.retained.rename"
				request, sha = encodedRequest(t, claim)
			}
			forgeKeyHolder(t, s, "shared-key", kind, claim.Image.Project, "", request, sha)
			before := operationCount(t, s)
			if _, err := s.BeginEnvironmentCollection(context.Background(), "shared-key", claim); !errors.Is(err, ErrConflict) {
				t.Fatalf("answer %v, want a conflict", err)
			}
			if after := operationCount(t, s); after != before {
				t.Fatalf("operations %d -> %d", before, after)
			}
		})
	}
}

func environmentCollectionKeyFixture(t *testing.T) (*Store, EnvironmentCollectionClaim) {
	t.Helper()
	s, _ := openTestStore(t)
	ctx := context.Background()
	if err := s.CreateProject(ctx, "team/a", json.RawMessage(`{"network":"none"}`)); err != nil {
		t.Fatal(err)
	}
	image := testEnvironmentImage("team/a")
	image.LogicalSize = 1024
	if err := s.PutEnvironmentImage(ctx, image); err != nil {
		t.Fatal(err)
	}
	image, _, _ = s.GetEnvironmentImage(ctx, image.Project, image.Key)
	claim := collectionClaim(image)
	return s, claim
}
