package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestRefusedWorkerProvenanceRecoversWithoutAcceptedEvidence(t *testing.T) {
	for _, defect := range []string{"invalid_digest", "too_many_sources"} {
		t.Run(defect, func(t *testing.T) {
			ctx := context.Background()
			s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
			grant.Worktree = seedWorkerRetryMutationFixture(t, s, grant)
			scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
			if err != nil {
				t.Fatal(err)
			}
			env := mutationEnvelope(grant, scopeVersion)
			const failedID = "attempt:work-1:refused-provenance"
			recordReadyRetryJob(t, s, service, env, "job:authorize-refused-provenance")
			first := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{
				"work_id": "work-1", "expected_version": retryWorkVersion(t, s), "action_id": "dispatch_worker", "idempotency_key": "authorize-refused-provenance",
				"fields": map[string]any{"attempt_id": failedID, "worker_packet": retryMutationPacket(t, s, failedID, nil)},
			})}, env)
			if first.Outcome != OutcomeOK {
				t.Fatalf("authorize worker: %+v", first.Error)
			}
			before, err := store.ReadWorkPin(ctx, s, "work-1")
			if err != nil {
				t.Fatal(err)
			}
			beforeEvents := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM domain_events`)
			digest := "sha256:" + strings.Repeat("a", 64)
			provenance := &store.WorkerHostProvenance{Digest: digest, Sources: []store.WorkerHostProvenanceSource{{Kind: "unenumerated", Path: "synthetic-host-surface"}}}
			if defect == "invalid_digest" {
				provenance.Digest = "sha256:short"
			} else {
				provenance.Sources = nil
				for i := 0; i < 33; i++ {
					provenance.Sources = append(provenance.Sources, store.WorkerHostProvenanceSource{Kind: "instruction_file", Path: fmt.Sprintf("/synthetic/instruction-%02d.md", i), SHA256: digest})
				}
			}
			lane := retryLane(t)
			dispatch := store.Event{EventID: "refused-provenance-dispatch", Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 3, Payload: retryJSON(store.WorkerDispatchedPayload{
				AttemptID: failedID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass,
				PacketDigest: digest, ReadbackModel: "model/synthetic", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion, HostProvenance: provenance,
			})}
			err = s.Transact(ctx, func(tx *store.Transaction) error {
				pair, err := store.PrepareLaneActorDispatch(ctx, tx, dispatch, grant.PrincipalRef, grant.ClientRef)
				if err != nil {
					return err
				}
				_, err = store.AppendLaneActorDispatchTx(ctx, tx, pair)
				return err
			})
			if !strings.Contains(fmt.Sprint(err), "worker host provenance has invalid digest or source bound") {
				t.Fatalf("dispatch refusal=%v, want the provenance boundary", err)
			}
			after, err := store.ReadWorkPin(ctx, s, "work-1")
			if err != nil || after.Version != before.Version || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM domain_events`) != beforeEvents {
				t.Fatalf("refused dispatch changed durable state: before=%d after=%d error=%v", before.Version, after.Version, err)
			}
			attempt, err := s.WorkerAttemptByID(ctx, failedID)
			if err != nil || attempt.LifecycleState != "in_flight" || attempt.ReadbackModel != "" {
				t.Fatalf("refused dispatch admitted readback: attempt=%#v error=%v", attempt, err)
			}
			var evidence []store.WorkerReportEvidence
			for _, obligation := range lane.EvidenceObligations {
				evidence = append(evidence, store.WorkerReportEvidence{Obligation: obligation, Detail: "synthetic worker report"})
			}
			completion := store.Event{EventID: "unaccepted-worker-completion", Kind: store.WorkerCompleted, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: retryJSON(store.WorkerCompletedPayload{AttemptID: failedID, ReadbackModel: "model/synthetic", ReportSchemaVersion: store.WorkerReportSchemaVersion, EvidenceOrigin: store.WorkerEvidenceReported, Evidence: evidence})}
			if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{completion}}); !strings.Contains(fmt.Sprint(err), "worker attempt lifecycle is not admissible for this terminal event") {
				t.Fatalf("completion without admitted dispatch evidence=%v, want the lifecycle fence", err)
			}
			if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM domain_events`) != beforeEvents {
				t.Fatal("refused completion persisted evidence")
			}
			// The host's terminal evidence precedes the typed abandonment receipt.
			failure := store.Event{EventID: "abandon-refused-provenance", Kind: store.WorkerFailed, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: retryJSON(store.WorkerFailedPayload{AttemptID: failedID, FailureKind: store.WorkerFailureAbandoned, Detail: "host provenance refused; worker report remains unaccepted"})}
			if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{failure}}); err != nil {
				t.Fatalf("close authorized attempt: %v", err)
			}
			scopeVersion, _, err = s.ScopeVersion(ctx, "project-1")
			if err != nil {
				t.Fatal(err)
			}
			env = mutationEnvelope(grant, scopeVersion)
			receipt := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "worker_abandon", Input: retryJSON(map[string]any{
				"work_id": "work-1", "attempt_id": failedID, "lane_id": lane.ID, "detail": "host provenance refused", "idempotency_key": "abandon-refused-provenance",
			})}, env)
			if receipt.Outcome != OutcomeOK {
				t.Fatalf("abandonment receipt: %+v", receipt.Error)
			}
			pin, err := store.ReadWorkPin(ctx, s, "work-1")
			if err != nil {
				t.Fatal(err)
			}
			// A distinct coordinator records the failure; recovery does not
			// confer verdict authority on the actor that opened the attempt.
			grant.SessionRef = "session-recovery"
			env = mutationEnvelope(grant, scopeVersion)
			disposition := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{
				"work_id": "work-1", "expected_version": pin.Version, "action_id": "record_worker_failure", "idempotency_key": "record-refused-provenance",
				"fields": map[string]any{"attempt_id": failedID, "attempt_epoch": 1},
			})}, env)
			if disposition.Outcome != OutcomeOK {
				t.Fatalf("failure disposition without dispatch evidence: %+v", disposition.Error)
			}
			attempt, err = s.WorkerAttemptByID(ctx, failedID)
			if err != nil || attempt.LifecycleState != "failed" || attempt.FailureKind != store.WorkerFailureAbandoned || attempt.ReadbackModel != "" {
				t.Fatalf("failure disposition changed the honest closure: attempt=%#v error=%v", attempt, err)
			}
			pin, err = store.ReadWorkPin(ctx, s, "work-1")
			if err != nil {
				t.Fatal(err)
			}
			scopeVersion, _, err = s.ScopeVersion(ctx, "project-1")
			if err != nil {
				t.Fatal(err)
			}
			env = mutationEnvelope(grant, scopeVersion)
			const freshID = "attempt:work-1:provenance-retry"
			retryInput := map[string]any{
				"work_id": "work-1", "expected_version": pin.Version, "action_id": "dispatch_worker", "idempotency_key": "retry-refused-provenance",
				"fields": map[string]any{"attempt_id": freshID, "worker_packet": retryMutationPacket(t, s, freshID, pin.Correction)},
			}
			challenge := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(retryInput)}, env)
			if challenge.Error == nil || challenge.Error.Kind != "approval_required" {
				t.Fatalf("retry without approval: %+v", challenge.Error)
			}
			ref, ok := challenge.Error.Details["approval_ref"].(string)
			if !ok || ref == "" {
				t.Fatal("retry challenge has no approval reference")
			}
			approvedRaw := retryJSON(cloneWithApproval(t, retryInput, ref))
			scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "failed_attempt_id": failedID, "scope_version": scopeVersion}
			versions := map[string]any{"work": pin.Version, "contract": 1, "failed_attempt_epoch": 1}
			env.HostApproval = signedHostApproval(privateKey, ref, mutationDigest("concord_work_transition", "workflow_action", env, approvedRaw), scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), nonceForChallenge(ref))
			approved := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedRaw}, env)
			if approved.Outcome != OutcomeOK {
				t.Fatalf("approved fresh retry: %+v", approved.Error)
			}
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM domain_events WHERE subject_id='work-1' AND kind IN ('worker.dispatched','worker.completed')`); got != 0 {
				t.Fatalf("recovery fabricated %d accepted evidence events", got)
			}
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1'`); got != 2 {
				t.Fatalf("recovery created %d attempts, want the failed and fresh identities", got)
			}
			var epoch int64
			if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' ORDER BY seq DESC LIMIT 1`, store.WorkflowActionStarted).Scan(&epoch); err != nil || epoch != 2 {
				t.Fatalf("fresh retry epoch=%d error=%v, want 2", epoch, err)
			}
		})
	}
}
