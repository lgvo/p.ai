package memory

import (
	"context"
	"errors"
	"fmt"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

var (
	errUnsafeStorage = errors.New("the storage is not private to P's account")
	errPartial       = errors.New("the project's address holds an incomplete repository")
)

// Check succeeds unless a control made the storage unsafe.
func (d *Driver) Check(context.Context) error {
	if err := d.alive(); err != nil {
		return err
	}
	d.w.mu.Lock()
	defer d.w.mu.Unlock()
	return d.w.checkStorage()
}

// checkStorage is the check every mutation makes first. The caller holds
// w.mu.
func (w *World) checkStorage() error {
	if w.unsafeStorage {
		return errUnsafeStorage
	}
	return nil
}

// validProject refuses only what core never sends.
func validProject(project string) error {
	if project == "" {
		return fmt.Errorf("empty project: %w", sourcedriver.ErrInvalid)
	}
	return nil
}

func (d *Driver) InspectRepository(_ context.Context, project string) (sourcedriver.Repository, error) {
	if err := d.alive(); err != nil {
		return sourcedriver.Repository{}, err
	}
	if err := validProject(project); err != nil {
		return sourcedriver.Repository{}, err
	}
	d.w.mu.Lock()
	defer d.w.mu.Unlock()
	return d.w.inspect(project)
}

// inspect reports what is at the project's address. The caller holds w.mu.
func (w *World) inspect(project string) (sourcedriver.Repository, error) {
	if w.partial[project] {
		return sourcedriver.Repository{}, errPartial
	}
	r := w.repos[project]
	if r == nil {
		return sourcedriver.Repository{Project: project}, nil
	}
	return sourcedriver.Repository{Project: project, Exists: true, Incarnation: r.incarnation}, nil
}

// CreateRepository keeps an existing repository whose HEAD names main and
// refuses one whose HEAD names another branch.
func (d *Driver) CreateRepository(_ context.Context, project string) (sourcedriver.Repository, error) {
	if err := d.alive(); err != nil {
		return sourcedriver.Repository{}, err
	}
	if err := validProject(project); err != nil {
		return sourcedriver.Repository{}, err
	}
	w := d.w
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.checkStorage(); err != nil {
		return sourcedriver.Repository{}, err
	}
	current, err := w.inspect(project)
	if err != nil {
		return sourcedriver.Repository{}, err
	}
	if current.Exists {
		if head := w.repos[project].head; head != "main" {
			return sourcedriver.Repository{}, fmt.Errorf("the existing repository's HEAD names %q, not main: %w", head, sourcedriver.ErrConflict)
		}
		return current, nil
	}
	w.repos[project] = &repo{
		incarnation: w.newID("incarnation"),
		heads:       map[string]sourcedriver.OID{},
		symbolic:    map[string]string{},
		objects:     map[sourcedriver.OID]*object{},
		head:        "main",
	}
	return w.inspect(project)
}

// DeleteRepository removes the repository at r's incarnation. The gate runs
// without w.mu, since core's gate may read the World; the repository is
// read again after it.
func (d *Driver) DeleteRepository(_ context.Context, r sourcedriver.Repository, g sourcedriver.Gate) error {
	if err := d.alive(); err != nil {
		return err
	}
	if err := validProject(r.Project); err != nil {
		return err
	}
	w := d.w
	w.mu.Lock()
	present, err := w.atIncarnation(r)
	w.mu.Unlock()
	if !present || err != nil {
		return err
	}
	if err := g(); err != nil {
		return err
	}
	if err := d.alive(); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if present, err = w.atIncarnation(r); present && err == nil {
		delete(w.repos, r.Project)
	}
	return err
}

// atIncarnation reports whether r's repository is present, at r's
// incarnation; another incarnation is ErrConflict. The caller holds w.mu.
func (w *World) atIncarnation(r sourcedriver.Repository) (bool, error) {
	if err := w.checkStorage(); err != nil {
		return false, err
	}
	current, err := w.inspect(r.Project)
	switch {
	case err != nil:
		return false, err
	case !current.Exists:
		return false, nil
	case current.Incarnation != r.Incarnation:
		return false, fmt.Errorf("another incarnation of the repository: %w", sourcedriver.ErrConflict)
	}
	return true, nil
}

// LoosenStorage makes the storage writable by others: Check and every
// mutation refuse from now on.
func (w *World) LoosenStorage() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.unsafeStorage = true
}

// DefaultBranch is the branch the project's HEAD names.
func (w *World) DefaultBranch(project string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.repoOf(project).head
}

// SetDefaultBranch points the project's HEAD at refs/heads/<name>.
func (w *World) SetDefaultBranch(project, name string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.repoOf(project).head = name
}

// LeavePartial leaves an incomplete repository at the project's address.
func (w *World) LeavePartial(project string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.repos[project] != nil {
		panic(fmt.Sprintf("memory: project %q already has a repository", project))
	}
	w.partial[project] = true
}
