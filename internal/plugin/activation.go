package plugin

import (
	"bytes"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"syscall"
)

// LoadActivation reads only trusted host configuration. A caller must keep the
// configuration outside any session or repository write boundary.
func LoadActivation(path string) ([]Active, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("activation path must be absolute")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		return nil, errors.New("activation file must be regular and not group/world writable")
	}
	if info.Size() > 1<<20 {
		return nil, errors.New("activation file exceeds 1 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, errors.New("activation file exceeds 1 MiB")
	}
	var config Activation
	if err := strictJSON(data, &config); err != nil {
		return nil, err
	}
	if config.Schema != ActivationSchema {
		return nil, fmt.Errorf("unsupported activation schema %q", config.Schema)
	}
	seenID := map[string]bool{}
	seenCapability := map[string]bool{}
	var active []Active
	for _, selection := range config.Plugins {
		pkg, err := Conformance(selection.Path)
		if err != nil {
			return nil, fmt.Errorf("%s: package digest mismatch or invalid package: %w", selection.ID, err)
		}
		if pkg.Manifest.ID != selection.ID {
			return nil, fmt.Errorf("%s: package id mismatch", selection.ID)
		}
		if seenID[selection.ID] {
			return nil, fmt.Errorf("duplicate plugin %s", selection.ID)
		}
		if seenCapability[pkg.Manifest.Capability] {
			return nil, fmt.Errorf("capability %s selected twice", pkg.Manifest.Capability)
		}
		seenID[selection.ID], seenCapability[pkg.Manifest.Capability] = true, true
		want, err := hex.DecodeString(selection.SHA256)
		if err != nil || len(want) != 32 {
			return nil, fmt.Errorf("%s: invalid package digest", selection.ID)
		}
		got, _ := hex.DecodeString(pkg.SHA256)
		if subtle.ConstantTimeCompare(got, want) != 1 {
			return nil, fmt.Errorf("%s: package digest mismatch", selection.ID)
		}
		requested := slices.Clone(pkg.Manifest.Requests)
		approved := slices.Clone(selection.Grants)
		slices.Sort(requested)
		slices.Sort(approved)
		if !slices.Equal(requested, approved) {
			return nil, fmt.Errorf("%s: grants must exactly match manifest requests", selection.ID)
		}
		if err := validateConfig(pkg.Manifest, selection.Config); err != nil {
			return nil, fmt.Errorf("%s: %w", selection.ID, err)
		}
		active = append(active, Active{Package: pkg, Grants: approved, Config: selection.Config})
	}
	return active, nil
}

func validateConfig(m Manifest, data []byte) error {
	if m.Runtime.Kind == "assets" {
		var config struct{}
		if len(data) == 0 {
			return nil
		}
		return strictJSON(data, &config)
	}
	if m.Capability == "source-git" && m.Runtime.Kind == "wasi-command" {
		var config struct{}
		if len(data) == 0 {
			return nil
		}
		return strictJSON(data, &config)
	}
	if m.Capability == "runtime" && m.Runtime.Kind == "wasi-command" {
		var config struct{}
		if len(data) == 0 {
			return nil
		}
		return strictJSON(data, &config)
	}
	if m.Capability == "environment" && m.Runtime.Kind == "wasi-command" {
		var config struct{}
		if len(data) == 0 {
			return nil
		}
		if trimmed := bytes.TrimSpace(data); len(trimmed) == 0 || trimmed[0] != '{' {
			return errors.New("environment config must be an empty object")
		}
		return strictJSON(data, &config)
	}
	if m.Capability != "event-handler" || (m.Runtime.Kind != "declarative" && m.Runtime.Kind != "wasi-command") {
		// The runner exists for other capability classes, but their typed core
		// adapters are deliberately unavailable until each method is defined.
		return errors.New("capability execution is not implemented")
	}
	if m.Runtime.Kind == "declarative" && m.Runtime.Entry != "event.file.append" {
		return errors.New("unsupported operation")
	}
	var config FileLogConfig
	if err := strictJSON(data, &config); err != nil {
		return fmt.Errorf("file-log config: %w", err)
	}
	if !filepath.IsAbs(config.Path) || filepath.Clean(config.Path) != config.Path {
		return errors.New("file-log path must be absolute and clean")
	}
	if config.MaxBytes < 1024 || config.MaxBytes > 100<<20 {
		return errors.New("file-log max_bytes must be between 1024 and 104857600")
	}
	return nil
}
