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
// flow-insensitively: a local is the union of every value assigned to it,
// except an assignment in an if arm that never runs, and one that a later
// statement of its own list overwrites before anything reads it. An arm never
// runs when its condition constantly selects the other arm; a condition whose
// resolution cut a cycle is never constant. Overwriting applies only to a
// local no closure captures, and only across statements with no branch
// statement.
//
// A parameter resolves through the calls of its function and a call result
// through the callee's returns, with the callee's parameters bound to that
// call's arguments. A site that reads its own function's parameters is
// evaluated once per static call of that function. Only a function every call
// of which the index sees takes part: not one used as a value, nor an exported
// method, nor a method whose name an interface method use names. The result
// of a function that defers, whose deferred call may recover a panic into
// zero results, a method expression call,
// a receiver, a global, a call through a value, or a local whose address
// escapes is "*". A tuple assignment takes one result of a static call or
// the entry of an event kind registry read.
//
// The event kind registry is a fact only when it is a package-level map
// literal the binary only reads by index, range, or len. A read of kind K is
// "reg:K", and its CurrentVersion is "cur:K": K's registered current version,
// or 0 when K is not registered. A comma-ok read is constantly true for a key
// of the literal. Such values are never compared, and a
// registry read local whose fields are written, or whose address is taken, is
// "*". A site whose pairs all resolve to finite
// values adds them to the release's writes; any other site is open. Pairs are
// "kind vVERSION", and "*" is any value. A zero kind or version is a value like any other: a
// recognized insert can write an Event that append never validated.
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
	// Comparison is the proven route of an assignment whose function's
	// Event result reaches, through its one static caller, only a bool
	// comparator; nil otherwise.
	Comparison *comparisonRoute `json:"comparison_only,omitempty"`
	// Confinement is "upcaster" on an assignment, outside any loop or
	// closure, of fields of the site function's own Event parameter, when
	// the function is reachable only as an Upcaster value through the one
	// dispatcher, names no result, and has no label, the parameter's Kind and whole value
	// are never assigned, it is never captured or addressed, and after the
	// site it is only returned or the base of a field selector.
	Confinement string `json:"confinement,omitempty"`
}

// upcastDispatcher is the one function declaration that calls Upcaster
// values and holds every other expression of the Upcaster type, with the
// conditions of its if statements whose body ends in a return, and its
// callers: every declared function that calls it, or calls a caller that
// passes its own parameter as the EventKindRegistration argument.
type upcastDispatcher struct {
	Function       string      `json:"function"`
	FunctionDigest string      `json:"function_digest"`
	Guards         []string    `json:"guards"`
	Callers        []namedHash `json:"callers"`
}

type namedHash struct {
	Function string `json:"function"`
	Digest   string `json:"digest"`
}

// comparisonRoute names the caller that binds a comparison-only function's
// Event result and the one bool comparator it feeds, each by digest.
type comparisonRoute struct {
	Caller           string `json:"caller"`
	CallerDigest     string `json:"caller_digest"`
	Comparator       string `json:"comparator"`
	ComparatorDigest string `json:"comparator_digest"`
}

// pureComparators are the calls a comparison-only comparator may hand its
// event to.
var pureComparators = map[string]bool{"bytes.Equal": true, "reflect.DeepEqual": true, "encoding/json.Marshal": true}

// adaptRoute names the one function that calls the recorded-schema adapt
// field, the forwarders its result passes through, and every function the
// registry's schema literals name as an adapt value together with every
// static callee of one whose result holds an Event, when the analysis
// proves every adapt result reaches only the upcast dispatcher. The value
// digests pin what an adapt can do to an event; a changed adapt function,
// or a changed function an adapt delegates its event to, changes a digest
// and refuses every adapt-keyed entry.
type adaptRoute struct {
	Function       string      `json:"function"`
	FunctionDigest string      `json:"function_digest"`
	Forwarders     []namedHash `json:"forwarders"`
	Values         []namedHash `json:"values"`
}

// writerAnalysis is one release binary's event writers. Writes are the pairs
// fully resolved sites produce.
// WorkItemFolds and CheckSubject are workItemFolds' result.
type writerAnalysis struct {
	Analyzer      string            `json:"analyzer"`
	Refused       string            `json:"refused,omitempty"`
	Writes        []string          `json:"writes,omitempty"`
	Open          []writerSite      `json:"open_sites,omitempty"`
	Dispatcher    *upcastDispatcher `json:"upcast_dispatcher,omitempty"`
	Adapt         *adaptRoute       `json:"adapt_route,omitempty"`
	WorkItemFolds []string          `json:"work_item_folds,omitempty"`
	CheckSubject  string            `json:"check_subject,omitempty"`
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
// Event field of it. A nil rhs is a var declaration's zero value; result is
// the value of a multi-value rhs it takes, or -1. path lists its enclosing if
// arms; list and pos place its statement, pos -1 when it is not in a
// statement list.
type assignment struct {
	rhs    ast.Expr
	result int
	field  string
	pkg    *checkedPackage
	path   []branch
	list   []ast.Stmt
	pos    int
}

// branch is one enclosing if statement and the arm the node sits in.
type branch struct {
	stmt *ast.IfStmt
	then bool
}

// seenKey is one variable or call being resolved under one call binding.
type seenKey struct {
	node  any
	field string
	env   *env
}

// env binds one function's parameters to one call's arguments, which resolve
// under caller. A strict env binds no arguments: its parameters are "?", and
// the site is then evaluated once per static call of its function.
type env struct {
	function string
	args     []ast.Expr
	pkg      *checkedPackage
	caller   *env
	strict   bool
}

// scope is one resolution's cycle set and the call binding it runs under.
type scope struct {
	seen map[seenKey]bool
	env  *env
}

func newScope(e *env) *scope { return &scope{seen: map[seenKey]bool{}, env: e} }

// formal is one non-variadic parameter of a function.
type formal struct {
	function string
	index    int
}

// function is one declared function: its static calls, its returns with
// their enclosing if arms, whether it is used other than by a call, whether
// it is an element of an Upcaster container literal, and whether it defers.
type function struct {
	decl     *ast.FuncDecl
	pkg      *checkedPackage
	calls    []callSite
	returns  []returned
	value    bool
	upcaster bool
	deferred bool
}

// returned is one return statement's results and enclosing if arms.
type returned struct {
	results []ast.Expr
	at      assignment
}

// callSite is one static call, its package, the declared function it sits
// in directly (nil in a closure or outside any function), and the declared
// function whose body encloses it even through closures, for proofs that
// pin the enclosing declaration's digest.
type callSite struct {
	call     *ast.CallExpr
	pkg      *checkedPackage
	caller   *function
	encloser *function
}

// analyzer holds one binary's type-checked packages, the flow facts the
// resolver reads, and the sites found. typeArgs lists, per type parameter
// key, every type argument the binary instantiates it with.
type analyzer struct {
	fset       *token.FileSet
	store      string
	sources    map[string][]byte
	sites      []writerSite
	writes     map[string]bool
	params     map[*types.TypeParam]string
	typeArgs   map[string][]types.Type
	assigns    map[types.Object][]assignment
	declared   map[types.Object]bool
	unknown    map[types.Object]bool
	captured   map[types.Object]bool
	formals    map[types.Object]formal
	funcs      map[string]*function
	funcNames  map[*function]string
	callees    map[*ast.Ident]bool
	dynamic    map[string]bool
	registry   types.Object
	registered map[string]bool
	steps      int
	cuts       int
	// upcastValues are the function identifiers listed as elements of an
	// Upcaster container literal; upcasterExprs and upcasterCalls the
	// top-level declarations holding an Upcaster-typed expression or call.
	upcastValues  map[*ast.Ident]bool
	upcasterExprs map[ast.Decl]*checkedPackage
	upcasterCalls map[ast.Decl]*checkedPackage
	dispatcher    *upcastDispatcher
	// adaptField is recordedPayloadSchema's adapt func field; adaptCalls the
	// top-level declarations that call it, adaptEscapes how often the field
	// is mentioned anywhere else but a call or a nil comparison or a schema
	// literal sets it to anything but an identifier, and adaptValues the
	// identifiers the schema literals name as adapt values.
	adaptField   *types.Var
	adaptCalls   map[ast.Decl]*checkedPackage
	adaptValues  map[*ast.Ident]*checkedPackage
	adaptEscapes int
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
	a.captured, a.formals, a.funcs, a.callees, a.dynamic = map[types.Object]bool{}, map[types.Object]formal{}, map[string]*function{}, map[*ast.Ident]bool{}, map[string]bool{}
	a.funcNames = map[*function]string{}
	a.upcastValues, a.upcasterExprs, a.upcasterCalls = map[*ast.Ident]bool{}, map[ast.Decl]*checkedPackage{}, map[ast.Decl]*checkedPackage{}
	a.adaptCalls, a.adaptValues = map[ast.Decl]*checkedPackage{}, map[*ast.Ident]*checkedPackage{}
	if schema, ok := store.types.Scope().Lookup("recordedPayloadSchema").(*types.TypeName); ok {
		if fields, ok := schema.Type().Underlying().(*types.Struct); ok {
			for i := range fields.NumFields() {
				if fields.Field(i).Name() == "adapt" {
					a.adaptField = fields.Field(i)
				}
			}
		}
	}
	for _, p := range packages {
		a.instances(p)
		a.index(p)
	}
	var literal *ast.CompositeLit
	a.registry, a.registered, literal = readRegistry(store)
	a.dispatcher = a.findDispatcher()
	result := writerAnalysis{Analyzer: analyzerVersion(), Dispatcher: a.dispatcher, Adapt: a.adaptRoute()}
	for _, p := range packages {
		a.collect(p)
	}
	result.Writes = slices.Sorted(maps.Keys(a.writes))
	result.Open = a.sites
	sort.Slice(result.Open, func(i, j int) bool {
		x, y := result.Open[i], result.Open[j]
		return x.Function < y.Function || x.Function == y.Function && x.Expression < y.Expression
	})
	result.WorkItemFolds, result.CheckSubject = a.workItemFolds(store, literal)
	return result, nil
}

// index records each local's declaration and assignments. A local declared by
// a var statement or := is resolvable; one whose address escapes, which is
// incremented, or which a range or op-assignment writes is unknown.
func (a *analyzer) index(p *checkedPackage) {
	for _, file := range p.files {
		var stack []ast.Node
		ast.Inspect(file, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			defer func() { stack = append(stack, n) }()
			a.indexUpcaster(p, n, stack)
			switch v := n.(type) {
			case *ast.AssignStmt:
				a.indexAssign(p, v, place(p, stack, v))
			case *ast.DeclStmt:
				if decl, ok := v.Decl.(*ast.GenDecl); ok && decl.Tok == token.VAR {
					for _, spec := range decl.Specs {
						a.indexSpec(p, spec.(*ast.ValueSpec), place(p, stack, v))
					}
				}
			case *ast.Ident:
				a.markCaptured(p, v, stack)
				if f, ok := p.info.Uses[v].(*types.Func); ok && f.Signature().Recv() != nil && types.IsInterface(f.Signature().Recv().Type()) {
					a.dynamic[f.Name()] = true
				}
				if f, ok := p.info.Uses[v].(*types.Func); ok && !a.callees[v] {
					if a.upcastValues[v] {
						a.function(f).upcaster = true
					} else {
						a.function(f).value = true
					}
				}
			case *ast.FuncDecl:
				a.indexFunc(p, v)
			case *ast.CallExpr:
				if selector, ok := ast.Unparen(v.Fun).(*ast.SelectorExpr); ok && a.isAdaptField(p, selector) {
					a.adaptCalls[stack[1].(ast.Decl)] = p
					a.callees[selector.Sel] = true
				}
				a.indexCall(p, v, a.enclosingFunc(p, stack), a.enclosingDecl(p, stack))
			case *ast.DeferStmt:
				if fn := a.enclosingFunc(p, stack); fn != nil {
					fn.deferred = true
				}
			case *ast.ReturnStmt:
				if fn := a.enclosingFunc(p, stack); fn != nil {
					fn.returns = append(fn.returns, returned{v.Results, place(p, stack, v)})
				}
			case *ast.IncDecStmt:
				a.markUnknown(p, v.X)
			case *ast.UnaryExpr:
				if v.Op == token.AND {
					a.markUnknown(p, v.X)
				}
			case *ast.SelectorExpr:
				if a.isAdaptField(p, v) && !a.callees[v.Sel] {
					if binary, ok := stack[len(stack)-1].(*ast.BinaryExpr); !(ok && (binary.Op == token.EQL || binary.Op == token.NEQ) && (p.info.Types[binary.X].IsNil() || p.info.Types[binary.Y].IsNil())) {
						a.adaptEscapes++
					}
				}
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
			case *ast.CompositeLit:
				typ := p.info.Types[v].Type
				if pointer, ok := typ.(*types.Pointer); ok {
					typ = pointer.Elem()
				}
				if a.adaptField != nil && a.isNamed(typ, "recordedPayloadSchema") {
					fields := typ.Underlying().(*types.Struct)
					for i, element := range v.Elts {
						adapt := false
						if pair, ok := element.(*ast.KeyValueExpr); ok {
							element = pair.Value
							if key, ok := pair.Key.(*ast.Ident); ok && key.Name == "adapt" {
								adapt = true
							}
						} else if i < fields.NumFields() && fields.Field(i) == a.adaptField {
							adapt = true
						}
						if !adapt {
							continue
						}
						if id, ok := ast.Unparen(element).(*ast.Ident); ok {
							a.adaptValues[id] = p
						} else {
							a.adaptEscapes++
						}
					}
				}
			}
			return true
		})
	}
}

// function returns the record of f's generic origin.
func (a *analyzer) function(f *types.Func) *function {
	name := f.Origin().FullName()
	if a.funcs[name] == nil {
		a.funcs[name] = &function{}
		a.funcNames[a.funcs[name]] = name
	}
	return a.funcs[name]
}

// indexFunc records a declared function and its parameters.
func (a *analyzer) indexFunc(p *checkedPackage, decl *ast.FuncDecl) {
	f, ok := p.info.Defs[decl.Name].(*types.Func)
	if !ok {
		return
	}
	fn := a.function(f)
	fn.decl, fn.pkg = decl, p
	index := 0
	for i, field := range decl.Type.Params.List {
		for _, id := range field.Names {
			if obj := p.info.Defs[id]; obj != nil && !(f.Signature().Variadic() && i == len(decl.Type.Params.List)-1) {
				a.formals[obj] = formal{f.Origin().FullName(), index}
			}
			index++
		}
		if len(field.Names) == 0 {
			index++
		}
	}
}

// indexCall records a static call. A method expression call, whose arguments
// start with the receiver, uses its function as a value.
func (a *analyzer) indexCall(p *checkedPackage, call *ast.CallExpr, caller, encloser *function) {
	id := calleeIdent(call.Fun)
	f, ok := p.info.Uses[id].(*types.Func)
	if !ok {
		return
	}
	a.callees[id] = true
	if sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr); ok && p.info.Selections[sel] != nil && p.info.Selections[sel].Kind() == types.MethodExpr {
		a.function(f).value = true
		return
	}
	a.function(f).calls = append(a.function(f).calls, callSite{call, p, caller, encloser})
}

// enclosingDecl returns the declared function whose body encloses the
// current node, through closures, or nil outside any declaration.
func (a *analyzer) enclosingDecl(p *checkedPackage, stack []ast.Node) *function {
	for i := len(stack) - 1; i >= 0; i-- {
		if decl, ok := stack[i].(*ast.FuncDecl); ok {
			if f, ok := p.info.Defs[decl.Name].(*types.Func); ok {
				return a.function(f)
			}
			return nil
		}
	}
	return nil
}

func calleeIdent(fun ast.Expr) *ast.Ident {
	switch f := ast.Unparen(fun).(type) {
	case *ast.Ident:
		return f
	case *ast.SelectorExpr:
		return f.Sel
	case *ast.IndexExpr:
		return calleeIdent(f.X)
	case *ast.IndexListExpr:
		return calleeIdent(f.X)
	}
	return nil
}

// enclosingFunc is the declared function a node sits in directly, not in a
// closure.
func (a *analyzer) enclosingFunc(p *checkedPackage, stack []ast.Node) *function {
	for i := len(stack) - 1; i >= 0; i-- {
		switch v := stack[i].(type) {
		case *ast.FuncLit:
			return nil
		case *ast.FuncDecl:
			if f, ok := p.info.Defs[v.Name].(*types.Func); ok {
				return a.function(f)
			}
			return nil
		}
	}
	return nil
}

// contextual reports whether the index sees every call of a function, and
// there is one: a function with none is dead code, which still counts.
func (a *analyzer) contextual(name string) bool {
	fn := a.funcs[name]
	if fn == nil || fn.decl == nil || fn.decl.Body == nil || fn.value || fn.upcaster || len(fn.calls) == 0 {
		return false
	}
	return fn.decl.Recv == nil || !ast.IsExported(fn.decl.Name.Name) && !a.dynamic[fn.decl.Name.Name]
}

// place records where stmt sits: its statement list and position, and its
// enclosing if arms up to the function or closure.
func place(p *checkedPackage, stack []ast.Node, stmt ast.Stmt) assignment {
	record := assignment{pkg: p, result: -1}
	switch parent := stack[len(stack)-1].(type) {
	case *ast.BlockStmt:
		record.list = parent.List
	case *ast.CaseClause:
		record.list = parent.Body
	case *ast.CommClause:
		record.list = parent.Body
	}
	record.pos = slices.Index(record.list, stmt)
	for i := len(stack) - 1; i > 0; i-- {
		switch v := stack[i-1].(type) {
		case *ast.FuncLit, *ast.FuncDecl:
			return record
		case *ast.IfStmt:
			if stack[i] == v.Body || stack[i] == v.Else {
				record.path = append(record.path, branch{v, stack[i] == v.Body})
			}
		}
	}
	return record
}

// markCaptured marks a variable used inside a closure it is declared outside.
func (a *analyzer) markCaptured(p *checkedPackage, id *ast.Ident, stack []ast.Node) {
	obj := p.info.Uses[id]
	for i := len(stack) - 1; obj != nil && i >= 0; i-- {
		if lit, ok := stack[i].(*ast.FuncLit); ok {
			if obj.Pos() < lit.Pos() || obj.Pos() >= lit.End() {
				a.captured[obj] = true
			}
			return
		}
	}
}

func (a *analyzer) indexAssign(p *checkedPackage, v *ast.AssignStmt, at assignment) {
	for i, lhs := range v.Lhs {
		record := at
		target := ast.Unparen(lhs)
		if selector, ok := target.(*ast.SelectorExpr); ok {
			if record.field = a.eventField(p, selector); record.field == "" {
				if a.isNamed(p.info.Types[selector.X].Type, "EventKindRegistration") {
					a.markUnknown(p, selector.X)
				}
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
		case v.Tok != token.ASSIGN && v.Tok != token.DEFINE || len(v.Lhs) != len(v.Rhs) && len(v.Rhs) != 1:
			a.unknown[obj] = true
		case len(v.Lhs) != len(v.Rhs):
			record.rhs, record.result = v.Rhs[0], i
			a.assigns[obj] = append(a.assigns[obj], record)
		default:
			record.rhs = v.Rhs[i]
			a.assigns[obj] = append(a.assigns[obj], record)
		}
	}
}

func (a *analyzer) indexSpec(p *checkedPackage, v *ast.ValueSpec, at assignment) {
	for i, id := range v.Names {
		obj := p.info.Defs[id]
		if obj == nil {
			continue
		}
		a.declared[obj] = true
		record := at
		switch len(v.Values) {
		case 0:
		case len(v.Names):
			record.rhs = v.Values[i]
		case 1:
			record.rhs, record.result = v.Values[0], i
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
			site := func(node ast.Node, resolve func(*env) []string) {
				pairs := anyPair
				if resolve != nil {
					pairs = a.evaluate(a.siteFunction(p, d), resolve)
				}
				if slices.ContainsFunc(pairs, func(pair string) bool { return strings.Contains(pair, "*") }) {
					open := writerSite{Function: name, Expression: normalize(a.source(node)), FunctionDigest: body, Pairs: pairs}
					if assign, ok := node.(*ast.AssignStmt); ok {
						if a.confinedSite(p, d, assign) {
							open.Confinement = "upcaster"
						} else {
							open.Comparison = a.comparisonSite(p, d, assign)
						}
					}
					a.sites = append(a.sites, open)
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
func (a *analyzer) collectAssign(p *checkedPackage, v *ast.AssignStmt, site func(ast.Node, func(*env) []string)) {
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
		site(v, func(e *env) []string {
			var out []string
			for _, i := range fields {
				field := a.eventField(p, v.Lhs[i])
				holder := ast.Unparen(v.Lhs[i]).(*ast.SelectorExpr).X
				other := map[string]string{"Kind": "PayloadVersion", "PayloadVersion": "Kind"}[field]
				values := map[string][]string{field: a.resolve(p, v.Rhs[i], newScope(e)), other: a.resolveField(p, holder, other, newScope(e))}
				out = append(out, pairs(values["Kind"], values["PayloadVersion"])...)
			}
			return out
		})
	}
}

func (a *analyzer) collectLiteral(p *checkedPackage, lit *ast.CompositeLit, site func(ast.Node, func(*env) []string)) {
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
		site(lit, func(e *env) []string {
			return pairs(a.resolve(p, kind, newScope(e)), a.resolve(p, version, newScope(e)))
		})
	}
}

// indexUpcaster records an Upcaster container literal's function elements,
// and the top-level declaration of every Upcaster-typed expression and call
// and of every node handsOnUpcaster reports. An operand of a comparison with
// nil neither calls nor hands on a value.
func (a *analyzer) indexUpcaster(p *checkedPackage, n ast.Node, stack []ast.Node) {
	if lit, ok := n.(*ast.CompositeLit); ok && a.isNamed(elementType(p.info.Types[lit].Type), "Upcaster") {
		for _, element := range lit.Elts {
			if pair, ok := element.(*ast.KeyValueExpr); ok {
				element = pair.Value
			}
			if id, ok := ast.Unparen(element).(*ast.Ident); ok {
				a.upcastValues[id] = true
			}
		}
	}
	if len(stack) < 2 {
		return
	}
	decl, _ := stack[1].(ast.Decl)
	if a.handsOnUpcaster(p, n) {
		a.upcasterExprs[decl] = p
	}
	expr, ok := n.(ast.Expr)
	if tv, typed := p.info.Types[expr]; !ok || !typed || tv.IsType() || !a.isNamed(tv.Type, "Upcaster") {
		return
	}
	if binary, ok := stack[len(stack)-1].(*ast.BinaryExpr); ok && (binary.Op == token.EQL || binary.Op == token.NEQ) && (p.info.Types[binary.X].IsNil() || p.info.Types[binary.Y].IsNil()) {
		return
	}
	a.upcasterExprs[decl] = p
	if call, ok := stack[len(stack)-1].(*ast.CallExpr); ok && call.Fun == expr {
		a.upcasterCalls[decl] = p
	}
}

// handsOnUpcaster reports a node that moves an Upcaster value where no
// expression of the Upcaster type shows it: a comma-ok read, a range that
// assigns existing variables, or an instantiation whose type argument is an
// Upcaster or holds one.
func (a *analyzer) handsOnUpcaster(p *checkedPackage, n ast.Node) bool {
	upcaster := func(t types.Type) bool {
		return a.isNamed(t, "Upcaster") || t != nil && a.isNamed(elementType(t), "Upcaster")
	}
	var holders []types.Type
	switch v := n.(type) {
	case *ast.RangeStmt:
		if v.Tok == token.ASSIGN {
			holders = append(holders, p.info.Types[v.X].Type)
		}
	case *ast.Ident:
		if instance, ok := p.info.Instances[v]; ok {
			holders = slices.Collect(instance.TypeArgs.Types())
		}
	case ast.Expr:
		if tuple, ok := p.info.Types[v].Type.(*types.Tuple); ok {
			for variable := range tuple.Variables() {
				holders = append(holders, variable.Type())
			}
		}
	}
	return slices.ContainsFunc(holders, upcaster)
}

// findDispatcher returns the one function declaration that calls Upcaster
// values when it also holds every other Upcaster-typed expression, or nil.
func (a *analyzer) findDispatcher() *upcastDispatcher {
	if len(a.upcasterCalls) != 1 || len(a.upcasterExprs) != 1 {
		return nil
	}
	var decl ast.Decl
	var p *checkedPackage
	for decl, p = range a.upcasterCalls {
	}
	fn, ok := decl.(*ast.FuncDecl)
	if !ok {
		return nil
	}
	callers, ok := a.dispatchCallers(a.funcs[a.siteFunction(p, fn)])
	if !ok {
		return nil
	}
	guards := []string{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if stmt, ok := n.(*ast.IfStmt); ok && len(stmt.Body.List) > 0 {
			if _, returns := stmt.Body.List[len(stmt.Body.List)-1].(*ast.ReturnStmt); returns {
				guards = append(guards, types.ExprString(stmt.Cond))
			}
		}
		return true
	})
	name, body := a.declName(p, fn)
	return &upcastDispatcher{Function: name, FunctionDigest: body, Guards: guards, Callers: callers}
}

// dispatchCallers lists upcastDispatcher's callers of dispatcher, sorted, or
// reports false when it or a forwarding caller is used other than by a
// static call, or a call sits in a closure or outside any function.
func (a *analyzer) dispatchCallers(dispatcher *function) ([]namedHash, bool) {
	var callers []namedHash
	seen, queued := map[*function]bool{}, map[*function]bool{dispatcher: true}
	for queue := []*function{dispatcher}; len(queue) > 0; queue = queue[1:] {
		fn := queue[0]
		if fn == nil || fn.value || fn.upcaster {
			return nil, false
		}
		for _, call := range fn.calls {
			if call.caller == nil || call.caller.decl == nil {
				return nil, false
			}
			if !seen[call.caller] {
				seen[call.caller] = true
				name, body := a.declName(call.caller.pkg, call.caller.decl)
				callers = append(callers, namedHash{name, body})
			}
			for _, arg := range call.call.Args {
				id, isIdent := ast.Unparen(arg).(*ast.Ident)
				if param, ok := a.formals[call.pkg.info.Uses[id]]; isIdent && ok && !queued[call.caller] && a.funcs[param.function] == call.caller && a.isNamed(call.pkg.info.Types[arg].Type, "EventKindRegistration") {
					queued[call.caller] = true
					queue = append(queue, call.caller)
					break
				}
			}
		}
	}
	slices.SortFunc(callers, func(x, y namedHash) int { return strings.Compare(x.Function, y.Function) })
	return callers, true
}

// isAdaptField reports a selector that reads the recorded-schema adapt field.
func (a *analyzer) isAdaptField(p *checkedPackage, selector *ast.SelectorExpr) bool {
	selection := p.info.Selections[selector]
	return a.adaptField != nil && selection != nil && selection.Kind() == types.FieldVal && selection.Obj() == a.adaptField
}

// adaptRoute proves the recorded-schema adapt structure: exactly one declared
// function calls the adapt field, the field is mentioned nowhere else but
// that call (a nil comparison excepted), each adapt result is assigned to an
// uncaptured Event local outside any loop, and every later use of that local
// is an argument of the upcast dispatcher itself or of a forwarder whose own
// matching parameter is only ever passed to the dispatcher. A label that a
// goto could replay, a package-level target, and a named result the caller
// returns without naming are excluded only by the pinned caller digest, not
// by this proof, exactly as in the censused analyzer. Any other shape leaves
// the route nil, and a release whose route is nil cannot match an
// adapt-keyed exemption entry.
func (a *analyzer) adaptRoute() *adaptRoute {
	if a.dispatcher == nil || a.adaptField == nil || a.adaptEscapes != 0 || len(a.adaptCalls) != 1 {
		return nil
	}
	var decl ast.Decl
	var p *checkedPackage
	for decl, p = range a.adaptCalls {
	}
	fn, ok := decl.(*ast.FuncDecl)
	if !ok || fn.Body == nil {
		return nil
	}
	parents := parentsOf(fn)
	forwarders := map[string]namedHash{}
	held := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
		if !ok || !a.isAdaptField(p, selector) {
			return true
		}
		assign, ok := parents[call].(*ast.AssignStmt)
		if !ok || len(assign.Rhs) != 1 || inLoop(parents, assign, fn) {
			held = true
			return true
		}
		id, ok := assign.Lhs[0].(*ast.Ident)
		obj := p.info.ObjectOf(id)
		if !ok || obj == nil || !a.isEvent(obj.Type()) || a.captured[obj] || a.unknown[obj] {
			held = true
			return true
		}
		for _, use := range usesAfter(p, fn, obj, assign.End()) {
			index, ok := argumentOf(parents, use)
			callee := calleeName(p, callOf(parents, use))
			switch {
			case !ok:
				held = true
			case callee == a.dispatcher.Function:
			case a.forwardsToDispatcher(callee, index):
				name, digest := a.declName(a.funcs[callee].pkg, a.funcs[callee].decl)
				forwarders[callee] = namedHash{name, digest}
			default:
				held = true
			}
		}
		return true
	})
	if held {
		return nil
	}
	name, digest := a.declName(p, fn)
	route := &adaptRoute{Function: name, FunctionDigest: digest, Forwarders: []namedHash{}, Values: []namedHash{}}
	for _, forwarder := range forwarders {
		route.Forwarders = append(route.Forwarders, forwarder)
	}
	slices.SortFunc(route.Forwarders, func(x, y namedHash) int { return strings.Compare(x.Function, y.Function) })
	// outbound lists, per function name, the static calls its declaration
	// makes, read once from the index's inbound call records.
	outbound := map[string][]*ast.CallExpr{}
	for _, fn := range a.funcs {
		if fn == nil {
			continue
		}
		for _, site := range fn.calls {
			if site.caller != nil {
				outbound[a.funcNames[site.caller]] = append(outbound[a.funcNames[site.caller]], site.call)
			}
		}
	}
	// pin records one function in the value set and follows its static
	// callees: an adapt value's event can come from a callee, so every
	// callee whose result holds an Event is pinned too, recursively. What
	// the adapt does to an event stays bound by a digest or the route
	// refuses.
	seen := map[string]bool{}
	var pin func(string) bool
	pin = func(name string) bool {
		target := a.funcs[name]
		if target == nil || target.decl == nil {
			return false
		}
		valueName, valueDigest := a.declName(target.pkg, target.decl)
		if seen[valueName+"\x00"+valueDigest] {
			return true
		}
		seen[valueName+"\x00"+valueDigest] = true
		route.Values = append(route.Values, namedHash{valueName, valueDigest})
		for _, call := range outbound[name] {
			callee, ok := target.pkg.info.Uses[calleeIdent(call.Fun)].(*types.Func)
			if !ok {
				return false
			}
			results := callee.Origin().Signature().Results()
			reaches := false
			for i := range results.Len() {
				reaches = reaches || a.isEvent(results.At(i).Type()) || a.holdsEvent(results.At(i).Type(), map[types.Type]bool{})
			}
			if reaches && !pin(callee.Origin().FullName()) {
				return false
			}
		}
		return true
	}
	for id, pkg := range a.adaptValues {
		f, ok := pkg.info.Uses[id].(*types.Func)
		if !ok || !pin(f.Origin().FullName()) {
			return nil
		}
	}
	slices.SortFunc(route.Values, func(x, y namedHash) int { return strings.Compare(x.Function, y.Function) })
	return route
}

// forwardsToDispatcher reports whether the function named callee, given an
// Event at argument position index, only ever passes that parameter to the
// upcast dispatcher. The parameter must be its own Event-typed local, never
// captured or addressed, and never read outside a dispatcher argument.
func (a *analyzer) forwardsToDispatcher(callee string, index int) bool {
	target := a.funcs[callee]
	if target == nil || target.decl == nil || target.value {
		return false
	}
	var param types.Object
	position := 0
	for _, field := range target.decl.Type.Params.List {
		names := field.Names
		if len(names) == 0 {
			names = []*ast.Ident{nil}
		}
		for _, name := range names {
			if position == index {
				if name == nil {
					return false
				}
				param = target.pkg.info.ObjectOf(name)
			}
			position++
		}
	}
	if param == nil || !a.isEvent(param.Type()) || a.captured[param] || a.unknown[param] {
		return false
	}
	if target.decl.Body == nil {
		return false
	}
	parents := parentsOf(target.decl)
	for _, use := range usesAfter(target.pkg, target.decl, param, target.decl.Body.Pos()) {
		_, ok := argumentOf(parents, use)
		if !ok || calleeName(target.pkg, callOf(parents, use)) != a.dispatcher.Function {
			return false
		}
	}
	return true
}

// parentsOf maps every node under root to its parent.
func parentsOf(root ast.Node) map[ast.Node]ast.Node {
	parents := map[ast.Node]ast.Node{}
	var stack []ast.Node
	ast.Inspect(root, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		if len(stack) > 0 {
			parents[n] = stack[len(stack)-1]
		}
		stack = append(stack, n)
		return true
	})
	return parents
}

// usesAfter lists the identifiers that read obj after position end in fn.
func usesAfter(p *checkedPackage, fn *ast.FuncDecl, obj types.Object, end token.Pos) []*ast.Ident {
	var uses []*ast.Ident
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok || id.Pos() < end || p.info.Uses[id] != obj {
			return true
		}
		uses = append(uses, id)
		return true
	})
	return uses
}

// callOf returns the innermost call expression enclosing the identifier.
func callOf(parents map[ast.Node]ast.Node, id *ast.Ident) *ast.CallExpr {
	for at := parents[id]; at != nil; at = parents[at] {
		if call, ok := at.(*ast.CallExpr); ok {
			return call
		}
	}
	return nil
}

// argumentOf reports the argument position of the identifier's innermost
// enclosing call, and whether that call exists at all.
func argumentOf(parents map[ast.Node]ast.Node, id *ast.Ident) (int, bool) {
	call := callOf(parents, id)
	if call == nil {
		return 0, false
	}
	for index, argument := range call.Args {
		if argument == ast.Node(id) {
			return index, true
		}
	}
	return 0, false
}

// calleeName returns a call's callee's generic-origin full name.
func calleeName(p *checkedPackage, call *ast.CallExpr) string {
	if call == nil {
		return ""
	}
	if f, ok := p.info.Uses[calleeIdent(call.Fun)].(*types.Func); ok {
		return f.Origin().FullName()
	}
	return ""
}

// inLoop reports whether stmt sits under a loop of fn.
func inLoop(parents map[ast.Node]ast.Node, stmt ast.Stmt, fn *ast.FuncDecl) bool {
	for at := parents[stmt]; at != nil; at = parents[at] {
		switch at.(type) {
		case *ast.ForStmt, *ast.RangeStmt:
			return true
		}
		if at == fn {
			return false
		}
	}
	return false
}

// fieldTarget is the single Event-typed local whose fields an assignment
// sets, when every left side is a field selector on it and the operation is
// a plain assignment.
func (a *analyzer) fieldTarget(p *checkedPackage, assign *ast.AssignStmt) types.Object {
	var target types.Object
	for _, lhs := range assign.Lhs {
		selector, ok := ast.Unparen(lhs).(*ast.SelectorExpr)
		if !ok {
			return nil
		}
		id, ok := ast.Unparen(selector.X).(*ast.Ident)
		if !ok || p.info.Selections[selector] == nil || p.info.Selections[selector].Kind() != types.FieldVal || target != nil && p.info.Uses[id] != target {
			return nil
		}
		target = p.info.Uses[id]
	}
	if target == nil || !a.isEvent(target.Type()) || assign.Tok != token.ASSIGN {
		return nil
	}
	return target
}

// comparisonSite proves a comparison-only route for an assignment in the
// declaration d: d is a plain function, never used as a value, an upcaster,
// or an adapt value, with exactly one static caller; the assignment sets
// fields of d's own Event local and, after it, the local leaves only through
// d's first result; the one caller binds that result to an unaddressed local
// whose every use is an argument of a single bool comparator. The censused
// analyzer proves the same route; the pinned caller and comparator digests
// carry what the proof does not.
func (a *analyzer) comparisonSite(p *checkedPackage, d ast.Decl, assign *ast.AssignStmt) *comparisonRoute {
	decl, ok := d.(*ast.FuncDecl)
	if !ok || decl.Recv != nil {
		return nil
	}
	name := a.siteFunction(p, d)
	fn := a.funcs[name]
	if fn == nil || fn.value || fn.upcaster || len(fn.calls) != 1 {
		return nil
	}
	for id, pkg := range a.adaptValues {
		if f, ok := pkg.info.Uses[id].(*types.Func); ok && f.Origin().FullName() == name {
			return nil
		}
	}
	target := a.fieldTarget(p, assign)
	if target == nil || !a.eventReturnsOnly(p, decl, target, assign) {
		return nil
	}
	results := p.info.Defs[decl.Name].(*types.Func).Signature().Results()
	if results.Len() == 0 || !a.isEvent(results.At(0).Type()) {
		return nil
	}
	site := fn.calls[0]
	if site.caller == nil {
		site.caller = site.encloser
	}
	if site.caller == nil || site.caller.decl == nil {
		return nil
	}
	caller, callerPkg := site.caller.decl, site.caller.pkg
	parents := parentsOf(caller)
	bound, ok := parents[site.call].(*ast.AssignStmt)
	if !ok || len(bound.Rhs) != 1 {
		return nil
	}
	id, ok := bound.Lhs[0].(*ast.Ident)
	result := callerPkg.info.ObjectOf(id)
	if !ok || result == nil || a.unknown[result] || a.captured[result] {
		return nil
	}
	comparator := ""
	for _, use := range usesAfter(callerPkg, caller, result, caller.Body.Pos()) {
		index, ok := argumentOf(parents, use)
		callee := calleeName(callerPkg, callOf(parents, use))
		if !ok || comparator != "" && callee != comparator || !a.compares(callee, index) {
			return nil
		}
		comparator = callee
	}
	if comparator == "" {
		return nil
	}
	callerName, callerDigest := a.declName(callerPkg, caller)
	_, comparatorDigest := a.declName(a.funcs[comparator].pkg, a.funcs[comparator].decl)
	return &comparisonRoute{Caller: callerName, CallerDigest: callerDigest, Comparator: comparator, ComparatorDigest: comparatorDigest}
}

// eventReturnsOnly reports whether, after stmt, obj leaves fn only as fn's
// first result or the base of a field selector that holds no Event.
func (a *analyzer) eventReturnsOnly(p *checkedPackage, fn *ast.FuncDecl, obj types.Object, stmt ast.Stmt) bool {
	parents := parentsOf(fn)
	if inLoop(parents, stmt, fn) || a.captured[obj] || a.unknown[obj] {
		return false
	}
	for _, use := range usesAfter(p, fn, obj, stmt.End()) {
		switch parent := parents[use].(type) {
		case *ast.ReturnStmt:
			if len(parent.Results) == 0 || parent.Results[0] != use {
				return false
			}
		case *ast.SelectorExpr:
			selection := p.info.Selections[parent]
			if selection == nil || selection.Kind() != types.FieldVal || a.holdsEvent(selection.Type(), map[types.Type]bool{}) || a.containsEvent(selection.Type(), map[types.Type]bool{}) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// compares reports whether the function named callee is a bool comparator
// for an Event at argument position index: a plain function returning one
// bool, whose Event parameter is never assigned and whose every use reads a
// field that holds no Event, a comparison operand, or an argument to a pure
// comparator.
func (a *analyzer) compares(callee string, index int) bool {
	target := a.funcs[callee]
	if target == nil || target.decl == nil || target.decl.Recv != nil || target.value {
		return false
	}
	p := target.pkg
	results := p.info.Defs[target.decl.Name].(*types.Func).Signature().Results()
	if results.Len() != 1 || !types.Identical(results.At(0).Type(), types.Typ[types.Bool]) {
		return false
	}
	param := paramAt(p, target.decl, index)
	if param == nil || !a.isEvent(param.Type()) {
		return false
	}
	parents := parentsOf(target.decl)
	written := false
	ast.Inspect(target.decl.Body, func(n ast.Node) bool {
		if assign, ok := n.(*ast.AssignStmt); ok {
			for _, lhs := range assign.Lhs {
				written = written || a.mentions(p, lhs, param)
			}
		}
		return !written
	})
	if written {
		return false
	}
	for _, use := range usesAfter(p, target.decl, param, target.decl.Body.Pos()) {
		switch parent := parents[use].(type) {
		case *ast.SelectorExpr:
			selection := p.info.Selections[parent]
			_, addressed := parents[parent].(*ast.UnaryExpr)
			if selection == nil || selection.Kind() != types.FieldVal || addressed || a.holdsEvent(selection.Type(), map[types.Type]bool{}) || a.containsEvent(selection.Type(), map[types.Type]bool{}) {
				return false
			}
		case *ast.BinaryExpr:
			if parent.Op != token.EQL && parent.Op != token.NEQ {
				return false
			}
		case *ast.CallExpr:
			f, ok := p.info.Uses[calleeIdent(parent.Fun)].(*types.Func)
			if !ok || !pureComparators[f.FullName()] || parent.Fun == use {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// paramAt returns the parameter object at argument position index.
func paramAt(p *checkedPackage, decl *ast.FuncDecl, index int) types.Object {
	position := 0
	for _, field := range decl.Type.Params.List {
		names := field.Names
		if len(names) == 0 {
			names = []*ast.Ident{nil}
		}
		for _, name := range names {
			if position == index {
				if name == nil {
					return nil
				}
				return p.info.ObjectOf(name)
			}
			position++
		}
	}
	return nil
}

// confinedSite reports whether an assignment meets writerSite.Confinement.
func (a *analyzer) confinedSite(p *checkedPackage, d ast.Decl, assign *ast.AssignStmt) bool {
	decl, ok := d.(*ast.FuncDecl)
	if !ok || a.dispatcher == nil {
		return false
	}
	if fn := a.funcs[a.siteFunction(p, d)]; fn == nil || !fn.upcaster || fn.value || len(fn.calls) > 0 || decl.Type.Results.List[0].Names != nil {
		return false
	}
	var param types.Object
	for _, lhs := range assign.Lhs {
		selector, ok := ast.Unparen(lhs).(*ast.SelectorExpr)
		if !ok {
			return false
		}
		id, ok := ast.Unparen(selector.X).(*ast.Ident)
		if !ok || param != nil && p.info.Uses[id] != param {
			return false
		}
		param = p.info.Uses[id]
	}
	if _, ok := a.formals[param]; !ok || a.unknown[param] || a.captured[param] {
		return false
	}
	for _, at := range a.assigns[param] {
		if at.field != "PayloadVersion" {
			return false
		}
	}
	return a.returnsOnly(p, decl, param, assign)
}

// returnsOnly reports whether fn has no label, through which a goto could run
// stmt again, stmt is in no loop of fn, and every read of obj after
// stmt is a returned result, which for an upcaster is its Event, or the base
// of a field selector.
func (a *analyzer) returnsOnly(p *checkedPackage, fn *ast.FuncDecl, obj types.Object, stmt ast.Stmt) bool {
	parents := map[ast.Node]ast.Node{}
	var stack []ast.Node
	labeled := false
	ast.Inspect(fn, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		_, label := n.(*ast.LabeledStmt)
		labeled = labeled || label
		if len(stack) > 0 {
			parents[n] = stack[len(stack)-1]
		}
		stack = append(stack, n)
		return true
	})
	if labeled {
		return false
	}
	for at := parents[stmt]; at != fn; at = parents[at] {
		switch at.(type) {
		case *ast.ForStmt, *ast.RangeStmt:
			return false
		}
	}
	only := true
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok || id.Pos() < stmt.End() || p.info.Uses[id] != obj {
			return only
		}
		switch parent := parents[id].(type) {
		case *ast.ReturnStmt:
		case *ast.SelectorExpr:
			only = p.info.Selections[parent] != nil && p.info.Selections[parent].Kind() == types.FieldVal
		default:
			only = false
		}
		return only
	})
	return only
}

// pairs crosses kinds with versions.
func pairs(kinds, versions []string) []string {
	var out []string
	for _, kind := range kinds {
		for _, version := range versions {
			out = append(out, kind+" v"+version)
		}
	}
	return out
}

// evaluate resolves one site's pairs within the step budget. A site that
// reads its function's parameters resolves once per static call of that
// function, or once unbound when the index does not see every call. A "*"
// pair absorbs the rest. A site that resolves to no pair is open: its values
// arrive only through a cycle cut, as in dead recursion, or a call that never
// returns.
func (a *analyzer) evaluate(function string, resolve func(*env) []string) []string {
	a.steps = 0
	result := resolve(&env{function: function, strict: true})
	if slices.ContainsFunc(result, func(pair string) bool { return strings.Contains(pair, "?") }) {
		result = nil
		if !a.contextual(function) {
			result = resolve(nil)
		} else {
			for _, call := range a.funcs[function].calls {
				result = append(result, resolve(&env{function: function, args: call.call.Args, pkg: call.pkg})...)
			}
		}
	}
	set := map[string]bool{}
	for _, pair := range result {
		set[pair] = true
	}
	if a.steps > resolveBudget || set["* v*"] || len(set) == 0 {
		return anyPair
	}
	return slices.Sorted(maps.Keys(set))
}

// siteFunction names the declared function d is, or "" for any other
// declaration.
func (a *analyzer) siteFunction(p *checkedPackage, d ast.Decl) string {
	if decl, ok := d.(*ast.FuncDecl); ok {
		if f, ok := p.info.Defs[decl.Name].(*types.Func); ok {
			return f.Origin().FullName()
		}
	}
	return ""
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
func (a *analyzer) resolve(p *checkedPackage, expr ast.Expr, sc *scope) []string {
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
		case constant.Bool:
			return []string{strconv.FormatBool(constant.BoolVal(tv.Value))}
		}
		return anyValue
	}
	switch v := expr.(type) {
	case *ast.ParenExpr:
		return a.resolve(p, v.X, sc)
	case *ast.CallExpr:
		// A conversion keeps its value only between identical basic types:
		// string(n) of an integer and a narrowing integer conversion do not.
		_, basic := under(p.info.Types[v.Fun].Type).(*types.Basic)
		if target := p.info.Types[v.Fun]; basic && target.IsType() && len(v.Args) == 1 && types.Identical(target.Type.Underlying(), under(p.info.Types[v.Args[0]].Type)) {
			return a.resolve(p, v.Args[0], sc)
		}
		return a.resolveCall(p, v, 0, "", sc)
	case *ast.IndexExpr:
		if a.isRegistry(p, v.X) {
			return tagValues(a.resolve(p, v.Index, sc), func(kind string) string { return "reg:" + kind })
		}
	case *ast.SelectorExpr:
		if field := a.eventField(p, v); field != "" {
			return a.resolveField(p, v.X, field, sc)
		}
		if v.Sel.Name == "CurrentVersion" && a.isNamed(p.info.Types[v.X].Type, "EventKindRegistration") {
			return tagValues(a.resolve(p, v.X, sc), func(value string) string {
				if kind, ok := strings.CutPrefix(value, "reg:"); ok {
					return "cur:" + kind
				}
				return "*"
			})
		}
	case *ast.BinaryExpr:
		return a.resolveBinary(p, v, sc)
	case *ast.Ident:
		return a.resolveVar(p, v, "", sc)
	}
	return anyValue
}

// resolveBinary resolves a comparison or boolean operator over finite
// operands to the results any pair of operand values gives. Any other
// operator is "*", and so is a comparison through an interface, which also
// compares the dynamic types the values drop.
func (a *analyzer) resolveBinary(p *checkedPackage, v *ast.BinaryExpr, sc *scope) []string {
	if !slices.Contains([]token.Token{token.EQL, token.NEQ, token.LAND, token.LOR}, v.Op) || types.IsInterface(p.info.Types[v.X].Type) || types.IsInterface(p.info.Types[v.Y].Type) {
		return anyValue
	}
	x, y := a.resolve(p, v.X, sc), a.resolve(p, v.Y, sc)
	symbolic := func(value string) bool {
		return value == "*" || strings.HasPrefix(value, "reg:") || strings.HasPrefix(value, "cur:")
	}
	if slices.ContainsFunc(x, symbolic) || slices.ContainsFunc(y, symbolic) {
		return anyValue
	}
	if slices.Contains(x, "?") || slices.Contains(y, "?") {
		return []string{"?"}
	}
	set := map[string]bool{}
	for _, l := range x {
		for _, r := range y {
			switch v.Op {
			case token.EQL:
				set[strconv.FormatBool(l == r)] = true
			case token.NEQ:
				set[strconv.FormatBool(l != r)] = true
			case token.LAND:
				set[strconv.FormatBool(l == "true" && r == "true")] = true
			case token.LOR:
				set[strconv.FormatBool(l == "true" || r == "true")] = true
			}
		}
	}
	return slices.Sorted(maps.Keys(set))
}

// constantly reports whether cond always resolves to want. A condition whose
// resolution cut a cycle may have missed values and is never constant.
func (a *analyzer) constantly(p *checkedPackage, cond ast.Expr, want bool, sc *scope) bool {
	cuts := a.cuts
	values := a.resolve(p, cond, sc)
	return a.cuts == cuts && slices.Equal(values, []string{strconv.FormatBool(want)})
}

// resolveVar resolves a declared local or a parameter, or with a field that
// Event field of a local or parameter Event, as the union of every value
// assigned to it and, for a parameter, the value it is called with. A field of any
// other holder, such as one promoted from an embedded Event, is "*".
func (a *analyzer) resolveVar(p *checkedPackage, id *ast.Ident, field string, sc *scope) []string {
	obj, ok := p.info.Uses[id].(*types.Var)
	param, isParam := a.formals[obj]
	if !ok || !a.declared[obj] && !isParam || a.unknown[obj] || field != "" && !a.isEvent(obj.Type()) {
		return anyValue
	}
	key := seenKey{obj, field, sc.env}
	if sc.seen[key] {
		a.cuts++
		return nil
	}
	sc.seen[key] = true
	defer delete(sc.seen, key)
	var values [][]string
	if isParam {
		values = append(values, a.resolveParam(param, field, sc))
	}
	for _, assign := range a.assigns[obj] {
		if (assign.field == "" || assign.field == field) && a.reaches(assign, obj, field, sc) {
			values = append(values, a.resolveAssigned(assign, field, sc))
		}
	}
	return union(values...)
}

// zeroField is the zero value of an Event field.
func zeroField(field string) []string {
	return []string{map[string]string{"Kind": "", "PayloadVersion": "0"}[field]}
}

func (a *analyzer) resolveAssigned(assign assignment, field string, sc *scope) []string {
	if assign.result >= 0 {
		// A field target takes the result itself.
		of := field
		if assign.field != "" {
			of = ""
		}
		switch rhs := ast.Unparen(assign.rhs).(type) {
		case *ast.CallExpr:
			return a.resolveCall(assign.pkg, rhs, assign.result, of, sc)
		case *ast.IndexExpr:
			if of != "" || !a.isRegistry(assign.pkg, rhs.X) {
				return anyValue
			}
			if assign.result == 0 {
				return a.resolve(assign.pkg, rhs, sc)
			}
			return a.registeredAll(a.resolve(assign.pkg, rhs.Index, sc))
		}
		return anyValue
	}
	switch {
	case field != "" && assign.field == field:
		return a.resolve(assign.pkg, assign.rhs, sc)
	case assign.rhs == nil && field != "":
		return zeroField(field)
	case field == "":
		return a.resolve(assign.pkg, assign.rhs, sc)
	}
	return a.resolveField(assign.pkg, assign.rhs, field, sc)
}

// resolveField resolves an Event expression's Kind or PayloadVersion.
func (a *analyzer) resolveField(p *checkedPackage, expr ast.Expr, field string, sc *scope) []string {
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
				return a.resolve(p, pair.Value, sc)
			}
		}
		return zeroField(field)
	case *ast.CallExpr:
		return a.resolveCall(p, v, 0, field, sc)
	case *ast.Ident:
		return a.resolveVar(p, v, field, sc)
	}
	return anyValue
}

// resolveParam resolves a parameter through the innermost binding of its
// function, or else through every static call of it.
func (a *analyzer) resolveParam(param formal, field string, sc *scope) []string {
	for e := sc.env; e != nil; e = e.caller {
		switch {
		case e.function != param.function:
		case e.strict:
			return []string{"?"}
		case param.index >= len(e.args):
			return anyValue
		default:
			return a.resolveExpr(e.pkg, e.args[param.index], field, &scope{seen: sc.seen, env: e.caller})
		}
	}
	if !a.contextual(param.function) {
		return anyValue
	}
	var values [][]string
	for _, call := range a.funcs[param.function].calls {
		if param.index >= len(call.call.Args) {
			return anyValue
		}
		values = append(values, a.resolveExpr(call.pkg, call.call.Args[param.index], field, &scope{seen: sc.seen}))
	}
	return union(values...)
}

func (a *analyzer) resolveExpr(p *checkedPackage, expr ast.Expr, field string, sc *scope) []string {
	if field == "" {
		return a.resolve(p, expr, sc)
	}
	return a.resolveField(p, expr, field, sc)
}

// callDepth bounds the call bindings one resolution nests.
const callDepth = 8

// resolveCall resolves one result of a static call, or with a field that
// Event field of it, through the callee's returns, with the callee's
// parameters bound to this call's arguments.
func (a *analyzer) resolveCall(p *checkedPackage, call *ast.CallExpr, result int, field string, sc *scope) []string {
	f, ok := p.info.Uses[calleeIdent(call.Fun)].(*types.Func)
	sel, isSel := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok || isSel && p.info.Selections[sel] != nil && p.info.Selections[sel].Kind() == types.MethodExpr {
		return anyValue
	}
	name := f.Origin().FullName()
	fn := a.funcs[name]
	if fn == nil || fn.decl == nil || fn.decl.Body == nil || fn.deferred {
		return anyValue
	}
	depth := 0
	for e := sc.env; e != nil; e = e.caller {
		depth++
	}
	if depth > callDepth {
		return anyValue
	}
	key := seenKey{call, field + "#" + strconv.Itoa(result), sc.env}
	if sc.seen[key] {
		a.cuts++
		return nil
	}
	sc.seen[key] = true
	defer delete(sc.seen, key)
	inner := &scope{seen: sc.seen, env: &env{function: name, args: call.Args, pkg: p, caller: sc.env}}
	var values [][]string
	for _, ret := range fn.returns {
		if !a.armsRun(ret.at, inner) {
			continue
		}
		// A bare return yields named results. return g() spreads g's
		// results.
		switch {
		case len(ret.results) == 0:
			return anyValue
		case len(ret.results) > result:
			values = append(values, a.resolveExpr(fn.pkg, ret.results[result], field, inner))
		default:
			values = append(values, a.resolveCall(fn.pkg, ast.Unparen(ret.results[0]).(*ast.CallExpr), result, field, inner))
		}
	}
	return union(values...)
}

// reaches reports whether an assignment can supply a read of obj, or with a
// field of that Event field: no enclosing arm is constantly skipped, and no
// later statement of its list overwrites it first.
func (a *analyzer) reaches(assign assignment, obj types.Object, field string, sc *scope) bool {
	if !a.armsRun(assign, sc) {
		return false
	}
	return assign.pos < 0 || a.captured[obj] || !a.overwrites(assign.pkg, assign.list[assign.pos+1:], obj, field, sc)
}

// armsRun reports whether no if arm around a statement is constantly skipped.
func (a *analyzer) armsRun(at assignment, sc *scope) bool {
	for _, b := range at.path {
		if a.constantly(at.pkg, b.stmt.Cond, !b.then, sc) {
			return false
		}
	}
	return true
}

// overwrites reports whether stmts assign obj (or obj.field) before anything
// reads it, on the only path: the scan stops at a statement that holds a
// branch statement, or that mentions obj other than to set another field.
func (a *analyzer) overwrites(p *checkedPackage, stmts []ast.Stmt, obj types.Object, field string, sc *scope) bool {
	for _, stmt := range stmts {
		switch {
		case jumps(stmt):
			return false
		case a.assignsWhole(p, stmt, obj, field):
			return true
		}
		if s, ok := stmt.(*ast.IfStmt); ok && !a.mentions(p, s.Init, obj) && !a.mentions(p, s.Cond, obj) && a.constantly(p, s.Cond, true, sc) {
			return a.overwrites(p, s.Body.List, obj, field, sc)
		}
		if field != "" && a.assignsOtherField(p, stmt, obj, field) {
			continue
		}
		if a.mentions(p, stmt, obj) {
			return false
		}
	}
	return false
}

// jumps reports whether stmt holds a branch statement outside a closure. A
// label only lets other paths in.
func jumps(stmt ast.Stmt) bool {
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.BranchStmt:
			found = true
		case *ast.FuncLit:
			return false
		}
		return !found
	})
	return found
}

// assignsWhole reports whether stmt assigns obj, or with a field obj.field,
// reading obj nowhere else.
func (a *analyzer) assignsWhole(p *checkedPackage, stmt ast.Stmt, obj types.Object, field string) bool {
	assign, ok := stmt.(*ast.AssignStmt)
	if !ok || assign.Tok != token.ASSIGN || slices.ContainsFunc(assign.Rhs, func(rhs ast.Expr) bool { return a.mentions(p, rhs, obj) }) {
		return false
	}
	found := false
	for _, lhs := range assign.Lhs {
		target := ast.Unparen(lhs)
		if selector, ok := target.(*ast.SelectorExpr); ok && field != "" && a.eventField(p, selector) == field {
			target = ast.Unparen(selector.X)
		}
		if id, ok := target.(*ast.Ident); ok && p.info.Uses[id] == obj {
			found = true
		} else if a.mentions(p, lhs, obj) {
			return false
		}
	}
	return found
}

// assignsOtherField reports whether stmt only sets a field of obj other than
// field, reading obj nowhere else.
func (a *analyzer) assignsOtherField(p *checkedPackage, stmt ast.Stmt, obj types.Object, field string) bool {
	assign, ok := stmt.(*ast.AssignStmt)
	if !ok || assign.Tok != token.ASSIGN || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 || a.mentions(p, assign.Rhs[0], obj) {
		return false
	}
	selector, ok := ast.Unparen(assign.Lhs[0]).(*ast.SelectorExpr)
	if !ok || selector.Sel.Name == field {
		return false
	}
	id, ok := ast.Unparen(selector.X).(*ast.Ident)
	return ok && p.info.Uses[id] == obj
}

func (a *analyzer) mentions(p *checkedPackage, node ast.Node, obj types.Object) bool {
	found := false
	if node != nil {
		ast.Inspect(node, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && (p.info.Uses[id] == obj || p.info.Defs[id] == obj) {
				found = true
			}
			return !found
		})
	}
	return found
}

// readRegistry returns the store's event kind registry, its literal's
// constant keys, and the literal when it is a package-level map literal of
// EventKindRegistration values that the binary only reads by index, range, or
// len; otherwise nil.
func readRegistry(store *checkedPackage) (types.Object, map[string]bool, *ast.CompositeLit) {
	registry, ok := store.types.Scope().Lookup("eventKindRegistry").(*types.Var)
	if !ok {
		return nil, nil, nil
	}
	if m, ok := registry.Type().Underlying().(*types.Map); !ok || !types.Identical(m.Key(), types.Typ[types.String]) {
		return nil, nil, nil
	} else if named, ok := types.Unalias(m.Elem()).(*types.Named); !ok || named.Obj().Name() != "EventKindRegistration" || named.Obj().Pkg() != store.types {
		return nil, nil, nil
	}
	var literal *ast.CompositeLit
	for _, file := range store.files {
		var stack []ast.Node
		ast.Inspect(file, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			defer func() { stack = append(stack, n) }()
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			if store.info.Defs[id] == registry {
				if spec, ok := stack[len(stack)-1].(*ast.ValueSpec); ok && len(spec.Names) == 1 && len(spec.Values) == 1 {
					literal, _ = ast.Unparen(spec.Values[0]).(*ast.CompositeLit)
				}
			}
			if store.info.Uses[id] == registry && !registryRead(store, id, stack) {
				registry = nil
			}
			return true
		})
	}
	if literal == nil || registry == nil {
		return nil, nil, nil
	}
	keys := map[string]bool{}
	for _, element := range literal.Elts {
		if pair, ok := element.(*ast.KeyValueExpr); ok {
			if key := store.info.Types[pair.Key].Value; key != nil && key.Kind() == constant.String {
				keys[constant.StringVal(key)] = true
			}
		}
	}
	return registry, keys, literal
}

// workItemFolds lists the registered kinds whose fold is not shown to refuse
// a work_item subject, or "*" when the registry is not a fact, and the digest
// of checkSubject, on which a refusal relies. A fold refuses when it is the
// registration's fold and begins
// `if err := checkSubject(event, C); err != nil { return err }`, with event its
// Event parameter and C a constant other than SubjectWorkItem.
func (a *analyzer) workItemFolds(store *checkedPackage, literal *ast.CompositeLit) ([]string, string) {
	check := ""
	if fn := a.funcs[a.store+".checkSubject"]; fn != nil && fn.decl != nil {
		_, check = a.declName(store, fn.decl)
	}
	workItem, _ := store.types.Scope().Lookup("SubjectWorkItem").(*types.Const)
	if literal == nil || check == "" || workItem == nil {
		return []string{"*"}, check
	}
	var open []string
	for _, element := range literal.Elts {
		pair, ok := element.(*ast.KeyValueExpr)
		if !ok || store.info.Types[pair.Key].Value == nil || store.info.Types[pair.Key].Value.Kind() != constant.String {
			return []string{"*"}, check
		}
		if fold := a.foldOf(store, pair.Value, nil, 0); fold == nil || !a.refusesWorkItem(fold, workItem) {
			open = append(open, constant.StringVal(store.info.Types[pair.Key].Value))
		}
	}
	sort.Strings(open)
	return open, check
}

// foldOf names the declared function a registration expression sets as its
// Fold: a literal's Fold field, or the fold every return of a static call
// sets, with the callee's unassigned parameters bound to the call's
// arguments. It is nil when unknown.
func (a *analyzer) foldOf(p *checkedPackage, expr ast.Expr, e *env, depth int) *types.Func {
	switch v := ast.Unparen(expr).(type) {
	case *ast.Ident:
		if f, ok := p.info.Uses[v].(*types.Func); ok {
			return f
		}
		obj, _ := p.info.Uses[v].(*types.Var)
		param, ok := a.formals[obj]
		if !ok || e == nil || e.function != param.function || param.index >= len(e.args) || a.unknown[obj] || len(a.assigns[obj]) > 0 {
			return nil
		}
		return a.foldOf(e.pkg, e.args[param.index], e.caller, depth)
	case *ast.CompositeLit:
		if !a.isNamed(p.info.Types[v].Type, "EventKindRegistration") {
			return nil
		}
		for _, element := range v.Elts {
			if pair, ok := element.(*ast.KeyValueExpr); ok && pair.Key.(*ast.Ident).Name == "Fold" {
				return a.foldOf(p, pair.Value, e, depth)
			}
		}
	case *ast.CallExpr:
		f, ok := p.info.Uses[calleeIdent(v.Fun)].(*types.Func)
		if !ok || f.Signature().Recv() != nil || depth > callDepth || v.Ellipsis.IsValid() {
			return nil
		}
		name := f.Origin().FullName()
		fn := a.funcs[name]
		if fn == nil || fn.decl == nil || fn.decl.Body == nil || fn.deferred || len(fn.returns) == 0 {
			return nil
		}
		inner := &env{function: name, args: v.Args, pkg: p, caller: e}
		var fold *types.Func
		for _, ret := range fn.returns {
			if len(ret.results) != 1 {
				return nil
			}
			got := a.foldOf(fn.pkg, ret.results[0], inner, depth+1)
			if got == nil || fold != nil && got != fold {
				return nil
			}
			fold = got
		}
		return fold
	}
	return nil
}

// refusesWorkItem reports whether fold begins by refusing any subject but a
// constant other than SubjectWorkItem through checkSubject.
func (a *analyzer) refusesWorkItem(fold *types.Func, workItem *types.Const) bool {
	fn := a.funcs[fold.Origin().FullName()]
	sig := fold.Origin().Signature()
	if fn == nil || fn.decl == nil || fn.decl.Body == nil || len(fn.decl.Body.List) == 0 || sig.Params().Len() == 0 || !a.isEvent(sig.Params().At(sig.Params().Len()-1).Type()) {
		return false
	}
	p := fn.pkg
	stmt, ok := fn.decl.Body.List[0].(*ast.IfStmt)
	if !ok || stmt.Else != nil || types.ExprString(stmt.Cond) != "err != nil" || len(stmt.Body.List) != 1 {
		return false
	}
	if ret, ok := stmt.Body.List[0].(*ast.ReturnStmt); !ok || len(ret.Results) != 1 || types.ExprString(ret.Results[0]) != "err" {
		return false
	}
	init, ok := stmt.Init.(*ast.AssignStmt)
	if !ok || init.Tok != token.DEFINE || len(init.Lhs) != 1 || len(init.Rhs) != 1 || types.ExprString(init.Lhs[0]) != "err" {
		return false
	}
	call, ok := init.Rhs[0].(*ast.CallExpr)
	if !ok || len(call.Args) != 2 {
		return false
	}
	callee, ok := p.info.Uses[calleeIdent(call.Fun)].(*types.Func)
	event, isIdent := call.Args[0].(*ast.Ident)
	if !ok || callee.FullName() != a.store+".checkSubject" || !isIdent || p.info.Uses[event] != sig.Params().At(sig.Params().Len()-1) {
		return false
	}
	subject := p.info.Types[call.Args[1]].Value
	return subject != nil && !constant.Compare(subject, token.EQL, workItem.Val())
}

// registeredAll resolves a registry read's ok: "true" when every kind is a
// key of the registry literal, else "*".
func (a *analyzer) registeredAll(kinds []string) []string {
	for _, kind := range kinds {
		if !a.registered[kind] {
			return anyValue
		}
	}
	return []string{"true"}
}

// registryRead reports whether a use of the registry only reads it.
func registryRead(p *checkedPackage, id *ast.Ident, stack []ast.Node) bool {
	parents := outsideParens(stack)
	if len(parents) == 0 {
		return false
	}
	switch parent := parents[len(parents)-1].(type) {
	case *ast.IndexExpr:
		if ast.Unparen(parent.X) != id {
			return false
		}
		if outer := outsideParens(parents[:len(parents)-1]); len(outer) > 0 {
			switch target := outer[len(outer)-1].(type) {
			case *ast.AssignStmt:
				return !slices.ContainsFunc(target.Lhs, func(lhs ast.Expr) bool { return ast.Unparen(lhs) == parent })
			case *ast.RangeStmt:
				return ast.Unparen(target.Key) != parent && ast.Unparen(target.Value) != parent
			}
		}
		return true
	case *ast.RangeStmt:
		return ast.Unparen(parent.X) == id
	case *ast.CallExpr:
		builtin, ok := p.info.Uses[calleeIdent(parent.Fun)].(*types.Builtin)
		return ok && builtin.Name() == "len" && len(parent.Args) == 1
	}
	return false
}

// outsideParens drops the parentheses that close a stack of ancestors.
func outsideParens(stack []ast.Node) []ast.Node {
	for len(stack) > 0 {
		if _, ok := stack[len(stack)-1].(*ast.ParenExpr); !ok {
			break
		}
		stack = stack[:len(stack)-1]
	}
	return stack
}

func (a *analyzer) isRegistry(p *checkedPackage, expr ast.Expr) bool {
	id, ok := ast.Unparen(expr).(*ast.Ident)
	return ok && a.registry != nil && p.info.Uses[id] == a.registry
}

// tagValues maps each value through f, keeping "*" and "?".
func tagValues(values []string, f func(string) string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "*" && value != "?" {
			value = f(value)
		}
		out = append(out, value)
	}
	return union(out)
}
