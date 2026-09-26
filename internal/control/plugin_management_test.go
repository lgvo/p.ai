package control

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestPluginRemovalDurableDependenciesReopenAndUnknownProvenance(t *testing.T) {
	s, dir := openTestStore(t)
	ctx := context.Background()
	digest := strings.Repeat("a", 64)
	if e := s.CheckPluginRemovalDependencies(ctx, digest); e != nil {
		t.Fatal(e)
	}
	if e := s.CreateProject(ctx, "app", json.RawMessage(`{}`)); e != nil {
		t.Fatal(e)
	}
	op, session, e := s.ReserveSession(ctx, ReserveSessionRequest{Key: "creator", Project: "app", Branch: "main", Choice: "new", Source: "refs/heads/seed"})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.CheckPluginRemovalDependencies(ctx, digest); e == nil {
		t.Fatal("unknown session selection permitted removal")
	}
	for _, selection := range []CreationSelection{{}, {RuntimeID: "runtime", RuntimeSHA256: "invalid", HostID: "host", HostSHA256: strings.Repeat("b", 64), SourceID: "source", SourceSHA256: strings.Repeat("c", 64)}, func() CreationSelection { v := testSelection(); v.AgentID = "agent"; return v }()} {
		raw, _ := json.Marshal(CreationEvidence{Selection: selection})
		if e = s.AdvanceOperation(ctx, op.ID, "blocked", "reserved", false, raw, ""); e != nil {
			t.Fatal(e)
		}
		if e = s.CheckPluginRemovalDependencies(ctx, strings.Repeat("f", 64)); e == nil {
			t.Fatal("empty/malformed live creator selection permitted removal")
		}
	}
	selection := testSelection()
	selection.AgentID = "example.agent"
	selection.AgentSHA256 = digest
	ev := CreationEvidence{Selection: selection, ImageFingerprint: strings.Repeat("d", 64), PolicySHA256: session.PolicySHA256, CapturedOID: strings.Repeat("e", 40), RuntimeInitState: "attempted"}
	raw, _ := json.Marshal(ev)
	if e = s.AdvanceOperation(ctx, op.ID, "blocked", "reserved", false, raw, ""); e != nil {
		t.Fatal(e)
	}
	s.Close()
	s, e = testScopedOpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.CheckPluginRemovalDependencies(ctx, digest); e == nil {
		t.Fatal("reopened blocked creator lost dependency")
	}
	if e = s.CompleteCreation(ctx, op.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.CheckPluginRemovalDependencies(ctx, digest); e == nil {
		t.Fatal("completed creator with live assets permitted removal")
	}
	if e = s.CheckPluginRemovalDependencies(ctx, strings.Repeat("f", 64)); e != nil {
		t.Fatal("unrelated package blocked", e)
	}
	image := testEnvironmentImage("app")
	if e = s.PutEnvironmentImage(ctx, image); e != nil {
		t.Fatal(e)
	}
	if e = s.CheckPluginRemovalDependencies(ctx, strings.Repeat("f", 64)); e == nil {
		t.Fatal("cache unknown module provenance guessed safe")
	}
	image, _, e = s.GetEnvironmentImage(ctx, image.Project, image.Key)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.DeleteEnvironmentImageExact(ctx, image); e != nil {
		t.Fatal(e)
	}
	for _, q := range []string{`INSERT INTO origin_requests(idempotency_key,project_path,kind,expected_url,proposed_url,status,created_at) VALUES('origin','app','set','','ssh://example/repo','prepared','now')`, `INSERT INTO publication_requests(idempotency_key,project_path,expected_origin_url,source_kind,source,source_oid,destination_ref,status,created_at) VALUES('publication','app','ssh://example/repo','retained','refs/heads/main','oid','refs/heads/main','attempted','now')`} {
		if _, e = s.db.Exec(q); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.CheckPluginRemovalDependencies(ctx, strings.Repeat("f", 64)); e == nil {
		t.Fatal("pending external recovery permitted removal")
	}
	if _, e = s.db.Exec(`UPDATE origin_requests SET status='completed'`); e != nil {
		t.Fatal(e)
	}
	if _, e = s.db.Exec(`UPDATE publication_requests SET status='completed'`); e != nil {
		t.Fatal(e)
	}
	if e = s.CheckPluginRemovalDependencies(ctx, strings.Repeat("f", 64)); e != nil {
		t.Fatal(e)
	}
}
