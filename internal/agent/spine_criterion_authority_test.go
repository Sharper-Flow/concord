package agent

import (
	"reflect"
	"strings"
	"testing"
)

func TestSpineMainAssociatesCurrentProofAcrossSessions(t *testing.T) {
	f := newSpineFixture(t)
	f.advanceToAcceptance(t)
	main := f.env
	candidate := gitRun(t, f.path, "rev-parse", "HEAD")
	verdict := map[string]any{"verdicts": []map[string]any{{"predicate_id": "predicate:primary", "receipt_refs": []string{f.proof}}}}
	for _, session := range []struct{ name, ref string }{
		{"accepting Main", main.SessionRef},
		{"resumed Main", "session-spine-resumed-main"},
	} {
		t.Run(session.name, func(t *testing.T) {
			f.env.SessionRef = session.ref
			before := f.read(t)
			response := spineOK(t, f.action(t, "record_verdict", verdict))
			after := f.read(t)
			if response.Replayed || after.Pinned.Pin.Version <= before.Pinned.Pin.Version {
				t.Fatal("fresh proof association did not commit")
			}
			if after.Pinned.Step != before.Pinned.Step || !reflect.DeepEqual(after.Pinned.Contract, before.Pinned.Contract) || !reflect.DeepEqual(after.Pinned.Pin.Review, before.Pinned.Pin.Review) || !reflect.DeepEqual(after.Pinned.Pin.VerdictEvidence, before.Pinned.Pin.VerdictEvidence) {
				t.Fatal("proof association changed the step, contract, accepted Review or bound evidence")
			}
			if gitRun(t, f.path, "rev-parse", "HEAD") != candidate {
				t.Fatal("Main proof association required approval or changed the candidate")
			}
		})
	}
	spineOK(t, f.call(t, "concord_work_define", "observation_record", map[string]any{
		"work_id": f.work, "statement": "The current producer receipt supports the approved criterion and unchanged accepted Review.",
		"refs": []string{f.read(t).Pinned.Contract.ArchitectureBinding.HomeDomainID, f.proof}, "idempotency_key": "spine-main-proof-investigation",
	}))
	spineOK(t, f.call(t, "concord_work_transition", "workflow_action", map[string]any{
		"work_id": f.work, "expected_version": f.read(t).Pinned.Pin.Version, "action_id": "confirm_premise",
		"idempotency_key": "spine-main-proof-confirm",
	}))
	spineOK(t, f.action(t, "bind_evidence", map[string]any{"evidence_kind": "review", "evidence_ref": "sha256:" + strings.Repeat("a", 64)}))
	spineOK(t, f.action(t, "complete", map[string]any{"impact_verdict": "non-breaking"}))
	done := f.read(t).Pinned.Pin
	if done.Lifecycle != "completed" || len(done.VerifiedCriteria) != 1 || done.VerifiedCriteria[0].PredicateID != "predicate:primary" || done.VerifiedCriteria[0].VerdictKind != "ok" {
		t.Fatalf("Main completion lost the approved criterion proof: %+v", done)
	}
}
