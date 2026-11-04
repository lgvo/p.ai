// Package sshserver is P's SSH server: the endpoint behind each session's
// git.sock. It authenticates a connection's key against core's Git
// principals, refuses every request but an exec command, and hands the
// authenticated channel to the service that declared the command's first
// word. Git, served by the source driver, is the first service and for now
// the only one; the server itself holds no service's command word.
package sshserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"charm.land/ssh"
	"charm.land/wish/v2"
	"github.com/lgvo/p.ai/internal/control"
	gossh "golang.org/x/crypto/ssh"
)

const (
	// Timeout is the longest a connection lasts, whatever it does; the
	// daemon's proxy bounds a session's raw transport with it too.
	Timeout = 10 * time.Minute
	// idleTimeout ends a connection that has sent or received nothing for
	// that long.
	idleTimeout = 2 * time.Minute
	// maxChannels bounds the session channels the server serves at once,
	// over every connection.
	maxChannels = 16
	// user is the SSH user every key connects as.
	user = "git"
)

// Service serves the exec commands whose first word it declared.
type Service interface {
	// Serve serves one exec command on a channel the server authenticated.
	// ctx ends when the channel's connection does. The command ends with
	// exit status 0 when Serve returns nil and 1 otherwise.
	Serve(ctx context.Context, e Exec) error
}

// Exec is one exec command on an authenticated channel.
type Exec struct {
	Command string // the whole command, its first word included
	// Fingerprint is the storage identity of the key that authenticated
	// the connection.
	Fingerprint string
	In          io.Reader
	Out, Err    io.Writer
}

// Route hands the exec commands whose first word is one of Words to
// Service.
type Route struct {
	Words   []string
	Service Service
}

// Server is P's SSH server on one listener.
type Server struct {
	ssh      *ssh.Server
	listener net.Listener
	services map[string]Service // by command word
	channels chan struct{}
}

// Prepare makes the server on listener, a loopback TCP listener, with
// hostKey, a PEM private key; principals decides which keys may connect.
// It refuses two routes for one word. Once it succeeds, Serve can accept
// clients at once.
func Prepare(listener net.Listener, hostKey []byte, principals *control.Store, routes ...Route) (*Server, error) {
	if listener == nil || len(hostKey) == 0 || principals == nil {
		return nil, control.ErrInvalid
	}
	if tcp, ok := listener.Addr().(*net.TCPAddr); !ok || tcp.IP == nil || !tcp.IP.IsLoopback() {
		return nil, errors.New("P's SSH listener must be loopback")
	}
	services := map[string]Service{}
	for _, r := range routes {
		for _, word := range r.Words {
			if word == "" || strings.ContainsAny(word, " \t\r\n") {
				return nil, fmt.Errorf("%q is not a command word", word)
			}
			if services[word] != nil {
				return nil, fmt.Errorf("two services declare the command word %q", word)
			}
			services[word] = r.Service
		}
	}
	srv, err := wish.NewServer(
		wish.WithHostKeyPEM(hostKey),
		wish.WithPublicKeyAuth(func(auth ssh.Context, key ssh.PublicKey) bool {
			return auth.User() == user && key != nil && principals.IsGitPrincipalActive(auth, Fingerprint(key))
		}),
		wish.WithMaxTimeout(Timeout),
		wish.WithIdleTimeout(idleTimeout),
	)
	if err != nil {
		return nil, err
	}
	s := &Server{ssh: srv, listener: listener, services: services, channels: make(chan struct{}, maxChannels)}
	// The standard session handler acknowledges env and PTY requests before
	// a middleware can refuse them, so a narrow handler of P's own refuses
	// them on the wire. With no other channel, subsystem or global request
	// handler, forwarding is refused too.
	srv.ChannelHandlers = map[string]ssh.ChannelHandler{"session": s.sessionChannel}
	srv.SubsystemHandlers = map[string]ssh.SubsystemHandler{}
	srv.RequestHandlers = map[string]ssh.RequestHandler{}
	return s, nil
}

// Serve accepts clients until ctx ends, then closes the listener and every
// connection.
func (s *Server) Serve(ctx context.Context) error {
	stop := context.AfterFunc(ctx, func() { _ = s.ssh.Close() })
	defer stop()
	err := s.ssh.Serve(s.listener)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// Fingerprint is the storage identity of an SSH public key.
func Fingerprint(key gossh.PublicKey) string {
	sum := sha256.Sum256(key.Marshal())
	return hex.EncodeToString(sum[:])
}

// sessionChannel serves one session channel: it refuses every request
// until an exec command whose first word a service declared, which it hands
// to that service, then ends the channel with the command's exit status.
func (s *Server) sessionChannel(_ *ssh.Server, _ *gossh.ServerConn, newChan gossh.NewChannel, ctx ssh.Context) {
	select {
	case s.channels <- struct{}{}:
		defer func() { <-s.channels }()
	default:
		_ = newChan.Reject(gossh.ResourceShortage, "P's SSH server is busy")
		return
	}
	key, ok := ctx.Value(ssh.ContextKeyPublicKey).(ssh.PublicKey)
	if !ok || key == nil {
		_ = newChan.Reject(gossh.Prohibited, "the connection has no key")
		return
	}
	channel, requests, err := newChan.Accept()
	if err != nil {
		return
	}
	defer channel.Close()
	for req := range requests {
		command, service := s.route(req)
		if service == nil {
			_ = req.Reply(false, nil)
			continue
		}
		_ = req.Reply(true, nil)
		var status uint32
		if service.Serve(ctx, Exec{Command: command, Fingerprint: Fingerprint(key), In: channel, Out: channel, Err: channel.Stderr()}) != nil {
			status = 1
		}
		_, _ = channel.SendRequest("exit-status", false, gossh.Marshal(struct{ Status uint32 }{status}))
		return
	}
}

// route finds the service of an exec request by its command's first word;
// any other request has none.
func (s *Server) route(req *gossh.Request) (string, Service) {
	if req.Type != "exec" {
		return "", nil
	}
	var payload struct{ Value string }
	if gossh.Unmarshal(req.Payload, &payload) != nil {
		return "", nil
	}
	word, _, _ := strings.Cut(payload.Value, " ")
	return payload.Value, s.services[word]
}
