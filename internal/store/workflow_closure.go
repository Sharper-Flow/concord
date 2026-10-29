package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

func reviewedClosureSubject(kind, hash string) bool {
	return (kind == string(EvidenceReview) || kind == string(EvidenceDurableNote)) && validSHA256Prefixed(hash)
}

// reviewedClosureContentHashQ reads the current close-step judgement, not an
// arbitrary review binding. The phase exit and its immediately preceding
// binding must belong to the same settled action, actor, and active contract.
// Invalid provenance on a recorded judgement fails closed rather than turning
// a bound receipt into an unbound publication.
func reviewedClosureContentHashQ(ctx context.Context, q queryer, workID string) (string, error) {
	var step, actor, currentStep string
	var contractVersion, exitSeq, recordVersion int64
	err := q.QueryRowContext(ctx, `SELECT x.step_id,x.actor,x.contract_version,x.exit_seq,x.record_version,i.current_step
 FROM workflow_phase_exits x JOIN workflow_instances i ON i.work_id=x.work_id
 WHERE x.work_id=? AND x.action_id='bind_evidence'`, workID).Scan(&step, &actor, &contractVersion, &exitSeq, &recordVersion, &currentStep)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", workflowProjectionError(err, "cannot read reviewed closure exit")
	}
	entry, err := VerifyWorkflowInstanceDefinitionTx(ctx, q, BuiltinWorkflowRegistry(), workID)
	if err != nil {
		return "", err
	}
	if currentStep != step || !stepDeclaresAction(entry.Definition, step, "complete") || !stepDeclaresAction(entry.Definition, step, "bind_evidence") {
		return "", nil
	}
	activeVersion, err := activeWorkflowContractVersion(ctx, q, workID, "reviewed_closure")
	if err != nil {
		return "", err
	}
	if contractVersion != activeVersion {
		return "", nil
	}
	if err := requireActor(ctx, q, actor); err != nil {
		return "", err
	}
	var raw []byte
	var payloadDigest string
	// The action dispatcher writes :completed and :semantic:evidence:N effect
	// identities. Matching both receipts prevents a later unrelated binding
	// from being attributed to an earlier close-step exit.
	err = q.QueryRowContext(ctx, `SELECT b.payload,r.payload_digest
 FROM workflow_evidence_bindings b
 JOIN effect_receipts x ON x.effect_seq=? AND x.kind=? AND x.subject_type='work_item' AND x.subject_id=b.work_id AND x.actor=?
 JOIN durable_operations d ON d.op_id=substr(x.event_id,1,length(x.event_id)-length(':completed')) AND x.event_id=d.op_id||':completed' AND d.attempt_epoch=1 AND d.work_id=b.work_id AND d.step_id=? AND d.workflow_type_ref=? AND d.result_kind='completed' AND d.completed_at IS NOT NULL
 JOIN workflow_actors a ON a.actor_ref=b.actor AND a.principal_ref=d.principal_ref
 JOIN effect_receipts r ON r.event_id=b.binding_id AND r.effect_seq=b.binding_seq AND r.kind=? AND r.subject_type='work_item' AND r.subject_id=b.work_id AND r.actor=b.actor
 JOIN durable_operations p ON p.op_id=json_extract(b.payload,'$.producer_run_ref') AND p.attempt_epoch=1 AND p.work_id=b.work_id AND p.principal_ref=json_extract(b.payload,'$.producer_id') AND p.request_id=json_extract(b.payload,'$.producer_watermark') AND p.result_kind='completed' AND p.completed_at IS NOT NULL
 WHERE b.work_id=? AND b.actor=? AND b.binding_seq=?
 AND json_extract(b.payload,'$.work_id')=b.work_id AND json_extract(b.payload,'$.resulting_version')=?
 AND substr(b.binding_id,1,length(d.op_id||':semantic:evidence:'))=d.op_id||':semantic:evidence:'
 AND EXISTS(SELECT 1 FROM json_each(d.evidence_refs) WHERE value=json_extract(b.payload,'$.immutable_subject_ref'))
 AND EXISTS(SELECT 1 FROM json_each(p.evidence_refs) WHERE value=json_extract(b.payload,'$.immutable_subject_ref'))`,
		exitSeq, WorkflowActionCompleted, actor, step, entry.Definition.Ref, WorkflowEvidenceBound, workID, actor, exitSeq-1, recordVersion-1).Scan(&raw, &payloadDigest)
	if err == sql.ErrNoRows {
		return "", newFailure(KindInvariantViolation, "reviewed_closure", "reviewed closure exit has no corresponding settled evidence binding", false, "restore the closure binding and its exact producer provenance")
	}
	if err != nil {
		return "", workflowProjectionError(err, "cannot read reviewed closure binding")
	}
	var binding workflowEvidenceBoundPayload
	if err := json.Unmarshal(raw, &binding); err != nil || nativeDigest(raw) != payloadDigest {
		return "", newFailure(KindInvariantViolation, "reviewed_closure", "reviewed closure binding has an invalid payload or effect digest", false, "restore the closure binding and its exact producer provenance")
	}
	// Ordinary late recovery is still evidence, but it is not a closure
	// judgement. Completion requires a reviewed hash as the final bind.
	if !reviewedClosureSubject(binding.EvidenceKind, binding.ImmutableSubjectRef) {
		return "", nil
	}
	return binding.ImmutableSubjectRef, nil
}

func requireReviewedClosureQ(ctx context.Context, q queryer, workID string) error {
	hash, err := reviewedClosureContentHashQ(ctx, q, workID)
	if err != nil {
		return err
	}
	if hash == "" {
		return newFailure(KindMissingEvidence, "complete_workflow", "workflow completion requires a reviewed closure content hash", false, "bind the reviewed receipt's exact sha256 content hash on the close step before complete")
	}
	return nil
}

// reviewedTerminalContentHashQ selects only the proof owned by the terminal
// route. Cancellation carries its judgement in the lifecycle transition, not
// an earlier workflow step. Other termination routes confer no closure proof.
func reviewedTerminalContentHashQ(ctx context.Context, q queryer, workID string) (string, error) {
	var lifecycle string
	err := q.QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id=?`, workID).Scan(&lifecycle)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", workflowProjectionError(err, "cannot read reviewed terminal lifecycle")
	}
	switch lifecycle {
	case "completed":
		return reviewedClosureContentHashQ(ctx, q, workID)
	case "cancelled":
		return reviewedCancellationContentHashQ(ctx, q, workID)
	default:
		return "", nil
	}
}

func reviewedCancellationContentHashQ(ctx context.Context, q queryer, workID string) (string, error) {
	var raw, from, reason string
	var version int64
	var receiptDigest sql.NullString
	var provenance bool
	// The lifecycle fold records the authenticated principal and effect
	// coordinate atomically with these refs. No workflow actor or producer run
	// exists for an ordinary lifecycle judgement; do not substitute a binder.
	err := q.QueryRowContext(ctx, `SELECT w.transition_evidence_refs,w.transition_from,w.transition_reason,w.transition_version,r.payload_digest,
 EXISTS(SELECT 1 FROM effect_receipts r WHERE r.effect_seq=w.transition_seq
 AND r.kind='work.transitioned' AND r.subject_type='work_item' AND r.subject_id=w.id
 AND r.actor=w.transition_actor AND r.actor<>'' AND r.payload_version=1 AND w.transition_version>0
 AND w.transition_version<=w.version AND w.transition_from IN ('needed','in_progress')
 AND w.terminal_time IS NOT NULL)
 FROM work_items w LEFT JOIN effect_receipts r ON r.effect_seq=w.transition_seq
 WHERE w.id=? AND w.lifecycle='cancelled'`, workID).Scan(&raw, &from, &reason, &version, &receiptDigest, &provenance)
	if err != nil {
		return "", workflowProjectionError(err, "cannot read reviewed cancellation evidence")
	}
	var refs []string
	if err := json.Unmarshal([]byte(raw), &refs); err != nil {
		return "", newFailure(KindInvariantViolation, "reviewed_closure", "cancellation evidence refs are invalid", false, "restore the authoritative lifecycle evidence")
	}
	hash := ""
	for _, ref := range refs {
		if !validSHA256Prefixed(ref) {
			continue
		}
		if hash != "" && hash != ref {
			return "", newFailure(KindInvariantViolation, "reviewed_closure", "cancellation evidence names multiple reviewed content hashes", false, "resolve the ambiguous lifecycle evidence before publication or cleanup")
		}
		hash = ref
	}
	if hash != "" {
		payload, err := json.Marshal(workTransitionPayload{From: from, To: "cancelled", Reason: reason, EvidenceRefs: refs, ExpectedVersion: version - 1, ResultingVersion: version})
		if err != nil {
			return "", err
		}
		digest, err := effectPayloadDigest(payload)
		if err != nil {
			return "", err
		}
		if !provenance || !receiptDigest.Valid || digest != receiptDigest.String {
			return "", newFailure(KindInvariantViolation, "reviewed_closure", "reviewed cancellation has no corresponding lifecycle effect receipt", false, "restore the lifecycle judgement and its exact actor provenance")
		}
	}
	return hash, nil
}

// CheckReviewedClosureContentHash holds publication to an existing reviewed
// terminal receipt. Unbound legacy or manual work may publish but is not collectible.
func (s *Store) CheckReviewedClosureContentHash(ctx context.Context, workID, hash string) error {
	return s.readSnapshot(ctx, func(q queryer) error {
		return checkReviewedClosureContentHashQ(ctx, q, workID, hash)
	})
}

func checkReviewedClosureContentHashQ(ctx context.Context, q queryer, workID, hash string) error {
	bound, err := reviewedTerminalContentHashQ(ctx, q, workID)
	if err != nil {
		return err
	}
	if bound != "" && bound != hash {
		return newFailure(KindOutcomeMismatch, "reviewed_closure", "publication content hash differs from the reviewed closure", false, "publish the exact reviewed receipt bytes")
	}
	return nil
}
