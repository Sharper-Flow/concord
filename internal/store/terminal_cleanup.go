package store

import (
	"context"
	"database/sql"
	"strings"
)

// refusePublishedWorkTx fences a work identity whose canonical outcome note is
// indexed from Git but whose live row is gone. Terminal cleanup deletes the
// execution state, including idempotency records, so a late retry would
// otherwise recreate the identity. Git, not a retained SQLite tombstone, is the
// proof the identity is closed.
func refusePublishedWorkTx(ctx context.Context, q queryer, workID, operation string) error {
	return publishedWorkAbsenceQ(ctx, q, workID, operation)
}

// CleanupPublishedTerminalWork releases execution state after publication settles.
func (s *Store) CleanupPublishedTerminalWork(ctx context.Context, workID string) error {
	const op = "terminal_cleanup"
	retained, err := retainedExecutionExistsQ(ctx, s.db, workID)
	if err != nil || !retained {
		return err
	}
	var version int64
	var lifecycle string
	err = s.db.QueryRowContext(ctx, `SELECT version,lifecycle FROM work_items WHERE id=?`, workID).Scan(&version, &lifecycle)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return wrapFailure(KindUnavailable, op, "cannot read terminal execution", true, "retry reconciliation when the store is readable", err)
	}
	if lifecycle != "completed" && lifecycle != "cancelled" && lifecycle != "superseded" {
		return newFailure(KindNotTerminal, op, "execution is not terminal", false, "complete the execution before cleanup")
	}
	home, err := s.ResolveCompactionHome(ctx, workID)
	if err != nil {
		return err
	}
	var commit, path, hash, terminal string
	if err := s.db.QueryRowContext(ctx, `SELECT commit_oid,note_path,content_hash,terminal_state FROM archived_work WHERE id=? AND type='work_note' AND home_project_id=? AND home_locator_id=?`, workID, home.HomeProjectID, home.HomeLocatorID).Scan(&commit, &path, &hash, &terminal); err == sql.ErrNoRows {
		return newFailure(KindMissingEvidence, op, "canonical outcome publication is absent", false, "publish and verify the canonical outcome before cleanup")
	} else if err != nil {
		return err
	}
	verified, err := VerifyCommittedNote(ctx, home.RepoPath, commit, path, hash)
	if err != nil {
		return err
	}
	if verified.ID != workID || verified.Kind != "work_note" || verified.TerminalState != lifecycle || terminal != lifecycle {
		return newFailure(KindInvalidNoteProof, op, "canonical outcome does not match the terminal execution", false, "reconcile the exact canonical work outcome")
	}
	prepared := terminalCleanupProof{workID: workID, version: version, lifecycle: lifecycle, home: home, commit: commit, path: path, hash: hash}
	return s.TransactDurable(ctx, func(transaction *Transaction) error {
		return cleanupPublishedTerminalWorkTx(ctx, transaction, prepared)
	})
}

type terminalCleanupProof struct {
	workID             string
	version            int64
	lifecycle          string
	home               KnowledgeHome
	commit, path, hash string
}

func cleanupPublishedTerminalWorkTx(ctx context.Context, transaction *Transaction, prepared terminalCleanupProof) error {
	const op = "terminal_cleanup"
	workID, version, lifecycle := prepared.workID, prepared.version, prepared.lifecycle
	home, commit, path, hash := prepared.home, prepared.commit, prepared.path, prepared.hash
	tx := transaction.tx
	retained, err := retainedExecutionExistsQ(ctx, tx, workID)
	if err != nil || !retained {
		return err
	}
	var current int64
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT version,lifecycle FROM work_items WHERE id=?`, workID).Scan(&current, &state); err == sql.ErrNoRows {
		return nil
	} else if err != nil {
		return err
	}
	if current != version || state != lifecycle {
		return newFailure(KindVersionConflict, op, "execution changed after canonical proof verification", true, "reread the terminal execution and retry reconciliation")
	}
	currentHome, err := resolveCompactionHome(ctx, tx, workID)
	if err != nil {
		return err
	}
	if currentHome != home {
		return newFailure(KindStaleContext, op, "canonical home changed after proof verification", true, "resolve the current canonical home and retry reconciliation")
	}
	var proof, settled bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM archived_work WHERE id=?1 AND type='work_note' AND home_project_id=?2 AND home_locator_id=?3 AND commit_oid=?4 AND note_path=?5 AND content_hash=?6 AND terminal_state=?7), EXISTS(SELECT 1 FROM durable_operations WHERE work_id=?1 AND workflow_type_ref='concord.pm6.compaction' AND step_id='git_proof' AND result_kind='completed' AND json_extract(accepted_scope_snapshot,'$.work_version') BETWEEN 1 AND ?8 AND json_extract(accepted_scope_snapshot,'$.content_digest')=?6 AND json_extract(accepted_scope_snapshot,'$.home_project_id')=?2 AND json_extract(accepted_scope_snapshot,'$.home_locator_id')=?3)`, workID, home.HomeProjectID, home.HomeLocatorID, commit, path, hash, lifecycle, version).Scan(&proof, &settled); err != nil {
		return err
	}
	if !proof || !settled {
		return newFailure(KindOperationConflict, op, "verified publication is changed or not settled", true, "settle the canonical publication before cleanup")
	}
	reviewedHash, err := reviewedTerminalContentHashQ(ctx, tx, workID)
	if err != nil {
		return err
	}
	if reviewedHash == "" {
		return newFailure(KindMissingEvidence, op, "terminal work has no reviewed closure content hash", false, "retain execution without an authoritative reviewed terminal receipt")
	}
	if reviewedHash != hash {
		return newFailure(KindOutcomeMismatch, op, "canonical publication differs from the reviewed closure", false, "publish the exact reviewed receipt bytes")
	}
	if err := terminalCleanupBlockersQ(ctx, tx, workID); err != nil {
		return err
	}
	scope := newWriteScope(tx)
	if err := scope.enter(ctx); err != nil {
		return err
	}
	if err := removeTerminalResearchBindings(ctx, tx, workID, transaction.now()); err != nil {
		return err
	}
	if err := deleteTerminalExecutionTx(ctx, tx, workID); err != nil {
		return err
	}
	return scope.close(ctx)
}

// Another execution retains an endpoint until it is terminal, published and
// has no unfinished durable operation. This includes optional references.
const terminalCleanupConsumers = `(SELECT w.id FROM work_items w WHERE w.lifecycle NOT IN ('completed','cancelled','superseded') OR NOT EXISTS(SELECT 1 FROM archived_work a WHERE a.id=w.id AND a.type='work_note' AND a.terminal_state=w.lifecycle) OR EXISTS(SELECT 1 FROM durable_operations d WHERE d.work_id=w.id AND (d.result_kind IS NULL OR d.result_kind IN ('pending','partial'))))`

func terminalCleanupBlockersQ(ctx context.Context, q queryer, workID string) error {
	if err := requireReleasedWorkflowLawEnactmentQ(ctx, q, workID, "terminal_cleanup"); err != nil {
		return err
	}
	checks := []struct {
		kind          FailureKind
		query, detail string
	}{
		{KindOperationConflict, `SELECT EXISTS(SELECT 1 FROM relations WHERE (work_id_from=?1 AND work_id_to IN ` + terminalCleanupConsumers + `) OR (work_id_to=?1 AND work_id_from IN ` + terminalCleanupConsumers + `))`, "another execution consumes a relation endpoint"},
		{KindOperationConflict, `SELECT EXISTS(SELECT 1 FROM workflow_candidate_sets WHERE work_id<>?1 AND work_id IN ` + terminalCleanupConsumers + ` AND candidate_kind='work_item' AND candidate_ref=?1)`, "another contract retains this candidate"},
		{KindOperationConflict, `SELECT EXISTS(SELECT 1 FROM workflow_impact_edges WHERE (work_id=?1 AND target_work_id IN ` + terminalCleanupConsumers + `) OR (target_work_id=?1 AND work_id IN ` + terminalCleanupConsumers + `))`, "another execution consumes an impact edge"},
		{KindOperationConflict, `SELECT EXISTS(SELECT 1 FROM workflow_overlap_resolutions WHERE (from_work_id=?1 AND to_work_id IN ` + terminalCleanupConsumers + `) OR (to_work_id=?1 AND from_work_id IN ` + terminalCleanupConsumers + `))`, "another execution consumes an overlap resolution"},
		{KindOperationConflict, `SELECT EXISTS(SELECT 1 FROM workflow_backlog_alignment WHERE related_work_id=?1 AND work_id<>?1 AND work_id IN ` + terminalCleanupConsumers + `)`, "another execution retains backlog alignment"},
		{KindOperationConflict, `SELECT EXISTS(SELECT 1 FROM workflow_contract_law_additions WHERE reservation_owner_work_id=?1 AND work_id<>?1)`, "another contract consumes a law reservation"},
		{KindResearchConsumerBlocked, `SELECT EXISTS(SELECT 1 FROM active_research_packs WHERE owner_work_id=?1)`, "owned research packs must be retired through research_retire"},
		{KindResourceClaimHeld, `SELECT EXISTS(SELECT 1 FROM resource_claims WHERE holder_work_id=?1 AND state='held')`, "execution holds a resource claim"},
		{KindResourceBusy, `SELECT EXISTS(SELECT 1 FROM worktree_claims WHERE work_id=?1 AND state IN ('pending','verified')) OR EXISTS(SELECT 1 FROM worktree_entries WHERE set_id=?2 AND state='active')`, "worktree claim or native tree remains active"},
		{KindResourceBusy, `SELECT EXISTS(SELECT 1 FROM worktree_occupancy o JOIN worktree_entries e ON o.worktree_id=e.set_id||':'||e.project_id||':'||e.claim_op_id WHERE e.set_id=?2)`, "native worktree occupancy has not been released"},
		{KindResourceBusy, `SELECT EXISTS(SELECT 1 FROM worktree_verify_leases WHERE work_id=?1 AND (state='held' OR outcome='running'))`, "native verification is held or unsettled"},
		{KindOperationConflict, `SELECT EXISTS(SELECT 1 FROM session_vacate_requests WHERE work_id=?1 AND landed=0)`, "session vacate has not landed"},
		{KindOperationConflict, `SELECT EXISTS(SELECT 1 FROM project_handoffs WHERE work_id=?1 AND state='recorded')`, "Project handoff has not been consumed"},
		{KindOperationConflict, `SELECT EXISTS(SELECT 1 FROM worktree_ref_outcomes WHERE set_id=?2 AND phase NOT IN ('deleted','settled'))`, "native reference preservation or deletion remains unsettled"},
		{KindOutsideRepairActive, `SELECT EXISTS(SELECT 1 FROM outside_repair_dispositions WHERE work_id=?1 AND state='active')`, "outside repair has not reconciled"},
		{KindOperationConflict, `SELECT EXISTS(SELECT 1 FROM bootstrap_operations WHERE work_id=?1 AND state NOT IN ('completed','rolled_back'))`, "bootstrap effect has not settled"},
		{KindOperationConflict, `SELECT EXISTS(SELECT 1 FROM durable_operations WHERE work_id=?1 AND (result_kind IS NULL OR result_kind IN ('pending','partial')))`, "durable effect has not reconciled"},
		{KindOperationConflict, `SELECT EXISTS(SELECT 1 FROM work_removal_operations WHERE work_id=?1)`, "removal operation retains its separate identity fence"},
		{KindOperationConflict, `SELECT EXISTS(SELECT 1 FROM worker_authorizations a WHERE a.work_id=?1 AND NOT EXISTS(SELECT 1 FROM worker_attempts t WHERE t.work_id=a.work_id AND t.attempt_id=a.attempt_id)) OR EXISTS(SELECT 1 FROM worker_attempts WHERE work_id=?1 AND lifecycle_state IN ('in_flight','dispatched'))`, "worker launch or execution is unknown or in flight"},
		{KindOperationConflict, `SELECT EXISTS(SELECT 1 FROM worker_attempts a WHERE a.work_id=?1 AND a.lifecycle_state IN ('completed','failed') AND NOT EXISTS(SELECT 1 FROM worker_attempt_dispositions d WHERE d.work_id=a.work_id AND d.attempt_id=a.attempt_id))`, "terminal worker result or failure has not been reconciled"},
		{KindOperationConflict, `SELECT EXISTS(SELECT 1 FROM workflow_action_obligations WHERE work_id=?1)`, "workflow action retains an unfinished obligation"},
		{KindOperationConflict, `SELECT EXISTS(SELECT 1 FROM work_messages WHERE (sender_work_id=?1 OR recipient_work_id=?1) AND state='sent')`, "coordination message is still outstanding"},
		{KindMissingEvidence, `SELECT EXISTS(SELECT 1 FROM workflow_external_conditions WHERE work_id=?1 AND condition_state='open')`, "external condition is unresolved"},
		{KindMissingEvidence, `SELECT EXISTS(SELECT 1 FROM workflow_evidence_bindings b WHERE b.work_id<>?1 AND json_extract(b.payload,'$.producer_run_ref') IN (SELECT op_id FROM durable_operations WHERE work_id=?1))`, "another execution retains an evidence producer"},
		{KindMissingEvidence, `SELECT EXISTS(SELECT 1 FROM workflow_contract_law_additions a WHERE a.work_id=?1 AND NOT EXISTS(SELECT 1 FROM archived_work n WHERE n.id=a.law_id AND n.type<>'work_note'))`, "required Product knowledge is not published"},
		{KindOperationConflict, `SELECT EXISTS(SELECT 1 FROM external_observations o WHERE o.work_id<>?1 AND EXISTS(SELECT 1 FROM effect_receipts r WHERE r.subject_type='work_item' AND r.subject_id=?1 AND (r.effect_seq=o.created_event_seq OR r.effect_seq=o.verified_event_seq))) OR EXISTS(SELECT 1 FROM work_size_overrides o JOIN effect_receipts r ON r.event_id=o.event_id WHERE o.work_id<>?1 AND r.subject_type='work_item' AND r.subject_id=?1) OR EXISTS(SELECT 1 FROM workflow_overlap_resolutions o WHERE o.from_work_id<>?1 AND o.to_work_id<>?1 AND EXISTS(SELECT 1 FROM effect_receipts r WHERE r.subject_type='work_item' AND r.subject_id=?1 AND (r.effect_seq=o.event_seq OR r.effect_seq=o.invalidated_seq)))`, "another owner retains an effect receipt"},
		{KindOperationConflict, `SELECT EXISTS(SELECT 1 FROM idempotency_records i WHERE i.op_id NOT IN (SELECT op_id FROM durable_operations WHERE work_id=?1) AND EXISTS(SELECT 1 FROM json_each(i.result_event_ids) j JOIN effect_receipts r ON r.event_id=j.value WHERE r.subject_type='work_item' AND r.subject_id=?1) AND EXISTS(SELECT 1 FROM json_each(i.result_event_ids) j LEFT JOIN effect_receipts r ON r.event_id=j.value WHERE r.event_id IS NULL OR r.subject_type<>'work_item' OR r.subject_id<>?1))`, "shared request retains retry authority over an effect receipt"},
	}
	for _, check := range checks {
		args := []any{workID}
		if strings.Contains(check.query, "?2") {
			args = append(args, WorktreeSetID(workID))
		}
		var blocked bool
		if err := q.QueryRowContext(ctx, check.query, args...).Scan(&blocked); err != nil {
			return wrapFailure(KindUnavailable, "terminal_cleanup", "cannot evaluate cleanup blocker: "+check.detail, true, "retry reconciliation when the owning projection is readable", err)
		}
		if blocked {
			return newFailure(check.kind, "terminal_cleanup", check.detail, false, "release or reconcile the named consumer before retrying cleanup")
		}
	}
	lineage, err := readWorkerOracleFindingLineageTx(ctx, q, workID)
	if err != nil {
		return err
	}
	for _, finding := range lineage.findings {
		if !finding.Closed && finding.blocks() {
			return newFailure(KindMissingEvidence, "terminal_cleanup", "execution retains an unresolved oracle finding", false, "resolve the finding before cleanup")
		}
	}
	return nil
}

func deleteTerminalExecutionTx(ctx context.Context, tx *sql.Tx, workID string) error {
	// Source-owned predicates, not discovered table names or heuristic IDs.
	// The three receipt-FK children rebuilt by migration151 precede receipts.
	for _, query := range []string{
		`DELETE FROM idempotency_records WHERE op_id IN (SELECT op_id FROM durable_operations WHERE work_id=?1) OR (json_array_length(result_event_ids)>0 AND NOT EXISTS(SELECT 1 FROM json_each(result_event_ids) j LEFT JOIN effect_receipts r ON r.event_id=j.value WHERE r.event_id IS NULL OR r.subject_type<>'work_item' OR r.subject_id<>?1))`,
		`DELETE FROM workflow_overlap_resolutions WHERE from_work_id=?1 OR to_work_id=?1`,
		`DELETE FROM external_observations WHERE work_id=?1`,
		`DELETE FROM work_size_overrides WHERE work_id=?1`,
		`DELETE FROM workflow_backlog_alignment WHERE work_id=?1 OR related_work_id=?1`,
		`DELETE FROM workflow_impact_notices WHERE source_work_id=?1 OR target_work_id=?1 OR edge_owner_work_id=?1`,
		`DELETE FROM workflow_impact_edges WHERE work_id=?1 OR target_work_id=?1`,
		`DELETE FROM relations WHERE work_id_from=?1 OR work_id_to=?1`,
		`DELETE FROM work_messages WHERE sender_work_id=?1 OR recipient_work_id=?1`,
		`DELETE FROM resource_claims WHERE holder_work_id=?1`,
		`DELETE FROM workflow_law_baselines WHERE work_id=?1`,
		`DELETE FROM workflow_group_members WHERE work_id=?1`,
		`DELETE FROM workflow_member_groups WHERE work_id=?1`,
		`DELETE FROM workflow_contract_predicates WHERE work_id=?1`,
		`DELETE FROM workflow_contract_law_revisions WHERE work_id=?1`,
		`DELETE FROM workflow_candidate_sets WHERE work_id=?1 OR (candidate_kind='work_item' AND candidate_ref=?1)`,
		`DELETE FROM workflow_contract_law_additions WHERE work_id=?1`,
		`DELETE FROM workflow_contract_verification_obligations WHERE work_id=?1`,
		`DELETE FROM workflow_contract_law_modifications WHERE work_id=?1`,
		`DELETE FROM workflow_contract_domain_relation_modifications WHERE work_id=?1`,
		`DELETE FROM workflow_contract_domain_modifications WHERE work_id=?1`,
		`DELETE FROM workflow_contract_affected_domains WHERE work_id=?1`,
		`DELETE FROM workflow_law_addition_reservations WHERE owner_work_id=?1`,
		`DELETE FROM workflow_architecture_bindings WHERE work_id=?1`,
		`DELETE FROM workflow_premise_confirmations WHERE work_id=?1`,
		`DELETE FROM workflow_context_boundaries WHERE work_id=?1`,
		`DELETE FROM workflow_context_checkpoints WHERE work_id=?1`,
		`DELETE FROM workflow_external_conditions WHERE work_id=?1`,
		`DELETE FROM workflow_checkpoints WHERE work_id=?1`,
		`DELETE FROM workflow_decision_records WHERE work_id=?1`,
		`DELETE FROM workflow_native_runs WHERE work_id=?1`,
		`DELETE FROM workflow_contracts WHERE work_id=?1`,
		`DELETE FROM workflow_design_records WHERE work_id=?1`,
		`DELETE FROM workflow_proposal_records WHERE work_id=?1`,
		`DELETE FROM workflow_instances WHERE work_id=?1 AND NOT EXISTS(SELECT 1 FROM work_items w WHERE w.id=?1 AND ` + retainedDefectClassificationSQL + `)`,
		// A retained classification keeps work_items, so its cascade-owned
		// execution children are deleted explicitly.
		`DELETE FROM worker_attempt_dispositions WHERE work_id=?1`,
		`DELETE FROM worker_authorizations WHERE work_id=?1`,
		`DELETE FROM worker_oracle_finding_facts WHERE work_id=?1`,
		`DELETE FROM workflow_evidence_bindings WHERE work_id=?1`,
		`DELETE FROM workflow_step_completions WHERE work_id=?1`,
		`DELETE FROM workflow_phase_exits WHERE work_id=?1`,
		`DELETE FROM workflow_completed_exits WHERE work_id=?1`,
		`DELETE FROM workflow_predicate_verdicts WHERE work_id=?1`,
		`DELETE FROM workflow_step_epochs WHERE work_id=?1`,
		`DELETE FROM workflow_delivery_state WHERE work_id=?1`,
		`DELETE FROM workflow_review_state WHERE work_id=?1`,
		`DELETE FROM workflow_correction_state WHERE work_id=?1`,
		`DELETE FROM workflow_artifact_actions WHERE work_id=?1`,
		`DELETE FROM workflow_coordinator_actions WHERE work_id=?1`,
		`DELETE FROM work_version_actions WHERE work_id=?1`,
		`DELETE FROM work_context_findings WHERE work_id=?1`,
		`DELETE FROM work_context_state WHERE work_id=?1`,
		`DELETE FROM work_size_proof_facts WHERE work_id=?1`,
		`DELETE FROM work_observations WHERE work_id=?1`,
		`DELETE FROM work_size_assessments WHERE work_id=?1`,
		`DELETE FROM worker_attempts WHERE work_id=?1`,
		`DELETE FROM worker_job_revisions WHERE work_id=?1`,
		`DELETE FROM project_handoffs WHERE work_id=?1`,
		`DELETE FROM outside_repair_reconciliations WHERE work_id=?1`,
		`DELETE FROM outside_repair_dispositions WHERE work_id=?1`,
		`DELETE FROM bootstrap_operations WHERE work_id=?1`,
		`DELETE FROM worktree_verify_leases WHERE work_id=?1`,
		`DELETE FROM session_claim_landings WHERE work_id=?1`,
		`DELETE FROM session_vacate_requests WHERE work_id=?1`,
		`DELETE FROM session_relocation_counts WHERE work_id=?1`,
		`DELETE FROM worktree_ref_outcomes WHERE set_id=?2`,
		`DELETE FROM worktree_entries WHERE set_id=?2`,
		`DELETE FROM worktree_claims WHERE work_id=?1`,
		`DELETE FROM worktree_claim_native_fences WHERE set_id=?2`,
		`DELETE FROM durable_operations WHERE work_id=?1`,
		`DELETE FROM work_projects WHERE work_id=?1 AND NOT EXISTS(SELECT 1 FROM work_items w WHERE w.id=?1 AND ` + retainedDefectClassificationSQL + `)`,
		`DELETE FROM work_items AS w WHERE id=?1 AND NOT ` + retainedDefectClassificationSQL,
		`DELETE FROM effect_receipts WHERE subject_type='work_item' AND subject_id=?1`,
	} {
		args := []any{workID}
		if strings.Contains(query, "?2") {
			args = append(args, WorktreeSetID(workID))
		}
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return wrapFailure(KindUnavailable, "terminal_cleanup", "cannot delete released execution rows", true, "reconcile the remaining execution owners before retrying cleanup", err)
		}
	}
	return nil
}
