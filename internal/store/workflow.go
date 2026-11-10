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
	WorkflowDefinitionSelected = "workflow.definition_selected"
	WorkflowContractApproved   = "workflow.contract_approved"
	WorkflowOverlapResolved    = "workflow.overlap_resolved"
	WorkflowContractSuperseded = "workflow.contract_superseded"
	WorkflowActorRecorded      = "workflow.actor_recorded"
	WorkflowActionStarted      = "workflow.action_started"
	WorkflowActionCheckpointed = "workflow.action_checkpointed"
	WorkflowActionCompleted    = "workflow.action_completed"
	WorkflowActionFailed       = "workflow.action_failed"
	WorkflowEvidenceBound      = "workflow.evidence_bound"
	WorkflowPremiseConfirmed   = "workflow.premise_confirmed"
	WorkflowSuccessorLinked    = "workflow.successor_linked"
	// WorkflowWorkContextRecorded is read only by frozen migration replay.
	WorkflowWorkContextRecorded = "workflow.work_context_recorded"
	WorkflowDiagnosisRecorded   = "workflow.diagnosis_recorded"
	WorkflowDeliveryCorrected   = "workflow.delivery_corrected"
	WorkflowCompleted           = "workflow.completed"
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

type workflowContractApprovedPayload struct {
	WorkflowVersionFields
	ContractVersion     int64                        `json:"contract_version"`
	SpecMandate         []string                     `json:"spec_mandate"`
	LawModifies         []string                     `json:"law_modifies"`
	LegislativeSubjects []string                     `json:"legislative_subjects"`
	ArchitectureBinding *WorkflowArchitectureBinding `json:"architecture_binding,omitempty"`
	LawBoundaryVersion  int                          `json:"law_boundary_version,omitempty"`
	ConsequenceClass    string                       `json:"consequence_class,omitempty"`
}

// workflowContractPayloadVersion is the workflow.contract_approved
// current payload version.
const workflowContractPayloadVersion = 7

type workflowOverlapResolvedPayload struct {
	WorkflowVersionFields
	ToWorkID            string `json:"to_work_id"`
	ToExpectedVersion   int64  `json:"to_expected_version"`
	ToResultingVersion  int64  `json:"to_resulting_version"`
	FromContractVersion int64  `json:"from_contract_version"`
	ToContractVersion   int64  `json:"to_contract_version"`
	ResolutionKind      string `json:"resolution_kind"`
	Reason              string `json:"reason"`
}

func (p workflowOverlapResolvedPayload) workflowVersionFields() WorkflowVersionFields {
	return p.WorkflowVersionFields
}

type workflowDiagnosisRecordedPayload struct {
	WorkflowVersionFields
	Reproduction string `json:"reproduction"`
	RootCause    string `json:"root_cause"`
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
	// ExecutionModel is the readback executing-model identity for this run.
	// Empty when the run dispatched no typed lane.
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
	ActionID               string   `json:"action_id,omitempty"`
	StepID                 string   `json:"step_id"`
	AttemptEpoch           int64    `json:"attempt_epoch"`
	CorrectionDiagnosis    string   `json:"correction_diagnosis,omitempty"`
	CorrectionStrategy     string   `json:"correction_strategy,omitempty"`
	CorrectionEvidenceRefs []string `json:"correction_evidence_refs,omitempty"`
	DeliveryArtifact       string   `json:"delivery_artifact,omitempty"`
	DeliveryState          string   `json:"delivery_state,omitempty"`
	ResultEvidenceRefs     []string `json:"result_evidence_refs"`
	ChangedRefs            []string `json:"changed_refs"`
	ActorRef               string   `json:"actor_ref"`
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
}

type workflowCompletedPayload struct {
	WorkflowVersionFields
	TerminalState     string `json:"terminal_state"`
	PremiseConfirmed  bool   `json:"premise_confirmed"`
	EvidenceCount     int64  `json:"evidence_count"`
	ChangedRefsDigest string `json:"changed_refs_digest"`
	ImpactVerdict     string `json:"impact_verdict"`
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
// this constant so the advertised floor is the enforced one.
const workflowStringMinBytes = 2

func workflowString(value string, upper int) bool {
	return len(value) >= workflowStringMinBytes && len(value) <= upper
}

// workflowOperationEvidenceRefBound is the number of distinct evidence
// references one workflow action operation may resolve. The action_completed
// fold enforces it on result_evidence_refs.
const workflowOperationEvidenceRefBound = 32

// workflowEvidenceRef bounds one evidence reference. An entry of this kind
// carries a declared evidence locator or immutable subject ref, which the tool
// surface admits at 1 to 2048 bytes, so the 2-to-128 reference bound that
// governs work ids and touched refs does not apply to it.
func workflowEvidenceRef(value string) bool { return len(value) >= 1 && len(value) <= 2048 }

// workflowEvidenceRefsFault names the entry that fails the evidence reference
// bound, so a caller correcting the call learns which locator to change. The
// list is a set — workflowActionEvidenceRefs normalizes the merged refs
// before the event is written — so a duplicate here is a projection fault
// rather than ordinary caller input.
func workflowEvidenceRefsFault(values []string) string {
	if len(values) > workflowOperationEvidenceRefBound {
		return fmt.Sprintf("the list holds %d references and the bound is 0 to %d", len(values), workflowOperationEvidenceRefBound)
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

func validLegislativeSubjects(values []string) bool {
	if !workflowList(values, 800, 0) {
		return false
	}
	for _, value := range values {
		if !ValidReference(value) {
			return false
		}
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
			return newFailure(KindInvalidPayload, "fold_event", "non-Product-changing workflow cannot carry architecture_binding", false, "pick a Product-changing workflow type or drop architecture_binding")
		}
		if len(p.LawModifies) != 0 {
			if err := validateDerivedLawModification(ctx, tx, event.SubjectID, p.LawModifies); err != nil {
				return err
			}
		}
	}
	return nil
}

func foldWorkflowContractApproved(ctx context.Context, tx *sql.Tx, event Event) error {
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
	return foldValidatedWorkflowContractApproval(ctx, tx, event, p)
}

func normalizeWorkflowContractApproval(p *workflowContractApprovedPayload) error {
	if p.LegislativeSubjects == nil {
		p.LegislativeSubjects = []string{}
	}
	if p.LawModifies == nil {
		p.LawModifies = []string{}
	}
	if p.SpecMandate == nil {
		p.SpecMandate = []string{}
	}
	if p.ContractVersion <= 0 {
		return newFailure(KindInvalidPayload, "fold_event", "contract_version must be positive", false, "supply a positive contract_version")
	}
	if !validContractLawList("spec_mandate", p.SpecMandate) {
		return newFailure(KindInvalidPayload, "fold_event", "spec_mandate requires at most 32 unique references of 2..128 code points each", false, workflowSpecMandateTeaching)
	}
	if !validContractLawList("law_modifies", p.LawModifies) {
		return newFailure(KindInvalidPayload, "fold_event", "law_modifies requires at most 32 unique references of 2..128 code points each", false, "supply unique bounded law_modifies references")
	}
	if !validLegislativeSubjects(p.LegislativeSubjects) {
		return newFailure(KindInvalidPayload, "fold_event", "legislative_subjects requires at most 800 unique valid references of 2..128 bytes each", false, "supply unique bounded legislative_subjects references")
	}
	if p.LawBoundaryVersion != 0 && p.LawBoundaryVersion != 1 {
		return newFailure(KindInvalidPayload, "fold_event", "contract_approved law boundary version is invalid", false, "use the supported law boundary version")
	}
	return nil
}

func validContractLawList(name string, values []string) bool {
	items := make([]any, len(values))
	for i, value := range values {
		items[i] = value
	}
	return validateWorkflowPayloadStringList(actionContractLawListField(name, true), items)
}

func foldValidatedWorkflowContractApproval(ctx context.Context, tx *sql.Tx, event Event, p workflowContractApprovedPayload) error {
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
	return nil
}

func validateWorkflowContractFacts(ctx context.Context, tx *sql.Tx, event Event, planType WorkflowStepPlanType, contractFields map[string]json.RawMessage, p workflowContractApprovedPayload) error {
	changesProductTruth := planType != WorkflowStepPlanResearch
	if err := validateWorkflowContractApprovalLaw(ctx, tx, event, planType, contractFields, p, changesProductTruth); err != nil {
		return err
	}
	if err := validateWorkflowApprovalLawBoundaryTx(ctx, tx, event.SubjectID, p, changesProductTruth); err != nil {
		return err
	}
	if p.ConsequenceClass != "internal_sqlite" && p.ConsequenceClass != "cross_authority" && p.ConsequenceClass != "external_effect" {
		return newFailure(KindInvalidPayload, "fold_event", "contract consequence class is not closed", false, "use internal_sqlite, cross_authority, or external_effect")
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
	truthValue := 0
	if changesProductTruth {
		truthValue = 1
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_contracts(work_id,contract_version,consequence_class,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,changes_product_truth,legislative_subjects) VALUES(?,?,?,?,?,?,?,?,?,?)`, event.SubjectID, p.ContractVersion, p.ConsequenceClass, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.Actor, workflowJSON(p.SpecMandate), workflowJSON(p.LawModifies), p.LawBoundaryVersion, truthValue, workflowJSON(p.LegislativeSubjects))
	if err != nil {
		return workflowProjectionError(err, "cannot record immutable workflow contract")
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
	return advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields)
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
// store applies a workflow action to it. Every action fold calls it, so the
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

func foldWorkflowActionCheckpointed(ctx context.Context, tx *sql.Tx, event Event) error {
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
	return persistWorkflowCheckpointTx(ctx, tx, event, p)
}

func persistWorkflowCheckpointTx(ctx context.Context, tx *sql.Tx, event Event, p workflowActionCheckpointedPayload) error {
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

// validateWorkflowActionCompletedShape bounds the payload before any read.
func validateWorkflowActionCompletedShape(p workflowActionCompletedPayload) error {
	if err := validateWorkflowCompletionResultFields(p); err != nil {
		return err
	}
	return nil
}

func validateWorkflowCompletionResultFields(p workflowActionCompletedPayload) error {
	if fault := workflowEvidenceRefsFault(p.ResultEvidenceRefs); fault != "" {
		return newFailure(KindInvalidPayload, "fold_event", "action_completed result_evidence_refs is invalid: "+fault, false, "correct the named evidence locator")
	}
	if (p.ActionID != "" && !workflowString(p.ActionID, 128)) || !workflowString(p.StepID, 128) || p.AttemptEpoch <= 0 || p.AttemptEpoch > 2147483647 || !workflowList(p.ChangedRefs, 32, 0) {
		return newFailure(KindInvalidPayload, "fold_event", "action_completed has invalid result fields", false, "supply bounded action result references")
	}
	return nil
}

func foldWorkflowActionCompleted(ctx context.Context, tx *sql.Tx, event Event) error {
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
	if plan == nil && p.ActionID == "record_delivery" && p.StepID == "delivery" {
		var retained bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_instances i JOIN workflow_delivery_state d ON d.work_id=i.work_id WHERE i.work_id=? AND i.instance_state='completed')`, event.SubjectID).Scan(&retained); err != nil {
			return workflowProjectionError(err, "cannot read retained delivery")
		}
		if retained {
			return persistWorkflowActionCompletionTx(ctx, tx, event, p, "")
		}
	}
	if !workflowPlanAllowsAction(plan, p.StepID, p.ActionID) {
		retained, err := foldWorkflowRetainedRecordTx(ctx, tx, event.SubjectID, plan, p.StepID, p.ActionID)
		if err != nil {
			return err
		}
		if !retained {
			owner, err := workflowPlanCompleteActionTx(ctx, tx, event.SubjectID, plan, p.ActionID, true)
			if err != nil {
				return err
			}
			if owner == "" || owner != p.StepID {
				return newFailure(KindIllegalLifecycleTransition, "fold_event", "completed action is not declared on a frontier step", false, "reread_entities")
			}
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_phase_exits(work_id,action_id,exit_seq,actor,step_id,contract_version,record_version)
		VALUES(?,?,?,?,?,COALESCE((SELECT MAX(contract_version) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL),0),?)
		ON CONFLICT(work_id,action_id) DO UPDATE SET exit_seq=excluded.exit_seq,actor=excluded.actor,step_id=excluded.step_id,contract_version=excluded.contract_version,record_version=excluded.record_version`, event.SubjectID, p.ActionID, event.Seq, event.Actor, p.StepID, event.SubjectID, *p.ResultingVersion); err != nil {
		return workflowProjectionError(err, "cannot record the workflow phase exit")
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
	const externalRecovery = "submit the external locator in the bind_evidence call, in top-level evidence or declared fields, and omit producer_id, producer_run_ref and producer_watermark to bind from that call; alternatively submit it on an otherwise admitted workflow action, then bind with its operation_id as producer_run_ref, request_id as producer_watermark, and recording principal as producer_id; omit producer_id when that principal binds"
	var recordedWork, principal, requestID, result string
	err := tx.QueryRowContext(ctx, `SELECT work_id, COALESCE(principal_ref,''), COALESCE(request_id,''), COALESCE(result_kind,'') FROM durable_operations WHERE op_id=? ORDER BY (work_id=?) DESC, (result_kind='completed') DESC, attempt_epoch DESC LIMIT 1`, p.ProducerRunRef, workID).Scan(&recordedWork, &principal, &requestID, &result)
	switch {
	case err == sql.ErrNoRows:
		return newFailure(KindInvariantViolation, "fold_event", "named producer operation "+producer+" has no durable operation record", false, externalRecovery)
	case err != nil:
		return wrapFailure(KindUnavailable, "fold_event", "cannot verify evidence authority", true, "retry once the evidence authority is readable", err)
	case recordedWork != workID:
		return newFailure(KindInvariantViolation, "fold_event", "named producer operation "+producer+" belongs to another work item", false, repair)
	case result != "completed":
		return newFailure(KindInvariantViolation, "fold_event", "named producer operation "+producer+" is not completed; its result is "+workflowRefExcerpt(result), false, "bind evidence only from a completed producer operation")
	case principal != p.ProducerID:
		return newFailure(KindInvariantViolation, "fold_event", "producer_id "+workflowRefExcerpt(p.ProducerID)+" does not match producer operation "+producer+", which recorded producer_id "+workflowRefExcerpt(principal), false, "set producer_id to the value the producer operation recorded")
	case requestID != p.ProducerWatermark:
		return newFailure(KindInvariantViolation, "fold_event", "producer_watermark "+workflowRefExcerpt(p.ProducerWatermark)+" does not match producer operation "+producer+", which recorded producer_watermark "+workflowRefExcerpt(requestID), false, "set producer_watermark to the value the producer operation recorded; or "+externalRecovery)
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
		return newFailure(KindInvariantViolation, "fold_event", "locator mismatch: producer operation "+workflowRefExcerpt(p.ProducerRunRef)+" completed without "+workflowRefExcerpt(p.ImmutableSubjectRef)+" among its recorded evidence", false, "bind a locator the producer operation recorded, rerun the producer with this evidence reference, or submit the external locator in the bind_evidence call, in top-level evidence or declared fields, and omit producer_id, producer_run_ref and producer_watermark to bind from that call")
	}
	// A native-run report may back completion
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
func foldWorkflowCompleted(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowCompletedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	// evidence_count derives from the item's total workflow.evidence_bound
	// history, so any non-negative count the constructor produces is valid.
	if p.TerminalState != "completed" && p.TerminalState != "cancelled" || p.EvidenceCount < 0 || !workflowDigest(p.ChangedRefsDigest) || !slices.Contains([]string{"breaking", "non-breaking"}, p.ImpactVerdict) {
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
		contractVersion, contractErr := completionActiveContractVersion(ctx, tx, event.SubjectID)
		if contractErr != nil {
			return contractErr
		}
		if err := requireWorkflowCompletionFactsTx(ctx, tx, event.SubjectID, plan.Type, contractVersion); err != nil {
			return err
		}
	}
	if err := requireActor(ctx, tx, event.Actor); err != nil {
		return err
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET instance_state=?,completed_at=?,completion_payload=?,completion_occurred_at=? WHERE work_id=? AND instance_state NOT IN ('completed','cancelled')`, p.TerminalState, event.OccurredAt.UTC().Format(time.RFC3339Nano), string(event.Payload), event.OccurredAt.UTC().Format(time.RFC3339Nano), event.SubjectID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return newFailure(KindInvalidOperation, "fold_event", "workflow is already terminal or missing", false, "complete an active workflow once")
	}
	return nil
}

func workflowJSON(value any) string { encoded, _ := json.Marshal(value); return string(encoded) }
