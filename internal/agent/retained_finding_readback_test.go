package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

func TestRetainedFindingReadRoutes(t *testing.T) {
	fx := seedReadPopulationFixture(t)
	const retained = `{"candidate":null,"accepted_review":null,"findings":[{"finding_id":"","severity":"P2","confidence":"high","detail":"The recorded proof is incomplete.","in_scope":false,"blocking":false,"status":"","evidence_refs":null,"closure_refs":null}]}`
	db := fx.store.DatabaseForTesting()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(t.Context(), `INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), `INSERT INTO workflow_review_state(work_id,state) VALUES(?,?)`, fx.workID, retained); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), `DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	inputs := readPopulationInputs(fx)
	inputs["concord_work_browse.list"] = `{"product_id":"prod-alpha","work_ids":["` + fx.workID + `"],"detail":"full","limit":10}`
	for _, call := range []struct{ tool, operation string }{{"concord_work_browse", "list"}, {"concord_work_trace", "continuity"}} {
		t.Run(call.operation, func(t *testing.T) {
			input := inputs[call.tool+"."+call.operation]
			response := dispatchRead(t, fx.store, fx.service, InvokeRequest{Tool: call.tool, Operation: call.operation, Input: json.RawMessage(input)}, fx.envelope(t))
			if response.Outcome != agentcontract.OutcomeOK {
				t.Fatalf("read refused retained finding: %+v", response.Error)
			}
			if err := agentcontract.ValidateOperationPayload(call.tool, call.operation, response.Result, true); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(response.Result), `"detail":"The recorded proof is incomplete."`) {
				t.Fatalf("read dropped retained finding: %s", response.Result)
			}
		})
	}
	var after string
	if err := db.QueryRowContext(t.Context(), `SELECT state FROM workflow_review_state WHERE work_id=?`, fx.workID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != retained {
		t.Fatalf("read routes rewrote retained state:\n%s\n%s", retained, after)
	}
}
