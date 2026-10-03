package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// seedSameStepWallWithoutCorrection drives three failed review-lane attempts
// through the verify checkpoint's dispatch action and completes a fourth
// operator-approved attempt without acceptance, so the same-step wall stands
// armed with no correction record behind it: the fourth dispatch consumed the
// third failure record, and a completed attempt is neither a counted failure
// nor a window reset (CD-0164 D1). It returns the latest counted failed
// attempt's identity and the attempt epoch its dispatch completion recorded.
func seedSameStepWallWithoutCorrection(t *testing.T, s *store.Store, grant Authority, worktree string) (string, int64) {
	t.Helper()
	owner := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	const failedID = "attempt:work-1:verify-3"
	for cycle := 1; cycle <= 3; cycle++ {
		attemptID := fmt.Sprintf("attempt:work-1:verify-%d", cycle)
		runGenericOneOffStoreAction(t, s, "dispatch_worker", map[string]any{
			"attempt_id":    attemptID,
			"worker_packet": verifyWallPacket(t, s, attemptID, nil),
		}, owner, worktree, "dispatch-"+strconv.Itoa(cycle))
		applyVerifyWallDispatchAndFailure(t, s, grant, attemptID)
	}

	// The fourth attempt dispatches behind the operator approval and its
	// worker completes without acceptance.
	fourth := "attempt:work-1:verify-4"
	version := workVersion(t, s, "work-1")
	payload, err := json.Marshal(map[string]any{
		"attempt_id":    fourth,
		"worker_packet": verifyWallPacket(t, s, fourth, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		_, err := store.ApplyWorkflowActionTx(context.Background(), tx, store.BuiltinWorkflowRegistry(), store.WorkflowActionExecutionRequest{
			WorkID: "work-1", ExpectedVersion: version, ActionID: "dispatch_worker", Payload: payload,
			Actor: owner, SessionWorktree: worktree, EscalatedRetryApproved: true,
			AcceptedInputsDigest: "sha256:" + strings.Repeat("e", 64), IdempotencyIdentity: "same-step-completed-4", OperationID: "same-step-completed-4",
			PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "same-step-completed-4", RequestID: "request:same-step-completed-4",
			ContractDigest: ManifestDigest, Now: fixedTime(),
		})
		return err
	}); err != nil {
		t.Fatalf("approved fourth dispatch: %v", err)
	}
	recordGenericOneOffWorkerDispatch(t, s, grant, fourth, "same-step-completed")
	// The fourth attempt rides the review lane, so its live completion carries
	// the typed review block (CD-0197).
	completion := store.Event{EventID: "same-step-completed-" + fourth, Kind: store.WorkerCompleted, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 3, Payload: retryJSON(store.WorkerCompletedPayload{AttemptID: fourth, ReadbackModel: "openai/gpt-5.6-luna", ReportSchemaVersion: store.WorkerReportSchemaVersion, EvidenceOrigin: store.WorkerEvidenceLegacyUnavailable, Review: &store.WorkerReviewBlock{Verdict: "ship", Findings: []store.WorkerReviewFinding{{Severity: "P3", Confidence: "high", Detail: "the bounded change matches the approved contract"}}}})}
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		_, err := store.ApplyOperationTx(context.Background(), tx, store.Operation{Events: []store.Event{completion}})
		return err
	}); err != nil {
		t.Fatalf("record completed attempt %s: %v", fourth, err)
	}

	// The wall still holds three counted failures, and no correction record
	// stands behind them.
	count, err := store.WorkflowFailedWorkerRetryBinding(context.Background(), s, nil, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	if count == nil {
		t.Fatal("the same-step wall holds three counted failures but no retry binding exists")
	}
	if count.FailedAttemptID != failedID || count.FailedAttemptEpoch <= 0 {
		t.Fatalf("retry binding = %+v, want the latest counted failed attempt %s", count, failedID)
	}
	return failedID, count.FailedAttemptEpoch
}

// TestSameStepWallAfterACompletedAttemptMintsTheApprovalChallenge pins the
// same-step wall's escape on the mutation boundary when no correction record
// stands behind it: three counted failed review dispatches, one completed
// attempt that consumed the failure record, then a fresh dispatch on the
// review lane. The wall refuses, the boundary mints the approval challenge
// bound to the latest counted failed attempt's identity, epoch, and contract,
// and one signed approval admits exactly that dispatch.
func TestSameStepWallAfterACompletedAttemptMintsTheApprovalChallenge(t *testing.T) {
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
	worktree := seedGenericOneOffVerifyWorkflow(t, s, grant)
	grant.Worktree = worktree
	failedID, failedEpoch := seedSameStepWallWithoutCorrection(t, s, grant, worktree)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)

	// The store-level wall refusal fires on an unapproved dispatch with the
	// exact counted-population text.
	retryID := "attempt:work-1:verify-5"
	unapprovedPayload, err := json.Marshal(map[string]any{
		"attempt_id":    retryID,
		"worker_packet": verifyWallPacket(t, s, retryID, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	unapprovedVersion := workVersion(t, s, "work-1")
	unapprovedErr := s.Transact(context.Background(), func(tx *store.Transaction) error {
		_, err := store.ApplyWorkflowActionTx(context.Background(), tx, store.BuiltinWorkflowRegistry(), store.WorkflowActionExecutionRequest{
			WorkID: "work-1", ExpectedVersion: unapprovedVersion, ActionID: "dispatch_worker", Payload: unapprovedPayload,
			Actor:                store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent},
			SessionWorktree:      worktree,
			AcceptedInputsDigest: "sha256:" + strings.Repeat("f", 64), IdempotencyIdentity: "same-step-unapproved-5", OperationID: "same-step-unapproved-5",
			PrincipalRef: grant.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "same-step-unapproved-5", RequestID: "request:same-step-unapproved-5",
			ContractDigest: ManifestDigest, Now: fixedTime(),
		})
		return err
	})
	var refusal *store.Failure
	if !errors.As(unapprovedErr, &refusal) || refusal.Kind != store.KindApprovalRequired {
		t.Fatalf("unapproved same-step dispatch = %v, want approval_required", unapprovedErr)
	}
	if !strings.Contains(refusal.Detail, "worker dispatch at step verify reached the three-failed-attempt limit: 3 failed attempts dispatched at this step since the last accepted result or step entry") {
		t.Fatalf("same-step refusal does not name the counted population: %q", refusal.Detail)
	}

	// The mutation boundary mints the operator challenge bound to the latest
	// counted failed attempt, its dispatch completion epoch, and the active
	// contract, so the wall's escape stays operator approvable.
	version := workVersion(t, s, "work-1")
	input := map[string]any{
		"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker",
		"idempotency_key": "same-step-challenge-1", "fields": map[string]any{"attempt_id": retryID, "worker_packet": verifyWallPacket(t, s, retryID, nil)},
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	env.RequestID = "request:same-step-challenge-1"
	challenge := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("same-step wall retry without approval = %+v, want approval_required", challenge.Error)
	}
	details := challenge.Error.Details
	challengeRef, _ := details["approval_ref"].(string)
	if len(challengeRef) != 64 {
		t.Fatalf("same-step wall challenge carries no approval reference: %+v", details)
	}
	if got, _ := details["action_id"].(string); got != "dispatch_worker" {
		t.Fatalf("same-step wall challenge action_id = %v", details["action_id"])
	}
	assertBindingContains(t, details["scope"], "failed_attempt_id:"+failedID)
	assertBindingContains(t, details["versions"], "failed_attempt_epoch:"+strconv.FormatInt(failedEpoch, 10))
	assertBindingContains(t, details["versions"], "contract:1")
	assertBindingContains(t, details["versions"], "work:"+strconv.FormatInt(version, 10))

	// The signed approval admits exactly one dispatch intent.
	approvedInput := cloneWithApproval(t, input, challengeRef)
	approvedRaw, err := json.Marshal(approvedInput)
	if err != nil {
		t.Fatal(err)
	}
	scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "failed_attempt_id": failedID, "scope_version": scopeVersion}
	versions := map[string]any{"work": version, "contract": int64(1), "failed_attempt_epoch": failedEpoch}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, mutationDigest("concord_work_transition", "workflow_action", env, approvedRaw), scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), nonceForChallenge(challengeRef))
	env.RequestID = "request:same-step-approved-1"
	approved := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedRaw}, env)
	if approved.Outcome != OutcomeOK {
		t.Fatalf("approved same-step wall retry = %+v", approved.Error)
	}
	var completions int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=?`, store.WorkflowActionCompleted, retryID).Scan(&completions); err != nil {
		t.Fatal(err)
	}
	if completions != 1 {
		t.Fatalf("approved retry recorded %d dispatch completions for %s, want 1", completions, retryID)
	}
}
