package store

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Include the log, approval, dispatch, resource and outbox rows as well as all
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
		out.WriteString(table + ":" + fmt.Sprint(sortedStrings(records)) + "\n")
	}
	return out.String()
}

func TestOutsideRepairHeldSupersedeRefusesWithoutAnyEffects(t *testing.T) {
	s := openTemp(t)
	work := "outside-held-supersede"
	seedOutsideRepairTestWork(t, s, work)
	seedWork(t, s, "outside-successor")
	claim := operationEvent("outside-claim", "work.resource_claimed", SubjectWorkItem, work, map[string]any{
		"work_id": work, "resource_key": "fence:outside-repair", "reason": "retain the repair claim",
		"holder_agent": "agent:test", "holder_session": "session:test", "expected_version": 3, "resulting_version": 4,
	})
	if err := applyWorkEvent(t, s, claim, workVersion(work, 3)); err != nil {
		t.Fatal(err)
	}
	if err := outsideRepairApply(t, s, outsideRepairTestRequest(t, s, work, "hold"), WorkflowOutsideRepairDispositionSet, OutsideRepairEvidence{}); err != nil {
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
		t.Fatal("held supersession changed log, versions, relations, claims or another projection")
	}
}

func TestOutsideRepairLifecycleWriterRequiresScopedAuthorization(t *testing.T) {
	s := openTemp(t)
	work := "outside-writer-scope"
	seedOutsideRepairTestWork(t, s, work)
	if err := outsideRepairApply(t, s, outsideRepairTestRequest(t, s, work, "hold"), WorkflowOutsideRepairDispositionSet, OutsideRepairEvidence{}); err != nil {
		t.Fatal(err)
	}
	before := outsideRepairDatabaseSnapshot(t, s)
	version := readWorkVersion(t, s, work)
	for _, target := range []string{"completed", "cancelled", "superseded", "in_progress"} {
		event := workTransitionEvent("outside-writer-"+target, work, "needed", target, version, version+1)
		err := s.Transact(context.Background(), func(tx *Transaction) error {
			scope, err := beginFold(context.Background(), tx.tx)
			if err != nil {
				return err
			}
			// Merely owning the fold guard grants no reconciliation authority.
			if err := updateWorkLifecycle(context.Background(), tx.tx, event, target, version, version+1, scope); err != nil {
				return err
			}
			return scope.close(context.Background())
		})
		assertFailureKind(t, err, KindOutsideRepairActive)
	}
	err := s.Transact(context.Background(), func(tx *Transaction) error {
		scope, err := beginFold(context.Background(), tx.tx)
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

func outsideRepairDispatchFixture(t *testing.T, label string) (*Store, cd0059DispatchSeed, string, string) {
	t.Helper()
	s := openTemp(t)
	seed := seedDispatchFixture(t, s, label)
	attempt := "attempt-" + label
	claimed := dispatchSessionWorktree(t, s, label)
	request := cd781DispatchRequest(t, s, label, readWorkVersion(t, s, label), attempt, seed.ownerActor, claimed, label)
	if _, err := invokeWorkflowActionForCD0059(context.Background(), t, s, request); err != nil {
		t.Fatal(err)
	}
	var digest string
	if err := s.Transact(context.Background(), func(tx *Transaction) error {
		window, err := FindAuthorizedDispatchWindowTx(context.Background(), tx.tx, label, attempt)
		digest = window.PacketDigest
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return s, seed, attempt, digest
}

func TestOutsideRepairHoldRefusesOutstandingExecution(t *testing.T) {
	for _, state := range []string{"in_flight", "dispatched"} {
		t.Run(state, func(t *testing.T) {
			s, seed, attempt, _ := outsideRepairDispatchFixture(t, "outside-live-"+state)
			if state == "dispatched" {
				lane := BuiltinLaneDefinitions()[0]
				event := Event{EventID: "outside-running", Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: seed.workID, Actor: "worker:test", OccurredAt: time.Unix(20, 0).UTC(), PayloadVersion: 2,
					Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: attempt, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion})}
				if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{event}}); err != nil {
					t.Fatal(err)
				}
			}
			req := outsideRepairTestRequest(t, s, seed.workID, "hold")
			before := outsideRepairDatabaseSnapshot(t, s)
			assertFailureKind(t, outsideRepairApply(t, s, req, WorkflowOutsideRepairDispositionSet, OutsideRepairEvidence{}), KindNotTerminal)
			if outsideRepairDatabaseSnapshot(t, s) != before {
				t.Fatal("refused live hold changed the database")
			}
		})
	}
}

func TestOutsideRepairUnusedAuthorizationCannotDispatchAfterHold(t *testing.T) {
	s, seed, attempt, digest := outsideRepairDispatchFixture(t, "outside-unused-window")
	// A stranded authorization closes through the existing lawful abandonment
	// route. Its unused window remains in history; the hold must fence its use.
	abandon := Event{EventID: "outside-abandon", Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: seed.workID, Actor: "worker:test", OccurredAt: time.Unix(30, 0).UTC(), PayloadVersion: 1,
		Payload: mustJSONValue(WorkerFailedPayload{AttemptID: attempt, FailureKind: WorkerFailureAbandoned, Detail: "the host stopped before dispatch"})}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{abandon}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(context.Background(), func(tx *Transaction) error {
		return ValidateWorkerDispatchWindow(context.Background(), tx, seed.workID, "", attempt, digest)
	}); err != nil {
		t.Fatalf("unused historical window before hold: %v", err)
	}
	if err := outsideRepairApply(t, s, outsideRepairTestRequest(t, s, seed.workID, "hold"), WorkflowOutsideRepairDispositionSet, OutsideRepairEvidence{}); err != nil {
		t.Fatalf("closed failed attempt must not block hold: %v", err)
	}
	before := outsideRepairDatabaseSnapshot(t, s)
	assertFailureKind(t, s.Transact(context.Background(), func(tx *Transaction) error {
		return ValidateWorkerDispatchWindow(context.Background(), tx, seed.workID, "", attempt, digest)
	}), KindOutsideRepairActive)
	lane := BuiltinLaneDefinitions()[0]
	event := Event{EventID: "outside-late-dispatch", Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: seed.workID, Actor: "worker:test", OccurredAt: time.Unix(40, 0).UTC(), PayloadVersion: 2,
		Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: "attempt-late", LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion})}
	assertFailureKind(t, ApplyOperation(context.Background(), s, Operation{Events: []Event{event}}), KindOutsideRepairActive)
	if outsideRepairDatabaseSnapshot(t, s) != before {
		t.Fatal("dispatch consumption while held changed the database")
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	assertWorkerAttemptState(t, s, seed.workID, attempt, "failed")
}

func TestOutsideRepairHoldWaitsForFailedActionSettlement(t *testing.T) {
	work := "outside-failed-action"
	s, _, owner, attempt := seedWorkerAtExecution(t, work)
	failWorkerAttempt(t, s, work, attempt)
	req := outsideRepairTestRequest(t, s, work, "hold")
	before := outsideRepairDatabaseSnapshot(t, s)
	assertFailureKind(t, outsideRepairApply(t, s, req, WorkflowOutsideRepairDispositionSet, OutsideRepairEvidence{}), KindNotTerminal)
	if outsideRepairDatabaseSnapshot(t, s) != before {
		t.Fatal("open action hold changed the database")
	}
	applyRecordWorkerFailureForTest(t, s, work, owner, attempt, 1, readWorkVersion(t, s, work), "outside-settle-failure")
	if err := outsideRepairApply(t, s, outsideRepairTestRequest(t, s, work, "settled-hold"), WorkflowOutsideRepairDispositionSet, OutsideRepairEvidence{}); err != nil {
		t.Fatalf("failed closed attempt/action cannot strand outside repair: %v", err)
	}
}

func TestOutsideRepairHoldRefusesPendingDurableOperation(t *testing.T) {
	s := openTemp(t)
	work := "outside-pending-operation"
	seedOutsideRepairTestWork(t, s, work)
	claim := testClaim("outside-pending", "outside-pending-key")
	claim.WorkID = work
	if _, err := ClaimStep(context.Background(), s, claim); err != nil {
		t.Fatal(err)
	}
	req := outsideRepairTestRequest(t, s, work, "hold")
	before := outsideRepairDatabaseSnapshot(t, s)
	assertFailureKind(t, outsideRepairApply(t, s, req, WorkflowOutsideRepairDispositionSet, OutsideRepairEvidence{}), KindNotTerminal)
	if outsideRepairDatabaseSnapshot(t, s) != before {
		t.Fatal("pending operation hold changed the database")
	}
}

func TestOutsideRepairConsumedTerminalWindowDoesNotBlockHold(t *testing.T) {
	s, seed, attempt, digest := outsideRepairDispatchFixture(t, "outside-consumed-window")
	if err := seedWorkerEvidenceForAttempt(t, s, seed.workID, attempt, "outside-consumed", digest); err != nil {
		t.Fatal(err)
	}
	assertWorkerAttemptState(t, s, seed.workID, attempt, "completed")
	workers := workerProjectionSnapshot(t, s)
	if err := outsideRepairApply(t, s, outsideRepairTestRequest(t, s, seed.workID, "hold"), WorkflowOutsideRepairDispositionSet, OutsideRepairEvidence{}); err != nil {
		t.Fatalf("consumed authorization without live execution blocks hold: %v", err)
	}
	if after := workerProjectionSnapshot(t, s); after != workers {
		t.Fatal("hold changed terminal worker evidence")
	}
	if err := outsideRepairApply(t, s, outsideRepairTestRequest(t, s, seed.workID, "reconcile"), WorkflowOutsideRepairReconciled, outsideRepairSampleEvidence()); err != nil {
		t.Fatal(err)
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if after := workerProjectionSnapshot(t, s); after != workers {
		t.Fatal("reconciliation/rebuild changed terminal worker evidence")
	}
}

func TestOutsideRepairReconcileRechecksOutstandingExecution(t *testing.T) {
	for _, state := range []string{"in_flight", "dispatched"} {
		t.Run(state, func(t *testing.T) {
			testOutsideRepairReconcileOutstandingExecution(t, state)
		})
	}
}

func testOutsideRepairReconcileOutstandingExecution(t *testing.T, state string) {
	s, seed, attempt, _ := outsideRepairDispatchFixture(t, "outside-recheck-"+state)
	if state == "dispatched" {
		lane := BuiltinLaneDefinitions()[0]
		event := Event{EventID: "outside-recheck-dispatch", Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: seed.workID, Actor: "worker:test", OccurredAt: time.Unix(20, 0).UTC(), PayloadVersion: 2,
			Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: attempt, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion})}
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{event}}); err != nil {
			t.Fatal(err)
		}
	}
	version := readWorkVersion(t, s, seed.workID)
	claim := operationEvent("outside-recheck-claim", "work.resource_claimed", SubjectWorkItem, seed.workID, map[string]any{
		"work_id": seed.workID, "resource_key": "fence:outside-recheck", "reason": "retain until execution stops",
		"holder_agent": "agent:test", "holder_session": "session:test", "expected_version": version, "resulting_version": version + 1,
	})
	if err := applyWorkEvent(t, s, claim, workVersion(seed.workID, version)); err != nil {
		t.Fatal(err)
	}
	// A retained historical hold can coexist with an open attempt. Replay
	// preserves it; fresh completion must still refuse to release its claims.
	req := outsideRepairTestRequest(t, s, seed.workID, "historical-hold")
	replay := workflowReplayContext(context.Background())
	if err := s.TransactDurable(replay, func(tx *Transaction) error {
		_, err := SetOutsideRepairDispositionTx(replay, tx, req)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	req = outsideRepairTestRequest(t, s, seed.workID, "reconcile")
	before := outsideRepairDatabaseSnapshot(t, s)
	assertFailureKind(t, outsideRepairApply(t, s, req, WorkflowOutsideRepairReconciled, outsideRepairSampleEvidence()), KindNotTerminal)
	if outsideRepairDatabaseSnapshot(t, s) != before {
		t.Fatal("reconciliation with outstanding execution changed the database")
	}
	// Worker terminal evidence remains available, without managed advancement.
	abandon := Event{EventID: "outside-recheck-abandon", Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: seed.workID, Actor: "worker:test", OccurredAt: time.Unix(30, 0).UTC(), PayloadVersion: 1,
		Payload: mustJSONValue(WorkerFailedPayload{AttemptID: attempt, FailureKind: WorkerFailureAbandoned, Detail: "host ended without a report"})}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{abandon}}); err != nil {
		t.Fatal(err)
	}
	if err := outsideRepairApply(t, s, req, WorkflowOutsideRepairReconciled, outsideRepairSampleEvidence()); err != nil {
		t.Fatalf("settled execution did not release reconciliation: %v", err)
	}
	claims, err := s.ResourceClaims(context.Background(), "fence:outside-recheck", "", 1)
	if err != nil || len(claims) != 1 || claims[0].State != ResourceClaimReleased {
		t.Fatalf("settled reconciliation did not release claim: %+v, %v", claims, err)
	}
}

func TestOutsideRepairContinuityDoesNotReadBrokenWorkflow(t *testing.T) {
	for _, broken := range []string{"definition", "contract"} {
		t.Run(broken, func(t *testing.T) {
			s := openTemp(t)
			work := "outside-broken-" + broken
			seedOutsideRepairTestWork(t, s, work)
			if err := outsideRepairApply(t, s, outsideRepairTestRequest(t, s, work, "hold"), WorkflowOutsideRepairDispositionSet, OutsideRepairEvidence{}); err != nil {
				t.Fatal(err)
			}
			if err := s.Transact(context.Background(), func(tx *Transaction) error {
				scope, err := beginFold(context.Background(), tx.tx)
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
