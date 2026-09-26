package control

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func projectDeleteFixture(t *testing.T) (*Store, string, ProjectDeleteRequest, ProjectDeleteEvidence) {
	t.Helper()
	s, dir := openTestStore(t)
	ctx := context.Background()
	if e := s.CreateProject(ctx, "bulk", json.RawMessage(`{}`)); e != nil {
		t.Fatal(e)
	}
	ev := ProjectDeleteEvidence{InstanceUUID: "11111111-1111-4111-8111-111111111111", BaseFingerprint: strings.Repeat("b", 64), IncusProject: "user-1000", ReviewSHA256: strings.Repeat("f", 64), Repository: ReplacementLocalIdentity{Inode: 1, Mode: 040700}, RepositoryParent: ReplacementLocalIdentity{Inode: 2, Mode: 040700}}
	for _, branch := range []string{"main", "work"} {
		op, session, e := s.ReserveSession(ctx, ReserveSessionRequest{Key: "create-" + branch, Project: "bulk", Branch: branch, Choice: "new", Source: "refs/heads/seed"})
		if e != nil {
			t.Fatal(e)
		}
		local := replacementCleanupIntent(t, op, ev.BaseFingerprint)
		if e = s.RegisterGitPrincipal(ctx, local.KeyFingerprint, "session", session.Project, session.UUID); e != nil {
			t.Fatal(e)
		}
		if e = s.AdvanceSessionRegistry(ctx, session.UUID, "creating", "established"); e != nil {
			t.Fatal(e)
		}
		if e = s.AdvanceOperation(ctx, op.ID, "completed", "established", true, nil, ""); e != nil {
			t.Fatal(e)
		}
		session, _ = s.GetSession(ctx, session.UUID)
		ev.Sessions = append(ev.Sessions, ProjectDeleteSession{Session: session, Condition: "stopped", Local: *local, Runtime: RemovalRuntimePreview{Condition: "present", IncusProject: ev.IncusProject, InstanceName: "p-" + session.UUID, IncusUUID: "22222222-2222-4222-8222-222222222222", Generation: "33333333-3333-4333-8333-333333333333", ImageFingerprint: ev.BaseFingerprint, OriginalStatus: "Stopped", LossOperationID: "44444444-4444-4444-8444-444444444444", Fingerprint: strings.Repeat("d", 64)}})
	}
	// Store admission compares the exact stable UUID ordering.
	if ev.Sessions[0].Session.UUID > ev.Sessions[1].Session.UUID {
		ev.Sessions[0], ev.Sessions[1] = ev.Sessions[1], ev.Sessions[0]
	}
	for i := range ev.Sessions {
		ps := &ev.Sessions[i]
		inspected := WorkspaceInspectEvidence{InstanceUUID: ev.InstanceUUID, BaseFingerprint: ev.BaseFingerprint, ImageFingerprint: ps.Runtime.ImageFingerprint, SourceIncusUUID: ps.Runtime.IncusUUID, SourceGeneration: ps.Runtime.Generation, OriginalStatus: "Stopped"}
		loss, e := s.BeginWorkspaceLossInspect(ctx, WorkspaceInspectRequest{Key: "loss:" + ps.Session.UUID, SessionUUID: ps.Session.UUID}, inspected)
		if e != nil {
			t.Fatal(e)
		}
		inspected.Result = json.RawMessage(`{"schema":"p.workspace-loss/v1","runtime_data_will_be_removed":true,"worktrees":[{"path":"/workspace"}],"external_worktrees":[],"fingerprint":"` + ps.Runtime.Fingerprint + `"}`)
		raw, _ := json.Marshal(inspected)
		if e = s.AdvanceOperation(ctx, loss.ID, "completed", "inspected", true, raw, ""); e != nil {
			t.Fatal(e)
		}
		loss, _ = s.GetOperation(ctx, loss.ID)
		ps.Runtime.LossOperationID = loss.ID
		ps.Runtime.ObservedAt = loss.UpdatedAt
	}
	image := testEnvironmentImage("bulk")
	image.Fingerprint = strings.Repeat("e", 64)
	if e := s.PutEnvironmentImage(ctx, image); e != nil {
		t.Fatal(e)
	}
	image, _, _ = s.GetEnvironmentImage(ctx, image.Project, image.Key)
	ev.Images = []EnvironmentCollectionClaim{{Image: image, ImagePresent: true, ObservedSize: 1024, RelatedDigest: strings.Repeat("c", 64)}}
	for _, ps := range ev.Sessions {
		ev.Resources = append(ev.Resources, ProjectDeleteResource{Kind: "runtime", ID: ps.Session.UUID, Status: "remaining"}, ProjectDeleteResource{Kind: "local", ID: ps.Session.UUID, Status: "remaining"})
	}
	ev.Resources = append(ev.Resources, ProjectDeleteResource{Kind: "image", ID: image.Key, Status: "remaining"}, ProjectDeleteResource{Kind: "repository", ID: "bare", Status: "remaining"})
	if _, e := s.db.ExecContext(ctx, `INSERT INTO origin_requests VALUES('old-origin','bulk','remove','ssh://host/private-origin','','completed','now','now','{}')`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.db.ExecContext(ctx, `INSERT INTO publication_requests VALUES('old-publication','bulk','ssh://host/private-origin','retained','main',?,'refs/heads/main','completed','{}','now','now')`, strings.Repeat("a", 40)); e != nil {
		t.Fatal(e)
	}
	_, ev.StateSHA256, _ = s.ProjectDeleteSnapshot(ctx, "bulk")
	req := ProjectDeleteRequest{Key: "delete-bulk", Project: "bulk", TokenSHA256: strings.Repeat("a", 64)}
	return s, dir, req, ev
}
func acceptProjectDelete(t *testing.T, s *Store, req ProjectDeleteRequest, ev ProjectDeleteEvidence) Operation {
	t.Helper()
	op, e := s.BeginProjectDelete(context.Background(), req, ev, time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano), func(context.Context) error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	return op
}
func TestProjectDeleteAtomicClosureSQLiteReopenAndMonotonicResources(t *testing.T) {
	s, dir, req, ev := projectDeleteFixture(t)
	ctx := context.Background()
	if e := s.CreateProject(ctx, "unrelated", json.RawMessage(`{}`)); e != nil {
		t.Fatal(e)
	}
	op := acceptProjectDelete(t, s, req, ev)
	if active, e := s.HasActiveProject(ctx, req.Project); e != nil || active {
		t.Fatal("project authority not retired", e)
	}
	for _, ps := range ev.Sessions {
		session, _ := s.GetSession(ctx, ps.Session.UUID)
		if session.Registry != "removing" || s.IsGitPrincipalActive(ctx, ps.Local.KeyFingerprint) {
			t.Fatal("session authority not retired")
		}
	}
	// Raw SQL late writers cannot bypass the API's project-active checks.
	for _, q := range []string{
		`UPDATE projects SET registry_state='active' WHERE path='bulk'`,
		`UPDATE projects SET policy_sha256='changed' WHERE path='bulk'`,
		`UPDATE sessions SET registry_state='established' WHERE project_path='bulk'`,
		`UPDATE sessions SET branch='changed' WHERE project_path='bulk'`,
		`UPDATE git_principals SET active=1 WHERE project_path='bulk'`,
		`DELETE FROM sessions WHERE project_path='bulk'`,
		`DELETE FROM projects WHERE path='bulk'`,
		`INSERT INTO origin_requests VALUES('late','bulk','remove','','','prepared','now','','')`,
		`INSERT INTO git_ref_guards VALUES('bulk','late','` + op.ID + `')`,
		`INSERT INTO operations(id,idempotency_key,kind,project_path,request_json,request_sha256,status,phase,created_at,updated_at) VALUES('late','late','session.create','bulk','{}','hash','running','reserved','now','now')`,
	} {
		if _, e := s.db.ExecContext(ctx, q); e == nil {
			t.Fatal("late authority bypass", q)
		}
	}
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	var e error
	s, e = testScopedOpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	pending, e := s.UnfinishedProjectDeletes(ctx)
	if e != nil || len(pending) != 1 || pending[0].ID != op.ID {
		t.Fatal("restart lost exact intent", e)
	}
	if e = s.ValidateProjectDeleteAuthority(ctx, op, ev); e != nil {
		t.Fatal(e)
	}
	if e = s.CompleteProjectDelete(ctx, op, func(context.Context) error { return nil }); !errors.Is(e, ErrConflict) {
		t.Fatal("forgot incomplete resources", e)
	}
	// Independent partial progress and irreversible dispatch survive reopening.
	next := ev
	next.Resources = append([]ProjectDeleteResource(nil), ev.Resources...)
	next.Resources[0].DeleteIssued = true
	next.Resources[0].Status = "unreachable"
	next.Resources[0].Diagnostic = "native authority unavailable"
	next.Resources[2].Status = "already_absent"
	if e = s.UpdateProjectDelete(ctx, op, next, "blocked", "partial"); e != nil {
		t.Fatal(e)
	}
	op, _ = s.GetOperation(ctx, op.ID)
	rewind := next
	rewind.Resources = append([]ProjectDeleteResource(nil), next.Resources...)
	rewind.Resources[0].DeleteIssued = false
	if e = s.UpdateProjectDelete(ctx, op, rewind, "running", ""); !errors.Is(e, ErrConflict) {
		t.Fatal("delete dispatch rewound", e)
	}
	rewind = next
	rewind.Resources = append([]ProjectDeleteResource(nil), next.Resources...)
	rewind.Resources[2].Status = "remaining"
	if e = s.UpdateProjectDelete(ctx, op, rewind, "running", ""); !errors.Is(e, ErrConflict) {
		t.Fatal("positive absence rewound", e)
	}
	changed := next
	changed.Sessions = append([]ProjectDeleteSession(nil), next.Sessions...)
	changed.Sessions[0].Runtime.IncusUUID = "55555555-5555-4555-8555-555555555555"
	if e = s.UpdateProjectDelete(ctx, op, changed, "running", ""); !errors.Is(e, ErrConflict) {
		t.Fatal("native identity drift admitted", e)
	}
	if e = s.AdvanceOperation(ctx, op.ID, "completed", "completed", true, nil, ""); !errors.Is(e, ErrConflict) {
		t.Fatal("generic advance bypassed absence verification", e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = testScopedOpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	op, _ = s.GetOperation(ctx, op.ID)
	var durable ProjectDeleteEvidence
	if json.Unmarshal(op.Evidence, &durable) != nil || !reflect.DeepEqual(durable, next) {
		t.Fatal("restart changed partial targets")
	}
	for i := range durable.Resources {
		if !projectDeleteResourceAbsent(durable.Resources[i]) {
			durable.Resources[i].Status = "deleted"
			durable.Resources[i].Diagnostic = ""
		}
	}
	if e = s.UpdateProjectDelete(ctx, op, durable, "running", ""); e != nil {
		t.Fatal(e)
	}
	op, _ = s.GetOperation(ctx, op.ID)
	if e = s.CompleteProjectDelete(ctx, op, func(context.Context) error { return errors.New("Incus unreachable") }); e == nil {
		t.Fatal("unavailable final proof forgot identity")
	}
	if _, e = s.GetSession(ctx, ev.Sessions[0].Session.UUID); e != nil {
		t.Fatal("uncertain completion lost session", e)
	}
	if e = s.CompleteProjectDelete(ctx, op, func(context.Context) error { return nil }); e != nil {
		t.Fatal(e)
	}
	if _, e = s.GetSession(ctx, ev.Sessions[0].Session.UUID); !errors.Is(e, ErrNotFound) {
		t.Fatal("session remains", e)
	}
	if active, e := s.HasActiveProject(ctx, "unrelated"); e != nil || !active {
		t.Fatal("unrelated project changed", e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = testScopedOpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	old := ev.Sessions[0].Local.OldOperationID
	if _, e = s.GetOperation(ctx, old); !errors.Is(e, ErrNotFound) {
		t.Fatal("old full operation retained", e)
	}
	for _, key := range []string{"create-main", "create-work", "old-origin", "old-publication"} {
		if _, e = s.GetOperationByKey(ctx, key); !errors.Is(e, ErrConflict) {
			t.Fatal("retired namespace key reused", key, e)
		}
	}
	_, _, e = s.BeginSessionCreate(ctx, ReserveSessionRequest{Key: "create-main", Project: "bulk", Branch: "main", Choice: "new", Source: "refs/heads/seed"}, "", CreationSelection{}, func(context.Context, ReserveSessionRequest) (string, bool, error) {
		t.Fatal("retired Create read source")
		return "", false, nil
	})
	if !errors.Is(e, ErrConflict) {
		t.Fatal("old Create recreated source", e)
	}
	if _, e = s.BeginBlankProject(ctx, BlankProjectRequest{Key: "create-main", Project: "bulk"}, json.RawMessage(`{}`), ev.BaseFingerprint, CreationSelection{}); !errors.Is(e, ErrConflict) {
		t.Fatal("retired key acquired new project", e)
	}
	if _, _, e = s.CompletedOriginChange(ctx, OriginChange{Key: "old-origin", Project: "bulk", Kind: "remove", ExpectedURL: "ssh://host/private-origin"}); !errors.Is(e, ErrConflict) {
		t.Fatal("retired origin replay accepted", e)
	}
	if _, _, e = s.PublicationRecord(ctx, PublicationRequest{Key: "old-publication", Project: "bulk"}); !errors.Is(e, ErrConflict) {
		t.Fatal("retired publication accepted", e)
	}
	originRunner := &fakeOriginRunner{}
	if _, e = (OriginAPI{Store: s, Runner: originRunner}).Change(ctx, OriginChange{Key: "old-origin", Project: "bulk", Kind: "remove", ExpectedURL: "ssh://host/private-origin"}); !errors.Is(e, ErrConflict) || originRunner.calls != 0 {
		t.Fatal("retired Origin contacted external authority", e)
	}
	publicationRunner := &fakePublication{}
	if _, e = (OriginAPI{Store: s, Runner: publicationRunner}).PublishPublication(ctx, PublicationRequest{Key: "old-publication", Project: "bulk", ExpectedOriginURL: "ssh://host/private-origin", Kind: "retained", Source: "main", SourceOID: strings.Repeat("a", 40), DestinationRef: "refs/heads/main"}); !errors.Is(e, ErrConflict) || publicationRunner.observes != 0 || publicationRunner.pushes != 0 {
		t.Fatal("retired publication contacted external authority", e)
	}
	for _, table := range []string{"origin_requests", "publication_requests", "sessions", "projects", "environment_images"} {
		var count int
		if e = s.db.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE `+func() string {
			if table == "projects" {
				return "path"
			}
			return "project_path"
		}()+`='bulk'`).Scan(&count); e != nil || count != 0 {
			t.Fatal("full project record remains", table, count, e)
		}
	}
	var count int
	if e = s.db.QueryRowContext(ctx, `SELECT count(*) FROM retired_lifecycle_requests WHERE length(request_sha256)!=64 OR kind=''`).Scan(&count); e != nil || count != 0 {
		t.Fatal("minimal retirement receipt malformed", e)
	}
	if _, e = s.db.ExecContext(ctx, `DELETE FROM retired_lifecycle_requests WHERE idempotency_key='create-main'`); e == nil {
		t.Fatal("retired replay restriction erased")
	}
	if _, e = s.db.ExecContext(ctx, `INSERT INTO origin_requests VALUES('old-origin','unrelated','remove','','','prepared','now','','')`); e == nil {
		t.Fatal("raw old origin key reused")
	}

	replay, e := s.BeginProjectDelete(ctx, req, ev, time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano), func(context.Context) error { t.Fatal("replay invoked new authority"); return nil })
	if e != nil || replay.ID != op.ID || replay.Status != "completed" || len(replay.Evidence) != 0 {
		t.Fatal("completed exact replay changed", e)
	}
}
func TestProjectDeleteStaleSnapshotAndAdmissionRace(t *testing.T) {
	for _, change := range []string{"verify", "expiry", "policy", "new-session", "active-operation", "origin", "cache"} {
		t.Run(change, func(t *testing.T) {
			s, _, req, ev := projectDeleteFixture(t)
			ctx := context.Background()
			expiry := time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)
			verify := func(context.Context) error { return nil }
			switch change {
			case "verify":
				verify = func(context.Context) error { return ErrConflict }
			case "expiry":
				expiry = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
			case "policy":
				if e := s.SetProjectPolicy(ctx, "bulk", json.RawMessage(`{"changed":true}`)); e != nil {
					t.Fatal(e)
				}
			case "new-session", "active-operation":
				_, _, e := s.ReserveSession(ctx, ReserveSessionRequest{Key: "late", Project: "bulk", Branch: "late", Choice: "new", Source: "refs/heads/main"})
				if e != nil {
					t.Fatal(e)
				}
			case "origin":
				_, e := s.db.ExecContext(ctx, `INSERT INTO project_origins VALUES('bulk','ssh://host/repo','unknown','now','[]','')`)
				if e != nil {
					t.Fatal(e)
				}
			case "cache":
				_, e := s.db.ExecContext(ctx, `UPDATE environment_images SET logical_size=999 WHERE project_path='bulk'`)
				if e != nil {
					t.Fatal(e)
				}
			}
			if _, e := s.BeginProjectDelete(ctx, req, ev, expiry, verify); !errors.Is(e, ErrConflict) {
				t.Fatal("stale proof accepted", e)
			}
			if active, _ := s.HasActiveProject(ctx, "bulk"); !active {
				t.Fatal("stale report retired project")
			}
		})
	}
	s, _, req, ev := projectDeleteFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(2)
	start := make(chan struct{})
	var deleteErr, createErr error
	go func() {
		defer wg.Done()
		<-start
		_, deleteErr = s.BeginProjectDelete(ctx, req, ev, time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano), func(context.Context) error { return nil })
	}()
	go func() {
		defer wg.Done()
		<-start
		_, _, createErr = s.ReserveSession(ctx, ReserveSessionRequest{Key: "race-create", Project: "bulk", Branch: "race", Choice: "new", Source: "refs/heads/main"})
	}()
	close(start)
	wg.Wait()
	if (deleteErr == nil) == (createErr == nil) {
		t.Fatalf("authority race elected %v / %v", deleteErr, createErr)
	}
}
