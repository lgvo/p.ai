// builder-fixture exercises the native builder substrate and, in explicit nix
// mode, closed offline resolution, realization, and activation capture from
// a clone of the commit's bundle. Explicit nix-image mode builds a commit
// image end to end through the Incus image driver.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/gitservice"
	"github.com/lgvo/p.ai/internal/plugin"
	"github.com/lgvo/p.ai/internal/runtimedriver"
	"github.com/lgvo/p.ai/internal/runtimeincus"
	"github.com/lgvo/p.ai/tests/integration/internal/bundledgit"
)

const project = "builder-lab"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "builder-fixture:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 3 && args[2] == "nix" {
		return runNixFixture(args[:2], false)
	}
	if len(args) == 3 && args[2] == "nix-image" {
		return runNixFixture(args[:2], true)
	}
	if len(args) != 2 {
		return errors.New("usage: builder-fixture ACTIVATION BASE-FINGERPRINT")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	state, err := os.MkdirTemp("", "p-builder-fixture-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(state)
	store, git, repo, err := openRepository(ctx, state, args[0])
	if err != nil {
		return err
	}
	defer store.Close()
	commit, err := seedCommit(ctx, repo)
	if err != nil {
		return err
	}
	src, err := exportCommit(ctx, git, state, commit)
	if err != nil {
		return err
	}
	binary, err := exec.LookPath("incus")
	if err != nil {
		return err
	}
	baseConfig := runtimeincus.Config{Binary: binary, UserSocket: "/var/lib/incus/unix.socket.user", Project: "user-1000", DiskSourceCeilings: []string{"/var/lib/p-vm/endpoints", "/var/lib/p-vm/grants"}, PInstanceID: "a0000000-0000-4000-8000-000000000021"}
	uuid, err := newUUID()
	if err != nil {
		return err
	}
	r := runtimeincus.Builder{RequestUUID: uuid, ProjectPath: project, CommitOID: commit, BaseImageFingerprint: strings.TrimSpace(args[1]), ContractVersion: "1"}
	name := "p-builder-" + uuid
	// The VM's original dir pool is deliberately unsupported even though Incus
	// accepts a root size string there without enforcing a quota.
	dirConfig := baseConfig
	dirConfig.BuilderStoragePool = "default"
	dirBackend, err := runtimeincus.New(dirConfig)
	if err != nil {
		return err
	}
	if _, err := dirBackend.CreateBuilder(ctx, r); err == nil {
		return errors.New("dir pool accepted as bounded builder storage")
	}
	baseConfig.BuilderStoragePool = "builders"
	b, err := runtimeincus.New(baseConfig)
	if err != nil {
		return err
	}
	if o, err := b.InspectBuilder(ctx, r); err != nil || o.Exists {
		return fmt.Errorf("failed precondition after rejected dir pool: %+v %w", o, err)
	}
	created := false
	defer func() {
		if created {
			_, _ = b.DeleteBuilder(context.Background(), r)
		}
	}()
	o, err := b.CreateBuilder(ctx, r)
	if err != nil || !o.Ready || o.Status != "Stopped" {
		return fmt.Errorf("builder create: %+v %w", o, err)
	}
	created = true
	if err := b.TransferBuilderSource(ctx, r, src); err != nil {
		return fmt.Errorf("source bundle transfer: %w", err)
	}
	if o, err = b.StartBuilder(ctx, r); err != nil || o.Status != "Running" {
		return fmt.Errorf("start sealed builder: %+v %w", o, err)
	}
	if err := waitGuestReady(ctx, binary, state, name); err != nil {
		return err
	}
	if err := b.CloneBuilderSource(ctx, r, src); err != nil {
		return fmt.Errorf("clone source bundle: %w", err)
	}
	if err := checkGuest(ctx, binary, state, name, commit); err != nil {
		return err
	}
	if o, err = b.StopBuilder(ctx, r); err != nil || o.Status != "Stopped" {
		return fmt.Errorf("stop builder: %+v %w", o, err)
	}
	if _, err := incus(ctx, binary, state, "config", "set", name, "user.p.builder_request_uuid=00000000-0000-4000-8000-000000000000"); err != nil {
		return err
	}
	if _, err := b.DeleteBuilder(ctx, r); err == nil {
		return errors.New("foreign builder metadata accepted for deletion")
	}
	d := runtimeincus.NewDriver(b)
	ref := runtimedriver.Ref{Kind: runtimedriver.KindBuild, ID: uuid}
	if err := d.Delete(ctx, ref, "", func() error { return errors.New("gate reached for a foreign builder") }); !errors.Is(err, runtimedriver.ErrMismatch) {
		return fmt.Errorf("driver delete of a foreign builder: %w", err)
	}
	if _, err := incus(ctx, binary, state, "config", "set", name, "user.p.builder_request_uuid="+uuid); err != nil {
		return err
	}
	gates := 0
	if err := d.Delete(ctx, ref, "", func() error { gates++; return nil }); err != nil || gates != 1 {
		return fmt.Errorf("delete verified builder: gates=%d %w", gates, err)
	}
	created = false
	if o, err = b.InspectBuilder(ctx, r); err != nil || o.Exists {
		return fmt.Errorf("builder remains after delete: %+v %w", o, err)
	}
	fmt.Println("P_BUILDER_SUBSTRATE_PASS")
	return nil
}

// openRepository makes a P repository for the fixture project. The caller
// closes the store.
func openRepository(ctx context.Context, state, activation string) (*control.Store, *gitservice.Backend, string, error) {
	store, err := control.OpenStore(state)
	if err != nil {
		return nil, nil, "", err
	}
	git, repo, err := initRepository(ctx, store, state, activation)
	if err != nil {
		store.Close()
		return nil, nil, "", err
	}
	return store, git, repo, nil
}

// initRepository makes the repository with the bundled source driver and
// returns the Git service that exports its commits.
func initRepository(ctx context.Context, store *control.Store, state, activation string) (*gitservice.Backend, string, error) {
	active, err := plugin.LoadActivation(activation)
	if err != nil {
		return nil, "", err
	}
	git, err := gitservice.New(store, state, active)
	if err != nil {
		return nil, "", err
	}
	driver, err := bundledgit.Open(ctx, state)
	if err != nil {
		return nil, "", err
	}
	if _, err := driver.CreateRepository(ctx, project); err != nil {
		return nil, "", err
	}
	return git, bundledgit.RepositoryPath(state, project), nil
}

// exportCommit writes the commit's bundle into a private directory, as core
// stages it for a build.
func exportCommit(ctx context.Context, git *gitservice.Backend, state, commit string) (runtimedriver.SourceExport, error) {
	dir, err := os.MkdirTemp(state, "export-")
	if err != nil {
		return runtimedriver.SourceExport{}, err
	}
	bundle, err := git.ExportBundle(ctx, project, commit, dir)
	if err != nil {
		return runtimedriver.SourceExport{}, fmt.Errorf("export commit bundle: %w", err)
	}
	if bundle.Commit != commit || bundle.Ref != gitservice.BundleRef {
		return runtimedriver.SourceExport{}, fmt.Errorf("bundle names another commit: %+v", bundle)
	}
	return runtimedriver.SourceExport{Bundle: bundle.Path, Ref: bundle.Ref, Commit: bundle.Commit}, nil
}

func newUUID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s", hex.EncodeToString(id[:4]), hex.EncodeToString(id[4:6]), hex.EncodeToString(id[6:8]), hex.EncodeToString(id[8:10]), hex.EncodeToString(id[10:])), nil
}

// seedCommit commits a tree with an executable, a relative link and a
// dangling link to main, and returns the commit.
func seedCommit(ctx context.Context, repo string) (string, error) {
	flake, err := gitCommand(ctx, repo, []byte("{ outputs = _: {}; }\n"), "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	script, err := gitCommand(ctx, repo, []byte("#!/bin/sh\nexit 0\n"), "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	link, err := gitCommand(ctx, repo, []byte("../flake.nix"), "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	dangling, err := gitCommand(ctx, repo, []byte("missing"), "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	inner, err := gitCommand(ctx, repo, []byte("nested\n"), "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	childTree, err := gitCommand(ctx, repo, []byte("120000 blob "+dangling+"\tdangling\x00100644 blob "+inner+"\tinner\x00120000 blob "+link+"\tlink\x00"), "mktree", "-z")
	if err != nil {
		return "", err
	}
	tree, err := gitCommand(ctx, repo, []byte("100644 blob "+flake+"\tflake.nix\x00100755 blob "+script+"\trun\x00040000 tree "+childTree+"\tdir\x00"), "mktree", "-z")
	if err != nil {
		return "", err
	}
	commit, err := gitCommand(ctx, repo, []byte("builder fixture\n"), "commit-tree", tree)
	if err != nil {
		return "", err
	}
	if _, err := gitCommand(ctx, repo, nil, "update-ref", "refs/heads/main", commit); err != nil {
		return "", err
	}
	return commit, nil
}

func gitCommand(ctx context.Context, repo string, input []byte, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=P Fixture", "GIT_AUTHOR_EMAIL=p@example.test", "GIT_COMMITTER_NAME=P Fixture", "GIT_COMMITTER_EMAIL=p@example.test")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %v: %w: %s", args, err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

func incus(ctx context.Context, binary, state string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, binary, append([]string{"--force-local", "--project", "user-1000"}, args...)...)
	cmd.Env = []string{"INCUS_SOCKET=/var/lib/incus/unix.socket.user", "INCUS_CONF=" + state, "HOME=" + state, "PATH=/usr/bin:/bin", "LANG=C"}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func waitGuestReady(ctx context.Context, binary, state, name string) error {
	readyCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	// Incus reports Running before NixOS has completed boot. Socket presence
	// can also precede a listening daemon, so require a real Nix store RPC.
	const probe = `if ! test -S /nix/var/nix/daemon-socket/socket; then echo nix-daemon-socket-missing; exit 41; fi
if ! test -x /run/current-system/sw/bin/fallocate; then echo fallocate-missing; exit 42; fi`
	lastFailure := "guest exec unavailable"
	for {
		out, err := incus(readyCtx, binary, state, "exec", name, "--", "/bin/sh", "-c", probe)
		if err == nil {
			info, rpcErr := incus(readyCtx, binary, state, "exec", name, "--user", "1000", "--group", "1000",
				"--env", "NIX_REMOTE=daemon", "--", "/run/current-system/sw/bin/nix",
				"--extra-experimental-features", "nix-command flakes", "store", "info", "--json")
			if rpcErr == nil {
				start := strings.IndexByte(info, '{')
				var daemon struct {
					Version string `json:"version"`
				}
				if start < 0 || len(info) > 4096 || json.NewDecoder(strings.NewReader(info[start:])).Decode(&daemon) != nil {
					return fmt.Errorf("guest Nix daemon response invalid: %q", info)
				}
				if daemon.Version != "2.34.8" {
					return fmt.Errorf("guest Nix daemon version changed: %q", daemon.Version)
				}
				return nil
			}
			out = info
		}
		if marker := strings.TrimSpace(out); marker != "" {
			lastFailure = marker
		}
		if readyCtx.Err() != nil {
			return fmt.Errorf("guest boot prerequisites unavailable after 45s: %w: %s", readyCtx.Err(), lastFailure)
		}
		select {
		case <-readyCtx.Done():
			return fmt.Errorf("guest boot prerequisites unavailable after 45s: %w: %s", readyCtx.Err(), lastFailure)
		case <-ticker.C:
		}
	}
}

// cloneCheck proves, as the session user, that /workspace is a clean clone
// at the commit: the user's, detached, with no ref, no remote and no
// initialization marker.
func cloneCheck(commit string) string {
	return `set -eu
cd /workspace
test "$(stat -c '%u:%g' /workspace /workspace/.git)" = "1000:1000
1000:1000"
test ! -e .git/p-initialized
git=/run/current-system/sw/bin/git
test "$($git rev-parse HEAD)" = ` + commit + `
test "$($git rev-parse --symbolic-full-name HEAD)" = HEAD
test -z "$($git for-each-ref)"
test -z "$($git remote)"
test -z "$($git status --porcelain --untracked-files=all)"`
}

func guestUser(ctx context.Context, binary, state, name, script string) (string, error) {
	return incus(ctx, binary, state, "exec", name, "--user", "1000", "--group", "1000", "--cwd", "/workspace",
		"--env", "HOME=/home/p", "--env", "USER=p", "--env", "LOGNAME=p", "--env", "NIX_REMOTE=daemon",
		"--", "/run/current-system/sw/bin/bash", "-c", script)
}

func checkGuest(ctx context.Context, binary, state, name, commit string) error {
	command := `set -eu
test "$(stat -c '%u:%g:%a' /opt/p/build)" = 0:0:755
test "$(stat -c '%u:%g:%a' /opt/p/build/source.bundle)" = 0:0:444
if chmod u+w /opt/p/build/source.bundle 2>/dev/null; then exit 41; fi
if : >> /opt/p/build/source.bundle 2>/dev/null; then exit 42; fi
if : > /opt/p/build/other 2>/dev/null; then exit 43; fi
cd /workspace
test "$(readlink dir/link)" = ../flake.nix
test "$(cat dir/link)" = '{ outputs = _: {}; }'
test "$(readlink dir/dangling)" = missing
test -L dir/dangling
test ! -e dir/dangling
test -x run`
	if out, err := guestUser(ctx, binary, state, name, command); err != nil {
		return fmt.Errorf("guest bundle immutability or clone content: %w: %s", err, out)
	}
	if out, err := guestUser(ctx, binary, state, name, cloneCheck(commit)); err != nil {
		return fmt.Errorf("guest clone is not the commit: %w: %s", err, out)
	}
	// The 9 GiB allocation must hit the 8 GiB qgroup limit, rather than the
	// outer disk or pool. The error text distinguishes EDQUOT from ENOSPC.
	out, err := incus(ctx, binary, state, "exec", name, "--user", "1000", "--group", "1000", "--", "/run/current-system/sw/bin/fallocate", "-l", "9G", "/tmp/p-quota-probe")
	if err == nil || !strings.Contains(out, "Disk quota exceeded") {
		return fmt.Errorf("expected EDQUOT from btrfs qgroup, got %v: %s", err, out)
	}
	return nil
}
