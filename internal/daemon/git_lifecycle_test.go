package daemon

import (
	"context"
	"testing"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
)

// TestGitOnlyDaemonRunsOnlyOriginOperations: a daemon with Git and no
// runtime runs the journal's origin operations; other kinds it neither
// resumes nor retries.
func TestGitOnlyDaemonRunsOnlyOriginOperations(t *testing.T) {
	f := newJournalFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := closedLifecycle(ctx, f.store)
	l.gitOnly = true
	change := f.operation(t, control.KindOriginChange, "running", "", "", false)
	publish := f.operation(t, control.KindOriginPublish, "running", "", "", false)
	rename := f.operation(t, control.KindSessionRename, "running", "", sessionUUID(1), false)
	blocked := f.operation(t, control.KindSessionDiscard, "blocked", "", sessionUUID(2), false)
	if err := l.resumeRunning(); err != nil {
		t.Fatal(err)
	}
	if got := l.queued(); len(got) != 2 || got[0] != change || got[1] != publish {
		t.Fatalf("queued %q, want the origin operations only (not %s)", got, rename)
	}
	if _, err := l.Retry(ctx, blocked); !refusedAs(err, api.KindNotConfigured, api.ReasonRuntime) {
		t.Fatalf("Retry of a runtime kind answered %v", err)
	}
}

func refusedAs(err error, kind api.ErrorKind, reason string) bool {
	refusal, ok := err.(*control.Refusal)
	return ok && refusal.Kind == kind && refusal.Reason == reason
}
