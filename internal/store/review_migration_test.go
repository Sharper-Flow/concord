package store

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPre148ReviewFindingSourceSurvivesNativeCutover(t *testing.T) {
	for _, test := range []struct {
		name, owner, digest, domain string
	}{
		{"bound", "owner:original", "a", "domain:original"},
		{"unknown_owner", "owner:unknown", "a", "unresolved"},
		{"unknown_revision_digest", "owner:original", "b", "unresolved"},
		{"unbound", "owner:original", "", "unresolved"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := openMigratedTo(t, filepath.Join(t.TempDir(), "review-144.db"), 144)
			retirementUpgradeExec(t, db, `INSERT INTO fold_guard VALUES(1)`)
			retirementUpgradeExec(t, db, `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('work:review','task','Retain finding provenance','needed',0,1,?,?)`, retirementUpgradeTime, retirementUpgradeTime)
			oracle := AcceptanceOracle{Owners: []OracleOwner{{OwnerID: "owner:original", DomainID: "domain:original"}}, Cases: []OracleCase{}, Controls: []OracleControl{}}
			digest := "sha256:" + strings.Repeat("a", 64)
			retirementUpgradeExec(t, db, `INSERT INTO worker_job_revisions(work_id,job_id,revision,contract_version,objective,stopping_condition,project_scope,path_scope,predicate_ids,checks,prerequisites,unresolved_refs,digest,state,recorded_at,recorded_payload) VALUES('work:review','job:original',1,1,'Retain original finding','Source retained','project:original','[]','[]','[]','[]','[]',?,'recorded',?,?)`, digest, retirementUpgradeTime, workflowJSON(map[string]any{"project_scope": "project:original", "acceptance_oracle": oracle}))
			report := reviewBlockFixture()
			report.Findings[0].Oracle = &ReviewOracleFinding{Classification: OracleClassificationDeliveryBlocker, OwnerID: test.owner, ControlIDs: []string{"control:original"}}
			job := map[string]any{"job_id": "job:original", "revision": 1, "digest": "sha256:" + strings.Repeat(test.digest, 64)}
			if test.digest == "" {
				job = nil
			}
			events := []struct {
				kind string
				body map[string]any
			}{
				{WorkflowActionCompleted, map[string]any{"action_id": "dispatch_worker", "worker_attempt_id": "attempt:original", "worker_capability_class": "review", "worker_job": job}},
				{"worker.dispatched", map[string]any{"attempt_id": "attempt:original", "worker_job": job}},
				{"worker.completed", map[string]any{"attempt_id": "attempt:original", "worker_job": job, "review": report, "context_findings": []WorkContextFinding{contextFindingFixture()}}},
				{WorkflowActionCompleted, map[string]any{"action_id": "accept_worker_result", "worker_attempt_id": "attempt:original"}},
			}
			for i, event := range events {
				if event.kind == WorkflowActionCompleted {
					event.body["step_id"] = "repair"
				}
				retirementUpgradeExec(t, db, `INSERT INTO domain_events(seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,'work_item','work:review','actor:original',?,1,?)`, i+1, fmt.Sprintf("review-source:%d", i+1), event.kind, retirementUpgradeTime, workflowJSON(event.body))
			}
			// A later report repeats the local ID under a different immutable
			// owner inventory. It must not replace the minting report's source.
			laterOracle := AcceptanceOracle{Owners: []OracleOwner{{OwnerID: test.owner, DomainID: "domain:later"}}, Cases: []OracleCase{}, Controls: []OracleControl{}}
			laterJob := map[string]any{"job_id": "job:later", "revision": 1, "digest": digest}
			retirementUpgradeExec(t, db, `INSERT INTO worker_job_revisions(work_id,job_id,revision,contract_version,objective,stopping_condition,project_scope,path_scope,predicate_ids,checks,prerequisites,unresolved_refs,digest,state,recorded_at,recorded_payload) VALUES('work:review','job:later',1,1,'Retain repeated finding','Source retained','project:later','[]','[]','[]','[]','[]',?,'recorded',?,?)`, digest, retirementUpgradeTime, workflowJSON(map[string]any{"project_scope": "project:later", "acceptance_oracle": laterOracle}))
			retirementUpgradeExec(t, db, `INSERT INTO domain_events(seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(5,'review-source:5','workflow.action_completed','work_item','work:review','actor:later',?,1,?)`, retirementUpgradeTime, workflowJSON(map[string]any{"step_id": "repair", "action_id": "dispatch_worker", "worker_attempt_id": "attempt:later", "worker_capability_class": "review", "worker_job": laterJob}))
			retirementUpgradeExec(t, db, `INSERT INTO domain_events(seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(6,'review-source:6','worker.completed','work_item','work:review','actor:later',?,1,?)`, retirementUpgradeTime, workflowJSON(map[string]any{"attempt_id": "attempt:later", "worker_job": laterJob, "review": report}))
			retirementUpgradeExec(t, db, `INSERT INTO domain_events(seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(7,'review-source:7','workflow.action_completed','work_item','work:review','actor:later',?,1,?)`, retirementUpgradeTime, workflowJSON(map[string]any{"step_id": "repair", "action_id": "accept_worker_result", "worker_attempt_id": "attempt:later"}))
			retirementUpgradeExec(t, db, `DELETE FROM fold_guard`)
			before := histEventRows(t, &Store{db: db})
			advanceHistoricalPrefix(t, db, 165)
			var legacyCount int
			if err := db.QueryRow(`SELECT count(*) FROM worker_oracle_finding_facts`).Scan(&legacyCount); err != nil || legacyCount != 0 {
				t.Fatalf("fixture unexpectedly relies on retired finding aggregate: %d, %v", legacyCount, err)
			}
			retained, err := readWorkflowReviewFacts(t.Context(), db, "work:review")
			if err != nil || retained.Accepted == nil || len(retained.Findings) != 1 {
				t.Fatalf("historical accepted Review=%+v err=%v", retained, err)
			}
			if err := Migrate(t.Context(), db); err != nil {
				t.Fatal(err)
			}
			after, err := readWorkflowReviewFacts(t.Context(), db, "work:review")
			if err != nil || !reflect.DeepEqual(retained.Candidate, after.Candidate) || !reflect.DeepEqual(retained.Accepted, after.Accepted) || !reflect.DeepEqual(retained.Findings, after.Findings) {
				t.Fatalf("native cutover changed retained Review: before=%+v after=%+v err=%v", retained, after, err)
			}
			source := after.findingSources[report.Findings[0].FindingID]
			if source.CanonicalID != "finding:3:1" || source.DomainID != test.domain || source.EventID != "review-source:3" || source.Kind != "worker.completed" || source.Seq != 3 || source.Ordinal != 1 {
				t.Fatalf("original finding source lost: %+v; want Domain %s", source, test.domain)
			}
			if test.digest == "a" && !reflect.DeepEqual(source.Oracle, &oracle) {
				t.Fatalf("original immutable oracle lost: %+v", source.Oracle)
			}
			if test.digest != "a" && source.Oracle != nil {
				t.Fatalf("unresolvable job invented source oracle: %+v", source.Oracle)
			}
			assertHistoricalReceipts(t, db, before)
			assertEventLogAbsent(t, &Store{db: db})
			retirementUpgradeHealthy(t, db)
		})
	}
}
