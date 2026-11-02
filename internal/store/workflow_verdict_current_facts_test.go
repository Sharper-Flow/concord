package store

import (
	"context"
	"testing"
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
