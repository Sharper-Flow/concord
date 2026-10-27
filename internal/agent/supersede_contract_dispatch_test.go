package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestSupersedeContractDispatchChallengesThenBindsApprovalOperator(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"before-start", "after-start", "acceptance", "overlap", "pending-dispatch", "pending-dispatch-overlap", "shared-domain", "self-repair"} {
		t.Run(stage, func(t *testing.T) {
			testSupersedeContractDispatch(t, stage)
		})
	}
}

func testSupersedeContractDispatch(t *testing.T, stage string) {
	t.Helper()
	s, service, grant, privateKey := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition", "worker_dispatch"})
	// The initial contract is engineering. The successor's serious registry
	// correction exercises exact-text consent under consequence_v1.
	version := seedAgentWorkflowAt(t, s, grant, legacyApprovalPolicyDefinitionForRef(t))
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)

	for _, actionID := range []string{"record_proposal", "record_alignment"} {
		invokeWorkflowIssue31Action(t, s, service, env, actionID, version, "supersede-"+actionID)
		version = workOneVersion(t, s)
	}
	binding := workflowArchitectureBindingFixture()
	if stage == "overlap" || stage == "pending-dispatch-overlap" || stage == "self-repair" {
		binding["domain_modifies"] = []string{"root"}
	}
	contractFields := workflowContractFieldsFixture()
	contractFields["architecture_binding"] = binding
	if len(binding["domain_modifies"].([]string)) != 0 {
		contractFields["law_change_reason"] = "Correct the declared Domain registry."
		contractFields["law_change_removes_user_visible_behavior"] = false
		contractFields["law_change_touches_security_or_data_safety"] = false
	}
	contract, _ := approvedWorkflowContract(t, s, service, grant, privateKey, env, version, contractFields, "supersede-approve-contract")
	if contract.Outcome != OutcomeOK {
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
	if stage == "overlap" || stage == "shared-domain" || stage == "self-repair" {
		seedContractCorrectionPeer(t, s)
	}
	if stage == "self-repair" {
		if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE domain_registries SET product_key='concord' WHERE product_id='product-1'; DELETE FROM write_guard`); err != nil {
			t.Fatal(err)
		}
	}
	if strings.HasPrefix(stage, "pending-dispatch") {
		env = workflowDispatchWorktreeFixture(t, s, env)
		var lane store.LaneDefinition
		for _, candidate := range store.BuiltinLaneDefinitions() {
			if candidate.ID == "verify" {
				lane = candidate
			}
		}
		// The job-capable pin (workflow.implementation v24+) refuses the
		// dispatch without a ready worker-job revision. Record one under
		// the v1 parent contract before authorizing the dispatch window
		// (CD-0205). The verify lane is a verification capability class
		// and the recorded checks cannot be empty.
		recordReadyRetryJobWithChecks(t, s, service, env, "job:supersede-pending", 1, []string{"go test ./internal/store/"})
		version = workOneVersion(t, s)
		payload, err := json.Marshal(map[string]any{
			"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker", "idempotency_key": "pending-dispatch",
			"fields": map[string]any{"attempt_id": "attempt:pending", "worker_packet": bindPacketToRecordedState(t, s, map[string]any{
				"schema_version": store.WorkerPacketSchemaVersion, "attempt_id": "attempt:pending", "lane_id": lane.ID, "lane_digest": lane.Digest,
				"work_id": "work-1", "step_id": "execution", "inputs": map[string]any{"task": "verify the synthetic change"},
			})},
		})
		if err != nil {
			t.Fatal(err)
		}
		dispatched := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: payload}, env)
		if dispatched.Outcome != OutcomeOK {
			t.Fatalf("authorize dispatch window: %+v", dispatched.Error)
		}
		version = workOneVersion(t, s)
	}
	if stage == "pending-dispatch-overlap" {
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
	// The advertisement is the shared admission's answer, not a per-site
	// predicate. The stale-law and overlap recovery is fold-admitted at every
	// running step, so the pin advertises it beside an authorized dispatch
	// window too; the plain window case stays closed because the fold's own
	// correction gate refuses supersede_contract there (CD-0133 D1).
	wantCorrection := stage != "pending-dispatch"
	if advertised != wantCorrection {
		t.Errorf("correction advertised=%v at %s: %+v", advertised, stage, pin.NextValidIntents)
	}

	successorBinding := workflowArchitectureBindingFixture()
	successorBinding["domain_modifies"] = []string{"root"}
	fields := map[string]any{
		"decision_domains":     []string{},
		"contract_version":     2,
		"premise":              "corrected premise",
		"architecture_binding": successorBinding,
		"law_change_reason":    "Correct the security-relevant Domain registry declaration.",
		"law_change_removes_user_visible_behavior":   false,
		"law_change_touches_security_or_data_safety": true,
		"law_change_registry_content_hash":           successorBinding["domain_registry_content_hash"],
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
	if stage == "self-repair" {
		fields["self_repair"] = map[string]any{"refusal_kind": "domain_overlap", "blocked_operation": "workflow_action.dispatch_worker", "evidence_refs": []string{"obs:0000000000000001"}}
	}
	input := map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "supersede_contract", "fields": fields, "idempotency_key": "supersede-contract-dispatch"}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	challenge := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if stage == "pending-dispatch" {
		if challenge.Outcome != OutcomeError || challenge.Error == nil || challenge.Error.Kind == "approval_required" {
			t.Fatalf("pending dispatch admitted correction: %+v", challenge.Error)
		}
		if workOneVersion(t, s) != version {
			t.Fatal("refused correction changed the work version")
		}
		return
	}
	if challenge.Outcome != OutcomeError || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("challenge outcome=%s error=%+v", challenge.Outcome, challenge.Error)
	}
	challengeRef, ok := challenge.Error.Details["approval_ref"].(string)
	if !ok || len(challengeRef) != 64 {
		t.Fatalf("approval challenge ref=%v", challenge.Error.Details["approval_ref"])
	}

	approved := input
	approved["approval"] = map[string]any{"approval_ref": challengeRef}
	approvedRaw, err := json.Marshal(approved)
	if err != nil {
		t.Fatal(err)
	}
	// The assertion binds the invoking session's worktree, which the
	// pending-dispatch stages point at their claimed worktree fixture.
	scope, versions := challengeApprovalBindings(t, challenge.Error.ConsequenceSummary)
	env.HostApproval = signedHostApproval(privateKey, challengeRef, mutationDigest("concord_work_transition", "workflow_action", env, approvedRaw), scope, versions, grant.SessionRef, grant.AgentRef, env.Worktree, fixedTime(), nonceForChallenge(challengeRef))
	tampered := bytes.Replace(approvedRaw, []byte("corrected premise"), []byte("unapproved premise"), 1)
	refused := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: tampered}, env)
	if refused.Outcome != OutcomeError || refused.Error == nil || refused.Error.Kind != "approval_invalid" || workOneVersion(t, s) != version {
		t.Fatalf("changed contract reused approval: outcome=%s error=%+v", refused.Outcome, refused.Error)
	}
	tamperedDesign := bytes.Replace(approvedRaw, []byte("corrected design"), []byte("unapproved design"), 1)
	refused = dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: tamperedDesign}, env)
	if refused.Outcome != OutcomeError || refused.Error == nil || refused.Error.Kind != "approval_invalid" || workOneVersion(t, s) != version {
		t.Fatalf("changed design reused approval: outcome=%s error=%+v", refused.Outcome, refused.Error)
	}
	var stale map[string]any
	if err := json.Unmarshal(approvedRaw, &stale); err != nil {
		t.Fatal(err)
	}
	stale["expected_version"] = version - 1
	staleRaw, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	refused = dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: staleRaw}, env)
	if refused.Outcome != OutcomeError || refused.Error == nil || refused.Error.Kind != "version_conflict" || workOneVersion(t, s) != version {
		t.Fatalf("stale version admitted correction: outcome=%s error=%+v", refused.Outcome, refused.Error)
	}
	response := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedRaw}, env)
	if response.Outcome != OutcomeOK {
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
	var lawChangeJSON string
	if err := s.DatabaseForTesting().QueryRow(`SELECT law_change FROM workflow_contracts WHERE work_id='work-1' AND contract_version=2`).Scan(&lawChangeJSON); err != nil {
		t.Fatal(err)
	}
	var change store.WorkflowLawChange
	if err := json.Unmarshal([]byte(lawChangeJSON), &change); err != nil {
		t.Fatal(err)
	}
	// The recorded binding names the consumed durable approval row, whose
	// operator identity the fold re-derives as actor_class operator with the
	// approval-prefixed agent ref and the approving session's principal.
	if change.Approval == nil || change.Approval.ApprovalRef == "" || change.Approval.OperationDigest == "" || change.Approval.Consequence == "" {
		t.Fatalf("successor omitted the consumed operator approval binding: %s", lawChangeJSON)
	}
	if change.Approval.Operator.ActorClass != store.ActorOperator || change.Approval.Operator.AgentRef != "approval:"+change.Approval.ApprovalRef {
		t.Fatalf("successor recorded a non-operator approval identity: %s", lawChangeJSON)
	}
	if change.Approval.Operator.PrincipalRef != grant.PrincipalRef || change.Approval.Operator.SessionRef != grant.SessionRef {
		t.Fatalf("successor approval operator %v does not bind the approving session", change.Approval.Operator)
	}
	after, err := store.VerifyWorkflowInstanceDefinition(context.Background(), s, store.BuiltinWorkflowRegistry(), "work-1")
	if err != nil || after.Digest != before.Digest {
		t.Fatalf("correction changed the pinned definition: before=%s after=%s err=%v", before.Digest, after.Digest, err)
	}
	// The correction route must not need or fabricate a worker attempt. The
	// pending-dispatch-overlap stage authorized one window before correcting,
	// so its in-flight attempt is the only row the journey may carry.
	wantAttempts := 0
	if stage == "pending-dispatch-overlap" {
		wantAttempts = 1
	}
	if attempts := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1'`); attempts != wantAttempts {
		t.Fatalf("operator correction required or fabricated %d worker attempts, want %d", attempts, wantAttempts)
	}
	if _, err := json.Marshal(response); err != nil {
		t.Fatalf("correction result cannot be serialized: %v", err)
	}
	if stage == "overlap" {
		if resolutions := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions`); resolutions != 0 {
			t.Fatalf("contract correction silently resolved %d overlaps", resolutions)
		}
		boundaryErr := s.Transact(context.Background(), func(tx *store.Transaction) error {
			return store.CheckWorkflowConsequentialBoundaryTx(context.Background(), tx, "work-1")
		})
		var failure *store.Failure
		if !errors.As(boundaryErr, &failure) || failure.Kind != store.KindDomainOverlap {
			t.Fatalf("correction bypassed subsequent overlap admission: %v", boundaryErr)
		}
		if _, err := json.Marshal(failureEnvelope(NewBase("overlap-check", "concord_work_transition", "workflow_action"), boundaryErr)); err != nil {
			t.Fatalf("overlap refusal cannot be serialized: %v", err)
		}
	}
	if stage == "shared-domain" {
		if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
			return store.CheckWorkflowConsequentialBoundaryTx(context.Background(), tx, "work-1")
		}); err != nil {
			t.Fatalf("shared Domain without shared writes blocked execution: %v", err)
		}
	}
	if stage == "self-repair" {
		if continuity.Contract == nil || continuity.Contract.SelfRepair == nil {
			t.Fatalf("approved correction omitted self-repair classification: %+v", continuity.Contract)
		}
		if len(continuity.UnresolvedOverlaps) == 0 {
			t.Fatal("self-repair classification hid the unresolved overlap")
		}
		// consequence_v1 classifications exempt nothing from the overlap
		// boundary (d5bac07f6, workflowSelfRepairOverlapExempt): the
		// classification records the blocked operation, the overlap stays
		// reported, and the real intersection resolves through the dedicated
		// overlap route.
		boundaryErr := s.Transact(context.Background(), func(tx *store.Transaction) error {
			return store.CheckWorkflowConsequentialBoundaryTx(context.Background(), tx, "work-1")
		})
		var failure *store.Failure
		if !errors.As(boundaryErr, &failure) || failure.Kind != store.KindDomainOverlap {
			t.Fatalf("self-repair classification exempted the recorded overlap: %v", boundaryErr)
		}
	}
	committedVersion := workOneVersion(t, s)
	replayed := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedRaw}, env)
	if replayed.Outcome != OutcomeOK || !replayed.Replayed || workOneVersion(t, s) != committedVersion {
		t.Fatalf("correction replay mutated work or failed: outcome=%s replayed=%v error=%+v", replayed.Outcome, replayed.Replayed, replayed.Error)
	}
	if designs := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_design_records WHERE work_id='work-1'`); designs != 2 {
		t.Fatalf("replayed correction changed design history: rows=%d", designs)
	}
}

func workflowDispatchWorktreeFixture(t *testing.T, s *store.Store, env CallEnvelope) CallEnvelope {
	t.Helper()
	env.Worktree = t.TempDir()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,git_facts)
		VALUES(?,'project-1','correction-claim','work/correction',?,?,'repo-1','active','2026-09-05T00:00:00Z','{}');
		DELETE FROM write_guard`, store.WorktreeSetID("work-1"), strings.Repeat("a", 40), env.Worktree); err != nil {
		t.Fatal(err)
	}
	return env
}

func seedContractCorrectionPeer(t *testing.T, s *store.Store) {
	t.Helper()
	events := []store.Event{
		{EventID: "correction-peer-created", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "correction-peer", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("work.created"), Payload: json.RawMessage(`{"work_kind":"task","title":"Peer work","priority":1}`)},
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
		INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,domain_registry_content_hash,home_domain_id,projection_hash)
		SELECT 'correction-peer',contract_version,product_id,domain_registry_content_hash,home_domain_id,projection_hash FROM workflow_architecture_bindings WHERE work_id='work-1' AND contract_version=1;
		INSERT INTO workflow_contract_affected_domains(work_id,contract_version,domain_id)
		SELECT 'correction-peer',contract_version,domain_id FROM workflow_contract_affected_domains WHERE work_id='work-1' AND contract_version=1;
		INSERT INTO workflow_contract_domain_modifications(work_id,contract_version,domain_id)
		SELECT 'correction-peer',contract_version,domain_id FROM workflow_contract_domain_modifications WHERE work_id='work-1' AND contract_version=1;
		DELETE FROM write_guard;`, breakFixFixtureDigest(t))
	if err != nil {
		t.Fatal(err)
	}
}
