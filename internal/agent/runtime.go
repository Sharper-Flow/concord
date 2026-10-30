package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// budgetInput carries the only agent-facing operation budget: the CD-0038 D1
// caller deadline parsed from the input top level. The result-size budget
// object is retired (CD-0038 amendment, 2026-10-02): time is bounded only by
// requested_budget_seconds and size only by limit/page. SupportedSeconds is
// the operation's declared ceiling from the contract registry, never
// caller-controlled. CeilingRefused marks requested-above-supported; the
// refusal itself is minted by the caller's admission point, because reads
// refuse at dispatch while mutations refuse only after their idempotency
// lookup (CD-0038 D3).
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
	TagIDs        []string  `json:"tag_ids"`
	PriorityMin   *int64    `json:"priority_min"`
	PriorityMax   *int64    `json:"priority_max"`
	Detail        string    `json:"detail"`
	TerminalSince *string   `json:"terminal_since"`
	Page          pageInput `json:"page"`
	Limit         int       `json:"limit,omitempty"`
}
type workReadyInput struct {
	ProductID string    `json:"product_id"`
	ProjectID string    `json:"project_id"`
	Kind      string    `json:"kind"`
	Page      pageInput `json:"page"`
	Limit     int       `json:"limit,omitempty"`
}
type workBlockedInput struct {
	ProductID string    `json:"product_id"`
	ProjectID string    `json:"project_id"`
	WorkID    string    `json:"work_id"`
	Kind      string    `json:"kind"`
	Depth     int       `json:"depth"`
	Page      pageInput `json:"page"`
	Limit     int       `json:"limit,omitempty"`
}
type workScopeInput struct {
	ProductID string    `json:"product_id"`
	ProjectID string    `json:"project_id"`
	WorkID    string    `json:"work_id"`
	Page      pageInput `json:"page"`
	Limit     int       `json:"limit,omitempty"`
	OneOf     string    `json:"one_of"`
}
type messagesInput struct {
	ProductID string    `json:"product_id"`
	WorkID    string    `json:"work_id"`
	Page      pageInput `json:"page"`
	Limit     int       `json:"limit,omitempty"`
}

type worktreeAuditInput struct {
	ProductID string    `json:"product_id"`
	Page      pageInput `json:"page"`
	Limit     int       `json:"limit,omitempty"`
}

type worktreeInspectInput struct {
	WorkID string `json:"work_id"`
	Mode   string `json:"mode"`
	// Path is the relative file selector for file mode. It selects inside
	// the identity-derived worktree (CD-0096 D2), never a worktree path.
	Path   string `json:"path"`
	RunRef string `json:"run_ref"`
	Stream string `json:"stream"`
	Offset int64  `json:"offset"`
	Length int64  `json:"length"`
}

type researchReadInput struct {
	ProductID string    `json:"product_id"`
	PackID    string    `json:"pack_id"`
	WorkID    string    `json:"work_id"`
	Page      pageInput `json:"page"`
	Limit     int       `json:"limit,omitempty"`
	// ResultVersion selects the result contract: 0 and 1 keep the legacy full
	// research_pack result; 2 is the canonical read (exact revision and
	// finding selection, or the owner descriptor page). The runtime, not the
	// schema, refuses the combinations the flat input cannot express.
	ResultVersion int      `json:"result_version,omitempty"`
	Revision      int64    `json:"revision,omitempty"`
	FindingIDs    []string `json:"finding_ids,omitempty"`
}

type historyInput struct {
	WorkID          string    `json:"work_id"`
	Direction       string    `json:"direction"`
	RecordKinds     []string  `json:"record_kinds"`
	ContractVersion int64     `json:"contract_version,omitempty"`
	Page            pageInput `json:"page"`
	Limit           int       `json:"limit,omitempty"`
}
type observationReadInput struct {
	WorkID string    `json:"work_id"`
	Page   pageInput `json:"page"`
	Limit  int       `json:"limit,omitempty"`
}
type externalObservationReadInput struct {
	WorkID string    `json:"work_id"`
	Page   pageInput `json:"page"`
	// Limit keeps its CD-0040 evidence-fixed bound (schema maximum 64); the
	// page object is the unified TS3 §3 shape around it.
	Limit int `json:"limit,omitempty"`
}
type continuityInput struct {
	WorkID string    `json:"work_id"`
	Page   pageInput `json:"page"`
	Limit  int       `json:"limit,omitempty"`
}
type relationInput struct {
	WorkID        string   `json:"work_id"`
	RelationKinds []string `json:"relation_kinds"`
	Direction     string   `json:"direction"`
	Depth         int      `json:"depth"`
}
type workIDInput struct {
	WorkID string      `json:"work_id"`
	Budget budgetInput `json:"budget"`
}
type domainReadInput struct {
	ProductID string    `json:"product_id"`
	DomainID  string    `json:"domain_id"`
	Page      pageInput `json:"page"`
	Limit     int       `json:"limit,omitempty"`
}
type domainFootprintInput struct {
	ProductID string    `json:"product_id"`
	Paths     []string  `json:"paths,omitempty"`
	Behavior  string    `json:"behavior,omitempty"`
	Page      pageInput `json:"page"`
	Limit     int       `json:"limit,omitempty"`
}
type domainShapeInput struct {
	ProductID string `json:"product_id"`
	store.DomainShapeSelector
}
type knowledgeSearchInput struct {
	ProductID string   `json:"product_id"`
	ProjectID string   `json:"project_id"`
	DomainID  string   `json:"domain_id"`
	Kinds     []string `json:"kinds"`
	Tags      []string `json:"tags"`
	Text      string   `json:"text"`
	// AllowDegraded opts the caller in to CD-0008 D3 degraded enumeration: a
	// knowledge index behind the git head answers with authority "degraded" plus
	// omissions instead of failing closed. Default false keeps the fail-closed
	// path, so a caller never receives a silently incomplete answer.
	AllowDegraded bool      `json:"allow_degraded"`
	Page          pageInput `json:"page"`
	Limit         int       `json:"limit,omitempty"`
}
type knowledgeResolveInput struct {
	WorkID      string `json:"work_id"`
	KnowledgeID string `json:"knowledge_id"`
}

type runtime struct {
	removalProof           *store.DefaultCheckoutRemovalProof
	Store                  *store.Store
	Authority              *Service
	Registry               store.DefinitionRegistry
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
	return DispatchWithRegistry(ctx, s, authority, request, env, store.BuiltinWorkflowRegistry())
}

// DispatchWithRegistry is the same authenticated agent boundary with an
// explicitly pinned definition registry. Production callers use Dispatch; the
// seam lets replay and availability tests prove a missing registry before any
// payload or grant mutation path is reached.
func DispatchWithRegistry(ctx context.Context, s *store.Store, authority *Service, request InvokeRequest, env CallEnvelope, registry store.DefinitionRegistry) (agentcontract.Envelope, error) {
	if registry == nil {
		registry = store.BuiltinWorkflowRegistry()
	}
	base := agentcontract.NewBase(env.RequestID, request.Tool, request.Operation)
	op, ok := agentcontract.ValidateContractOperation(request.Tool, request.Operation)
	if !ok {
		return base, errors.New("unsupported tool operation")
	}
	if op.ID == "concord_work_transition.workflow_action" {
		var refusal *agentcontract.Envelope
		ctx, refusal = preflightDispatchWorkflowAction(ctx, s, authority, request, env, registry, op, base)
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
	// CD-0038 D3: reads have no idempotency identity, so ceiling admission for
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
	return dispatchAuthorizedRequest(ctx, s, authority, request, env, registry, op, base, budget)
}

func preflightDispatchWorkflowAction(ctx context.Context, s *store.Store, authority *Service, request InvokeRequest, env CallEnvelope, registry store.DefinitionRegistry, op agentcontract.ContractOperation, base agentcontract.Envelope) (context.Context, *agentcontract.Envelope) {
	refuse := func(out agentcontract.Envelope) (context.Context, *agentcontract.Envelope) { return ctx, &out }
	// Generated outer-shape validation rejects duplicate or missing action
	// fields. Semantic workflow payload validation remains below the registry
	// availability check.
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
	// principal_ref, which CD-0080 D1 derives rather than accepts. Identity
	// authorization therefore precedes the preflight; Product and Project
	// scope stay out of it, because scope is validated after the budget.
	actorInv := Invocation{ClientRef: env.ClientRef, SessionRef: env.SessionRef, AgentRef: env.AgentRef, Directory: env.Directory, Worktree: env.Worktree, ManifestDigest: env.ManifestDigest, HostAssertionDigest: env.HostAssertionDigest, RequiredCapability: op.Capability, RequiredOperation: request.Operation}
	actor, actorErr := authority.Authorize(ctx, actorInv)
	if actorErr != nil {
		return refuse(failureEnvelope(base, actorErr))
	}
	if err := s.CheckMemberOperation(ctx, memberWorkflowActor(actor), memberOperationScope(request.Input, env, op)); err != nil {
		return refuse(failureEnvelope(base, err))
	}
	available, availabilityErr := store.WorkflowActionAvailableWithRegistry(ctx, s, registry, strictAction.WorkID)
	if availabilityErr != nil {
		return refuse(failureEnvelope(base, availabilityErr))
	}
	if !available {
		// The refusal names the unverifiable pin's cause and migration
		// route (CD-0242 D13).
		message := workflowRegistryUnavailableMessage(ctx, s, registry, strictAction.WorkID)
		return refuse(coreError(base, "invalid_transition", message, "reread_entities", false))
	}
	if strictAction.ActionID == "dispatch_worker" {
		ctx = store.WithHostLeaseSet(ctx, s.ReadHostLeases())
	}
	if err := preflightWorkflowActionRequestWithRegistry(ctx, s, request.Input, env, actor, registry); err != nil {
		return refuse(failureEnvelope(base, err))
	}
	return ctx, nil
}

func workflowRegistryUnavailableMessage(ctx context.Context, s *store.Store, registry store.DefinitionRegistry, workID string) string {
	message := "workflow action registry is unavailable"
	if _, verifyErr := store.VerifyWorkflowInstanceDefinition(ctx, s, registry, workID); verifyErr != nil {
		var failure *store.Failure
		if errors.As(verifyErr, &failure) && failure.Detail != "" {
			message += ": " + failure.Detail
		}
	}
	return message
}

func dispatchAuthorizedRequest(ctx context.Context, s *store.Store, authority *Service, request InvokeRequest, env CallEnvelope, registry store.DefinitionRegistry, op agentcontract.ContractOperation, base agentcontract.Envelope, budget budgetInput) (agentcontract.Envelope, error) {
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
		if err := s.CheckMemberOperation(ctx, memberWorkflowActor(identityGrant), memberOperationScope(request.Input, env, op)); err != nil {
			return failureEnvelope(base, err), nil
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
	if err := s.CheckMemberOperation(ctx, memberWorkflowActor(grant), memberOperationScope(request.Input, env, op)); err != nil {
		return failureEnvelope(base, err), nil
	}
	ctx = store.WithSessionWorktreeOwner(ctx, store.SessionWorktreeOwner{ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef})
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
	r := runtime{Store: s, Authority: authority, Registry: registry, Envelope: env, Tool: request.Tool, Operation: request.Operation, Budget: budget, Reader: grant, mutationIdempotencyKey: idempotencyKey(request.Input)}
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
	// A pack_id names content owned by one immutable work item, so these
	// operations are authorized as references to that owner.
	switch request.Tool + "." + request.Operation {
	case "concord_work_trace.research", "concord_work_define.research_revision_append", "concord_work_define.research_finding_record", "concord_work_define.research_source_record", "concord_work_define.research_freshness_set":
		var in struct {
			PackID string `json:"pack_id"`
		}
		if err := json.Unmarshal(request.Input, &in); err != nil {
			return err
		}
		if in.PackID != "" {
			owners, err := s.ResearchPackOwnerWorkIDs(ctx, []string{in.PackID})
			if err != nil {
				return err
			}
			workIDs = append(workIDs, owners...)
		}
	}
	if len(workIDs) > 0 {
		scopes, err := s.ProductsForWorkIDs(ctx, workIDs)
		if err != nil {
			return err
		}
		if err := validateWorkReferenceScope(env, grant, kind, workIDs, scopes); err != nil {
			return err
		}
	}
	if err := validateKnowledgeReferenceScope(ctx, s, env, grant, fields); err != nil {
		return err
	}
	if raw, ok := fields["product_id"]; ok {
		var product string
		if json.Unmarshal(raw, &product) == nil && product != "" && !slices.Contains(grant.ProductScope, product) {
			return newRuntimeFailure("unauthorized", "Product is outside grant scope", "contact_operator", false)
		}
	}
	return validateProjectReferenceScope(ctx, s, env, grant, kind, fields)
}

func validateKnowledgeReferenceScope(ctx context.Context, s *store.Store, env CallEnvelope, grant Authority, fields map[string]json.RawMessage) error {
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
			if !scopeIntersects(products, grant.ProductScope) || env.SelectedProductID != "" && !slices.Contains(products, env.SelectedProductID) {
				return newRuntimeFailure("unauthorized", "knowledge reference is outside authorized Product scope", "contact_operator", false)
			}
		}
	}
	return nil
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

// validateWorkReferenceScope is the one Product policy for a work reference:
// a direct work ID or a research pack owner. Every
// referenced work's Products must intersect the grant, and a mutation outside
// the selected Product also needs cross_scope.
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

// authorizeWorkflowActionTx authorizes the caller inside the action
// transaction, then authorizes the owner of every declared research binding
// and every explicitly selected research pack (CD-0216 D2) under that grant,
// before the action writes a binding, a selection, or an idempotency
// receipt. A retained pin or retained selection with no new declaration is
// not rechecked.
func (r runtime) authorizeWorkflowActionTx(ctx context.Context, tx *store.Transaction, host store.ResolvedProjectHost, inv Invocation, bindings []researchBindingInput, selectedPacks []string) error {
	grant, err := r.Authority.AuthorizeTx(ctx, tx, host, inv)
	if err != nil {
		return err
	}
	ids := combinedResearchPackIDs(bindings, selectedPacks)
	owners, err := store.ResearchPackOwnerWorkIDsTx(ctx, tx, ids)
	if err != nil {
		return err
	}
	owners = dedupeStrings(owners)
	scopes, err := store.ProductsForWorkIDsTx(ctx, tx, owners)
	if err != nil {
		return err
	}
	return validateWorkReferenceScope(r.Envelope, grant, agentcontract.OperationMutation, owners, scopes)
}

// combinedResearchPackIDs merges the declared binding packs and the
// explicitly selected packs into one deduplicated identity list, first-seen
// order kept, so a pack both bound and selected on one action resolves its
// owner once. No identity is dropped: an unresolvable one keeps its typed
// absence refusal at the owner lookup.
func combinedResearchPackIDs(bindings []researchBindingInput, selectedPacks []string) []string {
	seen := make(map[string]bool)
	ids := make([]string, 0)
	for _, binding := range bindings {
		if !seen[binding.PackID] {
			seen[binding.PackID] = true
			ids = append(ids, binding.PackID)
		}
	}
	for _, packID := range selectedPacks {
		if !seen[packID] {
			seen[packID] = true
			ids = append(ids, packID)
		}
	}
	return ids
}

// dedupeStrings keeps the first occurrence of every value, order preserved.
func dedupeStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	kept := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		kept = append(kept, value)
	}
	return kept
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
// composition production runs rather than a test-only shortcut (issue #450).
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

// InvokeWithRegistry is Invoke with an injected definition registry. Production
// never needs it: Dispatch resolves the builtin registry, and the injection
// point exists so the workflow corpus boundary runner can drive a scenario
// registry through the real boundary.
func InvokeWithRegistry(ctx context.Context, s *store.Store, authority *Service, data []byte, registry store.DefinitionRegistry) (agentcontract.Envelope, error) {
	request, env, err := DecodeInvokeRequest(data)
	if err != nil {
		return agentcontract.Envelope{}, err
	}
	response, dispatchErr := DispatchWithRegistry(ctx, s, authority, request, env, registry)
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
	Details     map[string]any
}

func (f *runtimeFailure) Error() string { return f.message }
func newRuntimeFailure(kind, message, recovery string, retry bool) *runtimeFailure {
	if recovery == "" {
		recovery = agentcontract.PublicRecovery(kind, recovery)
	}
	return &runtimeFailure{kind: kind, message: message, recovery: recovery, retry: retry}
}

// newRouteFailure mints a refusal whose remedy is a declared route of
// workflow actions rather than an operator. route is ordered: the caller
// executes it front to back. unauthorized is deliberately absent from the
// agentcontract recovery coupling table, so a refusal of that kind may name
// the route it admits instead of the standing contact_operator default.
func newRouteFailure(kind, message string, route ...string) *runtimeFailure {
	return &runtimeFailure{kind: kind, message: message, recovery: "use_declared_route", retry: false, recoveryRefs: route}
}
func failureEnvelope(base agentcontract.Envelope, err error) agentcontract.Envelope {
	// A failure marked EffectPossible followed a committed transaction
	// (CD-0050 D2), so it must not read as a clean timeout even when its
	// wrapped cause is a deadline. The store-failure branch below carries
	// the possible effect.
	var effectFailure *store.Failure
	effectPossible := errors.As(err, &effectFailure) && effectFailure.EffectPossible
	if errors.Is(err, context.DeadlineExceeded) && !effectPossible {
		// CD-0038 D5: expiry before any durable effect claims no effect. The
		// store rolls an open transaction back on error, so an in-process
		// deadline reaching this point means no commit happened. External
		// effects that outlive cancellation are the native-run surface's
		// problem (CD-0039), not this envelope's.
		return coreError(base, "timeout", "operation budget expired", "retry_same_request", true)
	}
	var f *runtimeFailure
	if errors.As(err, &f) {
		action := agentcontract.RecoveryAction{Kind: f.recovery, RequiredRefs: f.recoveryRefs}
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
	// request shape, despite sharing the legacy public invalid_input kind.
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
	refs := sf.RecoveryRefs
	if recovery != "use_declared_route" {
		// Route refs ride the declared-route action alone; any other
		// action would carry refs the envelope contract never asked for.
		refs = nil
	} else if len(refs) == 0 {
		// A route naming no action cannot marshal. The kind's standing
		// default keeps the refusal the core decided deliverable.
		recovery = agentcontract.PublicRecovery(kind, "")
		refs = nil
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
	out := coreErrorAction(base, kind, message, agentcontract.RecoveryAction{Kind: recovery, RequiredRefs: refs}, sf.RetrySafe, nonNilStrings(sf.CandidateIDs))
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
	// typed violations: see D5.
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
	projectStoreFailureDetails(&out, sf)
	return out
}

func projectStoreFailureDetails(out *agentcontract.Envelope, sf *store.Failure) {
	if sf.StaleLawRevision != nil {
		out.Error.StaleLawRevision = &agentcontract.StaleLawRevision{OldLawID: sf.StaleLawRevision.OldLawID, OldContentHash: sf.StaleLawRevision.OldContentHash, RecoveryActions: append([]string(nil), sf.StaleLawRevision.RecoveryActions...)}
	}
	if sf.ExternalRefConflict != nil {
		out.Error.ExternalRefConflict = &agentcontract.ExternalRefConflict{ExistingWorkID: sf.ExternalRefConflict.ExistingWorkID, ExternalRef: sf.ExternalRefConflict.ExternalRef}
	}
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

// coreErrorAction is coreError with a fully built recovery action, for the
// refusals whose remedy is a declared route of workflow actions. candidates
// are the typed identities a refusal carries so the caller can act on it; the
// envelope validation holds an ambiguous_scope error to a non-empty list.
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
	// budget before the message reaches the boundary (see
	// store.recurrenceRefusalDetail). When a producer exceeds the budget
	// anyway, the cut is marked rather than silent, so a shortened refusal
	// never reads as the complete decision it is not.
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
	case store.KindInvalidRelation, store.KindCycleDetected, store.KindRelationConflict, store.KindRelationNotFound, store.KindRelationContractViolation, store.KindSupersessionTargetAlreadySuperseded, store.KindSupersessionSecondSuccessor:
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
	}
	// A store kind whose value is already a public kind passes through. The
	// cases above are the genuine renames, where the store's name and the
	// public name differ by intent. Everything else that names a public kind
	// is that kind; collapsing it to internal_error told the caller a fault
	// occurred and to contact the operator for a refusal it could have
	// answered itself, and agentcontract.PublicRecovery then discarded the
	// store's remedy.
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
	case "concord_work_browse.ready":
		return r.readWorkReady(ctx, base, input)
	case "concord_work_browse.blocked":
		return r.readWorkBlocked(ctx, base, input)
	case "concord_work_browse.scope":
		return r.readWorkScope(ctx, base, input)
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
	case "concord_work_trace.mutation_result":
		return r.readMutationResult(ctx, base, input)
	case "concord_work_trace.research":
		return r.readTraceResearch(ctx, base, input)
	case "concord_work_trace.relations":
		return r.readTraceRelations(ctx, base, input)
	case "concord_work_trace.project_retirement":
		return r.readTraceProjectRetirement(ctx, base, input)
	case "concord_knowledge.search":
		return r.readKnowledgeSearch(ctx, base, input)
	case "concord_knowledge.resolve_note":
		return r.readKnowledgeResolveNote(ctx, base, input)
	case "concord_knowledge.observe_publication":
		return r.readKnowledgeObservePublication(ctx, base, input)
	case "concord_domain.list", "concord_domain.detail":
		return r.readDomain(ctx, base, input, queryID)
	case "concord_domain.footprint":
		return r.readDomainFootprint(ctx, base, input)
	case "concord_domain.shape":
		return r.readDomainShape(ctx, base, input)
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
// projections up to its knowledge sources' current content. It runs with no
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
	switch {
	case r.Tool == "concord_work_trace" && r.Operation == "mutation_result":
		// The retained document is immutable. Unrelated writes must not
		// invalidate its continuation; the filter binds its exact identity.
		expected.Source = binding
	case r.Tool != "concord_knowledge":
		watermark, err := r.Store.Revision(ctx)
		if err != nil {
			return "", err
		}
		expected.Source = strconv.FormatInt(watermark, 10)
	default:
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
	// Task, ValueStatement, Tags, and WorkflowTypeRef carry the recorded
	// mutable-intent fields the store populates on the authoritative intent
	// reads — the single-record scope read and the full-detail list read —
	// because the schema declares them and a coordinator must be able to
	// carry the complete revisable intent into a complete-replacement
	// revision. Bounded summary and preview reads leave them empty, and
	// empty values stay omitted. Urgency is not intent detail: CD-0018
	// declares it on work_summary, so every summary carries the band.
	// DefectIntake rides the same authoritative reads: the immutable defect
	// classification and the core's sibling snapshot, so a coordinator can
	// retrieve the canonical cluster keys without reading storage.
	Task            string                      `json:"task,omitempty"`
	ValueStatement  string                      `json:"value_statement,omitempty"`
	Tags            []string                    `json:"tags,omitzero"`
	WorkflowTypeRef string                      `json:"workflow_type_ref,omitempty"`
	DefectIntake    *store.DefectClassification `json:"defect_intake,omitempty"`
	Size            *store.WorkSizeSummary      `json:"size,omitempty"`
	Urgency         string                      `json:"urgency"`
	Lifecycle       string                      `json:"lifecycle"`
	Version         int64                       `json:"version"`
	Priority        int64                       `json:"priority,omitempty"`
	ProjectIDs      []string                    `json:"project_ids,omitempty"`
	Ready           bool                        `json:"ready,omitempty"`
	Narrative       string                      `json:"narrative,omitempty"`
	TerminalAt      *string                     `json:"terminal_at"`
	CreatedAt       string                      `json:"created_at"`
	WorkPin         *store.WorkPin              `json:"work_pin,omitempty"`
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
	return workSummary{ID: w.ID, Kind: kind, Title: w.Title, Task: w.Task, ValueStatement: w.ValueStatement, Tags: w.Tags, WorkflowTypeRef: w.WorkflowTypeRef, DefectIntake: w.DefectIntake, Size: w.Size, Urgency: w.Urgency, Lifecycle: w.Lifecycle, Version: w.Version, Priority: w.Priority, ProjectIDs: ids, Ready: w.Ready, Narrative: w.Narrative, TerminalAt: terminal, CreatedAt: w.CreatedAt, WorkPin: w.WorkPin}
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
func (r runtime) q5(base agentcontract.Envelope, q store.Q5Result) (agentcontract.Envelope, error) {
	items := make([]workSummary, 0, len(q.Items))
	for _, w := range q.Items {
		items = append(items, summary(w))
	}
	return r.resultEnvelope(base, q.ResultMeta, r.scope(q.ResultMeta), map[string]any{"items": items})
}
func (r runtime) q4(base agentcontract.Envelope, q store.Q4Result) (agentcontract.Envelope, error) {
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
func (r runtime) q6(base agentcontract.Envelope, q store.Q6Result) (agentcontract.Envelope, error) {
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
			envelope.Omissions = append(envelope.Omissions, agentcontract.Notice{Kind: "redacted", SourceID: q.Work.ID, Details: map[string]any{"field": "verdict", "reason": "executing_actor_verdict_read_scope"}})
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

func (r runtime) q7(base agentcontract.Envelope, q store.Q7Result) (agentcontract.Envelope, error) {
	entries := q.Entries
	if entries == nil {
		entries = []store.HistoryEntry{}
	}
	payload := map[string]any{"entries": entries}
	if q.Workflow != nil {
		workflow, err := publishedWorkflowRead(q.Workflow)
		if err != nil {
			return base, err
		}
		payload["workflow"] = workflow
	}
	return r.resultEnvelope(base, q.ResultMeta, r.scope(q.ResultMeta), payload)
}

// workflowReadPublishedFields derives the published workflow_read field set
// from the generated contract's $defs/workflow_read/properties. The contract
// schema is the declared boundary, so the published read is exactly the set
// it declares: a projection field the contract names is published, and one it
// does not name is dropped, without a hand-maintained list that can drift.
func workflowReadPublishedFields() map[string]any {
	defs, _ := payloadschema.Document()["$defs"].(map[string]any)
	read, _ := defs["workflow_read"].(map[string]any)
	properties, _ := read["properties"].(map[string]any)
	return properties
}

// publishedWorkflowRead shapes the store projection into the published
// workflow_read subset. The history page carries it so a reader sees the
// current effective delivery assertion beside the unchanged original, with
// the target_payload_version a correction admission consumes.
func publishedWorkflowRead(projection *store.WorkflowReadProjection) (map[string]any, error) {
	raw, err := json.Marshal(projection)
	if err != nil {
		return nil, err
	}
	var shaped map[string]any
	if err := json.Unmarshal(raw, &shaped); err != nil {
		return nil, err
	}
	pruneWorkflowReadObject(shaped, workflowReadPublishedFields())
	return shaped, nil
}

// pruneWorkflowReadObject follows inline object and array shapes. Referenced
// definitions remain unchanged and are checked by payload validation.
func pruneWorkflowReadObject(value, properties map[string]any) {
	for field, child := range value {
		schema, ok := properties[field].(map[string]any)
		if !ok {
			delete(value, field)
			continue
		}
		switch child := child.(type) {
		case map[string]any:
			if nested, ok := schema["properties"].(map[string]any); ok {
				pruneWorkflowReadObject(child, nested)
			}
		case []any:
			items, _ := schema["items"].(map[string]any)
			if nested, ok := items["properties"].(map[string]any); ok {
				for _, item := range child {
					if object, ok := item.(map[string]any); ok {
						pruneWorkflowReadObject(object, nested)
					}
				}
			}
		}
	}
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
	pinned := map[string]any{"product_identity": snapshot.ProductIdentity, "workflow_step": snapshot.WorkflowStep, "step_actions": stepActions, "contract": snapshot.Contract, "spec_mandate": snapshot.SpecMandate, "pending_operator_decision": snapshot.PendingOperatorDecision, "withheld_operator_decision": snapshot.WithheldOperatorDecision, "latest_checkpoint": snapshot.LatestCheckpoint, "design_record": snapshot.DesignRecord}
	if snapshot.WorkPin != nil {
		pinned["work_pin"] = snapshot.WorkPin
	}
	if snapshot.ReviewInput != nil {
		pinned["review_input"] = snapshot.ReviewInput
	}
	// Empty peer signals stay out of the prompt. This keeps the unchanged
	// projection's bytes stable while non-empty signals remain visible.
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
		pinned["proposal_record"] = snapshot.ProposalRecord.PacketProposal()
	}
	// The work's newest unconsumed Project-session handoff rides the pinned
	// projection when present, so a Project-selected boot names the bounded
	// job the receiving session must consume before managed execution.
	if snapshot.PendingProjectHandoff != nil {
		pinned["pending_project_handoff"] = snapshot.PendingProjectHandoff
	}
	// The dispatch-ready worker-job revisions ride the pinned projection in
	// the packet's inputs.worker_job shape (CD-0205), so the dispatcher
	// selects one by job_id and binds its recorded content verbatim. The
	// absent field keeps work without ready jobs byte-stable.
	if len(snapshot.ReadyWorkerJobs) > 0 {
		pinned["ready_worker_jobs"] = snapshot.ReadyWorkerJobs
	}
	if len(snapshot.ChecksRepairReviewJobs) > 0 {
		pinned["checks_repair_review_jobs"] = snapshot.ChecksRepairReviewJobs
	}
	if len(snapshot.ReviewWorkerJobs) > 0 {
		pinned["review_worker_jobs"] = snapshot.ReviewWorkerJobs
	}
	// Resumed sessions receive available context or explicit unavailability.
	// Absence keeps context-free payload bytes unchanged.
	if snapshot.WorkContext != nil {
		pinned["work_context"] = snapshot.WorkContext
	}
	if snapshot.WorkContextUnavailable != nil {
		pinned["work_context_unavailable"] = snapshot.WorkContextUnavailable
	}
	payload := map[string]any{
		"work_id":            snapshot.WorkID,
		"pinned":             pinned,
		"boundaries":         map[string]any{"count": snapshot.BoundaryCount, "items": snapshot.Boundaries, "next_cursor": snapshot.NextCursor, "watermark": snapshot.Watermark},
		"typed_availability": map[string]any{"restart": "unavailable", "reason": snapshot.RestartUnavailableReason},
		"pending_messages":   snapshot.PendingMessages,
		"observations":       observations,
		"observations_total": snapshot.ObservationsTotal,
		// The window holds only the newest observations; this read pages the
		// whole population, newest first, in full detail.
		"observations_read": map[string]any{"tool": "concord_work_trace", "operation": "observations", "input": map[string]any{"work_id": snapshot.WorkID}},
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

// FitContinuity renders the largest complete prefix of one captured
// continuity snapshot whose serialized form fits maximum bytes. It first
// shrinks the newest-observation window, which the paged observations read
// backs, and then the boundary page, which its cursor resumes. render returns
// the caller's final wire object and its size, so session boot and the
// continuity read measure what they emit. When even one boundary and no
// observations exceed maximum, the smallest rendering returns with its size
// and the caller refuses.
func FitContinuity[T any](snapshot store.ContinuitySnapshot, req store.ContinuityRequest, maximum int, render func(store.ContinuitySnapshot) (T, int, error)) (T, int, error) {
	window := func(observations, boundaries int) (T, int, error) {
		fitted, err := snapshot.LimitWindow(req, observations, boundaries)
		if err != nil {
			var zero T
			return zero, 0, err
		}
		return render(fitted)
	}
	// largest returns the greatest n in [low, high] whose rendering fits,
	// or ok=false when low itself does not fit.
	largest := func(low, high int, at func(int) (T, int, error)) (T, int, bool, error) {
		best, size, err := at(low)
		if err != nil || size > maximum {
			return best, size, false, err
		}
		for low < high {
			mid := low + (high-low+1)/2
			out, outSize, err := at(mid)
			if err != nil {
				return out, outSize, false, err
			}
			if outSize <= maximum {
				best, size, low = out, outSize, mid
			} else {
				high = mid - 1
			}
		}
		return best, size, true, nil
	}
	boundaries := max(len(snapshot.Boundaries), 1)
	out, size, ok, err := largest(0, len(snapshot.Observations), func(n int) (T, int, error) { return window(n, boundaries) })
	if err != nil || ok {
		return out, size, err
	}
	out, size, _, err = largest(1, boundaries, func(n int) (T, int, error) { return window(0, n) })
	return out, size, err
}

// ContinuityCanonicalDocument renders the exact canonical JSON document of
// one captured continuity snapshot: the single public ContinuityPayload
// rendering, serialized once, with no wall-clock or read-time member — the
// envelope's freshness rides the envelope metadata, never this document. The
// returned digest binds the exact bytes, so a completed page sequence is one
// projection. The canonical document is the authority; the bounded view of
// its bytes stays separate.
func ContinuityCanonicalDocument(snapshot store.ContinuitySnapshot) ([]byte, string, error) {
	doc, err := json.Marshal(ContinuityPayload(snapshot))
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(doc)
	return doc, "sha256:" + hex.EncodeToString(sum[:]), nil
}

// ContinuityProjectionReference renders the closed projection reference for
// one captured continuity snapshot: the identity header, the canonical
// document's exact digest and byte length, the explicit omitted-projection
// status, and the canonical initial continuity read pointer at the read's
// default boundary window of 20. It carries no
// projection member and no fragment, claims no complete contract, question,
// intent set, or evidence, and changes no reading obligation; the
// authenticated page stream reconstruction precedes use, and a later read
// may describe newer state or a different session's held leases than this
// captured snapshot.
func ContinuityProjectionReference(snapshot store.ContinuitySnapshot) (map[string]any, error) {
	doc, digest, err := ContinuityCanonicalDocument(snapshot)
	if err != nil {
		return nil, err
	}
	header := continuityProjectionHeaderOf(snapshot)
	reference := map[string]any{
		"work_id":            header.WorkID,
		"workflow_instance":  header.WorkflowInstance,
		"workflow_step":      header.WorkflowStep,
		"watermark":          header.Watermark,
		"digest":             digest,
		"total_bytes":        len(doc),
		"projection_omitted": true,
		// Limit 20 is the read's default boundary window, not the boot
		// capture's window: boot captures its snapshot at limit 1
		// (cmd/concord/session.go), the digest field describes exactly that
		// captured document, and the later read this pointer starts is
		// separate — it can describe newer state or a different session's
		// held leases than the captured snapshot.
		"read_pointer": map[string]any{"tool": "concord_work_trace", "operation": "continuity", "input": map[string]any{"work_id": header.WorkID, "page": map[string]any{"cursor": nil, "limit": 20}}},
	}
	if header.WorkVersion != nil {
		reference["work_version"] = *header.WorkVersion
	}
	return reference, nil
}

// continuityResultMeta builds the continuity result metadata both the inline
// answer and every projection page carry: one source watermark, ordering
// keys, and the continuation the envelope cursor authenticates.
func continuityResultMeta(snapshot store.ContinuitySnapshot, next *string) store.ResultMeta {
	return continuityResultMetaObservedAt(snapshot, next, time.Now().UTC().Format(time.RFC3339Nano))
}

// continuityResultMetaObservedAt is continuityResultMeta with the freshness
// instant fixed by the caller. The projection pager pins one instant per page
// render so the fragment-length search and the final emission measure
// byte-identical envelopes: RFC3339Nano trims trailing zeros, so a fresh
// instant per candidate would move the measured size by a few bytes and
// strand the emit at one byte over the cap.
func continuityResultMetaObservedAt(snapshot store.ContinuitySnapshot, next *string, observedAt string) store.ResultMeta {
	watermark := snapshot.SourceVersionWatermark
	warnings := []string{}
	if snapshot.ReviewInputWarning != nil {
		kind := store.KindUnavailable
		var failure *store.Failure
		if errors.As(snapshot.ReviewInputWarning, &failure) {
			kind = failure.Kind
		}
		// Stable notice: review_input_candidate_tree:<typed failure kind>.
		// Raw paths and Git output stay on the non-JSON snapshot error only.
		warnings = append(warnings, "review_input_candidate_tree:"+string(kind))
	}
	return store.ResultMeta{QueryID: "C19.Continuity", ContractVersion: "C19/1.0", ResolvedScope: store.ResolvedScope{WorkID: snapshot.WorkID, ProductIDs: snapshot.ProductIdentity}, SourceVersionWatermark: watermark, Authority: "authoritative", Freshness: store.Freshness{ObservedAt: observedAt}, OrderingKeys: []string{"boundary_sequence"}, NextCursor: next, Omissions: []string{}, Warnings: warnings}
}

// continuityProjectionHeader is the small closed identity header every page
// of one oversized projection repeats: exact scalar work, step, instance,
// and watermark identities, never an abbreviated projection member. The
// boot projection reference renders the same header.
type continuityProjectionHeader struct {
	WorkID           string
	WorkVersion      *int64
	WorkflowInstance string
	WorkflowStep     *string
	Watermark        string
}

func continuityProjectionHeaderOf(snapshot store.ContinuitySnapshot) continuityProjectionHeader {
	header := continuityProjectionHeader{WorkID: snapshot.WorkID, WorkflowInstance: snapshot.WorkflowInstance, Watermark: snapshot.Watermark}
	if snapshot.WorkPin != nil {
		version := snapshot.WorkPin.Version
		header.WorkVersion = &version
	}
	if snapshot.WorkflowStep != "" {
		step := snapshot.WorkflowStep
		header.WorkflowStep = &step
	}
	return header
}

// continuityProjectionCursor is the versioned inner continuation cursor of
// one paged projection: it binds the work, the original boundary-window
// cursor the document was captured under, the complete document's digest,
// and the next byte offset. It travels only inside the existing signed
// envelope cursor, so there is no second signature scheme; the digest alone
// binds the snapshot, because a different session's held verify leases
// change the document even when the event watermark does not (CD-0096 D5).
// Offsets are the document's own byte positions — no unrelated boundary-row
// cap bounds them.
type continuityProjectionCursor struct {
	Version        int    `json:"v"`
	Work           string `json:"work"`
	BoundaryCursor string `json:"boundary_cursor"`
	Digest         string `json:"digest"`
	NextByteOffset int64  `json:"next_byte_offset"`
}

// decodeContinuityProjectionCursor decodes the versioned inner cursor at the
// agent boundary. Only a complete, well-shaped version-2 cursor decodes;
// anything else — including the version-1 boundary cursor, which is base64
// and never begins with JSON's opening brace — falls through to the existing
// boundary handling and its own refusal family.
func decodeContinuityProjectionCursor(inner string) (continuityProjectionCursor, bool) {
	var cursor continuityProjectionCursor
	if json.Unmarshal([]byte(inner), &cursor) != nil {
		return continuityProjectionCursor{}, false
	}
	if cursor.Version != 2 || cursor.Work == "" || !agentcontract.ValidSHA256Proof(cursor.Digest) || cursor.NextByteOffset < 1 {
		return continuityProjectionCursor{}, false
	}
	return cursor, true
}

// continuityProjectionResult builds one page's closed result object: the
// identity header, the exact byte page of the canonical document, and the
// continuation kind. The fragment is not independently parseable JSON and is
// not a summary; reconstruction precedes use.
func (r runtime) continuityProjectionResult(header continuityProjectionHeader, digest string, totalBytes, offset int64, fragment string, documentComplete bool, nextKind any) map[string]any {
	result := map[string]any{
		"work_id":           header.WorkID,
		"workflow_instance": header.WorkflowInstance,
		"workflow_step":     header.WorkflowStep,
		"watermark":         header.Watermark,
		"projection_page": map[string]any{
			"encoding":          "json-utf8",
			"digest":            digest,
			"total_bytes":       totalBytes,
			"offset":            offset,
			"data":              fragment,
			"document_complete": documentComplete,
		},
		"next_kind": nextKind,
	}
	if header.WorkVersion != nil {
		result["work_version"] = *header.WorkVersion
	}
	return result
}

// continuityProjectionEnvelope renders one projection page's result envelope.
// The explicit omission notice states the transport's remaining bytes; it
// means incomplete transport, never missing durable data or weakened
// authority.
func (r runtime) continuityProjectionEnvelope(base agentcontract.Envelope, meta store.ResultMeta, header continuityProjectionHeader, digest string, totalBytes, offset int64, fragment string, documentComplete bool, nextKind any) (agentcontract.Envelope, error) {
	envelope, err := r.resultEnvelope(base, meta, r.scope(meta), r.continuityProjectionResult(header, digest, totalBytes, offset, fragment, documentComplete, nextKind))
	if err != nil {
		return envelope, err
	}
	if len(envelope.Omissions) >= agentcontract.MaxNotices {
		return envelope, errors.New("continuity projection page exhausted its omission budget")
	}
	envelope.Omissions = append(envelope.Omissions, agentcontract.Notice{Kind: "continuity_projection_paged", Details: map[string]any{"total_bytes": totalBytes, "offset": offset, "fragment_bytes": int64(len(fragment)), "remaining_bytes": totalBytes - offset - int64(len(fragment)), "document_complete": documentComplete}})
	return envelope, nil
}

// continuityFragmentLength returns the largest whole-code-point cut of
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
func continuityFragmentLength(doc []byte, offset int, fit func(length int) (bool, error)) (int, error) {
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

func (r runtime) continuity(base agentcontract.Envelope, snapshot store.ContinuitySnapshot) (agentcontract.Envelope, error) {
	meta := continuityResultMeta(snapshot, snapshot.NextCursor)
	return r.resultEnvelope(base, meta, r.scope(meta), ContinuityPayload(snapshot))
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
func (r runtime) q9(base agentcontract.Envelope, q store.Q9Result) (agentcontract.Envelope, error) {
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
		wire := item{ID: v.ID, Kind: kind, Locator: v.NotePath, Commit: v.CommitOID, Hash: v.ContentHash, Status: store.KnowledgeLawStatus(v.Kind, v.OutcomeTag), SuccessorID: v.SuccessorID}
		items = append(items, wire)
	}
	publications := q.Publications
	if publications == nil {
		publications = []store.KnowledgePublication{}
	}
	payload := map[string]any{"items": items, "watermark": q.IndexWatermark, "publications": publications}
	response, err := r.resultEnvelope(base, q.ResultMeta, r.scope(q.ResultMeta), payload)
	if err == nil {
		response.SourceVersionWatermark = []agentcontract.Watermark{{SourceKind: "git_knowledge", SourceID: q.IndexWatermark, Version: q.IndexWatermark}}
	}
	return response, err
}
func (r runtime) q10(base agentcontract.Envelope, q store.Q10Result) (agentcontract.Envelope, error) {
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
	if q.Note != nil {
		for key, value := range map[string]string{"commit_oid": q.Note.CommitOID, "content_hash": q.Note.ContentHash, "home_project_id": q.Note.HomeProjectID, "home_locator_id": q.Note.HomeLocatorID} {
			if value != "" {
				payload[key] = value
			}
		}
	}
	if q.Publication != nil {
		payload["publication"] = q.Publication
	}
	// Law status or a work-note successor never replaces the PM1 Q10 locator
	// state: canonical/not_compacted/missing/ambiguous.
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
