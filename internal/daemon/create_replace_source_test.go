package daemon

import (
	"context"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
)

type replacementSourceProbe struct {
	refs        map[string]string
	captured    string
	inspections int
	moveAt      int
}

func (p *replacementSourceProbe) InspectBranchRef(_ context.Context, _ string, branch string) (string, bool, error) {
	p.inspections++
	if p.inspections == p.moveAt {
		p.refs[branch] = "changed"
	}
	oid := p.refs[branch]
	return oid, oid != "", nil
}
func (p *replacementSourceProbe) CaptureLocalSource(_ context.Context, req control.ReserveSessionRequest) (control.CapturedSource, error) {
	oid := p.refs[req.Branch]
	if req.Choice == "existing" {
		if oid == "" {
			return control.CapturedSource{}, control.ErrConflict
		}
		return control.CapturedSource{OID: oid, Existed: true}, nil
	}
	if oid != "" {
		return control.CapturedSource{}, control.ErrConflict
	}
	return control.CapturedSource{OID: p.captured}, nil
}
func TestReplacementSourceFactsBindAbsentPreservedAndStaleRefs(t *testing.T) {
	for _, tc := range []struct {
		name, oldChoice, choice, target string
		refs                            map[string]string
		moveAt                          int
		safe                            bool
	}{
		{name: "absent-new", oldChoice: "new", choice: "new", target: "work", refs: map[string]string{}, safe: true},
		{name: "different-absent-target-preserves-old", oldChoice: "new", choice: "new", target: "other", refs: map[string]string{"work": "old"}, safe: true},
		{name: "preserved-created", oldChoice: "new", choice: "existing", target: "work", refs: map[string]string{"work": "old"}, safe: true},
		{name: "advanced-existing", oldChoice: "existing", choice: "existing", target: "work", refs: map[string]string{"work": "advanced"}, safe: true},
		{name: "unexpected-old-created", oldChoice: "new", choice: "existing", target: "work", refs: map[string]string{"work": "foreign"}},
		{name: "occupied-new-target", oldChoice: "new", choice: "new", target: "other", refs: map[string]string{"other": "foreign"}},
		{name: "target-moved-during-capture", oldChoice: "new", choice: "new", target: "other", refs: map[string]string{}, moveAt: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := &replacementSourceProbe{refs: tc.refs, captured: "fresh", moveAt: tc.moveAt}
			old := control.ReserveSessionRequest{Project: "app", Branch: "work", Choice: tc.oldChoice}
			req := control.ReserveSessionRequest{Project: "app", Branch: tc.target, Choice: tc.choice, Source: "refs/heads/main"}
			oldRef, target, source, e := observeCreateReplacementSources(context.Background(), probe, old, req, "old")
			if (e == nil) != tc.safe {
				t.Fatalf("safe=%v facts=%+v %+v source=%s err=%v", tc.safe, oldRef, target, source, e)
			}
			if !oldRef.Observed || !target.Observed {
				t.Fatalf("observed exact branch facts lost: %+v %+v", oldRef, target)
			}
			if tc.safe && tc.choice == "new" && (target.Exists || source != "fresh") {
				t.Fatal("absent target or fresh source not bound")
			}
			if tc.name == "occupied-new-target" && (!target.Exists || target.OID != "foreign") {
				t.Fatal("ineligible preview incorrectly claimed target absence")
			}
			if tc.name == "different-absent-target-preserves-old" && probe.refs["work"] != "old" {
				t.Fatal("old ref changed")
			}
		})
	}
}
