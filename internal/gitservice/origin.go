package gitservice

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
)

const originMaxRefs = 1024
const originMaxOutput = 300 << 10

type originLock struct{ slot chan struct{} }

// OriginScope holds the per-project origin/GC lock. The caller must complete
// any correctness-sensitive observation, selection and ref update within the
// callback. Future P-controlled Git GC must acquire this same lock.
type OriginScope struct {
	mu          sync.Mutex
	active      bool
	backend     *Backend
	project     string
	repo        string
	url         string
	observed    map[string]plugin.GitOriginRef
	publication *OriginPublicationPreview
}

func (b *Backend) WithOrigin(ctx context.Context, project string, fn func(*OriginScope) error) error {
	if fn == nil || !validProject(project) {
		return control.ErrInvalid
	}
	if err := b.revalidate(); err != nil {
		return err
	}
	// Initial origin contact precedes repository creation. Fetch checks the
	// repository later, after lifecycle code has created it.
	repo, err := b.repository(project)
	if err != nil {
		return err
	}
	actual, _ := b.originMu.LoadOrStore(project, &originLock{slot: make(chan struct{}, 1)})
	lock := actual.(*originLock)
	select {
	case lock.slot <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-lock.slot }()
	scope := &OriginScope{active: true, backend: b, project: project, repo: repo}
	defer func() { scope.mu.Lock(); scope.active = false; scope.mu.Unlock() }()
	return fn(scope)
}

// Observe contacts one validated SSH origin afresh. Even an empty advertised
// set is successful. Failure clears this scope's prior observation.
func (s *OriginScope) Observe(ctx context.Context, remote string) ([]plugin.GitOriginRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active {
		return nil, control.ErrInvalid
	}
	s.observed, s.url, s.publication = nil, "", nil
	if err := validOriginURL(remote); err != nil {
		return nil, err
	}
	broker := &originBroker{scope: s, remote: remote}
	out, err := plugin.RunSourceGit(ctx, s.backend.selection, plugin.GitCommand{Kind: "git.origin.observe", Project: s.project}, broker)
	if err != nil {
		return nil, err
	}
	s.url, s.observed = remote, make(map[string]plugin.GitOriginRef, len(out.OriginRefs))
	for _, ref := range out.OriginRefs {
		s.observed[ref.Ref] = ref
	}
	return out.OriginRefs, nil
}

// Fetch transfers exactly the selected observed ref into P's object cache.
// It creates no ordinary, remote-tracking, or protected P ref. A raced origin
// ref fails instead of silently selecting its newer tip.
func (s *OriginScope) Fetch(ctx context.Context, ref string, expectedCommit string) (oid string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() {
		if err != nil {
			s.observed, s.url = nil, ""
		}
	}()
	if !s.active {
		return "", control.ErrInvalid
	}
	observed, ok := s.observed[ref]
	if !ok || observed.CommitOID != expectedCommit || s.url == "" {
		return "", control.ErrInvalid
	}
	if _, err := s.backend.checkRepository(ctx, s.project); err != nil {
		return "", err
	}
	broker := &originBroker{scope: s, remote: s.url, selected: observed}
	out, err := plugin.RunSourceGit(ctx, s.backend.selection, plugin.GitCommand{Kind: "git.origin.fetch", Project: s.project, OriginRef: ref, CommitOID: expectedCommit}, broker)
	if err != nil {
		return "", err
	}
	return out.CommitOID, nil
}

type originBroker struct {
	plugin.GitBroker // other methods are outside this operation's scope
	scope            *OriginScope
	remote           string
	selected         plugin.GitOriginRef
	publication      *OriginPublicationPreview
	pushAttempted    bool
}

func (o *originBroker) ObserveOrigin(ctx context.Context) ([]plugin.GitOriginRef, error) {
	return o.scope.backend.advertisedOrigin(ctx, o.remote)
}
func (o *originBroker) FetchOrigin(ctx context.Context) (string, error) {
	return o.scope.backend.fetchOrigin(ctx, o.scope.repo, o.remote, o.selected)
}

// The allowlist prevents URL helpers, URL options, SSH command injection and
// local paths. Host aliases and ProxyCommand are still read from trusted user
// OpenSSH configuration by the actual ssh process.
// ValidateOriginURL checks the supported structured SSH URL forms without
// contacting the remote.
func ValidateOriginURL(raw string) error { return validOriginURL(raw) }

func validOriginURL(raw string) error {
	if raw == "" || len(raw) > 2048 || strings.IndexFunc(raw, func(r rune) bool { return r < 0x21 || r > 0x7e }) >= 0 {
		return control.ErrInvalid
	}
	if strings.HasPrefix(raw, "ssh://") {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "ssh" || u.Hostname() == "" || u.Path == "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
			return control.ErrInvalid
		}
		if u.User != nil {
			if _, has := u.User.Password(); has || !safeUser(u.User.Username()) {
				return control.ErrInvalid
			}
		}
		if !safeHost(u.Hostname()) || !safeRemotePath(u.Path) || u.RawPath != "" {
			return control.ErrInvalid
		}
		if p := u.Port(); p != "" {
			n, e := strconv.Atoi(p)
			if e != nil || n < 1 || n > 65535 {
				return control.ErrInvalid
			}
		}
		return nil
	}
	if strings.Contains(raw, "://") {
		return control.ErrInvalid
	}
	userHost, path, ok := strings.Cut(raw, ":")
	if !ok || !safeRemotePath(path) || strings.Count(userHost, "@") != 1 {
		return control.ErrInvalid
	}
	user, host, _ := strings.Cut(userHost, "@")
	if !safeUser(user) || !safeHost(host) {
		return control.ErrInvalid
	}
	return nil
}

func safeUser(s string) bool { return safeAtom(s, "._-") && s[0] != '-' }
func safeHost(s string) bool { return safeAtom(s, "._-:") && s[0] != '-' && !strings.Contains(s, "..") }
func safeRemotePath(s string) bool {
	if s == "" || s[0] == '-' || strings.Contains(s, "..") {
		return false
	}
	for _, r := range s {
		if !asciiAlphaNum(r) && !strings.ContainsRune("/_-.~", r) {
			return false
		}
	}
	return true
}
func safeAtom(s, extra string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !asciiAlphaNum(r) && !strings.ContainsRune(extra, r) {
			return false
		}
	}
	return true
}
func asciiAlphaNum(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}

type originOutput struct {
	buf bytes.Buffer
	max int
}

func (o *originOutput) Write(p []byte) (int, error) {
	if o.buf.Len()+len(p) > o.max {
		return 0, errors.New("origin Git output exceeded limit")
	}
	return o.buf.Write(p)
}

func (b *Backend) originEnv(sshPath string, extra ...string) []string {
	// GIT_SSH_COMMAND is constant and contains no URL or ref. Git invokes it
	// through a shell, so quote the trusted executable path as one word.
	quoted := "'" + strings.ReplaceAll(sshPath, "'", "'\\''") + "'"
	env := []string{
		"PATH=" + filepath.Dir(sshPath) + ":" + filepath.Dir(b.gitPath),
		"HOME=" + userHome(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_OPTIONAL_LOCKS=0",
		"GIT_SSH_COMMAND=" + quoted + " -oBatchMode=yes -oNumberOfPasswordPrompts=0 -oClearAllForwardings=yes -oRequestTTY=no",
	}
	env = append(env, noImplicitMaintenanceEnv()...)
	if sock := os.Getenv("SSH_AUTH_SOCK"); filepath.IsAbs(sock) && !strings.ContainsAny(sock, "\r\n") {
		env = append(env, "SSH_AUTH_SOCK="+sock)
	}
	return append(env, extra...)
}

func userHome() string {
	h, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(h) {
		return "/nonexistent"
	}
	return h
}

func (b *Backend) originGit(ctx context.Context, dir string, extraEnv []string, args ...string) (string, error) {
	sshPath, err := exec.LookPath("ssh")
	if err != nil || !filepath.IsAbs(sshPath) {
		return "", errors.New("OpenSSH executable unavailable")
	}
	base := []string{"-c", "protocol.allow=never", "-c", "protocol.ssh.allow=always", "-c", "core.hooksPath=/dev/null", "-c", "credential.helper=", "-c", "fetch.autoGC=false", "-c", "core.fsmonitor=false"}
	base = append(base, noImplicitMaintenanceArgs()...)
	cmd := exec.CommandContext(ctx, b.gitPath, append(base, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 250 * time.Millisecond
	cmd.Dir = dir
	cmd.Env = b.originEnv(sshPath, extraEnv...)
	var out, diagnostic originOutput
	out.max, diagnostic.max = originMaxOutput, 4096
	cmd.Stdout, cmd.Stderr = &out, &diagnostic
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("origin Git failed; check SSH identity, host key, URL and access")
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return out.buf.String(), nil
}

func (b *Backend) advertisedOrigin(ctx context.Context, remote string) ([]plugin.GitOriginRef, error) {
	if err := validOriginURL(remote); err != nil {
		return nil, err
	}
	out, err := b.originGit(ctx, "/", nil, "ls-remote", "--heads", "--tags", remote)
	if err != nil {
		return nil, err
	}
	return parseOriginAdvertisement(out)
}

func parseOriginAdvertisement(out string) ([]plugin.GitOriginRef, error) {
	refs := map[string]plugin.GitOriginRef{}
	peels := map[string]string{}
	oidLength := 0
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Split(line, "\t")
		if len(parts) != 2 || !validOID(parts[0]) || allZero(parts[0]) {
			return nil, errors.New("invalid origin advertisement")
		}
		if oidLength == 0 {
			oidLength = len(parts[0])
		} else if len(parts[0]) != oidLength {
			return nil, errors.New("mixed origin object formats")
		}
		name := parts[1]
		if strings.HasSuffix(name, "^{}") {
			name = strings.TrimSuffix(name, "^{}")
			if !strings.HasPrefix(name, "refs/tags/") || !pluginOriginRef(name) || peels[name] != "" {
				return nil, errors.New("invalid origin tag peel")
			}
			peels[name] = parts[0]
		} else {
			if !pluginOriginRef(name) {
				return nil, errors.New("invalid origin ref")
			}
			if _, exists := refs[name]; exists {
				return nil, errors.New("duplicate origin ref")
			}
			refs[name] = plugin.GitOriginRef{Ref: name, OID: parts[0], CommitOID: parts[0]}
		}
		if len(refs) > originMaxRefs || len(peels) > originMaxRefs {
			return nil, errors.New("origin has too many refs")
		}
	}
	if scanner.Err() != nil {
		return nil, errors.New("invalid origin advertisement")
	}
	for name, oid := range peels {
		r, ok := refs[name]
		if !ok || r.OID == oid {
			return nil, errors.New("invalid origin tag peel")
		}
		r.CommitOID = oid
		refs[name] = r
	}
	result := make([]plugin.GitOriginRef, 0, len(refs))
	for _, r := range refs {
		result = append(result, r)
	}
	// Stable results make durable observation encoding and review deterministic.
	slicesSortOrigin(result)
	return result, nil
}

func pluginOriginRef(ref string) bool {
	return plugin.ValidGitOriginRef(ref)
}

func slicesSortOrigin(refs []plugin.GitOriginRef) {
	for i := 1; i < len(refs); i++ {
		for j := i; j > 0 && refs[j].Ref < refs[j-1].Ref; j-- {
			refs[j], refs[j-1] = refs[j-1], refs[j]
		}
	}
}

func (b *Backend) fetchOrigin(ctx context.Context, repo, remote string, selected plugin.GitOriginRef) (string, error) {
	if err := validOriginURL(remote); err != nil {
		return "", err
	}
	if !pluginOriginRef(selected.Ref) || !validOID(selected.OID) || !validOID(selected.CommitOID) {
		return "", control.ErrInvalid
	}
	transport, err := os.MkdirTemp(b.stateDir, "origin-fetch-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(transport)
	// This scratch repository contains no project-controlled config or refs.
	// FETCH_HEAD is written only here and used to detect a moved remote ref.
	if _, err = b.originGit(ctx, "/", nil, "init", "--bare", "--template=/dev/null", transport); err != nil {
		return "", err
	}
	objectDir := filepath.Join(repo, "objects")
	env := []string{"GIT_OBJECT_DIRECTORY=" + objectDir}
	if _, err = b.originGit(ctx, transport, env, "fetch", "--no-tags", "--no-recurse-submodules", "--no-auto-gc", remote, selected.Ref); err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(transport, "FETCH_HEAD"))
	if err != nil {
		return "", errors.New("origin fetch missing captured tip")
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		return "", errors.New("origin fetch returned multiple tips")
	}
	fields := strings.Fields(lines[0])
	if len(fields) < 1 || fields[0] != selected.OID {
		return "", errors.New("origin ref moved during fetch")
	}
	typ, err := b.originGit(ctx, transport, env, "cat-file", "-t", selected.CommitOID)
	if err != nil || strings.TrimSpace(typ) != "commit" {
		return "", errors.New("origin source is not a commit")
	}
	if selected.OID != selected.CommitOID {
		typ, err = b.originGit(ctx, transport, env, "cat-file", "-t", selected.OID)
		if err != nil || strings.TrimSpace(typ) != "tag" {
			return "", errors.New("origin annotated tag is invalid")
		}
		peeled, err := b.originGit(ctx, transport, env, "rev-parse", selected.OID+"^{commit}")
		if err != nil || strings.TrimSpace(peeled) != selected.CommitOID {
			return "", errors.New("origin tag peel changed")
		}
	}
	return selected.CommitOID, nil
}
