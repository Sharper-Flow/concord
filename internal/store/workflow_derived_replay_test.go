package store

import (
	"encoding/json"
	"testing"
)

func TestNonProductChangingLawRevision(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name      string
		modifies  []string
		successor bool
	}{
		{name: "empty modification control", modifies: []string{}},
		{name: "derived approval", modifies: []string{"spec:one"}},
		{name: "derived successor", modifies: []string{}, successor: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			s := openTemp(t)
			const workID = "research-law-revision"
			actor := WorkflowActor{PrincipalRef: "principal:law-replay", ClientRef: "client:law-replay", AgentRef: "agent:law-replay", SessionRef: "session:law-replay", ActorClass: ActorAgent}
			seedWork(t, s, workID)
			seedWorkflowLaw(t, s)
			version := seedDerivedRevisionWorkflow(t, s, workID, actor)
			fields := map[string]any{
				"premise":      "Preserve the approved derived-law revision.",
				"spec_mandate": []string{"spec:one"}, "law_modifies": scenario.modifies,
				"outcome_predicates": []map[string]any{{
					"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "outcome",
					"outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"resolved"}},
				}},
			}
			// A contract that declares a modification records why (CD-0230 D4);
			// an empty law_modifies changes no law and must not declare the
			// law-change inputs.
			if len(scenario.modifies) != 0 {
				fields["law_change_reason"] = "Preserve the approved derived-law revision."
			}
			payload, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if err := runDerivedLawApproval(t, s, workID, version, actor, payload); err != nil {
				t.Fatalf("live approval: %v", err)
			}
			contractVersion := int64(1)
			modifies := scenario.modifies
			if scenario.successor {
				contractVersion = 2
				modifies = []string{"spec:one"}
				fields = pContractSuccessor(t, s, workID, "Preserve the approved derived-law revision.")
				fields["law_modifies"] = modifies
				fields["law_change_reason"] = "Carry the derived revision into the successor contract."
				version = readWorkVersion(t, s, workID)
				if err := reviseContractRecord(t, s, workID, actor, fields, version, nil); err != nil {
					t.Fatalf("live successor fold: %v", err)
				}
			}
			assertRevision := func() {
				t.Helper()
				var stored string
				if err := s.DatabaseForTesting().QueryRow(`SELECT law_modifies FROM workflow_contracts WHERE work_id=? AND contract_version=? AND superseded_by IS NULL`, workID, contractVersion).Scan(&stored); err != nil {
					t.Fatal(err)
				}
				want, err := json.Marshal(modifies)
				if err != nil || stored != string(want) {
					t.Fatalf("active contract %d law_modifies=%s, want %s: %v", contractVersion, stored, want, err)
				}
				if scenario.successor {
					var supersededBy int64
					if err := s.DatabaseForTesting().QueryRow(`SELECT superseded_by FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&supersededBy); err != nil || supersededBy != contractVersion {
						t.Fatalf("predecessor superseded_by=%d, want %d: %v", supersededBy, contractVersion, err)
					}
				}
			}
			assertRevision()
		})
	}
}
