package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// TestSameStepWallWithoutCorrectionRecordStaysConvergenceGated pins the
// amended CD-0148 same-step wall when no correction record stands behind it:
// three counted failed review dispatches with no recorded disposition. The
// wall refuses the next dispatch with missing_evidence and mints no approval
// challenge; a supplied operator approval has no effect; a superseded
// contract derives the approach_changed basis and admits one fresh dispatch
// without any challenge; the interrupted dispatch consumes the basis, so the
// blind re-dispatch refuses until a fresh supersession re-opens the wall.
func TestSameStepWallWithoutCorrectionRecordStaysConvergenceGated(t *testing.T) {
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
	worktree := seedGenericOneOffVerifyWorkflow(t, s, grant)
	grant.Worktree = worktree
	seedGenericOneOffVerifyWall(t, s, grant, worktree)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	owner := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}

	// The armed wall keeps no approval binding: nothing on the boundary
	// can mint a retry challenge for it.
	binding, err := store.WorkflowFailedWorkerRetryBinding(context.Background(), s, nil, "work-1")
	if err != nil || binding != nil {
		t.Fatalf("retry approval binding behind the same-step wall = %+v err=%v, want none", binding, err)
	}
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	if pin.Correction != nil {
		t.Fatalf("correction record behind the bare same-step wall = %+v, want none", pin.Correction)
	}
	reason := ""
	for _, intent := range pin.NextValidIntents {
		if intent.ActionID == "dispatch_worker" {
			reason = intent.ReasonCode
		}
	}
	if reason != "escalated_retry_requires_convergence" {
		t.Fatalf("pin dispatch reason behind the bare wall = %q, want escalated_retry_requires_convergence", reason)
	}

	// The wall refuses a fresh dispatch with the counted-population
	// refusal, and the refusal is never an operator approval ask.
	version := workVersion(t, s, "work-1")
	retryID := "attempt:work-1:verify-4"
	input, raw := verifyWallDispatchInput(t, s, retryID, "same-step-convergence-1", nil)
	env.RequestID = "request:same-step-convergence-1"
	refused := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if refused.Error == nil || refused.Error.Kind != "missing_evidence" {
		t.Fatalf("same-step retry without a basis = %+v, want missing_evidence", refused.Error)
	}
	if !strings.Contains(refused.Error.Message, "worker correction reached the three-attempt limit without a convergence basis: 3 non-progress attempts since the last accepted productive result (step verify)") {
		t.Fatalf("same-step refusal does not name the counted population: %q", refused.Error.Message)
	}
	if refused.Error.RecoveryAction.Kind != "provide_evidence" {
		t.Fatalf("same-step refusal recovery = %q, want provide_evidence and never an approval ask", refused.Error.RecoveryAction.Kind)
	}
	if refused.Error.ConsequenceSummary != nil {
		t.Fatal("same-step refusal carries a consequence summary, but no challenge was minted")
	}
	if ref, ok := refused.Error.Details["approval_ref"].(string); ok && ref != "" {
		t.Fatalf("same-step refusal carries approval_ref %q, but no challenge was minted", ref)
	}
	if got := verifyWallChallenges(t, s); got != 0 {
		t.Fatalf("same-step refusal minted %d approval challenges, want 0", got)
	}
	if got := verifyWallDispatchCompletions(t, s, retryID); got != 0 {
		t.Fatalf("refused same-step retry recorded %d dispatch completions, want 0", got)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1' AND attempt_id='`+retryID+`'`); got != 0 {
		t.Fatalf("refused same-step retry created %d worker attempt rows, want 0", got)
	}
	if after := workVersion(t, s, "work-1"); after != version {
		t.Fatalf("refused same-step retry changed the work version %d -> %d", version, after)
	}

	// A supplied operator approval neither opens the wall nor mints a
	// challenge: no basis exists, and none can be approved into existence.
	approvedInput := cloneWithApproval(t, input, strings.Repeat("d", 64))
	approvedRaw, err := json.Marshal(approvedInput)
	if err != nil {
		t.Fatal(err)
	}
	scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}
	versions := map[string]any{"work": version, "contract": int64(1)}
	env.HostApproval = signedHostApproval(privateKey, strings.Repeat("d", 64), mutationDigest("concord_work_transition", "workflow_action", env, approvedRaw), scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), "same-step-unused-approval")
	env.RequestID = "request:same-step-convergence-2"
	bypass := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedRaw}, env)
	if bypass.Error == nil || bypass.Error.Kind != "missing_evidence" {
		t.Fatalf("same-step retry behind a supplied approval = %+v, want missing_evidence", bypass.Error)
	}
	if got := verifyWallChallenges(t, s); got != 0 {
		t.Fatalf("supplied approval minted %d approval challenges, want 0", got)
	}
	if after := workVersion(t, s, "work-1"); after != version {
		t.Fatalf("supplied approval changed the work version %d -> %d", version, after)
	}
	env.HostApproval = nil

	// A superseded contract after the latest dispatch is a changed
	// approach: the store derives the basis, the pin flips its reason, and
	// the boundary admits the dispatch with no approval at all.
	supersedeVerifyWallContract(t, s, owner, workVersion(t, s, "work-1"), 2, "the same-step wall demanded a changed approach")
	pin, err = store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	reason = ""
	for _, intent := range pin.NextValidIntents {
		if intent.ActionID == "dispatch_worker" {
			reason = intent.ReasonCode
		}
	}
	if reason != "escalated_retry_convergence_recorded" {
		t.Fatalf("pin dispatch reason behind the derived basis = %q, want escalated_retry_convergence_recorded", reason)
	}
	// No correction record stands, so the converging packet carries none.
	if pin.Correction != nil {
		t.Fatalf("correction record after the supersession = %+v, want none", pin.Correction)
	}
	_, admittedRaw := verifyWallDispatchInput(t, s, retryID, "same-step-convergence-3", nil)
	env.RequestID = "request:same-step-convergence-3"
	admitted := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: admittedRaw}, env)
	if admitted.Outcome != OutcomeOK {
		t.Fatalf("converging same-step retry without any approval = %+v", admitted.Error)
	}
	if got := verifyWallChallenges(t, s); got != 0 {
		t.Fatalf("converging same-step retry minted %d approval challenges, want 0", got)
	}
	if got := verifyWallDispatchCompletions(t, s, retryID); got != 1 {
		t.Fatalf("converging same-step retry recorded %d dispatch completions, want 1", got)
	}
	basis := verifyWallRecordedConvergence(t, s, retryID)
	if basis.Basis != "approach_changed" || basis.SupersededSeq == 0 || basis.PreviousRecordSeq != 0 || basis.LatestRecordSeq != 0 {
		t.Fatalf("recorded convergence = %+v, want the approach_changed basis naming its supersession sequence", basis)
	}

	// The session dies before the host dispatches the worker: the
	// half-materialized dispatch consumed the basis, so the blind
	// re-dispatch refuses without a challenge. The wall re-armed.
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1' AND attempt_id='`+retryID+`' AND lifecycle_state='in_flight'`); got != 1 {
		t.Fatalf("interrupted retry left %d in-flight worker attempt rows, want 1", got)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM domain_events WHERE subject_id='work-1' AND kind='`+string(store.WorkerDispatched)+`' AND json_extract(payload,'$.attempt_id')='`+retryID+`'`); got != 0 {
		t.Fatalf("interrupted retry shows %d lane dispatches, want 0", got)
	}
	version = workVersion(t, s, "work-1")
	secondID := "attempt:work-1:verify-5"
	pin, err = store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	_, blindRaw := verifyWallDispatchInput(t, s, secondID, "same-step-convergence-4", pin.Correction)
	env.RequestID = "request:same-step-convergence-4"
	blind := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: blindRaw}, env)
	if blind.Error == nil || blind.Error.Kind != "missing_evidence" {
		t.Fatalf("blind re-dispatch after the consumed basis = %+v, want missing_evidence", blind.Error)
	}
	if got := verifyWallChallenges(t, s); got != 0 {
		t.Fatalf("blind re-dispatch minted %d approval challenges, want 0", got)
	}
	if got := verifyWallDispatchCompletions(t, s, secondID); got != 0 {
		t.Fatalf("blind re-dispatch recorded %d dispatch completions, want 0", got)
	}
	if after := workVersion(t, s, "work-1"); after != version {
		t.Fatalf("blind re-dispatch changed the work version %d -> %d", version, after)
	}

	// A fresh supersession derives a fresh basis: each basis buys exactly
	// one more fresh fenced attempt, and the whole journey minted no
	// approval challenge.
	supersedeVerifyWallContract(t, s, owner, workVersion(t, s, "work-1"), 3, "the interrupted converging retry needed a renewed approach")
	_, renewedRaw := verifyWallDispatchInput(t, s, secondID, "same-step-convergence-5", nil)
	env.RequestID = "request:same-step-convergence-5"
	renewed := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: renewedRaw}, env)
	if renewed.Outcome != OutcomeOK {
		t.Fatalf("renewed converging same-step retry = %+v", renewed.Error)
	}
	if got := verifyWallChallenges(t, s); got != 0 {
		t.Fatalf("the whole same-step journey minted %d approval challenges, want 0", got)
	}
	renewedBasis := verifyWallRecordedConvergence(t, s, secondID)
	if renewedBasis.Basis != "approach_changed" || renewedBasis.SupersededSeq <= basis.SupersededSeq {
		t.Fatalf("renewed convergence = %+v, want a fresh supersession after seq %d", renewedBasis, basis.SupersededSeq)
	}
}
