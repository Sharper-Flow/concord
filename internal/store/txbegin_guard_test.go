package store

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTxBeginGuard(t *testing.T) {
	t.Parallel()
	root := txScopeRepoRoot()
	readBegins, writeBegins, files := 0, 0, 0
	fset := token.NewFileSet()
	packages := map[string]map[string]*ast.File{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			if strings.HasPrefix(entry.Name(), ".") || entry.Name() == "node_modules" || entry.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		// These packages implement test fixtures, including deliberate foreign
		// write locks. No production file may import them. This is a package
		// boundary, not an exemption for a production transaction call site.
		if txBeginTestSupport(rel) {
			return nil
		}
		files++
		dir := filepath.Dir(rel)
		if packages[dir] == nil {
			packages[dir] = map[string]*ast.File{}
		}
		packages[dir][rel] = file
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	imports := txBeginImporter(t, fset)
	for dir, pkg := range packages {
		info := txBeginTypes(t, "github.com/sharper-flow/concord/"+filepath.ToSlash(dir), fset, pkg, imports, false)
		if info == nil {
			t.Fatalf("cannot type-check production package %s", dir)
		}
		for path, file := range pkg {
			findings, reads, writes := scanTxBeginFile(path, file, fset, info)
			readBegins += reads
			writeBegins += writes
			for _, finding := range findings {
				t.Error(finding)
			}
		}
	}
	if files < 30 || readBegins != 1 || writeBegins != 2 {
		t.Fatalf("begin discovery: %d production files, %d read begins, %d write begins; want at least 30 files and exactly 1/2 owner begins", files, readBegins, writeBegins)
	}
}

func txBeginTestSupport(path string) bool {
	return strings.HasPrefix(path, "internal/pm1fixture/") || strings.HasPrefix(path, "internal/store/storetest/")
}

func TestTxBeginGuardBites(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, path, source string
	}{
		{"raw DB", "internal/store/query.go", `package store; func bad() { db.BeginTx(ctx, nil) }`},
		{"raw Conn", "internal/store/query.go", `package store; func bad() { conn.BeginTx(ctx, nil) }`},
		{"legacy begin", "internal/store/query.go", `package store; func bad() { db.Begin() }`},
		{"method value", "internal/store/query.go", `package store; var begin = db.BeginTx`},
		{"method expression", "internal/store/query.go", `package store; var begin = (*sql.DB).BeginTx`},
		{"foreign package", "internal/other/read.go", `package other; func bad() { s.DatabaseForTesting().BeginTx(ctx, nil) }`},
		{"production fixture import", "internal/other/read.go", `package other; import "github.com/sharper-flow/concord/internal/pm1fixture"`},
		{"production neighbor import", "internal/other/read.go", `package other; import "github.com/sharper-flow/concord/internal/store/storetest/neighbor"`},
		{"raw SQL", "internal/store/query.go", `package store; func bad() { conn.ExecContext(ctx, "BEGIN IMMEDIATE") }`},
		{"raw SQL constant", "internal/store/query.go", `package store; const begin = "BEGIN " + "TRANSACTION"; func bad() { db.Exec(begin) }`},
		{"split SQL constants", "internal/store/query.go", `package store; const pre = "BE"; const suffix = "GIN"; func bad() { db.Exec(pre + suffix) }`},
		{"SQL reassignment", "internal/store/query.go", `package store; func bad() { q := "BEGIN IMMEDIATE"; db.Exec(q); q = "SELECT 1" }`},
		{"SQL shadowing", "internal/store/query.go", `package store; func bad() { q := "BEGIN IMMEDIATE"; db.Exec(q) }; func other() { q := "SELECT 1"; db.Exec(q) }`},
		{"SQL leading comment", "internal/store/query.go", `package store; func bad() { db.Exec("/* snapshot */ BEGIN IMMEDIATE") }`},
		{"SQL inter-token comment", "internal/store/query.go", `package store; func bad() { db.Exec("BEGIN/* snapshot */IMMEDIATE") }`},
		{"SQL line comment", "internal/store/query.go", `package store; func bad() { db.Exec("-- snapshot\nBEGIN IMMEDIATE") }`},
		{"SQL later statement", "internal/store/query.go", `package store; func bad() { db.Exec("SELECT 1; BEGIN IMMEDIATE") }`},
		{"SQL method value", "internal/store/query.go", `package store; func bad() { exec := conn.ExecContext; exec(ctx, "BEGIN IMMEDIATE") }`},
		{"SQL method expression", "internal/store/query.go", `package store; func bad() { exec := (*sql.Conn).ExecContext; exec(conn, ctx, "BEGIN IMMEDIATE") }`},
		{"read owner nil", "internal/store/transaction.go", `package store; func beginReadTx() { db.BeginTx(ctx, nil) }`},
		{"read owner false", "internal/store/transaction.go", `package store; import "database/sql"; func beginReadTx() { db.BeginTx(ctx, &sql.TxOptions{ReadOnly: false}) }`},
		{"read owner shadowed true", "internal/store/transaction.go", `package store; import "database/sql"; func beginReadTx() { true := false; db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true}) }`},
		{"read owner indirect options", "internal/store/transaction.go", `package store; func beginReadTx() { db.BeginTx(ctx, options) }`},
		{"read owner method capture", "internal/store/transaction.go", `package store; func beginReadTx() { begin := db.BeginTx; begin(ctx, nil) }`},
		{"read owner closure", "internal/store/transaction.go", `package store; import "database/sql"; func beginReadTx() { f := func() { db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true}) }; f() }`},
		{"owner name elsewhere", "internal/other/read.go", `package other; func beginReadTx() { db.BeginTx(ctx, nil) }`},
		{"owner method", "internal/store/durable.go", `package store; func (s *Store) beginWriteTx() { db.BeginTx(ctx, nil) }`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, tt.path, tt.source, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			info := txBeginFixtureTypes(t, fset, map[string]*ast.File{tt.path: file})
			findings, _, _ := scanTxBeginFile(tt.path, file, fset, info)
			if len(findings) != 1 || !strings.HasPrefix(findings[0], tt.path+":1:") {
				t.Fatalf("want one call-site finding, got %v", findings)
			}
		})
	}
}

func TestTxBeginGuardOwners(t *testing.T) {
	t.Parallel()
	tests := []struct {
		path, source  string
		reads, writes int
	}{
		{"internal/store/transaction.go", `package store; import dbsql "database/sql"; func beginReadTx() { db.BeginTx(ctx, &dbsql.TxOptions{ReadOnly: true}) }`, 1, 0},
		{"internal/store/durable.go", `package store; func beginWriteTx() { db.BeginTx(ctx, nil); conn.BeginTx(ctx, nil) }`, 0, 2},
		{"internal/store/query.go", `package store; func read() { beginReadTx(ctx, db) }`, 0, 0},
		{"internal/store/schema.go", "package store; func trigger() { db.Exec(`CREATE TRIGGER t AFTER INSERT ON x BEGIN SELECT 1; END`) }", 0, 0},
		{"internal/store/query.go", `package store; func quoted() { db.Exec("SELECT '; BEGIN IMMEDIATE', 'can''t; BEGIN', \"; BEGIN\", [; BEGIN], ` + "`" + `; BEGIN` + "`" + ` /* ; BEGIN */ -- ; BEGIN\n") }`, 0, 0},
	}
	for _, tt := range tests {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, tt.path, tt.source, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		info := txBeginFixtureTypes(t, fset, map[string]*ast.File{tt.path: file})
		findings, reads, writes := scanTxBeginFile(tt.path, file, fset, info)
		if len(findings) != 0 || reads != tt.reads || writes != tt.writes {
			t.Errorf("%s: findings=%v, reads=%d, writes=%d", tt.path, findings, reads, writes)
		}
	}
}

func TestTxBeginGuardPackageConstants(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	sources := map[string]string{
		"prefix.go": `package store; const prefix = "BE"`,
		"suffix.go": `package store; const suffix = "GIN"`,
		"query.go":  `package store; func bad() { db.Exec(prefix + suffix) }`,
	}
	files := map[string]*ast.File{}
	for path, source := range sources {
		file, err := parser.ParseFile(fset, path, source, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		files[path] = file
	}
	info := txBeginFixtureTypes(t, fset, files)
	findings, _, _ := scanTxBeginFile("internal/store/query.go", files["query.go"], fset, info)
	if len(findings) != 1 || !strings.HasPrefix(findings[0], "internal/store/query.go:1:") {
		t.Fatalf("want one cross-file SQL finding, got %v", findings)
	}
}

// Export data gives the standard type checker module-aware imports. It resolves
// package and imported constants without deprecated parser object resolution.
var txBeginExports = sync.OnceValues(func() (map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "list", "-deps", "-export", "-json", "./...")
	cmd.Dir = txScopeRepoRoot()
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("go list exports: %w\n%s", err, output)
	}
	exports := map[string]string{}
	decoder := json.NewDecoder(strings.NewReader(string(output)))
	for {
		var pkg struct{ ImportPath, Export string }
		err := decoder.Decode(&pkg)
		if err == io.EOF {
			return exports, nil
		}
		if err != nil {
			return nil, err
		}
		if pkg.Export != "" {
			exports[pkg.ImportPath] = pkg.Export
		}
	}
})

func txBeginImporter(t *testing.T, fset *token.FileSet) types.Importer {
	t.Helper()
	exports, err := txBeginExports()
	if err != nil {
		t.Fatal(err)
	}
	return importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		export, ok := exports[path]
		if !ok {
			return nil, fmt.Errorf("missing export data for %s", path)
		}
		return os.Open(export)
	})
}

func txBeginTypes(t *testing.T, pkgPath string, fset *token.FileSet, files map[string]*ast.File, imports types.Importer, fixture bool) *types.Info {
	t.Helper()
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Uses: map[*ast.Ident]types.Object{}}
	config := types.Config{Importer: imports, Error: func(err error) {
		if !fixture {
			t.Error(err)
		}
	}}
	sources := make([]*ast.File, 0, len(files))
	for _, file := range files {
		sources = append(sources, file)
	}
	_, err := config.Check(pkgPath, fset, sources, info)
	if err != nil && !fixture {
		return nil
	}
	return info
}

func txBeginFixtureTypes(t *testing.T, fset *token.FileSet, files map[string]*ast.File) *types.Info {
	t.Helper()
	var name string
	for _, file := range files {
		name = file.Name.Name
		break
	}
	support := `package ` + name + `
type fixtureDB struct{}
func (fixtureDB) BeginTx(any, any) {}
func (fixtureDB) Begin() {}
func (fixtureDB) Exec(...any) {}
func (fixtureDB) ExecContext(...any) {}
var db, conn fixtureDB
var ctx, options any
type Store struct{}
func (*Store) DatabaseForTesting() fixtureDB { return db }
var s *Store
`
	file, err := parser.ParseFile(fset, "fixture-support.go", support, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	files["fixture-support.go"] = file
	// Bite snippets intentionally omit unrelated declarations or use invalid
	// expressions. Only production type-check failures must refuse discovery.
	return txBeginTypes(t, "fixture/"+name, fset, files, txBeginImporter(t, fset), true)
}

// scanTxBeginFile admits only direct begins in the two owning functions. It
// also visits their bodies: a helper name cannot hide a read option regression
// or a captured method that can later receive different options.
func scanTxBeginFile(path string, file *ast.File, fset *token.FileSet, info *types.Info) ([]string, int, int) {
	var findings []string
	reads, writes := 0, 0
	for _, imp := range file.Imports {
		name, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			findings = append(findings, path+":"+strconv.Itoa(fset.Position(imp.Pos()).Line)+": invalid import: "+err.Error())
			continue
		}
		if txBeginTestSupport(strings.TrimPrefix(name, "github.com/sharper-flow/concord/") + "/") {
			findings = append(findings, path+":"+strconv.Itoa(fset.Position(imp.Pos()).Line)+": production imports transaction test support "+name)
		}
	}
	readCalls := map[*ast.SelectorExpr]*ast.CallExpr{}
	writeCalls := map[*ast.SelectorExpr]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Body == nil {
			continue
		}
		readOwner := path == "internal/store/transaction.go" && fn.Name.Name == "beginReadTx"
		writeOwner := path == "internal/store/durable.go" && fn.Name.Name == "beginWriteTx"
		if !readOwner && !writeOwner {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			if _, closure := node.(*ast.FuncLit); closure {
				return false
			}
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if ok && sel.Sel.Name == "BeginTx" {
				if readOwner {
					readCalls[sel] = call
				} else {
					writeCalls[sel] = true
				}
			}
			return true
		})
	}
	ast.Inspect(file, func(node ast.Node) bool {
		sel, ok := node.(*ast.SelectorExpr)
		if ok && (sel.Sel.Name == "BeginTx" || sel.Sel.Name == "Begin") {
			message := "raw " + sel.Sel.Name + " outside transaction owners; use beginReadTx or beginWriteTx"
			if call, owner := readCalls[sel]; owner {
				reads++
				if txBeginReadOnly(call, file, info) {
					return true
				}
				message = "read owner must pass &sql.TxOptions{ReadOnly: true} directly"
			} else if writeCalls[sel] {
				writes++
				return true
			}
			findings = append(findings, path+":"+strconv.Itoa(fset.Position(sel.Pos()).Line)+": "+message)
		}
		// Ban transaction-control SQL at its construction, not its execution.
		// Reassignment, shadowing and captured Exec methods cannot conceal a
		// literal or constant-concatenated BEGIN from this check.
		switch expr := node.(type) {
		case *ast.BasicLit, *ast.BinaryExpr:
			value := info.Types[expr.(ast.Expr)].Value
			if value != nil && value.Kind() == constant.String && txBeginSQL(constant.StringVal(value)) {
				findings = append(findings, path+":"+strconv.Itoa(fset.Position(node.Pos()).Line)+": raw SQL BEGIN outside transaction owners")
				return false
			}
		}
		return true
	})
	return findings, reads, writes
}

func txBeginReadOnly(call *ast.CallExpr, file *ast.File, info *types.Info) bool {
	if len(call.Args) != 2 {
		return false
	}
	address, ok := call.Args[1].(*ast.UnaryExpr)
	if !ok || address.Op != token.AND {
		return false
	}
	options, ok := address.X.(*ast.CompositeLit)
	if !ok {
		return false
	}
	typ, ok := options.Type.(*ast.SelectorExpr)
	if !ok || typ.Sel.Name != "TxOptions" {
		return false
	}
	qualifier, ok := typ.X.(*ast.Ident)
	if !ok {
		return false
	}
	sqlImport := false
	for _, imp := range file.Imports {
		if imp.Path.Value == `"database/sql"` {
			name := "sql"
			if imp.Name != nil {
				name = imp.Name.Name
			}
			sqlImport = name == qualifier.Name
		}
	}
	if !sqlImport {
		return false
	}
	for _, field := range options.Elts {
		kv, ok := field.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		value, literal := kv.Value.(*ast.Ident)
		if ok && key.Name == "ReadOnly" && literal && value.Name == "true" && info.Uses[value] == types.Universe.Lookup("true") {
			return true
		}
	}
	return false
}

// SQLite treats comments as whitespace. Quoted text is one token, including
// embedded semicolons, so it cannot introduce a transaction-control statement.
func txBeginSQL(text string) bool {
	start := true
	for text != "" {
		word, rest := txBeginSQLToken(text)
		text = rest
		if start && strings.EqualFold(word, "BEGIN") {
			return true
		}
		if word != "" {
			start = word == ";"
		}
	}
	return false
}

func txBeginSQLToken(text string) (string, string) {
	text = strings.TrimLeft(text, " \t\r\n\f\v")
	if text == "" {
		return "", ""
	}
	if strings.HasPrefix(text, "--") {
		_, rest, _ := strings.Cut(text, "\n")
		return "", rest
	}
	if strings.HasPrefix(text, "/*") {
		_, rest, _ := strings.Cut(text[2:], "*/")
		return "", rest
	}
	if strings.ContainsRune("'\"`[", rune(text[0])) {
		end := text[0]
		if end == '[' {
			end = ']'
		}
		for i := 1; i < len(text); i++ {
			if text[i] == end {
				if end != ']' && i+1 < len(text) && text[i+1] == end {
					i++
					continue
				}
				return "quoted", text[i+1:]
			}
		}
		return "quoted", ""
	}
	i := 0
	for i < len(text) && txBeginSQLIdentifier(text[i]) {
		i++
	}
	if i == 0 {
		i = 1
	}
	return text[:i], text[i:]
}

func txBeginSQLIdentifier(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '$' || c >= 0x80
}
