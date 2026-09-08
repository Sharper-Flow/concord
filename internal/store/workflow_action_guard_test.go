package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The guard table is the declared inventory of action-specific guards in the
// workflow action dispatcher. These tests bind the declaration to the actions
// that carry guards, the same way workflow_event_shape_test.go binds the
// declared event shapes to the dispatcher's control flow. A new guard is a
// deliberate declaration: adding one without extending this inventory, or
// extending the inventory without a guard, fails here first.

// guardedActions is the closed set of actions that carry an action-specific
// guard in applyWorkflowActionRawTx. Three guards are deliberately absent,
// because each applies to every action and the dispatcher therefore calls them
// directly: guardOperatorPremiseActor, guardRecordedActorTuple, and the
// spec-mandate guard.
var guardedActions = map[string]workflowActionGuardPhase{
	"supersede_contract":     guardPhaseRecovery,
	"complete":               guardPhaseBoundary,
	"link_successor":         guardPhasePostValidation,
	"cross_context_boundary": guardPhaseClaim,
	"record_delivery":        guardPhaseClaim,
}

func TestEveryGuardTableEntryIsARegisteredAction(t *testing.T) {
	for actionID := range workflowActionGuards {
		if _, ok := builtinActionPolicies[actionID]; !ok {
			t.Errorf("guard table names %q, which is not a registered action", actionID)
		}
	}
}

func TestGuardTableMatchesTheGuardedActionInventory(t *testing.T) {
	if len(workflowActionGuards) != len(guardedActions) {
		t.Fatalf("guard table has %d entries, want %d", len(workflowActionGuards), len(guardedActions))
	}
	for actionID, phase := range guardedActions {
		guard, ok := workflowActionGuards[actionID]
		if !ok {
			t.Errorf("%q carries a guard in the dispatcher but is absent from workflowActionGuards", actionID)
			continue
		}
		if guard.phase != phase {
			t.Errorf("%q is declared in phase %d, want %d", actionID, guard.phase, phase)
		}
		if guard.run == nil {
			t.Errorf("%q declares a nil guard function", actionID)
		}
	}
	for actionID := range workflowActionGuards {
		if _, ok := guardedActions[actionID]; !ok {
			t.Errorf("workflowActionGuards declares %q, which the guarded-action inventory does not list", actionID)
		}
	}
}

func TestOperatorActorIsRejectedOutsidePremiseConfirmation(t *testing.T) {
	g := &workflowActionGuardContext{
		request: WorkflowActionExecutionRequest{
			ActionID: "record_proposal",
			OperatorActor: &WorkflowActor{
				ActorClass: ActorOperator,
			},
		},
	}
	err := guardOperatorPremiseActor(g)
	if err == nil {
		t.Fatal("operator actor on a non-premise action passed, want unauthorized")
	}
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindUnauthorized {
		t.Fatalf("operator actor outside premise confirmation returned %v, want KindUnauthorized", err)
	}
}

func TestMandatedLawGuardNamesBindingRecoveryAndLeavesBindingAvailable(t *testing.T) {
	s := openTemp(t)
	workID := "mandate-guard-work"
	seedWork(t, s, workID)
	db := s.DatabaseForTesting()
	actorRef := DeriveWorkflowActorRef("principal/guard", "client/guard", "agent/guard", "session/guard")
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,?,?,?,?,?,?)`, actorRef, "principal/guard", "client/guard", "agent/guard", "session/guard", "agent", "now"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_instances(work_id,definition_ref,definition_version,definition_digest,current_step,instance_state,execution_actor_ref) VALUES(?, 'workflow.test', 1, ?, 'verify', 'running', ?)`, workID, testManifestDigest, actorRef); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,'guard mandate','internal_sqlite','[]','[]','now',?,'["law:required"]','[]',1,'prototype_internal')`, workID, actorRef); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	definition := WorkflowDefinition{StepGraph: WorkflowStepGraph{Steps: []WorkflowStep{
		{ID: "repair", Actions: []string{"bind_evidence"}},
		{ID: "verify", Actions: []string{"record_verdict", "confirm_premise"}},
	}}}
	err := guardMandatedWorkflowLawBound(context.Background(), s.db, workID, definition, "verify", "record_verdict", "workflow_action")
	var failure *Failure
	if err == nil || !failureAs(err, &failure) || !strings.Contains(failure.Detail, `spec mandate law "law:required" is not bound`) || !strings.Contains(failure.RecoveryAction, "present the law reference in the terminal action evidence") {
		t.Fatalf("mandate guard error=%v, want law and terminal-evidence recovery", err)
	}
	if err := guardMandatedWorkflowLawBound(context.Background(), s.db, workID, definition, "repair", "bind_evidence", "workflow_action"); err != nil {
		t.Fatalf("binding action refused recovery: %v", err)
	}
}

// Every builtin definition that declares bind_evidence must hold a contract
// with an unbound spec mandate at its binding step: no advancing action may
// leave that step, and no verdict, premise confirmation, or completion may run,
// until the mandate is bound. Once one evidence_bound event names the law, the
// same actions pass the guard. The walk pins the guard to every shipped
// definition rather than to one hand-built graph.
func TestMandatedContractGuardWalksEveryBuiltinDefinition(t *testing.T) {
	const lawID = "CD-0013"
	for _, definition := range BuiltinWorkflowDefinitions() {
		bindingStep := workflowEvidenceBindingStep(definition, "")
		if bindingStep == "" {
			continue
		}
		t.Run(definition.Ref, func(t *testing.T) {
			s := openTemp(t)
			workID := "mandate-walk-" + strings.ReplaceAll(definition.Ref, ".", "-")
			seedWork(t, s, workID)
			db := s.DatabaseForTesting()
			actorRef := DeriveWorkflowActorRef("principal/walk", "client/walk", "agent/walk", "session/walk")
			if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,?,?,?,?,?,?)`, actorRef, "principal/walk", "client/walk", "agent/walk", "session/walk", "agent", "now"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO workflow_instances(work_id,definition_ref,definition_version,definition_digest,current_step,instance_state,execution_actor_ref) VALUES(?,?,?,?,?,'running',?)`, workID, definition.Ref, definition.Version, testManifestDigest, bindingStep, actorRef); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,'walk mandate','internal_sqlite','[]','[]','now',?,?,'[]',1,'prototype_internal')`, workID, actorRef, `["`+lawID+`"]`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
				t.Fatal(err)
			}

			gated := []string{"record_verdict", "confirm_premise", "complete"}
			for _, actionID := range workflowStep(definition, bindingStep).Actions {
				if mode, ok := workflowActionExecutionMode(definition, actionID); ok && mode == ActionAdvance {
					gated = append(gated, actionID)
				}
			}
			if len(gated) == 3 {
				t.Fatalf("%s binding step %q declares no advancing action; the walk cannot prove the trap is closed", definition.Ref, bindingStep)
			}
			for _, actionID := range gated {
				err := guardMandatedWorkflowLawBound(context.Background(), s.db, workID, definition, bindingStep, actionID, "workflow_action")
				var failure *Failure
				if err == nil || !failureAs(err, &failure) || !strings.Contains(failure.Detail, `spec mandate law "`+lawID+`" is not bound`) || !strings.Contains(failure.RecoveryAction, `bind_evidence on step "`+bindingStep+`"`) {
					t.Fatalf("%s %s with unbound mandate: err=%v, want refusal naming %s and step %s", definition.Ref, actionID, err, lawID, bindingStep)
				}
			}
			if err := guardMandatedWorkflowLawBound(context.Background(), s.db, workID, definition, bindingStep, "bind_evidence", "workflow_action"); err != nil {
				t.Fatalf("%s bind_evidence must stay available while the mandate is unbound: %v", definition.Ref, err)
			}
			// An advance from any step before the binding step must pass, or a
			// definition whose bind_evidence sits on a later step could never
			// reach it (ops_runbook binds at execute, static_analysis at report).
			for _, step := range definition.StepGraph.Steps {
				if step.ID == bindingStep {
					break
				}
				for _, actionID := range step.Actions {
					if mode, ok := workflowActionExecutionMode(definition, actionID); ok && mode == ActionAdvance {
						if err := guardMandatedWorkflowLawBound(context.Background(), s.db, workID, definition, step.ID, actionID, "workflow_action"); err != nil {
							t.Fatalf("%s %s on pre-binding step %q must pass while unbound, got %v", definition.Ref, actionID, step.ID, err)
						}
					}
				}
			}

			if _, err := db.Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,1,?)`, "walk-bound-"+workID, WorkflowEvidenceBound, string(SubjectWorkItem), workID, actorRef, "now", `{"immutable_subject_ref":"`+lawID+`"}`); err != nil {
				t.Fatal(err)
			}
			for _, actionID := range gated {
				if err := guardMandatedWorkflowLawBound(context.Background(), s.db, workID, definition, bindingStep, actionID, "workflow_action"); err != nil {
					t.Fatalf("%s %s after binding %s: %v, want pass", definition.Ref, actionID, lawID, err)
				}
			}
		})
	}
}

func TestMandatedLawGuardAcceptsTerminalEvidenceWithoutReachableBindingStep(t *testing.T) {
	const workID = "mandate-terminal-evidence"
	s := openTemp(t)
	seedWork(t, s, workID)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	actorRef := DeriveWorkflowActorRef("principal/terminal", "client/terminal", "agent/terminal", "session/terminal")
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,?,?,?,?,?,?)`, actorRef, "principal/terminal", "client/terminal", "agent/terminal", "session/terminal", "agent", "now"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,'terminal mandate','internal_sqlite','[]','[]','now',?,'["law:terminal"]','[]',1,'prototype_internal')`, workID, actorRef); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	definition := WorkflowDefinition{StepGraph: WorkflowStepGraph{
		Steps: []WorkflowStep{{ID: "binding", Actions: []string{"bind_evidence"}}, {ID: "terminal", Actions: []string{"complete", "record_verdict"}}},
		Edges: []WorkflowEdge{{From: "binding", To: "terminal", Kind: WorkflowEdgeForward}},
	}}
	if err := guardMandatedWorkflowLawBound(context.Background(), s.db, workID, definition, "terminal", "complete", "complete_workflow", json.RawMessage(`{"evidence_refs":["law:terminal"]}`)); err != nil {
		t.Fatalf("terminal evidence did not satisfy the mandate guard: %v", err)
	}
	if err := guardMandatedWorkflowLawBoundWithEvidence(context.Background(), s.db, workID, definition, "terminal", "complete", "complete_workflow", nil, []string{"law:terminal"}); err != nil {
		t.Fatalf("top-level terminal evidence did not satisfy the mandate guard: %v", err)
	}
	wrongEvidenceErr := guardMandatedWorkflowLawBound(context.Background(), s.db, workID, definition, "terminal", "complete", "complete_workflow", json.RawMessage(`{"evidence_refs":["law:other"]}`))
	var wrongEvidenceFailure *Failure
	if wrongEvidenceErr == nil || !failureAs(wrongEvidenceErr, &wrongEvidenceFailure) || !strings.Contains(wrongEvidenceFailure.RecoveryAction, "present the law reference in the terminal action evidence") {
		t.Fatal("terminal evidence with the wrong law reference passed the mandate guard")
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	events, err := workflowSemanticActionEvents(context.Background(), tx, definition, WorkflowActionExecutionRequest{
		WorkID: workID, ActionID: "record_verdict", OperationID: "terminal-verdict", PrincipalRef: actorRef, RequestID: "terminal-request", Now: time.Unix(1, 0).UTC(),
	}, "terminal", actorRef, json.RawMessage(`{"predicate_id":"predicate:terminal","evaluation_evidence":["law:terminal"]}`), 1, false)
	if err != nil {
		t.Fatalf("terminal verdict did not bind its mandate evidence: %v", err)
	}
	if len(events) != 2 || events[0].Kind != WorkflowEvidenceBound {
		t.Fatalf("terminal verdict events=%v, want evidence binding and verdict", events)
	}
}

func TestMandatedLawGuardRefusesTerminalEvidenceWhileBindingStepIsReachable(t *testing.T) {
	const workID = "mandate-reachable-binding"
	s := openTemp(t)
	seedWork(t, s, workID)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	actorRef := DeriveWorkflowActorRef("principal/reachable", "client/reachable", "agent/reachable", "session/reachable")
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,?,?,?,?,?,?)`, actorRef, "principal/reachable", "client/reachable", "agent/reachable", "session/reachable", "agent", "now"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,'reachable mandate','internal_sqlite','[]','[]','now',?,'["law:reachable"]','[]',1,'prototype_internal')`, workID, actorRef); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	definition := WorkflowDefinition{StepGraph: WorkflowStepGraph{
		Steps: []WorkflowStep{{ID: "before", Actions: []string{"complete"}}, {ID: "binding", Actions: []string{"bind_evidence"}}, {ID: "terminal", Actions: []string{"complete"}}},
		Edges: []WorkflowEdge{{From: "before", To: "binding", Kind: WorkflowEdgeForward}, {From: "binding", To: "terminal", Kind: WorkflowEdgeForward}},
	}}
	err := guardMandatedWorkflowLawBound(context.Background(), s.db, workID, definition, "before", "complete", "complete_workflow", json.RawMessage(`{"evidence_refs":["law:reachable"]}`))
	var failure *Failure
	if err == nil || !failureAs(err, &failure) || !strings.Contains(failure.Detail, `spec mandate law "law:reachable" is not bound`) {
		t.Fatalf("terminal evidence passed while binding step was reachable: %v", err)
	}
}
