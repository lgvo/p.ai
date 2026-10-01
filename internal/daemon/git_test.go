package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeServerIdentity struct{ fingerprint string }

func (f *fakeServerIdentity) GitServerIdentity(context.Context) (string, bool, error) {
	return f.fingerprint, f.fingerprint != "", nil
}
func (f *fakeServerIdentity) EnsureGitServerIdentity(_ context.Context, fingerprint string) error {
	if f.fingerprint != "" && f.fingerprint != fingerprint {
		return errors.New("server key changed")
	}
	f.fingerprint = fingerprint
	return nil
}

func TestPinnedServerKeyRestartLossAndReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server-key")
	state := new(fakeServerIdentity)
	first, pub, err := loadPinnedServerKey(context.Background(), state, path)
	if err != nil {
		t.Fatal(err)
	}
	second, samePub, err := loadPinnedServerKey(context.Background(), state, path)
	if err != nil || string(first) != string(second) || string(pub.Marshal()) != string(samePub.Marshal()) {
		t.Fatalf("restart changed key: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadPinnedServerKey(context.Background(), state, path); err == nil {
		t.Fatal("missing pinned key was regenerated")
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing key was recreated: %v", err)
	}
	if _, _, err := loadOrCreateKey(path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadPinnedServerKey(context.Background(), state, path); err == nil {
		t.Fatal("replacement server key accepted")
	}
}

func TestDaemonCancellationStopsBothListeners(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{}, 2)
	stopped := make(chan struct{}, 2)
	serve := func(ctx context.Context) error {
		started <- struct{}{}
		<-ctx.Done()
		stopped <- struct{}{}
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- runPair(ctx, serve, serve, func() {}) }()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("listener did not start")
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not stop")
	}
	for i := 0; i < 2; i++ {
		select {
		case <-stopped:
		case <-time.After(2 * time.Second):
			t.Fatal("listener did not stop")
		}
	}
}

func TestKeyPersistsAndRejectsUnsafeReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	first, public, err := loadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	second, samePublic, err := loadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) || string(public.Marshal()) != string(samePublic.Marshal()) {
		t.Fatal("key identity changed")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadOrCreateKey(path); err == nil {
		t.Fatal("accepted public private key")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/dev/null", path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadOrCreateKey(path); err == nil {
		t.Fatal("accepted symlink")
	}
}
