package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

type WorkflowAcceptedReview struct {
	ReportRef    string      `json:"report_ref"`
	ReportDigest string      `json:"report_digest"`
	ActorRef     string      `json:"actor_ref"`
	Report       ReviewBlock `json:"report"`
}

type WorkflowReviewState struct {
	Dependencies           *ReviewDependencies     `json:"dependencies,omitempty"`
	Candidate              *ReviewCandidate        `json:"candidate"`
	Accepted               *WorkflowAcceptedReview `json:"accepted_review"`
	Findings               []ReviewFinding         `json:"findings"`
	DependenciesCurrent    bool                    `json:"dependencies_current"`
	PublicationOutstanding bool                    `json:"publication_outstanding"`
	Crucial                bool                    `json:"-"`
	findingSources         map[string]reviewFindingSource
}

type workflowReviewCandidateView struct {
	ProjectID string `json:"project_id,omitempty"`
	Commit    string `json:"commit"`
	Tree      string `json:"tree,omitempty"`
}

type workflowReviewFindingsView []ReviewFinding

func (findings workflowReviewFindingsView) MarshalJSON() ([]byte, error) {
	var projected []any
	if findings != nil {
		projected = make([]any, len(findings))
	}
	for i, finding := range findings {
		projected[i] = finding
		// Only the wholly unrecorded identity/state tuple admits a content-only
		// view. A partially identified finding must still satisfy the strict shape.
		if finding.FindingID == "" && finding.Status == "" && !finding.InScope && !finding.Blocking && finding.EvidenceRefs == nil && finding.ClosureRefs == nil {
			projected[i] = struct {
				Severity   string               `json:"severity"`
				Confidence string               `json:"confidence"`
				Detail     string               `json:"detail"`
				Oracle     *ReviewOracleFinding `json:"oracle,omitempty"`
			}{finding.Severity, finding.Confidence, finding.Detail, finding.Oracle}
		}
	}
	return json.Marshal(projected)
}

// MarshalJSON exposes only recorded identities and finding fields. Retained
// facts keep tree-less candidates and unidentified findings; storage and
// report admission stay strict.
func (state WorkflowReviewState) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Dependencies           *ReviewDependencies          `json:"dependencies,omitempty"`
		Candidate              *workflowReviewCandidateView `json:"candidate"`
		Accepted               *WorkflowAcceptedReview      `json:"accepted_review"`
		Findings               workflowReviewFindingsView   `json:"findings"`
		DependenciesCurrent    bool                         `json:"dependencies_current"`
		PublicationOutstanding bool                         `json:"publication_outstanding"`
	}{
		Dependencies: state.Dependencies, Candidate: (*workflowReviewCandidateView)(state.Candidate),
		Accepted: state.Accepted, Findings: workflowReviewFindingsView(state.Findings),
		DependenciesCurrent: state.DependenciesCurrent, PublicationOutstanding: state.PublicationOutstanding,
	})
}

type reviewFindingSource struct {
	CanonicalID string            `json:"canonical_id"`
	DomainID    string            `json:"domain_id"`
	EventID     string            `json:"event_id"`
	Kind        string            `json:"kind"`
	Seq         int64             `json:"seq"`
	Ordinal     int               `json:"ordinal"`
	Oracle      *AcceptanceOracle `json:"oracle,omitempty"`
}

func reviewFailure(kind FailureKind, detail, next string) *Failure {
	return newFailure(kind, "workflow_review", detail, false, next)
}

func reviewDigest(value any) string {
	raw, _ := json.Marshal(value)
	canonical, err := canonicalJSON(raw)
	if err != nil {
		return ""
	}
	return nativeDigest(canonical)
}

func workflowCriterionDigest(kind string, payload json.RawMessage) string {
	return reviewDigest(struct {
		Kind    string          `json:"kind"`
		Payload json.RawMessage `json:"payload"`
	}{kind, payload})
}

func workflowReviewDependencies(ctx context.Context, q queryer, work string) (ReviewDependencies, error) {
	result := ReviewDependencies{Criteria: []ReviewCriterion{}}
	version, err := activeWorkflowContractVersion(ctx, q, work, "workflow_review")
	if err != nil {
		return result, err
	}
	rows, err := q.QueryContext(ctx, `SELECT predicate_id,outcome_kind,outcome_payload FROM workflow_contract_predicates WHERE work_id=? AND contract_version=? ORDER BY predicate_id`, work, version)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var id, kind, payload string
		if err := rows.Scan(&id, &kind, &payload); err != nil {
			rows.Close()
			return result, err
		}
		result.Criteria = append(result.Criteria, ReviewCriterion{PredicateID: id, ContentDigest: workflowCriterionDigest(kind, json.RawMessage(payload))})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	design, err := readCurrentWorkflowDesign(ctx, q, work)
	if err != nil {
		return result, err
	}
	pins, err := readWorkflowLawRevisions(ctx, q, work, version)
	if err != nil {
		return result, err
	}
	if design == nil && len(pins) == 0 {
		result.ArchitectureNotApplicable = true
		return result, nil
	}
	if design == nil {
		design = &WorkflowDesignRecord{}
	}
	result.ArchitectureDigest = reviewDigest(struct {
		Approach    string                   `json:"approach"`
		Decisions   []WorkflowDesignDecision `json:"decisions"`
		TouchedRefs []string                 `json:"touched_refs"`
		LawPins     []WorkflowLawRevision    `json:"law_pins,omitempty"`
	}{design.Approach, design.Decisions, design.TouchedRefs, pins})
	return result, nil
}

func sameReviewDependencies(a, b ReviewDependencies) bool {
	if a.ArchitectureDigest != b.ArchitectureDigest || a.ArchitectureNotApplicable != b.ArchitectureNotApplicable || len(a.Criteria) != len(b.Criteria) {
		return false
	}
	ids := map[string]string{}
	for _, criterion := range a.Criteria {
		ids[criterion.PredicateID] = criterion.ContentDigest
	}
	for _, criterion := range b.Criteria {
		if ids[criterion.PredicateID] != criterion.ContentDigest {
			return false
		}
	}
	return true
}

func loadWorkflowReviewState(ctx context.Context, q queryer, work string) (WorkflowReviewState, error) {
	state, err := readWorkflowReviewFacts(ctx, q, work)
	if err != nil {
		return state, err
	}
	var hasContract bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_contracts WHERE work_id=?)`, work).Scan(&hasContract); err != nil {
		return state, err
	}
	if hasContract {
		deps, err := workflowReviewDependencies(ctx, q, work)
		if err != nil {
			return state, err
		}
		if len(deps.Criteria) > 0 {
			state.Dependencies = &deps
			state.DependenciesCurrent = state.Accepted != nil && sameReviewDependencies(state.Accepted.Report.Dependencies, deps)
		}
	}
	current, err := readCurrentOracleSubjectReceipt(ctx, q, work)
	if err != nil {
		return state, err
	}
	if current != nil && state.Candidate != nil && (current.ProjectID != state.Candidate.ProjectID || current.SubjectRef != "commit:"+state.Candidate.Commit) {
		state.DependenciesCurrent = false
	}
	size, sized, err := readEffectiveWorkSize(ctx, q, work)
	if err != nil {
		return state, err
	}
	state.Crucial = sized && size.Effective == WorkSizeCrucial
	return state, nil
}

func (state WorkflowReviewState) blocker() string {
	for _, finding := range state.Findings {
		if finding.Status == "open" && finding.InScope && finding.Blocking {
			return finding.FindingID
		}
	}
	return ""
}

var reviewHighOrMediumSeverities = map[string]bool{"P0": true, "P1": true, "P2": true}

func (state WorkflowReviewState) crucialExitBlocker() (string, string) {
	if state.Crucial {
		for _, finding := range state.Findings {
			if finding.Status == "open" && finding.InScope && reviewHighOrMediumSeverities[finding.Severity] {
				return finding.FindingID, finding.Severity
			}
		}
	}
	return "", ""
}

func workflowAdmitReview(state WorkflowReviewState) error {
	if state.Accepted == nil || state.Candidate == nil {
		return reviewFailure(KindMissingEvidence, "current candidate has no recorded Review", "obtain independent Review and record its subject-bound report")
	}
	if !state.DependenciesCurrent || state.Accepted.Report.OutputCandidate != *state.Candidate {
		return reviewFailure(KindStaleRequiresReview, "recorded Review does not cover the current candidate and dependencies", "review the exact current candidate and dependencies")
	}
	if finding := state.blocker(); finding != "" {
		return reviewFailure(KindMissingEvidence, "open in-scope blocking finding "+finding, "resolve the finding with output-subject proof")
	}
	if finding, severity := state.crucialExitBlocker(); finding != "" {
		return reviewFailure(KindMissingEvidence, "Crucial work retains open "+severity+" finding "+finding, "resolve the finding with output-subject proof")
	}
	if state.Accepted.Report.Disposition != "ready_for_final_proof" || len(state.Accepted.Report.RemainingObligations) != 0 {
		return reviewFailure(KindMissingEvidence, "Review has not reached ready_for_final_proof without remaining obligations", "finish the recorded obligations and record the independent Review outcome")
	}
	return nil
}

func decodeRecordedReview(payload json.RawMessage) (*ReviewBlock, error) {
	fields, err := workflowActionObject(payload)
	if err != nil {
		return nil, err
	}
	var report ReviewBlock
	decoder := json.NewDecoder(strings.NewReader(string(fields["review"])))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return nil, reviewFailure(KindInvalidPayload, "record_review requires one typed review report", "supply the complete Review block")
	}
	if err := validateReviewBlock(&report); err != nil {
		return nil, err
	}
	return &report, nil
}

func validateRecordedReview(ctx context.Context, q queryer, work string, report *ReviewBlock) error {
	if err := validateReviewBlock(report); err != nil {
		return err
	}
	current, err := readCurrentOracleSubjectReceipt(ctx, q, work)
	if err != nil {
		return err
	}
	state, err := loadWorkflowReviewState(ctx, q, work)
	if err != nil {
		return err
	}
	if err := validateReviewCandidates(report, current, workflowSubjectPreparationFrom(ctx), state.Candidate); err != nil {
		return err
	}
	deps, err := workflowReviewDependencies(ctx, q, work)
	if err != nil {
		return err
	}
	if !sameReviewDependencies(report.Dependencies, deps) {
		return reviewFailure(KindStaleRequiresReview, "Review judged different criterion or architecture content", "review the current criterion and architecture digests")
	}
	if err := validateReviewFindingChanges(report, state); err != nil {
		return err
	}
	if report.Disposition == "ready_for_final_proof" {
		state.Dependencies = &deps
		applyWorkflowAcceptedReview(&state, WorkflowAcceptedReview{Report: *report}, workflowReviewFindingMap(state))
		return workflowAdmitReview(state)
	}
	return nil
}

func bindWorkflowReviewRequest(ctx context.Context, q queryer, state *WorkflowAdmissionState, work, action string, payload json.RawMessage, actor WorkflowActor) error {
	if action != "record_review" {
		return nil
	}
	report, err := decodeRecordedReview(payload)
	if err != nil {
		return err
	}
	return validateRecordedReview(ctx, q, work, report)
}

func validateReviewCandidates(report *ReviewBlock, current *WorktreeVerifyResult, prepared *WorkflowSubjectPreparation, retained *ReviewCandidate) error {
	if report.InputCandidate.ProjectID != report.OutputCandidate.ProjectID {
		return reviewFailure(KindUnauthorized, "Review input and output name different Projects", "record one Project's exact candidate")
	}
	if current == nil || current.ProjectID != report.OutputCandidate.ProjectID || current.SubjectRef != "commit:"+report.OutputCandidate.Commit {
		return reviewFailure(KindStaleRequiresReview, "Review output is not the current qualified clean candidate", "qualify the exact candidate and record its Review")
	}
	if prepared == nil || prepared.candidate == nil || *prepared.candidate != report.OutputCandidate {
		return reviewFailure(KindStaleRequiresReview, "Review output does not name the observed Project, commit and tree", "review the exact qualified clean candidate")
	}
	if report.InputCandidate.Commit == report.OutputCandidate.Commit && report.InputCandidate.Tree != report.OutputCandidate.Tree {
		return reviewFailure(KindOutcomeMismatch, "unchanged Review commit names different trees", "retain the exact candidate tree")
	}
	if report.InputCandidate != report.OutputCandidate && (retained == nil || *retained != report.InputCandidate) {
		return reviewFailure(KindStaleRequiresReview, "corrective Review input is not the retained candidate", "record the exact retained input and qualified output")
	}
	return nil
}

// Finding closure records coordinator judgment; it does not create native
// execution evidence or discharge a criterion verdict.
func validateReviewFindingChanges(report *ReviewBlock, state WorkflowReviewState) error {
	previous := workflowReviewFindingMap(state)
	for _, finding := range report.Findings {
		old, retained := previous[finding.FindingID]
		if finding.Status == "closed" && !retained {
			return reviewFailure(KindMissingEvidence, "closed finding has no retained original finding", "retain and resolve the existing finding identity")
		}
		if finding.Oracle != nil && (!retained || !oracleEntryIdentical(finding.Oracle, old.Oracle)) {
			return reviewFailure(KindInvalidPayload, "Review cannot author or replace historical oracle metadata", "record an ordinary finding and retain its original identity")
		}
	}
	for _, resolution := range report.ResolvedFindings {
		local, found := reviewFindingLocalID(state, resolution.FindingID)
		if !found || previous[local].Status != "open" {
			return reviewFailure(KindInvalidPayload, "resolved_findings names no retained open canonical finding", "close an existing open finding exactly once")
		}
		for _, finding := range report.Findings {
			if finding.FindingID == local && finding.Status != "closed" {
				return reviewFailure(KindInvalidPayload, "resolved_findings contradicts an open continuation", "record one consistent finding disposition")
			}
		}
	}
	return nil
}

func reviewFindingLocalID(state WorkflowReviewState, canonical string) (string, bool) {
	for local, source := range state.findingSources {
		if source.CanonicalID == canonical {
			return local, true
		}
	}
	return "", false
}

func workflowCriterionReceipt(ctx context.Context, q queryer, work, predicate, ref, subject string) error {
	research, err := workflowResearchCriterionFamily(ctx, q, work)
	if err != nil {
		return err
	}
	if research {
		return workflowResearchCriterionReceipt(ctx, q, work, predicate, ref)
	}
	producer, err := readOracleVerifySubjectReceipt(ctx, q, work, ref)
	if err != nil {
		return err
	}
	if producer == nil || producer.ExitCode != 0 || producer.SubjectRef != "commit:"+subject {
		return reviewFailure(KindMissingEvidence, "criterion "+predicate+" requires a passing final-subject receipt", "execute the criterion and cite its captured result")
	}
	if producer.Oracle == nil {
		return workflowReviewedCommandReceipt(ctx, q, work, predicate, ref, subject, producer.ProjectID)
	}
	if producer.Oracle.Phase != "execute" {
		return reviewFailure(KindMissingEvidence, "preparation is readiness, not criterion proof", "execute the exact preparation and cite its result")
	}
	plan, _, err := readNativeOracleExecutePlanTx(ctx, q, work, producer)
	if err != nil {
		return err
	}
	control := plan.Bundle.Control
	if control.ControlID == producer.Oracle.ControlID && slices.Contains(control.PredicateIDs, predicate) && producer.Oracle.BundleDigest == nativeBundleDigest(plan.Bundle) && producer.Oracle.RecipeSource == control.RecipeSource && slices.Equal(producer.Command, control.Argv) {
		return validateWorkflowReceiptCriterion(ctx, q, work, predicate, producer)
	}
	return reviewFailure(KindMissingEvidence, fmt.Sprintf("receipt %s has no declared coverage of criterion %s", ref, predicate), "execute a declared control covering that criterion")
}

func sameReviewFindingIDs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	seen := map[string]bool{}
	for _, value := range left {
		seen[value] = true
	}
	for _, value := range right {
		if !seen[value] {
			return false
		}
		delete(seen, value)
	}
	return len(seen) == 0
}

const WorkflowOpenOracleFindingsLimit = 32

func validateCorrectionOpenFindingsTx(ctx context.Context, q queryer, workID string, supplied []string, present bool) error {
	if !present {
		return nil
	}
	state, err := readWorkflowReviewFacts(ctx, q, workID)
	if err != nil {
		return err
	}
	derived := []string{}
	for _, finding := range state.Findings {
		if finding.Status != "open" || finding.Oracle == nil || finding.Oracle.Classification == OracleClassificationFollowUp {
			continue
		}
		source, found := state.findingSources[finding.FindingID]
		if !found || !reviewFindingIDPattern.MatchString(source.CanonicalID) {
			return reviewFailure(KindInvariantViolation, "open Review finding has no retained canonical identity", "restore the verified finding source")
		}
		derived = append(derived, source.CanonicalID)
	}
	if len(derived) > WorkflowOpenOracleFindingsLimit {
		return reviewFailure(KindLimitExceeded, "open findings exceed the correction payload bound", "resolve findings or revise the approved scope; the set cannot be truncated")
	}
	if len(derived) == 0 {
		return reviewFailure(KindInvalidPayload, "open_finding_ids names findings while the derived open set is empty", "omit open_finding_ids")
	}
	if !sameReviewFindingIDs(derived, supplied) {
		return reviewFailure(KindInvalidPayload, "open_finding_ids differs from the derived open finding set", "record every current open canonical finding exactly once")
	}
	return nil
}

func workflowResearchCriterionReceipt(ctx context.Context, q queryer, work, predicate, ref string) error {
	view, err := readWorkContextView(ctx, q, work)
	if err != nil {
		return err
	}
	if view != nil {
		for _, entry := range view.ResearchFindings {
			if entry.ViewID == ref && entry.State == WorkContextResearchStateResolved && entry.Finding != nil && entry.Sources != nil && len(*entry.Sources) != 0 {
				return nil
			}
		}
	}
	return reviewFailure(KindMissingEvidence, "criterion "+predicate+" cites no evidenced research finding", "select the exact pack, revision and finding through record_work_context")
}

func workflowReviewedCommandReceipt(ctx context.Context, q queryer, work, predicate, ref, subject, project string) error {
	state, err := loadWorkflowReviewState(ctx, q, work)
	if err != nil {
		return err
	}
	if state.Candidate != nil && state.Accepted != nil && state.DependenciesCurrent && state.Accepted.Report.OutputCandidate == *state.Candidate && state.Candidate.Commit == subject && state.Candidate.ProjectID == project {
		for _, assessment := range state.Accepted.Report.Assessments {
			if assessment.PredicateID == predicate && assessment.Assessment == "supported" && slices.Contains(assessment.ReceiptRefs, ref) {
				return nil
			}
		}
	}
	return reviewFailure(KindMissingEvidence, "command receipt "+ref+" has no current Review assessment of criterion "+predicate, "record a Review assessing that criterion and captured receipt on the final candidate")
}

func validateWorkflowReceiptCriterion(ctx context.Context, q queryer, work, predicate string, producer *WorktreeVerifyResult) error {
	var oldKind, currentKind, oldPayload, currentPayload string
	if err := q.QueryRowContext(ctx, `SELECT outcome_kind,outcome_payload FROM workflow_contract_predicates WHERE work_id=? AND contract_version=? AND predicate_id=?`, work, producer.Oracle.ContractVersion, predicate).Scan(&oldKind, &oldPayload); err != nil {
		return reviewFailure(KindMissingEvidence, "receipt has no captured criterion coverage", "execute the criterion's declared control")
	}
	version, err := activeWorkflowContractVersion(ctx, q, work, "workflow_review")
	if err != nil {
		return err
	}
	if err := q.QueryRowContext(ctx, `SELECT outcome_kind,outcome_payload FROM workflow_contract_predicates WHERE work_id=? AND contract_version=? AND predicate_id=?`, work, version, predicate).Scan(&currentKind, &currentPayload); err != nil {
		return err
	}
	if workflowCriterionDigest(oldKind, json.RawMessage(oldPayload)) != workflowCriterionDigest(currentKind, json.RawMessage(currentPayload)) {
		return reviewFailure(KindStaleRequiresReview, "receipt covers different criterion content", "capture proof for the changed criterion")
	}
	return validateNativeOracleProducerTx(ctx, q, work, producer, "pass")
}

func workflowResearchCriterionFamily(ctx context.Context, q queryer, work string) (bool, error) {
	var research bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_instances WHERE work_id=? AND definition_ref='workflow.research')`, work).Scan(&research)
	return research, err
}

func verifyWorkflowCriterionAssociation(ctx context.Context, q queryer, work string, association workflowVerdictRecordedPayload) error {
	state, err := loadWorkflowReviewState(ctx, q, work)
	if err != nil {
		return err
	}
	research, err := workflowResearchCriterionFamily(ctx, q, work)
	if err != nil {
		return err
	}
	if !research && (association.Candidate == nil || state.Candidate == nil || *association.Candidate != *state.Candidate) || association.VerdictKind != "ok" || association.IncomparableWithApproved || len(association.EvaluationEvidence) == 0 {
		return reviewFailure(KindMissingEvidence, "criterion proof does not identify the current integrated candidate", "associate passing receipts for the current candidate")
	}
	deps, err := workflowReviewDependencies(ctx, q, work)
	if err != nil {
		return err
	}
	for _, criterion := range deps.Criteria {
		if criterion.PredicateID != association.PredicateID {
			continue
		}
		if criterion.ContentDigest != association.CriterionDigest {
			return reviewFailure(KindStaleRequiresReview, "criterion changed after proof association", "associate proof for the changed criterion")
		}
		for _, ref := range association.EvaluationEvidence {
			subject := ""
			if association.Candidate != nil {
				subject = association.Candidate.Commit
			}
			if err := workflowCriterionReceipt(ctx, q, work, criterion.PredicateID, ref, subject); err != nil {
				return err
			}
		}
		return nil
	}
	return reviewFailure(KindOutcomeMismatch, "proof association names an unapproved criterion", "associate an approved criterion")
}

func workflowMissingCriterionAssociations(ctx context.Context, q queryer, work string) (map[string]bool, error) {
	version, err := activeWorkflowContractVersion(ctx, q, work, "workflow_review")
	if err != nil {
		return nil, err
	}
	criteria, err := readWorkflowContractPredicates(ctx, q, work, version)
	if err != nil {
		return nil, err
	}
	missing := map[string]bool{}
	for _, criterion := range criteria {
		missing[criterion.PredicateID] = true
	}
	associations, err := latestWorkflowVerdicts(ctx, q, work, version)
	if err != nil {
		return nil, err
	}
	for _, association := range associations {
		if err := verifyWorkflowCriterionAssociation(ctx, q, work, association); err != nil {
			var failure *Failure
			if !failureAs(err, &failure) {
				return nil, err
			}
			switch failure.Kind {
			case KindMissingEvidence, KindStaleRequiresReview, KindOutcomeMismatch, KindUnauthorized:
				continue
			default:
				return nil, err
			}
		}
		delete(missing, association.PredicateID)
	}
	return missing, nil
}

func guardLateCriterionAssociations(ctx context.Context, q queryer, work string, definition WorkflowDefinition, step string, fields map[string]json.RawMessage) error {
	if !workflowLateCriterionAssociationStep(definition, step) {
		return nil
	}
	entries, err := normalizeWorkflowVerdictEntries(fields)
	if err != nil {
		return err
	}
	missing, err := workflowMissingCriterionAssociations(ctx, q, work)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !missing[entry.PredicateID] {
			return newFailure(KindIllegalLifecycleTransition, "workflow_action", "late criterion association requires missing applicable proof for each entry", false, "associate only approved criteria without passing proof")
		}
	}
	return nil
}
