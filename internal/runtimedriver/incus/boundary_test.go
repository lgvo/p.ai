package incus

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// incusPackage is this package's import path.
const incusPackage = "github.com/lgvo/p.ai/internal/runtimedriver/incus"

// incusCode is the code that may reach Incus, relative to the module root: this
// package, the daemon file that constructs the driver (until the driver runs
// as its own executable), and the VM fixtures that exercise Incus directly. A
// directory covers every file below it.
var incusCode = []string{
	"internal/runtimedriver/incus",
	"internal/daemon/driver_setup.go",
	"tests/integration/cmd/attachment-fixture",
	"tests/integration/cmd/builder-fixture",
	"tests/integration/cmd/runtime-fixture",
}

// TestOnlyTheIncusDriverReachesIncus reads every Go file of the module outside
// incusCode. No file, test or not, may import this package. No non-test file
// may hold a string literal that names the Incus binary ("incus", or a path
// ending in "/bin/incus") or its sockets and client environment (a literal
// containing "unix.socket" or "INCUS_"). The one "incus" allowed is the value of
// control.IncusDriver, the driver's name in host configuration.
func TestOnlyTheIncusDriverReachesIncus(t *testing.T) {
	root := moduleRoot(t)
	daemonFiles := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && skippedDir(path, d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || isIncusCode(rel) {
			return nil
		}
		if strings.HasPrefix(rel, "internal/daemon/") {
			daemonFiles++
		}
		for _, finding := range reachesIncus(t, path, rel) {
			t.Errorf("%s %s", rel, finding)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if daemonFiles == 0 {
		t.Fatalf("no daemon file was read under %s", root)
	}
}

// moduleRoot is the nearest directory above the test's that holds go.mod.
func moduleRoot(t *testing.T) string {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test")
		}
		dir = parent
	}
}

// skippedDir is a directory outside the module's own sources: hidden ones,
// vendored dependencies, test data, the rework notes and nested modules.
func skippedDir(path, name string) bool {
	if strings.HasPrefix(name, ".") || name == "vendor" || name == "testdata" || name == "rework" {
		return true
	}
	_, err := os.Stat(filepath.Join(path, "go.mod"))
	return err == nil
}

func isIncusCode(rel string) bool {
	for _, allowed := range incusCode {
		if rel == allowed || strings.HasPrefix(rel, allowed+"/") {
			return true
		}
	}
	return false
}

// reachesIncus is what the file at path does that only Incus code may do.
func reachesIncus(t *testing.T, path, rel string) []string {
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var findings []string
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == incusPackage {
			findings = append(findings, "imports the Incus driver")
		}
	}
	if strings.HasSuffix(rel, "_test.go") {
		return findings
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.ValueSpec:
			if rel == "internal/control/config.go" && len(n.Names) == 1 && n.Names[0].Name == "IncusDriver" {
				return false
			}
		case *ast.BasicLit:
			if s, err := strconv.Unquote(n.Value); n.Kind == token.STRING && err == nil && namesIncus(s) {
				findings = append(findings, "names Incus in "+n.Value)
			}
		}
		return true
	})
	return findings
}

// namesIncus reports a literal that names the Incus binary, its sockets or its
// client's environment.
func namesIncus(s string) bool {
	return s == "incus" || strings.HasSuffix(s, "/bin/incus") || strings.Contains(s, "unix.socket") || strings.Contains(s, "INCUS_")
}
