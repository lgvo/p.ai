package control

import (
	"context"

	"github.com/lgvo/p.ai/internal/api"
)

// CreationBuilderState tracks a settled owned builder across immutable retries.
// An attempted-but-absent builder can still represent delayed native admission.
type CreationBuilderState struct {
	Cycle uint64 `json:"cycle"`
	State string `json:"state"`
}

func builderStateRank(s string) int {
	switch s {
	case "not-attempted":
		return 0
	case "attempted":
		return 1
	case "owned":
		return 2
	case "absent":
		return 3
	}
	return -1
}
func (s CreationBuilderState) Valid() bool {
	return builderStateRank(s.State) >= 0 && (s.State == "not-attempted" && s.Cycle == 0 || s.State != "not-attempted" && s.Cycle > 0)
}
func monotonicBuilderState(a, b *CreationBuilderState) error {
	if b != nil && !b.Valid() {
		return ErrInvalid
	}
	if a == nil {
		if b != nil && (b.Cycle != 0 || b.State != "not-attempted") {
			return ErrConflict
		}
		return nil
	}
	if b == nil || b.Cycle < a.Cycle || b.Cycle == a.Cycle && (builderStateRank(b.State) < builderStateRank(a.State) || builderStateRank(b.State) > builderStateRank(a.State)+1) {
		return ErrConflict
	}
	if b.Cycle > a.Cycle && (b.Cycle != a.Cycle+1 || b.State != "attempted" || a.State != "absent" && a.State != "not-attempted") {
		return ErrConflict
	}
	return nil
}

func SafeCreateCleanupEvidence(req ReserveSessionRequest, ev CreationEvidence) bool {
	if !ValidSessionCreateRequest(req) || !validOID(ev.CapturedOID) || ev.BranchExisted != (req.Choice == "existing") || ev.RefCASIntent != (req.Choice == "new") || req.OriginRef != "" || ev.OriginURL != "" || ev.OriginRef != "" || ev.RuntimeInitState != "not-attempted" || ev.EnvironmentState != nil || ev.ReplacementCleanup != nil && !ev.ReplacementCleanup.Completed {
		return false
	}
	if ev.BuilderTreeOID == "" {
		return ev.EnvironmentBuilder == nil || ev.EnvironmentBuilder.Valid() && ev.EnvironmentBuilder.State == "not-attempted"
	}
	return validOID(ev.BuilderTreeOID) && ev.Environment != nil && ev.EnvironmentBuilder != nil && ev.EnvironmentBuilder.Valid() && (ev.EnvironmentBuilder.State == "not-attempted" || ev.EnvironmentBuilder.State == "absent")
}
func SafeCreateCleanupPhase(op api.Operation) bool {
	return op.Phase == "source-ready" && !op.Committed || (op.Phase == "branch-assigned" || op.Phase == "principals-ready") && op.Committed
}

type CreateCleanupEvidence struct {
	Review        api.CreateCleanupPreview `json:"review"`
	InstanceUUID  string                   `json:"instance_uuid"`
	LocalComplete bool                     `json:"local_complete,omitempty"`
	RuntimeAbsent bool                     `json:"runtime_absent,omitempty"`
}
type CreateCleanupAPI interface {
	PreviewCreateCleanup(context.Context, string) (api.CreateCleanupPreview, error)
	ConfirmCreateCleanup(context.Context, string, string, string) (api.Operation, error)
}
