package store

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Historical worker inputs are authored at their immutable schema boundary.
// No current worker writer or compatibility launcher participates in this proof.
func nativeTaskUpgradeFixture(t *testing.T) (*sql.DB, string, []histEventRow) {
	t.Helper()
	db, path := retirementUpgradeFixture(t)
	advanceHistoricalPrefix(t, db, 151)
	historical := contentIdentity151WorkerFixture(t, db, "work-upgrade-from")
	m := nativeTaskRetirementMigration(t)
	advanceHistoricalPrefix(t, db, m.Version-1)
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1)`)
	actor := "actor:" + strings.Repeat("a", 64)
	digest := "sha256:" + strings.Repeat("d", 64)
	commit, tree := strings.Repeat("a", 40), strings.Repeat("b", 40)
	retirementUpgradeExec(t, db, `INSERT INTO agent_clients(client_ref,status,principal_ref,capabilities_json,product_scope_json,project_scope_json,created_at) VALUES('client-upgrade','active','operator-upgrade','["product_read","work_write"]','["product-upgrade"]','["project-upgrade"]',?)`, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO agent_client_keys(client_ref,key_id,public_key,status,created_at) VALUES('client-upgrade','key-upgrade',?,'active',?)`, []byte(strings.Repeat("k", 32)), retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_instances(work_id,definition_ref,definition_digest,current_step,instance_state,execution_actor_ref,started_at,execution_started_at,result_disposition,latest_non_dispatch_start_seq) VALUES('work-upgrade-from','workflow.break_fix',?,'repair','running',?,?,?,'record_worker_failure',35)`, digest, actor, retirementUpgradeTime, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_step_epochs(work_id,step_id,attempt_epoch,latest_start_seq,latest_start_event_id,latest_start_payload) VALUES('work-upgrade-from','repair',3,35,'native-start','{ "action_id": "record_review", "retained": true }')`)
	retirementUpgradeExec(t, db, `INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_digest,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,result_kind,result_payload,evidence_refs,changed_refs,resume_cursor,principal_ref,request_id,observed_at,completed_at,contract_digest) VALUES('native-operation',3,'work-upgrade-from','workflow.break_fix',?,'repair','internal_sqlite',?,'{}','completed','{ "native": true }','["evidence:native"]','["internal/store/store.go"]','cursor:native','operator-upgrade','request-native',?,?,?)`, digest, digest, retirementUpgradeTime, retirementUpgradeTime, digest)
	retirementUpgradeExec(t, db, `INSERT INTO idempotency_records(principal_ref,tool,operation_kind,idempotency_key,canonical_digest,op_id,result_event_ids,first_observed_at,last_observed_at) VALUES('operator-upgrade','concord_work_transition','workflow_action','native-key',?,'native-operation','["native-review"]',?,?)`, digest, retirementUpgradeTime, retirementUpgradeTime)
	for _, receipt := range []struct {
		seq  int
		id   string
		kind string
	}{
		{32, "native-review", WorkflowActionCompleted},
		{33, "native-correction", WorkflowActionCompleted},
		{34, "native-evidence", WorkflowEvidenceBound},
		{35, "native-start", WorkflowActionStarted},
	} {
		retirementUpgradeExec(t, db, `INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version,payload_digest) VALUES(?,?,?,'work_item','work-upgrade-from',?,1,?)`, receipt.id, receipt.seq, receipt.kind, actor, historicalFixturePayloadDigest(t, `{ "native": true }`))
	}
	retirementUpgradeExec(t, db, `UPDATE store_sequences SET last=35 WHERE name='effect'; UPDATE work_items SET effect_seq=35 WHERE id='work-upgrade-from'`)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_evidence_bindings VALUES('work-upgrade-from',34,'native-evidence',?,'{ "evidence_ref": "evidence:native", "immutable_subject_ref": "commit:native", "kind": "verification" }')`, actor)
	retirementUpgradeExec(t, db, `UPDATE workflow_correction_state SET state=? WHERE work_id='work-upgrade-from'`, workflowJSON(map[string]any{
		"boundary": 0, "unresolved": map[string]bool{"job:upgrade|1": false},
		"records": []any{map[string]any{
			"seq": 33, "source_id": "native-correction",
			"fields": workflowCorrectionCompletionFields{ActionID: "request_correction", CorrectionDiagnosis: "candidate misses its obligation", CorrectionStrategy: "correct the candidate", CorrectionPredicates: []string{"predicate:primary"}, CorrectionEvidence: []string{"evidence:native"}},
		}},
	}))
	report := reviewBlockFixture()
	report.InputCandidate.ProjectID, report.OutputCandidate.ProjectID = "project-upgrade", "project-upgrade"
	state := WorkflowReviewState{Candidate: &report.OutputCandidate, Findings: report.Findings, Accepted: &WorkflowAcceptedReview{ReportRef: "native-review", ReportDigest: reviewDigest(report), ActorRef: actor, Report: *report}}
	if err := writeWorkflowReviewFacts(t.Context(), db, "work-upgrade-from", state); err != nil {
		t.Fatal(err)
	}
	retirementUpgradeExec(t, db, `INSERT INTO work_context_findings VALUES('work-upgrade-from',32,0,'ranked',?)`, workflowJSON(report.Findings[0]))
	oracle := AcceptanceOracle{Owners: []OracleOwner{{OwnerID: "owner:upgrade", DomainID: "domain:upgrade"}}, Cases: []OracleCase{{CaseID: "case:upgrade", OwnerID: "owner:upgrade", EntryPoint: "entry:upgrade", Judge: OracleJudgeReview}}, Controls: []OracleControl{}}
	retirementUpgradeExec(t, db, `INSERT INTO worker_job_revisions(work_id,job_id,revision,contract_version,objective,stopping_condition,project_scope,path_scope,predicate_ids,checks,prerequisites,unresolved_refs,digest,state,recorded_at,recorded_payload) VALUES('work-upgrade-from','job:upgrade',1,1,'Preserve the candidate','Candidate retained','project-upgrade','[]','[]','[]','[]','[]',?,'recorded',?,?)`, digest, retirementUpgradeTime, workflowJSON(map[string]any{"acceptance_oracle": oracle}))
	retirementUpgradeExec(t, db, `INSERT INTO worker_oracle_finding_facts VALUES('work-upgrade-from',1,?,'[]')`, workflowJSON([]any{map[string]any{"FindingID": "finding:32:0", "DomainID": "domain:upgrade", "SourceEventSeq": 32, "SourceOrdinal": 0, "source_job": map[string]any{"job_id": "job:upgrade", "revision": 1, "digest": digest}}}))
	retirementUpgradeExec(t, db, `INSERT INTO workflow_worker_actors VALUES(?,25)`, actor)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_member_groups(work_id,group_id,controller_actor_ref,permission,recorded_seq) VALUES('work-upgrade-from','group:upgrade',?,'continue',25)`, actor)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_group_members(work_id,group_id,member_id,job_id,job_revision,job_digest,project_id,actor_ref,confirmed) VALUES('work-upgrade-from','group:upgrade','member:upgrade','job:upgrade',1,?,'project-upgrade',?,1)`, digest, actor)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_member_callers VALUES(?,'work-upgrade-from','project-upgrade','group:upgrade','member:upgrade')`, actor)
	for _, member := range []string{"", "member:upgrade"} {
		claim, branch, path, state := "claim:ordinary", "topic/ordinary", "/synthetic/native", "verified"
		entryState := "active"
		var reclaimed any
		if member != "" {
			claim, branch, path, state = "claim:member", "topic/member", "/synthetic/member", "reclaimed"
			entryState, reclaimed = "reclaimed", retirementUpgradeTime
		}
		retirementUpgradeExec(t, db, `INSERT INTO worktree_claims(op_id,work_id,project_id,set_id,pinned_branch,pinned_base_sha,pinned_path,state,principal_ref,request_id,observed_at,updated_at,member_id) VALUES(?,'work-upgrade-from','project-upgrade','set:upgrade',?,?,?,?,'operator-upgrade','request-upgrade',?,?,?)`, claim, branch, commit, path, state, retirementUpgradeTime, retirementUpgradeTime, member)
		retirementUpgradeExec(t, db, `INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,reclaimed_at,git_facts,member_id) VALUES('set:upgrade','project-upgrade',?,?,?,?, 'repository:upgrade',?,?,?,'{ "retained": true }',?)`, claim, branch, commit, path, entryState, retirementUpgradeTime, reclaimed, member)
	}
	retirementUpgradeExec(t, db, `INSERT INTO worktree_occupancy(worktree_id,session_ref,recorded_at,has_process_identity) VALUES('set:upgrade:project-upgrade:claim:ordinary','session-upgrade',?,0)`, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO worktree_verify_leases(lease_id,work_id,project_id,path,state,client_ref,agent_ref,session_ref,principal_ref,command_json,acquired_at,released_at,exit_code,outcome,result_json) VALUES('verify:upgrade','work-upgrade-from','project-upgrade','/synthetic/native','released','client-upgrade','agent-upgrade','session-upgrade','operator-upgrade','["go","test"]',?,?,0,'completed',?)`, retirementUpgradeTime, retirementUpgradeTime, `{ "commit": "`+commit+`", "tree": "`+tree+`", "exit_code": 0 }`)
	retirementUpgradeExec(t, db, `INSERT INTO worktree_ref_outcomes(set_id,project_id,claim_op_id,branch,phase,reason,path,recorded_at) VALUES('set:upgrade','project-upgrade','claim:member','topic/member','settled','native owner settled the reference','/synthetic/member',?)`, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
	assertHistoricalReceipts(t, db, historical)
	retirementUpgradeHealthy(t, db)
	return db, path, historical
}

func nativeTaskUpgradeSnapshot(t *testing.T, db *sql.DB, path string) (string, BackupManifest) {
	t.Helper()
	before := retirementUpgradeSnapshot(t, db)
	snapshot := filepath.Join(t.TempDir(), "pre-native.db")
	manifest, err := BackupDatabase(t.Context(), path, snapshot)
	if err != nil {
		t.Fatalf("verified predecessor snapshot: %v", err)
	}
	verified, err := VerifyBackup(t.Context(), snapshot)
	if err != nil || verified.SchemaVersion != nativeTaskRetirementMigration(t).Version-1 || verified.DBChecksum != manifest.DBChecksum {
		t.Fatalf("verified predecessor inventory=%+v err=%v", verified, err)
	}
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("snapshot migrated or repaired the live predecessor")
	}
	snapshotDB, err := sql.Open(driverName, readOnlyDataSource(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	defer snapshotDB.Close()
	if got := retirementUpgradeSnapshot(t, snapshotDB); !reflect.DeepEqual(got, before) {
		t.Fatal("snapshot did not retain the entire predecessor schema and population")
	}
	return snapshot, manifest
}

func TestNativeTaskPopulatedUpgradePreservesCurrentData(t *testing.T) {
	useStampedBuild(t)
	db, path, historical := nativeTaskUpgradeFixture(t)
	m := nativeTaskRetirementMigration(t)
	before := retirementUpgradeSnapshot(t, db)
	if s, err := Open(t.Context(), path); err == nil {
		s.Close()
		t.Fatal("ordinary open silently performed the breaking native cutover")
	} else {
		assertFailureKind(t, err, KindUpgradeRequired)
	}
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("refused ordinary open changed populated predecessor")
	}
	snapshot, manifest := nativeTaskUpgradeSnapshot(t, db, path)
	retained := map[string]string{
		"agent_clients":              `SELECT * FROM agent_clients ORDER BY rowid`,
		"agent_client_keys":          `SELECT * FROM agent_client_keys ORDER BY rowid`,
		"workflow_actors":            `SELECT * FROM workflow_actors ORDER BY rowid`,
		"workflow_contracts":         `SELECT * FROM workflow_contracts ORDER BY rowid`,
		"workflow_evidence_bindings": `SELECT * FROM workflow_evidence_bindings ORDER BY rowid`,
		"durable_operations":         `SELECT * FROM durable_operations ORDER BY rowid`,
		"idempotency_records":        `SELECT * FROM idempotency_records ORDER BY rowid`,
		"effect_receipts":            `SELECT * FROM effect_receipts ORDER BY rowid`,
		"store_sequences":            `SELECT * FROM store_sequences ORDER BY rowid`,
		"work_context_findings":      `SELECT * FROM work_context_findings ORDER BY rowid`,
		"worktree_occupancy":         `SELECT * FROM worktree_occupancy ORDER BY rowid`,
		"worktree_verify_leases":     `SELECT * FROM worktree_verify_leases ORDER BY rowid`,
		"worktree_ref_outcomes":      `SELECT * FROM worktree_ref_outcomes ORDER BY rowid`,
		"worktree_entries":           `SELECT set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,reclaimed_at,git_facts FROM worktree_entries WHERE claim_op_id='claim:ordinary'`,
		"worktree_claims":            `SELECT op_id,work_id,project_id,set_id,pinned_branch,pinned_base_sha,pinned_path,state,principal_ref,request_id,observed_at,updated_at,repository_id,incarnation,executor_pid,executor_pid_start FROM worktree_claims ORDER BY op_id`,
		"workflow_instances":         `SELECT work_id,definition_ref,definition_digest,current_step,instance_state,execution_actor_ref,execution_model,started_at,completed_at,last_checkpoint_at,execution_started_at,outside_repair_state,completion_payload,completion_occurred_at,unhealthy_verdict_seq FROM workflow_instances`,
		"workflow_step_epochs":       `SELECT work_id,step_id,attempt_epoch,latest_start_seq,latest_start_event_id,latest_start_payload FROM workflow_step_epochs`,
	}
	want := map[string][][]any{}
	for table, query := range retained {
		want[table] = retirementUpgradeRows(t, db, query)
		if len(want[table]) == 0 {
			t.Fatalf("preservation witness %s is empty", table)
		}
	}
	review, err := readWorkflowReviewFacts(t.Context(), db, "work-upgrade-from")
	if err != nil {
		t.Fatal(err)
	}
	direction, err := readWorkflowCorrectionContext(t.Context(), db, "work-upgrade-from")
	if err != nil || direction == nil {
		t.Fatalf("predecessor genuine correction=%+v err=%v", direction, err)
	}
	report, err := Upgrade(t.Context(), path, nil)
	if err != nil || report.SchemaVersion != CurrentSchemaVersion() || len(report.Applied) != 1 || report.Applied[0] != m.Version {
		t.Fatalf("authorized native upgrade=%+v err=%v", report, err)
	}
	if got := retirementUpgradeRows(t, db, `SELECT name,checksum,breaking FROM schema_migrations WHERE version=?`, m.Version); !reflect.DeepEqual(got, [][]any{{m.Name, m.checksum(), int64(1)}}) {
		t.Fatalf("native cutover did not record its exact breaking migration: %v", got)
	}
	for table, query := range retained {
		if got := retirementUpgradeRows(t, db, query); !reflect.DeepEqual(got, want[table]) {
			t.Errorf("native cutover changed retained %s: before=%#v after=%#v", table, want[table], got)
		}
	}
	for _, table := range []string{"worker_attempts", "worker_authorizations", "worker_attempt_dispositions", "worker_job_revisions", "worker_oracle_finding_facts", "workflow_worker_actors", "workflow_member_groups", "workflow_group_members", "workflow_member_callers"} {
		if len(before[table]) == 0 {
			t.Errorf("retired source %s was not populated", table)
		}
		if got := retirementUpgradeRows(t, db, `SELECT name FROM sqlite_schema WHERE name=?`, table); len(got) != 0 {
			t.Errorf("retired worker owner remains: %v", got)
		}
	}
	if got := retirementUpgradeRows(t, db, `SELECT name FROM pragma_table_info('worktree_entries') WHERE name='member_id'`); len(got) != 0 {
		t.Fatalf("member entry metadata remains: %v", got)
	}
	if got := retirementUpgradeRows(t, db, `SELECT count(*) FROM worktree_entries`); !reflect.DeepEqual(got, [][]any{{int64(1)}}) {
		t.Fatalf("settled member tree was not retired: %v", got)
	}
	afterReview, err := readWorkflowReviewFacts(t.Context(), db, "work-upgrade-from")
	if err != nil || !reflect.DeepEqual(afterReview.Candidate, review.Candidate) || !reflect.DeepEqual(afterReview.Accepted, review.Accepted) || !reflect.DeepEqual(afterReview.Findings, review.Findings) {
		t.Fatalf("candidate, accepted report or open blocker changed: before=%+v after=%+v err=%v", review, afterReview, err)
	}
	source := afterReview.findingSources["finding:one"]
	if source.CanonicalID != "finding:32:0" || source.DomainID != "domain:upgrade" || source.EventID != "native-review" || source.Kind != WorkflowActionCompleted || source.Seq != 32 || source.Ordinal != 0 || source.Oracle == nil || len(source.Oracle.Cases) != 1 || source.Oracle.Cases[0].CaseID != "case:upgrade" {
		t.Fatalf("retained blocker lost exact source identity/inventory: %+v", source)
	}
	afterDirection, err := readWorkflowCorrectionContext(t.Context(), db, "work-upgrade-from")
	if err != nil || !reflect.DeepEqual(afterDirection, direction) {
		t.Fatalf("genuine correction changed: before=%+v after=%+v err=%v", direction, afterDirection, err)
	}
	assertHistoricalReceipts(t, db, historical)
	retirementUpgradeHealthy(t, db)
	committed := retirementUpgradeSnapshot(t, db)
	again, err := Upgrade(t.Context(), path, nil)
	if err != nil || len(again.Applied) != 0 {
		t.Fatalf("repeat upgrade=%+v err=%v", again, err)
	}
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, committed) {
		t.Fatal("repeated upgrade/open changed committed native data")
	}
	verified, err := VerifyBackup(t.Context(), snapshot)
	if err != nil || verified.DBChecksum != manifest.DBChecksum || verified.SchemaVersion != m.Version-1 {
		t.Fatalf("native upgrade changed retained predecessor snapshot: %+v err=%v", verified, err)
	}
}

func TestNativeTaskPopulatedUpgradeRefusesUnsettledMemberWithoutEffects(t *testing.T) {
	useStampedBuild(t)
	db, path, historical := nativeTaskUpgradeFixture(t)
	retirementUpgradeExec(t, db, `UPDATE worktree_entries SET state='active',reclaimed_at=NULL WHERE member_id<>''`)
	before := retirementUpgradeSnapshot(t, db)
	snapshot := filepath.Join(t.TempDir(), "refused-native.db")
	_, err := BackupDatabase(t.Context(), path, snapshot)
	assertFailureKind(t, err, KindResourceBusy)
	for _, file := range []string{snapshot, manifestPath(snapshot)} {
		if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("refused snapshot left a qualified artifact %s: %v", file, err)
		}
	}
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("refused verified snapshot changed populated predecessor")
	}
	_, err = Upgrade(t.Context(), path, nil)
	assertFailureKind(t, err, KindResourceBusy)
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("unsettled-member upgrade changed schema, receipts, guards, sequences or current data")
	}
	assertHistoricalReceipts(t, db, historical)
	retirementUpgradeHealthy(t, db)
}

func TestNativeTaskPopulatedUpgradeRollsBackDestructiveDDL(t *testing.T) {
	useStampedBuild(t)
	db, path, historical := nativeTaskUpgradeFixture(t)
	snapshot, manifest := nativeTaskUpgradeSnapshot(t, db, path)
	// SQLite refuses the final column retirement while a view depends on it.
	// Earlier table drops and correction normalization must roll back with it.
	retirementUpgradeExec(t, db, `CREATE VIEW native_cutover_failure AS SELECT result_disposition FROM workflow_instances`)
	before := retirementUpgradeSnapshot(t, db)
	_, err := Upgrade(t.Context(), path, nil)
	assertFailureKind(t, err, KindUnavailable)
	if !strings.Contains(err.Error(), "result_disposition") {
		t.Fatalf("upgrade did not reach the injected final DDL failure: %v", err)
	}
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("failed real upgrade changed predecessor schema, manifest, normalized state, native rows or immutable receipts")
	}
	assertHistoricalReceipts(t, db, historical)
	retirementUpgradeHealthy(t, db)
	verified, err := VerifyBackup(t.Context(), snapshot)
	if err != nil || verified.DBChecksum != manifest.DBChecksum {
		t.Fatalf("failed upgrade damaged verified predecessor snapshot: %+v err=%v", verified, err)
	}
	retirementUpgradeExec(t, db, `DROP VIEW native_cutover_failure`)
	if _, err := Upgrade(t.Context(), path, nil); err != nil {
		t.Fatalf("retry after removing injected failure: %v", err)
	}
	assertHistoricalReceipts(t, db, historical)
}
