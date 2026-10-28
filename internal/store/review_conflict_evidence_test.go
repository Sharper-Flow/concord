package store

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// This is a carrier and ordering witness, not proof of semantic rule application.
// No irreversible action, conflict settlement, approval, or predicate verdict is
// manufactured: the review reports claims through the existing context notebook.
func TestReviewCompletionRetainsAppliedRuleContextClaims(t *testing.T) {
	f := seedWorkContextFixture(t, "review-rule-context-claims")
	defer f.store.Close()
	ctx := t.Context()
	repo, head := reviewSubjectClaim(t, f.store, f.workID)
	job := reviewSubjectReadyRevision(t, f)
	const subject = "internal/store/review_conflict_evidence_test.go"
	declaration := WorkerContextFinding{
		Kind: "direction", Statement: "Record the risk and recovery explanation before execution; this synthetic scenario performs no data-loss action.",
		SubjectRef: subject, EvidenceRefs: []string{subject}, DomainID: workContextTestChild,
	}
	if err := f.recordWorkContext(t, map[string]any{
		"required_reading": []WorkContextReading{}, "finding_refs": []string{},
		"context_findings": []WorkerContextFinding{declaration},
	}); err != nil {
		t.Fatalf("pre-action context declaration: %v", err)
	}
	before, err := ReadWorkPin(ctx, f.store, f.workID)
	if err != nil {
		t.Fatal(err)
	}
	if before.WorkContext == nil || len(before.WorkContext.Findings) != 1 {
		t.Fatalf("pre-action context = %+v, want one declaration finding", before.WorkContext)
	}
	anchor := before.WorkContext.Findings[0]
	if anchor.Origin != WorkContextOriginDeclaration || anchor.SourceEventSeq <= 0 || anchor.SourceEventID == "" {
		t.Fatalf("declaration lost retained source identity: %+v", anchor)
	}

	// Use the same live dispatch and terminal observation owners as the current
	// Review fixture, without changing its shared completion builder.
	const attempt = "review:applied-rule-context"
	j := &candidateJourney{fixture: f, s: f.store, workID: f.workID, repo: repo}
	if err := descendantDispatch(t, j, "review", attempt, job, nil); err != nil {
		t.Fatalf("dispatch review: %v", err)
	}
	j.laneDispatch(t, attempt, "review", &job)
	readEvent := func(id string) Event {
		t.Helper()
		var event Event
		if err := f.store.Transact(ctx, func(tx *Transaction) error {
			var found bool
			var err error
			event, found, err = EventByIDTx(ctx, tx, id)
			if err == nil && !found {
				return fmt.Errorf("worker evidence %q is absent", id)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return event
	}
	dispatch := readEvent("candidate-dispatched-" + attempt)
	var dispatched WorkerDispatchedPayload
	if err := json.Unmarshal(dispatch.Payload, &dispatched); err != nil {
		t.Fatal(err)
	}
	producer, err := WorkflowActorRef(WorkflowActor{
		PrincipalRef: f.owner.PrincipalRef, ClientRef: f.owner.ClientRef,
		AgentRef: "agent/lane:review", SessionRef: "session/" + attempt, ActorClass: ActorAgent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dispatched.AttemptID != attempt || dispatched.LaneID != "review" || dispatched.CapabilityClass != "review" || dispatched.LaneActorRef != producer {
		t.Fatalf("dispatch lost review producer attribution: %+v", dispatched)
	}
	claims := []WorkerContextFinding{}
	for _, rule := range []string{"S", "P10", "A2-9", "P8", "P1", "P2", "P3", "P7", "P9", "P11"} {
		claims = append(claims, WorkerContextFinding{
			Kind: "observation", Statement: "conflict-rule:" + rule + ": synthetic carrier witness only; compliance remains unproved without the recorded size and rule-specific source evidence.",
			SubjectRef: subject, EvidenceRefs: []string{dispatch.EventID, anchor.FindingID}, DomainID: workContextTestChild,
		})
	}
	lane := reviewGateLane(t, "review")
	review := currentReviewBlockForTest(t, f.store, f.workID, attempt, "ready_for_final_proof")
	evidence := reportedLaneEvidenceForTest(lane, review)
	for i := range evidence {
		if evidence[i].Obligation == "contract_findings" {
			evidence[i].Detail = fmt.Sprintf("Applied-rule conclusions are in context_findings[0..%d].", len(claims)-1)
		}
	}
	completion := Event{
		EventID: attempt + ":completed", Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: f.workID,
		Actor: producer, OccurredAt: time.Now().UTC(), PayloadVersion: CurrentEventPayloadVersion(WorkerCompleted),
		Payload: mustJSONValue(WorkerCompletedPayload{
			AttemptID: attempt, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion,
			WorkerDirectory: repo, WorkerJob: &job, EvidenceOrigin: WorkerEvidenceReported, Evidence: evidence,
			Review: review, ContextFindings: claims,
		}),
	}
	prepared, err := PrepareWorkerTerminalDelivery(ctx, f.store, PrepareWorkerTerminalDeliveryRequest{
		WorkID: f.workID, AttemptID: attempt, EventID: completion.EventID,
	})
	if err != nil {
		t.Fatal(err)
	}
	workerTerminalEventThroughSeamCtx(t, WithWorkerTerminalPreparation(ctx, prepared), f.store, completion)
	retained := readEvent(completion.EventID)
	var report WorkerCompletedPayload
	if err := json.Unmarshal(retained.Payload, &report); err != nil {
		t.Fatal(err)
	}
	if retained.Kind != WorkerCompleted || retained.SubjectID != f.workID || retained.Actor != producer || report.AttemptID != attempt || !reflect.DeepEqual(report.WorkerJob, &job) {
		t.Fatalf("retained completion lost producer/work/job attribution: event=%+v report=%+v", retained, report)
	}
	if !(anchor.SourceEventSeq < dispatch.Seq && dispatch.Seq < retained.Seq) {
		t.Fatalf("retained order declaration=%d dispatch=%d completion=%d", anchor.SourceEventSeq, dispatch.Seq, retained.Seq)
	}
	if report.ConsumedContextFrontier == nil || *report.ConsumedContextFrontier != before.WorkContext.SourceEventFrontier {
		t.Fatalf("consumed context frontier = %v, want pre-action frontier %d", report.ConsumedContextFrontier, before.WorkContext.SourceEventFrontier)
	}
	if !reflect.DeepEqual(report.ContextFindings, claims) || !reflect.DeepEqual(report.Review, review) || !reflect.DeepEqual(report.Evidence, evidence) {
		t.Fatalf("retained report changed independent context, review, or obligation evidence: %+v", report)
	}
	for _, obligation := range lane.EvidenceObligations {
		if obligation == "severity" && report.Review != nil {
			continue // The typed review block discharges severity, even with no ranked findings.
		}
		covered := false
		for _, entry := range report.Evidence {
			covered = covered || entry.Obligation == obligation
		}
		if !covered {
			t.Fatalf("context claims replaced required lane obligation %q", obligation)
		}
	}
	if report.ObservedDelivery == nil {
		t.Fatal("live review completion omitted its host-owned delivery observation")
	}

	pin, err := ReadWorkPin(ctx, f.store, f.workID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := ReadWorkflowContinuity(ctx, f.store, ContinuityRequest{Work: f.workID})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.WorkPin == nil {
		t.Fatal("continuity omitted its work pin")
	}
	want := []WorkContextFindingView{anchor}
	for ordinal, claim := range claims {
		want = append(want, WorkContextFindingView{
			FindingID: fmt.Sprintf("finding:%d:%d", retained.Seq, ordinal), Kind: claim.Kind,
			Statement: claim.Statement, SubjectRef: claim.SubjectRef, EvidenceRefs: claim.EvidenceRefs,
			DomainID: claim.DomainID, Origin: WorkContextOriginWorkerReport, Status: WorkContextFindingStatusReported,
			SourceEventID: retained.EventID, SourceEventSeq: retained.Seq, Ordinal: ordinal,
		})
	}
	for _, surface := range []struct {
		name string
		pin  *WorkPin
		view *WorkContextView
	}{{"work pin", &pin, pin.WorkContext}, {"continuity", snapshot.WorkPin, snapshot.WorkContext}, {"continuity work pin", snapshot.WorkPin, snapshot.WorkPin.WorkContext}} {
		if surface.view == nil || surface.view.SubjectCommit != head || surface.view.SourceEventFrontier != retained.Seq || !reflect.DeepEqual(surface.view.Findings, want) {
			t.Fatalf("%s lost exact claims, evidence order, or source attribution: %+v", surface.name, surface.view)
		}
		if surface.pin.Attempt == nil || surface.pin.Attempt.ID != attempt || surface.pin.Attempt.Lane != "review" || surface.pin.Attempt.State != "completed" {
			t.Fatalf("%s does not expose the completed review attempt: %+v", surface.name, surface.pin.Attempt)
		}
		if surface.pin.Review == nil || surface.pin.Review.Accepted != nil || !reflect.DeepEqual(surface.pin.Review.Findings, review.Findings) {
			t.Fatalf("%s promoted context claims into accepted Review or typed review.findings: %+v", surface.name, surface.pin.Review)
		}
		if surface.pin.Step != before.Step || len(surface.pin.VerifiedCriteria) != 0 || !reflect.DeepEqual(surface.pin.OutstandingEvidenceKinds, before.OutstandingEvidenceKinds) || !reflect.DeepEqual(surface.pin.PendingOperatorDecision, before.PendingOperatorDecision) {
			t.Fatalf("%s promoted report claims into workflow progress, proof, or approval: %+v", surface.name, surface.pin)
		}
	}
}
