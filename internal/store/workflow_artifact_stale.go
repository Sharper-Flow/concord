package store

import (
	"context"
	"encoding/json"
	"strings"
)

// Artifact staleness (CD-0201 D5) is the folded truth the designStale field
// models: while the artifact an evaluator judges has gone stale — a bad
// verdict or a successor contract landed after the artifact was last
// produced — the evaluator cannot record the artifact healthy again and the
// premise confirmation cannot step past the verdict. Only actual fresh
// production at the declared recovery-route target clears the bit; a dispatch,
// a fenced start, an evidence rebinding, an unrelated step advance, or a
// re-recorded ok verdict never does.
//
// The fold derives both frontiers from the owning event history: the
// staleness frontier is the latest non-ok verdict or contract supersession,
// and the production frontier is the latest accepted worker delivery or
// artifact-action completion at the current step's unhealthy_verdict route
// target. The step that produces the artifact is the route target, so the
// causal question "was the artifact re-produced after it went stale?" has one
// owner: the declared table.

// workflowArtifactStale folds whether the artifact the current step owes or
// judges is stale. Ownership is span-resolved from the declared routes: a
// step inside an unhealthy_verdict route's producer-to-evaluator span — the
// producer itself, every intermediate step, and the evaluator — folds the
// route target's artifact; a step no route spans folds false, because no
// declared evaluation judges an artifact through it. The historical bit
// therefore survives the corrective return to the producer and every step
// between producer and evaluator: moving never reproduces an artifact. The
// read is total over any projection state: it reads event
// history only, never the singular active contract, so a duplicated
// projection folds the same answer the recovery that owns it reads.
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

// workflowArtifactStaleFrontier reads the latest staleness-causing event: a
// recorded verdict that is not ok or is incomparable with the approved
// result, or a contract supersession that changed the objective the artifact
// must satisfy (CD-0201 D6). Zero when neither stands in the history.
func workflowArtifactStaleFrontier(ctx context.Context, q queryer, workID, subject string) (int64, error) {
	var verdictSeq int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(v.seq),0) FROM domain_events v WHERE v.subject_type=? AND v.subject_id=? AND v.kind=? AND (COALESCE(json_extract(v.payload,'$.verdict_kind'),'')<>'ok' OR COALESCE(json_extract(v.payload,'$.incomparable_with_approved'),0)=1)`, string(SubjectWorkItem), workID, WorkflowVerdictRecorded).Scan(&verdictSeq); err != nil {
		return 0, wrapFailure(KindUnavailable, subject, "cannot read the artifact-staleness verdict frontier", true, "retry once the workflow verdict projection is readable", err)
	}
	var supersedeSeq int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(s.seq),0) FROM domain_events s WHERE s.subject_type=? AND s.subject_id=? AND s.kind=?`, string(SubjectWorkItem), workID, WorkflowContractSuperseded).Scan(&supersedeSeq); err != nil {
		return 0, wrapFailure(KindUnavailable, subject, "cannot read the artifact-staleness supersession frontier", true, "retry once the workflow contract projection is readable", err)
	}
	if supersedeSeq > verdictSeq {
		return supersedeSeq, nil
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
func workflowArtifactProductionFrontier(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, target string, staleSeq, throughSeq int64, subject string) (int64, error) {
	actions := workflowStepArtifactActions(definition, target)
	actionDisjunct := ""
	args := []any{string(SubjectWorkItem), workID, WorkflowActionCompleted, staleSeq}
	if len(actions) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(actions)), ",")
		actionDisjunct = "(json_extract(e.payload,'$.step_id')=? AND json_extract(e.payload,'$.action_id') IN (" + placeholders + "))"
		args = append(args, target)
		for _, actionID := range actions {
			args = append(args, actionID)
		}
	}
	capabilityPlaceholders := strings.TrimSuffix(strings.Repeat("?,", len(workflowProducingCapabilityClasses)), ",")
	args = append(args,
		target,
		WorkflowActionCompleted, target, staleSeq,
		WorkerDispatched, staleSeq)
	for _, class := range workflowProducingCapabilityClasses {
		args = append(args, class)
	}
	// acceptDelivery binds the accepted result to a producing dispatch
	// origin at the target step. The dispatch_worker completion (dc) and
	// the capability dispatch (wd) precede the acceptance (e.seq) and
	// postdate the stale cause, so the origin — not the acceptance alone —
	// decides freshness. An absent step, attempt, or capability path folds
	// NULL out of every comparison and the conjunct fails closed.
	acceptDelivery := `(json_extract(e.payload,'$.action_id')='accept_worker_result' AND json_extract(e.payload,'$.step_id')=?
		AND EXISTS(
			SELECT 1 FROM domain_events dc WHERE dc.subject_type=e.subject_type AND dc.subject_id=e.subject_id
			AND dc.kind=? AND json_extract(dc.payload,'$.action_id')='dispatch_worker'
			AND json_extract(dc.payload,'$.step_id')=?
			AND json_extract(dc.payload,'$.worker_attempt_id')=json_extract(e.payload,'$.worker_attempt_id')
			AND dc.seq>? AND dc.seq<e.seq)
		AND EXISTS(
			SELECT 1 FROM domain_events wd WHERE wd.subject_type=e.subject_type AND wd.subject_id=e.subject_id
			AND wd.kind=? AND json_extract(wd.payload,'$.attempt_id')=json_extract(e.payload,'$.worker_attempt_id')
			AND wd.seq>? AND wd.seq<e.seq
			AND json_extract(wd.payload,'$.capability_class') IN (` + capabilityPlaceholders + `)))`
	disjuncts := acceptDelivery
	if actionDisjunct != "" {
		disjuncts = actionDisjunct + " OR " + acceptDelivery
	}
	query := `SELECT COALESCE(MAX(e.seq),0) FROM domain_events e
		WHERE e.subject_type=? AND e.subject_id=? AND e.kind=? AND e.seq>? AND (` + disjuncts + `)`
	if throughSeq > 0 {
		query += ` AND e.seq<=?`
		args = append(args, throughSeq)
	}
	var seq int64
	if err := q.QueryRowContext(ctx, query, args...).Scan(&seq); err != nil {
		return 0, wrapFailure(KindUnavailable, subject, "cannot read the artifact production frontier", true, "retry once the workflow projection is readable", err)
	}
	return seq, nil
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
