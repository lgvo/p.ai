package runtimekit

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	TmuxPath   = "/usr/libexec/p/tmux"
	KitPath    = "/usr/libexec/p/runtime-kit"
	SocketPath = "/run/p-interactive/tmux.sock"
	Session    = "p"
)

func Validate() error {
	if os.Geteuid() != 1000 {
		return errors.New("runtime service requires fixed p uid 1000")
	}
	cfg, err := ReadConfig()
	if err != nil {
		return fmt.Errorf("trusted config: %w", err)
	}
	if cfg.Activation == "devshell" {
		if err := VerifyDevShellMaterial(cfg.MaterialSHA256); err != nil {
			return fmt.Errorf("trusted activation material: %w", err)
		}
	}
	if err := ValidateFixedAssets(cfg.AgentSHA256); err != nil {
		return fmt.Errorf("trusted assets: %w", err)
	}
	if err := ValidatePaths(); err != nil {
		return fmt.Errorf("fixed paths: %w", err)
	}
	if err := validateFilesystemMounts(cfg.FilesystemMounts); err != nil {
		return fmt.Errorf("filesystem grants: %w", err)
	}
	if err := ValidatePublicNetwork(cfg.PublicNetwork); err != nil {
		return fmt.Errorf("public network: %w", err)
	}
	if _, err := os.Lstat(SocketPath); !os.IsNotExist(err) {
		return errors.New("tmux socket already exists")
	}
	return nil
}

func ValidateFixedAssets(agentSHA256 string) error {
	for _, item := range []struct {
		visible, target string
		mode            os.FileMode
	}{
		{"/etc/systemd/system/p-session.target", "/etc/p/assets/p-session.target", 0644},
		{"/etc/systemd/system/p-interactive.service", "/etc/p/assets/p-interactive.service", 0644},
		{"/usr/libexec/p/attach", "/etc/p/assets/p-attach", 0555},
		{"/usr/libexec/p/git-ssh", "/etc/p/assets/p-git-ssh", 0555},
	} {
		st, err := os.Lstat(item.visible)
		if err != nil || st.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("%s must be fixed image link", item.visible)
		}
		resolved, err := filepath.EvalSymlinks(item.visible)
		if err != nil || resolved != item.target {
			return fmt.Errorf("%s resolves outside trusted assets", item.visible)
		}
		st, err = os.Lstat(item.target)
		if err != nil || !st.Mode().IsRegular() || owner(st) != 0 || st.Mode().Perm() != item.mode {
			return fmt.Errorf("%s has unsafe type, owner, or mode", item.target)
		}
	}
	if agentSHA256 != "" {
		const visible = "/usr/libexec/p/codex-adapter"
		const target = "/etc/p/assets/p-codex-adapter"
		st, err := os.Lstat(visible)
		if err != nil || st.Mode()&os.ModeSymlink == 0 {
			return errors.New("selected agent adapter link is missing")
		}
		resolved, err := filepath.EvalSymlinks(visible)
		if err != nil || resolved != target {
			return errors.New("selected agent adapter link changed")
		}
		fd, err := syscall.Open(target, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
		if err != nil {
			return errors.New("selected agent adapter is missing")
		}
		file := os.NewFile(uintptr(fd), target)
		defer file.Close()
		st, err = file.Stat()
		if err != nil || !st.Mode().IsRegular() || owner(st) != 0 || st.Mode().Perm() != 0555 || st.Size() < 1 || st.Size() > 1<<20 {
			return errors.New("selected agent adapter has unsafe metadata")
		}
		data, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
		h := sha256.Sum256(data)
		if err != nil || hex.EncodeToString(h[:]) != agentSHA256 {
			return errors.New("selected agent adapter bytes changed")
		}
	}
	return nil
}

// PrepareHost is ExecStartPost. Systemd keeps the unit activating until it
// returns; the foreground tmux server itself is the service MainPID.
func PrepareHost() error {
	if os.Geteuid() != 1000 {
		return errors.New("runtime service requires fixed p uid 1000")
	}
	if err := waitForServer(); err != nil {
		return err
	}
	cmd := exec.Command(TmuxPath, "-S", SocketPath, "-f", "/dev/null", "new-session", "-E", "-d", "-s", Session, "-c", "/workspace", KitPath, "run-command")
	cmd.Dir = "/workspace"
	cmd.Env = append(os.Environ(), "HOME=/home/p")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tmux startup: %w: %s", err, short(output))
	}
	cmd = exec.Command(TmuxPath, "-S", SocketPath, "set-option", "-g", "exit-empty", "on")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tmux lifetime setup: %w: %s", err, short(output))
	}
	// A short settling interval catches commands that exit as tmux starts.
	// ExecStartPost keeps Type=exec activating throughout this interval.
	time.Sleep(250 * time.Millisecond)
	if err := probeHost(); err != nil {
		return fmt.Errorf("tmux did not become attachable: %w", err)
	}
	return nil
}

func RunCommand() error {
	if os.Geteuid() != 1000 {
		return errors.New("interactive command requires fixed p uid 1000")
	}
	cfg, err := ReadConfig()
	if err != nil {
		return err
	}
	if err := validateFilesystemMounts(cfg.FilesystemMounts); err != nil {
		return err
	}
	if err := os.Chdir("/workspace"); err != nil {
		return err
	}
	if err := syscall.Exec(cfg.Command[0], cfg.Command, os.Environ()); err != nil {
		return fmt.Errorf("exec configured command: %w", err)
	}
	return nil
}

// StartHost is the service MainPID. Activation and its hook run once as the
// fixed user before tmux exists; systemd's Type=exec/ExecStartPost remains the
// only readiness authority. The configured pane command receives exported
// values inherited from the activated tmux server.
func StartHost() error {
	if os.Geteuid() != 1000 {
		return errors.New("interactive host requires fixed p uid 1000")
	}
	cfg, err := ReadConfig()
	if err != nil {
		return err
	}
	if err := validateFilesystemMounts(cfg.FilesystemMounts); err != nil {
		return err
	}
	if err := os.Chdir("/workspace"); err != nil {
		return err
	}
	closed := []string{"HOME=/home/p", "USER=p", "LOGNAME=p", "SHELL=/run/current-system/sw/bin/bash", "PATH=/run/current-system/sw/bin:/usr/bin:/bin", "LANG=C.UTF-8", "NIX_REMOTE=daemon"}
	if cfg.Activation == "devshell" {
		if err := VerifyDevShellMaterial(cfg.MaterialSHA256); err != nil {
			return err
		}
		argv := []string{"bash", "--noprofile", "--norc", "-c", `builtin source /etc/p/devshell/activate.sh || exit $?; builtin exec /usr/libexec/p/tmux -D -S /run/p-interactive/tmux.sock -f /opt/p/tmux.conf`, "p-devshell"}
		if err := syscall.Exec("/run/current-system/sw/bin/bash", argv, closed); err != nil {
			return fmt.Errorf("exec devShell host: %w", err)
		}
		return nil
	}
	if err := syscall.Exec(TmuxPath, []string{"tmux", "-D", "-S", SocketPath, "-f", "/opt/p/tmux.conf"}, closed); err != nil {
		return fmt.Errorf("exec base host: %w", err)
	}
	return nil
}

func ValidatePaths() error {
	for _, p := range []struct {
		path string
		uid  uint32
	}{
		{"/workspace", 1000}, {"/home/p", 1000}, {"/nix", 0},
		{"/run", 0}, {"/etc", 0}, {"/etc/p", 0}, {"/etc/p/git", 0},
	} {
		st, err := os.Lstat(p.path)
		if err != nil {
			return fmt.Errorf("%s: %w", p.path, err)
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || owner(st) != p.uid {
			return fmt.Errorf("%s: wrong type or owner", p.path)
		}
		if p.uid == 0 && st.Mode().Perm()&022 != 0 {
			return fmt.Errorf("%s: writable by session user", p.path)
		}
	}
	for _, path := range []string{"/etc/p/git/ssh_config", "/etc/p/git/known_hosts"} {
		if err := regularOwned(path, 0); err != nil {
			return err
		}
	}
	if err := privateKey("/etc/p/git/identity", 1000); err != nil {
		return err
	}
	if err := validateEndpoints(); err != nil {
		return err
	}
	st, err := os.Lstat("/run/p-interactive")
	if err != nil || !st.IsDir() || owner(st) != 1000 || st.Mode().Perm()&077 != 0 {
		return errors.New("tmux runtime directory has invalid owner or mode")
	}
	return nil
}

func regularOwned(path string, uid uint32) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || owner(st) != uid || st.Mode().Perm()&022 != 0 || st.Sys().(*syscall.Stat_t).Nlink != 1 {
		return fmt.Errorf("%s: wrong type, owner, or mode", path)
	}
	return nil
}

func owner(st os.FileInfo) uint32 { return st.Sys().(*syscall.Stat_t).Uid }

func probeHost() error {
	st, err := os.Lstat(SocketPath)
	if err != nil || st.Mode()&os.ModeSocket == 0 || owner(st) != 1000 {
		return errors.New("tmux socket is not available")
	}
	cmd := exec.Command(TmuxPath, "-S", SocketPath, "has-session", "-t", "="+Session)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tmux session absent: %s", short(output))
	}
	return nil
}

func waitForServer() error {
	// ExecStart sources the captured shellHook before tmux opens its socket.
	// This remains inside the unit's 120-second startup deadline.
	deadline := time.Now().Add(75 * time.Second)
	for time.Now().Before(deadline) {
		st, err := os.Lstat(SocketPath)
		if err == nil && st.Mode()&os.ModeSocket != 0 && owner(st) == 1000 {
			conn, err := net.DialTimeout("unix", SocketPath, 100*time.Millisecond)
			if err == nil {
				conn.Close()
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("foreground tmux server did not open fixed socket")
}

func Shutdown() error {
	if os.Geteuid() != 0 {
		return errors.New("shutdown requires root")
	}
	cmd := exec.Command("/usr/libexec/p/systemctl", "--no-block", "poweroff")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("container poweroff: %w: %s", err, short(output))
	}
	return nil
}

func short(data []byte) string {
	s := strings.TrimSpace(string(data))
	if len(s) > 512 {
		return s[:512]
	}
	return s
}
