package memory

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// ReadWorkspace reads one path of a stopped session's workspace, or "." for
// the workspace root.
func (d *Driver) ReadWorkspace(_ context.Context, t runtimedriver.Target, p string, limit int64) (runtimedriver.File, error) {
	var zero runtimedriver.File
	if err := d.alive(); err != nil {
		return zero, err
	}
	if limit < 0 {
		return zero, errors.New("a read needs a limit of zero or more bytes")
	}
	if p != "." {
		if err := checkWorkspacePath(p); err != nil {
			return zero, err
		}
	}
	w := d.w
	o, err := w.stoppedTarget(t)
	if err != nil {
		return zero, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := o.resolve(p)
	if err != nil {
		return zero, err
	}
	if n == nil {
		return runtimedriver.File{Type: runtimedriver.FileAbsent}, nil
	}
	if int64(len(n.data)) > limit {
		return zero, fmt.Errorf("%s holds %d bytes, more than the limit of %d", p, len(n.data), limit)
	}
	return n.file(), nil
}

// WriteWorkspace writes a regular file or a directory, with its exact
// owner, mode and bytes, where its parent directory exists. It never
// replaces a symlink, nor a path of another type.
func (d *Driver) WriteWorkspace(_ context.Context, t runtimedriver.Target, p string, f runtimedriver.File, g runtimedriver.Gate) error {
	if err := d.alive(); err != nil {
		return err
	}
	if g == nil {
		return errors.New("workspace write gate required")
	}
	if f.Type != runtimedriver.FileRegular && f.Type != runtimedriver.FileDirectory || f.Mode&^0o7777 != 0 || f.UID < 0 || f.GID < 0 ||
		f.Type == runtimedriver.FileDirectory && len(f.Data) != 0 {
		return errors.New("only a regular file or a directory with permission bits is written")
	}
	if err := checkWorkspacePath(p); err != nil {
		return err
	}
	w := d.w
	o, err := w.stoppedTarget(t)
	if err != nil {
		return err
	}
	w.mu.Lock()
	err = o.writable(p, f)
	w.mu.Unlock()
	if err != nil {
		return err
	}
	if err := g(); err != nil {
		return err
	}
	if current, err := w.stoppedTarget(t); err != nil || current != o {
		return errors.Join(err, errors.New("the runtime changed before the write"))
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := o.writable(p, f); err != nil {
		return err
	}
	written := &node{typ: f.Type, uid: f.UID, gid: f.GID, mode: f.Mode, data: append([]byte(nil), f.Data...)}
	if !w.admit(runtimedriver.EffectWrite, effectKey(runtimedriver.EffectWrite, o.ref.ID, p), o.name, func() { o.files[workspaceRoot+"/"+p] = written }) {
		return fmt.Errorf("the write was not answered: %w", runtimedriver.ErrUnknownOutcome)
	}
	return nil
}

// writable checks that f can be written at p. Called with w.mu held.
func (o *object) writable(p string, f runtimedriver.File) error {
	if dir := path.Dir(p); dir != "." {
		parent, err := o.resolve(dir)
		if err != nil {
			return err
		}
		if parent == nil || parent.typ != runtimedriver.FileDirectory {
			return fmt.Errorf("the parent of %s is not a directory", p)
		}
	}
	current, err := o.resolve(p)
	if err != nil {
		return err
	}
	if current != nil && current.typ != f.Type {
		return fmt.Errorf("%s is a %s, not a %s", p, current.typ, f.Type)
	}
	return nil
}

// RemoveWorkspace removes a file, a symlink or an empty directory. A path
// already absent needs no removal and no gate.
func (d *Driver) RemoveWorkspace(_ context.Context, t runtimedriver.Target, p string, g runtimedriver.Gate) error {
	if err := d.alive(); err != nil {
		return err
	}
	if g == nil {
		return errors.New("workspace remove gate required")
	}
	if err := checkWorkspacePath(p); err != nil {
		return err
	}
	w := d.w
	o, err := w.stoppedTarget(t)
	if err != nil {
		return err
	}
	w.mu.Lock()
	n, err := o.removable(p)
	w.mu.Unlock()
	if err != nil || n == nil {
		return err
	}
	if err := g(); err != nil {
		return err
	}
	if current, err := w.stoppedTarget(t); err != nil || current != o {
		return errors.Join(err, errors.New("the runtime changed before the removal"))
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if n, err := o.removable(p); err != nil || n == nil {
		return errors.Join(err, fmt.Errorf("%s changed before its removal", p))
	}
	if !w.admit(runtimedriver.EffectRemove, effectKey(runtimedriver.EffectRemove, o.ref.ID, p), o.name, func() { delete(o.files, workspaceRoot+"/"+p) }) {
		return fmt.Errorf("the removal was not answered: %w", runtimedriver.ErrUnknownOutcome)
	}
	return nil
}

// removable is the node at p that a removal would remove, nil when p is
// absent. Called with w.mu held.
func (o *object) removable(p string) (*node, error) {
	n, err := o.resolve(p)
	if err != nil || n == nil {
		return nil, err
	}
	if n.typ == runtimedriver.FileDirectory {
		prefix := workspaceRoot + "/" + p + "/"
		for name := range o.files {
			if strings.HasPrefix(name, prefix) {
				return nil, fmt.Errorf("directory %s is not empty", p)
			}
		}
	}
	return n, nil
}

// stoppedTarget is the stopped runtime of t at its incarnation, whose
// workspace can be reached.
func (w *World) stoppedTarget(t runtimedriver.Target) (*object, error) {
	if t.At == "" {
		return nil, errors.New("workspace operations need the runtime's incarnation")
	}
	if err := w.checkSession(t.Session); err != nil {
		return nil, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	o, err := w.observe(t.Session)
	switch {
	case err != nil:
		return nil, err
	case o == nil:
		return nil, errors.New("the session has no runtime")
	case o.incarnation != t.At:
		return nil, fmt.Errorf("the runtime is at incarnation %s, not %s", o.incarnation, t.At)
	case o.terminating || o.state != runtimedriver.StateStopped:
		return nil, errors.New("the workspace is reached only while the runtime is stopped")
	}
	return o, nil
}

func checkWorkspacePath(p string) error {
	if p == "" || p == "." || path.IsAbs(p) || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") || strings.ContainsRune(p, 0) {
		return fmt.Errorf("workspace path %q is not relative and clean", p)
	}
	return nil
}

// resolve finds the node at the workspace path p, or "." for the root,
// without following symlinks: nil when p is absent. A symlink above p, or a
// foreign mount at, above or below the workspace, is refused, since what p
// would reach is not the private root. Called with w.mu held.
func (o *object) resolve(p string) (*node, error) {
	for _, m := range o.mounts {
		if m == workspaceRoot || strings.HasPrefix(m, workspaceRoot+"/") || m == "/" || strings.HasPrefix(workspaceRoot, m+"/") {
			return nil, fmt.Errorf("a foreign mount at %q reaches the workspace", m)
		}
	}
	root := o.files[workspaceRoot]
	if root == nil || root.typ != runtimedriver.FileDirectory {
		return nil, errors.New("the runtime has no workspace")
	}
	if p == "." {
		return root, nil
	}
	current := workspaceRoot
	parts := strings.Split(p, "/")
	for i, part := range parts {
		current += "/" + part
		n := o.files[current]
		if n == nil || i == len(parts)-1 {
			return n, nil
		}
		switch n.typ {
		case runtimedriver.FileSymlink:
			return nil, fmt.Errorf("%s is a symlink above %s", strings.TrimPrefix(current, workspaceRoot+"/"), p)
		case runtimedriver.FileDirectory:
		default:
			return nil, nil // a file above p: p cannot exist
		}
	}
	return nil, nil
}
