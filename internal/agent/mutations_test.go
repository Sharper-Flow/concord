package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

// The operator's task instruction is scattered state when capture refuses it:
// the host surface accepts task at capture, so the agent-side payload schema
// is the only refusing gate, and a one-call capture could not carry the
// concrete instruction. These tests prove the gate admits it, the work.created
// fold persists it identically to a revise, and the scope readback projects it
// for the lane packet.

func captureTaskFixture(t *testing.T) (*store.Store, *Service, Authority, string) {
	t.Helper()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	events := []store.Event{
		{EventID: "capture-task-product", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Product","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "capture-task-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Project"}`)},
		{EventID: "capture-task-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"test","expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0}}); err != nil {
		t.Fatal(err)
	}
	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", []Capability{"work_define", "product_read"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	return s, service, grant, scopeVersion
}

func dispatchCapture(t *testing.T, s *store.Store, service *Service, grant Authority, scopeVersion, requestID, input string) Envelope {
	t.Helper()
	env := mutationEnvelope(grant, scopeVersion)
	env.RequestID = requestID
	response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: json.RawMessage(input)}, env)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func scopeWorkReadback(t *testing.T, s *store.Store, service *Service, grant Authority, scopeVersion, workID string) (task, valueStatement string) {
	t.Helper()
	env := mutationEnvelope(grant, scopeVersion)
	env.RequestID = "capture-task-scope-read"
	response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_browse", Operation: "scope", Input: json.RawMessage(`{"product_id":"product-1","work_id":"` + workID + `"}`)}, env)
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != OutcomeOK {
		t.Fatalf("scope read response=%+v", response)
	}
	var readback struct {
		Work struct {
			Task           string `json:"task"`
			ValueStatement string `json:"value_statement"`
		} `json:"work"`
	}
	raw, _ := json.Marshal(response.Result)
	if err := json.Unmarshal(raw, &readback); err != nil {
		t.Fatalf("decode scope readback %s: %v", raw, err)
	}
	return readback.Work.Task, readback.Work.ValueStatement
}

func TestCapturePersistsTaskInstructionInOneCall(t *testing.T) {
	t.Parallel()
	s, service, grant, scopeVersion := captureTaskFixture(t)
	response := dispatchCapture(t, s, service, grant, scopeVersion, "capture-task-request-1",
		`{"title":"Need","value_statement":"Why the work matters","task":"Reproduce the refusal, extract the loop, and keep the budget green.","kind":"task","project_ids":["project-1"],"idempotency_key":"capture-task-idem-1"}`)
	if response.Outcome != OutcomeOK {
		t.Fatalf("capture with task response=%+v", response)
	}
	workID := (*response.ChangedRefs)[0].ID
	task, valueStatement := scopeWorkReadback(t, s, service, grant, scopeVersion, workID)
	if task != "Reproduce the refusal, extract the loop, and keep the budget green." {
		t.Fatalf("scope readback task = %q, want the captured instruction", task)
	}
	if valueStatement != "Why the work matters" {
		t.Fatalf("scope readback value_statement = %q, want the captured value", valueStatement)
	}
}

func TestCaptureWithoutTaskInstructionStaysLegal(t *testing.T) {
	t.Parallel()
	s, service, grant, scopeVersion := captureTaskFixture(t)
	response := dispatchCapture(t, s, service, grant, scopeVersion, "capture-taskless-request-1",
		`{"title":"Need","value_statement":"Why the work matters","kind":"task","project_ids":["project-1"],"idempotency_key":"capture-taskless-idem-1"}`)
	if response.Outcome != OutcomeOK {
		t.Fatalf("capture without task response=%+v", response)
	}
	workID := (*response.ChangedRefs)[0].ID
	task, _ := scopeWorkReadback(t, s, service, grant, scopeVersion, workID)
	if task != "" {
		t.Fatalf("scope readback task = %q, want no persisted task", task)
	}
}

// TestCaptureRefusesTaskTheReviseFoldRefuses proves the identical-persistence
// rule the approved contract promises: a task value the revise fold rejects
// (an escaped NUL) refuses at capture too, so no captured instruction can
// land as a field a later revise would refuse.
func TestCaptureRefusesTaskTheReviseFoldRefuses(t *testing.T) {
	t.Parallel()
	s, service, grant, scopeVersion := captureTaskFixture(t)
	response := dispatchCapture(t, s, service, grant, scopeVersion, "capture-nul-task-request-1",
		`{"title":"Need","value_statement":"Why the work matters","task":"line one\u0000line two","kind":"task","project_ids":["project-1"],"idempotency_key":"capture-nul-task-idem-1"}`)
	if response.Outcome != OutcomeError || response.Error == nil {
		t.Fatalf("NUL-carrying task capture outcome=%s error=%+v, want typed refusal", response.Outcome, response.Error)
	}
	if !strings.Contains(response.Error.Message, "task") {
		t.Fatalf("refusal message %q does not name the task field", response.Error.Message)
	}
}
