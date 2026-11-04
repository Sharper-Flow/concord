package store

import (
	"encoding/json"
	"path/filepath"
	"strings"
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
	if string(payload["workflow_ref"]) != `"research"` {
		t.Fatalf("checkpoint identity: %s", events[0].Payload)
	}
}

func TestWorkflowPlanContinuityMigrationPreservesHistory(t *testing.T) {
	ctx := t.Context()
	index := -1
	for i, m := range migrations {
		if m.Version == 183 {
			index = i
			break
		}
	}
	if index < 0 {
		t.Fatal("migration 183 missing")
	}
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "pre183.db"), index)
	digest := "sha256:" + strings.Repeat("a", 64)
	_, err := db.ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1);
INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('work:history','task','history','needed',0,2,'now','now');
INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?2,'principal:test','client:test','agent:test','session:test','operator','now');
INSERT INTO workflow_context_checkpoints(work_id,work_version,checkpoint_sequence,checkpoint_id,step_id,attempt_epoch,active_unit,hypothesis,diagnosis,strategy,touched_refs,evidence_refs,pending_questions,pending_decisions,workflow_ref,workflow_definition_digest,actor_ref,request_id,recorded_at) VALUES('work:history',1,1,'checkpoint:history','step:history',1,'unit','hypothesis','diagnosis','strategy','["path:test"]','["evidence:test"]','[]','[]','workflow.legacy',?1,?2,'request:test','now');
INSERT INTO workflow_context_boundaries(work_id,work_version,boundary_sequence,boundary_count,boundary_id,boundary_kind,checkpoint_id,checkpoint_sequence,attempt_epoch,summary,workflow_ref,workflow_definition_digest,actor_ref,request_id,recorded_at) VALUES('work:history',2,1,1,'boundary:history','summary','checkpoint:history',1,1,'original summary','workflow.legacy',?1,?2,'request:test','now');
DELETE FROM write_guard;`, digest, "actor:"+strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := applyMigration(ctx, db, migrations[index]); err != nil {
		t.Fatal(err)
	}
	var hypothesis, checkpointRef, summary, boundaryRef string
	err = db.QueryRowContext(ctx, `SELECT c.hypothesis,c.workflow_ref,b.summary,b.workflow_ref FROM workflow_context_checkpoints c JOIN workflow_context_boundaries b ON b.work_id=c.work_id AND b.checkpoint_id=c.checkpoint_id WHERE c.work_id='work:history'`).Scan(&hypothesis, &checkpointRef, &summary, &boundaryRef)
	if err != nil || hypothesis != "hypothesis" || summary != "original summary" || checkpointRef != "workflow.legacy" || boundaryRef != checkpointRef {
		t.Fatalf("history: %q %q %q %q: %v", hypothesis, checkpointRef, summary, boundaryRef, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE workflow_context_boundaries SET summary='changed' WHERE work_id='work:history'`); err == nil {
		t.Fatal("migration lost boundary immutability")
	}
}
