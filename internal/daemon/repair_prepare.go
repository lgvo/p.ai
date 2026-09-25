package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/environmentnix"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

type repairPrepareRequest struct {
	Key  string `json:"key"`
	UUID string `json:"uuid"`
}

// PrepareRepair resolves an exact committed P tip in a disposable restricted
// builder. It never creates the session runtime or grants repair authority.
func (l *lifecycle) PrepareRepair(ctx context.Context, key, uuid string) (control.Operation, error) {
	if len(key) == 0 || len(key) > 128 || len(uuid) != 36 {
		return control.Operation{}, control.ErrInvalid
	}
	if prior, err := l.store.GetOperationByKey(ctx, key); err == nil {
		var req repairPrepareRequest
		if prior.Kind != "session.repair.prepare" || json.Unmarshal(prior.Request, &req) != nil || req.Key != key || req.UUID != uuid {
			return control.Operation{}, control.ErrConflict
		}
		if prior.Status == "running" {
			_ = l.enqueue(prior.ID)
		}
		return prior, nil
	} else if !errors.Is(err, control.ErrNotFound) {
		return control.Operation{}, err
	}
	preview, err := l.PreviewRepair(ctx, uuid)
	if err != nil {
		return control.Operation{}, err
	}
	if preview.BlockedReason != "recorded_image_missing" || preview.ImageStatus != "missing" || preview.AssignedTip == "" {
		return control.Operation{}, control.ErrConflict
	}
	release, err := l.lockSession(ctx, uuid)
	if err != nil {
		return control.Operation{}, err
	}
	defer release()
	s, err := l.store.GetSession(ctx, uuid)
	if err != nil || s.Registry != "established" || s.Project != preview.Project || s.Branch != preview.Branch || s.PolicySHA256 != preview.PolicySHA256 {
		return control.Operation{}, control.ErrConflict
	}
	selection, creation, err := l.repairSelection(ctx, s)
	if err != nil || selection != l.selection() || creation == nil || creation.Environment == nil || creation.EnvironmentState == nil || l.environmentIntent() == nil || *creation.Environment != *l.environmentIntent() {
		return control.Operation{}, control.ErrConflict
	}
	// The pinned base image is the builder's own root. Its absence is a
	// separate prerequisite failure, never a reason to substitute a host image.
	basePresent, err := l.runtime.ImagePresent(ctx, creation.Environment.BaseFingerprint)
	if err != nil {
		return control.Operation{}, err
	}
	if !basePresent || creation.Environment.BaseFingerprint != l.cfg.BaseImageFingerprint {
		return control.Operation{}, control.ErrConflict
	}
	native, err := l.runtimeSession(ctx, s)
	if err != nil || native.ImageFingerprint != preview.ImageFingerprint {
		return control.Operation{}, control.ErrConflict
	}
	ev := control.RepairPreparation{Project: s.Project, Branch: s.Branch, AssignedTip: preview.AssignedTip,
		PolicySHA256: s.PolicySHA256, CredentialFingerprint: preview.CredentialFingerprint,
		IncusProject: l.cfg.IncusProject, InstanceName: "p-" + uuid, InstanceUUID: l.instanceID,
		RecordedImageFingerprint: native.ImageFingerprint, RecordedSourceCommit: preview.ImageSourceCommit,
		Selection: selection, Environment: *creation.Environment}
	if creation.EnvironmentState != nil {
		ev.RecordedEnvironmentKey = creation.EnvironmentState.Key
	}
	if accepted, ok, e := l.store.AcceptedRepairImage(ctx, uuid); e != nil {
		return control.Operation{}, e
	} else if ok {
		if accepted.ImageFingerprint != native.ImageFingerprint {
			return control.Operation{}, control.ErrConflict
		}
		if accepted.EnvironmentState != nil {
			ev.RecordedEnvironmentKey = accepted.EnvironmentState.Key
		}
	}
	verify := func(call context.Context) error {
		if err := l.repairCommittedTip(call, control.RepairEvidence{Project: ev.Project, Branch: ev.Branch, AssignedTip: ev.AssignedTip}); err != nil {
			return err
		}
		fingerprint, keyErr := inspectRegisteredSessionKey(l.store.StateDir(), uuid)
		if keyErr != nil || fingerprint != ev.CredentialFingerprint {
			return control.ErrConflict
		}
		observed, inspectErr := l.runtime.Inspect(call, native)
		if inspectErr != nil {
			return inspectErr
		}
		if observed.Exists {
			return control.ErrConflict
		}
		oldPresent, imageErr := l.runtime.ImagePresent(call, ev.RecordedImageFingerprint)
		if imageErr != nil {
			return imageErr
		}
		if oldPresent {
			return control.ErrConflict
		}
		return nil
	}
	op, err := l.store.BeginRepairPreparation(ctx, key, uuid, ev, verify, l.observeSessionCapacity)
	if err != nil {
		return control.Operation{}, err
	}
	if err = l.enqueue(op.ID); err != nil {
		return op, err
	}
	return op, nil
}

func (l *lifecycle) repairPrepareAdvance(op *control.Operation, ev control.RepairPreparation, phase string) error {
	raw, err := json.Marshal(ev)
	if err != nil || len(raw) > 4096 {
		return control.ErrInvalid
	}
	committed := phase != "reserved" && phase != "source-pinned"
	if err = l.store.AdvanceOperation(l.ctx, op.ID, "running", phase, committed, raw, ""); err != nil {
		return err
	}
	op.Phase, op.Evidence, op.Status, op.Committed = phase, raw, "running", committed
	return nil
}

func (l *lifecycle) repairPrepareBlock(op control.Operation, err error) {
	if l.ctx.Err() != nil {
		return
	}
	message := fmt.Sprintf("repair preparation blocked: %v", err)
	if len(message) > 512 {
		message = message[:512]
	}
	_ = l.store.AdvanceOperation(l.ctx, op.ID, "blocked", op.Phase, op.Committed, op.Evidence, message)
}

func repairBuilder(op control.Operation, ev control.RepairPreparation) runtimeincus.Builder {
	return runtimeincus.Builder{RequestUUID: op.ID, ProjectPath: ev.Project,
		CommitOID: ev.AssignedTip, TreeOID: ev.BuilderTreeOID,
		BaseImageFingerprint: ev.Environment.BaseFingerprint, ContractVersion: "1"}
}

func (l *lifecycle) processRepairPrepare(op control.Operation) {
	if op.Status != "running" {
		return
	}
	var ev control.RepairPreparation
	if json.Unmarshal(op.Evidence, &ev) != nil || !ev.Environment.Valid() || ev.InstanceUUID != l.instanceID ||
		ev.IncusProject != l.cfg.IncusProject || ev.InstanceName != "p-"+op.SessionUUID ||
		l.environmentIntent() == nil || ev.Environment != *l.environmentIntent() || ev.Selection != l.selection() {
		l.repairPrepareBlock(op, errors.New("durable preparation identity unavailable"))
		return
	}
	ctx := l.ctx
	s, err := l.store.GetSession(ctx, op.SessionUUID)
	if err != nil || s.Registry != "established" || s.Project != ev.Project || s.Branch != ev.Branch || s.PolicySHA256 != ev.PolicySHA256 {
		l.repairPrepareBlock(op, errors.Join(err, errors.New("preparation assignment changed")))
		return
	}
	if l.policyCondition(ctx, s) == "invalid" {
		l.repairPrepareBlock(op, errors.New("preparation policy unavailable"))
		return
	}
	for step := 0; step < 12; step++ {
		switch op.Phase {
		case "reserved":
			if err = l.repairCommittedTip(ctx, control.RepairEvidence{Project: ev.Project, Branch: ev.Branch, AssignedTip: ev.AssignedTip}); err != nil {
				_ = l.store.FailRepairPreparationPreInit(ctx, op.ID)
				return
			}
			snapshot, e := l.git.backend.CapturePinnedCommit(ctx, ev.Project, ev.AssignedTip)
			if e != nil {
				l.repairPrepareBlock(op, e)
				return
			}
			ev.BuilderTreeOID = snapshot.TreeOID()
			snapshot.Close()
			if err = l.repairPrepareAdvance(&op, ev, "source-pinned"); err != nil {
				l.repairPrepareBlock(op, err)
				return
			}
		case "source-pinned":
			if err = l.repairPrepareAdvance(&op, ev, "builder-init-issued"); err != nil {
				l.repairPrepareBlock(op, err)
				return
			}
			if _, err = l.runtime.CreateBuilder(ctx, repairBuilder(op, ev)); err != nil {
				l.repairPrepareBlock(op, err)
				return
			}
		case "builder-init-issued":
			observed, e := l.runtime.InspectBuilder(ctx, repairBuilder(op, ev))
			if e != nil || !observed.Exists || !observed.Ready || observed.Status != "Stopped" {
				l.repairPrepareBlock(op, errors.Join(e, errors.New("builder init outcome unresolved")))
				return
			}
			if err = l.repairPrepareAdvance(&op, ev, "builder-created"); err != nil {
				l.repairPrepareBlock(op, err)
				return
			}
		case "builder-created":
			if err = l.repairPrepareAdvance(&op, ev, "source-transfer-issued"); err != nil {
				l.repairPrepareBlock(op, err)
				return
			}
			snapshot, e := l.git.backend.CapturePinnedCommit(ctx, ev.Project, ev.AssignedTip)
			if e != nil {
				l.repairPrepareBlock(op, e)
				return
			}
			if snapshot.TreeOID() != ev.BuilderTreeOID {
				snapshot.Close()
				l.repairPrepareBlock(op, errors.New("builder source tree changed"))
				return
			}
			e = l.runtime.TransferBuilderSource(ctx, repairBuilder(op, ev), snapshot)
			snapshot.Close()
			if e != nil {
				l.repairPrepareBlock(op, e)
				return
			}
			if err = l.repairPrepareAdvance(&op, ev, "source-ready"); err != nil {
				l.repairPrepareBlock(op, err)
				return
			}
		case "source-transfer-issued":
			// A partial transfer cannot be adopted. Clean the exact disposable
			// builder under its never-reused operation identity.
			if err = l.repairPrepareAdvance(&op, ev, "builder-cleanup-issued"); err != nil {
				l.repairPrepareBlock(op, err)
				return
			}
			if _, err = l.runtime.DeleteBuilder(ctx, repairBuilder(op, ev)); err != nil {
				l.repairPrepareBlock(op, err)
				return
			}
		case "builder-cleanup-issued":
			observed, e := l.runtime.InspectBuilder(ctx, repairBuilder(op, ev))
			if e != nil || observed.Exists {
				l.repairPrepareBlock(op, errors.Join(e, errors.New("builder cleanup outcome unresolved")))
				return
			}
			if err = l.repairPrepareAdvance(&op, ev, "builder-absent"); err != nil {
				l.repairPrepareBlock(op, err)
				return
			}
		case "source-ready":
			if err = l.repairPrepareAdvance(&op, ev, "builder-start-issued"); err != nil {
				l.repairPrepareBlock(op, err)
				return
			}
			if _, err = l.runtime.StartBuilder(ctx, repairBuilder(op, ev)); err != nil {
				l.repairPrepareBlock(op, err)
				return
			}
		case "builder-start-issued":
			observed, e := l.runtime.InspectBuilder(ctx, repairBuilder(op, ev))
			if e != nil || !observed.Exists || observed.Status != "Running" {
				l.repairPrepareBlock(op, errors.Join(e, errors.New("builder start outcome unresolved")))
				return
			}
			if err = l.repairPrepareAdvance(&op, ev, "builder-running"); err != nil {
				l.repairPrepareBlock(op, err)
				return
			}
		case "builder-running":
			pipeline, e := environmentnix.New(*l.environmentPlugin, l.runtime, repairBuilder(op, ev), ev.Environment.System)
			var selected runtimeincus.BuilderNixSelection
			if e == nil {
				selected, e = pipeline.Resolve(ctx)
			}
			if e != nil {
				if err = l.repairPrepareAdvance(&op, ev, "builder-cleanup-issued"); err != nil {
					l.repairPrepareBlock(op, errors.Join(e, err))
					return
				}
				if _, err = l.runtime.DeleteBuilder(ctx, repairBuilder(op, ev)); err != nil {
					l.repairPrepareBlock(op, errors.Join(e, err))
					return
				}
				continue
			}
			if selected.BaseOnly {
				ev.EnvironmentSelection = "base-no-flake"
				if selected.SourceNarHash != "" {
					ev.EnvironmentSelection, ev.SourceNarHash, ev.FlakePresent = "base-no-default", selected.SourceNarHash, true
				}
			} else {
				ev.EnvironmentSelection, ev.EnvironmentKey, ev.CommittedInputsDigest = "devshell", selected.KeyDigest, selected.CommittedInputsDigest
				ev.DerivationPath, ev.SourceNarHash, ev.FlakePresent = selected.DerivationPath, selected.SourceNarHash, true
			}
			if err = l.repairPrepareAdvance(&op, ev, "selection-ready"); err != nil {
				l.repairPrepareBlock(op, err)
				return
			}
		case "selection-ready":
			if err = l.repairPrepareAdvance(&op, ev, "builder-delete-issued"); err != nil {
				l.repairPrepareBlock(op, err)
				return
			}
			if _, err = l.runtime.DeleteBuilder(ctx, repairBuilder(op, ev)); err != nil {
				l.repairPrepareBlock(op, err)
				return
			}
		case "builder-delete-issued":
			observed, e := l.runtime.InspectBuilder(ctx, repairBuilder(op, ev))
			if e != nil || observed.Exists {
				l.repairPrepareBlock(op, errors.Join(e, errors.New("builder cleanup outcome unresolved")))
				return
			}
			if err = l.repairPrepareAdvance(&op, ev, "builder-absent"); err != nil {
				l.repairPrepareBlock(op, err)
				return
			}
		case "builder-absent":
			if ev.EnvironmentSelection == "" {
				if err = l.store.FailRepairPreparationAfterCleanup(ctx, op.ID, func(call context.Context, got control.RepairPreparation) error {
					observed, e := l.runtime.InspectBuilder(call, repairBuilder(op, got))
					if e != nil || observed.Exists {
						return control.ErrConflict
					}
					return nil
				}); err != nil {
					l.repairPrepareBlock(op, err)
				}
				return
			}
			if err = l.store.CompleteRepairPreparation(ctx, op.ID, func(call context.Context, got control.RepairPreparation) error {
				if got.AssignedTip != ev.AssignedTip || got.EnvironmentKey != ev.EnvironmentKey {
					return control.ErrConflict
				}
				if e := l.repairCommittedTip(call, control.RepairEvidence{Project: got.Project, Branch: got.Branch, AssignedTip: got.AssignedTip}); e != nil {
					return e
				}
				observed, e := l.runtime.InspectBuilder(call, repairBuilder(op, got))
				if e != nil || observed.Exists {
					return control.ErrConflict
				}
				return nil
			}); err != nil {
				l.repairPrepareBlock(op, err)
				return
			}
			l.recordProgress(op, "completed")
			return
		default:
			l.repairPrepareBlock(op, errors.New("preparation phase unavailable"))
			return
		}
	}
	l.repairPrepareBlock(op, errors.New("preparation phase bound exceeded"))
}
