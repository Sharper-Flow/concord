package store

import (
	"context"
	"database/sql"
	"slices"
	"strings"
)

// workflowLawExternalGit reads the reviewed candidate's history. Tests replace
// it only to prove the refusal when Git cannot answer.
var workflowLawExternalGit GitRunner = ExecGitRunner{}

// A law delta belongs to the completing item only when its own delivery made
// it. The accepted Review's output candidate is that delivery, and the law's
// projection is scanned from a main commit. The delta is proved external when
// the candidate's law file is byte-identical to the file at its merge base
// with that main commit, and no commit between the two changed the file on
// either side of a merge. A merge that keeps one parent's file is not a
// change. The edit then reached main through another change, whether or not
// the candidate later merged or rebased onto it. A candidate that main already contains has
// no fork point that separates the two, so it proves nothing. Only a text
// change with the law's tier and Domains unchanged can be proved external.
// Anything unproved stays charged, including an edit the item made and then
// reverted. History cannot see an item edit whose commit a rebase dropped
// because main already held the same change.

type workflowLawCandidateProof struct {
	candidate ReviewCandidate
	untouched map[string]workflowLawLocation
}

type workflowLawLocation struct {
	path    string
	scanned string
}

type workflowLawCandidateProofKey struct{}

func workflowLawCandidateProofFrom(ctx context.Context) *workflowLawCandidateProof {
	proof, _ := ctx.Value(workflowLawCandidateProofKey{}).(*workflowLawCandidateProof)
	return proof
}

// EstablishLawCandidateProof asks Git, before the completion transaction
// opens (CD-0195 D2), which changed laws the reviewed candidate's history
// left untouched. It never refuses: an unanswered question leaves the delta
// charged, and the completion gate owns the refusal.
func (s *Store) EstablishLawCandidateProof(ctx context.Context, workID string) context.Context {
	if s == nil || s.db == nil {
		return ctx
	}
	proof := s.lawCandidateProof(ctx, workID)
	if proof == nil {
		return ctx
	}
	return context.WithValue(ctx, workflowLawCandidateProofKey{}, proof)
}

func (s *Store) lawCandidateProof(ctx context.Context, workID string) *workflowLawCandidateProof {
	before, after, err := readWorkflowLawBaselineTx(ctx, s.db, workID)
	if err != nil {
		return nil
	}
	candidate, err := workflowLawAcceptedCandidate(ctx, s.db, workID)
	if err != nil || candidate == nil {
		return nil
	}
	ids := workflowLawTextOnlyChanges(before, after, nil, candidate.ProjectID)
	if len(ids) == 0 {
		return nil
	}
	gitCtx, cancel := context.WithTimeout(ctx, gitCommandTimeout)
	defer cancel()
	repo := s.lawCandidateRepo(gitCtx, workID, *candidate)
	if repo == "" {
		return nil
	}
	proof := &workflowLawCandidateProof{candidate: *candidate, untouched: map[string]workflowLawLocation{}}
	forks := map[string]string{}
	for _, id := range ids {
		location, err := workflowLawSubjectLocation(ctx, s.db, before, id)
		if err != nil || location.path == "" || location.scanned == "" {
			continue
		}
		fork, known := forks[location.scanned]
		if !known {
			fork = workflowLawGitLine(gitCtx, repo, "merge-base", "--end-of-options", candidate.Commit, location.scanned)
			forks[location.scanned] = fork
		}
		if fork == "" || fork == candidate.Commit {
			continue
		}
		own := workflowLawGitLine(gitCtx, repo, "rev-parse", "--verify", "--end-of-options", candidate.Commit+":"+location.path)
		start := workflowLawGitLine(gitCtx, repo, "rev-parse", "--verify", "--end-of-options", fork+":"+location.path)
		if own == "" || own != start {
			continue
		}
		touched, err := workflowLawExternalGit.Run(gitCtx, repo, "rev-list", "-1", "--full-history", "--simplify-merges", "--end-of-options", fork+".."+candidate.Commit, "--", location.path)
		if err == nil && strings.TrimSpace(string(touched)) == "" {
			proof.untouched[id] = location
		}
	}
	return proof
}

// The candidate must resolve to the exact reviewed commit and tree. The
// item's claimed worktree holds it first; the Project's canonical checkout
// shares its objects after the worktree is gone.
func (s *Store) lawCandidateRepo(gitCtx context.Context, workID string, candidate ReviewCandidate) string {
	rows, err := s.db.QueryContext(gitCtx, `SELECT e.path FROM worktree_entries e JOIN worktree_claims c ON c.op_id=e.claim_op_id WHERE c.work_id=? AND e.project_id=? AND e.state='active'
UNION ALL SELECT normalized_value FROM project_locators WHERE project_id=? AND kind='canonical_path'`, workID, candidate.ProjectID, candidate.ProjectID)
	if err != nil {
		return ""
	}
	var repos []string
	for rows.Next() {
		var repo string
		if rows.Scan(&repo) == nil && repo != "" {
			repos = append(repos, repo)
		}
	}
	failed := rows.Err() != nil
	_ = rows.Close()
	if failed {
		return ""
	}
	for _, repo := range repos {
		if workflowLawGitLine(gitCtx, repo, "rev-parse", "--verify", "--end-of-options", candidate.Commit+"^{tree}") == candidate.Tree {
			return repo
		}
	}
	return ""
}

func workflowLawGitLine(ctx context.Context, repo string, args ...string) string {
	out, err := workflowLawExternalGit.Run(ctx, repo, args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func workflowLawAcceptedCandidate(ctx context.Context, q queryer, workID string) (*ReviewCandidate, error) {
	facts, err := readWorkflowReviewFacts(ctx, q, workID)
	if err != nil || facts.Accepted == nil {
		return nil, err
	}
	candidate := facts.Accepted.Report.OutputCandidate
	if candidate.ProjectID == "" || candidate.Commit == "" || candidate.Tree == "" {
		return nil, nil
	}
	return &candidate, nil
}

// workflowLawTextOnlyChanges lists the undeclared laws in project whose text
// alone moved since the item's start cursor.
func workflowLawTextOnlyChanges(before, after *workflowLawBaseline, declared map[string]bool, project string) []string {
	old, now := workflowLawInventoryMap(before), workflowLawInventoryMap(after)
	var ids []string
	for _, id := range workflowLawInventoryDifferences(before, after) {
		start, existed := old[id]
		current, exists := now[id]
		if declared[id] || !existed || !exists || !strings.HasPrefix(id, project+"/") || start.Tier != current.Tier || !slices.Equal(start.DomainIDs, current.DomainIDs) {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

// workflowLawSubjectLocation finds the law's projected file and the main
// commit it was scanned from, across every baseline locator of its Project.
func workflowLawSubjectLocation(ctx context.Context, q queryer, before *workflowLawBaseline, id string) (workflowLawLocation, error) {
	project, lawID, _ := strings.Cut(id, "/")
	for _, source := range before.Sources {
		if source.ProjectID != project {
			continue
		}
		var location workflowLawLocation
		err := q.QueryRowContext(ctx, `SELECT path,scanned_commit_oid FROM law_subjects WHERE home_project_id=? AND home_locator_id=? AND law_id=?`, project, source.LocatorID, lawID).Scan(&location.path, &location.scanned)
		if err == nil {
			return location, nil
		}
		if err != sql.ErrNoRows {
			return workflowLawLocation{}, workflowProjectionError(err, "cannot read the changed law's location")
		}
	}
	return workflowLawLocation{}, nil
}

// readWorkflowLawAttributedBaselineTx reads the owning-start cursor and the
// current inventory with every delta the pre-transaction proof shows the
// item never made set back to its cursor state, so completion checks and
// records only the item's own law. The proof binds only while the accepted
// candidate and each law's file and scanned commit are unchanged.
func readWorkflowLawAttributedBaselineTx(ctx context.Context, q queryer, workID string, change *WorkflowLawChange) (*workflowLawBaseline, *workflowLawBaseline, map[string]bool, error) {
	before, after, err := readWorkflowLawBaselineTx(ctx, q, workID)
	if err != nil {
		return nil, nil, nil, err
	}
	declared := map[string]bool{}
	if change != nil {
		for _, reference := range change.LawIDs {
			canonical, err := workflowLawInventoryReference(reference, before, after)
			if err != nil {
				return nil, nil, nil, err
			}
			declared[canonical] = true
		}
	}
	proof := workflowLawCandidateProofFrom(ctx)
	if proof == nil || len(proof.untouched) == 0 {
		return before, after, declared, nil
	}
	candidate, err := workflowLawAcceptedCandidate(ctx, q, workID)
	if err != nil || candidate == nil || *candidate != proof.candidate {
		return before, after, declared, err
	}
	old := workflowLawInventoryMap(before)
	external := map[string]bool{}
	for _, id := range workflowLawTextOnlyChanges(before, after, declared, candidate.ProjectID) {
		proved, ok := proof.untouched[id]
		if !ok {
			continue
		}
		location, err := workflowLawSubjectLocation(ctx, q, before, id)
		if err != nil {
			return nil, nil, nil, err
		}
		if location == proved {
			external[id] = true
		}
	}
	for i, state := range after.Laws {
		if external[state.LawID] {
			after.Laws[i] = old[state.LawID]
		}
	}
	return before, after, declared, nil
}
