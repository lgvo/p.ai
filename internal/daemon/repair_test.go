package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

func TestRepairKeyPreviewNeverCreatesMissingCredential(t *testing.T) {
	state := t.TempDir()
	id := "550e8400-e29b-41d4-a716-446655440000"
	dir := filepath.Join(state, "session_keys")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id)
	if _, err := inspectRegisteredSessionKey(state, id); err == nil {
		t.Fatal("missing registered key accepted")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("preview created key: %v", err)
	}
	_, pub, err := loadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := inspectRegisteredSessionKey(state, id)
	if err != nil || got != gitservice.Fingerprint(pub) {
		t.Fatalf("registered key: %s %v", got, err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectRegisteredSessionKey(state, id); err == nil {
		t.Fatal("public key file accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", path); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectRegisteredSessionKey(state, id); err == nil {
		t.Fatal("symlink key accepted")
	}
}

func TestRepairExactRuntimeGenerationAndSiblingIsolation(t *testing.T) {
	ev := control.RepairEvidence{InstanceName: "p-550e8400-e29b-41d4-a716-446655440000",
		ImageFingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		IncusUUID:        "11111111-1111-4111-8111-111111111111", Generation: "22222222-2222-4222-8222-222222222222"}
	o := runtimeincus.Observation{Exists: true, Name: ev.InstanceName, Status: "Running", Fingerprint: ev.ImageFingerprint,
		IncusUUID: ev.IncusUUID, Generation: ev.Generation, EndpointMounted: true}
	if !repairExact(o, ev, "Running") {
		t.Fatal("exact created generation refused")
	}
	for _, mutate := range []func(*runtimeincus.Observation){
		func(v *runtimeincus.Observation) { v.Name = "p-33333333-3333-4333-8333-333333333333" },
		func(v *runtimeincus.Observation) {
			v.Fingerprint = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		},
		func(v *runtimeincus.Observation) { v.IncusUUID = "33333333-3333-4333-8333-333333333333" },
		func(v *runtimeincus.Observation) { v.Generation = "33333333-3333-4333-8333-333333333333" },
		func(v *runtimeincus.Observation) { v.EndpointMounted = false },
		func(v *runtimeincus.Observation) { v.Status = "Stopped" },
	} {
		changed := o
		mutate(&changed)
		if repairExact(changed, ev, "Running") {
			t.Fatalf("replacement/sibling adopted: %+v", changed)
		}
	}
}
