package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"unicode"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

type WorkflowActionPreflightRequest struct {
	WorkID          string
	ExpectedVersion int64
	StepID          string
	ActionID        string
	Payload         json.RawMessage
	Actor           WorkflowActor
	// SessionProjectID is the Project the acting session's authenticated
	// directory resolves. Project-handoff admission binds to it.
	SessionProjectID string
}

// WorkflowActionDefinitionFor answers whether the named action passes preflight.
// It reads a snapshot; the owning boundary revalidates before any effect.
func WorkflowActionDefinitionFor(ctx context.Context, s *Store, request WorkflowActionPreflightRequest) (WorkflowActionDefinition, error) {
	if s == nil || s.db == nil {
		return WorkflowActionDefinition{}, newFailure(KindUnavailable, "workflow_action_preflight", "store is not open", false, "open the authority database")
	}
	ctx, err := s.PrepareWorkflowActionProof(ctx, request.ActionID, request.WorkID)
	if err != nil {
		return WorkflowActionDefinition{}, err
	}
	readTx, err := beginReadTx(ctx, s.db)
	if err != nil {
		return WorkflowActionDefinition{}, wrapFailure(KindUnavailable, "workflow_action_preflight", "cannot begin workflow admission read snapshot", true, "retry once the database is readable", err)
	}
	defer func() { _ = readTx.Rollback() }()
	return workflowActionPreflightTx(ctx, readTx, request)
}

// PrepareWorkflowActionProof establishes, before any transaction for the
// action opens, the knowledge-source proof the action reads inside it.
// Approval verifies the law and Domains the contract binds, including a
// proposed contract that is not active yet; confirm_premise validates Domain
// refs against the committed registry; completion reads no law and needs no
// proof; every other action follows the work's own demand
// (PrepareWorkLawProof). Git runs only here, outside the pool.
// The agent boundary, the preflight, and the owning transaction share this one
// rule, so none refuses for a missing proof another would hold. An
// unreachable or invalid law home refuses: a law or registry boundary has no
// degraded form. A context that already carries a proof passes through; the
// transaction revalidates it against the live heads.
func (s *Store) PrepareWorkflowActionProof(ctx context.Context, actionID, workID string) (context.Context, error) {
	if knowledgeSourceSetProofFrom(ctx) != nil {
		return ctx, nil
	}
	switch actionID {
	case "complete":
		return ctx, nil
	case "approve_contract", "confirm_premise":
		return s.EstablishKnowledgeSourceSetProof(ctx, workID)
	}
	return s.PrepareWorkLawProof(ctx, workID)
}

// AuthorizeWorkflowActionAtBoundaryTx is the owning-action transaction
// coordinator. The preflight, authorization, and the persisted action callback
// share one transaction and one write guard, so a callback error or mutation
// failure commits nothing.
func AuthorizeWorkflowActionAtBoundaryTx(ctx context.Context, s *Store, request WorkflowActionPreflightRequest, authorize func(*Transaction) error, mutate func(*Transaction) error) error {
	return authorizeWorkflowActionAtBoundaryCore(ctx, s, request, nil, authorize, mutate)
}

// AuthorizeWorkflowActionAtBoundaryWithPreflightTx lets an owning dispatcher
// inspect durable idempotency before the version-sensitive preflight. This
// preserves exact replay when concurrent requests share one action key.
func AuthorizeWorkflowActionAtBoundaryWithPreflightTx(ctx context.Context, s *Store, request WorkflowActionPreflightRequest, beforePreflight func(*Transaction) (bool, error), authorize func(*Transaction) error, mutate func(*Transaction) error) error {
	if beforePreflight == nil {
		return AuthorizeWorkflowActionAtBoundaryTx(ctx, s, request, authorize, mutate)
	}
	return authorizeWorkflowActionAtBoundaryCore(ctx, s, request, beforePreflight, authorize, mutate)
}

func authorizeWorkflowActionAtBoundaryCore(ctx context.Context, s *Store, request WorkflowActionPreflightRequest, beforePreflight func(*Transaction) (bool, error), authorize func(*Transaction) error, mutate func(*Transaction) error) (retErr error) {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "workflow_action_boundary", "store is not open", false, "open the authority database")
	}
	if mutate == nil {
		return newFailure(KindInvalidOperation, "workflow_action_boundary", "owning action mutation is required", false, "supply the persisted action operation")
	}
	ctx, err := s.PrepareWorkflowActionProof(ctx, request.ActionID, request.WorkID)
	if err != nil {
		return err
	}
	tx, err := s.beginDurableTx(ctx)
	if err != nil {
		return wrapFailure(KindUnavailable, "workflow_action_boundary", "cannot begin owning action", true, "retry once the database is writable", err)
	}
	transaction := &Transaction{tx: tx.Tx}
	defer tx.finish(&retErr)
	defer func() { transaction.tx = nil }()
	scope, err := beginWrite(ctx, tx.Tx)
	if err != nil {
		return err
	}
	transaction.write = scope
	defer func() { _ = scope.close(ctx) }()
	if beforePreflight != nil {
		handled, err := beforePreflight(transaction)
		if err != nil {
			return err
		}
		if handled {
			if err := scope.close(ctx); err != nil {
				return err
			}
			if err := tx.Commit(); err != nil {
				return err
			}
			return nil
		}
	}
	// Confirmation is admitted by its event constructor in this transaction;
	// evaluating it here too would check the same facts twice per write.
	action, err := workflowActionPreflightCoreTx(ctx, tx.Tx, request, false)
	if err != nil {
		return err
	}
	if action.Consequence != ActionInternalSQLite {
		// Project-session handoff admission: a
		// session whose receiving Project holds an unconsumed or stale
		// frontier executes no managed external effect until a session of
		// that Project consumes the addressed handoff under the active
		// contract. A work with no handoffs is unaffected.
		if err := RefuseUnconsumedProjectHandoffTx(ctx, tx.Tx, request.WorkID, request.SessionProjectID, request.Actor.SessionRef); err != nil {
			return err
		}
	}
	if authorize != nil {
		if err := authorize(transaction); err != nil {
			return err
		}
	}
	if err := mutate(transaction); err != nil {
		return err
	}
	if err := scope.close(ctx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

// workflowPlanActionAdmissionTx is shared with the dispatcher: it reads one
// snapshot and selects the action's frontier step without mutating the plan.
func workflowPlanActionAdmissionTx(ctx context.Context, tx *sql.Tx, workID string, expectedVersion int64, actionID string, payload json.RawMessage) (WorkflowActionDefinition, *WorkflowStepPlan, string, error) {
	action, err := catalogAction(actionID)
	if err != nil {
		return action, nil, "", err
	}
	switch actionID {
	case "record_proposal", "record_alignment", "record_design":
		return action, nil, "", newFailure(KindIllegalLifecycleTransition, "workflow_action", "retired workflow action", false, "use an action declared by the current plan")
	}
	fields, err := validateWorkflowPlanActionPayload(actionID, payload)
	if err != nil {
		return action, nil, "", err
	}
	step, state, err := readWorkflowActionStateTx(ctx, tx, workID, expectedVersion)
	if err != nil {
		return action, nil, "", err
	}
	plan, err := readWorkflowPlanTx(ctx, tx, workID)
	if err != nil {
		return action, nil, "", err
	}
	// A terminal instance admits only a completed item's record_delivery
	// replacement over its retained delivery record (§4.2): the write keeps
	// the terminal lifecycle, state and plan, and carries the plan step that
	// declares the action.
	if state == "completed" || state == "cancelled" {
		owner, err := workflowPlanTerminalAdmissionTx(ctx, tx, workID, actionID, state, step, plan)
		return action, plan, owner, err
	}
	if _, err := readWorkflowPreludeTx(ctx, tx, workID); err != nil {
		return action, nil, "", err
	}
	step, err = workflowPlanFrontierAdmissionTx(ctx, tx, workID, actionID, step, plan, fields)
	if err != nil {
		return action, plan, step, err
	}
	if actionID == "bind_evidence" {
		if _, err := activeWorkflowContractVersion(ctx, tx, workID, "workflow_action"); err != nil {
			return action, plan, step, err
		}
	}
	return action, plan, step, nil
}

func workflowPlanTerminalAdmissionTx(ctx context.Context, tx *sql.Tx, workID, actionID, state, step string, plan *WorkflowStepPlan) (string, error) {
	if state == "completed" && actionID == "record_delivery" {
		owner := "delivery"
		if plan != nil {
			owner = workflowPlanDeclaringStep(plan, actionID)
		}
		records, recordsErr := workflowCurrentRecords(ctx, tx, workID)
		if recordsErr != nil {
			return owner, recordsErr
		}
		if plan == nil {
			var exists bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_delivery_state WHERE work_id=?)`, workID).Scan(&exists); err != nil {
				return owner, workflowProjectionError(err, "cannot read retained delivery")
			}
			records["record_delivery"] = exists
		}
		if owner != "" && records["record_delivery"] {
			return owner, nil
		}
	}
	return step, newFailure(KindInvalidOperation, "workflow_action", "terminal workflow cannot change", false, "reread the work item")
}

// workflowPlanCompleteConfirmationTx reports the declaring step that carries
// the plan-level premise confirmation and whether the active
// contract's confirmation is already recorded. A completed plan has no
// frontier, yet completion requires that confirmation, so the declaring step
// keeps the action while the confirmation is missing. Admission uses the step
// while it is missing; the action_completed fold uses it once the paired
// premise_confirmed event has recorded it. Every other action, any incomplete
// plan, and a work with no active contract return no step.
func workflowPlanCompleteConfirmationTx(ctx context.Context, q queryer, workID string, plan *WorkflowStepPlan, actionID string) (string, bool, error) {
	if plan == nil || actionID != "confirm_premise" || !WorkflowStepPlanComplete(*plan) {
		return "", false, nil
	}
	version, err := activeWorkflowContractVersion(ctx, q, workID, "workflow_action")
	if err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, err
	}
	var confirmed bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_premise_confirmations WHERE work_id=? AND contract_version=?)`, workID, version).Scan(&confirmed); err != nil {
		return "", false, wrapFailure(KindUnavailable, "workflow_action", "cannot read the premise confirmation", true, "retry once the database is readable", err)
	}
	return workflowPlanDeclaringStep(plan, actionID), confirmed, nil
}

// workflowPlanCompletePrerequisiteTx reports the declaring step that carries
// a completion-prerequisite action on a completed plan, whether the
// prerequisite is recorded, and whether the action's own fold records it
// after admission. On a completed plan the declaring step keeps admitting the
// action until the prerequisite is recorded: approve_contract's paired
// contract_approved event folds first, and record_delivery records the
// delivery assertion inside its own fold, so both folds admit while the
// prerequisite is still missing and no other action can record the first
// one. Once the prerequisite is recorded, the frontier and retained-record
// rules own the action again.
func workflowPlanCompletePrerequisiteTx(ctx context.Context, q queryer, workID string, plan *WorkflowStepPlan, actionID string) (string, bool, bool, error) {
	if plan == nil || !WorkflowStepPlanComplete(*plan) {
		return "", false, false, nil
	}
	var (
		recorded       bool
		afterAdmission bool
	)
	switch actionID {
	case "approve_contract":
		_, err := activeWorkflowContractVersion(ctx, q, workID, "workflow_action")
		if err != nil && err != sql.ErrNoRows {
			return "", false, false, err
		}
		recorded = err == nil
	case "record_delivery":
		if plan.Type == WorkflowStepPlanResearch {
			return "", false, false, nil
		}
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_delivery_state WHERE work_id=?)`, workID).Scan(&recorded); err != nil {
			return "", false, false, wrapFailure(KindUnavailable, "workflow_action", "cannot read the delivery assertion", true, "retry once the database is readable", err)
		}
		afterAdmission = true
	default:
		return "", false, false, nil
	}
	return workflowPlanDeclaringStep(plan, actionID), recorded, afterAdmission, nil
}

// workflowPlanCompleteActionTx shares the completed-plan admission state
// between preflight, projection and folding. Paired events record the contract
// and confirmation before action completion; delivery is recorded afterwards.
func workflowPlanCompleteActionTx(ctx context.Context, q queryer, workID string, plan *WorkflowStepPlan, actionID string, folding bool) (string, error) {
	owner, recorded, afterAdmission, err := workflowPlanCompletePrerequisiteTx(ctx, q, workID, plan, actionID)
	if err != nil {
		return "", err
	}
	if actionID == "confirm_premise" {
		owner, recorded, err = workflowPlanCompleteConfirmationTx(ctx, q, workID, plan, actionID)
		if err != nil {
			return "", err
		}
	}
	if owner != "" && recorded == (folding && !afterAdmission) {
		return owner, nil
	}
	return "", nil
}

func workflowPlanFrontierAdmissionTx(ctx context.Context, tx *sql.Tx, workID, actionID, step string, plan *WorkflowStepPlan, fields map[string]json.RawMessage) (string, error) {
	switch actionID {
	case "record_intake", "record_clarify", "pick_type", "insert_step":
	case "complete_step", "record_review":
		step, _ = workflowFieldString(fields, "step_id")
		return step, workflowPlanStepAttemptAdmission(plan, step, actionID)
	case "complete":
		if plan == nil || !WorkflowStepPlanComplete(*plan) {
			return step, newFailure(KindIllegalLifecycleTransition, "workflow_action", "workflow plan is not complete", false, "complete every plan step")
		}
		contractVersion, contractErr := completionActiveContractVersion(ctx, tx, workID)
		if contractErr != nil {
			return step, contractErr
		}
		if err := requireWorkflowCompletionFactsTx(ctx, tx, workID, plan.Type, contractVersion); err != nil {
			return step, err
		}
	default:
		if !workflowPlanAllowsAction(plan, step, actionID) {
			owner, err := workflowPlanCompleteActionTx(ctx, tx, workID, plan, actionID, false)
			if err != nil {
				return step, err
			}
			if owner != "" {
				return owner, nil
			}
			owner = ""
			if workflowRecordAction(actionID) && plan != nil {
				owner = workflowPlanDeclaringStep(plan, actionID)
			}
			if owner == "" {
				return step, newFailure(KindIllegalLifecycleTransition, "workflow_action", "action is not declared on a frontier step", false, "reread the workflow plan")
			}
			records, recordsErr := workflowCurrentRecords(ctx, tx, workID)
			if recordsErr != nil {
				return step, recordsErr
			}
			if !records[actionID] {
				return step, newFailure(KindIllegalLifecycleTransition, "workflow_action", "action has no current record to replace", false, "record it on its frontier step first")
			}
			step = owner
		}
	}
	return step, nil
}

func workflowPlanStepAttemptAdmission(plan *WorkflowStepPlan, step, actionID string) error {
	if plan == nil {
		return newFailure(KindIllegalLifecycleTransition, "workflow_action", "workflow has no plan", false, "pick a workflow type")
	}
	item, found := stepByID(plan.Steps, step)
	if !found || (len(item.Attempts) == 0 && !workflowPlanAllowsAction(plan, step, "complete_step")) {
		return newFailure(KindIllegalLifecycleTransition, "workflow_action", "step is not a frontier or completed step", false, "reread the workflow plan")
	}
	if actionID == "record_review" && !item.Reviewed {
		return newFailure(KindIllegalLifecycleTransition, "workflow_action", "step does not require review", false, "record review only for a reviewed step")
	}
	return nil
}

func workflowActionPreflightTx(ctx context.Context, tx *sql.Tx, request WorkflowActionPreflightRequest) (WorkflowActionDefinition, error) {
	return workflowActionPreflightCoreTx(ctx, tx, request, true)
}

func workflowActionPreflightCoreTx(ctx context.Context, tx *sql.Tx, request WorkflowActionPreflightRequest, checkConfirmation bool) (WorkflowActionDefinition, error) {
	action, plan, currentStep, err := workflowPlanActionAdmissionTx(ctx, tx, request.WorkID, request.ExpectedVersion, request.ActionID, request.Payload)
	if err != nil {
		return action, err
	}
	if request.StepID != "" && request.StepID != currentStep {
		return action, newFailure(KindIllegalLifecycleTransition, "workflow_action_preflight", "workflow action request does not match the current plan step", false, "reread the workflow plan")
	}
	if err := ValidateWorkflowActor(request.Actor); err != nil {
		return action, err
	}
	if _, err := WorkflowActorRef(request.Actor); err != nil {
		return action, err
	}
	if checkConfirmation && request.ActionID == "confirm_premise" {
		fields, err := workflowActionObject(request.Payload)
		if err != nil {
			return action, err
		}
		actor, err := WorkflowActorRef(request.Actor)
		if err != nil {
			return action, err
		}
		if _, err := workflowConfirmationAdmissionTx(ctx, tx, plan.Type, request.WorkID, actor, fields); err != nil {
			return action, err
		}
	}
	return action, nil
}

// workflowLivePayloadSchemaDocument resolves declared schema references
// against the generated public authoring document.
func workflowLivePayloadSchemaDocument() map[string]any {
	return payloadschema.Document()
}

func validateWorkflowPayloadStructure(actionID string, definitionFields []WorkflowPayloadField, allowed map[string]WorkflowPayloadField, fields map[string]json.RawMessage) error {
	// The structural scan runs before any per-field value check and refuses
	// once for the whole object: missing required fields in declared order,
	// then undeclared fields sorted by name, each segment keeping the
	// per-field message. A payload missing N fields therefore costs one
	// retry, not N, and the sorted undeclared walk reports the same fields
	// in the same order on every run.
	missing := make([]string, 0)
	for _, field := range definitionFields {
		if field.Required {
			if _, ok := fields[field.Name]; !ok {
				missing = append(missing, field.Name)
			}
		}
	}
	undeclared := make([]string, 0)
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		if _, ok := allowed[name]; !ok {
			undeclared = append(undeclared, name)
		}
	}
	if len(missing) > 0 || len(undeclared) > 0 {
		segments := make([]string, 0)
		for _, name := range missing {
			segments = append(segments, fmt.Sprintf("workflow action payload field %q is required for action %q", name, actionID))
		}
		for _, name := range undeclared {
			segments = append(segments, fmt.Sprintf("workflow action payload field %q is not declared for action %q", name, actionID))
		}
		remedy := "supply every required registered action field"
		if len(undeclared) > 0 {
			if len(missing) > 0 {
				remedy = "supply every required registered action field and use only fields declared by the pinned definition"
			} else {
				remedy = "use only fields declared by the pinned definition"
			}
		}
		return newFailure(KindInvalidPayload, "workflow_action_preflight", strings.Join(segments, "; "), false, remedy)
	}
	return nil
}

// validateWorkflowPayloadCrossFieldRules enforces the legal input
// combinations the action's ENGINE registry
// declaration names — the same single declaration (resolved through
// workflowActionCrossField) the generated published variants build their
// closed branches from, so a payload any published variant refuses, core
// admission refuses too, and vice versa. The rules are
// version-invariant engine law: they bind whatever action version the
// pinned definition carries, exactly as the released engine guard table
// always did.
func validateWorkflowPayloadCrossFieldRules(actionID string, fields map[string]json.RawMessage) error {
	declaration := workflowActionCrossField(actionID)
	if combination, field, requires, violated := workflowPayloadCombinationViolation(declaration, fields); violated {
		remedy := combination.Remedy
		if remedy == "" {
			remedy = "supply the fields the declared " + combination.Field + " value requires"
		}
		if requires {
			return newFailure(KindInvalidPayload, "workflow_action_preflight", fmt.Sprintf("%s %s %s requires %s", actionID, combination.Field, combination.Value, field), false, remedy)
		}
		return newFailure(KindInvalidPayload, "workflow_action_preflight", fmt.Sprintf("%s %s %s cannot carry %s", actionID, combination.Field, combination.Value, field), false, remedy)
	}
	if match, fieldKind, objectKind, violated := workflowPayloadKindMatchViolation(declaration, fields); violated {
		return workflowPayloadKindMatchRefusal(actionID, match, fieldKind, objectKind)
	}
	return nil
}

// workflowPayloadCombinationViolation evaluates the supplied fields against
// the declaration's legal input combinations and reports the first broken
// rule: which combination, the requires/forbids field that broke it, and
// whether the broken side is a required (absent) or forbidden (present)
// field. Callers render their own typed refusal; this function and the
// declaration are the rule's only owners.
func workflowPayloadCombinationViolation(declaration WorkflowActionCrossField, fields map[string]json.RawMessage) (combination WorkflowPayloadCombination, field string, requires bool, violated bool) {
	for _, candidate := range declaration.Combinations {
		value, ok := workflowFieldString(fields, candidate.Field)
		if !ok || value != candidate.Value {
			continue
		}
		for _, name := range candidate.Requires {
			if _, present := fields[name]; !present {
				return candidate, name, true, true
			}
		}
		for _, name := range candidate.Forbids {
			if _, present := fields[name]; present {
				return candidate, name, false, true
			}
		}
	}
	return WorkflowPayloadCombination{}, "", false, false
}

// workflowPayloadKindMatchViolation evaluates the declaration's nested
// discriminator equalities against the supplied fields and reports the first
// broken match: which match, the scalar value, and the object's
// discriminator value. The caller renders the field-named typed refusal;
// this function and the declaration are the rule's only owners.
func workflowPayloadKindMatchViolation(declaration WorkflowActionCrossField, fields map[string]json.RawMessage) (match WorkflowPayloadKindMatch, fieldKind, objectKind string, violated bool) {
	for _, candidate := range declaration.KindMatches {
		scalar, ok := workflowFieldString(fields, candidate.Field)
		if !ok {
			continue
		}
		raw, present := fields[candidate.Object]
		if !present {
			continue
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil {
			continue
		}
		kindRaw, carries := object[candidate.Discriminator]
		if !carries {
			continue
		}
		if json.Unmarshal(kindRaw, &objectKind) != nil || objectKind == "" {
			continue
		}
		if scalar != objectKind {
			return candidate, scalar, objectKind, true
		}
	}
	return WorkflowPayloadKindMatch{}, "", "", false
}

func workflowPayloadKindMatchRefusal(actionID string, match WorkflowPayloadKindMatch, fieldKind, objectKind string) error {
	remedy := match.Remedy
	if remedy == "" {
		remedy = "supply the " + match.Object + " whose " + match.Discriminator + " equals " + match.Field
	}
	return newFailure(KindInvalidPayload, "workflow_action_preflight", fmt.Sprintf("%s field %q is %q but field %q carries %s %q", actionID, match.Field, fieldKind, match.Object, match.Discriminator, objectKind), false, remedy)
}

func validateEvidenceBindingReferences(actionID string, fields map[string]json.RawMessage) error {
	if actionID != "bind_evidence" {
		return nil
	}
	for _, name := range []string{"evidence_ref", "immutable_subject_ref"} {
		raw, present := fields[name]
		if !present {
			continue
		}
		var value string
		if json.Unmarshal(raw, &value) != nil || !ValidReference(value) {
			return newFailure(KindInvalidPayload, "workflow_action_preflight", fmt.Sprintf("workflow action payload field %q must satisfy the reference rule: 2 to 128 bytes with no Unicode White_Space", name), false, "supply a bounded reference with no Unicode White_Space")
		}
	}
	return nil
}

// validateWorkflowPayloadSchema enforces the structure a payload field already
// declares by name. The registry carries schema_ref for its object and array
// fields, and the agent boundary resolves the same schema, so one declaration
// has one enforcement authority and every refusal is one a published contract
// states.
//
// The document argument is the public authoring document. A declared name
// that the document does not carry fails closed. A declaration pointing at a schema
// nobody generated enforces nothing, and silently passing it would admit a
// payload no contract checked.
func validateWorkflowPayloadSchema(document map[string]any, field WorkflowPayloadField, raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	// item_ref names the contract for each element. For a string list it
	// selects a built-in item kind, which validWorkflowPayloadListItem already
	// applied; for an array it names a generated schema.
	if field.ValueType == PayloadArray && field.ItemRef != "" {
		return validateWorkflowPayloadItems(document, field, raw)
	}
	if field.SchemaRef == "" {
		return nil
	}
	if !payloadschema.HasIn(document, field.SchemaRef) {
		return newFailure(KindInvalidPayload, "workflow_action_preflight", fmt.Sprintf("workflow action payload field %q declares schema %q, which is not generated", field.Name, field.SchemaRef), false, "regenerate the payload contracts or correct the declared schema reference")
	}
	// An array field frozen before item_ref could name a generated schema
	// describing one element rather than the whole value; those definitions
	// cannot be edited, so the element reading is preserved for them. A
	// definition written now chooses which it means by item_ref or schema_ref.
	if field.ValueType == PayloadArray && !payloadschema.DescribesArrayIn(document, field.SchemaRef) {
		return validateWorkflowPayloadItems(document, WorkflowPayloadField{Name: field.Name, ValueType: PayloadArray, ItemRef: field.SchemaRef}, raw)
	}
	if err := payloadschema.ValidateInDocument(document, field.SchemaRef, raw); err != nil {
		return newFailure(KindInvalidPayload, "workflow_action_preflight", fmt.Sprintf("workflow action payload field %q does not satisfy its declared schema %q: %v", field.Name, field.SchemaRef, err), false, "supply a value the declared field schema accepts")
	}
	return nil
}

// validateWorkflowPayloadItems checks every element of an array field against
// the schema its item reference names.
func validateWorkflowPayloadItems(document map[string]any, field WorkflowPayloadField, raw json.RawMessage) error {
	if !payloadschema.HasIn(document, field.ItemRef) {
		return newFailure(KindInvalidPayload, "workflow_action_preflight", fmt.Sprintf("workflow action payload field %q declares item schema %q, which is not generated", field.Name, field.ItemRef), false, "regenerate the payload contracts or correct the declared item schema reference")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return newFailure(KindInvalidPayload, "workflow_action_preflight", fmt.Sprintf("workflow action payload field %q is not one JSON array", field.Name), false, "supply the declared action field type and bounds")
	}
	for index, item := range items {
		if err := payloadschema.ValidateInDocument(document, field.ItemRef, item); err != nil {
			return newFailure(KindInvalidPayload, "workflow_action_preflight", fmt.Sprintf("workflow action payload field %q item %d does not satisfy its declared schema %q: %v", field.Name, index, field.ItemRef, err), false, "supply items the declared item schema accepts")
		}
	}
	return nil
}

// validateWorkflowPayloadValue enforces the declared value type and bounds
// against the generated public authoring document.
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
		return validateWorkflowPayloadText(field, value)
	case PayloadStringList:
		return validateWorkflowPayloadStringList(field, value)
	case PayloadInteger:
		return validateWorkflowPayloadInteger(field, value)
	case PayloadBoolean:
		_, ok := value.(bool)
		return ok
	case PayloadObject:
		// An object field must decode to exactly one strict JSON
		// object. UseNumber above preserves numeric literals, so the
		// decoded form is map[string]any (with json.Number values); any
		// other JSON type — array, scalar, null — fails closed. Structural
		// bounds beyond object-ness live in the contract schema, which
		// the generator embeds; this gate only enforces the registry's
		// declaration that the value is one object.
		_, ok := value.(map[string]any)
		return ok
	case PayloadArray:
		values, ok := value.([]any)
		if !ok {
			return false
		}
		return (field.MinItems == nil || int64(len(values)) >= *field.MinItems) && (field.MaxItems == nil || int64(len(values)) <= *field.MaxItems)
	default:
		return false
	}
}

func validateWorkflowPayloadText(field WorkflowPayloadField, value any) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	if field.ValueType == PayloadRef && !ValidReference(text) {
		return false
	}
	if field.ValueType == PayloadDigest && !workflowDigestPattern.MatchString(text) {
		return false
	}
	if field.NonBlank && strings.TrimSpace(text) == "" || slices.Contains(field.Forbidden, text) {
		return false
	}
	if len(field.Enum) != 0 && !slices.Contains(field.Enum, text) {
		return false
	}
	// String bounds count UTF-8 bytes, not runes: every enforcing
	// workflowString guard counts bytes, so the declared limits carry
	// the enforcing owner's unit and a schema-bound payload cannot be
	// refused by a deeper guard for length alone. JSON Schema
	// minLength/maxLength count code points — that published
	// representation limit is recorded beside the corpus tests.
	if field.MinLength != nil && int64(len(text)) < *field.MinLength || field.MaxLength != nil && int64(len(text)) > *field.MaxLength {
		return false
	}
	return true
}

func validateWorkflowPayloadStringList(field WorkflowPayloadField, value any) bool {
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
		if !ok || !validWorkflowPayloadListItem(field.ItemRef, text) || len(field.Enum) != 0 && !slices.Contains(field.Enum, text) {
			return false
		}
		if _, exists := seen[text]; exists {
			return false
		}
		seen[text] = struct{}{}
	}
	return true
}

func validateWorkflowPayloadInteger(field WorkflowPayloadField, value any) bool {
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
}

func validWorkflowPayloadListItem(itemRef, value string) bool {
	switch itemRef {
	case "law_id":
		runes := []rune(value)
		return len(runes) >= 2 && len(runes) <= 256 && !unicode.IsSpace(runes[0]) && !unicode.IsSpace(runes[len(runes)-1])
	case "proposal_affected_text":
		return validWorkflowProseItem(value, 256)
	case "proposal_text":
		return validWorkflowProseItem(value, 512)
	case "", "reference":
		return ValidReference(value)
	default:
		document := payloadschema.Document()
		if !payloadschema.HasIn(document, itemRef) {
			return false
		}
		raw, err := json.Marshal(value)
		return err == nil && payloadschema.ValidateInDocument(document, itemRef, raw) == nil
	}
}

func validWorkflowProseItem(value string, upper int) bool {
	return len([]rune(value)) >= 1 && len([]rune(value)) <= upper && strings.TrimSpace(value) != ""
}
