// Package memory is a complete in-memory source driver. Its native state, a
// World, lives apart from the drivers over it, as a backend's does, so a test
// can restart the driver; the World's native controls act on that state the
// way Git, a session's push, an administrator or a crash would. It models
// P's concepts over a Git-like object graph, not any real backend, and it is
// deterministic and safe for concurrent use.
package memory

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// World is the native state of an in-memory backend: P's repositories, the
// origins they reach, and the services in flight.
type World struct {
	mu      sync.Mutex
	repos   map[string]*repo // by project
	counter int

	// unsafeStorage: a control made the storage writable by others, so
	// Check and every mutation refuse.
	unsafeStorage bool
	// noOriginTransport: a control took away what the driver reaches
	// origins through, so Check refuses and no origin can be reached.
	noOriginTransport bool
	// partial holds the projects whose address holds something that is not
	// a complete repository, as a crash or an administrator could leave it.
	partial map[string]bool
	// headControls are the controls that disturb head reads and interrupt
	// head effects.
	headControls headControls
	// cutExport cuts the next Export off by its deadline.
	cutExport bool
	// origins holds the origins P's repositories reach, by URL; nil until
	// the first one is made.
	origins map[string]*origin
	// services are the Git services drivers run; nil until the first.
	services map[*gitService]struct{}
}

// repo is one project's repository.
type repo struct {
	incarnation string
	heads       map[string]sourcedriver.OID // by branch name
	symbolic    map[string]string           // branch name -> target branch name
	objects     map[sourcedriver.OID]*object
	// head is the branch HEAD names: the repository's default branch.
	head string
	// otherRefs are refs outside refs/heads, by full name, which no session
	// may see; nil until the first.
	otherRefs map[string]sourcedriver.OID
}

// objectType is a Git object's type.
type objectType string

const (
	commitObject objectType = "commit"
	treeObject   objectType = "tree"
	tagObject    objectType = "tag"
)

// object is one object of a repository.
type object struct {
	typ     objectType
	parents []sourcedriver.OID // a commit's parents
	target  sourcedriver.OID   // a tag's object
}

// NewWorld returns an empty backend.
func NewWorld() *World {
	return &World{repos: map[string]*repo{}, partial: map[string]bool{}}
}

// newID returns an object ID or incarnation no earlier call returned. The
// caller holds w.mu.
func (w *World) newID(kind string) string {
	w.counter++
	sum := sha1.Sum([]byte(fmt.Sprintf("%s %d", kind, w.counter)))
	return hex.EncodeToString(sum[:])
}

// reachable reports whether from reaches id through parents, from included.
// The caller holds w.mu.
func (r *repo) reachable(from, id sourcedriver.OID) bool {
	seen := map[sourcedriver.OID]bool{}
	stack := []sourcedriver.OID{from}
	for len(stack) > 0 {
		next := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if next == id {
			return true
		}
		if seen[next] {
			continue
		}
		seen[next] = true
		if o := r.objects[next]; o != nil && o.typ == commitObject {
			stack = append(stack, o.parents...)
		}
	}
	return false
}
