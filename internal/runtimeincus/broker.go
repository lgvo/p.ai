package runtimeincus

import (
	"context"
	"fmt"

	"github.com/lgvo/p.ai/internal/plugin"
)

// Scoped binds a WASI runtime broker to the trusted core-selected session.
// Plugin requests carry only a method and opaque invocation token.
type Scoped struct {
	Backend      *Backend
	Session      Session
	Assembly     *Assembly
	HostAssets   *plugin.AssetPlan
	BeforeCreate func() error // trusted core init-attempt marker, never plugin-selected
}

var _ plugin.RuntimeBroker = Scoped{}

func (s Scoped) Inspect(ctx context.Context) (plugin.RuntimeState, error) {
	o, e := s.Backend.Inspect(ctx, s.Session)
	return state(o), e
}

// InspectCreated is available only to the broker's preliminary observation
// for runtime.create. Ordinary runtime inspection remains strict.
func (s Scoped) InspectCreated(ctx context.Context) (plugin.RuntimeState, error) {
	o, e := s.Backend.InspectCreated(ctx, s.Session)
	return state(o), e
}
func (s Scoped) Create(ctx context.Context) (plugin.RuntimeState, error) {
	var o Observation
	var e error
	if s.BeforeCreate != nil {
		o, e = s.Backend.CreateWithGate(ctx, s.Session, s.BeforeCreate)
	} else {
		o, e = s.Backend.Create(ctx, s.Session)
	}
	return state(o), e
}
func (s Scoped) Start(ctx context.Context) (plugin.RuntimeState, error) {
	o, e := s.Backend.Start(ctx, s.Session)
	return state(o), e
}
func (s Scoped) Stop(ctx context.Context) (plugin.RuntimeState, error) {
	o, e := s.Backend.Stop(ctx, s.Session)
	return state(o), e
}
func (s Scoped) Delete(ctx context.Context) (plugin.RuntimeState, error) {
	o, e := s.Backend.Delete(ctx, s.Session)
	return state(o), e
}

func (s Scoped) Assemble(ctx context.Context) (plugin.RuntimeState, error) {
	if s.Assembly == nil {
		return plugin.RuntimeState{}, fmt.Errorf("trusted assembly input missing")
	}
	o, e := s.Backend.Assemble(ctx, s.Session, *s.Assembly)
	return state(o), e
}

func (s Scoped) ObserveHost(ctx context.Context) (plugin.RuntimeState, error) {
	o, e := s.Backend.ObserveHost(ctx, s.Session)
	return plugin.RuntimeState{Exists: o.Exists, Status: o.Status, HostUnit: o.Unit, HostReady: o.Ready, Diagnostic: o.Diagnostic, DiagnosticAvailable: o.DiagnosticAvailable}, e
}

func state(o Observation) plugin.RuntimeState {
	return plugin.RuntimeState{Exists: o.Exists, Status: o.Status}
}

func (s Scoped) Attach(ctx context.Context) (plugin.RuntimeState, error) {
	if s.HostAssets == nil {
		return plugin.RuntimeState{}, fmt.Errorf("trusted host assets missing")
	}
	spec, e := s.Backend.AttachSpec(ctx, s.Session, *s.HostAssets)
	if e != nil {
		return plugin.RuntimeState{}, e
	}
	return plugin.RuntimeState{Exists: true, Status: "Running", HostReady: true, AttachSpec: &spec}, nil
}
