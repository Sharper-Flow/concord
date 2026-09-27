package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The agent envelope refuses an ambiguous_scope error without candidates, so a
// store refusal minted bare can never cross the boundary: it dies as an
// undeliverable marshal error instead of reaching the agent typed. Every
// KindAmbiguousScope construction therefore routes through
// newAmbiguousScopeFailure, the one constructor that carries the enumerated
// candidate identities. This scan holds that route structurally, the way
// TestTxScope holds the transaction-scope route.

type ambiguousScopeFinding struct {
	Path    string
	Line    int
	Message string
}

func (f ambiguousScopeFinding) String() string {
	return f.Path + ":" + strconv.Itoa(f.Line) + ": " + f.Message
}

const ambiguousScopeConstructorOwner = "newAmbiguousScopeFailure"

func TestAmbiguousScopeRefusalsCarryCandidates(t *testing.T) {
	t.Parallel()
	findings, analyzed := scanAmbiguousScopeRefusalSites(txScopeRepoRoot())
	// A scan that reaches no files reports no findings, which would let this
	// assertion pass while proving nothing. The package holds dozens of
	// non-test files; a count this low means discovery broke, not that the
	// package shrank.
	if analyzed < 40 {
		t.Fatalf("ambiguous-scope analysis reached %d files in internal/store; discovery is broken", analyzed)
	}
	for _, finding := range findings {
		t.Errorf("%s", finding)
	}
}

func scanAmbiguousScopeRefusalSites(root string) ([]ambiguousScopeFinding, int) {
	storeDir := filepath.Join(root, "internal", "store")
	entries, err := os.ReadDir(storeDir)
	if err != nil {
		return []ambiguousScopeFinding{{Path: "internal/store", Line: 1, Message: "cannot read package: " + err.Error()}}, 0
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		paths = append(paths, filepath.Join(storeDir, entry.Name()))
	}
	sort.Strings(paths)
	var findings []ambiguousScopeFinding
	for _, path := range paths {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			findings = append(findings, ambiguousScopeFinding{Path: filepath.ToSlash(filepath.Join("internal/store", filepath.Base(path))), Line: 1, Message: "cannot parse: " + err.Error()})
			continue
		}
		findings = append(findings, scanAmbiguousScopeFile(filepath.ToSlash(filepath.Join("internal/store", filepath.Base(path))), file, fset)...)
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Path != findings[j].Path {
			return findings[i].Path < findings[j].Path
		}
		return findings[i].Line < findings[j].Line
	})
	return findings, len(paths)
}

func scanAmbiguousScopeFile(path string, file *ast.File, fset *token.FileSet) []ambiguousScopeFinding {
	var findings []ambiguousScopeFinding
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Name.Name == ambiguousScopeConstructorOwner {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.CallExpr:
				ident, ok := n.Fun.(*ast.Ident)
				if !ok {
					return true
				}
				if ident.Name != "newFailure" && ident.Name != "wrapFailure" && ident.Name != "newRouteFailure" {
					return true
				}
				if len(n.Args) == 0 {
					return true
				}
				if kind, ok := n.Args[0].(*ast.Ident); ok && kind.Name == "KindAmbiguousScope" {
					findings = append(findings, ambiguousScopeFinding{path, fset.Position(n.Pos()).Line, ident.Name + " mints an ambiguous_scope refusal outside " + ambiguousScopeConstructorOwner + "; candidates would be missing and the envelope could not deliver the refusal"})
				}
			case *ast.CompositeLit:
				lit, ok := n.Type.(*ast.Ident)
				if !ok || lit.Name != "Failure" {
					return true
				}
				for _, elt := range n.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Kind" {
						if value, ok := kv.Value.(*ast.Ident); ok && value.Name == "KindAmbiguousScope" {
							findings = append(findings, ambiguousScopeFinding{path, fset.Position(n.Pos()).Line, "Failure literal mints an ambiguous_scope refusal outside " + ambiguousScopeConstructorOwner + "; candidates would be missing and the envelope could not deliver the refusal"})
						}
					}
				}
			}
			return true
		})
	}
	return findings
}

// The scan properties bite: a source that mints the refusal outside the
// constructor reports exactly one finding, and the constructor itself passes.
func TestAmbiguousScopeScanPropertiesBite(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
	}{
		{"constructor site passes", `package store; func newAmbiguousScopeFailure(op, detail, recovery string, candidates []string) *Failure { f := newFailure(KindAmbiguousScope, op, detail, false, recovery); f.CandidateIDs = candidates; return f }`},
		{"bare newFailure violates", `package store; func violate() error { return newFailure(KindAmbiguousScope, "op", "detail", false, "recover") }`},
		{"bare wrapFailure violates", `package store; func violate() error { return wrapFailure(KindAmbiguousScope, "op", "detail", false, "recover", nil) }`},
		{"composite literal violates", `package store; func violate() *Failure { f := &Failure{Kind: KindAmbiguousScope}; return f }`},
		{"other kinds pass", `package store; func fine() error { return newFailure(KindUnknownScope, "op", "detail", false, "recover") }`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, tt.name+".go", tt.source, 0)
			if err != nil {
				t.Fatal(err)
			}
			findings := scanAmbiguousScopeFile(tt.name+".go", file, fset)
			want := 0
			if tt.name != "constructor site passes" && tt.name != "other kinds pass" {
				want = 1
			}
			if len(findings) != want {
				t.Fatalf("found %d findings, want %d: %v", len(findings), want, findings)
			}
		})
	}
}
