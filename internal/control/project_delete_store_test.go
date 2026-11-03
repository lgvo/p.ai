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

	"github.com/lgvo/p.ai/internal/api"
)

func projectDeleteFixture(t *testing.T) (*Store, string, ProjectDeleteRequest, ProjectDeleteEvidence) {
	t.Helper()
	s, dir := openTestStore(t)
	ctx := context.Background()
	if e := s.CreateProject(ctx, "bulk", json.RawMessage(`{}`)); e != nil {
		t.Fatal(e)
	}
	ev := ProjectDeleteEvidence{InstanceUUID: "11111111-1111-4111-8111-111111111111", BaseFingerprint: strings.Repeat("b", 64), IncusProject: "user-1000", ReviewSHA256: strings.Repeat("f", 64), Repository: api.ReplacementLocalIdentity{Inode: 1, Mode: 040700}, RepositoryParent: api.ReplacementLocalIdentity{Inode: 2, Mode: 040700}}
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
		if e = s.AdvanceOperation(ctx, op.ID, "completed", "established", true, nil, "", ""); e != nil {
			t.Fatal(e)
		}
		session, _ = s.GetSession(ctx, session.UUID)
		ev.Sessions = append(ev.Sessions, api.ProjectDeleteSession{Session: api.SessionRecord(session), Condition: "stopped", Local: *local, Runtime: api.RemovalRuntime{Condition: "present", IncusProject: ev.IncusProject, InstanceName: "p-" + session.UUID, IncusUUID: "22222222-2222-4222-8222-222222222222", Generation: "33333333-3333-4333-8333-333333333333", ImageFingerprint: ev.BaseFingerprint, OriginalStatus: "Stopped"}})
	}
	// Store admission compares the exact stable UUID ordering.
	if ev.Sessions[0].Session.UUID > ev.Sessions[1].Session.UUID {
		ev.Sessions[0], ev.Sessions[1] = ev.Sessions[1], ev.Sessions[0]
	}
	image := testCommitImage("bulk", strings.Repeat("a", 40))
	if e := s.PutCommitImage(ctx, image); e != nil {
		t.Fatal(e)
	}
	image, _, _ = s.GetCommitImage(ctx, image.Key)
	ev.Images = []CommitImageClaim{{Image: image, ImagePresent: true, ObservedSize: 1024, RelatedDigest: strings.Repeat("c", 64)}}
	for _, ps := range ev.Sessions {
		ev.Resources = append(ev.Resources, ProjectDeleteResource{Kind: "runtime", ID: ps.Session.UUID, Status: "remaining"}, ProjectDeleteResource{Kind: "local", ID: ps.Session.UUID, Status: "remaining"})
	}
	ev.Resources = append(ev.Resources, ProjectDeleteResource{Kind: "image", ID: image.Ref, Status: "remaining"}, ProjectDeleteResource{Kind: "repository", ID: "bare", Status: "remaining"})
	// The project's completed origin operations: deletion retires their keys.
	completedOperation(t, s, KindOriginChange, OriginChange{Key: "old-origin", Project: "bulk", Kind: "remove", ExpectedURL: "ssh://host/private-origin"})
	completedOperation(t, s, KindOriginPublish, oldPublication)
	_, ev.StateSHA256, _ = s.ProjectDeleteSnapshot(ctx, "bulk")
	req := ProjectDeleteRequest{Key: "delete-bulk", Project: "bulk", IfMatch: testTag(time.Minute)}
	return s, dir, req, ev
}

// oldPublication is the request of the publication projectDeleteFixture
// records completed.
var oldPublication = PublicationRequest{Key: "old-publication", Project: "bulk", ExpectedOriginURL: "ssh://host/private-origin", Kind: "retained", Source: "main", SourceOID: strings.Repeat("a", 40), DestinationRef: "refs/heads/main",
	IfMatch: "20261102T153012Z-" + strings.Repeat("a", 64)}

// completedOperation records request, an OriginChange or PublicationRequest,
// as a completed operation of kind.
func completedOperation(t *testing.T, s *Store, kind string, request any) {
	t.Helper()
	ctx := context.Background()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var named struct{ Key, Project string }
	if err = json.Unmarshal(raw, &named); err != nil {
		t.Fatal(err)
	}
	id, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	if err = insertOperation(ctx, s, operationRecord{ID: id, Key: named.Key, Kind: kind, Project: named.Project, Request: raw,
		Phase: "accepted", Now: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	if err = s.AdvanceOperation(ctx, id, "completed", "completed", false, nil, "", ""); err != nil {
		t.Fatal(err)
	}
}

func acceptProjectDelete(t *testing.T, s *Store, req ProjectDeleteRequest, ev ProjectDeleteEvidence) api.Operation {
	t.Helper()
	op, e := s.BeginProjectDelete(context.Background(), req, ev, testHash, func(context.Context) error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	checkIntent(t, s, op, api.OperationIntent{Project: req.Project})
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
		`INSERT INTO operations(id,idempotency_key,kind,project_path,request_json,request_sha256,intent_json,status,phase,created_at,updated_at) VALUES('late-origin','late-origin','origin.change','bulk','{}','hash','{}','running','accepted','now','now')`,
		`INSERT INTO project_origins(project_path,url,current_status,observed_at,refs_json) VALUES('bulk','ssh://host/late','fresh','now','[]')`,
		`INSERT INTO git_ref_guards VALUES('bulk','late','` + op.ID + `')`,
		`INSERT INTO operations(id,idempotency_key,kind,project_path,request_json,request_sha256,intent_json,status,phase,created_at,updated_at) VALUES('late','late','session.create','bulk','{}','hash','{}','running','reserved','now','now')`,
		`INSERT INTO commit_images(project_path,commit_oid,base_image,system,builder_version,image_ref,build_ref,labels_json,selection,logical_size,state,created_at) VALUES('bulk','late','base','x86_64-linux','v1','late','build','{}','no-flake',0,'ready','now')`,
		`UPDATE commit_images SET state='releasing' WHERE project_path='bulk'`,
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
	pending, e := unfinishedOf(ctx, s, KindProjectDelete)
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
	if e = s.UpdateProjectDelete(ctx, op, next, "blocked", "runtime", "partial"); e != nil {
		t.Fatal(e)
	}
	op, _ = s.GetOperation(ctx, op.ID)
	rewind := next
	rewind.Resources = append([]ProjectDeleteResource(nil), next.Resources...)
	rewind.Resources[0].DeleteIssued = false
	if e = s.UpdateProjectDelete(ctx, op, rewind, "running", "", ""); !errors.Is(e, ErrConflict) {
		t.Fatal("delete dispatch rewound", e)
	}
	rewind = next
	rewind.Resources = append([]ProjectDeleteResource(nil), next.Resources...)
	rewind.Resources[2].Status = "remaining"
	if e = s.UpdateProjectDelete(ctx, op, rewind, "running", "", ""); !errors.Is(e, ErrConflict) {
		t.Fatal("positive absence rewound", e)
	}
	changed := next
	changed.Sessions = append([]api.ProjectDeleteSession(nil), next.Sessions...)
	changed.Sessions[0].Runtime.IncusUUID = "55555555-5555-4555-8555-555555555555"
	if e = s.UpdateProjectDelete(ctx, op, changed, "running", "", ""); !errors.Is(e, ErrConflict) {
		t.Fatal("native identity drift admitted", e)
	}
	if e = s.AdvanceOperation(ctx, op.ID, "completed", "completed", true, nil, "", ""); !errors.Is(e, ErrConflict) {
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
	if e = s.UpdateProjectDelete(ctx, op, durable, "running", "", ""); e != nil {
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
	if _, e = s.BeginOriginChange(ctx, OriginChange{Key: "old-origin", Project: "bulk", Kind: "remove", ExpectedURL: "ssh://host/private-origin"}); !errors.Is(e, ErrConflict) {
		t.Fatal("retired origin replay accepted", e)
	}
	if _, e = s.BeginPublication(ctx, oldPublication, testHash, func(context.Context) (string, error) {
		t.Fatal("retired publication admitted")
		return "", nil
	}); !errors.Is(e, ErrConflict) {
		t.Fatal("retired publication accepted", e)
	}
	noQueue := func(string) error { t.Fatal("retired request queued work"); return nil }
	originRunner := &fakeOriginRunner{}
	if _, e = (OriginAPI{Store: s, Runner: originRunner, Queue: noQueue}).Change(ctx, OriginChange{Key: "old-origin", Project: "bulk", Kind: "remove", ExpectedURL: "ssh://host/private-origin"}); !errors.Is(e, ErrConflict) || originRunner.calls != 0 {
		t.Fatal("retired Origin contacted external authority", e)
	}
	publicationRunner := &fakePublication{}
	if _, e = (OriginAPI{Store: s, Runner: publicationRunner, Queue: noQueue}).Publish(ctx, publishRequest(oldPublication, api.ChangeRequest{Key: oldPublication.Key, IfMatch: oldPublication.IfMatch})); !errors.Is(e, ErrConflict) || publicationRunner.observes != 0 || publicationRunner.pushes != 0 {
		t.Fatal("retired publication contacted external authority", e)
	}
	for _, table := range []string{"project_origins", "sessions", "projects", "commit_images"} {
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
	if e = s.db.QueryRowContext(ctx, `SELECT count(*) FROM operations WHERE project_path='bulk' AND id!=?`, op.ID).Scan(&count); e != nil || count != 0 {
		t.Fatal("full operation record remains", count, e)
	}
	if _, e = s.db.ExecContext(ctx, `INSERT INTO operations(id,idempotency_key,kind,project_path,request_json,request_sha256,intent_json,status,phase,created_at,updated_at) VALUES('reused','old-origin','origin.change','unrelated','{}','hash','{}','running','accepted','now','now')`); e == nil {
		t.Fatal("raw old origin key reused")
	}

	replay, e := s.BeginProjectDelete(ctx, req, ev, testHash, func(context.Context) error { t.Fatal("replay invoked new authority"); return nil })
	if e != nil || replay.ID != op.ID || replay.Status != "completed" || len(replay.Evidence) != 0 {
		t.Fatal("completed exact replay changed", e)
	}
}
func TestProjectDeleteStaleSnapshotAndAdmissionRace(t *testing.T) {
	for _, change := range []string{"verify", "expiry", "policy", "new-session", "active-operation", "origin", "image"} {
		t.Run(change, func(t *testing.T) {
			s, _, req, ev := projectDeleteFixture(t)
			ctx := context.Background()
			verify := func(context.Context) error { return nil }
			switch change {
			case "verify":
				verify = func(context.Context) error { return ErrConflict }
			case "expiry":
				req.IfMatch = testTag(-time.Second)
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
			case "image":
				if e := s.MarkCommitImageReleasing(ctx, ev.Images[0].Image.Key, ev.Images[0].Image.Ref); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := s.BeginProjectDelete(ctx, req, ev, testHash, verify); !errors.Is(e, ErrConflict) {
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
		_, deleteErr = s.BeginProjectDelete(ctx, req, ev, testHash, func(context.Context) error { return nil })
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

func TestProjectDeletionPlanShowsEachImageClaim(t *testing.T) {
	img := testCommitImage("bulk", strings.Repeat("a", 40))
	img.State = "releasing"
	claim := CommitImageClaim{Image: img, ImagePresent: true, ObservedSize: 2048, RelatedDigest: strings.Repeat("f", 64)}
	k := img.Key
	want := []api.CommitImageClaim{{
		Image: api.CommitImage{
			Key: api.CommitImageKey{Project: "bulk", Commit: k.Commit, BaseImage: k.BaseImage, System: "x86_64-linux", BuilderVersion: "incus-commit-image/1"},
			Ref: img.Ref, Build: img.Build,
			Labels: []api.ImageLabel{{Name: "base_image", Value: k.BaseImage}, {Name: "build", Value: img.Build}, {Name: "builder_version", Value: "incus-commit-image/1"},
				{Name: "project", Value: "bulk"}, {Name: "source_commit", Value: k.Commit}, {Name: "system", Value: "x86_64-linux"}},
			Selection: "devshell", MaterialSHA256: img.MaterialSHA256, Size: 1024, State: "releasing",
		},
		ImagePresent: true, ObservedSize: 2048, RelatedDigest: strings.Repeat("f", 64),
	}}
	if got := CommitImageClaimViews([]CommitImageClaim{claim}); !reflect.DeepEqual(got, want) {
		t.Fatalf("preview shows %+v, want %+v", got, want)
	}
	if got := CommitImageClaimViews(nil); got == nil || len(got) != 0 {
		t.Fatalf("a deletion without images shows %#v, not an empty list", got)
	}
}

// TestProjectDeleteStateNamesAReCreatedImage checks that a commit image
// forgotten and indexed again with otherwise identical facts changes the
// state digest a project deletion's plan shows, and so its change hash: the
// plan the user saw named the earlier image.
func TestProjectDeleteStateNamesAReCreatedImage(t *testing.T) {
	s, _, _, ev := projectDeleteFixture(t)
	ctx := context.Background()
	image := ev.Images[0].Image
	_, before, err := s.ProjectDeleteSnapshot(ctx, "bulk")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ForgetCommitImage(ctx, image.Key, image.Ref); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if err = s.PutCommitImage(ctx, image); err != nil {
		t.Fatal(err)
	}
	again, _, _ := s.GetCommitImage(ctx, image.Key)
	again.CreatedAt, image.CreatedAt = "", ""
	if !reflect.DeepEqual(again, image) {
		t.Fatalf("re-created image differs beyond its creation: %+v %+v", again, image)
	}
	if _, after, err := s.ProjectDeleteSnapshot(ctx, "bulk"); err != nil || after == before {
		t.Fatalf("a re-created image kept state digest %s: %v", after, err)
	}
}
