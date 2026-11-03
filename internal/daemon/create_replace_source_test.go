package daemon

import (
	"context"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/sourcedriver"
	sourcememory "github.com/lgvo/p.ai/internal/sourcedriver/memory"
)

// movingHead is an in-memory source driver that moves head branch to tip
// right before its moveAt-th read of that head answers, as a push would.
type movingHead struct {
	*sourcememory.Driver
	world  *sourcememory.World
	branch string
	tip    sourcedriver.OID
	moveAt int
	reads  int
}

func (d *movingHead) InspectBranch(ctx context.Context, project, name string) (sourcedriver.OID, bool, error) {
	if name == d.branch {
		if d.reads++; d.reads == d.moveAt {
			d.world.SetHead(project, name, d.tip)
		}
	}
	return d.Driver.InspectBranch(ctx, project, name)
}

func TestReplacementSourceFactsBindAbsentPreservedAndStaleRefs(t *testing.T) {
	for _, tc := range []struct {
		name, oldChoice, choice, target string
		heads                           map[string]string // branch -> commit name
		moveAt                          int
		safe                            bool
	}{
		{name: "absent-new", oldChoice: "new", choice: "new", target: "work", heads: map[string]string{}, safe: true},
		{name: "different-absent-target-preserves-old", oldChoice: "new", choice: "new", target: "other", heads: map[string]string{"work": "old"}, safe: true},
		{name: "preserved-created", oldChoice: "new", choice: "existing", target: "work", heads: map[string]string{"work": "old"}, safe: true},
		{name: "advanced-existing", oldChoice: "existing", choice: "existing", target: "work", heads: map[string]string{"work": "advanced"}, safe: true},
		{name: "unexpected-old-created", oldChoice: "new", choice: "existing", target: "work", heads: map[string]string{"work": "foreign"}},
		{name: "occupied-new-target", oldChoice: "new", choice: "new", target: "other", heads: map[string]string{"other": "foreign"}},
		// The target's third read is the one after the capture.
		{name: "target-moved-during-capture", oldChoice: "new", choice: "new", target: "other", heads: map[string]string{}, moveAt: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			world, driver := memorySource(t)
			if _, err := driver.CreateRepository(ctx, "app"); err != nil {
				t.Fatal(err)
			}
			fresh := world.Commit("app")
			world.SetHead("app", "main", fresh)
			commits := map[string]sourcedriver.OID{}
			for _, name := range []string{"old", "advanced", "foreign"} {
				commits[name] = world.Commit("app", fresh)
			}
			for branch, commit := range tc.heads {
				world.SetHead("app", branch, commits[commit])
			}
			source := &movingHead{Driver: driver, world: world, branch: tc.target, tip: commits["foreign"], moveAt: tc.moveAt}
			l := &lifecycle{git: &gitCapability{driver: source}}
			old := control.ReserveSessionRequest{Project: "app", Branch: "work", Choice: tc.oldChoice}
			req := control.ReserveSessionRequest{Project: "app", Branch: tc.target, Choice: tc.choice, Source: "refs/heads/main"}
			from := sourcedriver.Name{Kind: sourcedriver.NameBranch, Value: "main"}
			oldRef, target, captured, e := l.observeCreateReplacementSources(ctx, old, req, from, string(commits["old"]))
			if (e == nil) != tc.safe {
				t.Fatalf("safe=%v facts=%+v %+v source=%s err=%v", tc.safe, oldRef, target, captured, e)
			}
			if !oldRef.Observed || !target.Observed {
				t.Fatalf("observed exact branch facts lost: %+v %+v", oldRef, target)
			}
			if tc.safe && tc.choice == "new" && (target.Exists || captured != string(fresh)) {
				t.Fatal("absent target or fresh source not bound")
			}
			if tc.name == "occupied-new-target" && (!target.Exists || target.OID != string(commits["foreign"])) {
				t.Fatal("ineligible preview incorrectly claimed target absence")
			}
			if tip, _, err := driver.InspectBranch(ctx, "app", "work"); tc.name == "different-absent-target-preserves-old" && (err != nil || tip != commits["old"]) {
				t.Fatalf("old ref changed: %s %v", tip, err)
			}
		})
	}
}
