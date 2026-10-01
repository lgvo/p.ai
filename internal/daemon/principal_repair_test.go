package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lgvo/p.ai/internal/gitservice"
)

func TestPrincipalRepairHostKeyRotationIsExactAndRestartable(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, "session_keys")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	uuid := "550e8400-e29b-41d4-a716-446655440000"
	sibling := "660e8400-e29b-41d4-a716-446655440000"
	opID := "770e8400-e29b-41d4-a716-446655440000"
	_, oldPub, err := loadOrCreateKey(filepath.Join(dir, uuid))
	if err != nil {
		t.Fatal(err)
	}
	_, siblingPub, err := loadOrCreateKey(filepath.Join(dir, sibling))
	if err != nil {
		t.Fatal(err)
	}
	old := gitservice.Fingerprint(oldPub)
	if status, e := principalKeyStatus(state, uuid, old); e != nil || status != "matching" {
		t.Fatalf("matching key: %s %v", status, e)
	}
	_, newPub, err := loadOrCreateKey(principalRepairStage(state, opID))
	if err != nil {
		t.Fatal(err)
	}
	newFingerprint := gitservice.Fingerprint(newPub)
	if err := placePrincipalRepairKey(state, uuid, opID, newFingerprint); err != nil {
		t.Fatal(err)
	}
	if err := placePrincipalRepairKey(state, uuid, opID, newFingerprint); err != nil {
		t.Fatalf("restart did not recognize exact completed rename: %v", err)
	}
	if got, e := inspectRegisteredSessionKey(state, uuid); e != nil || got != newFingerprint {
		t.Fatalf("target identity not replaced exactly: %s %v", got, e)
	}
	if got, e := inspectRegisteredSessionKey(state, sibling); e != nil || got != gitservice.Fingerprint(siblingPub) {
		t.Fatalf("sibling key changed: %s %v", got, e)
	}
	if status, e := principalKeyStatus(state, uuid, old); e != nil || status != "mismatch" {
		t.Fatalf("old registration still appeared matched: %s %v", status, e)
	}
	if _, e := os.Lstat(principalRepairStage(state, opID)); !os.IsNotExist(e) {
		t.Fatalf("staged replacement retained after completion: %v", e)
	}
}

func TestPrincipalRepairMissingAssignedRefCannotUseHistoricalBootstrap(t *testing.T) {
	// project.create remains the recorded creation kind after main has held a
	// commit and its P ref is deleted. An absent ref therefore blocks this
	// credential repair irrespective of that historical creation row.
	status, tip, reason := principalRepairRefFacts("", false)
	if status != "missing" || tip != "" || reason != "assigned_ref_missing" {
		t.Fatalf("deleted committed main was treated as unborn: %q %q %q", status, tip, reason)
	}
	status, tip, reason = principalRepairRefFacts("0123456789012345678901234567890123456789", true)
	if status != "present" || tip != "0123456789012345678901234567890123456789" || reason != "" {
		t.Fatalf("present assigned ref refused: %q %q %q", status, tip, reason)
	}
}
