// git-fixture is a VM-only driver for the Git substrate. It seeds synthetic
// SQLite authority state and invokes backend operations for integration tests;
// it does not create a session runtime or implement public lifecycle methods.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"charm.land/ssh"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
	"github.com/lgvo/p.ai/internal/plugin"
	gossh "golang.org/x/crypto/ssh"
)

const schema = "p.git-fixture/v1"

type request struct {
	Schema      string                    `json:"schema"`
	Kind        string                    `json:"kind"`
	Project     string                    `json:"project,omitempty"`
	Branch      string                    `json:"branch,omitempty"`
	Choice      string                    `json:"choice,omitempty"`
	Source      string                    `json:"source,omitempty"`
	Selector    *plugin.GitSourceSelector `json:"selector,omitempty"`
	CommitOID   string                    `json:"commit_oid,omitempty"`
	Key         string                    `json:"key,omitempty"`
	PublicKey   string                    `json:"public_key,omitempty"`
	SessionUUID string                    `json:"session_uuid,omitempty"`
	OperationID string                    `json:"operation_id,omitempty"`
	After       string                    `json:"after,omitempty"`
	Limit       int                       `json:"limit,omitempty"`
}

type response struct {
	OK     bool   `json:"ok"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "git-fixture:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 1 && args[0] == "git-hook" {
		return gitservice.RunPreReceiveHook(context.Background(), os.Stdin)
	}
	if len(args) == 3 && args[0] == "probe" {
		return probe(args[1], args[2])
	}
	if len(args) == 3 && args[0] == "call" {
		return call(args[1], args[2])
	}
	if len(args) == 5 && args[0] == "serve" {
		return serve(args[1], args[2], args[3], args[4])
	}
	return errors.New("usage: git-fixture serve <private-state-dir> <trusted-activation> <loopback-host:port> <host-private-key> | call <fixture.sock> <json> | probe <loopback-host:port> <client-private-key> | git-hook")
}

// probe checks SSH request replies directly. OpenSSH can continue after a
// refused optional request, so a shell exit code alone does not prove refusal.
func probe(address, privateKeyPath string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("probe target must be numeric loopback")
	}
	key, err := os.ReadFile(privateKeyPath)
	if err != nil || len(key) > 16384 {
		return errors.New("probe private key unreadable or too large")
	}
	signer, err := gossh.ParsePrivateKey(key)
	if err != nil {
		return err
	}
	config := &gossh.ClientConfig{User: "git", Auth: []gossh.AuthMethod{gossh.PublicKeys(signer)}, HostKeyCallback: gossh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second}
	client, err := gossh.Dial("tcp", address, config)
	if err != nil {
		return err
	}
	defer client.Close()
	checks := []struct {
		name string
		call func(*gossh.Session) error
	}{
		{"env", func(s *gossh.Session) error { return s.Setenv("P_PROBE", "1") }},
		{"pty", func(s *gossh.Session) error { return s.RequestPty("xterm", 24, 80, nil) }},
		{"subsystem", func(s *gossh.Session) error { return s.RequestSubsystem("sftp") }},
		{"arbitrary exec", func(s *gossh.Session) error { return s.Run("uname") }},
	}
	for _, check := range checks {
		session, err := client.NewSession()
		if err != nil {
			return err
		}
		result := check.call(session)
		_ = session.Close()
		if result == nil {
			return fmt.Errorf("SSH %s request was accepted", check.name)
		}
	}
	forward, err := client.Listen("tcp", "127.0.0.1:0")
	if err == nil {
		forward.Close()
		return errors.New("SSH remote forwarding was accepted")
	}
	// Target the listening SSH port itself, so a server that accidentally
	// allows direct forwarding has a reachable destination and the probe fails.
	forwarded, err := client.Dial("tcp", address)
	if err == nil {
		forwarded.Close()
		return errors.New("SSH direct forwarding was accepted")
	}
	fmt.Println("P_GIT_SSH_REQUESTS_REFUSED")
	return nil
}

func readPublic(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 8192 {
		return "", errors.New("public key unreadable or too large")
	}
	key, _, _, rest, err := ssh.ParseAuthorizedKey(data)
	if err != nil || len(strings.TrimSpace(string(rest))) != 0 {
		return "", errors.New("invalid public key")
	}
	return gitservice.Fingerprint(key), nil
}

func serve(stateDir, activationPath, address, hostKeyPath string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("listener must use a numeric loopback address")
	}
	store, err := control.OpenStore(stateDir)
	if err != nil {
		return err
	}
	defer store.Close()
	active, err := plugin.LoadActivation(activationPath)
	if err != nil {
		return err
	}
	backend, err := gitservice.New(store, stateDir, active)
	if err != nil {
		return err
	}
	keyBytes, err := os.ReadFile(hostKeyPath)
	if err != nil || len(keyBytes) > 16384 {
		return errors.New("host SSH private key unreadable or too large")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()
	controlPath := filepath.Join(stateDir, "fixture.sock")
	if info, err := os.Lstat(controlPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("fixture control path is not a socket")
		}
		if err := os.Remove(controlPath); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	controlListener, err := net.Listen("unix", controlPath)
	if err != nil {
		return err
	}
	defer controlListener.Close()
	defer os.Remove(controlPath)
	if err := os.Chmod(controlPath, 0600); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	gitDone := make(chan error, 1)
	go func() {
		gitDone <- backend.Serve(ctx, listener, keyBytes)
		_ = controlListener.Close()
	}()
	go func() { <-ctx.Done(); controlListener.Close() }()
	// Serve validates the host key and installs the fixed hook before it can
	// accept clients. Fail the fixture before reporting readiness if that setup
	// exits immediately; the first control call below catches later exits.
	select {
	case err := <-gitDone:
		if err == nil {
			return errors.New("Git SSH service exited before ready")
		}
		return err
	case <-time.After(50 * time.Millisecond):
	}
	fmt.Printf("{\"schema\":\"%s\",\"listen\":%q,\"control\":%q}\n", schema, listener.Addr().String(), controlPath)
	for {
		conn, err := controlListener.Accept()
		if err != nil {
			return <-gitDone
		}
		go serveCall(ctx, conn, store, backend)
		select {
		case err := <-gitDone:
			return err
		default:
		}
	}
}

func serveCall(parent context.Context, conn net.Conn, store *control.Store, backend *gitservice.Backend) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	line, err := bufio.NewReaderSize(conn, 4097).ReadSlice('\n')
	if err != nil || len(line) > 4096 {
		_ = json.NewEncoder(conn).Encode(response{Error: "invalid fixture request"})
		return
	}
	var req request
	dec := json.NewDecoder(strings.NewReader(string(line)))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || req.Schema != schema {
		_ = json.NewEncoder(conn).Encode(response{Error: "invalid fixture schema"})
		return
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	result, err := dispatch(ctx, store, backend, req)
	if err != nil {
		_ = json.NewEncoder(conn).Encode(response{Error: err.Error()})
		return
	}
	_ = json.NewEncoder(conn).Encode(response{OK: true, Result: result})
}

func dispatch(ctx context.Context, store *control.Store, backend *gitservice.Backend, r request) (any, error) {
	switch r.Kind {
	case "ping":
		return map[string]string{"status": "ready"}, nil
	case "project":
		if err := backend.EnsureBare(ctx, r.Project); err != nil {
			return nil, err
		}
		if err := store.CreateProject(ctx, r.Project, json.RawMessage(`{}`)); err != nil {
			return nil, err
		}
		repo, err := backend.RepositoryPath(r.Project)
		if err != nil {
			return nil, err
		}
		return map[string]string{"project": r.Project, "repository_path": repo}, nil
	case "host-key":
		fingerprint, err := readPublic(r.PublicKey)
		if err != nil {
			return nil, err
		}
		if err := store.RegisterGitPrincipal(ctx, fingerprint, "host", "", ""); err != nil {
			return nil, err
		}
		return map[string]string{"fingerprint": fingerprint}, nil
	case "seed-session":
		fingerprint, err := readPublic(r.PublicKey)
		if err != nil {
			return nil, err
		}
		op, session, err := store.ReserveSession(ctx, control.ReserveSessionRequest{Key: r.Key, Project: r.Project, Branch: r.Branch, Choice: r.Choice, Source: r.Source})
		if err != nil {
			return nil, err
		}
		if err := store.AdvanceOperation(ctx, op.ID, "running", "principals-ready", true, nil, ""); err != nil {
			return nil, err
		}
		if err := store.RegisterGitPrincipal(ctx, fingerprint, "session", r.Project, session.UUID); err != nil {
			return nil, err
		}
		if err := store.AdvanceSessionRegistry(ctx, session.UUID, "creating", "established"); err != nil {
			return nil, err
		}
		if err := store.AdvanceOperation(ctx, op.ID, "completed", "established", true, nil, ""); err != nil {
			return nil, err
		}
		return map[string]string{"session_uuid": session.UUID, "operation_id": op.ID, "fingerprint": fingerprint}, nil
	case "guard", "unguard":
		if err := store.SetGitRefGuard(ctx, r.Project, r.Branch, r.OperationID, r.Kind == "guard"); err != nil {
			return nil, err
		}
		return map[string]string{"status": r.Kind}, nil
	case "revoke":
		fingerprint, err := readPublic(r.PublicKey)
		if err != nil {
			return nil, err
		}
		if err := store.RevokeGitPrincipal(ctx, fingerprint); err != nil {
			return nil, err
		}
		return map[string]string{"status": "revoked"}, nil
	case "removing":
		if err := store.AdvanceSessionRegistry(ctx, r.SessionUUID, "established", "removing"); err != nil {
			return nil, err
		}
		return map[string]string{"status": "removing"}, nil
	case "refs":
		refs, next, calls, err := backend.ListRefsPageObserved(ctx, r.Project, r.After, r.Limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{"refs": refs, "next": next, "broker_calls": calls}, nil
	case "observe-source":
		if r.Selector == nil {
			return nil, errors.New("source selector is required")
		}
		oid, err := backend.ObserveSource(ctx, r.Project, *r.Selector)
		if err != nil {
			return nil, err
		}
		return map[string]string{"commit_oid": oid}, nil
	case "create-branch":
		if err := backend.CreateBranch(ctx, r.Project, r.Branch, r.CommitOID); err != nil {
			return nil, err
		}
		return map[string]string{"branch": r.Branch, "commit_oid": r.CommitOID}, nil
	default:
		return nil, errors.New("unknown fixture operation")
	}
}

func call(socket, payload string) error {
	if len(payload) > 4095 {
		return errors.New("fixture request too large")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(conn, payload+"\n"); err != nil {
		return err
	}
	line, err := bufio.NewReaderSize(conn, 4097).ReadSlice('\n')
	if err != nil || len(line) > 4096 {
		return errors.New("invalid fixture response")
	}
	_, _ = os.Stdout.Write(line)
	var r response
	if json.Unmarshal(line, &r) != nil || !r.OK {
		return errors.New("fixture operation failed")
	}
	return nil
}
