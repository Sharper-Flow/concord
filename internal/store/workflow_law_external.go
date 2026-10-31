package store

import (
	"context"
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
// A law's presence, tier, Domains, and text all live in its file under the
// committed law folder. Every delta needs each file that holds the law at the
// merge base, the candidate, or the scanned commit untouched; a law absent
// from all three needs its removal history untouched. The Domain registry is
// one file. Anything unproved stays charged, including an
// edit the item made and then reverted, and a delta in another Project's
// source. History cannot see an item edit whose commit a rebase dropped
// because main already held the same change.

type workflowLawCandidateProof struct {
	candidate ReviewCandidate
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
	repo := s.lawCandidateRepo(gitCtx, *candidate)
	if repo == "" {
		return nil
	}
	fork := workflowLawGitLine(gitCtx, repo, "merge-base", "--end-of-options", candidate.Commit, scanned)
	if fork == "" || fork == candidate.Commit {
		return nil
	}
	history := workflowLawHistory{repo: repo, fork: fork, candidate: candidate.Commit}
	var indexes workflowLawFolderIndexes
	for _, commit := range []string{fork, candidate.Commit, scanned} {
		index, err := readWorkflowLawFolderIndex(gitCtx, repo, commit)
		if err != nil {
			return nil
		}
		indexes = append(indexes, index)
	}
	proof := &workflowLawCandidateProof{candidate: *candidate, source: source, scanned: scanned, laws: map[string]bool{}}
	now := workflowLawInventoryMap(after)
	for _, id := range ids {
		_, exists := now[id]
		lawID := strings.TrimPrefix(id, candidate.ProjectID+"/")
		files := indexes.lawFiles(lawID)
		if history.untouched(gitCtx, files) || !exists && len(files) == 0 && history.removedLawUntouched(gitCtx, lawID) {
			proof.laws[id] = true
		}
	}
	if registryMoved && workflowLawRegistryHomedAt(ctx, source, scanned) {
		proof.registry = history.untouched(gitCtx, []string{knowledgeRegistryPath})
	}
	if len(proof.laws) == 0 && !proof.registry {
		return nil
	}
	return proof
}

// workflowLawHistory is the candidate's own history: the commits after its
// merge base with the scanned main commit.
type workflowLawHistory struct {
	repo, fork, candidate string
}

// untouched reports that every path is the same, or equally absent, in the
// candidate and at the fork, and that no commit between them changed it.
func (h workflowLawHistory) untouched(ctx context.Context, paths []string) bool {
	if len(paths) == 0 {
		return false
	}
	for _, path := range paths {
		own, ok := workflowLawGitBlob(ctx, h.repo, h.candidate, path)
		start, known := workflowLawGitBlob(ctx, h.repo, h.fork, path)
		if !ok || !known || own != start {
			return false
		}
	}
	args := append([]string{"rev-list", "-1", "--full-history", "--simplify-merges", "--end-of-options", h.fork + ".." + h.candidate, "--"}, paths...)
	touched, err := workflowLawExternalGit.Run(ctx, h.repo, args...)
	return err == nil && strings.TrimSpace(string(touched)) == ""
}

// When main removed a law before the fork, its file is absent from all three
// snapshots. Inspect the law-folder commits and their parents to recover only
// that law's paths, rather than charging unrelated law edits.
func (h workflowLawHistory) removedLawUntouched(ctx context.Context, lawID string) bool {
	args := []string{"rev-list", "--parents", "--full-history", "--simplify-merges", "--end-of-options", h.fork + ".." + h.candidate, "--", strings.TrimSuffix(lawFolderRoot, "/")}
	out, err := workflowLawExternalGit.Run(ctx, h.repo, args...)
	if err != nil {
		return false
	}
	var files []string
	for _, commit := range uniqueSorted(strings.Fields(string(out))) {
		index, err := readWorkflowLawFolderIndex(ctx, h.repo, commit)
		if err != nil {
			return false
		}
		files = append(files, (workflowLawFolderIndexes{index}).lawFiles(lawID)...)
	}
	return len(files) == 0 || h.untouched(ctx, uniqueSorted(files))
}

// workflowLawFolderIndexes locates law files across immutable Git snapshots.
type workflowLawFolderIndexes []workflowLawFolderIndex

// lawFiles lists every committed law-folder file carrying the law's stable
// ID. An empty result needs a separate removed-law history proof.
func (indexes workflowLawFolderIndexes) lawFiles(lawID string) []string {
	var paths []string
	for _, index := range indexes {
		if path, ok := index[lawID]; ok {
			paths = append(paths, path)
		}
	}
	return uniqueSorted(paths)
}

// workflowLawFolderIndex maps each law's stable ID to its file in the
// committed law folder at one commit.
type workflowLawFolderIndex map[string]string

func readWorkflowLawFolderIndex(ctx context.Context, repo, commit string) (workflowLawFolderIndex, error) {
	snapshot, err := loadLawFolderSnapshotWith(ctx, workflowLawExternalGit, repo, commit)
	if err != nil {
		return nil, err
	}
	index := workflowLawFolderIndex{}
	for _, id := range snapshot.LawIDs() {
		record, _ := snapshot.Law(id)
		index[id] = record.Path
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
// Project's canonical checkouts supply the Git objects.
func (s *Store) lawCandidateRepo(gitCtx context.Context, candidate ReviewCandidate) string {
	rows, err := s.db.QueryContext(gitCtx, `SELECT normalized_value FROM project_locators WHERE project_id=? AND kind='canonical_path'`, candidate.ProjectID)
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
// candidate's Project and the verified snapshot commit that locator's law
// folder was read from.
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
	snapshot, err := lawSnapshotForSource(ctx, KnowledgeHome{HomeProjectID: source.ProjectID, HomeLocatorID: source.LocatorID}, "workflow_law_change")
	if err != nil || snapshot.CommitOID() == "" {
		return workflowLawBaselineSource{}, "", false
	}
	return source, snapshot.CommitOID(), true
}

// workflowLawRegistryHomedAt reports whether the verified snapshot of the
// source was read at the scanned commit. The snapshot carries the registry
// and the law folder of exactly one commit, so the registry is homed at the
// commit whenever the snapshot is the one being asked about.
func workflowLawRegistryHomedAt(ctx context.Context, source workflowLawBaselineSource, scanned string) bool {
	snapshot, err := lawSnapshotForSource(ctx, KnowledgeHome{HomeProjectID: source.ProjectID, HomeLocatorID: source.LocatorID}, "workflow_law_change")
	return err == nil && snapshot.CommitOID() == scanned
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
