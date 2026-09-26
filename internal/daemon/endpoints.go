package daemon

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
)

type endpointPair struct {
	git, session net.Listener
	dir          string
}
type endpointManager struct {
	prefix, gitAddress string
	store              *control.Store
	onUnattended       func(context.Context, plugin.Event) error
	mu                 sync.Mutex
	opened             map[string]*endpointPair
	conns              map[net.Conn]struct{}
	gitSlots           chan struct{}
	sessionSlots       chan struct{}
	closed             bool
}

func newEndpointManager(prefix, gitAddress string, store *control.Store) (*endpointManager, error) {
	if err := os.Mkdir(prefix, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err := os.Lstat(prefix)
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != 0700 || st.Uid != uint32(os.Geteuid()) {
		return nil, errors.New("endpoint prefix must be private and daemon-owned")
	}
	m := &endpointManager{prefix: prefix, gitAddress: gitAddress, store: store, opened: map[string]*endpointPair{}, conns: map[net.Conn]struct{}{}, gitSlots: make(chan struct{}, 64), sessionSlots: make(chan struct{}, 16)}
	return m, nil
}

func (m *endpointManager) Ensure(ctx context.Context, uuid string) (string, error) {
	if len(uuid) != 36 {
		return "", errors.New("invalid session UUID")
	}
	for i, c := range uuid {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return "", errors.New("invalid session UUID")
			}
		} else if c < '0' || c > '9' && (c < 'a' || c > 'f') {
			return "", errors.New("invalid session UUID")
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return "", errors.New("endpoint manager closed")
	}
	if p := m.opened[uuid]; p != nil {
		return p.dir, nil
	}
	dir, err := ensureEndpointDirectory(m.prefix, uuid)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.Name() != "git.sock" && entry.Name() != "session.sock" {
			return "", errors.New("unexpected endpoint directory entry")
		}
		item, e := entry.Info()
		if e != nil {
			return "", e
		}
		owner, ok := item.Sys().(*syscall.Stat_t)
		if !ok || item.Mode()&os.ModeSocket == 0 || item.Mode().Perm() != 0666 || owner.Uid != uint32(os.Geteuid()) {
			return "", errors.New("unsafe stale endpoint socket")
		}
	}
	for _, entry := range entries {
		if e := os.Remove(filepath.Join(dir, entry.Name())); e != nil {
			return "", e
		}
	}
	git, err := listenEndpoint(filepath.Join(dir, "git.sock"))
	if err != nil {
		return "", err
	}
	session, err := listenEndpoint(filepath.Join(dir, "session.sock"))
	if err != nil {
		git.Close()
		return "", err
	}
	pair := &endpointPair{git: git, session: session, dir: dir}
	m.opened[uuid] = pair
	go m.serveGit(ctx, git)
	go m.serveSession(ctx, session, uuid)
	return dir, nil
}

func ensureEndpointDirectory(prefix, uuid string) (string, error) {
	dir := filepath.Join(prefix, uuid)
	if err := os.Mkdir(dir, 0755); err == nil {
		// Mkdir applies the daemon's umask. Open the new directory without
		// following a substituted symlink before making it traversable.
		fd, err := syscall.Open(dir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if err != nil {
			return "", err
		}
		err = syscall.Fchmod(fd, 0755)
		closeErr := syscall.Close(fd)
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
	} else if !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != 0755 || st.Uid != uint32(os.Geteuid()) {
		return "", errors.New("session endpoint directory has unsafe identity")
	}
	return dir, nil
}

func listenEndpoint(path string) (net.Listener, error) {
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(path, 0666); err != nil {
		l.Close()
		return nil, err
	}
	// Managed sockets are removed only after explicit identity checks. A
	// listener's automatic name-based unlink on shutdown could delete a path
	// substituted after a refused cleanup or review.
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	return l, nil
}
func (m *endpointManager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	for _, p := range m.opened {
		p.git.Close()
		p.session.Close()
	}
	for c := range m.conns {
		c.Close()
	}
}

// RemoveSession closes only this UUID's listeners and removes its exact
// daemon-owned socket directory after the native runtime is absent. A restart
// may leave stale socket entries, so the filesystem check is independent of
// the in-memory opened map. Unexpected content is never recursively removed.
func (m *endpointManager) RemoveSession(uuid string) error {
	if len(uuid) != 36 {
		return errors.New("invalid endpoint session identity")
	}
	for i, c := range uuid {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return errors.New("invalid endpoint session identity")
			}
		} else if c < '0' || c > '9' && (c < 'a' || c > 'f') {
			return errors.New("invalid endpoint session identity")
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if pair := m.opened[uuid]; pair != nil {
		pair.git.Close()
		pair.session.Close()
		delete(m.opened, uuid)
	}
	dir := filepath.Join(m.prefix, uuid)
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != 0755 || owner.Uid != uint32(os.Geteuid()) {
		return errors.New("endpoint cleanup directory identity changed")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != "git.sock" && entry.Name() != "session.sock" {
			return errors.New("endpoint cleanup found unexpected entry")
		}
		item, e := os.Lstat(filepath.Join(dir, entry.Name()))
		if e != nil {
			return e
		}
		st, ok := item.Sys().(*syscall.Stat_t)
		if !ok || item.Mode()&os.ModeSocket == 0 || item.Mode().Perm() != 0666 || st.Uid != uint32(os.Geteuid()) {
			return errors.New("endpoint cleanup socket identity changed")
		}
	}
	for _, entry := range entries {
		if err = os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	if err = os.Remove(dir); err != nil {
		return err
	}
	parent, err := os.Open(m.prefix)
	if err != nil {
		return err
	}
	syncErr := parent.Sync()
	closeErr := parent.Close()
	return errors.Join(syncErr, closeErr)
}

func (m *endpointManager) addConn(c net.Conn) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return false
	}
	m.conns[c] = struct{}{}
	return true
}
func (m *endpointManager) removeConn(c net.Conn) { m.mu.Lock(); delete(m.conns, c); m.mu.Unlock() }

func (m *endpointManager) serveGit(ctx context.Context, l net.Listener) {
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		select {
		case m.gitSlots <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		if !m.addConn(conn) {
			<-m.gitSlots
			conn.Close()
			return
		}
		go func() {
			defer func() { m.removeConn(conn); conn.Close(); <-m.gitSlots }()
			remote, e := net.DialTimeout("tcp", m.gitAddress, 5*time.Second)
			if e != nil {
				return
			}
			if !m.addConn(remote) {
				remote.Close()
				return
			}
			defer func() { m.removeConn(remote); remote.Close() }()
			done := make(chan struct{}, 1)
			go func() {
				io.Copy(remote, conn)
				if tcp, ok := remote.(*net.TCPConn); ok {
					tcp.CloseWrite()
				}
				done <- struct{}{}
			}()
			io.Copy(conn, remote)
			if unix, ok := conn.(*net.UnixConn); ok {
				unix.CloseWrite()
			}
			select {
			case <-done:
			case <-ctx.Done():
			}
		}()
	}
}

func (m *endpointManager) serveSession(ctx context.Context, l net.Listener, uuid string) {
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		select {
		case m.sessionSlots <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		if !m.addConn(conn) {
			<-m.sessionSlots
			conn.Close()
			return
		}
		go func() {
			defer func() { m.removeConn(conn); conn.Close(); <-m.sessionSlots }()
			var session control.Session
			var instance string
			contextReady := false
			if m.onUnattended != nil && m.store != nil {
				var se, ie error
				session, se = m.store.GetSession(ctx, uuid)
				instance, ie = m.store.InstanceID(ctx)
				contextReady = se == nil && ie == nil
			}
			control.ServeSessionConn(ctx, conn, m.store, uuid, func(ctx context.Context, changed control.UnattendedCondition) error {
				if m.onUnattended == nil {
					return nil
				}
				// Delivery follows the SQLite commit and has no authority to undo
				// the reduced status.
				if !contextReady {
					return errors.New("event context unavailable")
				}
				id := "e-" + instance + "-status-" + strconv.FormatInt(changed.ReceiveSequence, 10)
				event := plugin.Event{Schema: "p.event/v1", ID: id, Kind: "session.unattended_changed", OccurredAt: changed.ReceivedAt, Instance: instance, Project: session.Project, Session: uuid, Branch: session.Branch, Fields: map[string]string{"unattended_condition": changed.Condition}}
				return m.onUnattended(ctx, event)
			})
		}()
	}
}
