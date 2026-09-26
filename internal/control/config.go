package control

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

const HostSchema = "p.host/v1"

// HostConfig is supplied only by the trusted host owner. Repository content
// and session requests cannot choose a project policy.
type HostConfig struct {
	Schema   string         `json:"schema"`
	StateDir string         `json:"state_dir"`
	Git      *GitConfig     `json:"git,omitempty"`
	Runtime  *RuntimeConfig `json:"runtime,omitempty"`
	Events   *EventsConfig  `json:"events,omitempty"`
}

// EventsConfig selects one trusted host event handler.
type EventsConfig struct {
	ActivationPath string `json:"activation_path"`
	PluginID       string `json:"plugin_id"`
}

// RuntimeConfig is trusted instance configuration. Session requests cannot
// supply or widen these values.
type RuntimeConfig struct {
	ActivationPath       string                   `json:"activation_path"`
	RuntimePluginID      string                   `json:"runtime_plugin_id"`
	HostPluginID         string                   `json:"host_plugin_id"`
	IncusBinary          string                   `json:"incus_binary"`
	IncusUserSocket      string                   `json:"incus_user_socket"`
	IncusProject         string                   `json:"incus_project"`
	EndpointPrefix       string                   `json:"endpoint_prefix"`
	DiskSourceCeilings   []string                 `json:"disk_source_ceilings"`
	BaseImageFingerprint string                   `json:"base_image_fingerprint"`
	ProjectPolicy        ProjectPolicy            `json:"project_policy"`
	ProjectPolicies      map[string]ProjectPolicy `json:"project_policies,omitempty"`
	PublicEgress         *PublicEgressConfig      `json:"public_egress,omitempty"`
	Environment          *EnvironmentConfig       `json:"environment,omitempty"`
	AgentAdapter         *AgentAdapterConfig      `json:"agent_adapter,omitempty"`
}

// AgentAdapterConfig selects one trusted, optional session asset package.
// Session requests and repository content never choose this package.
type AgentAdapterConfig struct {
	ActivationPath string `json:"activation_path"`
	PluginID       string `json:"plugin_id"`
}

// EnvironmentConfig is a closed trusted host selection. Project source and
// session requests cannot select a module, system, Incus pool, or Nix policy.
type EnvironmentConfig struct {
	ActivationPath     string `json:"activation_path"`
	PluginID           string `json:"plugin_id"`
	System             string `json:"system"`
	BuilderStoragePool string `json:"builder_storage_pool"`
}

type ProjectPolicy struct {
	Network            string            `json:"network"`
	PublicEgressSHA256 string            `json:"public_egress_sha256,omitempty"`
	FilesystemMounts   []FilesystemGrant `json:"filesystem_mounts"`
	Command            []string          `json:"command"`
}

// PolicyForProject resolves only an exact complete P project path when an
// explicit map is configured. The singleton remains a legacy compatibility
// mode and is never a fallback for a partial map.
func (r RuntimeConfig) PolicyForProject(path string) (ProjectPolicy, bool) {
	if !validProject(path) {
		return ProjectPolicy{}, false
	}
	if r.ProjectPolicies != nil {
		policy, ok := r.ProjectPolicies[path]
		return policy, ok
	}
	return r.ProjectPolicy, true
}

func validProjectPolicy(policy ProjectPolicy) error {
	if policy.Network != "none" && policy.Network != "public-egress" || len(policy.FilesystemMounts) > 8 || len(policy.Command) == 0 || len(policy.Command) > 32 {
		return errors.New("project policy requires a confined baseline")
	}
	if err := validateGrantShapes(policy.FilesystemMounts); err != nil {
		return err
	}
	if policy.Network == "none" && policy.PublicEgressSHA256 != "" || policy.PublicEgressSHA256 != "" && !validFingerprint(policy.PublicEgressSHA256) {
		return errors.New("project network authority digest is invalid")
	}
	if !filepath.IsAbs(policy.Command[0]) || filepath.Clean(policy.Command[0]) != policy.Command[0] {
		return errors.New("project command must be absolute and clean")
	}
	for _, arg := range policy.Command {
		if arg == "" || len(arg) > 4096 || strings.IndexByte(arg, 0) >= 0 {
			return errors.New("invalid project command")
		}
	}
	policyJSON, err := json.Marshal(policy)
	if err != nil || len(policyJSON) > 8192 {
		return errors.New("project policy exceeds 8 KiB")
	}
	return nil
}

// ProjectPolicySnapshot applies the same canonical object encoding used by
// SQLite creation and policy updates, so status comparisons use exact digests.
func ProjectPolicySnapshot(policy ProjectPolicy) (json.RawMessage, string, error) {
	if err := validProjectPolicy(policy); err != nil {
		return nil, "", err
	}
	if policy.FilesystemMounts == nil {
		policy.FilesystemMounts = []FilesystemGrant{}
	} else {
		policy.FilesystemMounts = slices.Clone(policy.FilesystemMounts)
		slices.SortFunc(policy.FilesystemMounts, func(a, b FilesystemGrant) int { return strings.Compare(a.Name, b.Name) })
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		return nil, "", err
	}
	normalized, err := normalizedObject(raw)
	if err != nil {
		return nil, "", err
	}
	return normalized, digest(normalized), nil
}

// StoredProjectPolicySHA interprets an immutable snapshot under the current
// closed grant schema. Unknown future fields cannot be silently discarded by
// a downgraded daemon and reported as the current, safer policy.
func StoredProjectPolicySHA(raw json.RawMessage) (string, error) {
	policy, err := ParseStoredProjectPolicy(raw)
	if err != nil {
		return "", err
	}
	_, sha, err := ProjectPolicySnapshot(policy)
	return sha, err
}

func ParseStoredProjectPolicy(raw json.RawMessage) (ProjectPolicy, error) {
	var policy ProjectPolicy
	if len(raw) == 0 || len(raw) > 8192 {
		return policy, errors.New("stored project policy unavailable")
	}
	if err := rejectDuplicateKeys(raw); err != nil {
		return policy, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return policy, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return policy, errors.New("stored project policy has trailing data")
	}
	if err := validProjectPolicy(policy); err != nil {
		return policy, err
	}
	if policy.Network == "public-egress" && policy.PublicEgressSHA256 == "" {
		return policy, errors.New("stored public egress authority is unavailable")
	}
	for _, grant := range policy.FilesystemMounts {
		if grant.SourceIdentity == nil || grant.SourceIdentity.Inode == 0 {
			return policy, errors.New("stored filesystem grant identity unavailable")
		}
	}
	return policy, nil
}

func StoredProjectPolicyShapeSHA(raw json.RawMessage) (string, error) {
	policy, err := ParseStoredProjectPolicy(raw)
	if err != nil {
		return "", err
	}
	for i := range policy.FilesystemMounts {
		policy.FilesystemMounts[i].SourceIdentity = nil
	}
	_, sha, err := ProjectPolicySnapshot(policy)
	return sha, err
}

// GitConfig selects a reviewed source-Git package and a private host listener.
type GitConfig struct {
	ActivationPath string `json:"activation_path"`
	SourcePluginID string `json:"source_plugin_id"`
	Listen         string `json:"listen"`
}

// CheckTrustedAncestors rejects paths below an untrusted writable or symlinked
// directory. A root-owned sticky directory such as /tmp is an exception.
func CheckTrustedAncestors(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("path must be absolute and clean")
	}
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("untrusted path ancestor %s", dir)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return errors.New("cannot inspect path owner")
		}
		if stat.Uid != 0 && stat.Uid != uint32(os.Geteuid()) {
			return fmt.Errorf("foreign-owned path ancestor %s", dir)
		}
		if info.Mode().Perm()&0022 != 0 && !(stat.Uid == 0 && info.Mode()&os.ModeSticky != 0) {
			return fmt.Errorf("writable path ancestor %s", dir)
		}
		if dir == "/" {
			return nil
		}
	}
}

func LoadHostConfig(path string) (HostConfig, error) {
	return loadHostConfig(path, CheckTrustedAncestors)
}

func loadHostConfig(path string, checkPath func(string) error) (HostConfig, error) {
	var cfg HostConfig
	if err := checkPath(path); err != nil {
		return cfg, err
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		return cfg, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return cfg, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 || info.Mode().Perm()&0077 != 0 {
		return cfg, errors.New("host configuration must be a private, singly linked regular file owned by the daemon user")
	}
	if info.Size() > 65536 {
		return cfg, errors.New("host configuration exceeds 64 KiB")
	}
	data, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil {
		return cfg, err
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return cfg, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return cfg, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return cfg, errors.New("host configuration must contain one JSON object")
	}
	if cfg.Schema != HostSchema {
		return cfg, errors.New("unsupported host configuration schema")
	}
	if err := checkPath(cfg.StateDir); err != nil {
		return cfg, fmt.Errorf("state_dir: %w", err)
	}
	if cfg.Git != nil {
		if cfg.Git.SourcePluginID == "" || strings.TrimSpace(cfg.Git.SourcePluginID) != cfg.Git.SourcePluginID {
			return cfg, errors.New("git.source_plugin_id is required")
		}
		if err := checkPath(cfg.Git.ActivationPath); err != nil {
			return cfg, fmt.Errorf("git.activation_path: %w", err)
		}
		host, port, err := net.SplitHostPort(cfg.Git.Listen)
		if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
			return cfg, errors.New("git.listen must be a numeric loopback TCP address")
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 0 || n > 65535 {
			return cfg, errors.New("git.listen requires a valid TCP port")
		}
	}
	if cfg.Events != nil {
		if cfg.Events.PluginID == "" || strings.TrimSpace(cfg.Events.PluginID) != cfg.Events.PluginID {
			return cfg, errors.New("events.plugin_id is required")
		}
		if err := checkPath(cfg.Events.ActivationPath); err != nil {
			return cfg, fmt.Errorf("events.activation_path: %w", err)
		}
	}
	if cfg.Runtime != nil {
		r := cfg.Runtime
		var fields struct {
			Runtime map[string]json.RawMessage `json:"runtime"`
		}
		if err := json.Unmarshal(data, &fields); err != nil {
			return cfg, err
		}
		_, singleton := fields.Runtime["project_policy"]
		mapValue, mapped := fields.Runtime["project_policies"]
		mapValue = bytes.TrimSpace(mapValue)
		if singleton == mapped || mapped && (len(mapValue) == 0 || mapValue[0] != '{') {
			return cfg, errors.New("runtime requires exactly one project policy mode")
		}
		if cfg.Git == nil || r.RuntimePluginID == "" || r.HostPluginID == "" || r.IncusProject == "" ||
			len(r.BaseImageFingerprint) != 64 || len(r.DiskSourceCeilings) == 0 || len(r.DiskSourceCeilings) > 8 ||
			mapped && r.ProjectPolicies == nil {
			return cfg, errors.New("runtime requires Git, pinned plugins/image and a confined baseline policy")
		}
		for _, value := range []string{r.ActivationPath, r.IncusBinary, r.IncusUserSocket, r.EndpointPrefix} {
			if err := checkPath(value); err != nil {
				return cfg, fmt.Errorf("runtime path: %w", err)
			}
		}
		for _, value := range r.DiskSourceCeilings {
			if err := checkPath(value); err != nil {
				return cfg, fmt.Errorf("runtime disk ceiling: %w", err)
			}
		}
		if e := r.PublicEgress; e != nil {
			if err := e.Validate(); err != nil {
				return cfg, err
			}
			// The NixOS sudo wrapper has one fixed, root-owned generation link.
			// runtimeincus.New validates that resolved chain and the immutable
			// nft store path before any native operation; the general host-path
			// check deliberately rejects all symbolic-link ancestors.
		}
		if singleton {
			if r.ProjectPolicy.PublicEgressSHA256 != "" {
				return cfg, errors.New("public egress authority is derived from trusted substrate")
			}
			if err := validProjectPolicy(r.ProjectPolicy); err != nil {
				return cfg, err
			}
			if r.ProjectPolicy.Network == "public-egress" && r.PublicEgress == nil {
				return cfg, errors.New("public egress policy lacks a trusted network substrate")
			}
			if r.ProjectPolicy.Network == "public-egress" {
				r.ProjectPolicy.PublicEgressSHA256 = r.PublicEgress.SHA256()
			}
			if err := ValidateConfiguredGrants(r.ProjectPolicy, GrantBoundary{Ceilings: r.DiskSourceCeilings, StateDir: cfg.StateDir, EndpointPrefix: r.EndpointPrefix, IncusSocket: r.IncusUserSocket}); err != nil {
				return cfg, err
			}
		} else {
			for project, policy := range r.ProjectPolicies {
				if policy.PublicEgressSHA256 != "" {
					return cfg, errors.New("public egress authority is derived from trusted substrate")
				}
				if !validProject(project) {
					return cfg, errors.New("runtime project policy key must be an exact P project path")
				}
				if err := validProjectPolicy(policy); err != nil {
					return cfg, fmt.Errorf("project_policies[%q]: %w", project, err)
				}
				if policy.Network == "public-egress" && r.PublicEgress == nil {
					return cfg, fmt.Errorf("project_policies[%q] lacks a trusted public egress substrate", project)
				}
				if policy.Network == "public-egress" {
					policy.PublicEgressSHA256 = r.PublicEgress.SHA256()
					r.ProjectPolicies[project] = policy
				}
				if err := ValidateConfiguredGrants(policy, GrantBoundary{Ceilings: r.DiskSourceCeilings, StateDir: cfg.StateDir, EndpointPrefix: r.EndpointPrefix, IncusSocket: r.IncusUserSocket}); err != nil {
					return cfg, fmt.Errorf("project_policies[%q]: %w", project, err)
				}
			}
		}
		for _, c := range r.BaseImageFingerprint {
			if c < '0' || c > '9' && (c < 'a' || c > 'f') {
				return cfg, errors.New("invalid base image fingerprint")
			}
		}
		if e := r.Environment; e != nil {
			if e.PluginID == "" || strings.TrimSpace(e.PluginID) != e.PluginID || len(e.PluginID) > 128 ||
				(e.System != "x86_64-linux" && e.System != "aarch64-linux") ||
				!validTrustedPool(e.BuilderStoragePool) {
				return cfg, errors.New("invalid trusted environment selection")
			}
			if err := checkPath(e.ActivationPath); err != nil {
				return cfg, fmt.Errorf("environment.activation_path: %w", err)
			}
		}
		if a := r.AgentAdapter; a != nil {
			if a.PluginID == "" || strings.TrimSpace(a.PluginID) != a.PluginID || len(a.PluginID) > 128 {
				return cfg, errors.New("invalid trusted agent adapter selection")
			}
			if err := checkPath(a.ActivationPath); err != nil {
				return cfg, fmt.Errorf("agent_adapter.activation_path: %w", err)
			}
		}
	}
	return cfg, nil
}

func validTrustedPool(name string) bool {
	if name == "" || len(name) > 63 || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for _, c := range name[1:] {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

func EnsureStateDir(path string) error {
	return ensureStateDir(path, CheckTrustedAncestors)
}

func ensureStateDir(path string, checkPath func(string) error) error {
	if err := checkPath(path); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm() != 0700 {
		return errors.New("state directory must be private and owned by the daemon user")
	}
	return nil
}
