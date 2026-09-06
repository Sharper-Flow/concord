package store

import (
	"encoding/json"
	"testing"
)

func workflowProductTruth(value bool) *bool { return &value }

func TestWorkflowDefinitionValidationRequiresProductTruthClassification(t *testing.T) {
	for _, definition := range BuiltinWorkflowDefinitions() {
		definition.ChangesProductTruth = nil
		if err := ValidateWorkflowDefinition(definition); err == nil {
			t.Fatalf("%s definition with omitted product-truth classification passed validation", definition.Ref)
		}
	}

	generic := builtinGenericOneOff()
	generic.ChangesProductTruth = workflowProductTruth(true)
	if err := ValidateWorkflowDefinition(generic); err == nil {
		t.Fatal("generic definition with product-truth authority passed validation")
	}
}

func TestWorkflowDefinitionValidationEnforcesProductTruthMatrix(t *testing.T) {
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
			registered, ok := registry.Lookup(testCase.ref, 1)
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

func TestVerifyAcceptsEveryShippedDefinitionVersion(t *testing.T) {
	registry := NewBuiltinWorkflowRegistry()
	for _, fixture := range shippedDefinitionDigestFixture {
		if err := registry.Verify(fixture.ref, fixture.version, fixture.digest); err != nil {
			t.Fatalf("%s v%d does not verify: %v", fixture.ref, fixture.version, err)
		}
	}
}

func TestBuiltinWorkflowProductTruthClassification(t *testing.T) {
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
		if definition.Version != authoredWorkflowDefinitionVersion {
			t.Fatalf("built-in %s version=%d, want %d", definition.Ref, definition.Version, authoredWorkflowDefinitionVersion)
		}
		if definition.ChangesProductTruth == nil || *definition.ChangesProductTruth != want[definition.WorkKind] {
			t.Fatalf("built-in %s product truth=%v, want %t", definition.Ref, definition.ChangesProductTruth, want[definition.WorkKind])
		}
		registered, ok := registry.Lookup(definition.Ref, definition.Version)
		if !ok || registered.Definition.ChangesProductTruth == nil || *registered.Definition.ChangesProductTruth != want[definition.WorkKind] {
			t.Fatalf("registered %s product truth=%v, want %t", definition.Ref, registered.Definition.ChangesProductTruth, want[definition.WorkKind])
		}
	}
}

// The built-in registry holds the authored and retained historical versions.
func TestBuiltinWorkflowRegistryHoldsOneVersionPerFamily(t *testing.T) {
	registry := NewBuiltinWorkflowRegistry()
	for _, definition := range historicalWorkflowDefinitions() {
		if _, ok := registry.Lookup(definition.Ref, definition.Version); !ok {
			t.Fatalf("%s v%d is not registered", definition.Ref, definition.Version)
		}
	}
	for _, definition := range BuiltinWorkflowDefinitions() {
		for _, version := range []int64{0, authoredWorkflowDefinitionVersion + 1, authoredWorkflowDefinitionVersion + 2} {
			if _, ok := registry.Lookup(definition.Ref, version); ok {
				t.Fatalf("%s v%d is registered, want declared versions only", definition.Ref, version)
			}
		}
	}
}

func TestProductChangingDefinitionsHaveApprovalRoute(t *testing.T) {
	registry := NewBuiltinWorkflowRegistry()
	for _, definition := range BuiltinWorkflowDefinitions() {
		if definition.ChangesProductTruth == nil || !*definition.ChangesProductTruth {
			continue
		}
		registered, ok := registry.Lookup(definition.Ref, definition.Version)
		if !ok {
			t.Fatalf("%s is not registered", definition.Ref)
		}
		found := false
		for _, step := range registered.Definition.StepGraph.Steps {
			if step.Kind == WorkflowStepHumanCheckpoint && containsString(step.Actions, "approve_contract") {
				found = true
			}
		}
		if !found {
			t.Fatalf("Product-changing %s lacks a human approve_contract route", definition.Ref)
		}
	}
}

func TestWorkflowDefinitionCanonicalManifestCarriesProductTruth(t *testing.T) {
	registry := NewBuiltinWorkflowRegistry()
	registered, ok := registry.Lookup("workflow.implementation", 1)
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
