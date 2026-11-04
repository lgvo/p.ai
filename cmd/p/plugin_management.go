package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/eventhandler"
	handlers "github.com/lgvo/p.ai/internal/eventhandler/supervise"
	"github.com/lgvo/p.ai/internal/plugin"
)

func managePlugins(args []string) error {
	if len(args) < 4 {
		return pluginsUsage()
	}
	method := args[1]
	if method == "install" && len(args) != 5 || method == "update" && len(args) != 6 || method == "remove" && len(args) != 4 {
		return pluginsUsage()
	}
	host := mustAbs(args[2])
	cfg, err := control.LoadHostConfig(host)
	if err != nil {
		return err
	}
	original, _ := json.Marshal(cfg)
	store, err := control.OpenStore(cfg.StateDir)
	if err != nil {
		return fmt.Errorf("offline package management requires the instance daemon stopped: %w", err)
	}
	defer store.Close()
	verifyHost := func() (control.HostConfig, error) {
		fresh, e := control.LoadHostConfig(host)
		if e != nil {
			return fresh, e
		}
		raw, _ := json.Marshal(fresh)
		if !bytes.Equal(original, raw) {
			return fresh, errors.New("trusted host configuration changed; review again")
		}
		return fresh, nil
	}
	if _, err = verifyHost(); err != nil {
		return err
	}
	ctx := context.Background()
	instance, err := store.InstanceID(ctx)
	if err != nil {
		return err
	}
	switch method {
	case "install", "update":
		source, approved, previous := mustAbs(args[3]), args[4], ""
		if method == "update" {
			previous = args[3]
			source = mustAbs(args[4])
			approved = args[5]
		}
		result, e := plugin.StagePackage(ctx, cfg.StateDir, instance, source, approved, previous)
		if e != nil {
			return e
		}
		return printJSON(result)
	case "remove":
		digest := args[3]
		verify := func() error {
			fresh, e := verifyHost()
			if e != nil {
				return e
			}
			if fresh.SelectsPackage(digest) {
				return errors.New("package is selected in host configuration; deselect it from its role, then retry with the daemon stopped")
			}
			return store.CheckPluginRemovalDependencies(ctx, digest)
		}
		if err = verify(); err != nil {
			return err
		}
		if err = plugin.RemoveStaged(cfg.StateDir, instance, digest, verify); err != nil {
			return err
		}
		return printJSON(map[string]any{"sha256": digest, "path": filepath.Join(cfg.StateDir, "plugins", "packages", digest), "status": "removed", "new_invocations": "disabled", "reinstall": "new approved digest required", "session_assets_and_credentials": "preserved"})
	}
	return usage()
}

// runEvent starts the event handler the host configuration at host selects,
// after its launch checks, sends it the event in the file at path, and
// prints what the handler did with it. It is a diagnostic command: the
// daemon's delivery is not involved.
func runEvent(host, path string) error {
	cfg, err := control.LoadHostConfig(host)
	if err != nil {
		return err
	}
	if cfg.Events == nil {
		return errors.New("the host configuration selects no event handler")
	}
	event, err := readEvent(path)
	if err != nil {
		return err
	}
	release, err := plugin.LeasePackage(cfg.Events.Path)
	if err != nil {
		return err
	}
	defer release()
	handler, err := handlers.Start(context.Background(), handlers.Config{Path: cfg.Events.Path, SHA256: cfg.Events.SHA256, Config: cfg.Events.Config})
	if err != nil {
		return err
	}
	defer handler.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	status, err := handler.Handle(ctx, event)
	if err != nil {
		return err
	}
	return printJSON(map[string]string{"status": string(status), "event_id": event.ID})
}

// readEvent reads one event from the regular file at path, without
// following a symbolic link or blocking on a FIFO.
func readEvent(path string) (eventhandler.Event, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return eventhandler.Event{}, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return eventhandler.Event{}, err
	}
	if !info.Mode().IsRegular() {
		return eventhandler.Event{}, errors.New("event file must be regular")
	}
	data, err := io.ReadAll(io.LimitReader(f, eventhandler.MaxEventBytes+1))
	if err != nil {
		return eventhandler.Event{}, err
	}
	return eventhandler.ParseEvent(data)
}
