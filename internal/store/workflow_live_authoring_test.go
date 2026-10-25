package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// The live authoring selector and registry expose the same three current
// families. Their contracts publish explicit outcome predicates only.

func TestLiveAuthoringSelectorPublishesThreeFamiliesAndRefusesRetiredOnes(t *testing.T) {
	t.Parallel()
	for _, ref := range []string{"workflow.implementation", "workflow.break_fix", "workflow.research"} {
		registered, err := LiveWorkflowDefinitionForRef(ref)
		if err != nil {
			t.Fatalf("live selector %s: %v", ref, err)
		}
		if registered.Definition.Ref != ref {
			t.Fatalf("live selector %s resolved %s", ref, registered.Definition.Ref)
		}
		if registered.Digest == "" {
			t.Fatalf("live selector %s resolved no digest", ref)
		}
	}
	for ref := range archivedWorkflowFamilies {
		_, err := LiveWorkflowDefinitionForRef(ref)
		failure, ok := err.(*Failure)
		if !ok || failure.Kind != KindInvalidDefinition {
			t.Fatalf("archived family %s: err=%v, want %s", ref, err, KindInvalidDefinition)
		}
		if !strings.Contains(failure.Detail, ref) {
			t.Fatalf("archived family refusal %q must name the family", failure.Detail)
		}
	}
	if _, err := LiveWorkflowDefinitionForRef("workflow.unknown_family"); !hasFailureKind(err, KindInvalidDefinition) {
		t.Fatalf("unknown family: err=%v, want %s", err, KindInvalidDefinition)
	}
	for ref := range archivedWorkflowFamilies {
		if _, err := BuiltinWorkflowDefinitionForRef(ref); err == nil {
			t.Fatalf("retired family %s remains registered", ref)
		}
	}
}

func TestDefaultWorkflowRefForKindRequiresExplicitFamilyForDecisionAndOther(t *testing.T) {
	t.Parallel()
	defaults := map[string]string{
		"task":     "workflow.implementation",
		"bug":      "workflow.break_fix",
		"research": "workflow.research",
	}
	for kind, want := range defaults {
		ref, err := DefaultWorkflowRefForKind(kind)
		if err != nil {
			t.Fatalf("kind %s default: %v", kind, err)
		}
		if ref != want {
			t.Fatalf("kind %s default = %s, want %s", kind, ref, want)
		}
	}
	for _, kind := range []string{"decision", "other"} {
		_, err := DefaultWorkflowRefForKind(kind)
		failure, ok := err.(*Failure)
		if !ok || failure.Kind != KindInvalidOperation {
			t.Fatalf("kind %s default: err=%v, want %s requiring an explicit family", kind, err, KindInvalidOperation)
		}
	}
}

func TestArchivedFamilyPublicMutationRefusesBeforeWrite(t *testing.T) {
	for family := range archivedWorkflowFamilies {
		t.Run(family, func(t *testing.T) {
			ctx := context.Background()
			const workID = "archived-mutation"
			s := openTemp(t)
			seedWork(t, s, workID)
			registered, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
			if err != nil {
				t.Fatal(err)
			}
			owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
			operator := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/operator", SessionRef: "session/" + workID + "-operator", ActorClass: ActorOperator}
			tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: owner, Now: time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)}); err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			// Model a stored retired-family instance without registering a historical
			// definition. The live mutation must refuse before it can append anything.
			if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE workflow_instances SET definition_ref=? WHERE work_id=?; DELETE FROM fold_guard`, family, workID); err != nil {
				t.Fatal(err)
			}

			// The pinned retired definition stays readable through its instance.
			var pinnedRef string
			if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref FROM workflow_instances WHERE work_id=?`, workID).Scan(&pinnedRef); err != nil {
				t.Fatalf("archived instance read: %v", err)
			}
			if pinnedRef != family {
				t.Fatalf("pinned definition = %s, want the retired family", pinnedRef)
			}

			events := func() int64 {
				var count int64
				if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=?`, workID).Scan(&count); err != nil {
					t.Fatal(err)
				}
				return count
			}
			before := events()

			// Every action refuses at the unavailable-family boundary, not through
			// the archived graph's payload or step validation.
			for _, action := range []string{"checkpoint_context", "accept_worker_result", "record_decision"} {
				t.Run(action, func(t *testing.T) {
					preflightErr := WorkflowActionPreflightWithRegistry(ctx, s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
						WorkID: workID, ActionID: action, Payload: json.RawMessage(`{}`), Actor: owner,
					})
					if !hasFailureKind(preflightErr, KindInvariantViolation) {
						t.Fatalf("archived-family preflight = %v, want %s", preflightErr, KindInvariantViolation)
					}
					mutationErr := runIssue933OperatorAction(t, s, workID, action, json.RawMessage(`{}`), owner, operator)
					if !hasFailureKind(mutationErr, KindInvariantViolation) {
						t.Fatalf("archived-family mutation = %v, want %s", mutationErr, KindInvariantViolation)
					}
					for _, refusal := range []error{preflightErr, mutationErr} {
						failure, ok := refusal.(*Failure)
						if !ok || !strings.Contains(failure.Detail, family) {
							t.Fatalf("refusal %v must name the archived family %s", refusal, family)
						}
					}
					if got := events(); got != before {
						t.Fatalf("refused archived-family mutation moved events from %d to %d; the refusal must commit nothing", before, got)
					}
					if got := readWorkVersion(t, s, workID); got != 4 {
						t.Fatalf("refused mutation left the work at version %d, want the untouched 4", got)
					}
					if got := currentStep(t, s, workID); got != registered.Definition.StepGraph.StartStep {
						t.Fatalf("refused mutation moved the step to %s", got)
					}
				})
			}
		})
	}
}

func TestArchivedFamilyTerminalHistoryStaysReadable(t *testing.T) {
	t.Parallel()
	definition := genericDefinitionFixture()
	definition.Ref = "workflow.generic_one_off"
	nonterminal := WorkflowAdmissionState{InstanceState: "planned", Step: "define"}
	decision := workflowAdmit(definition, nonterminal, "checkpoint_context")
	if decision.Admitted || decision.Failure == nil || decision.Failure.Kind != KindInvalidOperation {
		t.Fatalf("nonterminal archived admission = %+v, want the archived-family refusal", decision)
	}
	completed := WorkflowAdmissionState{InstanceState: "completed", Step: "complete", Lifecycle: "completed"}
	decision = workflowAdmit(definition, completed, "approve_contract")
	if decision.Admitted || decision.Failure == nil {
		t.Fatalf("completed archived admission = %+v, want the terminal immutability refusal", decision)
	}
	if !strings.Contains(decision.Failure.Detail, "terminal workflow instance is immutable") {
		t.Fatalf("completed archived refusal = %q, want the terminal immutability answer", decision.Failure.Detail)
	}
	// A completed archived instance under a still-live work item admits one
	// route today: the complete-step correction. The retirement refuses it
	// before mutation too.
	correction := WorkflowAdmissionState{InstanceState: "completed", Step: "complete"}
	decision = workflowAdmit(definition, correction, "supersede_contract")
	if decision.Admitted || decision.Failure == nil || decision.Failure.Kind != KindInvalidOperation {
		t.Fatalf("archived complete-step correction = %+v, want the archived-family refusal", decision)
	}
}

// TestNoRetainedWorkflowVersionDeclaresAConditionAction holds CD-0234 D1: the
// engine has no condition resolver, so no version of a family outside the
// archived set may declare an action that opens, resolves, or cancels a
// condition. The walk covers every registered version, not only live heads.
func TestNoRetainedWorkflowVersionDeclaresAConditionAction(t *testing.T) {
	t.Parallel()
	if got := retainedConditionActionDeclarations(BuiltinWorkflowDefinitions()); len(got) != 0 {
		t.Fatalf("retained workflow versions declare condition actions: %s", strings.Join(got, ", "))
	}
	// Control: the walk flags a retained version that declares one, so an
	// empty answer above is a finding rather than a blind walk.
	probe := WorkflowDefinition{Ref: "workflow.implementation", StepGraph: WorkflowStepGraph{Steps: []WorkflowStep{{ID: "execution", Actions: []string{"add_condition"}}}}}
	if got := retainedConditionActionDeclarations([]WorkflowDefinition{probe}); len(got) != 1 {
		t.Fatalf("control declarations = %v, want the one retained step declaration", got)
	}
}

func retainedConditionActionDeclarations(definitions []WorkflowDefinition) []string {
	conditionAction := func(id string) bool {
		return id == "add_condition" || id == "resolve_condition" || id == "cancel_condition"
	}
	var found []string
	for _, definition := range definitions {
		if workflowFamilyArchived(definition.Ref) {
			continue
		}
		at := definition.Ref
		for _, action := range definition.AvailableActions {
			if conditionAction(action) {
				found = append(found, at+" available "+action)
			}
		}
		for _, action := range definition.ActionDefinitions {
			if conditionAction(action.ID) {
				found = append(found, at+" defines "+action.ID)
			}
		}
		for _, step := range definition.StepGraph.Steps {
			for _, action := range step.Actions {
				if conditionAction(action) {
					found = append(found, at+" step "+step.ID+" "+action)
				}
			}
		}
	}
	return found
}

func TestLiveAuthoringActionUnionTargets31WithRecovery(t *testing.T) {
	t.Parallel()
	declared := map[string]bool{}
	for _, ref := range []string{"workflow.implementation", "workflow.break_fix", "workflow.research"} {
		registered, err := LiveWorkflowDefinitionForRef(ref)
		if err != nil {
			t.Fatalf("live head %s: %v", ref, err)
		}
		for _, action := range registered.Definition.AvailableActions {
			declared[action] = true
		}
	}
	// The two recovery actions the engine admits off the pinned steps.
	declared["reject_worker_result"] = true
	declared["supersede_contract"] = true
	if len(declared) != 31 {
		names := make([]string, 0, len(declared))
		for name := range declared {
			names = append(names, name)
		}
		sort.Strings(names)
		t.Fatalf("live authoring union holds %d IDs, want the 31 target: %s", len(declared), strings.Join(names, ","))
	}
	retired := []string{
		"accept_decision", "add_condition", "approve_operation", "cancel_condition",
		"checkpoint_action", "checkpoint_analysis", "checkpoint_poc", "checkpoint_run",
		"cleanup_run", "declare_scope", "discard_poc", "frame_question", "record_decision",
		"record_health", "record_option", "record_research", "resolve_condition",
		"rollback_run", "run_analysis", "start_action", "start_poc", "start_run",
		"record_reproduction", "record_root_cause", "start_repair", "start_refine",
		"record_worker_failure", "record_discovery",
	}
	for _, action := range retired {
		if declared[action] {
			t.Fatalf("retired action %s is still authorable on the live heads", action)
		}
	}
	if !declared["record_diagnosis"] || !declared["record_alignment"] {
		t.Fatal("the typed diagnosis and the implementation alignment search must survive on the live heads")
	}
}

func TestCanonicalImplementationV30RemovesDiscoveryKeepsAlignmentBeforeDesign(t *testing.T) {
	t.Parallel()
	registered, err := LiveWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	definition := registered.Definition
	for _, candidate := range definition.StepGraph.Steps {
		if candidate.ID == "discovery" {
			t.Fatal("the discovery marker step is still on the canonical implementation head")
		}
		if slices.Contains(candidate.Actions, "record_discovery") {
			t.Fatalf("step %s still carries the record_discovery marker", candidate.ID)
		}
	}
	if slices.Contains(definition.AvailableActions, "record_discovery") {
		t.Fatal("record_discovery is still advertised on the canonical head")
	}
	for _, action := range definition.ActionDefinitions {
		if action.ID == "record_discovery" {
			t.Fatal("record_discovery is still declared on the canonical head")
		}
	}
	alignment, design := -1, -1
	for index, candidate := range definition.StepGraph.Steps {
		switch candidate.ID {
		case "alignment":
			alignment = index
		case "design":
			design = index
		}
	}
	if alignment == -1 || design == -1 || alignment > design {
		t.Fatalf("alignment/design steps = %d/%d, want alignment before design", alignment, design)
	}
	aligned := false
	for _, edge := range definition.StepGraph.Edges {
		if edge.From == "alignment" && edge.To == "design" && edge.Kind == WorkflowEdgeForward {
			aligned = true
		}
		if edge.From == "discovery" || edge.To == "discovery" {
			t.Fatalf("edge %+v still names the removed discovery step", edge)
		}
	}
	if !aligned {
		t.Fatal("the mandatory alignment step lost its forward edge into design")
	}
	if definition.StepGraph.StartStep != "proposal" {
		t.Fatalf("canonical start step = %q, want proposal", definition.StepGraph.StartStep)
	}
}

func TestCanonicalHeadsAuthorBatchVerdictsOnly(t *testing.T) {
	const workID = "canonical-verdict-batch-only"
	// The current head associates criteria with captured receipts (CD-0242
	// D8): the research fixture selects one sourced finding as that receipt.
	fixture, receipt := seedCriterionAssociationAtStep(t, workID, mustBuiltinDefinition(t, "workflow.research"), "conclude")
	s := fixture.store
	run := func(payload json.RawMessage) error {
		return runVerdictActionAs(t, s, workID, "record_verdict", payload, 0, fixture.owner)
	}

	verdictEvents := func() int64 {
		var count int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowVerdictRecorded).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	before := verdictEvents()

	// The single wire shape refuses, and the refusal writes nothing.
	single := mustJSONValue(map[string]any{"contract_version": 1, "predicate_id": "predicate:first", "receipt_refs": []string{receipt}})
	if err := run(single); !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("canonical single-form verdict = %v, want %s", err, KindInvalidPayload)
	}
	if got := verdictEvents(); got != before {
		t.Fatalf("refused single-form verdict moved verdict events from %d to %d", before, got)
	}
	// A one-entry batch binds the calling Main and the entry's own receipt.
	if err := run(criterionAssociationPayload(receipt, "predicate:first")); err != nil {
		t.Fatalf("canonical one-entry batch: %v", err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := latestWorkflowVerdicts(context.Background(), tx, workID, 1)
	_ = tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	if len(recorded) != 1 {
		t.Fatalf("canonical batch recorded %d verdicts, want 1", len(recorded))
	}
	entry := recorded[0]
	if entry.PredicateID != "predicate:first" || entry.ContractVersion != 1 {
		t.Fatalf("canonical entry = %+v, want the batched contract's own entry", entry)
	}
	if len(entry.EvaluationEvidence) != 1 || entry.EvaluationEvidence[0] != receipt {
		t.Fatalf("canonical entry evidence = %v, want the entry's own receipt", entry.EvaluationEvidence)
	}
	if entry.VerdictActorRef != mustActorRef(t, fixture.owner) {
		t.Fatalf("canonical entry actor = %q, want the calling Main", entry.VerdictActorRef)
	}
	// An empty batch and a batch-less call refuse on the canonical payload.
	if err := run(json.RawMessage(`{"contract_version":1,"verdicts":[]}`)); !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("empty canonical batch = %v, want %s", err, KindInvalidPayload)
	}
	if err := run(json.RawMessage(`{"contract_version":1}`)); !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("canonical call without verdicts = %v, want %s", err, KindInvalidPayload)
	}
}

func TestCanonicalHeadsApproveRecordsExplicitPredicatesWithoutInventedOutcome(t *testing.T) {
	ctx := context.Background()
	const workID = "canonical-approval"
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedIssue31DomainRegistry(t, s)
	registered, err := LiveWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: owner, Now: time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := advanceWorkflowTestInstanceToStep(ctx, s, workID, "planning", ownerRef); err != nil {
		t.Fatal(err)
	}
	approval := map[string]any{
		"contract_version": 1,
		"premise":          "deliver the explicitly checked change",
		"outcome_predicates": []map[string]any{{
			"predicate_id": "predicate:canonical-check", "ordinal": 0, "outcome_kind": "check",
			"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:canonical", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
		}},
		"architecture_binding": WorkflowArchitectureBinding{
			DomainRegistryContentHash: "sha256:" + strings.Repeat("b", 64), HomeDomainID: "root", AffectedDomainIDs: []string{"root"},
			DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{},
			VerificationObligations: []WorkflowVerificationObligation{},
		},
		"decision_domains": []string{},
	}
	encoded, err := json.Marshal(approval)
	if err != nil {
		t.Fatal(err)
	}
	// approve_contract's approval is the contract machinery itself, not an
	// operator actor: the action runs without one, like every live approver.
	version := readWorkVersion(t, s, workID)
	operationID := "canonical-approval-" + workID
	tx2, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applyWorkflowActionRawTx(ctx, tx2, newFoldScope(tx2), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: "approve_contract", Payload: encoded, Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("c", 64), IdempotencyIdentity: operationID, OperationID: operationID,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID,
		RequestID: "request:" + operationID, ContractDigest: testManifestDigest, Now: time.Date(2026, 10, 11, 1, 0, 0, 0, time.UTC),
	}); err != nil {
		_ = tx2.Rollback()
		t.Fatalf("canonical approval: %v", err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, workID, WorkflowContractApproved).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, `"outcome_predicates"`) || !strings.Contains(text, "predicate:canonical-check") {
		t.Fatalf("canonical approval event lost the explicit predicate array: %s", workflowRefExcerpt(text))
	}
	// The engine invents no legacy outcome pair beside the predicates: no
	// synthesized check and no completed token. The per-predicate entries
	// keep their own typed outcome fields; the top-level pair is the
	// invention this assertion refuses.
	var topFields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &topFields); err != nil {
		t.Fatal(err)
	}
	for _, invented := range []string{"outcome_kind", "outcome_payload"} {
		if _, present := topFields[invented]; present {
			t.Fatalf("canonical approval event invented the top-level %s field", invented)
		}
	}
	for _, synthesized := range []string{"check:workflow", `["completed"]`} {
		if strings.Contains(text, synthesized) {
			t.Fatalf("canonical approval event synthesized %s: %s", synthesized, workflowRefExcerpt(text))
		}
	}
	var decoded struct {
		OutcomePredicates []workflowContractPredicatePayload `json:"outcome_predicates"`
		OutcomeKind       string                             `json:"outcome_kind"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.OutcomePredicates) != 1 || decoded.OutcomePredicates[0].PredicateID != "predicate:canonical-check" {
		t.Fatalf("decoded predicates = %+v, want the one explicit check", decoded.OutcomePredicates)
	}
	if decoded.OutcomeKind != "" {
		t.Fatalf("canonical approval recorded outcome_kind %q, want the field absent", decoded.OutcomeKind)
	}
}

func TestCanonicalSupersedeRequiresExplicitOutcomePredicates(t *testing.T) {
	t.Parallel()
	canonical, err := LiveWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	predicates := func() string {
		return `"outcome_predicates":[{"predicate_id":"predicate:successor","ordinal":0,"outcome_kind":"absent","outcome_payload":{"kind":"absent","subjects":["regression"],"surface":"repository_file","distinguish_from":["archived"]}}]`
	}
	base := `{"contract_version":2,"premise":"the corrected successor premise",` +
		`"required_evidence":["verification"],"route_conventions":[],"spec_mandate":[],"law_modifies":[],` +
		`"rigor_class":"prototype_internal","supersede_reason":"correct the accepted premise","audit_evidence":["evidence:canonical-supersede"],`
	// The live head answers consent under consequence_v1, so its successor
	// declares the explicit engineering decision fact.
	live := base + `"decision_domains":[],`
	valid := json.RawMessage(live + predicates() + "}")
	if err := validateWorkflowContractRecoveryPayload(canonical.Definition, valid); err != nil {
		t.Fatalf("canonical successor with explicit predicates: %v", err)
	}
	for name, raw := range map[string]json.RawMessage{
		"outcome pair": json.RawMessage(live + `"outcome_kind":"absent","outcome_payload":{"kind":"absent","subjects":["regression"],"surface":"repository_file","distinguish_from":["archived"]}}`),
		"no outcome":   json.RawMessage(live + "}"),
		"empty array":  json.RawMessage(live + `"outcome_predicates":[]}`),
	} {
		if err := validateWorkflowContractRecoveryPayload(canonical.Definition, raw); !hasFailureKind(err, KindInvalidPayload) {
			t.Fatalf("canonical successor with %s = %v, want %s", name, err, KindInvalidPayload)
		}
	}
}

func TestCanonicalResearchHeadDerivesFailureWithVersionedPacket(t *testing.T) {
	t.Parallel()
	registered, err := LiveWorkflowDefinitionForRef("workflow.research")
	if err != nil {
		t.Fatal(err)
	}
	definition := registered.Definition
	if slices.Contains(definition.AvailableActions, "record_worker_failure") {
		t.Fatal("record_worker_failure is still advertised on the canonical research head")
	}
	for _, action := range definition.ActionDefinitions {
		if action.ID == "record_worker_failure" {
			t.Fatal("record_worker_failure is still declared on the canonical research head")
		}
	}
	if !workflowDerivedWorkerFailureActive(definition) {
		t.Fatal("the canonical research head does not derive the workflow failure record")
	}
	packetRef := ""
	for _, action := range definition.ActionDefinitions {
		if action.ID != "dispatch_worker" {
			continue
		}
		for _, field := range action.Payload.Fields {
			if field.Name == "worker_packet" {
				packetRef = field.SchemaRef
			}
		}
	}
	if packetRef != "worker_packet" {
		t.Fatalf("canonical research dispatch packet ref = %q, want the current packet closure", packetRef)
	}
	// The canonical authoring gate reads the definition's own registry
	// content: the batch-only record_verdict payload every current head
	// declares. A head without that payload is not canonical.
	for _, current := range BuiltinWorkflowDefinitions() {
		if !workflowCanonicalAuthoringActive(current) {
			t.Fatalf("%s is not canonical", current.Ref)
		}
		scalar := cloneWorkflowDefinition(current)
		for i := range scalar.ActionDefinitions {
			if scalar.ActionDefinitions[i].ID == "record_verdict" {
				scalar.ActionDefinitions[i].Payload.Fields = slices.DeleteFunc(scalar.ActionDefinitions[i].Payload.Fields, func(field WorkflowPayloadField) bool { return field.Name == "contract_version" })
			}
		}
		if workflowCanonicalAuthoringActive(scalar) {
			t.Fatalf("%s without the batch-only verdict payload reads as canonical", current.Ref)
		}
	}
}

// seedPinnedInstance opens one throwaway store and initializes a work item
// under the exact registered pin, with the work item still nonterminal — the
// state a serving release leaves behind mid-flight.
func seedPinnedInstance(t *testing.T, path string, workID string, registered RegisteredDefinition) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	defer func() { _ = s.Close() }()
	seedWork(t, s, workID)
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: owner, Now: time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// seedArchivedInstance seeds a nonterminal item whose recorded instance pin
// names an archived family. No archived definition is registered (CD-0242
// D14), so the seed rewrites the current pin's metadata the way an earlier
// binary left it; readiness reads only that recorded pin.
func seedArchivedInstance(t *testing.T, path string) string {
	t.Helper()
	return seedCutoverPin(t, path, "archived-cutover", "workflow.generic_one_off")
}

// seedCutoverPin seeds a nonterminal item on the current implementation head
// and rewrites its recorded instance family ref.
func seedCutoverPin(t *testing.T, path, workID, ref string) string {
	t.Helper()
	seedPinnedInstance(t, path, workID, mustBuiltinDefinition(t, "workflow.implementation"))
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	repinCutoverInstance(t, s, workID, ref)
	return workID
}

func repinCutoverInstance(t *testing.T, s *Store, workID, ref string) {
	t.Helper()
	execStaleRegistryInFold(t, s, fmt.Sprintf(`UPDATE workflow_instances SET definition_ref='%s' WHERE work_id='%s'`, ref, workID))
}

func storedCounts(t *testing.T, path string) (events int64, migrations int64) {
	t.Helper()
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.QueryRow(`SELECT count(*) FROM domain_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&migrations); err != nil {
		t.Fatal(err)
	}
	return events, migrations
}

// TestCutoverRefusesNonterminalArchivedPinAtReadiness proves the readiness
// gate: a store whose workflow_instances row pins an archived family at a
// nonterminal state refuses activation, names the pin, and the refusal reads
// only — no events and no schema manifest rows move.
func TestCutoverRefusesNonterminalArchivedPinAtReadiness(t *testing.T) {
	ctx := context.Background()
	path := copyTestDatabase(t)
	seedArchivedInstance(t, path)
	eventsBefore, migrationsBefore := storedCounts(t, path)

	_, err := PlanUpgradeReadiness(ctx, path)
	failure, ok := err.(*Failure)
	if !ok || failure.Kind != KindSchemaUnsupported {
		t.Fatalf("readiness with a nonterminal archived pin = %v, want %s", err, KindSchemaUnsupported)
	}
	if !strings.Contains(failure.Detail, "workflow.generic_one_off") || !strings.Contains(failure.Detail, "nonterminal") {
		t.Fatalf("cutover refusal %q must name the archived pin and its nonterminal state", failure.Detail)
	}
	eventsAfter, migrationsAfter := storedCounts(t, path)
	if eventsAfter != eventsBefore || migrationsAfter != migrationsBefore {
		t.Fatalf("refused readiness moved rows: events %d->%d, migrations %d->%d; the gate must write nothing", eventsBefore, eventsAfter, migrationsBefore, migrationsAfter)
	}
	// The recorded state stays openable for reads; the refusal
	// gates activation, not the frozen history.
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open over a nonterminal archived pin: %v", err)
	}
	defer func() { _ = s.Close() }()
}

// TestCutoverAllowsTerminalHistoricalPinsAtReadiness proves the gate keys on
// the work item's closed lifecycle: an archived pin whose work item reached a
// terminal lifecycle admits activation and reads.
func TestCutoverAllowsTerminalHistoricalPinsAtReadiness(t *testing.T) {
	ctx := context.Background()
	path := copyTestDatabase(t)
	workID := seedArchivedInstance(t, path)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	execStaleRegistryInFold(t, s, `UPDATE work_items SET lifecycle='cancelled' WHERE id='`+workID+`'`)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := PlanUpgradeReadiness(ctx, path); err != nil {
		t.Fatalf("readiness with a terminal archived pin: %v", err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open with a terminal archived pin: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	var lifecycle, ref string
	if err := reopened.DatabaseForTesting().QueryRow(`SELECT w.lifecycle,i.definition_ref FROM workflow_instances i JOIN work_items w ON w.id=i.work_id WHERE i.work_id=?`, workID).Scan(&lifecycle, &ref); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "cancelled" || ref != "workflow.generic_one_off" {
		t.Fatalf("reopened instance = %s/%s, want the terminal archived pin readable", lifecycle, ref)
	}
}

// TestCutoverAdmitsEarlierLiveFamilyPinAtReadiness proves an earlier
// recorded version of a live family does not block activation: no earlier
// definition is retained, and active work continues under the family's
// current definition (CD-0242 D13, D14). The recorded state opens.
func TestCutoverAdmitsEarlierLiveFamilyPinAtReadiness(t *testing.T) {
	ctx := context.Background()
	path := copyTestDatabase(t)
	seedCutoverPin(t, path, "earlier-live-pin", "workflow.implementation")
	if _, err := PlanUpgradeReadiness(ctx, path); err != nil {
		t.Fatalf("readiness with an earlier live-family pin: %v", err)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open over an earlier live-family pin: %v", err)
	}
	defer func() { _ = s.Close() }()
}

// TestCutoverBlocksOutsideRepairArchivedPin proves an outside-repair
// disposition is a held state, not a terminal one: an archived pin under an
// outside-repair instance still blocks activation while the work item stays
// nonterminal.
func TestCutoverBlocksOutsideRepairArchivedPin(t *testing.T) {
	ctx := context.Background()
	path := copyTestDatabase(t)
	workID := seedCutoverPin(t, path, "outside-repair-cutover", "workflow.ops_runbook")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	execStaleRegistryInFold(t, s, `UPDATE workflow_instances SET instance_state='outside_repair' WHERE work_id='`+workID+`'`)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = PlanUpgradeReadiness(ctx, path)
	failure, ok := err.(*Failure)
	if !ok || failure.Kind != KindSchemaUnsupported {
		t.Fatalf("readiness with an outside-repair archived pin = %v, want %s", err, KindSchemaUnsupported)
	}
	if !strings.Contains(failure.Detail, "workflow.ops_runbook version 9") {
		t.Fatalf("cutover refusal %q must name the held archived pin", failure.Detail)
	}
}

// TestCutoverAllowsExactCanonicalHeadsAtReadiness proves the gate leaves the
// current authoring surface live: a store whose nonterminal work items pin
// the exact canonical heads admits activation.
func TestCutoverAllowsExactCanonicalHeadsAtReadiness(t *testing.T) {
	ctx := context.Background()
	path := copyTestDatabase(t)
	for _, head := range BuiltinWorkflowDefinitions() {
		seedPinnedInstance(t, path, "canonical-"+strings.TrimPrefix(head.Ref, "workflow."), mustBuiltinDefinition(t, head.Ref))
	}
	if _, err := PlanUpgradeReadiness(ctx, path); err != nil {
		t.Fatalf("readiness with exact canonical heads: %v", err)
	}
}

// TestResearchDerivedFailureRetriesAndRebuilds executes the research failure
// extension end to end: a dispatched investigate attempt's terminal
// worker.failed event derives exactly one workflow failure record at the
// step, the correction context reads the original detail, a fresh retry
// dispatch opens the next fenced epoch, and the log rebuild reproduces the
// derived record and its correction.
func TestResearchDerivedFailureRetriesAndRebuilds(t *testing.T) {
	const workID = "research-derived-failure-retry"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.research", "investigate")
	s := fixture.store
	dispatcher := diagnosisS1Actor(workID, "dispatcher")
	lane := reviewGateLane(t, "research")
	// The research step dispatches its own lane's packet; research declares
	// no worker-job lifecycle, so the dispatch carries the packet alone.
	dispatchResearchAttempt := func(attemptID, key string, correction *WorkflowCorrectionContext) {
		task, binding := recordedPacketInputs(t, s, workID, lane.ID)
		inputs := map[string]any{"task": task, "binding": binding, "constraints": []string{"do-not-modify-product-truth"}}
		if correction != nil {
			inputs["correction"] = correction
		}
		for member, value := range recordedPacketRecords(t, s, workID) {
			inputs[member] = value
		}
		packet := map[string]any{
			"schema_version": WorkerPacketSchemaVersion, "attempt_id": attemptID,
			"lane_id": lane.ID, "lane_digest": lane.Digest,
			"work_id": workID, "step_id": "investigate", "inputs": inputs,
		}
		if err := dispatchJobPacketForTest(t, s, workID, attemptID, dispatcher, key, packet); err != nil {
			t.Fatal(err)
		}
	}
	// The dispatch action's durable worker record materializes the attempt
	// the terminal event names, under the same lane identity.
	recordResearchDispatch := func(attemptID string) {
		ownerRef, err := WorkflowActorRef(fixture.owner)
		if err != nil {
			t.Fatal(err)
		}
		window, err := findAuthorizedDispatchWindowCore(context.Background(), s.DatabaseForTesting(), workID, attemptID)
		if err != nil {
			t.Fatal(err)
		}
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{{
			EventID: "research-worker-dispatch-" + workID + "-" + attemptID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID,
			Actor: ownerRef, OccurredAt: time.Unix(20, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion(WorkerDispatched),
			Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion, PacketDigest: window.PacketDigest}),
		}}}); err != nil {
			t.Fatal(err)
		}
	}
	attempt := "attempt:" + workID + ":1"
	dispatchResearchAttempt(attempt, "research-failure-dispatch-"+workID, nil)
	recordResearchDispatch(attempt)
	if starts := workflowStartedEventsAtStep(t, s, workID, "investigate"); len(starts) != 1 || starts[0].Epoch != 1 {
		t.Fatalf("investigate starts after the dispatch = %+v, want one dispatch start at epoch 1", starts)
	}
	detail := "the research lane failed before a judgeable report existed"
	// The research failure ingest carries the recorded observation the
	// promoted pin requires; the derivation it proves is unchanged.
	if err := s2WorkerFailedLive(t, s, s2WorkerFailedAttributedEvent(workID, attempt, "failed-"+workID+"-1", preferredModelForLane(lane), WorkerFailureWorkerError, detail, time.Unix(30, 0).UTC())); err != nil {
		t.Fatalf("the live terminal worker.failed ingest: %v", err)
	}
	records := s2DerivedFailureRecords(t, s, workID, attempt)
	if len(records) != 1 {
		t.Fatalf("derived failure records = %d, want exactly one from the original terminal event", len(records))
	}
	record := records[0]
	if record["step"] != "investigate" || record["epoch"] != int64(1) {
		t.Fatalf("derived record = %+v, want the investigate step at the window's epoch 1", record)
	}
	if record["actor"] != mustActorRef(t, dispatcher) {
		t.Fatalf("derived record actor = %v, want the dispatch authorization's actor", record["actor"])
	}
	if !strings.HasSuffix(record["event_id"].(string), ":workflow-failure") {
		t.Fatalf("derived record event id = %v, want the terminal event's derivation pair", record["event_id"])
	}
	correction, err := workflowCorrectionContext(context.Background(), s.db, workID, "investigate")
	if err != nil {
		t.Fatal(err)
	}
	if correction == nil || correction.Disposition != "failed" || correction.Diagnosis != detail || correction.Strategy != "retry with a fresh fenced attempt" || correction.FailedAttemptID != attempt {
		t.Fatalf("correction after the derived record = %+v, want the failed disposition carrying the original failure detail", correction)
	}

	// The failed attempt holds the step through the wall: a fresh dispatch is
	// the admitted retry, consumes the derived correction context, and opens
	// the next fenced epoch.
	retryCorrection, retryCorrectionErr := workflowCorrectionContext(context.Background(), s.db, workID, "investigate")
	if retryCorrectionErr != nil {
		t.Fatal(retryCorrectionErr)
	}
	retry := "attempt:" + workID + ":2"
	dispatchResearchAttempt(retry, "research-retry-dispatch-"+workID, retryCorrection)
	recordResearchDispatch(retry)
	starts := workflowStartedEventsAtStep(t, s, workID, "investigate")
	if len(starts) != 2 || starts[1].Epoch != 2 {
		t.Fatalf("investigate starts after the retry = %+v, want the second dispatch at epoch 2", starts)
	}

}

// insertCutoverContract copies the seeded instance's own definition pin into
// the work's active contract, the shape an approval leaves behind, under the
// fold guard the fold-only contract table requires.
func insertCutoverContract(t *testing.T, s *Store, workID string) {
	t.Helper()
	execStaleRegistryInFold(t, s, `INSERT INTO workflow_contracts
		(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,definition_ref,definition_digest)
		SELECT i.work_id,1,'cutover contract pin','internal_sqlite','[]','[]','2026-10-11T00:00:00Z',
		(SELECT actor_ref FROM workflow_actors LIMIT 1),'[]',i.definition_ref,i.definition_digest
		FROM workflow_instances i WHERE i.work_id='`+workID+`'`)
}

// insertCutoverSuccessorContract adds the next contract version pinned to the
// given definition, the slot supersession points the old row at.
func insertCutoverSuccessorContract(t *testing.T, s *Store, workID string, contractVersion int64, ref, digest string) {
	t.Helper()
	execStaleRegistryInFold(t, s, `INSERT INTO workflow_contracts
		(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,definition_ref,definition_digest)
		VALUES('`+workID+`',`+fmt.Sprintf("%d", contractVersion)+`,'cutover successor pin','internal_sqlite','[]','[]','2026-10-11T00:00:00Z',
		(SELECT actor_ref FROM workflow_actors LIMIT 1),'[]','`+ref+`','`+digest+`')`)
}

// supersedeCutoverContract closes contract v1 the way the supersede
// projection does: the old row points at the successor's version.
func supersedeCutoverContract(t *testing.T, s *Store, workID string) {
	t.Helper()
	execStaleRegistryInFold(t, s, `UPDATE workflow_contracts SET superseded_by=2 WHERE work_id='`+workID+`' AND contract_version=1`)
}

// TestCutoverRefusesActiveArchivedContractBesideCanonicalInstance proves the
// gate reads the work's active contract pin, not only its instance: the
// instance holds the canonical head while the active contract still pins an
// archived family, and that store refuses activation without writing
// anything.
func TestCutoverRefusesActiveArchivedContractBesideCanonicalInstance(t *testing.T) {
	ctx := context.Background()
	path := copyTestDatabase(t)
	workID := "archived-contract-repin"
	canonical := mustBuiltinDefinition(t, "workflow.implementation")
	seedCutoverPin(t, path, workID, "workflow.ops_runbook")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	insertCutoverContract(t, s, workID)
	repinCutoverInstance(t, s, workID, canonical.Definition.Ref)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	eventsBefore, migrationsBefore := storedCounts(t, path)

	_, err = PlanUpgradeReadiness(ctx, path)
	failure, ok := err.(*Failure)
	if !ok || failure.Kind != KindSchemaUnsupported {
		t.Fatalf("readiness with an active archived contract beside the canonical instance = %v, want %s", err, KindSchemaUnsupported)
	}
	if !strings.Contains(failure.Detail, "workflow.ops_runbook version 9") || !strings.Contains(failure.Detail, "is archived") {
		t.Fatalf("cutover refusal %q must name the active contract's archived pin", failure.Detail)
	}
	eventsAfter, migrationsAfter := storedCounts(t, path)
	if eventsAfter != eventsBefore || migrationsAfter != migrationsBefore {
		t.Fatalf("refused readiness moved rows: events %d->%d, migrations %d->%d; the gate must write nothing", eventsBefore, eventsAfter, migrationsBefore, migrationsAfter)
	}
	// The read-only gate answers activation only; the recorded state opens,
	// and reads.
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("open over an active archived contract: %v", err)
	}
	defer func() { _ = s.Close() }()
}

// TestCutoverAllowsSupersededArchivedContractAtReadiness proves the contract
// half keys on supersession: the same archived pin, once superseded by the
// canonical successor beside a canonical instance, stops blocking activation.
func TestCutoverAllowsSupersededArchivedContractAtReadiness(t *testing.T) {
	ctx := context.Background()
	path := copyTestDatabase(t)
	workID := "superseded-contract"
	canonical := mustBuiltinDefinition(t, "workflow.implementation")
	seedPinnedInstance(t, path, workID, canonical)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	insertCutoverSuccessorContract(t, s, workID, 1, "workflow.ops_runbook", canonical.Digest)
	insertCutoverSuccessorContract(t, s, workID, 2, canonical.Definition.Ref, canonical.Digest)
	supersedeCutoverContract(t, s, workID)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanUpgradeReadiness(ctx, path); err != nil {
		t.Fatalf("readiness with the archived contract superseded: %v", err)
	}
}

// TestCutoverRefusesArchivedInstanceWhenContractSuperseded proves superseding
// the contract cannot unblock the instance half: the archived family's
// nonterminal instance still refuses activation on its own pin.
func TestCutoverRefusesArchivedInstanceWhenContractSuperseded(t *testing.T) {
	ctx := context.Background()
	path := copyTestDatabase(t)
	workID := seedArchivedInstance(t, path)
	canonical := mustBuiltinDefinition(t, "workflow.implementation")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	insertCutoverContract(t, s, workID)
	insertCutoverSuccessorContract(t, s, workID, 2, canonical.Definition.Ref, canonical.Digest)
	supersedeCutoverContract(t, s, workID)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = PlanUpgradeReadiness(ctx, path)
	failure, ok := err.(*Failure)
	if !ok || failure.Kind != KindSchemaUnsupported {
		t.Fatalf("readiness with a superseded contract under an archived instance = %v, want %s", err, KindSchemaUnsupported)
	}
	if !strings.Contains(failure.Detail, "workflow.generic_one_off") || !strings.Contains(failure.Detail, "nonterminal") {
		t.Fatalf("cutover refusal %q must still name the archived instance's pin", failure.Detail)
	}
}

// TestCutoverRefusesActiveArchivedContractWithoutInstance proves the contract
// pin stands alone: with the instance row gone, the work's active archived
// contract still refuses activation, and the gate stays read-only.
func TestCutoverRefusesActiveArchivedContractWithoutInstance(t *testing.T) {
	ctx := context.Background()
	path := copyTestDatabase(t)
	workID := seedCutoverPin(t, path, "contract-only-cutover", "workflow.ops_runbook")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	insertCutoverContract(t, s, workID)
	execStaleRegistryInFold(t, s, `DELETE FROM workflow_instances WHERE work_id='`+workID+`'`)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = PlanUpgradeReadiness(ctx, path)
	failure, ok := err.(*Failure)
	if !ok || failure.Kind != KindSchemaUnsupported {
		t.Fatalf("readiness with an active archived contract and no instance = %v, want %s", err, KindSchemaUnsupported)
	}
	if !strings.Contains(failure.Detail, "workflow.ops_runbook version 9") {
		t.Fatalf("cutover refusal %q must name the contract-only pin", failure.Detail)
	}
	// The refusal gates activation, not the recorded state.
	opened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open over a contract-only archived pin: %v", err)
	}
	defer func() { _ = opened.Close() }()
}

func seedCutoverDispositionFixture(t *testing.T) *Store {
	t.Helper()
	path := copyTestDatabase(t)
	canonical, err := LiveWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a-archived", "b-contract-only", "c-canonical", "d-terminal", "e-superseded-contract"} {
		seedPinnedInstance(t, path, id, canonical)
	}
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	repinCutoverInstance(t, s, "a-archived", "workflow.generic_one_off")
	repinCutoverInstance(t, s, "b-contract-only", "workflow.ops_runbook")
	repinCutoverInstance(t, s, "d-terminal", "workflow.generic_one_off")
	insertCutoverContract(t, s, "a-archived") // Two blocking surfaces still select one work.
	insertCutoverContract(t, s, "b-contract-only")
	execStaleRegistryInFold(t, s, `DELETE FROM workflow_instances WHERE work_id='b-contract-only'`)
	insertCutoverSuccessorContract(t, s, "e-superseded-contract", 1, "workflow.ops_runbook", canonical.Digest)
	insertCutoverSuccessorContract(t, s, "e-superseded-contract", 2, canonical.Definition.Ref, canonical.Digest)
	supersedeCutoverContract(t, s, "e-superseded-contract")
	terminal, err := cutoverReadWork(context.Background(), s, "d-terminal")
	if err != nil {
		t.Fatal(err)
	}
	if err := applyWorkEvent(t, s, workTransitionEvent("terminal-history", "d-terminal", terminal.lifecycle, "cancelled", terminal.version, terminal.version+1), workVersion("d-terminal", terminal.version)); err != nil {
		t.Fatal(err)
	}
	return s
}

func cutoverTableSnapshot(t *testing.T, s *Store, table string) string {
	t.Helper()
	rows, err := s.db.Query("SELECT * FROM " + table + " ORDER BY 1,2") //nolint:gosec // test-owned closed table names.
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var snapshot [][]any
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		snapshot = append(snapshot, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return string(mustJSONValue(snapshot))
}

func cutoverReadWork(ctx context.Context, s *Store, id string) (workProjection, error) {
	tx, err := beginReadTx(ctx, s.db)
	if err != nil {
		return workProjection{}, err
	}
	defer func() { _ = tx.Rollback() }()
	return readWork(ctx, tx, id)
}

func TestCutoverDispositionSelectsAndCancelsExactlyBlockedWork(t *testing.T) {
	ctx := context.Background()
	s := seedCutoverDispositionFixture(t)
	archived, err := cutoverReadWork(ctx, s, "a-archived")
	if err != nil {
		t.Fatal(err)
	}
	if err := applyWorkEvent(t, s, relationAddedEvent("cutover-relation", "depends_on", "a-archived", "b-contract-only", archived.version, archived.version+1), workVersion("a-archived", archived.version)); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, id := range []string{"a-archived", "b-contract-only"} {
		current, err := cutoverReadWork(ctx, s, id)
		if err != nil {
			t.Fatal(err)
		}
		claim := operationEvent("claim-"+id, "work.resource_claimed", SubjectWorkItem, id, map[string]any{
			"work_id": id, "resource_key": "fence:" + id, "reason": "cutover fixture", "holder_agent": "agent:test", "holder_session": "session:test",
			"expected_version": current.version, "resulting_version": current.version + 1,
		})
		if err := applyWorkEvent(t, s, claim, workVersion(id, current.version)); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		file := filepath.Join(dir, "retained.txt")
		if err := os.WriteFile(file, []byte(id), 0o600); err != nil {
			t.Fatal(err)
		}
		files[file] = id
		if _, err := s.db.Exec(`INSERT INTO worktree_claims(op_id,work_id,project_id,set_id,repository_id,pinned_branch,pinned_base_sha,pinned_path,state,principal_ref,request_id,observed_at,updated_at)
			VALUES(?,?,'project',?,'synthetic-repository',? ,?,?,'verified','operator',?,'now','now')`, "wt-"+id, id, WorktreeSetID(id), "work/"+id, strings.Repeat("a", 40), dir, "req-"+id); err != nil {
			t.Fatal(err)
		}
		execStaleRegistryInFold(t, s, fmt.Sprintf(`INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,git_facts)
			VALUES('%s','project','wt-%s','work/%s','%s','%s','synthetic-repository','active','now','{}')`, WorktreeSetID(id), id, id, strings.Repeat("a", 40), dir))
	}
	// Research content stays; only the cancelled work's active bindings drop.
	execStaleRegistryInFold(t, s, `UPDATE work_items SET kind='research' WHERE id='c-canonical'`)
	pack, err := createResearchPack(ctx, s, CreateResearchPackRequest{OwnerWorkID: "c-canonical", Revision: ResearchRevisionInput{Question: "cutover fixture", ScopeIn: json.RawMessage(`{}`), ScopeOut: json.RawMessage(`{}`), DoneWhen: json.RawMessage(`{}`), Method: "source"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a-archived", "b-contract-only"} {
		pinResearch(t, s, id, ResearchBindingDeclaration{PackID: pack.PackID, Revision: 1, UseRole: UseContext, Required: true})
	}
	before := map[string]workProjection{}
	for _, id := range []string{"a-archived", "b-contract-only", "c-canonical", "d-terminal", "e-superseded-contract"} {
		before[id], err = cutoverReadWork(ctx, s, id)
		if err != nil {
			t.Fatal(err)
		}
	}
	untouched := map[string]string{}
	for _, table := range []string{"relations", "work_messages", "workflow_contracts", "workflow_checkpoints", "worktree_claims", "worktree_entries", "linear_issue_links", "active_research_revisions"} {
		untouched[table] = cutoverTableSnapshot(t, s, table)
	}
	eventsBefore := countRows(t, s, "domain_events")
	options := UpgradeOptions{CancelCutoverPinnedWork: true}
	plan, err := PlanUpgradeReadiness(ctx, s.Path(), options)
	if err != nil {
		t.Fatal(err)
	}
	want := &CutoverDisposition{WorkCount: 2, WorkIDs: []string{"a-archived", "b-contract-only"}, ByLifecycle: map[string]int{"needed": 2}, ByPinnedFamily: map[string]int{"workflow.generic_one_off": 1, "workflow.ops_runbook": 1}}
	if plan.ActivationBlocked || !reflect.DeepEqual(plan.CutoverDisposition, want) {
		t.Fatalf("cutover plan = %+v, want %+v", plan, want)
	}
	if countRows(t, s, "domain_events") != eventsBefore {
		t.Fatal("plan appended an event")
	}
	report, err := Upgrade(ctx, s.Path(), nil, options)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(report.CutoverDisposition, want) {
		t.Fatalf("disposition = %+v, want %+v", report.CutoverDisposition, want)
	}
	if countRows(t, s, "domain_events") != eventsBefore+2 {
		t.Fatal("disposition must append exactly two events")
	}
	for id, prior := range before {
		current, err := cutoverReadWork(ctx, s, id)
		if err != nil {
			t.Fatal(err)
		}
		if id != "a-archived" && id != "b-contract-only" {
			if !reflect.DeepEqual(current, prior) {
				t.Fatalf("spared work %s changed: %+v -> %+v", id, prior, current)
			}
			continue
		}
		if current.lifecycle != "cancelled" || current.version != prior.version+1 {
			t.Fatalf("cancelled work %s = %+v", id, current)
		}
		var kind, actor, raw string
		if err := s.db.QueryRow(`SELECT kind,actor,payload FROM domain_events WHERE event_id=?`, fmt.Sprintf("upgrade-cutover-cancel:%s:%d", id, prior.version)).Scan(&kind, &actor, &raw); err != nil {
			t.Fatal(err)
		}
		var payload workTransitionPayload
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			t.Fatal(err)
		}
		if kind != "work.transitioned" || actor != "operator" || payload.From != prior.lifecycle || payload.To != "cancelled" || payload.ExpectedVersion != prior.version || payload.ResultingVersion != prior.version+1 || !strings.Contains(payload.Reason, "CD-0218 D5 cutover disposition") {
			t.Fatalf("cancellation event %s/%s/%s", kind, actor, raw)
		}
	}
	var state string
	if err := s.db.QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id='a-archived'`).Scan(&state); err != nil || state != "cancelled" {
		t.Fatalf("archived instance = %s, %v", state, err)
	}
	for table, prior := range untouched {
		if after := cutoverTableSnapshot(t, s, table); after != prior {
			t.Fatalf("cancellation changed %s: %s -> %s", table, prior, after)
		}
	}
	if countRows(t, s, "active_research_consumers") != 0 {
		t.Fatal("cancelled consumers still bind research")
	}
	var held int
	if err := s.db.QueryRow(`SELECT count(*) FROM resource_claims WHERE state='held'`).Scan(&held); err != nil || held != 0 {
		t.Fatalf("held resources = %d, %v", held, err)
	}
	for file, want := range files {
		content, err := os.ReadFile(file)
		if err != nil || string(content) != want {
			t.Fatalf("worktree file changed: %q, %v", content, err)
		}
	}
	if _, err := PlanUpgradeReadiness(ctx, s.Path()); err != nil {
		t.Fatalf("readiness after disposition: %v", err)
	}
}

func TestCutoverDispositionOutsideRepairRefusalRollsBackEveryCancellation(t *testing.T) {
	ctx := context.Background()
	s := seedCutoverDispositionFixture(t)
	// The held item sorts second, so the first cancellation must roll back.
	execStaleRegistryInFold(t, s, `INSERT INTO outside_repair_dispositions(work_id,approval_ref,state,reason,evidence_json,recorded_at)
		VALUES('b-contract-only','approval:hold','active','synthetic outside repair','{}','now')`)
	before := cutoverTableSnapshot(t, s, "work_items")
	eventsBefore := countRows(t, s, "domain_events")
	options := UpgradeOptions{CancelCutoverPinnedWork: true}
	plan, err := PlanUpgradeReadiness(ctx, s.Path(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.ActivationBlocked || len(plan.Blockers) != 1 || !strings.Contains(plan.Blockers[0], "b-contract-only") || !strings.Contains(plan.Blockers[0], "outside-repair") {
		t.Fatalf("held-item plan = %+v", plan)
	}
	_, err = Upgrade(ctx, s.Path(), nil, options)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindOutsideRepairActive || !strings.Contains(err.Error(), "b-contract-only") {
		t.Fatalf("held-item disposition = %v", err)
	}
	if after := cutoverTableSnapshot(t, s, "work_items"); after != before || countRows(t, s, "domain_events") != eventsBefore {
		t.Fatal("atomic refusal changed lifecycle, version, or event log")
	}
}

func TestCutoverDispositionRechecksGateBeforeCommit(t *testing.T) {
	ctx := context.Background()
	s := seedCutoverDispositionFixture(t)
	before := cutoverTableSnapshot(t, s, "work_items")
	eventsBefore := countRows(t, s, "domain_events")
	// A synthetic fold defect leaves a selected item nonterminal. The final
	// gate must refuse and roll back all events, not report a partial cutover.
	if _, err := s.db.Exec(`CREATE TRIGGER cutover_incomplete_fold AFTER UPDATE OF lifecycle ON work_items
		WHEN NEW.id='b-contract-only' AND NEW.lifecycle='cancelled'
		BEGIN UPDATE work_items SET lifecycle='needed' WHERE id=NEW.id; END`); err != nil {
		t.Fatal(err)
	}
	_, err := Upgrade(ctx, s.Path(), nil, UpgradeOptions{CancelCutoverPinnedWork: true})
	if err == nil || !strings.Contains(err.Error(), "b-contract-only") {
		t.Fatalf("incomplete disposition must refuse at the re-check: %v", err)
	}
	if after := cutoverTableSnapshot(t, s, "work_items"); after != before || countRows(t, s, "domain_events") != eventsBefore {
		t.Fatal("incomplete disposition committed a partial cutover")
	}
}
