package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// AgentAdapter is the agent adapter package host configuration selects,
// read and checked against its pinned digest: its identity, and its files
// for the runtime driver to install in sessions.
type AgentAdapter struct {
	ID     string
	SHA256 string
	Files  []AssetFile
}

// AssetFile is one file of an agent adapter package, named by its role. The
// runtime driver decides where a role goes in a session.
type AssetFile struct {
	Role   string
	Mode   uint32
	SHA256 string // hex SHA-256 of Data
	Data   []byte
}

// assetModes are the mode each asset role gets in a session.
var assetModes = map[string]uint32{"p-codex-adapter": 0555}

// ReadAgentAdapter reads the agent adapter package at path and checks it
// against digest: it holds the bytes it hashed, so the files it returns are
// the ones pinned. It installs nothing.
func ReadAgentAdapter(path, digest string) (AgentAdapter, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pkg, data, err := packageSnapshot(ctx, path, map[string]bool{"*": true})
	if err != nil {
		if ctx.Err() != nil {
			return AgentAdapter{}, errors.New("reading the agent adapter timed out")
		}
		return AgentAdapter{}, fmt.Errorf("the agent adapter package: %w", err)
	}
	if pkg.SHA256 != digest {
		return AgentAdapter{}, fmt.Errorf("the agent adapter package %s has SHA-256 %s, not the pinned %s", path, pkg.SHA256, digest)
	}
	if pkg.Manifest.Capability != CapabilityAgentAdapter {
		return AgentAdapter{}, fmt.Errorf("the package %s provides %s, not %s", path, pkg.Manifest.Capability, CapabilityAgentAdapter)
	}
	adapter := AgentAdapter{ID: pkg.Manifest.ID, SHA256: pkg.SHA256}
	for _, role := range pkg.Manifest.Assets {
		file := data[role]
		if len(file) == 0 || len(file) > 1<<20 {
			return AgentAdapter{}, fmt.Errorf("asset %q must be 1 byte to 1 MiB", role)
		}
		sum := sha256.Sum256(file)
		adapter.Files = append(adapter.Files, AssetFile{Role: role, Mode: assetModes[role], SHA256: hex.EncodeToString(sum[:]), Data: file})
	}
	return adapter, nil
}
