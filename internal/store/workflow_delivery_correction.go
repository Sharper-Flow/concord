package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"strings"
	"time"
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
// approval, records it, and appends the typed correction event. Admission and
// the approval binding are enforced again in the fold, so replay re-derives
// the same admission from durable state. The workflow instance stays closed:
// the fold writes no lifecycle or step state, and a running instance is
// refused because it corrects delivery through record_delivery instead.
func ApplyWorkflowDeliveryCorrectionTx(ctx context.Context, tx *Transaction, request WorkflowDeliveryCorrectionRequest) (ApplyOperationResult, error) {
	sqlTx, err := transactionSQL(tx, "workflow_delivery_correction")
	if err != nil {
		return ApplyOperationResult{}, err
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
// the correction.
func workflowDeliveryCorrectionOperatorFromApprovalTx(ctx context.Context, tx *sql.Tx, request WorkflowDeliveryCorrectionRequest) (workflowDeliveryCorrectionOperator, error) {
	if request.ApprovalRef == "" {
		return workflowDeliveryCorrectionOperator{}, newFailure(KindInvalidPayload, "workflow_delivery_correction", "delivery correction requires an operator approval reference", false, "request the core operator approval for this correction")
	}
	var principalRef, clientRef, sessionRef string
	if err := tx.QueryRowContext(ctx, `SELECT human_principal_ref,client_ref,session_ref FROM agent_approvals WHERE approval_ref=? AND revoked_at IS NULL`, request.ApprovalRef).Scan(&principalRef, &clientRef, &sessionRef); err != nil {
		if err == sql.ErrNoRows {
			return workflowDeliveryCorrectionOperator{}, newFailure(KindApprovalRequired, "workflow_delivery_correction", "delivery correction requires a consumed operator approval", false, "request the core operator approval for this correction")
		}
		return workflowDeliveryCorrectionOperator{}, wrapFailure(KindUnavailable, "workflow_delivery_correction", "cannot read the delivery correction approval", true, "retry once the approval projection is readable", err)
	}
	tuple := WorkflowActor{PrincipalRef: principalRef, ClientRef: clientRef, AgentRef: "approval:" + request.ApprovalRef, SessionRef: sessionRef, ActorClass: ActorOperator}
	ref, err := WorkflowActorRef(tuple)
	if err != nil {
		return workflowDeliveryCorrectionOperator{}, newFailure(KindInvalidPayload, "workflow_delivery_correction", "operator approval does not carry a bounded actor tuple", false, "request a fresh approval for this correction")
	}
	return workflowDeliveryCorrectionOperator{WorkflowActor: tuple, ref: ref}, nil
}

// workflowDeliveryCorrectionOperator pairs the derived operator actor with
// its computed reference, so the mutation writes the actor event and the
// correction event under one identity.
type workflowDeliveryCorrectionOperator struct {
	WorkflowActor
	ref string
}

// foldWorkflowDeliveryCorrected admits one typed delivery correction and
// leaves every projection of the closed workflow instance untouched. The
// checks reread durable state, so live application and replay admit the same
// corrections.
func foldWorkflowDeliveryCorrected(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowDeliveryCorrectedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := workflowBase(event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if !workflowString(p.TargetEventID, 256) || p.TargetSeq <= 0 || p.TargetPayloadVersion <= 0 || !workflowString(p.Reason, 1024) ||
		!ValidReference(p.DeliveryArtifact) || p.DeliveryState != "asserted" || p.EvidenceSource != DeliveryEvidenceSourceCoordinatorAsserted {
		return newFailure(KindInvalidPayload, "fold_event", "delivery correction has incomplete or unbounded fields", false, "supply the target identity, version, reason, merge evidence, and coordinator provenance")
	}
	if p.ApprovalRef == "" || !validDigest(p.ApprovalOperationDigest) || p.ApprovalScopeJSON == "" || p.ApprovalVersionsJSON == "" || p.ApprovalConsequence == "" {
		return newFailure(KindInvalidPayload, "fold_event", "delivery correction carries no complete operator approval binding", false, "request the core operator approval for this correction")
	}
	if event.Actor == "" {
		return newFailure(KindUnauthorized, "fold_event", "delivery correction has no authenticated actor", false, "append the correction through the approved operator route")
	}
	if err := authorizeWorkflowDeliveryCorrectionApprovalTx(ctx, tx, event, p); err != nil {
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
	if !validMergeEvidenceReference(p.DeliveryArtifact) {
		return newFailure(KindInvalidPayload, "fold_event", "delivery correction merge evidence is not an external merge reference", false, "supply the https merge URL the coordinator asserts, not a repository path")
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

// authorizeWorkflowDeliveryCorrectionApprovalTx admits the correction only
// through a recorded one-use operator approval bound to this exact operation
// digest, scope, versions, consequence, and client. It runs inside the fold's
// transaction, so a replay re-checks the same binding.
func authorizeWorkflowDeliveryCorrectionApprovalTx(ctx context.Context, tx *sql.Tx, event Event, p workflowDeliveryCorrectedPayload) error {
	var actorClass, agentRef, actorClientRef string
	if err := tx.QueryRowContext(ctx, `SELECT actor_class,agent_ref,client_ref FROM workflow_actors WHERE actor_ref=?`, event.Actor).Scan(&actorClass, &agentRef, &actorClientRef); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindUnauthorized, "fold_event", "delivery correction requires a recorded operator approval actor", false, "submit the correction through the approved operator route")
		}
		return workflowProjectionError(err, "cannot read the delivery correction actor")
	}
	if actorClass != string(ActorOperator) || agentRef != "approval:"+p.ApprovalRef {
		return newFailure(KindUnauthorized, "fold_event", "delivery correction requires a recorded operator approval actor", false, "submit the correction through the approved operator route")
	}
	var usedCount, maxUses int
	var approvalDigest, approvalScopeJSON, approvalVersionsJSON, approvalConsequence, approvalClientRef string
	if err := tx.QueryRowContext(ctx, `SELECT operation_digest,scope_json,version_json,consequence,client_ref,used_count,max_uses FROM agent_approvals WHERE approval_ref=? AND revoked_at IS NULL`, p.ApprovalRef).Scan(&approvalDigest, &approvalScopeJSON, &approvalVersionsJSON, &approvalConsequence, &approvalClientRef, &usedCount, &maxUses); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindApprovalRequired, "fold_event", "delivery correction requires a consumed operator approval", false, "request the core operator approval for this correction")
		}
		return workflowProjectionError(err, "cannot read the delivery correction approval")
	}
	if usedCount != 1 || maxUses != 1 {
		return newFailure(KindApprovalRequired, "fold_event", "delivery correction requires a consumed one-use operator approval", false, "request a fresh approval for this correction")
	}
	mismatches := make([]string, 0, 5)
	if !validDigest(p.ApprovalOperationDigest) || p.ApprovalOperationDigest != approvalDigest {
		mismatches = append(mismatches, "digest")
	}
	if p.ApprovalScopeJSON != approvalScopeJSON {
		mismatches = append(mismatches, "scope")
	}
	if p.ApprovalVersionsJSON != approvalVersionsJSON {
		mismatches = append(mismatches, "versions")
	}
	if p.ApprovalConsequence != approvalConsequence {
		mismatches = append(mismatches, "consequence")
	}
	if approvalClientRef != actorClientRef {
		mismatches = append(mismatches, "client")
	}
	if len(mismatches) != 0 {
		return newFailure(KindUnauthorized, "fold_event", "delivery correction approval is not bound to the exact operation, scope, versions, or consequence: "+strings.Join(mismatches, ","), false, "request a fresh approval for the exact correction operation")
	}
	return nil
}

// workflowDeliveryAssertionEventTx reads one recorded delivery assertion by
// event identity and returns its sequence, payload version, and asserted
// artifact. The assertion is a completed record_delivery action; an event
// without the asserted payload is not an assertion.
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
		ActionID         string `json:"action_id"`
		DeliveryArtifact string `json:"delivery_artifact"`
		DeliveryState    string `json:"delivery_state"`
	}
	if err := json.Unmarshal(payload, &fields); err != nil {
		return 0, 0, "", newFailure(KindInvariantViolation, "fold_event", "delivery correction target payload is malformed", false, "rebuild workflow projections from the event log")
	}
	if fields.ActionID != "record_delivery" || fields.DeliveryArtifact == "" || fields.DeliveryState != "asserted" {
		return 0, 0, "", newFailure(KindInvalidOperation, "fold_event", "delivery correction target is not a recorded delivery assertion", false, "target the completed record_delivery event that carries the asserted artifact")
	}
	return seq, payloadVersion, fields.DeliveryArtifact, nil
}

// workflowLatestDeliveryAssertionSeqTx returns the sequence of the latest
// recorded delivery assertion for the work, or zero when none exists.
func workflowLatestDeliveryAssertionSeqTx(ctx context.Context, tx *sql.Tx, workID string) (int64, error) {
	var seq int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='record_delivery' AND COALESCE(json_extract(payload,'$.delivery_artifact'),'')<>'' AND COALESCE(json_extract(payload,'$.delivery_state'),'')='asserted'`, string(SubjectWorkItem), workID, WorkflowActionCompleted).Scan(&seq); err != nil {
		return 0, wrapFailure(KindUnavailable, "fold_event", "cannot read the latest delivery assertion", true, "retry once the event log is readable", err)
	}
	return seq, nil
}

// WorkflowReadDeliveryAssertion is the read-time view of one completed
// delivery assertion. Assertion carries the unchanged original event;
// TargetPayloadVersion is the stored payload version a correction must name
// to target it exactly. When a correction exists, Correction carries the
// effective merge evidence and the coordinator provenance that records the
// core never verified it.
type WorkflowReadDeliveryAssertion struct {
	EventID              string                          `json:"event_id"`
	Seq                  int64                           `json:"seq"`
	TargetPayloadVersion int                             `json:"target_payload_version"`
	Artifact             string                          `json:"artifact"`
	State                string                          `json:"state"`
	ActorRef             string                          `json:"actor_ref"`
	AssertedAt           string                          `json:"asserted_at"`
	Correction           *WorkflowReadDeliveryCorrection `json:"correction,omitempty"`
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
func workflowDeliveryAssertionRead(ctx context.Context, q queryer, workID string) (*WorkflowReadDeliveryAssertion, error) {
	var seq int64
	var eventID, actor, occurredAt, payload string
	var payloadVersion int
	if err := q.QueryRowContext(ctx, `SELECT seq,event_id,actor,occurred_at,payload_version,payload FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='record_delivery' AND COALESCE(json_extract(payload,'$.delivery_artifact'),'')<>'' AND COALESCE(json_extract(payload,'$.delivery_state'),'')='asserted' ORDER BY seq DESC LIMIT 1`, string(SubjectWorkItem), workID, WorkflowActionCompleted).Scan(&seq, &eventID, &actor, &occurredAt, &payloadVersion, &payload); err != nil {
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

// EffectiveArtifact names the artifact every consumer must
// treat as the delivered evidence: the correction's merge evidence when a
// correction exists, the asserted artifact otherwise. The empty string means
// no delivery assertion is recorded.
func (a *WorkflowReadDeliveryAssertion) EffectiveArtifact() string {
	if a == nil {
		return ""
	}
	if a.Correction != nil {
		return a.Correction.Artifact
	}
	return a.Artifact
}

// validMergeEvidenceReference reports whether a coordinator-supplied merge
// evidence value is an absolute https URL. A repository path cannot carry a
// merge: admitting one would present an in-repo path as the proof of an
// out-of-repo merge the core never verified. The generated contract carries
// the same rule in $defs/merge_evidence.
func validMergeEvidenceReference(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
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
