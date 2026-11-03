package conformance

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// OriginControls act on origins as their other users would: clones that
// push to them, an administrator, or the network between. Their names say
// Origin because a fixture may serve Natives from the same value.
type OriginControls interface {
	// NewOrigin makes an empty origin and returns its URL, one CheckNames
	// accepts. On the bundled driver: a bare repository reached through an
	// ssh in PATH.
	NewOrigin() string
	// OriginCommit writes a new commit into the origin, with parents the
	// origin holds, reachable from no ref, and returns its ID. Each call
	// writes a distinct commit.
	OriginCommit(url string, parents ...sourcedriver.OID) sourcedriver.OID
	// OriginTree writes an object that is not a commit, a tree, into the
	// origin and returns its ID.
	OriginTree(url string) sourcedriver.OID
	// SetOriginRef points ref, refs/heads/* or refs/tags/*, at the origin's
	// object id, or removes it when id is empty: a branch, or a lightweight
	// tag.
	SetOriginRef(url, ref string, id sourcedriver.OID)
	// OriginAnnotatedTag writes an annotated tag object of the origin's
	// object target, points refs/tags/<name> at it, and returns the tag
	// object's ID.
	OriginAnnotatedTag(url, name string, target sourcedriver.OID) sourcedriver.OID
	// OriginRef reads the origin's ref: the object it names, or empty when
	// it is absent.
	OriginRef(url, ref string) sourcedriver.OID
	// CutOffOrigin makes every later contact with the origin fail, as a
	// host or network that is down would.
	CutOffOrigin(url string)
	// RejectNextOriginPush makes the origin refuse the next push that would
	// change a ref, as a server-side hook would.
	RejectNextOriginPush(url string)
	// LoseNextOriginAnswer lets the next push land and loses the origin's
	// answer to it, as a connection dropped after the origin accepted
	// would.
	LoseNextOriginAnswer(url string)
}

// observe observes the origin through the driver and returns its refs by
// name.
func observe(t *testing.T, f Fixture, url string) map[string]sourcedriver.OriginRef {
	t.Helper()
	refs, err := f.Driver().ObserveOrigin(t.Context(), url)
	if err != nil {
		t.Fatalf("ObserveOrigin: %v", err)
	}
	byName := map[string]sourcedriver.OriginRef{}
	for _, r := range refs {
		byName[r.Ref] = r
	}
	return byName
}

// fetch brings the origin's ref, as observed now, into the project's
// repository through the driver.
func fetch(t *testing.T, f Fixture, project, url, ref string) {
	t.Helper()
	observed, ok := observe(t, f, url)[ref]
	if !ok {
		t.Fatalf("the origin does not advertise %s", ref)
	}
	if got, err := f.Driver().FetchOrigin(t.Context(), project, url, observed); err != nil || got != observed.Target {
		t.Fatalf("FetchOrigin(%s) = %q, %v; want %q", ref, got, err, observed.Target)
	}
}

// wantPresent checks that each commit is present in the project's
// repository.
func wantPresent(t *testing.T, f Fixture, project string, ids ...sourcedriver.OID) {
	t.Helper()
	for _, id := range ids {
		if c, err := f.Driver().InspectCommit(t.Context(), project, id); err != nil || !c.Present {
			t.Fatalf("InspectCommit(%s) = %+v, %v; want present", id, c, err)
		}
	}
}

// wantNoHeads checks that the project's repository has no head.
func wantNoHeads(t *testing.T, f Fixture, project string) {
	t.Helper()
	if heads, err := f.Driver().Branches(t.Context(), project); err != nil || len(heads) != 0 {
		t.Fatalf("Branches = %v, %v; want no head", heads, err)
	}
}

// wantRef checks the object the origin's ref names; empty for none.
func wantRef(t *testing.T, f Fixture, url, ref string, want sourcedriver.OID) {
	t.Helper()
	if got := f.Origin().OriginRef(url, ref); got != want {
		t.Fatalf("the origin's %s names %q, want %q", ref, got, want)
	}
}

// publishing is a project "p" and an origin whose head main names base,
// which p holds, and p's head work names source, a child of base: a
// publication of work to main is a fast-forward.
type publishing struct {
	url          string
	base, source sourcedriver.OID
}

func newPublishing(t *testing.T, f Fixture) publishing {
	t.Helper()
	o := f.Origin()
	url := o.NewOrigin()
	base := o.OriginCommit(url)
	o.SetOriginRef(url, "refs/heads/main", base)
	repository(t, f, "p")
	fetch(t, f, "p", url, "refs/heads/main")
	source := f.Natives().Commit("p", base)
	f.Natives().SetHead("p", "work", source)
	return publishing{url: url, base: base, source: source}
}

// to is the publication of work's tip to the origin's head dest.
func (p publishing) to(dest string, relation sourcedriver.Relation) sourcedriver.Publication {
	return sourcedriver.Publication{URL: p.url, Branch: "work", Commit: p.source, Destination: dest, Relation: relation}
}

var originCases = []testCase{
	{"an origin with no refs is observed as an empty answer", func(t *testing.T, f Fixture) {
		url := f.Origin().NewOrigin()
		refs, err := f.Driver().ObserveOrigin(t.Context(), url)
		if err != nil || len(refs) != 0 {
			t.Fatalf("ObserveOrigin of an empty origin = %v, %v; want no refs and no error", refs, err)
		}
	}},
	{"heads and tags are observed in byte order with their advertised IDs and peeled targets", func(t *testing.T, f Fixture) {
		o := f.Origin()
		url := o.NewOrigin()
		first := o.OriginCommit(url)
		second := o.OriginCommit(url, first)
		o.SetOriginRef(url, "refs/heads/main", second)
		o.SetOriginRef(url, "refs/heads/Zeta", first)
		o.SetOriginRef(url, "refs/tags/light", first)
		tag := o.OriginAnnotatedTag(url, "v1", second)
		refs, err := f.Driver().ObserveOrigin(t.Context(), url)
		if err != nil {
			t.Fatalf("ObserveOrigin: %v", err)
		}
		want := []sourcedriver.OriginRef{
			{Ref: "refs/heads/Zeta", OID: first, Target: first},
			{Ref: "refs/heads/main", OID: second, Target: second},
			{Ref: "refs/tags/light", OID: first, Target: first},
			{Ref: "refs/tags/v1", OID: tag, Target: second},
		}
		if !reflect.DeepEqual(refs, want) {
			t.Fatalf("ObserveOrigin = %+v, want %+v", refs, want)
		}
	}},
	{"an origin with more than MaxOriginRefs heads and tags is ErrLimit", func(t *testing.T, f Fixture) {
		o := f.Origin()
		url := o.NewOrigin()
		tip := o.OriginCommit(url)
		for i := range sourcedriver.MaxOriginRefs {
			kind := "heads"
			if i%2 == 1 {
				kind = "tags"
			}
			o.SetOriginRef(url, fmt.Sprintf("refs/%s/r%04d", kind, i), tip)
		}
		if refs, err := f.Driver().ObserveOrigin(t.Context(), url); err != nil || len(refs) != sourcedriver.MaxOriginRefs {
			t.Fatalf("ObserveOrigin of %d refs = %d refs, %v; want all of them", sourcedriver.MaxOriginRefs, len(refs), err)
		}
		o.SetOriginRef(url, "refs/heads/one-more", tip)
		if refs, err := f.Driver().ObserveOrigin(t.Context(), url); !errors.Is(err, sourcedriver.ErrLimit) || refs != nil {
			t.Fatalf("ObserveOrigin of %d refs = %d refs, %v; want ErrLimit and no refs", sourcedriver.MaxOriginRefs+1, len(refs), err)
		}
	}},
	{"a branch, a lightweight tag and an annotated tag on commits fetch their commit and history, creating no head", func(t *testing.T, f Fixture) {
		o := f.Origin()
		url := o.NewOrigin()
		parent := o.OriginCommit(url)
		branch := o.OriginCommit(url, parent)
		light := o.OriginCommit(url)
		annotated := o.OriginCommit(url)
		o.SetOriginRef(url, "refs/heads/main", branch)
		o.SetOriginRef(url, "refs/tags/light", light)
		o.OriginAnnotatedTag(url, "v1", annotated)
		repository(t, f, "p")
		refs := observe(t, f, url)
		for ref, commit := range map[string]sourcedriver.OID{"refs/heads/main": branch, "refs/tags/light": light, "refs/tags/v1": annotated} {
			for range 2 { // a repeated fetch is safe
				if got, err := f.Driver().FetchOrigin(t.Context(), "p", url, refs[ref]); err != nil || got != commit {
					t.Fatalf("FetchOrigin(%s) = %q, %v; want %q", ref, got, err, commit)
				}
			}
		}
		wantPresent(t, f, "p", parent, branch, light, annotated)
		wantNoHeads(t, f, "p")
	}},
	{"a tag whose target is not a commit is observed as advertised, and FetchOrigin refuses it", func(t *testing.T, f Fixture) {
		o := f.Origin()
		url := o.NewOrigin()
		tree := o.OriginTree(url)
		o.SetOriginRef(url, "refs/tags/light-tree", tree)
		tag := o.OriginAnnotatedTag(url, "annotated-tree", tree)
		repository(t, f, "p")
		refs := observe(t, f, url)
		want := map[string]sourcedriver.OriginRef{
			"refs/tags/annotated-tree": {Ref: "refs/tags/annotated-tree", OID: tag, Target: tree},
			"refs/tags/light-tree":     {Ref: "refs/tags/light-tree", OID: tree, Target: tree},
		}
		if !reflect.DeepEqual(refs, want) {
			t.Fatalf("ObserveOrigin = %+v, want %+v", refs, want)
		}
		for _, ref := range want {
			if got, err := f.Driver().FetchOrigin(t.Context(), "p", url, ref); !errors.Is(err, sourcedriver.ErrConflict) || got != "" {
				t.Fatalf("FetchOrigin(%s) = %q, %v; want ErrConflict", ref.Ref, got, err)
			}
		}
		wantNoHeads(t, f, "p")
	}},
	{"a fetch refuses an observation the origin's objects contradict", func(t *testing.T, f Fixture) {
		o := f.Origin()
		url := o.NewOrigin()
		commit := o.OriginCommit(url)
		other := o.OriginCommit(url)
		o.SetOriginRef(url, "refs/heads/main", commit)
		o.SetOriginRef(url, "refs/heads/other", other)
		tag := o.OriginAnnotatedTag(url, "v1", commit)
		repository(t, f, "p")
		for _, lie := range []sourcedriver.OriginRef{
			{Ref: "refs/heads/main", OID: commit, Target: other}, // a commit that is not a tag
			{Ref: "refs/tags/v1", OID: tag, Target: tag},         // a tag that is not a commit
			{Ref: "refs/tags/v1", OID: tag, Target: other},       // a tag that peels elsewhere
		} {
			if got, err := f.Driver().FetchOrigin(t.Context(), "p", url, lie); !errors.Is(err, sourcedriver.ErrConflict) || got != "" {
				t.Fatalf("FetchOrigin(%+v) = %q, %v; want ErrConflict", lie, got, err)
			}
		}
		wantNoHeads(t, f, "p")
	}},
	{"a ref that moved since the observation is ErrConflict and creates no head", func(t *testing.T, f Fixture) {
		o := f.Origin()
		url := o.NewOrigin()
		first := o.OriginCommit(url)
		o.SetOriginRef(url, "refs/heads/main", first)
		repository(t, f, "p")
		stale := observe(t, f, url)["refs/heads/main"]
		moved := o.OriginCommit(url, first)
		o.SetOriginRef(url, "refs/heads/main", moved)
		if got, err := f.Driver().FetchOrigin(t.Context(), "p", url, stale); !errors.Is(err, sourcedriver.ErrConflict) || got != "" {
			t.Fatalf("FetchOrigin of a moved ref = %q, %v; want ErrConflict", got, err)
		}
		wantNoHeads(t, f, "p")
		fetch(t, f, "p", url, "refs/heads/main")
		wantPresent(t, f, "p", moved)
	}},
	{"an unreachable origin is ErrUnavailable, never an answer", func(t *testing.T, f Fixture) {
		p := newPublishing(t, f)
		main := observe(t, f, p.url)["refs/heads/main"]
		f.Origin().CutOffOrigin(p.url)
		if refs, err := f.Driver().ObserveOrigin(t.Context(), p.url); !errors.Is(err, sourcedriver.ErrUnavailable) || refs != nil {
			t.Fatalf("ObserveOrigin of an unreachable origin = %v, %v; want ErrUnavailable", refs, err)
		}
		if got, err := f.Driver().FetchOrigin(t.Context(), "p", p.url, main); !errors.Is(err, sourcedriver.ErrUnavailable) || got != "" {
			t.Fatalf("FetchOrigin from an unreachable origin = %q, %v; want ErrUnavailable", got, err)
		}
		if got, err := f.Driver().ComparePublication(t.Context(), "p", p.url, p.source, &main); !errors.Is(err, sourcedriver.ErrUnavailable) || got != "" {
			t.Fatalf("ComparePublication with an unreachable origin = %q, %v; want ErrUnavailable", got, err)
		}
		// The push starts after the gate, so the driver cannot tell whether
		// the origin heard it.
		if got, err := f.Driver().Publish(t.Context(), "p", p.to("main", sourcedriver.RelationFastForward), func() error { return nil }); err == nil || got != "" {
			t.Fatalf("Publish to an unreachable origin = %q, %v; want an error and no answer", got, err)
		}
	}},
	{"no destination is absent and an equal one is equal, without contacting the origin", func(t *testing.T, f Fixture) {
		p := newPublishing(t, f)
		f.Origin().SetOriginRef(p.url, "refs/heads/same", p.base)
		same := observe(t, f, p.url)["refs/heads/same"]
		f.Origin().CutOffOrigin(p.url)
		if got, err := f.Driver().ComparePublication(t.Context(), "p", p.url, p.source, nil); err != nil || got != sourcedriver.RelationAbsent {
			t.Fatalf("ComparePublication with no destination = %q, %v; want absent", got, err)
		}
		if got, err := f.Driver().ComparePublication(t.Context(), "p", p.url, p.base, &same); err != nil || got != sourcedriver.RelationEqual {
			t.Fatalf("ComparePublication with the source at the destination = %q, %v; want equal", got, err)
		}
	}},
	{"the destination's ancestry decides destination_contains, fast_forward and divergent, creating no ref", func(t *testing.T, f Fixture) {
		o := f.Origin()
		url := o.NewOrigin()
		base := o.OriginCommit(url)
		source := o.OriginCommit(url, base)
		child := o.OriginCommit(url, source)
		divergent := o.OriginCommit(url, base)
		o.SetOriginRef(url, "refs/heads/source", source)
		o.SetOriginRef(url, "refs/heads/child", child)
		o.SetOriginRef(url, "refs/heads/base", base)
		o.SetOriginRef(url, "refs/heads/divergent", divergent)
		repository(t, f, "p")
		fetch(t, f, "p", url, "refs/heads/source")
		refs := observe(t, f, url)
		for dest, want := range map[string]sourcedriver.Relation{
			"refs/heads/child":     sourcedriver.RelationDestinationContains,
			"refs/heads/base":      sourcedriver.RelationFastForward,
			"refs/heads/divergent": sourcedriver.RelationDivergent,
		} {
			ref := refs[dest]
			if got, err := f.Driver().ComparePublication(t.Context(), "p", url, source, &ref); err != nil || got != want {
				t.Fatalf("ComparePublication with %s = %q, %v; want %q", dest, got, err, want)
			}
		}
		wantNoHeads(t, f, "p")
	}},
	{"a source that is not a commit of the project's repository is ErrInvalid", func(t *testing.T, f Fixture) {
		p := newPublishing(t, f)
		absent := sourcedriver.OID("1111111111111111111111111111111111111111")
		for _, source := range []sourcedriver.OID{f.Natives().Tree("p"), absent} {
			if got, err := f.Driver().ComparePublication(t.Context(), "p", p.url, source, nil); !errors.Is(err, sourcedriver.ErrInvalid) || got != "" {
				t.Fatalf("ComparePublication of source %s = %q, %v; want ErrInvalid", source, got, err)
			}
		}
	}},
	{"a destination that is not a commit at a head as observed is refused", func(t *testing.T, f Fixture) {
		p := newPublishing(t, f)
		o := f.Origin()
		tag := o.OriginAnnotatedTag(p.url, "v1", p.base)
		main := observe(t, f, p.url)["refs/heads/main"]
		peeled := sourcedriver.OriginRef{Ref: "refs/heads/main", OID: tag, Target: p.base}
		if got, err := f.Driver().ComparePublication(t.Context(), "p", p.url, p.source, &peeled); !errors.Is(err, sourcedriver.ErrConflict) || got != "" {
			t.Fatalf("ComparePublication with a destination whose ID is not its target = %q, %v; want ErrConflict", got, err)
		}
		release := observe(t, f, p.url)["refs/tags/v1"]
		if got, err := f.Driver().ComparePublication(t.Context(), "p", p.url, p.source, &release); !errors.Is(err, sourcedriver.ErrInvalid) || got != "" {
			t.Fatalf("ComparePublication with a tag as destination = %q, %v; want ErrInvalid", got, err)
		}
		o.SetOriginRef(p.url, "refs/heads/main", o.OriginCommit(p.url, p.base))
		if got, err := f.Driver().ComparePublication(t.Context(), "p", p.url, p.source, &main); !errors.Is(err, sourcedriver.ErrConflict) || got != "" {
			t.Fatalf("ComparePublication with a destination that moved = %q, %v; want ErrConflict", got, err)
		}
	}},
	{"publishing to an absent destination creates it at the commit, after the gate", func(t *testing.T, f Fixture) {
		p := newPublishing(t, f)
		g := &gate{before: func() { wantRef(t, f, p.url, "refs/heads/new", "") }}
		if got, err := f.Driver().Publish(t.Context(), "p", p.to("new", sourcedriver.RelationAbsent), g.call); err != nil || got != sourcedriver.PublishCreated || g.count() != 1 {
			t.Fatalf("Publish = %q, %v after %d gate calls; want created after 1", got, err, g.count())
		}
		wantRef(t, f, p.url, "refs/heads/new", p.source)
	}},
	{"publishing a fast-forward advances the destination, after the gate", func(t *testing.T, f Fixture) {
		p := newPublishing(t, f)
		g := &gate{before: func() { wantRef(t, f, p.url, "refs/heads/main", p.base) }}
		if got, err := f.Driver().Publish(t.Context(), "p", p.to("main", sourcedriver.RelationFastForward), g.call); err != nil || got != sourcedriver.PublishAdvanced || g.count() != 1 {
			t.Fatalf("Publish = %q, %v after %d gate calls; want advanced after 1", got, err, g.count())
		}
		wantRef(t, f, p.url, "refs/heads/main", p.source)
	}},
	{"a destination that already names the commit is satisfied", func(t *testing.T, f Fixture) {
		p := newPublishing(t, f)
		if got, err := f.Driver().Publish(t.Context(), "p", p.to("copy", sourcedriver.RelationAbsent), func() error { return nil }); err != nil || got != sourcedriver.PublishCreated {
			t.Fatalf("Publish = %q, %v; want created", got, err)
		}
		f.Origin().SetOriginRef(p.url, "refs/heads/main", p.source) // another clone published it meanwhile
		if got, err := f.Driver().Publish(t.Context(), "p", p.to("main", sourcedriver.RelationFastForward), func() error { return nil }); err != nil || got != sourcedriver.PublishSatisfied {
			t.Fatalf("Publish to a destination at the commit = %q, %v; want satisfied", got, err)
		}
		wantRef(t, f, p.url, "refs/heads/main", p.source)
	}},
	{"a push the origin rejects is refused and leaves the destination", func(t *testing.T, f Fixture) {
		p := newPublishing(t, f)
		o := f.Origin()
		diverged := o.OriginCommit(p.url, p.base)
		o.SetOriginRef(p.url, "refs/heads/main", diverged) // the destination diverged after the comparison
		if got, err := f.Driver().Publish(t.Context(), "p", p.to("main", sourcedriver.RelationFastForward), func() error { return nil }); err != nil || got != sourcedriver.PublishRefused {
			t.Fatalf("Publish of what is no longer a fast-forward = %q, %v; want refused", got, err)
		}
		wantRef(t, f, p.url, "refs/heads/main", diverged)
		o.RejectNextOriginPush(p.url)
		if got, err := f.Driver().Publish(t.Context(), "p", p.to("new", sourcedriver.RelationAbsent), func() error { return nil }); err != nil || got != sourcedriver.PublishRefused {
			t.Fatalf("Publish the origin rejects = %q, %v; want refused", got, err)
		}
		wantRef(t, f, p.url, "refs/heads/new", "")
	}},
	{"a push whose answer is lost after it landed is ErrUnknownOutcome", func(t *testing.T, f Fixture) {
		p := newPublishing(t, f)
		f.Origin().LoseNextOriginAnswer(p.url)
		g := &gate{}
		if got, err := f.Driver().Publish(t.Context(), "p", p.to("new", sourcedriver.RelationAbsent), g.call); !errors.Is(err, sourcedriver.ErrUnknownOutcome) || got != "" || g.count() != 1 {
			t.Fatalf("Publish whose answer was lost = %q, %v after %d gate calls; want ErrUnknownOutcome after 1", got, err, g.count())
		}
		wantRef(t, f, p.url, "refs/heads/new", p.source)
	}},
	{"a failing gate pushes nothing", func(t *testing.T, f Fixture) {
		p := newPublishing(t, f)
		g := &gate{err: errGate}
		if got, err := f.Driver().Publish(t.Context(), "p", p.to("new", sourcedriver.RelationAbsent), g.call); !errors.Is(err, errGate) || got != "" {
			t.Fatalf("Publish with a failing gate = %q, %v; want the gate's error", got, err)
		}
		wantRef(t, f, p.url, "refs/heads/new", "")
	}},
	{"a source branch that no longer names the commit is ErrConflict, without the gate or a push", func(t *testing.T, f Fixture) {
		p := newPublishing(t, f)
		f.Natives().SetHead("p", "work", f.Natives().Commit("p", p.source))
		if got, err := f.Driver().Publish(t.Context(), "p", p.to("new", sourcedriver.RelationAbsent), unexpectedGate(t)); !errors.Is(err, sourcedriver.ErrConflict) || got != "" {
			t.Fatalf("Publish of a moved branch = %q, %v; want ErrConflict", got, err)
		}
		f.Natives().SetHead("p", "work", "")
		if got, err := f.Driver().Publish(t.Context(), "p", p.to("new", sourcedriver.RelationAbsent), unexpectedGate(t)); !errors.Is(err, sourcedriver.ErrConflict) || got != "" {
			t.Fatalf("Publish of a deleted branch = %q, %v; want ErrConflict", got, err)
		}
		wantRef(t, f, p.url, "refs/heads/new", "")
	}},
	{"only an absent or fast-forward relation is published", func(t *testing.T, f Fixture) {
		p := newPublishing(t, f)
		for _, relation := range []sourcedriver.Relation{sourcedriver.RelationEqual, sourcedriver.RelationDestinationContains, sourcedriver.RelationDivergent, ""} {
			if got, err := f.Driver().Publish(t.Context(), "p", p.to("main", relation), unexpectedGate(t)); !errors.Is(err, sourcedriver.ErrInvalid) || got != "" {
				t.Fatalf("Publish of relation %q = %q, %v; want ErrInvalid", relation, got, err)
			}
		}
		wantRef(t, f, p.url, "refs/heads/main", p.base)
	}},
	{"a project with no repository is ErrNotFound", func(t *testing.T, f Fixture) {
		p := newPublishing(t, f)
		main := observe(t, f, p.url)["refs/heads/main"]
		if got, err := f.Driver().FetchOrigin(t.Context(), "q", p.url, main); !errors.Is(err, sourcedriver.ErrNotFound) || got != "" {
			t.Fatalf("FetchOrigin into no repository = %q, %v; want ErrNotFound", got, err)
		}
		if got, err := f.Driver().ComparePublication(t.Context(), "q", p.url, p.base, &main); !errors.Is(err, sourcedriver.ErrNotFound) || got != "" {
			t.Fatalf("ComparePublication in no repository = %q, %v; want ErrNotFound", got, err)
		}
		if got, err := f.Driver().Publish(t.Context(), "q", p.to("new", sourcedriver.RelationAbsent), unexpectedGate(t)); !errors.Is(err, sourcedriver.ErrNotFound) || got != "" {
			t.Fatalf("Publish from no repository = %q, %v; want ErrNotFound", got, err)
		}
	}},
	{"an invalid URL, ref or commit ID is ErrInvalid before the origin is contacted", func(t *testing.T, f Fixture) {
		p := newPublishing(t, f)
		main := observe(t, f, p.url)["refs/heads/main"]
		// Contact would answer ErrUnavailable: only a refusal before it
		// answers ErrInvalid.
		f.Origin().CutOffOrigin(p.url)
		for _, url := range []string{"", "/tmp/origin.git", "file:///tmp/origin.git", "ext::sh -c touch% /tmp/pwned", "git@host:-oProxyCommand=x"} {
			if refs, err := f.Driver().ObserveOrigin(t.Context(), url); !errors.Is(err, sourcedriver.ErrInvalid) || refs != nil {
				t.Fatalf("ObserveOrigin(%q) = %v, %v; want ErrInvalid", url, refs, err)
			}
			if got, err := f.Driver().FetchOrigin(t.Context(), "p", url, main); !errors.Is(err, sourcedriver.ErrInvalid) || got != "" {
				t.Fatalf("FetchOrigin from %q = %q, %v; want ErrInvalid", url, got, err)
			}
			if got, err := f.Driver().ComparePublication(t.Context(), "p", url, p.source, &main); !errors.Is(err, sourcedriver.ErrInvalid) || got != "" {
				t.Fatalf("ComparePublication with %q = %q, %v; want ErrInvalid", url, got, err)
			}
			publication := p.to("new", sourcedriver.RelationAbsent)
			publication.URL = url
			if got, err := f.Driver().Publish(t.Context(), "p", publication, unexpectedGate(t)); !errors.Is(err, sourcedriver.ErrInvalid) || got != "" {
				t.Fatalf("Publish to %q = %q, %v; want ErrInvalid", url, got, err)
			}
		}
		for _, ref := range []sourcedriver.OriginRef{
			{Ref: "refs/remotes/origin/main", OID: main.OID, Target: main.Target},
			{Ref: "refs/heads/x?evil", OID: main.OID, Target: main.Target},
			{Ref: main.Ref, OID: "main", Target: main.Target},
			{Ref: main.Ref, OID: main.OID, Target: "0000000000000000000000000000000000000000"},
		} {
			if got, err := f.Driver().FetchOrigin(t.Context(), "p", p.url, ref); !errors.Is(err, sourcedriver.ErrInvalid) || got != "" {
				t.Fatalf("FetchOrigin(%+v) = %q, %v; want ErrInvalid", ref, got, err)
			}
			if got, err := f.Driver().ComparePublication(t.Context(), "p", p.url, p.source, &ref); !errors.Is(err, sourcedriver.ErrInvalid) || got != "" {
				t.Fatalf("ComparePublication with destination %+v = %q, %v; want ErrInvalid", ref, got, err)
			}
		}
		if got, err := f.Driver().ComparePublication(t.Context(), "p", p.url, "HEAD", &main); !errors.Is(err, sourcedriver.ErrInvalid) || got != "" {
			t.Fatalf("ComparePublication of source HEAD = %q, %v; want ErrInvalid", got, err)
		}
		for _, bad := range []sourcedriver.Publication{
			{URL: p.url, Branch: "../work", Commit: p.source, Destination: "new", Relation: sourcedriver.RelationAbsent},
			{URL: p.url, Branch: "work", Commit: "HEAD", Destination: "new", Relation: sourcedriver.RelationAbsent},
			{URL: p.url, Branch: "work", Commit: p.source, Destination: "a:refs/heads/b", Relation: sourcedriver.RelationAbsent},
			{URL: p.url, Branch: "work", Commit: p.source, Destination: "", Relation: sourcedriver.RelationAbsent},
		} {
			if got, err := f.Driver().Publish(t.Context(), "p", bad, unexpectedGate(t)); !errors.Is(err, sourcedriver.ErrInvalid) || got != "" {
				t.Fatalf("Publish(%+v) = %q, %v; want ErrInvalid", bad, got, err)
			}
		}
	}},
}
