package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

func defaultRemovalFixture(t *testing.T) (*store.Store, *Service, CallEnvelope, map[string]any) {
	t.Helper()
	s, service, grant, root := worktreeDispatchFixture(t)
	seedWorkTransition(t, s, "work-1", "needed", "cancelled", 2)
	resolveProjectAuthority(service, store.ProjectResolution{ProjectID: "project-1", MainWorktree: true})
	scope, _, err := s.ScopeVersion(t.Context(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scope)
	env.Directory, env.Worktree = root, root
	env.HostAssertionDigest = "sha256:shelving-host"
	in := map[string]any{"operation_id": "shelving-removal", "idempotency_key": "shelving-key", "work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "reason": "shelved", "actor": "human-1", "product_id": "product-1", "execution_relinquished": true, "writes_reconciled": true, "effects_reconciled": true, "dependencies_resolved": true, "artifacts_verified": true,
		"handoff": store.WorkRemovalHandoff{Findings: []string{"The work was cancelled to preserve its planning goal."}, RemainingScope: []string{"Resume only with new work identity."}, Blockers: []string{"No outside execution approved."}, Artifacts: []string{"The cancellation remains in the preservation record."}, RenewalConditions: []string{"New identity and runtime approval."}}}
	return s, service, env, in
}

func removalDispatch(t *testing.T, s *store.Store, service *Service, env CallEnvelope, input map[string]any) agentcontract.Envelope {
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

func TestDefaultCheckoutShelvingRemoval(t *testing.T) {
	s, service, env, in := defaultRemovalFixture(t)
	out := removalDispatch(t, s, service, env, in)
	if out.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("consent-free shelving: %+v", out.Error)
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_items WHERE id='work-1'`) != 0 {
		t.Fatal("execution identity survived")
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_removal_operations WHERE operation_id='shelving-removal' AND state='committed'`) != 1 {
		t.Fatal("missing committed receipt")
	}
	out = removalDispatch(t, s, service, env, in)
	if out.Outcome != agentcontract.OutcomeOK || !out.Replayed {
		t.Fatalf("committed replay: %+v", out.Error)
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
	out = removalDispatch(t, reopened, restarted, env, in)
	if out.Outcome != agentcontract.OutcomeOK || !out.Replayed {
		t.Fatalf("reopened removal replay: %+v", out.Error)
	}
	snapshot, err := Dispatch(t.Context(), reopened, restarted, InvokeRequest{Tool: "concord_product_view", Operation: "snapshot", Input: json.RawMessage(`{"product_id":"product-1"}`)}, env)
	if err != nil || snapshot.Outcome != agentcontract.OutcomeOK || strings.Contains(string(snapshot.Result), `"work-1"`) {
		t.Fatalf("removed identity appears in restarted snapshot: %+v %v", snapshot, err)
	}
}

func removalFixtureSQL(t *testing.T, s *store.Store, statement string) {
	t.Helper()
	execProjection(t, s, []string{statement}, [][]any{nil})
}

func TestDefaultCheckoutShelvingRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		change        func(*testing.T, *store.Store, map[string]any)
	}{
		{"lifecycle", "cancelled lifecycle", func(t *testing.T, s *store.Store, _ map[string]any) {
			removalFixtureSQL(t, s, `UPDATE work_items SET lifecycle='needed' WHERE id='work-1'`)
		}},
		{"reason", "requires shelving", func(_ *testing.T, _ *store.Store, in map[string]any) { in["reason"] = "cancelled" }},
		{"safety", "safety evidence is incomplete", func(_ *testing.T, _ *store.Store, in map[string]any) { in["effects_reconciled"] = false }},
		{"version", "version", func(_ *testing.T, _ *store.Store, in map[string]any) { in["expected_version"] = int64(1) }},
		{"Linear identity", "no recorded Linear issue", func(_ *testing.T, _ *store.Store, in map[string]any) {
			in["linear"] = &store.LinearHandoffConfirmation{ProductID: "product-1", RemoteIssueUUID: "wrong-issue", Destination: "linear", HandoffDigest: "sha256:aaaaaaaa"}
		}},
		{"completed lifecycle", "cancelled lifecycle", func(t *testing.T, s *store.Store, _ map[string]any) {
			removalFixtureSQL(t, s, `UPDATE work_items SET lifecycle='completed' WHERE id='work-1'`)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, service, env, in := defaultRemovalFixture(t)
			tc.change(t, s, in)
			out := removalDispatch(t, s, service, env, in)
			if out.Error == nil || !strings.Contains(out.Error.Message, tc.message) {
				t.Fatalf("want %q refusal: %+v", tc.message, out.Error)
			}
			if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_items WHERE id='work-1'`) != 1 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_removal_operations`) != 0 {
				t.Fatal("refusal changed removal state")
			}
		})
	}
}

func TestDefaultCheckoutShelvingTransactionRaces(t *testing.T) {
	for _, tc := range []struct{ name, sql string }{
		{"version", `UPDATE work_items SET version=version+1 WHERE id='work-1'`},
		{"lifecycle", `UPDATE work_items SET lifecycle='needed' WHERE id='work-1'`},
		{"completed lifecycle", `UPDATE work_items SET lifecycle='completed' WHERE id='work-1'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, service, env, in := defaultRemovalFixture(t)
			raw, _ := json.Marshal(in)
			grant, err := service.Authorize(t.Context(), Invocation{ClientRef: env.ClientRef, PrincipalRef: env.PrincipalRef, SessionRef: env.SessionRef, AgentRef: env.AgentRef, Directory: env.Directory, Worktree: env.Worktree, ManifestDigest: env.ManifestDigest, RequiredCapability: "work_transition", RequiredOperation: "remove"})
			if err != nil {
				t.Fatal(err)
			}
			op, _ := agentcontract.ValidateContractOperation("concord_work_transition", "remove")
			r := runtime{Store: s, Authority: service, Envelope: env, Tool: op.Tool, Operation: op.Operation, Reader: grant}
			plan := newMutationPlan(env)
			base := agentcontract.NewBase("shelving-race", op.Tool, op.Operation)
			digest := mutationDigest(op.Tool, op.Operation, env, raw)
			if out, err, handled := r.planWorkRemoval(t.Context(), base, raw, digest, grant, op, plan); err != nil || handled {
				t.Fatalf("plan: %+v %v", out, err)
			}
			removalFixtureSQL(t, s, tc.sql)
			out, err := r.executeMutation(t.Context(), base, raw, digest, plan.scope, nil, plan.intents, plan.effect)
			if err != nil || out.Error == nil {
				t.Fatalf("race must refuse: %+v %v", out, err)
			}
			if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_items WHERE id='work-1'`) != 1 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_removal_operations`) != 0 {
				t.Fatal("race crossed removal boundary")
			}
		})
	}
}

func TestDefaultCheckoutShelvingRollbackAndReplay(t *testing.T) {
	s, service, env, in := defaultRemovalFixture(t)
	if _, err := s.DatabaseForTesting().Exec(`CREATE TRIGGER removal_test_rollback BEFORE UPDATE ON work_removal_operations BEGIN SELECT RAISE(ABORT,'removal rollback test'); END`); err != nil {
		t.Fatal(err)
	}
	out := removalDispatch(t, s, service, env, in)
	if out.Error == nil || !strings.Contains(out.Error.Message, "removal rollback test") {
		t.Fatalf("want injected fold failure: %+v", out.Error)
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_items WHERE id='work-1'`) != 1 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_removal_operations`) != 0 {
		t.Fatal("rollback left a partial removal effect")
	}
	if _, err := s.DatabaseForTesting().Exec(`DROP TRIGGER removal_test_rollback`); err != nil {
		t.Fatal(err)
	}
	out = removalDispatch(t, s, service, env, in)
	if out.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("consent-free retry after rollback: %+v", out.Error)
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
			if out.Error == nil {
				t.Fatalf("changed replay: %+v", out)
			}
		})
	}
	restarted := NewService(s)
	restarted.Now = fixedTime
	resolveProjectAuthority(restarted, store.ProjectResolution{ProjectID: "project-1", MainWorktree: true})
	env.SessionRef = "new-authorized-session"
	env.HostAssertionDigest = "sha256:new-host"
	out = removalDispatch(t, s, restarted, env, in)
	if out.Outcome != agentcontract.OutcomeOK || !out.Replayed {
		t.Fatalf("same-operation reconciliation: %+v", out.Error)
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_removal_operations`) != 1 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_items WHERE id='work-1'`) != 0 {
		t.Fatal("new-session replay recreated execution, receipt, or consent")
	}
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
			s, service, env, in := defaultRemovalFixture(t)
			tc.change(t, s)
			scope, _, err := s.ScopeVersion(t.Context(), "project-1")
			if err != nil {
				t.Fatal(err)
			}
			env.ScopeVersion = scope
			out := removalDispatch(t, s, service, env, in)
			if out.Error == nil {
				t.Fatalf("ineligible scope or workflow: %+v", out)
			}
			if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_items WHERE id='work-1'`) != 1 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_removal_operations`) != 0 {
				t.Fatal("ineligible scope or workflow crossed the boundary")
			}
		})
	}
}

func TestDefaultCheckoutShelvingManagedRestore(t *testing.T) {
	ctx := t.Context()
	s, service, grant, root := worktreeDispatchFixture(t)
	seedContinuityWorkflow(t, s, "work-1")
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_law_baselines WHERE work_id='work-1'`) != 1 {
		t.Fatal("fixture lacks its owning-start law baseline")
	}
	// The contract's mandated law is committed content at the work's resolved
	// canonical home (law-folder-authority.md, Committed reads): the fixture
	// commits the independent registry and the pinned law into the home's
	// repository, then pins the committed whole-file revision.
	registry := store.KnowledgeDomainRegistry{
		SchemaVersion: "1.0",
		ProductKey:    "shelving-fixture",
		RootDomainID:  "product-root:shelving-fixture",
		Domains: []store.KnowledgeDomain{{DomainID: "product-root:shelving-fixture", Name: "Shelving fixture root",
			Purpose: "Product law fixture", Status: "current", ArchitectureRelations: []store.KnowledgeArchitectureRelation{}}},
	}
	if err := pm1fixture.WriteKnowledgeShards(root, registry); err != nil {
		t.Fatal(err)
	}
	// pm1fixture writes the registry at its existing repository path but leaves
	// it unstaged; the law commit below carries both into the committed state.
	gitRun(t, root, "add", "--", ".concord/docs/knowledge/domain-registry.json")
	lawHash := commitAgentFixtureLaw(t, root, ".concord/law/specs/shelving-fixture.md",
		agentFixtureLawFrontmatter("spec:shelving-fixture", "derived", registry.RootDomainID, "The shelving fixture registry declares no child Domain, so this law has no child home available."))
	execProjection(t, s, []string{
		`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) SELECT 'work-1',1,'Preserve the planning goal','internal_sqlite','[]','[]','2026-08-08T12:00:00Z',execution_actor_ref,'["spec:shelving-fixture"]','[]',1,'prototype_internal' FROM workflow_instances WHERE work_id='work-1'`,
		`INSERT INTO workflow_contract_law_revisions(work_id,contract_version,law_id,content_hash) VALUES('work-1',1,'spec:shelving-fixture',?)`,
	}, [][]any{nil, {lawHash}})
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
	cancel := map[string]any{"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "target": "cancelled", "reason": "Preserve planning while retiring execution", "idempotency_key": "managed-cancellation",
		"evidence": []agentcontract.EvidenceRef{{Kind: "verification", Authority: "fixture-verifier", LocatorKind: "artifact", Locator: "evidence:managed-cancellation"}}}
	raw, _ := json.Marshal(cancel)
	cancelled, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: raw}, env)
	if err != nil || cancelled.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("managed cancellation: %+v %v", cancelled.Error, err)
	}
	var cancellation string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_object('lifecycle',w.lifecycle,'workflow_state',i.instance_state,'definition',i.definition_digest,'reason',w.transition_reason,'actor',w.transition_actor,'evidence',json(w.transition_evidence_refs),'law',l.law_id,'law_hash',l.content_hash) FROM work_items w JOIN workflow_instances i ON i.work_id=w.id JOIN workflow_contract_law_revisions l ON l.work_id=w.id WHERE w.id='work-1' AND w.lifecycle='cancelled' AND i.instance_state='cancelled' AND i.definition_digest=? AND l.content_hash=?`, definitionPin, lawHash).Scan(&cancellation); err != nil {
		t.Fatalf("cancellation lost workflow/law pins: %v", err)
	}
	if !strings.Contains(cancellation, "evidence:managed-cancellation") || !strings.Contains(cancellation, "Preserve planning while retiring execution") {
		t.Fatalf("missing cancellation reason/evidence: %s", cancellation)
	}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		_, err := store.RecordLinearIssueLinkTx(ctx, tx, store.LinearIssueLink{WorkID: "work-1", RemoteIssueUUID: "remote-managed-shelving", HumanKey: "CON-1", URL: "https://linear.app/example/issue/CON-1"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	handoff := store.WorkRemovalHandoff{Findings: []string{cancellation}, RemainingScope: []string{"Planning goal remains in the recorded issue."}, Blockers: []string{"New execution needs a new identity."}, Artifacts: []string{"The verified cancellation evidence remains in this handoff."}, RenewalConditions: []string{"Capture a new work identity."}}
	handoffJSON, _ := json.Marshal(handoff)
	handoffHash := sha256.Sum256(handoffJSON)
	in := map[string]any{"operation_id": "managed-removal", "idempotency_key": "managed-removal-key", "work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "reason": "shelved", "actor": "human-1", "product_id": "product-1", "execution_relinquished": true, "writes_reconciled": true, "effects_reconciled": true, "dependencies_resolved": true, "artifacts_verified": true, "handoff": handoff,
		"linear": store.LinearHandoffConfirmation{ProductID: "product-1", RemoteIssueUUID: "remote-managed-shelving", Destination: "linear", HandoffDigest: fmt.Sprintf("sha256:%x", handoffHash)}}
	env.ScopeVersion, _, err = s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	out := removalDispatch(t, s, service, env, in)
	if out.Outcome != agentcontract.OutcomeOK {
		if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_items WHERE id='work-1'`) != 1 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_removal_operations`) != 0 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_law_baselines WHERE work_id='work-1'`) != 1 {
			t.Fatalf("failed managed shelving left a partial removal effect: %+v", out.Error)
		}
		t.Fatalf("managed shelving: %+v", out.Error)
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_law_baselines WHERE work_id='work-1'`) != 0 {
		t.Fatal("removed work retained its owning-start law baseline")
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
	if countRows(t, restored.DatabaseForTesting(), `SELECT count(*) FROM workflow_law_baselines WHERE work_id='work-1'`) != 0 {
		t.Fatal("restore resurrected the removed work's law baseline")
	}
	if countRows(t, restored.DatabaseForTesting(), `SELECT count(*) FROM work_items WHERE id='work-1'`) != 0 || countRows(t, restored.DatabaseForTesting(), `SELECT count(*) FROM workflow_instances WHERE work_id='work-1'`) != 0 || countRows(t, restored.DatabaseForTesting(), `SELECT count(*) FROM work_removal_operations WHERE operation_id='managed-removal' AND state='committed'`) != 1 {
		t.Fatal("restore resurrected removed execution or lost its receipt")
	}
	var restoredHandoff, restoredLinear string
	if err := restored.DatabaseForTesting().QueryRow(`SELECT handoff_json,linear_confirmation_json FROM work_removal_operations WHERE operation_id='managed-removal'`).Scan(&restoredHandoff, &restoredLinear); err != nil {
		t.Fatal(err)
	}
	if restoredHandoff != retainedHandoff || restoredLinear != retainedLinear {
		t.Fatal("restore changed the cancellation, pin, or handoff evidence")
	}
	restarted := NewService(restored)
	restarted.Now = fixedTime
	resolveProjectAuthority(restarted, store.ProjectResolution{ProjectID: "project-1", MainWorktree: true})
	env.SessionRef = "restored-session"
	out = removalDispatch(t, restored, restarted, env, in)
	if out.Outcome != agentcontract.OutcomeOK || !out.Replayed {
		t.Fatalf("restored retained replay: %+v", out.Error)
	}
	snapshot, err := Dispatch(ctx, restored, restarted, InvokeRequest{Tool: "concord_product_view", Operation: "snapshot", Input: json.RawMessage(`{"product_id":"product-1"}`)}, env)
	if err != nil || snapshot.Outcome != agentcontract.OutcomeOK || strings.Contains(string(snapshot.Result), `"work-1"`) {
		t.Fatalf("restored snapshot resurrected execution: %+v %v", snapshot, err)
	}
}
