package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
)

type workflowReviewFacts struct {
	Candidate *ReviewCandidate               `json:"candidate"`
	Accepted  *WorkflowAcceptedReview        `json:"accepted"`
	Findings  []ReviewFinding                `json:"findings"`
	Sources   map[string]reviewFindingSource `json:"finding_sources,omitempty"`
}

func readWorkflowReviewFacts(ctx context.Context, q queryer, work string) (WorkflowReviewState, error) {
	state := WorkflowReviewState{Findings: []ReviewFinding{}, PublicationOutstanding: true}
	var raw string
	err := q.QueryRowContext(ctx, `SELECT state FROM workflow_review_state WHERE work_id=?`, work).Scan(&raw)
	if err == sql.ErrNoRows {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	var facts workflowReviewFacts
	if err := json.Unmarshal([]byte(raw), &facts); err != nil {
		return state, err
	}
	state.Candidate, state.Accepted = facts.Candidate, facts.Accepted
	state.findingSources = facts.Sources
	if facts.Findings != nil {
		state.Findings = facts.Findings
	}
	return state, nil
}

func writeWorkflowReviewFacts(ctx context.Context, q migrationRunner, work string, state WorkflowReviewState) error {
	raw, err := json.Marshal(workflowReviewFacts{Candidate: state.Candidate, Accepted: state.Accepted, Findings: state.Findings, Sources: state.findingSources})
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO workflow_review_state(work_id,state) VALUES(?,?) ON CONFLICT(work_id) DO UPDATE SET state=excluded.state`, work, string(raw))
	return err
}

func workflowReviewFindingMap(state WorkflowReviewState) map[string]ReviewFinding {
	findings := map[string]ReviewFinding{}
	for _, finding := range state.Findings {
		findings[finding.FindingID] = finding
	}
	return findings
}

func setWorkflowReviewFindings(state *WorkflowReviewState, findings map[string]ReviewFinding) {
	ids := make([]string, 0, len(findings))
	for id := range findings {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	state.Findings = make([]ReviewFinding, 0, len(ids))
	for _, id := range ids {
		state.Findings = append(state.Findings, findings[id])
	}
}

func applyWorkflowAcceptedReview(state *WorkflowReviewState, accepted WorkflowAcceptedReview, findings map[string]ReviewFinding) {
	for _, finding := range accepted.Report.Findings {
		if old, found := findings[finding.FindingID]; found {
			if old.Oracle != nil {
				finding.Oracle = old.Oracle
			}
			finding.InScope = finding.InScope || old.InScope
			finding.Blocking = finding.Blocking || old.Blocking
		}
		findings[finding.FindingID] = finding
	}
	for _, resolution := range accepted.Report.ResolvedFindings {
		local, found := reviewFindingLocalID(*state, resolution.FindingID)
		if !found {
			continue
		}
		finding, retained := findings[local]
		if !retained {
			continue
		}
		finding.Status = "closed"
		if len(resolution.EvidenceRefs) > 0 {
			finding.ClosureRefs = append([]string{}, resolution.EvidenceRefs...)
		}
		findings[local] = finding
	}
	state.Accepted = &accepted
	candidate := accepted.Report.OutputCandidate
	state.Candidate = &candidate
	state.DependenciesCurrent = state.Dependencies != nil && sameReviewDependencies(accepted.Report.Dependencies, *state.Dependencies)
	setWorkflowReviewFindings(state, findings)
}

func advanceWorkflowRecordedReviewTx(ctx context.Context, tx *sql.Tx, event Event, action workflowActionCompletedPayload) error {
	if action.ActionID != "record_review" {
		return nil
	}
	if err := validateRecordedReview(ctx, tx, event.SubjectID, action.Review); err != nil {
		return err
	}
	if err := validateRecordedReviewObservation(ctx, event.SubjectID, action.Review); err != nil {
		return err
	}
	state, err := readWorkflowReviewFacts(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	accepted := WorkflowAcceptedReview{ReportRef: event.EventID, ReportDigest: reviewDigest(action.Review), ActorRef: action.ActorRef, Report: *action.Review}
	if state.findingSources == nil {
		state.findingSources = map[string]reviewFindingSource{}
	}
	for ordinal, finding := range action.Review.Findings {
		if _, retained := state.findingSources[finding.FindingID]; !retained {
			source := reviewFindingSource{CanonicalID: fmt.Sprintf("finding:%d:%d", event.Seq, ordinal), DomainID: "unresolved", EventID: event.EventID, Kind: event.Kind, Seq: event.Seq, Ordinal: ordinal}
			state.findingSources[finding.FindingID] = source
		}
	}
	applyWorkflowAcceptedReview(&state, accepted, workflowReviewFindingMap(state))
	if err := writeWorkflowReviewFacts(ctx, tx, event.SubjectID, state); err != nil {
		return err
	}
	return writeWorkContextFacts(ctx, tx, event)
}
