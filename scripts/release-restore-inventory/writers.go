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
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The writer analysis lists, for one release, every site of the binary that
// can set an Event's kind or payload version: an Event literal that sets both;
// an assignment, increment, or address of its Kind or PayloadVersion; a
// conversion into an Event; an Event holder handed to an interface or unsafe
// pointer; and SQL text, in Go source or an embedded file, that writes
// domain_events in any form other than a plain insert of one Event's own
// fields. Each site is open: its (kind, version) pair is "* v*", any value.
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
	EmbedFiles []string
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

var (
	domainEventsWrite = regexp.MustCompile(`(?is)\b(insert|replace)\b.*\binto\s+domain_events\b|\bupdate\s+(or\s+\w+\s+)?domain_events\b|\brename\s+to\s+domain_events\b`)
	domainEventsShape = regexp.MustCompile(`(?is)^\s*insert\s+(or\s+\w+\s+)?into\s+domain_events\s*\(([^)]*)\)\s*values\s*\(([^)]*)\)\s*$`)
)

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
		for _, name := range p.EmbedFiles {
			src, err := os.ReadFile(filepath.Clean(filepath.Join(p.Dir, name)))
			if err != nil {
				return writerAnalysis{}, err
			}
			if domainEventsWrite.Match(src) {
				a.sites = append(a.sites, writerSite{Function: p.ImportPath + " embed " + name, Expression: "embedded file writes domain_events", FunctionDigest: digest(src), Pairs: []string{"* v*"}})
			}
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

// holdsEvent reports whether a value of t reaches an Event a callee could
// overwrite through reflection: a pointer, slice, or map that holds one.
func (a *analyzer) holdsEvent(t types.Type, seen map[types.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true
	switch u := t.Underlying().(type) {
	case *types.Pointer, *types.Slice, *types.Map:
		elem := u.(interface{ Elem() types.Type }).Elem()
		return a.containsEvent(elem, map[types.Type]bool{}) || a.holdsEvent(elem, seen)
	case *types.Struct:
		for i := range u.NumFields() {
			if a.holdsEvent(u.Field(i).Type(), seen) {
				return true
			}
		}
	case *types.Array:
		return a.holdsEvent(u.Elem(), seen)
	}
	return false
}

func (a *analyzer) containsEvent(t types.Type, seen map[types.Type]bool) bool {
	if a.isEvent(t) {
		return true
	}
	if seen[t] {
		return false
	}
	seen[t] = true
	switch u := t.Underlying().(type) {
	case *types.Struct:
		for i := range u.NumFields() {
			if a.containsEvent(u.Field(i).Type(), seen) {
				return true
			}
		}
	case *types.Array:
		return a.containsEvent(u.Elem(), seen)
	}
	return false
}

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
			fn, _ := d.(*ast.FuncDecl)
			add := func(node ast.Node) {
				a.sites = append(a.sites, writerSite{Function: name, Expression: normalize(a.source(node)), FunctionDigest: body, Pairs: []string{"* v*"}})
			}
			ast.Inspect(d, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.CallExpr:
					if a.callHoldsEvent(p, v) {
						add(v)
					}
				case *ast.CompositeLit:
					a.collectLiteral(p, v, add)
				case *ast.AssignStmt:
					for _, lhs := range v.Lhs {
						if a.eventField(p, lhs) != "" {
							add(v)
							break
						}
					}
					for i, lhs := range v.Lhs {
						if t := p.info.Types[lhs].Type; t != nil && types.IsInterface(t) && len(v.Lhs) == len(v.Rhs) && a.holdsEvent(p.info.Types[v.Rhs[i]].Type, map[types.Type]bool{}) {
							add(lhs)
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
				case *ast.ReturnStmt:
					if a.returnHoldsEvent(p, fn, v) {
						add(v)
					}
				case *ast.BasicLit:
					if v.Kind == token.STRING {
						text, _ := strconv.Unquote(v.Value)
						if domainEventsWrite.MatchString(text) && !a.recognizedInsert(p, file, v, text) {
							add(v)
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
		fields, isStruct := typ.Underlying().(*types.Struct)
		for i, element := range lit.Elts {
			var field types.Type
			if pair, ok := element.(*ast.KeyValueExpr); ok {
				element = pair.Value
				if key, ok := pair.Key.(*ast.Ident); ok && isStruct {
					for j := range fields.NumFields() {
						if fields.Field(j).Name() == key.Name {
							field = fields.Field(j).Type()
						}
					}
				}
			} else if isStruct && i < fields.NumFields() {
				field = fields.Field(i).Type()
			}
			if !isStruct {
				field = elementType(typ)
			}
			if field != nil && types.IsInterface(field) && a.holdsEvent(p.info.Types[element].Type, map[types.Type]bool{}) {
				add(element)
			}
		}
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

func elementType(t types.Type) types.Type {
	switch u := t.Underlying().(type) {
	case *types.Map:
		return u.Elem()
	case *types.Slice:
		return u.Elem()
	case *types.Array:
		return u.Elem()
	}
	return nil
}

// isPlainPath is an identifier or field path with no call, index, or
// dereference, so both reads name the same Event.
func isPlainPath(expr ast.Expr) bool {
	switch v := expr.(type) {
	case *ast.Ident:
		return true
	case *ast.SelectorExpr:
		return isPlainPath(v.X)
	}
	return false
}

// callHoldsEvent reports a conversion that makes an Event from another type,
// or an argument that hands an Event holder to an interface or unsafe pointer.
func (a *analyzer) callHoldsEvent(p *checkedPackage, call *ast.CallExpr) bool {
	fun := p.info.Types[call.Fun]
	if fun.IsType() {
		if len(call.Args) != 1 {
			return false
		}
		from := p.info.Types[call.Args[0]].Type
		return (a.isEvent(fun.Type) && !a.isEvent(from)) || ((types.IsInterface(fun.Type) || isUnsafePointer(fun.Type)) && a.holdsEvent(from, map[types.Type]bool{}))
	}
	signature, ok := fun.Type.(*types.Signature)
	if !ok {
		return false
	}
	for i, arg := range call.Args {
		var param types.Type
		switch {
		case signature.Variadic() && i >= signature.Params().Len()-1:
			param = signature.Params().At(signature.Params().Len() - 1).Type()
			if !call.Ellipsis.IsValid() {
				param = param.(*types.Slice).Elem()
			}
		case i < signature.Params().Len():
			param = signature.Params().At(i).Type()
		}
		if param != nil && (types.IsInterface(param) || isUnsafePointer(param)) && a.holdsEvent(p.info.Types[arg].Type, map[types.Type]bool{}) {
			return true
		}
	}
	return false
}

func isUnsafePointer(t types.Type) bool {
	basic, ok := t.Underlying().(*types.Basic)
	return ok && basic.Kind() == types.UnsafePointer
}

func (a *analyzer) returnHoldsEvent(p *checkedPackage, fn *ast.FuncDecl, ret *ast.ReturnStmt) bool {
	if fn == nil || fn.Type.Results == nil {
		return false
	}
	var results []types.Type
	for _, field := range fn.Type.Results.List {
		for range max(1, len(field.Names)) {
			results = append(results, p.info.Types[field.Type].Type)
		}
	}
	if len(results) != len(ret.Results) {
		return false
	}
	for i, result := range ret.Results {
		if results[i] != nil && types.IsInterface(results[i]) && a.holdsEvent(p.info.Types[result].Type, map[types.Type]bool{}) {
			return true
		}
	}
	return false
}

// recognizedInsert accepts only the append statement form: a literal INSERT
// INTO domain_events with one placeholder per column, passed directly to a
// call whose kind and payload_version arguments read one Event's fields.
func (a *analyzer) recognizedInsert(p *checkedPackage, file *ast.File, lit *ast.BasicLit, text string) bool {
	match := domainEventsShape.FindStringSubmatch(text)
	if match == nil {
		return false
	}
	columns, values := strings.Split(match[2], ","), strings.Split(match[3], ",")
	if len(columns) != len(values) {
		return false
	}
	var call *ast.CallExpr
	position := -1
	ast.Inspect(file, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			for i, arg := range c.Args {
				if arg == lit {
					call, position = c, i
				}
			}
		}
		return call == nil
	})
	if call == nil || len(call.Args) != position+1+len(columns) {
		return false
	}
	var kind, version *ast.SelectorExpr
	for i, column := range columns {
		if strings.TrimSpace(values[i]) != "?" {
			return false
		}
		field := a.eventField(p, call.Args[position+1+i])
		selector, _ := ast.Unparen(call.Args[position+1+i]).(*ast.SelectorExpr)
		switch strings.TrimSpace(column) {
		case "kind":
			if field != "Kind" {
				return false
			}
			kind = selector
		case "payload_version":
			if field != "PayloadVersion" {
				return false
			}
			version = selector
		}
	}
	return kind != nil && version != nil && isPlainPath(kind.X) && types.ExprString(kind.X) == types.ExprString(version.X)
}

func normalize(s string) string { return strings.Join(strings.Fields(s), " ") }
