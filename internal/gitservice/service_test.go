package gitservice

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDanglingOriginCommitSurvivesAutoMaintenanceAndRefMovement(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git unavailable")
	}
	state := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	b := &Backend{stateDir: state, gitPath: gitPath}
	repo, err := b.repository("app")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Dir(repo), 0700); err != nil {
		t.Fatal(err)
	}
	work, origin := filepath.Join(state, "work"), filepath.Join(state, "origin.git")
	gitTest(t, nil, "init", "-q", work)
	gitTest(t, nil, "init", "--bare", "-q", origin)
	gitTest(t, nil, "init", "--bare", "-q", repo)
	author := []string{"GIT_AUTHOR_NAME=P Test", "GIT_AUTHOR_EMAIL=p@example.test", "GIT_COMMITTER_NAME=P Test", "GIT_COMMITTER_EMAIL=p@example.test"}
	commit := func(content string) string {
		if err := os.WriteFile(filepath.Join(work, "file"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		gitTest(t, author, "-C", work, "add", "file")
		gitTest(t, author, "-C", work, "commit", "-qm", content)
		return gitTest(t, nil, "-C", work, "rev-parse", "HEAD")
	}
	old := commit("first")
	gitTest(t, nil, "-C", work, "push", "-q", origin, "HEAD:refs/heads/main")
	runP := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(gitPath, args...)
		cmd.Env = b.gitEnv()
		out, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("P git %v: %v: %s", args, e, out)
		}
		return strings.TrimSpace(string(out))
	}
	runP("-C", repo, "fetch", "--no-tags", "--no-write-fetch-head", origin, "refs/heads/main")
	if got := runP("-C", repo, "for-each-ref", "--format=%(refname)"); got != "" {
		t.Fatalf("fetch created P ref %q", got)
	}
	for key, want := range map[string]string{"gc.auto": "0", "maintenance.auto": "false", "receive.autogc": "false"} {
		gitTest(t, nil, "-C", repo, "config", key, "true")
		if got := runP("-C", repo, "config", "--get", key); got != want {
			t.Fatalf("trusted %s=%q, want %q", key, got, want)
		}
		// The receive-pack process uses this exact prefix and environment.
		// An earlier native -c cannot override the final protective options.
		args := append([]string{"-c", key + "=true"}, transportGitConfigArgs("receive", filepath.Join(state, "git-hooks"), 1024)...)
		args = append(args, "-C", repo, "config", "--get", key)
		if got := runP(args...); got != want {
			t.Fatalf("receive-pack runner %s=%q, want %q", key, got, want)
		}
	}
	// A source-ready operation has only its durable captured OID at this point;
	// the ordinary branch is absent while the worker can be blocked or stopped.
	captureFile := filepath.Join(state, "captured-oid")
	if err = os.WriteFile(captureFile, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	newTip := commit("second")
	gitTest(t, nil, "-C", work, "push", "-q", origin, "HEAD:refs/heads/main")
	if newTip == old {
		t.Fatal("second commit did not move")
	}
	if got := gitTest(t, nil, "-C", origin, "rev-parse", "refs/heads/main"); got == old {
		t.Fatal("origin did not move")
	}
	b = &Backend{stateDir: state, gitPath: gitPath} // daemon restart
	runP("-C", repo, "gc", "--auto")
	runP("-C", repo, "maintenance", "run", "--auto")
	readCaptured, err := os.ReadFile(captureFile)
	if err != nil {
		t.Fatal(err)
	}
	old = string(readCaptured)
	if got := runP("-C", repo, "cat-file", "-t", old); got != "commit" {
		t.Fatalf("pending captured object lost: %q", got)
	}
	runP("-C", repo, "update-ref", "refs/heads/work", old, strings.Repeat("0", 40))
	if got := runP("-C", repo, "rev-parse", "refs/heads/work"); got != old {
		t.Fatalf("retry selected moved origin %q", got)
	}
}

func TestExactGitCommand(t *testing.T) {
	for _, tc := range []struct {
		raw, service, project string
		ok                    bool
	}{
		{"git-upload-pack 'team/app'", "upload", "team/app", true},
		{"git-receive-pack 'app'", "receive", "app", true},
		{"git-upload-pack 'app'; id'", "", "", false},
		{"git-upload-pack '/app'", "upload", "app", true},
		{"git-upload-pack /app", "", "", false},
		{"git-upload-pack '//app'", "", "", false},
		{"git-upload-pack 'app/../other'", "", "", false},
		{"git-upload-pack 'refs/p'", "upload", "refs/p", true},
		{"git-upload-archive 'app'", "", "", false},
		{"id", "", "", false},
	} {
		s, p, ok := parseCommand(tc.raw)
		if s != tc.service || p != tc.project || ok != tc.ok {
			t.Errorf("%q: %q %q %v", tc.raw, s, p, ok)
		}
	}
}

func gitTest(t *testing.T, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestRepositoryPreflightHonorsCancellation(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	state := t.TempDir()
	sleepPath, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep is unavailable")
	}
	if err := os.Mkdir(filepath.Join(state, "repositories"), 0700); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(state, "stalled-git")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexec "+sleepPath+" 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	backend := &Backend{stateDir: state, gitPath: fake}
	repo, err := backend.repository("app")
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, nil, "init", "--bare", repo)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = backend.checkRepository(ctx, "app")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("preflight did not return deadline: %v", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("preflight ignored cancellation")
	}
}

func TestPreReceiveRequiresAssignedFastForwardCommit(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is unavailable")
	}
	dir := t.TempDir()
	bare, work := filepath.Join(dir, "bare.git"), filepath.Join(dir, "work")
	gitTest(t, nil, "init", "--bare", bare)
	gitTest(t, nil, "init", work)
	env := []string{"GIT_AUTHOR_NAME=P Test", "GIT_AUTHOR_EMAIL=p@example.test", "GIT_COMMITTER_NAME=P Test", "GIT_COMMITTER_EMAIL=p@example.test"}
	for i := 0; i < 2; i++ {
		if err := os.WriteFile(filepath.Join(work, "file"), []byte(fmt.Sprint(i)), 0600); err != nil {
			t.Fatal(err)
		}
		gitTest(t, env, "-C", work, "add", "file")
		gitTest(t, env, "-C", work, "commit", "-m", fmt.Sprintf("c%d", i))
		gitTest(t, nil, "-C", work, "push", bare, "HEAD:refs/heads/work")
	}
	newOID := gitTest(t, nil, "-C", work, "rev-parse", "HEAD")
	oldOID := gitTest(t, nil, "-C", work, "rev-parse", "HEAD^")
	t.Setenv("P_GIT_ALLOWED_REF", "refs/heads/work")
	t.Setenv("P_GIT_EXEC", gitPath)
	t.Setenv("GIT_DIR", bare)
	t.Setenv("P_GIT_HOOK_TOKEN", strings.Repeat("d", 48))
	t.Setenv("P_GIT_HOOK_SOCKET", filepath.Join(dir, "hook.sock"))
	check := func(line string, wantOK bool) {
		t.Helper()
		var listener net.Listener
		if wantOK {
			var err error
			listener, err = net.Listen("unix", filepath.Join(dir, "hook.sock"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { listener.Close(); os.Remove(filepath.Join(dir, "hook.sock")) }()
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				var request [1024]byte
				_, _ = conn.Read(request[:])
				_, _ = conn.Write([]byte("ok\n"))
			}()
		}
		err := RunPreReceiveHook(context.Background(), strings.NewReader(line+"\n"))
		if (err == nil) != wantOK {
			t.Fatalf("hook %q: %v", line, err)
		}
	}
	check(oldOID+" "+newOID+" refs/heads/work", true)
	check(newOID+" "+oldOID+" refs/heads/work", false)
	check(oldOID+" "+newOID+" refs/heads/other", false)
	check(oldOID+" "+strings.Repeat("0", 40)+" refs/heads/work", false)
	check(strings.Repeat("0", 40)+" "+newOID+" refs/heads/work", true)
	check(oldOID+" "+newOID+" refs/heads/work\n"+oldOID+" "+newOID+" refs/heads/work", false)
}
