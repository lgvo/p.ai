package plugin

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestBundledCodexAdapterIsExactPinnedSessionAsset(t *testing.T) {
	path, err := filepath.Abs("../../plugins/bundled/codex-adapter")
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := Conformance(path)
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Manifest.ID != "org.p.codex-adapter" || pkg.Manifest.Capability != "agent-adapter" || pkg.Manifest.Runtime.Kind != "assets" {
		t.Fatal("bundled Codex adapter manifest changed")
	}
	plan, err := PlanAssets(Active{Package: pkg, Grants: []string{"agent.status.report"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 1 || plan.Files[0].Role != "p-codex-adapter" || plan.Files[0].Destination != "/usr/libexec/p/codex-adapter" || plan.Files[0].Mode != 0555 || !bytes.HasPrefix(plan.Files[0].Data, []byte("#!/run/current-system/sw/bin/python3 -I\n")) {
		t.Fatal("bundled adapter changed executable or installation target")
	}
}
