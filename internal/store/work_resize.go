package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"time"
)

// CD-0229 D6 and CD-0237: when a size revision changes a sized item's
// effective size, one transaction creates a successor with the same intent,
// kind and memberships, records its size and any carried operator size, pins
// it, forward-links it, and supersedes the item without completing it. The
// Linear issue identity moves to the successor. Evidence stays on the
// predecessor and is read by reference; no approval, contract, challenge or
// job acceptance carries.

// WorkResized records one resize: the predecessor is the subject.
const WorkResized = "work.resized"

// WorkResizeEvidenceSource names the predecessor's log, through the resize,
// that the successor reads its earlier evidence from.
type WorkResizeEvidenceSource struct {
	WorkID     string `json:"work_id"`
	ThroughSeq int64  `json:"through_seq"`
}

type workResizedPayload struct {
	SuccessorWorkID        string                   `json:"successor_work_id"`
	SourceModelEventID     string                   `json:"source_model_event_id"`
	SuccessorModelEventID  string                   `json:"successor_model_event_id"`
	SourceOverrideEventID  string                   `json:"source_override_event_id,omitempty"`
	RootWorkID             string                   `json:"root_work_id"`
	RootDefineStartEventID *string                  `json:"root_define_start_event_id,omitempty"`
	EvidenceSource         WorkResizeEvidenceSource `json:"evidence_source"`
	LinearIdentity         *LinearIssueLink         `json:"linear_identity,omitempty"`
}

// WorkLineage is a work item's resize root and the predecessors' logs its
// earlier evidence is read from, nearest first. Without a lineage row the
// item is its own root and reads no other log.
type WorkLineage struct {
	RootWorkID             string
	RootDefineStartEventID *string
	EvidenceSources        []WorkResizeEvidenceSource
}

// readWorkLineage reads a work item's resize root.
func readWorkLineage(ctx context.Context, q queryer, workID string) (WorkLineage, error) {
	lineage := WorkLineage{RootWorkID: workID}
	var start sql.NullString
	err := q.QueryRowContext(ctx, `SELECT root_work_id, root_define_start_event_id FROM work_resize_lineage WHERE successor_work_id=?`, workID).Scan(&lineage.RootWorkID, &start)
	if err == sql.ErrNoRows {
		return WorkLineage{RootWorkID: workID}, nil
	}
	if err != nil {
		return WorkLineage{}, wrapFailure(KindUnavailable, "work_lineage", "cannot read the resize lineage", true, "retry once the database is readable", err)
	}
	if start.Valid {
		lineage.RootDefineStartEventID = &start.String
	}
	rows, err := q.QueryContext(ctx, `WITH RECURSIVE chain(work_id, depth) AS (
			SELECT ?, 0
			UNION ALL SELECT l.predecessor_work_id, c.depth+1 FROM work_resize_lineage l JOIN chain c ON l.successor_work_id=c.work_id)
		SELECT json_extract(e.payload,'$.evidence_source.work_id'), json_extract(e.payload,'$.evidence_source.through_seq')
		FROM chain c JOIN work_resize_lineage l ON l.predecessor_work_id=c.work_id JOIN domain_events e ON e.event_id=l.event_id
		WHERE c.depth>0 ORDER BY c.depth`, workID)
	if err != nil {
		return WorkLineage{}, wrapFailure(KindUnavailable, "work_lineage", "cannot read the resize evidence sources", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var source WorkResizeEvidenceSource
		if err := rows.Scan(&source.WorkID, &source.ThroughSeq); err != nil {
			return WorkLineage{}, wrapFailure(KindUnavailable, "work_lineage", "cannot scan the resize evidence sources", true, "retry once the database is readable", err)
		}
		lineage.EvidenceSources = append(lineage.EvidenceSources, source)
	}
	if err := rows.Err(); err != nil {
		return WorkLineage{}, wrapFailure(KindUnavailable, "work_lineage", "cannot scan the resize evidence sources", true, "retry once the database is readable", err)
	}
	return lineage, nil
}

// workInResizeLineageTx reports whether the item is a predecessor or a
// successor of a resize.
func workInResizeLineageTx(ctx context.Context, q queryer, workID string) (bool, error) {
	var member bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_resize_lineage WHERE predecessor_work_id=?1 OR successor_work_id=?1)`, workID).Scan(&member); err != nil {
		return false, wrapFailure(KindUnavailable, "fold_event", "cannot read the resize lineage", true, "retry once the database is readable", err)
	}
	return member, nil
}

// workResizeOriginTx returns the predecessor a work item was created to
// succeed, or "" for ordinary capture. It reads the recorded creation, so
// replay answers as the live append did.
func workResizeOriginTx(ctx context.Context, q queryer, workID string) (string, error) {
	var origin string
	err := q.QueryRowContext(ctx, `SELECT coalesce(json_extract(payload,'$.resize_of'),'') FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind='work.created' ORDER BY seq LIMIT 1`, workID).Scan(&origin)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", wrapFailure(KindUnavailable, "fold_event", "cannot read the work item's creation", true, "retry once the event log is readable", err)
	}
	return origin, nil
}

type workResizeAdmissionKey struct{}

// withWorkResizeAdmission marks the live resize core's appends: only it may
// create a resize-origin work item for this predecessor.
func withWorkResizeAdmission(ctx context.Context, predecessor string) context.Context {
	return context.WithValue(ctx, workResizeAdmissionKey{}, predecessor)
}

func workResizeAdmitted(ctx context.Context, predecessor string) bool {
	admitted, _ := ctx.Value(workResizeAdmissionKey{}).(string)
	return admitted != "" && admitted == predecessor
}

// checkResizeOriginCreatedTx holds a resize-origin creation to its
// predecessor: same kind, live, and the same intent verbatim. It returns
// the predecessor's intent, which the successor keeps, defect siblings
// included.
func checkResizeOriginCreatedTx(ctx context.Context, tx *sql.Tx, event Event, payload workCreatedPayload) (string, error) {
	if !isWorkflowReplay(ctx) && !workResizeAdmitted(ctx, payload.ResizeOf) {
		return "", newFailure(KindInvalidOperation, "fold_event", "a resize successor is created only by the resize core", false, "revise the work item's size")
	}
	if payload.RaisedFromWorkID != "" || payload.ResizeOf == event.SubjectID {
		return "", newFailure(KindInvalidPayload, "fold_event", "a resize successor acknowledges no collision and is not its own predecessor", false, "record the resize successor's intent verbatim")
	}
	var kind, lifecycle, intent string
	if err := tx.QueryRowContext(ctx, `SELECT kind, lifecycle, intent_json FROM work_items WHERE id=?`, payload.ResizeOf).Scan(&kind, &lifecycle, &intent); err == sql.ErrNoRows {
		return "", newFailure(KindProjectionNotFound, "fold_event", "the resized work item is not recorded", false, "resize existing work")
	} else if err != nil {
		return "", wrapFailure(KindUnavailable, "fold_event", "cannot read the resized work item", true, "retry once the database is readable", err)
	}
	if isTerminalLifecycle(lifecycle) || kind != payload.WorkKind {
		return "", newFailure(KindInvalidTransition, "fold_event", "a resize successor keeps the kind of live work", false, "resize nonterminal work without changing its kind")
	}
	var recorded workIntentProjection
	if err := json.Unmarshal([]byte(intent), &recorded); err != nil {
		return "", wrapFailure(KindInvariantViolation, "fold_event", "the resized work item's intent is malformed", false, "rebuild the work projection", err)
	}
	copied := workIntentProjection{
		Title: payload.Title, Task: payload.Task, ValueStatement: payload.ValueStatement, Kind: payload.WorkKind,
		Priority: *payload.Priority, Urgency: payload.Urgency, Tags: payload.Tags,
		ComponentID: payload.ComponentID, WorkflowTypeRef: payload.WorkflowTypeRef, ExternalRef: payload.ExternalRef,
		Defect: defectIntakeFromProjection(payload.DefectIntake),
	}
	compare := recorded
	if compare.Defect != nil {
		defect := *compare.Defect
		defect.SiblingIDs = []string{}
		compare.Defect = &defect
	}
	compare.Tags, copied.Tags = sliceOrEmpty(compare.Tags), sliceOrEmpty(copied.Tags)
	if !reflect.DeepEqual(compare, copied) {
		return "", newFailure(KindInvariantViolation, "fold_event", "a resize successor copies its predecessor's intent verbatim", false, "record the predecessor's intent")
	}
	return intent, nil
}

func foldWorkResized(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var p workResizedPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	invalid := func(message string) error {
		return newFailure(KindInvariantViolation, "fold_event", message, false, "record the resize the core derives")
	}
	if !ValidReference(p.SuccessorWorkID) || p.SuccessorModelEventID == "" || p.EvidenceSource.WorkID != event.SubjectID || p.EvidenceSource.ThroughSeq < 1 {
		return newFailure(KindInvalidPayload, "fold_event", "work.resized names no successor, successor size record, or evidence source", false, "record the resize the core derives")
	}
	if origin, err := workResizeOriginTx(ctx, tx, p.SuccessorWorkID); err != nil {
		return err
	} else if origin != event.SubjectID {
		return invalid("the successor was not created to succeed the resized work item")
	}
	if member, err := workInResizeLineageTx(ctx, tx, p.SuccessorWorkID); err != nil {
		return err
	} else if member {
		return invalid("the successor already belongs to a resize")
	}
	var predecessorResized bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_resize_lineage WHERE predecessor_work_id=?)`, event.SubjectID).Scan(&predecessorResized); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot read the resize lineage", true, "retry once the database is readable", err)
	}
	if predecessorResized {
		return newFailure(KindSupersessionSecondSuccessor, "fold_event", "the work item already has a resize successor", false, "resize the current successor")
	}
	decision, sized, err := readEffectiveWorkSize(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	var sourceModel, sourceOverride string
	if sized {
		if err := tx.QueryRowContext(ctx, `SELECT event_id FROM work_size_assessments WHERE work_id=? AND sequence=?`, event.SubjectID, decision.Model.Sequence).Scan(&sourceModel); err != nil {
			return wrapFailure(KindUnavailable, "fold_event", "cannot read the resized size record", true, "retry once the database is readable", err)
		}
		if decision.OperatorSize != nil {
			if err := tx.QueryRowContext(ctx, `SELECT event_id FROM work_size_overrides WHERE work_id=?`, event.SubjectID).Scan(&sourceOverride); err != nil {
				return wrapFailure(KindUnavailable, "fold_event", "cannot read the resized operator size", true, "retry once the database is readable", err)
			}
		}
	}
	if !sized || p.SourceModelEventID != sourceModel || p.SourceOverrideEventID != sourceOverride {
		return invalid("work.resized does not name the resized item's current size records")
	}
	var successorSized bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_size_assessments WHERE work_id=?)`, p.SuccessorWorkID).Scan(&successorSized); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot read the successor's size", true, "retry once the database is readable", err)
	}
	if successorSized {
		return invalid("the successor is sized before its resize is recorded")
	}
	root, err := readWorkLineage(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if p.RootWorkID != root.RootWorkID || !reflect.DeepEqual(p.RootDefineStartEventID, root.RootDefineStartEventID) {
		return invalid("work.resized does not carry the resized item's lineage root")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO work_resize_lineage(predecessor_work_id,successor_work_id,event_id,root_work_id,root_define_start_event_id) VALUES(?,?,?,?,?)`,
		event.SubjectID, p.SuccessorWorkID, event.EventID, p.RootWorkID, p.RootDefineStartEventID); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot write the resize lineage", true, "retry once the database is writable", err)
	}
	return nil
}

// WorkSizeRevisionResult reports a size revision's events and, when the
// effective size changed, the successor that now carries the work.
type WorkSizeRevisionResult struct {
	EventIDs []string
	// WorkVersion is the revised item's version after the revision.
	WorkVersion     int64
	SuccessorWorkID string
	// SuccessorVersion is the successor's version when a resize created one.
	SuccessorVersion int64
}

// workSizeRevisionResultTx reads the versions a settled revision left.
func workSizeRevisionResultTx(ctx context.Context, q queryer, workID, successor string, eventIDs []string) (WorkSizeRevisionResult, error) {
	result := WorkSizeRevisionResult{EventIDs: eventIDs, SuccessorWorkID: successor}
	if err := q.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&result.WorkVersion); err != nil {
		return WorkSizeRevisionResult{}, wrapFailure(KindUnavailable, "work_size", "cannot read the revised work item's version", true, "retry once the database is readable", err)
	}
	if successor != "" {
		if err := q.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, successor).Scan(&result.SuccessorVersion); err != nil {
			return WorkSizeRevisionResult{}, wrapFailure(KindUnavailable, "work_size", "cannot read the successor's version", true, "retry once the database is readable", err)
		}
	}
	return result, nil
}

// workResizeRequest names the item a revision resized, the revision's event
// identity that every resize event derives from, and the acting principal.
type workResizeRequest struct {
	WorkID  string
	EventID string
	Actor   WorkflowActor
	Now     time.Time
}

// settleWorkSizeRevisionTx runs after a rerun or an override has folded. A
// first sizing, or a revision that keeps the effective size, keeps the item
// and its pin; any other change resizes it. Unpinned work has no size-chosen
// variant to change, so its size changes in place.
func settleWorkSizeRevisionTx(ctx context.Context, transaction *Transaction, before WorkSizeDecision, wasSized bool, request workResizeRequest) (string, error) {
	tx, err := transactionSQL(transaction, "work_resize")
	if err != nil {
		return "", err
	}
	after, _, err := readEffectiveWorkSize(ctx, tx, request.WorkID)
	if err != nil || !wasSized || after.Effective == before.Effective {
		return "", err
	}
	var pinned bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_instances WHERE work_id=?)`, request.WorkID).Scan(&pinned); err != nil {
		return "", wrapFailure(KindUnavailable, "work_resize", "cannot read the workflow pin", true, "retry once the database is readable", err)
	}
	if !pinned {
		return "", nil
	}
	return resizeWorkTx(ctx, transaction, request)
}

func resizeWorkTx(ctx context.Context, transaction *Transaction, request workResizeRequest) (string, error) {
	const op = "work_resize"
	tx, err := transactionSQL(transaction, op)
	if err != nil {
		return "", err
	}
	if request.Now.IsZero() {
		request.Now = transaction.now()
	}
	var kind, lifecycle, intentJSON string
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT kind, lifecycle, intent_json, version FROM work_items WHERE id=?`, request.WorkID).Scan(&kind, &lifecycle, &intentJSON, &version); err != nil {
		return "", wrapFailure(KindUnavailable, op, "cannot read the resized work item", true, "retry once the database is readable", err)
	}
	if lifecycle != "needed" && lifecycle != "in_progress" {
		return "", newFailure(KindInvalidTransition, op, "only live work is resized", false, "revise the size of live work")
	}
	if held, err := outsideRepairActiveTx(ctx, tx, request.WorkID); err != nil {
		return "", err
	} else if held {
		return "", newOutsideRepairRouteFailure(op, "an outside-repair hold blocks the resize")
	}
	var active, superseded bool
	if err := tx.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM worker_attempts WHERE work_id=?1 AND lifecycle_state IN ('dispatched','in_flight')),
		EXISTS(SELECT 1 FROM relations WHERE work_id_to=?1 AND kind='supersedes')`, request.WorkID).Scan(&active, &superseded); err != nil {
		return "", wrapFailure(KindUnavailable, op, "cannot read the resized work item's attempts", true, "retry once the database is readable", err)
	}
	if active {
		return "", newFailure(KindResourceBusy, op, "a worker attempt is still open", true, "settle the open worker attempt, then revise the size")
	}
	if superseded {
		return "", newFailure(KindSupersessionSecondSuccessor, op, "the work item already has a successor", false, "revise the size of the current successor")
	}
	var intent workIntentProjection
	if err := json.Unmarshal([]byte(intentJSON), &intent); err != nil {
		return "", wrapFailure(KindInvariantViolation, op, "the work item's intent is malformed", false, "rebuild the work projection", err)
	}
	decision, _, err := readEffectiveWorkSize(ctx, tx, request.WorkID)
	if err != nil {
		return "", err
	}
	var sourceModel, sourceModelPayload, sourceOverride, sourceOverridePayload string
	if err := tx.QueryRowContext(ctx, `SELECT s.event_id, e.payload FROM work_size_assessments s JOIN domain_events e ON e.event_id=s.event_id WHERE s.work_id=? AND s.sequence=?`, request.WorkID, decision.Model.Sequence).Scan(&sourceModel, &sourceModelPayload); err != nil {
		return "", wrapFailure(KindUnavailable, op, "cannot read the size record", true, "retry once the database is readable", err)
	}
	if decision.OperatorSize != nil {
		if err := tx.QueryRowContext(ctx, `SELECT o.event_id, e.payload FROM work_size_overrides o JOIN domain_events e ON e.event_id=o.event_id WHERE o.work_id=?`, request.WorkID).Scan(&sourceOverride, &sourceOverridePayload); err != nil {
			return "", wrapFailure(KindUnavailable, op, "cannot read the operator's size", true, "retry once the database is readable", err)
		}
	}
	var throughSeq int64
	if err := tx.QueryRowContext(ctx, `SELECT max(seq) FROM domain_events WHERE subject_type='work_item' AND subject_id=?`, request.WorkID).Scan(&throughSeq); err != nil {
		return "", wrapFailure(KindUnavailable, op, "cannot read the resized work item's log", true, "retry once the event log is readable", err)
	}
	root, err := readWorkLineage(ctx, tx, request.WorkID)
	if err != nil {
		return "", err
	}
	link, linked, err := readLinearLinkCore(ctx, tx, request.WorkID)
	if err != nil {
		return "", err
	}
	actorRef, err := WorkflowActorRef(request.Actor)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte("work.resized:" + request.EventID))
	successor := "work-" + hex.EncodeToString(sum[:])[:24]
	id := func(part string) string { return request.EventID + ":resize:" + part }
	now := request.Now.UTC()
	ctx = withWorkResizeAdmission(ctx, request.WorkID)

	// The successor, its memberships, and the lineage.
	priority := intent.Priority
	var defect *DefectIntake
	if intent.Defect != nil {
		defect = &DefectIntake{FailureShape: intent.Defect.FailureShape, Reproduction: intent.Defect.Reproduction, Searched: intent.Defect.Searched, RelatedDefectIDs: intent.Defect.RelatedDefectIDs, RootCauseWorkID: intent.Defect.RootCauseWorkID}
	}
	created := workCreatedPayload{WorkID: successor, WorkKind: kind, Title: intent.Title, Task: intent.Task, ValueStatement: intent.ValueStatement,
		Priority: &priority, Urgency: intent.Urgency, Tags: intent.Tags, ComponentID: intent.ComponentID, WorkflowTypeRef: intent.WorkflowTypeRef,
		ExternalRef: intent.ExternalRef, DefectIntake: defect, ResizeOf: request.WorkID}
	memberships, err := readWorkMembershipsTx(ctx, tx, request.WorkID)
	if err != nil {
		return "", err
	}
	resized := workResizedPayload{SuccessorWorkID: successor, SourceModelEventID: sourceModel, SuccessorModelEventID: id("sized"), SourceOverrideEventID: sourceOverride,
		RootWorkID: root.RootWorkID, RootDefineStartEventID: root.RootDefineStartEventID,
		EvidenceSource: WorkResizeEvidenceSource{WorkID: request.WorkID, ThroughSeq: throughSeq}}
	if linked {
		resized.LinearIdentity = &link
	}
	if _, err := ApplyOperationTx(ctx, transaction, Operation{Events: []Event{
		resizeEvent(id("created"), "work.created", successor, actorRef, now, 4, created),
		resizeEvent(id("memberships"), "work.memberships_replaced", successor, actorRef, now, 1, workMembershipsPayload{Memberships: memberships, ExpectedVersion: 1, ResultingVersion: 2}),
		resizeEvent(id("resized"), WorkResized, request.WorkID, actorRef, now, 1, resized),
	}}); err != nil {
		return "", err
	}

	// The successor's size record and the carried operator size.
	var model workSizedPayload
	if err := json.Unmarshal([]byte(sourceModelPayload), &model); err != nil {
		return "", wrapFailure(KindInvariantViolation, op, "the size record is malformed", false, "rebuild the size projection", err)
	}
	model.ReviewBasis, err = workSizeReviewBasisTx(ctx, tx, successor, 0)
	if err != nil {
		return "", err
	}
	model.ModelVersion = WorkSizeModelVersion
	if model.Size, err = DecideWorkSize(model.ModelVersion, model.Inputs, model.ReviewBasis); err != nil {
		return "", err
	}
	sizeEvents := []Event{resizeEvent(id("sized"), WorkSized, successor, actorRef, now, workSizedPayloadVersion, model)}
	if sourceOverride != "" {
		var carried workSizeOverriddenPayload
		if err := json.Unmarshal([]byte(sourceOverridePayload), &carried); err != nil {
			return "", wrapFailure(KindInvariantViolation, op, "the operator's size record is malformed", false, "rebuild the size projection", err)
		}
		sizeEvents = append(sizeEvents, resizeEvent(id("override"), WorkSizeOverridden, successor, actorRef, now, 1,
			workSizeOverriddenPayload{ModelSequence: 1, Size: carried.Size, Reason: carried.Reason, CarriedFromEventID: sourceOverride}))
	}
	if _, err := ApplyOperationTx(ctx, transaction, Operation{Events: sizeEvents}); err != nil {
		return "", err
	}

	// The successor's pin, the forward link, and the supersession.
	var definitionRef string
	if err := tx.QueryRowContext(ctx, `SELECT definition_ref FROM workflow_instances WHERE work_id=?`, request.WorkID).Scan(&definitionRef); err != nil {
		return "", wrapFailure(KindUnavailable, op, "cannot read the workflow pin", true, "retry once the database is readable", err)
	}
	definition, err := selectWorkflowDefinitionTx(ctx, tx, definitionRef, successor)
	if err != nil {
		return "", err
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: successor, Definition: definition, Actor: request.Actor, Now: now}); err != nil {
		return "", err
	}
	actorPayload := map[string]any{"actor_ref": actorRef, "principal_ref": request.Actor.PrincipalRef, "client_ref": request.Actor.ClientRef,
		"agent_ref": request.Actor.AgentRef, "session_ref": request.Actor.SessionRef, "actor_class": string(request.Actor.ActorClass)}
	if _, err := applyWorkflowOperationTx(ctx, tx, Operation{Events: []Event{
		workflowTypedEvent(id("actor"), WorkflowActorRecorded, request.WorkID, actorRef, now, version, actorPayload),
		workflowTypedEvent(id("link"), WorkflowSuccessorLinked, request.WorkID, actorRef, now, version+1, map[string]any{
			"successor_work_id": successor, "relation_kind": "forward_link", "successor_kind": kind, "definition_ref": definition.Definition.Ref, "resize": true}),
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, request.WorkID): version}}, newFoldScope(tx)); err != nil {
		return "", err
	}
	if _, err := ApplyOperationTx(ctx, transaction, Operation{Events: []Event{
		resizeEvent(id("superseded"), "work.superseded", request.WorkID, actorRef, now, 1, workSupersededPayload{Successor: successor, Superseded: request.WorkID,
			Reason: "the work item's effective size changed", ExpectedVersion: version + 2, ResultingVersion: version + 3}),
	}}); err != nil {
		return "", err
	}
	if linked {
		if err := moveLinearIssueLinkTx(ctx, transaction, request.WorkID, successor); err != nil {
			return "", err
		}
	}
	return successor, nil
}

func resizeEvent(id, kind, subject, actor string, now time.Time, version int, payload any) Event {
	raw, _ := json.Marshal(payload)
	return Event{EventID: id, Kind: kind, SubjectType: SubjectWorkItem, SubjectID: subject, Actor: actor, OccurredAt: now, PayloadVersion: version, Payload: raw}
}

func readWorkMembershipsTx(ctx context.Context, q queryer, workID string) ([]workMembershipPayload, error) {
	rows, err := q.QueryContext(ctx, `SELECT project_id, role FROM work_projects WHERE work_id=? ORDER BY role, project_id`, workID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "work_resize", "cannot read the work item's memberships", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var memberships []workMembershipPayload
	for rows.Next() {
		var membership workMembershipPayload
		if err := rows.Scan(&membership.ProjectID, &membership.Role); err != nil {
			return nil, wrapFailure(KindUnavailable, "work_resize", "cannot scan the work item's memberships", true, "retry once the database is readable", err)
		}
		memberships = append(memberships, membership)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "work_resize", "cannot scan the work item's memberships", true, "retry once the database is readable", err)
	}
	return memberships, nil
}

// moveLinearIssueLinkTx moves the issue identity to the resize successor
// (CD-0237 amends CD-0213 D3). The table is direct authority, so replay
// never moves it again and Concord makes no Linear call.
func moveLinearIssueLinkTx(ctx context.Context, transaction *Transaction, from, to string) error {
	tx, err := transactionSQL(transaction, "linear_link_record")
	if err != nil {
		return err
	}
	scope := transaction.fold
	if scope == nil {
		scope = newFoldScope(tx)
	}
	if err := scope.enter(ctx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE linear_issue_links SET work_id=?, updated_at=? WHERE work_id=?`, to, transaction.now().Format(time.RFC3339Nano), from); err != nil {
		return wrapFailure(KindUnavailable, "linear_link_record", "cannot move the Linear issue identity", true, "retry once the database is writable", err)
	}
	return scope.close(ctx)
}
