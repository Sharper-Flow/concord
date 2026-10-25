package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestWorkflowDefinitionResolution pins one rule: every workflow owner that
// reads the instance's definition derives it from the actual verified pin
// under the caller's selected registry — definition selection, the contract
// and action prefix, confirmation construction and folding, acceptance
// deliverables, the durable evidence producer, and the terminal fold that
// closes the work lifecycle. The registry is local-only and declares
// consequence_v1, so any owner that resolves the pin through the builtin
// registry refuses these witnesses.
//
// Seed constraints: the pin exists only through the WorkflowDefinitionSelected
// fold; the contract carries the explicit decision_domains fact; evidence
// binds only against a completed durable producer operation claimed and
// completed through the fence entry points; the investigation artifact is a
// work.observation_recorded fold. No raw projection or authority-row insert,
// pin flip, step reposition, prepare row, or replay override seeds any state.
//
// The negative subtests hold the refusals: a fold scope from another
// transaction and a conflicting scope binding refuse with a typed failure
// before any write, wrong and missing registries never fall back to the
// builtin registry, and wrong steps and stale epochs stay refused at the
// fence.
//

// resolutionWorkflowRef names the local-only workflow family. The builtin
// registry never carries it, so any fold that resolves the pin through the
// builtin registry refuses; the ref exists to make that refusal observable.
const resolutionWorkflowRef = "workflow.resolution_local"

// resolutionPolicyDefinition copies the registered fixture shape and declares
// consequence_v1 on a local-only ref and version, so the definition is a real
// new-policy pin that no builtin entry can resolve.
func resolutionPolicyDefinition(t *testing.T) WorkflowDefinition {
	t.Helper()
	definition := routinePolicyDefinition(t)
	definition.Ref = resolutionWorkflowRef
	// The decision-record checkpoint folds against the pinned payload
	// contract, so the local definition declares record_decision's typed
	// fields beside the implementation actions it already carries.
	definition.AvailableActions = append(definition.AvailableActions, "record_decision")
	definition.ActionDefinitions = append(definition.ActionDefinitions, currentActionDefinition("record_decision", false))
	return definition
}

// newResolutionRegistry registers the local definition in a fresh registry and
// returns the registry with its immutable registered entry.
func newResolutionRegistry(t *testing.T, definition WorkflowDefinition) (DefinitionRegistry, RegisteredDefinition) {
	t.Helper()
	registry := NewWorkflowDefinitionRegistry()
	registered, err := registry.Register(definition)
	if err != nil {
		t.Fatalf("local resolution definition refused registration: %v", err)
	}
	return registry, registered
}

// applyWorkflowOperationTxWithRegistry appends and folds one workflow
// operation with the caller's definition registry bound to the scope before
// any write. The binding refuses a scope from another transaction and a
// conflicting second registry on one scope.
func applyWorkflowOperationTxWithRegistry(ctx context.Context, tx *sql.Tx, operation Operation, scope *foldScope, registry DefinitionRegistry) error {
	if err := scope.bindRegistry(tx, registry); err != nil {
		return err
	}
	_, err := applyWorkflowOperationTx(ctx, tx, operation, scope)
	return err
}

// tryAppendWorkflowEventsWithRegistry appends and folds one operation through
// the workflow transaction entry and returns the refusal instead of failing
// the test, so the negative subtests can assert the typed refusal.
func tryAppendWorkflowEventsWithRegistry(s *Store, registry DefinitionRegistry, operation Operation) error {
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := applyWorkflowOperationTxWithRegistry(context.Background(), tx, operation, newFoldScope(tx), registry); err != nil {
		return err
	}
	return tx.Commit()
}

// appendWorkflowEventsWithRegistry is the positive seed seam. The caller
// registry rides the fold scope: the selection fold and every downstream
// pin-reading fold resolve the pin through this registry, never through the
// builtin registry.
func appendWorkflowEventsWithRegistry(t *testing.T, s *Store, registry DefinitionRegistry, operation Operation) {
	t.Helper()
	if err := tryAppendWorkflowEventsWithRegistry(s, registry, operation); err != nil {
		t.Fatalf("event-seeded workflow operation refused under the caller registry: %v", err)
	}
}

// resolutionEvent builds one workflow event at the registered payload version
// with sequential version fields.
func resolutionEvent(id, kind, workID, actor string, version int64, values map[string]any) Event {
	return workflowTypedEvent(id, kind, workID, actor, time.Unix(9, 0).UTC(), version, values)
}

// resolutionSeedStore opens one synthetic store with the work item and the
// synthetic law home the completion boundary reads.
func resolutionSeedStore(t *testing.T, workID string) (*Store, WorkflowActor) {
	t.Helper()
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	return s, owner
}

// resolutionSelectionOperation builds the owner actor event and the
// WorkflowDefinitionSelected event naming the local registry's ref, version,
// and digest, against the work item's current version.
func resolutionSelectionOperation(t *testing.T, s *Store, workID string, registered RegisteredDefinition, owner WorkflowActor) Operation {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	ownerRef := ownerRefForTest(t, owner)
	return Operation{
		Events: []Event{
			workflowEventWithActor("resolution-owner-"+workID, WorkflowActorRecorded, workID, ownerRef, map[string]any{
				"work_id": workID, "expected_version": version, "resulting_version": version + 1,
				"actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef,
				"agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent",
			}),
			resolutionEvent("resolution-selection-"+workID, WorkflowDefinitionSelected, workID, ownerRef, version+1, map[string]any{
				"ref": registered.Definition.Ref, "digest": registered.Digest,
				"work_kind": string(registered.Definition.WorkKind),
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version},
	}
}

// seedResolutionSelection establishes the local-only pin through the real
// selection fold. The instance row exists only when the selection fold
// resolved the pin through the caller registry.
func seedResolutionSelection(t *testing.T, workID string, registry DefinitionRegistry, registered RegisteredDefinition) (*Store, WorkflowActor) {
	t.Helper()
	s, owner := resolutionSeedStore(t, workID)
	appendWorkflowEventsWithRegistry(t, s, registry, resolutionSelectionOperation(t, s, workID, registered, owner))
	return s, owner
}

// runResolutionProducer claims and completes the execution step's durable
// producer operation through the fence entry points, so every evidence binding
// names a real completed producer of this work. The fence entry points keep
// their builtin registry choice; the registry-threaded cores carry the caller
// registry to the same claim/completion cores.
func runResolutionProducer(s *Store, registry DefinitionRegistry, workID, principal string) (FenceResult, error) {
	opID := "resolution-produce-" + workID
	requestID := "request:" + opID
	registered, ok := registry.Current(resolutionWorkflowRef)
	if !ok {
		return FenceResult{}, definitionFailure(KindDefinitionDigestMismatch, "local producer definition is not registered")
	}
	claimed, err := claimStepObservedAuthorizedWithRegistry(context.Background(), s, ClaimRequest{
		OpID: opID, WorkID: workID,
		WorkflowTypeRef: resolutionWorkflowRef, WorkflowTypeDigest: registered.Digest,
		StepID: "execution", StepKind: StepExternalEffect,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("c", 64), AcceptedScopeSnapshot: `{}`,
		PrincipalRef: principal, Tool: "workflow-resolution-test", IdempotencyKey: opID,
		RequestID: requestID, ObservedAt: time.Unix(11, 0).UTC(), ContractDigest: testManifestDigest,
	}, true, nil, nil, registry)
	if err != nil {
		return FenceResult{}, err
	}
	completed, err := completeStepWithRegistry(context.Background(), s, CompleteRequest{
		OpID: claimed.OpID, AttemptEpoch: claimed.AttemptEpoch, ResultKind: ResultCompleted,
		EvidenceRefs: []string{"evidence:operator-verdict", "evidence:operator-review"},
		ChangedRefs:  []string{workID},
		PrincipalRef: principal, Tool: "workflow-resolution-test", IdempotencyKey: "complete:" + opID,
		RequestID: requestID, ObservedAt: time.Unix(11, 1).UTC(),
		CompletedAt: ptrTime(time.Unix(11, 2).UTC()),
	}, true, nil, registry)
	if err != nil {
		return FenceResult{}, err
	}
	return completed, nil
}

// resolutionProducerFields names the completed producer operation one evidence
// binding joins: the fold verifies the producer operation is completed, for
// this work and principal, with the bound subject among its recorded evidence.
func resolutionProducerFields(workID, principal string) map[string]json.RawMessage {
	return map[string]json.RawMessage{
		"producer_id":        json.RawMessage(`"` + principal + `"`),
		"producer_run_ref":   json.RawMessage(`"resolution-produce-` + workID + `"`),
		"producer_watermark": json.RawMessage(`"request:resolution-produce-` + workID + `"`),
	}
}

// resolutionEvidencePayload builds one bind_evidence payload joined to the
// completed producer operation.
func resolutionEvidencePayload(workID, principal, kind, subject string) json.RawMessage {
	fields := resolutionProducerFields(workID, principal)
	fields["evidence_kind"] = json.RawMessage(`"` + kind + `"`)
	fields["immutable_subject_ref"] = json.RawMessage(`"` + subject + `"`)
	encoded, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return encoded
}

// advanceResolutionToExecution folds the contract (with the explicit
// decision_domains fact) and the proposal, discovery, design, and execution
// start events; claims and completes the step's durable producer operation;
// and binds the contract-required evidence against that completed producer.
// The instance sits at the execution step with both evidence kinds bound when
// this returns.
func advanceResolutionToExecution(t *testing.T, s *Store, registry DefinitionRegistry, workID string, owner WorkflowActor, domains []string) {
	t.Helper()
	advanceResolutionPrefix(t, s, registry, workID, owner, domains, true)
	bindResolutionEvidence(t, s, registry, workID, owner, "verification", "evidence:operator-verdict")
	bindResolutionEvidence(t, s, registry, workID, owner, "review", "evidence:operator-review")
}

// bindResolutionEvidence binds one evidence kind against the completed
// producer operation.
func bindResolutionEvidence(t *testing.T, s *Store, registry DefinitionRegistry, workID string, owner WorkflowActor, kind, subject string) {
	t.Helper()
	if err := runResolutionAction(s, registry, workID, "bind_evidence", resolutionEvidencePayload(workID, owner.PrincipalRef, kind, subject), owner); err != nil {
		t.Fatalf("bind %s evidence under the local pin: %v", kind, err)
	}
}

// advanceResolutionPrefix folds the contract (with the explicit
// decision_domains fact) and the proposal through execution start events. The
// instance sits at the execution step when this returns.
func advanceResolutionPrefix(t *testing.T, s *Store, registry DefinitionRegistry, workID string, owner WorkflowActor, domains []string, withProducer bool) {
	t.Helper()
	ownerRef := ownerRefForTest(t, owner)
	version := readWorkVersion(t, s, workID)
	lane := BuiltinLaneDefinitions()[0]
	appendWorkflowEventsWithRegistry(t, s, registry, Operation{
		Events: []Event{
			workflowActionCompletedFixture("resolution-proposal-"+workID, workID, ownerRef, version, "proposal", "record_proposal"),
			workflowActionCompletedFixture("resolution-discovery-"+workID, workID, ownerRef, version+1, "discovery", "record_discovery"),
			workflowActionCompletedFixture("resolution-design-"+workID, workID, ownerRef, version+2, "design", "record_design"),
			resolutionEvent("resolution-contract-"+workID, WorkflowContractApproved, workID, ownerRef, version+3, map[string]any{
				"contract_version": 1, "premise": "deliver the checked change",
				"outcome_kind":      "check",
				"outcome_payload":   map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
				"required_evidence": []string{"verification"}, "route_conventions": []string{},
				"spec_mandate": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
				"decision_domains": domains,
			}),
			workflowActionCompletedFixture("resolution-approval-"+workID, workID, ownerRef, version+4, "planning", "approve_contract"),
			workflowEventWithActor("resolution-start-"+workID, WorkflowActionStarted, workID, ownerRef, map[string]any{
				"work_id": workID, "expected_version": version + 5, "resulting_version": version + 6,
				"step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1,
				"accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": "resolution-start:" + workID,
				"actor_ref": ownerRef, "execution_model": preferredModelForLane(lane),
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version},
	})
	if withProducer {
		if _, err := runResolutionProducer(s, registry, workID, owner.PrincipalRef); err != nil {
			t.Fatalf("durable producer claim/completion refused under the local pin: %v", err)
		}
	}
}

// advanceResolutionDelivery records the delivery exit, which leaves the
// instance at the acceptance step.
func advanceResolutionDelivery(t *testing.T, s *Store, registry DefinitionRegistry, workID string, owner WorkflowActor) {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	appendWorkflowEventsWithRegistry(t, s, registry, Operation{
		Events: []Event{
			workflowActionCompletedFixture("resolution-delivery-"+workID, workID, ownerRefForTest(t, owner), version, "execution", "record_delivery"),
		},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version},
	})
}

// runResolutionAction runs one action through the real dispatcher under the
// caller registry: the sanctioned transactional seam whose verified entry the
// confirmation and completion constructors consume. It returns the refusal so
// negative callers can assert it.
func runResolutionAction(s *Store, registry DefinitionRegistry, workID, action string, payload json.RawMessage, actor WorkflowActor) error {
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		return err
	}
	if _, err := applyWorkflowActionRawTx(ctx, tx, newFoldScope(tx), registry, WorkflowActionExecutionRequest{
		EvidenceRefs: actionEvidenceRefs(action, payload), WorkID: workID, ExpectedVersion: version, ActionID: action, Payload: payload,
		Actor:                actor,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("b", 64) + fmt.Sprint(version), IdempotencyIdentity: action + "-" + workID + "-" + fmt.Sprint(version), OperationID: action + "-" + workID + "-" + fmt.Sprint(version),
		PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: action + "-" + workID + "-" + fmt.Sprint(version), RequestID: "request:" + action + "-" + workID + "-" + fmt.Sprint(version),
		ContractDigest: testManifestDigest, Now: time.Unix(10, 0).UTC(),
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// seedResolutionInvestigation records the engineering confirmation route's
// investigation artifact through the real observation fold: a
// work.observation_recorded event whose refs hold the Product's current
// Domain and another work item of that Product, beside the Domain registry
// fixture the ref resolution reads.
func seedResolutionInvestigation(t *testing.T, s *Store, workID string) {
	t.Helper()
	other := workID + "-compared"
	seedWork(t, s, other)
	seedIssue31DomainRegistry(t, s)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{{
		EventID: "resolution-investigation-" + workID, Kind: WorkObservationRecorded, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: "actor:test", OccurredAt: time.Unix(8, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion(WorkObservationRecorded),
		Payload: mustJSONValue(map[string]any{
			"observation_id": "obs:" + strings.Repeat("a", 16), "statement": "technical investigation",
			"refs": []string{"root", other}, "tags": []string{},
		}),
	}}}); err != nil {
		t.Fatalf("record investigation observation: %v", err)
	}
}

// confirmResolutionPremise drives the confirm_premise constructor wrapper —
// the entry dispatch itself calls — and folds the produced event through the
// registered fold. It returns the constructor error so callers can assert
// refusals, and folds only when construction succeeded.
func confirmResolutionPremise(t *testing.T, s *Store, registry DefinitionRegistry, definition WorkflowDefinition, workID string, actor WorkflowActor, operator *WorkflowActor, fields map[string]json.RawMessage) ([]Event, error) {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// Dispatch addresses the action's events to the operator when one is
	// supplied, and to the invoking coordinator otherwise.
	invoker := ownerRefForTest(t, actor)
	if operator != nil {
		invoker = ownerRefForTest(t, *operator)
	}
	choice, digest, rest := confirmationEnvelope(fields)
	events, err := workflowConfirmPremiseEvents(context.Background(), tx, definition, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: "confirm_premise", SelectedChoice: choice, DecisionContextDigest: digest, OperatorActor: operator,
		OperationID: "resolution-confirm-" + workID, Now: time.Unix(10, 30).UTC(),
	}, invoker, rest, "resolution-confirm-"+workID, version)
	// The store pools one connection: the constructor transaction must leave
	// the pool before the fold append opens its own transaction.
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err != nil {
		return nil, err
	}
	appendWorkflowEventsWithRegistry(t, s, registry, Operation{
		Events:           events,
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version},
	})
	return events, nil
}

// recordResolutionActors records the independent reviewer, the operator, and
// the non-executing coordinator on the seeded instance. Re-recording the same
// tuples folds idempotently, so callers may invoke it more than once.
func recordResolutionActors(t *testing.T, s *Store, workID string) (reviewer WorkflowActor, operator WorkflowActor, coordinator WorkflowActor) {
	t.Helper()
	return seedRoutineReviewersForSelection(t, s, workID)
}

func TestWorkflowDefinitionResolution(t *testing.T) {
	t.Parallel()

	t.Run("selection resolves the caller registry before an instance exists", func(t *testing.T) {
		t.Parallel()
		const workID = "resolution-select-plain"
		definition := resolutionPolicyDefinition(t)
		registry, registered := newResolutionRegistry(t, definition)
		s, _ := seedResolutionSelection(t, workID, registry, registered)
		var ref, digest string
		if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&ref, &digest); err != nil {
			t.Fatalf("selection fold did not create the instance under the caller registry: %v", err)
		}
		if ref != resolutionWorkflowRef || digest != registered.Digest {
			t.Fatalf("instance pin = %s %s, want the caller registry's %s %s", ref, digest, resolutionWorkflowRef, registered.Digest)
		}
	})

	t.Run("contract and action prefix folds carry the same registry", func(t *testing.T) {
		t.Parallel()
		const workID = "resolution-prefix"
		definition := resolutionPolicyDefinition(t)
		registry, registered := newResolutionRegistry(t, definition)
		s, owner := seedResolutionSelection(t, workID, registry, registered)
		recordResolutionActors(t, s, workID)
		advanceResolutionToExecution(t, s, registry, workID, owner, []string{})
		var contractRef, contractDigest, domains string
		if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_digest,decision_domains FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&contractRef, &contractDigest, &domains); err != nil {
			t.Fatalf("contract fold did not record the approved contract: %v", err)
		}
		if contractRef != resolutionWorkflowRef || contractDigest != registered.Digest {
			t.Fatalf("contract pin = %s %s, want the caller registry's pin", contractRef, contractDigest)
		}
		if domains != "[]" {
			t.Fatalf("contract decision_domains = %s, want the explicit engineering fact []", domains)
		}
		var step string
		if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
			t.Fatal(err)
		}
		if step != "execution" {
			t.Fatalf("current step after the seeded prefix = %q, want execution", step)
		}
	})

	t.Run("both confirmation authorities resolve the verified definition", func(t *testing.T) {
		t.Parallel()
		registry, definition := BuiltinWorkflowRegistry(), mustBuiltinDefinition(t, "workflow.implementation").Definition
		const engineeringID = "resolution-confirm-engineering"
		s, _, _, _, coordinator := engineeringConfirmationSeed(t, engineeringID, `[]`)
		events, err := confirmResolutionPremise(t, s, registry, definition, engineeringID, coordinator, nil, map[string]json.RawMessage{"selected_choice": json.RawMessage(`"confirm"`)})
		if err != nil {
			t.Fatalf("engineering confirmation refused: %v", err)
		}
		var payload struct {
			ConfirmingActorRef    string `json:"confirming_actor_ref"`
			ConfirmationAuthority string `json:"confirmation_authority"`
		}
		if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
			t.Fatal(err)
		}
		coordinatorRef := ownerRefForTest(t, coordinator)
		if payload.ConfirmingActorRef != coordinatorRef || payload.ConfirmationAuthority != "coordinator" {
			t.Fatalf("engineering confirmation recorded %q/%q, want the invoking coordinator with coordinator authority", payload.ConfirmingActorRef, payload.ConfirmationAuthority)
		}
		var confirmedBy string
		if err := s.DatabaseForTesting().QueryRow(`SELECT confirmed_by FROM workflow_premise_confirmations WHERE work_id=? AND contract_version=1`, engineeringID).Scan(&confirmedBy); err != nil {
			t.Fatalf("engineering confirmation row missing: %v", err)
		}
		if confirmedBy != coordinatorRef {
			t.Fatalf("confirmation row records %q, want %q", confirmedBy, coordinatorRef)
		}
		// Engineering takes no operator substitution.
		const substitutedID = "resolution-confirm-substituted"
		s2, _, _, operator2, coordinator2 := engineeringConfirmationSeed(t, substitutedID, `[]`)
		_, substitutionErr := confirmResolutionPremise(t, s2, registry, definition, substitutedID, coordinator2, &operator2, map[string]json.RawMessage{"selected_choice": json.RawMessage(`"confirm"`)})
		var substitutionFailure *Failure
		if substitutionErr == nil || !failureAs(substitutionErr, &substitutionFailure) || substitutionFailure.Kind != KindUnauthorized {
			t.Fatalf("engineering confirmation with an operator substitution returned %v, want the unauthorized refusal", substitutionErr)
		}
		// Decision-bearing keeps the verified operator and the human context.
		const decisionID = "resolution-confirm-decision"
		s3, _, _, operator3, coordinator3 := engineeringConfirmationSeed(t, decisionID, `["product"]`)
		_, missingOperatorErr := confirmResolutionPremise(t, s3, registry, definition, decisionID, coordinator3, nil, map[string]json.RawMessage{"selected_choice": json.RawMessage(`"confirm"`), "decision_context_digest": json.RawMessage(`"digest:human-question"`)})
		var missingOperatorFailure *Failure
		if missingOperatorErr == nil || !failureAs(missingOperatorErr, &missingOperatorFailure) || missingOperatorFailure.Kind != KindApprovalRequired {
			t.Fatalf("decision confirmation without an operator returned %v, want the approval requirement", missingOperatorErr)
		}
		if _, err := confirmResolutionPremise(t, s3, registry, definition, decisionID, coordinator3, &operator3, map[string]json.RawMessage{"selected_choice": json.RawMessage(`"confirm"`), "decision_context_digest": json.RawMessage(`"digest:human-question"`)}); err != nil {
			t.Fatalf("decision confirmation refused with the verified operator: %v", err)
		}
		// The decision fact rides contract 2, the reclassified successor.
		var confirmedOperator string
		if err := s3.DatabaseForTesting().QueryRow(`SELECT confirmed_by FROM workflow_premise_confirmations WHERE work_id=? AND contract_version=2`, decisionID).Scan(&confirmedOperator); err != nil {
			t.Fatal(err)
		}
		if confirmedOperator != ownerRefForTest(t, operator3) {
			t.Fatalf("decision confirmation row records %q, want the verified operator", confirmedOperator)
		}
	})

	t.Run("acceptance deliverables read the pinned definition", func(t *testing.T) {
		t.Parallel()
		definition := mustBuiltinDefinition(t, "workflow.implementation").Definition
		readDeliverables := func(t *testing.T, s *Store, definition WorkflowDefinition, workID string) error {
			t.Helper()
			tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			return requireAcceptanceDeliverables(context.Background(), tx, definition, workID)
		}
		// Before Main associates the accepted Review with the criteria the
		// deliverables check refuses.
		const unassociatedID = "resolution-deliverables-unassociated"
		s, _, _, _, _ := engineeringConfirmationFixture(t, unassociatedID, `[]`, false, true)
		var failure *Failure
		if err := readDeliverables(t, s, definition, unassociatedID); !failureAs(err, &failure) || failure.Kind != KindMissingEvidence {
			t.Fatalf("deliverables without the criterion association returned %v, want the missing-evidence refusal", err)
		}
		const workID = "resolution-deliverables"
		s2, _, _, _, _ := engineeringConfirmationSeed(t, workID, `[]`)
		if err := readDeliverables(t, s2, definition, workID); err != nil {
			t.Fatalf("deliverables refused with the criterion association recorded: %v", err)
		}
		// The stored contract's approved predicates are validated against the
		// definition the owner hands in: a definition that would not have
		// approved the stored outcome refuses.
		mismatched := definition
		mismatched.OutcomeSchema.AllowedKinds = []PredicateKind{PredicateAbsent}
		var shapeFailure *Failure
		if err := readDeliverables(t, s2, mismatched, workID); !failureAs(err, &shapeFailure) || shapeFailure.Kind != KindInvariantViolation || !strings.Contains(shapeFailure.Detail, "outside the pinned definition") {
			t.Fatalf("deliverables under a definition that disapproves the stored outcome returned %v, want the pinned-definition refusal", err)
		}
	})

	t.Run("decision record checkpoint folds against the pinned definition", func(t *testing.T) {
		t.Parallel()
		const workID = "resolution-decision-record"
		definition := resolutionPolicyDefinition(t)
		registry, registered := newResolutionRegistry(t, definition)
		s, owner := seedResolutionSelection(t, workID, registry, registered)
		advanceResolutionPrefix(t, s, registry, workID, owner, []string{}, false)
		ownerRef := ownerRefForTest(t, owner)
		version := readWorkVersion(t, s, workID)
		checkpoint := resolutionEvent("resolution-decision-"+workID, WorkflowActionCheckpointed, workID, ownerRef, version, map[string]any{
			"step_id": "execution", "step_kind": "external_effect", "attempt_epoch": 1,
			"checkpoint_payload": map[string]any{
				"action_id": "record_decision",
				"fields": map[string]any{
					"question": "adopt the delivered change?", "options_considered": []string{"adopt the delivered change", "reject the delivered change"},
					"decision": "accepted_decision", "rationale": "the stored independent verdict holds",
					"consequences": []string{"the delivered change ships"}, "inputs": []string{"the delivered evidence"},
					"poc_findings": "no prototype was required",
				},
			},
			"resume_cursor": "", "actor_ref": ownerRef, "request_id": "request:resolution-decision-" + workID,
		})
		appendWorkflowEventsWithRegistry(t, s, registry, Operation{
			Events:           []Event{checkpoint},
			ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version},
		})
		var question, decision string
		if err := s.DatabaseForTesting().QueryRow(`SELECT question,decision FROM workflow_decision_records WHERE work_id=?`, workID).Scan(&question, &decision); err != nil {
			t.Fatalf("the declared-fields decision record did not fold under the caller registry: %v", err)
		}
		if question != "adopt the delivered change?" || decision != "accepted_decision" {
			t.Fatalf("decision record = %q/%q, want the folded record", question, decision)
		}
	})

	t.Run("durable producer claims and completes under the local pin", func(t *testing.T) {
		t.Parallel()
		const workID = "resolution-producer"
		definition := resolutionPolicyDefinition(t)
		registry, registered := newResolutionRegistry(t, definition)
		s, owner := seedResolutionSelection(t, workID, registry, registered)
		// The seeded chain claims and completes the execution step's producer
		// operation through the fence entry points and binds both evidence
		// kinds against that completed operation.
		advanceResolutionToExecution(t, s, registry, workID, owner, []string{})
		var resultKind, evidenceRefs string
		if err := s.DatabaseForTesting().QueryRow(`SELECT result_kind,evidence_refs FROM durable_operations WHERE op_id=? AND attempt_epoch=1`, "resolution-produce-"+workID).Scan(&resultKind, &evidenceRefs); err != nil {
			t.Fatalf("the seeded durable producer operation is missing: %v", err)
		}
		if resultKind != "completed" {
			t.Fatalf("durable producer result_kind = %q, want completed", resultKind)
		}
		for _, subject := range []string{"evidence:operator-verdict", "evidence:operator-review"} {
			if !strings.Contains(evidenceRefs, subject) {
				t.Fatalf("durable producer evidence %q does not carry the bound subject %s", evidenceRefs, subject)
			}
		}
		// A claim naming another step of the same workflow refuses against the
		// stored pin.
		if _, err := claimStepObservedAuthorizedWithRegistry(context.Background(), s, ClaimRequest{
			OpID: "resolution-claim-wrong-step-" + workID, WorkID: workID,
			WorkflowTypeRef: resolutionWorkflowRef, WorkflowTypeDigest: registered.Digest,
			StepID: "proposal", StepKind: StepInternalSQLite,
			AcceptedInputsDigest: "sha256:" + strings.Repeat("d", 64), AcceptedScopeSnapshot: `{}`,
			PrincipalRef: owner.PrincipalRef, Tool: "workflow-resolution-test", IdempotencyKey: "resolution-claim-wrong-step-" + workID,
			RequestID: "request:resolution-claim-wrong-step-" + workID, ObservedAt: time.Unix(11, 3).UTC(), ContractDigest: testManifestDigest,
		}, true, nil, nil, registry); err == nil {
			t.Fatal("claim with a step outside the pinned current step was accepted")
		}
		// Completing a stale attempt epoch refuses and leaves the current
		// attempt without a result.
		next, err := claimStepObservedAuthorizedWithRegistry(context.Background(), s, ClaimRequest{
			OpID: "resolution-produce-" + workID, WorkID: workID,
			WorkflowTypeRef: resolutionWorkflowRef, WorkflowTypeDigest: registered.Digest,
			StepID: "execution", StepKind: StepExternalEffect,
			AcceptedInputsDigest: "sha256:" + strings.Repeat("e", 64), AcceptedScopeSnapshot: `{}`,
			PrincipalRef: owner.PrincipalRef, Tool: "workflow-resolution-test", IdempotencyKey: "resolution-produce-retry-" + workID,
			RequestID: "request:resolution-produce-" + workID, ObservedAt: time.Unix(11, 4).UTC(), ContractDigest: testManifestDigest,
		}, true, nil, nil, registry)
		if err != nil {
			t.Fatalf("retry claim refused under the local pin: %v", err)
		}
		if _, err := completeStepWithRegistry(context.Background(), s, CompleteRequest{
			OpID: next.OpID, AttemptEpoch: 1, ResultKind: ResultCompleted,
			PrincipalRef: owner.PrincipalRef, Tool: "workflow-resolution-test", IdempotencyKey: "resolution-produce-stale-" + workID,
			RequestID: "request:resolution-produce-" + workID, ObservedAt: time.Unix(11, 5).UTC(),
		}, true, nil, registry); err == nil {
			t.Fatal("completion with a stale attempt epoch was accepted")
		}
		var pending int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM durable_operations WHERE op_id=? AND attempt_epoch=? AND result_kind IS NULL`, next.OpID, next.AttemptEpoch).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		if pending != 1 {
			t.Fatalf("the stale completion touched the current attempt: %d open rows, want 1", pending)
		}
	})

	t.Run("transaction binding refuses a scope from another transaction before any write", func(t *testing.T) {
		t.Parallel()
		const workID = "resolution-foreign-scope"
		s := openTemp(t)
		seedWork(t, s, workID)
		seedWorkflowLaw(t, s)
		version := readWorkVersion(t, s, workID)
		// The builtin fixture pin verifies in every phase, so the only
		// behavior under test is the scope's transaction binding.
		fixture := workflowFixtureDefinition(t, 1)
		operation := Operation{
			Events: []Event{resolutionEvent("resolution-foreign-selection-"+workID, WorkflowDefinitionSelected, workID, "actor:test", version, map[string]any{
				"ref": fixture.Definition.Ref, "digest": fixture.Digest, "work_kind": string(fixture.Definition.WorkKind),
			})},
			ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version},
		}
		// The store pools one connection: the foreign transaction lives on a
		// second store so both transactions stay open beside each other.
		foreign := openTemp(t)
		foreignTx, err := foreign.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer foreignTx.Rollback()
		var baseline int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=?`, workID).Scan(&baseline); err != nil {
			t.Fatal(err)
		}
		foreignScope := newFoldScope(foreignTx)
		targetTx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer targetTx.Rollback()
		appendErr := applyWorkflowOperationTxWithRegistry(context.Background(), targetTx, operation, foreignScope, BuiltinWorkflowRegistry())
		var scopeFailure *Failure
		switch {
		case appendErr == nil:
			t.Fatal("an operation accepted a fold scope owned by another transaction")
		case !failureAs(appendErr, &scopeFailure):
			t.Fatalf("foreign-scope append returned %v, want a typed refusal", appendErr)
		case scopeFailure.Kind != KindInvariantViolation:
			t.Fatalf("foreign-scope append refused with %v, want the typed transaction-binding refusal", appendErr)
		}
		if err := targetTx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if err := foreignTx.Rollback(); err != nil {
			t.Fatal(err)
		}
		var instances int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_instances WHERE work_id=?`, workID).Scan(&instances); err != nil {
			t.Fatal(err)
		}
		if instances != 0 {
			t.Fatalf("the foreign-scope append left %d instance rows, want no terminal effect", instances)
		}
		var appended int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=?`, workID).Scan(&appended); err != nil {
			t.Fatal(err)
		}
		if appended != baseline {
			t.Fatalf("the foreign-scope append wrote %d events before refusing (baseline %d), want the refusal before any write", appended, baseline)
		}
		var itemVersion int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&itemVersion); err != nil {
			t.Fatal(err)
		}
		if itemVersion != version {
			t.Fatalf("the foreign-scope append moved the work item to version %d, want %d", itemVersion, version)
		}
		assertNoFoldGuardRows(t, s)
	})

	t.Run("a scope bound by one operation refuses a conflicting second binding", func(t *testing.T) {
		t.Parallel()
		const workID = "resolution-bound-scope"
		s := openTemp(t)
		seedWork(t, s, workID)
		seedWorkflowLaw(t, s)
		version := readWorkVersion(t, s, workID)
		// Both registries carry the same builtin fixture shape, so only the
		// binding conflict can refuse: one open transaction, one scope, two
		// different definition registries.
		fixture := workflowFixtureDefinition(t, 1)
		second := NewWorkflowDefinitionRegistry()
		if _, err := second.Register(fixture.Definition); err != nil {
			t.Fatal(err)
		}
		selection := func(eventID string) Operation {
			return Operation{
				Events: []Event{resolutionEvent(eventID, WorkflowDefinitionSelected, workID, "actor:test", version, map[string]any{
					"ref": fixture.Definition.Ref, "digest": fixture.Digest, "work_kind": string(fixture.Definition.WorkKind),
				})},
				ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version},
			}
		}
		var baseline int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=?`, workID).Scan(&baseline); err != nil {
			t.Fatal(err)
		}
		tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		boundScope := newFoldScope(tx)
		if err := applyWorkflowOperationTxWithRegistry(context.Background(), tx, selection("resolution-bound-first-"+workID), boundScope, BuiltinWorkflowRegistry()); err != nil {
			t.Fatalf("the first append on its own registry refused: %v", err)
		}
		rebindingErr := applyWorkflowOperationTxWithRegistry(context.Background(), tx, selection("resolution-bound-second-"+workID), boundScope, second)
		var bindingFailure *Failure
		switch {
		case rebindingErr == nil:
			t.Fatal("a scope already bound to one definition registry accepted a conflicting second registry")
		case !failureAs(rebindingErr, &bindingFailure):
			t.Fatalf("conflicting binding append returned %v, want a typed refusal", rebindingErr)
		case bindingFailure.Kind != KindInvariantViolation:
			t.Fatalf("conflicting binding append refused with %v, want the typed binding refusal", rebindingErr)
		}
		// The store pools one connection: the in-transaction count reads the
		// open transaction, never the pool beside it.
		var appended int
		if err := tx.QueryRowContext(context.Background(), `SELECT count(*) FROM domain_events WHERE subject_id=?`, workID).Scan(&appended); err != nil {
			t.Fatal(err)
		}
		if appended != baseline+1 {
			t.Fatalf("the conflicting binding appended %d events (baseline %d), want only the first append's one event", appended, baseline)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		var instances int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_instances WHERE work_id=?`, workID).Scan(&instances); err != nil {
			t.Fatal(err)
		}
		if instances != 0 {
			t.Fatalf("the conflicting binding left %d instance rows, want no terminal effect", instances)
		}
		assertNoFoldGuardRows(t, s)
	})

	t.Run("wrong and missing registries refuse without a builtin fallback", func(t *testing.T) {
		t.Parallel()
		const missingID = "resolution-registry-missing"
		definition := resolutionPolicyDefinition(t)
		_, registered := newResolutionRegistry(t, definition)
		empty := NewWorkflowDefinitionRegistry()
		s, owner := resolutionSeedStore(t, missingID)
		missingErr := tryAppendWorkflowEventsWithRegistry(s, empty, resolutionSelectionOperation(t, s, missingID, registered, owner))
		var missingFailure *Failure
		if missingErr == nil || !failureAs(missingErr, &missingFailure) {
			t.Fatalf("selection with a registry missing the definition returned %v, want a typed refusal", missingErr)
		}
		if err := s.DatabaseForTesting().QueryRow(`SELECT 1 FROM workflow_instances WHERE work_id=?`, missingID).Scan(new(any)); err == nil {
			t.Fatal("a caller registry missing the definition produced the instance through the builtin fallback")
		}
		// A registry that omits the builtin pin refuses a builtin-ref selection
		// instead of falling back to the builtin registry.
		const builtinID = "resolution-registry-builtin"
		fixture := workflowFixtureDefinition(t, 1)
		emptyForBuiltin := NewWorkflowDefinitionRegistry()
		s3, owner3 := resolutionSeedStore(t, builtinID)
		builtinVersion := readWorkVersion(t, s3, builtinID)
		builtinErr := tryAppendWorkflowEventsWithRegistry(s3, emptyForBuiltin, Operation{
			Events: []Event{resolutionEvent("resolution-selection-"+builtinID, WorkflowDefinitionSelected, builtinID, ownerRefForTest(t, owner3), builtinVersion, map[string]any{
				"ref": fixture.Definition.Ref, "digest": fixture.Digest, "work_kind": string(fixture.Definition.WorkKind),
			})},
			ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, builtinID): builtinVersion},
		})
		var builtinFailure *Failure
		if builtinErr == nil || !failureAs(builtinErr, &builtinFailure) {
			t.Fatalf("selection pinning the builtin ref under a registry that omits it returned %v, want a typed refusal", builtinErr)
		}
		if err := s3.DatabaseForTesting().QueryRow(`SELECT 1 FROM workflow_instances WHERE work_id=?`, builtinID).Scan(new(any)); err == nil {
			t.Fatal("a registry omitting the builtin pin produced the instance through the builtin fallback")
		}
		assertNoFoldGuardRows(t, s3)
		const driftID = "resolution-registry-drift"
		drifted := registered
		drifted.Digest = "sha256:" + strings.Repeat("f", 64)
		driftRegistry, _ := newResolutionRegistry(t, definition)
		s2, owner2 := resolutionSeedStore(t, driftID)
		driftErr := tryAppendWorkflowEventsWithRegistry(s2, driftRegistry, resolutionSelectionOperation(t, s2, driftID, drifted, owner2))
		var driftFailure *Failure
		if driftErr == nil || !failureAs(driftErr, &driftFailure) {
			t.Fatalf("selection with a drifted digest returned %v, want a typed refusal", driftErr)
		}
		if err := s2.DatabaseForTesting().QueryRow(`SELECT 1 FROM workflow_instances WHERE work_id=?`, driftID).Scan(new(any)); err == nil {
			t.Fatal("a digest-mismatched pin produced the instance without the drift refusal")
		}
		assertNoFoldGuardRows(t, s2)
	})
}

// assertNoFoldGuardRows holds the guard lifetime invariant after every
// refused operation: no committed guard row survives a closed scope.
func assertNoFoldGuardRows(t *testing.T, s *Store) {
	t.Helper()
	var guards int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM fold_guard`).Scan(&guards); err != nil {
		t.Fatal(err)
	}
	if guards != 0 {
		t.Fatalf("%d fold guard rows survived a refused operation", guards)
	}
}
