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
	// Preserve the established-session canonical digest from before creator
	// bindings were added; absent bindings do not change existing evidence.
	if base != "f7cb10a86800cdbb8fc411fb4f2fec97da3c8de1c733782634081b14dfdb5545" {
		t.Fatalf("established loss fingerprint changed: %s", base)
	}
	originalEvidence, originalBytes := ev, snapshot.Main.Entries[1].Data
	for _, mutation := range []func(){
		func() { ev.CreatorOperationID = "77777777-7777-4777-8777-777777777777" },
		func() { ev.SourceGeneration = "55555555-5555-5555-5555-555555555555" },
		func() { ev.SourceIncusUUID = "66666666-6666-6666-6666-666666666666" },
		func() { snapshot.Main.Entries[1].Data = []byte("two") },
	} {
		mutation()
		changed, err := workspaceLossFingerprint(session, "user-1000", "p-"+session.UUID, ev, snapshot, result)
		if err != nil || changed == base {
			t.Fatalf("native generation or source bytes omitted from fingerprint: %q %v", changed, err)
		}
		ev, snapshot.Main.Entries[1].Data = originalEvidence, originalBytes
	}
}

func TestWorkspaceLossFingerprintBindsEachCreatorAuthority(t *testing.T) {
	ev := control.WorkspaceInspectEvidence{CreatorOperationID: "77777777-7777-4777-8777-777777777777",
		CreatorRequestSHA256: strings.Repeat("c", 64), CreatorEvidenceSHA256: strings.Repeat("d", 64)}
	base, err := workspaceLossFingerprint(control.Session{}, "user-1000", "source", ev, runtimeincus.WorkspaceLossSnapshot{}, workspaceLossResult{})
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*control.WorkspaceInspectEvidence){
		func(e *control.WorkspaceInspectEvidence) {
			e.CreatorOperationID = "88888888-8888-4888-8888-888888888888"
		},
		func(e *control.WorkspaceInspectEvidence) { e.CreatorRequestSHA256 = strings.Repeat("e", 64) },
		func(e *control.WorkspaceInspectEvidence) { e.CreatorEvidenceSHA256 = strings.Repeat("e", 64) },
	} {
		next := ev
		change(&next)
		changed, err := workspaceLossFingerprint(control.Session{}, "user-1000", "source", next, runtimeincus.WorkspaceLossSnapshot{}, workspaceLossResult{})
		if err != nil || changed == base {
			t.Fatalf("creator authority omitted: %s %v", changed, err)
		}
	}
}
