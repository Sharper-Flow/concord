package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Build through the normal writers, then make every event query fail. Compare
// the typed answer, not just success, so missing optional facts cannot pass.
func TestWorkerCurrentJobFactsWithoutLog(t *testing.T) {
	const work = "current-job-facts"
	f := seedChecksRepairFixture(t, work, "repair")
	s := f.store
	defer s.Close()
	if err := recordWorkerJobFieldsForTest(t, s, work, f.owner, map[string]any{
		"job_id": "job:current", "objective": "Read the complete recorded job without history",
		"stopping_condition": "Current rows retain the admitted descriptor",
		"checks":             []string{"go test ./internal/store"}, "path_scope": []string{"internal/store"},
		"ready": true, "readiness_evidence": []string{"evidence:current"},
	}); err != nil {
		t.Fatal(err)
	}
	binding := checksRepairLatestBinding(t, s, work, "job:current")
	ctx := context.Background()
	checks := map[string]func() (any, error){
		"P13-allowances": func() (any, error) { return readWorkerJobAllowancesTx(ctx, s.db, work) },
		"P16-recorded-job": func() (any, error) {
			var job WorkerJobRecordedPayload
			err := workerJobRecordedDescriptorTx(ctx, s.db, work, binding.JobID, binding.Revision, &job)
			return job, err
		},
		"P17-checks-repairs": func() (any, error) { return readWorkerJobChecksRepairsTx(ctx, s.db, work) },
		"P24-oracles":        func() (any, error) { return readWorkerJobOraclesTx(ctx, s.db, work) },
		"P50-revision":       func() (any, error) { return readWorkerJobRevisionTx(ctx, s.db, work, *binding) },
		"P54-review-repair": func() (any, error) {
			lane := reviewGateLane(t, "review")
			base := "1111111111111111111111111111111111111111"
			return nil, validateNativeReviewRepairTx(ctx, s.db, work, workflowActionCompletedPayload{
				WorkerSubjectCommit: base, WorkerLaneID: lane.ID, WorkerLaneDigest: lane.Digest, WorkerJob: binding,
			}, &nativeReviewRepair{Base: base, Commit: "2222222222222222222222222222222222222222", ChangedPaths: []string{"internal/store/value.go"}})
		},
	}
	assertWorkerFactsLogBlind(t, s, checks)
}

func TestWorkerCurrentAttemptFactsWithoutLog(t *testing.T) {
	f := seedCrossAttemptFixture(t, "current-attempt-facts")
	defer f.s.Close()
	f.dispatch(t, f.attemptR, f.researchLane, f.dispatcher)
	completed := jobBoundCompletionEvent(f.workID, "current-facts-completed", f.attemptR, f.researchLane, time.Unix(21, 0).UTC(), nil, workerFixtureDirectory(t, f.s, f.workID))
	var payload WorkerCompletedPayload
	if err := json.Unmarshal(completed.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload.ObservedDelivery = &WorkerObservedDelivery{State: "clean", Commit: "1111111111111111111111111111111111111111"}
	completed.Payload = mustJSONValue(payload)
	if err := ApplyOperation(context.Background(), f.s, Operation{Events: []Event{completed}}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	dispatch, err := readWorkerDispatchFactTx(ctx, f.s.db, f.workID, f.attemptR)
	if err != nil {
		t.Fatal(err)
	}
	subject, err := workerFailureObservedDeliverySubject(ctx, f.s.db, f.workID, f.attemptR)
	if err != nil || subject == nil {
		t.Fatalf("missing observed delivery fixture: %v", err)
	}
	inTx := func(read func(*sql.Tx) (any, error)) (any, error) {
		tx, err := f.s.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		return read(tx)
	}
	checks := map[string]func() (any, error){
		"P02-P31-authorization-actor": func() (any, error) { return workerAuthorizationActorTx(ctx, f.s.db, f.workID, f.attemptR) },
		"P22-noop-acceptance": func() (any, error) {
			return inTx(func(tx *sql.Tx) (any, error) {
				g := &workflowActionGuardContext{ctx: ctx, tx: tx, request: WorkflowActionExecutionRequest{WorkID: f.workID}}
				return nil, guardAcceptWorkerResultChanges(g, map[string]json.RawMessage{"attempt_id": mustJSONValue(f.attemptR)})
			})
		},
		"P23-noop-producer": func() (any, error) {
			return inTx(func(tx *sql.Tx) (any, error) {
				g := &workflowActionGuardContext{ctx: ctx, tx: tx, request: WorkflowActionExecutionRequest{WorkID: f.workID}}
				return unchangedJobOutcomeProven(g, &payload, &AcceptanceOracle{Controls: []OracleControl{{ControlID: "control:current"}}})
			})
		},
		"P14-caller-exclusions": func() (any, error) {
			return inTx(func(tx *sql.Tx) (any, error) { return workerDispatchCallerExclusionsTx(ctx, tx, f.workID, f.attemptR) })
		},
		"P15-completed-delivery": func() (any, error) {
			return inTx(func(tx *sql.Tx) (any, error) {
				repairs, delivery, err := readWorkerCompletedDeliveryTx(ctx, tx, f.workID, f.attemptR)
				return struct {
					Repairs  []WorkerDirectCallerRepair
					Delivery *workerDeliveredDelta
				}{repairs, delivery}, err
			})
		},
		"P18-checks-repair-review": func() (any, error) {
			return dispatchConsumedChecksRepairReviewTx(ctx, f.s.db, f.workID, f.attemptR, WorkerJobBinding{JobID: "job:absent", Revision: 1})
		},
		"P21-packet-predicates": func() (any, error) {
			return inTx(func(tx *sql.Tx) (any, error) { return dispatchedPacketPredicateIDsTx(ctx, tx, f.workID, f.attemptR) })
		},
		"P11-verify-observed-subject": func() (any, error) {
			return nil, verifyWorkerFailureObservedDeliverySubject(ctx, f.s.db, f.workID, f.attemptR, subject)
		},
		"P26-independent-producer": func() (any, error) {
			return oracleClosureIndependentProducerTx(ctx, f.s.db, f.workID, &WorktreeVerifyResult{LeaseID: "lease:absent"}, &WorkerCompletedPayload{AttemptID: f.attemptR}, "actor:other")
		},
		"P27-packet-digest":               func() (any, error) { return workerDispatchPacketDigestTx(ctx, f.s.db, f.workID, f.attemptR) },
		"P28-dispatch-descriptor":         func() (any, error) { return readWorkerDispatchFactTx(ctx, f.s.db, f.workID, f.attemptR) },
		"P29-coordinator-session":         func() (any, error) { return workerCoordinatorSessionTx(ctx, f.s.db, f.workID, f.attemptR) },
		"P30-P34-P41-failure-disposition": func() (any, error) { return workerRecordedFailureCountTx(ctx, f.s.db, f.workID, f.attemptR) },
		"P32-executor-at-resolved-sequence": func() (any, error) {
			return workerExecutorAtSequenceTx(ctx, f.s.db, f.workID, f.attemptR, dispatch.Seq)
		},
		"P36-current-disposition": func() (any, error) {
			var state WorkflowAdmissionState
			err := (workflowAdmissionLoad{ctx: ctx, q: f.s.db, workID: f.workID}).attemptAndReview(&state)
			return state, err
		},
		"P19-authorization-job": func() (any, error) {
			found, job, err := dispatchCompletionJobForAttempt(ctx, f.s.db, f.workID, f.attemptR)
			return struct {
				Found bool
				Job   *WorkerJobBinding
			}{found, job}, err
		},
		"P20-dispatched-job": func() (any, error) { return workflowDispatchedJobForAttempt(ctx, f.s.db, f.workID, f.attemptR) },
		"P48-P49-review": func() (any, error) {
			auth, result, err := readReviewAttempt(ctx, f.s.db, f.workID, f.attemptR)
			return struct {
				Auth   workflowActionCompletedPayload
				Result WorkerCompletedPayload
			}{auth, result}, err
		},
		"P10-observed-subject":     func() (any, error) { return workerFailureObservedDeliverySubject(ctx, f.s.db, f.workID, f.attemptR) },
		"P35-failed-authorization": func() (any, error) { return workflowLatestFailedWorkerAuthorization(ctx, f.s.db, f.workID, f.step) },
		"P52-attempt-epoch": func() (any, error) {
			tx, err := f.s.db.BeginTx(ctx, nil)
			if err != nil {
				return nil, err
			}
			defer tx.Rollback()
			var pin WorkPin
			err = workPinReadAttemptTx(ctx, tx, f.workID, &pin)
			return pin.Attempt, err
		},
	}
	assertWorkerFactsLogBlind(t, f.s, checks)
}

func TestWorkerCurrentFailureFactsWithoutLog(t *testing.T) {
	const work = "current-failure-facts"
	f := seedWorkflowReturnRouteFixture(t, work, "workflow.break_fix", "repair")
	defer f.store.Close()
	lane := reviewGateLane(t, "implementation")
	attempt := "attempt:current-failure"
	dispatchJobBoundAttempt(t, f.store, work, "repair", attempt, nil, f.owner, 0, "current-failure-dispatch", &WorkerJobBinding{JobID: "job:current-failure"})
	if err := s2WorkerFailedLive(t, f.store, attributionWorkerFailedEvent(work, attempt, "current-failure-terminal", preferredModelForLane(lane), attributionHostObservation())); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	checks := map[string]func() (any, error){
		"P12-attribution":              func() (any, error) { return WorkerFailureAttributionForAttemptTx(ctx, f.store.db, work, attempt) },
		"P30-P34-P41-recorded-failure": func() (any, error) { return workerRecordedFailureCountTx(ctx, f.store.db, work, attempt) },
		"P35-failed-authorization":     func() (any, error) { return workflowLatestFailedWorkerAuthorization(ctx, f.store.db, work, "repair") },
		"P36-failure-disposition": func() (any, error) {
			var state WorkflowAdmissionState
			err := (workflowAdmissionLoad{ctx: ctx, q: f.store.db, workID: work}).attemptAndReview(&state)
			return state, err
		},
	}
	assertWorkerFactsLogBlind(t, f.store, checks)
}

func assertWorkerFactsLogBlind(t *testing.T, s *Store, checks map[string]func() (any, error)) {
	t.Helper()
	want := map[string]any{}
	for name, read := range checks {
		value, err := read()
		if err != nil {
			t.Fatalf("%s baseline: %v", name, err)
		}
		want[name] = value
	}
	assertEventLogAbsent(t, s)
	for name, read := range checks {
		t.Run(name, func(t *testing.T) {
			got, err := read()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want[name]) {
				t.Fatalf("current answer = %#v; with log = %#v", got, want[name])
			}
		})
	}
}

func TestWorkerCurrentFactsMigrationBackfillsStoredBytes(t *testing.T) {
	const work = "current-facts-backfill"
	f := seedChecksRepairFixture(t, work, "repair")
	s := f.store
	defer s.Close()
	if err := recordWorkerJobFieldsForTest(t, s, work, f.owner, map[string]any{
		"job_id": "job:backfill", "objective": "Preserve stored payload values",
		"stopping_condition": "The migrated bytes equal the live facts", "checks": []string{"go test ./internal/store"},
		"ready": true, "readiness_evidence": []string{"evidence:backfill"},
	}); err != nil {
		t.Fatal(err)
	}
	binding := checksRepairLatestBinding(t, s, work, "job:backfill")
	const attempt = "attempt:backfill"
	dispatchJobBoundAttempt(t, s, work, "repair", attempt, nil, f.owner, 0, "current-backfill-dispatch", binding)
	if err := completeJobBoundAttemptForTest(s, work, attempt, reviewGateLane(t, "implementation"), binding); err != nil {
		t.Fatal(err)
	}
	queries := []string{
		`SELECT json_object('job',job_id,'revision',revision,'payload',recorded_payload) FROM worker_job_revisions ORDER BY work_id,job_id,revision`,
		`SELECT json_object('attempt',attempt_id,'dispatch',dispatch_payload,'dispatch_id',dispatch_event_id,'actor',dispatch_actor,'seq',dispatch_seq,'version',dispatch_payload_version,'terminal',terminal_payload,'terminal_id',terminal_event_id,'terminal_version',terminal_payload_version,'kind',terminal_kind,'failure_recorded',failure_recorded) FROM worker_attempts ORDER BY attempt_id`,
		`SELECT json_object('attempt',attempt_id,'seq',seq,'payload',payload) FROM worker_authorizations ORDER BY work_id,attempt_id`,
		`SELECT json_object('step',step_id,'epoch',attempt_epoch) FROM workflow_step_epochs ORDER BY work_id,step_id`,
		`SELECT json_object('disposition',result_disposition) FROM workflow_instances ORDER BY work_id`,
	}
	want := workerFactsQueryRows(t, s, queries)
	assertWorkerFactsCorpus139(t)
	assertEventLogAbsent(t, s)
	got := workerFactsQueryRows(t, s, queries)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("backfill changed current bytes:\ngot %v\nwant %v", got, want)
	}
	guardWrites := []string{
		`INSERT INTO worker_authorizations SELECT * FROM worker_authorizations LIMIT 1`,
		`UPDATE worker_authorizations SET seq=seq`,
		`DELETE FROM worker_authorizations`,
		`INSERT INTO workflow_step_epochs SELECT * FROM workflow_step_epochs LIMIT 1`,
		`UPDATE workflow_step_epochs SET attempt_epoch=attempt_epoch`,
		`DELETE FROM workflow_step_epochs`,
	}
	for _, query := range guardWrites {
		if _, err := s.db.Exec(query); err == nil || !strings.Contains(err.Error(), "is write-seam-only") {
			t.Fatalf("%s: %v; want a write-seam-only refusal", query, err)
		}
	}
}

func workerFactsQueryRows(t *testing.T, s *Store, queries []string) [][]string {
	t.Helper()
	result := make([][]string, len(queries))
	for i, query := range queries {
		rows, err := s.db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var raw string
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			result[i] = append(result[i], raw)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
	}
	return result
}
