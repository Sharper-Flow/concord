package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestArchitectureCapacityTerminalCustody(t *testing.T) {
	ctx := context.Background()

	t.Run("two valid terminal reports retain byte-equal custody and refuse the next dispatch", func(t *testing.T) {
		// The findings-capable break-fix head predates the failure-attribution
		// terminal requirement, so a plain failed report is admissible there
		// and a completed report carries its full discharged evidence.
		breakFix, ok := BuiltinWorkflowRegistry().Lookup("workflow.break_fix", 27)
		if !ok {
			t.Fatal("workflow.break_fix v27 is not registered")
		}
		const workID = "capc-two-reports"
		fixture := seedWorkContextFixtureWithDefinition(t, workID, breakFix)
		s := fixture.store
		plan := projectionArchitecture()
		version := projectionWorkVersion(t, s, workID)
		if err := applyProjectionEvents(t, s, workID, version, projectionDesignEvent(t, workID, "capc-two-reports-design", version, "witness", &plan)); err != nil {
			t.Fatalf("witness design refused: %v", err)
		}
		lane := BuiltinLaneDefinitions()[0]
		model := preferredModelForLane(lane)
		bigFinding := func(letter string) WorkerContextFinding {
			finding := contextFindingFixture()
			finding.Statement = strings.Repeat(letter, 900)
			return finding
		}
		findingsA := []WorkerContextFinding{bigFinding("f"), bigFinding("g")}
		findingsB := []WorkerContextFinding{bigFinding("h"), bigFinding("i")}

		if err := ApplyOperation(ctx, s, Operation{Events: []Event{workerDispatchEvent(workID, "capc-two-reports-attempt-a", lane, nil)}}); err != nil {
			t.Fatalf("the first attempt dispatch refused: %v", err)
		}
		failedReport := workerFailedContextFindingsEvent(workID, "capc-two-reports-failed", "capc-two-reports-attempt-a", model, WorkerFailureWorkerError, findingsA, WorkerEvidenceEventPayloadVersion(WorkerFailed))
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{failedReport}}); err != nil {
			t.Fatalf("the first, individually-fitting terminal report refused: %v", err)
		}
		custodyAssertTerminalCustody(t, s, workID, failedReport, "capc-two-reports-attempt-a", "failed")
		custodyAssertContextFits(t, ctx, s, workID, 2)

		if err := ApplyOperation(ctx, s, Operation{Events: []Event{workerDispatchEvent(workID, "capc-two-reports-attempt-b", lane, nil)}}); err != nil {
			t.Fatalf("the second attempt dispatch refused: %v", err)
		}
		completedReport := workerCompletedContextFindingsEvent(workID, "capc-two-reports-completed", "capc-two-reports-attempt-b", model, lane, findingsB, WorkerEvidenceEventPayloadVersion(WorkerCompleted))
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{completedReport}}); err != nil {
			t.Fatalf("the second valid terminal report refused whole instead of committing retained custody: %v", err)
		}
		custodyAssertTerminalCustody(t, s, workID, completedReport, "capc-two-reports-attempt-b", "completed")
		// The strict view still reads: the overflow is the slot charge, not
		// the 32-finding view bound.
		custodyAssertViewHolds(t, ctx, s, workID, 4)

		version, events, rows := capacityWritesSnapshot(t, s, workID)
		nextDispatchErr := ApplyOperation(ctx, s, Operation{Events: []Event{workerDispatchEvent(workID, "capc-two-reports-attempt-c", lane, nil)}})
		_ = capacityWritesRefusal(t, nextDispatchErr, "work_context")
		capacityWritesZeroEffects(t, s, workID, version, events, rows)

		failedPayload := custodyStoredPayload(t, s, "capc-two-reports-failed")
		completedPayload := custodyStoredPayload(t, s, "capc-two-reports-completed")
		if err := RebuildFromLog(ctx, s); err != nil {
			t.Fatalf("rebuild of the retained custody history refused: %v", err)
		}
		capacityWritesZeroEffects(t, s, workID, version, events, rows)
		if custodyStoredPayload(t, s, "capc-two-reports-failed") != failedPayload || custodyStoredPayload(t, s, "capc-two-reports-completed") != completedPayload {
			t.Fatal("rebuild rewrote the retained terminal payload bytes")
		}
		if got := custodyAttemptState(t, s, workID, "capc-two-reports-attempt-a"); got != "failed" {
			t.Fatalf("attempt a lifecycle_state after rebuild = %q, want failed", got)
		}
		if got := custodyAttemptState(t, s, workID, "capc-two-reports-attempt-b"); got != "completed" {
			t.Fatalf("attempt b lifecycle_state after rebuild = %q, want completed", got)
		}
		version, events, rows = capacityWritesSnapshot(t, s, workID)
		afterRebuildErr := ApplyOperation(ctx, s, Operation{Events: []Event{workerDispatchEvent(workID, "capc-two-reports-attempt-d", lane, nil)}})
		_ = capacityWritesRefusal(t, afterRebuildErr, "work_context")
		capacityWritesZeroEffects(t, s, workID, version, events, rows)
	})

	t.Run("independent 32-to-33 tiny-finding growth persists the 33rd terminal and refuses strict read and dispatch", func(t *testing.T) {
		breakFix, ok := BuiltinWorkflowRegistry().Lookup("workflow.break_fix", 27)
		if !ok {
			t.Fatal("workflow.break_fix v27 is not registered")
		}
		const workID = "capc-tiny-growth"
		fixture := seedWorkContextFixtureWithDefinition(t, workID, breakFix)
		s := fixture.store
		// The rebalanced allocation keeps the unchanged 31,744-byte total and
		// enlarges work_context to 12,288 bytes, so the 32-finding tiny view
		// fits its slot and only the 32-finding strict view count can refuse
		// the 33rd.
		plan := custodyRebalancedArchitecture()
		version := projectionWorkVersion(t, s, workID)
		if err := applyProjectionEvents(t, s, workID, version, projectionDesignEvent(t, workID, "capc-tiny-growth-design", version, "witness", &plan)); err != nil {
			t.Fatalf("the rebalanced witness design refused: %v", err)
		}
		lane := BuiltinLaneDefinitions()[0]
		model := preferredModelForLane(lane)
		sixteen := make([]WorkerContextFinding, 16)
		for i := range sixteen {
			sixteen[i] = custodyTinyFinding()
		}

		if err := ApplyOperation(ctx, s, Operation{Events: []Event{workerDispatchEvent(workID, "capc-tiny-attempt-1", lane, nil)}}); err != nil {
			t.Fatalf("the first tiny-finding dispatch refused: %v", err)
		}
		completed16 := workerCompletedContextFindingsEvent(workID, "capc-tiny-completed-16", "capc-tiny-attempt-1", model, lane, sixteen, WorkerEvidenceEventPayloadVersion(WorkerCompleted))
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{completed16}}); err != nil {
			t.Fatalf("the first 16-finding terminal report refused: %v", err)
		}
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{workerDispatchEvent(workID, "capc-tiny-attempt-2", lane, nil)}}); err != nil {
			t.Fatalf("the second tiny-finding dispatch refused: %v", err)
		}
		failed16 := workerFailedContextFindingsEvent(workID, "capc-tiny-failed-16", "capc-tiny-attempt-2", model, WorkerFailureWorkerError, sixteen, WorkerEvidenceEventPayloadVersion(WorkerFailed))
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{failed16}}); err != nil {
			t.Fatalf("the second 16-finding terminal report refused: %v", err)
		}
		custodyAssertContextFits(t, ctx, s, workID, 32)
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{workerDispatchEvent(workID, "capc-tiny-attempt-3", lane, nil)}}); err != nil {
			t.Fatalf("the third tiny-finding dispatch refused: %v", err)
		}
		thirdReport := workerCompletedContextFindingsEvent(workID, "capc-tiny-completed-33", "capc-tiny-attempt-3", model, lane, []WorkerContextFinding{custodyTinyFinding()}, WorkerEvidenceEventPayloadVersion(WorkerCompleted))
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{thirdReport}}); err != nil {
			t.Fatalf("the 33rd valid terminal report refused whole instead of committing retained custody: %v", err)
		}
		custodyAssertTerminalCustody(t, s, workID, thirdReport, "capc-tiny-attempt-3", "completed")

		_, readErr := readWorkContextView(ctx, s.db, workID)
		custodyAssertStrictRefusal(t, readErr)
		version, events, rows := capacityWritesSnapshot(t, s, workID)
		nextDispatchErr := ApplyOperation(ctx, s, Operation{Events: []Event{workerDispatchEvent(workID, "capc-tiny-attempt-4", lane, nil)}})
		custodyAssertStrictRefusal(t, nextDispatchErr)
		capacityWritesZeroEffects(t, s, workID, version, events, rows)

		path := s.Path()
		completed16Payload := custodyStoredPayload(t, s, "capc-tiny-completed-16")
		failed16Payload := custodyStoredPayload(t, s, "capc-tiny-failed-16")
		thirdPayload := custodyStoredPayload(t, s, "capc-tiny-completed-33")
		if err := s.Close(); err != nil {
			t.Fatalf("close before reopen refused: %v", err)
		}
		reopened, err := Open(ctx, path)
		if err != nil {
			t.Fatalf("reopen refused: %v", err)
		}
		t.Cleanup(func() {
			if err := reopened.Close(); err != nil {
				t.Errorf("close of the reopened store refused: %v", err)
			}
		})
		s = reopened
		if custodyStoredPayload(t, s, "capc-tiny-completed-16") != completed16Payload || custodyStoredPayload(t, s, "capc-tiny-failed-16") != failed16Payload || custodyStoredPayload(t, s, "capc-tiny-completed-33") != thirdPayload {
			t.Fatal("reopen rewrote the retained terminal payload bytes")
		}
		for _, want := range []struct{ attempt, state string }{
			{"capc-tiny-attempt-1", "completed"}, {"capc-tiny-attempt-2", "failed"}, {"capc-tiny-attempt-3", "completed"},
		} {
			if got := custodyAttemptState(t, s, workID, want.attempt); got != want.state {
				t.Fatalf("attempt %s lifecycle_state after reopen = %q, want %q", want.attempt, got, want.state)
			}
		}
		if got := projectionWorkVersion(t, s, workID); got != version {
			t.Fatalf("reopen moved the work version to %d, want %d", got, version)
		}
		_, readErr = readWorkContextView(ctx, s.db, workID)
		custodyAssertStrictRefusal(t, readErr)
		version, events, rows = capacityWritesSnapshot(t, s, workID)
		afterReopenErr := ApplyOperation(ctx, s, Operation{Events: []Event{workerDispatchEvent(workID, "capc-tiny-attempt-5", lane, nil)}})
		custodyAssertStrictRefusal(t, afterReopenErr)
		capacityWritesZeroEffects(t, s, workID, version, events, rows)
	})
}

// custodyAssertTerminalCustody demands the retained terminal evidence: the
// stored event payload bytes equal the submitted event's payload verbatim and
// the attempt holds the wanted terminal lifecycle state.
func custodyAssertTerminalCustody(t *testing.T, s *Store, workID string, report Event, attemptID, wantState string) {
	t.Helper()
	if stored := custodyStoredPayload(t, s, report.EventID); stored != string(report.Payload) {
		t.Fatalf("the retained terminal payload for %s was rewritten:\n got %s\nwant %s", report.EventID, stored, report.Payload)
	}
	if got := custodyAttemptState(t, s, workID, attemptID); got != wantState {
		t.Fatalf("attempt %s lifecycle_state = %q, want %q", attemptID, got, wantState)
	}
}

// custodyAssertContextFits proves the fit precondition: the strict view reads
// with exactly the wanted finding count and the work_context slot charge,
// measured the way the capacity owner measures it, fits its reserve.
func custodyAssertContextFits(t *testing.T, ctx context.Context, s *Store, workID string, wantFindings int) {
	t.Helper()
	custodyAssertViewHolds(t, ctx, s, workID, wantFindings)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	profile, found, err := architectureCapacityProfileForWorkTx(ctx, tx, workID)
	if err != nil || !found {
		t.Fatalf("the witness allocation is missing: found=%v err=%v", found, err)
	}
	members, err := architectureCapacityCurrentMembersTx(ctx, tx, workID, architectureCapacityAllSlots())
	if err != nil {
		t.Fatalf("the capacity member projection refused: %v", err)
	}
	raw, ok := members["work_context"]
	if !ok {
		t.Fatal("the capacity projection carries no work_context member")
	}
	if span, reserve := architectureMemberSpan("work_context", raw, true), profile.Reserve(architectureSlotWorkContext); span > reserve {
		t.Fatalf("the work_context slot charges %d canonical bytes against the %d reserve; the fit precondition is broken", span, reserve)
	}
}

// custodyAssertViewHolds proves only the strict view part of the fit: the
// read succeeds with exactly the wanted finding count.
func custodyAssertViewHolds(t *testing.T, ctx context.Context, s *Store, workID string, wantFindings int) {
	t.Helper()
	view, err := readWorkContextView(ctx, s.db, workID)
	if err != nil {
		t.Fatalf("the strict context read refused at %d findings: %v", wantFindings, err)
	}
	if view == nil {
		t.Fatalf("the strict context read projected no view at %d findings", wantFindings)
	}
	if got := len(view.Findings); got != wantFindings {
		t.Fatalf("the strict view holds %d findings, want %d", got, wantFindings)
	}
}

// custodyAssertStrictRefusal asserts the strict 32-finding view-bound refusal
// the retained overflow must keep producing for reads and dispatches.
func custodyAssertStrictRefusal(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("the strict read accepted the over-bound view")
	}
	if !hasFailureKind(err, KindLimitExceeded) {
		t.Fatalf("strict refusal is not %s: %v", KindLimitExceeded, err)
	}
	if !strings.Contains(err.Error(), "the view bound is 32") {
		t.Fatalf("strict refusal does not name the 32-finding view bound: %v", err)
	}
}

// custodyStoredPayload reads one stored event's payload bytes.
func custodyStoredPayload(t *testing.T, s *Store, eventID string) string {
	t.Helper()
	var payload string
	if err := s.db.QueryRow(`SELECT payload FROM domain_events WHERE event_id=?`, eventID).Scan(&payload); err != nil {
		t.Fatalf("the terminal event %s is not durable: %v", eventID, err)
	}
	return payload
}

// custodyAttemptState reads one attempt's lifecycle state.
func custodyAttemptState(t *testing.T, s *Store, workID, attemptID string) string {
	t.Helper()
	var state string
	if err := s.db.QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, attemptID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

// custodyTinyFinding is one minimal admissible worker finding: every field
// passes the closed shape validation at its smallest size.
func custodyTinyFinding() WorkerContextFinding {
	return WorkerContextFinding{Kind: "observation", Statement: "t", SubjectRef: "r", EvidenceRefs: []string{}, DomainID: workContextTestChild}
}

// custodyRebalancedArchitecture moves 8,192 bytes of worker_job reserve into
// work_context inside the unchanged 31,744-byte allocation total.
func custodyRebalancedArchitecture() WorkflowDesignArchitecture {
	plan := projectionArchitecture()
	plan.Capacity.WorkerJob = 2048
	plan.Capacity.WorkContext = 12288
	return plan
}

// architectureCapacityTestNonWorkSubjects carries the registered event kinds
// whose fold validates a non-work subject; the capacity producer policy
// governs work subjects alone, so the exhaustiveness walk excludes exactly
// these.
var architectureCapacityTestNonWorkSubjects = map[string]bool{
	"product.created": true, "product.renamed": true, "product.stage_changed": true,
	"product.knowledge_home_designated": true, "product.knowledge_home_cleared": true,
	"product.knowledge_source_registered": true, "product.knowledge_source_removed": true,
	"product.planning_mode_set": true,
	"project.created":           true, "project.renamed": true, "project.stage_changed": true,
	"project.locator_added": true, "project.locator_updated": true, "project.locator_removed": true,
	"project.governing_requirement_declared": true, "project.governing_requirement_withdrawn": true,
	"product_project.added": true, "product_project.removed": true, "product_project.role_changed": true,
	"managed_resource.created": true, "managed_resource.consumer_added": true, "managed_resource.updated": true,
	"domain.project_attachments_replaced": true, "domain.resource_attachments_replaced": true,
	"domain.observation_recorded": true, "domain.observation_dismissed": true,
	EventSessionOrchestratorIdentityAsserted: true,
}

// custodyProducerPolicyEvent builds one work-subject policy event; the pure
// classification reads the kind and, for action_completed, the payload root.
func custodyProducerPolicyEvent(kind string, payload string) Event {
	return Event{Kind: kind, SubjectType: SubjectWorkItem, Payload: []byte(payload)}
}

// custodyActionCompletedEvent builds one validated action_completed completion
// whose root carries the action identity the fold dispatches on.
func custodyActionCompletedEvent(actionID string) Event {
	return custodyProducerPolicyEvent(WorkflowActionCompleted, fmt.Sprintf(`{"action_id":%q,"step_id":"implement","attempt_epoch":1}`, actionID))
}

// custodySlotKnown reports whether the slot is one of the closed declared
// eight.
func custodySlotKnown(slot architectureCapacitySlot) bool {
	for _, ordered := range architectureCapacitySlotsInOrder {
		if slot == ordered {
			return true
		}
	}
	return false
}

// custodyAssertSlots asserts one classification result: exactly the wanted
// distinct slots, each inside the closed inventory.
func custodyAssertSlots(t *testing.T, name string, got []architectureCapacitySlot, want ...architectureCapacitySlot) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s selected %v, want %v", name, got, want)
	}
	seen := make(map[architectureCapacitySlot]bool, len(want))
	for _, slot := range got {
		if !custodySlotKnown(slot) {
			t.Fatalf("%s selected %q outside the closed slot inventory", name, slot)
		}
		if seen[slot] {
			t.Fatalf("%s selected %q twice", name, slot)
		}
		seen[slot] = true
	}
	for _, slot := range want {
		if !seen[slot] {
			t.Fatalf("%s selected %v, want %v", name, got, want)
		}
	}
}

// TestArchitectureCapacityProducerPolicy proves the closed producer/slot
// policy: every registered work-subject kind classifies explicitly, the
// material producers select exactly their written slots, the custody and
// bookkeeping producers select none, and the refusals fail closed.
func TestArchitectureCapacityProducerPolicy(t *testing.T) {
	t.Run("every registered work-subject kind carries an explicit producer policy", func(t *testing.T) {
		for kind := range eventKindRegistry {
			if architectureCapacityTestNonWorkSubjects[kind] {
				continue
			}
			slots, err := architectureCapacitySlotsForEvent(custodyProducerPolicyEvent(kind, `{}`))
			if err != nil {
				t.Fatalf("registered work-subject kind %s has no explicit producer policy: %v", kind, err)
			}
			for _, slot := range slots {
				if !custodySlotKnown(slot) {
					t.Fatalf("kind %s selected %q outside the closed slot inventory", kind, slot)
				}
			}
		}
		for kind := range architectureCapacityTestNonWorkSubjects {
			if _, ok := eventKindRegistry[kind]; !ok {
				t.Fatalf("the exclusion list names %s, which the event registry does not register", kind)
			}
		}
	})

	t.Run("all-slot producers are strict", func(t *testing.T) {
		for _, event := range []Event{
			custodyProducerPolicyEvent(WorkflowDesignRecorded, `{}`),
			custodyProducerPolicyEvent(WorkflowContractApproved, `{}`),
			custodyProducerPolicyEvent(WorkflowContractSuperseded, `{}`),
			custodyProducerPolicyEvent(WorkerDispatched, `{}`),
			custodyActionCompletedEvent("dispatch_worker"),
		} {
			slots, err := architectureCapacitySlotsForEvent(event)
			if err != nil {
				t.Fatalf("%s refused: %v", event.Kind, err)
			}
			custodyAssertSlots(t, event.Kind, slots, architectureCapacitySlotsInOrder...)
		}
	})

	t.Run("material producers select their written slots", func(t *testing.T) {
		cases := []struct {
			name  string
			event Event
			want  []architectureCapacitySlot
		}{
			{"work created", custodyProducerPolicyEvent("work.created", `{}`), []architectureCapacitySlot{architectureSlotWorkRecord, architectureSlotTaskPremise}},
			{"intent revised", custodyProducerPolicyEvent("work.intent_revised", `{}`), []architectureCapacitySlot{architectureSlotWorkRecord, architectureSlotTaskPremise}},
			{"proposal recorded", custodyProducerPolicyEvent(WorkflowProposalRecorded, `{}`), []architectureCapacitySlot{architectureSlotProposalRecord}},
			{"worker job recorded", custodyProducerPolicyEvent(WorkerJobRecorded, `{}`), []architectureCapacitySlot{architectureSlotWorkerJob}},
			{"work context recorded", custodyProducerPolicyEvent(WorkflowWorkContextRecorded, `{}`), []architectureCapacitySlot{architectureSlotWorkContext}},
			{"context checkpointed", custodyProducerPolicyEvent(WorkflowContextCheckpointed, `{}`), []architectureCapacitySlot{architectureSlotWorkContext}},
			{"correction requested", custodyActionCompletedEvent("request_correction"), []architectureCapacitySlot{architectureSlotWorkContext}},
			{"worker result rejected", custodyActionCompletedEvent("reject_worker_result"), []architectureCapacitySlot{architectureSlotWorkContext}},
		}
		for _, tc := range cases {
			slots, err := architectureCapacitySlotsForEvent(tc.event)
			if err != nil {
				t.Fatalf("%s refused: %v", tc.name, err)
			}
			custodyAssertSlots(t, tc.name, slots, tc.want...)
		}
	})

	t.Run("custody and bookkeeping producers select no slot", func(t *testing.T) {
		withFindings := `{"attempt_id":"attempt:capc","attempt_epoch":1,"context_findings":[{"kind":"observation","statement":"the terminal finding rides custody"}]}`
		for _, tc := range []struct {
			name  string
			event Event
		}{
			{"completed terminal carrying context findings", custodyProducerPolicyEvent(WorkerCompleted, withFindings)},
			{"failed terminal carrying context findings", custodyProducerPolicyEvent(WorkerFailed, withFindings)},
			{"derived record_worker_failure", custodyActionCompletedEvent("record_worker_failure")},
			{"accept_worker_result", custodyActionCompletedEvent("accept_worker_result")},
			{"accept_worker_evidence", custodyActionCompletedEvent("accept_worker_evidence")},
			{"record_verdict", custodyActionCompletedEvent("record_verdict")},
			{"generic unnamed completion", custodyProducerPolicyEvent(WorkflowActionCompleted, `{}`)},
			{"action checkpointed", custodyProducerPolicyEvent(WorkflowActionCheckpointed, `{}`)},
			{"premise confirmed", custodyProducerPolicyEvent(WorkflowPremiseConfirmed, `{}`)},
			{"delivery corrected", custodyProducerPolicyEvent(WorkflowDeliveryCorrected, `{}`)},
			{"native run recorded", custodyProducerPolicyEvent(WorkflowNativeRunRecorded, `{}`)},
			{"context boundary crossed", custodyProducerPolicyEvent(WorkflowContextBoundaryCrossed, `{}`)},
			{"workflow completed", custodyProducerPolicyEvent(WorkflowCompleted, `{}`)},
			{"work transitioned", custodyProducerPolicyEvent("work.transitioned", `{}`)},
			{"retired initiative narrative", custodyProducerPolicyEvent("initiative.narrative_revised", `{}`)},
			{"compaction link published", custodyProducerPolicyEvent("compaction_link.published", `{}`)},
		} {
			slots, err := architectureCapacitySlotsForEvent(tc.event)
			if err != nil {
				t.Fatalf("%s refused: %v", tc.name, err)
			}
			if len(slots) != 0 {
				t.Fatalf("%s selected %v, want no slot", tc.name, slots)
			}
		}
	})

	t.Run("malformed completion identity refuses instead of classifying as bookkeeping", func(t *testing.T) {
		for _, payload := range []string{`not json`, `[]`, `null`, `{"action_id":7}`} {
			_, err := architectureCapacitySlotsForEvent(custodyProducerPolicyEvent(WorkflowActionCompleted, payload))
			if err == nil {
				t.Fatalf("the malformed completion payload %s classified without refusing", payload)
			}
			if !hasFailureKind(err, KindInvariantViolation) {
				t.Fatalf("the malformed completion payload %s refused as %v, want %s", payload, err, KindInvariantViolation)
			}
		}
	})

	t.Run("unknown event kind refuses as missing producer policy", func(t *testing.T) {
		_, err := architectureCapacitySlotsForEvent(custodyProducerPolicyEvent("future.workflow_producer", `{}`))
		if err == nil {
			t.Fatal("the unknown event kind classified without refusing")
		}
		if !hasFailureKind(err, KindInvariantViolation) {
			t.Fatalf("the unknown event kind refused as %v, want %s", err, KindInvariantViolation)
		}
		if !strings.Contains(err.Error(), "producer policy") {
			t.Fatalf("the refusal does not name the missing producer policy: %v", err)
		}
	})
}

// custodyOverflowedContextFixture seeds one witness-designed break-fix work
// and overflows its work_context slot through two valid terminal reports, the
// one custody route a valid event can raise the slot charge by. It proves the
// overflow precondition by refusing the next all-slot dispatch with zero
// effects, then returns the fixture for the selected-slot controls.
func custodyOverflowedContextFixture(t *testing.T, workID string) workContextFixture {
	t.Helper()
	ctx := context.Background()
	breakFix, ok := BuiltinWorkflowRegistry().Lookup("workflow.break_fix", 27)
	if !ok {
		t.Fatal("workflow.break_fix v27 is not registered")
	}
	fixture := seedWorkContextFixtureWithDefinition(t, workID, breakFix)
	s := fixture.store
	plan := projectionArchitecture()
	version := projectionWorkVersion(t, s, workID)
	if err := applyProjectionEvents(t, s, workID, version, projectionDesignEvent(t, workID, "capc-overflow-design-"+workID, version, "witness", &plan)); err != nil {
		t.Fatalf("witness design refused: %v", err)
	}
	lane := BuiltinLaneDefinitions()[0]
	model := preferredModelForLane(lane)
	bigFinding := func(letter string) WorkerContextFinding {
		finding := contextFindingFixture()
		finding.Statement = strings.Repeat(letter, 900)
		return finding
	}
	for _, attempt := range []struct{ attemptID, eventID, letter string }{
		{"capc-overflow-attempt-a-" + workID, "capc-overflow-failed-a-" + workID, "f"},
		{"capc-overflow-attempt-b-" + workID, "capc-overflow-failed-b-" + workID, "h"},
	} {
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{workerDispatchEvent(workID, attempt.attemptID, lane, nil)}}); err != nil {
			t.Fatalf("the overflow attempt dispatch refused: %v", err)
		}
		report := workerFailedContextFindingsEvent(workID, attempt.eventID, attempt.attemptID, model, WorkerFailureWorkerError, []WorkerContextFinding{bigFinding(attempt.letter), bigFinding(strings.ToUpper(attempt.letter))}, WorkerEvidenceEventPayloadVersion(WorkerFailed))
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{report}}); err != nil {
			t.Fatalf("the valid terminal report refused: %v", err)
		}
	}
	version, events, rows := capacityWritesSnapshot(t, s, workID)
	nextDispatch := ApplyOperation(ctx, s, Operation{Events: []Event{workerDispatchEvent(workID, "capc-overflow-next-"+workID, lane, nil)}})
	_ = capacityWritesRefusal(t, nextDispatch, "work_context")
	capacityWritesZeroEffects(t, s, workID, version, events, rows)
	return fixture
}

// custodyObservationEvent builds one work.observation_recorded custody event.
func custodyObservationEvent(workID, eventID, observationID, statement string) Event {
	return Event{EventID: eventID, Kind: WorkObservationRecorded, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "principal/agent", OccurredAt: time.Unix(21, 0).UTC(), PayloadVersion: 1,
		Payload: mustJSONValue(map[string]any{"observation_id": observationID, "statement": statement, "refs": []string{"internal/store"}, "tags": []string{}})}
}

// custodyBoundedJobPayload builds one bounded, not-ready worker-job revision
// the caller records through the typed fold-level route.
func custodyBoundedJobPayload(jobID, objective string, unresolved []string) WorkerJobRecordedPayload {
	job := WorkerJobRecordedPayload{
		JobID: jobID, Revision: 1, ContractVersion: 1,
		Objective: objective, StoppingCondition: "the recorded checks pass",
		PathScope: []string{"internal/store"}, PredicateIDs: []string{}, Checks: []string{"go test ./internal/store/"},
		Prerequisites: []WorkerJobPrerequisite{}, UnresolvedRefs: unresolved,
	}
	job.Digest = DeriveWorkerJobDigest(job)
	return job
}

// TestArchitectureCapacitySelectedSlotAdmission proves the selected-slot
// admission: job-only and custody writers continue over an already
// overflowing context without a context read, a job-slot overage still
// refuses whole, a bounded declaration replaces the terminal overflow and
// dispatch resumes without trimming retained events, and a custody event
// cannot launder a voluntary over-allocation in a mixed operation.
func TestArchitectureCapacitySelectedSlotAdmission(t *testing.T) {
	ctx := context.Background()

	t.Run("a bounded job-only write and observation custody continue over the overflowing context", func(t *testing.T) {
		const workID = "capc-selected-job"
		s := custodyOverflowedContextFixture(t, workID).store
		// The bounded job-only revision continues: its selection names the
		// worker_job slot alone, so the overflowing work_context is never
		// read. The typed fold-level route reaches the fold's own refusals
		// and the post-fold capacity admission, the boundary a replayed log
		// meets; the record_worker_job action gate above it is not exercised
		// here and claims no acceptance authority.
		if err := applyWorkerJobEventForTest(t, s, workID, custodyBoundedJobPayload("job:capc-selected-job", "the bounded job-only objective", []string{})); err != nil {
			t.Fatalf("the bounded job-only recording refused over the overflowing context: %v", err)
		}
		// Observation custody continues: the observation selects no slot, so
		// the overflowing context is never read for it either.
		observation := custodyObservationEvent(workID, "capc-selected-job-observation", "obs:0000000000000001", "the observation custody rides over the overflowing context")
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{observation}}); err != nil {
			t.Fatalf("observation custody refused over the overflowing context: %v", err)
		}
		// A job-slot overage still refuses whole: the over-allocated
		// revision names no revision row and commits nothing.
		unresolved := make([]string, 64)
		for i := range unresolved {
			unresolved[i] = fmt.Sprintf("%s-%02d", strings.Repeat("u", 150), i)
		}
		version, events, rows := capacityWritesSnapshot(t, s, workID)
		overErr := applyWorkerJobEventForTest(t, s, workID, custodyBoundedJobPayload("job:capc-selected-over", strings.Repeat("o", 4096), unresolved))
		_ = capacityWritesRefusal(t, overErr, "worker_job")
		capacityWritesZeroEffects(t, s, workID, version, events, rows)
		var recorded int
		if err := s.db.QueryRow(`SELECT count(*) FROM worker_job_revisions WHERE work_id=? AND job_id='job:capc-selected-over'`, workID).Scan(&recorded); err != nil {
			t.Fatal(err)
		}
		if recorded != 0 {
			t.Fatalf("the refused job left %d revision rows", recorded)
		}
	})

	t.Run("a bounded declaration replaces the terminal overflow and dispatch resumes without trimming retained events", func(t *testing.T) {
		const workID = "capc-selected-repair"
		fixture := custodyOverflowedContextFixture(t, workID)
		s := fixture.store
		failedA := custodyStoredPayload(t, s, "capc-overflow-failed-a-"+workID)
		failedB := custodyStoredPayload(t, s, "capc-overflow-failed-b-"+workID)
		// The bounded declaration re-anchors the context view: the terminal
		// findings recorded before it drop out of the derived view, so the
		// selected work_context slot fits again and the declaration commits.
		if err := fixture.recordWorkContext(t, map[string]any{
			"required_reading": []map[string]any{workContextRepoReading(workContextTestChild, "")},
			"finding_refs":     []string{},
			"context_findings": []map[string]any{workContextFinding(workContextTestChild, "observation", "the bounded declaration replaces the terminal overflow", "")},
		}); err != nil {
			t.Fatalf("the bounded context replacement refused over the overflowing context: %v", err)
		}
		// The next all-slot dispatch resumes on the replaced view.
		lane := BuiltinLaneDefinitions()[0]
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{workerDispatchEvent(workID, "capc-selected-repair-dispatch", lane, nil)}}); err != nil {
			t.Fatalf("the dispatch after the bounded replacement refused: %v", err)
		}
		// The retained terminal events are not trimmed: their stored bytes
		// are unchanged.
		if custodyStoredPayload(t, s, "capc-overflow-failed-a-"+workID) != failedA || custodyStoredPayload(t, s, "capc-overflow-failed-b-"+workID) != failedB {
			t.Fatal("the context replacement trimmed a retained terminal event")
		}
	})

	t.Run("a custody event cannot launder a voluntary over-allocation", func(t *testing.T) {
		const workID = "capc-mixed-op"
		s := capacityWritesWitnessStore(t, workID)
		// One operation carries observation custody and the over-allocated
		// intent revision: the union of written slots still measures the
		// work_record slot, the refusal names that slot, and the whole
		// operation rolls back, so the custody event commits nothing.
		observation := custodyObservationEvent(workID, "capc-mixed-op-observation", "obs:0000000000000002", "the custody event cannot launder the voluntary over-allocation")
		version, events, rows := capacityWritesSnapshot(t, s, workID)
		intent := capacityWritesIntentEvent(t, workID, version, strings.Repeat("t", 4100))
		mixedErr := ApplyOperation(ctx, s, Operation{Events: []Event{observation, intent}, ExpectedVersions: workVersion(workID, version)})
		_ = capacityWritesRefusal(t, mixedErr, "work_record")
		capacityWritesZeroEffects(t, s, workID, version, events, rows)
		var observations int
		if err := s.db.QueryRow(`SELECT count(*) FROM work_observations WHERE work_id=?`, workID).Scan(&observations); err != nil {
			t.Fatal(err)
		}
		if observations != 0 {
			t.Fatalf("the refused mixed operation left %d observation rows", observations)
		}
	})
}

// custodyAssertContextOverSlot measures the work_context slot charge the way
// the capacity owner measures it and asserts it stands above the reserve.
func custodyAssertContextOverSlot(t *testing.T, ctx context.Context, s *Store, workID string) {
	t.Helper()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	profile, found, err := architectureCapacityProfileForWorkTx(ctx, tx, workID)
	if err != nil || !found {
		t.Fatalf("the witness allocation is missing: found=%v err=%v", found, err)
	}
	members, err := architectureCapacityCurrentMembersTx(ctx, tx, workID, architectureCapacityAllSlots())
	if err != nil {
		t.Fatalf("the capacity member projection refused: %v", err)
	}
	raw, ok := members["work_context"]
	if !ok {
		t.Fatal("the capacity projection carries no work_context member")
	}
	if span, reserve := architectureMemberSpan("work_context", raw, true), profile.Reserve(architectureSlotWorkContext); span <= reserve {
		t.Fatalf("the work_context slot charges %d canonical bytes against the %d reserve; the over-slot precondition is broken", span, reserve)
	}
}

// TestArchitectureCapacityEvidenceAndAcceptanceCustody proves the real
// accept_worker_result and bind_evidence actions continue over an already
// overflowing context: both actions select no slot, so capacity admission
// never reads the four-finding view above its reserve for them, while the
// terminal payloads, their statuses, the finding view, and the over-slot
// charge stay untouched.
func TestArchitectureCapacityEvidenceAndAcceptanceCustody(t *testing.T) {
	ctx := context.Background()
	breakFix, ok := BuiltinWorkflowRegistry().Lookup("workflow.break_fix", 27)
	if !ok {
		t.Fatal("workflow.break_fix v27 is not registered")
	}
	const workID = "capc-evidence-accept"
	fixture := seedWorkContextFixtureWithDefinition(t, workID, breakFix)
	s := fixture.store
	plan := projectionArchitecture()
	version := projectionWorkVersion(t, s, workID)
	if err := applyProjectionEvents(t, s, workID, version, projectionDesignEvent(t, workID, "capc-evidence-accept-design", version, "witness", &plan)); err != nil {
		t.Fatalf("witness design refused: %v", err)
	}
	// The repair checkpoint derives the step start and fences execution from
	// its own actor; the reports and the acceptance ride its retained epoch.
	checkpointer := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/capc-repair-checkpointer", SessionRef: "session/" + workID + "-checkpointer", ActorClass: ActorAgent}
	if err := runVerdictActionAs(t, s, workID, "checkpoint_repair", json.RawMessage(`{}`), 0, checkpointer); err != nil {
		t.Fatalf("the repair checkpoint refused: %v", err)
	}
	epoch := latestStepStartEpoch(t, s, workID, "repair")
	if epoch == 0 {
		t.Fatal("the repair checkpoint opened no attempt epoch")
	}
	lane := BuiltinLaneDefinitions()[0]
	model := preferredModelForLane(lane)
	bigFinding := func(letter string) WorkerContextFinding {
		finding := contextFindingFixture()
		finding.Statement = strings.Repeat(letter, 900)
		return finding
	}
	reports := []struct {
		attemptID string
		report    Event
		state     string
	}{
		{"capc-evidence-attempt-a", workerFailedContextFindingsEvent(workID, "capc-evidence-failed", "capc-evidence-attempt-a", model, WorkerFailureWorkerError, []WorkerContextFinding{bigFinding("f"), bigFinding("g")}, WorkerEvidenceEventPayloadVersion(WorkerFailed)), "failed"},
		{"capc-evidence-attempt-b", workerCompletedContextFindingsEvent(workID, "capc-evidence-completed", "capc-evidence-attempt-b", model, lane, []WorkerContextFinding{bigFinding("h"), bigFinding("i")}, WorkerEvidenceEventPayloadVersion(WorkerCompleted)), "completed"},
	}
	for _, report := range reports {
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{workerDispatchEvent(workID, report.attemptID, lane, nil)}}); err != nil {
			t.Fatalf("the dispatch for %s refused: %v", report.attemptID, err)
		}
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{report.report}}); err != nil {
			t.Fatalf("the terminal report for %s refused: %v", report.attemptID, err)
		}
		custodyAssertTerminalCustody(t, s, workID, report.report, report.attemptID, report.state)
	}
	// The strict view still reads its four findings, and the work_context
	// slot charge stands above its reserve: the over-slot precondition both
	// actions must survive.
	custodyAssertViewHolds(t, ctx, s, workID, 4)
	custodyAssertContextOverSlot(t, ctx, s, workID)
	failedPayload := custodyStoredPayload(t, s, "capc-evidence-failed")
	completedPayload := custodyStoredPayload(t, s, "capc-evidence-completed")

	// The real accept continues: accept_worker_result selects no slot, so
	// capacity admission never reads the overflowing context for it.
	acceptor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/capc-acceptor", SessionRef: "session/" + workID + "-acceptor", ActorClass: ActorAgent}
	versionBeforeAccept := verdictItemVersion(t, s, workID)
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", json.RawMessage(`{"attempt_id":"capc-evidence-attempt-b","attempt_epoch":`+fmt.Sprint(epoch)+`}`), 0, acceptor); err != nil {
		t.Fatalf("accept_worker_result refused over the overflowing context: %v", err)
	}
	acceptorRef, err := WorkflowActorRef(acceptor)
	if err != nil {
		t.Fatal(err)
	}
	var acceptActor string
	if err := s.db.QueryRow(`SELECT json_extract(payload,'$.actor_ref') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='accept_worker_result' AND json_extract(payload,'$.worker_attempt_id')='capc-evidence-attempt-b' ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionCompleted).Scan(&acceptActor); err != nil {
		t.Fatalf("the accept completion is not durable: %v", err)
	}
	if acceptActor != acceptorRef {
		t.Fatalf("the accept completion producer actor = %q, want the acceptor %q", acceptActor, acceptorRef)
	}
	if got := verdictItemVersion(t, s, workID); got <= versionBeforeAccept {
		t.Fatalf("work version after the accept = %d, want an advance past %d", got, versionBeforeAccept)
	}
	var stepAfterAccept string
	if err := s.db.QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&stepAfterAccept); err != nil {
		t.Fatal(err)
	}
	if stepAfterAccept != "refine" {
		t.Fatalf("current step after the accept = %q, want refine", stepAfterAccept)
	}

	// The real evidence bind continues at refine: bind_evidence also selects
	// no slot, so capacity admission never reads the overflowing context for
	// it.
	versionBeforeBind := verdictItemVersion(t, s, workID)
	if err := runVerdictActionAs(t, s, workID, "bind_evidence", json.RawMessage(`{"evidence_kind":"verification","immutable_subject_ref":"evidence:custody-continuation"}`), 0, acceptor); err != nil {
		t.Fatalf("bind_evidence refused over the overflowing context: %v", err)
	}
	var bound int
	if err := s.db.QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.evidence_kind')='verification' AND json_extract(payload,'$.immutable_subject_ref')='evidence:custody-continuation'`, workID, WorkflowEvidenceBound).Scan(&bound); err != nil {
		t.Fatal(err)
	}
	if bound != 1 {
		t.Fatalf("verification evidence_bound events = %d, want 1", bound)
	}
	var bindCompletions int
	if err := s.db.QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='bind_evidence'`, workID, WorkflowActionCompleted).Scan(&bindCompletions); err != nil {
		t.Fatal(err)
	}
	if bindCompletions == 0 {
		t.Fatal("the bind_evidence completion is not durable")
	}
	var bindOperations int
	if err := s.db.QueryRow(`SELECT count(*) FROM durable_operations WHERE work_id=? AND op_id LIKE 'bind_evidence-%'`, workID).Scan(&bindOperations); err != nil {
		t.Fatal(err)
	}
	if bindOperations == 0 {
		t.Fatal("the bind_evidence producer operation is not durable")
	}
	if got := verdictItemVersion(t, s, workID); got <= versionBeforeBind {
		t.Fatalf("work version after the bind = %d, want an advance past %d", got, versionBeforeBind)
	}
	var stepAfterBind string
	if err := s.db.QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&stepAfterBind); err != nil {
		t.Fatal(err)
	}
	if stepAfterBind != "refine" {
		t.Fatalf("current step after the bind = %q, want refine held", stepAfterBind)
	}

	// Both actions left the retained terminal custody and the over-slot
	// charge exactly as they found them.
	if custodyStoredPayload(t, s, "capc-evidence-failed") != failedPayload || custodyStoredPayload(t, s, "capc-evidence-completed") != completedPayload {
		t.Fatal("the accept or evidence bind rewrote a retained terminal payload")
	}
	if got := custodyAttemptState(t, s, workID, "capc-evidence-attempt-a"); got != "failed" {
		t.Fatalf("attempt a lifecycle_state after the actions = %q, want failed", got)
	}
	if got := custodyAttemptState(t, s, workID, "capc-evidence-attempt-b"); got != "completed" {
		t.Fatalf("attempt b lifecycle_state after the actions = %q, want completed", got)
	}
	custodyAssertViewHolds(t, ctx, s, workID, 4)
	custodyAssertContextOverSlot(t, ctx, s, workID)
}
