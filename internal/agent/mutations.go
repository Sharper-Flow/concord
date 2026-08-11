package agent

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

type approvalInput struct {
	ApprovalRef string `json:"approval_ref"`
}
type mutationMembership struct {
	ProjectID string `json:"project_id"`
	Role      string `json:"role"`
}
type captureMutationInput struct {
	Title           string         `json:"title"`
	ValueStatement  string         `json:"value_statement"`
	Kind            string         `json:"kind"`
	ProjectIDs      []string       `json:"project_ids"`
	Priority        int64          `json:"priority"`
	Tags            []string       `json:"tags"`
	ComponentID     string         `json:"component_id"`
	WorkflowTypeRef string         `json:"workflow_type_ref"`
	ExternalRef     string         `json:"external_ref"`
	IdempotencyKey  string         `json:"idempotency_key"`
	Approval        *approvalInput `json:"approval"`
}
type reviseMutationInput struct {
	WorkID          string   `json:"work_id"`
	ExpectedVersion int64    `json:"expected_version"`
	Title           string   `json:"title"`
	ValueStatement  string   `json:"value_statement"`
	Kind            string   `json:"kind"`
	Priority        int64    `json:"priority"`
	Tags            []string `json:"tags"`
	ComponentID     string   `json:"component_id"`
	WorkflowTypeRef string   `json:"workflow_type_ref"`
	Reason          string   `json:"reason"`
	IdempotencyKey  string   `json:"idempotency_key"`
}
type lifecycleMutationInput struct {
	WorkID          string         `json:"work_id"`
	ExpectedVersion int64          `json:"expected_version"`
	Target          string         `json:"target"`
	Reason          string         `json:"reason"`
	IdempotencyKey  string         `json:"idempotency_key"`
	Evidence        []EvidenceRef  `json:"evidence"`
	Approval        *approvalInput `json:"approval"`
}
type actionMutationInput struct {
	WorkID                string          `json:"work_id"`
	ExpectedVersion       int64           `json:"expected_version"`
	ActionID              string          `json:"action_id"`
	SelectedChoice        string          `json:"selected_choice"`
	DecisionContextDigest string          `json:"decision_context_digest"`
	Fields                json.RawMessage `json:"fields"`
	IdempotencyKey        string          `json:"idempotency_key"`
	Evidence              []EvidenceRef   `json:"evidence"`
	Approval              *approvalInput  `json:"approval"`
}
type membershipsMutationInput struct {
	WorkID          string               `json:"work_id"`
	ExpectedVersion int64                `json:"expected_version"`
	Memberships     []mutationMembership `json:"memberships"`
	IdempotencyKey  string               `json:"idempotency_key"`
	Approval        *approvalInput       `json:"approval"`
}
type linkMutationInput struct {
	FromWorkID          string         `json:"from_work_id"`
	ToWorkID            string         `json:"to_work_id"`
	FromExpectedVersion int64          `json:"from_expected_version"`
	ToExpectedVersion   int64          `json:"to_expected_version"`
	Kind                string         `json:"kind"`
	Reason              string         `json:"reason"`
	IdempotencyKey      string         `json:"idempotency_key"`
	Approval            *approvalInput `json:"approval"`
}
type unlinkVersion struct {
	WorkID  string `json:"work_id"`
	Version int64  `json:"version"`
}
type unlinkMutationInput struct {
	RelationID       string          `json:"relation_id"`
	ExpectedVersions []unlinkVersion `json:"expected_versions"`
	Reason           string          `json:"reason"`
	IdempotencyKey   string          `json:"idempotency_key"`
	Approval         *approvalInput  `json:"approval"`
}
type supersedeMutationInput struct {
	PredecessorID       string         `json:"predecessor_id"`
	SuccessorID         string         `json:"successor_id"`
	PredecessorExpected int64          `json:"predecessor_expected_version"`
	SuccessorExpected   int64          `json:"successor_expected_version"`
	Reason              string         `json:"reason"`
	IdempotencyKey      string         `json:"idempotency_key"`
	Approval            *approvalInput `json:"approval"`
	Evidence            []EvidenceRef  `json:"evidence"`
}
type restoreMutationInput struct {
	PredecessorID          string         `json:"predecessor_id"`
	PredecessorExpected    int64          `json:"predecessor_expected_version"`
	SuccessorID            string         `json:"successor_id"`
	SuccessorExpected      int64          `json:"successor_expected_version"`
	ReplacementSuccessorID string         `json:"replacement_successor_id"`
	ReplacementExpected    int64          `json:"replacement_successor_expected_version"`
	Instruction            string         `json:"instruction"`
	Reason                 string         `json:"reason"`
	IdempotencyKey         string         `json:"idempotency_key"`
	Approval               *approvalInput `json:"approval"`
	Evidence               []EvidenceRef  `json:"evidence"`
}
type compactPublishInput struct {
	WorkID          string         `json:"work_id"`
	ExpectedVersion int64          `json:"expected_version"`
	Content         string         `json:"content"`
	ContentDigest   string         `json:"content_digest"`
	HomeProjectID   string         `json:"home_project_id"`
	HomeLocatorID   string         `json:"home_locator_id"`
	IdempotencyKey  string         `json:"idempotency_key"`
	Approval        *approvalInput `json:"approval"`
	Evidence        []EvidenceRef  `json:"evidence"`
}
type compactReconcileInput struct {
	OperationID              string         `json:"operation_id"`
	ExpectedOperationVersion int64          `json:"expected_operation_version"`
	WorkID                   string         `json:"work_id"`
	ExpectedWorkVersion      int64          `json:"expected_work_version"`
	ExpectedProofDigest      string         `json:"expected_proof_digest"`
	IdempotencyKey           string         `json:"idempotency_key"`
	Approval                 *approvalInput `json:"approval"`
	Evidence                 []EvidenceRef  `json:"evidence"`
}

type mutationEffect func(context.Context, *sql.Tx, Grant) (json.RawMessage, []string, []ChangedRef, error)

func (r runtime) replayMutationBeforeScope(ctx context.Context, base Envelope, raw []byte, grant Grant, op ContractOperation) (Envelope, bool, error) {
	key := idempotencyKey(raw)
	if key == "" {
		return Envelope{}, false, nil
	}
	digest := mutationDigest(r.Tool, r.Operation, r.Envelope, raw)
	operationKind := r.Operation
	if r.Tool == "concord_work_compact" {
		operationKind = "claim"
	}
	var storedDigest, opID, payload, changed, scopeJSON string
	err := r.Store.DB().QueryRowContext(ctx, `SELECT canonical_digest,op_id,COALESCE(result_payload,''),changed_refs,authorized_scope_snapshot FROM idempotency_records WHERE principal_ref=? AND tool=? AND operation_kind=? AND idempotency_key=?`, grant.PrincipalRef, r.Tool, operationKind, key).Scan(&storedDigest, &opID, &payload, &changed, &scopeJSON)
	if err == sql.ErrNoRows {
		return Envelope{}, false, nil
	}
	if err != nil {
		return Envelope{}, false, err
	}
	if r.Tool == "concord_work_compact" {
		var accepted string
		if err := r.Store.DB().QueryRowContext(ctx, `SELECT accepted_inputs_digest FROM durable_operations WHERE op_id=? ORDER BY attempt_epoch DESC LIMIT 1`, opID).Scan(&accepted); err != nil {
			return Envelope{}, false, err
		}
		storedDigest = accepted
	}
	if storedDigest != digest {
		return coreError(base, "idempotency_conflict", "idempotency key was reused with a different canonical request", "retry_same_request", false), true, nil
	}
	var authorizedScope map[string]any
	if scopeJSON != "" {
		_ = json.Unmarshal([]byte(scopeJSON), &authorizedScope)
	}
	if !scopeWithinGrant(authorizedScope, grant) {
		return coreError(base, "unauthorized", "original mutation scope is no longer authorized by the current grant", "contact_operator", false), true, nil
	}
	if op.ID == "concord_work_transition.workflow_action" {
		step, stepErr := store.Step(ctx, r.Store, opID)
		if stepErr != nil {
			return Envelope{}, false, stepErr
		}
		base.Replayed = true
		base.ResolvedScope = scopeFromMap(authorizedScope)
		replay, replayErr := r.replayWorkflowAction(base, step, changed)
		if replayErr != nil {
			return Envelope{}, false, replayErr
		}
		if replay.Outcome == OutcomeError {
			return replay, true, nil
		}
		if _, err := r.Store.DB().ExecContext(ctx, `UPDATE idempotency_records SET replayed_count=replayed_count+1,last_observed_at=? WHERE principal_ref=? AND tool=? AND operation_kind=? AND idempotency_key=?`, r.Authority.now().Format(time.RFC3339Nano), grant.PrincipalRef, r.Tool, operationKind, key); err != nil {
			return Envelope{}, false, err
		}
		return replay, true, nil
	}
	base.Replayed = true
	base.ResolvedScope = scopeFromMap(authorizedScope)
	if r.Tool != "concord_work_compact" {
		var refs []ChangedRef
		_ = json.Unmarshal([]byte(changed), &refs)
		response := r.mutationResult(base, json.RawMessage(payload), refs, nil)
		if response.Outcome == OutcomeError {
			return response, true, nil
		}
		if _, err := r.Store.DB().ExecContext(ctx, `UPDATE idempotency_records SET replayed_count=replayed_count+1,last_observed_at=? WHERE principal_ref=? AND tool=? AND operation_kind=? AND idempotency_key=?`, r.Authority.now().Format(time.RFC3339Nano), grant.PrincipalRef, r.Tool, operationKind, key); err != nil {
			return Envelope{}, false, err
		}
		return response, true, nil
	}
	step, err := store.Step(ctx, r.Store, opID)
	if err != nil {
		return Envelope{}, false, err
	}
	if step.ResultKind == store.ResultCompleted {
		refs := decodeChangedRefs(step.ChangedRefs)
		response := r.mutationResult(base, json.RawMessage(step.ResultPayload), refs, nil)
		if response.Outcome == OutcomeError {
			return response, true, nil
		}
		if _, err := r.Store.DB().ExecContext(ctx, `UPDATE idempotency_records SET replayed_count=replayed_count+1,last_observed_at=? WHERE principal_ref=? AND tool=? AND operation_kind=? AND idempotency_key=?`, r.Authority.now().Format(time.RFC3339Nano), grant.PrincipalRef, r.Tool, operationKind, key); err != nil {
			return Envelope{}, false, err
		}
		return response, true, nil
	}
	if _, err := r.Store.DB().ExecContext(ctx, `UPDATE idempotency_records SET replayed_count=replayed_count+1,last_observed_at=? WHERE principal_ref=? AND tool=? AND operation_kind=? AND idempotency_key=?`, r.Authority.now().Format(time.RFC3339Nano), grant.PrincipalRef, r.Tool, operationKind, key); err != nil {
		return Envelope{}, false, err
	}
	ref := operationRefFromFence(step, "pending", "git_proof")
	return NewPending(base, ref, RecoveryAction{Kind: "reconcile_operation", RequiredRefs: []string{"operation_id"}}), true, nil
}

func (r runtime) replayWorkflowAction(base Envelope, step store.FenceResult, legacyChanged string) (Envelope, error) {
	state := OperationState(step.ResultKind)
	if step.ResultKind == store.ResultFailedStale {
		// failed_stale is a durable store classification, not a TS7 state.
		state = OperationFailed
	}
	ref := OperationRef{ID: step.OpID, Kind: "workflow_action", Version: strconv.FormatInt(step.AttemptEpoch, 10), State: state, CurrentStep: step.StepID, UpdatedAt: time.Now().UTC()}
	switch step.ResultKind {
	case store.ResultCompleted:
		payload := json.RawMessage(step.ResultPayload)
		if step.ContractVersion != ManifestVersion && step.ContractVersion != "1.0.0" {
			return coreError(base, "malformed_response", "durable workflow result uses an unsupported contract version", "contact_operator", false), nil
		}
		if err := ValidateOperationPayload(base.Tool, base.Operation, payload, true); err != nil {
			if step.ContractVersion != "1.0.0" {
				return coreError(base, "malformed_response", fmt.Sprintf("durable workflow result is not a valid current result: %v", err), "contact_operator", false), nil
			}
			migrated, migrateErr := migrateLegacyWorkflowResult(payload, legacyChanged)
			if migrateErr != nil {
				return coreError(base, "malformed_response", migrateErr.Error(), "contact_operator", false), nil
			}
			payload = migrated
		}
		return r.mutationResult(base, payload, decodeWorkflowChangedRefs(step.ChangedRefs), nil), nil
	case store.ResultPending:
		return NewPending(base, ref, RecoveryAction{Kind: "reconcile_operation", RequiredRefs: []string{"operation_id"}}), nil
	case store.ResultPartial:
		return NewPartial(base, ref, []string{step.StepID}, TypedError{Kind: "operation_conflict", RetrySafe: true, RecoveryAction: RecoveryAction{Kind: "reconcile_operation"}, EffectState: EffectPartial}), nil
	case store.ResultFailed, store.ResultFailedStale:
		return NewPartial(base, ref, []string{step.StepID}, TypedError{Kind: "operation_conflict", RetrySafe: false, RecoveryAction: RecoveryAction{Kind: "reconcile_operation"}, EffectState: EffectPartial}), nil
	default:
		return coreError(base, "malformed_response", "durable workflow result classification is unsupported", "contact_operator", false), nil
	}
}

func migrateLegacyWorkflowResult(payload json.RawMessage, changed string) (json.RawMessage, error) {
	var legacy struct {
		ChangedRefs      []string     `json:"changed_refs"`
		NextValidIntents []NextIntent `json:"next_valid_intents"`
		OperationID      string       `json:"operation_id,omitempty"`
	}
	if err := json.Unmarshal(payload, &legacy); err != nil || len(legacy.ChangedRefs) == 0 {
		return nil, newRuntimeFailure("invariant_violation", "legacy durable workflow result cannot be migrated", "reread_entities", false)
	}
	var refs []ChangedRef
	if err := json.Unmarshal([]byte(changed), &refs); err != nil {
		return nil, newRuntimeFailure("invariant_violation", "legacy durable workflow result is missing typed changed references", "reread_entities", false)
	}
	if len(refs) != len(legacy.ChangedRefs) {
		return nil, newRuntimeFailure("invariant_violation", "legacy durable workflow result is missing typed changed references", "reread_entities", false)
	}
	currentRefs := make([]map[string]any, 0, len(refs))
	for _, ref := range refs {
		version, err := strconv.ParseInt(ref.Version, 10, 64)
		if err != nil || version < 1 {
			return nil, newRuntimeFailure("invariant_violation", "legacy durable workflow result has an invalid changed-reference version", "reread_entities", false)
		}
		currentRefs = append(currentRefs, map[string]any{"entity_kind": ref.EntityKind, "id": ref.ID, "version": version})
	}
	migrated, err := json.Marshal(map[string]any{"changed_refs": currentRefs, "next_valid_intents": mutationResultIntents(legacy.NextValidIntents), "operation_id": legacy.OperationID})
	if err != nil {
		return nil, newRuntimeFailure("invariant_violation", "legacy durable workflow result cannot be migrated", "reread_entities", false)
	}
	if err := ValidateOperationPayload("concord_work_transition", "workflow_action", migrated, true); err != nil {
		return nil, newRuntimeFailure("invariant_violation", fmt.Sprintf("migrated durable workflow result is invalid: %v", err), "reread_entities", false)
	}
	return migrated, nil
}

func decodeWorkflowChangedRefs(values []string) []ChangedRef {
	type durableRef struct {
		EntityKind string `json:"entity_kind"`
		ID         string `json:"id"`
		Version    int64  `json:"version"`
	}
	out := make([]ChangedRef, 0, len(values))
	for _, value := range values {
		var ref durableRef
		if json.Unmarshal([]byte(value), &ref) == nil && ref.EntityKind != "" && ref.ID != "" && ref.Version > 0 {
			out = append(out, ChangedRef{EntityKind: ref.EntityKind, ID: ref.ID, Version: strconv.FormatInt(ref.Version, 10)})
		}
	}
	return out
}

func scopeFromMap(scope map[string]any) *Scope {
	if scope == nil {
		return nil
	}
	result := &Scope{}
	if value, ok := scope["product_id"].(string); ok {
		result.ProductID = value
	}
	if value, ok := scope["product_ids"].([]any); ok {
		for _, item := range value {
			if text, ok := item.(string); ok {
				result.ProductIDs = append(result.ProductIDs, text)
			}
		}
	}
	if value, ok := scope["product_ids"].([]string); ok {
		result.ProductIDs = append(result.ProductIDs, value...)
	}
	if value, ok := scope["project_ids"].([]any); ok {
		for _, item := range value {
			if text, ok := item.(string); ok {
				result.ProjectIDs = append(result.ProjectIDs, text)
			}
		}
	}
	if value, ok := scope["project_ids"].([]string); ok {
		result.ProjectIDs = append(result.ProjectIDs, value...)
	}
	if value, ok := scope["work_ids"].([]any); ok {
		for _, item := range value {
			if text, ok := item.(string); ok {
				result.WorkIDs = append(result.WorkIDs, text)
			}
		}
	}
	if value, ok := scope["work_ids"].([]string); ok {
		result.WorkIDs = append(result.WorkIDs, value...)
	}
	if value, ok := scope["scope_version"].(string); ok {
		result.ScopeVersion = value
	}
	return result
}

func (r runtime) preflightWorkflowAction(ctx context.Context, raw []byte, grant Grant) error {
	var in actionMutationInput
	if err := decodeStrict(raw, &in); err != nil {
		return err
	}
	payload, err := workflowActionFields(in.Fields)
	if err != nil {
		return err
	}
	return store.AuthorizeWorkflowAction(ctx, r.Store, nil, store.WorkflowActionPreflightRequest{
		WorkID:                in.WorkID,
		ExpectedVersion:       in.ExpectedVersion,
		ActionID:              in.ActionID,
		SelectedChoice:        in.SelectedChoice,
		DecisionContextDigest: in.DecisionContextDigest,
		Payload:               payload,
		Actor: store.WorkflowActor{
			PrincipalRef: grant.PrincipalRef,
			ClientRef:    grant.ClientRef,
			AgentRef:     grant.AgentRef,
			SessionRef:   grant.SessionRef,
			ActorClass:   store.ActorAgent,
		},
	}, nil)
}

func (r runtime) authorizeWorkflowAction(ctx context.Context, raw []byte, grant Grant, authorize func() error) error {
	var in actionMutationInput
	if err := decodeStrict(raw, &in); err != nil {
		return err
	}
	payload, err := workflowActionFields(in.Fields)
	if err != nil {
		return err
	}
	return store.AuthorizeWorkflowAction(ctx, r.Store, nil, store.WorkflowActionPreflightRequest{
		WorkID:                in.WorkID,
		ExpectedVersion:       in.ExpectedVersion,
		ActionID:              in.ActionID,
		SelectedChoice:        in.SelectedChoice,
		DecisionContextDigest: in.DecisionContextDigest,
		Payload:               payload,
		Actor: store.WorkflowActor{
			PrincipalRef: grant.PrincipalRef,
			ClientRef:    grant.ClientRef,
			AgentRef:     grant.AgentRef,
			SessionRef:   grant.SessionRef,
			ActorClass:   store.ActorAgent,
		},
	}, authorize)
}

func preflightWorkflowActionRequest(ctx context.Context, s *store.Store, raw []byte, env CallEnvelope) error {
	return preflightWorkflowActionRequestWithRegistry(ctx, s, raw, env, store.BuiltinWorkflowRegistry())
}

func preflightWorkflowActionRequestWithRegistry(ctx context.Context, s *store.Store, raw []byte, env CallEnvelope, registry store.DefinitionRegistry) error {
	var in actionMutationInput
	if err := decodeStrict(raw, &in); err != nil {
		return newRuntimeFailure("invalid_input", err.Error(), "reread_entities", false)
	}
	payload, err := workflowActionFields(in.Fields)
	if err != nil {
		return newRuntimeFailure("invalid_input", err.Error(), "reread_entities", false)
	}
	// An exact durable replay is allowed to bypass the now-advanced expected
	// version. The digest comparison remains strict; no authorization callback
	// or mutation is reached until the normal replay path validates the grant.
	var priorDigest string
	key := in.IdempotencyKey
	if err := s.DB().QueryRowContext(ctx, `SELECT canonical_digest FROM idempotency_records WHERE principal_ref=? AND tool=? AND operation_kind='workflow_action' AND idempotency_key=?`, env.PrincipalRef, "concord_work_transition", key).Scan(&priorDigest); err == nil {
		if priorDigest != mutationDigest("concord_work_transition", "workflow_action", env, raw) {
			return store.IdempotencyConflict("workflow_action", key)
		}
		return nil
	} else if err != sql.ErrNoRows {
		return err
	}
	if err := store.ValidateWorkflowOperatorSelection(ctx, s, in.WorkID, in.ExpectedVersion, in.ActionID, in.SelectedChoice, in.DecisionContextDigest); err != nil {
		return err
	}
	return store.WorkflowActionPreflightWithRegistry(ctx, s, registry, store.WorkflowActionPreflightRequest{
		WorkID:                in.WorkID,
		ExpectedVersion:       in.ExpectedVersion,
		ActionID:              in.ActionID,
		SelectedChoice:        in.SelectedChoice,
		DecisionContextDigest: in.DecisionContextDigest,
		Payload:               payload,
		Actor:                 store.WorkflowActor{PrincipalRef: env.PrincipalRef, ClientRef: env.ClientRef, AgentRef: env.AgentRef, SessionRef: env.SessionRef, ActorClass: store.ActorAgent},
	})
}

func workflowActionFields(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`{}`), nil
	}
	if err := validateUniqueJSON(raw); err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if err := decodeStrict(raw, &object); err == nil && object != nil {
		return raw, nil
	}
	var fields []struct {
		Name  string          `json:"name"`
		Value json.RawMessage `json:"value"`
	}
	if err := decodeStrict(raw, &fields); err != nil {
		return nil, fmt.Errorf("workflow action fields must be a strict object or field list: %w", err)
	}
	object = make(map[string]json.RawMessage, len(fields))
	for _, field := range fields {
		if field.Name == "" {
			return nil, errors.New("workflow action field name is required")
		}
		if _, exists := object[field.Name]; exists {
			return nil, fmt.Errorf("workflow action field %q is duplicated", field.Name)
		}
		if field.Name == "payload" {
			var encoded string
			var payloadObject map[string]json.RawMessage
			if json.Unmarshal(field.Value, &encoded) == nil && json.Unmarshal([]byte(encoded), &payloadObject) == nil {
				field.Value, _ = json.Marshal(payloadObject)
			}
		}
		object[field.Name] = field.Value
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func (r runtime) mutateWorkflowAction(ctx context.Context, base Envelope, raw []byte, grant Grant) (Envelope, error) {
	if r.Store == nil {
		return coreError(base, "invalid_input", "workflow action requires a registered workflow authority", "contact_operator", false), nil
	}
	var in actionMutationInput
	if err := decodeStrict(raw, &in); err != nil {
		return coreError(base, "invalid_input", err.Error(), "reread_entities", false), nil
	}
	payload, err := workflowActionFields(in.Fields)
	if err != nil {
		return coreError(base, "invalid_input", err.Error(), "reread_entities", false), nil
	}
	if in.ActionID == "replay" {
		return r.mutateWorkflowReplay(ctx, base, in, payload)
	}
	registry := r.Registry
	if registry == nil {
		registry = store.BuiltinWorkflowRegistry()
	}
	_, action, err := store.WorkflowActionDefinitionFor(ctx, r.Store, registry, in.WorkID, in.ActionID)
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	digest := mutationDigest(r.Tool, r.Operation, r.Envelope, raw)
	operationID := "workflow-" + digest[7:31]
	scope := map[string]any{"product_id": r.Envelope.SelectedProductID, "project_ids": []string{r.Envelope.AmbientProjectID}, "work_ids": []string{in.WorkID}, "scope_version": r.Envelope.ScopeVersion}
	versions := map[string]any{"work": in.ExpectedVersion}
	approvalConsequence := "workflow_action"
	contractVersion := int64(0)
	if in.ActionID == "confirm_premise" {
		if err := r.Store.DB().QueryRowContext(ctx, `SELECT COALESCE((SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL ORDER BY contract_version DESC LIMIT 1),0)`, in.WorkID).Scan(&contractVersion); err != nil {
			return failureEnvelope(base, err), nil
		}
		if contractVersion == 0 {
			return coreError(base, "invalid_input", "confirm_premise requires an approved workflow contract", "reread_entities", false), nil
		}
		versions["contract"] = contractVersion
	}
	approval := ""
	if in.Approval != nil {
		approval = in.Approval.ApprovalRef
	}
	requiresApproval := action.Approval == store.ActionApprovalRequired
	if replay, handled, replayErr := r.replayMutationBeforeScope(ctx, base, raw, grant, ContractOperation{ID: "concord_work_transition.workflow_action"}); replayErr != nil || handled {
		if replayErr != nil {
			return failureEnvelope(base, replayErr), nil
		}
		return replay, nil
	}
	if err := store.ValidateWorkflowOperatorSelection(ctx, r.Store, in.WorkID, in.ExpectedVersion, in.ActionID, in.SelectedChoice, in.DecisionContextDigest); err != nil {
		return failureEnvelope(base, err), nil
	}

	inv := Invocation{GrantToken: r.Envelope.GrantRef, ClientRef: r.Envelope.ClientRef, ClientVersion: r.Envelope.ClientVersion, PrincipalRef: r.Envelope.PrincipalRef, SessionRef: r.Envelope.SessionRef, AgentRef: r.Envelope.AgentRef, Directory: r.Envelope.Directory, Worktree: r.Envelope.Worktree, SurfaceVersion: r.Envelope.SurfaceVersion, EnvelopeVersion: r.Envelope.EnvelopeVersion, ManifestDigest: r.Envelope.ManifestDigest, HostAssertionDigest: r.Envelope.HostAssertionDigest, RequiredCapability: Capability("work_transition"), ProductID: r.Envelope.SelectedProductID, ProjectID: r.Envelope.AmbientProjectID}
	if inv.HostAssertionDigest == "" {
		inv.HostAssertionDigest = digest
	}
	if requiresApproval && approval == "" {
		tx, txErr := r.Store.DB().BeginTx(ctx, nil)
		if txErr != nil {
			return failureEnvelope(base, txErr), nil
		}
		challengeRef, challengeErr := r.Authority.CreateApprovalChallengeTx(ctx, tx, inv, ApprovalChallengeSpec{OperationDigest: digest, Scope: boundedApprovalScope(scope), Versions: versions, Consequence: approvalConsequence, HostAssertionDigest: inv.HostAssertionDigest, ExpiresAt: r.Authority.now().Add(10 * time.Minute)})
		if challengeErr != nil {
			_ = tx.Rollback()
			return failureEnvelope(base, challengeErr), nil
		}
		if err := tx.Commit(); err != nil {
			return failureEnvelope(base, err), nil
		}
		response := coreError(base, "approval_required", "core approval is required for this workflow action", "request_approval", false)
		premiseSummary := ""
		if in.ActionID == "confirm_premise" {
			_ = r.Store.DB().QueryRowContext(ctx, `SELECT premise FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL ORDER BY contract_version DESC LIMIT 1`, in.WorkID).Scan(&premiseSummary)
		}
		if len([]rune(premiseSummary)) > 256 {
			premiseSummary = string([]rune(premiseSummary)[:256])
		}
		if premiseSummary == "" {
			premiseSummary = "Workflow action " + in.ActionID
		}
		response.Error.Details = map[string]any{"approval_ref": challengeRef, "summary": "Approve the exact workflow action, scope, and expected version.", "operation_digest": digest, "scope": approvalScopeBindings(scope), "versions": approvalVersionBindings(versions), "work_id": in.WorkID, "action_id": in.ActionID, "contract_version": strconv.FormatInt(contractVersion, 10), "selected_choice": in.SelectedChoice, "premise_summary": premiseSummary, "decision_context_digest": in.DecisionContextDigest}
		return response, nil
	}

	var execution store.WorkflowActionExecutionResult
	var operatorActor *store.WorkflowActor
	var result Envelope
	var resultRejected bool
	scopeJSON, _ := json.Marshal(scope)
	actionRequest := store.WorkflowActionExecutionRequest{WorkID: in.WorkID, ExpectedVersion: in.ExpectedVersion, ActionID: in.ActionID, SelectedChoice: in.SelectedChoice, DecisionContextDigest: in.DecisionContextDigest, Payload: payload, EvidenceRefs: evidenceLocators(in.Evidence), Actor: store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}, AcceptedInputsDigest: digest, IdempotencyIdentity: in.IdempotencyKey, OperationID: operationID, PrincipalRef: grant.PrincipalRef, Tool: r.Tool, IdempotencyKey: in.IdempotencyKey, RequestID: r.Envelope.RequestID, AcceptedScope: string(scopeJSON), ContractVersion: grant.SurfaceVersion, Now: r.Authority.now()}
	err = store.AuthorizeWorkflowActionAtBoundaryTx(ctx, r.Store, registry, store.WorkflowActionPreflightRequest{WorkID: in.WorkID, ExpectedVersion: in.ExpectedVersion, ActionID: in.ActionID, SelectedChoice: in.SelectedChoice, DecisionContextDigest: in.DecisionContextDigest, Payload: payload, Actor: actionRequest.Actor}, nil, time.Time{}, func(tx *sql.Tx) error {
		if _, err := r.Authority.ValidateAndConsumeGrantTx(ctx, tx, inv); err != nil {
			return err
		}
		if requiresApproval {
			verifiedOperator, err := r.consumeApprovalTx(ctx, tx, inv, grant, ApprovalCheck{ApprovalRef: approval, OperationDigest: digest, Scope: boundedApprovalScope(scope), Versions: versions, Consequence: approvalConsequence, ClientRef: grant.ClientRef, SessionRef: grant.SessionRef, RequireOperatorIdentity: in.ActionID == "confirm_premise"})
			if err != nil {
				return err
			}
			if in.ActionID == "confirm_premise" {
				operatorActor = &verifiedOperator
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO idempotency_records(principal_ref,tool,operation_kind,idempotency_key,canonical_digest,op_id,result_event_ids,result_payload,changed_refs,authorized_scope_snapshot,first_observed_at,last_observed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, grant.PrincipalRef, r.Tool, "workflow_action", in.IdempotencyKey, digest, operationID, "[]", "{}", "[]", string(scopeJSON), r.Authority.now().Format(time.RFC3339Nano), r.Authority.now().Format(time.RFC3339Nano))
		return err
	}, func(tx *sql.Tx) error {
		actionRequest.OperatorActor = operatorActor
		var err error
		execution, err = store.ApplyWorkflowActionTx(ctx, tx, registry, actionRequest)
		if err != nil {
			return err
		}
		changedVersion := execution.ResultingVersion
		if changedVersion == 0 {
			changedVersion = in.ExpectedVersion + 1
		}
		changed := []ChangedRef{{EntityKind: "work_item", ID: in.WorkID, Version: strconv.FormatInt(changedVersion, 10)}}
		base.ResolvedScope = scopeFromMap(scope)
		result = r.mutationResult(base, execution.Result, changed, nil)
		if result.Outcome == OutcomeError {
			resultRejected = true
			return errors.New("mutation result rejected")
		}
		changedJSON, _ := json.Marshal(changed)
		_, err = tx.ExecContext(ctx, `UPDATE idempotency_records SET result_event_ids=?,result_payload=?,changed_refs=?,last_observed_at=? WHERE principal_ref=? AND tool=? AND operation_kind='workflow_action' AND idempotency_key=?`, marshalEventIDs(execution.EventIDs), string(execution.Result), string(changedJSON), r.Authority.now().Format(time.RFC3339Nano), grant.PrincipalRef, r.Tool, in.IdempotencyKey)
		return err
	})
	if err != nil {
		if resultRejected {
			return result, nil
		}
		return failureEnvelope(base, err), nil
	}
	return result, nil
}

func (r runtime) mutateWorkflowReplay(ctx context.Context, base Envelope, in actionMutationInput, payload []byte) (Envelope, error) {
	events, err := decodeWorkflowReplayEvents(payload, in.WorkID)
	if err != nil {
		return coreError(base, "invalid_input", err.Error(), "reread_entities", false), nil
	}
	base.Authority = AuthorityAuthoritative
	result := r.mutationResult(base, mutationPayload(nil, nil), nil, nil)
	if result.Outcome == OutcomeError {
		return result, nil
	}
	if _, err := store.ImportWorkflowEventStream(ctx, r.Store, events); err != nil {
		return failureEnvelope(base, err), nil
	}
	if _, err := store.ReadWorkflowReplayEvidence(ctx, r.Store, in.WorkID, "work.created"); err != nil {
		return failureEnvelope(base, err), nil
	}
	return result, nil
}

type workflowReplayEventInput struct {
	EventID        string          `json:"event_id"`
	Kind           string          `json:"kind"`
	WorkID         string          `json:"work_id"`
	ActorRef       string          `json:"actor_ref"`
	OccurredAt     string          `json:"occurred_at"`
	PayloadVersion int             `json:"payload_version"`
	Payload        json.RawMessage `json:"payload"`
}

func decodeWorkflowReplayEvents(raw []byte, workID string) ([]store.Event, error) {
	var fields map[string]json.RawMessage
	if err := decodeStrict(raw, &fields); err != nil {
		return nil, fmt.Errorf("workflow replay fields must be a strict object: %w", err)
	}
	stream, ok := fields["event_stream"]
	if !ok {
		return nil, errors.New("workflow replay requires the structured event_stream")
	}
	var entries []workflowReplayEventInput
	if err := decodeStrict(stream, &entries); err != nil {
		return nil, fmt.Errorf("workflow replay event_stream is invalid: %w", err)
	}
	if len(entries) == 0 {
		return nil, errors.New("workflow replay event_stream is empty")
	}
	events := make([]store.Event, 0, len(entries))
	for _, entry := range entries {
		if entry.WorkID != workID || entry.EventID == "" || entry.Kind == "" || entry.ActorRef == "" || entry.PayloadVersion < 1 || len(entry.Payload) == 0 {
			return nil, errors.New("workflow replay event_stream entry has incomplete typed identity")
		}
		occurredAt, err := time.Parse(time.RFC3339Nano, entry.OccurredAt)
		if err != nil {
			return nil, errors.New("workflow replay event_stream entry has an invalid occurred_at timestamp")
		}
		var payloadObject map[string]any
		if err := json.Unmarshal(entry.Payload, &payloadObject); err != nil || payloadObject == nil {
			return nil, errors.New("workflow replay event_stream payload must be an object")
		}
		events = append(events, store.Event{EventID: entry.EventID, Kind: entry.Kind, SubjectType: store.SubjectWorkItem, SubjectID: entry.WorkID, Actor: entry.ActorRef, OccurredAt: occurredAt.UTC(), PayloadVersion: entry.PayloadVersion, Payload: append(json.RawMessage(nil), entry.Payload...)})
	}
	return events, nil
}

func (r runtime) mutate(ctx context.Context, base Envelope, raw []byte, grant Grant, op ContractOperation) (Envelope, error) {
	if op.ID == "concord_work_transition.workflow_action" {
		return r.mutateWorkflowAction(ctx, base, raw, grant)
	}
	digest := mutationDigest(r.Tool, r.Operation, r.Envelope, raw)
	if r.Tool == "concord_work_compact" {
		return r.mutateCompaction(ctx, base, raw, digest, op)
	}
	scope := map[string]any{"product_id": r.Envelope.SelectedProductID, "project_ids": []string{r.Envelope.AmbientProjectID}, "scope_version": r.Envelope.ScopeVersion}
	versions := map[string]any{}
	consequence := mutationConsequence(op.ID)
	approval := ""
	requiresApproval := op.Approval == ApprovalClass("required")
	var effect mutationEffect
	var intents []NextIntent

	switch op.ID {
	case "concord_work_define.capture":
		var in captureMutationInput
		if err := decodeStrict(raw, &in); err != nil {
			return base, err
		}
		if len(in.ProjectIDs) == 0 {
			return coreError(base, "invalid_input", "capture requires at least one Project membership", "reread_entities", false), nil
		}
		workID := "work-" + digest[7:31]
		var registeredDefinition store.RegisteredDefinition
		if in.WorkflowTypeRef != "" {
			var definitionErr error
			registeredDefinition, definitionErr = store.BuiltinWorkflowDefinitionForRef(in.WorkflowTypeRef)
			if definitionErr != nil {
				return failureEnvelope(base, definitionErr), nil
			}
		}
		if in.Approval != nil {
			approval = in.Approval.ApprovalRef
		}
		productsByProject, scopeErr := r.Store.ProductsForProjectIDs(ctx, in.ProjectIDs)
		if scopeErr != nil {
			return failureEnvelope(base, scopeErr), nil
		}
		for _, products := range productsByProject {
			for _, product := range products {
				if r.Envelope.SelectedProductID != "" && product != r.Envelope.SelectedProductID {
					requiresApproval = true
				}
			}
		}
		scope["project_ids"] = in.ProjectIDs
		intents = []NextIntent{{Tool: "concord_work_browse", Operation: "list", QueryID: "PM1.Q3", ReasonCode: "inspect_captured_work"}}
		effect = func(ctx context.Context, tx *sql.Tx, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
			priority := in.Priority
			payload, _ := json.Marshal(map[string]any{"work_kind": in.Kind, "title": in.Title, "value_statement": in.ValueStatement, "priority": priority, "tags": in.Tags, "component_id": in.ComponentID, "workflow_type_ref": in.WorkflowTypeRef, "external_ref": in.ExternalRef})
			memberships := make([]storeMembership, len(in.ProjectIDs))
			for i, project := range in.ProjectIDs {
				role := "secondary"
				if i == 0 {
					role = "primary"
				}
				memberships[i] = storeMembership{ProjectID: project, Role: role}
			}
			membershipPayload, _ := json.Marshal(map[string]any{"memberships": memberships, "expected_version": 1, "resulting_version": 2})
			now := r.Authority.now()
			result, err := store.ApplyOperationTx(ctx, tx, store.Operation{Events: []store.Event{
				{EventID: digest + ":create", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: grant.PrincipalRef, OccurredAt: now, PayloadVersion: 2, Payload: payload},
				{EventID: digest + ":memberships", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: grant.PrincipalRef, OccurredAt: now, PayloadVersion: 1, Payload: membershipPayload},
			}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, workID): 0}})
			if err != nil {
				return nil, nil, nil, err
			}
			if in.WorkflowTypeRef != "" {
				actor := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
				if err := store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: workID, Definition: registeredDefinition, Actor: actor, Now: now}); err != nil {
					return nil, nil, nil, err
				}
			}
			changedVersion := int64(2)
			if in.WorkflowTypeRef != "" {
				changedVersion = 4
			}
			changed := []ChangedRef{{EntityKind: "work_item", ID: workID, Version: strconv.FormatInt(changedVersion, 10)}}
			return mutationPayload(changed, intents), result.EventIDs, changed, nil
		}
	case "concord_work_define.revise_intent":
		var in reviseMutationInput
		if err := decodeStrict(raw, &in); err != nil {
			return base, err
		}
		versions["work"] = in.ExpectedVersion
		scope["work_ids"] = []string{in.WorkID}
		var registeredDefinition store.RegisteredDefinition
		if in.WorkflowTypeRef != "" {
			var definitionErr error
			registeredDefinition, definitionErr = store.BuiltinWorkflowDefinitionForRef(in.WorkflowTypeRef)
			if definitionErr != nil {
				return failureEnvelope(base, definitionErr), nil
			}
		}
		intents = []NextIntent{{Tool: "concord_work_transition", Operation: "lifecycle", ReasonCode: "continue_work", RequiredFields: []string{"work_id", "expected_version"}}}
		effect = func(ctx context.Context, tx *sql.Tx, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
			var existingDefinitionRef string
			existingErr := tx.QueryRowContext(ctx, `SELECT definition_ref FROM workflow_instances WHERE work_id=?`, in.WorkID).Scan(&existingDefinitionRef)
			if existingErr != nil && existingErr != sql.ErrNoRows {
				return nil, nil, nil, existingErr
			}
			if existingErr == nil && in.WorkflowTypeRef != "" && existingDefinitionRef != in.WorkflowTypeRef {
				return nil, nil, nil, fmt.Errorf("workflow definition cannot be changed after initialization")
			}
			payload, _ := json.Marshal(map[string]any{"title": in.Title, "value_statement": in.ValueStatement, "kind": in.Kind, "priority": in.Priority, "tags": in.Tags, "component_id": in.ComponentID, "workflow_type_ref": in.WorkflowTypeRef, "reason": in.Reason, "expected_version": in.ExpectedVersion, "resulting_version": in.ExpectedVersion + 1})
			result, err := store.ApplyOperationTx(ctx, tx, store.Operation{Events: []store.Event{{EventID: digest + ":revise", Kind: "work.intent_revised", SubjectType: store.SubjectWorkItem, SubjectID: in.WorkID, Actor: grant.PrincipalRef, OccurredAt: r.Authority.now(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, in.WorkID): in.ExpectedVersion}})
			if err != nil {
				return nil, nil, nil, err
			}
			if in.WorkflowTypeRef != "" && existingErr == sql.ErrNoRows {
				actor := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
				if err := store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: in.WorkID, Definition: registeredDefinition, Actor: actor, Now: r.Authority.now()}); err != nil {
					return nil, nil, nil, err
				}
			}
			changedVersion := in.ExpectedVersion + 1
			if in.WorkflowTypeRef != "" && existingErr == sql.ErrNoRows {
				changedVersion += 2
			}
			changed := []ChangedRef{{EntityKind: "work_item", ID: in.WorkID, Version: strconv.FormatInt(changedVersion, 10)}}
			return mutationPayload(changed, intents), result.EventIDs, changed, nil
		}
	case "concord_work_transition.lifecycle":
		var in lifecycleMutationInput
		if err := decodeStrict(raw, &in); err != nil {
			return base, err
		}
		if in.Target == "superseded" {
			return coreError(base, "invalid_input", "superseded is only available through relate.supersede", "use_relation_operation", false), nil
		}
		if (in.Target == "completed" || in.Target == "cancelled") && (in.Approval == nil || len(in.Evidence) == 0) {
			response := coreError(base, "approval_required", "terminal lifecycle transition requires approval and evidence", "request_approval", false)
			response.Error.Details = map[string]any{"operation_digest": digest, "work_id": in.WorkID, "expected_version": in.ExpectedVersion}
			return response, nil
		}
		if in.Approval != nil {
			approval = in.Approval.ApprovalRef
		}
		requiresApproval = in.Target == "completed" || in.Target == "cancelled"
		versions["work"] = in.ExpectedVersion
		scope["work_ids"] = []string{in.WorkID}
		intents = []NextIntent{{Tool: "concord_work_browse", Operation: "scope", QueryID: "PM1.Q6", ReasonCode: "refresh_work_version", RequiredFields: []string{"work_id"}}}
		effect = func(ctx context.Context, tx *sql.Tx, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
			payload, _ := json.Marshal(map[string]any{"from": "", "to": in.Target, "reason": in.Reason, "evidence_refs": evidenceLocators(in.Evidence), "expected_version": in.ExpectedVersion, "resulting_version": in.ExpectedVersion + 1})
			from, err := currentLifecycle(ctx, tx, in.WorkID)
			if err != nil {
				return nil, nil, nil, err
			}
			var eventPayload map[string]any
			_ = json.Unmarshal(payload, &eventPayload)
			eventPayload["from"] = from
			payload, _ = json.Marshal(eventPayload)
			result, err := store.ApplyOperationTx(ctx, tx, store.Operation{Events: []store.Event{{EventID: digest + ":transition", Kind: "work.transitioned", SubjectType: store.SubjectWorkItem, SubjectID: in.WorkID, Actor: grant.PrincipalRef, OccurredAt: r.Authority.now(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, in.WorkID): in.ExpectedVersion}})
			if err != nil {
				return nil, nil, nil, err
			}
			changed := []ChangedRef{{EntityKind: "work_item", ID: in.WorkID, Version: strconv.FormatInt(in.ExpectedVersion+1, 10)}}
			return mutationPayload(changed, intents), result.EventIDs, changed, nil
		}
	case "concord_work_relate.set_memberships":
		var in membershipsMutationInput
		if err := decodeStrict(raw, &in); err != nil {
			return base, err
		}
		if len(in.Memberships) == 0 {
			return coreError(base, "invalid_input", "membership replacement cannot be empty", "supply_memberships", false), nil
		}
		if in.Approval != nil {
			approval = in.Approval.ApprovalRef
		}
		requiresApproval = true
		versions["work"] = in.ExpectedVersion
		scope["work_ids"] = []string{in.WorkID}
		scope["project_ids"] = membershipIDs(in.Memberships)
		intents = []NextIntent{{Tool: "concord_work_browse", Operation: "scope", QueryID: "PM1.Q6", ReasonCode: "refresh_membership_scope", RequiredFields: []string{"work_id"}}}
		effect = func(ctx context.Context, tx *sql.Tx, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
			payload, _ := json.Marshal(map[string]any{"memberships": in.Memberships, "expected_version": in.ExpectedVersion, "resulting_version": in.ExpectedVersion + 1})
			result, err := store.ApplyOperationTx(ctx, tx, store.Operation{Events: []store.Event{{EventID: digest + ":memberships", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: in.WorkID, Actor: grant.PrincipalRef, OccurredAt: r.Authority.now(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, in.WorkID): in.ExpectedVersion}})
			if err != nil {
				return nil, nil, nil, err
			}
			changed := []ChangedRef{{EntityKind: "work_item", ID: in.WorkID, Version: strconv.FormatInt(in.ExpectedVersion+1, 10)}}
			return mutationPayload(changed, intents), result.EventIDs, changed, nil
		}
	case "concord_work_relate.link":
		var in linkMutationInput
		if err := decodeStrict(raw, &in); err != nil {
			return base, err
		}
		if in.Kind == "supersedes" {
			return coreError(base, "invalid_relation", "supersession requires relate.supersede", "use_supersede", false), nil
		}
		if in.Approval != nil {
			approval = in.Approval.ApprovalRef
		}
		versions["from"] = in.FromExpectedVersion
		versions["to"] = in.ToExpectedVersion
		scope["work_ids"] = []string{in.FromWorkID, in.ToWorkID}
		intents = []NextIntent{{Tool: "concord_work_relate", Operation: "unlink", ReasonCode: "remove_relation"}}
		effect = func(ctx context.Context, tx *sql.Tx, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
			payload, _ := json.Marshal(map[string]any{"from": in.FromWorkID, "to": in.ToWorkID, "kind": in.Kind, "reason": in.Reason, "expected_version": in.FromExpectedVersion, "resulting_version": in.FromExpectedVersion + 1, "to_expected_version": in.ToExpectedVersion, "to_resulting_version": in.ToExpectedVersion + 1})
			result, err := store.ApplyOperationTx(ctx, tx, store.Operation{Events: []store.Event{{EventID: digest + ":link", Kind: "relation.added", SubjectType: store.SubjectWorkItem, SubjectID: in.FromWorkID, Actor: grant.PrincipalRef, OccurredAt: r.Authority.now(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, in.FromWorkID): in.FromExpectedVersion, store.VersionRef(store.SubjectWorkItem, in.ToWorkID): in.ToExpectedVersion}})
			if err != nil {
				return nil, nil, nil, err
			}
			changed := []ChangedRef{{EntityKind: "work_item", ID: in.FromWorkID, Version: strconv.FormatInt(in.FromExpectedVersion+1, 10)}, {EntityKind: "work_item", ID: in.ToWorkID, Version: strconv.FormatInt(in.ToExpectedVersion+1, 10)}}
			return mutationPayload(changed, intents), result.EventIDs, changed, nil
		}
	case "concord_work_relate.unlink":
		var in unlinkMutationInput
		if err := decodeStrict(raw, &in); err != nil {
			return base, err
		}
		if len(in.ExpectedVersions) == 0 {
			return coreError(base, "invalid_input", "unlink requires endpoint versions", "reread_relations", false), nil
		}
		endpoints, endpointErr := r.Store.RelationEndpoints(ctx, in.RelationID)
		if endpointErr != nil {
			return failureEnvelope(base, endpointErr), nil
		}
		if in.Approval != nil {
			approval = in.Approval.ApprovalRef
		}
		scope["work_ids"] = append([]string(nil), endpoints...)
		for _, endpoint := range in.ExpectedVersions {
			if endpoint.WorkID == endpoints[0] {
				versions["from"] = endpoint.Version
			}
			if endpoint.WorkID == endpoints[1] {
				versions["to"] = endpoint.Version
			}
		}
		intents = []NextIntent{{Tool: "concord_work_trace", Operation: "relations", QueryID: "PM1.Q8", ReasonCode: "inspect_relation_graph"}}
		effect = r.unlinkEffect(digest, in, endpoints, intents)
	case "concord_work_relate.supersede":
		var in supersedeMutationInput
		if err := decodeStrict(raw, &in); err != nil {
			return base, err
		}
		if in.Approval != nil {
			approval = in.Approval.ApprovalRef
		}
		versions["predecessor"] = in.PredecessorExpected
		versions["successor"] = in.SuccessorExpected
		scope["work_ids"] = []string{in.PredecessorID, in.SuccessorID}
		intents = []NextIntent{{Tool: "concord_work_relate", Operation: "restore_superseded", ReasonCode: "restore_or_replace_successor"}}
		effect = func(ctx context.Context, tx *sql.Tx, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
			payload, _ := json.Marshal(map[string]any{"successor": in.SuccessorID, "superseded": in.PredecessorID, "reason": in.Reason, "expected_version": in.PredecessorExpected, "resulting_version": in.PredecessorExpected + 1, "successor_expected_version": in.SuccessorExpected, "successor_resulting_version": in.SuccessorExpected + 1})
			result, err := store.ApplyOperationTx(ctx, tx, store.Operation{Events: []store.Event{{EventID: digest + ":supersede", Kind: "work.superseded", SubjectType: store.SubjectWorkItem, SubjectID: in.PredecessorID, Actor: grant.PrincipalRef, OccurredAt: r.Authority.now(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, in.PredecessorID): in.PredecessorExpected, store.VersionRef(store.SubjectWorkItem, in.SuccessorID): in.SuccessorExpected}})
			if err != nil {
				return nil, nil, nil, err
			}
			changed := []ChangedRef{{EntityKind: "work_item", ID: in.PredecessorID, Version: strconv.FormatInt(in.PredecessorExpected+1, 10)}, {EntityKind: "work_item", ID: in.SuccessorID, Version: strconv.FormatInt(in.SuccessorExpected+1, 10)}}
			return mutationPayload(changed, intents), result.EventIDs, changed, nil
		}
	case "concord_work_relate.restore_superseded":
		var in restoreMutationInput
		if err := decodeStrict(raw, &in); err != nil {
			return base, err
		}
		if in.Approval != nil {
			approval = in.Approval.ApprovalRef
		}
		versions["predecessor"] = in.PredecessorExpected
		versions["successor"] = in.SuccessorExpected
		scope["work_ids"] = []string{in.PredecessorID, in.SuccessorID}
		intents = []NextIntent{{Tool: "concord_work_browse", Operation: "scope", QueryID: "PM1.Q6", ReasonCode: "inspect_restored_work"}}
		effect = func(ctx context.Context, tx *sql.Tx, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
			payload, _ := json.Marshal(map[string]any{"superseded": in.PredecessorID, "replacement_successor": in.ReplacementSuccessorID, "reason": in.Reason, "expected_version": in.PredecessorExpected, "resulting_version": in.PredecessorExpected + 1, "successor_expected_version": in.SuccessorExpected, "successor_resulting_version": in.SuccessorExpected + 1})
			result, err := store.ApplyOperationTx(ctx, tx, store.Operation{Events: []store.Event{{EventID: digest + ":restore", Kind: "work.reopened_from_superseded", SubjectType: store.SubjectWorkItem, SubjectID: in.PredecessorID, Actor: grant.PrincipalRef, OccurredAt: r.Authority.now(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, in.PredecessorID): in.PredecessorExpected, store.VersionRef(store.SubjectWorkItem, in.SuccessorID): in.SuccessorExpected}})
			if err != nil {
				return nil, nil, nil, err
			}
			changed := []ChangedRef{{EntityKind: "work_item", ID: in.PredecessorID, Version: strconv.FormatInt(in.PredecessorExpected+1, 10)}, {EntityKind: "work_item", ID: in.SuccessorID, Version: strconv.FormatInt(in.SuccessorExpected+1, 10)}}
			return mutationPayload(changed, intents), result.EventIDs, changed, nil
		}
	default:
		return coreError(base, "invalid_input", "mutation operation is not implemented", "contact_operator", false), nil
	}
	if effect == nil {
		return coreError(base, "invalid_input", "mutation input is not executable", "contact_operator", false), nil
	}
	preflightProducts, preflightErr := r.deriveMutationProducts(ctx, scope)
	if preflightErr != nil {
		return failureEnvelope(base, preflightErr), nil
	}
	scope["product_ids"] = preflightProducts
	return r.executeMutation(ctx, base, raw, digest, scope, versions, consequence, approval, requiresApproval, intents, effect)
}

func (r runtime) deriveMutationProducts(ctx context.Context, scope map[string]any) ([]string, error) {
	products := map[string]bool{}
	if raw, ok := scope["work_ids"].([]string); ok && len(raw) > 0 {
		byWork, err := r.Store.ProductsForWorkIDs(ctx, raw)
		if err != nil {
			return nil, err
		}
		for _, values := range byWork {
			for _, product := range values {
				products[product] = true
			}
		}
	}
	if raw, ok := scope["project_ids"].([]string); ok && len(raw) > 0 {
		byProject, err := r.Store.ProductsForProjectIDs(ctx, raw)
		if err != nil {
			return nil, err
		}
		for _, values := range byProject {
			for _, product := range values {
				products[product] = true
			}
		}
	}
	result := make([]string, 0, len(products))
	for product := range products {
		result = append(result, product)
	}
	sort.Strings(result)
	return result, nil
}

func deriveMutationProductsTx(ctx context.Context, tx *sql.Tx, scope map[string]any) ([]string, error) {
	products := map[string]bool{}
	queryProducts := func(query string, ids []string) error {
		if len(ids) == 0 {
			return nil
		}
		placeholders := make([]string, len(ids))
		args := make([]any, len(ids))
		for i, id := range ids {
			placeholders[i], args[i] = "?", id
		}
		rows, err := tx.QueryContext(ctx, query+strings.Join(placeholders, ",")+")", args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var product string
			if err := rows.Scan(&product); err != nil {
				return err
			}
			products[product] = true
		}
		return rows.Err()
	}
	if ids, ok := scope["work_ids"].([]string); ok {
		if err := queryProducts(`SELECT DISTINCT pp.product_id FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id IN (`, ids); err != nil {
			return nil, err
		}
	}
	if ids, ok := scope["project_ids"].([]string); ok {
		if err := queryProducts(`SELECT DISTINCT product_id FROM product_projects WHERE project_id IN (`, ids); err != nil {
			return nil, err
		}
	}
	result := make([]string, 0, len(products))
	for product := range products {
		result = append(result, product)
	}
	sort.Strings(result)
	return result, nil
}

func (r runtime) mutateCompaction(ctx context.Context, base Envelope, raw []byte, digest string, op ContractOperation) (Envelope, error) {
	var publish compactPublishInput
	var reconcile compactReconcileInput
	if op.ID == "concord_work_compact.publish" {
		if err := decodeStrict(raw, &publish); err != nil {
			return base, err
		}
		if publish.Approval == nil || publish.Approval.ApprovalRef == "" {
			return coreError(base, "approval_required", "publication requires a core approval reference", "request_approval", false), nil
		}
	}
	if op.ID == "concord_work_compact.reconcile" {
		if err := decodeStrict(raw, &reconcile); err != nil {
			return base, err
		}
	}
	key := idempotencyKey(raw)
	workID := publish.WorkID
	if op.ID == "concord_work_compact.reconcile" {
		workID = reconcile.WorkID
	}
	if workID == "" {
		workID = "unknown-work"
	}
	opID := "mutation-" + digest[7:31]
	if reconcile.OperationID != "" {
		opID = reconcile.OperationID
	}
	scope := map[string]any{"product_id": r.Envelope.SelectedProductID, "project_ids": []string{r.Envelope.AmbientProjectID}, "work_ids": []string{workID}, "scope_version": r.Envelope.ScopeVersion}
	claimScope := map[string]any{"product_id": r.Envelope.SelectedProductID, "project_ids": []string{r.Envelope.AmbientProjectID}, "work_ids": []string{workID}, "scope_version": r.Envelope.ScopeVersion}
	var resolvedHome store.KnowledgeHome
	if op.ID == "concord_work_compact.publish" {
		var homeErr error
		resolvedHome, homeErr = r.Store.ResolveCompactionHome(ctx, workID)
		if homeErr != nil {
			return failureEnvelope(base, homeErr), nil
		}
		if resolvedHome.HomeProjectID != publish.HomeProjectID || resolvedHome.HomeLocatorID != publish.HomeLocatorID {
			return coreError(base, "ambiguous_scope", "caller home does not match the deterministic terminal-work home", "resolve_ambiguity", false), nil
		}
		claimScope["work_version"] = publish.ExpectedVersion
		claimScope["content_digest"] = publish.ContentDigest
		claimScope["home_project_id"] = resolvedHome.HomeProjectID
		claimScope["home_locator_id"] = resolvedHome.HomeLocatorID
		claimScope["head_ref"] = resolvedHome.HeadRef
	}
	acceptedScope, _ := json.Marshal(claimScope)
	grant, err := r.Authority.ValidateInvocation(ctx, Invocation{GrantToken: r.Envelope.GrantRef, ClientRef: r.Envelope.ClientRef, ClientVersion: r.Envelope.ClientVersion, PrincipalRef: r.Envelope.PrincipalRef, SessionRef: r.Envelope.SessionRef, AgentRef: r.Envelope.AgentRef, Directory: r.Envelope.Directory, Worktree: r.Envelope.Worktree, SurfaceVersion: r.Envelope.SurfaceVersion, EnvelopeVersion: r.Envelope.EnvelopeVersion, ManifestDigest: r.Envelope.ManifestDigest, RequiredCapability: "work_compact", ProductID: r.Envelope.SelectedProductID, ProjectID: r.Envelope.AmbientProjectID})
	if err != nil {
		return coreError(base, "unauthorized", err.Error(), "contact_operator", false), nil
	}
	if op.ID == "concord_work_compact.publish" {
		changed := []ChangedRef{{EntityKind: "work_item", ID: workID, Version: strconv.FormatInt(publish.ExpectedVersion+1, 10)}}
		payload := mutationPayload(changed, []NextIntent{{Tool: "concord_knowledge", Operation: "resolve_note", QueryID: "PM1.Q10", ReasonCode: "verify_canonical_note"}})
		base.ResolvedScope = &Scope{ProductID: r.Envelope.SelectedProductID, ProjectIDs: []string{r.Envelope.AmbientProjectID}, WorkIDs: []string{workID}, ScopeVersion: r.Envelope.ScopeVersion}
		result := r.mutationResult(base, payload, changed, []NextIntent{{Tool: "concord_knowledge", Operation: "resolve_note", QueryID: "PM1.Q10", ReasonCode: "verify_canonical_note"}})
		if result.Outcome == OutcomeError {
			return result, nil
		}
		claimReq := store.ClaimRequest{OpID: opID, WorkID: workID, WorkflowTypeRef: "concord.pm6.compaction", WorkflowTypeVersion: 1, StepID: "git_proof", StepKind: store.StepCrossAuthority, AcceptedInputsDigest: digest, AcceptedScopeSnapshot: string(acceptedScope), PrincipalRef: grant.PrincipalRef, Tool: r.Tool, IdempotencyKey: key, RequestID: r.Envelope.RequestID, ObservedAt: r.Authority.now(), ApprovalRef: publish.Approval.ApprovalRef, ContractVersion: ManifestVersion}
		inv := Invocation{GrantToken: r.Envelope.GrantRef, ClientRef: r.Envelope.ClientRef, ClientVersion: r.Envelope.ClientVersion, PrincipalRef: r.Envelope.PrincipalRef, SessionRef: r.Envelope.SessionRef, AgentRef: r.Envelope.AgentRef, Directory: r.Envelope.Directory, Worktree: r.Envelope.Worktree, SurfaceVersion: r.Envelope.SurfaceVersion, EnvelopeVersion: r.Envelope.EnvelopeVersion, ManifestDigest: r.Envelope.ManifestDigest, HostAssertionDigest: r.Envelope.HostAssertionDigest, RequiredCapability: "work_compact", ProductID: r.Envelope.SelectedProductID, ProjectID: r.Envelope.AmbientProjectID}
		claim, claimErr := store.ClaimStepAuthorized(ctx, r.Store, claimReq, func(tx *sql.Tx) error {
			if _, err := r.Authority.ValidateAndConsumeGrantTx(ctx, tx, inv); err != nil {
				return err
			}
			_, err := r.consumeApprovalTx(ctx, tx, inv, grant, ApprovalCheck{ApprovalRef: publish.Approval.ApprovalRef, OperationDigest: digest, Scope: scope, Versions: map[string]any{"work": publish.ExpectedVersion}, Consequence: "publication", ClientRef: grant.ClientRef, SessionRef: grant.SessionRef})
			return err
		})
		if claimErr != nil {
			return failureEnvelope(base, claimErr), nil
		}
		if claim.ResultKind == store.ResultCompleted {
			changed := decodeChangedRefs(claim.ChangedRefs)
			base.Replayed = claim.Replayed
			base.ResolvedScope = &Scope{ProductID: r.Envelope.SelectedProductID, ProjectIDs: []string{r.Envelope.AmbientProjectID}, WorkIDs: []string{workID}, ScopeVersion: r.Envelope.ScopeVersion}
			return r.mutationResult(base, mutationPayload(changed, []NextIntent{{Tool: "concord_knowledge", Operation: "resolve_note", QueryID: "PM1.Q10", ReasonCode: "verify_canonical_note"}}), changed, nil), nil
		}
		note, proofErr := store.PublishCanonicalNote(ctx, resolvedHome, workID, publish.Content, publish.ContentDigest)
		if proofErr != nil {
			return pendingCompaction(base, workID, claim, "git_proof", proofErr), nil
		}
		if linkErr := store.PublishCompactionLink(ctx, r.Store, store.CompactionLinkRequest{EventID: opID + ":link", WorkID: workID, ExpectedVersion: publish.ExpectedVersion, Actor: grant.PrincipalRef, OccurredAt: r.Authority.now(), Home: resolvedHome, CommitOID: note.CommitOID, NotePath: note.NotePath, ExpectedHash: publish.ContentDigest, Reason: "agent compaction publish"}); linkErr != nil {
			return pendingCompaction(base, workID, claim, "sqlite_link", linkErr), nil
		}
		changedJSON, _ := json.Marshal(changed[0])
		complete, completeErr := store.CompleteStep(ctx, r.Store, store.CompleteRequest{OpID: opID, AttemptEpoch: claim.AttemptEpoch, ResultKind: store.ResultCompleted, ResultPayload: string(payload), ChangedRefs: []string{string(changedJSON)}, PrincipalRef: grant.PrincipalRef, Tool: r.Tool, IdempotencyKey: key + ":complete", RequestID: r.Envelope.RequestID, ObservedAt: r.Authority.now(), CompletedAt: timePtr(r.Authority.now())})
		if completeErr != nil {
			return pendingCompaction(base, workID, claim, "sqlite_link", completeErr), nil
		}
		base.Replayed = complete.Replayed
		result.Replayed = complete.Replayed
		return result, nil
	}
	if reconcile.OperationID == "" && reconcile.WorkID != "" {
		if reconcile.ExpectedWorkVersion <= 0 {
			return coreError(base, "invalid_input", "orphan discovery requires expected_work_version", "reread_entities", false), nil
		}
		var currentVersion int64
		if err := r.Store.DB().QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=? AND lifecycle IN ('completed','cancelled','superseded')`, reconcile.WorkID).Scan(&currentVersion); err != nil {
			return failureEnvelope(base, err), nil
		}
		if currentVersion != reconcile.ExpectedWorkVersion {
			return coreError(base, "version_conflict", "terminal work version changed before orphan discovery", "reread_entities", false), nil
		}
		if err := r.Store.DB().QueryRowContext(ctx, `SELECT op_id FROM durable_operations WHERE work_id=? AND (result_kind IS NULL OR result_kind IN ('pending','partial')) ORDER BY attempt_epoch DESC LIMIT 1`, reconcile.WorkID).Scan(&reconcile.OperationID); err != nil {
			return failureEnvelope(base, err), nil
		}
	}
	if reconcile.OperationID == "" {
		return coreError(base, "invalid_input", "reconcile requires an operation reference or terminal work identity", "reread_entities", false), nil
	}
	step, stepErr := store.Step(ctx, r.Store, reconcile.OperationID)
	if stepErr != nil {
		return failureEnvelope(base, stepErr), nil
	}
	if reconcile.ExpectedOperationVersion > 0 && step.AttemptEpoch != reconcile.ExpectedOperationVersion {
		return coreError(base, "version_conflict", "durable operation version changed before reconcile", "reread_entities", false), nil
	}
	if step.WorkID != "" && reconcile.WorkID == "" {
		reconcile.WorkID = step.WorkID
	}
	if step.ResultKind == store.ResultCompleted {
		changed := make([]ChangedRef, 0, len(step.ChangedRefs))
		for _, rawRef := range step.ChangedRefs {
			var ref ChangedRef
			if json.Unmarshal([]byte(rawRef), &ref) == nil {
				changed = append(changed, ref)
			}
		}
		return r.mutationResult(base, mutationPayload(changed, []NextIntent{{Tool: "concord_knowledge", Operation: "resolve_note", QueryID: "PM1.Q10", ReasonCode: "verify_canonical_note"}}), changed, nil), nil
	}
	claimScope = map[string]any{}
	_ = json.Unmarshal([]byte(step.AcceptedScopeSnapshot), &claimScope)
	workVersion := reconcile.ExpectedWorkVersion
	if workVersion == 0 {
		workVersion = integerScopeValue(claimScope, "work_version")
	}
	proofDigest := reconcile.ExpectedProofDigest
	if proofDigest == "" {
		if value, ok := claimScope["content_digest"].(string); ok {
			proofDigest = value
		}
	}
	var home store.KnowledgeHome
	var homeErr error
	recordedProject, _ := claimScope["home_project_id"].(string)
	recordedLocator, _ := claimScope["home_locator_id"].(string)
	recordedHead, _ := claimScope["head_ref"].(string)
	if recordedProject != "" && recordedLocator != "" {
		home, homeErr = r.Store.KnowledgeHomeForLocator(ctx, recordedProject, recordedLocator, recordedHead)
	} else {
		home, homeErr = r.Store.ResolveCompactionHome(ctx, reconcile.WorkID)
	}
	if homeErr != nil {
		return pendingCompaction(base, reconcile.WorkID, step, "resolve_home", homeErr), nil
	}
	note, proofErr := store.FindVerifiedWorkNote(ctx, home, reconcile.WorkID, proofDigest)
	if proofErr != nil {
		return pendingCompaction(base, reconcile.WorkID, step, "git_proof", proofErr), nil
	}
	if workVersion <= 0 {
		return pendingCompaction(base, reconcile.WorkID, step, "resolve_work_version", fmt.Errorf("durable publication did not retain terminal work version")), nil
	}
	changed := []ChangedRef{{EntityKind: "work_item", ID: reconcile.WorkID, Version: strconv.FormatInt(workVersion+1, 10)}}
	payload := mutationPayload(changed, []NextIntent{{Tool: "concord_knowledge", Operation: "resolve_note", QueryID: "PM1.Q10", ReasonCode: "verify_canonical_note"}})
	base.ResolvedScope = &Scope{ProductID: r.Envelope.SelectedProductID, ProjectIDs: []string{r.Envelope.AmbientProjectID}, WorkIDs: []string{reconcile.WorkID}, ScopeVersion: r.Envelope.ScopeVersion}
	result := r.mutationResult(base, payload, changed, []NextIntent{{Tool: "concord_knowledge", Operation: "resolve_note", QueryID: "PM1.Q10", ReasonCode: "verify_canonical_note"}})
	if result.Outcome == OutcomeError {
		return result, nil
	}
	if linkErr := store.PublishCompactionLink(ctx, r.Store, store.CompactionLinkRequest{EventID: reconcile.OperationID + ":reconcile-link", WorkID: reconcile.WorkID, ExpectedVersion: workVersion, Actor: grant.PrincipalRef, OccurredAt: r.Authority.now(), Home: home, CommitOID: note.CommitOID, NotePath: note.NotePath, ExpectedHash: proofDigest, Reason: "reconcile verified orphan note"}); linkErr != nil {
		return pendingCompaction(base, reconcile.WorkID, step, "sqlite_link", linkErr), nil
	}
	changedJSON, _ := json.Marshal(changed[0])
	complete, completeErr := store.CompleteStep(ctx, r.Store, store.CompleteRequest{OpID: reconcile.OperationID, AttemptEpoch: step.AttemptEpoch, ResultKind: store.ResultCompleted, ResultPayload: string(payload), ChangedRefs: []string{string(changedJSON)}, PrincipalRef: grant.PrincipalRef, Tool: r.Tool, IdempotencyKey: idempotencyKey(raw) + ":complete", RequestID: r.Envelope.RequestID, ObservedAt: r.Authority.now(), CompletedAt: timePtr(r.Authority.now())})
	if completeErr != nil {
		return pendingCompaction(base, reconcile.WorkID, step, "sqlite_link", completeErr), nil
	}
	base.Replayed = complete.Replayed
	result.Replayed = complete.Replayed
	return result, nil
}

func pendingCompaction(base Envelope, workID string, claim store.FenceResult, step string, cause error) Envelope {
	ref := operationRefFromFence(claim, "pending", step)
	base.ResolvedScope = &Scope{WorkIDs: []string{workID}}
	return NewPartial(base, ref, []string{"operation_claimed"}, TypedError{Kind: "operation_conflict", RetrySafe: true, RecoveryAction: RecoveryAction{Kind: "reconcile_operation"}, EffectState: EffectPartial, Message: cause.Error()})
}

func decodeChangedRefs(values []string) []ChangedRef {
	out := make([]ChangedRef, 0, len(values))
	for _, value := range values {
		var ref ChangedRef
		if json.Unmarshal([]byte(value), &ref) == nil {
			out = append(out, ref)
		}
	}
	return out
}

func integerScopeValue(scope map[string]any, key string) int64 {
	switch value := scope[key].(type) {
	case float64:
		return int64(value)
	case int64:
		return value
	case int:
		return int64(value)
	default:
		return 0
	}
}

func timePtr(value time.Time) *time.Time { return &value }

func operationRefFromFence(result store.FenceResult, state, step string) OperationRef {
	if state == "" {
		state = "pending"
	}
	return OperationRef{ID: result.OpID, Kind: "compaction", Version: strconv.FormatInt(result.AttemptEpoch, 10), State: OperationState(state), CurrentStep: step, UpdatedAt: time.Now().UTC()}
}

type storeMembership struct {
	ProjectID string `json:"project_id"`
	Role      string `json:"role"`
}

func mutationDigest(tool, operation string, env CallEnvelope, raw []byte) string {
	var input any
	_ = json.Unmarshal(raw, &input)
	if object, ok := input.(map[string]any); ok {
		// Approval references are core-issued authorization handles, not domain
		// intent. Excluding them lets a persisted challenge authorize the exact
		// original intent when it is resubmitted with its opaque reference.
		delete(object, "approval")
		delete(object, "idempotency_key")
	}
	canonical, _ := json.Marshal(struct {
		Tool, Operation, Product, Project string
		Input                             any
	}{tool, operation, env.SelectedProductID, env.AmbientProjectID, input})
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func mutationConsequence(id string) string {
	switch {
	case strings.Contains(id, "supersede"):
		return "supersession"
	case strings.Contains(id, "restore"):
		return "recovery"
	case strings.Contains(id, "transition"):
		return "lifecycle"
	case strings.Contains(id, "membership"):
		return "scope"
	case strings.Contains(id, "link"), strings.Contains(id, "unlink"):
		return "relation"
	default:
		return "intent"
	}
}

func actionIsFenced(action string) bool {
	return strings.HasPrefix(action, "start_") || strings.HasPrefix(action, "run_") || strings.HasPrefix(action, "rollback_")
}

func mutationPayload(changed []ChangedRef, intents []NextIntent) json.RawMessage {
	if changed == nil {
		changed = []ChangedRef{}
	}
	if intents == nil {
		intents = []NextIntent{}
	}
	b, _ := json.Marshal(map[string]any{"changed_refs": mutationResultChangedRefs(changed), "next_valid_intents": mutationResultIntents(intents)})
	return b
}

type mutationResultChangedRef struct {
	EntityKind string `json:"entity_kind"`
	ID         string `json:"id"`
	Version    int64  `json:"version"`
}

func mutationResultChangedRefs(changed []ChangedRef) []mutationResultChangedRef {
	result := make([]mutationResultChangedRef, 0, len(changed))
	for _, ref := range changed {
		version, _ := strconv.ParseInt(ref.Version, 10, 64)
		result = append(result, mutationResultChangedRef{EntityKind: ref.EntityKind, ID: ref.ID, Version: version})
	}
	return result
}

type mutationResultIntent struct {
	Tool       string `json:"tool"`
	Operation  string `json:"operation"`
	ReasonCode string `json:"reason_code"`
}

func mutationResultIntents(intents []NextIntent) []mutationResultIntent {
	result := make([]mutationResultIntent, 0, len(intents))
	for _, intent := range intents {
		result = append(result, mutationResultIntent{Tool: intent.Tool, Operation: intent.Operation, ReasonCode: intent.ReasonCode})
	}
	return result
}
func evidenceLocators(refs []EvidenceRef) []string {
	out := make([]string, len(refs))
	for i, ref := range refs {
		out[i] = ref.Locator
	}
	return out
}
func membershipIDs(values []mutationMembership) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = value.ProjectID
	}
	return out
}

func currentLifecycle(ctx context.Context, tx *sql.Tx, id string) (string, error) {
	var lifecycle string
	if err := tx.QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id=?`, id).Scan(&lifecycle); err != nil {
		return "", err
	}
	return lifecycle, nil
}

// mutationResult is the sole producer for inline and replayed mutation success.
// It validates the exact tool/operation result before the envelope can cross the
// agent boundary, including caller budgets and the canonical envelope cap.
func (r runtime) mutationResult(base Envelope, payload json.RawMessage, changed []ChangedRef, intents []NextIntent) Envelope {
	if err := ValidateOperationPayload(r.Tool, r.Operation, payload, true); err != nil {
		return coreError(base, "malformed_response", fmt.Sprintf("mutation result failed closed-schema validation: %v", err), "contact_operator", false)
	}
	if r.Budget.MaxBytes > 0 && len(payload) > r.Budget.MaxBytes {
		return coreError(base, "budget_refused", "mutation result exceeds requested max_bytes budget", "adjust_budget", false)
	}
	if r.Budget.MaxItems > 0 && maxArrayLength(payload) > r.Budget.MaxItems {
		return coreError(base, "budget_refused", "mutation result exceeds requested max_items budget", "adjust_budget", false)
	}
	response := NewOKMutation(base, payload, changed, intents)
	if err := response.Validate(); err != nil {
		return coreError(base, "malformed_response", fmt.Sprintf("mutation result envelope is invalid: %v", err), "contact_operator", false)
	}
	type envelopeWire Envelope
	encoded, err := json.Marshal(envelopeWire(response))
	if err != nil {
		return coreError(base, "malformed_response", fmt.Sprintf("mutation result envelope cannot be encoded: %v", err), "contact_operator", false)
	}
	if len(encoded) > MaxEnvelopeBytes {
		return coreError(base, "limit_exceeded", fmt.Sprintf("mutation result envelope exceeds %d bytes", MaxEnvelopeBytes), "reduce_limit", false)
	}
	return response
}

func (r runtime) executeMutation(ctx context.Context, base Envelope, raw []byte, digest string, scope, versions map[string]any, consequence, approval string, requiresApproval bool, intents []NextIntent, effect mutationEffect) (Envelope, error) {
	tx, err := r.Store.DB().BeginTx(ctx, nil)
	if err != nil {
		return failureEnvelope(base, err), nil
	}
	rollback := func(err error) (Envelope, error) { _ = tx.Rollback(); return failureEnvelope(base, err), nil }
	inv := Invocation{GrantToken: r.Envelope.GrantRef, ClientRef: r.Envelope.ClientRef, ClientVersion: r.Envelope.ClientVersion, PrincipalRef: r.Envelope.PrincipalRef, SessionRef: r.Envelope.SessionRef, AgentRef: r.Envelope.AgentRef, Directory: r.Envelope.Directory, Worktree: r.Envelope.Worktree, SurfaceVersion: r.Envelope.SurfaceVersion, EnvelopeVersion: r.Envelope.EnvelopeVersion, ManifestDigest: r.Envelope.ManifestDigest, HostAssertionDigest: r.Envelope.HostAssertionDigest, RequiredCapability: capabilityForMutation(r.Tool), ProductID: r.Envelope.SelectedProductID, ProjectID: r.Envelope.AmbientProjectID}
	if inv.HostAssertionDigest == "" {
		inv.HostAssertionDigest = digest
	}
	grant, err := r.Authority.ValidateGrantTx(ctx, tx, inv)
	if err != nil {
		return rollback(err)
	}
	derivedProducts, deriveErr := deriveMutationProductsTx(ctx, tx, scope)
	if deriveErr != nil {
		return rollback(deriveErr)
	}
	if expected, ok := scope["product_ids"].([]string); ok && !equalStrings(expected, derivedProducts) {
		return rollback(newRuntimeFailure("version_conflict", "derived Product scope changed after authorization preflight", "reread_entities", false))
	}
	crossProduct := false
	for _, product := range derivedProducts {
		if !contains(grant.ProductScope, product) {
			return rollback(newRuntimeFailure("unauthorized", fmt.Sprintf("mutation work Product %s is outside grant Product scope %v", product, grant.ProductScope), "contact_operator", false))
		}
		if r.Envelope.SelectedProductID != "" && product != r.Envelope.SelectedProductID {
			crossProduct = true
			if !containsCapability(grant.Capabilities, Capability("cross_scope")) {
				return rollback(newRuntimeFailure("unauthorized", "cross-Product mutation requires cross_scope capability", "contact_operator", false))
			}
		}
	}
	scope["product_ids"] = derivedProducts
	if crossProduct {
		requiresApproval = true
	}
	var priorDigest, priorPayload, priorChanged string
	err = tx.QueryRowContext(ctx, `SELECT canonical_digest,result_payload,changed_refs FROM idempotency_records WHERE principal_ref=? AND tool=? AND operation_kind=? AND idempotency_key=?`, grant.PrincipalRef, r.Tool, r.Operation, idempotencyKey(raw)).Scan(&priorDigest, &priorPayload, &priorChanged)
	if err == nil {
		if priorDigest != digest {
			return rollback(storeIdempotencyConflict(r.Operation, idempotencyKey(raw)))
		}
		var changed []ChangedRef
		_ = json.Unmarshal([]byte(priorChanged), &changed)
		base.Replayed = true
		base.ResolvedScope = &Scope{ProductID: r.Envelope.SelectedProductID, ProjectIDs: []string{r.Envelope.AmbientProjectID}, ScopeVersion: r.Envelope.ScopeVersion}
		response := r.mutationResult(base, json.RawMessage(priorPayload), changed, intents)
		if response.Outcome == OutcomeError {
			_ = tx.Rollback()
			return response, nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE idempotency_records SET replayed_count=replayed_count+1,last_observed_at=? WHERE principal_ref=? AND tool=? AND operation_kind=? AND idempotency_key=?`, r.Authority.now().Format(time.RFC3339Nano), grant.PrincipalRef, r.Tool, r.Operation, idempotencyKey(raw)); err != nil {
			return rollback(err)
		}
		if err := tx.Commit(); err != nil {
			return failureEnvelope(base, err), nil
		}
		return response, nil
	}
	if err != sql.ErrNoRows {
		return rollback(err)
	}
	if requiresApproval && approval == "" {
		challengeScope := boundedApprovalScope(scope)
		challengeRef, challengeErr := r.Authority.CreateApprovalChallengeTx(ctx, tx, inv, ApprovalChallengeSpec{OperationDigest: digest, Scope: challengeScope, Versions: versions, Consequence: consequence, HostAssertionDigest: inv.HostAssertionDigest, ExpiresAt: r.Authority.now().Add(10 * time.Minute)})
		if challengeErr != nil {
			return rollback(challengeErr)
		}
		if err := tx.Commit(); err != nil {
			return failureEnvelope(base, err), nil
		}
		response := coreError(base, "approval_required", "core approval is required for this mutation", "request_approval", false)
		response.Error.Details = map[string]any{"approval_ref": challengeRef, "summary": "Approve the exact requested mutation, scope, and expected versions.", "operation_digest": digest, "scope": approvalScopeBindings(challengeScope), "versions": approvalVersionBindings(versions)}
		return response, nil
	}
	if _, err := r.Authority.ValidateAndConsumeGrantTx(ctx, tx, inv); err != nil {
		return rollback(err)
	}
	if requiresApproval {
		approvalCheck := ApprovalCheck{ApprovalRef: approval, OperationDigest: digest, Scope: boundedApprovalScope(scope), Versions: versions, Consequence: consequence, ClientRef: grant.ClientRef, SessionRef: grant.SessionRef}
		if _, err := r.consumeApprovalTx(ctx, tx, inv, grant, approvalCheck); err != nil {
			_ = tx.Rollback()
			return coreError(base, "approval_invalid", err.Error(), "request_approval", false), nil
		}
	}
	payload, eventIDs, changed, err := effect(ctx, tx, grant)
	if err != nil {
		return rollback(err)
	}
	base.ResolvedScope = scopeFromMap(scope)
	if base.ResolvedScope == nil {
		base.ResolvedScope = &Scope{ProductID: r.Envelope.SelectedProductID, ProjectIDs: []string{r.Envelope.AmbientProjectID}, ScopeVersion: r.Envelope.ScopeVersion}
	}
	result := r.mutationResult(base, payload, changed, intents)
	if result.Outcome == OutcomeError {
		_ = tx.Rollback()
		return result, nil
	}
	key := idempotencyKey(raw)
	changedJSON, _ := json.Marshal(changed)
	authorizedScope, _ := json.Marshal(boundedApprovalScope(scope))
	if _, err := tx.ExecContext(ctx, `INSERT INTO idempotency_records(principal_ref,tool,operation_kind,idempotency_key,canonical_digest,op_id,result_event_ids,result_payload,changed_refs,authorized_scope_snapshot,first_observed_at,last_observed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, grant.PrincipalRef, r.Tool, r.Operation, key, digest, "mutation-"+digest[7:31], marshalEventIDs(eventIDs), string(payload), string(changedJSON), string(authorizedScope), r.Authority.now().Format(time.RFC3339Nano), r.Authority.now().Format(time.RFC3339Nano)); err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return failureEnvelope(base, err), nil
	}
	return result, nil
}

func (r runtime) consumeApprovalTx(ctx context.Context, tx *sql.Tx, inv Invocation, grant Grant, check ApprovalCheck) (store.WorkflowActor, error) {
	var operator store.WorkflowActor
	if r.Envelope.HostApproval == nil {
		return operator, fmt.Errorf("signed host approval assertion is required")
	}
	var err error
	challenge, err := r.Authority.ValidateHostApprovalAssertionTx(ctx, tx, inv, *r.Envelope.HostApproval, check)
	if err != nil {
		return operator, err
	}
	approvalRef := check.ApprovalRef
	if challenge {
		approvalRef, err = r.Authority.CreateApprovalFromChallengeTx(ctx, tx, inv, check.ApprovalRef)
		if err != nil {
			return operator, err
		}
	}
	if err := r.Authority.ValidateAndConsumeApprovalTx(ctx, tx, approvalRef, check); err != nil {
		return operator, err
	}
	if check.RequireOperatorIdentity {
		operator, err = r.Authority.ApprovalAuthorityActorTx(ctx, tx, inv, approvalRef)
		if err != nil {
			return store.WorkflowActor{}, err
		}
	}
	return operator, nil
}

func boundedApprovalScope(scope map[string]any) map[string]any {
	out := make(map[string]any, len(scope))
	for key, value := range scope {
		switch typed := value.(type) {
		case string:
			if typed != "" {
				out[key] = typed
			}
		case []string:
			if len(typed) > 0 {
				out[key] = typed
			}
		default:
			out[key] = value
		}
	}
	return out
}

func idempotencyKey(raw []byte) string {
	var value struct {
		IdempotencyKey string `json:"idempotency_key"`
	}
	_ = json.Unmarshal(raw, &value)
	return value.IdempotencyKey
}
func marshalEventIDs(ids []string) string { b, _ := json.Marshal(ids); return string(b) }
func storeIdempotencyConflict(operation, key string) error {
	return store.IdempotencyConflict(operation, key)
}
func capabilityForMutation(tool string) Capability {
	switch tool {
	case "concord_work_define":
		return "work_define"
	case "concord_work_transition":
		return "work_transition"
	case "concord_work_relate":
		return "work_relate"
	default:
		return "work_compact"
	}
}

func (r runtime) unlinkEffect(digest string, in unlinkMutationInput, preflightEndpoints []string, intents []NextIntent) mutationEffect {
	return func(ctx context.Context, tx *sql.Tx, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
		relationID, err := strconv.ParseInt(in.RelationID, 10, 64)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("invalid relation ID")
		}
		var from, to, kind string
		if err := tx.QueryRowContext(ctx, `SELECT work_id_from,work_id_to,kind FROM relations WHERE id=?`, relationID).Scan(&from, &to, &kind); err != nil {
			return nil, nil, nil, err
		}
		if len(preflightEndpoints) != 2 || from != preflightEndpoints[0] || to != preflightEndpoints[1] {
			return nil, nil, nil, newRuntimeFailure("version_conflict", "relation endpoints changed after scope preflight", "reread_relations", false)
		}
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT pp.product_id FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id IN (?,?)`, from, to)
		if err != nil {
			return nil, nil, nil, err
		}
		for rows.Next() {
			var product string
			if err := rows.Scan(&product); err != nil {
				rows.Close()
				return nil, nil, nil, err
			}
			if !contains(grant.ProductScope, product) || (r.Envelope.SelectedProductID != "" && product != r.Envelope.SelectedProductID && !containsCapability(grant.Capabilities, Capability("cross_scope"))) {
				rows.Close()
				return nil, nil, nil, newRuntimeFailure("unauthorized", "relation endpoint is outside authorized Product scope", "contact_operator", false)
			}
		}
		if err := rows.Close(); err != nil {
			return nil, nil, nil, err
		}
		versions := map[string]int64{}
		for _, item := range in.ExpectedVersions {
			versions[item.WorkID] = item.Version
		}
		if len(versions) < 2 {
			return nil, nil, nil, fmt.Errorf("unlink requires both endpoint versions")
		}
		payload, _ := json.Marshal(map[string]any{"from": from, "to": to, "kind": kind, "reason": in.Reason, "expected_version": versions[from], "resulting_version": versions[from] + 1, "to_expected_version": versions[to], "to_resulting_version": versions[to] + 1})
		result, err := store.ApplyOperationTx(ctx, tx, store.Operation{Events: []store.Event{{EventID: digest + ":unlink", Kind: "relation.removed", SubjectType: store.SubjectWorkItem, SubjectID: from, Actor: grant.PrincipalRef, OccurredAt: r.Authority.now(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, from): versions[from], store.VersionRef(store.SubjectWorkItem, to): versions[to]}})
		if err != nil {
			return nil, nil, nil, err
		}
		changed := []ChangedRef{{EntityKind: "work_item", ID: from, Version: strconv.FormatInt(versions[from]+1, 10)}, {EntityKind: "work_item", ID: to, Version: strconv.FormatInt(versions[to]+1, 10)}}
		return mutationPayload(changed, intents), result.EventIDs, changed, nil
	}
}
