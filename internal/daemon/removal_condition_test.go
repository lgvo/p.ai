package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
)

func TestRemovalConditionInspectEventsAndRestart(t *testing.T) {
	for _, action := range []string{"discard", "delete"} {
		for _, status := range []string{"running", "blocked"} {
			t.Run(action+"/"+status, func(t *testing.T) {
				f := newSessionAdmissionFixture(t)
				s, err := f.m.store.GetSession(f.ctx, f.sessions[0].UUID)
				if err != nil {
					t.Fatal(err)
				}
				events := make(chan plugin.Event, 8)
				d := testDelivery(f.ctx, func(_ context.Context, e plugin.Event) error { events <- e; return nil })
				defer d.Close()
				policy := control.ProjectPolicy{Network: "none", FilesystemMounts: []control.FilesystemGrant{}, Command: []string{"/bin/sh"}}
				l := &lifecycle{ctx: f.ctx, store: f.m.store, events: d, cfg: control.RuntimeConfig{ProjectPolicies: map[string]control.ProjectPolicy{s.Project: policy}}}
				l.seedRecoveredSession(s, "current")
				l.observeView(control.SessionView{UUID: s.UUID, Project: s.Project, Branch: s.Branch, Condition: "stopped", PolicyCondition: "current"})
				instance, err := f.m.store.InstanceID(f.ctx)
				if err != nil {
					t.Fatal(err)
				}
				ev := control.DiscardEvidence{Action: action, Project: s.Project, Branch: s.Branch, AssignedTip: strings.Repeat("e", 40), PolicySHA256: s.PolicySHA256, IncusProject: "user-1000", InstanceName: "p-" + s.UUID, InstanceUUID: instance, BaseFingerprint: strings.Repeat("d", 64), MissingRuntime: true, PRefsDigest: strings.Repeat("1", 64), PreviewExpiresAt: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}
				if action == "delete" {
					ev.DeleteReviewSHA256 = strings.Repeat("b", 64)
				}
				req := control.DiscardRequest{Key: "condition-" + action, UUID: s.UUID, TokenSHA256: strings.Repeat("a", 64)}
				var op control.Operation
				if action == "delete" {
					op, err = f.m.store.BeginDelete(f.ctx, req, ev)
				} else {
					op, err = f.m.store.BeginDiscard(f.ctx, req, ev)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = f.m.store.SetGitRefGuard(f.ctx, s.Project, s.Branch, op.ID, true); err != nil {
					t.Fatal(err)
				}
				if err = f.m.store.AdvanceOperation(f.ctx, op.ID, "running", "validated", false, op.Evidence, ""); err != nil {
					t.Fatal(err)
				}
				if action == "delete" {
					err = f.m.store.CommitDelete(f.ctx, op.ID, func(context.Context, control.DiscardEvidence) error { return nil })
				} else {
					err = f.m.store.CommitDiscard(f.ctx, op.ID, func(context.Context, control.DiscardEvidence) error { return nil })
				}
				if err != nil {
					t.Fatal(err)
				}
				if status == "blocked" {
					if err = f.m.store.AdvanceOperation(f.ctx, op.ID, "blocked", "removal-committed", true, op.Evidence, "fixture cleanup blocked"); err != nil {
						t.Fatal(err)
					}
				}
				want := "discarding"
				if action == "delete" {
					want = "deleting"
				}
				view, err := l.InspectSession(f.ctx, s.UUID)
				if err != nil || view.Condition != want {
					t.Fatalf("removal condition=%s want%s %v", view.Condition, want, err)
				}
				e := nextEvent(t, events)
				if e.Kind != "session.condition_changed" || e.Fields["condition"] != want {
					t.Fatalf("wrong condition transition: %+v", e)
				}
				stateDir := f.m.store.StateDir()
				if err = f.m.store.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := control.OpenStore(stateDir)
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				f.m.store = reopened
				ctx, cancel := context.WithCancel(f.ctx)
				defer cancel()
				// Keep operation execution queued while testing restart's read-only
				// seeding. This needs no runtime fixture and cannot mutate the removal.
				recovered := &lifecycle{ctx: ctx, store: reopened, endpoints: f.m, events: d, cfg: l.cfg, working: map[string]bool{}, queueSlots: make(chan struct{}), changedPolicies: map[string]bool{}}
				if err = recovered.Recover(); err != nil {
					t.Fatal(err)
				}
				f.m.mu.Lock()
				opened := f.m.opened[s.UUID] != nil
				f.m.mu.Unlock()
				if opened {
					t.Fatal("recovery reopened removing session endpoints")
				}
				recovered.eventMu.Lock()
				seed := recovered.observed[s.UUID].condition
				recovered.eventMu.Unlock()
				if seed != want {
					t.Fatalf("restart seeded %s want%s", seed, want)
				}
				view, err = recovered.InspectSession(ctx, s.UUID)
				if err != nil || view.Condition != want {
					t.Fatalf("reopened inspect=%s %v", view.Condition, err)
				}
				select {
				case e := <-events:
					t.Fatalf("restart manufactured event: %+v", e)
				case <-time.After(20 * time.Millisecond):
				}
			})
		}
	}
}
func TestRemovalConditionDoesNotInferMissingAuthority(t *testing.T) {
	f := newSessionAdmissionFixture(t)
	s := f.sessions[0]
	if err := f.m.store.AdvanceSessionRegistry(f.ctx, s.UUID, "established", "removing"); err != nil {
		t.Fatal(err)
	}
	l := &lifecycle{ctx: f.ctx, store: f.m.store}
	view, err := l.InspectSession(f.ctx, s.UUID)
	if err != nil || view.Condition != "unreachable" || view.Diagnostic == "" {
		t.Fatalf("missing intent inferred: %+v %v", view, err)
	}
	s.Registry = "removing"
	l.seedRecoveredSession(s, "current")
	if got := l.observed[s.UUID].condition; got != "unreachable" {
		t.Fatalf("missing intent seed=%s", got)
	}
	if _, err = l.registryCondition(f.ctx, control.Session{UUID: fmt.Sprintf("%s-bad", s.UUID), Registry: "removing"}); !errors.Is(err, control.ErrInvalid) {
		t.Fatalf("invalid identity inferred: %v", err)
	}
}
