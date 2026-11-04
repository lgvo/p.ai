package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lgvo/p.ai/internal/api"
)

// The task commands that ask before a risky change (ask.go).

func runSessionDiscard(in *invocation) error {
	return removeSession(in, api.SessionDiscard, func(uuid string, c api.ChangeRequest) api.SessionDiscardRequest {
		return api.SessionDiscardRequest{UUID: uuid, ChangeRequest: c}
	})
}

func runSessionDelete(in *invocation) error {
	return removeSession(in, api.SessionDelete, func(uuid string, c api.ChangeRequest) api.SessionDeleteRequest {
		return api.SessionDeleteRequest{UUID: uuid, ChangeRequest: c}
	})
}

// removeSession discards or deletes, by m, the session the argument names.
// P stops a running session itself.
func removeSession[Req api.Request](in *invocation, m api.Method[Req, api.ChangeResult[api.RemovalDetails]], request func(uuid string, c api.ChangeRequest) Req) error {
	name, err := parseSessionName(in.args[0])
	if err != nil {
		return err
	}
	deletes := m.Name == api.SessionDelete.Name
	verb, counting, done := "Discard", "Discarding", "Discarded"
	if deletes {
		verb, counting, done = "Delete", "Deleting", "Deleted"
	}
	var s api.SessionView
	_, err = askChange(in, riskyChange[Req, api.RemovalDetails]{
		method: m, kind: m.Name, matches: name.named,
		request: func() (Req, error) {
			s, err = in.session(name)
			return request(s.UUID, api.ChangeRequest{}), err
		},
		run: func(_ Req, c api.ChangeRequest) Req { return request(s.UUID, c) },
		heading: func(p api.Plan[api.RemovalDetails]) string {
			// The plan names the branch the session holds now; a plan that is
			// not allowed may name none.
			at := branchName{s.Project, s.Branch}
			if p.Details.Branch != "" {
				at = branchName{p.Details.Project, p.Details.Branch}
			}
			heading := fmt.Sprintf("%s session %s (%s)", verb, s.UUID, at)
			switch {
			case p.Details.AssignedRefMissing && deletes:
				heading += "; its branch is already gone, so nothing leaves P's repository."
			case p.Details.AssignedRefMissing:
				heading += "; its branch is already gone, so discard keeps no branch and ends like delete."
			case deletes:
				heading += fmt.Sprintf("; its branch %s is removed from P too.", at.branch)
			default:
				heading += fmt.Sprintf("; its branch %s stays in P.", at.branch)
			}
			if p.Details.Runtime.OriginalStatus == "Running" {
				heading += "\nP stops the running session first."
			}
			return heading
		},
		question: func() string { return verb + " session " + s.UUID + "?" },
		counting: counting,
		done: func(op api.Operation) string {
			return fmt.Sprintf("%s session %s (%s:%s).", done, op.SessionUUID, op.Intent.Project, op.Intent.Branch)
		},
	})
	return err
}

func runSessionCleanup(in *invocation) error {
	name, err := parseSessionName(in.args[0])
	if err != nil {
		return err
	}
	var s api.SessionView
	_, err = askChange(in, riskyChange[api.SessionCreateCleanupRequest, api.CreateCleanupDetails]{
		method: api.SessionCreateCleanup, kind: api.SessionCreateCleanup.Name, matches: name.named,
		request: func() (api.SessionCreateCleanupRequest, error) {
			s, err = in.session(name)
			return api.SessionCreateCleanupRequest{UUID: s.UUID}, err
		},
		run: func(r api.SessionCreateCleanupRequest, c api.ChangeRequest) api.SessionCreateCleanupRequest {
			r.ChangeRequest = c
			return r
		},
		heading: func(p api.Plan[api.CreateCleanupDetails]) string {
			heading := fmt.Sprintf("Clean up the failed creation of session %s (%s:%s)", s.UUID, s.Project, s.Branch)
			if p.Details.OldOperationID != "" {
				heading += ", operation " + p.Details.OldOperationID
			}
			return heading + fmt.Sprintf(". It keeps branch %s in P and the shared images.", s.Branch)
		},
		question: func() string { return "Clean up the failed creation of session " + s.UUID + "?" },
		counting: "Cleaning up",
		done: func(op api.Operation) string {
			return fmt.Sprintf("Cleaned up the failed creation of session %s (%s:%s).", op.SessionUUID, op.Intent.Project, op.Intent.Branch)
		},
	})
	return err
}

// replaceCreation replaces the blocked creation of the session --replace
// names with a new creation on b, of choice from source. Its run's key is
// also the new creation's, whose operation it waits for.
func replaceCreation(in *invocation, b branchName, choice, source string) (api.Operation, error) {
	old, err := parseSessionName(in.replace)
	if err != nil {
		return api.Operation{}, err
	}
	choices := api.CreationChoices{Project: b.project, Branch: b.branch, Choice: choice, Source: source}
	var s api.SessionView
	return askChange(in, riskyChange[api.SessionCreateReplaceRequest, api.CreateReplaceDetails]{
		method: api.SessionCreateReplace, kind: api.SessionCreate.Name,
		matches: func(i api.OperationIntent) bool {
			return old.replaced(i) && i.Project == b.project && i.Branch == b.branch && i.Choice == choice && i.Source == source && i.OriginRef == ""
		},
		request: func() (api.SessionCreateReplaceRequest, error) {
			s, err = in.session(old)
			return api.SessionCreateReplaceRequest{OldUUID: s.UUID, CreationChoices: choices}, err
		},
		run: func(r api.SessionCreateReplaceRequest, c api.ChangeRequest) api.SessionCreateReplaceRequest {
			r.ChangeRequest = c
			return r
		},
		heading: func(p api.Plan[api.CreateReplaceDetails]) string {
			heading := fmt.Sprintf("Replace the blocked creation of session %s (%s:%s)", s.UUID, s.Project, s.Branch)
			if p.Details.OldOperationID != "" {
				heading += ", operation " + p.Details.OldOperationID + ","
			}
			if choice == "existing" {
				heading += fmt.Sprintf(" with a new session on the existing branch %s", b)
			} else {
				heading += fmt.Sprintf(" with a new session on a new branch %s from %s", b, source)
			}
			return heading + fmt.Sprintf(". Branch %s stays in P.", s.Branch)
		},
		question: func() string { return "Replace the blocked creation of session " + s.UUID + "?" },
		counting: "Replacing",
		done: func(op api.Operation) string {
			return fmt.Sprintf("Replaced the blocked creation of session %s with session %s (%s).", op.Intent.ReplacesSession, op.SessionUUID, b)
		},
	})
}

func runProjectDelete(in *invocation) error {
	project := in.args[0]
	_, err := askChange(in, riskyChange[api.ProjectDeleteRequest, api.ProjectDeleteDetails]{
		method: api.ProjectDelete, kind: api.ProjectDelete.Name,
		matches: func(i api.OperationIntent) bool { return i.Project == project },
		request: func() (api.ProjectDeleteRequest, error) { return api.ProjectDeleteRequest{Project: project}, nil },
		run: func(r api.ProjectDeleteRequest, c api.ChangeRequest) api.ProjectDeleteRequest {
			r.ChangeRequest = c
			return r
		},
		heading: func(p api.Plan[api.ProjectDeleteDetails]) string {
			d := p.Details
			lines := []string{"Delete project " + project + " and everything P keeps of it:"}
			for _, s := range d.Sessions {
				line := fmt.Sprintf("  - session %s (%s:%s), %s", s.Session.UUID, s.Session.Project, s.Session.Branch, s.Condition)
				if s.Condition == "running" {
					line += "; P stops it first"
				}
				lines = append(lines, line)
			}
			if d.BranchLoss != nil {
				for _, ref := range d.BranchLoss.PRefs {
					lines = append(lines, fmt.Sprintf("  - branch %s at %s", strings.TrimPrefix(ref.Name, "refs/heads/"), ref.OID))
				}
			}
			if len(d.Images) > 0 {
				lines = append(lines, fmt.Sprintf("  - %d images of its commits", len(d.Images)))
			}
			kept := "It keeps the contents of granted host directories"
			if len(d.ExternalMounts) > 0 {
				var mounts []string
				for _, m := range d.ExternalMounts {
					mounts = append(mounts, m.Source)
				}
				kept += " (" + strings.Join(mounts, ", ") + ")"
			}
			lines = append(lines, kept+", shared images, other projects, and logs already delivered to event handlers, whose retention is each handler's own.")
			return strings.Join(lines, "\n")
		},
		question: func() string { return "Delete project " + project + "?" },
		counting: "Deleting",
		done:     func(api.Operation) string { return "Deleted project " + project + "." },
	})
	return err
}

func runBranchDelete(in *invocation) error {
	b, err := parseBranchName(in.args[0])
	if err != nil {
		return err
	}
	_, err = askChange(in, riskyChange[api.ProjectRetainedDeleteRequest, api.RetainedDeleteDetails]{
		method: api.ProjectRetainedDelete, kind: api.ProjectRetainedDelete.Name,
		matches: func(i api.OperationIntent) bool { return i.Project == b.project && i.Branch == b.branch },
		request: func() (api.ProjectRetainedDeleteRequest, error) {
			return api.ProjectRetainedDeleteRequest{Project: b.project, Branch: b.branch}, nil
		},
		run: func(r api.ProjectRetainedDeleteRequest, c api.ChangeRequest) api.ProjectRetainedDeleteRequest {
			r.ChangeRequest = c
			return r
		},
		heading: func(p api.Plan[api.RetainedDeleteDetails]) string {
			if p.Details.Tip == "" {
				return fmt.Sprintf("Delete branch %s from P's repository.", b)
			}
			return fmt.Sprintf("Delete branch %s, at %s, from P's repository.", b, p.Details.Tip)
		},
		question: func() string { return fmt.Sprintf("Delete branch %s?", b) },
		counting: "Deleting",
		done:     func(api.Operation) string { return fmt.Sprintf("Deleted branch %s.", b) },
	})
	return err
}

// publicationOutcomes says what a completed publication did, by its status.
var publicationOutcomes = map[string]string{
	"created":   "created it",
	"advanced":  "fast-forwarded it",
	"satisfied": "it already had that commit, so nothing was pushed",
}

func runOriginPublish(in *invocation) error {
	b, err := parseBranchName(in.args[0])
	if err != nil {
		return err
	}
	to := strings.TrimPrefix(in.to, "refs/heads/")
	if in.to == "" {
		to = b.branch
	}
	if !api.ValidBranch(to) {
		return usageError("--to %q is not a branch name", in.to)
	}
	destination := "refs/heads/" + to
	var url, source string
	op, err := askChange(in, riskyChange[api.OriginPublishRequest, api.PublicationPreview]{
		method: api.OriginPublish, kind: api.OriginPublish.Name,
		matches: func(i api.OperationIntent) bool {
			return i.Project == b.project && i.Branch == b.branch && i.Destination == destination
		},
		request: func() (api.OriginPublishRequest, error) {
			req := api.OriginPublishRequest{Project: b.project, Kind: "retained", Source: b.branch, DestinationRef: destination}
			current, err := call(in, api.OriginInspect, api.OriginInspectRequest{Project: b.project})
			if err != nil {
				return req, err
			}
			if current.Origin.Status == "local-only" {
				return req, &exitError{code: exitNotRun, text: fmt.Sprintf("Project %s has no origin; set one with: p origin set %s URL", b.project, b.project)}
			}
			holder, err := in.holder(b)
			if err != nil {
				return req, err
			}
			if holder != nil {
				req.Kind, req.Source = "session", holder.UUID
			}
			req.ExpectedOriginURL, url = current.Origin.URL, current.Origin.URL
			req.SourceOID, err = in.branchTip(b)
			source = req.SourceOID
			return req, err
		},
		run: func(r api.OriginPublishRequest, c api.ChangeRequest) api.OriginPublishRequest {
			r.ChangeRequest = c
			return r
		},
		heading: func(p api.Plan[api.PublicationPreview]) string {
			heading := fmt.Sprintf("Publish %s, at %s, to branch %s of %s", b, source, to, url)
			switch p.Details.Relation {
			case "absent":
				heading += ": it creates the branch."
			case "fast_forward":
				heading += fmt.Sprintf(": it fast-forwards the branch from %s.", p.Details.DestinationOID)
			case "equal", "destination_contains":
				heading += ": the branch already has that commit, so nothing is pushed."
			default:
				heading += "."
			}
			return heading + "\nP publishes the commit in its repository, not work still in a session's workspace."
		},
		question: func() string { return fmt.Sprintf("Publish %s to branch %s of %s?", b, to, url) },
		counting: "Publishing",
		done: func(op api.Operation) string {
			published := publicationOf(op)
			outcome, ok := publicationOutcomes[published.Status]
			if !ok {
				return ""
			}
			return fmt.Sprintf("Published %s to branch %s of %s: %s.", b, to, published.Preview.URL, outcome)
		},
	})
	if err == nil && op.Status == "completed" && publicationOf(op).Status == "refused" {
		return &exitError{code: exitFailed, text: fmt.Sprintf("The origin's server rejected the push to branch %s; nothing was published (operation %s).", to, op.ID)}
	}
	return err
}

// publicationOf is the publication an origin.publish operation recorded in
// its evidence: what it planned, and how its push ended.
func publicationOf(op api.Operation) api.Publication {
	var evidence api.PublicationEvidence
	_ = json.Unmarshal(op.Evidence, &evidence)
	return evidence.Publication
}
