package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

// originServer serves a fixture's origins: bare repositories in dir, which
// the driver reaches through the ssh script at dir/bin/ssh, the ssh its
// config names. The URL git@fixture:o<n>.git names dir/o<n>.git.
type originServer struct {
	dir   string
	count int
}

const sshScript = `#!/bin/sh
for command; do :; done
[ "$GIT_TERMINAL_PROMPT" = 0 ] || exit 81
case "$GIT_SSH_COMMAND" in *BatchMode=yes*) ;; *) exit 80 ;; esac
printf '%s %s %s %s\n' "${GIT_CONFIG_GLOBAL-unset}" "${GIT_CONFIG_COUNT-unset}" "${GIT_DIR-unset}" "${GIT_OBJECT_DIRECTORY-unset}" >> @DIR@/ssh-env
case "$command" in
"git-upload-pack '"*"'") service=upload-pack ;;
"git-receive-pack '"*"'") service=receive-pack ;;
*) exit 70 ;;
esac
path=${command#* \'}
repo=@DIR@/${path%\'}
if [ -e "$repo.cut-off" ]; then echo 'ssh: connect to host fixture port 22: Connection refused' >&2; exit 255; fi
[ -d "$repo" ] || exit 71
if [ "$service" = receive-pack ] && [ -e "$repo.lose-answer" ]; then
	rm -f "$repo.lose-answer"
	@GIT@ receive-pack "$repo"
	exit 55
fi
exec @GIT@ "$service" "$repo"
`

// shellQuote quotes s as one word of a POSIX shell.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// newOriginServer writes the ssh script of a fixture's origins into a new
// directory.
func newOriginServer(t *testing.T, git string) *originServer {
	t.Helper()
	dir := t.TempDir()
	script := strings.NewReplacer("@DIR@", shellQuote(dir), "@GIT@", shellQuote(git)).Replace(sshScript)
	if err := os.Mkdir(filepath.Join(dir, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	s := &originServer{dir: dir}
	if err := os.WriteFile(s.ssh(), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return s
}

// ssh is the path of the script that stands in for OpenSSH.
func (s *originServer) ssh() string { return filepath.Join(s.dir, "bin", "ssh") }

func (f *fixture) originServer() *originServer { return f.origins }

// originRepository is the bare repository of the origin at url.
func (f *fixture) originRepository(url string) string {
	name, ok := strings.CutPrefix(url, "git@fixture:")
	if !ok {
		f.t.Fatalf("%q is not a fixture origin", url)
	}
	return filepath.Join(f.originServer().dir, name)
}

// originGit runs git on the origin at url, as its administrator would, and
// returns its trimmed output.
func (f *fixture) originGit(url, input string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", append([]string{"--git-dir=" + f.originRepository(url)}, args...)...)
	cmd.Stdin = strings.NewReader(input)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=P Test", "GIT_AUTHOR_EMAIL=p@example.test",
		"GIT_COMMITTER_NAME=P Test", "GIT_COMMITTER_EMAIL=p@example.test", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		f.t.Fatalf("git %v on the origin: %v: %s", args, err, out.String())
	}
	return strings.TrimSpace(out.String())
}

func (f *fixture) NewOrigin() string {
	server := f.originServer()
	f.mu.Lock()
	server.count++
	url := fmt.Sprintf("git@fixture:o%d.git", server.count)
	f.mu.Unlock()
	cmd := exec.Command("git", "init", "--quiet", "--bare", f.originRepository(url))
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		f.t.Fatalf("git init of an origin: %v: %s", err, out)
	}
	return url
}

func (f *fixture) OriginCommit(url string, parents ...sourcedriver.OID) sourcedriver.OID {
	f.mu.Lock()
	f.commits++
	message := fmt.Sprintf("origin commit %d\n", f.commits)
	f.mu.Unlock()
	args := []string{"commit-tree", string(f.OriginTree(url))}
	for _, p := range parents {
		args = append(args, "-p", string(p))
	}
	return sourcedriver.OID(f.originGit(url, message, args...))
}

func (f *fixture) OriginTree(url string) sourcedriver.OID {
	return sourcedriver.OID(f.originGit(url, "", "hash-object", "-w", "-t", "tree", "--stdin"))
}

func (f *fixture) SetOriginRef(url, ref string, id sourcedriver.OID) {
	if id == "" {
		f.originGit(url, "", "update-ref", "-d", ref)
		return
	}
	f.originGit(url, "", "update-ref", ref, string(id))
}

func (f *fixture) OriginAnnotatedTag(url, name string, target sourcedriver.OID) sourcedriver.OID {
	f.originGit(url, "", "tag", "--annotate", "--message=release "+name, name, string(target))
	return sourcedriver.OID(f.originGit(url, "", "rev-parse", "refs/tags/"+name))
}

func (f *fixture) OriginRef(url, ref string) sourcedriver.OID {
	for _, line := range strings.Split(f.originGit(url, "", "for-each-ref", "--format=%(refname) %(objectname)"), "\n") {
		if name, id, _ := strings.Cut(line, " "); name == ref {
			return sourcedriver.OID(id)
		}
	}
	return ""
}

func (f *fixture) CutOffOrigin(url string) { f.originMarker(url, ".cut-off") }

func (f *fixture) LoseNextOriginAnswer(url string) { f.originMarker(url, ".lose-answer") }

// RejectNextOriginPush installs a pre-receive hook in the origin that
// refuses one push and removes itself.
func (f *fixture) RejectNextOriginPush(url string) {
	hook := filepath.Join(f.originRepository(url), "hooks", "pre-receive")
	if err := os.MkdirAll(filepath.Dir(hook), 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nrm -f \"$0\"\necho 'the fixture refuses this push' >&2\nexit 1\n"), 0o700); err != nil {
		f.t.Fatal(err)
	}
}

// originMarker leaves the marker beside the origin that the ssh script
// reads.
func (f *fixture) originMarker(url, suffix string) {
	if err := os.WriteFile(f.originRepository(url)+suffix, nil, 0o600); err != nil {
		f.t.Fatal(err)
	}
}

const (
	oidA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	oidB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestParseAdvertisement(t *testing.T) {
	refs, err := parseAdvertisement("")
	if err != nil || len(refs) != 0 {
		t.Fatalf("an empty advertisement = %+v, %v; want no refs", refs, err)
	}
	refs, err = parseAdvertisement(oidB + "\trefs/tags/v1^{}\n" + oidA + "\trefs/heads/main\n" + oidA + "\trefs/tags/v1\n" + oidA + "\trefs/heads/café\n")
	want := []sourcedriver.OriginRef{
		{Ref: "refs/heads/café", OID: oidA, Target: oidA},
		{Ref: "refs/heads/main", OID: oidA, Target: oidA},
		{Ref: "refs/tags/v1", OID: oidA, Target: oidB},
	}
	if err != nil || !reflect.DeepEqual(refs, want) {
		t.Fatalf("parseAdvertisement = %+v, %v; want %+v", refs, err, want)
	}
	for _, bad := range []string{
		oidA + "\trefs/tags/v1^{}\n",                                                    // a peel of no tag
		oidA + "\trefs/heads/main\n" + oidA + "\trefs/heads/main^{}\n",                  // a peeled head
		oidA + "\trefs/tags/v1\n" + oidA + "\trefs/tags/v1^{}\n",                        // a peel to the tag itself
		oidA + "\trefs/heads/main\n" + oidB + "\trefs/heads/main\n",                     // a repeated ref
		oidA + "\trefs/heads/main\n" + strings.Repeat("b", 64) + "\trefs/heads/other\n", // mixed object formats
		oidA + "\trefs/heads/x?evil\n",
		oidA + "\trefs/remotes/origin/main\n",
		oidA + "\trefs/heads/main\textra\n",
		strings.Repeat("0", 40) + "\trefs/heads/main\n",
		"not-an-oid\trefs/heads/main\n",
		oidA + "\trefs/heads/" + string([]byte{0xff}) + "\n",
	} {
		if refs, err := parseAdvertisement(bad); err == nil {
			t.Errorf("parseAdvertisement(%q) = %+v, want an error", bad, refs)
		}
	}
	var large strings.Builder
	for i := range sourcedriver.MaxOriginRefs + 1 {
		fmt.Fprintf(&large, "%s\trefs/heads/r%04d\n", oidA, i)
	}
	if refs, err := parseAdvertisement(large.String()); !errors.Is(err, sourcedriver.ErrLimit) || refs != nil {
		t.Fatalf("an advertisement of %d refs = %d refs, %v; want ErrLimit and none", sourcedriver.MaxOriginRefs+1, len(refs), err)
	}
}

func TestObserveOriginRefusesAnAdvertisementPastItsOutputBound(t *testing.T) {
	f := newFixture(t).(*fixture)
	url := f.NewOrigin()
	tip := f.OriginCommit(url)
	long := strings.Repeat("n", 120) + "/" + strings.Repeat("n", 120) + "/" + strings.Repeat("n", 120)
	var input strings.Builder
	for i := range 800 { // well under MaxOriginRefs, over originMaxOutput bytes
		fmt.Fprintf(&input, "update refs/heads/%s/%04d %s\n", long, i, tip)
	}
	f.originGit(url, input.String(), "update-ref", "--stdin")
	if refs, err := f.Driver().ObserveOrigin(t.Context(), url); !errors.Is(err, sourcedriver.ErrLimit) || refs != nil {
		t.Fatalf("ObserveOrigin of an oversized advertisement = %d refs, %v; want ErrLimit", len(refs), err)
	}
}

// TestOriginGitKeepsAmbientAndProjectConfigOut runs every origin operation
// with a hostile caller environment and hostile project configuration: the
// operations still reach the fixture's origin through its ssh, run no hook,
// write no ref, FETCH_HEAD or config into the project's repository, push no
// tag, and pass none of the caller's Git variables to ssh.
func TestOriginGitKeepsAmbientAndProjectConfigOut(t *testing.T) {
	f := newFixture(t).(*fixture)
	url := f.NewOrigin()
	base := f.OriginCommit(url)
	f.SetOriginRef(url, "refs/heads/main", base)
	f.OriginAnnotatedTag(url, "v1", base)
	d := f.Driver()
	if _, err := d.CreateRepository(t.Context(), "team/app"); err != nil {
		t.Fatal(err)
	}
	repo, err := f.driver.repository("team/app")
	if err != nil {
		t.Fatal(err)
	}
	refs := map[string]sourcedriver.OriginRef{}
	observed, err := d.ObserveOrigin(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range observed {
		refs[r.Ref] = r
	}
	if _, err := d.FetchOrigin(t.Context(), "team/app", url, refs["refs/heads/main"]); err != nil {
		t.Fatal(err)
	}
	source := f.Commit("team/app", base)
	f.SetHead("team/app", "work", source)
	f.git("team/app", "", "tag", "--annotate", "--message=local", "local", string(source))

	root := t.TempDir()
	hookMarker := filepath.Join(root, "hook-ran")
	hooks := filepath.Join(root, "hooks")
	if err := os.Mkdir(hooks, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, hook := range []string{"reference-transaction", "post-checkout", "pre-push"} {
		if err := os.WriteFile(filepath.Join(hooks, hook), []byte("#!/bin/sh\ntouch "+shellQuote(hookMarker)+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f.git("team/app", "", "config", "core.hooksPath", hooks)
	f.git("team/app", "", "config", "core.sshCommand", "false")
	f.git("team/app", "", "config", "url.file:///nonexistent/.insteadOf", "git@fixture:")
	f.git("team/app", "", "config", "remote.origin.uploadpack", "false")
	f.git("team/app", "", "config", "credential.helper", "false")
	f.git("team/app", "", "config", "push.followTags", "true")
	configBefore, err := os.ReadFile(filepath.Join(repo, "config"))
	if err != nil {
		t.Fatal(err)
	}
	refsBefore := f.git("team/app", "", "for-each-ref", "--format=%(refname)")

	hostileGlobal := filepath.Join(root, "hostile-global")
	if err := os.WriteFile(hostileGlobal, []byte("[url \"file:///nonexistent/\"]\n\tinsteadOf = git@fixture:\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", hostileGlobal)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.sshCommand")
	t.Setenv("GIT_CONFIG_VALUE_0", "false")
	t.Setenv("GIT_SSH_COMMAND", "false")
	t.Setenv("GIT_DIR", f.originRepository(url))
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(root, "elsewhere"))

	if _, err := d.ObserveOrigin(t.Context(), url); err != nil {
		t.Fatalf("ObserveOrigin under a hostile environment: %v", err)
	}
	if _, err := d.FetchOrigin(t.Context(), "team/app", url, refs["refs/tags/v1"]); err != nil {
		t.Fatalf("FetchOrigin under a hostile environment: %v", err)
	}
	main := refs["refs/heads/main"]
	if relation, err := d.ComparePublication(t.Context(), "team/app", url, source, &main); err != nil || relation != sourcedriver.RelationFastForward {
		t.Fatalf("ComparePublication under a hostile environment = %q, %v; want fast_forward", relation, err)
	}
	publication := sourcedriver.Publication{URL: url, Branch: "work", Commit: source, Destination: "main", Relation: sourcedriver.RelationFastForward}
	if status, err := d.Publish(t.Context(), "team/app", publication, func() error { return nil }); err != nil || status != sourcedriver.PublishAdvanced {
		t.Fatalf("Publish under a hostile environment = %q, %v; want advanced", status, err)
	}
	for _, name := range []string{"GIT_CONFIG_GLOBAL", "GIT_CONFIG_COUNT", "GIT_SSH_COMMAND", "GIT_DIR", "GIT_OBJECT_DIRECTORY"} {
		os.Unsetenv(name) // t.Setenv restores them when the test ends
	}

	if got := f.originGit(url, "", "for-each-ref", "--format=%(refname)", "refs/tags/"); got != "refs/tags/v1" {
		t.Fatalf("the origin's tags after the push are %q, want only refs/tags/v1", got)
	}
	if got := f.git("team/app", "", "for-each-ref", "--format=%(refname)"); got != refsBefore {
		t.Fatalf("the project's refs are %q, want %q as before", got, refsBefore)
	}
	if _, err := os.Stat(filepath.Join(repo, "FETCH_HEAD")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an origin operation wrote FETCH_HEAD into the project's repository: %v", err)
	}
	if configAfter, err := os.ReadFile(filepath.Join(repo, "config")); err != nil || !bytes.Equal(configAfter, configBefore) {
		t.Fatalf("an origin operation changed the project's config: %v", err)
	}
	if _, err := os.Stat(hookMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a hook of the project's configuration ran: %v", err)
	}
	log, err := os.ReadFile(filepath.Join(f.originServer().dir, "ssh-env"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(log)), "\n") {
		if line != "/dev/null unset unset unset" {
			t.Fatalf("ssh saw GIT_CONFIG_GLOBAL GIT_CONFIG_COUNT GIT_DIR GIT_OBJECT_DIRECTORY = %q, want /dev/null and none of the others", line)
		}
	}
}

// TestOriginGitDoesNotWaitForEscapedSSHDescendants checks that a cancelled
// origin command returns within its bound although an ssh descendant in
// another session still holds Git's pipes.
func TestOriginGitDoesNotWaitForEscapedSSHDescendants(t *testing.T) {
	setsid, se := exec.LookPath("setsid")
	sleep, le := exec.LookPath("sleep")
	if se != nil || le != nil {
		t.Skip("setsid or sleep unavailable")
	}
	f := newFixture(t).(*fixture)
	pidFile := filepath.Join(t.TempDir(), "child-pid")
	script := "#!/bin/sh\n" + shellQuote(setsid) + " " + shellQuote(sleep) + " 4 &\nprintf '%s' \"$!\" > " + shellQuote(pidFile) + "\nexit 1\n"
	if err := os.WriteFile(f.originServer().ssh(), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	if refs, err := f.Driver().ObserveOrigin(ctx, "git@fixture:repo.git"); err == nil {
		t.Fatalf("ObserveOrigin through a failing ssh = %+v, want an error", refs)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("ObserveOrigin waited %s for an escaped ssh descendant", elapsed)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the ssh stand-in did not run: %v", err)
	}
	if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
		syscall.Kill(pid, syscall.SIGKILL)
	}
}
