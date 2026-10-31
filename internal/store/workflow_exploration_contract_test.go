package store

import (
	"context"
	"encoding/json"
	"slices"
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
		rows, err := s.db.Query(`SELECT x.action_id FROM workflow_phase_exits x JOIN effect_receipts r ON r.effect_seq=x.exit_seq WHERE x.work_id=? AND r.kind=? ORDER BY x.exit_seq`, workID, WorkflowActionCompleted)
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
			if !slices.Contains(livenessDeclaredActions(definition, step.ID), "request_correction") {
				t.Errorf("%s/%s never probes engine-owned correction", definition.Ref, step.ID)
			}
		}
	}
}

func TestLivenessDisclosesOutcomeKindAndTokenOmissions(t *testing.T) {
	for _, definition := range BuiltinWorkflowDefinitions() {
		omissions := livenessOmittedVariants(definition)
		for _, kind := range definition.OutcomeSchema.AllowedKinds {
			if kind != definition.OutcomeSchema.DefaultKind && !slices.Contains(omissions, "outcome_kind="+string(kind)) {
				t.Errorf("%s silently omits outcome kind %s", definition.Ref, kind)
			}
		}
		for i, token := range definition.OutcomeSchema.AllowedOutcomeTokens {
			if i > 0 && !slices.Contains(omissions, "outcome_token="+token) {
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
	if !slices.Contains(livenessOmittedVariants(definition.Definition), "optional-presence:supersede_contract.design_record") {
		t.Error("engine-owned optional design presence was not reported as unexamined")
	}
}

func TestLivenessReportsOptionalPresenceSampling(t *testing.T) {
	for _, definition := range BuiltinWorkflowDefinitions() {
		omitted := livenessOmittedVariants(definition)
		for _, action := range definition.ActionDefinitions {
			for _, field := range action.Payload.Fields {
				if !field.Required && !slices.Contains(omitted, "optional-presence:"+action.ID+"."+field.Name) {
					t.Errorf("%s omits presence coverage for %s.%s", definition.Ref, action.ID, field.Name)
				}
			}
		}
	}
}

func TestLivenessExecutesGeneratedCorrection(t *testing.T) {
	const workID = "liveness-generated-correction"
	j := pReturnReviewFixture(t, workID, "workflow.implementation", "correction_required")
	reportRef := issue1013Pin(t, j.s, workID).Review.Accepted.ReportRef
	seedWorkflowAuthority(t, j.s, "liveness-review-authority", workID, j.fixture.owner.PrincipalRef, "request/liveness-review", []string{reportRef})
	if err := j.action(t, "bind_evidence", map[string]any{"evidence_kind": "review", "evidence_ref": reportRef, "producer_id": j.fixture.owner.PrincipalRef, "producer_run_ref": "liveness-review-authority", "producer_watermark": "request/liveness-review"}, j.fixture.owner); err != nil {
		t.Fatal(err)
	}
	definition, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	step := currentStep(t, j.s, workID)
	for _, move := range livenessStateMoves(t, definition.Definition, step) {
		if move.action != "request_correction" {
			continue
		}
		if err := livenessApply(context.Background(), j.s, workID, move, 100); err != nil {
			t.Fatalf("generated correction cannot execute: %v", err)
		}
		if got := currentStep(t, j.s, workID); got != "execution" {
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
		"workflow.implementation":     "record_proposal record_alignment approve_contract record_design start_execution bind record_delivery checkpoint_refine bind record_delivery record_delivery record_verdict confirm_premise complete",
		"workflow.break_fix":          "record_diagnosis approve_contract checkpoint_repair bind record_delivery checkpoint_refine bind record_delivery record_delivery record_verdict confirm_premise complete",
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
			s := openTemp(t)
			workID := livenessWorkID
			defer s.Close()
			seedWork(t, s, workID)
			seedWork(t, s, livenessPeerWorkID)
			seedWorkflowLaw(t, s)
			seedIssue31DomainRegistry(t, s)
			livenessInitialize(t, s, workID, definition)
			insertInvestigationGateObservation(t, s, workID, "obs:"+strings.Repeat("c", 16), []string{"root", livenessPeerWorkID})
			seedWorkflowAuthority(t, s, "liveness-authority", workID, livenessActor().PrincipalRef, "request/liveness", []string{livenessSubjectRef})
			ctx := context.Background()
			if workflowFamilyArchived(definition.Ref) {
				// A retired family refuses public execution: the recorded
				// journey's first action is the refusal witness, and the
				// recorded history replays through the frozen journal
				// owners.
				first := strings.Fields(sequence)[0]
				action, ok := livenessActionDefinition(definition, first)
				if !ok {
					t.Fatalf("unknown action %s", first)
				}
				payload, err := livenessPayload(definition, action, map[string]string{})
				if err != nil {
					t.Fatal(err)
				}
				if err := livenessApply(ctx, s, workID, livenessMove{action: first, payload: payload}, 0); err == nil {
					t.Fatal("the retired family admitted public execution")
				}
				return
			}
			ordinal := 0
			var journey *candidateJourney
			proofRef := ""
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
				if actionID == "approve_contract" && definition.ChangesProductTruth != nil && *definition.ChangesProductTruth {
					var fields map[string]any
					if err := json.Unmarshal(payload, &fields); err != nil {
						t.Fatal(err)
					}
					fields["architecture_binding"] = WorkflowArchitectureBinding{HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}}
					payload = mustJSONValue(fields)
				}
				switch {
				case actionID == "record_verdict":
					err = runVerdictActionAs(t, s, workID, actionID, criterionAssociationPayload(proofRef, "predicate:liveness-exit"), 0, livenessActor())
				case journey != nil && (actionID == "record_review" || actionID == "confirm_premise" || actionID == "complete"):
					err = journey.terminalAction(t, actionID, payload, livenessActor())
				default:
					err = livenessApply(ctx, s, workID, livenessMove{action: actionID, variant: livenessVariantLabel(variant), payload: payload}, ordinal)
				}
				if err != nil {
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
				if actionID == "record_delivery" {
					if journey == nil {
						journey = pReturnClaim(t, workflowReturnRouteFixture{store: s, owner: livenessActor()}, workID)
						journey.acceptor = livenessActor()
						run := journey.observeRun(t, "delivery-proof")
						journey.bindVerification(t, run)
						qAcceptCriterionReview(t, journey.fixture, run.OperationRef)
						proofRef = run.OperationRef
					}
				}
				if actionID == "record_verdict" && definition.Ref == "workflow.research" {
					fixture := workContextFixture{store: s, workID: workID, owner: livenessActor()}
					pack := seedResearchContextPack(t, fixture, "liveness-research")
					bindResearchPack(t, fixture, pack.PackID, 1, true)
					selection := researchSelectionEntry(pack.PackID, 1, "f2", "root")
					selection["product_wide_rationale"] = "The selected finding supports the Product-wide research criterion."
					if err := fixture.recordWorkContext(t, map[string]any{"research_finding_refs": []map[string]any{selection}}); err != nil {
						t.Fatal(err)
					}
					view, err := readWorkContextView(ctx, s.db, workID)
					if err != nil || view == nil || len(view.ResearchFindings) != 1 {
						t.Fatalf("selected research proof: %+v err=%v", view, err)
					}
					proofRef = view.ResearchFindings[0].ViewID
				}
				if actionID == "complete" {
					bindCompletionClosure(t, s, workID, livenessActor())
				}
				apply(actionID, map[string]string{})
			}
			state, events, err := livenessCompletion(ctx, s, workID)
			if err != nil || state != "completed" || events != 1 {
				t.Fatalf("completion state=%s events=%d err=%v", state, events, err)
			}
			{
				var operatorEvents int
				if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM effect_receipts e JOIN workflow_actors a ON a.actor_ref=e.actor WHERE e.subject_id=? AND a.actor_class='operator'`, workID).Scan(&operatorEvents); err != nil {
					t.Fatal(err)
				}
				if operatorEvents != 0 {
					t.Fatalf("completion used %d operator-authored events", operatorEvents)
				}
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
