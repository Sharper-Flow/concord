package store

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"testing"
)

// Exercise the correction-fact producer and the historical walk over the same
// authored job descriptors. Neither route reconstructs events from its output.
func assertDuplicateRepairCorpus142(t *testing.T) {
	t.Helper()
	const work = "duplicate-repair-corpus"
	c := evidenceCorpusForWork(t, work)
	original := WorkerJobRecordedPayload{JobID: "job:corpus-repair", Revision: 1, ContractVersion: 1, Objective: "Repair the declared checks", StoppingCondition: "The declared check executes", Checks: []string{"concord check --original-checks --exit-code=never"}}
	original.Digest = DeriveWorkerJobDigest(original)
	first := WorkerJobBinding{JobID: original.JobID, Revision: 1, Digest: original.Digest}
	repaired := original
	repaired.Revision = 2
	repaired.Checks = []string{"go test ./internal/store/ -run TestChecksRepair"}
	repaired.ChecksRepair = &WorkerJobChecksRepair{Reason: "The original command does not exist", Revisions: []WorkerJobChecksRepairEntry{{WorkerJob: first, Checks: original.Checks}}}
	repaired.Digest = DeriveWorkerJobDigest(repaired)
	repair := WorkerJobBinding{JobID: repaired.JobID, Revision: 2, Digest: repaired.Digest}
	const confirming = "attempt:corpus-confirm"
	c.events = []Event{
		corpusEvent("repair-corpus:original", WorkerJobRecorded, work, original),
		corpusEvent("repair-corpus:repair", WorkerJobRecorded, work, repaired),
		corpusEvent("repair-corpus:accept", WorkflowActionCompleted, work, map[string]any{"action_id": "accept_worker_result", "step_id": "repair", "worker_attempt_id": "attempt:corpus-repair", "worker_job": repair}),
		corpusEvent("repair-corpus:confirm", WorkflowActionCompleted, work, map[string]any{"action_id": "dispatch_worker", "step_id": "repair", "worker_attempt_id": confirming, "worker_job": repair, "checks_repair_review": WorkerChecksRepairReviewAdmission{WorkerJob: repair, RepairingAcceptanceSeq: 3}}),
		corpusEvent("repair-corpus:duplicate", WorkerJobRecorded, work, repaired),
	}
	parents := c.parents
	c.parents = func(t *testing.T, s *Store, guard string) {
		parents(t, s, guard)
		if _, err := s.db.Exec(`INSERT INTO ` + guard + ` VALUES(1)`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`DELETE FROM workflow_step_epochs WHERE work_id=?`, work); err != nil {
			t.Fatal(err)
		}
		for _, job := range []WorkerJobRecordedPayload{original, repaired} {
			if _, err := s.db.Exec(`INSERT INTO worker_job_revisions(work_id,job_id,revision,contract_version,objective,stopping_condition,project_scope,path_scope,predicate_ids,checks,prerequisites,unresolved_refs,reserved_integration,digest,state,recorded_at,recorded_payload) VALUES(?,?,?,?,?,?,'','[]','[]',?,'[]','[]','',?,'recorded','2026-01-01T00:00:00Z',?)`, work, job.JobID, job.Revision, job.ContractVersion, job.Objective, job.StoppingCondition, string(mustJSONValue(job.Checks)), job.Digest, string(mustJSONValue(job))); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.db.Exec(`DELETE FROM ` + guard); err != nil {
			t.Fatal(err)
		}
	}
	c.write = func(ctx context.Context, tx *sql.Tx, e Event) error {
		return advanceWorkflowCorrectionFactsTx(ctx, tx, e)
	}
	for _, duplicate := range []bool{false, true} {
		t.Run(fmt.Sprintf("duplicate=%t", duplicate), func(t *testing.T) {
			journey := c
			if !duplicate {
				journey.events = journey.events[:4]
			}
			live := journey.current(t)
			old := journey.prefix(t, 141)
			legacy, err := readWorkflowCorrectionStateForMigration(t.Context(), old.db, work, int64(1)<<62, "migration-fixture")
			if err != nil {
				t.Fatal(err)
			}
			want := &workflowChecksRepairState{binding: repair, namedKeys: []string{workerJobKey(first.JobID, first.Revision)}, contractVersion: 1}
			if !duplicate {
				want.acceptanceSeq, want.confirmAttempt = 3, confirming
			}
			key := workerJobKey(repair.JobID, repair.Revision)
			if !reflect.DeepEqual(legacy.checksRepairs[key], want) {
				t.Fatalf("legacy repair = %+v, want %+v", legacy.checksRepairs[key], want)
			}
			if err := Migrate(t.Context(), old.db); err != nil {
				t.Fatal(err)
			}
			for _, s := range []*Store{live, old} {
				h, err := readWorkflowCorrectionFacts(t.Context(), s.db, work, "migration-fixture")
				if err != nil || !reflect.DeepEqual(h.checksRepairs[key], want) {
					t.Fatalf("current repair = %+v, want %+v, err %v", h, want, err)
				}
			}
			query := []string{`SELECT json_array(work_id,state) FROM workflow_correction_state WHERE work_id='duplicate-repair-corpus'`}
			if got, want := workerFactsQueryRows(t, old, query), workerFactsQueryRows(t, live, query); !reflect.DeepEqual(got, want) {
				t.Fatalf("correction state: migrated %v current %v", got, want)
			}
		})
	}
}
