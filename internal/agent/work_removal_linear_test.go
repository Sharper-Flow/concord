package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestDispatchWorkRemovalCommitsWithRecordedLinearIssue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		_, err := store.RecordLinearIssueLinkTx(ctx, tx, store.LinearIssueLink{WorkID: "work-1", RemoteIssueUUID: "remote-removal-1", HumanKey: "CON-1", URL: "https://linear.app/example/issue/CON-1"})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	handoff := store.WorkRemovalHandoff{
		Findings: []string{"finding"}, RemainingScope: []string{"scope"}, Blockers: []string{"blocker"},
		Artifacts: []string{"artifact"}, RenewalConditions: []string{"new approval"},
	}
	handoffJSON, err := json.Marshal(handoff)
	if err != nil {
		t.Fatal(err)
	}
	handoffHash := sha256.Sum256(handoffJSON)
	linearDigest := "sha256:" + hex.EncodeToString(handoffHash[:])
	input := map[string]any{
		"operation_id": "remove-linear-op", "idempotency_key": "remove-linear-key", "work_id": "work-1",
		"expected_version": 2, "reason": "shelved", "actor": "human-1", "product_id": "product-1",
		"handoff": handoff, "linear": map[string]string{
			"product_id": "product-1", "remote_issue_uuid": "remote-removal-1", "destination": "linear", "handoff_digest": linearDigest,
		},
		"execution_relinquished": true, "writes_reconciled": true, "effects_reconciled": true,
		"dependencies_resolved": true, "artifacts_verified": true,
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	request := InvokeRequest{
		Tool: "concord_work_transition", Operation: "remove", Input: raw,
	}
	env := mutationEnvelope(grant, scopeVersion)
	// CD-0243 D3/D7 retire ordinary removal consent, not the Linear handoff.
	response, err := Dispatch(ctx, s, service, request, env)
	if err != nil || response.Outcome != OutcomeOK {
		if response.Error != nil {
			t.Fatalf("approved work removal outcome=%s kind=%s message=%s details=%v err=%v", response.Outcome, response.Error.Kind, response.Error.Message, response.Error.Details, err)
		}
		t.Fatalf("approved work removal response=%+v err=%v", response, err)
	}

	var payload string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT json_object('linear',json(linear_confirmation_json)) FROM work_removal_operations WHERE event_id=? AND state='committed'`, "work.removed:remove-linear-op").Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var eventPayload struct {
		Linear *store.LinearHandoffConfirmation `json:"linear"`
	}
	if err := json.Unmarshal([]byte(payload), &eventPayload); err != nil {
		t.Fatal(err)
	}
	if eventPayload.Linear == nil || eventPayload.Linear.RemoteIssueUUID != "remote-removal-1" {
		t.Fatalf("work.removed payload linear confirmation = %+v", eventPayload.Linear)
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_items WHERE id='work-1'`) != 0 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_instances WHERE work_id='work-1'`) != 0 {
		t.Fatal("removal must delete the live item and its workflow instance")
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_removal_operations WHERE operation_id='remove-linear-op' AND state='committed' AND event_id='work.removed:remove-linear-op'`) != 1 {
		t.Fatal("removal must retain its committed receipt")
	}
	var result struct {
		Pins []store.WorkPin `json:"work_pins"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil || len(result.Pins) != 0 {
		t.Fatalf("removed work has no post-state pin: pins=%+v error=%v", result.Pins, err)
	}
	if len(response.Omissions) != 1 || response.Omissions[0].Kind != "work_pin_unavailable" || response.Omissions[0].Count != 1 || response.Omissions[0].SourceID != "" || len(response.Omissions[0].Details) != 0 {
		t.Fatalf("own removal must explicitly report its absent pin: %+v", response.Omissions)
	}
	changed := *response.ChangedRefs
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		_, _, _, err := (runtime{}).enrichMutationPayloadTx(ctx, tx, mutationPayload(changed, nil), changed)
		return err
	}); err == nil {
		t.Fatal("a later undeclared missing work item must not inherit the old removal's exception")
	}
	replay, err := Dispatch(ctx, s, service, request, env)
	if err != nil || replay.Outcome != OutcomeOK || !replay.Replayed {
		t.Fatalf("own removal receipt must support replay: response=%+v error=%v", replay.Error, err)
	}
}
