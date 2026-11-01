package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func preflightWorkflowLawPinRetirementUpgrade(ctx context.Context, q queryer) error {
	var tables int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type='table' AND name IN ('workflow_contract_law_revisions','workflow_review_state')`).Scan(&tables); err != nil {
		return err
	}
	if tables != 2 {
		return nil
	}
	// The admission reader needs the schema entering this cutover. Older
	// prefixes are checked at migration 176 after their pending steps run.
	var predecessorReady bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=174)`).Scan(&predecessorReady); err != nil {
		return err
	}
	if !predecessorReady {
		return nil
	}
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT w.id,i.definition_ref,i.current_step
FROM work_items w JOIN workflow_instances i ON i.work_id=w.id
JOIN workflow_contracts c ON c.work_id=w.id AND c.superseded_by IS NULL
JOIN workflow_contract_law_revisions p ON p.work_id=w.id AND p.contract_version=c.contract_version
JOIN workflow_review_state r ON r.work_id=w.id
WHERE w.lifecycle NOT IN `+terminalLifecycleSQLList()+` ORDER BY w.id`)
	if err != nil {
		return err
	}
	var candidates []struct{ id, definition, step string }
	for rows.Next() {
		var candidate struct{ id, definition, step string }
		if err := rows.Scan(&candidate.id, &candidate.definition, &candidate.step); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, candidate)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var blockers []string
	registry := BuiltinWorkflowRegistry()
	for _, candidate := range candidates {
		registered, err := resolveCurrentWorkflowDefinition(registry, candidate.definition)
		if err != nil {
			return err
		}
		definition := registered.Definition
		if !workflowDefinitionProducesDelivery(definition) || !stepDeclaresAction(definition, candidate.step, "complete") || stepDeclaresAction(definition, candidate.step, "record_review") {
			continue
		}
		review, err := loadWorkflowReviewState(ctx, q, candidate.id)
		if err != nil {
			var syntaxError *json.SyntaxError
			var typeError *json.UnmarshalTypeError
			if errors.As(err, &syntaxError) || errors.As(err, &typeError) {
				return wrapFailure(KindInvariantViolation, "migrate", "cannot inspect retained Review for work "+candidate.id+" before law-pin retirement", false, "restore a verified backup into a new path with concord restore, then swap", err)
			}
			return err
		}
		if review.Accepted == nil || review.Candidate == nil || review.Dependencies == nil || review.DependenciesCurrent || review.Accepted.Report.OutputCandidate != *review.Candidate {
			continue
		}
		before, err := workflowReviewDependenciesBeforePinRetirement(ctx, q, candidate.id, *review.Dependencies)
		if err != nil {
			return err
		}
		if !sameReviewDependencies(review.Accepted.Report.Dependencies, before) {
			continue
		}
		moved, err := verificationReportMovedCandidate(ctx, q, candidate.id, *review.Accepted)
		if err != nil {
			return err
		}
		if moved {
			continue
		}
		state, err := loadWorkflowAdmissionStateTxForRegistry(ctx, q, candidate.id, definition, candidate.step, "migrate", registry)
		if err != nil {
			return err
		}
		if !workflowAdmit(definition, state, "record_review").Admitted {
			blockers = append(blockers, candidate.id)
		}
	}
	if len(blockers) == 0 {
		return nil
	}
	return newFailure(KindUpgradeBlocked, "migrate",
		fmt.Sprintf("law-pin retirement would invalidate accepted Review without an admitted re-review route for work: %s; no migration was applied", strings.Join(blockers, ", ")), true,
		"complete or cancel the named work with the installed release, then retry concord upgrade")
}

// Only the cutover preflight reads the retiring digest inputs. Accepted reports
// and their receipt-bound bytes remain untouched.
func workflowReviewDependenciesBeforePinRetirement(ctx context.Context, q queryer, work string, deps ReviewDependencies) (ReviewDependencies, error) {
	version, err := activeWorkflowContractVersion(ctx, q, work, "migrate")
	if err != nil {
		return deps, err
	}
	rows, err := q.QueryContext(ctx, `SELECT law_id,content_hash FROM workflow_contract_law_revisions WHERE work_id=? AND contract_version=? ORDER BY law_id`, work, version)
	if err != nil {
		return deps, err
	}
	type revision struct {
		LawID       string `json:"law_id"`
		ContentHash string `json:"content_hash"`
	}
	var pins []revision
	for rows.Next() {
		var pin revision
		if err := rows.Scan(&pin.LawID, &pin.ContentHash); err != nil {
			rows.Close()
			return deps, err
		}
		pins = append(pins, pin)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return deps, err
	}
	design, err := readCurrentWorkflowDesign(ctx, q, work)
	if err != nil {
		return deps, err
	}
	if design == nil {
		design = &WorkflowDesignRecord{}
	}
	deps.ArchitectureNotApplicable = false
	deps.ArchitectureDigest = reviewDigest(struct {
		Approach    string                   `json:"approach"`
		Decisions   []WorkflowDesignDecision `json:"decisions"`
		TouchedRefs []string                 `json:"touched_refs"`
		LawPins     []revision               `json:"law_pins,omitempty"`
	}{design.Approach, design.Decisions, design.TouchedRefs, pins})
	return deps, nil
}
