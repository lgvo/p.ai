package daemon

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

func TestDiscardAcceptsTheReviewedContainerWhateverItsRunState(t *testing.T) {
	ev := control.DiscardEvidence{InstanceName: "p-550e8400-e29b-41d4-a716-446655440000",
		IncusUUID: "22222222-2222-4222-8222-222222222222", Generation: "33333333-3333-4333-8333-333333333333", ImageFingerprint: strings.Repeat("c", 64)}
	reviewed := runtimeincus.Observation{Exists: true, Name: ev.InstanceName, IncusUUID: ev.IncusUUID, Generation: ev.Generation, Fingerprint: ev.ImageFingerprint}
	for _, status := range []string{"Running", "Stopped"} {
		o := reviewed
		o.Status = status
		if !discardSourceMatches(o, ev) {
			t.Fatalf("a %s container started or stopped after its preview was refused", status)
		}
	}
	for name, change := range map[string]func(*runtimeincus.Observation){
		"replaced":       func(o *runtimeincus.Observation) { o.IncusUUID = "44444444-4444-4444-8444-444444444444" },
		"restored":       func(o *runtimeincus.Observation) { o.Generation = "44444444-4444-4444-8444-444444444444" },
		"rebuilt":        func(o *runtimeincus.Observation) { o.Fingerprint = strings.Repeat("d", 64) },
		"renamed":        func(o *runtimeincus.Observation) { o.Name = "p-other" },
		"gone":           func(o *runtimeincus.Observation) { o.Exists = false },
		"frozen outside": func(o *runtimeincus.Observation) { o.Status = "Frozen" },
	} {
		o := reviewed
		o.Status = "Running"
		change(&o)
		if discardSourceMatches(o, ev) {
			t.Fatalf("a %s container was taken for the reviewed one", name)
		}
	}
}

func TestDiscardPrivateKeyCleanupIsExactAndDoesNotFollowLinks(t *testing.T) {
	const oldID = "550e8400-e29b-41d4-a716-446655440000"
	const siblingID = "11111111-1111-4111-8111-111111111111"
	root := t.TempDir()
	dir := filepath.Join(root, "session_keys")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, oldID)
	sibling := filepath.Join(dir, siblingID)
	if err := os.WriteFile(old, []byte("dummy old credential"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sibling, []byte("dummy sibling credential"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := removeSessionKeyAt(root, oldID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(old); !os.IsNotExist(err) {
		t.Fatalf("old key still present: %v", err)
	}
	if got, err := os.ReadFile(sibling); err != nil || string(got) != "dummy sibling credential" {
		t.Fatalf("sibling changed: %q %v", got, err)
	}
	if err := removeSessionKeyAt(root, oldID); err != nil {
		t.Fatalf("exact replay refused: %v", err)
	}
	if err := os.Symlink(sibling, old); err != nil {
		t.Fatal(err)
	}
	if err := removeSessionKeyAt(root, oldID); err == nil {
		t.Fatal("symlinked old key accepted")
	}
	if got, err := os.ReadFile(sibling); err != nil || string(got) != "dummy sibling credential" {
		t.Fatalf("symlink refusal changed sibling: %q %v", got, err)
	}
}

func TestDiscardEndpointCleanupSurvivesRestartAndPreservesSibling(t *testing.T) {
	const oldID = "550e8400-e29b-41d4-a716-446655440000"
	const siblingID = "11111111-1111-4111-8111-111111111111"
	prefix, err := os.MkdirTemp("/tmp", "p-d-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(prefix) })
	if err := os.Chmod(prefix, 0700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{oldID, siblingID} {
		dir := filepath.Join(prefix, id)
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"git.sock", "session.sock"} {
			path := filepath.Join(dir, name)
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			listener.(*net.UnixListener).SetUnlinkOnClose(false)
			if err := os.Chmod(path, 0666); err != nil {
				t.Fatal(err)
			}
			// A daemon restart closes the listener but leaves the socket entry.
			if err := listener.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
	m := &endpointManager{prefix: prefix, opened: map[string]*endpointPair{}}
	if err := m.RemoveSession(oldID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(prefix, oldID)); !os.IsNotExist(err) {
		t.Fatalf("old endpoint remains: %v", err)
	}
	for _, name := range []string{"git.sock", "session.sock"} {
		if _, err := os.Lstat(filepath.Join(prefix, siblingID, name)); err != nil {
			t.Fatalf("sibling endpoint changed: %s %v", name, err)
		}
	}
	if err := m.RemoveSession(oldID); err != nil {
		t.Fatalf("cleanup replay: %v", err)
	}
}
