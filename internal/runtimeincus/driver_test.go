package runtimeincus

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/lgvo/p.ai/internal/runtimedriver"
)

func TestImageBuilderVersionNamesTheBuildRecipe(t *testing.T) {
	const want = "p.incus-commit-image/v1+none;2cpu,4GiB,512proc,8GiB;nix-2.34.8+rules/1;p.nix-activation/v1;p.nix-print-dev-env/1"
	// Changing the Nix rules changes how images are built: bump
	// builderNixRulesVersion and pin the new rules here.
	if ImageBuilderVersion != want || builderNixRules != "pure-eval;accept-flake-config=false;flake-registry=empty;no-lock-update;no-lock-write;sandbox=false" {
		t.Fatalf("the build recipe changed: bump its version deliberately (%q)", ImageBuilderVersion)
	}
	if !validBuilderVersion(ImageBuilderVersion) || strings.ContainsFunc(ImageBuilderVersion, func(r rune) bool { return !unicode.IsPrint(r) }) {
		t.Fatalf("builder version is not a bounded printable label: %q", ImageBuilderVersion)
	}
	for _, network := range []string{"public", "offline", "egress", "substituters"} {
		if strings.Contains(ImageBuilderVersion, network) {
			t.Fatalf("builder version names the builder's network: %q", ImageBuilderVersion)
		}
	}
	if got := NewDriver(&Backend{}).Describe(); got.ImageBuilder != ImageBuilderVersion {
		t.Fatalf("describe: %+v", got)
	}
}

func testBuild(t *testing.T, r Builder) runtimedriver.Build {
	t.Helper()
	return runtimedriver.Build{
		Ref:    runtimedriver.Ref{Kind: runtimedriver.KindBuild, ID: r.RequestUUID},
		Labels: testImageLabels(r),
		Source: runtimedriver.SourceExport{Bundle: writeBundle(t, []byte("# v2 git bundle\n")), Ref: "refs/p/source", Commit: r.CommitOID},
	}
}

// unsafeOptGuest makes the transfer after init fail: /opt is writable.
func unsafeOptGuest() *fakeBuilderFiles {
	return &fakeBuilderFiles{files: map[string]guestFile{"/": {typ: "directory", mode: 0755}, "/opt": {typ: "directory", mode: 0777}}}
}

func TestBuildImageGatesInitAndCleansUpADefiniteFailure(t *testing.T) {
	r := testBuilder()
	f := &fakeBuilderIncus{request: r}
	b := guestBackend(t, f, unsafeOptGuest())
	gates := 0
	_, err := NewDriver(b).BuildImage(context.Background(), testBuild(t, r), func() error {
		gates++
		if len(f.mutations) != 0 {
			t.Fatalf("gate after an effect: %v", f.mutations)
		}
		return nil
	})
	if err == nil || errors.Is(err, runtimedriver.ErrUnknownOutcome) || gates != 1 {
		t.Fatalf("failed build: gates=%d err=%v", gates, err)
	}
	if f.exists || !slices.Equal(f.mutations, []string{"init", "delete"}) {
		t.Fatalf("definite failure left its builder: %v", f.mutations)
	}
}

// The builder's first boot is followed by the clone, whose tools live under
// /run/current-system, which the boot's activation creates; later boots are
// followed by the Nix daemon's own readiness check.
func TestBuildClonesOnlyAfterTheBuildersFirstBoot(t *testing.T) {
	r := testBuilder()
	f := &fakeBuilderIncus{request: r, exists: true, status: "Stopped", rootLimit: true}
	b := guestBackend(t, f, &fakeBuilderFiles{files: map[string]guestFile{"/": {typ: "directory", mode: 0755}}})
	var steps []string
	b.runBuilder = func(_ context.Context, _ string, argv, _ []string) ([]byte, error) {
		guest := argv[slices.Index(argv, "--")+1:]
		if guest[0] == "/bin/sh" && strings.Contains(guest[len(guest)-1], "test -S /run/systemd/private") {
			steps = append(steps, "boot-wait")
			return nil, nil
		}
		steps = append(steps, guest[0])
		return nil, errors.New("fixture ends the build here")
	}
	publishing := false
	if _, err := b.buildCommitImage(context.Background(), r, testBuild(t, r), "x86_64-linux", &publishing); err == nil {
		t.Fatal("the fixture's end went unnoticed")
	}
	if !slices.Equal(f.mutations, []string{"start"}) || !slices.Equal(steps, []string{"boot-wait", "/run/current-system/sw/bin/ls"}) {
		t.Fatalf("clone before the first boot ended: mutations %v, guest steps %v", f.mutations, steps)
	}
}

func TestBuildImageWithACleanupFailureIsUnknown(t *testing.T) {
	r := testBuilder()
	f := &fakeBuilderIncus{request: r}
	b := guestBackend(t, f, unsafeOptGuest())
	b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
		if argv[3] == "delete" {
			return nil, errors.New("Incus refused the delete")
		}
		return f.run(ctx, binary, argv, env)
	}
	if _, err := NewDriver(b).BuildImage(context.Background(), testBuild(t, r), func() error { return nil }); !errors.Is(err, runtimedriver.ErrUnknownOutcome) || !f.exists {
		t.Fatalf("an unproven cleanup was reported definite: exists=%t %v", f.exists, err)
	}
}

func TestBuildImageRefusesBeforeItsGate(t *testing.T) {
	r := testBuilder()
	otherUUID := "22222222-2222-4222-8222-222222222222"
	for _, tc := range []struct {
		name     string
		edit     func(*testing.T, *runtimedriver.Build, *fakeBuilderIncus, *[]builderImageJSON)
		mismatch bool
	}{
		{name: "builder at the name", mismatch: true, edit: func(_ *testing.T, _ *runtimedriver.Build, f *fakeBuilderIncus, _ *[]builderImageJSON) {
			f.exists, f.status, f.rootLimit = true, "Stopped", true
		}},
		{name: "foreign instance at the name", mismatch: true, edit: func(_ *testing.T, _ *runtimedriver.Build, f *fakeBuilderIncus, _ *[]builderImageJSON) {
			f.exists, f.status, f.foreign = true, "Running", true
		}},
		{name: "image already carries the build", mismatch: true, edit: func(_ *testing.T, _ *runtimedriver.Build, _ *fakeBuilderIncus, images *[]builderImageJSON) {
			*images = append(*images, builderImageJSON{Fingerprint: strings.Repeat("e", 64), Type: "container", Properties: map[string]string{"p.instance": testInstance, "p.build": r.RequestUUID}})
		}},
		{name: "other builder version", edit: func(_ *testing.T, b *runtimedriver.Build, _ *fakeBuilderIncus, _ *[]builderImageJSON) {
			b.Labels[runtimedriver.LabelBuilderVersion] = "older"
		}},
		{name: "build label differs from the reference", edit: func(_ *testing.T, b *runtimedriver.Build, _ *fakeBuilderIncus, _ *[]builderImageJSON) {
			b.Labels[runtimedriver.LabelBuild] = otherUUID
		}},
		{name: "extra label", edit: func(_ *testing.T, b *runtimedriver.Build, _ *fakeBuilderIncus, _ *[]builderImageJSON) {
			b.Labels["branch"] = "main"
		}},
		{name: "system other than the host's", edit: func(_ *testing.T, b *runtimedriver.Build, _ *fakeBuilderIncus, _ *[]builderImageJSON) {
			b.Labels[runtimedriver.LabelSystem] = "aarch64-linux"
		}},
		{name: "not a build reference", edit: func(_ *testing.T, b *runtimedriver.Build, _ *fakeBuilderIncus, _ *[]builderImageJSON) {
			b.Ref.Kind = "session"
		}},
		{name: "bundle of another commit", edit: func(_ *testing.T, b *runtimedriver.Build, _ *fakeBuilderIncus, _ *[]builderImageJSON) {
			b.Source.Commit = strings.Repeat("c", 40)
		}},
		{name: "bundle missing", edit: func(_ *testing.T, b *runtimedriver.Build, _ *fakeBuilderIncus, _ *[]builderImageJSON) {
			b.Source.Bundle += ".missing"
		}},
		{name: "public network without the substrate", edit: func(_ *testing.T, b *runtimedriver.Build, _ *fakeBuilderIncus, _ *[]builderImageJSON) {
			b.Network.PublicIPv4 = "10.233.0.11/24"
		}},
		{name: "base image missing", edit: func(_ *testing.T, _ *runtimedriver.Build, _ *fakeBuilderIncus, images *[]builderImageJSON) {
			*images = nil
		}},
		// The creation checks again right before init; the gate follows them.
		{name: "base image removed after the checks", edit: func(*testing.T, *runtimedriver.Build, *fakeBuilderIncus, *[]builderImageJSON) {}},
		{name: "unproven pool", edit: func(_ *testing.T, _ *runtimedriver.Build, f *fakeBuilderIncus, _ *[]builderImageJSON) {
			f.poolDriver = "dir"
		}},
		{name: "unconfined project", edit: func(_ *testing.T, _ *runtimedriver.Build, f *fakeBuilderIncus, _ *[]builderImageJSON) {
			f.base.unsafeProfile = true
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeBuilderIncus{request: r}
			b := guestBackend(t, f, unsafeOptGuest())
			images := []builderImageJSON{{Fingerprint: testImage, Type: "container", Architecture: "x86_64"}}
			build := testBuild(t, r)
			tc.edit(t, &build, f, &images)
			lists := 0
			b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
				if slices.Equal(argv[3:], []string{"image", "list", "--format", "json"}) {
					lists++
					if tc.name == "base image removed after the checks" && lists > 2 {
						return []byte("[]"), nil
					}
					return json.Marshal(images)
				}
				return f.run(ctx, binary, argv, env)
			}
			before := slices.Clone(f.mutations)
			_, err := NewDriver(b).BuildImage(context.Background(), build, func() error { t.Fatal("gate after a failed check"); return nil })
			if err == nil || errors.Is(err, runtimedriver.ErrMismatch) != tc.mismatch || !slices.Equal(f.mutations, before) {
				t.Fatalf("refusal: mismatch=%t mutations=%v err=%v", errors.Is(err, runtimedriver.ErrMismatch), f.mutations, err)
			}
		})
	}
	f := &fakeBuilderIncus{request: r}
	b := guestBackend(t, f, unsafeOptGuest())
	refused := errors.New("core could not record its intent")
	if _, err := NewDriver(b).BuildImage(context.Background(), testBuild(t, r), func() error { return refused }); err != refused || len(f.mutations) != 0 {
		t.Fatalf("gate error not returned unchanged before any effect: %v %v", err, f.mutations)
	}
}

// ownImage is a commit image of this instance as Incus lists it.
func ownImage(t *testing.T, fingerprint string, labels runtimedriver.Labels, shell runtimedriver.Shell) builderImageJSON {
	t.Helper()
	properties := commitImageLabels(labels, shell, testCapture, testInstance, "user-1000")
	properties["os"] = "NixOS"
	return builderImageJSON{Fingerprint: fingerprint, Type: "container", Architecture: "x86_64", Project: "user-1000", Size: 4096, Properties: properties}
}

type imageIncus struct {
	f         *fakeBuilderIncus
	images    []builderImageJSON
	lists     int
	pending   int
	deletes   []string
	onList    func(lists int) // changes the listing between observations
	gone      bool            // a delete removes the image
	failList  bool
	inventory []instanceJSON // instances besides the builder fake's
}

func (i *imageIncus) backend() *Backend {
	b := builderBackend(i.f)
	b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
		args := argv[3:]
		switch {
		case slices.Equal(args, []string{"image", "list", "--format", "json"}):
			if i.failList {
				return nil, errors.New("Incus unavailable")
			}
			i.lists++
			if i.onList != nil {
				i.onList(i.lists)
			}
			return json.Marshal(i.images)
		case len(args) == 3 && args[0] == "image" && args[1] == "delete":
			i.deletes = append(i.deletes, args[2])
			if i.gone {
				i.images = slices.DeleteFunc(i.images, func(image builderImageJSON) bool { return image.Fingerprint == args[2] })
			}
			return nil, errors.New("client lost the reply")
		case slices.Equal(args, []string{"list", "--format", "json"}) && i.inventory != nil:
			var instances []instanceJSON
			raw, err := i.f.run(ctx, binary, argv, env)
			if err != nil || json.Unmarshal(raw, &instances) != nil {
				return nil, errors.New("fixture inventory")
			}
			return json.Marshal(append(instances, i.inventory...))
		case slices.Equal(args, []string{"operation", "list", "--format", "json"}):
			operations := []map[string]string{}
			for range i.pending {
				operations = append(operations, map[string]string{"description": "Downloading image", "status": "Running"})
			}
			return json.Marshal(operations)
		}
		return i.f.run(ctx, binary, argv, env)
	}
	return b
}

func TestCommitImageLabelsRoundTrip(t *testing.T) {
	r := testBuilder()
	for _, shell := range []runtimedriver.Shell{
		{Selection: runtimedriver.SelectionNoFlake},
		{Selection: runtimedriver.SelectionNoDefault},
		{Selection: runtimedriver.SelectionDevShell, MaterialSHA256: strings.Repeat("d", 64)},
	} {
		image := ownImage(t, strings.Repeat("e", 64), testImageLabels(r), shell)
		if _, ok := image.Properties["p.material"]; ok != (shell.Selection == runtimedriver.SelectionDevShell) {
			t.Fatalf("%s image carries activation labels %t", shell.Selection, ok)
		}
		i := &imageIncus{f: &fakeBuilderIncus{request: r}, images: []builderImageJSON{image}}
		listed, err := NewDriver(i.backend()).Images(context.Background())
		if err != nil || len(listed) != 1 || listed[0].Ref != runtimedriver.ImageRef(image.Fingerprint) || listed[0].Size != 4096 ||
			!maps.Equal(listed[0].Labels, testImageLabels(r)) || listed[0].Shell != shell {
			t.Fatalf("%s labels did not round-trip: %+v %v", shell.Selection, listed, err)
		}
	}
}

func TestImagesRefusesAMalformedOwnImage(t *testing.T) {
	r := testBuilder()
	good := ownImage(t, strings.Repeat("e", 64), testImageLabels(r), runtimedriver.Shell{Selection: runtimedriver.SelectionNoFlake})
	foreign := ownImage(t, strings.Repeat("f", 64), testImageLabels(r), runtimedriver.Shell{Selection: runtimedriver.SelectionNoFlake})
	foreign.Properties["p.instance"] = "22222222-2222-4222-8222-222222222222"
	foreign.Properties["p.selection"] = "garbage" // another instance's image is never read
	base := builderImageJSON{Fingerprint: testImage, Type: "container", Architecture: "x86_64", Properties: map[string]string{"os": "NixOS"}}
	i := &imageIncus{f: &fakeBuilderIncus{request: r}, images: []builderImageJSON{base, good, foreign}}
	listed, err := NewDriver(i.backend()).Images(context.Background())
	if err != nil || len(listed) != 1 || listed[0].Ref != runtimedriver.ImageRef(good.Fingerprint) {
		t.Fatalf("own images: %+v %v", listed, err)
	}
	for name, edit := range map[string]func(*builderImageJSON){
		"old contract":       func(x *builderImageJSON) { x.Properties["p.contract"] = "p.incus-system-image/v2" },
		"no build label":     func(x *builderImageJSON) { delete(x.Properties, "p.build") },
		"unknown P label":    func(x *builderImageJSON) { x.Properties["p.environment_key"] = strings.Repeat("a", 64) },
		"devShell no digest": func(x *builderImageJSON) { x.Properties["p.selection"] = "devshell" },
		"bad commit":         func(x *builderImageJSON) { x.Properties["p.source_commit"] = "main" },
		"other Incus project": func(x *builderImageJSON) {
			x.Properties["p.incus_project"] = "default"
		},
		"public": func(x *builderImageJSON) { x.Public = true },
		"alias": func(x *builderImageJSON) {
			x.Aliases = []struct {
				Name string `json:"name"`
			}{{Name: "latest"}}
		},
	} {
		bad := ownImage(t, strings.Repeat("c", 64), testImageLabels(r), runtimedriver.Shell{Selection: runtimedriver.SelectionNoFlake})
		edit(&bad)
		i.images = []builderImageJSON{base, good, bad}
		if listed, err := NewDriver(i.backend()).Images(context.Background()); err == nil || !errors.Is(err, runtimedriver.ErrMismatch) {
			t.Fatalf("%s: listed a malformed own image partially: %+v %v", name, listed, err)
		}
	}
	i.failList = true
	if _, err := NewDriver(i.backend()).Images(context.Background()); err == nil {
		t.Fatal("unreachable Incus listed as empty")
	}
}

func TestInspectImageReadsOneReference(t *testing.T) {
	r := testBuilder()
	good := ownImage(t, strings.Repeat("e", 64), testImageLabels(r), runtimedriver.Shell{Selection: runtimedriver.SelectionNoDefault})
	i := &imageIncus{f: &fakeBuilderIncus{request: r}, images: []builderImageJSON{good}}
	d := NewDriver(i.backend())
	ctx := context.Background()
	if img, found, err := d.InspectImage(ctx, runtimedriver.ImageRef(good.Fingerprint)); err != nil || !found || !maps.Equal(img.Labels, testImageLabels(r)) {
		t.Fatalf("own image: %+v %t %v", img, found, err)
	}
	if _, found, err := d.InspectImage(ctx, runtimedriver.ImageRef(strings.Repeat("c", 64))); err != nil || found {
		t.Fatalf("absent image: %t %v", found, err)
	}
	i.images = append(i.images, builderImageJSON{Fingerprint: testImage, Type: "container", Architecture: "x86_64", Properties: map[string]string{"os": "NixOS"}})
	if _, _, err := d.InspectImage(ctx, runtimedriver.ImageRef(testImage)); !errors.Is(err, runtimedriver.ErrMismatch) {
		t.Fatalf("an image P did not build was not a mismatch: %v", err)
	}
	if _, _, err := d.InspectImage(ctx, "latest"); err == nil {
		t.Fatal("accepted an alias as an image reference")
	}
}

func TestDeleteImageDeletesExactlyTheImage(t *testing.T) {
	r := testBuilder()
	ctx := context.Background()
	image := ownImage(t, strings.Repeat("e", 64), testImageLabels(r), runtimedriver.Shell{Selection: runtimedriver.SelectionNoFlake})
	img := runtimedriver.Image{Ref: runtimedriver.ImageRef(image.Fingerprint), Labels: testImageLabels(r)}
	gates := 0
	gate := func() error { gates++; return nil }

	i := &imageIncus{f: &fakeBuilderIncus{request: r}}
	if err := NewDriver(i.backend()).DeleteImage(ctx, img, gate); err != nil || gates != 0 || len(i.deletes) != 0 {
		t.Fatalf("absent image: gates=%d deletes=%q %v", gates, i.deletes, err)
	}
	other := img
	other.Labels = maps.Clone(img.Labels)
	other.Labels[runtimedriver.LabelSourceCommit] = strings.Repeat("c", 40)
	i = &imageIncus{f: &fakeBuilderIncus{request: r}, images: []builderImageJSON{image}, gone: true}
	if err := NewDriver(i.backend()).DeleteImage(ctx, other, gate); !errors.Is(err, runtimedriver.ErrMismatch) || gates != 0 || len(i.deletes) != 0 {
		t.Fatalf("image with other labels: gates=%d deletes=%q %v", gates, i.deletes, err)
	}
	refused := errors.New("intent not recorded")
	if err := NewDriver(i.backend()).DeleteImage(ctx, img, func() error { return refused }); err != refused || len(i.deletes) != 0 {
		t.Fatalf("gate refusal: deletes=%q %v", i.deletes, err)
	}
	// The client lost the reply, but the image is gone: done.
	if err := NewDriver(i.backend()).DeleteImage(ctx, img, gate); err != nil || gates != 1 || !slices.Equal(i.deletes, []string{image.Fingerprint}) {
		t.Fatalf("exact delete: gates=%d deletes=%q %v", gates, i.deletes, err)
	}
	i = &imageIncus{f: &fakeBuilderIncus{request: r}, images: []builderImageJSON{image}}
	if err := NewDriver(i.backend()).DeleteImage(ctx, img, gate); !errors.Is(err, runtimedriver.ErrUnknownOutcome) || gates != 2 {
		t.Fatalf("image still there after its delete: gates=%d %v", gates, err)
	}
}

func TestSettleBuildOnlyFromTwoMatchingObservations(t *testing.T) {
	interval := settleInterval
	settleInterval = time.Millisecond
	t.Cleanup(func() { settleInterval = interval })
	r := testBuilder()
	labels := testImageLabels(r)
	effect := runtimedriver.Effect{Kind: runtimedriver.EffectBuild, Build: runtimedriver.Build{Ref: runtimedriver.Ref{Kind: runtimedriver.KindBuild, ID: r.RequestUUID}, Labels: labels}}
	image := ownImage(t, strings.Repeat("e", 64), labels, runtimedriver.Shell{Selection: runtimedriver.SelectionNoFlake})
	base := builderImageJSON{Fingerprint: testImage, Type: "container", Architecture: "x86_64", Size: 1, Properties: map[string]string{"os": "NixOS"}}
	otherBuild := maps.Clone(labels)
	otherBuild[runtimedriver.LabelBuild] = "22222222-2222-4222-8222-222222222222"
	for _, tc := range []struct {
		name string
		set  func(*imageIncus)
		want runtimedriver.Settlement
	}{
		{"published, builder gone", func(*imageIncus) {}, runtimedriver.Applied},
		// Only a build whose publication was accepted removes its builder.
		{"published, builder stopped", func(i *imageIncus) { i.f.exists, i.f.status = true, "Stopped" }, runtimedriver.Unknown},
		{"published, builder renamed", func(i *imageIncus) {
			config := map[string]string{"user.p.instance": testInstance, "user.p.builder_request_uuid": r.RequestUUID}
			i.inventory = []instanceJSON{{Name: "renamed-builder", Type: "container", Status: "Stopped", Config: config, ExpandedConfig: config}}
		}, runtimedriver.Unknown},
		{"builder still running", func(i *imageIncus) { i.f.exists, i.f.status = true, "Running" }, runtimedriver.Unknown},
		{"image operation running", func(i *imageIncus) { i.pending = 1 }, runtimedriver.Unknown},
		{"no image yet", func(i *imageIncus) { i.images = nil }, runtimedriver.Unknown},
		{"another build's image", func(i *imageIncus) {
			i.images = []builderImageJSON{base, ownImage(t, strings.Repeat("e", 64), otherBuild, runtimedriver.Shell{Selection: runtimedriver.SelectionNoFlake})}
		}, runtimedriver.Unknown},
		{"two images of the build", func(i *imageIncus) {
			i.images = append(i.images, ownImage(t, strings.Repeat("c", 64), labels, runtimedriver.Shell{Selection: runtimedriver.SelectionNoFlake}))
		}, runtimedriver.Unknown},
		{"labels differ", func(i *imageIncus) { i.images[0].Properties["p.source_commit"] = strings.Repeat("c", 40) }, runtimedriver.Unknown},
		// Settling accepts only what publication would have accepted.
		{"inherited property changed", func(i *imageIncus) { i.images[0].Properties["os"] = "tampered" }, runtimedriver.Unknown},
		{"extra inherited property", func(i *imageIncus) { i.images[0].Properties["release"] = "other" }, runtimedriver.Unknown},
		{"base image gone", func(i *imageIncus) { i.images = i.images[:1] }, runtimedriver.Unknown},
		{"image changed between observations", func(i *imageIncus) {
			i.onList = func(lists int) {
				if lists == 2 {
					i.images = []builderImageJSON{base, ownImage(t, strings.Repeat("c", 64), labels, runtimedriver.Shell{Selection: runtimedriver.SelectionNoFlake})}
				}
			}
		}, runtimedriver.Unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := &imageIncus{f: &fakeBuilderIncus{request: r}, images: []builderImageJSON{image, base}}
			i.images[0].Properties = maps.Clone(image.Properties)
			tc.set(i)
			got, err := NewDriver(i.backend()).Settle(context.Background(), effect)
			if err != nil || got != tc.want {
				t.Fatalf("settled %s, want %s: %v", got, tc.want, err)
			}
		})
	}
	i := &imageIncus{f: &fakeBuilderIncus{request: r}, failList: true}
	if got, err := NewDriver(i.backend()).Settle(context.Background(), effect); err == nil || got != runtimedriver.Unknown {
		t.Fatalf("unreachable Incus settled %s: %v", got, err)
	}
}

func TestSettleImageDeleteAndUnsupportedEffects(t *testing.T) {
	interval := settleInterval
	settleInterval = time.Millisecond
	t.Cleanup(func() { settleInterval = interval })
	r := testBuilder()
	image := ownImage(t, strings.Repeat("e", 64), testImageLabels(r), runtimedriver.Shell{Selection: runtimedriver.SelectionNoFlake})
	effect := runtimedriver.Effect{Kind: runtimedriver.EffectDeleteImage, Image: runtimedriver.Image{Ref: runtimedriver.ImageRef(image.Fingerprint)}}
	i := &imageIncus{f: &fakeBuilderIncus{request: r}}
	if got, err := NewDriver(i.backend()).Settle(context.Background(), effect); err != nil || got != runtimedriver.Applied {
		t.Fatalf("deleted image: %s %v", got, err)
	}
	i = &imageIncus{f: &fakeBuilderIncus{request: r}, images: []builderImageJSON{image}}
	if got, err := NewDriver(i.backend()).Settle(context.Background(), effect); err != nil || got != runtimedriver.Unknown {
		t.Fatalf("present image: %s %v", got, err)
	}
	i = &imageIncus{f: &fakeBuilderIncus{request: r}}
	i.onList = func(lists int) {
		if lists == 2 {
			i.images = []builderImageJSON{image}
		}
	}
	if got, err := NewDriver(i.backend()).Settle(context.Background(), effect); err != nil || got != runtimedriver.Unknown {
		t.Fatalf("image seen on the second listing: %s %v", got, err)
	}
	if _, err := NewDriver(i.backend()).Settle(context.Background(), runtimedriver.Effect{Kind: "create"}); !errors.Is(err, runtimedriver.ErrUnsupported) {
		t.Fatalf("unsupported effect: %v", err)
	}
}

func TestDeleteRemovesOnlyThisInstancesBuilder(t *testing.T) {
	r := testBuilder()
	ctx := context.Background()
	ref := runtimedriver.Ref{Kind: runtimedriver.KindBuild, ID: r.RequestUUID}
	if err := NewDriver(builderBackend(&fakeBuilderIncus{request: r})).Delete(ctx, runtimedriver.Ref{Kind: "session", ID: r.RequestUUID}, func() error { return nil }); !errors.Is(err, runtimedriver.ErrUnsupported) {
		t.Fatalf("session delete: %v", err)
	}
	gates := 0
	gate := func() error { gates++; return nil }
	f := &fakeBuilderIncus{request: r}
	if err := NewDriver(builderBackend(f)).Delete(ctx, ref, gate); err != nil || gates != 0 || len(f.mutations) != 0 {
		t.Fatalf("absent builder: gates=%d %v %v", gates, f.mutations, err)
	}
	for name, f := range map[string]*fakeBuilderIncus{
		"another build's builder": {request: r, exists: true, status: "Running", foreign: true},
		"another instance's":      {request: r, exists: true, status: "Stopped", instance: "22222222-2222-4222-8222-222222222222"},
	} {
		if err := NewDriver(builderBackend(f)).Delete(ctx, ref, gate); !errors.Is(err, runtimedriver.ErrMismatch) || gates != 0 || len(f.mutations) != 0 {
			t.Fatalf("%s: gates=%d %v %v", name, gates, f.mutations, err)
		}
	}
	f = &fakeBuilderIncus{request: r, exists: true, status: "Running"}
	refused := errors.New("intent not recorded")
	if err := NewDriver(builderBackend(f)).Delete(ctx, ref, func() error { return refused }); err != refused || len(f.mutations) != 0 {
		t.Fatalf("gate refusal: %v %v", f.mutations, err)
	}
	// A leftover builder may be unfinished: no root limit, no NIC. Delete
	// needs only its labels.
	if err := NewDriver(builderBackend(f)).Delete(ctx, ref, gate); err != nil || gates != 1 || f.exists || !slices.Equal(f.mutations, []string{"stop", "delete"}) {
		t.Fatalf("owned builder: gates=%d exists=%t %v %v", gates, f.exists, f.mutations, err)
	}
	f = &fakeBuilderIncus{request: r, exists: true, status: "Stopped"}
	b := builderBackend(f)
	b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
		if argv[3] == "delete" {
			return nil, errors.New("Incus refused the delete")
		}
		return f.run(ctx, binary, argv, env)
	}
	if err := NewDriver(b).Delete(ctx, ref, gate); err == nil || !f.exists {
		t.Fatalf("remaining builder reported deleted: %v", err)
	}
	for _, named := range []bool{false, true} {
		f, b := renamedBuilderIncus(r, named)
		before := gates
		if err := NewDriver(b).Delete(ctx, ref, gate); !errors.Is(err, runtimedriver.ErrMismatch) || gates != before || len(f.mutations) != 0 {
			t.Fatalf("renamed builder (also at its name: %t) passed for absent: gates=%d %v %v", named, gates, f.mutations, err)
		}
	}
}

// renamedBuilderIncus holds the build's builder under another name, and,
// when named, also an owned builder at the build's name.
func renamedBuilderIncus(r Builder, named bool) (*fakeBuilderIncus, *Backend) {
	f := &fakeBuilderIncus{request: r, exists: named, status: "Stopped", rootLimit: true}
	b := builderBackend(f)
	b.run = func(ctx context.Context, binary string, argv, env []string) ([]byte, error) {
		if slices.Equal(argv[3:], []string{"list", "--format", "json"}) {
			var instances []instanceJSON
			if named {
				raw, err := f.run(ctx, binary, argv, env)
				if err != nil || json.Unmarshal(raw, &instances) != nil {
					return nil, errors.New("fixture inventory")
				}
			}
			config := map[string]string{"user.p.instance": testInstance, "user.p.builder_request_uuid": r.RequestUUID}
			instances = append(instances, instanceJSON{Name: "renamed-builder", Type: "container", Status: "Running", Config: config, ExpandedConfig: config})
			return json.Marshal(instances)
		}
		return f.run(ctx, binary, argv, env)
	}
	return f, b
}

func TestFailedBuildCleanupFindsARenamedBuilder(t *testing.T) {
	r := testBuilder()
	cause := errors.New("transfer failed")
	f, b := renamedBuilderIncus(r, false)
	if err := b.failedBuild(r, cause, false); !errors.Is(err, cause) || !errors.Is(err, runtimedriver.ErrUnknownOutcome) || len(f.mutations) != 0 {
		t.Fatalf("a renamed builder left a definite failure: %v %v", err, f.mutations)
	}
	f = &fakeBuilderIncus{request: r, exists: true, status: "Running", rootLimit: true}
	if err := builderBackend(f).failedBuild(r, cause, false); err != cause || f.exists {
		t.Fatalf("an owned builder's removal was not definite: %v %v", err, f.mutations)
	}
}
