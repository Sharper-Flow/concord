package agent

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

func TestLifecycleCompletionRetainsEvidenceOnReplay(t *testing.T) {
	s, service, grant := agentJobsMutationPM1Fixture(t)
	env := agentJobsMutationEnvelope(t, s, grant)

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
	var evidence string
	if err := s.DatabaseForTesting().QueryRow(`SELECT transition_evidence_refs FROM work_items WHERE id=?`, "work-cross").Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(evidence, "verification-pass") {
		t.Fatalf("stored transition evidence = %q", evidence)
	}
	var scopeJSON string
	if err := s.DatabaseForTesting().QueryRow(`SELECT authorized_scope_snapshot FROM idempotency_records WHERE idempotency_key=?`, idempotencyKey).Scan(&scopeJSON); err != nil {
		t.Fatal(err)
	}
	snapshot, err := retainedMutationResponse(scopeJSON)
	if err != nil || len(snapshot.EvidenceRefs) != 1 || snapshot.EvidenceRefs[0].Authority != "agent-verifier" || snapshot.EvidenceRefs[0].LocatorKind != "test" || snapshot.EvidenceRefs[0].Locator != "verification-pass" {
		t.Fatalf("stored evidence = %+v: %v", snapshot.EvidenceRefs, err)
	}

	replay := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: input}, env)
	if replay.Outcome != agentcontract.OutcomeOK || !reflect.DeepEqual(replay.ChangedRefs, approved.ChangedRefs) {
		t.Fatalf("replay changed result: %+v", replay)
	}
	if _, version := readWorkFromStore(t, s, "work-cross"); version != preVersion+1 {
		t.Fatalf("replay changed work version to %d", version)
	}
}
