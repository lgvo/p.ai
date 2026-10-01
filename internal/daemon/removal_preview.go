package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
	"github.com/lgvo/p.ai/internal/plugin"
)

const removalPreviewTTL = 2 * time.Minute

type removalPreviewState struct {
	Preview control.RemovalPreview
	Expiry  time.Time
}

// PreviewRemoval creates no destructive intent. A present-runtime preview
// cites one completed, recent 8a2 snapshot; its contents are explicitly
// historical until confirmation re-quiesces and recomputes them.
func (l *lifecycle) PreviewRemoval(ctx context.Context, req control.RemovalPreviewRequest) (control.RemovalPreview, error) {
	if req.Kind != "discard" && req.Kind != "delete" || len(req.UUID) != 36 ||
		(req.LossOperationID == "") == !req.AcknowledgeMissingRuntime || len(req.LossOperationID) > 128 {
		return control.RemovalPreview{}, control.ErrInvalid
	}
	release, err := l.lockSession(ctx, req.UUID)
	if err != nil {
		return control.RemovalPreview{}, err
	}
	defer release()
	l.mu.Lock()
	attached := l.hasAttachmentLocked(req.UUID)
	l.mu.Unlock()
	if attached {
		return control.RemovalPreview{}, control.ErrConflict
	}
	if err := l.checkNoWorkspaceInspect(ctx, req.UUID); err != nil {
		return control.RemovalPreview{}, err
	}
	session, err := l.store.GetSession(ctx, req.UUID)
	if err != nil || session.Registry != "established" {
		return control.RemovalPreview{}, errors.Join(err, control.ErrConflict)
	}
	created, err := l.store.CreationForSession(ctx, session.UUID)
	if err != nil {
		return control.RemovalPreview{}, err
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
	var lossFinished time.Time
	if !observed.Exists {
		if !req.AcknowledgeMissingRuntime || req.LossOperationID != "" {
			return control.RemovalPreview{}, control.ErrConflict
		}
		if err := l.runtime.ConfirmSessionRuntimeAbsent(ctx, native); err != nil {
			return control.RemovalPreview{}, errors.Join(err, control.ErrConflict)
		}
		preview.Runtime.Condition = "missing"
		preview.Runtime.RuntimeLossUnknown = true
	} else {
		if req.AcknowledgeMissingRuntime || req.LossOperationID == "" ||
			observed.Name != preview.Runtime.InstanceName || observed.Status != "Running" && observed.Status != "Stopped" {
			return control.RemovalPreview{}, control.ErrConflict
		}
		lossOp, err := l.store.GetOperation(ctx, req.LossOperationID)
		if err != nil || lossOp.Kind != "workspace.loss.inspect" || lossOp.SessionUUID != session.UUID || lossOp.Project != session.Project || lossOp.Status != "completed" {
			return control.RemovalPreview{}, errors.Join(err, control.ErrConflict)
		}
		finished, err := time.Parse(time.RFC3339Nano, lossOp.UpdatedAt)
		if err != nil || time.Since(finished) < 0 || time.Since(finished) > removalPreviewTTL {
			return control.RemovalPreview{}, control.ErrConflict
		}
		lossFinished = finished
		var ev control.WorkspaceInspectEvidence
		var loss workspaceLossResult
		if json.Unmarshal(lossOp.Evidence, &ev) != nil || len(ev.Result) == 0 || len(ev.Result) > 12<<10 ||
			json.Unmarshal(ev.Result, &loss) != nil || loss.Schema != "p.workspace-loss/v1" || !loss.RuntimeDataWillBeRemoved ||
			len(loss.Fingerprint) != 64 || ev.InstanceUUID != l.instanceID || ev.BaseFingerprint != l.cfg.BaseImageFingerprint ||
			ev.ImageFingerprint != native.ImageFingerprint || ev.SourceIncusUUID != observed.IncusUUID ||
			ev.SourceGeneration != observed.Generation || ev.OriginalStatus != observed.Status {
			return control.RemovalPreview{}, control.ErrConflict
		}
		preview.Runtime.Condition, preview.Runtime.IncusUUID, preview.Runtime.Generation = "present", observed.IncusUUID, observed.Generation
		preview.Runtime.ImageFingerprint, preview.Runtime.OriginalStatus = observed.Fingerprint, observed.Status
		preview.Runtime.LossOperationID, preview.Runtime.ObservedAt = lossOp.ID, lossOp.UpdatedAt
		preview.Runtime.Fingerprint, preview.Runtime.Loss = loss.Fingerprint, ev.Result
	}
	var branchLoss gitservice.BranchRemovalLoss
	if req.Kind == "delete" {
		branchLoss, err = l.git.backend.BranchRemovalLoss(ctx, session.Project, session.Branch, created.Kind == "project.create")
	} else {
		branchLoss, err = l.git.backend.AssignedBranchSnapshot(ctx, session.Project, session.Branch, created.Kind == "project.create")
	}
	if err != nil {
		return control.RemovalPreview{}, err
	}
	preview.AssignedTip = branchLoss.AssignedTip
	if preview.Runtime.Condition == "present" {
		var loss workspaceLossResult
		if json.Unmarshal(preview.Runtime.Loss, &loss) != nil || !slices.EqualFunc(loss.PRefs, branchLoss.PRefs,
			func(a workspaceLossRef, b plugin.GitRef) bool { return a.Name == b.Ref && a.OID == b.OID }) {
			return control.RemovalPreview{}, control.ErrConflict
		}
	}
	if req.Kind == "delete" {
		preview.BranchLoss, err = l.makeDeleteReview(ctx, session.Project, branchLoss)
		if err != nil {
			return control.RemovalPreview{}, err
		}
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return control.RemovalPreview{}, err
	}
	preview.ConfirmationToken = hex.EncodeToString(token[:])
	now := time.Now().UTC()
	if !lossFinished.IsZero() && (now.Before(lossFinished) || now.Sub(lossFinished) >= removalPreviewTTL) {
		return control.RemovalPreview{}, control.ErrConflict
	}
	expires := now.Add(removalPreviewTTL)
	if !lossFinished.IsZero() && lossFinished.Add(removalPreviewTTL).Before(expires) {
		expires = lossFinished.Add(removalPreviewTTL)
	}
	preview.ExpiresAt = expires.Format(time.RFC3339Nano)
	if err := checkRemovalPreviewFrame(preview); err != nil {
		return control.RemovalPreview{}, err
	}
	l.mu.Lock()
	if l.removalPreviews == nil {
		l.removalPreviews = make(map[string]removalPreviewState)
	}
	for key, state := range l.removalPreviews {
		if !time.Now().Before(state.Expiry) {
			delete(l.removalPreviews, key)
		}
	}
	if len(l.removalPreviews) >= 128 {
		l.mu.Unlock()
		return control.RemovalPreview{}, control.ErrConflict
	}
	l.removalPreviews[preview.ConfirmationToken] = removalPreviewState{Preview: preview, Expiry: expires}
	l.mu.Unlock()
	return preview, nil
}

func checkRemovalPreviewFrame(preview control.RemovalPreview) error {
	encoded, err := json.Marshal(map[string]any{"v": 1, "preview": preview})
	if err != nil || len(encoded) > control.MaxFrameBytes-1024 {
		return errors.New("removal preview exceeds bounded control response")
	}
	return nil
}
