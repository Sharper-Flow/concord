package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"strconv"
)

const continuityMaxOffset = 1000000

// WorkflowInstance values on ContinuitySnapshot. Presence is the pinned
// default; absence is typed, never inferred from an empty step.
const (
	WorkflowInstancePresent = "present"
	WorkflowInstanceAbsent  = "absent"
)

type ContextCheckpoint struct {
	CheckpointID     string   `json:"checkpoint_id"`
	WorkVersion      int64    `json:"work_version"`
	Sequence         int64    `json:"sequence"`
	StepID           string   `json:"step_id"`
	AttemptEpoch     int64    `json:"attempt_epoch"`
	ActiveUnit       string   `json:"active_unit"`
	Hypothesis       string   `json:"hypothesis"`
	Diagnosis        string   `json:"diagnosis"`
	Strategy         string   `json:"strategy"`
	TouchedRefs      []string `json:"touched_refs"`
	EvidenceRefs     []string `json:"evidence_refs"`
	PendingQuestions []string `json:"pending_questions"`
	PendingDecisions []string `json:"pending_decisions"`
}

type WorkflowDesignDecision struct {
	ID        string   `json:"id"`
	Question  string   `json:"question"`
	Choice    string   `json:"choice"`
	Rationale string   `json:"rationale"`
	Rejected  []string `json:"rejected"`
}

type WorkflowDesignRecord struct {
	WorkVersion int64                    `json:"work_version"`
	Approach    string                   `json:"approach"`
	Decisions   []WorkflowDesignDecision `json:"decisions"`
	TouchedRefs []string                 `json:"touched_refs"`
	RecordedAt  string                   `json:"recorded_at"`
}

type WorkflowProposalRecord struct {
	WorkVersion   int64    `json:"work_version"`
	Problem       string   `json:"problem"`
	Affected      []string `json:"affected"`
	Stakes        string   `json:"stakes"`
	UserOutcomes  []string `json:"user_outcomes"`
	Constraints   []string `json:"constraints"`
	OpenQuestions []string `json:"open_questions"`
	RecordedAt    string   `json:"recorded_at"`
}

type ContextBoundary struct {
	BoundaryID         string `json:"boundary_id"`
	Sequence           int64  `json:"sequence"`
	Kind               string `json:"kind"`
	CheckpointID       string `json:"checkpoint_id"`
	CheckpointSequence int64  `json:"checkpoint_sequence"`
	Summary            string `json:"summary"`
	RecordedAt         string `json:"recorded_at"`
}

type ContinuitySnapshot struct {
	WorkID          string   `json:"work_id"`
	ProductIdentity []string `json:"product_identity"`
	// WorkflowInstance states workflow-instance presence as typed
	// information: WorkflowInstancePresent for the pinned default every
	// captured item carries, WorkflowInstanceAbsent for an imported or
	// otherwise instance-less work item the read still answers for.
	// WorkflowStep is empty exactly when this is WorkflowInstanceAbsent.
	WorkflowInstance       string                  `json:"workflow_instance"`
	WorkflowStep           string                  `json:"workflow_step"`
	StepActions            []string                `json:"step_actions"`
	Contract               *WorkflowReadContract   `json:"contract"`
	SpecMandate            []string                `json:"spec_mandate"`
	LatestCheckpoint       *ContextCheckpoint      `json:"latest_checkpoint"`
	DesignRecord           *WorkflowDesignRecord   `json:"design_record"`
	ProposalRecord         *WorkflowProposalRecord `json:"proposal_record"`
	Boundaries             []ContextBoundary       `json:"boundaries"`
	BoundaryCount          int64                   `json:"boundary_count"`
	NextCursor             *string                 `json:"next_cursor"`
	Watermark              string                  `json:"watermark"`
	SourceVersionWatermark int64                   `json:"-"`
	// NativeRuns carries the attributed native-run reports for this work
	// item, newest phase per run, with reporter, subject, evidence, and both
	// times alongside the status (CD-0039 D1/D4).
	NativeRuns               []NativeRunReport            `json:"native_runs"`
	RestartAvailable         bool                         `json:"restart_available"`
	RestartUnavailableReason string                       `json:"restart_unavailable_reason"`
	ChangesProductTruth      bool                         `json:"changes_product_truth"`
	ArchitectureBinding      *WorkflowArchitectureBinding `json:"architecture_binding,omitempty"`
	WorkPin                  *WorkPin                     `json:"work_pin,omitempty"`
	// PendingProjectHandoff carries the frontier handoff addressed to the
	// reading session's Project under the active contract, consumed or not,
	// so every session of the receiving Project reads the bounded job
	// (CD-0182 D5). A read with no Project, or whose Project no handoff
	// addresses, carries the work's newest unconsumed handoff. Nil when
	// neither stands.
	PendingProjectHandoff *ProjectHandoff `json:"pending_project_handoff,omitempty"`
	// LawContext resolves the approved contract's binding law and Domains
	// against the verified committed law folder and Domain registry at read
	// time. Nil when the contract binds no law and no Domain.
	LawContext *WorkflowLawContext `json:"law_context,omitempty"`
	// WorkContext and WorkContextUnavailable carry the shared projection.
	WorkContext            *WorkContextView        `json:"work_context,omitempty"`
	WorkContextUnavailable *WorkContextUnavailable `json:"work_context_unavailable,omitempty"`
}

type ContinuityRequest struct {
	Work   string
	Limit  int
	Cursor string
	// ProjectID is the reading session's authenticated Project. It selects
	// the handoff frontier addressed to that Project.
	ProjectID string
}

func ReadWorkflowContinuity(ctx context.Context, s *Store, req ContinuityRequest) (ContinuitySnapshot, error) {
	var out ContinuitySnapshot
	if s == nil || s.db == nil {
		return out, newFailure(KindUnavailable, "C19.Continuity", "store is not open", false, "open the authority database")
	}
	if len(req.Work) < 2 || len(req.Work) > 128 {
		return out, newFailure(KindInvalidPayload, "C19.Continuity", "work ID is out of bounds", false, "supply one bounded work ID")
	}
	if req.Limit <= 0 {
		req.Limit = 20
	}
	if req.Limit > 20 {
		return out, newFailure(KindInvalidPayload, "C19.Continuity", "continuity limit/page.limit exceeds maximum 20", false, "supply a limit of at most 20")
	}
	offset, err := continuityOffset(req.Cursor, req.Work)
	if err != nil {
		return out, err
	}
	// A contract that binds law or Domains reads the verified law-folder
	// snapshot inside the transaction, so its proof is prepared before the
	// transaction opens (CD-0195 D2).
	verifiedCtx, proofErr := s.PrepareWorkLawProof(ctx, req.Work)
	if proofErr != nil {
		return out, proofErr
	}
	ctx = verifiedCtx
	tx, err := beginReadTx(ctx, s.db)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "C19.Continuity", "cannot open a consistent continuity snapshot", true, "retry once the database is readable", err)
	}
	defer tx.Rollback()
	out.SourceVersionWatermark, err = storeRevision(ctx, tx)
	if err != nil {
		return out, err
	}
	out, err = continuityReadIdentityTx(ctx, tx, req.Work, out)
	if err != nil {
		return out, err
	}
	if err := continuityReadManagedHistoryTx(ctx, tx, req, offset, &out); err != nil {
		return out, err
	}
	if err := tx.Rollback(); err != nil {
		return out, err
	}
	return out, nil
}

func continuityReadIdentityTx(ctx context.Context, tx *sql.Tx, workID string, out ContinuitySnapshot) (ContinuitySnapshot, error) {
	out.WorkID = workID
	out.Boundaries = []ContextBoundary{}
	out.ProductIdentity = []string{}
	out.SpecMandate = []string{}
	out.StepActions = []string{}
	if err := publishedWorkAbsenceQ(ctx, tx, workID, "C19.Continuity"); err != nil {
		return out, err
	}
	exists, err := workExistsCore(ctx, tx, workID)
	if err != nil {
		return out, err
	}
	if !exists {
		return out, newFailure(KindProjectionNotFound, "C19.Continuity", "work item is not recorded", false, "reread_entities")
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT pp.product_id FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=? ORDER BY pp.product_id LIMIT 65`, workID)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "C19.Continuity", "cannot read Product identity", true, "retry once the database is readable", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return out, err
		}
		out.ProductIdentity = append(out.ProductIdentity, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	if len(out.ProductIdentity) > 64 {
		return out, newFailure(KindLimitExceeded, "C19.Continuity", "Product identity exceeds the continuity snapshot bound", false, "reduce_limit")
	}
	return out, nil
}

func continuityReadManagedHistoryTx(ctx context.Context, tx *sql.Tx, req ContinuityRequest, offset int, out *ContinuitySnapshot) error {
	err := readContinuityInstanceTx(ctx, tx, req.Work, out)
	if err != nil {
		return err
	}
	activeContractVersion, contractErr := activeWorkflowContractVersion(ctx, tx, req.Work, "C19.Continuity")
	if contractErr != nil && contractErr != sql.ErrNoRows {
		return contractErr
	}
	if err := continuityReadContractTx(ctx, tx, req.Work, out, activeContractVersion); err != nil {
		return err
	}
	if err := continuityReadCheckpointTx(ctx, tx, req.Work, out); err != nil {
		return err
	}
	// The handoff rides the snapshot when present; the absent field keeps a
	// handoff-free work's bytes unchanged.
	out.PendingProjectHandoff, err = selectProjectHandoffTx(ctx, tx, req.Work, req.ProjectID, activeContractVersion)
	if err != nil {
		return err
	}
	out.DesignRecord, err = readCurrentWorkflowDesign(ctx, tx, req.Work)
	if err != nil {
		return err
	}
	if out.ProposalRecord, err = readLatestWorkflowProposal(ctx, tx, req.Work, "C19.Continuity"); err != nil {
		return err
	}
	if err := continuityReadBoundariesTx(ctx, tx, req.Work, out, req.Limit, offset); err != nil {
		return err
	}
	if err := continuityReadTrailingTx(ctx, tx, req.Work, out); err != nil {
		return err
	}
	view, unavailable, viewErr := readWorkContextProjection(ctx, tx, req.Work)
	if viewErr != nil {
		return viewErr
	}
	out.WorkContext, out.WorkContextUnavailable = view, unavailable
	return nil
}

func selectProjectHandoffTx(ctx context.Context, tx *sql.Tx, work, project string, version int64) (*ProjectHandoff, error) {
	handoff, err := addressedProjectHandoffFrontierTx(ctx, tx, work, project, version)
	if err != nil || handoff != nil {
		return handoff, err
	}
	return PendingProjectHandoffTx(ctx, tx, work)
}

// continuityReadContractTx reads the one active contract and every enrichment
// that hangs off it: predicates, architecture binding, self-repair and current
// law context. A work item with no contract row leaves the snapshot without one.
func continuityReadContractTx(ctx context.Context, tx *sql.Tx, work string, out *ContinuitySnapshot, activeContractVersion int64) error {
	contract, err := readWorkflowContractVersion(ctx, tx, work, activeContractVersion)
	if err == nil {
		plan, planErr := readWorkflowPlanTx(ctx, tx, work)
		if planErr != nil {
			return planErr
		}
		out.ChangesProductTruth = workflowCurrentProductTruth(&contract, plan)
		out.ArchitectureBinding = contract.ArchitectureBinding
		out.Contract = &contract
		lawContext, lawErr := readWorkflowLawContext(ctx, tx, work, &contract)
		if lawErr != nil {
			return lawErr
		}
		out.LawContext = lawContext
		out.SpecMandate = nonNilStrings(append([]string(nil), contract.SpecMandate...))
	} else if err != sql.ErrNoRows {
		return err
	}
	return nil
}

// readLatestContextCheckpointTx reads the newest checkpoint inside the
// caller's transaction. Malformed stored arrays are an invariant violation.
func readLatestContextCheckpointTx(ctx context.Context, q queryer, workID, operation string) (*ContextCheckpoint, error) {
	var checkpoint ContextCheckpoint
	var touched, evidence, questions, decisions string
	if err := q.QueryRowContext(ctx, `SELECT checkpoint_id,work_version,checkpoint_sequence,step_id,attempt_epoch,active_unit,hypothesis,diagnosis,strategy,touched_refs,evidence_refs,pending_questions,pending_decisions FROM workflow_context_checkpoints WHERE work_id=? ORDER BY checkpoint_sequence DESC LIMIT 1`, workID).Scan(&checkpoint.CheckpointID, &checkpoint.WorkVersion, &checkpoint.Sequence, &checkpoint.StepID, &checkpoint.AttemptEpoch, &checkpoint.ActiveUnit, &checkpoint.Hypothesis, &checkpoint.Diagnosis, &checkpoint.Strategy, &touched, &evidence, &questions, &decisions); err == nil {
		if json.Unmarshal([]byte(touched), &checkpoint.TouchedRefs) != nil || json.Unmarshal([]byte(evidence), &checkpoint.EvidenceRefs) != nil || json.Unmarshal([]byte(questions), &checkpoint.PendingQuestions) != nil || json.Unmarshal([]byte(decisions), &checkpoint.PendingDecisions) != nil {
			return nil, newFailure(KindInvariantViolation, operation, "context checkpoint projection contains malformed arrays", false, "restore a verified backup into a new path with concord restore, then swap")
		}
		return &checkpoint, nil
	} else if err != sql.ErrNoRows {
		return nil, wrapFailure(KindUnavailable, operation, "cannot read latest context checkpoint", true, "retry once the database is readable", err)
	}
	return nil, nil
}

// continuityReadCheckpointTx reads the latest context checkpoint onto the
// snapshot through the shared tx-scoped reader.
func continuityReadCheckpointTx(ctx context.Context, tx *sql.Tx, work string, out *ContinuitySnapshot) error {
	checkpoint, err := readLatestContextCheckpointTx(ctx, tx, work, "C19.Continuity")
	if err != nil {
		return err
	}
	out.LatestCheckpoint = checkpoint
	return nil
}

// continuityReadBoundariesTx reads one history page of context boundaries and
// derives the next cursor from the page's last retained boundary.
func continuityReadBoundariesTx(ctx context.Context, tx *sql.Tx, work string, out *ContinuitySnapshot, limit, offset int) error {
	var total int64
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_context_boundaries WHERE work_id=?`, work).Scan(&total); err != nil {
		return wrapFailure(KindUnavailable, "C19.Continuity", "cannot count context boundaries", true, "retry once the database is readable", err)
	}
	out.BoundaryCount = total
	rows, err := tx.QueryContext(ctx, `SELECT boundary_id,boundary_sequence,boundary_kind,checkpoint_id,checkpoint_sequence,summary,recorded_at FROM workflow_context_boundaries WHERE work_id=? ORDER BY boundary_sequence DESC LIMIT ? OFFSET ?`, work, limit+1, offset)
	if err != nil {
		return wrapFailure(KindUnavailable, "C19.Continuity", "cannot read context boundary history", true, "retry once the database is readable", err)
	}
	for rows.Next() {
		var item ContextBoundary
		if err := rows.Scan(&item.BoundaryID, &item.Sequence, &item.Kind, &item.CheckpointID, &item.CheckpointSequence, &item.Summary, &item.RecordedAt); err != nil {
			rows.Close()
			return err
		}
		out.Boundaries = append(out.Boundaries, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return wrapFailure(KindUnavailable, "C19.Continuity", "cannot finish reading context boundary history", true, "retry once the database is readable", err)
	}
	rows.Close()
	if len(out.Boundaries) > limit {
		out.limitBoundaries(work, offset, limit)
	}
	return nil
}

// limitBoundaries keeps the first limit boundaries of the page read at
// offset and derives the continuation cursor from the last retained one, so
// no boundary drops without a cursor that resumes at it.
func (snapshot *ContinuitySnapshot) limitBoundaries(work string, offset, limit int) {
	last := snapshot.Boundaries[limit-1]
	snapshot.Boundaries = snapshot.Boundaries[:limit]
	raw, _ := json.Marshal(map[string]any{"v": 1, "work": work, "offset": offset + limit, "last": last.Sequence})
	next := base64.RawURLEncoding.EncodeToString(raw)
	snapshot.NextCursor = &next
}

// LimitWindow keeps a complete boundary prefix of one captured snapshot,
// with a cursor that resumes at its first dropped boundary.
func (snapshot ContinuitySnapshot) LimitWindow(req ContinuityRequest, boundaries int) (ContinuitySnapshot, error) {
	// A boundary page that keeps no boundary cannot advance its cursor, so a
	// non-empty page keeps at least one.
	if boundaries < 1 {
		return snapshot, newFailure(KindInvalidPayload, "C19.Continuity", "continuity window bounds are out of range", false, "keep at least one boundary")
	}
	if boundaries < len(snapshot.Boundaries) {
		offset, err := continuityOffset(req.Cursor, req.Work)
		if err != nil {
			return snapshot, err
		}
		snapshot.Boundaries = append([]ContextBoundary{}, snapshot.Boundaries...)
		snapshot.limitBoundaries(req.Work, offset, boundaries)
	}
	return snapshot, nil
}

// continuityReadTrailingTx fills the snapshot's trailing scalar and list
// fields: watermark, restart availability, and native runs.
func continuityReadTrailingTx(ctx context.Context, tx *sql.Tx, work string, out *ContinuitySnapshot) error {
	watermark, err := readWorkEffectSeq(ctx, tx, work)
	if err != nil {
		return wrapFailure(KindUnavailable, "C19.Continuity", "cannot read continuity watermark", true, "retry once the database is readable", err)
	}
	out.Watermark = "seq:" + strconv.FormatInt(watermark, 10)
	out.RestartAvailable = false
	out.RestartUnavailableReason = "typed restart is deliberately excluded (CD-0027); pinned continuity is re-derived per call"
	nativeRuns, nativeErr := readWorkflowNativeRunsTx(ctx, tx, work)
	if nativeErr != nil {
		return nativeErr
	}
	out.NativeRuns = nativeRuns
	return nil
}

func readContinuityInstanceTx(ctx context.Context, tx *sql.Tx, work string, out *ContinuitySnapshot) error {
	var state string
	err := tx.QueryRowContext(ctx, `SELECT instance_state FROM workflow_instances WHERE work_id=?`, work).Scan(&state)
	if err == sql.ErrNoRows {
		exists, existsErr := workExistsCore(ctx, tx, work)
		if existsErr != nil {
			return wrapFailure(KindUnavailable, "C19.Continuity", "cannot read work item", true, "retry once the database is readable", existsErr)
		}
		if !exists {
			return newFailure(KindProjectionNotFound, "C19.Continuity", "work item is not recorded", false, "reread_entities")
		}
		out.WorkflowInstance = WorkflowInstanceAbsent
		return nil
	}
	if err != nil {
		return wrapFailure(KindUnavailable, "C19.Continuity", "cannot read workflow step", true, "retry once the database is readable", err)
	}
	out.WorkflowInstance = WorkflowInstancePresent
	pin, err := ReadWorkPinTx(ctx, tx, work)
	if err != nil {
		return err
	}
	out.WorkPin = &pin
	out.WorkflowStep = pin.Step
	plan, err := readWorkflowPlanTx(ctx, tx, work)
	if err != nil {
		return err
	}
	out.ChangesProductTruth = workflowCurrentProductTruth(nil, plan)
	for _, intent := range pin.NextValidIntents {
		out.StepActions = append(out.StepActions, intent.ActionID)
	}
	return nil
}

func continuityOffset(cursor, work string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, newFailure(KindInvalidCursor, "C19.Continuity", "continuity cursor is malformed", false, "use the cursor returned for this continuity query")
	}
	var value struct {
		V      int    `json:"v"`
		Work   string `json:"work"`
		Offset int    `json:"offset"`
	}
	if json.Unmarshal(b, &value) != nil || value.V != 1 || value.Work != work || value.Offset < 0 {
		return 0, newFailure(KindInvalidCursor, "C19.Continuity", "continuity cursor does not match the work", false, "use the cursor returned for this continuity query")
	}
	if value.Offset > continuityMaxOffset {
		return 0, newFailure(KindLimitExceeded, "C19.Continuity", "continuity cursor offset exceeds the bounded history", false, "reduce_limit")
	}
	return value.Offset, nil
}
