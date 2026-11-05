package store

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestWorkflowClarifyPayloadBounds(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		valid         bool
	}{
		{"empty", `{"resolved_questions":[],"note":"No operator questions remain."}`, true},
		{"quoted answer", `{"resolved_questions":["What? Operator: \"A result.\""],"note":"Resolved."}`, true},
		{"multibyte", fmt.Sprintf(`{"resolved_questions":[%q],"note":"Resolved."}`, strings.Repeat("界", 2048)), true},
		{"too long", fmt.Sprintf(`{"resolved_questions":[%q],"note":"Resolved."}`, strings.Repeat("x", 2049)), false},
		{"blank", `{"resolved_questions":["  "],"note":"Resolved."}`, false},
		{"one code point", `{"resolved_questions":["x"],"note":"Resolved."}`, true},
		{"missing", `{"note":"Resolved."}`, false},
		{"duplicate", `{"resolved_questions":["same","same"],"note":"Resolved."}`, false},
		{"unknown", `{"resolved_questions":[],"note":"Resolved.","other":true}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateWorkflowPlanActionPayload("record_clarify", json.RawMessage(tc.payload))
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
	if _, err := validateWorkflowPlanActionPayload("record_intake", json.RawMessage(`{"outcome":"proceed","open_questions_found":false}`)); err == nil {
		t.Fatal("retired intake field accepted")
	}
}

func TestWorkflowClarifyRequiredBeforePlanning(t *testing.T) {
	for _, tc := range []struct {
		typ, planning, document string
	}{
		{"change", "record_plan", testWorkflowPlanDocument},
		{"fix", "record_plan", testWorkflowPlanDocument},
		{"research", "frame_research", `{}`},
		{"removal", "record_removal_scope", `{"removed":["file:one"],"retiring_clauses":[],"note":"retire this"}`},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			for _, answers := range []string{`[]`, `["Question: what outcome? Answer: the requested outcome."]`} {
				t.Run(answers, func(t *testing.T) {
					s := openTemp(t)
					defer s.Close()
					seedStepWork(t, s, "work-clarify")
					ctx := t.Context()
					if err := s.Transact(ctx, func(tx *Transaction) error {
						scope := newWriteScope(tx.tx)
						if err := scope.enter(ctx); err != nil {
							return err
						}
						if _, err := tx.tx.ExecContext(ctx, `INSERT INTO workflow_instances(work_id,instance_state) VALUES('work-clarify','intake')`); err != nil {
							return err
						}
						return scope.close(ctx)
					}); err != nil {
						t.Fatal(err)
					}
					var version int64
					if err := s.db.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id='work-clarify'`).Scan(&version); err != nil {
						t.Fatal(err)
					}
					seq := 0
					apply := func(action, payload string) error {
						t.Helper()
						seq++
						r := WorkflowActionExecutionRequest{WorkID: "work-clarify", ExpectedVersion: version, ActionID: action, Payload: json.RawMessage(payload), Actor: stepFixtureActor(), OperationID: fmt.Sprintf("clarify-op-%d", seq), IdempotencyIdentity: fmt.Sprintf("clarify-key-%d", seq), PrincipalRef: "principal-1", RequestID: fmt.Sprintf("clarify-request-%d", seq), AcceptedInputsDigest: testDigest("inputs"), ContractDigest: testManifestDigest, Now: time.Unix(100, 0).UTC()}
						var result WorkflowActionExecutionResult
						err := s.Transact(ctx, func(tx *Transaction) error {
							var err error
							result, err = ApplyWorkflowActionTx(ctx, tx, r)
							return err
						})
						if err == nil {
							version = result.ResultingVersion
						}
						return err
					}
					must := func(action, payload string) {
						t.Helper()
						if err := apply(action, payload); err != nil {
							t.Fatalf("%s: %v", action, err)
						}
					}
					must("record_intake", `{"outcome":"proceed"}`)
					must("pick_type", fmt.Sprintf(`{"type":%q,"reason":"initial choice"}`, tc.typ))
					if tc.typ == "fix" {
						must("record_diagnosis", `{"reproduction":"The defect reproduces.","searched":"Searched related work.","outcome":"none_found","root_cause":"The cause is confirmed."}`)
						must("complete_step", `{"step_id":"diagnose","note":"Diagnosis complete."}`)
					}
					if err := apply(tc.planning, tc.document); err == nil {
						t.Fatal("planning admitted before clarification")
					}
					if err := apply("complete_step", `{"step_id":"clarify","note":"No record."}`); err == nil {
						t.Fatal("clarify completed without a record")
					}
					must("record_clarify", fmt.Sprintf(`{"resolved_questions":%s,"note":"No operator questions remain."}`, answers))
					if err := apply(tc.planning, tc.document); err == nil {
						t.Fatal("planning admitted before clarify completion")
					}
					must("complete_step", `{"step_id":"clarify","note":"Clarification complete."}`)
					must(tc.planning, tc.document)
				})
			}
		})
	}
}

func TestWorkflowLegacyTypeSwitchDoesNotAddClarify(t *testing.T) {
	ctx := t.Context()
	f := qCurrentAcceptanceWithDelivery(t, "work-legacy-switch", "artifact:legacy-switch")
	s := f.store
	plan, err := WorkflowStepPlanTemplate(WorkflowStepPlanChange)
	if err != nil {
		t.Fatal(err)
	}
	plan.Steps = plan.Steps[1:]
	plan.Steps[0].After = []string{}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		scope := newWriteScope(tx.tx)
		if err := scope.enter(ctx); err != nil {
			return err
		}
		_, err := tx.tx.ExecContext(ctx, `UPDATE workflow_instances SET workflow_type='change',step_plan=? WHERE work_id='work-legacy-switch'`, string(raw))
		if err != nil {
			return err
		}
		return scope.close(ctx)
	}); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := s.db.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id='work-legacy-switch'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	current, next := version, version+1
	pick := workflowTypePickedPayload{WorkflowVersionFields: WorkflowVersionFields{WorkID: "work-legacy-switch", ExpectedVersion: &current, ResultingVersion: &next}, Type: WorkflowStepPlanFix, Reason: "switch type"}
	pickRaw, err := json.Marshal(pick)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		scope := newWriteScope(tx.tx)
		if err := scope.enter(ctx); err != nil {
			return err
		}
		if err := foldWorkflowTypePicked(ctx, tx.tx, Event{SubjectType: SubjectWorkItem, SubjectID: "work-legacy-switch", Payload: pickRaw, OccurredAt: time.Unix(100, 0).UTC()}); err != nil {
			return err
		}
		return scope.close(ctx)
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := readWorkflowPlanTx(ctx, s.db, "work-legacy-switch")
	if err != nil || updated == nil {
		t.Fatalf("read switched plan: %v", err)
	}
	if stepIDExists(updated.Steps, "clarify") || !stepIDExists(updated.Steps, "diagnose") || !workflowPlanAllowsAction(updated, "plan", "record_plan") {
		t.Fatalf("unexpected legacy switch: %+v", updated)
	}
	if err := ValidateStepPlanWrite(&plan, *updated); err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"impact_verdict":"non-breaking"}`)
	if err := f.run(ctx, "complete", "legacy-incomplete", f.version(ctx, t), payload, nil); err == nil {
		t.Fatal("completion admitted before the switched plan was complete")
	}
	completeWorkflowPlanFixture(t, s, "work-legacy-switch")
	if err := f.run(ctx, "complete", "legacy-complete", f.version(ctx, t), payload, nil); err != nil {
		t.Fatalf("complete grandfathered plan: %v", err)
	}
	var lifecycle string
	if err := s.db.QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id='work-legacy-switch'`).Scan(&lifecycle); err != nil || lifecycle != "completed" {
		t.Fatalf("stored lifecycle = %q, err=%v", lifecycle, err)
	}
}

func TestWorkflowLegacyTypeSwitchEveryPairCompletes(t *testing.T) {
	types := []WorkflowStepPlanType{WorkflowStepPlanChange, WorkflowStepPlanFix, WorkflowStepPlanResearch, WorkflowStepPlanRemoval}
	for _, from := range types {
		for _, to := range types {
			if from == to {
				continue
			}
			t.Run(string(from)+"_to_"+string(to), func(t *testing.T) {
				ctx := t.Context()
				const work = "legacy-pair"
				f := qCurrentAcceptanceWithDelivery(t, work, "artifact:legacy-pair")
				s := f.store
				old, err := WorkflowStepPlanTemplate(from)
				if err != nil {
					t.Fatal(err)
				}
				clarify, _ := stepByID(old.Steps, "clarify")
				steps := make([]WorkflowPlanStep, 0, len(old.Steps)-1)
				for _, step := range old.Steps {
					if step.ID == "clarify" {
						continue
					}
					var after []string
					for _, edge := range step.After {
						if edge == "clarify" {
							after = append(after, clarify.After...)
						} else {
							after = append(after, edge)
						}
					}
					step.After = after
					steps = append(steps, step)
				}
				old.Steps = steps
				raw, err := json.Marshal(old)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.Transact(ctx, func(tx *Transaction) error {
					scope := newWriteScope(tx.tx)
					if err := scope.enter(ctx); err != nil {
						return err
					}
					if _, err := tx.tx.ExecContext(ctx, `UPDATE workflow_instances SET workflow_type=?,step_plan=? WHERE work_id=?`, from, string(raw), work); err != nil {
						return err
					}
					return scope.close(ctx)
				}); err != nil {
					t.Fatal(err)
				}
				version := readWorkVersion(t, s, work)
				next := version + 1
				pick := workflowTypePickedPayload{WorkflowVersionFields: WorkflowVersionFields{WorkID: work, ExpectedVersion: &version, ResultingVersion: &next}, Type: to, Reason: "switch type"}
				pickRaw, err := json.Marshal(pick)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.Transact(ctx, func(tx *Transaction) error {
					scope := newWriteScope(tx.tx)
					if err := scope.enter(ctx); err != nil {
						return err
					}
					if err := foldWorkflowTypePicked(ctx, tx.tx, Event{SubjectType: SubjectWorkItem, SubjectID: work, Payload: pickRaw, OccurredAt: time.Unix(100, 0).UTC()}); err != nil {
						return err
					}
					return scope.close(ctx)
				}); err != nil {
					t.Fatalf("switch: %v", err)
				}
				updated, err := readWorkflowPlanTx(ctx, s.db, work)
				if err != nil || updated == nil {
					t.Fatalf("read switched plan: %v", err)
				}
				if stepIDExists(updated.Steps, "clarify") {
					t.Fatal("switch added clarify")
				}
				template, err := WorkflowStepPlanTemplate(to)
				if err != nil {
					t.Fatal(err)
				}
				for _, step := range template.Steps {
					if step.ID != "clarify" && !stepIDExists(updated.Steps, step.ID) {
						t.Errorf("missing template step %s", step.ID)
					}
					if !stepIDExists(old.Steps, step.ID) && step.ID != "clarify" && slices.Contains(step.After, "clarify") {
						got, _ := stepByID(updated.Steps, step.ID)
						predecessor, _ := stepByID(template.Steps, "clarify")
						if !slices.Equal(got.After, predecessor.After) {
							t.Errorf("%s after=%v, want %v", step.ID, got.After, predecessor.After)
						}
					}
				}
				if err := ValidateStepPlanWrite(&old, *updated); err != nil {
					t.Fatal(err)
				}
				payload := json.RawMessage(`{"impact_verdict":"non-breaking"}`)
				if err := f.run(ctx, "complete", "legacy-incomplete", f.version(ctx, t), payload, nil); err == nil {
					t.Fatal("incomplete plan admitted")
				}
				completeWorkflowPlanFixture(t, s, work)
				if err := f.run(ctx, "complete", "legacy-complete", f.version(ctx, t), payload, nil); err != nil {
					t.Fatalf("complete: %v", err)
				}
				var lifecycle string
				if err := s.db.QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id=?`, work).Scan(&lifecycle); err != nil || lifecycle != "completed" {
					t.Fatalf("lifecycle=%q err=%v", lifecycle, err)
				}
			})
		}
	}
}

func TestRemovalClarifyThenApproveContract(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	const work = "removal-clarify-approval"
	seedWork(t, s, work)
	seedWorkflowLaw(t, s)
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/clarify-removal", ActorClass: ActorAgent}
	if err := s.Transact(t.Context(), func(tx *Transaction) error {
		return InitializeWorkflowTx(t.Context(), tx, WorkflowInitializationRequest{WorkID: work, Actor: owner, Now: time.Unix(1, 0).UTC()})
	}); err != nil {
		t.Fatal(err)
	}
	seedWorkflowPlanFrontier(t, s, work, WorkflowStepPlanRemoval, "clarify")
	act := func(action string, fields map[string]any) {
		t.Helper()
		if err := runVerdictActionAs(t, s, work, action, mustJSONValue(fields), 0, owner); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	act("record_clarify", map[string]any{"resolved_questions": []string{}, "note": "No operator questions remain."})
	act("complete_step", map[string]any{"step_id": "clarify", "note": "Clarification complete."})
	act("record_removal_scope", map[string]any{"removed": []string{"file:one"}, "retiring_clauses": []string{}, "note": "Retire this."})
	act("approve_contract", map[string]any{
		"premise": "Retire the obsolete fixture.", "decision_domains": []string{}, "spec_mandate": []string{}, "law_modifies": []string{}, "legislative_subjects": []string{},
		"architecture_binding": WorkflowArchitectureBinding{HomeDomainID: "root", AffectedDomainIDs: []string{"root"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{}},
	})
}
