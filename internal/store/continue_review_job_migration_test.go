package store

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func continueReviewMigrationJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func continueReviewMigrationReceipt(t *testing.T, db *sql.DB, seq int, id, kind, raw string) {
	t.Helper()
	digest, err := effectPayloadDigest([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	memberMigrationExec(t, db, `INSERT INTO effect_receipts(effect_seq,event_id,kind,subject_type,subject_id,actor,payload_version,payload_digest) VALUES(?,?,?,'work_item','continue-work','main',1,?)`, seq, id, kind, digest)
}

// Receipts cannot be updated, even under write_guard. Seed a replacement
// through the fixture's guarded delete/insert seams, preserving every other
// receipt column so each refusal tests only its named conflict.
func continueReviewMigrationReplaceReceipt(t *testing.T, db *sql.DB, id, column string, value any) {
	t.Helper()
	rows, err := db.Query(`SELECT * FROM effect_receipts WHERE event_id=?`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	if !rows.Next() {
		t.Fatalf("receipt %q missing: %v", id, rows.Err())
	}
	values, targets := make([]any, len(columns)), make([]any, len(columns))
	for index := range values {
		targets[index] = &values[index]
	}
	if err := rows.Scan(targets...); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	found := false
	for index, name := range columns {
		if name == column {
			values[index], found = value, true
		}
	}
	if !found {
		t.Fatalf("receipt column %q missing", column)
	}
	memberMigrationExec(t, db, `DELETE FROM effect_receipts WHERE event_id=?`, id)
	memberMigrationExec(t, db, `INSERT INTO effect_receipts("`+strings.Join(columns, `","`)+`") VALUES(`+strings.TrimSuffix(strings.Repeat("?,", len(columns)), ",")+`)`, values...)
}

func continueReviewMigrationSeed(t *testing.T) *sql.DB {
	t.Helper()
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "continue-review.db"), 157)
	memberMigrationExec(t, db, `INSERT INTO write_guard(active) VALUES(1)`)
	memberMigrationExec(t, db, `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('continue-work','task','Continue Review recovery','in_progress',0,1,'before','before')`)
	memberMigrationExec(t, db, `INSERT INTO workflow_instances(work_id,definition_ref,definition_digest,current_step,instance_state) VALUES('continue-work','workflow.break_fix',?,'repair','running')`, "sha256:"+strings.Repeat("a", 64))
	job := WorkerJobRecordedPayload{
		JobID: "job:continue", Revision: 1, ContractVersion: 1, Objective: "Implement the repair", StoppingCondition: "Repair verified",
		PathScope: []string{"internal/store"}, PredicateIDs: []string{}, Checks: []string{"go test"}, Prerequisites: []WorkerJobPrerequisite{}, UnresolvedRefs: []string{},
	}
	job.Digest = DeriveWorkerJobDigest(job)
	binding := &WorkerJobBinding{JobID: job.JobID, Revision: job.Revision, Digest: job.Digest}
	memberMigrationExec(t, db, `INSERT INTO worker_job_revisions(work_id,job_id,revision,contract_version,objective,stopping_condition,path_scope,predicate_ids,checks,prerequisites,unresolved_refs,digest,state,recorded_at,satisfied_at,satisfied_result_ref,satisfied_seq,recorded_payload,recorded_seq,recorded_event_id)
VALUES('continue-work',?,1,1,?,?,'["internal/store"]','[]','["go test"]','[]','[]',?,'satisfied','recorded','accepted','accept',40,?,5,'job-recorded')`, job.JobID, job.Objective, job.StoppingCondition, job.Digest, continueReviewMigrationJSON(t, job))
	auth := workflowActionCompletedPayload{ActionID: "dispatch_worker", StepID: "repair", WorkerAttemptID: "attempt:continue", WorkerCapabilityClass: "review", WorkerJob: binding}
	dispatch := WorkerDispatchedPayload{AttemptID: "attempt:continue", CapabilityClass: "review", WorkerJob: binding}
	report := WorkerReviewBlock{Disposition: "continue", InputCandidate: WorkerReviewCandidate{Commit: strings.Repeat("b", 40)}, OutputCandidate: WorkerReviewCandidate{Commit: strings.Repeat("b", 40)}, Dependencies: WorkerReviewDependencies{Criteria: []WorkerReviewCriterion{}, ArchitectureNotApplicable: true}, Assessments: []WorkerReviewAssessment{}, Findings: []WorkerReviewFinding{}, RemainingObligations: []WorkerReviewObligation{}}
	terminal := WorkerCompletedPayload{AttemptID: "attempt:continue", WorkerJob: binding, Review: &report}
	accept := workflowActionCompletedPayload{ActionID: "accept_worker_result", StepID: "repair", WorkerAttemptID: "attempt:continue", WorkerJob: binding}
	authRaw, dispatchRaw, terminalRaw, acceptRaw := continueReviewMigrationJSON(t, auth), continueReviewMigrationJSON(t, dispatch), continueReviewMigrationJSON(t, terminal), continueReviewMigrationJSON(t, accept)
	memberMigrationExec(t, db, `INSERT INTO worker_authorizations(work_id,attempt_id,seq,event_id,payload) VALUES('continue-work','attempt:continue',10,'authorize',?)`, authRaw)
	memberMigrationExec(t, db, `INSERT INTO worker_attempts(work_id,attempt_id,lane_id,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,dispatched_at,completed_at,dispatch_payload,dispatch_event_id,dispatch_seq,terminal_payload,terminal_event_id,terminal_kind,terminal_seq)
VALUES('continue-work','attempt:continue','review',?,'review','model:review','2.0','2.0','completed','dispatched','completed',?,'dispatch',20,?,'terminal','worker.completed',30)`, "sha256:"+strings.Repeat("c", 64), dispatchRaw, terminalRaw)
	memberMigrationExec(t, db, `INSERT INTO worker_attempt_dispositions(work_id,attempt_id,action_id,seq,event_id,payload,first_acceptance_seq,first_acceptance_payload) VALUES('continue-work','attempt:continue','accept_worker_result',40,'accept',?,40,?)`, acceptRaw, acceptRaw)
	facts := workflowReviewFacts{Accepted: &WorkflowAcceptedReview{AttemptID: "attempt:continue", ReportRef: "terminal", ReportDigest: reviewDigest(report), ActorRef: "main", Report: report}, Findings: []WorkerReviewFinding{}}
	memberMigrationExec(t, db, `INSERT INTO workflow_review_state(work_id,state) VALUES('continue-work',?)`, continueReviewMigrationJSON(t, facts))
	continueReviewMigrationReceipt(t, db, 10, "authorize", WorkflowActionCompleted, authRaw)
	continueReviewMigrationReceipt(t, db, 20, "dispatch", WorkerDispatched, dispatchRaw)
	continueReviewMigrationReceipt(t, db, 30, "terminal", WorkerCompleted, terminalRaw)
	continueReviewMigrationReceipt(t, db, 40, "accept", WorkflowActionCompleted, acceptRaw)
	return db
}

func continueReviewMigrationSnapshot(t *testing.T, db *sql.DB, includeSatisfaction bool) [][]string {
	t.Helper()
	columns := "*"
	if !includeSatisfaction {
		if err := db.QueryRow(`SELECT group_concat('"'||name||'"',',') FROM pragma_table_info('worker_job_revisions') WHERE name NOT IN ('state','satisfied_at','satisfied_result_ref','satisfied_seq')`).Scan(&columns); err != nil {
			t.Fatal(err)
		}
	}
	queries := []string{`SELECT ` + columns + ` FROM worker_job_revisions ORDER BY work_id,job_id,revision`}
	for _, table := range []string{"worker_authorizations", "worker_attempts", "worker_attempt_dispositions", "workflow_review_state", "effect_receipts", "work_items", "workflow_instances", "workflow_member_groups", "workflow_group_members", "write_guard"} {
		queries = append(queries, "SELECT * FROM "+table+" ORDER BY 1,2")
	}
	// write_guard has only one column.
	queries[len(queries)-1] = "SELECT * FROM write_guard ORDER BY 1"
	out := make([][]string, len(queries))
	for index, query := range queries {
		out[index] = memberMigrationRows(t, db, query)
	}
	return out
}

func TestContinueReviewJobMigration158RestoresOnlySatisfaction(t *testing.T) {
	for _, repeated := range []string{"none", "first-retained", "latest-retained", "unrelated-facts"} {
		t.Run(repeated, func(t *testing.T) {
			db := continueReviewMigrationSeed(t)
			if repeated == "unrelated-facts" {
				memberMigrationExec(t, db, `UPDATE work_items SET lifecycle='needed'`)
				memberMigrationExec(t, db, `INSERT INTO worker_authorizations(work_id,attempt_id,seq,payload) SELECT work_id,'attempt:unrelated',50,json_set(payload,'$.worker_capability_class','implementation','$.worker_job.job_id','job:unrelated') FROM worker_authorizations`)
				memberMigrationExec(t, db, `INSERT INTO worker_job_revisions(work_id,job_id,revision,contract_version,objective,stopping_condition,path_scope,predicate_ids,checks,prerequisites,unresolved_refs,digest,state,recorded_at) SELECT work_id,'job:unrelated',1,contract_version,objective,stopping_condition,path_scope,predicate_ids,checks,'[{"job_id":"job:continue","revision":2}]',unresolved_refs,digest,'recorded',recorded_at FROM worker_job_revisions`)
			}
			if repeated == "first-retained" || repeated == "latest-retained" {
				var raw string
				if err := db.QueryRow(`SELECT payload FROM worker_attempt_dispositions`).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				continueReviewMigrationReceipt(t, db, 50, "accept-again", WorkflowActionCompleted, raw)
				memberMigrationExec(t, db, `UPDATE worker_attempt_dispositions SET seq=50,event_id='accept-again'`)
				if repeated == "latest-retained" {
					memberMigrationExec(t, db, `UPDATE worker_job_revisions SET satisfied_seq=50,satisfied_result_ref='accept-again'`)
				}
			}
			memberMigrationExec(t, db, `DELETE FROM write_guard`)
			before := continueReviewMigrationSnapshot(t, db, false)
			if err := Migrate(t.Context(), db); err != nil {
				t.Fatal(err)
			}
			var state string
			var at, ref sql.NullString
			var seq int64
			if err := db.QueryRow(`SELECT state,satisfied_at,satisfied_result_ref,satisfied_seq FROM worker_job_revisions WHERE job_id='job:continue'`).Scan(&state, &at, &ref, &seq); err != nil || state != "recorded" || at.Valid || ref.Valid || seq != 0 {
				t.Fatalf("satisfaction=(%s,%v,%v,%d), err=%v", state, at, ref, seq, err)
			}
			if after := continueReviewMigrationSnapshot(t, db, false); !reflect.DeepEqual(before, after) {
				t.Fatalf("immutable evidence changed\nbefore=%v\nafter=%v", before, after)
			}
			after := continueReviewMigrationSnapshot(t, db, true)
			if err := Migrate(t.Context(), db); err != nil {
				t.Fatal(err)
			}
			// Also replay the repair itself, not only its manifest fence.
			if err := restoreContinueReviewJobSatisfaction(t.Context(), db); err != nil {
				t.Fatal(err)
			}
			if again := continueReviewMigrationSnapshot(t, db, true); !reflect.DeepEqual(after, again) {
				t.Fatal("upgrade or repair replay changed the restored state")
			}
		})
	}
}

func TestContinueReviewJobMigration158RefusesUnprovedRestoration(t *testing.T) {
	dependent := `INSERT INTO worker_job_revisions(work_id,job_id,revision,contract_version,objective,stopping_condition,path_scope,predicate_ids,checks,prerequisites,unresolved_refs,digest,state,recorded_at) SELECT work_id,'job:dependent',1,contract_version,objective,stopping_condition,path_scope,predicate_ids,checks,`
	cases := []struct{ name, sql string }{
		{"completed-work", `UPDATE work_items SET lifecycle='completed'`},
		{"cancelled-work", `UPDATE work_items SET lifecycle='cancelled'`},
		{"superseded-work", `UPDATE work_items SET lifecycle='superseded'`},
		{"completed-workflow", `UPDATE workflow_instances SET instance_state='completed'`},
		{"cancelled-workflow", `UPDATE workflow_instances SET instance_state='cancelled'`},
		{"superseded-workflow", `UPDATE workflow_instances SET instance_state='superseded'`},
		{"outside-repair", `UPDATE workflow_instances SET instance_state='outside_repair'`},
		{"missing-workflow", `DELETE FROM workflow_instances`},
		{"missing-current-review", `DELETE FROM workflow_review_state`},
		{"different-current-attempt", `UPDATE workflow_review_state SET state=json_set(state,'$.accepted.attempt_id','attempt:other')`},
		{"different-report-reference", `UPDATE workflow_review_state SET state=json_set(state,'$.accepted.report_ref','other-terminal')`},
		{"different-report-digest", `UPDATE workflow_review_state SET state=json_set(state,'$.accepted.report_digest','other-digest')`},
		{"different-report-content", `UPDATE workflow_review_state SET state=json_set(state,'$.accepted.report.remaining_obligations',json('[{"obligation_id":"changed"}]'))`},
		{"different-accepting-actor", `UPDATE workflow_review_state SET state=json_set(state,'$.accepted.actor_ref','other-main')`},
		{"ready-review", `UPDATE workflow_review_state SET state=json_set(state,'$.accepted.report.disposition','ready_for_main')`},
		{"blocking-review", `UPDATE workflow_review_state SET state=json_set(state,'$.accepted.report.disposition','blocked')`},
		{"missing-authorization", `DELETE FROM worker_authorizations`},
		{"missing-terminal", `UPDATE worker_attempts SET terminal_payload=NULL`},
		{"missing-dispatch", `UPDATE worker_attempts SET dispatch_payload=NULL`},
		{"missing-job-content", `UPDATE worker_job_revisions SET recorded_payload=NULL`},
		{"conflicting-job-content", `UPDATE worker_job_revisions SET recorded_payload=json_set(recorded_payload,'$.objective','conflicting')`},
		{"missing-acceptance", `DELETE FROM worker_attempt_dispositions`},
		{"missing-acceptance-receipt", `DELETE FROM effect_receipts WHERE event_id='accept'`},
		{"missing-authorization-receipt", `DELETE FROM effect_receipts WHERE event_id='authorize'`},
		{"missing-terminal-receipt", `DELETE FROM effect_receipts WHERE event_id='terminal'`},
		{"missing-dispatch-receipt", `DELETE FROM effect_receipts WHERE event_id='dispatch'`},
		{"wrong-satisfaction-reference", `UPDATE worker_job_revisions SET satisfied_result_ref='other-accept'`},
		{"wrong-satisfaction-sequence", `UPDATE worker_job_revisions SET satisfied_seq=41`},
		{"missing-satisfaction-sequence", `UPDATE worker_job_revisions SET satisfied_seq=0`},
		{"conflicting-first-acceptance", `UPDATE worker_attempt_dispositions SET first_acceptance_payload='{}'`},
		{"confirmation-review", `UPDATE worker_authorizations SET payload=json_set(payload,'$.checks_repair_review',json('{}'))`},
		{"implementation-authorized", `INSERT INTO worker_authorizations(work_id,attempt_id,seq,payload) SELECT work_id,'attempt:implementation',50,json_set(payload,'$.worker_capability_class','implementation') FROM worker_authorizations`},
		{"implementation-dispatched", `INSERT INTO worker_attempts(work_id,attempt_id,lane_id,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,dispatched_at,dispatch_payload) SELECT work_id,'attempt:implementation',lane_id,lane_digest,'implementation',readback_model,packet_schema_version,report_schema_version,'dispatched',dispatched_at,json_set(dispatch_payload,'$.capability_class','implementation') FROM worker_attempts`},
		{"other-confirmation-authorized", `INSERT INTO worker_authorizations(work_id,attempt_id,seq,payload) SELECT work_id,'attempt:confirmation',50,json_set(payload,'$.checks_repair_review.worker_job',json_extract(payload,'$.worker_job')) FROM worker_authorizations`},
		{"prerequisite-without-result-ref", dependent + `'[{"job_id":"job:continue","revision":1}]',unresolved_refs,digest,'recorded',recorded_at FROM worker_job_revisions`},
		{"prerequisite-with-result-ref", dependent + `'[{"job_id":"job:continue","revision":1,"result_ref":"accept"}]',unresolved_refs,digest,'recorded',recorded_at FROM worker_job_revisions`},
		{"older-revision", `INSERT INTO worker_job_revisions(work_id,job_id,revision,contract_version,objective,stopping_condition,path_scope,predicate_ids,checks,prerequisites,unresolved_refs,digest,state,recorded_at) SELECT work_id,job_id,2,contract_version,objective,stopping_condition,path_scope,predicate_ids,checks,prerequisites,unresolved_refs,digest,'recorded',recorded_at FROM worker_job_revisions`},
		{"rejected-after-acceptance", `INSERT INTO worker_attempt_dispositions(work_id,attempt_id,action_id,seq,event_id,payload) VALUES('continue-work','attempt:continue','reject_worker_result',50,'reject','{}')`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := continueReviewMigrationSeed(t)
			memberMigrationExec(t, db, tc.sql)
			memberMigrationExec(t, db, `DELETE FROM write_guard`)
			before := continueReviewMigrationSnapshot(t, db, true)
			if err := Migrate(t.Context(), db); err != nil {
				t.Fatal(err)
			}
			if after := continueReviewMigrationSnapshot(t, db, true); !reflect.DeepEqual(before, after) {
				t.Fatalf("unproved restoration changed facts\nbefore=%v\nafter=%v", before, after)
			}
		})
	}
	for _, tc := range []struct {
		name, column string
		value        any
	}{
		{"missing-receipt-digest", "payload_digest", nil},
		{"conflicting-receipt-digest", "payload_digest", "other"},
		{"wrong-receipt-work", "subject_id", "other-work"},
		{"wrong-receipt-kind", "kind", "worker.completed"},
		{"delivery-assertion-receipt", "delivery_assertion", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := continueReviewMigrationSeed(t)
			continueReviewMigrationReplaceReceipt(t, db, "accept", tc.column, tc.value)
			memberMigrationExec(t, db, `DELETE FROM write_guard`)
			before := continueReviewMigrationSnapshot(t, db, true)
			if err := Migrate(t.Context(), db); err != nil {
				t.Fatal(err)
			}
			if after := continueReviewMigrationSnapshot(t, db, true); !reflect.DeepEqual(before, after) {
				t.Fatalf("unproved restoration changed facts\nbefore=%v\nafter=%v", before, after)
			}
		})
	}
}

func TestContinueReviewJobMigration158RefusesConflictingBoundPayloads(t *testing.T) {
	// Refresh receipt digests so these exercise exact semantic joins, rather
	// than merely refusing a stale digest after a payload change.
	for _, tc := range []struct{ name, query, receipt string }{
		{"authorization-job", `UPDATE worker_authorizations SET payload=json_set(payload,'$.worker_job.digest','other'); SELECT payload FROM worker_authorizations`, "authorize"},
		{"authorization-capability", `UPDATE worker_authorizations SET payload=json_set(payload,'$.worker_capability_class','implementation'); SELECT payload FROM worker_authorizations`, "authorize"},
		{"dispatch-revision", `UPDATE worker_attempts SET dispatch_payload=json_set(dispatch_payload,'$.worker_job.revision',2); SELECT dispatch_payload FROM worker_attempts`, "dispatch"},
		{"terminal-job", `UPDATE worker_attempts SET terminal_payload=json_set(terminal_payload,'$.worker_job.job_id','job:other'); SELECT terminal_payload FROM worker_attempts`, "terminal"},
		{"terminal-attempt", `UPDATE worker_attempts SET terminal_payload=json_set(terminal_payload,'$.attempt_id','attempt:other'); SELECT terminal_payload FROM worker_attempts`, "terminal"},
		{"terminal-disposition", `UPDATE worker_attempts SET terminal_payload=json_set(terminal_payload,'$.review.disposition','ready_for_main'); SELECT terminal_payload FROM worker_attempts`, "terminal"},
		{"acceptance-job", `UPDATE worker_attempt_dispositions SET payload=json_set(payload,'$.worker_job.revision',2),first_acceptance_payload=json_set(first_acceptance_payload,'$.worker_job.revision',2); SELECT payload FROM worker_attempt_dispositions`, "accept"},
		{"acceptance-delivery", `UPDATE worker_attempt_dispositions SET payload=json_set(payload,'$.delivery_artifact','synthetic-result'),first_acceptance_payload=json_set(first_acceptance_payload,'$.delivery_artifact','synthetic-result'); SELECT payload FROM worker_attempt_dispositions`, "accept"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := continueReviewMigrationSeed(t)
			parts := strings.SplitN(tc.query, "; ", 2)
			memberMigrationExec(t, db, parts[0])
			var raw string
			if err := db.QueryRow(parts[1]).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			digest, err := effectPayloadDigest([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			continueReviewMigrationReplaceReceipt(t, db, tc.receipt, "payload_digest", digest)
			memberMigrationExec(t, db, `DELETE FROM write_guard`)
			before := continueReviewMigrationSnapshot(t, db, true)
			if err := Migrate(t.Context(), db); err != nil {
				t.Fatal(err)
			}
			if after := continueReviewMigrationSnapshot(t, db, true); !reflect.DeepEqual(before, after) {
				t.Fatal("conflicting exact bindings changed retained facts")
			}
		})
	}
}
