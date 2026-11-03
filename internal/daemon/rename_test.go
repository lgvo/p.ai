package daemon

import (
	"testing"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

func TestRenameRequiresAStoppedSession(t *testing.T) {
	stopped := runtimeincus.Observation{Exists: true, Status: "Stopped", IncusUUID: "22222222-2222-4222-8222-222222222222", Generation: "33333333-3333-4333-8333-333333333333"}
	if !renameSourceStopped(stopped) {
		t.Fatal("a stopped session was refused")
	}
	for _, status := range []string{"Running", "Frozen", "Error"} {
		o := stopped
		o.Status = status
		if renameSourceStopped(o) {
			t.Fatalf("a %s session was accepted", status)
		}
	}
	ev := control.RenameEvidence{IncusUUID: stopped.IncusUUID, Generation: stopped.Generation}
	if !renameSourceExact(stopped, ev) {
		t.Fatal("the recorded stopped runtime was refused")
	}
	started := stopped
	started.Status = "Running"
	replaced := stopped
	replaced.IncusUUID = "44444444-4444-4444-8444-444444444444"
	for name, o := range map[string]runtimeincus.Observation{"started": started, "replaced": replaced} {
		if renameSourceExact(o, ev) {
			t.Fatalf("a %s runtime was taken for the recorded one", name)
		}
	}
}
