package agent

import (
	"encoding/json"
	"reflect"
	"strconv"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

func TestCompletedDeliverySameIdentityReplay(t *testing.T) {
	f := newSpineFixtureForFamily(t, "workflow.break_fix")
	f.advanceToAcceptance(t)
	f.env.SessionRef, f.env.AgentRef = "session-reevaluator", "agent-evaluator"
	spineOK(t, f.action(t, "record_verdict", map[string]any{"verdicts": []map[string]any{{"predicate_id": "predicate:primary", "receipt_refs": []string{f.proof}}}}))
	spineOK(t, f.call(t, "concord_work_transition", "workflow_action", map[string]any{
		"work_id": f.work, "expected_version": f.read(t).Pinned.Pin.Version, "action_id": "confirm_premise", "idempotency_key": "completed-replay-confirm",
	}))
	f.completeReview(t)
	spineOK(t, f.call(t, "concord_work_transition", "workflow_action", map[string]any{"work_id": f.work, "expected_version": workVersion(t, f.s, f.work), "action_id": "complete", "fields": map[string]any{"impact_verdict": "non-breaking"}, "idempotency_key": "completed-replay-complete"}))
	if pin := f.read(t).Pinned.Pin; pin.Lifecycle != "completed" {
		t.Fatalf("fixture not completed: %+v", pin)
	}
	version := workVersion(t, f.s, f.work)
	input := json.RawMessage(`{"work_id":"` + f.work + `","expected_version":` + strconv.FormatInt(version, 10) + `,"action_id":"record_delivery","fields":{"delivery_artifact":"https://example.com/corrected","delivery_state":"asserted"},"idempotency_key":"completed-delivery-replay"}`)
	request := InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: input}
	first := dispatchMutation(t, f.s, f.service, request, f.env)
	if first.Outcome != agentcontract.OutcomeOK || first.Error != nil || first.Replayed {
		t.Fatalf("first delivery edit: %+v error=%+v", first, first.Error)
	}
	if got := workVersion(t, f.s, f.work); got <= version {
		t.Fatalf("edit did not advance version: %d -> %d", version, got)
	}
	var artifact, state, lifecycle string
	var planRetained bool
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT json_extract(d.payload,'$.delivery_artifact'),i.instance_state,w.lifecycle,i.workflow_type IS NOT NULL AND i.step_plan IS NOT NULL FROM workflow_delivery_state d JOIN workflow_instances i ON i.work_id=d.work_id JOIN work_items w ON w.id=d.work_id WHERE d.work_id=?`, f.work).Scan(&artifact, &state, &lifecycle, &planRetained); err != nil || artifact != "https://example.com/corrected" || state != "completed" || lifecycle != "completed" || !planRetained {
		t.Fatalf("delivery/progress=%q/%q/%q plan retained=%t err=%v", artifact, state, lifecycle, planRetained, err)
	}
	baseline := agentCurrentEffects(t, f.s)
	replay := dispatchMutation(t, f.s, f.service, request, f.env)
	if replay.Outcome != agentcontract.OutcomeOK || replay.Error != nil || !replay.Replayed || string(first.Result) != string(replay.Result) || !reflect.DeepEqual(first.ChangedRefs, replay.ChangedRefs) {
		t.Fatalf("replay did not return original result: first=%+v replay=%+v", first, replay)
	}
	if got := agentCurrentEffects(t, f.s); got != baseline {
		t.Fatal("replay changed receipt, version, current delivery or progress")
	}
	if countRows(t, f.s.DatabaseForTesting(), `SELECT replayed_count FROM idempotency_records WHERE idempotency_key='completed-delivery-replay'`) != 1 {
		t.Fatal("cached replay was not recorded")
	}
}
