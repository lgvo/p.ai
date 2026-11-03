package memory

import (
	"fmt"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// The native controls below act on a World's repositories directly, without
// a driver, as Git, a session's push or an administrator would. They panic
// on a project with no repository: a test that calls them made one first.

// repoOf is the project's repository. The caller holds w.mu.
func (w *World) repoOf(project string) *repo {
	r := w.repos[project]
	if r == nil {
		panic(fmt.Sprintf("memory: project %q has no repository", project))
	}
	return r
}

// Commit writes a new commit object with parents into the project's
// repository, reachable from no head, and returns its ID.
func (w *World) Commit(project string, parents ...sourcedriver.OID) sourcedriver.OID {
	w.mu.Lock()
	defer w.mu.Unlock()
	r := w.repoOf(project)
	id := sourcedriver.OID(w.newID("commit"))
	r.objects[id] = &object{typ: commitObject, parents: append([]sourcedriver.OID(nil), parents...)}
	return id
}

// Tree writes a tree object into the project's repository and returns its
// ID.
func (w *World) Tree(project string) sourcedriver.OID {
	w.mu.Lock()
	defer w.mu.Unlock()
	r := w.repoOf(project)
	id := sourcedriver.OID(w.newID("tree"))
	r.objects[id] = &object{typ: treeObject}
	return id
}

// SetHead points refs/heads/<name> at tip, or removes it when tip is empty.
func (w *World) SetHead(project, name string, tip sourcedriver.OID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	r := w.repoOf(project)
	delete(r.symbolic, name)
	if tip == "" {
		delete(r.heads, name)
		return
	}
	r.heads[name] = tip
}

// SetSymbolic makes refs/heads/<name> a symbolic ref to
// refs/heads/<target>.
func (w *World) SetSymbolic(project, name, target string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	r := w.repoOf(project)
	delete(r.heads, name)
	r.symbolic[name] = target
}
