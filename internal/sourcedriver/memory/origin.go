package memory

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/gitnames"
)

// origin is one origin, reached by its URL. Object IDs are the World's, so
// an object an origin and a repository both hold is the same object.
type origin struct {
	refs    map[string]sourcedriver.OID // full ref name -> object
	objects map[sourcedriver.OID]*object
	// unreachable: every contact fails.
	unreachable bool
	// rejectNext: the next push that would change a ref is refused.
	rejectNext bool
	// loseNext: the next push lands and its answer is lost.
	loseNext bool
}

// The origin controls below act on an origin as its other users would. They
// panic on a URL NewOrigin did not return.

// originOf is the origin at url. The caller holds w.mu.
func (w *World) originOf(url string) *origin {
	o := w.origins[url]
	if o == nil {
		panic(fmt.Sprintf("memory: no origin at %q", url))
	}
	return o
}

// NewOrigin makes an empty origin and returns its URL.
func (w *World) NewOrigin() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.origins == nil {
		w.origins = map[string]*origin{}
	}
	url := fmt.Sprintf("git@origin%d.test:repo.git", len(w.origins)+1)
	w.origins[url] = &origin{refs: map[string]sourcedriver.OID{}, objects: map[sourcedriver.OID]*object{}}
	return url
}

// OriginCommit writes a new commit with parents into the origin and returns
// its ID.
func (w *World) OriginCommit(url string, parents ...sourcedriver.OID) sourcedriver.OID {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.originOf(url).write(w, &object{typ: commitObject, parents: append([]sourcedriver.OID(nil), parents...)})
}

// OriginTree writes a tree into the origin and returns its ID.
func (w *World) OriginTree(url string) sourcedriver.OID {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.originOf(url).write(w, &object{typ: treeObject})
}

// SetOriginRef points the origin's ref at id, or removes it when id is
// empty.
func (w *World) SetOriginRef(url, ref string, id sourcedriver.OID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	o := w.originOf(url)
	if id == "" {
		delete(o.refs, ref)
		return
	}
	o.refs[ref] = id
}

// OriginAnnotatedTag writes a tag object of target into the origin, points
// refs/tags/<name> at it, and returns its ID.
func (w *World) OriginAnnotatedTag(url, name string, target sourcedriver.OID) sourcedriver.OID {
	w.mu.Lock()
	defer w.mu.Unlock()
	o := w.originOf(url)
	id := o.write(w, &object{typ: tagObject, target: target})
	o.refs["refs/tags/"+name] = id
	return id
}

// OriginRef reads the origin's ref; empty when it is absent.
func (w *World) OriginRef(url, ref string) sourcedriver.OID {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.originOf(url).refs[ref]
}

// CutOffOrigin makes every later contact with the origin fail.
func (w *World) CutOffOrigin(url string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.originOf(url).unreachable = true
}

// RejectNextOriginPush makes the origin refuse the next push that would
// change a ref.
func (w *World) RejectNextOriginPush(url string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.originOf(url).rejectNext = true
}

// LoseNextOriginAnswer lets the next push land and loses its answer.
func (w *World) LoseNextOriginAnswer(url string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.originOf(url).loseNext = true
}

// write adds a new object to the origin and returns its ID. The caller
// holds w.mu.
func (o *origin) write(w *World, obj *object) sourcedriver.OID {
	id := sourcedriver.OID(w.newID(string(obj.typ)))
	o.objects[id] = obj
	return id
}

// ObserveOrigin answers the origin's heads and tags with their peeled
// targets, in byte order of Ref.
func (d *Driver) ObserveOrigin(_ context.Context, url string) ([]sourcedriver.OriginRef, error) {
	if err := d.alive(); err != nil {
		return nil, err
	}
	if !gitnames.OriginURL(url) {
		return nil, fmt.Errorf("origin URL: %w", sourcedriver.ErrInvalid)
	}
	w := d.w
	w.mu.Lock()
	defer w.mu.Unlock()
	o, err := w.reach(url)
	if err != nil {
		return nil, err
	}
	if len(o.refs) > sourcedriver.MaxOriginRefs {
		return nil, fmt.Errorf("the origin advertises %d refs: %w", len(o.refs), sourcedriver.ErrLimit)
	}
	refs := make([]sourcedriver.OriginRef, 0, len(o.refs))
	for name, id := range o.refs {
		refs = append(refs, sourcedriver.OriginRef{Ref: name, OID: id, Target: o.peel(id)})
	}
	slices.SortFunc(refs, func(a, b sourcedriver.OriginRef) int { return strings.Compare(a.Ref, b.Ref) })
	return refs, nil
}

// FetchOrigin copies the observed ref's objects into the project's
// repository once the origin's objects confirm the observation.
func (d *Driver) FetchOrigin(_ context.Context, project, url string, ref sourcedriver.OriginRef) (sourcedriver.OID, error) {
	if err := d.alive(); err != nil {
		return "", err
	}
	if !gitnames.OriginURL(url) || !validOriginRef(ref) {
		return "", fmt.Errorf("origin URL or ref: %w", sourcedriver.ErrInvalid)
	}
	w := d.w
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.checkStorage(); err != nil {
		return "", err
	}
	r, err := w.open(project)
	if err != nil {
		return "", err
	}
	if err := w.fetch(r, url, ref); err != nil {
		return "", err
	}
	return ref.Target, nil
}

// validOriginRef refuses an observed ref no ObserveOrigin returns.
func validOriginRef(ref sourcedriver.OriginRef) bool {
	return gitnames.OriginRef(ref.Ref) && gitnames.OID(string(ref.OID)) && gitnames.OID(string(ref.Target))
}

// ComparePublication decides the relation from the ancestry of source, a
// commit of the project's repository, and the destination's commit, which
// it copies from the origin when the two differ.
func (d *Driver) ComparePublication(_ context.Context, project, url string, source sourcedriver.OID, dest *sourcedriver.OriginRef) (sourcedriver.Relation, error) {
	if err := d.alive(); err != nil {
		return "", err
	}
	if !gitnames.OriginURL(url) || !gitnames.OID(string(source)) {
		return "", fmt.Errorf("origin URL or source: %w", sourcedriver.ErrInvalid)
	}
	if dest != nil && (!validOriginRef(*dest) || !strings.HasPrefix(dest.Ref, "refs/heads/")) {
		return "", fmt.Errorf("publication destination: %w", sourcedriver.ErrInvalid)
	}
	w := d.w
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.checkStorage(); err != nil {
		return "", err
	}
	r, err := w.open(project)
	if err != nil {
		return "", err
	}
	if o := r.objects[source]; o == nil || o.typ != commitObject {
		return "", fmt.Errorf("the source is not a commit of the project's repository: %w", sourcedriver.ErrInvalid)
	}
	switch {
	case dest == nil:
		return sourcedriver.RelationAbsent, nil
	case dest.OID != dest.Target:
		return "", fmt.Errorf("the origin destination is not a commit: %w", sourcedriver.ErrConflict)
	case dest.OID == source:
		return sourcedriver.RelationEqual, nil
	}
	if err := w.fetch(r, url, *dest); err != nil {
		return "", err
	}
	switch {
	case r.reachable(dest.OID, source):
		return sourcedriver.RelationDestinationContains, nil
	case r.reachable(source, dest.OID):
		return sourcedriver.RelationFastForward, nil
	}
	return sourcedriver.RelationDivergent, nil
}

// Publish pushes p.Commit to the origin's head as Git's receive-pack would
// without force, and answers as the bundled driver reads Git's answer.
func (d *Driver) Publish(ctx context.Context, project string, p sourcedriver.Publication, g sourcedriver.Gate) (sourcedriver.PublishStatus, error) {
	if err := d.alive(); err != nil {
		return "", err
	}
	if err := validPublication(p); err != nil {
		return "", err
	}
	// A publication is a mutation: it refuses an unsafe storage before its
	// gate, and again before its effect.
	w := d.w
	w.mu.Lock()
	err := w.checkStorage()
	w.mu.Unlock()
	if err != nil {
		return "", err
	}
	tip, exists, err := d.InspectBranch(ctx, project, p.Branch)
	if err != nil {
		return "", err
	}
	if !exists || tip != p.Commit {
		return "", fmt.Errorf("the branch %q no longer names the published commit: %w", p.Branch, sourcedriver.ErrConflict)
	}
	// The gate runs without w.mu, since core's gate may read the World.
	if err := g(); err != nil {
		return "", err
	}
	if err := d.alive(); err != nil {
		return "", err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.checkStorage(); err != nil {
		return "", err
	}
	r, err := w.open(project)
	if err != nil {
		return "", err
	}
	o, err := w.reach(p.URL)
	if err != nil {
		// The push started after the gate, and an origin that did not
		// answer may have heard it, as for the bundled driver.
		return "", fmt.Errorf("%w: %w", sourcedriver.ErrUnknownOutcome, err)
	}
	status := o.push(r, p)
	if o.loseNext {
		o.loseNext = false
		return "", fmt.Errorf("the origin's answer was lost: %w", sourcedriver.ErrUnknownOutcome)
	}
	if status == "" {
		return "", fmt.Errorf("the origin's answer does not follow the %s relation: %w", p.Relation, sourcedriver.ErrUnknownOutcome)
	}
	return status, nil
}

// validPublication refuses a publication whose values CheckNames refuses,
// or whose relation is not one a push may follow.
func validPublication(p sourcedriver.Publication) error {
	if !gitnames.OriginURL(p.URL) || !gitnames.Branch(p.Branch) || !gitnames.OID(string(p.Commit)) || !gitnames.OriginRef("refs/heads/"+p.Destination) {
		return fmt.Errorf("publication values: %w", sourcedriver.ErrInvalid)
	}
	if p.Relation != sourcedriver.RelationAbsent && p.Relation != sourcedriver.RelationFastForward {
		return fmt.Errorf("a push follows only an absent or fast-forward relation, not %q: %w", p.Relation, sourcedriver.ErrInvalid)
	}
	return nil
}

// reach is the origin at url, which a contact reaches. The caller holds
// w.mu.
func (w *World) reach(url string) (*origin, error) {
	o := w.origins[url]
	if o == nil || o.unreachable {
		return nil, fmt.Errorf("the origin %q cannot be reached: %w", url, sourcedriver.ErrUnavailable)
	}
	return o, nil
}

// fetch copies the objects of the observed ref from the origin at url into
// r once the origin confirms the observation: its ref still names ref.OID,
// which is the commit ref.Target or an annotated tag that peels to it. The
// caller holds w.mu.
func (w *World) fetch(r *repo, url string, ref sourcedriver.OriginRef) error {
	o, err := w.reach(url)
	if err != nil {
		return err
	}
	if o.refs[ref.Ref] != ref.OID {
		return fmt.Errorf("the origin's %s moved since the observation: %w", ref.Ref, sourcedriver.ErrConflict)
	}
	want := commitObject
	if ref.OID != ref.Target {
		want = tagObject
	}
	if o.typeOf(ref.OID) != want || o.peel(ref.OID) != ref.Target || o.typeOf(ref.Target) != commitObject {
		return fmt.Errorf("the origin's %s is not a commit as observed: %w", ref.Ref, sourcedriver.ErrConflict)
	}
	copyObjects(o.objects, r.objects, ref.OID)
	return nil
}

// typeOf is the type of the origin's object id; empty when it has none.
func (o *origin) typeOf(id sourcedriver.OID) objectType {
	if obj := o.objects[id]; obj != nil {
		return obj.typ
	}
	return ""
}

// peel follows tag objects from id to the object they end at.
func (o *origin) peel(id sourcedriver.OID) sourcedriver.OID {
	for obj := o.objects[id]; obj != nil && obj.typ == tagObject; obj = o.objects[id] {
		id = obj.target
	}
	return id
}

// push applies one push of p.Commit from r to the head p.Destination, as
// Git without force would, and returns the status the relation leads to:
// empty for an update the relation does not lead to, which Git's answer
// does not name either. The caller holds w.mu.
func (o *origin) push(r *repo, p sourcedriver.Publication) sourcedriver.PublishStatus {
	ref := "refs/heads/" + p.Destination
	current, exists := o.refs[ref]
	switch {
	case exists && current == p.Commit:
		return sourcedriver.PublishSatisfied
	case exists && !r.reachable(p.Commit, current):
		return sourcedriver.PublishRefused // not a fast-forward
	case o.rejectNext:
		o.rejectNext = false
		return sourcedriver.PublishRefused
	}
	copyObjects(r.objects, o.objects, p.Commit)
	o.refs[ref] = p.Commit
	switch {
	case !exists && p.Relation == sourcedriver.RelationAbsent:
		return sourcedriver.PublishCreated
	case exists && p.Relation == sourcedriver.RelationFastForward:
		return sourcedriver.PublishAdvanced
	}
	return ""
}

// copyObjects copies id and every object it reaches, through a commit's
// parents and a tag's object, from one object store to another.
func copyObjects(from, to map[sourcedriver.OID]*object, id sourcedriver.OID) {
	stack := []sourcedriver.OID{id}
	for len(stack) > 0 {
		next := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		obj := from[next]
		if obj == nil || to[next] != nil {
			continue
		}
		to[next] = obj
		stack = append(stack, obj.parents...)
		if obj.typ == tagObject {
			stack = append(stack, obj.target)
		}
	}
}
