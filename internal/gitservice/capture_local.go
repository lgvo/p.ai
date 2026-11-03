package gitservice

import (
	"context"
	"errors"
	"strings"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
)

// CaptureLocalSource uses the same selected-source observation as ordinary
// Create. It imports nothing and requires the exact requested target choice:
// an existing choice needs the branch, a new one needs the name free and a
// source that exists. A failure of Git itself answers failed with reason git.
func (b *Backend) CaptureLocalSource(ctx context.Context, req control.ReserveSessionRequest) (control.CapturedSource, error) {
	if !control.ValidSessionCreateRequest(req) || req.OriginRef != "" {
		return control.CapturedSource{}, control.ErrInvalid
	}
	branch := api.TargetBranch(req.Project, req.Branch)
	current, exists, err := b.InspectBranchRef(ctx, req.Project, req.Branch)
	if errors.Is(err, control.ErrConflict) {
		// A symbolic ref occupies the branch name.
		return control.CapturedSource{}, control.Conflict(api.ReasonBranchExists, branch)
	}
	if err != nil {
		return control.CapturedSource{}, control.Failed(api.ReasonGit, err)
	}
	selector := plugin.GitSourceSelector{Kind: "commit", Value: req.Source}
	if req.Choice == "existing" {
		if !exists {
			return control.CapturedSource{}, control.NotFound(api.ReasonBranch, branch)
		}
		selector = plugin.GitSourceSelector{Kind: "branch", Value: "refs/heads/" + req.Branch}
	} else {
		if exists {
			return control.CapturedSource{}, control.Conflict(api.ReasonBranchExists, branch)
		}
		if strings.HasPrefix(req.Source, "refs/heads/") {
			selector.Kind = "branch"
		}
	}
	oid, err := b.ObserveSource(ctx, req.Project, selector)
	if err != nil {
		return control.CapturedSource{}, control.Failed(api.ReasonGit, err)
	}
	if req.Choice == "existing" && oid != current {
		// The branch moved between the two reads; a new request reads it again.
		return control.CapturedSource{}, control.Retry(api.ReasonRefsMoved, branch)
	}
	return control.CapturedSource{OID: oid, Existed: exists}, nil
}
