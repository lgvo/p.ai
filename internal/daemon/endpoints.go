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
	"github.com/lgvo/p.ai/internal/gitservice"
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
	gitBySession       map[string]int
	sessionSlots       chan struct{}
	sessionByUUID      map[string]int
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
	m := &endpointManager{prefix: prefix, gitAddress: gitAddress, store: store, opened: map[string]*endpointPair{}, conns: map[net.Conn]struct{}{}, gitSlots: make(chan struct{}, 64), gitBySession: map[string]int{}, sessionSlots: make(chan struct{}, 16), sessionByUUID: map[string]int{}}
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
	go m.serveGit(ctx, git, uuid)
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

const gitConnectionsPerSession = 8

func (m *endpointManager) admitGit(conn net.Conn, uuid string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.gitBySession[uuid] >= gitConnectionsPerSession {
		return false
	}
	select {
	case m.gitSlots <- struct{}{}:
	default:
		return false
	}
	m.gitBySession[uuid]++
	m.conns[conn] = struct{}{}
	return true
}

func (m *endpointManager) releaseGit(conn net.Conn, uuid string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.conns, conn)
	m.gitBySession[uuid]--
	if m.gitBySession[uuid] == 0 {
		delete(m.gitBySession, uuid)
	}
	<-m.gitSlots
}

func (m *endpointManager) serveGit(ctx context.Context, l net.Listener, uuid string) {
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		if !m.admitGit(conn, uuid) {
			conn.Close()
			continue
		}
		go func() {
			defer m.releaseGit(conn, uuid)
			m.proxyGit(ctx, conn, gitservice.Timeout)
		}()
	}
}

// proxyGit bounds the raw transport independently of SSH authentication and
// service execution. A guest input half-close still permits the full upstream
// response, while upstream EOF ends both directions and joins the input pump.
func (m *endpointManager) proxyGit(ctx context.Context, conn net.Conn, lifetime time.Duration) {
	ctx, cancel := context.WithTimeout(ctx, lifetime)
	defer cancel()
	defer conn.Close()
	stopGuest := context.AfterFunc(ctx, func() { conn.Close() })
	defer stopGuest()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return
	}
	remote, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", m.gitAddress)
	if err != nil {
		return
	}
	defer remote.Close()
	if !m.addConn(remote) {
		return
	}
	defer m.removeConn(remote)
	stopRemote := context.AfterFunc(ctx, func() { remote.Close() })
	defer stopRemote()
	if err := remote.SetDeadline(deadline); err != nil {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := io.Copy(remote, conn); err != nil {
			remote.Close()
			return
		}
		if tcp, ok := remote.(*net.TCPConn); ok {
			tcp.CloseWrite()
		}
	}()
	io.Copy(conn, remote)
	// Closing both peers unblocks either a guest read or an upstream write.
	// The slot cannot be released until this accepted connection's pump exits.
	conn.Close()
	remote.Close()
	<-done
}

const rpcConnectionsPerSession = 4

func (m *endpointManager) admitSession(conn net.Conn, uuid string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.sessionByUUID[uuid] >= rpcConnectionsPerSession {
		return false
	}
	select {
	case m.sessionSlots <- struct{}{}:
	default:
		return false
	}
	m.sessionByUUID[uuid]++
	m.conns[conn] = struct{}{}
	return true
}

func (m *endpointManager) releaseSession(conn net.Conn, uuid string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.conns, conn)
	m.sessionByUUID[uuid]--
	if m.sessionByUUID[uuid] == 0 {
		delete(m.sessionByUUID, uuid)
	}
	<-m.sessionSlots
}

func (m *endpointManager) serveSession(ctx context.Context, l net.Listener, uuid string) {
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		if !m.admitSession(conn, uuid) {
			conn.Close()
			continue
		}
		go func() {
			defer func() { conn.Close(); m.releaseSession(conn, uuid) }()
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
