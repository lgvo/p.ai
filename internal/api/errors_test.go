package api

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// ReasonConstants returns the value of every Reason constant declared in the
// Go files of dir, the api package's directory.
func reasonConstants(t *testing.T, dir string) map[string]string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[string]string{}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				value := spec.(*ast.ValueSpec)
				for i, ident := range value.Names {
					if !strings.HasPrefix(ident.Name, "Reason") || i >= len(value.Values) {
						continue
					}
					lit, ok := value.Values[i].(*ast.BasicLit)
					if !ok {
						t.Fatalf("%s is not a string literal", ident.Name)
					}
					reasons[ident.Name], _ = strconv.Unquote(lit.Value)
				}
			}
		}
	}
	return reasons
}

func TestEveryKindAndReasonIsDocumented(t *testing.T) {
	doc, err := os.ReadFile("../../docs/archive/control-api.md")
	if errors.Is(err, fs.ErrNotExist) {
		// The package build copies the sources without the docs; the
		// repository's unit tests run this check.
		t.Skip("docs/archive/control-api.md is not in this source tree")
	}
	if err != nil {
		t.Fatal(err)
	}
	text := string(doc)
	for _, kind := range Kinds() {
		row := "`" + string(kind) + "` | " + strconv.Itoa(kind.Code())
		if !strings.Contains(text, row) {
			t.Errorf("docs/archive/control-api.md lacks the row %q", row)
		}
	}
	reasons := reasonConstants(t, ".")
	if len(reasons) < 20 {
		t.Fatalf("found only %d reason constants", len(reasons))
	}
	for name, value := range reasons {
		if !strings.Contains(text, "`"+value+"`") {
			t.Errorf("docs/archive/control-api.md does not list %s (%q)", name, value)
		}
	}
}

func TestErrorObjectShape(t *testing.T) {
	for _, kind := range Kinds() {
		if kind.Code() == 0 || NewError(kind, "").Message != string(kind) {
			t.Errorf("%s has no code or a message other than its name", kind)
		}
	}
	if NewError(KindCancelled, "").Data != nil {
		t.Error("an error without a reason carries data")
	}
	if e := InvalidParam("limit"); e.Error() != "invalid: param: limit" || e.Code != -32602 {
		t.Errorf("InvalidParam: %+v", e)
	}
	if ErrorKind("busy").Code() != 0 || ErrorKind("unavailable").Code() != 0 {
		t.Error("the removed kinds still have codes")
	}
}

func TestBoundDiagnosticCutsAtACharacterBoundary(t *testing.T) {
	if got := BoundDiagnostic("short"); got != "short" {
		t.Errorf("a short diagnostic became %q", got)
	}
	ascii := strings.Repeat("a", MaxDiagnosticBytes+10)
	if got := BoundDiagnostic(ascii); got != ascii[:MaxDiagnosticBytes] {
		t.Errorf("an ASCII diagnostic kept %d bytes", len(got))
	}
	// "€" takes three bytes, so the limit falls inside one.
	euro := strings.Repeat("€", MaxDiagnosticBytes)
	got := BoundDiagnostic(euro)
	if len(got) != MaxDiagnosticBytes/3*3 || !utf8.ValidString(got) || !strings.HasPrefix(euro, got) {
		t.Errorf("a multibyte diagnostic kept %d bytes, valid %t", len(got), utf8.ValidString(got))
	}
}
