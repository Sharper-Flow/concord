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

// workflowFixtureRef names the test-only workflow family that fold, projection,
// and supersession fixtures pin. It carries the implementation step graph so
// step IDs stay familiar, and a work kind without Product-truth authority so a
// contract fixture stays about the mechanism under test rather than about
// architecture binding. Version 2 adds the worker action pair, which gives the
// registry a real supersession to prove against; the shipped built-ins carry
// exactly one version each.
const workflowFixtureRef = "workflow.test_fixture"

// workflowFixtureWorkKind is the payload spelling of the fixture family's work
// kind.
const workflowFixtureWorkKind = "static_analysis"

// The fixture family registers at test-binary initialization so cross-process
// race workers, which re-enter the binary without running a helper, resolve the
// same pins as their parent.
var workflowFixtureVersions = registerWorkflowFixtureFamily()

func registerWorkflowFixtureFamily() []RegisteredDefinition {
	registry := BuiltinWorkflowRegistry()
	registered := make([]RegisteredDefinition, 0, 2)
	for _, definition := range []WorkflowDefinition{
		workflowFixtureShape(builtinImplementation(), 1),
		workflowFixtureShape(withWorkerActions(builtinImplementation()), 2),
	} {
		entry, err := registry.Register(definition)
		if err != nil {
			panic(err)
		}
		registered = append(registered, entry)
	}
	return registered
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
	return definition
}

func workflowFixtureDigest(t *testing.T) string {
	t.Helper()
	return workflowFixtureDefinition(t, 1).Digest
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
		if currentStep == "start" {
			currentStep = entry.Definition.StepGraph.StartStep
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
	if err := enterFold(ctx, tx); err != nil {
		tx.Rollback()
		return err
	}
	_, err = applyWorkflowOperationTx(ctx, tx, operation)
	_ = leaveFold(ctx, tx)
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
		if currentStep == "start" {
			currentStep = entry.Definition.StepGraph.StartStep
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
	if stepID == "start" {
		if definition, definitionErr := BuiltinWorkflowDefinitionForRef(workflowTypeRef); definitionErr == nil {
			stepID = definition.Definition.StepGraph.StartStep
		}
	}
	claimed, err := ClaimStep(ctx, s, ClaimRequest{
		OpID: opID, WorkID: workID, WorkflowTypeRef: workflowTypeRef, WorkflowTypeVersion: workflowTypeVersion,
		StepID: stepID, StepKind: StepInternalSQLite, AcceptedInputsDigest: digest,
		AcceptedScopeSnapshot: `{}`, PrincipalRef: principal, Tool: "workflow-corpus", IdempotencyKey: opID,
		RequestID: requestID, ObservedAt: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC), ContractDigest: testManifestDigest,
	})
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
