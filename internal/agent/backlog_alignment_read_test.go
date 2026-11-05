package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestDiagnosisStoresBacklogAlignment(t *testing.T) {
	t.Parallel()
	// workflow-engine-contract §§2.1, 5.1, 11 and 12 retire record_alignment;
	// diagnosis remains the authoring path for these retained alignment facts.
	for _, workflow := range []store.WorkflowStepPlanType{store.WorkflowStepPlanFix} {
		for _, matches := range []int{0, 1, 2, 64} {
			t.Run(fmt.Sprintf("%s/%d", workflow, matches), func(t *testing.T) {
				s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition", "product_read"})
				step := "plan"
				if workflow == store.WorkflowStepPlanFix {
					step = "diagnose"
				}
				version := seedAgentWorkflowAt(t, s, grant, workflow, step)
				scopeVersion, _, err := s.ScopeVersion(t.Context(), "project-1")
				if err != nil {
					t.Fatal(err)
				}
				env := mutationEnvelope(grant, scopeVersion)
				checkRows := func(wantSearched, wantOutcome string, wantIDs []string) {
					t.Helper()
					rows, err := s.DatabaseForTesting().Query(`SELECT COALESCE(related_work_id,''),searched,outcome FROM workflow_backlog_alignment WHERE work_id='work-1' ORDER BY related_work_id`)
					if err != nil {
						t.Fatal(err)
					}
					defer rows.Close()
					ids := []string{}
					count := 0
					for rows.Next() {
						var id, searched, outcome string
						if err := rows.Scan(&id, &searched, &outcome); err != nil {
							t.Fatal(err)
						}
						if searched != wantSearched || outcome != wantOutcome {
							t.Fatalf("stored search=%q outcome=%q", searched, outcome)
						}
						if id != "" {
							ids = append(ids, id)
						}
						count++
					}
					if err := rows.Err(); err != nil {
						t.Fatal(err)
					}
					if count != len(wantIDs)+1 || !slices.Equal(ids, wantIDs) {
						t.Fatalf("stored alignment rows=%d ids=%v want=%v", count, ids, wantIDs)
					}
				}
				action := "record_diagnosis"
				outcome := "none_found"
				related := []string{}
				for i := matches - 1; i >= 0; i-- {
					id := fmt.Sprintf("related-%02d", i)
					if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{{
						EventID: "created-" + id, Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: id,
						Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 4,
						Payload: json.RawMessage(`{"work_kind":"task","title":"Related work"}`),
					}, {
						EventID: "membership-" + id, Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: id,
						Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1,
						Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`),
					}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, id): 0}}); err != nil {
						t.Fatal(err)
					}
					related = append(related, id)
				}
				searched := "Searched open work for duplicate objectives; no inferred relations."
				if matches == 64 {
					searched = strings.Repeat("s", 4096)
				}
				fields := map[string]any{"searched": searched, "outcome": outcome}
				if matches > 0 {
					outcome = "related_found"
					fields["outcome"], fields["related_ids"] = outcome, related
				}
				if action == "record_diagnosis" {
					fields["reproduction"], fields["root_cause"] = "The defect reproduces.", "The cause is identified."
				}
				raw, err := json.Marshal(map[string]any{"work_id": "work-1", "expected_version": version, "action_id": action, "fields": fields, "idempotency_key": "alignment-read-record"})
				if err != nil {
					t.Fatal(err)
				}
				response := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
				if response.Outcome != agentcontract.OutcomeOK {
					t.Fatalf("%s: %+v", action, response.Error)
				}
				slices.Sort(related)
				checkRows(searched, outcome, related)
				if matches == 1 {
					effectKind := store.WorkflowBacklogAlignmentRecorded
					if action == "record_diagnosis" {
						effectKind = store.WorkflowDiagnosisRecorded
					}
					evidence := func() [5]string {
						t.Helper()
						var facts [5]string
						if err := s.DatabaseForTesting().QueryRow(`SELECT event_id,payload_digest FROM effect_receipts WHERE subject_id='work-1' AND kind=?`, effectKind).Scan(&facts[0], &facts[1]); err != nil {
							t.Fatal(err)
						}
						if err := s.DatabaseForTesting().QueryRow(`SELECT canonical_digest,op_id,result_event_ids FROM idempotency_records WHERE idempotency_key='alignment-read-record'`).Scan(&facts[2], &facts[3], &facts[4]); err != nil {
							t.Fatal(err)
						}
						return facts
					}
					before := evidence()
					if err := s.Transact(t.Context(), func(tx *store.Transaction) error {
						_, err := store.RecordLinearIssueLinkTx(t.Context(), tx, store.LinearIssueLink{WorkID: related[0], RemoteIssueUUID: "remote-align-related", HumanKey: "CON-1", URL: "https://linear.app/example/issue/CON-1"})
						return err
					}); err != nil {
						t.Fatal(err)
					}
					removalHandoff := store.WorkRemovalHandoff{Findings: []string{"finding"}, RemainingScope: []string{"scope"}, Blockers: []string{"blocker"}, Artifacts: []string{"artifact"}, RenewalConditions: []string{"renewal"}}
					removalHandoffJSON, err := json.Marshal(removalHandoff)
					if err != nil {
						t.Fatal(err)
					}
					removalHandoffHash := sha256.Sum256(removalHandoffJSON)
					removalScope, _, err := s.ScopeVersion(t.Context(), "project-1")
					if err != nil {
						t.Fatal(err)
					}
					removal, err := json.Marshal(map[string]any{"operation_id": "remove-related", "idempotency_key": "remove-related-key", "work_id": related[0], "expected_version": workVersion(t, s, related[0]), "reason": "shelved", "actor": "human-1", "product_id": "product-1",
						"execution_relinquished": true, "writes_reconciled": true, "effects_reconciled": true, "dependencies_resolved": true, "artifacts_verified": true,
						"handoff": removalHandoff,
						"linear":  store.LinearHandoffConfirmation{ProductID: "product-1", RemoteIssueUUID: "remote-align-related", Destination: "linear", HandoffDigest: fmt.Sprintf("sha256:%x", removalHandoffHash)}})
					if err != nil {
						t.Fatal(err)
					}
					if removed := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "remove", Input: removal}, mutationEnvelope(grant, removalScope)); removed.Outcome != agentcontract.OutcomeOK {
						t.Fatalf("related removal refused: %+v", removed.Error)
					}
					if after := evidence(); after != before {
						t.Fatalf("alignment evidence changed: before=%v after=%v", before, after)
					}
					checkRows(searched, "related_found", nil)
				}
			})
		}
	}
}
