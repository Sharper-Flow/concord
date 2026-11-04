package store

import (
	"encoding/json"
	"testing"
)

func TestWorkflowVerdictRequiresNonemptyBatch(t *testing.T) {
	f, _, refs := pReceiptFixture(t, "verdict-payload-current", 1, 1)
	for name, payload := range map[string]json.RawMessage{
		"scalar":         mustJSONValue(map[string]any{"contract_version": 1, "predicate_id": "predicate:p0", "receipt_refs": refs[0]}),
		"empty":          json.RawMessage(`{"contract_version":1,"verdicts":[]}`),
		"absent":         json.RawMessage(`{"contract_version":1}`),
		"empty current":  json.RawMessage(`{"verdicts":[]}`),
		"absent current": json.RawMessage(`{}`),
	} {
		t.Run(name, func(t *testing.T) {
			assertRecoveryRefusalWithoutEffect(t, f.store, f.workID, KindInvalidPayload, func() error {
				return runVerdictActionAs(t, f.store, f.workID, "record_verdict", payload, 0, f.owner)
			})
		})
	}
	if err := runVerdictActionAs(t, f.store, f.workID, "record_verdict", pReceiptBatch(refs, 0), 0, f.owner); err != nil {
		t.Fatal(err)
	}
	verdicts, err := latestWorkflowVerdicts(t.Context(), f.store.db, f.workID, 1)
	if err != nil || len(verdicts) != 1 {
		t.Fatalf("verdicts=%+v err=%v", verdicts, err)
	}
	verdict := verdicts[0]
	if verdict.PredicateID != "predicate:p0" || verdict.ContractVersion != 1 || len(verdict.EvaluationEvidence) != 1 || verdict.EvaluationEvidence[0] != refs[0][0] || verdict.VerdictActorRef != mustActorRef(t, f.owner) {
		t.Fatalf("recorded verdict=%+v", verdict)
	}
}
