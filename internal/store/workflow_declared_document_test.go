package store

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"
)

func TestWorkflowDeclaredDocumentEffects(t *testing.T) {
	for _, actionID := range []string{"record_proposal", "record_design"} {
		for _, declaresDocument := range []bool{false, true} {
			name := "no_document"
			if declaresDocument {
				name = "declared_document"
			}
			t.Run(actionID+"/"+name, func(t *testing.T) {
				ctx := context.Background()
				s := openTemp(t)
				defer s.Close()
				const workID = "work-declared-document"
				seedStepWork(t, s, workID)
				encoded, err := json.Marshal(currentWorkflowDefinition(t))
				if err != nil {
					t.Fatal(err)
				}
				var definition WorkflowDefinition
				if err := json.Unmarshal(encoded, &definition); err != nil {
					t.Fatal(err)
				}
				definition.Ref = "workflow.document-local"
				if !declaresDocument {
					for i := range definition.ActionDefinitions {
						if definition.ActionDefinitions[i].ID == actionID {
							definition.ActionDefinitions[i].Payload = WorkflowPayloadDefinition{Closed: true, Fields: []WorkflowPayloadField{}}
						}
					}
				}
				registry := NewWorkflowDefinitionRegistry()
				registered, err := registry.Register(definition)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.Transact(ctx, func(tx *Transaction) error {
					return InitializeWorkflowTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: stepFixtureActor(), Registry: registry, Now: time.Unix(5, 0).UTC()})
				}); err != nil {
					t.Fatal(err)
				}
				pinned, err := VerifyWorkflowInstanceDefinition(ctx, s, registry, workID)
				if err != nil || pinned.Digest != registered.Digest {
					t.Fatalf("work definition pin=%+v err=%v, want digest %s", pinned, err, registered.Digest)
				}
				declared, err := workflowDeclaredActionDefinition(pinned.Definition, actionID)
				if err != nil || (len(declared.Payload.Fields) != 0) != declaresDocument {
					t.Fatalf("pinned action fields=%+v err=%v, document=%t", declared.Payload.Fields, err, declaresDocument)
				}
				apply := func(action, payload, identity string) (WorkflowActionExecutionResult, error) {
					t.Helper()
					version := readWorkVersion(t, s, workID)
					actor := stepFixtureActor()
					tx, err := s.db.BeginTx(ctx, nil)
					if err != nil {
						t.Fatal(err)
					}
					defer tx.Rollback()
					result, err := applyWorkflowActionRawTx(ctx, tx, newFoldScope(tx), registry, WorkflowActionExecutionRequest{
						WorkID: workID, ExpectedVersion: version, ActionID: action, Payload: json.RawMessage(payload), Actor: actor,
						AcceptedInputsDigest: testManifestDigest, IdempotencyIdentity: identity, OperationID: identity,
						PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: identity,
						RequestID: "request:" + identity, ContractDigest: testManifestDigest, Now: time.Unix(10, 0).UTC(),
					})
					if err != nil {
						return result, err
					}
					return result, tx.Commit()
				}
				proposal := `{"problem":"The bounded problem.","affected":["The affected system."],"stakes":"The bounded stakes.","user_outcomes":["The checked outcome."]}`
				design := `{"approach":"Keep the declared document.","decisions":[{"id":"decision:document","question":"Which content?","choice":"Declared members","rationale":"The pin owns applicability.","rejected":[]}],"touched_refs":["internal/store/workflow_design.go"]}`
				if actionID == "record_design" {
					if _, err := apply("record_proposal", proposal, "prepare-proposal"); err != nil {
						t.Fatal(err)
					}
					if _, err := apply("record_alignment", `{"searched":"The bounded work inventory.","outcome":"none_found"}`, "prepare-alignment"); err != nil {
						t.Fatal(err)
					}
				}
				version := readWorkVersion(t, s, workID)
				var beforeSeq int64
				if err := s.db.QueryRow(`SELECT coalesce(max(seq),0) FROM domain_events`).Scan(&beforeSeq); err != nil {
					t.Fatal(err)
				}
				documentKind, table, payload := WorkflowProposalRecorded, "workflow_proposal_records", proposal
				if actionID == "record_design" {
					documentKind, table, payload = WorkflowDesignRecorded, "workflow_design_records", design
				}
				wantEffects := []string{WorkflowActionCompleted}
				wantRows := 0
				if declaresDocument {
					// A declared document still refuses missing and undeclared members
					// without leaving an event, row, or version advance behind.
					for i, invalid := range []string{`{}`, payload[:len(payload)-1] + `,"unknown":"field"}`} {
						_, err := apply(actionID, invalid, "invalid-document-"+string(rune('a'+i)))
						var failure *Failure
						if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload {
							t.Fatalf("invalid document error=%v, want invalid_payload", err)
						}
					}
					wantEffects = append([]string{documentKind}, wantEffects...)
					wantRows = 1
				} else {
					payload = `{}`
				}
				result, err := apply(actionID, payload, "record-document")
				if err != nil {
					t.Fatalf("pinned action execution: %v", err)
				}
				rows, err := s.db.Query(`SELECT kind FROM domain_events WHERE subject_id=? AND seq>? ORDER BY seq`, workID, beforeSeq)
				if err != nil {
					t.Fatal(err)
				}
				var effects []string
				for rows.Next() {
					var kind string
					if err := rows.Scan(&kind); err != nil {
						t.Fatal(err)
					}
					effects = append(effects, kind)
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				rows.Close()
				if !slices.Equal(effects, wantEffects) {
					t.Fatalf("action effects=%v, want exactly %v", effects, wantEffects)
				}
				if got := readWorkVersion(t, s, workID); got != version+int64(len(wantEffects)) || result.ResultingVersion != got {
					t.Fatalf("work version=%d result=%d, want %d", got, result.ResultingVersion, version+int64(len(wantEffects)))
				}
				var recorded int
				if err := s.db.QueryRow(`SELECT count(*) FROM `+table+` WHERE work_id=?`, workID).Scan(&recorded); err != nil {
					t.Fatal(err)
				}
				if recorded != wantRows {
					t.Fatalf("typed document rows=%d, want %d", recorded, wantRows)
				}
			})
		}
	}
}
