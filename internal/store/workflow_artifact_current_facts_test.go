package store

import (
	"reflect"
	"testing"
)

func TestWorkflowArtifactBackfillPreservesPostVerdictProduction(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	const work = "post-verdict-production"
	definition := mustBuiltinDefinition(t, "workflow.research").Definition
	pinWorkflowAtStep(t, s, work, definition.Ref, "conclude")
	stalenessSeedContract(t, s, work)
	stalenessEvent(t, s, work, WorkflowVerdictRecorded, stalenessVerdictPayload("outcome_mismatch"))
	stalenessEvent(t, s, work, WorkflowActionCompleted, stalenessActionPayload("late-producer", "record_finding"))
	queries := []string{`SELECT json_array(work_id,step_id,action_id,production_seq,verdict_seq,verdict_production_seq) FROM workflow_artifact_actions ORDER BY work_id,step_id,action_id`}
	want := workerFactsQueryRows(t, s, queries)
	assertWorkflowCurrentFactsWithIndependentMigration142(t, s)
	if got := workerFactsQueryRows(t, s, queries); !reflect.DeepEqual(got, want) {
		t.Fatalf("backfill changed post-verdict production: got %v, want %v", got, want)
	}
}
