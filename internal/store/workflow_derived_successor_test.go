package store

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestNonProductChangingSuccessorPreservesDerivedLaw(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name     string
		modifies []string
		refused  bool
	}{
		{name: "derived revision", modifies: []string{"spec:two"}},
		{name: "empty modification control", modifies: []string{}},
		{name: "legislated revision refused", modifies: []string{"spec:legislated"}, refused: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			s := openTemp(t)
			const workID = "research-law-successor"
			actor := WorkflowActor{PrincipalRef: "principal:law-successor", ClientRef: "client:law-successor", AgentRef: "agent:law-successor", SessionRef: "session:law-successor", ActorClass: ActorAgent}
			seedWork(t, s, workID)
			seedWorkflowLaw(t, s)
			seedDerivedLawExtension(t, s)
			// Both successor mandate candidates exist before this item's start.
			// A research contract revises derived law; it cannot add new law.
			// spec:two exists when its committed file is on the default
			// branch, so the successor candidate is a committed spec fixture.
			commitFixtureLawFile(t, lawChangeFixtureHome(t, s), "spec:two", fixtureLawFolderLaw("spec:two"))
			version := seedDerivedRevisionWorkflow(t, s, workID, actor)
			// The initial contract declares spec:one in every scenario, so the
			// CD-0230 D4 law-change inputs ride on the shared map; the
			// successor action consumes the same fields, and a legislated
			// successor still refuses at the tier guard before they are read.
			fields := map[string]any{
				"premise":      "Revise the derived record inside this contract.",
				"spec_mandate": []string{"spec:one"}, "law_modifies": []string{"spec:one"},
				"law_change_reason": "Move the derived revision into this contract.",
				"outcome_predicates": []map[string]any{{
					"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "outcome",
					"outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"resolved"}},
				}},
			}
			if err := runDerivedLawApproval(t, s, workID, version, actor, mustJSONValue(fields)); err != nil {
				t.Fatalf("live approval: %v", err)
			}
			// Re-pin to the other existing derived law without enacting an
			// unrelated deletion of the predecessor's mandate.
			fields = pContractSuccessor(t, s, workID, "Revise the derived record inside this contract.")
			fields["spec_mandate"] = []string{"spec:two"}
			fields["law_modifies"] = scenario.modifies
			fields["decision_domains"] = []string{}
			if len(scenario.modifies) != 0 {
				fields["law_change_reason"] = "Move the derived revision into this contract."
			}
			if scenario.refused {
				fields["spec_mandate"] = []string{"spec:two", "spec:legislated"}
			}
			version = readWorkVersion(t, s, workID)
			before := contractRecordEffectSnapshot(t, s, workID)
			err := reviseContractRecord(t, s, workID, actor, fields, version, nil)
			if scenario.refused {
				var failure *Failure
				if !failureAs(err, &failure) || !strings.Contains(failure.Detail, "may revise only derived law") {
					t.Fatalf("legislated successor did not meet the tier refusal: %v", err)
				}
				if after := contractRecordEffectSnapshot(t, s, workID); after != before {
					t.Fatal("refused legislated revision changed the record")
				}
				return
			}
			if err != nil {
				t.Fatalf("live successor action: %v", err)
			}
			assertSuccessor := func() {
				t.Helper()
				var stored string
				if err := s.DatabaseForTesting().QueryRow(`SELECT law_modifies FROM workflow_contracts WHERE work_id=? AND contract_version=2 AND superseded_by IS NULL`, workID).Scan(&stored); err != nil {
					t.Fatal(err)
				}
				want := workflowJSON(scenario.modifies)
				if stored != want {
					t.Errorf("active successor law_modifies=%s, want %s", stored, want)
				}
				revisions, err := readWorkflowLawRevisions(ctx, s.DatabaseForTesting(), workID, 2)
				wantPins := []WorkflowLawRevision{{LawID: "spec:two", ContentHash: fixtureLawFolderLawHash("spec:two")}}
				if err != nil || !slices.Equal(revisions, wantPins) {
					t.Fatalf("successor pins=%v, want %v: %v", revisions, wantPins, err)
				}
				var supersededBy int64
				if err := s.DatabaseForTesting().QueryRow(`SELECT superseded_by FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&supersededBy); err != nil || supersededBy != 2 {
					t.Fatalf("predecessor superseded_by=%d, want 2: %v", supersededBy, err)
				}
			}
			assertSuccessor()
		})
	}
}
