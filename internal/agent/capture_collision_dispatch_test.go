package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestCaptureCollisionDispatchRollsBack(t *testing.T) {
	for _, tc := range []struct{ name, key, uuid string }{
		{"key", "ZZ-9001", "other-uuid"},
		{"uuid", "ZZ-9002", "same-uuid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, service, grant, scope := captureTaskFixture(t)
			first := `{"title":"Holder","kind":"task","project_ids":["project-1"],"human_key":"ZZ-9001","remote_issue_uuid":"same-uuid","url":"https://example.com/issue/ZZ-9001","idempotency_key":"holder"}`
			response := dispatchCapture(t, s, service, grant, scope, "holder-request", first)
			if response.Outcome != agentcontract.OutcomeOK {
				t.Fatalf("holder: %+v", response)
			}
			holderID := (*response.ChangedRefs)[0].ID
			before := captureStateSnapshot(t, s)
			input := fmt.Sprintf(`{"title":"Collision","kind":"task","project_ids":["project-1"],"human_key":%q,"remote_issue_uuid":%q,"url":"https://example.com/issue/ZZ-9002","idempotency_key":"collision"}`, tc.key, tc.uuid)
			request := InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: json.RawMessage(input)}
			env := mutationEnvelope(grant, scope)
			env.RequestID = "collision-request"
			refused, err := Dispatch(context.Background(), s, service, request, env)
			if err != nil || refused.Error == nil || refused.Outcome != agentcontract.OutcomeError {
				t.Fatalf("collision: %+v, %v", refused, err)
			}
			if refused.Error.EffectState != "none" || !strings.Contains(refused.Error.Message, holderID) {
				t.Fatalf("collision must have no effect and must identify the same-product holder: %+v", refused.Error)
			}
			after := captureStateSnapshot(t, s)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("collision changed persisted state: before=%v after=%v", before, after)
			}
		})
	}
}

func captureStateSnapshot(t *testing.T, s *store.Store) []string {
	t.Helper()
	queries := []string{
		`SELECT COALESCE((SELECT last FROM store_sequences WHERE name='effect'),0)`,
		`SELECT COALESCE(json_group_array(json_array(id,version,lifecycle,title)),'[]') FROM (SELECT * FROM work_items ORDER BY id)`,
		`SELECT COALESCE(json_group_array(json_array(work_id,remote_issue_uuid,human_key,url)),'[]') FROM (SELECT * FROM linear_issue_links ORDER BY work_id)`,
		`SELECT count(*) FROM effect_receipts`,
		`SELECT count(*) FROM idempotency_records`,
		`SELECT count(*) FROM workflow_instances`,
	}
	values := make([]string, len(queries))
	for i, q := range queries {
		if err := s.DatabaseForTesting().QueryRow(q).Scan(&values[i]); err != nil {
			t.Fatal(err)
		}
	}
	return values
}
