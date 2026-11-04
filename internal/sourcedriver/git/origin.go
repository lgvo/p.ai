package git

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/gitnames"
)

// originMaxOutput bounds what Git may print about an origin.
const originMaxOutput = 300 << 10

// ObserveOrigin lists the origin's heads and tags with `git ls-remote`.
func (d *Driver) ObserveOrigin(ctx context.Context, url string) ([]sourcedriver.OriginRef, error) {
	if !gitnames.OriginURL(url) {
		return nil, fmt.Errorf("origin URL: %w", sourcedriver.ErrInvalid)
	}
	out, err := d.originGit(ctx, "/", nil, "ls-remote", "--heads", "--tags", url)
	if err != nil {
		return nil, err
	}
	return parseAdvertisement(out)
}

// parseAdvertisement reads `git ls-remote --heads --tags`: a line
// "<id>\t<ref>" per ref, and for an annotated tag a line
// "<peeled id>\t<ref>^{}". It refuses any other line, mixed object formats,
// a repeated ref, a peel of no tag and more than MaxOriginRefs refs, so it
// never returns part of an answer.
func parseAdvertisement(out string) ([]sourcedriver.OriginRef, error) {
	malformed := errors.New("the origin's advertisement is malformed")
	refs := map[string]sourcedriver.OriginRef{}
	peels := map[string]sourcedriver.OID{}
	idLength := 0
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		id, name, ok := strings.Cut(scanner.Text(), "\t")
		if !ok || strings.Contains(name, "\t") || !gitnames.OID(id) {
			return nil, malformed
		}
		if idLength == 0 {
			idLength = len(id)
		} else if len(id) != idLength {
			return nil, errors.New("the origin advertises mixed object formats")
		}
		if tag, peeled := strings.CutSuffix(name, "^{}"); peeled {
			if !strings.HasPrefix(tag, "refs/tags/") || !gitnames.OriginRef(tag) || peels[tag] != "" {
				return nil, malformed
			}
			peels[tag] = sourcedriver.OID(id)
		} else {
			if !gitnames.OriginRef(name) {
				return nil, malformed
			}
			if _, repeated := refs[name]; repeated {
				return nil, errors.New("the origin advertises a ref twice")
			}
			refs[name] = sourcedriver.OriginRef{Ref: name, OID: sourcedriver.OID(id), Target: sourcedriver.OID(id)}
		}
		if len(refs) > sourcedriver.MaxOriginRefs || len(peels) > sourcedriver.MaxOriginRefs {
			return nil, fmt.Errorf("the origin advertises more than %d refs: %w", sourcedriver.MaxOriginRefs, sourcedriver.ErrLimit)
		}
	}
	if scanner.Err() != nil {
		return nil, malformed
	}
	for name, target := range peels {
		r, ok := refs[name]
		if !ok || r.OID == target {
			return nil, malformed
		}
		r.Target = target
		refs[name] = r
	}
	result := make([]sourcedriver.OriginRef, 0, len(refs))
	for _, r := range refs {
		result = append(result, r)
	}
	slices.SortFunc(result, func(a, b sourcedriver.OriginRef) int { return strings.Compare(a.Ref, b.Ref) })
	return result, nil
}

// FetchOrigin brings the observed ref's objects into the project's
// repository and returns its commit.
func (d *Driver) FetchOrigin(ctx context.Context, project, url string, ref sourcedriver.OriginRef) (sourcedriver.OID, error) {
	if !gitnames.OriginURL(url) || !validOriginRef(ref) {
		return "", fmt.Errorf("origin URL or ref: %w", sourcedriver.ErrInvalid)
	}
	repo, err := d.originRepository(ctx, project)
	if err != nil {
		return "", err
	}
	if err := d.fetchObserved(ctx, repo, url, ref); err != nil {
		return "", err
	}
	return ref.Target, nil
}

// validOriginRef refuses an observed ref no ObserveOrigin returns.
func validOriginRef(ref sourcedriver.OriginRef) bool {
	return gitnames.OriginRef(ref.Ref) && gitnames.OID(string(ref.OID)) && gitnames.OID(string(ref.Target))
}

// originRepository is the project's repository for an origin operation
// that adds objects to it: the storage is proved private first, as before
// every mutation, and no repository is ErrNotFound.
func (d *Driver) originRepository(ctx context.Context, project string) (string, error) {
	repo, err := d.repository(project)
	if err != nil {
		return "", err
	}
	if err := d.prepareStorage(); err != nil {
		return "", err
	}
	r, err := d.inspectAt(ctx, project, repo)
	if err != nil {
		return "", err
	}
	if !r.Exists {
		return "", fmt.Errorf("project %q: %w", project, sourcedriver.ErrNotFound)
	}
	return repo, nil
}

// fetchObserved fetches ref from the origin into the objects of repo and
// proves the observation: the origin's ref still names ref.OID, which is the
// commit ref.Target or an annotated tag that peels to it. The fetch runs in
// a scratch repository with no config, hooks or refs of the project's, which
// sees repo's objects only through GIT_OBJECT_DIRECTORY, so it creates no
// ref in repo and its FETCH_HEAD, which shows a moved ref, stays there.
func (d *Driver) fetchObserved(ctx context.Context, repo, url string, ref sourcedriver.OriginRef) error {
	transport, err := d.scratchRepository(ctx, "origin-fetch-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(transport)
	objects := "GIT_OBJECT_DIRECTORY=" + filepath.Join(repo, "objects")
	if _, err := d.originGit(ctx, transport, []string{objects}, "fetch", "--no-tags", "--no-recurse-submodules", "--no-auto-gc", url, ref.Ref); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(transport, "FETCH_HEAD"))
	if err != nil {
		return errors.New("the origin fetch recorded no tip")
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		return errors.New("the origin fetch recorded several tips")
	}
	if tip, _, _ := strings.Cut(lines[0], "\t"); tip != string(ref.OID) {
		return fmt.Errorf("the origin's %s moved since the observation: %w", ref.Ref, sourcedriver.ErrConflict)
	}
	return d.confirmObserved(ctx, repo, ref)
}

// confirmObserved proves, from the objects fetched into repo, that ref.OID
// is the commit ref.Target or an annotated tag that peels to it.
func (d *Driver) confirmObserved(ctx context.Context, repo string, ref sourcedriver.OriginRef) error {
	want := "commit"
	if ref.OID != ref.Target {
		want = "tag"
	}
	typ, err := d.objectType(ctx, repo, ref.OID)
	if err != nil {
		return err
	}
	if typ != want {
		return fmt.Errorf("the origin's %s is a %q object, not a %s: %w", ref.Ref, typ, want, sourcedriver.ErrConflict)
	}
	if ref.OID == ref.Target {
		return nil
	}
	peeled, err := d.git(ctx, repo, 128, "rev-parse", "--verify", "--quiet", string(ref.OID)+"^{commit}")
	if err != nil {
		return err
	}
	switch {
	case peeled.Exit == 1:
		return fmt.Errorf("the origin's tag %s peels to no commit: %w", ref.Ref, sourcedriver.ErrConflict)
	case peeled.Exit != 0:
		return errors.New("peeling the origin's tag failed")
	case strings.TrimSpace(peeled.Stdout) != string(ref.Target):
		return fmt.Errorf("the origin's tag %s peels to another commit than observed: %w", ref.Ref, sourcedriver.ErrConflict)
	}
	return nil
}

// objectType is the type of object id in repo, or empty when repo does not
// hold it.
func (d *Driver) objectType(ctx context.Context, repo string, id sourcedriver.OID) (string, error) {
	exists, err := d.git(ctx, repo, 64, "cat-file", "-e", string(id))
	if err != nil {
		return "", err
	}
	switch exists.Exit {
	case 0:
	case 1:
		return "", nil
	default:
		return "", errors.New("reading an object failed")
	}
	typ, err := d.git(ctx, repo, 64, "cat-file", "-t", string(id))
	if err != nil {
		return "", err
	}
	if typ.Exit != 0 {
		return "", errors.New("reading an object's type failed")
	}
	return strings.TrimSpace(typ.Stdout), nil
}

// scratchRepository makes an empty bare repository in the storage, with no
// template, for one origin transfer; the caller removes it.
func (d *Driver) scratchRepository(ctx context.Context, prefix string) (string, error) {
	dir, err := os.MkdirTemp(d.storage, prefix)
	if err != nil {
		return "", err
	}
	initialized, err := d.run(ctx, d.storage, nil, 4096, "init", "--quiet", "--bare", "--template=/dev/null", dir)
	if err == nil && initialized.Exit != 0 {
		err = errors.New("git init of a scratch repository failed")
	}
	if err != nil {
		return "", errors.Join(err, os.RemoveAll(dir))
	}
	return dir, nil
}

// originGit runs git against an origin and returns what it printed. Any
// failure of Git is ErrUnavailable: Git does not tell an origin that could
// not be reached from one that refused, and either way it answered nothing.
func (d *Driver) originGit(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	token, remove, err := d.recordOrigin()
	if err != nil {
		return "", fmt.Errorf("the git against the origin could not be recorded: %w", err)
	}
	defer remove()
	base := []string{"-c", "protocol.allow=never", "-c", "protocol.ssh.allow=always", "-c", "core.hooksPath=/dev/null", "-c", "credential.helper=", "-c", "fetch.autoGC=false", "-c", "core.fsmonitor=false"}
	cmd := d.originCommand(ctx, dir, token, env, append(append(base, noImplicitMaintenanceArgs()...), args...)...)
	out, diagnostic := &boundedOutput{max: originMaxOutput}, &boundedOutput{max: 4096}
	cmd.Stdout, cmd.Stderr = out, diagnostic
	err = cmd.Run()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if out.exceeded {
		return "", fmt.Errorf("the origin's answer exceeds %d bytes: %w", originMaxOutput, sourcedriver.ErrLimit)
	}
	if err != nil {
		return "", fmt.Errorf("git %s against the origin failed: %w", args[0], sourcedriver.ErrUnavailable)
	}
	return out.buf.String(), nil
}

// originCommand is a git command against an origin, through the OpenSSH
// the driver's config names, in its own process group, which cancellation
// kills whole so that an ssh descendant holding Git's pipes cannot outlive
// it. Its environment carries token, the token of the record recordOrigin
// wrote for it, which everything it starts inherits.
func (d *Driver) originCommand(ctx context.Context, dir, token string, env []string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, d.gitPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 250 * time.Millisecond
	cmd.Dir = dir
	cmd.Env = d.originEnv(append(env, originTokenVariable+"="+token)...)
	return cmd
}

// originEnv is the environment of Git against an origin: the daemon
// account's HOME, for its OpenSSH configuration and known hosts, and its
// SSH agent; no system or global Git configuration, no prompt, and an SSH
// command that never asks.
func (d *Driver) originEnv(extra ...string) []string {
	// GIT_SSH_COMMAND is constant and holds no URL or ref. Git runs it
	// through a shell, so the trusted executable path is quoted as one word.
	quoted := "'" + strings.ReplaceAll(d.sshPath, "'", `'\''`) + "'"
	env := []string{
		"PATH=" + filepath.Dir(d.sshPath) + ":" + filepath.Dir(d.gitPath),
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

// userHome is the daemon account's home, or a path that does not exist.
func userHome() string {
	h, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(h) {
		return "/nonexistent"
	}
	return h
}
