package daemon

import (
	"context"
	"errors"

	"github.com/lgvo/p.ai/internal/control"
)

// recoverSessionEndpoint reopens endpoints with a durable consumer. Early
// creation phases have not committed credentials or a runtime; manufacturing
// their endpoint on restart would change a blocked request's no-effect facts.
// Their worker ensures endpoints when it actually reaches that stage. Existing
// unexpected early paths are preserved for inspection, never reopened here.
func recoverSessionEndpoint(ctx context.Context, session control.Session,
	creation func(context.Context, string) (control.Operation, error),
	ensure func(context.Context, string) (string, error)) error {
	if session.Registry == "established" {
		_, err := ensure(ctx, session.UUID)
		return err
	}
	if session.Registry != "creating" {
		return nil
	}
	op, err := creation(ctx, session.UUID)
	if errors.Is(err, control.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if op.Kind != "session.create" && op.Kind != "project.create" || op.SessionUUID != session.UUID || op.Project != session.Project || !op.Committed || op.Status != "running" && op.Status != "blocked" && op.Status != "unknown" {
		return nil
	}
	switch op.Phase {
	case "principals-ready", "runtime-created", "assembly-ready", "workspace-ready", "established":
	default:
		return nil
	}
	ev, err := control.Evidence(op)
	if err != nil || !ev.Selection.Valid() || ev.PolicySHA256 != session.PolicySHA256 {
		return nil
	}
	_, err = ensure(ctx, session.UUID)
	return err
}
