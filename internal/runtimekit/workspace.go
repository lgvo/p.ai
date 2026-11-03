package runtimekit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const WorkspaceConfigPath = "/etc/p/workspace.json"

var workspaceName = regexp.MustCompile(`^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)*$`)
var workspaceOID = regexp.MustCompile(`^[0-9a-f]{40}$`)

// WorkspaceConfig is trusted creation input. A blank bootstrap has no OID and
// must be the unborn main branch. Repository is a P Git project identifier.
// v1 clones Repository from P at the first Start. v2 adopts the clone the
// commit image already holds at InitialOID; EnvironmentSelection is what the
// image build found at that commit: no-flake, no-default or devshell.
type WorkspaceConfig struct {
	Schema               string `json:"schema"`
	Repository           string `json:"repository"`
	Branch               string `json:"branch"`
	InitialOID           string `json:"initial_oid,omitempty"`
	EnvironmentSelection string `json:"environment_selection,omitempty"`
}

func ParseWorkspaceConfig(data []byte) (WorkspaceConfig, error) {
	var c WorkspaceConfig
	if len(data) == 0 || len(data) > 4096 || uniqueJSONKeys(data) != nil {
		return c, errors.New("invalid workspace config JSON")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, err
	}
	if d.Decode(new(any)) != io.EOF {
		return c, errors.New("trailing workspace config")
	}
	if c.Schema != "p.workspace/v1" && c.Schema != "p.workspace/v2" || !workspaceName.MatchString(c.Repository) || len(c.Repository) > 255 || !workspaceName.MatchString(c.Branch) || len(c.Branch) > 200 || strings.Contains(c.Branch, "..") || strings.Contains(c.Branch, "@{") || strings.HasSuffix(c.Branch, ".lock") {
		return c, errors.New("invalid workspace identity")
	}
	for _, part := range strings.Split(c.Repository, "/") {
		if part == "." || part == ".." || len(part) > 100 {
			return c, errors.New("invalid repository component")
		}
	}
	for _, part := range strings.Split(c.Branch, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") {
			return c, errors.New("invalid branch component")
		}
	}
	if c.InitialOID == "" {
		if c.Branch != "main" {
			return c, errors.New("only unborn main can omit initial OID")
		}
	} else if !workspaceOID.MatchString(c.InitialOID) {
		return c, errors.New("invalid initial OID")
	}
	if c.Schema == "p.workspace/v1" && c.EnvironmentSelection != "" {
		return c, errors.New("unresolved workspace cannot carry environment selection")
	}
	if c.Schema == "p.workspace/v2" && (c.InitialOID == "" ||
		c.EnvironmentSelection != "no-flake" && c.EnvironmentSelection != "no-default" && c.EnvironmentSelection != "devshell") {
		return c, errors.New("image workspace needs its commit and environment selection")
	}
	return c, nil
}

func readWorkspaceConfig() (WorkspaceConfig, error) {
	if err := regularOwned(WorkspaceConfigPath, 0); err != nil {
		return WorkspaceConfig{}, err
	}
	st, err := os.Lstat(WorkspaceConfigPath)
	if err != nil {
		return WorkspaceConfig{}, err
	}
	if st.Size() > 4096 {
		return WorkspaceConfig{}, errors.New("workspace config too large")
	}
	data, err := os.ReadFile(WorkspaceConfigPath)
	if err != nil {
		return WorkspaceConfig{}, err
	}
	return ParseWorkspaceConfig(data)
}

// workspaceInputs verifies the fixed, session-scoped inputs before any Git work.
func workspaceInputs() (WorkspaceConfig, error) {
	cfg, err := ReadConfig()
	if err != nil {
		return WorkspaceConfig{}, err
	}
	if err := validateFilesystemMounts(cfg.FilesystemMounts); err != nil {
		return WorkspaceConfig{}, err
	}
	if err := ValidateFixedAssets(cfg.AgentSHA256); err != nil {
		return WorkspaceConfig{}, err
	}
	if err := ValidateGitCredentials(); err != nil {
		return WorkspaceConfig{}, err
	}
	if err := validateEndpoints(); err != nil {
		return WorkspaceConfig{}, err
	}
	return readWorkspaceConfig()
}

func ValidateGitCredentials() error {
	for _, path := range []string{"/etc/p/git", "/etc/p/git/ssh_config", "/etc/p/git/known_hosts", "/etc/p/git/identity"} {
		st, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if path == "/etc/p/git" {
			if !st.IsDir() || owner(st) != 0 || st.Mode().Perm() != 0755 {
				return errors.New("Git credential directory is unsafe")
			}
			continue
		}
		if path == "/etc/p/git/identity" {
			if err := privateKey(path, 1000); err != nil {
				return err
			}
		} else if err := regularOwned(path, 0); err != nil {
			return err
		}
	}
	return nil
}

func initWorkspaceAt(c WorkspaceConfig, dir, git string) error {
	return initWorkspaceRemote(c, dir, git, pRemote(c))
}

// pRemote is the origin a workspace fetches its assigned branch from: P's Git
// service, reached through the session's fixed SSH configuration.
func pRemote(c WorkspaceConfig) string { return "ssh://git@p/" + c.Repository }

// workspaceGit runs Git in dir as the session user's first initialization
// does: no hooks, credential helpers, fsmonitor, or system and global config,
// with bounded output and a deadline per command.
func workspaceGit(dir, git string) func(args ...string) (string, error) {
	return func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, git, append([]string{"-c", "core.hooksPath=/dev/null", "-c", "credential.helper=", "-c", "core.fsmonitor=false"}, args...)...)
		cmd.Dir = dir
		cmd.Env = []string{"PATH=/run/current-system/sw/bin:/usr/bin:/bin", "HOME=/home/p", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_SSH=/usr/libexec/p/git-ssh", "GIT_TERMINAL_PROMPT=0", "LANG=C"}
		var output boundedWorkspaceOutput
		cmd.Stdout = &output
		cmd.Stderr = &output
		err := cmd.Run()
		out := output.Bytes()
		if output.exceeded {
			return "", errors.New("workspace Git output exceeded bound")
		}
		if ctx.Err() != nil {
			return "", errors.New("workspace Git operation timed out")
		}
		if err != nil {
			return "", fmt.Errorf("workspace Git operation failed: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return strings.TrimSpace(string(out)), nil
	}
}

func initWorkspaceRemote(c WorkspaceConfig, dir, git, url string) error {
	if c.Schema != "p.workspace/v1" {
		return errors.New("only workspace v1 clones from P")
	}
	st, err := os.Lstat(dir)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return errors.New("workspace root missing or unsafe")
	}
	if owner(st) != uint32(os.Geteuid()) {
		return errors.New("workspace owner mismatch")
	}
	gitRun := workspaceGit(dir, git)
	marker := filepath.Join(dir, ".git", "p-initialized")
	if initialized, err := workspaceInitialized(dir, uint32(os.Geteuid())); err != nil || initialized {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("partial or unexpected workspace; refusing initialization")
	}
	if _, err := gitRun("init", "--quiet", "-b", c.Branch, "."); err != nil {
		return err
	}
	if _, err := gitRun("remote", "add", "origin", url); err != nil {
		return err
	}
	if c.InitialOID == "" {
		// Contact the remote: an unborn main bootstrap must still refer to an
		// actually empty P repository and keep HEAD explicitly on main.
		out, err := gitRun("ls-remote", "origin")
		if err != nil {
			return err
		}
		if out != "" {
			return errors.New("blank workspace remote has refs")
		}
	} else {
		if err := fetchAssignedBranch(gitRun, c); err != nil {
			return err
		}
		tree, err := gitRun("ls-tree", c.InitialOID, "--", "flake.nix")
		if err != nil {
			return err
		}
		if tree != "" {
			return errors.New("committed devShell activation unavailable for root flake")
		}
		if _, err := gitRun("update-ref", "refs/heads/"+c.Branch, c.InitialOID); err != nil {
			return err
		}
		if _, err := gitRun("checkout", "--quiet", "--force", c.Branch); err != nil {
			return err
		}
	}
	if c.InitialOID != "" {
		if _, err := gitRun("branch", "--set-upstream-to", "origin/"+c.Branch, c.Branch); err != nil {
			return err
		}
	} else {
		// An unborn branch has no remote-tracking ref yet, but its assigned
		// upstream can be recorded without inventing a commit or ref. This
		// happens only during first initialization, never on ordinary Start.
		if _, err := gitRun("config", "branch."+c.Branch+".remote", "origin"); err != nil {
			return err
		}
		if _, err := gitRun("config", "branch."+c.Branch+".merge", "refs/heads/"+c.Branch); err != nil {
			return err
		}
	}
	// This marker records only that first initialization finished. Systemd
	// readiness always comes from the interactive service, never this file.
	return os.WriteFile(marker, []byte("p.workspace/v1\n"), 0600)
}

// This is an initialization guard, not a readiness record. Once published,
// ordinary Start must leave the user's Git state and working files untouched.
func workspaceInitialized(dir string, uid uint32) (bool, error) {
	gitDir := filepath.Join(dir, ".git")
	if st, err := os.Lstat(gitDir); err == nil {
		if !st.IsDir() || owner(st) != uid {
			return false, errors.New("retained workspace Git directory changed")
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	marker := filepath.Join(gitDir, "p-initialized")
	if markerInfo, err := os.Lstat(marker); err == nil {
		if !markerInfo.Mode().IsRegular() || owner(markerInfo) != uid || markerInfo.Mode().Perm() != 0600 {
			return false, errors.New("retained workspace initialization record changed")
		}
		if markerInfo.Size() != int64(len("p.workspace/v1\n")) {
			return false, errors.New("retained workspace initialization record changed")
		}
		file, e := os.Open(marker)
		if e != nil {
			return false, errors.New("retained workspace initialization record changed")
		}
		data, e := io.ReadAll(io.LimitReader(file, int64(len("p.workspace/v1\n")+1)))
		file.Close()
		if e != nil || string(data) != "p.workspace/v1\n" {
			return false, errors.New("retained workspace initialization record changed")
		}
		// An established Start preserves ordinary user changes to Git state,
		// including HEAD, remotes, and newly committed flake inputs.
		return true, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	return false, nil
}

type boundedWorkspaceOutput struct {
	bytes.Buffer
	exceeded bool
}

func (b *boundedWorkspaceOutput) Write(p []byte) (int, error) {
	if len(p) > 4096-b.Len() {
		b.exceeded = true
		return 0, errors.New("Git output limit")
	}
	return b.Buffer.Write(p)
}

// fetchAssignedBranch requires that P still holds the session's branch at the
// captured commit.
func fetchAssignedBranch(gitRun func(...string) (string, error), c WorkspaceConfig) error {
	if _, err := gitRun("fetch", "--quiet", "--no-progress", "--no-tags", "origin", "+refs/heads/"+c.Branch+":refs/remotes/origin/"+c.Branch); err != nil {
		return err
	}
	oid, err := gitRun("rev-parse", "refs/remotes/origin/"+c.Branch)
	if err != nil || oid != c.InitialOID {
		return errors.New("assigned branch moved from captured OID")
	}
	return nil
}

// adoptWorkspaceRemote puts the clone a commit image holds, detached at the
// captured commit, on the session's branch with P as its upstream. Each step
// accepts the state its own earlier run leaves, so an interrupted adoption is
// repeated from the start; the marker comes last.
func adoptWorkspaceRemote(c WorkspaceConfig, dir, git, url string) error {
	if c.Schema != "p.workspace/v2" {
		return errors.New("only workspace v2 adopts an image clone")
	}
	uid := uint32(os.Geteuid())
	for _, path := range []string{dir, filepath.Join(dir, ".git")} {
		if err := workspaceDirectory(path, uid); err != nil {
			return err
		}
	}
	if initialized, err := workspaceInitialized(dir, uid); err != nil || initialized {
		return err
	}
	gitRun := workspaceGit(dir, git)
	// The image leaves HEAD detached at the commit; an earlier run may have
	// switched it to the branch already.
	if head, err := gitRun("rev-parse", "--verify", "HEAD"); err != nil || head != c.InitialOID {
		return errors.Join(err, errors.New("image workspace HEAD is not the captured commit"))
	}
	if name, err := gitRun("rev-parse", "--symbolic-full-name", "HEAD"); err != nil || name != "HEAD" && name != "refs/heads/"+c.Branch {
		return errors.Join(err, errors.New("image workspace HEAD is on another branch"))
	}
	if flake, err := gitRun("ls-tree", c.InitialOID, "--", "flake.nix"); err != nil {
		return err
	} else if (flake != "") != (c.EnvironmentSelection != "no-flake") {
		return errors.New("image environment selection differs from the commit's root flake")
	}
	switch remotes, err := gitRun("remote"); {
	case err != nil:
		return err
	case remotes == "":
		if _, err := gitRun("remote", "add", "origin", url); err != nil {
			return err
		}
	case remotes == "origin":
		if urls, err := gitRun("remote", "get-url", "--all", "origin"); err != nil || urls != url {
			return errors.Join(err, errors.New("image workspace origin is not P"))
		}
	default:
		return errors.New("image workspace has a remote other than P")
	}
	if err := fetchAssignedBranch(gitRun, c); err != nil {
		return err
	}
	switch tip, err := gitRun("branch", "--list", "--format=%(objectname)", c.Branch); {
	case err != nil:
		return err
	case tip == "":
		if _, err := gitRun("update-ref", "refs/heads/"+c.Branch, c.InitialOID, strings.Repeat("0", len(c.InitialOID))); err != nil {
			return err
		}
	case tip != c.InitialOID:
		return errors.New("assigned branch exists at another commit")
	}
	if _, err := gitRun("checkout", "--quiet", c.Branch, "--"); err != nil {
		return err
	}
	if _, err := gitRun("branch", "--set-upstream-to", "origin/"+c.Branch, c.Branch); err != nil {
		return err
	}
	return publishWorkspaceMarker(filepath.Join(dir, ".git"))
}

// publishWorkspaceMarker makes Git's changes durable, then the marker appears
// complete or not at all: a crash leaves either a repeatable adoption or a
// finished one, never a torn marker that blocks every later Start.
func publishWorkspaceMarker(gitDir string) error {
	if err := syncWorkspaceTree(gitDir); err != nil {
		return err
	}
	f, err := os.CreateTemp(gitDir, ".p-initialized-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.WriteString("p.workspace/v1\n")
	if err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(gitDir, "p-initialized")); err != nil {
		return err
	}
	return syncWorkspaceDirectory(gitDir)
}
