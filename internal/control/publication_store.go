package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/lgvo/p.ai/internal/api"
	"time"
)

// PublicationRequest binds a publication to one exact P tip and one origin
// head. Source is a session UUID or a retained branch name, according to
// Kind. Key and IfMatch are a run's; a dry run has neither.
type PublicationRequest struct {
	ExpectedOriginURL string `json:"expected_origin_url"`
	Key               string `json:"key,omitempty"`
	Project           string `json:"project"`
	Kind              string `json:"kind"`
	Source            string `json:"source"`
	SourceOID         string `json:"source_oid"`
	DestinationRef    string `json:"destination_ref"`
	IfMatch           string `json:"if_match,omitempty"`
}

func validPublicationRequest(r PublicationRequest) bool {
	if r.ExpectedOriginURL == "" || len(r.ExpectedOriginURL) > 2048 || !validProject(r.Project) || !validOID(r.SourceOID) || !api.BoundedName(r.DestinationRef, api.MaxRefBytes) {
		return false
	}
	if r.Kind == "session" {
		return len(r.Source) == 36
	}
	return r.Kind == "retained" && validBranch(r.Source)
}

// PublicationSource checks registry assignment, including unfinished lifecycle
// operations other than except, the publication's own. The caller holds the
// project's origin scope while it checks Git.
func (s *Store) PublicationSource(ctx context.Context, r PublicationRequest, except string) (string, error) {
	if !validPublicationRequest(r) {
		return "", ErrInvalid
	}
	var state string
	err := s.db.QueryRowContext(ctx, `SELECT registry_state FROM projects WHERE path=?`, r.Project).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", NotFound(api.ReasonProject, api.TargetProject(r.Project))
	}
	if err != nil {
		return "", err
	}
	if state != "active" {
		return "", Conflict(api.ReasonProjectDeleting, api.TargetProject(r.Project))
	}
	if r.Kind == "session" {
		var branch, registry string
		err = s.db.QueryRowContext(ctx, `SELECT branch,registry_state FROM sessions WHERE uuid=? AND project_path=?`, r.Source, r.Project).Scan(&branch, &registry)
		if errors.Is(err, sql.ErrNoRows) {
			return "", NotFound(api.ReasonSession, api.TargetSession(r.Source))
		}
		if err != nil {
			return "", err
		}
		// The journal's admission check: an unfinished operation, its
		// creation included, holds the session.
		if err = sessionHolderTx(ctx, s.db, r.Source, except); err != nil {
			return "", err
		}
		if registry != "established" {
			return "", Conflict(api.ReasonSessionNotEstablished, api.TargetSession(r.Source))
		}
		var guarded int
		err = s.db.QueryRowContext(ctx, `SELECT 1 FROM git_ref_guards WHERE project_path=? AND branch=?`, r.Project, branch).Scan(&guarded)
		if err == nil {
			return "", Conflict(api.ReasonBranchReserved, api.TargetBranch(r.Project, branch))
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		return "refs/heads/" + branch, nil
	}
	var assigned int
	err = s.db.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE project_path=? AND branch=? LIMIT 1`, r.Project, r.Source).Scan(&assigned)
	if err == nil {
		return "", Conflict(api.ReasonBranchAssigned, api.TargetBranch(r.Project, r.Source))
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	err = s.db.QueryRowContext(ctx, `SELECT 1 FROM git_ref_guards WHERE project_path=? AND branch=?`, r.Project, r.Source).Scan(&assigned)
	if err == nil {
		return "", Conflict(api.ReasonBranchReserved, api.TargetBranch(r.Project, r.Source))
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	return "refs/heads/" + r.Source, nil
}

// BeginPublication records publication run r, whose plan has change hash
// hash, as an operation of kind origin.publish, after admit accepts it, and
// returns it. An identical earlier request under r.Key returns its operation
// without admit. A session's publication holds the session: the journal
// refuses competing work on it while the operation is unfinished.
func (s *Store) BeginPublication(ctx context.Context, r PublicationRequest, hash string, admit func(context.Context) (branch string, err error)) (api.Operation, error) {
	expiry, ok := api.ParseChangeTag(r.IfMatch)
	if !validPublicationRequest(r) || r.Key == "" || len(r.Key) > 128 || !ok || !validFingerprint(hash) {
		return api.Operation{}, ErrInvalid
	}
	request, err := json.Marshal(r)
	if err != nil {
		return api.Operation{}, err
	}
	if prior, replayed, err := s.recorded(ctx, r.Key, KindOriginPublish, request); err != nil || replayed {
		return prior, err
	}
	branch, err := admit(ctx)
	if err != nil {
		// An identical request may have been recorded meanwhile, and now
		// hold the session admit checked.
		if prior, replayed, e := s.recorded(ctx, r.Key, KindOriginPublish, request); e != nil || replayed {
			return prior, e
		}
		return api.Operation{}, err
	}
	id, err := newUUID()
	if err != nil {
		return api.Operation{}, err
	}
	session := ""
	if r.Kind == "session" {
		session = r.Source
	}
	rec := operationRecord{ID: id, Key: r.Key, Kind: KindOriginPublish, Project: r.Project, Session: session, Request: request,
		Intent: api.OperationIntent{Project: r.Project, Session: session, Branch: branch, Destination: r.DestinationRef},
		Phase:  "accepted", ChangeHash: hash, Now: time.Now().UTC().Format(time.RFC3339Nano)}
	return s.insertForProject(ctx, rec, func(queryRower) error {
		// Inside the transaction, after its wait for the store's connection.
		if !time.Now().Before(expiry) {
			return Stale(api.ReasonExpired)
		}
		return nil
	})
}
