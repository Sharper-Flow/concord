package store

import (
	"fmt"
	"testing"
)

func TestWorkerContractCorrectionDispatchWithoutLog(t *testing.T) {
	for _, kind := range []string{WorkerDispatched, WorkflowActionStarted, WorkflowActionCompleted} {
		for _, seq := range []int64{9, 10, 11} {
			t.Run(fmt.Sprintf("%s/%d", kind, seq), func(t *testing.T) {
				db := workerDisposalCurrentDB(t)
				if _, err := db.Exec(`
CREATE TABLE workflow_contracts(work_id TEXT,superseded_by INTEGER);
INSERT INTO workflow_contracts VALUES('work',NULL);
CREATE TABLE workflow_correction_state(work_id TEXT,state TEXT);
CREATE TABLE worker_job_revisions(work_id TEXT,recorded_payload TEXT);
ALTER TABLE worker_attempts ADD COLUMN terminal_payload TEXT;
ALTER TABLE workflow_step_epochs ADD COLUMN latest_dispatch_start_seq INTEGER NOT NULL DEFAULT 0;
INSERT INTO workflow_step_epochs(work_id,step_id,latest_start_seq,latest_start_event_id,latest_start_payload)
VALUES('work','produce',10,'current-start','{"action_id":"record_worker_job","attempt_epoch":1}');`); err != nil {
					t.Fatal(err)
				}
				var query string
				switch kind {
				case WorkerDispatched:
					query = `UPDATE worker_attempts SET dispatch_seq=? WHERE work_id='work'`
				case WorkflowActionCompleted:
					query = `UPDATE worker_authorizations SET seq=? WHERE work_id='work'`
				case WorkflowActionStarted:
					query = `INSERT INTO workflow_step_epochs(work_id,step_id,latest_dispatch_start_seq) VALUES('work','elsewhere',?)`
				}
				if _, err := db.Exec(query, seq); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`INSERT INTO domain_events VALUES(?,'work_item','work',?,'{"action_id":"dispatch_worker"}')`, seq, kind); err != nil {
					t.Fatal(err)
				}
				definition := WorkflowDefinition{StepGraph: WorkflowStepGraph{Steps: []WorkflowStep{{ID: "produce", Actions: []string{"dispatch_worker"}}}}}
				disposalWithoutLog(t, db, func() {
					got, err := workflowContractCorrectionAvailable(t.Context(), db, "work", definition, "produce", "test")
					if err != nil || got != (seq < 10) {
						t.Fatalf("correction available=%v err=%v; want %v", got, err, seq < 10)
					}
				})
			})
		}
	}
}
