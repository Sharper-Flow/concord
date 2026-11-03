package store

import (
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

func TestWorkflowPlanConstructorIdentity(t *testing.T) {
	ctx := t.Context()
	db, err := sqlitedb.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `CREATE TABLE workflow_instances(work_id TEXT PRIMARY KEY, workflow_type TEXT);
CREATE TABLE durable_operations(work_id TEXT, attempt_epoch INTEGER);
INSERT INTO workflow_instances VALUES('research-work','research'),('change-work','change');`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		work     string
		research bool
	}{{"research-work", true}, {"change-work", false}} {
		got, err := workflowResearchCriterionFamily(ctx, db, tc.work)
		if err != nil || got != tc.research {
			t.Fatalf("%s: research=%v err=%v", tc.work, got, err)
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	request := WorkflowActionExecutionRequest{WorkID: "research-work", OperationID: "op", RequestID: "request"}
	events, err := workflowContextCheckpointEvents(ctx, tx, request, "research", "actor", nil, "event", 1)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if string(payload["workflow_ref"]) != `"research"` || string(payload["workflow_definition_digest"]) != `""` {
		t.Fatalf("checkpoint identity: %s", events[0].Payload)
	}
}
