package environmentnix

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lgvo/p.ai/internal/nixenv"
	"github.com/lgvo/p.ai/internal/plugin"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

type fakeNative struct {
	resolves, realizes             int
	publishes                      int
	publishError                   error
	baseOnly                       bool
	resolveStarted, resolveRelease chan struct{}
	realizeStarted, realizeRelease chan struct{}
}

func (f *fakeNative) PublishBuilderImage(_ context.Context, _ runtimeincus.Builder, result runtimeincus.BuilderNixResult) (nixenv.Handle, runtimeincus.BuilderImageInfo, error) {
	f.publishes++
	if f.publishError != nil || result.MaterialDigest != "native-material" {
		return nixenv.Handle{}, runtimeincus.BuilderImageInfo{}, errors.New("native image refused")
	}
	return nixenv.Handle{TargetKind: "incus-system-image", Locator: "fingerprint"}, runtimeincus.BuilderImageInfo{Fingerprint: "fingerprint"}, nil
}

func (f *fakeNative) PublishBuilderImageWithGate(ctx context.Context, r runtimeincus.Builder, result runtimeincus.BuilderNixResult, gate func(runtimeincus.BuilderImageClaim) error) (nixenv.Handle, runtimeincus.BuilderImageInfo, error) {
	if err := gate(runtimeincus.BuilderImageClaim{Key: result.Selection.KeyDigest, MaterialDigest: result.MaterialDigest}); err != nil {
		return nixenv.Handle{}, runtimeincus.BuilderImageInfo{}, err
	}
	return f.PublishBuilderImage(ctx, r, result)
}

func TestPipelinePublicationGateRefusalDoesNotPublish(t *testing.T) {
	active := pipelinePackage(t, "")
	native := &fakeNative{}
	p, err := newPipeline(active, native, runtimeincus.Builder{}, "x86_64-linux")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Resolve(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Realize(context.Background()); err != nil {
		t.Fatal(err)
	}
	called := 0
	if _, _, err := p.PublishPrivateImageWithGate(context.Background(), func(claim runtimeincus.BuilderImageClaim) error {
		called++
		if claim.Key != "native-key" || claim.MaterialDigest != "native-material" {
			t.Fatalf("native claim changed: %+v", claim)
		}
		return errors.New("durable write refused")
	}); err == nil || called != 1 || native.publishes != 0 {
		t.Fatalf("gate refusal published image: calls=%d publishes=%d err=%v", called, native.publishes, err)
	}
	if _, _, err := p.PublishPrivateImage(context.Background()); err == nil || native.publishes != 0 {
		t.Fatal("failed gated stage fell through to ungated publication")
	}
}

func (f *fakeNative) ResolveBuilderNix(ctx context.Context, r runtimeincus.Builder, system string) (runtimeincus.BuilderNixSelection, error) {
	f.resolves++
	if f.resolveStarted != nil {
		close(f.resolveStarted)
		select {
		case <-f.resolveRelease:
		case <-ctx.Done():
			return runtimeincus.BuilderNixSelection{}, ctx.Err()
		}
	}
	return runtimeincus.BuilderNixSelection{Builder: r, System: system, BaseOnly: f.baseOnly, KeyDigest: "native-key", DerivationPath: "/nix/store/native.drv"}, nil
}
func (f *fakeNative) RealizeBuilderNix(ctx context.Context, _ runtimeincus.Builder, selected runtimeincus.BuilderNixSelection) (runtimeincus.BuilderNixResult, error) {
	f.realizes++
	if f.realizeStarted != nil {
		close(f.realizeStarted)
		select {
		case <-f.realizeRelease:
		case <-ctx.Done():
			return runtimeincus.BuilderNixResult{}, ctx.Err()
		}
	}
	return runtimeincus.BuilderNixResult{Selection: selected, MaterialDigest: "native-material"}, nil
}

func pipelinePackage(t *testing.T, hostileMode string) plugin.Active {
	t.Helper()
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Skip("Go compiler unavailable")
	}
	dir := t.TempDir()
	manifest, err := os.ReadFile("../../plugins/bundled/environment-nix/plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"build", "-o", filepath.Join(dir, "environment.wasm")}
	source := "../../plugins/bundled/environment-nix"
	if hostileMode != "" {
		args = append(args, "-ldflags", "-X main.mode="+hostileMode)
		source = "../plugin/testdata/environment-hostile"
	}
	args = append(args, "./"+source)
	cmd := exec.Command(goPath, args...)
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0", "GOTOOLCHAIN=local")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build environment module: %v: %s", err, out)
	}
	pkg, err := plugin.Conformance(dir)
	if err != nil {
		t.Fatal(err)
	}
	return plugin.Active{Package: pkg, Grants: []string{"environment.nix"}}
}

func TestPipelineCommitsOnlyAcceptedNativeStages(t *testing.T) {
	active := pipelinePackage(t, "")
	native := &fakeNative{}
	p, err := newPipeline(active, native, runtimeincus.Builder{}, "x86_64-linux")
	if err != nil {
		t.Fatal(err)
	}
	selected, err := p.Resolve(context.Background())
	if err != nil || selected.KeyDigest != "native-key" {
		t.Fatalf("resolve: %+v %v", selected, err)
	}
	if _, ok := p.Result(); ok {
		t.Fatal("material visible before realization")
	}
	result, err := p.Realize(context.Background())
	if err != nil || result.MaterialDigest != "native-material" || native.resolves != 1 || native.realizes != 1 {
		t.Fatalf("realize: %+v %v native=%+v", result, err, native)
	}
	if _, ok := p.Result(); !ok {
		t.Fatal("accepted material not retained")
	}
	if _, err := p.Realize(context.Background()); err == nil || native.realizes != 1 {
		t.Fatal("repeated realization reached native backend")
	}
	handle, image, err := p.PublishPrivateImage(context.Background())
	if err != nil || handle.Locator != "fingerprint" || image.Fingerprint != handle.Locator || native.publishes != 1 {
		t.Fatalf("accepted publication: %+v %+v %v", handle, image, err)
	}
	if _, ok := p.Result(); ok {
		t.Fatal("published pipeline still exposes mutable builder material")
	}
	if got, _, ok := p.Image(); !ok || got.Locator != "fingerprint" {
		t.Fatal("published image handle not retained")
	}
}

func TestPipelineImageRequiresAcceptedMaterialAndDiscardsFailure(t *testing.T) {
	active := pipelinePackage(t, "")
	native := &fakeNative{}
	p, err := newPipeline(active, native, runtimeincus.Builder{}, "x86_64-linux")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.PublishPrivateImage(context.Background()); err == nil || native.publishes != 0 {
		t.Fatal("published before WASI stages")
	}
	if _, err := p.Resolve(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.PublishPrivateImage(context.Background()); err == nil || native.publishes != 0 {
		t.Fatal("published before accepted realization")
	}
	if _, err := p.Realize(context.Background()); err != nil {
		t.Fatal(err)
	}
	native.publishError = errors.New("native refused")
	if _, _, err := p.PublishPrivateImage(context.Background()); err == nil || native.publishes != 1 {
		t.Fatal("native publication failure was accepted")
	}
	if _, ok := p.Result(); ok {
		t.Fatal("failed publication retained material")
	}
	if _, _, ok := p.Image(); ok {
		t.Fatal("failed publication exposed handle")
	}
	if _, _, err := p.PublishPrivateImage(context.Background()); err == nil || native.publishes != 1 {
		t.Fatal("publication retried after uncertain failure")
	}
}

func TestPipelineDiscardsNativeEffectAfterBadWASIResult(t *testing.T) {
	for _, mode := range []string{"refuse-after", "extra-result"} {
		t.Run(mode, func(t *testing.T) {
			active := pipelinePackage(t, mode)
			native := &fakeNative{}
			p, err := newPipeline(active, native, runtimeincus.Builder{}, "x86_64-linux")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.Resolve(context.Background()); err == nil || native.resolves != 1 {
				t.Fatalf("effect/result mismatch: %v %+v", err, native)
			}
			if _, ok := p.Selection(); ok {
				t.Fatal("failed module exposed selection")
			}
			if _, err := p.Realize(context.Background()); err == nil || native.realizes != 0 {
				t.Fatal("failed module allowed realization")
			}
		})
	}
}

func TestPipelineBaseOnlyDoesNotRealize(t *testing.T) {
	active := pipelinePackage(t, "")
	native := &fakeNative{baseOnly: true}
	p, err := newPipeline(active, native, runtimeincus.Builder{}, "x86_64-linux")
	if err != nil {
		t.Fatal(err)
	}
	choice, err := p.Resolve(context.Background())
	if err != nil || !choice.BaseOnly {
		t.Fatalf("base selection: %+v %v", choice, err)
	}
	if _, err := p.Realize(context.Background()); err == nil || native.realizes != 0 {
		t.Fatal("base-only reached native realization")
	}
}

func TestPipelineInFlightGettersAndConcurrentAttemptReturnPromptly(t *testing.T) {
	active := pipelinePackage(t, "")
	native := &fakeNative{resolveStarted: make(chan struct{}), resolveRelease: make(chan struct{})}
	release := sync.OnceFunc(func() { close(native.resolveRelease) })
	defer release()
	p, err := newPipeline(active, native, runtimeincus.Builder{}, "x86_64-linux")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := p.Resolve(context.Background()); done <- err }()
	select {
	case <-native.resolveStarted:
	case <-time.After(time.Second):
		t.Fatal("native resolve never started")
	}
	probe := make(chan error, 1)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	go func() {
		if _, ok := p.Selection(); ok {
			probe <- errors.New("in-flight selection visible")
			return
		}
		_, err := p.Resolve(cancelled)
		if err == nil {
			probe <- errors.New("cancelled concurrent resolve accepted")
			return
		}
		_, err = p.Realize(cancelled)
		probe <- err
	}()
	select {
	case err := <-probe:
		if err == nil {
			t.Fatal("concurrent resolve accepted")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("getter or concurrent resolve blocked behind native effect")
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("resolve did not finish")
	}
	cancelNative := &fakeNative{resolveStarted: make(chan struct{}), resolveRelease: make(chan struct{})}
	cancelPipeline, err := newPipeline(active, cancelNative, runtimeincus.Builder{}, "x86_64-linux")
	if err != nil {
		t.Fatal(err)
	}
	cancelCtx, cancelRunning := context.WithCancel(context.Background())
	defer cancelRunning()
	go func() { _, err := cancelPipeline.Resolve(cancelCtx); done <- err }()
	select {
	case <-cancelNative.resolveStarted:
	case <-time.After(time.Second):
		t.Fatal("cancellable native resolve never started")
	}
	cancelRunning()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled native resolve accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled native resolve remained blocked")
	}
	if _, ok := cancelPipeline.Selection(); ok {
		t.Fatal("cancelled resolution retained a selection")
	}
}
