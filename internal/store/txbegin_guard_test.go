package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
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
		file, err := parser.ParseFile(fset, path, nil, 0)
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
	for _, pkg := range packages {
		txBeginResolveConstants(pkg)
		for path, file := range pkg {
			findings, reads, writes := scanTxBeginFile(path, file, fset)
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
			file, err := parser.ParseFile(fset, tt.path, tt.source, 0)
			if err != nil {
				t.Fatal(err)
			}
			findings, _, _ := scanTxBeginFile(tt.path, file, fset)
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
		file, err := parser.ParseFile(fset, tt.path, tt.source, 0)
		if err != nil {
			t.Fatal(err)
		}
		findings, reads, writes := scanTxBeginFile(tt.path, file, fset)
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
		file, err := parser.ParseFile(fset, path, source, 0)
		if err != nil {
			t.Fatal(err)
		}
		files[path] = file
	}
	txBeginResolveConstants(files)
	findings, _, _ := scanTxBeginFile("internal/store/query.go", files["query.go"], fset)
	if len(findings) != 1 || !strings.HasPrefix(findings[0], "internal/store/query.go:1:") {
		t.Fatalf("want one cross-file SQL finding, got %v", findings)
	}
}

// The parser resolves lexical names within a file. Only unresolved names can
// refer to package constants in another file, so local shadows keep their own
// objects and cannot overwrite these bindings.
func txBeginResolveConstants(files map[string]*ast.File) {
	constants := map[string]*ast.Object{}
	for _, file := range files {
		for _, decl := range file.Decls {
			group, ok := decl.(*ast.GenDecl)
			if !ok || group.Tok != token.CONST {
				continue
			}
			for _, spec := range group.Specs {
				if value, ok := spec.(*ast.ValueSpec); ok {
					for _, name := range value.Names {
						constants[name.Name] = name.Obj
					}
				}
			}
		}
	}
	for _, file := range files {
		for _, name := range file.Unresolved {
			if name.Obj == nil {
				name.Obj = constants[name.Name]
			}
		}
	}
}

// scanTxBeginFile admits only direct begins in the two owning functions. It
// also visits their bodies: a helper name cannot hide a read option regression
// or a captured method that can later receive different options.
func scanTxBeginFile(path string, file *ast.File, fset *token.FileSet) ([]string, int, int) {
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
				if txBeginReadOnly(call, file) {
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
			text, known := txBeginConstantString(expr.(ast.Expr), map[*ast.Object]bool{})
			if known && txBeginSQL(text) {
				findings = append(findings, path+":"+strconv.Itoa(fset.Position(node.Pos()).Line)+": raw SQL BEGIN outside transaction owners")
				return false
			}
		}
		return true
	})
	return findings, reads, writes
}

func txBeginReadOnly(call *ast.CallExpr, file *ast.File) bool {
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
		if ok && key.Name == "ReadOnly" && literal && value.Name == "true" && value.Obj == nil {
			return true
		}
	}
	return false
}

func txBeginConstantString(expr ast.Expr, visiting map[*ast.Object]bool) (string, bool) {
	switch value := expr.(type) {
	case *ast.BasicLit:
		if value.Kind == token.STRING {
			text, err := strconv.Unquote(value.Value)
			return text, err == nil
		}
	case *ast.Ident:
		if value.Obj == nil || value.Obj.Kind != ast.Con || visiting[value.Obj] {
			return "", false
		}
		visiting[value.Obj] = true
		defer delete(visiting, value.Obj)
		if spec, ok := value.Obj.Decl.(*ast.ValueSpec); ok {
			for i, name := range spec.Names {
				if name.Name == value.Name && i < len(spec.Values) {
					return txBeginConstantString(spec.Values[i], visiting)
				}
			}
		}
	case *ast.BinaryExpr:
		if value.Op == token.ADD {
			left, leftKnown := txBeginConstantString(value.X, visiting)
			right, rightKnown := txBeginConstantString(value.Y, visiting)
			return left + right, leftKnown && rightKnown
		}
	case *ast.ParenExpr:
		return txBeginConstantString(value.X, visiting)
	}
	return "", false
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
