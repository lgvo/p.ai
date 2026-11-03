package memory

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"slices"
	"strings"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

// The labels every build carries.
var buildLabels = []string{
	runtimedriver.LabelProject, runtimedriver.LabelSourceCommit, runtimedriver.LabelBaseImage,
	runtimedriver.LabelSystem, runtimedriver.LabelBuilderVersion, runtimedriver.LabelBuild,
}

// BuildImage builds in a builder that exists only while the build runs: it
// publishes an image labeled with the build's labels, which finds no flake
// at the commit, and removes the builder. A late publication leaves the
// builder behind for Delete.
func (d *Driver) BuildImage(_ context.Context, b runtimedriver.Build, g runtimedriver.Gate) (runtimedriver.Image, error) {
	var zero runtimedriver.Image
	if err := d.alive(); err != nil {
		return zero, err
	}
	if g == nil {
		return zero, errors.New("build gate required")
	}
	if err := d.checkBuild(b); err != nil {
		return zero, err
	}
	w := d.w
	w.mu.Lock()
	err := w.checkBuildFree(b)
	w.mu.Unlock()
	if err != nil {
		return zero, err
	}
	if err := g(); err != nil {
		return zero, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.checkBuildFree(b); err != nil {
		return zero, err
	}
	builder := &object{
		name: address(b.Ref), instance: thisInstance, ref: b.Ref, labels: maps.Clone(b.Labels),
		incarnation: runtimedriver.Incarnation(fmt.Sprintf("incarnation-%d", w.next())), state: runtimedriver.StateRunning,
		files: map[string]*node{},
	}
	w.objects[builder.name] = builder
	built := &image{ref: w.newImageRef(), instance: thisInstance, labels: maps.Clone(b.Labels),
		shell: runtimedriver.Shell{Selection: runtimedriver.SelectionNoFlake}, size: 1 << 30}
	key := effectKey(runtimedriver.EffectBuild, b.Ref.ID)
	if !w.admit(runtimedriver.EffectBuild, key, builder.name, func() { w.images[built.ref] = built }) {
		return zero, fmt.Errorf("the publication was not answered: %w", runtimedriver.ErrUnknownOutcome)
	}
	delete(w.objects, builder.name)
	return built.public(), nil
}

// checkBuild refuses a build the driver cannot run exactly.
func (d *Driver) checkBuild(b runtimedriver.Build) error {
	if b.Ref.Kind != runtimedriver.KindBuild || b.Ref.ID == "" {
		return errors.New("invalid build reference")
	}
	if len(b.Labels) != len(buildLabels) {
		return errors.New("build labels must be exactly the core image labels")
	}
	for _, l := range buildLabels {
		if b.Labels[l] == "" {
			return fmt.Errorf("build label %s missing", l)
		}
	}
	if b.Labels[runtimedriver.LabelBuild] != b.Ref.ID || b.Labels[runtimedriver.LabelBuilderVersion] != d.w.config.Capabilities.ImageBuilder {
		return errors.New("build labels differ from this driver's build")
	}
	if b.Source.Bundle == "" || b.Source.Ref == "" || b.Source.Commit != b.Labels[runtimedriver.LabelSourceCommit] {
		return errors.New("the build's source export does not name its commit")
	}
	if b.Network.PublicIPv4 != "" {
		if !slices.Contains(d.w.config.Capabilities.Networks, runtimedriver.NetworkPublicEgress) {
			return errors.New("the driver offers no public egress")
		}
		if ip := net.ParseIP(b.Network.PublicIPv4); ip == nil || ip.To4() == nil {
			return errors.New("invalid public-egress address")
		}
	}
	return nil
}

// checkBuildFree refuses a build whose reference anything already carries,
// or that the scope cannot hold. Called with w.mu held.
func (w *World) checkBuildFree(b runtimedriver.Build) error {
	if w.objects[address(b.Ref)] != nil || len(w.carrying(b.Ref)) > 0 {
		return fmt.Errorf("%w: a builder already carries the build's reference", runtimedriver.ErrMismatch)
	}
	for _, im := range w.images {
		if im.instance == thisInstance && im.labels[runtimedriver.LabelBuild] == b.Ref.ID {
			return fmt.Errorf("%w: image %s already carries the build", runtimedriver.ErrMismatch, im.ref)
		}
	}
	if w.pending[effectKey(runtimedriver.EffectBuild, b.Ref.ID)] != nil {
		return fmt.Errorf("%w: the build's publication may still land", runtimedriver.ErrMismatch)
	}
	if base := w.images[runtimedriver.ImageRef(b.Labels[runtimedriver.LabelBaseImage])]; base == nil {
		return errors.New("the build's base image is not in scope")
	}
	if w.full() {
		return errors.New("the scope holds no more builders")
	}
	return nil
}

// Images lists this instance's images, sorted by reference.
func (d *Driver) Images(context.Context) ([]runtimedriver.Image, error) {
	if err := d.alive(); err != nil {
		return nil, err
	}
	w := d.w
	w.mu.Lock()
	defer w.mu.Unlock()
	images := []runtimedriver.Image{}
	for _, im := range w.images {
		if im.instance == thisInstance {
			images = append(images, im.public())
		}
	}
	slices.SortFunc(images, func(a, b runtimedriver.Image) int { return strings.Compare(string(a.Ref), string(b.Ref)) })
	return images, nil
}

func (d *Driver) InspectImage(_ context.Context, ref runtimedriver.ImageRef) (runtimedriver.Image, bool, error) {
	if err := d.alive(); err != nil {
		return runtimedriver.Image{}, false, err
	}
	w := d.w
	w.mu.Lock()
	defer w.mu.Unlock()
	im := w.images[ref]
	switch {
	case im == nil:
		return runtimedriver.Image{}, false, nil
	case im.instance != thisInstance:
		return runtimedriver.Image{}, false, fmt.Errorf("%w: image %s is not this instance's", runtimedriver.ErrMismatch, ref)
	}
	return im.public(), true, nil
}

// DeleteImage deletes exactly img. Runtimes keep only the image's
// reference, so none is affected.
func (d *Driver) DeleteImage(ctx context.Context, img runtimedriver.Image, g runtimedriver.Gate) error {
	if g == nil {
		return errors.New("image delete gate required")
	}
	current, found, err := d.InspectImage(ctx, img.Ref)
	if err != nil || !found {
		return err
	}
	if !maps.Equal(current.Labels, img.Labels) {
		return fmt.Errorf("%w: image %s labels differ", runtimedriver.ErrMismatch, img.Ref)
	}
	if err := g(); err != nil {
		return err
	}
	w := d.w
	w.mu.Lock()
	defer w.mu.Unlock()
	if im := w.images[img.Ref]; im == nil || !maps.Equal(im.labels, img.Labels) {
		return errors.New("the image changed before its deletion")
	}
	key := effectKey(runtimedriver.EffectDeleteImage, string(img.Ref))
	if !w.admit(runtimedriver.EffectDeleteImage, key, string(img.Ref), func() { delete(w.images, img.Ref) }) {
		return fmt.Errorf("the image deletion was not answered: %w", runtimedriver.ErrUnknownOutcome)
	}
	return nil
}

// deleteBuilder removes the builder a build left behind. It never deletes
// an image.
func (d *Driver) deleteBuilder(r runtimedriver.Ref, g runtimedriver.Gate) error {
	w := d.w
	w.mu.Lock()
	builder, err := w.builderOf(r)
	w.mu.Unlock()
	if err != nil || builder == nil {
		return err
	}
	if err := g(); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.remove(builder)
	return nil
}

// builderOf is the builder at the build's address, nil when nothing carries
// the build. Called with w.mu held.
func (w *World) builderOf(r runtimedriver.Ref) (*object, error) {
	o := w.objects[address(r)]
	if o == nil {
		if len(w.carrying(r)) > 0 {
			return nil, fmt.Errorf("%w: the build's reference is carried under another name", runtimedriver.ErrMismatch)
		}
		return nil, nil
	}
	if o.instance != thisInstance || o.ref != r {
		return nil, fmt.Errorf("%w: %s is not this instance's builder of the build", runtimedriver.ErrMismatch, o.name)
	}
	return o, nil
}
