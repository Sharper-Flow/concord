package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// Typed diagnosis on workflow.break_fix and the derived repair/refine step
// start (CD-0198 D2/D3). The tests run against the current definition
// heads; each expectation that names the derived behavior is the suite's
// acceptance clause, and each released-pin expectation is the frozen
// boundary the suite must not move.

func diagnosisS1Actor(workID, name string) WorkflowActor {
	return WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/" + name, SessionRef: "session/" + workID + "-" + name, ActorClass: ActorAgent}
}

func diagnosisPayload(reproduction, searched, outcome string, relatedIDs []string, rootCause string) json.RawMessage {
	payload := map[string]any{"reproduction": reproduction, "searched": searched, "outcome": outcome, "root_cause": rootCause}
	if relatedIDs != nil {
		payload["related_ids"] = relatedIDs
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return encoded
}

type workflowStartObservation struct {
	Seq           int64
	EventID       string
	ActionID      string
	Epoch         int64
	ActorRef      string
	PayloadDigest string
}

func workflowCurrentStartAtStep(t *testing.T, s *Store, workID, stepID string) *workflowStartObservation {
	t.Helper()
	var start workflowStartObservation
	var raw []byte
	err := s.db.QueryRow(`SELECT latest_start_seq,latest_start_event_id,latest_start_payload,attempt_epoch FROM workflow_step_epochs WHERE work_id=? AND step_id=? AND latest_start_seq>0`, workID, stepID).Scan(&start.Seq, &start.EventID, &raw, &start.Epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		t.Fatalf("read the current step start: %v", err)
	}
	var payload workflowActionStartedPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.WorkID != workID || payload.StepID != stepID || payload.AttemptEpoch != start.Epoch {
		t.Fatalf("current start identity/epoch differs from its retained payload: %+v, %+v", start, payload)
	}
	if err := s.db.QueryRow(`SELECT actor,payload_digest FROM effect_receipts WHERE event_id=? AND effect_seq=? AND subject_type='work_item' AND subject_id=? AND kind=?`, start.EventID, start.Seq, workID, WorkflowActionStarted).Scan(&start.ActorRef, &start.PayloadDigest); err != nil {
		t.Fatalf("read the exact current-start receipt: %v", err)
	}
	wantDigest, err := effectPayloadDigest(raw)
	if err != nil || wantDigest != start.PayloadDigest || start.ActorRef != payload.ActorRef {
		t.Fatalf("current start differs from its receipt: %+v, payload=%+v, err=%v", start, payload, err)
	}
	start.ActionID = payload.ActionID
	return &start
}

func workflowStartReceiptIDs(t *testing.T, s *Store, workID string) []string {
	t.Helper()
	rows, err := s.db.Query(`SELECT event_id FROM effect_receipts WHERE subject_type='work_item' AND subject_id=? AND kind=? ORDER BY effect_seq`, workID, WorkflowActionStarted)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

func instanceExecutionStart(t *testing.T, s *Store, workID string) (string, string) {
	t.Helper()
	var startedAt, actorRef string
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(execution_started_at,''),COALESCE(execution_actor_ref,'') FROM workflow_instances WHERE work_id=?`, workID).Scan(&startedAt, &actorRef); err != nil {
		t.Fatal(err)
	}
	return startedAt, actorRef
}

func diagnosisAlignmentRows(t *testing.T, s *Store, workID string) []struct {
	Related  string
	Searched string
	Outcome  string
} {
	t.Helper()
	rows, err := s.DatabaseForTesting().Query(`SELECT COALESCE(related_work_id,''),searched,outcome FROM workflow_backlog_alignment WHERE work_id=? ORDER BY related_work_id`, workID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var found []struct {
		Related  string
		Searched string
		Outcome  string
	}
	for rows.Next() {
		var row struct {
			Related  string
			Searched string
			Outcome  string
		}
		if err := rows.Scan(&row.Related, &row.Searched, &row.Outcome); err != nil {
			t.Fatal(err)
		}
		found = append(found, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return found
}

// TestBreakFixDiagnosisStepShapeIsTheMergedTypedStep proves the CD-0198 D2
// graph: the current break_fix definition folds reproduce, alignment, and
// diagnose into one diagnosis step whose only advance exit is
// record_diagnosis, and the derived-start rule removes the callable
// start_repair/start_refine entries from both current heads while
// implementation keeps its alignment step.
func TestBreakFixDiagnosisStepShapeIsTheMergedTypedStep(t *testing.T) {
	t.Parallel()
	breakFix, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	definition := breakFix.Definition
	if definition.StepGraph.StartStep != "diagnosis" {
		t.Fatalf("break_fix start step = %q, want diagnosis", definition.StepGraph.StartStep)
	}
	for _, retired := range []string{"reproduce", "alignment", "diagnose"} {
		if workflowStep(definition, retired) != nil {
			t.Fatalf("break_fix still declares the %q step; CD-0198 D2 folds it into diagnosis", retired)
		}
	}
	if workflowStep(definition, "planning") == nil || workflowStep(definition, "repair") == nil || workflowStep(definition, "verify") == nil {
		t.Fatal("break_fix diagnosis graph lost planning, repair, or verify")
	}
	exits := alignmentAdvanceExits(t, definition, "diagnosis")
	if len(exits) != 1 || exits[0] != "record_diagnosis" {
		t.Fatalf("break_fix diagnosis advance exits are %v, want exactly [record_diagnosis]", exits)
	}
	for _, withheld := range []string{"record_reproduction", "record_alignment", "record_root_cause", "dispatch_worker", "record_worker_failure", "accept_worker_result"} {
		if definitionStepAllows(definition, "diagnosis", withheld) {
			t.Fatalf("break_fix diagnosis admits %s", withheld)
		}
	}
	if !definitionStepAllows(definition, "diagnosis", "checkpoint_context") || !definitionStepAllows(definition, "diagnosis", "cross_context_boundary") {
		t.Fatal("break_fix diagnosis lost the continuity holds")
	}
	for _, step := range definition.StepGraph.Steps {
		if slices.Contains(step.Actions, "start_repair") || slices.Contains(step.Actions, "start_refine") {
			t.Fatalf("break_fix step %q still declares a callable start; CD-0198 D3 derives it", step.ID)
		}
	}
	if !slices.Contains(definition.AvailableActions, "record_diagnosis") {
		t.Fatal("break_fix diagnosis does not advertise record_diagnosis")
	}
	for _, retired := range []string{"record_reproduction", "record_alignment", "record_root_cause", "start_repair", "start_refine"} {
		if slices.Contains(definition.AvailableActions, retired) {
			t.Fatalf("break_fix still advertises %s", retired)
		}
	}

	implementation, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	implDefinition := implementation.Definition
	if got := workflowNextStep(implDefinition, "proposal"); got != "alignment" {
		t.Fatalf("implementation routes proposal to %q, want alignment (record_alignment survives on implementation)", got)
	}
	if exits := alignmentAdvanceExits(t, implDefinition, "alignment"); len(exits) != 1 || exits[0] != "record_alignment" {
		t.Fatalf("implementation alignment advance exits are %v, want exactly [record_alignment]", exits)
	}
	if slices.Contains(implDefinition.AvailableActions, "start_refine") {
		t.Fatal("implementation still advertises start_refine; CD-0198 D3 derives the refine start")
	}
	for _, step := range implDefinition.StepGraph.Steps {
		if slices.Contains(step.Actions, "start_refine") {
			t.Fatalf("implementation step %q still declares start_refine", step.ID)
		}
	}
	if !slices.Contains(implDefinition.AvailableActions, "start_execution") {
		t.Fatal("implementation lost start_execution; CD-0198 D3 keeps it callable")
	}
}

// TestRecordDiagnosisRecordsAllSectionsAtomically proves the merged record:
// one valid call advances diagnosis with every obligation recorded — the
// alignment projection rows the CD-0156 projection fold writes, and no
// automatic relation — while a partial section leaves zero writes.
func TestRecordDiagnosisRecordsAllSectionsAtomically(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	const workID = "diagnosis-recorded-atomic"
	const relatedID = "diagnosis-related-work"
	seedWork(t, s, workID)
	seedWork(t, s, relatedID)
	seedWorkflowLaw(t, s)
	seedIssue31DomainRegistry(t, s)
	actor := diagnosisS1Actor(workID, "owner")
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := s.EstablishKnowledgeSourceSetProof(context.Background(), workID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: actor, Now: time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	version := issue31WorkflowActionWithPayload(t, s, workID, readWorkVersion(t, s, workID), "record_diagnosis", "diagnosis-record-"+workID, actor,
		diagnosisPayload("the defect reproduces from a clean store", "the backlog search covered the tracker", "related_found", []string{relatedID}, "the defect is the fold's pool"))

	if step := currentStep(t, s, workID); step != "planning" {
		t.Fatalf("step after record_diagnosis = %q, want planning", step)
	}
	rows := diagnosisAlignmentRows(t, s, workID)
	if len(rows) != 2 {
		t.Fatalf("alignment rows after record_diagnosis = %d, want the declaration row plus the related row", len(rows))
	}
	for _, row := range rows {
		if row.Searched != "the backlog search covered the tracker" || row.Outcome != "related_found" {
			t.Fatalf("alignment row = %+v, want the recorded search and outcome", row)
		}
	}
	declaration, related := rows[0], rows[1]
	if declaration.Related != "" {
		t.Fatalf("declaration row names related %q, want the NULL related declaration row", declaration.Related)
	}
	if related.Related != relatedID {
		t.Fatalf("related row names %q, want %q", related.Related, relatedID)
	}
	var diagnosisEvents int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, workID, "workflow.diagnosis_recorded").Scan(&diagnosisEvents); err != nil {
		t.Fatal(err)
	}
	if diagnosisEvents != 1 {
		t.Fatalf("workflow.diagnosis_recorded events = %d, want 1", diagnosisEvents)
	}
	if version <= readWorkVersion(t, s, workID)-1 {
		t.Fatal("record_diagnosis recorded no work version")
	}

	// The corrected projection is a record, not a relation: the merged
	// action creates no relation rows (CD-0198 D2 keeps CD-0156 D5).
	var relations int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM relations WHERE work_id_from=? OR work_id_to=?`, workID, workID).Scan(&relations); err != nil {
		t.Fatal(err)
	}
	if relations != 0 {
		t.Fatalf("record_diagnosis created %d relation rows, want none", relations)
	}
}

// TestRecordDiagnosisRefusalsLeaveZeroWrites proves every malformed section
// refuses the whole call before any write lands: no event, no projection
// row, no step advance.
func TestRecordDiagnosisRefusalsLeaveZeroWrites(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	const workID = "diagnosis-refusal-zero-writes"
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedIssue31DomainRegistry(t, s)
	actor := diagnosisS1Actor(workID, "owner")
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := s.EstablishKnowledgeSourceSetProof(context.Background(), workID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: actor, Now: time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)

	cases := []struct {
		name    string
		payload json.RawMessage
	}{
		{"missing reproduction", json.RawMessage(`{"searched":"the search","outcome":"none_found","root_cause":"the cause"}`)},
		{"missing searched", json.RawMessage(`{"reproduction":"the reproduction","outcome":"none_found","root_cause":"the cause"}`)},
		{"missing outcome", json.RawMessage(`{"reproduction":"the reproduction","searched":"the search","root_cause":"the cause"}`)},
		{"missing root cause", json.RawMessage(`{"reproduction":"the reproduction","searched":"the search","outcome":"none_found"}`)},
		{"related_found without ids", json.RawMessage(`{"reproduction":"the reproduction","searched":"the search","outcome":"related_found","root_cause":"the cause"}`)},
		{"none_found with ids", json.RawMessage(`{"reproduction":"the reproduction","searched":"the search","outcome":"none_found","related_ids":["work:diagnosis-refusal-zero-writes"],"root_cause":"the cause"}`)},
		{"unknown related id", json.RawMessage(`{"reproduction":"the reproduction","searched":"the search","outcome":"related_found","related_ids":["work:not-a-real-work-item"],"root_cause":"the cause"}`)},
		{"unknown field", json.RawMessage(`{"reproduction":"the reproduction","searched":"the search","outcome":"none_found","root_cause":"the cause","severity":"high"}`)},
	}
	for _, testCase := range cases {
		if err := runVerdictActionAs(t, s, workID, "record_diagnosis", testCase.payload, 0, actor); err == nil {
			t.Fatalf("%s: record_diagnosis admitted a malformed payload", testCase.name)
		}
	}
	if got := readWorkVersion(t, s, workID); got != version {
		t.Fatalf("refused record_diagnosis calls moved the work version %d -> %d", version, got)
	}
	if rows := diagnosisAlignmentRows(t, s, workID); len(rows) != 0 {
		t.Fatalf("refused record_diagnosis calls wrote %d alignment rows", len(rows))
	}
	var diagnosisEvents int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, workID, "workflow.diagnosis_recorded").Scan(&diagnosisEvents); err != nil {
		t.Fatal(err)
	}
	if diagnosisEvents != 0 {
		t.Fatalf("refused record_diagnosis calls wrote %d events", diagnosisEvents)
	}
	if step := currentStep(t, s, workID); step != "diagnosis" {
		t.Fatalf("step after refusals = %q, want diagnosis", step)
	}

	// The valid call still admits after the refusals, which proves the
	// refusals left nothing partial behind.
	if err := runVerdictActionAs(t, s, workID, "record_diagnosis", diagnosisPayload("the reproduction", "the search", "none_found", nil, "the cause"), 0, actor); err != nil {
		t.Fatalf("valid record_diagnosis after refusals: %v", err)
	}
	if step := currentStep(t, s, workID); step != "planning" {
		t.Fatalf("step after the valid record_diagnosis = %q, want planning", step)
	}
}

// TestDerivedRepairStartClaimsEpochFromFirstQualifyingCheckpoint proves the
// CD-0198 D3 derivation on the repair step: the first admitted qualifying
// action fences the step from its own actor, opens epoch 1, and the step's
// direct-work route stays live behind it.
func TestDerivedRepairStartClaimsEpochFromFirstQualifyingCheckpoint(t *testing.T) {
	t.Parallel()
	const workID = "derived-repair-start"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	if startedAt, _ := instanceExecutionStart(t, s, workID); startedAt != "" {
		t.Fatalf("repair step pre-start execution_started_at = %q, want empty", startedAt)
	}
	checkpointer := diagnosisS1Actor(workID, "checkpointer")
	if err := runVerdictActionAs(t, s, workID, "checkpoint_repair", json.RawMessage(`{}`), 0, checkpointer); err != nil {
		t.Fatalf("the first qualifying checkpoint derives the repair start: %v", err)
	}
	start := workflowCurrentStartAtStep(t, s, workID, "repair")
	if start == nil || len(workflowStartReceiptIDs(t, s, workID)) != 1 {
		t.Fatalf("repair current start = %+v, want one receipted derived start", start)
	}
	if start.ActionID != "checkpoint_repair" || start.Epoch != 1 {
		t.Fatalf("derived start = action %q epoch %d, want checkpoint_repair epoch 1", start.ActionID, start.Epoch)
	}
	checkpointerRef, err := WorkflowActorRef(checkpointer)
	if err != nil {
		t.Fatal(err)
	}
	if start.ActorRef != checkpointerRef {
		t.Fatalf("derived start actor = %q, want the first qualifying action's own actor %q", start.ActorRef, checkpointerRef)
	}
	startedAt, executionActor := instanceExecutionStart(t, s, workID)
	if startedAt == "" {
		t.Fatal("the derived start recorded no execution_started_at")
	}
	if executionActor != checkpointerRef {
		t.Fatalf("execution actor fence = %q, want %q", executionActor, checkpointerRef)
	}
	// The derived start admits the step's ordinary route: the repair
	// step's delivery advances toward the refinement pass, which owns the
	// refine proof behind its own derived start.
	proveDerivedCheckpointDelivery(t, fixture, workID)
	if err := runVerdictActionAs(t, s, workID, "record_delivery", json.RawMessage(`{"delivery_artifact":"artifact:derived-repair-`+workID+`","delivery_state":"asserted"}`), 0, fixture.owner); err != nil {
		t.Fatalf("record_delivery behind the derived start: %v", err)
	}
	if step := currentStep(t, s, workID); step != "refine" {
		t.Fatalf("step after repair delivery = %q, want refine", step)
	}
}

// TestDerivedRefineStartClaimsEpochFromCheckpointRefine proves the same
// derivation on the refine step: a passing report on the Reviewed candidate
// satisfies the proof, and the delivery exits through the declared route.
func TestDerivedRefineStartClaimsEpochFromCheckpointRefine(t *testing.T) {
	t.Parallel()
	for _, family := range []struct{ ref string }{
		{"workflow.implementation"}, {"workflow.break_fix"},
	} {
		const workID = "derived-refine-start"
		fixture := seedWorkflowReturnRouteFixture(t, workID, family.ref, "refine")
		s := fixture.store
		entry, err := VerifyWorkflowInstanceDefinition(context.Background(), s, BuiltinWorkflowRegistry(), workID)
		if err != nil {
			t.Fatal(err)
		}
		if !workflowRefineProofGateActive(entry.Definition, "refine") {
			t.Fatalf("%s does not gate the refine proof at refine", family.ref)
		}
		checkpointer := diagnosisS1Actor(workID, "checkpointer")
		if err := runVerdictActionAs(t, s, workID, "checkpoint_refine", json.RawMessage(`{}`), 0, checkpointer); err != nil {
			t.Fatalf("%s: checkpoint_refine derives the refine start: %v", family.ref, err)
		}
		start := workflowCurrentStartAtStep(t, s, workID, "refine")
		if start == nil || len(workflowStartReceiptIDs(t, s, workID)) != 1 || start.ActionID != "checkpoint_refine" || start.Epoch != 1 {
			t.Fatalf("%s: refine current start = %+v, want one checkpoint_refine start at epoch 1", family.ref, start)
		}
		proveDerivedCheckpointDelivery(t, fixture, workID)
		if err := runVerdictActionAs(t, s, workID, "record_delivery", json.RawMessage(`{"delivery_artifact":"artifact:derived-refine-`+workID+`","delivery_state":"asserted"}`), 0, fixture.owner); err != nil {
			t.Fatalf("%s: record_delivery behind the derived refine start: %v", family.ref, err)
		}
		if step := currentStep(t, s, workID); step != "delivery" {
			t.Fatalf("%s: step after refine delivery = %q, want delivery", family.ref, step)
		}
	}
}

// TestNonQualifyingActionsCannotStartTheRepairStep proves the qualifying
// boundary: context, evidence, and job records neither manufacture nor reset
// the start, and delivery without any qualifying start refuses.
func TestNonQualifyingActionsCannotStartTheRepairStep(t *testing.T) {
	t.Parallel()
	const workID = "derived-start-non-qualifying"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "refine")
	s := fixture.store
	actor := fixture.owner
	j := pReturnClaim(t, fixture, workID)
	acceptCurrentReviewForTest(t, j.fixture, "ready_for_final_proof")
	setWorkflowStepForOperatorVerdictTest(t, s, workID, "repair")
	receiptsBefore := workflowStartReceiptIDs(t, s, workID)

	contextPayload, err := json.Marshal(map[string]any{
		"required_reading": []map[string]any{workContextRepoReading("root", "the reading is product-wide")},
		"finding_refs":     []string{},
		"context_findings": []map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runVerdictActionAs(t, s, workID, "record_work_context", contextPayload, 0, actor); err != nil {
		t.Fatalf("record_work_context at the unstarted repair step: %v", err)
	}
	if err := runVerdictActionAs(t, s, workID, "record_work_context", contextPayload, 0, actor); err != nil {
		t.Fatalf("record_work_context at the unstarted repair step: %v", err)
	}
	err = runVerdictActionAs(t, s, workID, "record_delivery", json.RawMessage(`{"delivery_artifact":"artifact:no-start-`+workID+`","delivery_state":"asserted"}`), 0, actor)
	var failure *Failure
	if !errors.As(err, &failure) || !strings.Contains(failure.Detail, "fenced start") {
		t.Fatalf("record_delivery without any start = %v, want the missing-start refusal", err)
	}
	// The continuity hold stays behind the start at the fenced step, so no
	// checkpoint-shaped side door starts the step either.
	if err := runVerdictActionAs(t, s, workID, "checkpoint_context", json.RawMessage(`{}`), 0, actor); err == nil {
		t.Fatal("checkpoint_context admitted at an unstarted fenced repair step")
	}
	if start := workflowCurrentStartAtStep(t, s, workID, "repair"); start != nil || !slices.Equal(receiptsBefore, workflowStartReceiptIDs(t, s, workID)) {
		t.Fatalf("a refused repair-step action left a current start or new receipt: %+v", start)
	}

	// The qualifying checkpoint still derives cleanly afterwards.
	if err := runVerdictActionAs(t, s, workID, "checkpoint_repair", json.RawMessage(`{}`), 0, actor); err != nil {
		t.Fatalf("checkpoint_repair after the non-qualifying actions: %v", err)
	}
	start := workflowCurrentStartAtStep(t, s, workID, "repair")
	receiptsAfter := workflowStartReceiptIDs(t, s, workID)
	if start == nil || start.Epoch != 1 || len(receiptsAfter) != len(receiptsBefore)+1 || !slices.Equal(receiptsAfter[:len(receiptsBefore)], receiptsBefore) || receiptsAfter[len(receiptsAfter)-1] != start.EventID {
		t.Fatalf("repair current start after the non-qualifying actions = %+v, want epoch 1 and exactly its receipt", start)
	}
}

// TestRepeatedQualifyingActionsDoNotResetTheStartCutoff proves the epoch and
// cutoff stay fixed: the store admits one checkpoint identity per step epoch,
// so the repeat shape is further activity at the started step — a context
// checkpoint, a fresh qualifying read — and delivery still exits afterwards.
func TestRepeatedQualifyingActionsDoNotResetTheStartCutoff(t *testing.T) {
	t.Parallel()
	const workID = "derived-start-repeat"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "refine")
	s := fixture.store
	actor := fixture.owner
	if err := runVerdictActionAs(t, s, workID, "checkpoint_refine", json.RawMessage(`{}`), 0, actor); err != nil {
		t.Fatalf("first checkpoint_refine: %v", err)
	}
	firstStart := workflowCurrentStartAtStep(t, s, workID, "refine")
	firstReceipts := workflowStartReceiptIDs(t, s, workID)
	firstStartedAt, firstActor := instanceExecutionStart(t, s, workID)
	contextCheckpoint := json.RawMessage(`{"active_unit":"the refine unit","hypothesis":"the refined subject holds","diagnosis":"the review found no defect","strategy":"deliver the refined work","touched_refs":["internal/store/workflow.go"],"evidence_refs":["evidence:repeat-context"],"pending_questions":[],"pending_decisions":[]}`)
	if err := runWorkflowActionWithIdentity(t, s, workID, "checkpoint_context", contextCheckpoint, 0, actor, "repeat-context"); err != nil {
		t.Fatalf("context checkpoint behind the derived start: %v", err)
	}
	if start := workflowCurrentStartAtStep(t, s, workID, "refine"); start == nil || firstStart == nil || *start != *firstStart || !slices.Equal(firstReceipts, workflowStartReceiptIDs(t, s, workID)) {
		t.Fatalf("step activity changed the current start or its receipts: %+v -> %+v", firstStart, start)
	}
	startedAt, actorRef := instanceExecutionStart(t, s, workID)
	if startedAt == "" || actorRef == "" || startedAt != firstStartedAt || actorRef != firstActor {
		t.Fatal("step activity changed or lost the instance's execution time/actor fence")
	}
	proveDerivedCheckpointDelivery(t, fixture, workID)
	if err := runVerdictActionAs(t, s, workID, "record_delivery", json.RawMessage(`{"delivery_artifact":"artifact:repeat-`+workID+`","delivery_state":"asserted"}`), 0, actor); err != nil {
		t.Fatalf("record_delivery after the further step activity: %v", err)
	}
}

// TestRefusedQualifyingActionLeavesNoStart proves the derivation is atomic
// with the action: a refused qualifying checkpoint opens no start, and the
// next admitted one derives from clean state.
func TestRefusedQualifyingActionLeavesNoStart(t *testing.T) {
	t.Parallel()
	const workID = "derived-start-refused"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	actor := fixture.owner
	version := readWorkVersion(t, s, workID)
	if err := runVerdictActionAs(t, s, workID, "checkpoint_repair", json.RawMessage(`{"unknown_field":true}`), 0, actor); err == nil {
		t.Fatal("checkpoint_repair admitted an unknown payload field")
	}
	if got := readWorkVersion(t, s, workID); got != version {
		t.Fatalf("refused checkpoint_repair moved the work version %d -> %d", version, got)
	}
	if start := workflowCurrentStartAtStep(t, s, workID, "repair"); start != nil || len(workflowStartReceiptIDs(t, s, workID)) != 0 {
		t.Fatalf("the refused qualifying action left a current start or receipt: %+v", start)
	}
	if startedAt, _ := instanceExecutionStart(t, s, workID); startedAt != "" {
		t.Fatalf("the refused qualifying action set execution_started_at = %q", startedAt)
	}
	if err := runVerdictActionAs(t, s, workID, "checkpoint_repair", json.RawMessage(`{}`), 0, actor); err != nil {
		t.Fatalf("checkpoint_repair after the refusal: %v", err)
	}
	if start := workflowCurrentStartAtStep(t, s, workID, "repair"); start == nil || start.Epoch != 1 || len(workflowStartReceiptIDs(t, s, workID)) != 1 {
		t.Fatalf("repair current start after the refusal = %+v, want one receipt at epoch 1", start)
	}
}

// TestConcurrentFirstQualifyingActionsYieldOneFence proves the first-start
// rule under contention: two qualifying checkpoints at one step yield one
// actor fence and one epoch, and the loser refuses without writing.
func TestConcurrentFirstQualifyingActionsYieldOneFence(t *testing.T) {
	t.Parallel()
	const workID = "derived-start-concurrent"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	first := diagnosisS1Actor(workID, "first")
	second := diagnosisS1Actor(workID, "second")
	version := readWorkVersion(t, s, workID)
	if err := runVerdictActionAtVersion(t, s, workID, "checkpoint_repair", json.RawMessage(`{}`), version, first); err != nil {
		t.Fatalf("first concurrent checkpoint_repair: %v", err)
	}
	if err := runVerdictActionAtVersion(t, s, workID, "checkpoint_repair", json.RawMessage(`{}`), version, second); err == nil {
		t.Fatal("the second concurrent checkpoint_repair admitted at the same expected version")
	}
	start := workflowCurrentStartAtStep(t, s, workID, "repair")
	if start == nil || len(workflowStartReceiptIDs(t, s, workID)) != 1 {
		t.Fatalf("concurrent first qualifying actions left current start %+v, want one receipt", start)
	}
	firstRef, err := WorkflowActorRef(first)
	if err != nil {
		t.Fatal(err)
	}
	secondRef, err := WorkflowActorRef(second)
	if err != nil {
		t.Fatal(err)
	}
	if start.ActorRef != firstRef {
		t.Fatalf("the fence names %q, want the first actor %q", start.ActorRef, firstRef)
	}
	if _, executionActor := instanceExecutionStart(t, s, workID); executionActor != firstRef || executionActor == secondRef {
		t.Fatalf("execution actor fence = %q, want the first actor only", executionActor)
	}
}

// TestCorrectedContractLivenessThroughProducerReturn proves the producer
// absorb: after the declared producer return of a complete-step contract
// correction, the corrected contract is live — source-qualified context, a
// derived repair start, fresh check evidence, and the successor's verdict
// reach completion — while the pre-correction bindings satisfy nothing.
func TestCorrectedContractLivenessThroughProducerReturn(t *testing.T) {
	t.Parallel()
	const workID = "corrected-contract-liveness"
	fixture, _ := seedCompleteStepCorrection(t, workID, "workflow.break_fix", "verify", "complete")
	s := fixture.store
	current := workContextFixture{store: s, workID: workID, owner: fixture.owner, operator: fixture.operator}
	journey := &candidateJourney{fixture: current, s: s, workID: workID, repo: reviewFixtureDirectory(t, s, workID), acceptor: fixture.owner}
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", completeStepSuccessorPayload(workID), fixture.owner); err != nil {
		t.Fatalf("supersede the contract at the complete step: %v", err)
	}
	if step := currentStep(t, s, workID); step != "repair" {
		t.Fatalf("instance after the declared producer return = %q, want repair", step)
	}

	// The stale half: the successor's own gate refuses a completion built on
	// pre-correction facts — the ordinary stale recovery stays unchanged.
	if err := pConfirmReceiptPremise(t, current); err == nil {
		t.Fatal("confirm_premise admitted behind the successor's outstanding fresh evidence")
	}

	// Source-qualified context: the declared reading binds an original
	// source commit the correction returned to.
	const sourceProject = "liveness-source"
	var productVersion int64
	if err := s.db.QueryRow(`SELECT version FROM products WHERE id='product'`).Scan(&productVersion); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(t.Context(), s, Operation{Events: []Event{
		projectCreatedEvent(sourceProject, "create-"+sourceProject),
		operationEvent("add-"+sourceProject, "product_project.added", SubjectProduct, "product", map[string]any{
			"product_id": "product", "project_id": sourceProject, "role": "secondary", "reason": "the corrected contract's pinned reading",
			"expected_version": productVersion, "resulting_version": productVersion + 1,
		}),
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProject, sourceProject): 0, VersionRef(SubjectProduct, "product"): productVersion}}); err != nil {
		t.Fatal(err)
	}
	sourceRepo := initKnowledgeRepo(t)
	writeKnowledgeFile(t, sourceRepo, "internal/store/work_context.go", "package synthetic\n")
	sourceCommit := commitKnowledgeRepo(t, sourceRepo, "source-qualified liveness reading")
	if err := s.AddProjectLocator(t.Context(), sourceProject, ProjectLocator{ID: "liveness-source-repo", Kind: LocatorCanonicalPath, Value: sourceRepo}, 1); err != nil {
		t.Fatal(err)
	}
	reading := workContextRepoReading("root", "the reading is product-wide")
	reading["source"] = map[string]any{"kind": "repository_file", "project_id": sourceProject, "path": "internal/store/work_context.go", "commit_oid": sourceCommit}
	contextPayload, err := json.Marshal(map[string]any{
		"required_reading": []map[string]any{reading},
		"finding_refs":     []string{},
		"context_findings": []map[string]any{{"kind": "direction", "statement": "the corrected contract repairs the returned subject", "subject_ref": "internal/store/workflow.go", "evidence_refs": []string{"internal/store/workflow.go"}, "domain_id": "root", "product_wide_rationale": "the finding is product-wide"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runVerdictActionAs(t, s, workID, "record_work_context", contextPayload, 0, fixture.owner); err != nil {
		t.Fatalf("source-qualified work context at the returned repair step: %v", err)
	}
	if err := runVerdictActionAs(t, s, workID, "checkpoint_repair", json.RawMessage(`{}`), 0, fixture.owner); err != nil {
		t.Fatalf("the derived repair start after the producer return: %v", err)
	}
	bindKind := func(kind, ref string) {
		t.Helper()
		authority := "liveness-authority-" + kind + "-" + workID
		seedWorkflowAuthority(t, s, authority, workID, "principal:liveness-"+kind, "request/liveness-"+kind, []string{ref})
		payload := json.RawMessage(`{"evidence_ref":"evidence:liveness-` + kind + `-` + workID + `","evidence_kind":"` + kind + `","immutable_subject_ref":"` + ref + `","producer_id":"principal:liveness-` + kind + `","producer_run_ref":"` + authority + `","producer_watermark":"request/liveness-` + kind + `"}`)
		if err := runVerdictActionAs(t, s, workID, "bind_evidence", payload, 0, fixture.owner); err != nil {
			t.Fatalf("bind fresh %s evidence: %v", kind, err)
		}
	}
	bindKind("review", "review:liveness-"+workID)
	bindKind("artifact", "artifact:liveness-"+workID)
	proof := journey.observeRun(t, "liveness-current")
	qAcceptCriterionReview(t, current, proof.OperationRef)
	journey.bindVerification(t, proof)
	if err := runVerdictActionAs(t, s, workID, "record_delivery", json.RawMessage(`{"delivery_artifact":"artifact:liveness-repair-`+workID+`","delivery_state":"asserted"}`), 0, fixture.owner); err != nil {
		t.Fatalf("record_delivery on the corrected contract: %v", err)
	}
	if step := currentStep(t, s, workID); step != "refine" {
		t.Fatalf("step after the repaired delivery = %q, want refine", step)
	}
	// The unchanged candidate keeps its verification through the refine pass.
	if err := runVerdictActionAs(t, s, workID, "checkpoint_refine", json.RawMessage(`{}`), 0, fixture.owner); err != nil {
		t.Fatalf("the derived refine start on the corrected contract: %v", err)
	}
	journey.bindVerification(t, proof)
	if err := runVerdictActionAs(t, s, workID, "record_delivery", json.RawMessage(`{"delivery_artifact":"artifact:liveness-refine-`+workID+`","delivery_state":"asserted"}`), 0, fixture.owner); err != nil {
		t.Fatalf("record_delivery at the refine step on the corrected contract: %v", err)
	}
	if step := currentStep(t, s, workID); step != "delivery" {
		t.Fatalf("step after the refine delivery = %q, want the delivery gate", step)
	}
	if err := runVerdictActionAs(t, s, workID, "record_delivery", json.RawMessage(`{"delivery_artifact":"artifact:liveness-gate-`+workID+`","delivery_state":"asserted"}`), 0, fixture.owner); err != nil {
		t.Fatalf("record_delivery through the delivery gate on the corrected contract: %v", err)
	}
	reviewer := diagnosisS1Actor(workID, "liveness-reviewer")
	if err := runVerdictActionAs(t, s, workID, "record_verdict", criterionAssociationPayload(proof.OperationRef, "predicate:return-route"), 0, reviewer); err != nil {
		t.Fatalf("fresh verdict on the corrected contract: %v", err)
	}
	if err := pConfirmReceiptPremise(t, current); err != nil {
		t.Fatalf("confirm the corrected premise: %v", err)
	}
	if step := currentStep(t, s, workID); step != "complete" {
		t.Fatalf("step after the corrected premise = %q, want complete", step)
	}
	bindCompletionClosure(t, s, workID, fixture.owner)
	// The completing coordinator is neither the step's fenced executor nor
	// the verdict actor (CD-0013 D5).
	completer := diagnosisS1Actor(workID, "completer")
	completePayload := json.RawMessage(`{"impact_verdict":"non-breaking"}`)
	if err := runVerdictActionAs(t, s, workID, "complete", completePayload, 0, completer); err != nil {
		t.Fatalf("complete the corrected contract: %v", err)
	}
	var lifecycle string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, workID).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "completed" {
		t.Fatalf("lifecycle after completing the corrected contract = %q, want completed", lifecycle)
	}
}

func TestDiagnosisRecordAdvancesWithoutExecutionStart(t *testing.T) {
	t.Parallel()
	const workID = "diagnosis-lane-cannot-strand"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "diagnosis")
	s := fixture.store
	actor := fixture.owner
	if start := workflowCurrentStartAtStep(t, s, workID, "diagnosis"); start != nil || len(workflowStartReceiptIDs(t, s, workID)) != 0 {
		t.Fatalf("diagnosis has an execution start or receipt: %+v", start)
	}
	if rows := diagnosisAlignmentRows(t, s, workID); len(rows) != 0 {
		t.Fatalf("the refused lane actions wrote %d alignment rows", len(rows))
	}

	if err := runVerdictActionAs(t, s, workID, "record_diagnosis", diagnosisPayload("the reproduction", "the search", "none_found", nil, "the cause"), 0, actor); err != nil {
		t.Fatalf("the valid merged record behind the refused lane: %v", err)
	}
	if step := currentStep(t, s, workID); step != "planning" {
		t.Fatalf("step after the valid record = %q, want planning", step)
	}
}

func proveDerivedCheckpointDelivery(t *testing.T, f workflowReturnRouteFixture, workID string) {
	t.Helper()
	j := pReturnClaim(t, f, workID)
	j.acceptor = f.owner
	proof := j.observeRun(t, "derived-checkpoint")
	j.bindVerification(t, proof)
	qAcceptCriterionReview(t, j.fixture, proof.OperationRef)
}

func runVerdictActionAtVersion(t *testing.T, s *Store, workID, action string, payload json.RawMessage, version int64, owner WorkflowActor) error {
	t.Helper()
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := applyWorkflowActionRawTx(context.Background(), tx, newWriteScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		EvidenceRefs: actionEvidenceRefs(action, payload), WorkID: workID, ExpectedVersion: version, ActionID: action, Payload: payload, Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("c", 64) + fmt.Sprint(version), IdempotencyIdentity: action + "-" + workID + "-" + fmt.Sprint(version), OperationID: action + "-" + workID + "-" + fmt.Sprint(version),
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: action + "-" + workID + "-" + fmt.Sprint(version), RequestID: "request:" + action + "-" + workID + "-" + fmt.Sprint(version), ContractDigest: testManifestDigest, Now: time.Unix(11, 0).UTC(),
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// runWorkflowActionWithIdentity runs one action under a distinct operation
// identity, so a repeated action on one version mints its own durable
// operation instead of colliding with the first call's identity.
func runWorkflowActionWithIdentity(t *testing.T, s *Store, workID, action string, payload json.RawMessage, version int64, owner WorkflowActor, suffix string) error {
	t.Helper()
	if version == 0 {
		version = readWorkVersion(t, s, workID)
	}
	operation := action + "-" + suffix + "-" + workID + "-" + fmt.Sprint(version)
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := applyWorkflowActionRawTx(context.Background(), tx, newWriteScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		EvidenceRefs: actionEvidenceRefs(action, payload), WorkID: workID, ExpectedVersion: version, ActionID: action, Payload: payload, Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("b", 64) + operation, IdempotencyIdentity: operation, OperationID: operation,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operation, RequestID: "request:" + operation, ContractDigest: testManifestDigest, Now: time.Unix(13, 0).UTC(),
	}); err != nil {
		return err
	}
	return tx.Commit()
}
