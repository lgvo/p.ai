package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
)

type projectDeletePreviewState struct {
	Preview api.ProjectDeletionPreview
	// Images are the reviewed image claims, which the preview shows as
	// recorded.
	Images  []control.EnvironmentCollectionClaim
	Request control.ProjectDeletePreviewRequest
	Expiry  time.Time
}

// lockProjectDelete admits one project deletion review or confirmation of
// project at a time; another one holding it answers retry.
func (l *lifecycle) lockProjectDelete(ctx context.Context, project string) (func(), error) {
	release, err := l.lockSession(ctx, "project-delete:"+project)
	if err != nil && classifiedRetry(err) {
		return nil, control.Retry(api.ReasonProjectHeld, api.TargetProject(project))
	}
	return release, err
}

// classifiedRetry reports whether err answers retry.
func classifiedRetry(err error) bool {
	var refusal *control.Refusal
	return errors.As(err, &refusal) && refusal.Kind == api.KindRetry
}
func (l *lifecycle) lockProjectDeleteSessions(ctx context.Context, sessions []control.Session) (func(), error) {
	releases := []func(){}
	release := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	for _, s := range sessions {
		r, e := l.lockSession(ctx, s.UUID)
		if e != nil {
			release()
			if classifiedRetry(e) {
				return nil, control.Retry(api.ReasonSessionHeld, api.TargetSession(s.UUID))
			}
			return nil, e
		}
		releases = append(releases, r)
	}
	return release, nil
}

// collectionToken issues a project deletion preview's confirmation token.
func collectionToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

// observeCollection binds one indexed image's Incus presence, size and related
// sessions into the claim a project deletion reviews.
func (l *lifecycle) observeCollection(ctx context.Context, entry control.EnvironmentImage) (control.EnvironmentCollectionClaim, error) {
	claim := control.EnvironmentCollectionClaim{Image: entry}
	info, found, err := l.runtime.ObserveOwnedBuilderImage(ctx, imageClaimFromCache(entry))
	if err != nil {
		return claim, control.Failed(api.ReasonRuntime, err)
	}
	claim.ImagePresent = found
	claim.ObservedSize = entry.LogicalSize
	if found {
		claim.ObservedSize = info.Size
	}
	claim.RelatedDigest, err = l.store.RelatedEnvironmentDigest(ctx, entry.Project, entry.Key, entry.Fingerprint)
	return claim, err
}
func projectDeleteNamedResources(sessions []api.ProjectDeleteSession, images []control.EnvironmentCollectionClaim) (map[string]bool, map[string]bool) {
	names, fps := map[string]bool{}, map[string]bool{}
	for _, s := range sessions {
		names[s.Runtime.InstanceName] = true
	}
	for _, c := range images {
		fps[c.Image.Fingerprint] = true
	}
	return names, fps
}

// projectDeleteFacts reviews deleting req.Project and returns the review and
// the image claims it shows.
func (l *lifecycle) projectDeleteFacts(ctx context.Context, req control.ProjectDeletePreviewRequest) (api.ProjectDeletionPreview, []control.EnvironmentCollectionClaim, error) {
	claims := []control.EnvironmentCollectionClaim{}
	p := api.ProjectDeletionPreview{Project: req.Project, Outcome: "delete_project_and_all_p_data", Attachments: []string{}, Sessions: []api.ProjectDeleteSession{}, Warnings: []string{
		"All P refs, reachable and unreachable bare Git objects, runtime workspaces, private home files and runtime-local credentials will be deleted. P does not look inside the runtimes; their contents are not enumerated or hashed.",
		"External filesystem-grant contents, shared/unindexed images and unrelated projects are preserved; delivered handler logs remain subject to the handler's own retention policy."}}
	sessions, sha, err := l.store.ProjectDeleteSnapshot(ctx, req.Project)
	if err != nil {
		return p, nil, err
	}
	p.StateSHA256 = sha
	projectPolicy, projectSHA, e := l.store.ProjectPolicyRecord(ctx, req.Project)
	if e != nil {
		return p, nil, e
	}
	captured, e := control.ParseStoredProjectPolicy(projectPolicy)
	if e != nil {
		return p, nil, e
	}
	p.ProjectPolicySHA256 = projectSHA
	p.ExternalMounts = control.GrantViews(captured.FilesystemMounts)

	missing := map[string]bool{}
	for _, id := range req.AcknowledgeMissing {
		if missing[id] {
			return p, nil, control.Invalid("acknowledge_missing")
		}
		missing[id] = true
	}
	acknowledged := 0
	for _, s := range sessions {
		target := api.TargetSession(s.UUID)
		var attached, starting bool
		l.withLock(func() { attached, starting = l.hasAttachmentLocked(s.UUID), l.startActive[s.UUID] })
		if attached {
			return p, nil, control.Conflict(api.ReasonAttachmentActive, target)
		}
		if starting {
			return p, nil, control.Retry(api.ReasonSessionHeld, target)
		}
		native, e := l.runtimeSession(ctx, s)
		if e != nil {
			return p, nil, e
		}
		observed, e := l.runtime.Inspect(ctx, native)
		if e != nil {
			return p, nil, control.Failed(api.ReasonRuntime, e)
		}
		ps := api.ProjectDeleteSession{Session: api.SessionRecord(s), Condition: "stopped", Runtime: api.RemovalRuntimePreview{IncusProject: l.cfg.IncusProject, InstanceName: "p-" + s.UUID}}
		creator, e := l.store.CreationForSession(ctx, s.UUID)
		if e != nil {
			return p, nil, e
		}
		local, e := l.reviewCreateCleanupLocal(ctx, creator, s)
		if e != nil || local == nil {
			return p, nil, errors.Join(e, errors.New("exact session credentials/endpoints required; investigate manually"))
		}
		ps.Local = *local
		if observed.Exists {
			switch {
			case missing[s.UUID]:
				// The client acknowledged a missing runtime that is present.
				return p, nil, control.Stale(api.ReasonChanged)
			case observed.Status != "Stopped":
				return p, nil, control.Conflict(api.ReasonSessionNotStopped, target)
			case observed.IncusUUID == "" || observed.Generation == "":
				return p, nil, control.Failed(api.ReasonRuntime, fmt.Errorf("session runtime %s identity incomplete", s.UUID))
			}
			ps.Runtime.Condition = "present"
			ps.Runtime.IncusUUID = observed.IncusUUID
			ps.Runtime.Generation = observed.Generation
			ps.Runtime.ImageFingerprint = native.ImageFingerprint
			ps.Runtime.OriginalStatus = "Stopped"
			ps.Runtime.RuntimeDataLost = true
		} else {
			if !missing[s.UUID] {
				return p, nil, control.Conflict(api.ReasonRuntimeMissing, target)
			}
			if e = l.runtime.ConfirmSessionRuntimeAbsent(ctx, native); e != nil {
				return p, nil, runtimeFailure(e, target)
			}
			ps.Condition = "missing"
			ps.Runtime.Condition = "missing"
			ps.Runtime.RuntimeLossUnknown = true
			acknowledged++
		}
		p.Sessions = append(p.Sessions, ps)
	}
	if acknowledged != len(missing) {
		// An acknowledged session is not one of the project's.
		return p, nil, control.Invalid("acknowledge_missing")
	}
	branchLoss, err := l.git.backend.ProjectRemovalLoss(ctx, req.Project)
	if err != nil {
		return p, nil, gitFailure(err)
	}
	p.BranchLoss = deleteReview(branchLoss)
	entries, next, err := l.store.ListEnvironmentImages(ctx, req.Project, "", 20)
	if err != nil {
		return p, nil, err
	}
	if next != "" {
		return p, nil, control.Conflict(api.ReasonBoundExceeded, api.TargetProject(req.Project))
	}
	for _, entry := range entries {
		claim, e := l.observeCollection(ctx, entry)
		if e != nil {
			return p, nil, e
		}
		claims = append(claims, claim)
	}
	if p.Images, err = json.Marshal(claims); err != nil {
		return p, nil, err
	}
	names, images := projectDeleteNamedResources(p.Sessions, claims)
	if err = l.runtime.CheckProjectDeletionInventory(ctx, req.Project, names, images); err != nil {
		return p, nil, control.Failed(api.ReasonRuntime, err)
	}
	repo, err := l.git.backend.RepositoryPath(req.Project)
	if err != nil {
		return p, nil, control.Failed(api.ReasonGit, err)
	}
	p.Repository, err = replacementLocalIdentity(repo)
	if err != nil {
		return p, nil, err
	}
	p.RepositoryParent, err = replacementLocalIdentity(filepath.Dir(repo))
	if err != nil {
		return p, nil, err
	}
	if p.Repository.Mode&0170000 != 0040000 || p.Repository.Mode&0022 != 0 || p.RepositoryParent.Mode != 0040700 {
		return p, nil, control.ErrConflict
	}
	return p, claims, nil
}
func (l *lifecycle) PreviewProjectDelete(ctx context.Context, req control.ProjectDeletePreviewRequest) (api.ProjectDeletionPreview, error) {
	r, err := l.lockProjectDelete(ctx, req.Project)
	if err != nil {
		return api.ProjectDeletionPreview{}, err
	}
	defer r()
	sessions, _, err := l.store.ProjectDeleteSnapshot(ctx, req.Project)
	if err != nil {
		return api.ProjectDeletionPreview{}, err
	}
	release, err := l.lockProjectDeleteSessions(ctx, sessions)
	if err != nil {
		return api.ProjectDeletionPreview{}, err
	}
	defer release()
	p, claims, err := l.projectDeleteFacts(ctx, req)
	if err != nil {
		return p, err
	}
	token, err := collectionToken()
	if err != nil {
		return p, err
	}
	expiry := time.Now().Add(removalPreviewTTL)
	p.ConfirmationToken = token
	p.ExpiresAt = expiry.UTC().Format(time.RFC3339Nano)
	raw, err := json.Marshal(p)
	if err != nil {
		return api.ProjectDeletionPreview{}, control.Internal(api.ReasonEncoding, err)
	}
	if len(raw) > api.MaxFrameBytes-1024 {
		return api.ProjectDeletionPreview{}, control.Conflict(api.ReasonBoundExceeded, api.TargetProject(req.Project))
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.projectDeletePreviews == nil {
		l.projectDeletePreviews = map[string]projectDeletePreviewState{}
	}
	for key, s := range l.projectDeletePreviews {
		if !time.Now().Before(s.Expiry) {
			delete(l.projectDeletePreviews, key)
		}
	}
	if len(l.projectDeletePreviews) >= 32 {
		return api.ProjectDeletionPreview{}, control.Retry(api.ReasonTooManyPreviews, "")
	}
	l.projectDeletePreviews[token] = projectDeletePreviewState{p, claims, req, expiry}
	return p, nil
}
func (l *lifecycle) ConfirmProjectDelete(ctx context.Context, req control.ProjectDeleteConfirmRequest) (api.Operation, error) {
	if req.Key == "" || len(req.Key) > 128 || !validRetainedDeleteToken(req.ConfirmationToken) {
		return api.Operation{}, control.ErrInvalid
	}
	saved := control.ProjectDeleteRequest{Key: req.Key, Project: req.Project, TokenSHA256: hexDigest([]byte(req.ConfirmationToken))}
	replay := func() (api.Operation, bool, error) {
		op, e := l.store.GetOperationByKey(ctx, req.Key)
		if errors.Is(e, control.ErrNotFound) {
			return op, false, nil
		}
		if e != nil {
			return api.Operation{}, true, e
		}
		var old control.ProjectDeleteRequest
		if op.Kind != "project.delete" || json.Unmarshal(op.Request, &old) != nil || old != saved {
			return api.Operation{}, true, control.KeyReused()
		}
		return op, true, nil
	}
	if op, found, e := replay(); found || e != nil {
		return op, e
	}
	projectRelease, err := l.lockProjectDelete(ctx, req.Project)
	if err != nil {
		return api.Operation{}, err
	}
	defer projectRelease()
	if op, found, e := replay(); found || e != nil {
		return op, e
	}
	var state projectDeletePreviewState
	var found bool
	l.withLock(func() { state, found = l.projectDeletePreviews[req.ConfirmationToken] })
	switch {
	case !found || !time.Now().Before(state.Expiry):
		return api.Operation{}, control.Stale(api.ReasonExpired)
	case state.Request.Project != req.Project:
		// The token reviews another project's deletion.
		return api.Operation{}, control.Stale(api.ReasonChanged)
	}
	sessions, sha, err := l.store.ProjectDeleteSnapshot(ctx, req.Project)
	if err != nil {
		return api.Operation{}, staleIfConflict(err)
	}
	if sha != state.Preview.StateSHA256 {
		return api.Operation{}, control.Stale(api.ReasonChanged)
	}
	release, err := l.lockProjectDeleteSessions(ctx, sessions)
	if err != nil {
		return api.Operation{}, err
	}
	defer release()
	var accepted api.Operation
	err = l.git.backend.WithOrigin(ctx, req.Project, func(*gitservice.OriginScope) error {
		verify := func(call context.Context) error {
			fresh, _, e := l.projectDeleteFacts(call, state.Request)
			if e != nil {
				return staleIfConflict(e)
			}
			fresh.ConfirmationToken = state.Preview.ConfirmationToken
			fresh.ExpiresAt = state.Preview.ExpiresAt
			if !reflect.DeepEqual(fresh, state.Preview) {
				return control.Stale(api.ReasonChanged)
			}
			return nil
		}
		ev := control.ProjectDeleteEvidence{InstanceUUID: l.instanceID, BaseFingerprint: l.cfg.BaseImageFingerprint, IncusProject: l.cfg.IncusProject, StateSHA256: sha, Sessions: append([]api.ProjectDeleteSession(nil), state.Preview.Sessions...), Images: state.Images, Repository: state.Preview.Repository, RepositoryParent: state.Preview.RepositoryParent}
		reviewed, _ := json.Marshal(state.Preview)
		ev.ReviewSHA256 = hexDigest(reviewed)
		for i := range ev.Sessions {
			ev.Resources = append(ev.Resources, control.ProjectDeleteResource{Kind: "runtime", ID: ev.Sessions[i].Session.UUID, Status: "remaining"}, control.ProjectDeleteResource{Kind: "local", ID: ev.Sessions[i].Session.UUID, Status: "remaining"})
		}
		for _, im := range ev.Images {
			ev.Resources = append(ev.Resources, control.ProjectDeleteResource{Kind: "image", ID: im.Image.Key, Status: "remaining"})
		}
		ev.Resources = append(ev.Resources, control.ProjectDeleteResource{Kind: "repository", ID: "bare", Status: "remaining"})
		var e error
		accepted, e = l.store.BeginProjectDelete(ctx, saved, ev, state.Preview.ExpiresAt, verify)
		return staleIfConflict(e)
	})
	if err != nil {
		return api.Operation{}, err
	}
	l.withLock(func() { delete(l.projectDeletePreviews, req.ConfirmationToken) })
	l.recordProgress(accepted, "running")
	return accepted, l.enqueue(accepted.ID)
}

func (l *lifecycle) projectDeleteRepositoryAbsent(project string, parent api.ReplacementLocalIdentity) error {
	repo, e := l.git.backend.RepositoryPath(project)
	if e != nil {
		return e
	}
	if e = matchReplacementLocal(filepath.Dir(repo), parent, false); e != nil {
		return e
	}
	if _, e = os.Lstat(repo); !errors.Is(e, os.ErrNotExist) {
		return control.ErrConflict
	}
	return nil
}

func (l *lifecycle) forgetProjectDeletePreviews(project string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for key, v := range l.projectDeletePreviews {
		if v.Preview.Project == project {
			delete(l.projectDeletePreviews, key)
		}
	}
	for key, v := range l.removalPreviews {
		if v.Preview.Project == project {
			delete(l.removalPreviews, key)
		}
	}
	for key, v := range l.retainedDeletePreviews {
		if v.Preview.Project == project {
			delete(l.retainedDeletePreviews, key)
		}
	}
	for key, v := range l.createCleanupPreviews {
		if v.Preview.OldRequest.Project == project {
			delete(l.createCleanupPreviews, key)
		}
	}
	for key, v := range l.createReplacePreviews {
		if v.Preview.OldRequest.Project == project {
			delete(l.createReplacePreviews, key)
		}
	}
	delete(l.changedPolicies, project)
}
