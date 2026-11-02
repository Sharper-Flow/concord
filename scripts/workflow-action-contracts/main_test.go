package main

import (
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/testenv"
)

func TestMain(m *testing.M) {
	dir := testenv.ScrubEnv()
	code := m.Run()
	os.Exit(testenv.Cleanup(dir, code))
}

func field(name string, required bool, maxLength int64) store.WorkflowPayloadField {
	return store.WorkflowPayloadField{Name: name, ValueType: store.PayloadString, Required: required, MaxLength: &maxLength}
}

func closedPayload(fields ...store.WorkflowPayloadField) store.WorkflowPayloadDefinition {
	if fields == nil {
		fields = []store.WorkflowPayloadField{}
	}
	return store.WorkflowPayloadDefinition{Closed: true, Fields: fields}
}

func definition(ref string, actions ...store.WorkflowActionDefinition) store.WorkflowDefinition {
	return store.WorkflowDefinition{Ref: ref, ActionDefinitions: actions}
}

func action(id string, payload store.WorkflowPayloadDefinition) store.WorkflowActionDefinition {
	return store.WorkflowActionDefinition{ID: id, Payload: payload}
}

// retiredPublishedActions is the closed set CD-0218 D1/D2 retires from live
// authoring: the 22 retired-family action IDs and the 6 ceremony/derived
// IDs. The projection owner derives their absence from this published set;
// no compatibility alias or retained publication may revive them.
var retiredPublishedActions = []string{
	"accept_decision", "approve_operation",
	"checkpoint_action", "checkpoint_analysis", "checkpoint_poc", "checkpoint_run",
	"cleanup_run", "declare_scope", "discard_poc", "frame_question",
	"record_decision", "record_health", "record_option", "record_research",
	"rollback_run", "run_analysis", "start_action",
	"start_poc", "start_run",
	"record_reproduction", "record_root_cause", "start_repair", "start_refine",
	"record_worker_failure", "record_discovery", "supersede_contract",
}

var liveActionIDs = []string{
	"approve_contract", "bind_evidence", "checkpoint_context", "checkpoint_execution",
	"checkpoint_refine", "checkpoint_repair", "complete", "confirm_premise",
	"cross_context_boundary", "frame_research", "link_successor", "record_alignment",
	"record_conclusion", "record_delivery", "record_design", "record_diagnosis",
	"record_finding", "record_proposal", "record_report", "record_review",
	"record_verdict", "record_work_context", "request_correction", "revise_candidates",
	"start_execution",
}

// TestCurrentFieldsetAdditionProjectsVariants holds the variant shape: two
// current families declare the same action, one with an appended optional
// field, and the projection answers two exact closed variants instead of
// refusing the pair.
func TestCurrentFieldsetAdditionProjectsVariants(t *testing.T) {
	base := closedPayload(field("diagnosis", true, 4096), field("strategy", true, 4096))
	extended := closedPayload(field("diagnosis", true, 4096), field("strategy", true, 4096), field("open_finding_ids", false, 32))
	contracts, err := collectActionContracts(
		[]store.WorkflowDefinition{
			definition("workflow.implementation", action("request_correction", extended)),
			definition("workflow.research", action("request_correction", base)),
		},
		nil,
	)
	if err != nil {
		t.Fatalf("compatible current variants refused: %v", err)
	}
	contract := contracts["request_correction"]
	if len(contract.Variants) != 2 {
		t.Fatalf("variants = %d, want 2 (extended first, in registration order)", len(contract.Variants))
	}
	if !reflect.DeepEqual(contract.Variants[0].Payload, extended) || !reflect.DeepEqual(contract.Variants[1].Payload, base) {
		t.Fatalf("variant order or shape drifted: %#v", contract.Variants)
	}
	for _, variant := range contract.Variants {
		if !variant.Payload.Closed || !variant.PublicPayload.Closed {
			t.Fatalf("variant payload is not closed: %#v", variant)
		}
	}
}

// TestCurrentSameFieldConflictStillRefuses holds the surviving invariant: a
// field two current families declare with different declarations is a real
// conflict, and the projection refuses it rather than publishing a union no
// definition allows.
func TestCurrentSameFieldConflictStillRefuses(t *testing.T) {
	soft := closedPayload(field("diagnosis", true, 1024))
	hard := closedPayload(field("diagnosis", true, 4096))
	_, err := collectActionContracts(
		[]store.WorkflowDefinition{
			definition("workflow.implementation", action("request_correction", hard)),
			definition("workflow.research", action("request_correction", soft)),
		},
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "action request_correction has inconsistent current payload contracts") {
		t.Fatalf("error = %v, want the inconsistent-current-contracts refusal", err)
	}
	if err == nil || !strings.Contains(err.Error(), "diagnosis") {
		t.Fatalf("error = %v, want the conflicting field named", err)
	}
}

// TestPublicPayloadDivergenceKeepsThePair proves that one variant whose
// payload and public payload differ stays one
// variant carrying both halves, and a second family repeating the same pair
// dedupes rather than adding a variant.
func TestPublicPayloadDivergenceKeepsThePair(t *testing.T) {
	secret := closedPayload(field("internal_ref", true, 128))
	public := closedPayload(field("public_ref", true, 128))
	repeat := action("projection_probe", secret)
	repeat.PublicPayload = &public
	contracts, err := collectActionContracts(
		[]store.WorkflowDefinition{
			definition("workflow.implementation", repeat),
			definition("workflow.break_fix", func() store.WorkflowActionDefinition {
				again := action("projection_probe", secret)
				again.PublicPayload = &public
				return again
			}()),
		},
		nil,
	)
	if err != nil {
		t.Fatalf("repeated pair refused: %v", err)
	}
	contract := contracts["projection_probe"]
	if len(contract.Variants) != 1 {
		t.Fatalf("variants = %d, want the one deduped pair", len(contract.Variants))
	}
	if !reflect.DeepEqual(contract.Variants[0].PublicPayload, public) {
		t.Fatalf("public half lost: %#v", contract.Variants[0])
	}
}

// TestShippedBuiltinsProjectTheLiveSurfaceOnly is the live-surface
// integration: the shipped heads and the canonical recovery list fold into
// exactly the current authorable actions, no retired published action
// is authorable, retained versions add no variant to a live action, the
// canonical verdict and contract update shapes are the declared ones, and
// record_diagnosis is present (CD-0218 D1/D2).
func TestShippedBuiltinsProjectTheLiveSurfaceOnly(t *testing.T) {
	contracts, err := collectActionContracts(
		store.BuiltinWorkflowDefinitions(),
		store.BuiltinWorkflowRecoveryActionDefinitions(),
	)
	if err != nil {
		t.Fatalf("shipped builtins refused: %v", err)
	}
	ids := make([]string, 0, len(contracts))
	for id, contract := range contracts {
		ids = append(ids, id)
		for _, variant := range contract.Variants {
			if !variant.Payload.Closed || !variant.PublicPayload.Closed {
				t.Fatalf("action %s carries an open variant", id)
			}
		}
	}
	sort.Strings(ids)
	if !reflect.DeepEqual(ids, liveActionIDs) {
		t.Fatalf("live action projection = %d IDs %v, want %d IDs %v", len(ids), ids, len(liveActionIDs), liveActionIDs)
	}
	live := map[string]bool{}
	for _, id := range ids {
		live[id] = true
	}
	for _, retired := range retiredPublishedActions {
		if live[retired] {
			t.Fatalf("retired published action %s remains authorable (CD-0218 D1/D2)", retired)
		}
	}
	for _, retired := range []string{"dispatch_worker", "record_worker_job", "accept_worker_evidence", "accept_worker_result", "reject_worker_result"} {
		if live[retired] {
			t.Fatalf("retired worker action %s remains authorable (CD-0242 D6)", retired)
		}
	}
	if !live["record_diagnosis"] {
		t.Fatal("record_diagnosis is missing from the live projection")
	}
	// The canonical verdict contract is the batch alone: verdicts required,
	// the single-form scalar fields outside the declared set, and the
	// cross-field rule projected down to the batch group (CD-0218 D2).
	verdict := contracts["record_verdict"]
	if len(verdict.Variants) != 1 {
		t.Fatalf("record_verdict variants = %d, want the canonical batch shape", len(verdict.Variants))
	}
	verdictFields := map[string]bool{}
	for _, f := range verdict.Variants[0].Payload.Fields {
		verdictFields[f.Name] = f.Required
	}
	if !verdictFields["verdicts"] || verdictFields["predicate_id"] || len(verdictFields) != 2 {
		t.Fatalf("record_verdict canonical payload drifted: %#v", verdictFields)
	}
	if len(verdict.CrossField.Alternatives) != 1 || len(verdict.CrossField.Alternatives[0].Fields) != 1 || verdict.CrossField.Alternatives[0].Fields[0] != "verdicts" {
		t.Fatalf("record_verdict cross-field = %#v, want the single verdicts group", verdict.CrossField)
	}
	if len(verdict.CrossField.Alternatives[0].Forbids) != 0 {
		t.Fatalf("record_verdict cross-field keeps forbids the canonical payload does not declare: %#v", verdict.CrossField.Alternatives[0].Forbids)
	}
	updateFields := map[string]bool{}
	for _, f := range store.BuiltinWorkflowContractUpdateFields() {
		updateFields[f.Name] = f.Required
	}
	if !updateFields["outcome_predicates"] || !updateFields["contract_version"] || updateFields["outcome_kind"] || updateFields["outcome_payload"] {
		t.Fatalf("contract update payload drifted: %#v", updateFields)
	}
	review := contracts["record_review"]
	if len(review.Variants) != 1 {
		t.Fatalf("record_review variants = %d, want one current review shape", len(review.Variants))
	}
	reviewFields := review.Variants[0].Payload.Fields
	if len(reviewFields) != 1 || reviewFields[0].Name != "review" || !reviewFields[0].Required || reviewFields[0].SchemaRef != "workflow_review_report" {
		t.Fatalf("record_review does not require the current review report schema: %#v", reviewFields)
	}
	correction := contracts["request_correction"]
	if len(correction.Variants) != 2 {
		t.Fatalf("request_correction variants = %d, want the current and recovery shapes", len(correction.Variants))
	}
	hasOpenFindings := false
	for _, variant := range correction.Variants {
		for _, f := range variant.Payload.Fields {
			if f.Name == "open_finding_ids" {
				hasOpenFindings = true
			}
		}
	}
	if !hasOpenFindings {
		t.Fatalf("no request_correction variant declares open_finding_ids")
	}
}

// TestCanonicalCrossFieldRulesProjectAndRefuse holds the projection rule:
// a rule part naming only undeclared fields drops, a vacuous forbids entry
// drops, a combination declaring an undeclared required field refuses,
// and a fully declared rule passes
// through unchanged.
func TestCanonicalCrossFieldRulesProjectAndRefuse(t *testing.T) {
	// The projection binds rules to declared field names, so these
	// witnesses need only the names.
	verdictNames := []store.WorkflowPayloadField{
		{Name: "contract_version", ValueType: store.PayloadInteger},
		{Name: "verdicts", ValueType: store.PayloadArray, Required: true, ItemRef: "workflow_verdict_batch_entry"},
	}
	projected, err := store.WorkflowActionCanonicalCrossFieldRules("record_verdict", verdictNames)
	if err != nil {
		t.Fatalf("canonical verdict projection refused: %v", err)
	}
	if len(projected.Alternatives) != 1 || len(projected.Alternatives[0].Fields) != 1 || projected.Alternatives[0].Fields[0] != "verdicts" {
		t.Fatalf("projected alternatives = %#v, want the verdicts group alone", projected.Alternatives)
	}
	if len(projected.Alternatives[0].Forbids) != 0 {
		t.Fatalf("projected forbids = %#v, want the vacuous entries dropped", projected.Alternatives[0].Forbids)
	}
	// A fully declared rule passes through with its combinations intact.
	diagnosisNames := []store.WorkflowPayloadField{
		{Name: "reproduction", ValueType: store.PayloadString}, {Name: "searched", ValueType: store.PayloadString},
		{Name: "outcome", ValueType: store.PayloadString, Required: true},
		{Name: "related_ids", ValueType: store.PayloadStringList}, {Name: "root_cause", ValueType: store.PayloadString},
	}
	diagnosis, err := store.WorkflowActionCanonicalCrossFieldRules("record_diagnosis", diagnosisNames)
	if err != nil {
		t.Fatalf("declared combination projection refused: %v", err)
	}
	global := store.WorkflowActionCrossFieldRules("record_diagnosis")
	if !reflect.DeepEqual(diagnosis, global) {
		t.Fatalf("declared rule drifted through the projection: %#v vs %#v", diagnosis, global)
	}
	// A combination requiring an undeclared field refuses.
	if _, err := store.WorkflowActionCanonicalCrossFieldRules("record_diagnosis", diagnosisNames[:3]); err == nil {
		t.Fatal("a combination requiring an undeclared field projected instead of refusing")
	}
}

func TestSchemaRefsComeFromCurrentDeclarations(t *testing.T) {
	currentRef := "current_only_ref"
	projection := collectSchemaRefs(
		[]store.WorkflowDefinition{definition("workflow.implementation",
			action("record_proposal", closedPayload(field("objective", true, 4096)))),
		},
		[]store.WorkflowActionDefinition{{ID: "record_verdict", Payload: store.WorkflowPayloadDefinition{
			Closed: true,
			Fields: []store.WorkflowPayloadField{{Name: "verdicts", ValueType: store.PayloadArray, ItemRef: currentRef}},
		}}},
	)
	if len(projection.Current) != 1 || projection.Current[0] != currentRef {
		t.Fatalf("current refs = %v, want [%s]", projection.Current, currentRef)
	}
}

// TestProjectionIsDeterministicAcrossRuns holds the regeneration contract:
// the same inputs fold to the same variant order every run, so generated
// artifacts stay byte-stable.
func TestProjectionIsDeterministicAcrossRuns(t *testing.T) {
	first, firstErr := collectActionContracts(store.BuiltinWorkflowDefinitions(), store.BuiltinWorkflowRecoveryActionDefinitions())
	second, secondErr := collectActionContracts(store.BuiltinWorkflowDefinitions(), store.BuiltinWorkflowRecoveryActionDefinitions())
	if firstErr != nil || secondErr != nil {
		t.Fatalf("determinism run failed: %v / %v", firstErr, secondErr)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("two runs of the same builtins produced different projections")
	}
}

func TestCurrentDefinitionsDoNotPublishRetiredNativeRunActions(t *testing.T) {
	for _, definition := range store.BuiltinWorkflowDefinitions() {
		for _, action := range definition.ActionDefinitions {
			for _, retired := range []string{"start_run", "record_health", "cleanup_run", "rollback_run"} {
				if action.ID == retired {
					t.Errorf("current definition %s publishes retired action %s", definition.Ref, retired)
				}
			}
		}
	}
}
