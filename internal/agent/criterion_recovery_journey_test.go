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

	// Reassess the existing candidate without resetting its production step.
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
		"contract_version": contract.Version + 1,
		"premise":          contract.Premise, "outcome_predicates": predicates, "required_evidence": contract.RequiredEvidence,
		"route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{},
		"architecture_binding": contract.ArchitectureBinding, "rigor_class": "prototype_internal", "decision_domains": []string{},
	}
	beforeRevision := f.read(t)
	updated, _ := dispatchContractRevisionResult(t, f.s, f.service, f.env, f.work, beforeRevision.Pinned.Pin.Version, successor, "journey-revise-unchanged")
	spineOK(t, updated)
	if after := f.read(t); after.Pinned.Step != beforeRevision.Pinned.Step || !slices.Contains(after.Pinned.Pin.VerdictEvidence, proof) {
		t.Fatal("ordinary revision reset the step or discarded valid proof")
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
	successor["route_conventions"] = []string{}
	updated, _ = dispatchContractRevisionResult(t, f.s, f.service, f.env, f.work, f.read(t).Pinned.Pin.Version, successor, "journey-revise-subject")
	spineOK(t, updated)
	version := f.read(t).Pinned.Pin.Version
	refused := f.action(t, "confirm_premise", map[string]any{})
	if refused.Error == nil || refused.Error.EffectState != agentcontract.EffectNone || f.read(t).Pinned.Pin.Version != version || f.read(t).Pinned.Pin.Review.DependenciesCurrent {
		t.Fatalf("changed obligation used the old Review or committed an effect: %+v", refused.Error)
	}
	f.review(t, "correction_required", "")
	report := f.read(t).Pinned.Pin.Review.Accepted.ReportRef
	spineOK(t, f.action(t, "bind_evidence", map[string]any{"evidence_kind": "review", "evidence_ref": report}))
	if unmet := f.action(t, "confirm_premise", map[string]any{}); unmet.Error == nil || unmet.Error.EffectState != agentcontract.EffectNone {
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
	f.review(t, "ready_for_final_proof", f.proof)
	spineOK(t, f.action(t, "bind_evidence", map[string]any{"evidence_kind": "verification", "evidence_ref": f.proof}))
	f.advanceToAcceptance(t)
	spineOK(t, f.action(t, "record_verdict", verdict))
	spineOK(t, f.call(t, "concord_work_transition", "workflow_action", map[string]any{
		"work_id": f.work, "expected_version": f.read(t).Pinned.Pin.Version, "action_id": "confirm_premise",
		"idempotency_key": "journey-corrected-confirm",
	}))
	spineOK(t, f.action(t, "bind_evidence", map[string]any{"evidence_kind": "review", "evidence_ref": "sha256:" + strings.Repeat("a", 64)}))
	spineOK(t, f.action(t, "complete", map[string]any{"impact_verdict": "non-breaking"}))
	done := f.read(t)
	if done.Pinned.Pin.Lifecycle != "completed" || *done.Pinned.Pin.Review.Candidate != candidate || len(done.Pinned.Pin.VerifiedCriteria) != 1 ||
		f.proofCommit != candidate.Commit {
		t.Fatal("re-evaluation required no-op production or discarded proof")
	}
}
