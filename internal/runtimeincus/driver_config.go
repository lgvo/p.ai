package runtimeincus

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/lgvo/p.ai/internal/strictjson"
)

// DriverConfig is the Incus driver's own part of trusted host configuration,
// the `config` object of `runtime.driver`. Control passes it through without
// reading it; New checks the paths and project against the live host.
type DriverConfig struct {
	Binary     string `json:"binary"`
	UserSocket string `json:"user_socket"`
	Project    string `json:"project"`
}

// ParseDriverConfig reads exactly the three Incus settings and nothing else.
func ParseDriverConfig(raw json.RawMessage) (DriverConfig, error) {
	var c DriverConfig
	if err := strictjson.Decode(raw, &c); err != nil {
		return DriverConfig{}, fmt.Errorf("Incus driver config: %w", err)
	}
	if !absoluteClean(c.Binary) || !absoluteClean(c.UserSocket) || c.Project == "" {
		return DriverConfig{}, errors.New("Incus driver config requires an absolute clean binary and user_socket and a project")
	}
	return c, nil
}

func absoluteClean(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path
}
