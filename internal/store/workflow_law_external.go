package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
)

// workflowLawExternalGit reads the reviewed candidate's committed law text.
// Tests replace it only to prove the refusal when Git cannot answer.
var workflowLawExternalGit GitRunner = ExecGitRunner{}

// A law delta belongs to the completing item only when its own delivery could
// have made it. The accepted Review's output candidate is that delivery. When
// the candidate still carries a law's owning-start text and the law's tier
// and Domains are unchanged, the item's commits did not edit it: the edit
// reached the projection through another change. Such a delta is neither
// charged to the item nor recorded as its enactment. Anything Git cannot
// prove stays charged, as before.
func workflowExternalLawChangesTx(ctx context.Context, q queryer, workID string, before, after *workflowLawBaseline, declared map[string]bool) (map[string]bool, error) {
	old, now := workflowLawInventoryMap(before), workflowLawInventoryMap(after)
	var textOnly []string
	for _, id := range workflowLawInventoryDifferences(before, after) {
		start, existed := old[id]
		current, exists := now[id]
		if declared[id] || !existed || !exists || start.Tier != current.Tier || !slices.Equal(start.DomainIDs, current.DomainIDs) {
			continue
		}
		textOnly = append(textOnly, id)
	}
	if len(textOnly) == 0 {
		return nil, nil
	}
	gitCtx, cancel := context.WithTimeout(ctx, gitCommandTimeout)
	defer cancel()
	facts, err := readWorkflowReviewFacts(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	if facts.Accepted == nil {
		return nil, nil
	}
	candidate := facts.Accepted.Report.OutputCandidate
	locator := ""
	for _, source := range before.Sources {
		if source.ProjectID == candidate.ProjectID {
			locator = source.LocatorID
		}
	}
	if locator == "" {
		return nil, nil
	}
	repo, err := workflowLawCandidateRepoTx(ctx, gitCtx, q, workID, candidate)
	if err != nil || repo == "" {
		return nil, err
	}
	external := map[string]bool{}
	for _, id := range textOnly {
		project, lawID, found := strings.Cut(id, "/")
		if !found || project != candidate.ProjectID {
			continue
		}
		var path string
		if err := q.QueryRowContext(ctx, `SELECT path FROM law_subjects WHERE home_project_id=? AND home_locator_id=? AND law_id=?`, project, locator, lawID).Scan(&path); err != nil {
			return nil, workflowProjectionError(err, "cannot read the changed law's path")
		}
		content, err := workflowLawExternalGit.Run(gitCtx, repo, "cat-file", "blob", candidate.Commit+":"+path)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(content)
		if "sha256:"+hex.EncodeToString(sum[:]) == old[id].ContentHash {
			external[id] = true
		}
	}
	return external, nil
}

// The candidate must be the exact reviewed commit and tree. The item's claimed
// worktree holds it first; the Project's canonical checkout shares its objects
// after the worktree is gone. A repository that cannot prove the pair is
// skipped, so attribution fails closed.
func workflowLawCandidateRepoTx(ctx, gitCtx context.Context, q queryer, workID string, candidate WorkerReviewCandidate) (string, error) {
	if candidate.ProjectID == "" || candidate.Commit == "" || candidate.Tree == "" {
		return "", nil
	}
	rows, err := q.QueryContext(ctx, `SELECT e.path FROM worktree_entries e JOIN worktree_claims c ON c.op_id=e.claim_op_id WHERE c.work_id=? AND e.project_id=? AND e.state='active'
UNION ALL SELECT normalized_value FROM project_locators WHERE project_id=? AND kind='canonical_path'`, workID, candidate.ProjectID, candidate.ProjectID)
	if err != nil {
		return "", workflowProjectionError(err, "cannot read the reviewed candidate's repositories")
	}
	var repos []string
	for rows.Next() {
		var repo string
		if err := rows.Scan(&repo); err != nil {
			_ = rows.Close()
			return "", workflowProjectionError(err, "cannot read a reviewed candidate repository")
		}
		repos = append(repos, repo)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return "", workflowProjectionError(err, "cannot finish reading the reviewed candidate's repositories")
	}
	for _, repo := range repos {
		if repo == "" {
			continue
		}
		tree, err := workflowLawExternalGit.Run(gitCtx, repo, "rev-parse", "--verify", "--end-of-options", candidate.Commit+"^{tree}")
		if err == nil && strings.TrimSpace(string(tree)) == candidate.Tree {
			return repo, nil
		}
	}
	return "", nil
}

// readWorkflowLawAttributedBaselineTx reads the owning-start cursor and the
// current inventory with every externally made law delta set back to its
// cursor state, so completion checks and records only the item's own law.
func readWorkflowLawAttributedBaselineTx(ctx context.Context, q queryer, workID string, change *WorkflowLawChange) (*workflowLawBaseline, *workflowLawBaseline, error) {
	before, after, err := readWorkflowLawBaselineTx(ctx, q, workID)
	if err != nil {
		return nil, nil, err
	}
	declared := map[string]bool{}
	if change != nil {
		for _, reference := range change.LawIDs {
			canonical, err := workflowLawInventoryReference(reference, before, after)
			if err != nil {
				return nil, nil, err
			}
			declared[canonical] = true
		}
	}
	external, err := workflowExternalLawChangesTx(ctx, q, workID, before, after, declared)
	if err != nil || len(external) == 0 {
		return before, after, err
	}
	old := workflowLawInventoryMap(before)
	for i, state := range after.Laws {
		if external[state.LawID] {
			after.Laws[i] = old[state.LawID]
		}
	}
	return before, after, nil
}
