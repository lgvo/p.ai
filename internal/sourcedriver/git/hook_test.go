package git

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lgvo/p.ai/internal/sourcedriver"
)

func TestPreReceiveHookAsksOnlyForOneCommitFastForwardOrCreationOfTheAllowedHead(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git unavailable")
	}
	dir := shortTempDir(t)
	bare, work := filepath.Join(dir, "bare.git"), filepath.Join(dir, "work")
	hookGit(t, "init", "--quiet", "--bare", bare)
	hookGit(t, "init", "--quiet", work)
	for i := 0; i < 2; i++ {
		if err := os.WriteFile(filepath.Join(work, "file"), []byte(fmt.Sprint(i)), 0o600); err != nil {
			t.Fatal(err)
		}
		hookGit(t, "-C", work, "add", "file")
		hookGit(t, "-C", work, "commit", "--quiet", "-m", fmt.Sprintf("c%d", i))
		hookGit(t, "-C", work, "push", "--quiet", bare, "HEAD:refs/heads/work")
	}
	newID := hookGit(t, "-C", work, "rev-parse", "HEAD")
	oldID := hookGit(t, "-C", work, "rev-parse", "HEAD^")
	tree := hookGit(t, "-C", work, "rev-parse", "HEAD^{tree}")
	zero := strings.Repeat("0", 40)
	socket := filepath.Join(dir, "hook.sock")
	t.Setenv("P_GIT_ALLOWED_REF", "refs/heads/work")
	t.Setenv("P_GIT_EXEC", gitPath)
	t.Setenv("GIT_DIR", bare)
	t.Setenv("P_GIT_HOOK_TOKEN", strings.Repeat("d", 48))
	t.Setenv("P_GIT_HOOK_SOCKET", socket)
	for _, c := range []struct {
		what, input string
		allowed     bool
	}{
		{"a fast-forward", oldID + " " + newID + " refs/heads/work", true},
		{"a creation", zero + " " + newID + " refs/heads/work", true},
		{"a rewind", newID + " " + oldID + " refs/heads/work", false},
		{"another head", oldID + " " + newID + " refs/heads/other", false},
		{"a deletion", oldID + " " + zero + " refs/heads/work", false},
		{"a tree", zero + " " + tree + " refs/heads/work", false},
		{"two updates", oldID + " " + newID + " refs/heads/work\n" + oldID + " " + newID + " refs/heads/work", false},
	} {
		asked, stop := answerHook(t, socket)
		err := RunPreReceiveHook(context.Background(), strings.NewReader(c.input+"\n"))
		stop()
		if (err == nil) != c.allowed {
			t.Errorf("the hook on %s: %v", c.what, err)
		}
		if got := <-asked; got != c.allowed {
			t.Errorf("the hook on %s asked the driver: %t, want %t", c.what, got, c.allowed)
		}
	}
}

// answerHook approves one request on socket; once stop is called, asked
// reports whether one came.
func answerHook(t *testing.T, socket string) (asked <-chan bool, stop func()) {
	t.Helper()
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	came := make(chan bool, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			came <- false
			return
		}
		defer conn.Close()
		var request [1024]byte
		_, _ = conn.Read(request[:])
		_, _ = conn.Write([]byte("ok\n"))
		came <- true
	}()
	return came, func() { listener.Close() }
}

func hookGit(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=P Test", "GIT_AUTHOR_EMAIL=p@example.test", "GIT_COMMITTER_NAME=P Test", "GIT_COMMITTER_EMAIL=p@example.test")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestTheApprovalSocketApprovesOnlyItsPushsRequestForItsHeadOnce(t *testing.T) {
	d := &Driver{storage: shortTempDir(t)}
	if err := os.Mkdir(d.hooksDirectory(), 0o700); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 40)
	zero := strings.Repeat("0", 40)
	for _, c := range []struct {
		what     string
		request  func(token string) hookRequest
		approved bool
	}{
		{"a request of another push", func(string) hookRequest {
			return hookRequest{Token: strings.Repeat("e", 48), Ref: "refs/heads/work", Old: zero, New: id}
		}, false},
		{"a request for another head", func(token string) hookRequest {
			return hookRequest{Token: token, Ref: "refs/heads/other", Old: zero, New: id}
		}, false},
		{"a request with a malformed ID", func(token string) hookRequest {
			return hookRequest{Token: token, Ref: "refs/heads/work", Old: zero, New: "abc"}
		}, false},
		{"a deletion", func(token string) hookRequest {
			return hookRequest{Token: token, Ref: "refs/heads/work", Old: id, New: zero}
		}, false},
		{"the push's creation of its head", func(token string) hookRequest {
			return hookRequest{Token: token, Ref: "refs/heads/work", Old: zero, New: id}
		}, true},
	} {
		var calls []string
		a, err := d.startApproval(t.Context(), "work", func(branch string, old, new sourcedriver.OID) error {
			calls = append(calls, fmt.Sprintf("%s %q %s", branch, old, new))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		r := c.request(a.token)
		t.Setenv("P_GIT_HOOK_SOCKET", a.path)
		t.Setenv("P_GIT_HOOK_TOKEN", r.Token)
		if err := requestApproval(t.Context(), r.Ref, r.Old, r.New); (err == nil) != c.approved {
			t.Errorf("%s: %v", c.what, err)
		}
		if err := requestApproval(t.Context(), r.Ref, r.Old, r.New); err == nil {
			t.Errorf("%s: a second request was answered", c.what)
		}
		a.stop()
		want := 0
		if c.approved {
			want = 1
		}
		if len(calls) != want || c.approved && calls[0] != `work "" `+id {
			t.Errorf("%s: Approve calls %q, want %d with an empty old value", c.what, calls, want)
		}
	}
}
