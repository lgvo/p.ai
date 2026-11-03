// Package memory is a complete in-memory runtime driver. Its native state, a
// World, lives apart from the drivers over it, as a backend's does, so a test
// can restart the driver; the World's native controls act on that state the
// way an administrator, a crash or a slow backend would. It models P's
// concepts, not any real backend, and it is deterministic and safe for
// concurrent use.
package memory

import (
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// The P instance labels a World's objects carry: this instance's, which its
// drivers act on, or another instance's, which they never touch.
const (
	thisInstance  = "this"
	otherInstance = "other"
)

// Config is what a World's backend offers.
type Config struct {
	// Capacity is how many runtimes and builders the scope admits; 0 for no
	// limit.
	Capacity     int
	Capabilities runtimedriver.Capabilities
}

// World is the native state of an in-memory backend: its runtimes and
// builders, images, attachments, and what it admitted.
type World struct {
	mu          sync.Mutex
	config      Config
	objects     map[string]*object // by native name
	images      map[runtimedriver.ImageRef]*image
	attachments map[string]*attachment // by terminal ID
	baseImage   runtimedriver.ImageRef

	initializations     map[runtimedriver.Ref]int
	deletedWhileRunning map[runtimedriver.Ref]bool
	// admitted holds the key of every effect the backend admitted;
	// pending, the delayed ones it may still admit; delays, the delay armed
	// for the next effect of a kind.
	admitted map[string]bool
	pending  map[string]*Admission
	delays   map[runtimedriver.EffectKind]*Admission
	// busy counts the operations of the backend running on each native
	// name; they outlast the object they act on.
	busy    map[string]int
	death   *death
	partial bool // the next listing of the scope is incomplete
	counter int
}

// object is one runtime or builder in scope.
type object struct {
	name        string
	instance    string // whose P labels it carries; "" for none
	ref         runtimedriver.Ref
	labels      runtimedriver.Labels
	image       runtimedriver.ImageRef
	incarnation runtimedriver.Incarnation
	state       runtimedriver.State // stopped or running
	terminating bool

	devices   devices
	assembled bool             // every guest file of the spec was installed
	files     map[string]*node // the private root, by absolute guest path
	mounts    []string         // foreign mounts, by absolute guest path

	host        host
	transitions int
	services    serviceManager
}

// devices are what a runtime is given from outside its private root.
type devices struct {
	endpoints string                         // the endpoint directory; "" until installed
	grants    map[string]runtimedriver.Grant // by name
	address   string                         // the public-egress address; "" for none
}

// host is the session host of a runtime.
type host struct {
	ready      bool
	diagnostic string // why it is not ready, or a stopped host's last words
	failNext   bool   // the next start fails, telling failWith
	failWith   string
}

// node is one path of a runtime's private root.
type node struct {
	typ    runtimedriver.FileType
	uid    int
	gid    int
	mode   uint32
	data   []byte
	target string // a symlink's target
}

func (n *node) file() runtimedriver.File {
	return runtimedriver.File{Type: n.typ, UID: n.uid, GID: n.gid, Mode: n.mode, Data: append([]byte(nil), n.data...)}
}

func (n *node) equals(f runtimedriver.File) bool {
	return n.typ == f.Type && n.uid == f.UID && n.gid == f.GID && n.mode == f.Mode && string(n.data) == string(f.Data)
}

type image struct {
	ref      runtimedriver.ImageRef
	instance string // "" for an image no P instance labeled, such as the base image
	labels   runtimedriver.Labels
	shell    runtimedriver.Shell
	size     int64
}

func (im *image) public() runtimedriver.Image {
	return runtimedriver.Image{Ref: im.ref, Labels: maps.Clone(im.labels), Shell: im.shell, Size: im.size}
}

// NewWorld returns an empty scope holding only a base image.
func NewWorld(c Config) *World {
	c.Capabilities.Networks = append([]string(nil), c.Capabilities.Networks...)
	w := &World{
		config: c, objects: map[string]*object{}, images: map[runtimedriver.ImageRef]*image{},
		attachments: map[string]*attachment{}, initializations: map[runtimedriver.Ref]int{},
		deletedWhileRunning: map[runtimedriver.Ref]bool{}, admitted: map[string]bool{},
		pending: map[string]*Admission{}, delays: map[runtimedriver.EffectKind]*Admission{}, busy: map[string]int{},
	}
	w.baseImage = w.newImageRef()
	w.images[w.baseImage] = &image{ref: w.baseImage, size: 1 << 28}
	return w
}

// BaseImage is the image in scope that no P instance labeled; sessions
// without a commit image are created from it.
func (w *World) BaseImage() runtimedriver.ImageRef { return w.baseImage }

// SeedImage adds a commit image of this P instance to the scope.
func (w *World) SeedImage(labels runtimedriver.Labels, shell runtimedriver.Shell) runtimedriver.Image {
	w.mu.Lock()
	defer w.mu.Unlock()
	im := &image{ref: w.newImageRef(), instance: thisInstance, labels: maps.Clone(labels), shell: shell, size: 1 << 30}
	w.images[im.ref] = im
	return im.public()
}

// AddOtherInstanceImage adds an image another P instance labeled.
func (w *World) AddOtherInstanceImage() runtimedriver.ImageRef {
	w.mu.Lock()
	defer w.mu.Unlock()
	im := &image{ref: w.newImageRef(), instance: otherInstance, labels: runtimedriver.Labels{runtimedriver.LabelProject: "other"}, size: 1 << 30}
	w.images[im.ref] = im
	return im.ref
}

// next returns a number no earlier call returned. Called with w.mu held, or
// before the World is shared.
func (w *World) next() int {
	w.counter++
	return w.counter
}

func (w *World) newImageRef() runtimedriver.ImageRef {
	return runtimedriver.ImageRef(fmt.Sprintf("%064x", w.next()))
}

// address is the native name a driver gives the object of a reference.
func address(r runtimedriver.Ref) string { return string(r.Kind) + "-" + r.ID }

// carrying lists the objects that carry this instance's reference r, at any
// name. Called with w.mu held.
func (w *World) carrying(r runtimedriver.Ref) []*object {
	var found []*object
	for _, o := range w.objects {
		if o.instance == thisInstance && o.ref == r {
			found = append(found, o)
		}
	}
	return found
}

// runtimeOf is this instance's object carrying r, at any name; nil when
// there is none. Called with w.mu held.
func (w *World) runtimeOf(r runtimedriver.Ref) *object {
	if found := w.carrying(r); len(found) > 0 {
		return found[0]
	}
	return nil
}

// full reports whether the scope admits no other object. Called with w.mu
// held.
func (w *World) full() bool {
	return w.config.Capacity > 0 && len(w.objects) >= w.config.Capacity
}

// inFlight reports whether anything the backend admitted, or may still
// admit, is under way on the target. Called with w.mu held.
func (w *World) inFlight(target string) bool {
	for _, a := range w.pending {
		if a.target == target {
			return true
		}
	}
	return w.busy[target] > 0
}

// Admission is the late admission of one effect, armed by Delay.
type Admission struct {
	w      *World
	kind   runtimedriver.EffectKind
	key    string // the effect's key, once the effect reached the backend
	target string // the native name or image reference it acts on
	apply  func()
}

// Delay makes the backend admit the next effect of kind late: the call
// passes its gate and returns ErrUnknownOutcome, and the effect stays
// pending, invisible to every observation, until Land or Drop. Delayable
// kinds are create (the initialization), delete (the removal), write,
// remove, build (the publication) and delete-image.
func (w *World) Delay(kind runtimedriver.EffectKind) *Admission {
	w.mu.Lock()
	defer w.mu.Unlock()
	a := &Admission{w: w, kind: kind}
	w.delays[kind] = a
	return a
}

// Land admits the delayed effect now. Before the effect reached the
// backend, it only disarms the delay.
func (a *Admission) Land() {
	w := a.w
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.delays[a.kind] == a {
		delete(w.delays, a.kind)
		return
	}
	if w.pending[a.key] != a {
		return
	}
	delete(w.pending, a.key)
	w.admitted[a.key] = true
	a.apply()
}

// Drop makes the delayed effect never land, as a backend that proved it
// discarded the request.
func (a *Admission) Drop() {
	w := a.w
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.delays[a.kind] == a {
		delete(w.delays, a.kind)
	}
	if w.pending[a.key] == a {
		delete(w.pending, a.key)
	}
}

// admit lets the backend admit one effect on target: at once, or late when
// a delay of its kind is armed. It reports whether the effect landed.
// Called with w.mu held.
func (w *World) admit(kind runtimedriver.EffectKind, key, target string, apply func()) bool {
	if a := w.delays[kind]; a != nil {
		delete(w.delays, kind)
		a.key, a.target, a.apply = key, target, apply
		w.pending[key] = a
		return false
	}
	w.admitted[key] = true
	apply()
	return true
}

func effectKey(kind runtimedriver.EffectKind, parts ...string) string {
	key := string(kind)
	for _, p := range parts {
		key += "\x00" + p
	}
	return key
}

// CreatePoint is where DieInCreate stops a driver.
type CreatePoint int

const (
	// AfterInit: the runtime is initialized and nothing is installed yet.
	AfterInit CreatePoint = iota
	// BetweenGuestWrites: one guest file is written, the next is not.
	BetweenGuestWrites
	// DuringGuestWrite: the second guest file landed whole, unanswered.
	DuringGuestWrite
	// DuringPartialGuestWrite: half of the second guest file landed.
	DuringPartialGuestWrite
)

type death struct {
	point   CreatePoint
	reached chan struct{}
}

// DieInCreate makes the driver that next reaches point inside Create die
// there: the call effects nothing more and returns ErrUnknownOutcome once
// that driver is killed. The channel closes when the driver reached point.
func (w *World) DieInCreate(point CreatePoint) <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.death = &death{point: point, reached: make(chan struct{})}
	return w.death.reached
}

// Initializations counts the runtimes the backend initialized for r.
func (w *World) Initializations(r runtimedriver.Ref) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.initializations[r]
}

// Transitions counts the starts and stops the backend performed on the
// runtime of r.
func (w *World) Transitions(r runtimedriver.Ref) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if o := w.runtimeOf(r); o != nil {
		return o.transitions
	}
	return 0
}

// RenameAway moves the runtime of r to a name no driver uses, keeping its
// labels and incarnation.
func (w *World) RenameAway(r runtimedriver.Ref) {
	w.mu.Lock()
	defer w.mu.Unlock()
	o := w.mustRuntime(r)
	delete(w.objects, o.name)
	o.name = fmt.Sprintf("renamed-%d", w.next())
	w.objects[o.name] = o
}

// Drift is one way a runtime can differ from its spec.
type Drift int

const (
	DriftLabels  Drift = iota // every label value changed
	DriftImage                // another image
	DriftDevices              // a grant device the spec does not name
)

// Drift changes the runtime of r so that it differs from its spec as d
// says, keeping the reference.
func (w *World) Drift(r runtimedriver.Ref, d Drift) {
	w.mu.Lock()
	defer w.mu.Unlock()
	o := w.mustRuntime(r)
	switch d {
	case DriftLabels:
		for k, v := range o.labels {
			o.labels[k] = v + "-drifted"
		}
	case DriftImage:
		o.image = w.newImageRef()
	case DriftDevices:
		o.devices.grants["drifted"] = runtimedriver.Grant{Name: "drifted", Source: "/srv/drifted", Type: "directory", Access: "read-write"}
	}
}

// FailHostOnNextStart makes the session host of r fail on the runtime's
// next start, leaving diagnostic as its last words.
func (w *World) FailHostOnNextStart(r runtimedriver.Ref, diagnostic string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	o := w.mustRuntime(r)
	o.host.failNext, o.host.failWith = true, diagnostic
}

// UnreadyHost makes the session host of the running runtime of r report
// that it is not ready, with diagnostic.
func (w *World) UnreadyHost(r runtimedriver.Ref, diagnostic string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	o := w.mustRuntime(r)
	o.host.ready, o.host.diagnostic = false, diagnostic
}

// PublicAddress is the public-egress address the runtime of r is attached
// to, or "".
func (w *World) PublicAddress(r runtimedriver.Ref) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.mustRuntime(r).devices.address
}

// WriteRootFile writes data to a regular file of the session user at path,
// an absolute guest path, in the private root of the runtime of r.
func (w *World) WriteRootFile(r runtimedriver.Ref, path string, data []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.mustRuntime(r).files[path] = &node{typ: runtimedriver.FileRegular, uid: workspaceUID, gid: workspaceUID, mode: 0o644, data: slices.Clone(data)}
}

// ReadRootFile reads the regular file at path, an absolute guest path, in
// the private root of the runtime of r; false when there is none.
func (w *World) ReadRootFile(r runtimedriver.Ref, path string) ([]byte, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := w.mustRuntime(r).files[path]
	if n == nil || n.typ != runtimedriver.FileRegular {
		return nil, false
	}
	return slices.Clone(n.data), true
}

// Hold keeps an operation of the backend running on the runtime of r until
// release is called, even once the runtime is gone.
func (w *World) Hold(r runtimedriver.Ref) (release func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	name := w.mustRuntime(r).name
	w.busy[name]++
	var once sync.Once
	return func() {
		once.Do(func() {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.busy[name]--
		})
	}
}

// DeletedWhileRunning reports whether the backend deleted the runtime of r
// while it was running.
func (w *World) DeletedWhileRunning(r runtimedriver.Ref) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.deletedWhileRunning[r]
}

// Terminate starts the deletion of the runtime of r and keeps it in
// progress.
func (w *World) Terminate(r runtimedriver.Ref) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.mustRuntime(r).terminating = true
}

// PartialInventory makes the backend's next listing of the scope
// incomplete.
func (w *World) PartialInventory() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.partial = true
}

// AddUnlabeled adds a stopped object without P labels to the scope and
// returns its name.
func (w *World) AddUnlabeled() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.addForeign("", runtimedriver.Ref{}, nil)
}

// AddOtherInstance adds a stopped object another P instance labeled with r
// and labels, and returns its name.
func (w *World) AddOtherInstance(r runtimedriver.Ref, labels runtimedriver.Labels) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.addForeign(otherInstance, r, labels)
}

func (w *World) addForeign(instance string, r runtimedriver.Ref, labels runtimedriver.Labels) string {
	n := w.next()
	o := &object{name: fmt.Sprintf("foreign-%d", n), instance: instance, ref: r, labels: maps.Clone(labels),
		incarnation: runtimedriver.Incarnation(fmt.Sprintf("incarnation-%d", n)), state: runtimedriver.StateStopped}
	w.objects[o.name] = o
	return o.name
}

// Symlink leaves a symbolic link at path, relative to the workspace of the
// runtime of r, pointing at target.
func (w *World) Symlink(r runtimedriver.Ref, path, target string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.mustRuntime(r).files[workspaceRoot+"/"+path] = &node{typ: runtimedriver.FileSymlink, uid: workspaceUID, gid: workspaceUID, mode: 0o777, target: target}
}

// ForeignMount makes a mount that is not the runtime's private storage
// appear at guestPath of the runtime of r.
func (w *World) ForeignMount(r runtimedriver.Ref, guestPath string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	o := w.mustRuntime(r)
	o.mounts = append(o.mounts, guestPath)
}

// SetRoot gives the workspace root of the runtime of r an owner and
// permission bits.
func (w *World) SetRoot(r runtimedriver.Ref, uid, gid int, mode uint32) {
	w.mu.Lock()
	defer w.mu.Unlock()
	o := w.mustRuntime(r)
	root := *o.files[workspaceRoot]
	root.uid, root.gid, root.mode = uid, gid, mode
	o.files[workspaceRoot] = &root
}

// mustRuntime is the runtime of r; a control on a runtime that does not
// exist is a mistake in the test that calls it. Called with w.mu held.
func (w *World) mustRuntime(r runtimedriver.Ref) *object {
	o := w.runtimeOf(r)
	if o == nil {
		panic(fmt.Sprintf("memory: no runtime carries %s %s", r.Kind, r.ID))
	}
	return o
}
