package main

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/store"
)

// seedCLIOracleSettlementHistory extends the current-row dispatch fixture with
// an immutable job and a prior open finding. Job authoring and the earlier
// review's admission are not under test; all new dispatch and terminal writes
// below go through the authenticated CLI, without changing any admission gate.
func seedCLIOracleSettlementHistory(t *testing.T, dbPath string) store.WorkerJobBinding {
	t.Helper()
	job := store.WorkerJobBinding{JobID: "job:oracle-settlement", Revision: 1, Digest: "sha256:" + strings.Repeat("d", 64)}
	seedAuthorizedDispatchWindow(t, dbPath, "work-1", "attempt-1", job)
	oracle := map[string]any{
		"owners": []map[string]any{{
			"owner_id": "owner:repair", "domain_id": "repair",
			"mechanism":  map[string]any{"project_id": "project-1", "path": "repair.go", "entry_point": "Repair"},
			"obligation": "the controlled repair path passes", "predicate_ids": []string{"predicate:repair"},
		}},
		"cases": []map[string]any{{
			"case_id": "case:repair", "owner_id": "owner:repair", "entry_point": "controlled repair path",
			"input_class": "ordinary input", "expected_state": "repair passes", "control_ids": []string{"control:repair"},
		}},
		"controls": []map[string]any{{
			"control_id": "control:repair", "owner_id": "owner:repair", "predicate_ids": []string{"predicate:repair"},
			"case_ids": []string{"case:repair"}, "argv": []string{"go", "test", "-run", "^TestRepair$", "."}, "cwd": ".",
			"recipe_source":   map[string]any{"kind": "repository_file", "project_id": "project-1", "path": "repair_test.go", "commit_oid": strings.Repeat("a", 40)},
			"expected_result": "pass", "required_evidence_role": "reported", "readiness_evidence_refs": []string{"evidence:repair-ready"},
		}},
	}
	jobPayload := mustJSON(t, map[string]any{
		"job_id": job.JobID, "revision": job.Revision, "digest": job.Digest, "acceptance_oracle": oracle,
	})
	finding := store.WorkerOpenOracleFinding{
		FindingID: "finding:1:0", Severity: "P2", Detail: "the alternate entry path has no control",
		Classification: store.OracleClassificationUncoveredCase, OwnerID: "owner:repair", FailureFamily: "alternate-path",
		SourceEventID: "prior-review", SourceEventSeq: 1, SourceOrdinal: 0, SourceActorRef: "actor:prior-review",
		Tie: &store.WorkerOracleFinding{
			Classification: store.OracleClassificationUncoveredCase, OwnerID: "owner:repair",
			EntryPoint: "uncontrolled alternate path", FailureFamily: "alternate-path", EvidenceRefs: []string{"evidence:alternate-reproduction"},
		},
	}
	// Match the current fact encoding: SourceOracle is reconstructed from the
	// immutable source_job rather than stored in the aggregate.
	fact := struct {
		*store.WorkerOpenOracleFinding
		SourceOracle *store.AcceptanceOracle `json:"SourceOracle,omitempty"`
		SourceJob    *store.WorkerJobBinding `json:"source_job,omitempty"`
	}{WorkerOpenOracleFinding: &finding, SourceJob: &job}
	s := mustOpenStore(t, dbPath)
	defer s.Close()
	tx, err := s.DatabaseForTesting().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO write_guard(active) VALUES(1)`)
	exec(`UPDATE workflow_instances SET execution_started_at=started_at WHERE work_id='work-1'`)
	// The failure derivation requires the still-current step's start epoch,
	// not only the immutable authorization snapshot used by terminal ingest.
	exec(`INSERT INTO workflow_step_epochs(work_id,step_id,attempt_epoch,latest_start_seq,latest_start_event_id,latest_start_payload)
		SELECT work_id,'execution',1,start_seq,'dispatch-start-attempt-1',start_payload FROM worker_authorizations WHERE attempt_id='attempt-1'`)
	exec(`INSERT INTO worker_job_revisions(work_id,job_id,revision,contract_version,objective,stopping_condition,project_scope,path_scope,predicate_ids,checks,prerequisites,unresolved_refs,digest,state,recorded_at,recorded_payload)
		VALUES('work-1',?,1,1,'repair the alternate path','the path is controlled','project-1','["."]','["predicate:repair"]','[]','[]','[]',?,'recorded','2026-10-01T00:00:00Z',?)`, job.JobID, job.Digest, jobPayload)
	exec(`INSERT INTO worker_oracle_finding_facts(work_id,oracle_capable,findings,receipts) VALUES('work-1',1,?,'[]')`, mustJSON(t, []any{fact}))
	exec(`DELETE FROM write_guard`)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return job
}

func TestWorkerOracleRefusalCLIRollsBackNonceAndSettlesInvalidReportOnce(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	job := seedCLIOracleSettlementHistory(t, dbPath)
	key := seedWorkerEvidenceClient(t)
	lane := store.BuiltinLaneDefinitions()[0]
	readback := preferredLaneModel(lane)
	dispatch, dispatchAssertion := workerEvidenceRequest(t, agent.WorkerEvidenceVerbDispatch, lane, readback, "nonce-oracle-dispatch-0001")
	dispatch["worker_job"] = job
	dispatch["assertion"] = signWorkerEvidence(t, key, dispatchAssertion)
	if code, out, stderr := runWorkerCLI(t, "worker-dispatch", mustJSON(t, dispatch)); code != 0 {
		t.Fatalf("oracle dispatch exit=%d stdout=%s stderr=%s", code, out, stderr)
	}

	type snapshot struct {
		DispatchPayload, Findings, JobPayload string
		CapturedAt, StartedAt, ExecutionAt    string
		WorkLifecycle, WorkflowCompletedAt    string
		Receipts, Version, Attempts           int
	}
	read := func() snapshot {
		t.Helper()
		s := mustOpenStore(t, dbPath)
		defer s.Close()
		var value snapshot
		if err := s.DatabaseForTesting().QueryRow(`SELECT a.dispatch_payload,f.findings,j.recorded_payload,w.created_at,i.started_at,i.execution_started_at,w.lifecycle,coalesce(i.completed_at,''),
			(SELECT count(*) FROM effect_receipts),w.version,(SELECT count(*) FROM worker_attempts WHERE work_id='work-1')
			FROM worker_attempts a JOIN work_items w ON w.id=a.work_id
			JOIN workflow_instances i ON i.work_id=a.work_id
			JOIN worker_oracle_finding_facts f ON f.work_id=a.work_id
			JOIN worker_job_revisions j ON j.work_id=a.work_id AND j.job_id=? WHERE a.attempt_id='attempt-1'`, job.JobID).
			Scan(&value.DispatchPayload, &value.Findings, &value.JobPayload, &value.CapturedAt, &value.StartedAt, &value.ExecutionAt, &value.WorkLifecycle, &value.WorkflowCompletedAt, &value.Receipts, &value.Version, &value.Attempts); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := read()
	if before.CapturedAt != "2026-08-22T00:00:00Z" || before.StartedAt != before.CapturedAt || before.ExecutionAt != before.StartedAt {
		t.Fatalf("fixture lost its original capture/execution anchors: %+v", before)
	}
	request, assertion := workerEvidenceRequest(t, agent.WorkerEvidenceVerbComplete, lane, readback, "nonce-oracle-refused-0001")
	request["event_id"] = "complete-oracle-refused"
	request["worker_job"] = job
	head := runGitCLI(t, cliFixtureWorktree(t, dbPath), "rev-parse", "HEAD")
	tree := runGitCLI(t, cliFixtureWorktree(t, dbPath), "rev-parse", "HEAD^{tree}")
	candidate := map[string]any{"project_id": "project-1", "commit": head, "tree": tree}
	request["review"] = map[string]any{
		"disposition": "ready_for_final_proof", "findings": []any{}, "input_candidate": candidate, "output_candidate": candidate,
		"dependencies": map[string]any{"criteria": []map[string]any{{"predicate_id": "predicate:repair", "content_digest": "sha256:" + strings.Repeat("e", 64)}}, "architecture_not_applicable": true},
		"assessments":  []any{}, "remaining_obligations": []any{},
		"resolved_findings": []map[string]any{{"finding_id": "finding:1:0", "evidence_refs": []string{"evidence:alternate-reproduction"}}},
	}
	request["assertion"] = signWorkerEvidence(t, key, assertion)
	refusedRequest := mustJSON(t, request)
	code, out, stderr := runWorkerCLI(t, "worker-complete", refusedRequest)
	const detail = "uncovered_case entry path is still absent from the controlled inventory"
	if code == 0 || !strings.Contains(out+stderr, detail) {
		t.Fatalf("completion did not reach the fixed oracle contradiction: exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	kind, effect, retry := typedWorkerEvidenceError(t, out)
	// Keep this nonfatal so the red run also exercises rollback and settlement.
	if kind != string(store.KindInvalidPayload) || effect != "none" || retry {
		t.Errorf("oracle refusal kind/effect/retry = %s/%s/%v, want invalid_payload/none/false; stdout=%s", kind, effect, retry, out)
	}
	assertZeroTerminalEffects(t, dbPath, assertion.Nonce)
	if after := read(); !reflect.DeepEqual(before, after) {
		t.Fatalf("refused completion changed current history: before=%+v after=%+v", before, after)
	}
	// Reusing the exact signed completion proves its nonce was rolled back:
	// it reaches the same oracle guard again, not the nonce replay guard.
	code, repeated, stderr := runWorkerCLI(t, "worker-complete", refusedRequest)
	if code == 0 || !strings.Contains(repeated+stderr, detail) || strings.Contains(repeated+stderr, "nonce replayed") {
		t.Fatalf("rolled-back completion nonce could not reach the same guard: exit=%d stdout=%s stderr=%s", code, repeated, stderr)
	}
	assertZeroTerminalEffects(t, dbPath, assertion.Nonce)

	// The adapter hashes the exact refused request, including its assertion.
	sum := sha256.Sum256([]byte(refusedRequest))
	observation := &store.WorkerFailureObservation{
		Observer: store.WorkerFailureObserverHostAdapter, Phase: store.WorkerFailurePhaseEvidenceWrite,
		Cause: store.WorkerFailureCauseReportContract, Basis: store.WorkerFailureBasisHypothesis,
		Sources: []store.WorkerFailureSource{{Kind: store.WorkerFailureSourceValidator, Digest: "sha256:" + hex.EncodeToString(sum[:]), Message: detail}},
	}
	digest, err := store.WorkerFailureObservationDigest(observation)
	if err != nil {
		t.Fatal(err)
	}
	failure, failureAssertion := workerEvidenceRequest(t, agent.WorkerEvidenceVerbFail, lane, readback, "nonce-oracle-failure-0001")
	failure["event_id"] = "failed-oracle-report"
	failure["failure_kind"] = store.WorkerFailureInvalidReport
	failure["detail"] = detail
	failure["failure_attribution"] = observation
	failureAssertion.FailureKind = store.WorkerFailureInvalidReport
	failureAssertion.Version = agent.WorkerEvidenceAssertionVersion2
	failureAssertion.ObservationDigest = digest
	failure["assertion"] = signWorkerEvidence(t, key, failureAssertion)
	failureRequest := mustJSON(t, failure)
	tampered := *observation
	tampered.Sources = append([]store.WorkerFailureSource(nil), observation.Sources...)
	tampered.Sources[0].Digest = "sha256:" + strings.Repeat("f", 64)
	failure["failure_attribution"] = &tampered
	if code, out, stderr := runWorkerCLI(t, "worker-fail", mustJSON(t, failure)); code == 0 || !strings.Contains(out+stderr, "does not bind this attempt identity") {
		t.Fatalf("substituted refused-request digest exit=%d stdout=%s stderr=%s, want signature binding refusal", code, out, stderr)
	}
	assertZeroTerminalEffects(t, dbPath, failureAssertion.Nonce)
	failure["failure_attribution"] = observation
	if code, out, stderr := runWorkerCLI(t, "worker-fail", failureRequest); code != 0 {
		t.Fatalf("signed invalid-report settlement exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	if got := attributionNonceRows(t, dbPath, failureAssertion.Nonce); got != 1 {
		t.Fatalf("settlement nonce rows=%d, want one", got)
	}
	block := attributionStoredFailureAttribution(t, dbPath, "attempt-1")
	if block.Origin != store.WorkerFailureAttributionRecorded || !reflect.DeepEqual(block.Observation, observation) ||
		block.Identity == nil || block.Identity.Attempt == nil || block.Identity.Attempt.AttemptID != "attempt-1" ||
		block.Identity.Attempt.LaneID != lane.ID || block.Identity.Attempt.Epoch != 1 ||
		block.Identity.Attempt.PacketDigest != dispatchAssertion.PacketDigest || block.Identity.Attempt.NoJob || !reflect.DeepEqual(block.Identity.Attempt.Job, &job) {
		t.Fatalf("settlement did not retain signed observation and dispatch identity: %+v", block)
	}
	version, payload := storedTerminalEvent(t, dbPath, "failed-oracle-report")
	settled := read()
	if settled.Findings != before.Findings || settled.JobPayload != before.JobPayload || settled.DispatchPayload != before.DispatchPayload || settled.Attempts != before.Attempts {
		t.Fatal("settlement changed the open finding, immutable job, or original dispatch")
	}
	if settled.CapturedAt != before.CapturedAt || settled.StartedAt != before.StartedAt || settled.ExecutionAt != before.ExecutionAt || settled.WorkLifecycle != "in_progress" || settled.WorkflowCompletedAt != "" {
		t.Fatalf("failed report settlement reset timing or claimed work completion: before=%+v after=%+v", before, settled)
	}
	if code, out, stderr := runWorkerCLI(t, "worker-fail", failureRequest); code == 0 || !strings.Contains(out+stderr, "nonce replayed") {
		t.Fatalf("settlement replay exit=%d stdout=%s stderr=%s, want nonce replay refusal", code, out, stderr)
	}
	failureAssertion.Nonce = "nonce-oracle-failure-ack01"
	failure["assertion"] = signWorkerEvidence(t, key, failureAssertion)
	if code, out, stderr := runWorkerCLI(t, "worker-fail", mustJSON(t, failure)); code != 0 {
		t.Fatalf("fresh signed exact acknowledgment exit=%d stdout=%s stderr=%s", code, out, stderr)
	}
	afterVersion, afterPayload := storedTerminalEvent(t, dbPath, "failed-oracle-report")
	if afterVersion != version || string(afterPayload) != string(payload) || !reflect.DeepEqual(settled, read()) {
		t.Fatal("exact acknowledgment changed the terminal payload or current history")
	}
	if attributionNonceRows(t, dbPath, assertion.Nonce) != 0 || attributionNonceRows(t, dbPath, dispatchAssertion.Nonce) != 1 || attributionNonceRows(t, dbPath, failureAssertion.Nonce) != 1 {
		t.Fatal("settlement changed the rolled-back completion nonce or lost a committed dispatch/acknowledgment nonce")
	}
	s := mustOpenStore(t, dbPath)
	defer s.Close()
	var state, failureKind string
	var terminals, completed, derived int
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state,failure_kind,failure_recorded,
		(SELECT count(*) FROM effect_receipts WHERE kind IN (?,?)),
		(SELECT count(*) FROM effect_receipts WHERE kind=?) FROM worker_attempts WHERE attempt_id='attempt-1'`,
		store.WorkerCompleted, store.WorkerFailed, store.WorkerCompleted).Scan(&state, &failureKind, &derived, &terminals, &completed); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || failureKind != store.WorkerFailureInvalidReport || derived != 1 || terminals != 1 || completed != 0 {
		t.Fatalf("settlement state/kind/derived/terminals/completed=%s/%s/%d/%d/%d, want failed/invalid_report/1/1/0", state, failureKind, derived, terminals, completed)
	}
	var failedAt string
	if err := s.DatabaseForTesting().QueryRow(`SELECT failed_at FROM worker_attempts WHERE attempt_id='attempt-1'`).Scan(&failedAt); err != nil {
		t.Fatal(err)
	}
	start, err := time.Parse(time.RFC3339Nano, settled.ExecutionAt)
	if err != nil {
		t.Fatal(err)
	}
	end, err := time.Parse(time.RFC3339Nano, failedAt)
	if err != nil || end.Sub(start) < 45*time.Minute {
		t.Fatalf("failed/slow history lost its original elapsed interval: start=%s failed=%s err=%v", settled.ExecutionAt, failedAt, err)
	}
	t.Log("authenticated refused completion rolled back nonce and terminal; signed invalid_report settled once, retained the open finding and original job/dispatch, and recorded no successful completion")
}
