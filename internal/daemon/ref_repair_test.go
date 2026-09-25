package daemon

import (
	"errors"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

func TestRefRepairQuiescedReplayRejectsSameNamedReplacement(t *testing.T) {
	ev := control.RefRepairEvidence{InstanceName: "p-550e8400-e29b-41d4-a716-446655440000",
		IncusUUID:  "11111111-1111-4111-8111-111111111111",
		Generation: "22222222-2222-4222-8222-222222222222", ImageFingerprint: strings.Repeat("a", 64)}
	for _, status := range []string{"Stopped", "Frozen"} {
		exact := runtimeincus.Observation{Exists: true, Name: ev.InstanceName, IncusUUID: ev.IncusUUID,
			Generation: ev.Generation, Status: status, Fingerprint: ev.ImageFingerprint}
		if err := refRepairSourceMatches(exact, ev, status); err != nil {
			t.Fatalf("exact %s source refused: %v", status, err)
		}
		for _, changed := range []struct {
			name string
			edit func(*runtimeincus.Observation)
		}{
			{"same-name replacement", func(o *runtimeincus.Observation) { o.IncusUUID = "33333333-3333-4333-8333-333333333333" }},
			{"restored generation", func(o *runtimeincus.Observation) { o.Generation = "44444444-4444-4444-8444-444444444444" }},
			{"different status", func(o *runtimeincus.Observation) { o.Status = "Running" }},
			{"different image", func(o *runtimeincus.Observation) { o.Fingerprint = strings.Repeat("b", 64) }},
		} {
			observed := exact
			changed.edit(&observed)
			if err := refRepairSourceMatches(observed, ev, status); !errors.Is(err, control.ErrConflict) {
				t.Fatalf("%s %s admitted for ref effect: %v", status, changed.name, err)
			}
		}
	}
}

func TestRefRepairRequiresCompletedInspectedLossSnapshot(t *testing.T) {
	const uuid = "550e8400-e29b-41d4-a716-446655440000"
	op := control.Operation{Kind: "workspace.loss.inspect", Status: "completed",
		Phase: "inspected", SessionUUID: uuid, Project: "repair-project"}
	if !completedRefRepairLoss(op, uuid, "repair-project") {
		t.Fatal("actual terminal workspace loss operation refused")
	}
	for _, changed := range []control.Operation{
		func() control.Operation { v := op; v.Phase = "completed"; return v }(),
		func() control.Operation { v := op; v.Status = "running"; return v }(),
		func() control.Operation { v := op; v.Kind = "workspace.inspect"; return v }(),
		func() control.Operation { v := op; v.SessionUUID = "11111111-1111-4111-8111-111111111111"; return v }(),
		func() control.Operation { v := op; v.Project = "other-project"; return v }(),
	} {
		if completedRefRepairLoss(changed, uuid, "repair-project") {
			t.Fatalf("nonmatching loss operation accepted: %+v", changed)
		}
	}
}
