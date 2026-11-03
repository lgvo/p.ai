package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// lockProjectDelete admits one project deletion call of project at a time;
// another one holding it answers retry.
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

// reviewCommitImage binds one indexed image's presence, size and related
// sessions into the claim a project deletion reviews.
func (l *lifecycle) reviewCommitImage(ctx context.Context, row control.CommitImage) (control.CommitImageClaim, error) {
	claim := control.CommitImageClaim{Image: row, ObservedSize: row.Size}
	img, found, err := l.images.InspectImage(ctx, runtimedriver.ImageRef(row.Ref))
	if err != nil {
		return claim, imageFailure(err)
	}
	if found {
		if !maps.Equal(img.Labels, row.Labels) {
			return claim, imageFailure(fmt.Errorf("%w: image %s differs from its index row", runtimedriver.ErrMismatch, row.Ref))
		}
		claim.ImagePresent, claim.ObservedSize = true, img.Size
	}
	claim.RelatedDigest, err = l.store.CommitSessionsDigest(ctx, row.Key.Project, row.Key.Commit)
	return claim, err
}
func projectDeleteNamedResources(sessions []api.ProjectDeleteSession, images []control.CommitImageClaim) (map[string]bool, map[string]bool) {
	names, refs := map[string]bool{}, map[string]bool{}
	for _, s := range sessions {
		names[s.Runtime.InstanceName] = true
	}
	for _, c := range images {
		refs[c.Image.Ref] = true
	}
	return names, refs
}

// DeleteProject answers a deletion of project: its plan, or the operation of
// a run. P stops running sessions itself; an attached terminal refuses the
// deletion.
func (l *lifecycle) DeleteProject(ctx context.Context, project string, req api.ChangeRequest) (api.ChangeResult[api.ProjectDeleteDetails], error) {
	recorded := control.ProjectDeleteRequest{Key: req.Key, Project: project, IfMatch: req.IfMatch}
	// claims are the image claims of the latest plan, which a run's
	// evidence records; the plan shows them without their index rows' times.
	var claims []control.CommitImageClaim
	change := control.Change[api.ProjectDeleteDetails]{
		Action: api.ProjectDelete.Name, Target: api.TargetProject(project), Instance: l.instanceID,
		Hold: func(ctx context.Context, fn func(context.Context) error) error {
			release, err := l.lockProjectDelete(ctx, project)
			if err != nil {
				return err
			}
			defer release()
			sessions, _, err := l.store.ProjectDeleteSnapshot(ctx, project)
			if err != nil {
				var refusal *control.Refusal
				if errors.As(err, &refusal) && refusal.Kind == api.KindConflict {
					// Planning answers this refusal in the plan.
					return fn(ctx)
				}
				return err
			}
			releaseSessions, err := l.lockProjectDeleteSessions(ctx, sessions)
			if err != nil {
				return err
			}
			defer releaseSessions()
			return fn(ctx)
		},
		Plan: func(ctx context.Context, p *api.Plan[api.ProjectDeleteDetails]) error {
			var err error
			claims, err = l.planProjectDelete(ctx, project, p)
			return err
		},
		Replay: func(ctx context.Context) (api.Operation, bool, error) {
			return replayRequest(l, ctx, req.Key, control.KindProjectDelete, recorded)
		},
		Begin: func(ctx context.Context, run control.Run[api.ProjectDeleteDetails]) (api.Operation, error) {
			ev := l.projectDeleteEvidence(run.Plan.Details, claims)
			var accepted api.Operation
			err := l.git.backend.WithOrigin(ctx, project, func(*gitservice.OriginScope) error {
				var e error
				accepted, e = l.store.BeginProjectDelete(ctx, recorded, ev, run.Hash, run.Recheck)
				return e
			})
			if err != nil {
				return api.Operation{}, err
			}
			return accepted, l.enqueue(accepted.ID)
		},
	}
	return change.Do(ctx, req)
}

// planProjectDelete plans deleting project into p, and returns the image
// claims it shows.
func (l *lifecycle) planProjectDelete(ctx context.Context, project string, p *api.Plan[api.ProjectDeleteDetails]) ([]control.CommitImageClaim, error) {
	claims := []control.CommitImageClaim{}
	d := &p.Details
	*d = api.ProjectDeleteDetails{Project: project, Outcome: "delete_project_and_all_p_data", ExternalMounts: []api.FilesystemGrant{},
		Attachments: []string{}, Sessions: []api.ProjectDeleteSession{}, Images: []api.CommitImageClaim{}}
	sessions, sha, err := l.store.ProjectDeleteSnapshot(ctx, project)
	if err != nil {
		return nil, err
	}
	d.StateSHA256 = sha
	projectPolicy, projectSHA, e := l.store.ProjectPolicyRecord(ctx, project)
	if e != nil {
		return nil, e
	}
	captured, e := control.ParseStoredProjectPolicy(projectPolicy)
	if e != nil {
		return nil, e
	}
	d.ProjectPolicySHA256 = projectSHA
	d.ExternalMounts = control.GrantViews(captured.FilesystemMounts)
	for _, s := range sessions {
		target := api.TargetSession(s.UUID)
		if l.hasAttachment(s.UUID) {
			return nil, control.Conflict(api.ReasonAttachmentActive, target)
		}
		if e := l.admitSessionWork(ctx, s.UUID, ""); e != nil {
			return nil, e
		}
		native, e := l.runtimeSession(ctx, s)
		if e != nil {
			return nil, e
		}
		observed, e := l.runtime.Inspect(ctx, native)
		if e != nil {
			return nil, control.Failed(api.ReasonRuntime, e)
		}
		ps := api.ProjectDeleteSession{Session: api.SessionRecord(s), Runtime: api.RemovalRuntime{IncusProject: l.cfg.IncusProject, InstanceName: "p-" + s.UUID}}
		creator, e := l.store.CreationForSession(ctx, s.UUID)
		if e != nil {
			return nil, e
		}
		local, e := l.reviewCreateCleanupLocal(ctx, creator, s)
		if e != nil || local == nil {
			return nil, errors.Join(e, errors.New("exact session credentials/endpoints required; investigate manually"))
		}
		ps.Local = *local
		if observed.Exists {
			switch {
			case !removableStatus(observed.Status):
				return nil, control.Conflict(api.ReasonRuntimeUnsettled, target)
			case observed.IncusUUID == "" || observed.Generation == "":
				return nil, control.Failed(api.ReasonRuntime, fmt.Errorf("session runtime %s identity incomplete", s.UUID))
			}
			ps.Condition = strings.ToLower(observed.Status)
			ps.Runtime.Condition = "present"
			ps.Runtime.IncusUUID = observed.IncusUUID
			ps.Runtime.Generation = observed.Generation
			ps.Runtime.ImageFingerprint = native.ImageFingerprint
			ps.Runtime.OriginalStatus = observed.Status
			p.Loss = append(p.Loss, containerLoss(s.UUID))
		} else {
			if e = l.runtime.ConfirmSessionRuntimeAbsent(ctx, native); e != nil {
				return nil, runtimeFailure(e, target)
			}
			ps.Condition = "missing"
			ps.Runtime.Condition = "missing"
			p.Gone = append(p.Gone, missingRuntimeGone(s.UUID))
		}
		d.Sessions = append(d.Sessions, ps)
	}
	loss, err := l.git.backend.ProjectRemovalLoss(ctx, project)
	if err != nil {
		return nil, gitFailure(err)
	}
	d.BranchLoss = deleteReview(loss)
	p.Loss = append(p.Loss, branchLoss(api.TargetProject(project), d.BranchLoss)...)
	p.Loss = append(p.Loss, unreachableObjectsLoss(project))
	rows, more, err := l.store.ListCommitImages(ctx, project, control.CommitImageKey{}, control.MaxProjectDeleteImages)
	if err != nil {
		return nil, err
	}
	if more {
		return nil, control.Conflict(api.ReasonBoundExceeded, api.TargetProject(project))
	}
	for _, row := range rows {
		claim, e := l.reviewCommitImage(ctx, row)
		if e != nil {
			return nil, e
		}
		claims = append(claims, claim)
	}
	d.Images = control.CommitImageClaimViews(claims)
	names, images := projectDeleteNamedResources(d.Sessions, claims)
	if err = l.runtime.CheckProjectDeletionInventory(ctx, project, names, images); err != nil {
		return nil, control.Failed(api.ReasonRuntime, err)
	}
	repo, err := l.git.backend.RepositoryPath(project)
	if err != nil {
		return nil, control.Failed(api.ReasonGit, err)
	}
	d.Repository, err = replacementLocalIdentity(repo)
	if err != nil {
		return nil, err
	}
	d.RepositoryParent, err = replacementLocalIdentity(filepath.Dir(repo))
	if err != nil {
		return nil, err
	}
	if d.Repository.Mode&0170000 != 0040000 || d.Repository.Mode&0022 != 0 || d.RepositoryParent.Mode != 0040700 {
		return nil, control.ErrConflict
	}
	if err = l.boundProjectDeleteEvidence(*d, claims); err != nil {
		return nil, err
	}
	return claims, nil
}

// boundProjectDeleteEvidence refuses the plan d, with its image claims,
// whose deletion could not run because its evidence would exceed its bound.
func (l *lifecycle) boundProjectDeleteEvidence(d api.ProjectDeleteDetails, claims []control.CommitImageClaim) error {
	if !control.ProjectDeleteReviewFits(l.projectDeleteEvidence(d, claims)) {
		return control.Conflict(api.ReasonBoundExceeded, api.TargetProject(d.Project))
	}
	return nil
}

// projectDeleteEvidence is the evidence that deletes the plan d, with its
// image claims: every planned resource, remaining.
func (l *lifecycle) projectDeleteEvidence(d api.ProjectDeleteDetails, images []control.CommitImageClaim) control.ProjectDeleteEvidence {
	ev := control.ProjectDeleteEvidence{InstanceUUID: l.instanceID, BaseFingerprint: l.cfg.BaseImageFingerprint, IncusProject: l.cfg.IncusProject, StateSHA256: d.StateSHA256, Sessions: append([]api.ProjectDeleteSession(nil), d.Sessions...), Images: images, Repository: d.Repository, RepositoryParent: d.RepositoryParent}
	reviewed, _ := json.Marshal(d)
	ev.ReviewSHA256 = hexDigest(reviewed)
	for i := range ev.Sessions {
		ev.Resources = append(ev.Resources, control.ProjectDeleteResource{Kind: "runtime", ID: ev.Sessions[i].Session.UUID, Status: "remaining"}, control.ProjectDeleteResource{Kind: "local", ID: ev.Sessions[i].Session.UUID, Status: "remaining"})
	}
	for _, im := range ev.Images {
		ev.Resources = append(ev.Resources, control.ProjectDeleteResource{Kind: "image", ID: im.Image.Ref, Status: "remaining"})
	}
	ev.Resources = append(ev.Resources, control.ProjectDeleteResource{Kind: "repository", ID: "bare", Status: "remaining"})
	return ev
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

// forgetProject drops what the daemon keeps in memory for deleted project.
func (l *lifecycle) forgetProject(project string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.changedPolicies, project)
}
