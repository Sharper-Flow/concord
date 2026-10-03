package agent

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CD-0038 amendment (2026-10-02): the agent-facing budget object is retired.
// Time is bounded only by requested_budget_seconds and size only by
// limit/page. These tests pin the retirement on both sides of the boundary
// that disagreed in production: the admission validator refused nothing and
// the strict input decoder refused the field the published schema still
// taught, so a caller repaired one refusal into the next.

func TestAdmissionRefusesTheRetiredBudgetObject(t *testing.T) {
	t.Parallel()
	payloads := []string{
		`{"budget":{"max_bytes":65536,"max_items":1}}`,
		`{"max_bytes":65536}`,
		`{"max_items":1}`,
		`{"max_millis":30000}`,
	}
	for _, op := range ContractOperations {
		for _, payload := range payloads {
			err := ValidateOperationPayload(op.Tool, op.Operation, []byte(payload), false)
			if err == nil {
				t.Errorf("%s admitted the retired budget field in %s", op.ID, payload)
				continue
			}
			if !strings.Contains(err.Error(), "unknown payload field") {
				t.Errorf("%s refusal does not name the unknown field: %v", op.ID, err)
			}
		}
	}
}

func TestDecoderRetiresTheBudgetObjectStructurally(t *testing.T) {
	t.Parallel()
	// The decoder half of the agreement is the strict struct decode every
	// operation input passes through. The retirement is structural: no input
	// struct in the package carries the budget object or its fields, so no
	// decoder can accept what admission refuses or refuse what it admits.
	root := "."
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	scanned := 0
	fset := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(root, entry.Name()), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		scanned++
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				structType, ok := spec.(*ast.TypeSpec).Type.(*ast.StructType)
				if !ok {
					continue
				}
				for _, field := range structType.Fields.List {
					name := ""
					if len(field.Names) > 0 {
						name = field.Names[0].Name
					}
					tag := ""
					if field.Tag != nil && field.Tag.Value != "" {
						unquoted, err := strconvUnquote(field.Tag.Value)
						if err == nil {
							tag = unquoted
						}
					}
					// runtime.Budget is the parsed seconds budget the refusal
					// mints from; the retirement bans the wire-facing object,
					// so only tagged fields count here.
					if strings.Contains(tag, `json:"budget"`) {
						t.Errorf("%s:%d: struct field %s decodes the retired budget object", entry.Name(), fset.Position(field.Pos()).Line, name)
					}
					for _, retired := range []string{"MaxBytes", "MaxItems", "MaxMillis"} {
						if name == retired {
							t.Errorf("%s:%d: struct field %s carries a retired result-size budget field", entry.Name(), fset.Position(field.Pos()).Line, name)
						}
					}
				}
			}
		}
	}
	if scanned < 5 {
		t.Fatalf("structural scan reached %d files; the package holds more", scanned)
	}
}

func strconvUnquote(quoted string) (string, error) {
	var value string
	if err := json.Unmarshal([]byte(quoted), &value); err != nil {
		return "", err
	}
	return value, nil
}
