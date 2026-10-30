package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The premise authority owner keeps the confirm_premise action for both
// classes. Under consequence_v1 the active contract's stored decision fact
// classifies the confirmation: engineering records the authenticated
// non-worker coordinator's own confirm choice — no operator substitution, no
// pending human question, no human decision-context digest — while
// decision-bearing work keeps the exact verified operator identity, human
// choice, and decision-context digest. Every confirmation stays bound to its
// contract version; deliverables stay required; historical events replay with
// their original operator authority.

// engineeringConfirmationSeed parks a workflow.implementation item at
// acceptance on the live journey and records the routine actors.
func engineeringConfirmationSeed(t *testing.T, workID string) (s *Store, owner, reviewer, operator, coordinator WorkflowActor) {
	t.Helper()
	return engineeringConfirmationFixture(t, workID, true, true)
}

// engineeringConfirmationFixture is engineeringConfirmationSeed with Main's
// criterion association and the investigation each made optional.
func engineeringConfirmationFixture(t *testing.T, workID string, associate, investigation bool) (s *Store, owner, reviewer, operator, coordinator WorkflowActor) {
	t.Helper()
	f := rAcceptance(t, workID, []string{}, associate, investigation)
	actors := recordApprovalActors(t, f.store, workID)
	return f.store, f.owner, actors.reviewer, actors.operator, actors.coordinator
}

// engineeringConfirmationDefinition is the live definition the seeds pin.
func engineeringConfirmationDefinition(t *testing.T) WorkflowDefinition {
	t.Helper()
	return mustBuiltinDefinition(t, "workflow.implementation").Definition
}

// seedRoutineReviewersForSelection records the reviewer/operator/coordinator
// triple on a delivered seed (selection variants use their own store shape).
func seedRoutineReviewersForSelection(t *testing.T, s *Store, workID string) (reviewer, operator, coordinator WorkflowActor) {
	t.Helper()
	ctx := context.Background()
	reviewer = WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/independent-reviewer", SessionRef: "session/" + workID + "-reviewer", ActorClass: ActorAgent}
	operator = WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/operator", SessionRef: "session/" + workID + "-operator", ActorClass: ActorOperator}
	coordinator = WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/coordinator", SessionRef: "session/" + workID + "-coordinator", ActorClass: ActorAgent}
	version := readWorkVersion(t, s, workID)
	events := []Event{}
	for index, actor := range []WorkflowActor{reviewer, operator, coordinator} {
		ref, err := WorkflowActorRef(actor)
		if err != nil {
			t.Fatal(err)
		}
		classes := map[WorkflowActor]string{reviewer: "agent", operator: "operator", coordinator: "agent"}
		events = append(events, workflowEventWithActor(fmt.Sprintf("selection-actor-%d-%s", index, workID), WorkflowActorRecorded, workID, ref, map[string]any{"work_id": workID, "expected_version": version + int64(index), "resulting_version": version + int64(index) + 1, "actor_ref": ref, "principal_ref": actor.PrincipalRef, "client_ref": actor.ClientRef, "agent_ref": actor.AgentRef, "session_ref": actor.SessionRef, "actor_class": classes[actor]}))
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
	return reviewer, operator, coordinator
}

// runEngineeringConfirmation invokes the confirm_premise constructor directly
// on the seeded store: the sanctioned transactional seam for a definition the
// builtin registry does not carry.
func runEngineeringConfirmation(t *testing.T, s *Store, definition WorkflowDefinition, workID, invokerRef string, fields map[string]json.RawMessage) ([]Event, error) {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	choice, digest, rest := confirmationEnvelope(fields)
	return workflowConfirmPremiseEventsForDefinition(context.Background(), tx, definition, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: "confirm_premise", SelectedChoice: choice, DecisionContextDigest: digest, OperationID: "confirm-" + workID,
	}, invokerRef, rest, "confirm-"+workID, version)
}

// confirmationEnvelope lifts the envelope-carried selections out of a
// confirmation field map, as the tool boundary carries them outside the
// action fields.
func confirmationEnvelope(fields map[string]json.RawMessage) (choice, digest string, rest map[string]json.RawMessage) {
	rest = map[string]json.RawMessage{}
	for name, value := range fields {
		rest[name] = value
	}
	choice = workflowFieldStringDefault(rest, "selected_choice", "")
	digest = workflowFieldStringDefault(rest, "decision_context_digest", "")
	delete(rest, "selected_choice")
	delete(rest, "decision_context_digest")
	return choice, digest, rest
}

func confirmationFields(choice bool, digest string) map[string]json.RawMessage {
	fields := map[string]json.RawMessage{}
	if choice {
		fields["selected_choice"] = json.RawMessage(`"confirm"`)
	}
	if digest != "" {
		fields["decision_context_digest"] = json.RawMessage(`"` + digest + `"`)
	}
	return fields
}

func TestEngineeringConfirmation(t *testing.T) {
	t.Parallel()

	t.Run("engineering coordinator confirmation succeeds", func(t *testing.T) {
		t.Parallel()
		const workID = "engineering-confirm-coordinator"
		s, _, _, _, coordinator := engineeringConfirmationSeed(t, workID)
		events, err := runEngineeringConfirmation(t, s, engineeringConfirmationDefinition(t), workID, ownerRefForTest(t, coordinator), confirmationFields(true, ""))
		if err != nil {
			t.Fatalf("engineering coordinator confirmation refused: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("confirmation produced %d events, want 1", len(events))
		}
		var payload struct {
			ContractVersion       int64  `json:"contract_version"`
			ConfirmingActorRef    string `json:"confirming_actor_ref"`
			ConfirmationAuthority string `json:"confirmation_authority"`
			DecisionContextDigest string `json:"decision_context_digest"`
		}
		if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
			t.Fatal(err)
		}
		coordinatorRef := ownerRefForTest(t, coordinator)
		if payload.ConfirmingActorRef != coordinatorRef || payload.ConfirmationAuthority != "coordinator" {
			t.Fatalf("confirmation recorded %q/%q, want the invoking coordinator with coordinator authority", payload.ConfirmingActorRef, payload.ConfirmationAuthority)
		}
		if payload.DecisionContextDigest != "" {
			t.Fatalf("engineering confirmation carried a human decision-context digest %q", payload.DecisionContextDigest)
		}
		if events[0].PayloadVersion != 2 {
			t.Fatalf("confirmation payload version = %d, want 2", events[0].PayloadVersion)
		}
	})

	t.Run("engineering refuses a human decision-context digest", func(t *testing.T) {
		t.Parallel()
		const workID = "engineering-confirm-digest"
		s, _, _, _, coordinator := engineeringConfirmationSeed(t, workID)
		_, err := runEngineeringConfirmation(t, s, engineeringConfirmationDefinition(t), workID, ownerRefForTest(t, coordinator), confirmationFields(true, "digest:human-question"))
		var failure *Failure
		if err == nil || !failureAs(err, &failure) || failure.Kind != KindInvalidInput {
			t.Fatalf("engineering confirmation with a human digest returned %v, want the invalid-input refusal", err)
		}
	})

	t.Run("engineering refuses a choice other than confirm", func(t *testing.T) {
		t.Parallel()
		const workID = "engineering-confirm-choice"
		s, _, _, _, coordinator := engineeringConfirmationSeed(t, workID)
		_, err := runEngineeringConfirmation(t, s, engineeringConfirmationDefinition(t), workID, ownerRefForTest(t, coordinator), confirmationFields(false, ""))
		var failure *Failure
		if err == nil || !failureAs(err, &failure) || failure.Kind != KindInvalidInput {
			t.Fatalf("engineering confirmation with revise choice returned %v, want the invalid-input refusal", err)
		}
	})

	t.Run("missing deliverables and investigation refuse through the constructor", func(t *testing.T) {
		t.Parallel()
		const workID = "engineering-confirm-deliverables"
		s, _, _, _, coordinator := engineeringConfirmationFixture(t, workID, false, true)
		// Main has not associated the accepted Review with the criteria, so
		// the acceptance deliverables the confirmation requires are absent.
		_, err := runEngineeringConfirmation(t, s, engineeringConfirmationDefinition(t), workID, ownerRefForTest(t, coordinator), confirmationFields(true, ""))
		var failure *Failure
		if err == nil || !failureAs(err, &failure) || failure.Kind != KindMissingEvidence {
			t.Fatalf("engineering confirmation without deliverables returned %v, want the missing-evidence refusal", err)
		}
		// The investigation artifact is demanded on the no-question route.
		noInvestigation := "engineering-confirm-investigation"
		s2, _, _, _, coordinator2 := engineeringConfirmationFixture(t, noInvestigation, true, false)
		_, err = runEngineeringConfirmation(t, s2, engineeringConfirmationDefinition(t), noInvestigation, ownerRefForTest(t, coordinator2), confirmationFields(true, ""))
		if err == nil || !failureAs(err, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, "investigation artifact") {
			t.Fatalf("engineering confirmation without investigation returned %v, want the missing-investigation refusal", err)
		}
	})

	t.Run("worker authority refuses to confirm as coordinator", func(t *testing.T) {
		t.Parallel()
		const workID = "engineering-confirm-worker"
		s, _, _, _, coordinator := engineeringConfirmationSeed(t, workID)
		coordinatorRef := ownerRefForTest(t, coordinator)
		lane := BuiltinLaneDefinitions()[0]
		dispatch := workerDispatchEvent(workID, "engineering-confirm-dispatch-"+workID, lane, map[string]any{"attempt_id": "attempt:" + workID, "lane_actor_ref": coordinatorRef})
		dispatch.Actor = coordinatorRef
		dispatch.OccurredAt = time.Unix(9, 50).UTC()
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{dispatch}}); err != nil {
			t.Fatalf("seed worker dispatch: %v", err)
		}
		_, err := runEngineeringConfirmation(t, s, engineeringConfirmationDefinition(t), workID, coordinatorRef, confirmationFields(true, ""))
		var failure *Failure
		if err == nil || !failureAs(err, &failure) || failure.Kind != KindUnauthorized {
			t.Fatalf("worker-authority confirmation returned %v, want the unauthorized refusal", err)
		}
	})

	t.Run("operator selection admits engineering without a digest", func(t *testing.T) {
		t.Parallel()
		const workID = "engineering-selection-engineering"
		s, _, _, _, coordinator := engineeringConfirmationFixture(t, workID, true, true)
		registry := BuiltinWorkflowRegistry()
		selectionVersion := readWorkVersion(t, s, workID)
		tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		err = validateWorkflowOperatorSelectionTx(context.Background(), tx, registry, WorkflowActionPreflightRequest{
			WorkID: workID, ActionID: "confirm_premise", SelectedChoice: "confirm", Actor: coordinator,
			ExpectedVersion: selectionVersion,
		})
		if err != nil {
			t.Fatalf("engineering operator selection without a digest refused: %v", err)
		}
	})

	t.Run("operator selection refuses a human digest for engineering", func(t *testing.T) {
		t.Parallel()
		const workID = "engineering-selection-digest"
		s, _, _, _, _ := engineeringConfirmationFixture(t, workID, true, true)
		registry := BuiltinWorkflowRegistry()
		selectionVersion := readWorkVersion(t, s, workID)
		tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		// The engineering route refuses any human digest; the refusal fires
		// before any digest comparison, so a literal stands in for the value.
		err = validateWorkflowOperatorSelectionTx(context.Background(), tx, registry, WorkflowActionPreflightRequest{
			WorkID: workID, ActionID: "confirm_premise", SelectedChoice: "confirm", DecisionContextDigest: "digest:human-question",
			ExpectedVersion: selectionVersion,
		})
		var failure *Failure
		if err == nil || !failureAs(err, &failure) || failure.Kind != KindInvalidPayload || !strings.Contains(failure.Detail, "carries no human decision-context digest") {
			t.Fatalf("engineering selection with a human digest returned %v, want the digest refusal", err)
		}
	})

	t.Run("operator selection keeps the investigation requirement for engineering", func(t *testing.T) {
		t.Parallel()
		const workID = "engineering-selection-investigation"
		s, _, _, _, _ := engineeringConfirmationFixture(t, workID, true, false)
		registry := BuiltinWorkflowRegistry()
		selectionVersion := readWorkVersion(t, s, workID)
		tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		err = validateWorkflowOperatorSelectionTx(context.Background(), tx, registry, WorkflowActionPreflightRequest{
			WorkID: workID, ActionID: "confirm_premise", SelectedChoice: "confirm", ExpectedVersion: selectionVersion,
		})
		var failure *Failure
		if err == nil || !failureAs(err, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, "investigation artifact") {
			t.Fatalf("engineering selection without investigation returned %v, want the missing-investigation refusal", err)
		}
	})

	t.Run("engineering records the agent-class coordinator only", func(t *testing.T) {
		t.Parallel()
		const workID = "engineering-confirm-agent-class"
		s, _, _, operator, _ := engineeringConfirmationSeed(t, workID)
		_, err := runEngineeringConfirmation(t, s, engineeringConfirmationDefinition(t), workID, ownerRefForTest(t, operator), confirmationFields(true, ""))
		var failure *Failure
		if err == nil || !failureAs(err, &failure) || failure.Kind != KindInvalidOperation {
			t.Fatalf("operator-class invoker confirmation returned %v, want the coordinator-typed refusal", err)
		}
	})
}

func TestEngineeringConfirmationLifecycle(t *testing.T) {
	t.Parallel()

	t.Run("class upgrade invalidates the engineering confirmation", func(t *testing.T) {
		t.Parallel()
		const workID = "engineering-upgrade-lifecycle"
		s := openTemp(t)
		defer s.Close()
		seedDecisionDomainsItemFatal(t, s, workID, []string{})
		coordinator := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/coordinator", SessionRef: "session/" + workID + "-coordinator", ActorClass: ActorAgent}
		coordinatorRef := ownerRefForTest(t, coordinator)
		operator := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/operator", SessionRef: "session/" + workID + "-operator", ActorClass: ActorOperator}
		operatorRef := ownerRefForTest(t, operator)
		definition := routineDefinitionOnPin(t, s, workID)
		version := readWorkVersion(t, s, workID)
		recorded := workflowEventWithActor("engineering-coordinator-"+workID, WorkflowActorRecorded, workID, coordinatorRef, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "actor_ref": coordinatorRef, "principal_ref": coordinator.PrincipalRef, "client_ref": coordinator.ClientRef, "agent_ref": coordinator.AgentRef, "session_ref": coordinator.SessionRef, "actor_class": "agent"})
		operatorRecorded := workflowEventWithActor("engineering-operator-"+workID, WorkflowActorRecorded, workID, operatorRef, map[string]any{"work_id": workID, "expected_version": version + 1, "resulting_version": version + 2, "actor_ref": operatorRef, "principal_ref": operator.PrincipalRef, "client_ref": operator.ClientRef, "agent_ref": operator.AgentRef, "session_ref": operator.SessionRef, "actor_class": "operator"})
		if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{recorded, operatorRecorded}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
			t.Fatalf("record confirmation actors: %v", err)
		}
		version = readWorkVersion(t, s, workID)
		expectedV, resultingV := version, version+1
		// The factored fold body receives the verified synthetic definition
		// and genuinely writes the engineering confirmation row for v1.
		confirmed := workflowEventWithActor("engineering-confirm-v1-"+workID, WorkflowPremiseConfirmed, workID, coordinatorRef, map[string]any{"work_id": workID, "expected_version": expectedV, "resulting_version": resultingV, "contract_version": 1, "confirming_actor_ref": coordinatorRef, "confirmation_authority": "coordinator"})
		confirmed.PayloadVersion = 2
		writeTx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := enterWrite(context.Background(), writeTx); err != nil {
			t.Fatal(err)
		}
		writeErr := workflowPremiseConfirmedWriteTx(context.Background(), writeTx, confirmed, workflowPremiseConfirmedPayload{WorkflowVersionFields: WorkflowVersionFields{WorkID: workID, ExpectedVersion: &expectedV, ResultingVersion: &resultingV}, ContractVersion: 1, ConfirmingActorRef: coordinatorRef, ConfirmationAuthority: "coordinator"})
		if leaveErr := leaveWrite(context.Background(), writeTx); writeErr == nil {
			writeErr = leaveErr
		}
		if writeErr != nil {
			_ = writeTx.Rollback()
			t.Fatalf("engineering confirmation fold body refused: %v", writeErr)
		}
		if err := writeTx.Commit(); err != nil {
			t.Fatal(err)
		}
		if err := s.DatabaseForTesting().QueryRow(`SELECT confirmed_by FROM workflow_premise_confirmations WHERE work_id=? AND contract_version=1`, workID).Scan(&coordinatorRef); err != nil {
			t.Fatalf("engineering confirmation row genuinely missing: %v", err)
		}
		successor := map[string]any{
			"contract_version": int64(2), "premise": "the decision-bearing successor premise",
			"outcome_predicates": []map[string]any{{"predicate_id": "predicate:research-report-exit", "ordinal": 0, "outcome_kind": "outcome",
				"outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}}}},
			"required_evidence": []string{"artifact"}, "route_conventions": []string{}, "spec_mandate": []string{},
			"law_modifies": []string{}, "law_revisions": []WorkflowLawRevision{}, "law_boundary_version": 1,
			"rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
			"decision_domains": []string{"product"},
		}
		supersede := workflowEventWithActor("engineering-upgrade-supersede", WorkflowContractSuperseded, workID, coordinatorRef, map[string]any{
			"work_id": workID, "expected_version": version + 2, "resulting_version": version + 3,
			"previous_contract_version": 1, "new_contract_version": 2,
			"supersede_reason": "upgrade to decision-bearing work", "audit_evidence": []string{"evidence:upgrade-audit"},
			"successor_contract": successor,
		})
		supersede.PayloadVersion = eventKindRegistry[WorkflowContractSuperseded].CurrentVersion
		currentVersion := readWorkVersion(t, s, workID)
		supersede.Payload = mustJSONValue(map[string]any{"work_id": workID, "expected_version": currentVersion, "resulting_version": currentVersion + 1, "previous_contract_version": 1, "new_contract_version": 2, "supersede_reason": "upgrade to decision-bearing work", "audit_evidence": []string{"evidence:upgrade-audit"}, "successor_contract": successor})
		if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{supersede}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): currentVersion}}); err != nil {
			t.Fatalf("class upgrade supersession refused: %v", err)
		}
		// The v1 confirmation row survives as history, but the active
		// contract is now v2 and the stale v1 confirmation cannot satisfy it:
		// a live re-confirmation of v1 refuses as stale.
		staleCurrent := readWorkVersion(t, s, workID)
		staleTx, staleErr := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if staleErr != nil {
			t.Fatal(staleErr)
		}
		defer staleTx.Rollback()
		staleWrite := workflowPremiseConfirmedPayload{WorkflowVersionFields: WorkflowVersionFields{WorkID: workID, ExpectedVersion: &staleCurrent, ResultingVersion: new(int64)}, ContractVersion: 1, ConfirmingActorRef: coordinatorRef, ConfirmationAuthority: "coordinator"}
		confirmErr := workflowPremiseConfirmedWriteTx(context.Background(), staleTx, confirmed, staleWrite)
		var failure *Failure
		if confirmErr == nil || !failureAs(confirmErr, &failure) || !strings.Contains(failure.Detail, "supersession invalidated") {
			t.Fatalf("stale v1 confirmation write returned %v, want the supersession-invalidated refusal", confirmErr)
		}
		if err := staleTx.Rollback(); err != nil {
			t.Fatal(err)
		}
		staleFields := confirmationFields(true, "")
		staleFields["contract_version"] = json.RawMessage(`1`)
		_, constructorErr := runEngineeringConfirmation(t, s, definition, workID, coordinatorRef, staleFields)
		var constructorFailure *Failure
		if constructorErr == nil || !failureAs(constructorErr, &constructorFailure) || constructorFailure.Kind != KindInvalidPayload || !strings.Contains(constructorFailure.Detail, "supersession invalidated") {
			t.Fatalf("constructor stale v1 confirmation returned %v, want the supersession-invalidated refusal", constructorErr)
		}
		forgedTx, forgedErr := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if forgedErr != nil {
			t.Fatal(forgedErr)
		}
		if err := workflowPremiseConfirmedAuthorityTx(context.Background(), forgedTx, workID, 2, coordinatorRef, "coordinator"); err != nil {
			t.Fatalf("coordinator authority refused on the active successor: %v", err)
		}
		// A named contract version other than the active one refuses even
		// under the right authority: supersession invalidates the stale
		// confirmation.
		if err := workflowPremiseConfirmedAuthorityTx(context.Background(), forgedTx, workID, 1, coordinatorRef, "coordinator"); err == nil {
			t.Fatal("coordinator authority accepted a superseded contract version")
		}
		// Wrong-direction authority: an operator-typed claim refuses — the
		// confirmation records the coordinator authority alone.
		if err := workflowPremiseConfirmedAuthorityTx(context.Background(), forgedTx, workID, 1, operatorRef, "operator"); err == nil {
			t.Fatal("operator authority accepted")
		}
		if err := workflowPremiseConfirmationAuthority(context.Background(), forgedTx, workID, operatorRef); err == nil {
			t.Fatal("an operator-typed confirmation actor passed the completion authority owner")
		}
		if err := forgedTx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if err := s.DatabaseForTesting().QueryRow(`SELECT 1 FROM workflow_premise_confirmations WHERE work_id=? AND contract_version=2`, workID).Scan(new(any)); err == nil {
			t.Fatal("the successor inherited a confirmation the supersession invalidated")
		}
	})
}
