package memory

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// Driver is an in-memory runtime driver over a World.
type Driver struct {
	w    *World
	dead chan struct{}
}

var _ runtimedriver.Driver = (*Driver)(nil)

// NewDriver returns a driver over w. Several may share a World, as a
// restarted driver shares its backend with the one that died.
func NewDriver(w *World) *Driver { return &Driver{w: w, dead: make(chan struct{})} }

// Kill makes the driver die, as its process would: a call stopped inside
// Create returns ErrUnknownOutcome having effected nothing more, every later
// call fails with ErrUnavailable, and the channels of its terminals close,
// ending them unproven. Their guest processes keep running.
func (d *Driver) Kill() {
	w := d.w
	w.mu.Lock()
	defer w.mu.Unlock()
	select {
	case <-d.dead:
		return
	default:
	}
	close(d.dead)
	for _, a := range w.attachments {
		if a.driver == d {
			a.dropChannel()
		}
	}
}

var errDied = fmt.Errorf("the driver died during the call: %w", runtimedriver.ErrUnknownOutcome)

// alive refuses every call to a driver that was killed.
func (d *Driver) alive() error {
	select {
	case <-d.dead:
		return fmt.Errorf("the driver is not running: %w", runtimedriver.ErrUnavailable)
	default:
		return nil
	}
}

func (d *Driver) Describe() runtimedriver.Capabilities {
	c := d.w.config.Capabilities
	c.Networks = slices.Clone(c.Networks)
	return c
}

// Check has nothing to prove: the World's boundary is the World itself.
func (d *Driver) Check(context.Context) error { return d.alive() }

func (d *Driver) Inventory(context.Context) (runtimedriver.Inventory, error) {
	if err := d.alive(); err != nil {
		return runtimedriver.Inventory{}, err
	}
	w := d.w
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.partial {
		w.partial = false
		return runtimedriver.Inventory{}, errors.New("the backend's listing of the scope was incomplete")
	}
	inv := runtimedriver.Inventory{Capacity: w.config.Capacity, Objects: []runtimedriver.Object{}}
	for _, o := range w.objects {
		obj := runtimedriver.Object{Name: o.name, Labels: maps.Clone(o.labels), Incarnation: o.incarnation, State: o.observedState()}
		if o.instance == thisInstance {
			obj.Ref = o.ref
		}
		inv.Objects = append(inv.Objects, obj)
	}
	slices.SortFunc(inv.Objects, func(a, b runtimedriver.Object) int { return strings.Compare(a.Name, b.Name) })
	return inv, nil
}

func (o *object) observedState() runtimedriver.State {
	if o.terminating {
		return runtimedriver.StateOther
	}
	return o.state
}

func (d *Driver) Inspect(_ context.Context, s runtimedriver.Session) (runtimedriver.Observation, error) {
	if err := d.alive(); err != nil {
		return runtimedriver.Observation{}, err
	}
	if err := d.w.checkSession(s); err != nil {
		return runtimedriver.Observation{}, err
	}
	w := d.w
	w.mu.Lock()
	defer w.mu.Unlock()
	o, err := w.observe(s)
	if err != nil || o == nil {
		return runtimedriver.Observation{State: runtimedriver.StateAbsent}, err
	}
	return o.observation(s), nil
}

// HostLog is the session host's last account of itself: why it is not
// ready, or why it ended.
func (d *Driver) HostLog(_ context.Context, t runtimedriver.Target) (string, error) {
	if err := d.alive(); err != nil {
		return "", err
	}
	if err := d.w.checkSession(t.Session); err != nil {
		return "", err
	}
	w := d.w
	w.mu.Lock()
	defer w.mu.Unlock()
	o, err := w.observe(t.Session)
	if err != nil || o == nil {
		return "", err
	}
	if t.At != "" && o.incarnation != t.At {
		return "", fmt.Errorf("%w: another incarnation", runtimedriver.ErrMismatch)
	}
	return o.host.diagnostic, nil
}

// observe reads the object at the session's address: nil when the address
// finds nothing, and ErrMismatch when what is there is not s's runtime as
// specified. Devices an interrupted Create had not installed yet are not a
// mismatch. Called with w.mu held.
func (w *World) observe(s runtimedriver.Session) (*object, error) {
	o := w.objects[address(s.Ref)]
	if o == nil {
		return nil, nil
	}
	mismatch := func(what string) (*object, error) {
		return nil, fmt.Errorf("%w: %s at %s", runtimedriver.ErrMismatch, what, o.name)
	}
	switch {
	case o.instance != thisInstance || o.ref != s.Ref:
		return mismatch("another object")
	case !maps.Equal(o.labels, s.Labels):
		return mismatch("other labels")
	case o.image != s.Image:
		return mismatch("another image")
	case o.devices.differ(s):
		return mismatch("other devices")
	}
	return o, nil
}

func (o *object) observation(s runtimedriver.Session) runtimedriver.Observation {
	obs := runtimedriver.Observation{State: o.observedState(), Incarnation: o.incarnation, Complete: o.complete(s)}
	if o.state == runtimedriver.StateRunning {
		obs.Host = runtimedriver.HostState{Ready: o.host.ready, Diagnostic: o.host.diagnostic}
	}
	return obs
}

func (o *object) complete(s runtimedriver.Session) bool {
	return o.assembled && o.devices.complete(s)
}

// differ reports whether a device present differs from the spec, or one is
// present that the spec does not name.
func (d devices) differ(s runtimedriver.Session) bool {
	if d.endpoints != "" && d.endpoints != s.Endpoints || d.address != "" && d.address != s.Network.PublicIPv4 {
		return true
	}
	for name, g := range d.grants {
		i := slices.IndexFunc(s.Grants, func(want runtimedriver.Grant) bool { return want.Name == name })
		if i < 0 || s.Grants[i] != g {
			return true
		}
	}
	return false
}

func (d devices) complete(s runtimedriver.Session) bool {
	return !d.differ(s) && d.endpoints == s.Endpoints && d.address == s.Network.PublicIPv4 && len(d.grants) == len(s.Grants)
}

// Create initializes and installs a session's runtime, or with an
// incarnation completes the one an interrupted Create left.
func (d *Driver) Create(_ context.Context, s runtimedriver.Session, at runtimedriver.Incarnation, g runtimedriver.Gate) (runtimedriver.Observation, error) {
	var zero runtimedriver.Observation
	if err := d.alive(); err != nil {
		return zero, err
	}
	if err := d.w.checkSession(s); err != nil {
		return zero, err
	}
	if len(s.GitKey) == 0 {
		return zero, errors.New("the session's Git key is required to create its runtime")
	}
	if at != "" {
		return d.complete(s, at)
	}
	if g == nil {
		return zero, errors.New("create gate required")
	}
	if err := d.w.checkFree(s); err != nil {
		return zero, err
	}
	if err := g(); err != nil {
		return zero, err
	}
	if err := d.initialize(s); err != nil {
		return zero, err
	}
	if dd := d.armedDeath(AfterInit); dd != nil {
		return zero, d.die(dd)
	}
	return d.install(s)
}

// checkFree refuses a session whose reference something in scope already
// carries, at its address or under another name, that the scope cannot
// hold, or whose image or address is not available.
func (w *World) checkFree(s runtimedriver.Session) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.objects[address(s.Ref)] != nil || len(w.carrying(s.Ref)) > 0 {
		return fmt.Errorf("%w: the session's reference is already in scope", runtimedriver.ErrMismatch)
	}
	if w.full() {
		return errors.New("the scope holds no more runtimes")
	}
	if im := w.images[s.Image]; im == nil || im.instance == otherInstance {
		return fmt.Errorf("image %q is not in scope", s.Image)
	}
	if s.Network.PublicIPv4 != "" {
		for _, o := range w.objects {
			if o.devices.address == s.Network.PublicIPv4 {
				return fmt.Errorf("public-egress address %s is held by %s", s.Network.PublicIPv4, o.name)
			}
		}
	}
	return nil
}

// initialize makes the runtime at the session's address, stopped, with
// nothing installed. Admitted late, it returns ErrUnknownOutcome.
func (d *Driver) initialize(s runtimedriver.Session) error {
	w := d.w
	w.mu.Lock()
	defer w.mu.Unlock()
	name := address(s.Ref)
	landed := w.admit(runtimedriver.EffectCreate, effectKey(runtimedriver.EffectCreate, s.Ref.ID), name, func() {
		if w.objects[name] != nil || len(w.carrying(s.Ref)) > 0 {
			return // the backend refuses a name or reference in use
		}
		w.initializations[s.Ref]++
		w.objects[name] = &object{
			name: name, instance: thisInstance, ref: s.Ref, labels: maps.Clone(s.Labels), image: s.Image,
			incarnation: runtimedriver.Incarnation(fmt.Sprintf("incarnation-%d", w.next())), state: runtimedriver.StateStopped,
			devices: devices{grants: map[string]runtimedriver.Grant{}}, files: map[string]*node{},
		}
	})
	if !landed {
		return fmt.Errorf("the initialization was not answered: %w", runtimedriver.ErrUnknownOutcome)
	}
	if o := w.objects[name]; o == nil || o.ref != s.Ref {
		return errors.New("the backend refused the initialization")
	}
	return nil
}

// complete installs what an interrupted Create left out of the stopped
// runtime at incarnation at, comparing every guest file of the spec even
// when nothing is missing. It never initializes.
func (d *Driver) complete(s runtimedriver.Session, at runtimedriver.Incarnation) (runtimedriver.Observation, error) {
	w := d.w
	w.mu.Lock()
	_, err := w.completable(s, at)
	w.mu.Unlock()
	if err != nil {
		return runtimedriver.Observation{}, err
	}
	return d.install(s)
}

// completable is the stopped runtime of s at incarnation at. Called with
// w.mu held.
func (w *World) completable(s runtimedriver.Session, at runtimedriver.Incarnation) (*object, error) {
	o, err := w.observe(s)
	switch {
	case err != nil:
		return nil, err
	case o == nil:
		return nil, errors.New("no runtime to complete at the session's address")
	case o.incarnation != at:
		return nil, fmt.Errorf("the runtime is at incarnation %s, not %s", o.incarnation, at)
	case o.terminating || o.state != runtimedriver.StateStopped:
		return nil, errors.New("only a stopped runtime is completed")
	}
	return o, nil
}

func (d *Driver) Start(_ context.Context, s runtimedriver.Session) (runtimedriver.Observation, error) {
	var zero runtimedriver.Observation
	if err := d.alive(); err != nil {
		return zero, err
	}
	if err := d.w.checkSession(s); err != nil {
		return zero, err
	}
	w := d.w
	w.mu.Lock()
	defer w.mu.Unlock()
	o, err := w.startable(s)
	if err != nil {
		return zero, err
	}
	switch {
	case o.state == runtimedriver.StateRunning && o.host.ready:
		return o.observation(s), nil
	case o.state == runtimedriver.StateRunning:
		// The host of a running runtime is not ready, and nothing in memory
		// will make it so: the wait for it ends at once.
		diagnostic := o.host.diagnostic
		w.stopRuntime(o)
		o.host.diagnostic = diagnostic
		return o.observation(s), &runtimedriver.HostError{Diagnostic: diagnostic}
	}
	o.transitions++
	if o.host.failNext {
		// The runtime started, its host failed, and the runtime stopped.
		o.transitions++
		o.host = host{diagnostic: o.host.failWith}
		return o.observation(s), &runtimedriver.HostError{Diagnostic: o.host.diagnostic}
	}
	o.state = runtimedriver.StateRunning
	o.host = host{ready: true}
	return o.observation(s), nil
}

// startable is the complete runtime of s, ready to start. Called with w.mu
// held.
func (w *World) startable(s runtimedriver.Session) (*object, error) {
	o, err := w.observe(s)
	switch {
	case err != nil:
		return nil, err
	case o == nil:
		return nil, errors.New("the session has no runtime")
	case o.terminating:
		return nil, errors.New("the runtime is being deleted")
	case !o.complete(s):
		return nil, errors.New("the runtime is incomplete; complete it with Create at its incarnation")
	}
	if s.Network.PublicIPv4 != "" && !slices.Contains(w.config.Capabilities.Networks, runtimedriver.NetworkPublicEgress) {
		return nil, errors.New("the public egress substrate is gone")
	}
	return o, nil
}

func (d *Driver) Stop(_ context.Context, s runtimedriver.Session) (runtimedriver.Observation, error) {
	var zero runtimedriver.Observation
	if err := d.alive(); err != nil {
		return zero, err
	}
	if err := d.w.checkSession(s); err != nil {
		return zero, err
	}
	w := d.w
	w.mu.Lock()
	defer w.mu.Unlock()
	o, err := w.observe(s)
	switch {
	case err != nil:
		return zero, err
	case o == nil:
		return zero, errors.New("the session has no runtime")
	case o.terminating:
		return zero, errors.New("the runtime is being deleted")
	}
	if o.state == runtimedriver.StateRunning {
		w.stopRuntime(o)
	}
	return o.observation(s), nil
}

// stopRuntime stops a running runtime gracefully: its terminals' guest
// processes end, and its services and host with them. Called with w.mu held.
func (w *World) stopRuntime(o *object) {
	o.transitions++
	o.state = runtimedriver.StateStopped
	o.host = host{failNext: o.host.failNext, failWith: o.host.failWith}
	for _, a := range w.attachments {
		if a.runtime == o {
			a.endProcess()
		}
	}
	for _, svc := range o.services.services {
		svc.state = runtimedriver.ServiceStopped
	}
}

// Delete removes a session's runtime at incarnation at, stopping it first,
// or what a build left behind.
func (d *Driver) Delete(_ context.Context, r runtimedriver.Ref, at runtimedriver.Incarnation, g runtimedriver.Gate) error {
	if err := d.alive(); err != nil {
		return err
	}
	if g == nil || r.ID == "" {
		return errors.New("invalid delete")
	}
	switch r.Kind {
	case runtimedriver.KindSession:
		return d.deleteSession(r, at, g)
	case runtimedriver.KindBuild:
		if at != "" {
			return errors.New("a build has no incarnation")
		}
		return d.deleteBuilder(r, g)
	}
	return fmt.Errorf("unknown reference kind %q", r.Kind)
}

func (d *Driver) deleteSession(r runtimedriver.Ref, at runtimedriver.Incarnation, g runtimedriver.Gate) error {
	w := d.w
	w.mu.Lock()
	o, err := w.deletable(r, at)
	w.mu.Unlock()
	if err != nil || o == nil {
		return err
	}
	if err := g(); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if current, err := w.deletable(r, at); err != nil || current != o {
		return errors.Join(err, errors.New("the runtime changed before its deletion"))
	}
	if o.state == runtimedriver.StateRunning {
		w.stopRuntime(o)
	}
	landed := w.admit(runtimedriver.EffectDelete, effectKey(runtimedriver.EffectDelete, r.ID), o.name, func() { w.remove(o) })
	if !landed {
		return fmt.Errorf("the deletion was not answered: %w", runtimedriver.ErrUnknownOutcome)
	}
	return nil
}

// deletable is the runtime of r at its address and incarnation at, nil when
// no object carries r. Called with w.mu held.
func (w *World) deletable(r runtimedriver.Ref, at runtimedriver.Incarnation) (*object, error) {
	o := w.objects[address(r)]
	if o == nil {
		if len(w.carrying(r)) > 0 {
			return nil, fmt.Errorf("%w: the reference is carried under another name", runtimedriver.ErrMismatch)
		}
		return nil, nil
	}
	switch {
	case o.instance != thisInstance || o.ref != r:
		return nil, fmt.Errorf("%w: another object is at the reference's address", runtimedriver.ErrMismatch)
	case at == "" || o.incarnation != at:
		return nil, fmt.Errorf("the runtime is at incarnation %s, not %q", o.incarnation, at)
	}
	return o, nil
}

// remove deletes an object with its private root. Called with w.mu held.
func (w *World) remove(o *object) {
	if w.objects[o.name] != o {
		return
	}
	if o.state == runtimedriver.StateRunning {
		w.deletedWhileRunning[o.ref] = true
	}
	for _, a := range w.attachments {
		if a.runtime == o {
			a.endProcess()
		}
	}
	delete(w.objects, o.name)
}
