package sshserver

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/control"
	gossh "golang.org/x/crypto/ssh"
)

// openStore is core's control state in a fresh state directory, with the
// project app.
func openStore(t *testing.T) *control.Store {
	t.Helper()
	store, err := control.OpenStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.CreateProject(t.Context(), "app", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	return store
}

// newKey is a fresh Ed25519 key: its signer and its private key as PEM.
func newKey(t *testing.T) (gossh.Signer, []byte) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return signer, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// hostKey is a key registered as the host principal, which may connect.
func hostKey(t *testing.T, store *control.Store) gossh.Signer {
	t.Helper()
	signer, _ := newKey(t)
	if err := store.RegisterGitPrincipal(t.Context(), Fingerprint(signer.PublicKey()), "host", "", ""); err != nil {
		t.Fatal(err)
	}
	return signer
}

// serve runs P's SSH server with routes on a loopback port until the test
// ends, and returns its address.
func serve(t *testing.T, store *control.Store, routes ...Route) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, pemKey := newKey(t)
	server, err := Prepare(listener, pemKey, store, routes...)
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return listener.Addr().String()
}

// dial connects to address as user with signer.
func dial(t *testing.T, address, user string, signer gossh.Signer) (*gossh.Client, error) {
	t.Helper()
	client, err := gossh.Dial("tcp", address, &gossh.ClientConfig{User: user, Auth: []gossh.AuthMethod{gossh.PublicKeys(signer)}, HostKeyCallback: gossh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second})
	if err == nil {
		t.Cleanup(func() { client.Close() })
	}
	return client, err
}

// recordingService answers every command with its output and err, and
// records the commands it served.
type recordingService struct {
	output string
	err    error

	mu    sync.Mutex
	execs []Exec
}

func (s *recordingService) Serve(_ context.Context, e Exec) error {
	s.mu.Lock()
	s.execs = append(s.execs, Exec{Command: e.Command, Fingerprint: e.Fingerprint})
	s.mu.Unlock()
	_, _ = io.WriteString(e.Out, s.output)
	return s.err
}

func (s *recordingService) served() []Exec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Exec(nil), s.execs...)
}

func TestAKeyNoPrincipalHoldsCannotConnect(t *testing.T) {
	store := openStore(t)
	address := serve(t, store, Route{Words: []string{"echo"}, Service: &recordingService{}})
	unknown, _ := newKey(t)
	if _, err := dial(t, address, "git", unknown); err == nil {
		t.Fatal("a key no principal holds connected")
	}
}

func TestAKeyConnectsOnlyAsTheGitUser(t *testing.T) {
	store := openStore(t)
	address := serve(t, store, Route{Words: []string{"echo"}, Service: &recordingService{}})
	key := hostKey(t, store)
	if _, err := dial(t, address, "root", key); err == nil {
		t.Fatal("a principal's key connected as another user")
	}
	if _, err := dial(t, address, "git", key); err != nil {
		t.Fatalf("a principal's key did not connect as git: %v", err)
	}
}

func TestEveryRequestButAnExecCommandADeclaredWordStartsIsRefused(t *testing.T) {
	store := openStore(t)
	service := &recordingService{}
	address := serve(t, store, Route{Words: []string{"git-upload-pack"}, Service: service})
	client, err := dial(t, address, "git", hostKey(t, store))
	if err != nil {
		t.Fatal(err)
	}
	for name, request := range map[string]func(*gossh.Session) error{
		"env":                          func(s *gossh.Session) error { return s.Setenv("P_PROBE", "1") },
		"pty":                          func(s *gossh.Session) error { return s.RequestPty("xterm", 24, 80, nil) },
		"subsystem":                    func(s *gossh.Session) error { return s.RequestSubsystem("sftp") },
		"subsystem of a declared word": func(s *gossh.Session) error { return s.RequestSubsystem("git-upload-pack") },
		"shell":                        func(s *gossh.Session) error { return s.Shell() },
		"undeclared command":           func(s *gossh.Session) error { return s.Start("uname") },
		"undeclared word":              func(s *gossh.Session) error { return s.Start("git-upload-archive 'app'") },
		"declared word's extension":    func(s *gossh.Session) error { return s.Start("git-upload-pack-x 'app'") },
	} {
		session, err := client.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		if request(session) == nil {
			t.Errorf("the %s request was accepted", name)
		}
		session.Close()
	}
	if forward, err := client.Listen("tcp", "127.0.0.1:0"); err == nil {
		forward.Close()
		t.Error("remote forwarding was accepted")
	}
	// The server's own address is reachable, so only the server's refusal
	// can fail this.
	if forwarded, err := client.Dial("tcp", address); err == nil {
		forwarded.Close()
		t.Error("direct forwarding was accepted")
	}
	if served := service.served(); len(served) != 0 {
		t.Fatalf("a refused request reached the service: %v", served)
	}
}

func TestAnExecCommandGoesToTheServiceThatDeclaredItsFirstWord(t *testing.T) {
	store := openStore(t)
	upload := &recordingService{output: "uploaded"}
	receive := &recordingService{output: "received"}
	address := serve(t, store,
		Route{Words: []string{"git-upload-pack"}, Service: upload},
		Route{Words: []string{"git-receive-pack"}, Service: receive})
	key := hostKey(t, store)
	client, err := dial(t, address, "git", key)
	if err != nil {
		t.Fatal(err)
	}
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	output, err := session.Output("git-receive-pack '/app'")
	if err != nil || string(output) != "received" {
		t.Fatalf("the command answered %q, %v", output, err)
	}
	want := []Exec{{Command: "git-receive-pack '/app'", Fingerprint: Fingerprint(key.PublicKey())}}
	if served := receive.served(); len(served) != 1 || served[0] != want[0] {
		t.Fatalf("the declaring service served %v, want %v", served, want)
	}
	if served := upload.served(); len(served) != 0 {
		t.Fatalf("another service served %v", served)
	}
}

func TestACommandItsServiceFailsEndsWithExitStatusOne(t *testing.T) {
	store := openStore(t)
	address := serve(t, store, Route{Words: []string{"git-upload-pack"}, Service: &recordingService{err: errors.New("the service failed")}})
	client, err := dial(t, address, "git", hostKey(t, store))
	if err != nil {
		t.Fatal(err)
	}
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	var exit *gossh.ExitError
	if err := session.Run("git-upload-pack 'app'"); !errors.As(err, &exit) || exit.ExitStatus() != 1 {
		t.Fatalf("the failed command ended with %v", err)
	}
}

func TestPrepareRefusesTwoServicesForOneWord(t *testing.T) {
	store := openStore(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, pemKey := newKey(t)
	first := Route{Words: []string{"git-upload-pack", "git-receive-pack"}, Service: &recordingService{}}
	second := Route{Words: []string{"git-receive-pack"}, Service: &recordingService{}}
	if _, err := Prepare(listener, pemKey, store, first, second); err == nil {
		t.Fatal("two services were routed one word")
	}
}
