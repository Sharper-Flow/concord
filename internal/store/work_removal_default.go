package store

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// DefaultCheckoutRemovalProof binds core-owned native observations to the exact
// retired claims and cleanup state. Its contents cannot be supplied by a caller.
// It proves absence at observation time, not exclusion of later external Git or
// filesystem writes. Transactions recheck the recorded identities with SQL only.
type DefaultCheckoutRemovalProof struct {
	request WorkRemovalRequest
	project string
	state   defaultRemovalState
}

type defaultRemovalClaim struct {
	op, project, set, branch, base, path, repository, state string
	incarnation                                             int
}

type defaultRemovalState struct {
	root                      string
	claims                    []defaultRemovalClaim
	entries, outcomes, fences []string
}

func defaultRemovalRefusal(detail string) error {
	return newFailure(KindUnauthorized, "work_removal", detail, false, "cancel the item and verify exact-item worktree retirement before shelving")
}

// defaultCheckoutRemovalEligibilityQ is the single eligibility predicate used
// before a challenge and inside its transaction. Native reads never run here.
func defaultCheckoutRemovalEligibilityQ(ctx context.Context, q queryer, req WorkRemovalRequest, project string) (defaultRemovalState, error) {
	var out defaultRemovalState
	if err := defaultRemovalItemEligibleQ(ctx, q, req, project); err != nil {
		return out, err
	}
	root, err := worktreeRepoRootTx(ctx, q, WorktreeClaimRequest{ProjectID: project})
	if err != nil {
		return out, err
	}
	out.root = root
	if out.claims, err = defaultRemovalRetiredClaimsQ(ctx, q, req.WorkID, project, root); err != nil {
		return out, err
	}
	if err := defaultRemovalCleanupSettledQ(ctx, q, req.WorkID); err != nil {
		return out, err
	}
	set := WorktreeSetID(req.WorkID)
	if out.entries, err = removalStateRows(ctx, q, `SELECT json_array(project_id,claim_op_id,member_id,branch,base_sha,path,repository_id,state,verified_at,reclaimed_at,git_facts) FROM worktree_entries WHERE set_id=? ORDER BY project_id,member_id`, set); err != nil {
		return out, err
	}
	if out.outcomes, err = removalStateRows(ctx, q, `SELECT json_array(project_id,claim_op_id,claim_incarnation,branch,phase,tip,reason,path,recorded_at,settlement_count) FROM worktree_ref_outcomes WHERE set_id=? ORDER BY project_id,claim_op_id,claim_incarnation,branch`, set); err != nil {
		return out, err
	}
	out.fences, err = removalStateRows(ctx, q, `SELECT json_array(resource_kind,repository_id,resource_ref,generation) FROM worktree_claim_native_fences WHERE set_id=? ORDER BY resource_kind,repository_id,resource_ref`, set)
	return out, err
}

// defaultRemovalItemEligibleQ checks the item's scope, cancellation and the
// existing removal destination and safety gates.
func defaultRemovalItemEligibleQ(ctx context.Context, q queryer, req WorkRemovalRequest, project string) error {
	if err := validateRemovalRequest(req); err != nil {
		return err
	}
	if req.Reason != "shelved" || project == "" || req.ProductID == "" {
		return defaultRemovalRefusal("default-checkout removal requires shelving in one selected Product and resolved Project")
	}
	projects, err := removalStateRows(ctx, q, `SELECT project_id FROM work_projects WHERE work_id=? ORDER BY project_id`, req.WorkID)
	if err != nil {
		return err
	}
	products, err := productsForWorkIDs(ctx, q, []string{req.WorkID})
	if err != nil {
		return err
	}
	if !slices.Equal(projects, []string{project}) || !slices.Equal(products[req.WorkID], []string{req.ProductID}) {
		return defaultRemovalRefusal("default-checkout shelving requires the caller's sole Project and selected sole Product")
	}
	var lifecycle string
	if err := q.QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id=?`, req.WorkID).Scan(&lifecycle); err != nil {
		return err
	}
	var uncancelled int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM workflow_instances WHERE work_id=? AND instance_state<>'cancelled'`, req.WorkID).Scan(&uncancelled); err != nil {
		return err
	}
	if lifecycle != "cancelled" || uncancelled != 0 {
		return defaultRemovalRefusal("default-checkout shelving requires cancelled lifecycle and workflow")
	}
	if err := validateRemovalDestinationQ(ctx, q, req); err != nil {
		return err
	}
	if req.Linear != nil && req.Linear.HandoffDigest != removalHandoffDigest(req.Handoff) {
		return defaultRemovalRefusal("Linear confirmation does not match the handoff")
	}
	return validateRemovalGatesQ(ctx, q, req)
}

// defaultRemovalRetiredClaimsQ loads every recorded claim and requires each to
// be reclaimed in the caller's repository with its retirement receipt.
func defaultRemovalRetiredClaimsQ(ctx context.Context, q queryer, workID, project, root string) ([]defaultRemovalClaim, error) {
	rows, err := q.QueryContext(ctx, `SELECT op_id,project_id,set_id,pinned_branch,pinned_base_sha,pinned_path,repository_id,state,incarnation FROM worktree_claims WHERE work_id=? ORDER BY op_id`, workID)
	if err != nil {
		return nil, err
	}
	var claims []defaultRemovalClaim
	for rows.Next() {
		var c defaultRemovalClaim
		if err := rows.Scan(&c.op, &c.project, &c.set, &c.branch, &c.base, &c.path, &c.repository, &c.state, &c.incarnation); err != nil {
			rows.Close()
			return nil, err
		}
		claims = append(claims, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(claims) == 0 {
		return nil, defaultRemovalRefusal("default-checkout shelving requires recorded retired claims")
	}
	for _, c := range claims {
		if c.project != project || c.set != WorktreeSetID(workID) || c.state != worktreeStateReclaimed || c.repository != root || !filepath.IsAbs(c.path) || c.incarnation < 0 {
			return nil, defaultRemovalRefusal("a recorded claim is not retired in the caller's repository")
		}
		// Every reopened incarnation is retired through this same stable
		// receipt identity. Current-row outcomes retain all generations, not
		// only the worktree_entries slot a later claim replaces.
		receipt, found, err := readEffectReceipt(ctx, q, reclaimedEventID(workID, project, c.op, c.incarnation))
		if err != nil {
			return nil, err
		}
		if !found || receipt.Kind != "work.worktree_reclaimed" || receipt.SubjectID != workID {
			return nil, defaultRemovalRefusal("recorded claim lacks its retirement receipt")
		}
	}
	return claims, nil
}

// defaultRemovalCleanupSettledQ refuses owed native cleanup, retirement rows
// that match no recorded claim identity, and live or unknown occupants.
func defaultRemovalCleanupSettledQ(ctx context.Context, q queryer, workID string) error {
	set := WorktreeSetID(workID)
	checks := []struct {
		query  string
		args   []any
		detail string
	}{
		{`SELECT count(*) FROM worktree_ref_outcomes WHERE set_id=? AND phase NOT IN ('settled','retained_unproven')`, []any{set}, "recorded native retirement still owes cleanup"},
		{`SELECT count(*) FROM worktree_ref_outcomes o WHERE o.set_id=? AND NOT EXISTS (SELECT 1 FROM worktree_claims c WHERE c.work_id=? AND c.op_id=o.claim_op_id AND c.project_id=o.project_id AND c.set_id=o.set_id AND c.incarnation>=o.claim_incarnation AND o.claim_incarnation>=0 AND c.pinned_path=o.path)`, []any{set, workID}, "native retirement outcomes do not match recorded claim identities"},
		{`SELECT count(*) FROM worktree_entries e WHERE e.set_id=? AND NOT EXISTS (SELECT 1 FROM worktree_claims c WHERE c.work_id=? AND c.op_id=e.claim_op_id AND c.project_id=e.project_id AND c.set_id=e.set_id AND c.pinned_path=e.path AND c.repository_id=e.repository_id AND e.state='reclaimed')`, []any{set, workID}, "worktree entry does not match its retired claim"},
		{`SELECT count(*) FROM worktree_occupancy WHERE substr(worktree_id,1,length(?)+1)=?||':'`, []any{set, set}, "a live or unknown recorded occupant prevents shelving"},
	}
	for _, check := range checks {
		var count int
		if err := q.QueryRowContext(ctx, check.query, check.args...).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return defaultRemovalRefusal(check.detail)
		}
	}
	return nil
}

func removalStateRows(ctx context.Context, q queryer, query string, args ...any) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// ProveDefaultCheckoutRemoval reads complete native inventory and checks every
// recorded path outside transactions. It grants no deletion or ref authority.
func (s *Store) ProveDefaultCheckoutRemoval(ctx context.Context, req WorkRemovalRequest, project string) (*DefaultCheckoutRemovalProof, error) {
	state, err := defaultCheckoutRemovalEligibilityQ(ctx, s.db, req, project)
	if err != nil {
		return nil, err
	}
	if err := observeDefaultRemovalAbsence(ctx, ExecGitRunner{}, state); err != nil {
		return nil, err
	}
	return &DefaultCheckoutRemovalProof{request: req, project: project, state: state}, nil
}

func observeDefaultRemovalAbsence(ctx context.Context, runner GitRunner, state defaultRemovalState) error {
	rootOut, err := runner.Run(ctx, state.root, "rev-parse", "--show-toplevel")
	if err != nil {
		return wrapFailure(KindGitUnreachable, "work_removal", "cannot observe the retirement repository", true, "retry once the repository is readable", err)
	}
	root, err := normalizePath(strings.TrimSpace(string(rootOut)))
	if err != nil || root != state.root {
		return defaultRemovalRefusal("retirement repository identity changed")
	}
	output, err := runner.Run(ctx, state.root, "worktree", "list", "--porcelain")
	if err != nil {
		return wrapFailure(KindGitUnreachable, "work_removal", "cannot observe the complete worktree inventory", true, "retry once the repository is readable", err)
	}
	inventory, err := parseWorktreeInventory(output)
	if err != nil {
		return defaultRemovalRefusal("native worktree inventory is incomplete")
	}
	for _, entry := range inventory.entries {
		// Quoted or relative paths cannot establish recorded-path absence.
		if !filepath.IsAbs(entry.path) {
			return defaultRemovalRefusal("native inventory checkout paths are unobservable")
		}
	}
	for _, c := range state.claims {
		for _, entry := range inventory.entries {
			if filepath.Clean(entry.path) == filepath.Clean(c.path) {
				return defaultRemovalRefusal("native inventory still names a recorded checkout")
			}
		}
		if _, err := os.Lstat(c.path); !os.IsNotExist(err) {
			return defaultRemovalRefusal("a recorded checkout path survives or its absence is unobservable")
		}
	}
	return nil
}

// ValidateDefaultCheckoutRemovalTx revalidates the exact observed claim set,
// version, scope and native cleanup debt before issuing or consuming authority.
func ValidateDefaultCheckoutRemovalTx(ctx context.Context, transaction *Transaction, proof *DefaultCheckoutRemovalProof, project, product string) error {
	q, err := transactionSQL(transaction, "work_removal")
	if err != nil {
		return err
	}
	if proof == nil || proof.project != project || proof.request.ProductID != product {
		return defaultRemovalRefusal("default-checkout removal has no bound native retirement proof")
	}
	state, err := defaultCheckoutRemovalEligibilityQ(ctx, q, proof.request, project)
	if err != nil {
		return err
	}
	if state.root != proof.state.root || !slices.Equal(state.claims, proof.state.claims) || !slices.Equal(state.entries, proof.state.entries) || !slices.Equal(state.outcomes, proof.state.outcomes) || !slices.Equal(state.fences, proof.state.fences) {
		return defaultRemovalRefusal("retirement identities changed after native observation")
	}
	return nil
}
