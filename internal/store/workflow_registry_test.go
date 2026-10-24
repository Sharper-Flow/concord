package store

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestBuiltinRegistriesAreLazy(t *testing.T) {
	fset := token.NewFileSet()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	check := func(node ast.Node) {
		ast.Inspect(node, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			name, ok := call.Fun.(*ast.Ident)
			if ok && (name.Name == "NewBuiltinWorkflowRegistry" || name.Name == "BuiltinWorkflowRegistry" || name.Name == "NewBuiltinLaneRegistry" || name.Name == "builtinLaneRegistry") {
				t.Errorf("%s: %s builds a registry during package initialization", fset.Position(call.Pos()), name.Name)
			}
			return true
		})
	}
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range file.Decls {
			switch declaration := declaration.(type) {
			case *ast.GenDecl:
				if declaration.Tok == token.VAR {
					check(declaration)
				}
			case *ast.FuncDecl:
				if declaration.Name.Name == "init" {
					check(declaration.Body)
				}
			}
		}
	}
}

func workflowProductTruth(value bool) *bool { return &value }

func TestWorkflowDefinitionValidationRequiresProductTruthClassification(t *testing.T) {
	t.Parallel()
	for _, definition := range BuiltinWorkflowDefinitions() {
		definition.ChangesProductTruth = nil
		if err := ValidateWorkflowDefinition(definition); err == nil {
			t.Fatalf("%s definition with omitted product-truth classification passed validation", definition.Ref)
		}
	}

	generic := genericDefinitionFixture()
	generic.ChangesProductTruth = workflowProductTruth(true)
	if err := ValidateWorkflowDefinition(generic); err == nil {
		t.Fatal("generic definition with product-truth authority passed validation")
	}
}

func TestWorkflowDefinitionValidationEnforcesProductTruthMatrix(t *testing.T) {
	t.Parallel()
	registry := NewBuiltinWorkflowRegistry()
	cases := []struct {
		name  string
		ref   string
		value bool
	}{
		{name: "implementation cannot opt out", ref: "workflow.implementation", value: false},
		{name: "break-fix cannot opt out", ref: "workflow.break_fix", value: false},
		{name: "research cannot opt in", ref: "workflow.research", value: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			registered, ok := registry.Current(testCase.ref)
			if !ok {
				t.Fatalf("%s is not registered", testCase.ref)
			}
			registered.Definition.ChangesProductTruth = workflowProductTruth(testCase.value)
			if err := ValidateWorkflowDefinition(registered.Definition); err == nil {
				t.Fatalf("%s accepted product truth=%t", testCase.ref, testCase.value)
			}
		})
	}
}

func TestBuiltinWorkflowProductTruthClassification(t *testing.T) {
	t.Parallel()
	want := map[WorkKind]bool{
		WorkKindImplementation:    true,
		WorkKindBreakFix:          true,
		WorkKindResearch:          false,
		WorkKindArchitectureSpike: false,
		WorkKindOpsRunbook:        true,
		WorkKindStaticAnalysis:    false,
		WorkKindGenericOneOff:     false,
	}
	registry := NewBuiltinWorkflowRegistry()
	for _, definition := range BuiltinWorkflowDefinitions() {
		if definition.ChangesProductTruth == nil || *definition.ChangesProductTruth != want[definition.WorkKind] {
			t.Fatalf("built-in %s product truth=%v, want %t", definition.Ref, definition.ChangesProductTruth, want[definition.WorkKind])
		}
		registered, ok := registry.Lookup(definition.Ref, definition.Version)
		if !ok || registered.Definition.ChangesProductTruth == nil || *registered.Definition.ChangesProductTruth != want[definition.WorkKind] {
			t.Fatalf("registered %s product truth=%v, want %t", definition.Ref, registered.Definition.ChangesProductTruth, want[definition.WorkKind])
		}
	}
}

func TestWorkflowDefinitionRejectsFormFeedInputReference(t *testing.T) {
	t.Parallel()
	definition := genericDefinitionFixture()
	definition.StalenessRules = []WorkflowStalenessRule{{ID: "staleness.legacy", InputRef: "input:a\fb", Severity: "warning"}}
	if err := ValidateWorkflowDefinition(definition); err == nil {
		t.Fatal("new definition admission accepted a form-feed reference")
	}
}

// Only the current definition is addressable for each family.
func TestBuiltinWorkflowRegistryHoldsOnlyCurrentVersions(t *testing.T) {
	t.Parallel()
	registry := NewBuiltinWorkflowRegistry()
	highest := map[string]int64{}
	for _, definition := range BuiltinWorkflowDefinitions() {
		if definition.Version > highest[definition.Ref] {
			highest[definition.Ref] = definition.Version
		}
	}
	for ref, top := range highest {
		for version := top + 1; version <= top+3; version++ {
			if _, ok := registry.Lookup(ref, version); ok {
				t.Fatalf("%s v%d is registered, want nothing above v%d", ref, version, top)
			}
		}
	}
}

func TestProductChangingDefinitionsHaveApprovalRoute(t *testing.T) {
	t.Parallel()
	registry := NewBuiltinWorkflowRegistry()
	for _, definition := range BuiltinWorkflowDefinitions() {
		if definition.ChangesProductTruth == nil || !*definition.ChangesProductTruth {
			continue
		}
		registered, ok := registry.Current(definition.Ref)
		if !ok {
			t.Fatalf("%s is not registered", definition.Ref)
		}
		found := false
		for _, step := range registered.Definition.StepGraph.Steps {
			if step.Kind == WorkflowStepHumanCheckpoint && slices.Contains(step.Actions, "approve_contract") {
				found = true
			}
		}
		if !found {
			t.Fatalf("Product-changing %s lacks a human approve_contract route", definition.Ref)
		}
	}
}

func TestWorkflowDefinitionCanonicalManifestCarriesProductTruth(t *testing.T) {
	t.Parallel()
	registry := NewBuiltinWorkflowRegistry()
	registered, ok := registry.Current("workflow.implementation")
	if !ok {
		t.Fatal("implementation definition is not registered")
	}
	canonical, err := CanonicalWorkflowDefinition(registered.Definition)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(canonical, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest["schema_version"] != workflowDefinitionSchemaVersion {
		t.Fatalf("schema version=%v, want %s", manifest["schema_version"], workflowDefinitionSchemaVersion)
	}
	if value, ok := manifest["changes_product_truth"].(bool); !ok || !value {
		t.Fatalf("product truth=%v, want boolean true", manifest["changes_product_truth"])
	}
}

// TestRegistryKeyKeepsItsFormat pins the registry key to the ref, a NUL
// byte, and the decimal version, the format every registry map and
// composition table is keyed on.
func TestRegistryKeyKeepsItsFormat(t *testing.T) {
	for _, test := range []struct {
		ref     string
		version int64
		want    string
	}{
		{"workflow.implementation", 1, "workflow.implementation\x001"},
		{"workflow.implementation", 33, "workflow.implementation\x0033"},
		{"workflow.break_fix", 30, "workflow.break_fix\x0030"},
		{"workflow.research", 120, "workflow.research\x00120"},
		{"workflow.ops_runbook", 9223372036854775807, "workflow.ops_runbook\x009223372036854775807"},
		{"", 0, "\x000"},
	} {
		if got := registryKey(test.ref, test.version); got != test.want {
			t.Errorf("registryKey(%q, %d) = %q, want %q", test.ref, test.version, got, test.want)
		}
	}
}
