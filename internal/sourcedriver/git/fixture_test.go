package git

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/conformance"
)

// fixture runs the conformance suite over a private storage directory with
// real Git.
type fixture struct {
	t       *testing.T
	storage string
	driver  *Driver

	mu      sync.Mutex
	commits int

	// gitPath is the git the driver runs; origins serves the origin
	// controls' origins, through the ssh the driver runs.
	gitPath string
	origins *originServer
	// hook is the executable the driver's Git service runs as its
	// pre-receive hook: this test binary, which plays it.
	hook string
	// clients counts the service controls' clients.
	clients int
}

// TestMain lets the test binary play the parts Git runs it in, by its first
// argument: the pre-receive hook, as `p-source-git git-hook` does in
// production, and the roles other tests register in testBinaryRoles.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		if role, ok := testBinaryRoles[os.Args[1]]; ok {
			os.Exit(role(os.Args[2:]))
		}
	}
	os.Exit(m.Run())
}

// testBinaryRoles are the parts the test binary plays when Git runs it.
var testBinaryRoles = map[string]func(args []string) int{
	"git-hook": func([]string) int {
		if err := RunPreReceiveHook(context.Background(), os.Stdin); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	},
}

// shortTempDir is a temporary directory with a short path: the Git
// service's per-push socket lives in the storage, and a Unix socket's path
// must stay under 108 bytes, which t.TempDir's names, from the test's name,
// pass.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func newFixture(t *testing.T) conformance.Fixture {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git unavailable")
	}
	storage := filepath.Join(shortTempDir(t), "source")
	f := &fixture{t: t, storage: storage, gitPath: git, origins: newOriginServer(t, git), hook: testBinary()}
	f.driver = f.newDriver()
	return f
}

func (f *fixture) newDriver() *Driver {
	d, err := New(f.storage, driverConfig(f.gitPath, f.origins.ssh()), f.hook)
	if err != nil {
		f.t.Fatal(err)
	}
	return d
}

// testBinary is this test binary, which plays the driver's pre-receive
// hook (TestMain).
func testBinary() string {
	self, err := os.Executable()
	if err != nil {
		panic(err)
	}
	return self
}

// localConfig is the driver's config for a case that reaches no origin: the
// git on PATH, and an ssh that is never run.
func localConfig() json.RawMessage {
	git, err := exec.LookPath("git")
	if err != nil {
		git = "/nonexistent/git"
	}
	return driverConfig(git, "/nonexistent/ssh")
}

// driverConfig is the driver's config naming the git and ssh it runs.
func driverConfig(git, ssh string) json.RawMessage {
	config, err := json.Marshal(map[string]string{"git": git, "ssh": ssh})
	if err != nil {
		panic(err)
	}
	return config
}

func (f *fixture) Driver() sourcedriver.Driver { return f.driver }

// Restart closes the driver, which ends every service it runs, as the
// daemon's process group dies with a driver process, and makes a new one.
func (f *fixture) Restart() {
	f.driver.Close()
	f.driver = f.newDriver()
}

func (f *fixture) Natives() conformance.Natives                 { return f }
func (f *fixture) Repositories() conformance.RepositoryControls { return f }
func (f *fixture) Settling() conformance.SettlingControls       { return f }
func (f *fixture) Service() conformance.ServiceControls         { return f }
func (f *fixture) Origin() conformance.OriginControls           { return f }

// git runs git in the project's repository with input, as an
// administrator would, and returns its trimmed output.
func (f *fixture) git(project string, input string, args ...string) string {
	f.t.Helper()
	repo, err := f.driver.repository(project)
	if err != nil {
		f.t.Fatal(err)
	}
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Stdin = strings.NewReader(input)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=P Test", "GIT_AUTHOR_EMAIL=p@example.test",
		"GIT_COMMITTER_NAME=P Test", "GIT_COMMITTER_EMAIL=p@example.test", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		f.t.Fatalf("git %v: %v: %s", args, err, out.String())
	}
	return strings.TrimSpace(out.String())
}

func (f *fixture) Commit(project string, parents ...sourcedriver.OID) sourcedriver.OID {
	f.mu.Lock()
	f.commits++
	message := fmt.Sprintf("commit %d\n", f.commits)
	f.mu.Unlock()
	args := []string{"commit-tree", string(f.Tree(project))}
	for _, p := range parents {
		args = append(args, "-p", string(p))
	}
	return sourcedriver.OID(f.git(project, message, args...))
}

func (f *fixture) Tree(project string) sourcedriver.OID {
	return sourcedriver.OID(f.git(project, "", "hash-object", "-w", "-t", "tree", "--stdin"))
}

func (f *fixture) SetHead(project, name string, tip sourcedriver.OID) {
	if tip == "" {
		f.git(project, "", "update-ref", "--no-deref", "-d", "refs/heads/"+name)
		return
	}
	f.git(project, "", "update-ref", "--no-deref", "refs/heads/"+name, string(tip))
}

func (f *fixture) SetSymbolic(project, name, target string) {
	f.git(project, "", "symbolic-ref", "refs/heads/"+name, "refs/heads/"+target)
}

func TestConformance(t *testing.T) {
	conformance.Run(t, newFixture)
}
