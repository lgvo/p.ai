package main

import (
	"fmt"
	"strings"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/plugin"
)

// branchName is a PROJECT:BRANCH argument: a P branch of a project. The
// first ':' ends the path, because project paths and Git branch names cannot
// contain ':'.
type branchName struct{ project, branch string }

func parseBranchName(arg string) (branchName, error) {
	project, branch, ok := strings.Cut(arg, ":")
	if !ok || !api.ValidProject(project) || !api.ValidBranch(branch) {
		return branchName{}, usageError("%q is not PROJECT:BRANCH, such as team/app:work", arg)
	}
	return branchName{project, branch}, nil
}

func (b branchName) String() string { return b.project + ":" + b.branch }

// sessionName is a SESSION argument: a session UUID, or PROJECT:BRANCH for
// the session that holds that branch.
type sessionName struct {
	uuid string
	at   branchName
}

func parseSessionName(arg string) (sessionName, error) {
	if api.ValidUUID(arg) {
		return sessionName{uuid: arg}, nil
	}
	if !strings.Contains(arg, ":") {
		return sessionName{}, usageError("%q is not a session UUID or PROJECT:BRANCH", arg)
	}
	at, err := parseBranchName(arg)
	return sessionName{at: at}, err
}

// named reports whether an operation's intent names this session: by UUID,
// or by the project and branch it held when the daemon accepted it.
func (n sessionName) named(intent api.OperationIntent) bool {
	if n.uuid != "" {
		return intent.Session == n.uuid
	}
	return intent.Project == n.at.project && intent.Branch == n.at.branch
}

// sessionID is the UUID of the session n names.
func (in *invocation) sessionID(n sessionName) (string, error) {
	if n.uuid != "" {
		return n.uuid, nil
	}
	s, err := in.session(n)
	return s.UUID, err
}

// session reads the session n names. PROJECT:BRANCH names the session that
// holds that branch, found with session.list.
func (in *invocation) session(n sessionName) (api.SessionView, error) {
	if n.uuid != "" {
		r, err := call(in, api.SessionInspect, api.SessionInspectRequest{UUID: n.uuid})
		return r.Session, err
	}
	var found *api.SessionView
	err := pages(in, api.SessionList, func(after string) api.SessionListRequest {
		return api.SessionListRequest{Limit: 8, After: after}
	}, func(r api.SessionListResult) (string, bool) {
		for i, s := range r.Sessions {
			if s.Project == n.at.project && s.Branch == n.at.branch {
				found = &r.Sessions[i]
				return "", false
			}
		}
		return r.Next, true
	})
	if err != nil {
		return api.SessionView{}, err
	}
	if found == nil {
		return api.SessionView{}, &exitError{code: exitFailed, text: fmt.Sprintf("No session holds branch %s.", n.at)}
	}
	return *found, nil
}

// branchTip is the commit at the tip of P branch b, which a rename names as
// the tip it expects.
func (in *invocation) branchTip(b branchName) (string, error) {
	tip := ""
	err := pages(in, api.ProjectBranches, func(after string) api.ProjectBranchesRequest {
		return api.ProjectBranchesRequest{Project: b.project, Limit: 8, After: after}
	}, func(r api.ProjectBranchesResult) (string, bool) {
		for _, ref := range r.Refs {
			if ref.Ref == "refs/heads/"+b.branch {
				tip = ref.OID
				return "", false
			}
		}
		return r.Next, true
	})
	if err == nil && tip == "" {
		err = &exitError{code: exitNotRun, text: fmt.Sprintf("Branch %s has no commit in P.", b)}
	}
	return tip, err
}

// sourceRef is the form of a --from argument the daemon takes: a commit ID or
// a full ref as given, otherwise the P branch of that name.
func sourceRef(ref string) string {
	if strings.HasPrefix(ref, "refs/") || api.ValidOID(ref) {
		return ref
	}
	return "refs/heads/" + ref
}

// originRef is the form of a --from-origin argument the daemon takes: a full
// ref as given, such as refs/tags/v1, otherwise the origin branch of that
// name.
func originRef(ref string) string {
	if strings.HasPrefix(ref, "refs/") {
		return ref
	}
	return "refs/heads/" + ref
}

// freshOrigin is the origin's refs from a fresh observation of it.
type freshOrigin struct {
	url  string
	refs []plugin.GitOriginRef
}

// observeOrigin refreshes the origin of project and returns its refs only
// when the refresh and every page of origin.sources say fresh, as the TUI
// does: a failed contact still answers with the cached refs marked stale. A
// local-only project has no refs and is not run; a stale observation fails,
// saying when the origin was last reached.
func (in *invocation) observeOrigin(project string) (freshOrigin, error) {
	r, err := call(in, api.OriginRefresh, api.OriginRefreshRequest{Project: project})
	if err != nil {
		return freshOrigin{}, err
	}
	if r.Origin.Status == "local-only" {
		return freshOrigin{}, &exitError{code: exitNotRun, text: fmt.Sprintf("Project %s has no origin; set one with: p origin set %s URL", project, project)}
	}
	var origin freshOrigin
	stale := r.Origin.Status != "fresh"
	observedAt := r.Origin.ObservedAt
	if !stale {
		origin.refs, err = list(in, api.OriginSources, func(after string) api.OriginSourcesRequest {
			return api.OriginSourcesRequest{Project: project, Limit: 16, After: after}
		}, func(page api.OriginSourcesResult) ([]plugin.GitOriginRef, string) {
			if page.Status != "fresh" {
				stale, observedAt = true, page.ObservedAt
				return nil, ""
			}
			origin.url = page.OriginURL
			return page.Refs, page.Next
		})
		if err != nil {
			return freshOrigin{}, err
		}
	}
	if stale {
		last := "has never been reached"
		if observedAt != "" {
			last = "was last reached at " + observedAt
		}
		return freshOrigin{}, &exitError{code: exitFailed, text: fmt.Sprintf("The origin of %s could not be observed now, so its refs are not shown; it %s. Check its URL, the SSH identity and access: p origin show %s", project, last, project)}
	}
	return origin, nil
}
