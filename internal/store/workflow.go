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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

// Workflow event names are deliberately closed. Adding one is a contract change.
const (
	WorkflowDefinitionSelected          = "workflow.definition_selected"
	WorkflowContractApproved            = "workflow.contract_approved"
	WorkflowOverlapResolved             = "workflow.overlap_resolved"
	WorkflowContractSuperseded          = "workflow.contract_superseded"
	WorkflowCandidateSetRevised         = "workflow.candidate_set_revised"
	WorkflowActorRecorded               = "workflow.actor_recorded"
	WorkflowActionStarted               = "workflow.action_started"
	WorkflowActionCheckpointed          = "workflow.action_checkpointed"
	WorkflowActionCompleted             = "workflow.action_completed"
	WorkflowActionFailed                = "workflow.action_failed"
	WorkflowEvidenceBound               = "workflow.evidence_bound"
	WorkflowStalenessObserved           = "workflow.staleness_observed"
	WorkflowVerdictRecorded             = "workflow.verdict_recorded"
	WorkflowPremiseConfirmed            = "workflow.premise_confirmed"
	WorkflowSuccessorLinked             = "workflow.successor_linked"
	WorkflowImpactDeclared              = "workflow.impact_declared"
	WorkflowImpactNoticeRecorded        = "workflow.impact_notice_recorded"
	WorkflowConditionAdded              = "workflow.condition_added"
	WorkflowConditionResolved           = "workflow.condition_resolved"
	WorkflowConditionCancelled          = "workflow.condition_cancelled"
	WorkflowContextCheckpointed         = "workflow.context_checkpointed"
	WorkflowContextBoundaryCrossed      = "workflow.context_boundary_crossed"
	WorkflowWorkContextRecorded         = "workflow.work_context_recorded"
	WorkflowProposalRecorded            = "workflow.proposal_recorded"
	WorkflowDesignRecorded              = "workflow.design_recorded"
	WorkflowBacklogAlignmentRecorded    = "workflow.backlog_alignment_recorded"
	WorkflowDiagnosisRecorded           = "workflow.diagnosis_recorded"
	WorkflowDeliveryCorrected           = "workflow.delivery_corrected"
	WorkflowCompleted                   = "workflow.completed"
	WorkflowOutsideRepairDispositionSet = "workflow.outside_repair_disposition_set"
	WorkflowOutsideRepairReconciled     = "workflow.outside_repair_reconciled"
	WorkflowOutsideRepairResumed        = "workflow.outside_repair_resumed"
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
	LawRevisions        []WorkflowLawRevision              `json:"law_revisions"`
	ArchitectureBinding *WorkflowArchitectureBinding       `json:"architecture_binding,omitempty"`
	SelfRepair          *WorkflowSelfRepair                `json:"self_repair,omitempty"`
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
	// LawChange records a consequence_v1 contract's law change with its
	// derived Domains and seriousness (CD-0230 D4); nil when it changes none.
	LawChange *WorkflowLawChange `json:"law_change,omitempty"`
}

// workflowContractLawChangePayloadVersion is the workflow.contract_approved
// current payload version.
const workflowContractLawChangePayloadVersion = 6

// workflowContractSupersededLawChangePayloadVersion is the
// workflow.contract_superseded payload version whose successor_contract may
// carry law_change.
const workflowContractSupersededLawChangePayloadVersion = 5

// workflowDecisionDomainSet is the closed canonical decision-domain set. It is
// sorted, so canonical order is the slice order.
var workflowDecisionDomainSet = []string{"preference", "product", "ui_ux"}

// validateWorkflowDecisionDomains holds the typed fact to its canonical form:
// a sorted unique subset of the closed set with at most one entry per domain.
// nil stays legal here; the caller decides what absence means.
func validateWorkflowDecisionDomains(domains []string) error {
	if len(domains) > len(workflowDecisionDomainSet) {
		return newFailure(KindInvalidPayload, "fold_event", "contract decision_domains exceeds the closed domain set", false, "declare at most one entry per decision domain")
	}
	previous := ""
	for _, domain := range domains {
		known := false
		for _, candidate := range workflowDecisionDomainSet {
			if domain == candidate {
				known = true
				break
			}
		}
		if !known {
			return newFailure(KindInvalidPayload, "fold_event", "contract decision_domains names a domain outside the closed set", false, "declare only preference, product, or ui_ux")
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
	ToWorkID                string `json:"to_work_id"`
	ToExpectedVersion       int64  `json:"to_expected_version"`
	ToResultingVersion      int64  `json:"to_resulting_version"`
	FromContractVersion     int64  `json:"from_contract_version"`
	ToContractVersion       int64  `json:"to_contract_version"`
	ResolutionKind          string `json:"resolution_kind"`
	Reason                  string `json:"reason"`
	ApprovalRef             string `json:"approval_ref"`
	ApprovalOperationDigest string `json:"approval_operation_digest"`
	ApprovalScopeJSON       string `json:"approval_scope_json"`
	ApprovalVersionsJSON    string `json:"approval_versions_json"`
	ApprovalConsequence     string `json:"approval_consequence"`
}

func (p workflowOverlapResolvedPayload) workflowVersionFields() WorkflowVersionFields {
	return p.WorkflowVersionFields
}

type workflowContractSupersededPayload struct {
	WorkflowVersionFields
	PreviousContractVersion     int64    `json:"previous_contract_version"`
	NewContractVersion          int64    `json:"new_contract_version"`
	PredecessorContractVersions []int64  `json:"predecessor_contract_versions,omitempty"`
	SupersedeReason             string   `json:"supersede_reason"`
	AuditEvidence               []string `json:"audit_evidence"`
	ApprovalRef                 string   `json:"approval_ref,omitempty"`
	ApprovalOperationDigest     string   `json:"approval_operation_digest,omitempty"`
	ApprovalScopeJSON           string   `json:"approval_scope_json,omitempty"`
	ApprovalVersionsJSON        string   `json:"approval_versions_json,omitempty"`
	ApprovalConsequence         string   `json:"approval_consequence,omitempty"`
	// SuccessorContract is present for the operator-approved stale-law
	// recovery route. When present, the event installs the fully supplied
	// successor instead of cloning the prior contract.
	SuccessorContract *workflowContractApprovedPayload `json:"successor_contract,omitempty"`
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
	ActionID        string `json:"action_id,omitempty"`
	StepID          string `json:"step_id"`
	AttemptEpoch    int64  `json:"attempt_epoch"`
	WorkerAttemptID string `json:"worker_attempt_id,omitempty"`
	// CD-0059 D1/D5: dispatch_worker binds the attempt identity it
	// authorized so the worker-dispatch evidence boundary can prove the
	// window belongs to the claim. The fold validates the pair on the
	// next action that opens or accepts against this step epoch. CD-0067
	// D2 additionally binds the canonical digest of the lane packet so
	// the durable record names what the window was opened for.
	WorkerLaneID string `json:"worker_lane_id,omitempty"`
	// The dispatch_worker completion binds the authorized lane's registry
	// identity next to the attempt and packet digest, so the fold can bind
	// the attempt in flight from the durable record alone. The completion
	// builder writes the quartet together; a completion without one is a
	// pre-binding authorization the fold leaves unbound.
	WorkerLaneDigest         string                           `json:"worker_lane_digest,omitempty"`
	WorkerCapabilityClass    string                           `json:"worker_capability_class,omitempty"`
	WorkerPacketDigest       string                           `json:"worker_packet_digest,omitempty"`
	WorkerSubjectCommit      string                           `json:"worker_subject_commit,omitempty"`
	WorkerOraclePreparations []WorkerOraclePreparationBinding `json:"worker_oracle_preparations,omitempty"`
	// WorkerPacketPredicateIDs carries the typed outcome predicate ids the
	// dispatch_worker authorization extracted from the same packet bytes it
	// digested. The worker completion fold reads them as the immutable
	// discharge obligation set, so a report cannot narrow its own
	// obligations. The list marshals unconditionally, empty when the packet
	// declared no typed predicates, so the fold can refuse a dispatch record
	// that carries no list instead of reading its absence as none.
	WorkerPacketPredicateIDs []string `json:"worker_packet_predicate_ids"`
	WorkerWorktreeIdentity   string   `json:"worker_worktree_identity,omitempty"`
	// WorkerContextFrontier is the source-event frontier of the admitted
	// work-context view the dispatch_worker authorization recorded
	// New authorizations always record it — explicit zero when
	// the view was absent — and the terminal ingest reads it as the frontier
	// the terminal report consumed. Nil on authorizations recorded before
	// the field existed: their absence is history, never a zero.
	WorkerContextFrontier    *int64                    `json:"worker_context_frontier,omitempty"`
	CorrectionDiagnosis      string                    `json:"correction_diagnosis,omitempty"`
	CorrectionStrategy       string                    `json:"correction_strategy,omitempty"`
	CorrectionEvidenceRefs   []string                  `json:"correction_evidence_refs,omitempty"`
	CorrectionPredicateIDs   []string                  `json:"correction_predicate_ids,omitempty"`
	CorrectionOpenFindingIDs []string                  `json:"correction_open_finding_ids,omitempty"`
	RetryConvergence         *WorkflowRetryConvergence `json:"retry_convergence,omitempty"`
	// ChecksRepairReview records the dispatch_worker authorization that
	// dispatched the one confirming review of an unconfirmed checks repair
	// (CD-0205 D4): the exact satisfied repairing revision the packet bound,
	// the repairing acceptance the admission is bound to, and whether the
	// admission was funded or an ordinary-authority repeat. The walked
	// repair state reads it as the confirming dispatch; a repeat repair's
	// review records funded=false and rides ordinary wall authority.
	ChecksRepairReview *WorkerChecksRepairReviewAdmission `json:"checks_repair_review,omitempty"`
	DeliveryArtifact   string                             `json:"delivery_artifact,omitempty"`
	DeliveryState      string                             `json:"delivery_state,omitempty"`
	// ReviewAdvanceHeld records local acceptance without a delivery assertion.
	// The disposition is accepted while execution remains at its current step.
	ReviewAdvanceHeld bool `json:"review_advance_held,omitempty"`
	// FailureAttribution is the coordinator's typed failure attribution on
	// a rejected result or a correction request : the direct
	// producer observation the request carried, beside the core-derived
	// identity the store resolved from the cited run or the exact action
	// attempt. The terminal worker.failed event owns an attempt's
	// attribution; a derived failure record never duplicates the block and
	// refers to it by event id instead. Absent on every completion that
	// predates the attribution payload version.
	FailureAttribution *WorkerFailureAttribution `json:"failure_attribution,omitempty"`
	// FailureAttributionRef names the terminal worker.failed event whose
	// attribution one derived failure record references (CD-0198 D5
	// at the derived attribution fact): the terminal owns the observation and the
	// derived record refers to it by event id, never a copied block. The
	// fold verifies the reference against the same retained event and
	// attempt the derivation pair admits.
	FailureAttributionRef string `json:"failure_attribution_ref,omitempty"`
	// WorkerJob carries the recorded worker-job disposition an acceptance
	// satisfies (CD-0205). The accept guard derives it from the accepted
	// attempt's worker.dispatched binding — the caller never asserts it —
	// so the correction window closes on the dispatched job's own recorded
	// disposition and never on a name match against another job. Absent on
	// every acceptance whose attempt was dispatched without a job binding.
	WorkerJob       *WorkerJobBinding      `json:"worker_job,omitempty"`
	AssignmentGroup *workflowMemberGroup   `json:"assignment_group,omitempty"`
	MemberBinding   *workflowMemberBinding `json:"member_binding,omitempty"`
	// WorkerCallerExclusions is the caller_exclusions set the core computed
	// and bound to a size-capable implementation dispatch's packet (CD-0227
	// D1). The packet bytes are not retained, so acceptance reads the
	// exclusions the worker was given from here. Absent when the set was
	// empty.
	WorkerCallerExclusions []string `json:"worker_caller_exclusions,omitempty"`
	// SupplementalClassifications and WorkerProductionSize decode the
	// measurement content a v6+ acceptance may carry; the measurement is
	// retired (CD-0242 D6) and no live acceptance records either member.
	SupplementalClassifications []WorkerJobMeasuredPath `json:"supplemental_classifications,omitempty"`
	WorkerProductionSize        *WorkerProductionSize   `json:"worker_production_size,omitempty"`
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
	CheckpointID             string   `json:"checkpoint_id"`
	CheckpointSequence       int64    `json:"checkpoint_sequence"`
	StepID                   string   `json:"step_id"`
	AttemptEpoch             int64    `json:"attempt_epoch"`
	ActiveUnit               string   `json:"active_unit"`
	Hypothesis               string   `json:"hypothesis"`
	Diagnosis                string   `json:"diagnosis"`
	Strategy                 string   `json:"strategy"`
	TouchedRefs              []string `json:"touched_refs"`
	EvidenceRefs             []string `json:"evidence_refs"`
	PendingQuestions         []string `json:"pending_questions"`
	PendingDecisions         []string `json:"pending_decisions"`
	WorkflowRef              string   `json:"workflow_ref"`
	WorkflowDefinitionDigest string   `json:"workflow_definition_digest"`
	ActorRef                 string   `json:"actor_ref"`
	RequestID                string   `json:"request_id"`
}

type workflowContextBoundaryCrossedPayload struct {
	WorkflowVersionFields
	BoundaryID               string `json:"boundary_id"`
	BoundarySequence         int64  `json:"boundary_sequence"`
	BoundaryKind             string `json:"boundary_kind"`
	CheckpointID             string `json:"checkpoint_id"`
	CheckpointSequence       int64  `json:"checkpoint_sequence"`
	Summary                  string `json:"summary"`
	WorkflowRef              string `json:"workflow_ref"`
	WorkflowDefinitionDigest string `json:"workflow_definition_digest"`
	AttemptEpoch             int64  `json:"attempt_epoch"`
	ActorRef                 string `json:"actor_ref"`
	RequestID                string `json:"request_id"`
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

func decodeWorkflowProposalContent(raw json.RawMessage) (workflowProposalContent, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var content workflowProposalContent
	if err := decodeWorkflowActionPayloadStrict(raw, &content); err != nil {
		return content, newFailure(KindInvalidPayload, "workflow_proposal", "proposal payload is not one strict JSON object", false, "supply the closed proposal document")
	}
	if err := validateWorkflowProposalContent(content); err != nil {
		return content, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return content, newFailure(KindInvalidPayload, "workflow_proposal", "proposal payload is not one strict JSON object", false, "supply the closed proposal document")
	}
	for _, name := range []string{"constraints", "open_questions", "out_of_scope"} {
		if value, present := fields[name]; present && strings.TrimSpace(string(value)) == "null" {
			return content, newFailure(KindInvalidPayload, "workflow_proposal", "proposal optional list cannot be null", false, "supply an array or omit the optional list")
		}
	}
	content.Affected = nonNilStrings(content.Affected)
	content.UserOutcomes = nonNilStrings(content.UserOutcomes)
	content.Constraints = nonNilStrings(content.Constraints)
	content.OpenQuestions = nonNilStrings(content.OpenQuestions)
	content.OutOfScope = nonNilStrings(content.OutOfScope)
	return content, nil
}

type workflowDesignRecordedPayload struct {
	WorkflowVersionFields
	workflowDesignContent
}

type workflowEvidenceBoundPayload struct {
	WorkflowVersionFields
	EvidenceKind        string `json:"evidence_kind"`
	ImmutableSubjectRef string `json:"immutable_subject_ref"`
	ProducerID          string `json:"producer_id"`
	ProducerRunRef      string `json:"producer_run_ref"`
	ProducerWatermark   string `json:"producer_watermark"`
	ObservedAt          string `json:"observed_at"`
}

type workflowStalenessObservedPayload struct {
	RuleID               string `json:"rule_id"`
	Severity             string `json:"severity"`
	Drifted              bool   `json:"drifted"`
	ObservedAt           string `json:"observed_at"`
	AcceptedInputsDigest string `json:"accepted_inputs_digest"`
}

type workflowVerdictRecordedPayload struct {
	WorkflowVersionFields
	ContractVersion          int64                  `json:"contract_version"`
	PredicateID              string                 `json:"predicate_id"`
	VerdictKind              string                 `json:"verdict_kind"`
	VerdictActorRef          string                 `json:"verdict_actor_ref"`
	EvaluationEvidence       []string               `json:"evaluation_evidence"`
	CriterionDigest          string                 `json:"criterion_digest,omitempty"`
	Candidate                *WorkerReviewCandidate `json:"candidate,omitempty"`
	IncomparableWithApproved bool                   `json:"incomparable_with_approved"`
	// VerdictModel is the readback executing-model identity of the review run
	// (CD-0017 D5). Empty when the evaluator dispatched no typed lane.
	VerdictModel string `json:"verdict_model,omitempty"`
}

type workflowPremiseConfirmedPayload struct {
	WorkflowVersionFields
	ContractVersion    int64  `json:"contract_version"`
	ConfirmingActorRef string `json:"confirming_actor_ref"`
	// ConfirmationAuthority records whose authority the confirmation
	// carries: "operator" for the verified operator identity, "coordinator"
	// for consequence_v1's authenticated non-worker coordinator.
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

type workflowImpactDeclaredPayload struct {
	WorkflowVersionFields
	EdgeID       string `json:"edge_id"`
	EdgeKind     string `json:"edge_kind"`
	EdgeClass    string `json:"edge_class"`
	TargetWorkID string `json:"target_work_id"`
	TargetKind   string `json:"target_kind"`
	Severity     string `json:"severity"`
}

type workflowImpactNoticeRecordedPayload struct {
	WorkflowVersionFields
	NoticeID              string  `json:"notice_id"`
	SourceContractVersion int64   `json:"source_contract_version"`
	EntityKind            string  `json:"entity_kind"`
	EntityRef             string  `json:"entity_ref"`
	TargetWorkID          string  `json:"target_work_id"`
	EdgeOwnerWorkID       string  `json:"edge_owner_work_id"`
	EdgeID                string  `json:"edge_id"`
	OldHash               *string `json:"old_hash"`
	NewHash               *string `json:"new_hash"`
	Severity              string  `json:"severity"`
}

type workflowConditionAddedPayload struct {
	WorkflowVersionFields
	ConditionID         string `json:"condition_id"`
	AwaitType           string `json:"await_type"`
	AwaitRef            string `json:"await_ref"`
	ResolutionAuthority string `json:"resolution_authority"`
	// ExpectedWithinSeconds bounds the wait; beyond it the await reads as
	// overdue/unverified (issue #87). Zero means no declared expectation.
	ExpectedWithinSeconds int64 `json:"expected_within_seconds,omitempty"`
}

type workflowConditionResolvedPayload struct {
	WorkflowVersionFields
	ConditionID        string   `json:"condition_id"`
	ResolutionEvidence []string `json:"resolution_evidence"`
	ResolvedByEvent    string   `json:"resolved_by_event"`
}

type workflowConditionCancelledPayload struct {
	WorkflowVersionFields
	ConditionID           string   `json:"condition_id"`
	CancellationAuthority string   `json:"cancellation_authority"`
	CancellationEvidence  []string `json:"cancellation_evidence"`
	CancelledByEvent      string   `json:"cancelled_by_event"`
}

type workflowCompletedPayload struct {
	WorkflowVersionFields
	TerminalState     string   `json:"terminal_state"`
	FinalVerdictKind  string   `json:"final_verdict_kind"`
	VerdictActorRef   string   `json:"verdict_actor_ref"`
	PremiseConfirmed  bool     `json:"premise_confirmed"`
	EvidenceCount     int64    `json:"evidence_count"`
	ChangedRefsDigest string   `json:"changed_refs_digest"`
	ImpactVerdict     string   `json:"impact_verdict"`
	Warnings          []string `json:"warnings,omitempty"`
}

// Outside repair records operator authority independently of workflow gates.
type workflowOutsideRepairDispositionPayload struct {
	WorkflowVersionFields
	OutsideRepairApproval
	Reason string `json:"reason"`
	State  string `json:"state"`
}

type workflowOutsideRepairReconcilePayload struct {
	workflowOutsideRepairDispositionPayload
	Evidence       OutsideRepairEvidence `json:"evidence"`
	EvidenceSource string                `json:"evidence_source"`
}

type workflowOutsideRepairResumePayload = workflowOutsideRepairDispositionPayload

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

func requireActor(ctx context.Context, tx *sql.Tx, actorRef string) error {
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

func conditionAuthorityOperation(ctx context.Context, tx *sql.Tx, authority, workID string) (string, error) {
	const prefix = "durable_operation:"
	if !strings.HasPrefix(authority, prefix) || len(authority) <= len(prefix) {
		return "", newFailure(KindInvalidPayload, "fold_event", "resolution_authority is not a durable operation reference", false, "use durable_operation:<op_id>")
	}
	opID := strings.TrimPrefix(authority, prefix)
	var storedWorkID string
	if err := tx.QueryRowContext(ctx, `SELECT work_id FROM durable_operations WHERE op_id=? ORDER BY attempt_epoch DESC LIMIT 1`, opID).Scan(&storedWorkID); err != nil {
		if err == sql.ErrNoRows {
			return "", newFailure(KindInvalidOperation, "fold_event", "resolution authority operation is not recorded", false, "record the durable authority operation first")
		}
		return "", wrapFailure(KindUnavailable, "fold_event", "cannot inspect resolution authority operation", true, "retry once the authority is readable", err)
	}
	if storedWorkID != workID {
		return "", newFailure(KindInvalidOperation, "fold_event", "resolution authority belongs to a different work item", false, "use an authority operation for this work item")
	}
	return opID, nil
}

func verifyConditionEvidence(ctx context.Context, tx *sql.Tx, authority, workID string, refs []string) error {
	opID, err := conditionAuthorityOperation(ctx, tx, authority, workID)
	if err != nil {
		return err
	}
	var resultKind string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(result_kind,'') FROM durable_operations WHERE op_id=? AND work_id=? ORDER BY attempt_epoch DESC LIMIT 1`, opID, workID).Scan(&resultKind); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot inspect resolution authority result", true, "retry once the authority is readable", err)
	}
	if resultKind != "completed" {
		return newFailure(KindInvalidOperation, "fold_event", "resolution authority has no completed evidence result", false, "complete the authority operation with evidence first")
	}
	for _, ref := range refs {
		var found int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM durable_operations WHERE op_id=? AND work_id=? AND result_kind='completed' AND EXISTS (SELECT 1 FROM json_each(durable_operations.evidence_refs) WHERE value=?)`, opID, workID, ref).Scan(&found); err != nil {
			return wrapFailure(KindUnavailable, "fold_event", "cannot verify resolution evidence authority", true, "retry once the authority is readable", err)
		}
		if found != 1 {
			return newFailure(KindInvariantViolation, "fold_event", "condition evidence is not bound to its stored resolution authority", false, "supply evidence emitted by the stored authority operation")
		}
	}
	return nil
}

func workflowDigest(value string) bool {
	return len(value) == 71 && strings.HasPrefix(value, "sha256:")
}

func foldWorkflowDefinitionSelected(ctx context.Context, tx *sql.Tx, event Event, scope *writeScope) error {
	if active, err := outsideRepairActiveTx(ctx, tx, event.SubjectID); err != nil {
		return err
	} else if active {
		return workflowAdmit(WorkflowDefinition{}, WorkflowAdmissionState{OutsideRepairActive: true}, "select_definition").Failure
	}
	var p workflowDefinitionSelectedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if !workflowString(p.Ref, 128) || !workflowDigest(p.Digest) || !workflowKinds[p.WorkKind] {
		return newFailure(KindInvalidPayload, "fold_event", "definition_selected contains an invalid definition pin", false, "supply a closed definition reference, digest, and family")
	}
	registered, err := verifyWorkflowDefinitionPinForFold(scopeDefinitionRegistry(scope), WorkflowDefinitionPin{Ref: p.Ref, Digest: p.Digest})
	if err != nil {
		return err
	}
	if registered.Digest != p.Digest {
		return workflowPinFailure("requested workflow definition is not current")
	}
	var currentRef, currentStep string
	definitionErr := tx.QueryRowContext(ctx, `SELECT definition_ref,current_step FROM workflow_instances WHERE work_id=?`, event.SubjectID).Scan(&currentRef, &currentStep)
	if definitionErr != nil && definitionErr != sql.ErrNoRows {
		return workflowProjectionError(definitionErr, "cannot read the current workflow definition")
	}
	definitionExists := definitionErr == nil
	// Selecting the same family never restarts work. Definition content is
	// resolved from the current registry, not from an in-flight version pin.
	sameFamily := definitionExists && currentRef == p.Ref
	if sameFamily {
		if _, stepErr := workflowDeclaredStep(registered.Definition, currentStep); stepErr != nil {
			return newFailure(KindInvalidOperation, "fold_event", "current definition does not declare the instance's current step", false, "migrate the work item's current step before continuing")
		}
	}
	if definitionExists && !sameFamily {
		var activeContracts int64
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, event.SubjectID).Scan(&activeContracts); err != nil {
			return workflowProjectionError(err, "cannot inspect contracts before changing the workflow definition")
		}
		if activeContracts != 0 {
			return newFailure(KindInvalidOperation, "fold_event", "workflow definition change would leave an existing contract authoritative", false, "supersede the contract with the replacement definition")
		}
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	var started int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_step_epochs WHERE work_id=?)`, event.SubjectID).Scan(&started); err != nil {
		return err
	}
	if started > 0 && !sameFamily {
		return newFailure(KindInvalidOperation, "fold_event", "definition cannot change after execution starts", false, "supersede the workflow contract instead")
	}
	if sameFamily {
		return pinWorkflowInstanceToStepTx(ctx, tx, event.SubjectID, registered, currentStep, event.Actor)
	}
	return pinWorkflowInstanceTx(ctx, tx, event.SubjectID, registered, event.Actor)
}

func workflowProjectionError(err error, message string) error {
	if err == nil {
		return nil
	}
	if isIdentityConflict(err) {
		return newFailure(KindProjectionConflict, "fold_event", message, false, "append a new workflow version")
	}
	return wrapFailure(KindUnavailable, "fold_event", fmt.Sprintf("%s: %v", message, err), true, "retry once the database is writable", err)
}

// validateWorkflowContractApprovalLaw checks the law and architecture declarations an approval carries against the pinned definition: a
// Product-changing contract must carry the current law boundary and a complete binding, and every other contract must carry none of that
// authority. changesProductTruth is the pinned definition's Product-truth class, and contractFields is the raw approval payload the
// composed fields are read from.
func validateWorkflowContractApprovalLaw(ctx context.Context, tx *sql.Tx, event Event, registered RegisteredDefinition, contractFields map[string]json.RawMessage, p workflowContractApprovedPayload, changesProductTruth bool) error {
	if changesProductTruth {
		if p.LawBoundaryVersion != 1 {
			return newFailure(KindInvalidPayload, "fold_event", "Product-changing contract must carry the current law boundary", false, "supply law_boundary_version=1")
		}
		for _, field := range []string{"spec_mandate", "law_modifies", "law_revisions", "architecture_binding"} {
			if _, present := contractFields[field]; !present {
				return newFailure(KindInvalidPayload, "fold_event", "Product-changing contract is missing composed field "+field, false, "supply every architecture-bound contract field")
			}
		}
		if p.ArchitectureBinding == nil || p.LawRevisions == nil {
			return newFailure(KindInvalidPayload, "fold_event", "Product-changing contract contains null architecture-bound fields", false, "supply the complete architecture binding and law pins")
		}
		if err := validateArchitectureBindingTx(ctx, tx, event.SubjectID, registered.Definition, p.ArchitectureBinding, p.SpecMandate, p.LawModifies, p.LawRevisions); err != nil {
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
		if p.LawRevisions != nil {
			if err := validateWorkflowLawRevisions(p.SpecMandate, p.LawRevisions); err != nil {
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
	if err := normalizeWorkflowContractApproval(ctx, &p); err != nil {
		return err
	}
	return foldValidatedWorkflowContractApproval(ctx, tx, event, scope, p)
}

func normalizeWorkflowContractApproval(ctx context.Context, p *workflowContractApprovedPayload) error {
	if p.DecisionDomains != nil {
		if err := validateWorkflowDecisionDomains(p.DecisionDomains); err != nil {
			return err
		}
	}
	if p.ContractVersion <= 0 || !workflowString(p.Premise, WorkflowPremiseMaxLength) || !workflowList(p.RequiredEvidence, 7, 0) || !workflowList(p.RouteConventions, 16, 0) || !workflowList(p.SpecMandate, 32, 0) || !workflowList(p.LawModifies, 32, 0) {
		return newFailure(KindInvalidPayload, "fold_event", "contract_approved contains invalid contract fields", false, "supply a strict closed workflow outcome and bounded contract fields")
	}
	if slices.Contains(p.RouteConventions, workflowCompleteStepCorrectionRoute) && !inWorkflowContractSupersessionContext(ctx) {
		return newFailure(KindInvalidPayload, "fold_event", "route convention complete_step_correction is reserved for the complete-step correction successor", false, "drop the reserved route convention")
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
	if activeCount != 0 && !inWorkflowContractSupersessionContext(ctx) {
		return newFailure(KindInvariantViolation, "fold_event", "contract approval requires no active workflow contract", false, "supersede the active contract through the typed recovery route")
	}
	if activeCount == 0 && p.ContractVersion != 1 && !inWorkflowContractSupersessionContext(ctx) {
		return newFailure(KindInvalidPayload, "fold_event", "initial workflow contract approval must be version 1", false, "approve the initial contract as version 1")
	}
	if p.LawModifies == nil {
		p.LawModifies = []string{}
	}
	if p.SpecMandate == nil {
		p.SpecMandate = []string{}
	}
	var contractFields map[string]json.RawMessage
	if err := json.Unmarshal(event.Payload, &contractFields); err != nil {
		return newFailure(KindInvalidPayload, "fold_event", "contract_approved payload is malformed", false, "repair the workflow contract payload")
	}
	if p.LawBoundaryVersion == 1 {
		if _, present := contractFields["law_revisions"]; !present {
			return newFailure(KindInvalidPayload, "fold_event", "current law-aware contract approval is missing law revision pins", false, "capture one current Git law hash for every mandated law ID")
		}
	}
	registered, definitionErr := VerifyWorkflowInstanceDefinitionTx(ctx, tx, scopeDefinitionRegistry(scope), event.SubjectID)
	if definitionErr != nil {
		return definitionErr
	}
	if err := validateWorkflowContractApprovalPolicy(ctx, tx, event, registered, contractFields, p); err != nil {
		return err
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if err := requireActor(ctx, tx, event.Actor); err != nil {
		return err
	}
	if err := validateWorkflowSelfRepairAuthorityTx(ctx, tx, event.SubjectID, event.Actor, p.SelfRepair); err != nil {
		return err
	}
	return persistWorkflowContractApprovalTx(ctx, tx, event, registered, p)
}

func validateWorkflowContractApprovalPolicy(ctx context.Context, tx *sql.Tx, event Event, registered RegisteredDefinition, contractFields map[string]json.RawMessage, p workflowContractApprovedPayload) error {
	changesProductTruth := registered.Definition.ChangesProductTruth != nil && *registered.Definition.ChangesProductTruth
	// consequence_v1 answers consent from the contract's declared decision
	// domains, so a contract under that policy must declare the typed fact
	// itself: nil is the legacy absence shape and [] is explicit engineering.
	if workflowApprovalPolicyRequiresDecisionFact(registered.Definition) && p.DecisionDomains == nil {
		return newFailure(KindInvalidPayload, "fold_event", "consequence_v1 contract approval requires the declared decision-domain fact; absence is a legacy shape", false, "declare the contract's decision domains explicitly")
	}
	// A successor that changes no law itself still carries the law change its
	// predecessors recorded; the live check holds it to the item's baseline.
	tracksLawChange := workflowApprovalPolicyRequiresDecisionFact(registered.Definition)
	if p.LawChange != nil && !tracksLawChange {
		return newFailure(KindInvalidPayload, "fold_event", "law_change is recorded only on a consequence_v1 contract", false, "omit law_change")
	}
	if p.LawChange == nil && tracksLawChange && workflowContractChangesLaw(p.LawModifies, p.ArchitectureBinding) {
		return newFailure(KindInvalidPayload, "fold_event", "a consequence_v1 contract that changes law must record law_change", false, "record the law change with its reason")
	}
	if p.LawChange != nil {
		if err := validateWorkflowLawChangeShape(p.LawChange, p.DecisionDomains, p.LawModifies, p.ArchitectureBinding, changesProductTruth); err != nil {
			return err
		}
	}
	if err := validateLiveWorkflowLawChangeTx(ctx, tx, event.SubjectID, p, changesProductTruth); err != nil {
		return err
	}
	if err := validateWorkflowContractApprovalLaw(ctx, tx, event, registered, contractFields, p, changesProductTruth); err != nil {
		return err
	}
	if err := validateWorkflowApprovalLawRevisionBoundaryTx(ctx, tx, event.SubjectID, p, changesProductTruth); err != nil {
		return err
	}
	if p.ConsequenceClass != "internal_sqlite" && p.ConsequenceClass != "cross_authority" && p.ConsequenceClass != "external_effect" {
		return newFailure(KindInvalidPayload, "fold_event", "contract consequence class is not closed", false, "use internal_sqlite, cross_authority, or external_effect")
	}
	// The generic outcome union is only syntax.  Meaning is pinned by the
	// selected definition and must be checked before the immutable contract is
	// written; the fold already holds the verified entry, so it resolves the
	// pin once.
	for _, approved := range p.OutcomePredicates {
		predicate, decodeErr := DecodeWorkflowPredicate(approved.OutcomePayload)
		if decodeErr != nil || string(predicate.Kind) != approved.OutcomeKind {
			return newFailure(KindInvalidPayload, "fold_event", "contract outcome predicate is malformed or mismatched", false, "supply the strict registered outcome predicate")
		}
		if err := ValidateWorkflowPredicateForDefinition(registered.Definition, predicate); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkflowApprovalLawRevisionBoundaryTx(ctx context.Context, tx *sql.Tx, workID string, p workflowContractApprovedPayload, changesProductTruth bool) error {
	if p.LawBoundaryVersion != 1 {
		return nil
	}
	if err := validateLawModificationSubset(p.SpecMandate, p.LawModifies); err != nil {
		return err
	}
	if p.LawRevisions == nil && !changesProductTruth {
		if err := checkMandatedLawsTx(ctx, tx, workID, p.SpecMandate, p.LawModifies, true); err != nil {
			return err
		}
	}
	if p.LawRevisions != nil && len(p.SpecMandate) == 0 && len(p.LawRevisions) != 0 {
		return newFailure(KindInvalidPayload, "fold_event", "workflow law revision pins require a non-empty spec_mandate", false, "supply one pin for each mandated law")
	}
	return nil
}

func persistWorkflowContractApprovalTx(ctx context.Context, tx *sql.Tx, event Event, registered RegisteredDefinition, p workflowContractApprovedPayload) error {
	changesProductTruth := registered.Definition.ChangesProductTruth != nil && *registered.Definition.ChangesProductTruth
	// NULL on the projection column is historical absence, never engineering:
	// a contract that declares no array stores NULL, and [] stores the explicit
	// empty array.
	var decisionDomains any
	if p.DecisionDomains != nil {
		decisionDomains = workflowJSON(p.DecisionDomains)
	}
	var lawChange any
	if p.LawChange != nil {
		lawChange = workflowJSON(p.LawChange)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class,self_repair_json,definition_ref,definition_digest,decision_domains,law_change) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, event.SubjectID, p.ContractVersion, p.Premise, p.ConsequenceClass, workflowJSON(p.RequiredEvidence), workflowJSON(p.RouteConventions), event.OccurredAt.UTC().Format(time.RFC3339Nano), event.Actor, workflowJSON(p.SpecMandate), workflowJSON(p.LawModifies), p.LawBoundaryVersion, p.RigorClass, workflowJSON(p.SelfRepair), registered.Definition.Ref, registered.Digest, decisionDomains, lawChange)
	if err != nil {
		return workflowProjectionError(err, "cannot record immutable workflow contract")
	}
	if err := recordRuntimeStateWriter(ctx, tx, workflowStateWriter(WorkflowDefinitionPin{Ref: registered.Definition.Ref, Digest: registered.Digest})); err != nil {
		return err
	}
	for ordinal, predicate := range p.OutcomePredicates {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES(?,?,?,?,?,?)`, event.SubjectID, p.ContractVersion, predicate.PredicateID, ordinal, predicate.OutcomeKind, string(predicate.OutcomePayload)); err != nil {
			return workflowProjectionError(err, "cannot record workflow contract predicate")
		}
	}
	if p.LawRevisions != nil {
		for _, revision := range p.LawRevisions {
			if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contract_law_revisions(work_id,contract_version,law_id,content_hash) VALUES(?,?,?,?)`, event.SubjectID, p.ContractVersion, revision.LawID, revision.ContentHash); err != nil {
				return workflowProjectionError(err, "cannot record workflow law revision pin")
			}
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
	if !inWorkflowContractSupersessionContext(ctx) {
		if err := refreshWorkflowHealthySequence(ctx, tx, event.SubjectID, p.ContractVersion, true); err != nil {
			return err
		}
		return advanceWorkSizeProofFactsTx(ctx, tx, event)
	}
	return nil
}

// validateWorkflowContractSupersessionPredecessors normalizes and validates
// the predecessor set a supersession names: bounded versions, a consecutive
// successor version, an exact match with the active contracts, the recovery
// authorization a duplicate-projection recovery owes, and one active row per
// named predecessor. It returns the sorted predecessor versions the fold
// supersedes.
func validateWorkflowContractSupersessionPredecessors(ctx context.Context, tx *sql.Tx, registry DefinitionRegistry, event Event, p workflowContractSupersededPayload) ([]int64, error) {
	predecessors := p.PredecessorContractVersions
	if len(predecessors) == 0 && p.PreviousContractVersion > 0 {
		predecessors = []int64{p.PreviousContractVersion}
	}
	if !validWorkflowContractVersionList(predecessors) || !workflowString(p.SupersedeReason, 4096) || !workflowList(p.AuditEvidence, 32, 1) {
		return nil, newFailure(KindInvalidPayload, "fold_event", "contract_superseded has invalid version, reason, or audit evidence", false, "supersede one contract with a consecutive version and evidence")
	}
	predecessors = append([]int64(nil), predecessors...)
	sort.Slice(predecessors, func(i, j int) bool { return predecessors[i] < predecessors[j] })
	p.PredecessorContractVersions = predecessors
	if !containsInt64(predecessors, p.PreviousContractVersion) {
		return nil, newFailure(KindInvalidPayload, "fold_event", "contract supersession predecessor list does not contain the previous contract", false, "supply every exact predecessor contract version")
	}
	maxPredecessor := predecessors[0]
	for _, version := range predecessors[1:] {
		if version > maxPredecessor {
			maxPredecessor = version
		}
	}
	if p.NewContractVersion != maxPredecessor+1 {
		return nil, newFailure(KindInvalidPayload, "fold_event", "successor contract version must immediately follow every predecessor", false, "supply the next contract version")
	}
	var activeCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, event.SubjectID).Scan(&activeCount); err != nil {
		return nil, workflowProjectionError(err, "cannot inspect active workflow contracts")
	}
	if activeCount != len(predecessors) {
		return nil, newFailure(KindInvariantViolation, "fold_event", "contract supersession predecessor list does not match active workflow contracts", false, "supply the exact duplicate recovery set or restore a verified backup into a new path with concord restore, then swap")
	}
	if activeCount > 1 {
		registered, definitionErr := VerifyWorkflowInstanceDefinitionTx(ctx, tx, registry, event.SubjectID)
		if definitionErr != nil {
			return nil, definitionErr
		}
		if err := requireWorkflowDuplicateRecoveryApprovalTx(ctx, tx, event, p, registered.Definition); err != nil {
			return nil, err
		}
	}
	for _, predecessor := range predecessors {
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_contracts WHERE work_id=? AND contract_version=? AND superseded_by IS NULL`, event.SubjectID, predecessor).Scan(&active); err != nil {
			return nil, workflowProjectionError(err, "cannot inspect contract supersession predecessor")
		}
		if active != 1 {
			return nil, newFailure(KindInvariantViolation, "fold_event", "contract supersession predecessor is not active", false, "supply the exact active contract versions")
		}
	}
	return predecessors, nil
}

// requireWorkflowDuplicateRecoveryApprovalTx is the duplicate-recovery
// authorization branch beside the pin the fold resolved. Legacy pins admit
// only through the recorded operator approval binding; consequence_v1
// supersedes routinely — the unsigned attributed event keeps the exact
// predecessor set, audit evidence, and invalidation.
func requireWorkflowDuplicateRecoveryApprovalTx(ctx context.Context, tx *sql.Tx, event Event, p workflowContractSupersededPayload, definition WorkflowDefinition) error {
	if definition.ApprovalPolicy == WorkflowApprovalPolicyConsequenceV1 {
		return nil
	}
	return authorizeWorkflowOperatorApprovalTx(ctx, tx, event, workflowApprovalBinding{
		ApprovalRef:     p.ApprovalRef,
		OperationDigest: p.ApprovalOperationDigest,
		ScopeJSON:       p.ApprovalScopeJSON,
		VersionsJSON:    p.ApprovalVersionsJSON,
		Consequence:     p.ApprovalConsequence,
	}, contractRecoveryApprovalSubject)
}

func foldWorkflowContractSuperseded(ctx context.Context, tx *sql.Tx, event Event, scope *writeScope) error {
	var p workflowContractSupersededPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if err := beginWorkflowLifecycleTx(ctx, tx, event); err != nil {
		return err
	}
	predecessors, err := validateWorkflowContractSupersessionPredecessors(ctx, tx, scopeDefinitionRegistry(scope), event, p)
	if err != nil {
		return err
	}
	p.PredecessorContractVersions = predecessors
	if p.SuccessorContract != nil {
		if err := foldSuppliedWorkflowSuccessorTx(ctx, tx, event, scope, p); err != nil {
			return err
		}
	} else if err := cloneWorkflowSuccessorTx(ctx, tx, event, scope, p); err != nil {
		return err
	}
	return finishWorkflowContractSupersessionTx(ctx, tx, event, scope, p)
}

func foldSuppliedWorkflowSuccessorTx(ctx context.Context, tx *sql.Tx, event Event, scope *writeScope, p workflowContractSupersededPayload) error {
	if p.SuccessorContract.ContractVersion != p.NewContractVersion {
		return newFailure(KindInvalidPayload, "fold_event", "successor contract version does not match contract supersession", false, "supply the consecutive successor contract version")
	}
	registered, definitionErr := VerifyWorkflowInstanceDefinitionTx(ctx, tx, scopeDefinitionRegistry(scope), event.SubjectID)
	if definitionErr != nil {
		return definitionErr
	}
	productChanging := registered.Definition.ChangesProductTruth != nil && *registered.Definition.ChangesProductTruth
	if productChanging && p.SuccessorContract.ArchitectureBinding == nil {
		return newFailure(KindInvalidPayload, "fold_event", "true-definition supersession requires a fully supplied successor binding", false, "supply the complete successor architecture binding")
	}
	if productChanging {
		if err := validateArchitectureBindingTx(ctx, tx, event.SubjectID, registered.Definition, p.SuccessorContract.ArchitectureBinding, p.SuccessorContract.SpecMandate, p.SuccessorContract.LawModifies, p.SuccessorContract.LawRevisions); err != nil {
			return err
		}
	} else if len(p.SuccessorContract.LawModifies) != 0 || p.SuccessorContract.ArchitectureBinding != nil {
		// CD-0222 D5: a successor that
		// does not change Product truth may revise derived law in-contract.
		// A binding still carries Product authority and is refused here.
		if p.SuccessorContract.ArchitectureBinding != nil {
			return newFailure(KindInvalidPayload, "fold_event", "non-Product-changing successor carries Product authority", false, "leave Product-changing fields empty")
		}
		if err := validateDerivedLawModification(ctx, tx, event.SubjectID, p.SuccessorContract.LawModifies); err != nil {
			return err
		}
	}
	if !productChanging {
		if err := validateCurrentWorkflowLawRevisionsTx(ctx, tx, event.SubjectID, p.SuccessorContract.SpecMandate, p.SuccessorContract.LawModifies, p.SuccessorContract.LawRevisions); err != nil {
			return err
		}
	}
	p.SuccessorContract.WorkflowVersionFields = p.WorkflowVersionFields
	successorPayload, err := json.Marshal(p.SuccessorContract)
	if err != nil {
		return newFailure(KindInvalidPayload, "fold_event", "successor contract payload cannot be encoded", false, "supply a valid successor contract")
	}
	successorEvent := event
	successorEvent.EventID = event.EventID + ":successor"
	successorEvent.Kind = WorkflowContractApproved
	successorEvent.PayloadVersion = workflowContractLawChangePayloadVersion
	successorEvent.Payload = successorPayload
	if err := foldWorkflowContractApproved(workflowContractSupersessionContext(ctx), tx, successorEvent, scope); err != nil {
		return err
	}
	return nil
}

func cloneWorkflowSuccessorTx(ctx context.Context, tx *sql.Tx, event Event, scope *writeScope, p workflowContractSupersededPayload) error {
	registered, definitionErr := VerifyWorkflowInstanceDefinitionTx(ctx, tx, scopeDefinitionRegistry(scope), event.SubjectID)
	if definitionErr != nil {
		return definitionErr
	}
	if registered.Definition.ChangesProductTruth != nil && *registered.Definition.ChangesProductTruth {
		return newFailure(KindInvalidPayload, "fold_event", "Product-changing contract supersession cannot use the legacy clone route", false, "supply a fully bound successor contract")
	}
	// The clone route copies the predecessor row and supplies no successor
	// contract, so a consequence_v1 successor cannot declare its
	// decision-domain fact on this shape. The complete-successor route
	// declares it through the initial fold's policy gate.
	if workflowApprovalPolicyRequiresDecisionFact(registered.Definition) {
		return newFailure(KindInvalidPayload, "fold_event", "consequence_v1 contract supersession cannot use the legacy clone route", false, "supply a complete successor contract that declares its decision domains explicitly")
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if len(p.PredecessorContractVersions) != 1 {
		return newFailure(KindInvariantViolation, "fold_event", "duplicate active contracts require a fully supplied successor contract", false, "supply the typed successor contract")
	}
	if err := validateClonedWorkflowContractLawRevisionsTx(ctx, tx, event.SubjectID, p.PredecessorContractVersions); err != nil {
		return err
	}
	cloned, err := tx.ExecContext(ctx, `INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,rigor_class,law_modifies,law_boundary_version,definition_ref,definition_digest,decision_domains) SELECT work_id,?,premise,consequence_class,required_evidence,route_conventions,?,?,spec_mandate,rigor_class,law_modifies,law_boundary_version,definition_ref,definition_digest,decision_domains FROM workflow_contracts WHERE work_id=? AND contract_version=? AND NOT EXISTS (SELECT 1 FROM workflow_contracts WHERE work_id=? AND contract_version=?)`, p.NewContractVersion, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.Actor, event.SubjectID, p.PreviousContractVersion, event.SubjectID, p.NewContractVersion)
	if err != nil {
		return workflowProjectionError(err, "cannot create superseding workflow contract")
	}
	count, err := cloned.RowsAffected()
	if err != nil {
		return workflowProjectionError(err, "cannot inspect the cloned workflow contract")
	}
	if count > 0 {
		if err := recordRuntimeContractWriter(ctx, tx, event.SubjectID, p.NewContractVersion); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) SELECT work_id,?,predicate_id,ordinal,outcome_kind,outcome_payload FROM workflow_contract_predicates WHERE work_id=? AND contract_version=? AND NOT EXISTS (SELECT 1 FROM workflow_contract_predicates WHERE work_id=? AND contract_version=?)`, p.NewContractVersion, event.SubjectID, p.PreviousContractVersion, event.SubjectID, p.NewContractVersion); err != nil {
		return workflowProjectionError(err, "cannot clone workflow contract predicates")
	}
	return nil
}

func finishWorkflowContractSupersessionTx(ctx context.Context, tx *sql.Tx, event Event, scope *writeScope, p workflowContractSupersededPayload) error {
	for _, predecessor := range p.PredecessorContractVersions {
		result, err := tx.ExecContext(ctx, `UPDATE workflow_contracts SET superseded_by=? WHERE work_id=? AND contract_version=? AND superseded_by IS NULL`, p.NewContractVersion, event.SubjectID, predecessor)
		if err != nil {
			return workflowProjectionError(err, "cannot supersede workflow contract")
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return newFailure(KindProjectionNotFound, "fold_event", "predecessor workflow contract does not exist or is already superseded", false, "reload the exact active contract versions")
		}
	}
	if err := invalidateWorkflowOverlapForContractRevisionTx(ctx, tx, event, p.PredecessorContractVersions, p.NewContractVersion); err != nil {
		return err
	}
	// A contract supersession before the confirming Review is accepted voids an
	// unconfirmed checks repair (CD-0205 D4): the named revisions lose their
	// provisional readiness at the strict prefix of this supersession, and a
	// confirmed repair keeps its confirmation.
	if err := voidWorkerJobRepairsForSupersessionTx(ctx, tx, event.SubjectID, p.NewContractVersion, event.Seq-1); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_contracts SET supersession_seq=? WHERE work_id=? AND contract_version=?`, event.Seq, event.SubjectID, p.NewContractVersion); err != nil {
		return workflowProjectionError(err, "cannot record the workflow contract supersession boundary")
	}
	if err := refreshWorkflowHealthySequence(ctx, tx, event.SubjectID, p.NewContractVersion, true); err != nil {
		return err
	}
	if err := advanceWorkflowCorrectionFactsTx(ctx, tx, event); err != nil {
		return err
	}
	if p.SuccessorContract != nil {
		if err := foldCompleteStepContractCorrectionTxForRegistry(ctx, tx, scopeDefinitionRegistry(scope), event, p.SuccessorContract); err != nil {
			return err
		}
	}
	if p.SuccessorContract == nil {
		if err := resetWorkflowInstanceToContractStepTx(ctx, tx, scopeDefinitionRegistry(scope), event.SubjectID); err != nil {
			return err
		}
	}
	// The successor was folded before this update so the contract foreign
	// key remains valid; both writes commit or roll back together.
	if err := writeOracleFindingFactsTx(ctx, tx, event); err != nil {
		return err
	}
	if err := advanceWorkSizeProofFactsTx(ctx, tx, event); err != nil {
		return err
	}
	return appendWorkflowContractImpactNoticesTx(ctx, tx, event, p, *p.ResultingVersion, scope)
}

func foldCompleteStepContractCorrectionTxForRegistry(ctx context.Context, tx *sql.Tx, registry DefinitionRegistry, event Event, successor *workflowContractApprovedPayload) error {
	workID := event.SubjectID
	var foldStep, foldState string
	if err := tx.QueryRowContext(ctx, `SELECT current_step,instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&foldStep, &foldState); err != nil {
		return workflowProjectionError(err, "cannot read the workflow step for the supersession route check")
	}
	entry, err := VerifyWorkflowInstanceDefinitionTx(ctx, tx, registry, workID)
	if err != nil {
		return err
	}
	atCompleteStep := workflowCompleteStepCorrectionStep(entry.Definition, foldStep)
	declaresRoute := slices.Contains(successor.RouteConventions, workflowCompleteStepCorrectionRoute)
	if declaresRoute && !atCompleteStep {
		return newFailure(KindInvariantViolation, "fold_event", "route convention complete_step_correction does not match the supersession step", false, "declare the reserved convention exactly on the complete-step correction route")
	}
	if !atCompleteStep {
		// The fold return that reopens the work runs on no other shape.
		if workflowCompletedInstanceSupersedeOffShape(foldState, entry.Definition, foldStep) {
			return newFailure(KindInvariantViolation, "fold_event", "a completed workflow instance supersedes its contract only on the pinned complete-step correction shape", false, "start a successor workflow")
		}
		return nil
	}
	if !declaresRoute {
		return newFailure(KindInvariantViolation, "fold_event", "route convention complete_step_correction does not match the supersession step", false, "declare the reserved convention exactly on the complete-step correction route")
	}
	// Admission sees only the history that preceded this supersession.
	beforeSeq, err := eventSequenceTx(ctx, tx, event.EventID)
	if err != nil {
		return err
	}
	available, err := workflowCompleteStepCorrectionAvailable(ctx, tx, workID, entry.Definition, foldStep, "fold_event", beforeSeq)
	if err != nil {
		return err
	}
	if !available {
		return newFailure(KindInvariantViolation, "fold_event", "complete-step correction supersession does not satisfy its admission conditions", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	return returnWorkflowInstanceFromCompleteStepTx(ctx, tx, registry, workID)
}

type workflowContractDependent struct{ workID, edgeID, edgeClass string }

func readWorkflowContractDependentsTx(ctx context.Context, tx *sql.Tx, workID string) ([]workflowContractDependent, error) {
	rows, err := tx.QueryContext(ctx, `SELECT work_id,edge_id,edge_class FROM workflow_impact_edges WHERE target_work_id=? AND edge_kind='depends_on' AND edge_class IN ('hard','soft') ORDER BY work_id,edge_id`, workID)
	if err != nil {
		return nil, workflowProjectionError(err, "cannot inspect hard dependents")
	}
	var dependents []workflowContractDependent
	for rows.Next() {
		var item workflowContractDependent
		if err := rows.Scan(&item.workID, &item.edgeID, &item.edgeClass); err != nil {
			rows.Close()
			return nil, workflowProjectionError(err, "cannot read hard dependent")
		}
		dependents = append(dependents, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, workflowProjectionError(err, "cannot scan hard dependents")
	}
	rows.Close()
	selected := make(map[string]workflowContractDependent, len(dependents))
	for _, candidate := range dependents {
		current, exists := selected[candidate.workID]
		if !exists || candidate.edgeClass == "hard" && current.edgeClass != "hard" || candidate.edgeClass == current.edgeClass && candidate.edgeID < current.edgeID {
			selected[candidate.workID] = candidate
		}
	}
	owners := make([]string, 0, len(selected))
	for owner := range selected {
		owners = append(owners, owner)
	}
	sort.Strings(owners)
	var ordered []workflowContractDependent
	for _, owner := range owners {
		ordered = append(ordered, selected[owner])
	}
	return ordered, nil
}

func appendWorkflowContractImpactNoticesTx(ctx context.Context, tx *sql.Tx, event Event, payload workflowContractSupersededPayload, version int64, scope *writeScope) error {
	dependents, err := readWorkflowContractDependentsTx(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	for _, predecessor := range payload.PredecessorContractVersions {
		for _, item := range dependents {
			if err := appendWorkflowContractImpactNoticeTx(ctx, tx, event, payload, version, scope, predecessor, item); err != nil {
				return err
			}
			version++
		}
	}
	return nil
}

func appendWorkflowContractImpactNoticeTx(ctx context.Context, tx *sql.Tx, event Event, payload workflowContractSupersededPayload, version int64, scope *writeScope, predecessor int64, item workflowContractDependent) error {
	var consumed, active int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_contracts WHERE work_id=? AND contract_version=?`, item.workID, predecessor).Scan(&consumed); err != nil {
		return workflowProjectionError(err, "cannot inspect dependent contract consumption")
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_instances WHERE work_id=? AND instance_state NOT IN ('completed','cancelled','superseded')`, item.workID).Scan(&active); err != nil {
		return workflowProjectionError(err, "cannot inspect dependent lifecycle")
	}
	severity := "non-breaking"
	if item.edgeClass == "hard" && consumed != 0 && active != 0 {
		severity = "breaking"
	}
	entityRef := fmt.Sprintf("contract:%d", predecessor)
	noticeID := WorkflowNoticeID(event.SubjectID, payload.NewContractVersion, "workflow_contract", entityRef, item.workID, severity)
	noticePayload := map[string]any{"work_id": event.SubjectID, "expected_version": version, "resulting_version": version + 1, "notice_id": noticeID, "source_contract_version": payload.NewContractVersion, "entity_kind": "workflow_contract", "entity_ref": entityRef, "target_work_id": item.workID, "edge_owner_work_id": item.workID, "edge_id": item.edgeID, "old_hash": nil, "new_hash": nil, "severity": severity}
	raw, err := json.Marshal(noticePayload)
	if err != nil {
		return newFailure(KindInvalidPayload, "fold_event", "workflow impact notice payload cannot be encoded", false, "repair the workflow impact notice")
	}
	notice := Event{EventID: "notice-event:" + noticeID, Kind: WorkflowImpactNoticeRecorded, SubjectType: SubjectWorkItem, SubjectID: event.SubjectID, Actor: event.Actor, OccurredAt: event.OccurredAt, PayloadVersion: 2, Payload: raw}
	if err := notice.validate(); err != nil {
		return err
	}
	var typed workflowImpactNoticeRecordedPayload
	if err := decodeRegisteredPayload(notice, &typed); err != nil {
		return err
	}
	seq, err := appendPreparedEvent(ctx, tx, notice)
	if err != nil {
		return newFailure(KindOperationConflict, "fold_event", "dependent impact notice conflicted", false, "reconcile the workflow operation")
	}
	notice.Seq = seq
	scope.eventIDs = append(scope.eventIDs, notice.EventID)
	if err := foldWorkflowImpactNoticeRecorded(ctx, tx, notice); err != nil {
		return err
	}
	if err := advanceWorkEffectSeqTx(ctx, tx, notice); err != nil {
		return err
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
	if !workflowString(p.PrincipalRef, 128) || !workflowString(p.ClientRef, 128) || !workflowString(p.AgentRef, 128) || !workflowString(p.SessionRef, 128) || (p.ActorClass != "agent" && p.ActorClass != "operator") || p.ActorRef != DeriveWorkflowActorRef(p.PrincipalRef, p.ClientRef, p.AgentRef, p.SessionRef) {
		return newFailure(KindInvalidPayload, "fold_event", "actor tuple or actor_ref is invalid", false, "derive actor_ref from the complete canonical tuple")
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
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
	entry, err := VerifyWorkflowInstanceDefinitionTx(ctx, tx, scopeDefinitionRegistry(scope), event.SubjectID)
	if err != nil {
		return err
	}
	var currentStep string
	if err := tx.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, event.SubjectID).Scan(&currentStep); err != nil {
		return workflowProjectionError(err, "cannot read the current workflow step")
	}
	if p.ActorRef != event.Actor {
		return newFailure(KindUnauthorized, "fold_event", "action start actor must match the authenticated event actor", false, "start the workflow action through the authenticated workflow actor")
	}
	if p.StepID != currentStep {
		return newFailure(KindIllegalLifecycleTransition, "fold_event", "action start does not match the pinned current workflow step", false, "start the pinned current workflow step")
	}
	if !definitionStepAllows(entry.Definition, currentStep, p.ActionID) {
		return newFailure(KindIllegalLifecycleTransition, "fold_event", "action start is not declared on the pinned current workflow step", false, "start a declared action on the current workflow step")
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
	if err := startWorkflowInstanceStepTx(ctx, tx, scopeDefinitionRegistry(scope), event.SubjectID, p.StepID, p.ActorRef, p.ExecutionModel, event.OccurredAt); err != nil {
		return err
	}
	if err := recordWorkVersionActionTx(ctx, tx, event, p.ActionID, *p.ResultingVersion); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_step_epochs(work_id,step_id,attempt_epoch,latest_start_seq,latest_start_event_id,latest_start_payload) VALUES(?,?,?,?,?,?) ON CONFLICT(work_id,step_id) DO UPDATE SET attempt_epoch=excluded.attempt_epoch,latest_start_seq=excluded.latest_start_seq,latest_start_event_id=excluded.latest_start_event_id,latest_start_payload=excluded.latest_start_payload`, event.SubjectID, p.StepID, p.AttemptEpoch, event.Seq, event.EventID, string(event.Payload)); err != nil {
		return workflowProjectionError(err, "cannot record the current step epoch")
	}
	if p.ActionID == "dispatch_worker" {
		if _, err := tx.ExecContext(ctx, `UPDATE workflow_step_epochs SET latest_dispatch_start_seq=?,latest_dispatch_start_event_id=?,latest_dispatch_start_payload=? WHERE work_id=? AND step_id=?`, event.Seq, event.EventID, string(event.Payload), event.SubjectID, p.StepID); err != nil {
			return workflowProjectionError(err, "cannot record the current dispatch start")
		}
	} else if _, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET latest_non_dispatch_start_seq=? WHERE work_id=?`, event.Seq, event.SubjectID); err != nil {
		return workflowProjectionError(err, "cannot record the worker result interval boundary")
	}
	if err := advanceWorkflowActionObligationsTx(ctx, tx, event, p.StepID, p.AttemptEpoch, p.ActorRef); err != nil {
		return err
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
	if err := requireWorkLifecycleAdmissionTx(ctx, tx, event, "in_progress", nil); err != nil {
		return err
	}
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
	if p.StepKind != "internal_sqlite" && p.StepKind != "cross_authority" && p.StepKind != "external_effect" && p.StepKind != "human_checkpoint" {
		return newFailure(KindInvalidPayload, "fold_event", "checkpoint step kind is not closed", false, "use a closed workflow step kind")
	}
	entry, err := VerifyWorkflowInstanceDefinitionTx(ctx, tx, scopeDefinitionRegistry(scope), event.SubjectID)
	if err != nil {
		return err
	}
	var currentStep, executionActor string
	if err := tx.QueryRowContext(ctx, `SELECT current_step,COALESCE(execution_actor_ref,'') FROM workflow_instances WHERE work_id=?`, event.SubjectID).Scan(&currentStep, &executionActor); err != nil {
		return workflowProjectionError(err, "cannot read the current workflow checkpoint authority")
	}
	step := workflowStep(entry.Definition, currentStep)
	if p.ActorRef != event.Actor {
		return newFailure(KindUnauthorized, "fold_event", "checkpoint actor must match the authenticated event actor", false, "checkpoint through the authenticated workflow actor")
	}
	if p.StepID != currentStep || step == nil || p.StepKind != string(step.Kind) {
		return newFailure(KindIllegalLifecycleTransition, "fold_event", "checkpoint does not match the pinned current workflow step kind", false, "checkpoint the pinned current workflow step and kind")
	}
	expectedEpoch, err := workflowCheckpointAttemptEpoch(ctx, tx, entry.Definition, step, event.SubjectID, currentStep)
	if err != nil {
		return err
	}
	if p.AttemptEpoch != expectedEpoch {
		return newFailure(KindIllegalLifecycleTransition, "fold_event", "checkpoint does not match the latest workflow action start epoch", false, "checkpoint the current workflow action attempt")
	}
	if err := admitWorkflowCheckpointWriterTx(ctx, tx, event, p, entry.Definition, step, currentStep, executionActor); err != nil {
		return err
	}
	return persistWorkflowCheckpointTx(ctx, tx, event, scope, p)
}

func admitWorkflowCheckpointWriterTx(ctx context.Context, tx *sql.Tx, event Event, p workflowActionCheckpointedPayload, definition WorkflowDefinition, step *WorkflowStep, currentStep, executionActor string) error {
	fenced := step != nil && workflowStepFences(definition, *step)
	var admitted bool
	var err error
	if !fenced {
		admitted, err = workflowCheckpointWriterAdmitted(ctx, tx, event.SubjectID, executionActor, p.ActorRef, false, event.Seq)
		if err != nil {
			return err
		}
	} else {
		checkpointFields, checkpointFieldsErr := workflowActionObject(p.CheckpointPayload)
		if checkpointFieldsErr != nil {
			return checkpointFieldsErr
		}
		deriving, deriveErr := workflowDerivedStartPairAuthored(ctx, tx, definition, event.SubjectID, currentStep, workflowFieldStringDefault(checkpointFields, "action_id", ""), p.AttemptEpoch, event.Seq, p.ActorRef, event.EventID)
		if deriveErr != nil {
			return deriveErr
		}
		if deriving {
			// The checkpoint that its own derived start opened (CD-0198 D3):
			// the started fold already authenticated this actor and bound
			// the step fence from it, so the checkpoint-writer rule that
			// governs review and context events does not judge the
			// derivation pair. Every later checkpoint at the step answers
			// to the rule with the fence in place.
			admitted = true
		} else {
			admitted, err = workflowCheckpointWriterAdmitted(ctx, tx, event.SubjectID, executionActor, p.ActorRef, true, event.Seq)
			if err != nil {
				return err
			}
		}
	}
	if !admitted {
		return newFailure(KindUnauthorized, "fold_event", "checkpoint actor does not hold the workflow writing authority", false, "checkpoint through the workflow actor that holds the writing authority")
	}
	return nil
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
	if err := foldWorkflowDecisionRecordForRegistry(ctx, tx, scopeDefinitionRegistry(scope), event, p.CheckpointPayload); err != nil {
		return err
	}
	if err := writeWorkflowCoordinatorActionTx(ctx, tx, event); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE workflow_instances SET instance_state='running',last_checkpoint_at=? WHERE work_id=?`, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.SubjectID)
	return err
}

func foldWorkflowDecisionRecordForRegistry(ctx context.Context, tx *sql.Tx, registry DefinitionRegistry, event Event, raw json.RawMessage) error {
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
	declaredBounds := false
	if checkpoint.Fields != nil {
		var err error
		declaredBounds, err = validateWorkflowDecisionDeclaredBoundsTx(ctx, tx, registry, event.SubjectID, raw)
		if err != nil {
			return err
		}
	}
	// Typed records use their pinned payload contract for preflight and folding.
	if !declaredBounds && (!workflowString(checkpoint.Question, 4096) || !workflowList(checkpoint.Options, 16, 1) || (checkpoint.Decision != "accepted_decision" && checkpoint.Decision != "insufficient_evidence") || !workflowString(checkpoint.Rationale, 4096) || !workflowList(checkpoint.Consequences, 16, 1) || !workflowList(checkpoint.Inputs, 32, 1) || !workflowString(checkpoint.POCFindings, 4096)) {
		return newFailure(KindInvalidPayload, "fold_event", "record_decision checkpoint is structurally invalid", false, "supply the complete typed decision record")
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_decision_records(work_id,question,options_considered,decision,rationale,consequences,inputs,poc_findings,supersedes,superseded_by,recorded_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, event.SubjectID, checkpoint.Question, workflowJSON(checkpoint.Options), checkpoint.Decision, checkpoint.Rationale, workflowJSON(checkpoint.Consequences), workflowJSON(checkpoint.Inputs), checkpoint.POCFindings, valueOrNil(checkpoint.Supersedes), valueOrNil(checkpoint.SupersededBy), event.OccurredAt.UTC().Format(time.RFC3339Nano))
	return workflowProjectionError(err, "cannot record workflow decision")
}

func validateWorkflowDecisionDeclaredBoundsTx(ctx context.Context, tx *sql.Tx, registry DefinitionRegistry, workID string, raw json.RawMessage) (bool, error) {
	entry, err := VerifyWorkflowInstanceDefinitionTx(ctx, tx, registry, workID)
	if err != nil {
		return false, err
	}
	declaredBounds := false
	for _, action := range entry.Definition.ActionDefinitions {
		if action.ID != "record_decision" {
			continue
		}
		for _, field := range action.Payload.Fields {
			if field.Name == "options_considered" {
				declaredBounds = true
			}
		}
	}
	if declaredBounds {
		var envelope struct {
			Fields json.RawMessage `json:"fields"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return false, err
		}
		if err := validateWorkflowActionPayload(entry.Definition, "record_decision", envelope.Fields); err != nil {
			return false, err
		}
	}
	return declaredBounds, nil
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
		!workflowDigest(p.WorkflowDefinitionDigest) || !workflowString(p.ActorRef, 70) || !workflowString(p.RequestID, 128) {
		return newFailure(KindInvalidPayload, "fold_event", "context checkpoint is incomplete or outside its bounds", false, "supply all durable working-state fields within the context continuity bounds")
	}
	if err := requireActor(ctx, tx, p.ActorRef); err != nil {
		return err
	}
	var currentStep, workflowRef, workflowDigestValue string
	if err := tx.QueryRowContext(ctx, `SELECT current_step,definition_ref,definition_digest FROM workflow_instances WHERE work_id=?`, event.SubjectID).Scan(&currentStep, &workflowRef, &workflowDigestValue); err != nil {
		return workflowProjectionError(err, "cannot read workflow identity for context checkpoint")
	}
	if currentStep != p.StepID || workflowRef != p.WorkflowRef || workflowDigestValue != p.WorkflowDefinitionDigest {
		return newFailure(KindStaleAttempt, "fold_event", "context checkpoint does not bind the current workflow step and definition", false, "reread the current workflow context")
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
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_context_checkpoints(work_id,work_version,checkpoint_sequence,checkpoint_id,step_id,attempt_epoch,active_unit,hypothesis,diagnosis,strategy,touched_refs,evidence_refs,pending_questions,pending_decisions,workflow_ref,workflow_definition_digest,actor_ref,request_id,recorded_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, event.SubjectID, *p.ResultingVersion, sequence, p.CheckpointID, p.StepID, p.AttemptEpoch, p.ActiveUnit, p.Hypothesis, p.Diagnosis, p.Strategy, workflowJSON(p.TouchedRefs), workflowJSON(p.EvidenceRefs), workflowJSON(p.PendingQuestions), workflowJSON(p.PendingDecisions), p.WorkflowRef, p.WorkflowDefinitionDigest, p.ActorRef, p.RequestID, event.OccurredAt.UTC().Format(time.RFC3339Nano))
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
	if p.BoundaryKind != "summary" || !workflowString(p.BoundaryID, 128) || !workflowString(p.CheckpointID, 128) || !workflowString(p.Summary, 16*1024) || !workflowString(p.WorkflowRef, 128) || !workflowDigest(p.WorkflowDefinitionDigest) || p.AttemptEpoch <= 0 || !workflowString(p.ActorRef, 70) || !workflowString(p.RequestID, 128) {
		return newFailure(KindInvalidPayload, "fold_event", "context boundary is not a bounded summary boundary", false, "cross only a summary boundary after a durable checkpoint")
	}
	if err := requireActor(ctx, tx, p.ActorRef); err != nil {
		return err
	}
	var workflowRef, workflowDigestValue string
	if err := tx.QueryRowContext(ctx, `SELECT definition_ref,definition_digest FROM workflow_instances WHERE work_id=?`, event.SubjectID).Scan(&workflowRef, &workflowDigestValue); err != nil {
		return workflowProjectionError(err, "cannot read workflow identity for context boundary")
	}
	if workflowRef != p.WorkflowRef || workflowDigestValue != p.WorkflowDefinitionDigest {
		return newFailure(KindStaleAttempt, "fold_event", "context boundary does not bind the current workflow definition", false, "reread the current workflow context")
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
	var currentStep string
	if err := tx.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, event.SubjectID).Scan(&currentStep); err != nil {
		return workflowProjectionError(err, "cannot read current workflow step for context boundary")
	}
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
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM durable_operations WHERE work_id=? AND (result_kind IS NULL OR result_kind IN ('pending','partial'))`, event.SubjectID).Scan(&active); err != nil {
		return workflowProjectionError(err, "cannot inspect active workflow attempts")
	}
	if active != 0 {
		return newFailure(KindInvalidOperation, "fold_event", "summary boundary is forbidden during an active effect or attempt", false, "complete or reconcile the active operation first")
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
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_context_boundaries(work_id,work_version,boundary_sequence,boundary_count,boundary_id,boundary_kind,checkpoint_id,checkpoint_sequence,attempt_epoch,summary,workflow_ref,workflow_definition_digest,actor_ref,request_id,recorded_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, event.SubjectID, *p.ResultingVersion, sequence, sequence, p.BoundaryID, p.BoundaryKind, p.CheckpointID, p.CheckpointSequence, p.AttemptEpoch, p.Summary, p.WorkflowRef, p.WorkflowDefinitionDigest, p.ActorRef, p.RequestID, event.OccurredAt.UTC().Format(time.RFC3339Nano))
	return workflowProjectionError(err, "cannot record context boundary")
}

// validateWorkflowActionCompletedShape bounds the payload before any read.
// worker_attempt_id belongs to the worker result actions and to dispatch_worker
// alone, and a rejected result or a correction request carries its full
// correction record or none.
func validateWorkflowActionCompletedShape(p workflowActionCompletedPayload) error {
	if err := validateWorkflowCompletionDispatchMetadata(p); err != nil {
		return err
	}
	if err := validateWorkflowCompletionResultFields(p); err != nil {
		return err
	}
	if err := validateWorkflowCompletionJobBinding(p); err != nil {
		return err
	}
	if err := validateWorkflowCompletionLaneIdentity(p); err != nil {
		return err
	}
	if (p.ActionID == "reject_worker_result" || p.ActionID == "request_correction") && (!workflowString(p.CorrectionDiagnosis, 4096) || !workflowString(p.CorrectionStrategy, 4096) || !workflowList(p.CorrectionPredicateIDs, 8, 1) || !workflowList(p.CorrectionEvidenceRefs, 32, 1)) {
		return newFailure(KindInvalidPayload, "fold_event", "rejected worker result has incomplete correction fields", false, "supply diagnosis, strategy, predicate IDs, and evidence references")
	}
	// the typed failure attribution is the coordinator correction
	// completion's own observation surface, and the attribution reference is
	// the derived failure record's alone: the terminal worker.failed event
	// owns the observation and the derived record refers to it by event id,
	// never a copied block.
	if p.FailureAttribution != nil && !workflowFailureAttributionCorrectionActions[p.ActionID] {
		return newFailure(KindInvalidPayload, "fold_event", "action_completed failure_attribution is reserved for reject_worker_result and request_correction; the derived failure record refers to the terminal attribution by event id", false, "record the coordinator attribution on the correction completion that observed it")
	}
	if p.FailureAttributionRef != "" && p.ActionID != "record_worker_failure" {
		return newFailure(KindInvalidPayload, "fold_event", "action_completed failure_attribution_ref is reserved for the derived worker failure record", false, "record the terminal attribution reference on the record_worker_failure completion the derivation authored")
	}
	// The fold is the last line before a reject commits. The correction pin
	// serializes predicate ids with the generated id rules and evidence refs
	// with the generated reference rules, so a reject carrying a value
	// outside either type would fail its own closed response schema after
	// the effect, which the caller reads as a malformed response.
	if p.ActionID == "reject_worker_result" {
		if fault := workflowCorrectionSchemaValuesFaultForAdmission(p.CorrectionPredicateIDs, p.CorrectionEvidenceRefs, p.ResultEvidenceRefs); fault != "" {
			return newFailure(KindInvalidPayload, "fold_event", "rejected worker result carries correction values the closed response schema cannot represent: "+fault, false, "supply correction values the closed work_pin response schema accepts")
		}
	}
	return nil
}

func validateWorkflowCompletionDispatchMetadata(p workflowActionCompletedPayload) error {
	if err := validateWorkerOraclePreparationBindings(p); err != nil {
		return err
	}
	if p.WorkerSubjectCommit != "" && (p.ActionID != "dispatch_worker" || !worktreeSHAPattern.MatchString(p.WorkerSubjectCommit)) {
		return newFailure(KindInvalidPayload, "fold_event", "worker_subject_commit is reserved for dispatch_worker and must be one raw commit OID", false, "record the admitted dispatch subject only")
	}
	// The frontier attribution is dispatch authority: only a
	// dispatch_worker completion records it, and a frontier is never
	// negative.
	if p.WorkerContextFrontier != nil && (p.ActionID != "dispatch_worker" || *p.WorkerContextFrontier < 0) {
		return newFailure(KindInvalidPayload, "fold_event", "worker_context_frontier is reserved for dispatch_worker and must be a nonnegative source-event frontier", false, "record the admitted view frontier only")
	}
	if p.CorrectionOpenFindingIDs != nil && ((p.ActionID != "reject_worker_result" && p.ActionID != "request_correction") || !validWorkflowOpenFindings(p.CorrectionOpenFindingIDs)) {
		return newFailure(KindInvalidPayload, "fold_event", "correction_open_finding_ids requires a rejected result or a correction request with 1 to 32 unique finding IDs", false, "record the complete open finding set on reject_worker_result or request_correction")
	}
	if p.RetryConvergence != nil && (p.ActionID != "dispatch_worker" || !p.RetryConvergence.valid()) {
		return newFailure(KindInvalidPayload, "fold_event", "retry_convergence requires a dispatch and a complete convergence basis", false, "record the convergence basis derived by the store")
	}
	if p.ChecksRepairReview != nil {
		if p.ActionID != "dispatch_worker" {
			return newFailure(KindInvalidPayload, "fold_event", "checks_repair_review is reserved for dispatch_worker: the completion that records the confirming review admission", false, "record the confirming-review admission on its dispatch completion only")
		}
		if p.ChecksRepairReview.WorkerJob.JobID == "" || p.ChecksRepairReview.WorkerJob.Revision < 1 || p.ChecksRepairReview.WorkerJob.Digest == "" || p.ChecksRepairReview.RepairingAcceptanceSeq < 1 {
			return newFailure(KindInvalidPayload, "fold_event", "checks_repair_review must bind the exact repairing revision and its repairing acceptance", false, "record the confirming-review admission the store derives")
		}
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
	if (p.ActionID != "" && !workflowString(p.ActionID, 128)) || !workflowString(p.StepID, 128) || p.AttemptEpoch <= 0 || p.AttemptEpoch > 2147483647 || (p.WorkerAttemptID != "" && !workflowString(p.WorkerAttemptID, 128)) || !workflowList(p.ChangedRefs, 32, 0) {
		return newFailure(KindInvalidPayload, "fold_event", "action_completed has invalid result fields", false, "supply bounded action result references")
	}
	if p.ActionID != "accept_worker_result" && p.ActionID != "accept_worker_evidence" && p.ActionID != "record_worker_failure" && p.ActionID != "reject_worker_result" && p.ActionID != "dispatch_worker" && p.ActionID != "record_worker_job" && p.WorkerAttemptID != "" {
		return newFailure(KindInvalidPayload, "fold_event", "worker_attempt_id is reserved for worker result actions and dispatch_worker", false, "omit worker_attempt_id for ordinary action completion")
	}
	return nil
}

func validateWorkflowCompletionJobBinding(p workflowActionCompletedPayload) error {
	// The worker-job binding is recorded authority (CD-0205): the
	// dispatch_worker completion records the revision the packet binds, and
	// the accept_worker_result completion records the disposition it
	// satisfies. Both must be whole — a job_id, a positive revision, and a digest — because
	// the correction window reads it as the durable fact that one recorded
	// job was satisfied.
	if p.WorkerJob != nil {
		if p.ActionID != "accept_worker_result" && p.ActionID != "dispatch_worker" || !workflowString(p.WorkerJob.JobID, 128) || p.WorkerJob.Revision < 1 || !workflowString(p.WorkerJob.Digest, 128) {
			return newFailure(KindInvalidPayload, "fold_event", "action_completed worker_job is reserved for dispatch_worker and accept_worker_result and requires job_id, revision, and digest", false, "record the disposition derived from the accepted attempt's dispatch binding")
		}
	}
	if p.WorkerCallerExclusions != nil {
		if p.ActionID != "dispatch_worker" || p.WorkerJob == nil || len(p.WorkerCallerExclusions) > WorkerJobCallerExclusionsMax {
			return newFailure(KindInvalidPayload, "fold_event", fmt.Sprintf("action_completed worker_caller_exclusions is reserved for a job-bound dispatch_worker and carries at most %d paths", WorkerJobCallerExclusionsMax), false, "record the caller exclusions the dispatch admission computed")
		}
		for _, path := range p.WorkerCallerExclusions {
			if problem := normalizedRelativeRepoPathProblem("action_completed worker_caller_exclusions", path); problem != "" {
				return newFailure(KindInvalidPayload, "fold_event", problem, false, "record the caller exclusions the dispatch admission computed")
			}
		}
	}
	return nil
}

func validateWorkflowCompletionLaneIdentity(p workflowActionCompletedPayload) error {
	// The lane identity is the dispatch_worker completion's in-flight
	// binding input, so it is reserved for that action and must arrive all
	// together: a partial identity would bind an attempt the fold cannot
	// read back.
	laneFields := []bool{p.WorkerLaneID != "", p.WorkerLaneDigest != "", p.WorkerCapabilityClass != ""}
	laneBound, laneUnbound := 0, 0
	for _, set := range laneFields {
		if set {
			laneBound++
		} else {
			laneUnbound++
		}
	}
	if laneBound > 0 && laneUnbound > 0 {
		return newFailure(KindInvalidPayload, "fold_event", "dispatch_worker lane identity is partially bound", false, "record the complete lane identity on the dispatch_worker completion")
	}
	if laneBound > 0 {
		if p.ActionID != "dispatch_worker" {
			return newFailure(KindInvalidPayload, "fold_event", "worker lane identity is reserved for dispatch_worker completions", false, "omit the lane identity for ordinary action completion")
		}
		if !workflowString(p.WorkerLaneID, 32) || len(p.WorkerLaneDigest) != 71 || !strings.HasPrefix(p.WorkerLaneDigest, "sha256:") || !workflowString(p.WorkerCapabilityClass, 64) {
			return newFailure(KindInvalidPayload, "fold_event", "dispatch_worker lane identity is out of bounds", false, "record the registry lane identity the packet resolved")
		}
	}
	return nil
}

// workflowCorrectionID reports whether one correction predicate id fits the
// generated id schema the correction pin serializes.
func workflowCorrectionID(value string) bool {
	raw, err := json.Marshal(value)
	return err == nil && payloadschema.Validate("id", raw) == nil
}

func workflowCorrectionSchemaValuesFaultForAdmission(predicateIDs, correctionRefs, resultRefs []string) string {
	for _, id := range predicateIDs {
		if !workflowCorrectionID(id) {
			return "correction_predicate_ids entry " + workflowRefExcerpt(id)
		}
	}
	for _, ref := range correctionRefs {
		if !ValidReference(ref) {
			return "correction_evidence_refs entry " + workflowRefExcerpt(ref)
		}
	}
	for _, ref := range resultRefs {
		if !ValidReference(ref) {
			return "result_evidence_refs entry " + workflowRefExcerpt(ref)
		}
	}
	return ""
}

// admitWorkflowActionOffStep decides whether an action the pinned step does
// not declare may still fold under an engine-owned recovery route.
func admitWorkflowActionOffStep(ctx context.Context, tx *sql.Tx, event Event, p workflowActionCompletedPayload, entry RegisteredDefinition, currentStep string, registry DefinitionRegistry) error {
	if p.ActionID == "record_verdict" && workflowLateCriterionAssociationStep(entry.Definition, currentStep) {
		// Association events fold before their completion. Availability has
		// therefore closed for the last criterion already; join this action's
		// own verified effect rather than re-test the post-association state.
		var paired bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_predicate_verdicts WHERE work_id=? AND verdict_seq<? AND actor=? AND json_extract(payload,'$.resulting_version')=?)`, event.SubjectID, event.Seq, p.ActorRef, p.ExpectedVersion).Scan(&paired); err != nil {
			return workflowProjectionError(err, "cannot read late criterion association effect")
		}
		if paired {
			return nil
		}
		return newFailure(KindMissingEvidence, "fold_event", "late criterion association requires its own verified association effect", false, "associate applicable producer receipts before recording completion")
	}
	if p.ActionID == "accept_worker_evidence" {
		return validateRefineReviewEvidenceAttempt(ctx, tx, event.SubjectID, entry.Definition, currentStep, p.WorkerAttemptID, p.AttemptEpoch, "fold_event", event.Seq)
	}
	workerFailureRecovery := false
	correctionRecovery := false
	if p.ActionID == "record_worker_failure" {
		var recoveryErr error
		workerFailureRecovery, recoveryErr = workflowWorkerFailureRecoveryMayFold(ctx, tx, event.SubjectID, entry.Definition, currentStep, "fold_event")
		if recoveryErr != nil {
			return recoveryErr
		}
		if !workerFailureRecovery {
			// CD-0198 D5: the terminal event's own derivation pair folds
			// without the recovery route — the record derives from the
			// attempt's terminal event, not from a coordinator call.
			workerFailureRecovery, recoveryErr = workflowDerivedWorkerFailurePairAdmitsFold(ctx, tx, entry.Definition, event, p)
			if recoveryErr != nil {
				return recoveryErr
			}
		}
	}
	if p.ActionID == "reject_worker_result" && stepDeclaresAction(entry.Definition, currentStep, "dispatch_worker") {
		if _, recoveryErr := validateWorkerResultRejection(ctx, tx, event.SubjectID, entry.Definition, currentStep, "fold_event", p.WorkerAttemptID, p.AttemptEpoch, event.Seq); recoveryErr != nil {
			return recoveryErr
		}
		correctionRecovery = true
	}
	if p.ActionID == "request_correction" {
		var recoveryErr error
		var missing string
		correctionRecovery, missing, recoveryErr = workflowCorrectionRequestAdmissionStateForRegistry(ctx, tx, event.SubjectID, entry.Definition, currentStep, "fold_event", event.Seq, registry)
		if recoveryErr != nil {
			return recoveryErr
		}
		if !correctionRecovery {
			return workflowCorrectionRequestUnavailableFailure("fold_event", missing)
		}
		return nil
	}
	if !workerFailureRecovery && !correctionRecovery && p.ActionID != "request_correction" && (p.ActionID != "bind_evidence" || stepDeclaresAction(entry.Definition, currentStep, "bind_evidence")) {
		return newFailure(KindIllegalLifecycleTransition, "fold_event", "completed action is not declared on the pinned current step", false, "reread_entities")
	}
	if !workerFailureRecovery && !correctionRecovery {
		return admitWorkflowRecoveryEvidenceCompletionTx(ctx, tx, event, p, entry.Definition, currentStep)
	}
	return nil
}

func admitWorkflowRecoveryEvidenceCompletionTx(ctx context.Context, tx *sql.Tx, event Event, p workflowActionCompletedPayload, definition WorkflowDefinition, currentStep string) error {
	var raw []byte
	var bindingSeq int64
	// The evidence constructor emits its binding immediately before the
	// completion. Join that effect, not a tuple from an earlier action.
	err := tx.QueryRowContext(ctx, `SELECT binding_seq,payload FROM workflow_evidence_bindings WHERE work_id=? AND binding_seq<? AND actor=? AND json_extract(payload,'$.resulting_version')=? ORDER BY binding_seq DESC LIMIT 1`, event.SubjectID, event.Seq, p.ActorRef, p.ExpectedVersion).Scan(&bindingSeq, &raw)
	if err != nil && err != sql.ErrNoRows {
		return wrapFailure(KindUnavailable, "fold_event", "cannot inspect recovery evidence binding", true, "retry once the workflow evidence projection is readable", err)
	}
	if err == sql.ErrNoRows {
		return newFailure(KindMissingEvidence, "fold_event", "recovery evidence binding requires its own bound subject", false, "bind eligible recovery evidence before recording the action completion")
	}
	var binding workflowEvidenceBoundPayload
	if err := json.Unmarshal(raw, &binding); err != nil {
		return newFailure(KindInvariantViolation, "fold_event", "recovery evidence binding payload is malformed", false, "reread_entities")
	}
	// The binding has already folded. Evaluate the shared eligibility at
	// its input boundary, not against the requirement it just satisfied.
	recovery, err := workflowEvidenceBindingRecoveryEligibility(ctx, tx, definition, event.SubjectID, currentStep, bindingSeq)
	if err != nil {
		return err
	}
	if recovery.Refusal != nil {
		return recovery.Refusal
	}
	if slices.Contains(p.ResultEvidenceRefs, binding.ImmutableSubjectRef) {
		for _, requirement := range recovery.Requirements {
			if (requirement.Kind == "" || requirement.Kind == binding.EvidenceKind) && (requirement.Reference == "" || requirement.Reference == binding.ImmutableSubjectRef) {
				return nil
			}
		}
	}
	return newFailure(KindMissingEvidence, "fold_event", "recovery evidence binding does not bind eligible recovery evidence", false, "bind an outstanding requirement or verification of an unverdicted active check")
}

// workflowCompletedActionAdvances reports whether a recorded action completion advances the pinned step.
func workflowCompletedActionAdvances(definition WorkflowDefinition, p workflowActionCompletedPayload) bool {
	mode, ok := workflowActionExecutionMode(definition, p.ActionID)
	return ok && mode == ActionAdvance && (p.ActionID != "accept_worker_result" || (!p.ReviewAdvanceHeld && !(p.WorkerJob != nil && p.DeliveryState == "")))
}

func foldWorkflowWorkerAttemptCompletionTx(ctx context.Context, tx *sql.Tx, event Event, p workflowActionCompletedPayload, definition WorkflowDefinition, currentStep string) error {
	lifecycle := "completed"
	switch p.ActionID {
	case "accept_worker_result", "accept_worker_evidence", "reject_worker_result":
	case "record_worker_job":
		if p.WorkerAttemptID == "" {
			return nil
		}
	case "record_worker_failure":
		lifecycle = "failed"
	default:
		return nil
	}
	if err := validateWorkerAttemptAction(ctx, tx, event, p, definition, currentStep, lifecycle); err != nil {
		return err
	}
	switch p.ActionID {
	case "reject_worker_result":
		// A rejected confirming review voids provisional repair readiness
		// at the strict prefix of this rejection (CD-0205 D4).
		return voidWorkerJobRepairByAttemptTx(ctx, tx, event.SubjectID, p.WorkerAttemptID, event.Seq-1)
	case "record_worker_failure":
		var recorded int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT failure_recorded FROM worker_attempts WHERE work_id=? AND attempt_id=? AND (failure_recorded_seq=0 OR failure_recorded_seq<?)),0)`, event.SubjectID, p.WorkerAttemptID, event.Seq).Scan(&recorded); err != nil {
			return workflowProjectionError(err, "cannot inspect recorded worker failures")
		}
		if recorded != 0 {
			return newFailure(KindIllegalLifecycleTransition, "fold_event", "worker failure is already recorded", false, "start a fresh workflow attempt")
		}
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
	// SupplementalClassifications and WorkerProductionSize are retired
	// measurement members (CD-0242 D6).
	if p.SupplementalClassifications != nil || p.WorkerProductionSize != nil {
		return newFailure(KindInvalidPayload, "fold_event", "action_completed supplemental_classifications and worker_production_size are retired by CD-0242 D6: no production-line cap, size measurement, or mandatory split applies", false, "accept without measurement content")
	}
	if err := verifyWorkflowActionCompletedAttributionMembersTx(ctx, tx, event, p); err != nil {
		return err
	}
	if err := markAcceptedWorkerJobTx(ctx, tx, event, p); err != nil {
		return err
	}
	if err := requireActor(ctx, tx, p.ActorRef); err != nil {
		return err
	}
	var entry RegisteredDefinition
	var currentStep string
	entry, err := VerifyWorkflowInstanceDefinitionTx(ctx, tx, scopeDefinitionRegistry(scope), event.SubjectID)
	if err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, event.SubjectID).Scan(&currentStep); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot read the current workflow step", true, "retry once the workflow instance is readable", err)
	}
	if p.ActorRef != event.Actor {
		return newFailure(KindUnauthorized, "fold_event", "completed action actor must match the authenticated event actor", false, "complete the workflow action through the authenticated workflow actor")
	}
	if p.ActionID == "" || p.StepID != currentStep {
		return newFailure(KindIllegalLifecycleTransition, "fold_event", "completed action is not declared on the pinned current step", false, "reread_entities")
	}
	if !definitionStepAllows(entry.Definition, currentStep, p.ActionID) {
		if err := admitWorkflowActionOffStep(ctx, tx, event, p, entry, currentStep, scopeDefinitionRegistry(scope)); err != nil {
			return err
		}
	}
	return completeAdmittedWorkflowActionTx(ctx, tx, event, scope, p, entry, currentStep)
}

func completeAdmittedWorkflowActionTx(ctx context.Context, tx *sql.Tx, event Event, scope *writeScope, p workflowActionCompletedPayload, entry RegisteredDefinition, currentStep string) error {
	advancesStep := false
	if p.ActionID != "" {
		if _, ok := workflowActionExecutionMode(entry.Definition, p.ActionID); !ok {
			return newFailure(KindInvariantViolation, "fold_event", "workflow action execution mode is not declared", false, "repair the pinned workflow definition")
		}
		// A job-bound acceptance without a delivery assertion is local
		// acceptance (CD-0205): it records the disposition of its one worker
		// job and holds the parent step. Whole-work delivery stays with
		// record_delivery and the delivery-asserting accept. The fold reads
		// the recorded worker_job and delivery fields only.
		advancesStep = workflowCompletedActionAdvances(entry.Definition, p)
		if err := admitCompletedWorkflowStepAdvanceTx(ctx, tx, event, p, entry.Definition, currentStep, advancesStep); err != nil {
			return err
		}
		if err := completeWorkflowWorkerActionTx(ctx, tx, event, scope, p, entry, currentStep, advancesStep); err != nil {
			return err
		}
	}
	nextStep := ""
	if advancesStep {
		nextStep = workflowNextStep(entry.Definition, currentStep)
		if p.ActionID == "confirm_premise" {
			var nextErr error
			nextStep, nextErr = workflowNextStepForAction(ctx, tx, event.SubjectID, entry.Definition, currentStep, p.ActionID, scopeDefinitionRegistry(scope), event.Seq)
			if nextErr != nil {
				return nextErr
			}
		}
	} else if p.ActionID == "request_correction" {
		var err error
		nextStep, err = workflowFoldCorrectionReturnTarget(entry, currentStep)
		if err != nil {
			return err
		}
	}
	return persistWorkflowActionCompletionTx(ctx, tx, event, p, entry, advancesStep, nextStep)
}

func admitCompletedWorkflowStepAdvanceTx(ctx context.Context, tx *sql.Tx, event Event, p workflowActionCompletedPayload, definition WorkflowDefinition, currentStep string, advancesStep bool) error {
	if p.ActionID == "accept_worker_result" && advancesStep && workflowStepCarriesRejectionHold(definition, currentStep) {
		rejectionHeld, rejectionHoldErr := workflowUnconsumedUnboundRejection(ctx, tx, event.SubjectID, currentStep, "fold_event", event.Seq)
		if rejectionHoldErr != nil {
			return rejectionHoldErr
		}
		if failure := workflowRejectionAdvanceFailure(rejectionHeld, p.ActionID, advancesStep); failure != nil {
			return failure
		}
	}
	if advancesStep {
		if err := rejectWorkerDispatchedStepAdvance(ctx, tx, definition, currentStep, event, p); err != nil {
			return err
		}
	}
	return nil
}

func completeWorkflowWorkerActionTx(ctx context.Context, tx *sql.Tx, event Event, scope *writeScope, p workflowActionCompletedPayload, entry RegisteredDefinition, currentStep string, advancesStep bool) error {
	// The dispatch_worker completion binds its authorized attempt durably
	// in flight: the attempt is visible in worker_attempts from the
	// moment its authorization completes, before any worker evidence
	// exists. A completion without the lane identity is a pre-binding
	// authorization and stays unbound.
	if p.ActionID == "dispatch_worker" && p.WorkerLaneID != "" {
		if err := bindWorkerAttemptInFlightTx(ctx, tx, event, p); err != nil {
			return err
		}
	}
	if err := foldWorkflowWorkerAttemptCompletionTx(ctx, tx, event, p, entry.Definition, currentStep); err != nil {
		return err
	}
	// accept_worker_result is the one advance that moves the shared step
	// off a step holding live authorized work, so it waits while another
	// attempt bound at this step is still in flight (see the hold's own
	// comment).
	if p.ActionID == "accept_worker_result" && advancesStep {
		if err := rejectStepAdvancePastLiveAttemptTx(ctx, tx, event.SubjectID, currentStep, p.WorkerAttemptID, event.Seq); err != nil {
			return err
		}
	}
	if p.ActionID == "request_correction" {
		// The fold derives the correction request once — excludeSeq keeps
		// its own in-flight completion out of the anchor's settled-entry judgment — and
		// the payload check binds against that derivation.
		context, missing, contextErr := workflowCorrectionRequestAdmissionForRegistry(ctx, tx, event.SubjectID, entry.Definition, currentStep, "fold_event", event.Seq, scopeDefinitionRegistry(scope))
		if contextErr != nil {
			return contextErr
		}
		if context == nil {
			return workflowCorrectionRequestUnavailableFailure("fold_event", missing)
		}
		correctionPayload, _ := json.Marshal(map[string]any{"diagnosis": p.CorrectionDiagnosis, "strategy": p.CorrectionStrategy, "predicate_ids": p.CorrectionPredicateIDs, "evidence_refs": p.CorrectionEvidenceRefs})
		if err := validateCorrectionRequestPayload(ctx, tx, event.SubjectID, correctionPayload, "fold_event", context); err != nil {
			return err
		}
	}
	return nil
}

func persistWorkflowActionCompletionTx(ctx context.Context, tx *sql.Tx, event Event, p workflowActionCompletedPayload, entry RegisteredDefinition, advancesStep bool, nextStep string) error {
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if err := writeWorkerAuthorizationTx(ctx, tx, event, p); err != nil {
		return workflowProjectionError(err, "cannot record the worker authorization or disposition")
	}
	if err := writeWorkflowCoordinatorActionTx(ctx, tx, event); err != nil {
		return err
	}
	if err := writeWorkflowCompletedExitTx(ctx, tx, event, p, entry.Definition.Ref); err != nil {
		return err
	}
	if err := advanceWorkflowReviewAcceptanceTx(ctx, tx, event, p); err != nil {
		return err
	}
	if err := writeWorkflowMemberCompletionTx(ctx, tx, event, p); err != nil {
		return err
	}
	if err := advanceWorkSizeProofFactsTx(ctx, tx, event); err != nil {
		return err
	}
	if err := advanceWorkflowActionObligationsTx(ctx, tx, event, p.StepID, p.AttemptEpoch, p.ActorRef); err != nil {
		return err
	}
	switch p.ActionID {
	case "dispatch_worker", "record_worker_failure", "reject_worker_result", "request_correction":
		if err := advanceWorkflowCorrectionFactsTx(ctx, tx, event); err != nil {
			return err
		}
	}
	return persistWorkflowCompletionExitTx(ctx, tx, event, p, entry, advancesStep, nextStep)
}

func persistWorkflowCompletionExitTx(ctx context.Context, tx *sql.Tx, event Event, p workflowActionCompletedPayload, entry RegisteredDefinition, advancesStep bool, nextStep string) error {
	_, asserted, assertionErr := workflowEventDeliveryAssertion(event)
	if assertionErr != nil {
		return workflowProjectionError(assertionErr, "cannot classify the current delivery assertion")
	}
	if asserted {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_delivery_state(work_id,assertion_seq,assertion_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?) ON CONFLICT(work_id) DO UPDATE SET assertion_seq=excluded.assertion_seq,assertion_id=excluded.assertion_id,actor=excluded.actor,occurred_at=excluded.occurred_at,payload_version=excluded.payload_version,payload=excluded.payload,correction_id=NULL,correction_occurred_at=NULL,correction_payload=NULL`, event.SubjectID, event.Seq, event.EventID, event.Actor, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.PayloadVersion, string(event.Payload)); err != nil {
			return workflowProjectionError(err, "cannot record the current delivery assertion")
		}
	}
	// A local job or held Review acceptance holds its phase. Every other action keeps one
	// current exit coordinate; the pinned definition chooses advancing actions.
	if p.ActionID != "accept_worker_result" || (!p.ReviewAdvanceHeld && (p.WorkerJob == nil || p.DeliveryState != "")) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_phase_exits(work_id,action_id,exit_seq,actor,step_id,contract_version,record_version)
		VALUES(?,?,?,?,?,COALESCE((SELECT MAX(contract_version) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL),0),?)
		ON CONFLICT(work_id,action_id) DO UPDATE SET exit_seq=excluded.exit_seq,actor=excluded.actor,step_id=excluded.step_id,contract_version=excluded.contract_version,record_version=excluded.record_version`, event.SubjectID, p.ActionID, event.Seq, event.Actor, p.StepID, event.SubjectID, *p.ResultingVersion); err != nil {
			return workflowProjectionError(err, "cannot record the workflow phase exit")
		}
	}
	if workflowActionProducesArtifact(p.ActionID) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_artifact_actions(work_id,step_id,action_id,production_seq) VALUES(?,?,?,?) ON CONFLICT(work_id,step_id,action_id) DO UPDATE SET production_seq=excluded.production_seq`, event.SubjectID, p.StepID, p.ActionID, event.Seq); err != nil {
			return workflowProjectionError(err, "cannot record the current artifact production")
		}
	}
	if err := recordWorkVersionActionTx(ctx, tx, event, p.ActionID, *p.ResultingVersion); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET instance_state='running',last_checkpoint_at=? WHERE work_id=?`, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.SubjectID)
	if err != nil || p.ActionID == "" || (!advancesStep && p.ActionID != "request_correction") {
		return err
	}
	if nextStep != "" {
		return advanceWorkflowInstanceStepTx(ctx, tx, event.SubjectID, nextStep, entry.Definition)
	}
	return err
}

// workflowDispatchHoldsStepAdvance reports whether a dispatched worker
// attempt holds every advancing exit except accept_worker_result, and is the
// one hold rule the fold and the work pin both read (CD-0133 D4, CD-0193).
//
// At a human_checkpoint step the hold keys on attempts a worker actually
// dispatched: the latest attempt a worker dispatched there stops holding only
// when an accept dispositions its completed report, a failure record
// dispositions it at the checkpoint, or a later worker actually dispatches on
// the step. An authorized dispatch window no worker used is not a
// disposition, so a failed review keeps holding the operator's own gate until
// a worker uses a window or a failure record releases it.
//
// Every other step keeps the CD-0133 timing: an attempt dispatched after the
// latest workflow action start holds the step, so the effect-step recovery
// route that records the failed attempt and starts a fresh fenced attempt
// reopens the exits exactly as CD-0133 recorded it.
//
// Admission reads current state before the advancing action writes its effect.
func workflowDispatchHoldsStepAdvance(ctx context.Context, q queryer, definition WorkflowDefinition, workID, stepID string, beforeSeq int64) (bool, error) {
	if step := workflowStep(definition, stepID); step != nil && step.Kind == WorkflowStepHumanCheckpoint {
		return workflowCheckpointDispatchHoldsStepAdvance(ctx, q, workID, stepID, beforeSeq)
	}
	startSeq, _, found, err := latestWorkflowActionStart(ctx, q, workID, stepID)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	query := `SELECT count(*) FROM worker_attempts a WHERE a.work_id=? AND a.dispatch_seq>? AND NOT (
 a.lifecycle_state='completed' AND EXISTS (
    SELECT 1 FROM worker_attempt_dispositions f WHERE f.work_id=a.work_id
      AND f.attempt_id=a.attempt_id AND f.action_id IN ('accept_worker_result','accept_worker_evidence')))`
	var held int
	if err := q.QueryRowContext(ctx, query, workID, startSeq).Scan(&held); err != nil {
		return false, workflowProjectionError(err, "cannot inspect worker dispatches for the current workflow attempt")
	}
	return held != 0, nil
}

// workflowCheckpointDispatchHoldsStepAdvance is the checkpoint branch of the
// one hold rule: the latest attempt a worker actually dispatched at the
// checkpoint holds it until an accept or a checkpoint failure record
// dispositions it, or a later worker actually dispatches there. A window no
// worker used has no materialized dispatch coordinate, so it releases nothing.
func workflowCheckpointDispatchHoldsStepAdvance(ctx context.Context, q queryer, workID, stepID string, beforeSeq int64) (bool, error) {
	attemptID, lifecycle, found, err := latestDispatchedAttemptAtStep(ctx, q, workID, stepID, beforeSeq)
	if err != nil || !found {
		return false, err
	}
	if lifecycle == "completed" {
		var accepted int
		if err := q.QueryRowContext(ctx, `SELECT count(*) FROM worker_attempt_dispositions WHERE work_id=? AND attempt_id=? AND action_id IN ('accept_worker_result','accept_worker_evidence')`, workID, attemptID).Scan(&accepted); err != nil {
			return false, workflowProjectionError(err, "cannot inspect worker attempt accepts for the current workflow attempt")
		}
		if accepted != 0 {
			return false, nil
		}
	}
	var recorded int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM worker_attempt_dispositions WHERE work_id=? AND attempt_id=? AND action_id='record_worker_failure'`, workID, attemptID).Scan(&recorded); err != nil {
		return false, workflowProjectionError(err, "cannot inspect recorded worker failures for the current workflow attempt")
	}
	return recorded == 0, nil
}

// latestDispatchedAttemptAtStep resolves the latest attempt a worker actually
// dispatched at the step. The dispatch_worker completion names the attempt
// and its step, and a positive dispatch coordinate proves a worker used the
// window, so an unused authorization never appears here.
func latestDispatchedAttemptAtStep(ctx context.Context, q queryer, workID, stepID string, _ int64) (string, string, bool, error) {
	query := `SELECT a.attempt_id,a.lifecycle_state FROM worker_attempts a
 JOIN worker_authorizations c ON c.work_id=a.work_id AND c.attempt_id=a.attempt_id
 WHERE a.work_id=? AND a.dispatch_seq>0 AND json_extract(c.payload,'$.step_id')=?
 ORDER BY a.dispatch_seq DESC LIMIT 1`
	var attemptID, lifecycle string
	if err := q.QueryRowContext(ctx, query, workID, stepID).Scan(&attemptID, &lifecycle); err != nil {
		if err == sql.ErrNoRows {
			return "", "", false, nil
		}
		return "", "", false, workflowProjectionError(err, "cannot inspect worker dispatches for the current workflow attempt")
	}
	return attemptID, lifecycle, true, nil
}

func rejectWorkerDispatchedStepAdvance(ctx context.Context, tx *sql.Tx, definition WorkflowDefinition, stepID string, event Event, p workflowActionCompletedPayload) error {
	held, err := workflowDispatchHoldsStepAdvance(ctx, tx, definition, event.SubjectID, stepID, event.Seq)
	if err != nil {
		return err
	}
	if !held || p.ActionID == "accept_worker_result" {
		return nil
	}
	return newFailure(KindIllegalLifecycleTransition, "fold_event", "a dispatched worker attempt must advance through accept_worker_result", false, "accept the exact completed worker attempt, record the failed attempt at a human checkpoint, or dispatch a fresh worker")
}

// bindWorkerAttemptInFlightTx records the durable in-flight binding the
// dispatch_worker completion owes: the authorized attempt becomes a
// worker_attempts row the moment its authorization completes, before any
// worker evidence exists. The binding is what keeps a live attempt visible
// while its worker runs, so another coordinator can neither treat its window
// as orphaned nor advance the shared step past it. The lane identity rides
// the completion payload; the attempt_id primary key refuses a second
// authorization for an attempt identity already in flight.
func bindWorkerAttemptInFlightTx(ctx context.Context, tx *sql.Tx, event Event, p workflowActionCompletedPayload) error {
	if p.WorkerSubjectCommit != "" && !worktreeSHAPattern.MatchString(p.WorkerSubjectCommit) {
		return newFailure(KindInvalidPayload, "fold_event", "worker_subject_commit must be one raw commit OID", false, "record the admitted core subject")
	}
	if err := recordWorkflowAssignmentGroup(ctx, tx, event, p); err != nil {
		return err
	}
	now := event.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	_, err := tx.ExecContext(ctx, `INSERT INTO worker_attempts
		(work_id,attempt_id,lane_id,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,dispatched_at)
		VALUES (?,?,?,?,?, '', '1.0', '1.0', 'in_flight', ?)`,
		event.SubjectID, p.WorkerAttemptID, p.WorkerLaneID, p.WorkerLaneDigest, p.WorkerCapabilityClass, now)
	if err != nil {
		if isIdentityConflict(err) {
			return newFailure(KindProjectionConflict, "fold_event", "worker attempt is already authorized in flight", false, "open a fresh dispatch_worker authorization with a new attempt identity")
		}
		return wrapFailure(KindUnavailable, "fold_event", "cannot create the in-flight worker attempt binding", true, "retry once the database is writable", err)
	}
	return nil
}

// rejectStepAdvancePastLiveAttemptTx is the accept-side half of the step
// hold. accept_worker_result moves the shared workflow off the step, so it
// must wait while another authorized attempt bound at that step is still in
// flight: authorized, with no worker evidence recorded yet. Without the
// hold, a second coordinator's accept advances the step away from the live
// attempt and its late evidence refuses at a step where its authorization no
// longer resolves. The accepted attempt itself is excluded: its report is
// complete, which is the only lifecycle accept_worker_result admits.
// Admission reads the current bindings before writing the completing action.
func rejectStepAdvancePastLiveAttemptTx(ctx context.Context, tx *sql.Tx, workID, stepID, acceptedAttemptID string, _ int64) error {
	var live int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM worker_attempts a JOIN worker_authorizations c ON c.work_id=a.work_id AND c.attempt_id=a.attempt_id WHERE a.work_id=? AND a.attempt_id<>? AND a.lifecycle_state='in_flight' AND json_extract(c.payload,'$.step_id')=?`, workID, acceptedAttemptID, stepID).Scan(&live); err != nil {
		return workflowProjectionError(err, "cannot inspect in-flight worker attempts for the current workflow attempt")
	}
	if live == 0 {
		return nil
	}
	return newFailure(KindIllegalLifecycleTransition, "fold_event", "another live worker attempt holds the step", false, "wait for the live attempt's worker evidence to land, then accept again")
}

func latestWorkflowActionStart(ctx context.Context, q queryer, workID, stepID string) (int64, int64, bool, error) {
	start, err := readWorkflowActionStartTx(ctx, q, workID, stepID, false)
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

// workflowStepPassBoundary reports the event seq that opens the current pass
// at a step, and whether the step has any pass activity. A step re-entered
// through a return edge starts a new pass: the latest entry is the advancing
// action completion whose fold moved the instance back onto the step, and a
// WorkflowActionStarted that precedes that entry belongs to the previous pass.
// The step's latest start bounds the entry search, so the query only sees
// advancing completions recorded after the step's last observed start, and the
// later of the two opens the pass. Contract correction and worker-result
// recovery read this boundary, which keeps the work pin, action discovery,
// preflight, and the fold on one answer. Non-advancing step moves (a
// correction request's return) are outside this rule and keep their own
// recovery semantics.
func workflowStepPassBoundary(ctx context.Context, q queryer, definition WorkflowDefinition, workID, stepID, subject string) (int64, bool, error) {
	startSeq, _, started, err := latestWorkflowActionStart(ctx, q, workID, stepID)
	if err != nil {
		return 0, false, err
	}
	if !started {
		return 0, false, nil
	}
	advancing := advancingWorkflowActions(definition)
	if len(advancing) == 0 {
		return startSeq, true, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(advancing)), ",")
	args := []any{workID, startSeq}
	for _, action := range advancing {
		args = append(args, action)
	}
	var entrySeq int64
	// A held local job acceptance records a disposition and holds the step
	// (CD-0205 D3), so it is no step entry: the pass boundary it would move
	// past still governs the reads that recover results and corrections.
	query := `SELECT COALESCE(MAX(exit_seq),0) FROM workflow_phase_exits WHERE work_id=? AND exit_seq>? AND action_id IN (` + placeholders + `)`
	if err := q.QueryRowContext(ctx, query, args...).Scan(&entrySeq); err != nil {
		return 0, false, wrapFailure(KindUnavailable, subject, "cannot inspect the workflow step's latest entry", true, "retry once the current workflow rows are readable", err)
	}
	if workflowDerivedStepStartActive(definition) {
		// A successor contract opens fresh work at the held step. Its typed
		// supersession event is not an action completion, but the next
		// qualifying action must claim a new epoch rather than reuse the
		// predecessor contract's start and verification cutoff. Released
		// callable-start pins keep their original pass-boundary rule.
		var supersededSeq int64
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(supersession_seq),0) FROM workflow_contracts WHERE work_id=? AND supersession_seq>?`, workID, startSeq).Scan(&supersededSeq); err != nil {
			return 0, false, wrapFailure(KindUnavailable, subject, "cannot inspect the workflow step's contract entry", true, "retry once the current workflow rows are readable", err)
		}
		entrySeq = max(entrySeq, supersededSeq)
	}
	if entrySeq > startSeq {
		return entrySeq, true, nil
	}
	return startSeq, true, nil
}

// advancingWorkflowActions lists the pinned actions whose completed fold moves
// the instance between steps — the same mode test the completed-action fold
// applies — so the pass boundary reads the fold's own step-transition rule.
func advancingWorkflowActions(definition WorkflowDefinition) []string {
	var advancing []string
	for _, action := range definition.ActionDefinitions {
		if action.ExecutionMode == ActionAdvance {
			advancing = append(advancing, action.ID)
		}
	}
	return advancing
}

// workflowDerivedStartPairAuthored reports whether the checkpoint event at
// beforeSeq is the one its own derived start opened (CD-0198 D3). Only a
// derived-start pin qualifies, and the step's latest start must be the start
// event the checkpoint's own operation wrote (opid:started beside
// opid:checkpoint) naming the same qualifying action, epoch, and actor. A
// dispatch start, a callable start on a released pin, and every later
// checkpoint answer to the checkpoint-writer rule; the started fold
// authenticated the actor for the derivation pair alone.
func workflowDerivedStartPairAuthored(ctx context.Context, tx *sql.Tx, definition WorkflowDefinition, workID, stepID, actionID string, epoch int64, _ int64, actorRef, checkpointEventID string) (bool, error) {
	if !workflowDerivedStepStartActive(definition) || workflowDerivedStartCheckpoints[actionID] != stepID {
		return false, nil
	}
	start, err := readWorkflowActionStartTx(ctx, tx, workID, stepID, false)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, workflowProjectionError(err, "cannot read the derived step start")
	}
	var payload workflowActionStartedPayload
	if err := json.Unmarshal(start.Payload, &payload); err != nil {
		return false, err
	}
	if payload.AttemptEpoch != epoch {
		return false, nil
	}
	operationID := strings.TrimSuffix(checkpointEventID, ":checkpoint")
	if operationID == checkpointEventID || start.EventID != operationID+":started" || payload.ActionID != actionID {
		return false, nil
	}
	return payload.ActorRef == actorRef, nil
}

func latestWorkflowActionStartEpoch(ctx context.Context, tx *sql.Tx, workID, stepID string, beforeSeq int64) (int64, bool, error) {
	_, epoch, found, err := latestWorkflowActionStartAt(ctx, tx, workID, stepID, beforeSeq)
	return epoch, found, err
}

// workflowCheckpointAttemptEpoch resolves the attempt epoch a checkpoint on
// this step must carry, and is the single rule the checkpoint producer and the
// checkpoint fold both answer to.
//
// A fenced action opens an attempt and records its start, so a checkpoint on
// such a step must name a started attempt: a checkpoint with no start is a
// checkpoint of an attempt that never ran. A step that declares no fenced
// action opens no fenced attempt. Its checkpoint epoch follows the prior
// checkpoint on that step, so a return can produce another typed artifact
// without reusing the checkpoint's durable identity (CD-0112 D3).
func workflowCheckpointAttemptEpoch(ctx context.Context, q queryer, definition WorkflowDefinition, step *WorkflowStep, workID, stepID string) (int64, error) {
	_, latestEpoch, found, err := latestWorkflowActionStartAt(ctx, q, workID, stepID, 0)
	if err != nil {
		return 0, err
	}
	if step != nil && workflowStepFences(definition, *step) {
		if found {
			return latestEpoch, nil
		}
		return 0, newFailure(KindIllegalLifecycleTransition, "fold_event", "checkpoint does not match the latest workflow action start epoch", false, "checkpoint the current workflow action attempt")
	}
	var epoch int64
	err = q.QueryRowContext(ctx, `SELECT COALESCE(MAX(attempt_epoch),0)+1
		FROM workflow_checkpoints WHERE work_id=? AND step_id=?`, workID, stepID).Scan(&epoch)
	if err != nil {
		return 0, workflowProjectionError(err, "cannot inspect prior workflow checkpoint epochs")
	}
	// Review dispatches can start on an unfenced confirmation step. A later
	// checkpoint must not reuse a prior checkpoint identity.
	return max(epoch, latestEpoch), nil
}

// workflowCheckpointWriterAdmitted reports whether the workflow actor may
// append checkpoint-event actions and action failures on the work item at the
// event's sequence position.
//
// A live lane attempt — the latest worker.dispatched with no
// accept_worker_result, reject_worker_result, or record_worker_failure naming
// it — keeps the lane, pinned by the dispatch as the instance's executing
// actor, as the sole writer. A fenced action keeps the single executor and
// disposer rule: the executor writes while its attempt runs, and the owner
// that disposed of the attempt writes until the next dispatch re-pins a lane.
// Any other checkpoint-shaped action — a decision or review checkpoint on a
// step no fenced attempt owns — admits every recorded coordinator actor,
// except an actor that is the lane_actor_ref of any worker.dispatched on the
// item: a disposed lane never regains the pen. execution_actor_ref keeps
// serving only the independence readers, so no coordinator signs its own
// verdict and the executing pin does not move.
//
// Admission reads current state before writing the checkpoint.
func workflowCheckpointWriterAdmitted(ctx context.Context, q queryer, workID, executionActor, actorRef string, fenced bool, beforeSeq int64) (bool, error) {
	if executionActor == "" || actorRef == "" {
		return false, nil
	}
	writer, live, _, err := workflowFencedWriter(ctx, q, workID, executionActor, beforeSeq)
	if err != nil {
		return false, err
	}
	if live || fenced {
		// A live lane attempt keeps the lane as sole writer; a fenced action
		// keeps its executor and disposer rule.
		return actorRef == writer, nil
	}
	// Any recorded coordinator actor is admitted; an actor that is the
	// lane_actor_ref of any worker.dispatched on the item never is. A legacy
	// dispatch carries no lane_actor_ref, so it excludes nothing.
	var laneActors int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM worker_attempts WHERE work_id=? AND dispatch_seq>0 AND json_extract(dispatch_payload,'$.lane_actor_ref')=?`, workID, actorRef).Scan(&laneActors); err != nil {
		return false, workflowProjectionError(err, "cannot inspect worker dispatch actors for the workflow writing authority")
	}
	return laneActors == 0, nil
}

// workerAttemptDisposalActions lists, as SQL literals, the workflow actions
// that record a lane attempt's result decision and so end its hold.
const workerAttemptDisposalActions = `'accept_worker_result','accept_worker_evidence','reject_worker_result','record_worker_failure','record_worker_job'`

// workflowFencedWriter names the actor that holds a fenced attempt's current
// writing authority, and the sequence at which it took it: the lane
// of a live lane attempt from its dispatch, the actor that disposed of the
// latest lane attempt from its disposal, or the executing actor from no
// recorded sequence (zero).
func workflowFencedWriter(ctx context.Context, q queryer, workID, executionActor string, _ int64) (writer string, live bool, since int64, err error) {
	var dispatchSeq int64
	var attemptID string
	err = q.QueryRowContext(ctx, `SELECT dispatch_seq,attempt_id FROM worker_attempts WHERE work_id=? AND dispatch_seq>0 ORDER BY dispatch_seq DESC LIMIT 1`, workID).Scan(&dispatchSeq, &attemptID)
	if err == sql.ErrNoRows {
		return executionActor, false, 0, nil
	}
	if err != nil {
		return "", false, 0, workflowProjectionError(err, "cannot inspect worker dispatches for the workflow writing authority")
	}
	var disposalSeq int64
	var disposer string
	err = q.QueryRowContext(ctx, `SELECT seq,COALESCE(json_extract(payload,'$.actor_ref'),'') FROM worker_attempt_dispositions WHERE work_id=? AND attempt_id=? AND seq>? ORDER BY seq DESC LIMIT 1`, workID, attemptID, dispatchSeq).Scan(&disposalSeq, &disposer)
	if err == sql.ErrNoRows {
		// The dispatch pins the lane as the executing actor.
		return executionActor, true, dispatchSeq, nil
	}
	if err != nil {
		return "", false, 0, workflowProjectionError(err, "cannot inspect worker attempt disposals for the workflow writing authority")
	}
	if disposer == "" {
		disposer = executionActor
	}
	return disposer, false, disposalSeq, nil
}

// workflowStepFences reports whether any action on the step runs fenced, and so
// whether the step records an attempt start a checkpoint can bind to.
// workflowStepFences reports whether the step carries a coordinator
// execution fence: a fenced action whose attempt the step's checkpoint and
// failure events belong to. The CD-0187 checkpoint dispatch is a lane attempt
// beside the operator's gate, not a coordinator fence, so a confirmation step
// hosts none: its checkpoint events record operator decisions, admit the
// coordinator writers, and a checkpoint with no start still carries the first
// attempt epoch (CD-0112 D3).
func workflowStepFences(definition WorkflowDefinition, step WorkflowStep) bool {
	if step.Kind == WorkflowStepHumanCheckpoint {
		return false
	}
	for _, actionID := range step.Actions {
		if mode, ok := workflowActionExecutionMode(definition, actionID); ok && mode == ActionFenced {
			return true
		}
	}
	return false
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

// workflowDerivedStartCheckpoints names the checkpoint whose admission
// claims a derived step start (CD-0198 D3): the checkpoint matching the
// step whose callable start the derived-start pins removed. dispatch_worker
// needs no entry — a fenced dispatch opens the attempt epoch itself — and a
// context checkpoint, job record, evidence bind, result rejection, or
// delivery never appears here, so none of them can manufacture a start.
var workflowDerivedStartCheckpoints = map[string]string{
	"checkpoint_repair": "repair",
	"checkpoint_refine": "refine",
}

// workflowDerivedStepStartActive reports whether a pinned definition
// derives the repair/refine step starts from the first admitted qualifying
// action (CD-0198 D3). Earlier versions keep the callable starts and the
// behavior and digest they shipped with.
func workflowDerivedStepStartActive(definition WorkflowDefinition) bool {
	return workflowCapabilities(definition).has(WorkflowCapabilityDerivedStepStart)
}

// workflowDerivedCheckpointStartTx derives the step start a qualifying
// checkpoint claims (CD-0198 D3). On a derived-start pin, when the matching
// step holds no action start yet, the checkpoint opens the step's first
// attempt: the returned event folds through the same started-event fold a
// callable start uses, fencing the step from the checkpoint's own actor and
// opening epoch 1, and the checkpoint carries that epoch. A step that
// already started — by a dispatch or an earlier qualifying checkpoint —
// returns no event, and the caller reads the existing epoch, so a repeated
// qualifying action never resets the fence or its cutoff.
func workflowDerivedCheckpointStartTx(ctx context.Context, tx *sql.Tx, definition WorkflowDefinition, request WorkflowActionExecutionRequest, currentStep, actor string, expectedVersion int64) (int64, *Event, error) {
	if !workflowDerivedStepStartActive(definition) || workflowDerivedStartCheckpoints[request.ActionID] != currentStep {
		return 0, nil, nil
	}
	// The step's first qualifying action of the CURRENT pass derives the
	// start: a fresh entry has no start at all, and a step re-entered
	// through a declared return edge — a correction route — opened a new
	// pass whose advancing entry postdates the previous pass's start, so
	// its first qualifying action claims the next epoch exactly as the
	// callable start did. A repeated qualifying action inside one pass
	// finds its own pass's start and reuses the epoch.
	latestStartSeq, _, startFound, err := latestWorkflowActionStartAt(ctx, tx, request.WorkID, currentStep, 0)
	if err != nil {
		return 0, nil, err
	}
	if startFound {
		passSeq, active, passErr := workflowStepPassBoundary(ctx, tx, definition, request.WorkID, currentStep, "workflow_action")
		if passErr != nil {
			return 0, nil, passErr
		}
		if !active || passSeq <= latestStartSeq {
			return 0, nil, nil
		}
	}
	epoch, err := workflowActionStartEpochForDispatch(ctx, tx, request.WorkID, currentStep, true)
	if err != nil {
		return 0, nil, err
	}
	startPayload, err := json.Marshal(map[string]any{
		"work_id": request.WorkID, "expected_version": expectedVersion, "resulting_version": expectedVersion + 1,
		"step_id": currentStep, "action_id": request.ActionID, "attempt_epoch": epoch,
		"accepted_inputs_digest": request.AcceptedInputsDigest, "idempotency_identity": request.IdempotencyIdentity, "actor_ref": actor,
	})
	if err != nil {
		return 0, nil, workflowProjectionError(err, "cannot encode the derived step start")
	}
	event := Event{EventID: request.OperationID + ":started", Kind: WorkflowActionStarted, SubjectType: SubjectWorkItem, SubjectID: request.WorkID, Actor: actor, OccurredAt: request.Now, PayloadVersion: 1, Payload: startPayload}
	return epoch, &event, nil
}

// Admission reads the current descriptor before its own start is written.
// Authorized worker completions use their immutable authorization snapshot.
func latestWorkflowActionStartAt(ctx context.Context, q queryer, workID, stepID string, _ int64) (int64, int64, bool, error) {
	start, err := readWorkflowActionStartTx(ctx, q, workID, stepID, false)
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

// readWorkflowActionStartTx reads the current start descriptor. A dispatch
// start survives unrelated later starts so authorization can snapshot its pair.
func readWorkflowActionStartTx(ctx context.Context, q queryer, workID, stepID string, dispatchOnly bool) (Event, error) {
	query := `SELECT latest_start_seq,latest_start_event_id,latest_start_payload FROM workflow_step_epochs WHERE work_id=? AND step_id=? AND latest_start_seq>0`
	if dispatchOnly {
		query = `SELECT latest_dispatch_start_seq,latest_dispatch_start_event_id,latest_dispatch_start_payload FROM workflow_step_epochs WHERE work_id=? AND step_id=? AND latest_dispatch_start_seq>0`
	}
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

func validateWorkerAttemptAction(ctx context.Context, tx *sql.Tx, event Event, payload workflowActionCompletedPayload, definition WorkflowDefinition, currentStep, requiredLifecycle string) error {
	step := workflowStep(definition, currentStep)
	// derivedPair marks the CD-0198 D5 derivation pair: the completing event
	// is the workflow failure record the attempt's own terminal event
	// derives. The pair's actor is already authenticated by that terminal
	// fold, and the executor-distinctness law below serves verdict
	// independence, not the engine's own disposition of the worker's terminal
	// fact — so the pair alone skips it, exactly as the CD-0198 D3 pair skips
	// the checkpoint-writer rule. The pair check runs beside the recovery
	// route, not behind it: on a derived-failure pin the recovery admits the
	// record first, and the pair identity — not the admitting route — is
	// what the skip keys on.
	derivedPair, err := admitWorkflowWorkerAttemptStepTx(ctx, tx, event, payload, definition, step, currentStep)
	if err != nil {
		return err
	}
	if payload.WorkerAttemptID == "" {
		return newFailure(KindInvalidPayload, "fold_event", payload.ActionID+" requires worker_attempt_id", false, "supply the exact worker attempt identity")
	}
	var attemptWorkID, lifecycle, readback, capability string
	if err := tx.QueryRowContext(ctx, `SELECT work_id,lifecycle_state,readback_model,capability_class FROM worker_attempts WHERE attempt_id=?`, payload.WorkerAttemptID).Scan(&attemptWorkID, &lifecycle, &readback, &capability); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindProjectionNotFound, "fold_event", "worker attempt does not exist", false, "dispatch the worker attempt before recording its result")
		}
		return workflowProjectionError(err, "cannot read worker attempt projection")
	}
	if attemptWorkID != event.SubjectID {
		return newFailure(KindIllegalLifecycleTransition, "fold_event", "worker attempt belongs to another work item", false, "record the exact worker attempt for this work item")
	}
	var target workflowRejectableResult
	if payload.ActionID == "reject_worker_result" {
		var rejectionErr error
		target, rejectionErr = validateWorkerResultRejection(ctx, tx, event.SubjectID, definition, currentStep, "fold_event", payload.WorkerAttemptID, payload.AttemptEpoch, event.Seq)
		if rejectionErr != nil {
			return rejectionErr
		}
	}
	var attemptSeq int64
	if err := tx.QueryRowContext(ctx, `SELECT dispatch_seq FROM worker_attempts WHERE work_id=? AND attempt_id=? AND dispatch_seq>0 UNION ALL SELECT seq FROM worker_authorizations WHERE work_id=? AND attempt_id=? AND ? AND json_extract(payload,'$.step_id')=? ORDER BY 1 DESC LIMIT 1`, event.SubjectID, payload.WorkerAttemptID, event.SubjectID, payload.WorkerAttemptID, requiredLifecycle == "failed", currentStep).Scan(&attemptSeq); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindProjectionNotFound, "fold_event", "worker attempt dispatch event does not exist", false, "dispatch the worker attempt before recording its result")
		}
		return workflowProjectionError(err, "cannot read worker attempt dispatch")
	}
	if payload.ActionID == "record_worker_job" {
		// Recorded executor identity does not depend on current rejection eligibility.
		var err error
		target.ExecutorRef, err = workerExecutorAtSequenceTx(ctx, tx, event.SubjectID, payload.WorkerAttemptID, attemptSeq)
		if err != nil {
			return workflowProjectionError(err, "cannot read recorded worker executor")
		}
	}
	if err := validateWorkflowWorkerAttemptEpochTx(ctx, tx, event, payload, step, currentStep, capability, attemptSeq); err != nil {
		return err
	}
	if lifecycle != requiredLifecycle {
		return newFailure(KindIllegalLifecycleTransition, "fold_event", "worker attempt is not "+requiredLifecycle, false, "record only a "+requiredLifecycle+" worker attempt")
	}
	if requiredLifecycle == "completed" && readback == "" {
		return newFailure(KindIllegalLifecycleTransition, "fold_event", "worker readback model is empty", false, "accept only a worker attempt that reported a readback model")
	}
	if payload.ActorRef != event.Actor {
		return newFailure(KindUnauthorized, "fold_event", "worker result actor must match the authenticated event actor", false, "record the worker result through the authenticated workflow owner")
	}
	if !derivedPair {
		if err := workflowActorsDistinctForExecutor(ctx, tx, event.SubjectID, target.ExecutorRef, payload.ActorRef); err != nil {
			return err
		}
	}
	if capability == "review" && (payload.ActionID == "accept_worker_evidence" || payload.ActionID == "accept_worker_result") {
		return validateReviewAcceptance(ctx, tx, event.SubjectID, payload.WorkerAttemptID, payload.ActorRef)
	}
	return nil
}

func admitWorkflowWorkerAttemptStepTx(ctx context.Context, tx *sql.Tx, event Event, payload workflowActionCompletedPayload, definition WorkflowDefinition, step *WorkflowStep, currentStep string) (bool, error) {
	allowed := step != nil && definitionStepAllows(definition, currentStep, payload.ActionID)
	derivedPair := false
	if !allowed && payload.ActionID == "accept_worker_evidence" {
		if err := validateRefineReviewEvidenceAttempt(ctx, tx, event.SubjectID, definition, currentStep, payload.WorkerAttemptID, payload.AttemptEpoch, "fold_event", event.Seq); err != nil {
			return false, err
		}
		allowed = true
	}
	if !allowed && payload.ActionID == "record_worker_failure" {
		var recoveryErr error
		allowed, recoveryErr = workflowWorkerFailureRecoveryMayFold(ctx, tx, event.SubjectID, definition, currentStep, "fold_event")
		if recoveryErr != nil {
			return false, recoveryErr
		}
		// CD-0198 D5: the terminal event's own derivation pair folds without
		// the recovery route — the record derives from the attempt's terminal
		// event, not from a coordinator call.
		derivedPair, recoveryErr = workflowDerivedWorkerFailurePairAdmitsFold(ctx, tx, definition, event, payload)
		if recoveryErr != nil {
			return false, recoveryErr
		}
		allowed = allowed || derivedPair
	}
	if !allowed && payload.ActionID == "reject_worker_result" && stepDeclaresAction(definition, currentStep, "dispatch_worker") {
		// Live rejection eligibility is checked after reading the attempt.
		allowed = true
	}
	if !allowed {
		return false, newFailure(KindIllegalLifecycleTransition, "fold_event", payload.ActionID+" is only valid on a worker-dispatch step", false, "record the worker attempt on its pinned step")
	}
	return derivedPair, nil
}

func validateWorkflowWorkerAttemptEpochTx(ctx context.Context, tx *sql.Tx, event Event, payload workflowActionCompletedPayload, step *WorkflowStep, currentStep, capability string, attemptSeq int64) error {
	// Failure can bind to the authorization that created the attempt even
	// when no worker dispatch evidence was admitted. Success still requires
	// an actual dispatch. The epoch fence (CD-0133) remains the same.
	// At a human_checkpoint step the
	// attempt anchors to the dispatch window that opened it (CD-0193): an
	// authorized window no worker used must not orphan the failed attempt the
	// operator records there. Every other step keeps the prior rule for the
	// accept and failure dispositions: the epoch must match the latest
	// workflow action start on the step, and the dispatch must follow that
	// start, so a stale completed attempt cannot advance the step past the
	// current one. Live rejection skips that anchor: its exact target validation
	// above owns the epoch and dispatch provenance at this bound.
	var startSeq, startEpoch int64
	var found bool
	var err error
	var memberBase *workflowMemberBase
	if capability == "implementation" && (payload.ActionID == "accept_worker_result" || payload.ActionID == "accept_worker_evidence") {
		auth, _, readErr := readReviewAttempt(ctx, tx, event.SubjectID, payload.WorkerAttemptID)
		if readErr != nil {
			return readErr
		}
		memberBase, err = workflowMemberResultBase(ctx, tx, event.SubjectID, auth)
		if err != nil {
			return err
		}
	}
	if step != nil && step.Kind == WorkflowStepHumanCheckpoint || payload.ActionID == "record_worker_job" || memberBase != nil {
		var raw []byte
		err = tx.QueryRowContext(ctx, `SELECT start_seq,start_payload FROM worker_authorizations WHERE work_id=? AND attempt_id=? AND start_seq>0`, event.SubjectID, payload.WorkerAttemptID).Scan(&startSeq, &raw)
		switch err {
		case sql.ErrNoRows:
			err = nil
		case nil:
			var started workflowActionStartedPayload
			if decodeErr := json.Unmarshal(raw, &started); decodeErr != nil {
				return workflowProjectionError(decodeErr, "cannot decode the worker authorization start")
			}
			startEpoch, found = started.AttemptEpoch, started.StepID == currentStep
		default:
			return workflowProjectionError(err, "cannot inspect the worker authorization start")
		}
	} else {
		startSeq, startEpoch, found, err = latestWorkflowActionStart(ctx, tx, event.SubjectID, currentStep)
	}
	if err != nil {
		return err
	}
	if (!found || payload.AttemptEpoch != startEpoch) && payload.ActionID != "reject_worker_result" {
		return newFailure(KindIllegalLifecycleTransition, "fold_event", "worker attempt epoch does not match the latest workflow action start", false, "record the attempt with the epoch of the workflow action start that dispatched it")
	}
	if attemptSeq <= startSeq && payload.ActionID != "reject_worker_result" {
		return newFailure(KindIllegalLifecycleTransition, "fold_event", "worker attempt dispatch is stale", false, "record a worker attempt dispatched after the current action start")
	}
	return nil
}

func workflowNextStep(definition WorkflowDefinition, current string) string {
	for _, edge := range definition.StepGraph.Edges {
		if edge.From == current && edge.Kind != WorkflowEdgeRetry {
			return edge.To
		}
	}
	return ""
}

func workflowNextStepForAction(ctx context.Context, tx *sql.Tx, workID string, definition WorkflowDefinition, current, actionID string, registry DefinitionRegistry, beforeSeq int64) (string, error) {
	step := workflowStep(definition, current)
	if actionID != "confirm_premise" || step == nil || !slices.Contains(step.Actions, "record_verdict") {
		return workflowNextStep(definition, current), nil
	}
	hasFailureEdge := false
	for _, edge := range definition.StepGraph.Edges {
		if edge.From == current && edge.Kind == WorkflowEdgeFailure {
			hasFailureEdge = true
			break
		}
	}
	if !hasFailureEdge {
		return workflowNextStep(definition, current), nil
	}
	var contractCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_contracts WHERE work_id=?`, workID).Scan(&contractCount); err != nil {
		return "", workflowProjectionError(err, "cannot inspect workflow contracts")
	}
	if contractCount == 0 {
		return workflowNextStep(definition, current), nil
	}
	contract, _, err := workflowCompletionContract(ctx, tx, registry, workID)
	if err != nil {
		return "", err
	}
	verdicts, err := latestWorkflowVerdictsBefore(ctx, tx, workID, contract.Version, beforeSeq)
	if err != nil {
		return "", err
	}
	for _, verdict := range verdicts {
		if verdict.VerdictKind != "ok" || verdict.IncomparableWithApproved {
			for _, edge := range definition.StepGraph.Edges {
				if edge.From == current && edge.Kind == WorkflowEdgeFailure {
					return edge.To, nil
				}
			}
			break
		}
	}
	return workflowNextStep(definition, current), nil
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
	if !TypedErrorKindAllowed(p.FailureKind) {
		return newFailure(KindInvalidPayload, "fold_event", "action_failed failure kind is not closed", false, "use a TS7 typed error kind")
	}
	entry, err := VerifyWorkflowInstanceDefinitionTx(ctx, tx, scopeDefinitionRegistry(scope), event.SubjectID)
	if err != nil {
		return err
	}
	var currentStep, executionActor string
	if err := tx.QueryRowContext(ctx, `SELECT current_step,COALESCE(execution_actor_ref,'') FROM workflow_instances WHERE work_id=?`, event.SubjectID).Scan(&currentStep, &executionActor); err != nil {
		return workflowProjectionError(err, "cannot read the current workflow failure authority")
	}
	if p.ActorRef != event.Actor {
		return newFailure(KindUnauthorized, "fold_event", "action failure actor must match the authenticated event actor", false, "fail the workflow action through the authenticated workflow actor")
	}
	if p.StepID != currentStep {
		return newFailure(KindIllegalLifecycleTransition, "fold_event", "action failure does not match the pinned current workflow step", false, "fail the pinned current workflow step")
	}
	_, latestEpoch, found, err := latestWorkflowActionStartAt(ctx, tx, event.SubjectID, currentStep, event.Seq)
	if err != nil {
		return err
	}
	if !found || p.AttemptEpoch != latestEpoch {
		return newFailure(KindIllegalLifecycleTransition, "fold_event", "action failure does not match the latest workflow action start epoch", false, "fail the current workflow action attempt")
	}
	step := workflowStep(entry.Definition, currentStep)
	fenced := step != nil && workflowStepFences(entry.Definition, *step)
	admitted, err := workflowCheckpointWriterAdmitted(ctx, tx, event.SubjectID, executionActor, p.ActorRef, fenced, event.Seq)
	if err != nil {
		return err
	}
	if !admitted {
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
	return advanceWorkflowActionObligationsTx(ctx, tx, event, p.StepID, p.AttemptEpoch, p.ActorRef)
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

func validateWorkflowStalenessObservedPayload(event Event, payload workflowStalenessObservedPayload) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	if !workflowString(payload.RuleID, 128) || (payload.Severity != "warning" && payload.Severity != "block") || payload.ObservedAt == "" || !validDigest(payload.AcceptedInputsDigest) {
		return newFailure(KindInvalidPayload, "validate_event", "staleness observation is incomplete", false, "supply rule, severity, drift, and observation time")
	}
	if _, err := time.Parse(time.RFC3339Nano, payload.ObservedAt); err != nil {
		return newFailure(KindInvalidPayload, "validate_event", "staleness observation time is invalid", false, "supply an RFC3339 observation time")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(event.Payload, &fields); err != nil {
		return newFailure(KindInvalidPayload, "validate_event", "staleness observation payload is malformed", false, "supply a JSON object")
	}
	if _, ok := fields["drifted"]; !ok {
		return newFailure(KindInvalidPayload, "validate_event", "staleness observation drift is required", false, "supply the observed drift value")
	}
	return nil
}

func foldWorkflowStalenessObserved(ctx context.Context, tx *sql.Tx, event Event) error {
	var payload workflowStalenessObservedPayload
	if err := decodeWorkflowPayload(event, &payload); err != nil {
		return err
	}
	if err := validateWorkflowStalenessObservedPayload(event, payload); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_staleness_observations(event_id,work_id,rule_id,severity,drifted,accepted_inputs_digest,observed_at) VALUES(?,?,?,?,?,?,?)`, event.EventID, event.SubjectID, payload.RuleID, payload.Severity, payload.Drifted, payload.AcceptedInputsDigest, payload.ObservedAt)
	return err
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
	if err := workflowRequireMain(ctx, tx, event.SubjectID, p.VerdictActorRef); err != nil {
		return err
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_predicate_verdicts(work_id,contract_version,predicate_id,verdict_seq,actor,payload,prior_correction_seq) VALUES(?,?,?,?,?,?,COALESCE((SELECT exit_seq FROM workflow_phase_exits WHERE work_id=? AND action_id='request_correction'),0)) ON CONFLICT(work_id,contract_version,predicate_id) DO UPDATE SET verdict_seq=excluded.verdict_seq,actor=excluded.actor,payload=excluded.payload,prior_correction_seq=excluded.prior_correction_seq`, event.SubjectID, p.ContractVersion, p.PredicateID, event.Seq, event.Actor, string(event.Payload), event.SubjectID); err != nil {
		return workflowProjectionError(err, "cannot record the current predicate verdict")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_artifact_actions SET verdict_seq=?,verdict_production_seq=production_seq WHERE work_id=?`, event.Seq, event.SubjectID); err != nil {
		return workflowProjectionError(err, "cannot snapshot production for the current verdict window")
	}
	if p.VerdictKind != "ok" || p.IncomparableWithApproved {
		if _, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET unhealthy_verdict_seq=? WHERE work_id=?`, event.Seq, event.SubjectID); err != nil {
			return workflowProjectionError(err, "cannot record the artifact-staleness verdict frontier")
		}
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
	registered, registeredErr := VerifyWorkflowInstanceDefinitionTx(ctx, tx, scopeDefinitionRegistry(scope), event.SubjectID)
	if registeredErr != nil {
		return registeredErr
	}
	return workflowPremiseConfirmedWriteTx(ctx, tx, registered.Definition, event, p)
}

// workflowPremiseConfirmedWriteTx is the premise confirmation fold body minus
// the builtin definition resolution: the caller supplies the verified
// definition, so consequence_v1 seams can drive the same owner a live fold
// drives. It validates the claimed authority against the definition and the
// active contract's stored fact, binds the confirming actor, and records the
// confirmation row.
func workflowPremiseConfirmedWriteTx(ctx context.Context, tx *sql.Tx, definition WorkflowDefinition, event Event, p workflowPremiseConfirmedPayload) error {
	if err := workflowPremiseConfirmedAuthorityTx(ctx, tx, definition, event.SubjectID, p.ContractVersion, p.ConfirmingActorRef, p.ConfirmationAuthority); err != nil {
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
	var successorKind, definitionRef string
	if err := tx.QueryRowContext(ctx, `SELECT kind FROM work_items WHERE id=?`, p.SuccessorWorkID).Scan(&successorKind); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindInvalidRelation, "fold_event", "successor work item is not recorded", false, "create the typed successor before linking it")
		}
		return workflowProjectionError(err, "cannot read successor work item")
	}
	if p.SuccessorKind != "" && p.SuccessorKind != successorKind {
		return newFailure(KindInvalidRelation, "fold_event", "successor kind does not match the authoritative work item", false, "reread the successor work item")
	}
	if err := tx.QueryRowContext(ctx, `SELECT definition_ref FROM workflow_instances WHERE work_id=?`, p.SuccessorWorkID).Scan(&definitionRef); err != nil && err != sql.ErrNoRows {
		return workflowProjectionError(err, "cannot read successor workflow definition")
	}
	if definitionRef == "" {
		return newFailure(KindInvalidRelation, "fold_event", "successor has no workflow instance, so its family is undetermined", false, "select a workflow definition for the successor before linking it")
	}
	if p.DefinitionRef == "" {
		return newFailure(KindInvalidRelation, "fold_event", "successor definition reference is required", false, "record the pinned successor workflow definition")
	}
	if p.DefinitionRef != "" && p.DefinitionRef != definitionRef {
		return newFailure(KindInvalidRelation, "fold_event", "successor definition does not match the authoritative workflow instance", false, "reread the successor workflow definition")
	}
	successorDefinition, successorErr := BuiltinWorkflowDefinitionForRef(definitionRef)
	if successorErr != nil {
		return successorErr
	}
	successorFamily := successorDefinition.Definition.WorkKind
	var sourceDefinitionRef string
	if err := tx.QueryRowContext(ctx, `SELECT definition_ref FROM workflow_instances WHERE work_id=?`, event.SubjectID).Scan(&sourceDefinitionRef); err != nil {
		return workflowProjectionError(err, "cannot read source workflow definition")
	}
	source, err := BuiltinWorkflowDefinitionForRef(sourceDefinitionRef)
	if err != nil {
		return err
	}
	if p.Resize {
		// A stored resize link stays same-family; the retired lineage table
		// holds no rows to join (CD-0242 D6).
		if successorFamily != source.Definition.WorkKind {
			return newFailure(KindInvalidRelation, "fold_event", "a resize link joins a resized item to its own same-family successor", false, "link a resize successor through the resize")
		}
	} else if !slices.Contains(source.Definition.CompositionRules.AllowedSuccessorWorkKinds, successorFamily) {
		return newFailure(KindInvalidRelation, "fold_event", "successor family is not allowed by the source workflow composition", false, "use an allowed forward-linked successor family")
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if cycle, err := workflowEdgeWouldCycle(ctx, tx, event.SubjectID, p.SuccessorWorkID, "forward_link"); err != nil {
		return err
	} else if cycle {
		f := newFailure(KindCycleDetected, "fold_event", "forward link would create a cycle", false, "choose a non-cyclic successor")
		// Carry the typed offending edge so the agent layer can surface
		// error.violations structurally (D5).
		f.Violations = []string{"forward_link:" + event.SubjectID + "->" + p.SuccessorWorkID}
		return f
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workflow_impact_edges(work_id,edge_id,edge_kind,edge_class,target_work_id,target_kind,severity,recorded_at) VALUES(?,?,?,?,?,?,?,?)`, event.SubjectID, event.EventID, "forward_link", "hard", p.SuccessorWorkID, "work_item", "informational", event.OccurredAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return workflowProjectionError(err, "cannot record successor link")
	}
	return insertWorkflowForwardRelation(ctx, tx, event, p.SuccessorWorkID)
}

func foldWorkflowImpactDeclared(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowImpactDeclaredPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if !workflowString(p.EdgeID, 128) || p.EdgeKind != "modifies" && p.EdgeKind != "depends_on" && p.EdgeKind != "forward_link" || p.EdgeClass != "hard" && p.EdgeClass != "soft" && p.EdgeClass != "none" || !workflowString(p.TargetWorkID, 128) || p.TargetKind != "work_item" || p.Severity != "breaking" && p.Severity != "non-breaking" && p.Severity != "informational" {
		return newFailure(KindInvalidPayload, "fold_event", "impact edge contains a closed-enum or identity violation", false, "supply a typed workflow impact edge")
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if (p.EdgeKind == "depends_on" || p.EdgeKind == "forward_link") && p.EdgeClass == "hard" {
		if cycle, err := workflowEdgeWouldCycle(ctx, tx, event.SubjectID, p.TargetWorkID, p.EdgeKind); err != nil {
			return err
		} else if cycle {
			f := newFailure(KindCycleDetected, "fold_event", "hard workflow edge would create a cycle", false, "choose a non-cyclic edge")
			// Carry the typed offending edge so the agent layer can surface
			// error.violations structurally (D5).
			f.Violations = []string{p.EdgeKind + ":" + event.SubjectID + "->" + p.TargetWorkID}
			return f
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_impact_edges(work_id,edge_id,edge_kind,edge_class,target_work_id,target_kind,severity,recorded_at) VALUES(?,?,?,?,?,?,?,?)`, event.SubjectID, p.EdgeID, p.EdgeKind, p.EdgeClass, p.TargetWorkID, p.TargetKind, p.Severity, event.OccurredAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return workflowProjectionError(err, "cannot record workflow impact edge")
	}
	return nil
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

func WorkflowNoticeID(sourceWorkID string, sourceContractVersion int64, entityKind, entityRef, targetWorkID, severity string) string {
	canonical := "notice-v1\x00" + actorField("source_work_id", sourceWorkID) + actorField("source_contract_version", fmt.Sprintf("%d", sourceContractVersion)) + actorField("entity_kind", entityKind) + actorField("entity_ref", entityRef) + actorField("target_work_id", targetWorkID) + actorField("severity", severity)
	sum := sha256.Sum256([]byte(canonical))
	return "notice:" + hex.EncodeToString(sum[:])
}

// workflowPremiseConfirmedAuthorityTx validates a claimed confirmation
// authority against the verified definition and the active contract's stored
// decision fact: only consequence_v1 engineering admits coordinator authority,
// while legacy and decision-bearing contracts require the verified operator.
// The claimed class and the worker-history rule are checked here, so a forged
// role cannot ride on a caller-supplied authority string.
func workflowPremiseConfirmedAuthorityTx(ctx context.Context, tx *sql.Tx, definition WorkflowDefinition, workID string, contractVersion int64, confirmingActor, claimedAuthority string) error {
	if claimedAuthority != "operator" && claimedAuthority != "coordinator" {
		return newFailure(KindInvalidPayload, "fold_event", "premise confirmation authority is not closed", false, "record operator or coordinator authority")
	}
	var stored sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT decision_domains FROM workflow_contracts WHERE work_id=? AND contract_version=?`, workID, contractVersion).Scan(&stored); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindInvariantViolation, "fold_event", "confirmed workflow contract is missing", false, "reread_entities")
		}
		return err
	}
	if definition.ApprovalPolicy != WorkflowApprovalPolicyConsequenceV1 {
		if claimedAuthority != "operator" {
			return newFailure(KindInvalidOperation, "fold_event", "premise confirmation on this contract requires the verified operator authority", false, "record operator approval")
		}
		return requireActorClass(ctx, tx, confirmingActor, "operator")
	}
	// A consequence_v1 confirmation is bound to the contract version it named.
	// Supersession invalidates it: a confirmation must name the active version.
	active, activeErr := activeWorkflowContractVersion(ctx, tx, workID, "fold_event")
	if activeErr != nil {
		return activeErr
	}
	if active != contractVersion {
		return newFailure(KindInvalidPayload, "fold_event", fmt.Sprintf("premise confirmation names contract version %d, which supersession invalidated; the active version is %d", contractVersion, active), false, "confirm the active contract version")
	}
	if !stored.Valid {
		return newFailure(KindInvalidPayload, "fold_event", "consequence_v1 confirmation requires the contract's stored decision-domain fact; absence is a legacy shape", false, "record the contract's decision domains")
	}
	var domains []string
	if json.Unmarshal([]byte(stored.String), &domains) != nil {
		return newFailure(KindInvariantViolation, "fold_event", "the stored decision-domain fact is malformed", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	decision, err := ClassifyWorkflowApproval(definition, "confirm_premise", WorkflowApprovalFacts{ActiveDecisionDomains: domains})
	if err != nil {
		return err
	}
	if decision.Consent != WorkflowConsentNone && claimedAuthority != "operator" {
		return newFailure(KindInvalidOperation, "fold_event", "decision-bearing premise confirmation requires the verified operator authority", false, "record operator approval")
	}
	if decision.Consent == WorkflowConsentNone && claimedAuthority != "coordinator" {
		return newFailure(KindInvalidOperation, "fold_event", "engineering premise confirmation requires the authenticated coordinator authority; the verified operator confirms decision-bearing work alone", false, "record the coordinator confirmation")
	}
	if claimedAuthority == "coordinator" {
		authority, authorityErr := workflowEvaluationAuthority(ctx, tx, workID, confirmingActor)
		if authorityErr != nil {
			return authorityErr
		}
		if authority == WorkflowWorkerEvaluator {
			return newFailure(KindInvalidOperation, "fold_event", "worker actors cannot record an engineering premise confirmation", false, "return the worker report to the coordinator")
		}
		return requireActorClass(ctx, tx, confirmingActor, "agent")
	}
	return requireActorClass(ctx, tx, confirmingActor, "operator")
}

// workflowFailureAttributionCorrectionActions names the coordinator
// correction completions whose payload may carry the typed failure
// attribution . The derived failure record is deliberately absent:
// the terminal worker.failed event owns the observation and the derived
// record refers to it by event id, never a copied block.
var workflowFailureAttributionCorrectionActions = map[string]bool{
	"reject_worker_result": true,
	"request_correction":   true,
}

func foldWorkflowImpactNoticeRecorded(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowImpactNoticeRecordedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if p.SourceContractVersion <= 0 || !workflowString(p.EntityKind, 128) || !workflowString(p.EntityRef, 128) || !workflowString(p.TargetWorkID, 128) || !workflowString(p.EdgeOwnerWorkID, 128) || !workflowString(p.EdgeID, 128) || (p.Severity != "breaking" && p.Severity != "non-breaking" && p.Severity != "informational") || p.NoticeID != WorkflowNoticeID(event.SubjectID, p.SourceContractVersion, p.EntityKind, p.EntityRef, p.TargetWorkID, p.Severity) {
		return newFailure(KindInvariantViolation, "fold_event", "impact notice identity does not match canonical derivation", false, "recompute notice_id from its six identity fields")
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_impact_notices(notice_id,source_work_id,source_contract_version,entity_kind,entity_ref,target_work_id,edge_owner_work_id,edge_id,old_hash,new_hash,severity,recorded_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, p.NoticeID, event.SubjectID, p.SourceContractVersion, p.EntityKind, p.EntityRef, p.TargetWorkID, p.EdgeOwnerWorkID, p.EdgeID, valueOrNil(p.OldHash), valueOrNil(p.NewHash), p.Severity, event.OccurredAt.UTC().Format(time.RFC3339Nano))
	return workflowProjectionError(err, "cannot record workflow impact notice")
}

func valueOrNil(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
func foldWorkflowConditionAdded(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowConditionAddedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if !workflowString(p.ConditionID, 128) || !workflowString(p.AwaitRef, 128) || !workflowString(p.ResolutionAuthority, 128) || !slices.Contains([]string{"pr_merge", "ci_result", "timer", "human_approval", "remote_work_state"}, p.AwaitType) {
		return newFailure(KindInvalidPayload, "fold_event", "condition_added contains an invalid closed await type", false, "use a typed external condition")
	}
	if p.ExpectedWithinSeconds < 0 || p.ExpectedWithinSeconds > 31536000 {
		return newFailure(KindInvalidPayload, "fold_event", "expected_within_seconds must be a positive bound of at most one year", false, "declare a realistic wait bound or omit it")
	}
	if _, err := conditionAuthorityOperation(ctx, tx, p.ResolutionAuthority, event.SubjectID); err != nil {
		return err
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_external_conditions(work_id,condition_id,await_type,await_ref,resolution_authority,condition_state,recorded_at,expected_within_seconds) VALUES(?,?,?,?,?,'open',?,?)`, event.SubjectID, p.ConditionID, p.AwaitType, p.AwaitRef, p.ResolutionAuthority, event.OccurredAt.UTC().Format(time.RFC3339Nano), nullableInt(p.ExpectedWithinSeconds))
	return workflowProjectionError(err, "cannot record external condition")
}
func foldWorkflowConditionResolved(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowConditionResolvedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if !workflowString(p.ConditionID, 128) || !workflowList(p.ResolutionEvidence, 32, 1) || !workflowString(p.ResolvedByEvent, 128) {
		return newFailure(KindInvalidPayload, "fold_event", "condition resolution is incomplete", false, "supply owning-authority evidence")
	}
	var authority string
	if err := tx.QueryRowContext(ctx, `SELECT resolution_authority FROM workflow_external_conditions WHERE work_id=? AND condition_id=? AND condition_state='open'`, event.SubjectID, p.ConditionID).Scan(&authority); err != nil {
		return newFailure(KindInvalidOperation, "fold_event", "condition is missing or not open", false, "resolve only an open condition")
	}
	if err := verifyConditionEvidence(ctx, tx, authority, event.SubjectID, p.ResolutionEvidence); err != nil {
		return err
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE workflow_external_conditions SET condition_state='resolved',resolution_evidence=?,resolved_by_event=?,resolved_at=? WHERE work_id=? AND condition_id=? AND condition_state='open'`, workflowJSON(p.ResolutionEvidence), p.ResolvedByEvent, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.SubjectID, p.ConditionID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return newFailure(KindInvalidOperation, "fold_event", "condition is missing or already terminal", false, "resolve only an open condition")
	}
	return nil
}
func foldWorkflowConditionCancelled(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowConditionCancelledPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if !workflowString(p.ConditionID, 128) || p.CancellationAuthority != "operator" || !workflowList(p.CancellationEvidence, 32, 1) || !workflowString(p.CancelledByEvent, 128) {
		return newFailure(KindInvalidPayload, "fold_event", "condition cancellation is incomplete", false, "require operator cancellation evidence")
	}
	if err := requireActorClass(ctx, tx, event.Actor, "operator"); err != nil {
		return err
	}
	var authority string
	if err := tx.QueryRowContext(ctx, `SELECT resolution_authority FROM workflow_external_conditions WHERE work_id=? AND condition_id=? AND condition_state='open'`, event.SubjectID, p.ConditionID).Scan(&authority); err != nil {
		return newFailure(KindInvalidOperation, "fold_event", "condition is missing or not open", false, "cancel only an open condition")
	}
	if err := verifyConditionEvidence(ctx, tx, authority, event.SubjectID, p.CancellationEvidence); err != nil {
		return err
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE workflow_external_conditions SET condition_state='cancelled',cancellation_authority='operator',cancellation_evidence=?,cancelled_by_event=?,cancelled_at=? WHERE work_id=? AND condition_id=? AND condition_state='open'`, workflowJSON(p.CancellationEvidence), p.CancelledByEvent, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.SubjectID, p.ConditionID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return newFailure(KindInvalidOperation, "fold_event", "condition is missing or already terminal", false, "cancel only an open condition")
	}
	return nil
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
	if p.TerminalState != "completed" && p.TerminalState != "cancelled" && p.TerminalState != "superseded" || !slices.Contains([]string{"ok", "outcome_mismatch", "insufficient_evidence"}, p.FinalVerdictKind) || !workflowString(p.VerdictActorRef, 70) || p.EvidenceCount < 0 || !workflowDigest(p.ChangedRefsDigest) || !slices.Contains([]string{"breaking", "non-breaking"}, p.ImpactVerdict) || !workflowList(p.Warnings, 16, 0) {
		return newFailure(KindInvalidPayload, "fold_event", "completed has invalid terminal metadata", false, "supply closed terminal metadata including impact_verdict")
	}
	if _, err := VerifyWorkflowInstanceDefinitionTx(ctx, tx, scopeDefinitionRegistry(scope), event.SubjectID); err != nil {
		return err
	}
	if err := requireActor(ctx, tx, event.Actor); err != nil {
		return err
	}
	if err := workflowCompletionCallerAuthority(ctx, tx, event.SubjectID, event.Actor); err != nil {
		return err
	}
	if err := requireActor(ctx, tx, p.VerdictActorRef); err != nil {
		return err
	}
	// An outside-repair hold cannot acquire workflow completion evidence.
	if err := requireNoOutsideRepairTx(ctx, tx, event.SubjectID, "fold_event"); err != nil {
		return err
	}
	// A later evaluator's verdict cannot satisfy this completion's
	// evaluator-equality rule.
	verdict, err := latestWorkflowVerdictBefore(ctx, tx, event.SubjectID, event.Seq)
	if err != nil {
		return err
	}
	if verdict == nil || verdict.VerdictActorRef != p.VerdictActorRef {
		return newFailure(KindUnauthorized, "fold_event", "completed verdict actor is not the authenticated recorded evaluator", false, "use the persisted workflow verdict actor")
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET instance_state=?,completed_at=?,completion_payload=?,completion_occurred_at=? WHERE work_id=? AND instance_state NOT IN ('completed','cancelled','superseded','outside_repair')`, p.TerminalState, event.OccurredAt.UTC().Format(time.RFC3339Nano), string(event.Payload), event.OccurredAt.UTC().Format(time.RFC3339Nano), event.SubjectID)
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

// nullableInt stores 0 as NULL so "no declared bound" and "bound of zero"
// stay distinguishable at the storage boundary.
func nullableInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
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
