package conformance

import (
	"context"
	"errors"
	"testing"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// SettlingControls interrupt head effects.
type SettlingControls interface {
	// DieAfterHeadEffect makes the driver die right after its next head
	// effect lands, before it reads anything back or answers: that call
	// returns an error. On the bundled driver: a seam after Git's ref
	// update fails the call.
	DieAfterHeadEffect()
}

// dieAtGate is a gate that kills the driver: the call's context ends and
// the fixture restarts, so the call passes its gate and stops there, as if
// the driver's process died. The fixture's driver is a new one afterwards.
func dieAtGate(f Fixture) (context.Context, sourcedriver.Gate) {
	ctx, cancel := context.WithCancel(context.Background())
	return ctx, func() error {
		cancel()
		f.Restart()
		return nil
	}
}

// settle settles e through the fixture's driver, twice, and checks that both
// answers agree, since Settle answers from native state only.
func settle(t *testing.T, f Fixture, e sourcedriver.Effect) sourcedriver.Settlement {
	t.Helper()
	first, err := f.Driver().Settle(t.Context(), e)
	if err != nil {
		t.Fatalf("Settle(%+v): %v", e, err)
	}
	f.Restart()
	again, err := f.Driver().Settle(t.Context(), e)
	if err != nil || again != first {
		t.Fatalf("Settle after another restart = %v, %v; want %v again", again, err, first)
	}
	return first
}

func creation(project, branch string, at sourcedriver.OID) sourcedriver.Effect {
	return sourcedriver.Effect{Kind: sourcedriver.EffectCreateBranch, Project: project, Branch: branch, New: at}
}

func deletion(project, branch string, tip sourcedriver.OID) sourcedriver.Effect {
	return sourcedriver.Effect{Kind: sourcedriver.EffectDeleteBranch, Project: project, Branch: branch, Old: tip}
}

var settlingCases = []testCase{
	{"a creation killed after its gate, before its effect, settles not applied", func(t *testing.T, f Fixture) {
		repository(t, f, "p")
		at := f.Natives().Commit("p")
		ctx, g := dieAtGate(f)
		if err := f.Driver().CreateBranch(ctx, "p", "work", at, g); err == nil {
			t.Fatal("CreateBranch killed at its gate succeeded")
		}
		if got := settle(t, f, creation("p", "work", at)); got != sourcedriver.NotApplied {
			t.Fatalf("Settle = %v, want not-applied", got)
		}
		if _, ok := inspectBranch(t, f, "p", "work"); ok {
			t.Fatal("settling created the head")
		}
	}},
	{"a creation killed after its effect landed settles applied", func(t *testing.T, f Fixture) {
		repository(t, f, "p")
		at := f.Natives().Commit("p")
		f.Settling().DieAfterHeadEffect()
		if err := f.Driver().CreateBranch(t.Context(), "p", "work", at, func() error { return nil }); err == nil {
			t.Fatal("CreateBranch that died after its effect succeeded")
		}
		f.Restart()
		if got := settle(t, f, creation("p", "work", at)); got != sourcedriver.Applied {
			t.Fatalf("Settle = %v, want applied", got)
		}
		if got, ok := inspectBranch(t, f, "p", "work"); !ok || got != at {
			t.Fatalf("after settling the head is %q, %v; want %q", got, ok, at)
		}
	}},
	{"a deletion killed after its gate, before its effect, settles not applied", func(t *testing.T, f Fixture) {
		tip := withHead(t, f, "p", "work")
		ctx, g := dieAtGate(f)
		if err := f.Driver().DeleteBranch(ctx, "p", "work", tip, g); err == nil {
			t.Fatal("DeleteBranch killed at its gate succeeded")
		}
		if got := settle(t, f, deletion("p", "work", tip)); got != sourcedriver.NotApplied {
			t.Fatalf("Settle = %v, want not-applied", got)
		}
		if got, ok := inspectBranch(t, f, "p", "work"); !ok || got != tip {
			t.Fatal("settling deleted the head")
		}
	}},
	{"a deletion killed after its effect landed settles applied", func(t *testing.T, f Fixture) {
		tip := withHead(t, f, "p", "work")
		f.Settling().DieAfterHeadEffect()
		if err := f.Driver().DeleteBranch(t.Context(), "p", "work", tip, func() error { return nil }); err == nil {
			t.Fatal("DeleteBranch that died after its effect succeeded")
		}
		f.Restart()
		if got := settle(t, f, deletion("p", "work", tip)); got != sourcedriver.Applied {
			t.Fatalf("Settle = %v, want applied", got)
		}
		if _, ok := inspectBranch(t, f, "p", "work"); ok {
			t.Fatal("after settling the head is back")
		}
	}},
	{"a head in neither the prior nor the intended state settles unknown", func(t *testing.T, f Fixture) {
		tip := withHead(t, f, "p", "work")
		other := f.Natives().Commit("p", tip)
		f.Natives().SetHead("p", "elsewhere", other)
		f.Natives().SetSymbolic("p", "alias", "work")
		for what, e := range map[string]sourcedriver.Effect{
			"a creation whose head is at another commit": creation("p", "elsewhere", tip),
			"a deletion whose head moved":                deletion("p", "elsewhere", tip),
			"a creation whose name holds a symbolic ref": creation("p", "alias", tip),
			"a deletion whose name holds a symbolic ref": deletion("p", "alias", tip),
		} {
			if got := settle(t, f, e); got != sourcedriver.Unknown {
				t.Errorf("Settle of %s = %v, want unknown", what, got)
			}
		}
	}},
	{"settling a head effect of a project without a repository is an error", func(t *testing.T, f Fixture) {
		if got, err := f.Driver().Settle(t.Context(), creation("p", "work", missingCommit)); !errors.Is(err, sourcedriver.ErrNotFound) {
			t.Fatalf("Settle without a repository = %v, %v; want ErrNotFound", got, err)
		}
	}},
	{"a head effect that does not name its states is refused", func(t *testing.T, f Fixture) {
		tip := withHead(t, f, "p", "work")
		for what, e := range map[string]sourcedriver.Effect{
			"a creation from a head":     {Kind: sourcedriver.EffectCreateBranch, Project: "p", Branch: "work", Old: tip, New: tip},
			"a creation of nothing":      {Kind: sourcedriver.EffectCreateBranch, Project: "p", Branch: "work"},
			"a deletion to a commit":     {Kind: sourcedriver.EffectDeleteBranch, Project: "p", Branch: "work", Old: tip, New: tip},
			"a deletion of no head":      {Kind: sourcedriver.EffectDeleteBranch, Project: "p", Branch: "work"},
			"an effect of an empty name": {Kind: sourcedriver.EffectDeleteBranch, Project: "p", Old: tip},
		} {
			if got, err := f.Driver().Settle(t.Context(), e); !errors.Is(err, sourcedriver.ErrInvalid) {
				t.Errorf("Settle of %s = %v, %v; want ErrInvalid", what, got, err)
			}
		}
	}},
}
