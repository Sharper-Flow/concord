package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func seedContextFactTerminal(t *testing.T, f workContextFixture, kind string, fields map[string]any) int64 {
	t.Helper()
	tx, err := f.store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterWrite(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	var frontier int64
	if err := tx.QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&frontier); err != nil {
		t.Fatal(err)
	}
	event := Event{EventID: fmt.Sprintf("context-facts:%s:%d", f.workID, frontier+1), SubjectType: SubjectWorkItem, SubjectID: f.workID, Kind: kind, Actor: "fixture", PayloadVersion: 1, Payload: jsonRaw(workflowJSON(fields)), OccurredAt: time.Unix(1, 0)}
	seq, err := appendPreparedEvent(t.Context(), tx, event)
	if err != nil {
		t.Fatal(err)
	}
	event.Seq = seq
	if err := writeWorkContextFacts(t.Context(), tx, event); err != nil {
		t.Fatal(err)
	}
	if err := advanceWorkEffectSeqTx(t.Context(), tx, event); err != nil {
		t.Fatal(err)
	}
	if err := leaveWrite(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return seq
}

func TestWorkContextFactsAnchorSelectionBackfillAndLogBlind(t *testing.T) {
	f := seedWorkContextFixture(t, "context-facts-selection")
	ctx := t.Context()
	var corpus []Event
	// Construct source envelopes before their current action runs. No target
	// declaration/finding row is used to reconstruct historical input.
	recordDeclaration := func(payload map[string]any) {
		t.Helper()
		version := readWorkVersion(t, f.store, f.workID)
		identity := fmt.Sprintf("record_work_context-%s-%d", f.workID, version)
		actor, err := WorkflowActorRef(f.owner)
		if err != nil {
			t.Fatal(err)
		}
		fields, err := workflowActionObject(mustJSONValue(payload))
		if err != nil {
			t.Fatal(err)
		}
		tx, err := f.store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		events, err := workflowRecordWorkContextEvents(ctx, tx, WorkflowActionExecutionRequest{WorkID: f.workID, RequestID: "request:" + identity, Now: time.Unix(9, 0).UTC()}, "repair", actor, fields, identity+":semantic", version)
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			t.Fatal(rollbackErr)
		}
		if err != nil {
			t.Fatal(err)
		}
		corpus = append(corpus, events...)
		if err := f.recordWorkContext(t, payload); err != nil {
			t.Fatal(err)
		}
	}
	recordTerminal := func(kind string, fields map[string]any) int64 {
		t.Helper()
		var frontier int64
		if err := f.store.db.QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&frontier); err != nil {
			t.Fatal(err)
		}
		event := Event{EventID: fmt.Sprintf("context-facts:%s:%d", f.workID, frontier+1), SubjectType: SubjectWorkItem, SubjectID: f.workID, Kind: kind, Actor: "fixture", PayloadVersion: 1, Payload: jsonRaw(workflowJSON(fields)), OccurredAt: time.Unix(1, 0)}
		corpus = append(corpus, event)
		return seedContextFactTerminal(t, f, kind, fields)
	}
	recordDeclaration(map[string]any{"context_findings": []WorkerContextFinding{contextFindingFixture()}})
	reportSeq := recordTerminal(WorkerCompleted, map[string]any{
		"context_findings": []WorkerContextFinding{contextFindingFixture()},
		"review":           map[string]any{"findings": []WorkerReviewFinding{{FindingID: "review:one", Severity: "high", Detail: "ranked claim", Status: "open"}}},
	})
	selected := fmt.Sprintf("finding:%d:1", reportSeq)
	recordDeclaration(map[string]any{"finding_refs": []string{selected}, "context_findings": []map[string]any{workContextFinding(workContextTestChild, "direction", "current declaration", "")}})
	failedSeq := recordTerminal(WorkerFailed, map[string]any{"context_findings": []WorkerContextFinding{contextFindingFixture()}})
	emptySeq := recordTerminal(WorkerCompleted, map[string]any{})
	view, err := readWorkContextView(ctx, f.store.db, f.workID)
	if err != nil || view == nil {
		t.Fatalf("current view = %+v, %v", view, err)
	}
	if view.SourceEventFrontier != emptySeq || len(view.Findings) != 3 || view.Findings[0].Statement != "current declaration" || view.Findings[1].FindingID != selected || view.Findings[2].FindingID != fmt.Sprintf("finding:%d:0", failedSeq) {
		t.Fatalf("anchor/selection/report order or empty-report frontier changed: %+v", view)
	}
	// Compare migration148's retained facts before current subject-selection
	// columns exist. The current view is checked after the full migration chain.
	facts := func(db *sql.DB) []string {
		t.Helper()
		rows, err := db.Query(`SELECT json_array('state',work_id,declaration_seq,declaration_id,declaration_payload,source_frontier) FROM work_context_state
			UNION ALL SELECT json_array('finding',work_id,source_seq,ordinal,finding_class,finding) FROM work_context_findings ORDER BY 1`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var row string
			if err := rows.Scan(&row); err != nil {
				t.Fatal(err)
			}
			out = append(out, row)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	wantFacts := facts(f.store.db)
	// CD-0242 D2 retires replay construction only. Prefix144 lets the real
	// 145/146 chain derive receipts and coordinates before 148 selects claims.
	var currentPopulation int
	if err := f.store.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_type='work_item' AND subject_id=? AND kind IN (?,?,?)`, f.workID, WorkflowWorkContextRecorded, WorkerCompleted, WorkerFailed).Scan(&currentPopulation); err != nil || currentPopulation != len(corpus) {
		t.Fatalf("current context population=%d,%v; corpus=%d", currentPopulation, err, len(corpus))
	}
	prefix := func() *Store {
		t.Helper()
		path := filepath.Join(t.TempDir(), "context-selection-144.db")
		db := openMigratedTo(t, path, 144)
		copyFixtureDataToPrefix(t, f.store, db, []string{"products", "projects", "product_projects", "work_items", "work_projects", "workflow_actors", "workflow_contracts", "workflow_architecture_bindings", "workflow_contract_affected_domains"}, map[string]map[string]string{
			"workflow_contracts": {"definition_version": "26"},
		}, "fold_guard")
		for _, event := range corpus {
			var seq int64
			var digest string
			if err := f.store.db.QueryRow(`SELECT effect_seq,payload_digest FROM effect_receipts WHERE event_id=?`, event.EventID).Scan(&seq, &digest); err != nil {
				t.Fatal(err)
			}
			if digest != historicalFixturePayloadDigest(t, string(event.Payload)) {
				t.Fatalf("explicit context source %s differs from the current action", event.EventID)
			}
			if _, err := db.Exec(`INSERT INTO domain_events(seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?,?)`, seq, event.EventID, event.Kind, event.SubjectType, event.SubjectID, event.Actor, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.PayloadVersion, string(event.Payload)); err != nil {
				t.Fatal(err)
			}
		}
		return &Store{db: db, path: path}
	}
	upgraded := prefix()
	db := upgraded.db
	before := histEventRows(t, upgraded)
	for _, row := range before {
		t.Logf("migration148 source seq=%d id=%s kind=%s actor=%s time=%s digest=%s", row.Seq, row.EventID, row.Kind, row.Actor, row.OccurredAt, historicalFixturePayloadDigest(t, row.Payload))
	}
	advanceHistoricalPrefix(t, db, 148)
	if got := histEventRows(t, &Store{db: db}); !reflect.DeepEqual(got, before) {
		t.Fatal("context backfill changed original payload bytes, source identity, actor, sequence or time")
	}
	if got := facts(db); !reflect.DeepEqual(got, wantFacts) {
		t.Fatalf("migration148 declaration/selection/terminal facts differ: got %q; want %q", got, wantFacts)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	assertHistoricalReceipts(t, db, before)
	assertEventLogAbsent(t, f.store)
	assertEventLogAbsent(t, upgraded)
	got, err := readWorkContextView(ctx, db, f.workID)
	if err != nil || !reflect.DeepEqual(got, view) {
		t.Fatalf("backfilled/log-blind view differs: got %+v err %v; want %+v", got, err, view)
	}
	var declarationID, payload string
	var declarationSeq, frontier int64
	if err := db.QueryRow(`SELECT declaration_id,declaration_payload,declaration_seq,source_frontier FROM work_context_state WHERE work_id=?`, f.workID).Scan(&declarationID, &payload, &declarationSeq, &frontier); err != nil {
		t.Fatal(err)
	}
	if declarationID != corpus[2].EventID || payload != string(corpus[2].Payload) || declarationSeq != before[2].Seq || frontier != emptySeq {
		t.Fatalf("declaration bytes/identity/interval/frontier changed: %s %s %d %d", declarationID, payload, declarationSeq, frontier)
	}
	mutant := prefix()
	advanceHistoricalPrefix(t, mutant.db, 147)
	mutantTx, err := mutant.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer mutantTx.Rollback()
	m := migrations[147]
	if err := applyMigration(ctx, contextFindingSelectionMutant{mutantTx}, m); err != nil {
		t.Fatalf("context selection mutant failed before comparison: %v", err)
	}
	// Record the executed migration so the unchanged current tail cannot
	// rerun migration148 and repair the deliberately excluded population.
	if err := advanceStoreRevision(ctx, mutantTx); err != nil {
		t.Fatal(err)
	}
	if _, err := mutantTx.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,breaking,applied_at) VALUES(?,?,?,?,?)`, m.Version, m.Name, m.checksum(), m.Breaking, "2026-10-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := mutantTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(facts(mutant.db), wantFacts) {
		t.Fatal("migration148 failed-report exclusion escaped the retained-facts oracle")
	}
	if err := Migrate(ctx, mutant.db); err != nil {
		t.Fatal(err)
	}
	broken, err := readWorkContextView(ctx, mutant.db, f.workID)
	if err != nil || broken == nil {
		t.Fatalf("context selection mutant failed before the view assertion: %+v, %v", broken, err)
	}
	if reflect.DeepEqual(broken, view) {
		t.Fatal("migration148 failed-report exclusion escaped the anchor/selection oracle")
	}
	t.Logf("RED migration148 finding population=%d, want %d; frontier=%d, want %d", len(broken.Findings), len(view.Findings), broken.SourceEventFrontier, view.SourceEventFrontier)
}

// Break the admitted failed-report population, not a table or manifest. The
// current writer and reader still run unchanged on the independent fixture.
type contextFindingSelectionMutant struct{ migrationRunner }

func (q contextFindingSelectionMutant) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if strings.HasPrefix(query, "SELECT seq,event_id,kind,subject_type,subject_id,payload FROM domain_events WHERE") {
		query = strings.Replace(query, "ORDER BY seq", "AND kind <> 'worker.failed' ORDER BY seq", 1)
	}
	return q.migrationRunner.QueryContext(ctx, query, args...)
}

func TestWorkContextFactsReferenceAdmissionLogBlind(t *testing.T) {
	f := seedWorkContextFixture(t, "context-facts-refs")
	seq := seedContextFactTerminal(t, f, WorkerFailed, map[string]any{"context_findings": []WorkerContextFinding{contextFindingFixture()}})
	valid := fmt.Sprintf("finding:%d:0", seq)
	assertEventLogAbsent(t, f.store)
	tx, err := f.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := validateWorkContextFindingRefs(t.Context(), tx, f.workID, []string{valid}); err != nil {
		t.Fatalf("valid retained finding refused without log: %v", err)
	}
	for _, refs := range [][]string{{valid, valid}, {fmt.Sprintf("finding:%d:1", seq)}, {fmt.Sprintf("finding:%d:0", seq+1000)}, {"finding:1:0"}, {"not-a-finding"}} {
		if err := validateWorkContextFindingRefs(t.Context(), tx, f.workID, refs); !hasFailureKind(err, KindInvalidPayload) {
			t.Fatalf("invalid selection %v = %v, want invalid_payload", refs, err)
		}
	}
}

func TestWorkContextFactsGuardAndRollback(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "context-facts-guard")
	if _, err := s.db.Exec(`INSERT INTO work_context_state(work_id,source_frontier) VALUES('context-facts-guard',1)`); err == nil {
		t.Fatal("context state accepted an unguarded write")
	}
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterWrite(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	event := Event{Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: "context-facts-guard", EventID: "rolled-back", Seq: 200, Payload: jsonRaw(workflowJSON(map[string]any{"context_findings": []WorkerContextFinding{contextFindingFixture()}}))}
	if err := writeWorkContextFacts(t.Context(), tx, event); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var state, claims int
	if err := s.db.QueryRow(`SELECT (SELECT count(*) FROM work_context_state),(SELECT count(*) FROM work_context_findings)`).Scan(&state, &claims); err != nil || state != 0 || claims != 0 {
		t.Fatalf("rolled-back context state/claims = %d/%d, %v", state, claims, err)
	}
}

func TestWorkContextFactsCorruptionIsNotRetryable(t *testing.T) {
	f := seedWorkContextFixture(t, "context-facts-corruption")
	seq := seedContextFactTerminal(t, f, WorkerFailed, map[string]any{"context_findings": []WorkerContextFinding{contextFindingFixture()}})
	if _, err := f.store.db.Exec(`INSERT INTO write_guard VALUES(1); UPDATE work_context_findings SET finding='{"statement":12}' WHERE source_seq=?; DELETE FROM write_guard`, seq); err != nil {
		t.Fatal(err)
	}
	_, err := readWorkContextView(t.Context(), f.store.db, f.workID)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindInvariantViolation || failure.RetrySafe {
		t.Fatalf("corrupt claim = %+v, want non-retryable invariant violation", err)
	}
}
