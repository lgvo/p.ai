package runtimeincus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/lgvo/p.ai/internal/nixenv"
)

const (
	builderGCPath         = "/nix/var/nix/gcroots/p-devshell"
	builderActivationPath = "/etc/p/devshell/activate.sh"
	imageContract         = "p.incus-system-image/v2"
	builderImageFormatKey = imageContract + ";compression=none"
)

var profileGeneration = regexp.MustCompile(`^\.p-devshell-capture-[1-9][0-9]{0,6}-link$`)

type builderImageFiles interface {
	fileAPI
	getBounded(context.Context, string, int64) (guestFile, bool, error)
	getSymlinkBounded(context.Context, string, int64) (guestFile, bool, error)
	fullDirectoryMode(context.Context, string) (guestFile, error)
	chmodDirectory(context.Context, string, int) error
	deleteTree(context.Context, string) error
}

// BuilderImageInfo records the private Incus object verified after publish.
// The fingerprint remains the only image locator; properties are assertions,
// not an alias or a cache index.
type BuilderImageInfo struct {
	Fingerprint           string
	Project               string
	Size                  int64
	Properties            map[string]string
	BuilderCleanupPending bool
}

func validateBuilderImageResult(r Builder, result BuilderNixResult) error {
	s := result.Selection
	if s.Builder != r || s.BaseOnly || s.System == "" || !validBuilderSystem(s.System) ||
		s.KeyDigest == "" || s.KeyInputs.System != s.System || s.KeyInputs.ProjectPath != r.ProjectPath ||
		s.KeyInputs.BaseFingerprint != r.BaseImageFingerprint || s.KeyInputs.ImageFormatVersion != builderImageFormatKey ||
		s.KeyInputs.DerivationPath != s.DerivationPath || s.KeyInputs.CommittedInputsDigest != s.CommittedInputsDigest ||
		s.DerivationPath == "" || !builderDrvPattern.MatchString(s.DerivationPath) ||
		!builderStorePathPattern.MatchString(result.CaptureStorePath) || !strings.HasSuffix(result.CaptureStorePath, "-env") {
		return errors.New("invalid accepted builder image result")
	}
	key, err := s.KeyInputs.Digest()
	if err != nil || key != s.KeyDigest {
		return errors.New("builder image key changed")
	}
	digest, err := result.Material.Digest()
	if err != nil || digest != result.MaterialDigest {
		return errors.New("builder image material changed")
	}
	return nil
}

// PublishBuilderImage consumes only a previously accepted native result from
// the WASI pipeline. It never evaluates project code on the host or accepts a
// plugin path, Incus name, label, or command. Failure does not return a handle.
func (b *Backend) PublishBuilderImage(ctx context.Context, r Builder, result BuilderNixResult) (nixenv.Handle, BuilderImageInfo, error) {
	return b.publishBuilderImage(ctx, r, result, nil)
}

// PublishBuilderImageWithGate invokes gate after all reversible assembly,
// smoke, scrub and pre-publish checks, immediately before the Incus publish
// command. Callers durably record the exact claim there. A failed gate issues
// no publish command.
func (b *Backend) PublishBuilderImageWithGate(ctx context.Context, r Builder, result BuilderNixResult, gate func(BuilderImageClaim) error) (nixenv.Handle, BuilderImageInfo, error) {
	if gate == nil {
		return nixenv.Handle{}, BuilderImageInfo{}, errors.New("durable publication gate required")
	}
	return b.publishBuilderImage(ctx, r, result, gate)
}

func (b *Backend) publishBuilderImage(ctx context.Context, r Builder, result BuilderNixResult, gate func(BuilderImageClaim) error) (nixenv.Handle, BuilderImageInfo, error) {
	var empty nixenv.Handle
	var noImage BuilderImageInfo
	if err := validateBuilderImageResult(r, result); err != nil {
		return empty, noImage, err
	}
	if !uuidPattern.MatchString(b.config.PInstanceID) {
		return empty, noImage, errors.New("trusted P instance identity required for image publication")
	}
	if err := b.checkBuilderRunning(ctx, r); err != nil {
		return empty, noImage, err
	}
	current, err := b.ResolveBuilderNix(ctx, r, result.Selection.System)
	if err != nil || current.BaseOnly || current.KeyDigest != result.Selection.KeyDigest ||
		current.SourceNarHash != result.Selection.SourceNarHash ||
		current.DerivationPath != result.Selection.DerivationPath ||
		current.CommittedInputsDigest != result.Selection.CommittedInputsDigest {
		return empty, noImage, errors.Join(err, errors.New("builder selection changed before image assembly"))
	}
	// Verify the captured environment through the live daemon before changing
	// the filesystem. A later rooted image must retain this exact store object.
	if err := b.verifyBuilderCapture(ctx, r, result, true); err != nil {
		return empty, noImage, err
	}
	if _, err := b.StopBuilder(ctx, r); err != nil {
		return empty, noImage, b.settleImageBuilder(r, err)
	}
	api := &builderCheckedFileAPI{backend: b, request: r, inner: &unixFileAPI{socket: b.config.UserSocket, project: b.config.Project, instance: builderName(r)}}
	if err := installBuilderImageFiles(ctx, api, result); err != nil {
		return empty, noImage, err
	}
	if _, err := b.StartBuilder(ctx, r); err != nil {
		return empty, noImage, b.settleImageBuilder(r, err)
	}
	if err := b.builderNixReady(ctx, r); err != nil {
		return empty, noImage, b.settleImageBuilder(r, fmt.Errorf("smoke boot readiness: %w", err))
	}
	if err := b.smokeBuilderImage(ctx, r, result); err != nil {
		return empty, noImage, b.settleImageBuilder(r, err)
	}
	// Stop every hook descendant before removing its writable home. A second
	// boot runs only fixed Nix maintenance; no activation hook is sourced.
	if _, err := b.StopBuilder(ctx, r); err != nil {
		return empty, noImage, b.settleImageBuilder(r, err)
	}
	if err := scrubBuilderScratch(ctx, api); err != nil {
		return empty, noImage, err
	}
	if _, err := b.StartBuilder(ctx, r); err != nil {
		return empty, noImage, b.settleImageBuilder(r, err)
	}
	if err := b.builderNixReady(ctx, r); err != nil {
		return empty, noImage, b.settleImageBuilder(r, fmt.Errorf("collection boot readiness: %w", err))
	}
	// The P-owned GC root is installed. The unprivileged daemon client can
	// collect only paths outside the captured closure and system roots.
	if _, err := b.builderNixExec(ctx, r, "store", "gc"); err != nil {
		return empty, noImage, b.settleImageBuilder(r, fmt.Errorf("builder Nix garbage collection failed: %w", err))
	}
	if err := b.verifyBuilderCapture(ctx, r, result, false); err != nil {
		return empty, noImage, b.settleImageBuilder(r, err)
	}
	if _, err := b.StopBuilder(ctx, r); err != nil {
		return empty, noImage, b.settleImageBuilder(r, err)
	}
	if err := scrubBuilderImage(ctx, api); err != nil {
		return empty, noImage, err
	}
	if err := verifyBuilderImageFiles(ctx, api, result); err != nil {
		return empty, noImage, err
	}
	return b.publishStoppedBuilderImage(ctx, r, result, gate)
}

func (b *Backend) settleImageBuilder(r Builder, cause error) error {
	cleanup, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := b.StopBuilder(cleanup, r); err != nil {
		return errors.Join(cause, fmt.Errorf("builder stop outcome unresolved; exact owned builder may remain active: %w", err))
	}
	return errors.Join(cause, errors.New("verified builder stopped; image stage refused"))
}

func (b *Backend) verifyBuilderCapture(ctx context.Context, r Builder, result BuilderNixResult, checkDerivation bool) error {
	raw, err := b.builderNixExec(ctx, r, "path-info", result.CaptureStorePath)
	if err != nil || strings.TrimSpace(string(raw)) != result.CaptureStorePath {
		return errors.Join(err, errors.New("captured environment missing from Nix database"))
	}
	if checkDerivation {
		raw, err = b.builderNixExec(ctx, r, "path-info", result.Selection.DerivationPath)
		if err != nil || strings.TrimSpace(string(raw)) != result.Selection.DerivationPath {
			return errors.Join(err, errors.New("selected derivation missing from Nix database"))
		}
	}
	return nil
}

func installBuilderImageFiles(ctx context.Context, api builderImageFiles, result BuilderNixResult) error {
	// A profile made by Nix is a symlink to a numbered generation symlink,
	// which points to the -env store object. Check the chain while stopped.
	profile, exists, err := api.getSymlinkBounded(ctx, builderCaptureProfile, 256)
	if err != nil || !exists || profile.typ != "symlink" || profile.uid != 1000 || !profileGeneration.Match(profile.data) {
		return errors.Join(err, errors.New("captured Nix profile link invalid"))
	}
	generation := filepath.Join(filepath.Dir(builderCaptureProfile), string(profile.data))
	link, exists, err := api.getSymlinkBounded(ctx, generation, 256)
	if err != nil || !exists || link.typ != "symlink" || link.uid != 1000 || string(link.data) != result.CaptureStorePath {
		return errors.Join(err, errors.New("captured Nix generation target changed"))
	}
	root, exists, err := api.get(ctx, "/nix/var/nix/gcroots")
	if err != nil || !exists || root.typ != "directory" || root.uid != 0 || root.gid != 0 || root.mode&022 != 0 {
		return errors.Join(err, errors.New("Nix GC root parent unsafe"))
	}
	if err := createGuestAbsent(ctx, api, builderGCPath, guestFile{typ: "symlink", uid: 0, gid: 0, mode: 0777, data: []byte(result.CaptureStorePath)}); err != nil {
		return err
	}
	if err := ensureGuestEtc(ctx, api); err != nil {
		return err
	}
	if err := ensureGuestFile(ctx, api, "/etc/p", guestFile{typ: "directory", uid: 0, gid: 0, mode: 0755}); err != nil {
		return err
	}
	if err := ensureGuestFile(ctx, api, nixenv.AttrsDir, guestFile{typ: "directory", uid: 0, gid: 0, mode: 0755}); err != nil {
		return err
	}
	files := []guestInstall{
		{builderActivationPath, guestFile{typ: "file", uid: 0, gid: 0, mode: 0444, data: []byte(result.Material.Script)}},
		{nixenv.AttrsDir + "/.attrs.sh", guestFile{typ: "file", uid: 0, gid: 0, mode: 0444, data: []byte(result.Material.AttrsSH)}},
		{nixenv.AttrsDir + "/.attrs.json", guestFile{typ: "file", uid: 0, gid: 0, mode: 0444, data: []byte(result.Material.AttrsJSON)}},
	}
	manifest, err := json.Marshal(result.Material)
	if err != nil {
		return err
	}
	files = append(files, guestInstall{nixenv.AttrsDir + "/material.json", guestFile{typ: "file", uid: 0, gid: 0, mode: 0444, data: manifest}})
	for _, f := range files {
		if err := ensureGuestFile(ctx, api, f.path, f.file); err != nil {
			return err
		}
	}
	return verifyBuilderImageFiles(ctx, api, result)
}

func verifyBuilderImageFiles(ctx context.Context, api builderImageFiles, result BuilderNixResult) error {
	manifest, err := json.Marshal(result.Material)
	if err != nil {
		return err
	}
	for _, f := range []guestInstall{
		{builderActivationPath, guestFile{typ: "file", uid: 0, gid: 0, mode: 0444, data: []byte(result.Material.Script)}},
		{nixenv.AttrsDir + "/.attrs.sh", guestFile{typ: "file", uid: 0, gid: 0, mode: 0444, data: []byte(result.Material.AttrsSH)}},
		{nixenv.AttrsDir + "/.attrs.json", guestFile{typ: "file", uid: 0, gid: 0, mode: 0444, data: []byte(result.Material.AttrsJSON)}},
		{nixenv.AttrsDir + "/material.json", guestFile{typ: "file", uid: 0, gid: 0, mode: 0444, data: manifest}},
	} {
		got, exists, err := api.getBounded(ctx, f.path, int64(len(f.file.data)))
		if err != nil || !exists || got.typ != "file" || got.uid != 0 || got.gid != 0 || got.mode != 0444 || string(got.data) != string(f.file.data) {
			return errors.Join(err, fmt.Errorf("image material %s changed", f.path))
		}
	}
	root, exists, err := api.getSymlinkBounded(ctx, builderGCPath, 256)
	if err != nil || !exists || root.typ != "symlink" || root.uid != 0 || root.gid != 0 || string(root.data) != result.CaptureStorePath {
		return errors.Join(err, errors.New("captured environment GC root changed"))
	}
	return nil
}

func (b *Backend) smokeBuilderImage(ctx context.Context, r Builder, result BuilderNixResult) error {
	if err := b.checkBuilderRunning(ctx, r); err != nil {
		return err
	}
	api := &unixFileAPI{socket: b.config.UserSocket, project: b.config.Project, instance: builderName(r)}
	for _, path := range []string{"/etc/p/git/identity", "/etc/p/session.json", "/etc/p/workspace.json"} {
		if _, exists, err := api.head(ctx, path, 0); err != nil || exists {
			return errors.Join(err, fmt.Errorf("builder contains session credential or configuration at %s", path))
		}
	}
	if err := b.verifyBuilderCapture(ctx, r, result, true); err != nil {
		return err
	}
	if err := b.prepareBuilderSmokeWorkspace(ctx, r); err != nil {
		return err
	}
	// The script and hook run only as the unprivileged builder user. The
	// builder has no NIC, host store, session credentials, or extra devices.
	const smoke = "source /etc/p/devshell/activate.sh && test \"$HOME\" = /home/p && test \"$USER\" = p && test \"$PWD\" = /workspace && command -v bash >/dev/null && command -v nix >/dev/null && test -e /nix/var/nix/daemon-socket/socket"
	if _, err := b.builderGuestExecAt(ctx, r, "/workspace", "/run/current-system/sw/bin/bash", "-c", smoke); err != nil {
		return fmt.Errorf("unprivileged devShell activation smoke failed: %w", err)
	}
	// Copying and sourcing as UID 1000 must not change the sealed evaluation
	// tree. The source NAR and selected derivation remain authoritative.
	current, err := b.ResolveBuilderNix(ctx, r, result.Selection.System)
	if err != nil || current.BaseOnly || current.SourceNarHash != result.Selection.SourceNarHash ||
		current.DerivationPath != result.Selection.DerivationPath || current.KeyDigest != result.Selection.KeyDigest {
		return errors.Join(err, errors.New("sealed source changed during activation smoke"))
	}
	return nil
}

func (b *Backend) prepareBuilderSmokeWorkspace(ctx context.Context, r Builder) error {
	// The captured Git export is already limited to 20,000 entries and 256 MiB,
	// and every relative symlink was validated before transfer. An unprivileged
	// fixed copy gives hooks their eventual writable repository cwd without
	// making the sealed Nix evaluation tree writable.
	contents, err := b.builderGuestExecAt(ctx, r, "/workspace", "/run/current-system/sw/bin/ls", "-A", "/workspace")
	if err != nil || len(contents) != 0 {
		return errors.Join(err, errors.New("builder smoke workspace is not empty"))
	}
	if _, err := b.builderGuestExecAt(ctx, r, "/workspace", "/run/current-system/sw/bin/cp", "-R", "-P", "--", builderSource+"/.", "/workspace/"); err != nil {
		return fmt.Errorf("copy captured source into disposable smoke workspace: %w", err)
	}
	if _, err := b.builderGuestExecAt(ctx, r, "/workspace", "/run/current-system/sw/bin/chmod", "-R", "u+rwX", "--", "/workspace"); err != nil {
		return fmt.Errorf("make disposable smoke workspace writable: %w", err)
	}
	return nil
}

func scrubBuilderImage(ctx context.Context, api builderImageFiles) error {
	// The builder is stopped: hook descendants cannot repopulate these trees.
	// Incus's SFTP RemoveAll starts with Stat, so all roots and ancestors
	// must be Lstat-verified real directories before recursive deletion.
	if err := scrubBuilderScratch(ctx, api); err != nil {
		return err
	}
	for _, item := range []struct {
		path           string
		uid, gid, mode int
		recreate       bool
	}{
		{"/opt/p/build", 0, 0, 0755, false},
		{"/var/log", 0, 0, 0755, true},
		{"/nix/var/log/nix", 0, 0, 0755, true},
	} {
		if err := scrubBuilderPath(ctx, api, item.path, item.uid, item.gid, item.mode, item.recreate); err != nil {
			return err
		}
	}
	return nil
}

func scrubBuilderScratch(ctx context.Context, api builderImageFiles) error {
	// A shellHook can register indirect Nix roots from any user-writable
	// location. Remove those paths while stopped, before collection. The
	// trusted root under gcroots/p-devshell is outside these trees.
	for _, item := range []struct {
		path           string
		uid, gid, mode int
	}{
		{"/home/p", 1000, 1000, 0700},
		{"/workspace", 1000, 1000, 0755},
		{"/tmp", 0, 0, 01777},
		{"/var/tmp", 0, 0, 01777},
	} {
		if err := scrubBuilderPath(ctx, api, item.path, item.uid, item.gid, item.mode, true); err != nil {
			return err
		}
	}
	for _, path := range []string{"/nix/var/nix/gcroots/per-user/p", "/nix/var/nix/profiles/per-user/p"} {
		if err := verifyScrubAncestors(ctx, api, path); err != nil {
			return err
		}
		f, exists, err := api.get(ctx, path)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		if f.typ != "directory" || f.uid != 1000 || f.gid != 1000 || f.mode&022 != 0 || f.mode&0700 != 0700 {
			return fmt.Errorf("unsafe user Nix root path %s", path)
		}
		if err := api.deleteTree(ctx, path); err != nil {
			return err
		}
		if _, exists, err := api.get(ctx, path); err != nil || exists {
			return errors.Join(err, fmt.Errorf("user Nix root retained at %s", path))
		}
	}
	return nil
}

func scrubBuilderPath(ctx context.Context, api builderImageFiles, path string, uid, gid, mode int, recreate bool) error {
	if err := verifyScrubAncestors(ctx, api, path); err != nil {
		return err
	}
	f, err := api.fullDirectoryMode(ctx, path)
	if err != nil || f.typ != "directory" || f.uid != uid || f.gid != gid || f.mode != mode {
		return errors.Join(err, fmt.Errorf("unsafe image scrub path %s (observed %s)", path, guestFileState(f, err == nil)))
	}
	if err := api.deleteTree(ctx, path); err != nil {
		return err
	}
	if _, exists, err := api.get(ctx, path); err != nil || exists {
		return errors.Join(err, fmt.Errorf("image scrub retained %s", path))
	}
	if recreate {
		// Incus file POST sends os.FileMode.Perm() and cannot establish POSIX
		// sticky bits. Create with ordinary permissions, then use confined
		// SFTP SETSTAT and full LSTAT to prove the final mode.
		initial := mode & 0777
		if err := createGuestAbsent(ctx, api, path, guestFile{typ: "directory", uid: uid, gid: gid, mode: initial}); err != nil {
			return err
		}
		if mode != initial {
			if err := api.chmodDirectory(ctx, path, mode); err != nil {
				return err
			}
		}
		got, err := api.fullDirectoryMode(ctx, path)
		if err != nil || got.typ != "directory" || got.uid != uid || got.gid != gid || got.mode != mode {
			return errors.Join(err, fmt.Errorf("image scrub directory %s recreation changed mode (observed %s)", path, guestFileState(got, err == nil)))
		}
	}
	return nil
}

func verifyScrubAncestors(ctx context.Context, api builderImageFiles, path string) error {
	for parent := filepath.Dir(path); parent != "."; parent = filepath.Dir(parent) {
		f, exists, err := api.get(ctx, parent)
		if err != nil || !exists || f.typ != "directory" || f.uid != 0 || f.gid != 0 || f.mode&022 != 0 {
			return errors.Join(err, fmt.Errorf("unsafe image scrub ancestor %s", parent))
		}
		if parent == "/" {
			break
		}
	}
	return nil
}

func builderImageProperties(r Builder, result BuilderNixResult, instance, project string) map[string]string {
	return map[string]string{
		"p.contract":           imageContract,
		"p.compression":        "none",
		"p.instance":           instance,
		"p.incus_project":      project,
		"p.project_path":       r.ProjectPath,
		"p.builder_request":    r.RequestUUID,
		"p.base_image":         r.BaseImageFingerprint,
		"p.environment_key":    result.Selection.KeyDigest,
		"p.material":           result.MaterialDigest,
		"p.capture_store_path": result.CaptureStorePath,
		"p.system":             result.Selection.System,
	}
}

func (b *Backend) publishStoppedBuilderImage(ctx context.Context, r Builder, result BuilderNixResult, gate func(BuilderImageClaim) error) (nixenv.Handle, BuilderImageInfo, error) {
	var zero nixenv.Handle
	var info BuilderImageInfo
	if err := b.CheckConfinement(ctx); err != nil {
		return zero, info, err
	}
	o, err := b.InspectBuilder(ctx, r)
	if err != nil || !o.Exists || !o.Ready || o.Status != "Stopped" {
		return zero, info, errors.Join(err, errors.New("verified stopped builder required for publication"))
	}
	before, err := b.imageList(ctx)
	if err != nil {
		return zero, info, err
	}
	labels := builderImageProperties(r, result, b.config.PInstanceID, b.config.Project)
	properties, err := expectedBuilderImageProperties(before, r.BaseImageFingerprint, labels)
	if err != nil {
		return zero, info, err
	}
	if gate != nil {
		claim := BuilderImageClaim{
			ProjectPath: r.ProjectPath, Key: result.Selection.KeyDigest, BaseFingerprint: r.BaseImageFingerprint,
			System: result.Selection.System, MaterialDigest: result.MaterialDigest,
			CaptureStorePath: result.CaptureStorePath, BuilderRequest: r.RequestUUID,
			Properties: maps.Clone(properties),
		}
		if err := gate(claim); err != nil {
			return zero, info, err
		}
	}
	args := builderImagePublishArgs(r, labels)
	raw, err := b.builderCommand(ctx, args...)
	if err != nil {
		return zero, info, errors.Join(err, b.unresolvedBuilderPublish(before, properties, result.Selection.System))
	}
	const prefix = "Instance published with fingerprint: "
	line := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(line, prefix) || !fingerprintPattern.MatchString(strings.TrimPrefix(line, prefix)) {
		return zero, info, errors.Join(errors.New("Incus publish returned no exact fingerprint"), b.unresolvedBuilderPublish(before, properties, result.Selection.System))
	}
	fingerprint := strings.TrimPrefix(line, prefix)
	for _, image := range before {
		if image.Fingerprint == fingerprint {
			return zero, info, errors.New("published fingerprint existed before request")
		}
	}
	after, err := b.imageList(ctx)
	if err != nil {
		return zero, info, errors.Join(err, fmt.Errorf("published fingerprint %s remains unaccepted pending metadata verification", fingerprint))
	}
	var matches int
	for _, image := range after {
		if image.Fingerprint != fingerprint {
			continue
		}
		matches++
		if err := checkPublishedBuilderImage(image, properties, result.Selection.System, b.config.Project); err != nil {
			return zero, info, errors.Join(err, fmt.Errorf("published fingerprint %s remains unaccepted", fingerprint))
		}
		info = BuilderImageInfo{Fingerprint: fingerprint, Project: b.config.Project, Size: image.Size, Properties: properties}
	}
	if matches != 1 {
		return zero, BuilderImageInfo{}, fmt.Errorf("published fingerprint %s missing or ambiguous; outcome remains unresolved", fingerprint)
	}
	afterBuilder, err := b.InspectBuilder(ctx, r)
	if err != nil || !afterBuilder.Exists || afterBuilder.Status != "Stopped" {
		return zero, BuilderImageInfo{}, errors.Join(err, fmt.Errorf("builder changed during publication of fingerprint %s; image remains unaccepted", fingerprint))
	}
	info.BuilderCleanupPending = true
	if _, err := b.DeleteBuilder(ctx, r); err == nil {
		info.BuilderCleanupPending = false
	}
	return nixenv.Handle{TargetKind: "incus-system-image", ContractVersion: imageContract, ContentIdentity: result.MaterialDigest, Locator: fingerprint}, info, nil
}

func builderImagePublishArgs(r Builder, labels map[string]string) []string {
	args := []string{"publish", builderName(r), "--compression", "none"}
	for _, key := range []string{"p.contract", "p.compression", "p.instance", "p.incus_project", "p.project_path", "p.builder_request", "p.base_image", "p.environment_key", "p.material", "p.capture_store_path", "p.system"} {
		args = append(args, key+"="+labels[key])
	}
	return args
}

// Incus exports the instance's inherited base metadata.yaml and merges the
// requested image properties into it. Compare against exactly that pinned
// base image, rather than allowing arbitrary extra properties.
func expectedBuilderImageProperties(before []builderImageJSON, base string, labels map[string]string) (map[string]string, error) {
	var found int
	var properties map[string]string
	for _, image := range before {
		if image.Fingerprint != base {
			continue
		}
		found++
		if image.Type != "container" {
			return nil, errors.New("builder base image type changed")
		}
		for key := range image.Properties {
			if strings.HasPrefix(key, "p.") {
				return nil, fmt.Errorf("builder base image contains reserved property %s", key)
			}
		}
		properties = maps.Clone(image.Properties)
	}
	if found != 1 {
		return nil, errors.New("builder base image metadata missing or ambiguous")
	}
	if properties == nil {
		properties = map[string]string{}
	}
	maps.Copy(properties, labels)
	return properties, nil
}

// A canceled client can leave a daemon operation in flight. One immediate
// inventory cannot prove absence, so a failed publish never yields a handle,
// never retries automatically, and never deletes a possible orphan.
func (b *Backend) unresolvedBuilderPublish(before []builderImageJSON, properties map[string]string, system string) error {
	probe, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	after, err := b.imageList(probe)
	if err == nil {
		old := map[string]bool{}
		for _, image := range before {
			old[image.Fingerprint] = true
		}
		for _, image := range after {
			if old[image.Fingerprint] || !fingerprintPattern.MatchString(image.Fingerprint) {
				continue
			}
			if checkPublishedBuilderImage(image, properties, system, b.config.Project) == nil {
				return fmt.Errorf("image publication outcome unresolved; P-labeled orphan %s requires later reconciliation", image.Fingerprint)
			}
		}
	}
	return errors.New("image publication outcome unresolved; a P-labeled image may appear after the client disconnects")
}

func checkPublishedBuilderImage(image builderImageJSON, properties map[string]string, system, project string) error {
	if image.Type != "container" {
		return errors.New("published image type changed")
	}
	if image.Architecture != strings.TrimSuffix(system, "-linux") {
		return errors.New("published image architecture changed")
	}
	if image.Public || image.AutoUpdate {
		return errors.New("published image is public or auto-updating")
	}
	if len(image.Aliases) != 0 {
		return fmt.Errorf("published image has %d aliases", len(image.Aliases))
	}
	if image.Size <= 0 {
		return errors.New("published image size invalid")
	}
	if image.Project != "" && image.Project != project {
		return errors.New("published image project changed")
	}
	if len(image.Properties) != len(properties) {
		missing, extra := imagePropertyKeyDifference(image.Properties, properties)
		return fmt.Errorf("published image property count changed: got %d expected %d missing=%s extra=%s", len(image.Properties), len(properties), missing, extra)
	}
	for key, value := range properties {
		actual, ok := image.Properties[key]
		if !ok || actual != value {
			return fmt.Errorf("published image property %s changed", key)
		}
	}
	return nil
}

func imagePropertyKeyDifference(actual, expected map[string]string) (string, string) {
	var missing, extra []string
	for key := range expected {
		if _, ok := actual[key]; !ok {
			missing = append(missing, key)
		}
	}
	for key := range actual {
		if _, ok := expected[key]; !ok {
			extra = append(extra, key)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	render := func(keys []string) string {
		if len(keys) > 5 {
			keys = keys[:5]
		}
		for i := range keys {
			if len(keys[i]) > 64 {
				keys[i] = keys[i][:64]
			}
		}
		return strings.Join(keys, ",")
	}
	return render(missing), render(extra)
}

type builderImageJSON struct {
	Fingerprint  string `json:"fingerprint"`
	Type         string `json:"type"`
	Architecture string `json:"architecture"`
	Project      string `json:"project"`
	Size         int64  `json:"size"`
	Public       bool   `json:"public"`
	AutoUpdate   bool   `json:"auto_update"`
	Aliases      []struct {
		Name string `json:"name"`
	} `json:"aliases"`
	Properties map[string]string `json:"properties"`
}

func (b *Backend) imageList(ctx context.Context) ([]builderImageJSON, error) {
	raw, err := b.command(ctx, "image", "list", "--format", "json")
	if err != nil {
		return nil, err
	}
	var images []builderImageJSON
	if err := decode(raw, &images); err != nil {
		return nil, err
	}
	return images, nil
}
