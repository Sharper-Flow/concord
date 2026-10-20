package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// WorkerJobSplitDischarged records one split's automatic integration
// discharge (CD-0225 D3) once, beside the split it closes. It satisfies no
// revision and closes no job state: the predecessor keeps its replaced
// disposition, and the correction, prerequisite, and delivery readers
// consume the event itself.
const WorkerJobSplitDischarged = "worker.job_split_discharged"

// workerJobSplitDischargeSuffix closes a discharge event id: the producing
// operation ID beside this digest of the split, so the fold derives the
// operation prefix back and requires a source binding that operation minted.
func workerJobSplitDischargeSuffix(splitID string) string {
	return nativeDigest([]byte(splitID))
}

// WorkerJobSplitDischargeLeaf records one accepted required leaf beside the
// acceptance event that satisfied it.
type WorkerJobSplitDischargeLeaf struct {
	Binding      WorkerJobBinding `json:"binding"`
	SatisfiedRef string           `json:"satisfied_ref"`
}

// WorkerJobSplitDischargeSource records one qualifying integration run: the
// binding event that cited it, the worktree.verify operation it names, the
// Project its lease ran under, and the exact command.
type WorkerJobSplitDischargeSource struct {
	BindingEventID string   `json:"binding_event_id"`
	OperationRef   string   `json:"operation_ref"`
	ProjectID      string   `json:"project_id"`
	Command        []string `json:"command"`
}

// WorkerJobSplitDischargedPayload extends the discharge identity the split
// derivation joins on with the accepted leaves and qualifying integration.
type WorkerJobSplitDischargedPayload struct {
	WorkflowVersionFields
	workerJobSplitDischargePayload
	Leaves      []WorkerJobSplitDischargeLeaf   `json:"leaves"`
	Integration []WorkerJobSplitDischargeSource `json:"integration"`
}

func workerJobSplitDischargeFoldFailure(detail string) error {
	return newFailure(KindInvalidPayload, "fold_event", "worker.job_split_discharged "+detail, false, "rebuild the worker-job events from the authoritative log")
}

// workerJobVerificationBinding is one verification binding the action's
// pending semantic events carry: its event id and the run it names.
type workerJobVerificationBinding struct {
	eventID string
	subject string
}

// workerJobPendingVerificationBindings projects the action's pending
// verification bindings out of its semantic events.
func workerJobPendingVerificationBindings(semantic []Event) []workerJobVerificationBinding {
	bindings := []workerJobVerificationBinding{}
	for _, event := range semantic {
		if event.Kind != WorkflowEvidenceBound {
			continue
		}
		var payload workflowEvidenceBoundPayload
		if json.Unmarshal(event.Payload, &payload) != nil || payload.EvidenceKind != string(EvidenceVerification) || payload.ImmutableSubjectRef == "" {
			continue
		}
		bindings = append(bindings, workerJobVerificationBinding{eventID: event.EventID, subject: payload.ImmutableSubjectRef})
	}
	return bindings
}

// workerJobSplitDischargeEventsTx derives the automatic discharges the
// action completes (CD-0225 D3): size-capable pins only, and only when the
// pending semantic events bind verification evidence. Every split whose
// leaves are all satisfied and whose Project union the qualifying
// integration covers gains one discharge event after the pending events, in
// reverse split-log order. The generic completion adjusts versions after.
func workerJobSplitDischargeEventsTx(ctx context.Context, tx *sql.Tx, definition WorkflowDefinition, request WorkflowActionExecutionRequest, currentStep, actor string, semantic []Event, prefix int) ([]Event, error) {
	if !workflowJobSizeActive(definition) {
		return nil, nil
	}
	pending := workerJobPendingVerificationBindings(semantic)
	if len(pending) == 0 {
		return nil, nil
	}
	states, err := readWorkerJobSplitStatesTx(ctx, tx, request.WorkID)
	if err != nil {
		return nil, err
	}
	events := make([]Event, 0, 1)
	for index := len(states) - 1; index >= 0; index-- {
		state := states[index]
		if state.DischargeID != "" || len(state.Unsatisfied) != 0 {
			continue
		}
		anchorSeq, anchorAt, err := workerJobSplitCoverageAnchor(ctx, tx, request.WorkID, definition, currentStep, state.Leaves)
		if err != nil {
			return nil, err
		}
		sources, err := workerJobSplitQualifyingSourcesTx(ctx, tx, request.WorkID, anchorSeq, anchorAt, pending, request.ProjectTooling)
		if err != nil {
			return nil, err
		}
		// A split without qualifying coverage keeps no discharge: the
		// binding action still commits, and a later binding tries again.
		covered := map[string]bool{}
		for _, source := range sources {
			covered[source.ProjectID] = true
		}
		if sources == nil || !workflowIntegrationCovered(workerJobSplitLeafScopes(state.Leaves), covered) {
			continue
		}
		discharge, err := workerJobSplitDischargeEvent(request, actor, state, sources, request.ExpectedVersion+int64(prefix+len(events)))
		if err != nil {
			return nil, err
		}
		events = append(events, discharge)
	}
	return events, nil
}

// workerJobSplitCoverageAnchor derives the seq and time the split's
// integration must postdate: the phase start when one exists, every required
// leaf's acceptance, and every required leaf's latest recorded result
// population — the delivery integration facet's epoch, per split.
func workerJobSplitCoverageAnchor(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep string, leaves []WorkerJobRevisionView) (int64, time.Time, error) {
	startStep := currentStep
	if workflowStepIsDeliveryGate(workflowStep(definition, currentStep)) {
		for _, edge := range definition.StepGraph.Edges {
			if edge.To == currentStep && edge.Kind == WorkflowEdgeForward {
				startStep = edge.From
				break
			}
		}
	}
	startSeq, _, started, err := latestWorkflowActionStart(ctx, q, workID, startStep)
	if err != nil {
		return 0, time.Time{}, err
	}
	anchorAt := time.Time{}
	if started {
		anchorAt, err = workflowActionOccurredAt(ctx, q, workID, startSeq)
		if err != nil {
			return 0, time.Time{}, err
		}
	} else {
		startSeq = 0
	}
	coverageSeq := startSeq
	for _, leaf := range leaves {
		var seq int64
		var acceptanceAt string
		if err := q.QueryRowContext(ctx, `SELECT seq,occurred_at FROM domain_events WHERE subject_type=? AND subject_id=? AND event_id=?`, string(SubjectWorkItem), workID, leaf.SatisfiedResultRef).Scan(&seq, &acceptanceAt); err != nil {
			if err == sql.ErrNoRows {
				return 0, time.Time{}, newFailure(KindInvariantViolation, "workflow_action", "a satisfied split leaf names an acceptance that the event log does not hold", false, "rebuild the worker-job projection from the event log")
			}
			return 0, time.Time{}, wrapFailure(KindUnavailable, "workflow_action", "cannot read the split leaf's acceptance", true, "retry once the event log is readable", err)
		}
		if seq > coverageSeq {
			coverageSeq = seq
		}
		if at, parseErr := time.Parse(time.RFC3339Nano, acceptanceAt); parseErr == nil && at.After(anchorAt) {
			anchorAt = at
		}
	}
	required := map[string]bool{}
	for _, leaf := range leaves {
		required[workerJobKey(leaf.Binding.JobID, leaf.Binding.Revision)] = true
	}
	completions, err := q.QueryContext(ctx, `SELECT COALESCE(json_extract(payload,'$.worker_job.job_id'),''),COALESCE(json_extract(payload,'$.worker_job.revision'),0),seq,occurred_at FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND seq>?`, string(SubjectWorkItem), workID, WorkerCompleted, startSeq)
	if err != nil {
		return 0, time.Time{}, wrapFailure(KindUnavailable, "workflow_action", "cannot read the split's required result population", true, "retry once the event log is readable", err)
	}
	defer completions.Close()
	for completions.Next() {
		var jobID, completionAt string
		var revision, seq int64
		if err := completions.Scan(&jobID, &revision, &seq, &completionAt); err != nil {
			return 0, time.Time{}, wrapFailure(KindUnavailable, "workflow_action", "cannot scan the split's required result population", true, "retry once the event log is readable", err)
		}
		if !required[workerJobKey(jobID, revision)] {
			continue
		}
		if seq > coverageSeq {
			coverageSeq = seq
		}
		if at, parseErr := time.Parse(time.RFC3339Nano, completionAt); parseErr == nil && at.After(anchorAt) {
			anchorAt = at
		}
	}
	if err := completions.Err(); err != nil {
		return 0, time.Time{}, wrapFailure(KindUnavailable, "workflow_action", "cannot scan the split's required result population", true, "retry once the event log is readable", err)
	}
	return coverageSeq, anchorAt, nil
}

// workerJobSplitQualifyingSourcesTx reads the integration the split may
// discharge on: the log's verification bindings after the anchor beside the
// action's pending bindings, each through the one verify-run fold. Each run
// is retained once, at its first qualifying binding; a manifest-holding
// request keeps only declared commands. At least one pending binding must
// qualify, so an unrelated action cannot discharge on earlier bindings.
func workerJobSplitQualifyingSourcesTx(ctx context.Context, q queryer, workID string, anchorSeq int64, anchorAt time.Time, pending []workerJobVerificationBinding, tooling *ProjectToolingManifest) ([]WorkerJobSplitDischargeSource, error) {
	var candidates []workerJobVerificationBinding
	rows, err := q.QueryContext(ctx, `SELECT event_id,json_extract(payload,'$.immutable_subject_ref') FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND seq>? AND json_extract(payload,'$.evidence_kind')='verification' ORDER BY seq`, string(SubjectWorkItem), workID, WorkflowEvidenceBound, anchorSeq)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_job_split_discharge", "cannot read the work's verification bindings", true, "retry once the event log is readable", err)
	}
	seen := map[string]bool{}
	for rows.Next() {
		var candidate workerJobVerificationBinding
		var subject string
		if err := rows.Scan(&candidate.eventID, &subject); err != nil {
			_ = rows.Close()
			return nil, wrapFailure(KindUnavailable, "worker_job_split_discharge", "cannot scan the work's verification bindings", true, "retry once the event log is readable", err)
		}
		if subject == "" || seen[subject] {
			continue
		}
		seen[subject] = true
		candidate.subject = subject
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, wrapFailure(KindUnavailable, "worker_job_split_discharge", "cannot scan the work's verification bindings", true, "retry once the event log is readable", err)
	}
	if err := rows.Close(); err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_job_split_discharge", "cannot close the verification binding read", true, "retry once the event log is readable", err)
	}
	recorded := map[string]bool{}
	sources := make([]WorkerJobSplitDischargeSource, 0, len(candidates)+len(pending))
	qualify := func(candidate workerJobVerificationBinding) (bool, error) {
		command, project, why, loadErr := loadWorkflowVerifyRun(ctx, q, workID, candidate.subject, anchorAt, "worker_job_split_discharge")
		if loadErr != nil {
			var failure *Failure
			if !failureAs(loadErr, &failure) || failure.Kind == KindUnavailable {
				return false, loadErr
			}
			// A malformed projection fails closed: the run qualifies for no
			// Project until the projection is rebuilt.
			return false, nil
		}
		if why != "" {
			return false, nil
		}
		if tooling != nil && !ProjectToolingInvocationDeclared(tooling, command) {
			return false, nil
		}
		if !recorded[candidate.subject] {
			recorded[candidate.subject] = true
			sources = append(sources, WorkerJobSplitDischargeSource{BindingEventID: candidate.eventID, OperationRef: candidate.subject, ProjectID: project, Command: command})
		}
		return true, nil
	}
	for _, candidate := range candidates {
		if _, err := qualify(candidate); err != nil {
			return nil, err
		}
	}
	pendingEligible := false
	for _, candidate := range pending {
		eligible, err := qualify(candidate)
		if err != nil {
			return nil, err
		}
		pendingEligible = pendingEligible || eligible
	}
	if !pendingEligible {
		return nil, nil
	}
	return sources, nil
}

// workerJobSplitLeafScopes is the deterministic Project union the split's
// leaves require, in leaf order.
func workerJobSplitLeafScopes(leaves []WorkerJobRevisionView) []string {
	scopes := make([]string, 0, len(leaves))
	seen := map[string]bool{}
	for _, leaf := range leaves {
		if !seen[leaf.ProjectScope] {
			seen[leaf.ProjectScope] = true
			scopes = append(scopes, leaf.ProjectScope)
		}
	}
	return scopes
}

// workerJobSplitDischargeEvent serializes one split's discharge into its
// closed field map under the event id the fold derives the operation from.
func workerJobSplitDischargeEvent(request WorkflowActionExecutionRequest, actor string, state workerJobSplitState, sources []WorkerJobSplitDischargeSource, expected int64) (Event, error) {
	leaves := workerJobSplitDischargeLeaves(state)
	if len(leaves) == 0 {
		return Event{}, newFailure(KindInvariantViolation, "workflow_action", "the split derives no required leaves", false, "rebuild the worker-job events from the authoritative log")
	}
	resulting := expected + 1
	payload := WorkerJobSplitDischargedPayload{
		WorkflowVersionFields:          WorkflowVersionFields{WorkID: request.WorkID, ExpectedVersion: &expected, ResultingVersion: &resulting},
		workerJobSplitDischargePayload: workerJobSplitDischargePayload{SplitID: state.SplitID, Predecessor: state.Split.Predecessor},
		Leaves:                         leaves,
		Integration:                    sources,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Event{}, wrapFailure(KindInvalidPayload, "workflow_action", "cannot encode the split discharge", false, "report the encoding failure", err)
	}
	return Event{EventID: request.OperationID + ":" + workerJobSplitDischargeSuffix(state.SplitID), Kind: WorkerJobSplitDischarged, SubjectType: SubjectWorkItem, SubjectID: request.WorkID, Actor: actor, OccurredAt: request.Now, PayloadVersion: 1, Payload: raw}, nil
}

// workerJobSplitDischargeLeaves projects the derived state's accepted
// leaves into the record shape the payload carries.
func workerJobSplitDischargeLeaves(state workerJobSplitState) []WorkerJobSplitDischargeLeaf {
	leaves := make([]WorkerJobSplitDischargeLeaf, 0, len(state.Leaves))
	for _, leaf := range state.Leaves {
		leaves = append(leaves, WorkerJobSplitDischargeLeaf{Binding: leaf.Binding, SatisfiedRef: leaf.SatisfiedResultRef})
	}
	return leaves
}

// foldWorkerJobSplitDischarge proves the discharge against the log it folds
// from: the exact split, predecessor binding, and accepted leaves; no
// earlier discharge — the reader already sees this event, so only its own id
// counts; every retained source bound after the anchor and still reading
// green; at least one source the discharging operation minted beside this
// event's actor; and the leaf Project union the retained runs cover. It
// advances the workflow version and appends nothing.
func foldWorkerJobSplitDischarge(ctx context.Context, tx *sql.Tx, event Event) error {
	var payload WorkerJobSplitDischargedPayload
	if err := decodeClosedWorkerPayload(event, &payload); err != nil {
		return err
	}
	splitEvents, err := readWorkerJobSplitEventsTx(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	splitIndex := slices.IndexFunc(splitEvents, func(recorded workerJobSplitEvent) bool { return recorded.eventID == payload.SplitID })
	if splitIndex < 0 {
		return workerJobSplitDischargeFoldFailure("names split " + payload.SplitID + ", which the work holds no record of")
	}
	if splitEvents[splitIndex].payload.Predecessor != payload.Predecessor {
		return workerJobSplitDischargeFoldFailure("names predecessor job " + payload.Predecessor.JobID + ", which the discharged split did not record")
	}
	suffix := workerJobSplitDischargeSuffix(payload.SplitID)
	if !strings.HasSuffix(event.EventID, ":"+suffix) || strings.TrimSuffix(event.EventID, ":"+suffix) == "" {
		return workerJobSplitDischargeFoldFailure("carries an event id without the producing operation beside the closed suffix of split " + payload.SplitID)
	}
	operationID := strings.TrimSuffix(event.EventID, ":"+suffix)
	states, err := readWorkerJobSplitStatesTx(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	stateIndex := slices.IndexFunc(states, func(state workerJobSplitState) bool { return state.SplitID == payload.SplitID })
	if stateIndex < 0 {
		return workerJobSplitDischargeFoldFailure("holds no derived leaf state for split " + payload.SplitID)
	}
	state := states[stateIndex]
	if state.DischargeID != event.EventID {
		return workerJobSplitDischargeFoldFailure("records a discharge for split " + payload.SplitID + " that is not this event")
	}
	if len(state.Unsatisfied) != 0 {
		return workerJobSplitDischargeFoldFailure("discharges split " + payload.SplitID + " while " + fmt.Sprint(len(state.Unsatisfied)) + " required leaf revision(s) remain unsatisfied")
	}
	if !slices.Equal(payload.Leaves, workerJobSplitDischargeLeaves(state)) {
		return workerJobSplitDischargeFoldFailure("leaf bindings differ from the split's derived accepted leaves")
	}
	registered, err := verifyWorkflowDefinitionPinTx(ctx, tx, nil, event.SubjectID)
	if err != nil {
		return err
	}
	var currentStep string
	if err := tx.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, event.SubjectID).Scan(&currentStep); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot read the discharge anchor's workflow step", true, "retry once the workflow projection is readable", err)
	}
	anchorSeq, anchorAt, err := workerJobSplitCoverageAnchor(ctx, tx, event.SubjectID, registered.Definition, currentStep, state.Leaves)
	if err != nil {
		return err
	}
	operationPrefix := operationID + ":semantic:"
	sameOperation := false
	covered := map[string]bool{}
	for _, source := range payload.Integration {
		actorRef, bindingSeq, raw := "", int64(0), ""
		err := tx.QueryRowContext(ctx, `SELECT actor,seq,payload FROM domain_events WHERE event_id=? AND subject_type=? AND subject_id=? AND kind=? AND seq<?`, source.BindingEventID, string(SubjectWorkItem), event.SubjectID, WorkflowEvidenceBound, event.Seq).Scan(&actorRef, &bindingSeq, &raw)
		if err == sql.ErrNoRows {
			return workerJobSplitDischargeFoldFailure("names source binding " + workflowRefExcerpt(source.BindingEventID) + ", which the log holds no earlier record of")
		}
		if err != nil {
			return wrapFailure(KindUnavailable, "fold_event", "cannot read the discharge's source binding", true, "retry once the event log is readable", err)
		}
		if bindingSeq <= anchorSeq {
			return workerJobSplitDischargeFoldFailure("retains source binding " + workflowRefExcerpt(source.BindingEventID) + ", which the log bound at or before the split's integration anchor")
		}
		var binding workflowEvidenceBoundPayload
		if json.Unmarshal([]byte(raw), &binding) != nil || binding.EvidenceKind != string(EvidenceVerification) || binding.ImmutableSubjectRef != source.OperationRef {
			return workerJobSplitDischargeFoldFailure("names source binding " + workflowRefExcerpt(source.BindingEventID) + ", which binds no retained verification run")
		}
		command, project, why, loadErr := loadWorkflowVerifyRun(ctx, tx, event.SubjectID, source.OperationRef, anchorAt, "fold_event")
		if loadErr != nil {
			return loadErr
		}
		if why != "" {
			return workerJobSplitDischargeFoldFailure("retains source binding " + workflowRefExcerpt(source.BindingEventID) + " whose verify run does not qualify: " + why)
		}
		if project != source.ProjectID || !slices.Equal(command, source.Command) {
			return workerJobSplitDischargeFoldFailure("retains a run different from the one source binding " + workflowRefExcerpt(source.BindingEventID) + " cites")
		}
		covered[project] = true
		if strings.HasPrefix(source.BindingEventID, operationPrefix) && actorRef == event.Actor {
			sameOperation = true
		}
	}
	if !sameOperation {
		return workerJobSplitDischargeFoldFailure("retains no source binding the discharging operation minted under its own actor")
	}
	if !workflowIntegrationCovered(workerJobSplitLeafScopes(state.Leaves), covered) {
		return workerJobSplitDischargeFoldFailure("retains no qualifying run for every required Project of its leaves")
	}
	return advanceWorkflowVersion(ctx, tx, event, payload.WorkflowVersionFields)
}

// validateWorkerJobSplitDischargedPayload re-proves the discharge's closed
// shape: the derived leaves are present and every run appears once.
func validateWorkerJobSplitDischargedPayload(event Event, payload WorkerJobSplitDischargedPayload) error {
	if err := workflowBase(event, payload.WorkflowVersionFields); err != nil {
		return err
	}
	if len(payload.Leaves) == 0 {
		return invalidWorkerPayload("worker.job_split_discharged must carry its derived required leaves")
	}
	if len(payload.Integration) == 0 {
		return invalidWorkerPayload("worker.job_split_discharged must carry at least one qualifying verification binding")
	}
	bindings := map[string]bool{}
	runs := map[string]bool{}
	for _, source := range payload.Integration {
		if bindings[source.BindingEventID] {
			return invalidWorkerPayload("worker.job_split_discharged carries source binding " + workflowRefExcerpt(source.BindingEventID) + " twice")
		}
		if runs[source.OperationRef] {
			return invalidWorkerPayload("worker.job_split_discharged carries the run " + workflowRefExcerpt(source.OperationRef) + " through two bindings")
		}
		bindings[source.BindingEventID] = true
		runs[source.OperationRef] = true
	}
	return nil
}
