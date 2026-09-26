// Package environmentnix binds the executable environment capability to one
// core-selected native Incus builder and one system. WASI receives no builder
// identity, source path, derivation, or activation material.
package environmentnix

import (
	"context"
	"errors"
	"maps"
	"sync"

	"github.com/lgvo/p.ai/internal/nixenv"
	"github.com/lgvo/p.ai/internal/plugin"
	"github.com/lgvo/p.ai/internal/runtimeincus"
)

type phase uint8

const (
	phaseFresh phase = iota
	phaseResolving
	phaseResolved
	phaseRealizing
	phaseRealized
	phasePublishing
	phasePublished
	phaseFailed
)

// Pipeline is single-use. Its native values are committed only after a
// digest-verified module returns a valid ready result for the required effect.
type Pipeline struct {
	mu       sync.Mutex
	selected plugin.Active
	backend  nativeBackend
	builder  runtimeincus.Builder
	system   string
	phase    phase
	choice   runtimeincus.BuilderNixSelection
	result   runtimeincus.BuilderNixResult
	handle   nixenv.Handle
	image    runtimeincus.BuilderImageInfo
}

type imageBackend interface {
	PublishBuilderImage(context.Context, runtimeincus.Builder, runtimeincus.BuilderNixResult) (nixenv.Handle, runtimeincus.BuilderImageInfo, error)
}

type gatedImageBackend interface {
	PublishBuilderImageWithGate(context.Context, runtimeincus.Builder, runtimeincus.BuilderNixResult, func(runtimeincus.BuilderImageClaim) error) (nixenv.Handle, runtimeincus.BuilderImageInfo, error)
}

type nativeBackend interface {
	ResolveBuilderNix(context.Context, runtimeincus.Builder, string) (runtimeincus.BuilderNixSelection, error)
	RealizeBuilderNix(context.Context, runtimeincus.Builder, runtimeincus.BuilderNixSelection) (runtimeincus.BuilderNixResult, error)
}

func New(selected plugin.Active, backend *runtimeincus.Backend, builder runtimeincus.Builder, system string) (*Pipeline, error) {
	if backend == nil {
		return nil, errors.New("native environment backend required")
	}
	return newPipeline(selected, backend, builder, system)
}

func newPipeline(selected plugin.Active, backend nativeBackend, builder runtimeincus.Builder, system string) (*Pipeline, error) {
	if backend == nil || system != "x86_64-linux" && system != "aarch64-linux" {
		return nil, errors.New("invalid native environment binding")
	}
	return &Pipeline{selected: selected, backend: backend, builder: builder, system: system}, nil
}

// Resolve is the cache boundary: callers may inspect the native selection
// and its key before deciding whether a later Realize call is necessary.
func (p *Pipeline) Resolve(ctx context.Context) (runtimeincus.BuilderNixSelection, error) {
	p.mu.Lock()
	if p.phase != phaseFresh {
		p.mu.Unlock()
		return runtimeincus.BuilderNixSelection{}, errors.New("environment resolution already attempted")
	}
	p.phase = phaseResolving
	p.mu.Unlock()
	effect := &pendingEffect{backend: p.backend, builder: p.builder, system: p.system}
	if err := plugin.RunEnvironment(ctx, p.selected, "environment.resolve", effect); err != nil {
		p.mu.Lock()
		p.phase = phaseFailed
		p.mu.Unlock()
		return runtimeincus.BuilderNixSelection{}, err
	}
	if !effect.resolved {
		p.mu.Lock()
		p.phase = phaseFailed
		p.mu.Unlock()
		return runtimeincus.BuilderNixSelection{}, errors.New("environment resolver skipped native effect")
	}
	p.mu.Lock()
	p.choice = effect.choice
	p.phase = phaseResolved
	choice := p.choice
	p.mu.Unlock()
	return choice, nil
}

// Realize uses exactly the accepted selection. The native backend rechecks
// builder identity and the Nix derivation before producing material.
func (p *Pipeline) Realize(ctx context.Context) (runtimeincus.BuilderNixResult, error) {
	p.mu.Lock()
	if p.phase != phaseResolved || p.choice.BaseOnly {
		p.mu.Unlock()
		return runtimeincus.BuilderNixResult{}, errors.New("accepted devShell selection required")
	}
	selected := p.choice
	p.choice = runtimeincus.BuilderNixSelection{}
	p.phase = phaseRealizing
	p.mu.Unlock()
	effect := &pendingEffect{backend: p.backend, builder: p.builder, system: p.system, choice: selected, selected: true}
	if err := plugin.RunEnvironment(ctx, p.selected, "environment.realize", effect); err != nil {
		p.mu.Lock()
		p.phase = phaseFailed
		p.mu.Unlock()
		return runtimeincus.BuilderNixResult{}, err
	}
	if !effect.realized || effect.result.MaterialDigest == "" || effect.result.Selection.KeyDigest != selected.KeyDigest {
		p.mu.Lock()
		p.phase = phaseFailed
		p.mu.Unlock()
		return runtimeincus.BuilderNixResult{}, errors.New("environment realization postcondition failed")
	}
	p.mu.Lock()
	p.choice = selected
	p.result = effect.result
	p.phase = phaseRealized
	result := p.result
	p.mu.Unlock()
	return result, nil
}

func (p *Pipeline) Selection() (runtimeincus.BuilderNixSelection, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.phase != phaseResolved && p.phase != phaseRealized {
		return runtimeincus.BuilderNixSelection{}, false
	}
	return p.choice, true
}

func (p *Pipeline) Result() (runtimeincus.BuilderNixResult, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.phase != phaseRealized {
		return runtimeincus.BuilderNixResult{}, false
	}
	return p.result, true
}

// PublishPrivateImage accepts only this pipeline's completed WASI realization.
// It is a native core stage: the module cannot choose image labels or content.
func (p *Pipeline) PublishPrivateImage(ctx context.Context) (nixenv.Handle, runtimeincus.BuilderImageInfo, error) {
	return p.publishPrivateImage(ctx, nil)
}

func (p *Pipeline) PublishPrivateImageWithGate(ctx context.Context, gate func(runtimeincus.BuilderImageClaim) error) (nixenv.Handle, runtimeincus.BuilderImageInfo, error) {
	if gate == nil {
		return nixenv.Handle{}, runtimeincus.BuilderImageInfo{}, errors.New("durable publication gate required")
	}
	return p.publishPrivateImage(ctx, gate)
}

func (p *Pipeline) publishPrivateImage(ctx context.Context, gate func(runtimeincus.BuilderImageClaim) error) (nixenv.Handle, runtimeincus.BuilderImageInfo, error) {
	p.mu.Lock()
	if p.phase != phaseRealized {
		p.mu.Unlock()
		return nixenv.Handle{}, runtimeincus.BuilderImageInfo{}, errors.New("accepted environment realization required")
	}
	native, ok := p.backend.(imageBackend)
	if !ok {
		p.mu.Unlock()
		return nixenv.Handle{}, runtimeincus.BuilderImageInfo{}, errors.New("native image publisher unavailable")
	}
	result := p.result
	p.phase = phasePublishing
	p.choice = runtimeincus.BuilderNixSelection{}
	p.result = runtimeincus.BuilderNixResult{}
	p.mu.Unlock()
	var handle nixenv.Handle
	var image runtimeincus.BuilderImageInfo
	var err error
	if gate != nil {
		gated, ok := p.backend.(gatedImageBackend)
		if !ok {
			p.mu.Lock()
			p.phase = phaseFailed
			p.mu.Unlock()
			return nixenv.Handle{}, runtimeincus.BuilderImageInfo{}, errors.New("native gated publisher unavailable")
		}
		handle, image, err = gated.PublishBuilderImageWithGate(ctx, p.builder, result, gate)
	} else {
		handle, image, err = native.PublishBuilderImage(ctx, p.builder, result)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil || handle.Locator == "" || image.Fingerprint != handle.Locator {
		p.phase = phaseFailed
		return nixenv.Handle{}, runtimeincus.BuilderImageInfo{}, errors.Join(err, errors.New("private image publication postcondition failed"))
	}
	p.handle, p.image = handle, image
	p.image.Properties = maps.Clone(image.Properties)
	p.phase = phasePublished
	image.Properties = maps.Clone(image.Properties)
	return handle, image, nil
}

func (p *Pipeline) Image() (nixenv.Handle, runtimeincus.BuilderImageInfo, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.phase != phasePublished {
		return nixenv.Handle{}, runtimeincus.BuilderImageInfo{}, false
	}
	image := p.image
	image.Properties = maps.Clone(p.image.Properties)
	return p.handle, image, true
}

type pendingEffect struct {
	backend  nativeBackend
	builder  runtimeincus.Builder
	system   string
	choice   runtimeincus.BuilderNixSelection
	result   runtimeincus.BuilderNixResult
	selected bool
	resolved bool
	realized bool
}

func (e *pendingEffect) Resolve(ctx context.Context) error {
	if e.resolved || e.selected || e.realized {
		return errors.New("environment resolution repeated or in wrong stage")
	}
	choice, err := e.backend.ResolveBuilderNix(ctx, e.builder, e.system)
	if err != nil {
		return err
	}
	e.choice, e.resolved = choice, true
	return nil
}

func (e *pendingEffect) Realize(ctx context.Context) error {
	if !e.selected || e.realized || e.resolved || e.choice.BaseOnly {
		return errors.New("environment realization repeated or in wrong stage")
	}
	result, err := e.backend.RealizeBuilderNix(ctx, e.builder, e.choice)
	if err != nil {
		return err
	}
	e.result, e.realized = result, true
	return nil
}
