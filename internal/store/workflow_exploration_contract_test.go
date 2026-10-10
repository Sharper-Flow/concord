package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLivenessReplayPreservesOrderAndPayload(t *testing.T) {
	entry, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	definition := entry.Definition
	move := func(actionID string) livenessMove {
		t.Helper()
		action, ok := livenessActionDefinition(definition, actionID)
		if !ok {
			t.Fatalf("unknown action %s", actionID)
		}
		payload, err := livenessPayload(definition, action, map[string]string{})
		if err != nil {
			t.Fatal(err)
		}
		return livenessMove{action: actionID, payload: payload}
	}
	for _, problem := range []string{"first problem", "another problem", "first problem"} {
		proposal := move("record_proposal")
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(proposal.payload, &fields); err != nil {
			t.Fatal(err)
		}
		fields["problem"], _ = json.Marshal(problem)
		proposal.payload, err = json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		path := []livenessMove{proposal, move("record_alignment")}
		s, workID := livenessReplay(t, definition, append([]livenessMove(nil), path...))
		var gotProblem string
		if err := s.db.QueryRow(`SELECT problem FROM workflow_proposal_records WHERE work_id=?`, workID).Scan(&gotProblem); err != nil {
			t.Fatal(err)
		}
		if gotProblem != problem {
			t.Fatalf("replayed problem=%q, want %q", gotProblem, problem)
		}
		rows, err := s.db.Query(`SELECT json_extract(payload,'$.action_id') FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq`, workID, WorkflowActionCompleted)
		if err != nil {
			t.Fatal(err)
		}
		var actions []string
		for rows.Next() {
			var action string
			if err := rows.Scan(&action); err != nil {
				t.Fatal(err)
			}
			actions = append(actions, action)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(actions, " "); got != "record_proposal record_alignment" {
			t.Fatalf("replayed action order=%q", got)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLivenessResolvesEngineOwnedRequestCorrection(t *testing.T) {
	for _, definition := range BuiltinWorkflowDefinitions() {
		if _, ok := livenessActionDefinition(definition, "request_correction"); !ok {
			t.Errorf("%s omits the engine-owned correction payload", definition.Ref)
		}
		for _, step := range definition.StepGraph.Steps {
			if !containsString(livenessDeclaredActions(definition, step.ID), "request_correction") {
				t.Errorf("%s/%s never probes engine-owned correction", definition.Ref, step.ID)
			}
		}
	}
}

func TestLivenessDisclosesOutcomeKindAndTokenOmissions(t *testing.T) {
	for _, definition := range BuiltinWorkflowDefinitions() {
		omissions := livenessOmittedVariants(definition)
		for _, kind := range definition.OutcomeSchema.AllowedKinds {
			if kind != definition.OutcomeSchema.DefaultKind && !containsString(omissions, "outcome_kind="+string(kind)) {
				t.Errorf("%s silently omits outcome kind %s", definition.Ref, kind)
			}
		}
		for i, token := range definition.OutcomeSchema.AllowedOutcomeTokens {
			if i > 0 && !containsString(omissions, "outcome_token="+token) {
				t.Errorf("%s silently omits outcome token %s", definition.Ref, token)
			}
		}
	}
}

func TestLivenessOmitsUnrequestedOptionalDesignCorrection(t *testing.T) {
	definition, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	action := workflowContractRecoveryActionDefinition()
	raw, err := livenessPayload(definition.Definition, action, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["design_record"]; exists {
		t.Error("unrequested optional design_record was synthesized")
	}
	if !containsString(livenessOmittedVariants(definition.Definition), "optional-presence:supersede_contract.design_record") {
		t.Error("engine-owned optional design presence was not reported as unexamined")
	}
}

func TestLivenessReportsOptionalPresenceSampling(t *testing.T) {
	for _, definition := range BuiltinWorkflowDefinitions() {
		omitted := livenessOmittedVariants(definition)
		for _, action := range definition.ActionDefinitions {
			for _, field := range action.Payload.Fields {
				if !field.Required && !containsString(omitted, "optional-presence:"+action.ID+"."+field.Name) {
					t.Errorf("%s omits presence coverage for %s.%s", definition.Ref, action.ID, field.Name)
				}
			}
		}
	}
}

func TestLivenessExecutesGeneratedCorrection(t *testing.T) {
	const workID = "liveness-generated-correction"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.implementation", "execution")
	ownerRef, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	reviewer := acceptReturnRouteWorker(t, fixture, workID, ownerRef)
	if err := runVerdictActionAs(t, fixture.store, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:return-route","verdict_kind":"outcome_mismatch","evaluation_evidence":["evidence:return-route-verification"],"incomparable_with_approved":true}`), 0, reviewer); err != nil {
		t.Fatal(err)
	}
	definition, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	step := currentStep(t, fixture.store, workID)
	for _, move := range livenessStateMoves(t, definition.Definition, step) {
		if move.action != "request_correction" {
			continue
		}
		if err := livenessApply(context.Background(), fixture.store, workID, move, 100); err != nil {
			t.Fatalf("generated correction cannot execute: %v", err)
		}
		if got := currentStep(t, fixture.store, workID); got != "execution" {
			t.Fatalf("correction reached %s, want execution", got)
		}
		return
	}
	t.Fatal("no generated request_correction move")
}

func TestLivenessBindsCurrentAndSuccessorContractVersions(t *testing.T) {
	const workID = "liveness-contract-versions"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.implementation", "execution")
	for _, scenario := range []struct {
		action string
		want   int64
	}{{"supersede_contract", 2}, {"record_verdict", 1}} {
		raw, err := livenessBindRecordedState(context.Background(), fixture.store, workID, scenario.action, json.RawMessage(`{"contract_version":99}`))
		if err != nil {
			t.Fatal(err)
		}
		var fields struct {
			Version int64 `json:"contract_version"`
		}
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		if fields.Version != scenario.want {
			t.Errorf("%s version=%d, want %d", scenario.action, fields.Version, scenario.want)
		}
	}
}

// These finite witnesses must reach durable completion. Exploratory cutoffs
// cannot substitute for the success path each builtin promises to support.
func TestBuiltinWorkflowCompletionWitnesses(t *testing.T) {
	paths := map[string]string{
		"workflow.implementation":     "record_proposal record_alignment record_discovery record_design approve_contract start_execution bind record_delivery start_refine bind record_delivery record_delivery record_verdict confirm_premise complete",
		"workflow.break_fix":          "record_reproduction record_alignment record_root_cause approve_contract start_repair bind record_delivery start_refine bind record_delivery record_delivery record_verdict confirm_premise complete",
		"workflow.research":           "frame_research approve_contract bind record_finding record_report record_conclusion record_verdict confirm_premise complete",
		"workflow.architecture_spike": "frame_question approve_contract bind record_research record_option discard_poc record_decision record_verdict accept_decision confirm_premise complete",
		"workflow.ops_runbook":        "approve_contract approve_operation start_run bind record_delivery record_verdict record_health rollback_run record_delivery cleanup_run confirm_premise complete",
		"workflow.static_analysis":    "declare_scope approve_contract run_analysis record_delivery bind record_report record_verdict confirm_premise complete",
		"workflow.generic_one_off":    "approve_contract start_action bind record_delivery record_verdict confirm_premise complete",
	}
	for _, definition := range BuiltinWorkflowDefinitions() {
		t.Run(definition.Ref, func(t *testing.T) {
			sequence, ok := paths[definition.Ref]
			if !ok {
				t.Fatal("builtin has no completion witness")
			}
			s, workID := livenessReplay(t, definition, nil)
			defer s.Close()
			ctx := context.Background()
			ordinal := 0
			apply := func(actionID string, variant map[string]string) {
				t.Helper()
				action, ok := livenessActionDefinition(definition, actionID)
				if !ok {
					t.Fatalf("unknown action %s", actionID)
				}
				payload, err := livenessPayload(definition, action, variant)
				if err != nil {
					t.Fatal(err)
				}
				if err := livenessApply(ctx, s, workID, livenessMove{action: actionID, variant: livenessVariantLabel(variant), payload: payload}, ordinal); err != nil {
					t.Fatalf("witness action %d %s: %v", ordinal, actionID, err)
				}
				ordinal++
			}
			for _, actionID := range strings.Fields(sequence) {
				if actionID == "bind" {
					for _, kind := range definition.RequiredEvidenceKinds {
						if kind == EvidenceNativeRun {
							var observationID string
							if err := s.db.QueryRowContext(ctx, `SELECT observation_id FROM workflow_native_runs WHERE work_id=? LIMIT 1`, workID).Scan(&observationID); err != nil {
								t.Fatal(err)
							}
							if err := s.Transact(ctx, func(tx *Transaction) error {
								return AppendExternalObservationVerificationTx(ctx, tx, workID, "principal/operator", time.Unix(100, 0), ExternalObservationVerification{ObservationID: observationID, VerificationMethod: VerifyTrustedClientReport, VerifiedAt: time.Unix(100, 0).UTC().Format(time.RFC3339Nano), VerifyingAuthorityRef: "client/liveness-verifier", Result: VerificationMatched})
							}); err != nil {
								t.Fatal(err)
							}
						}
						apply("bind_evidence", map[string]string{"evidence_kind": string(kind)})
					}
					continue
				}
				// The current implementation and break-fix definitions gate the
				// refine exit on a green verify run bound in the epoch
				// (CD-0192); the witness supplies one before the refine
				// delivery exits.
				if actionID == "record_delivery" {
					var witnessStep string
					if err := s.db.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&witnessStep); err != nil {
						t.Fatal(err)
					}
					if workflowRefineProofGateActive(definition, witnessStep) {
						refineProofSeedGreenRun(t, s, workID, strings.Repeat("f", 64))
					}
				}
				apply(actionID, map[string]string{})
			}
			state, events, err := livenessCompletion(ctx, s, workID)
			if err != nil || state != "completed" || events != 1 {
				t.Fatalf("completion state=%s events=%d err=%v", state, events, err)
			}
		})
	}
}

func TestLivenessReplayIsolatesBranches(t *testing.T) {
	definition := BuiltinWorkflowDefinitions()[0]
	first, workID := livenessReplay(t, definition, nil)
	defer first.Close()
	second, _ := livenessReplay(t, definition, nil)
	defer second.Close()
	ctx := context.Background()
	before, err := livenessWorkVersion(ctx, second, workID)
	if err != nil {
		t.Fatal(err)
	}
	step, err := livenessStep(ctx, first, workID)
	if err != nil {
		t.Fatal(err)
	}
	var proposal livenessMove
	for _, move := range livenessStateMoves(t, definition, step) {
		if move.action == "record_proposal" {
			proposal = move
			break
		}
	}
	if proposal.action == "" {
		t.Fatal("fixture has no proposal action")
	}
	if err := livenessApply(ctx, first, workID, proposal, 0); err != nil {
		t.Fatal(err)
	}
	after, err := livenessWorkVersion(ctx, second, workID)
	if err != nil || before != after {
		t.Fatalf("branch changed sibling: %d -> %d, %v", before, after, err)
	}
	restored, _ := livenessReplay(t, definition, []livenessMove{proposal})
	defer restored.Close()
	step, err = livenessStep(ctx, restored, workID)
	if err != nil || step != "alignment" {
		t.Fatalf("replay did not restore committed state: %s, %v", step, err)
	}
}
