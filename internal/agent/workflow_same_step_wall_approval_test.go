package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestSameStepRetryKeepsContractRecoveryAndLaunchFence(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
	worktree := seedGenericOneOffVerifyWorkflow(t, s, grant)
	grant.Worktree = worktree
	seedGenericOneOffVerifyWall(t, s, grant, worktree)
	seedVerifyWallLawPins(t, s)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	owner := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}

	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	if pin.Correction != nil {
		t.Fatalf("correction after undispositioned failures = %+v, want none", pin.Correction)
	}
	reason := ""
	for _, intent := range pin.NextValidIntents {
		if intent.ActionID == "dispatch_worker" {
			reason = intent.ReasonCode
		}
	}
	if reason == "" || strings.Contains(reason, "convergence") {
		t.Fatalf("ordinary dispatch intent = %q, want launch authority without a budget", reason)
	}
	retryID := "attempt:work-1:verify-4"

	// Pre-pass contract recovery needs no correction record. The refine
	// step itself is not a correction checkpoint on the retained family, so
	// the ordinary recovery the instance actually admits runs from the
	// producer step whose pass never started: the fixture parks the
	// instance at execution through the fold-guarded fixture pattern, where
	// the unstarted pass admits the operator supersession with no
	// correction record and no failed attempt (CD-0133 D1's pre-pass
	// correction admission).
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE workflow_instances SET current_step='execution' WHERE work_id='work-1'; DELETE FROM fold_guard`); err != nil {
		t.Fatalf("park the instance at the producer step: %v", err)
	}
	if pin.Correction != nil {
		t.Fatalf("undispositioned failures left a correction record: %+v", pin.Correction)
	}
	// The successor retains the accepted law pin the fixture projected, so
	// the ordinary stale-law recovery can own the later renewal.
	supersedeVerifyWallContractWithLawPins(t, s, owner, workVersion(t, s, "work-1"), 2, []string{verifyWallLawID}, "the same-step wall demanded a changed approach")
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
	if reason == "" || strings.Contains(reason, "convergence") {
		t.Fatalf("dispatch intent after contract recovery = %q", reason)
	}
	// No correction record stands, so the packet carries none.
	if pin.Correction != nil {
		t.Fatalf("correction record after the supersession = %+v, want none", pin.Correction)
	}
	_, admittedRaw := verifyWallDispatchInput(t, s, retryID, "same-step-convergence-3", nil)
	env.RequestID = "request:same-step-convergence-3"
	admitted := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: admittedRaw}, env)
	if admitted.Outcome != OutcomeOK {
		t.Fatalf("same-step retry without any approval = %+v", admitted.Error)
	}
	if got := verifyWallChallenges(t, s); got != 0 {
		t.Fatalf("same-step retry minted %d approval challenges, want 0", got)
	}
	if got := verifyWallDispatchCompletions(t, s, retryID); got != 1 {
		t.Fatalf("same-step retry recorded %d dispatch completions, want 1", got)
	}
	assertDispatchHasNoRetryBudget(t, s, retryID)

	// An unknown launch outcome blocks a second authorization.
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1' AND attempt_id='`+retryID+`' AND lifecycle_state='in_flight'`); got != 1 {
		t.Fatalf("interrupted retry left %d in-flight worker attempt rows, want 1", got)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM domain_events WHERE subject_id='work-1' AND kind='`+string(store.WorkerDispatched)+`' AND json_extract(payload,'$.attempt_id')='`+retryID+`'`); got != 0 {
		t.Fatalf("interrupted retry shows %d lane dispatches, want 0", got)
	}
	version := workVersion(t, s, "work-1")
	secondID := "attempt:work-1:verify-5"
	pin, err = store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	_, blindRaw := verifyWallDispatchInput(t, s, secondID, "same-step-convergence-4", pin.Correction)
	env.RequestID = "request:same-step-convergence-4"
	blind := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: blindRaw}, env)
	if blind.Error == nil || blind.Error.Kind != "operation_conflict" || !strings.Contains(blind.Error.Message, "unresolved launch outcome") {
		t.Fatalf("blind re-dispatch = %+v, want unresolved launch conflict", blind.Error)
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

	// Resolve the launch through its terminal fact before stale-law recovery.
	applyVerifyWallDispatchAndFailure(t, s, grant, retryID)
	staleVerifyWallLawProjection(t, s)
	supersedeVerifyWallContractWithLawPins(t, s, owner, workVersion(t, s, "work-1"), 3, []string{verifyWallLawSuccessorID}, "the interrupted converging retry needed a renewed approach")
	_, renewedRaw := verifyWallDispatchInput(t, s, secondID, "same-step-convergence-5", nil)
	env.RequestID = "request:same-step-convergence-5"
	renewed := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: renewedRaw}, env)
	if renewed.Outcome != OutcomeOK {
		t.Fatalf("same-step retry after stale-law recovery = %+v", renewed.Error)
	}
	if got := verifyWallChallenges(t, s); got != 0 {
		t.Fatalf("the whole same-step journey minted %d approval challenges, want 0", got)
	}
	assertDispatchHasNoRetryBudget(t, s, secondID)
}
