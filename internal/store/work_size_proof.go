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

// deriveWorkSizeProofsTx reads the current pre-write proof window. Admitted
// writers advance the window only after validation has consumed this state.
func deriveWorkSizeProofsTx(ctx context.Context, q queryer, workID string) ([]string, error) {
	decision, sized, err := readEffectiveWorkSize(ctx, q, workID)
	if err != nil || !sized || decision.Effective != WorkSizeLarger {
		return nil, err
	}
	state, err := readWorkSizeProofFacts(ctx, q, workID)
	return state.Proofs, err
}

func workSizeAssessmentFrontierTx(ctx context.Context, q queryer, workID string) (int64, error) {
	var boundary int64
	if err := q.QueryRowContext(ctx, `SELECT coalesce(max(effect_seq),0) FROM work_size_assessments WHERE work_id=?`, workID).Scan(&boundary); err != nil {
		return 0, wrapFailure(KindUnavailable, "work_size", "cannot read the latest size record", true, "retry once the database is readable", err)
	}
	return boundary, nil
}

// workSizeReviewFindsInScopeTx reads the accepted review attempt's report
// recorded before its acceptance and reports whether it still carries a
// qualifying finding.
func workSizeReviewFindsInScopeTx(ctx context.Context, q queryer, workID, attemptID string, accepted int64) (bool, error) {
	var seq int64
	var raw string
	err := q.QueryRowContext(ctx, `SELECT terminal_seq, terminal_payload FROM worker_attempts
		WHERE work_id=? AND attempt_id=? AND terminal_kind=? AND terminal_seq<?`, workID, attemptID, WorkerCompleted, accepted).Scan(&seq, &raw)
	if err != nil {
		return false, wrapFailure(KindUnavailable, "work_size", "cannot read the accepted review report", true, "retry once the database is readable", err)
	}
	return workSizeReviewFindsInScope(raw, seq)
}

func workSizeReviewFindsInScope(raw string, seq int64) (bool, error) {
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
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT basis.value FROM work_size_assessments s,
		json_each(s.review_basis) basis WHERE s.work_id=?`, workID)
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
// work item, sorted and distinct. The window is the current pre-write state.
func workSizeReviewBasisTx(ctx context.Context, q queryer, workID string) ([]string, error) {
	derived, err := deriveWorkSizeProofsTx(ctx, q, workID)
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
