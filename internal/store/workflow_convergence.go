package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
)

// WorkflowRetryConvergence records the stored facts that admit one escalated
// dispatch. Sequence references point into this work item's event history.
type WorkflowRetryConvergence struct {
	Basis             string `json:"basis"`
	PreviousRecordSeq int64  `json:"previous_record_seq,omitempty"`
	LatestRecordSeq   int64  `json:"latest_record_seq,omitempty"`
	SupersededSeq     int64  `json:"superseded_seq,omitempty"`
	HostPID           int    `json:"host_pid,omitempty"`
	HostPIDStart      uint64 `json:"host_pid_start,omitempty"`
}

func (s WorkflowAdmissionState) retryEscalated() bool {
	return s.CorrectionEscalated || s.NonProgressAttempts >= workflowCorrectionAttemptLimit
}

// workflowRetryConvergenceFailure is the dispatch wall's typed refusal
// (CD-0164 D5). It names which populations stand at their limits — the
// correction window, the whole-work nonprogress population, or both — and
// separates a one-use basis or funded review from a renewed window. The
// refusal states the current step and separates the historical recorded
// corrections that escalated the window from the active nonprogress
// population, and the remedy names the supersession prerequisites and the
// one fresh fenced attempt a basis buys. The
// nonprogress count runs since the applicable nonprogress anchor, which the
// obligation-satisfaction policy renews from a qualified coordinator
// satisfaction as well as a satisfying worker acceptance (CD-0164 D2a); the
// two populations keep their own comparators (D4); and the host_changed
// allowance stays one per worker-satisfaction window under both policies.
func workflowRetryConvergenceFailure(definition WorkflowDefinition, state WorkflowAdmissionState) *Failure {
	detail := "worker correction reached the three-attempt limit without a convergence basis"
	nonprogress := state.NonProgressAttempts >= workflowCorrectionAttemptLimit
	switch {
	case state.CorrectionEscalated && nonprogress:
		detail += fmt.Sprintf(": both populations block the dispatch — %d non-progress attempts since the applicable nonprogress anchor (step %s), and the correction window sits at its escalation threshold from historical recorded corrections, a distinct population from the active nonprogress window", state.NonProgressAttempts, state.Step)
	case state.CorrectionEscalated:
		detail += fmt.Sprintf(": the correction window sits at its escalation threshold from historical recorded corrections while the active nonprogress population holds %d attempts (step %s)", state.NonProgressAttempts, state.Step)
	default:
		detail += fmt.Sprintf(": %d non-progress attempts since the applicable nonprogress anchor (step %s)", state.NonProgressAttempts, state.Step)
	}
	renewal := "a satisfying worker acceptance"
	if definition.ProgressPolicy == WorkflowProgressPolicyObligationSatisfactionV1 {
		renewal = "a satisfying worker acceptance or the first qualified coordinator satisfaction"
	}
	detail += ". Dispatched attempts escalate the correction window at three or more; correction requests escalate it past three. A convergence basis or funded confirming review admits exactly one dispatch and renews no window; only " + renewal + " reopens the nonprogress window. Host recovery applies only to an eligible recorded host execution failure and an unused host basis in the current worker-satisfaction window: repair the fault, restart the host more than one minute after the failure, resume the item, then retry once"
	return newFailure(KindMissingEvidence, "worker_dispatch", detail, false,
		"record shrinking open_finding_ids, or supersede_contract with a changed approach recorded through audit_evidence and a new contract_version — one fresh fenced attempt that does not renew the three-attempt budget — or stop the work; after a host execution failure, repair and restart the host, resume the item, and dispatch once from the restarted host (one host_changed retry per worker-satisfaction window)")
}

// workflowRetryConvergenceForRegistry derives a basis rather than trusting a caller's
// assessment. The latest dispatch consumes its basis, including an authorized
// dispatch whose host worker never materialized. A split_leaf_progress basis
// is consumed by the one sibling dispatch the accepted leaf funds.
// The caller's registry resolves the split-progress gate's pinned definition;
// a nil registry selects the builtin registry.
func workflowRetryConvergenceForRegistry(ctx context.Context, q queryer, workID, stepID string, registry DefinitionRegistry) (WorkflowRetryConvergence, error) {
	var lastDispatch, superseded int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker'`, string(SubjectWorkItem), workID, WorkflowActionCompleted).Scan(&lastDispatch); err != nil {
		return WorkflowRetryConvergence{}, workflowProjectionError(err, "cannot read the latest dispatch for convergence")
	}
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND seq>?`, string(SubjectWorkItem), workID, WorkflowContractSuperseded, lastDispatch).Scan(&superseded); err != nil {
		return WorkflowRetryConvergence{}, workflowProjectionError(err, "cannot read contract supersession for convergence")
	}
	if superseded != 0 {
		return WorkflowRetryConvergence{Basis: "approach_changed", SupersededSeq: superseded}, nil
	}
	progress, progressErr := workflowSplitLeafProgressConvergenceTx(ctx, q, workID, lastDispatch, registry)
	if progressErr != nil || progress.Basis != "" {
		return progress, progressErr
	}
	history, err := workflowCorrectionWalkWorkerSatisfaction(ctx, q, workID, int64(1)<<62, "worker_convergence")
	if err != nil || len(history.records) == 0 {
		return WorkflowRetryConvergence{}, err
	}
	latest := history.records[len(history.records)-1]
	if latest.seq <= lastDispatch {
		return WorkflowRetryConvergence{}, nil
	}
	if latest.fields.ActionID == "record_worker_failure" {
		return workflowHostRetryConvergence(ctx, q, workID, lastDispatch, history.boundary)
	}
	boundary := history.boundary
	if latest.fields.ActionID == "request_correction" {
		boundary, err = workflowCorrectionActiveHealthyBaseline(ctx, q, workID, latest.seq, "worker_convergence")
		if err != nil {
			return WorkflowRetryConvergence{}, err
		}
	} else if latest.fields.StepID != stepID {
		return WorkflowRetryConvergence{}, nil
	}
	if latest.seq <= boundary {
		return WorkflowRetryConvergence{}, nil
	}
	for i := len(history.records) - 2; i >= 0; i-- {
		previous := history.records[i]
		if previous.seq <= boundary {
			break
		}
		if previous.fields.ActionID != latest.fields.ActionID || previous.fields.StepID != latest.fields.StepID {
			continue
		}
		if workflowFindingsShrink(previous.fields.openFindings(), latest.fields.openFindings()) {
			comparable, comparableErr := workflowOracleConvergenceComparableTx(ctx, q, workID, previous, latest)
			if comparableErr != nil {
				return WorkflowRetryConvergence{}, comparableErr
			}
			if !comparable {
				// The wall never widens: an incomparable pair simply
				// buys no basis, and escalation proceeds only through
				// the existing explicit supersession route.
				return WorkflowRetryConvergence{}, nil
			}
			return WorkflowRetryConvergence{Basis: "findings_shrinking", PreviousRecordSeq: previous.seq, LatestRecordSeq: latest.seq}, nil
		}
		break
	}
	return WorkflowRetryConvergence{}, nil
}

// workflowOracleConvergenceComparableTx decides whether two correction
// records with a shrinking open-set comparison are comparable at all.
// An oracle-free history keeps the legacy comparison. An
// oracle-capable history compares only under the wall's strengthened
// comparability: same recorded contract and job identity, every earlier owner,
// case, and control retained byte-identically including the pinned harness,
// and every
// dropped finding closed through a proven non-oracle-defect closure. An
// oracle-defect closure never contributes: the harness was wrong, the set
// did not truthfully shrink. Receipt preservation alone is not progress.
func workflowOracleConvergenceComparableTx(ctx context.Context, q queryer, workID string, previous, latest workflowCorrectionRecordState) (bool, error) {
	lineage, err := readWorkerOracleFindingLineageTx(ctx, q, workID)
	if err != nil {
		return false, err
	}
	if !lineage.oracleCapable {
		return true, nil
	}
	if previous.jobBinding == nil || latest.jobBinding == nil {
		return false, nil
	}
	if previous.jobBinding.JobID != latest.jobBinding.JobID {
		return false, nil
	}
	previousContract, err := readOracleJobContractVersion(ctx, q, workID, *previous.jobBinding)
	if err != nil {
		return false, err
	}
	latestContract, err := readOracleJobContractVersion(ctx, q, workID, *latest.jobBinding)
	if err != nil {
		return false, err
	}
	if previousContract == 0 || previousContract != latestContract {
		return false, nil
	}
	if !oracleControlsRetained(lineage.oracleForBinding(*previous.jobBinding), lineage.oracleForBinding(*latest.jobBinding)) {
		return false, nil
	}
	for _, id := range previous.fields.openFindings() {
		if slices.Contains(latest.fields.openFindings(), id) {
			continue
		}
		finding, exists := lineage.findings[id]
		if !exists || !finding.Closed || finding.ClosureKind != oracleClosureResolution {
			// Vanished without a proven closure — replaced, relabeled,
			// closed as an oracle defect, or closed by supersession:
			// none of it is truthful shrinkage.
			return false, nil
		}
	}
	return true, nil
}

func readOracleJobContractVersion(ctx context.Context, q queryer, workID string, binding WorkerJobBinding) (int64, error) {
	var version int64
	err := q.QueryRowContext(ctx, `SELECT json_extract(payload,'$.contract_version') FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.job_id')=? AND json_extract(payload,'$.revision')=? AND json_extract(payload,'$.digest')=?`, workID, WorkerJobRecorded, binding.JobID, binding.Revision, binding.Digest).Scan(&version)
	if err != nil {
		return 0, workflowProjectionError(err, "cannot read the oracle job's recorded contract authority")
	}
	return version, nil
}

func (f workflowCorrectionCompletionFields) openFindings() []string {
	if f.ActionID == "request_correction" {
		// On an oracle-capable history the recorded request
		// carries the derived open finding set; the legacy request keeps
		// comparing predicate ids, which were never finding ids.
		if f.OpenFindingIDs != nil {
			return f.OpenFindingIDs
		}
		return f.CorrectionPredicates
	}
	return f.OpenFindingIDs
}

func workflowFindingsShrink(previous, latest []string) bool {
	if !validWorkflowOpenFindings(previous) || !validWorkflowOpenFindings(latest) || len(latest) >= len(previous) {
		return false
	}
	for _, id := range latest {
		if !slices.Contains(previous, id) {
			return false
		}
	}
	return true
}

func validWorkflowOpenFindings(ids []string) bool {
	if len(ids) < 1 || len(ids) > 32 {
		return false
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] || !workflowCorrectionID(id) {
			return false
		}
		seen[id] = true
	}
	return true
}

func validateWorkflowOpenFindingsPayload(fields map[string]json.RawMessage) error {
	raw, present := fields["open_finding_ids"]
	if !present {
		return nil
	}
	var ids []string
	if json.Unmarshal(raw, &ids) != nil || !validWorkflowOpenFindings(ids) {
		return newFailure(KindInvalidPayload, "workflow_action", "open_finding_ids must contain 1 to 32 unique finding IDs", false, "supply the complete open finding set with stable IDs")
	}
	return nil
}

func (c WorkflowRetryConvergence) valid() bool {
	switch c.Basis {
	case "findings_shrinking":
		return c.PreviousRecordSeq > 0 && c.LatestRecordSeq > c.PreviousRecordSeq && c.SupersededSeq == 0 && c.HostPID == 0 && c.HostPIDStart == 0
	case "approach_changed":
		return c.SupersededSeq > 0 && c.PreviousRecordSeq == 0 && c.LatestRecordSeq == 0 && c.HostPID == 0 && c.HostPIDStart == 0
	case "host_changed":
		return c.LatestRecordSeq > 0 && c.PreviousRecordSeq == 0 && c.SupersededSeq == 0 && c.HostPID > 0 && c.HostPIDStart > 0
	case workflowSplitLeafProgressBasis:
		// The funding acceptance's sequence is the exact reference the
		// dispatch consumes; no supersession, host identity, or second
		// record rides this basis.
		return c.LatestRecordSeq > 0 && c.PreviousRecordSeq == 0 && c.SupersededSeq == 0 && c.HostPID == 0 && c.HostPIDStart == 0
	}
	return false
}
