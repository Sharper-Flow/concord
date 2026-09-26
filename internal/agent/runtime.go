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
type resourcesInput struct {
	ProductID   string      `json:"product_id"`
	ResourceID  string      `json:"resource_id"`
	Class       string      `json:"class"`
	Kind        string      `json:"kind"`
	Environment string      `json:"environment"`
	Page        pageInput   `json:"page"`
	Budget      budgetInput `json:"budget"`
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

type worktreeAuditInput struct {
	ProductID string      `json:"product_id"`
	Page      pageInput   `json:"page"`
	Budget    budgetInput `json:"budget"`
}

type worktreeInspectInput struct {
	WorkID string `json:"work_id"`
	Mode   string `json:"mode"`
	// Path is the relative file selector for file mode. It selects inside
	// the identity-derived worktree (CD-0096 D2), never a worktree path.
	Path string `json:"path"`
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
		if s == nil || authority == nil {
			return coreError(base, "unreachable", "workflow authority is not available", "contact_operator", true), nil
		}
		// The workflow actor tuple and the idempotency partition both key on
		// principal_ref, which CD-0080 D1 derives rather than accepts. Identity
		// authorization therefore precedes the preflight; Product and Project
		// scope stay out of it, because scope is validated after the budget.
		actorInv := Invocation{ClientRef: env.ClientRef, SessionRef: env.SessionRef, AgentRef: env.AgentRef, Directory: env.Directory, Worktree: env.Worktree, ManifestDigest: env.ManifestDigest, HostAssertionDigest: env.HostAssertionDigest, RequiredCapability: op.Capability, RequiredOperation: request.Operation}
		actor, actorErr := authority.Authorize(ctx, actorInv)
		if actorErr != nil {
			return failureEnvelope(base, actorErr), nil
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
	inv := Invocation{ClientRef: env.ClientRef, PrincipalRef: env.PrincipalRef, SessionRef: env.SessionRef, AgentRef: env.AgentRef, Directory: env.Directory, Worktree: env.Worktree, ManifestDigest: env.ManifestDigest, HostAssertionDigest: env.HostAssertionDigest, RequiredCapability: op.Capability, RequiredOperation: request.Operation, ProductID: env.SelectedProductID}
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
			return failureEnvelope(base, identityErr), nil
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
		return failureEnvelope(base, err), nil
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
	if err := validateRequestedScope(ctx, s, env, grant, request, op.Kind); err != nil {
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

// validateRequestedScope checks every Product-scoped reference the request
// names against the grant before dispatch. A Product the policy authorizes is
// readable across the ambient selection (TS5 §2.3); spanning outside the
// selected Product by mutation keeps the cross_scope capability gate.
func validateRequestedScope(ctx context.Context, s *store.Store, env CallEnvelope, grant Authority, request InvokeRequest, kind OperationKind) error {
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
			if kind != OperationRead && env.SelectedProductID != "" && !contains(products, env.SelectedProductID) && !containsCapability(grant.Capabilities, Capability("cross_scope")) {
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
				if kind != OperationRead && env.SelectedProductID != "" && !contains(products, env.SelectedProductID) && !containsCapability(grant.Capabilities, Capability("cross_scope")) {
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
	// recoveryRefs carries the ordered workflow actions of a declared-route
	// remedy. It pairs with the use_declared_route action and stays empty for
	// every other recovery action.
	recoveryRefs        []string
	Candidates          []string
	CurrentScopeVersion string
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

// newRouteFailure mints a refusal whose remedy is a declared route of
// workflow actions rather than an operator. route is ordered: the caller
// executes it front to back. unauthorized is deliberately absent from
// enforcedRecoveryCouplings, so a refusal of that kind may name the route it
// admits instead of the standing contact_operator default.
func newRouteFailure(kind, message string, route ...string) *runtimeFailure {
	return &runtimeFailure{kind: kind, message: message, recovery: "use_declared_route", retry: false, recoveryRefs: route}
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
		action := RecoveryAction{Kind: f.recovery, RequiredRefs: f.recoveryRefs}
		out := coreErrorAction(base, f.kind, f.message, action, f.retry)
		out.Error.Candidates = f.Candidates
		return out
	}
	var sf *store.Failure
	if errors.As(err, &sf) {
		kind := mapFailureKind(sf.Kind)
		recovery := publicRecovery(kind, sf.RecoveryAction)
		refs := sf.RecoveryRefs
		if recovery != "use_declared_route" {
			// Route refs ride the declared-route action alone; any other
			// action would carry refs the envelope contract never asked for.
			refs = nil
		} else if len(refs) == 0 {
			// A route naming no action cannot marshal. The kind's standing
			// default keeps the refusal the core decided deliverable.
			recovery = publicRecovery(kind, "")
			refs = nil
		}
		out := coreErrorAction(base, kind, sf.Detail, RecoveryAction{Kind: recovery, RequiredRefs: refs}, sf.RetrySafe)
		// Carry typed current-version carriers into the agent envelope so
		// callers can recover the live projection version structurally without
		// having to parse the human detail string. Mirrors the same path for
		// typed violations: see D5.
		for _, current := range sf.CurrentVersions {
			out.Error.CurrentVersions = append(out.Error.CurrentVersions, ChangedRef{EntityKind: string(current.SubjectType), ID: current.SubjectID, Version: strconv.FormatInt(current.Version, 10)})
		}
		for _, action := range sf.InterveningActions {
			out.Error.InterveningActions = append(out.Error.InterveningActions, InterveningAction{ActionID: action.ActionID, SessionRef: action.SessionRef})
		}
		if len(sf.Violations) > 0 {
			out.Error.Violations = append(out.Error.Violations, sf.Violations...)
		}
		if sf.EffectPossible || len(sf.CommittedRefs) > 0 {
			out.Error.EffectState = EffectPossible
			out.ChangedRefs = committedChangedRefs(sf.CommittedRefs)
		}
		if sf.StaleLawRevision != nil {
			out.Error.StaleLawRevision = &StaleLawRevision{OldLawID: sf.StaleLawRevision.OldLawID, OldContentHash: sf.StaleLawRevision.OldContentHash, AcceptedSuccessorLawID: sf.StaleLawRevision.AcceptedSuccessorLawID, AcceptedSuccessorContentHash: sf.StaleLawRevision.AcceptedSuccessorContentHash, RecoveryActions: append([]string(nil), sf.StaleLawRevision.RecoveryActions...)}
		}
		if sf.DomainOverlap != nil {
			out.Error.DomainOverlap = &DomainOverlap{TotalOverlaps: sf.DomainOverlap.TotalOverlaps, ReturnedOverlaps: sf.DomainOverlap.ReturnedOverlaps, Truncated: sf.DomainOverlap.Truncated}
			for _, overlap := range sf.DomainOverlap.Overlaps {
				// The envelope contract requires an array for every shared list.
				// An architecture-only overlap carries empty law, modification,
				// and relation lists; a nil slice marshals as null and makes the
				// refusal undeliverable.
				converted := DomainOverlapDetail{ProductID: overlap.ProductID, FromWorkID: overlap.FromWorkID, ToWorkID: overlap.ToWorkID, FromContractVersion: overlap.FromContractVersion, ToContractVersion: overlap.ToContractVersion, SharedAffectedDomainIDs: nonNilStrings(overlap.SharedAffectedDomainIDs), SharedLawIDs: nonNilStrings(overlap.SharedLawIDs), SharedDomainModifications: nonNilStrings(overlap.SharedDomainModifications), SharedRelationTuples: []DomainOverlapRelationTuple{}, OverlapClasses: nonNilStrings(overlap.OverlapClasses), ResolutionState: overlap.ResolutionState, ResolutionKind: overlap.ResolutionKind, RecoveryActions: nonNilStrings(overlap.RecoveryActions), SharedAffectedDomainCount: overlap.SharedAffectedDomainCount, SharedLawCount: overlap.SharedLawCount, SharedDomainModificationCount: overlap.SharedDomainModificationCount, SharedRelationTupleCount: overlap.SharedRelationTupleCount, DetailTruncated: overlap.DetailTruncated}
				for _, tuple := range overlap.SharedRelationTuples {
					converted.SharedRelationTuples = append(converted.SharedRelationTuples, DomainOverlapRelationTuple{SourceDomainID: tuple.SourceDomainID, Kind: tuple.Kind, TargetDomainID: tuple.TargetDomainID})
				}
				out.Error.DomainOverlap.Overlaps = append(out.Error.DomainOverlap.Overlaps, converted)
			}
		}
		if sf.ExternalRefConflict != nil {
			out.Error.ExternalRefConflict = &ExternalRefConflict{ExistingWorkID: sf.ExternalRefConflict.ExistingWorkID, ExternalRef: sf.ExternalRefConflict.ExternalRef}
		}
		return out
	}
	return coreError(base, "internal_error", err.Error(), "contact_operator", false)
}

func committedChangedRefs(refs []store.SubjectCurrentVersion) *[]ChangedRef {
	if len(refs) == 0 {
		return nil
	}
	changed := make([]ChangedRef, 0, len(refs))
	for _, ref := range refs {
		changed = append(changed, ChangedRef{EntityKind: string(ref.SubjectType), ID: ref.SubjectID, Version: strconv.FormatInt(ref.Version, 10)})
	}
	return &changed
}

// nonNilStrings copies a string list so an empty input marshals as an empty
// array rather than null.
func nonNilStrings(values []string) []string {
	return append(make([]string, 0, len(values)), values...)
}

func coreError(base Envelope, kind, message, recovery string, retry bool) Envelope {
	return coreErrorAction(base, kind, message, RecoveryAction{Kind: recovery}, retry)
}

// coreErrorAction is coreError with a fully built recovery action, for the
// refusals whose remedy is a declared route of workflow actions.
func coreErrorAction(base Envelope, kind, message string, action RecoveryAction, retry bool) Envelope {
	message = boundedErrorMessage(message)
	// An unreachable refusal says the core could not answer, so the envelope
	// carries no authoritative claim, freshness, or watermark; the contract
	// pairs the kind with that authority and refuses any other pairing.
	authority := AuthorityAuthoritative
	if kind == "unreachable" {
		authority = AuthorityUnreachable
		base.Freshness = nil
		base.SourceVersionWatermark = []Watermark{}
	}
	base.Authority = authority
	base.Outcome = OutcomeError
	base.Error = &TypedError{Kind: kind, RetrySafe: retry, RecoveryAction: action, EffectState: EffectNone, Message: message}
	if _, err := base.Encode(); err == nil {
		return base
	}
	// Error delivery must remain possible when the rejected success inherited
	// optional fields large enough to exceed the envelope cap. Rebuild the base
	// from its bounded identity only when preserving metadata is undeliverable.
	// The typed error is the only authoritative response to a rejected result.
	errorBase := NewBase(base.RequestID, base.Tool, base.Operation)
	errorBase.ResolvedScope = base.ResolvedScope
	errorBase.Authority = authority
	errorBase.Outcome = OutcomeError
	errorBase.Error = &TypedError{Kind: kind, RetrySafe: retry, RecoveryAction: action, EffectState: EffectNone, Message: message}
	if _, err := errorBase.Encode(); err != nil {
		// Scope is useful when it remains deliverable, but never at the expense
		// of the limit error itself crossing the agent boundary.
		errorBase.ResolvedScope = nil
	}
	return errorBase
}

func boundedErrorMessage(message string) string {
	const maxBytes = 1000
	if len(message) <= maxBytes {
		return message
	}
	return strings.ToValidUTF8(message[:maxBytes], "")
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
	case store.KindWorktreeOwnershipConflict:
		// CD-0096 D3 Destroy: a removal refused because a live session runs in
		// the worktree. The remedy is ending that session and retiring the
		// worktree, so this is an authority refusal and the store names the
		// declared vacate-then-reclaim route the caller acts on. It is not an
		// operation to reconcile.
		return "unauthorized"
	case store.KindUnauthorizedDispatch:
		// A worker dispatch refused at the admission boundary: no active
		// worktree claim, a session outside the claimed worktree, or no
		// authorized window. unauthorized_dispatch is the adapter's name for
		// the same refusal; the core envelope carries it as the authority
		// refusal it is, never as an internal fault.
		return "unauthorized"
	case store.KindWorktreeLeaseHeld:
		// CD-0096 D3 Verify: exclusivity is coordination, not authority. The
		// message names the holding session; the refusal recorded nothing,
		// so the retry is safe. operation_conflict would couple this to
		// reconcile_operation, and there is nothing to reconcile.
		return "resource_busy"
	case store.KindWorktreeVerifyMutated:
		// CD-0096 D3 Verify: a verifier that edited its subject verifies
		// nothing. The lease is already released; completion refuses typed.
		return "operation_conflict"
	case store.KindProjectionConflict:
		// A projection identity another row already holds (a second active
		// worktree claim, a locator-drifted worktree) is a coordination
		// refusal, not an internal fault.
		return "operation_conflict"
	case store.KindIndexDegraded:
		return "degraded_not_allowed"
	case store.KindResearchConsumerBlocked:
		return "stale_requires_review"
	case store.KindStaleLawRevision:
		return "stale_law_revision"
	case store.KindDomainOverlap:
		return "domain_overlap"
	}
	// A store kind whose value is already a public kind passes through. The
	// cases above are the genuine renames, where the store's name and the
	// public name differ by intent. Everything else that names a public kind
	// is that kind; collapsing it to internal_error told the caller a fault
	// occurred and to contact the operator for a refusal it could have
	// answered itself, and publicRecovery then discarded the store's remedy.
	if store.TypedErrorKindAllowed(string(kind)) {
		return string(kind)
	}
	// A store kind that names no public kind is an internal fault to the
	// caller: the store raised something the contract cannot carry, which
	// is a defect to fix at the store, never a refusal to act on.
	return "internal_error"
}

func publicRecovery(kind, proposed string) string {
	// A coupled kind admits exactly one recovery action, and validateError
	// refuses every other pairing. The coupling therefore decides before the
	// proposed value is read: the store proposes free operator prose, and a
	// proposal that is merely a valid action is still the wrong one here.
	// Letting either reach the envelope produces a pair that cannot marshal,
	// which reaches the caller as a transport fault rather than the refusal
	// the core decided.
	if coupled, ok := enforcedRecoveryCouplings[kind]; ok {
		return coupled
	}
	allowed := map[string]bool{"none": true, "retry_same_request": true, "refresh_context": true, "reread_entities": true, "request_approval": true, "provide_evidence": true, "reduce_limit": true, "use_declared_route": true, "use_next_cursor": true, "restart_query": true, "adjust_budget": true, "reconcile_operation": true, "resolve_ambiguity": true, "contact_operator": true}
	if allowed[proposed] {
		return proposed
	}
	switch kind {
	case "unauthorized", "unreachable", "internal_error":
		return "contact_operator"
	case "approval_required", "approval_invalid":
		return "request_approval"
	case "invalid_transition", "invalid_relation", "invariant_violation", "invalid_input":
		return "reread_entities"
	case "not_terminal", "stale_requires_review", "idempotency_conflict", "degraded_not_allowed":
		// The caller acts on state it can reread: a lifecycle, a review
		// pin, a conflicting key, an index it may opt into degraded for.
		return "reread_entities"
	case "unknown_scope", "malformed_response", "transport_failure":
		return "contact_operator"
	default:
		// Every public kind is named above or coupled; reaching here means
		// a kind the vocabulary added without a recovery decision. That is
		// a contract defect, and contact_operator is the honest answer for
		// it, not a default any ordinary refusal should fall into.
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
		return r.readProductResolve(ctx, base, input)
	case "concord_product_view.snapshot":
		return r.readProductSnapshot(ctx, base, input)
	case "concord_product_view.portfolio":
		return r.readProductPortfolio(ctx, base, input)
	case "concord_product_view.blocked_sessions":
		return r.readProductBlockedSessions(ctx, base, input)
	case "concord_product_view.resources":
		return r.readProductResources(ctx, base, input)
	case "concord_work_browse.list":
		return r.readWorkList(ctx, base, input)
	case "concord_work_browse.ready":
		return r.readWorkReady(ctx, base, input)
	case "concord_work_browse.blocked":
		return r.readWorkBlocked(ctx, base, input)
	case "concord_work_browse.scope":
		return r.readWorkScope(ctx, base, input)
	case "concord_work_browse.resource_claims":
		return r.readResourceClaims(ctx, base, input)
	case "concord_work_browse.messages":
		return r.readWorkMessages(ctx, base, input)
	case "concord_work_browse.worktree_audit":
		return r.readWorktreeAudit(ctx, base, input)
	case "concord_work_browse.worktree_inspect":
		return r.readWorktreeInspect(ctx, base, input)
	case "concord_work_trace.history":
		return r.readTraceHistory(ctx, base, input)
	case "concord_work_trace.observations":
		return r.readTraceObservations(ctx, base, input)
	case "concord_work_trace.external_observations":
		return r.readTraceExternalObservations(ctx, base, input)
	case "concord_work_trace.continuity":
		return r.readTraceContinuity(ctx, base, input)
	case "concord_work_trace.research":
		return r.readTraceResearch(ctx, base, input)
	case "concord_work_trace.relations":
		return r.readTraceRelations(ctx, base, input)
	case "concord_work_initiative.entries":
		return r.readInitiativeEntries(ctx, base, input, queryID)
	case "concord_knowledge.search":
		return r.readKnowledgeSearch(ctx, base, input)
	case "concord_knowledge.resolve_note":
		return r.readKnowledgeResolveNote(ctx, base, input)
	case "concord_knowledge.unprocessed":
		return r.readKnowledgeUnprocessed(ctx, base, input)
	case "concord_domain.list", "concord_domain.detail", "concord_domain.active_work", "concord_domain.attachments", "concord_domain.overlaps":
		return r.readDomain(ctx, base, input, queryID)
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

// freshenProductKnowledge brings the index behind a Product's derived
// projections up to its knowledge home's current content. It runs with no
// transaction open. A Product without a unique designated home is not an
// error here: the caller's read owns that refusal, and an index cannot be
// built for a home that does not exist.
func (r runtime) freshenProductKnowledge(ctx context.Context, product string) error {
	home, err := r.Store.ResolveKnowledgeQueryHome(ctx, product, "", store.KnowledgeHome{}, r.Tool+"."+r.Operation)
	if err != nil {
		var failure *store.Failure
		if errors.As(err, &failure) && (failure.Kind == store.KindUnknownScope || failure.Kind == store.KindAmbiguousScope) {
			return nil
		}
		return err
	}
	return r.Store.EnsureKnowledgeIndexFresh(ctx, home)
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
	// A read that carries no observation time carries no freshness: the
	// envelope admits null there, and a fabricated zero instant fails the
	// marshal bound on every real call.
	base.Freshness = nil
	if observed := parseTime(meta.Freshness.ObservedAt); !observed.IsZero() {
		base.Freshness = &Freshness{ObservedAt: observed, Age: meta.Freshness.Age, Stale: meta.Freshness.Stale}
	}
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
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	Title      string         `json:"title"`
	Lifecycle  string         `json:"lifecycle"`
	Version    int64          `json:"version"`
	Priority   int64          `json:"priority,omitempty"`
	ProjectIDs []string       `json:"project_ids,omitempty"`
	Ready      bool           `json:"ready,omitempty"`
	Narrative  string         `json:"narrative,omitempty"`
	TerminalAt *string        `json:"terminal_at"`
	WorkPin    *store.WorkPin `json:"work_pin,omitempty"`
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
	return workSummary{ID: w.ID, Kind: kind, Title: w.Title, Lifecycle: w.Lifecycle, Version: w.Version, Priority: w.Priority, ProjectIDs: ids, Ready: w.Ready, Narrative: w.Narrative, TerminalAt: terminal, WorkPin: w.WorkPin}
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
	// nodes carries the unresolved blockers and items carries the blocked work.
	// They must not share a backing array: `nodes := items` copies the slice
	// header, so appending to one overwrites the elements of the other.
	nodes := []workSummary{}
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
	stepActions := snapshot.StepActions
	if stepActions == nil {
		stepActions = []string{}
	}
	pinned := map[string]any{"product_identity": snapshot.ProductIdentity, "workflow_step": snapshot.WorkflowStep, "step_actions": stepActions, "contract": snapshot.Contract, "spec_mandate": snapshot.SpecMandate, "pending_operator_decision": snapshot.PendingOperatorDecision, "withheld_operator_decision": snapshot.WithheldOperatorDecision, "latest_checkpoint": snapshot.LatestCheckpoint, "design_record": snapshot.DesignRecord, "unresolved_failure": snapshot.UnresolvedFailure}
	if snapshot.WorkPin != nil {
		pinned["work_pin"] = snapshot.WorkPin
	}
	// Empty peer signals stay out of the prompt. This keeps the unchanged
	// projection's bytes stable while non-empty signals remain visible.
	if len(snapshot.UnresolvedOverlaps) > 0 {
		pinned["unresolved_overlaps"] = snapshot.UnresolvedOverlaps
	}
	if len(snapshot.CompatibleLawAmendments) > 0 {
		pinned["compatible_law_amendments"] = snapshot.CompatibleLawAmendments
	}
	if snapshot.StaleLawRevision != nil {
		pinned["stale_law_revision"] = snapshot.StaleLawRevision
	}
	// CD-0096 D5: the pinned projection carries the reading session's held
	// verify leases. The absent field keeps the work-keyed boot bytes
	// byte-stable (CD-0090 D3).
	if len(snapshot.ActiveVerifyLeases) > 0 {
		pinned["active_verify_leases"] = snapshot.ActiveVerifyLeases
	}
	// The contract's resolved law and Domain references, and the recorded
	// proposal, ride the pinned projection when present. The absent fields
	// keep contracts with no bound law byte-stable.
	if snapshot.LawContext != nil {
		pinned["law_context"] = snapshot.LawContext
	}
	if snapshot.ProposalRecord != nil {
		pinned["proposal_record"] = proposalContextProjection(snapshot.ProposalRecord)
	}
	payload := map[string]any{
		"work_id":            snapshot.WorkID,
		"pinned":             pinned,
		"latest_checkpoint":  snapshot.LatestCheckpoint,
		"boundaries":         map[string]any{"count": snapshot.BoundaryCount, "items": snapshot.Boundaries, "next_cursor": snapshot.NextCursor, "watermark": snapshot.Watermark},
		"typed_availability": map[string]any{"restart": "unavailable", "reason": snapshot.RestartUnavailableReason},
		"pending_messages":   snapshot.PendingMessages,
		"observations":       observations,
	}
	// An instance-less work item states its workflow absence as typed
	// information: the step is null because no instance pins one, and the
	// absent marker is the packet's only workflow claim. A work item with an
	// instance keeps the bytes it has always carried.
	if snapshot.WorkflowInstance == store.WorkflowInstanceAbsent {
		pinned["workflow_step"] = nil
		pinned["workflow_instance"] = store.WorkflowInstanceAbsent
	}
	return payload
}

// proposalContextProjection carries the proposal record fields the
// dispatched lane packet renders: problem, user outcomes, and constraints.
// The typed record's optional lists normalize to empty arrays so the
// projected shape stays closed.
func proposalContextProjection(record *store.WorkflowProposalRecord) map[string]any {
	outcomes := record.UserOutcomes
	if outcomes == nil {
		outcomes = []string{}
	}
	constraints := record.Constraints
	if constraints == nil {
		constraints = []string{}
	}
	return map[string]any{"problem": record.Problem, "user_outcomes": outcomes, "constraints": constraints}
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
		ID          string `json:"knowledge_id"`
		Kind        string `json:"kind"`
		Locator     string `json:"locator"`
		Commit      string `json:"commit_oid,omitempty"`
		Hash        string `json:"content_hash,omitempty"`
		Status      string `json:"status,omitempty"`
		SuccessorID string `json:"successor_id,omitempty"`
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
		items = append(items, item{ID: v.ID, Kind: kind, Locator: v.NotePath, Commit: v.CommitOID, Hash: v.ContentHash, Status: store.KnowledgeLawStatus(v.Kind, v.OutcomeTag), SuccessorID: v.SuccessorID})
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
	lawStatus, successorID := "", ""
	if q.Result != nil {
		lawStatus, successorID = q.Result.LawStatus, q.Result.SuccessorID
	}
	if q.Note != nil {
		v := q.Note.NotePath
		locator = &v
	}
	payload := map[string]any{"state": state, "locator": locator, "candidates": []string{}}
	// Law status and successor ride beside, and never replace, the PM1 Q10
	// locator state: state stays canonical/not_compacted/missing/ambiguous.
	if lawStatus != "" {
		payload["status"] = lawStatus
	}
	if successorID != "" {
		payload["successor_id"] = successorID
	}
	return r.resultEnvelope(base, q.ResultMeta, r.scope(q.ResultMeta), payload)
}

// SignedCursor remains an agent-facing alias while the authority owns its
// representation and authenticated serialization.
type SignedCursor = store.SignedCursor
