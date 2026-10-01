package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/lgvo/p.ai/internal/plugin"
)

type PendingAttachment struct {
	V         int               `json:"v"`
	Token     string            `json:"token"`
	ExpiresAt time.Time         `json:"expires_at"`
	Spec      plugin.AttachSpec `json:"spec"`
}

// AttachmentLaunch is sent only to the trusted host helper on its dedicated
// connection. It cannot be selected or amended by the ordinary client.
type AttachmentLaunch struct {
	V      int               `json:"v"`
	Socket string            `json:"socket"`
	Spec   plugin.AttachSpec `json:"spec"`
}
type AttachmentAPI interface {
	AttachSession(context.Context, string) (PendingAttachment, error)
	ClaimAttachment(context.Context, string, *Connection) (AttachmentLaunch, error)
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

func (c *Connection) Own(release func()) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.dedicated {
		return ErrConflict
	}
	c.dedicated = true
	c.release = release
	return nil
}
func (c *Connection) dedicatedAttachment() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.dedicated }
func (c *Connection) close() {
	c.mu.Lock()
	c.closed = true
	release := c.release
	c.release = nil
	c.mu.Unlock()
	if release != nil {
		release()
	}
}
func attachmentHandler(ctx context.Context, method string, params json.RawMessage, life AttachmentAPI) (any, *RPCError) {
	conn, _ := ctx.Value(connectionKey{}).(*Connection)
	if conn == nil {
		return nil, lifecycleRPC(ErrInvalid)
	}
	switch method {
	case "session.attach":
		var p struct {
			V    int    `json:"v"`
			UUID string `json:"uuid"`
		}
		if strictDecode(params, &p) != nil || p.V != 1 || len(p.UUID) != 36 {
			return nil, lifecycleRPC(ErrInvalid)
		}
		result, err := life.AttachSession(ctx, p.UUID)
		if err != nil {
			return nil, lifecycleRPC(err)
		}
		return result, nil
	case "attachment.claim":
		if !conn.trustedHelper() {
			return nil, lifecycleRPC(ErrInvalid)
		}
		var p struct {
			V     int    `json:"v"`
			Token string `json:"token"`
		}
		if strictDecode(params, &p) != nil || p.V != 1 || len(p.Token) != 64 {
			return nil, lifecycleRPC(ErrInvalid)
		}
		result, err := life.ClaimAttachment(ctx, p.Token, conn)
		if err != nil {
			return nil, lifecycleRPC(err)
		}
		return result, nil
	case "attachment.confirm", "attachment.ping":
		var p struct {
			V         int    `json:"v"`
			Operation string `json:"operation,omitempty"`
		}
		if strictDecode(params, &p) != nil || p.V != 1 || !conn.dedicatedAttachment() {
			return nil, lifecycleRPC(ErrInvalid)
		}
		if method == "attachment.confirm" {
			if len(p.Operation) != len("/1.0/operations/")+36 {
				return nil, lifecycleRPC(ErrInvalid)
			}
			if err := life.ConfirmAttachment(ctx, conn, p.Operation); err != nil {
				return nil, lifecycleRPC(err)
			}
		}
		if method == "attachment.ping" && p.Operation != "" {
			return nil, lifecycleRPC(ErrInvalid)
		}
		return map[string]any{"v": 1}, nil
	}
	return nil, lifecycleRPC(errors.New("unknown attachment method"))
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
