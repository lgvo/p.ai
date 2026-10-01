package daemon

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/environmentnix"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

func repairImageBuilder(op control.Operation, ev control.RepairEvidence) runtimeincus.Builder {
	return runtimeincus.Builder{RequestUUID: op.ID, ProjectPath: ev.Project, CommitOID: ev.EnvironmentSourceCommit,
		TreeOID: ev.BuilderTreeOID, BaseImageFingerprint: ev.Environment.BaseFingerprint, ContractVersion: "1"}
}

func repairImageClaim(ev control.RepairEvidence) runtimeincus.BuilderImageClaim {
	s := ev.EnvironmentState
	return runtimeincus.BuilderImageClaim{Fingerprint: s.Fingerprint, ProjectPath: ev.Project, Key: s.Key,
		BaseFingerprint: ev.Environment.BaseFingerprint, System: ev.Environment.System,
		MaterialDigest: s.MaterialDigest, CaptureStorePath: s.CaptureStorePath,
		BuilderRequest: s.BuilderRequest, Properties: maps.Clone(s.Properties)}
}

func (l *lifecycle) repairImageAdvanceToReady(ctx context.Context, op *control.Operation, ev *control.RepairEvidence) error {
	if ev.PreparationOperationID == "" || ev.Environment == nil || ev.EnvironmentSourceCommit != ev.AssignedTip ||
		l.environmentIntent() == nil || *ev.Environment != *l.environmentIntent() {
		return control.ErrConflict
	}
	prepared, err := l.store.CompletedRepairPreparation(ctx, ev.PreparationOperationID, op.SessionUUID)
	if err != nil || prepared.AssignedTip != ev.AssignedTip || prepared.EnvironmentKey != ev.EnvironmentKey ||
		prepared.EnvironmentSelection != ev.EnvironmentSelection || prepared.BuilderTreeOID != ev.BuilderTreeOID ||
		prepared.Environment != *ev.Environment || prepared.RecordedImageFingerprint != ev.RecordedImageFingerprint {
		return control.ErrConflict
	}
	// Creation uses the same key lock before checking the cache and publishing.
	// Hold it over repair's entire native publication and acceptance sequence;
	// a durable repair phase also excludes publishers after a daemon restart.
	if ev.EnvironmentKey != "" {
		release, err := l.lockEnvironmentKey(ctx, ev.Project, ev.EnvironmentKey)
		if err != nil {
			return err
		}
		defer release()
	}
	for step := 0; step < 13; step++ {
		r := repairImageBuilder(*op, *ev)
		switch op.Phase {
		case "guarded":
			oldPresent, e := l.runtime.ImagePresent(ctx, ev.RecordedImageFingerprint)
			if e != nil {
				return e
			}
			if oldPresent {
				return errors.New("recorded image reappeared after repair preview")
			}
			if ev.ImageFingerprint != "" {
				if ev.EnvironmentKey == "" {
					present, e := l.runtime.ImagePresent(ctx, ev.ImageFingerprint)
					if e != nil || !present || ev.ImageFingerprint != ev.Environment.BaseFingerprint {
						return errors.Join(e, errors.New("resolved base image unavailable"))
					}
				} else {
					if ev.EnvironmentState == nil {
						return control.ErrConflict
					}
					_, present, e := l.runtime.VerifyBuilderImage(ctx, repairImageClaim(*ev))
					if e != nil || !present {
						return errors.Join(e, errors.New("resolved cached image unavailable"))
					}
				}
				return l.repairAdvance(op, *ev, "image-ready")
			}
			if ev.EnvironmentKey == "" {
				return control.ErrConflict
			}
			entry, found, e := l.store.GetEnvironmentImage(ctx, ev.Project, ev.EnvironmentKey)
			if e != nil {
				return e
			}
			if found {
				_, present, e := l.runtime.VerifyBuilderImage(ctx, imageClaimFromCache(entry))
				if e != nil {
					return e
				}
				if present {
					return errors.New("resolved environment cache changed after preview")
				}
				if e = l.store.ForgetEnvironmentImage(ctx, entry.Project, entry.Key, entry.Fingerprint); e != nil {
					return e
				}
			}
			if e = l.rejectOtherEnvironmentPublication(ctx, ev.Project, ev.EnvironmentKey, op.ID); e != nil {
				return e
			}
			snapshot, e := l.git.backend.CapturePinnedCommit(ctx, ev.Project, ev.AssignedTip)
			if e != nil {
				return e
			}
			match := snapshot.TreeOID() == ev.BuilderTreeOID
			snapshot.Close()
			if !match {
				return errors.New("resolved environment source tree changed")
			}
			if e = l.repairAdvance(op, *ev, "image-builder-init-issued"); e != nil {
				return e
			}
			if _, e = l.runtime.CreateBuilder(ctx, r); e != nil {
				return e
			}
		case "image-builder-init-issued":
			observed, e := l.runtime.InspectBuilder(ctx, r)
			if e != nil || !observed.Exists || !observed.Ready || observed.Status != "Stopped" {
				return errors.Join(e, errors.New("repair image builder init outcome unresolved"))
			}
			if e = l.repairAdvance(op, *ev, "image-builder-created"); e != nil {
				return e
			}
		case "image-builder-created":
			if e := l.repairAdvance(op, *ev, "image-source-transfer-issued"); e != nil {
				return e
			}
			snapshot, e := l.git.backend.CapturePinnedCommit(ctx, ev.Project, ev.AssignedTip)
			if e != nil {
				return e
			}
			if snapshot.TreeOID() != ev.BuilderTreeOID {
				snapshot.Close()
				return errors.New("repair image builder source changed")
			}
			e = l.runtime.TransferBuilderSource(ctx, r, snapshot)
			snapshot.Close()
			if e != nil {
				return e
			}
			if e = l.repairAdvance(op, *ev, "image-source-ready"); e != nil {
				return e
			}
		case "image-source-transfer-issued":
			return errors.New("repair image source transfer outcome requires exact cleanup")
		case "image-source-ready":
			if e := l.repairAdvance(op, *ev, "image-builder-start-issued"); e != nil {
				return e
			}
			if _, e := l.runtime.StartBuilder(ctx, r); e != nil {
				return e
			}
		case "image-builder-start-issued":
			observed, e := l.runtime.InspectBuilder(ctx, r)
			if e != nil || !observed.Exists || observed.Status != "Running" {
				return errors.Join(e, errors.New("repair image builder start outcome unresolved"))
			}
			if e = l.repairAdvance(op, *ev, "image-builder-running"); e != nil {
				return e
			}
		case "image-builder-running":
			pipeline, e := environmentnix.New(*l.environmentPlugin, l.runtime, r, ev.Environment.System)
			if e != nil {
				return e
			}
			selected, e := pipeline.Resolve(ctx)
			if e != nil || selected.BaseOnly || selected.KeyDigest != prepared.EnvironmentKey ||
				selected.DerivationPath != prepared.DerivationPath || selected.CommittedInputsDigest != prepared.CommittedInputsDigest ||
				selected.SourceNarHash != prepared.SourceNarHash {
				return errors.Join(e, errors.New("prepared environment identity changed"))
			}
			result, e := pipeline.Realize(ctx)
			if e != nil {
				return e
			}
			_, info, e := runDurableImagePublish(ctx, pipeline, func(actual runtimeincus.BuilderImageClaim) error {
				if actual.ProjectPath != ev.Project || actual.Key != ev.EnvironmentKey || actual.BuilderRequest != op.ID ||
					actual.BaseFingerprint != ev.Environment.BaseFingerprint || actual.System != ev.Environment.System ||
					actual.MaterialDigest != result.MaterialDigest || actual.CaptureStorePath != result.CaptureStorePath {
					return control.ErrConflict
				}
				return nil
			}, func(actual runtimeincus.BuilderImageClaim) error {
				ev.EnvironmentState = &control.EnvironmentState{Key: actual.Key, MaterialDigest: actual.MaterialDigest,
					CaptureStorePath: actual.CaptureStorePath, BuilderRequest: actual.BuilderRequest, Properties: maps.Clone(actual.Properties)}
				return l.repairAdvance(op, *ev, "environment-publishing")
			}, func() {})
			if e != nil {
				return e
			}
			if ev.EnvironmentState == nil || !maps.Equal(info.Properties, ev.EnvironmentState.Properties) {
				return errors.New("repair published image claim changed")
			}
			ev.ImageFingerprint, ev.EnvironmentState.Fingerprint = info.Fingerprint, info.Fingerprint
			if e = l.repairAdvance(op, *ev, "image-builder-delete-issued"); e != nil {
				return e
			}
			if info.BuilderCleanupPending {
				if _, e = l.runtime.DeleteBuilder(ctx, r); e != nil {
					return e
				}
			}
		case "environment-publishing":
			if ev.EnvironmentState == nil || ev.EnvironmentState.Key != ev.EnvironmentKey || ev.EnvironmentState.BuilderRequest != op.ID {
				return control.ErrConflict
			}
			info, e := l.runtime.ReconcileBuilderPublication(ctx, repairImageClaim(*ev))
			if e != nil {
				return e
			}
			ev.ImageFingerprint, ev.EnvironmentState.Fingerprint = info.Fingerprint, info.Fingerprint
			if e = l.repairAdvance(op, *ev, "image-builder-delete-issued"); e != nil {
				return e
			}
		case "image-builder-delete-issued":
			observed, e := l.runtime.InspectBuilder(ctx, r)
			if e != nil {
				return e
			}
			if observed.Exists {
				if _, e = l.runtime.DeleteBuilder(ctx, r); e != nil {
					return e
				}
				observed, e = l.runtime.InspectBuilder(ctx, r)
				if e != nil || observed.Exists {
					return errors.Join(e, errors.New("repair image builder cleanup unresolved"))
				}
			}
			if e = l.repairAdvance(op, *ev, "image-builder-absent"); e != nil {
				return e
			}
		case "image-builder-absent":
			if ev.EnvironmentState == nil || ev.EnvironmentState.Fingerprint != ev.ImageFingerprint || ev.EnvironmentState.Key != ev.EnvironmentKey {
				return control.ErrConflict
			}
			info, present, e := l.runtime.VerifyBuilderImage(ctx, repairImageClaim(*ev))
			if e != nil || !present || info.Fingerprint != ev.ImageFingerprint {
				return errors.Join(e, errors.New("published repair image unavailable"))
			}
			entry := control.EnvironmentImage{Project: ev.Project, Key: ev.EnvironmentKey, Fingerprint: ev.ImageFingerprint,
				BaseFingerprint: ev.Environment.BaseFingerprint, System: ev.Environment.System,
				MaterialDigest: ev.EnvironmentState.MaterialDigest, CaptureStorePath: ev.EnvironmentState.CaptureStorePath,
				BuilderRequest: ev.EnvironmentState.BuilderRequest, Properties: maps.Clone(ev.EnvironmentState.Properties), LogicalSize: info.Size}
			if e = l.store.PutEnvironmentImage(ctx, entry); e != nil {
				return fmt.Errorf("repair image cache acceptance unavailable: %w", e)
			}
			return l.repairAdvance(op, *ev, "image-ready")
		case "image-ready":
			return nil
		default:
			return control.ErrConflict
		}
	}
	return errors.New("repair image phase bound exceeded")
}
