package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// WorkflowOperatorChoice is the closed, model-facing choice set for a human
// checkpoint. The adapter never manufactures or interprets these choices; the
// model supplies the selected choice to the public action after calling the
// built-in question UI.
type WorkflowOperatorChoice struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	ActionID    string `json:"action_id"`
}

// WorkflowOperatorQuestion is a bounded semantic question. It is not an
// approval and carries no actor identity. The digest binds the model's answer
// to the exact workflow observation that produced the question.
type WorkflowOperatorQuestion struct {
	ActionID              string                   `json:"action_id"`
	Prompt                string                   `json:"prompt"`
	Header                string                   `json:"header"`
	Choices               []WorkflowOperatorChoice `json:"choices"`
	AllowMultiple         bool                     `json:"allow_multiple"`
	AllowCustom           bool                     `json:"allow_custom"`
	PremiseSummary        string                   `json:"premise_summary"`
	ContractSummary       string                   `json:"contract_summary"`
	DecisionContextDigest string                   `json:"decision_context_digest"`
}

// ComputeWorkflowDecisionContextDigest is the stable digest bound into a
// question and required by confirm_premise. Raw outcome bytes are deliberately
// retained: re-encoding or normalizing them must not silently change the
// approved context.
func ComputeWorkflowDecisionContextDigest(workID string, workVersion int64, definition WorkflowReadDefinition, contract WorkflowReadContract, actionID string) string {
	values := []struct {
		name  string
		value []byte
	}{
		{"work_id", []byte(workID)},
		{"work_version", []byte(fmt.Sprint(workVersion))},
		{"definition_ref", []byte(definition.Ref)},
		{"definition_version", []byte(fmt.Sprint(definition.Version))},
		{"definition_digest", []byte(definition.Digest)},
		{"contract_version", []byte(fmt.Sprint(contract.Version))},
		{"action_id", []byte(actionID)},
		{"premise", []byte(contract.Premise)},
		{"outcome_kind", []byte(contract.OutcomeKind)},
		{"outcome_payload", []byte(contract.OutcomePayload)},
	}
	var canonical strings.Builder
	canonical.WriteString("workflow-operator-question-v1\x00")
	for _, field := range values {
		canonical.WriteString(field.name)
		canonical.WriteByte('=')
		canonical.WriteString(fmt.Sprint(len(field.value)))
		canonical.WriteByte(':')
		canonical.Write(field.value)
		canonical.WriteByte('|')
	}
	sum := sha256.Sum256([]byte(canonical.String()))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ReadWorkflowOperatorQuestion returns the question for the next approved
// human-checkpoint action, if the current step has one. No question is emitted
// for non-human steps or for a workflow without an approved contract.
func ReadWorkflowOperatorQuestion(ctx context.Context, s *Store, workID string) (*WorkflowOperatorQuestion, error) {
	if s == nil || s.db == nil {
		return nil, newFailure(KindUnavailable, "workflow_operator_question", "store is not open", false, "open the authority database")
	}
	var currentStep string
	var workVersion int64
	var definition WorkflowReadDefinition
	if err := s.db.QueryRowContext(ctx, `SELECT current_step,definition_ref,definition_version,definition_digest,(SELECT version FROM work_items WHERE id=workflow_instances.work_id) FROM workflow_instances WHERE work_id=?`, workID).Scan(&currentStep, &definition.Ref, &definition.Version, &definition.Digest, &workVersion); err != nil {
		if err == context.Canceled || err == context.DeadlineExceeded {
			return nil, err
		}
		if err == sql.ErrNoRows {
			return nil, newFailure(KindProjectionNotFound, "workflow_operator_question", "workflow instance is not recorded", false, "reread_entities")
		}
		return nil, wrapFailure(KindUnavailable, "workflow_operator_question", "cannot read workflow question context", true, "retry once the database is readable", err)
	}
	if currentStep == "start" {
		entry, err := VerifyWorkflowDefinitionPin(BuiltinWorkflowRegistry(), WorkflowDefinitionPin(definition))
		if err != nil {
			return nil, err
		}
		currentStep = entry.Definition.StepGraph.StartStep
	}
	var contract WorkflowReadContract
	var required, routes, mandates, modifies string
	if err := s.db.QueryRowContext(ctx, `SELECT c.contract_version,c.premise,p.outcome_kind,p.outcome_payload,c.required_evidence,c.route_conventions,c.spec_mandate,c.law_modifies FROM workflow_contracts c JOIN workflow_contract_predicates p ON p.work_id=c.work_id AND p.contract_version=c.contract_version AND p.ordinal=0 WHERE c.work_id=? AND c.superseded_by IS NULL ORDER BY c.contract_version DESC LIMIT 1`, workID).Scan(&contract.Version, &contract.Premise, &contract.OutcomeKind, &contract.OutcomePayload, &required, &routes, &mandates, &modifies); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, wrapFailure(KindUnavailable, "workflow_operator_question", "cannot read workflow question contract", true, "retry once the database is readable", err)
	}
	if json.Unmarshal([]byte(required), &contract.RequiredEvidence) != nil || json.Unmarshal([]byte(routes), &contract.RouteConventions) != nil || json.Unmarshal([]byte(mandates), &contract.SpecMandate) != nil || json.Unmarshal([]byte(modifies), &contract.LawModifies) != nil {
		return nil, newFailure(KindInvariantViolation, "workflow_operator_question", "workflow contract projection contains malformed arrays", false, "rebuild projections from the event log")
	}
	entry, err := VerifyWorkflowDefinitionPin(BuiltinWorkflowRegistry(), WorkflowDefinitionPin(definition))
	if err != nil {
		return nil, err
	}
	step := workflowStep(entry.Definition, currentStep)
	if step == nil || step.Kind != WorkflowStepHumanCheckpoint {
		return nil, nil
	}
	for _, candidate := range step.Actions {
		for _, action := range entry.Definition.ActionDefinitions {
			if action.ID != candidate || action.Approval != ActionApprovalRequired {
				continue
			}
			return workflowOperatorQuestion(workID, workVersion, definition, contract, action.ID), nil
		}
	}
	return nil, nil
}

func workflowOperatorQuestion(workID string, workVersion int64, definition WorkflowReadDefinition, contract WorkflowReadContract, actionID string) *WorkflowOperatorQuestion {
	premise := boundedQuestionText(contract.Premise, 256)
	return &WorkflowOperatorQuestion{
		ActionID: actionID,
		Prompt:   fmt.Sprintf("Choose how to proceed with the approved workflow checkpoint for %s.", workID),
		Header:   "Operator checkpoint",
		Choices: []WorkflowOperatorChoice{
			{ID: "confirm", Label: "Confirm", Description: "Confirm the exact approved premise and continue.", ActionID: actionID},
			{ID: "revise", Label: "Revise", Description: "Route to the declared revision action before continuing.", ActionID: "concord_work_define.revise_intent"},
			{ID: "stop", Label: "Stop", Description: "Route to the declared cancellation action without applying this checkpoint.", ActionID: "concord_work_transition.lifecycle"},
		},
		AllowMultiple:         false,
		AllowCustom:           false,
		PremiseSummary:        premise,
		ContractSummary:       fmt.Sprintf("contract v%d; outcome %s", contract.Version, boundedQuestionText(contract.OutcomeKind, 128)),
		DecisionContextDigest: ComputeWorkflowDecisionContextDigest(workID, workVersion, definition, contract, actionID),
	}
}

func workflowOperatorQuestionTx(workID, currentStep string, workVersion int64, definition WorkflowReadDefinition, contract WorkflowReadContract) (*WorkflowOperatorQuestion, error) {
	entry, err := VerifyWorkflowDefinitionPin(BuiltinWorkflowRegistry(), WorkflowDefinitionPin(definition))
	if err != nil {
		return nil, err
	}
	if currentStep == "start" {
		currentStep = entry.Definition.StepGraph.StartStep
	}
	step := workflowStep(entry.Definition, currentStep)
	if step == nil || step.Kind != WorkflowStepHumanCheckpoint {
		return nil, nil
	}
	for _, candidate := range step.Actions {
		for _, action := range entry.Definition.ActionDefinitions {
			if action.ID == candidate && action.Approval == ActionApprovalRequired {
				return workflowOperatorQuestion(workID, workVersion, definition, contract, action.ID), nil
			}
		}
	}
	return nil, nil
}

func boundedQuestionText(value string, max int) string {
	if len([]rune(value)) <= max {
		return value
	}
	runes := []rune(value)
	return string(runes[:max])
}

// ValidateWorkflowOperatorSelection verifies the semantic answer before any
// grant, approval challenge, or mutation authority is consumed.
func ValidateWorkflowOperatorSelection(ctx context.Context, s *Store, workID string, expectedVersion int64, actionID, selectedChoice, decisionDigest string) error {
	if actionID != "confirm_premise" {
		if selectedChoice != "" || decisionDigest != "" {
			return newFailure(KindInvalidPayload, "workflow_operator_question", "question selection is only valid for confirm_premise", false, "use the declared action payload")
		}
		return nil
	}
	if selectedChoice == "" {
		return newFailure(KindInvalidPayload, "workflow_operator_question", "confirm_premise requires a selected closed choice", false, "reread the workflow question")
	}
	if selectedChoice != "confirm" {
		recovery := "use the declared revision or cancellation action"
		return newFailure(KindIllegalLifecycleTransition, "workflow_operator_question", "confirm_premise accepts only the closed confirm choice; revise and stop must use their declared actions", false, recovery)
	}
	if !workflowDigestPattern.MatchString(decisionDigest) {
		return newFailure(KindInvalidPayload, "workflow_operator_question", "confirm_premise requires a valid decision_context_digest", false, "reread the workflow question")
	}
	question, err := ReadWorkflowOperatorQuestion(ctx, s, workID)
	if err != nil {
		return err
	}
	if question == nil || question.ActionID != actionID {
		return newFailure(KindStaleRequiresReview, "workflow_operator_question", "the operator question is no longer available for this action", false, "refresh_context")
	}
	if question.DecisionContextDigest != decisionDigest {
		return newFailure(KindStaleRequiresReview, "workflow_operator_question", "decision context digest is stale or forged", false, "refresh_context")
	}
	var version int64
	if err := s.db.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		return wrapFailure(KindUnavailable, "workflow_operator_question", "cannot re-read workflow version", true, "retry once the database is readable", err)
	}
	if version != expectedVersion {
		return versionConflict(SubjectWorkItem, workID, expectedVersion, version, false)
	}
	return nil
}

func validateWorkflowOperatorSelectionTx(ctx context.Context, tx *sql.Tx, registry DefinitionRegistry, request WorkflowActionPreflightRequest) error {
	if request.ActionID != "confirm_premise" {
		if request.SelectedChoice != "" || request.DecisionContextDigest != "" {
			return newFailure(KindInvalidPayload, "workflow_operator_question", "question selection is only valid for confirm_premise", false, "use the declared action payload")
		}
		return nil
	}
	if request.SelectedChoice == "" {
		return newFailure(KindInvalidPayload, "workflow_operator_question", "confirm_premise requires a selected closed choice", false, "reread the workflow question")
	}
	if request.SelectedChoice != "confirm" {
		return newFailure(KindIllegalLifecycleTransition, "workflow_operator_question", "confirm_premise accepts only the closed confirm choice; revise and stop must use their declared actions", false, "use the declared revision or cancellation action")
	}
	if !workflowDigestPattern.MatchString(request.DecisionContextDigest) {
		return newFailure(KindInvalidPayload, "workflow_operator_question", "confirm_premise requires a valid decision_context_digest", false, "reread the workflow question")
	}
	var currentStep string
	var workVersion int64
	var definition WorkflowReadDefinition
	if err := tx.QueryRowContext(ctx, `SELECT current_step,definition_ref,definition_version,definition_digest,(SELECT version FROM work_items WHERE id=workflow_instances.work_id) FROM workflow_instances WHERE work_id=?`, request.WorkID).Scan(&currentStep, &definition.Ref, &definition.Version, &definition.Digest, &workVersion); err != nil {
		return wrapFailure(KindUnavailable, "workflow_operator_question", "cannot read workflow question context", true, "retry once the database is readable", err)
	}
	entry, err := VerifyWorkflowDefinitionPin(registry, WorkflowDefinitionPin(definition))
	if err != nil {
		return err
	}
	if currentStep == "start" {
		currentStep = entry.Definition.StepGraph.StartStep
	}
	var contract WorkflowReadContract
	var required, routes, mandates, modifies string
	if err := tx.QueryRowContext(ctx, `SELECT c.contract_version,c.premise,p.outcome_kind,p.outcome_payload,c.required_evidence,c.route_conventions,c.spec_mandate,c.law_modifies FROM workflow_contracts c JOIN workflow_contract_predicates p ON p.work_id=c.work_id AND p.contract_version=c.contract_version AND p.ordinal=0 WHERE c.work_id=? AND c.superseded_by IS NULL ORDER BY c.contract_version DESC LIMIT 1`, request.WorkID).Scan(&contract.Version, &contract.Premise, &contract.OutcomeKind, &contract.OutcomePayload, &required, &routes, &mandates, &modifies); err != nil {
		return newFailure(KindStaleRequiresReview, "workflow_operator_question", "the operator question contract is no longer available", false, "refresh_context")
	}
	if json.Unmarshal([]byte(required), &contract.RequiredEvidence) != nil || json.Unmarshal([]byte(routes), &contract.RouteConventions) != nil || json.Unmarshal([]byte(mandates), &contract.SpecMandate) != nil || json.Unmarshal([]byte(modifies), &contract.LawModifies) != nil {
		return newFailure(KindInvariantViolation, "workflow_operator_question", "workflow contract projection contains malformed arrays", false, "rebuild projections from the event log")
	}
	step := workflowStep(entry.Definition, currentStep)
	if step == nil || step.Kind != WorkflowStepHumanCheckpoint {
		return newFailure(KindStaleRequiresReview, "workflow_operator_question", "the operator question is no longer available for this action", false, "refresh_context")
	}
	question := (*WorkflowOperatorQuestion)(nil)
	for _, candidate := range step.Actions {
		for _, action := range entry.Definition.ActionDefinitions {
			if action.ID == candidate && action.Approval == ActionApprovalRequired {
				question = workflowOperatorQuestion(request.WorkID, workVersion, definition, contract, action.ID)
				break
			}
		}
		if question != nil {
			break
		}
	}
	if question == nil || question.ActionID != request.ActionID || question.DecisionContextDigest != request.DecisionContextDigest {
		return newFailure(KindStaleRequiresReview, "workflow_operator_question", "decision context digest is stale or forged", false, "refresh_context")
	}
	if workVersion != request.ExpectedVersion {
		return versionConflict(SubjectWorkItem, request.WorkID, request.ExpectedVersion, workVersion, false)
	}
	return nil
}
