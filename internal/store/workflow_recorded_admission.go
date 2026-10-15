package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// workflowRecordedCheckpointPins is the closed archived-pin set the recorded
// checkpoint admission serves. Live admission never reads it, and it is never
// widened.
var workflowRecordedCheckpointPins = map[recordedCorrectionPin]bool{
	{"workflow.implementation", 12, "sha256:26340249dcc709a625db60711bc980acb2c8bdfb8be1588b1b1b80839b8d6f33"}: true,
	{"workflow.break_fix", 12, "sha256:ad0d90e5e531160fea75094a38dcbac00b4ca33bafdc346ea02ccfe2fe44f7d2"}:      true,
}

// workflowRecordedRecoveryPins is the closed archived-pin set the recorded
// recovery-binding admission serves, keyed by the completion payload version
// whose producer window registered each definition version. Live admission and
// current payloads never read it, and it is never widened.
var workflowRecordedRecoveryPins = map[int]map[recordedCorrectionPin]bool{
	2: {
		{"workflow.implementation", 10, "sha256:9335bd4830c5592240e578528582a26b51702f3f6d0b4f20916c47267fd160ac"}: true,
		{"workflow.implementation", 16, "sha256:ac13f85d1e460c3f54be49e3550aeac564e510ed66db524ee10f619bd2ea05cc"}: true,
		{"workflow.break_fix", 11, "sha256:2c13955de3099ab12072d1cc450f2fe37a99027903facc11690ad1ea42d2d7a8"}:      true,
		{"workflow.break_fix", 13, "sha256:c11469369d6aa689997e0f3c801938fd05ee47d7b1eac37e46de91e26eba1573"}:      true,
		{"workflow.break_fix", 15, "sha256:dc4b081253f694fdd4383792b762b0b03517fa84256f04219c0a6fa15156162c"}:      true,
	},
	3: {
		{"workflow.break_fix", 14, "sha256:c89bc73749f03062931776ffb4ba4cbdcbaae023cf0b9d8be3dd841885dbef36"}:      true,
		{"workflow.break_fix", 18, "sha256:9b355f4af052d0f48c71d0616177f8b0c2b0cd56bfdfda57a92ea8af46cfaad1"}:      true,
		{"workflow.implementation", 21, "sha256:0a6ed9d4f03954f8d8802b4c953cbec7338860f3552aa83bac4b97fcf469c901"}: true,
	},
}

// workflowRecordedInstancePin reads the stored definition triple through the
// caller's transaction. The fold-verified registration tolerates replay digest
// drift, so a recorded admission compares this tuple against its policy map,
// never the registration it returns.
func workflowRecordedInstancePin(ctx context.Context, q queryer, workID string) (recordedCorrectionPin, error) {
	var pin recordedCorrectionPin
	err := q.QueryRowContext(ctx, `SELECT definition_ref,definition_version,definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&pin.ref, &pin.version, &pin.digest)
	if err != nil {
		if err == sql.ErrNoRows {
			return recordedCorrectionPin{}, newFailure(KindInvariantViolation, "fold_event", "workflow definition pin is missing", false, "reread_entities")
		}
		return recordedCorrectionPin{}, wrapFailure(KindUnavailable, "fold_event", "cannot read the workflow definition pin", true, "retry once the database is readable", err)
	}
	return pin, nil
}

// workflowRecordedCheckpointAdmission re-admits, during replay only, an
// original payload-v1 checkpoint whose stored prefix proves the archived
// executor-equality policy: the strictly latest matching-step start is a typed
// fenced external-effect start declared on the step by the checkpoint's own
// actor, it follows the latest dispatch and that dispatch's recorded disposal,
// no lane attempt is in flight, and no newer start or dispatch authorization
// intervenes. Every other shape keeps the fold's refusal.
func workflowRecordedCheckpointAdmission(ctx context.Context, tx *sql.Tx, event Event, p workflowActionCheckpointedPayload, definition WorkflowDefinition, currentStep, executionActor string) (bool, error) {
	if !isWorkflowReplay(ctx) || event.Seq <= 0 || event.PayloadVersion != 1 || event.replaySourcePayloadVersion != 0 {
		return false, nil
	}
	var inFlight int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM worker_attempts WHERE work_id=? AND lifecycle_state IN ('in_flight','dispatched')`, event.SubjectID).Scan(&inFlight); err != nil {
		return false, workflowProjectionError(err, "cannot inspect in-flight worker attempts")
	}
	if inFlight != 0 {
		return false, nil
	}
	pin, err := workflowRecordedInstancePin(ctx, tx, event.SubjectID)
	if err != nil {
		return false, err
	}
	if !workflowRecordedCheckpointPins[pin] || BuiltinWorkflowRegistry().Verify(pin.ref, pin.version, pin.digest) != nil {
		return false, nil
	}
	step := workflowStep(definition, currentStep)
	if step == nil || p.StepID != currentStep || p.StepKind != string(step.Kind) || executionActor == "" || p.ActorRef != executionActor {
		return false, nil
	}
	var startSeq int64
	var startActor string
	var startRaw []byte
	err = tx.QueryRowContext(ctx, `SELECT seq,actor,payload FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND seq<? AND json_extract(payload,'$.step_id')=? ORDER BY seq DESC LIMIT 1`, string(SubjectWorkItem), event.SubjectID, WorkflowActionStarted, event.Seq, p.StepID).Scan(&startSeq, &startActor, &startRaw)
	if err != nil && err != sql.ErrNoRows {
		return false, workflowProjectionError(err, "cannot inspect the recorded workflow step start")
	}
	if err == sql.ErrNoRows {
		return false, nil
	}
	var start workflowActionStartedPayload
	if err := json.Unmarshal(startRaw, &start); err != nil {
		return false, newFailure(KindInvariantViolation, "fold_event", "workflow action start payload is malformed", false, "rebuild workflow projections from the event log")
	}
	mode, declared := workflowActionExecutionMode(definition, start.ActionID)
	if !declared || mode != ActionFenced || workflowActionConsequence(definition, start.ActionID) != ActionExternalEffect || !stepDeclaresAction(definition, p.StepID, start.ActionID) ||
		start.AttemptEpoch != p.AttemptEpoch || start.ActorRef != startActor || start.ActorRef != p.ActorRef {
		return false, nil
	}
	var newerStarts int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND seq>? AND seq<?`, string(SubjectWorkItem), event.SubjectID, WorkflowActionStarted, startSeq, event.Seq).Scan(&newerStarts); err != nil {
		return false, workflowProjectionError(err, "cannot inspect newer workflow action starts")
	}
	if newerStarts != 0 {
		return false, nil
	}
	// A dispatch records its started event beside its own completed
	// authorization, so this gate also keeps a dispatch start from posing as
	// the explicit start.
	var newerAuthorizations int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND seq>? AND seq<? AND json_extract(payload,'$.action_id')='dispatch_worker'`, string(SubjectWorkItem), event.SubjectID, WorkflowActionCompleted, startSeq, event.Seq).Scan(&newerAuthorizations); err != nil {
		return false, workflowProjectionError(err, "cannot inspect newer workflow dispatch authorizations")
	}
	if newerAuthorizations != 0 {
		return false, nil
	}
	var dispatchSeq int64
	var attemptID string
	err = tx.QueryRowContext(ctx, `SELECT seq,json_extract(payload,'$.attempt_id') FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND seq<? ORDER BY seq DESC LIMIT 1`, string(SubjectWorkItem), event.SubjectID, WorkerDispatched, event.Seq).Scan(&dispatchSeq, &attemptID)
	if err != nil && err != sql.ErrNoRows {
		return false, workflowProjectionError(err, "cannot inspect the recorded worker dispatch")
	}
	if err == sql.ErrNoRows || dispatchSeq >= startSeq {
		return false, nil
	}
	var disposed int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND seq>? AND seq<? AND json_extract(payload,'$.action_id') IN ('accept_worker_result','accept_worker_evidence','reject_worker_result','record_worker_failure') AND json_extract(payload,'$.worker_attempt_id')=?`, string(SubjectWorkItem), event.SubjectID, WorkflowActionCompleted, dispatchSeq, startSeq, attemptID).Scan(&disposed); err != nil {
		return false, workflowProjectionError(err, "cannot inspect the recorded worker attempt disposal")
	}
	return disposed != 0, nil
}

// workflowRecordedRecoveryBindingAdmission re-admits, during replay only, a
// payload-v2/v3 bind_evidence completion whose own preceding binding satisfied
// a declared contract requirement, where the outstanding filter refuses it only
// because an earlier binding of the same kind already completed. The pinned
// current step must follow the definition's first binding step. A missing
// active contract and every other refusal keep the fold's denial.
func workflowRecordedRecoveryBindingAdmission(ctx context.Context, tx *sql.Tx, event Event, p workflowActionCompletedPayload, definition WorkflowDefinition, currentStep string, binding workflowEvidenceBoundPayload) (bool, error) {
	if !isWorkflowReplay(ctx) || event.Seq <= 0 || p.ActionID != "bind_evidence" || event.replaySourcePayloadVersion != 2 && event.replaySourcePayloadVersion != 3 {
		return false, nil
	}
	pins, mapped := workflowRecordedRecoveryPins[event.replaySourcePayloadVersion]
	if !mapped {
		return false, nil
	}
	pin, err := workflowRecordedInstancePin(ctx, tx, event.SubjectID)
	if err != nil {
		return false, err
	}
	if !pins[pin] || BuiltinWorkflowRegistry().Verify(pin.ref, pin.version, pin.digest) != nil {
		return false, nil
	}
	if !contains(p.ResultEvidenceRefs, binding.ImmutableSubjectRef) {
		return false, nil
	}
	bindingStep := workflowEvidenceBindingStep(definition, "")
	if bindingStep == "" || !workflowStepFollows(definition, bindingStep, currentStep) {
		return false, nil
	}
	if _, err := activeWorkflowContractVersion(ctx, tx, event.SubjectID, "workflow_action"); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	required, mandates, obligations, _, err := workflowEvidenceRequirementInputs(ctx, tx, event.SubjectID)
	if err != nil {
		return false, err
	}
	for _, requirement := range workflowEvidenceRequirementDefinitions(required, mandates, definition, obligations) {
		if (requirement.Kind == "" || requirement.Kind == binding.EvidenceKind) && (requirement.Reference == "" || requirement.Reference == binding.ImmutableSubjectRef) {
			return true, nil
		}
	}
	return false, nil
}
