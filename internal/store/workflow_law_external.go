package store

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
)

// workflowLawExternalGit reads the reviewed candidate's history. Tests replace
// it only to prove the refusal when Git cannot answer.
var workflowLawExternalGit GitRunner = ExecGitRunner{}

// A law or registry delta belongs to the completing item only when its own
// delivery made it. The accepted Review's output candidate is that delivery,
// and the inventory is projected from one scanned main commit. A delta is
// proved external when every file that can carry it is byte-identical, or
// absent, in the candidate and at its merge base with that main commit, and no
// commit between the two changed the file on either side of a merge. A merge
// that keeps one parent's file is not a change. The delta then reached main
// through another change, whether or not the candidate later merged or rebased
// onto it. A candidate that main already contains has no fork point that
// separates the two, so it proves nothing.
//
// A law's text lives in its document; its presence, tier, and Domains live in
// its knowledge record shard. A text-only delta needs its document untouched;
// any other delta needs its record shard, and every document a record names,
// untouched. The Domain registry is one shard. Anything unproved stays
// charged, including an edit the item made and then reverted, and a delta in
// another Project's source. History cannot see an item edit whose commit a
// rebase dropped because main already held the same change.

type workflowLawCandidateProof struct {
	candidate WorkerReviewCandidate
	source    workflowLawBaselineSource
	scanned   string
	laws      map[string]bool
	registry  bool
}

type workflowLawCandidateProofKey struct{}

func workflowLawCandidateProofFrom(ctx context.Context) *workflowLawCandidateProof {
	proof, _ := ctx.Value(workflowLawCandidateProofKey{}).(*workflowLawCandidateProof)
	return proof
}

// EstablishLawCandidateProof asks Git, before the completion transaction
// opens (CD-0195 D2), which law and registry deltas the reviewed candidate's
// history never made. It never refuses: an unanswered question leaves the
// delta charged, and the completion gate owns the refusal.
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
	ids := workflowLawCandidateDeltas(before, after, nil, candidate.ProjectID)
	registryMoved := !reflect.DeepEqual(before.Registry, after.Registry)
	if len(ids) == 0 && !registryMoved {
		return nil
	}
	source, scanned, ok := workflowLawCandidateSource(ctx, s.db, before, candidate.ProjectID)
	if !ok {
		return nil
	}
	gitCtx, cancel := context.WithTimeout(ctx, gitCommandTimeout)
	defer cancel()
	repo := s.lawCandidateRepo(gitCtx, workID, *candidate)
	if repo == "" {
		return nil
	}
	fork := workflowLawGitLine(gitCtx, repo, "merge-base", "--end-of-options", candidate.Commit, scanned)
	if fork == "" || fork == candidate.Commit {
		return nil
	}
	untouched := func(paths []string) bool {
		if len(paths) == 0 {
			return false
		}
		for _, path := range paths {
			own, ok := workflowLawGitBlob(gitCtx, repo, candidate.Commit, path)
			start, known := workflowLawGitBlob(gitCtx, repo, fork, path)
			if !ok || !known || own != start {
				return false
			}
		}
		args := append([]string{"rev-list", "-1", "--full-history", "--simplify-merges", "--end-of-options", fork + ".." + candidate.Commit, "--"}, paths...)
		touched, err := workflowLawExternalGit.Run(gitCtx, repo, args...)
		return err == nil && strings.TrimSpace(string(touched)) == ""
	}
	var shards []*workflowLawShardIndex
	shardsRead, shardsFailed := false, false
	readShards := func() bool {
		if !shardsRead {
			shardsRead = true
			for _, commit := range []string{fork, candidate.Commit, scanned} {
				index, err := readWorkflowLawShardIndex(gitCtx, repo, commit)
				if err != nil {
					shardsFailed = true
					break
				}
				shards = append(shards, index)
			}
		}
		return !shardsFailed
	}
	proof := &workflowLawCandidateProof{candidate: *candidate, source: source, scanned: scanned, laws: map[string]bool{}}
	old, now := workflowLawInventoryMap(before), workflowLawInventoryMap(after)
	for _, id := range ids {
		lawID := strings.TrimPrefix(id, candidate.ProjectID+"/")
		start, existed := old[id]
		current, exists := now[id]
		var paths []string
		if exists {
			var path string
			err := s.db.QueryRowContext(ctx, `SELECT path FROM law_subjects WHERE home_project_id=? AND home_locator_id=? AND law_id=?`, source.ProjectID, source.LocatorID, lawID).Scan(&path)
			if err != nil || path == "" {
				continue
			}
			paths = append(paths, path)
		}
		if !existed || !exists || start.Tier != current.Tier || !slices.Equal(start.DomainIDs, current.DomainIDs) {
			if !readShards() {
				continue
			}
			var records []string
			for _, index := range shards {
				records = append(records, index.records[lawID]...)
				paths = append(paths, index.documents[lawID]...)
			}
			if len(records) == 0 && !exists && shards[1].recordTree != "" {
				// No read commit holds the removed law's record, so the
				// whole record tree must be untouched.
				records = append(records, shards[1].recordTree)
			}
			if len(records) == 0 {
				continue
			}
			paths = append(paths, records...)
		}
		if untouched(uniqueSorted(paths)) {
			proof.laws[id] = true
		}
	}
	if registryMoved && workflowLawRegistryHomedAt(ctx, s.db, source, scanned) && readShards() {
		if registry := shards[len(shards)-1].registry; registry != "" {
			proof.registry = untouched([]string{registry})
		}
	}
	if len(proof.laws) == 0 && !proof.registry {
		return nil
	}
	return proof
}

// workflowLawShardIndex locates, at one commit, each law's record shard and
// the documents it names, the record tree, and the registry shard.
type workflowLawShardIndex struct {
	registry   string
	recordTree string
	records    map[string][]string
	documents  map[string][]string
}

func readWorkflowLawShardIndex(ctx context.Context, repo, commit string) (*workflowLawShardIndex, error) {
	index := &workflowLawShardIndex{records: map[string][]string{}, documents: map[string][]string{}}
	for _, layout := range knowledgeShardLayouts {
		shards, sharded, err := readKnowledgeShardsAtCommitLayout(ctx, workflowLawExternalGit, repo, commit, layout)
		if err != nil {
			return nil, err
		}
		if !sharded {
			continue
		}
		index.registry, index.recordTree = layout.registryPath, layout.recordTree
		for name, raw := range shards.records {
			var record struct {
				ID   string `json:"id"`
				Path string `json:"path"`
			}
			if err := json.Unmarshal(raw, &record); err != nil || record.ID == "" {
				return nil, newFailure(KindInvalidNoteProof, "workflow_law_change", "a knowledge record shard has no identity: "+name, false, "repair the record shard")
			}
			index.records[record.ID] = append(index.records[record.ID], layout.recordTreeDir+name)
			if record.Path != "" {
				index.documents[record.ID] = append(index.documents[record.ID], record.Path)
			}
		}
		return index, nil
	}
	return index, nil
}

// workflowLawGitBlob returns the file's blob at commit, or "" when the commit
// has no such file. The second result is false when Git cannot answer.
func workflowLawGitBlob(ctx context.Context, repo, commit, path string) (string, bool) {
	out, err := workflowLawExternalGit.Run(ctx, repo, "ls-tree", "-z", "--end-of-options", commit, "--", path)
	if err != nil {
		return "", false
	}
	entries, err := parseTreeEntries(out)
	if err != nil || len(entries) > 1 {
		return "", false
	}
	if len(entries) == 0 {
		return "", true
	}
	return entries[0].mode + " " + entries[0].oid, true
}

// The candidate must resolve to the exact reviewed commit and tree. The
// item's claimed worktree holds it first; the Project's canonical checkout
// shares its objects after the worktree is gone.
func (s *Store) lawCandidateRepo(gitCtx context.Context, workID string, candidate WorkerReviewCandidate) string {
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

func workflowLawAcceptedCandidate(ctx context.Context, q queryer, workID string) (*WorkerReviewCandidate, error) {
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

// workflowLawCandidateDeltas lists the undeclared laws in project that moved
// since the item's start cursor.
func workflowLawCandidateDeltas(before, after *workflowLawBaseline, declared map[string]bool, project string) []string {
	var ids []string
	for _, id := range workflowLawInventoryDifferences(before, after) {
		if !declared[id] && strings.HasPrefix(id, project+"/") {
			ids = append(ids, id)
		}
	}
	return ids
}

// workflowLawCandidateSource finds the baseline's one locator for the
// candidate's Project and the single main commit its projection was scanned
// from.
func workflowLawCandidateSource(ctx context.Context, q queryer, before *workflowLawBaseline, project string) (workflowLawBaselineSource, string, bool) {
	var found []workflowLawBaselineSource
	for _, source := range before.Sources {
		if source.ProjectID == project {
			found = append(found, source)
		}
	}
	if len(found) != 1 {
		return workflowLawBaselineSource{}, "", false
	}
	source := found[0]
	rows, err := q.QueryContext(ctx, `SELECT scanned_commit_oid FROM law_subjects WHERE home_project_id=? AND home_locator_id=?
UNION SELECT scanned_commit_oid FROM domain_registries WHERE home_project_id=? AND home_locator_id=?`, source.ProjectID, source.LocatorID, source.ProjectID, source.LocatorID)
	if err != nil {
		return workflowLawBaselineSource{}, "", false
	}
	var commits []string
	for rows.Next() {
		var commit string
		if rows.Scan(&commit) != nil {
			_ = rows.Close()
			return workflowLawBaselineSource{}, "", false
		}
		commits = append(commits, commit)
	}
	failed := rows.Err() != nil
	_ = rows.Close()
	if failed || len(commits) != 1 || commits[0] == "" {
		return workflowLawBaselineSource{}, "", false
	}
	return source, commits[0], true
}

func workflowLawRegistryHomedAt(ctx context.Context, q queryer, source workflowLawBaselineSource, scanned string) bool {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM domain_registries WHERE home_project_id=? AND home_locator_id=? AND scanned_commit_oid=?`, source.ProjectID, source.LocatorID, scanned).Scan(&one)
	return err == nil
}

// readWorkflowLawAttributedBaselineTx reads the owning-start cursor and the
// current inventory with every delta the pre-transaction proof shows the
// item never made set back to its cursor state, so completion checks and
// records only the item's own law. The proof binds only while the accepted
// candidate and the source's scanned commit are unchanged.
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
	if proof == nil {
		return before, after, declared, nil
	}
	candidate, err := workflowLawAcceptedCandidate(ctx, q, workID)
	if err != nil || candidate == nil || *candidate != proof.candidate {
		return before, after, declared, err
	}
	source, scanned, ok := workflowLawCandidateSource(ctx, q, before, candidate.ProjectID)
	if !ok || source != proof.source || scanned != proof.scanned {
		return before, after, declared, nil
	}
	old := workflowLawInventoryMap(before)
	external := map[string]bool{}
	for _, id := range workflowLawCandidateDeltas(before, after, declared, candidate.ProjectID) {
		if proof.laws[id] {
			external[id] = true
		}
	}
	laws := make([]workflowLawState, 0, len(after.Laws))
	for _, state := range after.Laws {
		if !external[state.LawID] {
			laws = append(laws, state)
		}
	}
	for id := range external {
		if state, existed := old[id]; existed {
			laws = append(laws, state)
		}
	}
	slices.SortFunc(laws, func(a, b workflowLawState) int { return strings.Compare(a.LawID, b.LawID) })
	after.Laws = laws
	if proof.registry {
		after.Registry = before.Registry
	}
	return before, after, declared, nil
}
