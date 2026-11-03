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
	Preview control.ProjectDeletePreview
	Request control.ProjectDeletePreviewRequest
	Expiry  time.Time
}

func (l *lifecycle) lockProjectDelete(ctx context.Context, project string) (func(), error) {
	return l.lockSession(ctx, "project-delete:"+project)
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
		return claim, err
	}
	claim.ImagePresent = found
	claim.ObservedSize = entry.LogicalSize
	if found {
		claim.ObservedSize = info.Size
	}
	claim.RelatedDigest, err = l.store.RelatedEnvironmentDigest(ctx, entry.Project, entry.Key, entry.Fingerprint)
	return claim, err
}
func projectDeleteNamedResources(sessions []control.ProjectDeleteSession, images []control.EnvironmentCollectionClaim) (map[string]bool, map[string]bool) {
	names, fps := map[string]bool{}, map[string]bool{}
	for _, s := range sessions {
		names[s.Runtime.InstanceName] = true
	}
	for _, c := range images {
		fps[c.Image.Fingerprint] = true
	}
	return names, fps
}
func (l *lifecycle) projectDeleteFacts(ctx context.Context, req control.ProjectDeletePreviewRequest) (control.ProjectDeletePreview, error) {
	p := control.ProjectDeletePreview{Project: req.Project, Outcome: "delete_project_and_all_p_data", Attachments: []string{}, Sessions: []control.ProjectDeleteSession{}, Images: []control.EnvironmentCollectionClaim{}, Warnings: []string{
		"All P refs, reachable and unreachable bare Git objects, runtime workspaces, private home files and runtime-local credentials will be deleted. P does not look inside the runtimes; their contents are not enumerated or hashed.",
		"External filesystem-grant contents, shared/unindexed images and unrelated projects are preserved; delivered handler logs remain subject to the handler's own retention policy."}}
	sessions, sha, err := l.store.ProjectDeleteSnapshot(ctx, req.Project)
	if err != nil {
		return p, err
	}
	p.StateSHA256 = sha
	projectPolicy, projectSHA, e := l.store.ProjectPolicyRecord(ctx, req.Project)
	if e != nil {
		return p, e
	}
	captured, e := control.ParseStoredProjectPolicy(projectPolicy)
	if e != nil {
		return p, e
	}
	p.ProjectPolicySHA256 = projectSHA
	p.ExternalMounts = captured.FilesystemMounts

	missing := map[string]bool{}
	for _, id := range req.AcknowledgeMissing {
		if missing[id] {
			return p, control.ErrInvalid
		}
		missing[id] = true
	}
	acknowledged := 0
	for _, s := range sessions {
		var attached bool
		l.withLock(func() { attached = l.hasAttachmentLocked(s.UUID) || l.startActive[s.UUID] })
		if attached {
			return p, fmt.Errorf("%w: detach and Stop all project sessions", control.ErrConflict)
		}
		native, e := l.runtimeSession(ctx, s)
		if e != nil {
			return p, e
		}
		observed, e := l.runtime.Inspect(ctx, native)
		if e != nil {
			return p, e
		}
		ps := control.ProjectDeleteSession{Session: s, Condition: "stopped", Runtime: control.RemovalRuntimePreview{IncusProject: l.cfg.IncusProject, InstanceName: "p-" + s.UUID}}
		creator, e := l.store.CreationForSession(ctx, s.UUID)
		if e != nil {
			return p, e
		}
		local, e := l.reviewCreateCleanupLocal(ctx, creator, s)
		if e != nil || local == nil {
			return p, errors.Join(e, errors.New("exact session credentials/endpoints required; investigate manually"))
		}
		ps.Local = *local
		if observed.Exists {
			if observed.Status != "Stopped" || observed.IncusUUID == "" || observed.Generation == "" || missing[s.UUID] {
				return p, fmt.Errorf("%w: Stop/detach session %s", control.ErrConflict, s.UUID)
			}
			ps.Runtime.Condition = "present"
			ps.Runtime.IncusUUID = observed.IncusUUID
			ps.Runtime.Generation = observed.Generation
			ps.Runtime.ImageFingerprint = native.ImageFingerprint
			ps.Runtime.OriginalStatus = "Stopped"
			ps.Runtime.RuntimeDataLost = true
		} else {
			if !missing[s.UUID] {
				return p, fmt.Errorf("%w: positively missing runtime %s requires acknowledge_missing", control.ErrConflict, s.UUID)
			}
			if e = l.runtime.ConfirmSessionRuntimeAbsent(ctx, native); e != nil {
				return p, e
			}
			ps.Condition = "missing"
			ps.Runtime.Condition = "missing"
			ps.Runtime.RuntimeLossUnknown = true
			acknowledged++
		}
		p.Sessions = append(p.Sessions, ps)
	}
	if acknowledged != len(missing) {
		return p, control.ErrInvalid
	}
	branchLoss, err := l.git.backend.ProjectRemovalLoss(ctx, req.Project)
	if err != nil {
		return p, err
	}
	p.BranchLoss = deleteReview(branchLoss)
	entries, next, err := l.store.ListEnvironmentImages(ctx, req.Project, "", 20)
	if err != nil || next != "" {
		return p, errors.Join(err, errors.New("project cache exceeds aggregate review bound"))
	}
	for _, entry := range entries {
		claim, e := l.observeCollection(ctx, entry)
		if e != nil {
			return p, e
		}
		p.Images = append(p.Images, claim)
	}
	names, images := projectDeleteNamedResources(p.Sessions, p.Images)
	if err = l.runtime.CheckProjectDeletionInventory(ctx, req.Project, names, images); err != nil {
		return p, err
	}
	repo, err := l.git.backend.RepositoryPath(req.Project)
	if err != nil {
		return p, err
	}
	p.Repository, err = replacementLocalIdentity(repo)
	if err != nil {
		return p, err
	}
	p.RepositoryParent, err = replacementLocalIdentity(filepath.Dir(repo))
	if err != nil {
		return p, err
	}
	if p.Repository.Mode&0170000 != 0040000 || p.Repository.Mode&0022 != 0 || p.RepositoryParent.Mode != 0040700 {
		return p, control.ErrConflict
	}
	return p, nil
}
func (l *lifecycle) PreviewProjectDelete(ctx context.Context, req control.ProjectDeletePreviewRequest) (control.ProjectDeletePreview, error) {
	r, err := l.lockProjectDelete(ctx, req.Project)
	if err != nil {
		return control.ProjectDeletePreview{}, err
	}
	defer r()
	sessions, _, err := l.store.ProjectDeleteSnapshot(ctx, req.Project)
	if err != nil {
		return control.ProjectDeletePreview{}, err
	}
	release, err := l.lockProjectDeleteSessions(ctx, sessions)
	if err != nil {
		return control.ProjectDeletePreview{}, err
	}
	defer release()
	p, err := l.projectDeleteFacts(ctx, req)
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
	if err != nil || len(raw) > api.MaxFrameBytes-1024 {
		return control.ProjectDeletePreview{}, errors.New("aggregate project deletion preview exceeds bound; manually review/clean resources before retry")
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
		return p, control.ErrConflict
	}
	l.projectDeletePreviews[token] = projectDeletePreviewState{p, req, expiry}
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
		var old control.ProjectDeleteRequest
		if e != nil || op.Kind != "project.delete" || json.Unmarshal(op.Request, &old) != nil || old != saved {
			return api.Operation{}, true, errors.Join(e, control.ErrConflict)
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
	if !found || state.Request.Project != req.Project || !time.Now().Before(state.Expiry) {
		return api.Operation{}, control.ErrConflict
	}
	sessions, sha, err := l.store.ProjectDeleteSnapshot(ctx, req.Project)
	if err != nil || sha != state.Preview.StateSHA256 {
		return api.Operation{}, errors.Join(err, control.ErrConflict)
	}
	release, err := l.lockProjectDeleteSessions(ctx, sessions)
	if err != nil {
		return api.Operation{}, err
	}
	defer release()
	var accepted api.Operation
	err = l.git.backend.WithOrigin(ctx, req.Project, func(*gitservice.OriginScope) error {
		verify := func(call context.Context) error {
			fresh, e := l.projectDeleteFacts(call, state.Request)
			fresh.ConfirmationToken = state.Preview.ConfirmationToken
			fresh.ExpiresAt = state.Preview.ExpiresAt
			if e != nil || !reflect.DeepEqual(fresh, state.Preview) {
				return errors.Join(e, control.ErrConflict)
			}
			return nil
		}
		ev := control.ProjectDeleteEvidence{InstanceUUID: l.instanceID, BaseFingerprint: l.cfg.BaseImageFingerprint, IncusProject: l.cfg.IncusProject, StateSHA256: sha, Sessions: append([]control.ProjectDeleteSession(nil), state.Preview.Sessions...), Images: state.Preview.Images, Repository: state.Preview.Repository, RepositoryParent: state.Preview.RepositoryParent}
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
		return e
	})
	if err != nil {
		return api.Operation{}, err
	}
	l.withLock(func() { delete(l.projectDeletePreviews, req.ConfirmationToken) })
	l.recordProgress(accepted, "running")
	return accepted, l.enqueue(accepted.ID)
}

func (l *lifecycle) projectDeleteRepositoryAbsent(project string, parent control.ReplacementLocalIdentity) error {
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
