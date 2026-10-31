package agent

import (
	"slices"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// Outside-repair hold/resume changes administrative disposition only. It
// preserves the candidate, contract, and step and needs no forge simulation.
func (f *spineFixture) administrativeRecovery(t *testing.T) {
	t.Helper()
	for _, operation := range []string{"outside_repair", "outside_repair_reconcile"} {
		input := map[string]any{"work_id": f.work, "expected_version": f.read(t).Pinned.Pin.Version,
			"reason": "Return the unchanged checked candidate to managed work.", "idempotency_key": "spine-" + operation}
		if operation == "outside_repair_reconcile" {
			input["mode"] = "resume"
		}
		// CD-0243 D3/D7 retire ordinary hold/resume consent, not recovery gates.
		spineOK(t, f.call(t, "concord_work_transition", operation, input))
	}
}

func TestSpineAdministrativeRecoveryPreservesValidProof(t *testing.T) {
	f := newSpineFixture(t)
	f.advanceToAcceptance(t)
	before := f.read(t)
	if before.Pinned.Step != "acceptance" {
		t.Fatalf("proof consumer step = %q, want acceptance", before.Pinned.Step)
	}
	bound := store.WorkPinEvidence{EvidenceKind: "verification", ImmutableSubjectRef: f.proof}
	if !slices.Contains(before.Pinned.Pin.VerdictEvidence, bound) {
		t.Fatal("verification proof is not bound")
	}
	candidate := gitRun(t, f.path, "rev-parse", "HEAD")
	f.administrativeRecovery(t)
	after := f.read(t)
	if gitRun(t, f.path, "rev-parse", "HEAD") != candidate || after.Pinned.Step != before.Pinned.Step || after.Pinned.Contract.Version != before.Pinned.Contract.Version {
		t.Fatal("administrative recovery changed a proof dependency")
	}
	if !slices.Contains(after.Pinned.Pin.VerdictEvidence, bound) {
		t.Fatal("administrative recovery removed bound verification")
	}
	// Evaluation belongs to a fresh session, not to the session that executed
	// the implementation. No verification producer runs after recovery.
	f.env.SessionRef = "session-evaluator"
	f.env.AgentRef = "agent-evaluator"
	verdict := map[string]any{"verdicts": []map[string]any{{"predicate_id": "predicate:primary", "receipt_refs": []string{f.proof}}}}
	accepted := f.action(t, "record_verdict", verdict)
	if accepted.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("unchanged candidate lost valid proof after administrative recovery: %+v", accepted.Error)
	}
	if f.read(t).Pinned.Step != "acceptance" {
		t.Fatal("verdict unexpectedly left the proof-consuming gate")
	}
}
