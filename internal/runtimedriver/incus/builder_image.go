package incus

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

	"github.com/lgvo/p.ai/internal/nixenv"
	"github.com/lgvo/p.ai/internal/runtimedriver"
)

const (
	builderGCPath         = "/nix/var/nix/gcroots/p-devshell"
	builderActivationPath = "/etc/p/devshell/activate.sh"
	// commitImageContract names what a commit image holds and how its
	// properties read: the base image, the commit's devShell realized into
	// /nix with its activation material, and /workspace holding a clone at
	// the commit, detached, with no branch, remote or credentials.
	commitImageContract    = "p.incus-commit-image/v1"
	commitImageCompression = "none"
)

var profileGeneration = regexp.MustCompile(`^\.p-devshell-capture-[1-9][0-9]{0,6}-link$`)

type builderImageFiles interface {
	fileAPI
	getBounded(context.Context, string, int64) (guestFile, bool, error)
	getSymlinkBounded(context.Context, string, int64) (guestFile, bool, error)
	fullDirectoryMode(context.Context, string) (guestFile, error)
	lstat(context.Context, string) (guestFile, error)
	chmodDirectory(context.Context, string, int) error
	deleteTree(context.Context, string) error
}

func validateBuilderImageResult(r Builder, result BuilderNixResult) error {
	s := result.Selection
	if s.Builder != r || s.Selection != runtimedriver.SelectionDevShell || !validBuilderSystem(s.System) ||
		s.DevShellAttr != "devShells."+s.System+".default" || !builderDrvPattern.MatchString(s.DerivationPath) ||
		!builderNarHashPattern.MatchString(s.SourceNarHash) ||
		!builderStorePathPattern.MatchString(result.CaptureStorePath) || !strings.HasSuffix(result.CaptureStorePath, "-env") {
		return errors.New("invalid accepted builder image result")
	}
	digest, err := result.Material.Digest()
	if err != nil || digest != result.MaterialDigest {
		return errors.New("builder image material changed")
	}
	return nil
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
	files, err := builderImageMaterial(result)
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := ensureGuestFile(ctx, api, f.path, f.file); err != nil {
			return err
		}
	}
	return verifyBuilderImageFiles(ctx, api, result)
}

// builderImageMaterial is the root-owned, read-only activation material a
// devShell image holds.
func builderImageMaterial(result BuilderNixResult) ([]guestInstall, error) {
	manifest, err := json.Marshal(result.Material)
	if err != nil {
		return nil, err
	}
	return []guestInstall{
		{builderActivationPath, guestFile{typ: "file", uid: 0, gid: 0, mode: 0444, data: []byte(result.Material.Script)}},
		{nixenv.AttrsDir + "/.attrs.sh", guestFile{typ: "file", uid: 0, gid: 0, mode: 0444, data: []byte(result.Material.AttrsSH)}},
		{nixenv.AttrsDir + "/.attrs.json", guestFile{typ: "file", uid: 0, gid: 0, mode: 0444, data: []byte(result.Material.AttrsJSON)}},
		{nixenv.AttrsDir + "/material.json", guestFile{typ: "file", uid: 0, gid: 0, mode: 0444, data: manifest}},
	}, nil
}

func verifyBuilderImageFiles(ctx context.Context, api builderImageFiles, result BuilderNixResult) error {
	files, err := builderImageMaterial(result)
	if err != nil {
		return err
	}
	for _, f := range files {
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

// smokeBuilderImage sources the installed activation in the workspace clone
// as the builder user, then resolves again: the commit's NAR hash and
// derivation must be what was realized.
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
	// The script and hook run only as the unprivileged builder user. The
	// builder has no host store, session credentials, or devices beyond its
	// root disk and, with public egress, its public NIC.
	const smoke = "source /etc/p/devshell/activate.sh && test \"$HOME\" = /home/p && test \"$USER\" = p && test \"$PWD\" = /workspace && command -v bash >/dev/null && command -v nix >/dev/null && test -e /nix/var/nix/daemon-socket/socket"
	if _, err := b.builderGuestExec(ctx, r, "/run/current-system/sw/bin/bash", "-c", smoke); err != nil {
		return fmt.Errorf("unprivileged devShell activation smoke failed: %w", err)
	}
	// The hook may change the clone's working tree, but Nix reads the commit
	// from its objects: the NAR hash and derivation must not move.
	current, err := b.ResolveBuilderNix(ctx, r, result.Selection.System)
	if err != nil || current != result.Selection {
		return errors.Join(err, errors.New("the commit's flake changed during activation smoke"))
	}
	return nil
}

// scrubBuilderImage leaves the image only what it must hold: it removes the
// builder's scratch, its source bundle, its logs and its network
// configuration, and keeps the workspace clone.
func scrubBuilderImage(ctx context.Context, api builderImageFiles) error {
	// The builder is stopped: hook descendants cannot repopulate these trees.
	// Incus's SFTP RemoveAll starts with Stat, so all roots and ancestors
	// must be Lstat-verified real directories before recursive deletion.
	if err := scrubBuilderScratch(ctx, api, true); err != nil {
		return err
	}
	for _, item := range []struct {
		path           string
		uid, gid, mode int
		recreate       bool
	}{
		{builderBuildDir, 0, 0, 0755, false},
		{"/var/log", 0, 0, 0755, true},
	} {
		if err := scrubBuilderPath(ctx, api, item.path, item.uid, item.gid, item.mode, item.recreate); err != nil {
			return err
		}
	}
	// Nix makes its log tree only when it builds something: a commit without
	// a devShell to realize leaves none, and then there is nothing to scrub.
	if err := verifyScrubAncestors(ctx, api, "/nix/var/log"); err != nil {
		return err
	}
	if _, exists, err := api.get(ctx, "/nix/var/log"); err != nil {
		return err
	} else if exists {
		if err := scrubBuilderPath(ctx, api, "/nix/var/log/nix", 0, 0, 0755, true); err != nil {
			return err
		}
	}
	return scrubBuilderResolver(ctx, api)
}

// scrubBuilderResolver takes the builder's network configuration out of the
// image. Preparing a public builder's network replaced the image's resolver
// link with a root file naming the loopback DNS-over-HTTPS resolver; the
// image gets its link back. Anything else at the path is refused.
func scrubBuilderResolver(ctx context.Context, api builderImageFiles) error {
	const path, imageTarget, publicResolver = "/etc/resolv.conf", "/etc/static/resolv.conf", "nameserver 127.0.0.1\n"
	if err := verifyScrubAncestors(ctx, api, path); err != nil {
		return err
	}
	f, exists, err := api.getSymlinkBounded(ctx, path, 64)
	if err != nil || !exists {
		return errors.Join(err, errors.New("builder resolver configuration missing"))
	}
	if f.typ == "symlink" && f.uid == 0 && f.gid == 0 && string(f.data) == imageTarget {
		return nil
	}
	if f.typ == "file" {
		f, exists, err = api.getBounded(ctx, path, int64(len(publicResolver)))
	}
	if err != nil || !exists || f.typ != "file" || f.uid != 0 || f.gid != 0 || f.mode != 0644 || string(f.data) != publicResolver {
		return errors.Join(err, errors.New("builder resolver configuration changed"))
	}
	if err := api.deleteTree(ctx, path); err != nil {
		return err
	}
	return createGuestAbsent(ctx, api, path, guestFile{typ: "symlink", uid: 0, gid: 0, mode: 0777, data: []byte(imageTarget)})
}

// scrubBuilderScratch empties the builder user's writable trees and user Nix
// roots while the builder is stopped. Before collection it empties the
// workspace too, so a root registered from the clone cannot keep anything;
// the final scrub keeps the fresh clone made after collection.
func scrubBuilderScratch(ctx context.Context, api builderImageFiles, keepWorkspace bool) error {
	// A shellHook can register indirect Nix roots from any user-writable
	// location. The trusted root under gcroots/p-devshell is outside these
	// trees.
	type scratch struct {
		path           string
		uid, gid, mode int
	}
	trees := []scratch{{"/home/p", 1000, 1000, 0700}, {"/tmp", 0, 0, 01777}, {"/var/tmp", 0, 0, 01777}}
	if !keepWorkspace {
		trees = append(trees, scratch{builderWorkspace, 1000, 1000, 0755})
	}
	for _, item := range trees {
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

// verifyBuilderWorkspace reads the stopped image's workspace without
// following a symlink: the builder user's clone with a detached HEAD at the
// commit.
func verifyBuilderWorkspace(ctx context.Context, api builderImageFiles, commit string) error {
	workspace, err := api.fullDirectoryMode(ctx, builderWorkspace)
	if err != nil || workspace.typ != "directory" || workspace.uid != 1000 || workspace.gid != 1000 || workspace.mode != 0755 {
		return errors.Join(err, errors.New("image workspace is not the builder user's directory"))
	}
	for _, item := range []struct{ path, typ string }{{builderWorkspace + "/.git", "directory"}, {builderWorkspace + "/.git/HEAD", "file"}} {
		f, err := api.lstat(ctx, item.path)
		if err != nil || f.typ != item.typ || f.uid != 1000 || f.gid != 1000 {
			return errors.Join(err, fmt.Errorf("image workspace %s is not the clone's", item.path))
		}
	}
	head, exists, err := api.getBounded(ctx, builderWorkspace+"/.git/HEAD", 128)
	if err != nil || !exists || head.typ != "file" || head.uid != 1000 || string(head.data) != commit+"\n" {
		return errors.Join(err, errors.New("image workspace HEAD is not the commit, detached"))
	}
	// A session's runtime kit initializes the workspace once and marks it;
	// the image must leave that to every session.
	if _, exists, err := api.get(ctx, builderWorkspace+"/.git/p-initialized"); err != nil || exists {
		return errors.Join(err, errors.New("image workspace is marked initialized"))
	}
	return nil
}

// commitLabelProperties names the image property that stores each core
// label.
var commitLabelProperties = [...]struct{ label, property string }{
	{runtimedriver.LabelProject, "p.project_path"},
	{runtimedriver.LabelSourceCommit, "p.source_commit"},
	{runtimedriver.LabelBaseImage, "p.base_image"},
	{runtimedriver.LabelSystem, "p.system"},
	{runtimedriver.LabelBuilderVersion, "p.builder_version"},
	{runtimedriver.LabelBuild, "p.build"},
}

// commitImageLabels are the P properties a commit image carries: the core
// labels under Incus names, this instance and project, and the devShell
// outcome. Incus merges the base image's own properties in.
func commitImageLabels(labels runtimedriver.Labels, shell runtimedriver.Shell, capture, instance, project string) map[string]string {
	out := map[string]string{
		"p.contract":      commitImageContract,
		"p.compression":   commitImageCompression,
		"p.instance":      instance,
		"p.incus_project": project,
		"p.selection":     string(shell.Selection),
	}
	for _, l := range commitLabelProperties {
		out[l.property] = labels[l.label]
	}
	if shell.Selection == runtimedriver.SelectionDevShell {
		out["p.material"] = shell.MaterialSHA256
		out["p.capture_store_path"] = capture
	}
	return out
}

func commitImagePublishArgs(r Builder, labels map[string]string) []string {
	args := []string{"publish", builderName(r), "--compression", commitImageCompression}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, key+"="+labels[key])
	}
	return args
}

// publishCommitImage publishes the stopped, scrubbed builder as a private
// image and verifies the image Incus reports. It sets *publishing before the
// publish command: from then on a failure leaves an outcome to settle.
func (b *Backend) publishCommitImage(ctx context.Context, r Builder, labels map[string]string, system string, publishing *bool) (builderImageJSON, error) {
	var zero builderImageJSON
	if err := b.CheckConfinement(ctx); err != nil {
		return zero, err
	}
	o, err := b.InspectBuilder(ctx, r)
	if err != nil || !o.Exists || !o.Ready || o.Status != "Stopped" {
		return zero, errors.Join(err, errors.New("verified stopped builder required for publication"))
	}
	before, err := b.imageList(ctx)
	if err != nil {
		return zero, err
	}
	properties, err := expectedBuilderImageProperties(before, r.BaseImageFingerprint, labels)
	if err != nil {
		return zero, err
	}
	*publishing = true
	raw, err := b.builderCommand(ctx, commitImagePublishArgs(r, labels)...)
	if err != nil {
		return zero, fmt.Errorf("Incus publish outcome unknown: %w", err)
	}
	const prefix = "Instance published with fingerprint: "
	line := strings.TrimSpace(string(raw))
	fingerprint, ok := strings.CutPrefix(line, prefix)
	if !ok || !fingerprintPattern.MatchString(fingerprint) {
		return zero, errors.New("Incus publish returned no exact fingerprint")
	}
	for _, image := range before {
		if image.Fingerprint == fingerprint {
			return zero, errors.New("published fingerprint existed before request")
		}
	}
	after, err := b.imageList(ctx)
	if err != nil {
		return zero, errors.Join(err, fmt.Errorf("published fingerprint %s unverified", fingerprint))
	}
	var published builderImageJSON
	var matches int
	for _, image := range after {
		if image.Fingerprint != fingerprint {
			continue
		}
		matches++
		if err := checkPublishedBuilderImage(image, properties, system, b.config.Project); err != nil {
			return zero, errors.Join(err, fmt.Errorf("published fingerprint %s differs from its build", fingerprint))
		}
		published = image
	}
	if matches != 1 {
		return zero, fmt.Errorf("published fingerprint %s missing or ambiguous", fingerprint)
	}
	afterBuilder, err := b.InspectBuilder(ctx, r)
	if err != nil || !afterBuilder.Exists || afterBuilder.Status != "Stopped" {
		return zero, errors.Join(err, fmt.Errorf("builder changed during publication of fingerprint %s", fingerprint))
	}
	return published, nil
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

func checkPublishedBuilderImage(image builderImageJSON, properties map[string]string, system, project string) error {
	if err := checkPrivateImage(image, system, project); err != nil {
		return err
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

// checkPrivateImage holds for every image P builds: a private container image
// of the system's architecture in P's Incus project, with no alias.
func checkPrivateImage(image builderImageJSON, system, project string) error {
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

// pendingImagePublications counts the image operations Incus is running. A
// publish runs as one; until none is left, a missing image may still appear.
func (b *Backend) pendingImagePublications(ctx context.Context) (int, error) {
	raw, err := b.command(ctx, "operation", "list", "--format", "json")
	if err != nil {
		return 0, err
	}
	var operations []struct {
		Description string `json:"description"`
		Status      string `json:"status"`
	}
	if err := decode(raw, &operations); err != nil {
		return 0, err
	}
	var pending int
	for _, op := range operations {
		if op.Description == "Downloading image" && (op.Status == "Running" || op.Status == "Pending") {
			pending++
		}
	}
	return pending, nil
}
