package control

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
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
	for _, selection := range []CreationSelection{{}, {HostID: "host", HostSHA256: "invalid", SourceID: "source", SourceSHA256: strings.Repeat("c", 64)}, func() CreationSelection { v := testSelection(); v.AgentID = "agent"; return v }()} {
		raw, _ := json.Marshal(CreationEvidence{Selection: selection})
		if e = s.AdvanceOperation(ctx, op.ID, "blocked", "reserved", false, raw, "internal", ""); e != nil {
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
	if e = s.AdvanceOperation(ctx, op.ID, "blocked", "reserved", false, raw, "internal", ""); e != nil {
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
	if e = s.PutCommitImage(ctx, testCommitImage("app", ev.CapturedOID)); e != nil {
		t.Fatal(e)
	}
	if e = s.CheckPluginRemovalDependencies(ctx, strings.Repeat("f", 64)); e != nil {
		t.Fatal("an indexed commit image blocked package removal", e)
	}
	// An unfinished origin change and a publication past its push boundary
	// use no package: the source driver does their origin work.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, r := range []operationRecord{
		{ID: "origin", Key: "origin", Kind: KindOriginChange, Project: "app", Request: []byte(`{}`), Phase: "accepted", Now: now},
		{ID: "publication", Key: "publication", Kind: KindOriginPublish, Project: "app", Request: []byte(`{}`), Phase: "push-issued", Committed: true, Now: now},
	} {
		if e = insertOperation(ctx, s, r); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.CheckPluginRemovalDependencies(ctx, strings.Repeat("f", 64)); e != nil {
		t.Fatal("pending origin work blocked package removal", e)
	}
}
