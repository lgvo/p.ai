package daemon

import (
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

func TestWorkspaceLossFingerprintBindsNativeGenerationAndSourceBytes(t *testing.T) {
	session := control.Session{UUID: "11111111-1111-1111-1111-111111111111", Project: "team/app", Branch: "main", PolicySHA256: strings.Repeat("a", 64)}
	ev := control.WorkspaceInspectEvidence{InstanceUUID: "22222222-2222-2222-2222-222222222222",
		ImageFingerprint: strings.Repeat("b", 64), OriginalStatus: "Running",
		SourceIncusUUID: "33333333-3333-3333-3333-333333333333", SourceGeneration: "44444444-4444-4444-4444-444444444444"}
	snapshot := runtimeincus.WorkspaceLossSnapshot{Main: runtimeincus.WorkspaceSnapshot{Entries: []runtimeincus.WorkspaceEntry{
		{Path: "", Type: "directory", Mode: 0755}, {Path: "tracked", Type: "file", Mode: 0644, Data: []byte("one")},
	}}}
	result := workspaceLossResult{Schema: "p.workspace-loss/v1", RuntimeDataWillBeRemoved: true, ExternalWorktrees: []string{}}
	base, err := workspaceLossFingerprint(session, "user-1000", "p-"+session.UUID, ev, snapshot, result)
	if err != nil || len(base) != 64 {
		t.Fatalf("base fingerprint: %q %v", base, err)
	}
	for _, mutation := range []func(){
		func() { ev.SourceGeneration = "55555555-5555-5555-5555-555555555555" },
		func() { ev.SourceIncusUUID = "66666666-6666-6666-6666-666666666666" },
		func() { snapshot.Main.Entries[1].Data = []byte("two") },
	} {
		mutation()
		changed, err := workspaceLossFingerprint(session, "user-1000", "p-"+session.UUID, ev, snapshot, result)
		if err != nil || changed == base {
			t.Fatalf("native generation or source bytes omitted from fingerprint: %q %v", changed, err)
		}
	}
}
