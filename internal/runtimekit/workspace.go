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
var workspaceKey = regexp.MustCompile(`^[0-9a-f]{64}$`)

// WorkspaceConfig is trusted creation input. A blank bootstrap has no OID and
// must be the unborn main branch. Repository is a P Git project identifier.
type WorkspaceConfig struct {
	Schema               string `json:"schema"`
	Repository           string `json:"repository"`
	Branch               string `json:"branch"`
	InitialOID           string `json:"initial_oid,omitempty"`
	EnvironmentSelection string `json:"environment_selection,omitempty"`
	EnvironmentCommitOID string `json:"environment_commit_oid,omitempty"`
	EnvironmentKey       string `json:"environment_key,omitempty"`
	ImageFingerprint     string `json:"image_fingerprint,omitempty"` // v3: exact recorded image reused by explicit repair
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
	if c.Schema != "p.workspace/v1" && c.Schema != "p.workspace/v2" && c.Schema != "p.workspace/v3" || !workspaceName.MatchString(c.Repository) || len(c.Repository) > 255 || !workspaceName.MatchString(c.Branch) || len(c.Branch) > 200 || strings.Contains(c.Branch, "..") || strings.Contains(c.Branch, "@{") || strings.HasSuffix(c.Branch, ".lock") {
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
	if c.Schema == "p.workspace/v1" {
		if c.EnvironmentSelection != "" || c.EnvironmentCommitOID != "" || c.EnvironmentKey != "" || c.ImageFingerprint != "" {
			return c, errors.New("unresolved workspace cannot carry environment selection")
		}
	} else if c.Schema == "p.workspace/v2" && (c.InitialOID == "" || c.EnvironmentCommitOID != c.InitialOID || c.ImageFingerprint != "" ||
		!(c.EnvironmentSelection == "base-no-flake" && c.EnvironmentKey == "" ||
			c.EnvironmentSelection == "base-no-default" && c.EnvironmentKey == "" ||
			c.EnvironmentSelection == "devshell" && workspaceKey.MatchString(c.EnvironmentKey))) {
		return c, errors.New("accepted workspace environment differs from captured commit")
	} else if c.Schema == "p.workspace/v3" && (c.InitialOID == "" || !workspaceKey.MatchString(c.ImageFingerprint) ||
		!(c.EnvironmentSelection == "base-recorded" && c.EnvironmentKey == "" && (c.EnvironmentCommitOID == "" || workspaceOID.MatchString(c.EnvironmentCommitOID)) ||
			workspaceOID.MatchString(c.EnvironmentCommitOID) && (c.EnvironmentSelection == "base-no-flake" && c.EnvironmentKey == "" ||
				c.EnvironmentSelection == "base-no-default" && c.EnvironmentKey == "" ||
				c.EnvironmentSelection == "devshell" && workspaceKey.MatchString(c.EnvironmentKey)))) {
		return c, errors.New("repair image provenance unavailable")
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
	return initWorkspaceRemote(c, dir, git, "ssh://git@p/"+c.Repository)
}

func initWorkspaceRemote(c WorkspaceConfig, dir, git, url string) error {
	st, err := os.Lstat(dir)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return errors.New("workspace root missing or unsafe")
	}
	if owner(st) != uint32(os.Geteuid()) {
		return errors.New("workspace owner mismatch")
	}
	gitRun := func(args ...string) (string, error) {
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
		if _, err := gitRun("fetch", "--quiet", "--no-progress", "--no-tags", "origin", "+refs/heads/"+c.Branch+":refs/remotes/origin/"+c.Branch); err != nil {
			return err
		}
		oid, err := gitRun("rev-parse", "refs/remotes/origin/"+c.Branch)
		if err != nil || oid != c.InitialOID {
			return errors.New("assigned branch moved from captured OID")
		}
		// Repair v3 checks out the freshly accepted P tip while explicitly
		// retaining the recorded image's older environment provenance. A
		// flake-presence comparison against the new tip would attribute that
		// old image selection to a commit it was never built for.
		if c.Schema != "p.workspace/v3" {
			tree, err := gitRun("ls-tree", c.InitialOID, "--", "flake.nix")
			if err != nil {
				return err
			}
			if tree != "" && (c.Schema == "p.workspace/v1" || c.EnvironmentSelection == "base-no-flake") {
				return errors.New("committed devShell activation unavailable for root flake")
			}
			if tree == "" && c.Schema == "p.workspace/v2" && c.EnvironmentSelection != "base-no-flake" {
				return errors.New("accepted environment expected a captured root flake")
			}
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
