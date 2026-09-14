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
	if livenessPathKey([]livenessMove{a, b}) != livenessPathKey([]livenessMove{a, b}) {
		t.Fatal("identical paths have different keys")
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
	if (livenessExploration{reports: []livenessReport{{}}, depthBoundStates: 1}).conclusion() != "counterexample" {
		t.Fatal("cutoff concealed a finding")
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
	if result.conclusion() != "counterexample" {
		t.Fatalf("missing exit not detected: %+v", result)
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
	if result.terminalStates != 0 || result.conclusion() != "counterexample" {
		t.Fatalf("terminal step hid rejected completion: %+v", result)
	}
}

// These finite witnesses must reach durable completion. Exploratory cutoffs
// cannot substitute for the success path each builtin promises to support.
func TestBuiltinWorkflowCompletionWitnesses(t *testing.T) {
	paths := map[string]string{
		"workflow.implementation":     "record_proposal record_discovery record_design approve_contract start_execution bind record_delivery start_refine bind record_delivery record_verdict confirm_premise complete",
		"workflow.break_fix":          "record_reproduction record_root_cause approve_contract start_repair bind record_delivery start_refine bind record_delivery record_verdict confirm_premise complete",
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
	if err != nil || step != "discovery" {
		t.Fatalf("prefix did not retain committed state: %s, %v", step, err)
	}
}
