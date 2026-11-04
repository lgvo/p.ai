package git

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The tests here read the module's code for the source driver's boundaries.
// The boundaries of its process are checked where it is started:
//   - the launch checks, which refuse an executable as they refuse the
//     runtime driver's, since both drivers start through the same code:
//     internal/driverwire/launch check_test.go (TestLaunchChecksRefuse and
//     its neighbors) and launch_test.go (TestTheFileCheckedIsTheFileStarted);
//   - its environment, only the daemon's HOME and SSH_AUTH_SOCK:
//     internal/sourcedriver/supervise
//     TestTheDriverGetsOnlyTheDaemonsHomeAndSSHAgent;
//   - the backend pin: internal/daemon git_test.go
//     TestTheDaemonRefusesToStartWithAnotherBackendWhileAProjectExists.

// module is the import path of P's module.
const module = "github.com/lgvo/p.ai"

// bundledDriver is this package's directory, relative to the module root.
const bundledDriver = "internal/sourcedriver/git"

// executable is the directory of p-source-git, the executable that serves
// this package to the daemon, relative to the module root.
const executable = "cmd/p-source-git"

// testCode is the module's code that only tests run, besides _test.go files.
const testCode = "tests/integration"

// mayImportTheBundledDriver are the code outside this package that may
// import it, files or directories: p-source-git, the executable that serves
// it and runs its pre-receive hook, and test fixtures that run it in
// process: the daemon's, for tests that need real Git, and the VM fixtures
// that start it and run its hook.
var mayImportTheBundledDriver = []string{
	executable,
	"internal/daemon/git_fixture_test.go",
	"tests/integration/cmd/git-fixture/main.go",
	"tests/integration/internal/bundledgit/bundledgit.go",
}

// guestCode runs inside a session or a builder, never on the host, so the Git
// it runs is the guest's: runtime-kit and its executable.
var guestCode = []string{
	"internal/runtimekit",
	"cmd/p-runtime-kit",
}

// guestArguments is code that may hold Git's or SSH's path for a guest, with
// the function that runs a command there: the Incus driver passes the
// builder's Git in the arguments of `incus exec`, which runs it inside the
// builder. There the path may only be the value of a constant, used only in a
// []string literal that goes to that function and nowhere else; the programs
// the code starts itself are still checked.
var guestArguments = map[string]string{
	"internal/runtimedriver/incus": "builderGuestExecEnv",
}

// storageNames are the fixed names the bundled driver gives what it keeps in
// its storage, which is P's state directory: the repositories, their lock and
// each one's incarnation file, the Git service's hooks and its records.
var storageNames = []string{"repositories", ".p-lock", "p-incarnation", "git-hooks", "git-services"}

// knowTheStorage are the test fixtures outside this package that plant and
// inspect Git objects in the bundled driver's repositories natively, as Git
// or a session's push would.
var knowTheStorage = []string{
	"internal/daemon/git_fixture_test.go",
	"tests/integration/internal/bundledgit/bundledgit.go",
}

// launches are the standard library's ways to start a program, with the
// index of the argument that names the program, by package and function.
var launches = map[string]map[string]int{
	"os/exec": {"Command": 0, "CommandContext": 1, "LookPath": 0},
	"os":      {"StartProcess": 0},
	"syscall": {"Exec": 0, "ForkExec": 0, "StartProcess": 0},
}

// TestNoOtherCodeImportsTheBundledDriver reads every Go file of the module,
// tests too: none outside this package imports it, except those in
// mayImportTheBundledDriver, so the daemon reaches the bundled driver only
// as the p-source-git executable, through a sourcedriver.Driver.
func TestNoOtherCodeImportsTheBundledDriver(t *testing.T) {
	for dir, files := range moduleCode(t) {
		if dir == bundledDriver {
			continue
		}
		for _, f := range files {
			if imports(f.syntax, module+"/"+bundledDriver) && !inside(f.rel, mayImportTheBundledDriver) {
				t.Errorf("%s imports the bundled source driver", f.rel)
			}
		}
	}
}

// TestOnlyTheBundledDriverRunsGitOrSSHOnTheHost reads every production
// package outside this one and guestCode. No program its code starts may be
// named git or ssh, or be a path ending in them, as far as string literals,
// concatenations, path joins and the package's constants and variables name
// it; a program the test cannot name at all, such as a parameter, is not
// judged. And no file may hold the path of a git or ssh binary, which its
// code could hand on to start, except as guestArguments allows.
func TestOnlyTheBundledDriverRunsGitOrSSHOnTheHost(t *testing.T) {
	for dir, files := range productionCode(t) {
		if dir == bundledDriver || inside(dir, guestCode) {
			continue
		}
		global := namedValues(packageDecls(files)...)
		for _, f := range files {
			for _, program := range startedPrograms(f.syntax, global) {
				t.Errorf("%s runs %q on the host", f.rel, program)
			}
		}
		executor := ""
		for guest, function := range guestArguments {
			if within(dir, guest) {
				executor = function
			}
		}
		for _, finding := range binaryPaths(files, executor) {
			t.Error(finding)
		}
	}
}

// TestOnlyTheBundledDriverOpensItsStorage reads every Go file of the module
// outside this package and knowTheStorage, tests too: none builds a path
// through one of storageNames, in a string literal with a slash, a
// concatenation or a path or filepath Join, as far as string literals and the
// package's constants and variables name it.
func TestOnlyTheBundledDriverOpensItsStorage(t *testing.T) {
	for dir, files := range moduleCode(t) {
		if dir == bundledDriver {
			continue
		}
		global := namedValues(packageDecls(files)...)
		for _, f := range files {
			if slices.Contains(knowTheStorage, f.rel) {
				continue
			}
			for _, p := range storagePaths(f.syntax, global) {
				t.Errorf("%s builds a path through %q into the bundled driver's storage", f.rel, p)
			}
		}
	}
}

// TestTheBundledDriverImportsNoSQLiteOrRuntimeDriverCode follows the
// production imports of this package and of p-source-git, the executable
// that serves it, through the module: no package they reach imports SQLite
// (modernc.org/sqlite, or internal/control, which holds P's database) or
// runtime driver code (internal/runtimedriver and below).
func TestTheBundledDriverImportsNoSQLiteOrRuntimeDriverCode(t *testing.T) {
	packages := productionCode(t)
	reached := map[string]bool{bundledDriver: true, executable: true}
	for queue := []string{bundledDriver, executable}; len(queue) > 0; queue = queue[1:] {
		for _, f := range packages[queue[0]] {
			for _, imp := range f.syntax.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				if within(p, "modernc.org/sqlite") || within(p, module+"/internal/control") || within(p, module+"/internal/runtimedriver") {
					t.Errorf("%s, which the bundled driver or p-source-git reaches, imports %s", f.rel, p)
					continue
				}
				if dir, ok := strings.CutPrefix(p, module+"/"); ok && !reached[dir] {
					reached[dir] = true
					queue = append(queue, dir)
				}
			}
		}
	}
}

// sourceFile is a parsed Go file, with its path relative to the module root.
type sourceFile struct {
	rel    string
	syntax *ast.File
	test   bool // a _test.go file, or in testCode
}

// productionCode is the module's code without its test code.
func productionCode(t *testing.T) map[string][]sourceFile {
	production := map[string][]sourceFile{}
	for dir, files := range moduleCode(t) {
		for _, f := range files {
			if !f.test {
				production[dir] = append(production[dir], f)
			}
		}
	}
	return production
}

// moduleCode parses the module's Go files, by package directory relative to
// the module root.
func moduleCode(t *testing.T) map[string][]sourceFile {
	root := moduleRoot(t)
	packages := map[string][]sourceFile{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && skippedDir(p, d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") {
			return nil
		}
		syntax, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		test := strings.HasSuffix(rel, "_test.go") || within(rel, testCode)
		packages[path.Dir(rel)] = append(packages[path.Dir(rel)], sourceFile{rel: rel, syntax: syntax, test: test})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(packages["internal/daemon"]) == 0 {
		t.Fatalf("no daemon file was read under %s", root)
	}
	return packages
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

// inside reports whether dir is one of dirs or below one.
func inside(dir string, dirs []string) bool {
	return slices.ContainsFunc(dirs, func(d string) bool { return within(dir, d) })
}

// within reports whether path p is root or below it.
func within(p, root string) bool {
	return p == root || strings.HasPrefix(p, root+"/")
}

func imports(f *ast.File, pkg string) bool {
	return slices.ContainsFunc(f.Imports, func(imp *ast.ImportSpec) bool {
		p, _ := strconv.Unquote(imp.Path.Value)
		return p == pkg
	})
}

func stringLiteral(n ast.Node) (string, bool) {
	lit, ok := n.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}

// binaryPaths are the places in files that hold the path of a git or ssh
// binary. Where executor is not empty, the path may be the value of a
// constant, used only as an element of a []string literal that only executor
// gets (see reachesGuest).
func binaryPaths(files []sourceFile, executor string) []string {
	constants := map[string]bool{}  // the names of constants that hold a path
	declared := map[ast.Node]bool{} // their names and values where declared
	for _, f := range files {
		ast.Inspect(f.syntax, func(n ast.Node) bool {
			if gen, ok := n.(*ast.GenDecl); ok && executor != "" && gen.Tok == token.CONST {
				for _, spec := range gen.Specs {
					v := spec.(*ast.ValueSpec)
					for i, value := range v.Values {
						if s, ok := stringLiteral(value); ok && binaryPath(s) && i < len(v.Names) {
							constants[v.Names[i].Name] = true
							declared[v.Names[i]], declared[value] = true, true
						}
					}
				}
			}
			return true
		})
	}
	var found []string
	for _, f := range files {
		for _, decl := range f.syntax.Decls {
			parents := parentsOf(decl)
			ast.Inspect(decl, func(n ast.Node) bool {
				if declared[n] {
					return true
				}
				if s, ok := stringLiteral(n); ok && binaryPath(s) {
					found = append(found, fmt.Sprintf("%s holds the path of %q", f.rel, s))
				}
				if name, ok := n.(*ast.Ident); ok && constants[name.Name] && !selected(parents, name) && !reachesGuest(name, parents, decl, executor) {
					found = append(found, fmt.Sprintf("%s uses %s, the guest's path of Git or SSH, other than in arguments only %s gets", f.rel, name.Name, executor))
				}
				return true
			})
		}
	}
	return found
}

// reachesGuest reports whether name, in decl, is an element of a []string
// literal that goes only to executor: as one of its arguments, directly or
// through append, or through a variable used only as one of its arguments.
func reachesGuest(name *ast.Ident, parents map[ast.Node]ast.Node, decl ast.Node, executor string) bool {
	literal, ok := parents[name].(*ast.CompositeLit)
	if !ok || !stringSlice(literal) {
		return false
	}
	var argv ast.Expr = literal
	if call, ok := parents[argv].(*ast.CallExpr); ok && builtin(call.Fun, "append") && call.Args[0] == argv {
		argv = call
	}
	if argumentOf(parents, argv, executor) {
		return true
	}
	assign, ok := parents[argv].(*ast.AssignStmt)
	if !ok || len(assign.Lhs) != len(assign.Rhs) {
		return false
	}
	variable, ok := assign.Lhs[slices.Index(assign.Rhs, argv)].(*ast.Ident)
	if !ok {
		return false
	}
	only := true
	ast.Inspect(decl, func(n ast.Node) bool {
		if use, ok := n.(*ast.Ident); ok && use != variable && use.Name == variable.Name && !selected(parents, use) && !argumentOf(parents, use, executor) {
			only = false
		}
		return true
	})
	return only
}

// argumentOf reports whether e is an argument of a call of the function or
// method named function.
func argumentOf(parents map[ast.Node]ast.Node, e ast.Expr, function string) bool {
	call, ok := parents[e].(*ast.CallExpr)
	if !ok || !slices.Contains(call.Args, e) {
		return false
	}
	switch f := call.Fun.(type) {
	case *ast.SelectorExpr:
		return f.Sel.Name == function
	case *ast.Ident:
		return f.Name == function
	}
	return false
}

// parentsOf maps each node below root to the node that holds it.
func parentsOf(root ast.Node) map[ast.Node]ast.Node {
	parents := map[ast.Node]ast.Node{}
	var stack []ast.Node
	ast.Inspect(root, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		if len(stack) > 0 {
			parents[n] = stack[len(stack)-1]
		}
		stack = append(stack, n)
		return true
	})
	return parents
}

// selected reports a name selected from a value or package, such as a field.
func selected(parents map[ast.Node]ast.Node, name *ast.Ident) bool {
	selector, ok := parents[name].(*ast.SelectorExpr)
	return ok && selector.Sel == name
}

// stringSlice reports a []string literal.
func stringSlice(literal *ast.CompositeLit) bool {
	slice, ok := literal.Type.(*ast.ArrayType)
	return ok && slice.Len == nil && builtin(slice.Elt, "string")
}

// builtin reports e is the name of the predeclared identifier name.
func builtin(e ast.Expr, name string) bool {
	ident, ok := e.(*ast.Ident)
	return ok && ident.Name == name
}

// storagePaths are the paths f builds through one of storageNames: string
// literals with a slash, concatenations, and path or filepath Joins, resolved
// through the assignments of the declaration that builds them, then through
// global's.
func storagePaths(f *ast.File, global map[string][]ast.Expr) []string {
	var found []string
	for _, decl := range f.Decls {
		local := namedValues(decl)
		ast.Inspect(decl, func(n ast.Node) bool {
			var built []string
			switch n := n.(type) {
			case *ast.BasicLit:
				if s, ok := stringLiteral(n); ok && strings.Contains(s, "/") {
					built = []string{s}
				}
			case *ast.BinaryExpr:
				if n.Op == token.ADD {
					built = values(n, local, global, map[string]bool{})
				}
			case *ast.CallExpr:
				if pathJoin(n) {
					built = values(n, local, global, map[string]bool{})
				}
			}
			before := len(found)
			for _, p := range built {
				if inStorage(p) {
					found = append(found, p)
				}
			}
			// A path found is reported once, not again for its parts.
			return len(found) == before
		})
	}
	return found
}

// inStorage reports a path with one of storageNames as an element.
func inStorage(p string) bool {
	return slices.ContainsFunc(strings.Split(p, "/"), func(element string) bool { return slices.Contains(storageNames, element) })
}

// pathJoin reports a call of path.Join or filepath.Join.
func pathJoin(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Join" {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	return ok && (pkg.Name == "filepath" || pkg.Name == "path")
}

// binaryPath reports the path of a git or ssh binary.
func binaryPath(s string) bool {
	return strings.HasSuffix(s, "/bin/git") || strings.HasSuffix(s, "/bin/ssh")
}

// startedPrograms are the programs named git or ssh, or a path ending in
// them, that f starts through a function of launches. A name is resolved
// through the assignments of the declaration that starts the program, then
// through global's.
func startedPrograms(f *ast.File, global map[string][]ast.Expr) []string {
	launchers := map[string]map[string]int{}
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		if functions, ok := launches[p]; ok {
			name := path.Base(p)
			if imp.Name != nil {
				name = imp.Name.Name
			}
			launchers[name] = functions
		}
	}
	var found []string
	for _, decl := range f.Decls {
		local := namedValues(decl)
		ast.Inspect(decl, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := selector.X.(*ast.Ident)
			if !ok {
				return true
			}
			index, ok := launchers[pkg.Name][selector.Sel.Name]
			if !ok || index >= len(call.Args) {
				return true
			}
			for _, program := range values(call.Args[index], local, global, map[string]bool{}) {
				if base := path.Base(program); base == "git" || base == "ssh" {
					found = append(found, program)
				}
			}
			return true
		})
	}
	return found
}

// packageDecls are the package-level declarations of files.
func packageDecls(files []sourceFile) []ast.Node {
	var decls []ast.Node
	for _, f := range files {
		for _, decl := range f.syntax.Decls {
			if gen, ok := decl.(*ast.GenDecl); ok {
				decls = append(decls, gen)
			}
		}
	}
	return decls
}

// namedValues are the expressions assigned to each name in nodes, by
// declaration or assignment.
func namedValues(nodes ...ast.Node) map[string][]ast.Expr {
	named := map[string][]ast.Expr{}
	add := func(names []ast.Expr, values []ast.Expr) {
		if len(names) != len(values) {
			return
		}
		for i, name := range names {
			if ident, ok := name.(*ast.Ident); ok {
				named[ident.Name] = append(named[ident.Name], values[i])
			}
		}
	}
	for _, node := range nodes {
		ast.Inspect(node, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.ValueSpec:
				names := make([]ast.Expr, len(n.Names))
				for i, name := range n.Names {
					names[i] = name
				}
				add(names, n.Values)
			case *ast.AssignStmt:
				add(n.Lhs, n.Rhs)
			}
			return true
		})
	}
	return named
}

// unknown stands for a part of a string values cannot resolve.
const unknown = "…"

// values are the strings e can hold, as far as string literals, names in
// local and global, concatenation and path joins tell, with unknown for each
// part they do not tell. seen holds the names being resolved, so a name that
// refers to itself ends.
func values(e ast.Expr, local, global map[string][]ast.Expr, seen map[string]bool) []string {
	switch e := e.(type) {
	case *ast.BasicLit:
		if s, ok := stringLiteral(e); ok {
			return []string{s}
		}
	case *ast.ParenExpr:
		return values(e.X, local, global, seen)
	case *ast.Ident:
		if seen[e.Name] {
			break
		}
		seen[e.Name] = true
		defer delete(seen, e.Name)
		var all []string
		for _, assigned := range append(slices.Clone(local[e.Name]), global[e.Name]...) {
			all = append(all, values(assigned, local, global, seen)...)
		}
		if len(all) > 0 {
			return all
		}
	case *ast.BinaryExpr:
		if e.Op == token.ADD {
			return joined(values(e.X, local, global, seen), values(e.Y, local, global, seen), "")
		}
	case *ast.CallExpr:
		if pathJoin(e) {
			all, sep := []string{""}, ""
			for _, arg := range e.Args {
				all, sep = joined(all, values(arg, local, global, seen), sep), "/"
			}
			return all
		}
	}
	return []string{unknown}
}

// joined is every left followed by sep and every right.
func joined(lefts, rights []string, sep string) []string {
	var all []string
	for _, left := range lefts {
		for _, right := range rights {
			all = append(all, left+sep+right)
		}
	}
	return all
}
