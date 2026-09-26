package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

type RetainedBranch struct {
	Branch string `json:"branch"`
	Ref    string `json:"ref"`
	OID    string `json:"oid"`
}

func (s *Store) IsRetainedBranch(ctx context.Context, project, branch string) (bool, error) {
	if !validProject(project) || !validBranch(branch) {
		return false, ErrInvalid
	}
	active, err := s.HasActiveProject(ctx, project)
	if err != nil {
		return false, err
	}
	if !active {
		return false, ErrNotFound
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

func retainedBranchesHandler(ctx context.Context, params json.RawMessage, api OriginAPI) (any, *RPCError) {
	if api.Git == nil {
		return nil, lifecycleRPC(ErrNotFound)
	}
	var p struct {
		V       int    `json:"v"`
		Project string `json:"project"`
		After   string `json:"after"`
		Limit   int    `json:"limit"`
	}
	if strictDecode(params, &p) != nil || p.V != 1 || !validProject(p.Project) || p.Limit < 1 || p.Limit > 8 || p.After != "" && (!strings.HasPrefix(p.After, "refs/heads/") || !validBranch(strings.TrimPrefix(p.After, "refs/heads/"))) {
		return nil, errorRPC(-32602, "invalid_params", "invalid retained branch page")
	}
	active, err := api.Store.HasActiveProject(ctx, p.Project)
	if err != nil {
		return nil, lifecycleRPC(err)
	}
	if !active {
		return nil, lifecycleRPC(ErrNotFound)
	}
	refs, next, err := api.Git.ListRefsPage(ctx, p.Project, p.After, p.Limit)
	if err != nil {
		return nil, lifecycleRPC(err)
	}
	branches := make([]RetainedBranch, 0, len(refs))
	for _, ref := range refs {
		name := strings.TrimPrefix(ref.Ref, "refs/heads/")
		retained, e := api.Store.IsRetainedBranch(ctx, p.Project, name)
		if e != nil {
			return nil, lifecycleRPC(e)
		}
		if retained {
			branches = append(branches, RetainedBranch{Branch: name, Ref: ref.Ref, OID: ref.OID})
		}
	}
	if branches == nil {
		branches = []RetainedBranch{}
	}
	return boundedLifecyclePage("branches", branches, next, func(v RetainedBranch) string { return v.Ref })
}
