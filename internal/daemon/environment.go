package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/nixenv"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

type environmentRecoveryRuntime interface {
	ReconcileBuilderPublication(context.Context, runtimeincus.BuilderImageClaim) (runtimeincus.BuilderImageInfo, error)
	VerifyBuilderImage(context.Context, runtimeincus.BuilderImageClaim) (runtimeincus.BuilderImageInfo, bool, error)
	InspectCreated(context.Context, runtimeincus.Session) (runtimeincus.Observation, error)
}

// ensureEnvironment consumes only the committed OID in durable creation
// evidence. It never observes the current branch or origin during exact retry.
func (l *lifecycle) ensureEnvironment(ctx context.Context, op *api.Operation, ev control.CreationEvidence) (string, error) {
	return l.ensureEnvironmentWithRuntime(ctx, op, ev, l.runtime, l.buildEnvironment)
}

func (l *lifecycle) ensureEnvironmentWithRuntime(ctx context.Context, op *api.Operation, ev control.CreationEvidence,
	runtime environmentRecoveryRuntime, rebuild func(context.Context, *api.Operation, *control.CreationEvidence) (string, error)) (string, error) {
	image, err := l.readyEnvironment(ctx, op, ev, runtime, rebuild)
	if err != nil {
		return "", err
	}
	// The builder is gone once the environment is ready, so an address slot
	// held only for it goes back.
	return image, l.store.ReleaseBuilderPublicAddress(ctx, op.SessionUUID)
}

func (l *lifecycle) readyEnvironment(ctx context.Context, op *api.Operation, ev control.CreationEvidence,
	runtime environmentRecoveryRuntime, rebuild func(context.Context, *api.Operation, *control.CreationEvidence) (string, error)) (string, error) {
	current := l.environmentIntent()
	if current == nil || ev.Environment == nil || *current != *ev.Environment || ev.CapturedOID == "" {
		return "", refusalBecause(api.KindConflict, api.ReasonImageBuilderChanged, api.TargetSession(op.SessionUUID), control.ErrConflict, errors.New("pinned environment selection unavailable"))
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
		info, err := runtime.ReconcileBuilderPublication(ctx, claim)
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
		_, exists, err := runtime.VerifyBuilderImage(ctx, claim)
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
		if err != nil || session.Project != op.Project || session.Registry != "creating" || session.PolicySHA256 != ev.PolicySHA256 {
			return "", errors.Join(err, errors.New("creating session identity unavailable"))
		}
		owned, err := l.runtimeSession(ctx, session)
		if err != nil {
			return "", err
		}
		// Creation recovery cannot substitute another image or source.
		// The stored session policy and reserved address supply every device.
		if owned.ImageFingerprint != ev.ImageFingerprint || owned.InitialOID != ev.CapturedOID {
			return "", errors.New("creating runtime image or source differs from captured request")
		}
		observed, err := runtime.InspectCreated(ctx, owned)
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
	return rebuild(ctx, op, &ev)
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

func (l *lifecycle) buildEnvironment(ctx context.Context, op *api.Operation, ev *control.CreationEvidence) (image string, resultErr error) {
	snapshot, err := l.git.backend.CapturePinnedCommit(ctx, op.Project, ev.CapturedOID)
	if err != nil {
		return "", err
	}
	defer snapshot.Close()
	if snapshot.CommitOID() != ev.CapturedOID || snapshot.Project() != op.Project {
		return "", errors.New("captured environment source changed")
	}
	address, err := l.reserveBuilderPublicIPv4(ctx, op.SessionUUID)
	if err != nil {
		return "", err
	}
	r := runtimeincus.Builder{
		RequestUUID: op.ID, ProjectPath: op.Project, CommitOID: ev.CapturedOID,
		TreeOID: snapshot.TreeOID(), BaseImageFingerprint: ev.Environment.BaseFingerprint,
		ContractVersion: "1", PublicIPv4: address,
	}
	if ev.EnvironmentBuilder == nil && ev.BuilderTreeOID == "" {
		// An empty durable tree precedes every builder dispatch in older workers.
		ev.EnvironmentBuilder = &control.CreationBuilderState{State: "not-attempted"}
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
	if ev.EnvironmentBuilder == nil {
		return "", errors.New("historical builder dispatch/cleanup evidence unavailable")
	}
	before, err := l.runtime.InspectBuilder(ctx, r)
	if err != nil {
		return "", err
	}
	if before.Exists {
		if ev.EnvironmentBuilder.State != "attempted" && ev.EnvironmentBuilder.State != "owned" {
			return "", errors.New("builder reappeared outside durable attempt")
		}
		ev.EnvironmentBuilder.State = "owned"
		if err = l.persistEnvironment(op, *ev, op.Phase); err != nil {
			return "", err
		}
		if _, err = l.runtime.DeleteBuilder(ctx, r); err != nil {
			return "", err
		}
	}
	if ev.EnvironmentBuilder.State == "owned" {
		if err = l.runtime.ConfirmFailedCreateEffectsAbsent(ctx, runtimeincus.Session{InstanceUUID: l.instanceID, SessionUUID: op.SessionUUID, ProjectPath: op.Project, ContractVersion: "1", ImageFingerprint: ev.ImageFingerprint}, op.ID); err != nil {
			return "", err
		}
		ev.EnvironmentBuilder.State = "absent"
		if err = l.persistEnvironment(op, *ev, op.Phase); err != nil {
			return "", err
		}
	}
	if ev.EnvironmentBuilder.State != "not-attempted" && ev.EnvironmentBuilder.State != "absent" {
		return "", control.OutcomeUnknown(errors.New("builder init outcome unknown; absent inventory cannot authorize another init"))
	}
	if _, err = l.runtime.CreateBuilderWithGate(ctx, r, func() error {
		next := *ev
		next.EnvironmentBuilder = &control.CreationBuilderState{Cycle: ev.EnvironmentBuilder.Cycle + 1, State: "attempted"}
		if e := l.persistEnvironment(op, next, op.Phase); e != nil {
			return e
		}
		*ev = next
		return nil
	}); err != nil {
		return "", err
	}
	if ev.EnvironmentBuilder.State != "attempted" {
		return "", errors.New("builder appeared outside recorded native dispatch")
	}
	ev.EnvironmentBuilder.State = "owned"
	if err = l.persistEnvironment(op, *ev, op.Phase); err != nil {
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
			return
		}
		if err := l.runtime.ConfirmFailedCreateEffectsAbsent(stopCtx, runtimeincus.Session{InstanceUUID: l.instanceID, SessionUUID: op.SessionUUID, ProjectPath: op.Project, ContractVersion: "1", ImageFingerprint: ev.ImageFingerprint}, op.ID); err != nil {
			resultErr = errors.Join(resultErr, err)
			return
		}
		ev.EnvironmentBuilder.State = "absent"
		if err := l.persistEnvironment(op, *ev, op.Phase); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}()
	if err := l.runtime.TransferBuilderSource(ctx, r, snapshot); err != nil {
		return "", err
	}
	if _, err := l.runtime.StartBuilder(ctx, r); err != nil {
		return "", err
	}
	selection, err := runNixStage(ctx, func(stage context.Context) (runtimeincus.BuilderNixSelection, error) {
		return l.runtime.ResolveBuilderNix(stage, r, ev.Environment.System)
	})
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
	// Realize exactly the accepted selection, and accept only material for it.
	result, err := runNixStage(ctx, func(stage context.Context) (runtimeincus.BuilderNixResult, error) {
		return l.runtime.RealizeBuilderNix(stage, r, selection)
	})
	if err != nil {
		return "", err
	}
	if result.MaterialDigest == "" || result.Selection.KeyDigest != selection.KeyDigest {
		return "", errors.New("environment realization postcondition failed")
	}
	return l.publishAcceptedEnvironment(ctx, op, ev, r, result, func() { cleanup = false })
}

// The builder's two Nix stages, resolve and realize, run at most two at a time
// across the daemon, and each ends after seven minutes, its wait for a slot
// included.
var nixStageSlots = make(chan struct{}, 2)

const nixStageDeadline = 7 * time.Minute

func runNixStage[T any](parent context.Context, stage func(context.Context) (T, error)) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(parent, nixStageDeadline)
	defer cancel()
	select {
	case nixStageSlots <- struct{}{}:
		defer func() { <-nixStageSlots }()
	case <-ctx.Done():
		return zero, errors.New("environment Nix stage timed out waiting for a slot")
	}
	return stage(ctx)
}

func (l *lifecycle) lockEnvironmentKey(ctx context.Context, project, key string) (func(), error) {
	var slot chan struct{}
	l.withLock(func() {
		if l.cacheKeyLocks == nil {
			l.cacheKeyLocks = map[string]chan struct{}{}
		}
		id := project + "\x00" + key
		slot = l.cacheKeyLocks[id]
		if slot == nil {
			slot = make(chan struct{}, 1)
			slot <- struct{}{}
			l.cacheKeyLocks[id] = slot
		}
	})
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
		return refusalBecause(api.KindConflict, api.ReasonOperationUnfinished, "", control.ErrConflict, errors.New("same environment key has an unresolved prior publication"))
	}
	return nil
}

type gatedPublisher interface {
	PublishBuilderImageWithGate(context.Context, runtimeincus.Builder, runtimeincus.BuilderNixResult, func(runtimeincus.BuilderImageClaim) error) (nixenv.Handle, runtimeincus.BuilderImageInfo, error)
}

func (l *lifecycle) publishAcceptedEnvironment(ctx context.Context, op *api.Operation, ev *control.CreationEvidence, r runtimeincus.Builder, result runtimeincus.BuilderNixResult, onAttempt func()) (string, error) {
	_, info, err := runDurableImagePublish(ctx, l.runtime, r, result, func(actual runtimeincus.BuilderImageClaim) error {
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

// runDurableImagePublish publishes exactly the realized result. It separates
// deterministic image preparation failures from an Incus publish attempt. Only
// the gate records the claim; a failure after it fires is never safe to
// republish without reconciliation.
func runDurableImagePublish(ctx context.Context, publisher gatedPublisher, r runtimeincus.Builder, result runtimeincus.BuilderNixResult, validate, record func(runtimeincus.BuilderImageClaim) error, onAttempt func()) (runtimeincus.BuilderImageClaim, runtimeincus.BuilderImageInfo, error) {
	var claim runtimeincus.BuilderImageClaim
	var zero runtimeincus.BuilderImageInfo
	marked := false
	handle, info, err := publisher.PublishBuilderImageWithGate(ctx, r, result, func(actual runtimeincus.BuilderImageClaim) error {
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
	if !marked || !maps.Equal(info.Properties, claim.Properties) || info.Fingerprint == "" || handle.Locator != info.Fingerprint {
		return claim, zero, errors.New("published image differs from durable claim")
	}
	return claim, info, nil
}

func (l *lifecycle) cleanupEnvironmentBuilder(ctx context.Context, op *api.Operation, ev control.CreationEvidence) error {
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
	address, err := l.builderPublicIPv4(ctx, op.SessionUUID)
	if err != nil {
		return err
	}
	r := runtimeincus.Builder{
		RequestUUID: op.ID, ProjectPath: op.Project, CommitOID: ev.CapturedOID,
		TreeOID: snapshot.TreeOID(), BaseImageFingerprint: ev.Environment.BaseFingerprint,
		ContractVersion: "1", PublicIPv4: address,
	}
	_, err = l.runtime.DeleteBuilder(ctx, r)
	return err
}

// Builders reach the public internet whenever the host provides public
// egress. A builder uses its session's address slot, reserved for it when the
// session's own policy holds none. An empty address means no NIC.
func (l *lifecycle) reserveBuilderPublicIPv4(ctx context.Context, sessionID string) (string, error) {
	if l.cfg.PublicEgress == nil {
		return "", nil
	}
	slot, err := l.store.ReserveBuilderPublicAddress(ctx, sessionID)
	if err != nil {
		return "", fmt.Errorf("no public address for the environment builder: %w", err)
	}
	return l.cfg.PublicEgress.PublicEgressGuestIPv4(slot)
}

// builderPublicIPv4 is the address a session's builder holds, if any.
func (l *lifecycle) builderPublicIPv4(ctx context.Context, sessionID string) (string, error) {
	if l.cfg.PublicEgress == nil {
		return "", nil
	}
	slot, err := l.store.PublicAddressSlot(ctx, sessionID)
	if errors.Is(err, control.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return l.cfg.PublicEgress.PublicEgressGuestIPv4(slot)
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

func (l *lifecycle) acceptEnvironmentImage(ctx context.Context, op *api.Operation, ev *control.CreationEvidence, info runtimeincus.BuilderImageInfo, hit bool) (string, error) {
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

func (l *lifecycle) persistEnvironment(op *api.Operation, ev control.CreationEvidence, phase string) error {
	data, err := json.Marshal(ev)
	if err != nil || len(data) > 16384 {
		return errors.New("environment creation evidence exceeds durable bound")
	}
	if err := l.store.AdvanceOperation(l.ctx, op.ID, "running", phase, true, data, "", ""); err != nil {
		return err
	}
	op.Phase, op.Evidence, op.Committed, op.Status = phase, data, true, "running"
	l.recordProgress(*op, "running")
	return nil
}
