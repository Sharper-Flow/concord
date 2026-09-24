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

// TestEscalatedVerificationCorrectionRetryMintsBindableChallengeAndAdmitsOneAttempt
// pins the verification escalation wall: after the fourth recorded correction
// request the dispatch mints the standard approval challenge bound to the
// correction's attempt count, and one operator approval admits exactly one
// fresh fenced attempt of the unchanged approved contract. CD-0164 D4 arms
// the wall when the request count passes the limit, so three recorded
// corrections stay approval-free. Before the repair this dispatch returned
// approval_required with no approval reference, so the escalated
// verification correction could never leave the limit.
func TestEscalatedVerificationCorrectionRetryMintsBindableChallengeAndAdmitsOneAttempt(t *testing.T) {
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
	version, worktree := seedEscalatedVerificationWorkerMutation(t, s, service, grant)
	grant.Worktree = worktree
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	retryAttemptID := "attempt:work-1:verification-4"
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil || pin.Correction == nil || !pin.Correction.Escalated || pin.Correction.Disposition != "verification" {
		t.Fatalf("read escalated verification correction: pin=%#v err=%v", pin.Correction, err)
	}
	input := map[string]any{
		"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker",
		"idempotency_key": "verification-retry-1", "fields": map[string]any{"attempt_id": retryAttemptID, "worker_packet": retryMutationPacket(t, "work-1", "execution", retryAttemptID, pin.Correction)},
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	challenge := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("escalated verification retry without approval = %+v, want approval_required", challenge.Error)
	}
	details := challenge.Error.Details
	challengeRef, _ := details["approval_ref"].(string)
	if len(challengeRef) != 64 {
		t.Fatalf("verification challenge carries no approval reference: %+v", details)
	}
	if got, _ := details["operation_digest"].(string); got == "" {
		t.Fatalf("verification challenge carries no operation digest: %+v", details)
	}
	if got, _ := details["action_id"].(string); got != "dispatch_worker" {
		t.Fatalf("verification challenge action_id = %v", details["action_id"])
	}
	if got, _ := details["contract_version"].(string); got != "1" {
		t.Fatalf("verification challenge contract_version = %v, want the approved contract version", details["contract_version"])
	}
	if got, _ := details["premise_summary"].(string); got != "approved retry objective" {
		t.Fatalf("verification challenge premise_summary = %q, want the approved contract premise", details["premise_summary"])
	}
	assertBindingContains(t, details["scope"], "work_ids:work-1")
	scopeList, scopeOK := details["scope"].([]string)
	if !scopeOK {
		raw, err := json.Marshal(details["scope"])
		if err != nil {
			t.Fatal(err)
		}
		var decoded []string
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("challenge scope is not a string list: %+v", details["scope"])
		}
		scopeList = decoded
	}
	for _, binding := range scopeList {
		if strings.HasPrefix(binding, "failed_attempt_id:") {
			t.Fatalf("verification challenge scope binds a failed attempt: %v", details["scope"])
		}
	}
	assertBindingContains(t, details["versions"], "correction_attempts:4")
	assertBindingContains(t, details["versions"], "contract:1")
	assertBindingContains(t, details["versions"], "work:"+strconv.FormatInt(version, 10))
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1'`); got != 3 {
		t.Fatalf("challenge created %d worker attempts, want 3", got)
	}

	approvedInput := cloneWithApproval(t, input, challengeRef)
	approvedRaw, _ := json.Marshal(approvedInput)
	scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}
	versions := map[string]any{"work": version, "contract": int64(1), "correction_attempts": int64(4)}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, mutationDigest("concord_work_transition", "workflow_action", env, approvedRaw), scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), nonceForChallenge(challengeRef))
	approved := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedRaw}, env)
	if approved.Outcome != OutcomeOK {
		t.Fatalf("approved escalated verification retry = %+v", approved.Error)
	}
	var retryEpoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE kind=? AND subject_id='work-1' AND json_extract(payload,'$.action_id')='dispatch_worker' ORDER BY seq DESC LIMIT 1`, store.WorkflowActionStarted).Scan(&retryEpoch); err != nil {
		t.Fatal(err)
	}
	if retryEpoch != 7 {
		t.Fatalf("approved escalated verification retry epoch=%d, want 7", retryEpoch)
	}
	var dispatched int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.idempotency_identity') LIKE '%verification-retry-1%'`, store.WorkflowActionStarted).Scan(&dispatched); err != nil {
		t.Fatal(err)
	}
	if dispatched != 1 {
		t.Fatalf("approved verification retry recorded %d dispatch authorizations for %s, want 1", dispatched, retryAttemptID)
	}
}

// TestEscalatedVerificationCorrectionRetryRefusesStaleApproval pins the fence
// around the approvable verification wall: a stale approval has no effect, one
// approval admits one attempt, a spent challenge cannot authorize the re-armed
// wall, and a fresh operator decision admits the next fenced attempt.
func TestEscalatedVerificationCorrectionRetryRefusesStaleApproval(t *testing.T) {
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
	version, worktree := seedEscalatedVerificationWorkerMutation(t, s, service, grant)
	grant.Worktree = worktree
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	retryAttemptID := "attempt:work-1:verification-4"
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil || pin.Correction == nil {
		t.Fatalf("read escalated verification correction: pin=%#v err=%v", pin.Correction, err)
	}
	input := map[string]any{
		"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker",
		"idempotency_key": "verification-retry-stale", "fields": map[string]any{"attempt_id": retryAttemptID, "worker_packet": retryMutationPacket(t, "work-1", "execution", retryAttemptID, pin.Correction)},
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	challenge := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	challengeRef, _ := challenge.Error.Details["approval_ref"].(string)
	if challengeRef == "" {
		t.Fatalf("verification challenge = %+v", challenge.Error)
	}

	// A stale approval names an attempt count the wall never armed at. The
	// signed bindings cannot match the challenge spec, so the mismatch refuses
	// with no durable effect.
	bindingScope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}
	staleVersions := map[string]any{"work": version, "contract": int64(1), "correction_attempts": int64(3)}
	staleInput := cloneWithApproval(t, input, challengeRef)
	staleRaw, _ := json.Marshal(staleInput)
	env.HostApproval = signedHostApproval(privateKey, challengeRef, mutationDigest("concord_work_transition", "workflow_action", env, staleRaw), bindingScope, staleVersions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), nonceForChallenge(challengeRef))
	stale := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: staleRaw}, env)
	if stale.Outcome != OutcomeError || stale.Error == nil || !strings.Contains(stale.Error.Message, "scope or versions invalid") {
		t.Fatalf("stale verification approval = %+v, want a stale-binding refusal", stale.Error)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1'`); got != 3 {
		t.Fatalf("stale approval created %d worker attempts, want 3", got)
	}

	// Consume the challenge with the matching approval, then re-arm the wall
	// with three failed attempts, and try to reuse the spent challenge on the
	// re-armed failed correction.
	approvedVersions := map[string]any{"work": version, "contract": int64(1), "correction_attempts": int64(4)}
	approvedInput := cloneWithApproval(t, input, challengeRef)
	approvedRaw, _ := json.Marshal(approvedInput)
	env.HostApproval = signedHostApproval(privateKey, challengeRef, mutationDigest("concord_work_transition", "workflow_action", env, approvedRaw), bindingScope, approvedVersions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), nonceForChallenge(challengeRef))
	approved := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedRaw}, env)
	if approved.Outcome != OutcomeOK {
		t.Fatalf("approved verification retry = %+v", approved.Error)
	}
	for cycle := int64(5); cycle <= 7; cycle++ {
		rearmAttemptID := "attempt:work-1:verification-" + strconv.FormatInt(cycle, 10)
		recordVerificationFailure(t, s, service, grant, rearmAttemptID, 7)
	}
	nextPin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil || nextPin.Correction == nil || nextPin.Correction.Disposition != "failed" || nextPin.Correction.FailedAttemptID != "attempt:work-1:verification-7" || !nextPin.Correction.Escalated {
		t.Fatalf("reread re-armed correction after retry: pin=%#v err=%v", nextPin.Correction, err)
	}
	version = workVersion(t, s, "work-1")
	nextAttemptID := "attempt:work-1:verification-8"
	reusedInput := map[string]any{
		"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker",
		"idempotency_key": "verification-retry-reuse", "fields": map[string]any{"attempt_id": nextAttemptID, "worker_packet": retryMutationPacket(t, "work-1", "execution", nextAttemptID, nextPin.Correction)},
	}
	withSpentApproval := cloneWithApproval(t, reusedInput, challengeRef)
	reusedRaw, _ := json.Marshal(withSpentApproval)
	currentScope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "failed_attempt_id": "attempt:work-1:verification-7", "scope_version": scopeVersion}
	currentVersions := map[string]any{"work": version, "contract": int64(1), "failed_attempt_epoch": int64(7)}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, mutationDigest("concord_work_transition", "workflow_action", env, reusedRaw), currentScope, currentVersions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), nonceForChallenge(challengeRef))
	reusedResponse := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: reusedRaw}, env)
	if reusedResponse.Outcome != OutcomeError || reusedResponse.Error == nil || !strings.Contains(reusedResponse.Error.Message, "approval challenge binding invalid") {
		t.Fatalf("reused approval = %+v, want an approval-binding refusal", reusedResponse.Error)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1'`); got != 6 {
		t.Fatalf("reused approval created %d worker attempts, want 6", got)
	}

	// The wall re-arms: a fresh request mints a fresh challenge bound to the
	// new failed attempt, and its approval admits the next fenced attempt.
	freshInput := map[string]any{
		"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker",
		"idempotency_key": "verification-retry-fresh", "fields": map[string]any{"attempt_id": nextAttemptID, "worker_packet": retryMutationPacket(t, "work-1", "execution", nextAttemptID, nextPin.Correction)},
	}
	freshRaw, _ := json.Marshal(freshInput)
	freshChallenge := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: freshRaw}, env)
	freshRef, _ := freshChallenge.Error.Details["approval_ref"].(string)
	if freshRef == "" || freshRef == challengeRef {
		t.Fatalf("fresh re-armed challenge = %+v, want a new approval reference", freshChallenge.Error)
	}
	freshApproved := cloneWithApproval(t, freshInput, freshRef)
	freshApprovedRaw, _ := json.Marshal(freshApproved)
	freshVersions := map[string]any{"work": version, "contract": int64(1), "failed_attempt_epoch": int64(7)}
	env.HostApproval = signedHostApproval(privateKey, freshRef, mutationDigest("concord_work_transition", "workflow_action", env, freshApprovedRaw), currentScope, freshVersions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), nonceForChallenge(freshRef))
	valid := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: freshApprovedRaw}, env)
	if valid.Outcome != OutcomeOK {
		t.Fatalf("fresh approved re-armed retry = %+v", valid.Error)
	}
}

// seedEscalatedVerificationWorkerMutation drives three full verification
// correction cycles through the boundary and then appends a fourth recorded
// request_correction. Three requests stay below the wall (CD-0164 D4: the
// dispatch comparator is strict), and the fourth record arms it, so the
// fourth dispatch faces the escalated wall with a durable correction to bind.
func seedEscalatedVerificationWorkerMutation(t *testing.T, s *store.Store, service *Service, grant Authority) (int64, string) {
	t.Helper()
	if got := seedAgentWorkflow(t, s, grant); got != 4 {
		t.Fatalf("workflow seed version=%d, want 4", got)
	}
	owner := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	ownerRef, err := store.WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	reviewer := store.WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/reviewer", SessionRef: "session/work-1-reviewer", ActorClass: store.ActorAgent}
	operator := store.WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/operator", SessionRef: "session/work-1-operator", ActorClass: store.ActorOperator}
	path := t.TempDir()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		UPDATE workflow_instances SET current_step='execution' WHERE work_id='work-1';
		INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('work-1',1,'approved retry objective','internal_sqlite','[]','[]','now',?,'[]','[]',0,'prototype_internal');
		INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES('work-1',1,'predicate:primary',0,'check','{"kind":"check","check_ref":"check:workflow","immutable_subject_ref":"commit:verification","expected_result":"pass"}');
		DELETE FROM fold_guard`, ownerRef); err != nil {
		t.Fatalf("seed verification projections: %v", err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at) VALUES(?,?,?,?,?,?,?,'active',?);
		DELETE FROM fold_guard`, store.WorktreeSetID("work-1"), "project-1", "claim:verification", "verification", strings.Repeat("a", 40), path, "repo:verification", fixedTime().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed verification worktree: %v", err)
	}
	var claimed int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worktree_entries WHERE set_id=? AND state='active'`, store.WorktreeSetID("work-1")).Scan(&claimed); err != nil || claimed != 1 {
		t.Fatalf("verification worktree claim count=%d err=%v", claimed, err)
	}
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	grant.Worktree = path
	env := mutationEnvelope(grant, scopeVersion)
	version := int64(4)
	for cycle := int64(1); cycle <= 3; cycle++ {
		attemptID := "attempt:work-1:verification-" + strconv.FormatInt(cycle, 10)
		version = workVersion(t, s, "work-1")
		start := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: json.RawMessage(`{"work_id":"work-1","expected_version":` + strconv.FormatInt(version, 10) + `,"action_id":"start_execution","idempotency_key":"verification-start-` + strconv.FormatInt(cycle, 10) + `"}`)}, env)
		if start.Outcome != OutcomeOK {
			t.Fatalf("seed verification start %d: %+v", cycle, start.Error)
		}
		version = workVersion(t, s, "work-1")
		pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
		if err != nil {
			t.Fatal(err)
		}
		input := map[string]any{
			"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker",
			"idempotency_key": "verification-dispatch-" + strconv.FormatInt(cycle, 10), "fields": map[string]any{"attempt_id": attemptID, "worker_packet": retryMutationPacket(t, "work-1", "execution", attemptID, pin.Correction)},
		}
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		env.RequestID = "request:verification-dispatch-" + strconv.FormatInt(cycle, 10)
		dispatch := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
		if dispatch.Outcome != OutcomeOK {
			t.Fatalf("seed verification dispatch %d: %+v", cycle, dispatch.Error)
		}
		var attemptEpoch int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' ORDER BY seq DESC LIMIT 1`, store.WorkflowActionStarted).Scan(&attemptEpoch); err != nil {
			t.Fatal(err)
		}
		applyVerificationWorkerDispatchAndCompletion(t, s, grant, attemptID, "verification")
		version = workVersion(t, s, "work-1")
		accept := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "accept_worker_result", "fields": map[string]any{"attempt_id": attemptID, "attempt_epoch": attemptEpoch}, "idempotency_key": "verification-accept-" + strconv.FormatInt(cycle, 10)})}, env)
		if accept.Outcome != OutcomeOK {
			t.Fatalf("seed verification accept %d: %+v", cycle, accept.Error)
		}
		version = workVersion(t, s, "work-1")
		refineStart := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: json.RawMessage(`{"work_id":"work-1","expected_version":` + strconv.FormatInt(version, 10) + `,"action_id":"start_refine","idempotency_key":"verification-refine-start-` + strconv.FormatInt(cycle, 10) + `"}`)}, env)
		if refineStart.Outcome != OutcomeOK {
			t.Fatalf("seed verification refine start %d: %+v", cycle, refineStart.Error)
		}
		for _, deliveryStep := range []string{"refine", "delivery"} {
			version = workVersion(t, s, "work-1")
			delivery := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "record_delivery", "fields": map[string]any{"delivery_artifact": "evidence:verification-" + deliveryStep + "-" + strconv.FormatInt(cycle, 10), "delivery_state": "asserted"}, "idempotency_key": "verification-delivery-" + deliveryStep + "-" + strconv.FormatInt(cycle, 10)})}, env)
			if delivery.Outcome != OutcomeOK {
				t.Fatalf("seed verification delivery %s %d: %+v", deliveryStep, cycle, delivery.Error)
			}
		}
		runVerificationStoreAction(t, s, "work-1", "record_verdict", map[string]any{
			"contract_version": 1, "predicate_id": "predicate:primary", "verdict_kind": "outcome_mismatch",
			"evaluation_evidence": []string{attemptID}, "incomparable_with_approved": true,
		}, reviewer, nil, "verdict-"+strconv.FormatInt(cycle, 10))
		runVerificationStoreAction(t, s, "work-1", "request_correction", map[string]any{
			"diagnosis":     "the delivered subject still misses the approved predicate",
			"strategy":      "repeat the implementation external effect",
			"predicate_ids": []string{"predicate:primary"},
			"evidence_refs": []string{attemptID},
		}, owner, &operator, "correction-"+strconv.FormatInt(cycle, 10))
		pin, err = store.ReadWorkPin(context.Background(), s, "work-1")
		if err != nil {
			t.Fatal(err)
		}
		if pin.Correction == nil || pin.Correction.Disposition != "verification" || pin.Correction.AttemptCount != cycle || pin.Correction.Escalated {
			t.Fatalf("verification correction after cycle %d = %#v", cycle, pin.Correction)
		}
	}
	// The boundary's request gate refuses a fourth request once three sit in
	// its counting window, so no live route records one. The log is the
	// authority for a recorded request, and production held exactly such a
	// fourth record: append it the way the log kept it.
	var recorded []byte
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='request_correction' ORDER BY seq DESC LIMIT 1`, string(store.SubjectWorkItem), "work-1", store.WorkflowActionCompleted).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(recorded, &fields); err != nil {
		t.Fatal(err)
	}
	fields["idempotency_identity"] = "verification-correction-request-4"
	updated, marshalErr := json.Marshal(fields)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?)`, "verification-correction-request-4", store.WorkflowActionCompleted, store.SubjectWorkItem, "work-1", grant.PrincipalRef, fixedTime().Format(time.RFC3339Nano), 1, updated); err != nil {
		t.Fatal(err)
	}
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	if pin.Correction == nil || pin.Correction.Disposition != "verification" || pin.Correction.AttemptCount != 4 || !pin.Correction.Escalated {
		t.Fatalf("escalated verification correction after the fourth request = %#v", pin.Correction)
	}
	if binding, err := store.WorkflowFailedWorkerRetryBinding(context.Background(), s, "work-1"); err != nil || binding == nil || binding.FailedAttemptID != "" || binding.FailedAttemptEpoch != 0 || binding.CorrectionAttempts != 4 || binding.ContractVersion != 1 {
		t.Fatalf("escalated verification retry binding=%+v err=%v", binding, err)
	}
	return workVersion(t, s, "work-1"), path
}

// recordVerificationFailure records one failed lane attempt and its
// record_worker_failure action, so the failed wall re-arms after the verified
// verification retry.
func recordVerificationFailure(t *testing.T, s *store.Store, service *Service, grant Authority, attemptID string, attemptEpoch int64) {
	t.Helper()
	applyEscalatedWorkerDispatchAndFailure(t, s, grant, attemptID, "verification-rearm")
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	version := workVersion(t, s, "work-1")
	record := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "record_worker_failure", "fields": map[string]any{"attempt_id": attemptID, "attempt_epoch": attemptEpoch}, "idempotency_key": "verification-rearm-record-" + attemptID})}, mutationEnvelope(grant, scopeVersion))
	if record.Outcome != OutcomeOK {
		t.Fatalf("record re-arm failure for %s: %+v", attemptID, record.Error)
	}
}

// applyVerificationWorkerDispatchAndCompletion records the lane dispatch and
// the completed report for one verification attempt before the boundary
// accepts the result.
func applyVerificationWorkerDispatchAndCompletion(t *testing.T, s *store.Store, grant Authority, attemptID, suffix string) {
	t.Helper()
	lane := retryLane(t)
	dispatch := store.Event{EventID: suffix + "-dispatch-" + attemptID, Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: retryJSON(store.WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, PacketDigest: "sha256:" + strings.Repeat("c", 64), ReadbackModel: "openai/gpt-5.6-luna", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion})}
	completion := store.Event{EventID: suffix + "-completed-" + attemptID, Kind: store.WorkerCompleted, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: retryJSON(store.WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: "openai/gpt-5.6-luna", ReportSchemaVersion: store.WorkerReportSchemaVersion})}
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		enriched, err := store.PrepareLaneActorDispatch(context.Background(), tx, dispatch, grant.PrincipalRef, grant.ClientRef)
		if err != nil {
			return err
		}
		if _, err := store.AppendLaneActorDispatchTx(context.Background(), tx, enriched); err != nil {
			return err
		}
		_, err = store.ApplyOperationTx(context.Background(), tx, store.Operation{Events: []store.Event{completion}})
		return err
	}); err != nil {
		t.Fatalf("seed verification dispatch and completion for %s: %v", attemptID, err)
	}
}

// runVerificationStoreAction applies one store-level workflow action so the
// fixture can record verdict and correction records without minting operator
// approval challenges on the mutation boundary.
func runVerificationStoreAction(t *testing.T, s *store.Store, workID, action string, fields map[string]any, actor store.WorkflowActor, operator *store.WorkflowActor, suffix string) {
	t.Helper()
	version := workVersion(t, s, workID)
	operationID := action + "-verification-" + suffix + "-" + strconv.FormatInt(version, 10)
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		_, err := store.ApplyWorkflowActionTx(context.Background(), tx, store.BuiltinWorkflowRegistry(), store.WorkflowActionExecutionRequest{
			WorkID: workID, ExpectedVersion: version, ActionID: action, Payload: raw, Actor: actor, OperatorActor: operator,
			AcceptedInputsDigest: "sha256:" + strings.Repeat("f", 64) + operationID, IdempotencyIdentity: operationID, OperationID: operationID,
			PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID, RequestID: "request:" + operationID,
			ContractDigest: ManifestDigest, Now: fixedTime(),
		})
		return err
	}); err != nil {
		t.Fatalf("seed %s for %s: %v", action, suffix, err)
	}
}
