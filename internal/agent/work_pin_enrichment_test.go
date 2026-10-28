package agent

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// CD-0113 D2: a mutation result's pin is the post-state of its own
// transaction, so it cannot disagree with the committed changed ref. The
// enrichment refuses a pin whose version differs from the ref it pins, in
// either direction, instead of stamping a pin that contradicts the result.
func TestMutationEnrichmentRefusesAPinThatDisagreesWithItsChangedRef(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, workID, pin := enrichmentPinFixture(t)
	enrich := func(version int64) (json.RawMessage, error) {
		changed := []agentcontract.ChangedRef{{EntityKind: "work_item", ID: workID, Version: strconv.FormatInt(version, 10)}}
		var enriched json.RawMessage
		err := s.Transact(ctx, func(tx *store.Transaction) error {
			var txErr error
			enriched, _, _, txErr = (runtime{}).enrichMutationPayloadTx(ctx, tx, mutationPayload(changed, nil), changed)
			return txErr
		})
		return enriched, err
	}

	enriched, err := enrich(pin.Version)
	if err != nil {
		t.Fatalf("enrich at the pin version: %v", err)
	}
	var result struct {
		WorkPins []store.WorkPin `json:"work_pins"`
	}
	if err := json.Unmarshal(enriched, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.WorkPins) != 1 || result.WorkPins[0].WorkID != workID || result.WorkPins[0].Version != pin.Version {
		t.Fatalf("work pins=%+v, want one pin for %s at version %d", result.WorkPins, workID, pin.Version)
	}

	for _, version := range []int64{pin.Version - 1, pin.Version + 1} {
		enriched, err := enrich(version)
		if err == nil || !strings.Contains(err.Error(), "does not match changed ref") {
			t.Fatalf("changed ref version %d against pin version %d: enriched=%s err=%v, want the version mismatch refusal", version, pin.Version, enriched, err)
		}
	}
}

// CD-0113 D3: a result that carries a pin takes its next_valid_intents from the
// pin and the workflow admission. A hand-written intent reaches only a result
// without a pin, so it may name a read that verifies a write, or a
// research-pack mutation, whose result names no work item. Only the derivation
// may name a work mutation.
func TestHandWrittenIntentsNeverNameAWorkMutation(t *testing.T) {
	t.Parallel()
	operations := map[string]agentcontract.ContractOperation{}
	for _, op := range agentcontract.ContractOperations {
		operations[op.Tool+"."+op.Operation] = op
	}
	derivations := map[string]bool{"nextIntentsFromPin": true}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}
	fset := token.NewFileSet()
	literals := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, name, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		for _, decl := range file.Decls {
			function := ""
			if fn, ok := decl.(*ast.FuncDecl); ok {
				function = fn.Name.Name
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				for _, intent := range nextIntentLiterals(lit) {
					tool, operation, literal := intentTarget(intent)
					position := fset.Position(intent.Pos())
					switch {
					case !literal && derivations[function]:
					case !literal:
						t.Errorf("%s: %s builds an intent from non-literal fields; only the pin derivation may", position, function)
					default:
						literals++
						op, known := operations[tool+"."+operation]
						if !known {
							t.Errorf("%s: hand-written intent names %s.%s, which the manifest does not declare", position, tool, operation)
						} else if op.Kind == agentcontract.OperationMutation && op.Capability != "research" {
							t.Errorf("%s: hand-written intent names work mutation %s; derive it from the work pin", position, op.ID)
						}
					}
				}
				return true
			})
		}
	}
	if literals == 0 {
		t.Fatal("found no hand-written intent literals; the scan is vacuous")
	}
}

// nextIntentLiterals returns the NextIntent literals lit builds directly:
// NextIntent{...} itself, or the elided elements of []NextIntent{{...}}.
func nextIntentLiterals(lit *ast.CompositeLit) []*ast.CompositeLit {
	switch typ := lit.Type.(type) {
	case *ast.Ident:
		if typ.Name == "NextIntent" {
			return []*ast.CompositeLit{lit}
		}
	case *ast.ArrayType:
		if elem, ok := typ.Elt.(*ast.Ident); ok && elem.Name == "NextIntent" {
			var out []*ast.CompositeLit
			for _, element := range lit.Elts {
				if inner, ok := element.(*ast.CompositeLit); ok && inner.Type == nil {
					out = append(out, inner)
				}
			}
			return out
		}
	}
	return nil
}

// intentTarget reads the Tool and Operation fields of an intent literal. It
// reports literal=false unless both are string literals.
func intentTarget(lit *ast.CompositeLit) (tool, operation string, literal bool) {
	for _, element := range lit.Elts {
		kv, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || (key.Name != "Tool" && key.Name != "Operation") {
			continue
		}
		value, ok := kv.Value.(*ast.BasicLit)
		if !ok || value.Kind != token.STRING {
			return "", "", false
		}
		text, err := strconv.Unquote(value.Value)
		if err != nil {
			return "", "", false
		}
		if key.Name == "Tool" {
			tool = text
		} else {
			operation = text
		}
	}
	return tool, operation, tool != "" && operation != ""
}
