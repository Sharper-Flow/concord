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
	"accept_decision", "add_condition", "approve_operation", "cancel_condition",
	"checkpoint_action", "checkpoint_analysis", "checkpoint_poc", "checkpoint_run",
	"cleanup_run", "declare_scope", "discard_poc", "frame_question",
	"record_decision", "record_health", "record_option", "record_research",
	"resolve_condition", "rollback_run", "run_analysis", "start_action",
	"start_poc", "start_run",
	"record_reproduction", "record_root_cause", "start_repair", "start_refine",
	"record_worker_failure", "record_discovery",
}

// liveActionTarget is the approved public action target: 58 prior public
// IDs − 22 retired-family IDs − 6 ceremony IDs + record_diagnosis
// (CD-0218 D1/D2).
const liveActionTarget = 31

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

// TestPublicPayloadDivergenceKeepsThePair keeps dispatch_worker's override
// truthful: one variant whose payload and public payload differ stays one
// variant carrying both halves, and a second family repeating the same pair
// dedupes rather than adding a variant.
func TestPublicPayloadDivergenceKeepsThePair(t *testing.T) {
	secret := closedPayload(field("attempt_id", true, 128), field("worker_packet", true, 64))
	public := closedPayload(field("lane_id", true, 128))
	repeat := action("dispatch_worker", secret)
	repeat.PublicPayload = &public
	contracts, err := collectActionContracts(
		[]store.WorkflowDefinition{
			definition("workflow.implementation", repeat),
			definition("workflow.break_fix", func() store.WorkflowActionDefinition {
				again := action("dispatch_worker", secret)
				again.PublicPayload = &public
				return again
			}()),
		},
		nil,
	)
	if err != nil {
		t.Fatalf("repeated pair refused: %v", err)
	}
	contract := contracts["dispatch_worker"]
	if len(contract.Variants) != 1 {
		t.Fatalf("variants = %d, want the one deduped pair", len(contract.Variants))
	}
	if !reflect.DeepEqual(contract.Variants[0].PublicPayload, public) {
		t.Fatalf("public half lost: %#v", contract.Variants[0])
	}
}

// TestShippedBuiltinsProjectTheLiveSurfaceOnly is the live-surface
// integration: the shipped heads and the canonical recovery list fold into
// exactly the approved 31 authorable actions, no retired published action
// is authorable, retained versions add no variant to a live action, the
// canonical verdict and supersede shapes are the declared ones, and
// record_diagnosis is present (CD-0218 D1/D2).
func TestShippedBuiltinsProjectTheLiveSurfaceOnly(t *testing.T) {
	contracts, err := collectActionContracts(
		store.BuiltinWorkflowDefinitions(),
		store.BuiltinWorkflowCanonicalRecoveryActionDefinitions(),
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
	if len(ids) != liveActionTarget {
		sort.Strings(ids)
		t.Fatalf("live action projection = %d IDs %v, want the approved target %d", len(ids), ids, liveActionTarget)
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
	// Canonical supersede authoring carries the explicit outcome_predicates
	// array alone; the paired outcome shape is not authorable.
	supersede := contracts["supersede_contract"]
	if len(supersede.Variants) != 1 {
		t.Fatalf("supersede_contract variants = %d, want the canonical recovery shape", len(supersede.Variants))
	}
	supersedeFields := map[string]bool{}
	for _, f := range supersede.Variants[0].Payload.Fields {
		supersedeFields[f.Name] = f.Required
	}
	if !supersedeFields["outcome_predicates"] || supersedeFields["outcome_kind"] || supersedeFields["outcome_payload"] {
		t.Fatalf("supersede_contract canonical payload drifted: %#v", supersedeFields)
	}
	if len(supersede.CrossField.Alternatives) != 1 || len(supersede.CrossField.KindMatches) != 0 {
		t.Fatalf("supersede_contract cross-field = %#v, want the predicates group without the paired kind match", supersede.CrossField)
	}
	// Retained versions keep declaring older shapes (the oracle-free
	// record_worker_job chain among them); none of them may widen a live
	// action's authoring variants — the frozen historical decoder owns
	// their replay side (CD-0218 D4).
	job := contracts["record_worker_job"]
	if len(job.Variants) != 1 {
		t.Fatalf("record_worker_job variants = %d, want only the current oracle shape", len(job.Variants))
	}
	oracleDeclared := false
	for _, f := range job.Variants[0].Payload.Fields {
		if f.Name == "acceptance_oracle" && f.Required && f.SchemaRef == "worker_acceptance_oracle" {
			oracleDeclared = true
		}
	}
	if !oracleDeclared {
		t.Fatalf("record_worker_job current variant does not require acceptance_oracle via worker_acceptance_oracle")
	}
	correction := contracts["request_correction"]
	if len(correction.Variants) != 2 {
		t.Fatalf("request_correction variants = %d, want the oracle shape and the recovery shape", len(correction.Variants))
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
// drops, a part declaring an undeclared required field or partially
// overlapping the declared set refuses, and a fully declared rule passes
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
	// A partially overlapping group is a structural disagreement: the
	// paired supersede group declares one half without the other.
	var canonicalRecoveryFields []store.WorkflowPayloadField
	for _, action := range store.BuiltinWorkflowCanonicalRecoveryActionDefinitions() {
		if action.ID == "supersede_contract" {
			canonicalRecoveryFields = action.Payload.Fields
			break
		}
	}
	if len(canonicalRecoveryFields) == 0 {
		t.Fatal("canonical recovery publication omits supersede_contract")
	}
	partialSupersede := append(append([]store.WorkflowPayloadField{}, canonicalRecoveryFields...),
		store.WorkflowPayloadField{Name: "outcome_kind", ValueType: store.PayloadString})
	if _, err := store.WorkflowActionCanonicalCrossFieldRules("supersede_contract", partialSupersede); err == nil {
		t.Fatal("a group partially overlapping the declared set projected instead of refusing")
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
	// Canonical supersede recovers the predicates group and drops the
	// paired kind match.
	supersede, err := store.WorkflowActionCanonicalCrossFieldRules("supersede_contract", canonicalRecoveryFields)
	if err != nil {
		t.Fatalf("canonical supersede projection refused: %v", err)
	}
	if len(supersede.Alternatives) != 1 || len(supersede.Alternatives[0].Fields) != 1 || supersede.Alternatives[0].Fields[0] != "outcome_predicates" {
		t.Fatalf("projected supersede alternatives = %#v, want the predicates group alone", supersede.Alternatives)
	}
	if len(supersede.KindMatches) != 0 {
		t.Fatalf("projected supersede kind matches = %#v, want the paired match dropped", supersede.KindMatches)
	}
}

func TestSchemaRefsComeFromCurrentDeclarations(t *testing.T) {
	currentRef := "current_only_ref"
	projection := collectSchemaRefs(
		[]store.WorkflowDefinition{definition("workflow.implementation",
			action("record_worker_job", closedPayload(field("objective", true, 4096)))),
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
