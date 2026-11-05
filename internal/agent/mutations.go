package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/store"
)

type mutationMembership struct {
	ProjectID string `json:"project_id"`
	Role      string `json:"role"`
}
type captureMutationInput struct {
	Title           string   `json:"title"`
	Task            *string  `json:"task"`
	Kind            string   `json:"kind"`
	ProjectIDs      []string `json:"project_ids"`
	HumanKey        string   `json:"human_key"`
	RemoteIssueUUID string   `json:"remote_issue_uuid"`
	URL             string   `json:"url"`
	IdempotencyKey  string   `json:"idempotency_key"`
}
type reviseMutationInput struct {
	Contract        json.RawMessage             `json:"contract"`
	WorkID          string                      `json:"work_id"`
	ExpectedVersion int64                       `json:"expected_version"`
	Title           string                      `json:"title"`
	Task            *string                     `json:"task"`
	Kind            string                      `json:"kind"`
	Reason          string                      `json:"reason"`
	IdempotencyKey  string                      `json:"idempotency_key"`
	Evidence        []agentcontract.EvidenceRef `json:"evidence"`
}
type lifecycleMutationInput struct {
	WorkID          string                      `json:"work_id"`
	ExpectedVersion int64                       `json:"expected_version"`
	Target          string                      `json:"target"`
	Reason          string                      `json:"reason"`
	IdempotencyKey  string                      `json:"idempotency_key"`
	Evidence        []agentcontract.EvidenceRef `json:"evidence"`
}

type workRemovalMutationInput struct {
	OperationID           string                           `json:"operation_id"`
	IdempotencyKey        string                           `json:"idempotency_key"`
	WorkID                string                           `json:"work_id"`
	ExpectedVersion       int64                            `json:"expected_version"`
	Reason                string                           `json:"reason"`
	Actor                 string                           `json:"actor"`
	ProductID             string                           `json:"product_id"`
	Handoff               store.WorkRemovalHandoff         `json:"handoff"`
	Linear                *store.LinearHandoffConfirmation `json:"linear"`
	ExecutionRelinquished bool                             `json:"execution_relinquished"`
	WritesReconciled      bool                             `json:"writes_reconciled"`
	EffectsReconciled     bool                             `json:"effects_reconciled"`
	DependenciesResolved  bool                             `json:"dependencies_resolved"`
	ArtifactsVerified     bool                             `json:"artifacts_verified"`
}

type issueLinkRecordInput struct {
	WorkID          string `json:"work_id"`
	HumanKey        string `json:"human_key"`
	RemoteIssueUUID string `json:"remote_issue_uuid"`
	URL             string `json:"url"`
	IdempotencyKey  string `json:"idempotency_key"`
}
type actionMutationInput struct {
	WorkID          string                      `json:"work_id"`
	ExpectedVersion int64                       `json:"expected_version"`
	ActionID        string                      `json:"action_id"`
	Fields          json.RawMessage             `json:"fields"`
	IdempotencyKey  string                      `json:"idempotency_key"`
	Evidence        []agentcontract.EvidenceRef `json:"evidence"`
}
type membershipsMutationInput struct {
	WorkID          string               `json:"work_id"`
	ExpectedVersion int64                `json:"expected_version"`
	Memberships     []mutationMembership `json:"memberships"`
	IdempotencyKey  string               `json:"idempotency_key"`
}

type resolveOverlapMutationInput struct {
	FromWorkID          string `json:"from_work_id"`
	ToWorkID            string `json:"to_work_id"`
	FromExpectedVersion int64  `json:"from_expected_version"`
	ToExpectedVersion   int64  `json:"to_expected_version"`
	FromContractVersion int64  `json:"from_contract_version"`
	ToContractVersion   int64  `json:"to_contract_version"`
	ResolutionKind      string `json:"resolution_kind"`
	Reason              string `json:"reason"`
	IdempotencyKey      string `json:"idempotency_key"`
}

type mutationEffect func(context.Context, *store.Transaction, Authority) (json.RawMessage, []string, []agentcontract.ChangedRef, error)

func (r runtime) replayMutationBeforeScope(ctx context.Context, base agentcontract.Envelope, raw []byte, grant Authority, op agentcontract.ContractOperation) (agentcontract.Envelope, bool, error) {
	// project_handoff_consume replays state-driven: its recorded success
	// goes stale when its bind names another receiving session or the active
	// contract moved on. Every replay must reach the planner and re-read the
	// recorded state inside the transaction.
	if op.ID == "concord_work_transition.project_handoff_consume" {
		return agentcontract.Envelope{}, false, nil
	}
	key := idempotencyKey(raw)
	if key == "" {
		return agentcontract.Envelope{}, false, nil
	}
	digest := mutationDigest(r.Tool, r.Operation, r.Envelope, raw)
	idempotency := store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: r.Tool, OperationKind: r.Operation, IdempotencyKey: key}
	record, found, err := r.Store.LookupMutationIdempotency(ctx, idempotency)
	if err != nil {
		return agentcontract.Envelope{}, false, err
	}
	if !found {
		return agentcontract.Envelope{}, false, nil
	}
	storedDigest, scopeJSON := record.CanonicalDigest, record.AuthorizedScopeSnapshot
	if storedDigest != digest {
		return coreError(base, "idempotency_conflict", "idempotency key was reused with a different canonical request", "retry_same_request", false), true, nil
	}
	authorizedScope, scopeErr := authorizedScopeFromSnapshot(scopeJSON)
	if scopeErr != nil {
		return coreError(base, "unauthorized", "stored mutation scope is unreadable and cannot be re-authorized", "contact_operator", false), true, nil
	}
	if !scopeWithinAuthority(authorizedScope, grant) {
		return coreError(base, "unauthorized", "original mutation scope is no longer authorized by the current grant", "contact_operator", false), true, nil
	}
	// The replay acknowledges the original authority, not only its observation
	// count. The operation's declared replay baseline and the persisted
	// durability binding select FULL; records without that binding use FULL
	// conservatively when a replay could repeat an external effect.
	declaredDurability, durabilityErr := mutationDurabilityFor(op)
	if durabilityErr != nil {
		return coreError(base, "invariant_violation", durabilityErr.Error(), "contact_operator", false), true, nil
	}
	durable, durabilityErr := mutationReplayDurability(declaredDurability.replay, authorizedScope, r.Envelope.SelectedProductID)
	if durabilityErr != nil {
		return coreError(base, "invariant_violation", durabilityErr.Error(), "contact_operator", false), true, nil
	}
	return r.replayAuthorizedMutation(ctx, base, op, record, authorizedScope, idempotency, durable)
}

func mutationReplayDurability(durable bool, authorizedScope map[string]any, selectedProduct string) (bool, error) {
	if rawDurability, present := authorizedScope["durable_commit"]; present {
		committedDurably, valid := rawDurability.(bool)
		if !valid {
			return false, errors.New("stored mutation durability binding is malformed")
		}
		durable = durable || committedDurably
	}
	// Preserve cross-Product durability on replay. CD-0243 D3/D7 retire
	// ordinary coordination consent, not re-authorization of the Product set.
	if products, present := authorizedScope["product_ids"].([]any); present && selectedProduct != "" {
		for _, rawProduct := range products {
			product, valid := rawProduct.(string)
			if !valid || product == "" {
				return false, errors.New("stored mutation Product binding is malformed")
			}
			if product != selectedProduct {
				durable = true
			}
		}
	}
	return durable, nil
}

func (r runtime) replayAuthorizedMutation(ctx context.Context, base agentcontract.Envelope, op agentcontract.ContractOperation, record store.MutationIdempotencyRecord, authorizedScope map[string]any, idempotency store.MutationIdempotencyKey, durable bool) (agentcontract.Envelope, bool, error) {
	base.Replayed = true
	base.ResolvedScope = scopeFromMap(authorizedScope)
	var response agentcontract.Envelope
	if op.ID == "concord_work_transition.workflow_action" {
		if err := r.Store.Transact(ctx, func(tx *store.Transaction) error {
			var err error
			response, err = r.replayWorkflowActionTx(ctx, tx, base, record, authorizedScope)
			return err
		}); err != nil {
			return agentcontract.Envelope{}, false, err
		}
	} else {
		response = r.recordedMutationResult(base, record)
	}
	if response.Outcome == agentcontract.OutcomeError {
		return response, true, nil
	}
	return r.finishMutationReplay(ctx, response, idempotency, durable)
}

func (r runtime) finishMutationReplay(ctx context.Context, response agentcontract.Envelope, key store.MutationIdempotencyKey, durable bool) (agentcontract.Envelope, bool, error) {
	if !durable {
		return response, true, nil
	}
	observed := r.Authority.now()
	// Consequential replay acknowledges the cached effect and every earlier
	// commit. The metadata update supplies a durable write without repeating
	// the business operation.
	err := r.Store.TransactDurable(ctx, func(tx *store.Transaction) error {
		return store.TouchMutationIdempotencyTx(ctx, tx, key, observed)
	})
	if err != nil {
		return agentcontract.Envelope{}, false, err
	}
	return response, true, nil
}

func scopeFromMap(scope map[string]any) *agentcontract.Scope {
	if scope == nil {
		return nil
	}
	result := &agentcontract.Scope{}
	if value, ok := scope["product_id"].(string); ok {
		result.ProductID = value
	}
	// product_ids stays out of the wire scope: it is derived authority
	// bookkeeping (deriveMutationProducts, stored
	// idempotency snapshots), and the envelope schema closes resolved_scope
	// to product_id, project_ids, work_ids, and scope_version. The list
	// members go through the shared scope-list owner so a path that names
	// the same identity twice resolves to one unique wire scope.
	result.ProjectIDs = scopeListValues(scope["project_ids"])
	result.WorkIDs = scopeListValues(scope["work_ids"])
	if value, ok := scope["scope_version"].(string); ok {
		result.ScopeVersion = value
	}
	return result
}

// preflightWorkflowActionRequest takes the authorized actor
// separately from the envelope. CD-0080 D1 derives principal_ref from the
// registered client, so the envelope carries no principal to build an actor
// tuple from and none to partition idempotency by.
func preflightWorkflowActionRequest(ctx context.Context, s *store.Store, raw []byte, env CallEnvelope, actor Authority) error {
	var in actionMutationInput
	if err := decodeOperationInput(raw, &in); err != nil {
		return newRuntimeFailure("invalid_input", err.Error(), "", false)
	}
	if err := s.RequireActionableWork(ctx, in.WorkID, "workflow_action"); err != nil {
		return err
	}
	payload, err := workflowActionFields(in.Fields)
	if err != nil {
		return newRuntimeFailure("invalid_input", err.Error(), "", false)
	}
	// An exact durable replay is allowed to bypass the now-advanced expected
	// version. The digest comparison remains strict; no authorization callback
	// or mutation is reached until the normal replay path validates the grant.
	key := in.IdempotencyKey
	prior, found, err := s.LookupMutationIdempotency(ctx, store.MutationIdempotencyKey{PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", OperationKind: "workflow_action", IdempotencyKey: key})
	if err != nil {
		return err
	}
	if found {
		if prior.CanonicalDigest != mutationDigest("concord_work_transition", "workflow_action", env, raw) {
			return store.IdempotencyConflict("workflow_action", key)
		}
		return nil
	}
	// One bind-family action records one durable evidence_kind. The refusal
	// sits after the replay window so an exact replay of a request recorded
	// before this rule keeps answering with its stored result (#945).
	if err := validateEvidenceKindBinding(in.ActionID, payload, in.Evidence); err != nil {
		return newRuntimeFailure("invalid_input", err.Error(), "", false)
	}
	// An approval carries an architecture binding that the mutation verifies
	// against the derived Domain registry inside its transaction. The demand
	// for a fresh registry is therefore here, before that transaction opens
	// (CD-0082 D1, and the single-connection rule: a rebuild needs the
	// connection the transaction would hold).
	if in.ActionID == "approve_contract" {
		if err := s.EnsureWorkProductKnowledgeFresh(ctx, in.WorkID); err != nil {
			return err
		}
	}
	_, preflightErr := store.WorkflowActionDefinitionFor(ctx, s, store.WorkflowActionPreflightRequest{
		WorkID:           in.WorkID,
		ExpectedVersion:  in.ExpectedVersion,
		ActionID:         in.ActionID,
		Payload:          payload,
		Actor:            store.WorkflowActor{PrincipalRef: actor.PrincipalRef, ClientRef: actor.ClientRef, AgentRef: actor.AgentRef, SessionRef: actor.SessionRef, ActorClass: store.ActorAgent},
		SessionProjectID: env.AmbientProjectID,
	})
	if preflightErr == nil {
		return nil
	}
	// A concurrent exact request can commit between the idempotency read above
	// and this version-sensitive preflight. Recheck the durable receipt once so
	// that the later mutation transaction can return its stored result.
	prior, found, lookupErr := s.LookupMutationIdempotency(ctx, store.MutationIdempotencyKey{PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", OperationKind: "workflow_action", IdempotencyKey: key})
	if lookupErr != nil || !found {
		return preflightErr
	}
	if prior.CanonicalDigest != mutationDigest("concord_work_transition", "workflow_action", env, raw) {
		return store.IdempotencyConflict("workflow_action", key)
	}
	return nil
}

// evidenceBindingFamily names the actions whose caller-supplied evidence
// array one durable event records under a single evidence_kind. The default
// fill and the consistency refusal both read this one list, so the two can
// never disagree about which actions they govern.
func evidenceBindingFamily(actionID string) bool {
	switch actionID {
	case "bind_evidence", "record_research", "record_report", "approve_operation":
		return true
	default:
		return false
	}
}

// withEvidenceKindDefault carries the caller-supplied evidence kind into the
// payload of the evidence-binding action family when the fields omit it. The
// store derives the durable event's evidence_kind from the payload alone, so
// without this mapping a commit-kind evidence array silently records as
// verification and premise confirmation reads the kind as unbound. The default
// applies only when the array states one kind: an array carrying several
// kinds threads them per entry into the store, and stamping the first kind
// into the fields would contradict the rest and refuse the binding.
func withEvidenceKindDefault(actionID string, payload json.RawMessage, evidence []agentcontract.EvidenceRef) json.RawMessage {
	if !evidenceBindingFamily(actionID) {
		return payload
	}
	kind := ""
	for _, entry := range evidence {
		if entry.Kind == "" {
			continue
		}
		if kind == "" {
			kind = entry.Kind
		} else if entry.Kind != kind {
			return payload
		}
	}
	if kind == "" || len(payload) == 0 {
		return payload
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil || fields == nil {
		return payload
	}
	if _, present := fields["evidence_kind"]; present {
		return payload
	}
	kindJSON, err := json.Marshal(kind)
	if err != nil {
		return payload
	}
	fields["evidence_kind"] = kindJSON
	encoded, err := json.Marshal(fields)
	if err != nil {
		return payload
	}
	return encoded
}

// validateEvidenceKindBinding refuses an evidence array that carries
// different kinds under different locators, and an explicit
// fields.evidence_kind that contradicts any entry's kind. Kinds submitted for
// one locator thread into the store per entry and bind one event per kind;
// kinds spread over several locators name several subjects, which the store
// refuses, so the boundary refuses first and names both kinds (#945).
func validateEvidenceKindBinding(actionID string, payload json.RawMessage, evidence []agentcontract.EvidenceRef) error {
	if !evidenceBindingFamily(actionID) || len(evidence) == 0 {
		return nil
	}
	for i, entry := range evidence {
		if entry.Kind == "" {
			continue
		}
		for _, prior := range evidence[:i] {
			if prior.Kind != "" && entry.Kind != prior.Kind && entry.Locator != prior.Locator {
				return fmt.Errorf("evidence array carries more than one kind (%s, %s); issue one bind per evidence kind", prior.Kind, entry.Kind)
			}
		}
	}
	if len(payload) == 0 {
		return nil
	}
	var fields struct {
		EvidenceKind string `json:"evidence_kind"`
	}
	if err := json.Unmarshal(payload, &fields); err != nil || fields.EvidenceKind == "" {
		return nil
	}
	for _, entry := range evidence {
		if entry.Kind != "" && entry.Kind != fields.EvidenceKind {
			return fmt.Errorf("fields.evidence_kind %q contradicts evidence array kind %q; issue one bind per evidence kind", fields.EvidenceKind, entry.Kind)
		}
	}
	return nil
}

func workflowActionFields(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`{}`), nil
	}
	if err := payloadschema.ValidateUniqueJSON(raw); err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if err := decodeOperationInput(raw, &object); err == nil && object != nil {
		return raw, nil
	}
	var fields []struct {
		Name  string          `json:"name"`
		Value json.RawMessage `json:"value"`
	}
	if err := decodeOperationInput(raw, &fields); err != nil {
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

func (r runtime) replayWorkflowActionTx(ctx context.Context, tx *store.Transaction, base agentcontract.Envelope, prior store.MutationIdempotencyRecord, scope map[string]any) (agentcontract.Envelope, error) {
	operation, err := store.OperationResultTx(ctx, tx, prior.OperationID)
	if err != nil {
		return agentcontract.Envelope{}, err
	}
	base.Replayed = true
	base.ResolvedScope = scopeFromMap(scope)
	if err := agentcontract.ValidateOperationPayload(base.Tool, base.Operation, json.RawMessage(operation.ResultPayload), true); err != nil {
		response := coreError(base, "malformed_response", fmt.Sprintf("durable workflow result is not a valid current result: %v", err), "contact_operator", false)
		response.Error.EffectState = agentcontract.EffectPossible
		return response, nil
	}
	return r.recordedMutationResult(base, prior), nil
}

func (r runtime) workflowActionReplayPreflight(ctx context.Context, base agentcontract.Envelope, digest string, grant Authority, in actionMutationInput, result *agentcontract.Envelope, resultRejected *bool) func(*store.Transaction) (bool, error) {
	return func(tx *store.Transaction) (bool, error) {
		prior, found, err := store.LookupMutationIdempotencyTx(ctx, tx, store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: r.Tool, OperationKind: "workflow_action", IdempotencyKey: in.IdempotencyKey})
		if err != nil {
			return false, err
		}
		if !found {
			return false, nil
		}
		if prior.CanonicalDigest != digest {
			return false, store.IdempotencyConflict("workflow_action", in.IdempotencyKey)
		}
		originalScope, err := authorizedScopeFromSnapshot(prior.AuthorizedScopeSnapshot)
		if err != nil {
			return false, newRuntimeFailure("unauthorized", "stored mutation scope is unreadable and cannot be re-authorized", "contact_operator", false)
		}
		if !scopeWithinAuthority(originalScope, grant) {
			return false, newRuntimeFailure("unauthorized", "original mutation scope is no longer authorized by the current grant", "contact_operator", false)
		}
		replayed, err := r.replayWorkflowActionTx(ctx, tx, base, prior, originalScope)
		if err != nil {
			return false, err
		}
		*result = replayed
		if result.Outcome == agentcontract.OutcomeError {
			*resultRejected = true
			return true, errors.New("mutation result rejected")
		}
		if err := store.TouchMutationIdempotencyTx(ctx, tx, store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: r.Tool, OperationKind: "workflow_action", IdempotencyKey: in.IdempotencyKey}, r.Authority.now()); err != nil {
			return false, err
		}
		return true, nil
	}
}

// knowledgeSourceProofForAction prepares the knowledge-source proof the
// action verifies inside its transaction (store.PrepareWorkflowActionProof).
// It runs after replay and approval admission, with
// no transaction open (CD-0195 D2). A non-nil envelope carries the refusal.
func (r runtime) knowledgeSourceProofForAction(ctx context.Context, base agentcontract.Envelope, actionID, workID string) (context.Context, *agentcontract.Envelope) {
	verifiedCtx, proofErr := r.Store.PrepareWorkflowActionProof(ctx, actionID, workID)
	if proofErr != nil {
		refusal := failureEnvelope(base, proofErr)
		return ctx, &refusal
	}
	return verifiedCtx, nil
}

func (r runtime) mutateWorkflowAction(ctx context.Context, base agentcontract.Envelope, raw []byte, grant Authority, op agentcontract.ContractOperation) (agentcontract.Envelope, error) {
	if r.Store == nil {
		return coreError(base, "invalid_input", "workflow action requires a workflow authority", "contact_operator", false), nil
	}
	var in actionMutationInput
	if err := decodeOperationInput(raw, &in); err != nil {
		return coreError(base, "invalid_input", err.Error(), "", false), nil
	}
	if err := r.Store.RequireActionableWork(ctx, in.WorkID, "workflow_action"); err != nil {
		return failureEnvelope(base, err), nil
	}
	payload, err := workflowActionFields(in.Fields)
	if err != nil {
		return coreError(base, "invalid_input", err.Error(), "", false), nil
	}
	payload = withEvidenceKindDefault(in.ActionID, payload, in.Evidence)
	if err := validateEvidenceKindBinding(in.ActionID, payload, in.Evidence); err != nil {
		return coreError(base, "invalid_input", err.Error(), "", false), nil
	}
	preflight := store.WorkflowActionPreflightRequest{WorkID: in.WorkID, ExpectedVersion: in.ExpectedVersion, ActionID: in.ActionID, Payload: payload, Actor: store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}, SessionProjectID: r.Envelope.AmbientProjectID}
	action, err := store.WorkflowActionDefinitionFor(ctx, r.Store, preflight)
	if err != nil {
		// A state-derived action may close once its effect commits; the
		// identical recorded request still replays.
		if replay, handled, replayErr := r.replayMutationBeforeScope(ctx, base, raw, grant, op); replayErr != nil || handled {
			if replayErr != nil {
				return failureEnvelope(base, replayErr), nil
			}
			return replay, nil
		}
		return failureEnvelope(base, err), nil
	}
	digest := mutationDigest(r.Tool, r.Operation, r.Envelope, raw)
	operationID := "workflow-" + digest[7:31]
	scope := map[string]any{"product_id": r.Envelope.SelectedProductID, "project_ids": []string{r.Envelope.AmbientProjectID}, "work_ids": []string{in.WorkID}, "scope_version": r.Envelope.ScopeVersion}
	if replay, handled, replayErr := r.replayMutationBeforeScope(ctx, base, raw, grant, op); replayErr != nil || handled {
		if replayErr != nil {
			return failureEnvelope(base, replayErr), nil
		}
		return replay, nil
	}
	mutation := workflowActionMutation{runtime: r, base: base, in: in, grant: grant, preflight: preflight, scope: scope, digest: digest, operationID: operationID}
	return mutation.execute(ctx, action)
}

func (m *workflowActionMutation) execute(ctx context.Context, action store.WorkflowActionDefinition) (agentcontract.Envelope, error) {
	r, base, in, grant := m.runtime, m.base, m.in, m.grant
	preflight, scope := m.preflight, m.scope
	digest, operationID := m.digest, m.operationID
	payload := preflight.Payload
	var takeover *store.WorkflowStartTakeover
	if store.WorkflowActionOpensHeldAttempt(action) {
		var err error
		takeover, err = store.WorkflowStartTakeoverRead(ctx, r.Store, action, preflight)
		if err != nil {
			return failureEnvelope(base, err), nil
		}
	}
	// CD-0038 D3: budget admission for a mutation follows its idempotency
	// lookup. A replay that matches the recorded digest returns the original
	// result regardless of the ceiling, so a lowered ceiling can never strand
	// an already-committed request behind a refusal.
	if r.Budget.CeilingRefused {
		return r.budgetRefusal(base, fmt.Sprintf("requested_budget_seconds %d exceeds supported %d", r.Budget.RequestedSeconds, r.Budget.SupportedSeconds)), nil
	}
	// CD-0059 D3: per-action capability resolution is structural. The
	// action declares the capability the dispatcher must hold;
	// legacy actions that did not declare one default to work_transition
	// so pre-CD-0059 actions retain their authority contract.
	requiredCapability := agentcontract.Capability("work_transition")
	if action.RequiredCapability != "" {
		requiredCapability = agentcontract.Capability(action.RequiredCapability)
	}
	inv := envelopeInvocation(r.Envelope, requiredCapability, r.Operation)
	if inv.HostAssertionDigest == "" {
		inv.HostAssertionDigest = digest
	}
	// The host probes before any transaction this mutation opens (CD-0195 D2);
	// each authorization inside matches the probed host with SQL only.
	host, hostErr := r.probeInvocationHost(ctx)
	if hostErr != nil {
		return failureEnvelope(base, hostErr), nil
	}
	scopeJSON, _ := json.Marshal(scope)
	actionRequest := store.WorkflowActionExecutionRequest{WorkID: in.WorkID, ExpectedVersion: in.ExpectedVersion, ActionID: in.ActionID, Payload: payload, EvidenceRefs: evidenceLocators(in.Evidence), EvidenceKinds: evidenceKinds(in.Evidence), Actor: store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}, ExpectedTakeover: takeover, AcceptedInputsDigest: digest, IdempotencyIdentity: in.IdempotencyKey, OperationID: operationID, PrincipalRef: grant.PrincipalRef, Tool: r.Tool, IdempotencyKey: in.IdempotencyKey, RequestID: r.Envelope.RequestID, AcceptedScope: string(scopeJSON), ContractDigest: agentcontract.ManifestDigest, Now: r.Authority.now()}
	// CD-0200: the approval, supersession, and completion gates each run the
	// mandated-law boundary inside their transaction, so their registered
	// source set verifies before the transaction opens (store connection
	// invariant); the returned context carries the proof.
	verifiedCtx, proofEnvelope := r.knowledgeSourceProofForAction(ctx, base, in.ActionID, in.WorkID)
	if proofEnvelope != nil {
		return *proofEnvelope, nil
	}
	ctx = verifiedCtx
	m.host, m.inv, m.scopeJSON, m.takeover, m.request = host, inv, string(scopeJSON), takeover, actionRequest
	err := store.AuthorizeWorkflowActionAtBoundaryWithPreflightTx(ctx, r.Store, preflight, r.workflowActionReplayPreflight(ctx, base, digest, grant, in, &m.result, &m.resultRejected), func(tx *store.Transaction) error {
		return m.authorizeTx(ctx, tx)
	}, func(tx *store.Transaction) error {
		return m.applyTx(ctx, tx)
	})
	if err != nil {
		if m.resultRejected {
			return m.result, nil
		}
		return failureEnvelope(m.base, err), nil
	}
	return m.result, nil
}

type workflowActionMutation struct {
	runtime
	base                           agentcontract.Envelope
	in                             actionMutationInput
	grant                          Authority
	preflight                      store.WorkflowActionPreflightRequest
	host                           store.ResolvedProjectHost
	inv                            Invocation
	scope                          map[string]any
	scopeJSON, digest, operationID string
	takeover                       *store.WorkflowStartTakeover
	request                        store.WorkflowActionExecutionRequest
	result                         agentcontract.Envelope
	resultRejected                 bool
}

func (m *workflowActionMutation) authorizeTx(ctx context.Context, tx *store.Transaction) error {
	r, in, grant := m.runtime, m.in, m.grant
	if _, err := r.Authority.AuthorizeTx(ctx, tx, m.host, m.inv); err != nil {
		return err
	}
	return store.InsertMutationIdempotencyTx(ctx, tx, store.MutationIdempotencyInsert{Key: store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: r.Tool, OperationKind: "workflow_action", IdempotencyKey: in.IdempotencyKey}, CanonicalDigest: m.digest, OperationID: m.operationID, ResultEventIDs: "[]", ResultPayload: "{}", ChangedRefs: "[]", AuthorizedScopeSnapshot: m.scopeJSON, ObservedAt: r.Authority.now()})
}

func (m *workflowActionMutation) applyTx(ctx context.Context, tx *store.Transaction) error {
	r, in, grant := m.runtime, m.in, m.grant
	base := m.base
	execution, err := store.ApplyWorkflowActionTx(ctx, tx, m.request)
	if err != nil {
		return err
	}
	changedVersion := execution.ResultingVersion
	if changedVersion == 0 {
		changedVersion = in.ExpectedVersion + 1
	}
	changed := []agentcontract.ChangedRef{{EntityKind: "work_item", ID: in.WorkID, Version: strconv.FormatInt(changedVersion, 10)}}
	base.ResolvedScope = scopeFromMap(m.scope)
	m.base = base
	resultPayload, derivedIntents, pinless, enrichErr := r.enrichMutationPayloadTx(ctx, tx, execution.Result, changed, execution.EventIDs...)
	if enrichErr != nil {
		return enrichErr
	}
	base.Omissions = append(base.Omissions, mutationPinOmissions(pinless)...)
	m.base = base
	m.result = r.mutationResult(base, resultPayload, changed, derivedIntents, pinless...)
	if m.result.Outcome == agentcontract.OutcomeError {
		m.resultRejected = true
		return errors.New("mutation result rejected")
	}
	changedJSON, _ := json.Marshal(changed)
	responseScope, _ := json.Marshal(mutationResponseScope(m.scope, agentcontract.NewOKMutation(base, resultPayload, changed, derivedIntents), pinless))
	return store.CompleteMutationResponseTx(ctx, tx, store.MutationIdempotencyInsert{Key: store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: r.Tool, OperationKind: "workflow_action", IdempotencyKey: in.IdempotencyKey}, CanonicalDigest: m.digest, OperationID: m.operationID, ResultPayload: string(resultPayload), ChangedRefs: string(changedJSON), AuthorizedScopeSnapshot: string(responseScope), ResultEventIDs: marshalEventIDs(execution.EventIDs), ObservedAt: r.Authority.now()})
}

func workKindMutationRefusal(kind string, allowed bool, fallback string) (string, bool) {
	if allowed {
		return "", false
	}
	message, _, ok := store.WorkKindRefusalFor(kind)
	if ok {
		return message, true
	}
	return fallback, true
}

// mutationPlan carries the scope, next intents, and transactional effect
// prepared by one operation handler for the common executor.
type mutationPlan struct {
	scope   map[string]any
	intents []agentcontract.NextIntent
	effect  mutationEffect
	// evidenceRefs surfaces the operation's evidence on the result envelope.
	// A lifecycle transition into a terminal state sets it so the envelope
	// that reports the state also carries the evidence behind it.
	evidenceRefs []agentcontract.EvidenceRef
	// plannedInitialDurability is the optional initial-durability fact a
	// planner raises from its own consequence branch. Nil leaves the
	// operation's declared baseline in force.
	plannedInitialDurability *bool
}

func newMutationPlan(envelope CallEnvelope) *mutationPlan {
	return &mutationPlan{
		scope: map[string]any{"product_id": envelope.SelectedProductID, "project_ids": []string{envelope.AmbientProjectID}, "scope_version": envelope.ScopeVersion},
	}
}

// declareInitialDurability records the executor's initial-durability fact.
// Only the operation's actual consequence branch supplies it.
func (plan *mutationPlan) declareInitialDurability(durable bool) {
	plan.plannedInitialDurability = &durable
}

// planCapture plans concord_work_define.capture.
func (r runtime) planCapture(ctx context.Context, base agentcontract.Envelope, raw []byte, digest string, _ Authority, _ agentcontract.ContractOperation, plan *mutationPlan) (agentcontract.Envelope, error, bool) {
	var in captureMutationInput
	if err := decodeOperationInput(raw, &in); err != nil {
		return base, err, true
	}
	if message, refused := workKindMutationRefusal(in.Kind, store.WorkKindAgentCaptureAllowed(in.Kind), "work kind is not capturable"); refused {
		return coreError(base, "invalid_input", message, "reread_entities", false), nil, true
	}
	if len(in.ProjectIDs) == 0 {
		return coreError(base, "invalid_input", "capture requires at least one Project membership", "reread_entities", false), nil, true
	}
	workID := "work-" + digest[7:31]
	productsByProject, scopeErr := r.Store.ProductsForProjectIDs(ctx, in.ProjectIDs)
	if scopeErr != nil {
		return failureEnvelope(base, scopeErr), nil, true
	}
	productIDs := captureProductIDs(productsByProject)
	for _, product := range productIDs {
		if r.Envelope.SelectedProductID != "" && product != r.Envelope.SelectedProductID {
			plan.declareInitialDurability(true)
		}
	}
	plan.scope["project_ids"] = in.ProjectIDs
	plan.effect = func(ctx context.Context, tx *store.Transaction, grant Authority) (json.RawMessage, []string, []agentcontract.ChangedRef, error) {
		return r.applyCaptureTx(ctx, tx, grant, in, digest, workID)
	}
	return agentcontract.Envelope{}, nil, false
}

func captureProductIDs(productsByProject map[string][]string) []string {
	productIDs := make([]string, 0, len(productsByProject))
	seenProducts := make(map[string]struct{})
	for _, products := range productsByProject {
		for _, product := range products {
			if _, seen := seenProducts[product]; seen {
				continue
			}
			seenProducts[product] = struct{}{}
			productIDs = append(productIDs, product)
		}
	}
	return productIDs
}

func (r runtime) applyCaptureTx(ctx context.Context, tx *store.Transaction, grant Authority, in captureMutationInput, digest, workID string) (json.RawMessage, []string, []agentcontract.ChangedRef, error) {
	// The captured note rides the work.created payload into the same
	// intent_json.task projection a revise writes, so one call persists
	// the operator's instruction identically to a later revise.
	payloadFields := map[string]any{"work_kind": in.Kind, "title": in.Title}
	if in.Task != nil {
		payloadFields["task"] = *in.Task
	}
	payload, _ := json.Marshal(payloadFields)
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
		{EventID: digest + ":create", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: grant.PrincipalRef, OccurredAt: now, PayloadVersion: 4, Payload: payload},
		{EventID: digest + ":memberships", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: grant.PrincipalRef, OccurredAt: now, PayloadVersion: 1, Payload: membershipPayload},
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, workID): 0}})
	if err != nil {
		return nil, nil, nil, err
	}
	// The Linear issue identity the caller read from the Linear MCP server
	// rides the same transaction as the capture (CD-0213 D3): the link row
	// is the only Linear identity carrier, and its one-live-item rule is
	// the collision gate.
	if _, err := store.RecordLinearIssueLinkTx(ctx, tx, store.LinearIssueLink{WorkID: workID, RemoteIssueUUID: in.RemoteIssueUUID, HumanKey: in.HumanKey, URL: in.URL}); err != nil {
		return nil, nil, nil, err
	}
	actor := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	if err := store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: workID, Actor: actor, Now: now}); err != nil {
		return nil, nil, nil, err
	}
	eventIDs := result.EventIDs
	changed := []agentcontract.ChangedRef{{EntityKind: "work_item", ID: workID, Version: "3"}, {EntityKind: "linear_issue_link", ID: workID, Version: "1"}}
	return mutationPayload(changed, nil), eventIDs, changed, nil
}

// planReviseIntent plans concord_work_define.revise_intent.
func (r runtime) planReviseIntent(ctx context.Context, base agentcontract.Envelope, raw []byte, digest string, _ Authority, _ agentcontract.ContractOperation, plan *mutationPlan) (agentcontract.Envelope, error, bool) {
	var in reviseMutationInput
	if err := decodeOperationInput(raw, &in); err != nil {
		return base, err, true
	}
	if message, refused := workKindMutationRefusal(in.Kind, store.WorkKindFoldReviseAllowed(in.Kind), "work kind cannot be revised"); refused {
		return coreError(base, "invalid_input", message, "reread_entities", false), nil, true
	}
	plan.scope["work_ids"] = []string{in.WorkID}
	verifiedCtx := ctx
	if in.Contract != nil {
		if err := r.Store.EnsureWorkProductKnowledgeFresh(ctx, in.WorkID); err != nil {
			return failureEnvelope(base, err), nil, true
		}
		var proofErr error
		verifiedCtx, proofErr = r.Store.EstablishKnowledgeSourceSetProof(ctx, in.WorkID)
		if proofErr != nil {
			return failureEnvelope(base, proofErr), nil, true
		}
	}
	plan.effect = func(ctx context.Context, tx *store.Transaction, grant Authority) (json.RawMessage, []string, []agentcontract.ChangedRef, error) {
		return r.applyReviseIntentTx(verifiedCtx, tx, grant, in, digest, plan)
	}
	return agentcontract.Envelope{}, nil, false
}

func (r runtime) applyReviseIntentTx(ctx context.Context, tx *store.Transaction, grant Authority, in reviseMutationInput, digest string, plan *mutationPlan) (json.RawMessage, []string, []agentcontract.ChangedRef, error) {
	payloadFields := map[string]any{"title": in.Title, "kind": in.Kind, "reason": in.Reason, "evidence_refs": evidenceLocators(in.Evidence), "expected_version": in.ExpectedVersion, "resulting_version": in.ExpectedVersion + 1}
	actor := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	eventActor := grant.PrincipalRef
	if in.Contract != nil {
		payloadFields["contract"] = in.Contract
		payloadFields["actor"] = actor
		var err error
		eventActor, err = store.WorkflowActorRef(actor)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	if in.Task != nil {
		payloadFields["task"] = *in.Task
	}
	payload, _ := json.Marshal(payloadFields)
	result, err := store.ApplyOperationTx(ctx, tx, store.Operation{Events: []store.Event{{EventID: digest + ":revise", Kind: "work.intent_revised", SubjectType: store.SubjectWorkItem, SubjectID: in.WorkID, Actor: eventActor, OccurredAt: r.Authority.now(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, in.WorkID): in.ExpectedVersion}})
	if err != nil {
		return nil, nil, nil, err
	}
	changedVersion := in.ExpectedVersion + 1
	eventIDs := result.EventIDs
	changed := []agentcontract.ChangedRef{{EntityKind: "work_item", ID: in.WorkID, Version: strconv.FormatInt(changedVersion, 10)}}
	return mutationPayload(changed, plan.intents), eventIDs, changed, nil
}

// planLifecycle plans concord_work_transition.lifecycle.
func (r runtime) planLifecycle(_ context.Context, base agentcontract.Envelope, raw []byte, digest string, _ Authority, _ agentcontract.ContractOperation, plan *mutationPlan) (agentcontract.Envelope, error, bool) {
	var in lifecycleMutationInput
	if err := decodeOperationInput(raw, &in); err != nil {
		return base, err, true
	}
	if in.Target != "needed" && in.Target != "in_progress" && in.Target != "completed" && in.Target != "cancelled" && in.Target != "reopen" {
		return coreError(base, "invalid_input", "unknown lifecycle target "+in.Target, "reread_entities", false), nil, true
	}
	// CD-0243 D3/D7 retire terminal-status consent, not verification evidence.
	if (in.Target == "completed" || in.Target == "cancelled") && len(in.Evidence) == 0 {
		response := coreError(base, "missing_evidence", "terminal lifecycle transition requires verification evidence", "provide_evidence", false)
		response.Error.Details = map[string]any{"operation_digest": digest, "work_id": in.WorkID, "expected_version": in.ExpectedVersion, "required_kind": "verification"}
		return response, nil, true
	}
	terminalTarget := in.Target == "completed" || in.Target == "cancelled"
	plan.declareInitialDurability(terminalTarget)
	if in.Target == "completed" {
		plan.evidenceRefs = append([]agentcontract.EvidenceRef{}, in.Evidence...)
	}
	plan.scope["work_ids"] = []string{in.WorkID}
	plan.intents = []agentcontract.NextIntent{{Tool: "concord_work_browse", Operation: "list", QueryID: "PM1.Q3", ReasonCode: "refresh_work_version", RequiredFields: []string{"work_ids", "detail"}}}
	plan.effect = func(ctx context.Context, tx *store.Transaction, grant Authority) (json.RawMessage, []string, []agentcontract.ChangedRef, error) {
		from, err := currentLifecycle(ctx, tx, in.WorkID)
		if err != nil {
			return nil, nil, nil, err
		}
		kind := "work.transitioned"
		if in.Target == "reopen" {
			// Reopen is the composite return from a terminal state: the
			// work.reopened fold owns it, and it invalidates every overlap
			// resolution the item still holds (CON-1571 end state 1).
			kind = "work.reopened"
		}
		payloadFields := map[string]any{"from": from, "reason": in.Reason, "evidence_refs": evidenceLocators(in.Evidence), "expected_version": in.ExpectedVersion, "resulting_version": in.ExpectedVersion + 1}
		if kind == "work.transitioned" {
			payloadFields["to"] = in.Target
		}
		payload, _ := json.Marshal(payloadFields)
		result, err := store.ApplyOperationTx(ctx, tx, store.Operation{Events: []store.Event{{EventID: digest + ":transition", Kind: kind, SubjectType: store.SubjectWorkItem, SubjectID: in.WorkID, Actor: grant.PrincipalRef, OccurredAt: r.Authority.now(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, in.WorkID): in.ExpectedVersion}})
		if err != nil {
			return nil, nil, nil, err
		}
		changed := []agentcontract.ChangedRef{{EntityKind: "work_item", ID: in.WorkID, Version: strconv.FormatInt(in.ExpectedVersion+1, 10)}}
		resultPayload := mutationPayload(changed, plan.intents)
		return resultPayload, result.EventIDs, changed, nil
	}
	return agentcontract.Envelope{}, nil, false
}

// planIssueLinkRecord plans concord_work_define.issue_link_record: record the
// Linear issue identity the agent read from the Linear MCP server on one work
// item. Concord makes no Linear call (CD-0213 D3, D7), and the record bumps no
// work version.
func (r runtime) planIssueLinkRecord(_ context.Context, base agentcontract.Envelope, raw []byte, _ string, _ Authority, _ agentcontract.ContractOperation, plan *mutationPlan) (agentcontract.Envelope, error, bool) {
	var in issueLinkRecordInput
	if err := decodeOperationInput(raw, &in); err != nil {
		return base, err, true
	}
	plan.scope["work_ids"] = []string{in.WorkID}
	plan.effect = func(ctx context.Context, tx *store.Transaction, grant Authority) (json.RawMessage, []string, []agentcontract.ChangedRef, error) {
		link, err := store.RecordLinearIssueLinkTx(ctx, tx, store.LinearIssueLink{WorkID: in.WorkID, RemoteIssueUUID: in.RemoteIssueUUID, HumanKey: in.HumanKey, URL: in.URL})
		if err != nil {
			return nil, nil, nil, err
		}
		changed := []agentcontract.ChangedRef{{EntityKind: "linear_issue_link", ID: link.WorkID, Version: "1"}}
		return mutationPayload(changed, plan.intents), []string{link.WorkID}, changed, nil
	}
	return agentcontract.Envelope{}, nil, false
}

// projectHandoffRecordInput is concord_work_transition.project_handoff_record
// (CD-0182 amendment). The source Project and source session resolve from the
// authenticated call context; the input only names the receiving Project and
// the handoff content.
type projectHandoffRecordInput struct {
	IdempotencyKey  string   `json:"idempotency_key"`
	WorkID          string   `json:"work_id"`
	TargetProjectID string   `json:"target_project_id"`
	BoundedJob      string   `json:"bounded_job"`
	NextAction      string   `json:"next_action"`
	Changes         []string `json:"changes"`
	Verification    []string `json:"verification"`
	ArtifactRefs    []string `json:"artifact_refs"`
	Blockers        []string `json:"blockers"`
}

// projectHandoffConsumeInput is concord_work_transition.project_handoff_consume.
// The consuming Project and session resolve from the authenticated call
// context; a caller that names them is ignored.
type projectHandoffConsumeInput struct {
	IdempotencyKey string `json:"idempotency_key"`
	WorkID         string `json:"work_id"`
	HandoffID      string `json:"handoff_id"`
}

// planProjectHandoffRecord plans the source side of the Project-session
// handoff. The artifact-preservation probe runs here, in the plan phase,
// BEFORE the mutation transaction opens: it is a git/filesystem fact, and
// the single-connection store cannot observe a subprocess parked inside a
// transaction. The probe runs in the session's host-reported worktree, the
// tree whose changes the session's departure could lose; the authority
// already refused the call unless that worktree resolves the ambient Project
// and is not the main checkout. Content drift inside the probed tree after
// the probe is not re-probeable inside a transaction; the retirement read
// re-proves preservation from the recorded references before any readiness.
func (r runtime) planProjectHandoffRecord(ctx context.Context, base agentcontract.Envelope, raw []byte, _ string, grant Authority, _ agentcontract.ContractOperation, plan *mutationPlan) (agentcontract.Envelope, error, bool) {
	var in projectHandoffRecordInput
	if err := decodeOperationInput(raw, &in); err != nil {
		return base, err, true
	}
	project := r.Envelope.AmbientProjectID
	if project == "" {
		return coreError(base, "unknown_scope", "project handoff record requires a resolved Project", "refresh_context", false), nil, true
	}
	if grant.SessionRef == "" || grant.Worktree == "" {
		return coreError(base, "unknown_scope", "project handoff record requires the session's authenticated session and linked worktree", "refresh_context", false), nil, true
	}
	if r.Store == nil {
		return coreError(base, "unreachable", "project handoff record requires the store authority", "contact_operator", true), nil, true
	}
	plan.scope["work_ids"] = []string{in.WorkID}
	plan.scope["project_ids"] = []string{project, in.TargetProjectID}
	plan.intents = []agentcontract.NextIntent{{Tool: "concord_work_trace", Operation: "project_retirement", QueryID: "CD-0182.R1", ReasonCode: "verify_handoff_recorded", RequiredFields: []string{"work_id"}}}
	req := store.RecordProjectHandoffRequest{
		WorkID: in.WorkID, SourceProjectID: project, TargetProjectID: in.TargetProjectID, SourceSessionRef: grant.SessionRef,
		BoundedJob: in.BoundedJob, Changes: in.Changes, Verification: in.Verification, ArtifactRefs: in.ArtifactRefs,
		Blockers: in.Blockers, NextAction: in.NextAction,
	}
	// Outside any transaction: the probe reads git and the artifact files.
	if err := store.VerifyProjectHandoffArtifactPreservation(ctx, grant.Worktree, in.ArtifactRefs); err != nil {
		return failureEnvelope(base, err), nil, true
	}
	plan.effect = func(ctx context.Context, tx *store.Transaction, grant Authority) (json.RawMessage, []string, []agentcontract.ChangedRef, error) {
		recorded, err := store.RecordProjectHandoffTx(ctx, tx, req)
		if err != nil {
			return nil, nil, nil, err
		}
		payload, err := json.Marshal(map[string]any{
			"changed_refs":       mutationResultChangedRefs([]agentcontract.ChangedRef{}),
			"next_valid_intents": mutationResultIntents(plan.intents),
			"work_id":            in.WorkID,
			"handoff_id":         recorded.HandoffID,
			"already_recorded":   recorded.AlreadyRecorded,
		})
		return payload, nil, []agentcontract.ChangedRef{}, err
	}
	return agentcontract.Envelope{}, nil, false
}

// planProjectHandoffConsume plans the receiving side of the Project-session
// handoff. The core binds the consume to the work item, the ambient
// receiving Project, and the active contract (CD-0182 D5 amendment): a
// missing, wrong-target, or stale consume refuses with no event, a frontier
// another session of the Project already consumed resolves from the
// standing shared bind, and managed execution stays closed until a bind
// stands.
func (r runtime) planProjectHandoffConsume(_ context.Context, base agentcontract.Envelope, raw []byte, _ string, grant Authority, _ agentcontract.ContractOperation, plan *mutationPlan) (agentcontract.Envelope, error, bool) {
	var in projectHandoffConsumeInput
	if err := decodeOperationInput(raw, &in); err != nil {
		return base, err, true
	}
	project := r.Envelope.AmbientProjectID
	if project == "" {
		return coreError(base, "unknown_scope", "project handoff consume requires a resolved Project", "refresh_context", false), nil, true
	}
	if grant.SessionRef == "" {
		return coreError(base, "unknown_scope", "project handoff consume requires the session's authenticated session", "refresh_context", false), nil, true
	}
	plan.scope["work_ids"] = []string{in.WorkID}
	plan.scope["project_ids"] = []string{project}
	plan.intents = []agentcontract.NextIntent{{Tool: "concord_work_trace", Operation: "continuity", QueryID: "C19.Continuity", ReasonCode: "read_consumed_handoff_context", RequiredFields: []string{"work_id"}}}
	req := store.ConsumeProjectHandoffRequest{
		WorkID: in.WorkID, HandoffID: in.HandoffID, ConsumerProjectID: project, ConsumerSessionRef: grant.SessionRef,
	}
	plan.effect = func(ctx context.Context, tx *store.Transaction, grant Authority) (json.RawMessage, []string, []agentcontract.ChangedRef, error) {
		consumed, err := store.ConsumeProjectHandoffTx(ctx, tx, req)
		if err != nil {
			return nil, nil, nil, err
		}
		payload, err := json.Marshal(map[string]any{
			"changed_refs":       mutationResultChangedRefs([]agentcontract.ChangedRef{}),
			"next_valid_intents": mutationResultIntents(plan.intents),
			"work_id":            in.WorkID,
			"handoff_id":         consumed.HandoffID,
			"already_consumed":   consumed.AlreadyConsumed,
		})
		return payload, nil, []agentcontract.ChangedRef{}, err
	}
	return agentcontract.Envelope{}, nil, false
}

// planWorkRemoval plans the operator-controlled, per-item removal operation.
func (r runtime) planWorkRemoval(ctx context.Context, base agentcontract.Envelope, raw []byte, digest string, grant Authority, _ agentcontract.ContractOperation, plan *mutationPlan) (agentcontract.Envelope, error, bool) {
	var in workRemovalMutationInput
	if err := decodeOperationInput(raw, &in); err != nil {
		return base, err, true
	}
	product := in.ProductID
	if product == "" {
		product = r.Envelope.SelectedProductID
	}
	if product != "" {
		products, err := r.Store.ProductsForWorkIDs(ctx, []string{in.WorkID})
		if err != nil {
			return failureEnvelope(base, err), nil, true
		}
		if !slices.Contains(products[in.WorkID], product) {
			return coreError(base, "ambiguous_scope", "removal Product is not an authorized Product of the work item", "resolve_ambiguity", false), nil, true
		}
	}
	removalRequest := store.WorkRemovalRequest{
		OperationID: in.OperationID, IdempotencyKey: in.IdempotencyKey, WorkID: in.WorkID,
		ExpectedVersion: in.ExpectedVersion, Reason: in.Reason, Actor: grant.PrincipalRef,
		ProductID: product, Handoff: in.Handoff, Linear: in.Linear,
		ExecutionRelinquished: in.ExecutionRelinquished, WritesReconciled: in.WritesReconciled,
		EffectsReconciled: in.EffectsReconciled, DependenciesResolved: in.DependenciesResolved,
		ArtifactsVerified: in.ArtifactsVerified,
	}
	if grant.MainWorktree {
		if product != r.Envelope.SelectedProductID {
			return coreError(base, "unauthorized", "default-checkout shelving requires the selected Product", "contact_operator", false), nil, true
		}
		project, _ := grant.ScopeSnapshot["project_id"].(string)
		if err := r.Store.CheckDefaultCheckoutRemoval(ctx, removalRequest, project); err != nil {
			return failureEnvelope(base, err), nil, true
		}
	}
	plan.scope["work_ids"] = []string{in.WorkID}
	plan.scope["product_ids"] = nonEmpty(product)
	// No next intent is served. The work projection is deleted, so every
	// read that resolves live work scope refuses the removed identity, and
	// the retained document read (mutation_result) accepts workflow_action
	// sources only. CD-0113 D3 bars a hand-written intent that names a work
	// mutation, so advertising the replay is not allowed either. The
	// retained acknowledgement this result carries is the audit record;
	// replaying this operation re-serves it from the idempotency record.
	plan.effect = func(ctx context.Context, tx *store.Transaction, grant Authority) (json.RawMessage, []string, []agentcontract.ChangedRef, error) {
		removalRequest.Actor = grant.PrincipalRef
		if grant.MainWorktree {
			project, _ := grant.ScopeSnapshot["project_id"].(string)
			if err := store.CheckDefaultCheckoutRemovalTx(ctx, tx, removalRequest, project); err != nil {
				return nil, nil, nil, err
			}
		}
		prepared, err := store.PrepareWorkRemovalTx(ctx, tx, removalRequest)
		if err != nil {
			return nil, nil, nil, err
		}
		payload, err := store.MarshalWorkRemovedPayload(removalRequest, prepared.HandoffDigest)
		if err != nil {
			return nil, nil, nil, err
		}
		result, err := store.ApplyOperationTx(ctx, tx, store.Operation{Events: []store.Event{{EventID: "work.removed:" + in.OperationID, Kind: store.WorkRemoved, SubjectType: store.SubjectWorkItem, SubjectID: in.WorkID, Actor: grant.PrincipalRef, OccurredAt: r.Authority.now(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, in.WorkID): in.ExpectedVersion}})
		if err != nil {
			return nil, nil, nil, err
		}
		changed := []agentcontract.ChangedRef{{EntityKind: "work_item", ID: in.WorkID, Version: strconv.FormatInt(in.ExpectedVersion+1, 10)}}
		return mutationPayload(changed, plan.intents), result.EventIDs, changed, nil
	}
	_ = digest
	return agentcontract.Envelope{}, nil, false
}

// planSetMemberships plans concord_work_relate.set_memberships.
func (r runtime) planSetMemberships(_ context.Context, base agentcontract.Envelope, raw []byte, digest string, _ Authority, _ agentcontract.ContractOperation, plan *mutationPlan) (agentcontract.Envelope, error, bool) {
	var in membershipsMutationInput
	if err := decodeOperationInput(raw, &in); err != nil {
		return base, err, true
	}
	if len(in.Memberships) == 0 {
		return coreError(base, "invalid_input", "membership replacement cannot be empty", "reread_entities", false), nil, true
	}
	plan.declareInitialDurability(true)
	plan.scope["work_ids"] = []string{in.WorkID}
	plan.scope["project_ids"] = membershipIDs(in.Memberships)
	plan.intents = []agentcontract.NextIntent{{Tool: "concord_work_browse", Operation: "list", QueryID: "PM1.Q3", ReasonCode: "refresh_membership_scope", RequiredFields: []string{"work_ids", "detail"}}}
	plan.effect = func(ctx context.Context, tx *store.Transaction, grant Authority) (json.RawMessage, []string, []agentcontract.ChangedRef, error) {
		payload, _ := json.Marshal(map[string]any{"memberships": in.Memberships, "expected_version": in.ExpectedVersion, "resulting_version": in.ExpectedVersion + 1})
		result, err := store.ApplyOperationTx(ctx, tx, store.Operation{Events: []store.Event{{EventID: digest + ":memberships", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: in.WorkID, Actor: grant.PrincipalRef, OccurredAt: r.Authority.now(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, in.WorkID): in.ExpectedVersion}})
		if err != nil {
			return nil, nil, nil, err
		}
		changed := []agentcontract.ChangedRef{{EntityKind: "work_item", ID: in.WorkID, Version: strconv.FormatInt(in.ExpectedVersion+1, 10)}}
		return mutationPayload(changed, plan.intents), result.EventIDs, changed, nil
	}
	return agentcontract.Envelope{}, nil, false
}

// planResolveOverlap plans concord_work_relate.resolve_overlap.
func (r runtime) planResolveOverlap(_ context.Context, base agentcontract.Envelope, raw []byte, digest string, _ Authority, _ agentcontract.ContractOperation, plan *mutationPlan) (agentcontract.Envelope, error, bool) {
	var in resolveOverlapMutationInput
	if err := decodeOperationInput(raw, &in); err != nil {
		return base, err, true
	}
	plan.declareInitialDurability(in.ResolutionKind != "depends_on")
	plan.scope["work_ids"] = []string{in.FromWorkID, in.ToWorkID}
	plan.scope["resolution_kind"] = in.ResolutionKind
	plan.scope["from_work_id"] = in.FromWorkID
	plan.scope["to_work_id"] = in.ToWorkID
	plan.scope["product_id"] = r.Envelope.SelectedProductID
	plan.intents = []agentcontract.NextIntent{{Tool: "concord_work_trace", Operation: "continuity", QueryID: "C19.Continuity", ReasonCode: "inspect_overlap_resolution", RequiredFields: []string{"work_id"}}}
	plan.effect = func(ctx context.Context, tx *store.Transaction, grant Authority) (json.RawMessage, []string, []agentcontract.ChangedRef, error) {
		request := store.WorkflowDomainOverlapResolutionRequest{EventID: digest + ":overlap", FromWorkID: in.FromWorkID, ToWorkID: in.ToWorkID, FromExpectedVersion: in.FromExpectedVersion, ToExpectedVersion: in.ToExpectedVersion, FromContractVersion: in.FromContractVersion, ToContractVersion: in.ToContractVersion, ResolutionKind: in.ResolutionKind, Reason: in.Reason, Actor: grant.PrincipalRef, OccurredAt: r.Authority.now()}
		result, err := store.ResolveWorkflowDomainOverlapTx(ctx, tx, request)
		if err != nil {
			return nil, nil, nil, err
		}
		changed := []agentcontract.ChangedRef{{EntityKind: "work_item", ID: in.FromWorkID, Version: strconv.FormatInt(in.FromExpectedVersion+1, 10)}, {EntityKind: "work_item", ID: in.ToWorkID, Version: strconv.FormatInt(in.ToExpectedVersion+1, 10)}}
		return mutationPayload(changed, plan.intents), result.EventIDs, changed, nil
	}
	return agentcontract.Envelope{}, nil, false
}

// mutate dispatches one mutation operation to its handler. A handler answers
// with a terminal envelope when the call is refused or fails before the
// transaction (handled true), or populates the plan and lets the common tail
// execute it (handled false). This is the dispatch shape (runtime).read
// established: arms delegate rather than inline.
func (r runtime) mutate(ctx context.Context, base agentcontract.Envelope, raw []byte, grant Authority, op agentcontract.ContractOperation) (agentcontract.Envelope, error) {
	if op.ID == "concord_work_transition.workflow_action" {
		return r.mutateWorkflowAction(ctx, base, raw, grant, op)
	}
	digest := mutationDigest(r.Tool, r.Operation, r.Envelope, raw)
	plan := newMutationPlan(r.Envelope)
	var answer agentcontract.Envelope
	var err error
	var handled bool
	switch op.ID {
	case "concord_work_define.capture":
		answer, err, handled = r.planCapture(ctx, base, raw, digest, grant, op, plan)
	case "concord_work_define.revise_intent":
		answer, err, handled = r.planReviseIntent(ctx, base, raw, digest, grant, op, plan)
	case "concord_work_transition.lifecycle":
		answer, err, handled = r.planLifecycle(ctx, base, raw, digest, grant, op, plan)
	case "concord_work_transition.remove":
		answer, err, handled = r.planWorkRemoval(ctx, base, raw, digest, grant, op, plan)
	case "concord_work_define.issue_link_record":
		answer, err, handled = r.planIssueLinkRecord(ctx, base, raw, digest, grant, op, plan)
	case "concord_work_transition.project_handoff_record":
		answer, err, handled = r.planProjectHandoffRecord(ctx, base, raw, digest, grant, op, plan)
	case "concord_work_transition.project_handoff_consume":
		answer, err, handled = r.planProjectHandoffConsume(ctx, base, raw, digest, grant, op, plan)
	case "concord_work_relate.set_memberships":
		answer, err, handled = r.planSetMemberships(ctx, base, raw, digest, grant, op, plan)
	case "concord_work_relate.resolve_overlap":
		answer, err, handled = r.planResolveOverlap(ctx, base, raw, digest, grant, op, plan)
	default:
		return coreError(base, "invalid_input", "mutation operation is not implemented", "contact_operator", false), nil
	}
	if handled {
		return answer, err
	}
	if plan.effect == nil {
		return coreError(base, "invalid_input", "mutation input is not executable", "contact_operator", false), nil
	}
	preflightProducts, preflightErr := r.deriveMutationProducts(ctx, plan.scope)
	if preflightErr != nil {
		return failureEnvelope(base, preflightErr), nil
	}
	plan.scope["product_ids"] = preflightProducts
	base.EvidenceRefs = append([]agentcontract.EvidenceRef{}, plan.evidenceRefs...)
	return r.executeMutation(ctx, base, raw, digest, plan.scope, plan.plannedInitialDurability, plan.intents, plan.effect)
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

func deriveMutationProductsTx(ctx context.Context, tx *store.Transaction, scope map[string]any) ([]string, error) {
	products := map[string]bool{}
	if ids, ok := scope["work_ids"].([]string); ok {
		byWork, err := store.ProductsForWorkIDsTx(ctx, tx, ids)
		if err != nil {
			return nil, err
		}
		for _, values := range byWork {
			for _, product := range values {
				products[product] = true
			}
		}
	}
	if ids, ok := scope["project_ids"].([]string); ok {
		byProject, err := store.ProductsForProjectIDsTx(ctx, tx, ids)
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

type storeMembership struct {
	ProjectID string `json:"project_id"`
	Role      string `json:"role"`
}

func mutationDigest(tool, operation string, env CallEnvelope, raw []byte) string {
	return store.AgentMutationDigest(tool, operation, env.SelectedProductID, env.AmbientProjectID, raw)
}

func mutationPayload(changed []agentcontract.ChangedRef, intents []agentcontract.NextIntent) json.RawMessage {
	if changed == nil {
		changed = []agentcontract.ChangedRef{}
	}
	if intents == nil {
		intents = []agentcontract.NextIntent{}
	}
	b, _ := json.Marshal(map[string]any{"changed_refs": mutationResultChangedRefs(changed), "next_valid_intents": mutationResultIntents(intents)})
	return b
}

func nextIntentsFromPin(pin store.WorkPin) []agentcontract.NextIntent {
	intents := make([]agentcontract.NextIntent, 0, len(pin.NextValidIntents))
	for _, intent := range pin.NextValidIntents {
		intents = append(intents, agentcontract.NextIntent{Tool: intent.Tool, Operation: intent.Operation, ActionID: intent.ActionID, ReasonCode: "declared_step_action", RequiredFields: append([]string(nil), intent.RequiredFields...), ExpectedVersion: intent.ExpectedVersion})
	}
	return intents
}

func workItemRefs(changed []agentcontract.ChangedRef) []agentcontract.ChangedRef {
	refs := make([]agentcontract.ChangedRef, 0, len(changed))
	seen := make(map[string]struct{}, len(changed))
	for _, ref := range changed {
		if ref.EntityKind != "work_item" || ref.ID == "" {
			continue
		}
		if _, ok := seen[ref.ID]; ok {
			continue
		}
		seen[ref.ID] = struct{}{}
		refs = append(refs, ref)
	}
	return refs
}

func enrichMutationPayload(payload json.RawMessage, pins []store.WorkPin) (json.RawMessage, []agentcontract.NextIntent, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil || object == nil {
		return nil, nil, fmt.Errorf("mutation result is not an object: %w", err)
	}
	if len(pins) == 0 {
		delete(object, "work_pins")
		enriched, err := json.Marshal(object)
		return enriched, nil, err
	}
	encodedPins, err := json.Marshal(pins)
	if err != nil {
		return nil, nil, err
	}
	object["work_pins"] = encodedPins
	intents := nextIntentsFromPin(pins[0])
	encodedIntents, err := json.Marshal(mutationResultIntents(intents))
	if err != nil {
		return nil, nil, err
	}
	object["next_valid_intents"] = encodedIntents
	enriched, err := json.Marshal(object)
	return enriched, intents, err
}

func (r runtime) enrichMutationPayloadTx(ctx context.Context, tx *store.Transaction, payload json.RawMessage, changed []agentcontract.ChangedRef, ownEventIDs ...string) (json.RawMessage, []agentcontract.NextIntent, []string, error) {
	refs := workItemRefs(changed)
	if len(refs) == 0 {
		return payload, nil, nil, nil
	}
	pins := make([]store.WorkPin, 0, len(refs))
	var pinless []string
	for _, ref := range refs {
		pin, available, err := store.ReadMutationWorkPinTx(ctx, tx, ref.ID, ownEventIDs)
		if err != nil {
			return nil, nil, nil, err
		}
		version, _ := strconv.ParseInt(ref.Version, 10, 64)
		if pin.Version != version {
			return nil, nil, nil, fmt.Errorf("work pin version %d does not match changed ref %s", pin.Version, ref.Version)
		}
		if !available {
			pinless = append(pinless, ref.ID)
			continue
		}
		pins = append(pins, pin)
	}
	enriched, intents, err := enrichMutationPayload(payload, pins)
	return enriched, intents, pinless, err
}

func pinlessMutationWorkIDs(changed []agentcontract.ChangedRef, pins []store.WorkPin) []string {
	missing := []string{}
	for _, ref := range workItemRefs(changed) {
		found := false
		for _, pin := range pins {
			found = found || pin.WorkID == ref.ID
		}
		if !found {
			missing = append(missing, ref.ID)
		}
	}
	return missing
}

func mutationPinOmissions(pinless []string) []agentcontract.Notice {
	if len(pinless) == 0 {
		return nil
	}
	ids := map[string]struct{}{}
	for _, id := range pinless {
		ids[id] = struct{}{}
	}
	return []agentcontract.Notice{{Kind: "work_pin_unavailable", Count: int64(len(ids))}}
}

func validateMutationPins(changed []agentcontract.ChangedRef, pins []store.WorkPin, allowedPinless []string, omissions []agentcontract.Notice) error {
	allowed := map[string]bool{}
	for _, id := range allowedPinless {
		allowed[id] = true
	}
	missing := pinlessMutationWorkIDs(changed, pins)
	if len(missing) != len(allowed) {
		return fmt.Errorf("work pin omissions do not match the allowed pinless work items")
	}
	for _, id := range missing {
		if !allowed[id] {
			return fmt.Errorf("work pin is missing for changed ref %s", id)
		}
	}
	noticeCount := 0
	for _, notice := range omissions {
		if notice.Kind == "work_pin_unavailable" {
			noticeCount++
			if notice.Count != int64(len(missing)) || notice.SourceID != "" || len(notice.Details) != 0 {
				return fmt.Errorf("work pin omission does not match the pinless work items")
			}
		}
	}
	if (len(missing) == 0 && noticeCount != 0) || (len(missing) > 0 && noticeCount != 1) {
		return fmt.Errorf("work pin omissions require one notice per pinless result")
	}
	for _, ref := range changed {
		if ref.EntityKind != "work_item" {
			continue
		}
		want, err := strconv.ParseInt(ref.Version, 10, 64)
		present, matching := false, false
		for _, pin := range pins {
			if pin.WorkID == ref.ID {
				present = true
				matching = matching || (err == nil && pin.Version == want)
			}
		}
		if !present {
			continue
		}
		if !matching {
			return fmt.Errorf("work pin does not match changed ref %s at version %s", ref.ID, ref.Version)
		}
	}
	return nil
}

type mutationResultChangedRef struct {
	EntityKind string `json:"entity_kind"`
	ID         string `json:"id"`
	Version    int64  `json:"version"`
}

func mutationResultChangedRefs(changed []agentcontract.ChangedRef) []mutationResultChangedRef {
	result := make([]mutationResultChangedRef, 0, len(changed))
	for _, ref := range changed {
		version, _ := strconv.ParseInt(ref.Version, 10, 64)
		result = append(result, mutationResultChangedRef{EntityKind: ref.EntityKind, ID: ref.ID, Version: version})
	}
	return result
}

type mutationResultIntent struct {
	Tool            string   `json:"tool"`
	Operation       string   `json:"operation"`
	ReasonCode      string   `json:"reason_code"`
	ActionID        string   `json:"action_id,omitempty"`
	RequiredFields  []string `json:"required_fields,omitempty"`
	ExpectedVersion int64    `json:"expected_version,omitempty"`
}

func mutationResultIntents(intents []agentcontract.NextIntent) []mutationResultIntent {
	result := make([]mutationResultIntent, 0, len(intents))
	for _, intent := range intents {
		result = append(result, mutationResultIntent{Tool: intent.Tool, Operation: intent.Operation, ReasonCode: intent.ReasonCode, ActionID: intent.ActionID, RequiredFields: intent.RequiredFields, ExpectedVersion: intent.ExpectedVersion})
	}
	return result
}
func evidenceLocators(refs []agentcontract.EvidenceRef) []string {
	out := make([]string, len(refs))
	for i, ref := range refs {
		out[i] = ref.Locator
	}
	return out
}

// evidenceKinds extracts each entry's evidence kind in submission order, so
// the store can bind one durable event per (locator, kind) pair instead of
// reading a single kind out of the payload fields. An entry without a kind
// stays empty and takes the payload's evidence_kind default in the store.
func evidenceKinds(refs []agentcontract.EvidenceRef) []string {
	out := make([]string, len(refs))
	for i, ref := range refs {
		out[i] = ref.Kind
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

func currentLifecycle(ctx context.Context, tx *store.Transaction, id string) (string, error) {
	return store.WorkLifecycleTx(ctx, tx, id)
}

// mutationResult is the sole producer for inline and replayed mutation success.
// It validates the exact tool/operation result before the envelope can cross the
// agent boundary, including the canonical envelope cap.
func (r runtime) mutationResult(base agentcontract.Envelope, payload json.RawMessage, changed []agentcontract.ChangedRef, intents []agentcontract.NextIntent, allowedPinless ...string) agentcontract.Envelope {
	_, err := r.validateMutationDocument(payload, changed, allowedPinless, base.Omissions)
	if err != nil {
		return coreError(base, "malformed_response", fmt.Sprintf("mutation result failed closed-schema validation: %v", err), "contact_operator", false)
	}
	response := agentcontract.NewOKMutation(base, payload, changed, intents)
	if err := response.Validate(); err != nil {
		return coreError(base, "malformed_response", fmt.Sprintf("mutation result envelope is invalid: %v", err), "contact_operator", false)
	}
	type envelopeWire agentcontract.Envelope
	encoded, err := json.Marshal(envelopeWire(response))
	if err != nil {
		return coreError(base, "malformed_response", fmt.Sprintf("mutation result envelope cannot be encoded: %v", err), "contact_operator", false)
	}
	if len(encoded) > agentcontract.MaxResultEnvelopeBytes {
		if mutationProjectionSupported(r.Tool, r.Operation) && r.mutationIdempotencyKey != "" {
			return r.projectMutationResult(base, payload, changed)
		}
		return coreError(base, "limit_exceeded", fmt.Sprintf("mutation result envelope exceeds %d bytes; this operation has no result limit to reduce; contact the operator for producer repair", agentcontract.MaxResultEnvelopeBytes), "contact_operator", false)
	}
	return response
}

var errMutationDurabilityRequired = errors.New("mutation requires a durable transaction")

func (r runtime) executeAdmittedMutation(ctx *context.Context, durable *bool, mutation func(*store.Transaction) error) error {
	transact := r.Store.Transact
	if *durable {
		transact = r.Store.TransactDurable
	}
	err := transact(*ctx, mutation)
	if err == errMutationDurabilityRequired {
		*durable = true
		transact = r.Store.TransactDurable
		err = transact(*ctx, mutation)
	}
	return err
}

func (r runtime) executeMutation(ctx context.Context, base agentcontract.Envelope, raw []byte, digest string, scope map[string]any, plannedInitialDurability *bool, intents []agentcontract.NextIntent, effect mutationEffect) (agentcontract.Envelope, error) {
	// Durability resolves before any effect: the operation's declaration is
	// the baseline, a planner fact replaces it, and a missing or unknown
	// declaration is an invariant failure, never an ordinary write.
	contractOp, registered := agentcontract.ValidateContractOperation(r.Tool, r.Operation)
	if !registered {
		return agentcontract.Envelope{}, newRuntimeFailure("invariant_violation", fmt.Sprintf("mutation dispatch reached unregistered operation %s.%s", r.Tool, r.Operation), "contact_operator", false)
	}
	declaredDurability, durabilityErr := mutationDurabilityFor(contractOp)
	if durabilityErr != nil {
		return agentcontract.Envelope{}, newRuntimeFailure("invariant_violation", durabilityErr.Error(), "contact_operator", false)
	}
	durable := declaredDurability.initial
	if plannedInitialDurability != nil {
		durable = *plannedInitialDurability
	}
	// The host probes before this mutation's transaction opens (CD-0195 D2);
	// the authorization inside matches the probed host with SQL only.
	host, hostErr := r.probeInvocationHost(ctx)
	if hostErr != nil {
		return agentcontract.Envelope{}, hostErr
	}
	// Read current law before opening the transaction (CD-0195 D2). Consumers
	// requiring verified law reject a missing proof; other mutations need none.
	for _, workID := range mutationScopeWorkIDs(scope) {
		verified, err := r.Store.PrepareWorkLawProof(ctx, workID)
		if err == nil {
			ctx = verified
		}
	}
	execution := mutationExecution{runtime: r, base: base, raw: raw, digest: digest, scope: scope, intents: intents, effect: effect, host: host, contractOp: contractOp}
	mutation := func(tx *store.Transaction) error {
		return execution.executeTx(ctx, tx, durable)
	}
	err := r.executeAdmittedMutation(&ctx, &durable, mutation)
	base, response := execution.base, execution.response
	resultRejected := execution.resultRejected
	if err != nil {
		if resultRejected {
			return response, nil
		}
		return failureEnvelope(base, err), nil
	}
	return response, nil
}

type mutationExecution struct {
	runtime
	base           agentcontract.Envelope
	raw            []byte
	digest         string
	scope          map[string]any
	intents        []agentcontract.NextIntent
	effect         mutationEffect
	host           store.ResolvedProjectHost
	contractOp     agentcontract.ContractOperation
	response       agentcontract.Envelope
	resultRejected bool
}

func (m *mutationExecution) executeTx(ctx context.Context, tx *store.Transaction, durable bool) error {
	r, raw, digest, scope := m.runtime, m.raw, m.digest, m.scope
	inv := envelopeInvocation(r.Envelope, m.contractOp.Capability, r.Operation)
	if inv.HostAssertionDigest == "" {
		inv.HostAssertionDigest = digest
	}
	grant, err := r.Authority.AuthorizeTx(ctx, tx, m.host, inv)
	if err != nil {
		return err
	}
	crossProduct, err := r.deriveAuthorizedProductsTx(ctx, tx, scope, grant)
	if err != nil {
		return err
	}
	if crossProduct && !durable {
		// SQL-only scope discovery runs before any effect.
		// Reopen under FULL and revalidate scope before issuing authority.
		return errMutationDurabilityRequired
	}
	key := idempotencyKey(raw)
	prior, found, err := store.LookupMutationIdempotencyTx(ctx, tx, store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: r.Tool, OperationKind: r.Operation, IdempotencyKey: key})
	if err != nil {
		return err
	}
	replay := false
	if found {
		if prior.CanonicalDigest != digest {
			return storeIdempotencyConflict(r.Operation, key)
		}
		if r.Operation != "project_handoff_consume" {
			return m.replayTx(ctx, tx, prior, grant, key)
		}
		// project_handoff_consume replays state-driven:
		// the cached payload is stale when the consume bind names another
		// receiving session or the active contract moved on, so the
		// effect below re-reads the recorded state and resolves or
		// refuses from it. The key stays recorded, so the
		// tail touches instead of inserting.
		replay = true
	}
	if err := m.admitTx(ctx, tx, inv, grant); err != nil {
		return err
	}
	return m.applyTx(ctx, tx, grant, key, replay, durable)
}

func (m *mutationExecution) replayTx(ctx context.Context, tx *store.Transaction, prior store.MutationIdempotencyRecord, grant Authority, key string) error {
	r := m.runtime
	var replayErr error
	m.response, m.resultRejected, replayErr = r.replayCachedMutationTx(ctx, tx, m.base, prior, grant, store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: r.Tool, OperationKind: r.Operation, IdempotencyKey: key})
	return replayErr
}

func (m *mutationExecution) admitTx(ctx context.Context, tx *store.Transaction, inv Invocation, grant Authority) error {
	r, base := m.runtime, m.base
	if grant.MainWorktree && m.contractOp.ID == "concord_work_transition.remove" {
		if grant.ScopeVersion != r.Envelope.ScopeVersion {
			return newRuntimeFailure("stale_context", "shelving scope changed after planning", "refresh_context", false)
		}
	}
	// CD-0038 D3: the seconds ceiling is admitted after the idempotency
	// lookup above, for the same reason the workflow-action path states.
	// A refused request records no idempotency effect, so the same key may
	// be reused with a lower budget.
	if r.Budget.CeilingRefused {
		m.response = r.budgetRefusal(base, fmt.Sprintf("requested_budget_seconds %d exceeds supported %d", r.Budget.RequestedSeconds, r.Budget.SupportedSeconds))
		m.resultRejected = true
		return errors.New("budget admission refused")
	}
	if _, err := r.Authority.AuthorizeTx(ctx, tx, m.host, inv); err != nil {
		return err
	}
	return nil
}

func (m *mutationExecution) applyTx(ctx context.Context, tx *store.Transaction, grant Authority, key string, replay, durable bool) error {
	r, scope, base := m.runtime, m.scope, m.base
	payload, eventIDs, changed, err := m.effect(ctx, tx, grant)
	if err != nil {
		return err
	}
	payload, derivedIntents, pinless, err := r.enrichMutationPayloadTx(ctx, tx, payload, changed, eventIDs...)
	if err != nil {
		return err
	}
	if derivedIntents != nil {
		m.intents = derivedIntents
	}
	base.Omissions = append(base.Omissions, mutationPinOmissions(pinless)...)
	base.ResolvedScope = scopeFromMap(scope)
	if base.ResolvedScope == nil {
		base.ResolvedScope = &agentcontract.Scope{ProductID: r.Envelope.SelectedProductID, ProjectIDs: []string{r.Envelope.AmbientProjectID}, ScopeVersion: r.Envelope.ScopeVersion}
	}
	if replay {
		base.Replayed = true
	}
	m.base = base
	m.response = r.mutationResult(base, payload, changed, m.intents, pinless...)
	if m.response.Outcome == agentcontract.OutcomeError {
		m.resultRejected = true
		return errors.New("mutation result rejected")
	}
	if replay {
		return store.TouchMutationIdempotencyTx(ctx, tx, store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: r.Tool, OperationKind: r.Operation, IdempotencyKey: key}, r.Authority.now())
	}
	// Retain the list the response reported: an effect that changed no
	// auxiliary reference commits an explicit empty list, never null.
	changedJSON, _ := json.Marshal(*m.response.ChangedRefs)
	authorizedSnapshot := mutationResponseScope(scope, agentcontract.NewOKMutation(base, payload, changed, m.intents), pinless)
	authorizedSnapshot["durable_commit"] = durable
	authorizedScope, _ := json.Marshal(authorizedSnapshot)
	insert := store.MutationIdempotencyInsert{Key: store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: r.Tool, OperationKind: r.Operation, IdempotencyKey: key}, CanonicalDigest: m.digest, OperationID: "mutation-" + m.digest[7:31], ResultEventIDs: marshalEventIDs(eventIDs), ResultPayload: string(payload), ChangedRefs: string(changedJSON), AuthorizedScopeSnapshot: string(authorizedScope), ObservedAt: r.Authority.now()}
	return store.InsertMutationIdempotencyTx(ctx, tx, insert)
}

// deriveAuthorizedProductsTx derives the mutation's Product scope inside the
// open transaction, refuses a scope the grant does not cover, and reports
// whether the mutation crosses Products and requires a durable transaction.
func (r runtime) deriveAuthorizedProductsTx(ctx context.Context, tx *store.Transaction, scope map[string]any, grant Authority) (bool, error) {
	derivedProducts, err := deriveMutationProductsTx(ctx, tx, scope)
	if err != nil {
		return false, err
	}
	if expected, ok := scope["product_ids"].([]string); ok && !slices.Equal(expected, derivedProducts) {
		return false, newRuntimeFailure("version_conflict", "derived Product scope changed after authorization preflight", "reread_entities", false)
	}
	crossProduct := false
	for _, product := range derivedProducts {
		if !slices.Contains(grant.ProductScope, product) {
			return false, newRuntimeFailure("unauthorized", fmt.Sprintf("mutation work Product %s is outside grant Product scope %v", product, grant.ProductScope), "contact_operator", false)
		}
		if r.Envelope.SelectedProductID != "" && product != r.Envelope.SelectedProductID {
			crossProduct = true
			if !containsCapability(grant.Capabilities, agentcontract.Capability("cross_scope")) {
				return false, newRuntimeFailure("unauthorized", "cross-Product mutation requires cross_scope capability", "contact_operator", false)
			}
		}
	}
	scope["product_ids"] = derivedProducts
	return crossProduct, nil
}

// replayCachedMutationTx answers a same-key, same-digest retry from the
// recorded result without applying the effect again. The bool reports that the
// replayed result is an error envelope the caller must return as the response.
func (r runtime) replayCachedMutationTx(ctx context.Context, tx *store.Transaction, base agentcontract.Envelope, prior store.MutationIdempotencyRecord, grant Authority, key store.MutationIdempotencyKey) (agentcontract.Envelope, bool, error) {
	scope, err := authorizedScopeFromSnapshot(prior.AuthorizedScopeSnapshot)
	if err != nil {
		return agentcontract.Envelope{}, false, err
	}
	if !scopeWithinAuthority(scope, grant) {
		return agentcontract.Envelope{}, false, newRuntimeFailure("unauthorized", "original mutation scope is no longer authorized by the current grant", "contact_operator", false)
	}
	base.ResolvedScope = scopeFromMap(scope)
	response := r.recordedMutationResult(base, prior)
	if response.Outcome == agentcontract.OutcomeError {
		return response, true, errors.New("mutation result rejected")
	}
	return response, false, store.TouchMutationIdempotencyTx(ctx, tx, key, r.Authority.now())
}

func mutationScopeWorkIDs(scope map[string]any) []string {
	values, _ := scope["work_ids"].([]string)
	out := make([]string, 0, len(values))
	for _, value := range normalizeScopeList(values) {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

// probeInvocationHost resolves the invoking git identity with no transaction
// open (CD-0195 D2). Every mutation that opens a transaction runs it before
// the transaction opens, and each authorization inside matches the probed
// host against Project locators with SQL only.
func (r runtime) probeInvocationHost(ctx context.Context) (store.ResolvedProjectHost, error) {
	return r.Authority.ProbeProjectHost(ctx, r.Envelope.Directory, r.Envelope.Worktree)
}

func boundedMutationScope(scope map[string]any) map[string]any {
	out := make(map[string]any, len(scope))
	for key, value := range scope {
		if key != "governing_requirements" && key != "product_id" && key != "product_ids" && key != "project_ids" && key != "work_ids" && key != "failed_attempt_id" && key != "held_attempt_holder" && key != "scope_version" && key != "project_id" && key != "role" && key != "client_ref" && key != "policy_version" && key != "capabilities" && key != "product_scope" && key != "project_scope" && key != "agent_scope" && key != "release_tag" && key != "pull_requests" {
			continue
		}
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
