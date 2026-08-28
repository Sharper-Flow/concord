package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/sharper-flow/concord/internal/portfolio"
	"github.com/sharper-flow/concord/internal/store"
)

// CallEnvelope is the hidden TS5 portion of an invoke request. The CLI accepts
// it only from trusted client code; model-facing input is validated separately.
type CallEnvelope struct {
	SchemaVersion       string                 `json:"schema_version"`
	RequestID           string                 `json:"request_id"`
	ClientRef           string                 `json:"client_ref"`
	PrincipalRef        string                 `json:"principal_ref"`
	SessionRef          string                 `json:"session_ref"`
	AgentRef            string                 `json:"agent_ref"`
	Directory           string                 `json:"directory"`
	Worktree            string                 `json:"worktree"`
	AmbientProjectID    string                 `json:"ambient_project_id"`
	SelectedProductID   string                 `json:"selected_product_id,omitempty"`
	ScopeVersion        string                 `json:"scope_version"`
	ManifestDigest      string                 `json:"manifest_digest"`
	HostAssertionDigest string                 `json:"host_assertion_digest,omitempty"`
	HostApproval        *HostApprovalAssertion `json:"host_approval_assertion,omitempty"`
}

type InvokeRequest struct {
	CallEnvelope json.RawMessage `json:"call_envelope"`
	Tool         string          `json:"tool"`
	Operation    string          `json:"operation"`
	Input        json.RawMessage `json:"input"`
}

func DecodeInvokeRequest(data []byte) (InvokeRequest, CallEnvelope, error) {
	if len(data) == 0 || len(data) > MaxEnvelopeBytes {
		return InvokeRequest{}, CallEnvelope{}, errors.New("invoke input exceeds 65536 bytes")
	}
	if err := validateUniqueJSON(data); err != nil {
		return InvokeRequest{}, CallEnvelope{}, err
	}
	var request InvokeRequest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&request); err != nil {
		return request, CallEnvelope{}, fmt.Errorf("decode invoke request: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return request, CallEnvelope{}, errors.New("invoke request contains trailing JSON")
	}
	if request.Tool == "" || request.Operation == "" || len(request.Input) == 0 || len(request.CallEnvelope) == 0 {
		return request, CallEnvelope{}, errors.New("invoke requires call_envelope, tool, operation, and input")
	}
	var env CallEnvelope
	dec = json.NewDecoder(bytes.NewReader(request.CallEnvelope))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&env); err != nil {
		return request, CallEnvelope{}, fmt.Errorf("decode call envelope: %w", err)
	}
	if err := dec.Decode(&trailing); err != io.EOF {
		return request, CallEnvelope{}, errors.New("call envelope contains trailing JSON")
	}
	if env.SchemaVersion == "" || env.RequestID == "" || env.ManifestDigest == "" {
		return request, CallEnvelope{}, errors.New("call envelope is missing schema_version, request_id, or manifest_digest")
	}
	return request, env, nil
}

type pageInput struct {
	Cursor *string `json:"cursor"`
	Limit  int     `json:"limit"`
}
type budgetInput struct {
	MaxBytes  int `json:"max_bytes"`
	MaxItems  int `json:"max_items"`
	MaxMillis int `json:"max_millis"`
	// RequestedSeconds is the CD-0038 D1 caller budget, parsed from the input
	// top level. SupportedSeconds is the operation's declared ceiling from the
	// contract registry, never caller-controlled. CeilingRefused marks
	// requested-above-supported; the refusal itself is minted by the caller's
	// admission point, because reads refuse at dispatch while mutations refuse
	// only after their idempotency lookup (CD-0038 D3).
	RequestedSeconds int  `json:"-"`
	SupportedSeconds int  `json:"-"`
	CeilingRefused   bool `json:"-"`
}
type productResolveInput struct {
	ProductID string      `json:"product_id"`
	ProjectID string      `json:"project_id"`
	Page      pageInput   `json:"page"`
	Budget    budgetInput `json:"budget"`
}
type productSnapshotInput struct {
	ProductID    string      `json:"product_id"`
	ProjectIDs   []string    `json:"project_ids"`
	PreviewLimit int         `json:"preview_limit"`
	Budget       budgetInput `json:"budget"`
}
type blockedSessionsInput struct {
	ProductID string      `json:"product_id"`
	Page      pageInput   `json:"page"`
	Budget    budgetInput `json:"budget"`
}

type productRowPortfolioInput struct {
	ProductID string                         `json:"product_id"`
	Page      pageInput                      `json:"page"`
	Budget    budgetInput                    `json:"budget"`
	Source    *store.ProductRowRelianceInput `json:"source"`
}
type workListInput struct {
	ProductID     string      `json:"product_id"`
	ProjectIDs    []string    `json:"project_ids"`
	WorkIDs       []string    `json:"work_ids"`
	Lifecycle     string      `json:"lifecycle"`
	Kind          string      `json:"kind"`
	TagIDs        []string    `json:"tag_ids"`
	PriorityMin   *int64      `json:"priority_min"`
	PriorityMax   *int64      `json:"priority_max"`
	Detail        string      `json:"detail"`
	TerminalSince *string     `json:"terminal_since"`
	Page          pageInput   `json:"page"`
	Budget        budgetInput `json:"budget"`
}
type workReadyInput struct {
	ProductID string      `json:"product_id"`
	ProjectID string      `json:"project_id"`
	Kind      string      `json:"kind"`
	Page      pageInput   `json:"page"`
	Budget    budgetInput `json:"budget"`
}
type workBlockedInput struct {
	ProductID string      `json:"product_id"`
	ProjectID string      `json:"project_id"`
	WorkID    string      `json:"work_id"`
	Kind      string      `json:"kind"`
	Depth     int         `json:"depth"`
	Page      pageInput   `json:"page"`
	Budget    budgetInput `json:"budget"`
}
type workScopeInput struct {
	ProductID string      `json:"product_id"`
	ProjectID string      `json:"project_id"`
	WorkID    string      `json:"work_id"`
	Page      pageInput   `json:"page"`
	OneOf     string      `json:"one_of"`
	Budget    budgetInput `json:"budget"`
}
type messagesInput struct {
	ProductID string      `json:"product_id"`
	WorkID    string      `json:"work_id"`
	Page      pageInput   `json:"page"`
	Budget    budgetInput `json:"budget"`
}

type resourceClaimsInput struct {
	ProductID   string      `json:"product_id"`
	ResourceKey string      `json:"resource_key"`
	Page        pageInput   `json:"page"`
	Budget      budgetInput `json:"budget"`
}

type researchReadInput struct {
	ProductID string    `json:"product_id"`
	PackID    string    `json:"pack_id"`
	WorkID    string    `json:"work_id"`
	Page      pageInput `json:"page"`
}

type historyInput struct {
	WorkID     string      `json:"work_id"`
	Direction  string      `json:"direction"`
	EventKinds []string    `json:"event_kinds"`
	Page       pageInput   `json:"page"`
	Budget     budgetInput `json:"budget"`
}
type observationReadInput struct {
	WorkID string    `json:"work_id"`
	Page   pageInput `json:"page"`
}
type externalObservationReadInput struct {
	WorkID string `json:"work_id"`
	Limit  int    `json:"limit"`
}
type continuityInput struct {
	WorkID string      `json:"work_id"`
	Page   pageInput   `json:"page"`
	Budget budgetInput `json:"budget"`
}
type relationInput struct {
	WorkID        string      `json:"work_id"`
	RelationKinds []string    `json:"relation_kinds"`
	Direction     string      `json:"direction"`
	Depth         int         `json:"depth"`
	Budget        budgetInput `json:"budget"`
}
type initiativeEntriesInput struct {
	InitiativeWorkID string      `json:"initiative_work_id"`
	Budget           budgetInput `json:"budget"`
}
type domainReadInput struct {
	ProductID string      `json:"product_id"`
	DomainID  string      `json:"domain_id"`
	Page      pageInput   `json:"page"`
	Budget    budgetInput `json:"budget"`
}
type knowledgeSearchInput struct {
	ProductID string   `json:"product_id"`
	ProjectID string   `json:"project_id"`
	DomainID  string   `json:"domain_id"`
	Kinds     []string `json:"kinds"`
	Tags      []string `json:"tags"`
	Text      string   `json:"text"`
	Since     *string  `json:"since"`
	Until     *string  `json:"until"`
	// AllowDegraded opts the caller in to CD-0008 D3 degraded enumeration: a
	// knowledge index behind the git head answers with authority "degraded" plus
	// omissions instead of failing closed. Default false keeps the fail-closed
	// path, so a caller never receives a silently incomplete answer.
	AllowDegraded bool        `json:"allow_degraded"`
	Page          pageInput   `json:"page"`
	Budget        budgetInput `json:"budget"`
}
type knowledgeResolveInput struct {
	WorkID      string `json:"work_id"`
	KnowledgeID string `json:"knowledge_id"`
}

type knowledgeUnprocessedInput struct {
	ProductID string    `json:"product_id"`
	ProjectID string    `json:"project_id"`
	Page      pageInput `json:"page"`
	Limit     int       `json:"limit"`
}

type runtime struct {
	Store           *store.Store
	Authority       *Service
	Registry        store.DefinitionRegistry
	Envelope        CallEnvelope
	Tool, Operation string
	Budget          budgetInput
	Reader          Authority
}

// Dispatch validates the generated input schema, revalidates TS5 authority,
// and routes both read and transaction-bound mutation operations through the
// generated contract surface.
func Dispatch(ctx context.Context, s *store.Store, authority *Service, request InvokeRequest, env CallEnvelope) (Envelope, error) {
	return DispatchWithRegistry(ctx, s, authority, request, env, store.BuiltinWorkflowRegistry())
}

// DispatchWithRegistry is the same authenticated agent boundary with an
// explicitly pinned definition registry. Production callers use Dispatch; the
// seam lets replay and availability tests prove a missing registry before any
// payload or grant mutation path is reached.
func DispatchWithRegistry(ctx context.Context, s *store.Store, authority *Service, request InvokeRequest, env CallEnvelope, registry store.DefinitionRegistry) (Envelope, error) {
	if registry == nil {
		registry = store.BuiltinWorkflowRegistry()
	}
	base := NewBase(env.RequestID, request.Tool, request.Operation)
	op, ok := ValidateContractOperation(request.Tool, request.Operation)
	if !ok {
		return base, errors.New("unsupported tool operation")
	}
	if op.ID == "concord_work_transition.workflow_action" {
		// Generated outer-shape validation rejects duplicate or missing action
		// fields. Semantic workflow payload validation remains below the registry
		// availability check.
		if err := ValidateOperationPayload(request.Tool, request.Operation, request.Input, false); err != nil {
			return coreError(base, "invalid_input", err.Error(), "reread_entities", false), nil
		}
		var strictAction actionMutationInput
		if err := decodeOperationInput(request.Input, &strictAction); err != nil {
			return coreError(base, "invalid_input", err.Error(), "reread_entities", false), nil
		}
		if len(strictAction.WorkID) < 2 || len(strictAction.WorkID) > 128 {
			return coreError(base, "invalid_input", "workflow action work_id is malformed", "reread_entities", false), nil
		}
		if len(strictAction.Fields) != 0 && bytes.Equal(bytes.TrimSpace(strictAction.Fields), []byte(`{}`)) {
			return coreError(base, "invalid_input", "workflow action fields cannot be empty", "reread_entities", false), nil
		}
		if s == nil || authority == nil {
			return coreError(base, "unreachable", "workflow authority is not available", "contact_operator", true), nil
		}
		// The workflow actor tuple and the idempotency partition both key on
		// principal_ref, which CD-0080 D1 derives rather than accepts. Identity
		// authorization therefore precedes the preflight; Product and Project
		// scope stay out of it, because scope is validated after the budget.
		actorInv := Invocation{ClientRef: env.ClientRef, SessionRef: env.SessionRef, AgentRef: env.AgentRef, Directory: env.Directory, Worktree: env.Worktree, ManifestDigest: env.ManifestDigest, HostAssertionDigest: env.HostAssertionDigest, RequiredCapability: op.Capability}
		actor, actorErr := authority.Authorize(ctx, actorInv)
		if actorErr != nil {
			return coreError(base, "unauthorized", actorErr.Error(), "contact_operator", false), nil
		}
		available, availabilityErr := store.WorkflowActionAvailableWithRegistry(ctx, s, registry, strictAction.WorkID)
		if availabilityErr != nil {
			return failureEnvelope(base, availabilityErr), nil
		}
		if !available {
			return coreError(base, "invalid_transition", "workflow action registry is unavailable", "reread_entities", false), nil
		}
		if err := preflightWorkflowActionRequestWithRegistry(ctx, s, request.Input, env, actor, registry); err != nil {
			return failureEnvelope(base, err), nil
		}
	} else if err := ValidateOperationPayload(request.Tool, request.Operation, request.Input, false); err != nil {
		return base, err
	}
	ctx, cancel, budget, budgetErr := applyBudget(ctx, op, request.Input)
	if budgetErr != nil {
		if budgetErr.kind == "invalid_input" {
			return coreError(base, "invalid_input", budgetErr.message, "none", false), nil
		}
		readRefusal := runtime{Tool: request.Tool, Operation: request.Operation, Budget: budget}
		return readRefusal.budgetRefusal(base, budgetErr.message), nil
	}
	defer cancel()
	// CD-0038 D3: reads have no idempotency identity, so ceiling admission for
	// a read happens here, immediately after strict input validation. A
	// mutation must not refuse here — its idempotency lookup precedes budget
	// admission, and the mutation paths enforce the ceiling themselves.
	if op.Kind == OperationRead && budget.CeilingRefused {
		readRefusal := runtime{Tool: request.Tool, Operation: request.Operation, Budget: budget}
		return readRefusal.budgetRefusal(base, fmt.Sprintf("requested_budget_seconds %d exceeds supported %d", budget.RequestedSeconds, budget.SupportedSeconds)), nil
	}
	if s == nil || authority == nil {
		return coreError(base, "unreachable", "authority is not available", "contact_operator", true), nil
	}
	inv := Invocation{ClientRef: env.ClientRef, PrincipalRef: env.PrincipalRef, SessionRef: env.SessionRef, AgentRef: env.AgentRef, Directory: env.Directory, Worktree: env.Worktree, ManifestDigest: env.ManifestDigest, HostAssertionDigest: env.HostAssertionDigest, RequiredCapability: op.Capability, ProductID: env.SelectedProductID}
	if op.Kind != OperationRead && op.ID != "concord_work_transition.workflow_action" {
		identity, identityExtractErr := extractMutationWorkIdentity(request.Input)
		if identityExtractErr != nil {
			return base, identityExtractErr
		}
		if identity.RelationID != "" {
			endpoints, endpointErr := s.RelationEndpoints(ctx, identity.RelationID)
			if endpointErr != nil {
				return failureEnvelope(base, endpointErr), nil
			}
			identity.WorkIDs = append(identity.WorkIDs, endpoints...)
		}
		identityInv := inv
		identityInv.ProductID, identityInv.ProjectID = "", ""
		identityGrant, identityErr := authority.Authorize(ctx, identityInv)
		if identityErr != nil {
			return coreError(base, "unauthorized", identityErr.Error(), "contact_operator", false), nil
		}
		r := runtime{Store: s, Authority: authority, Envelope: env, Tool: request.Tool, Operation: request.Operation, Budget: budget, Reader: identityGrant}
		if replay, handled, replayErr := r.replayMutationBeforeScope(ctx, base, request.Input, identityGrant, op); replayErr != nil || handled {
			if replayErr != nil {
				return failureEnvelope(base, replayErr), nil
			}
			return replay, nil
		}
	}
	if env.ScopeVersion == "" {
		return coreError(base, "stale_context", "scope_version is required for every invocation", "refresh_context", false), nil
	}
	grant, err := authority.Authorize(ctx, inv)
	if err != nil {
		return coreError(base, "unauthorized", err.Error(), "contact_operator", false), nil
	}
	if resolvedProject, ok := grant.ScopeSnapshot["project_id"].(string); ok && resolvedProject != env.AmbientProjectID {
		return coreError(base, "stale_context", "ambient Project no longer matches the signed worktree", "refresh_context", false), nil
	}
	if err := validateRuntimeScope(ctx, s, env, grant, op.Kind); err != nil {
		var stale *runtimeFailure
		if errors.As(err, &stale) && stale.Refreshable && op.Kind == OperationRead && equalStrings(stale.Candidates, grant.CandidateProducts) {
			base.Warnings = append(base.Warnings, Notice{Kind: "context_refreshed"})
			env.ScopeVersion = stale.CurrentScopeVersion
		} else {
			return failureEnvelope(base, err), nil
		}
	}
	if err := validateRequestedScope(ctx, s, env, grant, request); err != nil {
		return failureEnvelope(base, err), nil
	}
	r := runtime{Store: s, Authority: authority, Registry: registry, Envelope: env, Tool: request.Tool, Operation: request.Operation, Budget: budget, Reader: grant}
	if op.Kind != OperationRead {
		return r.mutate(ctx, base, request.Input, grant, op)
	}
	return r.read(ctx, base, request.Input, op.QueryID)
}

// budgetFailure carries the distinction applyBudget must report: a
// budget_refused finding needs the typed ceiling and adjust_budget recovery,
// while a malformed or self-contradictory budget is invalid_input. Both are
// admission outcomes; only one is a refusal. Callers read the typed fields —
// it deliberately implements no interface.
type budgetFailure struct {
	kind    string
	message string
}

func applyBudget(ctx context.Context, op ContractOperation, raw []byte) (context.Context, context.CancelFunc, budgetInput, *budgetFailure) {
	var envelope struct {
		Budget           budgetInput `json:"budget"`
		RequestedSeconds int         `json:"requested_budget_seconds"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return ctx, func() {}, budgetInput{}, &budgetFailure{kind: "invalid_input", message: "budget fields are not valid"}
	}
	budget := envelope.Budget
	budget.RequestedSeconds = envelope.RequestedSeconds
	budget.SupportedSeconds = op.SupportedBudgetSeconds
	if budget.RequestedSeconds < 0 {
		return ctx, func() {}, budget, &budgetFailure{kind: "invalid_input", message: "requested_budget_seconds must be at least 1"}
	}
	if budget.MaxMillis > 300000 {
		return ctx, func() {}, budget, &budgetFailure{kind: "budget_refused", message: "max_millis exceeds supported bound"}
	}
	// CD-0038 D6: during the compatibility window both denominations may be
	// sent, but only if they express one exact duration. A preference or
	// rounding rule would silently pick a budget the caller did not request.
	if budget.RequestedSeconds > 0 && budget.MaxMillis > 0 && budget.RequestedSeconds*1000 != budget.MaxMillis {
		return ctx, func() {}, budget, &budgetFailure{kind: "invalid_input", message: "requested_budget_seconds and budget.max_millis must express the same duration"}
	}
	if budget.RequestedSeconds > budget.SupportedSeconds {
		budget.CeilingRefused = true
	}
	if budget.MaxMillis > 0 {
		child, cancel := context.WithTimeout(ctx, time.Duration(budget.MaxMillis)*time.Millisecond)
		return child, cancel, budget, nil
	}
	if budget.RequestedSeconds > 0 {
		child, cancel := context.WithTimeout(ctx, time.Duration(budget.RequestedSeconds)*time.Second)
		return child, cancel, budget, nil
	}
	return ctx, func() {}, budget, nil
}

// budgetRefusal mints the one refusal envelope every budget_refused site uses,
// so the typed ceiling cannot be forgotten at an emission point. The ceiling
// resolves from the parsed budget, and from the contract registry when the
// budget was constructed without one — a caller that never went through
// applyBudget still owes the caller its ceiling.
func (r runtime) budgetRefusal(base Envelope, message string) Envelope {
	supported := r.Budget.SupportedSeconds
	if supported < 1 {
		if op, ok := ValidateContractOperation(r.Tool, r.Operation); ok {
			supported = op.SupportedBudgetSeconds
		}
	}
	out := coreError(base, "budget_refused", message, "adjust_budget", false)
	out.Error.SupportedBudgetSeconds = supported
	return out
}

func (r runtime) boundedLimit(limit int) int {
	if r.Budget.MaxItems > 0 && (limit == 0 || limit > r.Budget.MaxItems) {
		return r.Budget.MaxItems
	}
	return limit
}

func (r runtime) boundedPreview(limit int) int {
	if r.Budget.MaxItems > 0 && (limit == 0 || limit > r.Budget.MaxItems) {
		return r.Budget.MaxItems
	}
	return limit
}

func validateRequestedScope(ctx context.Context, s *store.Store, env CallEnvelope, grant Authority, request InvokeRequest) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(request.Input, &fields); err != nil {
		return err
	}
	// Q10 owns archived-row lookup, Product visibility, and historical locator
	// proof. Do not apply live/frozen scope joins here: home-scoped manifest rows
	// intentionally have no archived Product rows, and compacted work may no
	// longer have a live work membership.
	if request.Tool == "concord_knowledge" && request.Operation == "resolve_note" {
		return nil
	}
	identity, err := extractMutationWorkIdentity(request.Input)
	if err != nil {
		return err
	}
	workIDs := identity.WorkIDs
	if identity.RelationID != "" {
		endpoints, endpointErr := s.RelationEndpoints(ctx, identity.RelationID)
		if endpointErr != nil {
			return endpointErr
		}
		workIDs = append(workIDs, endpoints...)
	}
	if len(workIDs) > 0 {
		scopes, err := s.ProductsForWorkIDs(ctx, workIDs)
		if err != nil {
			return err
		}
		for _, id := range workIDs {
			products, ok := scopes[id]
			if !ok {
				return newRuntimeFailure("unknown_scope", "work reference is not in Product scope", "reread_entities", false)
			}
			if !scopeIntersects(products, grant.ProductScope) {
				return newRuntimeFailure("unauthorized", "work reference is outside authorized Product scope", "contact_operator", false)
			}
			if env.SelectedProductID != "" && !contains(products, env.SelectedProductID) && !containsCapability(grant.Capabilities, Capability("cross_scope")) {
				return newRuntimeFailure("unauthorized", "cross-Product mutation requires cross_scope capability", "contact_operator", false)
			}
		}
	}
	if raw, ok := fields["knowledge_id"]; ok {
		var id string
		if json.Unmarshal(raw, &id) == nil && id != "" {
			products, err := s.ProductsForKnowledgeID(ctx, id)
			if err != nil {
				return err
			}
			if len(products) == 0 {
				return newRuntimeFailure("unknown_scope", "knowledge reference is not in Product scope", "reread_entities", false)
			}
			if !scopeIntersects(products, grant.ProductScope) || env.SelectedProductID != "" && !contains(products, env.SelectedProductID) {
				return newRuntimeFailure("unauthorized", "knowledge reference is outside authorized Product scope", "contact_operator", false)
			}
		}
	}
	if raw, ok := fields["product_id"]; ok {
		var product string
		if json.Unmarshal(raw, &product) == nil && product != "" && !contains(grant.ProductScope, product) {
			return newRuntimeFailure("unauthorized", "Product is outside grant scope", "contact_operator", false)
		}
	}
	if raw, ok := fields["project_ids"]; ok {
		var projectIDs []string
		if json.Unmarshal(raw, &projectIDs) == nil && len(projectIDs) > 0 {
			productsByProject, err := s.ProductsForProjectIDs(ctx, projectIDs)
			if err != nil {
				return err
			}
			for _, project := range projectIDs {
				products := productsByProject[project]
				if len(products) == 0 {
					return newRuntimeFailure("unknown_scope", "Project is not in Product scope", "reread_entities", false)
				}
				if !scopeIntersects(products, grant.ProductScope) {
					return newRuntimeFailure("unauthorized", "Project is outside authorized Product scope", "contact_operator", false)
				}
				if env.SelectedProductID != "" && !contains(products, env.SelectedProductID) && !containsCapability(grant.Capabilities, Capability("cross_scope")) {
					return newRuntimeFailure("unauthorized", "cross-Product mutation requires cross_scope capability", "contact_operator", false)
				}
			}
		}
	}
	return nil
}

type mutationWorkIdentity struct {
	WorkIDs    []string
	RelationID string
}

func extractMutationWorkIdentity(raw []byte) (mutationWorkIdentity, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return mutationWorkIdentity{}, err
	}
	var result mutationWorkIdentity
	for _, field := range []string{"work_id", "initiative_work_id", "child_work_id", "from_work_id", "to_work_id", "predecessor_id", "successor_id", "replacement_successor_id"} {
		if value, ok := fields[field]; ok {
			var id string
			if json.Unmarshal(value, &id) == nil && id != "" {
				result.WorkIDs = append(result.WorkIDs, id)
			}
		}
	}
	if value, ok := fields["work_ids"]; ok {
		var ids []string
		if err := json.Unmarshal(value, &ids); err != nil {
			return mutationWorkIdentity{}, err
		}
		result.WorkIDs = append(result.WorkIDs, ids...)
	}
	if value, ok := fields["relation_id"]; ok {
		_ = json.Unmarshal(value, &result.RelationID)
	}
	return result, nil
}
func scopeIntersects(left, right []string) bool {
	for _, a := range left {
		if contains(right, a) {
			return true
		}
	}
	return false
}

// Invoke is the byte-oriented core boundary. cmd/concord's invoke verb and the
// agent boundary corpus both enter here, so the corpus exercises the same
// composition production runs rather than a test-only shortcut (issue #450).
//
// It decodes strictly, dispatches, and shapes a dispatch failure into the typed
// envelope the caller receives. Only a decode failure is returned as an error,
// because only a decode failure means no envelope can be addressed.
func Invoke(ctx context.Context, s *store.Store, authority *Service, data []byte) (Envelope, error) {
	request, env, err := DecodeInvokeRequest(data)
	if err != nil {
		return Envelope{}, err
	}
	response, dispatchErr := Dispatch(ctx, s, authority, request, env)
	return shapeInvokeFailure(response, dispatchErr, request, env), nil
}

// InvokeWithRegistry is Invoke with an injected definition registry. Production
// never needs it: Dispatch resolves the builtin registry, and the injection
// point exists so the workflow corpus boundary runner can drive a scenario
// registry through the real boundary.
func InvokeWithRegistry(ctx context.Context, s *store.Store, authority *Service, data []byte, registry store.DefinitionRegistry) (Envelope, error) {
	request, env, err := DecodeInvokeRequest(data)
	if err != nil {
		return Envelope{}, err
	}
	response, dispatchErr := DispatchWithRegistry(ctx, s, authority, request, env, registry)
	return shapeInvokeFailure(response, dispatchErr, request, env), nil
}

// shapeInvokeFailure converts a dispatch error into the typed envelope an
// operator receives. A dispatch failure is a product outcome, not a transport
// fault, so it must not reach the caller as a bare Go error.
func shapeInvokeFailure(response Envelope, dispatchErr error, request InvokeRequest, env CallEnvelope) Envelope {
	if dispatchErr == nil {
		return response
	}
	base := NewBase(env.RequestID, request.Tool, request.Operation)
	return NewCoreError(base, TypedError{Kind: "invalid_input", RetrySafe: false, RecoveryAction: RecoveryAction{Kind: "restart_query"}, EffectState: EffectNone, Message: dispatchErr.Error()})
}

func validateRuntimeScope(ctx context.Context, s *store.Store, env CallEnvelope, grant Authority, kind OperationKind) error {
	if env.AmbientProjectID == "" {
		return newRuntimeFailure("unknown_scope", "ambient Project is required", "resolve_ambiguity", false)
	}
	if !contains(grant.ProjectScope, env.AmbientProjectID) {
		return newRuntimeFailure("unauthorized", "Project is outside grant scope", "contact_operator", false)
	}
	version, candidates, err := s.ScopeVersion(ctx, env.AmbientProjectID)
	if err != nil {
		return err
	}
	if env.ScopeVersion != "" && env.ScopeVersion != version {
		f := newRuntimeFailure("stale_context", "scope version is stale", "refresh_context", kind == OperationRead)
		f.Candidates = candidates
		f.CurrentScopeVersion = version
		f.Refreshable = true
		return f
	}
	if env.SelectedProductID == "" {
		if len(candidates) != 1 {
			f := newRuntimeFailure("ambiguous_scope", "Project belongs to multiple Products", "resolve_ambiguity", false)
			f.Candidates = candidates
			return f
		}
		return nil
	}
	// TS5 §3 separates a context failure from an authorization failure, and the
	// two carry different recovery contracts. A selected Product the ambient
	// Project no longer resolves to is context the caller can re-resolve itself;
	// a Product outside the grant is not. Reporting the first as unauthorized
	// escalates a self-recoverable condition to the operator.
	if !contains(candidates, env.SelectedProductID) {
		if len(candidates) > 1 {
			f := newRuntimeFailure("ambiguous_scope", "selected Product no longer owns the ambient Project", "resolve_ambiguity", false)
			f.Candidates = candidates
			return f
		}
		f := newRuntimeFailure("stale_context", "selected Product no longer owns the ambient Project", "refresh_context", false)
		f.Candidates = candidates
		f.CurrentScopeVersion = version
		return f
	}
	if !contains(grant.ProductScope, env.SelectedProductID) {
		return newRuntimeFailure("unauthorized", "selected Product is outside grant scope", "contact_operator", false)
	}
	return nil
}

type runtimeFailure struct {
	kind, message, recovery string
	retry                   bool
	Candidates              []string
	CurrentScopeVersion     string
	// Refreshable marks the one stale_context cause a read may proceed through
	// under TS5 §3: the scope version moved while the resolved scope did not.
	// Every other stale_context describes scope the caller must actually change,
	// so refreshing the version alone would carry an invalid selection forward.
	Refreshable bool
}

func (f *runtimeFailure) Error() string { return f.message }
func newRuntimeFailure(kind, message, recovery string, retry bool) *runtimeFailure {
	return &runtimeFailure{kind: kind, message: message, recovery: recovery, retry: retry}
}
func failureEnvelope(base Envelope, err error) Envelope {
	if errors.Is(err, context.DeadlineExceeded) {
		// CD-0038 D5: expiry before any durable effect claims no effect. The
		// store rolls an open transaction back on error, so an in-process
		// deadline reaching this point means no commit happened. External
		// effects that outlive cancellation are the native-run surface's
		// problem (CD-0039), not this envelope's.
		return coreError(base, "timeout", "operation budget expired", "retry_same_request", true)
	}
	var f *runtimeFailure
	if errors.As(err, &f) {
		out := coreError(base, f.kind, f.message, f.recovery, f.retry)
		out.Error.Candidates = f.Candidates
		return out
	}
	var sf *store.Failure
	if errors.As(err, &sf) {
		kind := mapFailureKind(sf.Kind)
		out := coreError(base, kind, sf.Detail, publicRecovery(kind, sf.RecoveryAction), sf.RetrySafe)
		// Carry typed current-version carriers into the agent envelope so
		// callers can recover the live projection version structurally without
		// having to parse the human detail string. Mirrors the same path for
		// typed violations: see D5.
		if len(sf.CurrentVersions) > 0 {
			for _, current := range sf.CurrentVersions {
				if !current.Exists {
					continue
				}
				out.Error.CurrentVersions = append(out.Error.CurrentVersions, ChangedRef{EntityKind: string(current.SubjectType), ID: current.SubjectID, Version: strconv.FormatInt(current.Version, 10)})
			}
		}
		if len(sf.Violations) > 0 {
			out.Error.Violations = append(out.Error.Violations, sf.Violations...)
		}
		if sf.StaleLawRevision != nil {
			out.Error.StaleLawRevision = &StaleLawRevision{OldLawID: sf.StaleLawRevision.OldLawID, OldContentHash: sf.StaleLawRevision.OldContentHash, AcceptedSuccessorLawID: sf.StaleLawRevision.AcceptedSuccessorLawID, AcceptedSuccessorContentHash: sf.StaleLawRevision.AcceptedSuccessorContentHash, RecoveryActions: append([]string(nil), sf.StaleLawRevision.RecoveryActions...)}
		}
		if sf.DomainOverlap != nil {
			out.Error.DomainOverlap = &DomainOverlap{TotalOverlaps: sf.DomainOverlap.TotalOverlaps, ReturnedOverlaps: sf.DomainOverlap.ReturnedOverlaps, Truncated: sf.DomainOverlap.Truncated}
			for _, overlap := range sf.DomainOverlap.Overlaps {
				converted := DomainOverlapDetail{ProductID: overlap.ProductID, FromWorkID: overlap.FromWorkID, ToWorkID: overlap.ToWorkID, FromContractVersion: overlap.FromContractVersion, ToContractVersion: overlap.ToContractVersion, SharedAffectedDomainIDs: append([]string(nil), overlap.SharedAffectedDomainIDs...), SharedLawIDs: append([]string(nil), overlap.SharedLawIDs...), SharedDomainModifications: append([]string(nil), overlap.SharedDomainModifications...), OverlapClasses: append([]string(nil), overlap.OverlapClasses...), ResolutionState: overlap.ResolutionState, ResolutionKind: overlap.ResolutionKind, RecoveryActions: append([]string(nil), overlap.RecoveryActions...), SharedAffectedDomainCount: overlap.SharedAffectedDomainCount, SharedLawCount: overlap.SharedLawCount, SharedDomainModificationCount: overlap.SharedDomainModificationCount, SharedRelationTupleCount: overlap.SharedRelationTupleCount, DetailTruncated: overlap.DetailTruncated}
				for _, tuple := range overlap.SharedRelationTuples {
					converted.SharedRelationTuples = append(converted.SharedRelationTuples, DomainOverlapRelationTuple{SourceDomainID: tuple.SourceDomainID, Kind: tuple.Kind, TargetDomainID: tuple.TargetDomainID})
				}
				out.Error.DomainOverlap.Overlaps = append(out.Error.DomainOverlap.Overlaps, converted)
			}
		}
		return out
	}
	return coreError(base, "internal_error", err.Error(), "contact_operator", false)
}
func coreError(base Envelope, kind, message, recovery string, retry bool) Envelope {
	base.Authority = AuthorityAuthoritative
	base.Outcome = OutcomeError
	base.Error = &TypedError{Kind: kind, RetrySafe: retry, RecoveryAction: RecoveryAction{Kind: recovery}, EffectState: EffectNone, Message: message}
	if _, err := base.Encode(); err == nil {
		return base
	}
	// Error delivery must remain possible when the rejected success inherited
	// optional fields large enough to exceed the envelope cap. Rebuild the base
	// from its bounded identity only when preserving metadata is undeliverable.
	// The typed error is the only authoritative response to a rejected result.
	errorBase := NewBase(base.RequestID, base.Tool, base.Operation)
	errorBase.ResolvedScope = base.ResolvedScope
	errorBase.Authority = AuthorityAuthoritative
	errorBase.Outcome = OutcomeError
	errorBase.Error = &TypedError{Kind: kind, RetrySafe: retry, RecoveryAction: RecoveryAction{Kind: recovery}, EffectState: EffectNone, Message: message}
	if _, err := errorBase.Encode(); err != nil {
		// Scope is useful when it remains deliverable, but never at the expense
		// of the limit error itself crossing the agent boundary.
		errorBase.ResolvedScope = nil
	}
	return errorBase
}

// governingConflictEnvelope refuses a capture that does not cover the governing
// requirements its target scope declares (CD-0035 D1/D3). The omitted
// requirements are named in the typed violations rather than described in the
// message, so an agent recovers without parsing prose.
func governingConflictEnvelope(base Envelope, missing []string) Envelope {
	base.Authority = AuthorityAuthoritative
	base.Outcome = OutcomeError
	base.Error = &TypedError{
		Kind:           "invariant_violation",
		RetrySafe:      false,
		RecoveryAction: RecoveryAction{Kind: "contact_operator"},
		EffectState:    EffectNone,
		Message:        "capture does not carry the governing requirements accepted for this scope",
		Violations:     missing,
		Options:        GoverningConflictOptions,
	}
	if _, err := base.Encode(); err == nil {
		return base
	}
	errorBase := NewBase(base.RequestID, base.Tool, base.Operation)
	errorBase.Authority = AuthorityAuthoritative
	errorBase.Outcome = OutcomeError
	errorBase.Error = base.Error
	return errorBase
}

func mapFailureKind(kind store.FailureKind) string {
	switch kind {
	case store.KindUnavailable:
		return "unreachable"
	case store.KindUnknownScope:
		return "unknown_scope"
	case store.KindDomainRegistryAbsent, store.KindUnknownDomain:
		return "unknown_scope"
	case store.KindProjectionNotFound:
		return "unknown_scope"
	case store.KindAmbiguousScope:
		return "ambiguous_scope"
	case store.KindVersionConflict:
		return "version_conflict"
	case store.KindUnauthorized:
		return "unauthorized"
	case store.KindOutcomeMismatch:
		return "outcome_mismatch"
	case store.KindInvalidDefinition, store.KindDefinitionVersionConflict, store.KindDefinitionVersionNotMonotonic, store.KindDefinitionDigestMismatch, store.KindDefinitionActionOrStepUnknown:
		return "invariant_violation"
	case store.KindInvariantViolation, store.KindSchemaUnsupported:
		return "invariant_violation"
	case store.KindUnsupportedPayloadVersion:
		return "invariant_violation"
	case store.KindIllegalLifecycleTransition:
		return "invalid_transition"
	case store.KindInvalidRelation, store.KindCycleDetected, store.KindRelationConflict, store.KindRelationNotFound, store.KindRelationContractViolation, store.KindSupersessionTargetAlreadySuperseded, store.KindSupersessionSecondSuccessor:
		return "invalid_relation"
	case store.KindInitiativeScopeViolation:
		return "invariant_violation"
	case store.KindInitiativeEntryConflict:
		return "invalid_relation"
	case store.KindMembershipInvariant, store.KindMembershipConflict:
		return "invariant_violation"
	case store.KindInvalidNoteProof, store.KindKnowledgeMissing:
		return "invalid_input"
	case store.KindIdempotencyConflict:
		return "idempotency_conflict"
	case store.KindInvalidCursor:
		return "invalid_cursor"
	case store.KindInvalidFilter, store.KindInvalidPayload:
		return "invalid_input"
	case store.KindInvalidOperation:
		return "invalid_input"
	case store.KindLimitExceeded:
		return "limit_exceeded"
	case store.KindStaleRequiresReview:
		return "stale_requires_review"
	case store.KindUnreachable, store.KindGitUnreachable:
		return "unreachable"
	case store.KindIndexDegraded:
		return "degraded_not_allowed"
	case store.KindResearchConsumerBlocked:
		return "stale_requires_review"
	case store.KindStaleLawRevision:
		return "stale_law_revision"
	case store.KindDomainOverlap:
		return "domain_overlap"
	default:
		return "internal_error"
	}
}

func publicRecovery(kind, proposed string) string {
	allowed := map[string]bool{"none": true, "retry_same_request": true, "refresh_context": true, "reread_entities": true, "request_approval": true, "provide_evidence": true, "reduce_limit": true, "use_next_cursor": true, "restart_query": true, "adjust_budget": true, "reconcile_operation": true, "resolve_ambiguity": true, "contact_operator": true}
	if allowed[proposed] {
		return proposed
	}
	switch kind {
	case "unauthorized", "outcome_mismatch", "unreachable", "internal_error":
		return "contact_operator"
	case "approval_required", "approval_invalid":
		return "request_approval"
	case "stale_law_revision":
		return "request_approval"
	case "domain_overlap":
		return "request_approval"
	case "version_conflict", "invalid_transition", "invalid_relation", "invariant_violation", "invalid_input":
		return "reread_entities"
	default:
		return "contact_operator"
	}
}

func decodeStrict(data []byte, value any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

// decodeOperationInput is the strict decoder for operation inputs. CD-0038 D1
// supplies requested_budget_seconds to every operation through one shared
// schema definition rather than per-operation restatement, and the budget
// layer parses and enforces it before any typed input decode. Stripping it
// here — after the canonical digest was taken over the raw input, which is
// what makes changing the budget change the request — keeps every typed input
// struct closed without 53 copies of the same field.
func decodeOperationInput(data []byte, value any) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		// Not an object — the shared domain field cannot be present, and the
		// caller may be decoding a legitimately non-object payload such as a
		// workflow-action field list. The strict decoder owns the verdict.
		return decodeStrict(data, value)
	}
	if _, shared := fields["requested_budget_seconds"]; !shared {
		return decodeStrict(data, value)
	}
	delete(fields, "requested_budget_seconds")
	core, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	return decodeStrict(core, value)
}
func cursorValue(page pageInput) string {
	if page.Cursor == nil {
		return ""
	}
	return *page.Cursor
}

func (r runtime) read(ctx context.Context, base Envelope, input []byte, queryID string) (Envelope, error) {
	switch r.Tool + "." + r.Operation {
	case "concord_product_view.resolve":
		var in productResolveInput
		if err := decodeOperationInput(input, &in); err != nil {
			return base, err
		}
		if in.ProductID == "" && in.ProjectID == "" {
			in.ProjectID = r.Envelope.AmbientProjectID
		}
		bindingInput := in
		bindingInput.Page.Cursor = nil
		binding, _ := json.Marshal(bindingInput)
		inner, err := r.unwrapCursor(ctx, cursorValue(in.Page), string(binding), "summary")
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		q, err := r.Store.QueryQ1(ctx, store.Q1Request{Product: in.ProductID, Project: in.ProjectID, Limit: r.boundedLimit(in.Page.Limit), Cursor: inner})
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		response, err := r.q1(base, q)
		if err != nil {
			return response, err
		}
		return r.wrapCursor(ctx, response, inner, string(binding), "summary")
	case "concord_product_view.snapshot":
		var in productSnapshotInput
		if err := decodeOperationInput(input, &in); err != nil {
			return base, err
		}
		if in.ProductID == "" {
			in.ProductID = r.Envelope.SelectedProductID
		}
		q, err := r.Store.QueryQ2(ctx, store.Q2Request{Product: in.ProductID, ProjectIDs: in.ProjectIDs, PreviewLimit: r.boundedPreview(in.PreviewLimit)})
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		return r.q2(base, q)
	case "concord_product_view.portfolio":
		var in productRowPortfolioInput
		if err := decodeOperationInput(input, &in); err != nil {
			return base, err
		}
		q, err := portfolio.Read(ctx, r.Store, store.ProductRowRequest{Product: in.ProductID, Limit: r.boundedLimit(in.Page.Limit), Cursor: cursorValue(in.Page), Source: in.Source})
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		return r.productRows(base, q)
	case "concord_product_view.blocked_sessions":
		var in blockedSessionsInput
		if err := decodeOperationInput(input, &in); err != nil {
			return base, err
		}
		if in.ProductID == "" {
			in.ProductID = r.Envelope.SelectedProductID
		}
		products := []string{}
		if in.ProductID != "" {
			products = append(products, in.ProductID)
		}
		result, err := r.Store.BlockedSessions(ctx, time.Now().UTC(), products, r.boundedLimit(in.Page.Limit))
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		return r.resultEnvelope(base, result.ResultMeta, r.scope(result.ResultMeta), map[string]any{"sessions": result.Sessions})
	case "concord_work_browse.list":
		var in workListInput
		if err := decodeOperationInput(input, &in); err != nil {
			return base, err
		}
		if in.ProductID == "" {
			in.ProductID = r.Envelope.SelectedProductID
		}
		bindingInput := in
		bindingInput.Page.Cursor = nil
		binding, _ := json.Marshal(bindingInput)
		inner, err := r.unwrapCursor(ctx, cursorValue(in.Page), string(binding), "summary")
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		req := store.Q3Request{Product: in.ProductID, LifecycleStates: nonEmpty(in.Lifecycle), Limit: r.boundedLimit(in.Page.Limit), Cursor: inner, Kind: in.Kind, ProjectIDs: in.ProjectIDs, WorkIDs: in.WorkIDs, TagIDs: in.TagIDs, PriorityMin: in.PriorityMin, PriorityMax: in.PriorityMax, TerminalSince: in.TerminalSince, Detail: in.Detail}
		q, err := r.Store.QueryQ3(ctx, req)
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		response, err := r.q3(base, q)
		if err != nil {
			return response, err
		}
		return r.wrapCursor(ctx, response, inner, string(binding), "summary")
	case "concord_work_browse.ready":
		var in workReadyInput
		if err := decodeOperationInput(input, &in); err != nil {
			return base, err
		}
		bindingInput := in
		bindingInput.Page.Cursor = nil
		binding, _ := json.Marshal(bindingInput)
		inner, err := r.unwrapCursor(ctx, cursorValue(in.Page), string(binding), "summary")
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		q, err := r.Store.QueryQ5(ctx, store.Q5Request{Product: in.ProductID, Project: in.ProjectID, Kind: in.Kind, Limit: r.boundedLimit(in.Page.Limit), Cursor: inner})
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		response, err := r.q5(base, q)
		if err != nil {
			return response, err
		}
		return r.wrapCursor(ctx, response, inner, string(binding), "summary")
	case "concord_work_browse.blocked":
		var in workBlockedInput
		if err := decodeOperationInput(input, &in); err != nil {
			return base, err
		}
		bindingInput := in
		bindingInput.Page.Cursor = nil
		binding, _ := json.Marshal(bindingInput)
		inner, err := r.unwrapCursor(ctx, cursorValue(in.Page), string(binding), "summary")
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		q, err := r.Store.QueryQ4(ctx, store.Q4Request{Product: in.ProductID, Project: in.ProjectID, Work: in.WorkID, Kind: in.Kind, Depth: in.Depth, Limit: r.boundedLimit(in.Page.Limit), Cursor: inner})
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		response, err := r.q4(base, q)
		if err != nil {
			return response, err
		}
		return r.wrapCursor(ctx, response, inner, string(binding), "summary")
	case "concord_work_browse.scope":
		var in workScopeInput
		if err := decodeOperationInput(input, &in); err != nil {
			return base, err
		}
		bindingInput := in
		bindingInput.Page.Cursor = nil
		binding, _ := json.Marshal(bindingInput)
		inner, err := r.unwrapCursor(ctx, cursorValue(in.Page), string(binding), "summary")
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		q, err := r.Store.QueryQ6(ctx, store.Q6Request{Product: in.ProductID, Project: in.ProjectID, Work: in.WorkID, Limit: r.boundedLimit(in.Page.Limit), Cursor: inner})
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		response, err := r.q6(base, q)
		if err != nil {
			return response, err
		}
		return r.wrapCursor(ctx, response, inner, string(binding), "summary")
	case "concord_work_browse.resource_claims":
		var in resourceClaimsInput
		if err := decodeOperationInput(input, &in); err != nil {
			return base, err
		}
		if in.ProductID == "" {
			in.ProductID = r.Envelope.SelectedProductID
		}
		claims, err := r.Store.ResourceClaims(ctx, in.ResourceKey, in.ProductID, r.boundedLimit(in.Page.Limit))
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		meta := store.ResultMeta{QueryID: "PM1.Q13", ContractVersion: "PM1/1.0", ResolvedScope: store.ResolvedScope{ProductID: in.ProductID}, Authority: "authoritative", Freshness: store.Freshness{ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}, OrderingKeys: []string{"claimed_at"}}
		return r.resultEnvelope(base, meta, r.scope(meta), map[string]any{"claims": claims})
	case "concord_work_browse.messages":
		var in messagesInput
		if err := decodeOperationInput(input, &in); err != nil {
			return base, err
		}
		messages, err := r.Store.MessagesForWork(ctx, in.WorkID, r.boundedLimit(in.Page.Limit))
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		meta := store.ResultMeta{QueryID: "PM1.Q14", ContractVersion: "PM1/1.0", ResolvedScope: store.ResolvedScope{WorkID: in.WorkID}, Authority: "authoritative", Freshness: store.Freshness{ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}, OrderingKeys: []string{"sent_at"}}
		return r.resultEnvelope(base, meta, r.scope(meta), map[string]any{"messages": messages})
	case "concord_work_trace.history":
		var in historyInput
		if err := decodeOperationInput(input, &in); err != nil {
			return base, err
		}
		bindingInput := in
		bindingInput.Page.Cursor = nil
		binding, _ := json.Marshal(bindingInput)
		inner, err := r.unwrapCursor(ctx, cursorValue(in.Page), string(binding), "summary")
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		q, err := r.Store.QueryQ7(ctx, store.Q7Request{Work: in.WorkID, Direction: historyDirection(in.Direction), EventKinds: in.EventKinds, Limit: r.boundedLimit(in.Page.Limit), Cursor: inner})
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		response, err := r.q7(base, q)
		if err != nil {
			return response, err
		}
		return r.wrapCursor(ctx, response, inner, string(binding), "summary")
	case "concord_work_trace.observations":
		var in observationReadInput
		if err := decodeOperationInput(input, &in); err != nil {
			return base, err
		}
		now := r.Authority.now()
		observations, err := r.Store.ObservationsForWork(ctx, in.WorkID, r.boundedLimit(in.Page.Limit))
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		meta := store.ResultMeta{QueryID: "CD-0030.R1", ContractVersion: "CD-0030/1.0", ResolvedScope: store.ResolvedScope{WorkID: in.WorkID}, Authority: "authoritative", Freshness: store.Freshness{ObservedAt: now.UTC().Format(time.RFC3339Nano)}, OrderingKeys: []string{"recorded_at", "observation_id"}}
		return r.resultEnvelope(base, meta, r.scope(meta), map[string]any{"observations": observations})
	case "concord_work_trace.external_observations":
		var in externalObservationReadInput
		if err := decodeOperationInput(input, &in); err != nil {
			return base, err
		}
		now := r.Authority.now()
		externalObservations, err := r.Store.ExternalObservationsForWork(ctx, in.WorkID, now, r.boundedLimit(in.Limit))
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		meta := store.ResultMeta{QueryID: "CD-0040.R1", ContractVersion: "CD-0040/1.0", ResolvedScope: store.ResolvedScope{WorkID: in.WorkID}, Authority: "authoritative", Freshness: store.Freshness{ObservedAt: now.UTC().Format(time.RFC3339Nano)}, OrderingKeys: []string{"created_event_seq", "observation_id"}}
		return r.resultEnvelope(base, meta, r.scope(meta), map[string]any{"external_observations": externalObservations})
	case "concord_work_trace.continuity":
		var in continuityInput
		if err := decodeOperationInput(input, &in); err != nil {
			return base, err
		}
		bindingInput := in
		bindingInput.Page.Cursor = nil
		binding, _ := json.Marshal(bindingInput)
		inner, err := r.unwrapCursor(ctx, cursorValue(in.Page), string(binding), "continuity")
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		snapshot, err := store.ReadWorkflowContinuity(ctx, r.Store, store.ContinuityRequest{Work: in.WorkID, Limit: r.boundedLimit(in.Page.Limit), Cursor: inner})
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		response, err := r.continuity(base, snapshot)
		if err != nil {
			return response, err
		}
		return r.wrapCursor(ctx, response, inner, string(binding), "continuity")
	case "concord_work_trace.research":
		var in researchReadInput
		if err := decodeOperationInput(input, &in); err != nil {
			return base, err
		}
		if (in.PackID == "") == (in.WorkID == "") {
			return coreError(base, "invalid_input", "research read requires exactly one of pack_id or work_id", "supply_pack_or_work", false), nil
		}
		var pack store.ResearchPack
		var readErr error
		if in.PackID != "" {
			pack, readErr = store.GetResearchPack(ctx, r.Store, in.PackID, r.boundedLimit(in.Page.Limit))
		} else {
			packs, listErr := store.ResearchPacksByOwner(ctx, r.Store, in.WorkID, r.boundedLimit(in.Page.Limit))
			if listErr != nil {
				return failureEnvelope(base, listErr), nil
			}
			if len(packs) == 0 {
				return coreError(base, "not_found", "no active research pack for that work item", "check_the_owner", false), nil
			}
			pack = packs[0]
		}
		if readErr != nil {
			return failureEnvelope(base, readErr), nil
		}
		return r.resultEnvelope(base, store.ResultMeta{QueryID: "PM1.Q11", ContractVersion: "PM1/1.0", ResolvedScope: store.ResolvedScope{WorkID: pack.OwnerWorkID}, SourceVersionWatermark: pack.CurrentRevision, Authority: "authoritative", Freshness: store.Freshness{ObservedAt: pack.UpdatedAt}, OrderingKeys: []string{"pack:" + pack.PackID}}, r.scope(store.ResultMeta{}), pack)
	case "concord_work_trace.relations":
		var in relationInput
		if err := decodeOperationInput(input, &in); err != nil {
			return base, err
		}
		q, err := r.Store.QueryQ8(ctx, store.Q8Request{Work: in.WorkID, RelationKinds: in.RelationKinds, Direction: in.Direction, Depth: in.Depth})
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		return r.q8(base, q)
	case "concord_work_initiative.entries":
		var in initiativeEntriesInput
		if err := decodeOperationInput(input, &in); err != nil {
			return base, err
		}
		summary, err := r.Store.ReadWorkItemSummary(ctx, in.InitiativeWorkID)
		if err != nil {
			var failure *store.Failure
			if errors.As(err, &failure) && failure.Kind == store.KindProjectionNotFound {
				return coreError(base, "unknown_scope", "Initiative does not exist", "reread_entities", false), nil
			}
			return failureEnvelope(base, err), nil
		}
		if summary.Kind != "initiative" {
			return coreError(base, "invariant_violation", "entry read target is not an Initiative", "reread_entities", false), nil
		}
		products, err := r.Store.ProductsForWorkIDs(ctx, []string{in.InitiativeWorkID})
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		if len(products[in.InitiativeWorkID]) != 1 {
			return coreError(base, "invariant_violation", "Initiative does not derive exactly one Product", "resolve_ambiguity", false), nil
		}
		entries, err := r.Store.ReadInitiativeEntries(ctx, in.InitiativeWorkID)
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		meta := store.ResultMeta{QueryID: queryID, ContractVersion: "C21/1.0", ResolvedScope: store.ResolvedScope{ProductID: products[in.InitiativeWorkID][0], WorkID: in.InitiativeWorkID}, Authority: "authoritative", Freshness: store.Freshness{ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}, OrderingKeys: []string{"position", "child_work_id"}}
		return r.resultEnvelope(base, meta, r.scope(meta), map[string]any{"entries": entries, "narrative": summary.Narrative})
	case "concord_knowledge.search":
		var in knowledgeSearchInput
		if err := decodeOperationInput(input, &in); err != nil {
			return base, err
		}
		if in.ProductID == "" {
			in.ProductID = r.Envelope.SelectedProductID
		}
		var home store.KnowledgeHome
		var homeErr error
		if in.ProductID != "" || in.ProjectID != "" {
			home, homeErr = r.Store.ResolveKnowledgeQueryHome(ctx, in.ProductID, in.ProjectID, store.KnowledgeHome{}, "PM1.Q9")
		} else {
			home, homeErr = r.knowledgeHome(ctx)
		}
		if homeErr != nil {
			return failureEnvelope(base, homeErr), nil
		}
		bindingInput := in
		bindingInput.Page.Cursor = nil
		binding, _ := json.Marshal(bindingInput)
		inner, err := r.unwrapCursor(ctx, cursorValue(in.Page), string(binding), "summary", home)
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		q, err := r.Store.QueryQ9(ctx, store.Q9Request{Product: in.ProductID, Project: in.ProjectID, Domain: in.DomainID, Kinds: knowledgeKinds(in.Kinds), Tags: in.Tags, Text: in.Text, Since: deref(in.Since), Until: deref(in.Until), Limit: r.boundedLimit(in.Page.Limit), Cursor: inner, Home: home, AllowDegraded: in.AllowDegraded})
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		response, err := r.q9(base, q)
		if err != nil {
			return response, err
		}
		return r.wrapCursor(ctx, response, inner, string(binding), "summary")
	case "concord_knowledge.resolve_note":
		var in knowledgeResolveInput
		if err := decodeOperationInput(input, &in); err != nil {
			return base, err
		}
		q, err := r.Store.QueryQ10(ctx, store.Q10Request{Work: in.WorkID, KnowledgeID: in.KnowledgeID, Product: r.Envelope.SelectedProductID, Home: store.KnowledgeHome{}})
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		return r.q10(base, q)
	case "concord_knowledge.unprocessed":
		var in knowledgeUnprocessedInput
		if err := decodeOperationInput(input, &in); err != nil {
			return base, err
		}
		if in.ProductID == "" {
			in.ProductID = r.Envelope.SelectedProductID
		}
		var home store.KnowledgeHome
		var homeErr error
		if in.ProductID != "" || in.ProjectID != "" {
			home, homeErr = r.Store.ResolveKnowledgeQueryHome(ctx, in.ProductID, in.ProjectID, store.KnowledgeHome{}, "PM1.Q15")
		} else {
			home, homeErr = r.knowledgeHome(ctx)
		}
		if homeErr != nil {
			return failureEnvelope(base, homeErr), nil
		}
		paths, err := r.Store.ReadUnprocessedKnowledgeDocs(ctx, home)
		if err != nil {
			return failureEnvelope(base, err), nil
		}
		limit := in.Limit
		if limit == 0 {
			limit = in.Page.Limit
		}
		limit = r.boundedLimit(limit)
		if limit == 0 || limit > 100 {
			limit = 100
		}
		if len(paths) > limit {
			paths = paths[:limit]
		}
		meta := store.ResultMeta{
			QueryID:         "PM1.Q15",
			ContractVersion: "PM1/1.0",
			ResolvedScope:   store.ResolvedScope{ProductID: in.ProductID, ProjectID: in.ProjectID},
			Authority:       "authoritative",
			Freshness:       store.Freshness{ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)},
			OrderingKeys:    []string{"path"},
		}
		return r.resultEnvelope(base, meta, r.scope(meta), map[string]any{"paths": paths})
	case "concord_domain.list", "concord_domain.detail", "concord_domain.active_work", "concord_domain.attachments", "concord_domain.overlaps":
		var in domainReadInput
		if err := decodeOperationInput(input, &in); err != nil {
			return base, err
		}
		product := in.ProductID
		if product == "" {
			product = r.Envelope.SelectedProductID
		}
		if product == "" {
			return coreError(base, "unknown_scope", "Domain reads require a resolved Product", "reread_entities", false), nil
		}
		switch r.Operation {
		case "list":
			result, err := r.Store.QueryDomainList(ctx, store.DomainListRequest{Product: product, Limit: r.boundedLimit(in.Page.Limit), Cursor: deref(in.Page.Cursor)})
			if err != nil {
				return failureEnvelope(base, err), nil
			}
			response, err := r.resultEnvelope(base, result.ResultMeta, r.scope(result.ResultMeta), store.NewDomainListPayload(result))
			if err != nil {
				return response, err
			}
			return r.wrapCursor(ctx, response, deref(in.Page.Cursor), queryID+":"+product, "domains")
		case "detail":
			if in.DomainID == "" {
				return coreError(base, "invalid_input", "Domain detail requires domain_id", "reread_entities", false), nil
			}
			result, err := r.Store.QueryDomainDetail(ctx, store.DomainDetailRequest{Product: product, Domain: in.DomainID})
			if err != nil {
				return failureEnvelope(base, err), nil
			}
			return r.resultEnvelope(base, result.ResultMeta, r.scope(result.ResultMeta), store.NewDomainDetailPayload(result))
		case "active_work":
			if in.DomainID == "" {
				return coreError(base, "invalid_input", "active Domain work requires domain_id", "reread_entities", false), nil
			}
			result, err := r.Store.QueryDomainActiveWork(ctx, store.DomainActiveWorkRequest{Product: product, Domain: in.DomainID, Limit: r.boundedLimit(in.Page.Limit), Cursor: deref(in.Page.Cursor)})
			if err != nil {
				return failureEnvelope(base, err), nil
			}
			response, err := r.resultEnvelope(base, result.ResultMeta, r.scope(result.ResultMeta), store.NewDomainActiveWorkPayload(result))
			if err != nil {
				return response, err
			}
			return r.wrapCursor(ctx, response, deref(in.Page.Cursor), queryID+":"+product+":"+in.DomainID, "work")
		case "attachments":
			if in.DomainID == "" {
				return coreError(base, "invalid_input", "Domain attachments require domain_id", "reread_entities", false), nil
			}
			result, err := r.Store.QueryDomainAttachments(ctx, store.DomainAttachmentsRequest{Product: product, Domain: in.DomainID})
			if err != nil {
				return failureEnvelope(base, err), nil
			}
			return r.resultEnvelope(base, result.ResultMeta, r.scope(result.ResultMeta), store.NewDomainAttachmentsPayload(result))
		case "overlaps":
			result, err := r.Store.QueryDomainOverlaps(ctx, store.DomainOverlapsRequest{Product: product, Domain: in.DomainID})
			if err != nil {
				return failureEnvelope(base, err), nil
			}
			return r.resultEnvelope(base, result.ResultMeta, r.scope(result.ResultMeta), store.NewDomainOverlapsPayload(result))
		default:
			// Every Domain operation names its own read. Refusing an unmatched
			// operation keeps a new one from silently answering with another
			// Domain read's payload.
			return base, errors.New("unsupported Domain read operation")
		}
	default:
		return base, errors.New("unsupported read operation")
	}
}

func nonEmpty(v string) []string {
	if v == "" {
		return nil
	}
	return []string{v}
}
func deref(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func historyDirection(v string) string {
	if v == "incoming" {
		return "newest_first"
	}
	return "oldest_first"
}
func knowledgeKinds(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		switch v {
		case "specification":
			out = append(out, "spec")
		case "note":
			out = append(out, "work_note")
		default:
			out = append(out, v)
		}
	}
	return out
}

func (r runtime) knowledgeHome(ctx context.Context) (store.KnowledgeHome, error) {
	resolved, err := r.Store.ResolveProject(ctx, r.Envelope.Directory, r.Envelope.Worktree)
	if err != nil {
		return store.KnowledgeHome{}, err
	}
	if len(resolved.Locators) == 0 {
		return store.KnowledgeHome{}, newRuntimeFailure("unknown_scope", "Project has no durable git locator", "resolve_ambiguity", false)
	}
	return store.KnowledgeHome{HomeProjectID: resolved.ProjectID, HomeLocatorID: resolved.Locators[0].ID, RepoPath: resolved.Repository.CanonicalPath, HeadRef: "HEAD"}, nil
}

func (r runtime) unwrapCursor(ctx context.Context, token, binding, detail string, knowledgeHomes ...store.KnowledgeHome) (string, error) {
	if token == "" {
		return "", nil
	}
	expected := SignedCursor{Tool: r.Tool, Operation: r.Operation, Scope: r.Envelope.SelectedProductID + "|" + r.Envelope.AmbientProjectID, Filter: binding, Detail: detail, Order: "default"}
	if r.Tool != "concord_knowledge" {
		watermark, err := r.Store.DomainEventWatermark(ctx)
		if err != nil {
			return "", err
		}
		expected.Source = strconv.FormatInt(watermark, 10)
	} else {
		var watermark string
		projectID := r.Envelope.AmbientProjectID
		locatorID := ""
		headRef := ""
		if len(knowledgeHomes) > 0 {
			projectID = knowledgeHomes[0].HomeProjectID
			locatorID = knowledgeHomes[0].HomeLocatorID
			headRef = knowledgeHomes[0].HeadRef
		}
		watermark, err := r.Store.KnowledgeIndexWatermark(ctx, projectID, locatorID, headRef)
		if err != nil {
			return "", err
		}
		expected.Source = watermark
	}
	cursor, err := r.Store.DecodeCursor(ctx, token, expected)
	if err != nil {
		return "", err
	}
	return cursor.Inner, nil
}

func (r runtime) wrapCursor(ctx context.Context, response Envelope, inner, binding, detail string) (Envelope, error) {
	if response.NextCursor == nil {
		return response, nil
	}
	token, err := r.Store.EncodeCursor(ctx, SignedCursor{Tool: r.Tool, Operation: r.Operation, Scope: r.Envelope.SelectedProductID + "|" + r.Envelope.AmbientProjectID, Filter: binding, Detail: detail, Order: "default", Source: watermarkString(response), Last: "", Inner: *response.NextCursor})
	if err != nil {
		return response, err
	}
	response.NextCursor = &token
	return response, nil
}

func watermarkString(response Envelope) string {
	if len(response.SourceVersionWatermark) == 0 {
		return ""
	}
	return response.SourceVersionWatermark[0].Version
}

func applyMeta(base Envelope, meta store.ResultMeta, scope *Scope) Envelope {
	base.QueryID = meta.QueryID
	base.Authority = AuthorityLevel(meta.Authority)
	base.ResolvedScope = scope
	base.Freshness = &Freshness{ObservedAt: parseTime(meta.Freshness.ObservedAt), Age: meta.Freshness.Age, Stale: meta.Freshness.Stale}
	base.OrderingKeys = meta.OrderingKeys
	base.NextCursor = meta.NextCursor
	base.Omissions = notices(meta.Omissions)
	// Dispatch-level notices are recorded on the base envelope before the read
	// runs — TS5 §3's context_refreshed is the load-bearing case. Replacing the
	// slice here would drop the only signal that a stale read was refreshed, so
	// query warnings are appended within the envelope's bounded notice budget.
	base.Warnings = boundedNotices(base.Warnings, notices(meta.Warnings))
	base.SourceVersionWatermark = []Watermark{{SourceKind: "product_memory", SourceID: "sqlite", Version: strconv.FormatInt(meta.SourceVersionWatermark, 10)}}
	return base
}

func boundedNotices(existing, added []Notice) []Notice {
	out := append(append(make([]Notice, 0, len(existing)+len(added)), existing...), added...)
	if len(out) > MaxNotices {
		return out[:MaxNotices]
	}
	return out
}
func parseTime(v string) time.Time { t, _ := time.Parse(time.RFC3339Nano, v); return t }
func notices(values []string) []Notice {
	out := make([]Notice, 0, len(values))
	for _, v := range values {
		out = append(out, Notice{Kind: v})
	}
	return out
}
func (r runtime) scope(meta store.ResultMeta) *Scope {
	p := meta.ResolvedScope.ProductID
	if p == "" {
		p = r.Envelope.SelectedProductID
	}
	return &Scope{ProductID: p, ProjectIDs: nonEmpty(r.Envelope.AmbientProjectID), ScopeVersion: r.Envelope.ScopeVersion}
}
func (r runtime) resultEnvelope(base Envelope, meta store.ResultMeta, scope *Scope, payload any) (Envelope, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return base, err
	}
	if r.Budget.MaxBytes > 0 && len(raw) > r.Budget.MaxBytes {
		return r.budgetRefusal(base, "result exceeds requested max_bytes budget"), nil
	}
	if r.Budget.MaxItems > 0 && maxArrayLength(raw) > r.Budget.MaxItems {
		return r.budgetRefusal(base, "result exceeds requested max_items budget"), nil
	}
	base = applyMeta(base, meta, scope)
	base.Outcome = OutcomeOK
	base.Result = raw
	if err := ValidateOperationPayload(base.Tool, base.Operation, raw, true); err != nil {
		return base, err
	}
	return base, nil
}

func maxArrayLength(raw []byte) int {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return 0
	}
	var visit func(any) int
	visit = func(value any) int {
		max := 0
		switch value := value.(type) {
		case []any:
			max = len(value)
			for _, child := range value {
				if childMax := visit(child); childMax > max {
					max = childMax
				}
			}
		case map[string]any:
			for _, child := range value {
				if childMax := visit(child); childMax > max {
					max = childMax
				}
			}
		}
		return max
	}
	return visit(value)
}

type workSummary struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Title      string   `json:"title"`
	Lifecycle  string   `json:"lifecycle"`
	Version    int64    `json:"version"`
	Priority   int64    `json:"priority,omitempty"`
	ProjectIDs []string `json:"project_ids,omitempty"`
	Ready      bool     `json:"ready,omitempty"`
	Narrative  string   `json:"narrative,omitempty"`
	TerminalAt *string  `json:"terminal_at"`
}

func summary(w store.WorkItem) workSummary {
	ids := make([]string, 0, len(w.Projects))
	for _, p := range w.Projects {
		ids = append(ids, p.ID)
	}
	kind := w.Kind
	if kind != "task" && kind != "bug" && kind != "decision" && kind != "research" && kind != "initiative" && kind != "other" {
		kind = "other"
	}
	var terminal *string
	if w.TerminalAt != "" {
		terminal = &w.TerminalAt
	}
	return workSummary{ID: w.ID, Kind: kind, Title: w.Title, Lifecycle: w.Lifecycle, Version: 1, Priority: w.Priority, ProjectIDs: ids, Ready: w.Ready, Narrative: w.Narrative, TerminalAt: terminal}
}
func (r runtime) q1(base Envelope, q store.Q1Result) (Envelope, error) {
	projects := []map[string]any{}
	for _, p := range q.Projects {
		projects = append(projects, map[string]any{"project_id": p.ID, "version": 1, "role": p.Role})
	}
	id := ""
	stage := "prototype"
	if q.Product != nil {
		id = q.Product.ID
		stage = q.Product.StageMaturity
	} else if len(q.Products) > 0 {
		// The generated v1 result is a Product-context object. For an unscoped
		// resolve, expose the deterministic first Product plus all candidates;
		// callers must select a stable ID before Product-scoped reads.
		id = q.Products[0].ID
		stage = q.Products[0].StageMaturity
		if len(q.CandidateIDs) == 0 {
			for _, product := range q.Products {
				q.CandidateIDs = append(q.CandidateIDs, product.ID)
			}
		}
	}
	if q.CandidateIDs == nil {
		q.CandidateIDs = []string{}
	}
	return r.resultEnvelope(base, q.ResultMeta, r.scope(q.ResultMeta), map[string]any{"product_id": id, "stage": stage, "projects": projects, "candidates": q.CandidateIDs})
}
func (r runtime) q2(base Envelope, q store.Q2Result) (Envelope, error) {
	items := make([]workSummary, 0, len(q.Items))
	for _, w := range q.Items {
		items = append(items, summary(w))
	}
	return r.resultEnvelope(base, q.ResultMeta, r.scope(q.ResultMeta), map[string]any{"counts": map[string]int{"needed": q.LifecycleCounts["needed"], "in_progress": q.LifecycleCounts["in_progress"], "completed": q.LifecycleCounts["completed"], "cancelled": q.LifecycleCounts["cancelled"]}, "previews": items})
}

func (r runtime) productRows(base Envelope, q store.ProductRowResult) (Envelope, error) {
	payload, err := portfolio.Payload(q)
	if err != nil {
		return base, err
	}
	return r.resultEnvelope(base, q.ResultMeta, r.scope(q.ResultMeta), json.RawMessage(payload))
}
func (r runtime) q3(base Envelope, q store.Q3Result) (Envelope, error) {
	items := make([]workSummary, 0, len(q.Items))
	for _, w := range q.Items {
		items = append(items, summary(w))
	}
	return r.resultEnvelope(base, q.ResultMeta, r.scope(q.ResultMeta), map[string]any{"items": items})
}
func (r runtime) q5(base Envelope, q store.Q5Result) (Envelope, error) {
	items := make([]workSummary, 0, len(q.Items))
	for _, w := range q.Items {
		items = append(items, summary(w))
	}
	return r.resultEnvelope(base, q.ResultMeta, r.scope(q.ResultMeta), map[string]any{"items": items})
}
func (r runtime) q4(base Envelope, q store.Q4Result) (Envelope, error) {
	items := make([]workSummary, 0, len(q.Items))
	nodes := items
	edges := []map[string]string{}
	seen := map[string]bool{}
	for _, w := range q.Items {
		items = append(items, summary(w))
		seen[w.ID] = true
		for _, b := range w.Blockers {
			if !seen[b.ID] {
				nodes = append(nodes, summary(b))
				seen[b.ID] = true
			}
			edges = append(edges, map[string]string{"from": w.ID, "to": b.ID, "kind": "blocks"})
		}
	}
	return r.resultEnvelope(base, q.ResultMeta, r.scope(q.ResultMeta), map[string]any{"items": items, "nodes": nodes, "edges": edges})
}
func (r runtime) q6(base Envelope, q store.Q6Result) (Envelope, error) {
	if q.Work != nil {
		payload := map[string]any{"work": summary(*q.Work), "memberships": func() []map[string]string {
			out := []map[string]string{}
			for _, p := range q.Work.Projects {
				out = append(out, map[string]string{"project_id": p.ID, "role": p.Role})
			}
			return out
		}(), "items": []workSummary{}}
		verdict, redacted, verdictErr := r.verdictFor(q.Work.ID)
		if verdictErr != nil {
			return failureEnvelope(base, verdictErr), nil
		}
		if verdict != nil && !redacted {
			payload["verdict"] = verdict
		}
		envelope, err := r.resultEnvelope(base, q.ResultMeta, r.scope(q.ResultMeta), payload)
		if err != nil {
			return envelope, err
		}
		if redacted {
			envelope.Omissions = append(envelope.Omissions, Notice{Kind: "redacted", SourceID: q.Work.ID, Details: map[string]any{"field": "verdict", "reason": "executing_actor_verdict_read_scope"}})
		}
		return envelope, nil
	}
	items := []workSummary{}
	for _, w := range q.Items {
		items = append(items, summary(w))
	}
	return r.resultEnvelope(base, q.ResultMeta, r.scope(q.ResultMeta), map[string]any{"items": items})
}

// verdictFor applies CD-0023: the recorded verdict of a terminal work item
// is readable by every authority except the actor recorded as executing it.
// redacted is true when the reader is that executing actor; the omission is
// the caller's to record.
func (r runtime) verdictFor(workID string) (*store.WorkflowReadVerdict, bool, error) {
	verdict, err := store.ReadWorkflowVerdict(context.Background(), r.Store, workID)
	if err != nil || verdict == nil {
		return nil, false, err
	}
	agentRef, sessionRef, found, err := store.WorkflowExecutingIdentity(context.Background(), r.Store, workID)
	if err != nil || !found {
		return verdict, false, err
	}
	if r.Reader.AgentRef == agentRef && r.Reader.SessionRef == sessionRef {
		return nil, true, nil
	}
	return verdict, false, nil
}

func (r runtime) q7(base Envelope, q store.Q7Result) (Envelope, error) {
	events := make([]map[string]any, 0, len(q.Events))
	for _, e := range q.Events {
		evidence := make([]map[string]any, 0, len(e.EvidenceRefs))
		for _, ref := range e.EvidenceRefs {
			evidence = append(evidence, map[string]any{"kind": "artifact", "authority": "product_memory", "locator_kind": "reference", "locator": ref})
		}
		// `reason` and `actor` are optional in work_event_page and carry a
		// minimum length: an ordinary lifecycle event has neither, so the
		// keys are omitted rather than emitted empty. A present-but-empty
		// optional field fails the generated schema and refuses the whole
		// page (issue #383).
		event := map[string]any{"event_id": e.EventID, "kind": e.Kind, "version": e.Seq, "occurred_at": e.OccurredAt, "evidence": evidence}
		if e.Actor != "" {
			event["actor"] = e.Actor
		}
		if e.Reason != "" {
			event["reason"] = e.Reason
		}
		events = append(events, event)
	}
	return r.resultEnvelope(base, q.ResultMeta, r.scope(q.ResultMeta), map[string]any{"events": events})
}

// ContinuityPayload is the single public rendering of CD-0016 continuity.
// Session boot and the concord_work_trace.continuity read share this exact shape.
func ContinuityPayload(snapshot store.ContinuitySnapshot) map[string]any {
	observations := snapshot.Observations
	if observations == nil {
		observations = []store.WorkObservation{}
	}
	return map[string]any{
		"work_id":            snapshot.WorkID,
		"pinned":             map[string]any{"product_identity": snapshot.ProductIdentity, "workflow_step": snapshot.WorkflowStep, "contract": snapshot.Contract, "spec_mandate": snapshot.SpecMandate, "pending_operator_decision": snapshot.PendingOperatorDecision, "latest_checkpoint": snapshot.LatestCheckpoint, "unresolved_failure": snapshot.UnresolvedFailure},
		"latest_checkpoint":  snapshot.LatestCheckpoint,
		"boundaries":         map[string]any{"count": snapshot.BoundaryCount, "items": snapshot.Boundaries, "next_cursor": snapshot.NextCursor, "watermark": snapshot.Watermark},
		"typed_availability": map[string]any{"restart": "unavailable", "reason": snapshot.RestartUnavailableReason},
		"pending_messages":   snapshot.PendingMessages,
		"observations":       observations,
	}
}

func (r runtime) continuity(base Envelope, snapshot store.ContinuitySnapshot) (Envelope, error) {
	watermark := int64(0)
	if strings.HasPrefix(snapshot.Watermark, "seq:") {
		watermark, _ = strconv.ParseInt(strings.TrimPrefix(snapshot.Watermark, "seq:"), 10, 64)
	}
	meta := store.ResultMeta{QueryID: "C19.Continuity", ContractVersion: "C19/1.0", ResolvedScope: store.ResolvedScope{WorkID: snapshot.WorkID, ProductIDs: snapshot.ProductIdentity}, SourceVersionWatermark: watermark, Authority: "authoritative", Freshness: store.Freshness{ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}, OrderingKeys: []string{"boundary_sequence"}, NextCursor: snapshot.NextCursor, Omissions: []string{}, Warnings: []string{}}
	return r.resultEnvelope(base, meta, r.scope(meta), ContinuityPayload(snapshot))
}

func (r runtime) q8(base Envelope, q store.Q8Result) (Envelope, error) {
	if q.Edges == nil {
		q.Edges = []store.RelationEdge{}
	}
	// The store struct (store.RelationEdge) and the agent envelope use
	// deliberately different spellings: the store spells endpoints as
	// `source`/`target`, while the public agent envelope uses `from`/`to` so
	// the directional axis reads naturally. The envelope schema
	// (`work_relation_graph.edges` in
	// contracts/agent-tool-surface-payloads.schema.json) is the public
	// contract; the store-side spelling is pinned by scenarios
	// `Q8-relations` in scenarios/product-memory-query.v1.json, which asserts
	// $.edges[0].source / $.edges[0].target against the store projection
	// layer. Translating here keeps both contracts intact.
	edges := make([]map[string]any, 0, len(q.Edges))
	for _, e := range q.Edges {
		edges = append(edges, map[string]any{"from": e.Source, "to": e.Target, "kind": e.Kind, "depth": e.Depth, "relation_id": e.RelationID})
	}
	return r.resultEnvelope(base, q.ResultMeta, r.scope(q.ResultMeta), map[string]any{"nodes": []any{}, "edges": edges, "replacement_state": relationReplacementState(q.Edges)})
}
func relationReplacementState(edges []store.RelationEdge) string {
	for _, edge := range edges {
		if edge.Kind == "supersedes" || edge.Kind == "superseded_by" {
			return "active"
		}
	}
	return "none"
}
func (r runtime) q9(base Envelope, q store.Q9Result) (Envelope, error) {
	type item struct {
		ID      string `json:"knowledge_id"`
		Kind    string `json:"kind"`
		Locator string `json:"locator"`
		Commit  string `json:"commit_oid,omitempty"`
		Hash    string `json:"content_hash,omitempty"`
	}
	items := []item{}
	for _, v := range q.Items {
		kind := v.Kind
		if kind == "work_note" {
			kind = "note"
		}
		if kind == "spec" {
			kind = "specification"
		}
		items = append(items, item{v.ID, kind, v.NotePath, v.CommitOID, v.ContentHash})
	}
	response, err := r.resultEnvelope(base, q.ResultMeta, r.scope(q.ResultMeta), map[string]any{"items": items, "watermark": q.IndexWatermark})
	if err == nil {
		response.SourceVersionWatermark = []Watermark{{SourceKind: "git_knowledge", SourceID: q.IndexWatermark, Version: q.IndexWatermark}}
	}
	return response, err
}
func (r runtime) q10(base Envelope, q store.Q10Result) (Envelope, error) {
	state := q.Status
	var locator *string
	if q.Note != nil {
		v := q.Note.NotePath
		locator = &v
	}
	return r.resultEnvelope(base, q.ResultMeta, r.scope(q.ResultMeta), map[string]any{"state": state, "locator": locator, "candidates": []string{}})
}

// SignedCursor remains an agent-facing alias while the authority owns its
// representation and authenticated serialization.
type SignedCursor = store.SignedCursor
