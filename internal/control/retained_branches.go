package control

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/lgvo/p.ai/internal/api"
)

func (s *Store) IsRetainedBranch(ctx context.Context, project, branch string) (bool, error) {
	if !validProject(project) || !validBranch(branch) {
		return false, ErrInvalid
	}
	active, err := s.HasActiveProject(ctx, project)
	if err != nil {
		return false, err
	}
	if !active {
		return false, NotFound(api.ReasonProject, api.TargetProject(project))
	}
	var assigned int
	err = s.db.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE project_path=? AND branch=? LIMIT 1`, project, branch).Scan(&assigned)
	if err == nil {
		return false, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	return false, err
}

// retainedBranches answers project.retained_branches; origins.Git is
// configured.
func retainedBranches(ctx context.Context, r api.ProjectRetainedBranchesRequest, origins OriginAPI) (api.RetainedBranchesResult, error) {
	active, err := origins.Store.HasActiveProject(ctx, r.Project)
	if err != nil {
		return api.RetainedBranchesResult{}, err
	}
	if !active {
		return api.RetainedBranchesResult{}, NotFound(api.ReasonProject, api.TargetProject(r.Project))
	}
	refs, next, err := origins.Git.ListRefsPage(ctx, r.Project, r.After, r.Limit)
	if err != nil {
		return api.RetainedBranchesResult{}, Failed(api.ReasonGit, err)
	}
	branches := make([]api.RetainedBranch, 0, len(refs))
	for _, ref := range refs {
		name := strings.TrimPrefix(ref.Ref, "refs/heads/")
		retained, e := origins.Store.IsRetainedBranch(ctx, r.Project, name)
		if e != nil {
			return api.RetainedBranchesResult{}, e
		}
		if retained {
			branches = append(branches, api.RetainedBranch{Branch: name, Ref: ref.Ref, OID: ref.OID})
		}
	}
	return boundedPage(branches, next, func(v api.RetainedBranch) string { return v.Ref },
		func(branches []api.RetainedBranch, next string) api.RetainedBranchesResult {
			return api.RetainedBranchesResult{Branches: branches, Next: next}
		})
}
