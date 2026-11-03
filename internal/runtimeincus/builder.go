package runtimeincus

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// Builder is a core-selected disposable build request, never a Session. Its
// identity is fixed before any Incus mutation and is not selected by source.
type Builder struct {
	RequestUUID          string // the build reference
	ProjectPath          string
	CommitOID            string
	BaseImageFingerprint string
	ContractVersion      string
	PublicIPv4           string // its session's SQLite-reserved static address; empty means no NIC
}

type BuilderObservation struct {
	Exists bool
	Name   string
	Status string
	Ready  bool // the bounded private root and any public NIC are installed
}

// builderDevices says which of a builder's devices are installed, for
// finishing an interrupted creation.
type builderDevices struct{ rootLimit, publicNIC bool }

const (
	builderCPU       = "2"
	builderMemory    = "4GiB"
	builderProcesses = "512"
	builderRootSize  = "8GiB"
	// The build's one input is a Git bundle of the commit, root-owned and
	// read-only in the guest. The builder user clones it into the workspace.
	builderBuildDir  = "/opt/p/build"
	builderBundle    = builderBuildDir + "/source.bundle"
	builderWorkspace = "/workspace"
	// The clone fetches the bundle's one ref here and deletes it again, so
	// the workspace ends with no ref at all.
	builderBundleRef = "refs/p/bundle"
	// The image links its runtime kit here at boot.
	builderRuntimeKit = "/usr/libexec/p/runtime-kit"
)

// BuilderPolicyDigest pins the native restriction and Nix execution contract
// in durable creation intent. A changed policy cannot silently resume an old
// operation under a new builder interpretation. Builders reach the public
// internet when the host has a public-egress substrate, named by its digest;
// an empty digest means builders get no NIC.
func BuilderPolicyDigest(publicEgressSHA256 string) string {
	network, nix := "no-nic", offlineBuilderNix
	if publicEgressSHA256 != "" {
		network, nix = "public-egress="+publicEgressSHA256, publicBuilderNix
	}
	sum := sha256.Sum256([]byte("p.builder/v1;cpu=" + builderCPU + ";memory=" + builderMemory + ";processes=" + builderProcesses + ";root=" + builderRootSize + ";pool=btrfs;" + network + ";no-nesting;no-privilege;source-bundle;" + nix.policy))
	return hex.EncodeToString(sum[:])
}

func validBuilderPoolName(name string) bool {
	if name == "" || len(name) > 63 || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for _, c := range name[1:] {
		if c < 'a' || c > 'z' {
			if c < '0' || c > '9' {
				if c != '-' {
					return false
				}
			}
		}
	}
	return true
}

// The pinned dir driver can accept a size while silently skipping quota.
// Btrfs propagates qgroup setup and resize failures, so only that driver is
// admitted until another driver has its own compatibility evidence.
func (b *Backend) checkBuilderPool(ctx context.Context) error {
	name := b.config.BuilderStoragePool
	if !validBuilderPoolName(name) {
		return errors.New("trusted builder storage pool required")
	}
	raw, err := b.command(ctx, "storage", "list", "--format", "json")
	if err != nil {
		return err
	}
	var pools []struct{ Name, Driver, Status string }
	if err := decode(raw, &pools); err != nil {
		return err
	}
	var found int
	for _, pool := range pools {
		if pool.Name == name {
			found++
			if pool.Driver != "btrfs" || pool.Status != "Created" {
				return errors.New("builder storage pool has unproven quota enforcement")
			}
		}
	}
	if found != 1 {
		return errors.New("builder storage pool missing or ambiguous")
	}
	return nil
}

func validateBuilder(r Builder) error {
	if !uuidPattern.MatchString(r.RequestUUID) || !validBuilderProject(r.ProjectPath) || !validBuilderOID(r.CommitOID) || !fingerprintPattern.MatchString(r.BaseImageFingerprint) || len(r.ContractVersion) == 0 || len(r.ContractVersion) > 32 || strings.ContainsAny(r.ContractVersion, "\x00\r\n") {
		return errors.New("invalid builder request identity")
	}
	return nil
}

// ProjectPath is P's logical project identity, never a host filesystem path.
func validBuilderProject(project string) bool {
	if project == "" || len(project) > 255 {
		return false
	}
	for _, part := range strings.Split(project, "/") {
		if part == "" || part == "." || part == ".." || len(part) > 100 {
			return false
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
				return false
			}
		}
	}
	return true
}

func validBuilderOID(oid string) bool {
	if len(oid) != 40 && len(oid) != 64 {
		return false
	}
	if strings.Trim(oid, "0") == "" {
		return false
	}
	for _, c := range oid {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}

func builderName(r Builder) string { return builderNameFor(r.RequestUUID) }

func builderNameFor(request string) string { return "p-builder-" + request }

func builderIdentity(c map[string]string, r Builder, instance string) bool {
	return c != nil && c["user.p.instance"] == instance && c["user.p.builder_request_uuid"] == r.RequestUUID && c["user.p.builder_project_path"] == r.ProjectPath && c["user.p.builder_commit_oid"] == r.CommitOID && c["user.p.builder_contract_version"] == r.ContractVersion && c["user.p.builder_base_image"] == r.BaseImageFingerprint && c["volatile.base_image"] == r.BaseImageFingerprint
}

// InspectBuilder verifies identity, isolation, effective devices, and resource
// bounds. Unknown or changed instances are errors, never cleanup candidates.
func (b *Backend) InspectBuilder(ctx context.Context, r Builder) (BuilderObservation, error) {
	out, _, err := b.inspectBuilder(ctx, r, false)
	return out, err
}

func (b *Backend) inspectBuilder(ctx context.Context, r Builder, allowPendingDevices bool) (BuilderObservation, builderDevices, error) {
	var out BuilderObservation
	var installed builderDevices
	if err := validateBuilder(r); err != nil {
		return out, installed, err
	}
	if !uuidPattern.MatchString(b.config.PInstanceID) {
		return out, installed, errors.New("trusted P instance identity required for builders")
	}
	var nic map[string]string
	if r.PublicIPv4 != "" {
		if b.config.PublicEgress == nil {
			return out, installed, errors.New("public builder requires the public egress substrate")
		}
		if err := validatePublicAddress(b.config.PublicEgress, r.PublicIPv4); err != nil {
			return out, installed, err
		}
		nic = publicNICDevice(b.config.PublicEgress, r.PublicIPv4)
	}
	if err := b.checkBuilderPool(ctx); err != nil {
		return out, installed, err
	}
	instances, err := b.listBuilder(ctx, r.RequestUUID)
	if err != nil {
		return out, installed, err
	}
	if len(instances) == 0 {
		return out, installed, nil
	}
	in := instances[0]
	if in.Type != "container" || len(in.Profiles) != 1 || in.Profiles[0] != "default" || !builderIdentity(in.Config, r, b.config.PInstanceID) || !builderIdentity(in.ExpandedConfig, r, b.config.PInstanceID) || !safeSecurity(in.Config) || !safeSecurity(in.ExpandedConfig) || in.ExpandedConfig["security.idmap.isolated"] != "true" {
		return out, installed, errors.New("builder identity or security changed")
	}
	for _, c := range []map[string]string{in.Config, in.ExpandedConfig} {
		for key := range c {
			if strings.HasPrefix(key, "environment.") {
				return out, installed, errors.New("builder inherited environment is not empty")
			}
		}
		if c["limits.cpu"] != builderCPU || c["limits.memory"] != builderMemory || c["limits.processes"] != builderProcesses {
			return out, installed, errors.New("builder resource limits changed")
		}
	}
	// Besides its root disk, a builder has only its session's public NIC,
	// and only when it reaches the public internet.
	_, nicExpanded := in.ExpandedDevices[publicNICName]
	_, nicLocal := in.Devices[publicNICName]
	if nicExpanded || nicLocal {
		if nic == nil || !exactPublicNIC(in.ExpandedDevices[publicNICName], nic) || !exactPublicNIC(in.Devices[publicNICName], nic) {
			return out, installed, errors.New("builder has an extra device")
		}
		installed.publicNIC = true
	}
	_, rootOverride := in.Devices["root"]
	if len(in.ExpandedDevices) != 1+btoi(installed.publicNIC) || len(in.Devices) != btoi(rootOverride)+btoi(installed.publicNIC) {
		return out, installed, errors.New("builder has an extra device")
	}
	root := in.ExpandedDevices["root"]
	if root["type"] != "disk" || root["path"] != "/" || root["pool"] != b.config.BuilderStoragePool || root["source"] != "" {
		return out, installed, errors.New("builder root disk unsafe")
	}
	for key := range root {
		if key != "type" && key != "path" && key != "pool" && key != "size" {
			return out, installed, errors.New("builder root disk option unsafe")
		}
	}
	if rootOverride {
		d := in.Devices["root"]
		if len(d) == 0 || d["type"] != "disk" || d["path"] != "/" || d["pool"] != b.config.BuilderStoragePool || d["size"] != builderRootSize || d["source"] != "" {
			return out, installed, errors.New("builder root override changed")
		}
		for key := range d {
			if key != "type" && key != "path" && key != "pool" && key != "size" {
				return out, installed, errors.New("builder root override option unsafe")
			}
		}
	} else if !allowPendingDevices {
		return out, installed, errors.New("builder root size limit missing")
	}
	installed.rootLimit = rootOverride && root["size"] == builderRootSize
	if !allowPendingDevices && root["size"] != builderRootSize {
		return out, installed, errors.New("builder root size limit changed")
	}
	// A NIC still to be attached leaves an owned builder unready, so an
	// interrupted creation can be finished or removed.
	ready := installed.rootLimit && (nic == nil || installed.publicNIC)
	return BuilderObservation{Exists: true, Name: in.Name, Status: in.Status, Ready: ready}, installed, nil
}

// listBuilder reads whatever instance holds a build reference's builder name,
// P's or not.
func (b *Backend) listBuilder(ctx context.Context, request string) ([]instanceJSON, error) {
	name := builderNameFor(request)
	raw, err := b.command(ctx, "list", "^"+name+"$", "--format", "json")
	if err != nil {
		return nil, err
	}
	var instances []instanceJSON
	if err := decode(raw, &instances); err != nil {
		return nil, err
	}
	if len(instances) > 1 || len(instances) == 1 && instances[0].Name != name {
		return nil, errors.New("ambiguous builder observation")
	}
	return instances, nil
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// CreateBuilder creates only a stopped builder from a locally pinned image.
// An interrupted request may finish its own root limit; a different request
// with the same name is never adopted.
func (b *Backend) CreateBuilder(ctx context.Context, r Builder) (BuilderObservation, error) {
	return b.createBuilder(ctx, r, nil)
}

// createBuilder with a gate requires the builder to be absent: it runs the
// read-only confinement, pool, identity and pinned-image checks, calls the
// gate, and only then initializes the builder.
func (b *Backend) createBuilder(ctx context.Context, r Builder, beforeInit func() error) (BuilderObservation, error) {
	if err := validateBuilder(r); err != nil {
		return BuilderObservation{}, err
	}
	if err := b.CheckConfinement(ctx); err != nil {
		return BuilderObservation{}, err
	}
	if err := b.checkBuilderPool(ctx); err != nil {
		return BuilderObservation{}, err
	}
	before, installed, err := b.inspectBuilder(ctx, r, true)
	if err != nil {
		return before, err
	}
	if before.Exists && beforeInit != nil {
		return before, fmt.Errorf("%w: a builder already holds the build's name", runtimedriver.ErrMismatch)
	}
	if before.Exists && before.Ready {
		return before, nil
	}
	if before.Exists && before.Status != "Stopped" {
		return before, errors.New("unfinished builder is running")
	}
	if !before.Exists {
		raw, err := b.command(ctx, "image", "list", "--format", "json")
		if err != nil {
			return BuilderObservation{}, err
		}
		var images []struct{ Fingerprint, Type string }
		if err := decode(raw, &images); err != nil {
			return BuilderObservation{}, err
		}
		found := false
		for _, image := range images {
			found = found || image.Fingerprint == r.BaseImageFingerprint && image.Type == "container"
		}
		if !found {
			return BuilderObservation{}, errors.New("pinned builder base image missing")
		}
		if beforeInit != nil {
			if err := beforeInit(); err != nil {
				return BuilderObservation{}, err
			}
		}
		_, createErr := b.command(ctx, "init", r.BaseImageFingerprint, builderName(r), "--profile", "default", "--storage", b.config.BuilderStoragePool, "--device", "root,size="+builderRootSize, "--config", "security.idmap.isolated=true", "--config", "security.privileged=false", "--config", "security.nesting=false", "--config", "limits.cpu="+builderCPU, "--config", "limits.memory="+builderMemory, "--config", "limits.processes="+builderProcesses, "--config", "user.p.instance="+b.config.PInstanceID, "--config", "user.p.builder_request_uuid="+r.RequestUUID, "--config", "user.p.builder_project_path="+r.ProjectPath, "--config", "user.p.builder_commit_oid="+r.CommitOID, "--config", "user.p.builder_contract_version="+r.ContractVersion, "--config", "user.p.builder_base_image="+r.BaseImageFingerprint)
		before, installed, err = b.inspectBuilder(ctx, r, true)
		if err != nil || !before.Exists {
			return before, errors.Join(createErr, err, errors.New("builder creation postcondition failed"))
		}
		if before.Ready {
			return before, nil
		}
	}
	if !installed.rootLimit {
		_, overrideErr := b.command(ctx, "config", "device", "override", builderName(r), "root", "size="+builderRootSize)
		after, devices, err := b.inspectBuilder(ctx, r, true)
		if err != nil || !after.Exists || !devices.rootLimit {
			return after, errors.Join(overrideErr, err, errors.New("builder root limit postcondition failed"))
		}
		installed = devices
	}
	if r.PublicIPv4 != "" && !installed.publicNIC {
		if err := b.checkPublicAddressCollision(ctx, builderName(r), r.PublicIPv4); err != nil {
			return before, err
		}
		_, addErr := b.command(ctx, publicNICAddArgs(builderName(r), publicNICDevice(b.config.PublicEgress, r.PublicIPv4))...)
		after, devices, err := b.inspectBuilder(ctx, r, true)
		if err != nil || !after.Exists || !devices.publicNIC {
			return after, errors.Join(addErr, err, errors.New("builder public NIC postcondition failed"))
		}
	}
	after, err := b.InspectBuilder(ctx, r)
	if err != nil || !after.Exists || !after.Ready {
		return after, errors.Join(err, errors.New("builder device postcondition failed"))
	}
	return after, nil
}

func (b *Backend) StartBuilder(ctx context.Context, r Builder) (BuilderObservation, error) {
	if err := b.CheckConfinement(ctx); err != nil {
		return BuilderObservation{}, err
	}
	before, err := b.InspectBuilder(ctx, r)
	if err != nil || !before.Exists || !before.Ready {
		return before, errors.Join(err, errors.New("builder not ready to start"))
	}
	if err := b.checkBuilderBundle(ctx, r); err != nil {
		return before, err
	}
	if err := b.checkPublicAddressCollision(ctx, builderName(r), r.PublicIPv4); err != nil {
		return before, err
	}
	after, err := b.builderTransition(ctx, r, "start")
	if err != nil || r.PublicIPv4 == "" {
		return after, err
	}
	return after, b.prepareBuilderNetwork(ctx, r)
}

// checkBuilderBundle proves the transferred bundle is still the root-owned,
// read-only input it was transferred as. Its size has no bound.
func (b *Backend) checkBuilderBundle(ctx context.Context, r Builder) error {
	api := &unixFileAPI{socket: b.config.UserSocket, project: b.config.Project, instance: builderName(r)}
	dir, exists, err := api.head(ctx, builderBuildDir, 0)
	if err != nil || !exists || dir.typ != "directory" || dir.uid != 0 || dir.gid != 0 || dir.mode != 0755 {
		return errors.Join(err, errors.New("builder source bundle directory changed"))
	}
	bundle, exists, err := api.head(ctx, builderBundle, math.MaxInt64)
	if err != nil || !exists || bundle.typ != "file" || bundle.uid != 0 || bundle.gid != 0 || bundle.mode != 0444 {
		return errors.Join(err, errors.New("builder source bundle is not sealed"))
	}
	return nil
}

// prepareBuilderNetwork gives a started public builder the network of a
// public-egress session: its address, default route and the fixed
// DNS-over-HTTPS resolver. The image's runtime kit does it, run as root with
// fixed arguments, before any Nix or project code runs in this boot.
func (b *Backend) prepareBuilderNetwork(ctx context.Context, r Builder) error {
	if err := b.waitBuilderBoot(ctx, r); err != nil {
		return err
	}
	if err := b.checkBuilderRunning(ctx, r); err != nil {
		return err
	}
	gateway := strings.SplitN(b.config.PublicEgress.BridgeIPv4, "/", 2)[0]
	if _, err := b.builderCommand(ctx, "exec", builderName(r), "--mode", "non-interactive", "--user", "0", "--group", "0", "--cwd", "/",
		"--env", "PATH=/run/current-system/sw/bin", "--env", "LANG=C", "--env", "LC_ALL=C",
		"--", builderRuntimeKit, "prepare-builder-network", r.PublicIPv4, gateway); err != nil {
		return fmt.Errorf("builder public network preparation failed: %w", err)
	}
	return nil
}

// waitBuilderBoot waits until this boot's systemd manager is up and early
// boot has created the image's fixed runtime paths. The manager's private
// socket lives on the /run tmpfs, so it proves the current boot: the Nix
// daemon socket file and the runtime kit link persist on the builder's disk
// from an earlier boot. The Nix daemon socket listens only after
// sysinit.target, which follows systemd-tmpfiles setup.
func (b *Backend) waitBuilderBoot(ctx context.Context, r Builder) error {
	ready, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := b.builderGuestExec(ready, r, "/bin/sh", "-c", "test -S /run/systemd/private && test -S "+builderNixSocket+" && test -x "+builderRuntimeKit); err == nil {
			return nil
		}
		select {
		case <-ready.Done():
			return errors.New("builder did not finish booting")
		case <-ticker.C:
		}
	}
}
func (b *Backend) StopBuilder(ctx context.Context, r Builder) (BuilderObservation, error) {
	return b.builderTransition(ctx, r, "stop")
}
func (b *Backend) DeleteBuilder(ctx context.Context, r Builder) (BuilderObservation, error) {
	return b.builderTransition(ctx, r, "delete")
}
func (b *Backend) builderTransition(ctx context.Context, r Builder, action string) (BuilderObservation, error) {
	if err := b.CheckConfinement(ctx); err != nil {
		return BuilderObservation{}, err
	}
	before, err := b.InspectBuilder(ctx, r)
	if err != nil {
		return before, err
	}
	if !before.Exists {
		if action == "delete" {
			return before, nil
		}
		return before, errors.New("builder missing")
	}
	if action == "delete" && before.Status != "Stopped" {
		if before, err = b.builderTransition(ctx, r, "stop"); err != nil {
			return before, err
		}
	}
	want := "Stopped"
	if action == "start" {
		want = "Running"
	}
	if action == "delete" || before.Status != want {
		args := []string{action, builderName(r)}
		var bootErr error
		if action == "stop" {
			args = append(args, "--timeout", "30")
			if before.Status == "Running" {
				bootErr = b.awaitGuestSystemd(ctx, builderName(r))
			}
		}
		_, err = b.lifecycleCommand(ctx, args...)
		err = errors.Join(bootErr, err)
	}
	if action == "delete" {
		// Deletion is the one operation whose successful postcondition is absence.
		// InspectBuilder still refuses an unknown replacement at this name.
		after, inspectErr := b.InspectBuilder(ctx, r)
		if inspectErr != nil || after.Exists {
			return after, errors.Join(err, inspectErr, errors.New("builder delete postcondition failed"))
		}
		return after, nil
	}
	after, inspectErr := b.InspectBuilder(ctx, r)
	if action == "stop" && inspectErr == nil && after.Exists && after.Status != want {
		// Still running after Incus's graceful timeout: stop it by force.
		_, forceErr := b.lifecycleCommand(ctx, "stop", builderName(r), "--force")
		err = errors.Join(err, forceErr)
		after, inspectErr = b.InspectBuilder(ctx, r)
	}
	if inspectErr != nil || !after.Exists || after.Status != want {
		return after, errors.Join(err, inspectErr, errors.New("builder state postcondition failed"))
	}
	return after, nil
}

// validBundleRef accepts the one ref a source export names: a plain ref
// below refs/ that a fixed Git argv can carry.
// safeGitToken is a ref name P accepts in a builder's bundle.
var safeGitToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$`)

func validBundleRef(ref string) bool {
	name, ok := strings.CutPrefix(ref, "refs/")
	if !ok || !safeGitToken.MatchString(name) || strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".lock") || strings.Contains(name, "//") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}

func checkSourceExport(src runtimedriver.SourceExport, r Builder) error {
	if src.Commit != r.CommitOID || !validBundleRef(src.Ref) {
		return errors.New("source export differs from builder request")
	}
	return nil
}

// openBundle opens the host bundle core staged: a non-empty regular file the
// daemon owns, never through a symlink.
func openBundle(path string) (*os.File, int64, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, 0, errors.New("source bundle path must be absolute and clean")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, 0, fmt.Errorf("open source bundle: %w", err)
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !ownedByCurrentUser(info) || info.Size() <= 0 {
		f.Close()
		return nil, 0, errors.Join(err, errors.New("source bundle is not a non-empty regular file the daemon owns"))
	}
	return f, info.Size(), nil
}

// TransferBuilderSource puts the source export's bundle at
// /opt/p/build/source.bundle in the stopped builder, root-owned and
// read-only, through the confined Incus file API. The bytes stream from the
// host file and are never held in memory; the guest file's size is checked
// afterwards. Existing entries are never overwritten or adopted.
func (b *Backend) TransferBuilderSource(ctx context.Context, r Builder, src runtimedriver.SourceExport) error {
	if err := validateBuilder(r); err != nil {
		return err
	}
	if err := checkSourceExport(src, r); err != nil {
		return err
	}
	bundle, size, err := openBundle(src.Bundle)
	if err != nil {
		return err
	}
	defer bundle.Close()
	if err := b.CheckConfinement(ctx); err != nil {
		return err
	}
	o, err := b.InspectBuilder(ctx, r)
	if err != nil || !o.Exists || o.Status != "Stopped" || !o.Ready {
		return errors.Join(err, errors.New("source transfer requires owned stopped builder"))
	}
	if err := transferBuilderBundle(ctx, b.checkedBuilderFiles(r), bundle, size); err != nil {
		return err
	}
	// The file is core's; a change while it streamed is a different input.
	if info, err := bundle.Stat(); err != nil || info.Size() != size {
		return errors.Join(err, errors.New("source bundle changed during transfer"))
	}
	return nil
}

type builderBundleFiles interface {
	fileAPI
	putStream(ctx context.Context, path string, f guestFile, body io.Reader, size int64) error
	headSize(ctx context.Context, path string) (guestFile, int64, bool, error)
}

func transferBuilderBundle(ctx context.Context, api builderBundleFiles, bundle io.Reader, size int64) error {
	// The fixed image may already have /opt and /opt/p. Create absent ancestors
	// through Incus, but never follow or replace an existing entry.
	for _, p := range []string{"/", "/opt", "/opt/p"} {
		f, exists, err := api.get(ctx, p)
		if err == nil && !exists && p != "/" {
			if err = createGuestAbsent(ctx, api, p, guestFile{typ: "directory", uid: 0, gid: 0, mode: 0755}); err != nil {
				return err
			}
			f, exists, err = api.get(ctx, p)
		}
		if err != nil || !exists || f.typ != "directory" || f.uid != 0 || f.gid != 0 || f.mode&022 != 0 || f.mode&0005 != 0005 {
			return errors.Join(err, fmt.Errorf("unsafe builder source ancestor %s", p))
		}
	}
	// The bundle's directory is new, so nothing in it is overwritten.
	if err := createGuestAbsent(ctx, api, builderBuildDir, guestFile{typ: "directory", uid: 0, gid: 0, mode: 0755}); err != nil {
		return err
	}
	want := guestFile{typ: "file", uid: 0, gid: 0, mode: 0444}
	if err := api.putStream(ctx, builderBundle, want, io.LimitReader(bundle, size), size); err != nil {
		return fmt.Errorf("stream builder source bundle: %w", err)
	}
	got, written, exists, err := api.headSize(ctx, builderBundle)
	if err != nil || !exists || got.typ != want.typ || got.uid != want.uid || got.gid != want.gid || got.mode != want.mode || written != size {
		return errors.Join(err, fmt.Errorf("builder source bundle postcondition failed: %d of %d bytes", written, size))
	}
	return nil
}

// filesURL addresses one guest path through the fixed instance file endpoint.
func (a *unixFileAPI) filesURL(path string) string {
	u := url.URL{Scheme: "http", Host: "incus", Path: "/1.0/instances/" + url.PathEscape(a.instance) + "/files"}
	q := u.Query()
	q.Set("project", a.project)
	q.Set("path", path)
	u.RawQuery = q.Encode()
	return u.String()
}

// putStream creates a guest file from exactly size bytes of body. Unlike
// put, it holds no copy of the bytes and has no request time limit of its
// own: ctx bounds it.
func (a *unixFileAPI) putStream(ctx context.Context, path string, f guestFile, body io.Reader, size int64) error {
	if f.typ != "file" || size < 0 {
		return errors.New("streamed upload is for regular files only")
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", a.socket)
	}}
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.filesURL(path), io.NopCloser(body))
	if err != nil {
		return err
	}
	req.ContentLength = size
	req.Header.Set("X-Incus-type", f.typ)
	req.Header.Set("X-Incus-uid", strconv.Itoa(f.uid))
	req.Header.Set("X-Incus-gid", strconv.Itoa(f.gid))
	req.Header.Set("X-Incus-mode", fmt.Sprintf("%04o", f.mode))
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("Incus file POST status %d", resp.StatusCode)
	}
	return nil
}

// headSize reads a guest path's metadata and, for a regular file, its size,
// with no bound on the size. HTTP HEAD follows symlinks.
func (a *unixFileAPI) headSize(ctx context.Context, path string) (guestFile, int64, bool, error) {
	resp, err := a.request(ctx, http.MethodHead, path, guestFile{})
	if err != nil {
		return guestFile{}, 0, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return guestFile{}, 0, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return guestFile{}, 0, false, fmt.Errorf("Incus file HEAD status %d", resp.StatusCode)
	}
	meta, err := guestMetadata(resp.Header)
	if err != nil {
		return guestFile{}, 0, false, err
	}
	var size int64 = -1
	if meta.typ == "file" {
		size, err = strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
		if err != nil || size < 0 {
			return guestFile{}, 0, false, errors.New("Incus file size unavailable")
		}
	}
	return meta, size, true, nil
}

func (b *Backend) checkedBuilderFiles(r Builder) *builderCheckedFileAPI {
	return &builderCheckedFileAPI{backend: b, request: r, inner: &unixFileAPI{socket: b.config.UserSocket, project: b.config.Project, instance: builderName(r)}}
}

// Recheck the Incus authority and exact stopped builder before every file API
// effect. A replacement at the deterministic name cannot inherit the request.
type builderCheckedFileAPI struct {
	backend *Backend
	request Builder
	inner   *unixFileAPI
}

func (a *builderCheckedFileAPI) check(ctx context.Context) error {
	if err := a.backend.CheckConfinement(ctx); err != nil {
		return err
	}
	o, err := a.backend.InspectBuilder(ctx, a.request)
	if err != nil {
		return err
	}
	if !o.Exists || !o.Ready || o.Status != "Stopped" {
		return errors.New("builder changed while stopped for a file operation")
	}
	return nil
}
func (a *builderCheckedFileAPI) get(ctx context.Context, path string) (guestFile, bool, error) {
	if err := a.check(ctx); err != nil {
		return guestFile{}, false, err
	}
	return a.inner.get(ctx, path)
}
func (a *builderCheckedFileAPI) getBounded(ctx context.Context, path string, maxBytes int64) (guestFile, bool, error) {
	if err := a.check(ctx); err != nil {
		return guestFile{}, false, err
	}
	return a.inner.getBounded(ctx, path, maxBytes)
}
func (a *builderCheckedFileAPI) getSymlinkBounded(ctx context.Context, path string, maxBytes int64) (guestFile, bool, error) {
	if err := a.check(ctx); err != nil {
		return guestFile{}, false, err
	}
	return a.inner.getSymlinkBounded(ctx, path, maxBytes)
}
func (a *builderCheckedFileAPI) headSize(ctx context.Context, path string) (guestFile, int64, bool, error) {
	if err := a.check(ctx); err != nil {
		return guestFile{}, 0, false, err
	}
	return a.inner.headSize(ctx, path)
}
func (a *builderCheckedFileAPI) lstat(ctx context.Context, path string) (guestFile, error) {
	if err := a.check(ctx); err != nil {
		return guestFile{}, err
	}
	return a.inner.lstat(ctx, path)
}
func (a *builderCheckedFileAPI) put(ctx context.Context, path string, f guestFile) error {
	if err := a.check(ctx); err != nil {
		return err
	}
	return a.inner.put(ctx, path, f)
}
func (a *builderCheckedFileAPI) putStream(ctx context.Context, path string, f guestFile, body io.Reader, size int64) error {
	if err := a.check(ctx); err != nil {
		return err
	}
	return a.inner.putStream(ctx, path, f, body, size)
}
func (a *builderCheckedFileAPI) chmodDirectory(ctx context.Context, path string, mode int) error {
	if err := a.check(ctx); err != nil {
		return err
	}
	return a.inner.chmodDirectory(ctx, path, mode)
}
func (a *builderCheckedFileAPI) fullDirectoryMode(ctx context.Context, path string) (guestFile, error) {
	if err := a.check(ctx); err != nil {
		return guestFile{}, err
	}
	return a.inner.fullDirectoryMode(ctx, path)
}
func (a *builderCheckedFileAPI) deleteTree(ctx context.Context, path string) error {
	if err := a.check(ctx); err != nil {
		return err
	}
	return a.inner.deleteTree(ctx, path)
}

// CloneBuilderSource clones the transferred bundle into the running
// builder's empty workspace as the builder user, with fixed Git arguments
// and no hooks, filters or configuration beyond the repository's own. It
// then proves the result: a clone at the commit with a detached HEAD, no
// branch, no remote, no other ref and a clean tree.
func (b *Backend) CloneBuilderSource(ctx context.Context, r Builder, src runtimedriver.SourceExport) error {
	if err := checkSourceExport(src, r); err != nil {
		return err
	}
	// The clone's tools and the workspace come from this boot's activation,
	// so the clone waits for the boot, whoever started the builder.
	if err := b.waitBuilderBoot(ctx, r); err != nil {
		return err
	}
	contents, err := b.builderGuestExec(ctx, r, "/run/current-system/sw/bin/ls", "-A", builderWorkspace)
	if err != nil || len(contents) != 0 {
		return errors.Join(err, errors.New("builder workspace is not empty"))
	}
	// The files ref backend keeps HEAD in .git/HEAD, where the stopped image
	// is checked through the file API.
	init := []string{"init", "-q", "--ref-format=files"}
	if len(r.CommitOID) == 64 {
		init = append(init, "--object-format=sha256")
	}
	if _, err := b.builderGit(ctx, r, init...); err != nil {
		return fmt.Errorf("initialize builder workspace: %w", err)
	}
	if _, err := b.builderGit(ctx, r, "fetch", "-q", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", builderBundle, "+"+src.Ref+":"+builderBundleRef); err != nil {
		return fmt.Errorf("fetch builder source bundle: %w", err)
	}
	fetched, err := b.builderGit(ctx, r, "rev-parse", "--verify", "--quiet", builderBundleRef+"^{commit}")
	if err != nil || strings.TrimSpace(string(fetched)) != r.CommitOID {
		return errors.Join(err, errors.New("the bundle's ref does not name the build's commit"))
	}
	if _, err := b.builderGit(ctx, r, "checkout", "-q", "--detach", r.CommitOID); err != nil {
		return fmt.Errorf("check out the build's commit: %w", err)
	}
	if _, err := b.builderGit(ctx, r, "update-ref", "-d", builderBundleRef); err != nil {
		return fmt.Errorf("remove the fetched bundle ref: %w", err)
	}
	return b.verifyBuilderClone(ctx, r)
}

// verifyBuilderClone proves the workspace is a clean clone at the build's
// commit, detached, with no ref and no remote.
func (b *Backend) verifyBuilderClone(ctx context.Context, r Builder) error {
	for _, check := range []struct {
		args []string
		want string
	}{
		{[]string{"rev-parse", "--verify", "HEAD"}, r.CommitOID},
		{[]string{"rev-parse", "--symbolic-full-name", "HEAD"}, "HEAD"},
		{[]string{"for-each-ref"}, ""},
		{[]string{"remote"}, ""},
		{[]string{"status", "--porcelain=v1", "--untracked-files=all"}, ""},
	} {
		out, err := b.builderGit(ctx, r, check.args...)
		if err != nil || strings.TrimSpace(string(out)) != check.want {
			return errors.Join(err, fmt.Errorf("builder workspace clone differs from the commit (git %s)", strings.Join(check.args, " ")))
		}
	}
	return nil
}

func createGuestAbsent(ctx context.Context, api fileAPI, path string, f guestFile) error {
	_, exists, err := api.get(ctx, path)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("builder target %s already exists", path)
	}
	if err := api.put(ctx, path, f); err != nil {
		return err
	}
	got, exists, err := getBuilderFile(ctx, api, path, f.typ, int64(len(f.data)))
	if err != nil || !exists || got.typ != f.typ || got.uid != f.uid || got.gid != f.gid || got.mode != f.mode || (f.typ == "file" || f.typ == "symlink") && !bytes.Equal(got.data, f.data) {
		return errors.Join(err, fmt.Errorf("builder target %s postcondition failed", path))
	}
	return nil
}

func getBuilderFile(ctx context.Context, api fileAPI, path, typ string, size int64) (guestFile, bool, error) {
	if typ == "symlink" {
		if bounded, ok := api.(interface {
			getSymlinkBounded(context.Context, string, int64) (guestFile, bool, error)
		}); ok {
			return bounded.getSymlinkBounded(ctx, path, size)
		}
	}
	if bounded, ok := api.(interface {
		getBounded(context.Context, string, int64) (guestFile, bool, error)
	}); ok {
		return bounded.getBounded(ctx, path, size)
	}
	return api.get(ctx, path)
}

// HTTP GET follows symlinks, including links outside the source tree. Read
// their literal targets with SFTP READLINK and bracket that read with HEAD.
func (a *unixFileAPI) getSymlinkBounded(ctx context.Context, path string, maxBytes int64) (guestFile, bool, error) {
	meta, exists, err := a.head(ctx, path, maxBytes)
	if err != nil || !exists || meta.typ != "symlink" {
		return meta, exists, err
	}
	data, err := a.readlinkBounded(ctx, path, maxBytes)
	if err != nil {
		return guestFile{}, false, err
	}
	current, exists, err := a.head(ctx, path, maxBytes)
	if err != nil || !exists || current.typ != "symlink" || current.uid != meta.uid || current.gid != meta.gid || current.mode != meta.mode {
		return guestFile{}, false, errors.New("Incus symlink changed during read")
	}
	meta.data = data
	return meta, true, nil
}
