package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"time"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
	"github.com/lgvo/p.ai/internal/plugin"
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
func (l *lifecycle) projectDeleteFacts(ctx context.Context, req control.ProjectDeletePreviewRequest, scope *gitservice.OriginScope) (control.ProjectDeletePreview, error) {
	p := control.ProjectDeletePreview{Project: req.Project, Outcome: "delete_project_and_all_p_data", Attachments: []string{}, Sessions: []control.ProjectDeleteSession{}, Images: []control.EnvironmentCollectionClaim{}, Warnings: []string{
		"All P refs, reachable and unreachable bare Git objects, runtime workspaces, private home files and runtime-local credentials will be deleted. Private and credential contents outside the workspace are not enumerated or hashed.",
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
	requested := 0
	for _, s := range sessions {
		l.mu.Lock()
		attached := l.hasAttachmentLocked(s.UUID) || l.startActive[s.UUID]
		l.mu.Unlock()
		if attached {
			return p, fmt.Errorf("%w: detach and Stop all project sessions, then obtain fresh workspace loss inspections", control.ErrConflict)
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
			return p, errors.Join(e, errors.New("exact session credentials/endpoints required; use existing principal/record repair or investigate manually"))
		}
		ps.Local = *local
		if observed.Exists {
			lossID := req.LossOperations[s.UUID]
			if observed.Status != "Stopped" || lossID == "" || missing[s.UUID] {
				return p, fmt.Errorf("%w: Stop/detach session %s then supply its completed fresh loss_operation_id", control.ErrConflict, s.UUID)
			}
			lossOp, e := l.store.GetOperation(ctx, lossID)
			var proof control.WorkspaceInspectEvidence
			var loss workspaceLossResult
			finished, tErr := time.Parse(time.RFC3339Nano, lossOp.UpdatedAt)
			if e != nil || tErr != nil || time.Since(finished) < 0 || time.Since(finished) > removalPreviewTTL || lossOp.Kind != "workspace.loss.inspect" || lossOp.Status != "completed" || lossOp.Phase != "inspected" || lossOp.SessionUUID != s.UUID || lossOp.Project != s.Project || json.Unmarshal(lossOp.Evidence, &proof) != nil || proof.CreatorOperationID != "" || proof.InstanceUUID != l.instanceID || proof.BaseFingerprint != l.cfg.BaseImageFingerprint || proof.ImageFingerprint != native.ImageFingerprint || proof.OriginalStatus != "Stopped" || proof.SourceIncusUUID != observed.IncusUUID || proof.SourceGeneration != observed.Generation || len(proof.Result) > 12<<10 || json.Unmarshal(proof.Result, &loss) != nil || loss.Schema != "p.workspace-loss/v1" || !loss.RuntimeDataWillBeRemoved || len(loss.Fingerprint) != 64 || len(loss.Worktrees) != 1 || loss.Worktrees[0].Path != "/workspace" || len(loss.ExternalWorktrees) != 0 {
				return p, errors.Join(e, errors.New("recent exact stopped standalone workspace loss proof required"))
			}
			ps.Runtime.Condition = "present"
			ps.Runtime.IncusUUID = observed.IncusUUID
			ps.Runtime.Generation = observed.Generation
			ps.Runtime.ImageFingerprint = native.ImageFingerprint
			ps.Runtime.OriginalStatus = "Stopped"
			ps.Runtime.LossOperationID = lossID
			ps.Runtime.ObservedAt = lossOp.UpdatedAt
			ps.Runtime.Fingerprint = loss.Fingerprint
			ps.Runtime.Loss = proof.Result
		} else {
			if !missing[s.UUID] || req.LossOperations[s.UUID] != "" {
				return p, fmt.Errorf("%w: positively missing runtime %s requires acknowledge_missing", control.ErrConflict, s.UUID)
			}
			if e = l.runtime.ConfirmSessionRuntimeAbsent(ctx, native); e != nil {
				return p, e
			}
			ps.Condition = "missing"
			ps.Runtime.Condition = "missing"
			ps.Runtime.RuntimeLossUnknown = true
		}
		requested++
		p.Sessions = append(p.Sessions, ps)
	}
	if requested != len(req.LossOperations)+len(missing) {
		return p, control.ErrInvalid
	}
	branchLoss, err := l.git.backend.ProjectRemovalLoss(ctx, req.Project)
	if err != nil {
		return p, err
	}
	p.BranchLoss, err = l.makeDeleteReviewInScope(ctx, req.Project, branchLoss, scope)
	if err != nil {
		return p, err
	}
	for _, ps := range p.Sessions {
		if ps.Runtime.Condition == "present" {
			var loss workspaceLossResult
			_ = json.Unmarshal(ps.Runtime.Loss, &loss)
			if !slices.EqualFunc(loss.PRefs, branchLoss.PRefs, func(a workspaceLossRef, b plugin.GitRef) bool { return a.Name == b.Ref && a.OID == b.OID }) {
				return p, control.ErrConflict
			}
		}
	}
	entries, next, err := l.store.ListEnvironmentImages(ctx, req.Project, "", 20)
	if err != nil || next != "" {
		return p, errors.Join(err, errors.New("project cache exceeds aggregate review bound"))
	}
	for _, entry := range entries {
		claim, _, e := l.observeCollection(ctx, entry)
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
	var p control.ProjectDeletePreview
	err = l.git.backend.WithOrigin(ctx, req.Project, func(scope *gitservice.OriginScope) error {
		var e error
		p, e = l.projectDeleteFacts(ctx, req, scope)
		return e
	})
	if err != nil {
		return p, err
	}
	token, err := collectionToken()
	if err != nil {
		return p, err
	}
	expiry := time.Now().Add(removalPreviewTTL)
	for _, s := range p.Sessions {
		if s.Runtime.Condition == "present" {
			finished, _ := time.Parse(time.RFC3339Nano, s.Runtime.ObservedAt)
			if finished.Add(removalPreviewTTL).Before(expiry) {
				expiry = finished.Add(removalPreviewTTL)
			}
		}
	}
	p.ConfirmationToken = token
	p.ExpiresAt = expiry.UTC().Format(time.RFC3339Nano)
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > control.MaxFrameBytes-1024 {
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
func (l *lifecycle) ConfirmProjectDelete(ctx context.Context, req control.ProjectDeleteConfirmRequest) (control.Operation, error) {
	if req.Key == "" || len(req.Key) > 128 || !validRetainedDeleteToken(req.ConfirmationToken) {
		return control.Operation{}, control.ErrInvalid
	}
	saved := control.ProjectDeleteRequest{Key: req.Key, Project: req.Project, TokenSHA256: hexDigest([]byte(req.ConfirmationToken))}
	replay := func() (control.Operation, bool, error) {
		op, e := l.store.GetOperationByKey(ctx, req.Key)
		if errors.Is(e, control.ErrNotFound) {
			return op, false, nil
		}
		var old control.ProjectDeleteRequest
		if e != nil || op.Kind != "project.delete" || json.Unmarshal(op.Request, &old) != nil || old != saved {
			return control.Operation{}, true, errors.Join(e, control.ErrConflict)
		}
		return op, true, nil
	}
	if op, found, e := replay(); found || e != nil {
		return op, e
	}
	projectRelease, err := l.lockProjectDelete(ctx, req.Project)
	if err != nil {
		return control.Operation{}, err
	}
	defer projectRelease()
	if op, found, e := replay(); found || e != nil {
		return op, e
	}
	l.mu.Lock()
	state, found := l.projectDeletePreviews[req.ConfirmationToken]
	l.mu.Unlock()
	if !found || state.Request.Project != req.Project || !time.Now().Before(state.Expiry) {
		return control.Operation{}, control.ErrConflict
	}
	sessions, sha, err := l.store.ProjectDeleteSnapshot(ctx, req.Project)
	if err != nil || sha != state.Preview.StateSHA256 {
		return control.Operation{}, errors.Join(err, control.ErrConflict)
	}
	release, err := l.lockProjectDeleteSessions(ctx, sessions)
	if err != nil {
		return control.Operation{}, err
	}
	defer release()
	// Durable read helpers own their existing inspection guards. Cancellation or
	// daemon failure leaves that read recoverable and never retires the project.
	for _, ps := range state.Preview.Sessions {
		if ps.Runtime.Condition != "present" {
			continue
		}
		token, e := collectionToken()
		if e != nil {
			return control.Operation{}, e
		}
		proof := control.WorkspaceInspectEvidence{InstanceUUID: l.instanceID, ImageFingerprint: ps.Runtime.ImageFingerprint, BaseFingerprint: l.cfg.BaseImageFingerprint, SourceIncusUUID: ps.Runtime.IncusUUID, SourceGeneration: ps.Runtime.Generation, OriginalStatus: "Stopped"}
		op, e := l.store.BeginWorkspaceLossInspect(ctx, control.WorkspaceInspectRequest{Key: "project-delete-review:" + token, SessionUUID: ps.Session.UUID}, proof)
		if e != nil {
			return control.Operation{}, e
		}
		if e = l.enqueue(op.ID); e != nil {
			return control.Operation{}, e
		}
		for {
			current, e := l.store.GetOperation(ctx, op.ID)
			if e != nil {
				return control.Operation{}, e
			}
			if current.Status == "completed" {
				var inspected control.WorkspaceInspectEvidence
				var loss workspaceLossResult
				if json.Unmarshal(current.Evidence, &inspected) != nil || json.Unmarshal(inspected.Result, &loss) != nil || loss.Fingerprint != ps.Runtime.Fingerprint {
					return control.Operation{}, fmt.Errorf("%w: workspace changed; obtain a new aggregate loss preview", control.ErrConflict)
				}
				break
			}
			if current.Status != "running" {
				return control.Operation{}, fmt.Errorf("%w: fresh inspection %s incomplete; Retry/investigate that read before a new project review", control.ErrConflict, current.ID)
			}
			select {
			case <-ctx.Done():
				return control.Operation{}, ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	var accepted control.Operation
	err = l.git.backend.WithOrigin(ctx, req.Project, func(scope *gitservice.OriginScope) error {
		return func() error {
			verify := func(call context.Context) error {
				fresh, e := l.projectDeleteFacts(call, state.Request, scope)
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
				ev.Sessions[i].Runtime.Loss = nil
				ev.Resources = append(ev.Resources, control.ProjectDeleteResource{Kind: "runtime", ID: ev.Sessions[i].Session.UUID, Status: "remaining"}, control.ProjectDeleteResource{Kind: "local", ID: ev.Sessions[i].Session.UUID, Status: "remaining"})
			}
			for _, im := range ev.Images {
				ev.Resources = append(ev.Resources, control.ProjectDeleteResource{Kind: "image", ID: im.Image.Key, Status: "remaining"})
			}
			ev.Resources = append(ev.Resources, control.ProjectDeleteResource{Kind: "repository", ID: "bare", Status: "remaining"})
			var e error
			accepted, e = l.store.BeginProjectDelete(ctx, saved, ev, state.Preview.ExpiresAt, verify)
			return e
		}()
	})
	if err != nil {
		return control.Operation{}, err
	}
	l.mu.Lock()
	delete(l.projectDeletePreviews, req.ConfirmationToken)
	l.mu.Unlock()
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
	for key, v := range l.collectionPreviews {
		if v.Claim.Image.Project == project {
			delete(l.collectionPreviews, key)
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
	for key, v := range l.repairPreviews {
		if v.Preview.Project == project {
			delete(l.repairPreviews, key)
		}
	}
	for key, v := range l.refRepairPreviews {
		if v.Preview.Project == project {
			delete(l.refRepairPreviews, key)
		}
	}
	for key, v := range l.principalRepairPreviews {
		if v.Preview.Project == project {
			delete(l.principalRepairPreviews, key)
		}
	}
	for key, v := range l.recordRepairPreviews {
		if v.Preview.Project == project {
			delete(l.recordRepairPreviews, key)
		}
	}
	delete(l.changedPolicies, project)
}
