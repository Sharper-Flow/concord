package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
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
	var s *Store
	if assertions > 1 {
		s = historicalRepeatedDeliveryFixture(t, workID, assertions)
	} else {
		f := qCurrentAcceptanceWithDelivery(t, workID, deliveryCorrectionWrongPath)
		s = f.store
		ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), workID)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.run(ctx, "confirm_premise", "confirm", readWorkVersion(t, s, workID), nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := f.run(ctx, "complete", "complete", readWorkVersion(t, s, workID), json.RawMessage(`{"impact_verdict":"non-breaking"}`), nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`INSERT INTO agent_clients(client_ref,status,principal_ref,capabilities_json,product_scope_json,project_scope_json,created_at) VALUES('client/concord-1','active','principal/operator','[]','[]','[]','2026-09-10T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	return s
}

func historicalRepeatedDeliveryFixture(t *testing.T, work string, assertions int) *Store {
	t.Helper()
	c := evidenceCorpusForWork(t, work)
	var terminal Event
	for _, e := range c.events {
		if e.Kind == WorkflowCompleted {
			terminal = e
		}
	}
	var events []Event
	count := 0
	var version int64
	for _, e := range c.events {
		if e.Kind == WorkflowDeliveryCorrected || e.Kind == WorkflowCompleted && e.EventID != terminal.EventID {
			continue
		}
		var p map[string]any
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if e.Kind == WorkflowActionCompleted && p["delivery_state"] == "asserted" {
			if count >= assertions {
				continue
			}
			p["delivery_artifact"] = deliveryCorrectionWrongPath
			if count > 0 {
				p["delivery_artifact"] = deliveryCorrectionWrongPath + "/" + jsonInt(int64(count))
			}
			count++
		}
		if _, bumps := p["expected_version"]; bumps && e.Kind != "compaction_link.published" {
			if version == 0 {
				version = int64(p["expected_version"].(float64))
			}
			p["expected_version"], p["resulting_version"] = version, version+1
			version++
		}
		e.Payload = mustJSONValue(p)
		events = append(events, e)
	}
	if count != assertions {
		t.Fatalf("historical corpus has %d assertions, want %d", count, assertions)
	}
	c.events = events
	s := c.prefix(t, 141)
	if _, err := s.db.Exec(`INSERT INTO fold_guard VALUES(1); UPDATE work_items SET lifecycle='completed',version=? WHERE id=?; UPDATE workflow_instances SET instance_state='completed',completed_at=? WHERE work_id=?; DELETE FROM fold_guard`, version, work, terminal.OccurredAt.Format(time.RFC3339Nano), work); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(outcomeSnapshotContext(t, t.Context(), s.db), s.db); err != nil {
		t.Fatal(err)
	}
	return s
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
