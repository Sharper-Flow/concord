package store

import (
	"context"
	"encoding/json"
	"strings"
)

// Artifact staleness holds an unhealthy judgment until production at the
// declared recovery-route target. Contract revisions instead invalidate the
// exact Review and proof dependencies they change (CD-0242 D7). Those owners
// compare WorkerReviewBlock.Dependencies and the association's CriterionDigest
// in workflow_review.go; a supersession sequence is not a dependency digest.
// Keeping that comparison there permits reassessing an unchanged candidate
// without demanding another implementation delivery.

// workflowArtifactStale folds whether the artifact the current step owes or
// judges is stale. Ownership is span-resolved from the declared routes: a
// step inside an unhealthy_verdict route's producer-to-evaluator span — the
// producer itself, every intermediate step, and the evaluator — folds the
// route target's artifact; a step no route spans folds false, because no
// declared evaluation judges an artifact through it. The historical bit
// therefore survives the corrective return to the producer and every step
// between producer and evaluator: moving never reproduces an artifact. The
// read uses current verdict and production facts, never the singular active
// contract, so a duplicated projection folds the same answer the recovery reads.
func workflowArtifactStale(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string) (bool, error) {
	staleSeq, err := workflowArtifactStaleFrontier(ctx, q, workID, subject)
	if err != nil {
		return false, err
	}
	if staleSeq == 0 {
		return false, nil
	}
	for _, target := range workflowStalenessSpanTargets(definition, currentStep) {
		productionSeq, err := workflowArtifactProductionFrontier(ctx, q, workID, definition, target, staleSeq, 0, subject)
		if err != nil {
			return false, err
		}
		if staleSeq > productionSeq {
			return true, nil
		}
	}
	return false, nil
}

// workflowArtifactStaleFrontier reads the latest unhealthy artifact judgment.
// Dependency changes are checked when Review or criterion proof is reused,
// not converted into a demand for repeated production (CD-0242 D7).
func workflowArtifactStaleFrontier(ctx context.Context, q queryer, workID, subject string) (int64, error) {
	var verdictSeq int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(unhealthy_verdict_seq),0) FROM workflow_instances WHERE work_id=?`, workID).Scan(&verdictSeq); err != nil {
		return 0, wrapFailure(KindUnavailable, subject, "cannot read the artifact-staleness verdict frontier", true, "retry once the workflow verdict projection is readable", err)
	}
	return verdictSeq, nil
}

// workflowProducingCapabilityClasses is the closed positive classification
// of dispatch capability classes whose worker attempts produce artifacts
// (CD-0201 D5). An implementation, design, or research lane builds the
// artifact an evaluator judges; review and verification lanes judge one. A
// dispatch carrying a class the set does not name — or no class at all —
// never produces: identity and class fail closed.
var workflowProducingCapabilityClasses = []string{"implementation", "design", "research"}

// workflowArtifactProductionFrontier reads the latest actual production at
// the declared route target that postdates the stale cause. A producing
// delivery carries positive event and capability authority bound to its
// dispatch origin: the accepted result (accept_worker_result) counts only
// when the acceptance stands at the target step, its worker attempt was
// dispatched at the target step — a dispatch_worker completion there naming
// the same attempt — the attempt's capability dispatch (worker.dispatched,
// the event that carries the capability class) exists with a producing
// class, the dispatch and capability events precede the acceptance, and the
// whole producing origin postdates the stale cause, so a dispatch from
// before the artifact went stale never refreshes it. A completion of one of
// the target's artifact actions (workflowStepArtifactActions) counts with
// its own owning semantics. Evidence-only acceptance, starts, dispatches
// without an accepted delivery, checkpoints, evidence bindings, verdicts,
// and every completion at another step never reach this frontier, so a
// stale artifact cannot become fresh without re-production.
// A nonzero throughSeq names the latest verdict window, not an arbitrary
// historical frontier; direct production is snapshotted only for that window.
func workflowArtifactProductionFrontier(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, target string, staleSeq, throughSeq int64, subject string) (int64, error) {
	actions := workflowStepArtifactActions(definition, target)
	var directSeq int64
	if len(actions) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(actions)), ",")
		field := "production_seq"
		if throughSeq > 0 {
			field = "verdict_production_seq"
		}
		query := `SELECT COALESCE(MAX(` + field + `),0) FROM workflow_artifact_actions WHERE work_id=? AND step_id=? AND ` + field + `>? AND action_id IN (` + placeholders + `)`
		args := []any{workID, target, staleSeq}
		for _, actionID := range actions {
			args = append(args, actionID)
		}
		if throughSeq > 0 {
			query += ` AND verdict_seq=?`
			args = append(args, throughSeq)
		}
		if err := q.QueryRowContext(ctx, query, args...).Scan(&directSeq); err != nil {
			return 0, wrapFailure(KindUnavailable, subject, "cannot read the direct artifact production frontier", true, "retry once the workflow projection is readable", err)
		}
	}
	capabilityPlaceholders := strings.TrimSuffix(strings.Repeat("?,", len(workflowProducingCapabilityClasses)), ",")
	args := []any{workID, target, staleSeq, staleSeq, target, staleSeq}
	for _, class := range workflowProducingCapabilityClasses {
		args = append(args, class)
	}
	query := `SELECT COALESCE(MAX(e.first_acceptance_seq),0) FROM worker_attempt_dispositions e
 JOIN worker_attempts a ON a.work_id=e.work_id AND a.attempt_id=e.attempt_id
 JOIN worker_authorizations d ON d.work_id=e.work_id AND d.attempt_id=e.attempt_id
 WHERE e.work_id=? AND e.action_id='accept_worker_result' AND json_extract(e.first_acceptance_payload,'$.step_id')=? AND e.first_acceptance_seq>?
 AND d.seq>? AND d.seq<e.first_acceptance_seq AND json_extract(d.payload,'$.step_id')=?
 AND a.dispatch_seq>? AND a.dispatch_seq<e.first_acceptance_seq AND json_extract(a.dispatch_payload,'$.capability_class') IN (` + capabilityPlaceholders + `)`
	if throughSeq > 0 {
		query += ` AND e.first_acceptance_seq<=?`
		args = append(args, throughSeq)
	}
	var seq int64
	if err := q.QueryRowContext(ctx, query, args...).Scan(&seq); err != nil {
		return 0, wrapFailure(KindUnavailable, subject, "cannot read the artifact production frontier", true, "retry once the workflow projection is readable", err)
	}
	return max(seq, directSeq), nil
}

// workflowVerdictPayloadNamesOk reports whether one record_verdict payload
// carries an ok verdict entry, in either wire shape. An ok entry that is
// incomparable with the approved result still records the ok kind, so it
// counts: the refusal is for the recorded kind, not the comparison flag.
func workflowVerdictPayloadNamesOk(fields map[string]json.RawMessage) bool {
	entries, err := normalizeWorkflowVerdictEntries(fields)
	if err != nil {
		// A malformed shape is not an ok verdict; the verdict constructor's
		// own validation refuses it before any event exists.
		return false
	}
	for _, entry := range entries {
		if entry.VerdictKind == "ok" {
			return true
		}
	}
	return false
}

// guardRecordVerdictArtifactFreshness is the record_verdict half of the
// artifact-staleness admission (CD-0201 D5): while the folded state carries a
// stale artifact, the evaluator may record only a non-ok verdict. A single ok
// entry or a batch containing one refuses; the declared recovery route —
// request_correction back to the producer — is the path that re-produces the
// artifact and reopens a healthy verdict. Non-ok verdicts stay admitted, so
// the evaluator's independent judgment never waits on the correction.
func guardRecordVerdictArtifactFreshness(g *workflowActionGuardContext) error {
	state, err := g.foldedAdmissionState()
	if err != nil {
		return err
	}
	if !state.ArtifactStale {
		return nil
	}
	fields, fieldsErr := workflowActionObject(g.defaultedPayload())
	if fieldsErr != nil {
		return fieldsErr
	}
	if !workflowVerdictPayloadNamesOk(fields) {
		return nil
	}
	targets := workflowStalenessSpanTargets(g.entry.Definition, g.currentStep)
	return newFailure(KindStaleRequiresReview, "workflow_action", "the artifact this verdict judges is stale: a healthy verdict requires fresh production at "+strings.Join(targets, ", "), false, "record a non-ok verdict or request_correction to return to the producer step")
}
