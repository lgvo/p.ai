package control

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"

	"github.com/lgvo/p.ai/internal/api"
)

// CreationBuilderState tracks a creation's image builds across immutable
// retries. Each cycle is one build: attempted once its build may have started,
// abandoned once it is settled as not accepted while its leftovers may remain,
// absent once nothing of it remains. An attempted build whose outcome is
// unknown keeps its cycle until settled, and an abandoned one is never settled
// again: only an absent cycle lets another start.
type CreationBuilderState struct {
	Cycle uint64 `json:"cycle"`
	State string `json:"state"`
}

// BuildRef is the reference of a creation's build in one cycle: a version 4
// UUID derived from the operation and cycle, so a restarted daemon settles the
// same build and every cycle names a new one.
func BuildRef(operationID string, cycle uint64) string {
	sum := sha256.Sum256([]byte(operationID + "/" + strconv.FormatUint(cycle, 10)))
	b := sum[:16]
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func builderStateRank(s string) int {
	switch s {
	case "not-attempted":
		return 0
	case "attempted":
		return 1
	case "abandoned":
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
	if b == nil || b.Cycle < a.Cycle || b.Cycle == a.Cycle && builderStateRank(b.State) < builderStateRank(a.State) {
		return ErrConflict
	}
	if b.Cycle > a.Cycle && (b.Cycle != a.Cycle+1 || b.State != "attempted" || a.State != "absent" && a.State != "not-attempted") {
		return ErrConflict
	}
	return nil
}

func SafeCreateCleanupEvidence(req ReserveSessionRequest, ev CreationEvidence) bool {
	if !ValidSessionCreateRequest(req) || !validOID(ev.CapturedOID) || ev.BranchExisted != (req.Choice == "existing") || ev.RefCASIntent != (req.Choice == "new") || req.OriginRef != "" || ev.OriginURL != "" || ev.OriginRef != "" || ev.RuntimeInitState != "not-attempted" || ev.Image != nil || ev.ReplacementCleanup != nil && !ev.ReplacementCleanup.Completed {
		return false
	}
	b := ev.EnvironmentBuilder
	return b == nil || b.Valid() && (b.State == "not-attempted" || b.State == "absent" && ev.Environment != nil)
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
