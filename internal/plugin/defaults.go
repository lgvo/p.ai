package plugin

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/lgvo/p.ai/internal/eventfile"
)

// bundledRoles are the bundled packages, by their directory in the catalog,
// with the identity and capability each must have.
var bundledRoles = []struct {
	directory, id, capability string
}{
	{"file-log", "org.p.filelog", CapabilityEventHandler},
	{"codex-adapter", "org.p.codex-adapter", CapabilityAgentAdapter},
}

// bundledEventLogBytes is the bundled file-log's rotation limit.
const bundledEventLogBytes = 4 << 20

// Defaults are the host configuration blocks that select the bundled
// packages: the file-log event handler and the Codex agent adapter.
type Defaults struct {
	Events  Selection       `json:"events"`
	Runtime RuntimeDefaults `json:"runtime"`
}

// RuntimeDefaults is the bundled selection inside the runtime block.
type RuntimeDefaults struct {
	AgentAdapter Selection `json:"agent_adapter"`
}

// DefaultSelections validates the bundled packages in catalog and returns
// the host configuration blocks that select them, with the file-log writing
// to eventLog. It writes nothing and starts no code; the catalog and the
// event log are the trusted host owner's.
func DefaultSelections(catalog, eventLog string) (Defaults, error) {
	if !filepath.IsAbs(catalog) || filepath.Clean(catalog) != catalog {
		return Defaults{}, fmt.Errorf("bundled catalog path must be absolute and clean")
	}
	config, err := json.Marshal(eventfile.Config{Path: eventLog, MaxBytes: bundledEventLogBytes})
	if err != nil {
		return Defaults{}, err
	}
	if _, err := eventfile.ParseConfig(config); err != nil {
		return Defaults{}, err
	}
	var selected []Selection
	for _, role := range bundledRoles {
		pkg, err := Conformance(filepath.Join(catalog, role.directory))
		if err != nil {
			return Defaults{}, fmt.Errorf("bundled %s: %w", role.directory, err)
		}
		if pkg.Manifest.ID != role.id || pkg.Manifest.Capability != role.capability {
			return Defaults{}, fmt.Errorf("bundled %s has an unexpected identity or capability", role.directory)
		}
		selected = append(selected, Selection{Path: pkg.Path, SHA256: pkg.SHA256})
	}
	if _, err := ReadAgentAdapter(selected[1].Path, selected[1].SHA256); err != nil {
		return Defaults{}, err
	}
	selected[0].Config = config
	return Defaults{Events: selected[0], Runtime: RuntimeDefaults{AgentAdapter: selected[1]}}, nil
}
