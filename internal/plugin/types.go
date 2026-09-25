package plugin

import "encoding/json"

const (
	ManifestSchema   = "p.plugin/v1"
	ActivationSchema = "p.activation/v1"
	APIVersion       = "1.0"
)

// Manifest is package-authored data. It never grants authority by itself.
type Manifest struct {
	Schema      string   `json:"schema"`
	ID          string   `json:"id"`
	Version     string   `json:"version"`
	API         string   `json:"api"`
	Capability  string   `json:"capability"`
	Placement   string   `json:"placement"`
	Description string   `json:"description"`
	Runtime     Runtime  `json:"runtime"`
	Requests    []string `json:"requests"`
	Assets      []string `json:"assets,omitempty"`
}

type Runtime struct {
	Kind  string `json:"kind"`
	Entry string `json:"entry,omitempty"`
}

// Activation is trusted developer configuration, never read from a project repo.
type Activation struct {
	Schema  string            `json:"schema"`
	Plugins []SelectedPackage `json:"plugins"`
}

type SelectedPackage struct {
	ID     string          `json:"id"`
	Path   string          `json:"path"`
	SHA256 string          `json:"sha256"`
	Grants []string        `json:"grants"`
	Config json.RawMessage `json:"config,omitempty"`
}

type Package struct {
	Path     string   `json:"path"`
	SHA256   string   `json:"sha256"`
	Manifest Manifest `json:"manifest"`
}

type Active struct {
	Package Package         `json:"package"`
	Grants  []string        `json:"grants"`
	Config  json.RawMessage `json:"config,omitempty"`
}
