package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/store"
)

// CallEnvelope is the hidden TS5 portion of an invoke request. The CLI accepts
// it only from trusted client code; model-facing input is validated separately.
type CallEnvelope struct {
	SchemaVersion       string `json:"schema_version"`
	RequestID           string `json:"request_id"`
	ClientRef           string `json:"client_ref"`
	PrincipalRef        string `json:"principal_ref"`
	SessionRef          string `json:"session_ref"`
	AgentRef            string `json:"agent_ref"`
	Directory           string `json:"directory"`
	Worktree            string `json:"worktree"`
	AmbientProjectID    string `json:"ambient_project_id"`
	SelectedProductID   string `json:"selected_product_id,omitempty"`
	ScopeVersion        string `json:"scope_version"`
	ManifestDigest      string `json:"manifest_digest"`
	HostAssertionDigest string `json:"host_assertion_digest,omitempty"`
}

type InvokeRequest struct {
	CallEnvelope json.RawMessage `json:"call_envelope"`
	Tool         string          `json:"tool"`
	Operation    string          `json:"operation"`
	Input        json.RawMessage `json:"input"`
}

func DecodeInvokeRequest(data []byte) (InvokeRequest, CallEnvelope, error) {
	if len(data) == 0 || len(data) > MaxRequestBytes {
		return InvokeRequest{}, CallEnvelope{}, fmt.Errorf("invoke input exceeds %d bytes", MaxRequestBytes)
	}
	if err := payloadschema.ValidateUniqueJSON(data); err != nil {
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

// effectiveLimit resolves the unified TS3 §3 pagination alias: the top-level
// limit wins when both it and page.limit are nonzero, and 0 means the
// operation's server default. Every paginated read handler selects its
// effective limit through this one expression. Each alias field is omitempty so a
// call that does not send it marshals to the same cursor binding bytes as
// before, and in-flight cursors survive the alias.
func effectiveLimit(topLevel int, page pageInput) int {
	if topLevel != 0 {
		return topLevel
	}
	return page.Limit
}

// budgetInput carries the only agent-facing operation budget: the
// caller deadline parsed from the input top level. Time is bounded only by
// requested_budget_seconds and size only by limit/page. SupportedSeconds is
// the operation's declared ceiling from the contract registry, never
// caller-controlled. CeilingRefused marks requested-above-supported; the
// refusal itself is minted by the caller's admission point, because reads
// refuse at dispatch while mutations refuse only after their idempotency
// lookup.
type budgetInput struct {
	RequestedSeconds int  `json:"-"`
	SupportedSeconds int  `json:"-"`
	CeilingRefused   bool `json:"-"`
}
type productResolveInput struct {
	ProductID string    `json:"product_id"`
	ProjectID string    `json:"project_id"`
	Page      pageInput `json:"page"`
	Limit     int       `json:"limit,omitempty"`
}
type productSnapshotInput struct {
	ProductID    string    `json:"product_id"`
	ProjectIDs   []string  `json:"project_ids"`
	PreviewLimit int       `json:"preview_limit"`
	Page         pageInput `json:"page"`
	Limit        int       `json:"limit,omitempty"`
}
type workListInput struct {
	ProductID     string    `json:"product_id"`
	ProjectIDs    []string  `json:"project_ids"`
	WorkIDs       []string  `json:"work_ids"`
	Lifecycle     string    `json:"lifecycle"`
	Kind          string    `json:"kind"`
	Detail        string    `json:"detail"`
	TerminalSince *string   `json:"terminal_since"`
	Page          pageInput `json:"page"`
	Limit         int       `json:"limit,omitempty"`
}
type historyInput struct {
	WorkID      string    `json:"work_id"`
	Direction   string    `json:"direction"`
	RecordKinds []string  `json:"record_kinds"`
	Page        pageInput `json:"page"`
	Limit       int       `json:"limit,omitempty"`
}
type relationInput struct {
	WorkID        string   `json:"work_id"`
	RelationKinds []string `json:"relation_kinds"`
	Direction     string   `json:"direction"`
	Depth         int      `json:"depth"`
}
type workIDInput struct {
	WorkID string `json:"work_id"`
}
type domainReadInput struct {
	ProductID string    `json:"product_id"`
	DomainID  string    `json:"domain_id"`
	Page      pageInput `json:"page"`
	Limit     int       `json:"limit,omitempty"`
}

type runtime struct {
	Store                  *store.Store
	Authority              *Service
	Envelope               CallEnvelope
	Tool, Operation        string
	Budget                 budgetInput
	Reader                 Authority
	mutationIdempotencyKey string
}

// Dispatch validates the generated input schema, revalidates TS5 authority,
// and routes both read and transaction-bound mutation operations through the
// generated contract surface.
func Dispatch(ctx context.Context, s *store.Store, authority *Service, request InvokeRequest, env CallEnvelope) (agentcontract.Envelope, error) {
	base := agentcontract.NewBase(env.RequestID, request.Tool, request.Operation)
	op, ok := agentcontract.ValidateContractOperation(request.Tool, request.Operation)
	if !ok {
		return base, errors.New("unsupported tool operation")
	}
	// These reads assemble current law or workflow pins; enriched mutation
	// results publish pins too. Attach disclosure before size admission and
	// replay, not after a mutation has committed its response.
	if operationUsesCurrentLaw(op) {
		base.Warnings = append(base.Warnings, agentcontract.Notice{
			Kind: "law_remote_freshness_unchecked", SourceID: "origin/HEAD",
			Details: map[string]any{"message": "Current law uses locally fetched origin/HEAD; remote freshness is unchecked. At the designated Git home, run git fetch origin and retry; no checkout advance is required."},
		})
	}
	if op.ID == "concord_work_transition.workflow_action" {
		var refusal *agentcontract.Envelope
		ctx, refusal = preflightDispatchWorkflowAction(ctx, s, authority, request, env, op, base)
		if refusal != nil {
			return *refusal, nil
		}
	} else if err := agentcontract.ValidateOperationPayload(request.Tool, request.Operation, request.Input, false); err != nil {
		return coreError(base, "invalid_input", err.Error(), "correct_request", false), nil
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
	// Reads have no idempotency identity, so ceiling admission for
	// a read happens here, immediately after strict input validation. A
	// mutation must not refuse here — its idempotency lookup precedes budget
	// admission, and the mutation paths enforce the ceiling themselves.
	if op.Kind == agentcontract.OperationRead && budget.CeilingRefused {
		readRefusal := runtime{Tool: request.Tool, Operation: request.Operation, Budget: budget}
		return readRefusal.budgetRefusal(base, fmt.Sprintf("requested_budget_seconds %d exceeds supported %d", budget.RequestedSeconds, budget.SupportedSeconds)), nil
	}
	if s == nil || authority == nil {
		return coreError(base, "unreachable", "authority is not available", "contact_operator", true), nil
	}
	return dispatchAuthorizedRequest(ctx, s, authority, request, env, op, base, budget)
}

func operationUsesCurrentLaw(op agentcontract.ContractOperation) bool {
	switch op.ID {
	case "concord_domain.list", "concord_domain.detail",
		"concord_work_trace.history", "concord_work_trace.mutation_result",
		"concord_product_view.snapshot",
		"concord_work_browse.list":
		return true
	}
	return slices.Contains(agentcontract.GeneratedPayloadRules[op.ResultSchema].Properties, "work_pins")
}

func preflightDispatchWorkflowAction(ctx context.Context, s *store.Store, authority *Service, request InvokeRequest, env CallEnvelope, op agentcontract.ContractOperation, base agentcontract.Envelope) (context.Context, *agentcontract.Envelope) {
	refuse := func(out agentcontract.Envelope) (context.Context, *agentcontract.Envelope) { return ctx, &out }
	// Generated outer-shape validation rejects duplicate or missing action
	// fields. Semantic workflow payload validation follows actor authorization.
	if err := agentcontract.ValidateOperationPayload(request.Tool, request.Operation, request.Input, false); err != nil {
		return refuse(coreError(base, "invalid_input", err.Error(), "correct_request", false))
	}
	var strictAction actionMutationInput
	if err := decodeOperationInput(request.Input, &strictAction); err != nil {
		return refuse(coreError(base, "invalid_input", err.Error(), "", false))
	}
	if len(strictAction.WorkID) < 2 || len(strictAction.WorkID) > 128 {
		return refuse(coreError(base, "invalid_input", "workflow action work_id is malformed", "", false))
	}
	if s == nil || authority == nil {
		return refuse(coreError(base, "unreachable", "workflow authority is not available", "contact_operator", true))
	}
	// The workflow actor tuple and the idempotency partition both key on
	// principal_ref, which is derived from the registered client rather
	// than accepted from input. Identity
	// authorization therefore precedes the preflight; Product and Project
	// scope stay out of it, because scope is validated after the budget.
	actorInv := Invocation{ClientRef: env.ClientRef, SessionRef: env.SessionRef, AgentRef: env.AgentRef, Directory: env.Directory, Worktree: env.Worktree, ManifestDigest: env.ManifestDigest, HostAssertionDigest: env.HostAssertionDigest, RequiredCapability: op.Capability, RequiredOperation: request.Operation}
	actor, actorErr := authority.Authorize(ctx, actorInv)
	if actorErr != nil {
		return refuse(failureEnvelope(base, actorErr))
	}
	if err := preflightWorkflowActionRequest(ctx, s, request.Input, env, actor); err != nil {
		return refuse(failureEnvelope(base, err))
	}
	return ctx, nil
}

func dispatchAuthorizedRequest(ctx context.Context, s *store.Store, authority *Service, request InvokeRequest, env CallEnvelope, op agentcontract.ContractOperation, base agentcontract.Envelope, budget budgetInput) (agentcontract.Envelope, error) {
	inv := envelopeInvocation(env, op.Capability, request.Operation)
	if op.Kind != agentcontract.OperationRead && op.ID != "concord_work_transition.workflow_action" {
		if _, identityExtractErr := extractMutationWorkIdentity(request.Input); identityExtractErr != nil {
			return base, identityExtractErr
		}
		identityInv := inv
		identityInv.ProductID, identityInv.ProjectID = "", ""
		identityGrant, identityErr := authority.Authorize(ctx, identityInv)
		if identityErr != nil {
			return failureEnvelope(base, identityErr), nil
		}
		r := runtime{Store: s, Authority: authority, Envelope: env, Tool: request.Tool, Operation: request.Operation, Budget: budget, Reader: identityGrant, mutationIdempotencyKey: idempotencyKey(request.Input)}
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
		if errors.As(err, &stale) && stale.Refreshable && op.Kind == agentcontract.OperationRead && slices.Equal(stale.Candidates, grant.CandidateProducts) {
			base.Warnings = append(base.Warnings, agentcontract.Notice{Kind: "context_refreshed"})
			env.ScopeVersion = stale.CurrentScopeVersion
		} else {
			return failureEnvelope(base, err), nil
		}
	}
	if err := validateRequestedScope(ctx, s, env, grant, request, op.Kind); err != nil {
		return failureEnvelope(base, err), nil
	}
	r := runtime{Store: s, Authority: authority, Envelope: env, Tool: request.Tool, Operation: request.Operation, Budget: budget, Reader: grant, mutationIdempotencyKey: idempotencyKey(request.Input)}
	if op.Kind != agentcontract.OperationRead {
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

func applyBudget(ctx context.Context, op agentcontract.ContractOperation, raw []byte) (context.Context, context.CancelFunc, budgetInput, *budgetFailure) {
	var envelope struct {
		RequestedSeconds int `json:"requested_budget_seconds"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return ctx, func() {}, budgetInput{}, &budgetFailure{kind: "invalid_input", message: "budget fields are not valid"}
	}
	var budget budgetInput
	budget.RequestedSeconds = envelope.RequestedSeconds
	budget.SupportedSeconds = op.SupportedBudgetSeconds
	if budget.RequestedSeconds < 0 {
		return ctx, func() {}, budget, &budgetFailure{kind: "invalid_input", message: "requested_budget_seconds must be at least 1"}
	}
	if budget.RequestedSeconds > budget.SupportedSeconds {
		budget.CeilingRefused = true
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
func (r runtime) budgetRefusal(base agentcontract.Envelope, message string) agentcontract.Envelope {
	supported := r.Budget.SupportedSeconds
	if supported < 1 {
		if op, ok := agentcontract.ValidateContractOperation(r.Tool, r.Operation); ok {
			supported = op.SupportedBudgetSeconds
		}
	}
	out := coreError(base, "budget_refused", message, "adjust_budget", false)
	out.Error.SupportedBudgetSeconds = supported
	return out
}

// validateRequestedScope checks every Product-scoped reference the request
// names against the grant before dispatch. A Product the policy authorizes is
// readable across the ambient selection (TS5 §2.3); spanning outside the
// selected Product by mutation keeps the cross_scope capability gate.
func validateRequestedScope(ctx context.Context, s *store.Store, env CallEnvelope, grant Authority, request InvokeRequest, kind agentcontract.OperationKind) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(request.Input, &fields); err != nil {
		return err
	}
	identity, err := extractMutationWorkIdentity(request.Input)
	if err != nil {
		return err
	}
	workIDs := identity.WorkIDs
	if len(workIDs) > 0 {
		scopes, err := s.ProductsForWorkIDs(ctx, workIDs)
		if err != nil {
			return err
		}
		if err := validateWorkReferenceScope(env, grant, kind, workIDs, scopes); err != nil {
			return err
		}
	}
	if raw, ok := fields["product_id"]; ok {
		var product string
		if json.Unmarshal(raw, &product) == nil && product != "" && !slices.Contains(grant.ProductScope, product) {
			return newRuntimeFailure("unauthorized", "Product is outside grant scope", "contact_operator", false)
		}
	}
	return validateProjectReferenceScope(ctx, s, env, grant, kind, fields)
}

func validateProjectReferenceScope(ctx context.Context, s *store.Store, env CallEnvelope, grant Authority, kind agentcontract.OperationKind, fields map[string]json.RawMessage) error {
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
				if kind != agentcontract.OperationRead && env.SelectedProductID != "" && !slices.Contains(products, env.SelectedProductID) && !containsCapability(grant.Capabilities, agentcontract.Capability("cross_scope")) {
					return newRuntimeFailure("unauthorized", "cross-Product mutation requires cross_scope capability", "contact_operator", false)
				}
			}
		}
	}
	return nil
}

// validateWorkReferenceScope is the Product policy for direct work references.
// Every referenced work's Products must intersect the grant, and a mutation
// outside the selected Product also needs cross_scope.
func validateWorkReferenceScope(env CallEnvelope, grant Authority, kind agentcontract.OperationKind, workIDs []string, scopes map[string][]string) error {
	for _, id := range workIDs {
		products, ok := scopes[id]
		if !ok {
			return newRuntimeFailure("unknown_scope", "work reference is not in Product scope", "reread_entities", false)
		}
		if !scopeIntersects(products, grant.ProductScope) {
			return newRuntimeFailure("unauthorized", "work reference is outside authorized Product scope", "contact_operator", false)
		}
		if kind != agentcontract.OperationRead && env.SelectedProductID != "" && !slices.Contains(products, env.SelectedProductID) && !containsCapability(grant.Capabilities, agentcontract.Capability("cross_scope")) {
			return newRuntimeFailure("unauthorized", "cross-Product mutation requires cross_scope capability", "contact_operator", false)
		}
	}
	return nil
}

type mutationWorkIdentity struct {
	WorkIDs []string
}

func extractMutationWorkIdentity(raw []byte) (mutationWorkIdentity, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return mutationWorkIdentity{}, err
	}
	var result mutationWorkIdentity
	for _, field := range []string{"work_id", "initiative_work_id", "child_work_id", "from_work_id", "to_work_id"} {
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
	return result, nil
}
func scopeIntersects(left, right []string) bool {
	for _, a := range left {
		if slices.Contains(right, a) {
			return true
		}
	}
	return false
}

// Invoke is the byte-oriented core boundary. cmd/concord's invoke verb and the
// agent boundary corpus both enter here, so the corpus exercises the same
// composition production runs rather than a test-only shortcut.
//
// It decodes strictly, dispatches, and shapes a dispatch failure into the typed
// envelope the caller receives. Only a decode failure is returned as an error,
// because only a decode failure means no envelope can be addressed.
func Invoke(ctx context.Context, s *store.Store, authority *Service, data []byte) (agentcontract.Envelope, error) {
	request, env, err := DecodeInvokeRequest(data)
	if err != nil {
		return agentcontract.Envelope{}, err
	}
	response, dispatchErr := Dispatch(ctx, s, authority, request, env)
	return shapeInvokeFailure(response, dispatchErr, request, env), nil
}

// shapeInvokeFailure converts a dispatch error into the typed envelope an
// operator receives. A dispatch failure is a product outcome, not a transport
// fault, so it must not reach the caller as a bare Go error.
func shapeInvokeFailure(response agentcontract.Envelope, dispatchErr error, request InvokeRequest, env CallEnvelope) agentcontract.Envelope {
	if dispatchErr == nil {
		return response
	}
	base := agentcontract.NewBase(env.RequestID, request.Tool, request.Operation)
	return agentcontract.NewCoreError(base, agentcontract.TypedError{Kind: "invalid_input", RetrySafe: false, RecoveryAction: agentcontract.RecoveryAction{Kind: "restart_query"}, EffectState: agentcontract.EffectNone, Message: dispatchErr.Error()})
}

func validateRuntimeScope(ctx context.Context, s *store.Store, env CallEnvelope, grant Authority, kind agentcontract.OperationKind) error {
	if env.AmbientProjectID == "" {
		return newRuntimeFailure("unknown_scope", "ambient Project is required", "resolve_ambiguity", false)
	}
	if !slices.Contains(grant.ProjectScope, env.AmbientProjectID) {
		return newRuntimeFailure("unauthorized", "Project is outside grant scope", "contact_operator", false)
	}
	version, candidates, err := s.ScopeVersion(ctx, env.AmbientProjectID)
	if err != nil {
		return err
	}
	if env.ScopeVersion != "" && env.ScopeVersion != version {
		f := newRuntimeFailure("stale_context", "scope version is stale", "refresh_context", kind == agentcontract.OperationRead)
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
	if !slices.Contains(candidates, env.SelectedProductID) {
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
	if !slices.Contains(grant.ProductScope, env.SelectedProductID) {
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
	Details     map[string]any
}

func (f *runtimeFailure) Error() string { return f.message }
func newRuntimeFailure(kind, message, recovery string, retry bool) *runtimeFailure {
	if recovery == "" {
		recovery = agentcontract.PublicRecovery(kind, recovery)
	}
	return &runtimeFailure{kind: kind, message: message, recovery: recovery, retry: retry}
}

func failureEnvelope(base agentcontract.Envelope, err error) agentcontract.Envelope {
	// A failure marked EffectPossible followed a committed transaction,
	// so it must not read as a clean timeout even when its
	// wrapped cause is a deadline. The store-failure branch below carries
	// the possible effect.
	var effectFailure *store.Failure
	effectPossible := errors.As(err, &effectFailure) && effectFailure.EffectPossible
	if errors.Is(err, context.DeadlineExceeded) && !effectPossible {
		// Expiry before any durable effect claims no effect. The
		// store rolls an open transaction back on error, so an in-process
		// deadline reaching this point means no commit happened. External
		// effects that outlive cancellation are the native-run surface's
		// problem, not this envelope's.
		return coreError(base, "timeout", "operation budget expired", "retry_same_request", true)
	}
	var f *runtimeFailure
	if errors.As(err, &f) {
		action := agentcontract.RecoveryAction{Kind: f.recovery}
		out := coreErrorAction(base, f.kind, f.message, action, f.retry, f.Candidates)
		out.Error.Details = f.Details
		return out
	}
	var sf *store.Failure
	if errors.As(err, &sf) {
		return storeFailureEnvelope(base, sf)
	}
	return coreError(base, "internal_error", err.Error(), "contact_operator", false)
}

func storeFailureEnvelope(base agentcontract.Envelope, sf *store.Failure) agentcontract.Envelope {
	kind := mapFailureKind(sf.Kind)
	proposed := sf.RecoveryAction
	// These store kinds describe operation state or retained knowledge, not
	// request shape, despite sharing the public invalid_input kind.
	requestInput := kind == "invalid_input" && sf.Kind != store.KindInvalidOperation && sf.Kind != store.KindInvalidNoteProof && sf.Kind != store.KindKnowledgeMissing
	if kind == "invalid_input" && !requestInput && !agentcontract.RecoveryActionAllowed(proposed) {
		proposed = "reread_entities"
	}
	recovery := agentcontract.PublicRecovery(kind, proposed)
	if recovery == "correct_request" && (!requestInput || sf.RetrySafe || sf.EffectPossible || len(sf.CommittedRefs) > 0) {
		recovery = "reread_entities"
		if kind != "invalid_input" {
			recovery = agentcontract.PublicRecovery(kind, "")
		}
		if sf.EffectPossible || len(sf.CommittedRefs) > 0 {
			recovery = "reconcile_operation"
		}
	}
	// Candidate identities ride the error at construction, before the
	// envelope's first validation: an ambiguous_scope refusal the caller
	// cannot act on is one that never marshals. The store carries them on
	// every ambiguous-scope refusal it mints.
	message := sf.Detail
	if kind == "unreachable" && sf.Err != nil {
		// The cause tells contention from a full disk or a corrupt file;
		// the detail alone reads the same for all three.
		message += ": " + sf.Err.Error()
	}
	out := coreErrorAction(base, kind, message, agentcontract.RecoveryAction{Kind: recovery}, sf.RetrySafe, nonNilStrings(sf.CandidateIDs))
	// The store authors free remedy prose in RecoveryAction — the exact
	// command a caller must run. agentcontract.PublicRecovery projects that
	// field onto the closed action vocabulary, and the projection alone drops
	// the prose; a coupled kind keeps its coupled action (the coupling is not
	// weakened), so the authored text rides the bounded, schema-valid
	// details surface unchanged beside it.
	if prose := sf.RecoveryAction; prose != "" && !agentcontract.RecoveryActionAllowed(prose) {
		if out.Error.Details == nil {
			out.Error.Details = map[string]any{}
		}
		out.Error.Details["recovery_detail"] = boundedErrorMessage(prose)
	}
	// Carry typed current-version carriers into the agent envelope so
	// callers can recover the live projection version structurally without
	// having to parse the human detail string. Mirrors the same path for
	// typed violations.
	for _, current := range sf.CurrentVersions {
		out.Error.CurrentVersions = append(out.Error.CurrentVersions, agentcontract.ChangedRef{EntityKind: string(current.SubjectType), ID: current.SubjectID, Version: strconv.FormatInt(current.Version, 10)})
	}
	for _, action := range sf.InterveningActions {
		out.Error.InterveningActions = append(out.Error.InterveningActions, agentcontract.InterveningAction{ActionID: action.ActionID, SessionRef: action.SessionRef})
	}
	if len(sf.Violations) > 0 {
		out.Error.Violations = append(out.Error.Violations, sf.Violations...)
	}
	if sf.EffectPossible || len(sf.CommittedRefs) > 0 {
		out.Error.EffectState = agentcontract.EffectPossible
		out.ChangedRefs = committedChangedRefs(sf.CommittedRefs)
	}
	return out
}

func committedChangedRefs(refs []store.SubjectCurrentVersion) *[]agentcontract.ChangedRef {
	if len(refs) == 0 {
		return nil
	}
	changed := make([]agentcontract.ChangedRef, 0, len(refs))
	for _, ref := range refs {
		changed = append(changed, agentcontract.ChangedRef{EntityKind: string(ref.SubjectType), ID: ref.SubjectID, Version: strconv.FormatInt(ref.Version, 10)})
	}
	return &changed
}

// nonNilStrings copies a string list so an empty input marshals as an empty
// array rather than null.
func nonNilStrings(values []string) []string {
	return append(make([]string, 0, len(values)), values...)
}

func coreError(base agentcontract.Envelope, kind, message, recovery string, retry bool) agentcontract.Envelope {
	if recovery == "" {
		recovery = agentcontract.PublicRecovery(kind, recovery)
	}
	return coreErrorAction(base, kind, message, agentcontract.RecoveryAction{Kind: recovery}, retry, nil)
}

// coreErrorAction carries typed candidate identities so the caller can act on
// a refusal. Envelope validation requires them for an ambiguous_scope error.
func coreErrorAction(base agentcontract.Envelope, kind, message string, action agentcontract.RecoveryAction, retry bool, candidates []string) agentcontract.Envelope {
	message = boundedErrorMessage(message)
	// An unreachable refusal says the core could not answer, so the envelope
	// carries no authoritative claim, freshness, or watermark; the contract
	// pairs the kind with that authority and refuses any other pairing.
	authority := agentcontract.AuthorityAuthoritative
	if kind == "unreachable" {
		authority = agentcontract.AuthorityUnreachable
		base.Freshness = nil
		base.SourceVersionWatermark = []agentcontract.Watermark{}
	}
	base.Authority = authority
	base.Outcome = agentcontract.OutcomeError
	base.Error = &agentcontract.TypedError{Kind: kind, RetrySafe: retry, RecoveryAction: action, EffectState: agentcontract.EffectNone, Message: message, Candidates: candidates}
	if _, err := base.Encode(); err == nil {
		return base
	}
	// Error delivery must remain possible when the rejected success inherited
	// optional fields large enough to exceed the envelope cap. Rebuild the base
	// from its bounded identity only when preserving metadata is undeliverable.
	// The typed error is the only authoritative response to a rejected result.
	errorBase := agentcontract.NewBase(base.RequestID, base.Tool, base.Operation)
	errorBase.ResolvedScope = base.ResolvedScope
	errorBase.Authority = authority
	errorBase.Outcome = agentcontract.OutcomeError
	errorBase.Error = &agentcontract.TypedError{Kind: kind, RetrySafe: retry, RecoveryAction: action, EffectState: agentcontract.EffectNone, Message: message}
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
	// A producer that must keep refusal content whole renders it inside this
	// budget before the message reaches the boundary. When a producer exceeds
	// the budget anyway, the cut is marked rather than silent, so a shortened
	// refusal never reads as the complete decision it is not.
	const marker = "… [truncated]"
	cut := maxBytes - len(marker)
	if cut < 0 {
		cut = 0
	}
	return strings.ToValidUTF8(message[:cut], "") + marker
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
	case store.KindInvalidDefinition, store.KindDefinitionDigestMismatch, store.KindDefinitionActionOrStepUnknown:
		return "invariant_violation"
	case store.KindInvariantViolation, store.KindSchemaUnsupported:
		return "invariant_violation"
	case store.KindUnsupportedPayloadVersion:
		return "invariant_violation"
	case store.KindIllegalLifecycleTransition:
		return "invalid_transition"
	case store.KindInvalidRelation, store.KindCycleDetected, store.KindRelationConflict, store.KindRelationNotFound, store.KindRelationContractViolation:
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
	case store.KindUnauthorizedDispatch:
		// A dispatch admission refusal is an authority failure, not an internal
		// fault, regardless of the store's more specific kind name.
		return "unauthorized"
	case store.KindProjectionConflict:
		// Conflicting projection identities require coordination, not fault recovery.
		return "operation_conflict"
	case store.KindIndexDegraded:
		return "degraded_not_allowed"
	}
	// A store kind whose value is already a public kind passes through; the
	// cases above are the genuine renames. Collapsing one to internal_error
	// would report a self-answerable refusal as a fault and discard the
	// store's authored remedy.
	if payloadschema.TypedErrorKindAllowed(string(kind)) {
		return string(kind)
	}
	// A store kind that names no public kind is an internal fault to the
	// caller: the store raised something the contract cannot carry, which
	// is a defect to fix at the store, never a refusal to act on.
	return "internal_error"
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

// decodeOperationInput is the strict decoder for operation inputs. The
// shared schema supplies requested_budget_seconds to every operation
// through one definition rather than per-operation restatement, and the budget
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

func (r runtime) read(ctx context.Context, base agentcontract.Envelope, input []byte, queryID string) (agentcontract.Envelope, error) {
	response, err := r.readResult(ctx, base, input, queryID)
	if err != nil {
		return response, err
	}
	size, err := readEnvelopeSize(response)
	if err != nil {
		return response, err
	}
	if size > agentcontract.MaxResultEnvelopeBytes {
		return readSizeRefusal(response, size), nil
	}
	return response, nil
}

// readEnvelopeSize measures the final wire object, including authenticated
// cursors and notices. MarshalJSON retains the hard bound as a consumer backstop.
func readEnvelopeSize(response agentcontract.Envelope) (int, error) {
	if err := response.Validate(); err != nil {
		return 0, err
	}
	type wire agentcontract.Envelope
	raw, err := json.Marshal(wire(response))
	return len(raw), err
}

func readSizeRefusal(response agentcontract.Envelope, size int) agentcontract.Envelope {
	// No rejected payload or evidence belongs in a refusal. The store remains
	// unchanged, and the byte counts name a producer bound, not a transport fault.
	base := agentcontract.NewBase(response.RequestID, response.Tool, response.Operation)
	out := coreError(base, "limit_exceeded", fmt.Sprintf("read result including fixed projection and continuation requires %d serialized bytes; envelope maximum is %d bytes; reduce the page limit where supported, but a fixed projection or single item may not fit even at limit=1", size, agentcontract.MaxResultEnvelopeBytes), "reduce_limit", false)
	out.Error.Details = map[string]any{"serialized_bytes": size, "maximum_bytes": agentcontract.MaxResultEnvelopeBytes, "reason": "result_envelope_bytes"}
	return out
}

func (r runtime) readResult(ctx context.Context, base agentcontract.Envelope, input []byte, queryID string) (agentcontract.Envelope, error) {
	switch r.Tool + "." + r.Operation {
	case "concord_product_view.resolve":
		return r.readProductResolve(ctx, base, input)
	case "concord_product_view.snapshot":
		return r.readProductSnapshot(ctx, base, input)
	case "concord_work_browse.list":
		return r.readWorkList(ctx, base, input)
	case "concord_work_trace.history":
		return r.readTraceHistory(ctx, base, input)
	case "concord_work_trace.resume":
		return r.readTraceResume(ctx, base, input)
	case "concord_work_trace.mutation_result":
		return r.readMutationResult(ctx, base, input)
	case "concord_work_trace.relations":
		return r.readTraceRelations(ctx, base, input)
	case "concord_work_trace.project_retirement":
		return r.readTraceProjectRetirement(ctx, base, input)
	case "concord_domain.list", "concord_domain.detail":
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
func historyDirection(v string) string {
	if v == "incoming" {
		return "newest_first"
	}
	return "oldest_first"
}
func (r runtime) unwrapCursor(ctx context.Context, token, binding, detail string) (string, error) {
	if token == "" {
		return "", nil
	}
	expected := SignedCursor{Tool: r.Tool, Operation: r.Operation, Scope: r.Envelope.SelectedProductID + "|" + r.Envelope.AmbientProjectID, Filter: binding, Detail: detail, Order: "default"}
	if r.Tool == "concord_work_trace" && r.Operation == "mutation_result" {
		// The retained document is immutable. Unrelated writes must not
		// invalidate its continuation; the filter binds its exact identity.
		expected.Source = binding
	} else {
		watermark, err := r.Store.Revision(ctx)
		if err != nil {
			return "", err
		}
		expected.Source = strconv.FormatInt(watermark, 10)
	}
	cursor, err := r.Store.DecodeCursor(ctx, token, expected)
	if err != nil {
		return "", err
	}
	return cursor.Inner, nil
}

func (r runtime) wrapCursor(ctx context.Context, response agentcontract.Envelope, binding, detail string) (agentcontract.Envelope, error) {
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

func watermarkString(response agentcontract.Envelope) string {
	if len(response.SourceVersionWatermark) == 0 {
		return ""
	}
	return response.SourceVersionWatermark[0].Version
}

func applyMeta(base agentcontract.Envelope, meta store.ResultMeta, scope *agentcontract.Scope) agentcontract.Envelope {
	base.QueryID = meta.QueryID
	base.Authority = agentcontract.AuthorityLevel(meta.Authority)
	base.ResolvedScope = scope
	// A read that carries no observation time carries no freshness: the
	// envelope admits null there, and a fabricated zero instant fails the
	// marshal bound on every real call.
	base.Freshness = nil
	if observed := parseTime(meta.Freshness.ObservedAt); !observed.IsZero() {
		base.Freshness = &agentcontract.Freshness{ObservedAt: observed, Age: meta.Freshness.Age, Stale: meta.Freshness.Stale}
	}
	base.OrderingKeys = meta.OrderingKeys
	base.NextCursor = meta.NextCursor
	base.Omissions = notices(meta.Omissions)
	// Dispatch-level notices are recorded on the base envelope before the read
	// runs — TS5 §3's context_refreshed is the load-bearing case. Replacing the
	// slice here would drop the only signal that a stale read was refreshed, so
	// query warnings are appended within the envelope's bounded notice budget.
	base.Warnings = boundedNotices(base.Warnings, notices(meta.Warnings))
	base.SourceVersionWatermark = []agentcontract.Watermark{{SourceKind: "product_memory", SourceID: "sqlite", Version: strconv.FormatInt(meta.SourceVersionWatermark, 10)}}
	return base
}

func boundedNotices(existing, added []agentcontract.Notice) []agentcontract.Notice {
	out := append(append(make([]agentcontract.Notice, 0, len(existing)+len(added)), existing...), added...)
	if len(out) > agentcontract.MaxNotices {
		return out[:agentcontract.MaxNotices]
	}
	return out
}
func parseTime(v string) time.Time { t, _ := time.Parse(time.RFC3339Nano, v); return t }
func notices(values []string) []agentcontract.Notice {
	out := make([]agentcontract.Notice, 0, len(values))
	for _, v := range values {
		out = append(out, agentcontract.Notice{Kind: v})
	}
	return out
}
func (r runtime) scope(meta store.ResultMeta) *agentcontract.Scope {
	p := meta.ResolvedScope.ProductID
	if p == "" {
		p = r.Envelope.SelectedProductID
	}
	return &agentcontract.Scope{ProductID: p, ProjectIDs: nonEmpty(r.Envelope.AmbientProjectID), ScopeVersion: r.Envelope.ScopeVersion}
}
func (r runtime) resultEnvelope(base agentcontract.Envelope, meta store.ResultMeta, scope *agentcontract.Scope, payload any) (agentcontract.Envelope, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return base, err
	}
	base = applyMeta(base, meta, scope)
	base.Outcome = agentcontract.OutcomeOK
	base.Result = raw
	if err := agentcontract.ValidateOperationPayload(base.Tool, base.Operation, raw, true); err != nil {
		return base, err
	}
	return base, nil
}

type workSummary struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	// Task, the captured short note, rides the authoritative intent reads —
	// the single-record scope read and the full-detail list read. Bounded
	// summary and preview reads leave it empty.
	Task                  string                    `json:"task,omitempty"`
	Lifecycle             string                    `json:"lifecycle"`
	Version               int64                     `json:"version"`
	ProjectIDs            []string                  `json:"project_ids,omitempty"`
	Ready                 bool                      `json:"ready,omitempty"`
	TerminalAt            *string                   `json:"terminal_at"`
	CreatedAt             string                    `json:"created_at"`
	PendingProjectHandoff *store.ProjectHandoff     `json:"pending_project_handoff,omitempty"`
	LawContext            *store.WorkflowLawContext `json:"law_context,omitempty"`
	NativeRuns            []store.NativeRunReport   `json:"native_runs,omitempty"`
}

func summary(w store.WorkItem) workSummary {
	ids := make([]string, 0, len(w.Projects))
	for _, p := range w.Projects {
		ids = append(ids, p.ID)
	}
	kind := w.Kind
	if kind != "task" && kind != "bug" && kind != "decision" && kind != "research" && kind != "other" {
		kind = "other"
	}
	var terminal *string
	if w.TerminalAt != "" {
		terminal = &w.TerminalAt
	}
	return workSummary{ID: w.ID, Kind: kind, Title: w.Title, Task: w.Task, Lifecycle: w.Lifecycle, Version: w.Version, ProjectIDs: ids, Ready: w.Ready, TerminalAt: terminal, CreatedAt: w.CreatedAt, PendingProjectHandoff: w.PendingProjectHandoff, LawContext: w.LawContext, NativeRuns: w.NativeRuns}
}
func (r runtime) q1(base agentcontract.Envelope, q store.Q1Result) (agentcontract.Envelope, error) {
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
func (r runtime) q2(base agentcontract.Envelope, q store.Q2Result) (agentcontract.Envelope, error) {
	items := make([]workSummary, 0, len(q.Items))
	for _, w := range q.Items {
		items = append(items, summary(w))
	}
	return r.resultEnvelope(base, q.ResultMeta, r.scope(q.ResultMeta), map[string]any{"counts": map[string]int{"needed": q.LifecycleCounts["needed"], "in_progress": q.LifecycleCounts["in_progress"], "completed": q.LifecycleCounts["completed"], "cancelled": q.LifecycleCounts["cancelled"]}, "previews": items})
}

func (r runtime) q3(base agentcontract.Envelope, q store.Q3Result) (agentcontract.Envelope, error) {
	items := make([]workSummary, 0, len(q.Items))
	for _, w := range q.Items {
		items = append(items, summary(w))
	}
	return r.resultEnvelope(base, q.ResultMeta, r.scope(q.ResultMeta), map[string]any{"items": items})
}
func (r runtime) q7(base agentcontract.Envelope, q store.Q7Result) (agentcontract.Envelope, error) {
	entries := q.Entries
	if entries == nil {
		entries = []store.HistoryEntry{}
	}
	return r.resultEnvelope(base, q.ResultMeta, r.scope(q.ResultMeta), map[string]any{"entries": entries})
}

// fragmentLength returns the largest whole-code-point cut of
// doc[offset:] whose rendering fit accepts, so every page carries the
// largest fragment the measured final envelope admits, never merely one
// that fits. The complete remainder is rendered first: it is the largest
// candidate, and its final wire object drops the long projection
// continuation cursor, so it can fit when no shorter incomplete candidate
// does. Incomplete candidates are then searched only at whole-code-point
// boundaries: a mid-rune cut serializes its partial suffix as replacement
// characters, so arbitrary byte lengths cost more than the aligned cut
// below them, a plain byte search can prune the true maximum, and alignment
// restores the monotone rendered cost the search needs. A snap to zero is
// below the first admissible code point, never a failed fit: the search
// advances past it without rendering, and fit is never called for an empty
// cut. The snap walks back at most three bytes, so no rune index of the
// document is ever built. The first render error stops the search and
// propagates unchanged — a real failure must reach the caller as itself,
// never as a size verdict. Zero with no error means no admissible code
// point fits, and the caller refuses the frame breach honestly instead of
// hiding it.
func fragmentLength(doc []byte, offset int, fit func(length int) (bool, error)) (int, error) {
	remaining := len(doc) - offset
	if remaining == 0 {
		return 0, nil
	}
	fits, err := fit(remaining)
	if err != nil {
		return 0, err
	}
	if fits {
		return remaining, nil
	}
	low, high, best := 1, remaining-1, 0
	for low <= high {
		mid := (low + high) / 2
		cut := mid
		for cut > 0 && !utf8.RuneStart(doc[offset+cut]) {
			cut--
		}
		if cut == 0 {
			// Below the first admissible code point: every shorter candidate
			// snaps to zero too, so the lower half holds no renderable cut.
			low = mid + 1
			continue
		}
		fits, err := fit(cut)
		if err != nil {
			return 0, err
		}
		if fits {
			best = cut
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	return best, nil
}

func (r runtime) q8(base agentcontract.Envelope, q store.Q8Result) (agentcontract.Envelope, error) {
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
	// `Q8-relations` in .concord/scenarios/product-memory-query.v1.json, which asserts
	// $.edges[0].source / $.edges[0].target against the store projection
	// layer. Translating here keeps both contracts intact.
	edges := make([]map[string]any, 0, len(q.Edges))
	for _, e := range q.Edges {
		edges = append(edges, map[string]any{"from": e.Source, "to": e.Target, "kind": e.Kind, "depth": e.Depth, "relation_id": e.RelationID})
	}
	return r.resultEnvelope(base, q.ResultMeta, r.scope(q.ResultMeta), map[string]any{"nodes": []any{}, "edges": edges})
}

// SignedCursor remains an agent-facing alias while the authority owns its
// representation and authenticated serialization.
type SignedCursor = store.SignedCursor
