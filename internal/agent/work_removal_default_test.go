package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func defaultRemovalFixture(t *testing.T) (*store.Store, *Service, CallEnvelope, map[string]any, string) {
	t.Helper()
	return defaultRemovalGenerationsFixture(t, 1, false)
}

func defaultRemovalGenerationsFixture(t *testing.T, generations int, retain bool) (*store.Store, *Service, CallEnvelope, map[string]any, string) {
	t.Helper()
	ctx := t.Context()
	s, service, grant, root, base := worktreeDispatchFixture(t)
	claim, err := s.ClaimWorktree(ctx, store.WorktreeClaimRequest{OpID: "shelving-claim", WorkID: "work-1", ProjectID: "project-1", BaseSHA: base, PrincipalRef: "human-1", RequestID: "shelving-claim", ExpectedVersion: 2, Now: fixedTime()})
	if err != nil {
		t.Fatal(err)
	}
	for generation := 1; generation < generations; generation++ {
		if _, err := s.ReclaimWorktree(ctx, store.WorktreeReclaimRequest{WorkID: "work-1", ProjectID: "project-1", ExpectedVersion: workVersion(t, s, "work-1"), PrincipalRef: "human-1", RequestID: fmt.Sprintf("early-reclaim-%d", generation), Now: fixedTime(), DefaultRef: "main"}); err != nil {
			t.Fatal(err)
		}
		id := fmt.Sprintf("shelving-claim-%d", generation+1)
		claim, err = s.ClaimWorktree(ctx, store.WorktreeClaimRequest{OpID: id, WorkID: "work-1", ProjectID: "project-1", BaseSHA: base, PrincipalRef: "human-1", RequestID: id, ExpectedVersion: workVersion(t, s, "work-1"), Now: fixedTime()})
		if err != nil {
			t.Fatal(err)
		}
	}
	if retain {
		gitRun(t, claim.Entry.Path, "checkout", "--detach", base)
		gitRun(t, root, "symbolic-ref", "refs/heads/"+claim.Entry.Branch, "refs/heads/main")
	}
	version := workVersion(t, s, "work-1")
	payload, _ := json.Marshal(map[string]any{"from": "needed", "to": "cancelled", "reason": "preserve planning goal", "expected_version": version, "resulting_version": version + 1})
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{{EventID: "shelving-cancel", Kind: "work.transitioned", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("work.transitioned"), Payload: payload}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "work-1"): version}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReclaimWorktree(ctx, store.WorktreeReclaimRequest{WorkID: "work-1", ProjectID: "project-1", ExpectedVersion: version + 1, PrincipalRef: "human-1", RequestID: "shelving-reclaim", Now: fixedTime(), DefaultRef: "main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(claim.Entry.Path); !os.IsNotExist(err) {
		t.Fatalf("retired checkout remains: %v", err)
	}
	resolveProjectAuthority(service, store.ProjectResolution{ProjectID: "project-1", MainWorktree: true})
	scope, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scope)
	env.Directory, env.Worktree = root, root
	env.HostAssertionDigest = "sha256:shelving-host"
	var retirement string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_group_array(json_object('claim_op_id',claim_op_id,'incarnation',claim_incarnation,'branch',branch,'phase',phase,'tip',tip,'reason',reason)) FROM worktree_ref_outcomes WHERE set_id='wts:work-1'`).Scan(&retirement); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"operation_id": "shelving-removal", "idempotency_key": "shelving-key", "work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "reason": "shelved", "actor": "human-1", "product_id": "product-1", "execution_relinquished": true, "writes_reconciled": true, "effects_reconciled": true, "dependencies_resolved": true, "artifacts_verified": true,
		"handoff": store.WorkRemovalHandoff{Findings: []string{fmt.Sprintf("Cancellation shelving-cancel by operator: preserve planning goal; version %d to %d.", version, version+1)}, RemainingScope: []string{"Resume only with new work identity."}, Blockers: []string{"No outside execution approved."}, Artifacts: []string{"Retirement shelving-reclaim; native outcome and recovery identities: " + retirement}, RenewalConditions: []string{"New identity and runtime approval."}}}
	return s, service, env, input, claim.Entry.Path
}

func removalDispatch(t *testing.T, s *store.Store, service *Service, env CallEnvelope, input map[string]any) Envelope {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Dispatch(t.Context(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "remove", Input: raw}, env)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func approveDefaultRemoval(t *testing.T, s *store.Store, service *Service, env CallEnvelope, input map[string]any) CallEnvelope {
	t.Helper()
	challenge := removalDispatch(t, s, service, env, input)
	if challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("eligible shelving must reach exact-request challenge: %+v", challenge.Error)
	}
	ref, _ := challenge.Error.Details["approval_ref"].(string)
	if len(ref) != 64 {
		t.Fatalf("challenge ref=%q", ref)
	}
	raw, _ := json.Marshal(input)
	scope := map[string]any{"product_id": "product-1", "product_ids": []string{"product-1"}, "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": env.ScopeVersion}
	env.HostApproval = signedHostApproval(nil, ref, mutationDigest("concord_work_transition", "remove", env, raw), scope, map[string]any{"work": input["expected_version"]}, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), "")
	input["approval"] = map[string]string{"approval_ref": ref}
	return env
}

func TestDefaultCheckoutShelvingRemoval(t *testing.T) {
	s, service, env, input, _ := defaultRemovalFixture(t)
	env = approveDefaultRemoval(t, s, service, env, input)
	out := removalDispatch(t, s, service, env, input)
	if out.Outcome != OutcomeOK {
		t.Fatalf("approved shelving: %+v", out.Error)
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_items WHERE id='work-1'`) != 0 {
		t.Fatal("execution identity survived")
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_removal_operations WHERE operation_id='shelving-removal' AND state='committed'`) != 1 {
		t.Fatal("missing committed receipt")
	}
	replay := removalDispatch(t, s, service, env, input)
	if replay.Outcome != OutcomeOK || !replay.Replayed {
		t.Fatalf("committed replay: %+v", replay.Error)
	}
	path := s.Path()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted := NewService(reopened)
	restarted.Now = fixedTime
	resolveProjectAuthority(restarted, store.ProjectResolution{ProjectID: "project-1", MainWorktree: true})
	replay = removalDispatch(t, reopened, restarted, env, input)
	if replay.Outcome != OutcomeOK || !replay.Replayed {
		t.Fatalf("reopened removal replay: %+v", replay.Error)
	}
	snapshot, err := Dispatch(t.Context(), reopened, restarted, InvokeRequest{Tool: "concord_product_view", Operation: "snapshot", Input: json.RawMessage(`{"product_id":"product-1"}`)}, env)
	if err != nil || snapshot.Outcome != OutcomeOK || strings.Contains(string(snapshot.Result), `"work-1"`) {
		t.Fatalf("removed identity appears in restarted snapshot: %+v %v", snapshot, err)
	}
	if _, err := reopened.ClaimWorktree(t.Context(), store.WorktreeClaimRequest{OpID: "renew-removed", WorkID: "work-1", ProjectID: "project-1", BaseSHA: gitRun(t, env.Directory, "rev-parse", "HEAD"), ExpectedVersion: 1, PrincipalRef: "human-1", RequestID: "renew-removed", Now: fixedTime()}); err == nil {
		t.Fatal("removed execution identity renewed")
	}
}

func removalFixtureSQL(t *testing.T, s *store.Store, statement string, args ...any) {
	t.Helper()
	tx, err := s.DatabaseForTesting().BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT OR IGNORE INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(statement, args...); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultCheckoutShelvingRefusals(t *testing.T) {
	cases := []struct {
		name, message string
		change        func(*testing.T, *store.Store, map[string]any, string)
	}{
		{"lifecycle", "cancelled lifecycle", func(t *testing.T, s *store.Store, _ map[string]any, _ string) {
			removalFixtureSQL(t, s, `UPDATE work_items SET lifecycle='needed' WHERE id='work-1'`)
		}},
		{"reason", "requires shelving", func(_ *testing.T, _ *store.Store, in map[string]any, _ string) { in["reason"] = "cancelled" }},
		{"missing claims", "recorded retired claims", func(t *testing.T, s *store.Store, _ map[string]any, _ string) {
			removalFixtureSQL(t, s, `DELETE FROM worktree_claims`)
		}},
		{"pending claim", "active worktree claim", func(t *testing.T, s *store.Store, _ map[string]any, _ string) {
			removalFixtureSQL(t, s, `UPDATE worktree_claims SET state='pending'`)
		}},
		{"stale claim", "retirement receipt", func(t *testing.T, s *store.Store, _ map[string]any, _ string) {
			removalFixtureSQL(t, s, `UPDATE worktree_claims SET incarnation=incarnation+1`)
		}},
		{"mismatched retirement outcome", "native retirement outcomes do not match recorded claim identities", func(t *testing.T, s *store.Store, _ map[string]any, path string) {
			removalFixtureSQL(t, s, `UPDATE worktree_ref_outcomes SET path=?`, path+"-other")
		}},
		{"mismatched worktree entry", "worktree entry does not match its retired claim", func(t *testing.T, s *store.Store, _ map[string]any, path string) {
			removalFixtureSQL(t, s, `UPDATE worktree_entries SET path=?`, path+"-other")
		}},
		{"surviving directory", "path survives", func(t *testing.T, _ *store.Store, _ map[string]any, path string) {
			if err := os.MkdirAll(path, 0700); err != nil {
				t.Fatal(err)
			}
		}},
		{"dangling symlink", "path survives", func(t *testing.T, _ *store.Store, _ map[string]any, path string) {
			if err := os.Symlink(path+"-missing", path); err != nil {
				t.Fatal(err)
			}
		}},
		{"unknown occupant", "unknown recorded occupant", func(t *testing.T, s *store.Store, _ map[string]any, _ string) {
			removalFixtureSQL(t, s, `INSERT INTO worktree_occupancy(worktree_id,session_ref,recorded_at,has_process_identity) VALUES('wts:work-1:project-1:shelving-claim','unknown-session','2026-01-01T00:00:00Z',0)`)
		}},
		{"orphaned occupant", "unknown recorded occupant", func(t *testing.T, s *store.Store, _ map[string]any, _ string) {
			removalFixtureSQL(t, s, `INSERT INTO worktree_occupancy(worktree_id,session_ref,recorded_at,has_process_identity) VALUES('wts:work-1:project-1:unknown-claim','unknown-session','2026-01-01T00:00:00Z',0)`)
		}},
		{"safety", "safety evidence is incomplete", func(_ *testing.T, _ *store.Store, in map[string]any, _ string) { in["effects_reconciled"] = false }},
		{"version", "version", func(_ *testing.T, _ *store.Store, in map[string]any, _ string) { in["expected_version"] = int64(1) }},
		{"Linear identity", "no recorded Linear issue", func(_ *testing.T, _ *store.Store, in map[string]any, _ string) {
			in["linear"] = &store.LinearHandoffConfirmation{ProductID: "product-1", RemoteIssueUUID: "wrong-issue", Destination: "linear", HandoffDigest: "sha256:aaaaaaaa"}
		}},
	}
	for _, phase := range []string{store.WorktreeRefPhasePlanned, store.WorktreeRefPhaseCheckoutProvenAbsent, store.WorktreeRefPhaseDeleted, store.WorktreeRefPhaseRestorationOwed, store.WorktreeRefPhaseRestored} {
		cases = append(cases, struct {
			name, message string
			change        func(*testing.T, *store.Store, map[string]any, string)
		}{phase, "still owes cleanup", func(t *testing.T, s *store.Store, _ map[string]any, _ string) {
			removalFixtureSQL(t, s, `UPDATE worktree_ref_outcomes SET phase=?`, phase)
		}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, service, env, in, path := defaultRemovalFixture(t)
			tc.change(t, s, in, path)
			out := removalDispatch(t, s, service, env, in)
			if out.Error == nil || out.Error.Kind == "approval_required" || !strings.Contains(out.Error.Message, tc.message) {
				t.Fatalf("want %q refusal: %+v", tc.message, out.Error)
			}
			if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_items WHERE id='work-1'`) != 1 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_removal_operations`) != 0 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`) != 0 {
				t.Fatal("refusal changed removal or challenge state")
			}
		})
	}
}

func TestDefaultCheckoutShelvingTransactionRaces(t *testing.T) {
	for _, tc := range []struct{ name, sql string }{
		{"version", `UPDATE work_items SET version=version+1 WHERE id='work-1'`},
		{"lifecycle", `UPDATE work_items SET lifecycle='needed' WHERE id='work-1'`},
		{"claim identity", `UPDATE worktree_claims SET pinned_base_sha='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'`},
		{"claim incarnation", `UPDATE worktree_claims SET incarnation=incarnation+1`},
		{"settlement", `UPDATE worktree_ref_outcomes SET settlement_count=settlement_count+1`},
		{"debt", `UPDATE worktree_ref_outcomes SET phase='restoration_owed'`},
		{"occupancy", `INSERT INTO worktree_occupancy(worktree_id,session_ref,recorded_at,has_process_identity) VALUES('wts:work-1:project-1:shelving-claim','unknown-session','2026-01-01T00:00:00Z',0)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, service, env, in, _ := defaultRemovalFixture(t)
			env = approveDefaultRemoval(t, s, service, env, in)
			raw, _ := json.Marshal(in)
			grant, err := service.Authorize(t.Context(), Invocation{ClientRef: env.ClientRef, PrincipalRef: env.PrincipalRef, SessionRef: env.SessionRef, AgentRef: env.AgentRef, Directory: env.Directory, Worktree: env.Worktree, ManifestDigest: env.ManifestDigest, RequiredCapability: "work_transition", RequiredOperation: "remove"})
			if err != nil {
				t.Fatal(err)
			}
			op, _ := agentcontract.ValidateContractOperation("concord_work_transition", "remove")
			r := runtime{Store: s, Authority: service, Envelope: env, Tool: op.Tool, Operation: op.Operation, Reader: grant}
			plan := newMutationPlan(env, op)
			base := NewBase("shelving-race", op.Tool, op.Operation)
			digest := mutationDigest(op.Tool, op.Operation, env, raw)
			if out, err, handled := r.planWorkRemoval(t.Context(), base, raw, digest, grant, op, plan); err != nil || handled {
				t.Fatalf("plan: %+v %v", out, err)
			}
			r.removalProof = plan.removalProof
			removalFixtureSQL(t, s, tc.sql)
			out, err := r.executeMutation(t.Context(), base, raw, digest, plan.scope, plan.versions, plan.consequence, plan.approval, plan.requiresApproval, nil, nil, plan.intents, plan.effect, nil, nil)
			if err != nil || out.Error == nil || out.Error.Kind == "approval_required" {
				t.Fatalf("race must refuse: %+v %v", out, err)
			}
			if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_items WHERE id='work-1'`) != 1 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_removal_operations`) != 0 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals`) != 0 {
				t.Fatal("race crossed removal or approval boundary")
			}
		})
	}
}

func TestDefaultCheckoutShelvingApprovalRollbackAndBindings(t *testing.T) {
	s, service, env, in, _ := defaultRemovalFixture(t)
	env = approveDefaultRemoval(t, s, service, env, in)
	for _, tc := range []struct {
		name   string
		change func(*CallEnvelope)
	}{
		{"session", func(e *CallEnvelope) { e.SessionRef = "other-session" }},
		{"host", func(e *CallEnvelope) { e.HostAssertionDigest = "sha256:other-host" }},
		{"scope", func(e *CallEnvelope) {
			a := *e.HostApproval
			a.Scope = []string{"product_id:other"}
			e.HostApproval = &a
		}},
		{"version", func(e *CallEnvelope) { a := *e.HostApproval; a.Versions = []string{"work:1"}; e.HostApproval = &a }},
		{"digest", func(e *CallEnvelope) { a := *e.HostApproval; a.RequestDigest = "sha256:other"; e.HostApproval = &a }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := env
			tc.change(&changed)
			out := removalDispatch(t, s, service, changed, in)
			if out.Error == nil || out.Error.Kind != "approval_invalid" {
				t.Fatalf("binding: %+v", out.Error)
			}
		})
	}
	if _, err := s.DatabaseForTesting().Exec(`CREATE TRIGGER removal_test_rollback BEFORE UPDATE ON work_removal_operations BEGIN SELECT RAISE(ABORT,'removal rollback test'); END`); err != nil {
		t.Fatal(err)
	}
	out := removalDispatch(t, s, service, env, in)
	if out.Error == nil {
		t.Fatal("injected fold failure passed")
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_items WHERE id='work-1'`) != 1 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_removal_operations`) != 0 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals`) != 0 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges WHERE used_count=0 AND status='active'`) != 1 {
		t.Fatal("rollback left a partial effect or consumed approval")
	}
	if _, err := s.DatabaseForTesting().Exec(`DROP TRIGGER removal_test_rollback`); err != nil {
		t.Fatal(err)
	}
	out = removalDispatch(t, s, service, env, in)
	if out.Outcome != OutcomeOK {
		t.Fatalf("same approval after rollback: %+v", out.Error)
	}
	for _, tc := range []struct {
		name   string
		change func(*CallEnvelope, map[string]any)
	}{
		{"authority", func(e *CallEnvelope, _ map[string]any) { e.ClientRef = "unregistered-client" }},
		{"digest", func(_ *CallEnvelope, in map[string]any) { in["reason"] = "cancelled" }},
		{"key", func(_ *CallEnvelope, in map[string]any) { in["idempotency_key"] = "other-key" }},
		{"scope", func(e *CallEnvelope, _ map[string]any) { e.SelectedProductID = "other-product" }},
	} {
		t.Run("replay "+tc.name, func(t *testing.T) {
			changed := env
			input := map[string]any{}
			for k, v := range in {
				input[k] = v
			}
			tc.change(&changed, input)
			out := removalDispatch(t, s, service, changed, input)
			if out.Error == nil || out.Error.Kind == "approval_required" {
				t.Fatalf("changed replay: %+v", out)
			}
		})
	}
	// A fresh service simulates a lost response and process restart. Neither
	// live rows nor the old in-memory plan supply the replay authority.
	restarted := NewService(s)
	restarted.Now = fixedTime
	resolveProjectAuthority(restarted, store.ProjectResolution{ProjectID: "project-1", MainWorktree: true})
	env.HostApproval = nil
	env.SessionRef = "new-authorized-session"
	env.HostAssertionDigest = "sha256:new-host"
	out = removalDispatch(t, s, restarted, env, in)
	if out.Outcome != OutcomeOK || !out.Replayed {
		t.Fatalf("same-operation reconciliation: %+v", out.Error)
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`) != 1 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_removal_operations`) != 1 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_items WHERE id='work-1'`) != 0 {
		t.Fatal("new-session replay recreated execution, receipt, or consent")
	}
}

func TestDefaultCheckoutShelvingHistoricalRetirement(t *testing.T) {
	t.Run("earlier generation debt", func(t *testing.T) {
		s, service, env, in, _ := defaultRemovalGenerationsFixture(t, 2, false)
		if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worktree_claims WHERE state='reclaimed'`) != 2 {
			t.Fatal("fixture lacks two retired generations")
		}
		removalFixtureSQL(t, s, `UPDATE worktree_ref_outcomes SET phase='restoration_owed' WHERE claim_op_id='shelving-claim'`)
		out := removalDispatch(t, s, service, env, in)
		if out.Error == nil || !strings.Contains(out.Error.Message, "still owes cleanup") {
			t.Fatalf("earlier-generation debt: %+v", out.Error)
		}
		if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`) != 0 {
			t.Fatal("debt issued a challenge")
		}
	})
	t.Run("retained symbolic ref", func(t *testing.T) {
		s, service, env, in, _ := defaultRemovalGenerationsFixture(t, 1, true)
		if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worktree_ref_outcomes WHERE phase='retained_unproven'`) != 1 {
			t.Fatal("fixture lacks native retained-ref proof")
		}
		env = approveDefaultRemoval(t, s, service, env, in)
		out := removalDispatch(t, s, service, env, in)
		if out.Outcome != OutcomeOK {
			t.Fatalf("retained candidate: %+v", out.Error)
		}
		if got := gitRun(t, env.Directory, "symbolic-ref", "refs/heads/work/work-1"); got != "refs/heads/main" {
			t.Fatalf("protected symbolic ref changed: %s", got)
		}
		var handoff store.WorkRemovalHandoff
		var raw string
		if err := s.DatabaseForTesting().QueryRow(`SELECT handoff_json FROM work_removal_operations WHERE operation_id='shelving-removal'`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(raw), &handoff); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(handoff.Findings, " "), "Cancellation shelving-cancel by operator") || !strings.Contains(strings.Join(handoff.Artifacts, " "), "retained_unproven") || !strings.Contains(strings.Join(handoff.Artifacts, " "), "work/work-1") {
			t.Fatalf("receipt lost cancellation or retained recovery facts: %+v", handoff)
		}
	})
}

func TestDefaultCheckoutShelvingScopeAndWorkflow(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*testing.T, *store.Store)
	}{
		{"multiple Projects", func(t *testing.T, s *store.Store) {
			removalFixtureSQL(t, s, `INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('project-2','Second project',1,'now','now')`)
			removalFixtureSQL(t, s, `INSERT INTO product_projects(product_id,project_id,role) VALUES('product-1','project-2','secondary')`)
			removalFixtureSQL(t, s, `INSERT INTO work_projects(work_id,project_id,role) VALUES('work-1','project-2','secondary')`)
		}},
		{"wrong sole Project", func(t *testing.T, s *store.Store) {
			removalFixtureSQL(t, s, `INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('project-2','Second project',1,'now','now')`)
			removalFixtureSQL(t, s, `INSERT INTO product_projects(product_id,project_id,role) VALUES('product-1','project-2','secondary')`)
			removalFixtureSQL(t, s, `UPDATE work_projects SET project_id='project-2' WHERE work_id='work-1'`)
		}},
		{"multiple Products", func(t *testing.T, s *store.Store) {
			removalFixtureSQL(t, s, `INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('product-2','Second Product','prototype','operator_only',1,'now','now')`)
			removalFixtureSQL(t, s, `INSERT INTO product_projects(product_id,project_id,role) VALUES('product-2','project-1','secondary')`)
		}},
		{"uncancelled workflow", func(t *testing.T, s *store.Store) { seedContinuityWorkflow(t, s, "work-1") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, service, env, in, _ := defaultRemovalFixture(t)
			tc.change(t, s)
			scope, _, err := s.ScopeVersion(t.Context(), "project-1")
			if err != nil {
				t.Fatal(err)
			}
			env.ScopeVersion = scope
			out := removalDispatch(t, s, service, env, in)
			if out.Error == nil || out.Error.Kind == "approval_required" {
				t.Fatalf("ineligible scope or workflow: %+v", out)
			}
			if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_items WHERE id='work-1'`) != 1 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_removal_operations`) != 0 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`) != 0 {
				t.Fatal("ineligible scope or workflow crossed the boundary")
			}
		})
	}
}

func TestDefaultCheckoutShelvingManagedRestore(t *testing.T) {
	ctx := t.Context()
	s, service, grant, root, _ := worktreeDispatchFixture(t)
	seedContinuityWorkflow(t, s, "work-1")
	first, err := s.BootstrapExistingWorktree(ctx, store.ExistingBootstrapRequest{ProductID: "product-1", ProjectID: "project-1", WorkID: "work-1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReclaimWorktree(ctx, store.WorktreeReclaimRequest{WorkID: "work-1", ProjectID: "project-1", ExpectedVersion: first.WorkVersion, PrincipalRef: "human-1", RequestID: "managed-first-retirement", DefaultRef: "main"}); err != nil {
		t.Fatal(err)
	}
	var originalReceipt string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_group_array(json_array(event_id,payload_digest)) FROM effect_receipts WHERE kind='work.worktree_reclaimed' AND subject_id='work-1'`).Scan(&originalReceipt); err != nil {
		t.Fatal(err)
	}
	second, err := s.BootstrapExistingWorktree(ctx, store.ExistingBootstrapRequest{ProductID: "product-1", ProjectID: "project-1", WorkID: "work-1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.OperationID != first.OperationID || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worktree_claims WHERE work_id='work-1' AND incarnation=1 AND state='verified'`) != 1 {
		t.Fatal("bootstrap did not reopen the same claim as a second incarnation")
	}
	// The approved contract predates cancellation. Cancellation must retain
	// its definition and law pins rather than synthesize replacement evidence.
	const lawHash = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	execProjection(t, s, []string{
		`INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('project-1','path-1','spec:shelving-fixture','spec','accepted','.concord/docs/shelving-fixture.md','Synthetic shelving law',?,'fixture')`,
		`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) SELECT 'work-1',1,'Preserve the planning goal','internal_sqlite','[]','[]','2026-08-08T12:00:00Z',execution_actor_ref,'["spec:shelving-fixture"]','[]',1,'prototype_internal' FROM workflow_instances WHERE work_id='work-1'`,
		`INSERT INTO workflow_contract_law_revisions(work_id,contract_version,law_id,content_hash) VALUES('work-1',1,'spec:shelving-fixture',?)`,
	}, [][]any{{lawHash}, nil, {lawHash}})
	var definitionPin string
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_digest FROM workflow_instances WHERE work_id='work-1'`).Scan(&definitionPin); err != nil {
		t.Fatal(err)
	}
	resolveProjectAuthority(service, store.ProjectResolution{ProjectID: "project-1", MainWorktree: true})
	scope, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scope)
	env.Directory, env.Worktree = root, root
	env.HostAssertionDigest = "sha256:managed-shelving-host"
	version := workVersion(t, s, "work-1")
	cancel := map[string]any{"work_id": "work-1", "expected_version": version, "target": "cancelled", "reason": "Preserve planning while retiring execution", "idempotency_key": "managed-cancellation",
		"evidence": []EvidenceRef{{Kind: "verification", Authority: "fixture-verifier", LocatorKind: "artifact", Locator: "evidence:managed-cancellation"}}}
	raw, _ := json.Marshal(cancel)
	req := InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: raw}
	challenge, err := Dispatch(ctx, s, service, req, env)
	if err != nil || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("managed cancellation challenge: %+v %v", challenge.Error, err)
	}
	ref, _ := challenge.Error.Details["approval_ref"].(string)
	env.HostApproval = signedHostApproval(nil, ref, mutationDigest(req.Tool, req.Operation, env, raw), map[string]any{"product_id": "product-1", "product_ids": []string{"product-1"}, "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scope}, map[string]any{"work": version}, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), "")
	cancel["approval"] = map[string]string{"approval_ref": ref}
	req.Input, _ = json.Marshal(cancel)
	cancelled, err := Dispatch(ctx, s, service, req, env)
	if err != nil || cancelled.Outcome != OutcomeOK {
		t.Fatalf("managed cancellation: %+v %v", cancelled.Error, err)
	}
	var cancellation string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_object('lifecycle',w.lifecycle,'workflow_state',i.instance_state,'definition',i.definition_digest,'reason',w.transition_reason,'actor',w.transition_actor,'evidence',json(w.transition_evidence_refs),'law',l.law_id,'law_hash',l.content_hash) FROM work_items w JOIN workflow_instances i ON i.work_id=w.id JOIN workflow_contract_law_revisions l ON l.work_id=w.id WHERE w.id='work-1' AND w.lifecycle='cancelled' AND i.instance_state='cancelled' AND i.definition_digest=? AND l.content_hash=?`, definitionPin, lawHash).Scan(&cancellation); err != nil {
		t.Fatalf("cancellation lost workflow/law pins: %v", err)
	}
	if !strings.Contains(cancellation, "evidence:managed-cancellation") || !strings.Contains(cancellation, "Preserve planning while retiring execution") {
		t.Fatalf("missing cancellation reason/evidence: %s", cancellation)
	}
	if _, err := s.ReclaimWorktree(ctx, store.WorktreeReclaimRequest{WorkID: "work-1", ProjectID: "project-1", ExpectedVersion: workVersion(t, s, "work-1"), PrincipalRef: "human-1", RequestID: "managed-final-retirement", DefaultRef: "main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(second.Entry.Path); !os.IsNotExist(err) {
		t.Fatalf("second incarnation survives retirement: %v", err)
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM effect_receipts WHERE kind='work.worktree_reclaimed' AND subject_id='work-1'`) != 2 || countRows(t, s.DatabaseForTesting(), `SELECT count(DISTINCT claim_incarnation) FROM worktree_ref_outcomes WHERE set_id='wts:work-1'`) != 2 {
		t.Fatal("missing separate retirement receipts/outcomes for both incarnations")
	}
	var preservedReceipt, retirement string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_group_array(json_array(event_id,payload_digest)) FROM effect_receipts WHERE kind='work.worktree_reclaimed' AND subject_id='work-1' AND event_id=?`, "work-1:project-1:"+first.OperationID+":worktree-reclaimed").Scan(&preservedReceipt); err != nil {
		t.Fatal(err)
	}
	if preservedReceipt != originalReceipt {
		t.Fatalf("first incarnation receipt changed: %s -> %s", originalReceipt, preservedReceipt)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_group_array(json_object('claim',claim_op_id,'incarnation',claim_incarnation,'phase',phase,'reason',reason)) FROM worktree_ref_outcomes WHERE set_id='wts:work-1'`).Scan(&retirement); err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		_, err := store.RecordLinearIssueLinkTx(ctx, tx, store.LinearIssueLink{WorkID: "work-1", RemoteIssueUUID: "remote-managed-shelving", HumanKey: "CON-1", URL: "https://linear.app/example/issue/CON-1"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	handoff := store.WorkRemovalHandoff{Findings: []string{cancellation}, RemainingScope: []string{"Planning goal remains in the recorded issue."}, Blockers: []string{"New execution needs a new identity."}, Artifacts: []string{retirement}, RenewalConditions: []string{"Capture a new work identity."}}
	handoffJSON, _ := json.Marshal(handoff)
	handoffHash := sha256.Sum256(handoffJSON)
	in := map[string]any{"operation_id": "managed-removal", "idempotency_key": "managed-removal-key", "work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "reason": "shelved", "actor": "human-1", "product_id": "product-1", "execution_relinquished": true, "writes_reconciled": true, "effects_reconciled": true, "dependencies_resolved": true, "artifacts_verified": true, "handoff": handoff,
		"linear": store.LinearHandoffConfirmation{ProductID: "product-1", RemoteIssueUUID: "remote-managed-shelving", Destination: "linear", HandoffDigest: fmt.Sprintf("sha256:%x", handoffHash)}}
	env.HostApproval = nil
	env.ScopeVersion, _, err = s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("earlier incarnation debt", func(t *testing.T) {
		var phase string
		if err := s.DatabaseForTesting().QueryRow(`SELECT phase FROM worktree_ref_outcomes WHERE claim_incarnation=0`).Scan(&phase); err != nil {
			t.Fatal(err)
		}
		removalFixtureSQL(t, s, `UPDATE worktree_ref_outcomes SET phase='restoration_owed' WHERE claim_incarnation=0`)
		defer removalFixtureSQL(t, s, `UPDATE worktree_ref_outcomes SET phase=? WHERE claim_incarnation=0`, phase)
		out := removalDispatch(t, s, service, env, in)
		if out.Error == nil || !strings.Contains(out.Error.Message, "still owes cleanup") {
			t.Fatalf("earlier incarnation debt: %+v", out.Error)
		}
	})
	env = approveDefaultRemoval(t, s, service, env, in)
	out := removalDispatch(t, s, service, env, in)
	if out.Outcome != OutcomeOK {
		t.Fatalf("managed shelving: %+v", out.Error)
	}
	var retainedHandoff, retainedLinear string
	if err := s.DatabaseForTesting().QueryRow(`SELECT handoff_json,linear_confirmation_json FROM work_removal_operations WHERE operation_id='managed-removal' AND state='committed'`).Scan(&retainedHandoff, &retainedLinear); err != nil {
		t.Fatal(err)
	}
	if retainedHandoff != string(handoffJSON) || !strings.Contains(retainedLinear, "remote-managed-shelving") || !strings.Contains(retainedLinear, fmt.Sprintf("sha256:%x", handoffHash)) {
		t.Fatalf("receipt lost handoff or confirmed identity: %s %s", retainedHandoff, retainedLinear)
	}
	backup := filepath.Join(t.TempDir(), "shelved.db")
	if _, err := store.Backup(ctx, s, backup); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored.db")
	if _, err := store.RestoreBackup(ctx, backup, destination); err != nil {
		t.Fatal(err)
	}
	restored, err := store.Open(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if countRows(t, restored.DatabaseForTesting(), `SELECT count(*) FROM work_items WHERE id='work-1'`) != 0 || countRows(t, restored.DatabaseForTesting(), `SELECT count(*) FROM workflow_instances WHERE work_id='work-1'`) != 0 || countRows(t, restored.DatabaseForTesting(), `SELECT count(*) FROM work_removal_operations WHERE operation_id='managed-removal' AND state='committed'`) != 1 {
		t.Fatal("restore resurrected removed execution or lost its receipt")
	}
	var restoredHandoff, restoredLinear string
	if err := restored.DatabaseForTesting().QueryRow(`SELECT handoff_json,linear_confirmation_json FROM work_removal_operations WHERE operation_id='managed-removal'`).Scan(&restoredHandoff, &restoredLinear); err != nil {
		t.Fatal(err)
	}
	if restoredHandoff != retainedHandoff || restoredLinear != retainedLinear {
		t.Fatal("restore changed the cancellation, pin, retirement, or handoff evidence")
	}
	restarted := NewService(restored)
	restarted.Now = fixedTime
	resolveProjectAuthority(restarted, store.ProjectResolution{ProjectID: "project-1", MainWorktree: true})
	env.HostApproval = nil
	env.SessionRef = "restored-session"
	out = removalDispatch(t, restored, restarted, env, in)
	if out.Outcome != OutcomeOK || !out.Replayed {
		t.Fatalf("restored retained replay: %+v", out.Error)
	}
	snapshot, err := Dispatch(ctx, restored, restarted, InvokeRequest{Tool: "concord_product_view", Operation: "snapshot", Input: json.RawMessage(`{"product_id":"product-1"}`)}, env)
	if err != nil || snapshot.Outcome != OutcomeOK || strings.Contains(string(snapshot.Result), `"work-1"`) {
		t.Fatalf("restored snapshot resurrected execution: %+v %v", snapshot, err)
	}
}
