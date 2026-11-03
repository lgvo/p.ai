package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lgvo/p.ai/internal/api"
)

// TestEveryDiagnosticWriterKeepsOneLimit writes an over-long diagnostic
// through each store method that takes one and reads back at most
// api.MaxDiagnosticBytes, cut on a character boundary.
func TestEveryDiagnosticWriterKeepsOneLimit(t *testing.T) {
	ctx := context.Background()
	long := strings.Repeat("é", api.MaxDiagnosticBytes) // two bytes each
	check := func(what, got string) {
		t.Helper()
		if len(got) == 0 || len(got) > api.MaxDiagnosticBytes || !utf8.ValidString(got) || !strings.HasPrefix(long, got) {
			t.Errorf("%s kept %d bytes (valid UTF-8 %t)", what, len(got), utf8.ValidString(got))
		}
	}
	s, _, op := setupDiscardStore(t)
	if err := s.AdvanceOperation(ctx, op.ID, "blocked", op.Phase, op.Committed, op.Evidence, api.ReasonRuntime, long); err != nil {
		t.Fatal(err)
	}
	blocked, err := s.GetOperation(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	check("AdvanceOperation", blocked.Diagnostic)
	if summary := SummarizeOperation(blocked); summary.Diagnostic != blocked.Diagnostic || summary.Reason != api.ReasonRuntime {
		t.Errorf("the summary differs from the operation: %q %q", summary.Reason, summary.Diagnostic)
	}
	if err = s.FailStaleDiscard(ctx, op.ID, long); err != nil {
		t.Fatal(err)
	}
	stale, err := s.GetOperation(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	check("FailStaleDiscard", stale.Diagnostic)
	if stale.Status != "failed" || stale.Reason != api.ReasonChanged {
		t.Errorf("a stale removal recorded %s with reason %q", stale.Status, stale.Reason)
	}
	if err = s.SetSessionStartDiagnostic(ctx, op.SessionUUID, long); err != nil {
		t.Fatal(err)
	}
	start, err := s.SessionStartDiagnostic(ctx, op.SessionUUID)
	if err != nil {
		t.Fatal(err)
	}
	check("SetSessionStartDiagnostic", start)
}

// TestTheOtherDiagnosticWritersKeepOneLimit covers the writers outside the
// discard fixture: a Rename's precommit failure, a stale assembled cleanup,
// a recovered panic, and project deletion with its resource diagnostics.
func TestTheOtherDiagnosticWritersKeepOneLimit(t *testing.T) {
	ctx := context.Background()
	long := strings.Repeat("é", api.MaxDiagnosticBytes)
	bounded := func(what string, s *Store, id, reason string) {
		t.Helper()
		op, err := s.GetOperation(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if op.Reason != reason || len(op.Diagnostic) == 0 || len(op.Diagnostic) > api.MaxDiagnosticBytes || !utf8.ValidString(op.Diagnostic) {
			t.Errorf("%s recorded reason %q and %d diagnostic bytes", what, op.Reason, len(op.Diagnostic))
		}
	}
	rs, _, r, rev := renameStoreFixture(t)
	rename, err := rs.BeginRename(ctx, r, rev, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err = rs.FailRenamePrecommit(ctx, rename.ID, api.ReasonWorkspaceMismatch, long); err != nil {
		t.Fatal(err)
	}
	bounded("FailRenamePrecommit", rs, rename.ID, api.ReasonWorkspaceMismatch)

	as, _, _, aev := assembledCleanupFixture(t)
	cleanup := beginAssembledCleanup(t, as, aev)
	if err = as.AdvanceOperation(ctx, cleanup.ID, "blocked", "stale-cleanup", false, cleanup.Evidence, api.ReasonChanged, "stale"); err != nil {
		t.Fatal(err)
	}
	cleanup, _ = as.GetOperation(ctx, cleanup.ID)
	if err = as.FailStaleAssembledCleanup(ctx, cleanup, long); err != nil {
		t.Fatal(err)
	}
	bounded("FailStaleAssembledCleanup", as, cleanup.ID, api.ReasonChanged)

	ds, _, running := setupDiscardStore(t)
	if _, err = ds.BlockAfterPanic(ctx, running.ID, long); err != nil {
		t.Fatal(err)
	}
	bounded("BlockAfterPanic", ds, running.ID, api.ReasonInternal)

	ps, _, req, pev := projectDeleteFixture(t)
	deletion := acceptProjectDelete(t, ps, req, pev)
	next := pev
	next.Resources = append([]ProjectDeleteResource(nil), pev.Resources...)
	next.Resources[0].Status, next.Resources[0].Diagnostic = "unreachable", api.BoundDiagnostic(long)
	if err = ps.UpdateProjectDelete(ctx, deletion, next, "blocked", api.ReasonRuntime, long); err != nil {
		t.Fatal(err)
	}
	bounded("UpdateProjectDelete", ps, deletion.ID, api.ReasonRuntime)
	deletion, _ = ps.GetOperation(ctx, deletion.ID)
	over := next
	over.Resources = append([]ProjectDeleteResource(nil), next.Resources...)
	over.Resources[0].Diagnostic = long
	if err = ps.UpdateProjectDelete(ctx, deletion, over, "blocked", api.ReasonRuntime, ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("a resource diagnostic over the limit was accepted: %v", err)
	}
}

func TestOnlyStoppedOperationsRecordAReason(t *testing.T) {
	ctx := context.Background()
	s, _, op := setupDiscardStore(t)
	for _, test := range []struct{ status, reason string }{
		{"blocked", ""}, {"failed", ""}, {"running", api.ReasonRuntime}, {"completed", api.ReasonRuntime}, {"blocked", "Not A Code"}, {"running", "Not A Code"},
	} {
		if err := s.AdvanceOperation(ctx, op.ID, test.status, op.Phase, op.Committed, op.Evidence, test.reason, ""); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s with reason %q was accepted: %v", test.status, test.reason, err)
		}
	}
	if err := s.AdvanceOperation(ctx, op.ID, "blocked", op.Phase, op.Committed, op.Evidence, api.ReasonRuntime, "Incus did not answer"); err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceOperation(ctx, op.ID, "running", op.Phase, op.Committed, op.Evidence, "", ""); err != nil {
		t.Fatal(err)
	}
	if retried, err := s.GetOperation(ctx, op.ID); err != nil || retried.Reason != "" || retried.Diagnostic != "" {
		t.Fatalf("a retried operation kept its reason: %+v %v", retried, err)
	}
}

// TestProjectDeleteEvidenceFitsAtTheDiagnosticLimit checks that the largest
// evidence a deletion can be confirmed with still fits its bound once every
// resource carries a diagnostic at the limit, plain or escaped, and that the
// operation then fits a response frame. Every name is as long as P accepts.
func TestProjectDeleteEvidenceFitsAtTheDiagnosticLimit(t *testing.T) {
	_, _, _, ev := projectDeleteFixture(t)
	project := strings.Repeat("p", 100) + "/" + strings.Repeat("q", 100) + "/" + strings.Repeat("r", 53)
	for len(ev.Sessions) < 4 {
		ps := ev.Sessions[0]
		ps.Session.UUID = fmt.Sprintf("%08d-0000-4000-8000-000000000000", len(ev.Sessions))
		ev.Sessions = append(ev.Sessions, ps)
	}
	for i := range ev.Sessions {
		ev.Sessions[i].Session.Project, ev.Sessions[i].Session.Branch = project, strings.Repeat("b", 200)
	}
	withResources := func(images int, diagnostic string) ProjectDeleteEvidence {
		e := ev
		e.Images = nil
		for len(e.Images) < images {
			claim := ev.Images[0]
			img := claim.Image
			img.Key.Project, img.Key.Commit, img.Key.BuilderVersion = project, fmt.Sprintf("%064d", len(e.Images)), strings.Repeat("v", 128)
			img.Ref, img.Labels = fmt.Sprintf("%064x", len(e.Images)), img.Key.Labels(img.Build)
			claim.Image = img
			e.Images = append(e.Images, claim)
		}
		e.Resources = nil
		for _, ps := range e.Sessions {
			e.Resources = append(e.Resources, ProjectDeleteResource{Kind: "runtime", ID: ps.Session.UUID}, ProjectDeleteResource{Kind: "local", ID: ps.Session.UUID})
		}
		for _, c := range e.Images {
			e.Resources = append(e.Resources, ProjectDeleteResource{Kind: "image", ID: c.Image.Ref})
		}
		e.Resources = append(e.Resources, ProjectDeleteResource{Kind: "repository", ID: "bare"})
		for i := range e.Resources {
			e.Resources[i].Status, e.Resources[i].DeleteIssued, e.Resources[i].Diagnostic = "unreachable", true, diagnostic
		}
		return e
	}
	// The most images whose confirmed evidence fits its bound: the review
	// refuses more (ProjectDeleteReviewFits), whatever MaxProjectDeleteImages
	// allows.
	images := 0
	for images < MaxProjectDeleteImages && ProjectDeleteReviewFits(withResources(images+1, "")) {
		images++
	}
	t.Logf("%d images fit the confirmed evidence", images)
	// Each of four sessions started from its own commit keeps one image.
	if images < len(ev.Sessions) {
		t.Fatalf("only %d images fit the evidence of %d sessions", images, len(ev.Sessions))
	}
	var raw []byte
	// Plain text, then characters JSON escapes into six bytes each.
	for _, detail := range []string{strings.Repeat("d", api.MaxDiagnosticBytes), strings.Repeat("<", api.MaxDiagnosticBytes)} {
		encoded, err := json.Marshal(withResources(images, ResourceDiagnostic(detail)))
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) > maxProjectDeleteEvidenceBytes {
			t.Fatalf("evidence with %d images and diagnostics at the limit takes %d bytes, over its bound of %d", images, len(encoded), maxProjectDeleteEvidenceBytes)
		}
		if len(encoded) > len(raw) {
			raw = encoded
		}
	}
	// The store accepts a resource diagnostic only as ResourceDiagnostic
	// bounds it.
	escaped := strings.Repeat("<", api.MaxDiagnosticBytes)
	_, _, _, valid := projectDeleteFixture(t)
	valid.Resources[0].Status, valid.Resources[0].Diagnostic = "unreachable", ResourceDiagnostic(escaped)
	if !validProjectDeleteEvidence(valid) {
		t.Error("evidence with a bounded resource diagnostic is not valid")
	}
	valid.Resources[0].Diagnostic = escaped
	if validProjectDeleteEvidence(valid) {
		t.Error("evidence whose resource diagnostic encodes over the limit is valid")
	}
	op := api.Operation{ID: strings.Repeat("o", 36), Key: strings.Repeat("k", 128), Kind: KindProjectDelete, Project: strings.Repeat("p", 255),
		Request: json.RawMessage(`{"key":"` + strings.Repeat("k", 128) + `"}`), Status: "blocked", Phase: "ensure-absent", Committed: true,
		Evidence: raw, Reason: api.ReasonRuntime, Diagnostic: strings.Repeat("d", api.MaxDiagnosticBytes)}
	answer, err := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("1"), Result: api.OperationResult{Operation: op}})
	if err != nil || len(answer)+1 > lifecyclePageResultCeiling {
		t.Fatalf("operation.inspect of the largest deletion takes %d bytes: %v", len(answer), err)
	}
}
