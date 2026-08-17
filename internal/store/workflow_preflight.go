package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

type WorkflowDefinitionPin struct {
	Ref     string
	Version int64
	Digest  string
}

type WorkflowStartRequest struct {
	WorkID     string
	Definition WorkflowDefinitionPin
	StepID     string
	ActionID   string
	Actor      WorkflowActor
}

type WorkflowActionPreflightRequest struct {
	WorkID                string
	ExpectedVersion       int64
	StepID                string
	ActionID              string
	SelectedChoice        string
	DecisionContextDigest string
	Payload               json.RawMessage
	Actor                 WorkflowActor
	// Consequential boundaries may supply the same explicit resolver used by
	// ResolveWorkflowCondition. A zero time disables boundary resolution rather
	// than inventing a clock observation.
	ConditionResolver ConditionResolver
	BoundaryNow       time.Time
}

func VerifyWorkflowDefinitionPin(registry DefinitionRegistry, pin WorkflowDefinitionPin) (RegisteredDefinition, error) {
	if registry == nil || !validWorkflowRef(pin.Ref) || pin.Version < 1 || !workflowDigestPattern.MatchString(pin.Digest) {
		return RegisteredDefinition{}, workflowPinFailure("workflow definition pin is incomplete")
	}
	entry, ok := registry.Lookup(pin.Ref, pin.Version)
	if !ok {
		return RegisteredDefinition{}, workflowPinFailure("pinned workflow definition is unavailable")
	}
	computed, err := WorkflowDefinitionDigest(entry.Definition)
	if err != nil {
		return RegisteredDefinition{}, err
	}
	if computed != entry.Digest || computed != pin.Digest {
		return RegisteredDefinition{}, workflowPinFailure("pinned workflow definition digest drifted")
	}
	return entry, nil
}

func workflowPinFailure(detail string) error {
	return newFailure(KindInvariantViolation, "workflow_preflight", detail, false, "reread_entities")
}

func VerifyWorkflowInstanceDefinition(ctx context.Context, s *Store, registry DefinitionRegistry, workID string) (RegisteredDefinition, error) {
	if s == nil || s.db == nil {
		return RegisteredDefinition{}, newFailure(KindUnavailable, "workflow_preflight", "store is not open", false, "open the authority database")
	}
	if registry == nil {
		registry = BuiltinWorkflowRegistry()
	}
	var pin WorkflowDefinitionPin
	if err := s.db.QueryRowContext(ctx, `SELECT definition_ref,definition_version,definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&pin.Ref, &pin.Version, &pin.Digest); err != nil {
		if err == sql.ErrNoRows {
			return RegisteredDefinition{}, workflowPinFailure("workflow instance is not recorded")
		}
		return RegisteredDefinition{}, wrapFailure(KindUnavailable, "workflow_preflight", "cannot read workflow definition pin", true, "retry once the database is readable", err)
	}
	return VerifyWorkflowDefinitionPin(registry, pin)
}

// WorkflowActionAvailable performs the fail-closed definition check used by
// action authorization. It does not authorize an action by itself.
func WorkflowActionAvailable(ctx context.Context, s *Store, workID string) (bool, error) {
	return WorkflowActionAvailableWithRegistry(ctx, s, BuiltinWorkflowRegistry(), workID)
}

func WorkflowActionAvailableWithRegistry(ctx context.Context, s *Store, registry DefinitionRegistry, workID string) (bool, error) {
	if _, err := VerifyWorkflowInstanceDefinition(ctx, s, registry, workID); err != nil {
		var failure *Failure
		if failureAs(err, &failure) && failure.Kind == KindInvariantViolation {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func workflowStartPreflight(ctx context.Context, s *Store, request WorkflowStartRequest) error {
	return workflowStartPreflightWithRegistry(ctx, s, BuiltinWorkflowRegistry(), request)
}

func workflowStartPreflightWithRegistry(ctx context.Context, s *Store, registry DefinitionRegistry, request WorkflowStartRequest) error {
	entry, err := VerifyWorkflowDefinitionPin(registry, request.Definition)
	if err != nil {
		return err
	}
	if request.WorkID == "" {
		return newFailure(KindInvalidOperation, "workflow_start", "work ID is empty", false, "supply a work item ID")
	}
	if request.StepID == "" && request.ActionID == "" {
		return nil
	}
	stepID := request.StepID
	if stepID == "" {
		stepID = entry.Definition.StepGraph.StartStep
	}
	if stepID != entry.Definition.StepGraph.StartStep {
		return workflowPinFailure("workflow start does not use the definition start step")
	}
	if !definitionStepAllows(entry.Definition, stepID, request.ActionID) {
		return workflowPinFailure("workflow start action is not declared on the definition start path")
	}
	if request.ActionID != "" || request.Actor.ActorClass != "" {
		if err := ValidateWorkflowActor(request.Actor); err != nil {
			return err
		}
	}
	return nil
}

func workflowResumePreflight(ctx context.Context, s *Store, workID string) error {
	return workflowResumePreflightWithRegistry(ctx, s, BuiltinWorkflowRegistry(), workID)
}

func workflowResumePreflightWithRegistry(ctx context.Context, s *Store, registry DefinitionRegistry, workID string) error {
	_, err := VerifyWorkflowInstanceDefinition(ctx, s, registry, workID)
	return err
}

func WorkflowActionPreflight(ctx context.Context, s *Store, request WorkflowActionPreflightRequest) error {
	return WorkflowActionPreflightWithRegistry(ctx, s, BuiltinWorkflowRegistry(), request)
}

func WorkflowActionPreflightWithRegistry(ctx context.Context, s *Store, registry DefinitionRegistry, request WorkflowActionPreflightRequest) error {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "workflow_action_preflight", "store is not open", false, "open the authority database")
	}
	if registry == nil {
		registry = BuiltinWorkflowRegistry()
	}
	entry, err := VerifyWorkflowInstanceDefinition(ctx, s, registry, request.WorkID)
	if err != nil {
		return err
	}
	var currentStep, state string
	var version int64
	if err := s.db.QueryRowContext(ctx, `SELECT current_step,instance_state,(SELECT version FROM work_items WHERE id=workflow_instances.work_id) FROM workflow_instances WHERE work_id=?`, request.WorkID).Scan(&currentStep, &state, &version); err != nil {
		return wrapFailure(KindUnavailable, "workflow_action_preflight", "cannot read workflow instance state", true, "retry once the database is readable", err)
	}
	if currentStep == "start" {
		currentStep = entry.Definition.StepGraph.StartStep
	}
	if request.StepID != "" && request.StepID != currentStep {
		return workflowPinFailure("workflow action request does not match the current definition step")
	}
	if request.ExpectedVersion > 0 && request.ExpectedVersion != version {
		return versionConflict(SubjectWorkItem, request.WorkID, request.ExpectedVersion, version, false)
	}
	if state == "completed" || state == "cancelled" || state == "superseded" {
		return newFailure(KindInvalidOperation, "workflow_action_preflight", "terminal workflow instance is immutable", false, "start a successor workflow")
	}
	var consequence ActionConsequence
	declaredAction := false
	for _, action := range entry.Definition.ActionDefinitions {
		if action.ID == request.ActionID {
			declaredAction = true
			consequence = action.Consequence
			break
		}
	}
	if !declaredAction {
		return newFailure(KindIllegalLifecycleTransition, "workflow_action_preflight", "workflow action is not declared by the pinned definition", false, "reread_entities")
	}
	if workflowImpactBoundary(request.ActionID, consequence) {
		var breakingNotices int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_impact_notices n JOIN workflow_impact_edges e ON e.work_id=n.edge_owner_work_id AND e.edge_id=n.edge_id WHERE n.target_work_id=? AND n.severity='breaking' AND e.edge_class='hard'`, request.WorkID).Scan(&breakingNotices); err != nil {
			return wrapFailure(KindUnavailable, "workflow_action_preflight", "cannot inspect workflow impact notices", true, "retry once the database is readable", err)
		}
		if breakingNotices != 0 {
			return newFailure(KindInvariantViolation, "workflow_action_preflight", "breaking workflow impact notice blocks consequential execution", false, "reread_entities")
		}
	}
	if consequence == ActionCrossAuthority || consequence == ActionExternalEffect {
		var openConditions int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_external_conditions WHERE work_id=? AND condition_state='open'`, request.WorkID).Scan(&openConditions); err != nil {
			return wrapFailure(KindUnavailable, "workflow_action_preflight", "cannot inspect consequential workflow conditions", true, "retry once the database is readable", err)
		}
		if openConditions != 0 {
			return newFailure(KindNotTerminal, "workflow_action_preflight", "consequential action has unresolved external conditions", false, "reread_entities")
		}
	}
	if !definitionStepAllows(entry.Definition, currentStep, request.ActionID) {
		return newFailure(KindIllegalLifecycleTransition, "workflow_action_preflight", "workflow action is not declared on the current step", false, "reread_entities")
	}
	if err := validateWorkflowActionPayload(entry.Definition, request.ActionID, request.Payload); err != nil {
		return newFailure(KindIllegalLifecycleTransition, "workflow_action_preflight", err.Error(), false, "reread_entities")
	}
	if err := ValidateWorkflowOperatorSelection(ctx, s, request.WorkID, request.ExpectedVersion, request.ActionID, request.SelectedChoice, request.DecisionContextDigest); err != nil {
		return err
	}
	if err := ValidateWorkflowActor(request.Actor); err != nil {
		return err
	}
	actorRef, err := WorkflowActorRef(request.Actor)
	if err != nil {
		return err
	}
	if actorRef != "" {
		var recorded int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_actors WHERE actor_ref=?`, actorRef).Scan(&recorded); err != nil {
			return wrapFailure(KindUnavailable, "workflow_action_preflight", "cannot read workflow actor", true, "retry once the database is readable", err)
		}
		if recorded != 1 && request.ActionID != "record_verdict" {
			return newFailure(KindUnauthorized, "workflow_action_preflight", "workflow actor is not recorded", false, "record the complete actor tuple first")
		}
	}
	return nil
}

// AuthorizeWorkflowAction is the future dispatch ordering seam. It performs
// the complete read-only preflight before invoking any authorization callback;
// it does not append events or enable workflow_action dispatch.
func AuthorizeWorkflowAction(ctx context.Context, s *Store, registry DefinitionRegistry, request WorkflowActionPreflightRequest, authorize func() error) error {
	if request.ConditionResolver != nil || !request.BoundaryNow.IsZero() {
		return AuthorizeWorkflowActionAtBoundary(ctx, s, registry, request, request.ConditionResolver, request.BoundaryNow, authorize)
	}
	if err := WorkflowActionPreflightWithRegistry(ctx, s, registry, request); err != nil {
		return err
	}
	if authorize != nil {
		return authorize()
	}
	return nil
}

// AuthorizeWorkflowActionAtBoundary resolves eligible conditions once before
// authorizing a cross-authority or external-effect action. It intentionally
// leaves ordinary internal SQLite actions untouched.
func AuthorizeWorkflowActionAtBoundary(ctx context.Context, s *Store, registry DefinitionRegistry, request WorkflowActionPreflightRequest, resolver ConditionResolver, now time.Time, authorize func() error) error {
	return AuthorizeWorkflowActionAtBoundaryTx(ctx, s, registry, request, resolver, now, func(*sql.Tx) error {
		if authorize != nil {
			return authorize()
		}
		return nil
	}, func(*sql.Tx) error { return nil })
}

// AuthorizeWorkflowActionAtBoundaryTx is the owning-action transaction
// coordinator. Condition resolution, the second preflight, authorization, and
// the persisted action callback all share one transaction and one fold guard.
// A callback error or mutation failure rolls back every condition event.
func AuthorizeWorkflowActionAtBoundaryTx(ctx context.Context, s *Store, registry DefinitionRegistry, request WorkflowActionPreflightRequest, resolver ConditionResolver, now time.Time, authorize func(*sql.Tx) error, mutate func(*sql.Tx) error) error {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "workflow_action_boundary", "store is not open", false, "open the authority database")
	}
	if mutate == nil {
		return newFailure(KindInvalidOperation, "workflow_action_boundary", "owning action mutation is required", false, "supply the persisted action operation")
	}
	if registry == nil {
		registry = BuiltinWorkflowRegistry()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapFailure(KindUnavailable, "workflow_action_boundary", "cannot begin owning action", true, "retry once the database is writable", err)
	}
	rollback := func(cause error) error { _ = tx.Rollback(); return cause }
	if err := enterFold(ctx, tx); err != nil {
		return rollback(err)
	}
	defer func() { _ = leaveFold(ctx, tx) }()
	entry, err := workflowActionPreflightTx(ctx, tx, registry, request, false)
	if err != nil {
		return rollback(err)
	}
	if workflowActionConsequence(entry.Definition, request.ActionID) != ActionInternalSQLite {
		var open int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_external_conditions WHERE work_id=? AND condition_state='open'`, request.WorkID).Scan(&open); err != nil {
			return rollback(wrapFailure(KindUnavailable, "workflow_action_boundary", "cannot inspect consequential conditions", true, "retry once the database is readable", err))
		}
		if open != 0 {
			if resolver == nil || now.IsZero() {
				return rollback(newFailure(KindNotTerminal, "workflow_action_boundary", "consequential action requires an explicit condition resolver", false, "reread_entities"))
			}
			if _, err := resolveWorkflowConditionsAtBoundaryTx(ctx, tx, request.WorkID, resolver, now); err != nil {
				return rollback(err)
			}
			if err := tx.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, request.WorkID).Scan(&request.ExpectedVersion); err != nil {
				return rollback(wrapFailure(KindUnavailable, "workflow_action_boundary", "cannot reread workflow version after condition resolution", true, "retry once the database is readable", err))
			}
		}
	}
	if _, err := workflowActionPreflightTx(ctx, tx, registry, request, true); err != nil {
		return rollback(err)
	}
	if authorize != nil {
		if err := authorize(tx); err != nil {
			return rollback(err)
		}
	}
	if err := mutate(tx); err != nil {
		return rollback(err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return wrapFailure(KindUnavailable, "workflow_action_boundary", "cannot commit owning action", true, "retry once the database is writable", err)
	}
	return nil
}

func workflowActionConsequence(definition WorkflowDefinition, actionID string) ActionConsequence {
	if actionID == "supersede_contract" {
		return ActionInternalSQLite
	}
	for _, action := range definition.ActionDefinitions {
		if action.ID == actionID {
			return action.Consequence
		}
	}
	return ""
}

func workflowImpactBoundary(actionID string, consequence ActionConsequence) bool {
	return consequence == ActionCrossAuthority || consequence == ActionExternalEffect || actionID == "complete"
}

func workflowActionPreflightTx(ctx context.Context, tx *sql.Tx, registry DefinitionRegistry, request WorkflowActionPreflightRequest, requireTerminalConditions bool) (RegisteredDefinition, error) {
	entry, err := VerifyWorkflowInstanceDefinitionTx(ctx, tx, registry, request.WorkID)
	if err != nil {
		return RegisteredDefinition{}, err
	}
	var currentStep, state string
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT current_step,instance_state,(SELECT version FROM work_items WHERE id=workflow_instances.work_id) FROM workflow_instances WHERE work_id=?`, request.WorkID).Scan(&currentStep, &state, &version); err != nil {
		return RegisteredDefinition{}, wrapFailure(KindUnavailable, "workflow_action_preflight", "cannot read workflow instance state", true, "retry once the database is readable", err)
	}
	if currentStep == "start" {
		currentStep = entry.Definition.StepGraph.StartStep
	}
	if request.StepID != "" && request.StepID != currentStep {
		return RegisteredDefinition{}, workflowPinFailure("workflow action request does not match the current definition step")
	}
	if request.ExpectedVersion > 0 && request.ExpectedVersion != version {
		return RegisteredDefinition{}, versionConflict(SubjectWorkItem, request.WorkID, request.ExpectedVersion, version, true)
	}
	if state == "completed" || state == "cancelled" || state == "superseded" {
		return RegisteredDefinition{}, newFailure(KindInvalidOperation, "workflow_action_preflight", "terminal workflow instance is immutable", false, "start a successor workflow")
	}
	staleRecovery := false
	if request.ActionID == "supersede_contract" {
		if err := checkWorkflowLawRevisionStalenessTx(ctx, tx, request.WorkID); err != nil {
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind != KindStaleLawRevision {
				return RegisteredDefinition{}, err
			}
			staleRecovery = true
		} else {
			return RegisteredDefinition{}, newFailure(KindInvalidOperation, "workflow_action_preflight", "contract recovery is available only for a stale workflow contract", false, "continue the current contract or request terminal work")
		}
	} else if !workflowActionAllowsTerminalRecovery(request) {
		if err := checkWorkflowLawRevisionStalenessTx(ctx, tx, request.WorkID); err != nil {
			return RegisteredDefinition{}, err
		}
	}
	if workflowActionConsequence(entry.Definition, request.ActionID) != ActionInternalSQLite && requireTerminalConditions {
		var open int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_external_conditions WHERE work_id=? AND condition_state='open'`, request.WorkID).Scan(&open); err != nil {
			return RegisteredDefinition{}, wrapFailure(KindUnavailable, "workflow_action_preflight", "cannot inspect consequential workflow conditions", true, "retry once the database is readable", err)
		}
		if open != 0 {
			return RegisteredDefinition{}, newFailure(KindNotTerminal, "workflow_action_preflight", "consequential action has unresolved external conditions", false, "reread_entities")
		}
	}
	if workflowImpactBoundary(request.ActionID, workflowActionConsequence(entry.Definition, request.ActionID)) {
		var breakingNotices int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_impact_notices n JOIN workflow_impact_edges e ON e.work_id=n.edge_owner_work_id AND e.edge_id=n.edge_id WHERE n.target_work_id=? AND n.severity='breaking' AND e.edge_class='hard'`, request.WorkID).Scan(&breakingNotices); err != nil {
			return RegisteredDefinition{}, wrapFailure(KindUnavailable, "workflow_action_preflight", "cannot inspect workflow impact notices", true, "retry once the database is readable", err)
		}
		if breakingNotices != 0 {
			return RegisteredDefinition{}, newFailure(KindInvariantViolation, "workflow_action_preflight", "breaking workflow impact notice blocks consequential execution", false, "reread_entities")
		}
	}
	if !staleRecovery && !definitionStepAllows(entry.Definition, currentStep, request.ActionID) {
		return RegisteredDefinition{}, newFailure(KindIllegalLifecycleTransition, "workflow_action_preflight", "workflow action is not declared on the current step", false, "reread_entities")
	}
	if staleRecovery {
		if err := validateWorkflowContractRecoveryPayload(request.Payload); err != nil {
			return RegisteredDefinition{}, err
		}
	} else if err := validateWorkflowActionPayload(entry.Definition, request.ActionID, request.Payload); err != nil {
		return RegisteredDefinition{}, newFailure(KindIllegalLifecycleTransition, "workflow_action_preflight", err.Error(), false, "reread_entities")
	}
	if err := validateWorkflowOperatorSelectionTx(ctx, tx, registry, request); err != nil {
		return RegisteredDefinition{}, err
	}
	if err := ValidateWorkflowActor(request.Actor); err != nil {
		return RegisteredDefinition{}, err
	}
	actorRef, err := WorkflowActorRef(request.Actor)
	if err != nil {
		return RegisteredDefinition{}, err
	}
	var recorded int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_actors WHERE actor_ref=?`, actorRef).Scan(&recorded); err != nil {
		return RegisteredDefinition{}, wrapFailure(KindUnavailable, "workflow_action_preflight", "cannot read workflow actor", true, "retry once the database is readable", err)
	}
	if recorded != 1 && request.ActionID != "record_verdict" {
		return RegisteredDefinition{}, newFailure(KindUnauthorized, "workflow_action_preflight", "workflow actor is not recorded", false, "record the complete actor tuple first")
	}
	return entry, nil
}

func validateWorkflowActionPayload(definition WorkflowDefinition, actionID string, payload json.RawMessage) error {
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	var fields map[string]json.RawMessage
	if err := decodePredicateStrict(payload, &fields); err != nil {
		return newFailure(KindInvalidPayload, "workflow_action_preflight", "workflow action payload is not one strict JSON object", false, "supply the registered action payload")
	}
	var definitionFields []WorkflowPayloadField
	for _, action := range definition.ActionDefinitions {
		if action.ID == actionID {
			definitionFields = action.Payload.Fields
			break
		}
	}
	allowed := make(map[string]WorkflowPayloadField, len(definitionFields))
	for _, field := range definitionFields {
		allowed[field.Name] = field
	}
	for name := range fields {
		field, ok := allowed[name]
		if !ok {
			// Built-in semantic actions currently carry their closed request union
			// through the workflow envelope rather than duplicating every field in
			// each family definition. The boundary still requires one strict JSON
			// object; family-specific semantic validation owns its fields below.
			if len(definitionFields) == 0 {
				continue
			}
			return newFailure(KindInvalidPayload, "workflow_action_preflight", "workflow action payload contains an undeclared field", false, "use only fields declared by the pinned definition")
		}
		if !validateWorkflowPayloadValue(field, fields[name]) {
			return newFailure(KindInvalidPayload, "workflow_action_preflight", "workflow action payload field has the wrong registered type or bounds", false, "supply the declared action field type")
		}
	}
	for name, field := range allowed {
		if field.Required {
			if _, ok := fields[name]; !ok {
				return newFailure(KindInvalidPayload, "workflow_action_preflight", "workflow action payload omits a required field", false, "supply every required registered action field")
			}
		}
	}
	return nil
}

func validateWorkflowPayloadValue(field WorkflowPayloadField, raw json.RawMessage) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return false
	}
	switch field.ValueType {
	case PayloadString, PayloadRef, PayloadDigest:
		text, ok := value.(string)
		if !ok {
			return false
		}
		if field.ValueType == PayloadRef && !validReference(text) {
			return false
		}
		if field.ValueType == PayloadDigest && !workflowDigestPattern.MatchString(text) {
			return false
		}
		if field.MinLength != nil && int64(len([]rune(text))) < *field.MinLength || field.MaxLength != nil && int64(len([]rune(text))) > *field.MaxLength {
			return false
		}
		return true
	case PayloadStringList:
		values, ok := value.([]any)
		if !ok {
			return false
		}
		if field.MinItems != nil && int64(len(values)) < *field.MinItems || field.MaxItems != nil && int64(len(values)) > *field.MaxItems {
			return false
		}
		seen := make(map[string]struct{}, len(values))
		for _, item := range values {
			text, ok := item.(string)
			if !ok || !validReference(text) {
				return false
			}
			if _, exists := seen[text]; exists {
				return false
			}
			seen[text] = struct{}{}
		}
		return true
	case PayloadInteger:
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		integer, err := number.Int64()
		if err != nil || integer < -2147483648 || integer > 2147483647 {
			return false
		}
		if field.Minimum != nil && integer < *field.Minimum || field.Maximum != nil && integer > *field.Maximum {
			return false
		}
		return true
	case PayloadBoolean:
		_, ok := value.(bool)
		return ok
	default:
		return false
	}
}

func definitionStepAllows(definition WorkflowDefinition, stepID, actionID string) bool {
	if stepID == "" || actionID == "" {
		return false
	}
	for _, step := range definition.StepGraph.Steps {
		if step.ID == stepID {
			return containsString(step.Actions, actionID)
		}
	}
	return false
}

func verifyWorkflowDefinitionPinTx(ctx context.Context, tx *sql.Tx, registry DefinitionRegistry, workID string) (RegisteredDefinition, error) {
	if registry == nil {
		registry = BuiltinWorkflowRegistry()
	}
	var pin WorkflowDefinitionPin
	if err := tx.QueryRowContext(ctx, `SELECT definition_ref,definition_version,definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&pin.Ref, &pin.Version, &pin.Digest); err != nil {
		return RegisteredDefinition{}, wrapFailure(KindInvariantViolation, "workflow_preflight", "workflow definition pin is unavailable", false, "reread_entities", err)
	}
	return VerifyWorkflowDefinitionPin(registry, pin)
}

func preflightWorkflowClaimTx(ctx context.Context, tx *sql.Tx, req ClaimRequest) error {
	if !strings.HasPrefix(req.WorkflowTypeRef, "workflow.") {
		return nil
	}
	entry, err := verifyWorkflowDefinitionPinTx(ctx, tx, BuiltinWorkflowRegistry(), req.WorkID)
	if err != nil {
		return err
	}
	if err := checkWorkflowLawRevisionStalenessTx(ctx, tx, req.WorkID); err != nil {
		return err
	}
	if entry.Definition.Ref != req.WorkflowTypeRef || entry.Definition.Version != int64(req.WorkflowTypeVersion) {
		return workflowPinFailure("workflow claim identity does not match the stored definition pin")
	}
	if entry.Definition.StepGraph.StartStep == "" {
		return workflowPinFailure(fmt.Sprintf("workflow %q has no start step", req.WorkflowTypeRef))
	}
	var currentStep string
	if err := tx.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, req.WorkID).Scan(&currentStep); err != nil {
		return workflowPinFailure("workflow instance is unavailable")
	}
	if req.StepID != currentStep && !(currentStep == "start" && req.StepID == entry.Definition.StepGraph.StartStep) {
		return workflowPinFailure("workflow claim does not match the current definition step")
	}
	return nil
}

func preflightWorkflowOperationTx(ctx context.Context, tx *sql.Tx, opID string) error {
	var workID, workflowRef, stepID string
	var workflowVersion int
	err := tx.QueryRowContext(ctx, `SELECT work_id,workflow_type_ref,workflow_type_version,step_id FROM durable_operations WHERE op_id=? ORDER BY attempt_epoch DESC LIMIT 1`, opID).Scan(&workID, &workflowRef, &workflowVersion, &stepID)
	if err == sql.ErrNoRows {
		return newFailure(KindProjectionNotFound, "workflow_preflight", "workflow operation is not recorded", false, "claim the workflow operation before completing it")
	}
	if err != nil {
		return wrapFailure(KindUnavailable, "workflow_preflight", "cannot read workflow operation identity", true, "retry once the database is readable", err)
	}
	if !strings.HasPrefix(workflowRef, "workflow.") {
		return nil
	}
	entry, err := verifyWorkflowDefinitionPinTx(ctx, tx, BuiltinWorkflowRegistry(), workID)
	if err != nil {
		return err
	}
	var currentStep string
	if err := tx.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&currentStep); err != nil {
		return workflowPinFailure("workflow instance is unavailable")
	}
	if currentStep == "start" {
		currentStep = entry.Definition.StepGraph.StartStep
	}
	if currentStep != stepID || entry.Definition.Version != int64(workflowVersion) || entry.Definition.Ref != workflowRef {
		return workflowPinFailure("workflow operation does not match its pinned definition step")
	}
	return nil
}

func preflightWorkflowOperation(ctx context.Context, s *Store, opID string) error {
	var workID, workflowRef string
	var workflowVersion int
	if err := s.db.QueryRowContext(ctx, `SELECT work_id,workflow_type_ref,workflow_type_version FROM durable_operations WHERE op_id=? ORDER BY attempt_epoch DESC LIMIT 1`, opID).Scan(&workID, &workflowRef, &workflowVersion); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindProjectionNotFound, "workflow_preflight", "workflow operation is not recorded", false, "claim the workflow operation before resuming it")
		}
		return wrapFailure(KindUnavailable, "workflow_preflight", "cannot read workflow operation identity", true, "retry once the database is readable", err)
	}
	if !strings.HasPrefix(workflowRef, "workflow.") {
		return nil
	}
	entry, err := VerifyWorkflowInstanceDefinition(ctx, s, BuiltinWorkflowRegistry(), workID)
	if err != nil {
		return err
	}
	if entry.Definition.Ref != workflowRef || entry.Definition.Version != int64(workflowVersion) {
		return workflowPinFailure("workflow resume identity does not match the stored definition pin")
	}
	return nil
}
