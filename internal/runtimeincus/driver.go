package runtimeincus

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"
	"unicode"

	"github.com/lgvo/p.ai/internal/nixenv"
	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// ImageBuilderVersion names how this driver builds an image: the image
// contract and compression, the builder's resources, the Nix version and
// rules it runs, and the activation material it captures. The builder's
// network is not part of it. Every image carries it, so a change to the
// recipe builds new images even for a commit that has one.
const ImageBuilderVersion = commitImageContract + "+" + commitImageCompression + ";" +
	builderCPU + "cpu," + builderMemory + "," + builderProcesses + "proc," + builderRootSize + ";" +
	"nix-" + nixenv.NixVersion + "+" + builderNixRulesVersion + ";" +
	nixenv.MaterialSchema + ";" + nixenv.AdapterVersion

// Driver is the Incus image driver: it builds commit images in disposable
// builders and reads and deletes the images this P instance labeled.
type Driver struct{ backend *Backend }

var _ runtimedriver.Images = (*Driver)(nil)

func NewDriver(b *Backend) *Driver { return &Driver{backend: b} }

func (d *Driver) Describe() runtimedriver.Capabilities {
	return runtimedriver.Capabilities{ImageBuilder: ImageBuilderVersion}
}

// The builder's two Nix stages, resolve and realize, run at most two at a
// time across the process, and each ends after seven minutes, its wait for a
// slot included.
var nixStageSlots = make(chan struct{}, 2)

const nixStageDeadline = 7 * time.Minute

func runNixStage[T any](parent context.Context, stage func(context.Context) (T, error)) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(parent, nixStageDeadline)
	defer cancel()
	select {
	case nixStageSlots <- struct{}{}:
		defer func() { <-nixStageSlots }()
	case <-ctx.Done():
		return zero, errors.New("builder Nix stage timed out waiting for a slot")
	}
	return stage(ctx)
}

// builderRequest reads a build into the builder it needs. It checks only
// the request itself.
func (b *Backend) builderRequest(build runtimedriver.Build) (Builder, string, error) {
	if build.Ref.Kind != runtimedriver.KindBuild || !uuidPattern.MatchString(build.Ref.ID) {
		return Builder{}, "", errors.New("invalid build reference")
	}
	if len(build.Labels) != len(commitLabelProperties) {
		return Builder{}, "", errors.New("build labels must be exactly the core image labels")
	}
	for _, l := range commitLabelProperties {
		if _, ok := build.Labels[l.label]; !ok {
			return Builder{}, "", fmt.Errorf("build label %s missing", l.label)
		}
	}
	labels := build.Labels
	r := Builder{
		RequestUUID: build.Ref.ID, ProjectPath: labels[runtimedriver.LabelProject],
		CommitOID: labels[runtimedriver.LabelSourceCommit], BaseImageFingerprint: labels[runtimedriver.LabelBaseImage],
		ContractVersion: "1", PublicIPv4: build.Network.PublicIPv4,
	}
	system := labels[runtimedriver.LabelSystem]
	if err := validateBuilder(r); err != nil {
		return Builder{}, "", err
	}
	if !validBuilderSystem(system) || labels[runtimedriver.LabelBuilderVersion] != ImageBuilderVersion || labels[runtimedriver.LabelBuild] != build.Ref.ID {
		return Builder{}, "", errors.New("build labels differ from this driver's build")
	}
	if err := checkSourceExport(build.Source, r); err != nil {
		return Builder{}, "", err
	}
	if !uuidPattern.MatchString(b.config.PInstanceID) {
		return Builder{}, "", errors.New("trusted P instance identity required for image builds")
	}
	if r.PublicIPv4 != "" {
		if b.config.PublicEgress == nil {
			return Builder{}, "", errors.New("public builder requires the public egress substrate")
		}
		if err := validatePublicAddress(b.config.PublicEgress, r.PublicIPv4); err != nil {
			return Builder{}, "", err
		}
	}
	return r, system, nil
}

// prepareBuild runs every check BuildImage makes before its gate. None of
// them changes anything.
func (b *Backend) prepareBuild(ctx context.Context, build runtimedriver.Build) (Builder, string, error) {
	r, system, err := b.builderRequest(build)
	if err != nil {
		return Builder{}, "", err
	}
	bundle, _, err := openBundle(build.Source.Bundle)
	if err != nil {
		return Builder{}, "", err
	}
	bundle.Close()
	if err := b.CheckConfinement(ctx); err != nil {
		return Builder{}, "", err
	}
	if err := b.checkBuilderPool(ctx); err != nil {
		return Builder{}, "", err
	}
	if err := b.checkBuilderSystem(ctx, r, system); err != nil {
		return Builder{}, "", err
	}
	// A build reference is new for every attempt: anything already holding
	// it is never adopted.
	instances, err := b.listBuilder(ctx, r.RequestUUID)
	if err != nil {
		return Builder{}, "", err
	}
	if len(instances) != 0 {
		return Builder{}, "", fmt.Errorf("%w: a builder already holds the build's name", runtimedriver.ErrMismatch)
	}
	images, err := b.imageList(ctx)
	if err != nil {
		return Builder{}, "", err
	}
	for _, image := range images {
		if image.Properties["p.instance"] == b.config.PInstanceID && image.Properties["p.build"] == r.RequestUUID {
			return Builder{}, "", fmt.Errorf("%w: image %s already carries the build", runtimedriver.ErrMismatch, image.Fingerprint)
		}
	}
	return r, system, nil
}

// BuildImage builds an image of the build's commit in a disposable builder:
// it clones the bundle into the workspace, resolves and realizes the
// commit's default devShell, and publishes the scrubbed builder with the
// build's labels. It calls g right before it initializes the builder.
func (d *Driver) BuildImage(ctx context.Context, build runtimedriver.Build, g runtimedriver.Gate) (runtimedriver.Image, error) {
	var zero runtimedriver.Image
	b := d.backend
	if g == nil {
		return zero, errors.New("build gate required")
	}
	r, system, err := b.prepareBuild(ctx, build)
	if err != nil {
		return zero, err
	}
	// A gated creation calls the gate exactly when it initializes the
	// builder, and refuses one that already exists.
	gated := false
	if _, err := b.createBuilder(ctx, r, func() error {
		if err := g(); err != nil {
			return err
		}
		gated = true
		return nil
	}); err != nil {
		if !gated {
			// A refused re-check or the gate's own error: nothing happened.
			return zero, err
		}
		return zero, b.failedBuild(r, err, false)
	}
	publishing := false
	image, err := b.buildCommitImage(ctx, r, build, system, &publishing)
	if err != nil {
		return zero, b.failedBuild(r, err, publishing)
	}
	return image, nil
}

// failedBuild settles what a failed build leaves. Before a publish it stops
// and deletes the builder; once that is proven, the failure is definite.
// After a publish command, or when the builder cannot be removed, the
// outcome is unknown and the builder stays for Delete.
func (b *Backend) failedBuild(r Builder, cause error, publishing bool) error {
	if publishing {
		return errors.Join(cause, runtimedriver.ErrUnknownOutcome)
	}
	cleanup, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// The build's gate already ran: removing what it left needs no other.
	if err := b.removeBuilderOf(cleanup, r.RequestUUID, func() error { return nil }); err != nil {
		return errors.Join(cause, fmt.Errorf("builder %s may remain: %w", builderName(r), err), runtimedriver.ErrUnknownOutcome)
	}
	return cause
}

// buildCommitImage runs a gated build from the created builder to the
// verified image, and deletes the builder.
func (b *Backend) buildCommitImage(ctx context.Context, r Builder, build runtimedriver.Build, system string, publishing *bool) (runtimedriver.Image, error) {
	var zero runtimedriver.Image
	if err := b.TransferBuilderSource(ctx, r, build.Source); err != nil {
		return zero, err
	}
	if _, err := b.StartBuilder(ctx, r); err != nil {
		return zero, err
	}
	if err := b.CloneBuilderSource(ctx, r, build.Source); err != nil {
		return zero, err
	}
	selected, err := runNixStage(ctx, func(stage context.Context) (BuilderNixSelection, error) {
		return b.ResolveBuilderNix(stage, r, system)
	})
	if err != nil {
		return zero, err
	}
	shell := runtimedriver.Shell{Selection: selected.Selection}
	var result BuilderNixResult
	api := b.checkedBuilderFiles(r)
	if selected.Selection == runtimedriver.SelectionDevShell {
		result, err = runNixStage(ctx, func(stage context.Context) (BuilderNixResult, error) {
			return b.RealizeBuilderNix(stage, r, selected)
		})
		if err != nil {
			return zero, err
		}
		if err := validateBuilderImageResult(r, result); err != nil {
			return zero, err
		}
		shell.MaterialSHA256 = result.MaterialDigest
		// The image must keep this exact store object: verify it through the
		// live daemon before the filesystem changes.
		if err := b.verifyBuilderCapture(ctx, r, result, true); err != nil {
			return zero, err
		}
		if _, err := b.StopBuilder(ctx, r); err != nil {
			return zero, err
		}
		if err := installBuilderImageFiles(ctx, api, result); err != nil {
			return zero, err
		}
		if _, err := b.StartBuilder(ctx, r); err != nil {
			return zero, err
		}
		if err := b.builderNixReady(ctx, r); err != nil {
			return zero, fmt.Errorf("smoke boot readiness: %w", err)
		}
		if err := b.smokeBuilderImage(ctx, r, result); err != nil {
			return zero, err
		}
	}
	// Stop every hook descendant before removing the writable trees, the
	// workspace included. The next boot runs only fixed Nix maintenance and
	// Git; no activation hook is sourced.
	if _, err := b.StopBuilder(ctx, r); err != nil {
		return zero, err
	}
	if err := scrubBuilderScratch(ctx, api, false); err != nil {
		return zero, err
	}
	if _, err := b.StartBuilder(ctx, r); err != nil {
		return zero, err
	}
	if err := b.builderNixReady(ctx, r); err != nil {
		return zero, fmt.Errorf("collection boot readiness: %w", err)
	}
	// The P-owned GC root is installed. The unprivileged daemon client can
	// collect only paths outside the captured closure and system roots.
	if _, err := b.builderNixExec(ctx, r, "store", "gc"); err != nil {
		return zero, fmt.Errorf("builder Nix garbage collection failed: %w", err)
	}
	if selected.Selection == runtimedriver.SelectionDevShell {
		if err := b.verifyBuilderCapture(ctx, r, result, false); err != nil {
			return zero, err
		}
	}
	// The image's workspace is a fresh clone, made after the last hook ran.
	if err := b.CloneBuilderSource(ctx, r, build.Source); err != nil {
		return zero, err
	}
	if _, err := b.StopBuilder(ctx, r); err != nil {
		return zero, err
	}
	if err := scrubBuilderImage(ctx, api); err != nil {
		return zero, err
	}
	if selected.Selection == runtimedriver.SelectionDevShell {
		if err := verifyBuilderImageFiles(ctx, api, result); err != nil {
			return zero, err
		}
	}
	if err := verifyBuilderWorkspace(ctx, api, r.CommitOID); err != nil {
		return zero, err
	}
	labels := commitImageLabels(build.Labels, shell, result.CaptureStorePath, b.config.PInstanceID, b.config.Project)
	published, err := b.publishCommitImage(ctx, r, labels, system, publishing)
	if err != nil {
		return zero, err
	}
	if err := b.removeBuilderOf(ctx, r.RequestUUID, func() error { return nil }); err != nil {
		return zero, fmt.Errorf("published image %s; its builder remains: %w", published.Fingerprint, err)
	}
	return runtimedriver.Image{Ref: runtimedriver.ImageRef(published.Fingerprint), Labels: maps.Clone(build.Labels), Shell: shell, Size: published.Size}, nil
}

// commitImage reads one of this instance's images back into a driver image.
// Anything that is not exactly a commit image of this instance and Incus
// project is a mismatch.
func (b *Backend) commitImage(image builderImageJSON) (runtimedriver.Image, error) {
	p := image.Properties
	mismatch := func(reason string) (runtimedriver.Image, error) {
		return runtimedriver.Image{}, fmt.Errorf("%w: image %.12s %s", runtimedriver.ErrMismatch, image.Fingerprint, reason)
	}
	if !fingerprintPattern.MatchString(image.Fingerprint) {
		return mismatch("has an invalid fingerprint")
	}
	if p["p.instance"] != b.config.PInstanceID || p["p.contract"] != commitImageContract || p["p.compression"] != commitImageCompression || p["p.incus_project"] != b.config.Project {
		return mismatch("is not this instance's commit image")
	}
	selection := runtimedriver.Selection(p["p.selection"])
	keys := map[string]bool{"p.contract": true, "p.compression": true, "p.instance": true, "p.incus_project": true, "p.selection": true}
	labels := runtimedriver.Labels{}
	for _, l := range commitLabelProperties {
		keys[l.property] = true
		labels[l.label] = p[l.property]
	}
	switch selection {
	case runtimedriver.SelectionDevShell:
		keys["p.material"], keys["p.capture_store_path"] = true, true
		capture := p["p.capture_store_path"]
		if !fingerprintPattern.MatchString(p["p.material"]) || !builderStorePathPattern.MatchString(capture) || !strings.HasSuffix(capture, "-env") {
			return mismatch("has invalid activation labels")
		}
	case runtimedriver.SelectionNoFlake, runtimedriver.SelectionNoDefault:
	default:
		return mismatch("has an invalid selection")
	}
	for key := range p {
		if strings.HasPrefix(key, "p.") && !keys[key] {
			return mismatch("has an unknown P label " + key)
		}
	}
	if !validBuilderProject(p["p.project_path"]) || !validBuilderOID(p["p.source_commit"]) || !fingerprintPattern.MatchString(p["p.base_image"]) ||
		!validBuilderSystem(p["p.system"]) || !validBuilderVersion(p["p.builder_version"]) || !uuidPattern.MatchString(p["p.build"]) {
		return mismatch("has invalid identity labels")
	}
	if err := checkPrivateImage(image, p["p.system"], b.config.Project); err != nil {
		return mismatch(err.Error())
	}
	return runtimedriver.Image{
		Ref:    runtimedriver.ImageRef(image.Fingerprint),
		Labels: labels,
		Shell:  runtimedriver.Shell{Selection: selection, MaterialSHA256: p["p.material"]},
		Size:   image.Size,
	}, nil
}

// An image keeps the builder version it was built with, which may be an
// older driver's.
func validBuilderVersion(version string) bool {
	if version == "" || len(version) > 128 {
		return false
	}
	for _, c := range version {
		if c > unicode.MaxASCII || !unicode.IsPrint(c) {
			return false
		}
	}
	return true
}

// ownImages lists the images that carry this instance's label, and every
// image the same listing holds.
func (b *Backend) ownImages(ctx context.Context) ([]builderImageJSON, []builderImageJSON, error) {
	if !uuidPattern.MatchString(b.config.PInstanceID) {
		return nil, nil, errors.New("trusted P instance identity required for images")
	}
	all, err := b.imageList(ctx)
	if err != nil {
		return nil, nil, err
	}
	if len(all) > 1024 {
		return nil, nil, errors.New("image inventory exceeds bound")
	}
	var own []builderImageJSON
	for _, image := range all {
		if image.Properties["p.instance"] == b.config.PInstanceID {
			own = append(own, image)
		}
	}
	return own, all, nil
}

// Images lists every image labeled with this instance. One it cannot read
// as a commit image fails the call: the list is never partial.
func (d *Driver) Images(ctx context.Context) ([]runtimedriver.Image, error) {
	own, _, err := d.backend.ownImages(ctx)
	if err != nil {
		return nil, err
	}
	images := make([]runtimedriver.Image, 0, len(own))
	for _, image := range own {
		img, err := d.backend.commitImage(image)
		if err != nil {
			return nil, err
		}
		images = append(images, img)
	}
	return images, nil
}

func (d *Driver) InspectImage(ctx context.Context, ref runtimedriver.ImageRef) (runtimedriver.Image, bool, error) {
	b := d.backend
	if !fingerprintPattern.MatchString(string(ref)) {
		return runtimedriver.Image{}, false, errors.New("invalid image reference")
	}
	if !uuidPattern.MatchString(b.config.PInstanceID) {
		return runtimedriver.Image{}, false, errors.New("trusted P instance identity required for images")
	}
	all, err := b.imageList(ctx)
	if err != nil {
		return runtimedriver.Image{}, false, err
	}
	var found []builderImageJSON
	for _, image := range all {
		if image.Fingerprint == string(ref) {
			found = append(found, image)
		}
	}
	switch len(found) {
	case 0:
		return runtimedriver.Image{}, false, nil
	case 1:
		img, err := b.commitImage(found[0])
		if err != nil {
			return runtimedriver.Image{}, false, err
		}
		return img, true, nil
	}
	return runtimedriver.Image{}, false, errors.New("ambiguous image reference")
}

// DeleteImage deletes exactly img. An image already gone needs no delete
// and no gate.
func (d *Driver) DeleteImage(ctx context.Context, img runtimedriver.Image, g runtimedriver.Gate) error {
	b := d.backend
	if g == nil {
		return errors.New("image delete gate required")
	}
	current, found, err := d.InspectImage(ctx, img.Ref)
	if err != nil || !found {
		return err
	}
	if !maps.Equal(current.Labels, img.Labels) {
		return fmt.Errorf("%w: image %.12s labels differ", runtimedriver.ErrMismatch, img.Ref)
	}
	if err := b.CheckConfinement(ctx); err != nil {
		return err
	}
	if err := g(); err != nil {
		return err
	}
	_, deleteErr := b.command(ctx, "image", "delete", string(img.Ref))
	_, found, err = d.InspectImage(ctx, img.Ref)
	if err != nil {
		return errors.Join(deleteErr, err, runtimedriver.ErrUnknownOutcome)
	}
	if found {
		return errors.Join(deleteErr, fmt.Errorf("image %.12s remains", img.Ref), runtimedriver.ErrUnknownOutcome)
	}
	return nil
}

// settleInterval separates the two observations that settle an effect.
var settleInterval = 250 * time.Millisecond

// Settle answers from two observations settleInterval apart. A build is
// applied only when both see the same single image of the build, exactly as
// its publication would have accepted it, no image operation running, and
// no builder of the build anywhere in the Incus project. It is never proven
// not applied: Incus may still admit a queued publish.
func (d *Driver) Settle(ctx context.Context, e runtimedriver.Effect) (runtimedriver.Settlement, error) {
	var observe func() (string, bool, error)
	switch e.Kind {
	case runtimedriver.EffectBuild:
		if e.Build.Ref.Kind != runtimedriver.KindBuild || !uuidPattern.MatchString(e.Build.Ref.ID) || e.Build.Labels[runtimedriver.LabelBuild] != e.Build.Ref.ID {
			return runtimedriver.Unknown, errors.New("invalid build effect")
		}
		observe = func() (string, bool, error) { return d.backend.observeBuild(ctx, e.Build) }
	case runtimedriver.EffectDeleteImage:
		if !fingerprintPattern.MatchString(string(e.Image.Ref)) {
			return runtimedriver.Unknown, errors.New("invalid image delete effect")
		}
		observe = func() (string, bool, error) {
			images, err := d.backend.imageList(ctx)
			if err != nil {
				return "", false, err
			}
			for _, image := range images {
				if image.Fingerprint == string(e.Image.Ref) {
					return "", false, nil
				}
			}
			return "absent", true, nil
		}
	default:
		return runtimedriver.Unknown, runtimedriver.ErrUnsupported
	}
	first, applied, err := observe()
	if err != nil || !applied {
		return runtimedriver.Unknown, err
	}
	timer := time.NewTimer(settleInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return runtimedriver.Unknown, ctx.Err()
	case <-timer.C:
	}
	second, applied, err := observe()
	if err != nil || !applied || second != first {
		return runtimedriver.Unknown, err
	}
	return runtimedriver.Applied, nil
}

// observeBuild reports whether one observation shows the build applied, and
// the image it found. The image's activation labels are its own word, so
// the builder is the independent evidence: a build removes its builder only
// after publication accepted the image, and every failure from the publish
// on leaves the builder for Delete. While a builder of the build exists,
// the answer stays unknown.
func (b *Backend) observeBuild(ctx context.Context, build runtimedriver.Build) (string, bool, error) {
	pending, err := b.pendingImagePublications(ctx)
	if err != nil || pending != 0 {
		return "", false, err
	}
	own, all, err := b.ownImages(ctx)
	if err != nil {
		return "", false, err
	}
	var carrying []builderImageJSON
	for _, image := range own {
		if image.Properties["p.build"] == build.Ref.ID {
			carrying = append(carrying, image)
		}
	}
	if len(carrying) != 1 {
		return "", false, nil
	}
	img, err := b.commitImage(carrying[0])
	if err != nil || !maps.Equal(img.Labels, build.Labels) {
		return "", false, nil
	}
	// Exactly what publication accepts: the pinned base image's inherited
	// properties and exactly P's labels. Without the base, nobody can tell.
	labels := commitImageLabels(build.Labels, img.Shell, carrying[0].Properties["p.capture_store_path"], b.config.PInstanceID, b.config.Project)
	expected, err := expectedBuilderImageProperties(all, build.Labels[runtimedriver.LabelBaseImage], labels)
	if err != nil || checkPublishedBuilderImage(carrying[0], expected, build.Labels[runtimedriver.LabelSystem], b.config.Project) != nil {
		return "", false, nil
	}
	instances, err := b.instanceInventory(ctx)
	if err != nil {
		return "", false, err
	}
	for _, in := range instances {
		if carriesBuild(in, build.Ref.ID) {
			return "", false, nil
		}
	}
	return string(img.Ref), true, nil
}

// Delete removes a build's builder. It acts only on a builder carrying the
// build's reference and this instance's label, and succeeds once no instance
// carries the reference. It never deletes an image.
func (d *Driver) Delete(ctx context.Context, ref runtimedriver.Ref, g runtimedriver.Gate) error {
	if ref.Kind != runtimedriver.KindBuild {
		return runtimedriver.ErrUnsupported
	}
	if !uuidPattern.MatchString(ref.ID) || g == nil {
		return errors.New("invalid build delete")
	}
	return d.backend.removeBuilderOf(ctx, ref.ID, g)
}

// removeBuilderOf removes the builder a build reference left and proves it
// gone. It calls g before its first effect.
func (b *Backend) removeBuilderOf(ctx context.Context, request string, g runtimedriver.Gate) error {
	in, exists, err := b.builderLeftover(ctx, request)
	if err != nil || !exists {
		return err
	}
	if err := b.CheckConfinement(ctx); err != nil {
		return err
	}
	if err := g(); err != nil {
		return err
	}
	name := builderNameFor(request)
	if in.Status != "Stopped" {
		// A leftover builder holds nothing worth a graceful shutdown.
		if _, err := b.command(ctx, "stop", name, "--force"); err != nil {
			return fmt.Errorf("stop builder %s: %w", name, err)
		}
		if in, exists, err = b.builderLeftover(ctx, request); err != nil || exists && in.Status != "Stopped" {
			return errors.Join(err, fmt.Errorf("builder %s did not stop", name))
		}
	}
	if exists {
		_, deleteErr := b.command(ctx, "delete", name)
		if _, exists, err = b.builderLeftover(ctx, request); err != nil || exists {
			return errors.Join(deleteErr, err, fmt.Errorf("builder %s remains", name))
		}
	}
	return nil
}

// builderLeftover reads the whole Incus project for the builder of a build
// reference: the instance at its name, which must carry the reference and
// this instance's label. An instance under another name carrying the
// reference is a renamed builder: a mismatch, so it can never pass for
// absent.
func (b *Backend) builderLeftover(ctx context.Context, request string) (instanceJSON, bool, error) {
	if !uuidPattern.MatchString(b.config.PInstanceID) {
		return instanceJSON{}, false, errors.New("trusted P instance identity required for builders")
	}
	instances, err := b.instanceInventory(ctx)
	if err != nil {
		return instanceJSON{}, false, err
	}
	name := builderNameFor(request)
	var builder instanceJSON
	found := false
	for _, in := range instances {
		if in.Name == name {
			builder, found = in, true
		} else if carriesBuild(in, request) {
			return instanceJSON{}, false, fmt.Errorf("%w: %s carries the build's reference under another name", runtimedriver.ErrMismatch, in.Name)
		}
	}
	if !found {
		return instanceJSON{}, false, nil
	}
	for _, c := range []map[string]string{builder.Config, builder.ExpandedConfig} {
		if builder.Type != "container" || c == nil || c["user.p.builder_request_uuid"] != request || c["user.p.instance"] != b.config.PInstanceID {
			return builder, true, fmt.Errorf("%w: %s is not this instance's builder of the build", runtimedriver.ErrMismatch, name)
		}
	}
	return builder, true, nil
}

// instanceInventory reads every instance of the Incus project, P's or not.
func (b *Backend) instanceInventory(ctx context.Context) ([]instanceJSON, error) {
	raw, err := b.command(ctx, "list", "--format", "json")
	if err != nil {
		return nil, err
	}
	var instances []instanceJSON
	if err := decode(raw, &instances); err != nil || len(instances) > 64 {
		return nil, errors.Join(err, errors.New("Incus container inventory unavailable"))
	}
	seen := make(map[string]bool, len(instances))
	for _, in := range instances {
		if in.Name == "" || seen[in.Name] {
			return nil, errors.New("Incus container inventory ambiguous")
		}
		seen[in.Name] = true
	}
	return instances, nil
}

// carriesBuild says whether an instance is, by name or by label, a builder
// of the build reference.
func carriesBuild(in instanceJSON, request string) bool {
	return in.Name == builderNameFor(request) || in.Config["user.p.builder_request_uuid"] == request || in.ExpandedConfig["user.p.builder_request_uuid"] == request
}
