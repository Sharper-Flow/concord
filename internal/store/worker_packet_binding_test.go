package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// recordedPacketInputs returns the task and inputs.binding a truthful lane
// packet carries for the work item and lane from recorded state: the active
// contract premise and its version when a contract is active, otherwise the
// recorded question with a null contract version. The work version is the
// one the dispatch is admitted at, and the assigned result is the lane's
// worker-scope assignment.
func recordedPacketInputs(t *testing.T, s *Store, workID, laneID string) (string, map[string]any) {
	t.Helper()
	question, err := readRecordedWorkQuestion(context.Background(), s.DatabaseForTesting(), workID)
	if err != nil {
		t.Fatal(err)
	}
	assigned, ok := WorkerScopeAssignedResult(laneID)
	if !ok {
		t.Fatalf("lane %s carries no worker-scope assignment", laneID)
	}
	binding := map[string]any{"objective_source": "work_question", "work_version": readWorkVersion(t, s, workID), "contract_version": nil, "assigned_result": assigned}
	var contractVersion int64
	var premise string
	err = s.DatabaseForTesting().QueryRow(`SELECT contract_version,premise FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&contractVersion, &premise)
	if errors.Is(err, sql.ErrNoRows) {
		return question, binding
	}
	if err != nil {
		t.Fatal(err)
	}
	binding["objective_source"] = "contract_premise"
	binding["contract_version"] = contractVersion
	return premise, binding
}

// bindPacketToRecordedState rewrites a packet's task and binding from
// recorded state, for fixtures that build the packet before the dispatch
// they test and carry no store at build time.
func bindPacketToRecordedState(t *testing.T, s *Store, packet map[string]any) map[string]any {
	t.Helper()
	inputs := packet["inputs"].(map[string]any)
	task, binding := recordedPacketInputs(t, s, packet["work_id"].(string), packet["lane_id"].(string))
	inputs["task"] = task
	inputs["binding"] = binding
	for member, value := range recordedPacketRecords(t, s, packet["work_id"].(string)) {
		inputs[member] = value
	}
	if testLaneCapabilityClass(packet["lane_id"].(string)) == "review" {
		snapshot, err := ReadWorkflowContinuity(t.Context(), s, ContinuityRequest{Work: packet["work_id"].(string)})
		if err != nil {
			t.Fatal(err)
		}
		if reviewInput := reviewInputForPacketFixture(t, s, snapshot); reviewInput != nil {
			inputs["review_input"] = reviewInput
		}
	}
	return packet
}

// Synthetic Git fixtures derive the expected input with their declared subject
// runner. Real repositories carry the continuity descriptor unchanged.
func reviewInputForPacketFixture(t *testing.T, s *Store, snapshot ContinuitySnapshot) *WorkerReviewInput {
	t.Helper()
	if snapshot.ReviewInputWarning == nil {
		return snapshot.ReviewInput
	}
	definition := mustBuiltinDefinition(t, snapshot.WorkPin.WorkflowType).Definition
	if !workflowDefinitionProducesDelivery(definition) {
		return nil
	}
	receipt, err := readCurrentOracleSubjectReceipt(t.Context(), s.db, snapshot.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	if receipt == nil {
		t.Fatal("synthetic Review fixture has no qualified subject")
	}
	prepared, err := PrepareWorkerDispatchFreshness(t.Context(), s, PrepareWorkerDispatchRequest{
		WorkID: snapshot.WorkID, SessionWorktree: receipt.Path,
		Runner: qualifiedSubjectRunner{head: strings.TrimPrefix(receipt.SubjectRef, "commit:")},
	})
	if err != nil {
		t.Fatal(err)
	}
	input, err := currentWorkerReviewInput(WithWorkerDispatchPreparation(t.Context(), prepared), s.db, snapshot.WorkID, LaneDefinition{CapabilityClass: "review"}, definition)
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func TestBindPacketToRecordedStateCopiesReviewInputOnlyForReviewLane(t *testing.T) {
	f := seedWorkContextFixture(t, "packet-binding-review-input")
	t.Cleanup(func() { _ = f.store.Close() })
	reviewSubjectClaim(t, f.store, f.workID)
	snapshot, err := ReadWorkflowContinuity(t.Context(), f.store, ContinuityRequest{Work: f.workID})
	if err != nil || snapshot.ReviewInput == nil {
		t.Fatalf("current Review input = %+v, err = %v", snapshot.ReviewInput, err)
	}
	for _, lane := range []string{"review", "implement"} {
		t.Run(lane, func(t *testing.T) {
			packet := bindPacketToRecordedState(t, f.store, map[string]any{
				"work_id": f.workID, "lane_id": lane, "inputs": map[string]any{},
			})
			got, present := packet["inputs"].(map[string]any)["review_input"]
			if lane == "review" {
				if !reflect.DeepEqual(got, snapshot.ReviewInput) {
					t.Fatalf("Review input = %+v, want current continuity descriptor %+v", got, snapshot.ReviewInput)
				}
			} else if present {
				t.Fatalf("non-Review packet carries Review input: %+v", got)
			}
		})
	}
}

// recordedPacketRecords returns the recorded-state members a truthful lane
// packet carries, as the adapter builds them: the law context, work context,
// design record, and proposal from the pinned continuity, and the recorded value
// statement, task, and narrative. Members with no record are absent.
func recordedPacketRecords(t *testing.T, s *Store, workID string) map[string]any {
	t.Helper()
	snapshot, err := ReadWorkflowContinuity(context.Background(), s, ContinuityRequest{Work: workID})
	if err != nil {
		t.Fatal(err)
	}
	records := map[string]any{}
	if snapshot.LawContext != nil {
		records["law_context"] = snapshot.LawContext
	}
	if snapshot.WorkContext != nil {
		records["work_context"] = snapshot.WorkContext
	}
	if snapshot.DesignRecord != nil {
		records["design_record"] = snapshot.DesignRecord
	}
	if snapshot.ProposalRecord != nil {
		records["proposal_record"] = snapshot.ProposalRecord.PacketProposal()
	}
	work, err := readWorkerPacketWorkRecord(context.Background(), s.DatabaseForTesting(), workID)
	if err != nil {
		t.Fatal(err)
	}
	if work != nil {
		records["work_record"] = work
	}
	return records
}

func dispatchBindingAttempt(t *testing.T, s *Store, seed cd0059DispatchSeed, attemptID string, packet map[string]any) error {
	t.Helper()
	packetBytes, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	fieldsPayload, err := json.Marshal(map[string]any{"attempt_id": attemptID, "worker_packet": json.RawMessage(packetBytes)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = invokeWorkflowActionForCD0059(context.Background(), t, s, WorkflowActionExecutionRequest{
		WorkID: seed.workID, ExpectedVersion: readWorkVersion(t, s, seed.workID), ActionID: "dispatch_worker",
		Payload: fieldsPayload, SessionWorktree: dispatchSessionWorktree(t, s, seed.workID),
		Actor: seed.ownerActor, AcceptedInputsDigest: cd0059TestDigest(t, attemptID+"-inputs"),
		IdempotencyIdentity: attemptID + "-op", OperationID: "op-" + attemptID, PrincipalRef: seed.ownerActor.PrincipalRef,
		Tool: "concord_work_transition", IdempotencyKey: attemptID + "-key", RequestID: "req-" + attemptID,
		AcceptedScope: `{}`, ContractDigest: testManifestDigest,
	})
	return err
}

func dispatchStartedCount(t *testing.T, s *Store, workID string) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts r WHERE r.subject_type='work_item' AND r.subject_id=? AND r.kind=? AND (
		EXISTS(SELECT 1 FROM worker_authorizations a WHERE a.work_id=r.subject_id AND a.start_seq=r.effect_seq) OR
		EXISTS(SELECT 1 FROM workflow_step_epochs e WHERE e.work_id=r.subject_id AND e.latest_dispatch_start_seq=r.effect_seq) OR
		EXISTS(SELECT 1 FROM workflow_action_obligations o WHERE o.work_id=r.subject_id AND o.effect_seq=r.effect_seq AND json_extract(o.payload,'$.action_id')='dispatch_worker'))`,
		workID, WorkflowActionStarted).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// seedActiveContract records an active contract for the work item directly
// in the projection, so the binding validation reads a recorded premise.
func seedActiveContract(t *testing.T, s *Store, workID string, version int64, premise string) {
	t.Helper()
	actorRef := DeriveWorkflowActorRef("principal/binding", "client/binding", "agent/binding", "session/binding")
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); INSERT OR IGNORE INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,?,?,?,?,'agent','2026-09-09T00:00:00Z'); DELETE FROM write_guard`,
		actorRef, "principal/binding", "client/binding", "agent/binding", "session/binding"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,?,?,'internal_sqlite','[]','[]','2026-09-09T00:00:00Z',?,'[]','[]',1,'prototype_internal'); DELETE FROM write_guard`,
		workID, version, premise, actorRef); err != nil {
		t.Fatal(err)
	}
}

// TestDispatchRefusesBindingThatContradictsRecordedState proves the core
// validates inputs.binding against recorded state before it opens any
// attempt: the lane's worker-scope assignment, the admitted work version, and
// the objective source the active contract (or its absence) dictates.
func TestDispatchRefusesBindingThatContradictsRecordedState(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		contract bool
		mutate   func(inputs, binding map[string]any)
		want     string
	}{
		{name: "assigned result of another lane", mutate: func(_, b map[string]any) { b["assigned_result"] = "contract_findings" }, want: "worker-scope assignment is files_touched"},
		{name: "stale work version", mutate: func(_, b map[string]any) { b["work_version"] = b["work_version"].(int64) - 1 }, want: "work_version"},
		{name: "contract premise without a contract", mutate: func(_, b map[string]any) {
			b["objective_source"] = "contract_premise"
			b["contract_version"] = 1
		}, want: "work_question"},
		{name: "recorded question beside an active contract", contract: true, mutate: func(_, b map[string]any) {
			b["objective_source"] = "work_question"
			b["contract_version"] = nil
		}, want: "contract_premise"},
		{name: "task differs from the recorded question", mutate: func(i, _ map[string]any) { i["task"] = "an invented question" }, want: "differs from the work item's recorded question"},
		{name: "superseded contract version", contract: true, mutate: func(_, b map[string]any) { b["contract_version"] = 1 }, want: "active contract version is 2"},
		{name: "task differs from the approved premise", contract: true, mutate: func(i, _ map[string]any) { i["task"] = "an objective the operator never approved" }, want: "differs from the approved premise"},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := openTemp(t)
			defer s.Close()
			seed := seedDispatchFixture(t, s, "work-binding-"+strings.Repeat("x", index+1))
			if tc.contract {
				seedActiveContract(t, s, seed.workID, 2, "Approved premise with  preserved spacing.\n")
			}
			attemptID := "attempt-binding-" + strings.Repeat("y", index+1)
			packet := dispatchWorkerPacket(t, s, seed.workID, "execution", attemptID)
			inputs := packet["inputs"].(map[string]any)
			tc.mutate(inputs, inputs["binding"].(map[string]any))
			err := dispatchBindingAttempt(t, s, seed, attemptID, packet)
			var failure *Failure
			if !errors.As(err, &failure) || failure.Kind != KindInvalidPayload || !strings.Contains(failure.Detail, tc.want) {
				t.Fatalf("dispatch error = %v, want invalid_payload naming %q", err, tc.want)
			}
			if started := dispatchStartedCount(t, s, seed.workID); started != 0 {
				t.Fatalf("refused binding recorded %d dispatch starts, want 0", started)
			}
		})
	}
}

// TestDispatchAdmitsBindingThatMatchesRecordedState proves the positive
// control for both objective sources: a truthful binding derived from
// recorded state is admitted, and a contract-premise packet carries the
// premise byte-for-byte.
func TestDispatchAdmitsBindingThatMatchesRecordedState(t *testing.T) {
	t.Parallel()
	for _, contract := range []bool{false, true} {
		s := openTemp(t)
		workID := "work-binding-ok-question"
		if contract {
			workID = "work-binding-ok-contract"
		}
		seed := seedDispatchFixture(t, s, workID)
		if contract {
			seedActiveContract(t, s, seed.workID, 2, "Approved premise with  preserved spacing.\n")
		}
		packet := dispatchWorkerPacket(t, s, seed.workID, "execution", "attempt-"+workID)
		if err := dispatchBindingAttempt(t, s, seed, "attempt-"+workID, packet); err != nil {
			t.Fatalf("contract=%t truthful binding refused: %v", contract, err)
		}
		_ = s.Close()
	}
}

// TestRecordedWorkQuestionMatchesTheAdapterSelection pins the store's
// read-only question selection to the adapter's: the recorded task wins when
// it holds visible text, then the title, then the narrative, and the chosen
// text keeps its bytes.
func TestRecordedWorkQuestionMatchesTheAdapterSelection(t *testing.T) {
	t.Parallel()
	cases := []struct{ task, title, narrative, want string }{
		{"  Compare 𝕏 and é.\n", "Title", "Narrative", "  Compare 𝕏 and é.\n"},
		{" \t\u00a0\ufeff\u2028", "Title", "Narrative", "Title"},
		{"", " \u3000", "Narrative body", "Narrative body"},
	}
	for _, tc := range cases {
		if got := recordedWorkQuestion(tc.task, tc.title, tc.narrative); got != tc.want {
			t.Errorf("recordedWorkQuestion(%q, %q, %q) = %q, want %q", tc.task, tc.title, tc.narrative, got, tc.want)
		}
	}
}
