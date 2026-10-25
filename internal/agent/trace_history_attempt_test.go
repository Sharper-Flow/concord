package agent

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestTraceHistoryInvalidEventKindNamesAdmittedKinds(t *testing.T) {
	s, service, grant, _ := agentJobsPM1Fixture(t)
	response := dispatchRead(t, s, service, InvokeRequest{
		Tool: "concord_work_trace", Operation: "history",
		Input: json.RawMessage(`{"work_id":"work-done","event_kinds":["worker.unknown"]}`),
	}, agentJobsEnvelope(grant, "proj-web", "prod-alpha"))
	if response.Outcome != OutcomeError || response.Error == nil {
		t.Fatalf("unknown event kind did not refuse: outcome=%s error=%+v", response.Outcome, response.Error)
	}
	if !strings.Contains(response.Error.Message, "event kind") || string(response.Error.EffectState) != "none" {
		t.Fatalf("unexpected agent refusal: %+v", response.Error)
	}
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("agent refusal does not marshal: %v", err)
	}
	kinds, ok := response.Error.Details["admitted_event_kinds"].([]string)
	if !ok || len(kinds) == 0 || !sort.StringsAreSorted(kinds) {
		t.Fatalf("agent refusal lacks the sorted typed kind list: %+v", response.Error.Details)
	}
	for _, kind := range []string{"worker.dispatched", "worker.completed", "worker.failed"} {
		if !strings.Contains(string(raw), kind) {
			t.Errorf("agent refusal omits admitted kind %q: %s", kind, raw)
		}
	}
}

func TestHistoryAdmittedKindsDetailsBounds(t *testing.T) {
	maximum := make([]string, 128)
	for i := range maximum {
		maximum[i] = "event." + strconv.Itoa(i)
	}
	for _, tc := range []struct {
		name    string
		details map[string]any
		valid   bool
	}{
		{name: "declared maximum", details: map[string]any{"admitted_event_kinds": maximum}, valid: true},
		{name: "over declared maximum", details: map[string]any{"admitted_event_kinds": append(append([]string{}, maximum...), "event.extra")}},
		{name: "empty", details: map[string]any{"admitted_event_kinds": []string{}}},
		{name: "duplicate", details: map[string]any{"admitted_event_kinds": []string{"work.created", "work.created"}}},
		{name: "empty kind", details: map[string]any{"admitted_event_kinds": []string{""}}},
		{name: "long kind", details: map[string]any{"admitted_event_kinds": []string{strings.Repeat("a", 257)}}},
		{name: "wrong type", details: map[string]any{"admitted_event_kinds": "work.created"}},
		{name: "generic array bound unchanged", details: map[string]any{"other": maximum[:21]}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envelope := coreError(NewBase("history-details", "concord_work_trace", "history"), "invalid_input", "invalid filter", "reread_entities", false)
			envelope.Error.Details = tc.details
			_, err := json.Marshal(envelope)
			if (err == nil) != tc.valid {
				t.Fatalf("error details validity = %v, want %v: %v", err == nil, tc.valid, err)
			}
		})
	}
}

func TestTraceHistoryWorkerEventsExposeAttemptIDs(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch", "product_read"})
	version, attemptID, worktree := seedFailedWorkerRetryMutation(t, s, service, grant)
	grant.Worktree = worktree
	fixtureClaimSubject(t, worktree)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil || pin.Correction == nil {
		t.Fatalf("read failed attempt's correction: %v", err)
	}
	const freshID = "attempt:work-1:history-fresh"
	input := map[string]any{
		"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker", "idempotency_key": "history-fresh-dispatch",
		"fields": map[string]any{"attempt_id": freshID, "worker_packet": retryMutationPacket(t, s, freshID, pin.Correction)},
	}
	// A failed attempt's retry needs no operator consent (CD-0242 D6).
	approved := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(input)}, env)
	if approved.Outcome != OutcomeOK {
		t.Fatalf("fresh retry authorization refused: %+v", approved.Error)
	}
	var digest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.worker_packet_digest') FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.worker_attempt_id')=? ORDER BY seq DESC LIMIT 1`, store.WorkflowActionCompleted, freshID).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	lane := retryLane(t)
	job := authorizedWorkerJob(t, s, freshID)
	if job == nil {
		t.Fatal("fresh retry authorization has no worker job binding")
	}
	dispatch := store.Event{EventID: "history-fresh-dispatch", Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion(store.WorkerDispatched), Payload: retryJSON(store.WorkerDispatchedPayload{
		AttemptID: freshID, LaneID: lane.ID, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass,
		PacketDigest: digest, ReadbackModel: "openai/gpt-5.6-luna", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion, WorkerJob: job,
	})}
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		prepared, err := store.PrepareLaneActorDispatch(context.Background(), tx, dispatch, grant.PrincipalRef, grant.ClientRef)
		if err != nil {
			return err
		}
		_, err = store.AppendLaneActorDispatchTx(context.Background(), tx, prepared)
		return err
	}); err != nil {
		t.Fatalf("fresh worker lane dispatch: %v", err)
	}
	agentWorkerCompletedThroughSeam(t, s, freshID, "history-fresh-completed", store.WorkerCompletedPayload{
		AttemptID: freshID, ReadbackModel: "openai/gpt-5.6-luna", ReportSchemaVersion: store.WorkerReportSchemaVersion, WorkerJob: job, WorkerDirectory: worktree,
	})
	response := dispatchRead(t, s, service, InvokeRequest{
		Tool: "concord_work_trace", Operation: "history",
		Input: json.RawMessage(`{"work_id":"work-1","event_kinds":["worker.dispatched","worker.completed","worker.failed","work.created"],"limit":10}`),
	}, mutationEnvelope(grant, scopeVersion))
	if response.Outcome != OutcomeOK || response.Error != nil {
		t.Fatalf("admitted event-kind filter refused: outcome=%s error=%+v", response.Outcome, response.Error)
	}
	if _, err := json.Marshal(response); err != nil {
		t.Fatalf("agent history does not marshal: %v", err)
	}
	var page struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal(response.Result, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 5 {
		t.Fatalf("history returned %d events, want both attempts' worker events and work.created: %s", len(page.Events), response.Result)
	}
	want := map[string]string{"retry-dispatch": attemptID, "retry-failed": attemptID, "history-fresh-dispatch": freshID, "history-fresh-completed": freshID}
	for _, event := range page.Events {
		id, _ := event["event_id"].(string)
		if expected, worker := want[id]; worker {
			if event["attempt_id"] != expected {
				t.Errorf("agent history event %s attempt_id = %v, want %q", id, event["attempt_id"], expected)
			}
			delete(want, id)
		} else if _, present := event["attempt_id"]; present {
			t.Errorf("non-worker history event %s acquired an attempt identity: %+v", id, event)
		}
	}
	if len(want) != 0 {
		t.Fatalf("history omitted worker events: %+v", want)
	}
}
