package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type projection struct {
	SchemaVersion string                     `json:"schema_version"`
	Packages      map[string]map[string]bool `json:"packages"`
}

type sourceFile struct {
	packagePath string
	isTestFile  bool
	file        *ast.File
}

var failureMethods = map[string]bool{
	"Error":   true,
	"Errorf":  true,
	"Fail":    true,
	"FailNow": true,
	"Fatal":   true,
	"Fatalf":  true,
}

func main() {
	flag.Parse()
	root := "."
	if flag.NArg() > 1 {
		fmt.Fprintln(os.Stderr, "usage: go run scripts/go-test-assertions/main.go [module-root]")
		os.Exit(2)
	}
	if flag.NArg() == 1 {
		root = flag.Arg(0)
	}
	root, err := filepath.Abs(root)
	if err != nil {
		fail(err)
	}

	files, err := parseModule(root)
	if err != nil {
		fail(err)
	}
	facts := project(files)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(projection{SchemaVersion: "1.0", Packages: facts}); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func parseModule(root string) ([]sourceFile, error) {
	fset := token.NewFileSet()
	var files []sourceFile
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			if path != root && (info.Name() == ".git" || info.Name() == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		relative, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		files = append(files, sourceFile{
			packagePath: filepath.ToSlash(relative),
			isTestFile:  strings.HasSuffix(info.Name(), "_test.go"),
			file:        file,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].packagePath < files[j].packagePath
	})
	return files, nil
}

func project(files []sourceFile) map[string]map[string]bool {
	helpers := map[string]map[string]*ast.FuncDecl{}
	packages := map[string]map[string]bool{}
	for _, source := range files {
		if helpers[source.packagePath] == nil {
			helpers[source.packagePath] = map[string]*ast.FuncDecl{}
		}
		for _, declaration := range source.file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && function.Recv == nil {
				helpers[source.packagePath][function.Name.Name] = function
			}
		}
	}
	for _, source := range files {
		if !source.isTestFile {
			continue
		}
		for _, declaration := range source.file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv != nil || function.Body == nil ||
				!strings.HasPrefix(function.Name.Name, "Test") || len(testingParameters(function.Type)) == 0 {
				continue
			}
			if packages[source.packagePath] == nil {
				packages[source.packagePath] = map[string]bool{}
			}
			packages[source.packagePath][function.Name.Name] = bodyCanFail(function.Body, testingParameters(function.Type), helpers[source.packagePath])
		}
	}
	return packages
}

func testingParameters(functionType *ast.FuncType) map[string]bool {
	names := map[string]bool{}
	if functionType == nil || functionType.Params == nil {
		return names
	}
	for _, field := range functionType.Params.List {
		if !isTestingType(field.Type) {
			continue
		}
		for _, name := range field.Names {
			names[name.Name] = true
		}
	}
	return names
}

func isTestingType(expression ast.Expr) bool {
	if pointer, ok := expression.(*ast.StarExpr); ok {
		expression = pointer.X
	}
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	packageName, ok := selector.X.(*ast.Ident)
	return ok && packageName.Name == "testing" && (selector.Sel.Name == "T" || selector.Sel.Name == "TB")
}

func bodyCanFail(body *ast.BlockStmt, testingNames map[string]bool, helpers map[string]*ast.FuncDecl) bool {
	return bodyCanFailWithHelpers(body, testingNames, helpers, true)
}

func bodyCanFailWithHelpers(body *ast.BlockStmt, testingNames map[string]bool, helpers map[string]*ast.FuncDecl, resolveHelpers bool) bool {
	canFail := false
	ast.Inspect(body, func(node ast.Node) bool {
		if canFail {
			return false
		}
		switch value := node.(type) {
		case *ast.FuncLit:
			for name := range testingParameters(value.Type) {
				testingNames[name] = true
			}
		case *ast.CallExpr:
			if selector, ok := value.Fun.(*ast.SelectorExpr); ok {
				receiver, receiverOK := selector.X.(*ast.Ident)
				if receiverOK && testingNames[receiver.Name] && failureMethods[selector.Sel.Name] {
					canFail = true
					return false
				}
			}
			if resolveHelpers {
				callee, ok := value.Fun.(*ast.Ident)
				if ok && helpers[callee.Name] != nil {
					helper := helpers[callee.Name]
					if helper.Body != nil && bodyCanFailWithHelpers(helper.Body, testingParameters(helper.Type), helpers, false) {
						canFail = true
						return false
					}
				}
			}
		}
		return true
	})
	return canFail
}
