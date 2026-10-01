package runtimekit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const ConfigPath = "/etc/p/session.json"

// Config is written only by trusted session assembly. Later activation kinds
// require an explicit runtime-kit revision; repository files cannot add one.
type Config struct {
	Schema           string            `json:"schema"`
	Activation       string            `json:"activation"`
	MaterialSHA256   string            `json:"material_sha256,omitempty"`
	AgentSHA256      string            `json:"agent_sha256,omitempty"`
	Command          []string          `json:"command"`
	FilesystemMounts []FilesystemGrant `json:"filesystem_mounts,omitempty"`
	PublicNetwork    *PublicNetwork    `json:"public_network,omitempty"`
}

type PublicNetwork struct {
	Address string   `json:"address"`
	Gateway string   `json:"gateway"`
	DNS     []string `json:"dns"`
}

// FilesystemGrant contains only guest-visible authority. Host sources and
// source identities remain in the host's immutable policy and Incus device.
type FilesystemGrant struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Access     string `json:"access"`
	Executable bool   `json:"executable"`
	Device     uint64 `json:"device"`
	Inode      uint64 `json:"inode"`
}

func ParseConfig(data []byte) (Config, error) {
	var cfg Config
	if len(data) == 0 || len(data) > 64<<10 {
		return cfg, errors.New("session config size is invalid")
	}
	if err := uniqueJSONKeys(data); err != nil {
		return cfg, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return cfg, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return cfg, errors.New("trailing session config data")
	}
	base := cfg.Schema == "p.runtime-session/v1" && cfg.Activation == "base" && cfg.MaterialSHA256 == "" && cfg.AgentSHA256 == ""
	devshell := cfg.Schema == "p.runtime-session/v2" && cfg.Activation == "devshell" && validSHA256(cfg.MaterialSHA256) && cfg.AgentSHA256 == ""
	agent := cfg.Schema == "p.runtime-session/v3" && validSHA256(cfg.AgentSHA256) &&
		(cfg.Activation == "base" && cfg.MaterialSHA256 == "" || cfg.Activation == "devshell" && validSHA256(cfg.MaterialSHA256))
	grantSession := cfg.Schema == "p.runtime-session/v4" && len(cfg.FilesystemMounts) > 0 && len(cfg.FilesystemMounts) <= 8 &&
		(cfg.Activation == "base" && cfg.MaterialSHA256 == "" || cfg.Activation == "devshell" && validSHA256(cfg.MaterialSHA256)) &&
		(cfg.AgentSHA256 == "" || validSHA256(cfg.AgentSHA256))
	publicSession := cfg.Schema == "p.runtime-session/v5" && cfg.PublicNetwork != nil && len(cfg.FilesystemMounts) <= 8 &&
		(cfg.Activation == "base" && cfg.MaterialSHA256 == "" || cfg.Activation == "devshell" && validSHA256(cfg.MaterialSHA256)) &&
		(cfg.AgentSHA256 == "" || validSHA256(cfg.AgentSHA256))
	if !base && !devshell && !agent && !grantSession && !publicSession || !grantSession && !publicSession && len(cfg.FilesystemMounts) != 0 || !publicSession && cfg.PublicNetwork != nil {
		return cfg, errors.New("unsupported session schema or activation")
	}
	if publicSession {
		if err := validatePublicNetwork(*cfg.PublicNetwork); err != nil {
			return cfg, err
		}
	}
	seen := map[string]bool{}
	for _, grant := range cfg.FilesystemMounts {
		if !guestGrantName.MatchString(grant.Name) || seen[grant.Name] ||
			(grant.Type != "file" && grant.Type != "directory") ||
			(grant.Access != "read-only" && grant.Access != "read-write") || grant.Inode == 0 {
			return cfg, errors.New("invalid session filesystem grant")
		}
		seen[grant.Name] = true
	}
	if len(cfg.Command) == 0 || len(cfg.Command) > 32 {
		return cfg, errors.New("command must contain 1 to 32 arguments")
	}
	var total int
	for i, arg := range cfg.Command {
		if (i == 0 && arg == "") || len(arg) > 4096 || strings.IndexByte(arg, 0) >= 0 {
			return cfg, fmt.Errorf("invalid command argument %d", i)
		}
		total += len(arg)
	}
	if total > 16<<10 {
		return cfg, errors.New("command arguments exceed 16 KiB")
	}
	if !filepath.IsAbs(cfg.Command[0]) || filepath.Clean(cfg.Command[0]) != cfg.Command[0] {
		return cfg, errors.New("command executable must be an absolute path")
	}
	return cfg, nil
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ReadConfig refuses symlinks and user-writable config. The parent directory
// is made root-owned and non-writable to p by the image module.
func ReadConfig() (Config, error) {
	parent, err := os.Lstat(filepath.Dir(ConfigPath))
	if err != nil || !parent.IsDir() || owner(parent) != 0 || parent.Mode().Perm()&022 != 0 {
		return Config{}, errors.New("session config directory must be root-owned")
	}
	fd, err := syscall.Open(ConfigPath, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return Config{}, err
	}
	f := os.NewFile(uintptr(fd), ConfigPath)
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Config{}, err
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || sys.Uid != 0 || sys.Nlink != 1 || st.Mode().Perm()&022 != 0 || st.Size() > 64<<10 {
		return Config{}, errors.New("session config must be a bounded root-owned regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, 64<<10+1))
	if err != nil {
		return Config{}, err
	}
	return ParseConfig(data)
}

func uniqueJSONKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		d, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch d {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return errors.New("duplicate session config key")
				}
				seen[name] = true
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		case '[':
			for dec.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		default:
			return errors.New("invalid session config JSON")
		}
	}
	if err := walk(); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("multiple session config values")
	}
	return nil
}
