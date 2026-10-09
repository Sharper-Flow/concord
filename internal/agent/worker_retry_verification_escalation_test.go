package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// TestEscalatedVerificationCorrectionWallGatesOnFindingsConvergence pins the
// CON885 convergence gate on an escalated verification correction. Four
// recorded verification correction requests arm the wall (CD-0164 D4 keeps
// the comparator strict, so the first three stay below it, approval-free).
// At the wall the dispatch refuses with missing_evidence and mints no
// approval challenge, a supplied operator approval bound to the correction's
// attempt count — the exact shape the pre-CON885 wall consumed — has no
// effect, and WorkflowFailedWorkerRetryBinding returns nil. The only store
// route through the wall is a findings basis: when the latest correction
// request carries a strictly smaller predicate set than the previous
// comparable request, the store derives findings_shrinking and admits
// exactly one fresh fenced attempt with no approval. The admitted dispatch
// consumes the basis, and its recorded failure closes the findings route:
// the correction record flips to the failed disposition, but the wall the
// converging retry crossed stays armed, so the next dispatch refuses again.
func TestEscalatedVerificationCorrectionWallGatesOnFindingsConvergence(t *testing.T) {
	for _, tc := range []struct {
		name           string
		finalPredicate []string
		admit          bool
	}{
		{"unchanged request predicates keep the wall closed", []string{"predicate:primary", "predicate:secondary"}, false},
		{"shrinking request predicates admit one fenced retry", []string{"predicate:primary"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
			version, worktree := seedEscalatedVerificationWorkerMutation(t, s, service, grant, tc.finalPredicate)
			grant.Worktree = worktree
			scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
			if err != nil {
				t.Fatal(err)
			}
			env := mutationEnvelope(grant, scopeVersion)
			if got := verifyWallChallenges(t, s); got != 0 {
				t.Fatalf("the verification journey minted %d approval challenges, want 0", got)
			}
			if binding, err := store.WorkflowFailedWorkerRetryBinding(context.Background(), s, nil, "work-1"); err != nil || binding != nil {
				t.Fatalf("retry approval binding behind the escalated verification wall = %+v err=%v, want none", binding, err)
			}
			var seedEpoch int64
			if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' ORDER BY seq DESC LIMIT 1`, store.WorkflowActionStarted).Scan(&seedEpoch); err != nil {
				t.Fatal(err)
			}

			retryID := "attempt:work-1:verification-5"
			input := map[string]any{
				"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker",
				"idempotency_key": "verification-convergence-1", "fields": map[string]any{"attempt_id": retryID, "worker_packet": retryMutationPacket(t, s, retryID, verifyWallPinCorrection(t, s))},
			}
			raw, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			env.RequestID = "request:verification-convergence-1"
			attempt := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
			if !tc.admit {
				if attempt.Error == nil || attempt.Error.Kind != "missing_evidence" {
					t.Fatalf("escalated verification retry without a basis = %+v, want missing_evidence", attempt.Error)
				}
				if !strings.Contains(attempt.Error.Message, "worker correction reached the three-attempt limit without a convergence basis") {
					t.Fatalf("verification refusal does not name the convergence limit: %q", attempt.Error.Message)
				}
				if attempt.Error.RecoveryAction.Kind != "provide_evidence" {
					t.Fatalf("verification refusal recovery = %q, want provide_evidence and never an approval ask", attempt.Error.RecoveryAction.Kind)
				}
				if attempt.Error.ConsequenceSummary != nil {
					t.Fatal("verification refusal carries a consequence summary, but no challenge was minted")
				}
				if ref, ok := attempt.Error.Details["approval_ref"].(string); ok && ref != "" {
					t.Fatalf("verification refusal carries approval_ref %q, but no challenge was minted", ref)
				}
				if got := verifyWallChallenges(t, s); got != 0 {
					t.Fatalf("verification refusal minted %d approval challenges, want 0", got)
				}
				if got := verifyWallDispatchCompletions(t, s, retryID); got != 0 {
					t.Fatalf("refused verification retry recorded %d dispatch completions, want 0", got)
				}
				if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1' AND attempt_id='`+retryID+`'`); got != 0 {
					t.Fatalf("refused verification retry created %d worker attempt rows, want 0", got)
				}
				if after := workVersion(t, s, "work-1"); after != version {
					t.Fatalf("refused verification retry changed the work version %d -> %d", version, after)
				}

				// A supplied operator approval bound to the correction's
				// attempt count — the exact identity the pre-CON885 wall
				// minted its challenge for — has no effect: no challenge
				// exists to consume, and the fold still refuses.
				approvedInput := cloneWithApproval(t, input, strings.Repeat("e", 64))
				approvedRaw, err := json.Marshal(approvedInput)
				if err != nil {
					t.Fatal(err)
				}
				scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}
				approvedVersions := map[string]any{"work": version, "contract": int64(1), "correction_attempts": int64(4)}
				env.HostApproval = signedHostApproval(privateKey, strings.Repeat("e", 64), mutationDigest("concord_work_transition", "workflow_action", env, approvedRaw), scope, approvedVersions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), "verification-convergence-unused-approval")
				env.RequestID = "request:verification-convergence-2"
				bypass := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedRaw}, env)
				if bypass.Error == nil || bypass.Error.Kind != "missing_evidence" {
					t.Fatalf("escalated verification retry behind a supplied approval = %+v, want missing_evidence", bypass.Error)
				}
				if got := verifyWallChallenges(t, s); got != 0 {
					t.Fatalf("supplied approval minted %d approval challenges, want 0", got)
				}
				if after := workVersion(t, s, "work-1"); after != version {
					t.Fatalf("supplied approval changed the work version %d -> %d", version, after)
				}
				return
			}

			// The shrinking predicate set derives the findings basis: the
			// boundary admits one fresh fenced attempt with no approval.
			if attempt.Outcome != OutcomeOK {
				t.Fatalf("verification retry behind shrinking findings = %+v", attempt.Error)
			}
			if got := verifyWallChallenges(t, s); got != 0 {
				t.Fatalf("verification retry behind shrinking findings minted %d approval challenges, want 0", got)
			}
			if got := verifyWallDispatchCompletions(t, s, retryID); got != 1 {
				t.Fatalf("verification retry behind shrinking findings recorded %d dispatch completions, want 1", got)
			}
			if epoch := escalatedDispatchEpoch(t, s); epoch != seedEpoch+1 {
				t.Fatalf("verification retry behind shrinking findings epoch=%d, want %d", epoch, seedEpoch+1)
			}
			basis := verifyWallRecordedConvergence(t, s, retryID)
			if basis.Basis != "findings_shrinking" || basis.PreviousRecordSeq == 0 || basis.LatestRecordSeq <= basis.PreviousRecordSeq || basis.SupersededSeq != 0 {
				t.Fatalf("recorded convergence = %+v, want the findings_shrinking basis naming both correction request sequences", basis)
			}

			// The basis admitted exactly one dispatch: the interrupted
			// attempt consumed it, so a blind re-dispatch refuses.
			nextID := "attempt:work-1:verification-6"
			blindInput := map[string]any{
				"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": "dispatch_worker",
				"idempotency_key": "verification-convergence-3", "fields": map[string]any{"attempt_id": nextID, "worker_packet": retryMutationPacket(t, s, nextID, verifyWallPinCorrection(t, s))},
			}
			blindRaw, err := json.Marshal(blindInput)
			if err != nil {
				t.Fatal(err)
			}
			env.RequestID = "request:verification-convergence-3"
			blind := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: blindRaw}, env)
			if blind.Error == nil || blind.Error.Kind != "missing_evidence" {
				t.Fatalf("blind re-dispatch behind the consumed findings basis = %+v, want missing_evidence", blind.Error)
			}
			if got := verifyWallChallenges(t, s); got != 0 {
				t.Fatalf("blind re-dispatch minted %d approval challenges, want 0", got)
			}
			if got := verifyWallDispatchCompletions(t, s, nextID); got != 0 {
				t.Fatalf("blind re-dispatch recorded %d dispatch completions, want 0", got)
			}

			// The admitted attempt fails and the failure records. The
			// materialization closes the unbound correction record — its
			// count resets and the failed attempt owns the record — but the
			// wall the converging retry crossed stays armed, so the findings
			// route is closed: the latest record is now the failure, and no
			// request pair can derive a basis. The next dispatch refuses, and
			// no ordinary approval route appears either.
			recordFailureForEscalatedRetry(t, s, service, grant, retryID, seedEpoch+1)
			rearmed := verifyWallPinCorrection(t, s)
			if rearmed.Disposition != "failed" || rearmed.Escalated || rearmed.AttemptCount != 1 || rearmed.FailedAttemptID != retryID || rearmed.FailedAttemptEpoch != seedEpoch+1 {
				t.Fatalf("correction after the converging retry failed = %+v, want the unescalated failed record bound to %s at epoch %d", rearmed, retryID, seedEpoch+1)
			}
			if binding, err := store.WorkflowFailedWorkerRetryBinding(context.Background(), s, nil, "work-1"); err != nil || binding != nil {
				t.Fatalf("retry approval binding behind the re-armed verification wall = %+v err=%v, want none", binding, err)
			}
			afterID := "attempt:work-1:verification-7"
			afterInput := map[string]any{
				"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": "dispatch_worker",
				"idempotency_key": "verification-convergence-4", "fields": map[string]any{"attempt_id": afterID, "worker_packet": retryMutationPacket(t, s, afterID, rearmed)},
			}
			afterRaw, err := json.Marshal(afterInput)
			if err != nil {
				t.Fatal(err)
			}
			env.RequestID = "request:verification-convergence-4"
			after := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: afterRaw}, env)
			if after.Error == nil || after.Error.Kind != "missing_evidence" {
				t.Fatalf("dispatch after the converging retry failed = %+v, want the convergence refusal", after.Error)
			}
			if got := verifyWallChallenges(t, s); got != 0 {
				t.Fatalf("the whole verification journey minted %d approval challenges, want 0", got)
			}
			if got := verifyWallDispatchCompletions(t, s, afterID); got != 0 {
				t.Fatalf("dispatch after the converging retry failed recorded %d dispatch completions, want 0", got)
			}
		})
	}
}

// seedEscalatedVerificationWorkerMutation drives four full verification
// correction cycles through the boundary, every verdict and every correction
// request recording through its declared action. The contract carries two
// predicates and every cycle's verdicts are non-ok, so no healthy verdict
// set resets the correction window: all four request_correction records are
// comparable. Cycles one through three request both predicates and stay
// below the wall (CD-0164 D4: the dispatch comparator is strict, and each
// cycle's dispatch runs approval-free); the fourth request carries
// finalPredicates, so a strictly smaller set than the previous request
// derives the findings_shrinking basis while an unchanged set leaves the
// wall closed.
func seedEscalatedVerificationWorkerMutation(t *testing.T, s *store.Store, service *Service, grant Authority, finalPredicates []string) (int64, string) {
	t.Helper()
	if got := seedAgentWorkflow(t, s, grant); got != 4 {
		t.Fatalf("workflow seed version=%d, want 4", got)
	}
	// The refine exit resolves the Project's tooling manifest from the
	// canonical path's default ref (CD-0192); point the fixture locator at a
	// real repository so the resolution reads a resolvable ref. The locator
	// table is fold-only, so the update runs under the fold guard.
	repo := fixtureRepoWithOrigin(t)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE project_locators SET locator_value=?1, normalized_value=?1 WHERE project_id='project-1' AND kind='canonical_path'; DELETE FROM fold_guard`, repo); err != nil {
		t.Fatalf("point the fixture locator at the repository: %v", err)
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
		INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES('work-1',1,'predicate:secondary',1,'check','{"kind":"check","check_ref":"check:workflow","immutable_subject_ref":"commit:verification","expected_result":"pass"}');
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
	var version int64
	for cycle := int64(1); cycle <= 4; cycle++ {
		attemptID := "attempt:work-1:verification-" + strconv.FormatInt(cycle, 10)
		version = workVersion(t, s, "work-1")
		start := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: json.RawMessage(`{"work_id":"work-1","expected_version":` + strconv.FormatInt(version, 10) + `,"action_id":"start_execution","idempotency_key":"verification-start-` + strconv.FormatInt(cycle, 10) + `"}`)}, env)
		if start.Outcome != OutcomeOK {
			t.Fatalf("seed verification start %d: %+v", cycle, start.Error)
		}
		// Each cycle executes its own recorded job: an accepted job is
		// satisfied and never redispatches (CD-0205).
		recordReadyRetryJob(t, s, service, env, "job:verification-"+strconv.FormatInt(cycle, 10))
		version = workVersion(t, s, "work-1")
		pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
		if err != nil {
			t.Fatal(err)
		}
		input := map[string]any{
			"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker",
			"idempotency_key": "verification-dispatch-" + strconv.FormatInt(cycle, 10), "fields": map[string]any{"attempt_id": attemptID, "worker_packet": retryMutationPacket(t, s, attemptID, pin.Correction)},
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
		// The job-bound accept is local acceptance and holds execution; the
		// step exits through its delivery assertion. The delivery admission
		// requires qualifying core-owned worktree verification evidence bound
		// after the recorded acceptance, covering the job's Project
		// (CD-0205 D3): seed one green verify run after the acceptance and
		// bind its operation ref as the integration evidence.
		integrationRef := agentSeedIntegrationVerifyRun(t, s, "work-1", fmt.Sprintf("%064x", 40+cycle))
		version = workVersion(t, s, "work-1")
		integration := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "bind_evidence", "fields": map[string]any{"evidence_kind": "verification", "evidence_ref": integrationRef}, "idempotency_key": "verification-integration-" + strconv.FormatInt(cycle, 10)})}, env)
		if integration.Outcome != OutcomeOK {
			t.Fatalf("seed verification integration bind %d: %+v", cycle, integration.Error)
		}
		version = workVersion(t, s, "work-1")
		executionDelivery := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "record_delivery", "fields": map[string]any{"delivery_artifact": "evidence:verification-execution-" + strconv.FormatInt(cycle, 10), "delivery_state": "asserted"}, "idempotency_key": "verification-delivery-execution-" + strconv.FormatInt(cycle, 10)})}, env)
		if executionDelivery.Outcome != OutcomeOK {
			t.Fatalf("seed verification execution delivery %d: %+v", cycle, executionDelivery.Error)
		}
		version = workVersion(t, s, "work-1")
		refineStart := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: json.RawMessage(`{"work_id":"work-1","expected_version":` + strconv.FormatInt(version, 10) + `,"action_id":"start_refine","idempotency_key":"verification-refine-start-` + strconv.FormatInt(cycle, 10) + `"}`)}, env)
		if refineStart.Outcome != OutcomeOK {
			t.Fatalf("seed verification refine start %d: %+v", cycle, refineStart.Error)
		}
		// The refine exit consumes a green verify run bound in the epoch
		// (CD-0192); the seed supplies one and binds its operation ref.
		proofRef := agentSeedRefineProofRun(t, s, "work-1", fmt.Sprintf("%064x", cycle))
		version = workVersion(t, s, "work-1")
		proofBind := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "bind_evidence", "fields": map[string]any{"evidence_kind": "verification", "evidence_ref": proofRef}, "idempotency_key": "verification-proof-" + strconv.FormatInt(cycle, 10)})}, env)
		if proofBind.Outcome != OutcomeOK {
			t.Fatalf("seed verification proof bind %d: %+v", cycle, proofBind.Error)
		}
		for _, deliveryStep := range []string{"refine", "delivery"} {
			version = workVersion(t, s, "work-1")
			delivery := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "record_delivery", "fields": map[string]any{"delivery_artifact": "evidence:verification-" + deliveryStep + "-" + strconv.FormatInt(cycle, 10), "delivery_state": "asserted"}, "idempotency_key": "verification-delivery-" + deliveryStep + "-" + strconv.FormatInt(cycle, 10)})}, env)
			if delivery.Outcome != OutcomeOK {
				t.Fatalf("seed verification delivery %s %d: %+v", deliveryStep, cycle, delivery.Error)
			}
		}
		// Both contract predicates carry a non-ok verdict, so no healthy
		// verdict set resets the correction window and every recorded
		// correction request stays comparable.
		for _, predicate := range []string{"predicate:primary", "predicate:secondary"} {
			runVerificationStoreAction(t, s, "record_verdict", map[string]any{
				"contract_version": 1, "predicate_id": predicate, "verdict_kind": "outcome_mismatch",
				"evaluation_evidence": []string{attemptID}, "incomparable_with_approved": true,
			}, reviewer, nil, "verdict-"+predicate+"-"+strconv.FormatInt(cycle, 10))
		}
		requestPredicates := []string{"predicate:primary", "predicate:secondary"}
		if cycle == 4 {
			requestPredicates = finalPredicates
		}
		runVerificationStoreAction(t, s, "request_correction", map[string]any{
			"diagnosis":     "the delivered subject still misses the approved predicates",
			"strategy":      "repeat the implementation external effect",
			"predicate_ids": requestPredicates,
			"evidence_refs": []string{attemptID},
		}, owner, &operator, "correction-"+strconv.FormatInt(cycle, 10))
		pin, err = store.ReadWorkPin(context.Background(), s, "work-1")
		if err != nil {
			t.Fatal(err)
		}
		if pin.Correction == nil || pin.Correction.Disposition != "verification" || pin.Correction.AttemptCount != cycle || pin.Correction.Escalated != (cycle > 3) {
			t.Fatalf("verification correction after cycle %d = %#v", cycle, pin.Correction)
		}
	}
	// The corrective retry executes a freshly recorded job.
	recordReadyRetryJob(t, s, service, env, "job:verification-correction")
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	if pin.Correction == nil || pin.Correction.Disposition != "verification" || pin.Correction.AttemptCount != 4 || !pin.Correction.Escalated {
		t.Fatalf("escalated verification correction after the fourth request = %#v", pin.Correction)
	}
	return workVersion(t, s, "work-1"), path
}

// applyVerificationWorkerDispatchAndCompletion records the lane dispatch and
// the completed report for one verification attempt before the boundary
// accepts the result.
func applyVerificationWorkerDispatchAndCompletion(t *testing.T, s *store.Store, grant Authority, attemptID, suffix string) {
	t.Helper()
	lane := retryLane(t)
	job := authorizedWorkerJob(t, s, attemptID)
	evidence := make([]store.WorkerReportEvidence, 0, len(lane.EvidenceObligations))
	for _, obligation := range lane.EvidenceObligations {
		evidence = append(evidence, store.WorkerReportEvidence{Obligation: obligation, Detail: "synthetic reported evidence for " + obligation})
	}
	dispatch := store.Event{EventID: suffix + "-dispatch-" + attemptID, Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: retryJSON(store.WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, PacketDigest: "sha256:" + strings.Repeat("c", 64), ReadbackModel: "openai/gpt-5.6-luna", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion, WorkerJob: job})}
	completion := store.Event{EventID: suffix + "-completed-" + attemptID, Kind: store.WorkerCompleted, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: store.WorkerEvidenceEventPayloadVersion(store.WorkerCompleted), Payload: retryJSON(store.WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: "openai/gpt-5.6-luna", ReportSchemaVersion: store.WorkerReportSchemaVersion, WorkerJob: job, EvidenceOrigin: store.WorkerEvidenceReported, Evidence: evidence})}
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
func runVerificationStoreAction(t *testing.T, s *store.Store, action string, fields map[string]any, actor store.WorkflowActor, operator *store.WorkflowActor, suffix string) {
	t.Helper()
	version := workVersion(t, s, "work-1")
	operationID := action + "-verification-" + suffix + "-" + strconv.FormatInt(version, 10)
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		_, err := store.ApplyWorkflowActionTx(context.Background(), tx, store.BuiltinWorkflowRegistry(), store.WorkflowActionExecutionRequest{
			WorkID: "work-1", ExpectedVersion: version, ActionID: action, Payload: raw, Actor: actor, OperatorActor: operator,
			AcceptedInputsDigest: "sha256:" + strings.Repeat("f", 64) + operationID, IdempotencyIdentity: operationID, OperationID: operationID,
			PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID, RequestID: "request:" + operationID,
			ContractDigest: ManifestDigest, Now: fixedTime(),
		})
		return err
	}); err != nil {
		t.Fatalf("seed %s for %s: %v", action, suffix, err)
	}
}
