package conformance

import (
	"maps"
	"testing"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

var inventoryCases = []testCase{
	{"inventory lists a session's runtime with its reference, labels, incarnation and state", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		r := running(t, f, s)
		o, found := inventoried(t, f, func(o runtimedriver.Object) bool { return o.Ref == s.Ref })
		if !found {
			t.Fatal("Inventory has no object carrying the session's reference")
		}
		if o.Labels[runtimedriver.LabelInstance] == "" || !maps.Equal(withoutInstance(o.Labels), s.Labels) ||
			o.Incarnation != r.Incarnation || o.State != runtimedriver.StateRunning {
			t.Fatalf("Inventory lists %+v, want labels %v and the instance's at %s, running", o, s.Labels, r.Incarnation)
		}
	}},
	{"inventory states the scope's capacity", func(t *testing.T, f Fixture) {
		inv, err := f.Driver().Inventory(t.Context())
		if err != nil {
			t.Fatalf("Inventory: %v", err)
		}
		if want := f.Inventory().Capacity(); inv.Capacity != want {
			t.Fatalf("Inventory states capacity %d, want %d", inv.Capacity, want)
		}
	}},
	{"inventory lists objects that are not this instance's, with their references and labels as stored", func(t *testing.T, f Fixture) {
		own := f.Session(SessionOptions{})
		created(t, f, own)
		unlabeled := f.Inventory().AddUnlabeled()
		foreignRef := f.Session(SessionOptions{}).Ref
		foreignLabels := runtimedriver.Labels{runtimedriver.LabelProject: "another-instance-project"}
		foreign := f.Inventory().AddOtherInstance(foreignRef, foreignLabels)
		o, found := inventoried(t, f, func(o runtimedriver.Object) bool { return o.Name == unlabeled })
		if !found || o.Ref != (runtimedriver.Ref{}) || len(o.Labels) != 0 {
			t.Fatalf("the unlabeled object is listed as %+v (found %t), want no reference and no labels", o, found)
		}
		mine, _ := inventoried(t, f, func(o runtimedriver.Object) bool { return o.Ref == own.Ref })
		o, found = inventoried(t, f, func(o runtimedriver.Object) bool { return o.Name == foreign })
		if !found || o.Ref != foreignRef || !maps.Equal(withoutInstance(o.Labels), foreignLabels) ||
			o.Labels[runtimedriver.LabelInstance] == "" || o.Labels[runtimedriver.LabelInstance] == mine.Labels[runtimedriver.LabelInstance] {
			t.Fatalf("another instance's object is listed as %+v (found %t), want %s %s labeled %v by an instance other than %q",
				o, found, foreignRef.Kind, foreignRef.ID, foreignLabels, mine.Labels[runtimedriver.LabelInstance])
		}
	}},
	{"a runtime renamed away from its address is listed with its reference", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		at := created(t, f, s).Incarnation
		before, _ := inventoried(t, f, func(o runtimedriver.Object) bool { return o.Ref == s.Ref })
		f.Sessions().RenameAway(s.Ref)
		o, found := inventoried(t, f, func(o runtimedriver.Object) bool { return o.Ref == s.Ref })
		if !found || o.Name == before.Name || o.Incarnation != at {
			t.Fatalf("the renamed runtime is listed as %+v (found %t), want it under a new name at %s", o, found, at)
		}
	}},
	{"a terminating runtime is present", func(t *testing.T, f Fixture) {
		s := f.Session(SessionOptions{})
		created(t, f, s)
		f.Inventory().Terminate(s.Ref)
		if _, found := inventoried(t, f, func(o runtimedriver.Object) bool { return o.Ref == s.Ref }); !found {
			t.Fatal("Inventory omits a terminating runtime")
		}
		if o, err := f.Driver().Inspect(t.Context(), s); err == nil && o.State == runtimedriver.StateAbsent {
			t.Fatal("Inspect reports a terminating runtime absent")
		}
		if _, err := f.Driver().Create(t.Context(), s, "", unexpectedGate(t)); err == nil {
			t.Fatal("Create succeeded while the session's runtime is terminating")
		}
	}},
	{"a partial inventory is an error, never a list", func(t *testing.T, f Fixture) {
		created(t, f, f.Session(SessionOptions{}))
		f.Inventory().PartialInventory()
		if inv, err := f.Driver().Inventory(t.Context()); err == nil {
			t.Fatalf("Inventory over a partial listing = %+v, want an error", inv)
		}
	}},
}

// inventoried finds the first object of a fresh inventory that match
// accepts.
func inventoried(t *testing.T, f Fixture, match func(runtimedriver.Object) bool) (runtimedriver.Object, bool) {
	t.Helper()
	inv, err := f.Driver().Inventory(t.Context())
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	for _, o := range inv.Objects {
		if match(o) {
			return o, true
		}
	}
	return runtimedriver.Object{}, false
}

// withoutInstance is labels without LabelInstance: the labels core gave.
func withoutInstance(labels runtimedriver.Labels) runtimedriver.Labels {
	given := maps.Clone(labels)
	delete(given, runtimedriver.LabelInstance)
	return given
}
