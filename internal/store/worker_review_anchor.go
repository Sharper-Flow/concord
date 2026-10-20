package store

import (
	"context"
	"encoding/json"
	"slices"
)

// reviewDeliveryAnchor retains the immutable acceptance, completion, and
// authorization identities. It identifies a delivery, not the review subject.
type reviewDeliveryAnchor struct {
	attemptID, identity, raw string
	acceptSeq, dispatchSeq   int64
	payloadVersion           int
}

func readReviewDeliveryAnchors(ctx context.Context, q queryer, workID string) ([]reviewDeliveryAnchor, error) {
	return readReviewDeliveryAnchorsBefore(ctx, q, workID, 0)
}

func readReviewDeliveryAnchorsBefore(ctx context.Context, q queryer, workID string, beforeSeq int64) ([]reviewDeliveryAnchor, error) {
	rows, err := q.QueryContext(ctx, `SELECT a.attempt_id,COALESCE(json_extract(auth.payload,'$.worker_worktree_identity'),''),
 acc.seq,auth.seq,completed.payload_version,completed.payload
 FROM domain_events acc JOIN worker_attempts a ON a.work_id=acc.subject_id
 AND a.attempt_id=json_extract(acc.payload,'$.worker_attempt_id')
 JOIN domain_events auth ON auth.subject_type=acc.subject_type AND auth.subject_id=acc.subject_id
 AND auth.kind=? AND json_extract(auth.payload,'$.action_id')='dispatch_worker'
 AND json_extract(auth.payload,'$.worker_attempt_id')=a.attempt_id
 JOIN domain_events completed ON completed.subject_type=acc.subject_type AND completed.subject_id=acc.subject_id
 AND completed.kind=? AND json_extract(completed.payload,'$.attempt_id')=a.attempt_id
 WHERE acc.subject_type='work_item' AND acc.subject_id=? AND acc.kind=?
 AND json_extract(acc.payload,'$.action_id') IN ('accept_worker_result','accept_worker_evidence')
 AND a.capability_class='implementation' AND a.lifecycle_state='completed'
 AND auth.seq<completed.seq AND completed.seq<acc.seq
 AND (?=0 OR (auth.seq<? AND completed.seq<? AND acc.seq<?))
 ORDER BY acc.seq DESC`, WorkflowActionCompleted, WorkerCompleted, workID, WorkflowActionCompleted, beforeSeq, beforeSeq, beforeSeq, beforeSeq)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, workerDispatchFreshnessOp, "cannot read accepted implementation deliveries", true, "retry once the worker projection is readable", err)
	}
	defer rows.Close()
	var anchors []reviewDeliveryAnchor
	for rows.Next() {
		var anchor reviewDeliveryAnchor
		if err := rows.Scan(&anchor.attemptID, &anchor.identity, &anchor.acceptSeq, &anchor.dispatchSeq, &anchor.payloadVersion, &anchor.raw); err != nil {
			return nil, wrapFailure(KindUnavailable, workerDispatchFreshnessOp, "cannot scan accepted implementation deliveries", true, "retry once the worker projection is readable", err)
		}
		anchors = append(anchors, anchor)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, workerDispatchFreshnessOp, "cannot enumerate accepted implementation deliveries", true, "retry once the worker projection is readable", err)
	}
	return anchors, nil
}

func selectReviewDeliveryAnchor(anchors []reviewDeliveryAnchor, identity string) *reviewDeliveryAnchor {
	for i := range anchors {
		if anchors[i].identity != "" && anchors[i].identity == identity {
			return &anchors[i]
		}
	}
	return nil
}

func (a *reviewDeliveryAnchor) cleanCommit() (string, error) {
	if a == nil {
		return "", newFailure(KindUnauthorizedDispatch, workerDispatchFreshnessOp, "no accepted implementation delivery on this worktree claim can host this review", false, "dispatch the implementation lane, accept its clean delivery, then dispatch the review")
	}
	if a.payloadVersion < workerCompletedTerminalDeliveryVersion {
		return "", newFailure(KindUnauthorizedDispatch, workerDispatchFreshnessOp, "the latest accepted implementation delivery predates the core delivery observation; record a fresh implementation attempt and accept it before review", false, "dispatch a fresh implementation attempt so the core observes its delivery, then dispatch the review")
	}
	var payload WorkerCompletedPayload
	if err := json.Unmarshal([]byte(a.raw), &payload); err != nil {
		return "", newFailure(KindInvariantViolation, workerDispatchFreshnessOp, "the accepted implementation completion is malformed", false, "rebuild projections from the event log")
	}
	if payload.ObservedDelivery == nil || payload.ObservedDelivery.State != "clean" {
		return "", newFailure(KindUnauthorizedDispatch, workerDispatchFreshnessOp, "the latest accepted implementation delivery carries no clean core delivery observation; record a fresh implementation attempt and accept it before review", false, "dispatch a fresh implementation attempt so the core observes its clean delivery, then dispatch the review")
	}
	if !worktreeSHAPattern.MatchString(payload.ObservedDelivery.Commit) {
		return "", newFailure(KindInvariantViolation, workerDispatchFreshnessOp, "the accepted implementation delivery has no valid commit", false, "rebuild projections from the event log")
	}
	return payload.ObservedDelivery.Commit, nil
}

// No lifecycle filter: an authorization spends editing authority even when no
// native worker starts, or the worker fails, is rejected, or is abandoned.
func newerEditingDispatch(ctx context.Context, q queryer, workID string, afterSeq int64) (bool, error) {
	return newerEditingDispatchBefore(ctx, q, workID, afterSeq, 0)
}

func newerEditingDispatchBefore(ctx context.Context, q queryer, workID string, afterSeq, beforeSeq int64) (bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT COALESCE(json_extract(e.payload,'$.worker_lane_id'),a.lane_id),
 COALESCE(json_extract(e.payload,'$.worker_lane_version'),a.lane_version),
 COALESCE(json_extract(e.payload,'$.worker_lane_digest'),a.lane_digest)
 FROM domain_events e LEFT JOIN worker_attempts a ON a.work_id=e.subject_id
 AND a.attempt_id=json_extract(e.payload,'$.worker_attempt_id')
 WHERE e.subject_type='work_item' AND e.subject_id=? AND e.kind=? AND e.seq>?
 AND json_extract(e.payload,'$.action_id')='dispatch_worker' AND (?=0 OR e.seq<?) ORDER BY e.seq`, workID, WorkflowActionCompleted, afterSeq, beforeSeq, beforeSeq)
	if err != nil {
		return false, wrapFailure(KindUnavailable, workerDispatchFreshnessOp, "cannot read newer dispatch authorities", true, "retry once the event log is readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, digest string
		var version int64
		if err := rows.Scan(&id, &version, &digest); err != nil {
			return false, wrapFailure(KindUnavailable, workerDispatchFreshnessOp, "cannot read a newer dispatch lane", true, "retry once the event log is readable", err)
		}
		lane, err := builtinLaneRegistry().Lookup(id, version, digest)
		if err != nil {
			return false, err
		}
		if slices.Contains(lane.Capabilities, "edit_scoped_files") {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, wrapFailure(KindUnavailable, workerDispatchFreshnessOp, "cannot enumerate newer dispatch authorities", true, "retry once the event log is readable", err)
	}
	return false, nil
}
