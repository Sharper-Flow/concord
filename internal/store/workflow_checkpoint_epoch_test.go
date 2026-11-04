package store

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func stalenessEvent(t *testing.T, s *Store, workID, kind, payload string) {
	t.Helper()
	actorRef, err := WorkflowActorRef(stepFixtureActor())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	var lastSeq int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(effect_seq),0) FROM effect_receipts`).Scan(&lastSeq); err != nil {
		t.Fatal(err)
	}
	event := Event{EventID: workID + ":" + kind + ":after:" + fmt.Sprint(lastSeq), Kind: kind, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: actorRef, OccurredAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), PayloadVersion: 1, Payload: json.RawMessage(payload)}
	seq, err := appendPreparedEvent(t.Context(), tx, event)
	if err != nil {
		t.Fatal(err)
	}
	var verdict workflowVerdictRecordedPayload
	if err := json.Unmarshal(event.Payload, &verdict); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO workflow_predicate_verdicts(work_id,contract_version,predicate_id,verdict_seq,actor,payload) VALUES(?,?,?,?,?,?) ON CONFLICT(work_id,contract_version,predicate_id) DO UPDATE SET verdict_seq=excluded.verdict_seq,actor=excluded.actor,payload=excluded.payload`, workID, verdict.ContractVersion, verdict.PredicateID, seq, actorRef, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func stalenessVerdictPayload(kind string) string {
	actor := stepFixtureActor()
	raw, _ := json.Marshal(map[string]any{"predicate_id": "predicate:primary", "verdict_kind": kind, "contract_version": 1, "verdict_actor_ref": DeriveWorkflowActorRef(actor.PrincipalRef, actor.ClientRef, actor.AgentRef, actor.SessionRef), "evaluation_evidence": []string{}})
	return string(raw)
}
