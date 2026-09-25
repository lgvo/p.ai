package daemon

import (
	"context"
	"errors"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
)

// Serve composes only configured, validated capabilities around the durable store.
func Serve(parent context.Context, cfg control.HostConfig, store *control.Store) error {
	instance, err := store.InstanceID(parent)
	if err != nil {
		return err
	}
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
	git, err := newGitCapability(scope, cfg, store)
	if err != nil {
		return err
	}
	defer git.listener.Close()
	var handler control.Handler = control.StateHandlerWithGit(store, git.backend, git.info)
	if cfg.Runtime != nil {
		life, e := newLifecycle(scope, *cfg.Runtime, store, git)
		if e != nil {
			return e
		}
		defer life.Close()
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
