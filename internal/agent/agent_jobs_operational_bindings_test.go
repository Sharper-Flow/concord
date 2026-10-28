package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// This file implements the TS1 operational scenarios bound for the #162
// tranche. AJ8 is the "execute ops" job, and the accepted mutation contract
// (.concord/docs/agent-mutation-tool-contract.md section 5) is explicit that native
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
	seedClaimOrigin(t, repoRoot)
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
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_type='work_item' AND subject_id=?`, workID).Scan(&count); err != nil {
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

	env := agentJobsMutationEnvelope(t, s, grant, "proj-web")
	worktreePath := filepath.Join(filepath.Dir(s.Path()), "worktrees", "proj-web", "work-done")

	// The completed lifecycle and its original receipts must survive reclamation.
	historyBefore := workEventCount(t, s, "work-done")
	retainedBefore := agentRowsSnapshot(t, s, `SELECT * FROM effect_receipts WHERE subject_id='work-done'`)
	frontierBefore := countRows(t, s.DatabaseForTesting(), `SELECT last FROM store_sequences WHERE name='effect'`)
	transitionBefore := agentRowsSnapshot(t, s, `SELECT lifecycle,transition_from,transition_reason,transition_actor,transition_evidence_refs,transition_seq,transition_version FROM work_items WHERE id='work-done'`)
	if historyBefore == 0 {
		t.Fatal("PM1 seeded no history for work-done")
	}

	claimInput, _ := json.Marshal(map[string]any{"host_pid": os.Getpid(),
		"work_id": "work-done", "project_id": "proj-web",
		"base_sha":         baseSHA,
		"expected_version": workItemVersion(t, s, "work-done"), "idempotency_key": "aj8-reclaim-claim",
	})
	claim := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "worktree_claim", Input: claimInput}, env)
	if claim.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("worktree claim failed outcome=%s err=%+v", claim.Outcome, claim.Error)
	}

	// Make the branch genuinely merged: commit inside the linked worktree, then
	// merge it into the default ref. The tree is left clean.
	if err := os.WriteFile(filepath.Join(worktreePath, "done.md"), []byte("# completed\n"), 0o644); err != nil {
		t.Fatalf("write worktree file: %v", err)
	}
	gitRun(t, worktreePath, "add", "done.md")
	gitRun(t, worktreePath, "commit", "-m", "work-done change")
	gitRun(t, repoRoot, "merge", "--ff-only", "work/work-done")
	gitRun(t, repoRoot, "update-ref", "refs/remotes/origin/main", "HEAD")
	if dirty := gitRun(t, worktreePath, "status", "--porcelain"); dirty != "" {
		t.Fatalf("worktree is not clean before reclamation: %q", dirty)
	}

	// The projection is stale by construction: git has merged the branch while
	// Concord still records the worktree as active.
	before, err := s.WorktreeEntries(ctx, "work-done")
	if err != nil || len(before) != 1 || before[0].State != "active" {
		t.Fatalf("expected one active worktree entry before reclamation, got %+v err=%v", before, err)
	}

	// The claiming session vacates before the reclaim. The vacate records
	// the relocation request and leaves the occupancy row standing; the
	// adapter-only vacate-landing verb records the verified landing at the
	// registered main checkout and releases the row in one transaction, so
	// the removal gate no longer sees a live occupant. Occupancy is a
	// Concord projection, and the gate refuses while a recorded occupant
	// remains; ground-truth reclamation does not authorize stranding a
	// session.
	vacateEnv := env
	vacateEnv.Worktree = worktreePath
	vacate := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "session_vacate", Input: json.RawMessage(`{"idempotency_key":"aj8-vacate-1"}`)}, vacateEnv)
	if vacate.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("session vacate failed outcome=%s err=%+v", vacate.Outcome, vacate.Error)
	}
	if _, err := s.RecordSessionVacateLanding(ctx, store.SessionVacateLandingRequest{
		WorkID: "work-done", SessionRef: grant.SessionRef, LandedDirectory: repoRoot, HostPID: os.Getpid(),
	}); err != nil {
		t.Fatalf("vacate landing failed: %v", err)
	}

	reclaimInput, _ := json.Marshal(map[string]any{
		"work_id": "work-done", "project_id": "proj-web", "default_ref": "main",
		"expected_version": workItemVersion(t, s, "work-done"), "idempotency_key": "aj8-reclaim-1",
	})
	resp := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "worktree_reclaim", Input: reclaimInput}, env)
	if resp.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("reclamation refused outcome=%s err=%+v", resp.Outcome, resp.Error)
	}

	// Ground truth: the native worktree is gone and the projection agrees.
	after, err := s.WorktreeEntries(ctx, "work-done")
	if err != nil || len(after) != 1 || after[0].State != "reclaimed" {
		t.Fatalf("worktree entry after reclamation=%+v err=%v", after, err)
	}
	if strings.Contains(gitRun(t, repoRoot, "worktree", "list"), "work-done") {
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
	if got := agentRowsSnapshot(t, s, `SELECT * FROM effect_receipts WHERE subject_id='work-done' AND effect_seq<=?`, frontierBefore); got != retainedBefore {
		t.Fatal("reclamation changed original receipt identities or digests")
	}
	if got := agentRowsSnapshot(t, s, `SELECT lifecycle,transition_from,transition_reason,transition_actor,transition_evidence_refs,transition_seq,transition_version FROM work_items WHERE id='work-done'`); got != transitionBefore {
		t.Fatal("reclamation changed the completed lifecycle evidence")
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

// approvedWorkflowContract approves a contract through the signed host
// challenge and returns its envelope and resulting work version.
func approvedWorkflowContract(t *testing.T, s *store.Store, service *Service, grant Authority, privateKey ed25519.PrivateKey, env CallEnvelope, version int64, fields map[string]any, key string) (agentcontract.Envelope, int64) {
	t.Helper()
	return approvedWorkflowActionWithVersions(t, s, service, grant, privateKey, env, version, "approve_contract", fields, key, map[string]any{"work": version})
}

func approvedWorkflowActionWithVersions(t *testing.T, s *store.Store, service *Service, grant Authority, privateKey ed25519.PrivateKey, env CallEnvelope, version int64, action string, fields map[string]any, key string, versions map[string]any) (agentcontract.Envelope, int64) {
	t.Helper()
	input := map[string]any{"work_id": "work-1", "expected_version": version, "action_id": action, "idempotency_key": key}
	if fields != nil {
		input["fields"] = fields
	} else {
		input["fields"] = map[string]any{}
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
// bindAJ8HealthFailureRollback carries the AJ8 corpus row's retired
// ops-runbook authoring into its CD-0218 disposition: the native
// start/health/rollback choreography left the live catalog with its family,
// so the corpus witness becomes the archived-family refusal — the nonterminal
// retired instance a serving release leaves behind refuses every public
// workflow action with zero effects, and the retained native-run reader keeps
// the recorded-history shape the frozen journals cover.
func bindAJ8HealthFailureRollback(t *testing.T, sc jobScenario) jobObservation {
	t.Helper()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
	seedCurrentWorkflowDomainFixture(t, s)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)

	// No archived family registers a definition (CD-0242 D14), so the
	// nonterminal archived instance is the recorded projection a pre-cutover
	// store carries: a current instance whose pin names the archived family.
	registered, err := store.BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	ctx := agentWorkflowSourceProof(t, context.Background(), s, "work-1")
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-1", Definition: registered, Actor: store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}, Now: fixedTime()})
	}); err != nil {
		t.Fatalf("initialize the instance: %v", err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		UPDATE workflow_instances SET definition_ref='workflow.ops_runbook' WHERE work_id='work-1';
		DELETE FROM write_guard`); err != nil {
		t.Fatalf("pin the archived family: %v", err)
	}
	version := int64(0)
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-1'`).Scan(&version); err != nil {
		t.Fatal(err)
	}

	const runID = "run-routing-1"
	state := func() (int64, int64) {
		t.Helper()
		var events, contracts int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&events); err != nil {
			t.Fatal(err)
		}
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id='work-1'`).Scan(&contracts); err != nil {
			t.Fatal(err)
		}
		return events, contracts
	}
	nativeFields := func(phase, status string) map[string]any {
		digit := map[string]string{"start": "1", "health": "2", "rollback": "3"}[phase]
		return map[string]any{"run_id": runID, "native_subject_ref": "routing-provider:prod-edge", "status": status, "evidence_ref": "https://evidence.invalid/runs/" + runID + "/" + phase, "evidence_digest": "sha256:" + strings.Repeat(digit, 64), "asserted_at": fixedTime().Format("2006-01-02T15:04:05Z")}
	}

	// Every public action on the nonterminal archived instance refuses with
	// the archived-family answer and commits nothing — the approval-gated
	// contract first, then the retired native actions by id.
	baselineEvents, baselineContracts := state()
	baselineEffects := agentCurrentEffects(t, s)
	refuse := func(actionID string, fields map[string]any, key string) {
		t.Helper()
		input := map[string]any{"work_id": "work-1", "expected_version": version, "action_id": actionID, "idempotency_key": key}
		if fields != nil {
			input["fields"] = fields
		}
		challenge := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(input)}, env)
		if challenge.Outcome != agentcontract.OutcomeError || challenge.Error == nil {
			t.Fatalf("%s on the archived instance = %+v, want a refusal", actionID, challenge.Error)
		}
		// A live action id reaches the archived-family gate and names it; a
		// retired id refuses at the closed union before any authority read.
		if actionID == "approve_contract" && !strings.Contains(challenge.Error.Message, "workflow.ops_runbook is archived") {
			t.Fatalf("approve_contract on the archived instance = %+v, want the archived-family refusal", challenge.Error)
		}
		events, contracts := state()
		if events != baselineEvents || contracts != baselineContracts {
			t.Fatalf("%s refusal wrote state: events=%d/%d contracts=%d/%d", actionID, baselineEvents, events, baselineContracts, contracts)
		}
		if agentCurrentEffects(t, s) != baselineEffects {
			t.Fatalf("%s refusal changed current rows or receipts", actionID)
		}
	}
	refuse("approve_contract", workflowContractFieldsFixture(), "aj8-rollback-contract")
	refuse("approve_operation", nil, "aj8-rollback-operation")
	refuse("start_run", nativeFields("start", "started"), "aj8-rollback-start")
	refuse("record_health", nativeFields("health", "failed"), "aj8-rollback-health")
	refuse("rollback_run", nativeFields("rollback", "rolled_back"), "aj8-rollback-run")

	// No replayed journal reader survives (CD-0242 D2), and no archived
	// definition is registered (D14): the workflow read names the archived
	// family and its migration route instead of an unexplained absence (D13).
	_, err = store.ReadWorkflowContinuity(context.Background(), s, store.ContinuityRequest{Work: "work-1", Limit: 20})
	var failure *store.Failure
	if !errors.As(err, &failure) || failure.Kind != store.KindInvariantViolation || !strings.Contains(failure.Detail, "workflow.ops_runbook is archived") {
		t.Fatalf("continuity read on the archived instance = %v, want the archived-family answer", err)
	}

	obs := envelopeToObservation(agentcontract.Envelope{Outcome: agentcontract.OutcomeOK})
	obs.State = map[string]any{"archived_family": "workflow.ops_runbook", "refused_actions": "approve_contract,approve_operation,start_run,record_health,rollback_run", "zero_effects": true}
	obs.Effects["adapter_domain_logic"] = probedAbsent{
		Evidence: "the retired authoring refuses before any write; no adapter inference path exists to probe because no native action is authorable",
	}
	return obs
}
