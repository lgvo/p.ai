package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goodManifest = `{
  "schema":"p.plugin/v2", "id":"example.filelog", "version":"1.2.3",
  "description":"Test log handler", "capability":"event-handler", "entry":"handler"
}`

const handlerScript = "#!/bin/sh\nexit 0\n"

// writePackage writes a package with manifest and files into a new private
// directory: the entry 0700, every other file 0600.
func writePackage(t *testing.T, manifest string, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "package")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// testPackage is a conforming event-handler package whose entry is a shell
// script.
func testPackage(t *testing.T) Package {
	t.Helper()
	pkg, err := Conformance(writePackage(t, goodManifest, map[string]string{"handler": handlerScript}))
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}

const adapterManifest = `{"schema":"p.plugin/v2","id":"example.adapter","version":"1.0.0","description":"Test adapter","capability":"agent-adapter","assets":["p-codex-adapter"]}`

func TestConformanceRejectsUnsafePackage(t *testing.T) {
	handler := map[string]string{"handler": handlerScript}
	adapter := map[string]string{"p-codex-adapter": "#!/bin/sh\n"}
	tests := []struct {
		name, manifest string
		files          map[string]string
		extra          func(string) error
		want           string
	}{
		{"an earlier schema", strings.Replace(goodManifest, "p.plugin/v2", "p.plugin/v1", 1), handler, nil, `unsupported manifest schema "p.plugin/v1"`},
		{"an unknown field", strings.Replace(goodManifest, `"schema":"p.plugin/v2"`, `"schema":"p.plugin/v2","surprise":true`, 1), handler, nil, "unknown field"},
		{"grants requested", strings.Replace(goodManifest, `"entry":"handler"`, `"entry":"handler","requests":["event.file.append"]`, 1), handler, nil, "unknown field"},
		{"an unknown capability", strings.Replace(goodManifest, "event-handler", "runtime-provider", 1), handler, nil, "unsupported capability"},
		{"an executable capability without its entry", strings.Replace(goodManifest, `, "entry":"handler"`, ``, 1), nil, nil, "event-handler requires an entry"},
		{"an entry missing from the package", goodManifest, nil, nil, `missing entry "handler"`},
		{"the manifest as its entry", strings.Replace(goodManifest, `"entry":"handler"`, `"entry":"plugin.json"`, 1), nil, nil, `missing entry "plugin.json"`},
		{"an entry outside the package", strings.Replace(goodManifest, `"entry":"handler"`, `"entry":"../handler"`, 1), handler, nil, "unsafe relative path"},
		{"an executable capability with assets", strings.Replace(goodManifest, `"entry":"handler"`, `"entry":"handler","assets":["handler"]`, 1), handler, nil, "names an entry, not assets"},
		{"an undeclared file", goodManifest, map[string]string{"handler": handlerScript, "other": "x"}, nil, `undeclared package file "other"`},
		{"a symbolic link", goodManifest, handler, func(dir string) error { return os.Symlink("/etc/passwd", filepath.Join(dir, "other")) }, "symlink"},
		{"an adapter with an entry", strings.Replace(adapterManifest, `"assets"`, `"entry":"p-codex-adapter","assets"`, 1), adapter, nil, "names no entry"},
		{"an adapter with another asset", strings.Replace(adapterManifest, `["p-codex-adapter"]`, `["p-codex-adapter","other"]`, 1), map[string]string{"p-codex-adapter": "x", "other": "y"}, nil, "must hold exactly the assets"},
		{"an adapter without its asset", adapterManifest, nil, nil, `missing asset "p-codex-adapter"`},
		{"an asset outside the package", strings.Replace(adapterManifest, `["p-codex-adapter"]`, `["../other"]`, 1), adapter, nil, "unsafe relative path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := writePackage(t, tt.manifest, tt.files)
			if tt.extra != nil {
				if err := tt.extra(dir); err != nil {
					t.Fatal(err)
				}
			}
			_, err := Conformance(dir)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want %q", err, tt.want)
			}
		})
	}
}

func TestConformanceAcceptsBothCapabilities(t *testing.T) {
	if _, err := Conformance(writePackage(t, goodManifest, map[string]string{"handler": handlerScript})); err != nil {
		t.Fatalf("an event handler with its entry: %v", err)
	}
	if _, err := Conformance(writePackage(t, adapterManifest, map[string]string{"p-codex-adapter": "#!/bin/sh\n"})); err != nil {
		t.Fatalf("an agent adapter with its asset: %v", err)
	}
}

func TestConformanceBoundsPackageEntries(t *testing.T) {
	pkg := testPackage(t)
	for i := 0; i < 128; i++ {
		name := filepath.Join(pkg.Path, fmt.Sprintf("asset-%03d", i))
		if err := os.WriteFile(name, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Conformance(pkg.Path); err == nil || !strings.Contains(err.Error(), "more than 128") {
		t.Fatalf("oversize package accepted: %v", err)
	}
}

// A native executable fits a package: an entry of 24 MiB conforms.
func TestConformanceAdmitsANativeExecutable(t *testing.T) {
	entry := strings.Repeat("x", 24<<20)
	if _, err := Conformance(writePackage(t, goodManifest, map[string]string{"handler": entry})); err != nil {
		t.Fatalf("a 24 MiB entry: %v", err)
	}
}
