package store

import (
	"bytes"
	"encoding/json"
	"testing"
)

func workflowProductTruth(value bool) *bool { return &value }

func TestWorkflowDefinitionValidationRequiresProductTruthClassification(t *testing.T) {
	for _, definition := range builtinWorkflowV1Definitions() {
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

func TestWorkflowDefinitionValidationEnforcesVersionedProductTruthMatrix(t *testing.T) {
	registry := NewBuiltinWorkflowRegistry()
	cases := []struct {
		name    string
		ref     string
		version int64
		value   bool
	}{
		{name: "latest implementation cannot opt out", ref: "workflow.implementation", version: 4, value: false},
		{name: "latest research cannot opt in", ref: "workflow.research", version: 4, value: true},
		{name: "historical implementation cannot opt in", ref: "workflow.implementation", version: 3, value: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			registered, ok := registry.Lookup(testCase.ref, testCase.version)
			if !ok {
				t.Fatalf("%s v%d is not registered", testCase.ref, testCase.version)
			}
			registered.Definition.ChangesProductTruth = workflowProductTruth(testCase.value)
			if err := ValidateWorkflowDefinition(registered.Definition); err == nil {
				t.Fatalf("%s v%d accepted product truth=%t", testCase.ref, testCase.version, testCase.value)
			}
		})
	}
}

func TestLatestBuiltinWorkflowProductTruthClassification(t *testing.T) {
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
		if definition.Version != 4 {
			t.Fatalf("latest %s version=%d, want 4", definition.Ref, definition.Version)
		}
		if definition.ChangesProductTruth == nil || *definition.ChangesProductTruth != want[definition.WorkKind] {
			t.Fatalf("latest %s product truth=%v, want %t", definition.Ref, definition.ChangesProductTruth, want[definition.WorkKind])
		}
		for _, version := range []int64{1, 2, 3} {
			registered, ok := registry.Lookup(definition.Ref, version)
			if !ok {
				t.Fatalf("%s v%d is not registered", definition.Ref, version)
			}
			if registered.Definition.ChangesProductTruth == nil || *registered.Definition.ChangesProductTruth {
				t.Fatalf("historical %s v%d product truth=%v, want explicit false", definition.Ref, version, registered.Definition.ChangesProductTruth)
			}
		}
		latest, ok := registry.Lookup(definition.Ref, 4)
		if !ok || latest.Definition.ChangesProductTruth == nil || *latest.Definition.ChangesProductTruth != want[definition.WorkKind] {
			t.Fatalf("latest %s product truth=%v, want %t", definition.Ref, latest.Definition.ChangesProductTruth, want[definition.WorkKind])
		}
	}
}

func TestLatestProductChangingDefinitionsHaveApprovalRouteWithoutHistoricalRewrite(t *testing.T) {
	registry := NewBuiltinWorkflowRegistry()
	for _, definition := range BuiltinWorkflowDefinitions() {
		if definition.ChangesProductTruth == nil || !*definition.ChangesProductTruth {
			continue
		}
		registered, ok := registry.Lookup(definition.Ref, 4)
		if !ok {
			t.Fatalf("latest %s is not registered", definition.Ref)
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
	for _, version := range []int64{1, 2, 3} {
		historical, ok := registry.Lookup("workflow.break_fix", version)
		if !ok {
			t.Fatalf("historical break-fix v%d is not registered", version)
		}
		if containsString(historical.Definition.AvailableActions, "approve_contract") {
			t.Fatalf("historical break-fix v%d gained a v4 approval route", version)
		}
		for _, step := range historical.Definition.StepGraph.Steps {
			if step.ID == "planning" {
				t.Fatalf("historical break-fix v%d gained a v4 planning step", version)
			}
		}
	}
}

func TestWorkflowDefinitionCanonicalProductTruthVersioning(t *testing.T) {
	registry := NewBuiltinWorkflowRegistry()
	latest, ok := registry.Lookup("workflow.implementation", 4)
	if !ok {
		t.Fatal("latest implementation definition is not registered")
	}
	canonical, err := CanonicalWorkflowDefinition(latest.Definition)
	if err != nil {
		t.Fatal(err)
	}
	var latestManifest map[string]any
	if err := json.Unmarshal(canonical, &latestManifest); err != nil {
		t.Fatal(err)
	}
	if latestManifest["schema_version"] != "1.3" {
		t.Fatalf("latest schema version=%v, want 1.3", latestManifest["schema_version"])
	}
	if value, ok := latestManifest["changes_product_truth"].(bool); !ok || !value {
		t.Fatalf("latest product truth=%v, want boolean true", latestManifest["changes_product_truth"])
	}

	for _, version := range []int64{1, 2, 3} {
		historical, ok := registry.Lookup("workflow.implementation", version)
		if !ok {
			t.Fatalf("historical implementation v%d is not registered", version)
		}
		historicalCanonical, err := CanonicalWorkflowDefinition(historical.Definition)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(historicalCanonical, []byte(`"changes_product_truth"`)) {
			t.Fatalf("historical canonical definition v%d acquired product-truth field", version)
		}
	}
}
