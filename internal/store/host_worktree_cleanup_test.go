package store

import "testing"

func TestHostWorktreeRowsDoNotBlockCleanup(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		name := "work_removal"
		if terminal {
			name = "terminal_cleanup"
		}
		t.Run(name, func(t *testing.T) {
			s, _ := terminalCleanupFixture(t)
			terminalCleanupSQL(t, s,
				`INSERT INTO bootstrap_operations(idempotency_key,operation_id,request_digest,request_json,product_id,project_id,work_id,repo_path,expected_version,state,created_at,updated_at) VALUES('host-bootstrap','host-bootstrap-operation','sha256:1111111111111111111111111111111111111111111111111111111111111111','{}','prod-alpha','proj-web','work-done','/synthetic/main',1,'pending','now','now')`,
				`INSERT INTO worktree_claims(op_id,work_id,project_id,set_id,pinned_branch,pinned_base_sha,pinned_path,state,principal_ref,request_id,observed_at,updated_at) VALUES('host-claim','work-done','proj-web','wts:work-done','work/closed','1111111111111111111111111111111111111111','/synthetic/work','pending','operator','fixture','now','now')`,
				`INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at) VALUES('wts:work-done','proj-web','host-claim','work/closed','1111111111111111111111111111111111111111','/synthetic/work','repository-fixture','active','now')`,
				`INSERT INTO worktree_occupancy(worktree_id,session_ref,recorded_at,has_process_identity) VALUES('wts:work-done:proj-web:host-claim','session-fixture','now',0)`,
				`INSERT INTO worktree_ref_outcomes(set_id,project_id,claim_op_id,branch,phase,reason,recorded_at) VALUES('wts:work-done','proj-web','host-claim','work/closed','restoration_owed','restore','now')`,
				`INSERT INTO session_vacate_requests VALUES('work-done','session-fixture','proj-web','/synthetic/work','/synthetic/main',1,0)`,
			)
			if terminal {
				if err := terminalCleanupBlockersQ(t.Context(), s.db, "work-done"); err != nil {
					t.Fatalf("host-owned worktree blocked terminal cleanup: %v", err)
				}
			} else {
				req := removalTestRequest()
				req.WorkID = "work-done"
				req.ExpectedVersion = readWorkVersion(t, s, req.WorkID)
				if err := validateRemovalGatesQ(t.Context(), s.db, req); err != nil {
					t.Fatalf("host-owned worktree blocked removal: %v", err)
				}
			}
		})
	}
}

func TestHostWorktreeSessionDoesNotBlockRemovalRequest(t *testing.T) {
	req := removalTestRequest()
	req.Sessions = []WorkRemovalSession{{SessionRef: "host-session", State: "active"}}
	if err := validateRemovalRequest(req); err != nil {
		t.Fatalf("host session blocked removal request: %v", err)
	}
	req.WritesReconciled = false
	assertFailureKind(t, validateRemovalRequest(req), KindInvalidOperation)
}
