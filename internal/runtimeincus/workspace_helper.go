package runtimeincus

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const workspaceHelperContract = "p.workspace-helper/v1"
const inertWorkspaceTarget = "[Unit]\nDescription=P isolated workspace helper (inert)\n"

var inertStoreUnitTarget = regexp.MustCompile(`^/nix/store/[a-z0-9]{32}-p-host-unit-links/etc/systemd/system/p-session\.target$`)

const (
	workspaceHelperCPU       = "1"
	workspaceHelperMemory    = "768MiB"
	workspaceHelperProcesses = "256"
)

// WorkspaceHelper is a disposable base-image instance. The operation UUID is
// persisted before init, so an uncertain init can only be reconciled by this
// exact name and these exact labels. It is never a public session.
func WorkspaceHelper(instanceUUID, operationUUID, ownerUUID, project string, baseFingerprint string) Session {
	return Session{InstanceUUID: instanceUUID, SessionUUID: operationUUID, WorkspaceOwner: ownerUUID,
		ProjectPath: project, ContractVersion: workspaceHelperContract, ImageFingerprint: baseFingerprint}
}

func (b *Backend) InspectWorkspaceHelper(ctx context.Context, helper Session) (Observation, error) {
	if helper.WorkspaceOwner == "" || helper.ContractVersion != workspaceHelperContract {
		return Observation{}, errors.New("invalid workspace helper request")
	}
	o, err := b.Inspect(ctx, helper)
	if err != nil || !o.Exists {
		return o, err
	}
	if o.EndpointMounted {
		return Observation{}, errors.New("workspace helper has an endpoint")
	}
	raw, err := b.command(ctx, "list", "^"+b.name(helper)+"$", "--format", "json")
	if err != nil {
		return Observation{}, err
	}
	var instances []instanceJSON
	if err := decode(raw, &instances); err != nil || len(instances) != 1 {
		return Observation{}, errors.Join(err, errors.New("workspace helper observation ambiguous"))
	}
	if instances[0].Name != o.Name || instances[0].Status != o.Status || !validIdentity(instances[0].Config, helper) || !validIdentity(instances[0].ExpandedConfig, helper) {
		return Observation{}, errors.New("workspace helper changed during inspection")
	}
	for _, config := range []map[string]string{instances[0].Config, instances[0].ExpandedConfig} {
		if config["limits.cpu"] != workspaceHelperCPU || config["limits.memory"] != workspaceHelperMemory || config["limits.processes"] != workspaceHelperProcesses {
			return Observation{}, errors.New("workspace helper resource limits changed")
		}
		for key := range config {
			if strings.HasPrefix(key, "environment.") || strings.HasPrefix(key, "user.p.") && key != "user.p.instance_uuid" && key != "user.p.session_uuid" && key != "user.p.workspace_owner" && key != "user.p.project_path" && key != "user.p.contract_version" && key != "user.p.image_fingerprint" {
				return Observation{}, errors.New("workspace helper inherited unsafe configuration")
			}
		}
	}
	return o, nil
}

// Capacity is checked before a workspace operation is accepted. A later
// Incus admission failure still leaves the durable helper intent visible;
// it never causes the source runtime to be frozen first.
func (b *Backend) CheckWorkspaceHelperCapacity(ctx context.Context) error {
	if err := b.CheckConfinement(ctx); err != nil {
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
	limit := 0
	for _, project := range projects {
		if project.Name != b.config.Project {
			continue
		}
		if limit != 0 {
			return errors.New("workspace helper project ambiguous")
		}
		limit, err = strconv.Atoi(project.Config["limits.containers"])
		if err != nil || limit < 1 || limit > 64 {
			return errors.New("workspace helper capacity unavailable")
		}
	}
	if limit == 0 {
		return errors.New("workspace helper project unavailable")
	}
	raw, err = b.command(ctx, "list", "--format", "json")
	if err != nil {
		return err
	}
	var instances []instanceJSON
	if err := decode(raw, &instances); err != nil {
		return err
	}
	if len(instances) >= limit {
		return errors.New("workspace helper container capacity unavailable")
	}
	return nil
}

func (b *Backend) workspaceResourceBusy(ctx context.Context, instance string) (bool, error) {
	raw, err := b.command(ctx, "operation", "list", "--format", "json")
	if err != nil {
		return false, err
	}
	var operations []struct {
		StatusCode int                 `json:"status_code"`
		Resources  map[string][]string `json:"resources"`
	}
	if err := decode(raw, &operations); err != nil {
		return false, err
	}
	if len(operations) > 256 {
		return false, errors.New("Incus operation inventory exceeds bound")
	}
	want := "/1.0/instances/" + instance
	for _, op := range operations {
		if op.StatusCode < 100 || op.StatusCode > 401 {
			return false, errors.New("Incus operation status invalid")
		}
		for _, resource := range op.Resources["instances"] {
			parsed, err := url.Parse(resource)
			if err != nil || parsed.Path == "" || parsed.RawQuery != "" && parsed.Query().Get("project") != b.config.Project {
				return false, errors.New("Incus operation resource malformed")
			}
			if parsed.Path == want && op.StatusCode < 200 {
				return true, nil
			}
		}
	}
	return false, nil
}

// Two project-scoped observations may accept only a positive exact helper.
// A canceled request can be queued before Incus registers an operation, so
// repeated absence is still unknown and never authorizes another init.
func (b *Backend) ReconcileWorkspaceHelperInit(ctx context.Context, helper Session) (Observation, error) {
	var previous Observation
	for i := 0; i < 2; i++ {
		busy, err := b.workspaceResourceBusy(ctx, b.name(helper))
		if err != nil || busy {
			return Observation{}, errors.Join(err, errors.New("workspace helper init still active or ambiguous"))
		}
		current, err := b.InspectWorkspaceHelper(ctx, helper)
		if err != nil {
			return Observation{}, err
		}
		if i > 0 && (current.Exists != previous.Exists || current.Status != previous.Status) {
			return Observation{}, errors.New("workspace helper changed during reconciliation")
		}
		previous = current
		if i == 0 {
			select {
			case <-ctx.Done():
				return Observation{}, ctx.Err()
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	if !previous.Exists {
		return Observation{}, errors.New("workspace helper init outcome unknown; exact helper absent but delayed Incus admission cannot be excluded")
	}
	return previous, nil
}

func (b *Backend) ReconcileWorkspaceFreeze(ctx context.Context, source Session) error {
	return b.reconcileWorkspaceFreeze(ctx, source, "", "")
}

func (b *Backend) ReconcileWorkspaceFreezeExact(ctx context.Context, source Session, incusUUID, generation string) error {
	if !uuidPattern.MatchString(incusUUID) || !uuidPattern.MatchString(generation) {
		return errors.New("workspace source generation unavailable")
	}
	return b.reconcileWorkspaceFreeze(ctx, source, incusUUID, generation)
}

func (b *Backend) reconcileWorkspaceFreeze(ctx context.Context, source Session, incusUUID, generation string) error {
	for i := 0; i < 2; i++ {
		busy, err := b.workspaceResourceBusy(ctx, b.name(source))
		if err != nil || busy {
			return errors.Join(err, errors.New("workspace freeze operation still active or ambiguous"))
		}
		observed, err := b.Inspect(ctx, source)
		if err != nil || !observed.Exists || observed.Status != "Frozen" {
			return errors.Join(err, errors.New("workspace freeze outcome unknown; Running state does not exclude delayed Incus admission"))
		}
		if generation != "" && (observed.IncusUUID != incusUUID || observed.Generation != generation) {
			return errors.New("workspace source generation changed during freeze reconciliation")
		}
		if i == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	return nil
}

func (b *Backend) CreateWorkspaceHelper(ctx context.Context, helper Session, beforeInit func() error) (Observation, error) {
	if helper.WorkspaceOwner == "" || helper.ContractVersion != workspaceHelperContract {
		return Observation{}, errors.New("invalid workspace helper request")
	}
	_, err := b.create(ctx, helper, beforeInit)
	if err != nil {
		return Observation{}, err
	}
	observed, err := b.InspectWorkspaceHelper(ctx, helper)
	if err != nil || !observed.Exists || observed.Status != "Stopped" {
		return Observation{}, errors.Join(err, errors.New("workspace helper creation postcondition failed"))
	}
	api := &unixFileAPI{socket: b.config.UserSocket, project: b.config.Project, instance: b.name(helper)}
	if err = prepareInertWorkspaceHelper(ctx, api); err != nil {
		return Observation{}, err
	}
	return b.InspectWorkspaceHelper(ctx, helper)
}

func prepareInertWorkspaceHelper(ctx context.Context, api *unixFileAPI) error {
	root, err := api.lstat(ctx, "/")
	if err != nil || root.typ != "directory" || root.uid != 0 || root.gid != 0 || root.mode != 0755 {
		return errors.Join(err, errors.New("workspace helper root differs from base"))
	}
	for _, path := range []string{"/etc", "/etc/p", "/etc/p/assets"} {
		f, exists, err := api.lstatOptional(ctx, path)
		if err != nil || exists && (f.typ != "directory" || f.uid != 0 || f.gid != 0 || f.mode&022 != 0) {
			return errors.Join(err, fmt.Errorf("unsafe helper ancestor %s", path))
		}
	}
	for _, path := range []string{"/etc/p/session.json", "/etc/p/workspace.json", "/etc/p/assets/p-interactive.service", "/etc/p/git", "/etc/p/devshell", "/home/p/.ssh"} {
		_, exists, err := api.lstatOptional(ctx, path)
		if err != nil || exists {
			return errors.Join(err, fmt.Errorf("helper contains session state %s", path))
		}
	}
	if _, exists, err := api.lstatOptional(ctx, "/opt/p/endpoints"); err != nil || exists {
		return errors.Join(err, errors.New("helper contains endpoint staging"))
	}
	// An interrupted helper setup may leave only the exact inert asset. The
	// base image must not contribute any other mutable P asset or credential.
	for _, inventory := range []struct {
		dir     string
		allowed map[string]bool
	}{
		{"/etc/p", map[string]bool{"assets": true}},
		{"/etc/p/assets", map[string]bool{"p-session.target": true}},
	} {
		if _, exists, err := api.lstatOptional(ctx, inventory.dir); err != nil {
			return err
		} else if exists {
			names, err := api.listDirectory(ctx, inventory.dir, len(inventory.allowed))
			if err != nil {
				return err
			}
			for _, name := range names {
				if !inventory.allowed[name] {
					return errors.New("helper contains unexpected P asset")
				}
			}
		}
	}
	if err := ensureGuestEtc(ctx, api); err != nil {
		return err
	}
	for _, path := range []string{"/etc/p", "/etc/p/assets"} {
		if err := ensureGuestFile(ctx, api, path, guestFile{typ: "directory", uid: 0, gid: 0, mode: 0755}); err != nil {
			return err
		}
		f, err := api.lstat(ctx, path)
		if err != nil || f.typ != "directory" || f.uid != 0 || f.gid != 0 || f.mode != 0755 {
			return errors.Join(err, errors.New("helper ancestor changed during install"))
		}
	}
	path := "/etc/p/assets/p-session.target"
	if f, exists, err := api.lstatOptional(ctx, path); err != nil || exists && (f.typ != "file" || f.uid != 0 || f.gid != 0 || f.mode != 0644) {
		return errors.Join(err, errors.New("helper target path unsafe"))
	}
	if err := ensureGuestFile(ctx, api, path, guestFile{typ: "file", uid: 0, gid: 0, mode: 0644, data: []byte(inertWorkspaceTarget)}); err != nil {
		return err
	}
	f, err := api.lstat(ctx, path)
	if err != nil || f.typ != "file" || f.uid != 0 || f.gid != 0 || f.mode != 0644 {
		return errors.Join(err, errors.New("helper target changed during install"))
	}
	return nil
}

func (b *Backend) StartWorkspaceHelper(ctx context.Context, helper Session) error {
	before, err := b.InspectWorkspaceHelper(ctx, helper)
	if err != nil || !before.Exists || before.Status != "Stopped" {
		return errors.Join(err, errors.New("workspace helper must be stopped"))
	}
	api := &unixFileAPI{socket: b.config.UserSocket, project: b.config.Project, instance: b.name(helper)}
	if err := prepareInertWorkspaceHelper(ctx, api); err != nil {
		return err
	}
	if _, err := b.Start(ctx, helper); err != nil {
		return err
	}
	return waitWorkspaceHelperBoot(ctx, func(probeCtx context.Context) error {
		return b.verifyWorkspaceHelperBoot(probeCtx, helper)
	})
}

func waitWorkspaceHelperBoot(ctx context.Context, probe func(context.Context) error) error {
	readyCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var lastFailure error
	for {
		if err := probe(readyCtx); err == nil {
			return nil
		} else if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			lastFailure = err
		}
		if readyCtx.Err() != nil {
			return fmt.Errorf("workspace helper boot readiness unresolved: %w", errors.Join(lastFailure, readyCtx.Err()))
		}
		select {
		case <-readyCtx.Done():
			return fmt.Errorf("workspace helper boot readiness unresolved: %w", errors.Join(lastFailure, readyCtx.Err()))
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (b *Backend) verifyWorkspaceHelperBoot(ctx context.Context, helper Session) error {
	o, err := b.InspectWorkspaceHelper(ctx, helper)
	if err != nil || !o.Exists || o.Status != "Running" {
		return errors.Join(err, errors.New("workspace helper boot not verified"))
	}
	api := &unixFileAPI{socket: b.config.UserSocket, project: b.config.Project, instance: b.name(helper)}
	for _, path := range []string{"/", "/etc", "/etc/p", "/etc/p/assets"} {
		f, err := api.lstat(ctx, path)
		if err != nil || f.typ != "directory" || f.uid != 0 || f.gid != 0 || f.mode&022 != 0 {
			return errors.Join(err, fmt.Errorf("workspace helper boot ancestor unsafe: %s", path))
		}
	}
	for _, path := range []string{"/etc/p/session.json", "/etc/p/workspace.json", "/etc/p/assets/p-interactive.service", "/etc/p/git", "/etc/p/devshell", "/opt/p/endpoints", "/run/p-interactive/tmux.sock"} {
		_, exists, err := api.lstatOptional(ctx, path)
		if err != nil || exists {
			return errors.Join(err, fmt.Errorf("workspace helper acquired session state %s", path))
		}
	}
	meta, err := api.lstat(ctx, "/etc/p/assets/p-session.target")
	if err != nil || meta.typ != "file" || meta.uid != 0 || meta.gid != 0 || meta.mode != 0644 {
		return errors.Join(err, errors.New("workspace helper inert target metadata unsafe"))
	}
	target, exists, err := api.getBounded(ctx, "/etc/p/assets/p-session.target", 4096)
	if err != nil || !exists || target.typ != "file" || target.uid != 0 || target.gid != 0 || target.mode != 0644 || !bytes.Equal(target.data, []byte(inertWorkspaceTarget)) {
		return errors.Join(err, errors.New("workspace helper inert target changed"))
	}
	// The generated unit link appears only after first boot. Require it to
	// resolve to the fixed inert asset before any workspace bytes are copied.
	if err := verifyInertWorkspaceTargetLinks(ctx, api); err != nil {
		return err
	}
	unit, err := b.command(ctx, "exec", b.name(helper), "--", "/run/current-system/sw/bin/systemctl", "show", "p-session.target", "--no-pager", "-p", "LoadState", "-p", "ActiveState", "-p", "Wants", "-p", "Requires", "-p", "DropInPaths")
	if err != nil {
		return fmt.Errorf("helper p-session target query: %w", err)
	}
	props, err := parseHelperUnit(unit, "LoadState", "ActiveState", "Wants", "Requires", "DropInPaths")
	if err != nil || props["LoadState"] != "loaded" || props["ActiveState"] != "active" || props["DropInPaths"] != "" || containsPUnit(props["Wants"]) || containsPUnit(props["Requires"]) {
		return errors.Join(err, errors.New("workspace helper systemd dependencies unsafe"))
	}
	other, err := b.command(ctx, "exec", b.name(helper), "--", "/run/current-system/sw/bin/systemctl", "show", "p-interactive.service", "--no-pager", "-p", "LoadState", "-p", "ActiveState")
	if err != nil {
		return fmt.Errorf("helper p-interactive unit query: %w", err)
	}
	state, err := parseHelperUnit(other, "LoadState", "ActiveState")
	if err != nil || state["LoadState"] != "not-found" || state["ActiveState"] != "inactive" {
		return errors.Join(err, errors.New("workspace helper interactive unit available or active"))
	}
	return nil
}

type workspaceLinkReader interface {
	lstat(context.Context, string) (guestFile, error)
	readlinkBounded(context.Context, string, int64) ([]byte, error)
}

// The pinned NixOS image has a generated system-units link to a store unit
// package, whose own link points at the installed inert asset. The store item
// and every directory below it must be immutable to UID 1000. Do not resolve
// an arbitrary chain, or mistake the first store link for the final asset.
func verifyInertWorkspaceTargetLinks(ctx context.Context, files workspaceLinkReader) error {
	const first = "/etc/systemd/system/p-session.target"
	link, err := files.lstat(ctx, first)
	if err != nil || link.typ != "symlink" || link.uid == 1000 || link.gid == 1000 {
		return errors.Join(err, errors.New("helper generated target link unsafe"))
	}
	dest, err := files.readlinkBounded(ctx, first, 256)
	target := string(dest)
	if err != nil || !inertStoreUnitTarget.MatchString(target) || path.Clean(target) != target {
		return errors.Join(err, errors.New("helper generated target does not name pinned store unit layout"))
	}
	for _, ancestor := range []struct {
		path string
		mode int
	}{{"/nix", 0755}, {"/nix/store", 01775}} {
		meta, err := files.lstat(ctx, ancestor.path)
		if err != nil || meta.typ != "directory" || meta.uid == 1000 || meta.gid == 1000 || meta.mode != ancestor.mode {
			return errors.Join(err, fmt.Errorf("helper store ancestor unsafe: %s", ancestor.path))
		}
	}
	storeItem := strings.TrimSuffix(target, "/etc/systemd/system/p-session.target")
	for _, directory := range []string{storeItem, storeItem + "/etc", storeItem + "/etc/systemd", storeItem + "/etc/systemd/system"} {
		meta, err := files.lstat(ctx, directory)
		if err != nil || meta.typ != "directory" || meta.uid == 1000 || meta.gid == 1000 || meta.mode != 0555 {
			return errors.Join(err, fmt.Errorf("helper store unit directory unsafe: %s", directory))
		}
	}
	link, err = files.lstat(ctx, target)
	if err != nil || link.typ != "symlink" || link.uid == 1000 || link.gid == 1000 {
		return errors.Join(err, errors.New("helper store unit link unsafe"))
	}
	final, err := files.readlinkBounded(ctx, target, 256)
	if err != nil || string(final) != "/etc/p/assets/p-session.target" {
		return errors.Join(err, errors.New("helper store unit does not end at inert asset"))
	}
	return nil
}

func (b *Backend) VerifyWorkspaceHelperBoot(ctx context.Context, helper Session) error {
	return b.verifyWorkspaceHelperBoot(ctx, helper)
}

func containsPUnit(value string) bool {
	for _, field := range strings.Fields(value) {
		if strings.HasPrefix(field, "p-") {
			return true
		}
	}
	return false
}

func parseHelperUnit(raw []byte, required ...string) (map[string]string, error) {
	if len(raw) > 4096 {
		return nil, errors.New("helper unit output exceeded bound")
	}
	result := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" || strings.ContainsAny(key, " \r\t") || strings.ContainsAny(value, "\x00\r\n") || len(value) > 1024 {
			return nil, errors.New("helper unit output malformed")
		}
		if _, exists := result[key]; exists {
			return nil, errors.New("helper unit output repeated field")
		}
		result[key] = value
	}
	if len(result) != len(required) {
		return nil, errors.New("helper unit output field count changed")
	}
	for _, field := range required {
		if _, exists := result[field]; !exists {
			return nil, errors.New("helper unit output missing field")
		}
	}
	return result, nil
}

func (b *Backend) DeleteWorkspaceHelper(ctx context.Context, helper Session) error {
	o, err := b.InspectWorkspaceHelper(ctx, helper)
	if err != nil || !o.Exists {
		return err
	}
	if o.Status == "Running" {
		if _, err := b.Stop(ctx, helper); err != nil {
			return err
		}
	} else if o.Status != "Stopped" {
		return errors.New("workspace helper deletion state unavailable")
	}
	if _, err = b.Delete(ctx, helper); err != nil {
		return err
	}
	o, err = b.InspectWorkspaceHelper(ctx, helper)
	if err != nil || o.Exists {
		return errors.Join(err, errors.New("workspace helper deletion uncertain"))
	}
	return nil
}

func (b *Backend) FreezeWorkspaceSource(ctx context.Context, source Session, beforePause func() error) error {
	return b.freezeWorkspaceSource(ctx, source, "", "", beforePause)
}

func (b *Backend) FreezeWorkspaceSourceExact(ctx context.Context, source Session, incusUUID, generation string, beforePause func() error) error {
	if !uuidPattern.MatchString(incusUUID) || !uuidPattern.MatchString(generation) {
		return errors.New("workspace source generation unavailable")
	}
	return b.freezeWorkspaceSource(ctx, source, incusUUID, generation, beforePause)
}

func (b *Backend) freezeWorkspaceSource(ctx context.Context, source Session, incusUUID, generation string, beforePause func() error) error {
	if source.WorkspaceOwner != "" {
		return errors.New("helper cannot be frozen as a source")
	}
	if err := b.CheckConfinement(ctx); err != nil {
		return err
	}
	before, err := b.Inspect(ctx, source)
	if err != nil || !before.Exists || before.Status != "Running" {
		return errors.Join(err, errors.New("workspace source cannot be frozen"))
	}
	if generation != "" && (before.IncusUUID != incusUUID || before.Generation != generation) {
		return errors.New("workspace source generation changed before freeze")
	}
	// Attachment tokens are process-local. A daemon restart can forget an
	// already admitted Incus exec, so the project-scoped operation inventory
	// must show no active operation on this exact source before freezing it.
	busy, err := b.workspaceResourceBusy(ctx, b.name(source))
	if err != nil || busy {
		return errors.Join(err, errors.New("workspace source has an active or ambiguous Incus operation"))
	}
	if beforePause == nil {
		return errors.New("workspace freeze intent missing")
	}
	if err := beforePause(); err != nil {
		return err
	}
	_, mutationErr := b.command(ctx, "pause", b.name(source))
	after, inspectErr := b.Inspect(ctx, source)
	if mutationErr != nil || inspectErr != nil || !after.Exists || after.Status != "Frozen" {
		return errors.Join(mutationErr, inspectErr, errors.New("workspace freeze outcome unresolved"))
	}
	if generation != "" && (after.IncusUUID != incusUUID || after.Generation != generation) {
		return errors.New("workspace source generation changed during freeze")
	}
	return nil
}

func (b *Backend) ThawWorkspaceSource(ctx context.Context, source Session) error {
	return b.thawWorkspaceSource(ctx, source, "", "")
}

func (b *Backend) ThawWorkspaceSourceExact(ctx context.Context, source Session, incusUUID, generation string) error {
	if !uuidPattern.MatchString(incusUUID) || !uuidPattern.MatchString(generation) {
		return errors.New("workspace source generation unavailable")
	}
	return b.thawWorkspaceSource(ctx, source, incusUUID, generation)
}

func (b *Backend) thawWorkspaceSource(ctx context.Context, source Session, incusUUID, generation string) error {
	if source.WorkspaceOwner != "" {
		return errors.New("helper cannot be thawed as a source")
	}
	if err := b.CheckConfinement(ctx); err != nil {
		return err
	}
	before, err := b.Inspect(ctx, source)
	if err != nil || !before.Exists || before.Status != "Frozen" && before.Status != "Running" {
		return errors.Join(err, errors.New("workspace source cannot be thawed"))
	}
	if generation != "" && (before.IncusUUID != incusUUID || before.Generation != generation) {
		return errors.New("workspace source generation changed before thaw")
	}
	if before.Status == "Running" {
		return nil
	}
	_, mutationErr := b.command(ctx, "resume", b.name(source))
	after, inspectErr := b.Inspect(ctx, source)
	if inspectErr != nil || !after.Exists || after.Status != "Running" {
		return errors.Join(mutationErr, inspectErr, errors.New("workspace thaw outcome unresolved"))
	}
	if generation != "" && (after.IncusUUID != incusUUID || after.Generation != generation) {
		return errors.New("workspace source generation changed during thaw")
	}
	return nil
}
