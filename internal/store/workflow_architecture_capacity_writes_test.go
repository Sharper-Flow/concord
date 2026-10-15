package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestArchitectureCapacityWriterAtomicity is the event-sourced capacity
// invariant witness: after one operation's events fold and
// before the same transaction commits, every affected work item whose latest
// retained architecture projection declares an allocation must hold its
// actual current member projections inside that allocation. Each material
// writer family — intent, proposal, design, contract and successor contract,
// worker job, context checkpoint, correction, declared context, and terminal
// findings — is driven through its owning route, and the over-allocation
// refusal commits zero effects.

// capacityWritesWitnessStore seeds one work item and records the closed
// witness-plan design on it, so the capacity invariant is active for every
// later operation the test applies.
func capacityWritesWitnessStore(t *testing.T, workID string) *Store {
	t.Helper()
	s := openTemp(t)
	t.Cleanup(func() { s.Close() })
	seedWork(t, s, workID)
	plan := projectionArchitecture()
	if err := applyProjectionEvents(t, s, workID, 2, projectionDesignEvent(t, workID, "capw-design-"+workID, 2, "witness", &plan)); err != nil {
		t.Fatalf("witness design refused: %v", err)
	}
	return s
}

// capacityWritesRefusal asserts one typed slot over-allocation refusal that
// names the slot, the work, and the required and allowed byte counts — the
// same diagnostic the packet capacity owner returns, with no separate
// bookkeeping call.
func capacityWritesRefusal(t *testing.T, err error, slot string) *Failure {
	t.Helper()
	if err == nil {
		t.Fatalf("the store accepted an over-allocation the %s invariant must refuse", slot)
	}
	failure, ok := err.(*Failure)
	if !ok {
		t.Fatalf("refusal is not typed: %v", err)
	}
	if failure.Kind != KindLimitExceeded {
		t.Fatalf("refusal kind = %q, want %q: %v", failure.Kind, KindLimitExceeded, err)
	}
	for _, want := range []string{slot, "canonical bytes"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("slot refusal lacks %q: %v", want, err)
		}
	}
	return failure
}

// capacityWritesZeroEffects asserts the refused operation committed nothing:
// the work version, its event count, and its design rows are unchanged.
func capacityWritesZeroEffects(t *testing.T, s *Store, workID string, version, events, rows int64) {
	t.Helper()
	if got := projectionWorkVersion(t, s, workID); got != version {
		t.Fatalf("the refused operation moved the work version to %d, want %d", got, version)
	}
	if got := int64(projectionEventCount(t, s, workID)); got != events {
		t.Fatalf("the refused operation changed the event count to %d, want %d", got, events)
	}
	if got := int64(projectionDesignRowCount(t, s, workID)); got != rows {
		t.Fatalf("the refused operation changed the design row count to %d, want %d", got, rows)
	}
}

func capacityWritesSnapshot(t *testing.T, s *Store, workID string) (version, events, rows int64) {
	t.Helper()
	return projectionWorkVersion(t, s, workID), int64(projectionEventCount(t, s, workID)), int64(projectionDesignRowCount(t, s, workID))
}

// capacityWritesOverApproachEvent builds one design event whose record
// over-charges the design_record slot: the maximal closed architecture fills
// its own 8,192 canonical bytes and the approach pushes the whole member past
// the 10,240 reserve. The design itself is structurally valid, so the
// refusal can only come from the capacity invariant.
func capacityWritesOverApproachEvent(t *testing.T, workID, eventID string, expected int64) Event {
	t.Helper()
	plan := maximalProjectionArchitecture(t)
	content := projectionDesignContent("over")
	content.Approach = strings.Repeat("x", 2600)
	request := WorkflowActionExecutionRequest{WorkID: workID, Now: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
	events, err := workflowDesignRecordedEvents(request, "actor:test", projectionDesignRaw(t, content, &plan), eventID, expected)
	if err != nil {
		t.Fatalf("over-approach design writer refused: %v", err)
	}
	return events[0]
}

func capacityWritesIntentEvent(t *testing.T, workID string, expected int64, task string) Event {
	t.Helper()
	payload := map[string]any{
		"expected_version": expected, "resulting_version": expected + 1,
		"title": workID, "value_statement": "the witness value", "kind": "task",
		"priority": 10, "tags": []string{}, "reason": "capacity writer test",
	}
	if task != "" {
		payload["task"] = task
	}
	return workflowEvent("capw-intent-"+workID+"-"+fmt.Sprint(expected), "work.intent_revised", workID, payload)
}

func TestArchitectureCapacityWriterAtomicity(t *testing.T) {
	ctx := context.Background()

	t.Run("design record over allocation refuses with zero effects", func(t *testing.T) {
		const workID = "capw-design-over"
		s := capacityWritesWitnessStore(t, workID)
		version, events, rows := capacityWritesSnapshot(t, s, workID)
		err := applyProjectionEvents(t, s, workID, version, capacityWritesOverApproachEvent(t, workID, "capw-design-over-event", version))
		_ = capacityWritesRefusal(t, err, "design_record")
		capacityWritesZeroEffects(t, s, workID, version, events, rows)
	})

	t.Run("whole operation validates final state not intermediate events", func(t *testing.T) {
		const workID = "capw-atomic"
		s := capacityWritesWitnessStore(t, workID)
		plan := projectionArchitecture()
		over := capacityWritesOverApproachEvent(t, workID, "capw-atomic-over", 3)
		fitting := projectionDesignEvent(t, workID, "capw-atomic-fit", 4, "fitting", &plan)
		// The first event alone refuses.
		version, events, rows := capacityWritesSnapshot(t, s, workID)
		_ = capacityWritesRefusal(t, applyProjectionEvents(t, s, workID, version, over), "design_record")
		capacityWritesZeroEffects(t, s, workID, version, events, rows)
		// The same over event followed by a fitting design in one operation
		// commits: the operation's final state fits its allocation.
		if err := applyProjectionEvents(t, s, workID, version, over, fitting); err != nil {
			t.Fatalf("the atomic operation whose final state fits refused: %v", err)
		}
		if got := projectionDesignRowCount(t, s, workID); got != 3 {
			t.Fatalf("design rows after the atomic operation = %d, want 3", got)
		}
		design, _, err := readCurrentWorkflowDesign(ctx, s.db, workID)
		if err != nil || design == nil || design.Approach != "Projection approach" {
			t.Fatalf("current design after the atomic operation = %+v, %v, want the fitting design", design, err)
		}
	})

	t.Run("intent growth refuses through the generic route with zero effects", func(t *testing.T) {
		const workID = "capw-intent"
		s := capacityWritesWitnessStore(t, workID)
		version, events, rows := capacityWritesSnapshot(t, s, workID)
		intent := capacityWritesIntentEvent(t, workID, version, strings.Repeat("t", 4100))
		err := ApplyOperation(ctx, s, Operation{Events: []Event{intent}, ExpectedVersions: workVersion(workID, version)})
		_ = capacityWritesRefusal(t, err, "work_record")
		capacityWritesZeroEffects(t, s, workID, version, events, rows)
		// A bounded revision commits and lands its recorded task.
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{capacityWritesIntentEvent(t, workID, version, "a bounded task")}, ExpectedVersions: workVersion(workID, version)}); err != nil {
			t.Fatalf("bounded intent revision refused: %v", err)
		}
		var task string
		if err := s.db.QueryRow(`SELECT coalesce(json_extract(intent_json,'$.task'),'') FROM work_items WHERE id=?`, workID).Scan(&task); err != nil {
			t.Fatal(err)
		}
		if task != "a bounded task" {
			t.Fatalf("recorded task = %q, want the revised value", task)
		}
	})

	t.Run("legacy work without an allocation keeps its larger bounds", func(t *testing.T) {
		const workID = "capw-legacy"
		s := openTemp(t)
		defer s.Close()
		seedWork(t, s, workID)
		huge := strings.Repeat("t", 4100)
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{capacityWritesIntentEvent(t, workID, 2, huge)}, ExpectedVersions: workVersion(workID, 2)}); err != nil {
			t.Fatalf("a work without a retained allocation must keep its released bounds: %v", err)
		}
	})

	t.Run("proposal growth refuses and a bounded proposal commits", func(t *testing.T) {
		const workID = "capw-proposal"
		s := capacityWritesWitnessStore(t, workID)
		version, events, rows := capacityWritesSnapshot(t, s, workID)
		over := proposalEvent("capw-proposal-over", workID, version, nil)
		payload := map[string]any{}
		if err := json.Unmarshal(over.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		payload["problem"] = strings.Repeat("p", 600)
		over.Payload = mustJSONValue(payload)
		_ = capacityWritesRefusal(t, applyProjectionEvents(t, s, workID, version, over), "proposal_record")
		capacityWritesZeroEffects(t, s, workID, version, events, rows)
		if err := applyProjectionEvents(t, s, workID, version, proposalEvent("capw-proposal-fit", workID, version, nil)); err != nil {
			t.Fatalf("bounded proposal refused: %v", err)
		}
	})

	t.Run("contract approval over premise refuses with zero effects", func(t *testing.T) {
		const workID = "capw-contract"
		s := capacityWritesWitnessStore(t, workID)
		_ = capacityWritesApproveContract(t, s, workID, strings.Repeat("premise ", 512), false)
		// The bounded premise approves and the contract row exists.
		_ = capacityWritesApproveContract(t, s, workID, "deliver the bounded change", true)
		var active int
		if err := s.db.QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&active); err != nil {
			t.Fatal(err)
		}
		if active != 1 {
			t.Fatalf("active contracts = %d, want 1", active)
		}
	})

	t.Run("contract premise at the exact slot boundary", func(t *testing.T) {
		// The task member charges key, quotes, colon, and its trailing comma
		// through the shared span helper: a 4,086-byte decoded premise fills
		// the 4,096 reserve exactly and approves; one more decoded byte
		// charges 4,097 and refuses. Both premises stay inside the released
		// 4,096-byte decoded premise bound, so the refusal can only come
		// from the capacity invariant.
		for _, testCase := range []struct {
			name       string
			decodedLen int
			wantSpan   int
			wantCommit bool
		}{
			{"exact span approves", 4086, 4096, true},
			{"one byte over refuses", 4087, 4097, false},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				const workID = "capw-premise-boundary"
				s := capacityWritesWitnessStore(t, workID)
				premise := strings.Repeat("p", testCase.decodedLen)
				taskSpan := architectureMemberSpan("task", mustJSONValue(premise), true)
				if taskSpan != testCase.wantSpan {
					t.Fatalf("task member span for a %d-byte premise = %d, want %d", testCase.decodedLen, taskSpan, testCase.wantSpan)
				}
				if !testCase.wantCommit {
					err := capacityWritesApproveContract(t, s, workID, premise, false)
					if !strings.Contains(err.Error(), "4097") || !strings.Contains(err.Error(), "4096") {
						t.Fatalf("boundary refusal lacks the required 4097 and allowed 4096 counts: %v", err)
					}
					return
				}
				_ = capacityWritesApproveContract(t, s, workID, premise, true)
			})
		}
	})

	t.Run("initial architecture admission over allocation refuses", func(t *testing.T) {
		const workID = "capw-first-design"
		s := openTemp(t)
		defer s.Close()
		seedWork(t, s, workID)
		// The work carries no previous architecture: the first design's own
		// admission passes through the same fold and the same invariant.
		version, events, rows := capacityWritesSnapshot(t, s, workID)
		err := applyProjectionEvents(t, s, workID, version, capacityWritesOverApproachEvent(t, workID, "capw-first-design-over", version))
		_ = capacityWritesRefusal(t, err, "design_record")
		capacityWritesZeroEffects(t, s, workID, version, events, rows)
		if got := projectionDesignRowCount(t, s, workID); got != 0 {
			t.Fatalf("the refused first design left %d rows", got)
		}
	})

	t.Run("successor contract over premise refuses whole", func(t *testing.T) {
		for _, testCase := range []struct {
			name    string
			premise string
			want    bool
		}{
			{"bounded successor commits", "corrected bounded premise", true},
			{"over premise refuses", strings.Repeat("premise ", 512), false},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				const workID = "capw-successor"
				s, owner, reviewer := seedItemAtAcceptance(t, workID, false)
				defer s.Close()
				operator := operatorVerdictActor(t, workID)
				plan := projectionArchitecture()
				version := projectionWorkVersion(t, s, workID)
				if err := applyProjectionEvents(t, s, workID, version, projectionDesignEvent(t, workID, "capw-successor-design", version, "witness", &plan)); err != nil {
					t.Fatalf("witness design refused: %v", err)
				}
				if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:primary","verdict_kind":"ok"}`), 0, reviewer); err != nil {
					t.Fatalf("record the compatible verdict: %v", err)
				}
				seedComparisonObservation(t, s, workID)
				successor := fmt.Sprintf(`{"contract_version":2,"premise":%q,"outcome_predicates":[{"predicate_id":"predicate:primary","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:capw","immutable_subject_ref":"commit:%s","expected_result":"pass"}}],"required_evidence":["verification"],"route_conventions":[],"spec_mandate":[],"law_modifies":[],"rigor_class":"prototype_internal","supersede_reason":"capacity writer test","audit_evidence":["evidence:capw"]}`, testCase.premise, workID)
				// Capture every durable surface the workflow action route
				// writes before the call, so the refusal proves the whole
				// operation rolled back: version, events, design rows,
				// durable operation rows, and the active contract.
				versionBefore := projectionWorkVersion(t, s, workID)
				eventsBefore := int64(projectionEventCount(t, s, workID))
				rowsBefore := int64(projectionDesignRowCount(t, s, workID))
				var operationsBefore int64
				if err := s.db.QueryRow(`SELECT count(*) FROM durable_operations WHERE work_id=?`, workID).Scan(&operationsBefore); err != nil {
					t.Fatal(err)
				}
				err := runIssue933OperatorAction(t, s, workID, "supersede_contract", json.RawMessage(successor), owner, operator)
				if !testCase.want {
					_ = capacityWritesRefusal(t, err, "task")
					capacityWritesZeroEffects(t, s, workID, versionBefore, eventsBefore, rowsBefore)
					var operationsAfter int64
					if err := s.db.QueryRow(`SELECT count(*) FROM durable_operations WHERE work_id=?`, workID).Scan(&operationsAfter); err != nil {
						t.Fatal(err)
					}
					if operationsAfter != operationsBefore {
						t.Fatalf("the refused successor changed the durable operation count to %d, want %d", operationsAfter, operationsBefore)
					}
					var active int
					if err := s.db.QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&active); err != nil {
						t.Fatal(err)
					}
					if active != 1 {
						t.Fatalf("active contract after the refused successor = %d, want 1", active)
					}
					return
				}
				if err != nil {
					t.Fatalf("bounded successor contract refused: %v", err)
				}
			})
		}
	})

	t.Run("retained allocation survives design invalidation", func(t *testing.T) {
		const workID = "capw-invalidate"
		s := capacityWritesWitnessStore(t, workID)
		_ = capacityWritesApproveContract(t, s, workID, "deliver the bounded change", true)
		actorRef := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/owner", "session/"+workID)
		version := projectionWorkVersion(t, s, workID)
		supersede := workflowEventWithActor("capw-supersede", WorkflowContractSuperseded, workID, actorRef, map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1,
			"previous_contract_version": 1, "new_contract_version": 2,
			"supersede_reason": "capacity invalidation test", "audit_evidence": []string{"evidence:capw"},
		})
		if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{supersede}, ExpectedVersions: workVersion(workID, version)}); err != nil {
			t.Fatalf("contract supersession refused: %v", err)
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		design, invalidated, err := readCurrentWorkflowDesign(ctx, tx, workID)
		if err != nil || design != nil || !invalidated {
			t.Fatalf("current design after supersession = %+v invalidated=%v err=%v, want nil and invalidated", design, invalidated, err)
		}
		tx.Rollback()
		// The hidden design cannot turn its allocation off: the over-sized
		// intent revision still refuses on the retained reserves.
		version, events, rows := capacityWritesSnapshot(t, s, workID)
		intent := capacityWritesIntentEvent(t, workID, version, strings.Repeat("t", 4100))
		intentErr := ApplyOperation(ctx, s, Operation{Events: []Event{intent}, ExpectedVersions: workVersion(workID, version)})
		_ = capacityWritesRefusal(t, intentErr, "work_record")
		capacityWritesZeroEffects(t, s, workID, version, events, rows)
	})

	t.Run("worker job growth refuses and a bounded job commits", func(t *testing.T) {
		const workID = "capw-job"
		fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
		s := fixture.store
		plan := projectionArchitecture()
		version := projectionWorkVersion(t, s, workID)
		if err := applyProjectionEvents(t, s, workID, version, projectionDesignEvent(t, workID, "capw-job-design", version, "witness", &plan)); err != nil {
			t.Fatalf("witness design refused: %v", err)
		}
		// The bounded job fits its slot and commits.
		job := &WorkerJobBinding{JobID: "job:capw-fit", Revision: 1}
		recordWorkerJobRevisionForTest(t, s, workID, fixture.owner, job)
		// Sixty-four unique unresolved references at their bound exceed the
		// worker_job reserve, so the recording refuses whole.
		unresolved := make([]string, 64)
		for i := range unresolved {
			unresolved[i] = fmt.Sprintf("%s-%02d", strings.Repeat("u", 150), i)
		}
		version, events, rows := capacityWritesSnapshot(t, s, workID)
		over := map[string]any{
			"job_id": "job:capw-over", "objective": strings.Repeat("o", 4096),
			"stopping_condition":   strings.Repeat("s", 2048),
			"reserved_integration": strings.Repeat("i", 4096),
			"path_scope":           []string{"internal/store"}, "checks": []string{"go test ./internal/store/"},
			"unresolved_refs": unresolved, "ready": false,
		}
		if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, over); err != nil {
			_ = capacityWritesRefusal(t, err, "worker_job")
		} else {
			t.Fatal("the over-allocated job recording was accepted")
		}
		capacityWritesZeroEffects(t, s, workID, version, events, rows)
		var recorded int
		if err := s.db.QueryRow(`SELECT count(*) FROM worker_job_revisions WHERE work_id=? AND job_id='job:capw-over'`, workID).Scan(&recorded); err != nil {
			t.Fatal(err)
		}
		if recorded != 0 {
			t.Fatalf("the refused job left %d revision rows", recorded)
		}
	})

	t.Run("context checkpoint growth refuses and a bounded checkpoint commits", func(t *testing.T) {
		const workID = "capw-checkpoint"
		fixture := seedWorkContextFixture(t, workID)
		s := fixture.store
		plan := projectionArchitecture()
		version := projectionWorkVersion(t, s, workID)
		if err := applyProjectionEvents(t, s, workID, version, projectionDesignEvent(t, workID, "capw-checkpoint-design", version, "witness", &plan)); err != nil {
			t.Fatalf("witness design refused: %v", err)
		}
		ownerRef, err := WorkflowActorRef(fixture.owner)
		if err != nil {
			t.Fatal(err)
		}
		checkpoint := func(hypothesis, diagnosis, strategy string) Event {
			var ref, digest string
			var definitionVersion int64
			if err := s.db.QueryRow(`SELECT definition_ref,definition_digest,definition_version FROM workflow_instances WHERE work_id=?`, workID).Scan(&ref, &digest, &definitionVersion); err != nil {
				t.Fatal(err)
			}
			var epoch int64
			if err := s.db.QueryRow(`SELECT COALESCE(MAX(attempt_epoch),1) FROM durable_operations WHERE work_id=?`, workID).Scan(&epoch); err != nil {
				t.Fatal(err)
			}
			return workflowEventWithActor("capw-checkpoint-"+fmt.Sprint(len(hypothesis)), WorkflowContextCheckpointed, workID, ownerRef, map[string]any{
				"work_id": workID, "expected_version": projectionWorkVersion(t, s, workID), "resulting_version": projectionWorkVersion(t, s, workID) + 1,
				"checkpoint_id": "checkpoint:capw", "step_id": "repair", "attempt_epoch": epoch,
				"active_unit": "the capacity checkpoint unit", "hypothesis": hypothesis, "diagnosis": diagnosis, "strategy": strategy,
				"touched_refs": []string{"internal/store/workflow_architecture_capacity.go"}, "evidence_refs": []string{"evidence:capw"},
				"pending_questions": []string{}, "pending_decisions": []string{},
				"workflow_ref": ref, "workflow_definition_version": definitionVersion, "workflow_definition_digest": digest,
				"actor_ref": ownerRef, "request_id": "request:capw",
			})
		}
		version, events, rows := capacityWritesSnapshot(t, s, workID)
		over := checkpoint(strings.Repeat("h", 4000), strings.Repeat("d", 4000), strings.Repeat("s", 4000))
		if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{over}, ExpectedVersions: workVersion(workID, version)}); err != nil {
			_ = capacityWritesRefusal(t, err, "work_context")
		} else {
			t.Fatal("the over-sized checkpoint was accepted")
		}
		capacityWritesZeroEffects(t, s, workID, version, events, rows)
		fit := checkpoint("bounded hypothesis", "bounded diagnosis", "bounded strategy")
		if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{fit}, ExpectedVersions: workVersion(workID, version)}); err != nil {
			t.Fatalf("bounded checkpoint refused: %v", err)
		}
	})

	t.Run("declared context growth refuses and a bounded declaration commits", func(t *testing.T) {
		const workID = "capw-declared"
		fixture := seedWorkContextFixture(t, workID)
		s := fixture.store
		plan := projectionArchitecture()
		version := projectionWorkVersion(t, s, workID)
		if err := applyProjectionEvents(t, s, workID, version, projectionDesignEvent(t, workID, "capw-declared-design", version, "witness", &plan)); err != nil {
			t.Fatalf("witness design refused: %v", err)
		}
		if err := fixture.recordWorkContext(t, map[string]any{
			"required_reading": []map[string]any{workContextRepoReading(workContextTestChild, "")},
			"finding_refs":     []string{},
			"context_findings": []map[string]any{workContextFinding(workContextTestChild, "observation", "a bounded declaration reads back whole", "")},
		}); err != nil {
			t.Fatalf("bounded declaration refused: %v", err)
		}
		overFindings := make([]map[string]any, 6)
		for i := range overFindings {
			overFindings[i] = workContextFinding(workContextTestChild, "observation", strings.Repeat("f", 900), "")
		}
		version, events, rows := capacityWritesSnapshot(t, s, workID)
		if err := fixture.recordWorkContext(t, map[string]any{
			"required_reading": []map[string]any{workContextRepoReading(workContextTestChild, "")},
			"finding_refs":     []string{},
			"context_findings": overFindings,
		}); err != nil {
			_ = capacityWritesRefusal(t, err, "work_context")
		} else {
			t.Fatal("the over-sized declaration was accepted")
		}
		capacityWritesZeroEffects(t, s, workID, version, events, rows)
	})

	t.Run("terminal findings growth refuses and a bounded report commits", func(t *testing.T) {
		const workID = "capw-terminal"
		// The findings-capable break-fix head predates the failure-attribution
		// terminal requirement, so a plain failed report is admissible there.
		breakFix, ok := BuiltinWorkflowRegistry().Lookup("workflow.break_fix", 27)
		if !ok {
			t.Fatal("workflow.break_fix v27 is not registered")
		}
		fixture := seedWorkContextFixtureWithDefinition(t, workID, breakFix)
		s := fixture.store
		plan := projectionArchitecture()
		version := projectionWorkVersion(t, s, workID)
		if err := applyProjectionEvents(t, s, workID, version, projectionDesignEvent(t, workID, "capw-terminal-design", version, "witness", &plan)); err != nil {
			t.Fatalf("witness design refused: %v", err)
		}
		lane := BuiltinLaneDefinitions()[0]
		model := preferredModelForLane(lane)
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{workerDispatchEvent(workID, "capw-terminal-dispatch-fit", lane, nil)}}); err != nil {
			t.Fatalf("the bounded report dispatch refused: %v", err)
		}
		fit := workerFailedContextFindingsEvent(workID, "capw-terminal-failed-fit", "capw-terminal-dispatch-fit", model, WorkerFailureWorkerError, nil, WorkerEvidenceEventPayloadVersion(WorkerFailed))
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{fit}}); err != nil {
			t.Fatalf("the bounded terminal report refused: %v", err)
		}
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{workerDispatchEvent(workID, "capw-terminal-dispatch-over", lane, nil)}}); err != nil {
			t.Fatalf("the over findings dispatch refused: %v", err)
		}
		overFindings := make([]WorkerContextFinding, 5)
		for i := range overFindings {
			overFindings[i] = contextFindingFixture()
			overFindings[i].Statement = strings.Repeat("f", 900)
		}
		version, events, rows := capacityWritesSnapshot(t, s, workID)
		over := workerFailedContextFindingsEvent(workID, "capw-terminal-failed-over", "capw-terminal-dispatch-over", model, WorkerFailureWorkerError, overFindings, WorkerEvidenceEventPayloadVersion(WorkerFailed))
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{over}}); err != nil {
			_ = capacityWritesRefusal(t, err, "work_context")
		} else {
			t.Fatal("the over-sized terminal findings were accepted")
		}
		capacityWritesZeroEffects(t, s, workID, version, events, rows)
		// The refused report left the attempt dispatched: the terminal fold
		// never promoted it, and no worker.failed event for it exists.
		var attemptState string
		if err := s.db.QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, "capw-terminal-dispatch-over").Scan(&attemptState); err != nil {
			t.Fatal(err)
		}
		if attemptState != "dispatched" {
			t.Fatalf("the refused report left the attempt %q, want dispatched", attemptState)
		}
		var failedEvents int
		if err := s.db.QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.attempt_id')=?`, workID, WorkerFailed, "capw-terminal-dispatch-over").Scan(&failedEvents); err != nil {
			t.Fatal(err)
		}
		if failedEvents != 0 {
			t.Fatalf("the refused report left %d worker.failed events for the attempt", failedEvents)
		}
	})

	t.Run("correction growth refuses and a bounded record commits", func(t *testing.T) {
		for _, testCase := range []struct {
			name      string
			diagnosis string
			want      bool
		}{
			{"bounded correction commits", "the verdict found a stale artifact", true},
			{"over correction refuses", strings.Repeat("c", 4000), false},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				const workID = "capw-correction"
				s, owner, reviewer := seedItemAtAcceptance(t, workID, false)
				defer s.Close()
				operator := operatorVerdictActor(t, workID)
				plan := projectionArchitecture()
				version := projectionWorkVersion(t, s, workID)
				if err := applyProjectionEvents(t, s, workID, version, projectionDesignEvent(t, workID, "capw-correction-design", version, "witness", &plan)); err != nil {
					t.Fatalf("witness design refused: %v", err)
				}
				if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:primary","verdict_kind":"outcome_mismatch","incomparable_with_approved":true}`), 0, reviewer); err != nil {
					t.Fatalf("record the mismatch verdict: %v", err)
				}
				var verdictEvidence string
				if err := s.db.QueryRow(`SELECT json_extract(payload,'$.evaluation_evidence[0]') FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, workID, WorkflowVerdictRecorded).Scan(&verdictEvidence); err != nil {
					t.Fatal(err)
				}
				correction := fmt.Sprintf(`{"diagnosis":%q,"strategy":"repeat execution with a fresh worker","predicate_ids":["predicate:primary"],"evidence_refs":["%s"]}`, testCase.diagnosis, verdictEvidence)
				// Capture before the action, so the refusal proves the
				// request committed nothing rather than comparing the store
				// to its post-refusal state.
				versionBefore := projectionWorkVersion(t, s, workID)
				eventsBefore := int64(projectionEventCount(t, s, workID))
				rowsBefore := int64(projectionDesignRowCount(t, s, workID))
				err := runIssue933OperatorAction(t, s, workID, "request_correction", json.RawMessage(correction), owner, operator)
				if !testCase.want {
					_ = capacityWritesRefusal(t, err, "work_context")
					capacityWritesZeroEffects(t, s, workID, versionBefore, eventsBefore, rowsBefore)
					return
				}
				if err != nil {
					t.Fatalf("bounded correction request refused: %v", err)
				}
			})
		}
	})

	t.Run("unknown retained allocation refuses instead of measuring zero", func(t *testing.T) {
		const workID = "capw-unknown"
		s := capacityWritesWitnessStore(t, workID)
		version := projectionWorkVersion(t, s, workID)
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := enterFold(ctx, tx); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO workflow_design_records(work_id,work_version,approach,decisions,touched_refs,architecture,revision,digest,recorded_at) VALUES(?,?,?,?,?,?,?,?,?)`,
			workID, version+5, "Broken approach", `[{"id":"decision:approach","question":"Which plan?","choice":"broken","rationale":"unknown allocation","rejected":[]}]`, `["ref"]`,
			`{"mystery":true}`, 9, "sha256:"+strings.Repeat("a", 64), "2026-10-15T00:00:00Z")
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := leaveFold(ctx, tx); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		// The latest retained architecture row cannot decode, so the
		// invariant refuses the operation instead of treating the unknown
		// allocation as absent or zero.
		intent := capacityWritesIntentEvent(t, workID, version, "a bounded task")
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{intent}, ExpectedVersions: workVersion(workID, version)}); err == nil {
			t.Fatal("an unknown retained allocation measured as zero")
		} else if !strings.Contains(err.Error(), "mystery") && !strings.Contains(err.Error(), "closed") {
			t.Fatalf("unknown allocation refusal does not name the decode cause: %v", err)
		}
	})

	t.Run("capacity reads stay inside the caller transaction", func(t *testing.T) {
		const workID = "capw-tx-only"
		s := capacityWritesWitnessStore(t, workID)
		ctxTimeout, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		tx, err := s.db.BeginTx(ctxTimeout, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := enterFold(ctxTimeout, tx); err != nil {
			t.Fatal(err)
		}
		// The store pools one connection: any read that left the caller's
		// transaction for s.db would park on the pool until this deadline.
		if err := validateArchitectureCapacityAffectedWorkTx(ctxTimeout, tx, workID); err != nil {
			t.Fatalf("the capacity validation cannot run inside the caller transaction: %v", err)
		}
	})

	t.Run("rebuild reproduces a valid history and the invariant survives it", func(t *testing.T) {
		const workID = "capw-rebuild"
		s := capacityWritesWitnessStore(t, workID)
		if err := applyProjectionEvents(t, s, workID, 3, proposalEvent("capw-rebuild-proposal", workID, 3, nil)); err != nil {
			t.Fatalf("bounded proposal refused: %v", err)
		}
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{capacityWritesIntentEvent(t, workID, 4, "a bounded task")}, ExpectedVersions: workVersion(workID, 4)}); err != nil {
			t.Fatalf("bounded intent refused: %v", err)
		}
		before := projectionDesignRows(t, s, workID)
		beforeVersion := projectionWorkVersion(t, s, workID)
		if err := RebuildFromLog(ctx, s); err != nil {
			t.Fatalf("rebuild of the valid history refused: %v", err)
		}
		after := projectionDesignRows(t, s, workID)
		if len(before) != len(after) {
			t.Fatalf("rebuild changed the design row count %d -> %d", len(before), len(after))
		}
		for i := range before {
			if before[i] != after[i] {
				t.Fatalf("rebuild changed design row %d:\n got %s\nwant %s", i, after[i], before[i])
			}
		}
		if got := projectionWorkVersion(t, s, workID); got != beforeVersion {
			t.Fatalf("rebuild moved the work version to %d, want %d", got, beforeVersion)
		}
		version, events, rows := capacityWritesSnapshot(t, s, workID)
		intent := capacityWritesIntentEvent(t, workID, version, strings.Repeat("t", 4100))
		err := ApplyOperation(ctx, s, Operation{Events: []Event{intent}, ExpectedVersions: workVersion(workID, version)})
		_ = capacityWritesRefusal(t, err, "work_record")
		capacityWritesZeroEffects(t, s, workID, version, events, rows)
	})

	t.Run("authorized removal of an architecture-bearing work is not blocked", func(t *testing.T) {
		const workID = "capw-removal"
		s := capacityWritesWitnessStore(t, workID)
		// The removal fold deletes the retained design rows inside its own
		// transaction before the post-fold capacity invariant runs, so the
		// invariant finds no allocation and no member reader errors on the
		// disappearing subject: the authorized removal commits.
		receipt, err := s.RemoveWork(ctx, WorkRemovalRequest{
			OperationID: "capw-removal-op", IdempotencyKey: "capw-removal-key", WorkID: workID,
			ExpectedVersion: 3, Reason: "shelved", Actor: "operator:one",
			Handoff: WorkRemovalHandoff{
				Findings: []string{"finding"}, RemainingScope: []string{"scope"}, Blockers: []string{"blocker"},
				Artifacts: []string{"artifact"}, RenewalConditions: []string{"new approval"},
			}, ExecutionRelinquished: true, WritesReconciled: true, EffectsReconciled: true,
			DependenciesResolved: true, ArtifactsVerified: true,
		})
		if err != nil {
			t.Fatalf("removing an architecture-bearing work refused: %v", err)
		}
		if receipt.State != "committed" || receipt.EventID == "" {
			t.Fatalf("removal receipt = %+v, want a committed receipt", receipt)
		}
		if got := projectionDesignRowCount(t, s, workID); got != 0 {
			t.Fatalf("the removal left %d design rows", got)
		}
		var workRows int
		if err := s.db.QueryRow(`SELECT count(*) FROM work_items WHERE id=?`, workID).Scan(&workRows); err != nil {
			t.Fatal(err)
		}
		if workRows != 0 {
			t.Fatalf("the removal left %d work rows", workRows)
		}
	})
}

// capacityWritesApproveContract drives the contract-approval family through
// one direct operation: actor, definition, and the approval whose premise the
// caller owns. expectCommit asserts the operation's outcome; a refusal must
// leave the whole operation rolled back.
func capacityWritesApproveContract(t *testing.T, s *Store, workID string, premise string, expectCommit bool) error {
	t.Helper()
	expected := int64(3)
	actorRef := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/owner", "session/"+workID)
	events := []Event{
		workflowEventWithActor("capw-contract-actor-"+workID, WorkflowActorRecorded, workID, actorRef, map[string]any{
			"work_id": workID, "expected_version": expected, "resulting_version": expected + 1,
			"actor_ref": actorRef, "principal_ref": "principal/operator", "client_ref": "client/concord-1",
			"agent_ref": "agent/owner", "session_ref": "session/" + workID, "actor_class": "agent",
		}),
		workflowEvent("capw-contract-definition-"+workID, WorkflowDefinitionSelected, workID, map[string]any{
			"work_id": workID, "expected_version": expected + 1, "resulting_version": expected + 2,
			"ref": workflowFixtureRef, "version": 1, "digest": workflowFixtureDigest(t), "work_kind": workflowFixtureWorkKind,
		}),
		workflowEventWithActor("capw-contract-approved-"+workID, WorkflowContractApproved, workID, actorRef, map[string]any{
			"work_id": workID, "expected_version": expected + 2, "resulting_version": expected + 3,
			"contract_version": 1, "premise": premise, "outcome_kind": "check",
			"outcome_payload":   map[string]any{"kind": "check", "check_ref": "check:capw", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
			"required_evidence": []string{"verification"}, "route_conventions": []string{},
			"spec_mandate": []string{}, "law_modifies": []string{},
			"rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
		}),
	}
	// Capture before the operation, so a refusal proves zero committed
	// effects rather than comparing the store to itself.
	version, eventsBefore, rowsBefore := capacityWritesSnapshot(t, s, workID)
	err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: events, ExpectedVersions: workVersion(workID, expected)})
	if !expectCommit {
		_ = capacityWritesRefusal(t, err, "task")
		capacityWritesZeroEffects(t, s, workID, version, eventsBefore, rowsBefore)
		return err
	}
	if err != nil {
		t.Fatalf("bounded contract approval refused: %v", err)
	}
	return nil
}
