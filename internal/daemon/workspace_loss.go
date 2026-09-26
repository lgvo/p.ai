package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

type workspaceLossRef struct {
	Name string `json:"name"`
	OID  string `json:"oid"`
}

type workspaceLossResult struct {
	Schema                   string                           `json:"schema"`
	Fingerprint              string                           `json:"fingerprint"`
	RuntimeDataWillBeRemoved bool                             `json:"runtime_data_will_be_removed"`
	Worktrees                []runtimeincus.WorkspaceLossTree `json:"worktrees"`
	ExternalWorktrees        []string                         `json:"external_worktrees"`
	LocalRefs                []runtimeincus.WorkspaceRef      `json:"local_refs"`
	LocalOnlyCommits         []string                         `json:"local_only_commits"`
	PRefs                    []workspaceLossRef               `json:"p_refs"`
}

func (l *lifecycle) inspectWorkspaceLossResult(ctx context.Context, session control.Session, source, helper runtimeincus.Session, ev control.WorkspaceInspectEvidence) (json.RawMessage, error) {
	loss, err := l.runtime.ExportWorkspaceLoss(ctx, source)
	if err != nil {
		return nil, err
	}
	if err := l.runtime.InstallWorkspaceLossCopy(ctx, helper, loss); err != nil {
		return nil, err
	}
	analysis, err := l.runtime.AnalyzeWorkspaceLossHelper(ctx, helper, loss)
	if err != nil {
		return nil, err
	}
	retained, localOnly, err := l.git.backend.RetainedLossRefs(ctx, session.Project, analysis.Commits)
	if err != nil {
		return nil, err
	}
	pRefs := make([]workspaceLossRef, 0, len(retained))
	for _, ref := range retained {
		pRefs = append(pRefs, workspaceLossRef{Name: ref.Ref, OID: ref.OID})
	}
	observed, err := l.runtime.Inspect(ctx, source)
	wantStatus := "Stopped"
	if ev.OriginalStatus == "Running" {
		wantStatus = "Frozen"
	}
	if err != nil || !observed.Exists || observed.Status != wantStatus || observed.Fingerprint != ev.ImageFingerprint ||
		observed.IncusUUID != ev.SourceIncusUUID || observed.Generation != ev.SourceGeneration || observed.Name == "" {
		return nil, errors.Join(err, errors.New("workspace source changed before loss result"))
	}
	// Native Inspect accepts only the captured root, endpoint and typed grant
	// devices. This loss walker analyzes runtime-owned worktrees only; a Git
	// pointer into any grant is unavailable, never silently classified or
	// copied to the helper. Grant contents are not removal targets.
	result := workspaceLossResult{Schema: "p.workspace-loss/v1", RuntimeDataWillBeRemoved: true,
		Worktrees: analysis.Worktrees, ExternalWorktrees: []string{}, LocalRefs: analysis.LocalRefs,
		LocalOnlyCommits: localOnly, PRefs: pRefs}
	fingerprint, err := workspaceLossFingerprint(session, l.cfg.IncusProject, observed.Name, ev, loss, result)
	if err != nil {
		return nil, err
	}
	result.Fingerprint = fingerprint
	return json.Marshal(result)
}

func workspaceLossFingerprint(session control.Session, incusProject, instanceName string, ev control.WorkspaceInspectEvidence,
	loss runtimeincus.WorkspaceLossSnapshot, result workspaceLossResult) (string, error) {
	canonical, err := json.Marshal(struct {
		SessionUUID      string
		Project          string
		Branch           string
		PolicySHA256     string
		PInstanceUUID    string
		IncusProject     string
		InstanceName     string
		IncusUUID        string
		Generation       string
		ImageFingerprint string
		OriginalStatus   string
		Snapshot         runtimeincus.WorkspaceLossSnapshot
		Result           workspaceLossResult
	}{session.UUID, session.Project, session.Branch, session.PolicySHA256,
		ev.InstanceUUID, incusProject, instanceName, ev.SourceIncusUUID, ev.SourceGeneration,
		ev.ImageFingerprint, ev.OriginalStatus, loss, result})
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(canonical)
	return hex.EncodeToString(hash[:]), nil
}
