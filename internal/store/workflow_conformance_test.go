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
	WorkItem    string   `json:"work_item"`
	Actors      []string `json:"actors"`
	Definitions []string `json:"definitions"`
	Evidence    []string `json:"evidence"`
	Relations   []string `json:"relations"`
}
type workflowCorpusFault struct {
	Kind  string         `json:"kind"`
	Input map[string]any `json:"input"`
}
type workflowCorpusRequest struct {
	ActorRef        string                    `json:"actor_ref"`
	DefinitionRef   string                    `json:"definition_ref"`
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
	return decoder.Decode((*currentRequest)(request))
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
	want := []string{"WF01-capture-late-outcome", "WF02-planning-requires-outcome", "WF04-weaker-delivery", "WF05-stronger-delivery", "WF19-completion-one-transaction", "WF37-action-availability-before-register", "WF47-evidence-commit-binding", "WF50-one-conjunct-weaker"}
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
	case "capture", "record_proposal", "approve_contract", "record_verdict", "complete":
	default:
		return workflowObservation{}, fmt.Errorf("action %q is not in the current corpus action union", action)
	}
	if setup.CurrentState == nil || request.DefinitionRef == "" {
		return workflowObservation{}, fmt.Errorf("current fixture requires current state and a current family selector")
	}
	if action != request.ActionID || !slices.Contains(setup.FixtureRefs.Definitions, request.DefinitionRef) {
		return workflowObservation{}, fmt.Errorf("current fixture action or family selector mismatch")
	}
	if len(setup.Faults) != 0 {
		return workflowObservation{}, fmt.Errorf("current fixture declares unsupported faults")
	}
	registered, err := LiveWorkflowDefinitionForRef(request.DefinitionRef)
	if err != nil {
		return workflowObservation{}, err
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
	s, registry, err := prepareWorkflowCorpusState(t, setup, request, registered, actor)
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
	_, actionErr := applyWorkflowActionRawTx(ctx, tx, scope, registry, WorkflowActionExecutionRequest{WorkID: workID, ExpectedVersion: request.ExpectedVersion, ActionID: action, Payload: payload, Actor: actor, AcceptedInputsDigest: request.Idempotency.AcceptedInputsDigest, IdempotencyIdentity: request.Idempotency.Key, OperationID: request.Operation.OpID, PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: request.Idempotency.Key, RequestID: request.Idempotency.RequestID, AcceptedScope: `{"project":"project"}`, ContractDigest: testManifestDigest, Now: time.Now().UTC()})
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

func prepareWorkflowCorpusState(t *testing.T, setup workflowCorpusSetup, request workflowCorpusRequest, registered RegisteredDefinition, actor WorkflowActor) (*Store, DefinitionRegistry, error) {
	t.Helper()
	ctx := t.Context()
	workID, proof := setup.FixtureRefs.WorkItem, setup.CurrentState.ProofCase
	actions := map[string]string{"planning_without_outcome": "approve_contract", "weaker_delivery": "complete", "satisfied_delivery": "complete", "completion_rollback": "complete", "missing_definition": "record_proposal", "evidence_commit_mismatch": "complete", "one_conjunct_weaker": "complete"}
	if proof != "" {
		if action, ok := actions[proof]; !ok || action != request.ActionID {
			return nil, nil, fmt.Errorf("current fixture proof case %q does not prepare action %q", proof, request.ActionID)
		}
	} else if !slices.Contains([]string{"capture", "record_proposal"}, request.ActionID) {
		return nil, nil, fmt.Errorf("current fixture action %q requires a proof case", request.ActionID)
	}
	registry := BuiltinWorkflowRegistry()
	var s *Store
	var owner WorkflowActor
	switch proof {
	case "", "planning_without_outcome", "missing_definition":
		s = openTemp(t)
		if err := seedCorpusProduct(ctx, s); err != nil {
			return nil, nil, err
		}
		if err := createCorpusCurrentWorkflow(ctx, s, workID, registered, actor, request.Fields); err != nil {
			return nil, nil, err
		}
		if proof == "planning_without_outcome" {
			seedWorkflowLaw(t, s)
			seedIssue31DomainRegistry(t, s)
			version := readWorkVersion(t, s, workID)
			for _, action := range []string{"record_proposal", "record_alignment"} {
				version = issue31WorkflowAction(t, s, workID, version, action, "corpus:"+action, actor)
			}
		}
		if proof == "missing_definition" {
			registry = NewWorkflowDefinitionRegistry()
		}
	case "satisfied_delivery", "completion_rollback", "evidence_commit_mismatch":
		f := qCurrentAcceptance(t, workID, "review")
		s, owner = f.store, f.owner
		if err := f.run(ctx, "confirm_premise", "corpus-confirm", readWorkVersion(t, s, workID), nil, nil); err != nil {
			return nil, nil, err
		}
		bindCompletionClosure(t, s, workID, owner)
		if proof == "completion_rollback" {
			if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER corpus_completion_conflict BEFORE INSERT ON effect_receipts WHEN NEW.kind='workflow.completed' BEGIN SELECT RAISE(ABORT,'injected completion append conflict'); END`); err != nil {
				return nil, nil, err
			}
		}
	case "weaker_delivery", "one_conjunct_weaker":
		predicates := []map[string]any{}
		if proof == "one_conjunct_weaker" {
			predicates = []map[string]any{
				{"predicate_id": "predicate:present", "ordinal": 0, "outcome_kind": "exists", "outcome_payload": map[string]any{"kind": "exists", "surface": "surface:one", "subjects": []string{"subject:one"}}},
				{"predicate_id": "predicate:absent", "ordinal": 1, "outcome_kind": "absent", "outcome_payload": map[string]any{"kind": "absent", "surface": "surface:two", "subjects": []string{"subject:two"}, "distinguish_from": []string{"archived"}}},
			}
		}
		route := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, registered, "execution", []string{"verification", "review"}, []string{"verification", "review", "artifact"}, predicates...)
		j := pReturnClaim(t, route, workID)
		s, owner = route.store, route.owner
		run := j.observeRun(t, "corpus-proof")
		if proof == "one_conjunct_weaker" {
			j.bindVerification(t, run)
		}
		completeCurrentReviewForTest(t, j.fixture, "ready_for_final_proof", func(report *ReviewBlock) {
			for _, criterion := range report.Dependencies.Criteria {
				assessment := ReviewAssessment{PredicateID: criterion.PredicateID, Assessment: "mismatch", ReceiptRefs: []string{}}
				if proof == "one_conjunct_weaker" && criterion.PredicateID == "predicate:present" {
					assessment.Assessment, assessment.ReceiptRefs = "supported", []string{run.OperationRef}
				}
				report.Assessments = append(report.Assessments, assessment)
			}
		})
		if err := advanceWorkflowTestInstanceToStep(ctx, s, workID, "acceptance", mustActorRef(t, route.owner)); err != nil {
			return nil, nil, err
		}
		if proof == "one_conjunct_weaker" {
			if err := j.action(t, "record_verdict", criterionAssociationPayload(run.OperationRef, "predicate:present"), route.owner); err != nil {
				return nil, nil, err
			}
		}
		seedPremiseDeliverablesInvestigation(t, s, workID)
		if proof == "weaker_delivery" || proof == "one_conjunct_weaker" {
			// Exercise the ordered completion gate, not acceptance's earlier
			// premise-deliverables refusal. Neither proof nor confirmation is seeded.
			if err := advanceWorkflowTestInstanceToStep(ctx, s, workID, "release", mustActorRef(t, route.owner)); err != nil {
				return nil, nil, err
			}
			bindCompletionClosure(t, s, workID, owner)
		}
	default:
		return nil, nil, fmt.Errorf("current fixture has unknown proof case %q", proof)
	}
	if owner != (WorkflowActor{}) && actor != owner {
		return nil, nil, fmt.Errorf("current fixture actor does not match its prepared production actor")
	}
	var step, family string
	if err := s.db.QueryRowContext(ctx, `SELECT current_step,definition_ref FROM workflow_instances WHERE work_id=?`, workID).Scan(&step, &family); err != nil {
		return nil, nil, err
	}
	if step != setup.CurrentState.Step || family != registered.Definition.Ref {
		return nil, nil, fmt.Errorf("current fixture prepared %s/%s, want %s/%s", family, step, registered.Definition.Ref, setup.CurrentState.Step)
	}
	return s, registry, nil
}

func workflowCorpusContractBytes(ctx context.Context, s *Store, workID string) ([]byte, error) {
	projection, err := ReadWorkflowProjection(ctx, s, WorkflowReadRequest{WorkID: workID})
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
		{"planning_without_outcome", "planning", "approve_contract", "invalid_input"},
		{"weaker_delivery", "release", "complete", "missing_evidence"},
		{"satisfied_delivery", "release", "complete", ""},
		{"completion_rollback", "release", "complete", "operation_conflict"},
		{"missing_definition", "proposal", "record_proposal", "invariant_violation"},
		{"evidence_commit_mismatch", "release", "complete", "missing_evidence"},
		{"one_conjunct_weaker", "release", "complete", "missing_evidence"},
	} {
		t.Run(tc.proof, func(t *testing.T) {
			actor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/work-alpha", ActorClass: ActorAgent}
			ref := mustActorRef(t, actor)
			setup := workflowCorpusSetup{FixtureRefs: workflowCorpusFixtureRefs{WorkItem: "work-alpha", Actors: []string{ref}, Definitions: []string{"workflow.implementation"}}, CurrentState: &workflowCorpusCurrentState{Step: tc.step, ProofCase: tc.proof}}
			fields := map[string]any{"impact_verdict": "non-breaking"}
			switch tc.action {
			case "approve_contract":
				fields = map[string]any{"premise": "The bounded engineering result.", "decision_domains": []string{}}
			case "record_proposal":
				fields = map[string]any{"problem": "The bounded problem.", "affected": []string{"The fixture."}, "stakes": "The bounded stakes.", "user_outcomes": []string{"The checked result."}}
			}
			if tc.proof == "evidence_commit_mismatch" {
				fields["evidence_commit"], fields["current_commit"] = strings.Repeat("a", 40), strings.Repeat("b", 40)
			}
			request := workflowCorpusRequest{ActorRef: ref, DefinitionRef: "workflow.implementation", ActionID: tc.action, Fields: fields,
				Grant:       workflowCorpusGrant{PrincipalRef: actor.PrincipalRef, ClientRef: actor.ClientRef, AgentRef: actor.AgentRef, SessionRef: actor.SessionRef, Capability: "workflow_action", Scope: "project"},
				Idempotency: workflowCorpusIdempotency{Key: "corpus:test", RequestID: "corpus:request", AcceptedInputsDigest: testManifestDigest}, Operation: workflowCorpusOperation{OpID: "corpus:test"}}
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
				"planning_without_outcome": `workflow action payload field "outcome_predicates" is required for action "approve_contract"`,
				"missing_definition":       "current workflow definition is unavailable: workflow.implementation",
				"evidence_commit_mismatch": "immutable evidence commit does not match the current commit",
				"weaker_delivery":          "workflow verdict is missing for approved predicate predicate:return-route",
				"one_conjunct_weaker":      "workflow verdict is missing for approved predicate predicate:absent",
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

func TestCorpusCurrentRequestRejectsActorScopeAndActionMutations(t *testing.T) {
	t.Parallel()
	for _, mutation := range []string{"actor", "scope", "capability", "setup_actor", "definition", "action", "proof_case"} {
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
			case "definition":
				scenario.Request.DefinitionRef = "workflow.research"
			case "action":
				scenario.Request.ActionID = "approve_contract"
			case "proof_case":
				scenario.Setup.CurrentState.ProofCase = "satisfied_delivery"
			}
			if _, err := executeWorkflowScenario(t, scenario.Action, scenario.Setup, scenario.Request); err == nil {
				t.Fatalf("%s mutation crossed the current fixture boundary", mutation)
			}
		})
	}
}

func TestCorpusCurrentProofInputBindingsRejectMutations(t *testing.T) {
	t.Parallel()
	t.Run("receipt", func(t *testing.T) {
		const workID = "corpus-receipt-binding"
		f := qBuildAcceptance(t, workID, "", false)
		assertRecoveryRefusalWithoutEffect(t, f.store, workID, KindMissingEvidence, func() error {
			return runVerdictActionAs(t, f.store, workID, "record_verdict", criterionAssociationPayload("worktree_verify:unrelated", "predicate:return-route"), 0, f.owner)
		})
		if err := runVerdictActionAs(t, f.store, workID, "record_verdict", qAssociationPayload(t, f), 0, f.owner); err != nil {
			t.Fatalf("captured receipt refused after unrelated receipt control: %v", err)
		}
	})
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
	for _, missing := range []string{"state", "selector"} {
		setup, request := scenario.Setup, scenario.Request
		if missing == "state" {
			setup.CurrentState = nil
		} else {
			request.DefinitionRef = ""
		}
		if _, err := executeWorkflowScenario(t, scenario.Action, setup, request); err == nil || !strings.Contains(err.Error(), "current fixture") {
			t.Fatalf("incomplete current fixture was not refused: %v", err)
		}
	}
}

func TestCorpusCurrentStateChangeObservationUsesDurableRows(t *testing.T) {
	current := readWorkflowScenarioCorpus(t).Scenarios[0]
	for _, tc := range []struct {
		action    string
		fields    map[string]any
		unchanged bool
	}{
		{"record_proposal", map[string]any{"problem": "The bounded problem.", "affected": []string{"The fixture."}, "stakes": "The bounded stakes.", "user_outcomes": []string{"The checked result."}}, false},
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
	seedIssue31DomainRegistry(t, s)
	grant := scenario.Request.Grant
	actor := WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: ActorAgent}
	workID := scenario.Setup.FixtureRefs.WorkItem
	if err := createCorpusCurrentWorkflow(ctx, s, workID, mustBuiltinDefinition(t, scenario.Request.DefinitionRef), actor, scenario.Request.Fields); err != nil {
		t.Fatal(err)
	}
	observation, err := observeWorkflowStore(ctx, s, workID, 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	absent := scenario.Expected.Assertions[1]
	if err := workflowObservationAssertionError(observation, absent); err != nil {
		t.Fatalf("capture fabricated an approved contract: %v", err)
	}
	version := readWorkVersion(t, s, workID)
	for _, action := range []string{"record_proposal", "record_alignment"} {
		version = issue31WorkflowAction(t, s, workID, version, action, workID+":"+action, actor)
	}
	binding := corpusArchitectureBinding()
	var root string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT root_domain_id FROM domain_registries WHERE product_id='product'`).Scan(&root); err != nil {
		t.Fatal(err)
	}
	binding["home_domain_id"], binding["affected_domain_ids"] = root, []string{root}
	payload, err := json.Marshal(map[string]any{"spec_mandate": []string{}, "law_modifies": []string{}, "architecture_binding": binding})
	if err != nil {
		t.Fatal(err)
	}
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
	seedIssue31DomainRegistry(t, s)
	actor := WorkflowActor{PrincipalRef: "principal:corpus", ClientRef: "client:corpus", AgentRef: "agent:corpus-main", SessionRef: "session:corpus-inline", ActorClass: ActorAgent}
	ctx, err := s.EstablishKnowledgeSourceSetProof(ctx, workID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: mustBuiltinDefinition(t, "workflow.implementation"), Actor: actor, Now: corpusNow}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	for _, action := range []string{"record_proposal", "record_alignment"} {
		version = issue31WorkflowAction(t, s, workID, version, action, workID+":"+action, actor)
	}
	payload := testApprovalPayload(t, s, workID, "approve_contract", json.RawMessage(`{"spec_mandate":[],"law_modifies":[],"architecture_binding":{"home_domain_id":"root","affected_domain_ids":["root"],"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[]}}`))
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
	_, actionErr := applyWorkflowActionRawTx(ctx, tx, scope, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{WorkID: workID, ExpectedVersion: version, ActionID: "approve_contract", Payload: payload, Actor: actor, AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64), IdempotencyIdentity: operationID, OperationID: operationID, PrincipalRef: actor.PrincipalRef, Tool: "workflow-corpus", IdempotencyKey: operationID, RequestID: operationID + ":request", AcceptedScope: `{}`, ContractDigest: testManifestDigest, Now: corpusNow})
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

func corpusArchitectureBinding() map[string]any {
	return map[string]any{"home_domain_id": "domain/corpus-main", "affected_domain_ids": []string{"domain/corpus-main"}, "domain_modifies": []string{}, "domain_relation_modifies": []any{}, "law_additions": []any{}, "verification_obligations": []any{}}
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

func createCorpusCurrentWorkflow(ctx context.Context, s *Store, workID string, registered RegisteredDefinition, actor WorkflowActor, fields map[string]any) error {
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
	created := Event{EventID: workID + ":capture", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: actor.PrincipalRef, OccurredAt: corpusNow, PayloadVersion: registration.CurrentVersion, Payload: mustJSONValue(workCreatedPayload{WorkID: workID, WorkKind: "task", Title: title, ValueStatement: value, Priority: &priority, WorkflowTypeRef: registered.Definition.Ref})}
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
		return InitializeWorkflowTx(ctx, transaction, WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: actor, Now: corpusNow})
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
	projection, err := ReadWorkflowProjection(ctx, s, WorkflowReadRequest{WorkID: workID})
	if err != nil {
		return observation, err
	}
	observation.State[workID] = map[string]any{"state": projection.State, "current_step": projection.CurrentStep}
	if projection.Contract != nil {
		work["outcome_contract"] = projection.Contract
		review, err := loadWorkflowReviewState(ctx, s.db, workID)
		if err != nil {
			return observation, err
		}
		assessments := map[string]any{}
		if review.Accepted != nil {
			for _, assessment := range review.Accepted.Report.Assessments {
				assessments[assessment.PredicateID] = assessment.Assessment
			}
		}
		observation.State["review"] = map[string]any{"assessments": assessments}
		verdicts, err := latestWorkflowVerdicts(ctx, s.db, workID, projection.Contract.Version)
		if err != nil {
			return observation, err
		}
		proofs := map[string]any{}
		for _, verdict := range verdicts {
			proofs[verdict.PredicateID] = map[string]any{"kind": verdict.VerdictKind, "actor_ref": verdict.VerdictActorRef, "evidence": verdict.EvaluationEvidence}
		}
		observation.State["verdicts"] = proofs
	}
	terminal, err := ReadWorkflowVerdict(ctx, s, workID)
	if err != nil {
		return observation, err
	}
	var terminalTime, completedAt sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT w.terminal_time,i.completed_at FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, workID).Scan(&terminalTime, &completedAt); err != nil {
		return observation, err
	}
	if terminal != nil || terminalTime.Valid || completedAt.Valid {
		work["terminal_metadata"] = map[string]any{"record": terminal, "terminal_time": terminalTime.String, "completed_at": completedAt.String}
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
