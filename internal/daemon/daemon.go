package daemon

import (
	"context"
	"errors"
	"reflect"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
)

// Serve composes only configured, validated capabilities around the durable store.
func Serve(parent context.Context, cfg control.HostConfig, store *control.Store) error {
	instance, err := store.InstanceID(parent)
	if err != nil {
		return err
	}
	releasePackages, leasedPackages, err := leaseConfiguredPackageSnapshot(cfg, instance)
	if err != nil {
		return err
	}
	defer releasePackages()
	events, err := newEventDelivery(parent, cfg.Events, instance)
	if err != nil {
		return err
	}
	defer events.Close()
	if events != nil {
		for _, selected := range events.active {
			if !sameLeasedSelection(leasedPackages, selected) {
				return errors.New("event selection changed while acquiring package leases")
			}
		}
	}
	if cfg.Git == nil {
		return control.Serve(parent, store, cfg.StateDir)
	}
	scope, cancel := context.WithCancel(parent)
	defer cancel()
	git, err := newGitCapability(scope, cfg, store)
	if err != nil {
		return err
	}
	defer git.listener.Close()
	if !sameLeasedSelection(leasedPackages, git.source) {
		return errors.New("source selection changed while acquiring package leases")
	}
	var handler control.Handler = control.StateHandlerWithGit(store, git.backend, git.info)
	if cfg.Runtime != nil {
		life, e := newLifecycle(scope, *cfg.Runtime, store, git)
		if e != nil {
			return e
		}
		defer life.Close()
		if !sameLeasedSelection(leasedPackages, life.runtimePlugin) || life.environmentPlugin != nil && !sameLeasedSelection(leasedPackages, *life.environmentPlugin) || !sameLeasedAsset(leasedPackages, life.hostPlan) || !sameLeasedAsset(leasedPackages, life.sourcePlan) || life.agentPlan != nil && !sameLeasedAsset(leasedPackages, *life.agentPlan) {
			return errors.New("runtime/asset selection changed while acquiring package leases")
		}
		life.events = events
		life.onAttachment = life.attachmentChanged
		life.endpoints.onUnattended = func(_ context.Context, event plugin.Event) error {
			events.enqueue(event)
			return nil
		}
		handler = control.StateHandlerWithLifecycle(store, git.backend, git.info, life)
		if e = life.Recover(); e != nil {
			return e
		}
	}
	handler = control.OriginHandler(handler, control.OriginAPI{Store: store, Runner: originRunner{backend: git.backend}, Git: git.backend})
	return runPair(scope,
		func(ctx context.Context) error { return git.prepared.Serve(ctx) },
		func(ctx context.Context) error {
			return control.ServeWithHandler(ctx, cfg.StateDir, handler)
		},
		func() { git.listener.Close() },
	)
}

func runPair(parent context.Context, gitServe, rpcServe func(context.Context) error, closeGit func()) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	gitDone := make(chan error, 1)
	go func() { gitDone <- gitServe(ctx) }()
	rpcDone := make(chan error, 1)
	go func() { rpcDone <- rpcServe(ctx) }()
	var first error
	select {
	case first = <-gitDone:
		cancel()
		<-rpcDone
	case first = <-rpcDone:
		cancel()
		closeGit()
		<-gitDone
	case <-parent.Done():
		cancel()
		closeGit()
		<-gitDone
		<-rpcDone
	}
	if parent.Err() != nil {
		return nil
	}
	if first == nil {
		return errors.New("daemon listener stopped unexpectedly")
	}
	return first
}

// Cached role and asset selections keep managed packages leased until every
// runner is stopped; a different instance cannot borrow the bound registry.
func leaseConfiguredPackages(cfg control.HostConfig, instance string) (func(), error) {
	release, _, err := leaseConfiguredPackageSnapshot(cfg, instance)
	return release, err
}
func leaseConfiguredPackageSnapshot(cfg control.HostConfig, instance string) (func(), map[string]plugin.Active, error) {
	packages := map[string]plugin.Active{}
	releases := []func(){}
	release := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	seen := map[string]bool{}
	for _, path := range cfg.PluginActivationPaths() {
		if seen[path] {
			continue
		}
		seen[path] = true
		active, err := plugin.LoadPrivateActivation(path)
		if err != nil {
			release()
			return nil, nil, err
		}
		for _, selected := range active {
			r, err := plugin.LeaseInstancePackage(selected.Package.Path, cfg.StateDir, instance)
			if err != nil {
				release()
				return nil, nil, err
			}
			releases = append(releases, r)
			packages[selected.Package.Path] = selected
		}
	}
	return release, packages, nil
}

func sameLeasedSelection(packages map[string]plugin.Active, selected plugin.Active) bool {
	expected, ok := packages[selected.Package.Path]
	return ok && reflect.DeepEqual(expected, selected)
}
func sameLeasedAsset(packages map[string]plugin.Active, plan plugin.AssetPlan) bool {
	selected, ok := packages[plan.PackagePath]
	return ok && selected.Package.Manifest.ID == plan.PackageID && selected.Package.SHA256 == plan.PackageSHA256
}
