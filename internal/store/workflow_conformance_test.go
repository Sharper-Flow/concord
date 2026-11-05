package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

type workflowScenarioCorpus struct {
	Scenarios []workflowScenario `json:"scenarios"`
}

type workflowScenario struct {
	ID           string                     `json:"id"`
	Number       int                        `json:"scenario_number"`
	Action       string                     `json:"action"`
	Setup        workflowCorpusSetup        `json:"setup"`
	Request      workflowCorpusRequest      `json:"request"`
	Observations workflowCorpusObservations `json:"observations"`
	Expected     workflowExpected           `json:"expected"`
	Cases        []workflowScenario         `json:"cases"`
}

type workflowCorpusSetup struct {
	FixtureRefs  workflowCorpusFixtureRefs   `json:"fixture_refs"`
	CurrentState *workflowCorpusCurrentState `json:"current_state"`
	Faults       []workflowCorpusFault       `json:"faults"`
}

func (setup *workflowCorpusSetup) UnmarshalJSON(raw []byte) error {
	type currentSetup workflowCorpusSetup
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode((*currentSetup)(setup))
}

type workflowCorpusCurrentState struct {
	Step      string `json:"step"`
	ProofCase string `json:"proof_case,omitempty"`
}
type workflowCorpusFixtureRefs struct {
	WorkItem      string                 `json:"work_item"`
	Actors        []string               `json:"actors"`
	WorkflowTypes []WorkflowStepPlanType `json:"workflow_types"`
	Evidence      []string               `json:"evidence"`
	Relations     []string               `json:"relations"`
}
type workflowCorpusFault struct {
	Kind  string         `json:"kind"`
	Input map[string]any `json:"input"`
}
type workflowCorpusRequest struct {
	ActorRef        string                    `json:"actor_ref"`
	WorkflowType    *WorkflowStepPlanType     `json:"workflow_type"`
	ExpectedVersion int64                     `json:"expected_version,omitempty"`
	ActionID        string                    `json:"action_id"`
	Fields          map[string]any            `json:"fields"`
	Idempotency     workflowCorpusIdempotency `json:"idempotency"`
	Grant           workflowCorpusGrant       `json:"grant"`
	Operation       workflowCorpusOperation   `json:"operation"`
}

func (request *workflowCorpusRequest) UnmarshalJSON(raw []byte) error {
	type currentRequest workflowCorpusRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode((*currentRequest)(request)); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if _, ok := fields["workflow_type"]; !ok {
		return fmt.Errorf("current fixture requires workflow_type")
	}
	return nil
}

type workflowCorpusIdempotency struct {
	Key                  string `json:"key"`
	RequestID            string `json:"request_id"`
	OperationID          string `json:"operation_id"`
	AcceptedInputsDigest string `json:"accepted_inputs_digest"`
}
type workflowCorpusGrant struct {
	PrincipalRef string `json:"principal_ref"`
	ClientRef    string `json:"client_ref"`
	AgentRef     string `json:"agent_ref"`
	SessionRef   string `json:"session_ref"`
	Capability   string `json:"capability"`
	Scope        string `json:"scope"`
}
type workflowCorpusOperation struct {
	OpID string `json:"op_id"`
}
type workflowCorpusObservations struct {
	ExpectedReads []workflowAssertion `json:"expected_reads"`
}
type workflowExpected struct {
	Assertions []workflowAssertion `json:"assertions"`
	Invariants []string            `json:"invariants"`
}
type workflowAssertion struct {
	Target string `json:"target"`
	Path   string `json:"path"`
	Op     string `json:"op"`
	Value  any    `json:"value"`
}
type workflowObservation struct {
	State         map[string]any
	Result        map[string]any
	Communication map[string]any
	Effects       map[string]any
}

var corpusNow = time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)

func readWorkflowScenarioCorpus(t *testing.T) workflowScenarioCorpus {
	t.Helper()
	raw, err := os.ReadFile("../../.concord/scenarios/workflow-engine.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus workflowScenarioCorpus
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	want := []string{"WF01-capture-late-outcome", "WF05-stronger-delivery", "WF19-completion-one-transaction", "WF37-action-availability-before-register"}
	if len(corpus.Scenarios) != len(want) {
		t.Fatalf("scenario count=%d, want %d", len(corpus.Scenarios), len(want))
	}
	for i, scenario := range corpus.Scenarios {
		if scenario.ID != want[i] || !strings.HasPrefix(scenario.ID, fmt.Sprintf("WF%02d-", scenario.Number)) {
			t.Fatalf("scenario %d = %s/%d, want %s", i, scenario.ID, scenario.Number, want[i])
		}
	}
	return corpus
}

func TestWorkflowScenarioCorpusExecutesExactProductionActions(t *testing.T) {
	t.Parallel()
	for _, scenario := range readWorkflowScenarioCorpus(t).Scenarios {
		t.Run(scenario.ID, func(t *testing.T) {
			if len(scenario.Cases) == 0 {
				runWorkflowScenarioCase(t, scenario)
				return
			}
			for _, c := range scenario.Cases {
				t.Run(c.ID, func(t *testing.T) { runWorkflowScenarioCase(t, c) })
			}
		})
	}
}

func runWorkflowScenarioCase(t *testing.T, scenario workflowScenario) {
	t.Helper()
	observation, err := executeWorkflowScenario(t, scenario.Action, scenario.Setup, scenario.Request)
	if err != nil {
		t.Fatal(err)
	}
	for _, assertion := range append(scenario.Expected.Assertions, scenario.Observations.ExpectedReads...) {
		if err := workflowObservationAssertionError(observation, assertion); err != nil {
			t.Fatal(err)
		}
	}
	if len(scenario.Expected.Invariants) != 0 {
		t.Fatal("unregistered executable invariants")
	}
}

func executeWorkflowScenario(t *testing.T, action string, setup workflowCorpusSetup, request workflowCorpusRequest) (workflowObservation, error) {
	t.Helper()
	switch action {
	case "capture", "approve_contract", "confirm_premise", "complete":
	default:
		return workflowObservation{}, fmt.Errorf("action %q is not in the current corpus action union", action)
	}
	if setup.CurrentState == nil {
		return workflowObservation{}, fmt.Errorf("current fixture requires current state")
	}
	if action != request.ActionID || (request.WorkflowType == nil && len(setup.FixtureRefs.WorkflowTypes) != 0) || (request.WorkflowType != nil && (!slices.Contains(WorkflowStepPlanTypes, *request.WorkflowType) || !slices.Contains(setup.FixtureRefs.WorkflowTypes, *request.WorkflowType))) {
		return workflowObservation{}, fmt.Errorf("current fixture action or workflow type mismatch")
	}
	if request.Idempotency.Key == "" || request.Idempotency.RequestID == "" || request.Idempotency.AcceptedInputsDigest == "" || request.Operation.OpID == "" || request.Idempotency.OperationID != request.Operation.OpID {
		return workflowObservation{}, fmt.Errorf("current fixture idempotency identity mismatch")
	}
	if len(setup.Faults) != 0 {
		return workflowObservation{}, fmt.Errorf("current fixture declares unsupported faults")
	}
	workID := setup.FixtureRefs.WorkItem
	if workID == "" {
		return workflowObservation{}, fmt.Errorf("current fixture has no work reference")
	}
	grant := request.Grant
	actor := WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: ActorAgent}
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		return workflowObservation{}, err
	}
	if actorRef != request.ActorRef {
		return workflowObservation{}, fmt.Errorf("current fixture actor does not match authenticated actor")
	}
	if !slices.Contains(setup.FixtureRefs.Actors, actorRef) {
		return workflowObservation{}, fmt.Errorf("current fixture actor is outside setup scope")
	}
	if grant.Scope != "project" || grant.Capability != "workflow_action" {
		return workflowObservation{}, fmt.Errorf("current fixture grant does not authorize this scope and capability")
	}
	ctx := context.Background()
	s, err := prepareWorkflowCorpusState(t, setup, request, actor)
	if err != nil {
		return workflowObservation{}, err
	}
	if action == "capture" {
		return observeWorkflowStore(ctx, s, workID, 0, nil, nil)
	}
	if request.ExpectedVersion == 0 {
		request.ExpectedVersion, err = workflowCurrentVersion(ctx, s, workID)
		if err != nil {
			return workflowObservation{}, err
		}
	}
	beforeSeq, err := workflowEventSequence(ctx, s, workID)
	if err != nil {
		return workflowObservation{}, err
	}
	beforeState := contractCorrectionEffectSnapshot(t, s, workID)
	beforeContract, err := workflowCorpusContractBytes(ctx, s, workID)
	if err != nil {
		return workflowObservation{}, err
	}
	payload, err := json.Marshal(request.Fields)
	if err != nil {
		return workflowObservation{}, err
	}
	ctx, err = prepareTerminalTestContext(ctx, s, workID, action)
	if err != nil {
		return workflowObservation{}, err
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		return workflowObservation{}, err
	}
	scope, err := beginWrite(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		return workflowObservation{}, err
	}
	_, actionErr := applyWorkflowActionRawTx(ctx, tx, scope, WorkflowActionExecutionRequest{WorkID: workID, ExpectedVersion: request.ExpectedVersion, ActionID: action, Payload: payload, Actor: actor, AcceptedInputsDigest: request.Idempotency.AcceptedInputsDigest, IdempotencyIdentity: request.Idempotency.Key, OperationID: request.Operation.OpID, PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: request.Idempotency.Key, RequestID: request.Idempotency.RequestID, AcceptedScope: `{"project":"project"}`, ContractDigest: testManifestDigest, Now: time.Now().UTC()})
	_ = scope.close(ctx)
	if actionErr != nil {
		_ = tx.Rollback()
	} else if err := tx.Commit(); err != nil {
		return workflowObservation{}, err
	}
	afterContract, err := workflowCorpusContractBytes(ctx, s, workID)
	if err != nil {
		return workflowObservation{}, err
	}
	return observeWorkflowStore(ctx, s, workID, beforeSeq, actionErr, map[string]any{"state_unchanged": contractCorrectionEffectSnapshot(t, s, workID) == beforeState, "contract_unchanged": bytes.Equal(beforeContract, afterContract)})
}

func prepareWorkflowCorpusState(t *testing.T, setup workflowCorpusSetup, request workflowCorpusRequest, actor WorkflowActor) (*Store, error) {
	t.Helper()
	ctx := t.Context()
	workID, proof := setup.FixtureRefs.WorkItem, setup.CurrentState.ProofCase
	actions := map[string]string{"satisfied_delivery": "confirm_premise", "completion_rollback": "complete", "unpicked_plan": "confirm_premise"}
	if proof != "" {
		if action, ok := actions[proof]; !ok || action != request.ActionID {
			return nil, fmt.Errorf("current fixture proof case %q does not prepare action %q", proof, request.ActionID)
		}
	} else if request.ActionID != "capture" {
		return nil, fmt.Errorf("current fixture action %q requires a proof case", request.ActionID)
	}
	var s *Store
	var owner WorkflowActor
	switch proof {
	case "", "unpicked_plan":
		s = openTemp(t)
		if err := seedCorpusProduct(ctx, s); err != nil {
			return nil, err
		}
		if err := createCorpusCurrentWorkflow(ctx, s, workID, actor, request.Fields); err != nil {
			return nil, err
		}
	case "completion_rollback":
		if request.WorkflowType == nil {
			return nil, fmt.Errorf("current fixture requires a picked workflow type")
		}
		f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, *request.WorkflowType, "plan", []string{})
		s, owner = f.store, f.owner
		completeWorkflowPlanFixture(t, s, workID)
		version := readWorkVersion(t, s, workID)
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{workTransitionEvent(workID+":start", workID, "needed", "in_progress", version, version+1)}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
			return nil, err
		}
		if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER corpus_completion_conflict BEFORE INSERT ON effect_receipts WHEN NEW.kind='workflow.completed' BEGIN SELECT RAISE(ABORT,'injected completion append conflict'); END`); err != nil {
			return nil, err
		}
	case "satisfied_delivery":
		f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "review", nil)
		s, owner = f.store, f.owner
	default:
		return nil, fmt.Errorf("current fixture has unknown proof case %q", proof)
	}
	if owner != (WorkflowActor{}) && actor != owner {
		return nil, fmt.Errorf("current fixture actor does not match its prepared production actor")
	}
	projection, err := readTestWorkflowFacts(ctx, s, workID)
	if err != nil {
		return nil, err
	}
	if projection.CurrentStep != setup.CurrentState.Step || (request.WorkflowType == nil && projection.Plan != nil) || (request.WorkflowType != nil && (projection.Plan == nil || projection.Plan.Type != *request.WorkflowType)) {
		return nil, fmt.Errorf("current fixture prepared plan %v step %q, want type %v step %q", projection.Plan, projection.CurrentStep, request.WorkflowType, setup.CurrentState.Step)
	}
	return s, nil
}

func workflowCorpusContractBytes(ctx context.Context, s *Store, workID string) ([]byte, error) {
	projection, err := readTestWorkflowFacts(ctx, s, workID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(projection.Contract)
}

func TestCorpusCurrentProofPreparationProfiles(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		proof, step, action, kind string
	}{
		{"satisfied_delivery", "review", "confirm_premise", ""},
		{"completion_rollback", "", "complete", "operation_conflict"},
		{"unpicked_plan", "intake", "confirm_premise", "invalid_transition"},
	} {
		t.Run(tc.proof, func(t *testing.T) {
			actor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/work-alpha", ActorClass: ActorAgent}
			ref := mustActorRef(t, actor)
			planType := WorkflowStepPlanChange
			setup := workflowCorpusSetup{FixtureRefs: workflowCorpusFixtureRefs{WorkItem: "work-alpha", Actors: []string{ref}, WorkflowTypes: []WorkflowStepPlanType{planType}}, CurrentState: &workflowCorpusCurrentState{Step: tc.step, ProofCase: tc.proof}}
			fields := map[string]any{"impact_verdict": "non-breaking"}
			switch tc.action {
			case "approve_contract":
				fields = map[string]any{"premise": "The bounded engineering result.", "decision_domains": []string{}}
			case "confirm_premise":
				fields = map[string]any{}
			}
			request := workflowCorpusRequest{ActorRef: ref, WorkflowType: &planType, ActionID: tc.action, Fields: fields,
				Grant:       workflowCorpusGrant{PrincipalRef: actor.PrincipalRef, ClientRef: actor.ClientRef, AgentRef: actor.AgentRef, SessionRef: actor.SessionRef, Capability: "workflow_action", Scope: "project"},
				Idempotency: workflowCorpusIdempotency{Key: "corpus:test", RequestID: "corpus:request", OperationID: "corpus:test", AcceptedInputsDigest: testManifestDigest}, Operation: workflowCorpusOperation{OpID: "corpus:test"}}
			if tc.proof == "unpicked_plan" {
				request.WorkflowType = nil
				setup.FixtureRefs.WorkflowTypes = nil
			}
			observation, err := executeWorkflowScenario(t, tc.action, setup, request)
			if err != nil {
				t.Fatal(err)
			}
			if tc.kind == "" {
				if observation.Communication["outcome"] != "ok" {
					t.Fatalf("production action refused: %+v", observation.Communication)
				}
			} else if err := workflowObservationAssertionError(observation, workflowAssertion{Target: "communication", Path: "error.kind", Op: "eq", Value: tc.kind}); err != nil {
				t.Fatalf("%v; communication=%+v", err, observation.Communication)
			}
			detail := map[string]string{
				"unpicked_plan": "action is not declared on a frontier step",
			}[tc.proof]
			if detail != "" {
				if err := workflowObservationAssertionError(observation, workflowAssertion{Target: "communication", Path: "error.detail", Op: "eq", Value: detail}); err != nil {
					t.Fatal(err)
				}
			}
			clause := 0
			if tc.proof == "completion_rollback" {
				clause = 7
				if err := workflowObservationAssertionError(observation, workflowAssertion{Target: "communication", Path: "error.detail", Op: "eq", Value: "completion append conflicted"}); err != nil {
					t.Fatal(err)
				}
			}
			if clause != 0 {
				if err := workflowObservationAssertionError(observation, workflowAssertion{Target: "communication", Path: "error.clause", Op: "eq", Value: float64(clause)}); err != nil {
					t.Fatalf("%v; communication=%+v", err, observation.Communication)
				}
			}
			t.Logf("communication=%v work=%v result=%v", observation.Communication, observation.State["work"], observation.Result)
		})
	}
}

func TestWorkflowScenarioAssertionInterpreterRejectsMutatedExpectation(t *testing.T) {
	t.Parallel()
	observation := workflowObservation{Communication: map[string]any{"error": map[string]any{"kind": "invalid_transition"}}, Effects: map[string]any{}}
	assertion := workflowAssertion{Target: "communication", Path: "error.kind", Op: "eq", Value: "invalid_transition"}
	if err := workflowObservationAssertionError(observation, assertion); err != nil {
		t.Fatal(err)
	}
	assertion.Value = "invariant_violation"
	if err := workflowObservationAssertionError(observation, assertion); err == nil {
		t.Fatal("mutated expected code was accepted")
	}
	if err := workflowObservationAssertionError(observation, workflowAssertion{Target: "effects", Path: "workflow.compeleted", Op: "absent"}); err == nil {
		t.Fatal("an unknown event kind passed an absence assertion")
	}
}

func TestWorkflowScenarioCorpusMutationsRerunAgainstAuthoritativeState(t *testing.T) {
	t.Parallel()
	corpus := readWorkflowScenarioCorpus(t)
	var cases []workflowScenario
	for _, scenario := range corpus.Scenarios {
		if len(scenario.Cases) == 0 {
			cases = append(cases, scenario)
		} else {
			cases = append(cases, scenario.Cases...)
		}
	}
	for _, scenario := range cases {
		t.Run(scenario.ID, func(t *testing.T) {
			observation, err := executeWorkflowScenario(t, scenario.Action, scenario.Setup, scenario.Request)
			if err != nil {
				t.Fatal(err)
			}
			for _, original := range append(scenario.Expected.Assertions, scenario.Observations.ExpectedReads...) {
				if err := workflowObservationAssertionError(observation, original); err != nil {
					t.Fatal(err)
				}
				mutated := original
				mutated.Op, mutated.Value = "eq", "not-the-authoritative-value"
				if err := workflowObservationAssertionError(observation, mutated); err == nil {
					t.Fatalf("mutated assertion was accepted: %+v", mutated)
				}
			}
		})
	}
}

func TestWorkflowScenarioCorpusUnavailableActionPrecedesAuthorization(t *testing.T) {
	t.Parallel()
	scenario := readWorkflowScenarioCorpus(t).Scenarios[3]
	grant := scenario.Request.Grant
	actor := WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: ActorAgent}
	s, err := prepareWorkflowCorpusState(t, scenario.Setup, scenario.Request, actor)
	if err != nil {
		t.Fatal(err)
	}
	workID := scenario.Setup.FixtureRefs.WorkItem
	version := readWorkVersion(t, s, workID)
	request := WorkflowActionPreflightRequest{WorkID: workID, ExpectedVersion: version, ActionID: scenario.Action, Payload: json.RawMessage(`{}`), Actor: actor}
	before := contractCorrectionEffectSnapshot(t, s, workID)
	var actorsBefore, actorsAfter int
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_actors`).Scan(&actorsBefore); err != nil {
		t.Fatal(err)
	}
	_, preflight := WorkflowActionDefinitionFor(t.Context(), s, request)
	if !hasFailureKind(preflight, KindIllegalLifecycleTransition) {
		t.Fatalf("unpicked plan preflight: %v", preflight)
	}
	authorized, mutated := false, false
	err = AuthorizeWorkflowActionAtBoundaryTx(t.Context(), s, request, func(*Transaction) error {
		authorized = true
		return nil
	}, func(*Transaction) error {
		mutated = true
		return nil
	})
	if !hasFailureKind(err, KindIllegalLifecycleTransition) || authorized || mutated {
		t.Fatalf("unpicked plan boundary err=%v authorized=%v mutated=%v", err, authorized, mutated)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_actors`).Scan(&actorsAfter); err != nil {
		t.Fatal(err)
	}
	if actorsBefore != actorsAfter || contractCorrectionEffectSnapshot(t, s, workID) != before {
		t.Fatal("unavailable action changed actor registration or durable state")
	}
}

func TestCorpusCurrentRequestRejectsActorScopeAndActionMutations(t *testing.T) {
	t.Parallel()
	for _, mutation := range []string{"actor", "scope", "capability", "setup_actor", "workflow_type", "action", "proof_case", "operation_id"} {
		t.Run(mutation, func(t *testing.T) {
			scenario := readWorkflowScenarioCorpus(t).Scenarios[0]
			switch mutation {
			case "actor":
				scenario.Request.Grant.AgentRef = "agent:other"
			case "scope":
				scenario.Request.Grant.Scope = "project:other"
			case "capability":
				scenario.Request.Grant.Capability = "workflow_read"
			case "setup_actor":
				scenario.Setup.FixtureRefs.Actors = []string{"actor:other"}
			case "workflow_type":
				planType := WorkflowStepPlanResearch
				scenario.Request.WorkflowType = &planType
			case "action":
				scenario.Request.ActionID = "approve_contract"
			case "proof_case":
				scenario.Setup.CurrentState.ProofCase = "satisfied_delivery"
			case "operation_id":
				scenario.Request.Idempotency.OperationID = "different-operation"
			}
			if _, err := executeWorkflowScenario(t, scenario.Action, scenario.Setup, scenario.Request); err == nil {
				t.Fatalf("%s mutation crossed the current fixture boundary", mutation)
			}
		})
	}
}

func TestCorpusCurrentSetupRefusesHistoricalInputs(t *testing.T) {
	t.Parallel()
	scenario := readWorkflowScenarioCorpus(t).Scenarios[0]
	for _, tc := range []struct {
		name   string
		target any
		raw    string
	}{
		{"history", &workflowCorpusSetup{}, `{"event_history":[{"kind":"work.created"}]}`},
		{"pin", &workflowCorpusRequest{}, `{"definition_pin":{"ref":"workflow.implementation","version":1}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := json.Unmarshal([]byte(tc.raw), tc.target); err == nil {
				t.Fatal("historical input accepted")
			}
		})
	}
	setup := scenario.Setup
	setup.CurrentState = nil
	if _, err := executeWorkflowScenario(t, scenario.Action, setup, scenario.Request); err == nil || !strings.Contains(err.Error(), "current fixture") {
		t.Fatalf("incomplete current fixture was not refused: %v", err)
	}
	if err := json.Unmarshal([]byte(`{"actor_ref":"actor:test"}`), &workflowCorpusRequest{}); err == nil {
		t.Fatal("missing workflow_type selector accepted")
	}
}

func TestCorpusCurrentStateChangeObservationUsesDurableRows(t *testing.T) {
	current := readWorkflowScenarioCorpus(t).Scenarios[1]
	for _, tc := range []struct {
		action    string
		fields    map[string]any
		unchanged bool
	}{
		{"confirm_premise", map[string]any{}, false},
	} {
		t.Run(tc.action, func(t *testing.T) {
			request := current.Request
			request.ActionID, request.Fields = tc.action, tc.fields
			observation, err := executeWorkflowScenario(t, tc.action, current.Setup, request)
			if err != nil {
				t.Fatal(err)
			}
			if got, present := observation.Result["state_unchanged"]; !present || got != tc.unchanged {
				t.Fatalf("durable no-effect observation=%v present=%v, want %v; communication=%+v", got, present, tc.unchanged, observation.Communication)
			}
			if !tc.unchanged && observation.Communication["outcome"] != "ok" {
				t.Fatalf("the changing control must execute: %+v", observation.Communication)
			}
		})
	}
}

func TestCorpusCaptureObservationReadsApprovedContract(t *testing.T) {
	t.Parallel()
	scenario := readWorkflowScenarioCorpus(t).Scenarios[0]
	ctx := context.Background()
	s := openTemp(t)
	if err := seedCorpusProduct(ctx, s); err != nil {
		t.Fatal(err)
	}
	seedWorkflowLaw(t, s)
	grant := scenario.Request.Grant
	actor := WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: ActorAgent}
	workID := scenario.Setup.FixtureRefs.WorkItem
	if err := createCorpusCurrentWorkflow(ctx, s, workID, actor, scenario.Request.Fields); err != nil {
		t.Fatal(err)
	}
	observation, err := observeWorkflowStore(ctx, s, workID, 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	absent := workflowAssertion{Target: "state", Path: "work.contract", Op: "absent"}
	if err := workflowObservationAssertionError(observation, absent); err != nil {
		t.Fatalf("capture fabricated an approved contract: %v", err)
	}
	seedWorkflowPlanFrontier(t, s, workID, WorkflowStepPlanChange, "plan")
	version := readWorkVersion(t, s, workID)
	payload := corpusApprovalPayload(t, s, workID)
	issue31WorkflowActionWithPayload(t, s, workID, version, "approve_contract", workID+":approve", actor, payload)
	observation, err = observeWorkflowStore(ctx, s, workID, 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := workflowObservationAssertionError(observation, absent); err == nil {
		t.Fatal("capture absence assertion hides the actually approved contract")
	}
}

func TestWorkflowInlineTransactionRollbackLeavesNoSemanticOrActionEvents(t *testing.T) {
	t.Parallel()
	const workID = "corpus-inline-current"
	const operationID = "corpus-inline-approve"
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	actor := WorkflowActor{PrincipalRef: "principal:corpus", ClientRef: "client:corpus", AgentRef: "agent:corpus-main", SessionRef: "session:corpus-inline", ActorClass: ActorAgent}
	ctx, err := s.EstablishKnowledgeSourceSetProof(ctx, workID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Actor: actor, Now: corpusNow}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	seedWorkflowPlanFrontier(t, s, workID, WorkflowStepPlanChange, "plan")
	version := readWorkVersion(t, s, workID)
	payload := corpusApprovalPayload(t, s, workID)
	before := contractCorrectionEffectSnapshot(t, s, workID)
	var beforeContracts int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=?`, workID).Scan(&beforeContracts); err != nil {
		t.Fatal(err)
	}
	tx, err = s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	scope, err := beginWrite(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	_, actionErr := applyWorkflowActionRawTx(ctx, tx, scope, WorkflowActionExecutionRequest{WorkID: workID, ExpectedVersion: version, ActionID: "approve_contract", Payload: payload, Actor: actor, AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64), IdempotencyIdentity: operationID, OperationID: operationID, PrincipalRef: actor.PrincipalRef, Tool: "workflow-corpus", IdempotencyKey: operationID, RequestID: operationID + ":request", AcceptedScope: `{}`, ContractDigest: testManifestDigest, Now: corpusNow})
	_ = scope.close(ctx)
	if actionErr != nil {
		t.Fatalf("production action failed before rollback fault: %v", actionErr)
	}
	var pendingContracts int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_contracts WHERE work_id=?`, workID).Scan(&pendingContracts); err != nil {
		t.Fatal(err)
	}
	if pendingContracts != beforeContracts+1 {
		t.Fatalf("approval wrote %d contracts, want %d", pendingContracts, beforeContracts+1)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if after := contractCorrectionEffectSnapshot(t, s, workID); after != before {
		t.Fatal("rollback leaked durable state")
	}
	var afterContracts int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=?`, workID).Scan(&afterContracts); err != nil {
		t.Fatal(err)
	}
	if afterContracts != beforeContracts {
		t.Fatalf("rollback leaked contract rows: before=%d after=%d", beforeContracts, afterContracts)
	}
}

func corpusApprovalPayload(t *testing.T, s *Store, workID string) json.RawMessage {
	t.Helper()
	return testApprovalPayload(t, s, workID, "approve_contract", mustJSONValue(map[string]any{
		"contract_version": 1, "premise": "deliver the checked change", "legislative_subjects": []string{}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{}, "rigor_class": "prototype_internal",
	}))
}

func workflowCurrentVersion(ctx context.Context, s *Store, workID string) (int64, error) {
	var version int64
	err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&version)
	return version, err
}

func workflowEventSequence(ctx context.Context, s *Store, workID string) (int64, error) {
	var seq int64
	err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT COALESCE(MAX(effect_seq),0) FROM effect_receipts WHERE subject_type=? AND subject_id=?`, SubjectWorkItem, workID).Scan(&seq)
	return seq, err
}

func seedCorpusProduct(ctx context.Context, s *Store) error {
	return ApplyOperation(ctx, s, Operation{Events: []Event{
		operationEvent("workflow-corpus-product", "product.created", SubjectProduct, "product", productCreatedPayload{DisplayName: "Workflow corpus Product", StageMaturity: "prototype", StageAudienceCommitment: "operator_only"}),
		operationEvent("workflow-corpus-project", "project.created", SubjectProject, "project", projectCreatedPayload{DisplayName: "Workflow corpus Project"}),
		operationEvent("workflow-corpus-product-project", "product_project.added", SubjectProduct, "product", map[string]any{"product_id": "product", "project_id": "project", "role": "primary", "reason": "workflow corpus", "expected_version": 1, "resulting_version": 2}),
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product"): 0, VersionRef(SubjectProject, "project"): 0}})
}

func createCorpusCurrentWorkflow(ctx context.Context, s *Store, workID string, actor WorkflowActor, fields map[string]any) error {
	registration, ok := registeredEventKind("work.created")
	if !ok {
		return fmt.Errorf("current capture registration is unavailable")
	}
	title, _ := fields["title"].(string)
	if title == "" {
		title = workID
	}
	value, _ := fields["value_statement"].(string)
	projectIDs := []string{"project"}
	if ids, present := fields["project_ids"]; present {
		encoded, err := json.Marshal(ids)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(encoded, &projectIDs); err != nil {
			return err
		}
	}
	priority := int64(1)
	created := Event{EventID: workID + ":capture", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: actor.PrincipalRef, OccurredAt: corpusNow, PayloadVersion: registration.CurrentVersion, Payload: mustJSONValue(workCreatedPayload{WorkID: workID, WorkKind: "task", Title: title, ValueStatement: value, Priority: &priority})}
	if len(projectIDs) > 0 {
		var err error
		ctx, err = s.EstablishKnowledgeSourceSetProofForProject(ctx, projectIDs[0])
		if err != nil {
			return err
		}
	}
	return s.Transact(ctx, func(transaction *Transaction) error {
		events := []Event{created}
		for i, projectID := range projectIDs {
			role := "secondary"
			if i == 0 {
				role = "primary"
			}
			events = append(events, operationEvent(fmt.Sprintf("%s:membership:%d", workID, i), "work_project.added", SubjectWorkItem, workID, map[string]any{"work_id": workID, "project_id": projectID, "role": role, "reason": "workflow corpus", "expected_version": i + 1, "resulting_version": i + 2}))
		}
		if _, err := ApplyOperationTx(ctx, transaction, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 0}}); err != nil {
			return err
		}
		return InitializeWorkflowTx(ctx, transaction, WorkflowInitializationRequest{WorkID: workID, Actor: actor, Now: corpusNow})
	})
}

// Observations read current rows and the bounded writes of this request. They
// never reconstruct a fixture or a projection from stored history.
func observeWorkflowStore(ctx context.Context, s *Store, workID string, beforeSeq int64, actionErr error, result map[string]any) (workflowObservation, error) {
	observation := workflowObservation{State: map[string]any{}, Result: result, Communication: map[string]any{}, Effects: map[string]any{}}
	if actionErr != nil {
		var failure *Failure
		if failureAs(actionErr, &failure) {
			kind := string(failure.Kind)
			switch failure.Kind {
			case KindInvalidPayload, KindInvalidOperation:
				kind = "invalid_input"
			case KindIllegalLifecycleTransition:
				kind = string(KindInvalidTransition)
			}
			observation.Communication["error"] = map[string]any{"kind": kind, "detail": failure.Detail, "clause": float64(failure.Clause)}
		} else {
			observation.Communication["error"] = map[string]any{"kind": actionErr.Error()}
		}
	} else {
		observation.Communication["outcome"] = "ok"
	}
	var lifecycle string
	var version int64
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT lifecycle,version FROM work_items WHERE id=?`, workID).Scan(&lifecycle, &version); err != nil {
		return observation, err
	}
	work := map[string]any{"lifecycle": lifecycle, "version": version}
	observation.State["work"] = work
	projection, err := readTestWorkflowFacts(ctx, s, workID)
	if err != nil {
		return observation, err
	}
	observation.State[workID] = map[string]any{"state": projection.State, "current_step": projection.CurrentStep}
	if projection.Contract != nil {
		work["contract"] = projection.Contract
	}
	var terminalTime, completedAt sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT w.terminal_time,i.completed_at FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, workID).Scan(&terminalTime, &completedAt); err != nil {
		return observation, err
	}
	if terminalTime.Valid || completedAt.Valid {
		work["terminal_metadata"] = map[string]any{"terminal_time": terminalTime.String, "completed_at": completedAt.String}
	}
	rows, err := s.DatabaseForTesting().QueryContext(ctx, `SELECT kind FROM effect_receipts WHERE subject_type=? AND subject_id=? AND effect_seq>? ORDER BY effect_seq`, SubjectWorkItem, workID, beforeSeq)
	if err != nil {
		return observation, err
	}
	defer rows.Close()
	order := []any{}
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			return observation, err
		}
		observation.Effects[kind] = true
		if kind == "work.created" {
			observation.Effects["work.created_event"] = true
		}
		order = append(order, kind)
	}
	observation.Effects["events"] = map[string]any{"order": order}
	return observation, rows.Err()
}

func workflowObservationAssertionError(observation workflowObservation, assertion workflowAssertion) error {
	if assertion.Target == "effects" && assertion.Path != "work.created_event" && !strings.HasPrefix(assertion.Path, "events.") {
		if _, registered := registeredEventKind(assertion.Path); !registered {
			return fmt.Errorf("unsupported event effect %q", assertion.Path)
		}
	}
	targets := map[string]any{"state": observation.State, "result": observation.Result, "communication": observation.Communication, "effects": observation.Effects}
	target, known := targets[assertion.Target]
	if !known {
		return fmt.Errorf("unsupported target %q", assertion.Target)
	}
	got, present := workflowLookup(target, assertion.Path)
	if assertion.Op == "absent" {
		if present {
			return fmt.Errorf("path %s present with %#v", assertion.Path, got)
		}
		return nil
	}
	if !present {
		return fmt.Errorf("path %s is absent", assertion.Path)
	}
	var pass bool
	switch assertion.Op {
	case "eq":
		pass = reflect.DeepEqual(got, assertion.Value)
	case "nonempty":
		pass = got != nil && fmt.Sprint(got) != "" && fmt.Sprint(got) != "[]" && fmt.Sprint(got) != "map[]"
	default:
		return fmt.Errorf("unsupported operation %q", assertion.Op)
	}
	if !pass {
		return fmt.Errorf("%s.%s %s %#v, got %#v", assertion.Target, assertion.Path, assertion.Op, assertion.Value, got)
	}
	return nil
}

func workflowLookup(current any, path string) (any, bool) {
	parts := strings.Split(path, ".")
	for i, part := range parts {
		if object, ok := current.(map[string]any); ok {
			if value, exists := object[strings.Join(parts[i:], ".")]; exists {
				return value, true
			}
			value, exists := object[part]
			if !exists {
				return nil, false
			}
			current = value
		} else if values, ok := current.([]any); ok {
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(values) {
				return nil, false
			}
			current = values[index]
		} else {
			return nil, false
		}
	}
	return current, true
}
