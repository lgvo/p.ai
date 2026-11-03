package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/sourcedriver"
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

func TestSourceDriverStartsOnlyWithItsOwnConfigOverPrivateStorage(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(config string) *control.GitConfig {
		return &control.GitConfig{Driver: control.SourceDriverConfig{Name: control.GitDriver, Config: json.RawMessage(config)}}
	}
	if _, _, err := newSourceDriver(t.Context(), control.HostConfig{StateDir: state, Git: git(`{"unknown":1}`)}); err == nil {
		t.Fatal("the driver started with a setting it does not know")
	}
	if _, _, err := newSourceDriver(t.Context(), control.HostConfig{StateDir: state, Git: git(`{}`)}); err != nil {
		t.Fatalf("the driver did not start over private storage: %v", err)
	}
	if err := os.Chmod(state, 0770); err != nil {
		t.Fatal(err)
	}
	if _, _, err := newSourceDriver(t.Context(), control.HostConfig{StateDir: state, Git: git(`{}`)}); err == nil {
		t.Fatal("the driver started over storage others can write")
	}
}

// gitHostConfig is host configuration with P's Git endpoint on a free
// loopback port and the bundled source driver over store's state directory.
func gitHostConfig(store *control.Store) control.HostConfig {
	return control.HostConfig{StateDir: store.StateDir(), Git: &control.GitConfig{Listen: "127.0.0.1:0", Driver: control.SourceDriverConfig{Name: control.GitDriver, Config: json.RawMessage(`{}`)}}}
}

func TestTheDaemonRefusesToStartWithAnotherBackendWhileAProjectExists(t *testing.T) {
	store, err := control.OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.PinSourceBackend(t.Context(), "forge"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateProject(t.Context(), "app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	git, err := newGitCapability(t.Context(), gitHostConfig(store), store)
	if err == nil {
		git.close()
	}
	if !errors.Is(err, control.ErrConflict) {
		t.Fatalf("the daemon started the git backend over another backend's project: %v", err)
	}
}

func TestTheDaemonStartsAgainOverTheProjectsItsBackendMade(t *testing.T) {
	store, err := control.OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first, err := newGitCapability(t.Context(), gitHostConfig(store), store)
	if err != nil {
		t.Fatal(err)
	}
	first.close()
	if err := store.CreateProject(t.Context(), "app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	second, err := newGitCapability(t.Context(), gitHostConfig(store), store)
	if err != nil {
		t.Fatalf("the daemon did not start again over its backend's project: %v", err)
	}
	second.close()
}

func TestClosingTheGitCapabilityEndsTheDriversGitService(t *testing.T) {
	store, err := control.OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	git, err := newGitCapability(t.Context(), gitHostConfig(store), store)
	if err != nil {
		t.Fatal(err)
	}
	git.close()
	opened := false
	open := func(sourcedriver.ServiceKind, string) (sourcedriver.Service, sourcedriver.Approve, error) {
		opened = true
		return sourcedriver.Service{}, nil, errors.New("refused")
	}
	err = git.driver.ServeGit(t.Context(), "git-upload-pack 'app'", sourcedriver.Stream{In: strings.NewReader(""), Out: io.Discard, Err: io.Discard}, open)
	if !errors.Is(err, sourcedriver.ErrUnavailable) || opened {
		t.Fatalf("a closed capability's driver served Git: %v, opened %v", err, opened)
	}
}
