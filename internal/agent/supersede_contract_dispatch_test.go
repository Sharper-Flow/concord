package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestSupersedeContractDispatchAdvertisesCorrectionAndRefusesStaleVersion(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"before-start", "after-start", "acceptance", "shared-writer", "shared-domain"} {
		t.Run(stage, func(t *testing.T) {
			testSupersedeContractDispatch(t, stage)
		})
	}
}

func testSupersedeContractDispatch(t *testing.T, stage string) {
	t.Helper()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
	// The initial contract is engineering. The successor's serious registry
	// correction is recorded by the authenticated coordinator.
	version := seedAgentWorkflowAt(t, s, grant, currentImplementationFixtureDefinition(t))
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)

	for _, actionID := range []string{"record_proposal", "record_alignment"} {
		invokeWorkflowIssue31Action(t, s, service, env, actionID, version, "supersede-"+actionID)
		version = workOneVersion(t, s)
	}
	binding := workflowArchitectureBindingForSeededRegistry(t, s)
	if stage == "shared-writer" {
		binding["domain_modifies"] = []string{binding["home_domain_id"].(string)}
	}
	contractFields := workflowContractFieldsFixture()
	contractFields["architecture_binding"] = binding
	if len(binding["domain_modifies"].([]string)) != 0 {
		contractFields["law_change_reason"] = "Correct the declared Domain registry."
	}
	contract, _ := approvedWorkflowContract(t, s, service, env, version, contractFields, "supersede-approve-contract")
	if contract.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("approve_contract=%+v", contract.Error)
	}
	version = recordApprovedWorkDesign(t, s, service, env, "work-1", "supersede-design")
	if stage != "before-start" {
		invokeWorkflowIssue31Action(t, s, service, env, "start_execution", version, "supersede-start-execution")
		version = workOneVersion(t, s)
	}
	if stage == "acceptance" {
		if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_instances SET current_step='acceptance' WHERE work_id='work-1'; DELETE FROM write_guard WHERE active=1`); err != nil {
			t.Fatal(err)
		}
	}
	before, err := store.VerifyWorkflowInstanceDefinition(context.Background(), s, store.BuiltinWorkflowRegistry(), "work-1")
	if err != nil {
		t.Fatal(err)
	}
	if stage == "shared-writer" || stage == "shared-domain" {
		seedContractCorrectionPeer(t, s)
	}
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	advertised := false
	for _, intent := range pin.NextValidIntents {
		if intent.ActionID == "supersede_contract" && intent.ExpectedVersion == version {
			advertised = true
		}
	}
	if !advertised {
		t.Errorf("correction advertised=%v at %s: %+v", advertised, stage, pin.NextValidIntents)
	}

	successorBinding := workflowArchitectureBindingForSeededRegistry(t, s)
	successorBinding["domain_modifies"] = []string{successorBinding["home_domain_id"].(string)}
	fields := map[string]any{
		"decision_domains":     []string{"security"},
		"contract_version":     2,
		"premise":              "corrected premise",
		"architecture_binding": successorBinding,
		"law_change_reason":    "Correct the security-relevant Domain registry declaration.",
		"outcome_predicates": []map[string]any{{
			"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check",
			"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:workflow", "expected_result": "pass"},
		}},
		"required_evidence": []string{"verification"},
		"route_conventions": []string{},
		"spec_mandate":      []string{},
		"law_modifies":      []string{},
		"rigor_class":       "prototype_internal",
		"supersede_reason":  "correct the accepted premise",
		"audit_evidence":    []string{"evidence:supersede-dispatch"},
		"design_record": map[string]any{
			"approach":     "corrected design",
			"decisions":    []map[string]any{{"id": "decision:corrected", "question": "Which approach?", "choice": "corrected", "rationale": "Matches the approved objective", "rejected": []string{"original"}}},
			"touched_refs": []string{"src/work.go"},
		},
	}
	input := map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "supersede_contract", "fields": fields, "idempotency_key": "supersede-contract-dispatch"}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	approvedRaw := raw
	var stale map[string]any
	if err := json.Unmarshal(approvedRaw, &stale); err != nil {
		t.Fatal(err)
	}
	stale["expected_version"] = version - 1
	staleRaw, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	refused := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: staleRaw}, env)
	if refused.Outcome != agentcontract.OutcomeError || refused.Error == nil || refused.Error.Kind != "version_conflict" || workOneVersion(t, s) != version {
		t.Fatalf("stale version admitted correction: outcome=%s error=%+v", refused.Outcome, refused.Error)
	}
	response := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedRaw}, env)
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("approved supersede=%+v", response.Error)
	}
	var activeVersion int
	if err := s.DatabaseForTesting().QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id='work-1' AND superseded_by IS NULL`).Scan(&activeVersion); err != nil {
		t.Fatal(err)
	}
	if activeVersion != 2 {
		t.Fatalf("active contract version=%d, want 2", activeVersion)
	}
	continuity, err := store.ReadWorkflowContinuity(context.Background(), s, store.ContinuityRequest{Work: "work-1"})
	if err != nil || continuity.DesignRecord == nil || continuity.DesignRecord.Approach != "corrected design" {
		t.Fatalf("approved correction did not replace the design: %+v err=%v", continuity.DesignRecord, err)
	}
	var actorClass string
	if err := s.DatabaseForTesting().QueryRow(`SELECT a.actor_class FROM workflow_contracts c JOIN workflow_actors a ON a.actor_ref=c.approved_by WHERE c.work_id='work-1' AND c.contract_version=2`).Scan(&actorClass); err != nil {
		t.Fatal(err)
	}
	if actorClass != string(store.ActorAgent) {
		t.Fatalf("successor author actor class=%q, want the invoking coordinator", actorClass)
	}
	after, err := store.VerifyWorkflowInstanceDefinition(context.Background(), s, store.BuiltinWorkflowRegistry(), "work-1")
	if err != nil || after.Digest != before.Digest {
		t.Fatalf("correction changed the pinned definition: before=%s after=%s err=%v", before.Digest, after.Digest, err)
	}
	if _, err := json.Marshal(response); err != nil {
		t.Fatalf("correction result cannot be serialized: %v", err)
	}
	// A peer writing the same Domain is coordination, not a gate: the
	// correction succeeds without resolving anything on the peer's behalf.
	if stage == "shared-writer" || stage == "shared-domain" {
		if resolutions := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions`); resolutions != 0 {
			t.Fatalf("contract correction silently resolved %d overlaps", resolutions)
		}
	}
	committedVersion := workOneVersion(t, s)
	replayed := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedRaw}, env)
	if replayed.Outcome != agentcontract.OutcomeOK || !replayed.Replayed || workOneVersion(t, s) != committedVersion {
		t.Fatalf("correction replay mutated work or failed: outcome=%s replayed=%v error=%+v", replayed.Outcome, replayed.Replayed, replayed.Error)
	}
	if designs := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_design_records WHERE work_id='work-1'`); designs != 2 {
		t.Fatalf("replayed correction changed design history: rows=%d", designs)
	}
}

func workflowDispatchWorktreeFixture(t *testing.T, _ *store.Store, env CallEnvelope) CallEnvelope {
	t.Helper()
	env.Worktree = t.TempDir()
	return env
}

func seedContractCorrectionPeer(t *testing.T, s *store.Store) {
	t.Helper()
	events := []store.Event{
		{EventID: "correction-peer-created", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "correction-peer", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 4, Payload: json.RawMessage(`{"work_kind":"task","title":"Peer work","priority":1}`)},
		{EventID: "correction-peer-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "correction-peer", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
		{EventID: "correction-peer-active", Kind: "work.transitioned", SubjectType: store.SubjectWorkItem, SubjectID: "correction-peer", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"from":"needed","to":"in_progress","reason":"peer execution is active","expected_version":2,"resulting_version":3}`)},
	}
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "correction-peer"): 0}}); err != nil {
		t.Fatal(err)
	}
	_, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		INSERT INTO workflow_instances(work_id,definition_ref,definition_digest,current_step,instance_state,execution_started_at) VALUES('correction-peer','workflow.break_fix',?,'repair','running','2026-08-19T00:00:00Z');
		INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class)
		SELECT 'correction-peer',contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class FROM workflow_contracts WHERE work_id='work-1' AND contract_version=1;
		INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,home_domain_id,projection_hash)
		SELECT 'correction-peer',contract_version,product_id,home_domain_id,projection_hash FROM workflow_architecture_bindings WHERE work_id='work-1' AND contract_version=1;
		INSERT INTO workflow_contract_affected_domains(work_id,contract_version,domain_id)
		SELECT 'correction-peer',contract_version,domain_id FROM workflow_contract_affected_domains WHERE work_id='work-1' AND contract_version=1;
		INSERT INTO workflow_contract_domain_modifications(work_id,contract_version,domain_id)
		SELECT 'correction-peer',contract_version,domain_id FROM workflow_contract_domain_modifications WHERE work_id='work-1' AND contract_version=1;
		DELETE FROM write_guard;`, breakFixFixtureDigest(t))
	if err != nil {
		t.Fatal(err)
	}
}
