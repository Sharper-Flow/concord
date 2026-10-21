package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// CD-0242 D6 retires the work-size system: no production-line cap, size
// measurement, or mandatory split applies. domain_events is append-only and
// replay refuses an unregistered kind, so each retired kind stays registered:
// its payload still decodes and its fold still advances the subject version,
// so later historical events on the same subject fold at the version they
// were written against. The retired folds write no size, override, lineage,
// or split state, and appendEvent refuses every new append of a retired kind.
// The split folds keep advancing the workflow version, which the split events
// advanced when they were written; the work-size folds never advanced a
// version, so theirs are content-free.

const (
	// WorkSized records one size decision for a work item (retired).
	WorkSized = "work.sized"
	// WorkSizeOverridden records the operator's size (retired).
	WorkSizeOverridden = "work.size_overridden"
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

// upcastWorkSizedV1 reads a first-release record unchanged: it carries no
// complete law and check set, and its checks_complete member stays absent.
func upcastWorkSizedV1(event Event) (Event, error) {
	event.PayloadVersion = 2
	return event, nil
}

type workSizedPayload struct {
	ModelVersion int             `json:"model_version"`
	Size         string          `json:"size"`
	Inputs       json.RawMessage `json:"inputs"`
	Footprint    json.RawMessage `json:"footprint"`
	ReviewBasis  []string        `json:"review_basis,omitempty"`
}

type workSizeOverriddenPayload struct {
	ModelSequence           int    `json:"model_sequence"`
	Size                    string `json:"size"`
	Reason                  string `json:"reason"`
	ApprovalRef             string `json:"approval_ref"`
	ApprovalOperationDigest string `json:"approval_operation_digest"`
	ApprovalScopeJSON       string `json:"approval_scope_json"`
	ApprovalVersionsJSON    string `json:"approval_versions_json"`
	ApprovalConsequence     string `json:"approval_consequence"`
	CarriedFromEventID      string `json:"carried_from_event_id,omitempty"`
}

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

func foldRetiredWorkSized(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var payload workSizedPayload
	return decodePayload(event, &payload)
}

func foldRetiredWorkSizeOverridden(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var payload workSizeOverriddenPayload
	return decodePayload(event, &payload)
}

func foldRetiredWorkResized(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var payload workResizedPayload
	return decodePayload(event, &payload)
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
		"capture, revise, or record without size inputs; Main sizes each assignment by complexity and relatedness")
}

// retiredEventAppendRefused routes each retired kind to the refusal that
// names the decision that retired it.
func retiredEventAppendRefused(kind string) error {
	switch kind {
	case WorkSized, WorkSizeOverridden, WorkResized, WorkerJobSplit, WorkerJobSplitDischarged:
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
