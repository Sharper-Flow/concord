package store

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// This fixture exercises the maintaining seam without unrelated workflow
// admission. Assessments use the production fold, including basis validation.
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
CREATE TABLE worker_attempts(work_id TEXT,attempt_id TEXT,capability_class TEXT,lifecycle_state TEXT,terminal_kind TEXT,terminal_seq INTEGER,terminal_payload TEXT,PRIMARY KEY(work_id,attempt_id));
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
	switch kind {
	case WorkerCompleted:
		p := payload.(WorkerCompletedPayload)
		_, err = tx.Exec(`INSERT INTO worker_attempts VALUES(?,?,'review','completed',?,?,?)`, e.SubjectID, p.AttemptID, WorkerCompleted, e.Seq, string(e.Payload))
	case WorkSized:
		err = foldWorkSized(t.Context(), tx, e)
	default:
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

func (f *g11Fixture) action(t *testing.T, action, attempt string) Event {
	t.Helper()
	return f.apply(t, WorkflowActionCompleted, workflowActionCompletedPayload{ActionID: action, WorkerAttemptID: attempt})
}

func (f *g11Fixture) report(t *testing.T, attempt string) Event {
	t.Helper()
	return f.apply(t, WorkerCompleted, WorkerCompletedPayload{AttemptID: attempt, Review: &WorkerReviewBlock{Disposition: "correction_required", Findings: []WorkerReviewFinding{{Severity: "P2"}}}})
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
	f.report(t, "a")
	f.action(t, "accept_worker_evidence", "a")
	f.action(t, "checkpoint_refine", "")
	f.report(t, "b")
	proof := f.action(t, "accept_worker_result", "b")
	if _, err := f.db.Exec(`ALTER TABLE domain_events RENAME TO hidden_domain_events`); err != nil {
		t.Fatal(err)
	}
	f.log = false
	g11Proofs(t, f, proof.EventID)
	// Every maintaining effect and the assessment fold also stay log-blind.
	f.apply(t, WorkflowContractApproved, map[string]any{})
	g11Proofs(t, f, proof.EventID)
	f.action(t, "accept_worker_result", "a")
	f.action(t, "checkpoint_refine", "")
	second := f.action(t, "accept_worker_evidence", "b")
	g11Proofs(t, f, proof.EventID, second.EventID)
	f.apply(t, WorkflowContractSuperseded, map[string]any{})
	f.assess(t, []string{proof.EventID, second.EventID})
	state, err := readWorkSizeProofFacts(t.Context(), f.db, "work")
	if err != nil || state.Accepted || state.Refined || len(state.Proofs) != 0 {
		t.Fatalf("assessment reset=%+v %v", state, err)
	}
}

func TestG11AcceptedReportLogBlind(t *testing.T) {
	f := newG11Fixture(t)
	terminal := f.report(t, "review")
	if _, err := f.db.Exec(`ALTER TABLE domain_events RENAME TO hidden_domain_events`); err != nil {
		t.Fatal(err)
	}
	got, err := workSizeReviewFindsInScopeTx(t.Context(), f.db, "work", "review", terminal.Seq+1)
	if err != nil || !got {
		t.Fatalf("report=%v %v", got, err)
	}
	// The strict predecessor bound remains admission's bound, not a replay API.
	if _, err := workSizeReviewFindsInScopeTx(t.Context(), f.db, "work", "review", terminal.Seq); err == nil {
		t.Fatal("accepted a terminal at the acceptance sequence")
	}
	if _, err := workSizeReviewFindsInScopeTx(t.Context(), f.db, "other", "review", terminal.Seq+1); err == nil {
		t.Fatal("read another work's report")
	}
}

func TestG11ContractAndAssessmentReset(t *testing.T) {
	for _, kind := range []string{WorkflowContractApproved, WorkflowContractSuperseded} {
		t.Run(kind, func(t *testing.T) {
			f := newG11Fixture(t)
			f.report(t, "a")
			f.action(t, "checkpoint_refine", "")
			f.action(t, "accept_worker_result", "a")
			g11Proofs(t, f)
			f.action(t, "checkpoint_refine", "")
			proof := f.action(t, "accept_worker_result", "a")
			f.action(t, "checkpoint_refine", "")
			f.apply(t, kind, map[string]any{})
			f.action(t, "accept_worker_result", "a")
			g11Proofs(t, f, proof.EventID)
			f.action(t, "checkpoint_refine", "")
			proof2 := f.action(t, "accept_worker_evidence", "a")
			basis := []string{proof.EventID, proof2.EventID}
			// A mismatched assessment refuses before clearing the consumed window.
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
			if _, err := f.db.Exec(`UPDATE work_size_assessments SET size_class='larger' WHERE work_id='work'`); err != nil {
				t.Fatal(err)
			}
			f.action(t, "checkpoint_refine", "")
			f.action(t, "accept_worker_result", "a")
			g11Proofs(t, f)
			got, err := workSizeReviewBasisTx(t.Context(), f.db, "work")
			if err != nil || !reflect.DeepEqual(got, basis) {
				t.Fatalf("recorded basis=%v %v; want %v", got, err, basis)
			}
			f.action(t, "checkpoint_refine", "")
			proof3 := f.action(t, "accept_worker_result", "a")
			g11Proofs(t, f, proof3.EventID)
		})
	}
}

func TestG11RepeatedReviewAndNonReview(t *testing.T) {
	f := newG11Fixture(t)
	f.report(t, "a")
	f.report(t, "b")
	f.report(t, "implementation")
	if _, err := f.db.Exec(`UPDATE worker_attempts SET capability_class='implementation' WHERE attempt_id='implementation'`); err != nil {
		t.Fatal(err)
	}
	f.action(t, "accept_worker_result", "a")
	f.action(t, "checkpoint_refine", "")
	f.action(t, "accept_worker_result", "implementation")
	b := f.action(t, "accept_worker_evidence", "b")
	f.action(t, "accept_worker_result", "a")
	g11Proofs(t, f, b.EventID)
	f.action(t, "checkpoint_refine", "")
	a := f.action(t, "accept_worker_result", "a")
	g11Proofs(t, f, b.EventID, a.EventID)
	if _, err := f.db.Exec(`UPDATE work_size_assessments SET size_class='small' WHERE work_id='work'`); err != nil {
		t.Fatal(err)
	}
	g11Proofs(t, f)
	if _, err := f.db.Exec(`UPDATE work_size_assessments SET size_class='larger' WHERE work_id='work'`); err != nil {
		t.Fatal(err)
	}
	g11Proofs(t, f, b.EventID, a.EventID)
}

func TestG11NonLargerReportMaintenance(t *testing.T) {
	for _, size := range []WorkSize{WorkSizeSmall, WorkSizeCrucial} {
		t.Run(string(size), func(t *testing.T) {
			f := newG11Fixture(t)
			if _, err := f.db.Exec(`UPDATE work_size_assessments SET size_class=? WHERE work_id='work'`, size); err != nil {
				t.Fatal(err)
			}
			f.report(t, "a")
			f.action(t, "accept_worker_result", "a")
			f.action(t, "checkpoint_refine", "")
			proof := f.action(t, "accept_worker_evidence", "a")
			g11Proofs(t, f)
			g11CheckParity(t, f)
			if _, err := f.db.Exec(`UPDATE work_size_assessments SET size_class='larger' WHERE work_id='work'`); err != nil {
				t.Fatal(err)
			}
			g11Proofs(t, f, proof.EventID)
		})
	}
}

func TestG11CorruptNonLargerReportRefusesWithoutReset(t *testing.T) {
	f := newG11Fixture(t)
	if _, err := f.db.Exec(`UPDATE work_size_assessments SET size_class='small' WHERE work_id='work'`); err != nil {
		t.Fatal(err)
	}
	f.report(t, "a")
	f.action(t, "accept_worker_result", "a")
	f.action(t, "checkpoint_refine", "")
	// JSON-valid but type-invalid bytes require corruption or an out-of-band
	// write: the terminal producer's registered decoder refuses them.
	e := Event{Kind: WorkerCompleted, PayloadVersion: 2, SubjectType: SubjectWorkItem, SubjectID: "work", Payload: []byte(`{"attempt_id":"a","review":42}`)}
	var report WorkerCompletedPayload
	if err := decodeClosedWorkerPayload(e, &report); err == nil {
		t.Fatal("terminal decoder admitted a malformed review")
	}
	if _, err := f.db.Exec(`UPDATE worker_attempts SET terminal_payload=? WHERE attempt_id='a'`, string(e.Payload)); err != nil {
		t.Fatal(err)
	}
	// The effective-size read still skips proof derivation, but the maintaining
	// seam refuses corrupt data rather than discarding a potentially needed proof.
	g11Proofs(t, f)
	before, err := readWorkSizeProofFacts(t.Context(), f.db, "work")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	e.Kind, e.Seq, e.EventID = WorkflowActionCompleted, f.seq+1, "g11:corrupt-accept"
	e.Payload = mustJSONValue(workflowActionCompletedPayload{ActionID: "accept_worker_evidence", WorkerAttemptID: "a"})
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
			for _, disposition := range []string{"correction_required", "revision_required", "clean"} {
				for _, resolved := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/%s/resolved=%v", severity, classification, disposition, resolved), func(t *testing.T) {
						finding := WorkerReviewFinding{FindingID: "reported-id", Severity: severity}
						if classification != "" {
							finding.Oracle = &WorkerOracleFinding{Classification: classification}
						}
						p := WorkerCompletedPayload{ContextFindings: make([]WorkerContextFinding, 2), Review: &WorkerReviewBlock{Disposition: disposition, Findings: []WorkerReviewFinding{finding}}}
						if resolved {
							p.Review.ResolvedFindings = []WorkerOracleResolution{{FindingID: workerRankedFindingID(90, 2, 0)}}
						}
						want := (severity == "P0" || severity == "P1" || severity == "P2") && ((classification == "" && disposition != "clean") || (classification != "" && classification != OracleClassificationFollowUp && !resolved))
						got, err := workSizeReviewFindsInScope(string(mustJSONValue(p)), 90)
						if err != nil || got != want {
							t.Fatalf("qualifies=%v %v; want %v", got, err, want)
						}
					})
				}
			}
		}
	}
	// A continuation resolves by its original identity, not its report slot.
	p := WorkerCompletedPayload{Review: &WorkerReviewBlock{Findings: []WorkerReviewFinding{{Severity: "P1", Oracle: &WorkerOracleFinding{Classification: OracleClassificationDeliveryBlocker, ContinuesFindingID: "finding:earlier"}}}, ResolvedFindings: []WorkerOracleResolution{{FindingID: "finding:earlier"}}}}
	got, err := workSizeReviewFindsInScope(string(mustJSONValue(p)), 90)
	if err != nil || got {
		t.Fatalf("resolved continuation=%v %v", got, err)
	}
	p.Review.ResolvedFindings[0].FindingID = "reported-id"
	got, err = workSizeReviewFindsInScope(string(mustJSONValue(p)), 90)
	if err != nil || !got {
		t.Fatalf("unresolved continuation=%v %v", got, err)
	}
	if _, err := workSizeReviewFindsInScope("malformed", 90); err == nil {
		t.Fatal("malformed report admitted")
	}
}

func TestG11ProofFactsGuardAndRollback(t *testing.T) {
	f := newG11Fixture(t)
	if _, err := f.db.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{`INSERT INTO work_size_proof_facts VALUES('other','{}')`, `UPDATE work_size_proof_facts SET state='{}'`, `DELETE FROM work_size_proof_facts`} {
		if _, err := f.db.Exec(statement); err == nil || !strings.Contains(err.Error(), "fold-only") {
			t.Fatalf("unguarded %s: %v", statement, err)
		}
	}
	if _, err := f.db.Exec(`INSERT INTO fold_guard VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	f.report(t, "a")
	f.action(t, "accept_worker_result", "a")
	f.action(t, "checkpoint_refine", "")
	before, err := readWorkSizeProofFacts(t.Context(), f.db, "work")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	e := Event{Kind: WorkflowContractSuperseded, SubjectID: "work", Seq: 100}
	if err := advanceWorkSizeProofFactsTx(t.Context(), tx, e); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	after, err := readWorkSizeProofFacts(t.Context(), f.db, "work")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("rollback=%+v %v; want %+v", after, err, before)
	}
	if _, err := f.db.Exec(`DROP TABLE work_size_proof_facts`); err != nil {
		t.Fatal(err)
	}
	if _, err := deriveWorkSizeProofsTx(t.Context(), f.db, "work"); err == nil {
		t.Fatal("missing facts silently reconstructed from log")
	}
}
