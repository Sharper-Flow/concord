package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// TestRecordWorkerFailureAdmitsProseEvidenceLocators reproduces a production
// refusal: a record_worker_failure request carried evidence locators as prose
// (legal on the tool surface at 1 to 2048 bytes), the completion projected
// them into the work pin correction, and the closed mutation-result schema
// rejected the pin because correction evidence_refs must satisfy the
// whitespace-free reference rule. The mutation died with malformed_response
// and no effect. The correction projection now keeps only storable reference
// entries, so the mutation succeeds and the dropped locators stay in the
// durable completion payload.
func TestRecordWorkerFailureAdmitsProseEvidenceLocators(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
	if got := seedAgentWorkflow(t, s, grant); got != 4 {
		t.Fatalf("workflow seed version=%d, want 4", got)
	}
	owner := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	ownerRef, err := store.WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		UPDATE workflow_instances SET current_step='execution' WHERE work_id='work-1';
		INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('work-1',1,'approved retry objective','internal_sqlite','[]','[]','now',?,'[]','[]',0,'prototype_internal');
		DELETE FROM fold_guard`, ownerRef); err != nil {
		t.Fatalf("seed retry projections: %v", err)
	}
	lane := retryLane(t)
	attemptID := "attempt:work-1:prose-evidence"
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	start := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: json.RawMessage(`{"work_id":"work-1","expected_version":4,"action_id":"start_execution","idempotency_key":"prose-start"}`)}, mutationEnvelope(grant, scopeVersion))
	if start.Outcome != OutcomeOK {
		t.Fatalf("seed start: %+v", start.Error)
	}
	dispatch := store.Event{EventID: "prose-dispatch", Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: retryJSON(store.WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, PacketDigest: "sha256:" + strings.Repeat("b", 64), ReadbackModel: "commandcode/z-ai/glm-5.3-flash", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion})}
	failure := store.Event{EventID: "prose-failed", Kind: store.WorkerFailed, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: retryJSON(store.WorkerFailedPayload{AttemptID: attemptID, ReadbackModel: "commandcode/z-ai/glm-5.3-flash", FailureKind: store.WorkerFailureInvalidReport, Detail: "report envelope failed the closed schema"})}
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		enriched, err := store.PrepareLaneActorDispatch(context.Background(), tx, dispatch, grant.PrincipalRef, grant.ClientRef)
		if err != nil {
			return err
		}
		if _, err := store.AppendLaneActorDispatchTx(context.Background(), tx, enriched); err != nil {
			return err
		}
		_, err = store.ApplyOperationTx(context.Background(), tx, store.Operation{Events: []store.Event{failure}})
		return err
	}); err != nil {
		t.Fatalf("seed dispatch and failure: %v", err)
	}
	var failureVersion int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-1'`).Scan(&failureVersion); err != nil {
		t.Fatal(err)
	}
	scopeVersion, _, err = s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	record := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{
		"work_id":         "work-1",
		"expected_version": failureVersion,
		"action_id":       "record_worker_failure",
		"fields":          map[string]any{"attempt_id": attemptID, "attempt_epoch": 1},
		"evidence": []map[string]string{
			{"kind": "durable_note", "locator": "lane report refused at closed agent-lane-report.v1 schema: readback_model carried two slashes where the pattern admits one", "locator_kind": "chat_transcript", "authority": "adapter gate"},
			{"kind": "durable_note", "locator": "git:873d3af04", "locator_kind": "git_commit", "authority": "git"},
		},
		"idempotency_key": "prose-record-failure",
	})}, mutationEnvelope(grant, scopeVersion))
	if record.Outcome != OutcomeOK {
		t.Fatalf("record_worker_failure with prose evidence: %+v", record.Error)
	}
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	if pin.Correction == nil {
		t.Fatal("correction missing after record_worker_failure")
	}
	for _, ref := range pin.Correction.EvidenceRefs {
		if len(ref) > 128 || strings.ContainsAny(ref, " \t\r\n") {
			t.Fatalf("correction carries an unstorable evidence ref %q", ref)
		}
	}
	if len(pin.Correction.EvidenceRefs) != 1 || pin.Correction.EvidenceRefs[0] != "git:873d3af04" {
		t.Fatalf("correction evidence refs=%v, want [git:873d3af04]", pin.Correction.EvidenceRefs)
	}
}
