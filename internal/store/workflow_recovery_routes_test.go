package store

import (
	"bytes"
	"reflect"
	"slices"
	"testing"
)

func TestCurrentRecoveryRoutesAreDeclaredAndDefensive(t *testing.T) {
	for _, definition := range BuiltinWorkflowDefinitions() {
		if len(definition.RecoveryRoutes) == 0 {
			t.Fatalf("%s has no declared recovery routes", definition.Ref)
		}
		if err := ValidateWorkflowDefinition(definition); err != nil {
			t.Fatal(err)
		}
		before, err := CanonicalWorkflowDefinition(definition)
		if err != nil {
			t.Fatal(err)
		}
		routes := workflowRecoveryRoutes(definition)
		if !reflect.DeepEqual(routes, definition.RecoveryRoutes) {
			t.Fatalf("%s resolved another table", definition.Ref)
		}
		routes[0].Target = "mutated"
		after, err := CanonicalWorkflowDefinition(definition)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("route read mutated %s: %v", definition.Ref, err)
		}
	}
}

func TestRecoveryRouteRegistrationRefusals(t *testing.T) {
	base := BuiltinWorkflowDefinitions()[0]
	evaluator := slices.IndexFunc(base.RecoveryRoutes, func(r WorkflowRecoveryRoute) bool {
		return r.Step == "acceptance" && r.Trigger == WorkflowRecoveryTriggerUnhealthyVerdict
	})
	complete := slices.IndexFunc(base.RecoveryRoutes, func(r WorkflowRecoveryRoute) bool {
		return r.Trigger == WorkflowRecoveryTriggerDisprovedPremiseAtComplete
	})
	if evaluator < 0 || complete < 0 {
		t.Fatal("current definition lacks the evaluator or completion route under test")
	}
	for name, mutate := range map[string]func(*WorkflowDefinition){
		"missing evaluator route": func(d *WorkflowDefinition) {
			d.RecoveryRoutes = slices.Delete(d.RecoveryRoutes, evaluator, evaluator+1)
		},
		"ambiguous step and trigger":                func(d *WorkflowDefinition) { d.RecoveryRoutes = append(d.RecoveryRoutes, d.RecoveryRoutes[0]) },
		"unknown trigger":                           func(d *WorkflowDefinition) { d.RecoveryRoutes[0].Trigger = "failed_attempt" },
		"unknown step":                              func(d *WorkflowDefinition) { d.RecoveryRoutes[0].Step = "nonexistent" },
		"unknown action":                            func(d *WorkflowDefinition) { d.RecoveryRoutes[0].Action = "unknown_action" },
		"unknown target":                            func(d *WorkflowDefinition) { d.RecoveryRoutes[0].Target = "nonexistent" },
		"unhealthy trigger pairs wrong action":      func(d *WorkflowDefinition) { d.RecoveryRoutes[0].Action = "supersede_contract" },
		"complete trigger pairs wrong action":       func(d *WorkflowDefinition) { d.RecoveryRoutes[complete].Action = "request_correction" },
		"complete trigger off the complete step":    func(d *WorkflowDefinition) { d.RecoveryRoutes[complete].Step = "acceptance" },
		"unhealthy trigger on a non-evaluator step": func(d *WorkflowDefinition) { d.RecoveryRoutes[0].Step = "design" },
		"target is the route step":                  func(d *WorkflowDefinition) { d.RecoveryRoutes[evaluator].Target = d.RecoveryRoutes[evaluator].Step },
		"target is terminal":                        func(d *WorkflowDefinition) { d.RecoveryRoutes[0].Target = "release" },
		"target is approval-gated":                  func(d *WorkflowDefinition) { d.RecoveryRoutes[0].Target = "planning" },
		"target not upstream": func(d *WorkflowDefinition) {
			d.StepGraph.Steps = append(d.StepGraph.Steps, step("downstream", WorkflowStepExternalEffect, "checkpoint_execution"))
			d.StepGraph.Edges = append(d.StepGraph.Edges, WorkflowEdge{From: "acceptance", To: "downstream", Kind: WorkflowEdgeForward})
			d.RecoveryRoutes[0].Target = "downstream"
		},
		"second failure edge": func(d *WorkflowDefinition) {
			d.StepGraph.Edges = append(d.StepGraph.Edges, WorkflowEdge{From: "acceptance", To: "execution", Kind: WorkflowEdgeFailure})
		},
		"no route table": func(d *WorkflowDefinition) { d.RecoveryRoutes = nil },
	} {
		t.Run(name, func(t *testing.T) {
			d := cloneWorkflowDefinition(base)
			mutate(&d)
			if err := ValidateWorkflowDefinition(d); err == nil {
				t.Fatal("invalid recovery table was admitted")
			}
			if _, err := NewWorkflowDefinitionRegistry().Register(d); err == nil {
				t.Fatal("invalid recovery table was registered")
			}
		})
	}
}

func TestCriterionAssociationCannotUseAnUnhealthyVerdictReturn(t *testing.T) {
	definition := BuiltinWorkflowDefinitions()[2]
	for _, routes := range [][]WorkflowRecoveryRoute{definition.RecoveryRoutes, definition.RecoveryRoutes[:1]} {
		definition.RecoveryRoutes = routes
		if workflowAdmissionStepAllows(definition, WorkflowAdmissionState{Step: "complete", ActiveContracts: 1}, "record_verdict") {
			t.Fatal("criterion association acquired an off-step route")
		}
	}
	if !workflowAdmissionStepAllows(definition, WorkflowAdmissionState{Step: "conclude"}, "record_verdict") {
		t.Fatal("declared criterion association route was removed")
	}
}

func TestRecoveryRouteTargetMustProduceArtifact(t *testing.T) {
	definition := BuiltinWorkflowDefinitions()[2]
	definition.StepGraph.Steps = append(definition.StepGraph.Steps, step("inspection", WorkflowStepInternalSQLite, "bind_evidence"))
	definition.StepGraph.Edges = append(definition.StepGraph.Edges, WorkflowEdge{From: "inspection", To: "conclude", Kind: WorkflowEdgeForward})
	definition.RecoveryRoutes[0].Target = "inspection"
	if err := validateWorkflowRecoveryRoutes(definition); err == nil {
		t.Fatal("recovery accepted an upstream non-producer")
	}
}

func TestDeclaredEvaluatorAdmitsObjectiveRevision(t *testing.T) {
	for _, definition := range BuiltinWorkflowDefinitions() {
		for _, route := range workflowRecoveryRoutes(definition) {
			if route.Trigger != WorkflowRecoveryTriggerUnhealthyVerdict || stepDeclaresAction(definition, route.Step, "complete") {
				continue
			}
			if !workflowContractCorrectionCheckpoint(definition, route.Step) {
				t.Errorf("%s %s refuses objective revision", definition.Ref, route.Step)
			}
			if !admissionContractCorrection(definition, admissionModelState{step: route.Step, contracts: 1, verdict: "bad", artifactStale: true}) {
				t.Errorf("%s %s model refuses objective revision", definition.Ref, route.Step)
			}
		}
	}
}
