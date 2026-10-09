// Command domain-navigation-cli extracts operator verbs from the Go AST.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

type verb struct {
	Canonical string `json:"canonical"`
	TwoWord   string `json:"two_word"`
}

type commandCatalog struct {
	Verbs         []verb   `json:"verbs"`
	EarlyDispatch []string `json:"early_dispatch"`
}

func extract(path string) ([]verb, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return nil, err
	}
	var result []verb
	seen := make(map[string]bool)
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			value := spec.(*ast.ValueSpec)
			if len(value.Names) != 1 || value.Names[0].Name != "commandSpecs" {
				continue
			}
			if len(value.Values) != 1 {
				return nil, fmt.Errorf("commandSpecs must have one literal initializer")
			}
			list, ok := value.Values[0].(*ast.CompositeLit)
			if !ok {
				return nil, fmt.Errorf("commandSpecs must be a composite literal")
			}
			for _, element := range list.Elts {
				entry, ok := element.(*ast.CompositeLit)
				if !ok {
					return nil, fmt.Errorf("commandSpecs entry must be a composite literal")
				}
				var command verb
				for _, field := range entry.Elts {
					pair, ok := field.(*ast.KeyValueExpr)
					if !ok {
						return nil, fmt.Errorf("commandSpecs fields must be keyed")
					}
					key, ok := pair.Key.(*ast.Ident)
					if !ok {
						return nil, fmt.Errorf("commandSpecs field key must be an identifier")
					}
					if key.Name != "Canonical" && key.Name != "TwoWord" {
						continue
					}
					literal, ok := pair.Value.(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						return nil, fmt.Errorf("commandSpecs %s must be a string literal", key.Name)
					}
					text, err := strconv.Unquote(literal.Value)
					if err != nil {
						return nil, err
					}
					if key.Name == "Canonical" {
						command.Canonical = text
					} else {
						command.TwoWord = text
					}
				}
				if command.Canonical == "" || seen[command.Canonical] {
					return nil, fmt.Errorf("empty or duplicate canonical verb: %q", command.Canonical)
				}
				seen[command.Canonical] = true
				result = append(result, command)
			}
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("commandSpecs is missing or empty")
	}
	return result, nil
}

// conjuncts flattens the && chain of an expression into its operands.
func conjuncts(expression ast.Expr) []ast.Expr {
	binary, ok := expression.(*ast.BinaryExpr)
	if !ok || binary.Op != token.LAND {
		return []ast.Expr{expression}
	}
	return append(conjuncts(binary.X), conjuncts(binary.Y)...)
}

// isLenArgs reports whether the expression is the call len(args).
func isLenArgs(expression ast.Expr) bool {
	call, ok := expression.(*ast.CallExpr)
	if !ok || call.Fun == nil || len(call.Args) != 1 {
		return false
	}
	identifier, ok := call.Fun.(*ast.Ident)
	if !ok || identifier.Name != "len" {
		return false
	}
	argument, ok := call.Args[0].(*ast.Ident)
	return ok && argument.Name == "args"
}

// isArgsIndex reports whether the expression is args[<index>] with a constant
// index, returning the index.
func isArgsIndex(expression ast.Expr) (int, bool) {
	index, ok := expression.(*ast.IndexExpr)
	if !ok {
		return 0, false
	}
	identifier, ok := index.X.(*ast.Ident)
	if !ok || identifier.Name != "args" {
		return 0, false
	}
	literal, ok := index.Index.(*ast.BasicLit)
	if !ok || literal.Kind != token.INT {
		return 0, false
	}
	value, err := strconv.Atoi(literal.Value)
	if err != nil {
		return 0, false
	}
	return value, true
}

// argsZeroComparison recognizes `args[0] == "<literal>"` in either operand
// order and returns the literal.
func argsZeroComparison(binary *ast.BinaryExpr) (string, bool) {
	if binary.Op != token.EQL {
		return "", false
	}
	literal, ok := binary.Y.(*ast.BasicLit)
	if ok && literal.Kind == token.STRING {
		if position, valid := isArgsIndex(binary.X); valid && position == 0 {
			text, err := strconv.Unquote(literal.Value)
			return text, err == nil
		}
	}
	literal, ok = binary.X.(*ast.BasicLit)
	if ok && literal.Kind == token.STRING {
		if position, valid := isArgsIndex(binary.Y); valid && position == 0 {
			text, err := strconv.Unquote(literal.Value)
			return text, err == nil
		}
	}
	return "", false
}

// extractEarlyDispatch enumerates the commands the source routes on args[0]
// before its commandSpecs matching: every if whose condition conjoins
// len(args) > 0 with args[0] == "<literal>". Exact-length flag checks such as
// len(args) == 1 && args[0] == "--version", unguarded comparisons, and switch
// cases do not match, so flags and sub-command routing stay out of the
// catalog. The result is sorted; a token routed twice is an error.
func extractEarlyDispatch(path string) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return nil, err
	}
	var result []string
	ast.Inspect(file, func(node ast.Node) bool {
		statement, ok := node.(*ast.IfStmt)
		if !ok {
			return true
		}
		guarded := false
		var names []string
		for _, conjunct := range conjuncts(statement.Cond) {
			binary, ok := conjunct.(*ast.BinaryExpr)
			if !ok {
				continue
			}
			switch binary.Op {
			case token.GTR:
				literal, ok := binary.Y.(*ast.BasicLit)
				if ok && literal.Kind == token.INT && literal.Value == "0" && isLenArgs(binary.X) {
					guarded = true
				}
			case token.EQL:
				if name, ok := argsZeroComparison(binary); ok {
					names = append(names, name)
				}
			}
		}
		if guarded {
			result = append(result, names...)
		}
		return true
	})
	sort.Strings(result)
	seen := make(map[string]bool, len(result))
	for _, name := range result {
		if name == "" || seen[name] {
			return nil, fmt.Errorf("empty or duplicate early dispatch command: %q", name)
		}
		seen[name] = true
	}
	return result, nil
}

type fileImports struct {
	Package string   `json:"package"`
	Imports []string `json:"imports"`
}

// extractImports parses the selected repository files with ImportsOnly and
// maps each repository-relative path to its package and import paths. It is a
// package-level observation only: same-package uses need no import, so
// intra-package edges are unmeasurable here, no symbol graph is built, and the
// result grants no execution permission. The result is deterministic.
func extractImports(root string, paths []string) (result map[string]fileImports, err error) {
	repository, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, repository.Close()) }()
	result = make(map[string]fileImports)
	for _, path := range paths {
		source, err := repository.ReadFile(path)
		if err != nil {
			return nil, err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, source, parser.ImportsOnly)
		if err != nil {
			return nil, err
		}
		imports := make([]string, 0, len(file.Imports))
		for _, imported := range file.Imports {
			text, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return nil, err
			}
			imports = append(imports, text)
		}
		sort.Strings(imports)
		result[filepath.ToSlash(path)] = fileImports{Package: file.Name.Name, Imports: imports}
	}
	return result, nil
}

func main() {
	if len(os.Args) >= 3 && os.Args[1] == "--imports" {
		imports, err := extractImports(os.Args[2], os.Args[3:])
		if err == nil {
			err = json.NewEncoder(os.Stdout).Encode(imports)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: domain-navigation-cli <Go source> | domain-navigation-cli --imports <root> [repository-relative Go files...]")
		os.Exit(2)
	}
	verbs, err := extract(os.Args[1])
	var early []string
	if err == nil {
		early, err = extractEarlyDispatch(os.Args[1])
	}
	if err == nil {
		err = json.NewEncoder(os.Stdout).Encode(commandCatalog{Verbs: verbs, EarlyDispatch: early})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
