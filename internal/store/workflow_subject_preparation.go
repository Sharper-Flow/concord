package store

import (
	"context"
	"database/sql"
	"time"
)

type PrepareWorkflowSubjectRequest struct {
	WorkID string
	Runner GitRunner
}

// WorkflowSubjectPreparation seals a native Git observation captured after its
// read snapshot closes. Live admission rechecks its SQL identities before effects.
type WorkflowSubjectPreparation struct {
	workID     string
	version    int64
	step       string
	contract   int64
	viewDigest string
	entry      *WorktreeEntry
	receipt    *WorktreeVerifyResult
	candidate  *ReviewCandidate
}

type workflowSubjectPreparationKey struct{}

func WithWorkflowSubjectPreparation(ctx context.Context, prepared *WorkflowSubjectPreparation) context.Context {
	return context.WithValue(ctx, workflowSubjectPreparationKey{}, prepared)
}

func workflowSubjectPreparationFrom(ctx context.Context) *WorkflowSubjectPreparation {
	prepared, _ := ctx.Value(workflowSubjectPreparationKey{}).(*WorkflowSubjectPreparation)
	return prepared
}

func PrepareWorkflowAcceptanceFreshness(ctx context.Context, s *Store, request PrepareWorkflowSubjectRequest) (*WorkflowSubjectPreparation, error) {
	if s == nil || s.db == nil {
		return nil, newFailure(KindUnavailable, "workflow_subject", "store is not open", false, "open the authority database")
	}
	p := &WorkflowSubjectPreparation{workID: request.WorkID}
	if err := s.readSnapshot(ctx, func(q queryer) error {
		return p.sample(ctx, q)
	}); err != nil {
		return nil, err
	}
	if p.receipt == nil {
		return p, nil
	}
	path, err := canonicalNativeWorktreePath(p.entry.Path)
	if err != nil {
		return nil, wrapFailure(KindGitUnreachable, "workflow_subject", "cannot resolve the qualified worktree", true, "restore repository access and prepare the action again", err)
	}
	runner := request.Runner
	if runner == nil {
		runner = ExecGitRunner{}
	}
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	before, err := snapshotOracleGitSubject(probe, runner, path)
	if err != nil {
		return nil, err
	}
	if !before.clean || !worktreeSHAPattern.MatchString(before.head) || p.receipt.SubjectRef != "commit:"+before.head {
		return nil, newFailure(KindStaleRequiresReview, "workflow_subject", "the live clean HEAD does not match the qualified terminal subject", false, "verify the clean worktree and prepare acceptance again")
	}
	tree, err := readReviewCandidateTree(probe, runner, path, before.head)
	if err != nil {
		return nil, wrapFailure(KindGitUnreachable, "workflow_subject", "cannot observe the candidate tree", true, "restore repository access and prepare acceptance again", err)
	}
	p.candidate = &ReviewCandidate{ProjectID: p.receipt.ProjectID, Commit: before.head, Tree: tree}
	return p, nil
}

func (p *WorkflowSubjectPreparation) sample(ctx context.Context, q queryer) error {
	var err error
	if err = q.QueryRowContext(ctx, `SELECT w.version,COALESCE(i.current_step,'') FROM work_items w LEFT JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, p.workID).Scan(&p.version, &p.step); err != nil {
		return workflowProjectionError(err, "cannot sample workflow subject version and step")
	}
	p.contract, err = activeWorkflowContractVersion(ctx, q, p.workID, "workflow_subject")
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	view, err := readWorkContextView(ctx, q, p.workID)
	if err != nil {
		return err
	}
	p.viewDigest = reviewDigest(view)
	p.receipt, err = readCurrentOracleSubjectReceipt(ctx, q, p.workID)
	if err != nil || p.receipt == nil {
		return err
	}
	entries, err := worktreeEntriesCore(ctx, q, p.workID)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.State == worktreeEntryActive && entry.ProjectID == p.receipt.ProjectID && entry.Path == p.receipt.Path && entry.Branch == p.receipt.Branch {
			copy := entry
			p.entry = &copy
			return nil
		}
	}
	return newFailure(KindStaleRequiresReview, "workflow_subject", "terminal acceptance cannot establish the current subject's claimed worktree", false, "claim and verify the subject's worktree")
}

func (p *WorkflowSubjectPreparation) revalidateTx(ctx context.Context, tx *sql.Tx, workID string) error {
	stale := func(detail string) error {
		return newFailure(KindProjectionConflict, "workflow_subject", detail, true, "prepare the action again against current state")
	}
	if p.workID != workID {
		return stale("the subject preparation belongs to another work item")
	}
	var version int64
	var step string
	if err := tx.QueryRowContext(ctx, `SELECT w.version,COALESCE(i.current_step,'') FROM work_items w LEFT JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, workID).Scan(&version, &step); err != nil {
		return workflowProjectionError(err, "cannot revalidate workflow subject state")
	}
	if version != p.version || step != p.step {
		return stale("the work version or workflow step changed under the subject observation")
	}
	contract, err := activeWorkflowContractVersion(ctx, tx, workID, "workflow_subject")
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if contract != p.contract {
		return stale("the active contract changed under the subject observation")
	}
	view, err := readWorkContextView(ctx, tx, workID)
	if err != nil {
		return err
	}
	if reviewDigest(view) != p.viewDigest {
		return stale("the work context changed under the subject observation")
	}
	current, err := readCurrentOracleSubjectReceipt(ctx, tx, workID)
	if err != nil {
		return err
	}
	if reviewDigest(current) != reviewDigest(p.receipt) {
		return stale("the qualified subject changed under its observation")
	}
	if p.entry == nil {
		return nil
	}
	entries, err := worktreeEntriesCore(ctx, tx, workID)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.ProjectID == p.entry.ProjectID && entry.ClaimOpID == p.entry.ClaimOpID && entry.Path == p.entry.Path && entry.Branch == p.entry.Branch && entry.State == p.entry.State {
			return nil
		}
	}
	return stale("the claimed worktree changed under the subject observation")
}

func workflowDefinitionProducesDelivery(definition WorkflowDefinition) bool {
	for _, step := range definition.StepGraph.Steps {
		for _, action := range step.Actions {
			if action == "record_delivery" {
				return true
			}
		}
	}
	return false
}
