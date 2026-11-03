package control

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"strconv"
	"syscall"

	"github.com/lgvo/p.ai/internal/api"
	"github.com/lgvo/p.ai/internal/runtimedriver"
)

type AttachmentAPI interface {
	AttachSession(context.Context, string) (api.PendingAttachment, error)
	// ClaimAttachment redeems a pending attachment's token and opens its
	// terminal at size. The claim's connection then carries the terminal.
	ClaimAttachment(ctx context.Context, token string, size runtimedriver.Size) (Terminal, error)
}

// Terminal is what a connection carries once attachment.claim is answered:
// an open terminal, whose Close ends the attachment.
type Terminal interface {
	io.ReadWriteCloser
	Resize(runtimedriver.Size) error
}

type connectionKey struct{}

// Connection is one client connection: the peer it was accepted from, and
// the terminal a claim opened for it to carry. ServeConn serves a claim on
// the connection's own goroutine and takes the terminal as soon as the claim
// answers, so the terminal has that one owner.
type Connection struct {
	terminal Terminal
	peerPID  int
	peerUID  uint32
}

// takeTerminal hands over the terminal a claim opened, once.
func (c *Connection) takeTerminal() Terminal {
	t := c.terminal
	c.terminal = nil
	return t
}

// connectionOf is the connection a request arrived on; nil outside ServeConn.
func connectionOf(ctx context.Context) *Connection {
	conn, _ := ctx.Value(connectionKey{}).(*Connection)
	return conn
}

// RegisterClaim serves attachment.claim with claim. Once claim returns an
// open terminal, the claim answers, and ServeConn carries the terminal over
// the rest of the connection.
func RegisterClaim(t *MethodTable, claim func(context.Context, api.AttachmentClaimRequest) (Terminal, error)) {
	Register(t, api.AttachmentClaim, func(ctx context.Context, r api.AttachmentClaimRequest) (api.AttachmentLaunch, error) {
		conn := connectionOf(ctx)
		if conn == nil {
			return api.AttachmentLaunch{}, wrongConnection()
		}
		terminal, err := claim(ctx, r)
		if err != nil {
			return api.AttachmentLaunch{}, err
		}
		conn.terminal = terminal
		return api.AttachmentLaunch{}, nil
	})
}

// addAttachmentMethods serves session.attach and the attachment helper's
// claim. Only the trusted helper may claim a token.
func addAttachmentMethods(t *MethodTable, life AttachmentAPI) {
	Register(t, api.SessionAttach, func(ctx context.Context, r api.SessionAttachRequest) (api.PendingAttachment, error) {
		if connectionOf(ctx) == nil {
			return api.PendingAttachment{}, wrongConnection()
		}
		return life.AttachSession(ctx, r.UUID)
	})
	RegisterClaim(t, func(ctx context.Context, r api.AttachmentClaimRequest) (Terminal, error) {
		if !connectionOf(ctx).trustedHelper() {
			return nil, wrongConnection()
		}
		return life.ClaimAttachment(ctx, r.Token, runtimedriver.Size{Width: r.Width, Height: r.Height})
	})
}

// wrongConnection refuses a method this connection may not call: claiming a
// token from anything but the trusted attachment helper, or attaching outside
// a client connection. It classifies as ErrConflict inside the daemon.
func wrongConnection() error {
	return &Refusal{Kind: api.KindInvalidRequest, Reason: api.ReasonConnection, Cause: ErrConflict}
}

// Only the fixed helper mode of this exact running executable may redeem an
// attachment token. Host socket authentication alone does not make a normal API
// client a source of presence assertions. Session-mounted sockets never expose
// this handler. The trusted host user remains the governing principal.
func (c *Connection) trustedHelper() bool {
	if c.peerPID <= 0 || c.peerUID != uint32(os.Geteuid()) {
		return false
	}
	prefix := "/proc/" + strconv.Itoa(c.peerPID)
	self, e1 := os.Stat("/proc/self/exe")
	peer, e2 := os.Stat(prefix + "/exe")
	if e1 != nil || e2 != nil || !os.SameFile(self, peer) {
		return false
	}
	raw, e := os.ReadFile(prefix + "/cmdline")
	if e != nil || len(raw) > 4096 {
		return false
	}
	args := bytes.Split(bytes.TrimSuffix(raw, []byte{0}), []byte{0})
	return len(args) == 2 && string(args[1]) == "attach-helper"
}
func connectionPeer(conn net.Conn) (int, uint32) {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, 0
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return 0, 0
	}
	var peer *syscall.Ucred
	var getErr error
	if err = raw.Control(func(fd uintptr) {
		peer, getErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || getErr != nil || peer == nil {
		return 0, 0
	}
	return int(peer.Pid), peer.Uid
}
