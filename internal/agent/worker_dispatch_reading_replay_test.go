package agent

// The authenticated boundary replays an exact workflow_action request from
// its durable idempotency record before any scope, budget, or dispatch
// preparation runs. A successful dispatch over declared required readings
// therefore replays without resolving the declared sources again: removing
// the repository the readings pin cannot change the replayed result, add
// effects, or run Git a second time.

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

func TestDispatchRequiredReadingReplayAfterSourceRemoval(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition", "worker_dispatch"})

	// The Product's knowledge home is one real Git repository with a
	// committed manifest and a committed law record; rebuilding the index
	// projects the exact canonical locator the reading pins. The same
	// repository holds the declared repository file, so both reading arms
	// pin this one required source repository.
	home, err := pm1fixture.SeedCommittedProductDomain(ctx, s, "product-1", "project-1", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var repoLocator int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM project_locators WHERE project_id='project-1' AND kind='canonical_path'`).Scan(&repoLocator); err != nil {
		t.Fatal(err)
	}
	if repoLocator == 0 {
		var projectVersion int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM projects WHERE id='project-1'`).Scan(&projectVersion); err != nil {
			t.Fatal(err)
		}
		if err := s.AddProjectLocator(ctx, "project-1", store.ProjectLocator{ID: "replay-reading-repo", Kind: store.LocatorCanonicalPath, Value: home.RepoPath}, projectVersion); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	var lawPath, lawCommit, lawHash string
	if err := s.DatabaseForTesting().QueryRow(`SELECT note_path, commit_oid, content_hash FROM archived_work WHERE home_project_id='project-1' AND home_locator_id=? AND id='CD-0001'`, home.HomeLocatorID).Scan(&lawPath, &lawCommit, &lawHash); err != nil {
		t.Fatalf("the rebuilt index holds no canonical locator for the committed law record: %v", err)
	}
	var domainID string
	if err := s.DatabaseForTesting().QueryRow(`SELECT domain_id FROM domains WHERE product_id='product-1' AND status='current' ORDER BY domain_id LIMIT 1`).Scan(&domainID); err != nil {
		t.Fatalf("the rebuilt index holds no current Domain: %v", err)
	}

	// The live implementation head declares record_work_context and gates
	// its dispatch on a ready worker job; the fixture parks the work at the
	// execution step with an approved contract and the claimed worktree the
	// dispatch boundary requires.
	definition, err := store.LiveWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	grantActor := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-1", Definition: definition, Actor: grantActor, Now: fixedTime()})
	}); err != nil {
		t.Fatal(err)
	}
	claimDir := t.TempDir()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at) VALUES(?1,'project-1','claim:replay-journey','replay-journey',?2,?3,'repo:replay-journey','active',?4);
		DELETE FROM write_guard`,
		store.WorktreeSetID("work-1"), strings.Repeat("a", 40), claimDir, fixedTime().Format("2006-01-02T15:04:05.999999999Z07:00")); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
	grant.Worktree = claimDir
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	invoke := func(input map[string]any) Envelope {
		t.Helper()
		response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(input)}, env)
		if err != nil {
			t.Fatalf("dispatch %s: %v", input["action_id"], err)
		}
		return response
	}
	version := func() int64 { return workVersion(t, s, "work-1") }

	advanceCapturedWorkToContract(t, s, service, env, "work-1", "replay-journey")
	contractFields := workflowContractFieldsFixture()
	contractFields["premise"] = "the replay journey delivers its checked change"
	contractFields["outcome_predicates"] = []map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:replay-journey", "immutable_subject_ref": "commit:replay-journey", "expected_result": "pass"}}}
	contractFields["architecture_binding"] = workflowArchitectureBindingForSeededRegistry(t, s)
	if approved := invoke(map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "approve_contract", "fields": contractFields, "idempotency_key": "replay-journey-contract"}); approved.Outcome != OutcomeOK {
		t.Fatalf("approve_contract: %+v", approved.Error)
	}

	recordApprovedWorkDesign(t, s, service, env, "work-1", "replay-journey-design")
	if start := invoke(map[string]any{
		"work_id": "work-1", "expected_version": version(), "action_id": "start_execution",
		"fields": map[string]any{}, "idempotency_key": "replay-journey-start",
	}); start.Outcome != OutcomeOK {
		t.Fatalf("start_execution: %+v", start.Error)
	}
	recordRecoveryWorkerJob(t, s, service, env, "job:replay-journey")
	oAgentReview(t, oAgentFixture(t, s, service, env, "work-1"), "continue", false)
	if declared := invoke(map[string]any{
		"work_id": "work-1", "expected_version": version(), "action_id": "record_work_context",
		"fields": map[string]any{
			"required_reading": []map[string]any{
				{"domain_id": domainID, "product_wide_rationale": "the replay journey reads its product law", "reason": "the replay journey pins its repository source", "source": map[string]any{"kind": "repository_file", "project_id": "project-1", "path": lawPath, "commit_oid": lawCommit}},
				{"domain_id": domainID, "product_wide_rationale": "the replay journey reads its product law", "reason": "the replay journey pins its law", "source": map[string]any{"kind": "knowledge", "source_id": "project-1", "law_id": "CD-0001", "content_hash": lawHash}},
			},
			"finding_refs":     []string{},
			"context_findings": []map[string]any{},
		},
		"idempotency_key": "replay-journey-declare",
	}); declared.Outcome != OutcomeOK {
		t.Fatalf("record_work_context: %+v", declared.Error)
	}
	attemptID := "attempt:work-1:replay-journey"
	input := retryJSON(map[string]any{
		"work_id": "work-1", "expected_version": version(), "action_id": "dispatch_worker",
		"fields": map[string]any{
			"attempt_id": attemptID,
			"worker_packet": bindPacketToRecordedState(t, s, func() map[string]any {
				lane := recoveryProducerLane(t, "implement")
				return map[string]any{
					"schema_version": store.WorkerPacketSchemaVersion, "attempt_id": attemptID, "lane_id": lane.ID, "lane_digest": lane.Digest,
					"work_id": "work-1", "step_id": "execution",
					"inputs": map[string]any{"task": "deliver the replay journey change", "constraints": []string{"preserve the approved contract"}},
				}
			}()),
		},
		"idempotency_key": "replay-journey-dispatch",
	})
	env.RequestID = "request:replay-journey-dispatch"
	first := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: input}, env)
	if first.Outcome != OutcomeOK {
		t.Fatalf("dispatch over the declared readings refused: %+v", first.Error)
	}

	var eventsBefore, dispatchesBefore int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_authorizations WHERE work_id='work-1'`).Scan(&dispatchesBefore); err != nil {
		t.Fatal(err)
	}

	// Remove the required source repository. A replay that re-prepared or
	// re-resolved the declared readings would refuse here.
	if err := os.RemoveAll(home.RepoPath); err != nil {
		t.Fatal(err)
	}

	replay := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: input}, env)
	if replay.Outcome != OutcomeOK || !replay.Replayed {
		t.Fatalf("replay after the source removal = %+v replayed=%t, want the recorded result without a new preparation", replay.Error, replay.Replayed)
	}
	if string(replay.Result) != string(first.Result) {
		t.Fatalf("replay result %s differs from the original %s", replay.Result, first.Result)
	}
	var eventsAfter, dispatchesAfter int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&eventsAfter); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_authorizations WHERE work_id='work-1'`).Scan(&dispatchesAfter); err != nil {
		t.Fatal(err)
	}
	if eventsAfter != eventsBefore || dispatchesAfter != dispatchesBefore {
		t.Fatalf("replay added effects: events %d -> %d, dispatch completions %d -> %d", eventsBefore, eventsAfter, dispatchesBefore, dispatchesAfter)
	}
}
