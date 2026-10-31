package store

// The terminal conformance witness for the workflow's confirmation exit: an
// engineering contract reaches the terminal state through the public action
// route — ApplyWorkflowActionTx → applyCompleteWorkflowActionTx →
// CompleteWorkflowTxWithRegistry → the registered terminal fold → the
// lifecycle fold — with the executing coordinator, the actor the recorded
// execution history names, recording the confirmation and the completion.
//
// The seed holds one rule: every state the witnesses read is folded from
// events through the local consequence_v1 registry — selection, contract,
// action prefix, durable producer, evidence binding, investigation
// observation, verdict, confirmation, completion. No projection row,
// definition pin, step position, or durable authority row is written
// directly, and no builtin registry entry is mutated.
//
import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// approvalRouteDefinition copies the local resolution definition and ships
// the route's contract-authoring declarations: the initial approve_contract
// action carries the required decision-domain fact the live composition
// build appends.
func approvalRouteDefinition(t *testing.T) WorkflowDefinition {
	t.Helper()
	definition := resolutionPolicyDefinition(t)
	for index := range definition.ActionDefinitions {
		switch definition.ActionDefinitions[index].ID {
		case "approve_contract":
			action := currentActionDefinition("approve_contract", true)
			action.Payload.Fields = append(slices.Clone(action.Payload.Fields), workflowDecisionFactField())
			definition.ActionDefinitions[index] = action
		}
	}
	return definition
}

// runCoordinatorActionTx drives one workflow action through the public
// ApplyWorkflowActionTx entry under the caller registry, the same transaction
// entry the owning boundary uses. The store pools one connection, so the
// transaction is the store's own Transact scope and the action commits or
// rolls back as one unit. It returns the refusal so negative callers can
// assert it.
//
// A terminal action consumes the sealed subject preparation of the item's
// active claimed worktree, the same preparation the host seals before the
// boundary call.
func runCoordinatorActionTx(t *testing.T, s *Store, registry DefinitionRegistry, workID, actionID string, actor WorkflowActor, payload string) error {
	t.Helper()
	ctx := context.Background()
	if actionID == "approve_contract" || actionID == "supersede_contract" || actionID == "complete" {
		verified, err := s.EstablishKnowledgeSourceSetProof(ctx, workID)
		if err != nil {
			return err
		}
		ctx = verified
	}
	var worktree string
	if WorkflowActionRequiresSubjectFreshness(actionID) && s.DatabaseForTesting().QueryRow(`SELECT path FROM worktree_entries WHERE set_id=? AND state='active' LIMIT 1`, WorktreeSetID(workID)).Scan(&worktree) == nil {
		prepared, err := PrepareWorkflowAcceptanceFreshness(ctx, s, PrepareWorkflowSubjectRequest{WorkID: workID})
		if err != nil {
			return err
		}
		ctx = WithWorkflowSubjectPreparation(ctx, prepared)
	}
	version := readWorkVersion(t, s, workID)
	request := WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: actionID,
		Actor:                actor,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("b", 64) + strconv.FormatInt(version, 10),
		IdempotencyIdentity:  actionID + "-approval-" + workID + "-" + strconv.FormatInt(version, 10),
		OperationID:          actionID + "-approval-" + workID + "-" + strconv.FormatInt(version, 10),
		PrincipalRef:         actor.PrincipalRef, Tool: "concord_work_transition",
		IdempotencyKey: actionID + "-approval-" + workID + "-" + strconv.FormatInt(version, 10),
		RequestID:      "request:" + actionID + "-approval-" + workID + "-" + strconv.FormatInt(version, 10),
		ContractDigest: testManifestDigest, Now: time.Unix(13, 0).UTC(),
	}
	if payload != "" {
		request.Payload = json.RawMessage(payload)
	}
	return s.Transact(ctx, func(tx *Transaction) error {
		_, err := ApplyWorkflowActionTx(ctx, tx, registry, request)
		return err
	})
}

// approvalKindEvidenceCount counts approval-kind evidence bindings on one work
// item. Neither witness fabricates or consumes approval-kind evidence.
func approvalKindEvidenceCount(t *testing.T, s *Store, workID string) int {
	t.Helper()
	var bound int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_evidence_bindings WHERE work_id=? AND json_extract(payload,'$.evidence_kind')='approval'`, workID).Scan(&bound); err != nil {
		t.Fatalf("read approval-kind evidence: %v", err)
	}
	return bound
}

// foldedApprovalAdmission loads the tx-scoped admission state for one work
// item through the shared loader, in one short read transaction so the pooled
// connection is released before the next store call.
func foldedApprovalAdmission(t *testing.T, s *Store, registry DefinitionRegistry, definition WorkflowDefinition, workID string) WorkflowAdmissionState {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var step string
	if err := tx.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	state, _, err := loadWorkflowAdmissionStateTxForRegistry(ctx, tx, workID, definition, step, "workflow_approval_terminal_test", registry)
	if err != nil {
		t.Fatalf("fold admission state: %v", err)
	}
	return state
}

func assertApprovalConfirmationReceipt(t *testing.T, s *Store, workID, confirmingActor, authority string, contractVersion, expectedVersion int64) {
	t.Helper()
	var got string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload_digest FROM effect_receipts WHERE subject_id=? AND kind=? ORDER BY effect_seq DESC LIMIT 1`, workID, WorkflowPremiseConfirmed).Scan(&got); err != nil {
		t.Fatalf("read confirmation receipt: %v", err)
	}
	want, err := effectPayloadDigest(mustJSONValue(map[string]any{"work_id": workID, "expected_version": expectedVersion, "resulting_version": expectedVersion + 1, "contract_version": contractVersion, "confirming_actor_ref": confirmingActor, "confirmation_authority": authority}))
	if err != nil || got != want {
		t.Fatalf("confirmation digest=%q, want exact actor/authority payload %q with no approval or operator fields: %v", got, want, err)
	}
}

// latestApprovalEventActor reads the latest event of one kind's recorded
// actor from its immutable receipt header.
func latestApprovalEventActor(t *testing.T, s *Store, workID, kind string) string {
	t.Helper()
	var actor string
	if err := s.DatabaseForTesting().QueryRow(`SELECT actor FROM effect_receipts WHERE subject_id=? AND kind=? ORDER BY effect_seq DESC LIMIT 1`, workID, kind).Scan(&actor); err != nil {
		t.Fatalf("read latest %s actor: %v", kind, err)
	}
	return actor
}

// approvalExecutionActor reads the instance's recorded execution actor.
func approvalExecutionActor(t *testing.T, s *Store, workID string) string {
	t.Helper()
	var actor string
	if err := s.DatabaseForTesting().QueryRow(`SELECT execution_actor_ref FROM workflow_instances WHERE work_id=?`, workID).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	return actor
}

// approvalPremiseConfirmationRow reads one confirmation row's confirmed_by; ok
// reports whether the row exists.
func approvalPremiseConfirmationRow(t *testing.T, s *Store, workID string, contractVersion int64) (confirmedBy string, ok bool) {
	t.Helper()
	if err := s.DatabaseForTesting().QueryRow(`SELECT confirmed_by FROM workflow_premise_confirmations WHERE work_id=? AND contract_version=?`, workID, contractVersion).Scan(&confirmedBy); err != nil {
		return "", false
	}
	return confirmedBy, true
}

// approvalWorkAndInstanceState reads the two terminal projections.
func approvalWorkAndInstanceState(t *testing.T, s *Store, workID string) (lifecycle, instanceState string) {
	t.Helper()
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, workID).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&instanceState); err != nil {
		t.Fatal(err)
	}
	return lifecycle, instanceState
}

// approvalEventCount counts one work item's events, the rollback witness.
func approvalEventCount(t *testing.T, s *Store, workID string) int {
	t.Helper()
	var events int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=?`, workID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	return events
}

// reviewerOperatorCoordinator carries the seeded actor triple with its refs.
type reviewerOperatorCoordinator struct {
	reviewer       WorkflowActor
	operator       WorkflowActor
	coordinator    WorkflowActor
	reviewerRef    string
	operatorRef    string
	coordinatorRef string
}

// recordApprovalActors records the reviewer/operator/coordinator triple on the
// seeded instance through the real actor-recorded events. None of the three
// executes the seeded work: the executing coordinator is the seed's owner.
func recordApprovalActors(t *testing.T, s *Store, workID string) reviewerOperatorCoordinator {
	t.Helper()
	reviewer, operator, coordinator := recordResolutionActors(t, s, workID)
	return reviewerOperatorCoordinator{
		reviewer: reviewer, operator: operator, coordinator: coordinator,
		reviewerRef:    ownerRefForTest(t, reviewer),
		operatorRef:    ownerRefForTest(t, operator),
		coordinatorRef: ownerRefForTest(t, coordinator),
	}
}

// seedApprovalItemAtAcceptance parks a builtin workflow.implementation item at
// acceptance through rAcceptance, with Main's criterion association and the
// recorded investigation, then records the actor triple. domains is the
// active contract's decision fact. The owner it returns is the executing
// coordinator.
func seedApprovalItemAtAcceptance(t *testing.T, workID string, domains []string) (*Store, DefinitionRegistry, WorkflowDefinition, WorkflowActor) {
	t.Helper()
	f := rAcceptance(t, workID, domains, true, true)
	recordApprovalActors(t, f.store, workID)
	return f.store, BuiltinWorkflowRegistry(), mustBuiltinDefinition(t, "workflow.implementation").Definition, f.owner
}

// seedApprovalAbsentFactAtAcceptance seeds the consequence_v1 pin to the
// acceptance step with a contract whose stored decision fact is absent: the
// legacy absence shape. The absent contract is authored under a registered
// synthetic legacy twin of the route definition, the same ref at
// definition.Version, so the contract write
// keeps its legacy-shape absence; the legacy twin is the registry's only, and
// so current, registration while the item selects it and records its history.
// The investigation observation is recorded through its real fold. The higher
// same-ref consequence_v1 version is then registered for its actual digest, and
// a real WorkflowDefinitionSelected event carries the instance forward through
// the registered fold to it: the carry forward
// keeps the acceptance step, keeps the NULL historical contract authoritative
// with its recorded bytes, and keeps the owner as the executing actor. The
// seed asserts each preserved state, so a carry forward that rewrote history
// fails at the seed instead of downstream. No projection row, definition pin,
// step position, or durable authority row is written directly, and no builtin
// registry entry is mutated.
func seedApprovalAbsentFactAtAcceptance(t *testing.T, workID string) (*Store, DefinitionRegistry, WorkflowActor) {
	t.Helper()
	definition := approvalRouteDefinition(t)
	// The legacy twin is the same route without the declared decision-domain
	// fact: its approve_contract payload drops the required declaration, the
	// shape historical pins carry. The higher version below re-adds it.
	legacy := definition
	legacy.ActionDefinitions = slices.Clone(definition.ActionDefinitions)
	for index := range legacy.ActionDefinitions {
		if legacy.ActionDefinitions[index].ID != "approve_contract" {
			continue
		}
		action := legacy.ActionDefinitions[index]
		fields := make([]WorkflowPayloadField, 0, len(action.Payload.Fields))
		for _, field := range action.Payload.Fields {
			if field.Name == "decision_domains" {
				continue
			}
			fields = append(fields, field)
		}
		action.Payload.Fields = fields
		legacy.ActionDefinitions[index] = action
	}
	higher := definition
	registry, legacyRegistered := newResolutionRegistry(t, legacy)
	s, owner := seedResolutionSelection(t, workID, registry, legacyRegistered)
	recordApprovalActors(t, s, workID)
	ownerRef := ownerRefForTest(t, owner)
	version := readWorkVersion(t, s, workID)
	contractEvent := resolutionEvent("approval-absent-contract-"+workID, WorkflowContractApproved, workID, ownerRef, version+3, map[string]any{
		"contract_version": 1, "premise": "deliver the checked change",
		"outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
		"required_evidence": []string{"verification"}, "route_conventions": []string{},
		"spec_mandate": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
	})
	appendWorkflowEventsWithRegistry(t, s, registry, Operation{
		Events: []Event{
			workflowActionCompletedFixture("approval-absent-proposal-"+workID, workID, ownerRef, version, "proposal", "record_proposal"),
			workflowActionCompletedFixture("approval-absent-discovery-"+workID, workID, ownerRef, version+1, "discovery", "record_discovery"),
			workflowActionCompletedFixture("approval-absent-design-"+workID, workID, ownerRef, version+2, "design", "record_design"),
			contractEvent,
			workflowActionCompletedFixture("approval-absent-planning-"+workID, workID, ownerRef, version+4, "planning", "approve_contract"),
		},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version},
	})
	advanceResolutionDelivery(t, s, registry, workID, owner)
	seedResolutionInvestigation(t, s, workID)
	// The absent-fact state the carry forward must preserve: the contract
	// payload carries no decision_domains member, and the projection stores
	// NULL absence.
	var premise string
	var domainsAbsent int
	if err := s.DatabaseForTesting().QueryRow(`SELECT premise,decision_domains IS NULL FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&premise, &domainsAbsent); err != nil {
		t.Fatal(err)
	}
	var contractDigest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload_digest FROM effect_receipts WHERE subject_id=? AND kind=? AND event_id=?`, workID, WorkflowContractApproved, contractEvent.EventID).Scan(&contractDigest); err != nil {
		t.Fatalf("read the absent contract event: %v", err)
	}
	wantDigest, err := effectPayloadDigest(contractEvent.Payload)
	if err != nil || domainsAbsent != 1 || strings.Contains(string(contractEvent.Payload), "decision_domains") || contractDigest != wantDigest {
		t.Fatalf("the legacy contract NULL=%d digest=%q, want the exact absent-fact payload %q: %v", domainsAbsent, contractDigest, wantDigest, err)
	}
	// The higher version arrives after the legacy history, as an upgrade
	// does: the legacy definition was current when the item selected it.
	higherRegistered, err := registry.Register(higher)
	if err != nil {
		t.Fatalf("the higher consequence_v1 definition refused registration: %v", err)
	}
	carryVersion := readWorkVersion(t, s, workID)
	appendWorkflowEventsWithRegistry(t, s, registry, Operation{
		Events: []Event{
			resolutionEvent("approval-absent-carry-"+workID, WorkflowDefinitionSelected, workID, ownerRef, carryVersion, map[string]any{
				"ref": higherRegistered.Definition.Ref, "digest": higherRegistered.Digest,
				"work_kind": string(higherRegistered.Definition.WorkKind),
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): carryVersion},
	})
	// The carry forward kept the acceptance step, the NULL historical
	// contract authoritative with its recorded bytes, and the executing
	// owner, and moved the pin to the registered higher version and digest.
	var carriedRef, carriedDigest, step string
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_digest,current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&carriedRef, &carriedDigest, &step); err != nil {
		t.Fatal(err)
	}
	if carriedRef != higherRegistered.Definition.Ref || carriedDigest != higherRegistered.Digest {
		t.Fatalf("the carry forward pinned %s %s, want the registered %s %s", carriedRef, carriedDigest, higherRegistered.Definition.Ref, higherRegistered.Digest)
	}
	if step != "acceptance" {
		t.Fatalf("the carry forward moved the step to %q, want acceptance", step)
	}
	var carriedPremise string
	var carriedDomainsAbsent, active int
	if err := s.DatabaseForTesting().QueryRow(`SELECT premise,decision_domains IS NULL,(SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL) FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID, workID).Scan(&carriedPremise, &carriedDomainsAbsent, &active); err != nil {
		t.Fatal(err)
	}
	if carriedPremise != premise || carriedDomainsAbsent != 1 {
		t.Fatalf("the carry forward changed the historical contract to %q (NULL absence %d), want %q preserved with NULL absence", carriedPremise, carriedDomainsAbsent, premise)
	}
	if active != 1 {
		t.Fatalf("the carry forward left %d active contracts, want the absent-fact contract kept authoritative", active)
	}
	var carriedContractDigest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload_digest FROM effect_receipts WHERE subject_id=? AND kind=? AND event_id=?`, workID, WorkflowContractApproved, contractEvent.EventID).Scan(&carriedContractDigest); err != nil {
		t.Fatalf("reread the absent contract event: %v", err)
	}
	if carriedContractDigest != contractDigest {
		t.Fatalf("the carry forward rewrote the absent contract digest to %q, want %q preserved", carriedContractDigest, contractDigest)
	}
	if executionActor := approvalExecutionActor(t, s, workID); executionActor != ownerRef {
		t.Fatalf("the carry forward moved the execution actor to %q, want the owner %s", executionActor, ownerRef)
	}
	return s, registry, owner
}

// assertNoApprovalEffect holds the refusal invariant: the work version and the
// work item's event count did not move, no confirmation row appeared, and no
// write guard row survived.
func assertNoApprovalEffect(t *testing.T, s *Store, workID string, baselineVersion, baselineEvents int64) {
	t.Helper()
	if version := readWorkVersion(t, s, workID); version != baselineVersion {
		t.Fatalf("the refused action advanced the work version from %d to %d", baselineVersion, version)
	}
	if events := int64(approvalEventCount(t, s, workID)); events != baselineEvents {
		t.Fatalf("the refused action left %d events (baseline %d), want rollback with no append", events, baselineEvents)
	}
	if _, present := approvalPremiseConfirmationRow(t, s, workID, 1); present {
		t.Fatal("the refused action recorded a premise confirmation row")
	}
	assertNoFoldGuardRows(t, s)
}

// approvalContinuityProjectionError returns the continuity projection's
// refusal so negative callers can assert it.
func approvalContinuityProjectionError(t *testing.T, s *Store, registry DefinitionRegistry, workID string) error {
	t.Helper()
	_, err := approvalContinuityProjection(t, s, registry, workID)
	return err
}

// approvalContinuityProjection seats one continuity snapshot's work pin
// through the registry-threaded continuity instance phase.
func approvalContinuityProjection(t *testing.T, s *Store, registry DefinitionRegistry, workID string) (*WorkPin, error) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var snapshot ContinuitySnapshot
	if err := readContinuityInstanceForRegistryTx(ctx, tx, workID, registry, &snapshot); err != nil {
		return nil, err
	}
	return snapshot.WorkPin, nil
}

func TestCoordinatorConfirmationTerminalEngineering(t *testing.T) {
	t.Parallel()

	t.Run("engineering completes through the public action route", func(t *testing.T) {
		t.Parallel()
		const workID = "approval-terminal-engineering"
		s, registry, definition, owner := seedApprovalItemAtAcceptance(t, workID, []string{})
		ownerRef := ownerRefForTest(t, owner)

		if kind := approvalKindEvidenceCount(t, s, workID); kind != 0 {
			t.Fatalf("the witness recorded %d approval-kind evidence bindings; none is fabricated", kind)
		}

		// The model and the fold agree: the coordinator confirmation is a real
		// exit.
		conformanceCheckpoint(t, s, registry, workID, definition, rAcceptanceModel(), "engineering at acceptance")
		state := foldedApprovalAdmission(t, s, registry, definition, workID)
		if decision := workflowAdmit(definition, state, "confirm_premise"); !decision.Admitted {
			t.Fatalf("engineering confirm_premise refused at admission: %v", decision.Failure)
		}

		// The executing coordinator confirms through the public action with no
		// operator actor.
		confirmationVersion := readWorkVersion(t, s, workID)
		if err := runCoordinatorActionTx(t, s, registry, workID, "confirm_premise", owner, `{}`); err != nil {
			t.Fatalf("engineering coordinator confirmation refused through the public action: %v", err)
		}
		confirmed, ok := approvalPremiseConfirmationRow(t, s, workID, 1)
		if !ok || confirmed != ownerRef {
			t.Fatalf("engineering confirmation row = %q (present %v), want the executing coordinator %s", confirmed, ok, ownerRef)
		}
		if eventActor := latestApprovalEventActor(t, s, workID, WorkflowPremiseConfirmed); eventActor != ownerRef {
			t.Fatalf("confirmation event actor = %q, want the executing coordinator %s", eventActor, ownerRef)
		}
		assertApprovalConfirmationReceipt(t, s, workID, ownerRef, "coordinator", 1, confirmationVersion)

		// The executing coordinator completes through the real public complete
		// store action; the registered terminal fold and the lifecycle fold
		// close both projections under the same actor.
		bindCompletionClosure(t, s, workID, owner)
		if err := runCoordinatorActionTx(t, s, registry, workID, "complete", owner, `{"impact_verdict":"non-breaking"}`); err != nil {
			t.Fatalf("public complete action refused for engineering work: %v", err)
		}
		lifecycle, instanceState := approvalWorkAndInstanceState(t, s, workID)
		if lifecycle != "completed" || instanceState != "completed" {
			t.Fatalf("terminal close left lifecycle=%q instance=%q, want both completed", lifecycle, instanceState)
		}
		if eventActor := latestApprovalEventActor(t, s, workID, WorkflowCompleted); eventActor != ownerRef {
			t.Fatalf("completion event actor = %q, want the executing coordinator %s", eventActor, ownerRef)
		}
		if kind := approvalKindEvidenceCount(t, s, workID); kind != 0 {
			t.Fatalf("engineering witness left %d approval-kind evidence bindings", kind)
		}
		assertNoFoldGuardRows(t, s)
	})

	t.Run("engineering confirmation refuses on the recorded investigation", func(t *testing.T) {
		t.Parallel()
		const workID = "approval-terminal-engineering-investigation"
		f := rAcceptance(t, workID, []string{}, true, false)
		s, registry, definition, owner := f.store, BuiltinWorkflowRegistry(), mustBuiltinDefinition(t, "workflow.implementation").Definition, f.owner
		// No investigation observation is recorded: the confirmation requires
		// it, and the refusal is that requirement's typed failure.
		baselineVersion := readWorkVersion(t, s, workID)
		baselineEvents := int64(approvalEventCount(t, s, workID))
		err := runCoordinatorActionTx(t, s, registry, workID, "confirm_premise", owner, `{}`)
		var failure *Failure
		if err == nil || !failureAs(err, &failure) {
			t.Fatalf("engineering confirmation without the recorded investigation returned %v, want a typed refusal", err)
		}
		if failure.Kind != KindMissingEvidence {
			t.Fatalf("engineering confirmation without the recorded investigation refused with %v, want the investigation's typed missing-evidence refusal", err)
		}
		// The folded admission carries the same prerequisite refusal.
		state := foldedApprovalAdmission(t, s, registry, definition, workID)
		var foldedFailure *Failure
		if state.AcceptanceDeliverablesMissing == nil || !failureAs(state.AcceptanceDeliverablesMissing, &foldedFailure) || foldedFailure.Kind != KindMissingEvidence {
			t.Fatalf("the engineering fold carries %+v, want the folded investigation refusal", state.AcceptanceDeliverablesMissing)
		}
		assertNoApprovalEffect(t, s, workID, baselineVersion, baselineEvents)
	})

	t.Run("engineering confirmation refuses on missing acceptance deliverables", func(t *testing.T) {
		t.Parallel()
		const workID = "approval-terminal-engineering-deliverables"
		f := rAcceptance(t, workID, []string{}, false, true)
		s, registry, owner := f.store, BuiltinWorkflowRegistry(), f.owner
		// Main has not associated the accepted Review with the criteria: the
		// deliverables gate refuses.
		baselineVersion := readWorkVersion(t, s, workID)
		baselineEvents := int64(approvalEventCount(t, s, workID))
		err := runCoordinatorActionTx(t, s, registry, workID, "confirm_premise", owner, `{}`)
		var failure *Failure
		if err == nil || !failureAs(err, &failure) || failure.Kind != KindMissingEvidence {
			t.Fatalf("engineering confirmation without the criterion association returned %v, want the missing-evidence refusal", err)
		}
		assertNoApprovalEffect(t, s, workID, baselineVersion, baselineEvents)
	})

	t.Run("engineering completion cannot bypass the unconfirmed acceptance step", func(t *testing.T) {
		t.Parallel()
		const workID = "approval-terminal-engineering-unconfirmed"
		s, registry, _, owner := seedApprovalItemAtAcceptance(t, workID, []string{})
		baselineVersion := readWorkVersion(t, s, workID)
		baselineEvents := int64(approvalEventCount(t, s, workID))
		if err := runCoordinatorActionTx(t, s, registry, workID, "complete", owner, `{"impact_verdict":"non-breaking"}`); err == nil {
			t.Fatal("engineering completion bypassed the unconfirmed acceptance step")
		}
		assertNoApprovalEffect(t, s, workID, baselineVersion, baselineEvents)
	})

	t.Run("engineering fold preserves an earlier staleness behind a missing investigation", func(t *testing.T) {
		t.Parallel()
		const workID = "approval-terminal-engineering-stale-subject"
		f := rAcceptance(t, workID, []string{}, true, false)
		s, definition := f.store, mustBuiltinDefinition(t, "workflow.implementation").Definition
		// No investigation observation is recorded, and the acceptance
		// subject staleness folded first. Direct state over the real seed:
		// the engineering gate must preserve the earlier typed failure, not
		// overwrite it with the investigation's missing-evidence refusal.
		stale := newFailure(KindStaleRequiresReview, "workflow_action", "the accepted review subject is stale: the artifact was re-produced after its acceptance", false, "record a fresh verdict for the re-produced artifact")
		ctx := context.Background()
		tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		state := WorkflowAdmissionState{ActiveContracts: 1, Step: "acceptance", AcceptanceDeliverablesMissing: stale}
		if err := loadWorkflowAcceptanceAdmission(ctx, tx, workID, definition, &state); err != nil {
			tx.Rollback()
			t.Fatalf("the acceptance fold refused: %v", err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		var preserved *Failure
		if state.AcceptanceDeliverablesMissing == nil || !failureAs(state.AcceptanceDeliverablesMissing, &preserved) || preserved.Kind != KindStaleRequiresReview {
			t.Fatalf("the fold overwrote the earlier staleness with %+v, want the preserved stale-requires-review refusal", state.AcceptanceDeliverablesMissing)
		}
		decision := workflowAdmit(definition, state, "confirm_premise")
		var confirmFailure *Failure
		if decision.Admitted || !failureAs(decision.Failure, &confirmFailure) || confirmFailure.Kind != KindStaleRequiresReview {
			t.Fatalf("the confirmation behind the preserved staleness folded %v, want the stale-requires-review refusal", decision.Failure)
		}
	})
}

// registeredDigest reads the registered entry's digest for the local
// resolution definition family from the caller registry.
func registeredDigest(t *testing.T, registry DefinitionRegistry, definition WorkflowDefinition) string {
	t.Helper()
	entry, ok := registry.Current(definition.Ref)
	if !ok {
		t.Fatalf("the caller registry does not hold %s", definition.Ref)
	}
	return entry.Digest
}
