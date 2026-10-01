package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func assembledCleanupFixture(t *testing.T) (*Store, string, Operation, CreateCleanupEvidence) {
	t.Helper()
	s, dir, creator, inspectEv := assembledLossFixture(t)
	ctx := context.Background()
	inspectEv.Result = json.RawMessage(`{"schema":"p.workspace-loss/v1","fingerprint":"` + strings.Repeat("f", 64) + `","runtime_data_will_be_removed":true,"worktrees":[{"path":"/workspace"}],"p_refs":[]}`)
	result := inspectEv.Result
	inspectEv.Result = nil
	inspected, err := s.BeginWorkspaceLossInspect(ctx, WorkspaceInspectRequest{Key: "loss", SessionUUID: creator.SessionUUID}, inspectEv)
	if err != nil {
		t.Fatal(err)
	}
	inspectEv.Result = result
	raw, _ := json.Marshal(inspectEv)
	if err = s.AdvanceOperation(ctx, inspected.ID, "completed", "inspected", true, raw, ""); err != nil {
		t.Fatal(err)
	}
	inspected, _ = s.GetOperation(ctx, inspected.ID)
	session, _ := s.GetSession(ctx, creator.SessionUUID)
	creation, _ := Evidence(creator)
	var request ReserveSessionRequest
	_ = json.Unmarshal(creator.Request, &request)
	local := replacementCleanupIntent(t, creator, creation.ImageFingerprint)
	if err = s.RegisterGitPrincipal(ctx, local.KeyFingerprint, "session", creator.Project, creator.SessionUUID); err != nil {
		t.Fatal(err)
	}
	runtime := &RemovalRuntimePreview{Condition: "present", IncusProject: "user-1000", InstanceName: "p-" + creator.SessionUUID, IncusUUID: inspectEv.SourceIncusUUID,
		Generation: inspectEv.SourceGeneration, ImageFingerprint: creation.ImageFingerprint, OriginalStatus: "Stopped", LossOperationID: inspected.ID,
		ObservedAt: inspected.UpdatedAt, Fingerprint: strings.Repeat("f", 64), Loss: result}
	ev := CreateCleanupEvidence{InstanceUUID: inspectEv.InstanceUUID, Review: CreateCleanupPreview{UUID: creator.SessionUUID, OldOperationID: creator.ID, OldRequest: request,
		OldPhase: creator.Phase, OldEvidenceSHA256: digest(creator.Evidence), OldRequestSHA256: digest(creator.Request), PolicySHA256: session.PolicySHA256,
		AssignedBranch: CreateReplaceBranch{Ref: "refs/heads/work", Observed: true, Exists: true, OID: creation.CapturedOID}, ImageFingerprint: creation.ImageFingerprint,
		Runtime: runtime, Provisional: CreateReplaceResources{Cleanup: local}, LossWarnings: []string{"workspace", "private files and credentials", "host credentials"}, Eligible: true,
		ExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}}
	return s, dir, creator, ev
}

func beginAssembledCleanup(t *testing.T, s *Store, ev CreateCleanupEvidence) Operation {
	t.Helper()
	op, err := s.BeginAssembledCreateCleanup(context.Background(), DiscardRequest{Key: "cleanup", UUID: ev.Review.UUID, TokenSHA256: strings.Repeat("d", 64)}, ev, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func advanceAssembledToValidated(t *testing.T, s *Store, op Operation) Operation {
	t.Helper()
	for _, phase := range []string{"init-issued", "helper-ready", "quiescent", "analyzed", "validated"} {
		if err := s.AdvanceOperation(context.Background(), op.ID, "running", phase, false, op.Evidence, ""); err != nil {
			t.Fatal(phase, err)
		}
	}
	op, _ = s.GetOperation(context.Background(), op.ID)
	return op
}

func TestAssembledCleanupSQLiteReopenAtomicRetirementAndForwardCompletion(t *testing.T) {
	s, dir, creator, ev := assembledCleanupFixture(t)
	ctx := context.Background()
	op := beginAssembledCleanup(t, s, ev)
	if op.Committed || op.Phase != "helper-intent" {
		t.Fatal("cleanup crossed authority boundary before fresh loss")
	}
	if err := s.AdvanceOperation(ctx, creator.ID, "running", creator.Phase, true, creator.Evidence, ""); !errors.Is(err, ErrConflict) {
		t.Fatal("creator retry bypassed pending cleanup", err)
	}
	if err := s.CompleteCreation(ctx, creator.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("pending cleanup promoted creator", err)
	}
	if _, err := s.BeginWorkspaceLossInspect(ctx, WorkspaceInspectRequest{Key: "overlap", SessionUUID: creator.SessionUUID}, assembledCleanupWorkspaceProof(ev)); !errors.Is(err, ErrConflict) {
		t.Fatal("inspection overlapped confirmed read guard", err)
	}
	op = advanceAssembledToValidated(t, s, op)
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
	if _, active, err := s.ActiveWorkspaceInspect(ctx, creator.SessionUUID); err != nil || active {
		t.Fatal("completed cleanup retained guard", err)
	}
}

func assembledCleanupWorkspaceProof(ev CreateCleanupEvidence) WorkspaceInspectEvidence {
	r := ev.Review.Runtime
	return WorkspaceInspectEvidence{InstanceUUID: ev.InstanceUUID, BaseFingerprint: ev.Review.ImageFingerprint, ImageFingerprint: r.ImageFingerprint,
		SourceIncusUUID: r.IncusUUID, SourceGeneration: r.Generation, OriginalStatus: "Stopped", CreatorOperationID: ev.Review.OldOperationID,
		CreatorRequestSHA256: ev.Review.OldRequestSHA256, CreatorEvidenceSHA256: ev.Review.OldEvidenceSHA256}
}

func TestAssembledCleanupStalePrecommitReleasesOnlyItsOwnGuardAcrossRestart(t *testing.T) {
	s, dir, creator, ev := assembledCleanupFixture(t)
	ctx := context.Background()
	op := beginAssembledCleanup(t, s, ev)
	if err := s.AdvanceOperation(ctx, op.ID, "blocked", "stale-cleanup", false, op.Evidence, "native helper cleanup unavailable"); err != nil {
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
		t.Fatal("unsettled helper lost creator guard", err)
	}
	if err = s.FailStaleAssembledCleanup(ctx, op, "review fresh loss"); err != nil {
		t.Fatal(err)
	}
	old, _ := s.GetOperation(ctx, creator.ID)
	session, _ := s.GetSession(ctx, creator.SessionUUID)
	active, _ := s.IsSessionGitPrincipalActive(ctx, creator.SessionUUID)
	if old.Status != "blocked" || session.Registry != "creating" || !active {
		t.Fatal("pre-deletion stale refusal changed original creator authority")
	}
	if _, active, err := s.ActiveWorkspaceInspect(ctx, creator.SessionUUID); err != nil || active {
		t.Fatal("settled stale precommit guard retained", err)
	}
	if err = s.AdvanceOperation(ctx, op.ID, "running", "helper-intent", false, op.Evidence, ""); !errors.Is(err, ErrConflict) {
		t.Fatal("stale confirmation restarted", err)
	}
	if _, err = s.BeginWorkspaceLossInspect(ctx, WorkspaceInspectRequest{Key: "fresh-loss", SessionUUID: creator.SessionUUID}, assembledCleanupWorkspaceProof(ev)); err != nil {
		t.Fatal("fresh review was stranded", err)
	}
}

func TestAssembledCleanupRefusesUnboundReviewAndRacesOriginalRetry(t *testing.T) {
	for _, name := range []string{"creator-request", "creator-evidence", "native-uuid", "generation", "loss-bytes", "loss-id", "expiry", "verify-race"} {
		t.Run(name, func(t *testing.T) {
			s, _, creator, ev := assembledCleanupFixture(t)
			ctx := context.Background()
			switch name {
			case "creator-request":
				ev.Review.OldRequestSHA256 = strings.Repeat("0", 64)
			case "creator-evidence":
				ev.Review.OldEvidenceSHA256 = strings.Repeat("0", 64)
			case "native-uuid":
				ev.Review.Runtime.IncusUUID = ev.InstanceUUID
			case "generation":
				ev.Review.Runtime.Generation = ev.InstanceUUID
			case "loss-bytes":
				ev.Review.Runtime.Loss = json.RawMessage(`{}`)
			case "loss-id":
				ev.Review.Runtime.LossOperationID = ev.InstanceUUID
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
