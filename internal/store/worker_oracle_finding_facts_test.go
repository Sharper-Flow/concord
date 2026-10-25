package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func oracleFindingFactsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`CREATE TABLE worker_job_revisions(work_id TEXT,job_id TEXT,revision INTEGER,digest TEXT,recorded_payload TEXT);
CREATE TABLE domain_events(seq INTEGER,event_id TEXT,subject_type TEXT,subject_id TEXT,kind TEXT,payload TEXT);`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE work_items(id TEXT PRIMARY KEY); CREATE TABLE fold_guard(active INTEGER); INSERT INTO fold_guard VALUES(1);` + oracleFindingFactsDDL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO work_items VALUES('work'),('other');
CREATE TABLE worker_attempts(work_id TEXT,attempt_id TEXT,dispatch_payload TEXT,PRIMARY KEY(work_id,attempt_id));`); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestOracleFindingFactsReadWithoutLog(t *testing.T) {
	db := oracleFindingFactsDB(t)
	if _, err := db.Exec(`ALTER TABLE domain_events RENAME TO hidden_events`); err != nil {
		t.Fatal(err)
	}
	lineage, err := readWorkerOracleFindingLineageTx(t.Context(), db, "work")
	if err != nil {
		t.Fatal(err)
	}
	if len(lineage.findings) != 0 || lineage.oracleCapable || len(lineage.retainedReceipts) != 0 {
		t.Fatalf("unexpected empty lineage: %#v", lineage)
	}
}

func oracleFindingFactsEvents(t *testing.T, db *sql.DB) []Event {
	t.Helper()
	oracle := testOracleGraph()
	binding := &WorkerJobBinding{JobID: "job:oracle", Revision: 1, Digest: "digest"}
	raw := mustJSONValue(map[string]any{"acceptance_oracle": oracle})
	if _, err := db.Exec(`INSERT INTO worker_job_revisions VALUES('work',?,?,?,?)`, binding.JobID, binding.Revision, binding.Digest, string(raw)); err != nil {
		t.Fatal(err)
	}
	finding := func(classification, continues string) WorkerReviewFinding {
		return WorkerReviewFinding{Severity: "P1", Detail: "retained detail", Oracle: &WorkerOracleFinding{
			Classification: classification, OwnerID: oracle.Owners[0].OwnerID, FailureFamily: "family",
			PredicateIDs: []string{"predicate:one"}, ControlIDs: []string{"control:immutable"}, CaseIDs: []string{"case:immutable"},
			ContinuesFindingID: continues,
		}}
	}
	receipt := func(ref string) []WorkerReportEvidence {
		return []WorkerReportEvidence{{OracleReceipt: &WorkerOracleReceipt{RunRef: ref, Result: OracleReceiptResultNotRun}}}
	}
	event := func(seq int64, kind string, payload any) Event {
		return Event{Seq: seq, EventID: fmt.Sprintf("effect-%d", seq), SubjectType: SubjectWorkItem, SubjectID: "work", Kind: kind, Payload: mustJSONValue(payload)}
	}
	return []Event{
		event(10, WorkerDispatched, WorkerDispatchedPayload{AttemptID: "legacy", LaneActorRef: "actor:legacy"}),
		event(20, WorkerDispatched, WorkerDispatchedPayload{AttemptID: "oracle", LaneActorRef: "actor:oracle", WorkerJob: binding}),
		event(30, WorkerCompleted, WorkerCompletedPayload{AttemptID: "legacy", Evidence: receipt("receipt:before-review")}),
		event(40, WorkerCompleted, WorkerCompletedPayload{AttemptID: "oracle", WorkerJob: binding, Evidence: receipt("receipt:mint"), ContextFindings: make([]WorkerContextFinding, 2), Review: &WorkerReviewBlock{Findings: []WorkerReviewFinding{
			{Detail: "untyped finding occupies its ordinal"},
			finding(OracleClassificationDeliveryBlocker, ""), finding(OracleClassificationOracleDefect, ""), finding(OracleClassificationFollowUp, ""),
		}}}),
		// Continuation does not replace detail, identity, actor or source oracle.
		event(50, WorkerCompleted, WorkerCompletedPayload{AttemptID: "legacy", Review: &WorkerReviewBlock{Findings: []WorkerReviewFinding{finding(OracleClassificationDeliveryBlocker, "finding:40:3")}, ResolvedFindings: []WorkerOracleResolution{{FindingID: "finding:40:4"}, {FindingID: "finding:missing"}}}}),
		// Unresolvable legacy continuations mint; the migration must not narrow them.
		event(60, WorkerCompleted, WorkerCompletedPayload{AttemptID: "legacy", Review: &WorkerReviewBlock{Findings: []WorkerReviewFinding{finding(OracleClassificationDeliveryBlocker, "finding:missing")}, ResolvedFindings: []WorkerOracleResolution{{FindingID: "finding:40:3"}}}}),
		event(70, WorkerCompleted, WorkerCompletedPayload{AttemptID: "legacy", Evidence: receipt("receipt:again"), Review: &WorkerReviewBlock{Findings: []WorkerReviewFinding{finding(OracleClassificationDeliveryBlocker, "finding:40:3")}, ResolvedFindings: []WorkerOracleResolution{{FindingID: "finding:40:4"}}}}),
		event(80, WorkflowContractSuperseded, map[string]any{}),
		event(90, WorkerCompleted, WorkerCompletedPayload{AttemptID: "legacy", Evidence: receipt("receipt:successor"), Review: &WorkerReviewBlock{Findings: []WorkerReviewFinding{finding(OracleClassificationDeliveryBlocker, "finding:40:5"), finding(OracleClassificationDeliveryBlocker, "")}}}),
	}
}

func insertOracleFindingEffect(t *testing.T, db *sql.DB, event Event) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO domain_events VALUES(?,?,?,?,?,?)`, event.Seq, event.EventID, event.SubjectType, event.SubjectID, event.Kind, string(event.Payload)); err != nil {
		t.Fatal(err)
	}
}

// This is the admitted writer seam, not report admission. Full fold tests use
// real dispatch and completion routes once the integrating migration is wired.
func writeOracleFindingEffect(t *testing.T, db *sql.DB, event Event) {
	t.Helper()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if event.Kind == WorkerDispatched {
		var p WorkerDispatchedPayload
		if err := json.Unmarshal(event.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO worker_attempts VALUES(?,?,?) ON CONFLICT(work_id,attempt_id) DO UPDATE SET dispatch_payload=excluded.dispatch_payload`, event.SubjectID, p.AttemptID, string(event.Payload)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeOracleFindingFactsTx(t.Context(), tx, event); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestOracleFindingFactsMigrationLiveParity(t *testing.T) {
	live := oracleFindingFactsDB(t)
	events := oracleFindingFactsEvents(t, live)
	for index, event := range events {
		insertOracleFindingEffect(t, live, event)
		// The effect is already appended, but admission must still read the
		// previous aggregate, not the effect's own mints or closures.
		prior, err := readWorkerOracleFindingLineageTx(t.Context(), live, "work")
		if err != nil {
			t.Fatal(err)
		}
		if event.Seq == 40 && len(prior.findings) != 0 {
			t.Fatal("completion consumed its own findings before admission")
		}
		writeOracleFindingEffect(t, live, event)
		migrated := oracleFindingFactsDB(t)
		_ = oracleFindingFactsEvents(t, migrated)
		for _, legacy := range events[:index+1] {
			insertOracleFindingEffect(t, migrated, legacy)
		}
		// Same attempt identity on another work must not affect source actors.
		other := events[1]
		other.SubjectID = "other"
		insertOracleFindingEffect(t, migrated, other)
		if err := backfillOracleFindingFacts(t.Context(), migrated); err != nil {
			t.Fatal(err)
		}
		got, err := readWorkerOracleFindingLineageTx(t.Context(), live, "work")
		if err != nil {
			t.Fatal(err)
		}
		want, err := readWorkerOracleFindingLineageTx(t.Context(), migrated, "work")
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("seq %d: migrated=%+v live=%+v err=%v", event.Seq, want, got, err)
		}
		assertOracleFindingLegacySelection(t, event.Seq, got)
		if _, err := migrated.Exec(`ALTER TABLE domain_events RENAME TO hidden_events`); err != nil {
			t.Fatal(err)
		}
		blind, err := readWorkerOracleFindingLineageTx(t.Context(), migrated, "work")
		if err != nil || !reflect.DeepEqual(blind, got) {
			t.Fatalf("seq %d: log-blind lineage diverged: %v", event.Seq, err)
		}
	}
}

func assertOracleFindingLegacySelection(t *testing.T, seq int64, got *workerOracleFindingLineage) {
	t.Helper()
	if got.oracleCapable != (seq >= 20) {
		t.Fatalf("seq %d: oracle-capable flag changed", seq)
	}
	if seq < 40 {
		return
	}
	wantIDs := []string{"finding:40:3", "finding:40:4", "finding:40:5"}
	if seq >= 60 {
		wantIDs = append(wantIDs, "finding:60:0")
	}
	if seq >= 90 {
		wantIDs = append(wantIDs, "finding:90:1")
	}
	if !slices.Equal(got.openOrder, wantIDs) {
		t.Fatalf("seq %d: identity/order=%v want %v", seq, got.openOrder, wantIDs)
	}
	wantOpen := []string{"finding:40:3", "finding:40:4"}
	if seq >= 50 {
		wantOpen = []string{"finding:40:3"}
	}
	if seq >= 60 {
		wantOpen = []string{"finding:60:0"}
	}
	if seq == 80 {
		wantOpen = nil
	}
	if seq >= 90 {
		wantOpen = []string{"finding:90:1"}
	}
	if !slices.Equal(got.openFindingIDs(), wantOpen) {
		t.Fatalf("seq %d: open blockers=%v want %v", seq, got.openFindingIDs(), wantOpen)
	}
	source := got.findings["finding:40:3"]
	if source.SourceActorRef != "actor:oracle" || source.SourceOrdinal != 3 || source.SourceEventSeq != 40 || source.SourceEventID != "effect-40" || source.Detail != "retained detail" || source.DomainID != testOracleGraph().Owners[0].DomainID || !reflect.DeepEqual(source.SourceOracle, testOracleGraph()) {
		t.Fatalf("seq %d: source identity changed: %+v", seq, source)
	}
	if got.findings["finding:40:4"].Closed != (seq >= 50) || (seq >= 50 && got.findings["finding:40:4"].ClosureKind != oracleClosureOracleDefect) {
		t.Fatalf("seq %d: oracle-defect closure changed", seq)
	}
	if source.Closed != (seq >= 60) || (seq >= 60 && source.ClosureKind != oracleClosureResolution) {
		t.Fatalf("seq %d: resolution closure changed", seq)
	}
	if seq >= 80 && (!got.findings["finding:40:5"].Closed || got.findings["finding:40:5"].ClosureKind != oracleClosureSupersession || got.findings["finding:60:0"].ClosureKind != oracleClosureSupersession) {
		t.Fatal("supersession did not close only the still-open findings")
	}
	refs := make([]string, 0, len(got.retainedReceipts))
	for _, receipt := range got.retainedReceipts {
		refs = append(refs, receipt.RunRef)
	}
	wantRefs := []string{"receipt:before-review", "receipt:mint"}
	if seq >= 70 {
		wantRefs = append(wantRefs, "receipt:again")
	}
	if seq == 80 {
		wantRefs = nil
	}
	if seq >= 90 {
		wantRefs = []string{"receipt:successor"}
	}
	if !slices.Equal(refs, wantRefs) {
		t.Fatalf("seq %d: receipts=%v want %v", seq, refs, wantRefs)
	}
}

func TestOracleFindingFactsWriterWithoutLogAndRollback(t *testing.T) {
	db := oracleFindingFactsDB(t)
	events := oracleFindingFactsEvents(t, db)
	if _, err := db.Exec(`ALTER TABLE domain_events RENAME TO hidden_events`); err != nil {
		t.Fatal(err)
	}
	for _, event := range events[:4] {
		writeOracleFindingEffect(t, db, event)
	}
	before, err := readWorkerOracleFindingLineageTx(t.Context(), db, "work")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeOracleFindingFactsTx(t.Context(), tx, events[7]); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	after, err := readWorkerOracleFindingLineageTx(t.Context(), db, "work")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("rollback changed lineage: %v", err)
	}
	for _, event := range events[4:] {
		writeOracleFindingEffect(t, db, event)
	}
	got, err := readWorkerOracleFindingLineageTx(t.Context(), db, "work")
	if err != nil {
		t.Fatal(err)
	}
	assertOracleFindingLegacySelection(t, 90, got)
}

func TestOracleFindingFactsFoldOnly(t *testing.T) {
	db := oracleFindingFactsDB(t)
	event := oracleFindingFactsEvents(t, db)[0]
	writeOracleFindingEffect(t, db, event)
	if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO worker_oracle_finding_facts VALUES('other',0,'[]','[]')`,
		`UPDATE worker_oracle_finding_facts SET oracle_capable=1`,
		`DELETE FROM worker_oracle_finding_facts`,
	} {
		if _, err := db.Exec(query); err == nil || !strings.Contains(err.Error(), "fold-only") {
			t.Fatalf("unguarded mutation: %s: %v", query, err)
		}
	}
}

func TestOracleFindingFactsNoLogFallback(t *testing.T) {
	db := oracleFindingFactsDB(t)
	for _, event := range oracleFindingFactsEvents(t, db) {
		insertOracleFindingEffect(t, db, event)
	}
	if err := backfillOracleFindingFacts(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE worker_oracle_finding_facts`); err != nil {
		t.Fatal(err)
	}
	if _, err := readWorkerOracleFindingLineageTx(t.Context(), db, "work"); err == nil {
		t.Fatal("missing current facts silently fell back to a readable event log")
	}
}

func TestOracleFindingFactsBackfillFailureRollsBack(t *testing.T) {
	db := oracleFindingFactsDB(t)
	events := oracleFindingFactsEvents(t, db)
	for _, event := range events {
		insertOracleFindingEffect(t, db, event)
	}
	broken := events[1]
	broken.SubjectID, broken.Payload = "other", json.RawMessage(`{"attempt_id":1}`)
	insertOracleFindingEffect(t, db, broken)
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := backfillOracleFindingFacts(t.Context(), tx); err == nil {
		_ = tx.Rollback()
		t.Fatal("malformed legacy dispatch backfilled successfully")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM worker_oracle_finding_facts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed migration leaked %d rows: %v", count, err)
	}
}

func TestOracleFindingFactsBackfillSkipsRemovedWork(t *testing.T) {
	db := oracleFindingFactsDB(t)
	if _, err := db.Exec(`PRAGMA foreign_keys=ON; INSERT INTO work_items VALUES('removed')`); err != nil {
		t.Fatal(err)
	}
	var foreignKeys int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("foreign key enforcement=%d: %v", foreignKeys, err)
	}
	events := oracleFindingFactsEvents(t, db)
	for _, event := range events {
		insertOracleFindingEffect(t, db, event)
	}
	removed := events[2]
	removed.SubjectID, removed.EventID = "removed", "removed-receipt"
	insertOracleFindingEffect(t, db, removed)
	if _, err := db.Exec(`DELETE FROM work_items WHERE id='removed'`); err != nil {
		t.Fatal(err)
	}
	if err := backfillOracleFindingFacts(t.Context(), db); err != nil {
		t.Fatalf("removed work's retained effects aborted migration: %v", err)
	}
	var removedFacts, retainedEffects int
	if err := db.QueryRow(`SELECT count(*) FROM worker_oracle_finding_facts WHERE work_id='removed'`).Scan(&removedFacts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id='removed'`).Scan(&retainedEffects); err != nil {
		t.Fatal(err)
	}
	if removedFacts != 0 || retainedEffects != 1 {
		t.Fatalf("removed work: facts=%d retained effects=%d; want 0 and 1", removedFacts, retainedEffects)
	}
	lineage, err := readWorkerOracleFindingLineageTx(t.Context(), db, "work")
	if err != nil {
		t.Fatal(err)
	}
	assertOracleFindingLegacySelection(t, 90, lineage)
}

func TestOracleFindingFactsStoresSourceBindings(t *testing.T) {
	for _, migrate := range []bool{false, true} {
		t.Run(fmt.Sprintf("migration=%v", migrate), func(t *testing.T) {
			db := oracleFindingFactsDB(t)
			events := oracleFindingFactsEvents(t, db)[:4]
			binding := WorkerJobBinding{JobID: "job:oracle", Revision: 1, Digest: "digest"}
			laterBinding := WorkerJobBinding{JobID: binding.JobID, Revision: 2, Digest: "later-digest"}
			laterOracle := testOracleGraph()
			laterOracle.Owners[0].Obligation = "later revision obligation"
			laterOracle.Owners[0].DomainID = "later-domain"
			if _, err := db.Exec(`INSERT INTO worker_job_revisions VALUES('work',?,?,?,?)`, laterBinding.JobID, laterBinding.Revision, laterBinding.Digest, string(mustJSONValue(map[string]any{"acceptance_oracle": laterOracle}))); err != nil {
				t.Fatal(err)
			}
			// An identical binding on another work is not this finding's source.
			if _, err := db.Exec(`INSERT INTO worker_job_revisions VALUES('other',?,?,?,?)`, binding.JobID, binding.Revision, binding.Digest, string(mustJSONValue(map[string]any{"acceptance_oracle": laterOracle}))); err != nil {
				t.Fatal(err)
			}
			events = append(events,
				Event{Seq: 45, EventID: "later-dispatch", Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: "work", Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: "later", LaneActorRef: "actor:later", WorkerJob: &laterBinding})},
				Event{Seq: 50, EventID: "later-mint", Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: "work", Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: "later", WorkerJob: &laterBinding, Review: &WorkerReviewBlock{Findings: []WorkerReviewFinding{{Severity: "P1", Detail: "later finding", Oracle: &WorkerOracleFinding{OwnerID: laterOracle.Owners[0].OwnerID, Classification: OracleClassificationDeliveryBlocker}}}}})},
			)
			expected := &workerOracleFindingLineage{findings: map[string]*WorkerOpenOracleFinding{}}
			oracles := map[WorkerJobBinding]*AcceptanceOracle{binding: testOracleGraph(), laterBinding: laterOracle}
			actors := map[string]string{}
			for _, event := range events {
				if err := reduceOracleFindingEffect(expected, event, oracles, actors); err != nil {
					t.Fatal(err)
				}
				insertOracleFindingEffect(t, db, event)
				if !migrate {
					writeOracleFindingEffect(t, db, event)
				}
			}
			if migrate {
				if err := backfillOracleFindingFacts(t.Context(), db); err != nil {
					t.Fatal(err)
				}
			}
			var raw string
			if err := db.QueryRow(`SELECT findings FROM worker_oracle_finding_facts WHERE work_id='work'`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(raw, `"SourceOracle"`) || strings.Contains(raw, `"owners"`) || strings.Contains(raw, `"controls"`) || strings.Contains(raw, `"obligation"`) {
				t.Fatal("aggregate repeats canonical acceptance oracle bytes")
			}
			var facts []struct {
				FindingID string
				SourceJob *WorkerJobBinding `json:"source_job"`
			}
			if err := json.Unmarshal([]byte(raw), &facts); err != nil {
				t.Fatal(err)
			}
			if len(facts) != 4 {
				t.Fatalf("stored findings=%d want 4", len(facts))
			}
			if _, err := db.Exec(`ALTER TABLE domain_events RENAME TO hidden_events`); err != nil {
				t.Fatal(err)
			}
			lineage, err := readWorkerOracleFindingLineageTx(t.Context(), db, "work")
			if err != nil {
				t.Fatal(err)
			}
			for index, fact := range facts {
				wantBinding, wantOracle := binding, testOracleGraph()
				if index == 3 {
					wantBinding, wantOracle = laterBinding, laterOracle
				}
				if fact.SourceJob == nil || *fact.SourceJob != wantBinding {
					t.Fatalf("finding %s source job=%+v want %+v", fact.FindingID, fact.SourceJob, wantBinding)
				}
				finding := lineage.findings[fact.FindingID]
				if finding == nil || !reflect.DeepEqual(finding.SourceOracle, wantOracle) || finding.DomainID != wantOracle.Owners[0].DomainID {
					t.Fatalf("finding %s lost canonical source fields: %+v", fact.FindingID, finding)
				}
				if !slices.Equal(mustJSONValue(finding), mustJSONValue(expected.findings[fact.FindingID])) {
					t.Fatalf("finding %s public lineage differs from the pure legacy reducer", fact.FindingID)
				}
			}
			// Missing canonical provenance keeps the old map-lookup result:
			// nil source oracle and no derived Domain, never a stored oracle copy.
			if _, err := db.Exec(`DELETE FROM worker_job_revisions WHERE work_id='work' AND revision=1`); err != nil {
				t.Fatal(err)
			}
			missing, err := readWorkerOracleFindingLineageTx(t.Context(), db, "work")
			if err != nil {
				t.Fatal(err)
			}
			if finding := missing.findings["finding:40:3"]; finding.SourceOracle != nil || finding.DomainID != "" {
				t.Fatalf("missing canonical source reused a copied oracle: %+v", finding)
			}
			if finding := missing.findings["finding:50:0"]; !reflect.DeepEqual(finding.SourceOracle, laterOracle) || finding.DomainID != laterOracle.Owners[0].DomainID {
				t.Fatalf("another revision's canonical source changed: %+v", finding)
			}
		})
	}
}
