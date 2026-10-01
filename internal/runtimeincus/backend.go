// Package runtimeincus implements the local, confined Incus runtime effects.
// Callers choose one session; no method accepts an instance name or Incus argv.
package runtimeincus

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
	"slices"
	"strings"
	"syscall"
	"time"
)

const maxOutput = 1 << 20
const endpointStagingPath = "/opt/p/endpoints"

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var fingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Config struct {
	Binary             string   // trusted absolute Incus client executable
	JournalctlBinary   string   // optional trusted host journalctl; default discovered from PATH
	UserSocket         string   // explicit confined unix.socket.user; never auto-discovered
	Project            string   // configured Incus project, distinct from a P project
	BuilderStoragePool string   // trusted btrfs pool for disposable builders; empty disables builders
	PInstanceID        string   // trusted P instance UUID; required for private environment images
	EndpointPrefix     string   // configured project-allowed host path; empty disables mount
	DiskSourceCeilings []string // exact trusted restricted.devices.disk.paths entries
	PublicEgress       *PublicEgressConfig
}

type PublicEgressConfig struct {
	Network           string
	ACL               string
	BridgeIPv4        string
	DNS               []string
	SudoBinary        string
	NftBinary         string
	BridgeProofBinary string
}

type Session struct {
	InstanceUUID     string
	SessionUUID      string
	ProjectPath      string
	AssignedBranch   string // bound at assembly; immutable creation input
	InitialOID       string // captured committed tip, absent only for unborn main
	ContractVersion  string
	ImageFingerprint string
	EndpointSource   string // optional session-owned socket directory under EndpointPrefix
	WorkspaceOwner   string // exact source session UUID for a disposable read-only helper
	Grants           []FilesystemGrant
	PublicIPv4       string // exact SQLite-reserved static address; empty means no NIC
}

type FilesystemGrant struct {
	Name       string
	Source     string
	Type       string
	Access     string
	Executable bool
	Device     uint64
	Inode      uint64
	OwnerUID   uint32
	OwnerGID   uint32
}

type Observation struct {
	Exists          bool
	Name            string
	Status          string
	Fingerprint     string
	IncusUUID       string // server-created volatile.uuid
	Generation      string // volatile.uuid.generation changes on snapshot restore
	EndpointMounted bool
	GrantsMounted   bool
	MountedGrants   map[string]bool // exact validated instance devices; nil when absent
	PublicNIC       bool
}

type runner func(context.Context, string, []string, []string) ([]byte, error)

type Backend struct {
	config     Config
	run        runner
	runBuilder runner // purpose-specific bounded Nix output; nil uses run in tests
	validate   func(Config) error
	proofRun   runner
}

// New refuses an administrative socket, a root process, an unbounded project,
// and an untrusted executable before any Incus operation can be attempted.
func New(config Config) (*Backend, error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	return &Backend{config: config, run: runCommand, runBuilder: runBuilderCommand, validate: validateConfig, proofRun: runCommand}, nil
}

func validateConfig(c Config) error {
	if os.Geteuid() == 0 {
		return errors.New("runtime must not run as root")
	}
	if !filepath.IsAbs(c.Binary) || filepath.Clean(c.Binary) != c.Binary {
		return errors.New("Incus binary must be an absolute clean path")
	}
	if err := validateAncestorsWithOptions(c.Binary, 0, true, true); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(c.Binary)
	if err != nil || !filepath.IsAbs(resolved) {
		return errors.New("Incus binary path cannot be resolved")
	}
	if resolved != c.Binary {
		// An executable symlink is permitted only when its final target is in
		// the root-owned Nix store; inspect the target path independently.
		if !pathWithinCeiling(resolved, "/nix/store") {
			return errors.New("Incus binary symlink leaves Nix store")
		}
		if err := validateAncestorsWithOptions(resolved, 0, true, false); err != nil {
			return err
		}
	}
	if info, err := os.Stat(c.Binary); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || !ownedByRootOrDaemon(info) {
		return errors.New("Incus binary is missing or writable by untrusted users")
	}
	if !filepath.IsAbs(c.UserSocket) || filepath.Clean(c.UserSocket) != c.UserSocket || filepath.Base(c.UserSocket) != "unix.socket.user" {
		return errors.New("explicit confined user socket required")
	}
	if err := validateAncestors(c.UserSocket); err != nil {
		return err
	}
	if info, err := os.Lstat(c.UserSocket); err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode()&os.ModeSymlink != 0 || !ownedByRoot(info) {
		return errors.New("confined user socket unavailable")
	}
	admin := filepath.Join(filepath.Dir(c.UserSocket), "unix.socket")
	if err := syscall.Access(admin, 2); err == nil {
		return errors.New("current account can write administrative Incus socket")
	}
	if c.Project == "" || len(c.Project) > 64 || c.Project[0] == '-' || strings.ContainsAny(c.Project, ":/\x00\n\r") || c.Project == "default" {
		return errors.New("confined Incus project required")
	}
	if c.BuilderStoragePool != "" && !validBuilderPoolName(c.BuilderStoragePool) {
		return errors.New("invalid builder storage pool")
	}
	if c.PInstanceID != "" && !uuidPattern.MatchString(c.PInstanceID) {
		return errors.New("invalid P instance identity")
	}
	if c.EndpointPrefix != "" && (!filepath.IsAbs(c.EndpointPrefix) || filepath.Clean(c.EndpointPrefix) != c.EndpointPrefix) {
		return errors.New("endpoint prefix must be an absolute clean path")
	}
	if err := validateDiskCeilings(c); err != nil {
		return err
	}
	if err := validatePublicEgressConfig(c.PublicEgress); err != nil {
		return err
	}
	return nil
}

func validateSession(c Config, s Session) error {
	if !uuidPattern.MatchString(s.InstanceUUID) || !uuidPattern.MatchString(s.SessionUUID) || s.ProjectPath == "" || len(s.ProjectPath) > 1024 || strings.ContainsRune(s.ProjectPath, 0) || s.ContractVersion == "" || len(s.ContractVersion) > 32 || !fingerprintPattern.MatchString(s.ImageFingerprint) {
		return errors.New("invalid runtime session identity or image fingerprint")
	}
	if s.EndpointSource != "" {
		if c.EndpointPrefix == "" || !filepath.IsAbs(s.EndpointSource) || filepath.Clean(s.EndpointSource) != s.EndpointSource || !strings.HasPrefix(s.EndpointSource, c.EndpointPrefix+string(filepath.Separator)) {
			return errors.New("endpoint source exceeds configured ceiling")
		}
	}
	if s.WorkspaceOwner != "" && (!uuidPattern.MatchString(s.WorkspaceOwner) || s.ContractVersion != "p.workspace-helper/v1" || s.EndpointSource != "") {
		return errors.New("invalid workspace helper identity")
	}
	if err := validateRuntimeGrants(c, s, false); err != nil {
		return err
	}
	if err := validatePublicSession(c.PublicEgress, s); err != nil {
		return err
	}
	return nil
}

func (b *Backend) validateEndpoint(source string) error {
	if filepath.Dir(source) != b.config.EndpointPrefix {
		return errors.New("endpoint directory must be a direct child of the private prefix")
	}
	if err := validateAncestors(b.config.EndpointPrefix); err != nil {
		return err
	}
	prefix, err := os.Lstat(b.config.EndpointPrefix)
	if err != nil || !prefix.IsDir() || prefix.Mode().Perm() != 0700 || !ownedByCurrentUser(prefix) {
		return errors.New("endpoint prefix must be daemon-owned mode 0700")
	}
	path := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(source, path), path) {
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("endpoint source path contains missing or non-directory component")
		}
	}
	sourceInfo, err := os.Lstat(source)
	if err != nil || !sourceInfo.IsDir() || sourceInfo.Mode().Perm() != 0755 || !ownedByCurrentUser(sourceInfo) {
		return errors.New("session endpoint directory must be daemon-owned mode 0755")
	}
	dir, err := os.Open(source)
	if err != nil {
		return err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(3)
	if err != nil && err != io.EOF {
		return err
	}
	if len(entries) > 2 {
		return errors.New("endpoint source has too many entries")
	}
	for _, entry := range entries {
		if entry.Name() != "session.sock" && entry.Name() != "git.sock" || entry.Type()&os.ModeSocket == 0 {
			return errors.New("endpoint source may contain only fixed P sockets")
		}
		info, err := entry.Info()
		if err != nil || info.Mode().Perm() != 0666 || !ownedByCurrentUser(info) {
			return errors.New("endpoint socket must be daemon-owned mode 0666")
		}
	}
	if len(entries) != 2 {
		return errors.New("both fixed P sockets are required")
	}
	return nil
}

func ownedByRoot(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == 0
}

func ownedByRootOrDaemon(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && (st.Uid == 0 || st.Uid == uint32(os.Geteuid()))
}

func ownedByCurrentUser(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Geteuid())
}

func validateAncestors(name string) error {
	return validateAncestorsWithOptions(name, 0, false, false)
}

// rootUID is explicit so tests in a remapped user namespace can exercise the
// same rules without changing the production trust boundary (host UID 0).
func validateAncestorsWithRoot(name string, rootUID uint32) error {
	return validateAncestorsWithOptions(name, rootUID, false, false)
}

func validateAncestorsWithOptions(name string, rootUID uint32, allowNixStore bool, allowFinalLink bool) error {
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(name, current), current) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 && !(allowFinalLink && current == name) {
			return errors.New("runtime path contains a missing or symbolic-link component")
		}
		uid := fileUID(info)
		if info.Mode()&os.ModeSymlink != 0 && current == name && uid != rootUID && uid != uint32(os.Geteuid()) {
			return errors.New("runtime executable link has untrusted owner")
		}
		if current != name && (!info.IsDir() || info.Mode().Perm()&0022 != 0) {
			if !allowWritableAncestor(current, info.Mode(), uid, rootUID, allowNixStore) {
				return errors.New("runtime path ancestor is writable by untrusted users")
			}
		}
		if current != name && uid != rootUID && uid != uint32(os.Geteuid()) {
			return errors.New("runtime path ancestor has untrusted owner")
		}
	}
	return nil
}

func allowWritableAncestor(path string, mode os.FileMode, uid, rootUID uint32, allowNixStore bool) bool {
	if uid != rootUID || !mode.IsDir() || mode&os.ModeSticky == 0 {
		return false
	}
	if (path == "/tmp" || path == "/var/tmp") && mode.Perm() == 0777 {
		return true
	}
	return allowNixStore && path == "/nix/store" && mode.Perm() == 0775
}

func fileUID(info os.FileInfo) uint32 {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ^uint32(0)
	}
	return st.Uid
}

func (b *Backend) name(s Session) string {
	if s.WorkspaceOwner != "" {
		return "p-workspace-" + s.SessionUUID
	}
	return "p-" + s.SessionUUID
}

func runCommand(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
	return runCommandBounded(ctx, binary, argv, env, maxOutput, maxOutput, false)
}

func runBuilderCommand(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
	return runCommandBounded(ctx, binary, argv, env, 2<<20, 64<<10, true)
}

func runCommandBounded(ctx context.Context, binary string, argv, env []string, stdoutLimit, stderrLimit int, diagnostic bool) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, argv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 250 * time.Millisecond
	cmd.Env = env
	cmd.Stdin = nil
	stdout, stderr := limitedBuffer{limit: stdoutLimit}, limitedBuffer{limit: stderrLimit}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if stdout.exceeded || stderr.exceeded {
		return nil, errors.New("Incus output exceeded bound")
	}
	if err != nil {
		if diagnostic {
			return nil, fmt.Errorf("Incus command failed: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("Incus command failed: %w", err)
	}
	return stdout.Bytes(), nil
}

type limitedBuffer struct {
	buffer   bytes.Buffer
	exceeded bool
	limit    int
}

func (b *limitedBuffer) String() string { return b.buffer.String() }
func (b *limitedBuffer) Bytes() []byte  { return b.buffer.Bytes() }
func (b *limitedBuffer) Len() int       { return b.buffer.Len() }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	limit := b.limit
	if limit == 0 {
		limit = maxOutput
	}
	if len(p) > limit-b.Len() {
		b.exceeded = true
		return 0, errors.New("output limit")
	}
	return b.buffer.Write(p)
}

func (b *Backend) command(ctx context.Context, args ...string) ([]byte, error) {
	return b.commandWith(ctx, b.run, 120*time.Second, args...)
}

func (b *Backend) builderCommand(ctx context.Context, args ...string) ([]byte, error) {
	run := b.runBuilder
	if run == nil {
		run = b.run
	}
	return b.commandWith(ctx, run, 5*time.Minute, args...)
}

func (b *Backend) commandWith(ctx context.Context, run runner, timeout time.Duration, args ...string) ([]byte, error) {
	validate := b.validate
	if validate == nil {
		validate = validateConfig
	}
	if err := validate(b.config); err != nil {
		return nil, err
	}
	// No inherited INCUS_REMOTE, aliases, client config, proxies, or PATH.
	conf, err := os.MkdirTemp("", "p-incus-conf-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(conf)
	scoped, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	argv := append([]string{"--force-local", "--project", b.config.Project}, args...)
	env := []string{"INCUS_SOCKET=" + b.config.UserSocket, "INCUS_CONF=" + conf, "HOME=" + conf, "PATH=/usr/bin:/bin", "LANG=C"}
	out, err := run(scoped, b.config.Binary, argv, env)
	if scoped.Err() != nil {
		return nil, errors.Join(err, scoped.Err())
	}
	return out, err
}

type projectJSON struct {
	Config map[string]string `json:"config"`
	Name   string            `json:"name"`
}
type profileJSON struct {
	Config  map[string]string            `json:"config"`
	Devices map[string]map[string]string `json:"devices"`
	Name    string                       `json:"name"`
}

// CheckConfinement checks the actual project and default profile on every
// mutating call. A changed host policy blocks mutation rather than widening it.
func (b *Backend) CheckConfinement(ctx context.Context) error {
	if err := validateDiskCeilings(b.config); err != nil {
		return err
	}
	raw, err := b.command(ctx, "project", "list", "--format", "json")
	if err != nil {
		return err
	}
	var projects []projectJSON
	if err := decode(raw, &projects); err != nil {
		return err
	}
	found := false
	for _, p := range projects {
		if p.Name == b.config.Project {
			found = true
			nicRestriction := "block"
			if b.config.PublicEgress != nil {
				nicRestriction = "managed"
			}
			required := map[string]string{"restricted": "true", "restricted.containers.privilege": "isolated", "restricted.containers.nesting": "block", "restricted.containers.lowlevel": "block", "restricted.devices.nic": nicRestriction, "restricted.devices.gpu": "block", "restricted.devices.disk": "allow", "features.profiles": "true", "features.networks": "false"}
			for key, want := range required {
				if p.Config[key] != want {
					return fmt.Errorf("Incus project lacks %s=%s", key, want)
				}
			}
			if b.config.PublicEgress == nil && p.Config["restricted.networks.access"] != "" {
				return errors.New("network access differs from no-NIC project authority")
			}
			if b.config.PublicEgress != nil && p.Config["restricted.networks.access"] != b.config.PublicEgress.Network {
				return errors.New("Incus network access differs from trusted public egress network")
			}
			actual := parseDiskCeilings(p.Config["restricted.devices.disk.paths"])
			if !slices.Equal(actual, canonicalDiskCeilings(b.config.DiskSourceCeilings)) {
				return errors.New("Incus disk ceiling differs from trusted configuration")
			}
		}
	}
	if !found {
		return errors.New("configured Incus project not visible")
	}
	raw, err = b.command(ctx, "profile", "list", "--format", "json")
	if err != nil {
		return err
	}
	var profiles []profileJSON
	if err := decode(raw, &profiles); err != nil {
		return err
	}
	found = false
	for _, p := range profiles {
		if p.Name == "default" {
			found = true
			if p.Config["security.idmap.isolated"] != "true" || p.Config["security.privileged"] == "true" || p.Config["security.nesting"] == "true" {
				return errors.New("default profile lacks isolated unprivileged mapping")
			}
			for key := range p.Config {
				if strings.HasPrefix(key, "raw.") || strings.HasPrefix(key, "security.") && key != "security.idmap.isolated" {
					return errors.New("default profile has unsafe security configuration")
				}
			}
			root := 0
			for _, d := range p.Devices {
				if d["type"] != "disk" || d["path"] != "/" || d["source"] != "" {
					return errors.New("default profile has an extra or unsafe device")
				}
				root++
			}
			if root != 1 {
				return errors.New("default profile needs exactly one root disk")
			}
		}
	}
	if !found {
		return errors.New("default profile missing")
	}
	if b.config.PublicEgress != nil {
		if err := b.checkPublicEgress(ctx); err != nil {
			return err
		}
	}
	return nil
}

func pathWithinCeiling(path, ceiling string) bool {
	return path == ceiling || strings.HasPrefix(path, ceiling+string(filepath.Separator))
}

func canonicalDiskCeilings(paths []string) []string {
	result := slices.Clone(paths)
	slices.Sort(result)
	return result
}
func parseDiskCeilings(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	slices.Sort(parts)
	return parts
}
func validateDiskCeilings(c Config) error {
	if len(c.DiskSourceCeilings) == 0 || len(c.DiskSourceCeilings) > 8 {
		return errors.New("disk source ceilings must contain 1 to 8 trusted prefixes")
	}
	seen := map[string]bool{}
	for _, path := range c.DiskSourceCeilings {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || seen[path] {
			return errors.New("invalid or duplicate disk source ceiling")
		}
		seen[path] = true
		for _, sensitive := range []string{"/etc", "/home", "/root", "/var/lib/incus", "/var/lib/p", "/nix", "/run", "/proc", "/sys", "/dev", "/tmp", "/var/tmp"} {
			if pathWithinCeiling(path, sensitive) || pathWithinCeiling(sensitive, path) {
				return errors.New("disk source ceiling includes sensitive host path")
			}
		}
	}
	if c.EndpointPrefix != "" {
		covered := false
		for _, path := range c.DiskSourceCeilings {
			if pathWithinCeiling(c.EndpointPrefix, path) {
				covered = true
			}
		}
		if !covered {
			return errors.New("endpoint prefix exceeds trusted disk ceiling")
		}
	}
	return nil
}

func decode(raw []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	// Incus evolves metadata: decode from a generic intermediate while still
	// rejecting malformed JSON/trailing data and bounding the command output.
	var value any
	if err := d.Decode(&value); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("trailing Incus JSON")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, dst)
}

type instanceJSON struct {
	Name            string                       `json:"name"`
	Type            string                       `json:"type"`
	Status          string                       `json:"status"`
	Config          map[string]string            `json:"config"`
	ExpandedConfig  map[string]string            `json:"expanded_config"`
	Devices         map[string]map[string]string `json:"devices"`
	ExpandedDevices map[string]map[string]string `json:"expanded_devices"`
	Profiles        []string                     `json:"profiles"`
}

func (b *Backend) Inspect(ctx context.Context, s Session) (Observation, error) {
	return b.inspect(ctx, s, false)
}

// InspectCreated checks the exact owned instance even when an interrupted
// init has not yet attached its endpoint. Callers must not treat an observed
// instance as a missing image or silently adopt an incomplete endpoint.
func (b *Backend) InspectCreated(ctx context.Context, s Session) (Observation, error) {
	return b.inspect(ctx, s, true)
}

func (b *Backend) inspect(ctx context.Context, s Session, allowPendingMount bool) (Observation, error) {
	var observation Observation
	if err := validateSession(b.config, s); err != nil {
		return observation, err
	}
	raw, err := b.command(ctx, "list", "^"+b.name(s)+"$", "--format", "json")
	if err != nil {
		return observation, err
	}
	var instances []instanceJSON
	if err := decode(raw, &instances); err != nil {
		return observation, err
	}
	if len(instances) == 0 {
		return observation, nil
	}
	if len(instances) != 1 || instances[0].Name != b.name(s) {
		return observation, errors.New("ambiguous Incus instance observation")
	}
	in := instances[0]
	if in.Type != "container" || len(in.Profiles) != 1 || in.Profiles[0] != "default" || !validIdentity(in.Config, s) || !validIdentity(in.ExpandedConfig, s) {
		return observation, errors.New("Incus instance identity mismatch")
	}
	if !safeSecurity(in.Config) || !safeSecurity(in.ExpandedConfig) || in.ExpandedConfig["security.idmap.isolated"] != "true" {
		return observation, errors.New("Incus instance security changed")
	}
	grants := make(map[string]FilesystemGrant, len(s.Grants))
	for _, grant := range s.Grants {
		grants[grantDeviceName(grant.Name)] = grant
	}
	mounted := make(map[string]bool, len(s.Grants))
	publicNIC := false
	for name, d := range in.Devices {
		if name == "p-endpoint" && validEndpointDevice(d, s.EndpointSource) {
			continue
		}
		if name == publicNICName && s.PublicIPv4 != "" && exactPublicNIC(d, publicNICDevice(b.config.PublicEgress, s)) {
			publicNIC = true
			continue
		}
		grant, ok := grants[name]
		if !ok || !validGrantDevice(d, grant) {
			return observation, errors.New("Incus instance device changed")
		}
		mounted[grant.Name] = true
	}
	_, endpointMounted := in.Devices["p-endpoint"]
	if !allowPendingMount && (s.EndpointSource != "" && !endpointMounted || len(mounted) != len(s.Grants) || (s.PublicIPv4 != "") != publicNIC) {
		return observation, errors.New("Incus endpoint or grant mount missing")
	}
	if in.ExpandedDevices == nil || len(in.ExpandedDevices) < 1 || len(in.ExpandedDevices) > 2+len(s.Grants)+1 {
		return observation, errors.New("Incus effective devices missing or unsafe")
	}
	root, ok := in.ExpandedDevices["root"]
	if !ok || root["type"] != "disk" || root["path"] != "/" || root["pool"] == "" || root["source"] != "" {
		return observation, errors.New("Incus effective root disk unsafe")
	}
	for key := range root {
		if key != "type" && key != "path" && key != "pool" {
			return observation, errors.New("Incus effective root disk option unsafe")
		}
	}
	for name, d := range in.ExpandedDevices {
		if name == "root" {
			continue
		}
		if name == "p-endpoint" && validEndpointDevice(d, s.EndpointSource) {
			continue
		}
		if name == publicNICName && s.PublicIPv4 != "" && exactPublicNIC(d, publicNICDevice(b.config.PublicEgress, s)) {
			continue
		}
		grant, ok := grants[name]
		if !ok || !validGrantDevice(d, grant) {
			return observation, errors.New("Incus effective device changed")
		}
	}
	if s.EndpointSource != "" && !allowPendingMount {
		if _, ok := in.ExpandedDevices["p-endpoint"]; !ok {
			return observation, errors.New("Incus effective endpoint mount missing")
		}
	}
	if !allowPendingMount {
		for name := range grants {
			if _, ok := in.ExpandedDevices[name]; !ok {
				return observation, errors.New("Incus effective grant mount missing")
			}
		}
		if s.PublicIPv4 != "" {
			if _, ok := in.ExpandedDevices[publicNICName]; !ok {
				return observation, errors.New("Incus effective public NIC missing")
			}
		}
	}
	incusUUID := in.Config["volatile.uuid"]
	generation := in.Config["volatile.uuid.generation"]
	if !uuidPattern.MatchString(incusUUID) || in.ExpandedConfig["volatile.uuid"] != incusUUID ||
		!uuidPattern.MatchString(generation) || in.ExpandedConfig["volatile.uuid.generation"] != generation {
		incusUUID, generation = "", ""
	}
	observation = Observation{Exists: true, Name: in.Name, Status: in.Status, Fingerprint: s.ImageFingerprint,
		IncusUUID: incusUUID, Generation: generation, EndpointMounted: endpointMounted, GrantsMounted: len(mounted) == len(s.Grants), MountedGrants: mounted, PublicNIC: publicNIC}
	return observation, nil
}

func validIdentity(c map[string]string, s Session) bool {
	return c != nil && c["user.p.instance_uuid"] == s.InstanceUUID && c["user.p.session_uuid"] == s.SessionUUID && c["user.p.project_path"] == s.ProjectPath && c["user.p.contract_version"] == s.ContractVersion && c["user.p.image_fingerprint"] == s.ImageFingerprint && c["volatile.base_image"] == s.ImageFingerprint && c["user.p.workspace_owner"] == s.WorkspaceOwner
}

func safeSecurity(c map[string]string) bool {
	if c == nil {
		return false
	}
	allowed := map[string]string{"security.idmap.isolated": "true", "security.privileged": "false", "security.nesting": "false"}
	for key, value := range c {
		if strings.HasPrefix(key, "raw.") {
			return false
		}
		if strings.HasPrefix(key, "security.") {
			want, ok := allowed[key]
			if !ok || want != value {
				return false
			}
		}
	}
	return true
}

func validEndpointDevice(d map[string]string, source string) bool {
	if source == "" || len(d) != 6 || d["type"] != "disk" || d["path"] != endpointStagingPath || d["source"] != source || d["readonly"] != "true" || d["propagation"] != "private" || d["shift"] != "false" {
		return false
	}
	return true
}

func (b *Backend) Create(ctx context.Context, s Session) (Observation, error) {
	return b.create(ctx, s, nil)
}

// CreateWithGate persists an exact core init intent after all no-effect checks
// and immediately before a name-targeted Incus init can be submitted.
func (b *Backend) CreateWithGate(ctx context.Context, s Session, beforeInit func() error) (Observation, error) {
	if beforeInit == nil {
		return Observation{}, errors.New("runtime init gate missing")
	}
	return b.create(ctx, s, beforeInit)
}

// beforeInit persists the trusted core effect-attempt marker after every
// deterministic preflight and before the Incus init request can be sent.
func (b *Backend) create(ctx context.Context, s Session, beforeInit func() error) (Observation, error) {
	if err := validateSession(b.config, s); err != nil {
		return Observation{}, err
	}
	if err := validateRuntimeGrants(b.config, s, true); err != nil {
		return Observation{}, err
	}
	if s.EndpointSource != "" {
		if err := b.validateEndpoint(s.EndpointSource); err != nil {
			return Observation{}, err
		}
	}
	if err := b.CheckConfinement(ctx); err != nil {
		return Observation{}, err
	}
	if err := b.checkPublicAddressCollision(ctx, s); err != nil {
		return Observation{}, err
	}
	before, err := b.inspect(ctx, s, true)
	if err != nil {
		return before, err
	}
	if before.Exists {
		if (s.EndpointSource == "" || before.EndpointMounted) && before.GrantsMounted && (s.PublicIPv4 == "" || before.PublicNIC) {
			return before, nil
		}
		return b.ensureSessionDevices(ctx, s, before)
	}
	// Incus's image digest must exist locally; no remote or alias is accepted.
	raw, err := b.command(ctx, "image", "list", "--format", "json")
	if err != nil {
		return Observation{}, err
	}
	var images []struct {
		Fingerprint string `json:"fingerprint"`
		Type        string `json:"type"`
	}
	if err := decode(raw, &images); err != nil {
		return Observation{}, err
	}
	present := false
	for _, image := range images {
		if image.Fingerprint == s.ImageFingerprint && image.Type == "container" {
			present = true
		}
	}
	if !present {
		return Observation{}, errors.New("pinned container image not present in confined project")
	}
	// A missing deterministic name does not prove the session runtime absent:
	// an external rename can leave its UUID labels on another instance. Check
	// the full project before recording or sending an ordinary session init.
	// Disposable helpers have their own operation identity and ownership rules.
	if s.WorkspaceOwner == "" {
		if err := b.ConfirmSessionRuntimeAbsent(ctx, s); err != nil {
			return Observation{}, err
		}
	}
	if beforeInit != nil {
		if err := beforeInit(); err != nil {
			return Observation{}, err
		}
	}
	argv := []string{"init", s.ImageFingerprint, b.name(s), "--profile", "default", "--config", "security.idmap.isolated=true", "--config", "security.privileged=false", "--config", "security.nesting=false", "--config", "user.p.instance_uuid=" + s.InstanceUUID, "--config", "user.p.session_uuid=" + s.SessionUUID, "--config", "user.p.project_path=" + s.ProjectPath, "--config", "user.p.contract_version=" + s.ContractVersion, "--config", "user.p.image_fingerprint=" + s.ImageFingerprint}
	if s.WorkspaceOwner != "" {
		argv = append(argv, "--config", "user.p.workspace_owner="+s.WorkspaceOwner,
			"--config", "limits.cpu="+workspaceHelperCPU,
			"--config", "limits.memory="+workspaceHelperMemory,
			"--config", "limits.processes="+workspaceHelperProcesses)
	}
	_, mutationErr := b.command(ctx, argv...)
	after, inspectErr := b.inspect(ctx, s, true)
	if inspectErr != nil {
		return Observation{}, errors.Join(mutationErr, inspectErr)
	}
	if !after.Exists {
		return after, errors.Join(mutationErr, errors.New("Incus create postcondition failed"))
	}
	return b.ensureSessionDevices(ctx, s, after)
}

func (b *Backend) ensureSessionDevices(ctx context.Context, s Session, before Observation) (Observation, error) {
	if !strings.EqualFold(before.Status, "Stopped") && (!before.GrantsMounted || s.EndpointSource != "" && !before.EndpointMounted || s.PublicIPv4 != "" && !before.PublicNIC) {
		return before, errors.New("session devices cannot be repaired on running instance")
	}
	if s.PublicIPv4 != "" && !before.PublicNIC {
		device := publicNICDevice(b.config.PublicEgress, s)
		argv := []string{"config", "device", "add", b.name(s), publicNICName, "nic"}
		for _, key := range []string{"name", "network", "ipv4.address", "ipv6.address", "security.acls", "security.acls.default.ingress.action", "security.acls.default.egress.action", "security.mac_filtering", "security.ipv4_filtering", "security.ipv6_filtering"} {
			argv = append(argv, key+"="+device[key])
		}
		_, mutationErr := b.command(ctx, argv...)
		after, inspectErr := b.inspect(ctx, s, true)
		if inspectErr != nil {
			return Observation{}, errors.Join(mutationErr, inspectErr)
		}
		if !after.PublicNIC {
			return after, errors.Join(mutationErr, errors.New("public NIC postcondition failed"))
		}
		before = after
	}
	for _, grant := range s.Grants {
		if before.MountedGrants[grant.Name] {
			continue
		}
		if err := inspectRuntimeGrantSource(grant); err != nil {
			return before, err
		}
		argv := grantDeviceAddArgs(b.name(s), grant)
		_, mutationErr := b.command(ctx, argv...)
		after, inspectErr := b.inspect(ctx, s, true)
		if inspectErr != nil {
			return Observation{}, errors.Join(mutationErr, inspectErr)
		}
		if !after.MountedGrants[grant.Name] {
			return after, errors.Join(mutationErr, fmt.Errorf("grant device %s postcondition failed (exists=%t status=%s observed=%d/%d)",
				grant.Name, after.Exists, after.Status, len(after.MountedGrants), len(s.Grants)))
		}
		before = after
	}
	if s.EndpointSource != "" && !before.EndpointMounted {
		return b.ensureEndpoint(ctx, s, before)
	}
	return b.Inspect(ctx, s)
}

func (b *Backend) ensureEndpoint(ctx context.Context, s Session, before Observation) (Observation, error) {
	if err := b.validateEndpoint(s.EndpointSource); err != nil {
		return before, err
	}
	if !strings.EqualFold(before.Status, "Stopped") {
		return before, errors.New("endpoint mount cannot be repaired on running instance")
	}
	// The mount is a socket-only directory; no key/config is placed on it.
	_, mutationErr := b.command(ctx, "config", "device", "add", b.name(s), "p-endpoint", "disk", "source="+s.EndpointSource, "path="+endpointStagingPath, "readonly=true", "propagation=private", "shift=false")
	after, inspectErr := b.Inspect(ctx, s)
	if inspectErr != nil {
		return Observation{}, errors.Join(mutationErr, inspectErr)
	}
	return after, nil
}

func (b *Backend) Start(ctx context.Context, s Session) (Observation, error) {
	return b.transition(ctx, s, "start")
}
func (b *Backend) Stop(ctx context.Context, s Session) (Observation, error) {
	return b.transition(ctx, s, "stop")
}
func (b *Backend) Delete(ctx context.Context, s Session) (Observation, error) {
	return b.transition(ctx, s, "delete")
}

func (b *Backend) transition(ctx context.Context, s Session, action string) (Observation, error) {
	if action == "start" && s.EndpointSource != "" {
		if err := b.validateEndpoint(s.EndpointSource); err != nil {
			return Observation{}, err
		}
	}
	if action == "start" {
		if err := validateRuntimeGrants(b.config, s, true); err != nil {
			return Observation{}, err
		}
	}
	if err := b.CheckConfinement(ctx); err != nil {
		return Observation{}, err
	}
	if action == "start" {
		if err := b.checkPublicAddressCollision(ctx, s); err != nil {
			return Observation{}, err
		}
	}
	before, err := b.Inspect(ctx, s)
	if err != nil {
		return Observation{}, err
	}
	if !before.Exists {
		if action == "delete" {
			return before, nil
		}
		return before, errors.New("runtime instance missing")
	}
	want := "Running"
	if action != "start" {
		want = "Stopped"
	}
	if action == "delete" || !strings.EqualFold(before.Status, want) {
		argv := []string{action, b.name(s)}
		if action == "stop" {
			argv = append(argv, "--timeout", "30")
		}
		_, err = b.command(ctx, argv...)
	}
	after, inspectErr := b.Inspect(ctx, s)
	if inspectErr != nil {
		return Observation{}, errors.Join(err, inspectErr)
	}
	if action == "delete" {
		if after.Exists {
			return after, errors.Join(err, errors.New("Incus delete postcondition failed"))
		}
		return after, nil
	}
	if !after.Exists || !strings.EqualFold(after.Status, want) {
		return after, errors.Join(err, errors.New("Incus state postcondition failed"))
	}
	return after, nil
}
