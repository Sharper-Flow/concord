package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
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
	Candidate              *WorkerReviewCandidate  `json:"candidate"`
	Accepted               *WorkflowAcceptedReview `json:"accepted_review"`
	Findings               []WorkerReviewFinding   `json:"findings"`
	ContinueConsumed       bool                    `json:"continue_consumed"`
	GroupID                string                  `json:"group_id,omitempty"`
	DependenciesCurrent    bool                    `json:"dependencies_current"`
	PublicationOutstanding bool                    `json:"publication_outstanding"`
}

func reviewFailure(kind FailureKind, detail, next string) *Failure {
	return newFailure(kind, "workflow_review", detail, false, next)
}

func workflowRequireMain(ctx context.Context, q queryer, workID, actor string) error {
	worker, _, err := workflowRecordedEvaluationFacts(ctx, q, workID, actor, int64(1)<<62)
	if err != nil {
		return err
	}
	if worker {
		return reviewFailure(KindUnauthorized, "a worker cannot accept results, associate criterion proof, or complete work", "return the attributed report to Main")
	}
	return nil
}

func reviewDigest(value any) string {
	raw, _ := json.Marshal(value)
	canonical, err := canonicalJSON(raw)
	if err != nil {
		return ""
	}
	return nativeDigest(canonical)
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
		result.Criteria = append(result.Criteria, WorkerReviewCriterion{PredicateID: id, ContentDigest: reviewDigest(struct {
			Kind    string          `json:"kind"`
			Payload json.RawMessage `json:"payload"`
		}{kind, json.RawMessage(payload)})})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	design, _, err := readCurrentWorkflowDesign(ctx, q, workID)
	if err != nil {
		return result, err
	}
	if design == nil {
		result.ArchitectureNotApplicable = true
	} else {
		result.ArchitectureDigest = reviewDigest(struct {
			Approach    string                   `json:"approach"`
			Decisions   []WorkflowDesignDecision `json:"decisions"`
			TouchedRefs []string                 `json:"touched_refs"`
		}{design.Approach, design.Decisions, design.TouchedRefs})
	}
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

type workflowReviewRow struct {
	seq              int64
	kind, actor, ref string
	raw              []byte
}

// This reader consumes the existing action/terminal owner. It adds no launch,
// mutation route, or duplicate receipt. The storage cutover can retain these
// same current descriptors without changing admission's input.
func loadWorkflowReviewState(ctx context.Context, q queryer, workID string, before int64) (WorkflowReviewState, error) {
	state := WorkflowReviewState{Findings: []WorkerReviewFinding{}, PublicationOutstanding: true}
	rows, err := q.QueryContext(ctx, `SELECT seq,kind,actor,event_id,payload FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind IN (?,?) AND (?=0 OR seq<?) ORDER BY seq`, workID, WorkflowActionCompleted, WorkerCompleted, before, before)
	if err != nil {
		return state, err
	}
	var records []workflowReviewRow
	for rows.Next() {
		var row workflowReviewRow
		if err := rows.Scan(&row.seq, &row.kind, &row.actor, &row.ref, &row.raw); err != nil {
			rows.Close()
			return state, err
		}
		records = append(records, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return state, err
	}
	reports := map[string]workflowReviewRow{}
	completions := map[string]WorkerCompletedPayload{}
	dispatches := map[string]workflowActionCompletedPayload{}
	findings := map[string]WorkerReviewFinding{}
	for _, row := range records {
		if row.kind == WorkerCompleted {
			var result WorkerCompletedPayload
			if err := json.Unmarshal(row.raw, &result); err != nil {
				return state, err
			}
			completions[result.AttemptID] = result
			if result.Review != nil {
				reports[result.AttemptID] = row
				if state.Candidate != nil && *state.Candidate != result.Review.InputCandidate {
					continue
				}
				for _, finding := range result.Review.Findings {
					if previous, found := findings[finding.FindingID]; found {
						if previous.Oracle != nil {
							finding.Oracle = previous.Oracle
						}
						if previous.Status == "open" {
							finding.InScope = finding.InScope || previous.InScope
							finding.Blocking = finding.Blocking || previous.Blocking
							finding.Status = "open"
						}
					}
					// A report can introduce an evidenced closure, but only Main's
					// exact acceptance below closes a previously open finding.
					if finding.Status == "closed" {
						finding.Status = "open"
					}
					findings[finding.FindingID] = finding
				}
			}
			continue
		}
		var action workflowActionCompletedPayload
		if err := json.Unmarshal(row.raw, &action); err != nil {
			return state, err
		}
		if action.ActionID == "dispatch_worker" {
			dispatches[action.WorkerAttemptID] = action
			if action.WorkerCapabilityClass == "implementation" && state.Accepted != nil && state.Accepted.Report.Disposition == "continue" && state.Candidate != nil && state.Accepted.Report.OutputCandidate == *state.Candidate {
				state.ContinueConsumed = true
				if action.WorkerJob != nil {
					state.GroupID = action.WorkerJob.JobID
				} else {
					state.GroupID = action.WorkerAttemptID
				}
			}
			continue
		}
		if action.ActionID != "accept_worker_result" {
			continue
		}
		result, found := completions[action.WorkerAttemptID]
		if !found {
			continue
		}
		if result.Review != nil {
			reportRow := reports[action.WorkerAttemptID]
			state.Accepted = &WorkflowAcceptedReview{AttemptID: result.AttemptID, ReportRef: reportRow.ref, ReportDigest: reviewDigest(result.Review), ActorRef: row.actor, Report: *result.Review}
			candidate := result.Review.OutputCandidate
			state.Candidate = &candidate
			state.ContinueConsumed, state.GroupID = false, ""
			for _, finding := range result.Review.Findings {
				if finding.Status == "closed" {
					previous := findings[finding.FindingID]
					previous.Status, previous.ClosureRefs = "closed", finding.ClosureRefs
					findings[finding.FindingID] = previous
				}
			}
		} else if delivery := result.ObservedDelivery; delivery != nil && delivery.State == "clean" {
			project := ""
			if auth := dispatches[result.AttemptID]; auth.WorkerJob != nil {
				job, err := readWorkerJobRevisionTx(ctx, q, workID, *auth.WorkerJob)
				if err != nil {
					return state, err
				}
				project = job.ProjectScope
			}
			state.Candidate = &WorkerReviewCandidate{ProjectID: project, Commit: delivery.Commit, Tree: delivery.Tree}
		}
	}
	ids := make([]string, 0, len(findings))
	for id := range findings {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		state.Findings = append(state.Findings, findings[id])
	}
	if state.Accepted != nil {
		dependencies, err := workflowReviewDependencies(ctx, q, workID)
		if err != nil {
			return state, err
		}
		state.DependenciesCurrent = sameReviewDependencies(state.Accepted.Report.Dependencies, dependencies)
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
			return err
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
	if err := validateReviewAcceptance(ctx, q, workID, workflowFieldStringDefault(fields, "attempt_id", ""), ref, 0); err != nil {
		state.ReviewAcceptanceError = workflowFailureOf(err)
	}
	return nil
}

func readReviewAttempt(ctx context.Context, q queryer, workID, attempt string) (workflowActionCompletedPayload, WorkerCompletedPayload, error) {
	var auth workflowActionCompletedPayload
	var result WorkerCompletedPayload
	var raw []byte
	err := q.QueryRowContext(ctx, `SELECT payload FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=? ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionCompleted, attempt).Scan(&raw)
	if err != nil {
		return auth, result, err
	}
	if err = json.Unmarshal(raw, &auth); err != nil {
		return auth, result, err
	}
	err = q.QueryRowContext(ctx, `SELECT payload FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.attempt_id')=? ORDER BY seq DESC LIMIT 1`, workID, WorkerCompleted, attempt).Scan(&raw)
	if err != nil {
		return auth, result, err
	}
	err = json.Unmarshal(raw, &result)
	return auth, result, err
}

// validateReviewAcceptance never accepts a worker-claimed subject. The report
// must agree with both the admitted input and the host's terminal output.
func validateReviewAcceptance(ctx context.Context, q queryer, workID, attempt, actor string, before int64) error {
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
		if auth.WorkerCapabilityClass == "implementation" {
			state, err := loadWorkflowReviewState(ctx, q, workID, before)
			if err != nil {
				return err
			}
			if state.Candidate != nil && auth.WorkerSubjectCommit != state.Candidate.Commit {
				if result.ObservedDelivery == nil || result.ObservedDelivery.Commit != state.Candidate.Commit || result.ObservedDelivery.Tree != state.Candidate.Tree {
					return reviewFailure(KindStaleRequiresReview, "this result starts from an older integrated candidate", "retain its evidence and integrate useful changes onto Main's current candidate before obtaining applicable Review")
				}
			}
		}
		return nil
	}
	report := result.Review
	if report == nil {
		return reviewFailure(KindMissingEvidence, "Review has no exact terminal report", "obtain the terminal report before accepting it")
	}
	if err := validateWorkerReviewBlock(report); err != nil {
		return err
	}
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
	state, err := loadWorkflowReviewState(ctx, q, workID, before)
	if err != nil {
		return err
	}
	if state.Candidate != nil && *state.Candidate != report.InputCandidate {
		return reviewFailure(KindStaleRequiresReview, "a newer accepted candidate replaced this Review's input", "retain this report as evidence of its original subject and obtain Review of the current candidate")
	}
	dependencies, err := workflowReviewDependencies(ctx, q, workID)
	if err != nil {
		return err
	}
	if !sameReviewDependencies(report.Dependencies, dependencies) {
		return reviewFailure(KindStaleRequiresReview, "Review judged different criterion or architecture content", "reassess the unchanged candidate against the changed depended-on revisions and return a new report")
	}
	for _, finding := range report.Findings {
		for _, previous := range state.Findings {
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
		coveredControls := map[string]bool{}
		for _, ref := range finding.ClosureRefs {
			producer, err := readOracleVerifySubjectReceipt(ctx, q, workID, ref)
			if err != nil {
				return err
			}
			if producer == nil || producer.SubjectRef != "commit:"+report.OutputCandidate.Commit || producer.ExitCode != 0 || producer.Oracle == nil || producer.Oracle.Phase != "execute" || producer.ProjectID != report.OutputCandidate.ProjectID {
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
	err := q.QueryRowContext(ctx, `SELECT payload FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.job_id')=? AND json_extract(payload,'$.revision')=? AND json_extract(payload,'$.digest')=? ORDER BY seq DESC LIMIT 1`, workID, WorkerJobRecorded, binding.JobID, binding.Revision, binding.Digest).Scan(&raw)
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
	producer, err := readOracleVerifySubjectReceipt(ctx, q, workID, ref)
	if err != nil {
		return err
	}
	if producer == nil || producer.ExitCode != 0 || producer.Oracle == nil || producer.Oracle.Phase != "execute" || producer.SubjectRef != "commit:"+subject {
		return reviewFailure(KindMissingEvidence, "criterion "+predicate+" requires a passing producer receipt on the final candidate; "+ref+" does not qualify", "run the criterion's declared control on the current candidate and reference its captured receipt")
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
		}
	}
	return reviewFailure(KindMissingEvidence, fmt.Sprintf("receipt %s has no declared coverage of criterion %s", ref, predicate), "execute a declared control that covers this criterion; an unrelated green command cannot satisfy it")
}

// Associations retain the criterion content and candidate captured when Main
// selected the receipt. Unchanged dependencies survive an administrative resume.
func verifyWorkflowCriterionAssociation(ctx context.Context, q queryer, workID string, association workflowVerdictRecordedPayload) error {
	if err := workflowRequireMain(ctx, q, workID, association.VerdictActorRef); err != nil {
		return err
	}
	state, err := loadWorkflowReviewState(ctx, q, workID, 0)
	if err != nil {
		return err
	}
	if association.Candidate == nil || state.Candidate == nil || *association.Candidate != *state.Candidate || association.VerdictKind != "ok" || association.IncomparableWithApproved || len(association.EvaluationEvidence) == 0 {
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
			if err := workflowCriterionReceipt(ctx, q, workID, criterion.PredicateID, ref, association.Candidate.Commit); err != nil {
				return err
			}
		}
		return nil
	}
	return reviewFailure(KindOutcomeMismatch, "proof association names an unapproved criterion", "associate a criterion of the current approved contract")
}
