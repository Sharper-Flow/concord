package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func seedWorkflowVerdictBatchFixture(t *testing.T, workID string) workflowReturnRouteFixture {
	t.Helper()
	return seedWorkflowReturnRouteFixtureWithDefinition(t, workID, mustBuiltinDefinition(t, "workflow.implementation"), "acceptance", []string{"verification"}, []string{"verification", "review", "artifact"}, batchVerdictContractPredicates(workID)...)
}

func batchVerdictContractPredicates(workID string) []map[string]any {
	var predicates []map[string]any
	for ordinal, id := range []string{"batch-present", "batch-absent", "batch-third"} {
		predicates = append(predicates, map[string]any{"predicate_id": "predicate:" + id, "ordinal": ordinal, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:" + id, "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"}})
	}
	return predicates
}

// The fixture captures VerifyWorktree receipts and an independently dispatched
// Review. No caller verdict or evidence-kind binding can substitute for them.
func pReceiptFixture(t *testing.T, workID string, criteria, receiptsPerCriterion int, beforeProduction ...func(workContextFixture)) (workContextFixture, string, [][]string) {
	t.Helper()
	var predicates []map[string]any
	for i := 0; i < criteria; i++ {
		predicates = append(predicates, map[string]any{"predicate_id": fmt.Sprintf("predicate:p%d", i), "ordinal": i, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": fmt.Sprintf("check:p%d", i), "immutable_subject_ref": "commit:fixture", "expected_result": "pass"}})
	}
	route := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, mustBuiltinDefinition(t, "workflow.implementation"), "execution", []string{}, []string{}, predicates...)
	f := workContextFixture{store: route.store, workID: workID, owner: route.owner, operator: route.operator}
	t.Cleanup(func() { _ = f.store.Close() })
	for _, setup := range beforeProduction {
		setup(f)
	}
	repo, _ := reviewSubjectClaim(t, f.store, workID)
	var branch string
	if err := f.store.db.QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(workID)).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, repo, "branch", "-m", branch)
	j := &candidateJourney{fixture: f, s: f.store, workID: workID, repo: repo}
	job := reviewSubjectReadyRevision(t, f)
	if err := descendantDispatch(t, j, "implement", "p:implementation", job, nil); err != nil {
		t.Fatal(err)
	}
	j.laneDispatch(t, "p:implementation", "implementation", &job)
	editDir := repo + "/internal/store/" + job.JobID
	if err := os.MkdirAll(editDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(editDir+"/value.go", []byte("package fixture\nconst Value = 7\n"), 0600); err != nil {
		t.Fatal(err)
	}
	j.commit(t, "Concord Test", "Implement the checked fixture")
	j.completeLane(t, "p:implementation", "implementation", &job, nil)
	if err := j.action(t, "accept_worker_result", map[string]any{"attempt_id": "p:implementation", "attempt_epoch": j.dispatchEpoch(t, "p:implementation")}, f.owner); err != nil {
		t.Fatal(err)
	}
	refs := make([][]string, criteria)
	for i := range refs {
		for k := 0; k < receiptsPerCriterion; k++ {
			lease := fmt.Sprintf("sha256:%064x:worktree-verify:%s", i*receiptsPerCriterion+k+1, workID)
			result, err := f.store.VerifyWorktree(t.Context(), WorktreeVerifyRequest{Owner: SessionWorktreeOwner{ClientRef: f.owner.ClientRef, AgentRef: f.owner.AgentRef, SessionRef: f.owner.SessionRef}, WorkID: workID, ProjectID: "project", LeaseID: lease, PrincipalRef: f.owner.PrincipalRef, RequestID: fmt.Sprintf("request:p:%d:%d", i, k), Now: time.Now().UTC(), Runner: ExecGitRunner{}, Command: []string{"true"}, RunCommand: func(context.Context, string, []string, int) (int, []byte, bool, error) { return 0, nil, false, nil }})
			if err != nil || result.ExitCode != 0 || result.TrackedFilesChanged {
				t.Fatalf("capture criterion %d receipt %d: %+v %v", i, k, result, err)
			}
			refs[i] = append(refs[i], verifyOperationRef(lease))
		}
	}
	pAcceptReceiptReview(t, f, repo, refs)
	if err := runVerdictActionAs(t, f.store, workID, "bind_evidence", mustJSONValue(map[string]any{"evidence_kind": "verification", "immutable_subject_ref": refs[0][0], "producer_id": f.owner.PrincipalRef, "producer_run_ref": refs[0][0], "producer_watermark": "request:p:0:0"}), 0, f.owner); err != nil {
		t.Fatal(err)
	}
	owner, _ := WorkflowActorRef(f.owner)
	if err := advanceWorkflowTestInstanceToStep(t.Context(), f.store, workID, "acceptance", owner); err != nil {
		t.Fatal(err)
	}
	return f, repo, refs
}

func pAcceptReceiptReview(t *testing.T, f workContextFixture, repo string, refs [][]string) {
	t.Helper()
	j := &candidateJourney{fixture: f, s: f.store, workID: f.workID, repo: repo}
	job := reviewSubjectReadyRevision(t, f)
	attempt := "p:review:" + job.JobID
	if err := descendantDispatch(t, j, "review", attempt, job, nil); err != nil {
		t.Fatal(err)
	}
	j.laneDispatch(t, attempt, "review", &job)
	report := currentReviewBlockForTest(t, f.store, f.workID, attempt, "ready_for_final_proof")
	for i, proof := range refs {
		report.Assessments = append(report.Assessments, WorkerReviewAssessment{PredicateID: fmt.Sprintf("predicate:p%d", i), Assessment: "supported", ReceiptRefs: proof})
	}
	lane := reviewGateLane(t, "review")
	event := jobBoundCompletionEvent(f.workID, attempt+":completed", attempt, lane, time.Now().UTC(), &job, repo)
	var payload WorkerCompletedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload.Review = report
	event.Payload = mustJSONValue(payload)
	prepared, err := PrepareWorkerTerminalDelivery(t.Context(), f.store, PrepareWorkerTerminalDeliveryRequest{WorkID: f.workID, AttemptID: attempt, EventID: event.EventID})
	if err != nil {
		t.Fatal(err)
	}
	workerTerminalEventThroughSeamCtx(t, WithWorkerTerminalPreparation(t.Context(), prepared), f.store, event)
	if err := j.action(t, "accept_worker_result", map[string]any{"attempt_id": attempt, "attempt_epoch": j.dispatchEpoch(t, attempt)}, f.owner); err != nil {
		t.Fatal(err)
	}
}

func pReceiptBatch(refs [][]string, indexes ...int) json.RawMessage {
	entries := make([]map[string]any, 0, len(indexes))
	for _, i := range indexes {
		entries = append(entries, map[string]any{"predicate_id": fmt.Sprintf("predicate:p%d", i), "receipt_refs": refs[i]})
	}
	return mustJSONValue(map[string]any{"verdicts": entries})
}

func pConfirmReceiptPremise(t *testing.T, f workContextFixture, repo string) error {
	t.Helper()
	ctx := t.Context()
	prepared, err := PrepareWorkflowAcceptanceFreshness(ctx, f.store, PrepareWorkerDispatchRequest{WorkID: f.workID, SessionWorktree: repo})
	if err != nil {
		return err
	}
	ctx = WithWorkerDispatchPreparation(ctx, prepared)
	version := readWorkVersion(t, f.store, f.workID)
	id := fmt.Sprintf("confirm-%s-%d", f.workID, version)
	return f.store.Transact(ctx, func(tx *Transaction) error {
		_, err := ApplyWorkflowActionTx(ctx, tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
			WorkID: f.workID, ExpectedVersion: version, ActionID: "confirm_premise", Payload: json.RawMessage(`{}`), Actor: f.owner, SelectedChoice: "confirm",
			AcceptedInputsDigest: nativeDigest([]byte(id)), IdempotencyIdentity: id, OperationID: id,
			PrincipalRef: f.owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: id, RequestID: "request:" + id, ContractDigest: testManifestDigest, Now: time.Now().UTC(),
		})
		return err
	})
}

func TestWorkflowVerdictBatchParity(t *testing.T) {
	f, _, refs := pReceiptFixture(t, "batch-parity-current", 3, 1)
	// A single association is a one-entry batch, not the retired scalar form.
	for _, indexes := range [][]int{{0}, {1}} {
		if err := runVerdictActionAs(t, f.store, f.workID, "record_verdict", pReceiptBatch(refs, indexes...), 0, f.owner); err != nil {
			t.Fatalf("one-entry batch: %v", err)
		}
	}
	before, err := latestWorkflowVerdicts(t.Context(), f.store.db, f.workID, 1)
	if err != nil || len(before) != 2 {
		t.Fatalf("single associations = %+v: %v", before, err)
	}
	if err := runVerdictActionAs(t, f.store, f.workID, "record_verdict", pReceiptBatch(refs, 0, 1), 0, f.owner); err != nil {
		t.Fatalf("two-entry batch: %v", err)
	}
	var eventIDs []string
	rows, err := f.store.db.Query(`SELECT event_id FROM effect_receipts WHERE subject_id=? AND kind=? ORDER BY effect_seq DESC LIMIT 2`, f.workID, WorkflowVerdictRecorded)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		eventIDs = append(eventIDs, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(eventIDs) != 2 || !strings.HasSuffix(eventIDs[0], ":semantic:verdict:1") || !strings.HasSuffix(eventIDs[1], ":semantic:verdict:0") || strings.TrimSuffix(eventIDs[0], ":semantic:verdict:1") != strings.TrimSuffix(eventIDs[1], ":semantic:verdict:0") {
		t.Fatalf("batch event ids = %v, want the operation-derived per-entry pair", eventIDs)
	}
	after, err := latestWorkflowVerdicts(t.Context(), f.store.db, f.workID, 1)
	for i := range before {
		before[i].WorkflowVersionFields = WorkflowVersionFields{}
	}
	for i := range after {
		after[i].WorkflowVersionFields = WorkflowVersionFields{}
	}
	if err != nil || string(mustJSONValue(before)) != string(mustJSONValue(after)) {
		t.Fatalf("batch differs from separate associations: before=%+v after=%+v err=%v", before, after, err)
	}
	owner, err := WorkflowActorRef(f.owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range after {
		if v.VerdictActorRef != owner {
			t.Fatalf("association actor=%s, want Main %s", v.VerdictActorRef, owner)
		}
	}
	missing, err := missingPredicateVerdictsCtx(t, f.store, f.workID)
	if err != nil || !slices.Equal(missing, []string{"predicate:p2"}) {
		t.Fatalf("parity omitted criterion=%v err=%v", missing, err)
	}
	pAssertBatchAssociations(t, f, 2)
}

func TestWorkflowVerdictBatchAtomicRefusal(t *testing.T) {
	f, _, refs := pReceiptFixture(t, "batch-atomic-current", 2, 1)
	entry := func(id string, proof []string) map[string]any {
		return map[string]any{"predicate_id": id, "receipt_refs": proof}
	}
	valid := entry("predicate:p0", refs[0])
	for _, tc := range []struct {
		name    string
		entries []map[string]any
		kind    FailureKind
	}{
		{"duplicate", []map[string]any{valid, valid}, KindInvalidPayload},
		{"unapproved last", []map[string]any{valid, entry("predicate:foreign", refs[1])}, KindInvalidPayload},
		{"unproved last", []map[string]any{valid, entry("predicate:p1", []string{"worktree_verify:missing"})}, KindMissingEvidence},
		{"empty last", []map[string]any{valid, entry("predicate:p1", []string{})}, KindInvalidPayload},
		{"missing last receipts", []map[string]any{valid, {"predicate_id": "predicate:p1"}}, KindInvalidPayload},
		{"caller verdict", []map[string]any{valid, {"predicate_id": "predicate:p1", "receipt_refs": refs[1], "verdict_kind": "ok"}}, KindInvalidPayload},
		{"unknown entry field", []map[string]any{valid, {"predicate_id": "predicate:p1", "receipt_refs": refs[1], "ordinal": 1}}, KindInvalidPayload},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := mustJSONValue(map[string]any{"verdicts": tc.entries})
			before := contractCorrectionEffectSnapshot(t, f.store, f.workID)
			preflight := WorkflowActionPreflightWithRegistry(t.Context(), f.store, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{WorkID: f.workID, ActionID: "record_verdict", Payload: payload, Actor: f.owner})
			err := runVerdictActionAs(t, f.store, f.workID, "record_verdict", payload, 0, f.owner)
			if !hasFailureKind(preflight, tc.kind) || !hasFailureKind(err, tc.kind) {
				t.Fatalf("preflight=%v mutation=%v want %s", preflight, err, tc.kind)
			}
			if after := contractCorrectionEffectSnapshot(t, f.store, f.workID); after != before {
				t.Fatal("refused batch changed durable state")
			}
		})
	}
	for _, payload := range []json.RawMessage{
		mustJSONValue(map[string]any{"predicate_id": "predicate:p0", "verdicts": []map[string]any{valid}}),
		json.RawMessage(`{"verdict_kind":"ok"}`),
		mustJSONValue(map[string]any{"receipt_refs": refs[0], "verdicts": []map[string]any{valid}}),
	} {
		assertRecoveryRefusalWithoutEffect(t, f.store, f.workID, KindInvalidPayload, func() error { return runVerdictActionAs(t, f.store, f.workID, "record_verdict", payload, 0, f.owner) })
	}
	// Main may have executed; a dispatched worker still cannot associate proof.
	var worker WorkflowActor
	if err := f.store.db.QueryRow(`SELECT principal_ref,client_ref,agent_ref,session_ref,actor_class FROM workflow_actors WHERE actor_ref=(SELECT json_extract(dispatch_payload,'$.lane_actor_ref') FROM worker_attempts WHERE work_id=? AND attempt_id='p:implementation')`, f.workID).Scan(&worker.PrincipalRef, &worker.ClientRef, &worker.AgentRef, &worker.SessionRef, &worker.ActorClass); err != nil {
		t.Fatal(err)
	}
	assertRecoveryRefusalWithoutEffect(t, f.store, f.workID, KindUnauthorized, func() error {
		return runVerdictActionAs(t, f.store, f.workID, "record_verdict", pReceiptBatch(refs, 0, 1), 0, worker)
	})
	if err := runVerdictActionAs(t, f.store, f.workID, "record_verdict", pReceiptBatch(refs, 0, 1), 0, f.owner); err != nil {
		t.Fatal(err)
	}
	pAssertBatchAssociations(t, f, 2)
}

func TestWorkflowVerdictBatchLateRecoveryResolvesActiveContract(t *testing.T) {
	f, ref := seedLateCriterionAssociation(t, "batch-late-current")
	// Isolate active-version resolution from the separate contract-return journey.
	if _, err := f.store.db.Exec(`INSERT INTO write_guard(active) VALUES(1);
		INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class,decision_domains,definition_ref,definition_version,definition_digest)
		SELECT work_id,2,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class,decision_domains,definition_ref,definition_version,definition_digest FROM workflow_contracts WHERE work_id=? AND contract_version=1;
		UPDATE workflow_contracts SET superseded_by=2 WHERE work_id=? AND contract_version=1;
		INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) SELECT work_id,2,predicate_id,ordinal,outcome_kind,outcome_payload FROM workflow_contract_predicates WHERE work_id=? AND contract_version=1;
		DELETE FROM write_guard`, "batch-late-current", "batch-late-current", "batch-late-current"); err != nil {
		t.Fatal(err)
	}
	if err := runVerdictActionAs(t, f.store, "batch-late-current", "record_verdict", criterionAssociationPayload(ref, "predicate:first"), 0, f.owner); err != nil {
		t.Fatal(err)
	}
	payload := criterionAssociationPayload(ref, "predicate:first", "predicate:second")
	before := contractCorrectionEffectSnapshot(t, f.store, "batch-late-current")
	preflight := WorkflowActionPreflightWithRegistry(t.Context(), f.store, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{WorkID: "batch-late-current", ActionID: "record_verdict", Payload: payload, Actor: f.owner})
	err := runVerdictActionAs(t, f.store, "batch-late-current", "record_verdict", payload, 0, f.owner)
	if !hasFailureKind(preflight, KindIllegalLifecycleTransition) || !hasFailureKind(err, KindIllegalLifecycleTransition) {
		t.Fatalf("mixed late batch preflight=%v mutation=%v", preflight, err)
	}
	if after := contractCorrectionEffectSnapshot(t, f.store, "batch-late-current"); after != before {
		t.Fatal("mixed late batch changed durable state")
	}
	payload = criterionAssociationPayload(ref, "predicate:second")
	if err := WorkflowActionPreflightWithRegistry(t.Context(), f.store, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{WorkID: "batch-late-current", ActionID: "record_verdict", Payload: payload, Actor: f.owner}); err != nil {
		t.Fatal(err)
	}
	if err := runVerdictActionAs(t, f.store, "batch-late-current", "record_verdict", payload, 0, f.owner); err != nil {
		t.Fatal(err)
	}
	verdicts, err := latestWorkflowVerdicts(t.Context(), f.store.db, "batch-late-current", 2)
	if err != nil || len(verdicts) != 2 {
		t.Fatalf("active associations=%+v err=%v", verdicts, err)
	}
	for _, v := range verdicts {
		if v.ContractVersion != 2 || v.CriterionDigest == "" || !slices.Equal(v.EvaluationEvidence, []string{ref}) {
			t.Fatalf("association did not capture active criterion and exact proof: %+v", v)
		}
	}
	if step := currentStep(t, f.store, "batch-late-current"); step != "complete" {
		t.Fatalf("late association moved step to %s", step)
	}
}

func TestWorkflowVerdictBatchOmissionStaysMissing(t *testing.T) {
	f, repo, refs := pReceiptFixture(t, "batch-omission-current", 3, 1)
	if err := runVerdictActionAs(t, f.store, f.workID, "record_verdict", pReceiptBatch(refs, 0, 1), 0, f.owner); err != nil {
		t.Fatal(err)
	}
	missing, err := missingPredicateVerdictsCtx(t, f.store, f.workID)
	if err != nil || !slices.Equal(missing, []string{"predicate:p2"}) {
		t.Fatalf("missing=%v err=%v", missing, err)
	}
	seedComparisonObservation(t, f.store, f.workID)
	err = runIssue933OperatorAction(t, f.store, f.workID, "confirm_premise", json.RawMessage(`{}`), f.owner, f.operator)
	if !hasFailureKind(err, KindMissingEvidence) || !strings.Contains(err.Error(), "predicate:p2") {
		t.Fatalf("confirmation did not name omitted criterion: %v", err)
	}
	if step := currentStep(t, f.store, f.workID); step != "acceptance" {
		t.Fatalf("refused confirmation moved to %s", step)
	}
	if err := runVerdictActionAs(t, f.store, f.workID, "record_verdict", pReceiptBatch(refs, 2), 0, f.owner); err != nil {
		t.Fatal(err)
	}
	missing, err = missingPredicateVerdictsCtx(t, f.store, f.workID)
	if err != nil || len(missing) != 0 {
		t.Fatalf("full coverage still missing=%v err=%v", missing, err)
	}
	if err := pConfirmReceiptPremise(t, f, repo); err != nil {
		t.Fatalf("confirm after full coverage: %v", err)
	}
	if step := currentStep(t, f.store, f.workID); step != "release" {
		t.Fatalf("confirmation moved to %s, want release", step)
	}
}

func pAssertBatchAssociations(t *testing.T, f workContextFixture, count int) {
	t.Helper()
	before, err := latestWorkflowVerdicts(t.Context(), f.store.db, f.workID, 1)
	if err != nil || len(before) != count {
		t.Fatalf("associations=%+v want %d: %v", before, count, err)
	}
	for _, association := range before {
		if err := verifyWorkflowCriterionAssociation(t.Context(), f.store.db, f.workID, association); err != nil {
			t.Fatalf("association lost criterion proof: %v", err)
		}
	}
	var eventsBefore int
	if err := f.store.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, f.workID, WorkflowVerdictRecorded).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	if eventsBefore < count {
		t.Fatalf("verdict event count %d, want at least %d", eventsBefore, count)
	}
}

func pAssertReceiptUnion(t *testing.T, criteria, perCriterion int) {
	t.Helper()
	f, _, refs := pReceiptFixture(t, fmt.Sprintf("batch-union-%d-%d", criteria, perCriterion), criteria, perCriterion)
	indexes := make([]int, criteria)
	var want []string
	for i := range indexes {
		indexes[i] = i
		want = append(want, refs[i]...)
	}
	version := readWorkVersion(t, f.store, f.workID)
	op := fmt.Sprintf("record_verdict-%s-%d", f.workID, version)
	if err := runVerdictActionAs(t, f.store, f.workID, "record_verdict", pReceiptBatch(refs, indexes...), 0, f.owner); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := f.store.db.QueryRow(`SELECT evidence_refs FROM durable_operations WHERE op_id=?`, op).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) || len(got) != criteria*perCriterion {
		t.Fatalf("durable union=%v want %v", got, want)
	}
	var completed struct {
		VerdictEntryCount  int      `json:"verdict_entry_count"`
		ResultEvidenceRefs []string `json:"result_evidence_refs"`
	}
	if err := f.store.db.QueryRow(`SELECT payload FROM domain_events WHERE event_id=?`, op+":completed").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &completed); err != nil {
		t.Fatal(err)
	}
	slices.Sort(completed.ResultEvidenceRefs)
	if completed.VerdictEntryCount != criteria || !slices.Equal(completed.ResultEvidenceRefs, want) {
		t.Fatalf("completion lost union or count: %+v", completed)
	}
	var recorded int
	if err := f.store.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, f.workID, WorkflowVerdictRecorded).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != criteria {
		t.Fatalf("batch recorded %d verdict events, want %d", recorded, criteria)
	}
	pAssertBatchAssociations(t, f, criteria)
}

func TestWorkflowVerdictBatchEvidenceUnionIsTheDurableAuthority(t *testing.T) {
	pAssertReceiptUnion(t, 2, 17)
}
func TestWorkflowVerdictBatchMaximumUnionRebuilds(t *testing.T) { pAssertReceiptUnion(t, 8, 32) }

// Missing proof must not inherit another entry's receipts or mint evidence.
func TestWorkflowVerdictBatchMixedDefaultEvidenceIsPerEntry(t *testing.T) {
	f, _, refs := pReceiptFixture(t, "batch-no-default-current", 2, 1)
	payload := mustJSONValue(map[string]any{"verdicts": []map[string]any{{"predicate_id": "predicate:p0", "receipt_refs": refs[0]}, {"predicate_id": "predicate:p1"}}})
	assertRecoveryRefusalWithoutEffect(t, f.store, f.workID, KindInvalidPayload, func() error { return runVerdictActionAs(t, f.store, f.workID, "record_verdict", payload, 0, f.owner) })
	if err := runVerdictActionAs(t, f.store, f.workID, "record_verdict", pReceiptBatch(refs, 0, 1), 0, f.owner); err != nil {
		t.Fatal(err)
	}
	verdicts, err := latestWorkflowVerdicts(t.Context(), f.store.db, f.workID, 1)
	if err != nil || len(verdicts) != 2 {
		t.Fatalf("associations=%v err=%v", verdicts, err)
	}
	for _, v := range verdicts {
		var i int
		if _, err := fmt.Sscanf(v.PredicateID, "predicate:p%d", &i); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(v.EvaluationEvidence, refs[i]) {
			t.Fatalf("entry borrowed proof: %+v", v)
		}
	}
	var minted int
	if err := f.store.db.QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.producer_run_ref') LIKE 'record_verdict-%'`, f.workID, WorkflowEvidenceBound).Scan(&minted); err != nil || minted != 0 {
		t.Fatalf("association minted %d bindings: %v", minted, err)
	}
}

func TestWorkflowVerdictBatchEvidenceUnionSpansNativeRunEnrichment(t *testing.T) {
	f, _, refs := pReceiptFixture(t, "batch-native-not-proof", 2, 1)
	capture := seedVerifiedNativeRunCapture(t, f.store, f.workID)
	bad := [][]string{refs[0], {capture}}
	assertRecoveryRefusalWithoutEffect(t, f.store, f.workID, KindMissingEvidence, func() error {
		return runVerdictActionAs(t, f.store, f.workID, "record_verdict", pReceiptBatch(bad, 0, 1), 0, f.owner)
	})
	if err := runVerdictActionAs(t, f.store, f.workID, "record_verdict", pReceiptBatch(refs, 0, 1), 0, f.owner); err != nil {
		t.Fatal(err)
	}
	verdicts, err := latestWorkflowVerdicts(t.Context(), f.store.db, f.workID, 1)
	if err != nil || len(verdicts) != 2 {
		t.Fatalf("associations=%v err=%v", verdicts, err)
	}
	for _, v := range verdicts {
		var i int
		if _, err := fmt.Sscanf(v.PredicateID, "predicate:p%d", &i); err != nil {
			t.Fatal(err)
		}
		if slices.Contains(v.EvaluationEvidence, capture) || !slices.Equal(v.EvaluationEvidence, refs[i]) {
			t.Fatalf("native-run capture changed exact criterion proof: %+v", v)
		}
	}
}

func missingPredicateVerdictsCtx(t *testing.T, s *Store, workID string) ([]string, error) {
	t.Helper()
	return missingPredicateVerdicts(t.Context(), s.db, workID)
}

func missingPredicateVerdicts(ctx context.Context, tx queryer, workID string) ([]string, error) {
	contract, _, err := workflowCompletionContract(ctx, tx, BuiltinWorkflowRegistry(), workID)
	if err != nil {
		return nil, err
	}
	return missingPredicateVerdictsForContract(ctx, tx, workID, contract)
}
