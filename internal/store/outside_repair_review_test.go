package store

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/workflowproof"
)

// Include the log, approval, action, resource and outbox rows as well as all
// projections: a refusal must roll back the entire operation, not just work.
func outsideRepairDatabaseSnapshot(t *testing.T, s *Store) string {
	t.Helper()
	rows, err := s.db.Query(`SELECT name FROM sqlite_schema WHERE type='table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	var out strings.Builder
	for _, table := range tables {
		rows, err := s.db.Query(`SELECT * FROM "` + strings.ReplaceAll(table, `"`, `""`) + `"`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		var records []string
		for rows.Next() {
			values := make([]any, len(columns))
			dest := make([]any, len(columns))
			for i := range values {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			records = append(records, string(raw))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		// Row identity, not scan order, is the invariant.
		out.WriteString(table + ":" + fmt.Sprint(slices.Sorted(slices.Values(records))) + "\n")
	}
	return out.String()
}

func TestOutsideRepairHeldSupersedeRefusesWithoutAnyEffects(t *testing.T) {
	s := openTemp(t)
	work := "outside-held-supersede"
	seedOutsideRepairTestWork(t, s, work)
	seedWork(t, s, "outside-successor")
	if err := outsideRepairApply(t, s, outsideRepairTestRequest(t, s, work, "hold"), WorkflowOutsideRepairDispositionSet, workflowproof.Evidence{}); err != nil {
		t.Fatal(err)
	}
	before := outsideRepairDatabaseSnapshot(t, s)
	version := readWorkVersion(t, s, work)
	event := workSupersededEvent("outside-supersede", "outside-successor", work, version, version+1)
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload["successor_expected_version"] = 2
	payload["successor_resulting_version"] = 3
	event.Payload = mustJSONValue(payload)
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{
		VersionRef(SubjectWorkItem, work): version, VersionRef(SubjectWorkItem, "outside-successor"): 2,
	}})
	assertFailureKind(t, err, KindOutsideRepairActive)
	if after := outsideRepairDatabaseSnapshot(t, s); after != before {
		t.Fatal("held supersession changed log, versions, relations or another projection")
	}
}

func TestOutsideRepairLifecycleWriterRequiresScopedAuthorization(t *testing.T) {
	s := openTemp(t)
	work := "outside-writer-scope"
	seedOutsideRepairTestWork(t, s, work)
	if err := outsideRepairApply(t, s, outsideRepairTestRequest(t, s, work, "hold"), WorkflowOutsideRepairDispositionSet, workflowproof.Evidence{}); err != nil {
		t.Fatal(err)
	}
	before := outsideRepairDatabaseSnapshot(t, s)
	version := readWorkVersion(t, s, work)
	for _, target := range []string{"completed", "cancelled", "superseded", "in_progress"} {
		event := workTransitionEvent("outside-writer-"+target, work, "needed", target, version, version+1)
		err := s.Transact(context.Background(), func(tx *Transaction) error {
			scope, err := beginWrite(context.Background(), tx.tx)
			if err != nil {
				return err
			}
			// Merely owning the write guard grants no reconciliation authority.
			if err := updateWorkLifecycle(context.Background(), tx.tx, event, target, version, version+1, scope); err != nil {
				return err
			}
			return scope.close(context.Background())
		})
		assertFailureKind(t, err, KindOutsideRepairActive)
	}
	err := s.Transact(context.Background(), func(tx *Transaction) error {
		scope, err := beginWrite(context.Background(), tx.tx)
		if err != nil {
			return err
		}
		event := workTransitionEvent("outside-writer-start", work, "needed", "in_progress", version, version+1)
		if err := beginWorkflowLifecycleTx(context.Background(), tx.tx, event); err != nil {
			return err
		}
		return scope.close(context.Background())
	})
	assertFailureKind(t, err, KindOutsideRepairActive)
	if outsideRepairDatabaseSnapshot(t, s) != before {
		t.Fatal("ordinary fold scope acquired outside-repair lifecycle authority")
	}
}

func outsideRepairExecutionFixture(t *testing.T, work string) (*Store, WorkflowActor) {
	t.Helper()
	s := openTemp(t)
	t.Cleanup(func() { s.Close() })
	actor, _ := continuityTestWorkflow(t, s, work)
	return s, actor
}

func outsideRepairSettleAction(t *testing.T, s *Store, work string, actor WorkflowActor, kind string) {
	t.Helper()
	ref, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, work)
	fields := map[string]any{"work_id": work, "expected_version": version, "resulting_version": version + 1,
		"step_id": "proposal", "attempt_epoch": 1, "actor_ref": ref}
	if kind == WorkflowActionFailed {
		fields["failure_kind"], fields["recoverable"] = "timeout", false
	} else {
		fields["action_id"] = "checkpoint_context"
	}
	event := workflowEventWithActor(fmt.Sprintf("%s-settle-%d", work, version), kind, work, ref, fields)
	if err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{event}, ExpectedVersions: workVersion(work, version)}); err != nil {
		t.Fatal(err)
	}
}

func TestOutsideRepairHoldRefusesOutstandingExecution(t *testing.T) {
	const work = "outside-live-action"
	s, _ := outsideRepairExecutionFixture(t, work)
	req := outsideRepairTestRequest(t, s, work, "hold")
	before := outsideRepairDatabaseSnapshot(t, s)
	assertFailureKind(t, outsideRepairApply(t, s, req, WorkflowOutsideRepairDispositionSet, workflowproof.Evidence{}), KindNotTerminal)
	if outsideRepairDatabaseSnapshot(t, s) != before {
		t.Fatal("refused live hold changed the database")
	}
}

func TestOutsideRepairSettledActionCannotAdvanceAfterHold(t *testing.T) {
	const work = "outside-settled-action"
	s, actor := outsideRepairExecutionFixture(t, work)
	outsideRepairSettleAction(t, s, work, actor, WorkflowActionFailed)
	if err := outsideRepairApply(t, s, outsideRepairTestRequest(t, s, work, "hold"), WorkflowOutsideRepairDispositionSet, workflowproof.Evidence{}); err != nil {
		t.Fatalf("closed failed action must not block hold: %v", err)
	}
	before := outsideRepairDatabaseSnapshot(t, s)
	_, err := continuityAction(t, s, work, readWorkVersion(t, s, work), "record_proposal", "outside-late-action", nil, actor)
	assertFailureKind(t, err, KindOutsideRepairActive)
	if outsideRepairDatabaseSnapshot(t, s) != before {
		t.Fatal("managed action while held changed the database")
	}
}

func TestOutsideRepairHoldAcceptsFailedClosedAttempt(t *testing.T) {
	work := "outside-failed-action"
	s, actor := outsideRepairExecutionFixture(t, work)
	req := outsideRepairTestRequest(t, s, work, "hold")
	before := outsideRepairDatabaseSnapshot(t, s)
	assertFailureKind(t, outsideRepairApply(t, s, req, WorkflowOutsideRepairDispositionSet, workflowproof.Evidence{}), KindNotTerminal)
	if outsideRepairDatabaseSnapshot(t, s) != before {
		t.Fatal("open action hold changed the database")
	}
	outsideRepairSettleAction(t, s, work, actor, WorkflowActionFailed)
	if err := outsideRepairApply(t, s, outsideRepairTestRequest(t, s, work, "settled-hold"), WorkflowOutsideRepairDispositionSet, workflowproof.Evidence{}); err != nil {
		t.Fatalf("failed closed action cannot strand outside repair: %v", err)
	}
}

func TestOutsideRepairHoldRefusesPendingDurableOperation(t *testing.T) {
	s := openTemp(t)
	work := "outside-pending-operation"
	seedOutsideRepairTestWork(t, s, work)
	claim := testClaim("outside-pending", "outside-pending-key")
	claim.WorkID = work
	if _, err := ClaimStepAuthorized(context.Background(), s, claim, nil); err != nil {
		t.Fatal(err)
	}
	req := outsideRepairTestRequest(t, s, work, "hold")
	before := outsideRepairDatabaseSnapshot(t, s)
	assertFailureKind(t, outsideRepairApply(t, s, req, WorkflowOutsideRepairDispositionSet, workflowproof.Evidence{}), KindNotTerminal)
	if outsideRepairDatabaseSnapshot(t, s) != before {
		t.Fatal("pending operation hold changed the database")
	}
}

func TestOutsideRepairCompletedActionDoesNotBlockHold(t *testing.T) {
	const work = "outside-completed-action"
	s, actor := outsideRepairExecutionFixture(t, work)
	outsideRepairSettleAction(t, s, work, actor, WorkflowActionCompleted)
	completion := func() int64 {
		t.Helper()
		var seq int64
		if err := s.db.QueryRow(`SELECT completion_seq FROM workflow_step_completions WHERE work_id=? AND step_id='proposal'`, work).Scan(&seq); err != nil {
			t.Fatal(err)
		}
		return seq
	}
	before := completion()
	if err := outsideRepairApply(t, s, outsideRepairTestRequest(t, s, work, "hold"), WorkflowOutsideRepairDispositionSet, workflowproof.Evidence{}); err != nil {
		t.Fatalf("completed action without live execution blocks hold: %v", err)
	}
	if completion() != before {
		t.Fatal("hold changed terminal action evidence")
	}
	if err := outsideRepairApply(t, s, outsideRepairTestRequest(t, s, work, "reconcile"), WorkflowOutsideRepairReconciled, outsideRepairSampleEvidence()); err != nil {
		t.Fatal(err)
	}
	if completion() != before {
		t.Fatal("reconciliation changed terminal action evidence")
	}
}

func TestOutsideRepairReconcileRechecksOutstandingExecution(t *testing.T) {
	const work = "outside-recheck-action"
	s, actor := outsideRepairExecutionFixture(t, work)
	// Reconciliation must protect outstanding execution even when a hold
	// already exists in the current rows.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		INSERT INTO outside_repair_dispositions(work_id,state,reason,evidence_json,recorded_at)
		VALUES(?,'active','fixture with outstanding execution','{}','2026-10-23T00:00:00Z');
		DELETE FROM write_guard`, work); err != nil {
		t.Fatal(err)
	}
	req := outsideRepairTestRequest(t, s, work, "reconcile")
	before := outsideRepairDatabaseSnapshot(t, s)
	assertFailureKind(t, outsideRepairApply(t, s, req, WorkflowOutsideRepairReconciled, outsideRepairSampleEvidence()), KindNotTerminal)
	if outsideRepairDatabaseSnapshot(t, s) != before {
		t.Fatal("reconciliation with outstanding execution changed the database")
	}
	outsideRepairSettleAction(t, s, work, actor, WorkflowActionFailed)
	req = outsideRepairTestRequest(t, s, work, "reconcile")
	if err := outsideRepairApply(t, s, req, WorkflowOutsideRepairReconciled, outsideRepairSampleEvidence()); err != nil {
		t.Fatalf("settled execution did not release reconciliation: %v", err)
	}
}

func TestOutsideRepairContinuityDoesNotReadBrokenWorkflow(t *testing.T) {
	for _, broken := range []string{"definition", "contract"} {
		t.Run(broken, func(t *testing.T) {
			s := openTemp(t)
			work := "outside-broken-" + broken
			seedOutsideRepairTestWork(t, s, work)
			if err := outsideRepairApply(t, s, outsideRepairTestRequest(t, s, work, "hold"), WorkflowOutsideRepairDispositionSet, workflowproof.Evidence{}); err != nil {
				t.Fatal(err)
			}
			if err := s.Transact(context.Background(), func(tx *Transaction) error {
				scope, err := beginWrite(context.Background(), tx.tx)
				if err != nil {
					return err
				}
				if broken == "definition" {
					_, err = tx.tx.Exec(`UPDATE workflow_instances SET definition_digest=? WHERE work_id=?`, "sha256:"+strings.Repeat("a", 64), work)
				} else {
					// Removing the projection proves the held read makes no
					// contract query, not merely that it tolerates one bad row.
					_, err = tx.tx.Exec(`DROP TABLE workflow_contracts`)
				}
				if err != nil {
					return err
				}
				return scope.close(context.Background())
			}); err != nil {
				t.Fatal(err)
			}
			before := outsideRepairDatabaseSnapshot(t, s)
			out, err := ReadWorkflowContinuity(context.Background(), s, ContinuityRequest{Work: work})
			if err != nil {
				t.Fatalf("held continuity consulted broken %s: %v", broken, err)
			}
			if out.OutsideRepairDisposition == nil || out.OutsideRepairDisposition.State != OutsideRepairStateActive || out.WorkPin == nil || out.WorkPin.WorkID != work || out.Contract != nil || len(out.StepActions) != 0 || out.PendingOperatorDecision != nil || !reflect.DeepEqual(out.OutsideRepairRoute, outsideRepairRouteNames()) {
				t.Fatalf("bounded outside context = %+v", out)
			}
			if outsideRepairDatabaseSnapshot(t, s) != before {
				t.Fatal("held continuity wrote state")
			}
		})
	}
}
