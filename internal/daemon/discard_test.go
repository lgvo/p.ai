package daemon

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

func TestDiscardAcceptsTheReviewedRuntimeWhateverItsRunState(t *testing.T) {
	reviewed := recordedIncarnation("22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333")
	for _, state := range []runtimedriver.State{runtimedriver.StateRunning, runtimedriver.StateStopped} {
		if !discardSourceMatches(runtimedriver.Observation{State: state, Incarnation: reviewed}, reviewed) {
			t.Fatalf("a %s runtime started or stopped after its preview was refused", state)
		}
	}
	for name, o := range map[string]runtimedriver.Observation{
		"replaced": {State: runtimedriver.StateRunning,
			Incarnation: recordedIncarnation("44444444-4444-4444-8444-444444444444", "33333333-3333-4333-8333-333333333333")},
		"restored": {State: runtimedriver.StateRunning,
			Incarnation: recordedIncarnation("22222222-2222-4222-8222-222222222222", "44444444-4444-4444-8444-444444444444")},
		"gone":           {State: runtimedriver.StateAbsent},
		"frozen outside": {State: runtimedriver.StateOther, Incarnation: reviewed},
	} {
		if discardSourceMatches(o, reviewed) {
			t.Fatalf("a %s runtime was taken for the reviewed one", name)
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
