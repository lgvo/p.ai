package control

import (
	"bytes"
	"context"
	"net"
	"os"
	"strconv"
	"sync"
	"syscall"

	"github.com/lgvo/p.ai/internal/api"
)

type AttachmentAPI interface {
	AttachSession(context.Context, string) (api.PendingAttachment, error)
	ClaimAttachment(context.Context, string, *Connection) (api.AttachmentLaunch, error)
	ConfirmAttachment(context.Context, *Connection, string) error
}

type connectionKey struct{}

// Connection owns ephemeral authority independently of individual RPC requests.
type Connection struct {
	mu        sync.Mutex
	closed    bool
	dedicated bool
	release   func()
	peerPID   int
	peerUID   uint32
}

// Own dedicates the connection to one attachment whose release runs when the
// connection closes. A closed or already dedicated connection refuses it.
func (c *Connection) Own(release func()) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.dedicated {
		return wrongConnection()
	}
	c.dedicated = true
	c.release = release
	return nil
}
func (c *Connection) dedicatedAttachment() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.dedicated }

// close runs the attachment's release at connection teardown, where no request
// is left to answer, so a panic in the release is logged and recovered there.
func (c *Connection) close() {
	release := c.takeRelease()
	if release == nil {
		return
	}
	defer func() {
		if value := recover(); value != nil {
			logPanic("attachment release at connection close", value)
		}
	}()
	release()
}

// takeRelease marks the connection closed, so nothing can own it again, and
// hands over its release once.
func (c *Connection) takeRelease() func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	release := c.release
	c.release = nil
	return release
}

// addAttachmentMethods serves session.attach and the attachment helper's
// methods. Only the trusted helper may claim a token, and only the connection
// that claimed it may confirm and ping; such a connection may call nothing else.
func addAttachmentMethods(t *MethodTable, life AttachmentAPI) {
	connection := func(ctx context.Context) (*Connection, error) {
		conn, _ := ctx.Value(connectionKey{}).(*Connection)
		if conn == nil {
			return nil, wrongConnection()
		}
		return conn, nil
	}
	Register(t, api.SessionAttach, func(ctx context.Context, r api.SessionAttachRequest) (api.PendingAttachment, error) {
		if _, err := connection(ctx); err != nil {
			return api.PendingAttachment{}, err
		}
		return life.AttachSession(ctx, r.UUID)
	})
	Register(t, api.AttachmentClaim, func(ctx context.Context, r api.AttachmentClaimRequest) (api.AttachmentLaunch, error) {
		conn, err := connection(ctx)
		if err != nil {
			return api.AttachmentLaunch{}, err
		}
		if !conn.trustedHelper() {
			return api.AttachmentLaunch{}, wrongConnection()
		}
		return life.ClaimAttachment(ctx, r.Token, conn)
	})
	Register(t, api.AttachmentConfirm, func(ctx context.Context, r api.AttachmentConfirmRequest) (api.AttachmentResult, error) {
		conn, err := connection(ctx)
		if err != nil || !conn.dedicatedAttachment() {
			return api.AttachmentResult{}, wrongConnection()
		}
		return api.AttachmentResult{}, life.ConfirmAttachment(ctx, conn, r.Operation)
	})
	Register(t, api.AttachmentPing, func(ctx context.Context, _ api.AttachmentPingRequest) (api.AttachmentResult, error) {
		conn, err := connection(ctx)
		if err != nil || !conn.dedicatedAttachment() {
			return api.AttachmentResult{}, wrongConnection()
		}
		return api.AttachmentResult{}, nil
	})
}

// wrongConnection refuses a method this connection may not call: claiming a
// token from anything but the trusted attachment helper, a second claim, or
// confirm and ping from a connection that claimed nothing. It classifies as
// ErrConflict inside the daemon.
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
