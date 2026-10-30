package store

import "testing"

func TestAcceptedCriteriaPrecedeArchitecture(t *testing.T) {
	definition := mustBuiltinDefinition(t, "workflow.implementation").Definition
	for _, edge := range definition.StepGraph.Edges {
		if edge.From == "planning" && edge.Kind == WorkflowEdgeForward {
			if edge.To != "design" {
				t.Fatalf("accepted criteria advance to %q, want architecture before execution", edge.To)
			}
			return
		}
	}
	t.Fatal("accepted criteria have no forward architecture stage")
}

func TestArchitectureReviewGuardsDeliveryAndCompletion(t *testing.T) {
	report := reviewBlockFixture()
	report.Disposition = "ready_for_final_proof"
	report.Findings = []ReviewFinding{}
	report.Dependencies.ArchitectureNotApplicable = false
	report.Dependencies.ArchitectureDigest = testManifestDigest
	definition := mustBuiltinDefinition(t, "workflow.implementation").Definition
	for _, phase := range []string{"unreviewed", "current", "stale"} {
		t.Run(phase, func(t *testing.T) {
			state := WorkflowReviewState{Dependencies: &report.Dependencies, Findings: []ReviewFinding{}}
			want := KindMissingEvidence
			if phase != "unreviewed" {
				state.Candidate = &report.OutputCandidate
				state.Accepted = &WorkflowAcceptedReview{ReportRef: "review:recorded", Report: *report}
				state.DependenciesCurrent = phase == "current"
				want = KindStaleRequiresReview
			}
			if err := workflowAdmitReview(state); phase == "current" && err != nil || phase != "current" && !hasFailureKind(err, want) {
				t.Fatalf("architecture Review exit=%v, phase=%s", err, phase)
			}
			if phase == "current" {
				return
			}
			for _, action := range []string{"record_delivery", "complete"} {
				admission := WorkflowAdmissionState{Step: "execution", InstanceState: "running", Lifecycle: "in_progress", ActiveContracts: 1,
					Review: &state, Delivery: workflowDeliveryAdmission{Started: true}}
				decision := workflowAdmit(definition, admission, action)
				if decision.Admitted || decision.Failure == nil || decision.Failure.Kind != want {
					t.Fatalf("%s bypassed %s architecture: %+v", action, phase, decision)
				}
			}
		})
	}
}
