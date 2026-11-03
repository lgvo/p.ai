package daemon

import (
	"context"
	"errors"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
)

type branchAuthority interface {
	InspectBranchRef(context.Context, string, string) (string, bool, error)
	CreateBranch(context.Context, string, string, string) error
	CreateCapturedOriginBranch(context.Context, string, string, string, string) error
}

func ensureBranchAssigned(ctx context.Context, git branchAuthority, operationID string, req control.ReserveSessionRequest, capturedOID string) error {
	tip, exists, err := git.InspectBranchRef(ctx, req.Project, req.Branch)
	if err != nil {
		return control.Failed(api.ReasonGit, err)
	}
	if req.Choice == "existing" {
		if !exists || tip != capturedOID {
			return refusalBecause(api.KindPreconditionFailed, api.ReasonChanged, "", control.ErrConflict, errors.New("assigned existing branch changed from captured tip"))
		}
		return nil
	}
	if req.Choice != "new" {
		return control.ErrInvalid
	}
	if exists {
		if tip != capturedOID {
			return refusalBecause(api.KindPreconditionFailed, api.ReasonChanged, "", control.ErrConflict, errors.New("new branch differs from captured CAS intent"))
		}
		return nil
	}
	if req.OriginRef != "" {
		err = git.CreateCapturedOriginBranch(ctx, operationID, req.Project, req.Branch, capturedOID)
	} else {
		err = git.CreateBranch(ctx, req.Project, req.Branch, capturedOID)
	}
	if err != nil {
		return control.Failed(api.ReasonGit, err)
	}
	tip, exists, err = git.InspectBranchRef(ctx, req.Project, req.Branch)
	if err != nil {
		return control.Failed(api.ReasonGit, err)
	}
	if !exists || tip != capturedOID {
		return control.Failed(api.ReasonGit, errors.New("new branch CAS postcondition failed"))
	}
	return nil
}
