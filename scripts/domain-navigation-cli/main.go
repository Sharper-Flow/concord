// Command domain-navigation-cli extracts operator verbs from the Go AST.
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
)

type verb struct {
	Canonical string `json:"canonical"`
	TwoWord   string `json:"two_word"`
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

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: domain-navigation-cli <Go source>")
		os.Exit(2)
	}
	verbs, err := extract(os.Args[1])
	if err == nil {
		err = json.NewEncoder(os.Stdout).Encode(verbs)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
