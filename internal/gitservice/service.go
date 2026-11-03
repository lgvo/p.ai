package gitservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"charm.land/ssh"
	"charm.land/wish/v2"
	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
	gossh "golang.org/x/crypto/ssh"
)

// Backend is the typed p.git-service/v1 broker. Git owns objects and refs;
// SQLite owns principal, assignment, and guard authority.
type Backend struct {
	store     *control.Store
	stateDir  string
	gitPath   string
	selection plugin.Active
	sessions  chan struct{}
	originMu  sync.Map // project -> *originLock; also used by future P-controlled GC
}

type boundedInput struct {
	source    io.Reader
	remaining int64
	overflow  atomic.Bool
}

func (r *boundedInput) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		var one [1]byte
		n, err := r.source.Read(one[:])
		if n > 0 {
			r.overflow.Store(true)
			return 0, errors.New("Git input exceeded plan")
		}
		return 0, err
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.source.Read(p)
	r.remaining -= int64(n)
	return n, err
}

func New(store *control.Store, stateDir string, active []plugin.Active) (*Backend, error) {
	if store == nil || stateDir != store.StateDir() || !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir {
		return nil, control.ErrInvalid
	}
	if err := privateDirectory(stateDir); err != nil {
		return nil, err
	}
	var selected *plugin.Active
	for i := range active {
		if active[i].Package.Manifest.Capability == "source-git" {
			selected = &active[i]
		}
	}
	if selected == nil || selected.Package.Manifest.Runtime.Kind != "wasi-command" ||
		len(selected.Grants) != 1 || selected.Grants[0] != "git.project" {
		return nil, errors.New("source-git activation is required")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(gitPath) {
		return nil, errors.New("Git executable must resolve to an absolute path")
	}
	return &Backend{store: store, stateDir: stateDir, gitPath: gitPath, selection: *selected, sessions: make(chan struct{}, 16)}, nil
}

func (b *Backend) revalidate() error {
	pkg, err := plugin.Conformance(b.selection.Package.Path)
	if err != nil {
		return err
	}
	if pkg.SHA256 != b.selection.Package.SHA256 || pkg.Manifest.ID != b.selection.Package.Manifest.ID {
		return errors.New("source-git package changed after activation")
	}
	return nil
}

func validProject(path string) bool {
	if path == "" || len(path) > 255 || strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." || len(part) > 100 {
			return false
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
				return false
			}
		}
	}
	return true
}

func (b *Backend) repository(project string) (string, error) {
	if !validProject(project) {
		return "", control.ErrInvalid
	}
	// Logical names can contain `.git` components. Mapping them into nested
	// paths would allow a.git/b to land inside a's physical repository.
	name := sha256.Sum256([]byte(project))
	return filepath.Join(b.stateDir, "repositories", hex.EncodeToString(name[:])+".git"), nil
}

func privateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0022 != 0 {
		return errors.New("Git repository ancestry is not private and owned")
	}
	return nil
}

func (b *Backend) checkRepositoryParents(project string) error {
	if !validProject(project) {
		return control.ErrInvalid
	}
	if err := privateDirectory(b.stateDir); err != nil {
		return err
	}
	return privateDirectory(filepath.Join(b.stateDir, "repositories"))
}

func (b *Backend) gitEnv(extra ...string) []string {
	base := []string{"PATH=" + filepath.Dir(b.gitPath), "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_REPLACE_OBJECTS=1", "GIT_OPTIONAL_LOCKS=0"}
	return append(append(base, noImplicitMaintenanceEnv()...), extra...)
}

// Pending origin-source operations retain their captured commit as an object
// before the ordinary P branch exists. Git operations that can invoke native
// maintenance must leave unreachable objects intact until that branch CAS (or later explicit
// lifecycle cleanup). In particular, receive-pack otherwise may auto-prune.
// Any future explicit P GC must account for pending creation evidence.
func noImplicitMaintenanceEnv() []string {
	return []string{
		"GIT_CONFIG_COUNT=3",
		"GIT_CONFIG_KEY_0=gc.auto", "GIT_CONFIG_VALUE_0=0",
		"GIT_CONFIG_KEY_1=maintenance.auto", "GIT_CONFIG_VALUE_1=false",
		"GIT_CONFIG_KEY_2=receive.autogc", "GIT_CONFIG_VALUE_2=false",
	}
}

func noImplicitMaintenanceArgs() []string {
	return []string{"-c", "gc.auto=0", "-c", "maintenance.auto=false", "-c", "receive.autogc=false"}
}

// Keep the protective options last: Git's command-line -c overrides the
// trusted environment, so earlier transport settings cannot enable pruning.
func transportGitConfigArgs(service, hooksDir string, maxInputBytes int64) []string {
	args := []string{"-c", "transfer.hideRefs=refs", "-c", "transfer.hideRefs=!refs/heads", "-c", "uploadpack.allowTipSHA1InWant=false", "-c", "uploadpack.allowReachableSHA1InWant=false", "-c", "uploadpack.allowAnySHA1InWant=false", "-c", "uploadpack.allowRefInWant=false"}
	if service == "receive" {
		args = append(args, "-c", "core.hooksPath="+hooksDir, "-c", "receive.denyDeletes=true", "-c", "receive.denyNonFastForwards=true", "-c", "receive.maxInputSize="+strconv.FormatInt(maxInputBytes, 10))
	}
	return append(args, noImplicitMaintenanceArgs()...)
}

// gitBroker runs Git on one checked project repository for the bundle
// export.
type gitBroker struct {
	backend *Backend
	project string
}

func (b *Backend) checkRepository(ctx context.Context, project string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	repo, err := b.repository(project)
	if err != nil {
		return "", err
	}
	if err := b.checkRepositoryParents(project); err != nil {
		return "", err
	}
	if err := verifyBareDir(repo); err != nil {
		return "", err
	}
	bare := exec.CommandContext(ctx, b.gitPath, "-C", repo, "rev-parse", "--is-bare-repository")
	bare.Env = b.gitEnv()
	out, err := bare.Output()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		return "", errors.New("repository is not bare Git")
	}
	head := exec.CommandContext(ctx, b.gitPath, "-C", repo, "symbolic-ref", "HEAD")
	head.Env = b.gitEnv()
	out, err = head.Output()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(out)), "refs/heads/") || !validBranch(strings.TrimPrefix(strings.TrimSpace(string(out)), "refs/heads/")) {
		return "", errors.New("repository HEAD is invalid")
	}
	return repo, nil
}

func verifyBareDir(repo string) error {
	if err := privateDirectory(repo); err != nil {
		return err
	}
	for _, name := range []string{"HEAD", "config", "objects", "refs"} {
		info, err := os.Lstat(filepath.Join(repo, name))
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || info.Mode()&os.ModeSymlink != 0 || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0022 != 0 {
			return errors.New("Git repository has an unsafe control path")
		}
		if (name == "HEAD" || name == "config") && (!info.Mode().IsRegular() || stat.Nlink != 1) {
			return errors.New("Git repository control file is unsafe")
		}
		if (name == "objects" || name == "refs") && !info.IsDir() {
			return errors.New("Git repository control directory is unsafe")
		}
	}
	return nil
}

func keyFingerprint(key ssh.PublicKey) string {
	sum := sha256.Sum256(key.Marshal())
	return hex.EncodeToString(sum[:])
}

// Fingerprint is the storage identity for a parsed SSH public key.
func Fingerprint(key ssh.PublicKey) string { return keyFingerprint(key) }

func (b *Backend) Serve(ctx context.Context, listener net.Listener, hostKey []byte) error {
	prepared, err := b.Prepare(ctx, listener, hostKey)
	if err != nil {
		return err
	}
	return prepared.Serve(ctx)
}

// PreparedServer has completed every synchronous Git SSH setup step. A daemon
// can publish its RPC capability only after Prepare succeeds.
type PreparedServer struct {
	server   *ssh.Server
	listener net.Listener
}

func (b *Backend) Prepare(ctx context.Context, listener net.Listener, hostKey []byte) (*PreparedServer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := b.revalidate(); err != nil {
		return nil, err
	}
	if listener == nil || len(hostKey) == 0 {
		return nil, control.ErrInvalid
	}
	tcp, ok := listener.Addr().(*net.TCPAddr)
	if !ok || tcp.IP == nil || !tcp.IP.IsLoopback() {
		return nil, errors.New("Git SSH listener must be loopback")
	}
	if err := b.installHook(); err != nil {
		return nil, err
	}
	srv, err := wish.NewServer(
		wish.WithHostKeyPEM(hostKey),
		wish.WithPublicKeyAuth(func(auth ssh.Context, key ssh.PublicKey) bool {
			if auth.User() != "git" || key == nil {
				return false
			}
			return b.store.IsGitPrincipalActive(auth, keyFingerprint(key))
		}),
		wish.WithMaxTimeout(Timeout),
		wish.WithIdleTimeout(2*time.Minute),
	)
	if err != nil {
		return nil, err
	}
	// The standard session handler acknowledges env and PTY before a middleware
	// can reject them. This narrow handler refuses those requests on the wire.
	srv.ChannelHandlers = map[string]ssh.ChannelHandler{"session": b.sessionChannel}
	srv.SubsystemHandlers = map[string]ssh.SubsystemHandler{}
	srv.RequestHandlers = map[string]ssh.RequestHandler{}
	return &PreparedServer{server: srv, listener: listener}, nil
}

func (prepared *PreparedServer) Serve(ctx context.Context) error {
	if prepared == nil || prepared.server == nil || prepared.listener == nil {
		return control.ErrInvalid
	}
	srv, listener := prepared.server, prepared.listener
	go func() { <-ctx.Done(); _ = srv.Close() }()
	err := srv.Serve(listener)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func parseCommand(raw string) (service, project string, ok bool) {
	const upload, receive = "git-upload-pack '", "git-receive-pack '"
	if strings.HasPrefix(raw, upload) {
		service, raw = "upload", strings.TrimPrefix(raw, upload)
	} else if strings.HasPrefix(raw, receive) {
		service, raw = "receive", strings.TrimPrefix(raw, receive)
	} else {
		return "", "", false
	}
	if !strings.HasSuffix(raw, "'") {
		return "", "", false
	}
	project = strings.TrimSuffix(raw, "'")
	if strings.HasPrefix(project, "/") {
		project = strings.TrimPrefix(project, "/")
	}
	if strings.ContainsAny(project, "'\\\n\r\t") || !validProject(project) {
		return "", "", false
	}
	return service, project, true
}

func (b *Backend) sessionChannel(_ *ssh.Server, _ *gossh.ServerConn, newChan gossh.NewChannel, auth ssh.Context) {
	select {
	case b.sessions <- struct{}{}:
		defer func() { <-b.sessions }()
	default:
		_ = newChan.Reject(gossh.ResourceShortage, "Git service is busy")
		return
	}
	channel, requests, err := newChan.Accept()
	if err != nil {
		return
	}
	defer channel.Close()
	for req := range requests {
		if req.Type != "exec" {
			_ = req.Reply(false, nil)
			continue
		}
		var payload struct{ Value string }
		if err := gossh.Unmarshal(req.Payload, &payload); err != nil {
			_ = req.Reply(false, nil)
			continue
		}
		service, project, ok := parseCommand(payload.Value)
		if !ok {
			_ = req.Reply(false, nil)
			continue
		}
		_ = req.Reply(true, nil)
		status := b.handle(auth, channel, service, project)
		_, _ = channel.SendRequest("exit-status", false, gossh.Marshal(struct{ Status uint32 }{uint32(status)}))
		return
	}
}

func (b *Backend) handle(auth ssh.Context, channel gossh.Channel, service, project string) int {
	if err := b.revalidate(); err != nil {
		return 1
	}
	key, ok := auth.Value(ssh.ContextKeyPublicKey).(ssh.PublicKey)
	if !ok || key == nil {
		return 1
	}
	ref, release, err := b.store.AcquireGitLease(auth, keyFingerprint(key), project, service)
	if err != nil {
		return 1
	}
	defer release()
	repo, err := b.checkRepository(auth, project)
	if err != nil {
		return 1
	}
	planOutcome, err := plugin.RunSourceGit(auth, b.selection, plugin.GitCommand{Kind: "git.transport.plan", Project: project, Service: service, Ceilings: &plugin.GitTransportPlan{MaxInputBytes: plugin.GitCoreInputCeiling, MaxDurationMS: plugin.GitCoreDurationMS}}, nil)
	if err != nil {
		return 1
	}
	plan := planOutcome.Plan
	hookPath, hookToken := "", ""
	if service == "receive" {
		var closeHook func()
		hookPath, hookToken, closeHook, err = b.startHookApproval(auth, keyFingerprint(key), project, ref)
		if err != nil {
			return 1
		}
		defer closeHook()
	}
	args := transportGitConfigArgs(service, filepath.Join(b.stateDir, "git-hooks"), plan.MaxInputBytes)
	if service == "receive" {
		args = append(args, "receive-pack", repo)
	} else {
		args = append(args, "upload-pack", repo)
	}
	streamCtx, cancel := context.WithTimeout(auth, time.Duration(plan.MaxDurationMS)*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(streamCtx, b.gitPath, args...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = b.gitEnv("P_GIT_ALLOWED_REF="+ref, "P_GIT_HOOK_SOCKET="+hookPath, "P_GIT_HOOK_TOKEN="+hookToken, "P_GIT_EXEC="+b.gitPath)
	input := &boundedInput{source: channel, remaining: plan.MaxInputBytes}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = input, channel, channel.Stderr()
	if err := cmd.Run(); err != nil || input.overflow.Load() {
		return 1
	}
	return 0
}

func (b *Backend) installHook() error {
	dir := filepath.Join(b.stateDir, "git-hooks")
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := privateDirectory(dir); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if strings.ContainsAny(executable, "'\n\r") {
		return control.ErrInvalid
	}
	content := []byte("#!/bin/sh\nexec '" + executable + "' git-hook\n")
	path := filepath.Join(dir, "pre-receive")
	temp, err := os.CreateTemp(dir, ".pre-receive-")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Chmod(0700); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

// RunPreReceiveHook is the fixed hook entry used by both p and a packaged
// integration driver. The receive lease keeps assignment/guard state fixed.
func RunPreReceiveHook(ctx context.Context, input io.Reader) error {
	allowed := os.Getenv("P_GIT_ALLOWED_REF")
	gitPath := os.Getenv("P_GIT_EXEC")
	if !strings.HasPrefix(allowed, "refs/heads/") || gitPath == "" || !filepath.IsAbs(gitPath) {
		return control.ErrGitDenied
	}
	data, err := io.ReadAll(io.LimitReader(input, 1025))
	if err != nil || len(data) > 1024 {
		return control.ErrGitDenied
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 1 {
		return control.ErrGitDenied
	}
	parts := strings.Split(lines[0], " ")
	if len(parts) != 3 || parts[2] != allowed || !validOID(parts[0]) || !validOID(parts[1]) || allZero(parts[1]) {
		return control.ErrGitDenied
	}
	commit := exec.CommandContext(ctx, gitPath, "cat-file", "-t", parts[1])
	commit.Env = []string{"PATH=" + filepath.Dir(gitPath), "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_REPLACE_OBJECTS=1"}
	for _, key := range []string{"GIT_DIR", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_QUARANTINE_PATH"} {
		if value, ok := os.LookupEnv(key); ok {
			commit.Env = append(commit.Env, key+"="+value)
		}
	}
	out, err := commit.Output()
	if err != nil || strings.TrimSpace(string(out)) != "commit" {
		return control.ErrGitDenied
	}
	if !allZero(parts[0]) {
		ff := exec.CommandContext(ctx, gitPath, "merge-base", "--is-ancestor", parts[0], parts[1])
		ff.Env = commit.Env
		if err := ff.Run(); err != nil {
			return control.ErrGitDenied
		}
	}
	return requestHookApproval(ctx, parts[2], parts[0], parts[1])
}

func validOID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func validBranch(branch string) bool {
	if len(branch) == 0 || len(branch) > 200 || strings.HasSuffix(branch, ".lock") || strings.Contains(branch, "..") || strings.Contains(branch, "@{") {
		return false
	}
	for _, part := range strings.Split(branch, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
				return false
			}
		}
	}
	return true
}
func allZero(s string) bool { return strings.Trim(s, "0") == "" }

// Timeout is the maximum SSH service duration in the v1 broker.
const Timeout = 10 * time.Minute
