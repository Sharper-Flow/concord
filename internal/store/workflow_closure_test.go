package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const closureTestHash = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func cancelWithReviewedEvidence(t *testing.T, s *Store, workID, actor string, refs []string) {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	var from string
	if err := s.db.QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, workID).Scan(&from); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"from": from, "to": "cancelled", "reason": "reviewed cancellation", "evidence_refs": refs, "expected_version": version, "resulting_version": version + 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(t.Context(), s, Operation{Events: []Event{{EventID: "cancel-reviewed-" + workID, Kind: "work.transitioned", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: actor, OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: workVersion(workID, version)}); err != nil {
		t.Fatal(err)
	}
}

func TestReviewedTerminalCancellationPublication(t *testing.T) {
	for _, tc := range []struct {
		name string
		refs []string
		want FailureKind
	}{
		{"without_hash", []string{"evidence:ordinary"}, ""},
		{"empty_evidence", nil, ""},
		{"malformed_hash", []string{"sha256:" + strings.Repeat("g", 64)}, ""},
		{"embedded_hash", []string{"review:" + closureTestHash}, ""},
		{"reviewed_hash", []string{"evidence:ordinary", closureTestHash}, KindOutcomeMismatch},
		{"duplicate_hash", []string{closureTestHash, closureTestHash}, KindOutcomeMismatch},
		{"multiple_hashes", []string{closureTestHash, "sha256:" + strings.Repeat("b", 64)}, KindInvariantViolation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := closureBindingFixture(t, "terminal-cancel-"+tc.name, "proposal")
			workID := "terminal-cancel-" + tc.name
			cancelWithReviewedEvidence(t, f.store, workID, f.owner.PrincipalRef, tc.refs)
			err := f.store.CheckReviewedClosureContentHash(t.Context(), workID, "sha256:"+strings.Repeat("c", 64))
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertFailureKind(t, err, tc.want)
			}
			if tc.want == KindOutcomeMismatch {
				requireTerminalHash(t, f.store, workID, closureTestHash)
				if err := f.store.CheckReviewedClosureContentHash(t.Context(), workID, closureTestHash); err != nil {
					t.Fatal(err)
				}
			} else if tc.want == "" {
				requireTerminalHash(t, f.store, workID, "")
			}
		})
	}
}

func TestReviewedTerminalCancellationRevalidatesProvenance(t *testing.T) {
	for _, mutation := range []string{
		`UPDATE work_items SET transition_actor='principal/forged' WHERE id=?`,
		`UPDATE work_items SET transition_seq=transition_seq+1 WHERE id=?`,
		`UPDATE work_items SET transition_version=transition_version-1 WHERE id=?`,
		`UPDATE work_items SET transition_reason='forged reason' WHERE id=?`,
		`UPDATE work_items SET transition_evidence_refs='["sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"]' WHERE id=?`,
	} {
		t.Run(mutation, func(t *testing.T) {
			s := openTemp(t)
			seedWork(t, s, "cancel-provenance")
			cancelWithReviewedEvidence(t, s, "cancel-provenance", "principal/operator", []string{closureTestHash})
			requireTerminalHash(t, s, "cancel-provenance", closureTestHash)
			if _, err := s.db.Exec(`INSERT INTO fold_guard(active) VALUES(1); `+mutation+`; DELETE FROM fold_guard`, "cancel-provenance"); err != nil {
				t.Fatal(err)
			}
			assertFailureKind(t, s.CheckReviewedClosureContentHash(t.Context(), "cancel-provenance", closureTestHash), KindInvariantViolation)
		})
	}
}

func TestReviewedTerminalCancellationDoesNotReuseCloseBinding(t *testing.T) {
	for _, refs := range [][]string{nil, {"sha256:" + strings.Repeat("b", 64)}} {
		f := closureBindingFixture(t, "cancel-after-close", "release")
		if err := closureBind(t, f, "cancel-after-close", "review", closureTestHash); err != nil {
			t.Fatal(err)
		}
		cancelWithReviewedEvidence(t, f.store, "cancel-after-close", f.owner.PrincipalRef, refs)
		want := ""
		if len(refs) != 0 {
			want = refs[0]
		}
		requireTerminalHash(t, f.store, "cancel-after-close", want)
		requireClosureHash(t, f.store, "cancel-after-close", closureTestHash)
	}
}

func TestReviewedTerminalUnsupportedRoutesAndReopen(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "terminal-old")
	seedWork(t, s, "terminal-new")
	cancelWithReviewedEvidence(t, s, "terminal-old", "operator", []string{closureTestHash})
	requireTerminalHash(t, s, "terminal-old", closureTestHash)
	version := readWorkVersion(t, s, "terminal-old")
	if err := applyWorkEvent(t, s, workReopenedEvent("reopen-reviewed", "terminal-old", "cancelled", version, version+1), workVersion("terminal-old", version)); err != nil {
		t.Fatal(err)
	}
	requireTerminalHash(t, s, "terminal-old", "")
	version++
	if err := applyWorkEvent(t, s, workSupersededEvent("supersede-reviewed", "terminal-new", "terminal-old", version, version+1), workVersion("terminal-old", version)); err != nil {
		t.Fatal(err)
	}
	requireTerminalHash(t, s, "terminal-old", "")
	if err := s.CheckReviewedClosureContentHash(t.Context(), "terminal-old", closureTestHash); err != nil {
		t.Fatal(err)
	}
	seedWork(t, s, "terminal-manual")
	if err := applyWorkEvent(t, s, workTransitionEvent("manual-reviewed", "terminal-manual", "needed", "completed", 2, 3), workVersion("terminal-manual", 2)); err != nil {
		t.Fatal(err)
	}
	requireTerminalHash(t, s, "terminal-manual", "")
	requireTerminalHash(t, s, "terminal-absent", "")
}

func TestReviewedClosureLifecycleCompletionAfterCancelledWorkflowRequiresJudgement(t *testing.T) {
	for _, from := range []string{"needed", "in_progress"} {
		for _, reviewedCancellation := range []bool{false, true} {
			name := from + "/ordinary_cancellation"
			if reviewedCancellation {
				name = from + "/reviewed_cancellation"
			}
			t.Run(name, func(t *testing.T) {
				const workID = "lifecycle-after-cancel"
				f := closureBindingFixture(t, workID, "proposal")
				refs := []string{"evidence:ordinary"}
				if reviewedCancellation {
					refs = []string{closureTestHash}
				}
				cancelWithReviewedEvidence(t, f.store, workID, f.owner.PrincipalRef, refs)
				version := readWorkVersion(t, f.store, workID)
				if err := applyWorkEvent(t, f.store, workReopenedEvent("reopen-cancelled-workflow", workID, "cancelled", version, version+1), workVersion(workID, version)); err != nil {
					t.Fatal(err)
				}
				version++
				if from == "in_progress" {
					if err := applyWorkEvent(t, f.store, workTransitionEvent("restart-cancelled-workflow", workID, "needed", from, version, version+1), workVersion(workID, version)); err != nil {
						t.Fatal(err)
					}
					version++
				}
				snapshot := func() string {
					t.Helper()
					var state string
					if err := f.store.db.QueryRow(`SELECT json_array(w.lifecycle,w.version,w.transition_from,w.transition_reason,w.transition_actor,w.transition_evidence_refs,w.transition_seq,w.transition_version,w.updated_at,w.terminal_time,i.instance_state,i.completed_at,
 (SELECT count(*) FROM effect_receipts WHERE subject_type='work_item' AND subject_id=w.id),
 (SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=w.id))
 FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, workID).Scan(&state); err != nil {
						t.Fatal(err)
					}
					return state
				}
				before := snapshot()
				err := applyWorkEvent(t, f.store, workTransitionEvent("complete-cancelled-workflow", workID, from, "completed", version, version+1), workVersion(workID, version))
				assertFailureKind(t, err, KindMissingEvidence)
				if !strings.Contains(err.Error(), "reviewed closure") {
					t.Fatalf("expected reviewed closure refusal, got %v", err)
				}
				if after := snapshot(); after != before {
					t.Fatalf("refused lifecycle completion changed state or receipts:\nbefore %s\nafter  %s", before, after)
				}
				requireClosureHash(t, f.store, workID, "")
				requireTerminalHash(t, f.store, workID, "")
			})
		}
	}
}

func TestReviewedClosureLifecycleCompletionWithoutWorkflowAllowed(t *testing.T) {
	for _, from := range []string{"needed", "in_progress"} {
		t.Run(from, func(t *testing.T) {
			const workID = "legacy-lifecycle-completion"
			s := openTemp(t)
			seedWork(t, s, workID)
			version := readWorkVersion(t, s, workID)
			if from == "in_progress" {
				if err := applyWorkEvent(t, s, workTransitionEvent("legacy-start", workID, "needed", from, version, version+1), workVersion(workID, version)); err != nil {
					t.Fatal(err)
				}
				version++
			}
			if err := applyWorkEvent(t, s, workTransitionEvent("legacy-complete", workID, from, "completed", version, version+1), workVersion(workID, version)); err != nil {
				t.Fatal(err)
			}
			var lifecycle string
			if err := s.db.QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, workID).Scan(&lifecycle); err != nil || lifecycle != "completed" || readWorkVersion(t, s, workID) != version+1 {
				t.Fatalf("legacy completion lifecycle=%q err=%v", lifecycle, err)
			}
			requireTerminalHash(t, s, workID, "")
		})
	}
}

func TestReviewedClosureLifecycleRepairPreservesCompletedWorkflow(t *testing.T) {
	const workID = "reviewed-lifecycle-repair"
	s, _ := seedCompletionGateCase(t, workID, completionGateCase{
		requiredEvidence: []string{"verification", "review"}, emptyMandate: true,
		includeVerdict: true, includePremise: true, verdictKind: "ok", omitClosure: true,
	})
	f := workflowReturnRouteFixture{store: s, owner: WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}}
	if err := closureBind(t, f, workID, "review", closureTestHash); err != nil {
		t.Fatal(err)
	}
	if err := runVerdictAction(t, s, workID, "complete", json.RawMessage(`{"impact_verdict":"non-breaking"}`)); err != nil {
		t.Fatal(err)
	}
	var completedAt string
	if err := s.db.QueryRow(`SELECT completed_at FROM workflow_instances WHERE work_id=?`, workID).Scan(&completedAt); err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	if err := applyWorkEvent(t, s, workReopenedEvent("reopen-completed-workflow", workID, "completed", version, version+1), workVersion(workID, version)); err != nil {
		t.Fatal(err)
	}
	version++
	if err := applyWorkEvent(t, s, workTransitionEvent("repair-reviewed-lifecycle", workID, "needed", "completed", version, version+1), workVersion(workID, version)); err != nil {
		t.Fatal(err)
	}
	var lifecycle, state, afterCompletedAt string
	if err := s.db.QueryRow(`SELECT w.lifecycle,i.instance_state,i.completed_at FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, workID).Scan(&lifecycle, &state, &afterCompletedAt); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "completed" || state != "completed" || afterCompletedAt != completedAt || readWorkVersion(t, s, workID) != version+1 {
		t.Fatalf("lifecycle repair lifecycle=%q state=%q stamp=%q, original stamp=%q", lifecycle, state, afterCompletedAt, completedAt)
	}
	requireTerminalHash(t, s, workID, closureTestHash)
}

func closureBindingFixture(t *testing.T, workID, stepID string) workflowReturnRouteFixture {
	t.Helper()
	return seedWorkflowReturnRouteFixtureWithDefinition(t, workID, mustBuiltinDefinition(t, "workflow.implementation"), stepID, []string{}, nil)
}

func closureBind(t *testing.T, f workflowReturnRouteFixture, workID, kind, hash string) error {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"evidence_kind": kind, "immutable_subject_ref": hash})
	if err != nil {
		t.Fatal(err)
	}
	return runVerdictActionAs(t, f.store, workID, "bind_evidence", payload, 0, f.owner)
}

func bindCompletionClosure(t *testing.T, s *Store, workID string, owner WorkflowActor) {
	t.Helper()
	if err := closureBind(t, workflowReturnRouteFixture{store: s, owner: owner}, workID, "review", closureTestHash); err != nil {
		t.Fatalf("bind reviewed completion closure: %v", err)
	}
	requireClosureHash(t, s, workID, closureTestHash)
}

func TestReviewedClosureCloseStepDeclaresBinding(t *testing.T) {
	for _, ref := range []string{"workflow.implementation", "workflow.break_fix", "workflow.research"} {
		definition := mustBuiltinDefinition(t, ref).Definition
		for _, step := range definition.StepGraph.Steps {
			if stepDeclaresAction(definition, step.ID, "complete") && !stepDeclaresAction(definition, step.ID, "bind_evidence") {
				t.Errorf("%s close step %s does not declare bind_evidence", ref, step.ID)
			}
		}
	}
}

func TestReviewedClosureOrdinaryRecoverySubjectIsNotJudgement(t *testing.T) {
	for _, tc := range []struct{ name, kind, hash string }{
		{"verification", "verification", closureTestHash},
		{"locator", "review", "receipt:unreviewed"},
		{"malformed_hash", "durable_note", "sha256:" + strings.Repeat("g", 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workID := "closure-invalid-" + tc.name
			f := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, mustBuiltinDefinition(t, "workflow.implementation"), "release", []string{tc.kind}, nil)
			if err := closureBind(t, f, workID, tc.kind, tc.hash); err != nil {
				t.Fatalf("ordinary recovery binding refused: %v", err)
			}
			requireClosureHash(t, f.store, workID, "")
			assertFailureKind(t, requireReviewedClosureQ(t.Context(), f.store.db, workID), KindMissingEvidence)
		})
	}
}

func TestReviewedClosureEnvelopeOrdinaryKindCannotUseExemption(t *testing.T) {
	for _, alreadyBound := range []bool{false, true} {
		name := "no_outstanding"
		if alreadyBound {
			name = "already_bound"
		}
		t.Run(name, func(t *testing.T) {
			workID := "closure-envelope-" + name
			entry := mustBuiltinDefinition(t, "workflow.implementation")
			f := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, entry, "execution", []string{"verification"}, []string{"review", "artifact"}, map[string]any{
				"predicate_id": "predicate:envelope", "ordinal": 0, "outcome_kind": "exists",
				"outcome_payload": map[string]any{"kind": "exists", "subjects": []string{workID}, "surface": "work_items"},
			})
			verificationRef := "receipt:verified"
			if alreadyBound {
				verificationRef = closureTestHash
			}
			if err := closureBind(t, f, workID, "verification", verificationRef); err != nil {
				t.Fatal(err)
			}
			actor, _ := WorkflowActorRef(f.owner)
			if err := advanceWorkflowTestInstanceToStep(t.Context(), f.store, workID, "release", actor); err != nil {
				t.Fatal(err)
			}
			outstanding, err := outstandingWorkflowEvidenceRequirementsForWork(t.Context(), f.store.db, workID, entry.Definition)
			if err != nil || len(outstanding) != 0 {
				t.Fatalf("ordinary evidence debt=%v err=%v, want none", outstanding, err)
			}
			priorHash := "sha256:" + strings.Repeat("b", 64)
			if err := closureBind(t, f, workID, "review", priorHash); err != nil {
				t.Fatal(err)
			}
			payload := mustJSONValue(map[string]any{"evidence_kind": "review", "immutable_subject_ref": closureTestHash})
			assertRecoveryRefusalWithoutEffect(t, f.store, workID, KindIllegalLifecycleTransition, func() error {
				return runBindAction(t, f.store, workID, payload, []string{closureTestHash}, "verification")
			})
			requireClosureHash(t, f.store, workID, priorHash)
		})
	}
}

func TestReviewedClosureEnvelopeClosureKindsRemainAdmitted(t *testing.T) {
	for _, tc := range []struct {
		name  string
		kinds []string
		count int
	}{
		{"payload_only", nil, 1},
		{"review", []string{"review"}, 1},
		{"durable_note", []string{"durable_note"}, 2},
		{"closure_duplicates", []string{"review", "durable_note", "review"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workID := "closure-envelope-positive-" + tc.name
			f := closureBindingFixture(t, workID, "release")
			payload := mustJSONValue(map[string]any{"evidence_kind": "review", "immutable_subject_ref": closureTestHash})
			if err := runBindAction(t, f.store, workID, payload, []string{closureTestHash}, tc.kinds...); err != nil {
				t.Fatal(err)
			}
			requireClosureHash(t, f.store, workID, closureTestHash)
			var count int
			if err := f.store.db.QueryRow(`SELECT count(*) FROM workflow_evidence_bindings WHERE work_id=? AND json_extract(payload,'$.immutable_subject_ref')=?`, workID, closureTestHash).Scan(&count); err != nil || count != tc.count {
				t.Fatalf("emitted binding count=%d err=%v, want %d", count, err, tc.count)
			}
		})
	}
}

func TestReviewedClosureOrdinaryCompleteRequiresJudgement(t *testing.T) {
	const workID = "closure-required-complete"
	s, _ := seedCompletionGateCase(t, workID, completionGateCase{
		requiredEvidence: []string{"verification", "review"}, emptyMandate: true,
		includeVerdict: true, includePremise: true, verdictKind: "ok", omitClosure: true,
	})
	requireClosureHash(t, s, workID, "")
	before := readWorkVersion(t, s, workID)
	err := runVerdictAction(t, s, workID, "complete", json.RawMessage(`{"impact_verdict":"non-breaking"}`))
	assertFailureKind(t, err, KindMissingEvidence)
	if err == nil || !strings.Contains(err.Error(), "reviewed closure") {
		t.Fatalf("expected the reviewed closure refusal, got %v", err)
	}
	if readWorkVersion(t, s, workID) != before {
		t.Fatal("refused completion changed the work")
	}
}

func requireClosureHash(t *testing.T, s *Store, workID, want string) {
	t.Helper()
	var got string
	err := s.readSnapshot(t.Context(), func(q queryer) error {
		var err error
		got, err = reviewedClosureContentHashQ(t.Context(), q, workID)
		return err
	})
	if err != nil || got != want {
		t.Fatalf("reviewed hash=%q error=%v, want %q", got, err, want)
	}
}

func closureReadError(t *testing.T, s *Store, workID string) error {
	t.Helper()
	return s.readSnapshot(t.Context(), func(q queryer) error {
		_, err := reviewedClosureContentHashQ(t.Context(), q, workID)
		return err
	})
}

func requireTerminalHash(t *testing.T, s *Store, workID, want string) {
	t.Helper()
	var got string
	err := s.readSnapshot(t.Context(), func(q queryer) error {
		var err error
		got, err = reviewedTerminalContentHashQ(t.Context(), q, workID)
		return err
	})
	if err != nil || got != want {
		t.Fatalf("terminal hash=%q error=%v, want %q", got, err, want)
	}
}

func TestReviewedClosureRealBindingAndQ7(t *testing.T) {
	for _, kind := range []string{"review", "durable_note"} {
		t.Run(kind, func(t *testing.T) {
			workID := "closure-real-" + kind
			f := closureBindingFixture(t, workID, "release")
			requireClosureHash(t, f.store, workID, "")
			if err := closureBind(t, f, workID, kind, closureTestHash); err != nil {
				t.Fatal(err)
			}
			requireClosureHash(t, f.store, workID, closureTestHash)
			if err := f.store.CheckReviewedClosureContentHash(t.Context(), workID, closureTestHash); err != nil {
				t.Fatal(err)
			}
			requireTerminalHash(t, f.store, workID, "")
			out, err := f.store.QueryQ7(t.Context(), Q7Request{Work: workID, RecordKinds: []string{"phase_exit", "evidence_binding"}, Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			exit, binding := false, false
			actor, _ := WorkflowActorRef(f.owner)
			for _, entry := range out.Entries {
				if entry.EntryKind == "phase_exit" && entry.ActionID == "bind_evidence" && entry.StepID == "release" && entry.Actor == actor && entry.ContractVersion == 1 {
					exit = true
				}
				if entry.EntryKind == "evidence_binding" && entry.EvidenceKind == kind && entry.ImmutableSubjectRef == closureTestHash && entry.Actor == actor {
					binding = true
				}
			}
			if !exit || !binding {
				t.Fatalf("Q7 closure exit=%v binding=%v entries=%+v", exit, binding, out.Entries)
			}
			// Recovery without an outstanding requirement must not erase the judgement.
			assertFailureKind(t, closureBind(t, f, workID, "approval", "receipt:unrelated"), KindIllegalLifecycleTransition)
			requireClosureHash(t, f.store, workID, closureTestHash)
		})
	}
}

func TestReviewedClosureUnboundAndVerificationAreNotJudgement(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "closure-manual")
	requireClosureHash(t, s, "closure-manual", "")
	if err := s.CheckReviewedClosureContentHash(t.Context(), "closure-manual", closureTestHash); err != nil {
		t.Fatal(err)
	}
	const workID = "closure-verification"
	f := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, mustBuiltinDefinition(t, "workflow.break_fix"), "verify", []string{}, nil)
	for _, kind := range []string{"verification", "review"} {
		if err := closureBind(t, f, workID, kind, closureTestHash); err != nil {
			t.Fatal(err)
		}
		requireClosureHash(t, f.store, workID, "")
	}
	// Moving into close does not relabel the earlier verify-step exit.
	actor, _ := WorkflowActorRef(f.owner)
	if err := advanceWorkflowTestInstanceToStep(t.Context(), f.store, workID, "complete", actor); err != nil {
		t.Fatal(err)
	}
	requireClosureHash(t, f.store, workID, "")
}

func TestReviewedClosureRevalidatesRecordedProvenance(t *testing.T) {
	for _, tc := range []struct{ name, statement string }{
		{"exit_actor", `UPDATE workflow_phase_exits SET actor='actor:other' WHERE work_id=? AND action_id='bind_evidence'`},
		{"exit_coordinate", `UPDATE workflow_phase_exits SET exit_seq=exit_seq+1 WHERE work_id=? AND action_id='bind_evidence'`},
		{"exit_version", `UPDATE workflow_phase_exits SET record_version=record_version+1 WHERE work_id=? AND action_id='bind_evidence'`},
		{"producer_identity", `UPDATE durable_operations SET principal_ref='principal/other' WHERE work_id=? AND step_id='release'`},
		{"producer_watermark", `UPDATE durable_operations SET request_id='request/other' WHERE work_id=? AND step_id='release'`},
		{"producer_work", `UPDATE durable_operations SET work_id='work:other' WHERE work_id=? AND step_id='release'`},
		{"producer_pending", `UPDATE durable_operations SET result_kind='pending' WHERE work_id=? AND step_id='release'`},
		{"producer_unsettled", `UPDATE durable_operations SET completed_at=NULL WHERE work_id=? AND step_id='release'`},
		{"producer_evidence", `UPDATE durable_operations SET evidence_refs='["receipt:other"]' WHERE work_id=? AND step_id='release'`},
		{"binding_payload", `UPDATE workflow_evidence_bindings SET payload=json_set(payload,'$.observed_at','2026-10-01T00:00:00Z') WHERE work_id=?`},
		{"binding_kind", `UPDATE workflow_evidence_bindings SET payload=json_set(payload,'$.evidence_kind','verification') WHERE work_id=?`},
		{"binding_hash", `UPDATE workflow_evidence_bindings SET payload=json_set(payload,'$.immutable_subject_ref','receipt:ordinary') WHERE work_id=?`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workID := "closure-provenance-" + tc.name
			f := closureBindingFixture(t, workID, "release")
			if err := closureBind(t, f, workID, "review", closureTestHash); err != nil {
				t.Fatal(err)
			}
			requireClosureHash(t, f.store, workID, closureTestHash)
			if _, err := f.store.db.Exec(`INSERT INTO fold_guard(active) VALUES(1); `+tc.statement+`; DELETE FROM fold_guard`, workID); err != nil {
				t.Fatal(err)
			}
			assertFailureKind(t, closureReadError(t, f.store, workID), KindInvariantViolation)
		})
	}
}

func TestReviewedClosureStaleContractIsNotJudgement(t *testing.T) {
	const workID = "closure-stale-contract"
	f := closureBindingFixture(t, workID, "release")
	if err := closureBind(t, f, workID, "review", closureTestHash); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE workflow_phase_exits SET contract_version=0 WHERE work_id=? AND action_id='bind_evidence'; DELETE FROM fold_guard`, workID); err != nil {
		t.Fatal(err)
	}
	requireClosureHash(t, f.store, workID, "")
	assertFailureKind(t, requireReviewedClosureQ(t.Context(), f.store.db, workID), KindMissingEvidence)
}

func TestReviewedClosureBoundOrdinaryComplete(t *testing.T) {
	const workID = "closure-bound-complete"
	s, _ := seedCompletionGateCase(t, workID, completionGateCase{
		requiredEvidence: []string{"verification", "review"}, emptyMandate: true,
		includeVerdict: true, includePremise: true, verdictKind: "ok", omitClosure: true,
	})
	f := workflowReturnRouteFixture{store: s, owner: WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}}
	if err := closureBind(t, f, workID, "review", closureTestHash); err != nil {
		t.Fatal(err)
	}
	if err := runVerdictAction(t, s, workID, "complete", json.RawMessage(`{"impact_verdict":"non-breaking"}`)); err != nil {
		t.Fatal(err)
	}
	var state, lifecycle string
	if err := s.db.QueryRowContext(context.Background(), `SELECT i.instance_state,w.lifecycle FROM workflow_instances i JOIN work_items w ON w.id=i.work_id WHERE i.work_id=?`, workID).Scan(&state, &lifecycle); err != nil {
		t.Fatal(err)
	}
	if state != "completed" || lifecycle != "completed" {
		t.Fatalf("completion state=%s lifecycle=%s", state, lifecycle)
	}
	requireClosureHash(t, s, workID, closureTestHash)
	requireTerminalHash(t, s, workID, closureTestHash)
	if err := s.CheckReviewedClosureContentHash(t.Context(), workID, closureTestHash); err != nil {
		t.Fatal(err)
	}
	assertFailureKind(t, s.CheckReviewedClosureContentHash(t.Context(), workID, "sha256:"+strings.Repeat("b", 64)), KindOutcomeMismatch)
}

func TestReviewedClosureRecoveryRequiresFinalRebind(t *testing.T) {
	const workID = "closure-recovery-rebind"
	s, _ := seedCompletionGateCase(t, workID, completionGateCase{
		requiredEvidence: []string{"verification", "review"},
		includeVerdict:   true, includePremise: true, verdictKind: "ok", omitClosure: true,
	})
	f := workflowReturnRouteFixture{store: s, owner: WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}}
	if err := closureBind(t, f, workID, "review", closureTestHash); err != nil {
		t.Fatal(err)
	}
	requireClosureHash(t, s, workID, closureTestHash)
	// Reaffirming the same reviewed bytes is not ordinary tuple recovery.
	if err := closureBind(t, f, workID, "review", closureTestHash); err != nil {
		t.Fatalf("repeat reviewed closure: %v", err)
	}
	ordinary := json.RawMessage(`{"evidence_kind":"artifact","immutable_subject_ref":"spec:one"}`)
	if err := issue1013Preflight(t, s, workID, "bind_evidence", ordinary, f.owner); err != nil {
		t.Fatalf("preflight late ordinary mandate binding: %v", err)
	}
	if err := closureBind(t, f, workID, "artifact", "spec:one"); err != nil {
		t.Fatalf("late ordinary mandate binding: %v", err)
	}
	requireClosureHash(t, s, workID, "")
	snapshot := func() string {
		t.Helper()
		var state string
		if err := s.db.QueryRow(`SELECT json_array(w.lifecycle,w.version,i.instance_state,i.completed_at,
 (SELECT count(*) FROM effect_receipts WHERE subject_id=w.id),
 (SELECT count(*) FROM domain_events WHERE subject_id=w.id),
 (SELECT count(*) FROM durable_operations WHERE work_id=w.id),
 (SELECT count(*) FROM workflow_evidence_bindings WHERE work_id=w.id),
 (SELECT json_group_array(json_array(action_id,exit_seq,record_version)) FROM workflow_phase_exits WHERE work_id=w.id))
 FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, workID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	before := snapshot()
	assertFailureKind(t, runVerdictAction(t, s, workID, "complete", json.RawMessage(`{"impact_verdict":"non-breaking"}`)), KindMissingEvidence)
	if after := snapshot(); after != before {
		t.Fatalf("refused completion changed execution:\nbefore %s\nafter  %s", before, after)
	}
	finalHash := "sha256:" + strings.Repeat("b", 64)
	if err := closureBind(t, f, workID, "durable_note", finalHash); err != nil {
		t.Fatalf("rebind closure after ordinary recovery: %v", err)
	}
	if err := runVerdictAction(t, s, workID, "complete", json.RawMessage(`{"impact_verdict":"non-breaking"}`)); err != nil {
		t.Fatal(err)
	}
	requireClosureHash(t, s, workID, finalHash)
	// Cleanup and publication share this terminal proof selector.
	requireTerminalHash(t, s, workID, finalHash)
	if err := s.CheckReviewedClosureContentHash(t.Context(), workID, finalHash); err != nil {
		t.Fatal(err)
	}
	assertFailureKind(t, s.CheckReviewedClosureContentHash(t.Context(), workID, closureTestHash), KindOutcomeMismatch)
}

func TestReviewedClosureDirectCompleteRequiresJudgement(t *testing.T) {
	const workID = "closure-direct-complete"
	s, completion := seedCompletionGateCase(t, workID, completionGateCase{
		requiredEvidence: []string{"verification", "review"}, emptyMandate: true,
		includeVerdict: true, includePremise: true, verdictKind: "ok", omitClosure: true,
	})
	requireClosureHash(t, s, workID, "")
	before := readWorkVersion(t, s, workID)
	err := CompleteWorkflow(t.Context(), s, completion)
	assertFailureKind(t, err, KindMissingEvidence)
	if !strings.Contains(err.Error(), "reviewed closure") || readWorkVersion(t, s, workID) != before {
		t.Fatalf("direct completion refusal=%v", err)
	}
}

func TestReviewedClosureExternalProducerAndUnrelatedBinding(t *testing.T) {
	const workID = "closure-other-binding"
	f := closureBindingFixture(t, workID, "execution")
	if err := closureBind(t, f, workID, "artifact", "receipt:unrelated"); err != nil {
		t.Fatal(err)
	}
	var unrelated workflowEvidenceBoundPayload
	var raw []byte
	if err := f.store.db.QueryRow(`SELECT payload FROM workflow_evidence_bindings WHERE work_id=? ORDER BY binding_seq DESC LIMIT 1`, workID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &unrelated); err != nil {
		t.Fatal(err)
	}
	if err := closureBind(t, f, workID, "review", closureTestHash); err != nil {
		t.Fatal(err)
	}
	var producer workflowEvidenceBoundPayload
	if err := f.store.db.QueryRow(`SELECT payload FROM workflow_evidence_bindings WHERE work_id=? ORDER BY binding_seq DESC LIMIT 1`, workID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &producer); err != nil {
		t.Fatal(err)
	}
	actor, _ := WorkflowActorRef(f.owner)
	if err := advanceWorkflowTestInstanceToStep(t.Context(), f.store, workID, "release", actor); err != nil {
		t.Fatal(err)
	}
	requireClosureHash(t, f.store, workID, "")
	payload, _ := json.Marshal(map[string]any{
		"evidence_kind": "review", "immutable_subject_ref": closureTestHash,
		"producer_id": producer.ProducerID, "producer_run_ref": producer.ProducerRunRef, "producer_watermark": producer.ProducerWatermark,
	})
	if err := runVerdictActionAs(t, f.store, workID, "bind_evidence", payload, 0, f.owner); err != nil {
		t.Fatal(err)
	}
	requireClosureHash(t, f.store, workID, closureTestHash)
	// A separate producer event may add binding metadata, but cannot replace
	// the close action's exit or change the hash selected by that exit.
	version := readWorkVersion(t, f.store, workID)
	if err := applyWorkflowTestOperation(t.Context(), f.store, Operation{Events: []Event{workflowEventWithActor("closure-unrelated-metadata", WorkflowEvidenceBound, workID, actor, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"evidence_kind": "artifact", "immutable_subject_ref": unrelated.ImmutableSubjectRef, "producer_id": unrelated.ProducerID,
		"producer_run_ref": unrelated.ProducerRunRef, "producer_watermark": unrelated.ProducerWatermark, "observed_at": "2026-09-12T00:00:00Z",
	})}, ExpectedVersions: workVersion(workID, version)}); err != nil {
		t.Fatal(err)
	}
	requireClosureHash(t, f.store, workID, closureTestHash)
	requireTerminalHash(t, f.store, workID, "")
	// The referenced producer is checked separately from the binder operation.
	if _, err := f.store.db.Exec(`UPDATE durable_operations SET request_id='request/forged' WHERE op_id=?`, producer.ProducerRunRef); err != nil {
		t.Fatal(err)
	}
	assertFailureKind(t, closureReadError(t, f.store, workID), KindInvariantViolation)
}
