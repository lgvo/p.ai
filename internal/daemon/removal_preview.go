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
)

const removalPreviewTTL = 2 * time.Minute

type removalPreviewState struct {
	Preview control.RemovalPreview
	Expiry  time.Time
}

// PreviewRemoval creates no destructive intent. P does not look inside a
// present container: the preview states that it and everything in it is lost,
// and a running session is stopped as part of removing it.
func (l *lifecycle) PreviewRemoval(ctx context.Context, req control.RemovalPreviewRequest) (control.RemovalPreview, error) {
	if req.Kind != "discard" && req.Kind != "delete" || len(req.UUID) != 36 {
		return control.RemovalPreview{}, control.ErrInvalid
	}
	release, err := l.lockSession(ctx, req.UUID)
	if err != nil {
		return control.RemovalPreview{}, err
	}
	defer release()
	attached := l.hasAttachment(req.UUID)
	if attached {
		return control.RemovalPreview{}, control.ErrConflict
	}
	if err := l.checkNoSessionChange(ctx, req.UUID); err != nil {
		return control.RemovalPreview{}, err
	}
	session, err := l.store.GetSession(ctx, req.UUID)
	if err != nil || session.Registry != "established" {
		return control.RemovalPreview{}, errors.Join(err, control.ErrConflict)
	}
	native, err := l.runtimeSession(ctx, session)
	if err != nil {
		return control.RemovalPreview{}, err
	}
	observed, err := l.runtime.Inspect(ctx, native)
	if err != nil {
		return control.RemovalPreview{}, err // unreachable is not missing
	}
	preview := control.RemovalPreview{Kind: req.Kind, SessionUUID: session.UUID, Project: session.Project,
		Branch: session.Branch, AssignedRef: "refs/heads/" + session.Branch, PolicySHA256: session.PolicySHA256,
		Runtime: control.RemovalRuntimePreview{IncusProject: l.cfg.IncusProject, InstanceName: "p-" + session.UUID}}
	if !observed.Exists {
		if !req.AcknowledgeMissingRuntime {
			return control.RemovalPreview{}, control.ErrConflict
		}
		if err := l.runtime.ConfirmSessionRuntimeAbsent(ctx, native); err != nil {
			return control.RemovalPreview{}, errors.Join(err, control.ErrConflict)
		}
		preview.Runtime.Condition = "missing"
		preview.Runtime.RuntimeLossUnknown = true
	} else {
		if req.AcknowledgeMissingRuntime || observed.Name != preview.Runtime.InstanceName || !removableStatus(observed.Status) ||
			observed.IncusUUID == "" || observed.Generation == "" {
			return control.RemovalPreview{}, control.ErrConflict
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
		return control.RemovalPreview{}, err
	}
	preview.AssignedTip, preview.AssignedRefMissing = branchLoss.AssignedTip, branchLoss.AssignedTip == ""
	if req.Kind == "delete" {
		preview.BranchLoss = deleteReview(branchLoss)
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return control.RemovalPreview{}, err
	}
	preview.ConfirmationToken = hex.EncodeToString(token[:])
	expires := time.Now().UTC().Add(removalPreviewTTL)
	preview.ExpiresAt = expires.Format(time.RFC3339Nano)
	if err := checkRemovalPreviewFrame(preview); err != nil {
		return control.RemovalPreview{}, err
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
		return control.RemovalPreview{}, control.ErrConflict
	}
	l.removalPreviews[preview.ConfirmationToken] = removalPreviewState{Preview: preview, Expiry: expires}
	return preview, nil
}

// removableStatus reports whether removal accepts a present session runtime
// in this Incus status: P stops a running one itself before deleting it.
func removableStatus(status string) bool {
	return status == "Running" || status == "Stopped"
}

func checkRemovalPreviewFrame(preview control.RemovalPreview) error {
	encoded, err := json.Marshal(map[string]any{"v": 1, "preview": preview})
	if err != nil || len(encoded) > api.MaxFrameBytes-1024 {
		return errors.New("removal preview exceeds bounded control response")
	}
	return nil
}
