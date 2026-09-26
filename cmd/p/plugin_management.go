package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/lgvo/p.ai/internal/control"
	"github.com/lgvo/p.ai/internal/plugin"
)

func managePlugins(args []string) error {
	if len(args) < 4 {
		return errors.New("usage: p plugins install HOST_JSON PACKAGE APPROVED_SHA256 | update HOST_JSON OLD_SHA256 PACKAGE APPROVED_SHA256 | remove HOST_JSON SHA256")
	}
	method := args[1]
	if method == "install" && len(args) != 5 || method == "update" && len(args) != 6 || method == "remove" && len(args) != 4 {
		return usage()
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
			for _, path := range fresh.PluginActivationPaths() {
				active, e := plugin.LoadPrivateActivation(path)
				if e != nil {
					return e
				}
				for _, selected := range active {
					if selected.Package.SHA256 == digest {
						return errors.New("package is selected in trusted activation; explicitly deselect it and its host role, then retry with daemon stopped")
					}
				}
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
