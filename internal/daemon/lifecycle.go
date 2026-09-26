package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
	"github.com/lgvo/p.ai/internal/plugin"
	"github.com/lgvo/p.ai/internal/runtimeincus"
	"github.com/lgvo/p.ai/internal/runtimekit"
	"golang.org/x/crypto/ssh"
)

type lifecycle struct {
	store                   *control.Store
	cfg                     control.RuntimeConfig
	git                     *gitCapability
	runtime                 *runtimeincus.Backend
	runtimePlugin           plugin.Active
	environmentPlugin       *plugin.Active
	hostPlan                plugin.AssetPlan
	sourcePlan              plugin.AssetPlan
	agentPlan               *plugin.AssetPlan
	endpoints               *endpointManager
	instanceID              string
	ctx                     context.Context
	mu                      sync.Mutex
	working                 map[string]bool
	queueSlots              chan struct{}
	sessionLocks            map[string]chan struct{}
	confinementCheck        func(context.Context) error
	startActive             map[string]bool
	attachments             map[string]*attachment
	onAttachment            func(context.Context, string, int, bool)
	events                  *eventDelivery
	eventMu                 sync.Mutex
	observed                map[string]observedFacts
	eventContexts           map[string]eventContext
	progress                map[string]string
	changedPolicies         map[string]bool
	collectionPreviews      map[string]collectionPreviewState
	cacheKeyLocks           map[string]chan struct{}
	removalPreviews         map[string]removalPreviewState
	repairPreviews          map[string]repairPreviewState
	refRepairPreviews       map[string]refRepairPreviewState
	principalRepairPreviews map[string]principalRepairPreviewState
	recordRepairPreviews    map[string]recordRepairPreviewState
	retainedDeletePreviews  map[string]retainedDeletePreviewState
	createReplacePreviews   map[string]createReplacePreviewState
}

func newLifecycle(ctx context.Context, cfg control.RuntimeConfig, store *control.Store, git *gitCapability) (*lifecycle, error) {
	if err := privateTrustedFile(cfg.ActivationPath); err != nil {
		return nil, fmt.Errorf("runtime activation: %w", err)
	}
	active, err := plugin.LoadActivation(cfg.ActivationPath)
	if err != nil {
		return nil, err
	}
	var runtimeSelection, hostSelection *plugin.Active
	for i := range active {
		v := &active[i]
		switch v.Package.Manifest.ID {
		case cfg.RuntimePluginID:
			runtimeSelection = v
		case cfg.HostPluginID:
			hostSelection = v
		}
	}
	if runtimeSelection == nil || runtimeSelection.Package.Manifest.Capability != "runtime" || runtimeSelection.Package.Manifest.Runtime.Kind != "wasi-command" || len(runtimeSelection.Grants) != 1 || runtimeSelection.Grants[0] != "runtime.incus" {
		return nil, errors.New("selected runtime plugin unavailable")
	}
	if hostSelection == nil || hostSelection.Package.Manifest.Capability != "interactive-host" || hostSelection.Package.Manifest.Runtime.Kind != "assets" || len(hostSelection.Grants) != 1 || hostSelection.Grants[0] != "session.asset.install" {
		return nil, errors.New("selected interactive host unavailable")
	}
	hostPlan, err := plugin.PlanAssets(*hostSelection)
	if err != nil {
		return nil, err
	}
	sourcePlan, err := plugin.PlanAssets(git.source)
	if err != nil {
		return nil, err
	}
	var agentPlan *plugin.AssetPlan
	if cfg.AgentAdapter != nil {
		if err := privateTrustedFile(cfg.AgentAdapter.ActivationPath); err != nil {
			return nil, fmt.Errorf("agent adapter activation: %w", err)
		}
		selections, e := plugin.LoadActivation(cfg.AgentAdapter.ActivationPath)
		if e != nil {
			return nil, e
		}
		for i := range selections {
			v := &selections[i]
			if v.Package.Manifest.ID != cfg.AgentAdapter.PluginID {
				continue
			}
			if v.Package.Manifest.Capability != "agent-adapter" || v.Package.Manifest.Runtime.Kind != "assets" || len(v.Grants) != 1 || v.Grants[0] != "agent.status.report" {
				return nil, errors.New("selected agent adapter has an unsafe grant or kind")
			}
			plan, e := plugin.PlanAssets(*v)
			if e != nil {
				return nil, e
			}
			agentPlan = &plan
			break
		}
		if agentPlan == nil {
			return nil, errors.New("selected agent adapter unavailable")
		}
	}
	id, err := store.InstanceID(ctx)
	if err != nil {
		return nil, err
	}
	var environmentSelection *plugin.Active
	pool := ""
	if cfg.Environment != nil {
		if err := privateTrustedFile(cfg.Environment.ActivationPath); err != nil {
			return nil, fmt.Errorf("environment activation: %w", err)
		}
		selections, err := plugin.LoadActivation(cfg.Environment.ActivationPath)
		if err != nil {
			return nil, err
		}
		for i := range selections {
			v := &selections[i]
			if v.Package.Manifest.ID == cfg.Environment.PluginID {
				if v.Package.Manifest.Capability != "environment" || v.Package.Manifest.Runtime.Kind != "wasi-command" || len(v.Grants) != 1 || v.Grants[0] != "environment.nix" {
					return nil, errors.New("selected environment plugin has an unsafe grant or config")
				}
				environmentSelection = v
			}
		}
		if environmentSelection == nil {
			return nil, errors.New("selected environment plugin unavailable")
		}
		pool = cfg.Environment.BuilderStoragePool
	}
	var egress *runtimeincus.PublicEgressConfig
	if p := cfg.PublicEgress; p != nil {
		egress = &runtimeincus.PublicEgressConfig{Network: p.Network, ACL: p.ACL, BridgeIPv4: p.BridgeIPv4, DNS: p.DNS, SudoBinary: p.SudoBinary, NftBinary: p.NftBinary, BridgeProofBinary: p.BridgeProofBinary}
	}
	runtime, err := runtimeincus.New(runtimeincus.Config{Binary: cfg.IncusBinary, UserSocket: cfg.IncusUserSocket, Project: cfg.IncusProject, EndpointPrefix: cfg.EndpointPrefix, DiskSourceCeilings: cfg.DiskSourceCeilings, BuilderStoragePool: pool, PInstanceID: id, PublicEgress: egress})
	if err != nil {
		return nil, err
	}
	if err = runtime.CheckConfinement(ctx); err != nil {
		return nil, err
	}
	endpoints, err := newEndpointManager(cfg.EndpointPrefix, git.info.Endpoint, store)
	if err != nil {
		return nil, err
	}
	l := &lifecycle{store: store, cfg: cfg, git: git, runtime: runtime, runtimePlugin: *runtimeSelection, environmentPlugin: environmentSelection, hostPlan: hostPlan, sourcePlan: sourcePlan, agentPlan: agentPlan, endpoints: endpoints, instanceID: id, ctx: ctx, working: map[string]bool{}, queueSlots: make(chan struct{}, 8), sessionLocks: map[string]chan struct{}{}, confinementCheck: runtime.CheckConfinement, startActive: map[string]bool{}, changedPolicies: map[string]bool{}}
	after := ""
	for {
		projects, next, e := store.ListProjects(ctx, after, 100)
		if e != nil {
			return nil, e
		}
		for _, p := range projects {
			if p.Registry == "active" {
				selected, configured := cfg.PolicyForProject(p.Path)
				if !configured {
					// Keep the immutable historical project/session snapshot;
					// absent exact authority is invalid, never a fallback grant.
					l.changedPolicies[p.Path] = true
					continue
				}
				captured, e := control.CaptureProjectPolicy(selected, l.grantBoundary())
				if e != nil {
					l.changedPolicies[p.Path] = true
					continue
				}
				policy, selectedSHA, e := control.ProjectPolicySnapshot(captured)
				if e != nil {
					return nil, e
				}
				oldRaw, oldSHA, e := store.ProjectPolicyRecord(ctx, p.Path)
				if e != nil {
					return nil, e
				}
				if e = store.SetProjectPolicy(ctx, p.Path, policy); e != nil {
					return nil, e
				}
				newSHA, e := store.ProjectPolicySHA(ctx, p.Path)
				if e != nil {
					return nil, e
				}
				if newSHA != selectedSHA {
					return nil, errors.New("stored project policy differs from selected authority")
				}
				if projectPolicyRecordChanged(oldRaw, oldSHA, selectedSHA) {
					l.changedPolicies[p.Path] = true
				}
			}
		}
		if next == "" {
			break
		}
		after = next
	}
	return l, nil
}

func projectPolicyRecordChanged(raw json.RawMessage, storedSHA, selectedSHA string) bool {
	oldDigest := sha256.Sum256(raw)
	if hex.EncodeToString(oldDigest[:]) != storedSHA {
		return true
	}
	oldSemantic, err := control.StoredProjectPolicySHA(raw)
	return err != nil || oldSemantic != selectedSHA
}

func (l *lifecycle) grantBoundary() control.GrantBoundary {
	state := ""
	if l.store != nil {
		state = l.store.StateDir()
	}
	return control.GrantBoundary{Ceilings: l.cfg.DiskSourceCeilings, StateDir: state, EndpointPrefix: l.cfg.EndpointPrefix, IncusSocket: l.cfg.IncusUserSocket}
}

func (l *lifecycle) currentProjectPolicy(ctx context.Context, project string) error {
	selected, configured := l.cfg.PolicyForProject(project)
	if !configured {
		return control.ErrConflict
	}
	_, shapeSHA, err := control.ProjectPolicySnapshot(selected)
	if err != nil {
		return err
	}
	raw, storedSHA, err := l.store.ProjectPolicyRecord(ctx, project)
	if err != nil {
		return err
	}
	rawSum := sha256.Sum256(raw)
	if hex.EncodeToString(rawSum[:]) != storedSHA {
		return control.ErrConflict
	}
	storedShape, err := control.StoredProjectPolicyShapeSHA(raw)
	if err != nil || storedShape != shapeSHA {
		return control.ErrConflict
	}
	stored, err := control.ParseStoredProjectPolicy(raw)
	if err != nil {
		return err
	}
	if err := control.RevalidateProjectPolicy(stored, l.grantBoundary()); err != nil {
		return control.ErrConflict
	}
	return nil
}

func (l *lifecycle) Close() { l.endpoints.Close() }
func (l *lifecycle) Recover() error {
	ops, err := l.store.UnfinishedCreates(l.ctx)
	if err != nil {
		return err
	}
	collections, err := l.store.UnfinishedEnvironmentCollections(l.ctx)
	if err != nil {
		return err
	}
	ops = append(ops, collections...)
	workspace, err := l.store.UnfinishedWorkspaceInspects(l.ctx)
	if err != nil {
		return err
	}
	ops = append(ops, workspace...)
	discards, err := l.store.UnfinishedRemovals(l.ctx)
	if err != nil {
		return err
	}
	ops = append(ops, discards...)
	renames, err := l.store.UnfinishedRenames(l.ctx)
	if err != nil {
		return err
	}
	ops = append(ops, renames...)
	retainedRenames, err := l.store.UnfinishedRetainedRenames(l.ctx)
	if err != nil {
		return err
	}
	ops = append(ops, retainedRenames...)
	retainedDeletes, err := l.store.UnfinishedRetainedDeletes(l.ctx)
	if err != nil {
		return err
	}
	ops = append(ops, retainedDeletes...)
	repairs, err := l.store.UnfinishedRepairs(l.ctx)
	if err != nil {
		return err
	}
	ops = append(ops, repairs...)
	preparations, err := l.store.UnfinishedRepairPreparations(l.ctx)
	if err != nil {
		return err
	}
	ops = append(ops, preparations...)
	refRepairs, err := l.store.UnfinishedRefRepairs(l.ctx)
	if err != nil {
		return err
	}
	ops = append(ops, refRepairs...)
	principalRepairs, err := l.store.UnfinishedPrincipalRepairs(l.ctx)
	if err != nil {
		return err
	}
	ops = append(ops, principalRepairs...)
	recordRepairs, err := l.store.UnfinishedRecordRepairs(l.ctx)
	if err != nil {
		return err
	}
	ops = append(ops, recordRepairs...)
	after := ""
	for {
		sessions, next, e := l.store.ListSessions(l.ctx, after, 100)
		if e != nil {
			return e
		}
		for _, s := range sessions {
			if s.Registry == "removing" {
				continue
			}
			if e = recoverSessionEndpoint(l.ctx, s, l.store.CreationForSession, l.endpoints.Ensure); e != nil {
				return e
			}
			if l.events != nil {
				policy := l.policyCondition(l.ctx, s)
				l.seedRecoveredSession(s, policy)
				if l.changedPolicies[s.Project] {
					l.events.emit("session.policy_changed", s.Project, s.UUID, s.Branch, "policy_condition", policy)
				}
			}
		}
		if next == "" {
			break
		}
		after = next
	}
	for _, op := range ops {
		if deferBlockedCreationUntilRetry(op) {
			continue
		}
		if op.Kind == "environment.collect" && op.Status != "running" {
			continue
		}
		if err = l.enqueue(op.ID); err != nil {
			return err
		}
	}
	go l.schedule()
	return nil
}
func (l *lifecycle) schedule() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-ticker.C:
			ops, err := l.store.UnfinishedCreates(l.ctx)
			if err != nil {
				log.Printf("creation reconciliation: %v", err)
				continue
			}
			collections, err := l.store.UnfinishedEnvironmentCollections(l.ctx)
			if err != nil {
				log.Printf("collection reconciliation: %v", err)
				continue
			}
			ops = append(ops, collections...)
			workspace, err := l.store.UnfinishedWorkspaceInspects(l.ctx)
			if err != nil {
				log.Printf("workspace reconciliation: %v", err)
				continue
			}
			ops = append(ops, workspace...)
			discards, err := l.store.UnfinishedRemovals(l.ctx)
			if err != nil {
				log.Printf("discard reconciliation: %v", err)
				continue
			}
			ops = append(ops, discards...)
			for _, op := range ops {
				if op.Status == "running" {
					_ = l.enqueue(op.ID)
				}
			}
		}
	}
}
func (l *lifecycle) enqueue(id string) error {
	l.mu.Lock()
	if l.working[id] {
		l.mu.Unlock()
		return nil
	}
	select {
	case l.queueSlots <- struct{}{}:
	default:
		l.mu.Unlock()
		return nil
	}
	l.working[id] = true
	l.mu.Unlock()
	go func() {
		defer func() { l.mu.Lock(); delete(l.working, id); l.mu.Unlock(); <-l.queueSlots }()
		l.process(id)
	}()
	return nil
}

func (l *lifecycle) selection() control.CreationSelection {
	s := control.CreationSelection{RuntimeID: l.runtimePlugin.Package.Manifest.ID, RuntimeSHA256: l.runtimePlugin.Package.SHA256, HostID: l.hostPlan.PackageID, HostSHA256: l.hostPlan.PackageSHA256, SourceID: l.sourcePlan.PackageID, SourceSHA256: l.sourcePlan.PackageSHA256}
	if l.agentPlan != nil {
		s.AgentID, s.AgentSHA256 = l.agentPlan.PackageID, l.agentPlan.PackageSHA256
	}
	return s
}

func (l *lifecycle) environmentIntent() *control.EnvironmentIntent {
	if l.environmentPlugin == nil || l.cfg.Environment == nil {
		return nil
	}
	config := sha256.Sum256(l.environmentPlugin.Config)
	return &control.EnvironmentIntent{
		ModuleID:            l.environmentPlugin.Package.Manifest.ID,
		ModuleSHA256:        l.environmentPlugin.Package.SHA256,
		ConfigSHA256:        hex.EncodeToString(config[:]),
		System:              l.cfg.Environment.System,
		BaseFingerprint:     l.cfg.BaseImageFingerprint,
		BuilderStoragePool:  l.cfg.Environment.BuilderStoragePool,
		BuilderPolicySHA256: runtimeincus.BuilderPolicyDigest(),
	}
}

func (l *lifecycle) sessionLockSlot(id string) chan struct{} {
	l.mu.Lock()
	if l.sessionLocks == nil {
		l.sessionLocks = map[string]chan struct{}{}
	}
	slot := l.sessionLocks[id]
	if slot == nil {
		slot = make(chan struct{}, 1)
		slot <- struct{}{}
		l.sessionLocks[id] = slot
	}
	l.mu.Unlock()
	return slot
}

func (l *lifecycle) lockSession(ctx context.Context, id string) (func(), error) {
	slot := l.sessionLockSlot(id)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-slot:
		return func() { slot <- struct{}{} }, nil
	default:
		return nil, control.ErrConflict
	}
}

func (l *lifecycle) CreateProject(ctx context.Context, req control.BlankProjectRequest) (control.Operation, error) {
	_, priorErr := l.store.GetOperationByKey(ctx, req.Key)
	if req.URL != "" {
		if err := gitservice.ValidateOriginURL(req.URL); err != nil {
			return control.Operation{}, control.ErrInvalid
		}
	}
	var policy []byte
	if priorErr == nil {
		// The store returns the accepted exact-key operation before using policy.
	} else if selected, configured := l.cfg.PolicyForProject(req.Project); configured {
		var snapshotErr error
		var captured control.ProjectPolicy
		captured, snapshotErr = control.CaptureProjectPolicy(selected, l.grantBoundary())
		if snapshotErr == nil {
			policy, _, snapshotErr = control.ProjectPolicySnapshot(captured)
		}
		if snapshotErr != nil {
			return control.Operation{}, control.ErrConflict
		}
	} else if errors.Is(priorErr, control.ErrNotFound) {
		return control.Operation{}, control.ErrConflict
	}
	op, err := l.store.BeginBlankProjectWithCapacity(ctx, req, policy, l.cfg.BaseImageFingerprint, l.selection(), l.observeSessionCapacity)
	if err != nil {
		return op, err
	}
	if errors.Is(priorErr, control.ErrNotFound) {
		l.recordCreation(op, "")
	}
	if op.Status != "completed" {
		if err = l.enqueue(op.ID); err != nil {
			return op, err
		}
	}
	return op, nil
}
func (l *lifecycle) CreateSession(ctx context.Context, req control.ReserveSessionRequest) (control.Operation, error) {
	if !control.ValidSessionCreateRequest(req) {
		return control.Operation{}, control.ErrInvalid
	}
	_, priorErr := l.store.GetOperationByKey(ctx, req.Key)
	if _, configured := l.cfg.PolicyForProject(req.Project); !configured && errors.Is(priorErr, control.ErrNotFound) {
		return control.Operation{}, control.ErrConflict
	}
	if errors.Is(priorErr, control.ErrNotFound) {
		if err := l.currentProjectPolicy(ctx, req.Project); err != nil {
			return control.Operation{}, control.ErrConflict
		}
	}
	if req.OriginRef != "" {
		if err := l.store.CheckOriginKeyConflict(ctx, req.Key); err != nil {
			return control.Operation{}, err
		}
		if priorErr == nil {
			op, _, err := l.store.BeginSessionCreateCapturedWithCapacity(ctx, req, l.cfg.BaseImageFingerprint, l.selection(), func(context.Context, control.ReserveSessionRequest) (control.CapturedSource, error) {
				return control.CapturedSource{}, control.ErrConflict
			}, l.observeSessionCapacity, l.environmentIntent())
			if err != nil {
				return op, err
			}
			if op.Status != "completed" && op.Status != "superseded" {
				if err = l.enqueue(op.ID); err != nil {
					return op, err
				}
			}
			return op, nil
		}
		var op control.Operation
		var err error
		err = l.git.backend.WithOrigin(ctx, req.Project, func(scope *gitservice.OriginScope) error {
			state, _, e := l.store.Origin(ctx, req.Project)
			if e != nil {
				return e
			}
			if state.URL == "" {
				return control.ErrConflict
			}
			refs, e := scope.Observe(ctx, state.URL)
			writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if e != nil {
				_ = l.store.StoreOriginRefresh(writeCtx, req.Project, state.URL, nil, "origin refresh failed; check SSH identity, host key, URL and access")
				return e
			}
			if e = l.store.StoreOriginRefresh(writeCtx, req.Project, state.URL, refs, ""); e != nil {
				return e
			}
			var selected *plugin.GitOriginRef
			for i := range refs {
				if refs[i].Ref == req.OriginRef {
					selected = &refs[i]
					break
				}
			}
			if selected == nil || selected.CommitOID != req.ExpectedCommitOID {
				return control.ErrConflict
			}
			fetchFailed := false
			op, _, e = l.store.BeginSessionCreateCapturedWithCapacity(ctx, req, l.cfg.BaseImageFingerprint, l.selection(), func(ctx context.Context, r control.ReserveSessionRequest) (control.CapturedSource, error) {
				_, exists, e := l.git.backend.InspectBranchRef(ctx, r.Project, r.Branch)
				if e != nil {
					return control.CapturedSource{}, e
				}
				if exists {
					return control.CapturedSource{}, control.ErrConflict
				}
				oid, e := scope.Fetch(ctx, r.OriginRef, r.ExpectedCommitOID)
				if e != nil {
					fetchFailed = true
					return control.CapturedSource{}, e
				}
				return control.CapturedSource{OID: oid, OriginURL: state.URL, OriginRef: r.OriginRef}, nil
			}, l.observeSessionCapacity, l.environmentIntent())
			if fetchFailed {
				failedCtx, cancelFailed := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				_ = l.store.StoreOriginRefresh(failedCtx, req.Project, state.URL, nil, "origin fetch failed; check source ref and access")
				cancelFailed()
			}
			return e
		})
		if err != nil {
			return op, err
		}
		if errors.Is(priorErr, control.ErrNotFound) {
			l.recordCreation(op, req.Branch)
		}
		if op.Status != "completed" && op.Status != "superseded" {
			if err = l.enqueue(op.ID); err != nil {
				return op, err
			}
		}
		return op, nil
	}
	op, _, err := l.store.BeginSessionCreateCapturedWithCapacity(ctx, req, l.cfg.BaseImageFingerprint, l.selection(),
		l.git.backend.CaptureLocalSource, l.observeSessionCapacity, l.environmentIntent())
	if err != nil {
		return op, err
	}
	if errors.Is(priorErr, control.ErrNotFound) {
		l.recordCreation(op, req.Branch)
	}
	if op.Status != "completed" && op.Status != "superseded" {
		if err = l.enqueue(op.ID); err != nil {
			return op, err
		}
	}
	return op, nil
}
func (l *lifecycle) Retry(ctx context.Context, id string) (control.Operation, error) {
	op, err := l.store.GetOperation(ctx, id)
	if err != nil {
		return op, err
	}
	if op.Kind != "project.create" && op.Kind != "session.create" && op.Kind != "environment.collect" && op.Kind != "workspace.inspect" && op.Kind != "workspace.loss.inspect" && op.Kind != "session.discard" && op.Kind != "session.delete" && op.Kind != "session.rename" && op.Kind != "project.retained.rename" && op.Kind != "project.retained.delete" && op.Kind != "session.repair" && op.Kind != "session.repair.prepare" && op.Kind != "session.ref.repair" && op.Kind != "session.principal.repair" && op.Kind != "session.record.repair" {
		return op, control.ErrInvalid
	}
	if op.Status == "superseded" {
		return op, control.ErrConflict
	}
	if op.Status != "completed" {
		if op.Status == "blocked" {
			if err = l.store.AdvanceOperation(ctx, op.ID, "running", op.Phase, op.Committed, op.Evidence, ""); err != nil {
				return op, err
			}
			op.Status = "running"
			op.Diagnostic = ""
			l.recordProgress(op, "running")
		}
		if err = l.enqueue(op.ID); err != nil {
			return op, err
		}
	}
	return op, nil
}

func (l *lifecycle) process(id string) {
	ctx := l.ctx
	op, err := l.store.GetOperation(ctx, id)
	if err != nil {
		return
	}
	if op.Kind == "environment.collect" {
		l.processEnvironmentCollection(op)
		return
	}
	if op.Kind == "session.discard" || op.Kind == "session.delete" {
		l.processDiscard(op)
		return
	}
	if op.Kind == "session.rename" {
		l.processRename(op)
		return
	}
	if op.Kind == "project.retained.rename" {
		l.processRetainedRename(op)
		return
	}
	if op.Kind == "project.retained.delete" {
		l.processRetainedDelete(op)
		return
	}
	if op.Kind == "session.repair" {
		l.processRepair(op)
		return
	}
	if op.Kind == "session.repair.prepare" {
		l.processRepairPrepare(op)
		return
	}
	if op.Kind == "session.ref.repair" {
		l.processRefRepair(op)
		return
	}
	if op.Kind == "session.principal.repair" {
		l.processPrincipalRepair(op)
		return
	}
	if op.Kind == "session.record.repair" {
		l.processRecordRepair(op)
		return
	}
	if op.Kind == "workspace.inspect" || op.Kind == "workspace.loss.inspect" {
		l.processWorkspaceInspect(op)
		return
	}
	if op.Status == "completed" || op.Status == "superseded" {
		return
	}
	if op.Kind == "session.create" {
		var release func()
		op, release, err = l.lockCreationOperation(ctx, op, l.store.GetOperation)
		if err != nil {
			return
		}
		defer release()
	}
	fail := func(err error) {
		if ctx.Err() != nil {
			return
		}
		message := err.Error()
		if len(message) > 900 {
			message = message[:900]
		}
		if l.store.AdvanceOperation(ctx, op.ID, "blocked", op.Phase, op.Committed, op.Evidence, message) == nil {
			l.recordProgress(op, "blocked")
		}
	}
	if op.Kind == "session.create" && op.Phase == "replacement-cleanup" {
		if err = l.completeReplacementCleanup(ctx, &op); err != nil {
			fail(err)
			return
		}
	}
	var pinned control.CreationSelection
	if op.Kind == "project.create" {
		var ev control.BlankProjectEvidence
		if json.Unmarshal(op.Evidence, &ev) != nil {
			fail(errors.New("project evidence unreadable"))
			return
		}
		pinned = ev.Selection
	} else {
		ev, e := control.Evidence(op)
		if e != nil {
			fail(e)
			return
		}
		pinned = ev.Selection
	}
	if !pinned.Valid() || pinned != l.selection() {
		fail(errors.New("pinned creation plugin selection unavailable; repair required"))
		return
	}
	if op.Kind == "project.create" && op.Phase == "repo-pending" {
		var req control.BlankProjectRequest
		if json.Unmarshal(op.Request, &req) != nil {
			fail(errors.New("invalid project request"))
			return
		}
		if req.URL != "" {
			var created control.Session
			var bootstrap bool
			err = l.git.backend.WithOrigin(ctx, op.Project, func(scope *gitservice.OriginScope) error {
				refs, e := scope.Observe(ctx, req.URL)
				if e != nil {
					return e
				}
				if e = l.git.backend.EnsureBare(ctx, op.Project); e != nil {
					return e
				}
				created, bootstrap, e = l.store.CommitOriginProject(ctx, op.ID, refs)
				return e
			})
			if err != nil {
				fail(err)
				return
			}
			if !bootstrap {
				l.recordProgress(op, "completed")
				return
			}
			l.recordCreatingSession(created)
		} else {
			if err = l.git.backend.EnsureBare(ctx, op.Project); err != nil {
				fail(err)
				return
			}
			created, commitErr := l.store.CommitBlankProject(ctx, op.ID)
			if commitErr != nil {
				fail(commitErr)
				return
			}
			l.recordCreatingSession(created)
		}
		op, err = l.store.GetOperation(ctx, id)
		if err != nil {
			return
		}
	}
	session, err := l.store.GetSession(ctx, op.SessionUUID)
	if err != nil {
		fail(err)
		return
	}
	selectedPolicy, policyErr := control.ParseStoredProjectPolicy(session.Policy)
	if policyErr != nil || selectedPolicy.Network == "public-egress" && l.cfg.PublicEgress == nil || control.RevalidateProjectPolicy(selectedPolicy, l.grantBoundary()) != nil {
		fail(errors.New("stored project policy is unavailable"))
		return
	}
	var image, initialOID string
	var environmentEvidence control.CreationEvidence
	if op.Kind == "project.create" {
		var ev control.BlankProjectEvidence
		if json.Unmarshal(op.Evidence, &ev) != nil {
			fail(errors.New("invalid project evidence"))
			return
		}
		image = ev.ImageFingerprint
	} else {
		ev, e := control.Evidence(op)
		if e != nil {
			fail(e)
			return
		}
		image = ev.ImageFingerprint
		initialOID = ev.CapturedOID
		environmentEvidence = ev
		if op.Phase == "source-ready" {
			var req control.ReserveSessionRequest
			if json.Unmarshal(op.Request, &req) != nil {
				fail(errors.New("invalid creation request"))
				return
			}
			if e = ensureBranchAssigned(ctx, l.git.backend, op.ID, req, initialOID); e != nil {
				fail(e)
				return
			}
			if e = l.advance(&op, "branch-assigned", true); e != nil {
				fail(e)
				return
			}
		}
	}
	if op.Kind == "project.create" {
		tip, exists, e := l.git.backend.InspectBranchRef(ctx, session.Project, "main")
		if e != nil {
			fail(e)
			return
		}
		if phaseRank(op.Phase) < phaseRank("workspace-ready") && (exists || tip != "") {
			fail(errors.New("unborn main unexpectedly has a ref"))
			return
		}
	}
	if op.Kind == "session.create" {
		tip, exists, e := l.git.backend.InspectBranchRef(ctx, session.Project, session.Branch)
		if e != nil {
			fail(e)
			return
		}
		if phaseRank(op.Phase) < phaseRank("workspace-ready") && (!exists || tip != initialOID) {
			fail(errors.New("assigned branch changed from captured tip"))
			return
		}
	}
	if op.Kind == "session.create" && environmentEvidence.Environment != nil {
		image, err = l.ensureEnvironment(ctx, &op, environmentEvidence)
		if err != nil {
			fail(err)
			return
		}
		environmentEvidence, err = control.Evidence(op)
		if err != nil {
			fail(err)
			return
		}
	}
	if op.Kind == "session.create" {
		// Historical early checkpoints precede the durable principals marker and
		// therefore cannot have dispatched session init. Historical principals-ready
		// without the new marker stays ambiguous and must never be relabeled safe.
		if err = prepareCreationInitState(&op); err != nil {
			fail(err)
			return
		}
	}
	dir, err := l.endpoints.Ensure(ctx, session.UUID)
	if err != nil {
		fail(err)
		return
	}
	key, pub, err := l.sessionKey(ctx, session.UUID)
	if err != nil {
		fail(err)
		return
	}
	if err = l.store.EnsureSessionGitPrincipal(ctx, gitservice.Fingerprint(pub), session.Project, session.UUID); err != nil {
		fail(err)
		return
	}
	if err = l.advance(&op, "principals-ready", true); err != nil {
		fail(err)
		return
	}
	publicIPv4, networkErr := l.publicSessionIPv4(ctx, session.UUID, selectedPolicy)
	if networkErr != nil {
		fail(networkErr)
		return
	}
	native := runtimeincus.Session{InstanceUUID: l.instanceID, SessionUUID: session.UUID, ProjectPath: session.Project, AssignedBranch: session.Branch, InitialOID: initialOID, ContractVersion: "1", ImageFingerprint: image, EndpointSource: dir, Grants: nativeFilesystemGrants(selectedPolicy), PublicIPv4: publicIPv4}
	scoped := runtimeincus.Scoped{Backend: l.runtime, Session: native}
	if op.Kind == "session.create" {
		scoped.BeforeCreate = func() error {
			return recordCreationInitAttempt(ctx, &op, func(call context.Context, raw json.RawMessage) error {
				return l.store.AdvanceOperation(call, op.ID, "running", op.Phase, op.Committed, raw, "")
			})
		}
	}
	runtimeRunning := false
	if phaseRank(op.Phase) >= phaseRank("runtime-created") {
		state, inspectErr := plugin.RunRuntime(ctx, l.runtimePlugin, "runtime.inspect", scoped)
		if inspectErr != nil {
			fail(inspectErr)
			return
		}
		runtimeRunning = state.Status == "Running"
		if runtimeRunning && phaseRank(op.Phase) < phaseRank("assembly-ready") {
			fail(errors.New("creating runtime is running before verified assembly"))
			return
		}
	}
	if !runtimeRunning {
		run := func(call context.Context, kind string) (plugin.RuntimeState, error) {
			return plugin.RunRuntime(call, l.runtimePlugin, kind, scoped)
		}
		if _, err = ensureStoppedRuntime(ctx, run); err != nil {
			fail(err)
			return
		}
		if err = l.advance(&op, "runtime-created", true); err != nil {
			fail(err)
			return
		}
		activation := runtimekit.Config{Schema: "p.runtime-session/v1", Activation: "base", Command: selectedPolicy.Command}
		if environmentEvidence.EnvironmentState != nil && environmentEvidence.EnvironmentState.Key != "" {
			activation = runtimekit.Config{Schema: "p.runtime-session/v2", Activation: "devshell", MaterialSHA256: environmentEvidence.EnvironmentState.MaterialDigest, Command: selectedPolicy.Command}
		}
		if l.agentPlan != nil {
			if len(l.agentPlan.Files) != 1 || l.agentPlan.Files[0].Role != "p-codex-adapter" {
				fail(errors.New("selected agent asset role unavailable"))
				return
			}
			activation.Schema = "p.runtime-session/v3"
			activation.AgentSHA256 = l.agentPlan.Files[0].SHA256
		}
		if len(selectedPolicy.FilesystemMounts) != 0 {
			activation.Schema = "p.runtime-session/v4"
			activation.FilesystemMounts = guestFilesystemGrants(selectedPolicy)
		}
		if publicIPv4 != "" {
			activation.Schema = "p.runtime-session/v5"
			activation.PublicNetwork = l.guestPublicNetwork(publicIPv4)
		}
		workspace := runtimekit.WorkspaceConfig{Schema: "p.workspace/v1", Repository: session.Project, Branch: session.Branch, InitialOID: initialOID}
		if environmentEvidence.Environment != nil && environmentEvidence.EnvironmentState != nil {
			workspace.Schema = "p.workspace/v2"
			workspace.EnvironmentCommitOID = initialOID
			workspace.EnvironmentSelection = "base-no-flake"
			if environmentEvidence.EnvironmentState.Key != "" {
				workspace.EnvironmentSelection = "devshell"
				workspace.EnvironmentKey = environmentEvidence.EnvironmentState.Key
			} else if environmentEvidence.EnvironmentState.FlakePresent {
				workspace.EnvironmentSelection = "base-no-default"
			}
		}
		assembly := runtimeincus.Assembly{HostAssets: l.hostPlan, SourceAssets: l.sourcePlan, AgentAssets: l.agentPlan, SessionConfig: activation, Workspace: workspace, Identity: key, ServerPublicKey: l.git.info.HostPublicKey}
		scoped.Assembly = &assembly
		if err = assembleStoppedRuntime(ctx, run); err != nil {
			fail(err)
			return
		}
		if err = l.advance(&op, "assembly-ready", true); err != nil {
			fail(err)
			return
		}
		if err = l.startOwned(ctx, scoped); err != nil {
			fail(err)
			return
		}
	}
	if err = l.awaitOwnedHost(ctx, scoped); err != nil {
		fail(err)
		return
	}
	if err = l.advance(&op, "workspace-ready", true); err != nil {
		fail(err)
		return
	}
	if err = l.store.CompleteCreation(ctx, op.ID); err != nil {
		fail(err)
	} else {
		l.recordProgress(op, "completed")
		_, _ = l.InspectSession(ctx, op.SessionUUID)
	}
}

func guestFilesystemGrants(policy control.ProjectPolicy) []runtimekit.FilesystemGrant {
	grants := make([]runtimekit.FilesystemGrant, 0, len(policy.FilesystemMounts))
	for _, g := range policy.FilesystemMounts {
		grants = append(grants, runtimekit.FilesystemGrant{Name: g.Name, Type: g.Type, Access: g.Access, Executable: g.Executable,
			Device: g.SourceIdentity.Device, Inode: g.SourceIdentity.Inode})
	}
	return grants
}

func nativeFilesystemGrants(policy control.ProjectPolicy) []runtimeincus.FilesystemGrant {
	grants := make([]runtimeincus.FilesystemGrant, 0, len(policy.FilesystemMounts))
	for _, g := range policy.FilesystemMounts {
		id := g.SourceIdentity
		grants = append(grants, runtimeincus.FilesystemGrant{Name: g.Name, Source: g.Source, Type: g.Type, Access: g.Access, Executable: g.Executable,
			Device: id.Device, Inode: id.Inode, OwnerUID: id.OwnerUID, OwnerGID: id.OwnerGID})
	}
	return grants
}

func (l *lifecycle) stopOwned(ctx context.Context, scoped runtimeincus.Scoped) error {
	_, err := plugin.RunRuntime(ctx, l.runtimePlugin, "runtime.stop", scoped)
	return err
}
func (l *lifecycle) awaitOwnedHost(ctx context.Context, scoped runtimeincus.Scoped) error {
	return awaitHostReady(ctx, 100*time.Second, func(call context.Context) (plugin.RuntimeState, error) {
		return plugin.RunRuntime(call, l.runtimePlugin, "runtime.observe-host", scoped)
	}, func(call context.Context) error { return l.stopOwned(call, scoped) })
}
func (l *lifecycle) startOwned(ctx context.Context, scoped runtimeincus.Scoped) error {
	_, err := plugin.RunRuntime(ctx, l.runtimePlugin, "runtime.start", scoped)
	if err == nil {
		return nil
	}
	observed, e := plugin.RunRuntime(ctx, l.runtimePlugin, "runtime.observe-host", scoped)
	if e == nil && observed.DiagnosticAvailable && observed.Diagnostic != "" {
		err = errors.New(observed.Diagnostic)
	}
	if e != nil || observed.Exists && observed.Status == "Running" {
		stopCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		if stopErr := l.stopOwned(stopCtx, scoped); stopErr != nil {
			err = fmt.Errorf("%v; stopping failed: %w", err, stopErr)
		}
	}
	return err
}

func (l *lifecycle) advance(op *control.Operation, phase string, committed bool) error {
	if phaseRank(op.Phase) > phaseRank(phase) {
		return nil
	}
	if err := l.store.AdvanceOperation(l.ctx, op.ID, "running", phase, committed, op.Evidence, ""); err != nil {
		return err
	}
	op.Phase = phase
	op.Committed = committed
	op.Status = "running"
	l.recordProgress(*op, "running")
	return nil
}

func phaseRank(phase string) int {
	switch phase {
	case "repo-pending":
		return 0
	case "source-ready":
		return 1
	case "branch-assigned":
		return 2
	case "environment-publishing":
		return 3
	case "environment-ready":
		return 4
	case "principals-ready":
		return 5
	case "runtime-created":
		return 6
	case "assembly-ready":
		return 7
	case "workspace-ready":
		return 8
	case "established":
		return 9
	default:
		return -1
	}
}

func (l *lifecycle) sessionKey(ctx context.Context, id string) ([]byte, ssh.PublicKey, error) {
	return sessionKeyAt(ctx, id, l.store.StateDir(), l.store.SessionGitPrincipal)
}

func sessionKeyAt(ctx context.Context, id, stateDir string, principal func(context.Context, string) (string, bool, error)) ([]byte, ssh.PublicKey, error) {
	path := filepath.Join(stateDir, "session_keys")
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != 0700 || owner.Uid != uint32(os.Geteuid()) {
		return nil, nil, errors.New("session key directory is unsafe")
	}
	file := filepath.Join(path, id)
	registered, exists, err := principal(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if exists {
		if _, err = os.Lstat(file); errors.Is(err, os.ErrNotExist) {
			return nil, nil, errors.New("registered session key missing; repair required")
		}
		if err != nil {
			return nil, nil, err
		}
	}
	key, pub, err := loadOrCreateKey(file)
	if err != nil {
		return nil, nil, err
	}
	if exists && registered != gitservice.Fingerprint(pub) {
		return nil, nil, errors.New("registered session key changed; repair required")
	}
	return key, pub, nil
}

func (l *lifecycle) runtimeSession(ctx context.Context, s control.Session) (runtimeincus.Session, error) {
	policy, err := control.ParseStoredProjectPolicy(s.Policy)
	if err != nil {
		return runtimeincus.Session{}, err
	}
	grants := nativeFilesystemGrants(policy)
	op, err := l.store.CreationForSession(ctx, s.UUID)
	if err != nil {
		return runtimeincus.Session{}, err
	}
	image := ""
	if op.Kind == "project.create" {
		var ev control.BlankProjectEvidence
		if json.Unmarshal(op.Evidence, &ev) != nil {
			return runtimeincus.Session{}, control.ErrInvalid
		}
		image = ev.ImageFingerprint
	} else {
		ev, e := control.Evidence(op)
		if e != nil {
			return runtimeincus.Session{}, e
		}
		image = ev.ImageFingerprint
	}
	if accepted, ok, e := l.store.AcceptedRepairImage(ctx, s.UUID); e != nil {
		return runtimeincus.Session{}, e
	} else if ok {
		if accepted.Project != s.Project || accepted.Branch != s.Branch {
			return runtimeincus.Session{}, control.ErrConflict
		}
		image = accepted.ImageFingerprint
	}
	initialOID := ""
	if op.Kind == "session.create" {
		ev, e := control.Evidence(op)
		if e != nil {
			return runtimeincus.Session{}, e
		}
		initialOID = ev.CapturedOID
	}
	publicIPv4, err := l.publicSessionIPv4(ctx, s.UUID, policy)
	if err != nil {
		return runtimeincus.Session{}, err
	}
	return runtimeincus.Session{InstanceUUID: l.instanceID, SessionUUID: s.UUID, ProjectPath: s.Project, AssignedBranch: s.Branch, InitialOID: initialOID, ContractVersion: "1", ImageFingerprint: image, EndpointSource: filepath.Join(l.cfg.EndpointPrefix, s.UUID), Grants: grants, PublicIPv4: publicIPv4}, nil
}

func (l *lifecycle) publicSessionIPv4(ctx context.Context, sessionID string, policy control.ProjectPolicy) (string, error) {
	if policy.Network == "none" {
		return "", nil
	}
	if policy.Network != "public-egress" || l.cfg.PublicEgress == nil || policy.PublicEgressSHA256 != l.cfg.PublicEgress.SHA256() {
		return "", errors.New("public egress substrate unavailable")
	}
	slot, err := l.store.PublicAddressSlot(ctx, sessionID)
	if err != nil {
		return "", err
	}
	return l.cfg.PublicEgress.PublicEgressGuestIPv4(slot)
}

func (l *lifecycle) guestPublicNetwork(address string) *runtimekit.PublicNetwork {
	if address == "" || l.cfg.PublicEgress == nil {
		return nil
	}
	return &runtimekit.PublicNetwork{Address: address, Gateway: strings.SplitN(l.cfg.PublicEgress.BridgeIPv4, "/", 2)[0], DNS: append([]string(nil), l.cfg.PublicEgress.DNS...)}
}

func (l *lifecycle) policyCondition(ctx context.Context, s control.Session) string {
	current := func(_ context.Context, project string) (string, error) {
		policy, configured := l.cfg.PolicyForProject(project)
		if !configured {
			return "", control.ErrNotFound
		}
		if policy.Network == "public-egress" && (l.cfg.PublicEgress == nil || policy.PublicEgressSHA256 != l.cfg.PublicEgress.SHA256()) {
			return "", control.ErrConflict
		}
		// A configured grant whose source disappeared or changed cannot be
		// treated as an ordinary policy mismatch. It is an invalid authority.
		if _, err := control.CaptureProjectPolicy(policy, l.grantBoundary()); err != nil {
			return "", err
		}
		_, sha, err := control.ProjectPolicySnapshot(policy)
		return sha, err
	}
	condition := policyCondition(ctx, s, current, l.confinementCheck)
	if condition == "invalid" {
		return condition
	}
	stored, err := control.ParseStoredProjectPolicy(s.Policy)
	if err != nil || control.RevalidateProjectPolicy(stored, l.grantBoundary()) != nil {
		return "invalid"
	}
	if stored.Network == "public-egress" {
		selected, configured := l.cfg.PolicyForProject(s.Project)
		if !configured || selected.Network != "public-egress" || l.cfg.PublicEgress == nil ||
			stored.PublicEgressSHA256 != l.cfg.PublicEgress.SHA256() {
			return "invalid"
		}
	}
	return condition
}

func policyCondition(ctx context.Context, s control.Session, currentPolicy func(context.Context, string) (string, error), confinementCheck func(context.Context) error) string {
	if len(s.Policy) == 0 {
		return "invalid"
	}
	condition := "current"
	if current, e := currentPolicy(ctx, s.Project); e != nil {
		condition = "invalid"
	} else {
		rawDigest := sha256.Sum256(s.Policy)
		if hex.EncodeToString(rawDigest[:]) != s.PolicySHA256 {
			return "invalid"
		}
		previous, err := control.StoredProjectPolicyShapeSHA(s.Policy)
		if err != nil {
			return "invalid"
		}
		if current != previous {
			condition = "outdated"
		}
	}
	if confinementCheck != nil {
		if e := confinementCheck(ctx); e != nil {
			return "invalid"
		}
	}
	return condition
}

func startEligible(ctx context.Context, id string, session func(context.Context, string) (control.Session, error), policy func(context.Context, control.Session) string) (control.Session, error) {
	s, err := session(ctx, id)
	if err != nil {
		return control.Session{}, err
	}
	if s.Registry != "established" || policy(ctx, s) == "invalid" {
		return control.Session{}, control.ErrConflict
	}
	return s, nil
}

func (l *lifecycle) InspectSession(ctx context.Context, id string) (result control.SessionView, resultErr error) {
	defer func() {
		if resultErr != nil {
			return
		}
		count, latest, e := l.store.SessionStatus(ctx, id)
		if e != nil {
			resultErr = e
			return
		}
		result.AttachedCount = count
		result.LatestUnattendedCondition = latest
		l.observeView(result)
	}()
	s, err := l.store.GetSession(ctx, id)
	if err != nil {
		return control.SessionView{}, err
	}
	v := control.NewSessionView(s)
	created, err := l.store.CreationForSession(ctx, s.UUID)
	if err != nil {
		return control.SessionView{}, err
	}
	v.Environment = control.CreationEnvironmentView(created)
	if accepted, ok, e := l.store.AcceptedRepairImage(ctx, s.UUID); e != nil {
		return control.SessionView{}, e
	} else if ok {
		if repaired := control.AcceptedRepairEnvironmentView(accepted); repaired != nil {
			v.Environment = repaired
		}
	}
	v.PolicyCondition = l.policyCondition(ctx, s)
	if s.Registry == "creating" {
		v.Condition = "creating"
		if op, e := l.store.CreationForSession(ctx, id); e == nil {
			v.Diagnostic = op.Diagnostic
		}
		return v, nil
	}
	if s.Registry == "removing" {
		v.Condition = "deleting"
		return v, nil
	}
	native, err := l.runtimeSession(ctx, s)
	if err != nil {
		v.Condition = "unreachable"
		return v, nil
	}
	scoped := runtimeincus.Scoped{Backend: l.runtime, Session: native}
	state, err := plugin.RunRuntime(ctx, l.runtimePlugin, "runtime.observe-host", scoped)
	if err != nil {
		v.Condition = "unreachable"
		return v, nil
	}
	switch {
	case !state.Exists:
		v.Condition = "missing"
	case state.Status == "Stopped":
		v.Condition = "stopped"
	case state.HostReady:
		v.Condition = "ready"
	default:
		v.Condition = "starting"
	}
	if state.DiagnosticAvailable {
		v.Diagnostic = state.Diagnostic
	}
	if v.Condition != "ready" {
		if diagnostic, e := l.store.SessionStartDiagnostic(ctx, id); e == nil && diagnostic != "" {
			v.Diagnostic = diagnostic
		}
	}
	l.mu.Lock()
	starting := l.startActive[id]
	l.mu.Unlock()
	if starting && v.Condition == "stopped" {
		v.Condition = "starting"
	}
	return v, nil
}

func (l *lifecycle) StartSession(ctx context.Context, id string) (control.SessionView, error) {
	release, err := l.lockSession(ctx, id)
	if err != nil {
		return control.SessionView{}, err
	}
	keepLock := false
	defer func() {
		if !keepLock {
			release()
		}
	}()
	if err := l.checkNoWorkspaceInspect(ctx, id); err != nil {
		return control.SessionView{}, err
	}
	s, err := startEligible(ctx, id, l.store.GetSession, l.policyCondition)
	if err != nil {
		return control.SessionView{}, err
	}
	if _, registered, e := l.store.SessionGitPrincipal(ctx, id); e != nil {
		return control.SessionView{}, e
	} else if !registered {
		return control.SessionView{}, errors.New("session Git principal missing; repair required")
	}
	if active, e := l.store.IsSessionGitPrincipalActive(ctx, id); e != nil {
		return control.SessionView{}, e
	} else if !active {
		return control.SessionView{}, errors.New("session Git principal revoked; repair required")
	}
	if _, _, err = l.sessionKey(ctx, id); err != nil {
		return control.SessionView{}, err
	}
	native, err := l.runtimeSession(ctx, s)
	if err != nil {
		return control.SessionView{}, err
	}
	if _, err = l.endpoints.Ensure(ctx, id); err != nil {
		return control.SessionView{}, err
	}
	scoped := runtimeincus.Scoped{Backend: l.runtime, Session: native}
	observed, err := plugin.RunRuntime(ctx, l.runtimePlugin, "runtime.inspect", scoped)
	if err != nil {
		return control.SessionView{}, err
	}
	if !observed.Exists {
		return control.SessionView{}, control.ErrNotFound
	}
	if observed.Status != "Stopped" && observed.Status != "Running" {
		return control.SessionView{}, control.ErrConflict
	}
	if observed.Status == "Running" {
		view, e := l.InspectSession(ctx, id)
		if e != nil {
			return view, e
		}
		if view.Condition == "ready" {
			return view, nil
		}
	}
	if err = l.store.SetSessionStartDiagnostic(ctx, id, ""); err != nil {
		return control.SessionView{}, err
	}
	l.mu.Lock()
	if l.startActive == nil {
		l.startActive = map[string]bool{}
	}
	l.startActive[id] = true
	l.mu.Unlock()
	keepLock = true
	go func() {
		defer func() {
			l.mu.Lock()
			delete(l.startActive, id)
			l.mu.Unlock()
			release()
			if l.ctx.Err() == nil {
				_, _ = l.InspectSession(l.ctx, id)
			}
		}()
		var e error
		if observed.Status == "Stopped" {
			e = l.startOwned(l.ctx, scoped)
		}
		if e == nil {
			e = l.awaitOwnedHost(l.ctx, scoped)
		}
		message := ""
		if e != nil {
			message = e.Error()
			if len(message) > 1024 {
				message = message[:1024]
			}
		}
		if writeErr := l.store.SetSessionStartDiagnostic(l.ctx, id, message); writeErr != nil && l.ctx.Err() == nil {
			log.Printf("start diagnostic: %v", writeErr)
		}
	}()
	policyCondition := l.policyCondition(ctx, s)
	v := control.NewSessionView(s)
	v.Condition = "starting"
	v.PolicyCondition = policyCondition
	l.observeView(v)
	return v, nil
}

func (l *lifecycle) StopSession(ctx context.Context, id string) (control.SessionView, error) {
	release, err := l.lockSession(ctx, id)
	if err != nil {
		return control.SessionView{}, err
	}
	defer release()
	l.mu.Lock()
	blocked := l.hasAttachmentLocked(id)
	l.mu.Unlock()
	if blocked {
		return control.SessionView{}, control.ErrConflict
	}
	if err := l.checkNoWorkspaceInspect(ctx, id); err != nil {
		return control.SessionView{}, err
	}
	s, err := l.store.GetSession(ctx, id)
	if err != nil {
		return control.SessionView{}, err
	}
	if s.Registry != "established" {
		return control.SessionView{}, control.ErrConflict
	}
	native, err := l.runtimeSession(ctx, s)
	if err != nil {
		return control.SessionView{}, err
	}
	scoped := runtimeincus.Scoped{Backend: l.runtime, Session: native}
	if _, err = plugin.RunRuntime(ctx, l.runtimePlugin, "runtime.stop", scoped); err != nil {
		return control.SessionView{}, err
	}
	return l.InspectSession(ctx, id)
}
