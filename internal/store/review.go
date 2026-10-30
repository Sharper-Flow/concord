package store

import (
	"encoding/json"
	"regexp"
)

const (
	OracleClassificationDeliveryBlocker = "delivery_blocker"
	OracleClassificationUncoveredCase   = "uncovered_case"
	OracleClassificationFollowUp        = "follow_up"
	OracleClassificationOracleDefect    = "oracle_defect"
)

type ReviewCandidate struct {
	ProjectID string `json:"project_id"`
	Commit    string `json:"commit"`
	Tree      string `json:"tree"`
}

type ReviewCriterion struct {
	PredicateID   string `json:"predicate_id"`
	ContentDigest string `json:"content_digest"`
}

type ReviewDependencies struct {
	Criteria                  []ReviewCriterion `json:"criteria"`
	ArchitectureDigest        string            `json:"architecture_digest,omitempty"`
	ArchitectureNotApplicable bool              `json:"architecture_not_applicable"`
}

type ReviewAssessment struct {
	PredicateID string   `json:"predicate_id"`
	Assessment  string   `json:"assessment"`
	ReceiptRefs []string `json:"receipt_refs"`
}

type ReviewObligation struct {
	ObligationID string `json:"obligation_id"`
	OwnerRef     string `json:"owner_ref"`
	SubjectRef   string `json:"subject_ref"`
	InputDigest  string `json:"input_digest"`
	NeededProof  string `json:"needed_proof"`
}

type ReviewFinding struct {
	FindingID    string               `json:"finding_id"`
	Severity     string               `json:"severity"`
	Confidence   string               `json:"confidence"`
	Detail       string               `json:"detail"`
	InScope      bool                 `json:"in_scope"`
	Blocking     bool                 `json:"blocking"`
	Status       string               `json:"status"`
	EvidenceRefs []string             `json:"evidence_refs"`
	ClosureRefs  []string             `json:"closure_refs"`
	Oracle       *ReviewOracleFinding `json:"oracle,omitempty"`
}

type ReviewOracleFinding struct {
	Classification     string                   `json:"classification"`
	OwnerID            string                   `json:"owner_id,omitempty"`
	FailureFamily      string                   `json:"failure_family,omitempty"`
	PredicateIDs       []string                 `json:"predicate_ids,omitempty"`
	LawBindings        []ReviewOracleLawBinding `json:"law_bindings,omitempty"`
	CaseIDs            []string                 `json:"case_ids,omitempty"`
	ControlIDs         []string                 `json:"control_ids,omitempty"`
	EvidenceRefs       []string                 `json:"evidence_refs,omitempty"`
	EntryPoint         string                   `json:"entry_point,omitempty"`
	ContinuesFindingID string                   `json:"continues_finding_id,omitempty"`
	VariantOf          string                   `json:"variant_of,omitempty"`
}

type ReviewOracleLawSource struct {
	Kind        string `json:"kind"`
	SourceID    string `json:"source_id"`
	LawID       string `json:"law_id"`
	ContentHash string `json:"content_hash"`
}

type ReviewOracleLawBinding struct {
	Source ReviewOracleLawSource `json:"source"`
	Clause string                `json:"clause"`
}

type ReviewResolution struct {
	FindingID     string   `json:"finding_id"`
	EvidenceRefs  []string `json:"evidence_refs,omitempty"`
	JudgedCaseIDs []string `json:"judged_case_ids,omitempty"`
}

// ReviewBlock records the coordinator's claim, not subagent execution evidence.
type ReviewBlock struct {
	Disposition          string             `json:"disposition"`
	InputCandidate       ReviewCandidate    `json:"input_candidate"`
	OutputCandidate      ReviewCandidate    `json:"output_candidate"`
	Dependencies         ReviewDependencies `json:"dependencies"`
	Assessments          []ReviewAssessment `json:"assessments"`
	Findings             []ReviewFinding    `json:"findings"`
	RemainingObligations []ReviewObligation `json:"remaining_obligations"`
	ResolvedFindings     []ReviewResolution `json:"resolved_findings,omitempty"`
}

var reviewPredicatePattern = regexp.MustCompile(`^predicate:[A-Za-z0-9][A-Za-z0-9._:-]{0,117}$`)
var reviewFindingIDPattern = regexp.MustCompile(`^finding:[0-9]+:[0-9]+$`)

func validateReviewBlock(review *ReviewBlock) error {
	bad := func(detail string) error {
		return reviewFailure(KindInvalidPayload, detail, "record a bounded Review with exact candidate and dependency identities")
	}
	if review == nil {
		return bad("record_review requires a review report")
	}
	switch review.Disposition {
	case "continue", "correction_required", "revision_required", "ready_for_final_proof":
	default:
		return bad("Review disposition is unknown")
	}
	if review.Findings == nil || len(review.Findings) > 64 || review.Assessments == nil || len(review.Assessments) > 8 || review.RemainingObligations == nil || len(review.RemainingObligations) > 32 {
		return bad("Review requires bounded findings, assessments and remaining_obligations arrays")
	}
	for _, candidate := range []ReviewCandidate{review.InputCandidate, review.OutputCandidate} {
		if !ValidReference(candidate.ProjectID) || !worktreeSHAPattern.MatchString(candidate.Commit) || !worktreeSHAPattern.MatchString(candidate.Tree) {
			return bad("Review requires exact Project, commit and tree identities")
		}
	}
	if err := validateReviewAssessments(review.Dependencies, review.Assessments); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, finding := range review.Findings {
		if err := validateReviewFinding(finding); err != nil {
			return err
		}
		if seen[finding.FindingID] {
			return bad("Review repeats a finding identity")
		}
		seen[finding.FindingID] = true
	}
	seen = map[string]bool{}
	for _, obligation := range review.RemainingObligations {
		if !ValidReference(obligation.ObligationID) || seen[obligation.ObligationID] || !ValidReference(obligation.OwnerRef) || !ValidReference(obligation.SubjectRef) || !validDigest(obligation.InputDigest) || len(obligation.NeededProof) < 1 || len(obligation.NeededProof) > 512 {
			return bad("Review remaining obligations require unique identity, owner, subject, input digest and bounded needed proof")
		}
		seen[obligation.ObligationID] = true
	}
	return validateReviewResolutions(review.ResolvedFindings)
}

func validateReviewAssessments(deps ReviewDependencies, assessments []ReviewAssessment) error {
	bad := func(detail string) error {
		return reviewFailure(KindInvalidPayload, detail, "record a bounded Review with exact candidate and dependency identities")
	}
	if len(deps.Criteria) < 1 || len(deps.Criteria) > 8 || deps.ArchitectureNotApplicable == (deps.ArchitectureDigest != "") || deps.ArchitectureDigest != "" && !validDigest(deps.ArchitectureDigest) {
		return bad("Review requires criteria and exactly one architecture digest or not-applicable status")
	}
	criteria := map[string]bool{}
	for _, criterion := range deps.Criteria {
		if !reviewPredicatePattern.MatchString(criterion.PredicateID) || !validDigest(criterion.ContentDigest) || criteria[criterion.PredicateID] {
			return bad("Review criteria require unique identities and exact content digests")
		}
		criteria[criterion.PredicateID] = true
	}
	seen := map[string]bool{}
	for _, assessment := range assessments {
		if !criteria[assessment.PredicateID] || seen[assessment.PredicateID] || assessment.Assessment != "supported" && assessment.Assessment != "unproved" && assessment.Assessment != "mismatch" || assessment.ReceiptRefs == nil || len(assessment.ReceiptRefs) > 32 {
			return bad("Review assessments must uniquely name judged criteria and bounded receipt references")
		}
		seen[assessment.PredicateID] = true
		for _, ref := range assessment.ReceiptRefs {
			if !ValidReference(ref) {
				return bad("Review receipt reference is invalid")
			}
		}
	}
	return nil
}

func validateReviewFinding(finding ReviewFinding) error {
	bad := func(detail string) error {
		return reviewFailure(KindInvalidPayload, detail, "record the finding's exact identity, classification and evidence")
	}
	switch finding.Severity {
	case "P0", "P1", "P2", "P3":
	default:
		return bad("Review finding severity must be P0, P1, P2 or P3")
	}
	switch finding.Confidence {
	case "low", "medium", "high":
	default:
		return bad("Review finding confidence must be low, medium or high")
	}
	if len(finding.Detail) < 1 || len(finding.Detail) > 512 || !ValidReference(finding.FindingID) || (finding.Status != "open" && finding.Status != "closed") || finding.EvidenceRefs == nil || finding.ClosureRefs == nil || len(finding.EvidenceRefs) > 32 || len(finding.ClosureRefs) > 32 || finding.Status == "closed" && len(finding.ClosureRefs) == 0 {
		return bad("Review finding requires bounded detail, identity, status and evidence arrays")
	}
	for _, ref := range append(append([]string{}, finding.EvidenceRefs...), finding.ClosureRefs...) {
		if !ValidReference(ref) {
			return bad("Review finding evidence reference is invalid")
		}
	}
	return validateReviewOracleFinding(finding.Oracle)
}

func validateReviewOracleFinding(finding *ReviewOracleFinding) error {
	if finding == nil {
		return nil
	}
	bad := func(detail string) error {
		return reviewFailure(KindInvalidPayload, detail, "retain the finding's bounded classification, source and evidence identities")
	}
	switch finding.Classification {
	case OracleClassificationDeliveryBlocker, OracleClassificationUncoveredCase, OracleClassificationFollowUp, OracleClassificationOracleDefect:
	default:
		return bad("Review finding oracle classification is unknown")
	}
	if finding.OwnerID != "" && !oracleOwnerIDPattern.MatchString(finding.OwnerID) || len(finding.FailureFamily) > 128 || len(finding.EntryPoint) > 512 {
		return bad("Review finding oracle owner, failure family or entry point is invalid")
	}
	for _, group := range []struct {
		values  []string
		pattern *regexp.Regexp
	}{{finding.PredicateIDs, reviewPredicatePattern}, {finding.CaseIDs, oracleCaseIDPattern}, {finding.ControlIDs, oracleControlIDPattern}} {
		if len(group.values) > 8 {
			return bad("Review finding oracle references exceed their eight-member bound")
		}
		seen := map[string]bool{}
		for _, value := range group.values {
			if !group.pattern.MatchString(value) || seen[value] {
				return bad("Review finding oracle references must use unique bounded identities")
			}
			seen[value] = true
		}
	}
	if len(finding.LawBindings) > 8 {
		return bad("Review finding oracle has too many law bindings")
	}
	laws := map[string]bool{}
	for _, binding := range finding.LawBindings {
		source := binding.Source
		if source.Kind != WorkContextSourceKnowledge || len(source.SourceID) < 2 || len(source.SourceID) > oracleProjectIDMaxBytes || len(source.LawID) < 2 || len(source.LawID) > workContextLawIDMaxBytes || !workflowDigest(source.ContentHash) || len(binding.Clause) < 1 || len(binding.Clause) > oracleDescriptionMaxBytes || laws[source.LawID] {
			return bad("Review finding oracle requires unique pinned law revisions and bounded clauses")
		}
		laws[source.LawID] = true
	}
	if len(finding.EvidenceRefs) > 8 {
		return bad("Review finding oracle has too many evidence references")
	}
	for _, ref := range finding.EvidenceRefs {
		if len(ref) < 1 || len(ref) > 256 {
			return bad("Review finding oracle evidence references must contain one to 256 bytes")
		}
	}
	for _, ref := range []string{finding.ContinuesFindingID, finding.VariantOf} {
		if ref != "" && !reviewFindingIDPattern.MatchString(ref) {
			return bad("Review finding lineage must name finding:<event_seq>:<ordinal>")
		}
	}
	return nil
}

func validateReviewResolutions(resolutions []ReviewResolution) error {
	bad := func(detail string) error {
		return reviewFailure(KindInvalidPayload, detail, "record bounded, unique finding closures with evidence or judged case identities")
	}
	if len(resolutions) > 32 {
		return bad("Review has too many finding resolutions")
	}
	seen := map[string]bool{}
	for _, resolution := range resolutions {
		if !reviewFindingIDPattern.MatchString(resolution.FindingID) || seen[resolution.FindingID] || len(resolution.EvidenceRefs) > 8 || resolution.EvidenceRefs != nil && len(resolution.EvidenceRefs) == 0 || len(resolution.EvidenceRefs) == 0 && len(resolution.JudgedCaseIDs) == 0 || len(resolution.JudgedCaseIDs) > 8 || resolution.JudgedCaseIDs != nil && len(resolution.JudgedCaseIDs) == 0 {
			return bad("Review finding resolution identity or evidence/case bounds are invalid")
		}
		seen[resolution.FindingID] = true
		refs := map[string]bool{}
		for _, ref := range resolution.EvidenceRefs {
			if len(ref) < 1 || len(ref) > 256 || refs[ref] {
				return bad("Review resolution evidence references must be unique and contain one to 256 bytes")
			}
			refs[ref] = true
		}
		cases := map[string]bool{}
		for _, id := range resolution.JudgedCaseIDs {
			if len(id) > 128 || !oracleCaseIDPattern.MatchString(id) || cases[id] {
				return bad("Review resolution judged cases must use unique bounded case identities")
			}
			cases[id] = true
		}
	}
	if resolutions != nil {
		raw, err := json.Marshal(resolutions)
		if err != nil {
			return bad("Review finding resolutions cannot be measured")
		}
		if len(raw) > 8192 {
			return reviewFailure(KindLimitExceeded, "Review finding resolutions exceed their 8192-byte aggregate bound", "split the closures across reports")
		}
	}
	return nil
}
