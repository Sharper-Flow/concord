package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func memberMigrationExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func memberMigrationRows(t *testing.T, db *sql.DB, query string) []string {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var result []string
	for rows.Next() {
		values, targets := make([]any, len(columns)), make([]any, len(columns))
		for i := range values {
			targets[i] = &values[i]
		}
		if err := rows.Scan(targets...); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, string(encoded))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func memberMigration155(t *testing.T) migration {
	t.Helper()
	for _, m := range migrations {
		if m.Version == 155 {
			return m
		}
	}
	t.Fatal("member claim migration 155 is missing")
	return migration{}
}

func memberMigrationSeedOwners(t *testing.T, db *sql.DB) (string, string) {
	t.Helper()
	memberMigrationExec(t, db, `INSERT INTO write_guard(active) VALUES(1)`)
	memberMigrationExec(t, db, `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('member-work','task','Member work','needed',0,1,'now','now')`)
	memberMigrationExec(t, db, `INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('member-project','Member project',1,'now','now')`)
	actors := []string{"actor:" + strings.Repeat("a", 64), "actor:" + strings.Repeat("b", 64)}
	for i, actor := range actors {
		memberMigrationExec(t, db, `INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'principal','client','agent',?,'agent','now')`, actor, fmt.Sprintf("session-%d", i))
	}
	memberMigrationExec(t, db, `DELETE FROM write_guard`)
	return actors[0], actors[1]
}

func TestMemberClaimMigration155PreservesIntegration(t *testing.T) {
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "member-prefix.db"), 154)
	// No table references the old two-column entry key. Occupancy and leases
	// retain their own exact claim/native identities, not entry foreign keys.
	if got := memberMigrationRows(t, db, `SELECT s.name,f."table" FROM sqlite_schema s,pragma_foreign_key_list(s.name) f WHERE s.type='table' AND f."table"='worktree_entries'`); len(got) != 0 {
		t.Fatalf("entry rebuild has inbound foreign keys: %v", got)
	}
	entryObjects := memberMigrationRows(t, db, `SELECT type,name,sql FROM sqlite_schema WHERE tbl_name='worktree_entries' AND type IN ('index','trigger') AND sql IS NOT NULL ORDER BY name`)
	t.Logf("154 entry indexes and triggers: %v", entryObjects)
	memberMigrationSeedOwners(t, db)
	for i, state := range []string{"verified", "pending", "reclaimed"} {
		memberMigrationExec(t, db, `INSERT INTO worktree_claims(op_id,work_id,project_id,set_id,pinned_branch,pinned_base_sha,pinned_path,state,principal_ref,request_id,observed_at,updated_at,repository_id,incarnation,executor_pid,executor_pid_start) VALUES(?,?,'member-project',?,?,?,?,?,'principal',?,'before','after','synthetic-repo',7,12,34)`, fmt.Sprintf("claim-%d", i), fmt.Sprintf("work-%d", i), fmt.Sprintf("wts:work-%d", i), fmt.Sprintf("work/work-%d", i), strings.Repeat("a", 40), fmt.Sprintf("/synthetic/tree-%d", i), state, fmt.Sprintf("request-%d", i))
	}
	memberMigrationExec(t, db, `INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,reclaimed_at,git_facts) VALUES
('wts:work-0','member-project','claim-0','work/work-0','base-0','/synthetic/tree-0','synthetic-repo','active','verified',NULL,'{"clean":true}'),
('wts:work-2','member-project','claim-2','work/work-2','base-2','/synthetic/tree-2','synthetic-repo','reclaimed','verified','reclaimed','{"clean":false}')`)
	memberMigrationExec(t, db, `INSERT INTO write_guard(active) VALUES(1)`)
	memberMigrationExec(t, db, `INSERT INTO worktree_occupancy(worktree_id,session_ref,recorded_at,host_pid,host_pid_start,has_process_identity) VALUES('wts:work-0:member-project:claim-0','session-0','recorded',12,34,1),('wts:work-0:member-project:claim-0','session-legacy','recorded',NULL,NULL,0)`)
	memberMigrationExec(t, db, `DELETE FROM write_guard`)
	memberMigrationExec(t, db, `INSERT INTO worktree_verify_leases(lease_id,work_id,project_id,path,state,client_ref,agent_ref,session_ref,principal_ref,command_json,acquired_at,outcome,native_plan_json,native_plan_sha256,stdout_blob,stderr_blob,event_watermark) VALUES('lease','member-work','member-project','/synthetic/tree-0','held','client','agent','session-0','principal','["true"]','acquired','running','{}',?,x'0001',x'0203',8)`, "sha256:"+strings.Repeat("b", 64))
	memberMigrationExec(t, db, `INSERT INTO worktree_claim_native_fences VALUES('path','','/synthetic/tree-0','wts:work-0',9),('branch','synthetic-repo','work/work-0','wts:work-0',11)`)
	queries := []string{
		`SELECT op_id,work_id,project_id,set_id,pinned_branch,pinned_base_sha,pinned_path,state,principal_ref,request_id,observed_at,updated_at,repository_id,incarnation,executor_pid,executor_pid_start FROM worktree_claims ORDER BY op_id`,
		`SELECT set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,reclaimed_at,git_facts FROM worktree_entries ORDER BY set_id,project_id`,
		`SELECT * FROM worktree_occupancy ORDER BY worktree_id,session_ref`,
		`SELECT * FROM worktree_verify_leases ORDER BY lease_id`,
		`SELECT * FROM worktree_claim_native_fences ORDER BY resource_kind,repository_id,resource_ref`,
		`SELECT type,name,sql FROM sqlite_schema WHERE tbl_name IN ('worktree_claim_native_fences','worktree_occupancy','worktree_verify_leases') ORDER BY name`,
		`SELECT type,name,sql FROM sqlite_schema WHERE tbl_name='worktree_entries' AND type IN ('index','trigger') AND sql IS NOT NULL ORDER BY name`,
		`SELECT type,name,sql FROM sqlite_schema WHERE tbl_name='worktree_claims' AND type IN ('index','trigger') AND name<>'worktree_claims_one_active' ORDER BY name`,
	}
	before := make([][]string, len(queries))
	for i, query := range queries {
		before[i] = memberMigrationRows(t, db, query)
	}
	m := memberMigration155(t)
	if !m.Breaking || m.FoldMaintained != "advance" {
		t.Fatalf("migration compatibility: breaking=%t maintained=%q", m.Breaking, m.FoldMaintained)
	}
	assertFailureKind(t, migrateAtOpen(t.Context(), db), KindUpgradeRequired)
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	for i, query := range queries {
		if after := memberMigrationRows(t, db, query); !reflect.DeepEqual(before[i], after) {
			t.Errorf("migration changed %s\nbefore=%v\nafter=%v", query, before[i], after)
		}
	}
	for _, table := range []string{"worktree_entries", "worktree_claims"} {
		var nonIntegration int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table + ` WHERE member_id<>'' OR member_id IS NULL`).Scan(&nonIntegration); err != nil || nonIntegration != 0 {
			t.Fatalf("%s integration identity: count=%d err=%v", table, nonIntegration, err)
		}
	}
	for _, query := range []string{
		`INSERT INTO worktree_claim_native_fences VALUES('other','','/synthetic/new','wts:other',1)`,
		`INSERT INTO worktree_claim_native_fences VALUES('path','repo','/synthetic/new','wts:other',1)`,
		`INSERT INTO worktree_claim_native_fences VALUES('branch','','work/new','wts:other',1)`,
		`INSERT INTO worktree_claim_native_fences VALUES('path','','/synthetic/new','wts:other',0)`,
		`INSERT INTO worktree_claim_native_fences VALUES('path','','/synthetic/new','wts:other',1.5)`,
		`INSERT INTO worktree_claim_native_fences VALUES('path','','/synthetic/tree-0','wts:other',12)`,
		`INSERT INTO worktree_claim_native_fences VALUES('branch','synthetic-repo','work/work-0','wts:other',12)`,
		`UPDATE worktree_claims SET executor_pid=-1 WHERE op_id='claim-0'`,
		`UPDATE worktree_claims SET executor_pid=0 WHERE op_id='claim-0'`,
		`UPDATE worktree_claims SET executor_pid_start=0 WHERE op_id='claim-0'`,
		`UPDATE worktree_claims SET executor_pid_start=1.5 WHERE op_id='claim-0'`,
	} {
		if _, err := db.Exec(query); err == nil {
			t.Fatalf("migration lost a 154 fence: %s", query)
		}
	}
	var floor int
	if err := db.QueryRow(`SELECT max(version) FROM schema_migrations WHERE breaking=1`).Scan(&floor); err != nil || floor != 166 {
		t.Fatalf("compatibility floor=%d err=%v", floor, err)
	}
	if got := memberMigrationRows(t, db, `PRAGMA foreign_key_check`); len(got) != 0 {
		t.Fatalf("foreign key violations: %v", got)
	}
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("idempotent migration: %v", err)
	}
}

func memberMigrationOpen155(t *testing.T) *sql.DB {
	t.Helper()
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "member-155.db"), 154)
	memberMigration155(t)
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMemberClaimMigration155ScopeAndNativeUniqueness(t *testing.T) {
	db := memberMigrationOpen155(t)
	claim := func(op, work, project, member, path, repo, branch, state string) error {
		_, err := db.Exec(`INSERT INTO worktree_claims(op_id,work_id,project_id,set_id,member_id,pinned_branch,pinned_base_sha,pinned_path,repository_id,state,principal_ref,request_id,observed_at,updated_at) VALUES(?,?,?,'wts:scope',?,?,?,?,?,?,'principal',?,'now','now')`, op, work, project, member, branch, strings.Repeat("a", 40), path, repo, state, op)
		return err
	}
	for _, c := range []struct{ op, member, state string }{
		{"integration", "", "verified"}, {"member-a", "a", "pending"}, {"member-b", "b", "verified"},
		{"old-a", "a", "reclaimed"}, {"old-integration", "", "reclaimed"},
	} {
		if err := claim(c.op, "work", "project", c.member, "/synthetic/"+c.op, "repo", "work/"+c.op, c.state); err != nil {
			t.Fatalf("distinct scope or reclaimed claim %s: %v", c.op, err)
		}
	}
	for _, c := range []struct{ name, member, path, repo, branch string }{
		{"integration-scope", "", "/synthetic/new", "repo", "work/new"},
		{"pending-scope", "a", "/synthetic/new", "repo", "work/new"},
		{"verified-scope", "b", "/synthetic/new", "repo", "work/new"},
		{"native-path", "c", "/synthetic/member-a", "other-repo", "work/new"},
		{"native-branch", "c", "/synthetic/new", "repo", "work/member-b"},
	} {
		for _, state := range []string{"pending", "verified"} {
			if err := claim("collision", "work", "project", c.member, c.path, c.repo, c.branch, state); err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed") {
				t.Errorf("%s/%s must refuse uniqueness, got %v", c.name, state, err)
			}
		}
	}
	// Native reservations cross work and Project scopes; branch identity is
	// repository-relative, while path identity is global.
	for _, c := range []struct{ name, path, repo, branch string }{
		{"foreign-path", "/synthetic/integration", "foreign-repo", "work/new"},
		{"foreign-branch", "/synthetic/foreign", "repo", "work/integration"},
	} {
		if err := claim(c.name, "other-work", "other-project", "other-member", c.path, c.repo, c.branch, "pending"); err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed") {
			t.Errorf("%s must retain native uniqueness, got %v", c.name, err)
		}
	}
	if err := claim("other-repo", "work", "project", "c", "/synthetic/other-repo", "other-repo", "work/integration", "pending"); err != nil {
		t.Fatalf("same branch in different repository: %v", err)
	}
	for _, member := range []string{"", "a", "b"} {
		memberMigrationExec(t, db, `INSERT INTO worktree_entries(set_id,project_id,member_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at) VALUES('wts:scope','project',?,'claim','branch','base','/synthetic/entry','repo','active','now')`, member)
		if _, err := db.Exec(`INSERT INTO worktree_entries SELECT * FROM worktree_entries WHERE member_id=?`, member); err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed") {
			t.Fatalf("duplicate entry member %q: %v", member, err)
		}
	}
	for _, query := range []string{
		`UPDATE worktree_entries SET state='invalid' WHERE member_id='a'`,
		`UPDATE worktree_entries SET member_id=NULL WHERE member_id='a'`,
		`UPDATE worktree_claims SET member_id=NULL WHERE op_id='member-a'`,
	} {
		if _, err := db.Exec(query); err == nil {
			t.Fatalf("entry/claim constraint lost: %s", query)
		}
	}
}

func TestMemberClaimMigration155RosterConstraintsAndRetention(t *testing.T) {
	db := memberMigrationOpen155(t)
	controller, actor := memberMigrationSeedOwners(t, db)
	memberMigrationExec(t, db, `INSERT INTO write_guard(active) VALUES(1)`)
	groupSQL := `INSERT INTO workflow_member_groups(work_id,group_id,controller_actor_ref,base_subject,permission,recorded_seq) VALUES('member-work',?,?,'{}',?,1)`
	memberMigrationExec(t, db, groupSQL, "group-a", controller, "first_entry")
	memberMigrationExec(t, db, groupSQL, "group-b", controller, "continue")
	memberSQL := `INSERT INTO workflow_group_members(work_id,group_id,member_id,job_id,job_revision,job_digest,project_id,actor_ref) VALUES('member-work',?,?,?,1,'digest','member-project',?)`
	memberMigrationExec(t, db, memberSQL, "group-a", "a", "job-a", actor)
	memberMigrationExec(t, db, memberSQL, "group-a", "b", "job-b", nil)
	memberMigrationExec(t, db, memberSQL, "group-b", "a", "job-a", nil)
	for _, query := range []string{
		`UPDATE workflow_member_groups SET recorded_seq=0`,
		`UPDATE workflow_member_groups SET base_subject='not-json'`,
		`UPDATE workflow_member_groups SET permission='other'`,
		`UPDATE workflow_member_groups SET controller_actor_ref='missing'`,
		`UPDATE workflow_member_groups SET work_id='missing'`,
		`UPDATE workflow_group_members SET job_revision=0`,
		`UPDATE workflow_group_members SET confirmed=2`,
		`UPDATE workflow_group_members SET project_id='missing'`,
		`UPDATE workflow_group_members SET group_id='missing'`,
		`UPDATE workflow_group_members SET actor_ref='missing'`,
		`UPDATE workflow_group_members SET job_id='job-a' WHERE group_id='group-a' AND member_id='b'`,
		`INSERT INTO workflow_group_members SELECT * FROM workflow_group_members WHERE group_id='group-a' AND member_id='a'`,
	} {
		if _, err := db.Exec(query); err == nil {
			t.Fatalf("roster constraint accepted %s", query)
		}
	}
	if _, err := db.Exec(memberSQL, "group-b", "c", "job-c", actor); err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("actor bound in two groups: %v", err)
	}
	var confirmed int
	var review string
	if err := db.QueryRow(`SELECT confirmed,review_ref FROM workflow_group_members m JOIN workflow_member_groups g USING(work_id,group_id) WHERE m.group_id='group-a' AND member_id='a'`).Scan(&confirmed, &review); err != nil || confirmed != 0 || review != "" {
		t.Fatalf("roster defaults confirmed=%d review=%q err=%v", confirmed, review, err)
	}
	memberMigrationExec(t, db, `UPDATE workflow_group_members SET confirmed=1 WHERE actor_ref=?`, actor)
	memberMigrationExec(t, db, `INSERT INTO workflow_member_callers VALUES(?,'member-work','member-project','group-a','a')`, actor)
	if _, err := db.Exec(`INSERT INTO workflow_member_callers VALUES(?,'foreign-work','foreign-project','foreign-group','foreign-member')`, actor); err == nil {
		t.Fatal("caller tuple overwritten by a second insertion")
	}
	if _, err := db.Exec(`DELETE FROM workflow_actors WHERE actor_ref=?`, actor); err == nil {
		t.Fatal("retained narrowing allowed actor deletion")
	}
	// Scope strings deliberately have no execution FKs. Removing their live
	// owners must leave the same narrowing and authenticated identity intact.
	callerBefore := memberMigrationRows(t, db, `SELECT * FROM workflow_member_callers`)
	actorBefore := memberMigrationRows(t, db, `SELECT * FROM workflow_actors ORDER BY actor_ref`)
	memberMigrationExec(t, db, `DELETE FROM workflow_member_groups WHERE group_id='group-b'`)
	memberMigrationExec(t, db, `DELETE FROM work_items WHERE id='member-work'`)
	memberMigrationExec(t, db, `DELETE FROM projects WHERE id='member-project'`)
	for _, table := range []string{"workflow_member_groups", "workflow_group_members"} {
		if got := memberMigrationRows(t, db, `SELECT * FROM `+table); len(got) != 0 {
			t.Fatalf("execution survived cascade in %s: %v", table, got)
		}
	}
	if got := memberMigrationRows(t, db, `SELECT * FROM workflow_member_callers`); !reflect.DeepEqual(got, callerBefore) {
		t.Fatalf("caller narrowing changed: %v != %v", got, callerBefore)
	}
	if got := memberMigrationRows(t, db, `SELECT * FROM workflow_actors ORDER BY actor_ref`); !reflect.DeepEqual(got, actorBefore) {
		t.Fatalf("authenticated identities changed: %v != %v", got, actorBefore)
	}
	if _, err := db.Exec(`DELETE FROM workflow_actors WHERE actor_ref=?`, actor); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Fatalf("retained narrowing lost restrictive actor FK: %v", err)
	}
	memberMigrationExec(t, db, `DELETE FROM write_guard`)
	if got := memberMigrationRows(t, db, `PRAGMA foreign_key_check`); len(got) != 0 {
		t.Fatalf("foreign key violations: %v", got)
	}
}

func TestMemberClaimMigration155WriteGuards(t *testing.T) {
	db := memberMigrationOpen155(t)
	controller, actor := memberMigrationSeedOwners(t, db)
	statements := []struct{ table, insert, update, delete string }{
		{"workflow_member_groups", fmt.Sprintf(`INSERT INTO workflow_member_groups VALUES('member-work','group','%s','{}','first_entry','',1)`, controller), `UPDATE workflow_member_groups SET review_ref='review'`, `DELETE FROM workflow_member_groups`},
		{"workflow_group_members", `INSERT INTO workflow_group_members VALUES('member-work','group','member','job',1,'digest','member-project',NULL,0)`, `UPDATE workflow_group_members SET confirmed=1`, `DELETE FROM workflow_group_members`},
		{"workflow_member_callers", fmt.Sprintf(`INSERT INTO workflow_member_callers VALUES('%s','member-work','member-project','group','member')`, actor), `UPDATE workflow_member_callers SET member_id='other'`, `DELETE FROM workflow_member_callers`},
	}
	for _, s := range statements {
		if _, err := db.Exec(s.insert); err == nil || !strings.Contains(err.Error(), s.table+" is write-seam-only") {
			t.Fatalf("unguarded insert on %s: %v", s.table, err)
		}
		memberMigrationExec(t, db, `INSERT INTO write_guard(active) VALUES(1)`)
		memberMigrationExec(t, db, s.insert)
		memberMigrationExec(t, db, `DELETE FROM write_guard`)
		for _, query := range []string{s.update, s.delete} {
			if _, err := db.Exec(query); err == nil || !strings.Contains(err.Error(), s.table+" is write-seam-only") {
				t.Fatalf("unguarded mutation on %s: %v", s.table, err)
			}
		}
		memberMigrationExec(t, db, `INSERT INTO write_guard(active) VALUES(1)`)
		memberMigrationExec(t, db, s.update)
		memberMigrationExec(t, db, `DELETE FROM write_guard`)
	}
	memberMigrationExec(t, db, `INSERT INTO write_guard(active) VALUES(1)`)
	// Delete in child-first order to exercise every guarded delete directly.
	for _, i := range []int{1, 0, 2} {
		memberMigrationExec(t, db, statements[i].delete)
	}
	memberMigrationExec(t, db, `DELETE FROM write_guard`)
}

func TestMemberClaimMigration155MovesLatestJobAdmission(t *testing.T) {
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "job-admission-prefix.db"), 154)
	memberMigrationSeedOwners(t, db)
	memberMigrationExec(t, db, `INSERT INTO write_guard(active) VALUES(1)`)
	memberMigrationExec(t, db, `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('other-work','task','Other work','needed',0,1,'now','now')`)
	for _, job := range []struct {
		work, id                       string
		revision                       int
		permission, review, correction string
	}{
		{"member-work", "first", 1, "first_entry", "", ""},
		{"member-work", "continued", 1, "first_entry", "", ""},
		{"member-work", "continued", 2, "continue", "review-older", ""},
		{"member-work", "continued", 3, "continue", "review-current", "correction-current"},
		{"member-work", "corrective-only", 1, "", "review-corrective", "correction-only"},
		{"member-work", "correction-ref-only", 1, "", "", "correction-no-review"},
		{"member-work", "cleared", 1, "continue", "review-cleared", ""},
		{"member-work", "cleared", 2, "", "", ""},
		{"member-work", "unadmitted", 1, "", "", ""},
		{"member-work", "review-only", 1, "", "review-without-permission", ""},
		{"other-work", "continued", 1, "first_entry", "", ""},
	} {
		// This is a schema-154 corpus, not a reconstructed current receipt.
		// Recording metadata is deliberately present on some jobs, but is not
		// an admission sequence or native qualification and cannot become one.
		memberMigrationExec(t, db, `INSERT INTO worker_job_revisions(work_id,job_id,revision,contract_version,objective,stopping_condition,project_scope,path_scope,predicate_ids,checks,prerequisites,unresolved_refs,digest,state,recorded_at,group_permission,group_review_ref,correction_ref,recorded_seq,recorded_payload)
VALUES(?,?,?,1,'Objective','Stop','member-project','[]','[]','[]','[]','[]',?,'recorded','now',?,?,?,?,'{}')`, job.work, job.id, job.revision, "sha256:"+strings.Repeat("a", 64), job.permission, job.review, job.correction, job.revision*10)
	}
	memberMigrationExec(t, db, `DELETE FROM write_guard`)
	// Compare every surviving job column, not just the corrective reference.
	var jobColumns string
	if err := db.QueryRow(`SELECT group_concat('"'||name||'"',',') FROM pragma_table_info('worker_job_revisions') WHERE name NOT IN ('group_permission','group_review_ref')`).Scan(&jobColumns); err != nil {
		t.Fatal(err)
	}
	jobQuery := `SELECT ` + jobColumns + ` FROM worker_job_revisions ORDER BY work_id,job_id,revision`
	beforeJobs := memberMigrationRows(t, db, jobQuery)
	guardQuery := `SELECT type,name,sql FROM sqlite_schema WHERE tbl_name='worker_job_revisions' AND type IN ('index','trigger') ORDER BY name`
	beforeGuards := memberMigrationRows(t, db, guardQuery)
	beforeReceipts := memberMigrationRows(t, db, `SELECT * FROM effect_receipts ORDER BY event_id`)
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`["member-work","continued","continue","review-current",null,null,null]`,
		`["member-work","corrective-only","","review-corrective",null,null,null]`,
		`["member-work","first","first_entry","",null,null,null]`,
		`["member-work","review-only","","review-without-permission",null,null,null]`,
		`["other-work","continued","first_entry","",null,null,null]`,
	}
	if got := memberMigrationRows(t, db, `SELECT work_id,group_id,permission,review_ref,controller_actor_ref,base_subject,recorded_seq FROM workflow_member_groups ORDER BY work_id,group_id`); !reflect.DeepEqual(got, want) {
		t.Fatalf("latest job admissions: got %v, want %v", got, want)
	}
	if got := memberMigrationRows(t, db, jobQuery); !reflect.DeepEqual(got, beforeJobs) {
		t.Fatalf("migration changed retained job facts: got %v, want %v", got, beforeJobs)
	}
	if got := memberMigrationRows(t, db, guardQuery); !reflect.DeepEqual(got, beforeGuards) {
		t.Fatalf("migration changed job guards/indexes: got %v, want %v", got, beforeGuards)
	}
	if got := memberMigrationRows(t, db, `SELECT * FROM effect_receipts ORDER BY event_id`); !reflect.DeepEqual(got, beforeReceipts) {
		t.Fatalf("migration invented receipts: %v", got)
	}
	for _, table := range []string{"workflow_group_members", "workflow_member_callers"} {
		if got := memberMigrationRows(t, db, `SELECT * FROM `+table); len(got) != 0 {
			t.Fatalf("migration invented roster/binding in %s: %v", table, got)
		}
	}
	for _, check := range []struct{ table, column string }{
		{"workflow_member_groups", "contract_version"},
		{"worker_job_revisions", "group_permission"},
		{"worker_job_revisions", "group_review_ref"},
	} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info(?) WHERE name=?`, check.table, check.column).Scan(&count); err != nil || count != 0 {
			t.Fatalf("retired column %s.%s: count=%d err=%v", check.table, check.column, count, err)
		}
	}
	if got := memberMigrationRows(t, db, `PRAGMA foreign_key_check`); len(got) != 0 {
		t.Fatalf("foreign key violations: %v", got)
	}
}
