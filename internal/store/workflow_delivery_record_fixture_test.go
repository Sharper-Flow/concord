package store

import (
	"encoding/json"
	"strings"
	"testing"
)

const deliveryCorrectionWrongPath = "file:internal/store/impl.go"

func jsonInt(value int64) string { raw, _ := json.Marshal(value); return string(raw) }

func completedDeliveryFixture(t *testing.T, workID string) (*Store, string, int64, int, int64) {
	t.Helper()
	s := completedDeliveryFixtureWithAssertions(t, workID, 1)
	id, seq, version := deliveryAssertionEvent(t, s, workID, 0)
	return s, id, readWorkVersion(t, s, workID), version, seq
}

func completedDeliveryFixtureWithAssertions(t *testing.T, workID string, assertions int) *Store {
	t.Helper()
	{
		f := qCurrentAcceptanceWithDelivery(t, workID, deliveryCorrectionWrongPath)
		s := f.store
		for i := 1; i < assertions; i++ {
			payload := mustJSONValue(map[string]any{"delivery_artifact": deliveryCorrectionWrongPath + "/" + jsonInt(int64(i)), "delivery_state": "asserted"})
			if err := runVerdictActionAs(t, s, workID, "record_delivery", payload, 0, f.owner); err != nil {
				t.Fatalf("record delivery: %v", err)
			}
		}
		ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), workID)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.run(ctx, "confirm_premise", "confirm", readWorkVersion(t, s, workID), nil, nil); err != nil {
			t.Fatal(err)
		}
		completeWorkflowPlanFixture(t, s, workID)
		if err := f.run(ctx, "complete", "complete", readWorkVersion(t, s, workID), json.RawMessage(`{"impact_verdict":"non-breaking"}`), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`INSERT INTO agent_clients(client_ref,status,principal_ref,capabilities_json,product_scope_json,project_scope_json,created_at) VALUES('client/concord-1','active','principal/operator','[]','[]','[]','2026-09-10T00:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		return s
	}
}

func workflowCompletedFixtureEvent(id, workID, actor, verdictRef string, expected int64) Event {
	event := workflowEventWithActor(id, WorkflowCompleted, workID, actor, map[string]any{"work_id": workID, "expected_version": expected, "resulting_version": expected + 1, "terminal_state": "completed", "final_verdict_kind": "ok", "verdict_actor_ref": verdictRef, "premise_confirmed": false, "evidence_count": 0, "changed_refs_digest": "sha256:" + strings.Repeat("a", 64), "impact_verdict": "non-breaking"})
	event.PayloadVersion = 2
	return event
}

func deliveryAssertionEvent(t *testing.T, s *Store, workID string, offsetFromLatest int) (string, int64, int) {
	t.Helper()
	var id string
	var seq int64
	var version int
	if err := s.db.QueryRow(`SELECT event_id,effect_seq,payload_version FROM effect_receipts WHERE subject_type='work_item' AND subject_id=? AND kind=? AND delivery_assertion=1 ORDER BY effect_seq DESC LIMIT 1 OFFSET ?`, workID, WorkflowActionCompleted, offsetFromLatest).Scan(&id, &seq, &version); err != nil {
		t.Fatal(err)
	}
	return id, seq, version
}
