package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"time"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/environmentnix"
	"github.com/lgvo/p.ai/internal/nixenv"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

// ensureEnvironment consumes only the committed OID in durable creation
// evidence. It never observes the current branch or origin during exact retry.
func (l *lifecycle) ensureEnvironment(ctx context.Context, op *control.Operation, ev control.CreationEvidence) (string, error) {
	current := l.environmentIntent()
	if current == nil || ev.Environment == nil || *current != *ev.Environment || ev.CapturedOID == "" {
		return "", errors.New("pinned environment selection unavailable; repair required")
	}
	if op.Phase == "environment-publishing" {
		if ev.EnvironmentState == nil || ev.EnvironmentState.Key == "" || ev.EnvironmentState.Fingerprint != "" || len(ev.EnvironmentState.Properties) == 0 {
			return "", errors.New("durable publication attempt incomplete")
		}
		release, err := l.lockEnvironmentKey(ctx, op.Project, ev.EnvironmentState.Key)
		if err != nil {
			return "", err
		}
		defer release()
		if err := l.rejectOtherEnvironmentPublication(ctx, op.Project, ev.EnvironmentState.Key, op.ID); err != nil {
			return "", err
		}
		claim := environmentClaimForProject(ev, op.Project)
		info, err := l.runtime.ReconcileBuilderPublication(ctx, claim)
		if err != nil {
			return "", err
		}
		if err := l.cleanupEnvironmentBuilder(ctx, op, ev); err != nil {
			return "", fmt.Errorf("published image %s and builder request %s remain unaccepted until exact cleanup: %w", info.Fingerprint, ev.EnvironmentState.BuilderRequest, err)
		}
		return l.acceptEnvironmentImage(ctx, op, &ev, info, false)
	}
	if phaseRank(op.Phase) >= phaseRank("environment-ready") {
		if ev.EnvironmentState == nil {
			return "", errors.New("accepted environment state missing")
		}
		if ev.EnvironmentState.Key == "" && ev.EnvironmentState.Fingerprint == "" && ev.ImageFingerprint == ev.Environment.BaseFingerprint {
			return ev.ImageFingerprint, nil // absent conventional devShell
		}
		if ev.EnvironmentState.Key == "" || ev.EnvironmentState.Fingerprint != ev.ImageFingerprint {
			return "", errors.New("accepted environment image state changed")
		}
		// An already-created instance retains its private root even if its
		// source image was later removed. Its own identity is checked below.
		if phaseRank(op.Phase) >= phaseRank("runtime-created") {
			return ev.ImageFingerprint, nil
		}
		claim := environmentClaimForProject(ev, op.Project)
		_, exists, err := l.runtime.VerifyBuilderImage(ctx, claim)
		if err != nil {
			return "", err
		}
		if exists {
			return ev.ImageFingerprint, nil
		}
		// Incus init can succeed before runtime-created is persisted. Its
		// private root remains valid after the source image is removed, so
		// inspect the exact durable instance identity before cache eviction.
		session, err := l.store.GetSession(ctx, op.SessionUUID)
		if err != nil || session.Project != op.Project {
			return "", errors.Join(err, errors.New("creating session identity unavailable"))
		}
		owned := runtimeincus.Session{
			InstanceUUID: l.instanceID, SessionUUID: session.UUID,
			ProjectPath: session.Project, AssignedBranch: session.Branch,
			InitialOID: ev.CapturedOID, ContractVersion: "1",
			ImageFingerprint: ev.ImageFingerprint,
			EndpointSource:   filepath.Join(l.cfg.EndpointPrefix, session.UUID),
		}
		observed, err := l.runtime.InspectCreated(ctx, owned)
		if err != nil {
			return "", err
		}
		if observed.Exists {
			// The selected runtime module's idempotent create stage will
			// attach a missing endpoint on this exact stopped instance.
			return ev.ImageFingerprint, nil
		}
		if err := l.store.ForgetEnvironmentImage(ctx, op.Project, ev.EnvironmentState.Key, ev.ImageFingerprint); err != nil {
			return "", err
		}
		// A verified external removal is a cache miss. Rebuild from the
		// captured commit, without touching any existing session instance.
	}
	return l.buildEnvironment(ctx, op, &ev)
}

func environmentClaimForProject(ev control.CreationEvidence, project string) runtimeincus.BuilderImageClaim {
	s := ev.EnvironmentState
	return runtimeincus.BuilderImageClaim{
		Fingerprint: s.Fingerprint, ProjectPath: project, Key: s.Key,
		BaseFingerprint: ev.Environment.BaseFingerprint, System: ev.Environment.System,
		MaterialDigest: s.MaterialDigest, CaptureStorePath: s.CaptureStorePath,
		BuilderRequest: s.BuilderRequest, Properties: maps.Clone(s.Properties),
	}
}

func (l *lifecycle) buildEnvironment(ctx context.Context, op *control.Operation, ev *control.CreationEvidence) (image string, resultErr error) {
	snapshot, err := l.git.backend.CapturePinnedCommit(ctx, op.Project, ev.CapturedOID)
	if err != nil {
		return "", err
	}
	defer snapshot.Close()
	if snapshot.CommitOID() != ev.CapturedOID || snapshot.Project() != op.Project {
		return "", errors.New("captured environment source changed")
	}
	r := runtimeincus.Builder{
		RequestUUID: op.ID, ProjectPath: op.Project, CommitOID: ev.CapturedOID,
		TreeOID: snapshot.TreeOID(), BaseImageFingerprint: ev.Environment.BaseFingerprint,
		ContractVersion: "1",
	}
	// Capture the verified tree before any builder init. Capacity admission
	// can then verify a concurrent builder without reentering an origin lock.
	if ev.BuilderTreeOID != r.TreeOID {
		if ev.BuilderTreeOID != "" {
			return "", errors.New("builder source tree changed")
		}
		ev.BuilderTreeOID = r.TreeOID
		if err := l.persistEnvironment(op, *ev, op.Phase); err != nil {
			return "", err
		}
	}
	before, err := l.runtime.InspectBuilder(ctx, r)
	if err != nil {
		return "", err
	}
	if before.Exists {
		if _, err := l.runtime.DeleteBuilder(ctx, r); err != nil {
			return "", err
		}
	}
	if _, err := l.runtime.CreateBuilder(ctx, r); err != nil {
		return "", err
	}
	cleanup := true
	defer func() {
		if !cleanup {
			return
		}
		stopCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if _, err := l.runtime.DeleteBuilder(stopCtx, r); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("verified builder cleanup failed: %w", err))
		}
	}()
	if err := l.runtime.TransferBuilderSource(ctx, r, snapshot); err != nil {
		return "", err
	}
	if _, err := l.runtime.StartBuilder(ctx, r); err != nil {
		return "", err
	}
	pipeline, err := environmentnix.New(*l.environmentPlugin, l.runtime, r, ev.Environment.System)
	if err != nil {
		return "", err
	}
	selection, err := pipeline.Resolve(ctx)
	if err != nil {
		return "", err
	}
	if selection.BaseOnly {
		if _, err := l.runtime.DeleteBuilder(ctx, r); err != nil {
			return "", err
		}
		cleanup = false
		ev.EnvironmentState = &control.EnvironmentState{BuilderRequest: r.RequestUUID, FlakePresent: selection.SourceNarHash != ""}
		ev.ImageFingerprint = ev.Environment.BaseFingerprint
		if err := l.persistEnvironment(op, *ev, "environment-ready"); err != nil {
			return "", err
		}
		return ev.ImageFingerprint, nil
	}
	release, err := l.lockEnvironmentKey(ctx, op.Project, selection.KeyDigest)
	if err != nil {
		return "", err
	}
	defer release()
	if err := l.rejectOtherEnvironmentPublication(ctx, op.Project, selection.KeyDigest, op.ID); err != nil {
		return "", err
	}
	cache, found, err := l.store.GetEnvironmentImage(ctx, op.Project, selection.KeyDigest)
	if err != nil {
		return "", err
	}
	if found {
		claim := imageClaimFromCache(cache)
		_, exists, err := l.runtime.VerifyBuilderImage(ctx, claim)
		if err != nil {
			return "", err
		}
		if exists {
			if _, err := l.runtime.DeleteBuilder(ctx, r); err != nil {
				return "", err
			}
			cleanup = false
			ev.EnvironmentState = environmentStateFromCache(cache, true)
			ev.ImageFingerprint = cache.Fingerprint
			if err := l.persistEnvironment(op, *ev, "environment-ready"); err != nil {
				return "", err
			}
			return cache.Fingerprint, nil
		}
		if err := l.store.ForgetEnvironmentImage(ctx, cache.Project, cache.Key, cache.Fingerprint); err != nil {
			return "", err
		}
	}
	result, err := pipeline.Realize(ctx)
	if err != nil {
		return "", err
	}
	return l.publishAcceptedEnvironment(ctx, op, ev, r, result, pipeline, func() { cleanup = false })
}

func (l *lifecycle) lockEnvironmentKey(ctx context.Context, project, key string) (func(), error) {
	l.mu.Lock()
	if l.cacheKeyLocks == nil {
		l.cacheKeyLocks = map[string]chan struct{}{}
	}
	id := project + "\x00" + key
	slot := l.cacheKeyLocks[id]
	if slot == nil {
		slot = make(chan struct{}, 1)
		slot <- struct{}{}
		l.cacheKeyLocks[id] = slot
	}
	l.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-slot:
		return func() { slot <- struct{}{} }, nil
	}
}

func (l *lifecycle) rejectOtherEnvironmentPublication(ctx context.Context, project, key, owner string) error {
	pending, err := l.store.PendingEnvironmentPublication(ctx, project, key, owner)
	if err != nil {
		return err
	}
	if pending {
		return errors.New("same environment key has an unresolved prior publication; reconcile its exact operation before retry")
	}
	return nil
}

type gatedPublisher interface {
	PublishPrivateImageWithGate(context.Context, func(runtimeincus.BuilderImageClaim) error) (nixenv.Handle, runtimeincus.BuilderImageInfo, error)
}

func (l *lifecycle) publishAcceptedEnvironment(ctx context.Context, op *control.Operation, ev *control.CreationEvidence, r runtimeincus.Builder, result runtimeincus.BuilderNixResult, pipeline gatedPublisher, onAttempt func()) (string, error) {
	if pipeline == nil || onAttempt == nil {
		return "", errors.New("durable environment publisher unavailable")
	}
	_, info, err := runDurableImagePublish(ctx, pipeline, func(actual runtimeincus.BuilderImageClaim) error {
		if actual.ProjectPath != op.Project || actual.Key != result.Selection.KeyDigest ||
			actual.BuilderRequest != r.RequestUUID || actual.BaseFingerprint != ev.Environment.BaseFingerprint ||
			actual.System != ev.Environment.System || actual.MaterialDigest != result.MaterialDigest ||
			actual.CaptureStorePath != result.CaptureStorePath {
			return errors.New("native publish claim changed from accepted realization")
		}
		return nil
	}, func(actual runtimeincus.BuilderImageClaim) error {
		ev.EnvironmentState = &control.EnvironmentState{
			Key: actual.Key, MaterialDigest: actual.MaterialDigest,
			CaptureStorePath: actual.CaptureStorePath, BuilderRequest: actual.BuilderRequest,
			Properties: maps.Clone(actual.Properties),
		}
		// The native gate runs after smoke, scrub, GC and all pre-publish
		// checks, immediately before the Incus publish command.
		return l.persistEnvironment(op, *ev, "environment-publishing")
	}, onAttempt)
	if err != nil {
		return "", err
	}
	if info.BuilderCleanupPending {
		if err := l.cleanupEnvironmentBuilder(ctx, op, *ev); err != nil {
			return "", fmt.Errorf("published image %s and builder request %s remain unaccepted until exact cleanup: %w", info.Fingerprint, ev.EnvironmentState.BuilderRequest, err)
		}
	}
	return l.acceptEnvironmentImage(ctx, op, ev, info, false)
}

// runDurableImagePublish separates deterministic image preparation failures
// from an Incus publish attempt. Only the gate records the claim; a failure
// after it fires is never safe to republish without reconciliation.
func runDurableImagePublish(ctx context.Context, pipeline gatedPublisher, validate, record func(runtimeincus.BuilderImageClaim) error, onAttempt func()) (runtimeincus.BuilderImageClaim, runtimeincus.BuilderImageInfo, error) {
	var claim runtimeincus.BuilderImageClaim
	var zero runtimeincus.BuilderImageInfo
	marked := false
	_, info, err := pipeline.PublishPrivateImageWithGate(ctx, func(actual runtimeincus.BuilderImageClaim) error {
		if err := validate(actual); err != nil {
			return err
		}
		if err := record(actual); err != nil {
			return err
		}
		claim = actual
		marked = true
		onAttempt()
		return nil
	})
	if err != nil {
		return claim, zero, err
	}
	if !marked || !maps.Equal(info.Properties, claim.Properties) || info.Fingerprint == "" {
		return claim, zero, errors.New("published image differs from durable claim")
	}
	return claim, info, nil
}

func (l *lifecycle) cleanupEnvironmentBuilder(ctx context.Context, op *control.Operation, ev control.CreationEvidence) error {
	if ev.Environment == nil || ev.EnvironmentState == nil || ev.EnvironmentState.BuilderRequest != op.ID {
		return errors.New("exact builder cleanup identity unavailable")
	}
	snapshot, err := l.git.backend.CapturePinnedCommit(ctx, op.Project, ev.CapturedOID)
	if err != nil {
		return err
	}
	defer snapshot.Close()
	if snapshot.CommitOID() != ev.CapturedOID || snapshot.Project() != op.Project {
		return errors.New("captured cleanup source changed")
	}
	r := runtimeincus.Builder{
		RequestUUID: op.ID, ProjectPath: op.Project, CommitOID: ev.CapturedOID,
		TreeOID: snapshot.TreeOID(), BaseImageFingerprint: ev.Environment.BaseFingerprint,
		ContractVersion: "1",
	}
	_, err = l.runtime.DeleteBuilder(ctx, r)
	return err
}

func imageClaimFromCache(e control.EnvironmentImage) runtimeincus.BuilderImageClaim {
	return runtimeincus.BuilderImageClaim{
		Fingerprint: e.Fingerprint, ProjectPath: e.Project, Key: e.Key,
		BaseFingerprint: e.BaseFingerprint, System: e.System,
		MaterialDigest: e.MaterialDigest, CaptureStorePath: e.CaptureStorePath,
		BuilderRequest: e.BuilderRequest, Properties: maps.Clone(e.Properties),
	}
}

func environmentStateFromCache(e control.EnvironmentImage, hit bool) *control.EnvironmentState {
	return &control.EnvironmentState{
		Key: e.Key, MaterialDigest: e.MaterialDigest, CaptureStorePath: e.CaptureStorePath,
		BuilderRequest: e.BuilderRequest, Properties: maps.Clone(e.Properties),
		Fingerprint: e.Fingerprint, CacheHit: hit,
	}
}

func (l *lifecycle) acceptEnvironmentImage(ctx context.Context, op *control.Operation, ev *control.CreationEvidence, info runtimeincus.BuilderImageInfo, hit bool) (string, error) {
	s := ev.EnvironmentState
	if s == nil || info.Fingerprint == "" || !maps.Equal(info.Properties, s.Properties) {
		return "", errors.New("environment image acceptance changed")
	}
	entry := control.EnvironmentImage{
		Project: op.Project, Key: s.Key, Fingerprint: info.Fingerprint,
		BaseFingerprint: ev.Environment.BaseFingerprint, System: ev.Environment.System,
		MaterialDigest: s.MaterialDigest, CaptureStorePath: s.CaptureStorePath,
		BuilderRequest: s.BuilderRequest, Properties: maps.Clone(s.Properties), LogicalSize: info.Size,
	}
	if err := l.store.PutEnvironmentImage(ctx, entry); err != nil {
		return "", err
	}
	s.Fingerprint, s.CacheHit = info.Fingerprint, hit
	ev.ImageFingerprint = info.Fingerprint
	if err := l.persistEnvironment(op, *ev, "environment-ready"); err != nil {
		return "", err
	}
	return info.Fingerprint, nil
}

func (l *lifecycle) persistEnvironment(op *control.Operation, ev control.CreationEvidence, phase string) error {
	data, err := json.Marshal(ev)
	if err != nil || len(data) > 16384 {
		return errors.New("environment creation evidence exceeds durable bound")
	}
	if err := l.store.AdvanceOperation(l.ctx, op.ID, "running", phase, true, data, ""); err != nil {
		return err
	}
	op.Phase, op.Evidence, op.Committed, op.Status = phase, data, true, "running"
	l.recordProgress(*op, "running")
	return nil
}
