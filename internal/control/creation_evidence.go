package control

import "encoding/json"

// A stale Retry/status writer must never erase native dispatch provenance or
// approved replacement cleanup. Enforcement shares AdvanceOperation's SQLite
// transaction with its status/committed checks, including across processes.
func monotonicCreationEvidence(oldPhase string, oldRaw, newRaw json.RawMessage) error {
	var old, next CreationEvidence
	if len(oldRaw) > 0 && json.Unmarshal(oldRaw, &old) != nil || len(newRaw) > 0 && json.Unmarshal(newRaw, &next) != nil {
		return ErrInvalid
	}
	if next.RuntimeInitState != "" && next.RuntimeInitState != "not-attempted" && next.RuntimeInitState != "attempted" {
		return ErrInvalid
	}
	if old.RuntimeInitState == "attempted" && next.RuntimeInitState != "attempted" || old.RuntimeInitState == "not-attempted" && next.RuntimeInitState == "" {
		return ErrConflict
	}
	if old.RuntimeInitState == "" && next.RuntimeInitState == "not-attempted" {
		switch oldPhase {
		case "source-ready", "branch-assigned", "environment-publishing", "environment-ready":
		default:
			return ErrConflict
		}
	}
	if old.EnvironmentBuilder == nil && next.EnvironmentBuilder != nil && old.BuilderTreeOID != "" {
		return ErrConflict
	}
	if err := monotonicBuilderState(old.EnvironmentBuilder, next.EnvironmentBuilder); err != nil {
		return err
	}
	if old.ReplacementCleanup != nil {
		if next.ReplacementCleanup == nil {
			return ErrConflict
		}
		a, b := *old.ReplacementCleanup, *next.ReplacementCleanup
		if a.Completed && !b.Completed {
			return ErrConflict
		}
		a.Completed, b.Completed = false, false
		if a != b {
			return ErrConflict
		}
	}
	return nil
}
