package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
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
			ctx := context.Background()
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
				revisions, err := readWorkflowLawRevisions(ctx, s.DatabaseForTesting(), workID, 1)
				if err != nil {
					t.Fatal(err)
				}
				fields["contract_version"] = contractVersion
				fields["law_modifies"] = modifies
				fields["law_revisions"] = revisions
				fields["law_boundary_version"] = 1
				fields["required_evidence"] = []string{"verification"}
				fields["route_conventions"] = []string{}
				fields["rigor_class"] = "prototype_internal"
				fields["consequence_class"] = "internal_sqlite"
				// The successor declares spec:one, so its recorded contract
				// carries the canonical law_change fact the constructor would
				// derive: build it through the production derive helper rather
				// than authoring bytes, and keep the raw law-change inputs off
				// the successor contract, whose payload shape is closed.
				lawChangeInputs := map[string]json.RawMessage{
					workflowLawChangeReasonField: json.RawMessage(`"Carry the derived revision into the successor contract."`),
				}
				// The store pools one connection: resolve the pinned definition
				// before the derive transaction parks it.
				definition := pinnedWorkflowDefinitionForTest(t, s, workID)
				ctx, err = s.EstablishKnowledgeSourceSetProof(ctx, workID)
				if err != nil {
					t.Fatal(err)
				}
				tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				lawChange, err := deriveWorkflowLawChangeTx(ctx, tx, definition, workID, lawChangeInputs, modifies, nil, []string{})
				if rollbackErr := tx.Rollback(); rollbackErr != nil {
					t.Fatal(rollbackErr)
				}
				if err != nil {
					t.Fatal(err)
				}
				fields["law_change"] = lawChange
				version = readWorkVersion(t, s, workID)
				actorRef, err := WorkflowActorRef(actor)
				if err != nil {
					t.Fatal(err)
				}
				event := workflowEventWithActor("research-law-successor", WorkflowContractSuperseded, workID, actorRef, map[string]any{
					"work_id": workID, "expected_version": version, "resulting_version": version + 1,
					"previous_contract_version": 1, "new_contract_version": contractVersion,
					"supersede_reason": "Revise the derived record.", "audit_evidence": []string{"evidence:law-revision"},
					"successor_contract": fields,
				})
				event.PayloadVersion = eventKindRegistry[WorkflowContractSuperseded].CurrentVersion
				event.OccurredAt = time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
				event = withPinnedDecisionFact(t, pinnedWorkflowDefinitionForTest(t, s, workID), event)
				if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
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
