package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
)

func TestCompletedCreateReplayDoesNotQueueRemovedSession(t *testing.T) {
	f := newSessionAdmissionFixture(t)
	s := f.sessions[0]
	req := control.ReserveSessionRequest{Key: "session-0", Project: s.Project, Branch: s.Branch, Choice: "existing"}
	original, err := f.m.store.GetOperationByKey(f.ctx, req.Key)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := f.m.store.InstanceID(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	ev := control.DiscardEvidence{Action: "discard", Project: s.Project, Branch: s.Branch, AssignedTip: strings.Repeat("e", 40), PolicySHA256: s.PolicySHA256, IncusProject: "user-1000", InstanceName: "p-" + s.UUID, InstanceUUID: instance, BaseFingerprint: strings.Repeat("d", 64), MissingRuntime: true, PRefsDigest: strings.Repeat("1", 64), PreviewExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}
	removal, err := f.m.store.BeginDiscard(f.ctx, control.DiscardRequest{Key: "discard-replayed", UUID: s.UUID, TokenSHA256: strings.Repeat("a", 64)}, ev)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.m.store.SetGitRefGuard(f.ctx, s.Project, s.Branch, removal.ID, true); err != nil {
		t.Fatal(err)
	}
	if err = f.m.store.AdvanceOperation(f.ctx, removal.ID, "running", "validated", false, removal.Evidence, ""); err != nil {
		t.Fatal(err)
	}
	if err = f.m.store.CommitDiscard(f.ctx, removal.ID, func(context.Context, control.DiscardEvidence) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err = f.m.store.AdvanceOperation(f.ctx, removal.ID, "running", "secrets-absent", true, removal.Evidence, ""); err != nil {
		t.Fatal(err)
	}
	if err = f.m.store.CompleteDiscard(f.ctx, removal.ID, func(context.Context, string, string, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	_, sessions, operations, err := f.m.store.Count(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The empty backend and absent worker map would fail if completed replay
	// attempted source capture, capacity inspection, or worker admission.
	l := &lifecycle{ctx: f.ctx, store: f.m.store, git: &gitCapability{backend: &gitservice.Backend{}}, cfg: control.RuntimeConfig{ProjectPolicies: map[string]control.ProjectPolicy{}}}
	got, err := l.CreateSession(f.ctx, req)
	if err != nil || got.ID != original.ID || got.Status != "completed" || got.SessionUUID != s.UUID {
		t.Fatalf("daemon replay failed: %+v %v", got, err)
	}
	if _, err = f.m.store.GetSession(f.ctx, s.UUID); !errors.Is(err, control.ErrNotFound) {
		t.Fatalf("daemon resurrected removed session: %v", err)
	}
	_, afterSessions, afterOperations, err := f.m.store.Count(f.ctx)
	if err != nil || afterSessions != sessions || afterOperations != operations {
		t.Fatalf("daemon replay changed records: %d/%d %d/%d %v", afterSessions, sessions, afterOperations, operations, err)
	}
}
