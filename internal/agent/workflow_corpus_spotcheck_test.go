package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

type corpusFixtureRefs struct {
	WorkItem string `json:"work_item"`
}
type corpusEvent struct {
	EventID        string         `json:"event_id"`
	Kind           string         `json:"kind"`
	WorkID         string         `json:"work_id"`
	ActorRef       string         `json:"actor_ref"`
	OccurredAt     string         `json:"occurred_at"`
	PayloadVersion int            `json:"payload_version"`
	Payload        map[string]any `json:"payload"`
}
type corpusSetup struct {
	FixtureRefs  corpusFixtureRefs `json:"fixture_refs"`
	EventHistory []corpusEvent     `json:"event_history"`
}
type corpusIdempotency struct {
	Key string `json:"key"`
}
type corpusRequest struct {
	ExpectedVersion int64             `json:"expected_version"`
	ActionID        string            `json:"action_id"`
	Fields          map[string]any    `json:"fields"`
	Idempotency     corpusIdempotency `json:"idempotency"`
}
type corpusAssertion struct {
	Target string `json:"target"`
	Path   string `json:"path"`
	Op     string `json:"op"`
	Value  any    `json:"value"`
}
type corpusExpected struct {
	Assertions []corpusAssertion `json:"assertions"`
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustMarshalInvoke(t *testing.T, request InvokeRequest, env CallEnvelope) []byte {
	t.Helper()
	raw, err := json.Marshal(struct {
		CallEnvelope CallEnvelope    `json:"call_envelope"`
		Tool         string          `json:"tool"`
		Operation    string          `json:"operation"`
		Input        json.RawMessage `json:"input"`
	}{env, request.Tool, request.Operation, request.Input})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func assertWF39Corpus(t *testing.T, response Envelope, assertions []corpusAssertion) {
	t.Helper()
	observation := map[string]any{"communication": map[string]any{"envelope": map[string]any{"outcome": string(response.Outcome)}}}
	if response.Error != nil {
		observation["communication"].(map[string]any)["envelope"].(map[string]any)["error"] = map[string]any{"recovery_action": map[string]any{"kind": response.Error.RecoveryAction.Kind}}
	}
	observation["effects"] = map[string]any{}
	assertAgentCorpus(t, observation, assertions)
}

func assertAgentCorpus(t *testing.T, observation map[string]any, assertions []corpusAssertion) {
	t.Helper()
	for index, assertion := range assertions {
		root, ok := observation[assertion.Target]
		if !ok {
			t.Fatalf("agent corpus assertion %d has unsupported target %q", index, assertion.Target)
		}
		value, present := corpusLookup(root, strings.Split(assertion.Path, "."))
		if assertion.Op == "absent" {
			if present {
				t.Fatalf("agent corpus assertion %d path %s unexpectedly present: %#v", index, assertion.Path, value)
			}
			continue
		}
		if assertion.Op == "nonempty" {
			if !present || value == nil || fmt.Sprint(value) == "" {
				t.Fatalf("agent corpus assertion %d path %s is empty", index, assertion.Path)
			}
			continue
		}
		if assertion.Op != "eq" || !present || fmt.Sprint(value) != fmt.Sprint(assertion.Value) {
			t.Fatalf("agent corpus assertion %d path %s %s %#v, got %#v", index, assertion.Path, assertion.Op, assertion.Value, value)
		}
	}
}

func corpusLookup(value any, parts []string) (any, bool) {
	if len(parts) == 0 {
		return value, true
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	next, ok := object[parts[0]]
	if !ok {
		return nil, false
	}
	return corpusLookup(next, parts[1:])
}

func TestWorkflowCorpusWF39DispatchesThroughAgentWorkflowAction(t *testing.T) {
	corpus := struct {
		Scenarios []struct {
			ID       string         `json:"id"`
			Action   string         `json:"action"`
			Setup    corpusSetup    `json:"setup"`
			Request  corpusRequest  `json:"request"`
			Expected corpusExpected `json:"expected"`
		} `json:"scenarios"`
	}{}
	raw, err := os.ReadFile("../../scenarios/workflow-engine.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	var selected *struct {
		ID       string         `json:"id"`
		Action   string         `json:"action"`
		Setup    corpusSetup    `json:"setup"`
		Request  corpusRequest  `json:"request"`
		Expected corpusExpected `json:"expected"`
	}
	for _, scenario := range corpus.Scenarios {
		if scenario.ID == "WF39-action-error-envelope" {
			copy := scenario
			selected = &struct {
				ID       string         `json:"id"`
				Action   string         `json:"action"`
				Setup    corpusSetup    `json:"setup"`
				Request  corpusRequest  `json:"request"`
				Expected corpusExpected `json:"expected"`
			}{copy.ID, copy.Action, copy.Setup, copy.Request, copy.Expected}
		}
	}
	if selected == nil {
		t.Fatal("WF39 is missing from the corpus")
	}
	if selected.Action != "workflow_action" || selected.Request.ActionID == "" {
		t.Fatalf("WF39 action=%q request action=%q", selected.Action, selected.Request.ActionID)
	}

	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
	workID := selected.Setup.FixtureRefs.WorkItem
	if workID == "" {
		t.Fatal("WF39 setup has no work item")
	}
	seedCorpusWorkflowWork(t, s, grant, workID, selected.Setup.EventHistory)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	input := map[string]any{"work_id": workID, "expected_version": selected.Request.ExpectedVersion, "action_id": selected.Request.ActionID, "fields": selected.Request.Fields, "idempotency_key": selected.Request.Idempotency.Key}
	inputRaw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := InvokeRequest{Tool: "concord_work_transition", Operation: selected.Action, Input: inputRaw}
	outer, err := json.Marshal(map[string]any{"call_envelope": env, "tool": request.Tool, "operation": request.Operation, "input": json.RawMessage(request.Input)})
	if err != nil {
		t.Fatal(err)
	}
	decoded, decodedEnv, err := DecodeInvokeRequest(outer)
	if err != nil {
		t.Fatal(err)
	}
	response, err := Invoke(context.Background(), s, service, mustMarshalInvoke(t, decoded, decodedEnv))
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.RecoveryAction.Kind == "" {
		t.Fatalf("WF39 response=%+v", response)
	}
	assertWF39Corpus(t, response, selected.Expected.Assertions)
	var fallback int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE kind='work.transitioned'`).Scan(&fallback); err != nil {
		t.Fatal(err)
	}
	if fallback != 0 {
		t.Fatalf("WF39 fallback path appended %d lifecycle events", fallback)
	}
}

func seedCorpusWorkflowWork(t *testing.T, s *store.Store, grant Grant, workID string, history []corpusEvent) {
	t.Helper()
	definition := store.BuiltinWorkflowDefinitions()[0]
	registered, err := store.BuiltinWorkflowRegistry().Register(definition)
	if err != nil {
		t.Fatal(err)
	}
	created := history[0]
	occurredAt, err := time.Parse(time.RFC3339Nano, created.OccurredAt)
	if err != nil {
		t.Fatal(err)
	}
	events := []store.Event{
		{EventID: created.EventID, Kind: created.Kind, SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: created.ActorRef, OccurredAt: occurredAt.UTC(), PayloadVersion: created.PayloadVersion, Payload: mustJSON(t, created.Payload)},
		{EventID: workID + "-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, workID): 0}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(context.Background(), tx, store.WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: store.WorkflowActor{PrincipalRef: "principal:operator", ClientRef: "client:concord-1", AgentRef: "agent-engineer", SessionRef: "session-executor", ActorClass: store.ActorAgent}, Now: occurredAt.UTC()})
	}); err != nil {
		t.Fatal(err)
	}
}
