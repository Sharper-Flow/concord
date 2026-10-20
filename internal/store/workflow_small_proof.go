package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

func smallProofMissing(detail string) error {
	return newFailure(KindMissingEvidence, "workflow_action", "small proof requires "+detail, false, "record current outcomes, run and bind the exact footprint checks, and accept a fresh independent native ship review")
}

// The same derivation gates advertised admission and both terminal folds.
// A fold reads only events before itself; retained native provenance replaces
// filesystem and Git observations when deciding a historical prefix.
func requireWorkflowSmallProofBefore(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, beforeSeq int64) error {
	if !workflowCapabilities(definition).has(WorkflowCapabilitySmallProof) {
		return nil
	}
	contract, err := workflowCompletionContractRows(ctx, q, workID)
	if err != nil {
		return err
	}
	checks, frontier, err := readWorkflowSmallFootprintBefore(ctx, q, workID, definition, contract.Version, beforeSeq)
	if err != nil {
		return err
	}
	if debt, err := workflowPostRejectionReviewOutstandingBefore(ctx, q, workID, definition, "workflow_action", beforeSeq); err != nil {
		return err
	} else if debt {
		return smallProofMissing("a fresh accepted review after repair")
	}
	lineage, err := readWorkerOracleFindingLineageBeforeTx(ctx, q, workID, 0, beforeSeq)
	if err != nil {
		return err
	}
	if len(lineage.openFindingIDs()) != 0 {
		return smallProofMissing("canonical closure of every blocking finding")
	}
	verdicts, err := latestWorkflowVerdictsBefore(ctx, q, workID, contract.Version, beforeSeq)
	if err != nil {
		return err
	}
	for _, predicate := range contract.Predicates {
		var valid bool
		for _, verdict := range verdicts {
			if verdict.PredicateID != predicate.PredicateID || verdict.ContractVersion != contract.Version || verdict.VerdictKind != "ok" || verdict.IncomparableWithApproved {
				continue
			}
			if err := workflowSmallActorIndependentBefore(ctx, q, workID, verdict.VerdictActorRef, verdict.VerdictModel, definition.EvaluatorIndependence.ModelDistinct, beforeSeq); err != nil {
				return err
			}
			if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.contract_version')=? AND json_extract(payload,'$.predicate_id')=? AND seq>? AND (?=0 OR seq<?))`, workID, WorkflowVerdictRecorded, contract.Version, predicate.PredicateID, frontier, beforeSeq, beforeSeq).Scan(&valid); err != nil {
				return err
			}
		}
		if !valid {
			return smallProofMissing("a current successful verdict for " + predicate.PredicateID)
		}
	}
	window, err := workflowSmallReviewBefore(ctx, q, workID, definition, frontier, beforeSeq)
	if err != nil {
		return err
	}
	proofs, err := readWorkflowFootprintProofs(ctx, q, workflowFootprintProofBasis{WorkID: workID, ProjectID: window.ProjectID, Commit: window.SubjectCommit, Frontier: frontier, BeforeSeq: beforeSeq}, checks)
	if err != nil {
		return err
	}
	for _, proof := range proofs {
		if !proof.Supported || proof.OperationRef == "" {
			return smallProofMissing("the exact bound receipt for " + proof.Check.Kind + ":" + proof.Check.Value)
		}
	}
	return nil
}

func readWorkflowSmallFootprintBefore(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, version, beforeSeq int64) ([]FootprintCheck, int64, error) {
	binding, frontier, err := readWorkflowFootprintBindingBeforeTx(ctx, q, workID, version, beforeSeq)
	if err != nil {
		return nil, 0, err
	}
	if binding == nil {
		return nil, 0, smallProofMissing("the active contract's recorded footprint binding")
	}
	var raw string
	err = q.QueryRowContext(ctx, `SELECT payload FROM domain_events WHERE event_id=? AND subject_type='work_item' AND subject_id=? AND kind=? AND payload_version>=2 AND seq<?`, binding.AssessmentEventID, workID, WorkSized, frontier).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, 0, smallProofMissing("a complete recorded footprint assessment before contract approval")
	}
	if err != nil {
		return nil, 0, err
	}
	var sized workSizedPayload
	if err := json.Unmarshal([]byte(raw), &sized); err != nil {
		return nil, 0, err
	}
	checks, complete := workflowFootprintChecks(sized.Footprint)
	if !complete {
		return nil, 0, smallProofMissing("a complete footprint check inventory")
	}
	repair, err := workflowPostRejectionRepairFrontierBefore(ctx, q, workID, workflowReviewProofRoles(definition).producer, 0, "workflow_action", beforeSeq)
	return checks, max(frontier, repair), err
}

// Footprint checks remain mandatory even after declared evidence kinds and
// outcome verdicts are satisfied. Reuse the existing binding recovery owner
// for the exact native runs, not a generic permission to bind arbitrary refs.
func workflowSmallFootprintRequirements(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, version, beforeSeq int64) ([]workflowEvidenceRequirement, error) {
	if !workflowCapabilities(definition).has(WorkflowCapabilitySmallProof) {
		return nil, nil
	}
	checks, frontier, err := readWorkflowSmallFootprintBefore(ctx, q, workID, definition, version, beforeSeq)
	if err != nil {
		return nil, err
	}
	var current *WorktreeVerifyResult
	if beforeSeq == 0 {
		current, err = readCurrentOracleSubjectReceipt(ctx, q, workID)
	} else {
		// A binding fold's original native pin cannot depend on today's
		// auxiliary claim path or on a producer started after that binding.
		var raw string
		err = q.QueryRowContext(ctx, oracleVerifyReceiptSource(ctx, oracleVerifyReceiptSQL)+` AND l.event_watermark<? ORDER BY d.rowid DESC LIMIT 1`, workID, beforeSeq).Scan(&raw)
		if err == sql.ErrNoRows {
			return nil, nil
		}
		if err == nil {
			current, err = decodeOracleVerifyReceipt(raw, workID)
		}
	}
	if err != nil || current == nil {
		return nil, err
	}
	basis := workflowFootprintProofBasis{WorkID: workID, ProjectID: current.ProjectID, Commit: strings.TrimPrefix(current.SubjectRef, "commit:"), Frontier: frontier, BeforeSeq: beforeSeq}
	var requirements []workflowEvidenceRequirement
	for _, check := range checks {
		command, supported := workflowFootprintCheckCommand(check)
		if !supported {
			continue
		}
		ref, _, err := readWorkflowFootprintRun(ctx, q, basis, check, command, false)
		if err != nil {
			return nil, err
		}
		if ref != "" {
			requirements = append(requirements, workflowEvidenceRequirement{Kind: string(EvidenceVerification), Reference: ref})
		}
	}
	return requirements, nil
}

func workflowSmallReviewBefore(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, frontier, beforeSeq int64) (WorkerDispatchWindow, error) {
	candidates, err := readAcceptedWorkflowReviewsBefore(ctx, q, workID, beforeSeq)
	if err != nil {
		return WorkerDispatchWindow{}, err
	}
	anchors, err := readReviewDeliveryAnchorsBefore(ctx, q, workID, beforeSeq)
	if err != nil {
		return WorkerDispatchWindow{}, err
	}
	for _, candidate := range candidates {
		window, _, err := readAuthorizedDispatchWindowBefore(ctx, q, workID, candidate.attemptID, beforeSeq)
		if err != nil {
			return WorkerDispatchWindow{}, err
		}
		if window.StartSeq <= frontier || window.StepID != workflowReviewProofRoles(definition).review || window.ProjectID == "" || window.ClaimOpID == "" || !worktreeSHAPattern.MatchString(window.SubjectCommit) {
			continue
		}
		anchor := selectReviewDeliveryAnchor(anchors, window.WorktreeIdentity)
		if _, err := anchor.cleanCommit(); err != nil || anchor.acceptSeq >= window.StartSeq {
			continue
		}
		delivery, _, err := readAuthorizedDispatchWindowBefore(ctx, q, workID, anchor.attemptID, beforeSeq)
		if err != nil {
			return WorkerDispatchWindow{}, err
		}
		if delivery.ProjectID != window.ProjectID || delivery.ClaimOpID != window.ClaimOpID {
			continue
		}
		if editing, err := newerEditingDispatchBefore(ctx, q, workID, anchor.dispatchSeq, beforeSeq); err != nil {
			return WorkerDispatchWindow{}, err
		} else if editing {
			continue
		}
		if stale, err := workflowStaleReviewAcceptBeforeTx(ctx, q, workID, candidate.attemptID, "workflow_action", beforeSeq); err != nil {
			return WorkerDispatchWindow{}, err
		} else if stale {
			continue
		}
		var dispatched, completed string
		err = q.QueryRowContext(ctx, `SELECT d.payload,c.payload FROM domain_events d JOIN domain_events c ON c.subject_type=d.subject_type AND c.subject_id=d.subject_id AND c.kind=? AND json_extract(c.payload,'$.attempt_id')=json_extract(d.payload,'$.attempt_id') AND c.seq>d.seq AND c.seq<? WHERE d.subject_type='work_item' AND d.subject_id=? AND d.kind=? AND json_extract(d.payload,'$.attempt_id')=? AND d.seq>? AND (?=0 OR (d.seq<? AND c.seq<?))`, WorkerCompleted, candidate.acceptSeq, workID, WorkerDispatched, candidate.attemptID, window.StartSeq, beforeSeq, beforeSeq, beforeSeq).Scan(&dispatched, &completed)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return WorkerDispatchWindow{}, err
		}
		var dispatch WorkerDispatchedPayload
		var report WorkerCompletedPayload
		if json.Unmarshal([]byte(dispatched), &dispatch) != nil || json.Unmarshal([]byte(completed), &report) != nil || report.Review == nil || report.Review.Verdict != "ship" || dispatch.LaneActorRef == "" || dispatch.PacketDigest != window.PacketDigest || dispatch.ReadbackModel != report.ReadbackModel {
			continue
		}
		if err := workflowSmallActorIndependentBefore(ctx, q, workID, dispatch.LaneActorRef, report.ReadbackModel, definition.EvaluatorIndependence.ModelDistinct, beforeSeq); err != nil {
			if failure := workflowFailureOf(err); failure.Kind == KindUnavailable {
				return WorkerDispatchWindow{}, err
			}
			continue
		}
		if beforeSeq == 0 {
			current, err := readCurrentOracleSubjectReceipt(ctx, q, workID)
			if err != nil {
				return WorkerDispatchWindow{}, err
			}
			if current == nil || current.ProjectID != window.ProjectID || current.ClaimOpID != window.ClaimOpID || current.WorktreeIdentity != window.WorktreeIdentity || strings.TrimPrefix(current.SubjectRef, "commit:") != window.SubjectCommit {
				continue
			}
		}
		return window, nil
	}
	return WorkerDispatchWindow{}, smallProofMissing("an accepted independent native ship review of the current contract and candidate")
}

// Actor tuples are immutable. Their registration and every execution/model
// observation still need the same prefix as the proof that consumes them.
func workflowSmallActorIndependentBefore(ctx context.Context, q queryer, workID, actorRef, model string, modelDistinct bool, beforeSeq int64) error {
	var evaluator WorkflowActor
	err := q.QueryRowContext(ctx, `SELECT actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class FROM workflow_actors a WHERE actor_ref=? AND EXISTS(SELECT 1 FROM domain_events e WHERE e.kind=? AND json_extract(e.payload,'$.actor_ref')=a.actor_ref AND (?=0 OR e.seq<?))`, actorRef, WorkflowActorRecorded, beforeSeq, beforeSeq).Scan(&evaluator.ActorRef, &evaluator.PrincipalRef, &evaluator.ClientRef, &evaluator.AgentRef, &evaluator.SessionRef, &evaluator.ActorClass)
	if err == sql.ErrNoRows {
		return smallProofMissing("the authenticated evaluator actor tuple")
	}
	if err != nil {
		return err
	}
	evaluator.Model = model
	rows, err := q.QueryContext(ctx, `SELECT a.actor_ref,a.principal_ref,a.client_ref,a.agent_ref,a.session_ref,a.actor_class,COALESCE(json_extract(e.payload,'$.execution_model'),json_extract(e.payload,'$.readback_model'),'') FROM domain_events e LEFT JOIN workflow_actors a ON a.actor_ref=CASE WHEN e.kind=? THEN json_extract(e.payload,'$.lane_actor_ref') ELSE json_extract(e.payload,'$.actor_ref') END WHERE e.subject_type='work_item' AND e.subject_id=? AND (e.kind=? OR (e.kind=? AND json_extract(e.payload,'$.capability_class') IN ('implementation','design'))) AND (?=0 OR e.seq<?)`, WorkerDispatched, workID, WorkflowActionStarted, WorkerDispatched, beforeSeq, beforeSeq)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var executor WorkflowActor
		if err := rows.Scan(&executor.ActorRef, &executor.PrincipalRef, &executor.ClientRef, &executor.AgentRef, &executor.SessionRef, &executor.ActorClass, &executor.Model); err != nil {
			return err
		}
		if err := validateDistinctWorkflowActorsForReplay(executor, evaluator, modelDistinct); err != nil {
			return err
		}
	}
	return rows.Err()
}
