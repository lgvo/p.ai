package control

import "github.com/lgvo/p.ai/internal/api"

// SameCreateSelection ignores only the idempotency key when detecting changes.
func SameCreateSelection(a, b ReserveSessionRequest) bool {
	a.Key, b.Key = "", ""
	return a == b
}

// ReplaceableCreationEvidence permits local choices without builder/publication
// evidence; the checkpoint and reviewed local resources are checked separately.
// RefCASIntent records a planned Git CAS; replacement never resets/deletes it.
func ReplaceableCreationEvidence(req ReserveSessionRequest, ev CreationEvidence) bool {
	return ValidSessionCreateRequest(req) && req.OriginRef == "" && ev.OriginURL == "" && ev.OriginRef == "" &&
		ev.BranchExisted == (req.Choice == "existing") && ev.RefCASIntent == (req.Choice == "new") &&
		ev.BuilderTreeOID == "" && ev.EnvironmentState == nil && (ev.RuntimeInitState == "" || ev.RuntimeInitState == "not-attempted")
}

// ReplaceableCreationPhase names the bounded local checkpoints, with a durable
// committed marker appropriate to each. Principals require base-image evidence.
func ReplaceableCreationPhase(op api.Operation, ev CreationEvidence) bool {
	switch op.Phase {
	case "source-ready":
		return !op.Committed
	case "branch-assigned":
		return op.Committed
	case "principals-ready":
		return op.Committed && ev.Environment == nil && ev.RuntimeInitState == "not-attempted"
	default:
		return false
	}
}
