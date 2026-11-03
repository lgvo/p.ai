package conformance

import (
	"errors"
	"maps"
	"testing"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

var imageCases = []testCase{
	{"a build calls the gate once and labels its image verbatim", func(t *testing.T, f Fixture) {
		b := f.Images().Build()
		g := &gate{before: func() {
			if img, found := imageOfBuild(t, f, b.Ref); found {
				t.Errorf("at the gate the build's image %s already existed", img.Ref)
			}
		}}
		img, err := f.Driver().BuildImage(t.Context(), b, g.call)
		if err != nil {
			t.Fatalf("BuildImage: %v", err)
		}
		if g.count() != 1 {
			t.Fatalf("BuildImage called the gate %d times, want 1", g.count())
		}
		if !maps.Equal(img.Labels, b.Labels) {
			t.Fatalf("the image is labeled %v, want %v", img.Labels, b.Labels)
		}
		listed, found := imageOfBuild(t, f, b.Ref)
		if !found || listed.Ref != img.Ref || !maps.Equal(listed.Labels, b.Labels) {
			t.Fatalf("Images lists %+v (found %t), want %s labeled %v", listed, found, img.Ref, b.Labels)
		}
		inspected, found, err := f.Driver().InspectImage(t.Context(), img.Ref)
		if err != nil || !found || !maps.Equal(inspected.Labels, b.Labels) {
			t.Fatalf("InspectImage = %+v, %t, %v; want labels %v", inspected, found, err, b.Labels)
		}
	}},
	{"a failing build gate leaves nothing behind", func(t *testing.T, f Fixture) {
		b := f.Images().Build()
		g := &gate{err: errGate}
		if _, err := f.Driver().BuildImage(t.Context(), b, g.call); !errors.Is(err, errGate) {
			t.Fatalf("BuildImage with a failing gate = %v, want the gate's error", err)
		}
		if img, found := imageOfBuild(t, f, b.Ref); found {
			t.Fatalf("a failed gate left the image %s", img.Ref)
		}
		if err := f.Driver().Delete(t.Context(), b.Ref, "", unexpectedGate(t)); err != nil {
			t.Fatalf("Delete of the build's leftovers: %v", err)
		}
	}},
	{"a build reference that already carries an image is refused without calling the gate", func(t *testing.T, f Fixture) {
		b := f.Images().Build()
		if _, err := f.Driver().BuildImage(t.Context(), b, func() error { return nil }); err != nil {
			t.Fatalf("BuildImage: %v", err)
		}
		if _, err := f.Driver().BuildImage(t.Context(), b, unexpectedGate(t)); err == nil {
			t.Fatal("a second build of the same reference succeeded")
		}
	}},
	{"delete image deletes exactly the image with its labels", func(t *testing.T, f Fixture) {
		img := built(t, f)
		relabeled := img
		relabeled.Labels = maps.Clone(img.Labels)
		relabeled.Labels[runtimedriver.LabelProject] += "-other"
		if err := f.Driver().DeleteImage(t.Context(), relabeled, unexpectedGate(t)); !errors.Is(err, runtimedriver.ErrMismatch) {
			t.Fatalf("DeleteImage with other labels = %v, want ErrMismatch", err)
		}
		if _, found, err := f.Driver().InspectImage(t.Context(), img.Ref); err != nil || !found {
			t.Fatalf("after a refused delete InspectImage = %t, %v; want the image", found, err)
		}
		g := &gate{}
		if err := f.Driver().DeleteImage(t.Context(), img, g.call); err != nil || g.count() != 1 {
			t.Fatalf("DeleteImage = %v after %d gate calls, want success after 1", err, g.count())
		}
		if _, found, err := f.Driver().InspectImage(t.Context(), img.Ref); err != nil || found {
			t.Fatalf("after DeleteImage InspectImage = %t, %v; want nothing", found, err)
		}
		if err := f.Driver().DeleteImage(t.Context(), img, unexpectedGate(t)); err != nil {
			t.Fatalf("DeleteImage of an image already gone: %v", err)
		}
		wantSettled(t, f, runtimedriver.Effect{Kind: runtimedriver.EffectDeleteImage, Image: img}, runtimedriver.Applied)
	}},
	{"another instance's image is a mismatch and is never listed", func(t *testing.T, f Fixture) {
		ref := f.Images().AddOtherInstanceImage()
		if _, _, err := f.Driver().InspectImage(t.Context(), ref); !errors.Is(err, runtimedriver.ErrMismatch) {
			t.Fatalf("InspectImage of another instance's image = %v, want ErrMismatch", err)
		}
		images, err := f.Driver().Images(t.Context())
		if err != nil {
			t.Fatalf("Images: %v", err)
		}
		for _, img := range images {
			if img.Ref == ref {
				t.Fatal("Images lists another instance's image")
			}
		}
	}},
	{"deleting an image never affects runtimes created from it", func(t *testing.T, f Fixture) {
		img := built(t, f)
		s := f.Session(SessionOptions{})
		s.Image = img.Ref
		c := created(t, f, s)
		if err := f.Driver().DeleteImage(t.Context(), img, func() error { return nil }); err != nil {
			t.Fatalf("DeleteImage: %v", err)
		}
		if o := inspect(t, f, s); o.State != runtimedriver.StateStopped || !o.Complete || o.Incarnation != c.Incarnation {
			t.Fatalf("after DeleteImage the runtime is %+v, want it as created: %+v", o, c)
		}
		if o, err := f.Driver().Start(t.Context(), s); err != nil || !o.Host.Ready {
			t.Fatalf("Start after DeleteImage = %+v, %v; want a ready host", o, err)
		}
	}},
	{"the builder an unsettled build left is in the inventory with the build's reference and labels", func(t *testing.T, f Fixture) {
		b := f.Images().Build()
		f.Images().DelayBuild()
		if _, err := f.Driver().BuildImage(t.Context(), b, func() error { return nil }); !errors.Is(err, runtimedriver.ErrUnknownOutcome) {
			t.Fatalf("delayed BuildImage = %v, want ErrUnknownOutcome", err)
		}
		o, found := inventoried(t, f, func(o runtimedriver.Object) bool { return o.Ref == b.Ref })
		if !found || o.Labels[runtimedriver.LabelInstance] == "" {
			t.Fatalf("the builder is listed as %+v (found %t), want it with the instance's label", o, found)
		}
		for _, l := range []string{runtimedriver.LabelBuild, runtimedriver.LabelProject, runtimedriver.LabelSourceCommit, runtimedriver.LabelBaseImage} {
			if o.Labels[l] != b.Labels[l] {
				t.Fatalf("the builder's label %s is %q, want %q", l, o.Labels[l], b.Labels[l])
			}
		}
	}},
	{"a delayed build stays unknown and holds its reference until it lands, then settles applied", func(t *testing.T, f Fixture) {
		b := f.Images().Build()
		land := f.Images().DelayBuild()
		g := &gate{}
		if _, err := f.Driver().BuildImage(t.Context(), b, g.call); !errors.Is(err, runtimedriver.ErrUnknownOutcome) || g.count() != 1 {
			t.Fatalf("delayed BuildImage = %v after %d gate calls, want ErrUnknownOutcome after 1", err, g.count())
		}
		effect := runtimedriver.Effect{Kind: runtimedriver.EffectBuild, Build: b}
		wantSettled(t, f, effect, runtimedriver.Unknown)
		if _, err := f.Driver().BuildImage(t.Context(), b, unexpectedGate(t)); err == nil {
			t.Fatal("a second build of a reference whose build is unsettled succeeded")
		}
		land()
		if err := f.Driver().Delete(t.Context(), b.Ref, "", func() error { return nil }); err != nil {
			t.Fatalf("Delete of the build's leftovers: %v", err)
		}
		img, found := imageOfBuild(t, f, b.Ref)
		if !found || !maps.Equal(img.Labels, b.Labels) {
			t.Fatalf("after the build landed and its leftovers went, Images lists %+v (found %t)", img, found)
		}
		wantSettled(t, f, effect, runtimedriver.Applied)
		other := effect
		other.Build.Labels = maps.Clone(b.Labels)
		other.Build.Labels[runtimedriver.LabelProject] += "-other"
		if got := settle(t, f, other); got == runtimedriver.Applied {
			t.Fatal("Settle(build) with other labels than the image's = applied")
		}
	}},
}

func built(t *testing.T, f Fixture) runtimedriver.Image {
	t.Helper()
	img, err := f.Driver().BuildImage(t.Context(), f.Images().Build(), func() error { return nil })
	if err != nil {
		t.Fatalf("BuildImage: %v", err)
	}
	return img
}

// imageOfBuild finds the listed image labeled with the build reference.
func imageOfBuild(t *testing.T, f Fixture, r runtimedriver.Ref) (runtimedriver.Image, bool) {
	t.Helper()
	images, err := f.Driver().Images(t.Context())
	if err != nil {
		t.Fatalf("Images: %v", err)
	}
	for _, img := range images {
		if img.Labels[runtimedriver.LabelBuild] == r.ID {
			return img, true
		}
	}
	return runtimedriver.Image{}, false
}
