package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
)

// These tests state start and stop as operations where no runtime is needed:
// what the journal answers. VM scenarios cover the runtime effects.

// changeFixture is a journal with established session sessionUUID(1), its
// completed creation, and a lifecycle observing it.
func changeFixture(t *testing.T) (*journalFixture, *lifecycle, context.Context, string) {
	t.Helper()
	f := newJournalFixture(t)
	l, ctx := observedLifecycle(t, f)
	session := sessionUUID(1)
	// A session whose policy and creation evidence hold, so its work reaches
	// the runtime.
	policy, sha, err := control.ProjectPolicySnapshot(control.ProjectPolicy{Network: "none", FilesystemMounts: []control.FilesystemGrant{}, Command: []string{"/bin/sh"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.raw.Exec(`INSERT INTO sessions(uuid,project_path,branch,registry_state,policy_json,policy_sha256) VALUES(?,'app','work','established',?,?)`,
		session, string(policy), sha); err != nil {
		t.Fatal(err)
	}
	evidence, _ := json.Marshal(control.CreationEvidence{CapturedOID: strings.Repeat("a", 40), ImageFingerprint: strings.Repeat("d", 64), RuntimeInitState: "attempted"})
	f.operationWith(t, "app", "session.create", "completed", "established", session, true, "{}", string(evidence))
	return f, l, ctx, session
}

func TestUnfinishedStartOrStopIsTheSessionsCondition(t *testing.T) {
	for kind, want := range map[string]string{control.KindSessionStart: "starting", control.KindSessionStop: "stopping"} {
		t.Run(kind, func(t *testing.T) {
			f, l, ctx, session := changeFixture(t)
			id := f.operation(t, kind, "running", "", session, false)
			view, err := l.InspectSession(ctx, session)
			if err != nil || view.Condition != want {
				t.Fatalf("condition %q %v, want %s", view.Condition, err, want)
			}
			// The same kind sent again returns the unfinished one, unchanged;
			// the other kind competes with it.
			same, other := l.StartSession, l.StopSession
			if kind == control.KindSessionStop {
				same, other = l.StopSession, l.StartSession
			}
			result, err := same(ctx, session)
			if err != nil || result.Changed || result.Operation == nil || result.Operation.ID != id || result.Session != nil {
				t.Fatalf("repeat answered %+v %v", result, err)
			}
			if _, err = other(ctx, session); !refusedNaming(err, api.KindRetry, id) {
				t.Fatalf("the other kind answered %v", err)
			}
			if _, err = l.AttachSession(ctx, session); !refusedNaming(err, api.KindRetry, id) {
				t.Fatalf("attach answered %v", err)
			}
		})
	}
}

// TestStartWorkerRecordsTheRuntimeFailure runs a start without a runtime
// plugin: the runtime fails, the operation ends failed with reason runtime
// and its diagnostic, and the worker lets go of the session.
func TestStartWorkerRecordsTheRuntimeFailure(t *testing.T) {
	f, l, ctx, session := changeFixture(t)
	id := f.operation(t, control.KindSessionStart, "running", "", session, false)
	l.process(id, false)
	op, err := f.store.GetOperation(ctx, id)
	if err != nil || op.Status != "failed" || op.Reason != api.ReasonRuntime || op.Diagnostic == "" {
		t.Fatalf("start ended %+v %v", op, err)
	}
	release, err := l.lockSession(ctx, session)
	if err != nil {
		t.Fatalf("the worker still held the session: %v", err)
	}
	release()
}

// TestFailedStartExplainsASessionThatIsNotReady: the newest start failed, so
// its diagnostic is the session's while the session is not ready, even when
// the runtime cannot be observed.
func TestFailedStartExplainsASessionThatIsNotReady(t *testing.T) {
	f, l, ctx, session := changeFixture(t)
	id := f.operation(t, control.KindSessionStart, "running", "", session, false)
	if err := f.store.AdvanceOperation(ctx, id, "failed", "starting", false, nil, api.ReasonRuntime, "host did not become ready"); err != nil {
		t.Fatal(err)
	}
	view, err := l.InspectSession(ctx, session)
	if err != nil || view.Condition != "unreachable" || view.Diagnostic != "host did not become ready" {
		t.Fatalf("view %q %q %v", view.Condition, view.Diagnostic, err)
	}
}
