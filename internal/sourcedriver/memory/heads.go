package memory

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/gitnames"
)

// headControls are the World's controls over head reads and head effects.
type headControls struct {
	// betweenReads runs once, between the two reads of the next head
	// observation.
	betweenReads func()
	// dieAfterEffect makes the next head effect's driver die once the
	// effect lands.
	dieAfterEffect bool
}

// errMoved is a head observation whose two reads differ: a new read may
// succeed.
var errMoved = fmt.Errorf("the heads moved while they were read: %w", sourcedriver.ErrConflict)

// errDied is a call whose driver died after its effect landed.
var errDied = fmt.Errorf("the driver died after its effect: %w", sourcedriver.ErrUnknownOutcome)

// open is the project's repository for a call. The caller holds w.mu.
func (w *World) open(project string) (*repo, error) {
	if err := validProject(project); err != nil {
		return nil, err
	}
	if w.partial[project] {
		return nil, errPartial
	}
	r := w.repos[project]
	if r == nil {
		return nil, fmt.Errorf("project %q: %w", project, sourcedriver.ErrNotFound)
	}
	return r, nil
}

// validBranch refuses a branch name CheckNames refuses.
func validBranch(name string) error {
	if !gitnames.Branch(name) {
		return fmt.Errorf("branch %q: %w", name, sourcedriver.ErrInvalid)
	}
	return nil
}

// validCommit refuses a commit ID CheckNames refuses.
func validCommit(id sourcedriver.OID) error {
	if !gitnames.OID(string(id)) {
		return fmt.Errorf("commit %q: %w", id, sourcedriver.ErrInvalid)
	}
	return nil
}

// resolves reports whether the symbolic ref name leads to a head, as Git
// resolves it. The caller holds w.mu.
func (r *repo) resolves(name string) bool {
	for range 5 {
		target, ok := r.symbolic[name]
		if !ok {
			_, head := r.heads[name]
			return head
		}
		name = target
	}
	return false
}

// readHeads reads every head once: in byte order, each a P branch at a
// commit. A symbolic ref that leads to a head is an error, since a deletion
// of its target would leave it dangling; a dangling one holds nothing. The
// caller holds w.mu.
func (r *repo) readHeads() ([]sourcedriver.Head, error) {
	for name := range r.symbolic {
		if r.resolves(name) {
			return nil, fmt.Errorf("the symbolic ref %q holds a head's name", name)
		}
	}
	if len(r.heads) > sourcedriver.MaxHeads {
		return nil, fmt.Errorf("%d heads: %w", len(r.heads), sourcedriver.ErrLimit)
	}
	heads := make([]sourcedriver.Head, 0, len(r.heads))
	for name, tip := range r.heads {
		if !gitnames.Branch(name) {
			return nil, fmt.Errorf("the head %q is not a P branch", name)
		}
		if o := r.objects[tip]; o == nil || o.typ != commitObject {
			return nil, fmt.Errorf("the head %q is not at a commit", name)
		}
		heads = append(heads, sourcedriver.Head{Name: name, Tip: tip})
	}
	slices.SortFunc(heads, func(a, b sourcedriver.Head) int { return strings.Compare(a.Name, b.Name) })
	return heads, nil
}

// observe reads every head of the project, runs use on them in the same
// read, and reads them again after the between-reads control: heads that
// moved between the reads are errMoved.
func (w *World) observe(project string, use func(r *repo, heads []sourcedriver.Head) error) ([]sourcedriver.Head, error) {
	w.mu.Lock()
	r, err := w.open(project)
	if err != nil {
		w.mu.Unlock()
		return nil, err
	}
	first, err := r.readHeads()
	if err == nil && use != nil {
		err = use(r, first)
	}
	between := w.headControls.betweenReads
	w.headControls.betweenReads = nil
	w.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if between != nil {
		between()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if r, err = w.open(project); err != nil {
		return nil, err
	}
	again, err := r.readHeads()
	if err != nil {
		return nil, err
	}
	if !slices.Equal(first, again) {
		return nil, errMoved
	}
	return first, nil
}

// Branches returns every head as one observation, read twice.
func (d *Driver) Branches(_ context.Context, project string) ([]sourcedriver.Head, error) {
	if err := d.alive(); err != nil {
		return nil, err
	}
	return d.w.observe(project, nil)
}

// headState is what holds a branch name: a head's tip, a symbolic ref, or
// nothing.
type headState struct {
	tip      sourcedriver.OID
	symbolic bool
}

// state reads what holds the name. The caller holds w.mu.
func (r *repo) state(name string) headState {
	if _, ok := r.symbolic[name]; ok {
		return headState{symbolic: true}
	}
	return headState{tip: r.heads[name]}
}

func (d *Driver) InspectBranch(_ context.Context, project, name string) (sourcedriver.OID, bool, error) {
	if err := d.alive(); err != nil {
		return "", false, err
	}
	if err := validBranch(name); err != nil {
		return "", false, err
	}
	d.w.mu.Lock()
	defer d.w.mu.Unlock()
	r, err := d.w.open(project)
	if err != nil {
		return "", false, err
	}
	s := r.state(name)
	if s.symbolic {
		return "", false, fmt.Errorf("a symbolic ref holds %q: %w", name, sourcedriver.ErrConflict)
	}
	return s.tip, s.tip != "", nil
}

// headEffect is one head creation or deletion: the state the name must hold
// before it, and the state it leaves.
type headEffect struct {
	project, name string
	old, new      sourcedriver.OID
}

// check refuses the effect unless the name holds exactly its old state and
// a new tip is a commit of the repository. The caller holds w.mu.
func (w *World) check(e headEffect) (*repo, error) {
	if err := w.checkStorage(); err != nil {
		return nil, err
	}
	r, err := w.open(e.project)
	if err != nil {
		return nil, err
	}
	if s := r.state(e.name); s.symbolic || s.tip != e.old {
		return nil, fmt.Errorf("the head %q is not as expected: %w", e.name, sourcedriver.ErrConflict)
	}
	if e.new != "" {
		if o := r.objects[e.new]; o == nil || o.typ != commitObject {
			return nil, fmt.Errorf("%s is not a commit of the repository: %w", e.new, sourcedriver.ErrConflict)
		}
	}
	return r, nil
}

// apply checks the effect, calls the gate, and makes the effect unless the
// driver died meanwhile. The gate runs without w.mu, since core's gate may
// read the World.
func (d *Driver) apply(e headEffect, g sourcedriver.Gate) error {
	w := d.w
	w.mu.Lock()
	_, err := w.check(e)
	w.mu.Unlock()
	if err != nil {
		return err
	}
	if err := g(); err != nil {
		return err
	}
	if err := d.alive(); err != nil {
		return err
	}
	w.mu.Lock()
	r, err := w.check(e)
	if err != nil {
		w.mu.Unlock()
		return err
	}
	if e.new == "" {
		delete(r.heads, e.name)
	} else {
		r.heads[e.name] = e.new
	}
	die := w.headControls.dieAfterEffect
	w.headControls.dieAfterEffect = false
	w.mu.Unlock()
	if die {
		d.Kill()
		return errDied
	}
	return nil
}

func (d *Driver) CreateBranch(_ context.Context, project, name string, at sourcedriver.OID, g sourcedriver.Gate) error {
	if err := d.alive(); err != nil {
		return err
	}
	if err := errors.Join(validBranch(name), validCommit(at)); err != nil {
		return err
	}
	return d.apply(headEffect{project: project, name: name, new: at}, g)
}

func (d *Driver) DeleteBranch(_ context.Context, project, name string, tip sourcedriver.OID, g sourcedriver.Gate) error {
	if err := d.alive(); err != nil {
		return err
	}
	if err := errors.Join(validBranch(name), validCommit(tip)); err != nil {
		return err
	}
	return d.apply(headEffect{project: project, name: name, old: tip}, g)
}

// MoveDuringRead moves a head between the two reads of the next head
// observation.
func (w *World) MoveDuringRead(project, name string, tip sourcedriver.OID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.headControls.betweenReads = func() { w.SetHead(project, name, tip) }
}

// SetHeads points each named head at tip.
func (w *World) SetHeads(project string, names []string, tip sourcedriver.OID) {
	for _, name := range names {
		w.SetHead(project, name, tip)
	}
}

// DieAfterHeadEffect makes the next head effect's driver die once the
// effect lands.
func (w *World) DieAfterHeadEffect() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.headControls.dieAfterEffect = true
}

// DamageHead points the head at tip, whatever tip is.
func (w *World) DamageHead(project, name string, tip sourcedriver.OID) {
	w.SetHead(project, name, tip)
}
