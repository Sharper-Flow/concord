package store

import (
	"context"
	"encoding/json"
	"sort"
)

// CD-0229 D5: "A `larger` change whose second review still finds one inside
// its scope has proved bigger." The proof derives from recorded events only.
// It is a review acceptance (accept_worker_evidence or accept_worker_result
// of a completed review attempt) that falls after the item's latest size
// record while the effective size is larger, follows an earlier accepted
// review and a later checkpoint_refine under the same contract version, and
// whose report still carries a qualifying finding.
//
// Severity reads high as P0/P1 and medium as P2 (CD-0230 D4). A finding
// qualifies when its oracle tie blocks delivery (it is not a follow-up), it
// is P0-P2, and the same report does not resolve it; an oracle-free finding
// has no scope classification and qualifies at P0-P2 in a correction_required
// or revision_required report.

// RecoveryWorkSizeRerun names the one exit of the CD-0229 D6 stop.
const RecoveryWorkSizeRerun = "rerun the size model through revise_intent with size_inputs"

var workSizeProofSeverities = map[string]bool{"P0": true, "P1": true, "P2": true}

// workSizeProofEvent is one recorded review acceptance in the proof window.
type workSizeProofEvent struct {
	seq       int64
	eventID   string
	kind      string
	actionID  string
	attemptID string
	review    bool
}

// deriveWorkSizeProofsTx lists, in log order, the event IDs that prove the
// item bigger since its latest size record. Without a larger effective size
// it lists none. A positive before reads only the log before that sequence,
// so a replayed size record sees the log its live fold saw.
func deriveWorkSizeProofsTx(ctx context.Context, q queryer, workID string, before int64) ([]string, error) {
	decision, sized, err := readEffectiveWorkSize(ctx, q, workID)
	if err != nil || !sized || decision.Effective != WorkSizeLarger {
		return nil, err
	}
	var boundary int64
	// The folded records bound the window, so a size record being folded
	// reads the window its predecessor opened.
	if err := q.QueryRowContext(ctx, `SELECT coalesce(max(e.seq),0) FROM work_size_assessments s
		JOIN domain_events e ON e.event_id=s.event_id WHERE s.work_id=?`, workID).Scan(&boundary); err != nil {
		return nil, wrapFailure(KindUnavailable, "work_size", "cannot read the latest size record", true, "retry once the event log is readable", err)
	}
	rows, err := q.QueryContext(ctx, `SELECT e.seq, e.event_id, e.kind,
		coalesce(json_extract(e.payload,'$.action_id'),''), coalesce(json_extract(e.payload,'$.worker_attempt_id'),''),
		EXISTS(SELECT 1 FROM worker_attempts a WHERE a.work_id=e.subject_id AND a.attempt_id=json_extract(e.payload,'$.worker_attempt_id')
			AND a.capability_class='review' AND a.lifecycle_state='completed')
		FROM domain_events e
		WHERE e.subject_type='work_item' AND e.subject_id=? AND e.seq>? AND (?<=0 OR e.seq<?) AND e.kind IN (?,?,?)
		ORDER BY e.seq`, workID, boundary, before, before, WorkflowActionCompleted, WorkflowContractApproved, WorkflowContractSuperseded)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "work_size", "cannot read the review window", true, "retry once the event log is readable", err)
	}
	var events []workSizeProofEvent
	for rows.Next() {
		var event workSizeProofEvent
		if err := rows.Scan(&event.seq, &event.eventID, &event.kind, &event.actionID, &event.attemptID, &event.review); err != nil {
			rows.Close()
			return nil, wrapFailure(KindUnavailable, "work_size", "cannot scan the review window", true, "retry once the event log is readable", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, wrapFailure(KindUnavailable, "work_size", "cannot scan the review window", true, "retry once the event log is readable", err)
	}
	rows.Close()

	var proofs []string
	// An accepted review opens a pass; a refine checkpoint after it arms
	// the next accepted review; a contract change resets both.
	accepted, refined := false, false
	for _, event := range events {
		switch {
		case event.kind != WorkflowActionCompleted:
			accepted, refined = false, false
		case event.actionID == "checkpoint_refine":
			refined = accepted
		case event.review && (event.actionID == "accept_worker_evidence" || event.actionID == "accept_worker_result"):
			if refined {
				qualifies, err := workSizeReviewFindsInScopeTx(ctx, q, workID, event.attemptID, event.seq)
				if err != nil {
					return nil, err
				}
				if qualifies {
					proofs = append(proofs, event.eventID)
				}
			}
			accepted, refined = true, false
		}
	}
	return proofs, nil
}

// workSizeReviewFindsInScopeTx reads the accepted review attempt's report
// recorded before its acceptance and reports whether it still carries a
// qualifying finding.
func workSizeReviewFindsInScopeTx(ctx context.Context, q queryer, workID, attemptID string, accepted int64) (bool, error) {
	var seq int64
	var raw string
	err := q.QueryRowContext(ctx, `SELECT seq, payload FROM domain_events
		WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.attempt_id')=? AND seq<?
		ORDER BY seq DESC LIMIT 1`, workID, WorkerCompleted, attemptID, accepted).Scan(&seq, &raw)
	if err != nil {
		return false, wrapFailure(KindUnavailable, "work_size", "cannot read the accepted review report", true, "retry once the event log is readable", err)
	}
	var report WorkerCompletedPayload
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		return false, newFailure(KindInvariantViolation, "work_size", "a stored review report is malformed", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	if report.Review == nil {
		return false, nil
	}
	resolved := map[string]bool{}
	for _, resolution := range report.Review.ResolvedFindings {
		resolved[resolution.FindingID] = true
	}
	for index, finding := range report.Review.Findings {
		if !workSizeProofSeverities[finding.Severity] {
			continue
		}
		if finding.Oracle == nil {
			if report.Review.Disposition == "correction_required" || report.Review.Disposition == "revision_required" {
				return true, nil
			}
			continue
		}
		if finding.Oracle.Classification == OracleClassificationFollowUp {
			continue
		}
		id := finding.Oracle.ContinuesFindingID
		if id == "" {
			id = workerRankedFindingID(seq, len(report.ContextFindings), index)
		}
		if !resolved[id] {
			return true, nil
		}
	}
	return false, nil
}

// workSizeRecordedBasisTx lists every proof an earlier size record on this
// item names. A proof stays recorded after the fix: the change has proved bigger.
func workSizeRecordedBasisTx(ctx context.Context, q queryer, workID string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT basis.value FROM work_size_assessments s
		JOIN domain_events e ON e.event_id=s.event_id, json_each(coalesce(json_extract(e.payload,'$.review_basis'),'[]')) basis WHERE s.work_id=?`, workID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "work_size", "cannot read the recorded review basis", true, "retry once the event log is readable", err)
	}
	defer rows.Close()
	var basis []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, wrapFailure(KindUnavailable, "work_size", "cannot scan the recorded review basis", true, "retry once the event log is readable", err)
		}
		basis = append(basis, id)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "work_size", "cannot scan the recorded review basis", true, "retry once the event log is readable", err)
	}
	return basis, nil
}

// workSizeReviewBasisTx is the exact basis the next size record of the item
// carries: every proof derived now and every proof recorded earlier on its
// work item, sorted and distinct. A positive before bounds the derivation as
// deriveWorkSizeProofsTx does.
func workSizeReviewBasisTx(ctx context.Context, q queryer, workID string, before int64) ([]string, error) {
	derived, err := deriveWorkSizeProofsTx(ctx, q, workID, before)
	if err != nil {
		return nil, err
	}
	recorded, err := workSizeRecordedBasisTx(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	basis := []string{}
	for _, id := range append(derived, recorded...) {
		if !seen[id] {
			seen[id] = true
			basis = append(basis, id)
		}
	}
	sort.Strings(basis)
	return basis, nil
}
