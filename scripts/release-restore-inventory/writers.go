package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
// fields.
//
// Each site's (kind, version) pairs resolve through constants and locals,
// flow-insensitively: a local is the union of every value assigned to it. A
// parameter, result, receiver, global, call result, or local whose address
// escapes is "*". A site whose pairs all resolve to finite values adds them to
// the release's writes; any other site is open. Pairs are "kind vVERSION",
// and "*" is any value. A zero kind or version pairs with nothing.
//
// The analysis sees only what ./cmd/concord compiles at the tag, with the local
// toolchain. A tree that does not list or type-check, or whose binary compiles
// a main-module package outside internal/ and cmd/ (the inputs the group key
// pins), is refused. Liveness is not analyzed: a site in dead code still
// counts.

// writerSite is one open site and its pairs.
type writerSite struct {
	Function       string   `json:"function"`
	Expression     string   `json:"expression"`
	FunctionDigest string   `json:"function_digest"`
	Pairs          []string `json:"pairs"`
}

// writerAnalysis is one release binary's event writers. Writes are the pairs
// fully resolved sites produce.
type writerAnalysis struct {
	Analyzer string       `json:"analyzer"`
	Refused  string       `json:"refused,omitempty"`
	Writes   []string     `json:"writes,omitempty"`
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

// domainEventsTable matches a reference to domain_events, quoted or
// schema-qualified, after the keyword before it.
const domainEventsTable = `(?:\s|["'\x60\[\]]|/\*.*?\*/)*(?:\w+(?:\s|["'\x60\[\]])*\.(?:\s|["'\x60\[\]])*)?domain_events\b`

var (
	domainEventsWrite = regexp.MustCompile(`(?is)\b(insert|replace)\b.*\binto\b` + domainEventsTable + `|\bupdate\b(?:\s+or\s+\w+)?` + domainEventsTable + `|\brename\s+to\b` + domainEventsTable)
	domainEventsShape = regexp.MustCompile(`(?is)^\s*insert\s+into\s+domain_events\s*\(([^)]*)\)\s*values\s*\(([^)]*)\)\s*$`)
	sqlColumn         = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
	anyValue          = []string{"*"}
	anyPair           = []string{"* v*"}
)

// resolveBudget bounds the resolver steps one site may take; a site over it
// is open.
const resolveBudget = 200000

// assignment is one write of a local: the whole variable (field "") or one
// Event field of it. A nil rhs is a var declaration's zero value.
type assignment struct {
	rhs   ast.Expr
	field string
	pkg   *checkedPackage
}

type seenKey struct {
	obj   types.Object
	field string
}

// analyzer holds one binary's type-checked packages, the flow facts the
// resolver reads, and the sites found. typeArgs lists, per type parameter
// key, every type argument the binary instantiates it with.
type analyzer struct {
	fset     *token.FileSet
	store    string
	sources  map[string][]byte
	sites    []writerSite
	writes   map[string]bool
	params   map[*types.TypeParam]string
	typeArgs map[string][]types.Type
	assigns  map[types.Object][]assignment
	declared map[types.Object]bool
	unknown  map[types.Object]bool
	steps    int
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
		checked.info = &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}, Selections: map[*ast.SelectorExpr]*types.Selection{}, Instances: map[*ast.Ident]types.Instance{}}
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
	a.params, a.typeArgs = map[*types.TypeParam]string{}, map[string][]types.Type{}
	a.assigns, a.declared, a.unknown, a.writes = map[types.Object][]assignment{}, map[types.Object]bool{}, map[types.Object]bool{}, map[string]bool{}
	for _, p := range packages {
		a.instances(p)
		a.index(p)
	}
	result := writerAnalysis{Analyzer: analyzerVersion()}
	for _, p := range packages {
		a.collect(p)
	}
	result.Writes = slices.Sorted(maps.Keys(a.writes))
	result.Open = a.sites
	sort.Slice(result.Open, func(i, j int) bool {
		x, y := result.Open[i], result.Open[j]
		return x.Function < y.Function || x.Function == y.Function && x.Expression < y.Expression
	})
	return result, nil
}

// index records each local's declaration and assignments. A local declared by
// a var statement or := is resolvable; one whose address escapes, which is
// incremented, or which a range, op-assignment, or tuple result writes is
// unknown.
func (a *analyzer) index(p *checkedPackage) {
	for _, file := range p.files {
		ast.Inspect(file, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.AssignStmt:
				a.indexAssign(p, v)
			case *ast.DeclStmt:
				if decl, ok := v.Decl.(*ast.GenDecl); ok && decl.Tok == token.VAR {
					for _, spec := range decl.Specs {
						a.indexSpec(p, spec.(*ast.ValueSpec))
					}
				}
			case *ast.IncDecStmt:
				a.markUnknown(p, v.X)
			case *ast.UnaryExpr:
				if v.Op == token.AND {
					a.markUnknown(p, v.X)
				}
			case *ast.SelectorExpr:
				// A pointer method called on an addressable value takes its
				// address.
				if sel := p.info.Selections[v]; sel != nil && sel.Kind() == types.MethodVal {
					_, pointerRecv := sel.Obj().Type().(*types.Signature).Recv().Type().(*types.Pointer)
					if _, pointerX := under(p.info.Types[v.X].Type).(*types.Pointer); pointerRecv && !pointerX {
						a.markUnknown(p, v.X)
					}
				}
			case *ast.RangeStmt:
				for _, e := range []ast.Expr{v.Key, v.Value} {
					a.markUnknown(p, e)
				}
			}
			return true
		})
	}
}

func (a *analyzer) indexAssign(p *checkedPackage, v *ast.AssignStmt) {
	for i, lhs := range v.Lhs {
		record := assignment{pkg: p}
		target := ast.Unparen(lhs)
		if selector, ok := target.(*ast.SelectorExpr); ok {
			if record.field = a.eventField(p, selector); record.field == "" {
				continue
			}
			target = ast.Unparen(selector.X)
		}
		id, ok := target.(*ast.Ident)
		if !ok {
			continue
		}
		obj := p.info.Defs[id]
		if obj != nil && record.field == "" {
			a.declared[obj] = true
		} else {
			obj = p.info.Uses[id]
		}
		switch {
		case obj == nil:
		case len(v.Lhs) != len(v.Rhs) || v.Tok != token.ASSIGN && v.Tok != token.DEFINE:
			a.unknown[obj] = true
		default:
			record.rhs = v.Rhs[i]
			a.assigns[obj] = append(a.assigns[obj], record)
		}
	}
}

func (a *analyzer) indexSpec(p *checkedPackage, v *ast.ValueSpec) {
	for i, id := range v.Names {
		obj := p.info.Defs[id]
		if obj == nil {
			continue
		}
		a.declared[obj] = true
		record := assignment{pkg: p}
		switch len(v.Values) {
		case 0:
		case len(v.Names):
			record.rhs = v.Values[i]
		default:
			a.unknown[obj] = true
		}
		a.assigns[obj] = append(a.assigns[obj], record)
	}
}

// markUnknown gives up on the variable an expression's path starts from.
func (a *analyzer) markUnknown(p *checkedPackage, expr ast.Expr) {
	for {
		switch v := ast.Unparen(expr).(type) {
		case *ast.Ident:
			if obj := p.info.Uses[v]; obj != nil {
				a.unknown[obj] = true
			}
			if obj := p.info.Defs[v]; obj != nil {
				a.unknown[obj] = true
			}
			return
		case *ast.SelectorExpr:
			expr = v.X
		case *ast.IndexExpr:
			expr = v.X
		default:
			return
		}
	}
}

func (a *analyzer) isNamed(t types.Type, name string) bool {
	if t == nil {
		return false
	}
	named, ok := types.Unalias(t).(*types.Named)
	return ok && named.Obj().Name() == name && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == a.store
}

func (a *analyzer) isEvent(t types.Type) bool { return a.isNamed(t, "Event") }

// instances keys each type parameter one package declares by its generic
// function or type and position, and records each instantiation's arguments
// under the same key, wherever the generic is declared.
func (a *analyzer) instances(p *checkedPackage) {
	key := func(origin types.Object, list *types.TypeParamList) {
		for i := range list.Len() {
			a.params[list.At(i)] = origin.Pkg().Path() + "." + origin.Name() + "#" + strconv.Itoa(i)
		}
	}
	for _, obj := range p.info.Defs {
		switch v := obj.(type) {
		case *types.Func:
			sig := v.Type().(*types.Signature)
			key(v, sig.TypeParams())
			if recv := sig.Recv(); recv != nil {
				if named := namedOf(recv.Type()); named != nil {
					key(named.Origin().Obj(), sig.RecvTypeParams())
				}
			}
		case *types.TypeName:
			if named, ok := v.Type().(*types.Named); ok {
				key(named.Obj(), named.TypeParams())
			}
		}
	}
	for id, instance := range p.info.Instances {
		var origin types.Object
		switch v := p.info.Uses[id].(type) {
		case *types.Func:
			origin = v.Origin()
		case *types.TypeName:
			if named := namedOf(v.Type()); named != nil {
				origin = named.Origin().Obj()
			}
		}
		if origin == nil {
			continue
		}
		for i := range instance.TypeArgs.Len() {
			k := origin.Pkg().Path() + "." + origin.Name() + "#" + strconv.Itoa(i)
			a.typeArgs[k] = append(a.typeArgs[k], instance.TypeArgs.At(i))
		}
	}
}

func namedOf(t types.Type) *types.Named {
	if pointer, ok := types.Unalias(t).(*types.Pointer); ok {
		t = pointer.Elem()
	}
	named, _ := types.Unalias(t).(*types.Named)
	return named
}

// typeParamReaches reports whether any type argument of a type parameter
// satisfies reaches. A parameter the binary does not key is assumed to.
func (a *analyzer) typeParamReaches(param *types.TypeParam, reaches func(types.Type) bool) bool {
	key, ok := a.params[param]
	if !ok {
		return true
	}
	for _, arg := range a.typeArgs[key] {
		if reaches(arg) {
			return true
		}
	}
	return false
}

// holdsEvent reports whether a value of t reaches an Event a callee could
// overwrite through reflection: a pointer, slice, map, or channel whose key
// or element holds or contains one, a struct or array of such, or a type
// parameter some instantiation binds to a holder.
func (a *analyzer) holdsEvent(t types.Type, seen map[types.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true
	if param, ok := types.Unalias(t).(*types.TypeParam); ok {
		return a.typeParamReaches(param, func(arg types.Type) bool { return a.holdsEvent(arg, seen) })
	}
	switch u := t.Underlying().(type) {
	case *types.Pointer, *types.Slice, *types.Map, *types.Chan:
		elems := []types.Type{u.(interface{ Elem() types.Type }).Elem()}
		if m, ok := u.(*types.Map); ok {
			elems = append(elems, m.Key())
		}
		for _, elem := range elems {
			if a.containsEvent(elem, map[types.Type]bool{}) || a.holdsEvent(elem, seen) {
				return true
			}
		}
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
	if param, ok := types.Unalias(t).(*types.TypeParam); ok {
		return a.typeParamReaches(param, func(arg types.Type) bool { return a.containsEvent(arg, seen) })
	}
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
			site := func(node ast.Node, resolve func() []string) {
				pairs := anyPair
				if resolve != nil {
					pairs = a.evaluate(resolve)
				}
				if slices.ContainsFunc(pairs, func(pair string) bool { return strings.Contains(pair, "*") }) {
					a.sites = append(a.sites, writerSite{Function: name, Expression: normalize(a.source(node)), FunctionDigest: body, Pairs: pairs})
					return
				}
				for _, pair := range pairs {
					a.writes[pair] = true
				}
			}
			add := func(node ast.Node) { site(node, nil) }
			ast.Inspect(d, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.CallExpr:
					if a.callHoldsEvent(p, v) {
						add(v)
					}
				case *ast.CompositeLit:
					a.collectLiteral(p, v, site)
				case *ast.AssignStmt:
					a.collectAssign(p, v, site)
					values := valueTypes(p, v.Rhs)
					for i, lhs := range v.Lhs {
						if i < len(values) && a.escapesTo(p.info.Types[lhs].Type, values[i]) {
							add(lhs)
						}
					}
				case *ast.ValueSpec:
					if v.Type != nil {
						for i, value := range valueTypes(p, v.Values) {
							if a.escapesTo(p.info.Types[v.Type].Type, value) {
								add(v.Names[i])
							}
						}
					}
				case *ast.SendStmt:
					if channel, ok := under(p.info.Types[v.Chan].Type).(*types.Chan); ok && a.escapesTo(channel.Elem(), p.info.Types[v.Value].Type) {
						add(v)
					}
				case *ast.IndexExpr:
					if m, ok := under(p.info.Types[v.X].Type).(*types.Map); ok && a.escapesTo(m.Key(), p.info.Types[v.Index].Type) {
						add(v)
					}
				case *ast.FuncDecl:
					if obj, ok := p.info.Defs[v.Name].(*types.Func); ok && v.Body != nil {
						a.collectReturns(p, obj.Type().(*types.Signature), v.Body, add)
					}
				case *ast.FuncLit:
					a.collectReturns(p, p.info.Types[v].Type.(*types.Signature), v.Body, add)
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
				case *ast.BasicLit, *ast.BinaryExpr:
					if value := p.info.Types[n.(ast.Expr)].Value; value != nil && value.Kind() == constant.String {
						text := constant.StringVal(value)
						if domainEventsWrite.MatchString(text) && !a.recognizedInsert(p, file, n.(ast.Expr), text) {
							add(n)
						}
						return false
					}
				}
				return true
			})
		}
	}
}

// collectAssign adds an assignment to an Event's fields as one site: each
// assigned field's values paired with the other field's values.
func (a *analyzer) collectAssign(p *checkedPackage, v *ast.AssignStmt, site func(ast.Node, func() []string)) {
	var fields []int
	for i, lhs := range v.Lhs {
		if a.eventField(p, lhs) != "" {
			fields = append(fields, i)
		}
	}
	switch {
	case len(fields) == 0:
	case len(v.Lhs) != len(v.Rhs) || (v.Tok != token.ASSIGN && v.Tok != token.DEFINE):
		site(v, nil)
	default:
		site(v, func() []string {
			var out []string
			for _, i := range fields {
				field := a.eventField(p, v.Lhs[i])
				holder := ast.Unparen(v.Lhs[i]).(*ast.SelectorExpr).X
				other := map[string]string{"Kind": "PayloadVersion", "PayloadVersion": "Kind"}[field]
				values := map[string][]string{field: a.resolve(p, v.Rhs[i], map[seenKey]bool{}), other: a.resolveField(p, holder, other, map[seenKey]bool{})}
				out = append(out, pairs(values["Kind"], values["PayloadVersion"])...)
			}
			return out
		})
	}
}

func (a *analyzer) collectLiteral(p *checkedPackage, lit *ast.CompositeLit, site func(ast.Node, func() []string)) {
	add := func(node ast.Node) { site(node, nil) }
	// An element of []*Event may elide &Event; its literal records *Event.
	typ := p.info.Types[lit].Type
	if pointer, ok := typ.Underlying().(*types.Pointer); ok {
		typ = pointer.Elem()
	}
	if !a.isEvent(typ) {
		fields, isStruct := typ.Underlying().(*types.Struct)
		mapType, _ := typ.Underlying().(*types.Map)
		for i, element := range lit.Elts {
			var field types.Type
			if pair, ok := element.(*ast.KeyValueExpr); ok {
				if mapType != nil && a.escapesTo(mapType.Key(), p.info.Types[pair.Key].Type) {
					add(pair.Key)
				}
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
			if a.escapesTo(field, p.info.Types[element].Type) {
				add(element)
			}
		}
		return
	}
	var kind, version ast.Expr
	for _, element := range lit.Elts {
		pair, ok := element.(*ast.KeyValueExpr)
		if !ok {
			add(lit)
			return
		}
		switch pair.Key.(*ast.Ident).Name {
		case "Kind":
			kind = pair.Value
		case "PayloadVersion":
			version = pair.Value
		}
	}
	if kind != nil && version != nil {
		site(lit, func() []string {
			return pairs(a.resolve(p, kind, map[seenKey]bool{}), a.resolve(p, version, map[seenKey]bool{}))
		})
	}
}

// pairs crosses kinds with versions, skipping a zero kind or version.
func pairs(kinds, versions []string) []string {
	var out []string
	for _, kind := range kinds {
		for _, version := range versions {
			if kind != "" && version != "0" {
				out = append(out, kind+" v"+version)
			}
		}
	}
	return out
}

// evaluate resolves one site's pairs within the step budget. A site with no
// pair writes nothing; a "*" pair absorbs the rest.
func (a *analyzer) evaluate(resolve func() []string) []string {
	a.steps = 0
	set := map[string]bool{}
	for _, pair := range resolve() {
		set[pair] = true
	}
	if a.steps > resolveBudget || set["* v*"] {
		return anyPair
	}
	return slices.Sorted(maps.Keys(set))
}

func elementType(t types.Type) types.Type {
	if e, ok := t.Underlying().(interface{ Elem() types.Type }); ok {
		return e.Elem()
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

// callHoldsEvent reports a conversion into an Event, or into a type that
// reaches one, from another type, or an argument that hands an Event holder
// to an interface or unsafe pointer.
func (a *analyzer) callHoldsEvent(p *checkedPackage, call *ast.CallExpr) bool {
	fun := p.info.Types[call.Fun]
	if fun.IsType() {
		if len(call.Args) != 1 {
			return false
		}
		from := p.info.Types[call.Args[0]]
		reaches := a.containsEvent(fun.Type, map[types.Type]bool{}) || a.holdsEvent(fun.Type, map[types.Type]bool{})
		return (reaches && !from.IsNil() && !types.Identical(fun.Type, from.Type)) || a.escapesTo(fun.Type, from.Type)
	}
	signature, ok := fun.Type.(*types.Signature)
	if !ok {
		return false
	}
	for i, value := range valueTypes(p, call.Args) {
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
		if a.escapesTo(param, value) {
			return true
		}
	}
	return false
}

func isUnsafePointer(t types.Type) bool {
	basic, ok := t.Underlying().(*types.Basic)
	return ok && basic.Kind() == types.UnsafePointer
}

// escapesTo reports whether a value of type from hands an Event holder to a
// slot of interface or unsafe pointer type.
func (a *analyzer) escapesTo(slot, from types.Type) bool {
	return slot != nil && (types.IsInterface(slot) || isUnsafePointer(slot)) && a.holdsEvent(from, map[types.Type]bool{})
}

// valueTypes lists the types of values, spreading a multi-value expression.
func valueTypes(p *checkedPackage, values []ast.Expr) []types.Type {
	var out []types.Type
	for _, value := range values {
		if tuple, ok := p.info.Types[value].Type.(*types.Tuple); ok {
			for i := range tuple.Len() {
				out = append(out, tuple.At(i).Type())
			}
		} else {
			out = append(out, p.info.Types[value].Type)
		}
	}
	return out
}

func under(t types.Type) types.Type {
	if t == nil {
		return nil
	}
	return t.Underlying()
}

// collectReturns lists each return in body that hands an Event holder to an
// interface result of sig; a nested function literal has its own results.
func (a *analyzer) collectReturns(p *checkedPackage, sig *types.Signature, body *ast.BlockStmt, add func(ast.Node)) {
	ast.Inspect(body, func(n ast.Node) bool {
		if ret, ok := n.(*ast.ReturnStmt); ok {
			for i, value := range valueTypes(p, ret.Results) {
				if i < sig.Results().Len() && a.escapesTo(sig.Results().At(i).Type(), value) {
					add(ret)
					break
				}
			}
		}
		_, nested := n.(*ast.FuncLit)
		return !nested
	})
}

// recognizedInsert accepts only the append statement form: a literal INSERT
// INTO domain_events with one placeholder per distinct plain lowercase column,
// passed directly to a call whose kind and payload_version arguments read one
// Event's fields.
func (a *analyzer) recognizedInsert(p *checkedPackage, file *ast.File, lit ast.Expr, text string) bool {
	match := domainEventsShape.FindStringSubmatch(text)
	if match == nil {
		return false
	}
	columns, values := strings.Split(match[1], ","), strings.Split(match[2], ",")
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
	seen := map[string]bool{}
	for i, column := range columns {
		column = strings.TrimSpace(column)
		if strings.TrimSpace(values[i]) != "?" || !sqlColumn.MatchString(column) || seen[column] {
			return false
		}
		seen[column] = true
		field := a.eventField(p, call.Args[position+1+i])
		selector, _ := ast.Unparen(call.Args[position+1+i]).(*ast.SelectorExpr)
		switch column {
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

func union(sets ...[]string) []string {
	values := map[string]bool{}
	for _, set := range sets {
		for _, value := range set {
			values[value] = true
		}
	}
	if values["*"] {
		return anyValue
	}
	return slices.Sorted(maps.Keys(values))
}

// resolve reduces an expression to its finite value set: a constant, a
// conversion of a resolvable value, an Event field read, or a resolvable
// local. Anything else is "*".
func (a *analyzer) resolve(p *checkedPackage, expr ast.Expr, seen map[seenKey]bool) []string {
	a.steps++
	if a.steps > resolveBudget || expr == nil {
		return anyValue
	}
	if tv := p.info.Types[expr]; tv.Value != nil {
		switch tv.Value.Kind() {
		case constant.String:
			return []string{constant.StringVal(tv.Value)}
		case constant.Int:
			return []string{tv.Value.ExactString()}
		}
		return anyValue
	}
	switch v := expr.(type) {
	case *ast.ParenExpr:
		return a.resolve(p, v.X, seen)
	case *ast.CallExpr:
		// A conversion keeps its value only between identical basic types:
		// string(n) of an integer and a narrowing integer conversion do not.
		if target := p.info.Types[v.Fun]; target.IsType() && len(v.Args) == 1 && types.Identical(target.Type.Underlying(), under(p.info.Types[v.Args[0]].Type)) {
			return a.resolve(p, v.Args[0], seen)
		}
	case *ast.SelectorExpr:
		if field := a.eventField(p, v); field != "" {
			return a.resolveField(p, v.X, field, seen)
		}
	case *ast.Ident:
		return a.resolveVar(p, v, "", seen)
	}
	return anyValue
}

// resolveVar resolves a declared local, or with a field that Event field of
// it, as the union of every value assigned to it.
func (a *analyzer) resolveVar(p *checkedPackage, id *ast.Ident, field string, seen map[seenKey]bool) []string {
	obj, ok := p.info.Uses[id].(*types.Var)
	if !ok || !a.declared[obj] || a.unknown[obj] {
		return anyValue
	}
	key := seenKey{obj, field}
	if seen[key] {
		return nil
	}
	seen[key] = true
	defer delete(seen, key)
	var values [][]string
	for _, assign := range a.assigns[obj] {
		if assign.field == "" || assign.field == field {
			values = append(values, a.resolveAssigned(assign, field, seen))
		}
	}
	return union(values...)
}

// zeroField is the zero value of an Event field.
func zeroField(field string) []string {
	return []string{map[string]string{"Kind": "", "PayloadVersion": "0"}[field]}
}

func (a *analyzer) resolveAssigned(assign assignment, field string, seen map[seenKey]bool) []string {
	switch {
	case field != "" && assign.field == field:
		return a.resolve(assign.pkg, assign.rhs, seen)
	case assign.rhs == nil && field != "":
		return zeroField(field)
	case assign.rhs == nil:
		return anyValue
	case field == "":
		return a.resolve(assign.pkg, assign.rhs, seen)
	}
	return a.resolveField(assign.pkg, assign.rhs, field, seen)
}

// resolveField resolves an Event expression's Kind or PayloadVersion.
func (a *analyzer) resolveField(p *checkedPackage, expr ast.Expr, field string, seen map[seenKey]bool) []string {
	a.steps++
	if a.steps > resolveBudget {
		return anyValue
	}
	switch v := ast.Unparen(expr).(type) {
	case *ast.CompositeLit:
		if !a.isEvent(p.info.Types[v].Type) {
			return anyValue
		}
		for _, element := range v.Elts {
			pair, ok := element.(*ast.KeyValueExpr)
			if !ok {
				return anyValue
			}
			if pair.Key.(*ast.Ident).Name == field {
				return a.resolve(p, pair.Value, seen)
			}
		}
		return zeroField(field)
	case *ast.Ident:
		return a.resolveVar(p, v, field, seen)
	}
	return anyValue
}
