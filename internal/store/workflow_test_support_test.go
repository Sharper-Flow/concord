package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

const testManifestDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

func ReadWorkflowProjection(ctx context.Context, s *Store, request WorkflowReadRequest) (WorkflowReadProjection, error) {
	var out WorkflowReadProjection
	if len(request.WorkID) < 2 || len(request.WorkID) > 128 {
		return out, newFailure(KindInvalidOperation, "workflow_read", "work ID is out of bounds", false, "supply one bounded work ID")
	}
	var err error
	err = s.readSnapshot(ctx, func(q queryer) error {
		out, err = readWorkflowProjectionTx(ctx, q, request)
		return err
	})
	if err == nil && out.WorkID == "" {
		return out, newFailure(KindProjectionNotFound, "workflow_read", "workflow instance is not recorded", false, "reread_entities")
	}
	return out, err
}

// workflowFixtureRef names the test-only workflow family that fold, projection,
// and supersession fixtures pin. It carries the implementation step graph so
// step IDs stay familiar, and a work kind without Product-truth authority so a
// contract fixture stays about the mechanism under test rather than about
// architecture binding. The worker fixture is a separate synthetic family;
// neither family retains another definition.
const workflowFixtureRef = "workflow.test_fixture"
const workflowWorkerFixtureRef = "workflow.worker_test_fixture"

// workflowFixtureWorkKind is the payload spelling of the fixture family's work
// kind.
const workflowFixtureWorkKind = "static_analysis"

// workflowFixtureVersions and workflowEvidenceGapRegistration hold the
// test-only definitions registerTestWorkflowDefinitions pins.
var (
	workflowFixtureVersions         []RegisteredDefinition
	workflowEvidenceGapRegistration RegisteredDefinition
)

// registerTestWorkflowDefinitions pins the test-only workflow definitions into
// the builtin registry. TestMain calls it before any test runs, so
// cross-process race workers, which re-enter the binary without running a
// helper, resolve the same pins as their parent.
func registerTestWorkflowDefinitions() {
	workflowFixtureVersions = registerWorkflowFixtureFamily()
	workflowEvidenceGapRegistration = registerWorkflowEvidenceGapDefinition()
}

func registerWorkflowFixtureFamily() []RegisteredDefinition {
	registry := BuiltinWorkflowRegistry()
	registered := make([]RegisteredDefinition, 0, 2)
	for _, definition := range []WorkflowDefinition{
		workflowFixtureShape(workflowFixtureBase(), 1),
		workflowFixtureWorkerDefinition(),
	} {
		entry, err := registry.Register(definition)
		if err != nil {
			panic(err)
		}
		registered = append(registered, entry)
	}
	return registered
}

func workflowFixtureWorkerDefinition() WorkflowDefinition {
	definition := workflowFixtureShape(workflowFixtureWorkerActions(workflowFixtureBase()), 2)
	definition.Ref = workflowWorkerFixtureRef
	return definition
}

func workflowFixtureWorkerActions(definition WorkflowDefinition) WorkflowDefinition {
	for _, actionID := range []string{"accept_worker_result", "record_worker_failure", "dispatch_worker"} {
		definition.AvailableActions = append(definition.AvailableActions, actionID)
		action := currentActionDefinition(actionID, false)
		action.RequiredCapability = "work_transition"
		if actionID == "dispatch_worker" {
			action.RequiredCapability = WorkerDispatchCapability
		}
		definition.ActionDefinitions = append(definition.ActionDefinitions, action)
		for i := range definition.StepGraph.Steps {
			if definition.StepGraph.Steps[i].Kind == WorkflowStepExternalEffect {
				definition.StepGraph.Steps[i].Actions = append(definition.StepGraph.Steps[i].Actions, actionID)
			}
		}
	}
	return definition
}

func workflowFixtureDefinition(t *testing.T, version int64) RegisteredDefinition {
	t.Helper()
	if version < 1 || version > int64(len(workflowFixtureVersions)) {
		t.Fatalf("workflow fixture v%d is not registered", version)
	}
	return workflowFixtureVersions[version-1]
}

func workflowFixtureShape(definition WorkflowDefinition, version int64) WorkflowDefinition {
	definition.Ref = workflowFixtureRef
	definition.Version = version
	definition.WorkKind = WorkKindStaticAnalysis
	changesProductTruth := false
	definition.ChangesProductTruth = &changesProductTruth
	// The fixture keeps the implementation step graph, so it must also keep
	// the recovery-route table that graph owns. A table-less evaluator
	// definition refuses at registration.
	definition.RecoveryRoutes = mustCompileWorkflowComposition(currentImplementationComposition()).RecoveryRoutes
	return definition
}

func workflowFixtureDigest(t *testing.T) string {
	t.Helper()
	return workflowFixtureDefinition(t, 1).Digest
}

// seedTerminalInstanceForTesting marks a fixture's workflow instance terminal
// without running the ordered completion gate: the state a stranded or
// historical item carries when its lifecycle completes outside the workflow
// (CD-0183 D4 admits this repair). A completed instance proves the gate ran;
// cancelled leaves the lifecycle-side decision gate in force.
func seedTerminalInstanceForTesting(t *testing.T, s *Store, workID, state string) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE workflow_instances SET instance_state=?,completed_at='2026-08-19T00:00:00Z' WHERE work_id=? AND instance_state NOT IN ('completed','cancelled','superseded'); DELETE FROM fold_guard`, state, workID); err != nil {
		t.Fatal(err)
	}
}

// testApprovalPayload fills the approval defaults a test leaves out. A
// consequence_v1 pin's approval declares the engineering decision-domain
// fact. It reads the pin through s, so call it outside any open transaction.
func testApprovalPayload(t testing.TB, s *Store, workID, actionID string, payload json.RawMessage) json.RawMessage {
	t.Helper()
	if actionID == "record_proposal" {
		var fields map[string]any
		if json.Unmarshal(payload, &fields) == nil && len(fields) == 0 {
			return json.RawMessage(`{"problem":"The bounded problem statement.","affected":["The affected system."],"stakes":"The bounded stakes statement.","user_outcomes":["The expected user outcome."]}`)
		}
	}
	if actionID != "approve_contract" {
		return payload
	}
	var fields map[string]any
	if json.Unmarshal(payload, &fields) != nil || fields == nil {
		return payload
	}
	if _, present := fields["outcome_predicates"]; !present {
		fields["outcome_predicates"] = []map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:test", "immutable_subject_ref": "commit:" + strings.Repeat("a", 64), "expected_result": "pass"}}}
	}
	if _, present := fields["premise"]; !present {
		fields["premise"] = "The operator stated this premise."
	}
	if _, present := fields["decision_domains"]; !present && workflowApprovalPolicyRequiresDecisionFact(pinnedWorkflowDefinitionForTest(t, s, workID)) {
		fields["decision_domains"] = []string{}
	}
	encoded, _ := json.Marshal(fields)
	return encoded
}

// applyWorkflowTestOperation models the owning workflow route for white-box
// fold tests. Production callers must use the dispatcher or initialization and
// completion entry points; generic ApplyOperation intentionally rejects the
// reserved event families.
func applyWorkflowTestOperation(ctx context.Context, s *Store, operation Operation) error {
	for index, event := range operation.Events {
		if event.Kind != WorkflowActionStarted {
			continue
		}
		if index > 0 {
			var versionBefore int64
			if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, event.SubjectID).Scan(&versionBefore); err != nil {
				return applyWorkflowTestOperationDirect(ctx, s, operation)
			}
			prefix := Operation{Events: operation.Events[:index], ExpectedVersions: operation.ExpectedVersions}
			if err := applyWorkflowTestOperation(ctx, s, prefix); err != nil {
				return err
			}
			var versionAfterPrefix int64
			if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, event.SubjectID).Scan(&versionAfterPrefix); err != nil {
				return err
			}
			prefixEventsForSubject := int64(0)
			for _, prefixEvent := range prefix.Events {
				if prefixEvent.SubjectID == event.SubjectID {
					prefixEventsForSubject++
				}
			}
			shift := versionAfterPrefix - versionBefore - prefixEventsForSubject
			suffix := make([]Event, len(operation.Events)-index)
			copy(suffix, operation.Events[index:])
			for i := range suffix {
				if suffix[i].SubjectID == event.SubjectID {
					suffix[i] = shiftWorkflowTestEventVersions(suffix[i], shift)
				}
			}
			expected := make(map[SubjectRef]int64, len(operation.ExpectedVersions))
			for subject, expectedVersion := range operation.ExpectedVersions {
				expected[subject] = expectedVersion
				if subject == VersionRef(SubjectWorkItem, event.SubjectID) {
					expected[subject] = versionAfterPrefix
				}
			}
			return applyWorkflowTestOperation(ctx, s, Operation{Events: suffix, ExpectedVersions: expected})
		}
		var currentStep, definitionRef string
		var definitionVersion, versionBefore int64
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT current_step,definition_ref,definition_version,(SELECT version FROM work_items WHERE id=workflow_instances.work_id) FROM workflow_instances WHERE work_id=?`, event.SubjectID).Scan(&currentStep, &definitionRef, &definitionVersion, &versionBefore); err != nil {
			continue
		}
		entry, ok := BuiltinWorkflowRegistry().Lookup(definitionRef, definitionVersion)
		if !ok {
			continue
		}
		var payload struct {
			StepID   string `json:"step_id"`
			ActionID string `json:"action_id"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil || payload.StepID == currentStep || !definitionStepAllows(entry.Definition, payload.StepID, payload.ActionID) {
			continue
		}

		if err := advanceWorkflowTestInstanceToStep(ctx, s, event.SubjectID, payload.StepID, event.Actor); err != nil {
			return err
		}
		var versionAfter int64
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, event.SubjectID).Scan(&versionAfter); err != nil {
			return err
		}
		shift := versionAfter - versionBefore
		suffix := make([]Event, len(operation.Events)-index)
		copy(suffix, operation.Events[index:])
		for i := range suffix {
			if suffix[i].SubjectID == event.SubjectID {
				suffix[i] = shiftWorkflowTestEventVersions(suffix[i], shift)
			}
		}
		expected := make(map[SubjectRef]int64, len(operation.ExpectedVersions))
		for subject, expectedVersion := range operation.ExpectedVersions {
			expected[subject] = expectedVersion
			if subject == VersionRef(SubjectWorkItem, event.SubjectID) {
				expected[subject] = versionAfter
			}
		}
		return applyWorkflowTestOperation(ctx, s, Operation{Events: suffix, ExpectedVersions: expected})
	}
	return applyWorkflowTestOperationDirect(ctx, s, operation)
}

func applyWorkflowTestOperationDirect(ctx context.Context, s *Store, operation Operation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	_, err = applyWorkflowOperationTx(ctx, tx, operation, newFoldScope(tx))
	if err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func shiftWorkflowTestEventVersions(event Event, shift int64) Event {
	if shift == 0 {
		return event
	}
	var payload map[string]any
	if json.Unmarshal(event.Payload, &payload) != nil {
		return event
	}
	if expected, ok := payload["expected_version"].(float64); ok {
		payload["expected_version"] = int64(expected) + shift
	}
	if resulting, ok := payload["resulting_version"].(float64); ok {
		payload["resulting_version"] = int64(resulting) + shift
	}
	event.Payload, _ = json.Marshal(payload)
	return event
}

func advanceWorkflowTestInstanceToStep(ctx context.Context, s *Store, workID, targetStep, actor string) error {
	for {
		var currentStep, definitionRef string
		var definitionVersion, version int64
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT current_step,definition_ref,definition_version,(SELECT version FROM work_items WHERE id=workflow_instances.work_id) FROM workflow_instances WHERE work_id=?`, workID).Scan(&currentStep, &definitionRef, &definitionVersion, &version); err != nil {
			return err
		}
		entry, ok := BuiltinWorkflowRegistry().Lookup(definitionRef, definitionVersion)
		if !ok {
			return fmt.Errorf("workflow fixture definition %s@%d is not registered", definitionRef, definitionVersion)
		}
		if currentStep == targetStep {
			return nil
		}
		step := workflowStep(entry.Definition, currentStep)
		if step == nil {
			return fmt.Errorf("workflow fixture is at unknown step %q", currentStep)
		}
		actionID := ""
		for _, candidate := range step.Actions {
			if mode, declared := workflowActionExecutionMode(entry.Definition, candidate); declared && mode == ActionAdvance {
				actionID = candidate
				break
			}
		}
		if actionID == "" {
			return fmt.Errorf("workflow fixture step %q has no advancing action", currentStep)
		}
		nextStep := workflowNextStep(entry.Definition, currentStep)
		if nextStep == "" {
			return fmt.Errorf("workflow fixture cannot reach step %q from %q", targetStep, currentStep)
		}
		payload := map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "step_id": currentStep, "action_id": actionID, "attempt_epoch": 1, "result_evidence_refs": []string{}, "changed_refs": []string{workID}, "actor_ref": actor}
		if err := applyWorkflowTestOperationDirect(ctx, s, Operation{Events: []Event{workflowEventWithActor(workID+":fixture-walk:"+currentStep, WorkflowActionCompleted, workID, actor, payload)}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
			return err
		}
	}
}

// replayWorkflowAuthority is the corpus-only authority fixture seam. It uses
// the production durable claim/completion protocol so condition folds can
// validate evidence against a real completed operation; it never inserts a
// durable_operations row directly.
func replayWorkflowAuthority(ctx context.Context, s *Store, opID, workID, principal, requestID string, evidence []string) error {
	digest := "sha256:" + strings.Repeat("a", 64)
	workflowTypeRef, workflowTypeVersion, stepID := "workflow.test", 1, "evidence"
	_ = s.DatabaseForTesting().QueryRowContext(ctx, `SELECT definition_ref,definition_version,current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&workflowTypeRef, &workflowTypeVersion, &stepID)
	claimed, err := ClaimStepAuthorized(ctx, s, ClaimRequest{
		OpID: opID, WorkID: workID, WorkflowTypeRef: workflowTypeRef, WorkflowTypeVersion: workflowTypeVersion,
		StepID: stepID, StepKind: StepInternalSQLite, AcceptedInputsDigest: digest,
		AcceptedScopeSnapshot: `{}`, PrincipalRef: principal, Tool: "workflow-corpus", IdempotencyKey: opID,
		RequestID: requestID, ObservedAt: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC), ContractDigest: testManifestDigest,
	}, nil)
	if err != nil {
		return err
	}
	_, err = CompleteStep(ctx, s, CompleteRequest{
		OpID: opID, AttemptEpoch: claimed.AttemptEpoch, ResultKind: ResultCompleted,
		EvidenceRefs: evidence, PrincipalRef: principal, Tool: "workflow-corpus", IdempotencyKey: opID,
		RequestID: requestID, ObservedAt: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC),
		CompletedAt: ptrTime(time.Date(2026, 8, 9, 0, 0, 1, 0, time.UTC)),
	})
	return err
}

func ptrTime(value time.Time) *time.Time { return &value }

// CompleteWorkflow drives the ordered completion gate directly, outside the
// workflow action boundary that shipped completion runs through. It owns one
// durable transaction from the first gate clause through all impact notices
// and the reserved completion append.
func CompleteWorkflow(ctx context.Context, s *Store, event Event) error {
	return CompleteWorkflowWithRegistry(ctx, s, BuiltinWorkflowRegistry(), event)
}

// CompleteWorkflowWithRegistry is CompleteWorkflow for a pinned definition
// registry. The registry is read-only during completion.
func CompleteWorkflowWithRegistry(ctx context.Context, s *Store, registry DefinitionRegistry, event Event) (retErr error) {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "complete_workflow", "store is not open", false, "open a store before completing a workflow")
	}
	// A replay of the exact completion identity is an idempotent no-op.  This
	// check happens before opening the mutation transaction and never treats a
	// different event identity as a replay.
	var existingKind string
	err := s.db.QueryRowContext(ctx, `SELECT kind FROM domain_events WHERE event_id=?`, event.EventID).Scan(&existingKind)
	if err == nil && existingKind == WorkflowCompleted {
		replay, err := s.beginDurableTx(ctx)
		if err != nil {
			return err
		}
		defer replay.finish(&retErr)
		return replay.Commit()
	}
	// CD-0200: the completion's law boundary verifies the Product's complete
	// registered source set before this transaction opens. The verification
	// probes git and rebuilds through the pool, and the transaction-scoped
	// boundary clause proves it from the returned context.
	ctx, err = s.EstablishKnowledgeSourceSetProof(ctx, event.SubjectID)
	if err != nil {
		return err
	}
	tx, err := s.beginDurableTx(ctx)
	if err != nil {
		return wrapFailure(KindUnavailable, "complete_workflow", "cannot begin workflow completion", true, "retry once the database is writable", err)
	}
	defer tx.finish(&retErr)
	scope, err := beginFold(ctx, tx.Tx)
	if err != nil {
		return err
	}
	if err := CompleteWorkflowTxWithRegistry(ctx, tx.Tx, registry, event, scope); err != nil {
		return err
	}
	if err := scope.close(ctx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrapFailure(KindUnavailable, "complete_workflow", "cannot commit workflow completion", true, "retry once the database is writable", err)
	}
	return nil
}

// pinnedWorkflowDefinitionForTest resolves the definition the work item's
// instance pins.
func pinnedWorkflowDefinitionForTest(t testing.TB, s *Store, workID string) WorkflowDefinition {
	t.Helper()
	var ref string
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_version FROM workflow_instances WHERE work_id=?`, workID).Scan(&ref, &version); err != nil {
		t.Fatal(err)
	}
	registered, ok := BuiltinWorkflowRegistry().Lookup(ref, version)
	if !ok {
		t.Fatalf("pinned definition %s v%d is not registered", ref, version)
	}
	return registered.Definition
}
