package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/control"
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
	ev := control.DiscardEvidence{Action: "discard", Project: s.Project, Branch: s.Branch, AssignedTip: strings.Repeat("e", 40), PolicySHA256: s.PolicySHA256, InstanceUUID: instance, BaseFingerprint: strings.Repeat("d", 64), MissingRuntime: true, PRefsDigest: strings.Repeat("1", 64)}
	removal, err := f.m.store.BeginDiscard(f.ctx, control.DiscardRequest{Key: "discard-replayed", UUID: s.UUID, IfMatch: futureTag(time.Minute)}, ev, strings.Repeat("c", 64), noRecheck)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.m.store.SetGitRefGuard(f.ctx, s.Project, s.Branch, removal.ID, true); err != nil {
		t.Fatal(err)
	}
	if err = f.m.store.AdvanceOperation(f.ctx, removal.ID, "running", "validated", false, removal.Evidence, "", ""); err != nil {
		t.Fatal(err)
	}
	if err = f.m.store.CommitDiscard(f.ctx, removal.ID, func(context.Context, control.DiscardEvidence) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err = f.m.store.AdvanceOperation(f.ctx, removal.ID, "running", "secrets-absent", true, removal.Evidence, "", ""); err != nil {
		t.Fatal(err)
	}
	if err = f.m.store.CompleteDiscard(f.ctx, removal.ID, func(context.Context, string, string, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	_, sessions, operations, err := f.m.store.Count(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The source driver is down and the worker map absent: completed replay
	// would fail if it checked names, captured the source, inspected
	// capacity or admitted a worker.
	_, source := memorySource(t)
	source.Kill()
	l := &lifecycle{ctx: f.ctx, store: f.m.store, git: &gitCapability{driver: source}, cfg: control.RuntimeConfig{ProjectPolicies: map[string]control.ProjectPolicy{}}}
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
