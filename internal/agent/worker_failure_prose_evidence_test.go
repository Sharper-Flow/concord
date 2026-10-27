package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// TestDerivedWorkerFailureCorrectionKeepsStorableEvidenceRefs carries the
// record_worker_failure evidence boundary forward onto the derived route:
// the live terminal worker.failed ingest derives exactly one workflow
// failure record whose correction keeps the original prose failure detail
// and only storable reference evidence. The retired callable action's
// envelope carried prose locators the pin projection once mangled; the
// derivation carries the original detail verbatim and mints its own
// born-bound reference, so every correction evidence ref stays a storable
// reference by construction.
func TestDerivedWorkerFailureCorrectionKeepsStorableEvidenceRefs(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition", "worker_dispatch"})
	grant.Worktree = seedWorkerRetryMutationFixture(t, s, service, grant)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	// The canonical head derives the workflow failure record from the
	// attempt's own terminal event (CD-0198 D5); the dispatch that opens the
	// fenced window is the qualifying public authorization.
	recordReadyRetryJob(t, s, service, env, "job:prose-evidence")
	attemptID := "attempt:work-1:prose-evidence"
	version := workVersion(t, s, "work-1")
	dispatch := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{
		"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker", "idempotency_key": "prose-evidence-dispatch",
		"fields": map[string]any{"attempt_id": attemptID, "worker_packet": retryMutationPacket(t, s, attemptID, nil)},
	})}, env)
	if dispatch.Outcome != OutcomeOK {
		t.Fatalf("seed derived-failure dispatch: %+v", dispatch.Error)
	}
	detail := "report envelope failed the closed schema: readback_model carried two slashes where the pattern admits one"
	// The worker session's own lane dispatch precedes its terminal fact: the
	// attempt lifecycle folds the dispatch evidence before the terminal
	// event is admissible.
	lane := retryLane(t)
	job := authorizedWorkerJob(t, s, attemptID)
	laneDispatch := store.Event{EventID: "prose-dispatch", Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: retryJSON(store.WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, PacketDigest: "sha256:" + strings.Repeat("b", 64), ReadbackModel: "commandcode/z-ai/glm-5.3-flash", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion, WorkerJob: job})}
	// The invalid-report terminal carries the recorded host observation the
	// promoted definition requires; the host boundary observed the rejected
	// report with an unknown cause, and the store derives the identity.
	failure := agentWorkerFailedAttributedEvent("prose-failed", store.WorkerFailedPayload{AttemptID: attemptID, ReadbackModel: "commandcode/z-ai/glm-5.3-flash", FailureKind: store.WorkerFailureInvalidReport, Detail: detail})
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		enriched, err := store.PrepareLaneActorDispatch(context.Background(), tx, laneDispatch, grant.PrincipalRef, grant.ClientRef)
		if err != nil {
			return err
		}
		if _, err := store.AppendLaneActorDispatchTx(context.Background(), tx, enriched); err != nil {
			return err
		}
		// The live worker-evidence seam is the route the worker-fail CLI
		// owns: the terminal fact and the workflow record it owes commit in
		// one transaction, exactly once.
		_, seamErr := store.ApplyWorkerEvidenceOperationTx(context.Background(), tx, failure)
		return seamErr
	}); err != nil {
		t.Fatalf("live terminal worker-failed ingest: %v", err)
	}
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	if pin.Correction == nil {
		t.Fatal("correction missing after the derived failure record")
	}
	if pin.Correction.Diagnosis != detail {
		t.Fatalf("derived correction diagnosis %q, want the original failure detail verbatim", pin.Correction.Diagnosis)
	}
	for _, ref := range pin.Correction.EvidenceRefs {
		if len(ref) > 128 || strings.ContainsAny(ref, " \t\r\n") {
			t.Fatalf("correction carries an unstorable evidence ref %q", ref)
		}
	}
	var recorded int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_attempt_dispositions WHERE work_id='work-1' AND action_id='record_worker_failure'`).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 1 {
		t.Fatalf("derived failure records = %d, want exactly one", recorded)
	}
}
