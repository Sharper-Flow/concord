package agent

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestCriterion38RecoveryReevaluationJourney(t *testing.T) {
	f := newSpineFixtureForFamily(t, "workflow.break_fix")
	f.advanceToAcceptance(t)
	before := f.read(t)
	candidate := *before.Pinned.Pin.Review.Candidate
	proof := store.WorkPinEvidence{EvidenceKind: "verification", ImmutableSubjectRef: f.proof}
	challenges := countWorkflowApprovalChallenges(t, f.s)

	f.administrativeRecovery(t)
	after := f.read(t)
	if !reflect.DeepEqual(before.Pinned.Contract, after.Pinned.Contract) || *after.Pinned.Pin.Review.Candidate != candidate || !slices.Contains(after.Pinned.Pin.VerdictEvidence, proof) {
		t.Fatal("recovery-only correction invalidated the candidate, criteria or applicable proof")
	}
	f.env.SessionRef, f.env.AgentRef = "session-reevaluator", "agent-evaluator"
	verdict := map[string]any{"verdicts": []map[string]any{{"predicate_id": "predicate:primary", "receipt_refs": []string{f.proof}}}}
	spineOK(t, f.action(t, "record_verdict", verdict))
	spineOK(t, f.call(t, "concord_work_define", "observation_record", map[string]any{
		"work_id": f.work, "statement": "The approved value criterion has checked production and accepted Review.",
		"refs": []string{before.Pinned.Contract.ArchitectureBinding.HomeDomainID, f.proof}, "idempotency_key": "journey-initial-investigation",
	}))
	spineOK(t, f.call(t, "concord_work_transition", "workflow_action", map[string]any{
		"work_id": f.work, "expected_version": f.read(t).Pinned.Pin.Version, "action_id": "confirm_premise",
		"selected_choice": "confirm", "idempotency_key": "journey-initial-confirm",
	}))

	// Reopen evaluation of the existing candidate, not its production. The
	// observation supplies the existing close-step correction route's premise.
	spineOK(t, f.call(t, "concord_work_define", "observation_record", map[string]any{
		"work_id": f.work, "statement": "Recheck whether the existing candidate satisfies its approved value criterion.",
		"refs": []string{before.Pinned.Contract.ArchitectureBinding.HomeDomainID, f.proof}, "idempotency_key": "journey-reevaluation",
	}))
	contract := f.read(t).Pinned.Contract
	predicates := make([]map[string]any, 0, len(contract.OutcomePredicates))
	for _, predicate := range contract.OutcomePredicates {
		predicates = append(predicates, map[string]any{"predicate_id": predicate.PredicateID, "ordinal": predicate.Ordinal,
			"outcome_kind": predicate.OutcomeKind, "outcome_payload": json.RawMessage(predicate.OutcomePayload)})
	}
	successor := map[string]any{
		"contract_version": contract.Version + 1, "predecessor_contract_versions": []int64{contract.Version},
		"premise": contract.Premise, "outcome_predicates": predicates, "required_evidence": contract.RequiredEvidence,
		"route_conventions": []string{"complete_step_correction"}, "spec_mandate": []string{}, "law_modifies": []string{},
		"architecture_binding": contract.ArchitectureBinding, "rigor_class": "prototype_internal", "decision_domains": []string{},
		"supersede_reason": "Re-evaluate the existing candidate without changing its requirement.", "audit_evidence": []string{f.proof},
	}
	spineOK(t, f.action(t, "supersede_contract", successor))
	spineOK(t, f.action(t, "checkpoint_repair", map[string]any{}))
	for _, binding := range []store.WorkPinEvidence{proof,
		{EvidenceKind: "review", ImmutableSubjectRef: f.read(t).Pinned.Pin.Review.Accepted.ReportRef},
		{EvidenceKind: "artifact", ImmutableSubjectRef: candidate.Commit},
	} {
		spineOK(t, f.action(t, "bind_evidence", map[string]any{"evidence_kind": binding.EvidenceKind, "evidence_ref": binding.ImmutableSubjectRef}))
	}
	if review := f.read(t).Pinned.Pin.Review; *review.Candidate != candidate || !review.DependenciesCurrent {
		t.Fatal("evaluation-admission correction lost production or its applicable Review")
	}

	// The approved check now names a different required subject. Its previous
	// Review cannot establish the changed obligation, even on identical code.
	contract = f.read(t).Pinned.Contract
	var changed map[string]any
	if err := json.Unmarshal(predicates[0]["outcome_payload"].(json.RawMessage), &changed); err != nil {
		t.Fatal(err)
	}
	changed["immutable_subject_ref"] = "commit:reevaluated-value"
	predicates[0]["outcome_payload"] = changed
	successor["contract_version"] = contract.Version + 1
	successor["predecessor_contract_versions"] = []int64{contract.Version}
	successor["route_conventions"] = []string{}
	successor["supersede_reason"] = "Require attribution to the reassessed value subject."
	spineOK(t, f.action(t, "supersede_contract", successor))
	delivery := map[string]any{"delivery_artifact": "artifact:spine-value", "delivery_state": "asserted"}
	version := f.read(t).Pinned.Pin.Version
	refused := f.action(t, "record_delivery", delivery)
	if refused.Error == nil || refused.Error.Kind != "missing_evidence" || refused.Error.EffectState != agentcontract.EffectNone || f.read(t).Pinned.Pin.Version != version {
		t.Fatalf("changed obligation used the old Review or committed an effect: %+v", refused.Error)
	}
	f.prepareControl(t)
	f.review(t, "correction_required", "")
	report := f.read(t).Pinned.Pin.Review.Accepted.ReportRef
	spineOK(t, f.action(t, "bind_evidence", map[string]any{"evidence_kind": "review", "evidence_ref": report}))
	if unmet := f.action(t, "record_delivery", delivery); unmet.Error == nil || unmet.Error.Kind != "missing_evidence" || !strings.Contains(unmet.Error.Message, "correction_required") {
		t.Fatalf("an unmet changed obligation left execution without correction: %+v", unmet.Error)
	}
	spineOK(t, f.action(t, "request_correction", map[string]any{
		"diagnosis":     "The changed subject attribution needs assessment against the checked candidate.",
		"strategy":      "Review the retained candidate and its existing check receipt against the changed criterion.",
		"predicate_ids": []string{"predicate:primary"}, "evidence_refs": []string{report},
	}))
	if correction := f.read(t).Pinned.Pin.Correction; correction == nil || !slices.Contains(correction.EvidenceRefs, report) {
		t.Fatal("the unmet changed obligation lost its correction authority")
	}
	f.prepareControl(t)
	f.review(t, "ready_for_final_proof", f.proof)
	spineOK(t, f.action(t, "bind_evidence", map[string]any{"evidence_kind": "verification", "evidence_ref": f.proof}))
	f.advanceToAcceptance(t)
	spineOK(t, f.action(t, "record_verdict", verdict))
	spineOK(t, f.call(t, "concord_work_transition", "workflow_action", map[string]any{
		"work_id": f.work, "expected_version": f.read(t).Pinned.Pin.Version, "action_id": "confirm_premise",
		"selected_choice": "confirm", "idempotency_key": "journey-corrected-confirm",
	}))
	spineOK(t, f.action(t, "bind_evidence", map[string]any{"evidence_kind": "review", "evidence_ref": "sha256:" + strings.Repeat("a", 64)}))
	spineOK(t, f.action(t, "complete", map[string]any{"impact_verdict": "non-breaking"}))
	done := f.read(t)
	if done.Pinned.Pin.Lifecycle != "completed" || *done.Pinned.Pin.Review.Candidate != candidate || len(done.Pinned.Pin.VerifiedCriteria) != 1 ||
		countWorkflowApprovalChallenges(t, f.s) != challenges || f.proofCommit != candidate.Commit {
		t.Fatal("re-evaluation required no-op production, discarded proof, or introduced unrelated approval")
	}
}
