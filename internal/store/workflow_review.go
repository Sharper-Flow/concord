package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
)

// WorkflowAcceptedReview identifies the exact terminal report Main accepted.
// The output candidate changes only when that acceptance commits.
type WorkflowAcceptedReview struct {
	AttemptID    string            `json:"attempt_id"`
	ReportRef    string            `json:"report_ref"`
	ReportDigest string            `json:"report_digest"`
	ActorRef     string            `json:"actor_ref"`
	Report       WorkerReviewBlock `json:"report"`
}

// WorkflowReviewState is the current execution view consumed by admission and
// exposed on resumption. Findings remain open when a later report omits them.
type WorkflowReviewState struct {
	Groups    map[string]workflowAssignmentGroup `json:"-"`
	StaleJobs map[string]bool                    `json:"-"`
	// ExecutionProgressed comes from current assignments and implementation
	// attempts, not the presence of Review or a session's lifetime.
	ExecutionProgressed    bool                      `json:"-"`
	Dependencies           *WorkerReviewDependencies `json:"dependencies,omitempty"`
	Candidate              *WorkerReviewCandidate    `json:"candidate"`
	Accepted               *WorkflowAcceptedReview   `json:"accepted_review"`
	Findings               []WorkerReviewFinding     `json:"findings"`
	ContinueConsumed       bool                      `json:"continue_consumed"`
	GroupID                string                    `json:"group_id,omitempty"`
	DependenciesCurrent    bool                      `json:"dependencies_current"`
	PublicationOutstanding bool                      `json:"publication_outstanding"`
}

type workflowReviewCandidateView struct {
	ProjectID string `json:"project_id,omitempty"`
	Commit    string `json:"commit"`
	Tree      string `json:"tree,omitempty"`
}

// MarshalJSON exposes only recorded candidate identities. It leaves stored
// review facts and the strict worker-report candidate representation unchanged.
func (state WorkflowReviewState) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Dependencies           *WorkerReviewDependencies    `json:"dependencies,omitempty"`
		Candidate              *workflowReviewCandidateView `json:"candidate"`
		Accepted               *WorkflowAcceptedReview      `json:"accepted_review"`
		Findings               []WorkerReviewFinding        `json:"findings"`
		ContinueConsumed       bool                         `json:"continue_consumed"`
		GroupID                string                       `json:"group_id,omitempty"`
		DependenciesCurrent    bool                         `json:"dependencies_current"`
		PublicationOutstanding bool                         `json:"publication_outstanding"`
	}{
		Dependencies: state.Dependencies, Candidate: (*workflowReviewCandidateView)(state.Candidate),
		Accepted: state.Accepted, Findings: state.Findings, ContinueConsumed: state.ContinueConsumed,
		GroupID: state.GroupID, DependenciesCurrent: state.DependenciesCurrent,
		PublicationOutstanding: state.PublicationOutstanding,
	})
}

func reviewFailure(kind FailureKind, detail, next string) *Failure {
	return newFailure(kind, "workflow_review", detail, false, next)
}

func workflowRequireMain(ctx context.Context, q queryer, workID, actor string) error {
	worker, err := workflowRecordedEvaluationFacts(ctx, q, workID, actor, int64(1)<<62)
	if err != nil {
		return err
	}
	if worker {
		return reviewFailure(KindUnauthorized, "a worker cannot accept results, associate criterion proof, or complete work", "return the attributed report to Main")
	}
	return requireNonMemberMain(ctx, q, actor)
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

func workflowReviewDependencies(ctx context.Context, q queryer, workID string) (WorkerReviewDependencies, error) {
	result := WorkerReviewDependencies{Criteria: []WorkerReviewCriterion{}}
	version, err := activeWorkflowContractVersion(ctx, q, workID, "workflow_review")
	if err != nil {
		return result, err
	}
	rows, err := q.QueryContext(ctx, `SELECT predicate_id,outcome_kind,outcome_payload FROM workflow_contract_predicates WHERE work_id=? AND contract_version=? ORDER BY predicate_id`, workID, version)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var id, kind string
		var payload string
		if err := rows.Scan(&id, &kind, &payload); err != nil {
			rows.Close()
			return result, err
		}
		result.Criteria = append(result.Criteria, WorkerReviewCriterion{PredicateID: id, ContentDigest: workflowCriterionDigest(kind, json.RawMessage(payload))})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	design, err := readCurrentWorkflowDesign(ctx, q, workID)
	if err != nil {
		return result, err
	}
	// The architecture basis is the design plus the law the active contract
	// pins (CD-0242 D10), so a changed pin stales an accepted Review.
	pins, err := readWorkflowLawRevisions(ctx, q, workID, version)
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

func sameReviewDependencies(a, b WorkerReviewDependencies) bool {
	if a.ArchitectureDigest != b.ArchitectureDigest || a.ArchitectureNotApplicable != b.ArchitectureNotApplicable || len(a.Criteria) != len(b.Criteria) {
		return false
	}
	byID := map[string]string{}
	for _, criterion := range a.Criteria {
		byID[criterion.PredicateID] = criterion.ContentDigest
	}
	for _, criterion := range b.Criteria {
		if byID[criterion.PredicateID] != criterion.ContentDigest {
			return false
		}
	}
	return true
}

// Admission runs before the current effect updates Review facts.
func loadWorkflowReviewState(ctx context.Context, q queryer, workID string) (WorkflowReviewState, error) {
	state, err := readWorkflowReviewFacts(ctx, q, workID)
	if err != nil {
		return state, err
	}
	var hasContract bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_contracts WHERE work_id=?)`, workID).Scan(&hasContract); err != nil {
		return state, err
	}
	if hasContract {
		dependencies, err := workflowReviewDependencies(ctx, q, workID)
		if err != nil {
			return state, err
		}
		// A contract without approved criteria has no reviewable dependency
		// descriptor yet. Keep the closed report shape absent, not malformed.
		if len(dependencies.Criteria) != 0 {
			state.Dependencies = &dependencies
			if state.Accepted != nil {
				state.DependenciesCurrent = sameReviewDependencies(state.Accepted.Report.Dependencies, dependencies)
			}
		}
	}
	if err := loadWorkflowAssignmentGroups(ctx, q, workID, &state); err != nil {
		return state, err
	}
	if err := loadWorkflowReviewJobChanges(ctx, q, workID, &state); err != nil {
		return state, err
	}
	return state, nil
}

func applyWorkflowAcceptedReview(state *WorkflowReviewState, accepted WorkflowAcceptedReview, findings map[string]WorkerReviewFinding) {
	state.Accepted = &accepted
	state.ContinueConsumed, state.GroupID = false, ""
	state.DependenciesCurrent = state.Dependencies != nil && sameReviewDependencies(accepted.Report.Dependencies, *state.Dependencies)
	for _, finding := range accepted.Report.Findings {
		if finding.Status == "closed" {
			previous := findings[finding.FindingID]
			previous.Status, previous.ClosureRefs = "closed", finding.ClosureRefs
			findings[finding.FindingID] = previous
		}
	}
}

// Combined delivery judges the same accepted Review, candidate and finding
// closures the fold will record, without changing the pre-acceptance view.
// The caller must first admit the exact result through validateReviewAcceptance.
func projectWorkflowReviewAcceptance(ctx context.Context, q queryer, workID string, state WorkflowReviewState, auth workflowActionCompletedPayload, result WorkerCompletedPayload, actor string) (WorkflowReviewState, error) {
	candidate, err := workflowAcceptedResultCandidate(ctx, q, workID, auth, result)
	if err != nil {
		return state, err
	}
	if candidate != nil {
		state.Candidate = candidate
	}
	if auth.WorkerCapabilityClass != "review" || result.Review == nil {
		return state, nil
	}
	var reportRef string
	if err := q.QueryRowContext(ctx, `SELECT terminal_event_id FROM worker_attempts WHERE work_id=? AND attempt_id=? AND terminal_kind=?`, workID, result.AttemptID, WorkerCompleted).Scan(&reportRef); err != nil {
		return state, err
	}
	findings := make(map[string]WorkerReviewFinding, len(state.Findings))
	for _, finding := range state.Findings {
		findings[finding.FindingID] = finding
	}
	applyWorkflowAcceptedReview(&state, WorkflowAcceptedReview{AttemptID: result.AttemptID, ReportRef: reportRef, ReportDigest: reviewDigest(result.Review), ActorRef: actor, Report: *result.Review}, findings)
	if err := loadWorkflowReviewJobChanges(ctx, q, workID, &state); err != nil {
		return state, err
	}
	state.Findings = slices.Clone(state.Findings)
	for i, finding := range state.Findings {
		state.Findings[i] = findings[finding.FindingID]
	}
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

func bindWorkflowReviewIntent(state *WorkflowAdmissionState, actionID string, payload json.RawMessage) error {
	if actionID != "dispatch_worker" {
		return nil
	}
	fields, err := workflowActionObject(payload)
	if err != nil {
		return err
	}
	laneID := workflowFieldStringDefault(fields, "lane_id", "")
	var packet struct {
		LaneID    string `json:"lane_id"`
		AttemptID string `json:"attempt_id"`
		Inputs    struct {
			WorkerJob *WorkerPacketJob `json:"worker_job"`
		} `json:"inputs"`
	}
	if raw := fields["worker_packet"]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &packet); err != nil {
			return wrapFailure(KindInvalidPayload, "workflow_review", "workflow action payload field \"worker_packet\" has wrong registered type or bounds", false, "supply the declared worker packet object", err)
		}
		laneID = packet.LaneID
	}
	for _, lane := range BuiltinLaneDefinitions() {
		if lane.ID == laneID {
			state.ReviewLane = lane.CapabilityClass
		}
	}
	state.ReviewGroup = packet.AttemptID
	if packet.Inputs.WorkerJob != nil {
		state.ReviewGroup = packet.Inputs.WorkerJob.JobID
	}
	if state.ReviewLane == "implementation" && state.ReviewGroup == "" {
		state.ReviewGroup = "unbound-group"
	}
	return nil
}

func bindWorkflowReviewRequest(ctx context.Context, q queryer, state *WorkflowAdmissionState, workID, actionID string, payload json.RawMessage, actor WorkflowActor) error {
	if err := bindWorkflowReviewIntent(state, actionID, payload); err != nil {
		return err
	}
	if actionID == "dispatch_worker" && state.ReviewLane == "implementation" && state.ReviewGroup == "unbound-group" {
		var count int
		var job string
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MIN(j.job_id),'') FROM worker_job_revisions j WHERE j.work_id=? AND `+workerJobReadyPredicate, workID).Scan(&count, &job); err != nil {
			return err
		}
		if count == 1 {
			state.ReviewGroup = job
		}
	}
	if actionID == "dispatch_worker" && state.ReviewLane == "implementation" {
		fields, err := workflowActionObject(payload)
		if err != nil {
			return err
		}
		correctionRef, err := workflowDeclaredReviewCorrection(ctx, q, workID, workflowFieldStringDefault(fields, "attempt_id", ""), state.Review)
		if err != nil {
			return err
		}
		state.ReviewCorrective = correctionRef != ""
	}
	if actionID != "accept_worker_result" {
		return nil
	}
	fields, err := workflowActionObject(payload)
	if err != nil {
		return err
	}
	ref, err := WorkflowActorRef(actor)
	if err != nil {
		return err
	}
	if err := validateReviewAcceptance(ctx, q, workID, workflowFieldStringDefault(fields, "attempt_id", ""), ref); err != nil {
		state.ReviewAcceptanceError = workflowFailureOf(err)
	}
	return nil
}

func readReviewAttempt(ctx context.Context, q queryer, workID, attempt string) (workflowActionCompletedPayload, WorkerCompletedPayload, error) {
	var auth workflowActionCompletedPayload
	var result WorkerCompletedPayload
	var raw []byte
	err := q.QueryRowContext(ctx, `SELECT payload FROM worker_authorizations WHERE work_id=? AND attempt_id=?`, workID, attempt).Scan(&raw)
	if err != nil {
		return auth, result, err
	}
	if err = json.Unmarshal(raw, &auth); err != nil {
		return auth, result, err
	}
	err = q.QueryRowContext(ctx, `SELECT terminal_payload FROM worker_attempts WHERE work_id=? AND attempt_id=? AND terminal_kind=?`, workID, attempt, WorkerCompleted).Scan(&raw)
	if err != nil {
		return auth, result, err
	}
	err = json.Unmarshal(raw, &result)
	return auth, result, err
}

// validateReviewAcceptance never accepts a worker-claimed subject. The report
// must agree with both the admitted input and the host's terminal output.
func validateReviewAcceptance(ctx context.Context, q queryer, workID, attempt, actor string) error {
	if err := workflowRequireMain(ctx, q, workID, actor); err != nil {
		return err
	}
	auth, result, err := readReviewAttempt(ctx, q, workID, attempt)
	if err == sql.ErrNoRows {
		return reviewFailure(KindMissingEvidence, "the requested assignment has no admitted terminal result", "reconcile the exact assignment's terminal result before accepting it")
	}
	if err != nil {
		return err
	}
	if auth.WorkerCapabilityClass != "review" {
		return validateImplementationAcceptance(ctx, q, workID, auth, result)
	}
	report := result.Review
	if report == nil {
		return reviewFailure(KindMissingEvidence, "Review has no exact terminal report", "obtain the terminal report before accepting it")
	}
	if err := validateWorkerReviewBlock(report); err != nil {
		return err
	}
	if err := validateReviewObservedCandidates(auth, result); err != nil {
		return err
	}
	return validateReviewAssignmentAcceptance(ctx, q, workID, auth, report)
}

func validateImplementationAcceptance(ctx context.Context, q queryer, workID string, auth workflowActionCompletedPayload, result WorkerCompletedPayload) error {
	if auth.WorkerCapabilityClass != "implementation" {
		return nil
	}
	base, err := workflowMemberResultBase(ctx, q, workID, auth)
	if err != nil {
		return err
	}
	if base != nil {
		if auth.WorkerSubjectCommit != base.Commit {
			return reviewFailure(KindStaleRequiresReview, "member result does not start from its group's shared base", "dispatch the member from its admitted group base")
		}
		return nil
	}
	state, err := loadWorkflowReviewState(ctx, q, workID)
	if err != nil {
		return err
	}
	if state.Candidate != nil && auth.WorkerSubjectCommit != state.Candidate.Commit {
		if result.ObservedDelivery == nil || result.ObservedDelivery.Commit != state.Candidate.Commit || result.ObservedDelivery.Tree != state.Candidate.Tree {
			return reviewFailure(KindStaleRequiresReview, "this result starts from an older integrated candidate", "retain its evidence and integrate useful changes onto Main's current candidate before obtaining applicable Review")
		}
	}
	return nil
}

func validateReviewObservedCandidates(auth workflowActionCompletedPayload, result WorkerCompletedPayload) error {
	report := result.Review
	observation := result.ObservedDelivery
	if observation == nil || observation.State != "clean" || observation.Commit != report.OutputCandidate.Commit || observation.Tree != report.OutputCandidate.Tree || auth.WorkerSubjectCommit != report.InputCandidate.Commit || (observation.Base != "" && observation.Base != report.InputCandidate.Commit) || report.InputCandidate.ProjectID != report.OutputCandidate.ProjectID {
		return reviewFailure(KindStaleRequiresReview, "Review input/output does not match its admitted input and clean host-observed output", "finish and observe the scoped Review candidate, then return its exact terminal report to Main")
	}
	if report.InputCandidate.Commit == report.OutputCandidate.Commit {
		if report.InputCandidate.Tree != report.OutputCandidate.Tree {
			return reviewFailure(KindStaleRequiresReview, "unchanged Review commit names different input and output trees", "return the exact observed candidate identity")
		}
	} else if observation.Base != report.InputCandidate.Commit || observation.BaseTree != report.InputCandidate.Tree || observation.ChangedPaths == nil {
		return reviewFailure(KindMissingEvidence, "repairing Review lacks the host-observed input tree or corrective delta", "capture the scoped input-to-output delta and input tree through the terminal producer")
	}
	return nil
}

func validateReviewAssignmentAcceptance(ctx context.Context, q queryer, workID string, auth workflowActionCompletedPayload, report *WorkerReviewBlock) error {
	if auth.WorkerJob == nil {
		return reviewFailure(KindMissingEvidence, "Review has no admitted assignment binding", "dispatch Review with the recorded scoped assignment")
	}
	job, err := readWorkerJobRevisionTx(ctx, q, workID, *auth.WorkerJob)
	if err != nil {
		return err
	}
	if job.ProjectScope != report.InputCandidate.ProjectID {
		return reviewFailure(KindUnauthorized, "Review candidate names another Project", "return a report for the Project owning the admitted assignment")
	}
	state, err := loadWorkflowReviewState(ctx, q, workID)
	if err != nil {
		return err
	}
	if state.Candidate != nil && *state.Candidate != report.InputCandidate {
		current, err := workflowMemberReviewCurrent(ctx, q, workID, auth, *state.Candidate, report.InputCandidate)
		if err != nil {
			return err
		}
		if !current {
			return reviewFailure(KindStaleRequiresReview, "a newer accepted candidate replaced this Review's input", "retain this report as evidence of its original subject and obtain Review of the current candidate")
		}
	}
	dependencies, err := workflowReviewDependencies(ctx, q, workID)
	if err != nil {
		return err
	}
	if !sameReviewDependencies(report.Dependencies, dependencies) {
		return reviewFailure(KindStaleRequiresReview, "Review judged different criterion or architecture content", "reassess the unchanged candidate against the changed depended-on revisions and return a new report")
	}
	return validateReviewFindingClosures(ctx, q, workID, report, state.Findings)
}

func validateReviewFindingClosures(ctx context.Context, q queryer, workID string, report *WorkerReviewBlock, previousFindings []WorkerReviewFinding) error {
	for _, finding := range report.Findings {
		for _, previous := range previousFindings {
			if previous.FindingID == finding.FindingID {
				if err := validateReviewFindingBinding(previous, finding); err != nil {
					return err
				}
				break
			}
		}
		if finding.Status != "closed" {
			continue
		}
		if err := validateReviewFindingClosure(ctx, q, workID, report.OutputCandidate, finding); err != nil {
			return err
		}
	}
	return nil
}

func validateReviewFindingClosure(ctx context.Context, q queryer, workID string, output WorkerReviewCandidate, finding WorkerReviewFinding) error {
	coveredControls := map[string]bool{}
	for _, ref := range finding.ClosureRefs {
		producer, err := readOracleVerifySubjectReceipt(ctx, q, workID, ref)
		if err != nil {
			return err
		}
		if producer == nil || producer.SubjectRef != "commit:"+output.Commit || producer.ExitCode != 0 || producer.Oracle == nil || producer.Oracle.Phase != "execute" || producer.ProjectID != output.ProjectID {
			return reviewFailure(KindMissingEvidence, "finding "+finding.FindingID+" has no passing output-subject closure receipt", "verify the corrected candidate and cite the producer's receipt in the finding closure")
		}
		if err := validateNativeOracleProducerTx(ctx, q, workID, producer, OracleReceiptResultPass); err != nil {
			return err
		}
		if finding.Oracle != nil && !slices.Contains(finding.Oracle.ControlIDs, producer.Oracle.ControlID) {
			return reviewFailure(KindMissingEvidence, "finding closure cites a different control", "run the finding's declared control on the corrected candidate")
		}
		coveredControls[producer.Oracle.ControlID] = true
	}
	if finding.Oracle != nil {
		for _, control := range finding.Oracle.ControlIDs {
			if !coveredControls[control] {
				return reviewFailure(KindMissingEvidence, "finding "+finding.FindingID+" has no closure receipt for control "+control, "run each declared finding control on the corrected candidate and cite its captured receipt")
			}
		}
	}
	return nil
}

// A finding's identity retains its original obligation. New reproduction
// evidence and prose may change; closing an unrelated control cannot close it.
func validateReviewFindingBinding(previous, reported WorkerReviewFinding) error {
	old, current := previous.Oracle, reported.Oracle
	if old == nil {
		return nil
	}
	bound := current != nil && old.OwnerID == current.OwnerID && old.FailureFamily == current.FailureFamily &&
		sameStringSet(old.PredicateIDs, current.PredicateIDs) && len(old.LawBindings) == len(current.LawBindings)
	if bound {
		for _, coverage := range []struct{ original, reported []string }{
			{old.ControlIDs, current.ControlIDs}, {old.CaseIDs, current.CaseIDs},
		} {
			for _, id := range coverage.original {
				if !slices.Contains(coverage.reported, id) {
					bound = false
				}
			}
		}
	}
	if bound {
		laws := map[string]string{}
		for _, law := range old.LawBindings {
			laws[law.Source.LawID] = reviewDigest(law)
		}
		for _, law := range current.LawBindings {
			if laws[law.Source.LawID] != reviewDigest(law) {
				bound = false
				break
			}
		}
	}
	if !bound {
		return reviewFailure(KindOutcomeMismatch, "finding "+previous.FindingID+" changes its recorded obligation binding", "retain the finding's recorded owner, criteria, laws and controls; report an unrelated obligation as a separate finding")
	}
	return nil
}

func readWorkerJobRevisionTx(ctx context.Context, q queryer, workID string, binding WorkerJobBinding) (*WorkerJobRecordedPayload, error) {
	var raw []byte
	err := q.QueryRowContext(ctx, `SELECT recorded_payload FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=? AND digest=? AND recorded_payload IS NOT NULL`, workID, binding.JobID, binding.Revision, binding.Digest).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var job WorkerJobRecordedPayload
	if err := json.Unmarshal(raw, &job); err != nil {
		return nil, err
	}
	if DeriveWorkerJobDigest(job) != binding.Digest {
		return nil, reviewFailure(KindInvariantViolation, "recorded assignment digest does not match its content", "reconcile the assignment before accepting its result")
	}
	return &job, nil
}

// Passing producer identity and declared control coverage, not a caller's
// verdict word or a merely bound evidence kind, discharge a criterion.
func workflowCriterionReceipt(ctx context.Context, q queryer, workID, predicate, ref, subject string) error {
	research, err := workflowResearchCriterionFamily(ctx, q, workID)
	if err != nil {
		return err
	}
	if research {
		return workflowResearchCriterionReceipt(ctx, q, workID, predicate, ref)
	}
	producer, err := readOracleVerifySubjectReceipt(ctx, q, workID, ref)
	if err != nil {
		return err
	}
	if producer == nil || producer.ExitCode != 0 || producer.SubjectRef != "commit:"+subject {
		return reviewFailure(KindMissingEvidence, "criterion "+predicate+" requires a passing producer receipt on the final candidate; "+ref+" does not qualify", "run the criterion's declared control on the current candidate and reference its captured receipt")
	}
	if producer.Oracle == nil {
		return workflowReviewedCommandReceipt(ctx, q, workID, predicate, ref, subject, producer.ProjectID)
	}
	if producer.Oracle.Phase != "execute" {
		return reviewFailure(KindMissingEvidence, "preparation is readiness, not criterion proof", "execute the criterion's declared control and cite its captured result")
	}
	if producer.Oracle.WorkerJobBinding == nil {
		return reviewFailure(KindMissingEvidence, "producer receipt names no recorded assignment", "execute the declared control under its admitted assignment")
	}
	job, err := readWorkerJobRevisionTx(ctx, q, workID, *producer.Oracle.WorkerJobBinding)
	if err != nil {
		return err
	}
	if job.AcceptanceOracle != nil && job.ProjectScope == producer.ProjectID {
		for _, control := range job.AcceptanceOracle.Controls {
			if control.ControlID != producer.Oracle.ControlID || !slices.Contains(control.PredicateIDs, predicate) {
				continue
			}
			bundle, err := nativeBundleForControl(job.AcceptanceOracle, control.ControlID)
			if err == nil && producer.Oracle.BundleDigest == nativeBundleDigest(bundle) && producer.Oracle.RecipeSource == control.RecipeSource && slices.Equal(producer.Command, control.Argv) {
				return validateWorkflowReceiptCriterion(ctx, q, workID, predicate, producer)
			}
		}
	}
	return reviewFailure(KindMissingEvidence, fmt.Sprintf("receipt %s has no declared coverage of criterion %s", ref, predicate), "execute a declared control that covers this criterion; an unrelated green command cannot satisfy it")
}

func workflowResearchCriterionReceipt(ctx context.Context, q queryer, workID, predicate, ref string) error {
	view, err := readWorkContextView(ctx, q, workID)
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
	return reviewFailure(KindMissingEvidence, "criterion "+predicate+" cites no current evidenced research finding; "+ref+" does not qualify", "select the exact pack, revision and evidenced finding through record_work_context and cite its research view reference")
}

func workflowReviewedCommandReceipt(ctx context.Context, q queryer, workID, predicate, ref, subject, projectID string) error {
	state, err := loadWorkflowReviewState(ctx, q, workID)
	if err != nil {
		return err
	}
	if state.Candidate != nil && state.Accepted != nil && state.DependenciesCurrent && state.Accepted.Report.OutputCandidate == *state.Candidate && state.Candidate.Commit == subject && state.Candidate.ProjectID == projectID {
		for _, assessment := range state.Accepted.Report.Assessments {
			if assessment.PredicateID == predicate && assessment.Assessment == "supported" && slices.Contains(assessment.ReceiptRefs, ref) {
				return nil
			}
		}
	}
	return reviewFailure(KindMissingEvidence, "command receipt "+ref+" has no accepted Review assessment covering criterion "+predicate, "have Review assess this criterion on the final candidate and cite the captured passing command receipt, then accept that exact report")
}

func validateWorkflowReceiptCriterion(ctx context.Context, q queryer, workID, predicate string, producer *WorktreeVerifyResult) error {
	var oldKind, currentKind string
	var oldPayload, currentPayload string
	if err := q.QueryRowContext(ctx, `SELECT outcome_kind,outcome_payload FROM workflow_contract_predicates WHERE work_id=? AND contract_version=? AND predicate_id=?`, workID, producer.Oracle.ContractVersion, predicate).Scan(&oldKind, &oldPayload); err != nil {
		return reviewFailure(KindMissingEvidence, "receipt has no captured coverage of this criterion", "execute the criterion's declared control and cite its producer receipt")
	}
	version, err := activeWorkflowContractVersion(ctx, q, workID, "workflow_review")
	if err != nil {
		return err
	}
	if err := q.QueryRowContext(ctx, `SELECT outcome_kind,outcome_payload FROM workflow_contract_predicates WHERE work_id=? AND contract_version=? AND predicate_id=?`, workID, version, predicate).Scan(&currentKind, &currentPayload); err != nil {
		return err
	}
	oldDigest := reviewDigest(struct {
		Kind    string
		Payload json.RawMessage
	}{oldKind, json.RawMessage(oldPayload)})
	currentDigest := reviewDigest(struct {
		Kind    string
		Payload json.RawMessage
	}{currentKind, json.RawMessage(currentPayload)})
	if oldDigest != currentDigest {
		return reviewFailure(KindStaleRequiresReview, "receipt covers different criterion content", "reassess the changed criterion and capture applicable proof")
	}
	return validateNativeOracleProducerTx(ctx, q, workID, producer, OracleReceiptResultPass)
}

func workflowResearchCriterionFamily(ctx context.Context, q queryer, workID string) (bool, error) {
	var research bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_instances WHERE work_id=? AND definition_ref='workflow.research')`, workID).Scan(&research)
	return research, err
}

// Associations retain the criterion content and candidate captured when Main
// selected the receipt. Unchanged dependencies survive an administrative resume.
func verifyWorkflowCriterionAssociation(ctx context.Context, q queryer, workID string, association workflowVerdictRecordedPayload) error {
	if err := workflowRequireMain(ctx, q, workID, association.VerdictActorRef); err != nil {
		return err
	}
	state, err := loadWorkflowReviewState(ctx, q, workID)
	if err != nil {
		return err
	}
	research, err := workflowResearchCriterionFamily(ctx, q, workID)
	if err != nil {
		return err
	}
	if (!research && (association.Candidate == nil || state.Candidate == nil || *association.Candidate != *state.Candidate)) || association.VerdictKind != "ok" || association.IncomparableWithApproved || len(association.EvaluationEvidence) == 0 {
		return reviewFailure(KindMissingEvidence, "criterion proof does not identify the current integrated candidate", "associate captured passing receipts for the current candidate")
	}
	dependencies, err := workflowReviewDependencies(ctx, q, workID)
	if err != nil {
		return err
	}
	for _, criterion := range dependencies.Criteria {
		if criterion.PredicateID != association.PredicateID {
			continue
		}
		if criterion.ContentDigest != association.CriterionDigest {
			return reviewFailure(KindStaleRequiresReview, "criterion content changed after proof association", "reassess only the changed criterion and associate applicable producer proof")
		}
		for _, ref := range association.EvaluationEvidence {
			subject := ""
			if association.Candidate != nil {
				subject = association.Candidate.Commit
			}
			if err := workflowCriterionReceipt(ctx, q, workID, criterion.PredicateID, ref, subject); err != nil {
				return err
			}
		}
		return nil
	}
	return reviewFailure(KindOutcomeMismatch, "proof association names an unapproved criterion", "associate a criterion of the current approved contract")
}

func workflowMissingCriterionAssociations(ctx context.Context, q queryer, workID string) (map[string]bool, error) {
	version, err := activeWorkflowContractVersion(ctx, q, workID, "workflow_review")
	if err != nil {
		return nil, err
	}
	criteria, err := readWorkflowContractPredicates(ctx, q, workID, version)
	if err != nil {
		return nil, err
	}
	missing := make(map[string]bool, len(criteria))
	for _, criterion := range criteria {
		missing[criterion.PredicateID] = true
	}
	associations, err := latestWorkflowVerdicts(ctx, q, workID, version)
	if err != nil {
		return nil, err
	}
	for _, association := range associations {
		if err := verifyWorkflowCriterionAssociation(ctx, q, workID, association); err != nil {
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

// Every entry in a late batch must fill a missing association. An unrelated
// missing criterion cannot authorize replacing already applicable proof.
func guardLateCriterionAssociations(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, step string, fields map[string]json.RawMessage) error {
	if !workflowLateCriterionAssociationStep(definition, step) {
		return nil
	}
	entries, err := normalizeWorkflowVerdictEntries(fields)
	if err != nil {
		return err
	}
	missing, err := workflowMissingCriterionAssociations(ctx, q, workID)
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
