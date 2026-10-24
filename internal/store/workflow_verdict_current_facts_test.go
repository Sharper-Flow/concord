package store

import (
	"context"
	"testing"
	"time"
)

func TestWorkflowVerdictCurrentFactsWithoutLog(t *testing.T) {
	const work = "verdict-current-facts"
	s, _ := seedCompletionGateCase(t, work, completionGateCase{includeVerdict: true, requiredEvidence: []string{"verification", "review"}})
	defer s.Close()
	ctx := context.Background()
	version, err := activeWorkflowContractVersion(ctx, s.db, work, "workflow_action")
	if err != nil {
		t.Fatal(err)
	}
	verdicts, err := latestWorkflowVerdicts(ctx, s.db, work, version)
	if err != nil || len(verdicts) == 0 {
		t.Fatalf("nonempty verdict fixture: %+v, %v", verdicts, err)
	}
	assertWorkerFactsLogBlind(t, s, map[string]func() (any, error){
		"G27-latest-verdict":     func() (any, error) { return latestWorkflowVerdict(ctx, s.db, work) },
		"G27-predicate-verdicts": func() (any, error) { return latestWorkflowVerdicts(ctx, s.db, work, version) },
		"G27-verdict-sequences":  func() (any, error) { return workflowLatestVerdictSequences(ctx, s.db, work, version, verdicts) },
	})
}

func TestWorkflowContradictionCurrentFactsWithoutLog(t *testing.T) {
	const work = "contradiction-current-facts"
	s, _ := seedCompletionGateCase(t, work, completionGateCase{includeVerdict: true, requiredEvidence: []string{"verification", "review"}})
	defer s.Close()
	ctx := context.Background()
	observation := Event{EventID: "contradiction-observation", Kind: WorkObservationRecorded, SubjectType: SubjectWorkItem, SubjectID: work,
		Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1,
		Payload: mustJSONValue(workObservationRecordedPayload{ObservationID: "obs:0000000000000001", Statement: "The delivered result contradicts the accepted premise.", Refs: []string{}, Tags: []string{}})}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{observation}}); err != nil {
		t.Fatal(err)
	}
	found, err := workflowContradictionObservationRecorded(ctx, s.db, work, "workflow_action", 0)
	if err != nil || !found {
		t.Fatalf("new observation must postdate the verdict regardless of timestamp: %v, %v", found, err)
	}
	verdict, err := latestWorkflowVerdict(ctx, s.db, work)
	if err != nil || verdict == nil {
		t.Fatalf("latest verdict fixture: %+v, %v", verdict, err)
	}
	version := readWorkVersion(t, s, work)
	resultingVersion := version + 1
	verdict.ExpectedVersion, verdict.ResultingVersion = &version, &resultingVersion
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{{EventID: "contradiction-new-verdict", Kind: WorkflowVerdictRecorded,
		SubjectType: SubjectWorkItem, SubjectID: work, Actor: verdict.VerdictActorRef, OccurredAt: time.Unix(0, 0).UTC(),
		PayloadVersion: CurrentEventPayloadVersion(WorkflowVerdictRecorded), Payload: mustJSONValue(verdict)}}}); err != nil {
		t.Fatal(err)
	}
	found, err = workflowContradictionObservationRecorded(ctx, s.db, work, "workflow_action", 0)
	if err != nil || found {
		t.Fatalf("older observation must not contradict a later verdict: %v, %v", found, err)
	}
	assertWorkerFactsLogBlind(t, s, map[string]func() (any, error){
		"G25-contradiction-observation": func() (any, error) {
			return workflowContradictionObservationRecorded(ctx, s.db, work, "workflow_action", 0)
		},
	})
}
