package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// This file implements the TS1 operational scenarios bound for the #162
// tranche. AJ8 is the "execute ops" job, and the accepted mutation contract
// (docs/agent-mutation-tool-contract.md section 5) is explicit that native
// execution, rollback, and reclamation are deliberately not claimed as Concord
// mutations: the native authority performs and proves the real operation while
// Concord records intent, authority, and evidence. The reclamation scenario is
// the one AJ8 case whose Concord-visible half is fully built, so it binds here;
// the other three name capabilities that do not exist and are tracked
// separately rather than approximated.

// worktreeReclamationRepo builds a real git repository whose branch is merged
// into the default ref and whose tree is clean — the ground truth the scenario
// declares. ExecGitRunner runs against this repository, so the probes under
// test are real git probes rather than stubs.
func worktreeReclamationRepo(t *testing.T) (string, string) {
	t.Helper()
	repoRoot := t.TempDir()
	gitRun(t, repoRoot, "init", "-b", "main")
	gitRun(t, repoRoot, "config", "user.email", "concord@example.invalid")
	gitRun(t, repoRoot, "config", "user.name", "Concord Reclamation Test")
	if err := os.WriteFile(filepath.Join(repoRoot, "README.md"), []byte("# reclamation fixture\n"), 0o644); err != nil {
		t.Fatalf("write base file: %v", err)
	}
	gitRun(t, repoRoot, "add", "README.md")
	gitRun(t, repoRoot, "commit", "-m", "reclamation base")
	return repoRoot, gitRun(t, repoRoot, "rev-parse", "HEAD")
}

func workItemVersion(t *testing.T, s *store.Store, workID string) int64 {
	t.Helper()
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatalf("read version for %s: %v", workID, err)
	}
	return version
}

func workEventCount(t *testing.T, s *store.Store, workID string) int {
	t.Helper()
	count := 0
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=?`, workID).Scan(&count); err != nil {
		t.Fatalf("count events for %s: %v", workID, err)
	}
	return count
}

// AJ8-ground-truth-reclamation: work-done is complete, its branch is merged and
// its tree is clean, but the Concord projection still records the worktree as
// active. Git is the stronger authority for whether the native resource may go,
// so reclamation proceeds from git facts and the stale projection never blocks
// it. Reclaiming the native resource must not cost the durable history.
func bindAJ8GroundTruthReclamation(t *testing.T, sc jobScenario) jobObservation {
	t.Helper()
	ctx := context.Background()
	s, service, grant, _, _ := agentJobsMutationPM1Fixture(t)

	gitState, _ := sc.InitialState["git"].(map[string]any)
	merged, _ := gitState["merged"].(bool)
	clean, _ := gitState["clean"].(bool)
	if !merged || !clean {
		t.Fatalf("AJ8-ground-truth-reclamation expects merged and clean git state, got %+v", gitState)
	}
	projection, _ := sc.InitialState["projection"].(map[string]any)
	if status, _ := projection["status"].(string); status != "active" {
		t.Fatalf("AJ8-ground-truth-reclamation expects an active projection, got %+v", projection)
	}

	repoRoot, baseSHA := worktreeReclamationRepo(t)
	projectVersion := int64(0)
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM projects WHERE id=?`, "proj-web").Scan(&projectVersion); err != nil {
		t.Fatalf("read proj-web version: %v", err)
	}
	if err := s.AddProjectLocator(ctx, "proj-web", store.ProjectLocator{ID: "reclamation-path", Kind: store.LocatorCanonicalPath, Value: repoRoot}, projectVersion); err != nil {
		t.Fatalf("add canonical path locator: %v", err)
	}

	env := agentJobsMutationEnvelope(t, s, grant, "proj-web", "prod-alpha")
	worktreePath := filepath.Join(t.TempDir(), "work-done-wt")

	// History that must survive the reclamation: PM1 seeds work-done with its
	// full created → in_progress → completed lifecycle.
	historyBefore := workEventCount(t, s, "work-done")
	if historyBefore == 0 {
		t.Fatal("PM1 seeded no history for work-done")
	}

	claimInput, _ := json.Marshal(map[string]any{
		"work_id": "work-done", "project_id": "proj-web",
		"branch": "work/reclaim-done", "base_sha": baseSHA, "path": worktreePath,
		"expected_version": workItemVersion(t, s, "work-done"), "idempotency_key": "aj8-reclaim-claim",
	})
	claim := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "worktree_claim", Input: claimInput}, env)
	if claim.Outcome != OutcomeOK {
		t.Fatalf("worktree claim failed outcome=%s err=%+v", claim.Outcome, claim.Error)
	}

	// Make the branch genuinely merged: commit inside the linked worktree, then
	// merge it into the default ref. The tree is left clean.
	if err := os.WriteFile(filepath.Join(worktreePath, "done.md"), []byte("# completed\n"), 0o644); err != nil {
		t.Fatalf("write worktree file: %v", err)
	}
	gitRun(t, worktreePath, "add", "done.md")
	gitRun(t, worktreePath, "commit", "-m", "work-done change")
	gitRun(t, repoRoot, "merge", "--ff-only", "work/reclaim-done")
	if dirty := gitRun(t, worktreePath, "status", "--porcelain"); dirty != "" {
		t.Fatalf("worktree is not clean before reclamation: %q", dirty)
	}

	// The projection is stale by construction: git has merged the branch while
	// Concord still records the worktree as active.
	before, err := s.WorktreeEntries(ctx, "work-done")
	if err != nil || len(before) != 1 || before[0].State != "active" {
		t.Fatalf("expected one active worktree entry before reclamation, got %+v err=%v", before, err)
	}

	reclaimInput, _ := json.Marshal(map[string]any{
		"work_id": "work-done", "project_id": "proj-web", "default_ref": "main",
		"expected_version": workItemVersion(t, s, "work-done"), "idempotency_key": "aj8-reclaim-1",
	})
	resp := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "worktree_reclaim", Input: reclaimInput}, env)
	if resp.Outcome != OutcomeOK {
		t.Fatalf("reclamation refused outcome=%s err=%+v", resp.Outcome, resp.Error)
	}

	// Ground truth: the native worktree is gone and the projection agrees.
	after, err := s.WorktreeEntries(ctx, "work-done")
	if err != nil || len(after) != 1 || after[0].State != "reclaimed" {
		t.Fatalf("worktree entry after reclamation=%+v err=%v", after, err)
	}
	if strings.Contains(gitRun(t, repoRoot, "worktree", "list"), "work-done-wt") {
		t.Fatal("native worktree still present after reclamation")
	}
	if _, statErr := os.Stat(worktreePath); !os.IsNotExist(statErr) {
		t.Fatalf("worktree path still exists after reclamation: %v", statErr)
	}
	if len(after[0].GitFacts) == 0 || string(after[0].GitFacts) == "null" {
		t.Fatalf("reclamation recorded no git facts: %q", string(after[0].GitFacts))
	}

	// Reclaiming a native resource must never cost durable history.
	historyAfter := workEventCount(t, s, "work-done")
	if historyAfter < historyBefore {
		t.Fatalf("reclamation dropped history: %d events before, %d after", historyBefore, historyAfter)
	}
	lifecycle, _ := readWorkFromStore(t, s, "work-done")
	if lifecycle != "completed" {
		t.Fatalf("work-done lifecycle after reclamation=%q, want completed", lifecycle)
	}

	obs := envelopeToObservation(resp)
	obs.State = map[string]any{
		"worktree": map[string]any{"exists": false},
		"work": map[string]any{
			"work-done": map[string]any{"history_retained": true},
		},
	}
	obs.Communication["ground_truth_evidence"] = string(after[0].GitFacts)
	// The prohibited effect is the reclamation being blocked by the stale
	// projection. Probing it means proving the projection really was stale at
	// the moment of the call and that the call still succeeded.
	obs.Effects["blocked_by_stale_projection"] = probedAbsent{
		Evidence: "projection read active immediately before the call while git had merged the branch; the reclamation returned ok and the entry moved active -> reclaimed",
	}
	return obs
}

// approvedOpsAction dispatches one ops workflow action, cycling the core
// approval challenge through a signed host approval when the action requires
// operator authority. It returns the final envelope and the work version the
// action produced.
func approvedOpsAction(t *testing.T, s *store.Store, service *Service, grant Grant, privateKey ed25519.PrivateKey, env CallEnvelope, version int64, action string, fields map[string]any, key string) (Envelope, int64) {
	t.Helper()
	input := map[string]any{"work_id": "work-1", "expected_version": version, "action_id": action, "idempotency_key": key}
	if fields != nil {
		input["fields"] = fields
	} else {
		input["fields"] = []any{}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	resp := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if resp.Error != nil && resp.Error.Kind == "approval_required" {
		challengeRef, _ := resp.Error.Details["approval_ref"].(string)
		if challengeRef == "" {
			t.Fatalf("%s minted no challenge: %+v", action, resp.Error.Details)
		}
		withApproval := input
		withApproval["approval"] = map[string]any{"approval_ref": challengeRef}
		approvedRaw, _ := json.Marshal(withApproval)
		scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": env.ScopeVersion}
		versions := map[string]any{"work": version}
		approvalEnv := env
		approvalEnv.HostApproval = signedHostApproval(privateKey, challengeRef, mutationDigest("concord_work_transition", "workflow_action", env, approvedRaw), scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), nonceForChallenge(challengeRef))
		resp = dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedRaw}, approvalEnv)
	}
	var newVersion int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-1'`).Scan(&newVersion); err != nil {
		t.Fatal(err)
	}
	return resp, newVersion
}

// AJ8-health-failure-rollback: an approved production routing change is
// applied, its health check fails, and the declared rollback runs. Concord
// never calls the provider (CD-0039 D9): the native authority reports each
// phase through typed workflow-action fields, Concord folds one attributed
// native-run event per phase, and the logical operation completes partial —
// the native steps succeeded, the approved change did not.
func bindAJ8HealthFailureRollback(t *testing.T, sc jobScenario) jobObservation {
	t.Helper()
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition"})
	seedCurrentWorkflowDomainFixture(t, s)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)

	registered := mustOpsRunbookDefinition(t)
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(context.Background(), tx, store.WorkflowInitializationRequest{WorkID: "work-1", Definition: registered, Actor: store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}, Now: fixedTime()})
	}); err != nil {
		t.Fatalf("initialize ops runbook: %v", err)
	}
	version := int64(0)
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-1'`).Scan(&version); err != nil {
		t.Fatal(err)
	}

	const runID = "run-routing-1"
	const subject = "routing-provider:prod-edge"
	evidenceFor := func(phase string) (string, string) {
		digit := map[string]string{"start": "1", "health": "2", "rollback": "3"}[phase]
		return "https://evidence.invalid/runs/" + runID + "/" + phase, "sha256:" + strings.Repeat(digit, 64)
	}
	nativeFields := func(phase, status string) map[string]any {
		evidenceRef, evidenceDigest := evidenceFor(phase)
		return map[string]any{"run_id": runID, "native_subject_ref": subject, "status": status, "evidence_ref": evidenceRef, "evidence_digest": evidenceDigest, "asserted_at": fixedTime().Format("2006-01-02T15:04:05Z")}
	}

	// plan → approval → execute: the operator approves the contract and the
	// operation through real challenge/approval cycles.
	contractResp, version := approvedOpsAction(t, s, service, grant, privateKey, env, version, "approve_contract", workflowContractFieldsFixture(), "aj8-rollback-contract")
	if contractResp.Outcome != OutcomeOK {
		t.Fatalf("approve_contract=%+v", contractResp.Error)
	}
	operationResp, version := approvedOpsAction(t, s, service, grant, privateKey, env, version, "approve_operation", nil, "aj8-rollback-operation")
	if operationResp.Outcome != OutcomeOK {
		t.Fatalf("approve_operation=%+v", operationResp.Error)
	}
	startResp, version := approvedOpsAction(t, s, service, grant, privateKey, env, version, "start_run", nativeFields("start", "started"), "aj8-rollback-start")
	if startResp.Outcome != OutcomeOK {
		t.Fatalf("start_run=%+v", startResp.Error)
	}
	// Fixture placement to the health step: the execute step's only
	// advance-mode actions are condition actions, and an open condition
	// blocks the cross-authority health report by design. The scenario's
	// initial_state already declares the approval valid and the change
	// applied, so the runbook position at the health check is fixture
	// context (mirroring seedOverlapProjection's fold-guarded seeding), not
	// behavior under test. Every reported action below is a real dispatch.
	placeOpsRunbookAtStep(t, s, "health")
	healthResp, version := approvedOpsAction(t, s, service, grant, privateKey, env, version, "record_health", nativeFields("health", "failed"), "aj8-rollback-health")
	if healthResp.Outcome != OutcomePartial {
		t.Fatalf("failed health must classify partial, got %s: %+v", healthResp.Outcome, healthResp.Error)
	}
	rollbackResp, _ := approvedOpsAction(t, s, service, grant, privateKey, env, version, "rollback_run", nativeFields("rollback", "rolled_back"), "aj8-rollback-run")
	if rollbackResp.Outcome != OutcomePartial {
		t.Fatalf("successful rollback after failed health must classify partial (logical operation unsuccessful), got %s: %+v", rollbackResp.Outcome, rollbackResp.Error)
	}
	if rollbackResp.Error == nil || rollbackResp.Error.Kind != "operation_conflict" || rollbackResp.Error.RecoveryAction.Kind != "reconcile_operation" || rollbackResp.Error.EffectState != EffectPartial {
		t.Fatalf("rollback partial shape wrong: %+v", rollbackResp.Error)
	}
	healthFailure, _ := rollbackResp.Error.Details["health_failure"].(string)
	rollbackResult, _ := rollbackResp.Error.Details["rollback_result"].(string)
	if healthFailure == "" || rollbackResult == "" {
		t.Fatalf("partial outcome missing native failure/rollback results: %+v", rollbackResp.Error.Details)
	}

	// The durable projection: every phase is an attributed report with the
	// reporter, subject, and evidence riding alongside the status (CD-0039 D4).
	snapshot, err := store.ReadWorkflowContinuity(context.Background(), s, store.ContinuityRequest{Work: "work-1", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	reports := snapshot.NativeRuns
	phases := map[string]store.NativeRunReport{}
	for _, report := range reports {
		if report.RunID == runID {
			phases[report.Phase] = report
		}
	}
	if len(phases) != 3 || phases["start"].Status != "started" || phases["health"].Status != "failed" || phases["rollback"].Status != "rolled_back" {
		t.Fatalf("native run phases=%+v", phases)
	}
	for phase, report := range phases {
		if report.ReportingAuthorityRef != grant.ClientRef || report.NativeSubjectRef != subject || report.EvidenceRef == "" || report.EvidenceDigest == "" || report.AssertedAt == "" {
			t.Fatalf("%s report lost attribution: %+v", phase, report)
		}
	}

	obs := envelopeToObservation(rollbackResp)
	obs.State = map[string]any{"native_change": map[string]any{"status": phases["rollback"].Status}}
	obs.Communication["health_failure"] = healthFailure
	obs.Communication["rollback_result"] = rollbackResult
	obs.Effects["evidence_authority_supplied"] = true
	// The prohibited effect is adapter domain logic: the adapter inferring a
	// provider outcome or synthesizing partial from prose. Probing it means
	// proving the typed core path owns the classification: the partial status
	// equals the folded attributed report, and the inputs carried only the
	// closed typed fields — no summary, no authority strings, no prose.
	obs.Effects["adapter_domain_logic"] = probedAbsent{
		Evidence: "outcome partial is derived core-side from the folded workflow.native_run_recorded event and matches the durable projection (rolled_back by " + phases["rollback"].ReportingAuthorityRef + "); the adapter input surface carried only the closed typed fields with no caller authority or prose summary",
	}
	return obs
}

// mustOpsRunbookDefinition returns the registered ops-runbook definition the
// instance pins.
func mustOpsRunbookDefinition(t *testing.T) store.RegisteredDefinition {
	t.Helper()
	registered, err := store.BuiltinWorkflowDefinitionForRef("workflow.ops_runbook")
	if err != nil {
		t.Fatal(err)
	}
	return registered
}

// placeOpsRunbookAtStep fixture-places the runbook instance at one step via
// the fold guard, the same seeding seam seedOverlapProjection uses.
func placeOpsRunbookAtStep(t *testing.T, s *store.Store, stepID string) {
	t.Helper()
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(context.Background(), `UPDATE workflow_instances SET current_step=? WHERE work_id='work-1'`, stepID); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(context.Background(), `DELETE FROM fold_guard`); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
