package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

const removalPreviewTTL = 2 * time.Minute

type removalPreviewState struct {
	Preview api.RemovalPreview
	Expiry  time.Time
}

// PreviewRemoval creates no destructive intent. P does not look inside a
// present container: the preview states that it and everything in it is lost,
// and a running session is stopped as part of removing it.
func (l *lifecycle) PreviewRemoval(ctx context.Context, req control.RemovalPreviewRequest) (api.RemovalPreview, error) {
	if req.Kind != "discard" && req.Kind != "delete" || len(req.UUID) != 36 {
		return api.RemovalPreview{}, control.ErrInvalid
	}
	release, err := l.lockSession(ctx, req.UUID)
	if err != nil {
		return api.RemovalPreview{}, err
	}
	defer release()
	target := api.TargetSession(req.UUID)
	if l.hasAttachment(req.UUID) {
		return api.RemovalPreview{}, control.Conflict(api.ReasonAttachmentActive, target)
	}
	if err := l.checkNoSessionChange(ctx, req.UUID); err != nil {
		return api.RemovalPreview{}, err
	}
	session, err := l.store.GetSession(ctx, req.UUID)
	if err != nil {
		return api.RemovalPreview{}, err
	}
	if session.Registry != "established" {
		return api.RemovalPreview{}, control.Conflict(api.ReasonSessionNotEstablished, target)
	}
	native, err := l.runtimeSession(ctx, session)
	if err != nil {
		return api.RemovalPreview{}, err
	}
	observed, err := l.runtime.Inspect(ctx, native)
	if err != nil {
		return api.RemovalPreview{}, control.Failed(api.ReasonRuntime, err) // unreachable is not missing
	}
	preview := api.RemovalPreview{Kind: req.Kind, SessionUUID: session.UUID, Project: session.Project,
		Branch: session.Branch, AssignedRef: "refs/heads/" + session.Branch, PolicySHA256: session.PolicySHA256,
		Runtime: api.RemovalRuntimePreview{IncusProject: l.cfg.IncusProject, InstanceName: "p-" + session.UUID}}
	if !observed.Exists {
		if !req.AcknowledgeMissingRuntime {
			return api.RemovalPreview{}, control.Conflict(api.ReasonRuntimeMissing, target)
		}
		if err := l.runtime.ConfirmSessionRuntimeAbsent(ctx, native); err != nil {
			return api.RemovalPreview{}, runtimeFailure(err, target)
		}
		preview.Runtime.Condition = "missing"
		preview.Runtime.RuntimeLossUnknown = true
	} else {
		switch {
		case req.AcknowledgeMissingRuntime:
			// The client acknowledged a missing runtime that is present.
			return api.RemovalPreview{}, control.Stale(api.ReasonChanged)
		case observed.Name != preview.Runtime.InstanceName || observed.IncusUUID == "" || observed.Generation == "":
			return api.RemovalPreview{}, control.Failed(api.ReasonRuntime, errors.New("session runtime identity incomplete"))
		case !removableStatus(observed.Status):
			return api.RemovalPreview{}, control.Conflict(api.ReasonRuntimeUnsettled, target)
		}
		preview.Runtime.Condition, preview.Runtime.IncusUUID, preview.Runtime.Generation = "present", observed.IncusUUID, observed.Generation
		preview.Runtime.ImageFingerprint, preview.Runtime.OriginalStatus = observed.Fingerprint, observed.Status
		preview.Runtime.RuntimeDataLost = true
	}
	var branchLoss gitservice.BranchRemovalLoss
	if req.Kind == "delete" {
		branchLoss, err = l.git.backend.BranchRemovalLoss(ctx, session.Project, session.Branch, true)
	} else {
		branchLoss, err = l.git.backend.AssignedBranchSnapshot(ctx, session.Project, session.Branch, true)
	}
	if err != nil {
		return api.RemovalPreview{}, gitFailure(err)
	}
	preview.AssignedTip, preview.AssignedRefMissing = branchLoss.AssignedTip, branchLoss.AssignedTip == ""
	if req.Kind == "delete" {
		preview.BranchLoss = deleteReview(branchLoss)
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return api.RemovalPreview{}, err
	}
	preview.ConfirmationToken = hex.EncodeToString(token[:])
	expires := time.Now().UTC().Add(removalPreviewTTL)
	preview.ExpiresAt = expires.Format(time.RFC3339Nano)
	if err := checkRemovalPreviewFrame(preview); err != nil {
		return api.RemovalPreview{}, control.Conflict(api.ReasonBoundExceeded, target)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.removalPreviews == nil {
		l.removalPreviews = make(map[string]removalPreviewState)
	}
	for key, state := range l.removalPreviews {
		if !time.Now().Before(state.Expiry) {
			delete(l.removalPreviews, key)
		}
	}
	if len(l.removalPreviews) >= 128 {
		return api.RemovalPreview{}, control.Retry(api.ReasonTooManyPreviews, "")
	}
	l.removalPreviews[preview.ConfirmationToken] = removalPreviewState{Preview: preview, Expiry: expires}
	return preview, nil
}

// removableStatus reports whether removal accepts a present session runtime
// in this Incus status: P stops a running one itself before deleting it.
func removableStatus(status string) bool {
	return status == "Running" || status == "Stopped"
}

// runtimeFailure answers a failed check of a session's runtime: a container of
// the session that is present where its absence was to be proven is a
// conflict on target; anything else is a runtime failure.
func runtimeFailure(err error, target string) error {
	var present *runtimeincus.RuntimePresentError
	if errors.As(err, &present) {
		return &control.Refusal{Kind: api.KindConflict, Reason: api.ReasonRuntimePresent, Target: target, Cause: err}
	}
	return control.Failed(api.ReasonRuntime, err)
}

// gitFailure answers a failed read of P's repository: refs that moved while
// they were read can be read again.
func gitFailure(err error) error {
	if errors.Is(err, gitservice.ErrRefsMoving) {
		return &control.Refusal{Kind: api.KindRetry, Reason: api.ReasonRefsMoved, Cause: err}
	}
	return control.Failed(api.ReasonGit, err)
}

func checkRemovalPreviewFrame(preview api.RemovalPreview) error {
	encoded, err := json.Marshal(api.RemovalPreviewResult{Preview: preview})
	if err != nil || len(encoded) > api.MaxFrameBytes-1024 {
		return errors.New("removal preview exceeds bounded control response")
	}
	return nil
}
