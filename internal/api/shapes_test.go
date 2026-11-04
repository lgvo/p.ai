package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The daemon and its clients exchange only the method types: no client sends
// untyped params, and no handler returns a loose map. This parses their
// non-test Go files and fails on a call to CallRaw outside `p api` (cmd/p),
// on a map[string]any literal passed to a call, and on a map[string]any
// literal returned by a handler given to Register.
func TestNoLooseShapesOnTheWire(t *testing.T) {
	for _, dir := range []string{"../control", "../daemon", "../tui", "../attachment", "../../cmd/p", "../../tests/integration/cmd/tui-mock"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, problem := range looseShapes(file, filepath.Base(dir) == "p") {
				t.Errorf("%s: %s", fset.Position(problem.pos), problem.what)
			}
		}
	}
}

type looseShape struct {
	pos  token.Pos
	what string
}

// wireCalls are the calls whose arguments reach the wire: client calls and
// the TUI's call constructors, and handler registration.
var wireCalls = map[string]bool{"Call": true, "Do": true, "CallRaw": true, "Register": true, "callOf": true, "begin": true}

func looseShapes(file *ast.File, rawAllowed bool) []looseShape {
	maps := looseMapAliases(file)
	var found []looseShape
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			if n.Sel.Name == "CallRaw" && !rawAllowed {
				found = append(found, looseShape{n.Pos(), "CallRaw outside `p api`"})
			}
		case *ast.CallExpr:
			name := calleeName(n.Fun)
			if !wireCalls[name] {
				return true
			}
			for _, arg := range n.Args {
				if isLooseMap(arg, maps) {
					found = append(found, looseShape{arg.Pos(), "map[string]any passed to " + name})
				}
				if lit, ok := arg.(*ast.FuncLit); ok && name == "Register" {
					ast.Inspect(lit.Body, func(m ast.Node) bool {
						if ret, ok := m.(*ast.ReturnStmt); ok {
							for _, result := range ret.Results {
								if isLooseMap(result, maps) {
									found = append(found, looseShape{result.Pos(), "map[string]any returned by a handler"})
								}
							}
						}
						return true
					})
				}
			}
		}
		return true
	})
	return found
}

func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	case *ast.IndexExpr:
		return calleeName(f.X)
	case *ast.IndexListExpr:
		return calleeName(f.X)
	}
	return ""
}

// looseMapAliases are the file's type names declared as map[string]any.
func looseMapAliases(file *ast.File) map[string]bool {
	aliases := map[string]bool{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			if ts := spec.(*ast.TypeSpec); isLooseMapType(ts.Type, nil) {
				aliases[ts.Name.Name] = true
			}
		}
	}
	return aliases
}

func isLooseMap(e ast.Expr, aliases map[string]bool) bool {
	lit, ok := e.(*ast.CompositeLit)
	return ok && isLooseMapType(lit.Type, aliases)
}

func isLooseMapType(e ast.Expr, aliases map[string]bool) bool {
	switch t := e.(type) {
	case *ast.Ident:
		return aliases[t.Name]
	case *ast.MapType:
		key, ok := t.Key.(*ast.Ident)
		if !ok || key.Name != "string" {
			return false
		}
		switch v := t.Value.(type) {
		case *ast.Ident:
			return v.Name == "any"
		case *ast.InterfaceType:
			return len(v.Methods.List) == 0
		}
	}
	return false
}

// The guard itself: each loose shape it exists for is found.
func TestLooseShapeGuardFindsEachShape(t *testing.T) {
	src := `package x
type params = map[string]any
func f() {
	c.Call(ctx, "m", map[string]any{"v": 1}, &r)
	m.begin("m", params{"v": 1})
	api.CallRaw(ctx, s, "m", raw)
	control.Register(t, api.SystemHealth, func(context.Context, api.HealthRequest) (any, error) { return map[string]interface{}{}, nil })
	json.Marshal(map[string]any{"fine": true})
}`
	file, err := parser.ParseFile(token.NewFileSet(), "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if found := looseShapes(file, false); len(found) != 4 {
		t.Fatalf("found %d loose shapes, want 4: %+v", len(found), found)
	}
	if found := looseShapes(file, true); len(found) != 3 {
		t.Fatalf("with CallRaw allowed, found %d loose shapes, want 3", len(found))
	}
}
