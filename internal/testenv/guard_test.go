package testenv_test

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

func TestPackagesInvokeScrubEnv(t *testing.T) {
	// Resolve the module root so the guard covers every package, including itself.
	module, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "list", "-json", "./...")
	command.Dir = string(bytes.TrimSpace(module))
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	for {
		var pkg struct {
			Dir, ImportPath           string
			TestGoFiles, XTestGoFiles []string
		}
		if err := decoder.Decode(&pkg); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		files := slices.Concat(pkg.TestGoFiles, pkg.XTestGoFiles)
		if len(files) == 0 {
			continue
		}
		count := 0
		for _, name := range files {
			file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(pkg.Dir, name), nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Name.Name != "TestMain" {
					continue
				}
				count++
				if !scrubsBeforeRun(file, fn) {
					t.Errorf("%s: TestMain must call testenv.ScrubEnv before m.Run", pkg.ImportPath)
				}
			}
		}
		if count != 1 {
			t.Errorf("%s: want one TestMain calling testenv.ScrubEnv, found %d", pkg.ImportPath, count)
		}
	}
}

func scrubsBeforeRun(file *ast.File, fn *ast.FuncDecl) bool {
	alias := ""
	for _, imp := range file.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		if path == "github.com/sharper-flow/concord/internal/testenv" {
			alias = "testenv"
			if imp.Name != nil {
				alias = imp.Name.Name
			}
		}
	}
	if alias == "" || fn.Body == nil || len(fn.Type.Params.List) != 1 || len(fn.Type.Params.List[0].Names) != 1 {
		return false
	}
	m := fn.Type.Params.List[0].Names[0].Name
	scrubbed := false
	for _, stmt := range fn.Body.List {
		valid := true
		ast.Inspect(stmt, func(node ast.Node) bool {
			if call, ok := node.(*ast.CallExpr); ok && selector(call, m, "Run") && !scrubbed {
				valid = false
			}
			return true
		})
		if !valid {
			return false
		}
		// Require an unconditional, synchronous scrub in an earlier statement.
		if assignment, ok := stmt.(*ast.AssignStmt); ok {
			for _, value := range assignment.Rhs {
				if call, ok := value.(*ast.CallExpr); ok && selector(call, alias, "ScrubEnv") {
					scrubbed = true
				}
			}
		}
	}
	return scrubbed
}

func selector(call *ast.CallExpr, receiver, name string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == receiver && sel.Sel.Name == name
}

func TestScrubGuard(t *testing.T) {
	for _, tc := range []struct {
		name, body, importPath string
		want                   bool
	}{
		{"valid", "dir := testenv.ScrubEnv(); code := m.Run(); _ = dir; _ = code", "github.com/sharper-flow/concord/internal/testenv", true},
		{"missing", "m.Run()", "github.com/sharper-flow/concord/internal/testenv", false},
		{"late", "m.Run(); dir := testenv.ScrubEnv(); _ = dir", "github.com/sharper-flow/concord/internal/testenv", false},
		{"same_statement", "code, dir := m.Run(), testenv.ScrubEnv(); _ = code; _ = dir", "github.com/sharper-flow/concord/internal/testenv", false},
		{"deferred", "defer testenv.ScrubEnv(); m.Run()", "github.com/sharper-flow/concord/internal/testenv", false},
		{"conditional", "if false { testenv.ScrubEnv() }; m.Run()", "github.com/sharper-flow/concord/internal/testenv", false},
		{"wrong_import", "dir := testenv.ScrubEnv(); m.Run(); _ = dir", "example.com/testenv", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := "package fixture\nimport testenv " + strconv.Quote(tc.importPath) + "\nfunc TestMain(m *testing.M) {" + tc.body + "}"
			file, err := parser.ParseFile(token.NewFileSet(), "fixture_test.go", source, 0)
			if err != nil {
				t.Fatal(err)
			}
			fn := file.Decls[1].(*ast.FuncDecl)
			if got := scrubsBeforeRun(file, fn); got != tc.want {
				t.Fatalf("guard = %v, want %v", got, tc.want)
			}
		})
	}
}
