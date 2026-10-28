package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestDeterministicCIWaitOwner(t *testing.T) {
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, ".concord", "docs", "knowledge", "package-domains.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ownership struct {
		Packages []struct {
			Path          string   `json:"path"`
			Role          string   `json:"role"`
			OwnerDomainID string   `json:"owner_domain_id"`
			DomainIDs     []string `json:"domain_ids"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(raw, &ownership); err != nil {
		t.Fatal(err)
	}
	owners := 0
	for _, entry := range ownership.Packages {
		if entry.Path != "internal/ciwait" {
			continue
		}
		owners++
		if entry.Role != "domain" || entry.OwnerDomainID != "agent-surface" || len(entry.DomainIDs) != 0 {
			t.Fatalf("CI-wait engine lacks exclusive agent-surface ownership: %+v", entry)
		}
	}
	if owners != 1 {
		t.Fatalf("want one CI-wait engine owner, got %d", owners)
	}
	files, err := filepath.Glob("ci_wait*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range file.Decls {
			switch declaration := declaration.(type) {
			case *ast.FuncDecl:
				if declaration.Name.Name != "runCiWait" {
					t.Errorf("CI-wait mechanism remains in CLI: %s:%s", path, declaration.Name.Name)
				}
			case *ast.GenDecl:
				if declaration.Tok != token.IMPORT {
					t.Errorf("CI-wait model or policy remains in CLI: %s", path)
				}
			}
		}
	}
	files, err = filepath.Glob(filepath.Join(root, "internal", "ciwait", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("CI-wait engine has no physical package")
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		if file.Name.Name != "ciwait" {
			t.Errorf("CI-wait engine has a different package: %s", path)
		}
		for _, dependency := range file.Imports {
			name, err := strconv.Unquote(dependency.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(strings.Split(name, "/")[0], ".") {
				t.Errorf("CI-wait engine gained a non-stdlib dependency: %s imports %s", path, name)
			}
		}
	}
}
