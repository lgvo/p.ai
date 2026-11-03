package runtimeincus

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

var _ runtimedriver.Driver = (*Driver)(nil)

// publicEgressAddresses is how many guest addresses P reserves on the public
// egress bridge: the /24's hosts 10 to 13, as validatePublicAddress accepts.
const publicEgressAddresses = 4

// hostReadyWait bounds Start's wait for the session host, and hostStopWait
// the stop of a runtime whose host failed.
const (
	hostReadyWait = 100 * time.Second
	hostStopWait  = 45 * time.Second
)

// kindError is an error in the backend's own words that is also one of the
// driver's error kinds, such as runtimedriver.ErrMismatch.
type kindError struct{ kind, err error }

func (e kindError) Error() string   { return e.err.Error() }
func (e kindError) Unwrap() []error { return []error{e.kind, e.err} }

func withKind(kind error, text string) error { return kindError{kind, errors.New(text)} }

// mismatch is an object that differs from the spec it was checked against.
func mismatch(text string) error { return withKind(runtimedriver.ErrMismatch, text) }

// incarnationOf is a runtime's incarnation: Incus's instance UUID with its
// generation, which a snapshot restore changes. It is empty when Incus has
// not set them.
func incarnationOf(o Observation) runtimedriver.Incarnation {
	return incarnation(o.IncusUUID, o.Generation)
}

func incarnation(incusUUID, generation string) runtimedriver.Incarnation {
	if incusUUID == "" || generation == "" {
		return ""
	}
	return runtimedriver.Incarnation(incusUUID + "/" + generation)
}

// volatileIdentity is an instance's Incus UUID and generation, both empty
// unless both are UUIDs that its config and expanded config agree on.
func volatileIdentity(in instanceJSON) (incusUUID, generation string) {
	incusUUID, generation = in.Config["volatile.uuid"], in.Config["volatile.uuid.generation"]
	if !uuidPattern.MatchString(incusUUID) || in.ExpandedConfig["volatile.uuid"] != incusUUID ||
		!uuidPattern.MatchString(generation) || in.ExpandedConfig["volatile.uuid.generation"] != generation {
		return "", ""
	}
	return incusUUID, generation
}

// stateOf is the driver's state of an instance's Incus status: a starting,
// stopping, frozen or failed instance is present, in another state.
func stateOf(status string) runtimedriver.State {
	switch status {
	case "Running":
		return runtimedriver.StateRunning
	case "Stopped":
		return runtimedriver.StateStopped
	}
	return runtimedriver.StateOther
}

// Check proves the Incus project's confinement and, when configured, the
// public egress substrate.
func (d *Driver) Check(ctx context.Context) error { return d.backend.CheckConfinement(ctx) }

// NativeSession reads a session spec into the backend's session. Its labels
// must be exactly its project, whose repository its workspace clones. The
// daemon uses it for the backend calls that later migration steps move into
// the driver.
func (b *Backend) NativeSession(s runtimedriver.Session) (Session, error) {
	if s.Ref.Kind != runtimedriver.KindSession || !uuidPattern.MatchString(s.Ref.ID) {
		return Session{}, errors.New("invalid session reference")
	}
	project, ok := s.Labels[runtimedriver.LabelProject]
	if !ok || len(s.Labels) != 1 {
		return Session{}, errors.New("session labels must be exactly its project")
	}
	if s.Workspace.Repository != project {
		return Session{}, errors.New("session workspace is not its project's repository")
	}
	if s.Endpoints == "" {
		return Session{}, errors.New("session endpoint directory required")
	}
	if !uuidPattern.MatchString(b.config.PInstanceID) {
		return Session{}, errors.New("trusted P instance identity required for sessions")
	}
	grants := make([]FilesystemGrant, 0, len(s.Grants))
	for _, g := range s.Grants {
		grants = append(grants, FilesystemGrant{Name: g.Name, Source: g.Source, Type: g.Type, Access: g.Access, Executable: g.Executable,
			Device: g.Device, Inode: g.Inode, OwnerUID: g.OwnerUID, OwnerGID: g.OwnerGID})
	}
	native := Session{InstanceUUID: b.config.PInstanceID, SessionUUID: s.Ref.ID, ProjectPath: project, AssignedBranch: s.Workspace.Branch,
		InitialOID: s.Workspace.Commit, ContractVersion: "1", ImageFingerprint: string(s.Image), EndpointSource: s.Endpoints,
		Grants: grants, PublicIPv4: s.Network.PublicIPv4}
	if err := validateSession(b.config, native); err != nil {
		return Session{}, err
	}
	return native, nil
}

// Inspect reads the session's runtime, tolerating only the devices an
// interrupted Create has not added yet, and its session host.
func (d *Driver) Inspect(ctx context.Context, s runtimedriver.Session) (runtimedriver.Observation, error) {
	native, err := d.backend.NativeSession(s)
	if err != nil {
		return runtimedriver.Observation{}, err
	}
	o, err := d.backend.InspectCreated(ctx, native)
	if err != nil {
		return runtimedriver.Observation{}, err
	}
	return d.backend.observe(ctx, native, o), nil
}

// observe turns an observation of the session's runtime into the driver's,
// with the session host of a running runtime. A running runtime whose host
// cannot be read has an unknown host rather than an error. A stopped
// runtime's journal is read only by HostLog: Incus's file API can stall on a
// stopped instance, as after an external rename, and a stalled read holds
// the start that follows it.
func (b *Backend) observe(ctx context.Context, s Session, o Observation) runtimedriver.Observation {
	out := observeRuntime(s, o)
	if out.State != runtimedriver.StateRunning {
		return out
	}
	host, err := b.observeHostOf(ctx, s, o)
	if err != nil {
		out.Host.Unknown = true
		return out
	}
	out.Host = runtimedriver.HostState{Ready: host.Ready, Diagnostic: host.Diagnostic}
	return out
}

// observeRuntime is the driver's observation of the session's runtime without
// its session host. Create and Stop return it: reading a stopped runtime's
// journal through the file API right after they changed the runtime gives
// the caller nothing it needs.
func observeRuntime(s Session, o Observation) runtimedriver.Observation {
	if !o.Exists {
		return runtimedriver.Observation{State: runtimedriver.StateAbsent}
	}
	return runtimedriver.Observation{State: stateOf(o.Status), Incarnation: incarnationOf(o), Complete: devicesComplete(s, o)}
}

// Create with no incarnation refuses a runtime that already exists, proves
// the whole project free of the session's identity, calls g, initializes the
// runtime with its devices and installs the guest file plan. With an
// incarnation it completes that stopped runtime instead and never
// initializes. Either way the runtime is left stopped.
func (d *Driver) Create(ctx context.Context, s runtimedriver.Session, at runtimedriver.Incarnation, g runtimedriver.Gate) (runtimedriver.Observation, error) {
	var zero runtimedriver.Observation
	b := d.backend
	native, err := b.NativeSession(s)
	if err != nil {
		return zero, err
	}
	plan, err := b.sessionGuestPlan(ctx, s)
	if err != nil {
		return zero, err
	}
	if at != "" {
		o, err := b.completeRuntime(ctx, native, plan, at)
		if err != nil {
			return zero, err
		}
		return observeRuntime(native, o), nil
	}
	if g == nil {
		return zero, errors.New("runtime create gate required")
	}
	gated := false
	o, err := b.createRuntime(ctx, native, plan, func() error {
		if err := g(); err != nil {
			return err
		}
		gated = true
		return nil
	})
	if err != nil {
		if gated {
			// Incus may still admit the init, or a runtime may exist
			// without its devices or files: core settles it.
			return zero, errors.Join(err, runtimedriver.ErrUnknownOutcome)
		}
		return zero, err
	}
	return observeRuntime(native, o), nil
}

// createRuntime initializes the session's runtime and installs its guest
// file plan. Anything at the session's address is never adopted.
func (b *Backend) createRuntime(ctx context.Context, s Session, plan guestPlan, gate func() error) (Observation, error) {
	if err := b.effectChecks(ctx, s); err != nil {
		return Observation{}, err
	}
	before, err := b.InspectCreated(ctx, s)
	if err != nil {
		return Observation{}, err
	}
	if before.Exists {
		return Observation{}, mismatch("the session's runtime already exists")
	}
	o, err := b.initSession(ctx, s, gate)
	if err != nil {
		return Observation{}, err
	}
	// Every device and guest file goes to the runtime this init made.
	at := incarnationOf(o)
	if at == "" {
		return Observation{}, errors.New("Incus set no incarnation on the new runtime")
	}
	if _, err := b.ensureSessionDevices(ctx, s, o, at); err != nil {
		return Observation{}, err
	}
	return b.installGuestPlan(ctx, s, plan, at)
}

// completeRuntime adds the devices and guest files an interrupted Create left
// out of the stopped runtime at incarnation at, keeping those it installed.
func (b *Backend) completeRuntime(ctx context.Context, s Session, plan guestPlan, at runtimedriver.Incarnation) (Observation, error) {
	if err := b.effectChecks(ctx, s); err != nil {
		return Observation{}, err
	}
	o, err := b.InspectCreated(ctx, s)
	if err != nil {
		return Observation{}, err
	}
	if !o.Exists {
		return Observation{}, errors.New("runtime instance missing")
	}
	// Both steps refuse another incarnation before any effect, and neither
	// touches a running runtime.
	if !devicesComplete(s, o) {
		if _, err := b.ensureSessionDevices(ctx, s, o, at); err != nil {
			return Observation{}, err
		}
	}
	return b.installGuestPlan(ctx, s, plan, at)
}

// sameIncarnation refuses an observation of another runtime than the
// incarnation at; an empty at accepts any.
func sameIncarnation(o Observation, at runtimedriver.Incarnation) error {
	if at != "" && incarnationOf(o) != at {
		return mismatch("the runtime is another incarnation")
	}
	return nil
}

// stillBound refuses, between two device additions bound to incarnation at,
// a runtime that is another incarnation or no longer stopped: no further
// device goes to it.
func stillBound(o Observation, at runtimedriver.Incarnation) error {
	if err := sameIncarnation(o, at); err != nil {
		return err
	}
	if at != "" && !strings.EqualFold(o.Status, "Stopped") {
		return errors.New("the runtime started while its devices were added")
	}
	return nil
}

// Start starts the stopped runtime and waits until its session host is
// ready; for a running runtime it only waits. It checks the endpoint
// directory, the grant sources and the egress substrate first, even when the
// runtime runs. When the host does not become ready, the runtime is stopped
// and Start returns the host's diagnostic in a *runtimedriver.HostError.
func (d *Driver) Start(ctx context.Context, s runtimedriver.Session) (runtimedriver.Observation, error) {
	var zero runtimedriver.Observation
	b := d.backend
	native, err := b.NativeSession(s)
	if err != nil {
		return zero, err
	}
	if err := b.effectChecks(ctx, native); err != nil {
		return zero, err
	}
	o, err := b.Inspect(ctx, native)
	if err != nil {
		return zero, err
	}
	switch {
	case !o.Exists:
		return zero, errors.New("runtime instance missing")
	case o.Status == "Stopped":
		if err := d.startRuntime(ctx, native); err != nil {
			return zero, err
		}
	case o.Status != "Running":
		return zero, fmt.Errorf("runtime is %s, neither running nor stopped", o.Status)
	}
	host, err := awaitHostReady(ctx, hostReadyWait, func(call context.Context) (HostObservation, error) {
		return b.ObserveHost(call, native)
	}, func(call context.Context) error {
		return b.stopFailedHost(call, native)
	})
	if err != nil {
		if ctx.Err() != nil {
			return zero, errors.Join(err, ctx.Err())
		}
		return zero, hostError(err)
	}
	return runtimedriver.Observation{State: runtimedriver.StateRunning, Incarnation: incarnationOf(host.Observation), Complete: true,
		Host: runtimedriver.HostState{Ready: true}}, nil
}

// startRuntime starts the stopped runtime. When Incus fails to start it, the
// session host's own account, if it left one, says why, and a runtime left
// running is stopped.
func (d *Driver) startRuntime(ctx context.Context, s Session) error {
	b := d.backend
	_, err := b.checkedTransition(ctx, s, "start")
	if err == nil {
		return nil
	}
	observed, e := b.ObserveHost(ctx, s)
	if e == nil && observed.DiagnosticAvailable && observed.Diagnostic != "" {
		err = errors.New(observed.Diagnostic)
	}
	if e != nil || observed.Exists && observed.Status == "Running" {
		stopCtx, cancel := context.WithTimeout(ctx, hostStopWait)
		defer cancel()
		if stopErr := b.stopFailedHost(stopCtx, s); stopErr != nil {
			err = fmt.Errorf("%v; stopping failed: %w", err, stopErr)
		}
	}
	return hostError(err)
}

// stopFailedHost stops a runtime whose session host failed. A failed host
// shuts its runtime down by itself, so Incus may refuse or lose a stop sent
// meanwhile; the runtime counts as stopped once it is seen stopped before
// ctx ends, and Start never returns a host failure while it still runs. A
// stop not seen to take effect, because ctx ended first or the runtime went
// missing, keeps its error.
func (b *Backend) stopFailedHost(ctx context.Context, s Session) error {
	_, err := b.Stop(ctx, s)
	if err == nil {
		return nil
	}
	for {
		o, e := b.Inspect(ctx, s)
		if e == nil && !o.Exists {
			return err
		}
		if e == nil && o.Status == "Stopped" {
			return nil
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return err
		case <-timer.C:
		}
	}
}

// maxHostDiagnostic bounds the diagnostic of a host that did not become
// ready: the host's 2,048-byte account and what stopping the runtime said.
const maxHostDiagnostic = 4096

func hostError(err error) *runtimedriver.HostError {
	text := strings.ToValidUTF8(err.Error(), "�")
	if len(text) > maxHostDiagnostic {
		cut := maxHostDiagnostic
		for !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut]
	}
	return &runtimedriver.HostError{Diagnostic: text}
}

// awaitHostReady polls the session host every 250 ms until it is ready, at
// most for timeout. When the host fails, or cannot be read until the
// timeout, a runtime left running is stopped within 45 s, keeping its files
// for an ordinary retry. The error says why the host is not ready, in the
// host's own words when it has some.
func awaitHostReady(parent context.Context, timeout time.Duration, observe func(context.Context) (HostObservation, error), stop func(context.Context) error) (HostObservation, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	var reason error
	mustStop := false
	for {
		state, err := observe(ctx)
		if err != nil {
			reason = err
			mustStop = true
		} else {
			if state.Ready {
				return state, nil
			}
			if state.DiagnosticAvailable && state.Diagnostic != "" {
				reason = errors.New(state.Diagnostic)
			}
			if !state.Exists {
				if reason == nil {
					reason = errors.New("runtime missing during host startup")
				}
				break
			}
			if state.Status == "Stopped" {
				if reason == nil {
					reason = errors.New("interactive host stopped before ready")
				}
				break
			}
			if state.Status != "Running" {
				if reason == nil {
					reason = errors.New("runtime status unavailable during host startup")
				}
				mustStop = true
				break
			}
			if strings.HasPrefix(state.Unit, "failed/") {
				if reason == nil {
					reason = errors.New("interactive host failed")
				}
				mustStop = true
				break
			}
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			if reason == nil {
				reason = errors.New("interactive host readiness timed out")
			} else {
				reason = fmt.Errorf("interactive host readiness timed out: %w", reason)
			}
			mustStop = parent.Err() == nil
			return HostObservation{}, stopAfterHostFailure(parent, reason, mustStop, stop)
		case <-timer.C:
		}
	}
	return HostObservation{}, stopAfterHostFailure(parent, reason, mustStop, stop)
}

func stopAfterHostFailure(parent context.Context, reason error, mustStop bool, stop func(context.Context) error) error {
	if mustStop && parent.Err() == nil {
		stopCtx, cancel := context.WithTimeout(parent, hostStopWait)
		defer cancel()
		if err := stop(stopCtx); err != nil {
			reason = fmt.Errorf("%v; stopping failed: %w", reason, err)
		}
	}
	return reason
}

// Stop stops the runtime gracefully, and by force once Incus's graceful
// timeout has passed.
func (d *Driver) Stop(ctx context.Context, s runtimedriver.Session) (runtimedriver.Observation, error) {
	native, err := d.backend.NativeSession(s)
	if err != nil {
		return runtimedriver.Observation{}, err
	}
	o, err := d.backend.Stop(ctx, native)
	if err != nil {
		return runtimedriver.Observation{}, err
	}
	return observeRuntime(native, o), nil
}

// HostLog reads the session host's journal: through systemd in a running
// runtime, and for a stopped one its persistent journal through the file
// API, briefly and at most once (see observeHostOf).
func (d *Driver) HostLog(ctx context.Context, t runtimedriver.Target) (string, error) {
	native, err := d.backend.NativeSession(t.Session)
	if err != nil {
		return "", err
	}
	o, err := d.backend.InspectCreated(ctx, native)
	if err != nil || !o.Exists {
		return "", err
	}
	if err := sameIncarnation(o, t.At); err != nil {
		return "", err
	}
	host, err := d.backend.observeHostOf(ctx, native, o)
	if err != nil {
		return "", err
	}
	return host.Diagnostic, nil
}

// OpenTerminal comes in migration step M5.
func (d *Driver) OpenTerminal(context.Context, runtimedriver.Target, runtimedriver.Size) (runtimedriver.Terminal, error) {
	return nil, runtimedriver.ErrUnsupported
}

// The workspace operations come in migration step M4.
func (d *Driver) ReadWorkspace(context.Context, runtimedriver.Target, string, int64) (runtimedriver.File, error) {
	return runtimedriver.File{}, runtimedriver.ErrUnsupported
}

func (d *Driver) WriteWorkspace(context.Context, runtimedriver.Target, string, runtimedriver.File, runtimedriver.Gate) error {
	return runtimedriver.ErrUnsupported
}

func (d *Driver) RemoveWorkspace(context.Context, runtimedriver.Target, string, runtimedriver.Gate) error {
	return runtimedriver.ErrUnsupported
}
