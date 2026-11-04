package plugin

import "encoding/json"

// ManifestSchema is the schema of a package's plugin.json.
const ManifestSchema = "p.plugin/v2"

// The capabilities a package may provide: an executable plugin the daemon
// starts, or a package of files the runtime driver installs in sessions.
const (
	CapabilityEventHandler = "event-handler"
	CapabilityAgentAdapter = "agent-adapter"
)

// Manifest is package-authored data. It never grants authority by itself:
// only trusted host configuration selects a package. An executable
// capability names its Entry; a package of files names its Assets.
type Manifest struct {
	Schema      string   `json:"schema"`
	ID          string   `json:"id"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Capability  string   `json:"capability"`
	Entry       string   `json:"entry,omitempty"`
	Assets      []string `json:"assets,omitempty"`
}

type Package struct {
	Path     string   `json:"path"`
	SHA256   string   `json:"sha256"`
	Manifest Manifest `json:"manifest"`
}

// Selection is how trusted host configuration selects a package for one
// role: its directory, its package digest, and, for an executable plugin,
// the plugin's own config.
type Selection struct {
	Path   string          `json:"path"`
	SHA256 string          `json:"sha256"`
	Config json.RawMessage `json:"config,omitempty"`
}

// GitRef is one head of a project as project.branches pages it: its full ref
// and its tip.
type GitRef struct {
	Ref string `json:"ref"`
	OID string `json:"oid"`
}
