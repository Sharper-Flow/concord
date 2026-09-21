package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLivenessPathIdentityPreservesOrderAndPayload(t *testing.T) {
	a := livenessMove{action: "record_verdict", payload: json.RawMessage(`{"verdict_kind":"ok"}`)}
	b := livenessMove{action: "record_verdict", payload: json.RawMessage(`{"verdict_kind":"outcome_mismatch"}`)}
	if livenessPathKey([]livenessMove{a, b}) == livenessPathKey([]livenessMove{b, a}) {
		t.Fatal("order-sensitive verdict histories share a key")
	}
	if livenessPathKey([]livenessMove{a}) == livenessPathKey([]livenessMove{b}) {
		t.Fatal("different payloads share a key")
	}
	original := []livenessMove{a, b}
	copied := append([]livenessMove(nil), original...)
	if livenessPathKey(original) != livenessPathKey(copied) {
		t.Fatal("identical paths have different keys")
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

func TestLivenessConclusionDoesNotConflateIncompleteAndSuccessful(t *testing.T) {
	for _, result := range []livenessExploration{{}, {terminalStates: 1, depthBoundStates: 1}, {terminalStates: 1, omittedVariants: []string{"route"}}} {
		if result.conclusion() != "inconclusive" {
			t.Fatalf("false completion: %+v", result)
		}
	}
	if (livenessExploration{terminalStates: 1}).conclusion() != "complete-within-model" {
		t.Fatal("closed model lost its result")
	}
	if (livenessExploration{reports: []livenessReport{{}}}).conclusion() != "candidate-found" {
		t.Fatal("closed exploration lost its candidate finding")
	}
}

func TestLivenessTruncationNeverBecomesConclusive(t *testing.T) {
	for _, result := range []livenessExploration{
		{reports: []livenessReport{{}}, depthBoundStates: 1},
		{reports: []livenessReport{{}}, omittedVariants: []string{"unexamined-exit"}},
	} {
		if result.conclusion() != "inconclusive" {
			t.Fatalf("candidate report overrode incomplete exploration: %s", result.conclusion())
		}
		if len(result.reports) != 1 {
			t.Fatal("inconclusive result lost its candidate finding")
		}
	}
}

func TestLivenessDetectsSeededMissingExit(t *testing.T) {
	d := cloneWorkflowDefinition(BuiltinWorkflowDefinitions()[0])
	d.Ref = "workflow.seed_missing_exit"
	d.Version = 1
	for i := range d.StepGraph.Steps {
		if d.StepGraph.Steps[i].ID == d.StepGraph.StartStep {
			d.StepGraph.Steps[i].Actions = []string{"checkpoint_context"}
		}
	}
	result := livenessExplore(t, d)
	if len(result.reports) == 0 {
		t.Fatalf("missing exit not detected: %+v", result)
	}
	if result.testedTransitions != 0 {
		t.Fatalf("refused probes counted as admitted transitions: %d", result.testedTransitions)
	}
	if result.testedProbes == 0 {
		t.Fatal("missing-exit fixture exercised no probes")
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
		if err := livenessApply(context.Background(), fixture.store, workID, move, 100, true); err != nil {
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

func TestLivenessDoesNotTreatHoldLoopAsCompletion(t *testing.T) {
	d := cloneWorkflowDefinition(BuiltinWorkflowDefinitions()[0])
	d.Ref = "workflow.seed_hold_loop"
	d.Version = 1
	for i := range d.ActionDefinitions {
		if d.ActionDefinitions[i].ID == "record_proposal" {
			d.ActionDefinitions[i].ExecutionMode = ActionHold
		}
	}
	result := livenessExplore(t, d)
	if result.conclusion() != "inconclusive" || result.terminalStates != 0 {
		t.Fatalf("loop became completion: %+v", result)
	}
}

func TestLivenessExecutesAndRejectsUnapprovedCompletion(t *testing.T) {
	d := cloneWorkflowDefinition(BuiltinWorkflowDefinitions()[0])
	d.Ref = "workflow.seed_rejected_completion"
	d.Version = 1
	d.StepGraph = graph([]WorkflowStep{step("discovery", WorkflowStepInternalSQLite, "record_discovery"), step("release", WorkflowStepInternalSQLite, "complete")}, forward("discovery", "release"), "release")
	result := livenessExplore(t, d)
	if result.terminalStates != 0 || len(result.reports) == 0 {
		t.Fatalf("terminal step hid rejected completion: %+v", result)
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
			s, workID := (livenessReplayCache{}).replay(t, definition, nil)
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
				if err := livenessApply(ctx, s, workID, livenessMove{action: actionID, variant: livenessVariantLabel(variant), payload: payload}, ordinal, true); err != nil {
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
				apply(actionID, map[string]string{})
			}
			state, events, err := livenessCompletion(ctx, s, workID)
			if err != nil || state != "completed" || events != 1 {
				t.Fatalf("completion state=%s events=%d err=%v", state, events, err)
			}
		})
	}
}

func TestLivenessReplayCacheIsolatesBranches(t *testing.T) {
	definition := BuiltinWorkflowDefinitions()[0]
	cache := livenessReplayCache{}
	root, workID := cache.replay(t, definition, nil)
	cache.retain(t, root, nil)
	first, _ := cache.replay(t, definition, nil)
	defer first.Close()
	second, _ := cache.replay(t, definition, nil)
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
	if err := livenessApply(ctx, first, workID, proposal, 0, true); err != nil {
		t.Fatal(err)
	}
	after, err := livenessWorkVersion(ctx, second, workID)
	if err != nil || before != after {
		t.Fatalf("branch changed sibling: %d -> %d, %v", before, after, err)
	}
	cache.retain(t, first, []livenessMove{proposal})
	restored, _ := cache.replay(t, definition, []livenessMove{proposal})
	defer restored.Close()
	step, err = livenessStep(ctx, restored, workID)
	if err != nil || step != "alignment" {
		t.Fatalf("prefix did not retain committed state: %s, %v", step, err)
	}
}
