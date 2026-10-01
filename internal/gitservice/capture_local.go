package gitservice

import (
	"context"
	"errors"
	"strings"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
)

// CaptureLocalSource uses the same selected-source observation as ordinary
// Create. It imports nothing and requires the exact requested target choice.
func (b *Backend) CaptureLocalSource(ctx context.Context, req control.ReserveSessionRequest) (control.CapturedSource, error) {
	if !control.ValidSessionCreateRequest(req) || req.OriginRef != "" {
		return control.CapturedSource{}, control.ErrInvalid
	}
	current, exists, err := b.InspectBranchRef(ctx, req.Project, req.Branch)
	if err != nil {
		return control.CapturedSource{}, err
	}
	selector := plugin.GitSourceSelector{Kind: "commit", Value: req.Source}
	if req.Choice == "existing" {
		if !exists {
			return control.CapturedSource{}, control.ErrNotFound
		}
		selector = plugin.GitSourceSelector{Kind: "branch", Value: "refs/heads/" + req.Branch}
	} else {
		if exists {
			return control.CapturedSource{}, control.ErrConflict
		}
		if strings.HasPrefix(req.Source, "refs/heads/") {
			selector.Kind = "branch"
		}
	}
	oid, err := b.ObserveSource(ctx, req.Project, selector)
	if err != nil {
		return control.CapturedSource{}, err
	}
	if req.Choice == "existing" && oid != current {
		return control.CapturedSource{}, errors.Join(control.ErrConflict, errors.New("source changed during capture"))
	}
	return control.CapturedSource{OID: oid, Existed: exists}, nil
}
