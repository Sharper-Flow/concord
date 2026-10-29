package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

func TestWorkflowActionRefusesRetiredInitiativeBeforeNativePlanning(t *testing.T) {
	for _, actionID := range []string{"approve_contract", "record_review"} {
		t.Run(actionID, func(t *testing.T) {
			s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
			version := seedAgentWorkflow(t, s, grant)
			// Model the historical kind in the temporary projection. Capture of
			// a new initiative is independently refused at the append boundary.
			if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
				UPDATE work_items SET kind='initiative' WHERE id='work-1';
				DELETE FROM write_guard`); err != nil {
				t.Fatal(err)
			}
			before := countWorkflowEvents(t, s)
			challengesBefore := countWorkflowApprovalChallenges(t, s)
			probeCalls := 0
			service.ProjectHostProber = func(context.Context, string, string) (store.ResolvedProjectHost, error) {
				probeCalls++
				return probedHost(), nil
			}
			scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
			if err != nil {
				t.Fatal(err)
			}
			env := mutationEnvelope(grant, scopeVersion)
			r := runtime{Store: s, Authority: service, Envelope: env, Tool: "concord_work_transition", Operation: "workflow_action"}
			raw, err := json.Marshal(actionMutationInput{WorkID: "work-1", ExpectedVersion: version, ActionID: actionID, IdempotencyKey: "retired-" + actionID})
			if err != nil {
				t.Fatal(err)
			}
			response, err := r.mutateWorkflowAction(context.Background(), agentcontract.Envelope{}, raw, grant, agentcontract.ContractOperation{})
			if err != nil || response.Error == nil || response.Error.Kind != "invalid_input" || response.Error.RecoveryAction.Kind != "reread_entities" || !strings.Contains(response.Error.Message, "CD-0213") {
				t.Errorf("response=%+v err=%v, want retired-kind refusal", response.Error, err)
			}
			if probeCalls != 0 || countWorkflowEvents(t, s) != before || countWorkflowApprovalChallenges(t, s) != challengesBefore {
				t.Fatalf("retired action reached native planning: probes=%d events=%d->%d challenges=%d->%d", probeCalls, before, countWorkflowEvents(t, s), challengesBefore, countWorkflowApprovalChallenges(t, s))
			}
		})
	}
}

func TestWorkflowActionPreflightRefusesRetiredInitiative(t *testing.T) {
	s, _, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
	version := seedAgentWorkflow(t, s, grant)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		UPDATE work_items SET kind='initiative' WHERE id='work-1';
		DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	before := countWorkflowEvents(t, s)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	for _, actionID := range []string{"approve_contract", "record_review"} {
		t.Run(actionID, func(t *testing.T) {
			raw, err := json.Marshal(actionMutationInput{WorkID: "work-1", ExpectedVersion: version, ActionID: actionID, IdempotencyKey: "retired-preflight-" + actionID})
			if err != nil {
				t.Fatal(err)
			}
			err = preflightWorkflowActionRequestWithRegistry(context.Background(), s, raw, env, grant, store.BuiltinWorkflowRegistry())
			refusal := failureEnvelope(agentcontract.Envelope{}, err)
			if refusal.Error == nil || refusal.Error.Kind != "invalid_input" || refusal.Error.RecoveryAction.Kind != "reread_entities" || !strings.Contains(refusal.Error.Message, "CD-0213") || countWorkflowEvents(t, s) != before {
				t.Fatalf("preflight error=%v events=%d->%d", err, before, countWorkflowEvents(t, s))
			}
		})
	}
}

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
		{EventID: "capture-task-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("project.created"), Payload: json.RawMessage(`{"display_name":"Project"}`)},
		{EventID: "capture-task-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"test","expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0}}); err != nil {
		t.Fatal(err)
	}
	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", []agentcontract.Capability{"work_define", "product_read"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	return s, service, grant, scopeVersion
}

func dispatchCapture(t *testing.T, s *store.Store, service *Service, grant Authority, scopeVersion, requestID, input string) agentcontract.Envelope {
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
	if response.Outcome != agentcontract.OutcomeOK {
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
	if response.Outcome != agentcontract.OutcomeOK {
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
	if response.Outcome != agentcontract.OutcomeOK {
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
	if response.Outcome != agentcontract.OutcomeError || response.Error == nil {
		t.Fatalf("NUL-carrying task capture outcome=%s error=%+v, want typed refusal", response.Outcome, response.Error)
	}
	if !strings.Contains(response.Error.Message, "task") {
		t.Fatalf("refusal message %q does not name the task field", response.Error.Message)
	}
}
