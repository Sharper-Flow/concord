package agent

import (
	"fmt"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

// TestLifecycleCompletionEvidenceRefs proves the response envelope for a
// terminal lifecycle transition carries the evidence the caller supplied.
//
// The agent writes its completion response from the recorded facts and
// evidence (CD-0084 D5). The single response that reports lifecycle
// "completed" must therefore carry that evidence in evidence_refs, not only
// in the transition event payload.
func TestLifecycleCompletionEvidenceRefs(t *testing.T) {
	s, service, grant, _, _ := agentJobsMutationPM1Fixture(t)
	env := agentJobsMutationEnvelope(t, s, grant, "proj-web")

	_, preVersion := readWorkFromStore(t, s, "work-cross")
	idempotencyKey := fmt.Sprintf("evidence-refs-cross-%d", preVersion)
	input := []byte(fmt.Sprintf(`{"work_id":"work-cross","expected_version":%d,"target":"completed","reason":"complete with verification","idempotency_key":"%s","evidence":[{"kind":"verification","authority":"agent-verifier","locator_kind":"test","locator":"verification-pass"}]}`, preVersion, idempotencyKey))

	// CD-0243 D3/D7 retire ordinary terminal consent, not completion evidence.
	approved := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: input}, env)
	if approved.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("approved dispatch failed outcome=%s err=%+v", approved.Outcome, approved.Error)
	}
	if lifecycle, _ := readWorkFromStore(t, s, "work-cross"); lifecycle != "completed" {
		t.Fatalf("post-lifecycle=%q, want completed", lifecycle)
	}
	// The terminal state and its evidence arrive in this one envelope.
	if len(approved.EvidenceRefs) != 1 {
		t.Fatalf("completion envelope evidence_refs=%d, want 1: %+v", len(approved.EvidenceRefs), approved.EvidenceRefs)
	}
	if got := approved.EvidenceRefs[0].Locator; got != "verification-pass" {
		t.Fatalf("evidence_refs[0].locator=%q, want verification-pass", got)
	}
	if got := approved.EvidenceRefs[0].Kind; got != "verification" {
		t.Fatalf("evidence_refs[0].kind=%q, want verification", got)
	}

	// A replayed completion must surface the same evidence, or a retry of the
	// same key reports the terminal state without the evidence behind it.
	replay := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: input}, env)
	if !replay.Replayed {
		t.Fatalf("replay did not set Replayed=true")
	}
	if len(replay.EvidenceRefs) != 1 || replay.EvidenceRefs[0].Locator != "verification-pass" {
		t.Fatalf("replayed completion evidence_refs=%+v, want the original verification evidence", replay.EvidenceRefs)
	}
}
