package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
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
	Title           string   `json:"title"`
	ValueStatement  string   `json:"value_statement"`
	Kind            string   `json:"kind"`
	ProjectIDs      []string `json:"project_ids"`
	Priority        int64    `json:"priority"`
	Urgency         string   `json:"urgency"`
	Tags            []string `json:"tags"`
	WorkflowTypeRef string   `json:"workflow_type_ref"`
	ExternalRef     string   `json:"external_ref"`
	IdempotencyKey  string   `json:"idempotency_key"`
	// GoverningRequirements enumerates the scope-level obligations this capture
	// carries (CD-0035 D3/D4). It confers no authority: the core refuses when it
	// fails to cover the requirements the target scope declares, and the caller
	// cannot assert satisfaction or mark a requirement inapplicable.
	GoverningRequirements []string       `json:"governing_requirements"`
	Approval              *approvalInput `json:"approval"`
}
type reviseMutationInput struct {
	WorkID          string   `json:"work_id"`
	ExpectedVersion int64    `json:"expected_version"`
	Title           string   `json:"title"`
	ValueStatement  string   `json:"value_statement"`
	Kind            string   `json:"kind"`
	Priority        int64    `json:"priority"`
	Urgency         string   `json:"urgency"`
	Tags            []string `json:"tags"`
	WorkflowTypeRef string   `json:"workflow_type_ref"`
	Reason          string   `json:"reason"`
	IdempotencyKey  string   `json:"idempotency_key"`
}
type initiativeCreateMutationInput struct {
	Title          string   `json:"title"`
	ValueStatement string   `json:"value_statement"`
	ProjectIDs     []string `json:"project_ids"`
	Priority       int64    `json:"priority"`
	Urgency        string   `json:"urgency"`
	Tags           []string `json:"tags"`
	ExternalRef    string   `json:"external_ref"`
	IdempotencyKey string   `json:"idempotency_key"`
}
type initiativeEntryMutationInput struct {
	InitiativeWorkID string `json:"initiative_work_id"`
	ChildWorkID      string `json:"child_work_id"`
	ExpectedVersion  int64  `json:"expected_version"`
	Position         int64  `json:"position"`
	Required         *bool  `json:"required"`
	IdempotencyKey   string `json:"idempotency_key"`
}
type initiativeRemoveEntryMutationInput struct {
	InitiativeWorkID string `json:"initiative_work_id"`
	ChildWorkID      string `json:"child_work_id"`
	ExpectedVersion  int64  `json:"expected_version"`
	IdempotencyKey   string `json:"idempotency_key"`
}
type initiativeNarrativeMutationInput struct {
	InitiativeWorkID string `json:"initiative_work_id"`
	ExpectedVersion  int64  `json:"expected_version"`
	Narrative        string `json:"narrative"`
	Reason           string `json:"reason"`
	IdempotencyKey   string `json:"idempotency_key"`
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
type worktreeClaimInput struct {
	WorkID          string `json:"work_id"`
	ProjectID       string `json:"project_id"`
	Branch          string `json:"branch"`
	BaseSHA         string `json:"base_sha"`
	Path            string `json:"path"`
	ExpectedVersion int64  `json:"expected_version"`
	IdempotencyKey  string `json:"idempotency_key"`
}
type worktreeReclaimInput struct {
	WorkID          string `json:"work_id"`
	ProjectID       string `json:"project_id"`
	DefaultRef      string `json:"default_ref"`
	ExpectedVersion int64  `json:"expected_version"`
	IdempotencyKey  string `json:"idempotency_key"`
}
type researchRevisionInput struct {
	Question string `json:"question"`
	ScopeIn  any    `json:"scope_in"`
	ScopeOut any    `json:"scope_out"`
	DoneWhen any    `json:"done_when"`
	Method   string `json:"method"`
}
type researchScopesInput struct {
	Mode       string   `json:"mode"`
	ProductIDs []string `json:"product_ids"`
	ProjectIDs []string `json:"project_ids"`
	DomainIDs  []string `json:"domain_ids"`
	TagIDs     []string `json:"tag_ids"`
}
type researchFindingInput struct {
	FindingID  string               `json:"finding_id"`
	Kind       string               `json:"kind"`
	Statement  string               `json:"statement"`
	Confidence string               `json:"confidence"`
	Freshness  string               `json:"freshness"`
	Status     string               `json:"status"`
	Scopes     *researchScopesInput `json:"scopes"`
}
type researchSourceInput struct {
	SourceID          string `json:"source_id"`
	Kind              string `json:"kind"`
	Locator           string `json:"locator"`
	Title             string `json:"title"`
	PublisherOrAuthor string `json:"publisher_or_author"`
	PublishedAt       string `json:"published_at"`
	AccessedAt        string `json:"accessed_at"`
}
type lessonPublishInput struct {
	WorkID         string             `json:"work_id"`
	LessonID       string             `json:"lesson_id"`
	Title          string             `json:"title"`
	Summary        string             `json:"summary"`
	Content        string             `json:"content"`
	Tags           []string           `json:"tags"`
	Scopes         *lessonScopesInput `json:"scopes"`
	Evidence       []string           `json:"evidence"`
	IdempotencyKey string             `json:"idempotency_key"`
	Approval       *approvalInput     `json:"approval"`
}
type lessonScopesInput struct {
	Mode       string   `json:"mode"`
	ProductIDs []string `json:"product_ids"`
	ProjectIDs []string `json:"project_ids"`
	TagIDs     []string `json:"tag_ids"`
}
type resourceClaimInput struct {
	WorkID          string `json:"work_id"`
	ResourceKey     string `json:"resource_key"`
	Reason          string `json:"reason"`
	ExpectedVersion int64  `json:"expected_version"`
	IdempotencyKey  string `json:"idempotency_key"`
}
type resourceReleaseInput struct {
	WorkID          string `json:"work_id"`
	ResourceKey     string `json:"resource_key"`
	ExpectedVersion int64  `json:"expected_version"`
	IdempotencyKey  string `json:"idempotency_key"`
}
type messageSendInput struct {
	WorkID          string `json:"work_id"`
	RecipientWorkID string `json:"recipient_work_id"`
	Broadcast       bool   `json:"broadcast"`
	Body            string `json:"body"`
	ExpectedVersion int64  `json:"expected_version"`
	IdempotencyKey  string `json:"idempotency_key"`
}
type messageWithdrawInput struct {
	WorkID          string `json:"work_id"`
	MessageID       string `json:"message_id"`
	ExpectedVersion int64  `json:"expected_version"`
	IdempotencyKey  string `json:"idempotency_key"`
}
type observationRecordInput struct {
	WorkID         string   `json:"work_id"`
	ObservationID  string   `json:"observation_id"`
	Statement      string   `json:"statement"`
	Refs           []string `json:"refs"`
	Tags           []string `json:"tags"`
	IdempotencyKey string   `json:"idempotency_key"`
	// External is the CD-0040 D10 variant: a capture of, or verification
	// against, state outside Concord. Plain observations stay plain statements
	// and satisfy no evidence or gate; the external variant is also
	// non-authoritative and can only supply or withhold a precondition.
	External *observationExternalInput `json:"external"`
}

// CD-0068: the Domain-anchored twin of observationRecordInput. It carries no
// external variant — CD-0040 attaches external capture to work, and CD-0068
// widens only the anchor, not the observation's kinds.
type domainObservationRecordInput struct {
	ProductID      string   `json:"product_id"`
	DomainID       string   `json:"domain_id"`
	Statement      string   `json:"statement"`
	Refs           []string `json:"refs"`
	Tags           []string `json:"tags"`
	IdempotencyKey string   `json:"idempotency_key"`
}

type domainObservationDismissInput struct {
	ProductID      string         `json:"product_id"`
	DomainID       string         `json:"domain_id"`
	ObservationID  string         `json:"observation_id"`
	IdempotencyKey string         `json:"idempotency_key"`
	Approval       *approvalInput `json:"approval"`
}

type observationExternalInput struct {
	Kind          string `json:"kind"`
	ObservationID string `json:"observation_id"`
	// capture fields
	SubjectKind      string                  `json:"subject_kind"`
	SubjectRef       string                  `json:"subject_ref"`
	CapturedAt       string                  `json:"captured_at"`
	SubjectDigest    string                  `json:"subject_digest"`
	ObservedUniverse *store.ObservedUniverse `json:"observed_universe"`
	// verification fields
	VerificationMethod string   `json:"verification_method"`
	VerifiedAt         string   `json:"verified_at"`
	Result             string   `json:"result"`
	CurrentDigest      string   `json:"current_digest"`
	Omissions          []string `json:"omissions"`
}
type researchBindingInput struct {
	PackID   string `json:"pack_id"`
	Revision int64  `json:"revision"`
	UseRole  string `json:"use_role"`
	Required bool   `json:"required"`
}
type actionMutationInput struct {
	WorkID                string                 `json:"work_id"`
	ExpectedVersion       int64                  `json:"expected_version"`
	ActionID              string                 `json:"action_id"`
	SelectedChoice        string                 `json:"selected_choice"`
	DecisionContextDigest string                 `json:"decision_context_digest"`
	Fields                json.RawMessage        `json:"fields"`
	IdempotencyKey        string                 `json:"idempotency_key"`
	Evidence              []EvidenceRef          `json:"evidence"`
	Approval              *approvalInput         `json:"approval"`
	ResearchBindings      []researchBindingInput `json:"research_bindings"`
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
type resolveOverlapMutationInput struct {
	FromWorkID          string         `json:"from_work_id"`
	ToWorkID            string         `json:"to_work_id"`
	FromExpectedVersion int64          `json:"from_expected_version"`
	ToExpectedVersion   int64          `json:"to_expected_version"`
	FromContractVersion int64          `json:"from_contract_version"`
	ToContractVersion   int64          `json:"to_contract_version"`
	ResolutionKind      string         `json:"resolution_kind"`
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

type mutationEffect func(context.Context, *store.Transaction, Grant) (json.RawMessage, []string, []ChangedRef, error)

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
	record, found, err := r.Store.LookupMutationIdempotency(ctx, store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: r.Tool, OperationKind: operationKind, IdempotencyKey: key})
	if err != nil {
		return Envelope{}, false, err
	}
	if !found {
		return Envelope{}, false, nil
	}
	storedDigest, opID, payload, changed, scopeJSON := record.CanonicalDigest, record.OperationID, record.ResultPayload, record.ChangedRefs, record.AuthorizedScopeSnapshot
	if r.Tool == "concord_work_compact" {
		accepted, err := r.Store.AcceptedInputsDigest(ctx, opID)
		if err != nil {
			return Envelope{}, false, err
		}
		storedDigest = accepted
	}
	if storedDigest != digest {
		return coreError(base, "idempotency_conflict", "idempotency key was reused with a different canonical request", "retry_same_request", false), true, nil
	}
	authorizedScope, scopeErr := authorizedScopeFromSnapshot(scopeJSON)
	if scopeErr != nil {
		return coreError(base, "unauthorized", "stored mutation scope is unreadable and cannot be re-authorized", "contact_operator", false), true, nil
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
		replay, replayErr := r.replayWorkflowAction(base, step)
		if replayErr != nil {
			return Envelope{}, false, replayErr
		}
		if replay.Outcome == OutcomeError {
			return replay, true, nil
		}
		if err := r.Store.TouchMutationIdempotency(ctx, store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: r.Tool, OperationKind: operationKind, IdempotencyKey: key}, r.Authority.now()); err != nil {
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
		if err := r.Store.TouchMutationIdempotency(ctx, store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: r.Tool, OperationKind: operationKind, IdempotencyKey: key}, r.Authority.now()); err != nil {
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
		if err := r.Store.TouchMutationIdempotency(ctx, store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: r.Tool, OperationKind: operationKind, IdempotencyKey: key}, r.Authority.now()); err != nil {
			return Envelope{}, false, err
		}
		return response, true, nil
	}
	if err := r.Store.TouchMutationIdempotency(ctx, store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: r.Tool, OperationKind: operationKind, IdempotencyKey: key}, r.Authority.now()); err != nil {
		return Envelope{}, false, err
	}
	ref := operationRefFromFence(step, "pending", "git_proof")
	return NewPending(base, ref, RecoveryAction{Kind: "reconcile_operation", RequiredRefs: []string{"operation_id"}}), true, nil
}

func (r runtime) replayWorkflowAction(base Envelope, step store.FenceResult) (Envelope, error) {
	state := OperationState(step.ResultKind)
	if step.ResultKind == store.ResultFailedStale {
		// failed_stale is a durable store classification, not a TS7 state.
		state = OperationFailed
	}
	ref := OperationRef{ID: step.OpID, Kind: "workflow_action", Version: strconv.FormatInt(step.AttemptEpoch, 10), State: state, CurrentStep: step.StepID, UpdatedAt: time.Now().UTC()}
	switch step.ResultKind {
	case store.ResultCompleted:
		payload := json.RawMessage(step.ResultPayload)
		if step.ContractDigest != ManifestDigest {
			return coreError(base, "manifest_mismatch", "durable workflow result manifest digest does not match the current contract", "contact_operator", false), nil
		}
		if err := ValidateOperationPayload(base.Tool, base.Operation, payload, true); err != nil {
			return coreError(base, "malformed_response", fmt.Sprintf("durable workflow result is not a valid current result: %v", err), "contact_operator", false), nil
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

// validateObservationExternalVariant checks the mode-specific shape before any
// effect exists. Field-level bounds are owned by the store validator; this
// keeps the two variants from being mixed into one call.
func validateObservationExternalVariant(in *observationExternalInput) error {
	switch in.Kind {
	case "capture":
		if in.SubjectKind == "" || in.SubjectRef == "" || in.CapturedAt == "" || in.ObservedUniverse == nil {
			return errors.New("external capture requires subject kind, subject reference, capture time, and the observed universe")
		}
		if in.VerificationMethod != "" || in.VerifiedAt != "" || in.Result != "" {
			return errors.New("external capture cannot carry verification fields")
		}
	case "verification":
		if in.VerifiedAt == "" || in.Result == "" {
			return errors.New("external verification requires the check time and result")
		}
		if in.SubjectKind != "" || in.SubjectRef != "" || in.CapturedAt != "" || in.ObservedUniverse != nil {
			return errors.New("external verification cannot carry capture fields")
		}
	default:
		return errors.New("external observation variant must be capture or verification")
	}
	if len(in.ObservationID) < 6 || len(in.ObservationID) > 32 {
		return errors.New("external observation requires its xobs: identifier")
	}
	return nil
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
	if err := decodeOperationInput(raw, &in); err != nil {
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
	if err := decodeOperationInput(raw, &in); err != nil {
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
	if err := decodeOperationInput(raw, &in); err != nil {
		return newRuntimeFailure("invalid_input", err.Error(), "reread_entities", false)
	}
	payload, err := workflowActionFields(in.Fields)
	if err != nil {
		return newRuntimeFailure("invalid_input", err.Error(), "reread_entities", false)
	}
	// An exact durable replay is allowed to bypass the now-advanced expected
	// version. The digest comparison remains strict; no authorization callback
	// or mutation is reached until the normal replay path validates the grant.
	key := in.IdempotencyKey
	prior, found, err := s.LookupMutationIdempotency(ctx, store.MutationIdempotencyKey{PrincipalRef: env.PrincipalRef, Tool: "concord_work_transition", OperationKind: "workflow_action", IdempotencyKey: key})
	if err != nil {
		return err
	}
	if found {
		if prior.CanonicalDigest != mutationDigest("concord_work_transition", "workflow_action", env, raw) {
			return store.IdempotencyConflict("workflow_action", key)
		}
		return nil
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

func (r runtime) mutateWorkflowAction(ctx context.Context, base Envelope, raw []byte, grant Grant, op ContractOperation) (Envelope, error) {
	if r.Store == nil {
		return coreError(base, "invalid_input", "workflow action requires a registered workflow authority", "contact_operator", false), nil
	}
	var in actionMutationInput
	if err := decodeOperationInput(raw, &in); err != nil {
		return coreError(base, "invalid_input", err.Error(), "reread_entities", false), nil
	}
	payload, err := workflowActionFields(in.Fields)
	if err != nil {
		return coreError(base, "invalid_input", err.Error(), "reread_entities", false), nil
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
	approvalConsequence := string(op.Consequence)
	contractVersion := int64(0)
	if in.ActionID == "confirm_premise" {
		version, err := r.Store.LatestWorkflowContractVersion(ctx, in.WorkID)
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		contractVersion = version
		if contractVersion == 0 {
			return coreError(base, "invalid_input", "confirm_premise requires an approved workflow contract", "reread_entities", false), nil
		}
		versions["contract"] = contractVersion
	}
	if in.ActionID == "supersede_contract" {
		contract, err := r.Store.ActiveWorkflowContract(ctx, in.WorkID)
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		contractVersion = contract.Version
		versions["contract"] = contractVersion
	}
	approval := ""
	if in.Approval != nil {
		approval = in.Approval.ApprovalRef
	}
	requiresApproval := action.Approval == store.ActionApprovalRequired
	if replay, handled, replayErr := r.replayMutationBeforeScope(ctx, base, raw, grant, op); replayErr != nil || handled {
		if replayErr != nil {
			return failureEnvelope(base, replayErr), nil
		}
		return replay, nil
	}
	// CD-0038 D3: budget admission for a mutation follows its idempotency
	// lookup. A replay that matches the recorded digest returns the original
	// result regardless of the ceiling, so a lowered ceiling can never strand
	// an already-committed request behind a refusal.
	if r.Budget.CeilingRefused {
		return r.budgetRefusal(base, fmt.Sprintf("requested_budget_seconds %d exceeds supported %d", r.Budget.RequestedSeconds, r.Budget.SupportedSeconds)), nil
	}
	if err := store.ValidateWorkflowOperatorSelection(ctx, r.Store, in.WorkID, in.ExpectedVersion, in.ActionID, in.SelectedChoice, in.DecisionContextDigest); err != nil {
		return failureEnvelope(base, err), nil
	}

	// CD-0059 D3: per-action capability resolution is structural. The
	// registry entry declares the capability the dispatcher must hold;
	// legacy actions that did not declare one default to work_transition
	// so pre-CD-0059 actions retain their authority contract.
	requiredCapability := Capability("work_transition")
	if action.RequiredCapability != "" {
		requiredCapability = Capability(action.RequiredCapability)
	}
	inv := Invocation{GrantToken: r.Envelope.GrantToken, ClientRef: r.Envelope.ClientRef, PrincipalRef: r.Envelope.PrincipalRef, SessionRef: r.Envelope.SessionRef, AgentRef: r.Envelope.AgentRef, Directory: r.Envelope.Directory, Worktree: r.Envelope.Worktree, ManifestDigest: r.Envelope.ManifestDigest, HostAssertionDigest: r.Envelope.HostAssertionDigest, RequiredCapability: requiredCapability, ProductID: r.Envelope.SelectedProductID, ProjectID: r.Envelope.AmbientProjectID}
	if inv.HostAssertionDigest == "" {
		inv.HostAssertionDigest = digest
	}
	if requiresApproval && approval == "" {
		spec := ApprovalChallengeSpec{OperationDigest: digest, Scope: boundedApprovalScope(scope), Versions: versions, Consequence: approvalConsequence, HostAssertionDigest: inv.HostAssertionDigest, ExpiresAt: r.Authority.now().Add(10 * time.Minute)}
		var challengeRef string
		txErr := r.Store.Transact(ctx, func(tx *store.Transaction) error {
			var err error
			challengeRef, err = r.Authority.CreateApprovalChallengeTx(ctx, tx, inv, spec)
			return err
		})
		if txErr != nil {
			return failureEnvelope(base, txErr), nil
		}
		response := coreError(base, "approval_required", "core approval is required for this workflow action", "request_approval", false)
		// CD-0037 D2: a challenge was minted, so the refusal carries the typed
		// summary derived from the same spec the challenge binds.
		response.Error.ConsequenceSummary = consequenceSummaryFor(r.Tool, r.Operation, spec)
		premiseSummary := ""
		if in.ActionID == "confirm_premise" {
			if contract, err := r.Store.ActiveWorkflowContract(ctx, in.WorkID); err == nil {
				premiseSummary = contract.Premise
			}
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
	actionRequest := store.WorkflowActionExecutionRequest{WorkID: in.WorkID, ExpectedVersion: in.ExpectedVersion, ActionID: in.ActionID, SelectedChoice: in.SelectedChoice, DecisionContextDigest: in.DecisionContextDigest, Payload: payload, EvidenceRefs: evidenceLocators(in.Evidence), Actor: store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}, ResearchBindings: researchBindingDeclarations(in.ResearchBindings), AcceptedInputsDigest: digest, IdempotencyIdentity: in.IdempotencyKey, OperationID: operationID, PrincipalRef: grant.PrincipalRef, Tool: r.Tool, IdempotencyKey: in.IdempotencyKey, RequestID: r.Envelope.RequestID, AcceptedScope: string(scopeJSON), ContractDigest: ManifestDigest, Now: r.Authority.now()}
	err = store.AuthorizeWorkflowActionAtBoundaryTx(ctx, r.Store, registry, store.WorkflowActionPreflightRequest{WorkID: in.WorkID, ExpectedVersion: in.ExpectedVersion, ActionID: in.ActionID, SelectedChoice: in.SelectedChoice, DecisionContextDigest: in.DecisionContextDigest, Payload: payload, Actor: actionRequest.Actor}, nil, time.Time{}, func(tx *store.Transaction) error {
		if _, err := r.Authority.ValidateAndConsumeGrantTx(ctx, tx, inv); err != nil {
			return err
		}
		if requiresApproval {
			verifiedOperator, _, err := r.consumeApprovalTx(ctx, tx, inv, grant, ApprovalCheck{ApprovalRef: approval, OperationDigest: digest, Scope: boundedApprovalScope(scope), Versions: versions, Consequence: approvalConsequence, ClientRef: grant.ClientRef, SessionRef: grant.SessionRef, RequireOperatorIdentity: in.ActionID == "confirm_premise"})
			if err != nil {
				return err
			}
			if in.ActionID == "confirm_premise" {
				operatorActor = &verifiedOperator
			}
		}
		return store.InsertMutationIdempotencyTx(ctx, tx, store.MutationIdempotencyInsert{Key: store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: r.Tool, OperationKind: "workflow_action", IdempotencyKey: in.IdempotencyKey}, CanonicalDigest: digest, OperationID: operationID, ResultEventIDs: "[]", ResultPayload: "{}", ChangedRefs: "[]", AuthorizedScopeSnapshot: string(scopeJSON), ObservedAt: r.Authority.now()})
	}, func(tx *store.Transaction) error {
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
		// CD-0039 D7/D8: a native report that the approved logical operation
		// did not complete successfully classifies the action partial. The
		// native steps are durable attributed facts; ok is reserved for
		// successful native predicates.
		if execution.NativeRun != nil && store.NativeRunStatusIsFailure(execution.NativeRun.Phase, execution.NativeRun.Status) {
			report := execution.NativeRun
			ref := OperationRef{ID: operationID, Kind: "workflow_action", Version: "1", State: OperationPartial, CurrentStep: report.Phase, UpdatedAt: r.Authority.now()}
			partialErr := TypedError{Kind: "operation_conflict", RetrySafe: true, RecoveryAction: RecoveryAction{Kind: "reconcile_operation"}, EffectState: EffectPartial, Message: "native authority reported the operation did not complete successfully"}
			partialErr.Details = map[string]any{
				"health_failure":   report.Phase + ":" + report.Status + " by " + report.ReportingAuthorityRef + " at " + report.AssertedAt,
				"rollback_result":  report.EvidenceRef,
				"native_run_id":    report.RunID,
				"native_phase":     report.Phase,
				"native_status":    report.Status,
				"reporting_client": report.ReportingAuthorityRef,
			}
			result = NewPartial(base, ref, []string{report.Phase}, partialErr)
			changedJSON, _ := json.Marshal(changed)
			return store.UpdateMutationResultTx(ctx, tx, store.MutationResultUpdate{Key: store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: r.Tool, OperationKind: "workflow_action", IdempotencyKey: in.IdempotencyKey}, ResultEventIDs: marshalEventIDs(execution.EventIDs), ResultPayload: "{}", ChangedRefs: string(changedJSON), ObservedAt: r.Authority.now()})
		}
		result = r.mutationResult(base, execution.Result, changed, nil)
		if result.Outcome == OutcomeError {
			resultRejected = true
			return errors.New("mutation result rejected")
		}
		changedJSON, _ := json.Marshal(changed)
		return store.UpdateMutationResultTx(ctx, tx, store.MutationResultUpdate{Key: store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: r.Tool, OperationKind: "workflow_action", IdempotencyKey: in.IdempotencyKey}, ResultEventIDs: marshalEventIDs(execution.EventIDs), ResultPayload: string(execution.Result), ChangedRefs: string(changedJSON), ObservedAt: r.Authority.now()})
	})
	if err != nil {
		if resultRejected {
			return result, nil
		}
		return failureEnvelope(base, err), nil
	}
	return result, nil
}

func (r runtime) mutate(ctx context.Context, base Envelope, raw []byte, grant Grant, op ContractOperation) (Envelope, error) {
	if op.ID == "concord_work_transition.workflow_action" {
		return r.mutateWorkflowAction(ctx, base, raw, grant, op)
	}
	digest := mutationDigest(r.Tool, r.Operation, r.Envelope, raw)
	if r.Tool == "concord_work_compact" && op.ID != "concord_work_compact.lesson_publish" {
		return r.mutateCompaction(ctx, base, raw, digest, op)
	}
	scope := map[string]any{"product_id": r.Envelope.SelectedProductID, "project_ids": []string{r.Envelope.AmbientProjectID}, "scope_version": r.Envelope.ScopeVersion}
	versions := map[string]any{}
	consequence := string(op.Consequence)
	approval := ""
	requiresApproval := op.Approval == ApprovalClass("required")
	// governingConflict names the scope requirements a capture failed to carry.
	// It is empty for every other mutation and turns the approval refusal into a
	// CD-0035 governing-law conflict carrying the operator's three resolutions.
	var governingConflict []string
	var effect mutationEffect
	var intents []NextIntent

	switch op.ID {
	case "concord_work_define.capture":
		var in captureMutationInput
		if err := decodeOperationInput(raw, &in); err != nil {
			return base, err
		}
		if in.Kind == "epic" || in.Kind == "initiative" {
			return coreError(base, "invalid_input", "capture cannot create Initiative work; use concord_work_initiative.create", "use_initiative_operation", false), nil
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
		// CD-0035 D3: a governing-law conflict is a set difference against the
		// requirements the target scope declares, never a reading of the
		// instruction text. The refusal writes nothing and returns the operator
		// the three resolutions; a reduced set proceeds only behind a core-issued
		// approval bound to this exact operation, which is CD-0006 D5's
		// legislative moment rather than an agent-side decision.
		applicable, requirementErr := r.Store.GoverningRequirementsForProjectIDs(ctx, in.ProjectIDs)
		if requirementErr != nil {
			return failureEnvelope(base, requirementErr), nil
		}
		if missing := store.MissingGoverningRequirements(applicable, in.GoverningRequirements); len(missing) > 0 {
			// The conflict is consequential, so it routes through the same
			// approval machinery as every other governed consequence. That is
			// deliberate: offering accept_scope_cut without minting the challenge
			// it resolves against would repeat the defect where a refusal named a
			// recovery the agent had nothing to act on.
			requiresApproval = true
			governingConflict = missing
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
		effect = func(ctx context.Context, tx *store.Transaction, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
			priority := in.Priority
			urgency := in.Urgency
			if urgency == "" {
				urgency = "standard"
			}
			payload, _ := json.Marshal(map[string]any{"work_kind": in.Kind, "title": in.Title, "value_statement": in.ValueStatement, "priority": priority, "urgency": urgency, "tags": in.Tags, "workflow_type_ref": in.WorkflowTypeRef, "external_ref": in.ExternalRef})
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
		if err := decodeOperationInput(raw, &in); err != nil {
			return base, err
		}
		if in.Kind == "epic" || in.Kind == "initiative" {
			return coreError(base, "invalid_input", "revise_intent cannot create Initiative work; use the dedicated Initiative operation", "use_initiative_operation", false), nil
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
		effect = func(ctx context.Context, tx *store.Transaction, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
			definition, definitionFound, existingErr := store.WorkflowInstanceDefinitionTx(ctx, tx, in.WorkID)
			if existingErr != nil {
				return nil, nil, nil, existingErr
			}
			if definitionFound && in.WorkflowTypeRef != "" && definition.DefinitionRef != in.WorkflowTypeRef {
				return nil, nil, nil, fmt.Errorf("workflow definition cannot be changed after initialization")
			}
			urgency := in.Urgency
			if urgency == "" {
				urgency = "standard"
			}
			payload, _ := json.Marshal(map[string]any{"title": in.Title, "value_statement": in.ValueStatement, "kind": in.Kind, "priority": in.Priority, "urgency": urgency, "tags": in.Tags, "workflow_type_ref": in.WorkflowTypeRef, "reason": in.Reason, "expected_version": in.ExpectedVersion, "resulting_version": in.ExpectedVersion + 1})
			result, err := store.ApplyOperationTx(ctx, tx, store.Operation{Events: []store.Event{{EventID: digest + ":revise", Kind: "work.intent_revised", SubjectType: store.SubjectWorkItem, SubjectID: in.WorkID, Actor: grant.PrincipalRef, OccurredAt: r.Authority.now(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, in.WorkID): in.ExpectedVersion}})
			if err != nil {
				return nil, nil, nil, err
			}
			if in.WorkflowTypeRef != "" && !definitionFound {
				actor := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
				if err := store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: in.WorkID, Definition: registeredDefinition, Actor: actor, Now: r.Authority.now()}); err != nil {
					return nil, nil, nil, err
				}
			}
			changedVersion := in.ExpectedVersion + 1
			if in.WorkflowTypeRef != "" && !definitionFound {
				changedVersion += 2
			}
			changed := []ChangedRef{{EntityKind: "work_item", ID: in.WorkID, Version: strconv.FormatInt(changedVersion, 10)}}
			return mutationPayload(changed, intents), result.EventIDs, changed, nil
		}
	case "concord_work_initiative.create":
		var in initiativeCreateMutationInput
		if err := decodeOperationInput(raw, &in); err != nil {
			return base, err
		}
		if len(in.ProjectIDs) == 0 {
			return coreError(base, "invalid_input", "Initiative creation requires at least one Project membership", "reread_entities", false), nil
		}
		scope["project_ids"] = in.ProjectIDs
		productsByProject, err := r.Store.ProductsForProjectIDs(ctx, in.ProjectIDs)
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		if products := uniqueProducts(productsByProject, in.ProjectIDs); len(products) != 1 {
			return coreError(base, "invariant_violation", "Initiative creation requires exactly one derived Product", "resolve_ambiguity", false), nil
		}
		workID := "initiative-" + digest[7:31]
		intents = []NextIntent{{Tool: "concord_work_browse", Operation: "list", QueryID: "PM1.Q3", ReasonCode: "inspect_created_initiative"}}
		effect = func(ctx context.Context, tx *store.Transaction, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
			if products, err := deriveInitiativeProductsTx(ctx, tx, in.ProjectIDs); err != nil {
				return nil, nil, nil, err
			} else if len(products) != 1 {
				return nil, nil, nil, newRuntimeFailure("invariant_violation", "Initiative creation requires exactly one derived Product", "resolve_ambiguity", false)
			}
			urgency := in.Urgency
			if urgency == "" {
				urgency = "standard"
			}
			payload, _ := json.Marshal(map[string]any{"work_kind": "initiative", "title": in.Title, "value_statement": in.ValueStatement, "priority": in.Priority, "urgency": urgency, "tags": in.Tags, "external_ref": in.ExternalRef})
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
			changed := []ChangedRef{{EntityKind: "work_item", ID: workID, Version: "2"}}
			return mutationPayload(changed, intents), result.EventIDs, changed, nil
		}
	case "concord_work_initiative.add_entry", "concord_work_initiative.reorder_entry", "concord_work_initiative.change_requiredness":
		var in initiativeEntryMutationInput
		if err := decodeOperationInput(raw, &in); err != nil {
			return base, err
		}
		versions["initiative"] = in.ExpectedVersion
		scope["work_ids"] = []string{in.InitiativeWorkID, in.ChildWorkID}
		kind := "initiative_entry.added"
		intentReason := "inspect_initiative_entries"
		if op.ID == "concord_work_initiative.reorder_entry" {
			kind = "initiative_entry.reordered"
			intentReason = "inspect_reordered_initiative"
		} else if op.ID == "concord_work_initiative.change_requiredness" {
			kind = "initiative_entry.requiredness_changed"
			intentReason = "inspect_initiative_requiredness"
		}
		intents = []NextIntent{{Tool: "concord_work_initiative", Operation: "entries", QueryID: "C21.InitiativeEntries", ReasonCode: intentReason}}
		required := true
		if in.Required != nil {
			required = *in.Required
		}
		effect = func(ctx context.Context, tx *store.Transaction, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
			event, err := store.InitiativeEntryEvent(digest+":entry", kind, in.InitiativeWorkID, store.InitiativeEntry{InitiativeWorkID: in.InitiativeWorkID, ChildWorkID: in.ChildWorkID, Position: in.Position, Required: required}, grant.PrincipalRef, r.Authority.now(), in.ExpectedVersion)
			if err != nil {
				return nil, nil, nil, err
			}
			result, err := store.ApplyOperationTx(ctx, tx, store.Operation{Events: []store.Event{event}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, in.InitiativeWorkID): in.ExpectedVersion}})
			if err != nil {
				return nil, nil, nil, err
			}
			changed := []ChangedRef{{EntityKind: "work_item", ID: in.InitiativeWorkID, Version: strconv.FormatInt(in.ExpectedVersion+1, 10)}}
			return mutationPayload(changed, intents), result.EventIDs, changed, nil
		}
	case "concord_work_initiative.remove_entry":
		var in initiativeRemoveEntryMutationInput
		if err := decodeOperationInput(raw, &in); err != nil {
			return base, err
		}
		versions["initiative"] = in.ExpectedVersion
		scope["work_ids"] = []string{in.InitiativeWorkID, in.ChildWorkID}
		intents = []NextIntent{{Tool: "concord_work_initiative", Operation: "entries", QueryID: "C21.InitiativeEntries", ReasonCode: "inspect_removed_initiative_entry"}}
		effect = func(ctx context.Context, tx *store.Transaction, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
			event, err := store.InitiativeEntryEvent(digest+":entry", "initiative_entry.removed", in.InitiativeWorkID, store.InitiativeEntry{InitiativeWorkID: in.InitiativeWorkID, ChildWorkID: in.ChildWorkID}, grant.PrincipalRef, r.Authority.now(), in.ExpectedVersion)
			if err != nil {
				return nil, nil, nil, err
			}
			result, err := store.ApplyOperationTx(ctx, tx, store.Operation{Events: []store.Event{event}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, in.InitiativeWorkID): in.ExpectedVersion}})
			if err != nil {
				return nil, nil, nil, err
			}
			changed := []ChangedRef{{EntityKind: "work_item", ID: in.InitiativeWorkID, Version: strconv.FormatInt(in.ExpectedVersion+1, 10)}}
			return mutationPayload(changed, intents), result.EventIDs, changed, nil
		}
	case "concord_work_initiative.revise_narrative":
		var in initiativeNarrativeMutationInput
		if err := decodeOperationInput(raw, &in); err != nil {
			return base, err
		}
		versions["initiative"] = in.ExpectedVersion
		scope["work_ids"] = []string{in.InitiativeWorkID}
		intents = []NextIntent{{Tool: "concord_work_initiative", Operation: "entries", QueryID: "C21.InitiativeEntries", ReasonCode: "refresh_initiative_context"}}
		effect = func(ctx context.Context, tx *store.Transaction, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
			event, err := store.InitiativeNarrativeEvent(digest+":narrative", in.InitiativeWorkID, in.Narrative, in.Reason, grant.PrincipalRef, r.Authority.now(), in.ExpectedVersion)
			if err != nil {
				return nil, nil, nil, err
			}
			result, err := store.ApplyOperationTx(ctx, tx, store.Operation{Events: []store.Event{event}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, in.InitiativeWorkID): in.ExpectedVersion}})
			if err != nil {
				return nil, nil, nil, err
			}
			changed := []ChangedRef{{EntityKind: "work_item", ID: in.InitiativeWorkID, Version: strconv.FormatInt(in.ExpectedVersion+1, 10)}}
			return mutationPayload(changed, intents), result.EventIDs, changed, nil
		}
	case "concord_work_transition.lifecycle":
		var in lifecycleMutationInput
		if err := decodeOperationInput(raw, &in); err != nil {
			return base, err
		}
		if in.Target == "superseded" {
			return coreError(base, "invalid_input", "superseded is only available through relate.supersede", "use_relation_operation", false), nil
		}
		// Terminal lifecycle transitions demand evidence before approval can be
		// granted. Refuse the missing-evidence case structurally with a typed
		// missing_evidence refusal whose recovery_action is provide_evidence;
		// only when evidence is present do we let the normal challenge-minting
		// path execute so the agent receives a real approval_ref to act on.
		if (in.Target == "completed" || in.Target == "cancelled") && len(in.Evidence) == 0 {
			response := coreError(base, "missing_evidence", "terminal lifecycle transition requires verification evidence", "provide_evidence", false)
			response.Error.Details = map[string]any{"operation_digest": digest, "work_id": in.WorkID, "expected_version": in.ExpectedVersion, "required_kind": "verification"}
			return response, nil
		}
		if in.Approval != nil {
			approval = in.Approval.ApprovalRef
		}
		requiresApproval = in.Target == "completed" || in.Target == "cancelled"
		versions["work"] = in.ExpectedVersion
		scope["work_ids"] = []string{in.WorkID}
		intents = []NextIntent{{Tool: "concord_work_browse", Operation: "scope", QueryID: "PM1.Q6", ReasonCode: "refresh_work_version", RequiredFields: []string{"work_id"}}}
		effect = func(ctx context.Context, tx *store.Transaction, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
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
	case "concord_work_define.research_pack_create":
		var in researchPackCreateMutation
		if err := decodeOperationInput(raw, &in); err != nil {
			return base, err
		}
		scope["work_ids"] = []string{in.OwnerWorkID}
		intents = []NextIntent{{Tool: "concord_work_define", Operation: "research_finding_record", ReasonCode: "record_findings", RequiredFields: []string{"pack_id", "expected_version"}}}
		effect = func(ctx context.Context, tx *store.Transaction, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
			pack, err := store.CreateResearchPackWithinTx(ctx, tx, store.CreateResearchPackRequest{OwnerWorkID: in.OwnerWorkID, Revision: storeResearchRevision(in.Revision), Freshness: store.ResearchFreshness(in.Freshness)})
			if err != nil {
				return nil, nil, nil, err
			}
			changed := []ChangedRef{{EntityKind: "research_pack", ID: pack.PackID, Version: strconv.FormatInt(pack.ExpectedVersion, 10)}}
			return mutationPayload(changed, intents), []string{pack.PackID}, changed, nil
		}
	case "concord_work_define.research_revision_append":
		var in researchRevisionMutation
		if err := decodeOperationInput(raw, &in); err != nil {
			return base, err
		}
		effect = func(ctx context.Context, tx *store.Transaction, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
			if _, err := store.AppendResearchRevisionWithinTx(ctx, tx, store.AppendResearchRevisionRequest{PackID: in.PackID, ExpectedVersion: in.ExpectedVersion, Revision: storeResearchRevision(in.Revision)}); err != nil {
				return nil, nil, nil, err
			}
			changed := []ChangedRef{{EntityKind: "research_pack", ID: in.PackID, Version: strconv.FormatInt(in.ExpectedVersion+1, 10)}}
			return mutationPayload(changed, intents), []string{in.PackID}, changed, nil
		}
	case "concord_work_define.research_finding_record":
		var in researchFindingMutation
		if err := decodeOperationInput(raw, &in); err != nil {
			return base, err
		}
		effect = func(ctx context.Context, tx *store.Transaction, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
			freshness := in.Finding.Freshness
			if freshness == "" {
				freshness = "current"
			}
			status := in.Finding.Status
			if status == "" {
				status = "active"
			}
			scopes := store.ResearchScopes{Mode: "home"}
			if in.Finding.Scopes != nil {
				scopes = store.ResearchScopes{Mode: in.Finding.Scopes.Mode, ProductIDs: in.Finding.Scopes.ProductIDs, ProjectIDs: in.Finding.Scopes.ProjectIDs, DomainIDs: in.Finding.Scopes.DomainIDs, TagIDs: in.Finding.Scopes.TagIDs}
			}
			finding, err := store.RecordResearchFindingWithinTxUpsert(ctx, tx, store.ResearchFindingRequest{PackID: in.PackID, ExpectedVersion: in.ExpectedVersion, Finding: store.ResearchFinding{FindingID: in.Finding.FindingID, Kind: store.ResearchFindingKind(in.Finding.Kind), Statement: in.Finding.Statement, Confidence: store.ResearchConfidence(in.Finding.Confidence), Freshness: store.ResearchFreshness(freshness), Status: store.ResearchFindingStatus(status), Scopes: scopes}})
			if err != nil {
				return nil, nil, nil, err
			}
			changed := []ChangedRef{{EntityKind: "research_pack", ID: in.PackID, Version: strconv.FormatInt(in.ExpectedVersion+1, 10)}}
			return mutationPayload(changed, intents), []string{in.PackID + ":" + finding.FindingID}, changed, nil
		}
	case "concord_work_define.research_source_record":
		var in researchSourceMutation
		if err := decodeOperationInput(raw, &in); err != nil {
			return base, err
		}
		effect = func(ctx context.Context, tx *store.Transaction, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
			source, err := store.RecordResearchSourceWithinTx(ctx, tx, store.ResearchSourceRequest{PackID: in.PackID, ExpectedVersion: in.ExpectedVersion, Source: store.ResearchSource{SourceID: in.Source.SourceID, Kind: store.ResearchSourceKind(in.Source.Kind), Locator: in.Source.Locator, Title: in.Source.Title, PublisherOrAuthor: in.Source.PublisherOrAuthor, PublishedAt: in.Source.PublishedAt, AccessedAt: in.Source.AccessedAt}})
			if err != nil {
				return nil, nil, nil, err
			}
			changed := []ChangedRef{{EntityKind: "research_pack", ID: in.PackID, Version: strconv.FormatInt(in.ExpectedVersion+1, 10)}}
			return mutationPayload(changed, intents), []string{in.PackID + ":" + source.SourceID}, changed, nil
		}
	case "concord_work_define.research_freshness_set":
		var in researchFreshnessMutation
		if err := decodeOperationInput(raw, &in); err != nil {
			return base, err
		}
		effect = func(ctx context.Context, tx *store.Transaction, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
			if err := store.SetResearchFreshnessWithinTx(ctx, tx, store.SetResearchFreshnessRequest{PackID: in.PackID, ExpectedVersion: in.ExpectedVersion, Freshness: store.ResearchFreshness(in.Freshness), Revision: in.Revision}); err != nil {
				return nil, nil, nil, err
			}
			changed := []ChangedRef{{EntityKind: "research_pack", ID: in.PackID, Version: strconv.FormatInt(in.ExpectedVersion+1, 10)}}
			return mutationPayload(changed, intents), []string{in.PackID}, changed, nil
		}
	case "concord_work_compact.lesson_publish":
		var in lessonPublishInput
		if err := decodeOperationInput(raw, &in); err != nil {
			return base, err
		}
		if in.Approval != nil {
			approval = in.Approval.ApprovalRef
		}
		requiresApproval = true
		scope["work_ids"] = []string{in.WorkID}
		workVersion, err := r.Store.WorkVersion(ctx, in.WorkID)
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		versions["work"] = workVersion
		// The knowledge home and work-version reads happen before the
		// mutation transaction opens: the effect's transaction holds the
		// write lock, and a second connection's read would deadlock.
		lessonHome, homeErr := r.Store.ResolveCompactionHome(ctx, in.WorkID)
		if homeErr != nil {
			return failureEnvelope(base, homeErr), nil
		}
		intents = []NextIntent{{Tool: "concord_knowledge", Operation: "resolve_note", QueryID: "PM1.Q10", ReasonCode: "verify_published_lesson"}}
		effect = func(ctx context.Context, tx *store.Transaction, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
			scopes := store.KnowledgeRecordScopes{Mode: "home"}
			if in.Scopes != nil {
				scopes = store.KnowledgeRecordScopes{Mode: in.Scopes.Mode, ProductIDs: in.Scopes.ProductIDs, ProjectIDs: in.Scopes.ProjectIDs, TagIDs: in.Scopes.TagIDs}
			}
			published, pubErr := store.PublishLessonRecord(ctx, lessonHome, store.LessonPublication{
				LessonID: in.LessonID, Title: in.Title, Summary: in.Summary, Content: in.Content,
				Tags: in.Tags, Scopes: scopes, Evidence: in.Evidence, Now: r.Authority.now(),
			})
			if pubErr != nil {
				return nil, nil, nil, pubErr
			}
			changed := []ChangedRef{{EntityKind: "lesson", ID: published.Record.ID, Version: strconv.FormatInt(workVersion, 10)}}
			return mutationPayload(changed, intents), []string{published.Record.ID}, changed, nil
		}
	case "concord_work_relate.resource_claim":
		var in resourceClaimInput
		if err := decodeOperationInput(raw, &in); err != nil {
			return base, err
		}
		versions["work"] = in.ExpectedVersion
		scope["work_ids"] = []string{in.WorkID}
		intents = []NextIntent{{Tool: "concord_work_browse", Operation: "resource_claims", QueryID: "PM1.Q13", ReasonCode: "verify_claim", RequiredFields: []string{"product_id"}}}
		effect = func(ctx context.Context, tx *store.Transaction, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
			payload, _ := json.Marshal(map[string]any{"work_id": in.WorkID, "expected_version": in.ExpectedVersion, "resulting_version": in.ExpectedVersion + 1, "resource_key": in.ResourceKey, "reason": in.Reason, "holder_agent": grant.AgentRef, "holder_session": grant.SessionRef})
			if _, err := store.ApplyOperationTx(ctx, tx, store.Operation{Events: []store.Event{{EventID: digest + ":claim", Kind: "work.resource_claimed", SubjectType: store.SubjectWorkItem, SubjectID: in.WorkID, Actor: grant.PrincipalRef, OccurredAt: r.Authority.now(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, in.WorkID): in.ExpectedVersion}}); err != nil {
				return nil, nil, nil, err
			}
			changed := []ChangedRef{{EntityKind: "resource_claim", ID: in.ResourceKey, Version: strconv.FormatInt(in.ExpectedVersion+1, 10)}}
			return mutationPayload(changed, intents), []string{in.ResourceKey}, changed, nil
		}
	case "concord_work_relate.resource_release":
		var in resourceReleaseInput
		if err := decodeOperationInput(raw, &in); err != nil {
			return base, err
		}
		versions["work"] = in.ExpectedVersion
		scope["work_ids"] = []string{in.WorkID}
		intents = []NextIntent{{Tool: "concord_work_browse", Operation: "resource_claims", QueryID: "PM1.Q13", ReasonCode: "verify_release", RequiredFields: []string{"product_id"}}}
		effect = func(ctx context.Context, tx *store.Transaction, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
			payload, _ := json.Marshal(map[string]any{"work_id": in.WorkID, "expected_version": in.ExpectedVersion, "resulting_version": in.ExpectedVersion + 1, "resource_key": in.ResourceKey})
			if _, err := store.ApplyOperationTx(ctx, tx, store.Operation{Events: []store.Event{{EventID: digest + ":release", Kind: "work.resource_claim_released", SubjectType: store.SubjectWorkItem, SubjectID: in.WorkID, Actor: grant.PrincipalRef, OccurredAt: r.Authority.now(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, in.WorkID): in.ExpectedVersion}}); err != nil {
				return nil, nil, nil, err
			}
			changed := []ChangedRef{{EntityKind: "resource_claim", ID: in.ResourceKey, Version: strconv.FormatInt(in.ExpectedVersion+1, 10)}}
			return mutationPayload(changed, intents), []string{in.ResourceKey}, changed, nil
		}
	case "concord_work_relate.message_send":
		var in messageSendInput
		if err := decodeOperationInput(raw, &in); err != nil {
			return base, err
		}
		if in.RecipientWorkID == "" && !in.Broadcast {
			return coreError(base, "invalid_input", "message requires a recipient work id or broadcast", "supply_recipient_or_broadcast", false), nil
		}
		if in.RecipientWorkID != "" && in.Broadcast {
			return coreError(base, "invalid_input", "message cannot both target one work and broadcast", "choose_addressing", false), nil
		}
		versions["work"] = in.ExpectedVersion
		scope["work_ids"] = []string{in.WorkID}
		if in.RecipientWorkID != "" {
			scope["work_ids"] = []string{in.WorkID, in.RecipientWorkID}
		}
		intents = []NextIntent{{Tool: "concord_work_browse", Operation: "messages", QueryID: "PM1.Q14", ReasonCode: "read_messages", RequiredFields: []string{"product_id", "work_id"}}}
		effect = func(ctx context.Context, tx *store.Transaction, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
			// Broadcast resolves the recipient set now, inside the caller's
			// transaction, so the fan-out sees a consistent Product snapshot.
			recipients := []string{in.RecipientWorkID}
			if in.Broadcast {
				active, listErr := activeWorkIDsTx(ctx, tx, r.Envelope.SelectedProductID)
				if listErr != nil {
					return nil, nil, nil, listErr
				}
				// The sender is not its own recipient.
				filtered := make([]string, 0, len(active))
				for _, id := range active {
					if id != in.WorkID {
						filtered = append(filtered, id)
					}
				}
				if len(filtered) == 0 {
					return nil, nil, nil, fmt.Errorf("no active work in this Product to receive the broadcast")
				}
				recipients = filtered
			}
			// One event per (sender, recipient) pair: the work version
			// advances once (on the sender) regardless of fan-out size.
			events := make([]store.Event, 0, len(recipients))
			ids := make([]string, 0, len(recipients))
			for i, recipient := range recipients {
				messageID := messageIDFor(digest, recipient)
				expected := in.ExpectedVersion + int64(i)
				payload, _ := json.Marshal(map[string]any{"work_id": in.WorkID, "expected_version": expected, "resulting_version": expected + 1, "message_id": messageID, "recipient_work_id": recipient, "body": in.Body})
				events = append(events, store.Event{EventID: digest + ":msg:" + recipient, Kind: "work.message_sent", SubjectType: store.SubjectWorkItem, SubjectID: in.WorkID, Actor: grant.PrincipalRef, OccurredAt: r.Authority.now(), PayloadVersion: 1, Payload: payload})
				ids = append(ids, messageID)
			}
			if _, err := store.ApplyOperationTx(ctx, tx, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, in.WorkID): in.ExpectedVersion}}); err != nil {
				return nil, nil, nil, err
			}
			changed := []ChangedRef{{EntityKind: "work_item", ID: in.WorkID, Version: strconv.FormatInt(in.ExpectedVersion+1, 10)}}
			return mutationPayload(changed, intents), ids, changed, nil
		}
	case "concord_work_relate.message_withdraw":
		var in messageWithdrawInput
		if err := decodeOperationInput(raw, &in); err != nil {
			return base, err
		}
		versions["work"] = in.ExpectedVersion
		scope["work_ids"] = []string{in.WorkID}
		intents = []NextIntent{{Tool: "concord_work_browse", Operation: "messages", QueryID: "PM1.Q14", ReasonCode: "read_messages", RequiredFields: []string{"product_id", "work_id"}}}
		effect = func(ctx context.Context, tx *store.Transaction, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
			payload, _ := json.Marshal(map[string]any{"work_id": in.WorkID, "expected_version": in.ExpectedVersion, "resulting_version": in.ExpectedVersion + 1, "message_id": in.MessageID})
			if _, err := store.ApplyOperationTx(ctx, tx, store.Operation{Events: []store.Event{{EventID: digest + ":withdraw", Kind: "work.message_withdrawn", SubjectType: store.SubjectWorkItem, SubjectID: in.WorkID, Actor: grant.PrincipalRef, OccurredAt: r.Authority.now(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, in.WorkID): in.ExpectedVersion}}); err != nil {
				return nil, nil, nil, err
			}
			changed := []ChangedRef{{EntityKind: "work_item", ID: in.WorkID, Version: strconv.FormatInt(in.ExpectedVersion+1, 10)}}
			return mutationPayload(changed, intents), []string{in.MessageID}, changed, nil
		}
	case "concord_work_define.observation_record":
		var in observationRecordInput
		if err := decodeOperationInput(raw, &in); err != nil {
			return base, err
		}
		scope["work_ids"] = []string{in.WorkID}
		if in.External != nil {
			// CD-0040 D2/D3: the reporting or verifying authority is derived
			// from the validated grant's trusted client. A caller-supplied
			// authority name never reaches the record, and the agent surface
			// can only ever claim attributed trusted-client reports — the
			// core-owned Git probe is not reachable from agent input.
			if err := validateObservationExternalVariant(in.External); err != nil {
				return coreError(base, "invalid_input", err.Error(), "reread_entities", false), nil
			}
			intents = []NextIntent{{Tool: "concord_work_trace", Operation: "external_observations", QueryID: "CD-0040.R1", ReasonCode: "verify_external_observation", RequiredFields: []string{"work_id"}}}
			effect = func(ctx context.Context, tx *store.Transaction, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
				if in.External.Kind == "capture" {
					// The reviewed policy references are derived from the
					// subject kind before validation, which requires the
					// record to carry exactly the reviewed policy.
					policy, _ := store.ExternalSubjectPolicyFor(in.External.SubjectKind)
					capture := store.ExternalObservationCapture{
						ObservationID:         in.External.ObservationID,
						SubjectKind:           in.External.SubjectKind,
						SubjectRef:            in.External.SubjectRef,
						CaptureMethod:         store.CaptureTrustedClientReport,
						CapturedAt:            in.External.CapturedAt,
						ReportingAuthorityRef: "client:" + grant.ClientRef,
						SubjectDigest:         in.External.SubjectDigest,
						ObservedUniverse:      *in.External.ObservedUniverse,
						FreshnessPolicyRef:    store.PolicyRef(policy),
						DivergencePolicyRef:   store.PolicyRef(policy),
					}
					if err := store.ValidateExternalObservationCapture(capture); err != nil {
						return nil, nil, nil, err
					}
					if err := store.AppendExternalObservationCaptureTx(ctx, tx, in.WorkID, grant.PrincipalRef, r.Authority.now(), capture); err != nil {
						return nil, nil, nil, err
					}
					changed := []ChangedRef{{EntityKind: "external_observation", ID: capture.ObservationID, Version: "1"}}
					return mutationPayload(changed, intents), []string{capture.ObservationID}, changed, nil
				}
				verification := store.ExternalObservationVerification{
					ObservationID:         in.External.ObservationID,
					VerificationMethod:    store.VerifyTrustedClientReport,
					VerifiedAt:            in.External.VerifiedAt,
					VerifyingAuthorityRef: "client:" + grant.ClientRef,
					Result:                store.VerificationResultKind(in.External.Result),
					CurrentDigest:         in.External.CurrentDigest,
					Omissions:             in.External.Omissions,
				}
				if err := store.ValidateExternalObservationVerification(verification); err != nil {
					return nil, nil, nil, err
				}
				if err := store.AppendExternalObservationVerificationTx(ctx, tx, in.WorkID, grant.PrincipalRef, r.Authority.now(), verification); err != nil {
					return nil, nil, nil, err
				}
				changed := []ChangedRef{{EntityKind: "external_observation", ID: verification.ObservationID, Version: "2"}}
				return mutationPayload(changed, intents), []string{verification.ObservationID}, changed, nil
			}
		} else {
			intents = []NextIntent{{Tool: "concord_work_trace", Operation: "continuity", QueryID: "C19.Continuity", ReasonCode: "verify_observation_visible", RequiredFields: []string{"work_id"}}}
			effect = func(ctx context.Context, tx *store.Transaction, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
				observationID := in.ObservationID
				if observationID == "" {
					sum := sha256.Sum256([]byte(digest))
					observationID = "obs:" + hex.EncodeToString(sum[:8])
				}
				payload, _ := json.Marshal(map[string]any{"observation_id": observationID, "statement": in.Statement, "refs": in.Refs, "tags": in.Tags})
				if _, err := store.ApplyOperationTx(ctx, tx, store.Operation{Events: []store.Event{{EventID: digest + ":observation", Kind: "work.observation_recorded", SubjectType: store.SubjectWorkItem, SubjectID: in.WorkID, Actor: grant.PrincipalRef, OccurredAt: r.Authority.now(), PayloadVersion: 1, Payload: payload}}}); err != nil {
					return nil, nil, nil, err
				}
				changed := []ChangedRef{{EntityKind: "observation", ID: observationID, Version: "1"}}
				return mutationPayload(changed, intents), []string{observationID}, changed, nil
			}
		}
	case "concord_domain.observation_record":
		var in domainObservationRecordInput
		if err := decodeOperationInput(raw, &in); err != nil {
			return base, err
		}
		product := in.ProductID
		if product == "" {
			product = r.Envelope.SelectedProductID
		}
		if product == "" || in.DomainID == "" {
			return coreError(base, "unknown_scope", "recording a Domain observation requires a resolved Product and Domain", "reread_entities", false), nil
		}
		scope["product_id"] = product
		// CD-0068 D6: the observation is read back through the Domain surface
		// it was recorded against, not through a dedicated read.
		intents = []NextIntent{{Tool: "concord_domain", Operation: "detail", QueryID: "C22.DomainDetail", ReasonCode: "verify_observation_visible", RequiredFields: []string{"domain_id"}}}
		effect = func(ctx context.Context, tx *store.Transaction, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
			sum := sha256.Sum256([]byte(digest))
			observationID := "dob:" + hex.EncodeToString(sum[:8])
			payload, _ := json.Marshal(map[string]any{"observation_id": observationID, "product_id": product, "domain_id": in.DomainID, "statement": in.Statement, "refs": in.Refs, "tags": in.Tags})
			if _, err := store.ApplyOperationTx(ctx, tx, store.Operation{Events: []store.Event{{EventID: digest + ":domain_observation", Kind: "domain.observation_recorded", SubjectType: store.SubjectProduct, SubjectID: product, Actor: grant.PrincipalRef, OccurredAt: r.Authority.now(), PayloadVersion: 1, Payload: payload}}}); err != nil {
				return nil, nil, nil, err
			}
			changed := []ChangedRef{{EntityKind: "domain_observation", ID: observationID, Version: "1"}}
			return mutationPayload(changed, intents), []string{observationID}, changed, nil
		}
	case "concord_domain.observation_dismiss":
		var in domainObservationDismissInput
		if err := decodeOperationInput(raw, &in); err != nil {
			return base, err
		}
		if in.Approval != nil {
			approval = in.Approval.ApprovalRef
		}
		product := in.ProductID
		if product == "" {
			product = r.Envelope.SelectedProductID
		}
		if product == "" || in.DomainID == "" || in.ObservationID == "" {
			return coreError(base, "unknown_scope", "dismissing a Domain observation requires a resolved Product, Domain, and observation", "reread_entities", false), nil
		}
		scope["product_id"] = product
		intents = []NextIntent{{Tool: "concord_domain", Operation: "detail", QueryID: "C22.DomainDetail", ReasonCode: "verify_observation_dismissed", RequiredFields: []string{"domain_id"}}}
		effect = func(ctx context.Context, tx *store.Transaction, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
			payload, _ := json.Marshal(map[string]any{"observation_id": in.ObservationID, "product_id": product, "domain_id": in.DomainID})
			if _, err := store.ApplyOperationTx(ctx, tx, store.Operation{Events: []store.Event{{EventID: digest + ":domain_observation_dismissed", Kind: "domain.observation_dismissed", SubjectType: store.SubjectProduct, SubjectID: product, Actor: grant.PrincipalRef, OccurredAt: r.Authority.now(), PayloadVersion: 1, Payload: payload}}}); err != nil {
				return nil, nil, nil, err
			}
			changed := []ChangedRef{{EntityKind: "domain_observation", ID: in.ObservationID, Version: "2"}}
			return mutationPayload(changed, intents), []string{in.ObservationID}, changed, nil
		}
	case "concord_work_transition.worktree_claim":
		var in worktreeClaimInput
		if err := decodeOperationInput(raw, &in); err != nil {
			return base, err
		}
		versions["work"] = in.ExpectedVersion
		scope["work_ids"] = []string{in.WorkID}
		intents = []NextIntent{{Tool: "concord_work_browse", Operation: "scope", QueryID: "PM1.Q6", ReasonCode: "refresh_work_version", RequiredFields: []string{"work_id"}}}
		effect = func(ctx context.Context, tx *store.Transaction, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
			opID := digest + ":worktree-claim:" + in.ProjectID
			if _, err := store.ClaimWorktreeTx(ctx, tx, store.WorktreeClaimRequest{
				OpID: opID, WorkID: in.WorkID, ProjectID: in.ProjectID,
				Branch: in.Branch, BaseSHA: in.BaseSHA, Path: in.Path,
				PrincipalRef: grant.PrincipalRef, RequestID: in.IdempotencyKey,
				ExpectedVersion: in.ExpectedVersion, Now: r.Authority.now(),
			}); err != nil {
				return nil, nil, nil, err
			}
			changed := []ChangedRef{{EntityKind: "work_item", ID: in.WorkID, Version: strconv.FormatInt(in.ExpectedVersion+1, 10)}}
			return mutationPayload(changed, intents), []string{opID + ":worktree-created"}, changed, nil
		}
	case "concord_work_transition.worktree_reclaim":
		var in worktreeReclaimInput
		if err := decodeOperationInput(raw, &in); err != nil {
			return base, err
		}
		versions["work"] = in.ExpectedVersion
		scope["work_ids"] = []string{in.WorkID}
		intents = []NextIntent{{Tool: "concord_work_browse", Operation: "scope", QueryID: "PM1.Q6", ReasonCode: "refresh_work_version", RequiredFields: []string{"work_id"}}}
		effect = func(ctx context.Context, tx *store.Transaction, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
			if _, err := store.ReclaimWorktreeTx(ctx, tx, store.WorktreeReclaimRequest{
				WorkID: in.WorkID, ProjectID: in.ProjectID, DefaultRef: in.DefaultRef,
				PrincipalRef: grant.PrincipalRef, RequestID: in.IdempotencyKey,
				ExpectedVersion: in.ExpectedVersion, Now: r.Authority.now(),
			}); err != nil {
				return nil, nil, nil, err
			}
			changed := []ChangedRef{{EntityKind: "work_item", ID: in.WorkID, Version: strconv.FormatInt(in.ExpectedVersion+1, 10)}}
			return mutationPayload(changed, intents), []string{in.WorkID + ":" + in.ProjectID + ":worktree-reclaimed"}, changed, nil
		}
	case "concord_work_relate.set_memberships":
		var in membershipsMutationInput
		if err := decodeOperationInput(raw, &in); err != nil {
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
		effect = func(ctx context.Context, tx *store.Transaction, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
			payload, _ := json.Marshal(map[string]any{"memberships": in.Memberships, "expected_version": in.ExpectedVersion, "resulting_version": in.ExpectedVersion + 1})
			result, err := store.ApplyOperationTx(ctx, tx, store.Operation{Events: []store.Event{{EventID: digest + ":memberships", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: in.WorkID, Actor: grant.PrincipalRef, OccurredAt: r.Authority.now(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, in.WorkID): in.ExpectedVersion}})
			if err != nil {
				return nil, nil, nil, err
			}
			changed := []ChangedRef{{EntityKind: "work_item", ID: in.WorkID, Version: strconv.FormatInt(in.ExpectedVersion+1, 10)}}
			return mutationPayload(changed, intents), result.EventIDs, changed, nil
		}
	case "concord_work_relate.resolve_overlap":
		var in resolveOverlapMutationInput
		if err := decodeOperationInput(raw, &in); err != nil {
			return base, err
		}
		if in.Approval != nil {
			approval = in.Approval.ApprovalRef
		}
		requiresApproval = true
		versions["from"] = in.FromExpectedVersion
		versions["to"] = in.ToExpectedVersion
		versions["from_contract"] = in.FromContractVersion
		versions["to_contract"] = in.ToContractVersion
		scope["work_ids"] = []string{in.FromWorkID, in.ToWorkID}
		scope["resolution_kind"] = in.ResolutionKind
		scope["from_work_id"] = in.FromWorkID
		scope["to_work_id"] = in.ToWorkID
		scope["product_id"] = r.Envelope.SelectedProductID
		intents = []NextIntent{{Tool: "concord_work_trace", Operation: "continuity", QueryID: "C19.Continuity", ReasonCode: "inspect_overlap_resolution", RequiredFields: []string{"work_id"}}}
		effect = func(ctx context.Context, tx *store.Transaction, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
			consumedApprovalRef, _ := scope["approval_ref"].(string)
			result, err := store.ResolveWorkflowDomainOverlapTx(ctx, tx, store.WorkflowDomainOverlapResolutionRequest{EventID: digest + ":overlap", FromWorkID: in.FromWorkID, ToWorkID: in.ToWorkID, FromExpectedVersion: in.FromExpectedVersion, ToExpectedVersion: in.ToExpectedVersion, FromContractVersion: in.FromContractVersion, ToContractVersion: in.ToContractVersion, ResolutionKind: in.ResolutionKind, Reason: in.Reason, ApprovalRef: consumedApprovalRef, Actor: grant.PrincipalRef, OccurredAt: r.Authority.now()})
			if err != nil {
				return nil, nil, nil, err
			}
			changed := []ChangedRef{{EntityKind: "work_item", ID: in.FromWorkID, Version: strconv.FormatInt(in.FromExpectedVersion+1, 10)}, {EntityKind: "work_item", ID: in.ToWorkID, Version: strconv.FormatInt(in.ToExpectedVersion+1, 10)}}
			return mutationPayload(changed, intents), result.EventIDs, changed, nil
		}
	case "concord_work_relate.link":
		var in linkMutationInput
		if err := decodeOperationInput(raw, &in); err != nil {
			return base, err
		}
		if in.Kind == "supersedes" || in.Kind == "compatible_with" || in.Kind == "merged_into" {
			return coreError(base, "invalid_relation", "operator-only overlap relations require relate.resolve_overlap", "reread_entities", false), nil
		}
		if in.Approval != nil {
			approval = in.Approval.ApprovalRef
		}
		versions["from"] = in.FromExpectedVersion
		versions["to"] = in.ToExpectedVersion
		scope["work_ids"] = []string{in.FromWorkID, in.ToWorkID}
		intents = []NextIntent{{Tool: "concord_work_relate", Operation: "unlink", ReasonCode: "remove_relation"}}
		effect = func(ctx context.Context, tx *store.Transaction, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
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
		if err := decodeOperationInput(raw, &in); err != nil {
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
		if err := decodeOperationInput(raw, &in); err != nil {
			return base, err
		}
		if in.Approval != nil {
			approval = in.Approval.ApprovalRef
		}
		versions["predecessor"] = in.PredecessorExpected
		versions["successor"] = in.SuccessorExpected
		scope["work_ids"] = []string{in.PredecessorID, in.SuccessorID}
		intents = []NextIntent{{Tool: "concord_work_relate", Operation: "restore_superseded", ReasonCode: "restore_or_replace_successor"}}
		effect = func(ctx context.Context, tx *store.Transaction, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
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
		if err := decodeOperationInput(raw, &in); err != nil {
			return base, err
		}
		if in.Approval != nil {
			approval = in.Approval.ApprovalRef
		}
		versions["predecessor"] = in.PredecessorExpected
		versions["successor"] = in.SuccessorExpected
		scope["work_ids"] = []string{in.PredecessorID, in.SuccessorID}
		intents = []NextIntent{{Tool: "concord_work_browse", Operation: "scope", QueryID: "PM1.Q6", ReasonCode: "inspect_restored_work"}}
		effect = func(ctx context.Context, tx *store.Transaction, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
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
	return r.executeMutation(ctx, base, raw, digest, scope, versions, consequence, approval, requiresApproval, governingConflict, intents, effect)
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

func uniqueProducts(byProject map[string][]string, projectIDs []string) []string {
	seen := map[string]bool{}
	for _, projectID := range projectIDs {
		for _, productID := range byProject[projectID] {
			seen[productID] = true
		}
	}
	products := make([]string, 0, len(seen))
	for productID := range seen {
		products = append(products, productID)
	}
	sort.Strings(products)
	return products
}

func deriveInitiativeProductsTx(ctx context.Context, tx *store.Transaction, projectIDs []string) ([]string, error) {
	byProject, err := store.ProductsForProjectIDsTx(ctx, tx, projectIDs)
	if err != nil {
		return nil, err
	}
	return uniqueProducts(byProject, projectIDs), nil
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

func (r runtime) mutateCompaction(ctx context.Context, base Envelope, raw []byte, digest string, op ContractOperation) (Envelope, error) {
	var publish compactPublishInput
	var reconcile compactReconcileInput
	if op.ID == "concord_work_compact.publish" {
		if err := decodeOperationInput(raw, &publish); err != nil {
			return base, err
		}
		if publish.Approval == nil || publish.Approval.ApprovalRef == "" {
			return coreError(base, "approval_required", "publication requires a core approval reference", "request_approval", false), nil
		}
	}
	if op.ID == "concord_work_compact.reconcile" {
		if err := decodeOperationInput(raw, &reconcile); err != nil {
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
	grant, err := r.Authority.ValidateInvocation(ctx, Invocation{GrantToken: r.Envelope.GrantToken, ClientRef: r.Envelope.ClientRef, PrincipalRef: r.Envelope.PrincipalRef, SessionRef: r.Envelope.SessionRef, AgentRef: r.Envelope.AgentRef, Directory: r.Envelope.Directory, Worktree: r.Envelope.Worktree, ManifestDigest: r.Envelope.ManifestDigest, RequiredCapability: "work_compact", ProductID: r.Envelope.SelectedProductID, ProjectID: r.Envelope.AmbientProjectID})
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
		claimReq := store.ClaimRequest{OpID: opID, WorkID: workID, WorkflowTypeRef: "concord.pm6.compaction", WorkflowTypeVersion: 1, StepID: "git_proof", StepKind: store.StepCrossAuthority, AcceptedInputsDigest: digest, AcceptedScopeSnapshot: string(acceptedScope), PrincipalRef: grant.PrincipalRef, Tool: r.Tool, IdempotencyKey: key, RequestID: r.Envelope.RequestID, ObservedAt: r.Authority.now(), ApprovalRef: publish.Approval.ApprovalRef, ContractDigest: ManifestDigest}
		inv := Invocation{GrantToken: r.Envelope.GrantToken, ClientRef: r.Envelope.ClientRef, PrincipalRef: r.Envelope.PrincipalRef, SessionRef: r.Envelope.SessionRef, AgentRef: r.Envelope.AgentRef, Directory: r.Envelope.Directory, Worktree: r.Envelope.Worktree, ManifestDigest: r.Envelope.ManifestDigest, HostAssertionDigest: r.Envelope.HostAssertionDigest, RequiredCapability: "work_compact", ProductID: r.Envelope.SelectedProductID, ProjectID: r.Envelope.AmbientProjectID}
		claim, claimErr := store.ClaimStepAuthorized(ctx, r.Store, claimReq, func(tx *store.Transaction) error {
			// CD-0041 D7: publication is consequential, so the claim refuses
			// before any git note is written when the contract's law revision
			// pins or its active Domain overlaps no longer validate.
			if err := store.CheckWorkflowConsequentialBoundaryTx(ctx, tx, workID); err != nil {
				return err
			}
			if _, err := r.Authority.ValidateAndConsumeGrantTx(ctx, tx, inv); err != nil {
				return err
			}
			_, _, err := r.consumeApprovalTx(ctx, tx, inv, grant, ApprovalCheck{ApprovalRef: publish.Approval.ApprovalRef, OperationDigest: digest, Scope: scope, Versions: map[string]any{"work": publish.ExpectedVersion}, Consequence: string(op.Consequence), ClientRef: grant.ClientRef, SessionRef: grant.SessionRef})
			return err
		})
		if claimErr != nil {
			return failureEnvelope(base, claimErr), nil
		}
		// committed; the durability barrier must hold before acknowledging the claim dispatch
		if syncErr := r.Store.SyncDurable(ctx); syncErr != nil {
			return failureEnvelope(base, syncErr), nil
		}
		if claim.ResultKind == store.ResultCompleted {
			changed := decodeChangedRefs(claim.ChangedRefs)
			base.Replayed = claim.Replayed
			base.ResolvedScope = &Scope{ProductID: r.Envelope.SelectedProductID, ProjectIDs: []string{r.Envelope.AmbientProjectID}, WorkIDs: []string{workID}, ScopeVersion: r.Envelope.ScopeVersion}
			return r.mutationResult(base, mutationPayload(changed, []NextIntent{{Tool: "concord_knowledge", Operation: "resolve_note", QueryID: "PM1.Q10", ReasonCode: "verify_canonical_note"}}), changed, nil), nil
		}
		var committed store.CommittedNote
		var verified store.VerifiedNote
		completed, failedStep, publishErr := r.runPublication([]publicationStep{
			{ID: "git_write", Phase: "git_publish", Run: func() (err error) {
				committed, err = store.PublishCanonicalNote(ctx, resolvedHome, workID, publish.Content, publish.ContentDigest)
				return err
			}},
			{ID: "commit_verification", Phase: "verify_commit", Run: func() (err error) {
				verified, err = store.VerifyCommittedNote(ctx, resolvedHome.RepoPath, committed.CommitOID, committed.NotePath, publish.ContentDigest)
				return err
			}},
			{ID: "sqlite_link", Phase: "record_locator", Run: func() error {
				return store.PublishCompactionLink(ctx, r.Store, store.CompactionLinkRequest{EventID: opID + ":link", WorkID: workID, ExpectedVersion: publish.ExpectedVersion, Actor: grant.PrincipalRef, OccurredAt: r.Authority.now(), Home: resolvedHome, CommitOID: verified.CommitOID, NotePath: verified.NotePath, ExpectedHash: publish.ContentDigest, Reason: "agent compaction publish"})
			}},
		})
		if publishErr != nil {
			return pendingCompaction(base, workID, claim, failedStep, completed, publishErr), nil
		}
		changedJSON, _ := json.Marshal(changed[0])
		complete, completeErr := store.CompleteStep(ctx, r.Store, store.CompleteRequest{OpID: opID, AttemptEpoch: claim.AttemptEpoch, ResultKind: store.ResultCompleted, ResultPayload: string(payload), ChangedRefs: []string{string(changedJSON)}, PrincipalRef: grant.PrincipalRef, Tool: r.Tool, IdempotencyKey: key + ":complete", RequestID: r.Envelope.RequestID, ObservedAt: r.Authority.now(), CompletedAt: timePtr(r.Authority.now())})
		if completeErr != nil {
			return pendingCompaction(base, workID, claim, "operation_complete", completed, completeErr), nil
		}
		// committed; the durability barrier must hold before acknowledging the completion
		if syncErr := r.Store.SyncDurable(ctx); syncErr != nil {
			return failureEnvelope(base, syncErr), nil
		}
		base.Replayed = complete.Replayed
		result.Replayed = complete.Replayed
		return result, nil
	}
	if reconcile.OperationID == "" && reconcile.WorkID != "" {
		if reconcile.ExpectedWorkVersion <= 0 {
			return coreError(base, "invalid_input", "orphan discovery requires expected_work_version", "reread_entities", false), nil
		}
		currentVersion, err := r.Store.TerminalWorkVersion(ctx, reconcile.WorkID)
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		if currentVersion != reconcile.ExpectedWorkVersion {
			return coreError(base, "version_conflict", "terminal work version changed before orphan discovery", "reread_entities", false), nil
		}
		pending, err := r.Store.PendingOperationForWork(ctx, reconcile.WorkID)
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		reconcile.OperationID = pending.OperationID
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
		return pendingCompaction(base, reconcile.WorkID, step, "resolve_home", nil, homeErr), nil
	}
	note, proofErr := store.FindVerifiedWorkNote(ctx, home, reconcile.WorkID, proofDigest)
	if proofErr != nil {
		return pendingCompaction(base, reconcile.WorkID, step, "git_proof", nil, proofErr), nil
	}
	if workVersion <= 0 {
		return pendingCompaction(base, reconcile.WorkID, step, "resolve_work_version", nil, fmt.Errorf("durable publication did not retain terminal work version")), nil
	}
	changed := []ChangedRef{{EntityKind: "work_item", ID: reconcile.WorkID, Version: strconv.FormatInt(workVersion+1, 10)}}
	payload := mutationPayload(changed, []NextIntent{{Tool: "concord_knowledge", Operation: "resolve_note", QueryID: "PM1.Q10", ReasonCode: "verify_canonical_note"}})
	base.ResolvedScope = &Scope{ProductID: r.Envelope.SelectedProductID, ProjectIDs: []string{r.Envelope.AmbientProjectID}, WorkIDs: []string{reconcile.WorkID}, ScopeVersion: r.Envelope.ScopeVersion}
	result := r.mutationResult(base, payload, changed, []NextIntent{{Tool: "concord_knowledge", Operation: "resolve_note", QueryID: "PM1.Q10", ReasonCode: "verify_canonical_note"}})
	if result.Outcome == OutcomeError {
		return result, nil
	}
	// Reconcile is the closed recovery choice for an orphaned note, so its link
	// is exempt from the CD-0041 D7 boundary check. Guarding it would refuse the
	// only way out of a pending compaction.
	if linkErr := store.PublishCompactionLink(ctx, r.Store, store.CompactionLinkRequest{EventID: reconcile.OperationID + ":reconcile-link", WorkID: reconcile.WorkID, ExpectedVersion: workVersion, Actor: grant.PrincipalRef, OccurredAt: r.Authority.now(), Home: home, CommitOID: note.CommitOID, NotePath: note.NotePath, ExpectedHash: proofDigest, Reason: "reconcile verified orphan note", Boundary: store.CompactionBoundaryRecoveryExempt}); linkErr != nil {
		return pendingCompaction(base, reconcile.WorkID, step, "sqlite_link", []string{"operation_claimed", "git_proof"}, linkErr), nil
	}
	changedJSON, _ := json.Marshal(changed[0])
	complete, completeErr := store.CompleteStep(ctx, r.Store, store.CompleteRequest{OpID: reconcile.OperationID, AttemptEpoch: step.AttemptEpoch, ResultKind: store.ResultCompleted, ResultPayload: string(payload), ChangedRefs: []string{string(changedJSON)}, PrincipalRef: grant.PrincipalRef, Tool: r.Tool, IdempotencyKey: idempotencyKey(raw) + ":complete", RequestID: r.Envelope.RequestID, ObservedAt: r.Authority.now(), CompletedAt: timePtr(r.Authority.now())})
	if completeErr != nil {
		return pendingCompaction(base, reconcile.WorkID, step, "operation_complete", []string{"operation_claimed", "git_proof", "sqlite_link"}, completeErr), nil
	}
	// committed; the durability barrier must hold before acknowledging the completion
	if syncErr := r.Store.SyncDurable(ctx); syncErr != nil {
		return failureEnvelope(base, syncErr), nil
	}
	base.Replayed = complete.Replayed
	result.Replayed = complete.Replayed
	return result, nil
}

// publicationStep is one phase of the CD-0006/PM6 cross-authority publication
// seam. ID names the operation step an envelope reports as completed; Phase
// names the cross-authority ordering position. The corpus uses both vocabularies
// — `completed_steps` carries step IDs, `publication_order` carries phases — so
// the pipeline carries both rather than conflating them.
type publicationStep struct {
	ID    string
	Phase string
	Run   func() error
}

// publicationPhases is the accepted publication order, declared as data. The
// order is a contract (docs/agent-mutation-tool-contract.md: commit to git,
// verify the commit, then append the SQLite compaction link), so it lives in one
// declared sequence rather than being an emergent property of statement order.
var publicationPhases = []string{"git_publish", "verify_commit", "record_locator"}

// runPublication executes the publication steps in declared order and returns
// the steps that actually completed. On failure it returns the honest completed
// prefix and the step that failed, so a partial outcome tells the operator
// exactly how far the cross-authority effect got.
func (r runtime) runPublication(steps []publicationStep) ([]string, string, error) {
	completed := make([]string, 0, len(steps)+1)
	completed = append(completed, "operation_claimed")
	for i, step := range steps {
		if step.Phase != publicationPhases[i] {
			return completed, step.ID, fmt.Errorf("publication step %d is %q but the accepted order requires %q", i, step.Phase, publicationPhases[i])
		}
		if err := step.Run(); err != nil {
			return completed, step.ID, err
		}
		completed = append(completed, step.ID)
		if r.Authority != nil && r.Authority.publicationObserver != nil {
			if err := r.Authority.publicationObserver(step.Phase); err != nil {
				return completed, step.ID, err
			}
		}
	}
	return completed, "", nil
}

func pendingCompaction(base Envelope, workID string, claim store.FenceResult, step string, completed []string, cause error) Envelope {
	ref := operationRefFromFence(claim, "pending", step)
	base.ResolvedScope = &Scope{WorkIDs: []string{workID}}
	if len(completed) == 0 {
		completed = []string{"operation_claimed"}
	}
	return NewPartial(base, ref, completed, TypedError{Kind: "operation_conflict", RetrySafe: true, RecoveryAction: RecoveryAction{Kind: "reconcile_operation"}, EffectState: EffectPartial, Message: cause.Error()})
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

func currentLifecycle(ctx context.Context, tx *store.Transaction, id string) (string, error) {
	return store.WorkLifecycleTx(ctx, tx, id)
}

// mutationResult is the sole producer for inline and replayed mutation success.
// It validates the exact tool/operation result before the envelope can cross the
// agent boundary, including caller budgets and the canonical envelope cap.
func (r runtime) mutationResult(base Envelope, payload json.RawMessage, changed []ChangedRef, intents []NextIntent) Envelope {
	if err := ValidateOperationPayload(r.Tool, r.Operation, payload, true); err != nil {
		return coreError(base, "malformed_response", fmt.Sprintf("mutation result failed closed-schema validation: %v", err), "contact_operator", false)
	}
	if r.Budget.MaxBytes > 0 && len(payload) > r.Budget.MaxBytes {
		return r.budgetRefusal(base, "mutation result exceeds requested max_bytes budget")
	}
	if r.Budget.MaxItems > 0 && maxArrayLength(payload) > r.Budget.MaxItems {
		return r.budgetRefusal(base, "mutation result exceeds requested max_items budget")
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

func (r runtime) executeMutation(ctx context.Context, base Envelope, raw []byte, digest string, scope, versions map[string]any, consequence, approval string, requiresApproval bool, governingConflict []string, intents []NextIntent, effect mutationEffect) (Envelope, error) {
	var response Envelope
	var resultRejected bool
	err := r.Store.Transact(ctx, func(tx *store.Transaction) error {
		contractOp, registered := ValidateContractOperation(r.Tool, r.Operation)
		if !registered {
			return newRuntimeFailure("invariant_violation", fmt.Sprintf("mutation dispatch reached unregistered operation %s.%s", r.Tool, r.Operation), "contact_operator", false)
		}
		inv := Invocation{GrantToken: r.Envelope.GrantToken, ClientRef: r.Envelope.ClientRef, PrincipalRef: r.Envelope.PrincipalRef, SessionRef: r.Envelope.SessionRef, AgentRef: r.Envelope.AgentRef, Directory: r.Envelope.Directory, Worktree: r.Envelope.Worktree, ManifestDigest: r.Envelope.ManifestDigest, HostAssertionDigest: r.Envelope.HostAssertionDigest, RequiredCapability: contractOp.Capability, ProductID: r.Envelope.SelectedProductID, ProjectID: r.Envelope.AmbientProjectID}
		if inv.HostAssertionDigest == "" {
			inv.HostAssertionDigest = digest
		}
		grant, err := r.Authority.ValidateGrantTx(ctx, tx, inv)
		if err != nil {
			return err
		}
		derivedProducts, err := deriveMutationProductsTx(ctx, tx, scope)
		if err != nil {
			return err
		}
		if expected, ok := scope["product_ids"].([]string); ok && !equalStrings(expected, derivedProducts) {
			return newRuntimeFailure("version_conflict", "derived Product scope changed after authorization preflight", "reread_entities", false)
		}
		crossProduct := false
		for _, product := range derivedProducts {
			if !contains(grant.ProductScope, product) {
				return newRuntimeFailure("unauthorized", fmt.Sprintf("mutation work Product %s is outside grant Product scope %v", product, grant.ProductScope), "contact_operator", false)
			}
			if r.Envelope.SelectedProductID != "" && product != r.Envelope.SelectedProductID {
				crossProduct = true
				if !containsCapability(grant.Capabilities, Capability("cross_scope")) {
					return newRuntimeFailure("unauthorized", "cross-Product mutation requires cross_scope capability", "contact_operator", false)
				}
			}
		}
		scope["product_ids"] = derivedProducts
		if crossProduct {
			requiresApproval = true
		}
		key := idempotencyKey(raw)
		prior, found, err := store.LookupMutationIdempotencyTx(ctx, tx, store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: r.Tool, OperationKind: r.Operation, IdempotencyKey: key})
		if err != nil {
			return err
		}
		if found {
			if prior.CanonicalDigest != digest {
				return storeIdempotencyConflict(r.Operation, key)
			}
			var changed []ChangedRef
			_ = json.Unmarshal([]byte(prior.ChangedRefs), &changed)
			base.Replayed = true
			base.ResolvedScope = &Scope{ProductID: r.Envelope.SelectedProductID, ProjectIDs: []string{r.Envelope.AmbientProjectID}, ScopeVersion: r.Envelope.ScopeVersion}
			response = r.mutationResult(base, json.RawMessage(prior.ResultPayload), changed, intents)
			if response.Outcome == OutcomeError {
				resultRejected = true
				return errors.New("mutation result rejected")
			}
			return store.TouchMutationIdempotencyTx(ctx, tx, store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: r.Tool, OperationKind: r.Operation, IdempotencyKey: key}, r.Authority.now())
		}
		// CD-0041 D7: every consequential boundary validates the contract's law
		// revision pins and its active Domain overlaps. The recovery operations
		// this exempts are the closed recovery choices both refusals name, so
		// guarding them would refuse the only way out of either condition.
		if !mutationIsOverlapRecovery(r.Tool, r.Operation, raw) {
			for _, workID := range mutationScopeWorkIDs(scope) {
				if err := store.CheckWorkflowConsequentialBoundaryTx(ctx, tx, workID); err != nil {
					return err
				}
			}
		}
		// CD-0038 D3: the seconds ceiling is admitted after the idempotency
		// lookup above, for the same reason the workflow-action path states.
		// A refused request records no idempotency effect, so the same key may
		// be reused with a lower budget.
		if r.Budget.CeilingRefused {
			response = r.budgetRefusal(base, fmt.Sprintf("requested_budget_seconds %d exceeds supported %d", r.Budget.RequestedSeconds, r.Budget.SupportedSeconds))
			resultRejected = true
			return errors.New("budget admission refused")
		}
		if requiresApproval && approval == "" {
			challengeScope := boundedApprovalScope(scope)
			spec := ApprovalChallengeSpec{OperationDigest: digest, Scope: challengeScope, Versions: versions, Consequence: consequence, HostAssertionDigest: inv.HostAssertionDigest, ExpiresAt: r.Authority.now().Add(10 * time.Minute)}
			challengeRef, err := r.Authority.CreateApprovalChallengeTx(ctx, tx, inv, spec)
			if err != nil {
				return err
			}
			details := map[string]any{"approval_ref": challengeRef, "summary": "Approve the exact requested mutation, scope, and expected versions.", "operation_digest": digest, "scope": approvalScopeBindings(challengeScope), "versions": approvalVersionBindings(versions)}
			// CD-0037 D2: the coupling is challenge presence. Both branches
			// below minted this challenge, so both carry the summary — the
			// governing-conflict envelope as much as the plain refusal.
			summary := consequenceSummaryFor(r.Tool, r.Operation, spec)
			if len(governingConflict) > 0 {
				response = governingConflictEnvelope(base, governingConflict)
				details["summary"] = "Clarify the intent, amend the accepted contract, or approve this scope cut."
			} else {
				response = coreError(base, "approval_required", "core approval is required for this mutation", "request_approval", false)
			}
			response.Error.ConsequenceSummary = summary
			for _, key := range []string{"resolution_kind", "from_work_id", "to_work_id"} {
				if value, ok := scope[key]; ok {
					details[key] = value
				}
			}
			response.Error.Details = details
			return nil
		}
		if _, err := r.Authority.ValidateAndConsumeGrantTx(ctx, tx, inv); err != nil {
			return err
		}
		if requiresApproval {
			approvalCheck := ApprovalCheck{ApprovalRef: approval, OperationDigest: digest, Scope: boundedApprovalScope(scope), Versions: versions, Consequence: consequence, ClientRef: grant.ClientRef, SessionRef: grant.SessionRef}
			if _, consumedApprovalRef, err := r.consumeApprovalTx(ctx, tx, inv, grant, approvalCheck); err != nil {
				response = coreError(base, "approval_invalid", err.Error(), "request_approval", false)
				resultRejected = true
				return errors.New("approval invalid")
			} else {
				scope["approval_ref"] = consumedApprovalRef
			}
		}
		payload, eventIDs, changed, err := effect(ctx, tx, grant)
		if err != nil {
			return err
		}
		base.ResolvedScope = scopeFromMap(scope)
		if base.ResolvedScope == nil {
			base.ResolvedScope = &Scope{ProductID: r.Envelope.SelectedProductID, ProjectIDs: []string{r.Envelope.AmbientProjectID}, ScopeVersion: r.Envelope.ScopeVersion}
		}
		response = r.mutationResult(base, payload, changed, intents)
		if response.Outcome == OutcomeError {
			resultRejected = true
			return errors.New("mutation result rejected")
		}
		changedJSON, _ := json.Marshal(changed)
		authorizedScope, _ := json.Marshal(boundedApprovalScope(scope))
		return store.InsertMutationIdempotencyTx(ctx, tx, store.MutationIdempotencyInsert{Key: store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: r.Tool, OperationKind: r.Operation, IdempotencyKey: key}, CanonicalDigest: digest, OperationID: "mutation-" + digest[7:31], ResultEventIDs: marshalEventIDs(eventIDs), ResultPayload: string(payload), ChangedRefs: string(changedJSON), AuthorizedScopeSnapshot: string(authorizedScope), ObservedAt: r.Authority.now()})
	})
	if err != nil {
		if resultRejected {
			return response, nil
		}
		return failureEnvelope(base, err), nil
	}
	return response, nil
}

func mutationScopeWorkIDs(scope map[string]any) []string {
	values, _ := scope["work_ids"].([]string)
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func mutationIsOverlapRecovery(tool, operation string, raw []byte) bool {
	if tool == "concord_work_relate" && (operation == "resolve_overlap" || operation == "supersede" || operation == "restore_superseded") {
		return true
	}
	if tool == "concord_work_transition" && operation == "lifecycle" {
		var input struct {
			Target string `json:"target"`
		}
		if json.Unmarshal(raw, &input) == nil {
			return input.Target == "completed" || input.Target == "cancelled"
		}
	}
	return false
}

func (r runtime) consumeApprovalTx(ctx context.Context, tx *store.Transaction, inv Invocation, grant Grant, check ApprovalCheck) (store.WorkflowActor, string, error) {
	var operator store.WorkflowActor
	if r.Envelope.HostApproval == nil {
		return operator, "", fmt.Errorf("signed host approval assertion is required")
	}
	var err error
	challenge, err := r.Authority.ValidateHostApprovalAssertionTx(ctx, tx, inv, *r.Envelope.HostApproval, check)
	if err != nil {
		return operator, "", err
	}
	approvalRef := check.ApprovalRef
	if challenge {
		approvalRef, err = r.Authority.CreateApprovalFromChallengeTx(ctx, tx, inv, check.ApprovalRef)
		if err != nil {
			return operator, "", err
		}
	}
	if err := r.Authority.ValidateAndConsumeApprovalTx(ctx, tx, approvalRef, check); err != nil {
		return operator, "", err
	}
	if check.RequireOperatorIdentity {
		operator, err = r.Authority.ApprovalAuthorityActorTx(ctx, tx, inv, approvalRef)
		if err != nil {
			return store.WorkflowActor{}, "", err
		}
	}
	return operator, approvalRef, nil
}

func boundedApprovalScope(scope map[string]any) map[string]any {
	out := make(map[string]any, len(scope))
	for key, value := range scope {
		if key != "product_id" && key != "product_ids" && key != "project_ids" && key != "work_ids" && key != "scope_version" {
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

func (r runtime) unlinkEffect(digest string, in unlinkMutationInput, preflightEndpoints []string, intents []NextIntent) mutationEffect {
	return func(ctx context.Context, tx *store.Transaction, grant Grant) (json.RawMessage, []string, []ChangedRef, error) {
		relationID, err := strconv.ParseInt(in.RelationID, 10, 64)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("invalid relation ID")
		}
		relation, err := store.RelationByIDTx(ctx, tx, relationID)
		if err != nil {
			return nil, nil, nil, err
		}
		from, to, kind := relation.FromWorkID, relation.ToWorkID, relation.Kind
		if len(preflightEndpoints) != 2 || from != preflightEndpoints[0] || to != preflightEndpoints[1] {
			return nil, nil, nil, newRuntimeFailure("version_conflict", "relation endpoints changed after scope preflight", "reread_relations", false)
		}
		byWork, err := store.ProductsForWorkIDsTx(ctx, tx, []string{from, to})
		if err != nil {
			return nil, nil, nil, err
		}
		for _, products := range byWork {
			for _, product := range products {
				if !contains(grant.ProductScope, product) || (r.Envelope.SelectedProductID != "" && product != r.Envelope.SelectedProductID && !containsCapability(grant.Capabilities, Capability("cross_scope"))) {
					return nil, nil, nil, newRuntimeFailure("unauthorized", "relation endpoint is outside authorized Product scope", "contact_operator", false)
				}
			}
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

type researchPackCreateMutation struct {
	OwnerWorkID    string                `json:"owner_work_id"`
	Revision       researchRevisionInput `json:"revision"`
	Freshness      string                `json:"freshness"`
	IdempotencyKey string                `json:"idempotency_key"`
}
type researchRevisionMutation struct {
	PackID          string                `json:"pack_id"`
	ExpectedVersion int64                 `json:"expected_version"`
	Revision        researchRevisionInput `json:"revision"`
	IdempotencyKey  string                `json:"idempotency_key"`
}
type researchFindingMutation struct {
	PackID          string               `json:"pack_id"`
	ExpectedVersion int64                `json:"expected_version"`
	Finding         researchFindingInput `json:"finding"`
	SourceIDs       []string             `json:"source_ids"`
	IdempotencyKey  string               `json:"idempotency_key"`
}
type researchSourceMutation struct {
	PackID          string              `json:"pack_id"`
	ExpectedVersion int64               `json:"expected_version"`
	Source          researchSourceInput `json:"source"`
	IdempotencyKey  string              `json:"idempotency_key"`
}
type researchFreshnessMutation struct {
	PackID          string `json:"pack_id"`
	ExpectedVersion int64  `json:"expected_version"`
	Freshness       string `json:"freshness"`
	Revision        int64  `json:"revision"`
	IdempotencyKey  string `json:"idempotency_key"`
}

func rawJSON(v any) json.RawMessage {
	if v == nil {
		return json.RawMessage("null")
	}
	out, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return out
}

func storeResearchRevision(in researchRevisionInput) store.ResearchRevisionInput {
	return store.ResearchRevisionInput{Question: in.Question, ScopeIn: rawJSON(in.ScopeIn), ScopeOut: rawJSON(in.ScopeOut), DoneWhen: rawJSON(in.DoneWhen), Method: in.Method}
}

func researchBindingDeclarations(in []researchBindingInput) []store.ResearchBindingDeclaration {
	if len(in) == 0 {
		return nil
	}
	out := make([]store.ResearchBindingDeclaration, 0, len(in))
	for _, b := range in {
		out = append(out, store.ResearchBindingDeclaration{PackID: b.PackID, Revision: b.Revision, UseRole: store.ResearchUseRole(b.UseRole), Required: b.Required})
	}
	return out
}

func messageIDFor(digest, recipient string) string {
	sum := sha256.Sum256([]byte(digest + ":" + recipient))
	return "msg:" + hex.EncodeToString(sum[:16])
}

func activeWorkIDsTx(ctx context.Context, tx *store.Transaction, productID string) ([]string, error) {
	return store.ActiveWorkIDsTx(ctx, tx, productID)
}
