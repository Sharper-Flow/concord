package store

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func TestWorkflowCorrectionAdmissionRejectsNBSPReference(t *testing.T) {
	t.Parallel()
	const legacyReference = "evidence:\u00a0legacy"
	if fault := workflowCorrectionSchemaValuesFaultForAdmission(nil, []string{legacyReference}, nil); fault == "" {
		t.Fatal("new correction admission accepted a non-breaking-space reference")
	}
}

func TestCorrectionRequestPayloadRegistration(t *testing.T) {
	completed, ok := registeredEventKind(WorkflowActionCompleted)
	if !ok || completed.CurrentVersion != 8 {
		t.Fatalf("action_completed registration = %#v, want v8", completed)
	}
}

// Late criterion association agrees across discovery, the work pin, preflight
// and mutation. It closes when every criterion has applicable proof.
func TestIssue1013LateVerdictRecoveryPassesReadOnlyPreflight(t *testing.T) {
	t.Parallel()
	const workID = "issue1013-late-verdict-preflight"
	fixture, ref := seedLateCriterionAssociation(t, workID)
	s, owner := fixture.store, fixture.owner
	for _, predicate := range []string{"predicate:first", "predicate:second"} {
		payload := criterionAssociationPayload(ref, predicate)
		if !issue1013HasIntent(issue1013Pin(t, s, workID), "record_verdict") {
			t.Fatal("missing criterion proof has no late-association intent")
		}
		_, action, err := WorkflowActionDefinitionFor(t.Context(), s, BuiltinWorkflowRegistry(), workID, "record_verdict")
		if err != nil || action.ExecutionMode != ActionHold {
			t.Fatalf("late association discovery=%+v err=%v", action, err)
		}
		before := contractCorrectionEffectSnapshot(t, s, workID)
		if err := issue1013Preflight(t, s, workID, "record_verdict", payload, owner); err != nil {
			t.Fatalf("late association preflight: %v", err)
		}
		if after := contractCorrectionEffectSnapshot(t, s, workID); after != before {
			t.Fatal("read-only late association preflight changed state")
		}
		if err := runVerdictActionAs(t, s, workID, "record_verdict", payload, 0, owner); err != nil {
			t.Fatalf("late association: %v", err)
		}
		if got := currentStep(t, s, workID); got != "complete" {
			t.Fatalf("late association moved to %s", got)
		}
	}
	payload := criterionAssociationPayload(ref, "predicate:first")
	before := contractCorrectionEffectSnapshot(t, s, workID)
	if issue1013HasIntent(issue1013Pin(t, s, workID), "record_verdict") {
		t.Fatal("all criteria proved but the late route remains advertised")
	}
	for _, err := range []error{issue1013Preflight(t, s, workID, "record_verdict", payload, owner), runVerdictActionAs(t, s, workID, "record_verdict", payload, 0, owner)} {
		if !hasFailureKind(err, KindIllegalLifecycleTransition) {
			t.Fatalf("closed late route=%v, want current-step refusal", err)
		}
	}
	if after := contractCorrectionEffectSnapshot(t, s, workID); after != before {
		t.Fatal("closed-route refusals changed durable state")
	}
}

func TestRequestCorrectionRecordsSourceContext(t *testing.T) {
	const workID = "request-correction-source"
	j := pReturnReviewFixture(t, workID, "workflow.implementation", "correction_required")
	_, action, err := WorkflowActionDefinitionFor(t.Context(), j.s, BuiltinWorkflowRegistry(), workID, "request_correction")
	if err != nil || action.ID != "request_correction" {
		t.Fatalf("resolve corrective return: %+v, %v", action, err)
	}
	payload := pReviewCorrectionPayload(t)
	if err := j.action(t, "request_correction", payload, j.fixture.owner); err != nil {
		t.Fatal(err)
	}
	pin := issue1013Pin(t, j.s, workID)
	if pin.Correction == nil {
		t.Fatal("corrective return omitted its source context")
	}
	want := WorkflowCorrectionContext{Disposition: "verification", Diagnosis: "the accepted current Review requires correction", Strategy: "perform the declared corrective work and obtain applicable Review", PredicateIDs: []string{"predicate:return-route"}, EvidenceRefs: []string{"evidence:return-route-verification"}, SourceEventID: pin.Correction.SourceEventID, SourceEventSeq: pin.Correction.SourceEventSeq}
	if want.SourceEventID == "" || want.SourceEventSeq < 1 || !reflect.DeepEqual(*pin.Correction, want) {
		t.Fatalf("correction context=%+v, want %+v with exact durable source", pin.Correction, want)
	}
	var sourceID, sourceDigest string
	if err := j.s.db.QueryRow(`SELECT event_id,payload_digest FROM effect_receipts WHERE subject_id=? AND effect_seq=? AND kind=?`, workID, want.SourceEventSeq, WorkflowActionCompleted).Scan(&sourceID, &sourceDigest); err != nil || sourceID != want.SourceEventID || sourceDigest == "" {
		t.Fatalf("correction source=%q digest=%q err=%v", sourceID, sourceDigest, err)
	}
	if err := payloadschema.Validate("work_pin", mustJSONValue(pin)); err != nil {
		t.Fatalf("corrective work pin violates the response schema: %v", err)
	}
	before := j.effects(t)
	if err := j.action(t, "request_correction", payload, j.fixture.owner); err == nil {
		t.Fatal("a consumed Review admitted another correction")
	}
	if j.effects(t) != before || !reflect.DeepEqual(issue1013Pin(t, j.s, workID).Correction, &want) {
		t.Fatal("refused repeated request changed effects or its exact source")
	}
}

func TestCorrectiveReviewClearsPinContext(t *testing.T) {
	const workID = "work-c1b9adae6631673333fc94b8"
	j := pReturnReviewFixture(t, workID, "workflow.break_fix", "correction_required")
	s, owner := j.s, j.fixture.owner
	if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), owner); err != nil {
		t.Fatalf("request correction: %v", err)
	}
	pin := issue1013Pin(t, s, workID)
	if pin.Correction == nil {
		t.Fatal("work pin omitted the undischarged correction")
	}
	validatorCorrection, err := readWorkflowCorrectionContext(t.Context(), s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pin.Correction, validatorCorrection) {
		t.Fatalf("work pin correction=%#v, validator correction=%#v", pin.Correction, validatorCorrection)
	}
	before := j.effects(t)
	if err := j.action(t, "record_delivery", map[string]any{"delivery_artifact": "artifact:uncorrected", "delivery_state": "asserted"}, owner); err == nil {
		t.Fatal("delivery without fresh Review discharged correction")
	}
	if j.effects(t) != before {
		t.Fatal("refused delivery changed durable state")
	}
	pReturnProduce(t, j, "corrective")
	if got := issue1013Pin(t, s, workID).Correction; !reflect.DeepEqual(got, validatorCorrection) {
		t.Fatalf("subject observation alone consumed correction: %+v", got)
	}
	acceptCurrentReviewForTest(t, j.fixture, "ready_for_final_proof")
	pin = issue1013Pin(t, s, workID)
	if pin.Correction != nil {
		t.Fatalf("work pin retained discharged correction: %#v", pin.Correction)
	}
	if correction, err := readWorkflowCorrectionContext(t.Context(), s.db, workID); err != nil || correction != nil {
		t.Fatalf("later correction=%#v err=%v, want none", correction, err)
	}
}

// Every correction consumes the accepted Review that admitted it. Each later
// correction needs a fresh corrective delivery and a fresh accepted
// correction_required Review; a consumed Review never admits another.
func TestRepeatedVerificationCorrectionRequiresFreshVerdict(t *testing.T) {
	t.Parallel()
	const workID = "verification-wall-fourth-request"
	j := pReturnReviewFixture(t, workID, "workflow.implementation", "correction_required")
	for cycle := 1; cycle <= 4; cycle++ {
		if cycle > 1 {
			pReturnProduce(t, j, fmt.Sprintf("correction-%d", cycle))
			acceptCurrentReviewForTest(t, j.fixture, "correction_required")
		}
		if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); err != nil {
			t.Fatalf("cycle %d correction request: %v", cycle, err)
		}
		reviewGateRequireStep(t, j.s, workID, "execution")
		pin := issue1013Pin(t, j.s, workID)
		if pin.Correction == nil || len(pin.Correction.PredicateIDs) != 1 || pin.Correction.PredicateIDs[0] != "predicate:return-route" {
			t.Fatalf("correction after cycle %d = %#v, want the current predicate source", cycle, pin.Correction)
		}
	}
	before := j.effects(t)
	if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); err == nil {
		t.Fatal("request_correction reused a consumed Review")
	}
	if j.effects(t) != before {
		t.Fatal("refused correction changed durable state")
	}
}

// A correction must be representable in the response before its effect commits.
func TestRequestCorrectionRefusesSchemaBreakingRefs(t *testing.T) {
	for _, tc := range []struct{ name, field, value string }{
		{"predicate slash", "predicate_ids", "predicate:primary/slash"},
		{"evidence whitespace", "evidence_refs", "evidence:review with spaces"},
		{"evidence length", "evidence_refs", "evidence:" + strings.Repeat("a", 129)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workID := "correction-charset-" + strings.ReplaceAll(tc.name, " ", "-")
			j := pReturnReviewFixture(t, workID, "workflow.implementation", "correction_required")
			var payload map[string]any
			if err := json.Unmarshal(pReviewCorrectionPayload(t), &payload); err != nil {
				t.Fatal(err)
			}
			payload[tc.field] = []string{tc.value}
			before := j.effects(t)
			err := j.action(t, "request_correction", payload, j.fixture.owner)
			if !hasFailureKind(err, KindInvalidPayload) {
				t.Fatalf("correction with %s=%q: %v, want invalid_payload", tc.field, tc.value, err)
			}
			if j.effects(t) != before || issue1013Pin(t, j.s, workID).Correction != nil {
				t.Fatal("invalid correction changed effects or acquired a source")
			}
			if err := payloadschema.Validate("work_pin", mustJSONValue(issue1013Pin(t, j.s, workID))); err != nil {
				t.Fatalf("refused correction left a malformed response: %v", err)
			}
		})
	}
}

func TestCorrectiveReviewAllowsFreshCorrectionSource(t *testing.T) {
	const workID = "correction-count-accept-reset"
	j := pReturnReviewFixture(t, workID, "workflow.break_fix", "correction_required")
	if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); err != nil {
		t.Fatal(err)
	}
	source := issue1013Pin(t, j.s, workID).Correction
	if source == nil || source.SourceEventID == "" || source.SourceEventSeq < 1 {
		t.Fatalf("first correction has no source: %+v", source)
	}
	pReturnProduce(t, j, "corrective")
	acceptCurrentReviewForTest(t, j.fixture, "ready_for_final_proof")
	pin := issue1013Pin(t, j.s, workID)
	if pin.Correction != nil {
		t.Fatalf("corrective Review left correction context: %#v", pin.Correction)
	}
	if step := currentStep(t, j.s, workID); step != "repair" {
		t.Fatalf("Review advanced the step to %q, want repair", step)
	}
	pReturnProduce(t, j, "later-defect")
	acceptCurrentReviewForTest(t, j.fixture, "correction_required")
	if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); err != nil {
		t.Fatal(err)
	}
	pin = issue1013Pin(t, j.s, workID)
	if pin.Correction == nil || pin.Correction.SourceEventID == source.SourceEventID || pin.Correction.SourceEventSeq <= source.SourceEventSeq || pin.Correction.Diagnosis != source.Diagnosis || !reflect.DeepEqual(pin.Correction.PredicateIDs, source.PredicateIDs) {
		t.Fatalf("later correction=%+v, want fresh identity for the same declared direction", pin.Correction)
	}
}

// A scope-preserving contract revision cannot erase a genuine corrective request.
func TestCorrectionSourceSurvivesContractRevision(t *testing.T) {
	const workID = "correction-count-revision"
	j := pReturnReviewFixture(t, workID, "workflow.break_fix", "correction_required")
	s, owner := j.s, j.fixture.owner
	if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), owner); err != nil {
		t.Fatal(err)
	}
	pin := issue1013Pin(t, s, workID)
	if pin.Correction == nil || pin.Correction.SourceEventID == "" || pin.Correction.SourceEventSeq < 1 {
		t.Fatalf("correction before revision = %#v, want the genuine request source", pin.Correction)
	}
	source := *pin.Correction
	var sourceDigest string
	if err := s.db.QueryRow(`SELECT payload_digest FROM effect_receipts WHERE event_id=? AND effect_seq=?`, source.SourceEventID, source.SourceEventSeq).Scan(&sourceDigest); err != nil {
		t.Fatal(err)
	}
	contract := pContractSuccessor(t, s, workID, "The same approved outcome needs a corrected approach.")
	if err := reviseContractRecord(t, s, workID, owner, contract, readWorkVersion(t, s, workID), nil); err != nil {
		t.Fatalf("revise the contract across the correction sequence: %v", err)
	}
	if active, err := activeWorkflowContractVersion(t.Context(), s.db, workID, "test"); err != nil || active != 2 {
		t.Fatalf("active contract version = %d, %v; want 2", active, err)
	}
	pin = issue1013Pin(t, s, workID)
	if !reflect.DeepEqual(pin.Correction, &source) {
		t.Errorf("correction across revision = %#v, want the same genuine request", pin.Correction)
	}
	var afterDigest string
	if err := s.db.QueryRow(`SELECT payload_digest FROM effect_receipts WHERE event_id=? AND effect_seq=?`, source.SourceEventID, source.SourceEventSeq).Scan(&afterDigest); err != nil || afterDigest != sourceDigest {
		t.Fatalf("revision changed the source receipt: %q -> %q, %v", sourceDigest, afterDigest, err)
	}
	pReturnProduce(t, j, "successor-correction")
	acceptCurrentReviewForTest(t, j.fixture, "correction_required")
	if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), owner); err != nil {
		t.Fatalf("fresh corrective return under successor: %v", err)
	}
	pin = issue1013Pin(t, s, workID)
	if pin.Correction == nil || pin.Correction.SourceEventID == source.SourceEventID || pin.Correction.SourceEventSeq <= source.SourceEventSeq {
		t.Fatalf("successor correction=%+v, want fresh request identity", pin.Correction)
	}
}
