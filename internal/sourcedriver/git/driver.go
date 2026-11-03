// Package git is P's bundled source driver: P's repositories as bare Git
// repositories in a private storage directory, Git run on them by the git
// executable, the Git service behind P's endpoint, and the origin transport
// through the daemon account's OpenSSH.
//
// The storage holds repositories/<sha256 of the project>.git, one bare
// repository per project, and git-hooks, the pre-receive hook of the Git
// service and its per-push sockets.
package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// Backend is the backend the bundled driver describes.
const Backend = "git"

// Version is the bundled driver's version.
const Version = "1"

// Driver is the bundled source driver.
type Driver struct {
	storage string // absolute and clean
	gitPath string // absolute

	// seams are the points where the conformance fixture acts inside a
	// call; all are empty in production.
	seams seams

	// services are the Git services ServeGit runs, for Settle and Close.
	services services
}

var _ sourcedriver.Driver = (*Driver)(nil)

// New returns the bundled driver over storage, a directory whose parent
// exists, with config, the driver's own settings from host configuration:
// a JSON object with no settings yet. It touches nothing; Check makes the
// storage.
func New(storage string, config json.RawMessage) (*Driver, error) {
	if !filepath.IsAbs(storage) || filepath.Clean(storage) != storage {
		return nil, errors.New("source storage must be an absolute, clean path")
	}
	var settings struct{}
	decoder := json.NewDecoder(bytes.NewReader(config))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil || bytes.TrimSpace(config)[0] != '{' {
		return nil, errors.New("the git source driver's config must be an empty JSON object")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(gitPath) {
		return nil, errors.New("the Git executable must resolve to an absolute path")
	}
	return &Driver{storage: storage, gitPath: gitPath}, nil
}

func (d *Driver) Describe() sourcedriver.Description {
	return sourcedriver.Description{Backend: Backend, Version: Version, Commands: []string{"git-upload-pack", "git-receive-pack"}}
}

// validProject accepts P's project names; the driver maps each to a path of
// its own, so this only refuses what core never sends.
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

// repository is the path of the project's bare repository. Logical names can
// contain `.git` components; mapping them into nested paths would let a.git/b
// land inside a's repository, so each name is hashed.
func (d *Driver) repository(project string) (string, error) {
	if !validProject(project) {
		return "", fmt.Errorf("project %q: %w", project, sourcedriver.ErrInvalid)
	}
	name := sha256.Sum256([]byte(project))
	return filepath.Join(d.storage, "repositories", hex.EncodeToString(name[:])+".git"), nil
}

// privateDirectory proves path a real directory owned by P's account that no
// one else may write.
func privateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("%s is not a private directory owned by P's account", path)
	}
	return nil
}

// gitEnv is the environment of every Git command on P's repositories: no
// system or global configuration, no replace objects, no optional locks, no
// lazy fetch, and configuration that overrides the repository's own so that
// it runs no hook, no fsmonitor and no implicit maintenance.
func (d *Driver) gitEnv(extra ...string) []string {
	base := []string{"PATH=" + filepath.Dir(d.gitPath), "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_REPLACE_OBJECTS=1", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1"}
	return append(append(base, repositoryGuardEnv()...), extra...)
}

// repositoryGuardEnv is noImplicitMaintenanceEnv with no hooks and no
// fsmonitor. Configuration from the environment overrides the repository's
// config but not a command line's -c, so the Git service's receive-pack
// still runs P's own hook from its core.hooksPath.
func repositoryGuardEnv() []string {
	return []string{
		"GIT_CONFIG_COUNT=5",
		"GIT_CONFIG_KEY_0=gc.auto", "GIT_CONFIG_VALUE_0=0",
		"GIT_CONFIG_KEY_1=maintenance.auto", "GIT_CONFIG_VALUE_1=false",
		"GIT_CONFIG_KEY_2=receive.autogc", "GIT_CONFIG_VALUE_2=false",
		"GIT_CONFIG_KEY_3=core.hooksPath", "GIT_CONFIG_VALUE_3=/dev/null",
		"GIT_CONFIG_KEY_4=core.fsmonitor", "GIT_CONFIG_VALUE_4=false",
	}
}

// Pending origin-source operations keep their captured commit as an object
// before the ordinary P head exists. Git operations that can run native
// maintenance must leave unreachable objects intact; receive-pack otherwise
// may auto-prune. Any future explicit P GC must account for pending creation
// evidence.
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

// boundedOutput keeps at most max bytes and fails a write past them.
type boundedOutput struct {
	buf      bytes.Buffer
	max      int
	exceeded bool // a write went past max
}

func (o *boundedOutput) Write(p []byte) (int, error) {
	if o.buf.Len()+len(p) > o.max {
		o.exceeded = true
		return 0, errors.New("Git output exceeded its bound")
	}
	return o.buf.Write(p)
}

// gitResult is how one Git command ended.
type gitResult struct {
	Stdout string // whole, untrimmed
	// Exit is Git's exit status; 0 when it succeeded. A command that could
	// not run, was cut off by ctx or overflowed its output bound returns an
	// error instead.
	Exit int
	// Incomplete is whether Git's standard error says its answer may leave
	// something out: an error, or a ref it skipped as broken. Some reads
	// say so and still exit 0: cat-file's batch answers an object it cannot
	// read as missing, and for-each-ref leaves out a head whose ref or tip
	// it cannot read. Other warnings, such as a deprecated setting, do not
	// count.
	Incomplete bool
}

// leftOut are the starts of the lines with which Git says that its answer
// may leave something out. Git runs in the C locale, so they are not
// translated.
var leftOut = [][]byte{
	[]byte("error:"),
	[]byte("fatal:"),
	[]byte("warning: ignoring broken ref "),
	[]byte("warning: ignoring ref with broken name "),
}

// leavesOut reports whether Git's standard error holds one of leftOut.
func leavesOut(stderr []byte) bool {
	for line := range bytes.Lines(stderr) {
		for _, start := range leftOut {
			if bytes.HasPrefix(line, start) {
				return true
			}
		}
	}
	return false
}

// run runs git with args in dir under the repository environment plus env,
// keeping at most max bytes of output. Git's own failure is a nonzero Exit
// with no error, so a caller can tell an absent object or ref from a failed
// read; diagnostics never leave the driver.
func (d *Driver) run(ctx context.Context, dir string, env []string, max int, args ...string) (gitResult, error) {
	return d.runInput(ctx, dir, env, "", max, args...)
}

// runInput is run with input on Git's standard input.
func (d *Driver) runInput(ctx context.Context, dir string, env []string, input string, max int, args ...string) (gitResult, error) {
	if err := ctx.Err(); err != nil {
		return gitResult{}, err
	}
	cmd := exec.CommandContext(ctx, d.gitPath, args...)
	cmd.Dir = dir
	cmd.Env = d.gitEnv(env...)
	cmd.Stdin = strings.NewReader(input)
	stdout, stderr := &boundedOutput{max: max}, &boundedOutput{max: 4096}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return gitResult{}, ctx.Err()
	}
	incomplete := leavesOut(stderr.buf.Bytes())
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() > 0 {
		return gitResult{Stdout: stdout.buf.String(), Exit: exit.ExitCode(), Incomplete: incomplete}, nil
	}
	if err != nil {
		return gitResult{}, fmt.Errorf("git %s could not run: %w", args[0], err)
	}
	return gitResult{Stdout: stdout.buf.String(), Incomplete: incomplete}, nil
}
