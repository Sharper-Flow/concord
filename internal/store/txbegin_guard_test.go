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
	readBegins, writeBegins := 0, 0
	opens, pools := 0, 0
	fset := token.NewFileSet()
	packages, files := txBeginProductionPackages(t, root, fset)
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
			connectionFindings, fileOpens, filePools := scanTxConnectionFile(path, file, fset, info)
			opens += fileOpens
			pools += filePools
			for _, finding := range append(findings, connectionFindings...) {
				t.Error(finding)
			}
		}
	}
	if files < 30 || readBegins != 1 || writeBegins != 2 {
		t.Fatalf("begin discovery: %d production files, %d read begins, %d write begins; want at least 30 files and exactly 1/2 owner begins", files, readBegins, writeBegins)
	}
	if err := txConnectionCounts(opens, pools); err != nil {
		t.Fatal(err)
	}
}

func txBeginProductionPackages(t *testing.T, root string, fset *token.FileSet) (map[string]map[string]*ast.File, int) {
	t.Helper()
	packages := map[string]map[string]*ast.File{}
	files := 0
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
		// Declared test-support packages implement fixtures, including
		// deliberate foreign write locks. No production file may import them.
		// This is a package boundary, not an exemption for a production
		// transaction call site.
		if testSupportPackage(filepath.Dir(rel)) {
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
	return packages, files
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
		{"production test-support import", "internal/other/read.go", `package other; import "github.com/sharper-flow/concord/internal/testenv"`},
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
		{"read owner nil", "internal/store/internal/sqlitedb/read.go", `package sqlitedb; func BeginRead() { db.BeginTx(ctx, nil) }`},
		{"read owner false", "internal/store/internal/sqlitedb/read.go", `package sqlitedb; import "database/sql"; func BeginRead() { db.BeginTx(ctx, &sql.TxOptions{ReadOnly: false}) }`},
		{"read owner shadowed true", "internal/store/internal/sqlitedb/read.go", `package sqlitedb; import "database/sql"; func BeginRead() { true := false; db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true}) }`},
		{"read owner indirect options", "internal/store/internal/sqlitedb/read.go", `package sqlitedb; func BeginRead() { db.BeginTx(ctx, options) }`},
		{"read owner method capture", "internal/store/internal/sqlitedb/read.go", `package sqlitedb; func BeginRead() { begin := db.BeginTx; begin(ctx, nil) }`},
		{"read owner closure", "internal/store/internal/sqlitedb/read.go", `package sqlitedb; import "database/sql"; func BeginRead() { f := func() { db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true}) }; f() }`},
		{"stale parent read owner", "internal/store/transaction.go", `package store; import "database/sql"; func beginReadTx() { db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true}) }`},
		{"stale parent write owner", "internal/store/durable.go", `package store; func beginWriteTx() { db.BeginTx(ctx, nil) }`},
		{"durable unauthorized begin", "internal/store/internal/sqlitedb/query.go", `package sqlitedb; func query() { db.BeginTx(ctx, nil) }`},
		{"durable wrong read file", "internal/store/internal/sqlitedb/query.go", `package sqlitedb; import "database/sql"; func BeginRead() { db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true}) }`},
		{"durable wrong read function", "internal/store/internal/sqlitedb/read.go", `package sqlitedb; import "database/sql"; func query() { db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true}) }`},
		{"durable wrong write function", "internal/store/internal/sqlitedb/write.go", `package sqlitedb; func query() { db.BeginTx(ctx, nil) }`},
		{"durable owner method", "internal/store/internal/sqlitedb/write.go", `package sqlitedb; func (s *Store) BeginWrite() { db.BeginTx(ctx, nil) }`},
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
		{"internal/store/internal/sqlitedb/read.go", `package sqlitedb; import dbsql "database/sql"; func BeginRead() { db.BeginTx(ctx, &dbsql.TxOptions{ReadOnly: true}) }`, 1, 0},
		{"internal/store/internal/sqlitedb/write.go", `package sqlitedb; func BeginWrite() { db.BeginTx(ctx, nil); conn.BeginTx(ctx, nil) }`, 0, 2},
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

func TestTxBeginGuardStaleOwners(t *testing.T) {
	t.Parallel()
	for _, fixture := range []struct{ path, source string }{
		{"internal/store/transaction.go", `package store; import "database/sql"; func beginReadTx() { db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true}) }`},
		{"internal/store/durable.go", `package store; func beginWriteTx() { db.BeginTx(ctx, nil); conn.BeginTx(ctx, nil) }`},
	} {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, fixture.path, fixture.source, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		info := txBeginFixtureTypes(t, fset, map[string]*ast.File{fixture.path: file})
		findings, reads, writes := scanTxBeginFile(fixture.path, file, fset, info)
		want := 1
		if strings.HasSuffix(fixture.path, "/durable.go") {
			want = 2
		}
		if len(findings) != want || reads != 0 || writes != 0 {
			t.Fatalf("stale owner %s: want %d findings and no admitted begins; got %v, %d/%d", fixture.path, want, findings, reads, writes)
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

func TestTxConnectionGuardFixtures(t *testing.T) {
	t.Parallel()
	const owner = "internal/store/internal/sqlitedb/connection.go"
	tests := []struct {
		name, path, source     string
		findings, opens, pools int
	}{
		{"owner", owner, `package sqlitedb; import "database/sql"; func Open(db *sql.DB) { sql.Open("sqlite", ""); db.SetMaxOpenConns(1) }`, 0, 1, 1},
		{"aliased owner", owner, `package sqlitedb; import dbsql "database/sql"; func Open(db *dbsql.DB) { dbsql.Open("sqlite", ""); db.SetMaxOpenConns(1) }`, 0, 1, 1},
		{"stale parent", "internal/store/store.go", `package store; import "database/sql"; func openUnmigrated(db *sql.DB) { sql.Open("sqlite", ""); db.SetMaxOpenConns(1) }`, 2, 0, 0},
		{"wrong durable file", "internal/store/internal/sqlitedb/other.go", `package sqlitedb; import "database/sql"; func Open(db *sql.DB) { sql.Open("sqlite", ""); db.SetMaxOpenConns(1) }`, 2, 0, 0},
		{"wrong durable function", owner, `package sqlitedb; import "database/sql"; func other(db *sql.DB) { sql.Open("sqlite", ""); db.SetMaxOpenConns(1) }`, 2, 0, 0},
		{"method is not owner", owner, `package sqlitedb; import "database/sql"; type opener struct{}; func (opener) Open(db *sql.DB) { sql.Open("sqlite", ""); db.SetMaxOpenConns(1) }`, 2, 0, 0},
		{"closure is not owner", owner, `package sqlitedb; import "database/sql"; func Open(db *sql.DB) { _ = func() { sql.Open("sqlite", ""); db.SetMaxOpenConns(1) } }`, 2, 0, 0},
		{"open capture", owner, `package sqlitedb; import "database/sql"; func Open() { open := sql.Open; open("sqlite", "") }`, 1, 0, 0},
		{"pool capture", owner, `package sqlitedb; import "database/sql"; func Open(db *sql.DB) { set := db.SetMaxOpenConns; set(1) }`, 1, 0, 0},
		{"pool method expression", owner, `package sqlitedb; import "database/sql"; func Open(db *sql.DB) { (*sql.DB).SetMaxOpenConns(db, 1) }`, 1, 0, 1},
		{"zero pool", owner, `package sqlitedb; import "database/sql"; func Open(db *sql.DB) { db.SetMaxOpenConns(0) }`, 1, 0, 1},
		{"larger pool", owner, `package sqlitedb; import "database/sql"; func Open(db *sql.DB) { db.SetMaxOpenConns(2) }`, 1, 0, 1},
		{"constant pool", owner, `package sqlitedb; import "database/sql"; const pool = 1; func Open(db *sql.DB) { db.SetMaxOpenConns(pool) }`, 1, 0, 1},
		{"expression pool", owner, `package sqlitedb; import "database/sql"; func Open(db *sql.DB) { db.SetMaxOpenConns(1+0) }`, 1, 0, 1},
		{"aliased outside", "cmd/concord/other.go", `package main; import dbsql "database/sql"; func other(db *dbsql.DB) { dbsql.Open("sqlite", ""); db.SetMaxOpenConns(1) }`, 2, 0, 0},
		{"dot import outside", "internal/agent/other.go", `package agent; import . "database/sql"; func other(db *DB) { Open("sqlite", ""); db.SetMaxOpenConns(1) }`, 2, 0, 0},
		{"connector pool in owner", owner, `package sqlitedb; import ("database/sql"; "database/sql/driver"); func Open(c driver.Connector) { sql.OpenDB(c) }`, 1, 0, 0},
		{"connector pool outside", "internal/agent/other.go", `package agent; import ("database/sql"; "database/sql/driver"); func other(c driver.Connector) { sql.OpenDB(c) }`, 1, 0, 0},
		{"unrelated methods", owner, `package sqlitedb; type other struct{}; func (other) SetMaxOpenConns(int) {}; func Open(db other) { db.SetMaxOpenConns(2) }`, 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, tt.path, tt.source, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			info := txBeginTypes(t, "fixture/"+file.Name.Name, fset, map[string]*ast.File{tt.path: file}, txBeginImporter(t, fset), false)
			if info == nil {
				t.Fatal("connection fixture must type-check")
			}
			findings, opens, pools := scanTxConnectionFile(tt.path, file, fset, info)
			if len(findings) != tt.findings || opens != tt.opens || pools != tt.pools {
				t.Fatalf("want findings/opens/pools %d/%d/%d, got %v/%d/%d", tt.findings, tt.opens, tt.pools, findings, opens, pools)
			}
		})
	}
}

func TestTxBeginGuardDurableDiscovery(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const durable = "internal/store/internal/sqlitedb/"
	for path, source := range map[string]string{
		durable + "read.go":        `package sqlitedb; import "database/sql"; func BeginRead(db *sql.DB) { db.BeginTx(nil, &sql.TxOptions{ReadOnly: true}) }`,
		durable + "write.go":       `package sqlitedb; import "database/sql"; func BeginWrite(db *sql.DB, conn *sql.Conn) { db.BeginTx(nil, nil); conn.BeginTx(nil, nil) }`,
		durable + "connection.go":  `package sqlitedb; import "database/sql"; func Open(db *sql.DB) { sql.Open("sqlite", ""); db.SetMaxOpenConns(1) }`,
		durable + "ignore_test.go": `this is not production Go`,
	} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fset := token.NewFileSet()
	packages, files := txBeginProductionPackages(t, root, fset)
	if files != 3 || len(packages) != 1 || len(packages[strings.TrimSuffix(durable, "/")]) != 3 {
		t.Fatalf("want exactly the three durable production files, got %d files in %v", files, packages)
	}
	pkg := packages[strings.TrimSuffix(durable, "/")]
	info := txBeginTypes(t, "fixture/sqlitedb", fset, pkg, txBeginImporter(t, fset), false)
	if info == nil {
		t.Fatal("durable discovery fixture must type-check")
	}
	reads, writes, opens, pools := 0, 0, 0, 0
	for path, file := range pkg {
		findings, r, w := scanTxBeginFile(path, file, fset, info)
		connectionFindings, o, p := scanTxConnectionFile(path, file, fset, info)
		if len(findings) != 0 || len(connectionFindings) != 0 {
			t.Fatalf("unexpected findings: %v, %v", findings, connectionFindings)
		}
		reads, writes, opens, pools = reads+r, writes+w, opens+o, pools+p
	}
	if reads != 1 || writes != 2 || txConnectionCounts(opens, pools) != nil {
		t.Fatalf("durable discovery reads/writes/opens/pools = %d/%d/%d/%d", reads, writes, opens, pools)
	}
}

func TestTxConnectionGuardOmissions(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, body string
		wantError  bool
	}{
		{"complete", `sql.Open("sqlite", ""); db.SetMaxOpenConns(1)`, false},
		{"missing open", `db.SetMaxOpenConns(1)`, true},
		{"missing pool", `sql.Open("sqlite", "")`, true},
		{"missing both", ``, true},
		{"duplicate open", `sql.Open("sqlite", ""); sql.Open("sqlite", ""); db.SetMaxOpenConns(1)`, true},
		{"duplicate pool", `sql.Open("sqlite", ""); db.SetMaxOpenConns(1); db.SetMaxOpenConns(1)`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			const path = "internal/store/internal/sqlitedb/connection.go"
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, `package sqlitedb; import "database/sql"; func Open(db *sql.DB) { `+tt.body+` }`, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			info := txBeginTypes(t, "fixture/sqlitedb", fset, map[string]*ast.File{path: file}, txBeginImporter(t, fset), false)
			if info == nil {
				t.Fatal("connection omission fixture must type-check")
			}
			findings, opens, pools := scanTxConnectionFile(path, file, fset, info)
			if len(findings) != 0 || (txConnectionCounts(opens, pools) != nil) != tt.wantError {
				t.Fatalf("counts %d/%d, findings %v, want count error %t", opens, pools, findings, tt.wantError)
			}
		})
	}
}

func txConnectionCounts(opens, pools int) error {
	if opens != 1 || pools != 1 {
		return fmt.Errorf("connection discovery: %d sql.Open and %d SetMaxOpenConns owner references; want exactly 1/1", opens, pools)
	}
	return nil
}

// Connection references use toolchain identities, so import aliases, dot imports,
// method expressions and captures cannot create another connection owner.
func scanTxConnectionFile(path string, file *ast.File, fset *token.FileSet, info *types.Info) ([]string, int, int) {
	direct := map[*ast.Ident]*ast.CallExpr{}
	if path == "internal/store/internal/sqlitedb/connection.go" {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "Open" || fn.Recv != nil || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				if _, closure := node.(*ast.FuncLit); closure {
					return false
				}
				if call, ok := node.(*ast.CallExpr); ok {
					switch fun := call.Fun.(type) {
					case *ast.SelectorExpr:
						direct[fun.Sel] = call
					case *ast.Ident:
						direct[fun] = call
					}
				}
				return true
			})
		}
	}
	var findings []string
	opens, pools := 0, 0
	ast.Inspect(file, func(node ast.Node) bool {
		ident, ok := node.(*ast.Ident)
		if !ok {
			return true
		}
		fn, ok := info.Uses[ident].(*types.Func)
		if !ok || fn.Pkg() == nil || fn.Pkg().Path() != "database/sql" || (fn.Name() != "Open" && fn.Name() != "OpenDB" && fn.Name() != "SetMaxOpenConns") {
			return true
		}
		message := fn.Name() + " outside connection owner; use sqlitedb.Open"
		if call := direct[ident]; call != nil && fn.Name() != "OpenDB" {
			if fn.Name() == "Open" {
				opens++
				return true
			}
			pools++
			if len(call.Args) == 1 {
				if literal, ok := call.Args[0].(*ast.BasicLit); ok && literal.Kind == token.INT && literal.Value == "1" {
					return true
				}
			}
			message = "connection owner must pass literal pool size 1 to SetMaxOpenConns"
		}
		findings = append(findings, path+":"+strconv.Itoa(fset.Position(ident.Pos()).Line)+": "+message)
		return true
	})
	return findings, opens, pools
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
		if testSupportPackage(strings.TrimPrefix(name, "github.com/sharper-flow/concord/")) {
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
		readOwner := path == "internal/store/internal/sqlitedb/read.go" && fn.Name.Name == "BeginRead"
		writeOwner := path == "internal/store/internal/sqlitedb/write.go" && fn.Name.Name == "BeginWrite"
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
			message := "raw " + sel.Sel.Name + " outside transaction owners; use sqlitedb.BeginRead or sqlitedb.BeginWrite"
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
