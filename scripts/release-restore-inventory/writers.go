package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// The writer analysis lists, for one release, every site of the binary that
// can set an Event's kind or payload version: an Event literal that sets both,
// and an assignment, increment, or address of its Kind or PayloadVersion. Each
// site is open: its (kind, version) pair is "* v*", any value.
//
// The analysis sees only what ./cmd/concord compiles at the tag, with the local
// toolchain. A tree that does not list or type-check, or whose binary compiles
// a main-module package outside internal/ and cmd/ (the inputs the group key
// pins), is refused. Liveness is not analyzed: a site in dead code still
// counts.

// writerSite is one open site.
type writerSite struct {
	Function       string   `json:"function"`
	Expression     string   `json:"expression"`
	FunctionDigest string   `json:"function_digest"`
	Pairs          []string `json:"pairs"`
}

// writerAnalysis is one release binary's event writers.
type writerAnalysis struct {
	Analyzer string       `json:"analyzer"`
	Refused  string       `json:"refused,omitempty"`
	Open     []writerSite `json:"open_sites,omitempty"`
}

type listedPackage struct {
	ImportPath string
	Dir        string
	Export     string
	GoFiles    []string
	Standard   bool
	Module     *struct{ Path string }
	Error      *struct{ Err string }
}

type checkedPackage struct {
	path  string
	files []*ast.File
	info  *types.Info
	types *types.Package
}

// analyzer holds one binary's type-checked packages and the sites found.
type analyzer struct {
	fset    *token.FileSet
	store   string
	sources map[string][]byte
	sites   []writerSite
}

func analyzerVersion() string { return "release-writer-sites.v1 " + digest([]byte(generatorSource)) }

// analyzeWriters analyzes the release source tree in dir. A source that does
// not list or type-check is refused.
func analyzeWriters(dir string) writerAnalysis {
	result, err := analyzeWritersIn(dir)
	if err != nil {
		return writerAnalysis{Analyzer: analyzerVersion(), Refused: err.Error()}
	}
	return result
}

func analyzeWritersIn(dir string) (writerAnalysis, error) {
	out, err := command(dir, "go", "list", "-e", "-export", "-deps", "-json", "./cmd/concord")
	if err != nil {
		return writerAnalysis{}, err
	}
	var listed []listedPackage
	exports := map[string]string{}
	module := ""
	for decoder := json.NewDecoder(bytes.NewReader(out)); ; {
		var p listedPackage
		if err := decoder.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return writerAnalysis{}, err
		}
		if p.Error != nil {
			return writerAnalysis{}, fmt.Errorf("package %s: %s", p.ImportPath, p.Error.Err)
		}
		if p.ImportPath == "unsafe" {
			continue
		}
		if p.Export == "" {
			return writerAnalysis{}, fmt.Errorf("package %s has no export data", p.ImportPath)
		}
		exports[p.ImportPath] = p.Export
		listed = append(listed, p)
		if strings.HasSuffix(p.ImportPath, "/cmd/concord") && p.Module != nil {
			module = p.Module.Path
		}
	}
	if module == "" {
		return writerAnalysis{}, errors.New("./cmd/concord is not a main-module package")
	}
	a := &analyzer{fset: token.NewFileSet(), store: module + "/internal/store", sources: map[string][]byte{}}
	imports := importer.ForCompiler(a.fset, "gc", func(path string) (io.ReadCloser, error) {
		file, ok := exports[path]
		if !ok {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(filepath.Clean(file))
	})
	var packages []*checkedPackage
	for _, p := range listed {
		if p.Standard || p.Module == nil || p.Module.Path != module {
			continue
		}
		if !strings.HasPrefix(p.ImportPath, module+"/internal/") && !strings.HasPrefix(p.ImportPath, module+"/cmd/") {
			return writerAnalysis{}, fmt.Errorf("package %s lies outside internal/ and cmd/, which the group key pins", p.ImportPath)
		}
		checked := &checkedPackage{path: p.ImportPath}
		for _, name := range p.GoFiles {
			path := filepath.Join(p.Dir, name)
			src, err := os.ReadFile(filepath.Clean(path))
			if err != nil {
				return writerAnalysis{}, err
			}
			file, err := parser.ParseFile(a.fset, path, src, parser.SkipObjectResolution)
			if err != nil {
				return writerAnalysis{}, err
			}
			a.sources[path] = src
			checked.files = append(checked.files, file)
		}
		checked.info = &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}, Selections: map[*ast.SelectorExpr]*types.Selection{}}
		checked.types, err = (&types.Config{Importer: imports}).Check(p.ImportPath, a.fset, checked.files, checked.info)
		if err != nil {
			return writerAnalysis{}, fmt.Errorf("type-check %s: %w", p.ImportPath, err)
		}
		packages = append(packages, checked)
	}
	var store *checkedPackage
	for _, p := range packages {
		if p.path == a.store {
			store = p
		}
	}
	if store == nil || store.types.Scope().Lookup("Event") == nil {
		return writerAnalysis{}, errors.New("the binary does not compile internal/store.Event")
	}
	result := writerAnalysis{Analyzer: analyzerVersion()}
	for _, p := range packages {
		a.collect(p)
	}
	result.Open = a.sites
	sort.Slice(result.Open, func(i, j int) bool {
		x, y := result.Open[i], result.Open[j]
		return x.Function < y.Function || x.Function == y.Function && x.Expression < y.Expression
	})
	return result, nil
}

func (a *analyzer) isNamed(t types.Type, name string) bool {
	if t == nil {
		return false
	}
	named, ok := types.Unalias(t).(*types.Named)
	return ok && named.Obj().Name() == name && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == a.store
}

func (a *analyzer) isEvent(t types.Type) bool { return a.isNamed(t, "Event") }

// eventField names the Event field a selector reads or writes, or "".
func (a *analyzer) eventField(p *checkedPackage, expr ast.Expr) string {
	selector, ok := ast.Unparen(expr).(*ast.SelectorExpr)
	if !ok || (selector.Sel.Name != "Kind" && selector.Sel.Name != "PayloadVersion") {
		return ""
	}
	selection := p.info.Selections[selector]
	if selection == nil || selection.Kind() != types.FieldVal {
		return ""
	}
	recv := selection.Recv()
	for _, i := range selection.Index()[:len(selection.Index())-1] {
		if pointer, ok := recv.Underlying().(*types.Pointer); ok {
			recv = pointer.Elem()
		}
		recv = recv.Underlying().(*types.Struct).Field(i).Type()
	}
	if pointer, ok := recv.Underlying().(*types.Pointer); ok {
		recv = pointer.Elem()
	}
	if !a.isEvent(recv) {
		return ""
	}
	return selector.Sel.Name
}

func (a *analyzer) source(node ast.Node) string {
	start, end := a.fset.PositionFor(node.Pos(), false), a.fset.PositionFor(node.End(), false)
	return string(a.sources[start.Filename][start.Offset:end.Offset])
}

// declName names a declaration and digests its source.
func (a *analyzer) declName(p *checkedPackage, d ast.Decl) (string, string) {
	body := digest([]byte(a.source(d)))
	if fn, ok := d.(*ast.FuncDecl); ok {
		if obj, ok := p.info.Defs[fn.Name].(*types.Func); ok {
			return obj.FullName(), body
		}
	}
	return p.path + " declaration at line " + strconv.Itoa(a.fset.PositionFor(d.Pos(), false).Line), body
}

// collect lists one package's writer sites.
func (a *analyzer) collect(p *checkedPackage) {
	for _, file := range p.files {
		for _, d := range file.Decls {
			name, body := a.declName(p, d)
			add := func(node ast.Node) {
				a.sites = append(a.sites, writerSite{Function: name, Expression: normalize(a.source(node)), FunctionDigest: body, Pairs: []string{"* v*"}})
			}
			ast.Inspect(d, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.CompositeLit:
					a.collectLiteral(p, v, add)
				case *ast.AssignStmt:
					for _, lhs := range v.Lhs {
						if a.eventField(p, lhs) != "" {
							add(v)
							break
						}
					}
				case *ast.IncDecStmt:
					if a.eventField(p, v.X) != "" {
						add(v)
					}
				case *ast.UnaryExpr:
					if a.eventField(p, v.X) != "" && v.Op == token.AND {
						add(v)
					}
				case *ast.RangeStmt:
					for _, lhs := range []ast.Expr{v.Key, v.Value} {
						if a.eventField(p, lhs) != "" {
							add(lhs)
						}
					}
				}
				return true
			})
		}
	}
}

func (a *analyzer) collectLiteral(p *checkedPackage, lit *ast.CompositeLit, add func(ast.Node)) {
	// An element of []*Event may elide &Event; its literal records *Event.
	typ := p.info.Types[lit].Type
	if pointer, ok := typ.Underlying().(*types.Pointer); ok {
		typ = pointer.Elem()
	}
	if !a.isEvent(typ) {
		return
	}
	kind, version := false, false
	for _, element := range lit.Elts {
		pair, ok := element.(*ast.KeyValueExpr)
		if !ok {
			add(lit)
			return
		}
		kind = kind || pair.Key.(*ast.Ident).Name == "Kind"
		version = version || pair.Key.(*ast.Ident).Name == "PayloadVersion"
	}
	if kind && version {
		add(lit)
	}
}

func normalize(s string) string { return strings.Join(strings.Fields(s), " ") }
