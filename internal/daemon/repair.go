package daemon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
	"github.com/lgvo/p.ai/internal/plugin"
	"github.com/lgvo/p.ai/internal/runtimeincus"
	"github.com/lgvo/p.ai/internal/runtimekit"
	"golang.org/x/crypto/ssh"
)

const repairPreviewTTL = 2 * time.Minute

type repairPreviewState struct {
	Preview control.RepairPreview
	Expiry  time.Time
}

// A preview must never create or rotate the P-owned session credential.
func inspectRegisteredSessionKey(stateDir, uuid string) (string, error) {
	if len(uuid) != 36 {
		return "", control.ErrInvalid
	}
	dir := filepath.Join(stateDir, "session_keys")
	parent, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	owner, ok := parent.Sys().(*syscall.Stat_t)
	if !ok || !parent.IsDir() || parent.Mode().Perm() != 0700 || owner.Uid != uint32(os.Geteuid()) {
		return "", errors.New("session key directory unavailable")
	}
	name := filepath.Join(dir, uuid)
	fd, err := syscall.Open(name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() < 1 || info.Size() > 4096 || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) {
		return "", errors.New("session key file unavailable")
	}
	data := make([]byte, info.Size())
	if _, err = io.ReadFull(f, data); err != nil {
		return "", err
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return "", errors.New("session key identity unavailable")
	}
	return gitservice.Fingerprint(signer.PublicKey()), nil
}

func (l *lifecycle) repairSelection(ctx context.Context, s control.Session) (control.CreationSelection, *control.CreationEvidence, error) {
	op, err := l.store.CreationForSession(ctx, s.UUID)
	if err != nil {
		return control.CreationSelection{}, nil, err
	}
	if op.Kind == "project.create" {
		var ev control.BlankProjectEvidence
		if json.Unmarshal(op.Evidence, &ev) != nil || !ev.Selection.Valid() {
			return control.CreationSelection{}, nil, control.ErrInvalid
		}
		return ev.Selection, nil, nil
	}
	ev, err := control.Evidence(op)
	if err != nil {
		return control.CreationSelection{}, nil, err
	}
	return ev.Selection, &ev, nil
}

func (l *lifecycle) PreviewRepair(ctx context.Context, id string) (control.RepairPreview, error) {
	return l.previewRepair(ctx, id, "")
}

func (l *lifecycle) PreviewPreparedRepair(ctx context.Context, id, preparationID string) (control.RepairPreview, error) {
	if len(preparationID) != 36 {
		return control.RepairPreview{}, control.ErrInvalid
	}
	return l.previewRepair(ctx, id, preparationID)
}

func (l *lifecycle) previewRepair(ctx context.Context, id, preparationID string) (control.RepairPreview, error) {
	if len(id) != 36 {
		return control.RepairPreview{}, control.ErrInvalid
	}
	release, err := l.lockSession(ctx, id)
	if err != nil {
		return control.RepairPreview{}, err
	}
	defer release()
	s, err := l.store.GetSession(ctx, id)
	if err != nil {
		return control.RepairPreview{}, err
	}
	if s.Registry != "established" {
		return control.RepairPreview{}, control.ErrConflict
	}
	native, err := l.runtimeSession(ctx, s)
	if err != nil {
		return control.RepairPreview{}, err
	}
	preview := control.RepairPreview{Kind: "missing_runtime", SessionUUID: id, Project: s.Project, Branch: s.Branch,
		ImageFingerprint: native.ImageFingerprint, PolicySHA256: s.PolicySHA256, IncusProject: l.cfg.IncusProject,
		InstanceName: "p-" + id, RuntimeLocalLoss: "unrecoverable", ImageStatus: "missing"}
	if l.policyCondition(ctx, s) == "invalid" {
		preview.BlockedReason = "policy_invalid"
		return preview, nil
	}
	observed, err := l.runtime.Inspect(ctx, native)
	if err != nil {
		return control.RepairPreview{}, err
	} // unreachable cannot be called missing
	if observed.Exists {
		preview.BlockedReason = "runtime_present"
		return preview, nil
	}
	image, err := l.runtime.ImagePresent(ctx, native.ImageFingerprint)
	if err != nil {
		return control.RepairPreview{}, err
	}
	if image {
		preview.ImageStatus = "present"
	}
	tip, exists, err := l.git.backend.InspectBranchRef(ctx, s.Project, s.Branch)
	if err != nil {
		return control.RepairPreview{}, err
	}
	if exists {
		preview.AssignedTip = tip
		proof, snapshotErr := l.git.backend.AssignedBranchSnapshot(ctx, s.Project, s.Branch, false)
		if snapshotErr != nil || proof.AssignedTip != tip {
			preview.BlockedReason = "assigned_branch_unavailable"
			return preview, nil
		}
	} else {
		preview.BlockedReason = "assigned_branch_uncommitted"
		return preview, nil
	}
	credential, registered, err := l.store.SessionGitPrincipal(ctx, id)
	if err != nil {
		return control.RepairPreview{}, err
	}
	if registered {
		preview.CredentialFingerprint = credential
	}
	active, err := l.store.IsSessionGitPrincipalActive(ctx, id)
	if err != nil {
		return control.RepairPreview{}, err
	}
	if !registered || !active {
		preview.BlockedReason = "credential_unavailable"
		return preview, nil
	}
	keyFingerprint, keyErr := inspectRegisteredSessionKey(l.store.StateDir(), id)
	if keyErr != nil || keyFingerprint != credential {
		preview.BlockedReason = "credential_unavailable"
		return preview, nil
	}
	currentPolicy, err := l.store.ProjectPolicySHA(ctx, s.Project)
	if err != nil {
		return control.RepairPreview{}, err
	}
	if currentPolicy != s.PolicySHA256 {
		preview.BlockedReason = "policy_changed"
		return preview, nil
	}
	selection, creation, err := l.repairSelection(ctx, s)
	if err != nil {
		return control.RepairPreview{}, err
	}
	if creation != nil {
		preview.ImageSourceCommit = creation.CapturedOID
	}
	if accepted, ok, e := l.store.AcceptedRepairImage(ctx, id); e != nil {
		return control.RepairPreview{}, e
	} else if ok {
		if accepted.ImageFingerprint != native.ImageFingerprint {
			preview.BlockedReason = "image_provenance_unavailable"
			return preview, nil
		}
		if accepted.EnvironmentSourceCommit != "" {
			preview.ImageSourceCommit = accepted.EnvironmentSourceCommit
		} else {
			preview.ImageSourceCommit = accepted.ImageSourceCommit
		}
	}
	preview.ImageSourceDiffers = preview.ImageSourceCommit != preview.AssignedTip
	if selection != l.selection() {
		preview.BlockedReason = "plugin_selection_changed"
		return preview, nil
	}
	if err = l.checkNoWorkspaceInspect(ctx, id); err != nil {
		preview.BlockedReason = "operation_active"
		return preview, nil
	}
	l.mu.Lock()
	attached := l.hasAttachmentLocked(id)
	l.mu.Unlock()
	if attached {
		preview.BlockedReason = "attachment_active"
		return preview, nil
	}
	if !image {
		if preparationID == "" {
			preview.BlockedReason = "recorded_image_missing"
			return preview, nil
		}
		prepared, prepErr := l.store.CompletedRepairPreparation(ctx, preparationID, id)
		if prepErr != nil {
			preview.BlockedReason = "preparation_unavailable"
			return preview, nil
		}
		if prepared.Project != s.Project || prepared.Branch != s.Branch || prepared.AssignedTip != preview.AssignedTip ||
			prepared.PolicySHA256 != s.PolicySHA256 || prepared.CredentialFingerprint != preview.CredentialFingerprint ||
			prepared.RecordedImageFingerprint != preview.ImageFingerprint || prepared.RecordedSourceCommit != preview.ImageSourceCommit ||
			prepared.Selection != selection || prepared.IncusProject != l.cfg.IncusProject ||
			prepared.InstanceName != preview.InstanceName || prepared.InstanceUUID != l.instanceID ||
			l.environmentIntent() == nil || prepared.Environment != *l.environmentIntent() {
			preview.BlockedReason = "preparation_stale"
			return preview, nil
		}
		candidate := &control.RepairEnvironmentPreview{SourceCommit: prepared.AssignedTip, System: prepared.Environment.System,
			Selection: prepared.EnvironmentSelection, EnvironmentKey: prepared.EnvironmentKey,
			BaseImageFingerprint: prepared.Environment.BaseFingerprint, RecordedImageFingerprint: preview.ImageFingerprint,
			RecordedEnvironmentKey: prepared.RecordedEnvironmentKey, SourceDiffers: prepared.AssignedTip != prepared.RecordedSourceCommit}
		candidate.IdentityDiffers = prepared.EnvironmentKey != prepared.RecordedEnvironmentKey
		if prepared.EnvironmentKey == "" {
			present, e := l.runtime.ImagePresent(ctx, prepared.Environment.BaseFingerprint)
			if e != nil {
				return control.RepairPreview{}, e
			}
			if !present {
				preview.BlockedReason = "base_image_missing"
				return preview, nil
			}
			candidate.CandidateImageFingerprint, candidate.ImageStatus = prepared.Environment.BaseFingerprint, "base"
		} else {
			entry, found, e := l.store.GetEnvironmentImage(ctx, s.Project, prepared.EnvironmentKey)
			if e != nil {
				return control.RepairPreview{}, e
			}
			candidate.ImageStatus = "needs_build"
			if found {
				_, present, verifyErr := l.runtime.VerifyBuilderImage(ctx, imageClaimFromCache(entry))
				if verifyErr != nil {
					return control.RepairPreview{}, verifyErr
				}
				if present {
					candidate.CandidateImageFingerprint, candidate.ImageStatus = entry.Fingerprint, "cache_hit"
				}
			}
		}
		preview.Environment, preview.PreparationOperationID = candidate, preparationID
	} else if preparationID != "" {
		preview.BlockedReason = "preparation_stale"
		return preview, nil
	}
	var token [16]byte
	if _, err = rand.Read(token[:]); err != nil {
		return control.RepairPreview{}, err
	}
	preview.Eligible = true
	preview.ConfirmationToken = hex.EncodeToString(token[:])
	expiry := time.Now().UTC().Add(repairPreviewTTL)
	preview.ExpiresAt = expiry.Format(time.RFC3339Nano)
	encoded, err := json.Marshal(map[string]any{"v": 1, "preview": preview})
	if err != nil || len(encoded) > control.MaxFrameBytes-1024 {
		return control.RepairPreview{}, errors.New("repair preview exceeds bounded response")
	}
	l.mu.Lock()
	if l.repairPreviews == nil {
		l.repairPreviews = map[string]repairPreviewState{}
	}
	for key, state := range l.repairPreviews {
		if !time.Now().Before(state.Expiry) {
			delete(l.repairPreviews, key)
		}
	}
	if len(l.repairPreviews) >= 128 {
		l.mu.Unlock()
		return control.RepairPreview{}, control.ErrConflict
	}
	l.repairPreviews[preview.ConfirmationToken] = repairPreviewState{Preview: preview, Expiry: expiry}
	l.mu.Unlock()
	return preview, nil
}

func (l *lifecycle) ConfirmRepair(ctx context.Context, req control.RepairConfirmRequest) (control.Operation, error) {
	if len(req.Key) < 1 || len(req.Key) > 128 || len(req.UUID) != 36 || len(req.ConfirmationToken) != 32 {
		return control.Operation{}, control.ErrInvalid
	}
	release, err := l.lockSession(ctx, req.UUID)
	if err != nil {
		return control.Operation{}, err
	}
	defer release()
	sum := sha256.Sum256([]byte(req.ConfirmationToken))
	pinned := control.RepairRequest{Key: req.Key, UUID: req.UUID, TokenSHA256: hex.EncodeToString(sum[:])}
	if prior, e := l.store.GetOperationByKey(ctx, req.Key); e == nil {
		var old control.RepairRequest
		if prior.Kind != "session.repair" || json.Unmarshal(prior.Request, &old) != nil || old != pinned {
			return control.Operation{}, control.ErrConflict
		}
		if prior.Status == "running" {
			_ = l.enqueue(prior.ID)
		}
		return prior, nil
	} else if !errors.Is(e, control.ErrNotFound) {
		return control.Operation{}, e
	}
	l.mu.Lock()
	state, found := l.repairPreviews[req.ConfirmationToken]
	attached := l.hasAttachmentLocked(req.UUID)
	l.mu.Unlock()
	if !found || attached || !state.Preview.Eligible || state.Preview.SessionUUID != req.UUID || !time.Now().Before(state.Expiry) {
		return control.Operation{}, control.ErrConflict
	}
	if err = l.checkNoWorkspaceInspect(ctx, req.UUID); err != nil {
		return control.Operation{}, err
	}
	s, err := l.store.GetSession(ctx, req.UUID)
	if err != nil {
		return control.Operation{}, err
	}
	if s.Registry != "established" || s.Project != state.Preview.Project || s.Branch != state.Preview.Branch || s.PolicySHA256 != state.Preview.PolicySHA256 {
		return control.Operation{}, control.ErrConflict
	}
	if l.policyCondition(ctx, s) == "invalid" {
		return control.Operation{}, control.ErrConflict
	}
	currentPolicy, err := l.store.ProjectPolicySHA(ctx, s.Project)
	if err != nil || currentPolicy != s.PolicySHA256 {
		return control.Operation{}, control.ErrConflict
	}
	selection, creation, err := l.repairSelection(ctx, s)
	if err != nil || selection != l.selection() {
		return control.Operation{}, control.ErrConflict
	}
	imageSource := ""
	if creation != nil {
		imageSource = creation.CapturedOID
	}
	if accepted, ok, e := l.store.AcceptedRepairImage(ctx, req.UUID); e != nil {
		return control.Operation{}, e
	} else if ok {
		if accepted.EnvironmentSourceCommit != "" {
			imageSource = accepted.EnvironmentSourceCommit
		} else {
			imageSource = accepted.ImageSourceCommit
		}
	}
	if imageSource != state.Preview.ImageSourceCommit {
		return control.Operation{}, control.ErrConflict
	}
	keyFingerprint, keyErr := inspectRegisteredSessionKey(l.store.StateDir(), req.UUID)
	if keyErr != nil || keyFingerprint != state.Preview.CredentialFingerprint {
		return control.Operation{}, control.ErrConflict
	}
	native, err := l.runtimeSession(ctx, s)
	if err != nil || native.ImageFingerprint != state.Preview.ImageFingerprint {
		return control.Operation{}, control.ErrConflict
	}
	ev := control.RepairEvidence{Project: s.Project, Branch: s.Branch, AssignedTip: state.Preview.AssignedTip,
		PolicySHA256: s.PolicySHA256, CredentialFingerprint: state.Preview.CredentialFingerprint,
		IncusProject: l.cfg.IncusProject, InstanceName: "p-" + s.UUID, InstanceUUID: l.instanceID,
		ImageFingerprint: native.ImageFingerprint, ImageSourceCommit: imageSource, BaseFingerprint: l.cfg.BaseImageFingerprint, PreviewExpiresAt: state.Preview.ExpiresAt}
	if accepted, ok, e := l.store.AcceptedRepairImage(ctx, req.UUID); e != nil {
		return control.Operation{}, e
	} else if ok {
		ev.EnvironmentSourceCommit, ev.EnvironmentSelection, ev.EnvironmentKey = accepted.EnvironmentSourceCommit, accepted.EnvironmentSelection, accepted.EnvironmentKey
		ev.EnvironmentState = accepted.EnvironmentState
		ev.Environment = accepted.Environment
	}
	if state.Preview.PreparationOperationID != "" {
		prepared, prepErr := l.store.CompletedRepairPreparation(ctx, state.Preview.PreparationOperationID, req.UUID)
		if prepErr != nil || state.Preview.Environment == nil || prepared.AssignedTip != ev.AssignedTip ||
			prepared.Project != ev.Project || prepared.Branch != ev.Branch || prepared.PolicySHA256 != ev.PolicySHA256 ||
			prepared.CredentialFingerprint != ev.CredentialFingerprint || prepared.RecordedImageFingerprint != ev.ImageFingerprint ||
			prepared.RecordedSourceCommit != ev.ImageSourceCommit || prepared.Selection != selection ||
			l.environmentIntent() == nil || prepared.Environment != *l.environmentIntent() ||
			prepared.EnvironmentKey != state.Preview.Environment.EnvironmentKey ||
			prepared.EnvironmentSelection != state.Preview.Environment.Selection {
			return control.Operation{}, control.ErrConflict
		}
		prepOp, prepErr := l.store.GetOperation(ctx, state.Preview.PreparationOperationID)
		if prepErr != nil || prepOp.Status != "completed" || prepOp.Kind != "session.repair.prepare" {
			return control.Operation{}, control.ErrConflict
		}
		sum := sha256.Sum256(prepOp.Evidence)
		ev.PreparationOperationID, ev.PreparationSHA256 = prepOp.ID, hex.EncodeToString(sum[:])
		ev.RecordedImageFingerprint = native.ImageFingerprint
		ev.ImageFingerprint = state.Preview.Environment.CandidateImageFingerprint
		ev.EnvironmentKey, ev.EnvironmentSelection = prepared.EnvironmentKey, prepared.EnvironmentSelection
		ev.EnvironmentSourceCommit = prepared.AssignedTip
		ev.Environment = &prepared.Environment
		ev.BuilderTreeOID = prepared.BuilderTreeOID
		if ev.EnvironmentKey == "" {
			ev.EnvironmentState = &control.EnvironmentState{BuilderRequest: prepOp.ID, FlakePresent: prepared.FlakePresent, Fingerprint: ev.ImageFingerprint}
		} else if ev.ImageFingerprint != "" {
			entry, found, cacheErr := l.store.GetEnvironmentImage(ctx, ev.Project, ev.EnvironmentKey)
			if cacheErr != nil || !found || entry.Fingerprint != ev.ImageFingerprint {
				return control.Operation{}, control.ErrConflict
			}
			ev.EnvironmentState = environmentStateFromCache(entry, true)
		}
	}
	verify := func(call context.Context) error {
		if err := l.repairCommittedTip(call, ev); err != nil {
			return err
		}
		fingerprint, e := inspectRegisteredSessionKey(l.store.StateDir(), req.UUID)
		if e != nil || fingerprint != ev.CredentialFingerprint {
			return control.ErrConflict
		}
		observed, e := l.runtime.Inspect(call, native)
		if e != nil {
			return e
		}
		if observed.Exists {
			return control.ErrConflict
		}
		present, e := l.runtime.ImagePresent(call, native.ImageFingerprint)
		if e != nil {
			return e
		}
		if ev.PreparationOperationID == "" && !present || ev.PreparationOperationID != "" && present {
			return control.ErrConflict
		}
		if ev.PreparationOperationID != "" {
			base, e := l.runtime.ImagePresent(call, ev.Environment.BaseFingerprint)
			if e != nil {
				return e
			}
			if !base {
				return control.ErrConflict
			}
			if ev.ImageFingerprint != "" && ev.EnvironmentKey != "" {
				_, available, e := l.runtime.VerifyBuilderImage(call, imageClaimFromCache(control.EnvironmentImage{Project: ev.Project, Key: ev.EnvironmentKey,
					Fingerprint: ev.ImageFingerprint, BaseFingerprint: ev.Environment.BaseFingerprint,
					System: ev.Environment.System, MaterialDigest: ev.EnvironmentState.MaterialDigest,
					CaptureStorePath: ev.EnvironmentState.CaptureStorePath, BuilderRequest: ev.EnvironmentState.BuilderRequest,
					Properties: ev.EnvironmentState.Properties}))
				if e != nil || !available {
					return control.ErrConflict
				}
			}
		}
		return nil
	}
	op, err := l.store.BeginRepair(ctx, pinned, ev, verify, l.observeSessionCapacity)
	if err != nil {
		return control.Operation{}, err
	}
	l.mu.Lock()
	delete(l.repairPreviews, req.ConfirmationToken)
	l.mu.Unlock()
	if err = l.enqueue(op.ID); err != nil {
		return op, err
	}
	return op, nil
}

func (l *lifecycle) repairAssignedTip(ctx context.Context, ev control.RepairEvidence) error {
	tip, exists, err := l.git.backend.InspectBranchRef(ctx, ev.Project, ev.Branch)
	if err != nil {
		return err
	}
	if !exists || tip != ev.AssignedTip {
		return control.ErrConflict
	}
	return nil
}

func (l *lifecycle) repairCommittedTip(ctx context.Context, ev control.RepairEvidence) error {
	if err := l.repairAssignedTip(ctx, ev); err != nil {
		return err
	}
	proof, err := l.git.backend.AssignedBranchSnapshot(ctx, ev.Project, ev.Branch, false)
	if err != nil {
		return err
	}
	if proof.AssignedTip != ev.AssignedTip {
		return control.ErrConflict
	}
	return nil
}

func (l *lifecycle) repairAdvance(op *control.Operation, ev control.RepairEvidence, phase string) error {
	raw, err := json.Marshal(ev)
	if err != nil || len(raw) > 16384 {
		return control.ErrInvalid
	}
	committed := phase != "reserved" && phase != "guarded"
	if err = l.store.AdvanceOperation(l.ctx, op.ID, "running", phase, committed, raw, ""); err != nil {
		return err
	}
	op.Phase, op.Evidence, op.Status = phase, raw, "running"
	op.Committed = committed
	return nil
}

func (l *lifecycle) repairBlock(op control.Operation, err error) {
	if l.ctx.Err() != nil {
		return
	}
	message := fmt.Sprintf("repair blocked: %v", err)
	if len(message) > 512 {
		message = message[:512]
	}
	_ = l.store.AdvanceOperation(l.ctx, op.ID, "blocked", op.Phase, op.Committed, op.Evidence, message)
}

func (l *lifecycle) repairAssembly(ctx context.Context, s control.Session, ev control.RepairEvidence, key []byte, creation *control.CreationEvidence) (runtimeincus.Assembly, error) {
	policy, policyErr := control.ParseStoredProjectPolicy(s.Policy)
	if policyErr != nil || policy.Network == "public-egress" && l.cfg.PublicEgress == nil || control.RevalidateProjectPolicy(policy, l.grantBoundary()) != nil {
		return runtimeincus.Assembly{}, control.ErrConflict
	}
	activation := runtimekit.Config{Schema: "p.runtime-session/v1", Activation: "base", Command: policy.Command}
	workspace := runtimekit.WorkspaceConfig{Schema: "p.workspace/v3", Repository: s.Project, Branch: s.Branch, InitialOID: ev.AssignedTip,
		EnvironmentCommitOID: ev.ImageSourceCommit, EnvironmentSelection: "base-recorded", ImageFingerprint: ev.ImageFingerprint}
	if ev.EnvironmentSourceCommit != "" {
		if ev.EnvironmentState == nil || ev.PreparationOperationID != "" && ev.EnvironmentSourceCommit != ev.AssignedTip {
			return runtimeincus.Assembly{}, control.ErrConflict
		}
		workspace.EnvironmentCommitOID = ev.EnvironmentSourceCommit
		workspace.EnvironmentSelection = ev.EnvironmentSelection
		if ev.EnvironmentSelection == "devshell" {
			if ev.EnvironmentState.Key != ev.EnvironmentKey || ev.EnvironmentState.MaterialDigest == "" {
				return runtimeincus.Assembly{}, control.ErrConflict
			}
			workspace.EnvironmentKey = ev.EnvironmentKey
			activation.Schema = "p.runtime-session/v2"
			activation.Activation = "devshell"
			activation.MaterialSHA256 = ev.EnvironmentState.MaterialDigest
		}
	} else if creation != nil && creation.Environment != nil && creation.EnvironmentState != nil {
		state := creation.EnvironmentState
		if state.Key != "" {
			activation.Schema = "p.runtime-session/v2"
			activation.Activation = "devshell"
			activation.MaterialSHA256 = state.MaterialDigest
		}
		workspace.EnvironmentCommitOID = ev.ImageSourceCommit
		workspace.EnvironmentSelection = "base-no-flake"
		if state.Key != "" {
			workspace.EnvironmentSelection = "devshell"
			workspace.EnvironmentKey = state.Key
		} else if state.FlakePresent {
			workspace.EnvironmentSelection = "base-no-default"
		}
	}
	if ev.PreparationOperationID == "" && creation != nil && creation.Environment != nil && creation.EnvironmentState == nil {
		return runtimeincus.Assembly{}, control.ErrConflict
	}
	if l.agentPlan != nil {
		if len(l.agentPlan.Files) != 1 || l.agentPlan.Files[0].Role != "p-codex-adapter" {
			return runtimeincus.Assembly{}, control.ErrConflict
		}
		activation.Schema = "p.runtime-session/v3"
		activation.AgentSHA256 = l.agentPlan.Files[0].SHA256
	}
	if len(policy.FilesystemMounts) != 0 {
		activation.Schema = "p.runtime-session/v4"
		activation.FilesystemMounts = guestFilesystemGrants(policy)
	}
	address, err := l.publicSessionIPv4(ctx, s.UUID, policy)
	if err != nil {
		return runtimeincus.Assembly{}, err
	}
	if address != "" {
		activation.Schema = "p.runtime-session/v5"
		activation.PublicNetwork = l.guestPublicNetwork(address)
	}
	return runtimeincus.Assembly{HostAssets: l.hostPlan, SourceAssets: l.sourcePlan, AgentAssets: l.agentPlan,
		SessionConfig: activation, Workspace: workspace, Identity: key, ServerPublicKey: l.git.info.HostPublicKey}, nil
}

func (l *lifecycle) processRepair(op control.Operation) {
	if op.Status != "running" {
		return
	}
	var ev control.RepairEvidence
	if json.Unmarshal(op.Evidence, &ev) != nil || ev.InstanceUUID != l.instanceID || ev.BaseFingerprint != l.cfg.BaseImageFingerprint || ev.IncusProject != l.cfg.IncusProject || ev.InstanceName != "p-"+op.SessionUUID {
		l.repairBlock(op, errors.New("durable repair identity unavailable"))
		return
	}
	ctx := l.ctx
	s, err := l.store.GetSession(ctx, op.SessionUUID)
	if err != nil || s.Registry != "established" || s.Project != ev.Project || s.Branch != ev.Branch || s.PolicySHA256 != ev.PolicySHA256 {
		l.repairBlock(op, errors.Join(err, errors.New("repair assignment changed")))
		return
	}
	if l.policyCondition(ctx, s) == "invalid" {
		l.repairBlock(op, errors.New("current project policy authority unavailable"))
		return
	}
	if ev.PreparationOperationID != "" {
		prepared, e := l.store.GetOperation(ctx, ev.PreparationOperationID)
		if e != nil || prepared.Kind != "session.repair.prepare" || prepared.Status != "completed" || prepared.SessionUUID != s.UUID {
			l.repairBlock(op, errors.New("completed repair preparation unavailable"))
			return
		}
		sum := sha256.Sum256(prepared.Evidence)
		if hex.EncodeToString(sum[:]) != ev.PreparationSHA256 {
			l.repairBlock(op, errors.New("prepared environment identity changed"))
			return
		}
	}
	native, err := l.runtimeSession(ctx, s)
	recorded := ev.ImageFingerprint
	if ev.PreparationOperationID != "" {
		recorded = ev.RecordedImageFingerprint
	}
	if err != nil || native.ImageFingerprint != recorded {
		l.repairBlock(op, errors.Join(err, errors.New("recorded repair image changed")))
		return
	}
	if ev.PreparationOperationID != "" && ev.ImageFingerprint != "" {
		native.ImageFingerprint = ev.ImageFingerprint
	}
	// The repaired workspace starts at the freshly confirmed committed P tip,
	// which may have advanced since the original creation evidence.
	native.InitialOID = ev.AssignedTip
	selection, creation, err := l.repairSelection(ctx, s)
	if err != nil || selection != l.selection() {
		l.repairBlock(op, errors.New("pinned repair selection changed"))
		return
	}
	imageSource := ""
	if creation != nil {
		imageSource = creation.CapturedOID
	}
	if accepted, ok, e := l.store.AcceptedRepairImage(ctx, s.UUID); e != nil {
		l.repairBlock(op, e)
		return
	} else if ok {
		if accepted.EnvironmentSourceCommit != "" {
			imageSource = accepted.EnvironmentSourceCommit
		} else {
			imageSource = accepted.ImageSourceCommit
		}
	}
	if imageSource != ev.ImageSourceCommit {
		l.repairBlock(op, errors.New("recorded image provenance changed"))
		return
	}
	for step := 0; step < 24; step++ {
		switch op.Phase {
		case "reserved":
			if err = l.repairCommittedTip(ctx, ev); err != nil {
				_ = l.store.FailRepairPreInit(ctx, op.ID)
				return
			}
			observed, e := l.runtime.Inspect(ctx, native)
			if e != nil {
				l.repairBlock(op, e)
				return
			}
			if observed.Exists {
				_ = l.store.FailRepairPreInit(ctx, op.ID)
				return
			}
			if err = l.repairAdvance(&op, ev, "guarded"); err != nil {
				l.repairBlock(op, err)
				return
			}
		case "guarded":
			currentPolicy, policyErr := l.store.ProjectPolicySHA(ctx, s.Project)
			if policyErr != nil || currentPolicy != ev.PolicySHA256 {
				if policyErr != nil {
					l.repairBlock(op, policyErr)
				} else {
					_ = l.store.FailRepairPreInit(ctx, op.ID)
				}
				return
			}
			active, principalErr := l.store.IsSessionGitPrincipalActive(ctx, s.UUID)
			if principalErr != nil || !active {
				if principalErr != nil {
					l.repairBlock(op, principalErr)
				} else {
					_ = l.store.FailRepairPreInit(ctx, op.ID)
				}
				return
			}
			if err = l.repairCommittedTip(ctx, ev); err != nil {
				_ = l.store.FailRepairPreInit(ctx, op.ID)
				return
			}
			observed, e := l.runtime.Inspect(ctx, native)
			if e != nil {
				l.repairBlock(op, e)
				return
			}
			if observed.Exists {
				_ = l.store.FailRepairPreInit(ctx, op.ID)
				return
			}
			if ev.PreparationOperationID != "" {
				if e = l.repairImageAdvanceToReady(ctx, &op, &ev); e != nil {
					if op.Phase == "guarded" {
						if finishErr := l.store.FailRepairPreInit(ctx, op.ID); finishErr != nil {
							l.repairBlock(op, errors.Join(e, finishErr))
						}
					} else {
						l.repairBlock(op, e)
					}
					return
				}
				native.ImageFingerprint = ev.ImageFingerprint
				continue
			}
			present, e := l.runtime.ImagePresent(ctx, ev.ImageFingerprint)
			if e != nil {
				l.repairBlock(op, e)
				return
			}
			if !present {
				_ = l.store.FailRepairPreInit(ctx, op.ID)
				return
			}
			if e = l.repairIssueRuntimeCreate(ctx, s, native, &op, ev); e != nil {
				if op.Phase == "guarded" {
					_ = l.store.FailRepairPreInit(ctx, op.ID)
				} else {
					l.repairBlock(op, e)
				}
				return
			}
			// The accepted native identity is captured only after positive reinspection.
		case "image-builder-init-issued", "image-builder-created", "image-source-transfer-issued", "image-source-ready", "image-builder-start-issued", "image-builder-running", "environment-publishing", "image-builder-delete-issued", "image-builder-absent":
			if err = l.repairImageAdvanceToReady(ctx, &op, &ev); err != nil {
				l.repairBlock(op, err)
				return
			}
			native.ImageFingerprint = ev.ImageFingerprint
		case "image-ready":
			if ev.PreparationOperationID == "" || ev.ImageFingerprint == "" {
				l.repairBlock(op, control.ErrConflict)
				return
			}
			native.ImageFingerprint = ev.ImageFingerprint
			currentPolicy, policyErr := l.store.ProjectPolicySHA(ctx, s.Project)
			if policyErr != nil || currentPolicy != ev.PolicySHA256 || l.policyCondition(ctx, s) == "invalid" {
				l.repairBlock(op, errors.Join(policyErr, errors.New("repair project policy changed before runtime init")))
				return
			}
			oldPresent, imageErr := l.runtime.ImagePresent(ctx, ev.RecordedImageFingerprint)
			if imageErr != nil || oldPresent {
				l.repairBlock(op, errors.Join(imageErr, errors.New("recorded image state changed before runtime init")))
				return
			}
			if err = l.repairCommittedTip(ctx, ev); err != nil {
				l.repairBlock(op, err)
				return
			}
			observed, e := l.runtime.Inspect(ctx, native)
			if e != nil || observed.Exists {
				l.repairBlock(op, errors.Join(e, errors.New("repair source changed before runtime init")))
				return
			}
			if err = l.repairIssueRuntimeCreate(ctx, s, native, &op, ev); err != nil {
				l.repairBlock(op, err)
				return
			}
		case "init-issued":
			observed, e := l.runtime.InspectCreated(ctx, native)
			if e != nil || !observed.Exists || observed.Status != "Stopped" || observed.IncusUUID == "" || observed.Generation == "" {
				l.repairBlock(op, errors.Join(e, errors.New("repair init outcome unresolved")))
				return
			}
			// An exact partial init may still need its endpoint. The gate refuses
			// another init if the instance disappeared after the positive read.
			scoped := runtimeincus.Scoped{Backend: l.runtime, Session: native, BeforeCreate: func() error { return control.ErrConflict }}
			if _, e = plugin.RunRuntime(ctx, l.runtimePlugin, "runtime.create", scoped); e != nil {
				l.repairBlock(op, e)
				return
			}
			ev.IncusUUID, ev.Generation = observed.IncusUUID, observed.Generation
			if err = l.repairAdvance(&op, ev, "runtime-created"); err != nil {
				l.repairBlock(op, err)
				return
			}
		case "runtime-created":
			observed, e := l.runtime.Inspect(ctx, native)
			if e != nil || !repairExact(observed, ev, "Stopped") {
				l.repairBlock(op, errors.Join(e, errors.New("repair runtime identity unavailable")))
				return
			}
			key, pub, e := l.sessionKey(ctx, s.UUID)
			if e != nil || pub == nil || gitservice.Fingerprint(pub) != ev.CredentialFingerprint {
				l.repairBlock(op, errors.Join(e, errors.New("repair credential changed")))
				return
			}
			assembly, e := l.repairAssembly(ctx, s, ev, key, creation)
			if e != nil {
				l.repairBlock(op, e)
				return
			}
			scoped := runtimeincus.Scoped{Backend: l.runtime, Session: native, Assembly: &assembly}
			if _, e = plugin.RunRuntime(ctx, l.runtimePlugin, "runtime.assemble", scoped); e != nil {
				l.repairBlock(op, e)
				return
			}
			if err = l.repairAdvance(&op, ev, "assembly-ready"); err != nil {
				l.repairBlock(op, err)
				return
			}
		case "assembly-ready":
			observed, e := l.runtime.Inspect(ctx, native)
			if e != nil || !repairExact(observed, ev, "Stopped") {
				l.repairBlock(op, errors.Join(e, errors.New("assembled repair runtime changed")))
				return
			}
			if err = l.repairAdvance(&op, ev, "start-issued"); err != nil {
				l.repairBlock(op, err)
				return
			}
			scoped := runtimeincus.Scoped{Backend: l.runtime, Session: native}
			if _, e = plugin.RunRuntime(ctx, l.runtimePlugin, "runtime.start", scoped); e != nil {
				l.repairBlock(op, e)
				return
			}
		case "start-issued":
			observed, e := l.runtime.Inspect(ctx, native)
			if e != nil || !repairExact(observed, ev, "Running") {
				l.repairBlock(op, errors.Join(e, errors.New("repair start outcome unresolved")))
				return
			}
			scoped := runtimeincus.Scoped{Backend: l.runtime, Session: native}
			if e = l.repairAwaitHost(ctx, scoped, ev); e != nil {
				l.repairBlock(op, e)
				return
			}
			if err = l.repairAdvance(&op, ev, "host-ready"); err != nil {
				l.repairBlock(op, err)
				return
			}
		case "host-ready":
			if err = l.store.CompleteRepair(ctx, op.ID, func(call context.Context, accepted control.RepairEvidence) error {
				if e := l.repairCommittedTip(call, accepted); e != nil {
					return e
				}
				observed, e := l.runtime.Inspect(call, native)
				if e != nil {
					return e
				}
				if !repairExact(observed, accepted, "Running") {
					return control.ErrConflict
				}
				return nil
			}); err != nil {
				l.repairBlock(op, err)
				return
			}
			l.recordProgress(op, "completed")
			return
		default:
			l.repairBlock(op, errors.New("repair phase unavailable"))
			return
		}
	}
	l.repairBlock(op, errors.New("repair phase bound exceeded"))
}

func repairExact(o runtimeincus.Observation, ev control.RepairEvidence, status string) bool {
	return o.Exists && o.Name == ev.InstanceName && o.Status == status && o.IncusUUID == ev.IncusUUID && o.Generation == ev.Generation && o.Fingerprint == ev.ImageFingerprint && o.EndpointMounted
}

func (l *lifecycle) repairIssueRuntimeCreate(ctx context.Context, s control.Session, native runtimeincus.Session, op *control.Operation, ev control.RepairEvidence) error {
	_, pub, err := l.sessionKey(ctx, s.UUID)
	if err != nil || pub == nil || gitservice.Fingerprint(pub) != ev.CredentialFingerprint {
		return errors.Join(err, errors.New("repair credential changed"))
	}
	dir, err := l.endpoints.Ensure(ctx, s.UUID)
	if err != nil || dir != native.EndpointSource {
		return errors.Join(err, errors.New("repair endpoint unavailable"))
	}
	scoped := runtimeincus.Scoped{Backend: l.runtime, Session: native, BeforeCreate: func() error { return l.repairAdvance(op, ev, "init-issued") }}
	_, err = plugin.RunRuntime(ctx, l.runtimePlugin, "runtime.create", scoped)
	return err
}

func (l *lifecycle) repairAwaitHost(ctx context.Context, scoped runtimeincus.Scoped, ev control.RepairEvidence) error {
	deadline, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()
	var last error
	for {
		observed, err := l.runtime.Inspect(deadline, scoped.Session)
		if err != nil || !repairExact(observed, ev, "Running") {
			return errors.Join(err, errors.New("repair runtime changed during readiness"))
		}
		state, err := plugin.RunRuntime(deadline, l.runtimePlugin, "runtime.observe-host", scoped)
		if err != nil {
			last = err
		} else if state.HostReady {
			return nil
		} else {
			if state.DiagnosticAvailable && state.Diagnostic != "" {
				last = errors.New(state.Diagnostic)
			}
			if state.Status != "Running" || len(state.HostUnit) >= 7 && state.HostUnit[:7] == "failed/" {
				return errors.Join(last, errors.New("repair interactive host failed"))
			}
		}
		select {
		case <-deadline.Done():
			return errors.Join(last, errors.New("repair host readiness timed out"))
		case <-time.After(250 * time.Millisecond):
		}
	}
}
