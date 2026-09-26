package gitservice

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"
)

type hookRequest struct {
	Token string `json:"token"`
	Ref   string `json:"ref"`
	Old   string `json:"old"`
	New   string `json:"new"`
}

// startHookApproval exposes one private callback for this receive-pack only.
// Its closure runs under the parent receive lease held by Backend.handle.
func (b *Backend) startHookApproval(ctx context.Context, fingerprint, project, ref string) (path, token string, closeHook func(), err error) {
	var random [24]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", "", nil, err
	}
	token = hex.EncodeToString(random[:])
	path = filepath.Join(b.stateDir, "git-hooks", "h-"+token[:16]+".sock")
	if len(path) >= 100 {
		return "", "", nil, errors.New("Git hook endpoint path too long")
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return "", "", nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		listener.Close()
		os.Remove(path)
		return "", "", nil, err
	}
	done := make(chan struct{})
	closeHook = func() { listener.Close(); <-done; os.Remove(path) }
	go func() {
		defer close(done)
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		line, readErr := bufio.NewReaderSize(conn, 1025).ReadSlice('\n')
		if readErr != nil || len(line) > 1024 {
			_, _ = io.WriteString(conn, "deny\n")
			return
		}
		var request hookRequest
		if json.Unmarshal(line, &request) != nil || request.Token != token || request.Ref != ref || !validOID(request.Old) || !validOID(request.New) || allZero(request.New) || ctx.Err() != nil {
			_, _ = io.WriteString(conn, "deny\n")
			return
		}
		if allZero(request.Old) {
			allowed, claimErr := b.store.ConsumeGitUnbornGrant(ctx, fingerprint, project)
			if claimErr != nil || !allowed {
				_, _ = io.WriteString(conn, "deny\n")
				return
			}
		}
		_, _ = io.WriteString(conn, "ok\n")
	}()
	return path, token, closeHook, nil
}

func requestHookApproval(ctx context.Context, ref, old, new string) error {
	path, token := os.Getenv("P_GIT_HOOK_SOCKET"), os.Getenv("P_GIT_HOOK_TOKEN")
	if path == "" || len(token) != 48 {
		return errors.New("Git hook scope missing")
	}
	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := json.NewEncoder(conn).Encode(hookRequest{Token: token, Ref: ref, Old: old, New: new}); err != nil {
		return err
	}
	response := make([]byte, 3)
	if _, err := io.ReadFull(conn, response); err != nil || string(response) != "ok\n" {
		return errors.New("Git hook update refused")
	}
	return nil
}
