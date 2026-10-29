package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The artifact-staleness fold and its admission halves (CD-0201 D5/D6),
// exercised over current facts: the staleness causes, the production
// frontier at the declared route target, the healthy-association refusal, and
// the premise refusal.

// pinWorkflowAtStep initializes a work item with its current definition and
// walks it to step, so the fold reads a real instance row.
func pinWorkflowAtStep(t *testing.T, s *Store, workID, ref, step string) {
	t.Helper()
	entry, ok := BuiltinWorkflowRegistry().Current(ref)
	if !ok {
		t.Fatalf("%s is not registered", ref)
	}
	seedStepWork(t, s, workID)
	initializeStepWorkflow(t, s, workID, entry.Definition)
	if step != "" {
		actorRef, err := WorkflowActorRef(stepFixtureActor())
		if err != nil {
			t.Fatal(err)
		}
		if err := advanceWorkflowTestInstanceToStep(context.Background(), s, workID, step, actorRef); err != nil {
			t.Fatal(err)
		}
	}
}

// stalenessSeedContract approves contract version 1 with one check predicate,
// so the verdict folds read a real active contract.
func stalenessSeedContract(t *testing.T, s *Store, workID string) {
	t.Helper()
	actorRef, err := WorkflowActorRef(stepFixtureActor())
	if err != nil {
		t.Fatal(err)
	}
	// The walk that moved the instance recorded the actor through the real
	// fold; workflow_actors is write-seam-only, so the contract row reuses it.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,?,'internal_sqlite','[]','[]','2026-01-01T00:00:00Z',?,'[]','[]',0,'prototype_internal'); INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES(?,1,'predicate:primary',0,'check','{"kind":"check"}'); DELETE FROM write_guard`, workID, "The premise the test contract states.", actorRef, workID); err != nil {
		t.Fatal(err)
	}
}

// stalenessEvent supplies trusted current descriptors without authorizing an
// action. The live-writer tests separately exercise admission and persistence.
func stalenessEvent(t *testing.T, s *Store, workID, kind, payload string) Event {
	t.Helper()
	actorRef, err := WorkflowActorRef(stepFixtureActor())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	var lastSeq int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(effect_seq),0) FROM effect_receipts`).Scan(&lastSeq); err != nil {
		t.Fatal(err)
	}
	event := Event{EventID: workID + ":" + kind + ":after:" + fmt.Sprint(lastSeq), Kind: kind, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: actorRef, OccurredAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), PayloadVersion: 1, Payload: json.RawMessage(payload)}
	nextSeq, err := appendPreparedEvent(t.Context(), tx, event)
	if err != nil {
		t.Fatal(err)
	}
	event.Seq = nextSeq
	switch kind {
	case WorkflowActionStarted:
		var start workflowActionStartedPayload
		if err := json.Unmarshal(event.Payload, &start); err != nil {
			t.Fatal(err)
		}
		if start.AttemptEpoch > 0 {
			if _, err := tx.Exec(`INSERT INTO workflow_step_epochs(work_id,step_id,attempt_epoch,latest_start_seq,latest_start_event_id,latest_start_payload) VALUES(?,?,?,?,?,?) ON CONFLICT(work_id,step_id) DO UPDATE SET attempt_epoch=excluded.attempt_epoch,latest_start_seq=excluded.latest_start_seq,latest_start_event_id=excluded.latest_start_event_id,latest_start_payload=excluded.latest_start_payload`, workID, start.StepID, start.AttemptEpoch, nextSeq, event.EventID, payload); err != nil {
				t.Fatal(err)
			}
		}
	case WorkflowActionCheckpointed:
		var checkpoint workflowActionCheckpointedPayload
		if err := json.Unmarshal(event.Payload, &checkpoint); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO workflow_checkpoints(work_id,checkpoint_id,step_id,step_kind,attempt_epoch,accepted_inputs_digest,result_evidence_refs,resume_cursor,idempotency_identity,actor_ref,request_id,recorded_at) VALUES(?,?,?,'internal_sqlite',?,'checkpoint-inputs','[]','',?,?,?,'2026-01-02T00:00:00Z')`, workID, event.EventID, checkpoint.StepID, checkpoint.AttemptEpoch, event.EventID, actorRef, event.EventID); err != nil {
			t.Fatal(err)
		}
	case WorkflowVerdictRecorded:
		var verdict workflowVerdictRecordedPayload
		if err := json.Unmarshal(event.Payload, &verdict); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO workflow_predicate_verdicts(work_id,contract_version,predicate_id,verdict_seq,actor,payload) VALUES(?,?,?,?,?,?) ON CONFLICT(work_id,contract_version,predicate_id) DO UPDATE SET verdict_seq=excluded.verdict_seq,actor=excluded.actor,payload=excluded.payload`, workID, verdict.ContractVersion, verdict.PredicateID, nextSeq, actorRef, payload); err != nil {
			t.Fatal(err)
		}
		if verdict.VerdictKind != "ok" || verdict.IncomparableWithApproved {
			if _, err := tx.Exec(`UPDATE workflow_instances SET unhealthy_verdict_seq=? WHERE work_id=?`, nextSeq, workID); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := tx.Exec(`UPDATE workflow_artifact_actions SET verdict_seq=?,verdict_production_seq=production_seq WHERE work_id=?`, nextSeq, workID); err != nil {
			t.Fatal(err)
		}
	case WorkflowActionCompleted:
		var action workflowActionCompletedPayload
		if err := json.Unmarshal(event.Payload, &action); err != nil {
			t.Fatal(err)
		}
		if workflowActionProducesArtifact(action.ActionID) {
			if _, err := tx.Exec(`INSERT INTO workflow_artifact_actions(work_id,step_id,action_id,production_seq) VALUES(?,?,?,?) ON CONFLICT(work_id,step_id,action_id) DO UPDATE SET production_seq=excluded.production_seq`, workID, action.StepID, action.ActionID, nextSeq); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := tx.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return event
}

func stalenessActionPayload(step, action string) string {
	encoded, _ := json.Marshal(map[string]any{"step_id": step, "action_id": action})
	return string(encoded)
}

func stalenessSeedDeliveryAt(t *testing.T, s *Store, workID, step string) {
	t.Helper()
	stalenessEvent(t, s, workID, WorkflowActionCompleted, stalenessActionPayload(step, "record_delivery"))
}

func stalenessVerdictPayload(kind string) string {
	encoded, _ := json.Marshal(map[string]any{"predicate_id": "predicate:primary", "verdict_kind": kind, "contract_version": 1})
	return string(encoded)
}

func TestUnfencedCheckpointEpochUsesCausalPrefix(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	const workID = "unfenced-checkpoint-prefix"
	definition := mustBuiltinDefinition(t, "workflow.implementation").Definition
	pinWorkflowAtStep(t, s, workID, definition.Ref, "")
	if err := ApplyOperation(t.Context(), s, Operation{Events: []Event{
		{EventID: "another-work-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "another-work", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion("work.created"), Payload: jsonRaw(`{"work_kind":"task","title":"Other checkpoint work","priority":1}`)},
		{EventID: "another-work-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: "another-work", Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"memberships":[{"project_id":"project-s","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "another-work"): 0}}); err != nil {
		t.Fatal(err)
	}
	step := workflowStep(definition, "acceptance")
	assertEpoch := func(want int64) {
		t.Helper()
		got, err := workflowCheckpointAttemptEpoch(context.Background(), s.db, definition, step, workID, step.ID)
		if err != nil || got != want {
			t.Fatalf("current checkpoint epoch = %d, %v; want %d", got, err, want)
		}
	}
	assertEpoch(1)
	stalenessEvent(t, s, workID, WorkflowActionCheckpointed, `{"step_id":"acceptance","attempt_epoch":1}`)
	assertEpoch(2)
	stalenessEvent(t, s, workID, WorkflowActionStarted, `{"step_id":"acceptance","action_id":"dispatch_worker","attempt_epoch":1}`)
	assertEpoch(2)
	stalenessEvent(t, s, workID, WorkflowActionCheckpointed, `{"step_id":"review","attempt_epoch":41}`)
	stalenessEvent(t, s, "another-work", WorkflowActionCheckpointed, `{"step_id":"acceptance","attempt_epoch":51}`)
	assertEpoch(2)
	stalenessEvent(t, s, workID, WorkflowActionCheckpointed, `{"step_id":"acceptance","attempt_epoch":2}`)
	assertEpoch(3)
	stalenessEvent(t, s, workID, WorkflowActionStarted, `{"step_id":"acceptance","action_id":"dispatch_worker","attempt_epoch":61}`)
	assertEpoch(61)
	stalenessEvent(t, s, workID, WorkflowActionCheckpointed, `{"step_id":"acceptance","attempt_epoch":61}`)
	assertEpoch(62)
	var logs int
	if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='domain_events'`).Scan(&logs); err != nil || logs != 0 {
		t.Fatalf("event log tables=%d, want zero: %v", logs, err)
	}
	assertEpoch(62)
	if _, err := workflowCheckpointAttemptEpoch(context.Background(), s.db, definition, workflowStep(definition, "execution"), workID, "execution"); !hasFailureKind(err, KindIllegalLifecycleTransition) {
		t.Fatalf("fenced checkpoint without a start = %v, want invalid_transition", err)
	}
}

// foldStalenessState opens the read transaction the fold requires and returns
// the folded admission state at the work item's current step.
func foldStalenessState(t *testing.T, s *Store, workID string, definition WorkflowDefinition) WorkflowAdmissionState {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var currentStep string
	if err := tx.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&currentStep); err != nil {
		t.Fatal(err)
	}
	state, _, foldErr := loadWorkflowAdmissionStateTxForRegistry(ctx, tx, workID, definition, currentStep, "staleness_test", BuiltinWorkflowRegistry())
	if foldErr != nil {
		t.Fatal(foldErr)
	}
	return state
}

// TestBadVerdictStalesArtifactUntilReproduced holds the causal core: a bad
// verdict sets the bit, neither a fenced start nor an evidence
// binding clears it, and only a recorded delivery at the declared
// route target does.
func TestBadVerdictStalesArtifactUntilReproduced(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	const workID = "stale-bad-verdict"
	definition := mustBuiltinDefinition(t, "workflow.implementation").Definition
	if target := workflowUnhealthyVerdictRouteTarget(definition, "acceptance"); target != "execution" {
		t.Fatalf("implementation acceptance route target = %q, want execution", target)
	}
	pinWorkflowAtStep(t, s, workID, definition.Ref, "acceptance")
	stalenessSeedContract(t, s, workID)
	// Production first: an accepted delivery at execution predates the bad
	// verdict, so the verdict alone decides staleness.
	stalenessSeedDeliveryAt(t, s, workID, "execution")
	stalenessEvent(t, s, workID, string(WorkflowVerdictRecorded), stalenessVerdictPayload("bad"))
	if state := foldStalenessState(t, s, workID, definition); !state.ArtifactStale {
		t.Fatal("bad verdict at acceptance did not stale the artifact")
	}
	// A fenced start at the target is start-only history: it never clears.
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), stalenessActionPayload("execution", "start_execution"))
	if state := foldStalenessState(t, s, workID, definition); !state.ArtifactStale {
		t.Fatal("fenced start cleared artifact staleness")
	}
	// Rebidding evidence at an unrelated step does not clear it.
	stalenessEvent(t, s, workID, string(WorkflowEvidenceBound), `{"evidence_ref":"evidence:stale"}`)
	if state := foldStalenessState(t, s, workID, definition); !state.ArtifactStale {
		t.Fatal("evidence binding cleared artifact staleness")
	}
	// The accepted delivery at the target is the fresh production.
	stalenessSeedDeliveryAt(t, s, workID, "execution")
	if state := foldStalenessState(t, s, workID, definition); state.ArtifactStale {
		t.Fatal("accepted delivery at the route target did not clear artifact staleness")
	}
}

// CD-0242 D7 retires version-only invalidation. A supersession without a
// changed depended-on digest must not require repeated artifact production.
func TestSupersessionAlonePreservesArtifact(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	const workID = "stale-supersession"
	definition := mustBuiltinDefinition(t, "workflow.break_fix").Definition
	pinWorkflowAtStep(t, s, workID, definition.Ref, "verify")
	stalenessSeedContract(t, s, workID)
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), stalenessActionPayload("repair", "record_delivery"))
	if state := foldStalenessState(t, s, workID, definition); state.ArtifactStale {
		t.Fatal("production without a staleness cause folded stale")
	}
	stalenessEvent(t, s, workID, string(WorkflowContractSuperseded), `{"new_contract_version":2}`)
	if state := foldStalenessState(t, s, workID, definition); state.ArtifactStale {
		t.Fatal("version-only successor staled the artifact without a changed dependency")
	}
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), stalenessActionPayload("repair", "record_delivery"))
	if state := foldStalenessState(t, s, workID, definition); state.ArtifactStale {
		t.Fatal("fresh delivery at the route target did not clear supersession staleness")
	}
}

// Research's investigate step produces through its recording action.
func TestResearchProducerClearsStaleness(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	research := mustBuiltinDefinition(t, "workflow.research").Definition
	if actions := workflowStepArtifactActions(research, "investigate"); len(actions) == 0 {
		t.Fatal("research investigate derives no artifact actions")
	}
	const researchWork = "stale-research"
	pinWorkflowAtStep(t, s, researchWork, research.Ref, "conclude")
	stalenessSeedContract(t, s, researchWork)
	stalenessEvent(t, s, researchWork, string(WorkflowVerdictRecorded), stalenessVerdictPayload("bad"))
	if state := foldStalenessState(t, s, researchWork, research); !state.ArtifactStale {
		t.Fatal("bad verdict at conclude did not stale the research artifact")
	}
	stalenessEvent(t, s, researchWork, string(WorkflowActionCompleted), stalenessActionPayload("investigate", "record_finding"))
	if state := foldStalenessState(t, s, researchWork, research); state.ArtifactStale {
		t.Fatal("record_finding at investigate did not clear research staleness")
	}
}

// TestStaleArtifactRefusesOkVerdictAndPremise holds both admission halves:
// while stale, record_verdict refuses a criterion-to-receipt batch, every
// entry of which associates passing proof, and confirm_premise refuses with
// the declared route as the remedy — the break_fix shape whose verify step
// confirms the premise with no forward escape past the stale artifact.
func TestStaleArtifactRefusesOkVerdictAndPremise(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	const workID = "stale-refusals"
	definition := mustBuiltinDefinition(t, "workflow.break_fix").Definition
	pinWorkflowAtStep(t, s, workID, definition.Ref, "verify")
	stalenessSeedContract(t, s, workID)
	stalenessEvent(t, s, workID, string(WorkflowVerdictRecorded), stalenessVerdictPayload("bad"))
	state := foldStalenessState(t, s, workID, definition)
	if !state.ArtifactStale {
		t.Fatal("fixture did not fold stale")
	}
	confirm := workflowAdmit(definition, state, "confirm_premise")
	if confirm.Admitted || confirm.Failure == nil || !strings.Contains(confirm.Failure.RecoveryAction, "request_correction") {
		t.Fatalf("confirm_premise while stale = %+v, want the declared-route refusal", confirm)
	}
	// The guard refuses a one-entry and a two-entry receipt batch through
	// the folded state it consumes.
	receipts := func(predicates ...string) json.RawMessage {
		entries := make([]map[string]any, 0, len(predicates))
		for _, predicate := range predicates {
			entries = append(entries, map[string]any{"predicate_id": predicate, "receipt_refs": []string{"worktree_verify:stale-refusals"}})
		}
		return mustJSONValue(map[string]any{"verdicts": entries})
	}
	guard := &workflowActionGuardContext{
		ctx: context.Background(), entry: mustBuiltinDefinition(t, "workflow.break_fix"),
		currentStep: "verify", admissionState: &state,
		request: WorkflowActionExecutionRequest{WorkID: workID, ActionID: "record_verdict", Payload: receipts("predicate:primary")},
	}
	if err := guardRecordVerdictArtifactFreshness(guard); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("receipt association while stale = %v, want the staleness refusal", err)
	}
	guard.request = WorkflowActionExecutionRequest{WorkID: workID, ActionID: "record_verdict", Payload: receipts("predicate:primary", "predicate:other")}
	if err := guardRecordVerdictArtifactFreshness(guard); err == nil {
		t.Fatal("receipt batch while stale admitted")
	}
	// After re-production the same association passes the guard: the
	// refusal is the staleness alone, not the batch shape.
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), stalenessActionPayload("repair", "record_delivery"))
	fresh := foldStalenessState(t, s, workID, definition)
	if fresh.ArtifactStale {
		t.Fatal("reproduction did not clear staleness")
	}
	guard.admissionState = &fresh
	guard.request = WorkflowActionExecutionRequest{WorkID: workID, ActionID: "record_verdict", Payload: receipts("predicate:primary")}
	if err := guardRecordVerdictArtifactFreshness(guard); err != nil {
		t.Fatalf("receipt association after reproduction refused: %v", err)
	}
}

// TestCompleteStepReturnReadsTheRouteTable pins the CD-0172 return against the
// table: implementation release returns to execution and break_fix complete to
// repair, and no other family resolves a complete-step route.
func TestCompleteStepReturnReadsTheRouteTable(t *testing.T) {
	t.Parallel()
	implementation := mustBuiltinDefinition(t, "workflow.implementation").Definition
	if target := workflowDisprovedPremiseAtCompleteRouteTarget(implementation, "release"); target != "execution" {
		t.Fatalf("implementation release complete-step target = %q, want execution", target)
	}
	breakFix := mustBuiltinDefinition(t, "workflow.break_fix").Definition
	if target := workflowDisprovedPremiseAtCompleteRouteTarget(breakFix, "complete"); target != "repair" {
		t.Fatalf("break_fix complete-step target = %q, want repair", target)
	}
	research := mustBuiltinDefinition(t, "workflow.research").Definition
	if target := workflowDisprovedPremiseAtCompleteRouteTarget(research, "complete"); target != "" {
		t.Fatalf("research complete-step target = %q, want none: the route is not broadened to other families", target)
	}
}

// A version-only supersession opens no unhealthy-verdict route (CD-0242 D7).
// A subsequent unhealthy judgment opens the producer's declared return.
func TestStaleOkVerdictNeedsUnhealthyVerdictForDeclaredRoute(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	const workID = "stale-ok-reopen"
	definition := mustBuiltinDefinition(t, "workflow.implementation").Definition
	pinWorkflowAtStep(t, s, workID, definition.Ref, "acceptance")
	stalenessSeedContract(t, s, workID)
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), stalenessActionPayload("execution", "record_delivery"))
	stalenessSeedDeliveryAt(t, s, workID, "execution")
	stalenessEvent(t, s, workID, string(WorkflowVerdictRecorded), stalenessVerdictPayload("ok"))
	if state := foldStalenessState(t, s, workID, definition); state.ArtifactStale {
		t.Fatal("ok verdict without a staleness cause folded stale")
	}
	stalenessEvent(t, s, workID, string(WorkflowContractSuperseded), `{"new_contract_version":2}`)
	state := foldStalenessState(t, s, workID, definition)
	if state.ArtifactStale {
		t.Fatal("version-only successor invalidated the artifact; CD-0242 D7 requires a changed depended-on digest")
	}
	if confirm := workflowAdmit(definition, state, "confirm_premise"); confirm.Admitted {
		t.Fatal("confirm_premise bypassed its other admission requirements")
	}
	if state.CorrectionRequestRecovery {
		t.Fatal("an old ok verdict opened correction without an unhealthy verdict")
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class)
		SELECT work_id,2,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class FROM workflow_contracts WHERE work_id=? AND contract_version=1;
		INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload)
		SELECT work_id,2,predicate_id,ordinal,outcome_kind,outcome_payload FROM workflow_contract_predicates WHERE work_id=? AND contract_version=1;
		UPDATE workflow_contracts SET superseded_by=2 WHERE work_id=? AND contract_version=1;
		DELETE FROM write_guard`, workID, workID, workID); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"predicate_id": "predicate:primary", "verdict_kind": "outcome_mismatch", "contract_version": 2})
	stalenessEvent(t, s, workID, WorkflowVerdictRecorded, string(payload))
	state = foldStalenessState(t, s, workID, definition)
	if !state.CorrectionRequestRecovery || state.CorrectionRequestContext == nil {
		t.Fatalf("fresh unhealthy verdict left no declared route: recovered %v context %v", state.CorrectionRequestRecovery, state.CorrectionRequestContext)
	}
}

// TestCorrectionRoutesDeriveFromNoWorkKind guards the deletion directly: no
// work-kind list or step-order heuristic may decide a recovery target.
func TestCorrectionRoutesDeriveFromNoWorkKind(t *testing.T) {
	t.Parallel()
	expectations := map[string][2]string{
		"workflow.implementation": {"acceptance", "execution"},
		"workflow.break_fix":      {"verify", "repair"},
		"workflow.research":       {"conclude", "investigate"},
	}
	for ref, want := range expectations {
		definition := mustBuiltinDefinition(t, ref).Definition
		if target := workflowUnhealthyVerdictRouteTarget(definition, want[0]); target != want[1] {
			t.Errorf("%s %s route target = %q, want %q", ref, want[0], target, want[1])
		}
	}
}

func TestProductionBindsToDeclaredRouteTarget(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	const workID = "stale-production-authority"
	definition := mustBuiltinDefinition(t, "workflow.implementation").Definition
	pinWorkflowAtStep(t, s, workID, definition.Ref, "acceptance")
	stalenessSeedContract(t, s, workID)
	stalenessEvent(t, s, workID, string(WorkflowVerdictRecorded), stalenessVerdictPayload("bad"))
	if state := foldStalenessState(t, s, workID, definition); !state.ArtifactStale {
		t.Fatal("fixture did not fold stale")
	}
	stalenessEvent(t, s, workID, WorkflowActionCompleted, stalenessActionPayload("execution", "checkpoint_context"))
	if state := foldStalenessState(t, s, workID, definition); !state.ArtifactStale {
		t.Fatal("checkpoint cleared artifact staleness")
	}
	stalenessSeedDeliveryAt(t, s, workID, "acceptance")
	if state := foldStalenessState(t, s, workID, definition); !state.ArtifactStale {
		t.Fatal("off-target accepted delivery cleared artifact staleness")
	}
	stalenessSeedDeliveryAt(t, s, workID, "execution")
	if state := foldStalenessState(t, s, workID, definition); state.ArtifactStale {
		t.Fatal("positively bound delivery at the target did not clear artifact staleness")
	}
}

// TestNonProducingCompletionsNeverClearStaleness pins the positive action
// classification at the producer: a health reading or a candidate revision —
// advance-moded completions the blocklist era counted — never re-produce the
// artifact; only the producer's own recording actions do.
func TestNonProducingCompletionsNeverClearStaleness(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	const workID = "stale-nonproducing"
	definition := mustBuiltinDefinition(t, "workflow.implementation").Definition
	pinWorkflowAtStep(t, s, workID, definition.Ref, "acceptance")
	stalenessSeedContract(t, s, workID)
	stalenessEvent(t, s, workID, string(WorkflowVerdictRecorded), stalenessVerdictPayload("bad"))
	// A health reading completed at the target step is not production.
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), stalenessActionPayload("execution", "record_health"))
	if state := foldStalenessState(t, s, workID, definition); !state.ArtifactStale {
		t.Fatal("health reading at the target cleared artifact staleness")
	}
	// A checkpoint-style completion stays outside the producing-action set.
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), stalenessActionPayload("execution", "link_successor"))
	if state := foldStalenessState(t, s, workID, definition); !state.ArtifactStale {
		t.Fatal("successor link at the target cleared artifact staleness")
	}
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), stalenessActionPayload("execution", "record_delivery"))
	if state := foldStalenessState(t, s, workID, definition); state.ArtifactStale {
		t.Fatal("delivery record at the target did not clear artifact staleness")
	}

	// Research: revising candidates is not recording findings.
	research := openTemp(t)
	defer research.Close()
	researchDefinition := mustBuiltinDefinition(t, "workflow.research").Definition
	const researchWork = "stale-nonproducing-research"
	pinWorkflowAtStep(t, research, researchWork, researchDefinition.Ref, "conclude")
	stalenessSeedContract(t, research, researchWork)
	stalenessEvent(t, research, researchWork, string(WorkflowVerdictRecorded), stalenessVerdictPayload("bad"))
	if actions := workflowStepArtifactActions(researchDefinition, "investigate"); len(actions) != 1 || actions[0] != "record_finding" {
		t.Fatalf("research investigate artifact actions = %v, want [record_finding]", actions)
	}
	stalenessEvent(t, research, researchWork, string(WorkflowActionCompleted), stalenessActionPayload("investigate", "revise_candidates"))
	if state := foldStalenessState(t, research, researchWork, researchDefinition); !state.ArtifactStale {
		t.Fatal("candidate revision at investigate cleared research staleness")
	}
	stalenessEvent(t, research, researchWork, string(WorkflowActionCompleted), stalenessActionPayload("investigate", "record_finding"))
	if state := foldStalenessState(t, research, researchWork, researchDefinition); state.ArtifactStale {
		t.Fatal("record_finding at investigate did not clear research staleness")
	}
}

// CD-0242 D7: removing contract authority does not change the artifact's
// dependencies. Admission still separately requires an active contract.
func TestSupersessionWithoutSuccessorDoesNotInventArtifactChange(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	const workID = "stale-supersede-no-successor"
	definition := mustBuiltinDefinition(t, "workflow.break_fix").Definition
	pinWorkflowAtStep(t, s, workID, definition.Ref, "verify")
	stalenessSeedContract(t, s, workID)
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), stalenessActionPayload("repair", "record_delivery"))
	stalenessEvent(t, s, workID, string(WorkflowContractSuperseded), `{}`)
	if state := foldStalenessState(t, s, workID, definition); state.ArtifactStale {
		t.Fatal("supersession without a successor invented a changed artifact dependency")
	}
	stalenessEvent(t, s, workID, string(WorkflowActionCompleted), stalenessActionPayload("repair", "record_delivery"))
	if state := foldStalenessState(t, s, workID, definition); state.ArtifactStale {
		t.Fatal("fresh production did not clear no-successor supersession staleness")
	}
}

// TestTerminalUnhealthyVerdictReturnsThroughRouteTable restores the terminal
// implementation shape through the single route owner: at release — the
// terminal step past the acceptance verdict — an unhealthy acceptance
// verdict folds the corrective return from release's own declared route,
// admits request_correction off-step, and names execution as the return
// target. The work-kind whitelist once carried this route (CD-0143) and the
// nearest-verdict heuristic once derived it (CD-0204); the declared table
// owns it now (CD-0201 D1): no graph walk may choose the target.
func TestTerminalUnhealthyVerdictReturnsThroughRouteTable(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	const workID = "stale-terminal-release"
	definition := mustBuiltinDefinition(t, "workflow.implementation").Definition
	if target := workflowUnhealthyVerdictRouteTarget(definition, "release"); target != "execution" {
		t.Fatalf("implementation release declares its own unhealthy route %q; the terminal shape needs the declared route to execution", target)
	}
	pinWorkflowAtStep(t, s, workID, definition.Ref, "release")
	stalenessSeedContract(t, s, workID)
	stalenessSeedDeliveryAt(t, s, workID, "execution")
	stalenessEvent(t, s, workID, string(WorkflowVerdictRecorded), stalenessVerdictPayload("bad"))
	state := foldStalenessState(t, s, workID, definition)
	if !state.CorrectionRequestRecovery || state.CorrectionRequestContext == nil {
		t.Fatalf("terminal unhealthy verdict left no declared route: recovered %v context %v", state.CorrectionRequestRecovery, state.CorrectionRequestContext)
	}
	if !strings.Contains(state.CorrectionRequestContext.Strategy, "execution") {
		t.Fatalf("terminal correction strategy = %q, want the served route target execution", state.CorrectionRequestContext.Strategy)
	}
	decision := workflowAdmit(definition, state, "request_correction")
	if !decision.Admitted {
		t.Fatalf("request_correction at the terminal step refused: %v", decision.Failure)
	}
	if target := workflowCorrectionReturnTarget(definition, "release"); target != "execution" {
		t.Fatalf("terminal correction return target = %q, want execution", target)
	}
}
