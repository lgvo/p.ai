package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

func assembledLossFixture(t *testing.T) (*Store, string, Operation, WorkspaceInspectEvidence) {
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
	if err = AssembledCreationForLoss(session, op, image); err != nil {
		t.Fatal(err)
	}
	ev := WorkspaceInspectEvidence{InstanceUUID: "33333333-3333-4333-8333-333333333333", ImageFingerprint: image, BaseFingerprint: image,
		SourceIncusUUID: "44444444-4444-4444-8444-444444444444", SourceGeneration: "55555555-5555-4555-8555-555555555555", OriginalStatus: "Stopped"}
	BindWorkspaceCreator(&ev, op)
	return s, dir, op, ev
}

func TestAssembledWorkspaceLossGuardBindsSourceAcrossSQLiteReopen(t *testing.T) {
	s, dir, creator, ev := assembledLossFixture(t)
	ctx := context.Background()
	req := WorkspaceInspectRequest{Key: "loss", SessionUUID: creator.SessionUUID}
	op, err := s.BeginWorkspaceLossInspect(ctx, req, ev)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = testScopedOpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, status := range []string{"running", "blocked", "unknown"} {
		if err = s.AdvanceOperation(ctx, op.ID, status, op.Phase, true, op.Evidence, ""); err != nil {
			t.Fatal(err)
		}
		if err = s.AdvanceOperation(ctx, creator.ID, "running", creator.Phase, true, creator.Evidence, ""); !errors.Is(err, ErrConflict) {
			t.Fatalf("creator retry erased durable %s inspection guard: %v", status, err)
		}
		if err = s.AdvanceOperation(ctx, creator.ID, "blocked", "principals-ready", true, creator.Evidence, "stale worker"); !errors.Is(err, ErrConflict) {
			t.Fatalf("stale worker bypassed %s inspection guard: %v", status, err)
		}
	}
	for _, change := range []func(*WorkspaceInspectEvidence){
		func(e *WorkspaceInspectEvidence) { e.CreatorOperationID = e.InstanceUUID },
		func(e *WorkspaceInspectEvidence) { e.CreatorRequestSHA256 = strings.Repeat("c", 64) },
		func(e *WorkspaceInspectEvidence) { e.CreatorEvidenceSHA256 = strings.Repeat("c", 64) },
		func(e *WorkspaceInspectEvidence) { e.SourceIncusUUID = e.InstanceUUID },
		func(e *WorkspaceInspectEvidence) { e.SourceGeneration = e.InstanceUUID },
	} {
		next := ev
		change(&next)
		raw, _ := json.Marshal(next)
		if err = s.AdvanceOperation(ctx, op.ID, "running", op.Phase, true, raw, ""); !errors.Is(err, ErrConflict) {
			t.Fatalf("inspection source binding changed: %v", err)
		}
	}
	if _, err = s.BeginWorkspaceLossInspect(ctx, WorkspaceInspectRequest{Key: "second", SessionUUID: creator.SessionUUID}, ev); !errors.Is(err, ErrConflict) {
		t.Fatalf("overlapping helper admitted: %v", err)
	}
	if _, err = s.BeginWorkspaceInspect(ctx, WorkspaceInspectRequest{Key: "ordinary", SessionUUID: creator.SessionUUID}, ev); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ordinary workspace inspect accepted creator: %v", err)
	}
	if err = s.CompleteCreation(ctx, creator.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("creation promoted inspected session: %v", err)
	}
	for _, query := range []string{
		`UPDATE sessions SET branch='changed' WHERE uuid=?`,
		`UPDATE sessions SET registry_state='established' WHERE uuid=?`,
		`DELETE FROM sessions WHERE uuid=?`,
	} {
		if _, err = s.db.ExecContext(ctx, query, creator.SessionUUID); err == nil {
			t.Fatalf("schema released assignment during inspection: %s", query)
		}
	}
	var original ReserveSessionRequest
	_ = json.Unmarshal(creator.Request, &original)
	// Store replay preserves identity. Daemon enqueue is separately fenced.
	replay, session, err := s.BeginSessionCreate(ctx, original, "", CreationSelection{}, func(context.Context, ReserveSessionRequest) (string, bool, error) {
		t.Fatal("exact replay recaptured source")
		return "", false, nil
	})
	if err != nil || replay.ID != creator.ID || replay.Status != "blocked" || session.Registry != "creating" || session.Branch != "work" {
		t.Fatalf("guard changed original creator or assignment: %+v %+v %v", replay, session, err)
	}
	if err = s.AdvanceOperation(ctx, op.ID, "failed", "cleaned", true, op.Evidence, "unsupported layout after exact helper cleanup"); err != nil {
		t.Fatal(err)
	}
	if _, active, err := s.ActiveWorkspaceInspect(ctx, creator.SessionUUID); err != nil || active {
		t.Fatalf("settled read retained guard: %v %v", active, err)
	}
	if err = s.AdvanceOperation(ctx, creator.ID, "running", creator.Phase, true, creator.Evidence, ""); err != nil {
		t.Fatalf("exact retry did not regain authority after cleaned inspection: %v", err)
	}
}

func TestAssembledWorkspaceLossTransactionRejectsChangedCreatorAndUnsupportedStates(t *testing.T) {
	for _, name := range []string{"request-hash", "evidence-hash", "wrong-id", "legacy-init", "not-attempted", "early", "workspace-ready", "running", "bootstrap", "origin", "builder", "environment", "publication", "captured-oid", "policy", "image", "selection", "replacement"} {
		t.Run(name, func(t *testing.T) {
			s, _, op, ev := assembledLossFixture(t)
			ctx := context.Background()
			creation, _ := Evidence(op)
			switch name {
			case "request-hash":
				ev.CreatorRequestSHA256 = strings.Repeat("c", 64)
			case "evidence-hash":
				ev.CreatorEvidenceSHA256 = strings.Repeat("c", 64)
			case "wrong-id":
				ev.CreatorOperationID = ev.InstanceUUID
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
				creation.SupersedesOperationID = ev.InstanceUUID
			}
			op.Evidence, _ = json.Marshal(creation)
			// Simulate historical/future durable records rather than teaching
			// AdvanceOperation to erase provenance just to construct a fixture.
			if _, err := s.db.ExecContext(ctx, `DROP TRIGGER immutable_operation_request`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.ExecContext(ctx, `UPDATE operations SET kind=?,status=?,phase=?,evidence_json=? WHERE id=?`, op.Kind, op.Status, op.Phase, string(op.Evidence), op.ID); err != nil {
				t.Fatal(err)
			}
			if name != "request-hash" && name != "evidence-hash" && name != "wrong-id" {
				BindWorkspaceCreator(&ev, op)
			}
			if _, err := s.BeginWorkspaceLossInspect(ctx, WorkspaceInspectRequest{Key: "loss", SessionUUID: op.SessionUUID}, ev); !errors.Is(err, ErrConflict) {
				t.Fatalf("unsupported creator admitted: %v", err)
			}
			if _, active, err := s.ActiveWorkspaceInspect(ctx, op.SessionUUID); err != nil || active {
				t.Fatal("refusal installed helper guard", err)
			}
		})
	}
}

func TestAssembledWorkspaceLossRacesCreatorRetryTransaction(t *testing.T) {
	s, _, creator, ev := assembledLossFixture(t)
	ctx := context.Background()
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	var inspected Operation
	var inspectErr, retryErr error
	go func() {
		defer wg.Done()
		<-start
		inspected, inspectErr = s.BeginWorkspaceLossInspect(ctx, WorkspaceInspectRequest{Key: "loss", SessionUUID: creator.SessionUUID}, ev)
	}()
	go func() {
		defer wg.Done()
		<-start
		retryErr = s.AdvanceOperation(ctx, creator.ID, "running", creator.Phase, true, creator.Evidence, "")
	}()
	close(start)
	wg.Wait()
	if inspectErr == nil && retryErr == nil {
		t.Fatal("retry and inspection both acquired creator authority")
	}
	if inspectErr != nil && retryErr != nil {
		t.Fatalf("neither contender acquired authority: %v %v", inspectErr, retryErr)
	}
	if inspectErr == nil {
		if !errors.Is(retryErr, ErrConflict) || inspected.ID == "" {
			t.Fatal("inspection did not fence retry", retryErr)
		}
	} else if !errors.Is(inspectErr, ErrConflict) {
		t.Fatal(inspectErr)
	}
}

func TestAssembledCreationForLossExistingBranchAndStandalonePolicyBoundary(t *testing.T) {
	s, _, op, ev := assembledLossFixture(t)
	session, err := s.GetSession(context.Background(), op.SessionUUID)
	if err != nil {
		t.Fatal(err)
	}
	var req ReserveSessionRequest
	_ = json.Unmarshal(op.Request, &req)
	req.Choice, req.Source = "existing", ""
	op.Request, _ = json.Marshal(req)
	creation, _ := Evidence(op)
	creation.BranchExisted, creation.RefCASIntent = true, false
	op.Evidence, _ = json.Marshal(creation)
	if err = AssembledCreationForLoss(session, op, ev.BaseFingerprint); err != nil {
		t.Fatalf("ordinary existing branch refused: %v", err)
	}
	for _, policy := range []ProjectPolicy{
		{Network: "public-egress", PublicEgressSHA256: ev.BaseFingerprint, Command: []string{"/bin/false"}},
		{Network: "none", Command: []string{"/bin/false"}, FilesystemMounts: []FilesystemGrant{{Name: "data", Source: "/var/lib/p-vm/grants/data", Type: "directory", Access: "read-only", SourceIdentity: &SourceIdentity{Device: 1, Inode: 2}}}},
	} {
		session.Policy, session.PolicySHA256, err = ProjectPolicySnapshot(policy)
		if err != nil {
			t.Fatal(err)
		}
		creation.PolicySHA256 = session.PolicySHA256
		op.Evidence, _ = json.Marshal(creation)
		if err = AssembledCreationForLoss(session, op, ev.BaseFingerprint); !errors.Is(err, ErrConflict) {
			t.Fatalf("non-standalone policy admitted: %v", err)
		}
	}
}
