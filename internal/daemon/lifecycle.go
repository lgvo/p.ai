package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
	"github.com/lgvo/p.ai/internal/plugin"
	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/runtimeincus"
	"github.com/lgvo/p.ai/internal/runtimekit"
)

type lifecycle struct {
	store                  *control.Store
	cfg                    control.RuntimeConfig
	git                    *gitCapability
	runtime                *runtimeincus.Backend
	images                 runtimedriver.Images
	runtimePlugin          plugin.Active
	hostPlan               plugin.AssetPlan
	sourcePlan             plugin.AssetPlan
	agentPlan              *plugin.AssetPlan
	endpoints              *endpointManager
	instanceID             string
	ctx                    context.Context
	mu                     sync.Mutex
	working                map[string]bool
	gitOnly                bool // no runtime: only kinds marked withoutRuntime run
	origins                *control.OriginAPI
	onlyRunning            map[string]bool // queued resumptions: run only while still running
	pending                []string
	queueSlots             chan struct{}
	sessionLocks           map[string]chan struct{}
	sessionReads           map[string]*readsInFlight
	holdMu                 sync.Mutex
	holds                  map[string][]heldSession // what each operation's worker holds
	holders                map[string]string        // the operation whose worker holds each session
	waitMu                 sync.Mutex
	waiters                map[string]map[chan struct{}]bool // operation.wait calls, by operation
	confinementCheck       func(context.Context) error
	attachments            map[string]*attachment
	onAttachment           func(context.Context, string, int, bool)
	events                 *eventDelivery
	eventMu                sync.Mutex
	observed               map[string]observedFacts
	eventContexts          map[string]eventContext
	changedPolicies        map[string]bool
	commitLocks            map[string]chan struct{}
	releases               map[string]bool // projects whose image release runs; true asks for another run
	removalPreviews        map[string]removalPreviewState
	retainedDeletePreviews map[string]retainedDeletePreviewState
	createReplacePreviews  map[string]createReplacePreviewState
	createCleanupPreviews  map[string]createCleanupPreviewState
	projectDeletePreviews  map[string]projectDeletePreviewState
}

// The control-API surface dispatches each lifecycle method only to an
// implementation of its interface; a missing method would silently answer
// "unavailable" instead of failing the build.
var (
	_ control.LifecycleAPI      = (*lifecycle)(nil)
	_ control.ServiceAPI        = (*lifecycle)(nil)
	_ control.AttachmentAPI     = (*lifecycle)(nil)
	_ control.RemovalPreviewAPI = (*lifecycle)(nil)
	_ control.DiscardAPI        = (*lifecycle)(nil)
	_ control.DeleteAPI         = (*lifecycle)(nil)
	_ control.RenameAPI         = (*lifecycle)(nil)
	_ control.RetainedRenameAPI = (*lifecycle)(nil)
	_ control.ProjectDeleteAPI  = (*lifecycle)(nil)
	_ control.RetainedDeleteAPI = (*lifecycle)(nil)
	_ control.CreateReplaceAPI  = (*lifecycle)(nil)
	_ control.CreateCleanupAPI  = (*lifecycle)(nil)
)

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
	pool := ""
	if cfg.Environment != nil {
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
	l := &lifecycle{store: store, cfg: cfg, git: git, runtime: runtime, images: runtimeincus.NewDriver(runtime), runtimePlugin: *runtimeSelection, hostPlan: hostPlan, sourcePlan: sourcePlan, agentPlan: agentPlan, endpoints: endpoints, instanceID: id, ctx: ctx, working: map[string]bool{}, queueSlots: make(chan struct{}, 8), sessionLocks: map[string]chan struct{}{}, confinementCheck: runtime.CheckConfinement, changedPolicies: map[string]bool{}}
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
	store.ObserveJournal(l)
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

// currentProjectPolicy admits a new session of project only while its policy
// holds: the project exists, the host configuration still selects a policy
// for it, and the stored snapshot still matches that policy and its grants.
func (l *lifecycle) currentProjectPolicy(ctx context.Context, project string) error {
	raw, storedSHA, err := l.store.ProjectPolicyRecord(ctx, project)
	if err != nil {
		return err
	}
	selected, configured := l.cfg.PolicyForProject(project)
	if !configured {
		return policyNotConfigured(project)
	}
	changed := control.Conflict(api.ReasonPolicyInvalid, api.TargetProject(project))
	_, shapeSHA, err := control.ProjectPolicySnapshot(selected)
	if err != nil {
		return changed
	}
	rawSum := sha256.Sum256(raw)
	if hex.EncodeToString(rawSum[:]) != storedSHA {
		return changed
	}
	storedShape, err := control.StoredProjectPolicyShapeSHA(raw)
	if err != nil || storedShape != shapeSHA {
		return changed
	}
	stored, err := control.ParseStoredProjectPolicy(raw)
	if err != nil {
		return err
	}
	if err := control.RevalidateProjectPolicy(stored, l.grantBoundary()); err != nil {
		return changed
	}
	return nil
}

// policyNotConfigured answers that the host configuration selects no policy
// for project.
func policyNotConfigured(project string) error {
	return &control.Refusal{Kind: api.KindNotConfigured, Reason: api.ReasonProjectPolicy, Target: api.TargetProject(project), Cause: control.ErrNotFound}
}

func (l *lifecycle) Close() { l.endpoints.Close() }

// Recover resumes the journal's work at start: every running operation, in
// the order the journal accepted them. A blocked operation waits for Retry,
// at start as while the daemon runs.
func (l *lifecycle) Recover() error {
	after := ""
	for {
		sessions, next, e := l.store.ListSessions(l.ctx, after, 100)
		if e != nil {
			return e
		}
		for _, s := range sessions {
			if s.Registry != "removing" {
				if e = recoverSessionEndpoint(l.ctx, s, l.store.CreationForSession, l.endpoints.Ensure); e != nil {
					return e
				}
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
	if err := l.recoverJournal(); err != nil {
		return err
	}
	// Images left unused while the daemon was down go once recovery is under way.
	after = ""
	for {
		projects, next, e := l.store.ListProjects(l.ctx, after, 100)
		if e != nil {
			return e
		}
		for _, p := range projects {
			l.startRelease(p.Path)
		}
		if next == "" {
			return nil
		}
		after = next
	}
}

// recoverJournal resumes the journal's running work and schedules it every
// second from then on.
func (l *lifecycle) recoverJournal() error {
	if err := l.resumeRunning(); err != nil {
		return err
	}
	go l.schedule()
	return nil
}

// schedule resumes running work every second: an operation whose worker
// ended without recording its end, or that admission held back, runs again.
func (l *lifecycle) schedule() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-ticker.C:
			if err := l.resumeRunning(); err != nil {
				log.Printf("lifecycle reconciliation: %v", err)
			}
		}
	}
}

// resumeRunning queues every running operation in acceptance order. One that
// admission refuses now, or that cannot be read, stays recorded, and the next
// round tries it again.
func (l *lifecycle) resumeRunning() error {
	ops, err := l.store.UnfinishedOperations(l.ctx)
	if err != nil {
		return err
	}
	for _, op := range ops {
		if op.Status != "running" {
			continue
		}
		var refusal *control.Refusal
		if err := l.queue(op.ID, true); err != nil && !errors.As(err, &refusal) {
			log.Printf("resuming operation %s: %v", op.ID, err)
		}
	}
	return nil
}

// enqueue queues operation id for a worker. Once the daemon is stopping it
// queues nothing and succeeds: the operation stays recorded, and the next
// start resumes it (Recover), so the request that recorded it still returns
// it.
func (l *lifecycle) enqueue(id string) error { return l.queue(id, false) }

// queue queues operation id as enqueue does. With onlyRunning, as resumption
// asks, its worker runs only if the operation still reads running when the
// worker starts, after any earlier worker of it ended: one whose worker
// blocked it since resumption listed it waits for Retry.
func (l *lifecycle) queue(id string, onlyRunning bool) error {
	if l.ctx.Err() != nil {
		return nil
	}
	if l.store != nil {
		op, err := l.store.GetOperation(l.ctx, id)
		if err == nil && (onlyRunning && op.Status != "running" || !l.runs(op.Kind)) {
			return nil
		}
		if err == nil {
			if kind := operationKinds[op.Kind]; kind.admit != nil {
				err = kind.admit(l, l.ctx, op)
			}
		}
		if err != nil {
			if l.ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ctx.Err() != nil {
		return nil
	}
	if l.working[id] {
		if !onlyRunning {
			delete(l.onlyRunning, id) // a request's own enqueue asks for no more
		}
		return nil
	}
	// Accepted work stays pending when all workers are busy. The working map
	// deduplicates both queued and executing IDs; the durable journal supplies
	// the same intents again on restart.
	l.working[id] = true
	if onlyRunning {
		if l.onlyRunning == nil {
			l.onlyRunning = map[string]bool{}
		}
		l.onlyRunning[id] = true
	}
	l.pending = append(l.pending, id)
	l.dispatchLocked()
	return nil
}

// runs reports whether this daemon runs operations of kind: without a
// runtime, only the kinds marked withoutRuntime.
func (l *lifecycle) runs(kind string) bool {
	return !l.gitOnly || operationKinds[kind].withoutRuntime
}

// dispatchLocked starts only as many workers as the execution limit allows.
// Workers drain the FIFO as they finish, including recovery intents admitted
// while every slot was occupied.
func (l *lifecycle) dispatchLocked() {
	for len(l.pending) != 0 && l.ctx.Err() == nil {
		select {
		case l.queueSlots <- struct{}{}:
		default:
			return
		}
		id := l.pending[0]
		l.pending[0] = ""
		l.pending = l.pending[1:]
		onlyRunning := l.onlyRunning[id]
		delete(l.onlyRunning, id)
		go func() {
			defer func() {
				l.mu.Lock()
				defer l.mu.Unlock()
				delete(l.working, id)
				<-l.queueSlots
				l.dispatchLocked()
			}()
			l.process(id, onlyRunning)
		}()
	}
}

func (l *lifecycle) selection() control.CreationSelection {
	s := control.CreationSelection{RuntimeID: l.runtimePlugin.Package.Manifest.ID, RuntimeSHA256: l.runtimePlugin.Package.SHA256, HostID: l.hostPlan.PackageID, HostSHA256: l.hostPlan.PackageSHA256, SourceID: l.sourcePlan.PackageID, SourceSHA256: l.sourcePlan.PackageSHA256}
	if l.agentPlan != nil {
		s.AgentID, s.AgentSHA256 = l.agentPlan.PackageID, l.agentPlan.PackageSHA256
	}
	return s
}

func (l *lifecycle) environmentIntent() *control.EnvironmentIntent {
	if l.cfg.Environment == nil {
		return nil
	}
	publicEgress := ""
	if l.cfg.PublicEgress != nil {
		publicEgress = l.cfg.PublicEgress.SHA256()
	}
	return &control.EnvironmentIntent{
		System:              l.cfg.Environment.System,
		BaseFingerprint:     l.cfg.BaseImageFingerprint,
		BuilderStoragePool:  l.cfg.Environment.BuilderStoragePool,
		BuilderPolicySHA256: runtimeincus.BuilderPolicyDigest(publicEgress),
		ImageBuilder:        l.images.Describe().ImageBuilder,
	}
}

// withLock runs f holding l.mu and releases it even when f panics: the
// control server recovers a panicking request and keeps serving, and an l.mu
// left held would stop every later request.
func (l *lifecycle) withLock(f func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f()
}

func (l *lifecycle) sessionLockSlot(id string) chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sessionLocks == nil {
		l.sessionLocks = map[string]chan struct{}{}
	}
	slot := l.sessionLocks[id]
	if slot == nil {
		slot = make(chan struct{}, 1)
		slot <- struct{}{}
		l.sessionLocks[id] = slot
	}
	return slot
}

// lockSession admits a change to session id. A change already holding the
// session refuses it. Reads in flight do not: the change waits for them to
// end, and readSession admits no new read while the change holds the session.
func (l *lifecycle) lockSession(ctx context.Context, id string) (func(), error) {
	slot := l.sessionLockSlot(id)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-slot:
	default:
		return nil, l.heldRefusal(id)
	}
	release := func() { slot <- struct{}{} }
	l.mu.Lock()
	reads := l.sessionReads[id]
	l.mu.Unlock()
	if reads != nil {
		select {
		case <-ctx.Done():
			release()
			return nil, ctx.Err()
		case <-reads.ended:
		}
	}
	return release, nil
}

// heldRefusal refuses a request on session id, whose lock is taken: retry,
// naming the operation whose worker holds it, if one does.
func (l *lifecycle) heldRefusal(id string) error {
	l.holdMu.Lock()
	defer l.holdMu.Unlock()
	if holder := l.holders[id]; holder != "" {
		return control.Retry(api.ReasonSessionHeld, api.TargetOperation(holder))
	}
	return control.Retry(api.ReasonSessionHeld, "")
}

// admitSessionWork is the journal's admission check for work on session:
// while an unfinished operation other than except holds it, retry naming that
// operation while it runs, conflict once it is blocked and waits for Retry.
func (l *lifecycle) admitSessionWork(ctx context.Context, session, except string) error {
	if l.store == nil {
		return nil
	} // unit-only lifecycle seam; production always owns a Store
	return l.store.AdmitSessionWork(ctx, session, except)
}

// heldSession is a session an operation's worker holds, with its lock.
type heldSession struct {
	session string
	slot    chan struct{}
}

// holdSession holds session for operation opID's worker, waiting while a
// request or another worker holds it. The hold belongs to the operation: the
// journal releases it just before it records the operation's end (Ending),
// and process releases what a worker still holds when it returns without one.
func (l *lifecycle) holdSession(ctx context.Context, opID, session string) error {
	slot := l.sessionLockSlot(session)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-slot:
	}
	l.holdMu.Lock()
	defer l.holdMu.Unlock()
	if l.holds == nil {
		l.holds, l.holders = map[string][]heldSession{}, map[string]string{}
	}
	l.holds[opID] = append(l.holds[opID], heldSession{session, slot})
	l.holders[session] = opID
	return nil
}

// releaseHolds releases every session operation opID's worker holds. It takes
// only holdMu, never l.mu: the journal calls it while its transaction holds
// the store's one connection, and l.mu is held around store calls elsewhere.
func (l *lifecycle) releaseHolds(opID string) {
	l.holdMu.Lock()
	defer l.holdMu.Unlock()
	for _, held := range l.holds[opID] {
		delete(l.holders, held.session)
		held.slot <- struct{}{}
	}
	delete(l.holds, opID)
}

// Ending releases what operation id's worker holds, before the journal
// records its end.
func (l *lifecycle) Ending(id string) { l.releaseHolds(id) }

// Changed emits the progress event of op's committed status change from
// status from, and wakes the calls waiting for op.
func (l *lifecycle) Changed(op api.Operation, from string) {
	if phase := progressPhase(from, op.Status); phase != "" && op.Kind != "" {
		l.recordProgress(op, phase)
	}
	l.wake(op.ID)
}

// progressPhase is the operation.progress phase of a status change, or empty
// when the change emits none.
func progressPhase(from, to string) string {
	switch {
	case from == "":
		return "accepted"
	case from == to:
		return ""
	case to == "running":
		return "running"
	case to == "superseded":
		return "cancelled"
	default:
		return to
	}
}

// WaitOperation returns operation id as soon as it is not running, or when
// timeout passes with it still running. It reads the journal before and after
// waiting, never while: the journal's commits wake it.
func (l *lifecycle) WaitOperation(ctx context.Context, id string, timeout time.Duration) (api.Operation, error) {
	woken := make(chan struct{}, 1)
	l.waitMu.Lock()
	if l.waiters == nil {
		l.waiters = map[string]map[chan struct{}]bool{}
	}
	if l.waiters[id] == nil {
		l.waiters[id] = map[chan struct{}]bool{}
	}
	l.waiters[id][woken] = true
	l.waitMu.Unlock()
	defer func() {
		l.waitMu.Lock()
		delete(l.waiters[id], woken)
		if len(l.waiters[id]) == 0 {
			delete(l.waiters, id)
		}
		l.waitMu.Unlock()
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		op, err := l.store.GetOperation(ctx, id)
		if err != nil || op.Status != "running" {
			return op, err
		}
		select {
		case <-ctx.Done():
			return api.Operation{}, ctx.Err()
		case <-timer.C:
			return l.store.GetOperation(ctx, id)
		case <-woken:
		}
	}
}

// wake wakes the calls waiting for operation id.
func (l *lifecycle) wake(id string) {
	l.waitMu.Lock()
	defer l.waitMu.Unlock()
	for woken := range l.waiters[id] {
		select {
		case woken <- struct{}{}:
		default:
		}
	}
}

// readsInFlight counts the admitted reads of one session.
type readsInFlight struct {
	count int
	ended chan struct{} // closed when the last of them ends
}

// readSession admits a read of session id unless a change holds the session.
// Reads run alongside each other and refuse no change (see lockSession).
func (l *lifecycle) readSession(id string) (func(), error) {
	slot := l.sessionLockSlot(id)
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(slot) == 0 {
		return nil, l.heldRefusal(id)
	}
	if l.sessionReads == nil {
		l.sessionReads = map[string]*readsInFlight{}
	}
	reads := l.sessionReads[id]
	if reads == nil {
		reads = &readsInFlight{ended: make(chan struct{})}
		l.sessionReads[id] = reads
	}
	reads.count++
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		reads.count--
		if reads.count == 0 {
			close(reads.ended)
			delete(l.sessionReads, id)
		}
	}, nil
}

func (l *lifecycle) CreateProject(ctx context.Context, req control.BlankProjectRequest) (api.Operation, error) {
	_, priorErr := l.store.GetOperationByKey(ctx, req.Key)
	if errors.Is(priorErr, control.ErrConflict) {
		return api.Operation{}, priorErr
	}
	if req.URL != "" {
		if err := gitservice.ValidateOriginURL(req.URL); err != nil {
			return api.Operation{}, control.Invalid("url")
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
			// The configured policy's grants do not hold on this host.
			return api.Operation{}, &control.Refusal{Kind: api.KindConflict, Reason: api.ReasonPolicyInvalid,
				Target: api.TargetProject(req.Project), Cause: errors.Join(control.ErrConflict, snapshotErr)}
		}
	} else if errors.Is(priorErr, control.ErrNotFound) {
		return api.Operation{}, policyNotConfigured(req.Project)
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
func (l *lifecycle) CreateSession(ctx context.Context, req control.ReserveSessionRequest) (api.Operation, error) {
	if !control.ValidSessionCreateRequest(req) {
		return api.Operation{}, control.ErrInvalid
	}
	_, priorErr := l.store.GetOperationByKey(ctx, req.Key)
	if errors.Is(priorErr, control.ErrConflict) {
		return api.Operation{}, priorErr
	}
	if errors.Is(priorErr, control.ErrNotFound) {
		if err := l.currentProjectPolicy(ctx, req.Project); err != nil {
			return api.Operation{}, err
		}
	}
	if req.OriginRef != "" {
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
		var op api.Operation
		var err error
		err = l.git.backend.WithOrigin(ctx, req.Project, func(scope *gitservice.OriginScope) error {
			state, _, e := l.store.Origin(ctx, req.Project)
			if e != nil {
				return e
			}
			if state.URL == "" {
				return control.Conflict(api.ReasonOriginNotSet, api.TargetProject(req.Project))
			}
			if req.ExpectedOriginURL != "" && state.URL != req.ExpectedOriginURL {
				return control.Stale(api.ReasonChanged)
			}
			refs, e := scope.Observe(ctx, state.URL)
			writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if e != nil {
				_ = l.store.StoreOriginRefresh(writeCtx, req.Project, state.URL, nil, "origin refresh failed; check SSH identity, host key, URL and access")
				return control.Failed(api.ReasonOrigin, e)
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
				// The origin no longer has the ref, or it moved from the
				// commit the client observed.
				return control.Stale(api.ReasonChanged)
			}
			fetchFailed := false
			op, _, e = l.store.BeginSessionCreateCapturedWithCapacity(ctx, req, l.cfg.BaseImageFingerprint, l.selection(), func(ctx context.Context, r control.ReserveSessionRequest) (control.CapturedSource, error) {
				branch := api.TargetBranch(r.Project, r.Branch)
				_, exists, e := l.git.backend.InspectBranchRef(ctx, r.Project, r.Branch)
				if errors.Is(e, control.ErrConflict) {
					return control.CapturedSource{}, control.Conflict(api.ReasonBranchExists, branch)
				}
				if e != nil {
					return control.CapturedSource{}, control.Failed(api.ReasonGit, e)
				}
				if exists {
					return control.CapturedSource{}, control.Conflict(api.ReasonBranchExists, branch)
				}
				oid, e := scope.Fetch(ctx, r.OriginRef, r.ExpectedCommitOID)
				if e != nil {
					fetchFailed = true
					return control.CapturedSource{}, control.Failed(api.ReasonOrigin, e)
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
func (l *lifecycle) Retry(ctx context.Context, id string) (api.Operation, error) {
	op, err := l.store.GetOperation(ctx, id)
	if err != nil {
		return op, err
	}
	kind, known := operationKinds[op.Kind]
	if !known {
		return op, control.Conflict(api.ReasonOperationNotRetryable, api.TargetOperation(op.ID))
	}
	if !l.runs(op.Kind) {
		return op, control.NotConfigured(api.ReasonRuntime)
	}
	if op.Status == "superseded" {
		return op, control.Conflict(api.ReasonOperationSuperseded, api.TargetOperation(op.ID))
	}
	if kind.refuseRetry != nil {
		if err = kind.refuseRetry(l, ctx, op); err != nil {
			return op, err
		}
	}
	if op.Status != "completed" {
		if op.Status == "blocked" {
			if err = l.store.AdvanceOperation(ctx, op.ID, "running", op.Phase, op.Committed, op.Evidence, "", ""); err != nil {
				return op, err
			}
			op.Status = "running"
			op.Reason, op.Diagnostic = "", ""
		}
		if err = l.enqueue(op.ID); err != nil {
			return op, err
		}
	}
	return op, nil
}

// process runs the work of operation id. Resumed work (onlyRunning) runs only
// while the operation reads running.
func (l *lifecycle) process(id string, onlyRunning bool) {
	// Deferred first, so it runs last: a worker that returns without
	// recording its operation's end, or panics, lets go of what it holds.
	defer l.releaseHolds(id)
	defer l.recoverOperation(id)
	op, err := l.store.GetOperation(l.ctx, id)
	if err != nil || onlyRunning && op.Status != "running" {
		return
	}
	if kind, ok := operationKinds[op.Kind]; ok {
		kind.process(l, op)
	}
	// Once an operation of the project leaves the worker, the images no
	// session of the project uses any more go.
	l.startRelease(op.Project)
}

// recoverOperation recovers a panic in the work of operation id. It logs the
// panic and records the running operation blocked with reason internal,
// keeping its guards, so it waits for Retry instead of the scheduler running
// it again. Call it deferred.
func (l *lifecycle) recoverOperation(id string) {
	value := recover()
	if value == nil {
		return
	}
	log.Printf("operation %s work panicked: %v\n%s", id, value, debug.Stack())
	if _, err := l.store.BlockAfterPanic(l.ctx, id, fmt.Sprintf("operation work panicked: %v", value)); err != nil {
		log.Printf("operation %s stays as it was after the panic: %v", id, err)
	}
}

func (l *lifecycle) processCreation(op api.Operation) {
	ctx := l.ctx
	var err error
	if op.Status == "completed" || op.Status == "superseded" {
		return
	}
	if op.Kind == "session.create" {
		op, err = l.lockCreationOperation(ctx, op, l.store.GetOperation)
		if err != nil {
			return
		}
	}
	fail := func(err error) {
		if ctx.Err() != nil {
			return
		}
		_ = l.store.AdvanceOperation(ctx, op.ID, "blocked", op.Phase, op.Committed, op.Evidence, control.OperationReason(err), err.Error())
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
		fail(control.Failed(api.ReasonPlugin, errors.New("pinned creation plugin selection unavailable")))
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
		op, err = l.store.GetOperation(ctx, op.ID)
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
		fail(refusalBecause(api.KindConflict, api.ReasonPolicyInvalid, api.TargetSession(session.UUID), control.ErrConflict, errors.New("stored project policy is unavailable")))
		return
	}
	var image, initialOID string
	var creationEvidence control.CreationEvidence
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
		creationEvidence = ev
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
			fail(refusalBecause(api.KindPreconditionFailed, api.ReasonChanged, "", control.ErrConflict, errors.New("unborn main unexpectedly has a ref")))
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
			fail(refusalBecause(api.KindPreconditionFailed, api.ReasonChanged, "", control.ErrConflict, errors.New("assigned branch changed from captured tip")))
			return
		}
	}
	if op.Kind == "session.create" && creationEvidence.Environment != nil {
		image, err = l.ensureImage(ctx, &op, creationEvidence)
		if err != nil {
			fail(err)
			return
		}
		creationEvidence, err = control.Evidence(op)
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
				return l.store.AdvanceOperation(call, op.ID, "running", op.Phase, op.Committed, raw, "", "")
			})
		}
	}
	runtimeRunning := false
	if phaseRank(op.Phase) >= phaseRank("runtime-created") {
		state, inspectErr := runRuntime(ctx, l.runtimePlugin, "runtime.inspect", scoped)
		if inspectErr != nil {
			fail(inspectErr)
			return
		}
		runtimeRunning = state.Status == "Running"
		if runtimeRunning && phaseRank(op.Phase) < phaseRank("assembly-ready") {
			fail(refusalBecause(api.KindConflict, api.ReasonRuntimeUnsettled, api.TargetSession(session.UUID), control.ErrConflict, errors.New("creating runtime is running before verified assembly")))
			return
		}
	}
	if !runtimeRunning {
		run := func(call context.Context, kind string) (plugin.RuntimeState, error) {
			return runRuntime(call, l.runtimePlugin, kind, scoped)
		}
		if _, err = ensureStoppedRuntime(ctx, run); err != nil {
			fail(err)
			return
		}
		if err = l.advance(&op, "runtime-created", true); err != nil {
			fail(err)
			return
		}
		activation, workspace := imageSessionConfig(creationEvidence.Image, session, initialOID, selectedPolicy.Command)
		if l.agentPlan != nil {
			if len(l.agentPlan.Files) != 1 || l.agentPlan.Files[0].Role != "p-codex-adapter" {
				fail(control.Failed(api.ReasonPlugin, errors.New("selected agent asset role unavailable")))
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
		_, _ = l.InspectSession(ctx, op.SessionUUID)
	}
}

// imageSessionConfig is the session configuration a runtime kit receives for
// the image a session was created from. A commit image activates its
// devShell, if it has one, and already holds the workspace's clone; without
// one, the runtime kit clones the workspace from P at start.
func imageSessionConfig(img *control.CreationImage, session control.Session, initialOID string, command []string) (runtimekit.Config, runtimekit.WorkspaceConfig) {
	activation := runtimekit.Config{Schema: "p.runtime-session/v1", Activation: "base", Command: command}
	workspace := runtimekit.WorkspaceConfig{Schema: "p.workspace/v1", Repository: session.Project, Branch: session.Branch, InitialOID: initialOID}
	if img == nil {
		return activation, workspace
	}
	if img.Selection == string(runtimedriver.SelectionDevShell) {
		activation = runtimekit.Config{Schema: "p.runtime-session/v2", Activation: "devshell", MaterialSHA256: img.MaterialSHA256, Command: command}
	}
	workspace.Schema, workspace.EnvironmentSelection = "p.workspace/v2", img.Selection
	return activation, workspace
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
	_, err := runRuntime(ctx, l.runtimePlugin, "runtime.stop", scoped)
	return err
}
func (l *lifecycle) awaitOwnedHost(ctx context.Context, scoped runtimeincus.Scoped) error {
	return control.Failed(api.ReasonRuntime, awaitHostReady(ctx, 100*time.Second, func(call context.Context) (plugin.RuntimeState, error) {
		return runRuntime(call, l.runtimePlugin, "runtime.observe-host", scoped)
	}, func(call context.Context) error { return l.stopOwned(call, scoped) }))
}
func (l *lifecycle) startOwned(ctx context.Context, scoped runtimeincus.Scoped) error {
	_, err := runRuntime(ctx, l.runtimePlugin, "runtime.start", scoped)
	if err == nil {
		return nil
	}
	observed, e := runRuntime(ctx, l.runtimePlugin, "runtime.observe-host", scoped)
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

func (l *lifecycle) advance(op *api.Operation, phase string, committed bool) error {
	if phaseRank(op.Phase) > phaseRank(phase) {
		return nil
	}
	if err := l.store.AdvanceOperation(l.ctx, op.ID, "running", phase, committed, op.Evidence, "", ""); err != nil {
		return err
	}
	op.Phase = phase
	op.Committed = committed
	op.Status = "running"
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
	case "image-ready":
		return 3
	case "principals-ready":
		return 4
	case "runtime-created":
		return 5
	case "assembly-ready":
		return 6
	case "workspace-ready":
		return 7
	case "established":
		return 8
	default:
		return -1
	}
}

// runtimeSession is the runtime identity of session s. Its policy and its
// creation's evidence come from the store, so a failure to read them answers
// internal with reason store.
func (l *lifecycle) runtimeSession(ctx context.Context, s control.Session) (runtimeincus.Session, error) {
	policy, err := control.ParseStoredProjectPolicy(s.Policy)
	if err != nil {
		return runtimeincus.Session{}, control.Internal(api.ReasonStore, err)
	}
	grants := nativeFilesystemGrants(policy)
	op, err := l.store.CreationForSession(ctx, s.UUID)
	if err != nil {
		return runtimeincus.Session{}, control.Internal(api.ReasonStore, err)
	}
	image := ""
	if op.Kind == "project.create" {
		var ev control.BlankProjectEvidence
		if e := json.Unmarshal(op.Evidence, &ev); e != nil {
			return runtimeincus.Session{}, control.Internal(api.ReasonStore, e)
		}
		image = ev.ImageFingerprint
	} else {
		ev, e := control.Evidence(op)
		if e != nil {
			return runtimeincus.Session{}, control.Internal(api.ReasonStore, e)
		}
		image = ev.ImageFingerprint
	}
	initialOID := ""
	if op.Kind == "session.create" {
		ev, e := control.Evidence(op)
		if e != nil {
			return runtimeincus.Session{}, control.Internal(api.ReasonStore, e)
		}
		initialOID = ev.CapturedOID
	}
	publicIPv4, err := l.publicSessionIPv4(ctx, s.UUID, policy)
	if err != nil {
		return runtimeincus.Session{}, err
	}
	return runtimeincus.Session{InstanceUUID: l.instanceID, SessionUUID: s.UUID, ProjectPath: s.Project, AssignedBranch: s.Branch, InitialOID: initialOID, ContractVersion: "1", ImageFingerprint: image, EndpointSource: filepath.Join(l.cfg.EndpointPrefix, s.UUID), Grants: grants, PublicIPv4: publicIPv4}, nil
}

// publicSessionIPv4 is the public egress address of a session with policy,
// or empty for a session without network. A public-egress session needs the
// configured substrate its policy captured, and its own address slot.
func (l *lifecycle) publicSessionIPv4(ctx context.Context, sessionID string, policy control.ProjectPolicy) (string, error) {
	if policy.Network == "none" {
		return "", nil
	}
	unavailable := errors.New("public egress substrate unavailable")
	if policy.Network == "public-egress" && l.cfg.PublicEgress == nil {
		return "", refusalBecause(api.KindNotConfigured, api.ReasonPublicEgress, "", control.ErrNotFound, unavailable)
	}
	if policy.Network != "public-egress" || policy.PublicEgressSHA256 != l.cfg.PublicEgress.SHA256() {
		return "", refusalBecause(api.KindConflict, api.ReasonPolicyInvalid, api.TargetSession(sessionID), control.ErrConflict, unavailable)
	}
	slot, err := l.store.PublicAddressSlot(ctx, sessionID)
	if errors.Is(err, control.ErrNotFound) {
		return "", refusalBecause(api.KindConflict, api.ReasonPublicEgressUnassigned, api.TargetSession(sessionID), control.ErrConflict,
			errors.New("public egress address slot missing"))
	}
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

// startEligible is session id when it may run: established, with a policy
// that still holds.
func startEligible(ctx context.Context, id string, session func(context.Context, string) (control.Session, error), policy func(context.Context, control.Session) string) (control.Session, error) {
	s, err := session(ctx, id)
	if err != nil {
		return control.Session{}, err
	}
	if s.Registry != "established" {
		return control.Session{}, control.Conflict(api.ReasonSessionNotEstablished, api.TargetSession(id))
	}
	if policy(ctx, s) == "invalid" {
		return control.Session{}, control.Conflict(api.ReasonPolicyInvalid, api.TargetSession(id))
	}
	return s, nil
}

// registryCondition queries durable removal authority before taking eventMu.
// A registry marker alone cannot say whether the assigned branch is retained.
func (l *lifecycle) registryCondition(ctx context.Context, s control.Session) (string, error) {
	if s.Registry == "creating" {
		return "creating", nil
	}
	if s.Registry != "removing" {
		return "", nil
	}
	if l.store == nil {
		return "", control.ErrNotFound
	}
	kind, err := l.store.SessionRemovalKind(ctx, s.UUID)
	if err != nil {
		return "", err
	}
	if removal := operationKinds[kind].removal; removal != "" {
		return removal, nil
	}
	return "", control.ErrConflict
}

func (l *lifecycle) InspectSession(ctx context.Context, id string) (result api.SessionView, resultErr error) {
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
		return api.SessionView{}, err
	}
	v := control.NewSessionView(s)
	created, err := l.store.CreationForSession(ctx, s.UUID)
	if err != nil {
		return api.SessionView{}, err
	}
	v.Image = control.CreationImageView(created)
	v.PolicyCondition = l.policyCondition(ctx, s)
	if s.Registry == "creating" {
		v.Condition = "creating"
		if op, e := l.store.CreationForSession(ctx, id); e == nil {
			v.Diagnostic = op.Diagnostic
		}
		return v, nil
	}
	if s.Registry == "removing" {
		v.Condition, err = l.registryCondition(ctx, s)
		if err != nil {
			v.Condition = "unreachable"
			v.Diagnostic = "committed session removal intent unavailable"
		}
		return v, nil
	}
	// While a start or stop is unfinished the journal says what the session
	// is doing, whatever the runtime shows: a client that acts on ready or
	// stopped must not find that operation holding the session.
	change, unfinished, err := l.unfinishedSessionChange(ctx, id)
	if err != nil || unfinished != "" {
		v.Condition = unfinished
		return v, err
	}
	v.Condition = "unreachable"
	if native, e := l.runtimeSession(ctx, s); e == nil {
		state, e := runRuntime(ctx, l.runtimePlugin, "runtime.observe-host", runtimeincus.Scoped{Backend: l.runtime, Session: native})
		if e == nil {
			v.Condition = observedCondition(state)
			if state.DiagnosticAvailable {
				v.Diagnostic = state.Diagnostic
			}
		}
	}
	// A start or stop recorded while the runtime was observed may already
	// have changed it: the journal still says what the session is doing.
	if change, unfinished, err = l.unfinishedSessionChange(ctx, id); err != nil || unfinished != "" {
		v.Condition, v.Diagnostic = unfinished, ""
		return v, err
	}
	// A failed start explains a session that is not ready.
	if v.Condition != "ready" && change.Kind == control.KindSessionStart && change.Status == "failed" && change.Diagnostic != "" {
		v.Diagnostic = change.Diagnostic
	}
	return v, nil
}

// unfinishedSessionChange is session id's latest start or stop, and the
// condition it gives the session while it is unfinished: starting or
// stopping; empty once it ended or when there is none.
func (l *lifecycle) unfinishedSessionChange(ctx context.Context, id string) (api.Operation, string, error) {
	change, found, err := l.store.LatestSessionChange(ctx, id)
	if err != nil || !found || change.Status != "running" && change.Status != "blocked" {
		return change, "", err
	}
	if change.Kind == control.KindSessionStop {
		return change, "stopping", nil
	}
	return change, "starting", nil
}

// observedCondition is the condition of an established session, with no
// unfinished start or stop, whose runtime was observed as state.
func observedCondition(state plugin.RuntimeState) string {
	switch {
	case !state.Exists:
		return "missing"
	case state.Status == "Stopped":
		return "stopped"
	case state.HostReady:
		return "ready"
	default:
		return "starting"
	}
}

// StartSession starts session id as an operation of kind session.start,
// whose worker starts the runtime and waits for the session host. A session
// already ready records nothing, and a start sent while another is
// unfinished returns that one.
func (l *lifecycle) StartSession(ctx context.Context, id string) (api.SessionChangeResult, error) {
	return l.changeSession(ctx, control.KindSessionStart, id, l.admitStart)
}

// StopSession stops session id as an operation of kind session.stop. A
// session already stopped records nothing, and a stop sent while another is
// unfinished returns that one.
func (l *lifecycle) StopSession(ctx context.Context, id string) (api.SessionChangeResult, error) {
	return l.changeSession(ctx, control.KindSessionStop, id, l.admitStop)
}

// changeSession records a start or stop, kind, of session id once admit
// accepts it. admit answers the session's view instead when the session is
// already as asked, and nothing is recorded.
func (l *lifecycle) changeSession(ctx context.Context, kind, id string,
	admit func(context.Context, string) (*api.SessionView, error)) (api.SessionChangeResult, error) {
	// A worker of the same kind holds the session: name its operation before
	// taking the lock it holds.
	if result, same, err := l.unfinishedChange(ctx, kind, id); err != nil || same {
		return result, err
	}
	release, err := l.lockSession(ctx, id)
	if err != nil {
		return api.SessionChangeResult{}, err
	}
	defer release()
	if result, same, err := l.unfinishedChange(ctx, kind, id); err != nil || same {
		return result, err
	}
	if err = l.admitSessionWork(ctx, id, ""); err != nil {
		return api.SessionChangeResult{}, err
	}
	view, err := admit(ctx, id)
	if err != nil {
		return api.SessionChangeResult{}, err
	}
	if view != nil {
		return api.SessionChangeResult{Session: view}, nil
	}
	op, recorded, err := l.store.BeginSessionChange(ctx, kind, id)
	if err != nil {
		return api.SessionChangeResult{}, err
	}
	if !recorded {
		return api.SessionChangeResult{Operation: &op}, nil
	}
	if _, err = l.InspectSession(ctx, id); err != nil {
		log.Printf("observing session %s after its %s: %v", id, kind, err)
	}
	return api.SessionChangeResult{Changed: true, Operation: &op}, l.enqueue(op.ID)
}

// unfinishedChange answers a start or stop, kind, of session id while one of
// the same kind is unfinished: that operation, unchanged.
func (l *lifecycle) unfinishedChange(ctx context.Context, kind, id string) (api.SessionChangeResult, bool, error) {
	change, found, err := l.store.LatestSessionChange(ctx, id)
	if err != nil || !found || change.Kind != kind || change.Status != "running" && change.Status != "blocked" {
		return api.SessionChangeResult{}, false, err
	}
	return api.SessionChangeResult{Operation: &change}, true, nil
}

// admitStart checks what a start needs before it is recorded: an
// established session whose policy holds, its Git principal registered and
// active, its key and endpoints, and its runtime. A ready session needs no
// start: its view comes back instead.
func (l *lifecycle) admitStart(ctx context.Context, id string) (*api.SessionView, error) {
	s, err := startEligible(ctx, id, l.store.GetSession, l.policyCondition)
	if err != nil {
		return nil, err
	}
	principalMissing := func(cause error) error {
		return refusalBecause(api.KindConflict, api.ReasonPrincipalMissing, api.TargetSession(id), control.ErrConflict, cause)
	}
	if _, registered, e := l.store.SessionGitPrincipal(ctx, id); e != nil {
		return nil, e
	} else if !registered {
		return nil, principalMissing(errors.New("session Git principal missing"))
	}
	if active, e := l.store.IsSessionGitPrincipalActive(ctx, id); e != nil {
		return nil, e
	} else if !active {
		return nil, principalMissing(errors.New("session Git principal revoked"))
	}
	if _, _, err = l.sessionKey(ctx, id); errors.Is(err, errSessionKeyMismatch) {
		return nil, principalMissing(err)
	} else if err != nil {
		return nil, err
	}
	native, err := l.runtimeSession(ctx, s)
	if err != nil {
		return nil, err
	}
	if _, err = l.endpoints.Ensure(ctx, id); err != nil {
		return nil, err
	}
	observed, err := runRuntime(ctx, l.runtimePlugin, "runtime.inspect", runtimeincus.Scoped{Backend: l.runtime, Session: native})
	if err != nil {
		return nil, control.Failed(api.ReasonRuntime, err)
	}
	if !observed.Exists {
		return nil, control.Conflict(api.ReasonRuntimeMissing, api.TargetSession(id))
	}
	if observed.Status != "Stopped" && observed.Status != "Running" {
		return nil, control.Failed(api.ReasonRuntime, fmt.Errorf("runtime status %q is neither Running nor Stopped", observed.Status))
	}
	if observed.Status == "Running" {
		view, e := l.InspectSession(ctx, id)
		if e != nil {
			return nil, e
		}
		if view.Condition == "ready" {
			return &view, nil
		}
	}
	return nil, nil
}

// admitStop checks what a stop needs before it is recorded: an established
// session with no pending or confirmed attachment, and its runtime. A stopped
// session needs no stop: its view comes back instead.
func (l *lifecycle) admitStop(ctx context.Context, id string) (*api.SessionView, error) {
	if l.hasAttachment(id) {
		return nil, control.Conflict(api.ReasonAttachmentActive, api.TargetSession(id))
	}
	s, err := l.store.GetSession(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.Registry != "established" {
		return nil, control.Conflict(api.ReasonSessionNotEstablished, api.TargetSession(id))
	}
	native, err := l.runtimeSession(ctx, s)
	if err != nil {
		return nil, err
	}
	observed, err := runRuntime(ctx, l.runtimePlugin, "runtime.inspect", runtimeincus.Scoped{Backend: l.runtime, Session: native})
	if err != nil {
		return nil, control.Failed(api.ReasonRuntime, err)
	}
	if !observed.Exists {
		return nil, control.Conflict(api.ReasonRuntimeMissing, api.TargetSession(id))
	}
	if observed.Status == "Stopped" {
		view, e := l.InspectSession(ctx, id)
		return &view, e
	}
	return nil, nil
}

// processSessionChange runs a start or stop operation. Its worker holds the
// session; the start completes once the session host is ready, the stop once
// the runtime is stopped. Either fails with the runtime's diagnostic.
func (l *lifecycle) processSessionChange(observed api.Operation) {
	ctx := l.ctx
	if l.holdSession(ctx, observed.ID, observed.SessionUUID) != nil {
		return
	}
	op, err := l.store.GetOperation(ctx, observed.ID)
	if err != nil || op.Status != "running" {
		return
	}
	done := "stopped"
	if op.Kind == control.KindSessionStart {
		done = "ready"
	}
	err = l.runSessionChange(ctx, op)
	if ctx.Err() != nil {
		return // the next start resumes the running operation
	}
	if err != nil {
		err = l.store.AdvanceOperation(ctx, op.ID, "failed", op.Phase, false, nil, control.OperationReason(err), err.Error())
	} else {
		err = l.store.AdvanceOperation(ctx, op.ID, "completed", done, false, nil, "", "")
	}
	if err != nil {
		log.Printf("recording the end of %s %s: %v", op.Kind, op.ID, err)
		return
	}
	_, _ = l.InspectSession(ctx, op.SessionUUID)
}

// runSessionChange starts or stops op's session runtime.
func (l *lifecycle) runSessionChange(ctx context.Context, op api.Operation) error {
	s, err := l.store.GetSession(ctx, op.SessionUUID)
	if err != nil {
		return err
	}
	native, err := l.runtimeSession(ctx, s)
	if err != nil {
		return err
	}
	scoped := runtimeincus.Scoped{Backend: l.runtime, Session: native}
	if op.Kind == control.KindSessionStop {
		_, err = runRuntime(ctx, l.runtimePlugin, "runtime.stop", scoped)
		return control.Failed(api.ReasonRuntime, err)
	}
	observed, err := runRuntime(ctx, l.runtimePlugin, "runtime.inspect", scoped)
	if err != nil {
		return control.Failed(api.ReasonRuntime, err)
	}
	if !observed.Exists {
		return control.Conflict(api.ReasonRuntimeMissing, api.TargetSession(s.UUID))
	}
	if observed.Status == "Stopped" {
		if err = l.startOwned(ctx, scoped); err != nil {
			return control.Failed(api.ReasonRuntime, err)
		}
	}
	return l.awaitOwnedHost(ctx, scoped)
}
