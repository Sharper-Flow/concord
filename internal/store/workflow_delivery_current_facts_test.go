package store

import (
	"encoding/json"
	"testing"
)

func TestWorkflowAcceptedDeliveryCurrentFactsWithoutLog(t *testing.T) {
	const work = "accepted-delivery-current-facts"
	f := qBuildAcceptance(t, work, "", false)
	defer f.store.Close()
	s := f.store
	var attempt string
	var dispatched int64
	if err := s.db.QueryRow(`SELECT attempt_id,dispatch_seq FROM worker_attempts WHERE work_id=? ORDER BY dispatch_seq DESC LIMIT 1`, work).Scan(&attempt, &dispatched); err != nil {
		t.Fatal(err)
	}
	seq, accepted, err := workflowAcceptedWorkerDelivery(t.Context(), s.db, work, int64(1)<<62, "test")
	if err != nil || !accepted || seq != dispatched {
		t.Fatalf("accepted fixture = %d,%v,%v; want dispatch %d", seq, accepted, err, dispatched)
	}
	assertWorkerFactsLogBlind(t, s, map[string]func() (any, error){
		"accepted-delivery": func() (any, error) {
			seq, accepted, err := workflowAcceptedWorkerDelivery(t.Context(), s.db, work, int64(1)<<62, "test")
			return struct {
				Seq      int64
				Accepted bool
			}{seq, accepted}, err
		},
		"before-acceptance": func() (any, error) {
			seq, accepted, err := workflowAcceptedWorkerDelivery(t.Context(), s.db, work, dispatched, "test")
			return struct {
				Seq      int64
				Accepted bool
			}{seq, accepted}, err
		},
		"dispatch-source": func() (any, error) { return workflowAttemptDispatchSequence(t.Context(), s.db, work, attempt, "test") },
	})
}

func TestWorkflowCurrentCorrectionRequestPreservesVerdictWindow(t *testing.T) {
	const work = "current-correction-consumes-verdict"
	f := qBuildAcceptance(t, work, "", false)
	defer f.store.Close()
	s := f.store
	var predicate string
	if err := s.db.QueryRow(`SELECT predicate_id FROM workflow_contract_predicates WHERE work_id=? AND contract_version=1 AND ordinal=0`, work).Scan(&predicate); err != nil {
		t.Fatal(err)
	}
	var seq int64
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(effect_seq),0)+1 FROM effect_receipts`).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(workflowVerdictRecordedPayload{ContractVersion: 1, PredicateID: predicate, VerdictKind: "insufficient_evidence"})
	if err != nil {
		t.Fatal(err)
	}
	// Model a retained unhealthy judgment using its current descriptor. Its
	// correction baseline belongs to the judgment, not a later request.
	if _, err := s.db.Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO workflow_predicate_verdicts(work_id,contract_version,predicate_id,verdict_seq,actor,payload) VALUES(?,1,?,?,'actor:test',?); DELETE FROM fold_guard`, work, predicate, seq, string(raw)); err != nil {
		t.Fatal(err)
	}
	definition := mustBuiltinDefinition(t, "workflow.implementation").Definition
	read := func() (any, error) {
		state, err := workflowCorrectionVerdictState(t.Context(), s.db, work, definition, "acceptance", "test")
		if err != nil {
			return nil, err
		}
		return workflowVerdictCorrectionFromState(t.Context(), s.db, work, definition, "acceptance", "test", state)
	}
	want, err := read()
	if err != nil || want == (*WorkflowCorrectionContext)(nil) {
		t.Fatalf("current unhealthy judgment has no correction: %+v, %v", want, err)
	}
	assertWorkerFactsLogBlind(t, s, map[string]func() (any, error){"verdict-correction": read})
	if _, err := s.db.Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO workflow_phase_exits(work_id,action_id,exit_seq) VALUES(?,'request_correction',?); DELETE FROM fold_guard`, work, seq+1); err != nil {
		t.Fatal(err)
	}
	got, err := read()
	if err != nil || got == (*WorkflowCorrectionContext)(nil) {
		t.Fatalf("later correction changed the recorded judgment window: %+v, %v", got, err)
	}
}

func TestWorkflowVerdictWriterSnapshotsPriorCorrection(t *testing.T) {
	const work = "verdict-correction-snapshot"
	f := qBuildAcceptance(t, work, "", false)
	defer f.store.Close()
	s := f.store
	actor, err := WorkflowActorRef(f.owner)
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"action_id":"request_correction","step_id":"acceptance"}`
	result, err := s.db.Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES('prior-correction','workflow.action_completed','work_item',?,?,'2026-01-01T00:00:00Z',1,?)`, work, actor, raw)
	if err != nil {
		t.Fatal(err)
	}
	prior, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO workflow_phase_exits VALUES(?,'request_correction',?); DELETE FROM fold_guard`, work, prior); err != nil {
		t.Fatal(err)
	}
	if err := runVerdictActionAs(t, s, work, "record_verdict", qAssociationPayload(t, f), 0, f.owner); err != nil {
		t.Fatal(err)
	}
	read := func() (any, error) {
		var seq int64
		err := s.db.QueryRow(`SELECT prior_correction_seq FROM workflow_predicate_verdicts WHERE work_id=? ORDER BY verdict_seq DESC LIMIT 1`, work).Scan(&seq)
		return seq, err
	}
	if got, err := read(); err != nil || got != prior {
		t.Fatalf("verdict correction snapshot = %v,%v; want %d", got, err, prior)
	}
	repopulateWorkflowCurrentFactsForTest(t, s)
	if got, err := read(); err != nil || got != prior {
		t.Fatalf("backfilled correction snapshot = %v,%v; want %d", got, err, prior)
	}
	assertWorkerFactsLogBlind(t, s, map[string]func() (any, error){"prior-correction-snapshot": read})
}

func TestWorkflowArtifactProductionCurrentFactsWithoutLog(t *testing.T) {
	const work = "artifact-production-current-facts"
	f := qBuildAcceptance(t, work, "artifact:current-production", true)
	defer f.store.Close()
	s := f.store
	var target string
	var assertion, verdict int64
	if err := s.db.QueryRow(`SELECT json_extract(payload,'$.step_id'),assertion_seq FROM workflow_delivery_state WHERE work_id=?`, work).Scan(&target, &assertion); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT MAX(verdict_seq) FROM workflow_predicate_verdicts WHERE work_id=?`, work).Scan(&verdict); err != nil {
		t.Fatal(err)
	}
	definition := mustBuiltinDefinition(t, "workflow.implementation").Definition
	read := func(through int64) (any, error) {
		return workflowArtifactProductionFrontier(t.Context(), s.db, work, definition, target, 0, through, "test")
	}
	if got, err := read(0); err != nil || got != assertion {
		t.Fatalf("production fixture = %v,%v; want %d", got, err, assertion)
	}
	repopulateWorkflowCurrentFactsForTest(t, s)
	if got, err := read(verdict); err != nil || got != assertion {
		t.Fatalf("backfill lost the verdict production snapshot: %v,%v; want %d", got, err, assertion)
	}
	assertWorkerFactsLogBlind(t, s, map[string]func() (any, error){
		"current-artifact-production":      func() (any, error) { return read(0) },
		"production-before-latest-verdict": func() (any, error) { return read(verdict) },
	})
	// A later producer update changes current freshness, not the ground on
	// which the existing verdict was made.
	if _, err := s.db.Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE workflow_artifact_actions SET production_seq=? WHERE work_id=? AND step_id=?; DELETE FROM fold_guard`, verdict+10, work, target); err != nil {
		t.Fatal(err)
	}
	if got, err := read(0); err != nil || got != verdict+10 {
		t.Fatalf("later current production = %v,%v; want %d", got, err, verdict+10)
	}
	if got, err := read(verdict); err != nil || got != assertion {
		t.Fatalf("verdict borrowed a later production: %v,%v; want %d", got, err, assertion)
	}
}
