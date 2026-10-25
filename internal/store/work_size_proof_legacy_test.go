package store

import (
	"context"
	"reflect"
	"testing"
)

// Independent pre-cutover window selection, retained only as a parity oracle.
func g11LegacyWindow(ctx context.Context, q queryer, workID string) (workSizeProofFacts, error) {
	var boundary int64
	if err := q.QueryRowContext(ctx, `SELECT coalesce(max(effect_seq),0) FROM work_size_assessments WHERE work_id=?`, workID).Scan(&boundary); err != nil {
		return workSizeProofFacts{}, err
	}
	rows, err := q.QueryContext(ctx, `SELECT e.seq,e.event_id,e.kind,
coalesce(json_extract(e.payload,'$.action_id'),''),coalesce(json_extract(e.payload,'$.worker_attempt_id'),''),
EXISTS(SELECT 1 FROM worker_attempts a WHERE a.work_id=e.subject_id AND a.attempt_id=json_extract(e.payload,'$.worker_attempt_id') AND a.capability_class='review' AND a.lifecycle_state='completed')
FROM domain_events e WHERE e.subject_type='work_item' AND e.subject_id=? AND e.seq>? AND e.kind IN (?,?,?) ORDER BY e.seq`, workID, boundary, WorkflowActionCompleted, WorkflowContractApproved, WorkflowContractSuperseded)
	if err != nil {
		return workSizeProofFacts{}, err
	}
	type legacyEffect struct {
		seq                       int64
		id, kind, action, attempt string
		review                    bool
	}
	var effects []legacyEffect
	for rows.Next() {
		var e legacyEffect
		if err := rows.Scan(&e.seq, &e.id, &e.kind, &e.action, &e.attempt, &e.review); err != nil {
			rows.Close()
			return workSizeProofFacts{}, err
		}
		effects = append(effects, e)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return workSizeProofFacts{}, err
	}
	if err := rows.Close(); err != nil {
		return workSizeProofFacts{}, err
	}
	var state workSizeProofFacts
	for _, e := range effects {
		switch {
		case e.kind != WorkflowActionCompleted:
			state.Accepted, state.Refined = false, false
		case e.action == "checkpoint_refine":
			state.Refined = state.Accepted
		case e.review && (e.action == "accept_worker_evidence" || e.action == "accept_worker_result"):
			if state.Refined {
				var seq int64
				var raw string
				if err := q.QueryRowContext(ctx, `SELECT seq,payload FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.attempt_id')=? AND seq<? ORDER BY seq DESC LIMIT 1`, workID, WorkerCompleted, e.attempt, e.seq).Scan(&seq, &raw); err != nil {
					return state, err
				}
				found, err := workSizeReviewFindsInScope(raw, seq)
				if err != nil {
					return state, err
				}
				if found {
					state.Proofs = append(state.Proofs, e.id)
				}
			}
			state.Accepted, state.Refined = true, false
		}
	}
	return state, nil
}

func g11CheckParity(t *testing.T, f *g11Fixture) {
	t.Helper()
	want, err := g11LegacyWindow(t.Context(), f.db, "work")
	if err != nil {
		t.Fatal(err)
	}
	live, err := readWorkSizeProofFacts(t.Context(), f.db, "work")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(live, want) {
		t.Fatalf("prefix %d: live=%+v; legacy=%+v", f.seq, live, want)
	}
	// Backfill must use the caller's transaction and retain its guard. A single
	// connection pool makes any accidental Store-backed nested read deadlock.
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM work_size_proof_facts`); err != nil {
		t.Fatal(err)
	}
	if err := backfillWorkSizeProofFacts(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	backfilled, err := readWorkSizeProofFacts(t.Context(), tx, "work")
	if err != nil {
		t.Fatal(err)
	}
	var guard int64
	if err := tx.QueryRow(`SELECT active FROM fold_guard`).Scan(&guard); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(backfilled, want) || guard != 1 {
		t.Fatalf("prefix %d: backfill=%+v; want %+v; guard=%d", f.seq, backfilled, want, guard)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestG11ProofFactsBackfillParity(t *testing.T) {
	f := newG11Fixture(t)
	g11CheckParity(t, f)
	effects := []struct {
		kind    string
		payload any
	}{
		{WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "checkpoint_refine"}},
		{WorkerCompleted, WorkerCompletedPayload{AttemptID: "a", Review: &WorkerReviewBlock{Disposition: "revision_required", Findings: []WorkerReviewFinding{{Severity: "P1"}}}}},
		{WorkerCompleted, WorkerCompletedPayload{AttemptID: "b", Review: &WorkerReviewBlock{Findings: []WorkerReviewFinding{{Severity: "P0", Oracle: &WorkerOracleFinding{Classification: OracleClassificationDeliveryBlocker}}}}}},
		{WorkerCompleted, WorkerCompletedPayload{AttemptID: "clean", Review: &WorkerReviewBlock{Disposition: "clean"}}},
		{WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "accept_worker_result", WorkerAttemptID: "a"}},
		{WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "checkpoint_refine"}},
		{WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "record_design"}},
		{WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "accept_worker_evidence", WorkerAttemptID: "b"}},
		{WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "accept_worker_result", WorkerAttemptID: "a"}},
		{WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "checkpoint_refine"}},
		{WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "accept_worker_result", WorkerAttemptID: "a"}},
		{WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "checkpoint_refine"}},
		{WorkflowContractApproved, map[string]any{}},
		{WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "accept_worker_result", WorkerAttemptID: "a"}},
		{WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "checkpoint_refine"}},
		{WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "accept_worker_result", WorkerAttemptID: "clean"}},
		{WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "accept_worker_result", WorkerAttemptID: "b"}},
		{WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "checkpoint_refine"}},
		{WorkflowContractSuperseded, map[string]any{}},
		{WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "accept_worker_evidence", WorkerAttemptID: "b"}},
		{WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "checkpoint_refine"}},
		{WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "accept_worker_result", WorkerAttemptID: "a"}},
	}
	for _, e := range effects {
		f.apply(t, e.kind, e.payload)
		g11CheckParity(t, f)
	}
	basis, err := workSizeReviewBasisTx(t.Context(), f.db, "work")
	if err != nil {
		t.Fatal(err)
	}
	f.assess(t, basis)
	g11CheckParity(t, f)
	f.action(t, "accept_worker_result", "a")
	g11CheckParity(t, f)
	f.action(t, "checkpoint_refine", "")
	g11CheckParity(t, f)
	f.action(t, "accept_worker_result", "b")
	g11CheckParity(t, f)
}

func TestG11BackfillUsesLegacyReportSelection(t *testing.T) {
	f := newG11Fixture(t)
	f.report(t, "a")
	f.action(t, "accept_worker_result", "a")
	f.action(t, "checkpoint_refine", "")
	proof := f.action(t, "accept_worker_result", "a")
	// A migration uses the exact legacy log selection, not a convenient current
	// owner substitution. This divergence is not an admitted live terminal edit.
	if _, err := f.db.Exec(`UPDATE worker_attempts SET terminal_payload='{}'; DELETE FROM work_size_proof_facts`); err != nil {
		t.Fatal(err)
	}
	if err := backfillWorkSizeProofFacts(t.Context(), f.db); err != nil {
		t.Fatal(err)
	}
	g11Proofs(t, f, proof.EventID)
	// A failed migration remains entirely within the caller's rollback scope.
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM work_size_proof_facts; ALTER TABLE domain_events RENAME TO hidden_domain_events`); err != nil {
		t.Fatal(err)
	}
	if err := backfillWorkSizeProofFacts(t.Context(), tx); err == nil {
		t.Fatal("backfill without legacy source succeeded")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	g11Proofs(t, f, proof.EventID)
}
