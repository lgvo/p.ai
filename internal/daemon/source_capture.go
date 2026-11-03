package daemon

import (
	"context"
	"errors"
	"fmt"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// checkCreationNames has the source driver check the fields of creation req
// whose syntax is Git's, and returns req's source as the driver classified
// it: a branch or a commit, or nothing when req names none. A value the
// driver refuses answers invalid with its field.
func (l *lifecycle) checkCreationNames(ctx context.Context, req control.ReserveSessionRequest) (sourcedriver.Name, error) {
	fields := []control.NameField{{Field: "branch", Name: sourcedriver.Name{Kind: sourcedriver.NameBranch, Value: req.Branch}}}
	source := -1
	if req.Source != "" {
		source = len(fields)
		fields = append(fields, control.NameField{Field: "source", Name: sourcedriver.Name{Kind: sourcedriver.NameCreationSource, Value: req.Source}})
	}
	if req.OriginRef != "" {
		fields = append(fields, control.NameField{Field: "origin_ref", Name: sourcedriver.Name{Kind: sourcedriver.NameOriginRef, Value: req.OriginRef}})
	}
	if req.ExpectedCommitOID != "" {
		fields = append(fields, control.NameField{Field: "expected_commit_oid", Name: sourcedriver.Name{Kind: sourcedriver.NameCommit, Value: req.ExpectedCommitOID}})
	}
	checked, err := control.CheckNames(ctx, l.git.driver, fields...)
	if err != nil || source < 0 {
		return sourcedriver.Name{}, err
	}
	return checked[source], nil
}

// captureLocalSource captures the commit local creation req starts from.
// source is req's source as the source driver classified it; an existing
// choice starts from its own branch instead. It imports nothing and requires
// the exact choice req makes: an existing choice needs its branch, a new one
// needs the name free and a source that exists. Its caller holds the store's
// Git authority, so no session push moves a head meanwhile.
func (l *lifecycle) captureLocalSource(ctx context.Context, req control.ReserveSessionRequest, source sourcedriver.Name) (control.CapturedSource, error) {
	branch := api.TargetBranch(req.Project, req.Branch)
	current, exists, err := l.inspectBranch(ctx, req.Project, req.Branch)
	if errors.Is(err, control.ErrConflict) {
		// A symbolic ref holds the branch's name.
		return control.CapturedSource{}, control.Conflict(api.ReasonBranchExists, branch)
	}
	if err != nil {
		return control.CapturedSource{}, control.Failed(api.ReasonGit, err)
	}
	if req.Choice == "existing" {
		if !exists {
			return control.CapturedSource{}, control.NotFound(api.ReasonBranch, branch)
		}
		source = sourcedriver.Name{Kind: sourcedriver.NameBranch, Value: req.Branch}
	} else if exists {
		return control.CapturedSource{}, control.Conflict(api.ReasonBranchExists, branch)
	}
	oid, err := l.resolveSource(ctx, req.Project, source)
	if err != nil {
		return control.CapturedSource{}, err
	}
	if req.Choice == "existing" && oid != current {
		// The branch moved between the two reads; a new request reads it again.
		return control.CapturedSource{}, control.Retry(api.ReasonRefsMoved, branch)
	}
	return control.CapturedSource{OID: oid, Existed: exists}, nil
}

// resolveSource resolves a creation's source in project to the commit it
// names: a branch to its tip, and a commit ID to itself, which some head
// must reach, since a session may read only what P's heads reach. A source
// that names no such commit answers not_found, reason source; a failed read
// answers failed, reason git.
func (l *lifecycle) resolveSource(ctx context.Context, project string, source sourcedriver.Name) (string, error) {
	id := sourcedriver.OID(source.Value)
	switch source.Kind {
	case sourcedriver.NameBranch:
		tip, exists, err := l.git.driver.InspectBranch(ctx, project, source.Value)
		switch {
		case errors.Is(err, sourcedriver.ErrConflict):
			return "", sourceMissing("a symbolic ref, not a head, holds the source branch's name")
		case err != nil:
			return "", control.Failed(api.ReasonGit, err)
		case !exists:
			return "", sourceMissing("the source branch is missing")
		}
		id = tip
	case sourcedriver.NameCommit:
	default:
		return "", control.ErrInvalid
	}
	commit, err := l.git.driver.InspectCommit(ctx, project, id)
	switch {
	case err != nil:
		return "", control.Failed(api.ReasonGit, err)
	case !commit.Present:
		return "", sourceMissing("the source is not a commit")
	case source.Kind == sourcedriver.NameCommit && !commit.Reachable:
		return "", sourceMissing("no head reaches the source commit")
	}
	return string(id), nil
}

// sourceMissing answers that a creation's source does not exist as one.
func sourceMissing(detail string) error {
	return &control.Refusal{Kind: api.KindNotFound, Reason: api.ReasonSource, Cause: fmt.Errorf("%w: %s", control.ErrNotFound, detail)}
}
