package store

import "testing"

// CD-0243 D2-D4 retire preferences, seriousness, size and cancellation as
// independent consent triggers. Every applicable approved class still counts.
func TestSixClassApprovalPolicy(t *testing.T) {
	t.Parallel()
	definition := approvalPolicyDefinition(WorkflowApprovalPolicyConsequenceV1, "approve_contract", "supersede_contract", "confirm_premise", "complete")
	for _, action := range []string{"approve_contract", "supersede_contract", "confirm_premise"} {
		for _, domains := range [][]string{{}, {"preference"}, {"product"}, {"ui_ux"}, {"production_data"}, {"security"}, {"increased_spending"}, {"legal"}} {
			facts := WorkflowApprovalFacts{ProposedDecisionDomains: domains, ActiveDecisionDomains: domains}
			got, err := ClassifyWorkflowApproval(definition, action, facts)
			if err != nil {
				t.Fatalf("%s %v: %v", action, domains, err)
			}
			want := WorkflowConsentRequired
			if len(domains) == 0 || domains[0] == "preference" {
				want = WorkflowConsentNone
			}
			if got.Consent != want {
				t.Errorf("%s %v: consent %s, want %s", action, domains, got.Consent, want)
			}
		}
	}
	got, err := ClassifyWorkflowApproval(definition, "complete", WorkflowApprovalFacts{})
	if err != nil || got.Consent != WorkflowConsentNone {
		t.Errorf("cancellation fact: %+v, %v; want no independent consent", got, err)
	}
}
