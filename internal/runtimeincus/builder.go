package runtimeincus

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/lgvo/p.ai/internal/gitservice"
)

// Builder is a core-selected disposable build request, never a Session. Its
// identity is fixed before any Incus mutation and is not selected by source.
type Builder struct {
	RequestUUID          string
	ProjectPath          string
	CommitOID            string
	TreeOID              string
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
	builderSource    = "/opt/p/build/source"
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
	sum := sha256.Sum256([]byte("p.builder/v1;cpu=" + builderCPU + ";memory=" + builderMemory + ";processes=" + builderProcesses + ";root=" + builderRootSize + ";pool=btrfs;" + network + ";no-nesting;no-privilege;source-readonly;" + nix.policy))
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
	if !uuidPattern.MatchString(r.RequestUUID) || !validBuilderProject(r.ProjectPath) || !validBuilderOID(r.CommitOID) || !validBuilderOID(r.TreeOID) || !fingerprintPattern.MatchString(r.BaseImageFingerprint) || len(r.ContractVersion) == 0 || len(r.ContractVersion) > 32 || strings.ContainsAny(r.ContractVersion, "\x00\r\n") {
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

func builderName(r Builder) string { return "p-builder-" + r.RequestUUID }

func builderIdentity(c map[string]string, r Builder) bool {
	return c != nil && c["user.p.builder_request_uuid"] == r.RequestUUID && c["user.p.builder_project_path"] == r.ProjectPath && c["user.p.builder_commit_oid"] == r.CommitOID && c["user.p.builder_tree_oid"] == r.TreeOID && c["user.p.builder_contract_version"] == r.ContractVersion && c["user.p.builder_base_image"] == r.BaseImageFingerprint && c["volatile.base_image"] == r.BaseImageFingerprint
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
	raw, err := b.command(ctx, "list", "^"+builderName(r)+"$", "--format", "json")
	if err != nil {
		return out, installed, err
	}
	var instances []instanceJSON
	if err := decode(raw, &instances); err != nil {
		return out, installed, err
	}
	if len(instances) == 0 {
		return out, installed, nil
	}
	if len(instances) != 1 || instances[0].Name != builderName(r) {
		return out, installed, errors.New("ambiguous builder observation")
	}
	in := instances[0]
	if in.Type != "container" || len(in.Profiles) != 1 || in.Profiles[0] != "default" || !builderIdentity(in.Config, r) || !builderIdentity(in.ExpandedConfig, r) || !safeSecurity(in.Config) || !safeSecurity(in.ExpandedConfig) || in.ExpandedConfig["security.idmap.isolated"] != "true" {
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

// CreateBuilderWithGate records trusted dispatch intent after read-only
// confinement, pool, ownership and pinned-image preflight, before native init.
// Existing owned builders are observed without issuing another init or gate.
func (b *Backend) CreateBuilderWithGate(ctx context.Context, r Builder, beforeInit func() error) (BuilderObservation, error) {
	if beforeInit == nil {
		return BuilderObservation{}, errors.New("builder init gate unavailable")
	}
	return b.createBuilder(ctx, r, beforeInit)
}
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
		_, createErr := b.command(ctx, "init", r.BaseImageFingerprint, builderName(r), "--profile", "default", "--storage", b.config.BuilderStoragePool, "--device", "root,size="+builderRootSize, "--config", "security.idmap.isolated=true", "--config", "security.privileged=false", "--config", "security.nesting=false", "--config", "limits.cpu="+builderCPU, "--config", "limits.memory="+builderMemory, "--config", "limits.processes="+builderProcesses, "--config", "user.p.builder_request_uuid="+r.RequestUUID, "--config", "user.p.builder_project_path="+r.ProjectPath, "--config", "user.p.builder_commit_oid="+r.CommitOID, "--config", "user.p.builder_tree_oid="+r.TreeOID, "--config", "user.p.builder_contract_version="+r.ContractVersion, "--config", "user.p.builder_base_image="+r.BaseImageFingerprint)
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
	api := &unixFileAPI{socket: b.config.UserSocket, project: b.config.Project, instance: builderName(r)}
	source, exists, err := api.get(ctx, builderSource)
	if err != nil || !exists || source.typ != "directory" || source.uid != 0 || source.gid != 0 || source.mode != 0555 {
		return before, errors.Join(err, errors.New("builder source is not sealed"))
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

// waitBuilderBoot waits until early boot has created the image's fixed
// runtime paths. The Nix daemon socket listens only after sysinit.target,
// which follows systemd-tmpfiles setup.
func (b *Backend) waitBuilderBoot(ctx context.Context, r Builder) error {
	ready, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := b.builderGuestExec(ready, r, "/bin/sh", "-c", "test -S "+builderNixSocket+" && test -x "+builderRuntimeKit); err == nil {
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
	if inspectErr != nil || !after.Exists || after.Status != want {
		return after, errors.Join(err, inspectErr, errors.New("builder state postcondition failed"))
	}
	return after, nil
}

// TransferBuilderSource copies a sealed committed snapshot via the confined
// Incus file API. The source path is never mounted or supplied to a plugin.
// A transfer may be retried by cleaning the verified builder and creating it
// again; existing source entries are never overwritten or adopted.
func (b *Backend) TransferBuilderSource(ctx context.Context, r Builder, s *gitservice.SourceSnapshot) error {
	if s == nil || s.Project() != r.ProjectPath || s.CommitOID() != r.CommitOID || s.TreeOID() != r.TreeOID || s.Entries() > gitservice.SnapshotMaxEntries || s.Bytes() > gitservice.SnapshotMaxSourceBytes {
		return errors.New("source snapshot differs from builder request")
	}
	if err := b.CheckConfinement(ctx); err != nil {
		return err
	}
	o, err := b.InspectBuilder(ctx, r)
	if err != nil || !o.Exists || o.Status != "Stopped" || !o.Ready {
		return errors.Join(err, errors.New("source transfer requires owned stopped builder"))
	}
	api := &builderCheckedFileAPI{backend: b, request: r, inner: &unixFileAPI{socket: b.config.UserSocket, project: b.config.Project, instance: builderName(r)}}
	return transferBuilderSource(ctx, api, s)
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
		return errors.New("builder changed during source transfer")
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
func (a *builderCheckedFileAPI) put(ctx context.Context, path string, f guestFile) error {
	if err := a.check(ctx); err != nil {
		return err
	}
	return a.inner.put(ctx, path, f)
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

type builderSourceSnapshot interface {
	Path() string
	Entries() int
	Bytes() int64
}

func transferBuilderSource(ctx context.Context, api fileAPI, s builderSourceSnapshot) error {
	root := s.Path()
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0500 || !ownedByCurrentUser(info) {
		return errors.New("snapshot root is not sealed and daemon-owned")
	}
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
	if err := createGuestAbsent(ctx, api, "/opt/p/build", guestFile{typ: "directory", uid: 0, gid: 0, mode: 0755}); err != nil {
		return err
	}
	if err := createGuestAbsent(ctx, api, builderSource, guestFile{typ: "directory", uid: 0, gid: 0, mode: 0700}); err != nil {
		return err
	}
	var dirs []string
	links := map[string]string{}
	var entries int
	var total int64
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || !safeBuilderRelative(rel) {
			return errors.New("unsafe snapshot path")
		}
		entries++
		if entries > gitservice.SnapshotMaxEntries {
			return errors.New("snapshot entry limit exceeded")
		}
		info, err := os.Lstat(path)
		if err != nil || !ownedByCurrentUser(info) {
			return errors.New("snapshot entry changed owner")
		}
		target := builderSource + "/" + filepath.ToSlash(rel)
		var f guestFile
		switch {
		case info.IsDir() && info.Mode().Perm() == 0500:
			f = guestFile{typ: "directory", uid: 0, gid: 0, mode: 0700}
			dirs = append(dirs, target)
		case info.Mode().IsRegular() && (info.Mode().Perm() == 0400 || info.Mode().Perm() == 0500):
			if info.Size() < 0 || info.Size() > gitservice.SnapshotMaxSourceBytes-total {
				return errors.New("snapshot bytes exceed limit")
			}
			if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Nlink != 1 {
				return errors.New("snapshot file has unexpected links")
			}
			data, err := readSealedFile(path, info, info.Size())
			if err != nil {
				return err
			}
			total += int64(len(data))
			mode := 0444
			if info.Mode().Perm()&0100 != 0 {
				mode = 0555
			}
			f = guestFile{typ: "file", uid: 0, gid: 0, mode: mode, data: data}
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil || !safeBuilderLink(rel, link) || len(link) > int(gitservice.SnapshotMaxSymlinkBytes) {
				return errors.New("unsafe snapshot symlink")
			}
			total += int64(len(link))
			if total > gitservice.SnapshotMaxSourceBytes {
				return errors.New("snapshot bytes exceed limit")
			}
			f = guestFile{typ: "symlink", uid: 0, gid: 0, mode: 0777, data: []byte(link)}
			links[filepath.ToSlash(rel)] = link
		default:
			return errors.New("unsupported snapshot entry")
		}
		return createGuestAbsent(ctx, api, target, f)
	})
	if err != nil {
		return err
	}
	if entries != s.Entries() || total != s.Bytes() {
		return errors.New("snapshot metadata changed during transfer")
	}
	if err := validateBuilderLinks(ctx, links); err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := sealGuestDirectory(ctx, api, dirs[i]); err != nil {
			return err
		}
	}
	return sealGuestDirectory(ctx, api, builderSource)
}

func createGuestAbsent(ctx context.Context, api fileAPI, path string, f guestFile) error {
	_, exists, err := api.get(ctx, path)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("builder source target %s already exists", path)
	}
	if err := api.put(ctx, path, f); err != nil {
		return err
	}
	got, exists, err := getBuilderFile(ctx, api, path, f.typ, int64(len(f.data)))
	if err != nil || !exists || got.typ != f.typ || got.uid != f.uid || got.gid != f.gid || got.mode != f.mode || (f.typ == "file" || f.typ == "symlink") && !bytes.Equal(got.data, f.data) {
		return errors.Join(err, fmt.Errorf("builder source target %s postcondition failed", path))
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

func validateBuilderLinks(ctx context.Context, links map[string]string) error {
	steps := 0
	for path, target := range links {
		current := make([]string, 0, 8)
		queue := append(strings.Split(filepath.Dir(path), "/"), strings.Split(target, "/")...)
		expansions := 0
		for len(queue) > 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			part := queue[0]
			queue = queue[1:]
			steps++
			if steps > 1<<20 {
				return errors.New("snapshot symlink resolution exceeds limit")
			}
			switch part {
			case "", ".":
				continue
			case "..":
				if len(current) == 0 {
					return fmt.Errorf("snapshot symlink %q escapes source", path)
				}
				current = current[:len(current)-1]
			default:
				candidate := strings.Join(append(current, part), "/")
				if next, ok := links[candidate]; ok {
					expansions++
					if expansions > 40 {
						return fmt.Errorf("snapshot symlink %q has cycle", path)
					}
					queue = append(strings.Split(next, "/"), queue...)
				} else {
					current = append(current, part)
				}
			}
		}
	}
	return nil
}

func sealGuestDirectory(ctx context.Context, api fileAPI, path string) error {
	f, exists, err := api.get(ctx, path)
	if err != nil || !exists || f.typ != "directory" || f.uid != 0 || f.gid != 0 || f.mode != 0700 {
		return errors.Join(err, errors.New("builder source directory changed before sealing"))
	}
	sealer, ok := api.(interface {
		chmodDirectory(context.Context, string, int) error
	})
	if !ok {
		return errors.New("builder file API cannot seal directories")
	}
	if err := sealer.chmodDirectory(ctx, path, 0555); err != nil {
		return err
	}
	f, exists, err = api.get(ctx, path)
	if err != nil || !exists || f.typ != "directory" || f.uid != 0 || f.gid != 0 || f.mode != 0555 {
		return errors.Join(err, errors.New("builder source directory seal failed"))
	}
	return nil
}

func safeBuilderRelative(path string) bool {
	if path == "" || path == "." || filepath.IsAbs(path) || len(path) > gitservice.SnapshotMaxPathBytes || !utf8.ValidString(path) {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return false
		}
		for _, c := range part {
			if unicode.IsControl(c) || c == '\\' {
				return false
			}
		}
	}
	return true
}

func safeBuilderLink(path, target string) bool {
	if target == "" || filepath.IsAbs(target) || !utf8.ValidString(target) || strings.ContainsRune(target, '\\') {
		return false
	}
	for _, c := range target {
		if unicode.IsControl(c) {
			return false
		}
	}
	clean := filepath.Clean(filepath.Join(filepath.Dir(path), target))
	return clean != ".." && !strings.HasPrefix(clean, "../") && !filepath.IsAbs(clean)
}

func readSealedFile(path string, before os.FileInfo, size int64) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) || after.Size() != size {
		return nil, errors.New("snapshot file changed during open")
	}
	data, err := io.ReadAll(io.LimitReader(f, size+1))
	if err != nil || int64(len(data)) != size {
		return nil, errors.New("snapshot file changed during read")
	}
	return data, nil
}
