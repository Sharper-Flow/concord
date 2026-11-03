package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

// Workflow event names are deliberately closed. Adding one is a contract change.
const (
	WorkflowDefinitionSelected       = "workflow.definition_selected"
	WorkflowContractApproved         = "workflow.contract_approved"
	WorkflowOverlapResolved          = "workflow.overlap_resolved"
	WorkflowContractSuperseded       = "workflow.contract_superseded"
	WorkflowCandidateSetRevised      = "workflow.candidate_set_revised"
	WorkflowActorRecorded            = "workflow.actor_recorded"
	WorkflowActionStarted            = "workflow.action_started"
	WorkflowActionCheckpointed       = "workflow.action_checkpointed"
	WorkflowActionCompleted          = "workflow.action_completed"
	WorkflowActionFailed             = "workflow.action_failed"
	WorkflowEvidenceBound            = "workflow.evidence_bound"
	WorkflowVerdictRecorded          = "workflow.verdict_recorded"
	WorkflowPremiseConfirmed         = "workflow.premise_confirmed"
	WorkflowSuccessorLinked          = "workflow.successor_linked"
	WorkflowContextCheckpointed      = "workflow.context_checkpointed"
	WorkflowContextBoundaryCrossed   = "workflow.context_boundary_crossed"
	WorkflowWorkContextRecorded      = "workflow.work_context_recorded"
	WorkflowProposalRecorded         = "workflow.proposal_recorded"
	WorkflowDesignRecorded           = "workflow.design_recorded"
	WorkflowBacklogAlignmentRecorded = "workflow.backlog_alignment_recorded"
	WorkflowDiagnosisRecorded        = "workflow.diagnosis_recorded"
	WorkflowDeliveryCorrected        = "workflow.delivery_corrected"
	WorkflowCompleted                = "workflow.completed"
)

type WorkflowVersionFields struct {
	WorkID           string `json:"work_id"`
	ExpectedVersion  *int64 `json:"expected_version"`
	ResultingVersion *int64 `json:"resulting_version"`
}

func (f WorkflowVersionFields) workflowVersionFields() WorkflowVersionFields { return f }

type workflowPayload interface {
	workflowVersionFields() WorkflowVersionFields
}

func validateWorkflowPayload[T workflowPayload](event Event, payload T) error {
	return workflowBase(event, payload.workflowVersionFields())
}

func workflowRegistration[T workflowPayload](currentVersion int, fold projectionMutation) EventKindRegistration {
	return registerEventKind[T](currentVersion, EventAppendAuthorityWorkflow, fold, validateWorkflowPayload[T])
}

// workflowScopedRegistration registers a workflow event kind whose fold reads
// the transaction's verified definition source from the write scope, so one
// caller-selected registry carries selection, contract, action, confirmation,
// and terminal folds.
func workflowScopedRegistration[T workflowPayload](currentVersion int, fold scopedProjectionMutation) EventKindRegistration {
	return registerScopedEventKind[T](currentVersion, EventAppendAuthorityWorkflow, fold, validateWorkflowPayload[T])
}

type workflowDefinitionSelectedPayload struct {
	WorkflowVersionFields
	Ref    string `json:"ref"`
	Digest string `json:"digest"`
	// RecordedVersion decodes the definition counter that recorded events
	// still carry; nothing reads it, and the log drop removes it with replay.
	RecordedVersion int64  `json:"version,omitempty"`
	WorkKind        string `json:"work_kind"`
}

type workflowContractApprovedPayload struct {
	WorkflowVersionFields
	ContractVersion     int64                              `json:"contract_version"`
	Premise             string                             `json:"premise"`
	OutcomePredicates   []workflowContractPredicatePayload `json:"outcome_predicates,omitempty"`
	OutcomeKind         string                             `json:"outcome_kind"`
	OutcomePayload      json.RawMessage                    `json:"outcome_payload"`
	RequiredEvidence    []string                           `json:"required_evidence"`
	RouteConventions    []string                           `json:"route_conventions"`
	SpecMandate         []string                           `json:"spec_mandate"`
	LawModifies         []string                           `json:"law_modifies"`
	ArchitectureBinding *WorkflowArchitectureBinding       `json:"architecture_binding,omitempty"`
	LawBoundaryVersion  int                                `json:"law_boundary_version,omitempty"`
	RigorClass          string                             `json:"rigor_class"`
	ConsequenceClass    string                             `json:"consequence_class,omitempty"`
	PremiseHash         string                             `json:"premise_hash,omitempty"`
	OutcomeHash         string                             `json:"outcome_hash,omitempty"`
	// DecisionDomains is the contract-owned declared classification fact: a
	// canonical sorted unique subset of workflowDecisionDomainSet, where []
	// records engineering and nil records historical absence. omitzero drops a
	// nil fact from the encoding while a non-nil empty slice stays present.
	DecisionDomains []string `json:"decision_domains,omitzero"`
}

// workflowContractPayloadVersion is the workflow.contract_approved
// current payload version.
const workflowContractPayloadVersion = 7

// workflowDecisionDomainSet is the sorted authoring set from CD-0243 D2.
var workflowDecisionDomainSet = []string{"increased_spending", "legal", "product", "production_data", "security", "ui_ux"}

// validateWorkflowAuthoredDecisionDomains excludes the retired preference class
// from new facts without changing the decoding of stored contracts.
func validateWorkflowAuthoredDecisionDomains(domains []string) error {
	if err := validateWorkflowDecisionDomains(domains); err != nil {
		return err
	}
	for _, domain := range domains {
		if domain == "preference" {
			return newFailure(KindInvalidPayload, "fold_event", "contract decision_domains names the retired preference class", false, "declare only increased_spending, legal, product, production_data, security, or ui_ux")
		}
	}
	return nil
}

// validateWorkflowDecisionDomains holds the typed fact to its canonical form:
// a sorted unique subset of the closed set with at most one entry per domain.
// Stored preference remains decodable; nil stays legal here, and the caller
// decides what absence means.
func validateWorkflowDecisionDomains(domains []string) error {
	if len(domains) > len(workflowDecisionDomainSet)+1 {
		return newFailure(KindInvalidPayload, "fold_event", "contract decision_domains exceeds the closed domain set", false, "declare at most one entry per decision domain")
	}
	previous := ""
	for _, domain := range domains {
		known := domain == "preference"
		for _, candidate := range workflowDecisionDomainSet {
			if domain == candidate {
				known = true
				break
			}
		}
		if !known {
			return newFailure(KindInvalidPayload, "fold_event", "contract decision_domains names a domain outside the closed set", false, "declare only increased_spending, legal, product, production_data, security, or ui_ux; preference is readable only on stored contracts")
		}
		if domain <= previous {
			return newFailure(KindInvalidPayload, "fold_event", "contract decision_domains is not canonical sorted unique", false, "declare the domains in canonical order without duplicates")
		}
		previous = domain
	}
	return nil
}

type workflowContractPredicatePayload struct {
	PredicateID    string          `json:"predicate_id"`
	Ordinal        int             `json:"ordinal,omitempty"`
	OutcomeKind    string          `json:"outcome_kind"`
	OutcomePayload json.RawMessage `json:"outcome_payload"`
}

type workflowOverlapResolvedPayload struct {
	WorkflowVersionFields
	ToWorkID            string `json:"to_work_id"`
	ToExpectedVersion   int64  `json:"to_expected_version"`
	ToResultingVersion  int64  `json:"to_resulting_version"`
	FromContractVersion int64  `json:"from_contract_version"`
	ToContractVersion   int64  `json:"to_contract_version"`
	ResolutionKind      string `json:"resolution_kind"`
	Reason              string `json:"reason"`
	CrossScope          bool   `json:"cross_scope"`
}

func (p workflowOverlapResolvedPayload) workflowVersionFields() WorkflowVersionFields {
	return p.WorkflowVersionFields
}

type workflowCandidateSetRevisedPayload struct {
	WorkflowVersionFields
	ContractVersion int64    `json:"contract_version"`
	CandidateKind   string   `json:"candidate_kind"`
	CandidateRef    string   `json:"candidate_ref"`
	Added           []string `json:"added"`
	Removed         []string `json:"removed"`
}

// workflowBacklogAlignmentRecordedPayload carries the CD-0156 mandatory
// backlog-alignment search result. related_ids is present only when outcome
// is related_found; an omitted optional list and a deliberate empty result
// must stay distinguishable, so none_found records no list at all.
type workflowBacklogAlignmentRecordedPayload struct {
	WorkflowVersionFields
	Searched   string   `json:"searched"`
	Outcome    string   `json:"outcome"`
	RelatedIDs []string `json:"related_ids,omitempty"`
}

// workflowDiagnosisRecordedPayload is the typed record_diagnosis event
// (CD-0198 D2): one required section per merged source action, and the
// alignment section's fields keep the shape the alignment event carried so
// the projection fold writes identical rows.
type workflowDiagnosisRecordedPayload struct {
	WorkflowVersionFields
	Reproduction string   `json:"reproduction"`
	Searched     string   `json:"searched"`
	Outcome      string   `json:"outcome"`
	RelatedIDs   []string `json:"related_ids,omitempty"`
	RootCause    string   `json:"root_cause"`
}

type workflowActorRecordedPayload struct {
	WorkflowVersionFields
	ActorRef     string `json:"actor_ref"`
	PrincipalRef string `json:"principal_ref"`
	ClientRef    string `json:"client_ref"`
	AgentRef     string `json:"agent_ref"`
	SessionRef   string `json:"session_ref"`
	ActorClass   string `json:"actor_class"`
}

type workflowActionStartedPayload struct {
	WorkflowVersionFields
	StepID               string `json:"step_id"`
	ActionID             string `json:"action_id"`
	AttemptEpoch         int64  `json:"attempt_epoch"`
	AcceptedInputsDigest string `json:"accepted_inputs_digest"`
	IdempotencyIdentity  string `json:"idempotency_identity"`
	ActorRef             string `json:"actor_ref"`
	// ExecutionModel is the readback executing-model identity for this run
	// (CD-0017 D5). Empty when the run dispatched no typed lane.
	ExecutionModel string `json:"execution_model,omitempty"`
}

type workflowActionCheckpointedPayload struct {
	WorkflowVersionFields
	StepID               string          `json:"step_id"`
	StepKind             string          `json:"step_kind"`
	AttemptEpoch         int64           `json:"attempt_epoch"`
	CheckpointPayload    json.RawMessage `json:"checkpoint_payload"`
	ResumeCursor         string          `json:"resume_cursor"`
	ActorRef             string          `json:"actor_ref"`
	RequestID            string          `json:"request_id"`
	CheckpointID         string          `json:"checkpoint_id,omitempty"`
	AcceptedInputsDigest string          `json:"accepted_inputs_digest,omitempty"`
	IdempotencyIdentity  string          `json:"idempotency_identity,omitempty"`
	ResultEvidenceRefs   []string        `json:"result_evidence_refs,omitempty"`
}

type workflowActionCompletedPayload struct {
	WorkflowVersionFields
	ActionID                 string       `json:"action_id,omitempty"`
	StepID                   string       `json:"step_id"`
	AttemptEpoch             int64        `json:"attempt_epoch"`
	Review                   *ReviewBlock `json:"review,omitempty"`
	CorrectionDiagnosis      string       `json:"correction_diagnosis,omitempty"`
	CorrectionStrategy       string       `json:"correction_strategy,omitempty"`
	CorrectionEvidenceRefs   []string     `json:"correction_evidence_refs,omitempty"`
	CorrectionPredicateIDs   []string     `json:"correction_predicate_ids,omitempty"`
	CorrectionOpenFindingIDs []string     `json:"correction_open_finding_ids,omitempty"`
	DeliveryArtifact         string       `json:"delivery_artifact,omitempty"`
	DeliveryState            string       `json:"delivery_state,omitempty"`
	// VerdictEntryCount carries the number of entries one batched
	// record_verdict operation judged, so the fold bounds the operation's
	// result evidence at the schema-bounded batch union instead of the
	// single-form per-operation bound. The single form and every other action
	// leave it absent, and payloads stored before the batched form decode
	// without it, so both keep the bound they have always carried.
	VerdictEntryCount  int      `json:"verdict_entry_count,omitempty"`
	ResultEvidenceRefs []string `json:"result_evidence_refs"`
	ChangedRefs        []string `json:"changed_refs"`
	ActorRef           string   `json:"actor_ref"`
}

type workflowActionFailedPayload struct {
	WorkflowVersionFields
	StepID       string `json:"step_id"`
	AttemptEpoch int64  `json:"attempt_epoch"`
	FailureKind  string `json:"failure_kind"`
	Recoverable  bool   `json:"recoverable"`
	ActorRef     string `json:"actor_ref"`
}

type workflowContextCheckpointedPayload struct {
	WorkflowVersionFields
	CheckpointID       string   `json:"checkpoint_id"`
	CheckpointSequence int64    `json:"checkpoint_sequence"`
	StepID             string   `json:"step_id"`
	AttemptEpoch       int64    `json:"attempt_epoch"`
	ActiveUnit         string   `json:"active_unit"`
	Hypothesis         string   `json:"hypothesis"`
	Diagnosis          string   `json:"diagnosis"`
	Strategy           string   `json:"strategy"`
	TouchedRefs        []string `json:"touched_refs"`
	EvidenceRefs       []string `json:"evidence_refs"`
	PendingQuestions   []string `json:"pending_questions"`
	PendingDecisions   []string `json:"pending_decisions"`
	WorkflowRef        string   `json:"workflow_ref"`
	ActorRef           string   `json:"actor_ref"`
	RequestID          string   `json:"request_id"`
}

type workflowContextBoundaryCrossedPayload struct {
	WorkflowVersionFields
	BoundaryID         string `json:"boundary_id"`
	BoundarySequence   int64  `json:"boundary_sequence"`
	BoundaryKind       string `json:"boundary_kind"`
	CheckpointID       string `json:"checkpoint_id"`
	CheckpointSequence int64  `json:"checkpoint_sequence"`
	Summary            string `json:"summary"`
	WorkflowRef        string `json:"workflow_ref"`
	AttemptEpoch       int64  `json:"attempt_epoch"`
	ActorRef           string `json:"actor_ref"`
	RequestID          string `json:"request_id"`
}

type workflowDesignDecisionPayload struct {
	ID        string   `json:"id"`
	Question  string   `json:"question"`
	Choice    string   `json:"choice"`
	Rationale string   `json:"rationale"`
	Rejected  []string `json:"rejected"`
}

type workflowProposalContent struct {
	Problem       string   `json:"problem"`
	Affected      []string `json:"affected"`
	Stakes        string   `json:"stakes"`
	UserOutcomes  []string `json:"user_outcomes"`
	Constraints   []string `json:"constraints,omitempty"`
	OpenQuestions []string `json:"open_questions,omitempty"`
	OutOfScope    []string `json:"out_of_scope,omitempty"`
}

type workflowProposalRecordedPayload struct {
	WorkflowVersionFields
	Problem       string   `json:"problem"`
	Affected      []string `json:"affected"`
	Stakes        string   `json:"stakes"`
	UserOutcomes  []string `json:"user_outcomes"`
	Constraints   []string `json:"constraints,omitempty"`
	OpenQuestions []string `json:"open_questions,omitempty"`
	OutOfScope    []string `json:"out_of_scope,omitempty"`
}

func (p workflowProposalRecordedPayload) content() workflowProposalContent {
	return workflowProposalContent{Problem: p.Problem, Affected: p.Affected, Stakes: p.Stakes, UserOutcomes: p.UserOutcomes, Constraints: p.Constraints, OpenQuestions: p.OpenQuestions, OutOfScope: p.OutOfScope}
}

func validateWorkflowProposalContent(content workflowProposalContent) error {
	if !validWorkflowProseItem(content.Problem, 4096) || len(content.Affected) < 1 || len(content.Affected) > 16 || !validWorkflowProseItem(content.Stakes, 2048) || len(content.UserOutcomes) < 1 || len(content.UserOutcomes) > 16 || len(content.Constraints) > 16 || len(content.OpenQuestions) > 16 || len(content.OutOfScope) > 16 {
		return newFailure(KindInvalidPayload, "workflow_proposal", "proposal document is missing required text or exceeds its bounds", false, "supply the bounded proposal document")
	}
	for _, values := range [][]string{content.Affected, content.UserOutcomes, content.Constraints, content.OpenQuestions, content.OutOfScope} {
		seen := make(map[string]struct{}, len(values))
		for _, value := range values {
			if _, exists := seen[value]; exists {
				return newFailure(KindInvalidPayload, "workflow_proposal", "proposal text entries must be unique", false, "supply unique proposal text entries")
			}
			seen[value] = struct{}{}
		}
	}
	for _, value := range content.Affected {
		if !validWorkflowProseItem(value, 256) {
			return newFailure(KindInvalidPayload, "workflow_proposal", "proposal affected entry is blank or exceeds 256 characters", false, "supply bounded affected entries")
		}
	}
	for _, values := range [][]string{content.UserOutcomes, content.Constraints, content.OpenQuestions, content.OutOfScope} {
		for _, value := range values {
			if !validWorkflowProseItem(value, 512) {
				return newFailure(KindInvalidPayload, "workflow_proposal", "proposal text entry is blank or exceeds 512 characters", false, "supply bounded proposal text")
			}
		}
	}
	return nil
}

type workflowDesignRecordedPayload struct {
	WorkflowVersionFields
	workflowDesignContent
}

type workflowEvidenceBoundPayload struct {
	WorkflowVersionFields
	EvidenceKind        string              `json:"evidence_kind"`
	ImmutableSubjectRef string              `json:"immutable_subject_ref"`
	ProducerID          string              `json:"producer_id"`
	ProducerRunRef      string              `json:"producer_run_ref"`
	ProducerWatermark   string              `json:"producer_watermark"`
	ObservedAt          string              `json:"observed_at"`
	VerificationReport  *VerificationReport `json:"verification_report,omitempty"`
}

type workflowVerdictRecordedPayload struct {
	WorkflowVersionFields
	ContractVersion          int64            `json:"contract_version"`
	PredicateID              string           `json:"predicate_id"`
	VerdictKind              string           `json:"verdict_kind"`
	VerdictActorRef          string           `json:"verdict_actor_ref"`
	EvaluationEvidence       []string         `json:"evaluation_evidence"`
	CriterionDigest          string           `json:"criterion_digest,omitempty"`
	Candidate                *ReviewCandidate `json:"candidate,omitempty"`
	IncomparableWithApproved bool             `json:"incomparable_with_approved"`
	// VerdictModel is the readback executing-model identity of the review run
	// (CD-0017 D5). Empty when the evaluator dispatched no typed lane.
	VerdictModel string `json:"verdict_model,omitempty"`
}

type workflowPremiseConfirmedPayload struct {
	WorkflowVersionFields
	ContractVersion    int64  `json:"contract_version"`
	ConfirmingActorRef string `json:"confirming_actor_ref"`
	// ConfirmationAuthority records whose authority the confirmation
	// carries: the authenticated non-worker coordinator.
	ConfirmationAuthority string `json:"confirmation_authority,omitempty"`
}

type workflowSuccessorLinkedPayload struct {
	WorkflowVersionFields
	SuccessorWorkID string `json:"successor_work_id"`
	RelationKind    string `json:"relation_kind"`
	SuccessorKind   string `json:"successor_kind"`
	DefinitionRef   string `json:"definition_ref"`
	// Resize marks the same-family link a resize records (v3); it holds
	// only beside the lineage row of that resize.
	Resize bool `json:"resize,omitempty"`
}

type workflowCompletedPayload struct {
	WorkflowVersionFields
	TerminalState     string   `json:"terminal_state"`
	FinalVerdictKind  string   `json:"final_verdict_kind,omitempty"`
	VerdictActorRef   string   `json:"verdict_actor_ref"`
	PremiseConfirmed  bool     `json:"premise_confirmed"`
	EvidenceCount     int64    `json:"evidence_count"`
	ChangedRefsDigest string   `json:"changed_refs_digest"`
	ImpactVerdict     string   `json:"impact_verdict"`
	Warnings          []string `json:"warnings,omitempty"`
}

var workflowKinds = map[string]bool{
	"implementation": true, "break_fix": true, "research": true,
	"architecture_spike": true, "ops_runbook": true, "static_analysis": true,
	"generic_one_off": true,
}

func isWorkflowAdvancementEvent(kind string) bool {
	registration, ok := registeredEventKind(kind)
	return ok && registration.Authority == EventAppendAuthorityWorkflow
}

func workflowDispatcherRequired(kind string) error {
	if kind == WorkflowCompleted {
		return workflowCompletionRequired()
	}
	return newFailure(KindInvalidOperation, "apply_operation", "workflow event "+kind+" is reserved for an authoritative workflow route", false, "dispatch the workflow action or use the workflow initialization/completion entry point")
}

// DeriveWorkflowActorRef implements the contract's byte-length-prefixed,
// NUL-separated canonical actor encoding. It is exported so callers can derive
// the identity before constructing workflow.actor_recorded.
func DeriveWorkflowActorRef(principalRef, clientRef, agentRef, sessionRef string) string {
	canonical := "actor-v1\x00" + actorField("principal_ref", principalRef) + actorField("client_ref", clientRef) + actorField("agent_ref", agentRef) + actorField("session_ref", sessionRef)
	sum := sha256.Sum256([]byte(canonical))
	return "actor:" + hex.EncodeToString(sum[:])
}

func actorField(name, value string) string {
	return fmt.Sprintf("%s=%d:%s|", name, len([]byte(value)), value)
}

func decodeWorkflowPayload(event Event, target any) error {
	if !isJSONObject(event.Payload) {
		return newFailure(KindInvalidPayload, "fold_event", "workflow payload is not a JSON object", false, "repair the workflow event payload")
	}
	decoder := json.NewDecoder(strings.NewReader(string(event.Payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		failure := wrapFailure(KindInvalidPayload, "fold_event", fmt.Sprintf("event %s payload does not match its workflow kind", event.EventID), false, "repair the workflow event payload", err)
		failure.Stage = StageDecode
		return failure
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		failure := newFailure(KindInvalidPayload, "fold_event", fmt.Sprintf("event %s payload contains trailing data", event.EventID), false, "repair the workflow event payload")
		failure.Stage = StageDecode
		return failure
	}
	return nil
}

func workflowBase(event Event, fields WorkflowVersionFields) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	if fields.WorkID != event.SubjectID || fields.WorkID == "" {
		return newFailure(KindInvalidPayload, "fold_event", "workflow payload work_id does not match its subject", false, "use the event subject work ID")
	}
	if fields.ExpectedVersion == nil || fields.ResultingVersion == nil {
		return newFailure(KindInvalidPayload, "fold_event", "workflow event must carry expected and resulting versions", false, "supply consecutive expected_version and resulting_version")
	}
	if *fields.ExpectedVersion <= 0 || *fields.ExpectedVersion >= 2147483647 || *fields.ResultingVersion != *fields.ExpectedVersion+1 {
		return newFailure(KindInvalidPayload, "fold_event", fmt.Sprintf("workflow event must carry positive expected and resulting versions (work=%q expected=%v resulting=%v)", fields.WorkID, *fields.ExpectedVersion, *fields.ResultingVersion), false, "supply consecutive expected_version and resulting_version")
	}
	return nil
}

// workflowStringMinBytes is the floor every workflowString guard holds a
// workflow string to, in UTF-8 bytes. Declared payload limits derive from
// this constant so the advertised floor is the enforced one (CON-412).
const workflowStringMinBytes = 2

func workflowString(value string, upper int) bool {
	return len(value) >= workflowStringMinBytes && len(value) <= upper
}

// workflowOperationEvidenceRefBound is the per-operation evidence bound: the
// number of distinct evidence references one workflow action operation may
// resolve. The action_completed fold enforces it on result_evidence_refs. A
// batched record_verdict operation carries its own schema-bounded union bound
// derived from its declared entry count, so the bound below stays the
// single-form and every-other-action bound the fold has always applied.
const workflowOperationEvidenceRefBound = 32

// workflowVerdictBatchMaxEntries is the declared verdicts array bound: one
// batched record_verdict call judges at most eight approved predicates.
const workflowVerdictBatchMaxEntries = 8

// workflowVerdictBatchEvidenceUnionBound is the schema-bounded evidence union
// one batched record_verdict operation may resolve: each of the declared
// entries carries at most 32 evaluation references, the call envelope adds at
// most 32, and the operation-minted reference and the required native-run
// capture add one each. The fold bounds a batched operation's
// result_evidence_refs at this union, so the durable operation names every
// reference the batch resolved without an undeclared refusal.
func workflowVerdictBatchEvidenceUnionBound(entries int) int {
	return (entries+1)*workflowOperationEvidenceRefBound + 2
}

// workflowEvidenceRef bounds one evidence reference. An entry of this kind
// carries a declared evidence locator or immutable subject ref, which the tool
// surface admits at 1 to 2048 bytes, so the 2-to-128 reference bound that
// governs work ids and touched refs does not apply to it.
func workflowEvidenceRef(value string) bool { return len(value) >= 1 && len(value) <= 2048 }

// workflowEvidenceRefsFault names the entry that fails the evidence reference
// bound, and returns the empty string when the list holds. A caller correcting
// the call learns which locator to change instead of learning only that some
// field of the payload was wrong. The list is a set: workflowActionEvidenceRefs
// normalizes the merged refs before the event is written, so a duplicate here
// is a projection fault rather than ordinary caller input.
// workflowEvidenceRefsFault names the entry that fails the evidence reference
// bound, and returns the empty string when the list holds. A caller correcting
// the call learns which locator to change instead of learning only that some
// field of the payload was wrong. The list is a set: workflowActionEvidenceRefs
// normalizes the merged refs before the event is written, so a duplicate here
// is a projection fault rather than ordinary caller input.
func workflowEvidenceRefsFault(values []string, upper int) string {
	if len(values) > upper {
		return fmt.Sprintf("the list holds %d references and the bound is 0 to %d", len(values), upper)
	}
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if !workflowEvidenceRef(value) {
			return fmt.Sprintf("entry %s is %d bytes and the declared evidence locator bound is 1 to 2048", workflowRefExcerpt(value), len(value))
		}
		if seen[value] {
			return fmt.Sprintf("entry %s is named twice", workflowRefExcerpt(value))
		}
		seen[value] = true
	}
	return ""
}

// workflowRefExcerpt renders a reference for a refusal message. A locator runs
// to 2048 bytes, and the refusal has to stay readable, so a long value is cut.
func workflowRefExcerpt(value string) string {
	const limit = 96
	if len(value) <= limit {
		return strconv.Quote(value)
	}
	return strconv.Quote(value[:limit]) + "..."
}
func workflowList(values []string, upper, lower int) bool {
	if len(values) < lower || len(values) > upper {
		return false
	}
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if !workflowString(value, 128) || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}

func advanceWorkflowVersion(ctx context.Context, tx *sql.Tx, event Event, fields WorkflowVersionFields) error {
	if err := workflowBase(event, fields); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE work_items SET version=?, updated_at=? WHERE id=? AND version=?`, *fields.ResultingVersion, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.SubjectID, *fields.ExpectedVersion)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot advance workflow subject version", true, "retry once the database is writable", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot verify workflow subject version", true, "retry once the database is readable", err)
	}
	if count != 1 {
		// The update matched nothing, which is either a moved version or an
		// absent subject. Read which, so the refusal carries the live version
		// when one exists rather than a placeholder zero.
		current, exists, versionErr := projectionVersion(ctx, tx, SubjectWorkItem, event.SubjectID)
		if versionErr != nil {
			return versionErr
		}
		if !exists {
			return absentSubject(SubjectWorkItem, event.SubjectID)
		}
		conflict, conflictErr := versionConflictForQuery(ctx, tx, event.SubjectID, *fields.ExpectedVersion, current)
		if conflictErr != nil {
			return conflictErr
		}
		return conflict
	}
	return nil
}

func requireActor(ctx context.Context, tx queryer, actorRef string) error {
	if actorRef == "" {
		return newFailure(KindInvalidPayload, "fold_event", "workflow actor reference is empty", false, "record the complete actor tuple first")
	}
	var class string
	if err := tx.QueryRowContext(ctx, `SELECT actor_class FROM workflow_actors WHERE actor_ref=?`, actorRef).Scan(&class); err != nil {
		return newFailure(KindProjectionNotFound, "fold_event", "workflow actor reference is not recorded", false, "append workflow.actor_recorded before using the actor")
	}
	return nil
}

func requireActorClass(ctx context.Context, tx *sql.Tx, actorRef, want string) error {
	var class string
	if err := tx.QueryRowContext(ctx, `SELECT actor_class FROM workflow_actors WHERE actor_ref=?`, actorRef).Scan(&class); err != nil {
		return newFailure(KindInvalidOperation, "fold_event", "workflow actor reference is not recorded", false, "record the complete actor tuple first")
	}
	if class != want {
		return newFailure(KindInvalidOperation, "fold_event", "workflow actor class is not authorized for this event", false, "use an actor with the required closed authority class")
	}
	return nil
}

func workflowDigest(value string) bool {
	return len(value) == 71 && strings.HasPrefix(value, "sha256:")
}

func foldWorkflowDefinitionSelected(ctx context.Context, tx *sql.Tx, event Event, _ *writeScope) error {
	var p workflowDefinitionSelectedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	return advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields)
}

func workflowProjectionError(err error, message string) error {
	if err == nil {
		return nil
	}
	if sqlitedb.IsIdentityConflict(err) {
		return newFailure(KindProjectionConflict, "fold_event", message, false, "append a new workflow version")
	}
	return wrapFailure(KindUnavailable, "fold_event", fmt.Sprintf("%s: %v", message, err), true, "retry once the database is writable", err)
}

// validateWorkflowContractApprovalLaw checks the law and architecture declarations
// against the workflow type and the composed fields in the approval payload.
func validateWorkflowContractApprovalLaw(ctx context.Context, tx *sql.Tx, event Event, planType WorkflowStepPlanType, contractFields map[string]json.RawMessage, p workflowContractApprovedPayload, changesProductTruth bool) error {
	if changesProductTruth {
		if p.LawBoundaryVersion != 1 {
			return newFailure(KindInvalidPayload, "fold_event", "Product-changing contract must carry the current law boundary", false, "supply law_boundary_version=1")
		}
		for _, field := range []string{"spec_mandate", "law_modifies", "architecture_binding"} {
			if _, present := contractFields[field]; !present {
				return newFailure(KindInvalidPayload, "fold_event", "Product-changing contract is missing composed field "+field, false, "supply every architecture-bound contract field")
			}
		}
		if p.ArchitectureBinding == nil {
			return newFailure(KindInvalidPayload, "fold_event", "Product-changing contract contains null architecture-bound fields", false, "supply the complete architecture binding")
		}
		if err := validateArchitectureBindingTx(ctx, tx, event.SubjectID, planType, p.ArchitectureBinding, p.SpecMandate, p.LawModifies); err != nil {
			return err
		}
	} else {
		if p.ArchitectureBinding != nil {
			return newFailure(KindInvalidPayload, "fold_event", "non-Product-changing workflow cannot carry architecture_binding", false, "select a registered Product-changing workflow")
		}
		if len(p.LawModifies) != 0 {
			if err := validateDerivedLawModification(ctx, tx, event.SubjectID, p.LawModifies); err != nil {
				return err
			}
		}
	}
	return nil
}

func foldWorkflowContractApproved(ctx context.Context, tx *sql.Tx, event Event, scope *writeScope) error {
	var p workflowContractApprovedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if err := normalizeWorkflowContractApproval(&p); err != nil {
		return err
	}
	return foldValidatedWorkflowContractApproval(ctx, tx, event, scope, p)
}

func normalizeWorkflowContractApproval(p *workflowContractApprovedPayload) error {
	if p.LawModifies == nil {
		p.LawModifies = []string{}
	}
	if p.SpecMandate == nil {
		p.SpecMandate = []string{}
	}
	if p.DecisionDomains != nil {
		if err := validateWorkflowDecisionDomains(p.DecisionDomains); err != nil {
			return err
		}
	}
	if p.ContractVersion <= 0 || !workflowString(p.Premise, WorkflowPremiseMaxLength) || !workflowList(p.RequiredEvidence, 7, 0) || !workflowList(p.RouteConventions, 16, 0) || !workflowList(p.SpecMandate, 32, 0) || !workflowList(p.LawModifies, 32, 0) {
		return newFailure(KindInvalidPayload, "fold_event", "contract_approved contains invalid contract fields", false, "supply a strict closed workflow outcome and bounded contract fields")
	}
	if p.OutcomePredicates == nil {
		if !validateWorkflowOutcome(p.OutcomePayload, p.OutcomeKind) {
			return newFailure(KindInvalidPayload, "fold_event", "contract_approved contains invalid contract fields", false, "supply a strict closed workflow outcome and bounded contract fields")
		}
		p.OutcomePredicates = []workflowContractPredicatePayload{{PredicateID: "predicate:primary", OutcomeKind: p.OutcomeKind, OutcomePayload: p.OutcomePayload}}
	}
	if len(p.OutcomePredicates) < 1 || len(p.OutcomePredicates) > 8 {
		return newFailure(KindInvalidPayload, "fold_event", "contract_approved requires 1-8 outcome predicates", false, "supply 1-8 uniquely named outcome predicates")
	}
	seenPredicateIDs := make(map[string]bool, len(p.OutcomePredicates))
	for ordinal, predicate := range p.OutcomePredicates {
		if err := validateWorkflowContractPredicate(ordinal, predicate, seenPredicateIDs); err != nil {
			return err
		}
		seenPredicateIDs[predicate.PredicateID] = true
		if ordinal > 7 {
			return newFailure(KindInvalidPayload, "fold_event", "contract_approved requires 1-8 outcome predicates", false, "supply 1-8 uniquely named outcome predicates")
		}
	}
	if p.LawBoundaryVersion != 0 && p.LawBoundaryVersion != 1 {
		return newFailure(KindInvalidPayload, "fold_event", "contract_approved law boundary version is invalid", false, "use the supported law boundary version")
	}
	return nil
}

func foldValidatedWorkflowContractApproval(ctx context.Context, tx *sql.Tx, event Event, scope *writeScope, p workflowContractApprovedPayload) error {
	var activeCount int64
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, event.SubjectID).Scan(&activeCount); err != nil {
		return workflowProjectionError(err, "cannot inspect active workflow contracts")
	}
	if activeCount != 0 {
		return newFailure(KindInvariantViolation, "fold_event", "contract approval requires no active workflow contract", false, "revise the existing contract with revise_intent")
	}
	if p.ContractVersion != 1 {
		return newFailure(KindInvalidPayload, "fold_event", "initial workflow contract approval must be version 1", false, "approve the initial contract as version 1")
	}
	var contractFields map[string]json.RawMessage
	if err := json.Unmarshal(event.Payload, &contractFields); err != nil {
		return newFailure(KindInvalidPayload, "fold_event", "contract_approved payload is malformed", false, "repair the workflow contract payload")
	}
	plan, err := readWorkflowPlanTx(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if plan == nil {
		return newFailure(KindInvalidOperation, "fold_event", "contract approval requires a workflow type", false, "pick a workflow type first")
	}
	if err := validateWorkflowContractFacts(ctx, tx, event, plan.Type, contractFields, p); err != nil {
		return err
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if err := requireActor(ctx, tx, event.Actor); err != nil {
		return err
	}
	if err := persistWorkflowContractApprovalTx(ctx, tx, event, plan.Type, p); err != nil {
		return err
	}
	return refreshWorkflowHealthySequence(ctx, tx, event.SubjectID, p.ContractVersion, true)
}

func validateWorkflowContractFacts(ctx context.Context, tx *sql.Tx, event Event, planType WorkflowStepPlanType, contractFields map[string]json.RawMessage, p workflowContractApprovedPayload) error {
	changesProductTruth := planType != WorkflowStepPlanResearch
	// The contract's declared decision-domain fact is the authored record of
	// its decision class, so a contract under the typed declaration must
	// carry the fact itself: nil is the legacy absence shape and [] is
	// explicit engineering.
	if p.DecisionDomains == nil {
		return newFailure(KindInvalidPayload, "fold_event", "contract approval requires the declared decision-domain fact; absence is a legacy shape", false, "declare the contract's decision domains explicitly")
	}
	if err := validateWorkflowContractApprovalLaw(ctx, tx, event, planType, contractFields, p, changesProductTruth); err != nil {
		return err
	}
	if err := validateWorkflowApprovalLawBoundaryTx(ctx, tx, event.SubjectID, p, changesProductTruth); err != nil {
		return err
	}
	if p.ConsequenceClass != "internal_sqlite" && p.ConsequenceClass != "cross_authority" && p.ConsequenceClass != "external_effect" {
		return newFailure(KindInvalidPayload, "fold_event", "contract consequence class is not closed", false, "use internal_sqlite, cross_authority, or external_effect")
	}
	// Predicate meaning is constrained by the chosen workflow type.
	allowedKinds, allowedTokens := WorkflowPlanAllowedPredicates(planType)
	for _, approved := range p.OutcomePredicates {
		predicate, decodeErr := DecodeWorkflowPredicate(approved.OutcomePayload)
		if decodeErr != nil || string(predicate.Kind) != approved.OutcomeKind {
			return newFailure(KindInvalidPayload, "fold_event", "contract outcome predicate is malformed or mismatched", false, "supply the strict outcome predicate")
		}
		if !slices.Contains(allowedKinds, predicate.Kind) {
			return definitionFailure(KindInvalidDefinition, "predicate kind not allowed by the workflow type")
		}
		if predicate.Kind == PredicateOutcome {
			for _, token := range predicate.Allowed {
				if !slices.Contains(allowedTokens, token) {
					return definitionFailure(KindInvalidDefinition, "outcome token not allowed by the workflow type")
				}
			}
			if predicate.DecisionRecord != nil && !slices.Contains(predicate.Allowed, predicate.DecisionRecord.Decision) {
				return definitionFailure(KindInvalidDefinition, "decision record token is not allowed by the workflow type")
			}
		}
	}
	return nil
}

func validateWorkflowApprovalLawBoundaryTx(ctx context.Context, tx *sql.Tx, workID string, p workflowContractApprovedPayload, changesProductTruth bool) error {
	if p.LawBoundaryVersion != 1 {
		return nil
	}
	if err := validateLawModificationSubset(p.SpecMandate, p.LawModifies); err != nil {
		return err
	}
	if !changesProductTruth {
		return checkMandatedLawsTx(ctx, tx, workID, p.SpecMandate, p.LawModifies, true)
	}
	return nil
}

func persistWorkflowContractApprovalTx(ctx context.Context, tx *sql.Tx, event Event, planType WorkflowStepPlanType, p workflowContractApprovedPayload) error {
	changesProductTruth := planType != WorkflowStepPlanResearch
	// NULL on the projection column is historical absence, never engineering:
	// a contract that declares no array stores NULL, and [] stores the explicit
	// empty array.
	var decisionDomains any
	if p.DecisionDomains != nil {
		decisionDomains = workflowJSON(p.DecisionDomains)
	}
	truthValue := 0
	if changesProductTruth {
		truthValue = 1
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class,decision_domains,changes_product_truth) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, event.SubjectID, p.ContractVersion, p.Premise, p.ConsequenceClass, workflowJSON(p.RequiredEvidence), workflowJSON(p.RouteConventions), event.OccurredAt.UTC().Format(time.RFC3339Nano), event.Actor, workflowJSON(p.SpecMandate), workflowJSON(p.LawModifies), p.LawBoundaryVersion, p.RigorClass, decisionDomains, truthValue)
	if err != nil {
		return workflowProjectionError(err, "cannot record immutable workflow contract")
	}
	for ordinal, predicate := range p.OutcomePredicates {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES(?,?,?,?,?,?)`, event.SubjectID, p.ContractVersion, predicate.PredicateID, ordinal, predicate.OutcomeKind, string(predicate.OutcomePayload)); err != nil {
			return workflowProjectionError(err, "cannot record workflow contract predicate")
		}
	}
	if changesProductTruth {
		if err := persistWorkflowArchitectureBindingTx(ctx, tx, event.SubjectID, p.ContractVersion, *p.ArchitectureBinding); err != nil {
			return err
		}
		for _, lawID := range p.LawModifies {
			if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contract_law_modifications(work_id,contract_version,law_id) VALUES(?,?,?)`, event.SubjectID, p.ContractVersion, lawID); err != nil {
				return workflowProjectionError(err, "cannot record workflow law modification")
			}
		}
	}
	return nil
}

func foldWorkflowCandidateSetRevised(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowCandidateSetRevisedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if p.ContractVersion <= 0 || !slices.Contains([]string{"work_item", "product", "project"}, p.CandidateKind) || !workflowString(p.CandidateRef, 128) || !workflowList(p.Added, 100, 0) || !workflowList(p.Removed, 100, 0) || len(p.Added) == 0 && len(p.Removed) == 0 || overlap(p.Added, p.Removed) {
		return newFailure(KindInvalidPayload, "fold_event", "candidate_set_revised has invalid candidate bounds", false, "supply disjoint bounded additions or removals")
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	role, scope := "include", "{}"
	for _, ref := range p.Added {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_candidate_sets(work_id,contract_version,candidate_kind,candidate_ref,candidate_role,candidate_scope,recorded_at,recorded_by) VALUES(?,?,?,?,?,?,?,?)`, event.SubjectID, p.ContractVersion, p.CandidateKind, ref, role, scope, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.Actor); err != nil {
			return workflowProjectionError(err, "cannot add workflow candidate")
		}
	}
	for _, ref := range p.Removed {
		if _, err := tx.ExecContext(ctx, `DELETE FROM workflow_candidate_sets WHERE work_id=? AND contract_version=? AND candidate_kind=? AND candidate_ref=?`, event.SubjectID, p.ContractVersion, p.CandidateKind, ref); err != nil {
			return workflowProjectionError(err, "cannot remove workflow candidate")
		}
	}
	return nil
}

func overlap(a, b []string) bool {
	seen := map[string]bool{}
	for _, x := range a {
		seen[x] = true
	}
	for _, x := range b {
		if seen[x] {
			return true
		}
	}
	return false
}

// foldWorkflowBacklogAlignmentRecorded writes the CD-0156 write-seam-only alignment
// projection: one declaration row and, when the search found related work,
// one row per named work id. It does not reuse workflow_candidate_sets, whose
// foreign key binds to workflow_contracts(work_id, contract_version) and
// which therefore cannot hold a row before planning approves a contract. The
// outcome/related_ids cross-field rule is the record_alignment registry
// declaration's; the fold evaluates the same declaration here.
func foldWorkflowBacklogAlignmentRecorded(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowBacklogAlignmentRecordedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if !workflowString(p.Searched, 4096) || !slices.Contains([]string{"related_found", "none_found"}, p.Outcome) {
		return newFailure(KindInvalidPayload, "fold_event", "backlog_alignment_recorded has invalid search or outcome fields", false, "supply the search scope and a closed outcome")
	}
	// Presence is a non-empty id list, the event's own recorded shape: the
	// constructor writes related_ids only when the search found work.
	alignmentFields := map[string]json.RawMessage{"outcome": []byte(`"` + p.Outcome + `"`)}
	if len(p.RelatedIDs) != 0 {
		encoded, err := json.Marshal(p.RelatedIDs)
		if err != nil {
			return workflowProjectionError(err, "cannot encode the alignment related ids")
		}
		alignmentFields["related_ids"] = encoded
	}
	if rule, _, requires, violated := workflowPayloadCombinationViolation(workflowActionCrossField("record_alignment"), alignmentFields); violated {
		if requires {
			return newFailure(KindInvalidPayload, "fold_event", "backlog_alignment_recorded outcome "+rule.Value+" requires one to sixty-four unique related ids", false, "name the related work items the search found")
		}
		return newFailure(KindInvalidPayload, "fold_event", "backlog_alignment_recorded outcome "+rule.Value+" cannot carry related_ids", false, "drop related_ids or record outcome related_found")
	}
	if p.Outcome == "related_found" && !workflowList(p.RelatedIDs, 64, 1) {
		return newFailure(KindInvalidPayload, "fold_event", "backlog_alignment_recorded outcome related_found requires one to sixty-four unique related ids", false, "name the related work items the search found")
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	return writeWorkflowBacklogAlignmentRowsTx(ctx, tx, event.SubjectID, p.Searched, p.Outcome, p.RelatedIDs, event.OccurredAt, event.Actor)
}

// writeWorkflowBacklogAlignmentRowsTx writes the CD-0156 write-seam-only alignment
// projection rows for one recorded search: the declaration row and, when the
// search found related work, one row per named work id. The CD-0198 D2
// diagnosis fold reuses it, so the merged record writes exactly the rows the
// alignment action wrote.
func writeWorkflowBacklogAlignmentRowsTx(ctx context.Context, tx *sql.Tx, workID, searched, outcome string, relatedIDs []string, at time.Time, actor string) error {
	recordedAt := at.UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `DELETE FROM workflow_backlog_alignment WHERE work_id=?`, workID); err != nil {
		return workflowProjectionError(err, "cannot replace workflow backlog alignment")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_backlog_alignment(work_id,related_work_id,searched,outcome,recorded_at,recorded_by) VALUES(?,NULL,?,?,?,?)`, workID, searched, outcome, recordedAt, actor); err != nil {
		return workflowProjectionError(err, "cannot record workflow backlog alignment")
	}
	for _, relatedID := range relatedIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_backlog_alignment(work_id,related_work_id,searched,outcome,recorded_at,recorded_by) VALUES(?,?,?,?,?,?)`, workID, relatedID, searched, outcome, recordedAt, actor); err != nil {
			return workflowProjectionError(err, "cannot record a workflow backlog alignment match")
		}
	}
	return nil
}

// foldWorkflowDiagnosisRecorded writes the CD-0198 D2 merged diagnosis
// record. The alignment section answers to the record_alignment registry
// declaration — the same cross-field rules the alignment fold evaluates —
// and the projection rows are the alignment fold's own; the reproduction and
// root-cause sections ride the event the action recorded. No relation is
// created.
func foldWorkflowDiagnosisRecorded(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowDiagnosisRecordedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if !workflowString(p.Reproduction, 4096) || !workflowString(p.RootCause, 4096) {
		return newFailure(KindInvalidPayload, "fold_event", "diagnosis_recorded has invalid reproduction or root-cause sections", false, "record every required diagnosis section")
	}
	if !workflowString(p.Searched, 4096) || !slices.Contains([]string{"related_found", "none_found"}, p.Outcome) {
		return newFailure(KindInvalidPayload, "fold_event", "diagnosis_recorded has invalid search or outcome fields", false, "supply the search scope and a closed outcome")
	}
	alignmentFields := map[string]json.RawMessage{"outcome": []byte(`"` + p.Outcome + `"`)}
	if len(p.RelatedIDs) != 0 {
		alignmentFields["related_ids"] = mustJSONRawMessageOrEmpty(p.RelatedIDs)
	}
	if rule, _, requires, violated := workflowPayloadCombinationViolation(workflowActionCrossField("record_alignment"), alignmentFields); violated {
		if requires {
			return newFailure(KindInvalidPayload, "fold_event", "diagnosis_recorded outcome "+rule.Value+" requires one to sixty-four unique related ids", false, "name the related work items the search found")
		}
		return newFailure(KindInvalidPayload, "fold_event", "diagnosis_recorded outcome "+rule.Value+" cannot carry related_ids", false, "drop related_ids or record outcome related_found")
	}
	if p.Outcome == "related_found" && !workflowList(p.RelatedIDs, 64, 1) {
		return newFailure(KindInvalidPayload, "fold_event", "diagnosis_recorded outcome related_found requires one to sixty-four unique related ids", false, "name the related work items the search found")
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	return writeWorkflowBacklogAlignmentRowsTx(ctx, tx, event.SubjectID, p.Searched, p.Outcome, p.RelatedIDs, event.OccurredAt, event.Actor)
}

// mustJSONRawMessageOrEmpty encodes the related-id list for the fold's
// cross-field evaluation: absent for an empty list, so an outcome that
// forbids the field never sees a present empty array.
func mustJSONRawMessageOrEmpty(relatedIDs []string) json.RawMessage {
	if len(relatedIDs) == 0 {
		return nil
	}
	encoded, err := json.Marshal(relatedIDs)
	if err != nil {
		return nil
	}
	return encoded
}

func foldWorkflowActorRecorded(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowActorRecordedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	return persistWorkflowActorTuple(ctx, tx, event, p)
}

func persistWorkflowActorTuple(ctx context.Context, tx *sql.Tx, event Event, p workflowActorRecordedPayload) error {
	if !workflowString(p.PrincipalRef, 128) || !workflowString(p.ClientRef, 128) || !workflowString(p.AgentRef, 128) || !workflowString(p.SessionRef, 128) || (p.ActorClass != "agent" && p.ActorClass != "operator") || p.ActorRef != DeriveWorkflowActorRef(p.PrincipalRef, p.ClientRef, p.AgentRef, p.SessionRef) {
		return newFailure(KindInvalidPayload, "fold_event", "actor tuple or actor_ref is invalid", false, "derive actor_ref from the complete canonical tuple")
	}
	var principal, client, agent, session string
	err := tx.QueryRowContext(ctx, `SELECT principal_ref,client_ref,agent_ref,session_ref FROM workflow_actors WHERE actor_ref=?`, p.ActorRef).Scan(&principal, &client, &agent, &session)
	if err == nil {
		if principal != p.PrincipalRef || client != p.ClientRef || agent != p.AgentRef || session != p.SessionRef {
			return newFailure(KindInvalidPayload, "fold_event", "actor_ref is already bound to a different tuple", false, "derive a new actor_ref")
		}
		return nil
	}
	if err != sql.ErrNoRows {
		return workflowProjectionError(err, "cannot read workflow actor")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,?,?,?,?,?,?)`, p.ActorRef, p.PrincipalRef, p.ClientRef, p.AgentRef, p.SessionRef, p.ActorClass, event.OccurredAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return workflowProjectionError(err, "cannot record workflow actor")
	}
	return nil
}

func workflowPlanCurrentStep(plan *WorkflowStepPlan) string {
	if plan == nil {
		return "intake"
	}
	frontier := WorkflowStepPlanFrontier(*plan)
	if len(frontier) == 0 {
		// A completed plan reports its terminal step; continuity's workflow
		// step is a non-empty closed value.
		if WorkflowStepPlanComplete(*plan) && len(plan.Steps) > 0 {
			return plan.Steps[len(plan.Steps)-1].ID
		}
		return ""
	}
	return frontier[0].ID
}

// workflowStepDeclaresAction reports whether the named plan step declares the
// action, independent of the frontier.
func workflowStepDeclaresAction(plan *WorkflowStepPlan, stepID, actionID string) bool {
	if plan == nil {
		return false
	}
	step, ok := stepByID(plan.Steps, stepID)
	if !ok {
		return false
	}
	if step.Required {
		return slices.Contains(WorkflowStepPlanTemplateActions(plan.Type, stepID), actionID)
	}
	return slices.Contains([]string{"complete_step", "record_review", "record_delivery", "link_successor", "bind_evidence"}, actionID)
}

func workflowPlanAllowsAction(plan *WorkflowStepPlan, stepID, actionID string) bool {
	if plan == nil {
		return false
	}
	for _, candidate := range WorkflowStepPlanFrontier(*plan) {
		if candidate.ID == stepID {
			return workflowStepDeclaresAction(plan, stepID, actionID)
		}
	}
	return false
}

// workflowPlanDeclaringStep names the plan step that declares the action, so a
// retained record's owning write keeps a real step identity after its
// producing step passes (§4.2).
func workflowPlanDeclaringStep(plan *WorkflowStepPlan, actionID string) string {
	if plan == nil {
		return ""
	}
	for _, step := range plan.Steps {
		if workflowStepDeclaresAction(plan, step.ID, actionID) {
			return step.ID
		}
	}
	return ""
}

// foldWorkflowRetainedRecordTx admits an event-level write of a record owner
// off the frontier while its current record exists (§4.2).
func foldWorkflowRetainedRecordTx(ctx context.Context, tx *sql.Tx, workID string, plan *WorkflowStepPlan, stepID, actionID string) (bool, error) {
	if !workflowRecordAction(actionID) || !workflowStepDeclaresAction(plan, stepID, actionID) {
		return false, nil
	}
	records, err := workflowCurrentRecords(ctx, tx, workID)
	if err != nil {
		return false, err
	}
	return records[actionID], nil
}

func foldWorkflowActionStarted(ctx context.Context, tx *sql.Tx, event Event, scope *writeScope) error {
	var p workflowActionStartedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if !workflowString(p.StepID, 128) || !workflowString(p.ActionID, 128) || p.AttemptEpoch <= 0 || p.AttemptEpoch > 2147483647 || p.AcceptedInputsDigest == "" || !workflowString(p.IdempotencyIdentity, 128) {
		return newFailure(KindInvalidPayload, "fold_event", "action_started has invalid step, attempt, or idempotency fields", false, "supply bounded action-start fields")
	}
	if err := ValidateWorkflowActorModel(p.ExecutionModel); err != nil {
		return newFailure(KindInvalidPayload, "fold_event", "action_started has an invalid readback model identity", false, "supply a bounded readback model identity")
	}
	plan, err := readWorkflowPlanTx(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if p.ActorRef != event.Actor {
		return newFailure(KindUnauthorized, "fold_event", "action start actor must match the authenticated event actor", false, "start the workflow action through the authenticated workflow actor")
	}
	if !workflowPlanAllowsAction(plan, p.StepID, p.ActionID) {
		return newFailure(KindIllegalLifecycleTransition, "fold_event", "action start is not declared on a frontier step", false, "start a declared action on a frontier step")
	}
	if _, err := catalogAction(p.ActionID); err != nil {
		return err
	}
	latestEpoch, found, err := latestWorkflowActionStartEpoch(ctx, tx, event.SubjectID, p.StepID, event.Seq)
	if err != nil {
		return err
	}
	wantEpoch := int64(1)
	if found {
		if latestEpoch >= 2147483647 {
			return newFailure(KindIllegalLifecycleTransition, "fold_event", "workflow action start epoch is exhausted", false, "start a successor workflow")
		}
		wantEpoch = latestEpoch + 1
	}
	if p.AttemptEpoch != wantEpoch {
		return newFailure(KindIllegalLifecycleTransition, "fold_event", fmt.Sprintf("workflow action start epoch %d is not the next epoch %d for step %q", p.AttemptEpoch, wantEpoch, p.StepID), false, "use the next per-step workflow action start epoch")
	}
	if err := requireActor(ctx, tx, p.ActorRef); err != nil {
		return err
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if err := startWorkflowInstanceStepTx(ctx, tx, event.SubjectID, p.StepID, p.ActorRef, p.ExecutionModel, event.OccurredAt); err != nil {
		return err
	}
	if err := recordWorkVersionActionTx(ctx, tx, event, p.ActionID, *p.ResultingVersion); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_step_epochs(work_id,step_id,attempt_epoch,latest_start_seq,latest_start_event_id,latest_start_payload) VALUES(?,?,?,?,?,?) ON CONFLICT(work_id,step_id) DO UPDATE SET attempt_epoch=excluded.attempt_epoch,latest_start_seq=excluded.latest_start_seq,latest_start_event_id=excluded.latest_start_event_id,latest_start_payload=excluded.latest_start_payload`, event.SubjectID, p.StepID, p.AttemptEpoch, event.Seq, event.EventID, string(event.Payload)); err != nil {
		return workflowProjectionError(err, "cannot record the current step epoch")
	}
	if err := writeWorkflowCoordinatorActionTx(ctx, tx, event); err != nil {
		return err
	}
	return beginWorkflowLifecycleTx(ctx, tx, event)
}

// beginWorkflowLifecycleTx moves a needed work item to in_progress when the
// store applies a workflow action to it (CD-0183 D1). Every action fold calls it, so the
// first action of any kind — checkpoint and dispatch included — starts the
// lifecycle and the item stops reading ready. Only a needed item moves, which
// leaves a resumed item untouched, and the move carries no version of its
// own: the action already advanced the work version.
func beginWorkflowLifecycleTx(ctx context.Context, tx *sql.Tx, event Event) error {
	now := event.OccurredAt.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE work_items SET
	transition_from=lifecycle,transition_reason=COALESCE(json_extract(?,'$.reason'),''),transition_actor=?,
	transition_evidence_refs=COALESCE(json_extract(?,'$.evidence_refs'),'[]'),transition_seq=?,
	transition_version=COALESCE(json_extract(?,'$.resulting_version'),version),
	lifecycle='in_progress',updated_at=? WHERE id=? AND lifecycle='needed'`,
		string(event.Payload), event.Actor, string(event.Payload), event.Seq, string(event.Payload), now, event.SubjectID)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot start the work item lifecycle", true,
			"retry once the database is writable", err)
	}
	if _, err := result.RowsAffected(); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot verify the work item lifecycle start", true,
			"retry once the database is readable", err)
	}
	return nil
}

func foldWorkflowActionCheckpointed(ctx context.Context, tx *sql.Tx, event Event, scope *writeScope) error {
	var p workflowActionCheckpointedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if err := beginWorkflowLifecycleTx(ctx, tx, event); err != nil {
		return err
	}
	if !workflowString(p.StepID, 128) || p.AttemptEpoch <= 0 || p.AttemptEpoch > 2147483647 || !workflowString(p.StepKind, 32) || len(p.ResumeCursor) > 2048 || len(p.CheckpointPayload) > 16*1024 || p.RequestID == "" {
		return newFailure(KindInvalidPayload, "fold_event", "action_checkpointed has invalid checkpoint fields", false, "supply a typed bounded checkpoint")
	}
	if p.StepKind != "internal_sqlite" && p.StepKind != "cross_authority" && p.StepKind != "external_effect" {
		return newFailure(KindInvalidPayload, "fold_event", "checkpoint step kind is not closed", false, "use a closed workflow step kind")
	}
	plan, err := readWorkflowPlanTx(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	var executionActor string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(execution_actor_ref,'') FROM workflow_instances WHERE work_id=?`, event.SubjectID).Scan(&executionActor); err != nil {
		return workflowProjectionError(err, "cannot read the current workflow checkpoint authority")
	}
	if p.ActorRef != event.Actor {
		return newFailure(KindUnauthorized, "fold_event", "checkpoint actor must match the authenticated event actor", false, "checkpoint through the authenticated workflow actor")
	}
	if plan == nil || !slices.ContainsFunc(WorkflowStepPlanFrontier(*plan), func(step WorkflowPlanStep) bool { return step.ID == p.StepID }) {
		return newFailure(KindIllegalLifecycleTransition, "fold_event", "checkpoint does not match a frontier step", false, "checkpoint a frontier step")
	}
	_, expectedEpoch, found, err := latestWorkflowActionStartAt(ctx, tx, event.SubjectID, p.StepID, event.Seq)
	if err != nil {
		return err
	}
	if !found {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(attempt_epoch),0)+1 FROM workflow_checkpoints WHERE work_id=? AND step_id=?`, event.SubjectID, p.StepID).Scan(&expectedEpoch); err != nil {
			return workflowProjectionError(err, "cannot inspect prior workflow checkpoint epochs")
		}
	}
	if p.AttemptEpoch != expectedEpoch {
		return newFailure(KindIllegalLifecycleTransition, "fold_event", "checkpoint does not match the latest workflow action start epoch", false, "checkpoint the current workflow action attempt")
	}
	if !workflowCheckpointWriterAdmitted(executionActor, p.ActorRef, found) {
		return newFailure(KindUnauthorized, "fold_event", "checkpoint actor does not hold the workflow writing authority", false, "checkpoint through the workflow actor that holds the writing authority")
	}
	return persistWorkflowCheckpointTx(ctx, tx, event, scope, p)
}

func persistWorkflowCheckpointTx(ctx context.Context, tx *sql.Tx, event Event, scope *writeScope, p workflowActionCheckpointedPayload) error {
	if err := requireActor(ctx, tx, p.ActorRef); err != nil {
		return err
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	checkpointID := p.CheckpointID
	if checkpointID == "" {
		checkpointID = event.EventID
	}
	digest := p.AcceptedInputsDigest
	if digest == "" {
		digest = "checkpoint-inputs"
	}
	idem := p.IdempotencyIdentity
	if idem == "" {
		idem = event.EventID
	}
	evidence := p.ResultEvidenceRefs
	if evidence == nil {
		evidence = []string{}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_checkpoints(work_id,checkpoint_id,step_id,step_kind,attempt_epoch,accepted_inputs_digest,result_evidence_refs,resume_cursor,idempotency_identity,actor_ref,request_id,recorded_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, event.SubjectID, checkpointID, p.StepID, p.StepKind, p.AttemptEpoch, digest, workflowJSON(evidence), p.ResumeCursor, idem, p.ActorRef, p.RequestID, event.OccurredAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return workflowProjectionError(err, "cannot record workflow checkpoint")
	}
	if err := foldWorkflowDecisionRecord(ctx, tx, event, p.CheckpointPayload); err != nil {
		return err
	}
	if err := writeWorkflowCoordinatorActionTx(ctx, tx, event); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE workflow_instances SET instance_state='running',last_checkpoint_at=? WHERE work_id=?`, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.SubjectID)
	return err
}

func foldWorkflowDecisionRecord(ctx context.Context, tx *sql.Tx, event Event, raw json.RawMessage) error {
	// The checkpoint envelope carries the action's declared payload under
	// "fields". A stored event from before that envelope carries the same
	// record at the top level or under "checkpoint", so all three are read and
	// the emitted shape wins.
	type decisionRecordFields struct {
		ActionID     string   `json:"action_id"`
		Question     string   `json:"question"`
		Options      []string `json:"options_considered"`
		Decision     string   `json:"decision"`
		Rationale    string   `json:"rationale"`
		Consequences []string `json:"consequences"`
		Inputs       []string `json:"inputs"`
		POCFindings  string   `json:"poc_findings"`
	}
	var checkpoint struct {
		ActionID     string                `json:"action_id"`
		Fields       *decisionRecordFields `json:"fields,omitempty"`
		Checkpoint   *decisionRecordFields `json:"checkpoint,omitempty"`
		Question     string                `json:"question"`
		Options      []string              `json:"options_considered"`
		Decision     string                `json:"decision"`
		Rationale    string                `json:"rationale"`
		Consequences []string              `json:"consequences"`
		Inputs       []string              `json:"inputs"`
		POCFindings  string                `json:"poc_findings"`
		Supersedes   *string               `json:"supersedes"`
		SupersededBy *string               `json:"superseded_by"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &checkpoint) != nil {
		return nil
	}
	for _, nested := range []*decisionRecordFields{checkpoint.Fields, checkpoint.Checkpoint} {
		if nested == nil || nested == checkpoint.Checkpoint && (checkpoint.Fields != nil || checkpoint.Question != "") {
			continue
		}
		if checkpoint.ActionID == "" {
			checkpoint.ActionID = nested.ActionID
		}
		checkpoint.Question = nested.Question
		checkpoint.Options = nested.Options
		checkpoint.Decision = nested.Decision
		checkpoint.Rationale = nested.Rationale
		checkpoint.Consequences = nested.Consequences
		checkpoint.Inputs = nested.Inputs
		checkpoint.POCFindings = nested.POCFindings
	}
	if checkpoint.ActionID != "record_decision" {
		return nil
	}
	if !workflowString(checkpoint.Question, 4096) || !workflowList(checkpoint.Options, 16, 1) || (checkpoint.Decision != "accepted_decision" && checkpoint.Decision != "insufficient_evidence") || !workflowString(checkpoint.Rationale, 4096) || !workflowList(checkpoint.Consequences, 16, 1) || !workflowList(checkpoint.Inputs, 32, 1) || !workflowString(checkpoint.POCFindings, 4096) {
		return newFailure(KindInvalidPayload, "fold_event", "record_decision checkpoint is structurally invalid", false, "supply the complete typed decision record")
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_decision_records(work_id,question,options_considered,decision,rationale,consequences,inputs,poc_findings,supersedes,superseded_by,recorded_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, event.SubjectID, checkpoint.Question, workflowJSON(checkpoint.Options), checkpoint.Decision, checkpoint.Rationale, workflowJSON(checkpoint.Consequences), workflowJSON(checkpoint.Inputs), checkpoint.POCFindings, valueOrNil(checkpoint.Supersedes), valueOrNil(checkpoint.SupersededBy), event.OccurredAt.UTC().Format(time.RFC3339Nano))
	return workflowProjectionError(err, "cannot record workflow decision")
}

func foldWorkflowContextCheckpointed(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowContextCheckpointedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if err := beginWorkflowLifecycleTx(ctx, tx, event); err != nil {
		return err
	}
	if !workflowString(p.CheckpointID, 128) || !workflowString(p.StepID, 128) || p.AttemptEpoch <= 0 ||
		!workflowString(p.ActiveUnit, 256) || !workflowString(p.Hypothesis, 4096) || !workflowString(p.Diagnosis, 4096) ||
		!workflowString(p.Strategy, 4096) || !workflowList(p.TouchedRefs, 64, 1) || !workflowList(p.EvidenceRefs, 64, 1) ||
		!workflowList(p.PendingQuestions, 16, 0) || !workflowList(p.PendingDecisions, 16, 0) || !workflowString(p.WorkflowRef, 128) ||
		!workflowString(p.ActorRef, 70) || !workflowString(p.RequestID, 128) {
		return newFailure(KindInvalidPayload, "fold_event", "context checkpoint is incomplete or outside its bounds", false, "supply all durable working-state fields within the context continuity bounds")
	}
	if err := requireActor(ctx, tx, p.ActorRef); err != nil {
		return err
	}
	plan, err := readWorkflowPlanTx(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if plan == nil || workflowPlanCurrentStep(plan) != p.StepID || string(plan.Type) != p.WorkflowRef {
		return newFailure(KindStaleAttempt, "fold_event", "context checkpoint does not bind the current workflow step and type", false, "reread the current workflow context")
	}
	var activeAttempt int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(attempt_epoch),1) FROM durable_operations WHERE work_id=?`, event.SubjectID).Scan(&activeAttempt); err != nil {
		return workflowProjectionError(err, "cannot read workflow attempt epoch")
	}
	if activeAttempt != p.AttemptEpoch {
		return newFailure(KindStaleAttempt, "fold_event", "context checkpoint does not bind the current attempt epoch", false, "reread the current workflow attempt")
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(checkpoint_sequence),0)+1 FROM workflow_context_checkpoints WHERE work_id=?`, event.SubjectID).Scan(&sequence); err != nil {
		return workflowProjectionError(err, "cannot assign context checkpoint sequence")
	}
	if p.CheckpointSequence != 0 && p.CheckpointSequence != sequence {
		return newFailure(KindInvalidPayload, "fold_event", "context checkpoint sequence is not the next monotonic sequence", false, "use the next checkpoint sequence")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workflow_context_checkpoints(work_id,work_version,checkpoint_sequence,checkpoint_id,step_id,attempt_epoch,active_unit,hypothesis,diagnosis,strategy,touched_refs,evidence_refs,pending_questions,pending_decisions,workflow_ref,actor_ref,request_id,recorded_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, event.SubjectID, *p.ResultingVersion, sequence, p.CheckpointID, p.StepID, p.AttemptEpoch, p.ActiveUnit, p.Hypothesis, p.Diagnosis, p.Strategy, workflowJSON(p.TouchedRefs), workflowJSON(p.EvidenceRefs), workflowJSON(p.PendingQuestions), workflowJSON(p.PendingDecisions), p.WorkflowRef, p.ActorRef, p.RequestID, event.OccurredAt.UTC().Format(time.RFC3339Nano))
	return workflowProjectionError(err, "cannot record context checkpoint")
}

func foldWorkflowDesignRecorded(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowDesignRecordedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if err := validateWorkflowDesignContent(p.workflowDesignContent); err != nil {
		return err
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_design_records(work_id,work_version,approach,decisions,touched_refs,recorded_at) VALUES(?,?,?,?,?,?)`, event.SubjectID, *p.ResultingVersion, p.Approach, workflowJSON(p.Decisions), workflowJSON(p.TouchedRefs), event.OccurredAt.UTC().Format(time.RFC3339Nano))
	return workflowProjectionError(err, "cannot record workflow design")
}

func foldWorkflowProposalRecorded(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowProposalRecordedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if err := validateWorkflowProposalContent(p.content()); err != nil {
		return err
	}
	p.Affected = nonNilStrings(p.Affected)
	p.UserOutcomes = nonNilStrings(p.UserOutcomes)
	p.Constraints = nonNilStrings(p.Constraints)
	p.OpenQuestions = nonNilStrings(p.OpenQuestions)
	p.OutOfScope = nonNilStrings(p.OutOfScope)
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_proposal_records(work_id,work_version,problem,affected,stakes,user_outcomes,constraints,open_questions,out_of_scope,recorded_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, event.SubjectID, *p.ResultingVersion, p.Problem, workflowJSON(p.Affected), p.Stakes, workflowJSON(p.UserOutcomes), workflowJSON(p.Constraints), workflowJSON(p.OpenQuestions), workflowJSON(p.OutOfScope), event.OccurredAt.UTC().Format(time.RFC3339Nano))
	return workflowProjectionError(err, "cannot record workflow proposal")
}

func foldWorkflowContextBoundaryCrossed(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowContextBoundaryCrossedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if err := beginWorkflowLifecycleTx(ctx, tx, event); err != nil {
		return err
	}
	if p.BoundaryKind != "summary" || !workflowString(p.BoundaryID, 128) || !workflowString(p.CheckpointID, 128) || !workflowString(p.Summary, 16*1024) || !workflowString(p.WorkflowRef, 128) || p.AttemptEpoch <= 0 || !workflowString(p.ActorRef, 70) || !workflowString(p.RequestID, 128) {
		return newFailure(KindInvalidPayload, "fold_event", "context boundary is not a bounded summary boundary", false, "cross only a summary boundary after a durable checkpoint")
	}
	if err := requireActor(ctx, tx, p.ActorRef); err != nil {
		return err
	}
	plan, err := readWorkflowPlanTx(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if plan == nil || string(plan.Type) != p.WorkflowRef {
		return newFailure(KindStaleAttempt, "fold_event", "context boundary does not bind the current workflow type", false, "reread the current workflow context")
	}
	var checkpointVersion, checkpointSequence, attemptEpoch int64
	var checkpointStep string
	var pendingDecisions string
	if err := tx.QueryRowContext(ctx, `SELECT work_version,checkpoint_sequence,step_id,attempt_epoch,pending_decisions FROM workflow_context_checkpoints WHERE work_id=? AND checkpoint_id=?`, event.SubjectID, p.CheckpointID).Scan(&checkpointVersion, &checkpointSequence, &checkpointStep, &attemptEpoch, &pendingDecisions); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindInvalidOperation, "fold_event", "context boundary requires a durable checkpoint", false, "checkpoint_context before crossing a boundary")
		}
		return workflowProjectionError(err, "cannot read context checkpoint")
	}
	var currentWorkVersion int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, event.SubjectID).Scan(&currentWorkVersion); err != nil {
		return workflowProjectionError(err, "cannot read current work version for context boundary")
	}
	currentStep := workflowPlanCurrentStep(plan)
	if checkpointVersion != currentWorkVersion || checkpointStep != currentStep || attemptEpoch != p.AttemptEpoch {
		return newFailure(KindStaleAttempt, "fold_event", "context boundary references a stale checkpoint or attempt", false, "reread the latest context checkpoint")
	}
	if p.CheckpointSequence != 0 && p.CheckpointSequence != checkpointSequence {
		return newFailure(KindStaleAttempt, "fold_event", "context boundary references a stale checkpoint sequence", false, "reread the latest context checkpoint")
	}
	if p.CheckpointSequence == 0 {
		p.CheckpointSequence = checkpointSequence
	}
	var latestCheckpointSequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(checkpoint_sequence),0) FROM workflow_context_checkpoints WHERE work_id=?`, event.SubjectID).Scan(&latestCheckpointSequence); err != nil {
		return workflowProjectionError(err, "cannot read latest context checkpoint sequence")
	}
	if checkpointSequence != latestCheckpointSequence {
		return newFailure(KindStaleAttempt, "fold_event", "context boundary must reference the latest context checkpoint", false, "reread the latest context checkpoint")
	}
	var pending []string
	if json.Unmarshal([]byte(pendingDecisions), &pending) != nil {
		return newFailure(KindInvariantViolation, "fold_event", "checkpoint pending decisions are malformed", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	if len(pending) != 0 {
		return newFailure(KindInvalidOperation, "fold_event", "summary boundary is forbidden while operator decisions remain pending", false, "resolve pending decisions before crossing a summary boundary")
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(boundary_sequence),0)+1 FROM workflow_context_boundaries WHERE work_id=?`, event.SubjectID).Scan(&sequence); err != nil {
		return workflowProjectionError(err, "cannot assign context boundary sequence")
	}
	if p.BoundarySequence != 0 && p.BoundarySequence != sequence {
		return newFailure(KindInvalidPayload, "fold_event", "context boundary sequence is not monotonic", false, "use the next boundary sequence")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workflow_context_boundaries(work_id,work_version,boundary_sequence,boundary_count,boundary_id,boundary_kind,checkpoint_id,checkpoint_sequence,attempt_epoch,summary,workflow_ref,actor_ref,request_id,recorded_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, event.SubjectID, *p.ResultingVersion, sequence, sequence, p.BoundaryID, p.BoundaryKind, p.CheckpointID, p.CheckpointSequence, p.AttemptEpoch, p.Summary, p.WorkflowRef, p.ActorRef, p.RequestID, event.OccurredAt.UTC().Format(time.RFC3339Nano))
	return workflowProjectionError(err, "cannot record context boundary")
}

// validateWorkflowActionCompletedShape bounds the payload before any read.
func validateWorkflowActionCompletedShape(p workflowActionCompletedPayload) error {
	if err := validateWorkflowCompletionResultFields(p); err != nil {
		return err
	}
	if p.Review != nil && p.ActionID != "record_review" {
		return newFailure(KindInvalidPayload, "fold_event", "review is reserved for record_review", false, "record Review through its coordinator action")
	}
	if p.ActionID == "record_review" {
		return validateReviewBlock(p.Review)
	}
	return nil
}

func validateWorkflowCompletionResultFields(p workflowActionCompletedPayload) error {
	bound := workflowOperationEvidenceRefBound
	if p.VerdictEntryCount != 0 {
		if p.ActionID != "record_verdict" || p.VerdictEntryCount < 0 || p.VerdictEntryCount > workflowVerdictBatchMaxEntries {
			return newFailure(KindInvalidPayload, "fold_event", "action_completed verdict_entry_count is reserved for a batched record_verdict operation", false, "emit verdict_entry_count only for the batched record_verdict form")
		}
		bound = workflowVerdictBatchEvidenceUnionBound(p.VerdictEntryCount)
	}
	if fault := workflowEvidenceRefsFault(p.ResultEvidenceRefs, bound); fault != "" {
		return newFailure(KindInvalidPayload, "fold_event", "action_completed result_evidence_refs is invalid: "+fault, false, "correct the named evidence locator")
	}
	if (p.ActionID != "" && !workflowString(p.ActionID, 128)) || !workflowString(p.StepID, 128) || p.AttemptEpoch <= 0 || p.AttemptEpoch > 2147483647 || !workflowList(p.ChangedRefs, 32, 0) {
		return newFailure(KindInvalidPayload, "fold_event", "action_completed has invalid result fields", false, "supply bounded action result references")
	}
	return nil
}

func foldWorkflowActionCompleted(ctx context.Context, tx *sql.Tx, event Event, scope *writeScope) error {
	var p workflowActionCompletedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if err := beginWorkflowLifecycleTx(ctx, tx, event); err != nil {
		return err
	}
	if err := validateWorkflowActionCompletedShape(p); err != nil {
		return err
	}
	if err := requireActor(ctx, tx, p.ActorRef); err != nil {
		return err
	}
	plan, err := readWorkflowPlanTx(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if p.ActorRef != event.Actor {
		return newFailure(KindUnauthorized, "fold_event", "completed action actor must match the authenticated event actor", false, "complete the workflow action through the authenticated workflow actor")
	}
	if !workflowPlanAllowsAction(plan, p.StepID, p.ActionID) {
		retained, err := foldWorkflowRetainedRecordTx(ctx, tx, event.SubjectID, plan, p.StepID, p.ActionID)
		if err != nil {
			return err
		}
		if !retained {
			return newFailure(KindIllegalLifecycleTransition, "fold_event", "completed action is not declared on a frontier step", false, "reread_entities")
		}
	}
	return persistWorkflowActionCompletionTx(ctx, tx, event, p, plan.Type)
}

func persistWorkflowActionCompletionTx(ctx context.Context, tx *sql.Tx, event Event, p workflowActionCompletedPayload, planType WorkflowStepPlanType) error {
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT instance_state FROM workflow_instances WHERE work_id=?`, event.SubjectID).Scan(&state); err != nil {
		return err
	}
	if workflowCompletedInstanceActionImmutable(state) {
		if state != "completed" || p.ActionID != "record_delivery" {
			return newFailure(KindInvalidOperation, "fold_event", "terminal workflow instance is immutable", false, "start a successor workflow")
		}
		if err := recordWorkVersionActionTx(ctx, tx, event, p.ActionID, *p.ResultingVersion); err != nil {
			return err
		}
		return writeWorkflowDeliveryRecordTx(ctx, tx, event)
	}
	if err := writeWorkflowCoordinatorActionTx(ctx, tx, event); err != nil {
		return err
	}
	if err := writeWorkflowCompletedExitTx(ctx, tx, event, p, string(planType)); err != nil {
		return err
	}
	if err := advanceWorkflowRecordedReviewTx(ctx, tx, event, p); err != nil {
		return err
	}
	return persistWorkflowCompletionExitTx(ctx, tx, event, p)
}

func writeWorkflowDeliveryRecordTx(ctx context.Context, tx *sql.Tx, event Event) error {
	asserted, err := workflowEventDeliveryAssertion(event)
	if err != nil {
		return workflowProjectionError(err, "cannot classify the current delivery assertion")
	}
	if !asserted {
		return nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workflow_delivery_state(work_id,assertion_seq,assertion_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?) ON CONFLICT(work_id) DO UPDATE SET assertion_seq=excluded.assertion_seq,assertion_id=excluded.assertion_id,actor=excluded.actor,occurred_at=excluded.occurred_at,payload_version=excluded.payload_version,payload=excluded.payload`, event.SubjectID, event.Seq, event.EventID, event.Actor, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.PayloadVersion, string(event.Payload))
	return workflowProjectionError(err, "cannot record the current delivery assertion")
}

func persistWorkflowCompletionExitTx(ctx context.Context, tx *sql.Tx, event Event, p workflowActionCompletedPayload, _ ...any) error {
	if err := writeWorkflowDeliveryRecordTx(ctx, tx, event); err != nil {
		return err
	}
	if p.ActionID != "record_review" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_phase_exits(work_id,action_id,exit_seq,actor,step_id,contract_version,record_version)
		VALUES(?,?,?,?,?,COALESCE((SELECT MAX(contract_version) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL),0),?)
		ON CONFLICT(work_id,action_id) DO UPDATE SET exit_seq=excluded.exit_seq,actor=excluded.actor,step_id=excluded.step_id,contract_version=excluded.contract_version,record_version=excluded.record_version`, event.SubjectID, p.ActionID, event.Seq, event.Actor, p.StepID, event.SubjectID, *p.ResultingVersion); err != nil {
			return workflowProjectionError(err, "cannot record the workflow phase exit")
		}
	}
	if err := recordWorkVersionActionTx(ctx, tx, event, p.ActionID, *p.ResultingVersion); err != nil {
		return err
	}
	return nil
}

func latestWorkflowActionStart(ctx context.Context, q queryer, workID, stepID string) (int64, int64, bool, error) {
	start, err := readWorkflowActionStartTx(ctx, q, workID, stepID)
	if err == sql.ErrNoRows {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, workflowProjectionError(err, "cannot inspect the latest workflow action start")
	}
	var payload workflowActionStartedPayload
	if err := json.Unmarshal(start.Payload, &payload); err != nil {
		return 0, 0, false, newFailure(KindInvariantViolation, "fold_event", "workflow action start payload is malformed", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	return start.Seq, payload.AttemptEpoch, true, nil
}

func latestWorkflowActionStartEpoch(ctx context.Context, tx *sql.Tx, workID, stepID string, beforeSeq int64) (int64, bool, error) {
	_, epoch, found, err := latestWorkflowActionStartAt(ctx, tx, workID, stepID, beforeSeq)
	return epoch, found, err
}

func workflowCheckpointWriterAdmitted(executionActor, actorRef string, fenced bool) bool {
	if executionActor == "" || actorRef == "" {
		return false
	}
	return !fenced || actorRef == executionActor
}

func workflowActionStartEpochForDispatch(ctx context.Context, tx *sql.Tx, workID, stepID string, starting bool) (int64, error) {
	latestEpoch, found, err := latestWorkflowActionStartEpoch(ctx, tx, workID, stepID, 0)
	if err != nil {
		return 0, err
	}
	if !found {
		return 1, nil
	}
	if starting {
		if latestEpoch >= 2147483647 {
			return 0, newFailure(KindIllegalLifecycleTransition, "workflow_action", "workflow action start epoch is exhausted", false, "start a successor workflow")
		}
		return latestEpoch + 1, nil
	}
	return latestEpoch, nil
}

// Admission reads the current descriptor before its own start is written.
// Authorized worker completions use their immutable authorization snapshot.
func latestWorkflowActionStartAt(ctx context.Context, q queryer, workID, stepID string, _ int64) (int64, int64, bool, error) {
	start, err := readWorkflowActionStartTx(ctx, q, workID, stepID)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, 0, false, nil
		}
		return 0, 0, false, workflowProjectionError(err, "cannot inspect the latest workflow action start")
	}
	var payload workflowActionStartedPayload
	if err := json.Unmarshal(start.Payload, &payload); err != nil {
		return 0, 0, false, newFailure(KindInvariantViolation, "fold_event", "workflow action start payload is malformed", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	return start.Seq, payload.AttemptEpoch, true, nil
}

// readWorkflowActionStartTx reads the current step's fenced start descriptor.
func readWorkflowActionStartTx(ctx context.Context, q queryer, workID, stepID string) (Event, error) {
	query := `SELECT latest_start_seq,latest_start_event_id,latest_start_payload FROM workflow_step_epochs WHERE work_id=? AND step_id=? AND latest_start_seq>0`
	start := Event{Kind: WorkflowActionStarted, SubjectType: SubjectWorkItem, SubjectID: workID, PayloadVersion: 1}
	var raw string
	if err := q.QueryRowContext(ctx, query, workID, stepID).Scan(&start.Seq, &start.EventID, &raw); err != nil {
		return Event{}, err
	}
	start.Payload = json.RawMessage(raw)
	var payload workflowActionStartedPayload
	if err := json.Unmarshal(start.Payload, &payload); err != nil {
		return Event{}, newFailure(KindInvariantViolation, "workflow_action", "workflow action start payload is malformed", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	start.Actor = payload.ActorRef
	return start, nil
}

func foldWorkflowActionFailed(ctx context.Context, tx *sql.Tx, event Event, scope *writeScope) error {
	var p workflowActionFailedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if !workflowString(p.StepID, 128) || p.AttemptEpoch <= 0 || p.FailureKind == "" {
		return newFailure(KindInvalidPayload, "fold_event", "action_failed has invalid failure fields", false, "supply step, attempt, and closed failure kind")
	}
	if !payloadschema.TypedErrorKindAllowed(p.FailureKind) {
		return newFailure(KindInvalidPayload, "fold_event", "action_failed failure kind is not closed", false, "use a TS7 typed error kind")
	}
	plan, err := readWorkflowPlanTx(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	var executionActor string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(execution_actor_ref,'') FROM workflow_instances WHERE work_id=?`, event.SubjectID).Scan(&executionActor); err != nil {
		return workflowProjectionError(err, "cannot read the current workflow failure authority")
	}
	if p.ActorRef != event.Actor {
		return newFailure(KindUnauthorized, "fold_event", "action failure actor must match the authenticated event actor", false, "fail the workflow action through the authenticated workflow actor")
	}
	if plan == nil || !slices.ContainsFunc(WorkflowStepPlanFrontier(*plan), func(step WorkflowPlanStep) bool { return step.ID == p.StepID }) {
		return newFailure(KindIllegalLifecycleTransition, "fold_event", "action failure does not match a frontier step", false, "fail a frontier step")
	}
	_, latestEpoch, found, err := latestWorkflowActionStartAt(ctx, tx, event.SubjectID, p.StepID, event.Seq)
	if err != nil {
		return err
	}
	if !found || p.AttemptEpoch != latestEpoch {
		return newFailure(KindIllegalLifecycleTransition, "fold_event", "action failure does not match the latest workflow action start epoch", false, "fail the current workflow action attempt")
	}
	if !workflowCheckpointWriterAdmitted(executionActor, p.ActorRef, true) {
		return newFailure(KindUnauthorized, "fold_event", "action failure actor does not hold the workflow writing authority", false, "fail the workflow action through the workflow actor that holds the writing authority")
	}
	if err := requireActor(ctx, tx, p.ActorRef); err != nil {
		return err
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	state := "blocked"
	if p.Recoverable {
		state = "running"
	}
	_, err = tx.ExecContext(ctx, `UPDATE workflow_instances SET instance_state=? WHERE work_id=?`, state, event.SubjectID)
	if err != nil {
		return err
	}
	if err := writeWorkflowCoordinatorActionTx(ctx, tx, event); err != nil {
		return err
	}
	return nil
}

// producerBindingMismatch explains why a named producer operation cannot back
// an evidence binding. The authority check matches four facts at once, so a
// single generic refusal left the caller unable to tell which field to repair.
// This names the first fact that fails, in the order a caller must fix them,
// and quotes the value the operation recorded.
func producerBindingMismatch(ctx context.Context, tx *sql.Tx, workID string, p workflowEvidenceBoundPayload) error {
	producer := workflowRefExcerpt(p.ProducerRunRef)
	const repair = "name the producer operation that recorded the evidence reference"
	var recordedWork, principal, requestID, result string
	err := tx.QueryRowContext(ctx, `SELECT work_id, COALESCE(principal_ref,''), COALESCE(request_id,''), COALESCE(result_kind,'') FROM durable_operations WHERE op_id=? ORDER BY (work_id=?) DESC, (result_kind='completed') DESC, attempt_epoch DESC LIMIT 1`, p.ProducerRunRef, workID).Scan(&recordedWork, &principal, &requestID, &result)
	switch {
	case err == sql.ErrNoRows:
		return newFailure(KindInvariantViolation, "fold_event", "named producer operation "+producer+" has no durable operation record", false, repair)
	case err != nil:
		return wrapFailure(KindUnavailable, "fold_event", "cannot verify evidence authority", true, "retry once the evidence authority is readable", err)
	case recordedWork != workID:
		return newFailure(KindInvariantViolation, "fold_event", "named producer operation "+producer+" belongs to another work item", false, repair)
	case result != "completed":
		return newFailure(KindInvariantViolation, "fold_event", "named producer operation "+producer+" is not completed; its result is "+workflowRefExcerpt(result), false, "bind evidence only from a completed producer operation")
	case principal != p.ProducerID:
		return newFailure(KindInvariantViolation, "fold_event", "producer_id "+workflowRefExcerpt(p.ProducerID)+" does not match producer operation "+producer+", which recorded producer_id "+workflowRefExcerpt(principal), false, "set producer_id to the value the producer operation recorded")
	case requestID != p.ProducerWatermark:
		return newFailure(KindInvariantViolation, "fold_event", "producer_watermark "+workflowRefExcerpt(p.ProducerWatermark)+" does not match producer operation "+producer+", which recorded producer_watermark "+workflowRefExcerpt(requestID), false, "set producer_watermark to the value the producer operation recorded")
	}
	return newFailure(KindInvariantViolation, "fold_event", "named producer operation "+producer+" is not a completed durable operation for this work and actor", false, repair)
}

func foldWorkflowEvidenceBound(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowEvidenceBoundPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if !slices.Contains([]string{"verification", "review", "approval", "commit", "durable_note", "native_run", "artifact"}, p.EvidenceKind) || !workflowEvidenceRef(p.ImmutableSubjectRef) || !workflowString(p.ProducerID, 128) || !workflowString(p.ProducerRunRef, 128) || !workflowString(p.ProducerWatermark, 128) || p.ObservedAt == "" {
		return newFailure(KindInvalidPayload, "fold_event", "evidence_bound has invalid evidence identity", false, "supply a complete immutable evidence binding")
	}
	if p.VerificationReport != nil {
		if p.EvidenceKind != "verification" {
			return newFailure(KindInvalidPayload, "fold_event", "verification report requires verification evidence", false, "bind the report as verification evidence")
		}
		if err := validateVerificationReport(p.ImmutableSubjectRef, p.VerificationReport); err != nil {
			return err
		}
		var relabelled bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_evidence_bindings WHERE work_id=? AND json_extract(payload,'$.evidence_kind')='verification' AND (json_extract(payload,'$.verification_report.run_ref')=? OR (json_extract(payload,'$.immutable_subject_ref')=? AND json_extract(payload,'$.verification_report.commit')<>?)))`, event.SubjectID, p.VerificationReport.RunRef, p.ImmutableSubjectRef, p.VerificationReport.Commit).Scan(&relabelled); err != nil {
			return workflowProjectionError(err, "cannot inspect verification report identity")
		}
		if relabelled {
			return newFailure(KindIllegalLifecycleTransition, "fold_event", "verification report run is already bound or immutable subject is bound to a different commit", false, "bind an independent report for its original subject")
		}
	}
	var authoritative int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM durable_operations WHERE op_id=? AND work_id=? AND principal_ref=? AND request_id=? AND result_kind='completed'`, p.ProducerRunRef, event.SubjectID, p.ProducerID, p.ProducerWatermark).Scan(&authoritative); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot verify evidence authority", true, "retry once the evidence authority is readable", err)
	}
	if authoritative != 1 {
		return producerBindingMismatch(ctx, tx, event.SubjectID, p)
	}
	var recorded int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM durable_operations WHERE op_id=? AND attempt_epoch=1 AND EXISTS (SELECT 1 FROM json_each(durable_operations.evidence_refs) WHERE value=?)`, p.ProducerRunRef, p.ImmutableSubjectRef).Scan(&recorded); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot verify evidence authority", true, "retry once the evidence authority is readable", err)
	}
	if recorded != 1 {
		return newFailure(KindInvariantViolation, "fold_event", "locator mismatch: producer operation "+workflowRefExcerpt(p.ProducerRunRef)+" completed without "+workflowRefExcerpt(p.ImmutableSubjectRef)+" among its recorded evidence", false, "bind a locator the producer operation recorded, or rerun the producer with this evidence reference")
	}
	// CD-0040 D9 via CD-0039 D4/D11: a native-run report may back completion
	// evidence only through this explicit binding, and only while its
	// attributed record is verified. The report stays readable while
	// unverified — the gate is on consumption, not existence.
	if p.EvidenceKind == "native_run" {
		var state string
		err := tx.QueryRowContext(ctx, `SELECT COALESCE(verification_state,'unverified') FROM workflow_native_runs WHERE work_id=? AND observation_id=?`, event.SubjectID, p.ImmutableSubjectRef).Scan(&state)
		if err == sql.ErrNoRows {
			err = tx.QueryRowContext(ctx, `SELECT verification_state FROM external_observations WHERE work_id=? AND observation_id=?`, event.SubjectID, p.ImmutableSubjectRef).Scan(&state)
		}
		if err == sql.ErrNoRows {
			return newFailure(KindMissingEvidence, "fold_event", "native_run evidence names no captured attributed record on this work", false, "verify the attributed record before binding it as completion evidence")
		} else if err != nil {
			return wrapFailure(KindUnavailable, "fold_event", "cannot read the attributed record's verification state", true, "retry once the projection is readable", err)
		}
		if state != string(VerificationVerified) {
			return newFailure(KindStaleRequiresReview, "fold_event", "native_run evidence is "+state+": an attributed report may be read but cannot satisfy completion while unverified", false, "append a verification of the attributed record first")
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_evidence_bindings(work_id,binding_seq,binding_id,actor,payload) VALUES(?,?,?,?,?)`, event.SubjectID, event.Seq, event.EventID, event.Actor, string(event.Payload)); err != nil {
		return workflowProjectionError(err, "cannot record workflow evidence binding")
	}
	return advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields)
}

func foldWorkflowVerdictRecorded(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowVerdictRecordedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if p.ContractVersion <= 0 || !workflowString(p.PredicateID, 128) || (p.VerdictKind != "ok" && p.VerdictKind != "outcome_mismatch" && p.VerdictKind != "insufficient_evidence") || !workflowString(p.VerdictActorRef, 70) || !workflowList(p.EvaluationEvidence, 32, 1) {
		return newFailure(KindInvalidPayload, "fold_event", "verdict_recorded has invalid verdict fields", false, "supply a closed verdict and complete evidence")
	}
	if err := ValidateWorkflowActorModel(p.VerdictModel); err != nil {
		return newFailure(KindInvalidPayload, "fold_event", "verdict_recorded has an invalid readback model identity", false, "supply a bounded readback model identity")
	}
	if err := requireActor(ctx, tx, p.VerdictActorRef); err != nil {
		return err
	}
	if p.VerdictActorRef != event.Actor {
		return newFailure(KindUnauthorized, "fold_event", "verdict actor must match the authenticated event actor", false, "record the verdict through the authenticated evaluator invocation")
	}
	if err := verifyWorkflowCriterionAssociation(ctx, tx, event.SubjectID, p); err != nil {
		return err
	}
	var approvedPredicate, contractPredicateCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_contracts WHERE work_id=?`, event.SubjectID).Scan(&contractPredicateCount); err != nil {
		return workflowProjectionError(err, "cannot inspect workflow contract")
	}
	if contractPredicateCount != 0 {
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_contract_predicates WHERE work_id=? AND contract_version=? AND predicate_id=?`, event.SubjectID, p.ContractVersion, p.PredicateID).Scan(&approvedPredicate); err != nil {
			return workflowProjectionError(err, "cannot inspect approved workflow predicate")
		}
		if approvedPredicate != 1 {
			return newFailure(KindInvalidPayload, "fold_event", "verdict predicate_id is not approved for the contract version", false, "name an approved contract predicate")
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_predicate_verdicts(work_id,contract_version,predicate_id,verdict_seq,actor,payload) VALUES(?,?,?,?,?,?) ON CONFLICT(work_id,contract_version,predicate_id) DO UPDATE SET verdict_seq=excluded.verdict_seq,actor=excluded.actor,payload=excluded.payload`, event.SubjectID, p.ContractVersion, p.PredicateID, event.Seq, event.Actor, string(event.Payload)); err != nil {
		return workflowProjectionError(err, "cannot record the current predicate verdict")
	}
	version, err := activeWorkflowContractVersion(ctx, tx, event.SubjectID, "fold_event")
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil {
		if err := refreshWorkflowHealthySequence(ctx, tx, event.SubjectID, version, false); err != nil {
			return err
		}
	}
	return advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields)
}

func foldWorkflowPremiseConfirmed(ctx context.Context, tx *sql.Tx, event Event, scope *writeScope) error {
	var p workflowPremiseConfirmedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if p.ContractVersion <= 0 {
		return newFailure(KindInvalidPayload, "fold_event", "premise confirmation has invalid contract version", false, "supply a positive contract version")
	}
	plan, err := readWorkflowPlanTx(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if plan == nil {
		return newFailure(KindInvalidOperation, "fold_event", "premise confirmation requires a workflow type", false, "pick a workflow type first")
	}
	return workflowPremiseConfirmedWriteTx(ctx, tx, event, p)
}

// workflowPremiseConfirmedWriteTx binds the authenticated coordinator and active
// contract before recording the confirmation row.
func workflowPremiseConfirmedWriteTx(ctx context.Context, tx *sql.Tx, event Event, p workflowPremiseConfirmedPayload) error {
	if err := workflowPremiseConfirmedAuthorityTx(ctx, tx, event.SubjectID, p.ContractVersion, p.ConfirmingActorRef, p.ConfirmationAuthority); err != nil {
		return err
	}
	var class string
	if p.ConfirmingActorRef != event.Actor || tx.QueryRowContext(ctx, `SELECT actor_class FROM workflow_actors WHERE actor_ref=?`, event.Actor).Scan(&class) != nil {
		return newFailure(KindInvalidOperation, "fold_event", "premise confirmation does not carry its declared authority class", false, "record the confirmation under the declared authority identity")
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_premise_confirmations(work_id,contract_version,confirmed_by,confirmed_at) VALUES(?,?,?,?) ON CONFLICT(work_id,contract_version) DO UPDATE SET confirmed_by=excluded.confirmed_by, confirmed_at=excluded.confirmed_at`, event.SubjectID, p.ContractVersion, p.ConfirmingActorRef, event.OccurredAt.UTC().Format(time.RFC3339Nano))
	return workflowProjectionError(err, "cannot record premise confirmation")
}

func foldWorkflowSuccessorLinked(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowSuccessorLinkedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if !workflowString(p.SuccessorWorkID, 128) || p.RelationKind != "forward_link" {
		return newFailure(KindInvalidPayload, "fold_event", "successor link is not a forward link", false, "use relation_kind=forward_link")
	}
	var successorKind, lifecycle string
	if err := tx.QueryRowContext(ctx, `SELECT kind,lifecycle FROM work_items WHERE id=?`, p.SuccessorWorkID).Scan(&successorKind, &lifecycle); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindInvalidRelation, "fold_event", "successor work item is not recorded", false, "create the typed successor before linking it")
		}
		return workflowProjectionError(err, "cannot read successor work item")
	}
	if p.SuccessorKind != "" && p.SuccessorKind != successorKind {
		return newFailure(KindInvalidRelation, "fold_event", "successor kind does not match the authoritative work item", false, "reread the successor work item")
	}
	if isTerminalLifecycle(lifecycle) {
		return newFailure(KindInvalidRelation, "fold_event", "successor work item is terminal", false, "link active successor work")
	}
	successor, err := readWorkflowPlanTx(ctx, tx, p.SuccessorWorkID)
	if err != nil {
		return err
	}
	if successor == nil {
		return newFailure(KindInvalidRelation, "fold_event", "successor workflow type is not selected", false, "pick a successor workflow type")
	}
	if p.DefinitionRef != "" && p.DefinitionRef != string(successor.Type) {
		return newFailure(KindInvalidRelation, "fold_event", "successor workflow type does not match its plan", false, "reread the successor workflow")
	}
	if p.Resize {
		source, err := readWorkflowPlanTx(ctx, tx, event.SubjectID)
		if err != nil {
			return err
		}
		if source == nil || source.Type != successor.Type {
			return newFailure(KindInvalidRelation, "fold_event", "a resize link requires the same workflow type", false, "link a same-type successor")
		}
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if cycle, err := workflowEdgeWouldCycle(ctx, tx, event.SubjectID, p.SuccessorWorkID, "forward_link"); err != nil {
		return err
	} else if cycle {
		return cycleFailure("fold_event", "forward link would create a cycle", "choose a non-cyclic successor", "forward_link", event.SubjectID, p.SuccessorWorkID)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workflow_impact_edges(work_id,edge_id,edge_kind,edge_class,target_work_id,target_kind,severity,recorded_at) VALUES(?,?,?,?,?,?,?,?)`, event.SubjectID, event.EventID, "forward_link", "hard", p.SuccessorWorkID, "work_item", "informational", event.OccurredAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return workflowProjectionError(err, "cannot record successor link")
	}
	return insertWorkflowForwardRelation(ctx, tx, event, p.SuccessorWorkID)
}

func workflowEdgeWouldCycle(ctx context.Context, tx *sql.Tx, source, target, kind string) (bool, error) {
	if source == target {
		return true, nil
	}
	var cycle bool
	_ = kind // the governing graph intentionally combines both hard edge kinds.
	err := tx.QueryRowContext(ctx, `WITH RECURSIVE reachable(work_id) AS (
        SELECT target_work_id FROM workflow_impact_edges
        WHERE work_id=? AND edge_kind IN ('depends_on','forward_link') AND edge_class='hard'
        UNION
        SELECT e.target_work_id FROM workflow_impact_edges e JOIN reachable r ON e.work_id=r.work_id
        WHERE e.edge_kind IN ('depends_on','forward_link') AND e.edge_class='hard'
    ) SELECT EXISTS(SELECT 1 FROM reachable WHERE work_id=?)`, target, source).Scan(&cycle)
	if err != nil {
		return false, wrapFailure(KindUnavailable, "fold_event", "cannot check workflow edge cycle", true, "retry once the database is readable", err)
	}
	return cycle, nil
}

func insertWorkflowForwardRelation(ctx context.Context, tx *sql.Tx, event Event, successor string) error {
	return insertRelation(ctx, tx, event, relationPayload{From: event.SubjectID, To: successor, Kind: "forward_link"})
}

// workflowPremiseConfirmedAuthorityTx validates a claimed confirmation
// authority against the active contract and recorded coordinator identity.
func workflowPremiseConfirmedAuthorityTx(ctx context.Context, tx *sql.Tx, workID string, contractVersion int64, confirmingActor, claimedAuthority string) error {
	if claimedAuthority != "coordinator" {
		return newFailure(KindInvalidPayload, "fold_event", "premise confirmation requires coordinator authority", false, "record the coordinator confirmation")
	}
	// A confirmation is bound to the contract version it named.
	// Supersession invalidates it: a confirmation must name the active version.
	active, activeErr := activeWorkflowContractVersion(ctx, tx, workID, "fold_event")
	if activeErr != nil {
		return activeErr
	}
	if active != contractVersion {
		return newFailure(KindInvalidPayload, "fold_event", fmt.Sprintf("premise confirmation names contract version %d, which supersession invalidated; the active version is %d", contractVersion, active), false, "confirm the active contract version")
	}
	return requireActorClass(ctx, tx, confirmingActor, "agent")
}

func valueOrNil(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
func foldWorkflowCompleted(ctx context.Context, tx *sql.Tx, event Event, scope *writeScope) error {
	var p workflowCompletedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	// evidence_count derives from the item's total workflow.evidence_bound
	// history, so any non-negative count the constructor produces is valid.
	if p.TerminalState != "completed" && p.TerminalState != "cancelled" && p.TerminalState != "superseded" || p.FinalVerdictKind != "" && !slices.Contains([]string{"ok", "outcome_mismatch", "insufficient_evidence"}, p.FinalVerdictKind) || !workflowString(p.VerdictActorRef, 70) || p.EvidenceCount < 0 || !workflowDigest(p.ChangedRefsDigest) || !slices.Contains([]string{"breaking", "non-breaking"}, p.ImpactVerdict) || !workflowList(p.Warnings, 16, 0) {
		return newFailure(KindInvalidPayload, "fold_event", "completed has invalid terminal metadata", false, "supply closed terminal metadata including impact_verdict")
	}
	plan, err := readWorkflowPlanTx(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if p.TerminalState == "completed" {
		if plan == nil || !WorkflowStepPlanComplete(*plan) {
			return newFailure(KindIllegalLifecycleTransition, "fold_event", "workflow plan is not complete", false, "complete every plan step")
		}
	}
	if err := requireActor(ctx, tx, event.Actor); err != nil {
		return err
	}
	if err := requireActor(ctx, tx, p.VerdictActorRef); err != nil {
		return err
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET instance_state=?,completed_at=?,completion_payload=?,completion_occurred_at=? WHERE work_id=? AND instance_state NOT IN ('completed','cancelled','superseded')`, p.TerminalState, event.OccurredAt.UTC().Format(time.RFC3339Nano), string(event.Payload), event.OccurredAt.UTC().Format(time.RFC3339Nano), event.SubjectID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return newFailure(KindInvalidOperation, "fold_event", "workflow is already terminal or missing", false, "complete an active workflow once")
	}
	return nil
}

func workflowJSON(value any) string { encoded, _ := json.Marshal(value); return string(encoded) }

func validateWorkflowOutcome(raw json.RawMessage, kind string) bool {
	var object map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &object) != nil || object == nil {
		return false
	}
	var declared string
	if json.Unmarshal(object["kind"], &declared) != nil || declared != kind {
		return false
	}
	switch kind {
	case "exists", "absent":
		var v struct {
			Kind        string   `json:"kind"`
			Surface     string   `json:"surface"`
			Subjects    []string `json:"subjects"`
			Distinguish []string `json:"distinguish_from,omitempty"`
		}
		if !decodeStrict(raw, &v) || !workflowString(v.Surface, 128) || !workflowList(v.Subjects, 100, 1) {
			return false
		}
		if kind == "absent" {
			if len(v.Distinguish) < 1 || len(v.Distinguish) > 4 || !allClosed(v.Distinguish, []string{"archived", "relocated", "renamed", "disabled"}) {
				return false
			}
		}
		return true
	case "outcome":
		var v struct {
			Kind           string          `json:"kind"`
			Allowed        []string        `json:"allowed"`
			DecisionRecord json.RawMessage `json:"decision_record,omitempty"`
		}
		if !decodeStrict(raw, &v) || len(v.Allowed) < 1 || len(v.Allowed) > 8 {
			return false
		}
		return allClosed(v.Allowed, []string{"no_change", "accepted_decision", "insufficient_evidence", "resolved", "remediated", "report_recorded", "completed", "operator_defined"})
	case "check":
		var v struct {
			Kind                string `json:"kind"`
			CheckRef            string `json:"check_ref"`
			ImmutableSubjectRef string `json:"immutable_subject_ref"`
			ExpectedResult      string `json:"expected_result"`
		}
		if !decodeStrict(raw, &v) || !workflowString(v.CheckRef, 134) || !workflowString(v.ImmutableSubjectRef, 256) {
			return false
		}
		return slices.Contains([]string{"true", "false", "pass", "fail", "present", "absent", "healthy", "unhealthy", "accepted", "rejected"}, v.ExpectedResult)
	}
	return false
}
func decodeStrict(raw []byte, target any) bool {
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	return d.Decode(target) == nil
}
func allClosed(values, allowed []string) bool {
	seen := map[string]bool{}
	for _, v := range values {
		if !slices.Contains(allowed, v) || seen[v] {
			return false
		}
		seen[v] = true
	}
	return true
}

// workflowPredicateOrdinalTeaching states the position rule
// validateWorkflowContractPredicate enforces on every outcome predicate; the
// teaching projection publishes the same text from this guard site.
const workflowPredicateOrdinalTeaching = "The store refuses an outcome predicate whose ordinal is not its own zero-based position in outcome_predicates: the predicate at array position N must carry ordinal N (0, 1, 2, ...)."

// workflowOutcomeKindEqualityTeaching states the nested equality the
// contract_approved fold enforces through DecodeWorkflowPredicate — the
// decoded payload's kind must equal the entry's outcome_kind — and the
// engine's cross-field kind-match declaration enforces at payload
// validation. The teaching projection publishes the same text from this
// guard site, and the generated per-kind item branches carry it on the
// discriminated outcome_kind (CON-412).
const workflowOutcomeKindEqualityTeaching = "The store refuses an outcome predicate whose outcome_kind and outcome_payload.kind disagree: supply the outcome_payload variant that matches the declared outcome_kind."

// validateWorkflowContractPredicate refuses one outcome predicate and names the
// rule it broke and the value that broke it. The predicate set carries seven
// independent rules; reporting them as one opaque set failure forces the caller
// to read this source to learn which rule applied, and the prefix rule in
// particular appears in no schema the caller can see.
func validateWorkflowContractPredicate(ordinal int, predicate workflowContractPredicatePayload, seen map[string]bool) error {
	const prefix = "predicate:"
	id := predicate.PredicateID
	switch {
	case id == "":
		return newFailure(KindInvalidPayload, "fold_event",
			fmt.Sprintf("outcome predicate at ordinal %d has no predicate_id", ordinal), false,
			"give every outcome predicate a predicate_id prefixed with \"predicate:\"")
	case !strings.HasPrefix(id, prefix):
		return newFailure(KindInvalidPayload, "fold_event",
			fmt.Sprintf("outcome predicate id %q must start with %q", id, prefix), false,
			"prefix the predicate_id with \"predicate:\"")
	case len(id) < 11 || len(id) > 128:
		return newFailure(KindInvalidPayload, "fold_event",
			fmt.Sprintf("outcome predicate id %q is %d characters, want 11-128", id, len(id)), false,
			"supply a predicate_id of 11-128 characters")
	case seen[id]:
		return newFailure(KindInvalidPayload, "fold_event",
			fmt.Sprintf("outcome predicate id %q appears more than once", id), false,
			"give every outcome predicate a distinct predicate_id")
	case predicate.Ordinal != ordinal:
		return newFailure(KindInvalidPayload, "fold_event",
			fmt.Sprintf("outcome predicate %q declares ordinal %d at position %d", id, predicate.Ordinal, ordinal), false,
			"set each predicate ordinal to its own position in the set")
	case predicate.OutcomeKind == "":
		return newFailure(KindInvalidPayload, "fold_event",
			fmt.Sprintf("outcome predicate %q has no outcome_kind", id), false,
			"supply exists, absent, outcome, or check as the outcome_kind")
	case len(predicate.OutcomePayload) == 0:
		return newFailure(KindInvalidPayload, "fold_event",
			fmt.Sprintf("outcome predicate %q has no outcome_payload", id), false,
			"supply the outcome_payload matching the declared outcome_kind")
	}
	return nil
}
