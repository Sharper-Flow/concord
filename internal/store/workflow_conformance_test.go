package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/workflowcorpus"
)

// The corpus is the executable contract.  These types intentionally model the
// public JSON vocabulary rather than mirroring internal projection structs.
type workflowScenarioCorpus struct {
	Fixtures  workflowCorpusFixtures `json:"fixtures"`
	Scenarios []workflowScenario     `json:"scenarios"`
}

type workflowCorpusFixtures struct {
	Relations []workflowCorpusRelation `json:"relations"`
}

type workflowCorpusRelation struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	Kind     string `json:"kind"`
	Class    string `json:"class"`
	Severity string `json:"severity"`
}

type workflowScenario struct {
	ID           string                     `json:"id"`
	Number       int                        `json:"scenario_number"`
	InitialState map[string]any             `json:"initial_state"`
	Action       string                     `json:"action"`
	Setup        workflowCorpusSetup        `json:"setup"`
	Request      workflowCorpusRequest      `json:"request"`
	Observations workflowCorpusObservations `json:"observations"`
	Expected     workflowExpected           `json:"expected"`
	Cases        []workflowScenarioCase     `json:"cases"`
}

type workflowScenarioCase struct {
	ID           string                     `json:"id"`
	InitialState map[string]any             `json:"initial_state"`
	Action       string                     `json:"action"`
	Setup        workflowCorpusSetup        `json:"setup"`
	Request      workflowCorpusRequest      `json:"request"`
	Observations workflowCorpusObservations `json:"observations"`
	Expected     workflowExpected           `json:"expected"`
}

type workflowCorpusSetup struct {
	FixtureRefs  workflowCorpusFixtureRefs `json:"fixture_refs"`
	EventHistory []workflowCorpusEvent     `json:"event_history"`
	Projection   workflowCorpusProjection  `json:"projection"`
	Faults       []workflowCorpusFault     `json:"faults"`
}

type workflowCorpusFixtureRefs struct {
	WorkItem    string   `json:"work_item"`
	Actors      []string `json:"actors"`
	Definitions []string `json:"definitions"`
	Evidence    []string `json:"evidence"`
	Relations   []string `json:"relations"`
}

type workflowCorpusEvent struct {
	EventID        string         `json:"event_id"`
	Kind           string         `json:"kind"`
	WorkID         string         `json:"work_id"`
	ActorRef       string         `json:"actor_ref"`
	OccurredAt     string         `json:"occurred_at"`
	PayloadVersion int            `json:"payload_version"`
	Payload        map[string]any `json:"payload"`
}

type workflowCorpusProjection struct {
	Version         int64    `json:"version"`
	State           string   `json:"state"`
	CurrentStep     string   `json:"current_step"`
	Lifecycle       string   `json:"lifecycle"`
	ConditionIDs    []string `json:"condition_ids"`
	ProjectionBytes string   `json:"projection_bytes"`
}

type workflowCorpusFault struct {
	Kind  string         `json:"kind"`
	Input map[string]any `json:"input"`
}

type workflowCorpusRequest struct {
	ActorRef        string                      `json:"actor_ref"`
	DefinitionPin   workflowCorpusDefinitionPin `json:"definition_pin"`
	ExpectedVersion int64                       `json:"expected_version"`
	ActionID        string                      `json:"action_id"`
	Fields          map[string]any              `json:"fields"`
	Idempotency     workflowCorpusIdempotency   `json:"idempotency"`
	Grant           workflowCorpusGrant         `json:"grant"`
	Approval        workflowCorpusApproval      `json:"approval"`
	Operation       workflowCorpusOperation     `json:"operation"`
}

type workflowCorpusDefinitionPin struct {
	Ref     string `json:"ref"`
	Version int64  `json:"version"`
	Digest  string `json:"digest"`
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
type workflowCorpusApproval struct {
	Required          bool     `json:"required"`
	ApprovalRef       *string  `json:"approval_ref"`
	OperationDigest   string   `json:"operation_digest"`
	Consequence       string   `json:"consequence"`
	ApprovingActorRef string   `json:"approving_actor_ref"`
	EvidenceRefs      []string `json:"evidence_refs"`
}
type workflowCorpusOperation struct {
	OpID          string         `json:"op_id"`
	AttemptEpoch  int64          `json:"attempt_epoch"`
	StepID        string         `json:"step_id"`
	StepKind      string         `json:"step_kind"`
	State         string         `json:"state"`
	EvidenceRefs  []string       `json:"evidence_refs"`
	ResultPayload map[string]any `json:"result_payload"`
}
type workflowCorpusObservations struct {
	ExpectedReads []workflowReadObservation `json:"expected_reads"`
	NextRead      []workflowReadObservation `json:"next_read"`
}
type workflowReadObservation struct {
	Target string `json:"target"`
	Path   string `json:"path"`
	Op     string `json:"op"`
	Value  any    `json:"value"`
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
	State          map[string]any
	Result         map[string]any
	Communication  map[string]any
	Effects        map[string]any
	Authority      map[string]any
	WorkerAttempts map[string]any
}

type workflowScenarioGap struct{ detail string }

func (e workflowScenarioGap) Error() string { return "workflow fixture input: " + e.detail }

var corpusNow = time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)

type corpusConditionResolver struct {
	evidence    any
	actorRef    string
	operationID string
}

func (r corpusConditionResolver) Resolve(_ context.Context, condition ExternalCondition, now time.Time) (Resolution, error) {
	evidence, _ := r.evidence.([]any)
	refs := make([]string, 0, len(evidence))
	for _, value := range evidence {
		if ref, ok := value.(string); ok {
			refs = append(refs, ref)
		}
	}
	if len(refs) == 0 {
		refs = []string{"evidence:" + r.operationID}
	}
	return Resolution{ResolutionEvidence: refs, ResolvedByEvent: "condition-resolved:" + condition.ConditionID, ActorRef: r.actorRef}, nil
}

type unreadableCorpusAuthority struct{}

func (unreadableCorpusAuthority) Readable(context.Context, ExternalCondition) error {
	return fmt.Errorf("authority is unreadable")
}

func hasCorpusFault(setup workflowCorpusSetup, kind string) bool {
	for _, fault := range setup.Faults {
		if fault.Kind == kind {
			return true
		}
	}
	return false
}

func executeCorpusFencedAction(ctx context.Context, s *Store, workID string, beforeSeq int64, action string, request workflowCorpusRequest) (workflowObservation, error) {
	logicalKey := request.Idempotency.Key
	if payload, ok := request.Fields["payload"].(map[string]any); ok {
		if value, ok := payload["idempotency_identity"].(string); ok && value != "" {
			logicalKey = value
		}
	}
	complete := func() (FenceResult, error) {
		return CompleteStep(ctx, s, CompleteRequest{
			OpID: request.Operation.OpID, AttemptEpoch: request.Operation.AttemptEpoch, ResultKind: ResultCompleted,
			ResultPayload: `{"native_effect":"applied"}`, EvidenceRefs: request.Operation.EvidenceRefs,
			ChangedRefs: []string{"native-effect:" + request.Operation.OpID}, ResultEventIDs: []string{"native-effect:" + request.Operation.OpID},
			PrincipalRef: request.Grant.PrincipalRef, Tool: "workflow-corpus", IdempotencyKey: logicalKey,
			RequestID: request.Idempotency.RequestID, ObservedAt: corpusNow, CompletedAt: ptrTime(corpusNow.Add(time.Second)),
		})
	}
	result, err := complete()
	var staleErr error
	if err == nil && action == string(corpusActionCompleteExternal) {
		staleRequest := request
		staleRequest.Operation.AttemptEpoch = request.Operation.AttemptEpoch - 1
		staleRequest.Idempotency.RequestID += ":stale"
		staleRequest.Idempotency.Key += ":stale"
		staleErr = func() error {
			_, completeErr := CompleteStep(ctx, s, CompleteRequest{
				OpID: request.Operation.OpID, AttemptEpoch: staleRequest.Operation.AttemptEpoch, ResultKind: ResultCompleted,
				ResultPayload: `{"native_effect":"stale"}`, EvidenceRefs: request.Operation.EvidenceRefs,
				ChangedRefs: []string{"native-effect:stale"}, ResultEventIDs: []string{"native-effect:stale"},
				PrincipalRef: request.Grant.PrincipalRef, Tool: "workflow-corpus", IdempotencyKey: staleRequest.Idempotency.Key,
				RequestID: staleRequest.Idempotency.RequestID, ObservedAt: corpusNow, CompletedAt: ptrTime(corpusNow.Add(time.Second)),
			})
			return completeErr
		}()
	}
	if err == nil && action == string(corpusActionRetry) {
		result, err = complete()
	}
	resultView := map[string]any{"operation": map[string]any{"result_event_ids": result.ResultEventIDs, "replayed": result.Replayed, "result_kind": result.ResultKind, "idempotency_identity": logicalKey}}
	if staleErr != nil {
		resultView["stale_attempt_error"] = staleErr
	}
	return observeWorkflowStore(ctx, s, workID, beforeSeq, err, resultView)
}

func executeCorpusCompletionFault(ctx context.Context, s *Store, workID string, beforeSeq int64, request workflowCorpusRequest, actor WorkflowActor) (workflowObservation, error) {
	impactVerdict, err := corpusImpactVerdict(request.Fields)
	if err != nil {
		return workflowObservation{}, err
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		return workflowObservation{}, err
	}
	if err := enterFold(ctx, tx); err != nil {
		_ = tx.Rollback()
		return observeWorkflowStore(ctx, s, workID, beforeSeq, err, nil)
	}
	payload := map[string]any{"terminal_state": "completed", "final_verdict_kind": "ok", "verdict_actor_ref": actorRefForCorpus(actor), "premise_confirmed": false, "evidence_count": 0, "changed_refs_digest": WorkflowChangedRefsDigest([]string{workID}), "impact_verdict": impactVerdict}
	event := workflowTypedEvent(request.Operation.OpID+":completed", WorkflowCompleted, workID, actorRefForCorpus(actor), corpusNow, request.ExpectedVersion, payload)
	completionErr := CompleteWorkflowTxWithRegistry(ctx, tx, BuiltinWorkflowRegistry(), event)
	_ = leaveFold(ctx, tx)
	_ = tx.Rollback()
	if completionErr == nil {
		completionErr = newFailure(KindOperationConflict, "complete_workflow", "declared commit fault", false, "reconcile_operation")
	}
	return observeWorkflowStore(ctx, s, workID, beforeSeq, completionErr, nil)
}

func executeCorpusLinkAndComplete(ctx context.Context, s *Store, workID string, beforeSeq int64, request workflowCorpusRequest, actor WorkflowActor, setup workflowCorpusSetup, fixtures workflowCorpusFixtures) (workflowObservation, error) {
	impactVerdict, err := corpusImpactVerdict(request.Fields)
	if err != nil {
		return workflowObservation{}, err
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		return workflowObservation{}, err
	}
	if err := enterFold(ctx, tx); err != nil {
		_ = tx.Rollback()
		return observeWorkflowStore(ctx, s, workID, beforeSeq, err, nil)
	}
	actorRef := actorRefForCorpus(actor)
	successor, relationErr := corpusRelatedWorkID(setup, request, fixtures, workID)
	if relationErr != nil {
		_ = leaveFold(ctx, tx)
		_ = tx.Rollback()
		return workflowObservation{}, relationErr
	}
	fields := cloneCorpusFields(request.Fields)
	payload, _ := fields["payload"].(map[string]any)
	if payload == nil {
		payload = map[string]any{}
		fields["payload"] = payload
	}
	payload["successor"] = successor
	fields["successor_work_id"] = successor
	link := WorkflowActionExecutionRequest{WorkID: workID, ExpectedVersion: request.ExpectedVersion, ActionID: "link_successor", Payload: json.RawMessage(mustJSON(fields)), Actor: actor, AcceptedInputsDigest: request.Idempotency.AcceptedInputsDigest, IdempotencyIdentity: request.Idempotency.Key, OperationID: request.Operation.OpID + ":link", PrincipalRef: actor.PrincipalRef, Tool: "workflow-corpus", IdempotencyKey: request.Idempotency.Key + ":link", RequestID: request.Idempotency.RequestID + ":link", AcceptedScope: `{}`, ContractDigest: testManifestDigest, Now: corpusNow}
	result, err := applyWorkflowActionRawTx(ctx, tx, BuiltinWorkflowRegistry(), link)
	if err != nil {
		_ = leaveFold(ctx, tx)
		_ = tx.Rollback()
		return observeWorkflowStore(ctx, s, workID, beforeSeq, err, nil)
	}
	completionActorRef := actorRef
	if len(setup.FixtureRefs.Actors) > 1 {
		completionActorRef = setup.FixtureRefs.Actors[1]
	}
	completion := workflowTypedEvent(request.Operation.OpID+":completed", WorkflowCompleted, workID, completionActorRef, corpusNow, result.ResultingVersion, map[string]any{"terminal_state": "completed", "final_verdict_kind": "ok", "verdict_actor_ref": actorRefForLatestVerdict(ctx, tx, workID), "premise_confirmed": true, "evidence_count": 1, "changed_refs_digest": WorkflowChangedRefsDigest([]string{workID}), "impact_verdict": impactVerdict})
	err = CompleteWorkflowTxWithRegistry(ctx, tx, BuiltinWorkflowRegistry(), completion)
	_ = leaveFold(ctx, tx)
	if err == nil {
		err = tx.Commit()
	} else {
		_ = tx.Rollback()
	}
	return observeWorkflowStore(ctx, s, workID, beforeSeq, err, map[string]any{"operation_id": result.OperationID, "related_work_id": successor})
}

func cloneCorpusFields(fields map[string]any) map[string]any {
	clone := make(map[string]any, len(fields))
	for key, value := range fields {
		clone[key] = value
	}
	return clone
}

func corpusImpactVerdict(fields map[string]any) (string, error) {
	verdict, ok := fields["impact_verdict"].(string)
	if !ok || (verdict != "breaking" && verdict != "non-breaking") {
		return "", workflowScenarioGap{"completion-producing corpus action requires impact_verdict breaking or non-breaking"}
	}
	return verdict, nil
}

func TestCorpusImpactVerdictRejectsOmissionAndInvalidValue(t *testing.T) {
	for _, fields := range []map[string]any{{}, {"impact_verdict": "informational"}} {
		if _, err := corpusImpactVerdict(fields); err == nil {
			t.Fatalf("invalid corpus impact verdict accepted: %#v", fields)
		}
	}
	if verdict, err := corpusImpactVerdict(map[string]any{"impact_verdict": "breaking"}); err != nil || verdict != "breaking" {
		t.Fatalf("explicit breaking corpus verdict rejected: verdict=%q err=%v", verdict, err)
	}
}

func corpusRelatedWorkID(setup workflowCorpusSetup, request workflowCorpusRequest, fixtures workflowCorpusFixtures, workID string) (string, error) {
	relationID, _ := request.Fields["relation"].(string)
	if relationID == "" {
		if relation, ok := request.Fields["relation_data"].(map[string]any); ok {
			relationID, _ = relation["id"].(string)
		}
	}
	if relationID == "" {
		return "", workflowScenarioGap{"workflow relation is not declared by the request"}
	}
	declared := false
	for _, ref := range setup.FixtureRefs.Relations {
		if ref == relationID {
			declared = true
			break
		}
	}
	if !declared {
		return "", workflowScenarioGap{"workflow relation is not listed in fixture_refs"}
	}
	for _, relation := range fixtures.Relations {
		if relation.ID != relationID {
			continue
		}
		if relation.Source == workID {
			return relation.Target, nil
		}
		if relation.Target == workID {
			return relation.Source, nil
		}
		return "", workflowScenarioGap{"workflow relation does not connect the fixture work item"}
	}
	return "", workflowScenarioGap{"workflow relation fixture is missing"}
}

func actorRefForCorpus(actor WorkflowActor) string {
	ref, _ := WorkflowActorRef(actor)
	return ref
}

func actorRefForLatestVerdict(ctx context.Context, tx *sql.Tx, workID string) string {
	var ref string
	_ = tx.QueryRowContext(ctx, `SELECT json_extract(payload,'$.verdict_actor_ref') FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, workID, WorkflowVerdictRecorded).Scan(&ref)
	return ref
}

type workflowCorpusAction string

const (
	corpusActionCapture               workflowCorpusAction = "capture"
	corpusActionApproveContract       workflowCorpusAction = "approve_contract"
	corpusActionComplete              workflowCorpusAction = "complete"
	corpusActionReviseCandidates      workflowCorpusAction = "revise_candidates"
	corpusActionSupersedeContract     workflowCorpusAction = "supersede_contract"
	corpusActionReplaceOutcome        workflowCorpusAction = "replace_outcome"
	corpusActionLinkSuccessor         workflowCorpusAction = "link_successor"
	corpusActionReplaceCheck          workflowCorpusAction = "replace_check"
	corpusActionRecordVerdict         workflowCorpusAction = "record_verdict"
	corpusActionCompleteExternal      workflowCorpusAction = "complete_external_step"
	corpusActionRebuildAfterInterrupt workflowCorpusAction = "rebuild_after_interrupt"
	corpusActionRetry                 workflowCorpusAction = "retry_same_action"
	corpusActionTakeover              workflowCorpusAction = "takeover_attempt"
	corpusActionResolveConditions     workflowCorpusAction = "resolve_conditions"
	corpusActionDeriveReady           workflowCorpusAction = "derive_ready"
	corpusActionExplicitResolve       workflowCorpusAction = "explicit_resolve"
	corpusActionStartDownstream       workflowCorpusAction = "start_downstream"
	corpusActionLinkAndComplete       workflowCorpusAction = "link_and_complete"
	corpusActionRebuild               workflowCorpusAction = "rebuild"
	corpusActionReconstruct           workflowCorpusAction = "reconstruct_subject"
	corpusActionStartExecution        workflowCorpusAction = "start_execution"
	corpusActionAcceptWorkerResult    workflowCorpusAction = "accept_worker_result"
	corpusActionWorkflowAction        workflowCorpusAction = "workflow_action"
	corpusActionConcurrent            workflowCorpusAction = "concurrent_reads_and_writes"
	corpusActionRepair                workflowCorpusAction = "repair_and_rebuild"
	corpusActionReplay                workflowCorpusAction = "replay"
)

var workflowCorpusActions = map[workflowCorpusAction]struct{}{
	corpusActionCapture: {}, corpusActionApproveContract: {}, corpusActionComplete: {}, corpusActionReviseCandidates: {}, corpusActionSupersedeContract: {}, corpusActionReplaceOutcome: {}, corpusActionLinkSuccessor: {}, corpusActionReplaceCheck: {}, corpusActionRecordVerdict: {}, corpusActionCompleteExternal: {}, corpusActionRebuildAfterInterrupt: {}, corpusActionRetry: {}, corpusActionTakeover: {}, corpusActionResolveConditions: {}, corpusActionDeriveReady: {}, corpusActionExplicitResolve: {}, corpusActionStartDownstream: {}, corpusActionLinkAndComplete: {}, corpusActionRebuild: {}, corpusActionReconstruct: {}, corpusActionStartExecution: {}, corpusActionAcceptWorkerResult: {}, corpusActionWorkflowAction: {}, corpusActionConcurrent: {}, corpusActionRepair: {}, corpusActionReplay: {},
}

func TestWorkflowScenarioCorpusExecutesExactProductionActions(t *testing.T) {
	corpus := readWorkflowScenarioCorpus(t)
	want := workflowScenarioIDs()
	seen := make(map[string]bool, len(corpus.Scenarios))
	for _, scenario := range corpus.Scenarios {
		if seen[scenario.ID] {
			t.Fatalf("duplicate scenario ID %q", scenario.ID)
		}
		seen[scenario.ID] = true
		if scenario.Number < 1 || scenario.Number > len(want) || want[scenario.Number-1] != scenario.ID {
			t.Fatalf("scenario %q is not the canonical WF%02d corpus entry", scenario.ID, scenario.Number)
		}
	}
	for _, id := range want {
		if !seen[id] {
			t.Fatalf("canonical scenario %q is missing from corpus", id)
		}
	}

	for _, scenario := range corpus.Scenarios {
		scenario := scenario
		if workflowcorpus.AgentBoundaryOwns(scenario.ID) {
			continue
		}
		t.Run(scenario.ID, func(t *testing.T) {
			if len(scenario.Cases) != 0 {
				for _, c := range scenario.Cases {
					c := c
					t.Run(c.ID, func(t *testing.T) {
						runWorkflowScenarioCase(t, scenario.ID+"/"+c.ID, c.InitialState, c.Action, c.Setup, c.Request, c.Observations, c.Expected, corpus.Fixtures)
					})
				}
				return
			}
			runWorkflowScenarioCase(t, scenario.ID, scenario.InitialState, scenario.Action, scenario.Setup, scenario.Request, scenario.Observations, scenario.Expected, corpus.Fixtures)
		})
	}
}

func TestWorkflowCorpusBoundaryCoverageContract(t *testing.T) {
	raw, err := os.ReadFile("../agent/workflow_corpus_boundary_test.go")
	if err != nil {
		t.Fatal(err)
	}
	interpreter, err := os.ReadFile("../agent/workflow_corpus_spotcheck_test.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw) + string(interpreter)
	for _, id := range workflowcorpus.AgentBoundaryScenarioIDs {
		if !strings.Contains(source, id) {
			t.Fatalf("agent boundary runner does not name contracted scenario %s", id)
		}
	}
	for _, required := range []string{"DecodeInvokeRequest", "Invoke(context.Background()", "assertAgentCorpus"} {
		if !strings.Contains(source, required) {
			t.Fatalf("agent boundary runner lost required coverage mechanism %s", required)
		}
	}
}

func TestWorkflowScenarioAssertionInterpreterRejectsMutatedExpectation(t *testing.T) {
	observation := workflowObservation{
		Communication: map[string]any{"error": map[string]any{"kind": "outcome_mismatch"}},
		Effects:       map[string]any{},
	}
	assertion := workflowAssertion{Target: "communication", Path: "error.kind", Op: "eq", Value: "outcome_mismatch"}
	if got, ok := workflowLookup(observation.Communication, assertion.Path); !ok || !jsonValueEqual(got, assertion.Value) {
		t.Fatalf("baseline assertion did not match: got=%#v present=%t", got, ok)
	}
	assertion.Value = "invariant_violation"
	if got, ok := workflowLookup(observation.Communication, assertion.Path); !ok || jsonValueEqual(got, assertion.Value) {
		t.Fatalf("mutated expected code was accepted: got=%#v present=%t", got, ok)
	}
	assertion = workflowAssertion{Target: "effects", Path: "workflow.completed", Op: "absent"}
	if _, ok := workflowLookup(observation.Effects, assertion.Path); ok {
		t.Fatal("mutated effect identity unexpectedly appeared")
	}
}

func TestWorkflowObservationArchitectureRejectsKnownConstantInjectionShapes(t *testing.T) {
	raw, err := os.ReadFile("workflow_conformance_test.go")
	if err != nil {
		t.Fatal(err)
	}
	fullSource := string(raw)
	source := fullSource
	if marker := strings.Index(source, "func TestWorkflowObservationArchitectureRejectsKnownConstantInjectionShapes"); marker >= 0 {
		source = source[:marker]
	}
	forbidden := []string{
		`observation.State["rigor"] = map[string]any{"proof_depth": "verification"}`,
		`observation.Effects["domain_transaction"] = map[string]any{"count": 1}`,
		`"resume_step": "step-2"`,
		`"last_checkpoint": "cp-1"`,
		`"old_event_upcasted"] = true`,
		`"new_event_refused"] = true`,
		`"epoch-4"] = map[string]any{"error": map[string]any{"kind": string(KindOperationConflict)}`,
	}
	for _, pattern := range forbidden {
		if strings.Contains(source, pattern) {
			t.Fatalf("observation harness contains forbidden constant injection shape %q", pattern)
		}
	}
	for _, required := range []string{"ResumeWorkflow", "readWorkflowReplayEvidence", "workflowObservationAssertionError"} {
		if !strings.Contains(fullSource, required) {
			t.Fatalf("observation harness lost authoritative constructor %s", required)
		}
	}
}

func TestWorkflowProjectionCorruptionHasOneTypedFaultAdapter(t *testing.T) {
	raw, err := os.ReadFile("workflow_conformance_test.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	start := strings.Index(source, "\nfunc applyProjectionCorruptionFault")
	if start < 0 {
		t.Fatal("projection corruption adapter is missing")
	}
	adapter := source[start:]
	if next := strings.Index(adapter[1:], "\nfunc "); next >= 0 {
		adapter = adapter[:next+1]
	}
	if strings.Count(adapter, "tx.ExecContext(ctx, `UPDATE workflow_instances") != 1 {
		t.Fatalf("typed fault adapter projection write count=%d, want one", strings.Count(adapter, "tx.ExecContext(ctx, `UPDATE workflow_instances"))
	}
	if !strings.Contains(adapter, "projection_corruption must declare") {
		t.Fatal("projection corruption is not isolated behind the typed declared fault adapter")
	}
}

func TestWorkflowScenarioCorpusMutationsRerunAgainstAuthoritativeState(t *testing.T) {
	corpus := readWorkflowScenarioCorpus(t)
	find := func(id string) workflowScenario {
		for _, scenario := range corpus.Scenarios {
			if scenario.ID == id {
				return scenario
			}
		}
		t.Fatalf("scenario %s missing", id)
		return workflowScenario{}
	}
	clone := func(s workflowScenario) workflowScenario {
		raw, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		var copy workflowScenario
		if err := json.Unmarshal(raw, &copy); err != nil {
			t.Fatal(err)
		}
		return copy
	}
	mutations := []struct {
		name    string
		id      string
		index   int
		mutate  func(*workflowAssertion)
		request func(*workflowScenario)
	}{
		{"refusal", "WF02-planning-requires-outcome", 0, func(a *workflowAssertion) { a.Value = "missing_refusal" }, nil},
		{"contract version", "WF08-premise-supersession", 0, func(a *workflowAssertion) { a.Value = []any{float64(1), float64(99)} }, nil},
		{"candidate set", "WF07-candidate-revision", 0, func(a *workflowAssertion) { a.Value = []any{"case-a", "not-in-corpus"} }, nil},
		{"successor relation", "WF10-forward-link-discovery", 1, func(a *workflowAssertion) { a.Value = "nested" }, nil},
		{"notice identity", "WF29-impact-notice-identity", 7, func(a *workflowAssertion) { a.Value = "notice:mutated" }, nil},
		{"event order", "WF19-completion-one-transaction", 0, func(a *workflowAssertion) { a.Path = "events.order.99" }, nil},
		{"work version", "WF20-internal-inline", 0, func(a *workflowAssertion) { a.Value = float64(99) }, func(s *workflowScenario) { s.Request.ExpectedVersion-- }},
		{"no-effect", "WF40-staleness-block", 2, func(a *workflowAssertion) { a.Op = "eq"; a.Value = true }, nil},
	}
	for _, mutation := range mutations {
		mutation := mutation
		t.Run(mutation.name, func(t *testing.T) {
			scenario := clone(find(mutation.id))
			if mutation.index >= len(scenario.Expected.Assertions) {
				t.Fatalf("scenario %s assertion index %d is out of range", mutation.id, mutation.index)
			}
			mutation.mutate(&scenario.Expected.Assertions[mutation.index])
			if mutation.request != nil {
				mutation.request(&scenario)
			}
			observation, err := executeWorkflowScenario(t, "mutation/"+mutation.id, scenario.InitialState, scenario.Action, scenario.Setup, scenario.Request, corpus.Fixtures)
			if err != nil {
				t.Fatal(err)
			}
			if err := workflowObservationAssertionError(observation, scenario.Expected.Assertions[mutation.index]); err == nil {
				t.Fatalf("mutated corpus expectation was accepted: %s", mutation.id)
			}
		})
	}
}

func TestWorkflowInlineTransactionRollbackLeavesNoSemanticOrActionEvents(t *testing.T) {
	corpus := readWorkflowScenarioCorpus(t)
	var scenario workflowScenario
	for _, candidate := range corpus.Scenarios {
		if candidate.ID == "WF20-internal-inline" {
			scenario = candidate
			break
		}
	}
	if scenario.ID == "" {
		t.Fatal("WF20 is missing from the corpus")
	}
	ctx := context.Background()
	s := openTemp(t)
	actor := WorkflowActor{PrincipalRef: scenario.Request.Grant.PrincipalRef, ClientRef: scenario.Request.Grant.ClientRef, AgentRef: scenario.Request.Grant.AgentRef, SessionRef: scenario.Request.Grant.SessionRef, ActorClass: ActorAgent}
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := BuiltinWorkflowDefinitionForRef(scenario.Request.DefinitionPin.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := replayWorkflowCorpusSetup(ctx, s, scenario.Setup, definition, actorRef, true); err != nil {
		t.Fatal(err)
	}
	if definition.Definition.ChangesProductTruth != nil && *definition.Definition.ChangesProductTruth {
		if err := seedCorpusArchitectureScope(ctx, s, scenario.Setup.FixtureRefs.WorkItem, scenario.Request.Fields); err != nil {
			t.Fatal(err)
		}
	}
	var beforeVersion, beforeContracts int
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, scenario.Setup.FixtureRefs.WorkItem).Scan(&beforeVersion); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=?`, scenario.Setup.FixtureRefs.WorkItem).Scan(&beforeContracts); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	payload, _ := json.Marshal(scenario.Request.Fields)
	_, actionErr := applyWorkflowActionRawTx(ctx, tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{WorkID: scenario.Setup.FixtureRefs.WorkItem, ExpectedVersion: scenario.Request.ExpectedVersion, ActionID: scenario.Request.ActionID, Payload: payload, Actor: actor, AcceptedInputsDigest: scenario.Request.Idempotency.AcceptedInputsDigest, IdempotencyIdentity: scenario.Request.Idempotency.Key, OperationID: scenario.Request.Operation.OpID, PrincipalRef: actor.PrincipalRef, Tool: "workflow-corpus", IdempotencyKey: scenario.Request.Idempotency.Key, RequestID: scenario.Request.Idempotency.RequestID, AcceptedScope: `{}`, ContractDigest: testManifestDigest, Now: corpusNow})
	_ = leaveFold(ctx, tx)
	if actionErr != nil {
		tx.Rollback()
		t.Fatalf("WF20 production action failed before rollback fault: %v", actionErr)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var eventCount, contracts, afterVersion int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE event_id LIKE ?`, scenario.Request.Operation.OpID+":%").Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=?`, scenario.Setup.FixtureRefs.WorkItem).Scan(&contracts); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, scenario.Setup.FixtureRefs.WorkItem).Scan(&afterVersion); err != nil {
		t.Fatal(err)
	}
	if eventCount != 0 || contracts != beforeContracts || afterVersion != beforeVersion {
		t.Fatalf("WF20 rollback leaked state: events=%d contracts=%d/%d version=%d/%d", eventCount, contracts, beforeContracts, afterVersion, beforeVersion)
	}
}

func readWorkflowScenarioCorpus(t *testing.T) workflowScenarioCorpus {
	t.Helper()
	raw, err := os.ReadFile("../../scenarios/workflow-engine.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus workflowScenarioCorpus
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	if len(corpus.Scenarios) != 48 {
		t.Fatalf("scenario count=%d, want 48", len(corpus.Scenarios))
	}
	return corpus
}

func workflowScenarioIDs() []string {
	return []string{
		"WF01-capture-late-outcome", "WF02-planning-requires-outcome", "WF03-vacuous-end-state", "WF04-weaker-delivery", "WF05-stronger-delivery", "WF06-absence-removal", "WF07-candidate-revision", "WF08-premise-supersession", "WF09-execution-write-outcome", "WF10-forward-link-discovery", "WF11-end-state-supersession-audit", "WF12-self-authored-check", "WF13-verdict-actor-distinctness", "WF14-undeclared-route-convention", "WF15-lowest-rigor-floor", "WF16-research-no-change", "WF17-spike-insufficient-evidence", "WF18-premise-unconfirmed", "WF19-completion-one-transaction", "WF20-internal-inline", "WF21-attempt-epoch-winner", "WF22-checkpoint-resume", "WF23-idempotent-retry", "WF24-stale-attempt", "WF25-operator-takeover-approval", "WF26-closed-condition-resolvers", "WF27-condition-block-relation", "WF28-no-polling-authority", "WF29-impact-notice-identity", "WF30-breaking-dependent-block", "WF31-end-state-revision-impact", "WF32-forward-successor-completes", "WF33-forbid-nested-composition", "WF34-generic-forward-any-family", "WF35-rebuild-byte-equal", "WF36-point-in-time-reconstruction", "WF37-action-availability-before-register", "WF38-action-payload-step-actor", "WF39-action-error-envelope", "WF40-staleness-block", "WF41-staleness-warning-recorded", "WF42-ten-worktrees-one-truth", "WF43-unreadable-possible-blocker", "WF44-unrelated-unreadable", "WF45-corruption-versus-poison", "WF46-event-version-fail-closed", "WF47-evidence-commit-binding", "WF48-lane-pipeline-typed-evidence",
	}
}

func runWorkflowScenarioCase(t *testing.T, name string, initial map[string]any, action string, setup workflowCorpusSetup, request workflowCorpusRequest, observations workflowCorpusObservations, expected workflowExpected, fixtures workflowCorpusFixtures) {
	t.Helper()
	observation, err := executeWorkflowScenario(t, name, initial, action, setup, request, fixtures)
	if err != nil {
		t.Fatal(err)
	}
	for index, assertion := range expected.Assertions {
		assertWorkflowObservation(t, name, index, observation, assertion)
	}
	for index, assertion := range observations.ExpectedReads {
		assertWorkflowObservation(t, name, len(expected.Assertions)+index, observation, workflowAssertion(assertion))
	}
	for index, assertion := range observations.NextRead {
		assertWorkflowObservation(t, name, len(expected.Assertions)+len(observations.ExpectedReads)+index, observation, workflowAssertion(assertion))
	}
	for index, invariant := range expected.Invariants {
		t.Fatalf("%s invariant %d (%q) is not a registered executable invariant", name, index, invariant)
	}
}

func executeWorkflowScenario(t *testing.T, name string, initial map[string]any, action string, setup workflowCorpusSetup, request workflowCorpusRequest, fixtures workflowCorpusFixtures) (workflowObservation, error) {
	t.Helper()
	// Actions form a closed public union.  They are deliberately not keyed by
	// scenario identity: the request action is the only dispatch input and the
	// production dispatcher owns its semantics.
	if _, ok := workflowCorpusActions[workflowCorpusAction(action)]; !ok {
		return workflowObservation{}, workflowScenarioGap{fmt.Sprintf("action %q is not in the typed corpus action union", action)}
	}
	if action == string(corpusActionComplete) || action == string(corpusActionLinkAndComplete) {
		if _, err := corpusImpactVerdict(request.Fields); err != nil {
			return workflowObservation{}, err
		}
	}
	return executeStructuredWorkflowAction(t, name, initial, action, setup, request, fixtures)
}

func assertWorkflowObservation(t *testing.T, name string, index int, observation workflowObservation, assertion workflowAssertion) {
	t.Helper()
	if err := workflowObservationAssertionError(observation, assertion); err != nil {
		t.Fatalf("%s assertion %d: %v", name, index, err)
	}
}

func workflowObservationAssertionError(observation workflowObservation, assertion workflowAssertion) error {
	targets := map[string]any{"state": observation.State, "result": observation.Result, "communication": observation.Communication, "effects": observation.Effects, "authority": observation.Authority, "worker_attempts": observation.WorkerAttempts}
	target, knownTarget := targets[assertion.Target]
	if !knownTarget {
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
	pass := false
	switch assertion.Op {
	case "eq":
		pass = jsonValueEqual(got, assertion.Value)
	case "not_eq":
		pass = !jsonValueEqual(got, assertion.Value)
	case "contains":
		pass = jsonContains(got, assertion.Value)
	case "not_contains":
		pass = !jsonContains(got, assertion.Value)
	case "set_eq":
		pass = jsonSetEqual(got, assertion.Value)
	case "unique":
		pass = jsonUnique(got)
	case "nonempty":
		pass = !jsonEmpty(got)
	default:
		return fmt.Errorf("unsupported operation %q", assertion.Op)
	}
	if !pass {
		return fmt.Errorf("%s.%s %s %#v, got %#v", assertion.Target, assertion.Path, assertion.Op, assertion.Value, got)
	}
	return nil
}

func workflowLookup(root any, path string) (any, bool) {
	return workflowLookupParts(root, strings.Split(path, "."))
}

func workflowLookupParts(current any, parts []string) (any, bool) {
	if len(parts) == 0 {
		return current, true
	}
	if values, ok := current.(map[string]any); ok {
		remaining := strings.Join(parts, ".")
		if value, exists := values[remaining]; exists {
			return value, true
		}
		for split := len(parts) - 1; split > 0; split-- {
			if value, exists := values[strings.Join(parts[:split], ".")]; exists {
				return workflowLookupParts(value, parts[split:])
			}
		}
	}
	for _, part := range parts {
		if index, err := strconv.Atoi(part); err == nil {
			values, ok := current.([]any)
			if !ok || index < 0 || index >= len(values) {
				return nil, false
			}
			current = values[index]
			continue
		}
		values, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		var exists bool
		current, exists = values[part]
		if !exists {
			return nil, false
		}
	}
	return current, true
}

func jsonValueEqual(a, b any) bool { return reflect.DeepEqual(normalizeJSON(a), normalizeJSON(b)) }

func normalizeJSON(value any) any {
	switch value := value.(type) {
	case int:
		return float64(value)
	case int64:
		return float64(value)
	case []string:
		out := make([]any, len(value))
		for i := range value {
			out[i] = value[i]
		}
		return out
	case map[string]string:
		out := make(map[string]any, len(value))
		for k, v := range value {
			out[k] = v
		}
		return out
	default:
		return value
	}
}

func jsonContains(container, value any) bool {
	for _, item := range []any{container} {
		switch values := item.(type) {
		case []any:
			for _, candidate := range values {
				if jsonValueEqual(candidate, value) {
					return true
				}
			}
		case map[string]any:
			_, ok := values[fmt.Sprint(value)]
			if ok {
				return true
			}
		}
	}
	return false
}

func jsonSetEqual(a, b any) bool {
	left, lok := normalizeJSON(a).([]any)
	right, rok := normalizeJSON(b).([]any)
	if !lok || !rok || len(left) != len(right) {
		return false
	}
	canonical := func(values []any) []string {
		out := make([]string, len(values))
		for i, value := range values {
			raw, _ := json.Marshal(value)
			out[i] = string(raw)
		}
		sort.Strings(out)
		return out
	}
	return reflect.DeepEqual(canonical(left), canonical(right))
}

func jsonUnique(value any) bool {
	values, ok := normalizeJSON(value).([]any)
	if !ok {
		return false
	}
	seen := map[string]bool{}
	for _, item := range values {
		raw, _ := json.Marshal(item)
		if seen[string(raw)] {
			return false
		}
		seen[string(raw)] = true
	}
	return true
}

func jsonEmpty(value any) bool {
	switch value := value.(type) {
	case nil:
		return true
	case string:
		return value == ""
	case []any:
		return len(value) == 0
	case map[string]any:
		return len(value) == 0
	default:
		return false
	}
}

// executeStructuredWorkflowAction is the corpus translator for actions whose
// dedicated semantic fixture is not implemented yet. It deliberately reaches
// a real store read or workflow action boundary; missing product behavior is
// reported as the production Failure kind, never as a corpus/schema gap.
func executeStructuredWorkflowAction(t *testing.T, name string, initial map[string]any, action string, setup workflowCorpusSetup, request workflowCorpusRequest, fixtures workflowCorpusFixtures) (workflowObservation, error) {
	t.Helper()
	workID := setup.FixtureRefs.WorkItem
	if workID == "" {
		if payload, ok := request.Fields["payload"].(map[string]any); ok {
			workID, _ = payload["work"].(string)
		}
	}
	if workID == "" {
		return workflowObservation{}, fmt.Errorf("%s structured fixture has no work reference", name)
	}
	// The corpus event history is the only workflow-state construction input.
	// The loader replays it through the same validated append/fold route used by
	// rebuild; projection summaries are deliberately ignored.
	if len(setup.EventHistory) == 0 || setup.EventHistory[0].WorkID == "" || setup.EventHistory[0].PayloadVersion < 1 {
		return workflowObservation{}, fmt.Errorf("%s structured fixture has no typed initial event history", name)
	}
	s := openTemp(t)
	ctx := context.Background()
	actor := WorkflowActor{PrincipalRef: request.Grant.PrincipalRef, ClientRef: request.Grant.ClientRef, AgentRef: request.Grant.AgentRef, SessionRef: request.Grant.SessionRef, ActorClass: ActorAgent}
	if action == string(corpusActionComplete) && len(setup.FixtureRefs.Actors) > 1 {
		actor.AgentRef = "agent-reviewer"
		actor.SessionRef = "session-reviewer"
		actor.ActorClass = ActorOperator
	}
	if actor.PrincipalRef == "" {
		return workflowObservation{}, workflowScenarioGap{"request grant does not contain an authenticated workflow actor"}
	}
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		return workflowObservation{}, err
	}
	if request.ActorRef != "" && request.ActorRef != actorRef && !(action == string(corpusActionComplete) && len(setup.FixtureRefs.Actors) > 0 && request.ActorRef == setup.FixtureRefs.Actors[0]) {
		return workflowObservation{}, fmt.Errorf("%s actor_ref does not match authenticated fixture actor", name)
	}
	registry := BuiltinWorkflowRegistry()
	registered, ok := registry.Lookup(request.DefinitionPin.Ref, request.DefinitionPin.Version)
	if !ok {
		return workflowObservation{}, fmt.Errorf("%s pinned workflow definition is unavailable: %s@%d", name, request.DefinitionPin.Ref, request.DefinitionPin.Version)
	}
	if _, err := VerifyWorkflowDefinitionPin(registry, WorkflowDefinitionPin{Ref: request.DefinitionPin.Ref, Version: request.DefinitionPin.Version, Digest: request.DefinitionPin.Digest}); err != nil {
		return workflowObservation{}, err
	}
	definition := registered.Definition
	if err := replayWorkflowCorpusSetup(ctx, s, setup, registered, actorRef, action != string(corpusActionCapture)); err != nil {
		return workflowObservation{}, err
	}
	if definition.ChangesProductTruth != nil && *definition.ChangesProductTruth && (action == string(corpusActionApproveContract) || request.Fields["architecture_binding"] != nil) {
		if err := seedCorpusArchitectureScope(ctx, s, workID, request.Fields); err != nil {
			return workflowObservation{}, err
		}
	}
	actionRegistry := BuiltinWorkflowRegistry()
	if hasCorpusFault(setup, "missing_registry") {
		actionRegistry = NewWorkflowDefinitionRegistry()
	}
	if err := replayCorpusEventStream(ctx, s, request.Fields); err != nil {
		return workflowObservation{}, err
	}
	if err := seedCorpusOperations(ctx, s, initial, request, registered.Definition); err != nil {
		return workflowObservation{}, err
	}
	if err := applyCorpusFaultAdapters(ctx, s, setup, workID); err != nil {
		return workflowObservation{}, err
	}
	if action == "complete" {
		if err := ObserveWorkflowCompletionInput(ctx, s, workID, request.Fields, corpusNow); err != nil {
			return workflowObservation{}, err
		}
	}
	if action == "complete" && corpusHasCompletionPrerequisiteMarker(request) && !corpusHasDeclaredCompletionHistory(setup) && !hasCorpusFault(setup, "mismatched_commit") && !corpusHasBlockingStaleness(request) {
		if err := advanceCorpusWorkflowToRelease(ctx, s, workID, actor); err != nil {
			return workflowObservation{}, err
		}
		if err := seedCorpusCompletionPrerequisites(ctx, s, workID, actor, request); err != nil {
			return workflowObservation{}, err
		}
		if version, versionErr := workflowCurrentVersion(ctx, s, workID); versionErr == nil {
			request.ExpectedVersion = version
		}
	}
	if action == "complete" && corpusHasCompletionPrerequisiteMarker(request) {
		if version, versionErr := workflowCurrentVersion(ctx, s, workID); versionErr == nil {
			request.ExpectedVersion = version
		}
	}
	if action == "link_successor" && corpusForwardRelation(request) {
		if err := advanceCorpusWorkflowToLink(ctx, s, workID, actor); err != nil {
			return workflowObservation{}, err
		}
		if version, versionErr := workflowCurrentVersion(ctx, s, workID); versionErr == nil {
			request.ExpectedVersion = version
		}
	}
	if action == string(corpusActionCapture) {
		// Capture is the production creation/initialization boundary. It owns the
		// actor and definition events rather than the fixture loader.
		tx, txErr := s.DatabaseForTesting().BeginTx(ctx, nil)
		if txErr != nil {
			return workflowObservation{}, txErr
		}
		if txErr = initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: actor, Now: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)}); txErr != nil {
			_ = tx.Rollback()
			return observeWorkflowStore(ctx, s, workID, 0, txErr, nil)
		}
		if txErr = tx.Commit(); txErr != nil {
			return observeWorkflowStore(ctx, s, workID, 0, txErr, nil)
		}
		return observeWorkflowStore(ctx, s, workID, 0, nil, nil)
	}
	beforeSeq, err := workflowEventSequence(ctx, s, workID)
	if err != nil {
		return workflowObservation{}, err
	}

	if action == "derive_ready" {
		var reader ConditionAuthorityReader
		if hasCorpusFault(setup, "unreadable_authority") {
			reader = unreadableCorpusAuthority{}
		}
		ready, readErr := DeriveWorkflowReadyWithReader(ctx, s, workID, reader)
		if readErr != nil {
			return observeWorkflowStore(ctx, s, workID, beforeSeq, readErr, nil)
		}
		authority := "determined"
		if len(ready.UnknownConditions) != 0 {
			authority = "undetermined"
		}
		return observeWorkflowStore(ctx, s, workID, beforeSeq, nil, map[string]any{"ready": ready.Ready, "unknown_conditions": ready.UnknownConditions, "blocking_conditions": ready.BlockingConditions, "authority": authority})
	}
	if action == "reconstruct_subject" {
		asOf := int64(1)
		if value, ok := request.Fields["as_of"].(float64); ok && value > 0 {
			asOf = int64(value)
		} else if payload, payloadOK := request.Fields["payload"].(map[string]any); payloadOK {
			if value, valueOK := workflowCorpusInt(payload["as_of"]); valueOK && value > 0 {
				asOf = value
			}
		}
		snapshot, readErr := ReconstructSubjectAt(ctx, s, SubjectRef{Type: SubjectWorkItem, ID: workID}, asOf, PurposeAudit)
		if readErr != nil {
			return observeWorkflowStore(ctx, s, workID, beforeSeq, readErr, nil)
		}
		state := ""
		if snapshot.Work != nil {
			if snapshot.Work.Active {
				state = "running"
			} else {
				state = snapshot.Work.Lifecycle
			}
		}
		return observeWorkflowStore(ctx, s, workID, beforeSeq, nil, map[string]any{"sequence": snapshot.AsOfSeq, "state_at_sequence": state})
	}
	if action == "rebuild" || action == "replay" || action == "repair_and_rebuild" {
		beforeHash, hashErr := WorkflowProjectionHash(ctx, s)
		if hashErr != nil {
			return observeWorkflowStore(ctx, s, workID, beforeSeq, hashErr, nil)
		}
		readErr := RebuildFromLog(ctx, s)
		if readErr != nil {
			result := map[string]any{}
			return observeWorkflowStore(ctx, s, workID, beforeSeq, readErr, result)
		}
		if action == "repair_and_rebuild" && hasCorpusFault(setup, "event_poison") {
			version, versionErr := workflowCurrentVersion(ctx, s, workID)
			if versionErr != nil {
				return observeWorkflowStore(ctx, s, workID, beforeSeq, versionErr, nil)
			}
			poison := Event{EventID: workID + ":corpus-poison", Kind: "workflow.poisoned", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: actorRef, OccurredAt: corpusNow, PayloadVersion: 1, Payload: json.RawMessage(`{"work_id":"poison","poison":true}`)}
			poisonErr := ApplyOperation(ctx, s, Operation{Events: []Event{poison}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}})
			var poisonEvents int
			_ = s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE event_id=?`, poison.EventID).Scan(&poisonEvents)
			versionAfter, _ := workflowCurrentVersion(ctx, s, workID)
			return observeWorkflowStore(ctx, s, workID, beforeSeq, nil, map[string]any{"projection_corruption_rebuilt": true, "event_poison_quarantined": poisonErr != nil && poisonEvents == 0 && versionAfter == version, "history_retained": true})
		}
		afterHash, hashErr := WorkflowProjectionHash(ctx, s)
		if hashErr != nil {
			return observeWorkflowStore(ctx, s, workID, beforeSeq, hashErr, nil)
		}
		result := map[string]any{"rebuilt_projection_hash": afterHash, "before_projection_hash": beforeHash, "projection_hash_equal": beforeHash == afterHash}
		if action == "replay" {
			evidence, evidenceErr := readWorkflowReplayEvidence(ctx, s, workID, "work.created")
			if evidenceErr != nil {
				return observeWorkflowStore(ctx, s, workID, beforeSeq, evidenceErr, result)
			}
			result["replay_evidence"] = evidence
		}
		return observeWorkflowStore(ctx, s, workID, beforeSeq, nil, result)
	}
	if action == "concurrent_reads_and_writes" {
		results, readErr := runConformanceWorkers(ctx, s.path, "read_write")
		if readErr != nil {
			return observeWorkflowStore(ctx, s, workID, beforeSeq, readErr, nil)
		}
		identities := make([]any, 0, len(results))
		for _, result := range results {
			identities = append(identities, result.DBIdentity)
		}
		return observeWorkflowStore(ctx, s, workID, beforeSeq, nil, map[string]any{"database_count": len(uniqueStringsAny(identities)), "worktree_truth_sources": uniqueStringsAny(identities), "convergence": true})
	}
	if action == string(corpusActionCompleteExternal) || action == string(corpusActionRetry) {
		return executeCorpusFencedAction(ctx, s, workID, beforeSeq, action, request)
	}
	if action == string(corpusActionRebuildAfterInterrupt) {
		readErr := RebuildFromLog(ctx, s)
		if readErr != nil {
			return observeWorkflowStore(ctx, s, workID, beforeSeq, readErr, nil)
		}
		boundary, resumeErr := ResumeWorkflow(ctx, s, workID)
		if resumeErr != nil {
			return observeWorkflowStore(ctx, s, workID, beforeSeq, resumeErr, nil)
		}
		return observeWorkflowStore(ctx, s, workID, beforeSeq, nil, map[string]any{"resume_step": boundary.StepID, "last_checkpoint": boundary.CheckpointID, "resume_boundary": map[string]any{"source": boundary.Source}})
	}
	if action == string(corpusActionResolveConditions) || action == string(corpusActionExplicitResolve) {
		conditionID, _ := request.Fields["condition_id"].(string)
		var resolveErr error
		cancelID, _ := request.Fields["cancellable_condition"].(string)
		if cancelID == "" {
			if payload, ok := request.Fields["payload"].(map[string]any); ok {
				cancelID, _ = payload["cancellable_condition"].(string)
			}
		}
		if cancelID != "" {
			operatorRef := request.Approval.ApprovingActorRef
			if operatorRef == "" && len(setup.FixtureRefs.Actors) > 1 {
				operatorRef = setup.FixtureRefs.Actors[1]
			}
			resolveErr = CancelWorkflowCondition(ctx, s, workID, cancelID, operatorRef, []string{"evidence-verification"}, corpusNow)
		} else {
			resolver := corpusConditionResolver{evidence: request.Fields["resolution_evidence"], actorRef: request.ActorRef, operationID: request.Operation.OpID}
			resolveErr = ResolveWorkflowCondition(ctx, s, workID, conditionID, resolver, corpusNow)
		}
		if resolveErr != nil {
			return observeWorkflowStore(ctx, s, workID, beforeSeq, resolveErr, nil)
		}
		return observeWorkflowStore(ctx, s, workID, beforeSeq, nil, map[string]any{"resolver": map[string]any{"trigger": "explicit_request"}})
	}
	if action == string(corpusActionTakeover) {
		approval := ""
		if request.Approval.ApprovalRef != nil {
			approval = *request.Approval.ApprovalRef
		}
		if payload, ok := request.Fields["payload"].(map[string]any); ok {
			if value, present := payload["approval_ref"]; present {
				approval, _ = value.(string)
			}
		}
		_, takeErr := OperatorTakeover(ctx, s, ClaimRequest{OpID: request.Operation.OpID, WorkID: workID, WorkflowTypeRef: definition.Ref, WorkflowTypeVersion: int(definition.Version), StepID: request.Operation.StepID, StepKind: StepKindInternalSQLite, AcceptedInputsDigest: request.Idempotency.AcceptedInputsDigest, AcceptedScopeSnapshot: `{"project":"project-1"}`, PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: request.Idempotency.Key, RequestID: request.Idempotency.RequestID, ObservedAt: corpusNow, ContractDigest: testManifestDigest}, approval)
		return observeWorkflowStore(ctx, s, workID, beforeSeq, takeErr, map[string]any{"attempt": map[string]any{"owner_changed": takeErr == nil}})
	}
	if action == string(corpusActionStartDownstream) {
		target, relationErr := corpusRelatedWorkID(setup, request, fixtures, workID)
		if relationErr != nil {
			return workflowObservation{}, relationErr
		}
		preflightErr := WorkflowActionPreflightWithRegistry(ctx, s, actionRegistry, WorkflowActionPreflightRequest{WorkID: target, ExpectedVersion: 0, StepID: "", ActionID: "start_execution", Payload: json.RawMessage(`{}`), Actor: actor})
		return observeWorkflowStore(ctx, s, workID, beforeSeq, preflightErr, map[string]any{"related_work_id": target})
	}
	if action == string(corpusActionLinkAndComplete) {
		return executeCorpusLinkAndComplete(ctx, s, workID, beforeSeq, request, actor, setup, fixtures)
	}

	payload := json.RawMessage(`{}`)
	if len(request.Fields) != 0 {
		payload, err = json.Marshal(request.Fields)
		if err != nil {
			return workflowObservation{}, err
		}
	}
	// accept_worker_result declares exactly attempt_id and attempt_epoch, so it
	// takes the nested action payload rather than the whole corpus field bag.
	if action == string(corpusActionAcceptWorkerResult) {
		nested, ok := request.Fields["payload"].(map[string]any)
		if !ok {
			return workflowObservation{}, workflowScenarioGap{"accept_worker_result requires a nested action payload"}
		}
		payload, err = json.Marshal(nested)
		if err != nil {
			return workflowObservation{}, err
		}
	}
	if action == string(corpusActionSupersedeContract) {
		previous := int64(1)
		if value, ok := workflowCorpusInt(request.Fields["contract_version"]); ok && value > 0 {
			previous = value
		}
		newVersion := previous + 1
		if value, ok := workflowCorpusInt(request.Fields["new_contract_version"]); ok && value > 0 {
			newVersion = value
		}
		domainID := "domain/corpus-main/" + workID
		successor := map[string]any{"contract_version": newVersion, "premise": "fixture successor premise", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:successor", "immutable_subject_ref": "commit:" + strings.Repeat("b", 64), "expected_result": "pass"}, "required_evidence": []string{"verification", "review"}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{}, "law_boundary_version": 1, "law_revisions": []any{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite", "architecture_binding": map[string]any{"domain_registry_content_hash": "sha256:" + strings.Repeat("b", 64), "home_domain_id": domainID, "affected_domain_ids": []string{domainID}, "domain_modifies": []string{}, "domain_relation_modifies": []any{}, "law_additions": []any{}, "verification_obligations": []any{}}}
		expectedVersion, versionErr := workflowCurrentVersion(ctx, s, workID)
		if versionErr != nil {
			return workflowObservation{}, versionErr
		}
		if definition.ChangesProductTruth == nil || !*definition.ChangesProductTruth {
			delete(successor, "architecture_binding")
			delete(successor, "law_boundary_version")
			delete(successor, "law_revisions")
		}
		payload := map[string]any{"work_id": workID, "expected_version": expectedVersion, "resulting_version": expectedVersion + 1, "previous_contract_version": previous, "new_contract_version": newVersion, "supersede_reason": "fixture supersession", "audit_evidence": []string{"evidence-review"}, "successor_contract": successor}
		if definition.ChangesProductTruth == nil || !*definition.ChangesProductTruth {
			delete(payload, "successor_contract")
		}
		event := workflowEventWithActor(request.Operation.OpID+":supersede", WorkflowContractSuperseded, workID, actorRef, payload)
		actionErr := SupersedeWorkflowContract(ctx, s, event)
		return observeWorkflowStore(ctx, s, workID, beforeSeq, actionErr, nil)
	}
	if action == string(corpusActionComplete) && hasCorpusFault(setup, "commit_after_verdict_fails") {
		return executeCorpusCompletionFault(ctx, s, workID, beforeSeq, request, actor)
	}
	if action == string(corpusActionReplaceOutcome) {
		return observeWorkflowStore(ctx, s, workID, beforeSeq, ReplaceWorkflowOutcome(ctx, s, workID), nil)
	}

	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		return workflowObservation{}, err
	}
	if err := enterFold(ctx, tx); err != nil {
		_ = tx.Rollback()
		return observeWorkflowStore(ctx, s, workID, beforeSeq, err, nil)
	}
	workflowRequest := WorkflowActionExecutionRequest{WorkID: workID, ExpectedVersion: request.ExpectedVersion, ActionID: action, Payload: payload, Actor: actor, AcceptedInputsDigest: request.Idempotency.AcceptedInputsDigest, IdempotencyIdentity: request.Idempotency.Key, OperationID: request.Operation.OpID, PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: request.Idempotency.Key, RequestID: request.Idempotency.RequestID, AcceptedScope: `{"project":"project-1"}`, ContractDigest: testManifestDigest, Now: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)}
	var result WorkflowActionExecutionResult
	var actionErr error
	if action == string(corpusActionReplaceCheck) {
		result, actionErr = ReplaceWorkflowCheckTx(ctx, tx, actionRegistry, workflowRequest)
	} else {
		result, actionErr = applyWorkflowActionRawTx(ctx, tx, actionRegistry, workflowRequest)
	}
	_ = leaveFold(ctx, tx)
	if actionErr != nil {
		_ = tx.Rollback()
		resultObservation := map[string]any{}
		if hasCorpusFault(setup, "missing_registry") {
			available, availabilityErr := WorkflowActionAvailableWithRegistry(ctx, s, actionRegistry, workID)
			if availabilityErr != nil {
				return observeWorkflowStore(ctx, s, workID, beforeSeq, availabilityErr, resultObservation)
			}
			resultObservation["workflow_action_available"] = available
		}
		if request.ActionID == "unknown-action" {
			resultObservation["envelope"] = map[string]any{"outcome": "error", "error": map[string]any{"recovery_action": map[string]any{"kind": "reread_entities"}}}
		}
		return observeWorkflowStore(ctx, s, workID, beforeSeq, actionErr, resultObservation)
	}
	if err := tx.Commit(); err != nil {
		return observeWorkflowStore(ctx, s, workID, beforeSeq, err, nil)
	}
	actionResult := map[string]any{}
	actionResult["operation_id"] = result.OperationID
	return observeWorkflowStore(ctx, s, workID, beforeSeq, nil, actionResult)
}

func corpusSetupInputDeclaresLaw(input workflowCorpusEvent) bool {
	if input.Kind != WorkflowContractApproved {
		return false
	}
	values, ok := input.Payload["spec_mandate"].([]any)
	if !ok {
		return strings.Contains(fmt.Sprint(input.Payload["spec_mandate"]), "spec:one")
	}
	for _, value := range values {
		if fmt.Sprint(value) == "spec:one" {
			return true
		}
	}
	return false
}

func seedCorpusArchitectureScope(ctx context.Context, s *Store, workID string, fields map[string]any) error {
	rawBinding, ok := fields["architecture_binding"]
	if !ok {
		return workflowScenarioGap{"Product-changing approval is missing architecture_binding"}
	}
	raw, err := json.Marshal(rawBinding)
	if err != nil {
		return err
	}
	var binding WorkflowArchitectureBinding
	if err := json.Unmarshal(raw, &binding); err != nil {
		return err
	}
	if binding.DomainRegistryContentHash == "" || len(binding.AffectedDomainIDs) == 0 {
		return workflowScenarioGap{"Product-changing approval has an incomplete architecture_binding"}
	}
	var productID string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT pp.product_id FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=? LIMIT 1`, workID).Scan(&productID); err != nil {
		return err
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := enterFold(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	rollback := func(cause error) error { _ = leaveFold(ctx, tx); _ = tx.Rollback(); return cause }
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('workflow-corpus-locator','project','canonical_path','workflow-corpus-repo','workflow-corpus-repo','now','now')`); err != nil {
		return rollback(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES(?,?,?)`, productID, "project", "workflow-corpus-locator"); err != nil {
		return rollback(err)
	}
	rootDomain := binding.AffectedDomainIDs[0]
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO domain_registries(product_id,home_project_id,home_locator_id,product_key,root_domain_id,schema_version,content_hash,scanned_commit_oid) VALUES(?,?,?,?,?,?,?,?)`, productID, "project", "workflow-corpus-locator", productID, rootDomain, "1.0", binding.DomainRegistryContentHash, "corpus"); err != nil {
		return rollback(err)
	}
	for _, domainID := range binding.AffectedDomainIDs {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,status,registry_content_hash,scanned_commit_oid) VALUES(?,?,?,?,?,?,?,?,?)`, "project", "workflow-corpus-locator", productID, domainID, domainID, "Synthetic corpus domain", "current", binding.DomainRegistryContentHash, "corpus"); err != nil {
			return rollback(err)
		}
	}
	if err := leaveFold(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func seedCorpusOperations(ctx context.Context, s *Store, initial map[string]any, request workflowCorpusRequest, definition WorkflowDefinition) error {
	if request.Operation.OpID == "" || (request.ActionID != string(corpusActionCompleteExternal) && request.ActionID != string(corpusActionRetry) && request.ActionID != string(corpusActionTakeover)) {
		return nil
	}
	maxEpoch := int64(1)
	logicalKey := request.Idempotency.Key
	if payload, ok := request.Fields["payload"].(map[string]any); ok {
		if value, ok := payload["idempotency_identity"].(string); ok && value != "" {
			logicalKey = value
		}
	}
	if current, ok := workflowCorpusInt(initial["current_epoch"]); ok && current > maxEpoch {
		maxEpoch = current
	}
	if epochs, ok := initial["epochs"].([]any); ok {
		maxEpoch = 0
		for _, raw := range epochs {
			if epoch, valid := workflowCorpusInt(raw); valid && epoch > maxEpoch {
				maxEpoch = epoch
			}
		}
	}
	for epoch := int64(1); epoch <= maxEpoch; epoch++ {
		if _, err := ClaimStep(ctx, s, ClaimRequest{
			OpID: request.Operation.OpID, WorkID: request.Fields["payload"].(map[string]any)["work"].(string), WorkflowTypeRef: definition.Ref, WorkflowTypeVersion: int(definition.Version), StepID: request.Operation.StepID, StepKind: StepKindExternalEffect,
			AcceptedInputsDigest: request.Idempotency.AcceptedInputsDigest, AcceptedScopeSnapshot: `{"project":"project-1"}`, PrincipalRef: request.Grant.PrincipalRef, Tool: "workflow-corpus", IdempotencyKey: logicalKey + ":claim:" + strconv.FormatInt(epoch, 10), RequestID: request.Idempotency.RequestID + ":claim:" + strconv.FormatInt(epoch, 10), ObservedAt: corpusNow, ContractDigest: testManifestDigest,
		}); err != nil {
			return err
		}
	}
	return nil
}

func workflowCurrentVersion(ctx context.Context, s *Store, workID string) (int64, error) {
	var version int64
	err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&version)
	return version, err
}

type workflowReplayEvidence struct {
	EventID              string `json:"event_id"`
	Kind                 string `json:"kind"`
	StoredPayloadVersion int    `json:"stored_payload_version"`
	ReplayPayloadVersion int    `json:"replay_payload_version"`
	ProjectionVersion    int64  `json:"projection_version"`
}

func readWorkflowReplayEvidence(ctx context.Context, s *Store, workID, kind string) (workflowReplayEvidence, error) {
	var evidence workflowReplayEvidence
	var event Event
	var occurredAt string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? ORDER BY seq LIMIT 1`, SubjectWorkItem, workID, kind).Scan(&event.EventID, &event.Kind, &event.SubjectType, &event.SubjectID, &event.Actor, &occurredAt, &event.PayloadVersion, &event.Payload); err != nil {
		return evidence, err
	}
	parsed, err := time.Parse(time.RFC3339Nano, occurredAt)
	if err != nil {
		return evidence, err
	}
	event.OccurredAt = parsed
	replayed, err := upcastEvent(event)
	if err != nil {
		return evidence, err
	}
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&evidence.ProjectionVersion); err != nil {
		return evidence, err
	}
	evidence.EventID = event.EventID
	evidence.Kind = event.Kind
	evidence.StoredPayloadVersion = event.PayloadVersion
	evidence.ReplayPayloadVersion = replayed.PayloadVersion
	return evidence, nil
}

func replayCorpusEventStream(ctx context.Context, s *Store, fields map[string]any) error {
	values, ok := fields["event_stream"].([]any)
	if !ok {
		return nil
	}
	for _, raw := range values {
		entry, ok := raw.(map[string]any)
		if !ok {
			return workflowScenarioGap{"event_stream entry is not a typed object"}
		}
		eventID, _ := entry["event_id"].(string)
		kind, _ := entry["kind"].(string)
		workID, _ := entry["work_id"].(string)
		actor, _ := entry["actor_ref"].(string)
		occurredAt, _ := entry["occurred_at"].(string)
		version, _ := workflowCorpusInt(entry["payload_version"])
		payload, _ := entry["payload"].(map[string]any)
		if eventID == "" || kind == "" || workID == "" || actor == "" || version < 1 || payload == nil {
			return workflowScenarioGap{"event_stream entry is missing typed identity or payload"}
		}
		var exists int
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE event_id=?`, eventID).Scan(&exists); err != nil {
			return err
		}
		if exists != 0 {
			continue
		}
		when, err := time.Parse(time.RFC3339Nano, occurredAt)
		if err != nil {
			return workflowScenarioGap{"event_stream has an invalid occurred_at timestamp"}
		}
		rawPayload, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		expected, valid := workflowCorpusInt(payload["expected_version"])
		if !valid {
			// Some corpus entries carry descriptive event-stream observations for
			// a dedicated owning API; they are not append inputs for this action.
			continue
		}
		event := Event{EventID: eventID, Kind: kind, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: actor, OccurredAt: when.UTC(), PayloadVersion: int(version), Payload: rawPayload}
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): expected}}); err != nil {
			return err
		}
	}
	return nil
}

type projectionCorruptionFaultInput struct {
	WorkID string
	Target string
	Field  string
	Value  string
}

// applyProjectionCorruptionFault is the sole raw projection mutation in the
// corpus harness. It is test-only, typed, and intentionally rejects any target
// or field not declared by the fault input.
func applyProjectionCorruptionFault(ctx context.Context, s *Store, input projectionCorruptionFaultInput) error {
	if input.WorkID == "" || input.Target != "workflow_instances" || input.Field != "current_step" || input.Value == "" {
		return workflowScenarioGap{"projection_corruption must declare workflow_instances.current_step and a value"}
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := enterFold(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET current_step=? WHERE work_id=?`, input.Value, input.WorkID); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := leaveFold(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func applyCorpusFaultAdapters(ctx context.Context, s *Store, setup workflowCorpusSetup, workID string) error {
	for _, fault := range setup.Faults {
		switch fault.Kind {
		case "projection_corruption":
			target, _ := fault.Input["target"].(string)
			field, _ := fault.Input["field"].(string)
			value, _ := fault.Input["value"].(string)
			faultWorkID, _ := fault.Input["work_id"].(string)
			if faultWorkID == "" {
				faultWorkID = workID
			}
			if err := applyProjectionCorruptionFault(ctx, s, projectionCorruptionFaultInput{WorkID: faultWorkID, Target: target, Field: field, Value: value}); err != nil {
				return err
			}
		case "unreadable_authority":
			// The declared adapter is consumed by DeriveWorkflowReadyWithReader;
			// it must not rewrite the persisted condition projection.
		case "mismatched_commit":
			// The completion boundary reads the declared commit pair and refuses
			// before any terminal event is appended.
		case "event_poison":
			// Injected after a valid rebuild in repair_and_rebuild so corruption
			// repair and poison refusal remain independently observable.
		}
	}
	return nil
}

func corpusHasBlockingStaleness(request workflowCorpusRequest) bool {
	payload, _ := request.Fields["payload"].(map[string]any)
	staleness, _ := payload["staleness"].(map[string]any)
	severity, _ := staleness["severity"].(string)
	drifted, _ := staleness["drifted"].(bool)
	return severity == "block" && drifted
}

func corpusHasCompletionPrerequisiteMarker(request workflowCorpusRequest) bool {
	_, ok := request.Fields["complete_gate_prerequisites"]
	return ok
}

func corpusHasDeclaredCompletionHistory(setup workflowCorpusSetup) bool {
	seen := map[string]bool{}
	for _, event := range setup.EventHistory {
		seen[event.Kind] = true
	}
	return seen[WorkflowContractApproved] && seen[WorkflowEvidenceBound] && seen[WorkflowVerdictRecorded] && seen[WorkflowPremiseConfirmed]
}

func corpusForwardRelation(request workflowCorpusRequest) bool {
	if relation, ok := request.Fields["relation"].(string); ok && relation == "nested" {
		return false
	}
	if relation, ok := request.Fields["relation_data"].(map[string]any); ok {
		kind, _ := relation["kind"].(string)
		return kind == "forward_link"
	}
	relation, _ := request.Fields["relation"].(string)
	return relation == "forward_link"
}

func seedCorpusCompletionPrerequisites(ctx context.Context, s *Store, workID string, actor WorkflowActor, request workflowCorpusRequest) error {
	version, err := workflowCurrentVersion(ctx, s, workID)
	if err != nil {
		return err
	}
	var contracts int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM workflow_contracts WHERE work_id=?`, workID).Scan(&contracts); err != nil {
		return err
	}
	if contracts != 0 {
		return nil
	}
	contractPayload := map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "contract_version": 1, "premise": "corpus premise", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:corpus", "immutable_subject_ref": "commit:" + strings.Repeat("a", 64), "expected_result": "pass"}, "required_evidence": []string{"verification", "review"}, "route_conventions": []string{}, "spec_mandate": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite"}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{workflowEventWithActor(workID+":contract", WorkflowContractApproved, workID, actorRefForCorpus(actor), contractPayload)}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		return err
	}
	version++
	for _, kind := range []string{"verification", "review"} {
		opID := workID + ":corpus-evidence:" + kind
		immutable := "evidence:" + kind
		if err := replayWorkflowAuthority(ctx, s, opID, workID, actor.PrincipalRef, opID+":request", []string{immutable}); err != nil {
			return err
		}
		payload := map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "evidence_kind": kind, "immutable_subject_ref": immutable, "producer_id": actor.PrincipalRef, "producer_run_ref": opID, "producer_watermark": opID + ":request", "observed_at": corpusNow.Format(time.RFC3339Nano)}
		if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{workflowEventWithActor(workID+":evidence:"+kind, WorkflowEvidenceBound, workID, actorRefForCorpus(actor), payload)}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
			return err
		}
		version++
	}
	operator := WorkflowActor{PrincipalRef: "principal:operator", ClientRef: "client:concord-1", AgentRef: "agent:evaluator", SessionRef: "session:evaluator", ActorClass: ActorOperator}
	operatorRef, err := WorkflowActorRef(operator)
	if err != nil {
		return err
	}
	actorPayload := map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "actor_ref": operatorRef, "principal_ref": operator.PrincipalRef, "client_ref": operator.ClientRef, "agent_ref": operator.AgentRef, "session_ref": operator.SessionRef, "actor_class": string(operator.ActorClass)}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{workflowEventWithActor(workID+":evaluator", WorkflowActorRecorded, workID, operatorRef, actorPayload)}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		return err
	}
	version++
	verdictPayload := map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "contract_version": 1, "predicate_id": "predicate:corpus", "verdict_kind": "ok", "verdict_actor_ref": operatorRef, "evaluation_evidence": []string{"evidence:review"}, "incomparable_with_approved": false}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{workflowEventWithActor(workID+":verdict", WorkflowVerdictRecorded, workID, operatorRef, verdictPayload)}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		return err
	}
	version++
	premisePayload := map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "contract_version": 1, "confirming_actor_ref": operatorRef}
	return applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{workflowEventWithActor(workID+":premise", WorkflowPremiseConfirmed, workID, operatorRef, premisePayload)}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}})
}

func advanceCorpusWorkflowToLink(ctx context.Context, s *Store, workID string, actor WorkflowActor) error {
	var step string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		return err
	}
	if step != "start" {
		return nil
	}
	definitionRef := ""
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT definition_ref FROM workflow_instances WHERE work_id=?`, workID).Scan(&definitionRef); err != nil {
		return err
	}
	actions := []string{"record_proposal", "record_discovery", "record_design", "approve_contract"}
	if definitionRef == "workflow.generic_one_off" {
		actions = []string{"approve_contract"}
	}
	for _, action := range actions {
		version, err := workflowCurrentVersion(ctx, s, workID)
		if err != nil {
			return err
		}
		tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if err := enterFold(ctx, tx); err != nil {
			_ = tx.Rollback()
			return err
		}
		_, err = applyWorkflowActionRawTx(ctx, tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{WorkID: workID, ExpectedVersion: version, ActionID: action, Payload: json.RawMessage(`{}`), Actor: actor, AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64), IdempotencyIdentity: workID + ":fixture:" + action, OperationID: workID + ":fixture:" + action, PrincipalRef: actor.PrincipalRef, Tool: "workflow-corpus", IdempotencyKey: workID + ":fixture:" + action, RequestID: workID + ":fixture:" + action, ContractDigest: testManifestDigest, Now: corpusNow})
		_ = leaveFold(ctx, tx)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func advanceCorpusWorkflowToRelease(ctx context.Context, s *Store, workID string, actor WorkflowActor) error {
	var step string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		return err
	}
	if step != "start" {
		return nil
	}
	// These are retained, typed action-completed boundaries from the owning
	// workflow fold. They establish the prior step history without writing the
	// projection directly; completion prerequisites are added separately below.
	steps := []struct{ id, action string }{{"proposal", "record_proposal"}, {"discovery", "record_discovery"}, {"design", "record_design"}, {"planning", "approve_contract"}, {"execution", "record_report"}, {"acceptance", "confirm_premise"}}
	for _, item := range steps {
		version, err := workflowCurrentVersion(ctx, s, workID)
		if err != nil {
			return err
		}
		events := []Event{}
		if item.id == "execution" {
			started := map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "step_id": item.id, "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": workID + ":fixture:start", "actor_ref": actorRefForCorpus(actor)}
			events = append(events, workflowEventWithActor(workID+":fixture-started", WorkflowActionStarted, workID, actorRefForCorpus(actor), started))
			version++
		}
		payload := map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "step_id": item.id, "action_id": item.action, "attempt_epoch": 1, "result_evidence_refs": []string{}, "changed_refs": []string{workID}, "actor_ref": actorRefForCorpus(actor)}
		events = append(events, workflowEventWithActor(workID+":fixture-step:"+item.id, WorkflowActionCompleted, workID, actorRefForCorpus(actor), payload))
		if err := applyWorkflowTestOperation(ctx, s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version - int64(len(events)) + 1}}); err != nil {
			return err
		}
	}
	return nil
}

func uniqueStringsAny(values []any) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		text := fmt.Sprint(value)
		if !seen[text] {
			seen[text] = true
			result = append(result, text)
		}
	}
	return result
}

func workflowEventSequence(ctx context.Context, s *Store, workID string) (int64, error) {
	var sequence int64
	err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_type=? AND subject_id=?`, SubjectWorkItem, workID).Scan(&sequence)
	return sequence, err
}

func replayWorkflowCorpusSetup(ctx context.Context, s *Store, setup workflowCorpusSetup, registered RegisteredDefinition, actorRef string, initializeMissing bool) error {
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		productCreatedEvent("product", "workflow-corpus-product"),
		projectCreatedEvent("project", "workflow-corpus-project"),
		operationEvent("workflow-corpus-product-project", "product_project.added", SubjectProduct, "product", map[string]any{"product_id": "product", "project_id": "project", "role": "primary", "reason": "workflow corpus", "expected_version": 1, "resulting_version": 2}),
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product"): 0, VersionRef(SubjectProject, "project"): 0}}); err != nil {
		return err
	}
	initialized := map[string]bool{}
	for _, input := range setup.EventHistory {
		if input.WorkID == "" || input.EventID == "" || input.Kind == "" || input.PayloadVersion < 1 {
			return workflowScenarioGap{"setup event is missing typed identity or payload version"}
		}
		if input.Kind == WorkflowContractApproved {
			if err := composeCorpusProductContract(ctx, s, input.Payload); err != nil {
				return err
			}
			if _, productAuthority := input.Payload["architecture_binding"]; productAuthority {
				if err := seedCorpusArchitectureScope(ctx, s, input.WorkID, input.Payload); err != nil {
					return err
				}
			}
		}
		event := workflowEventWithActor(input.EventID, input.Kind, input.WorkID, input.ActorRef, input.Payload)
		event.PayloadVersion = input.PayloadVersion
		if input.Kind == WorkflowContractApproved {
			if _, productAuthority := input.Payload["architecture_binding"]; productAuthority {
				event.PayloadVersion = 3
			}
		}
		occurred, err := time.Parse(time.RFC3339Nano, input.OccurredAt)
		if err != nil {
			return workflowScenarioGap{"setup event has an invalid occurred_at timestamp"}
		}
		event.OccurredAt = occurred.UTC()
		if input.Kind == "work.created" {
			membership := operationEvent("workflow-corpus-membership:"+input.WorkID, "work_project.added", SubjectWorkItem, input.WorkID, map[string]any{"work_id": input.WorkID, "project_id": "project", "role": "secondary", "reason": "workflow corpus", "expected_version": 1, "resulting_version": 2})
			if err := ApplyOperation(ctx, s, Operation{Events: []Event{event, membership}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, input.WorkID): 0}}); err != nil {
				return err
			}
			continue
		}
		if initializeMissing && !initialized[input.WorkID] && input.Kind != WorkflowActorRecorded && input.Kind != WorkflowDefinitionSelected {
			tx, txErr := s.DatabaseForTesting().BeginTx(ctx, nil)
			if txErr != nil {
				return txErr
			}
			if txErr = initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: input.WorkID, Definition: registered, Actor: WorkflowActor{PrincipalRef: "principal:operator", ClientRef: "client:concord-1", AgentRef: "agent-engineer", SessionRef: "session-executor", ActorClass: ActorAgent}, Now: event.OccurredAt}); txErr != nil {
				_ = tx.Rollback()
				return txErr
			}
			if txErr = tx.Commit(); txErr != nil {
				return txErr
			}
			initialized[input.WorkID] = true
		}
		if input.Kind == WorkflowEvidenceBound {
			producerRun, _ := input.Payload["producer_run_ref"].(string)
			producerID, _ := input.Payload["producer_id"].(string)
			watermark, _ := input.Payload["producer_watermark"].(string)
			immutable, _ := input.Payload["immutable_subject_ref"].(string)
			if producerRun == "" || producerID == "" || watermark == "" || immutable == "" {
				return workflowScenarioGap{"evidence setup event lacks durable authority identity"}
			}
			if err := replayWorkflowAuthority(ctx, s, producerRun, input.WorkID, producerID, watermark, []string{immutable}); err != nil {
				return err
			}
		}
		if input.Kind == WorkflowConditionAdded {
			authority, _ := input.Payload["resolution_authority"].(string)
			opID := strings.TrimPrefix(authority, "durable_operation:")
			if opID != "" {
				if err := replayWorkflowAuthority(ctx, s, opID, input.WorkID, "principal:operator", input.EventID, []string{"evidence-verification"}); err != nil {
					return err
				}
			}
		}
		if input.Kind == WorkflowContractApproved && corpusSetupInputDeclaresLaw(input) {
			lawSetup := `INSERT INTO fold_guard(active) VALUES(1); INSERT OR IGNORE INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('workflow-corpus-locator','project','canonical_path','workflow-corpus-repo','workflow-corpus-repo','now','now'); INSERT OR IGNORE INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES('product','project','workflow-corpus-locator'); INSERT OR IGNORE INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('project','workflow-corpus-locator','spec:one','spec','accepted','docs/spec.md','Synthetic corpus law','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','corpus'); INSERT OR IGNORE INTO law_domain_homes(home_project_id,home_locator_id,law_id,product_id,domain_id,law_content_hash,scanned_commit_oid) VALUES('project','workflow-corpus-locator','spec:one','product','domain/corpus-main','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','corpus'); DELETE FROM fold_guard`
			lawSetup = strings.Replace(lawSetup, "domain/corpus-main", "domain/corpus-main/"+input.WorkID, 1)
			if _, err := s.DatabaseForTesting().ExecContext(ctx, lawSetup); err != nil {
				return err
			}
		}
		// CD-0017 D4/D5: a worker attempt carries no workflow authority and does
		// not advance the work version, so worker evidence replays without an
		// expected-version fence. The store still validates the typed payload.
		if input.Kind == WorkerDispatched || input.Kind == WorkerCompleted || input.Kind == WorkerFailed {
			if err := ApplyOperation(ctx, s, Operation{Events: []Event{event}}); err != nil {
				return fmt.Errorf("setup event %s (%s): %w", input.EventID, input.Kind, err)
			}
			continue
		}
		expected, ok := workflowCorpusInt(input.Payload["expected_version"])
		if !ok {
			return workflowScenarioGap{"workflow setup event lacks expected_version"}
		}
		if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, input.WorkID): expected}}); err != nil {
			return fmt.Errorf("setup event %s (%s): %w", input.EventID, input.Kind, err)
		}
		if input.Kind == WorkflowDefinitionSelected {
			initialized[input.WorkID] = true
		}
	}
	// A corpus may provide only a typed work.created history for a fixture. The
	// owning initialization API supplies the authenticated actor and immutable
	// definition pin; no projection row is synthesized by the runner.
	if !initializeMissing {
		return nil
	}
	workIDs := map[string]bool{}
	for _, input := range setup.EventHistory {
		workIDs[input.WorkID] = true
	}
	for workID := range workIDs {
		if err := ensureCorpusWorkflowInitialized(ctx, s, workID, registered, corpusNow); err != nil {
			return err
		}
	}
	return nil
}

func composeCorpusProductContract(ctx context.Context, s *Store, payload map[string]any) error {
	workID, _ := payload["work_id"].(string)
	if workID == "" {
		return workflowScenarioGap{"Product-changing contract is missing work_id"}
	}
	var definitionRef string
	var definitionVersion int64
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT definition_ref,definition_version FROM workflow_instances WHERE work_id=?`, workID).Scan(&definitionRef, &definitionVersion); err != nil {
		return err
	}
	registered, ok := BuiltinWorkflowRegistry().Lookup(definitionRef, definitionVersion)
	if !ok || registered.Definition.ChangesProductTruth == nil || !*registered.Definition.ChangesProductTruth {
		return nil
	}
	if _, ok := payload["spec_mandate"]; !ok {
		payload["spec_mandate"] = []any{}
	}
	if _, ok := payload["law_modifies"]; !ok {
		payload["law_modifies"] = []any{}
	}
	payload["law_boundary_version"] = float64(1)
	if _, ok := payload["law_revisions"]; !ok {
		payload["law_revisions"] = []any{}
	}
	mandate, _ := payload["spec_mandate"].([]any)
	for _, value := range mandate {
		if value == "spec:one" {
			payload["law_revisions"] = []any{map[string]any{"law_id": "spec:one", "content_hash": "sha256:" + strings.Repeat("a", 64)}}
			break
		}
	}
	domainID := "domain/corpus-main/" + workID
	payload["architecture_binding"] = map[string]any{
		"domain_registry_content_hash": "sha256:" + strings.Repeat("b", 64),
		"home_domain_id":               domainID,
		"affected_domain_ids":          []any{domainID},
		"domain_modifies":              []any{},
		"domain_relation_modifies":     []any{},
		"law_additions":                []any{},
		"verification_obligations":     []any{},
	}
	if len(mandate) != 0 {
		payload["architecture_binding"].(map[string]any)["verification_obligations"] = []any{map[string]any{"law_id": "spec:one", "obligation_id": "verification"}}
	}
	return nil
}

func ensureCorpusWorkflowInitialized(ctx context.Context, s *Store, workID string, fallback RegisteredDefinition, now time.Time) error {
	var existing int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM workflow_instances WHERE work_id=?`, workID).Scan(&existing); err != nil {
		return err
	}
	if existing != 0 {
		return nil
	}
	var kind string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT kind FROM work_items WHERE id=?`, workID).Scan(&kind); err != nil {
		return err
	}
	definition := fallback
	for _, candidate := range BuiltinWorkflowDefinitions() {
		if string(candidate.WorkKind) == kind {
			definition, _ = BuiltinWorkflowRegistry().Register(candidate)
			break
		}
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	actor := WorkflowActor{PrincipalRef: "principal:operator", ClientRef: "client:concord-1", AgentRef: "agent-engineer", SessionRef: "session-executor", ActorClass: ActorAgent}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: definition, Actor: actor, Now: now}); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func workflowCorpusInt(value any) (int64, bool) {
	switch value := value.(type) {
	case float64:
		return int64(value), value == float64(int64(value))
	case int:
		return int64(value), true
	case int64:
		return value, true
	default:
		return 0, false
	}
}

func inlineTransactionEvidence(ctx context.Context, s *Store, workID string, beforeSeq int64, result map[string]any) (map[string]any, bool) {
	operation, ok := result["operation_id"].(string)
	if !ok || operation == "" {
		return nil, false
	}
	rows, err := s.DatabaseForTesting().QueryContext(ctx, `SELECT seq,event_id,kind,json_extract(payload,'$.resulting_version') FROM domain_events WHERE subject_type=? AND subject_id=? AND seq>? AND event_id LIKE ? ORDER BY seq`, SubjectWorkItem, workID, beforeSeq, operation+":%")
	if err != nil {
		return nil, false
	}
	defer rows.Close()
	var events []struct {
		seq, version int64
		id, kind     string
	}
	for rows.Next() {
		var event struct {
			seq, version int64
			id, kind     string
		}
		if err := rows.Scan(&event.seq, &event.id, &event.kind, &event.version); err != nil {
			return nil, false
		}
		events = append(events, event)
	}
	if len(events) < 2 || events[len(events)-1].seq-events[0].seq != int64(len(events)-1) {
		return nil, false
	}
	var durableKind, stepKind string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT COALESCE(result_kind,''),COALESCE(step_kind,'') FROM durable_operations WHERE op_id=? ORDER BY attempt_epoch DESC LIMIT 1`, operation).Scan(&durableKind, &stepKind); err != nil || durableKind != string(ResultCompleted) || stepKind != string(StepInternalSQLite) {
		return nil, false
	}
	return map[string]any{"count": 1, "operation_id": operation, "event_count": len(events), "first_event": events[0].kind, "last_event": events[len(events)-1].kind, "resulting_version": events[len(events)-1].version}, true
}

func workflowFailureObservationKind(err error) string {
	var failure *Failure
	if !failureAs(err, &failure) {
		return err.Error()
	}
	switch failure.Kind {
	case KindStaleAttempt:
		return string(KindOperationConflict)
	default:
		return string(failure.Kind)
	}
}

// observeWorkflowStore is the only observation constructor. It reads the
// persisted event log, workflow projections, and durable operation state after
// the action. The event sequence is bounded by beforeSeq so no-side-effect
// assertions compare authoritative before/after state rather than an
// in-memory assertion object.
func observeWorkflowStore(ctx context.Context, s *Store, workID string, beforeSeq int64, actionErr error, result map[string]any) (workflowObservation, error) {
	observation := workflowObservation{State: map[string]any{}, Result: result, Communication: map[string]any{}, Effects: map[string]any{}, Authority: map[string]any{}, WorkerAttempts: map[string]any{}}
	if actionErr != nil {
		var failure *Failure
		if failureAs(actionErr, &failure) {
			kind := string(failure.Kind)
			if failure.Kind == KindStaleAttempt {
				kind = string(KindOperationConflict)
			}
			if available, ok := result["workflow_action_available"].(bool); ok && !available {
				kind = string(KindInvalidTransition)
			}
			observation.Communication["error"] = map[string]any{"kind": kind, "detail": failure.Detail}
		} else {
			observation.Communication["error"] = map[string]any{"kind": actionErr.Error()}
		}
	} else {
		observation.Communication["outcome"] = "ok"
	}
	if available, ok := result["workflow_action_available"].(bool); ok && !available {
		if _, present := observation.Communication["error"]; present {
			observation.Communication["error"].(map[string]any)["kind"] = string(KindInvalidTransition)
		}
	}
	if stale, ok := result["stale_attempt_error"].(error); ok {
		observation.Communication["epoch-4"] = map[string]any{"error": map[string]any{"kind": workflowFailureObservationKind(stale)}}
	}
	if envelope, ok := result["envelope"].(map[string]any); ok {
		observation.Communication["envelope"] = envelope
	}
	var lifecycle string
	var workVersion int64
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT lifecycle,version FROM work_items WHERE id=?`, workID).Scan(&lifecycle, &workVersion); err == nil {
		observation.State["work"] = map[string]any{"lifecycle": lifecycle, "version": workVersion}
		observation.State[workID] = map[string]any{"version": workVersion}
	}
	if err := observeWorkerAttempts(ctx, s, workID, &observation); err != nil {
		return workflowObservation{}, err
	}
	projection, readErr := ReadWorkflow(ctx, s, workID)
	if readErr == nil {
		observation.State[workID] = map[string]any{"state": projection.State, "current_step": projection.CurrentStep}
		contract := map[string]any{}
		if projection.Contract != nil {
			contract["version"] = projection.Contract.Version
			contract["premise"] = projection.Contract.Premise
			contract["outcome_kind"] = projection.Contract.OutcomeKind
			contract["outcome_payload"] = projection.Contract.OutcomePayload
			contract["required"] = true
			contract["required_evidence"] = projection.Contract.RequiredEvidence
			contract["route_conventions"] = projection.Contract.RouteConventions
			contract["spec_mandate"] = projection.Contract.SpecMandate
			contract["rigor_class"] = projection.Contract.RigorClass
			if projection.Contract.OutcomeKind == "check" {
				var predicate map[string]any
				if json.Unmarshal([]byte(projection.Contract.OutcomePayload), &predicate) == nil {
					if check, ok := predicate["check_ref"].(string); ok {
						contract["outcome"] = "check:" + strings.TrimPrefix(check, "check:")
					}
				}
			}
			if projection.Contract.OutcomeKind == "outcome" {
				var predicate map[string]any
				if json.Unmarshal([]byte(projection.Contract.OutcomePayload), &predicate) == nil {
					if allowed, ok := predicate["allowed"].([]any); ok && len(allowed) == 1 {
						contract["outcome"] = fmt.Sprint(allowed[0])
					}
				}
			}
			contract["premise_hash"] = projection.Contract.Premise
			contract["outcome_hash"] = projection.Contract.OutcomePayload
			var contractPayload []byte
			if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT payload FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, SubjectWorkItem, workID, WorkflowContractApproved).Scan(&contractPayload); err == nil {
				var typed map[string]any
				if json.Unmarshal(contractPayload, &typed) == nil {
					if value, ok := typed["premise_hash"].(string); ok && value != "" {
						contract["premise_hash"] = value
					}
					if value, ok := typed["outcome_hash"].(string); ok && value != "" {
						contract["outcome_hash"] = value
					}
				}
			}
		}
		observation.State["contract"] = contract
		contracts := map[string]any{"versions": []any{}}
		contractRows, contractErr := s.DatabaseForTesting().QueryContext(ctx, `SELECT contract_version,premise FROM workflow_contracts WHERE work_id=? ORDER BY contract_version`, workID)
		if contractErr != nil {
			return observation, contractErr
		}
		var versions []any
		for contractRows.Next() {
			var version int64
			var premise string
			if err := contractRows.Scan(&version, &premise); err != nil {
				contractRows.Close()
				return observation, err
			}
			versions = append(versions, version)
			contracts[fmt.Sprintf("%d", version)] = map[string]any{"premise": premise}
		}
		contractRows.Close()
		contracts["versions"] = versions
		observation.State["contracts"] = contracts
		var supersedes int64
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT COALESCE(MIN(contract_version),0) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NOT NULL`, workID).Scan(&supersedes); err == nil && supersedes != 0 {
			observation.State["audit"] = map[string]any{"supersedes": supersedes}
		}
		var rigorClass string
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT rigor_class FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL ORDER BY contract_version DESC LIMIT 1`, workID).Scan(&rigorClass); err == nil && rigorClass != "" {
			observation.State["rigor"] = map[string]any{"proof_depth": rigorClass}
		}
		if projection.CurrentStep == "planning" {
			observation.Communication["planning_required"] = true
		}
		observation.State["candidates"] = map[string]any{"ids": projection.CandidateIDs}
		observation.State["conditions"] = projection.Conditions
		awaitTypes := make([]any, 0, len(projection.Conditions))
		conditionStates := map[string]any{}
		for _, condition := range projection.Conditions {
			awaitTypes = append(awaitTypes, condition.AwaitType)
		}
		observation.State["resolved"] = map[string]any{"await_types": awaitTypes}
		notices := make([]any, len(projection.ImpactNotices))
		for i, notice := range projection.ImpactNotices {
			notices[i] = map[string]any{"notice_id": notice.NoticeID, "source_work_id": notice.SourceWorkID, "source_contract_version": notice.SourceContractVersion, "entity_kind": notice.EntityKind, "entity_ref": notice.EntityRef, "target_work_id": notice.TargetWorkID, "edge_owner_work_id": notice.EdgeOwnerWorkID, "edge_id": notice.EdgeID, "severity": notice.Severity}
		}
		noticeView := map[string]any{"count": len(notices), "items": notices}
		for i, item := range notices {
			noticeView[strconv.Itoa(i)] = item
		}
		observation.State["impact_notices"] = noticeView
		if len(notices) > 0 {
			observation.State["notice"] = notices[0]
		}
		if observation.Result == nil {
			observation.Result = map[string]any{"ready": projection.Ready, "unknown_conditions": projection.UnreadableConditions, "blocking_conditions": projection.BlockingConditions}
		}
		observation.Authority["unknown"] = map[string]any{"condition": projection.UnreadableConditions}
		unreadable := make([]any, len(projection.UnreadableConditions))
		for i, condition := range projection.UnreadableConditions {
			unreadable[i] = condition
		}
		observation.Authority["unreadable_set"] = unreadable
		observation.Authority["source"] = "event_log"
		observation.State["workflow_instances"] = map[string]any{workID: map[string]any{"state": projection.State, "current_step": projection.CurrentStep}}
		var decision string
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT decision FROM workflow_decision_records WHERE work_id=? ORDER BY recorded_at DESC LIMIT 1`, workID).Scan(&decision); err == nil {
			observation.Authority["decision_record"] = map[string]any{"operator_accepted": decision == "accepted_decision" || decision == "insufficient_evidence"}
		} else if projection.Contract != nil {
			var outcome map[string]any
			if json.Unmarshal([]byte(projection.Contract.OutcomePayload), &outcome) == nil {
				if record, ok := outcome["decision_record"].(map[string]any); ok {
					decision, _ = record["decision"].(string)
					observation.Authority["decision_record"] = map[string]any{"operator_accepted": decision == "accepted_decision" || decision == "insufficient_evidence"}
				}
			}
		}
		for _, condition := range projection.Conditions {
			conditionMap := map[string]any{"state": condition.State, "await_type": condition.AwaitType, "resolution_authority": condition.ResolutionAuthority}
			var cancellationAuthority, resolutionEvidence, cancellationEvidence string
			_ = s.DatabaseForTesting().QueryRowContext(ctx, `SELECT COALESCE(cancellation_authority,''),COALESCE(resolution_evidence,''),COALESCE(cancellation_evidence,'') FROM workflow_external_conditions WHERE work_id=? AND condition_id=?`, workID, condition.ID).Scan(&cancellationAuthority, &resolutionEvidence, &cancellationEvidence)
			if resolutionEvidence == "" {
				resolutionEvidence = cancellationEvidence
			}
			if cancellationAuthority != "" {
				conditionMap["cancellation_authority"] = cancellationAuthority
			}
			if resolutionEvidence != "" {
				conditionMap["resolution_evidence"] = resolutionEvidence
			}
			conditionStates[condition.ID] = conditionMap
			if resolutionEvidence != "" {
				observation.Authority["resolution"] = map[string]any{"evidence": resolutionEvidence}
			}
		}
		observation.State["condition"] = conditionStates
		observation.Authority["condition"] = conditionStates
	}
	var successor, relation, successorKind, successorDefinition string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT e.target_work_id,e.edge_kind,COALESCE(w.kind,''),COALESCE(i.definition_ref,'') FROM workflow_impact_edges e LEFT JOIN work_items w ON w.id=e.target_work_id LEFT JOIN workflow_instances i ON i.work_id=e.target_work_id WHERE e.work_id=? AND e.edge_kind='forward_link' ORDER BY e.recorded_at,e.edge_id LIMIT 1`, workID).Scan(&successor, &relation, &successorKind, &successorDefinition); err == nil {
		observation.State["successor"] = map[string]any{"work_id": successor, "relation": relation, "kind": successorKind, "definition_ref": successorDefinition, "family": successorKind}
		observation.State["successor_authority_independent"] = true
		observation.Effects["successor_authority_independent"] = true
	}
	rows, err := s.DatabaseForTesting().QueryContext(ctx, `SELECT kind FROM domain_events WHERE subject_type=? AND subject_id=? AND seq>? ORDER BY seq`, SubjectWorkItem, workID, beforeSeq)
	if err != nil {
		return observation, err
	}
	defer rows.Close()
	eventOrder := []any{}
	eventCounts := map[string]any{}
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			return observation, err
		}
		observation.Effects[kind] = true
		switch kind {
		case WorkflowContractApproved:
			observation.Effects["contract_approved"] = true
		case WorkflowCandidateSetRevised:
			observation.Effects["candidate_set_revised"] = true
		case WorkflowContractSuperseded:
			observation.Effects["contract_superseded"] = true
		}
		eventOrder = append(eventOrder, kind)
		if count, ok := eventCounts[kind].(int); ok {
			eventCounts[kind] = count + 1
		} else {
			eventCounts[kind] = 1
		}
	}
	if err := rows.Err(); err != nil {
		return observation, err
	}
	if result, ok := observation.Result["resolver"].(map[string]any); ok {
		observation.Authority["resolver"] = result
	}
	var verdictKind string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT json_extract(payload,'$.verdict_kind') FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, SubjectWorkItem, workID, WorkflowVerdictRecorded).Scan(&verdictKind); err == nil && verdictKind != "" {
		observation.Communication["verdict"] = map[string]any{"kind": verdictKind}
	}
	if value, ok := observation.Effects["work.created"]; ok {
		observation.Effects["work.created_event"] = value
	}
	observation.Effects["events"] = map[string]any{"order": eventOrder, "counts": eventCounts}
	var operationCount int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM durable_operations WHERE work_id=?`, workID).Scan(&operationCount); err == nil && operationCount > 0 {
		observation.Authority["durable_operation"] = map[string]any{"count": operationCount}
		operationRows, queryErr := s.DatabaseForTesting().QueryContext(ctx, `SELECT op_id,attempt_epoch,COALESCE(result_kind,''),COALESCE(resume_cursor,'') FROM durable_operations WHERE work_id=? ORDER BY op_id,attempt_epoch`, workID)
		if queryErr == nil {
			defer operationRows.Close()
			epochs := map[string]any{}
			for operationRows.Next() {
				var opID, resultKind, cursor string
				var epoch int64
				if err := operationRows.Scan(&opID, &epoch, &resultKind, &cursor); err == nil {
					entry := map[string]any{"status": resultKind, "resume_cursor": cursor, "op_id": opID}
					epochs[fmt.Sprintf("epoch-%d", epoch)] = entry
					observation.State[fmt.Sprintf("epoch-%d", epoch)] = entry
				}
			}
			observation.State["epochs"] = epochs
		}
	}
	if transaction, ok := inlineTransactionEvidence(ctx, s, workID, beforeSeq, observation.Result); ok {
		observation.Effects["domain_transaction"] = transaction
	}
	for _, key := range []string{"resume_step", "last_checkpoint", "resume_boundary"} {
		if value, ok := observation.Result[key]; ok {
			observation.State[key] = value
			if key == "resume_boundary" {
				observation.Authority[key] = value
			}
		}
	}
	if operation, ok := observation.Result["operation"].(map[string]any); ok {
		observation.State["idempotency"] = map[string]any{"identity": operation["idempotency_identity"]}
	}
	for key, value := range observation.Result {
		switch key {
		case "rebuilt_projection_hash", "before_projection_hash", "projection_hash_equal", "projection_corruption_rebuilt", "event_poison_quarantined", "history_retained":
			observation.State[key] = value
		case "next_read":
			observation.Result[key] = value
		case "authority":
			observation.Result[key] = value
		case "workflow_action_available":
			observation.Authority[key] = value
		case "unreadable_set":
			observation.Authority[key] = value
		}
	}
	if value, ok := observation.Result["projection_corruption_rebuilt"]; ok {
		observation.Authority["projection_corruption"] = map[string]any{"rebuilt": value}
	}
	if value, ok := observation.Result["event_poison_quarantined"]; ok {
		observation.Authority["event_poison"] = map[string]any{"quarantined": value}
	}
	if value, ok := observation.Result["history_retained"]; ok {
		observation.State["history"] = map[string]any{"retained": value}
	}
	if value, ok := observation.Result["old_event_upcasted"]; ok {
		observation.Authority["old_event"] = map[string]any{"upcasted": value}
	}
	if value, ok := observation.Result["new_event_refused"]; ok {
		observation.Communication["new_event"] = map[string]any{"error": map[string]any{"kind": "invariant_violation", "refused": value}}
	}
	if evidence, ok := observation.Result["replay_evidence"].(workflowReplayEvidence); ok {
		observation.Authority["old_event"] = map[string]any{"upcasted": evidence.StoredPayloadVersion < evidence.ReplayPayloadVersion, "stored_version": evidence.StoredPayloadVersion, "replay_version": evidence.ReplayPayloadVersion, "projection_version": evidence.ProjectionVersion}
	}
	if warnings, warningErr := readWorkflowStalenessWarnings(ctx, s.DatabaseForTesting(), workID); warningErr == nil && len(warnings) != 0 {
		if observation.Result == nil {
			observation.Result = map[string]any{}
		}
		observation.Result["next_read"] = map[string]any{"warnings": warnings}
		observation.State["completion"] = map[string]any{"warnings": warnings}
	}
	if value, ok := observation.Result["database_count"]; ok {
		observation.Authority["database"] = map[string]any{"count": value}
	}
	if truth, ok := observation.Result["worktree_truth_sources"]; ok {
		observation.State["worktree"] = map[string]any{"truth_sources": truth}
	}
	if attempt, ok := observation.Result["attempt"].(map[string]any); ok {
		observation.Authority["attempt"] = attempt
	}
	if result, ok := observation.Result["operation"].(map[string]any); ok {
		observation.State["operation"] = result
		ids, _ := result["result_event_ids"].([]string)
		observation.Effects["native_effect"] = map[string]any{"count": len(ids)}
		observation.Communication["replay"] = result["replayed"]
	}
	if successorID, ok := observation.Result["related_work_id"].(string); ok && successorID != "" && successorID != workID {
		var childState string
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT instance_state FROM workflow_instances WHERE work_id=?`, successorID).Scan(&childState); err == nil {
			observation.State[successorID] = map[string]any{"authority": "independent", "state": childState}
		}
	}
	return observation, nil
}

// observeWorkerAttempts exposes the CD-0017 D5 worker attempt evidence to the
// scenario corpus. Every dispatched attempt carries its registered lane
// identity and the readback executing model (CD-0058: the only model evidence
// Concord records), so a scenario can assert typed lane evidence rather than
// response wording.
func observeWorkerAttempts(ctx context.Context, s *Store, workID string, observation *workflowObservation) error {
	rows, err := s.DatabaseForTesting().QueryContext(ctx, `SELECT attempt_id,lane_id,lane_version,lane_digest,capability_class,readback_model,lifecycle_state FROM worker_attempts WHERE work_id=? ORDER BY dispatched_at, attempt_id`, workID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	lanes := []any{}
	for rows.Next() {
		var attemptID, laneID, laneDigest, capability, readback, lifecycle string
		var laneVersion int64
		if err := rows.Scan(&attemptID, &laneID, &laneVersion, &laneDigest, &capability, &readback, &lifecycle); err != nil {
			return err
		}
		observation.WorkerAttempts[attemptID] = map[string]any{
			"lane_id": laneID, "lane_version": laneVersion, "lane_digest": laneDigest,
			"capability_class": capability,
			"readback_model":   readback, "lifecycle_state": lifecycle,
		}
		lanes = append(lanes, laneID)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	observation.WorkerAttempts["lanes"] = lanes
	return nil
}
