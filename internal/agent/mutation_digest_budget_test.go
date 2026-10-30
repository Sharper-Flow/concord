package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestMutationDigestWireProtocol(t *testing.T) {
	for _, test := range []struct {
		name, raw, canonical string
	}{
		{"plain", `{"from_work_id":"from","to_work_id":"to","reason":"same"}`, `{"Tool":"concord_work_relate","Operation":"resolve_overlap","Product":"product-1","Project":"project-1","Input":{"from_work_id":"from","reason":"same","to_work_id":"to"}}`},
		{"idempotency handles do not change intent", `{"reason":"same","idempotency_key":"key","to_work_id":"to","from_work_id":"from"}`, `{"Tool":"concord_work_relate","Operation":"resolve_overlap","Product":"product-1","Project":"project-1","Input":{"from_work_id":"from","reason":"same","to_work_id":"to"}}`},
		{"explicit budget remains intent", `{"from_work_id":"from","to_work_id":"to","reason":"same","requested_budget_seconds":5}`, `{"Tool":"concord_work_relate","Operation":"resolve_overlap","Product":"product-1","Project":"project-1","Input":{"from_work_id":"from","reason":"same","requested_budget_seconds":5,"to_work_id":"to"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := CallEnvelope{SelectedProductID: "product-1", AmbientProjectID: "project-1"}
			want := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(test.canonical)))
			if got := mutationDigest("concord_work_relate", "resolve_overlap", env, []byte(test.raw)); got != want {
				t.Fatalf("canonical mutation digest=%s, want %s for %s", got, want, test.canonical)
			}
		})
	}
}

func TestRoutineCoordinationExplicitBudgetResolvesThroughRealDispatch(t *testing.T) {
	t.Parallel()
	s, service, env := routineCoordinationFixture(t)
	binding := routineOverlapExactBinding(t, s, store.ResolutionMergedInto)
	input := routineOverlapResolveInput(t, binding, "overlap-budget-dispatch")
	budgeted := map[string]any{}
	if err := json.Unmarshal(input, &budgeted); err != nil {
		t.Fatal(err)
	}
	budgeted["requested_budget_seconds"] = 5
	budgetedInput, err := json.Marshal(budgeted)
	if err != nil {
		t.Fatal(err)
	}
	response := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: budgetedInput}, env)
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("the budgeted destructive resolution outcome=%s error=%+v", response.Outcome, response.Error)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions`); got != 1 {
		t.Fatalf("the budgeted resolution left %d resolution rows, want exactly one", got)
	}
}
