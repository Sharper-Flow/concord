package agent

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestSpineCompleteStepContractRecovery(t *testing.T) {
	for _, family := range []struct{ ref, close, target string }{
		{"workflow.implementation", "release", "execution"},
		{"workflow.break_fix", "complete", "repair"},
	} {
		t.Run(family.ref, func(t *testing.T) {
			f := newSpineFixtureForFamily(t, family.ref)
			f.advanceToAcceptance(t)
			spineOK(t, f.call(t, "concord_work_define", "observation_record", map[string]any{
				"work_id": f.work, "statement": "The current Domain's approved value criterion has checked implementation and accepted Review.",
				"refs": []string{f.read(t).Pinned.Contract.ArchitectureBinding.HomeDomainID, f.proof}, "idempotency_key": "spine-complete-investigation",
			}))
			proof := map[string]any{"verdicts": []map[string]any{{"predicate_id": "predicate:primary", "receipt_refs": []string{f.proof}}}}
			spineOK(t, f.action(t, "record_verdict", proof))
			spineOK(t, f.call(t, "concord_work_transition", "workflow_action", map[string]any{
				"work_id": f.work, "expected_version": f.read(t).Pinned.Pin.Version, "action_id": "confirm_premise",
				"idempotency_key": "spine-complete-confirm",
			}))
			before := f.read(t)
			if before.Pinned.Step != family.close || before.Pinned.Pin.Lifecycle != "in_progress" {
				t.Fatalf("public progression did not reach the nonterminal complete step: %+v", before.Pinned)
			}
			contract := before.Pinned.Contract
			predicates := make([]map[string]any, 0, len(contract.OutcomePredicates))
			for _, predicate := range contract.OutcomePredicates {
				predicates = append(predicates, map[string]any{
					"predicate_id": predicate.PredicateID, "ordinal": predicate.Ordinal,
					"outcome_kind": predicate.OutcomeKind, "outcome_payload": json.RawMessage(predicate.OutcomePayload),
				})
			}
			fields := map[string]any{
				"contract_version": contract.Version + 1, "predecessor_contract_versions": []int64{contract.Version},
				"premise": contract.Premise, "outcome_predicates": predicates,
				"required_evidence": contract.RequiredEvidence, "route_conventions": []string{"complete_step_correction"},
				"spec_mandate": []string{}, "law_modifies": []string{}, "architecture_binding": contract.ArchitectureBinding,
				"rigor_class": "prototype_internal", "decision_domains": []string{},
				"supersede_reason": "Reassess the implementation while keeping the approved criterion unchanged.",
				"audit_evidence":   []string{f.proof},
			}
			actor := store.WorkflowActor{PrincipalRef: f.env.PrincipalRef, ClientRef: f.env.ClientRef, AgentRef: f.env.AgentRef, SessionRef: f.env.SessionRef, ActorClass: store.ActorAgent}
			refuse := func(t *testing.T, payload map[string]any) {
				t.Helper()
				before := f.read(t)
				effects := premiseConsentSnapshot(t, f.s)
				err := store.WorkflowActionPreflightWithRegistry(t.Context(), f.s, store.BuiltinWorkflowRegistry(), store.WorkflowActionPreflightRequest{
					WorkID: f.work, ExpectedVersion: before.Pinned.Pin.Version, ActionID: "supersede_contract", Payload: retryJSON(payload), Actor: actor, SessionWorktree: f.path,
				})
				var failure *store.Failure
				if !errors.As(err, &failure) {
					t.Fatalf("preflight admitted invalid complete-step recovery: %v", err)
				}
				response := f.action(t, "supersede_contract", payload)
				if response.Outcome != agentcontract.OutcomeError || response.Error == nil || response.Error.Kind != mapFailureKind(failure.Kind) || response.Error.Message != failure.Detail || response.Error.EffectState != agentcontract.EffectNone || !reflect.DeepEqual(effects, premiseConsentSnapshot(t, f.s)) {
					t.Fatalf("complete-step refusal disagreed with preflight or changed state: preflight=%v response=%+v", err, response.Error)
				}
			}
			t.Run("no later observation", func(t *testing.T) { refuse(t, fields) })
			spineOK(t, f.call(t, "concord_work_define", "observation_record", map[string]any{
				"work_id": f.work, "statement": "The implementation needs reassessment under the unchanged approved value criterion.",
				"refs": []string{contract.ArchitectureBinding.HomeDomainID, f.proof}, "idempotency_key": "spine-complete-contradiction",
			}))
			t.Run("missing declared return convention", func(t *testing.T) {
				invalid := make(map[string]any, len(fields))
				for key, value := range fields {
					invalid[key] = value
				}
				invalid["route_conventions"] = []string{}
				refuse(t, invalid)
			})
			before = f.read(t)
			if !slices.ContainsFunc(before.Pinned.Pin.NextValidIntents, func(i store.WorkPinIntent) bool { return i.ActionID == "supersede_contract" }) {
				t.Fatal("the current complete-step recovery is not advertised")
			}
			if err := store.WorkflowActionPreflightWithRegistry(t.Context(), f.s, store.BuiltinWorkflowRegistry(), store.WorkflowActionPreflightRequest{
				WorkID: f.work, ExpectedVersion: before.Pinned.Pin.Version, ActionID: "supersede_contract", Payload: retryJSON(fields), Actor: actor, SessionWorktree: f.path,
			}); err != nil {
				t.Fatalf("current complete-step recovery preflight: %v", err)
			}
			input := map[string]any{"work_id": f.work, "expected_version": before.Pinned.Pin.Version, "action_id": "supersede_contract", "fields": fields, "idempotency_key": "spine-complete-supersede"}
			spineOK(t, f.call(t, "concord_work_transition", "workflow_action", input))
			after := f.read(t)
			if after.WorkID != before.WorkID || after.Pinned.Step != family.target || after.Pinned.Pin.Lifecycle != before.Pinned.Pin.Lifecycle || after.Pinned.Contract.Version != contract.Version+1 || !reflect.DeepEqual(after.Pinned.Contract.OutcomePredicates, contract.OutcomePredicates) || !reflect.DeepEqual(after.Pinned.Pin.Review.Candidate, before.Pinned.Pin.Review.Candidate) {
				t.Fatalf("complete-step recovery lost its target, subject or routine authority: %+v", after.Pinned)
			}
			effects := premiseConsentSnapshot(t, f.s)
			replayed := spineOK(t, f.call(t, "concord_work_transition", "workflow_action", input))
			if !replayed.Replayed || !reflect.DeepEqual(effects, premiseConsentSnapshot(t, f.s)) {
				t.Fatal("exact complete-step recovery replay had another effect")
			}
			// The unchanged criterion and candidate still have applicable proof.
			// A return to execution must not invent a need for another edit.
			entry := "start_execution"
			if family.target == "repair" {
				entry = "checkpoint_repair"
			}
			spineOK(t, f.action(t, entry, map[string]any{}))
			for _, binding := range []store.WorkPinEvidence{
				{EvidenceKind: "verification", ImmutableSubjectRef: f.proof},
				{EvidenceKind: "review", ImmutableSubjectRef: after.Pinned.Pin.Review.Accepted.ReportRef},
				{EvidenceKind: "artifact", ImmutableSubjectRef: after.Pinned.Pin.Review.Candidate.Commit},
			} {
				spineOK(t, f.action(t, "bind_evidence", map[string]any{"evidence_kind": binding.EvidenceKind, "evidence_ref": binding.ImmutableSubjectRef}))
			}
			f.review(t, "ready_for_final_proof", f.proof)
			f.advanceToAcceptance(t)
			spineOK(t, f.call(t, "concord_work_define", "observation_record", map[string]any{
				"work_id": f.work, "statement": "Reassessment retains the checked candidate and unchanged approved criterion.",
				"refs": []string{contract.ArchitectureBinding.HomeDomainID, f.proof}, "idempotency_key": "spine-successor-investigation",
			}))
			spineOK(t, f.action(t, "record_verdict", proof))
			spineOK(t, f.call(t, "concord_work_transition", "workflow_action", map[string]any{
				"work_id": f.work, "expected_version": f.read(t).Pinned.Pin.Version, "action_id": "confirm_premise",
				"idempotency_key": "spine-successor-confirm",
			}))
			spineOK(t, f.action(t, "bind_evidence", map[string]any{"evidence_kind": "review", "evidence_ref": "sha256:" + strings.Repeat("a", 64)}))
			spineOK(t, f.action(t, "complete", map[string]any{"impact_verdict": "non-breaking"}))
			done := f.read(t)
			if done.WorkID != before.WorkID || done.Pinned.Pin.Lifecycle != "completed" || !reflect.DeepEqual(done.Pinned.Pin.Review.Candidate, before.Pinned.Pin.Review.Candidate) || len(done.Pinned.Pin.VerifiedCriteria) != 1 || gitRun(t, f.path, "rev-parse", "HEAD") != before.Pinned.Pin.Review.Candidate.Commit {
				t.Fatalf("successor completion lost applicable proof or forced another edit: %+v", done.Pinned)
			}
		})
	}
}
