// Package agent contains the core-owned, generated-contract-backed agent runtime
// primitives. It deliberately does not dispatch domain operations.
package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

const MaxEnvelopeBytes = 65536

type Outcome string

const (
	OutcomeOK      Outcome = "ok"
	OutcomePending Outcome = "pending"
	OutcomePartial Outcome = "partial"
	OutcomeError   Outcome = "error"
)

type Origin string

const (
	OriginCore    Origin = "core"
	OriginAdapter Origin = "adapter"
)

type Authority string

const (
	AuthorityAuthoritative Authority = "authoritative"
	AuthorityDegraded      Authority = "degraded"
	AuthorityUnreachable   Authority = "unreachable"
)

type EffectState string

const (
	EffectNone     EffectState = "none"
	EffectPossible EffectState = "possible"
	EffectPartial  EffectState = "partial"
)

type OperationState string

const (
	OperationPending   OperationState = "pending"
	OperationPartial   OperationState = "partial"
	OperationFailed    OperationState = "failed"
	OperationCompleted OperationState = "completed"
)

type Scope struct {
	ProductID    string   `json:"product_id,omitempty"`
	ProductIDs   []string `json:"product_ids,omitempty"`
	ProjectIDs   []string `json:"project_ids,omitempty"`
	WorkIDs      []string `json:"work_ids,omitempty"`
	ScopeVersion string   `json:"scope_version,omitempty"`
}
type Freshness struct {
	ObservedAt time.Time `json:"observed_at"`
	Age        int64     `json:"age"`
	Stale      bool      `json:"stale"`
}
type Watermark struct {
	SourceKind string `json:"source_kind"`
	SourceID   string `json:"source_id"`
	Version    string `json:"version"`
}
type Notice struct {
	Kind     string         `json:"kind"`
	SourceID string         `json:"source_id,omitempty"`
	Count    int64          `json:"count,omitempty"`
	Details  map[string]any `json:"details,omitempty"`
}
type EvidenceRef struct {
	Kind        string `json:"kind"`
	Authority   string `json:"authority"`
	LocatorKind string `json:"locator_kind"`
	Locator     string `json:"locator"`
	Version     string `json:"version,omitempty"`
	Digest      string `json:"digest,omitempty"`
}
type ChangedRef struct {
	EntityKind string `json:"entity_kind"`
	ID         string `json:"id"`
	Version    string `json:"version"`
}
type NextIntent struct {
	Tool           string   `json:"tool"`
	Operation      string   `json:"operation"`
	QueryID        string   `json:"query_id,omitempty"`
	ReasonCode     string   `json:"reason_code"`
	RequiredFields []string `json:"required_fields,omitempty"`
}
type RecoveryAction struct {
	Kind         string   `json:"kind"`
	RequiredRefs []string `json:"required_refs,omitempty"`
}

type StaleLawRevision struct {
	OldLawID                     string   `json:"old_law_id"`
	OldContentHash               string   `json:"old_content_hash"`
	AcceptedSuccessorLawID       string   `json:"accepted_successor_law_id"`
	AcceptedSuccessorContentHash string   `json:"accepted_successor_content_hash"`
	RecoveryActions              []string `json:"recovery_actions"`
}

type DomainOverlapRelationTuple struct {
	SourceDomainID string `json:"source_domain_id"`
	Kind           string `json:"kind"`
	TargetDomainID string `json:"target_domain_id"`
}

type DomainOverlapDetail struct {
	ProductID                     string                       `json:"product_id"`
	FromWorkID                    string                       `json:"from_work_id"`
	ToWorkID                      string                       `json:"to_work_id"`
	FromContractVersion           int64                        `json:"from_contract_version"`
	ToContractVersion             int64                        `json:"to_contract_version"`
	SharedAffectedDomainIDs       []string                     `json:"shared_affected_domain_ids"`
	SharedLawIDs                  []string                     `json:"shared_law_ids"`
	SharedDomainModifications     []string                     `json:"shared_domain_modifications"`
	SharedRelationTuples          []DomainOverlapRelationTuple `json:"shared_relation_tuples"`
	OverlapClasses                []string                     `json:"overlap_classes"`
	ResolutionState               string                       `json:"resolution_state"`
	ResolutionKind                string                       `json:"resolution_kind,omitempty"`
	RecoveryActions               []string                     `json:"recovery_actions"`
	SharedAffectedDomainCount     int                          `json:"shared_affected_domain_count"`
	SharedLawCount                int                          `json:"shared_law_count"`
	SharedDomainModificationCount int                          `json:"shared_domain_modification_count"`
	SharedRelationTupleCount      int                          `json:"shared_relation_tuple_count"`
	DetailTruncated               bool                         `json:"detail_truncated"`
}

type DomainOverlap struct {
	Overlaps         []DomainOverlapDetail `json:"overlaps"`
	TotalOverlaps    int                   `json:"total_overlaps"`
	ReturnedOverlaps int                   `json:"returned_overlaps"`
	Truncated        bool                  `json:"truncated"`
}

// MaxNotices bounds each notice collection on an envelope. Producers that merge
// notices from more than one stage must respect it before validation runs.
const MaxNotices = 16

type TypedError struct {
	Kind             string            `json:"kind"`
	RetrySafe        bool              `json:"retry_safe"`
	RecoveryAction   RecoveryAction    `json:"recovery_action"`
	EffectState      EffectState       `json:"effect_state"`
	AdapterReason    string            `json:"adapter_reason,omitempty"`
	Message          string            `json:"message,omitempty"`
	CurrentVersions  []ChangedRef      `json:"current_versions,omitempty"`
	Candidates       []string          `json:"candidates,omitempty"`
	Violations       []string          `json:"violations,omitempty"`
	Options          []string          `json:"options,omitempty"`
	StaleLawRevision *StaleLawRevision `json:"stale_law_revision,omitempty"`
	DomainOverlap    *DomainOverlap    `json:"domain_overlap,omitempty"`
	// ConsequenceSummary is the CD-0037 typed approval prompt. It is derived
	// at challenge mint from the exact facts the challenge binds, so nothing
	// it describes can change without invalidating the challenge itself. It
	// rides only refusals that minted a challenge; a refusal that minted
	// nothing is instructions, not a consent prompt, and carries none.
	ConsequenceSummary *ConsequenceSummary `json:"consequence_summary,omitempty"`
	// SupportedBudgetSeconds is the CD-0038 D3 typed ceiling. It rides every
	// budget_refused error — seconds refusal, result-size overrun, and the
	// legacy millisecond bound alike — so the value a caller needs to recover
	// is a field, never a details entry an implementation may forget to mint.
	SupportedBudgetSeconds int            `json:"supported_budget_seconds,omitempty"`
	Details                map[string]any `json:"details,omitempty"`
}

// ConsequenceSummary is the closed wire form of CD-0037 D1. Scope and
// versions use the canonical sorted approval renderers, so an agent branches
// on the same strings approval consumption compares byte-for-byte.
type ConsequenceSummary struct {
	Tool            string   `json:"tool"`
	Operation       string   `json:"operation"`
	Consequence     string   `json:"consequence"`
	OperationDigest string   `json:"operation_digest"`
	Scope           []string `json:"scope"`
	Versions        []string `json:"versions"`
	ExpiresAt       string   `json:"expires_at"`
}

// GoverningConflictOptions is the closed operator-choice vocabulary of CD-0035
// D1/D5. It is the wire form of the three resolutions specs-as-laws.md section 4
// and CD-0012 D6 name in prose.
var GoverningConflictOptions = []string{"clarify", "amend_contract", "accept_scope_cut"}

func validateOptions(err TypedError) error {
	if len(err.Options) == 0 {
		return nil
	}
	allowed := map[string]bool{"clarify": true, "amend_contract": true, "accept_scope_cut": true}
	if len(err.Options) > len(allowed) || !unique(err.Options) {
		return errors.New("invalid operator options")
	}
	for _, option := range err.Options {
		if !allowed[option] {
			return fmt.Errorf("unknown operator option %q", option)
		}
	}
	// CD-0035 D1: options are permitted only on a governing-law conflict, and
	// only alongside the recovery action that returns the choice to the operator.
	if err.Kind != "invariant_violation" || err.RecoveryAction.Kind != "contact_operator" {
		return errors.New("operator options coupling violated")
	}
	return nil
}

type OperationRef struct {
	ID          string         `json:"id"`
	Kind        string         `json:"kind"`
	Version     string         `json:"version"`
	State       OperationState `json:"state"`
	CurrentStep string         `json:"current_step"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// Envelope is the closed TS7 wire envelope. Domain payloads remain raw JSON so
// their owning TS3/TS4 contract can evolve independently; envelope structure is
// validated here and by the canonical JSON schema.
type Envelope struct {
	SchemaVersion          string            `json:"schema_version"`
	ManifestDigest         string            `json:"manifest_digest"`
	RequestID              string            `json:"request_id"`
	Origin                 Origin            `json:"origin"`
	Tool                   string            `json:"tool"`
	Operation              string            `json:"operation"`
	QueryID                string            `json:"query_id,omitempty"`
	Outcome                Outcome           `json:"outcome"`
	ResolvedScope          *Scope            `json:"resolved_scope"`
	Authority              Authority         `json:"authority"`
	Freshness              *Freshness        `json:"freshness"`
	SourceVersionWatermark []Watermark       `json:"source_version_watermark"`
	OrderingKeys           []string          `json:"ordering_keys"`
	NextCursor             *string           `json:"next_cursor"`
	Omissions              []Notice          `json:"omissions"`
	Warnings               []Notice          `json:"warnings"`
	EvidenceRefs           []EvidenceRef     `json:"evidence_refs"`
	Replayed               bool              `json:"replayed"`
	Items                  []json.RawMessage `json:"items,omitempty"`
	Result                 json.RawMessage   `json:"result,omitempty"`
	ChangedRefs            []ChangedRef      `json:"changed_refs,omitempty"`
	NextValidIntents       []NextIntent      `json:"next_valid_intents,omitempty"`
	OperationRef           *OperationRef     `json:"operation_ref,omitempty"`
	NextAction             *RecoveryAction   `json:"next_action,omitempty"`
	CompletedSteps         []string          `json:"completed_steps,omitempty"`
	FailedStep             string            `json:"failed_step,omitempty"`
	Error                  *TypedError       `json:"error,omitempty"`
}

func NewBase(requestID, tool, operation string) Envelope {
	e := Envelope{SchemaVersion: "1.0", ManifestDigest: ManifestDigest, RequestID: requestID, Origin: OriginCore, Tool: tool, Operation: operation, ResolvedScope: nil, Authority: AuthorityAuthoritative, Freshness: nil, SourceVersionWatermark: []Watermark{}, OrderingKeys: []string{}, NextCursor: nil, Omissions: []Notice{}, Warnings: []Notice{}, EvidenceRefs: []EvidenceRef{}, Replayed: false}
	for _, op := range ContractOperations {
		if op.Tool == tool && op.Operation == operation {
			e.QueryID = op.QueryID
			break
		}
	}
	return e
}
func NewOKMutation(base Envelope, payload json.RawMessage, changed []ChangedRef, intents []NextIntent) Envelope {
	base.Outcome = OutcomeOK
	base.Result = payload
	base.ChangedRefs = changed
	base.NextValidIntents = intents
	if base.ChangedRefs == nil {
		base.ChangedRefs = []ChangedRef{}
	}
	if base.NextValidIntents == nil {
		base.NextValidIntents = []NextIntent{}
	}
	return base
}
func NewPending(base Envelope, ref OperationRef, next RecoveryAction) Envelope {
	base.Outcome = OutcomePending
	base.OperationRef = &ref
	base.NextAction = &next
	return base
}
func NewPartial(base Envelope, ref OperationRef, steps []string, err TypedError) Envelope {
	base.Outcome = OutcomePartial
	base.OperationRef = &ref
	base.CompletedSteps = steps
	base.Error = &err
	return base
}
func NewCoreError(base Envelope, err TypedError) Envelope {
	base.Outcome = OutcomeError
	base.Error = &err
	return base
}
func NewAdapterError(requestID, tool, operation, reason string, kind string) Envelope {
	e := Envelope{SchemaVersion: "1.0", ManifestDigest: ManifestDigest, RequestID: requestID, Origin: OriginAdapter, Tool: tool, Operation: operation, ResolvedScope: nil, Authority: AuthorityUnreachable, Freshness: nil, SourceVersionWatermark: []Watermark{}, OrderingKeys: []string{}, NextCursor: nil, Omissions: []Notice{}, Warnings: []Notice{}, EvidenceRefs: []EvidenceRef{}, Outcome: OutcomeError}
	for _, op := range ContractOperations {
		if op.Tool == tool && op.Operation == operation {
			e.QueryID = op.QueryID
			break
		}
	}
	e.Error = &TypedError{Kind: kind, RetrySafe: false, RecoveryAction: RecoveryAction{Kind: "contact_operator"}, EffectState: EffectNone, AdapterReason: reason}
	return e
}

func (e Envelope) MarshalJSON() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	type wire Envelope
	b, err := json.Marshal(wire(e))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxEnvelopeBytes {
		return nil, fmt.Errorf("agent envelope exceeds %d bytes", MaxEnvelopeBytes)
	}
	return b, nil
}
func (e *Envelope) UnmarshalJSON(data []byte) error {
	type wire Envelope
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var value wire
	if err := dec.Decode(&value); err != nil {
		return fmt.Errorf("decode agent envelope: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("agent envelope contains trailing JSON")
	}
	*e = Envelope(value)
	return e.Validate()
}
func (e Envelope) Encode() ([]byte, error) { return json.Marshal(e) }
func (e Envelope) Validate() error {
	if e.SchemaVersion != "1.0" || e.ManifestDigest != ManifestDigest || e.RequestID == "" || len(e.RequestID) > 128 || e.Tool == "" || e.Operation == "" {
		return errors.New("invalid envelope identity")
	}
	if e.Origin != OriginCore && e.Origin != OriginAdapter {
		return errors.New("unknown envelope origin")
	}
	if e.Outcome != OutcomeOK && e.Outcome != OutcomePending && e.Outcome != OutcomePartial && e.Outcome != OutcomeError {
		return errors.New("unknown envelope outcome")
	}
	if err := validateOperation(e.Tool, e.Operation, e.QueryID); err != nil {
		return err
	}
	if len(e.SourceVersionWatermark) > 32 || len(e.OrderingKeys) > 16 || len(e.Omissions) > MaxNotices || len(e.Warnings) > MaxNotices || len(e.EvidenceRefs) > 32 {
		return errors.New("envelope bound exceeded")
	}
	if err := validateEnvelopeCollections(e); err != nil {
		return err
	}
	if err := validateScope(e.ResolvedScope); err != nil {
		return err
	}
	if e.Authority != AuthorityAuthoritative && e.Authority != AuthorityDegraded && e.Authority != AuthorityUnreachable {
		return errors.New("unknown authority")
	}
	if e.Authority == AuthorityDegraded && len(e.Omissions) == 0 {
		return errors.New("degraded envelope requires omissions")
	}
	if e.Authority == AuthorityUnreachable && (e.Outcome != OutcomeError || e.Freshness != nil || len(e.SourceVersionWatermark) != 0) {
		return errors.New("unreachable envelope coupling violated")
	}
	if e.Origin == OriginCore && e.Authority == AuthorityUnreachable && e.Error != nil && e.Error.Kind != "unreachable" {
		return errors.New("unreachable authority requires unreachable error")
	}
	if e.Origin == OriginAdapter && (e.Outcome != OutcomeError || e.Authority != AuthorityUnreachable) {
		return errors.New("adapter envelope must be unreachable error")
	}
	if e.NextCursor != nil && len(*e.NextCursor) > 4096 {
		return errors.New("cursor exceeds bound")
	}
	switch e.Outcome {
	case OutcomeOK:
		return e.validateOK()
	case OutcomePending:
		return e.validatePending()
	case OutcomePartial:
		return e.validatePartial()
	case OutcomeError:
		return e.validateError()
	}
	return nil
}
func (e Envelope) validateOK() error {
	hasItems, hasResult := e.Items != nil, e.Result != nil
	if hasItems == hasResult {
		return errors.New("ok envelope requires exactly one payload")
	}
	if len(e.Items) > 100 || len(e.ChangedRefs) > 32 || len(e.NextValidIntents) > 16 {
		return errors.New("ok payload bound exceeded")
	}
	for _, intent := range e.NextValidIntents {
		if err := validateOperation(intent.Tool, intent.Operation, intent.QueryID); err != nil {
			return fmt.Errorf("invalid next intent: %w", err)
		}
		if intent.ReasonCode == "" || len(intent.ReasonCode) > 64 || len(intent.RequiredFields) > 16 || !unique(intent.RequiredFields) || !boundedStrings(intent.RequiredFields, 1, 64) {
			return errors.New("invalid next intent bounds")
		}
	}
	if hasResult {
		if err := ValidateOperationPayload(e.Tool, e.Operation, e.Result, true); err != nil {
			return fmt.Errorf("invalid result payload: %w", err)
		}
	}
	if isMutation(e.Tool) && (hasItems || e.ChangedRefs == nil || e.NextValidIntents == nil) {
		return errors.New("mutation ok envelope requires result and mutation metadata")
	}
	for _, ref := range e.ChangedRefs {
		if !bounded(ref.EntityKind, 1, 64) || !bounded(ref.ID, 1, 128) || !bounded(ref.Version, 1, 128) {
			return errors.New("invalid changed reference")
		}
	}
	if e.Error != nil || e.OperationRef != nil || e.NextAction != nil {
		return errors.New("ok envelope contains another outcome")
	}
	return nil
}
func (e Envelope) validatePending() error {
	if (e.Tool != "concord_work_compact" && e.Tool != "concord_work_transition") || e.OperationRef == nil || e.OperationRef.State != OperationPending || e.NextAction == nil || e.Error != nil || len(e.Items) > 0 || len(e.Result) > 0 {
		return errors.New("invalid pending envelope")
	}
	if err := validateOperationRef(*e.OperationRef); err != nil {
		return err
	}
	return validateRecoveryAction(*e.NextAction)
}
func (e Envelope) validatePartial() error {
	if (e.Tool != "concord_work_compact" && e.Tool != "concord_work_transition") || e.OperationRef == nil || (e.OperationRef.State != OperationPartial && e.OperationRef.State != OperationFailed) || len(e.CompletedSteps) == 0 || len(e.CompletedSteps) > 32 || !boundedStrings(e.CompletedSteps, 1, 64) || (e.FailedStep != "" && !bounded(e.FailedStep, 1, 64)) || e.Error == nil || e.Error.EffectState != EffectPartial || e.Error.AdapterReason != "" {
		return errors.New("invalid partial envelope")
	}
	if err := validateOperationRef(*e.OperationRef); err != nil {
		return err
	}
	return validateError(*e.Error)
}
func validateOperationRef(ref OperationRef) error {
	if !bounded(ref.ID, 1, 128) || !bounded(ref.Kind, 1, 64) || !bounded(ref.Version, 1, 128) || !bounded(ref.CurrentStep, 1, 64) || ref.UpdatedAt.IsZero() {
		return errors.New("invalid operation reference")
	}
	return nil
}
func (e Envelope) validateError() error {
	if e.Error == nil || e.Error.EffectState == "" || e.Error.EffectState == EffectPartial || len(e.Items) > 0 || len(e.Result) > 0 {
		return errors.New("invalid error envelope")
	}
	if err := validateError(*e.Error); err != nil {
		return err
	}
	if e.Origin == OriginAdapter && e.Error.AdapterReason == "" {
		return errors.New("adapter error reason missing")
	}
	if e.Origin == OriginAdapter {
		allowedKinds := map[string]bool{"transport_failure": true, "malformed_response": true, "timeout": true, "cancelled": true, "operation_conflict": true}
		if !allowedKinds[e.Error.Kind] {
			return errors.New("adapter error kind is not transport-safe")
		}
		if e.Error.AdapterReason == "manifest_mismatch" || e.Error.AdapterReason == "grant_bootstrap_failed" {
			if e.Error.Kind != "transport_failure" {
				return errors.New("adapter bootstrap failure must be transport_failure")
			}
		}
	}
	if e.Origin == OriginCore && e.Error.AdapterReason != "" {
		return errors.New("core error contains adapter reason")
	}
	if e.Origin == OriginAdapter && (e.Error.EffectState != EffectNone || e.Error.RecoveryAction.Kind != "contact_operator") {
		return errors.New("adapter error effect or recovery is not fail-closed")
	}
	return nil
}

func validateEnvelopeCollections(e Envelope) error {
	for _, watermark := range e.SourceVersionWatermark {
		if !oneOf(watermark.SourceKind, "product_memory", "git_knowledge", "native_authority", "adapter") || !bounded(watermark.SourceID, 1, 256) || !bounded(watermark.Version, 1, 256) {
			return errors.New("invalid source watermark")
		}
	}
	for _, key := range e.OrderingKeys {
		if !bounded(key, 1, 64) {
			return errors.New("invalid ordering key")
		}
	}
	notices := append(append([]Notice{}, e.Omissions...), e.Warnings...)
	for _, notice := range notices {
		if !bounded(notice.Kind, 1, 64) || len(notice.Details) > 16 || !scalarDetails(notice.Details) || (notice.SourceID != "" && !bounded(notice.SourceID, 1, 256)) || notice.Count < 0 {
			return errors.New("invalid notice")
		}
	}
	for _, ref := range e.EvidenceRefs {
		if !oneOf(ref.Kind, "verification", "review", "approval", "commit", "durable_note", "native_run", "artifact") || !bounded(ref.Authority, 1, 128) || !bounded(ref.LocatorKind, 1, 64) || !bounded(ref.Locator, 1, 2048) || (ref.Version != "" && !bounded(ref.Version, 1, 256)) || (ref.Digest != "" && !bounded(ref.Digest, 1, 256)) {
			return errors.New("invalid evidence reference")
		}
	}
	if e.Freshness != nil && (e.Freshness.ObservedAt.IsZero() || e.Freshness.Age < 0) {
		return errors.New("invalid freshness")
	}
	return nil
}
func bounded(value string, min, max int) bool { return len(value) >= min && len(value) <= max }
func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
func scalarDetails(values map[string]any) bool {
	for _, value := range values {
		if !scalarDetailValue(value) {
			return false
		}
	}
	return true
}

func scalarDetailValue(value any) bool {
	switch value := value.(type) {
	case nil, string, bool, float64, int, int64, json.Number:
		return true
	case []string:
		if len(value) > 20 {
			return false
		}
		return true
	case []any:
		if len(value) > 20 {
			return false
		}
		for _, child := range value {
			if !scalarDetailValue(child) || childValueIsArray(child) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func childValueIsArray(value any) bool {
	switch value.(type) {
	case []any, []string:
		return true
	default:
		return false
	}
}

func validateOperation(tool, operation, query string) error {
	for _, op := range ContractOperations {
		if op.Tool == tool && op.Operation == operation {
			if op.QueryID != query {
				return fmt.Errorf("query id %q does not match %s.%s", query, tool, operation)
			}
			return nil
		}
	}
	return fmt.Errorf("unknown tool operation %s.%s", tool, operation)
}
func isMutation(tool string) bool {
	for _, op := range ContractOperations {
		if op.Tool == tool && op.Kind == OperationMutation {
			return true
		}
	}
	return false
}
func validateScope(s *Scope) error {
	if s == nil {
		return nil
	}
	if s.ProductID != "" && !bounded(s.ProductID, 1, 128) || len(s.ProjectIDs) > 100 || len(s.WorkIDs) > 100 || (s.ScopeVersion != "" && !bounded(s.ScopeVersion, 1, 128)) {
		return errors.New("scope bound exceeded")
	}
	if !unique(s.ProjectIDs) || !unique(s.WorkIDs) {
		return errors.New("scope identifiers are not unique")
	}
	return nil
}
func unique(values []string) bool {
	seen := map[string]bool{}
	for _, value := range values {
		if value == "" || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}
func validateRecovery(kind string) error {
	allowed := map[string]bool{"none": true, "retry_same_request": true, "refresh_context": true, "reread_entities": true, "request_approval": true, "provide_evidence": true, "reduce_limit": true, "use_next_cursor": true, "restart_query": true, "adjust_budget": true, "reconcile_operation": true, "resolve_ambiguity": true, "contact_operator": true}
	if !allowed[kind] {
		return fmt.Errorf("unknown recovery action %q", kind)
	}
	return nil
}
func validateRecoveryAction(action RecoveryAction) error {
	if err := validateRecovery(action.Kind); err != nil {
		return err
	}
	if len(action.RequiredRefs) > 20 || !unique(action.RequiredRefs) {
		return errors.New("invalid recovery references")
	}
	for _, ref := range action.RequiredRefs {
		if !bounded(ref, 1, 128) {
			return errors.New("invalid recovery reference")
		}
	}
	return nil
}

var (
	toolIDRE      = regexp.MustCompile("^concord_[a-z0-9_]+$")
	operationIDRE = regexp.MustCompile("^[a-z0-9_]+$")
)

// sortedBoundedList enforces the canonical renderer's contract: sorted,
// unique, non-empty strings within the envelope bound.
func sortedBoundedList(values []string, limit int) bool {
	if len(values) == 0 || len(values) > limit {
		return false
	}
	previous := ""
	for i, value := range values {
		if !bounded(value, 3, 256) || (i > 0 && value <= previous) {
			return false
		}
		previous = value
	}
	return true
}

func validateError(err TypedError) error {
	if !store.TypedErrorKindAllowed(err.Kind) {
		return fmt.Errorf("unknown error kind %q", err.Kind)
	}
	if err.RecoveryAction.Kind == "" {
		return errors.New("error recovery action missing")
	}
	if x := validateRecoveryAction(err.RecoveryAction); x != nil {
		return x
	}
	if len(err.Message) > 1000 || len(err.Candidates) > 20 || len(err.Violations) > 20 || !boundedStrings(err.Candidates, 1, 128) || !boundedStrings(err.Violations, 1, 128) {
		return errors.New("error scalar/list bound exceeded")
	}
	if len(err.CurrentVersions) > 20 || len(err.Candidates) > 20 || len(err.Violations) > 20 {
		return errors.New("error bound exceeded")
	}
	for _, ref := range err.CurrentVersions {
		if !bounded(ref.EntityKind, 1, 64) || !bounded(ref.ID, 1, 128) || !bounded(ref.Version, 1, 128) {
			return errors.New("invalid current version reference")
		}
	}
	if err.AdapterReason != "" {
		adapterReasons := map[string]bool{"missing_binary": true, "spawn_failure": true, "io_failure": true, "malformed_core_response": true, "timeout_no_effect": true, "cancelled_no_effect": true, "manifest_mismatch": true, "grant_bootstrap_failed": true, "unknown_effect": true}
		if !adapterReasons[err.AdapterReason] {
			return fmt.Errorf("unknown adapter reason %q", err.AdapterReason)
		}
	}
	if len(err.Details) > 20 || !scalarDetails(err.Details) {
		return errors.New("invalid error details")
	}
	if err.Kind == "stale_law_revision" {
		if err.RecoveryAction.Kind != "request_approval" || err.StaleLawRevision == nil || !bounded(err.StaleLawRevision.OldLawID, 2, 256) || !validSHA256Proof(err.StaleLawRevision.OldContentHash) || !bounded(err.StaleLawRevision.AcceptedSuccessorLawID, 2, 256) || !validSHA256Proof(err.StaleLawRevision.AcceptedSuccessorContentHash) || len(err.StaleLawRevision.RecoveryActions) == 0 || len(err.StaleLawRevision.RecoveryActions) > 4 || !boundedStrings(err.StaleLawRevision.RecoveryActions, 1, 128) {
			return errors.New("stale law revision coupling violated")
		}
	}
	if err.Kind == "domain_overlap" {
		if err.RecoveryAction.Kind != "request_approval" || err.DomainOverlap == nil || len(err.DomainOverlap.Overlaps) == 0 || len(err.DomainOverlap.Overlaps) > 20 || err.DomainOverlap.TotalOverlaps < len(err.DomainOverlap.Overlaps) || err.DomainOverlap.ReturnedOverlaps != len(err.DomainOverlap.Overlaps) || err.DomainOverlap.TotalOverlaps < 1 || (!err.DomainOverlap.Truncated && err.DomainOverlap.TotalOverlaps != err.DomainOverlap.ReturnedOverlaps) {
			return errors.New("domain overlap coupling violated")
		}
		for _, overlap := range err.DomainOverlap.Overlaps {
			if !bounded(overlap.ProductID, 1, 128) || !bounded(overlap.FromWorkID, 1, 128) || !bounded(overlap.ToWorkID, 1, 128) || overlap.FromContractVersion <= 0 || overlap.ToContractVersion <= 0 || len(overlap.SharedAffectedDomainIDs) == 0 || len(overlap.SharedAffectedDomainIDs) > 20 || len(overlap.SharedLawIDs) > 20 || len(overlap.SharedDomainModifications) > 20 || len(overlap.SharedRelationTuples) > 20 || overlap.SharedAffectedDomainCount < len(overlap.SharedAffectedDomainIDs) || overlap.SharedLawCount < len(overlap.SharedLawIDs) || overlap.SharedDomainModificationCount < len(overlap.SharedDomainModifications) || overlap.SharedRelationTupleCount < len(overlap.SharedRelationTuples) || len(overlap.OverlapClasses) == 0 || len(overlap.OverlapClasses) > 4 || len(overlap.RecoveryActions) == 0 || len(overlap.RecoveryActions) > 4 || (overlap.ResolutionState != "unresolved" && overlap.ResolutionState != "stale" && overlap.ResolutionState != "sequenced") {
				return errors.New("domain overlap detail bounds violated")
			}
			if overlap.ResolutionState == "sequenced" && overlap.ResolutionKind != "depends_on" && overlap.ResolutionKind != "blocks" {
				return errors.New("sequenced overlap must preserve its directed resolution kind")
			}
			allowedRecovery := map[string]bool{"wait": true, "resolve_overlap": true, "terminal_work": true, "supersede_contract": true}
			for _, action := range overlap.RecoveryActions {
				if !allowedRecovery[action] {
					return errors.New("unknown domain overlap recovery action")
				}
			}
		}
	}
	if err.Kind == "ambiguous_scope" && (len(err.Candidates) == 0 || err.RecoveryAction.Kind != "resolve_ambiguity") {
		return errors.New("ambiguous scope recovery coupling violated")
	}
	if err.Kind == "stale_context" && err.RecoveryAction.Kind != "refresh_context" {
		return errors.New("stale context recovery coupling violated")
	}
	if err.Kind == "version_conflict" && (len(err.CurrentVersions) == 0 || err.RecoveryAction.Kind != "reread_entities") {
		return errors.New("version conflict recovery coupling violated")
	}
	if err.Kind == "missing_evidence" && err.RecoveryAction.Kind != "provide_evidence" {
		return errors.New("missing evidence recovery coupling violated")
	}
	if err.Kind == "limit_exceeded" && err.RecoveryAction.Kind != "reduce_limit" {
		return errors.New("limit recovery coupling violated")
	}
	if err.Kind == "budget_refused" && err.RecoveryAction.Kind != "adjust_budget" {
		return errors.New("budget recovery coupling violated")
	}
	// CD-0038 D3: the ceiling is a typed field, required wherever the kind
	// appears. Byte and item overruns carry it too — the coupling is on the
	// kind, not on which budget was exceeded.
	if err.Kind == "budget_refused" && err.SupportedBudgetSeconds < 1 {
		return errors.New("budget refusal must carry supported_budget_seconds")
	}
	// CD-0037 D1: the summary is a closed object of challenge-bound facts. The
	// D2 coupling — present exactly when a challenge was minted — belongs to
	// the mint sites; this validates the object's shape wherever it appears.
	if summary := err.ConsequenceSummary; summary != nil {
		if !toolIDRE.MatchString(summary.Tool) || !operationIDRE.MatchString(summary.Operation) || !bounded(summary.Consequence, 2, 64) || !validSHA256Proof(summary.OperationDigest) {
			return errors.New("consequence summary identity fields are invalid")
		}
		if summary.ExpiresAt == "" {
			return errors.New("consequence summary lacks expiry")
		}
		if _, err := time.Parse(time.RFC3339Nano, summary.ExpiresAt); err != nil {
			return errors.New("consequence summary expiry is not RFC3339")
		}
		if !sortedBoundedList(summary.Scope, 32) || !sortedBoundedList(summary.Versions, 32) {
			return errors.New("consequence summary scope or versions are not canonical sorted bindings")
		}
	}
	if err.Kind == "invalid_cursor" && err.RecoveryAction.Kind != "restart_query" {
		return errors.New("cursor recovery coupling violated")
	}
	if err.Kind == "operation_conflict" && err.RecoveryAction.Kind != "reconcile_operation" {
		return errors.New("operation recovery coupling violated")
	}
	if err.Kind == "outcome_mismatch" && err.RecoveryAction.Kind != "contact_operator" {
		return errors.New("outcome mismatch recovery coupling violated")
	}
	if (err.Kind == "cancelled" || err.Kind == "timeout") && (err.EffectState != EffectNone || err.RecoveryAction.Kind != "retry_same_request") {
		return errors.New("cancel/timeout coupling violated")
	}
	if x := validateOptions(err); x != nil {
		return x
	}
	return nil
}

func validSHA256Proof(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	for _, r := range value[len("sha256:"):] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
func boundedStrings(values []string, min, max int) bool {
	for _, value := range values {
		if !bounded(value, min, max) {
			return false
		}
	}
	return true
}
