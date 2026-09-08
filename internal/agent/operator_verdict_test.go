package agent

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// CD-0116 at the agent surface: the session that delivered in-session submits
// its verdict, the surface mints the operator challenge instead of refusing
// blind, and the signed approval records the verdict with the operator as the
// verdict actor.
func TestOperatorVerdictChallengeAfterInSessionDelivery(t *testing.T) {
	ctx := context.Background()
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"product_read", "work_define", "work_transition"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	workID := captureCompositionWork(t, ctx, s, service, env, "Operator verdict challenge", "task", "workflow.generic_one_off", "operator-verdict-capture")

	signedAction := func(actionID string, fields map[string]any, key string) Envelope {
		t.Helper()
		var version int64
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
			t.Fatal(err)
		}
		input := map[string]any{"work_id": workID, "expected_version": version, "action_id": actionID, "fields": fields, "idempotency_key": key}
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		env.RequestID = "request:" + key + ":" + strconv.FormatInt(version, 10)
		response := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
		if response.Error != nil && response.Error.Kind == "approval_required" {
			challengeRef, _ := response.Error.Details["approval_ref"].(string)
			if challengeRef == "" {
				t.Fatalf("%s minted no challenge", actionID)
			}
			input["approval"] = map[string]any{"approval_ref": challengeRef}
			approvedRaw, _ := json.Marshal(input)
			scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{workID}, "scope_version": env.ScopeVersion}
			versions := map[string]any{"work": version}
			approvalEnv := env
			approvalEnv.HostApproval = signedHostApproval(privateKey, challengeRef, mutationDigest("concord_work_transition", "workflow_action", env, approvedRaw), scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), nonceForChallenge(challengeRef))
			response = dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedRaw}, approvalEnv)
		}
		return response
	}

	contract := signedAction("approve_contract", map[string]any{
		"premise": "The operator signs the verdict after an in-session delivery.", "contract_version": 1,
		"required_evidence": []string{"verification"}, "route_conventions": []string{},
		"outcome_predicates": []map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:fixture", "immutable_subject_ref": "commit:fixture", "expected_result": "pass"}}},
	}, "operator-verdict-contract")
	if contract.Outcome != OutcomeOK {
		t.Fatalf("approve_contract refused: %+v", contract.Error)
	}
	if started := signedAction("start_action", map[string]any{"summary": "in-session step"}, "operator-verdict-start"); started.Outcome != OutcomeOK {
		t.Fatalf("start_action refused: %+v", started.Error)
	}
	if bound := signedAction("bind_evidence", map[string]any{"evidence_kind": "verification", "evidence_ref": "evidence:operator-challenge"}, "operator-verdict-bind"); bound.Outcome != OutcomeOK {
		t.Fatalf("bind_evidence refused: %+v", bound.Error)
	}
	if delivered := signedAction("record_delivery", map[string]any{"summary": "delivered in-session"}, "operator-verdict-delivery"); delivered.Outcome != OutcomeOK {
		t.Fatalf("record_delivery refused: %+v", delivered.Error)
	}

	// The verdict from the delivering session mints the operator challenge,
	// and the signed approval records the verdict.
	verdict := signedAction("record_verdict", map[string]any{
		"predicate_id": "predicate:primary", "verdict_kind": "ok", "evaluation_evidence": []string{"evidence:operator-challenge"},
	}, "operator-verdict-verdict")
	if verdict.Outcome != OutcomeOK {
		t.Fatalf("operator-signed verdict refused: %+v", verdict.Error)
	}
	var step string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	if step != "verify" && step != "acceptance" {
		t.Fatalf("current_step=%q after the verdict", step)
	}
	var verdictActor string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT json_extract(payload,'$.verdict_actor_ref') FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, workID, store.WorkflowVerdictRecorded).Scan(&verdictActor); err != nil {
		t.Fatal(err)
	}
	if verdictActor == "" || verdictActor == store.DeriveWorkflowActorRef(grant.PrincipalRef, grant.ClientRef, grant.AgentRef, grant.SessionRef) {
		t.Fatalf("verdict actor=%q, want the operator identity", verdictActor)
	}
}

// CD-0116 after a lane exit: the session that accepted a completed worker
// result submits its verdict, the surface mints the operator challenge, and
// the signed approval records the verdict with the operator identity. A
// distinct agent evaluator keeps the ordinary verdict route.
func TestOperatorVerdictChallengeAfterAcceptedWorkerResult(t *testing.T) {
	ctx := context.Background()
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"product_read", "work_define", "work_transition", "worker_dispatch"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	workID := captureCompositionWork(t, ctx, s, service, env, "Verdict after acceptance", "task", "workflow.generic_one_off", "accept-e2e-capture")

	// Dispatch admission requires the host session to sit inside the work
	// item's active worktree claim, compared by filesystem identity, so the
	// dispatching session runs in a real directory that an active claim names.
	sessionWorktree := t.TempDir()
	db := s.DatabaseForTesting()
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO fold_guard(active) VALUES(1)`, nil},
		{`INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,git_facts) VALUES(?,?,?,?,?,?,?,'active',?,'{}')`, []any{store.WorktreeSetID(workID), "project-1", "accept-e2e-claim", "work/accept-e2e", strings.Repeat("a", 40), sessionWorktree, "repo-1", "2026-09-05T00:00:00Z"}},
		{`DELETE FROM fold_guard`, nil},
	} {
		if _, err := db.ExecContext(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("seed active worktree claim: %v", err)
		}
	}
	env.Worktree = sessionWorktree

	signedAction := func(actionID string, fields map[string]any, key string) Envelope {
		t.Helper()
		var version int64
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
			t.Fatal(err)
		}
		input := map[string]any{"work_id": workID, "expected_version": version, "action_id": actionID, "fields": fields, "idempotency_key": key}
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		env.RequestID = "request:" + key + ":" + strconv.FormatInt(version, 10)
		response := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
		if response.Error != nil && response.Error.Kind == "approval_required" {
			challengeRef, _ := response.Error.Details["approval_ref"].(string)
			if challengeRef == "" {
				t.Fatalf("%s minted no challenge", actionID)
			}
			input["approval"] = map[string]any{"approval_ref": challengeRef}
			approvedRaw, _ := json.Marshal(input)
			scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{workID}, "scope_version": env.ScopeVersion}
			versions := map[string]any{"work": version}
			approvalEnv := env
			approvalEnv.HostApproval = signedHostApproval(privateKey, challengeRef, mutationDigest("concord_work_transition", "workflow_action", env, approvedRaw), scope, versions, grant.SessionRef, grant.AgentRef, env.Worktree, fixedTime(), nonceForChallenge(challengeRef))
			response = dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedRaw}, approvalEnv)
		}
		return response
	}

	// Evaluator independence (#801): the session that accepted the worker
	// result receives an operator challenge, while a distinct agent evaluator
	// remains an ordinary verdict route.
	reviewInvocation := Invocation{
		ClientRef: grant.ClientRef, PrincipalRef: grant.PrincipalRef, SessionRef: "session-evaluator", AgentRef: "agent-evaluator",
		Directory: grant.Directory, Worktree: grant.Worktree, ManifestDigest: ManifestDigest,
		RequiredCapability: "work_transition", ProductID: "product-1", ProjectID: "project-1",
	}
	reviewAuthority, err := service.Authorize(ctx, reviewInvocation)
	if err != nil {
		t.Fatalf("evaluator authorization refused: %v", err)
	}
	reviewEnv := mutationEnvelope(reviewAuthority, scopeVersion)
	signedReviewAction := func(actionID string, fields map[string]any, key string) Envelope {
		t.Helper()
		var version int64
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
			t.Fatal(err)
		}
		input := map[string]any{"work_id": workID, "expected_version": version, "action_id": actionID, "fields": fields, "idempotency_key": key}
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		reviewEnv.RequestID = "request:" + key + ":" + strconv.FormatInt(version, 10)
		return dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, reviewEnv)
	}

	contract := signedAction("approve_contract", map[string]any{
		"premise": "A verdict after acceptance cites the accepted attempt.", "contract_version": 1,
		"required_evidence": []string{"verification"}, "route_conventions": []string{},
		"outcome_predicates": []map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:fixture", "immutable_subject_ref": "commit:fixture", "expected_result": "pass"}}},
	}, "accept-e2e-contract")
	if contract.Outcome != OutcomeOK {
		t.Fatalf("approve_contract refused: %+v", contract.Error)
	}
	if started := signedAction("start_action", map[string]any{"summary": "lane-run step"}, "accept-e2e-start"); started.Outcome != OutcomeOK {
		t.Fatalf("start_action refused: %+v", started.Error)
	}

	var lane store.LaneDefinition
	for _, candidate := range store.BuiltinLaneDefinitions() {
		if candidate.ID == "verify" {
			lane = candidate
		}
	}
	attemptID := "attempt:accept-e2e"
	dispatched := signedAction("dispatch_worker", map[string]any{
		"attempt_id": attemptID,
		"worker_packet": map[string]any{
			"schema_version": "1.0", "attempt_id": attemptID, "lane_id": lane.ID, "lane_version": lane.Version, "lane_digest": lane.Digest,
			"work_id": workID, "step_id": "execute", "inputs": map[string]any{"task": "prove the acceptance binds evidence"},
		},
	}, "accept-e2e-dispatch")
	if dispatched.Outcome != OutcomeOK {
		t.Fatalf("dispatch_worker refused: %+v", dispatched.Error)
	}

	// CD-0109: the dispatch route derives the lane actor and appends the
	// actor/dispatch pair through the authoritative closed-shape route.
	dispatchedEventPayload, err := json.Marshal(store.WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: "openai/test-model", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion, PacketDigest: "sha256:" + strings.Repeat("0", 64)})
	if err != nil {
		t.Fatal(err)
	}
	dispatchEvent := store.Event{EventID: "accept-e2e-worker-dispatched", Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 2, Payload: dispatchedEventPayload}
	err = s.Transact(ctx, func(tx *store.Transaction) error {
		pair, err := store.PrepareLaneActorDispatch(ctx, tx, dispatchEvent, "principal/operator", "client/concord-1")
		if err != nil {
			return err
		}
		_, err = store.AppendLaneActorDispatchTx(ctx, tx, pair)
		return err
	})
	if err != nil {
		t.Fatalf("worker dispatch folded: %v", err)
	}

	// The lane completes with a readback model and every obligation discharged.
	evidence := make([]store.WorkerReportEvidence, 0, len(lane.EvidenceObligations))
	for _, obligation := range lane.EvidenceObligations {
		evidence = append(evidence, store.WorkerReportEvidence{Obligation: obligation, Detail: "discharged " + obligation})
	}
	completedPayload, err := json.Marshal(store.WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: "openai/test-model", ReportSchemaVersion: store.WorkerReportSchemaVersion, Evidence: evidence, EvidenceOrigin: store.WorkerEvidenceReported})
	if err != nil {
		t.Fatal(err)
	}
	completion := store.Event{EventID: "accept-e2e-worker-completed", Kind: store.WorkerCompleted, SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 2, Payload: completedPayload}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{completion}}); err != nil {
		t.Fatalf("worker completion folded: %v", err)
	}

	accepted := signedAction("accept_worker_result", map[string]any{"attempt_id": attemptID, "attempt_epoch": 2}, "accept-e2e-accept")
	if accepted.Outcome != OutcomeOK {
		t.Fatalf("accept_worker_result refused: %+v", accepted.Error)
	}

	var step string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	if step != "verify" {
		t.Fatalf("current_step=%q, want verify", step)
	}
	operatorVerdict := signedAction("record_verdict", map[string]any{
		"predicate_id": "predicate:primary", "verdict_kind": "ok", "evaluation_evidence": []string{attemptID},
	}, "accept-e2e-operator-verdict")
	if operatorVerdict.Outcome != OutcomeOK {
		t.Fatalf("operator verdict challenge after accepted worker result refused: %+v", operatorVerdict.Error)
	}
	var operatorVerdictActor string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT json_extract(payload,'$.verdict_actor_ref') FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, workID, store.WorkflowVerdictRecorded).Scan(&operatorVerdictActor); err != nil {
		t.Fatal(err)
	}
	if operatorVerdictActor == store.DeriveWorkflowActorRef(grant.PrincipalRef, grant.ClientRef, grant.AgentRef, grant.SessionRef) {
		t.Fatalf("operator verdict actor=%q, want the signed operator identity", operatorVerdictActor)
	}
	verdict := signedReviewAction("record_verdict", map[string]any{
		"predicate_id": "predicate:primary", "verdict_kind": "ok", "evaluation_evidence": []string{attemptID},
	}, "accept-e2e-verdict")
	if verdict.Outcome != OutcomeOK {
		t.Fatalf("verdict citing the accepted attempt refused: %+v", verdict.Error)
	}
	var bound int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.immutable_subject_ref')=?`, workID, store.WorkflowEvidenceBound, attemptID).Scan(&bound); err != nil {
		t.Fatal(err)
	}
	if bound == 0 {
		t.Fatal("the accepted attempt was never bound as evidence")
	}
	if !strings.Contains(string(verdict.Result), workID) {
		t.Fatalf("verdict result carried no work reference: %s", verdict.Result)
	}
}
