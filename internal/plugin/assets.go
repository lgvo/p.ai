package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"
)

// AssetFile is an installation instruction, never a package-selected path.
// Data is the digest-verified snapshot that a later session installer writes.
type AssetFile struct {
	Role        string `json:"role"`
	Destination string `json:"destination"`
	Mode        uint32 `json:"mode"`
	SHA256      string `json:"sha256"`
	Data        []byte `json:"-"`
}

type AssetPlan struct {
	Schema        string      `json:"schema"`
	PackageID     string      `json:"package_id"`
	PackageSHA256 string      `json:"package_sha256"`
	Scope         string      `json:"scope"`
	Files         []AssetFile `json:"files"`
}

var assetRoles = map[string]struct {
	destination string
	mode        uint32
}{
	"p-session.target":      {"/etc/systemd/system/p-session.target", 0644},
	"p-interactive.service": {"/etc/systemd/system/p-interactive.service", 0644},
	"p-attach":              {"/usr/libexec/p/attach", 0555},
	"p-codex-adapter":       {"/usr/libexec/p/codex-adapter", 0555},
	"p-git-ssh":             {"/usr/libexec/p/git-ssh", 0555},
}

// PlanAssets creates a typed, content-pinned plan for a session image. It does
// not write host or session files; the destination set is owned by core.
func PlanAssets(selected Active) (AssetPlan, error) {
	var plan AssetPlan
	m := selected.Package.Manifest
	if !((m.Runtime.Kind == "assets" && m.Placement == "internal-session") || (m.Runtime.Kind == "wasi-command" && m.Capability == "source-git" && m.Placement == "hybrid")) {
		return plan, errors.New("selected package is not a session asset package")
	}
	if m.Capability != "interactive-host" && m.Capability != "agent-adapter" && m.Capability != "source-git" {
		return plan, errors.New("unsupported asset capability")
	}
	if !slices.Equal(selected.Grants, []string{map[string]string{"interactive-host": "session.asset.install", "agent-adapter": "agent.status.report", "source-git": "git.project"}[m.Capability]}) {
		return plan, errors.New("asset grant mismatch")
	}
	capture := make(map[string]bool, len(m.Assets))
	for _, name := range m.Assets {
		capture[name] = true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	current, bytes, err := packageSnapshot(ctx, selected.Package.Path, capture)
	if err != nil {
		if ctx.Err() != nil {
			return plan, errors.New("asset planning timed out")
		}
		return plan, errors.New("selected package digest changed or invalid")
	}
	if current.SHA256 != selected.Package.SHA256 || !reflect.DeepEqual(current.Manifest, m) {
		return plan, errors.New("selected package digest changed")
	}
	plan = AssetPlan{Schema: "p.asset-plan/v1", PackageID: m.ID, PackageSHA256: current.SHA256, Scope: "internal-session"}
	for _, name := range m.Assets {
		role := assetRoles[name]
		data := bytes[name]
		if len(data) == 0 || len(data) > 1<<20 {
			return AssetPlan{}, fmt.Errorf("asset %q must be 1 byte to 1 MiB", name)
		}
		sha := sha256.Sum256(data)
		plan.Files = append(plan.Files, AssetFile{Role: name, Destination: role.destination, Mode: role.mode, SHA256: hex.EncodeToString(sha[:]), Data: data})
	}
	slices.SortFunc(plan.Files, func(a, b AssetFile) int {
		if a.Role < b.Role {
			return -1
		}
		if a.Role > b.Role {
			return 1
		}
		return 0
	})
	return plan, nil
}
