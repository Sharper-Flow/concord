package store

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// workflowEvidenceGapDefinition is a synthetic definition whose evidence
// demands exceed its reach. Its only producer-bearing action is
// accept_worker_result, which binds verification, review, and artifact and
// nothing else, while the definition, its gate step, and its rigor rule all
// require commit: no route through the graph can bind commit. The shape is
// registry-valid so the refusal test can pin a real instance to it; the
// definition is never shipped.
func workflowEvidenceGapDefinition() WorkflowDefinition {
	changesProductTruth := false
	steps := []WorkflowStep{
		{ID: "gate", Kind: WorkflowStepHumanCheckpoint, Actions: []string{"approve_contract"}, RequiredEvidenceKinds: []EvidenceKind{EvidenceCommit}},
		{ID: "worker_output", Kind: WorkflowStepInternalSQLite, Actions: []string{"accept_worker_result"}},
		{ID: "complete", Kind: WorkflowStepInternalSQLite, Actions: []string{"complete"}},
	}
	edges := []WorkflowEdge{
		{From: "gate", To: "worker_output", Kind: WorkflowEdgeForward},
		{From: "worker_output", To: "complete", Kind: WorkflowEdgeForward},
	}
	actions := []string{"accept_worker_result", "approve_contract", "complete"}
	return WorkflowDefinition{
		Ref: "workflow.evidence_gap", Version: 1, WorkKind: WorkKindGenericOneOff, ChangesProductTruth: &changesProductTruth,
		StepGraph:             WorkflowStepGraph{StartStep: "gate", TerminalSteps: []string{"complete"}, Steps: steps, Edges: edges},
		AvailableActions:      actions,
		ActionDefinitions:     actionDefinitions(actions, false),
		RequiredEvidenceKinds: []EvidenceKind{EvidenceCommit},
		OutcomeSchema:         WorkflowOutcomeSchema{DefaultKind: PredicateOutcome, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateOutcome, PredicateCheck}, AllowedOutcomeTokens: []string{"no_change", "accepted_decision", "insufficient_evidence", "resolved", "remediated", "report_recorded", "completed", "operator_defined"}},
		RigorRules:            []WorkflowRigorRule{{Maturity: "prototype", AudienceBand: "internal", RequiredEvidenceKinds: []EvidenceKind{EvidenceCommit}}},
		CompositionRules:      WorkflowCompositionRules{ForwardLinkOnly: true, AllowedSuccessorWorkKinds: []WorkKind{WorkKindBreakFix}, ForbiddenCompositions: []WorkflowForbiddenComposition{}},
	}
}

// workflowEvidenceGapRegistration pins the synthetic definition into the
// builtin registry at test-binary initialization, the same commitment the
// fixture family in workflow_test_support_test.go makes: cross-process race
// workers re-enter the binary without running a helper, so the pin must
// resolve before any test runs.
var workflowEvidenceGapRegistration = registerWorkflowEvidenceGapDefinition()

func registerWorkflowEvidenceGapDefinition() RegisteredDefinition {
	entry, err := BuiltinWorkflowRegistry().Register(workflowEvidenceGapDefinition())
	if err != nil {
		panic(err)
	}
	return entry
}

// TestSyntheticDefinitionEvidenceGapIsDetected gives the shipped reachability
// invariant its negative. The synthetic definition demands commit where its
// only reachable producer binds verification, review, and artifact, and the
// same check that holds every shipped definition to its reach names the gap
// at all three requirement sites.
func TestSyntheticDefinitionEvidenceGapIsDetected(t *testing.T) {
	definition := workflowEvidenceGapDefinition()
	producible := evidenceStrings(workflowReachableEvidenceKinds(definition))
	for _, kind := range []string{"verification", "review", "artifact"} {
		if !containsString(producible, kind) {
			t.Fatalf("accept_worker_result makes %q producible; producible=%v", kind, producible)
		}
	}
	for _, kind := range []string{"commit", "approval", "durable_note", "native_run"} {
		if containsString(producible, kind) {
			t.Fatalf("no reachable action of the synthetic definition produces %q; producible=%v", kind, producible)
		}
	}
	want := []EvidenceKind{EvidenceCommit, EvidenceCommit, EvidenceCommit}
	if missing := workflowRequiredEvidenceOutsideReach(definition); !reflect.DeepEqual(missing, want) {
		t.Fatalf("unproducible requirements=%v, want commit at the definition, the gate step, and the rigor rule", missing)
	}
	definition.StepGraph.StartStep = ""
	if producible := workflowReachableEvidenceKinds(definition); len(producible) != 0 {
		t.Fatalf("a definition with no start step produces nothing; got %v", producible)
	}
}

// TestApproveContractRefusesUnproducibleRequiredEvidence holds the runtime
// half of the reachability invariant: a caller-supplied required_evidence
// naming a kind the pinned definition cannot produce is refused before any
// effect, and the same contract is admitted once its requirement names a kind
// the definition can produce, so the boundary sits at producibility alone.
func TestApproveContractRefusesUnproducibleRequiredEvidence(t *testing.T) {
	s := openTemp(t)
	workID := "workflow-evidence-reachability"
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	actor := WorkflowActor{PrincipalRef: "principal:reachability", ClientRef: "client:reachability", AgentRef: "agent:reachability", SessionRef: "session:reachability", ActorClass: ActorAgent}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(context.Background(), tx, WorkflowInitializationRequest{WorkID: workID, Definition: workflowEvidenceGapRegistration, Actor: actor, Now: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}

	runApprove := func(operationID string, requiredEvidence string) error {
		payload := testApprovalPayload("approve_contract", json.RawMessage(`{"required_evidence":[`+requiredEvidence+`]}`))
		tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = applyWorkflowActionRawTx(context.Background(), tx, newFoldScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
			WorkID: workID, ExpectedVersion: version, ActionID: "approve_contract", Payload: payload, Actor: actor,
			AcceptedInputsDigest: "sha256:reachability", IdempotencyIdentity: operationID, OperationID: operationID,
			PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID,
			RequestID: "request:" + operationID, ContractDigest: testManifestDigest, Now: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC),
		})
		if err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return nil
	}

	err = runApprove("reachability-refuse", `"commit"`)
	if err == nil {
		t.Fatal("approve_contract admitted a required_evidence kind the pinned definition cannot produce")
	}
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload {
		t.Fatalf("refusal failure=%+v err=%v", failure, err)
	}
	if !strings.Contains(failure.Detail, `"commit"`) || !strings.Contains(failure.Detail, "workflow.evidence_gap") {
		t.Fatalf("refusal does not name the unproducible kind and the pinned definition: %s", failure.Detail)
	}

	var contracts int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=?`, workID).Scan(&contracts); err != nil {
		t.Fatal(err)
	}
	var approvals int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowContractApproved).Scan(&approvals); err != nil {
		t.Fatal(err)
	}
	var versionAfter int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&versionAfter); err != nil {
		t.Fatal(err)
	}
	if contracts != 0 || approvals != 0 || versionAfter != version {
		t.Fatalf("refusal left effects behind: contracts=%d approvals=%d version=%d want %d", contracts, approvals, versionAfter, version)
	}

	if err := runApprove("reachability-admit", `"verification"`); err != nil {
		t.Fatalf("approve_contract with a producible required_evidence kind: %v", err)
	}
	var stored string
	if err := s.DatabaseForTesting().QueryRow(`SELECT required_evidence FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != `["verification"]` {
		t.Fatalf("stored required_evidence=%s, want the admitted kind alone", stored)
	}
}

// TestWorkerAttemptEvidenceKindsCoverTheCapabilityMapping keeps the closed
// accept_worker_result producer set equal to the codomain of
// workerAttemptEvidenceKind, so the reachability table cannot drift from the
// kind an accepted attempt actually binds as.
func TestWorkerAttemptEvidenceKindsCoverTheCapabilityMapping(t *testing.T) {
	t.Parallel()
	produced := map[string]bool{}
	for _, capability := range []string{"verification", "review", "implementation", "research", "design", "unlisted"} {
		produced[workerAttemptEvidenceKind(capability)] = true
	}
	want := map[string]bool{}
	for _, kind := range workflowWorkerAttemptEvidenceKinds {
		want[string(kind)] = true
	}
	if !reflect.DeepEqual(produced, want) {
		t.Fatalf("workerAttemptEvidenceKind produces %v, workflowWorkerAttemptEvidenceKinds declares %v", produced, want)
	}
}
