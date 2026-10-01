package daemon

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/lgvo/p.ai/internal/control"
)

func prepareCreationInitState(op *control.Operation) error {
	ev, err := control.Evidence(*op)
	if err != nil {
		return err
	}
	if ev.RuntimeInitState != "" {
		return nil
	}
	switch op.Phase {
	case "source-ready", "branch-assigned", "environment-publishing", "environment-ready":
		ev.RuntimeInitState = "not-attempted"
		op.Evidence, err = json.Marshal(ev)
	}
	return err
}

// recordCreationInitAttempt runs only at the native pre-dispatch boundary.
// Persisting before dispatch makes a crash/lost reply fail closed even when
// inventory temporarily reports absence. Exact existing native identity can
// resume through Create's observation without issuing another init.
func recordCreationInitAttempt(ctx context.Context, op *control.Operation, persist func(context.Context, json.RawMessage) error) error {
	ev, err := control.Evidence(*op)
	if err != nil {
		return err
	}
	if ev.RuntimeInitState != "not-attempted" || op.Kind != "session.create" || !op.Committed {
		return errors.New("creation init outcome unknown; absent runtime cannot authorize another init")
	}
	ev.RuntimeInitState = "attempted"
	raw, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if err = persist(ctx, raw); err != nil {
		return err
	}
	op.Evidence = raw
	return nil
}
