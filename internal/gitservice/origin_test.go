package gitservice

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/plugin"
)

const oidA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const oidB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func TestOriginURLAllowlist(t *testing.T) {
	for _, raw := range []string{
		"ssh://git@example.com/org/repo.git", "ssh://alias.example:2222/a.git",
		"git@host-alias:team/repo.git", "git@host:~/repo.git",
	} {
		if err := validOriginURL(raw); err != nil {
			t.Errorf("valid %q: %v", raw, err)
		}
	}
	for _, raw := range []string{
		"", "/tmp/a.git", "file:///tmp/a.git", "https://example.com/x",
		"ext::ssh evil", "git@host", "host:repo", "ssh://git:password@host/repo",
		"ssh://host/repo?x=y", "git@host:repo;touch", "git@host:-option",
		"git@host:../repo", "git@host:repo\nX", "ssh://host:0/repo",
	} {
		if err := validOriginURL(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}

func TestOriginScopeClosed(t *testing.T) {
	scope := &OriginScope{}
	if _, err := scope.Observe(context.Background(), "git@host:repo.git"); err == nil {
		t.Fatal("closed scope observed")
	}
	if _, err := scope.Fetch(context.Background(), "refs/heads/main", oidA); err == nil {
		t.Fatal("closed scope fetched")
	}
}

func TestParseOriginAdvertisement(t *testing.T) {
	refs, err := parseOriginAdvertisement("")
	if err != nil || len(refs) != 0 {
		t.Fatalf("empty origin: %+v %v", refs, err)
	}
	text := oidB + "\trefs/tags/v1^{}\n" + oidA + "\trefs/heads/main\n" + oidA + "\trefs/tags/v1\n"
	refs, err = parseOriginAdvertisement(text)
	if err != nil || len(refs) != 2 || refs[0].Ref != "refs/heads/main" || refs[1].OID != oidA || refs[1].CommitOID != oidB {
		t.Fatalf("parsed: %+v %v", refs, err)
	}
	for _, bad := range []string{
		oidA + "\trefs/tags/v1^{}\n", // peel without tag
		oidA + "\trefs/heads/main\n" + oidB + "\trefs/heads/main\n",
		oidA + "\trefs/heads/x?evil\n",
		"not-an-oid\trefs/heads/main\n",
		oidA + "\trefs/heads/" + string([]byte{0xff}) + "\n",
		oidA + "\trefs/heads/main\n" + oidA + "\trefs/tags/v1^{}\n",
	} {
		if _, err := parseOriginAdvertisement(bad); err == nil {
			t.Errorf("accepted advertisement %q", bad)
		}
	}
	unicodeRefs, err := parseOriginAdvertisement(oidA + "\trefs/heads/café\n")
	if err != nil || len(unicodeRefs) != 1 || unicodeRefs[0].Ref != "refs/heads/café" {
		t.Fatalf("valid Unicode ref: %+v %v", unicodeRefs, err)
	}
	var large strings.Builder
	for i := 0; i < originMaxRefs+1; i++ {
		large.WriteString(oidA + "\trefs/heads/r" + strings.Repeat("a", i/1000) + string(rune('a'+i%26)) + "\n")
	}
	// Repeated names or an over-limit count must never yield a partial result.
	if _, err := parseOriginAdvertisement(large.String()); err == nil {
		t.Fatal("accepted oversized origin")
	}
}

func TestOriginAdvertisementRealGit(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git unavailable")
	}
	root := t.TempDir()
	env := []string{"PATH=" + filepath.Dir(git), "HOME=" + root, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(git, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	work, origin := filepath.Join(root, "work"), filepath.Join(root, "origin.git")
	run("-c", "init.defaultBranch=main", "init", "-q", work)
	run("init", "--bare", "-q", origin)
	if refs, err := parseOriginAdvertisement(run("ls-remote", "--heads", "--tags", origin)); err != nil || len(refs) != 0 {
		t.Fatalf("empty real origin: %+v %v", refs, err)
	}
	if err := os.WriteFile(filepath.Join(work, "readme"), []byte("source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run("-C", work, "add", "readme")
	run("-C", work, "-c", "user.name=P", "-c", "user.email=p@example.test", "commit", "-qm", "source")
	run("-C", work, "-c", "user.name=P", "-c", "user.email=p@example.test", "tag", "-am", "release", "v1")
	run("-C", work, "push", "-q", origin, "main", "v1")
	refs, err := parseOriginAdvertisement(run("ls-remote", "--heads", "--tags", origin))
	if err != nil || len(refs) != 2 || refs[0].Ref != "refs/heads/main" || refs[1].Ref != "refs/tags/v1" || refs[1].CommitOID != refs[0].OID || refs[1].OID == refs[1].CommitOID {
		t.Fatalf("real advertised refs: %+v %v", refs, err)
	}
	// Git talks to this fixed upload-pack program through its normal SSH
	// transport. The script has no network access or credential authority.
	shim := filepath.Join(root, "shim")
	if err := os.Mkdir(shim, 0700); err != nil {
		t.Fatal(err)
	}
	sshScript := filepath.Join(shim, "ssh")
	marker := filepath.Join(root, "ssh-env")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"${GIT_CONFIG_GLOBAL-unset}\" \"${GIT_CONFIG_COUNT-unset}\" \"${GIT_DIR-unset}\" >> " + quote(marker) + "\n" +
		"case \"$GIT_SSH_COMMAND\" in *BatchMode=yes*) ;; *) exit 80 ;; esac\n" +
		"[ \"$GIT_TERMINAL_PROMPT\" = 0 ] || exit 81\n" +
		"exec " + quote(git) + " upload-pack " + quote(origin) + "\n"
	if err := os.WriteFile(sshScript, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	pkgDir := filepath.Join(root, "source-package")
	if err := os.Mkdir(pkgDir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"plugin.json", "p-git-ssh"} {
		data, err := os.ReadFile(filepath.Join("../../plugins/bundled/source-git", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pkgDir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Skip("Go compiler unavailable")
	}
	build := exec.Command(goPath, "build", "-o", filepath.Join(pkgDir, "git.wasm"), "./plugins/bundled/source-git")
	build.Dir = filepath.Join("..", "..")
	build.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0", "GOTOOLCHAIN=local")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build source plugin: %v %s", err, out)
	}
	pkg, err := plugin.Conformance(pkgDir)
	if err != nil {
		t.Fatal(err)
	}
	b := &Backend{stateDir: root, gitPath: git, selection: plugin.Active{Package: pkg, Grants: []string{"git.project"}}}
	remote := "git@fixture:repo.git"
	t.Setenv("PATH", shim+":"+os.Getenv("PATH"))
	t.Setenv("HOME", root)
	// The caller's Git environment and global config must not reach the
	// origin command. The project repository below has hostile local config.
	hostileGlobal := filepath.Join(root, "hostile-global")
	if err := os.WriteFile(hostileGlobal, []byte("[url \"file:///nonexistent/\"]\n\tinsteadOf = git@fixture:\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", hostileGlobal)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.sshCommand")
	t.Setenv("GIT_CONFIG_VALUE_0", "false")
	t.Setenv("GIT_SSH_COMMAND", "false")
	t.Setenv("GIT_DIR", origin)
	var contacted []plugin.GitOriginRef
	if err := b.WithOrigin(context.Background(), "team/app", func(scope *OriginScope) error {
		var e error
		contacted, e = scope.Observe(context.Background(), remote)
		return e
	}); err != nil || len(contacted) != 2 {
		t.Fatalf("native contact before repository: %+v %v", contacted, err)
	}
	if contacted[0].CommitOID != refs[0].CommitOID || contacted[1].CommitOID != refs[1].CommitOID {
		t.Fatalf("native refs differ: %+v %+v", contacted, refs)
	}
	if err := os.Mkdir(filepath.Join(root, "repositories"), 0700); err != nil {
		t.Fatal(err)
	}
	pRepo, err := b.repository("team/app")
	if err != nil {
		t.Fatal(err)
	}
	run("init", "--bare", "-q", pRepo)
	run("-C", pRepo, "symbolic-ref", "HEAD", "refs/heads/main")
	hookDir := filepath.Join(root, "untrusted-hooks")
	if err := os.Mkdir(hookDir, 0700); err != nil {
		t.Fatal(err)
	}
	hookMarker := filepath.Join(root, "hook-fired")
	if err := os.WriteFile(filepath.Join(hookDir, "post-fetch"), []byte("#!/bin/sh\ntouch "+quote(hookMarker)+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	run("-C", pRepo, "config", "core.hooksPath", hookDir)
	run("-C", pRepo, "config", "core.sshCommand", "false")
	run("-C", pRepo, "config", "url.file:///nonexistent/.insteadOf", remote)
	run("-C", pRepo, "config", "remote.origin.uploadpack", "false")
	run("-C", pRepo, "config", "credential.helper", "false")
	configBefore, err := os.ReadFile(filepath.Join(pRepo, "config"))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WithOrigin(context.Background(), "team/app", func(scope *OriginScope) error {
		fresh, e := scope.Observe(context.Background(), remote)
		if e != nil {
			return e
		}
		for _, selected := range fresh {
			if _, e = scope.Fetch(context.Background(), selected.Ref, selected.CommitOID); e != nil {
				return e
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("native source fetch: %v", err)
	}
	if got := strings.TrimSpace(run("--git-dir="+pRepo, "cat-file", "-t", refs[0].OID)); got != "commit" {
		t.Fatalf("cached commit type %q", got)
	}
	if got := strings.TrimSpace(run("--git-dir="+pRepo, "cat-file", "-t", refs[1].OID)); got != "tag" {
		t.Fatalf("cached tag type %q", got)
	}
	if got := strings.TrimSpace(run("--git-dir="+pRepo, "for-each-ref", "--format=%(refname)")); got != "" {
		t.Fatalf("fetch wrote P refs: %s", got)
	}
	if _, err := os.Stat(filepath.Join(pRepo, "FETCH_HEAD")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fetch wrote P FETCH_HEAD: %v", err)
	}
	configAfter, err := os.ReadFile(filepath.Join(pRepo, "config"))
	if err != nil || string(configAfter) != string(configBefore) {
		t.Fatalf("fetch changed P config: %v", err)
	}
	if _, err := os.Stat(hookMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository hook ran: %v", err)
	}
	envLog, err := os.ReadFile(marker)
	if err != nil || !strings.Contains(string(envLog), "/dev/null\nunset\nunset\n") {
		t.Fatalf("ambient Git variables leaked: %v %q", err, envLog)
	}
	// A source ref that moves after observation must fail, and that failed
	// fetch invalidates the scope until it observes again.
	if err := b.WithOrigin(context.Background(), "team/app", func(scope *OriginScope) error {
		fresh, e := scope.Observe(context.Background(), remote)
		if e != nil {
			return e
		}
		if err := os.WriteFile(filepath.Join(work, "readme"), []byte("moved\n"), 0600); err != nil {
			return err
		}
		run("-C", work, "add", "readme")
		run("-C", work, "-c", "user.name=P", "-c", "user.email=p@example.test", "commit", "-qm", "moved")
		run("-C", work, "push", "-q", origin, "main")
		if _, e = scope.Fetch(context.Background(), fresh[0].Ref, fresh[0].CommitOID); e == nil {
			return errors.New("moved ref was accepted")
		}
		if _, e = scope.Fetch(context.Background(), fresh[1].Ref, fresh[1].CommitOID); e == nil {
			return errors.New("stale observation remained reusable")
		}
		updated, e := scope.Observe(context.Background(), remote)
		if e != nil || updated[0].OID == fresh[0].OID {
			return errors.New("fresh observation did not see movement")
		}
		_, e = scope.Fetch(context.Background(), updated[0].Ref, updated[0].CommitOID)
		return e
	}); err != nil {
		t.Fatalf("moved ref/invalidation: %v", err)
	}
	if got := strings.TrimSpace(run("--git-dir="+pRepo, "for-each-ref", "--format=%(refname)")); got != "" {
		t.Fatalf("raced fetch wrote P refs: %s", got)
	}
}

func TestOriginGitWaitDelayClosesEscapedPipes(t *testing.T) {
	git, ge := exec.LookPath("git")
	setsid, se := exec.LookPath("setsid")
	sleep, le := exec.LookPath("sleep")
	if ge != nil || se != nil || le != nil {
		t.Skip("Git, setsid, or sleep unavailable")
	}
	root := t.TempDir()
	shim := filepath.Join(root, "shim")
	if err := os.Mkdir(shim, 0700); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(root, "child-pid")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	script := "#!/bin/sh\n" + quote(setsid) + " " + quote(sleep) + " 4 &\nprintf '%s' \"$!\" > " + quote(pidFile) + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(shim, "ssh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shim+":"+os.Getenv("PATH"))
	b := &Backend{gitPath: git}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := b.originGit(ctx, "/", nil, "ls-remote", "--heads", "git@fixture:repo.git")
	if err == nil {
		t.Fatal("escaped SSH descendant did not fail")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("origin wait exceeded bound: %s", elapsed)
	}
	data, e := os.ReadFile(pidFile)
	if e != nil {
		t.Fatalf("SSH test double did not run: %v", e)
	}
	pid, e := strconv.Atoi(strings.TrimSpace(string(data)))
	if e != nil {
		t.Fatal(e)
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
}
