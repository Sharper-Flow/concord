package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
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

// WorkflowOperatorQuestionWithheld explains a human checkpoint whose question
// exists in the definition but is not open yet. A bare nil question cannot say
// whether the step has no checkpoint at all or has one the caller can still
// open, and those two states need different actions from the caller.
type WorkflowOperatorQuestionWithheld struct {
	ActionID string `json:"action_id"`
	Reason   string `json:"reason"`
	Remedy   string `json:"remedy"`
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
	}
	for _, predicate := range contract.OutcomePredicates {
		values = append(values,
			struct {
				name  string
				value []byte
			}{"predicate_id", []byte(predicate.PredicateID)},
			struct {
				name  string
				value []byte
			}{"predicate_ordinal", []byte(fmt.Sprint(predicate.Ordinal))},
			struct {
				name  string
				value []byte
			}{"outcome_kind", []byte(predicate.OutcomeKind)},
			struct {
				name  string
				value []byte
			}{"outcome_payload", []byte(predicate.OutcomePayload)},
		)
	}
	if contract.SelfRepair != nil {
		encoded, _ := json.Marshal(contract.SelfRepair)
		values = append(values, struct {
			name  string
			value []byte
		}{"self_repair", encoded})
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

func workflowOperatorQuestion(workID string, workVersion int64, definition WorkflowReadDefinition, contract WorkflowReadContract, actionID string) *WorkflowOperatorQuestion {
	premise := boundedQuestionText(contract.Premise, 256)
	outcomeKinds := make([]string, 0, len(contract.OutcomePredicates))
	for _, predicate := range contract.OutcomePredicates {
		outcomeKinds = append(outcomeKinds, predicate.OutcomeKind)
	}
	return &WorkflowOperatorQuestion{
		ActionID: actionID,
		Prompt:   fmt.Sprintf("Choose how to proceed with the approved workflow checkpoint for %s.", workID),
		Header:   "Operator checkpoint",
		Choices: []WorkflowOperatorChoice{
			{ID: "confirm", Label: "Confirm", Description: "Confirm the exact approved premise and continue.", ActionID: actionID},
			{ID: "revise", Label: "Revise", Description: "Route to operator-approved contract supersession before continuing.", ActionID: "supersede_contract"},
			{ID: "stop", Label: "Stop", Description: "Route to the declared cancellation action without applying this checkpoint.", ActionID: "concord_work_transition.lifecycle"},
		},
		AllowMultiple:         false,
		AllowCustom:           false,
		PremiseSummary:        premise,
		ContractSummary:       fmt.Sprintf("contract v%d; outcomes %s", contract.Version, boundedQuestionText(strings.Join(outcomeKinds, ", "), 128)),
		DecisionContextDigest: ComputeWorkflowDecisionContextDigest(workID, workVersion, definition, contract, actionID),
	}
}

// workflowOperatorQuestionTx returns the open question for the current step,
// or the reason its question is withheld. Exactly one of the two is non-nil,
// and both are nil when the step declares no approval-required action. It is
// the builtin convenience: the caller's read definition is verified against
// the builtin registry, and the shared owner below answers.
func workflowOperatorQuestionTx(ctx context.Context, q queryer, workID, currentStep string, workVersion int64, definition WorkflowReadDefinition, contract WorkflowReadContract) (*WorkflowOperatorQuestion, *WorkflowOperatorQuestionWithheld, error) {
	entry, err := resolveCurrentWorkflowDefinition(BuiltinWorkflowRegistry(), definition.Ref)
	if err != nil {
		return nil, nil, err
	}
	confirmation, err := foldWorkflowConfirmationTx(ctx, q, workID, entry.Definition)
	if err != nil {
		return nil, nil, err
	}
	return workflowOperatorQuestionForDefinitionTx(ctx, q, workID, currentStep, workVersion, entry.Definition, definition, contract, confirmation)
}

// workflowOperatorQuestionForDefinitionTx is the shared question-projection
// owner. The caller's verified definition resolves the pinned shape and the
// caller supplies the one folded confirmation answer — the state's fields or
// a light foldWorkflowConfirmationTx read — so no consumer classifies a
// second time: engineering work projects neither a question nor a
// withholding, decision work projects the exact question its digest binds,
// and a missing or malformed new-policy fact withholds the question with
// its typed reason.
func workflowOperatorQuestionForDefinitionTx(ctx context.Context, q queryer, workID, currentStep string, workVersion int64, definition WorkflowDefinition, readDefinition WorkflowReadDefinition, contract WorkflowReadContract, confirmation workflowConfirmationFold) (*WorkflowOperatorQuestion, *WorkflowOperatorQuestionWithheld, error) {
	// The step's shape decides first: a step that declares no
	// approval-required action projects no question and no withholding,
	// whatever the folded confirmation answer says.
	actionID, ok := workflowOperatorQuestionAction(definition, currentStep)
	if !ok {
		return nil, nil, nil
	}
	if definition.ApprovalPolicy == WorkflowApprovalPolicyConsequenceV1 {
		if confirmation.factFailure != nil {
			return nil, &WorkflowOperatorQuestionWithheld{ActionID: actionID, Reason: confirmation.factFailure.Detail, Remedy: confirmation.factFailure.RecoveryAction}, nil
		}
		if confirmation.authority == WorkflowConfirmationCoordinator {
			return nil, nil, nil
		}
	}
	if err := requireRecordedInvestigationArtifact(ctx, q, workID); err != nil {
		var failure *Failure
		if failureAs(err, &failure) && failure.Kind == KindMissingEvidence {
			return nil, &WorkflowOperatorQuestionWithheld{ActionID: actionID, Reason: failure.Detail, Remedy: failure.RecoveryAction}, nil
		}
		return nil, nil, err
	}
	return workflowOperatorQuestion(workID, workVersion, readDefinition, contract, actionID), nil, nil
}

// requireRecordedInvestigationArtifact admits an operator question only when a
// work observation names a current Domain of the work item's Product and,
// when the Product holds another work item, a different work item. The
// comparison-work condition is counted here in the same transaction as the
// ref resolution (CD-0173): a Product whose only work item is this one has no
// other work item to name, so demanding one would wedge every single-item
// Product out of its own premise gate.
func requireRecordedInvestigationArtifact(ctx context.Context, q queryer, workID string) error {
	comparisonRequired, err := productHoldsAnotherWorkItem(ctx, q, workID)
	if err != nil {
		return err
	}
	rows, err := q.QueryContext(ctx, `SELECT refs FROM work_observations WHERE work_id=? ORDER BY recorded_at DESC, observation_id`, workID)
	if err != nil {
		return wrapFailure(KindUnavailable, "workflow_operator_question", "cannot inspect recorded investigation artifacts", true, "retry once the workflow evidence is readable", err)
	}
	var candidates [][]string
	for rows.Next() {
		var refsJSON string
		if err := rows.Scan(&refsJSON); err != nil {
			rows.Close()
			return wrapFailure(KindUnavailable, "workflow_operator_question", "cannot read recorded investigation artifact refs", true, "retry once the workflow evidence is readable", err)
		}
		var refs []string
		if json.Unmarshal([]byte(refsJSON), &refs) != nil {
			rows.Close()
			return newFailure(KindInvariantViolation, "workflow_operator_question", "recorded investigation artifact refs are malformed", false, "rebuild the work observation projection")
		}
		candidates = append(candidates, refs)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return wrapFailure(KindUnavailable, "workflow_operator_question", "cannot inspect recorded investigation artifacts", true, "retry once the workflow evidence is readable", err)
	}
	if err := rows.Close(); err != nil {
		return wrapFailure(KindUnavailable, "workflow_operator_question", "cannot inspect recorded investigation artifacts", true, "retry once the workflow evidence is readable", err)
	}
	for _, refs := range candidates {
		namesDomain := false
		namesWork := false
		for _, ref := range refs {
			if !namesDomain {
				current, err := investigationRefIsCurrentDomain(ctx, q, workID, ref)
				if err != nil {
					return err
				}
				namesDomain = current
			}
			if !namesWork && ref != workID {
				known, err := investigationRefIsKnownWork(ctx, q, ref)
				if err != nil {
					return err
				}
				namesWork = known
			}
			if namesDomain && (namesWork || !comparisonRequired) {
				return nil
			}
		}
	}
	if comparisonRequired {
		return newFailure(KindMissingEvidence, "workflow_operator_question", "operator question requires a recorded investigation artifact naming a current Domain of the Product and another work item; its refs must hold the bare domain_id of the current Domain and the id of another work item", false, "call concord_work_define.observation_record with this work_id and refs containing the bare domain_id of a current Product Domain and the id of another work item")
	}
	return newFailure(KindMissingEvidence, "workflow_operator_question", "operator question requires a recorded investigation artifact naming a current Domain of the Product; its ref must hold the bare domain_id of the current Domain", false, "call concord_work_define.observation_record with this work_id and refs containing the bare domain_id of a current Product Domain")
}

// productHoldsAnotherWorkItem reports whether the work item's Product holds a
// work item other than this one, in any lifecycle. The count runs on the
// caller's queryer, so it reads the same transaction snapshot the ref
// resolution reads.
func productHoldsAnotherWorkItem(ctx context.Context, q queryer, workID string) (bool, error) {
	var held int
	err := q.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1
		FROM work_items other
		JOIN work_projects wp2 ON wp2.work_id = other.id
		JOIN product_projects pp2 ON pp2.project_id = wp2.project_id
		WHERE other.id <> ?
		  AND pp2.product_id IN (
			SELECT pp.product_id
			FROM work_projects wp
			JOIN product_projects pp ON pp.project_id = wp.project_id
			WHERE wp.work_id = ?
		  )
	)`, workID, workID).Scan(&held)
	if err != nil {
		return false, wrapFailure(KindUnavailable, "workflow_operator_question", "cannot count the Product's work items", true, "retry once the work projection is readable", err)
	}
	return held == 1, nil
}

func investigationRefIsCurrentDomain(ctx context.Context, q queryer, workID, ref string) (bool, error) {
	var status string
	err := q.QueryRowContext(ctx, `SELECT d.status FROM domains d JOIN product_projects pp ON pp.product_id=d.product_id JOIN work_projects wp ON wp.project_id=pp.project_id WHERE wp.work_id=? AND d.domain_id=? LIMIT 1`, workID, ref).Scan(&status)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, wrapFailure(KindUnavailable, "workflow_operator_question", "cannot resolve investigation artifact Domain ref", true, "retry once the Domain projection is readable", err)
	}
	return status == "current", nil
}

func investigationRefIsKnownWork(ctx context.Context, q queryer, ref string) (bool, error) {
	var found string
	err := q.QueryRowContext(ctx, `SELECT id FROM work_items WHERE id=?`, ref).Scan(&found)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, wrapFailure(KindUnavailable, "workflow_operator_question", "cannot resolve investigation artifact work ref", true, "retry once the work item projection is readable", err)
	}
	return true, nil
}

func boundedQuestionText(value string, upper int) string {
	if len([]rune(value)) <= upper {
		return value
	}
	runes := []rune(value)
	return string(runes[:upper])
}

// ValidateWorkflowOperatorSelectionForRegistry verifies the semantic answer
// before any grant, approval challenge, or mutation authority is consumed. A
// caller registry verifies the stored pin through itself with no builtin
// fallback; a nil registry verifies against the builtin registry. The read
// opens and closes one read transaction.
func ValidateWorkflowOperatorSelectionForRegistry(ctx context.Context, s *Store, registry DefinitionRegistry, request WorkflowActionPreflightRequest) error {
	if registry == nil {
		registry = BuiltinWorkflowRegistry()
	}
	tx, err := beginReadTx(ctx, s.db)
	if err != nil {
		return wrapFailure(KindUnavailable, "workflow_operator_question", "cannot open the selection read transaction", true, "retry once the database is readable", err)
	}
	defer func() { _ = tx.Rollback() }()
	return validateWorkflowOperatorSelectionTx(ctx, tx, registry, request)
}

// validateWorkflowOperatorSelectionTx is the stateless compatibility half of
// the selection chain: no folded admission state is in hand, so it folds the
// confirmation answer once and hands it to the core.
func validateWorkflowOperatorSelectionTx(ctx context.Context, tx *sql.Tx, registry DefinitionRegistry, request WorkflowActionPreflightRequest) error {
	if done, fastErr := workflowOperatorSelectionFastPath(request); done || fastErr != nil {
		return fastErr
	}
	pin := WorkflowDefinitionPin{}
	if err := tx.QueryRowContext(ctx, `SELECT definition_ref,definition_version,definition_digest FROM workflow_instances WHERE work_id=?`, request.WorkID).Scan(&pin.Ref, &pin.Version, &pin.Digest); err != nil {
		return wrapFailure(KindUnavailable, "workflow_operator_question", "cannot read workflow question context", true, "retry once the database is readable", err)
	}
	entry, err := resolveCurrentWorkflowDefinition(registry, pin.Ref)
	if err != nil {
		return err
	}
	confirmation, err := foldWorkflowConfirmationTx(ctx, tx, request.WorkID, entry.Definition)
	if err != nil {
		return err
	}
	return validateWorkflowOperatorSelectionCoreTx(ctx, tx, entry, request, confirmation)
}

// workflowOperatorSelectionFastPath answers the non-confirm fast path: the
// actions that take no operator selection pass without any store read, and
// selection fields riding one refuse. The core and the stateless wrapper
// share it, so neither queries an instance for an answer the request already
// settles.
func workflowOperatorSelectionFastPath(request WorkflowActionPreflightRequest) (bool, error) {
	if request.ActionID != "confirm_premise" {
		if request.SelectedChoice != "" || request.DecisionContextDigest != "" {
			return false, newFailure(KindInvalidPayload, "workflow_operator_question", "question selection is only valid for confirm_premise", false, "use the declared action payload")
		}
		return true, nil
	}
	return false, nil
}

// validateWorkflowOperatorSelectionCoreTx is the selection chain's one core:
// the caller's verified definition and the one folded confirmation answer —
// the preflight's state fields or the convenience's light fold — decide
// between the engineering and decision paths. No caller classifies a second
// time.
func validateWorkflowOperatorSelectionCoreTx(ctx context.Context, tx *sql.Tx, entry RegisteredDefinition, request WorkflowActionPreflightRequest, confirmation workflowConfirmationFold) error {
	if done, fastErr := workflowOperatorSelectionFastPath(request); done || fastErr != nil {
		return fastErr
	}
	if request.SelectedChoice == "" {
		return newFailure(KindInvalidPayload, "workflow_operator_question", "confirm_premise requires a selected closed choice", false, "reread the workflow question")
	}
	if request.SelectedChoice != "confirm" {
		return newFailure(KindIllegalLifecycleTransition, "workflow_operator_question", "confirm_premise accepts only the closed confirm choice; revise and stop must use their declared actions", false, "use the declared revision or cancellation action")
	}
	var currentStep string
	var workVersion int64
	var definitionRef string
	if err := tx.QueryRowContext(ctx, `SELECT current_step,definition_ref,(SELECT version FROM work_items WHERE id=workflow_instances.work_id) FROM workflow_instances WHERE work_id=?`, request.WorkID).Scan(&currentStep, &definitionRef, &workVersion); err != nil {
		return wrapFailure(KindUnavailable, "workflow_operator_question", "cannot read workflow question context", true, "retry once the database is readable", err)
	}
	// The caller verified the current definition. Stored instance metadata is
	// not a dependency pin; only a family change invalidates that selection.
	if definitionRef != entry.Definition.Ref {
		return newFailure(KindStaleRequiresReview, "workflow_operator_question", "the workflow definition moved under the verified entry", false, "refresh_context")
	}
	activeContractVersion, contractErr := activeWorkflowContractVersion(ctx, tx, request.WorkID, "workflow_operator_question")
	if contractErr != nil {
		if contractErr == sql.ErrNoRows {
			return newFailure(KindStaleRequiresReview, "workflow_operator_question", "the operator question contract is no longer available", false, "refresh_context")
		}
		return contractErr
	}
	var contract WorkflowReadContract
	var required, routes, mandates, modifies string
	if err := tx.QueryRowContext(ctx, `SELECT contract_version,premise,required_evidence,route_conventions,spec_mandate,law_modifies FROM workflow_contracts WHERE work_id=? AND contract_version=? AND superseded_by IS NULL`, request.WorkID, activeContractVersion).Scan(&contract.Version, &contract.Premise, &required, &routes, &mandates, &modifies); err != nil {
		return newFailure(KindStaleRequiresReview, "workflow_operator_question", "the operator question contract is no longer available", false, "refresh_context")
	}
	if json.Unmarshal([]byte(required), &contract.RequiredEvidence) != nil || json.Unmarshal([]byte(routes), &contract.RouteConventions) != nil || json.Unmarshal([]byte(mandates), &contract.SpecMandate) != nil || json.Unmarshal([]byte(modifies), &contract.LawModifies) != nil {
		return newFailure(KindInvariantViolation, "workflow_operator_question", "workflow contract projection contains malformed arrays", false, "rebuild projections from the event log")
	}
	predicates, predicateErr := readWorkflowContractPredicates(ctx, tx, request.WorkID, contract.Version)
	if predicateErr != nil {
		return newFailure(KindInvariantViolation, "workflow_operator_question", "workflow contract predicates are unavailable", false, "rebuild projections from the event log")
	}
	contract.OutcomePredicates = predicates
	selfRepair, selfRepairErr := readWorkflowSelfRepair(ctx, tx, request.WorkID, contract.Version)
	if selfRepairErr != nil {
		return newFailure(KindInvariantViolation, "workflow_operator_question", "workflow self-repair classification is unavailable", false, "rebuild projections from the event log")
	}
	contract.SelfRepair = selfRepair
	engineering := false
	if entry.Definition.ApprovalPolicy == WorkflowApprovalPolicyConsequenceV1 {
		if confirmation.factFailure != nil {
			return confirmation.factFailure
		}
		engineering = confirmation.authority == WorkflowConfirmationCoordinator
	}
	if engineering {
		// Engineering: the coordinator records its own confirm choice — no
		// human digest, no operator question, and the recorded investigation
		// artifact the question path also demands.
		if request.DecisionContextDigest != "" {
			return newFailure(KindInvalidPayload, "workflow_operator_question", "an engineering premise confirmation carries no human decision-context digest", false, "drop the human digest for engineering work")
		}
		if artifactErr := requireRecordedInvestigationArtifact(ctx, tx, request.WorkID); artifactErr != nil {
			return artifactErr
		}
		if !slices.Contains(workflowStep(entry.Definition, currentStep).Actions, "confirm_premise") {
			return newFailure(KindStaleRequiresReview, "workflow_operator_question", "confirm_premise is not available at the current workflow step", false, "reread the workflow step and its declared actions")
		}
		if workVersion != request.ExpectedVersion {
			conflict, conflictErr := versionConflictForQuery(ctx, tx, request.WorkID, request.ExpectedVersion, workVersion)
			if conflictErr != nil {
				return conflictErr
			}
			return conflict
		}
		return nil
	}
	if !workflowDigestPattern.MatchString(request.DecisionContextDigest) {
		return newFailure(KindInvalidPayload, "workflow_operator_question", "confirm_premise requires a valid decision_context_digest", false, "reread the workflow question")
	}
	if !workflowOperatorDecisionPending(entry.Definition, currentStep) {
		return newFailure(KindStaleRequiresReview, "workflow_operator_question", "the operator question is no longer available for this action", false, "refresh_context")
	}
	definition := WorkflowReadDefinition{Ref: entry.Definition.Ref, Version: entry.Definition.Version, Digest: entry.Digest}
	question, _, err := workflowOperatorQuestionForDefinitionTx(ctx, tx, request.WorkID, currentStep, workVersion, entry.Definition, definition, contract, confirmation)
	if err != nil {
		return err
	}
	if question == nil {
		if artifactErr := requireRecordedInvestigationArtifact(ctx, tx, request.WorkID); artifactErr != nil {
			return artifactErr
		}
		return newFailure(KindStaleRequiresReview, "workflow_operator_question", "no operator question is open at the current workflow step", false, "reread the workflow step and its declared actions")
	}
	if question.ActionID != request.ActionID {
		return newFailure(KindStaleRequiresReview, "workflow_operator_question", fmt.Sprintf("the open operator question is for action %s, not %s", question.ActionID, request.ActionID), false, "answer the open question's action")
	}
	if question.DecisionContextDigest != request.DecisionContextDigest {
		return newFailure(KindStaleRequiresReview, "workflow_operator_question", "decision context digest is stale or forged", false, "refresh_context")
	}
	if workVersion != request.ExpectedVersion {
		conflict, conflictErr := versionConflictForQuery(ctx, tx, request.WorkID, request.ExpectedVersion, workVersion)
		if conflictErr != nil {
			return conflictErr
		}
		return conflict
	}
	return nil
}
