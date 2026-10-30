package store

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

type g11Fixture struct {
	db  *sql.DB
	seq int64
	log bool
}

func newG11Fixture(t *testing.T) *g11Fixture {
	t.Helper()
	db := correctionHistoryDatabase(t)
	if _, err := db.Exec(`CREATE TABLE work_items(id TEXT PRIMARY KEY,kind TEXT,lifecycle TEXT);
INSERT INTO work_items VALUES('work','task','in_progress'),('other','task','in_progress');
CREATE TABLE fold_guard(active INTEGER); INSERT INTO fold_guard VALUES(1);
CREATE TABLE work_size_assessments(event_id TEXT,work_id TEXT,sequence INTEGER,model_version INTEGER,size_class TEXT,inputs_json TEXT,footprint_json TEXT,recorded_at TEXT,effect_seq INTEGER,review_basis TEXT);
` + workSizeProofFactsDDL); err != nil {
		t.Fatal(err)
	}
	f := &g11Fixture{db: db, log: true}
	f.assess(t, nil)
	return f
}

func (f *g11Fixture) apply(t *testing.T, kind string, payload any) Event {
	t.Helper()
	f.seq++
	e := Event{Seq: f.seq, EventID: fmt.Sprintf("g11:%04d", f.seq), Kind: kind, SubjectType: SubjectWorkItem, SubjectID: "work", Payload: mustJSONValue(payload), OccurredAt: time.Unix(100, 0)}
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if f.log {
		if _, err := tx.Exec(`INSERT INTO domain_events VALUES(?,?,?,?,?,?)`, e.Seq, e.SubjectType, e.SubjectID, e.Kind, e.Payload, e.EventID); err != nil {
			t.Fatal(err)
		}
	}
	if kind == WorkSized {
		err = foldWorkSized(t.Context(), tx, e)
	} else {
		err = advanceWorkSizeProofFactsTx(t.Context(), tx, e)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return e
}

func (f *g11Fixture) assess(t *testing.T, basis []string) {
	t.Helper()
	in := workSizeInputsFixture()
	in.Declared.EstimatedProductionLines = 61
	size, err := DecideWorkSize(2, in, basis)
	if err != nil {
		t.Fatal(err)
	}
	f.apply(t, WorkSized, workSizedPayload{ModelVersion: 2, Size: size, Inputs: in, Footprint: workSizeFootprintFixture(), ReviewBasis: basis})
}

func (f *g11Fixture) action(t *testing.T, action string) {
	t.Helper()
	f.apply(t, WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: action})
}

func (f *g11Fixture) review(t *testing.T, report *ReviewBlock) Event {
	t.Helper()
	return f.apply(t, WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: "record_review", Review: report})
}

func g11Proofs(t *testing.T, f *g11Fixture, want ...string) {
	t.Helper()
	got, err := deriveWorkSizeProofsTx(t.Context(), f.db, "work")
	if err != nil || !reflect.DeepEqual(sliceOrEmpty(got), sliceOrEmpty(want)) {
		t.Fatalf("proofs=%v err=%v; want %v", got, err, want)
	}
}

func TestG11ProofWindowLogBlind(t *testing.T) {
	f := newG11Fixture(t)
	f.review(t, reviewBlockFixture())
	f.action(t, "checkpoint_refine")
	proof := f.review(t, reviewBlockFixture())
	if _, err := f.db.Exec(`ALTER TABLE domain_events RENAME TO hidden_domain_events`); err != nil {
		t.Fatal(err)
	}
	f.log = false
	g11Proofs(t, f, proof.EventID)
	f.apply(t, WorkflowContractApproved, map[string]any{})
	g11Proofs(t, f, proof.EventID)
	f.review(t, reviewBlockFixture())
	f.action(t, "checkpoint_refine")
	second := f.review(t, reviewBlockFixture())
	g11Proofs(t, f, proof.EventID, second.EventID)
	f.apply(t, WorkflowContractSuperseded, map[string]any{})
	f.assess(t, []string{proof.EventID, second.EventID})
	state, err := readWorkSizeProofFacts(t.Context(), f.db, "work")
	if err != nil || state.Accepted || state.Refined || len(state.Proofs) != 0 {
		t.Fatalf("assessment reset=%+v %v", state, err)
	}
}

func TestG11ContractAndAssessmentReset(t *testing.T) {
	for _, kind := range []string{WorkflowContractApproved, WorkflowContractSuperseded} {
		t.Run(kind, func(t *testing.T) {
			f := newG11Fixture(t)
			f.action(t, "checkpoint_refine")
			f.review(t, reviewBlockFixture())
			g11Proofs(t, f)
			f.action(t, "checkpoint_refine")
			proof := f.review(t, reviewBlockFixture())
			f.action(t, "checkpoint_refine")
			f.apply(t, kind, map[string]any{})
			f.review(t, reviewBlockFixture())
			g11Proofs(t, f, proof.EventID)
			f.action(t, "checkpoint_refine")
			proof2 := f.review(t, reviewBlockFixture())
			basis := []string{proof.EventID, proof2.EventID}
			tx, err := f.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			in := workSizeInputsFixture()
			in.Declared.EstimatedProductionLines = 61
			bad := Event{Kind: WorkSized, SubjectType: SubjectWorkItem, SubjectID: "work", Seq: f.seq + 1, Payload: mustJSONValue(workSizedPayload{ModelVersion: 2, Size: WorkSizeLarger, Inputs: in, Footprint: workSizeFootprintFixture()})}
			requireFailureKind(t, foldWorkSized(t.Context(), tx, bad), KindInvariantViolation, "missing basis")
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			g11Proofs(t, f, basis...)
			f.assess(t, basis)
			memberMigrationExec(t, f.db, `UPDATE work_size_assessments SET size_class='larger' WHERE work_id='work'`)
			f.action(t, "checkpoint_refine")
			f.review(t, reviewBlockFixture())
			g11Proofs(t, f)
			got, err := workSizeReviewBasisTx(t.Context(), f.db, "work")
			if err != nil || !reflect.DeepEqual(got, basis) {
				t.Fatalf("recorded basis=%v %v; want %v", got, err, basis)
			}
			f.action(t, "checkpoint_refine")
			proof3 := f.review(t, reviewBlockFixture())
			g11Proofs(t, f, proof3.EventID)
		})
	}
}

func TestG11RepeatedReviewAndNonReview(t *testing.T) {
	f := newG11Fixture(t)
	f.review(t, reviewBlockFixture())
	f.action(t, "checkpoint_refine")
	f.action(t, "record_delivery")
	f.action(t, "record_design")
	g11Proofs(t, f)
	b := f.review(t, reviewBlockFixture())
	f.review(t, reviewBlockFixture())
	g11Proofs(t, f, b.EventID)
	f.action(t, "checkpoint_refine")
	a := f.review(t, reviewBlockFixture())
	g11Proofs(t, f, b.EventID, a.EventID)
	memberMigrationExec(t, f.db, `UPDATE work_size_assessments SET size_class='small' WHERE work_id='work'`)
	g11Proofs(t, f)
	memberMigrationExec(t, f.db, `UPDATE work_size_assessments SET size_class='larger' WHERE work_id='work'`)
	g11Proofs(t, f, b.EventID, a.EventID)
}

func TestG11NonLargerReportMaintenance(t *testing.T) {
	for _, size := range []WorkSize{WorkSizeSmall, WorkSizeCrucial} {
		t.Run(string(size), func(t *testing.T) {
			f := newG11Fixture(t)
			memberMigrationExec(t, f.db, `UPDATE work_size_assessments SET size_class=? WHERE work_id='work'`, size)
			f.review(t, reviewBlockFixture())
			f.action(t, "checkpoint_refine")
			proof := f.review(t, reviewBlockFixture())
			g11Proofs(t, f)
			g11CheckParity(t, f)
			memberMigrationExec(t, f.db, `UPDATE work_size_assessments SET size_class='larger' WHERE work_id='work'`)
			g11Proofs(t, f, proof.EventID)
		})
	}
}

func TestG11CorruptNonLargerReportRefusesWithoutReset(t *testing.T) {
	f := newG11Fixture(t)
	memberMigrationExec(t, f.db, `UPDATE work_size_assessments SET size_class='small' WHERE work_id='work'`)
	f.review(t, reviewBlockFixture())
	f.action(t, "checkpoint_refine")
	e := Event{Kind: WorkflowActionCompleted, SubjectType: SubjectWorkItem, SubjectID: "work", Seq: f.seq + 1, EventID: "g11:corrupt-review", Payload: []byte(`{"action_id":"record_review","review":42}`)}
	if _, err := decodeRecordedReview(e.Payload); err == nil {
		t.Fatal("record_review decoder admitted a malformed review")
	}
	g11Proofs(t, f)
	before, err := readWorkSizeProofFacts(t.Context(), f.db, "work")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	requireFailureKind(t, advanceWorkSizeProofFactsTx(t.Context(), tx, e), KindInvariantViolation, "corrupt non-larger report")
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	after, err := readWorkSizeProofFacts(t.Context(), f.db, "work")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("corrupt report changed window: %+v %v; want %+v", after, err, before)
	}
}

func TestG11FindingSeverityTies(t *testing.T) {
	for _, severity := range []string{"P0", "P1", "P2", "P3", "high", "medium"} {
		for _, classification := range []string{"", OracleClassificationDeliveryBlocker, OracleClassificationUncoveredCase, OracleClassificationOracleDefect, OracleClassificationFollowUp} {
			for _, disposition := range []string{"correction_required", "revision_required", "ready_for_final_proof"} {
				for _, resolved := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/%s/resolved=%v", severity, classification, disposition, resolved), func(t *testing.T) {
						finding := ReviewFinding{FindingID: "reported-id", Severity: severity}
						if classification != "" {
							finding.Oracle = &ReviewOracleFinding{Classification: classification}
						}
						report := &ReviewBlock{Disposition: disposition, Findings: []ReviewFinding{finding}}
						if resolved {
							report.ResolvedFindings = []ReviewResolution{{FindingID: "finding:90:0"}}
						}
						want := (severity == "P0" || severity == "P1" || severity == "P2") && ((classification == "" && disposition != "ready_for_final_proof") || (classification != "" && classification != OracleClassificationFollowUp && !resolved))
						got, err := workSizeReviewFindsInScope(string(mustJSONValue(map[string]any{"review": report})), 90)
						if err != nil || got != want {
							t.Fatalf("qualifies=%v %v; want %v", got, err, want)
						}
					})
				}
			}
		}
	}
	// Historical context findings still offset canonical oracle finding IDs.
	legacy := map[string]any{"context_findings": []any{map[string]any{}, map[string]any{}}, "review": &ReviewBlock{Findings: []ReviewFinding{{Severity: "P1", Oracle: &ReviewOracleFinding{Classification: OracleClassificationDeliveryBlocker}}}, ResolvedFindings: []ReviewResolution{{FindingID: "finding:90:2"}}}}
	if got, err := workSizeReviewFindsInScope(string(mustJSONValue(legacy)), 90); err != nil || got {
		t.Fatalf("legacy offset resolution=%v %v", got, err)
	}
	p := &ReviewBlock{Findings: []ReviewFinding{{Severity: "P1", Oracle: &ReviewOracleFinding{Classification: OracleClassificationDeliveryBlocker, ContinuesFindingID: "finding:earlier"}}}, ResolvedFindings: []ReviewResolution{{FindingID: "finding:earlier"}}}
	got, err := workSizeReviewFindsInScope(string(mustJSONValue(map[string]any{"review": p})), 90)
	if err != nil || got {
		t.Fatalf("resolved continuation=%v %v", got, err)
	}
	p.ResolvedFindings[0].FindingID = "reported-id"
	got, err = workSizeReviewFindsInScope(string(mustJSONValue(map[string]any{"review": p})), 90)
	if err != nil || !got {
		t.Fatalf("unresolved continuation=%v %v", got, err)
	}
	if _, err := workSizeReviewFindsInScope("malformed", 90); err == nil {
		t.Fatal("malformed report admitted")
	}
}

func TestG11ProofFactsGuardAndRollback(t *testing.T) {
	f := newG11Fixture(t)
	memberMigrationExec(t, f.db, `DELETE FROM fold_guard`)
	for _, statement := range []string{`INSERT INTO work_size_proof_facts VALUES('other','{}')`, `UPDATE work_size_proof_facts SET state='{}'`, `DELETE FROM work_size_proof_facts`} {
		if _, err := f.db.Exec(statement); err == nil || !strings.Contains(err.Error(), "fold-only") {
			t.Fatalf("unguarded %s: %v", statement, err)
		}
	}
	memberMigrationExec(t, f.db, `INSERT INTO fold_guard VALUES(1)`)
	f.review(t, reviewBlockFixture())
	f.action(t, "checkpoint_refine")
	before, err := readWorkSizeProofFacts(t.Context(), f.db, "work")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := advanceWorkSizeProofFactsTx(t.Context(), tx, Event{Kind: WorkflowContractSuperseded, SubjectID: "work", Seq: 100}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	after, err := readWorkSizeProofFacts(t.Context(), f.db, "work")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("rollback=%+v %v; want %+v", after, err, before)
	}
	memberMigrationExec(t, f.db, `DROP TABLE work_size_proof_facts`)
	if _, err := deriveWorkSizeProofsTx(t.Context(), f.db, "work"); err == nil {
		t.Fatal("missing facts silently reconstructed from log")
	}
}
