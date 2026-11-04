package daemon

import (
	"context"
	"errors"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/eventhandler"
	"github.com/lgvo/p.ai/internal/plugin"
)

// Serve composes only configured, validated capabilities around the durable store.
func Serve(parent context.Context, cfg control.HostConfig, store *control.Store) error {
	instance, err := store.InstanceID(parent)
	if err != nil {
		return err
	}
	releasePackages, err := leaseConfiguredPackages(cfg, instance)
	if err != nil {
		return err
	}
	defer releasePackages()
	events, err := newEventDelivery(parent, cfg.Events, instance)
	if err != nil {
		return err
	}
	defer events.Close()
	if cfg.Git == nil {
		return control.Serve(parent, store, cfg.StateDir)
	}
	scope, cancel := context.WithCancel(parent)
	defer cancel()
	driver, endServices, err := newSourceDriver(scope, cfg)
	if err != nil {
		return err
	}
	git, err := newGitCapability(scope, cfg, store, driver, endServices)
	if err != nil {
		return err
	}
	defer git.close()
	origins := &control.OriginAPI{Store: store, Driver: git.driver}
	surface := control.HandlerConfig{Store: store, Git: git.driver, GitInfo: git.info, Origins: origins}
	if cfg.Runtime != nil {
		life, e := newLifecycle(scope, *cfg.Runtime, store, git)
		if e != nil {
			return e
		}
		defer func() {
			// The workers stop waiting before the driver they call stops, so a
			// call cut short resumes at the next start instead of failing.
			cancel()
			life.Close()
		}()
		life.events = events
		life.origins, origins.Queue = origins, life.enqueue
		life.onAttachment = life.attachmentChanged
		life.endpoints.onUnattended = func(_ context.Context, event eventhandler.Event) error {
			events.enqueue(event)
			return nil
		}
		surface.Lifecycle = life
		if e = life.Recover(); e != nil {
			return e
		}
	} else {
		// Without a runtime the journal still runs origin operations.
		life := newGitLifecycle(scope, store, git)
		life.events = events
		life.origins, origins.Queue = origins, life.enqueue
		surface.Operations = life
		if e := life.recoverJournal(); e != nil {
			return e
		}
	}
	handler := control.NewHandler(surface)
	return runPair(scope,
		func(ctx context.Context) error { return git.server.Serve(ctx) },
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

// leaseConfiguredPackages leases the managed packages host configuration
// selects for the daemon's lifetime, so that none is removed while its
// handler may run or a creation may read it; a different instance cannot
// borrow the registry they belong to.
func leaseConfiguredPackages(cfg control.HostConfig, instance string) (func(), error) {
	releases := []func(){}
	release := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	for _, path := range cfg.SelectedPackages() {
		r, err := plugin.LeaseInstancePackage(path, cfg.StateDir, instance)
		if err != nil {
			release()
			return nil, err
		}
		releases = append(releases, r)
	}
	return release, nil
}
