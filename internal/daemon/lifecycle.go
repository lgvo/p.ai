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
	"slices"
	"sync"
	"time"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
	"github.com/lgvo/p.ai/internal/plugin"
	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/runtimedriver/supervise"
	"github.com/lgvo/p.ai/internal/sourcedriver"
)

type lifecycle struct {
	store                  *control.Store
	cfg                    control.RuntimeConfig
	git                    *gitCapability
	driver                 runtimedriver.Driver
	supervisor             *supervise.Supervisor // runs the driver executable; Close stops it
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
	confinementCheck       func(context.Context) error       // the driver's Check before a policy decision
	attachments            map[string]*attachment
	terminalSettleInterval time.Duration // 0 is defaultTerminalSettleInterval
	onAttachment           func(context.Context, string, int, bool)
	events                 *eventDelivery
	eventMu                sync.Mutex
	observed               map[string]observedFacts
	eventContexts          map[string]eventContext
	changedPolicies        map[string]bool
	commitLocks            map[string]chan struct{}
	releases               map[string]bool // projects whose image release runs; true asks for another run
}

// The control-API surface dispatches each lifecycle method only to an
// implementation of its interface; a missing method would silently answer
// "unavailable" instead of failing the build.
var (
	_ control.LifecycleAPI      = (*lifecycle)(nil)
	_ control.ServiceAPI        = (*lifecycle)(nil)
	_ control.AttachmentAPI     = (*lifecycle)(nil)
	_ control.RemovalAPI        = (*lifecycle)(nil)
	_ control.RenameAPI         = (*lifecycle)(nil)
	_ control.RetainedRenameAPI = (*lifecycle)(nil)
	_ control.ProjectDeleteAPI  = (*lifecycle)(nil)
	_ control.RetainedDeleteAPI = (*lifecycle)(nil)
	_ control.CreateReplaceAPI  = (*lifecycle)(nil)
	_ control.CreateCleanupAPI  = (*lifecycle)(nil)
)

// newLifecycle starts the runtime driver and supervises it until Close; a
// first start that fails refuses the daemon's start.
func newLifecycle(ctx context.Context, cfg control.RuntimeConfig, store *control.Store, git *gitCapability) (_ *lifecycle, err error) {
	if err := privateTrustedFile(cfg.ActivationPath); err != nil {
		return nil, fmt.Errorf("runtime activation: %w", err)
	}
	active, err := plugin.LoadActivation(cfg.ActivationPath)
	if err != nil {
		return nil, err
	}
	var hostSelection *plugin.Active
	for i := range active {
		if active[i].Package.Manifest.ID == cfg.HostPluginID {
			hostSelection = &active[i]
		}
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
	driver, err := supervise.Start(ctx, driverLaunch(cfg, id))
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			driver.Close()
		}
	}()
	if err = driver.Check(ctx); err != nil {
		return nil, err
	}
	endpoints, err := newEndpointManager(cfg.EndpointPrefix, git.info.Endpoint, store)
	if err != nil {
		return nil, err
	}
	l := &lifecycle{store: store, cfg: cfg, git: git, driver: driver, supervisor: driver, hostPlan: hostPlan, sourcePlan: sourcePlan, agentPlan: agentPlan, endpoints: endpoints, instanceID: id, ctx: ctx, working: map[string]bool{}, queueSlots: make(chan struct{}, 8), sessionLocks: map[string]chan struct{}{}, confinementCheck: driver.Check, changedPolicies: map[string]bool{}}
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
	return control.GrantBoundary{Ceilings: l.cfg.DiskSourceCeilings, StateDir: state, EndpointPrefix: l.cfg.EndpointPrefix}
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

// Close stops the session endpoints and the runtime driver.
func (l *lifecycle) Close() {
	l.endpoints.Close()
	l.supervisor.Close()
}

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
	s := control.CreationSelection{HostID: l.hostPlan.PackageID, HostSHA256: l.hostPlan.PackageSHA256, SourceID: l.sourcePlan.PackageID, SourceSHA256: l.sourcePlan.PackageSHA256}
	if l.agentPlan != nil {
		s.AgentID, s.AgentSHA256 = l.agentPlan.PackageID, l.agentPlan.PackageSHA256
	}
	return s
}

func (l *lifecycle) environmentIntent() *control.EnvironmentIntent {
	if l.cfg.Environment == nil {
		return nil
	}
	// Core pins the driver's builder policy by its digest; it never reads it.
	caps := l.driver.Describe()
	policy := sha256.Sum256([]byte(caps.BuilderPolicy))
	return &control.EnvironmentIntent{
		System:              l.cfg.Environment.System,
		BaseFingerprint:     l.cfg.BaseImageFingerprint,
		BuilderStoragePool:  l.cfg.Environment.BuilderStoragePool,
		BuilderPolicySHA256: hex.EncodeToString(policy[:]),
		ImageBuilder:        caps.ImageBuilder,
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

// CreateProject records creation req and queues it. A recorded request is
// answered from SQLite alone; a new one with an origin has the source driver
// check its URL first.
func (l *lifecycle) CreateProject(ctx context.Context, req control.BlankProjectRequest) (api.Operation, error) {
	_, priorErr := l.store.GetOperationByKey(ctx, req.Key)
	if errors.Is(priorErr, control.ErrConflict) {
		return api.Operation{}, priorErr
	}
	if priorErr != nil && req.URL != "" {
		if _, err := control.CheckNames(ctx, l.git.driver, control.NameField{Field: "url", Name: sourcedriver.Name{Kind: sourcedriver.NameOriginURL, Value: req.URL}}); err != nil {
			return api.Operation{}, err
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

// CreateSession records creation req and queues it. A recorded request is
// answered from SQLite alone; a new one has the source driver check its Git
// names first, then captures its source under the store's Git authority.
func (l *lifecycle) CreateSession(ctx context.Context, req control.ReserveSessionRequest) (api.Operation, error) {
	if !control.ValidSessionCreateRequest(req) {
		return api.Operation{}, control.ErrInvalid
	}
	_, priorErr := l.store.GetOperationByKey(ctx, req.Key)
	if errors.Is(priorErr, control.ErrConflict) {
		return api.Operation{}, priorErr
	}
	if priorErr == nil {
		// Its source was captured when it was recorded.
		return l.beginSessionCreate(ctx, req, false, func(context.Context, control.ReserveSessionRequest) (control.CapturedSource, error) {
			return control.CapturedSource{}, control.ErrConflict
		})
	}
	newRequest := errors.Is(priorErr, control.ErrNotFound)
	if newRequest {
		if err := l.currentProjectPolicy(ctx, req.Project); err != nil {
			return api.Operation{}, err
		}
	}
	source, err := l.checkCreationNames(ctx, req)
	if err != nil {
		return api.Operation{}, err
	}
	if req.OriginRef == "" {
		return l.beginSessionCreate(ctx, req, newRequest, func(ctx context.Context, r control.ReserveSessionRequest) (control.CapturedSource, error) {
			return l.captureLocalSource(ctx, r, source)
		})
	}
	op, err := l.createFromOrigin(ctx, req)
	if err != nil {
		return op, err
	}
	return l.queueSessionCreate(op, req, newRequest)
}

// createFromOrigin records creation req, from an origin ref at the commit
// the client observed, under the project's origin lock: it observes the
// origin afresh, records the observation, and fetches the ref's objects as
// it captures the source. A ref the origin no longer advertises at that
// commit answers precondition_failed; an origin that cannot be observed or
// fetched from answers failed with reason origin.
func (l *lifecycle) createFromOrigin(ctx context.Context, req control.ReserveSessionRequest) (api.Operation, error) {
	release, err := l.store.LockOrigin(ctx, req.Project)
	if err != nil {
		return api.Operation{}, err
	}
	defer release()
	state, _, err := l.store.Origin(ctx, req.Project)
	if err != nil {
		return api.Operation{}, err
	}
	if state.URL == "" {
		return api.Operation{}, control.Conflict(api.ReasonOriginNotSet, api.TargetProject(req.Project))
	}
	if req.ExpectedOriginURL != "" && state.URL != req.ExpectedOriginURL {
		return api.Operation{}, control.Stale(api.ReasonChanged)
	}
	refs, err := l.observeOrigin(ctx, state.URL)
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err != nil {
		_ = l.store.StoreOriginRefresh(writeCtx, req.Project, state.URL, nil, "origin refresh failed; check SSH identity, host key, URL and access")
		return api.Operation{}, control.Failed(api.ReasonOrigin, err)
	}
	if err = l.store.StoreOriginRefresh(writeCtx, req.Project, state.URL, refs, ""); err != nil {
		return api.Operation{}, err
	}
	var selected *sourcedriver.OriginRef
	for i := range refs {
		if refs[i].Ref == req.OriginRef {
			selected = &refs[i]
			break
		}
	}
	if selected == nil || string(selected.Target) != req.ExpectedCommitOID {
		// The origin no longer has the ref, or it moved from the commit the
		// client observed.
		return api.Operation{}, control.Stale(api.ReasonChanged)
	}
	fetchFailed := false
	op, _, err := l.store.BeginSessionCreateCapturedWithCapacity(ctx, req, l.cfg.BaseImageFingerprint, l.selection(), func(ctx context.Context, r control.ReserveSessionRequest) (control.CapturedSource, error) {
		branch := api.TargetBranch(r.Project, r.Branch)
		_, exists, e := l.inspectBranch(ctx, r.Project, r.Branch)
		if errors.Is(e, control.ErrConflict) {
			return control.CapturedSource{}, control.Conflict(api.ReasonBranchExists, branch)
		}
		if e != nil {
			return control.CapturedSource{}, control.Failed(api.ReasonGit, e)
		}
		if exists {
			return control.CapturedSource{}, control.Conflict(api.ReasonBranchExists, branch)
		}
		var oid sourcedriver.OID
		e = l.store.CallOrigin(ctx, func(ctx context.Context) (err error) {
			oid, err = l.git.driver.FetchOrigin(ctx, r.Project, state.URL, *selected)
			return err
		})
		if e != nil {
			fetchFailed = true
			return control.CapturedSource{}, control.Failed(api.ReasonOrigin, e)
		}
		return control.CapturedSource{OID: string(oid), OriginURL: state.URL, OriginRef: r.OriginRef}, nil
	}, l.observeSessionCapacity, l.environmentIntent())
	if fetchFailed {
		failedCtx, cancelFailed := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		_ = l.store.StoreOriginRefresh(failedCtx, req.Project, state.URL, nil, "origin fetch failed; check source ref and access")
		cancelFailed()
	}
	return op, err
}

// beginSessionCreate records creation req, with capture reading its source,
// and queues it. newRequest says that no operation had req's key before.
func (l *lifecycle) beginSessionCreate(ctx context.Context, req control.ReserveSessionRequest, newRequest bool, capture func(context.Context, control.ReserveSessionRequest) (control.CapturedSource, error)) (api.Operation, error) {
	op, _, err := l.store.BeginSessionCreateCapturedWithCapacity(ctx, req, l.cfg.BaseImageFingerprint, l.selection(), capture, l.observeSessionCapacity, l.environmentIntent())
	if err != nil {
		return op, err
	}
	return l.queueSessionCreate(op, req, newRequest)
}

// queueSessionCreate queues creation op of req unless it ended. A new
// request's creation is also one the daemon reports.
func (l *lifecycle) queueSessionCreate(op api.Operation, req control.ReserveSessionRequest, newRequest bool) (api.Operation, error) {
	if newRequest {
		l.recordCreation(op, req.Branch)
	}
	if op.Status != "completed" && op.Status != "superseded" {
		if err := l.enqueue(op.ID); err != nil {
			return op, err
		}
	}
	return op, nil
}

// observeOrigin has the source driver observe the origin at url, within the
// bounds of a call to an origin (control.Store.CallOrigin).
func (l *lifecycle) observeOrigin(ctx context.Context, url string) (refs []sourcedriver.OriginRef, err error) {
	err = l.store.CallOrigin(ctx, func(ctx context.Context) error {
		refs, err = l.git.driver.ObserveOrigin(ctx, url)
		return err
	})
	return refs, err
}

// createOriginProject creates project creation op's repository once the
// origin at url answers, and records the project with that observation,
// under the project's origin lock. bootstrap says that the origin is empty
// and created reserves the session that pushes its first main.
func (l *lifecycle) createOriginProject(ctx context.Context, op api.Operation, url string) (created control.Session, bootstrap bool, err error) {
	release, err := l.store.LockOrigin(ctx, op.Project)
	if err != nil {
		return control.Session{}, false, err
	}
	defer release()
	refs, err := l.observeOrigin(ctx, url)
	if err != nil {
		return control.Session{}, false, control.Failed(api.ReasonOrigin, err)
	}
	if _, err = l.git.driver.CreateRepository(ctx, op.Project); err != nil {
		return control.Session{}, false, control.Failed(api.ReasonGit, err)
	}
	return l.store.CommitOriginProject(ctx, op.ID, refs)
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
			if created, bootstrap, err = l.createOriginProject(ctx, op, req.URL); err != nil {
				fail(err)
				return
			}
			if !bootstrap {
				return
			}
			l.recordCreatingSession(created)
		} else {
			if _, err = l.git.driver.CreateRepository(ctx, op.Project); err != nil {
				fail(control.Failed(api.ReasonGit, err))
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
	var initialOID string
	var creationEvidence control.CreationEvidence
	if op.Kind == "session.create" {
		ev, e := control.Evidence(op)
		if e != nil {
			fail(e)
			return
		}
		initialOID = ev.CapturedOID
		creationEvidence = ev
		if op.Phase == "source-ready" {
			var req control.ReserveSessionRequest
			if json.Unmarshal(op.Request, &req) != nil {
				fail(errors.New("invalid creation request"))
				return
			}
			if e = l.ensureBranchAssigned(ctx, &op, req, initialOID); e != nil {
				fail(e)
				return
			}
			if e = l.advance(&op, "branch-assigned", true); e != nil {
				fail(e)
				return
			}
			// Assigning a new branch records its creation as sent: later
			// steps record evidence from op's, never from the earlier copy.
			if creationEvidence, e = control.Evidence(op); e != nil {
				fail(e)
				return
			}
		}
	}
	if op.Kind == "project.create" {
		tip, exists, e := l.inspectBranch(ctx, session.Project, "main")
		if e != nil {
			fail(control.Failed(api.ReasonGit, e))
			return
		}
		if phaseRank(op.Phase) < phaseRank("workspace-ready") && (exists || tip != "") {
			fail(refusalBecause(api.KindPreconditionFailed, api.ReasonChanged, "", control.ErrConflict, errors.New("unborn main unexpectedly has a ref")))
			return
		}
	}
	if op.Kind == "session.create" {
		tip, exists, e := l.inspectBranch(ctx, session.Project, session.Branch)
		if e != nil {
			fail(control.Failed(api.ReasonGit, e))
			return
		}
		if phaseRank(op.Phase) < phaseRank("workspace-ready") && (!exists || tip != initialOID) {
			fail(refusalBecause(api.KindPreconditionFailed, api.ReasonChanged, "", control.ErrConflict, errors.New("assigned branch changed from captured tip")))
			return
		}
	}
	if op.Kind == "session.create" && creationEvidence.Environment != nil {
		if _, err = l.ensureImage(ctx, &op, creationEvidence); err != nil {
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
	if _, err = l.endpoints.Ensure(ctx, session.UUID); err != nil {
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
	spec, err := l.sessionSpec(ctx, session)
	if err != nil {
		fail(err)
		return
	}
	spec.GitKey = key
	if err = l.createRuntime(ctx, &op, spec); err != nil {
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

// createRuntime brings the runtime of a creation at principals-ready or
// later to a ready session host. An absent runtime is created through the
// creation's init gate; a stopped one an interrupted creation left is
// completed at its incarnation and never initialized again. A runtime
// running before its assembly was recorded blocks the creation. Start returns
// once the session host is ready, or fails with the host's diagnostic.
func (l *lifecycle) createRuntime(ctx context.Context, op *api.Operation, spec runtimedriver.Session) error {
	observed, err := l.driver.Inspect(ctx, spec)
	if err != nil {
		return control.Failed(api.ReasonRuntime, err)
	}
	switch {
	case observed.State == runtimedriver.StateRunning:
		// The daemon restarted after starting it: Start only waits.
		if phaseRank(op.Phase) < phaseRank("assembly-ready") {
			return refusalBecause(api.KindConflict, api.ReasonRuntimeUnsettled, api.TargetSession(spec.Ref.ID), control.ErrConflict, errors.New("creating runtime is running before verified assembly"))
		}
		_, err = l.driver.Start(ctx, spec)
		return control.Failed(api.ReasonRuntime, err)
	case observed.State == runtimedriver.StateAbsent:
		err = l.initRuntime(ctx, op, spec)
	case observed.Incarnation == "":
		err = errors.New("creating runtime has no incarnation to complete")
	default:
		_, err = l.driver.Create(ctx, spec, observed.Incarnation, nil)
	}
	if err != nil {
		return control.Failed(api.ReasonRuntime, err)
	}
	// Create installs the runtime and its guest files together.
	if err = l.advance(op, "runtime-created", true); err != nil {
		return err
	}
	if err = l.advance(op, "assembly-ready", true); err != nil {
		return err
	}
	_, err = l.driver.Start(ctx, spec)
	return control.Failed(api.ReasonRuntime, err)
}

// initRuntime creates the absent runtime of a creation through its init
// gate. An init whose outcome is unknown, this one's or an earlier
// attempt's, is settled and never sent again: a runtime the driver settles
// as initialized is completed at its incarnation, and any other answer
// keeps the creation blocked.
func (l *lifecycle) initRuntime(ctx context.Context, op *api.Operation, spec runtimedriver.Session) error {
	gate := l.creationInitGate(ctx, op)
	sent, attempted := false, false
	_, err := l.driver.Create(ctx, spec, "", tracked(func() error {
		err := gate()
		attempted = errors.Is(err, errCreationInitUnknown)
		return err
	}, &sent))
	if err == nil || !sent && !attempted {
		return err
	}
	settled, settleErr := l.driver.Settle(ctx, runtimedriver.Effect{Kind: runtimedriver.EffectCreate, Target: runtimedriver.Target{Session: spec}})
	if settleErr != nil || settled != runtimedriver.Applied {
		return errors.Join(err, settleErr)
	}
	observed, err := l.driver.Inspect(ctx, spec)
	if err != nil || observed.Incarnation == "" {
		return errors.Join(err, errors.New("settled creation runtime has no incarnation to complete"))
	}
	_, err = l.driver.Create(ctx, spec, observed.Incarnation, nil)
	return err
}

// creationInitGate is the gate of a creation's runtime init. A session
// creation records its init attempt there, so that a runtime absent after an
// attempted init never authorizes another; a blank project's creation
// records none.
func (l *lifecycle) creationInitGate(ctx context.Context, op *api.Operation) runtimedriver.Gate {
	if op.Kind != "session.create" {
		return func() error { return nil }
	}
	return func() error {
		return recordCreationInitAttempt(ctx, op, func(call context.Context, raw json.RawMessage) error {
			return l.store.AdvanceOperation(call, op.ID, "running", op.Phase, op.Committed, raw, "", "")
		})
	}
}

// sessionGrants are the filesystem grants of a stored policy, with the
// source identity each had when captured.
func sessionGrants(policy control.ProjectPolicy) []runtimedriver.Grant {
	grants := make([]runtimedriver.Grant, 0, len(policy.FilesystemMounts))
	for _, g := range policy.FilesystemMounts {
		id := g.SourceIdentity
		grants = append(grants, runtimedriver.Grant{Name: g.Name, Source: g.Source, Type: g.Type, Access: g.Access, Executable: g.Executable,
			Device: id.Device, Inode: id.Inode, OwnerUID: id.OwnerUID, OwnerGID: id.OwnerGID})
	}
	return grants
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

// sessionSpec is the runtime driver's spec of session s: its project, the
// image and commit its creation captured, the command of its stored policy,
// P's Git host key, its endpoint directory, grants, network and the selected
// session assets. It holds no Git key: only creating the runtime installs
// one, and the creation adds it. Its policy and its creation's evidence come
// from the store, so a failure to read them answers internal with reason
// store.
func (l *lifecycle) sessionSpec(ctx context.Context, s control.Session) (runtimedriver.Session, error) {
	policy, err := control.ParseStoredProjectPolicy(s.Policy)
	if err != nil {
		return runtimedriver.Session{}, control.Internal(api.ReasonStore, err)
	}
	created, err := l.store.CreationForSession(ctx, s.UUID)
	if err != nil {
		return runtimedriver.Session{}, control.Internal(api.ReasonStore, err)
	}
	var image, commit string
	if created.Kind == "project.create" {
		var ev control.BlankProjectEvidence
		if e := json.Unmarshal(created.Evidence, &ev); e != nil {
			return runtimedriver.Session{}, control.Internal(api.ReasonStore, e)
		}
		image = ev.ImageFingerprint
	} else {
		ev, e := control.Evidence(created)
		if e != nil {
			return runtimedriver.Session{}, control.Internal(api.ReasonStore, e)
		}
		image, commit = ev.ImageFingerprint, ev.CapturedOID
	}
	publicIPv4, err := l.publicSessionIPv4(ctx, s.UUID, policy)
	if err != nil {
		return runtimedriver.Session{}, err
	}
	spec := l.runtimeSpec(s.UUID, s.Project, image, sessionGrants(policy), publicIPv4)
	spec.Command = policy.Command
	spec.Workspace.Branch, spec.Workspace.Commit = s.Branch, commit
	spec.GitHostKey = l.git.info.HostPublicKey
	spec.Assets = l.sessionAssets()
	return spec, nil
}

// runtimeSpec is the part of a session's spec that names its runtime and
// that Inspect compares: its reference, project, image, endpoint directory,
// grants and network. A proof about a runtime that may not exist needs no
// more.
func (l *lifecycle) runtimeSpec(uuid, project, image string, grants []runtimedriver.Grant, publicIPv4 string) runtimedriver.Session {
	return runtimedriver.Session{
		Ref:       runtimedriver.Ref{Kind: runtimedriver.KindSession, ID: uuid},
		Labels:    runtimedriver.Labels{runtimedriver.LabelProject: project},
		Image:     runtimedriver.ImageRef(image),
		Workspace: runtimedriver.Workspace{Repository: project},
		Endpoints: filepath.Join(l.cfg.EndpointPrefix, uuid),
		Grants:    grants,
		Network:   runtimedriver.Network{PublicIPv4: publicIPv4},
	}
}

// sessionAssets are the files of the selected interactive host, source and
// agent adapter packages, each named by its role.
func (l *lifecycle) sessionAssets() []runtimedriver.Asset {
	files := slices.Concat(l.hostPlan.Files, l.sourcePlan.Files)
	if l.agentPlan != nil {
		files = append(files, l.agentPlan.Files...)
	}
	assets := make([]runtimedriver.Asset, 0, len(files))
	for _, f := range files {
		assets = append(assets, runtimedriver.Asset{Role: f.Role, Mode: f.Mode, Data: f.Data, SHA256: f.SHA256})
	}
	return assets
}

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
	// A runtime missing a device, or whose host cannot be read, is as
	// unreachable as one the driver cannot inspect.
	v.Condition = "unreachable"
	if spec, e := l.sessionSpec(ctx, s); e == nil {
		observed, e := l.driver.Inspect(ctx, spec)
		if e == nil && !observed.Host.Unknown && (observed.State == runtimedriver.StateAbsent || observed.Complete) {
			v.Condition, v.Diagnostic = observedCondition(observed), observed.Host.Diagnostic
			if observed.State == runtimedriver.StateStopped {
				// The host's last words say why a session stopped on its own.
				if words, e := l.driver.HostLog(ctx, runtimedriver.Target{Session: spec, At: observed.Incarnation}); e == nil {
					v.Diagnostic = words
				}
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
// unfinished start or stop, whose runtime was observed.
func observedCondition(observed runtimedriver.Observation) string {
	switch {
	case observed.State == runtimedriver.StateAbsent:
		return "missing"
	case observed.State == runtimedriver.StateStopped:
		return "stopped"
	case observed.Host.Ready:
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
	spec, err := l.sessionSpec(ctx, s)
	if err != nil {
		return nil, err
	}
	if _, err = l.endpoints.Ensure(ctx, id); err != nil {
		return nil, err
	}
	observed, err := l.driver.Inspect(ctx, spec)
	if err != nil {
		return nil, control.Failed(api.ReasonRuntime, err)
	}
	if err = startableRuntime(id, observed); err != nil {
		return nil, err
	}
	if observed.State == runtimedriver.StateRunning {
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

// startableRuntime refuses a start of session id whose runtime was observed
// missing, neither running nor stopped, or incomplete.
func startableRuntime(id string, observed runtimedriver.Observation) error {
	switch {
	case observed.State == runtimedriver.StateAbsent:
		return control.Conflict(api.ReasonRuntimeMissing, api.TargetSession(id))
	case observed.State != runtimedriver.StateStopped && observed.State != runtimedriver.StateRunning:
		return control.Failed(api.ReasonRuntime, fmt.Errorf("runtime is %s, neither running nor stopped", observed.State))
	case !observed.Complete:
		return control.Failed(api.ReasonRuntime, errors.New("session runtime is incomplete"))
	}
	return nil
}

// admitStop checks what a stop needs before it is recorded: an established
// session whose attachments, if any, all ended unproven, and its complete
// runtime. Stopping the runtime ends whatever such a terminal's process left,
// and so its lease. A stopped session needs no stop: its view comes back
// instead, and such leases end.
func (l *lifecycle) admitStop(ctx context.Context, id string) (*api.SessionView, error) {
	if l.hasLiveAttachment(id) {
		return nil, control.Conflict(api.ReasonAttachmentActive, api.TargetSession(id))
	}
	s, err := l.store.GetSession(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.Registry != "established" {
		return nil, control.Conflict(api.ReasonSessionNotEstablished, api.TargetSession(id))
	}
	spec, err := l.sessionSpec(ctx, s)
	if err != nil {
		return nil, err
	}
	observed, err := l.driver.Inspect(ctx, spec)
	if err != nil {
		return nil, control.Failed(api.ReasonRuntime, err)
	}
	switch {
	case observed.State == runtimedriver.StateAbsent:
		return nil, control.Conflict(api.ReasonRuntimeMissing, api.TargetSession(id))
	case !observed.Complete:
		return nil, control.Failed(api.ReasonRuntime, errors.New("session runtime is incomplete"))
	case observed.State == runtimedriver.StateStopped:
		l.endUnprovenLeases(id)
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

// runSessionChange starts or stops op's session runtime. The driver's Start
// starts a stopped runtime and waits until its session host is ready; on a
// running one it only waits.
func (l *lifecycle) runSessionChange(ctx context.Context, op api.Operation) error {
	s, err := l.store.GetSession(ctx, op.SessionUUID)
	if err != nil {
		return err
	}
	spec, err := l.sessionSpec(ctx, s)
	if err != nil {
		return err
	}
	if op.Kind == control.KindSessionStop {
		stopped, err := l.driver.Stop(ctx, spec)
		if err == nil && stopped.State == runtimedriver.StateStopped {
			l.endUnprovenLeases(s.UUID)
		}
		return control.Failed(api.ReasonRuntime, err)
	}
	observed, err := l.driver.Inspect(ctx, spec)
	if err != nil {
		return control.Failed(api.ReasonRuntime, err)
	}
	if err = startableRuntime(s.UUID, observed); err != nil {
		return err
	}
	_, err = l.driver.Start(ctx, spec)
	return control.Failed(api.ReasonRuntime, err)
}
