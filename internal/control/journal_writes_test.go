package control

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestJournalWritesGoThroughOneFunction keeps every write of an operation's
// row on the journal's one write path: an UPDATE of operations is the query
// of a journalTx.update call, and the only INSERT is journalTx.insert's. A
// status change written any other way would end an operation without
// releasing its worker's hold, emitting its progress or waking its waiters.
func TestJournalWritesGoThroughOneFunction(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		allowed := map[*ast.BasicLit]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			if decl, ok := n.(*ast.FuncDecl); ok && decl.Recv != nil && decl.Name.Name == "insert" && name == "operation_journal.go" {
				ast.Inspect(decl, func(n ast.Node) bool {
					if lit, ok := n.(*ast.BasicLit); ok {
						allowed[lit] = true
					}
					return true
				})
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "update" && len(call.Args) >= 3 {
				if lit, ok := call.Args[2].(*ast.BasicLit); ok {
					allowed[lit] = true
				}
			}
			return true
		})
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			text := strings.Join(strings.Fields(lit.Value), " ")
			if (strings.Contains(text, "UPDATE operations") || strings.Contains(text, "INSERT INTO operations")) && !allowed[lit] {
				t.Errorf("%s: writes operations past the journal's write path", fset.Position(lit.Pos()))
			}
			return true
		})
	}
}
