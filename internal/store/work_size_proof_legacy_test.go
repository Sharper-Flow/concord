package store

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

// The independent oracle reads migration inputs, never a live worker route.
func g11LegacyWindow(ctx context.Context, q queryer, workID string) (workSizeProofFacts, error) {
	var boundary int64
	if err := q.QueryRowContext(ctx, `SELECT coalesce(max(effect_seq),0) FROM work_size_assessments WHERE work_id=?`, workID).Scan(&boundary); err != nil {
		return workSizeProofFacts{}, err
	}
	rows, err := q.QueryContext(ctx, `SELECT seq,event_id,kind,payload FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND seq>? ORDER BY seq`, workID, boundary)
	if err != nil {
		return workSizeProofFacts{}, err
	}
	type effect struct {
		seq      int64
		id, kind string
		raw      []byte
	}
	var effects []effect
	for rows.Next() {
		var e effect
		if err := rows.Scan(&e.seq, &e.id, &e.kind, &e.raw); err != nil {
			rows.Close()
			return workSizeProofFacts{}, err
		}
		effects = append(effects, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return workSizeProofFacts{}, err
	}
	var state workSizeProofFacts
	for _, e := range effects {
		if e.kind == WorkflowContractApproved || e.kind == WorkflowContractSuperseded {
			state.Accepted, state.Refined = false, false
			continue
		}
		if e.kind != WorkflowActionCompleted {
			continue
		}
		var payload struct {
			Action  string       `json:"action_id"`
			Attempt string       `json:"worker_attempt_id"`
			Review  *ReviewBlock `json:"review"`
		}
		if err := json.Unmarshal(e.raw, &payload); err != nil {
			return state, err
		}
		if payload.Action == "checkpoint_refine" {
			state.Refined = state.Accepted
			continue
		}
		raw, seq := e.raw, e.seq
		if payload.Action == "accept_worker_result" || payload.Action == "accept_worker_evidence" {
			// An old acceptance selects the latest genuine earlier terminal.
			payload.Review = nil
			for _, terminal := range effects {
				if terminal.seq >= e.seq {
					break
				}
				if terminal.kind != "worker.completed" {
					continue
				}
				var report struct {
					Attempt string       `json:"attempt_id"`
					Review  *ReviewBlock `json:"review"`
				}
				if err := json.Unmarshal(terminal.raw, &report); err != nil {
					return state, err
				}
				if report.Attempt == payload.Attempt && report.Review != nil {
					payload.Review, raw, seq = report.Review, terminal.raw, terminal.seq
				}
			}
		} else if payload.Action != "record_review" {
			continue
		}
		if payload.Review == nil {
			continue
		}
		if state.Refined {
			found, err := workSizeReviewFindsInScope(string(raw), seq)
			if err != nil {
				return state, err
			}
			if found {
				state.Proofs = append(state.Proofs, e.id)
			}
		}
		state.Accepted, state.Refined = true, false
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
	if err != nil || !reflect.DeepEqual(live, want) {
		t.Fatalf("prefix %d: live=%+v; oracle=%+v, %v", f.seq, live, want, err)
	}
	g11CheckBackfill(t, f, want)
}

func g11CheckBackfill(t *testing.T, f *g11Fixture, want workSizeProofFacts) {
	t.Helper()
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
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestG11ProofFactsBackfillParity(t *testing.T) {
	f := newG11Fixture(t)
	g11CheckParity(t, f)
	for _, action := range []string{"checkpoint_refine", "record_review", "checkpoint_refine", "record_design", "record_review", "record_review", "checkpoint_refine", "record_review", "checkpoint_refine"} {
		if action == "record_review" {
			f.review(t, reviewBlockFixture())
		} else {
			f.action(t, action)
		}
		g11CheckParity(t, f)
	}
	for _, kind := range []string{WorkflowContractApproved, WorkflowContractSuperseded} {
		f.apply(t, kind, map[string]any{})
		g11CheckParity(t, f)
		f.review(t, reviewBlockFixture())
		g11CheckParity(t, f)
		f.action(t, "checkpoint_refine")
		clean := reviewBlockFixture()
		clean.Disposition, clean.Findings = "ready_for_final_proof", []ReviewFinding{}
		f.review(t, clean)
		g11CheckParity(t, f)
	}
	basis, err := workSizeReviewBasisTx(t.Context(), f.db, "work")
	if err != nil {
		t.Fatal(err)
	}
	f.assess(t, basis)
	g11CheckParity(t, f)
	f.review(t, reviewBlockFixture())
	f.action(t, "checkpoint_refine")
	f.review(t, reviewBlockFixture())
	g11CheckParity(t, f)
}

func TestG11BackfillUsesLegacyReportSelection(t *testing.T) {
	f := newG11Fixture(t)
	report := reviewBlockFixture()
	clean := reviewBlockFixture()
	clean.Disposition, clean.Findings = "ready_for_final_proof", []ReviewFinding{}
	// Historical literals remain confined to the migration fixture.
	for _, e := range []struct {
		kind    string
		payload any
	}{
		{"worker.completed", map[string]any{"attempt_id": "a", "review": report}},
		{WorkflowActionCompleted, map[string]any{"action_id": "accept_worker_result", "worker_attempt_id": "a"}},
		{WorkflowActionCompleted, map[string]any{"action_id": "checkpoint_refine"}},
		{"worker.completed", map[string]any{"attempt_id": "a", "review": clean}},
		{WorkflowActionCompleted, map[string]any{"action_id": "accept_worker_result", "worker_attempt_id": "a"}},
		{WorkflowActionCompleted, map[string]any{"action_id": "checkpoint_refine"}},
		{"worker.completed", map[string]any{"attempt_id": "a", "review": report}},
		{WorkflowActionCompleted, map[string]any{"action_id": "accept_worker_evidence", "worker_attempt_id": "a"}},
		{"worker.completed", map[string]any{"attempt_id": "a", "review": clean}},
	} {
		f.apply(t, e.kind, e.payload)
		want, err := g11LegacyWindow(t.Context(), f.db, "work")
		if err != nil {
			t.Fatal(err)
		}
		g11CheckBackfill(t, f, want)
	}
	want, err := g11LegacyWindow(t.Context(), f.db, "work")
	if err != nil || len(want.Proofs) != 1 {
		t.Fatalf("historical proof selection=%+v %v", want, err)
	}
	if err := backfillWorkSizeProofFacts(t.Context(), f.db); err != nil {
		t.Fatal(err)
	}
	g11Proofs(t, f, want.Proofs...)
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
	g11Proofs(t, f, want.Proofs...)
}
