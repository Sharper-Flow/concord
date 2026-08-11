package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Workflow event names are deliberately closed. Adding one is a contract change.
const (
	WorkflowDefinitionSelected   = "workflow.definition_selected"
	WorkflowContractApproved     = "workflow.contract_approved"
	WorkflowContractSuperseded   = "workflow.contract_superseded"
	WorkflowCandidateSetRevised  = "workflow.candidate_set_revised"
	WorkflowActorRecorded        = "workflow.actor_recorded"
	WorkflowActionStarted        = "workflow.action_started"
	WorkflowActionCheckpointed   = "workflow.action_checkpointed"
	WorkflowActionCompleted      = "workflow.action_completed"
	WorkflowActionFailed         = "workflow.action_failed"
	WorkflowEvidenceBound        = "workflow.evidence_bound"
	WorkflowVerdictRecorded      = "workflow.verdict_recorded"
	WorkflowPremiseConfirmed     = "workflow.premise_confirmed"
	WorkflowSuccessorLinked      = "workflow.successor_linked"
	WorkflowImpactDeclared       = "workflow.impact_declared"
	WorkflowImpactNoticeRecorded = "workflow.impact_notice_recorded"
	WorkflowConditionAdded       = "workflow.condition_added"
	WorkflowConditionResolved    = "workflow.condition_resolved"
	WorkflowConditionCancelled   = "workflow.condition_cancelled"
	WorkflowCompleted            = "workflow.completed"
)

type WorkflowVersionFields struct {
	WorkID           string `json:"work_id"`
	ExpectedVersion  *int64 `json:"expected_version"`
	ResultingVersion *int64 `json:"resulting_version"`
}

type workflowDefinitionSelectedPayload struct {
	WorkflowVersionFields
	Ref      string `json:"ref"`
	Version  int64  `json:"version"`
	Digest   string `json:"digest"`
	WorkKind string `json:"work_kind"`
}

type workflowContractApprovedPayload struct {
	WorkflowVersionFields
	ContractVersion    int64           `json:"contract_version"`
	Premise            string          `json:"premise"`
	OutcomeKind        string          `json:"outcome_kind"`
	OutcomePayload     json.RawMessage `json:"outcome_payload"`
	RequiredEvidence   []string        `json:"required_evidence"`
	RouteConventions   []string        `json:"route_conventions"`
	SpecMandate        []string        `json:"spec_mandate"`
	LawModifies        []string        `json:"law_modifies"`
	LawBoundaryVersion int             `json:"law_boundary_version,omitempty"`
	RigorClass         string          `json:"rigor_class"`
	ConsequenceClass   string          `json:"consequence_class,omitempty"`
	PremiseHash        string          `json:"premise_hash,omitempty"`
	OutcomeHash        string          `json:"outcome_hash,omitempty"`
}

type workflowContractSupersededPayload struct {
	WorkflowVersionFields
	PreviousContractVersion int64    `json:"previous_contract_version"`
	NewContractVersion      int64    `json:"new_contract_version"`
	SupersedeReason         string   `json:"supersede_reason"`
	AuditEvidence           []string `json:"audit_evidence"`
}

type workflowCandidateSetRevisedPayload struct {
	WorkflowVersionFields
	ContractVersion int64    `json:"contract_version"`
	CandidateKind   string   `json:"candidate_kind"`
	CandidateRef    string   `json:"candidate_ref"`
	Added           []string `json:"added"`
	Removed         []string `json:"removed"`
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
	ActionID           string   `json:"action_id,omitempty"`
	StepID             string   `json:"step_id"`
	AttemptEpoch       int64    `json:"attempt_epoch"`
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

type workflowEvidenceBoundPayload struct {
	WorkflowVersionFields
	EvidenceKind        string `json:"evidence_kind"`
	ImmutableSubjectRef string `json:"immutable_subject_ref"`
	ProducerID          string `json:"producer_id"`
	ProducerRunRef      string `json:"producer_run_ref"`
	ProducerWatermark   string `json:"producer_watermark"`
	ObservedAt          string `json:"observed_at"`
}

type workflowVerdictRecordedPayload struct {
	WorkflowVersionFields
	ContractVersion          int64    `json:"contract_version"`
	PredicateID              string   `json:"predicate_id"`
	VerdictKind              string   `json:"verdict_kind"`
	VerdictActorRef          string   `json:"verdict_actor_ref"`
	EvaluationEvidence       []string `json:"evaluation_evidence"`
	IncomparableWithApproved bool     `json:"incomparable_with_approved"`
}

type workflowPremiseConfirmedPayload struct {
	WorkflowVersionFields
	ContractVersion    int64  `json:"contract_version"`
	ConfirmingActorRef string `json:"confirming_actor_ref"`
}

type workflowSuccessorLinkedPayload struct {
	WorkflowVersionFields
	SuccessorWorkID string `json:"successor_work_id"`
	RelationKind    string `json:"relation_kind"`
	SuccessorKind   string `json:"successor_kind"`
	DefinitionRef   string `json:"definition_ref"`
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
	Warnings          []string `json:"warnings,omitempty"`
}

var workflowKinds = map[string]bool{
	"implementation": true, "break_fix": true, "research": true,
	"architecture_spike": true, "ops_runbook": true, "static_analysis": true,
	"generic_one_off": true,
}

var workflowAdvancementEventKinds = map[string]struct{}{
	WorkflowDefinitionSelected: {}, WorkflowContractApproved: {}, WorkflowContractSuperseded: {},
	WorkflowCandidateSetRevised: {}, WorkflowActorRecorded: {}, WorkflowActionStarted: {},
	WorkflowActionCheckpointed: {}, WorkflowActionCompleted: {}, WorkflowActionFailed: {},
	WorkflowEvidenceBound: {}, WorkflowVerdictRecorded: {}, WorkflowPremiseConfirmed: {},
	WorkflowSuccessorLinked: {}, WorkflowImpactDeclared: {}, WorkflowImpactNoticeRecorded: {},
	WorkflowConditionAdded: {}, WorkflowConditionResolved: {}, WorkflowConditionCancelled: {},
	WorkflowCompleted: {},
}

func isWorkflowAdvancementEvent(kind string) bool {
	_, ok := workflowAdvancementEventKinds[kind]
	return ok
}

func workflowDispatcherRequired(kind string) error {
	if kind == WorkflowCompleted {
		return workflowCompletionRequired()
	}
	return newFailure(KindInvalidOperation, "apply_operation", "workflow event "+kind+" is reserved for an authoritative workflow route", false, "dispatch the workflow action or use the workflow initialization/completion entry point")
}

func workflowRegistration(f projectionMutation) EventKindRegistration {
	return EventKindRegistration{CurrentVersion: 1, MinSupported: 1, Upcasters: map[int]Upcaster{}, Fold: f}
}

func init() {
	eventKindRegistry[WorkflowDefinitionSelected] = workflowRegistration(foldWorkflowDefinitionSelected)
	eventKindRegistry[WorkflowContractApproved] = workflowRegistration(foldWorkflowContractApproved)
	eventKindRegistry[WorkflowContractSuperseded] = workflowRegistration(foldWorkflowContractSuperseded)
	eventKindRegistry[WorkflowCandidateSetRevised] = workflowRegistration(foldWorkflowCandidateSetRevised)
	eventKindRegistry[WorkflowActorRecorded] = workflowRegistration(foldWorkflowActorRecorded)
	eventKindRegistry[WorkflowActionStarted] = workflowRegistration(foldWorkflowActionStarted)
	eventKindRegistry[WorkflowActionCheckpointed] = workflowRegistration(foldWorkflowActionCheckpointed)
	eventKindRegistry[WorkflowActionCompleted] = workflowRegistration(foldWorkflowActionCompleted)
	eventKindRegistry[WorkflowActionFailed] = workflowRegistration(foldWorkflowActionFailed)
	eventKindRegistry[WorkflowEvidenceBound] = workflowRegistration(foldWorkflowEvidenceBound)
	eventKindRegistry[WorkflowVerdictRecorded] = workflowRegistration(foldWorkflowVerdictRecorded)
	eventKindRegistry[WorkflowPremiseConfirmed] = workflowRegistration(foldWorkflowPremiseConfirmed)
	eventKindRegistry[WorkflowSuccessorLinked] = workflowRegistration(foldWorkflowSuccessorLinked)
	eventKindRegistry[WorkflowImpactDeclared] = workflowRegistration(foldWorkflowImpactDeclared)
	eventKindRegistry[WorkflowImpactNoticeRecorded] = workflowRegistration(foldWorkflowImpactNoticeRecorded)
	eventKindRegistry[WorkflowConditionAdded] = workflowRegistration(foldWorkflowConditionAdded)
	eventKindRegistry[WorkflowConditionResolved] = workflowRegistration(foldWorkflowConditionResolved)
	eventKindRegistry[WorkflowConditionCancelled] = workflowRegistration(foldWorkflowConditionCancelled)
	eventKindRegistry[WorkflowCompleted] = workflowRegistration(foldWorkflowCompleted)
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
	decoder := json.NewDecoder(strings.NewReader(string(event.Payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		failure := wrapFailure(KindInvalidPayload, "fold_event", fmt.Sprintf("event %s payload does not match its workflow kind", event.EventID), false, "repair the workflow event payload", err)
		failure.Stage = StageDecode
		return failure
	}
	return nil
}

func validateWorkflowPayloadShape(event Event) error {
	var target any
	switch event.Kind {
	case WorkflowDefinitionSelected:
		target = &workflowDefinitionSelectedPayload{}
	case WorkflowContractApproved:
		target = &workflowContractApprovedPayload{}
	case WorkflowContractSuperseded:
		target = &workflowContractSupersededPayload{}
	case WorkflowCandidateSetRevised:
		target = &workflowCandidateSetRevisedPayload{}
	case WorkflowActorRecorded:
		target = &workflowActorRecordedPayload{}
	case WorkflowActionStarted:
		target = &workflowActionStartedPayload{}
	case WorkflowActionCheckpointed:
		target = &workflowActionCheckpointedPayload{}
	case WorkflowActionCompleted:
		target = &workflowActionCompletedPayload{}
	case WorkflowActionFailed:
		target = &workflowActionFailedPayload{}
	case WorkflowEvidenceBound:
		target = &workflowEvidenceBoundPayload{}
	case WorkflowVerdictRecorded:
		target = &workflowVerdictRecordedPayload{}
	case WorkflowPremiseConfirmed:
		target = &workflowPremiseConfirmedPayload{}
	case WorkflowSuccessorLinked:
		target = &workflowSuccessorLinkedPayload{}
	case WorkflowImpactDeclared:
		target = &workflowImpactDeclaredPayload{}
	case WorkflowImpactNoticeRecorded:
		target = &workflowImpactNoticeRecordedPayload{}
	case WorkflowConditionAdded:
		target = &workflowConditionAddedPayload{}
	case WorkflowConditionResolved:
		target = &workflowConditionResolvedPayload{}
	case WorkflowConditionCancelled:
		target = &workflowConditionCancelledPayload{}
	case WorkflowCompleted:
		target = &workflowCompletedPayload{}
	default:
		return unknownEventKind(event.Kind)
	}
	if err := decodeWorkflowPayload(event, target); err != nil {
		return err
	}
	var fields WorkflowVersionFields
	switch payload := target.(type) {
	case *workflowDefinitionSelectedPayload:
		fields = payload.WorkflowVersionFields
	case *workflowContractApprovedPayload:
		fields = payload.WorkflowVersionFields
	case *workflowContractSupersededPayload:
		fields = payload.WorkflowVersionFields
	case *workflowCandidateSetRevisedPayload:
		fields = payload.WorkflowVersionFields
	case *workflowActorRecordedPayload:
		fields = payload.WorkflowVersionFields
	case *workflowActionStartedPayload:
		fields = payload.WorkflowVersionFields
	case *workflowActionCheckpointedPayload:
		fields = payload.WorkflowVersionFields
	case *workflowActionCompletedPayload:
		fields = payload.WorkflowVersionFields
	case *workflowActionFailedPayload:
		fields = payload.WorkflowVersionFields
	case *workflowEvidenceBoundPayload:
		fields = payload.WorkflowVersionFields
	case *workflowVerdictRecordedPayload:
		fields = payload.WorkflowVersionFields
	case *workflowPremiseConfirmedPayload:
		fields = payload.WorkflowVersionFields
	case *workflowSuccessorLinkedPayload:
		fields = payload.WorkflowVersionFields
	case *workflowImpactDeclaredPayload:
		fields = payload.WorkflowVersionFields
	case *workflowImpactNoticeRecordedPayload:
		fields = payload.WorkflowVersionFields
	case *workflowConditionAddedPayload:
		fields = payload.WorkflowVersionFields
	case *workflowConditionResolvedPayload:
		fields = payload.WorkflowVersionFields
	case *workflowConditionCancelledPayload:
		fields = payload.WorkflowVersionFields
	case *workflowCompletedPayload:
		fields = payload.WorkflowVersionFields
	}
	return workflowBase(event, fields)
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

func workflowString(value string, max int) bool { return len(value) >= 2 && len(value) <= max }
func workflowList(values []string, max, min int) bool {
	if len(values) < min || len(values) > max {
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
		return versionConflict(SubjectWorkItem, event.SubjectID, *fields.ExpectedVersion, 0, false)
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

func workflowDigest(value string, prefix string) bool {
	return len(value) == 71 && strings.HasPrefix(value, prefix)
}

func foldWorkflowDefinitionSelected(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowDefinitionSelectedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if !workflowString(p.Ref, 128) || p.Version <= 0 || !workflowDigest(p.Digest, "sha256:") || !workflowKinds[p.WorkKind] {
		return newFailure(KindInvalidPayload, "fold_event", "definition_selected contains an invalid definition pin", false, "supply a closed definition reference, version, digest, and family")
	}
	if _, err := VerifyWorkflowDefinitionPin(BuiltinWorkflowRegistry(), WorkflowDefinitionPin{Ref: p.Ref, Version: p.Version, Digest: p.Digest}); err != nil {
		return err
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	var started int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND seq <= ?`, event.SubjectID, WorkflowActionStarted, event.Seq).Scan(&started); err != nil {
		return err
	}
	if started > 0 {
		return newFailure(KindInvalidOperation, "fold_event", "definition cannot change after execution starts", false, "supersede the workflow contract instead")
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_instances(work_id,definition_ref,definition_version,definition_digest,current_step,instance_state) VALUES(?,?,?,?,?,'planned') ON CONFLICT(work_id) DO UPDATE SET definition_ref=excluded.definition_ref,definition_version=excluded.definition_version,definition_digest=excluded.definition_digest`, event.SubjectID, p.Ref, p.Version, p.Digest, "start")
	if err != nil {
		return workflowProjectionError(err, "cannot record workflow definition")
	}
	return nil
}

func workflowProjectionError(err error, message string) error {
	if err == nil {
		return nil
	}
	if isUniqueViolation(err) {
		return newFailure(KindProjectionConflict, "fold_event", message, false, "append a new workflow version")
	}
	return wrapFailure(KindUnavailable, "fold_event", fmt.Sprintf("%s: %v", message, err), true, "retry once the database is writable", err)
}

func foldWorkflowContractApproved(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowContractApprovedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if p.ContractVersion <= 0 || !workflowString(p.Premise, 4096) || !workflowList(p.RequiredEvidence, 7, 0) || !workflowList(p.RouteConventions, 16, 0) || !workflowList(p.SpecMandate, 32, 0) || !workflowList(p.LawModifies, 32, 0) || !validateWorkflowOutcome(p.OutcomePayload, p.OutcomeKind) {
		return newFailure(KindInvalidPayload, "fold_event", "contract_approved contains invalid contract fields", false, "supply a strict closed workflow outcome and bounded contract fields")
	}
	if p.LawBoundaryVersion != 0 && p.LawBoundaryVersion != 1 {
		return newFailure(KindInvalidPayload, "fold_event", "contract_approved law boundary version is invalid", false, "use the supported law boundary version")
	}
	if p.LawModifies == nil {
		p.LawModifies = []string{}
	}
	if p.SpecMandate == nil {
		p.SpecMandate = []string{}
	}
	if p.LawBoundaryVersion == 1 {
		if err := validateLawModificationSubset(p.SpecMandate, p.LawModifies); err != nil {
			return err
		}
		if err := checkMandatedLawsTx(ctx, tx, event.SubjectID, p.SpecMandate, p.LawModifies, true); err != nil {
			return err
		}
	}
	if p.ConsequenceClass != "internal_sqlite" && p.ConsequenceClass != "cross_authority" && p.ConsequenceClass != "external_effect" {
		return newFailure(KindInvalidPayload, "fold_event", "contract consequence class is not closed", false, "use internal_sqlite, cross_authority, or external_effect")
	}
	// The generic outcome union is only syntax.  Meaning is pinned by the
	// selected definition and must be checked before the immutable contract is
	// written.
	if registered, err := VerifyWorkflowInstanceDefinitionTx(ctx, tx, BuiltinWorkflowRegistry(), event.SubjectID); err != nil {
		return err
	} else {
		var predicate OutcomePredicate
		if err := json.Unmarshal(p.OutcomePayload, &predicate); err != nil {
			return newFailure(KindInvalidPayload, "fold_event", "contract outcome payload is malformed", false, "supply the strict registered outcome predicate")
		}
		if err := ValidateWorkflowPredicateForDefinition(registered.Definition, predicate); err != nil {
			return err
		}
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if err := requireActor(ctx, tx, event.Actor); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_contracts(work_id,contract_version,premise,outcome_kind,outcome_payload,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, event.SubjectID, p.ContractVersion, p.Premise, p.OutcomeKind, string(p.OutcomePayload), p.ConsequenceClass, workflowJSON(p.RequiredEvidence), workflowJSON(p.RouteConventions), event.OccurredAt.UTC().Format(time.RFC3339Nano), event.Actor, workflowJSON(p.SpecMandate), workflowJSON(p.LawModifies), p.LawBoundaryVersion, p.RigorClass)
	if err != nil {
		return workflowProjectionError(err, "cannot record immutable workflow contract")
	}
	return nil
}

func foldWorkflowContractSuperseded(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowContractSupersededPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if p.PreviousContractVersion <= 0 || p.NewContractVersion != p.PreviousContractVersion+1 || !workflowString(p.SupersedeReason, 4096) || !workflowList(p.AuditEvidence, 32, 1) {
		return newFailure(KindInvalidPayload, "fold_event", "contract_superseded has invalid version, reason, or audit evidence", false, "supersede one contract with a consecutive version and evidence")
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contracts(work_id,contract_version,premise,outcome_kind,outcome_payload,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) SELECT work_id,?,premise,outcome_kind,outcome_payload,consequence_class,required_evidence,route_conventions,?,?,spec_mandate,law_modifies,law_boundary_version,rigor_class FROM workflow_contracts WHERE work_id=? AND contract_version=? AND superseded_by IS NULL AND NOT EXISTS (SELECT 1 FROM workflow_contracts WHERE work_id=? AND contract_version=?)`, p.NewContractVersion, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.Actor, event.SubjectID, p.PreviousContractVersion, event.SubjectID, p.NewContractVersion); err != nil {
		// The revision event carries the audit and version identity; the
		// approved predicate remains immutable until the next explicit approval.
		return workflowProjectionError(err, "cannot create superseding workflow contract")
	}
	result, err := tx.ExecContext(ctx, `UPDATE workflow_contracts SET superseded_by=? WHERE work_id=? AND contract_version=? AND superseded_by IS NULL`, p.NewContractVersion, event.SubjectID, p.PreviousContractVersion)
	if err != nil {
		return workflowProjectionError(err, "cannot supersede workflow contract")
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return newFailure(KindProjectionNotFound, "fold_event", "previous workflow contract does not exist or is already superseded", false, "reload the current contract")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET instance_state='planned',current_step='planning' WHERE work_id=?`, event.SubjectID); err != nil {
		return workflowProjectionError(err, "cannot return workflow to planning after contract supersession")
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
	if p.ContractVersion <= 0 || !contains([]string{"work_item", "product", "project"}, p.CandidateKind) || !workflowString(p.CandidateRef, 128) || !workflowList(p.Added, 100, 0) || !workflowList(p.Removed, 100, 0) || len(p.Added) == 0 && len(p.Removed) == 0 || overlap(p.Added, p.Removed) {
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

func foldWorkflowActionStarted(ctx context.Context, tx *sql.Tx, event Event) error {
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
	if err := requireActor(ctx, tx, p.ActorRef); err != nil {
		return err
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	instanceState := "running"
	if p.StepID == "planning" {
		instanceState = "planned"
	}
	result, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET current_step=?,instance_state=?,execution_actor_ref=?,started_at=coalesce(started_at,?) WHERE work_id=?`, p.StepID, instanceState, p.ActorRef, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.SubjectID)
	if err != nil {
		return workflowProjectionError(err, "cannot start workflow action")
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return newFailure(KindProjectionNotFound, "fold_event", "workflow instance does not exist", false, "select a workflow definition before starting an action")
	}
	return nil
}

func foldWorkflowActionCheckpointed(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowActionCheckpointedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if !workflowString(p.StepID, 128) || p.AttemptEpoch <= 0 || p.AttemptEpoch > 2147483647 || !workflowString(p.StepKind, 32) || len(p.ResumeCursor) > 2048 || len(p.CheckpointPayload) > 16*1024 || p.RequestID == "" {
		return newFailure(KindInvalidPayload, "fold_event", "action_checkpointed has invalid checkpoint fields", false, "supply a typed bounded checkpoint")
	}
	if p.StepKind != "internal_sqlite" && p.StepKind != "cross_authority" && p.StepKind != "external_effect" && p.StepKind != "human_checkpoint" {
		return newFailure(KindInvalidPayload, "fold_event", "checkpoint step kind is not closed", false, "use a closed workflow step kind")
	}
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
	_, err = tx.ExecContext(ctx, `UPDATE workflow_instances SET instance_state='running',last_checkpoint_at=? WHERE work_id=?`, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.SubjectID)
	return err
}

func foldWorkflowDecisionRecord(ctx context.Context, tx *sql.Tx, event Event, raw json.RawMessage) error {
	var checkpoint struct {
		ActionID   string `json:"action_id"`
		Checkpoint *struct {
			ActionID     string   `json:"action_id"`
			Question     string   `json:"question"`
			Options      []string `json:"options_considered"`
			Decision     string   `json:"decision"`
			Rationale    string   `json:"rationale"`
			Consequences []string `json:"consequences"`
			Inputs       []string `json:"inputs"`
			POCFindings  string   `json:"poc_findings"`
		} `json:"checkpoint,omitempty"`
		Question     string   `json:"question"`
		Options      []string `json:"options_considered"`
		Decision     string   `json:"decision"`
		Rationale    string   `json:"rationale"`
		Consequences []string `json:"consequences"`
		Inputs       []string `json:"inputs"`
		POCFindings  string   `json:"poc_findings"`
		Supersedes   *string  `json:"supersedes"`
		SupersededBy *string  `json:"superseded_by"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &checkpoint) != nil {
		return nil
	}
	if checkpoint.ActionID == "" && checkpoint.Checkpoint != nil {
		checkpoint.ActionID = checkpoint.Checkpoint.ActionID
		checkpoint.Question = checkpoint.Checkpoint.Question
		checkpoint.Options = checkpoint.Checkpoint.Options
		checkpoint.Decision = checkpoint.Checkpoint.Decision
		checkpoint.Rationale = checkpoint.Checkpoint.Rationale
		checkpoint.Consequences = checkpoint.Checkpoint.Consequences
		checkpoint.Inputs = checkpoint.Checkpoint.Inputs
		checkpoint.POCFindings = checkpoint.Checkpoint.POCFindings
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

func foldWorkflowActionCompleted(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowActionCompletedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if (p.ActionID != "" && !workflowString(p.ActionID, 128)) || !workflowString(p.StepID, 128) || p.AttemptEpoch <= 0 || !workflowList(p.ResultEvidenceRefs, 32, 0) || !workflowList(p.ChangedRefs, 32, 0) {
		return newFailure(KindInvalidPayload, "fold_event", "action_completed has invalid result fields", false, "supply bounded action result references")
	}
	if err := requireActor(ctx, tx, p.ActorRef); err != nil {
		return err
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET instance_state='running',last_checkpoint_at=? WHERE work_id=?`, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.SubjectID)
	if err != nil || p.ActionID == "" || !workflowActionAdvancesStep(p.ActionID) {
		return err
	}
	entry, err := VerifyWorkflowInstanceDefinitionTx(ctx, tx, BuiltinWorkflowRegistry(), event.SubjectID)
	if err != nil {
		return err
	}
	if next := workflowNextStep(entry.Definition, p.StepID); next != "" {
		_, err = tx.ExecContext(ctx, `UPDATE workflow_instances SET current_step=? WHERE work_id=?`, next, event.SubjectID)
	}
	return err
}

func workflowActionAdvancesStep(action string) bool {
	return !strings.HasPrefix(action, "start_") && !strings.HasPrefix(action, "run_") && !strings.HasPrefix(action, "rollback_") && !strings.HasPrefix(action, "checkpoint_") && action != "bind_evidence" && action != "declare_impact" && action != "link_successor" && action != "record_decision" && action != "record_verdict" && action != "accept_decision" && action != "cleanup_run" && action != "complete"
}

func workflowNextStep(definition WorkflowDefinition, current string) string {
	for _, edge := range definition.StepGraph.Edges {
		if edge.From == current && edge.Kind != WorkflowEdgeRetry {
			return edge.To
		}
	}
	return ""
}

func foldWorkflowActionFailed(ctx context.Context, tx *sql.Tx, event Event) error {
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
	if !contains([]string{"unknown_scope", "ambiguous_scope", "stale_context", "unauthorized", "approval_required", "approval_invalid", "version_conflict", "idempotency_conflict", "operation_conflict", "invalid_transition", "invalid_relation", "invariant_violation", "missing_evidence", "not_terminal", "outcome_mismatch", "stale_requires_review", "degraded_not_allowed", "unreachable", "invalid_cursor", "limit_exceeded", "budget_refused", "invalid_input", "cancelled", "timeout", "transport_failure", "malformed_response", "internal_error"}, p.FailureKind) {
		return newFailure(KindInvalidPayload, "fold_event", "action_failed failure kind is not closed", false, "use a TS7 typed error kind")
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
	_, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET instance_state=? WHERE work_id=?`, state, event.SubjectID)
	return err
}

func foldWorkflowEvidenceBound(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowEvidenceBoundPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if !contains([]string{"verification", "review", "approval", "commit", "durable_note", "native_run", "artifact"}, p.EvidenceKind) || !workflowString(p.ImmutableSubjectRef, 256) || !workflowString(p.ProducerID, 128) || !workflowString(p.ProducerRunRef, 128) || !workflowString(p.ProducerWatermark, 128) || p.ObservedAt == "" {
		return newFailure(KindInvalidPayload, "fold_event", "evidence_bound has invalid evidence identity", false, "supply a complete immutable evidence binding")
	}
	var authoritative int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM durable_operations WHERE op_id=? AND work_id=? AND principal_ref=? AND request_id=? AND result_kind='completed' AND EXISTS (SELECT 1 FROM json_each(durable_operations.evidence_refs) WHERE value=?)`, p.ProducerRunRef, event.SubjectID, p.ProducerID, p.ProducerWatermark, p.ImmutableSubjectRef).Scan(&authoritative); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot verify evidence authority", true, "retry once the evidence authority is readable", err)
	}
	if authoritative != 1 {
		return newFailure(KindInvariantViolation, "fold_event", "evidence binding is not backed by the existing durable-operation evidence authority", false, "complete the producer operation with the immutable evidence reference first")
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
	if err := requireActor(ctx, tx, p.VerdictActorRef); err != nil {
		return err
	}
	if p.VerdictActorRef != event.Actor {
		return newFailure(KindUnauthorized, "fold_event", "verdict actor must match the authenticated event actor", false, "record the verdict through the authenticated evaluator invocation")
	}
	return advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields)
}

func foldWorkflowPremiseConfirmed(ctx context.Context, tx *sql.Tx, event Event) error {
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
	var class string
	if p.ConfirmingActorRef != event.Actor || tx.QueryRowContext(ctx, `SELECT actor_class FROM workflow_actors WHERE actor_ref=?`, event.Actor).Scan(&class) != nil || class != "operator" {
		return newFailure(KindInvalidOperation, "fold_event", "premise must be confirmed by an operator actor", false, "record operator approval")
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_premise_confirmations(work_id,contract_version,confirmed_by,confirmed_at) VALUES(?,?,?,?)`, event.SubjectID, p.ContractVersion, p.ConfirmingActorRef, event.OccurredAt.UTC().Format(time.RFC3339Nano))
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
	if p.DefinitionRef != "" && p.DefinitionRef != definitionRef {
		return newFailure(KindInvalidRelation, "fold_event", "successor definition does not match the authoritative workflow instance", false, "reread the successor workflow definition")
	}
	var sourceDefinitionRef string
	if err := tx.QueryRowContext(ctx, `SELECT definition_ref FROM workflow_instances WHERE work_id=?`, event.SubjectID).Scan(&sourceDefinitionRef); err != nil {
		return workflowProjectionError(err, "cannot read source workflow definition")
	}
	source, err := BuiltinWorkflowDefinitionForRef(sourceDefinitionRef)
	if err != nil {
		return err
	}
	if !containsWorkKind(source.Definition.CompositionRules.AllowedSuccessorWorkKinds, WorkKind(successorKind)) {
		return newFailure(KindInvalidRelation, "fold_event", "successor family is not allowed by the source workflow composition", false, "use an allowed forward-linked successor family")
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if cycle, err := workflowEdgeWouldCycle(ctx, tx, event.SubjectID, p.SuccessorWorkID, "forward_link"); err != nil {
		return err
	} else if cycle {
		return newFailure(KindCycleDetected, "fold_event", "forward link would create a cycle", false, "choose a non-cyclic successor")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workflow_impact_edges(work_id,edge_id,edge_kind,edge_class,target_work_id,target_kind,severity,recorded_at) VALUES(?,?,?,?,?,?,?,?)`, event.SubjectID, event.EventID, "forward_link", "hard", p.SuccessorWorkID, "work_item", "informational", event.OccurredAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return workflowProjectionError(err, "cannot record successor link")
	}
	return insertWorkflowForwardRelation(ctx, tx, event, p.SuccessorWorkID)
}

func containsWorkKind(values []WorkKind, want WorkKind) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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
			return newFailure(KindCycleDetected, "fold_event", "hard workflow edge would create a cycle", false, "choose a non-cyclic edge")
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
	var relationID int64
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE seq <= ? AND kind IN ('relation.added','work.superseded','work.reopened_from_superseded','epic_entry.added','workflow.successor_linked')`, event.Seq).Scan(&relationID); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot assign a deterministic forward-link identity", true, "retry once the event log is readable", err)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO relations(id,work_id_from,work_id_to,kind,created_at) VALUES(?,?,?,?,?)`, relationID, event.SubjectID, successor, "forward_link", event.OccurredAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return workflowProjectionError(err, "cannot record forward-link relation")
	}
	return nil
}

func WorkflowNoticeID(sourceWorkID string, sourceContractVersion int64, entityKind, entityRef, targetWorkID, severity string) string {
	canonical := "notice-v1\x00" + actorField("source_work_id", sourceWorkID) + actorField("source_contract_version", fmt.Sprintf("%d", sourceContractVersion)) + actorField("entity_kind", entityKind) + actorField("entity_ref", entityRef) + actorField("target_work_id", targetWorkID) + actorField("severity", severity)
	sum := sha256.Sum256([]byte(canonical))
	return "notice:" + hex.EncodeToString(sum[:])
}

func foldWorkflowImpactNoticeRecorded(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowImpactNoticeRecordedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if p.SourceContractVersion <= 0 || !workflowString(p.EntityKind, 128) || !workflowString(p.EntityRef, 128) || !workflowString(p.TargetWorkID, 128) || !workflowString(p.EdgeID, 128) || (p.Severity != "breaking" && p.Severity != "non-breaking" && p.Severity != "informational") || p.NoticeID != WorkflowNoticeID(event.SubjectID, p.SourceContractVersion, p.EntityKind, p.EntityRef, p.TargetWorkID, p.Severity) {
		return newFailure(KindInvariantViolation, "fold_event", "impact notice identity does not match canonical derivation", false, "recompute notice_id from its six identity fields")
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_impact_notices(notice_id,source_work_id,source_contract_version,entity_kind,entity_ref,target_work_id,edge_id,old_hash,new_hash,severity,recorded_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, p.NoticeID, event.SubjectID, p.SourceContractVersion, p.EntityKind, p.EntityRef, p.TargetWorkID, p.EdgeID, valueOrNil(p.OldHash), valueOrNil(p.NewHash), p.Severity, event.OccurredAt.UTC().Format(time.RFC3339Nano))
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
	if !workflowString(p.ConditionID, 128) || !workflowString(p.AwaitRef, 128) || !workflowString(p.ResolutionAuthority, 128) || !contains([]string{"pr_merge", "ci_result", "timer", "human_approval", "remote_work_state"}, p.AwaitType) {
		return newFailure(KindInvalidPayload, "fold_event", "condition_added contains an invalid closed await type", false, "use a typed external condition")
	}
	if _, err := conditionAuthorityOperation(ctx, tx, p.ResolutionAuthority, event.SubjectID); err != nil {
		return err
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_external_conditions(work_id,condition_id,await_type,await_ref,resolution_authority,condition_state,recorded_at) VALUES(?,?,?,?,?,'open',?)`, event.SubjectID, p.ConditionID, p.AwaitType, p.AwaitRef, p.ResolutionAuthority, event.OccurredAt.UTC().Format(time.RFC3339Nano))
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
func foldWorkflowCompleted(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowCompletedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if p.TerminalState != "completed" && p.TerminalState != "cancelled" && p.TerminalState != "superseded" || !contains([]string{"ok", "outcome_mismatch", "insufficient_evidence"}, p.FinalVerdictKind) || !workflowString(p.VerdictActorRef, 70) || p.EvidenceCount < 0 || p.EvidenceCount > 32 || !workflowDigest(p.ChangedRefsDigest, "sha256:") || !workflowList(p.Warnings, 16, 0) {
		return newFailure(KindInvalidPayload, "fold_event", "completed has invalid terminal metadata", false, "supply closed terminal metadata")
	}
	if err := requireActor(ctx, tx, p.VerdictActorRef); err != nil {
		return err
	}
	verdict, err := latestWorkflowVerdict(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if verdict == nil || verdict.VerdictActorRef != p.VerdictActorRef {
		return newFailure(KindUnauthorized, "fold_event", "completed verdict actor is not the authenticated recorded evaluator", false, "use the persisted workflow verdict actor")
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET instance_state=?,completed_at=? WHERE work_id=? AND instance_state NOT IN ('completed','cancelled','superseded')`, p.TerminalState, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.SubjectID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return newFailure(KindInvalidOperation, "fold_event", "workflow is already terminal or missing", false, "complete an active workflow once")
	}
	return nil
}

func contains(values []string, value string) bool {
	for _, x := range values {
		if x == value {
			return true
		}
	}
	return false
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
		return contains([]string{"true", "false", "pass", "fail", "present", "absent", "healthy", "unhealthy", "accepted", "rejected"}, v.ExpectedResult)
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
		if !contains(allowed, v) || seen[v] {
			return false
		}
		seen[v] = true
	}
	return true
}
