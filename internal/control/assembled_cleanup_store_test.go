package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/api"
)

// assembledCreatorFixture records a blocked local base-image creation whose
// stopped assembly completed before startup failed.
func assembledCreatorFixture(t *testing.T) (*Store, string, api.Operation, Session) {
	t.Helper()
	s, dir := openTestStore(t)
	ctx := context.Background()
	policy, _, err := ProjectPolicySnapshot(ProjectPolicy{Network: "none", Command: []string{"/bin/false"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CreateProject(ctx, "app", policy); err != nil {
		t.Fatal(err)
	}
	image := strings.Repeat("a", 64)
	req := ReserveSessionRequest{Key: "failed-create", Project: "app", Branch: "work", Choice: "new", Source: "refs/heads/main"}
	op, session, err := s.BeginSessionCreate(ctx, req, image, testSelection(), func(context.Context, ReserveSessionRequest) (string, bool, error) {
		return strings.Repeat("b", 40), false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	creation, _ := Evidence(op)
	creation.RuntimeInitState = "attempted"
	raw, _ := json.Marshal(creation)
	if err = s.AdvanceOperation(ctx, op.ID, "blocked", "assembly-ready", true, raw, "interactive startup failed"); err != nil {
		t.Fatal(err)
	}
	op, _ = s.GetOperation(ctx, op.ID)
	if err = AssembledCreationForCleanup(session, op, image); err != nil {
		t.Fatal(err)
	}
	return s, dir, op, session
}

func TestAssembledCreationForCleanupRefusesUnsupportedCreators(t *testing.T) {
	for _, name := range []string{"legacy-init", "not-attempted", "early", "workspace-ready", "running", "bootstrap", "origin", "builder", "environment", "publication", "captured-oid", "policy", "image", "selection", "replacement"} {
		t.Run(name, func(t *testing.T) {
			_, _, op, session := assembledCreatorFixture(t)
			creation, _ := Evidence(op)
			switch name {
			case "legacy-init":
				creation.RuntimeInitState = ""
			case "not-attempted":
				creation.RuntimeInitState = "not-attempted"
			case "early":
				op.Phase = "runtime-created"
			case "workspace-ready":
				op.Phase = "workspace-ready"
			case "running":
				op.Status = "running"
			case "bootstrap":
				op.Kind = "project.create"
			case "origin":
				creation.OriginURL = "ssh://origin.invalid/repo"
			case "builder":
				creation.EnvironmentBuilder = &CreationBuilderState{State: "absent", Cycle: 1}
			case "environment":
				creation.Environment = &EnvironmentIntent{}
			case "publication":
				creation.EnvironmentState = &EnvironmentState{}
			case "captured-oid":
				creation.CapturedOID = ""
			case "policy":
				creation.PolicySHA256 = strings.Repeat("c", 64)
			case "image":
				creation.ImageFingerprint = strings.Repeat("c", 64)
			case "selection":
				creation.Selection = CreationSelection{}
			case "replacement":
				creation.SupersedesOperationID = op.ID
			}
			op.Evidence, _ = json.Marshal(creation)
			if err := AssembledCreationForCleanup(session, op, strings.Repeat("a", 64)); !errors.Is(err, ErrConflict) {
				t.Fatalf("unsupported creator admitted: %v", err)
			}
		})
	}
}

func assembledCleanupFixture(t *testing.T) (*Store, string, api.Operation, CreateCleanupEvidence) {
	t.Helper()
	s, dir, creator, session := assembledCreatorFixture(t)
	ctx := context.Background()
	creation, _ := Evidence(creator)
	var request ReserveSessionRequest
	_ = json.Unmarshal(creator.Request, &request)
	local := replacementCleanupIntent(t, creator, creation.ImageFingerprint)
	if err := s.RegisterGitPrincipal(ctx, local.KeyFingerprint, "session", creator.Project, creator.SessionUUID); err != nil {
		t.Fatal(err)
	}
	runtime := &api.RemovalRuntimePreview{Condition: "present", IncusProject: "user-1000", InstanceName: "p-" + creator.SessionUUID, IncusUUID: "44444444-4444-4444-8444-444444444444",
		Generation: "55555555-5555-4555-8555-555555555555", ImageFingerprint: creation.ImageFingerprint, OriginalStatus: "Stopped", RuntimeDataLost: true}
	ev := CreateCleanupEvidence{InstanceUUID: "33333333-3333-4333-8333-333333333333", Review: api.CreateCleanupPreview{UUID: creator.SessionUUID, OldOperationID: creator.ID, OldRequest: request,
		OldPhase: creator.Phase, OldEvidenceSHA256: digest(creator.Evidence), OldRequestSHA256: digest(creator.Request), PolicySHA256: session.PolicySHA256,
		AssignedBranch: api.CreateReplaceBranch{Ref: "refs/heads/work", Observed: true, Exists: true, OID: creation.CapturedOID}, ImageFingerprint: creation.ImageFingerprint,
		Runtime: runtime, Provisional: api.CreateReplaceResources{Cleanup: local}, LossWarnings: []string{"workspace", "private files and credentials", "host credentials"}, Eligible: true,
		ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}}
	return s, dir, creator, ev
}

func beginAssembledCleanup(t *testing.T, s *Store, ev CreateCleanupEvidence) api.Operation {
	t.Helper()
	op, err := s.BeginAssembledCreateCleanup(context.Background(), DiscardRequest{Key: "cleanup", UUID: ev.Review.UUID, TokenSHA256: strings.Repeat("d", 64)}, ev, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func TestAssembledCleanupSQLiteReopenAtomicRetirementAndForwardCompletion(t *testing.T) {
	s, dir, creator, ev := assembledCleanupFixture(t)
	ctx := context.Background()
	op := beginAssembledCleanup(t, s, ev)
	if op.Committed || op.Phase != "validated" {
		t.Fatal("cleanup crossed authority boundary before its final checks")
	}
	if err := s.AdvanceOperation(ctx, creator.ID, "running", creator.Phase, true, creator.Evidence, ""); !errors.Is(err, ErrConflict) {
		t.Fatal("creator retry bypassed pending cleanup", err)
	}
	if err := s.CompleteCreation(ctx, creator.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("pending cleanup promoted creator", err)
	}
	for _, query := range []string{`UPDATE sessions SET branch='changed' WHERE uuid=?`, `UPDATE sessions SET registry_state='established' WHERE uuid=?`, `DELETE FROM sessions WHERE uuid=?`} {
		if _, err := s.db.ExecContext(ctx, query, creator.SessionUUID); err == nil {
			t.Fatalf("pending cleanup assignment guard bypassed: %s", query)
		}
	}
	if _, err := s.BeginAssembledCreateCleanup(ctx, DiscardRequest{Key: "overlap", UUID: creator.SessionUUID, TokenSHA256: strings.Repeat("e", 64)}, ev, func(context.Context) error { return nil }); !errors.Is(err, ErrConflict) {
		t.Fatal("second cleanup overlapped the pending one", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	s, err = testScopedOpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.ValidateAssembledCleanupAuthority(ctx, op); err != nil {
		t.Fatal("reopen lost reversible creator/ref proof", err)
	}
	if err = s.CommitAssembledCleanupDelete(ctx, op, func(context.Context) error { return ErrConflict }); !errors.Is(err, ErrConflict) {
		t.Fatal("stale native/ref proof admitted", err)
	}
	old, _ := s.GetOperation(ctx, creator.ID)
	session, _ := s.GetSession(ctx, creator.SessionUUID)
	if old.Status != "blocked" || session.Registry != "creating" {
		t.Fatal("failed final proof retired original authority")
	}
	if err = s.CommitAssembledCleanupDelete(ctx, op, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	op, _ = s.GetOperation(ctx, op.ID)
	if !op.Committed || op.Phase != "delete-issued" {
		t.Fatal("native admission was not durable")
	}
	for _, query := range []string{`UPDATE sessions SET branch='changed' WHERE uuid=?`, `UPDATE sessions SET registry_state='creating' WHERE uuid=?`, `DELETE FROM sessions WHERE uuid=?`} {
		if _, err = s.db.ExecContext(ctx, query, creator.SessionUUID); err == nil {
			t.Fatalf("committed cleanup assignment guard bypassed: %s", query)
		}
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO operations(id,idempotency_key,kind,project_path,session_uuid,request_json,request_sha256,status,phase,created_at,updated_at) VALUES('other','other','unknown',?,?, '{}',?,'running','unknown','now','now')`, creator.Project, creator.SessionUUID, strings.Repeat("a", 64)); err == nil {
		t.Fatal("committed cleanup admitted competing durable operation")
	}
	old, _ = s.GetOperation(ctx, creator.ID)
	session, _ = s.GetSession(ctx, creator.SessionUUID)
	active, _ := s.IsSessionGitPrincipalActive(ctx, creator.SessionUUID)
	if old.Status != "superseded" || session.Registry != "removing" || active {
		t.Fatal("atomic native boundary retained source authority")
	}
	if err = s.AdvanceOperation(ctx, creator.ID, "running", creator.Phase, true, creator.Evidence, ""); !errors.Is(err, ErrConflict) {
		t.Fatal("retired creator retry acted", err)
	}
	if err = s.AdvanceOperation(ctx, op.ID, "running", "validated", false, op.Evidence, ""); !errors.Is(err, ErrConflict) {
		t.Fatal("stale precommit writer rewound deletion", err)
	}
	if err = s.FailStaleAssembledCleanup(ctx, op, "stale"); !errors.Is(err, ErrConflict) {
		t.Fatal("delete dispatch cancelled", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = testScopedOpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.ValidateAssembledCleanupAuthority(ctx, op); err != nil {
		t.Fatal("reopen lost committed exact source proof", err)
	}
	var request ReserveSessionRequest
	_ = json.Unmarshal(creator.Request, &request)
	replay, session, err := s.BeginSessionCreate(ctx, request, "", CreationSelection{}, func(context.Context, ReserveSessionRequest) (string, bool, error) {
		t.Fatal("old replay recaptured source")
		return "", false, nil
	})
	if err != nil || replay.ID != creator.ID || replay.Status != "superseded" || session.UUID != "" {
		t.Fatal("old replay recreated retired identity", err)
	}
	ev.RuntimeAbsent = true
	raw, _ := json.Marshal(ev)
	if err = s.AdvanceOperation(ctx, op.ID, "running", "runtime-absent", true, raw, ""); err != nil {
		t.Fatal(err)
	}
	if err = s.AdvanceOperation(ctx, op.ID, "running", "delete-issued", true, op.Evidence, ""); !errors.Is(err, ErrConflict) {
		t.Fatal("stale writer erased positive absence", err)
	}
	ev.LocalComplete = true
	raw, _ = json.Marshal(ev)
	if err = s.AdvanceOperation(ctx, op.ID, "running", "local-complete", true, raw, ""); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteCreateCleanup(ctx, op.ID, func(context.Context, CreateCleanupEvidence) error { return errors.New("native authority unavailable") }); err == nil {
		t.Fatal("unknown absence released assignment")
	}
	if _, err = s.GetSession(ctx, creator.SessionUUID); err != nil {
		t.Fatal("uncertainty lost retained removing identity")
	}
	if err = s.CompleteCreateCleanup(ctx, op.ID, func(context.Context, CreateCleanupEvidence) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetSession(ctx, creator.SessionUUID); !errors.Is(err, ErrNotFound) {
		t.Fatal("completed cleanup retained assignment", err)
	}
	if _, active, err := s.ActiveSessionChange(ctx, creator.SessionUUID); err != nil || active {
		t.Fatal("completed cleanup retained guard", err)
	}
}

func TestAssembledCleanupStalePrecommitReleasesOnlyItsOwnGuardAcrossRestart(t *testing.T) {
	s, dir, creator, ev := assembledCleanupFixture(t)
	ctx := context.Background()
	op := beginAssembledCleanup(t, s, ev)
	if err := s.AdvanceOperation(ctx, op.ID, "blocked", "stale-cleanup", false, op.Evidence, "assembled source changed"); err != nil {
		t.Fatal(err)
	}
	op, _ = s.GetOperation(ctx, op.ID)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := testScopedOpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.AdvanceOperation(ctx, creator.ID, "running", creator.Phase, true, creator.Evidence, ""); !errors.Is(err, ErrConflict) {
		t.Fatal("unsettled stale cleanup lost creator guard", err)
	}
	if err = s.FailStaleAssembledCleanup(ctx, op, "review the cleanup again"); err != nil {
		t.Fatal(err)
	}
	old, _ := s.GetOperation(ctx, creator.ID)
	session, _ := s.GetSession(ctx, creator.SessionUUID)
	active, _ := s.IsSessionGitPrincipalActive(ctx, creator.SessionUUID)
	if old.Status != "blocked" || session.Registry != "creating" || !active {
		t.Fatal("pre-deletion stale refusal changed original creator authority")
	}
	if _, active, err := s.ActiveSessionChange(ctx, creator.SessionUUID); err != nil || active {
		t.Fatal("settled stale precommit guard retained", err)
	}
	if err = s.AdvanceOperation(ctx, op.ID, "running", "validated", false, op.Evidence, ""); !errors.Is(err, ErrConflict) {
		t.Fatal("stale confirmation restarted", err)
	}
	ev.Review.ExpiresAt = time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)
	if _, err = s.BeginAssembledCreateCleanup(ctx, DiscardRequest{Key: "fresh-cleanup", UUID: creator.SessionUUID, TokenSHA256: strings.Repeat("e", 64)}, ev, func(context.Context) error { return nil }); err != nil {
		t.Fatal("fresh cleanup was stranded", err)
	}
}

func TestAssembledCleanupRefusesUnboundReviewAndRacesOriginalRetry(t *testing.T) {
	for _, name := range []string{"creator-request", "creator-evidence", "expiry", "verify-race"} {
		t.Run(name, func(t *testing.T) {
			s, _, creator, ev := assembledCleanupFixture(t)
			ctx := context.Background()
			switch name {
			case "creator-request":
				ev.Review.OldRequestSHA256 = strings.Repeat("0", 64)
			case "creator-evidence":
				ev.Review.OldEvidenceSHA256 = strings.Repeat("0", 64)
			case "expiry":
				ev.Review.ExpiresAt = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
			}
			_, err := s.BeginAssembledCreateCleanup(ctx, DiscardRequest{Key: "cleanup", UUID: creator.SessionUUID, TokenSHA256: strings.Repeat("d", 64)}, ev, func(context.Context) error {
				if name == "verify-race" {
					return s.AdvanceOperation(ctx, creator.ID, "running", creator.Phase, true, creator.Evidence, "")
				}
				return nil
			})
			if !errors.Is(err, ErrConflict) {
				t.Fatal("unsupported/stale source review admitted", err)
			}
			session, _ := s.GetSession(ctx, creator.SessionUUID)
			if session.Registry != "creating" {
				t.Fatal("refusal lost creating assignment")
			}
		})
	}
	s, _, creator, ev := assembledCleanupFixture(t)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	var admitErr, retryErr error
	go func() {
		defer wg.Done()
		<-start
		_, admitErr = s.BeginAssembledCreateCleanup(context.Background(), DiscardRequest{Key: "cleanup", UUID: creator.SessionUUID, TokenSHA256: strings.Repeat("d", 64)}, ev, func(context.Context) error { return nil })
	}()
	go func() {
		defer wg.Done()
		<-start
		retryErr = s.AdvanceOperation(context.Background(), creator.ID, "running", creator.Phase, true, creator.Evidence, "")
	}()
	close(start)
	wg.Wait()
	if (admitErr == nil) == (retryErr == nil) {
		t.Fatalf("cleanup/retry did not elect one authority: %v %v", admitErr, retryErr)
	}
}
