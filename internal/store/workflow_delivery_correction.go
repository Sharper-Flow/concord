package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

// DeliveryEvidenceSourceCoordinatorAsserted is the closed provenance value
// every delivery correction carries. The core records what the coordinator
// asserted; it observes no forge, acquires no client, and runs no prober, so
// it never claims the merge evidence was verified anywhere.
const DeliveryEvidenceSourceCoordinatorAsserted = "coordinator_asserted"

// WorkflowDeliveryCorrectionRequest names one append-only correction of a
// completed delivery assertion. The target names the exact assertion event by
// identity, sequence, and payload version; the reason and merge evidence are
// coordinator-provided; the approval binding is the consumed core operator
// approval for this one correction.
type WorkflowDeliveryCorrectionRequest struct {
	WorkID                  string
	ExpectedVersion         int64
	TargetEventID           string
	TargetSeq               int64
	TargetPayloadVersion    int
	Reason                  string
	DeliveryArtifact        string
	DeliveryState           string
	ApprovalRef             string
	ApprovalOperationDigest string
	ApprovalScopeJSON       string
	ApprovalVersionsJSON    string
	ApprovalConsequence     string
	EventID                 string
	OccurredAt              time.Time
}

// workflowDeliveryCorrectedPayload is the closed payload of the typed
// append-only correction event. The original assertion event is never
// rewritten; the correction supersedes its effect in every read surface while
// the log keeps both.
type workflowDeliveryCorrectedPayload struct {
	WorkflowVersionFields
	TargetEventID           string `json:"target_event_id"`
	TargetSeq               int64  `json:"target_seq"`
	TargetPayloadVersion    int    `json:"target_payload_version"`
	Reason                  string `json:"reason"`
	DeliveryArtifact        string `json:"delivery_artifact"`
	DeliveryState           string `json:"delivery_state"`
	EvidenceSource          string `json:"evidence_source"`
	ApprovalRef             string `json:"approval_ref"`
	ApprovalOperationDigest string `json:"approval_operation_digest"`
	ApprovalScopeJSON       string `json:"approval_scope_json"`
	ApprovalVersionsJSON    string `json:"approval_versions_json"`
	ApprovalConsequence     string `json:"approval_consequence"`
}

// ApplyWorkflowDeliveryCorrectionTx appends one delivery correction for a
// completed delivery assertion inside the caller's transaction. The operator
// approval must already be consumed exactly once by the owning approval
// boundary; the mutation derives the operator workflow actor from that
// approval, records it, and appends the typed correction event. Admission
// authorizes the exact approval binding against the live approval row here,
// and the fold re-derives the same admission from the event payload and the
// log-derived actor row alone, so replay stays replay-pure. The workflow
// instance stays closed: the fold writes no lifecycle or step state, and a
// running instance is refused because it corrects delivery through
// record_delivery instead.
func ApplyWorkflowDeliveryCorrectionTx(ctx context.Context, tx *Transaction, request WorkflowDeliveryCorrectionRequest) (ApplyOperationResult, error) {
	sqlTx, err := transactionSQL(tx, "workflow_delivery_correction")
	if err != nil {
		return ApplyOperationResult{}, err
	}
	if err := validateMergeEvidenceAdmission(request.DeliveryArtifact, "$.delivery_artifact"); err != nil {
		return ApplyOperationResult{}, newFailure(KindInvalidPayload, "workflow_delivery_correction", fmt.Sprintf("delivery_artifact is invalid: %v", err), false, "supply a schema-valid https merge reference")
	}
	if request.OccurredAt.IsZero() {
		request.OccurredAt = tx.now()
	}
	operator, err := workflowDeliveryCorrectionOperatorFromApprovalTx(ctx, sqlTx, request)
	if err != nil {
		return ApplyOperationResult{}, err
	}
	version := request.ExpectedVersion
	events := make([]Event, 0, 2)
	events = append(events, workflowTypedEvent(request.EventID+":operator", WorkflowActorRecorded, request.WorkID, operator.ref, request.OccurredAt, version, map[string]any{
		"actor_ref": operator.ref, "principal_ref": operator.PrincipalRef, "client_ref": operator.ClientRef,
		"agent_ref": operator.AgentRef, "session_ref": operator.SessionRef, "actor_class": string(ActorOperator),
	}))
	version++
	payload, err := json.Marshal(map[string]any{
		"work_id": request.WorkID, "expected_version": version, "resulting_version": version + 1,
		"target_event_id": request.TargetEventID, "target_seq": request.TargetSeq, "target_payload_version": request.TargetPayloadVersion,
		"reason": request.Reason, "delivery_artifact": request.DeliveryArtifact, "delivery_state": request.DeliveryState,
		"evidence_source":           DeliveryEvidenceSourceCoordinatorAsserted,
		"approval_ref":              request.ApprovalRef,
		"approval_operation_digest": request.ApprovalOperationDigest,
		"approval_scope_json":       request.ApprovalScopeJSON,
		"approval_versions_json":    request.ApprovalVersionsJSON,
		"approval_consequence":      request.ApprovalConsequence,
	})
	if err != nil {
		return ApplyOperationResult{}, newFailure(KindInvalidPayload, "workflow_delivery_correction", "delivery correction payload cannot be encoded", false, "supply the bounded correction fields")
	}
	events = append(events, Event{
		EventID: request.EventID, Kind: WorkflowDeliveryCorrected, SubjectType: SubjectWorkItem, SubjectID: request.WorkID,
		Actor: operator.ref, OccurredAt: request.OccurredAt.UTC(), PayloadVersion: workflowDeliveryCorrectionRegisteredVersion(), Payload: payload,
	})
	return applyOperationTx(ctx, sqlTx, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, request.WorkID): request.ExpectedVersion}}, newFoldScope(sqlTx), true)
}

// workflowDeliveryCorrectionOperatorFromApprovalTx derives the operator
// workflow actor from the consumed one-use approval row, so the durable
// record shows the operator approval — not a session identity — asserting
// the correction. This admission route is the only live-table authorization:
// it re-checks the row's one-use consumed state and the request's exact
// binding, and the fold later re-derives the same admission from the event
// payload and the log-derived workflow_actors row alone.
func workflowDeliveryCorrectionOperatorFromApprovalTx(ctx context.Context, tx *sql.Tx, request WorkflowDeliveryCorrectionRequest) (workflowApprovalOperator, error) {
	return workflowOperatorFromConsumedApprovalTx(ctx, tx, workflowApprovalBinding{
		ApprovalRef: request.ApprovalRef, OperationDigest: request.ApprovalOperationDigest,
		ScopeJSON: request.ApprovalScopeJSON, VersionsJSON: request.ApprovalVersionsJSON,
		Consequence: request.ApprovalConsequence,
	}, "workflow_delivery_correction")
}

// foldWorkflowDeliveryCorrected admits one typed delivery correction and
// leaves every projection of the closed workflow instance untouched. The
// checks read only the event payload and log-derived projections, so live
// application and replay admit the same corrections.
func foldWorkflowDeliveryCorrected(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowDeliveryCorrectedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if !workflowString(p.TargetEventID, 256) || p.TargetSeq <= 0 || p.TargetPayloadVersion <= 0 || !workflowString(p.Reason, 1024) ||
		p.DeliveryState != "asserted" || p.EvidenceSource != DeliveryEvidenceSourceCoordinatorAsserted {
		return newFailure(KindInvalidPayload, "fold_event", "delivery correction has incomplete or unbounded fields", false, "supply the target identity, version, reason, merge evidence, and coordinator provenance")
	}
	if !replayValidReference(p.DeliveryArtifact) {
		return newFailure(KindInvalidPayload, "fold_event", "delivery_artifact is not a reference admitted by a supported historical rule", false, "supply a bounded delivery reference")
	}
	if p.ApprovalRef == "" || !validDigest(p.ApprovalOperationDigest) || p.ApprovalScopeJSON == "" || p.ApprovalVersionsJSON == "" || p.ApprovalConsequence == "" {
		return newFailure(KindInvalidPayload, "fold_event", "delivery correction carries no complete operator approval binding", false, "request the core operator approval for this correction")
	}
	if event.Actor == "" {
		return newFailure(KindUnauthorized, "fold_event", "delivery correction has no authenticated actor", false, "append the correction through the approved operator route")
	}
	if err := authorizeWorkflowOperatorApprovalTx(ctx, tx, event, workflowApprovalBinding{
		ApprovalRef:     p.ApprovalRef,
		OperationDigest: p.ApprovalOperationDigest,
		ScopeJSON:       p.ApprovalScopeJSON,
		VersionsJSON:    p.ApprovalVersionsJSON,
		Consequence:     p.ApprovalConsequence,
	}, deliveryCorrectionApprovalSubject); err != nil {
		return err
	}
	targetSeq, targetPayloadVersion, targetArtifact, err := workflowDeliveryAssertionEventTx(ctx, tx, event.SubjectID, p.TargetEventID)
	if err != nil {
		return err
	}
	if targetSeq != p.TargetSeq {
		return newFailure(KindInvalidOperation, "fold_event", "delivery correction target identity does not match the recorded assertion", false, "reread the current delivery assertion and target it exactly")
	}
	if targetPayloadVersion != p.TargetPayloadVersion {
		return newFailure(KindInvalidOperation, "fold_event", "delivery correction target version does not match the recorded assertion", false, "reread the current delivery assertion and target its payload version")
	}
	if targetArtifact == p.DeliveryArtifact {
		return newFailure(KindInvalidOperation, "fold_event", "delivery correction restates the asserted artifact", false, "supply the merged delivery evidence the assertion is missing")
	}
	if !replayValidMergeEvidenceReference(p.DeliveryArtifact) {
		return newFailure(KindInvalidPayload, "fold_event", "delivery_artifact does not match a supported merge_evidence rule", false, "supply the https merge URL the coordinator asserts, not a repository path")
	}
	latestSeq, err := workflowLatestDeliveryAssertionSeqTx(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if latestSeq != targetSeq {
		return newFailure(KindInvalidOperation, "fold_event", "delivery correction target is not the current delivery assertion", false, "target the latest recorded delivery assertion")
	}
	var corrections int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND event_id<>? AND json_extract(payload,'$.target_event_id')=?`, string(SubjectWorkItem), event.SubjectID, WorkflowDeliveryCorrected, event.EventID, p.TargetEventID).Scan(&corrections); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot inspect prior delivery corrections", true, "retry once the event log is readable", err)
	}
	if corrections != 0 {
		return newFailure(KindInvalidOperation, "fold_event", "delivery assertion is already corrected", false, "reread the effective delivery assertion")
	}
	var instanceState string
	if err := tx.QueryRowContext(ctx, `SELECT instance_state FROM workflow_instances WHERE work_id=?`, event.SubjectID).Scan(&instanceState); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindProjectionNotFound, "fold_event", "workflow instance is not recorded", false, "reread_entities")
		}
		return wrapFailure(KindUnavailable, "fold_event", "cannot read the workflow instance", true, "retry once the workflow projection is readable", err)
	}
	if instanceState != "completed" {
		return newFailure(KindInvalidOperation, "fold_event", "delivery correction applies only to a completed workflow instance", false, "correct delivery on a live instance through record_delivery")
	}
	// The fold writes nothing besides the version advance: the instance stays
	// closed, the original event stays untouched, and the read surfaces derive
	// the effective assertion from the log.
	return advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields)
}

// workflowDeliveryAssertionEventTx reads one recorded delivery assertion by
// event identity and returns its sequence, payload version, and asserted
// artifact. The assertion is a completed action whose event carries the
// asserted delivery fields (CD-0198 D4): record_delivery and the combined
// accept_worker_result append the same shape, so the reader keys on the
// fields, never on the action id.
func workflowDeliveryAssertionEventTx(ctx context.Context, tx *sql.Tx, workID, eventID string) (int64, int, string, error) {
	var seq int64
	var payloadVersion int
	var payload []byte
	if err := tx.QueryRowContext(ctx, `SELECT seq,payload_version,payload FROM domain_events WHERE event_id=? AND subject_type=? AND subject_id=? AND kind=?`, eventID, string(SubjectWorkItem), workID, WorkflowActionCompleted).Scan(&seq, &payloadVersion, &payload); err != nil {
		if err == sql.ErrNoRows {
			return 0, 0, "", newFailure(KindProjectionNotFound, "fold_event", "delivery correction target event is not recorded", false, "reread the current delivery assertion")
		}
		return 0, 0, "", wrapFailure(KindUnavailable, "fold_event", "cannot read the delivery correction target", true, "retry once the event log is readable", err)
	}
	var fields struct {
		DeliveryArtifact string `json:"delivery_artifact"`
		DeliveryState    string `json:"delivery_state"`
	}
	if err := json.Unmarshal(payload, &fields); err != nil {
		return 0, 0, "", newFailure(KindInvariantViolation, "fold_event", "delivery correction target payload is malformed", false, "rebuild workflow projections from the event log")
	}
	if fields.DeliveryArtifact == "" || fields.DeliveryState != "asserted" {
		return 0, 0, "", newFailure(KindInvalidOperation, "fold_event", "delivery correction target is not a recorded delivery assertion", false, "target the completed delivery event that carries the asserted artifact")
	}
	return seq, payloadVersion, fields.DeliveryArtifact, nil
}

// workflowLatestDeliveryAssertionSeqTx returns the sequence of the latest
// recorded delivery assertion for the work, or zero when none exists. The
// identity is the asserted delivery fields on a completed action event, so a
// combined accept assertion (CD-0198 D4) and a record_delivery assertion
// answer as one kind of event.
func workflowLatestDeliveryAssertionSeqTx(ctx context.Context, tx *sql.Tx, workID string) (int64, error) {
	var seq int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND COALESCE(json_extract(payload,'$.delivery_artifact'),'')<>'' AND COALESCE(json_extract(payload,'$.delivery_state'),'')='asserted'`, string(SubjectWorkItem), workID, WorkflowActionCompleted).Scan(&seq); err != nil {
		return 0, wrapFailure(KindUnavailable, "fold_event", "cannot read the latest delivery assertion", true, "retry once the event log is readable", err)
	}
	return seq, nil
}

// WorkflowReadDeliveryAssertion is the read-time view of one completed
// delivery assertion. Assertion carries the unchanged original event;
// TargetPayloadVersion is the stored payload version a correction must name
// to target it exactly. When a correction exists, Correction carries the
// effective merge evidence and the coordinator provenance that records the
// core never verified it. EffectiveArtifact carries the correction-overlay
// rule as data: the correction's merge evidence when a correction exists,
// the asserted artifact otherwise.
type WorkflowReadDeliveryAssertion struct {
	EventID              string                          `json:"event_id"`
	Seq                  int64                           `json:"seq"`
	TargetPayloadVersion int                             `json:"target_payload_version"`
	Artifact             string                          `json:"artifact"`
	State                string                          `json:"state"`
	ActorRef             string                          `json:"actor_ref"`
	AssertedAt           string                          `json:"asserted_at"`
	Correction           *WorkflowReadDeliveryCorrection `json:"correction,omitempty"`
	EffectiveArtifact    string                          `json:"effective_artifact"`
}

// WorkflowReadDeliveryCorrection is the typed append-only correction of a
// delivery assertion. EvidenceSource is always coordinator_asserted: the core
// records the coordinator's merge evidence and claims no verification of it.
type WorkflowReadDeliveryCorrection struct {
	EventID        string `json:"event_id"`
	Reason         string `json:"reason"`
	Artifact       string `json:"artifact"`
	EvidenceSource string `json:"evidence_source"`
	ApprovalRef    string `json:"approval_ref"`
	CorrectedAt    string `json:"corrected_at"`
}

// workflowDeliveryAssertionRead derives the current delivery assertion and its
// effective correction for one work item from the event log. The original
// event is returned unchanged; the correction is an overlay, never a rewrite.
// The assertion identity is the asserted delivery fields on a completed
// action event, so a combined accept assertion (CD-0198 D4) reads beside a
// record_delivery assertion.
func workflowDeliveryAssertionRead(ctx context.Context, q queryer, workID string) (*WorkflowReadDeliveryAssertion, error) {
	var seq int64
	var eventID, actor, occurredAt, payload string
	var payloadVersion int
	if err := q.QueryRowContext(ctx, `SELECT seq,event_id,actor,occurred_at,payload_version,payload FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND COALESCE(json_extract(payload,'$.delivery_artifact'),'')<>'' AND COALESCE(json_extract(payload,'$.delivery_state'),'')='asserted' ORDER BY seq DESC LIMIT 1`, string(SubjectWorkItem), workID, WorkflowActionCompleted).Scan(&seq, &eventID, &actor, &occurredAt, &payloadVersion, &payload); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, wrapFailure(KindUnavailable, "workflow_read", "cannot read the delivery assertion", true, "retry once the event log is readable", err)
	}
	var fields struct {
		DeliveryArtifact string `json:"delivery_artifact"`
		DeliveryState    string `json:"delivery_state"`
	}
	if err := json.Unmarshal([]byte(payload), &fields); err != nil || fields.DeliveryArtifact == "" {
		return nil, newFailure(KindInvariantViolation, "workflow_read", "delivery assertion payload is malformed", false, "rebuild workflow projections from the event log")
	}
	assertion := &WorkflowReadDeliveryAssertion{EventID: eventID, Seq: seq, TargetPayloadVersion: payloadVersion, Artifact: fields.DeliveryArtifact, State: fields.DeliveryState, ActorRef: actor, AssertedAt: occurredAt}
	correction, err := workflowDeliveryCorrectionRead(ctx, q, workID, eventID)
	if err != nil {
		return nil, err
	}
	assertion.Correction = correction
	assertion.EffectiveArtifact = effectiveDeliveryArtifact(assertion)
	return assertion, nil
}

func workflowDeliveryCorrectionRead(ctx context.Context, q queryer, workID, targetEventID string) (*WorkflowReadDeliveryCorrection, error) {
	var eventID, reason, artifact, evidenceSource, approvalRef, occurredAt string
	if err := q.QueryRowContext(ctx, `SELECT event_id,COALESCE(json_extract(payload,'$.reason'),''),COALESCE(json_extract(payload,'$.delivery_artifact'),''),COALESCE(json_extract(payload,'$.evidence_source'),''),COALESCE(json_extract(payload,'$.approval_ref'),''),occurred_at FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.target_event_id')=? ORDER BY seq DESC LIMIT 1`, string(SubjectWorkItem), workID, WorkflowDeliveryCorrected, targetEventID).Scan(&eventID, &reason, &artifact, &evidenceSource, &approvalRef, &occurredAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, wrapFailure(KindUnavailable, "workflow_read", "cannot read the delivery correction", true, "retry once the event log is readable", err)
	}
	if artifact == "" || evidenceSource == "" {
		return nil, newFailure(KindInvariantViolation, "workflow_read", "delivery correction payload is malformed", false, "rebuild workflow projections from the event log")
	}
	return &WorkflowReadDeliveryCorrection{EventID: eventID, Reason: reason, Artifact: artifact, EvidenceSource: evidenceSource, ApprovalRef: approvalRef, CorrectedAt: occurredAt}, nil
}

// effectiveDeliveryArtifact applies the correction-overlay rule the read
// declares as effective_artifact: the correction's merge evidence when a
// correction exists, the asserted artifact otherwise. The empty string means
// no delivery assertion is recorded.
func effectiveDeliveryArtifact(assertion *WorkflowReadDeliveryAssertion) string {
	if assertion == nil {
		return ""
	}
	if assertion.Correction != nil {
		return assertion.Correction.Artifact
	}
	return assertion.Artifact
}

func mergeEvidenceSchemaParts() (map[string]any, map[string]any, string, error) {
	document := payloadschema.Document()
	definitions, _ := document["$defs"].(map[string]any)
	reference, _ := definitions["reference"].(map[string]any)
	mergeEvidence, _ := definitions["merge_evidence"].(map[string]any)
	pattern, _ := mergeEvidence["pattern"].(string)
	if reference == nil || pattern == "" {
		return nil, nil, "", fmt.Errorf("published merge_evidence schema is incomplete")
	}
	return document, reference, pattern, nil
}

// validMergeEvidenceURLSyntax applies the URL pattern owned by the published schema.
func validMergeEvidenceURLSyntax(value string) bool {
	document, _, pattern, err := mergeEvidenceSchemaParts()
	if err != nil {
		return false
	}
	return payloadschema.ValidateValue(value, map[string]any{"pattern": pattern}, document, "$") == nil
}

func validateMergeEvidenceAdmission(value, path string) error {
	document, reference, pattern, err := mergeEvidenceSchemaParts()
	if err != nil {
		return err
	}
	if err := payloadschema.ValidateValue(value, reference, document, path); err != nil {
		return err
	}
	return payloadschema.ValidateValue(value, map[string]any{"pattern": pattern}, document, path)
}

// replayValidMergeEvidenceReference keeps legacy URLs readable and accepts
// URLs admitted by the current published syntax.
func replayValidMergeEvidenceReference(value string) bool {
	parsed, err := url.Parse(value)
	legacy := err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
	return legacy || validMergeEvidenceURLSyntax(value)
}

// workflowDeliveryCorrectionRegisteredVersion returns the registry's current
// payload version for the correction event, so the mutation always writes the
// registered shape.
func workflowDeliveryCorrectionRegisteredVersion() int {
	if registration, ok := registeredEventKind(WorkflowDeliveryCorrected); ok {
		return registration.CurrentVersion
	}
	return 1
}
