package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// qCurrentAcceptance records a current implementation candidate, a captured
// command receipt, and an accepted Review assessment before associating proof.
// It leaves extra evidence kinds outstanding without changing frozen scenarios.
func qCurrentAcceptance(t *testing.T, workID string, extraRequiredKinds ...string) acceptanceRecoveryFixture {
	t.Helper()
	return qCurrentAcceptanceWithDelivery(t, workID, "", extraRequiredKinds...)
}

func qCurrentAcceptanceWithDelivery(t *testing.T, workID, artifact string, extraRequiredKinds ...string) acceptanceRecoveryFixture {
	t.Helper()
	return qBuildAcceptance(t, workID, artifact, true, extraRequiredKinds...)
}

func qBuildAcceptance(t *testing.T, workID, artifact string, associate bool, extraRequiredKinds ...string) acceptanceRecoveryFixture {
	t.Helper()
	return qBuildAcceptancePredicates(t, workID, artifact, associate, extraRequiredKinds)
}

func qBuildAcceptancePredicates(t *testing.T, workID, artifact string, associate bool, extraRequiredKinds []string, predicates ...map[string]any) acceptanceRecoveryFixture {
	t.Helper()
	definition, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	route := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, definition, "execution", append([]string{"verification"}, extraRequiredKinds...), []string{"verification", "review", "artifact"}, predicates...)
	f := workContextFixture{store: route.store, workID: workID, owner: route.owner, operator: route.operator}
	repo, _ := reviewSubjectClaim(t, f.store, workID)
	var branch string
	if err := f.store.db.QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(workID)).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, repo, "branch", "-m", branch)
	j := &candidateJourney{fixture: f, s: f.store, workID: workID, repo: repo, acceptor: f.owner}
	run, err := f.store.VerifyWorktree(t.Context(), j.request("q-proof", nil))
	if err != nil || run.ExitCode != 0 {
		t.Fatalf("capture candidate command receipt: %+v, %v", run, err)
	}
	qAcceptCriterionReview(t, f, repo, run.OperationRef)
	if artifact != "" {
		j.bindVerification(t, j.observeRun(t, "q-delivery"))
		if err := j.action(t, "record_delivery", map[string]any{"delivery_artifact": artifact, "delivery_state": "asserted"}, f.owner); err != nil {
			t.Fatalf("record candidate delivery: %v", err)
		}
	}
	actor, err := WorkflowActorRef(f.owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := advanceWorkflowTestInstanceToStep(t.Context(), f.store, workID, "acceptance", actor); err != nil {
		t.Fatal(err)
	}
	if associate {
		dependencies, err := workflowReviewDependencies(t.Context(), f.store.db, workID)
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, criterion := range dependencies.Criteria {
			ids = append(ids, criterion.PredicateID)
		}
		if err := runVerdictActionAs(t, f.store, workID, "record_verdict", criterionAssociationPayload(run.OperationRef, ids...), 0, f.owner); err != nil {
			t.Fatalf("associate captured current proof: %v", err)
		}
	}
	seedPremiseDeliverablesInvestigation(t, f.store, workID)
	return acceptanceRecoveryFixture{store: f.store, workID: workID, owner: f.owner, operator: f.operator, repo: repo, receipt: run.OperationRef}
}

func qAcceptCriterionReview(t *testing.T, f workContextFixture, repo, receipt string) {
	t.Helper()
	j := &candidateJourney{fixture: f, s: f.store, workID: f.workID, repo: repo}
	job := reviewSubjectReadyRevision(t, f)
	attempt := fmt.Sprintf("q:review:%d", readWorkVersion(t, f.store, f.workID))
	if err := descendantDispatch(t, j, "review", attempt, job, nil); err != nil {
		t.Fatal(err)
	}
	j.laneDispatch(t, attempt, "review", &job)
	window := reviewSubjectWindow(t, f.store, f.workID, attempt)
	report := currentReviewBlockForTest(t, f.store, f.workID, attempt, "ready_for_final_proof")
	for _, criterion := range report.Dependencies.Criteria {
		report.Assessments = append(report.Assessments, WorkerReviewAssessment{PredicateID: criterion.PredicateID, Assessment: "supported", ReceiptRefs: []string{receipt}})
	}
	lane := reviewGateLane(t, "review")
	evidence := []WorkerReportEvidence{}
	for _, obligation := range lane.EvidenceObligations {
		if obligation != "severity" {
			evidence = append(evidence, WorkerReportEvidence{Obligation: obligation, Detail: "Reviewed the candidate and its captured command receipt."})
		}
	}
	event := Event{EventID: attempt + ":completed", Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: f.workID, Actor: "worker:test", OccurredAt: time.Now().UTC(), PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerCompleted), Payload: mustJSONValue(WorkerCompletedPayload{
		AttemptID: attempt, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion, WorkerDirectory: repo,
		Evidence: evidence, EvidenceOrigin: WorkerEvidenceReported, WorkerJob: &job, Review: report,
	})}
	prepared, err := PrepareWorkerTerminalDelivery(t.Context(), f.store, PrepareWorkerTerminalDeliveryRequest{WorkID: f.workID, AttemptID: attempt, EventID: event.EventID})
	if err != nil {
		t.Fatal(err)
	}
	workerTerminalEventThroughSeamCtx(t, WithWorkerTerminalPreparation(context.Background(), prepared), f.store, event)
	if err := j.action(t, "accept_worker_result", map[string]any{"attempt_id": attempt, "attempt_epoch": window.AttemptEpoch}, f.owner); err != nil {
		t.Fatalf("accept criterion Review: %v", err)
	}
}
