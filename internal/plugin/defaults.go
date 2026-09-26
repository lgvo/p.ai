package plugin

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
)

type bundledRole struct {
	directory, id, capability, kind, entry, grant string
}

var bundledRoles = []bundledRole{
	{"runtime-incus", "org.p.runtime.incus", "runtime", "wasi-command", "runtime.wasm", "runtime.incus"},
	{"tmux-host", "org.p.tmux-host", "interactive-host", "assets", "", "session.asset.install"},
	{"source-git", "org.p.git", "source-git", "wasi-command", "git.wasm", "git.project"},
	{"environment-nix", "org.p.environment.nix", "environment", "wasi-command", "environment.wasm", "environment.nix"},
	{"file-log", "org.p.filelog", "event-handler", "declarative", "event.file.append", "event.file.append"},
	{"codex-adapter", "org.p.codex-adapter", "agent-adapter", "assets", "", "agent.status.report"},
}

// DefaultActivation describes the exact bundled composition. It validates all
// packages but writes no configuration, starts no code and grants no effects.
// The catalog and event-log path are supplied by the trusted host owner.
func DefaultActivation(catalog, eventLog string) (Activation, error) {
	if !filepath.IsAbs(catalog) || filepath.Clean(catalog) != catalog {
		return Activation{}, fmt.Errorf("bundled catalog path must be absolute and clean")
	}
	result := Activation{Schema: ActivationSchema, Plugins: []SelectedPackage{}}
	for _, role := range bundledRoles {
		pkg, err := Conformance(filepath.Join(catalog, role.directory))
		if err != nil {
			return Activation{}, fmt.Errorf("bundled %s: %w", role.directory, err)
		}
		m := pkg.Manifest
		if m.ID != role.id || m.Capability != role.capability || m.Runtime.Kind != role.kind || m.Runtime.Entry != role.entry ||
			!slices.Equal(m.Requests, []string{role.grant}) {
			return Activation{}, fmt.Errorf("bundled %s has unexpected identity, capability or grants", role.directory)
		}
		config := json.RawMessage(`{}`)
		if role.capability == "event-handler" {
			config, err = json.Marshal(FileLogConfig{Path: eventLog, MaxBytes: 4 << 20})
			if err != nil {
				return Activation{}, err
			}
		}
		if err = validateConfig(m, config); err != nil {
			return Activation{}, err
		}
		if role.kind == "assets" {
			if _, err = PlanAssets(Active{Package: pkg, Grants: []string{role.grant}, Config: config}); err != nil {
				return Activation{}, err
			}
		}
		result.Plugins = append(result.Plugins, SelectedPackage{ID: role.id, Path: pkg.Path, SHA256: pkg.SHA256,
			Grants: []string{role.grant}, Config: config})
	}
	return result, nil
}
