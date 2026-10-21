package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// CD-0242 D6 retires assignment caps and resize lineage: no production-line cap, size
// measurement, or mandatory split applies. domain_events is append-only and
// replay refuses an unregistered kind, so each retired kind stays registered:
// its payload still decodes and its fold still advances the subject version,
// so later historical events on the same subject fold at the version they
// were written against. The retired folds write no lineage
// or split state, and appendEvent refuses every new append of a retired kind.
// The split folds keep advancing the workflow version, which the split events
// advanced when they were written; the resize fold never advanced a version.

const (
	// WorkResized records one resize (retired).
	WorkResized = "work.resized"
	// WorkerJobSplit records one atomic split of a worker-job revision (retired).
	WorkerJobSplit = "worker.job_split"
	// WorkerJobSplitDischarged records one split's discharge (retired).
	WorkerJobSplitDischarged = "worker.job_split_discharged"
)

// The retired payload shapes decode the history each kind may already carry.
// Deeply nested members decode as raw JSON: the retired folds read none of
// them, and a raw member keeps the registered decode from re-implementing a
// retired nested schema.

type workResizeEvidenceSource struct {
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
	EvidenceSource         workResizeEvidenceSource `json:"evidence_source"`
	LinearIdentity         *LinearIssueLink         `json:"linear_identity,omitempty"`
}

type WorkerJobSplitPayload struct {
	WorkflowVersionFields
	Predecessor WorkerJobBinding `json:"predecessor"`
	BaseCommit  string           `json:"base_commit,omitempty"`
	// Successors, Allocation, and ReplacedAttempt decode as raw JSON: the
	// split is retired, so no fold reads their nested content.
	Successors      json.RawMessage `json:"successors"`
	Allocation      json.RawMessage `json:"allocation"`
	ReplacedAttempt json.RawMessage `json:"replaced_attempt,omitempty"`
}

type WorkerJobSplitDischargedPayload struct {
	WorkflowVersionFields
	SplitID     string           `json:"split_id"`
	Predecessor WorkerJobBinding `json:"predecessor"`
	// Leaves and Integration decode as raw JSON for the same reason.
	Leaves      json.RawMessage `json:"leaves"`
	Integration json.RawMessage `json:"integration"`
}

func foldRetiredWorkResized(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var payload workResizedPayload
	return decodePayload(event, &payload)
}

// retiredResizeSizeBasisTx reads the immutable source snapshot of a historical
// successor's first size record. It restores no lineage projection or writer.
func retiredResizeSizeBasisTx(ctx context.Context, q queryer, event Event) ([]string, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT s.payload FROM domain_events r
		JOIN domain_events s ON s.event_id=json_extract(r.payload,'$.source_model_event_id')
		AND s.kind=? AND s.subject_type='work_item' AND s.subject_id=r.subject_id AND s.seq<r.seq
		WHERE r.kind=? AND r.subject_type='work_item' AND r.seq<?
		AND json_extract(r.payload,'$.successor_work_id')=?
		AND json_extract(r.payload,'$.successor_model_event_id')=?`, WorkSized, WorkResized, event.Seq, event.SubjectID, event.EventID).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "fold_event", "cannot read the historical size snapshot", true, "retry once the event log is readable", err)
	}
	var source struct {
		ReviewBasis []string `json:"review_basis"`
	}
	if err := json.Unmarshal([]byte(raw), &source); err != nil {
		return nil, wrapFailure(KindInvariantViolation, "fold_event", "historical size snapshot is malformed", false, "repair the recorded history", err)
	}
	return source.ReviewBasis, nil
}

func foldRetiredWorkerJobSplit(ctx context.Context, tx *sql.Tx, event Event) error {
	var payload WorkerJobSplitPayload
	if err := decodeClosedWorkerPayload(event, &payload); err != nil {
		return err
	}
	return advanceWorkflowVersion(ctx, tx, event, payload.WorkflowVersionFields)
}

func foldRetiredWorkerJobSplitDischarged(ctx context.Context, tx *sql.Tx, event Event) error {
	var payload WorkerJobSplitDischargedPayload
	if err := decodeClosedWorkerPayload(event, &payload); err != nil {
		return err
	}
	return advanceWorkflowVersion(ctx, tx, event, payload.WorkflowVersionFields)
}

// retireSizeEventAppendRefused names the decision that retired the size
// kinds. The planning kinds keep their own message in
// retiredEventAppendRefused.
func retireSizeEventAppendRefused(kind string) error {
	return newFailure(KindInvalidOperation, "apply_operation",
		"event kind "+kind+" is retired by CD-0242 D6: no production-line cap, size measurement, or mandatory split applies", false,
		"revise the work size in place; Main sizes each assignment by complexity and relatedness")
}

// retiredEventAppendRefused routes each retired kind to the refusal that
// names the decision that retired it.
func retiredEventAppendRefused(kind string) error {
	switch kind {
	case WorkResized, WorkerJobSplit, WorkerJobSplitDischarged:
		return retireSizeEventAppendRefused(kind)
	}
	return planningRetiredEventAppendRefused(kind)
}

// WorkerJobMeasuredPath is one measured inventory entry of the retired size
// system (CD-0225 D1): a normalized relative repository path and its
// production, test, documentation, or generated classification. It decodes
// the measurement content a stored acceptance may carry; no live route
// records or reads it.
type WorkerJobMeasuredPath struct {
	Path         string `json:"path"`
	Class        string `json:"class"`
	GeneratorRef string `json:"generator_ref,omitempty"`
}

// retiredWorkerCompletedProductionCountMax is the closed count bound the
// retired report member validated against; it survives only to hold the
// stored members' shape on replay.
const retiredWorkerCompletedProductionCountMax = 2147483647
