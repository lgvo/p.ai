package plugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The bundled Codex adapter is read as the pinned file the driver installs
// executable in sessions.
func TestTheBundledCodexAdapterIsItsPinnedFile(t *testing.T) {
	path, err := filepath.Abs("../../plugins/bundled/codex-adapter")
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := Conformance(path)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := ReadAgentAdapter(path, pkg.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(path, "p-codex-adapter"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if adapter.ID != "org.p.codex-adapter" || adapter.SHA256 != pkg.SHA256 || len(adapter.Files) != 1 {
		t.Fatalf("the bundled adapter reads as %+v", adapter)
	}
	f := adapter.Files[0]
	if f.Role != "p-codex-adapter" || f.Mode != 0555 || !bytes.Equal(f.Data, data) || f.SHA256 != hex.EncodeToString(sum[:]) ||
		!bytes.HasPrefix(f.Data, []byte("#!/run/current-system/sw/bin/python3 -I\n")) {
		t.Fatalf("the bundled adapter's file is %s %o %s", f.Role, f.Mode, f.SHA256)
	}
}

func TestReadAgentAdapterRefuses(t *testing.T) {
	adapter := writePackage(t, adapterManifest, map[string]string{"p-codex-adapter": "#!/bin/sh\n"})
	pkg, err := Conformance(adapter)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAgentAdapter(adapter, strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "not the pinned") {
		t.Fatalf("another digest: %v", err)
	}
	handler := testPackage(t)
	if _, err := ReadAgentAdapter(handler.Path, handler.SHA256); err == nil || !strings.Contains(err.Error(), "provides event-handler") {
		t.Fatalf("an event handler package: %v", err)
	}
	empty := writePackage(t, adapterManifest, map[string]string{"p-codex-adapter": ""})
	emptyPkg, err := Conformance(empty)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAgentAdapter(empty, emptyPkg.SHA256); err == nil || !strings.Contains(err.Error(), "1 byte to 1 MiB") {
		t.Fatalf("an empty asset: %v", err)
	}
	if _, err := ReadAgentAdapter(adapter, pkg.SHA256); err != nil {
		t.Fatalf("the pinned adapter: %v", err)
	}
}
