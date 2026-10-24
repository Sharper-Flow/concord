package store

import (
	"bytes"
	"context"
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
	Step string `json:"step"`
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
	Approval        json.RawMessage           `json:"approval"`
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
	want := []string{"WF01-capture-late-outcome", "WF26-condition-actions-refused"}
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
	case "capture", "add_condition", "resolve_condition", "cancel_condition", "record_proposal":
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
	if setup.CurrentState.Step != registered.Definition.StepGraph.StartStep {
		return workflowObservation{}, fmt.Errorf("current fixture must start at its declared first step")
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
	s := openTemp(t)
	if err := seedCorpusProduct(ctx, s); err != nil {
		return workflowObservation{}, err
	}
	if action == "capture" {
		err := createCorpusCurrentWorkflow(ctx, s, workID, registered, actor, request.Fields)
		return observeWorkflowStore(ctx, s, workID, 0, err, nil)
	}
	if err := createCorpusCurrentWorkflow(ctx, s, workID, registered, actor, request.Fields); err != nil {
		return workflowObservation{}, err
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
	payload, err := json.Marshal(request.Fields)
	if err != nil {
		return workflowObservation{}, err
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		return workflowObservation{}, err
	}
	scope, err := beginFold(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		return workflowObservation{}, err
	}
	_, actionErr := applyWorkflowActionRawTx(ctx, tx, scope, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{WorkID: workID, ExpectedVersion: request.ExpectedVersion, ActionID: action, Payload: payload, Actor: actor, AcceptedInputsDigest: request.Idempotency.AcceptedInputsDigest, IdempotencyIdentity: request.Idempotency.Key, OperationID: request.Operation.OpID, PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: request.Idempotency.Key, RequestID: request.Idempotency.RequestID, AcceptedScope: `{"project":"project"}`, ContractDigest: testManifestDigest, Now: corpusNow})
	_ = scope.close(ctx)
	if actionErr != nil {
		_ = tx.Rollback()
	} else if err := tx.Commit(); err != nil {
		return workflowObservation{}, err
	}
	return observeWorkflowStore(ctx, s, workID, beforeSeq, actionErr, map[string]any{"state_unchanged": contractCorrectionEffectSnapshot(t, s, workID) == beforeState})
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
}

func TestWorkflowScenarioCorpusMutationsRerunAgainstAuthoritativeState(t *testing.T) {
	t.Parallel()
	corpus := readWorkflowScenarioCorpus(t)
	for _, scenario := range append([]workflowScenario{corpus.Scenarios[0]}, corpus.Scenarios[1].Cases...) {
		t.Run(scenario.ID, func(t *testing.T) {
			observation, err := executeWorkflowScenario(t, scenario.Action, scenario.Setup, scenario.Request)
			if err != nil {
				t.Fatal(err)
			}
			for _, original := range scenario.Expected.Assertions {
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
	for _, mutation := range []string{"actor", "scope", "capability", "setup_actor", "definition", "action"} {
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
				scenario.Request.ActionID = "resolve_condition"
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
		{"add_condition", map[string]any{"payload": map[string]any{"condition_id": "condition:probe"}}, true},
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
	for _, action := range []string{"record_proposal", "record_alignment", "record_design"} {
		version = issue31WorkflowAction(t, s, workID, version, action, workID+":"+action, actor)
	}
	binding := corpusArchitectureBinding()
	var hash, root string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT content_hash,root_domain_id FROM domain_registries WHERE product_id='product'`).Scan(&hash, &root); err != nil {
		t.Fatal(err)
	}
	binding["domain_registry_content_hash"], binding["home_domain_id"], binding["affected_domain_ids"] = hash, root, []string{root}
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
	for _, action := range []string{"record_proposal", "record_alignment", "record_design"} {
		version = issue31WorkflowAction(t, s, workID, version, action, workID+":"+action, actor)
	}
	payload := testApprovalPayload(t, s, workID, "approve_contract", json.RawMessage(`{"spec_mandate":[],"law_modifies":[],"architecture_binding":{"domain_registry_content_hash":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","home_domain_id":"root","affected_domain_ids":["root"],"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[]}}`))
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
	scope, err := beginFold(ctx, tx)
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
	return map[string]any{"domain_registry_content_hash": "sha256:" + strings.Repeat("b", 64), "home_domain_id": "domain/corpus-main", "affected_domain_ids": []string{"domain/corpus-main"}, "domain_modifies": []string{}, "domain_relation_modifies": []any{}, "law_additions": []any{}, "verification_obligations": []any{}}
}

func workflowCurrentVersion(ctx context.Context, s *Store, workID string) (int64, error) {
	var version int64
	err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&version)
	return version, err
}

func workflowEventSequence(ctx context.Context, s *Store, workID string) (int64, error) {
	var seq int64
	err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_type=? AND subject_id=?`, SubjectWorkItem, workID).Scan(&seq)
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
			observation.Communication["error"] = map[string]any{"kind": kind, "detail": failure.Detail}
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
	}
	conditions := map[string]any{}
	for _, condition := range projection.Conditions {
		conditions[condition.ID] = map[string]any{"state": condition.State}
	}
	observation.State["condition"] = conditions
	rows, err := s.DatabaseForTesting().QueryContext(ctx, `SELECT kind FROM domain_events WHERE subject_type=? AND subject_id=? AND seq>? ORDER BY seq`, SubjectWorkItem, workID, beforeSeq)
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
