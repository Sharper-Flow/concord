package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestDispatchWorkRemovalCommitsWithLinearConfirmation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition"})
	if _, err := s.SetProductPlanningMode(ctx, "product-1", store.PlanningModeLinear, "pilot", "human-1", 2); err != nil {
		t.Fatal(err)
	}
	seedAdoptionConnectionResource(t, s)
	if err := s.RecordLinearLink(ctx, "work-1", "remote-removal-1", "CON-1", "https://linear.app/example/issue/CON-1", "", "", store.LinearLinkPending); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordLinearLink(ctx, "work-1", "remote-removal-1", "CON-1", "https://linear.app/example/issue/CON-1", "", "", store.LinearLinkConfirmed); err != nil {
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
	challenge, err := Dispatch(ctx, s, service, request, env)
	if err != nil || challenge.Outcome != OutcomeError || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("work removal challenge=%+v err=%v", challenge, err)
	}
	approvalRef, ok := challenge.Error.Details["approval_ref"].(string)
	if !ok || len(approvalRef) != 64 {
		t.Fatalf("approval_ref=%v", challenge.Error.Details["approval_ref"])
	}
	digest := mutationDigest(request.Tool, request.Operation, env, request.Input)
	scope := map[string]any{"product_id": "product-1", "product_ids": []string{"product-1"}, "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}
	versions := map[string]any{"work": int64(2)}
	env.HostApproval = signedHostApproval(privateKey, approvalRef, digest, scope, versions, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), nonceForChallenge(approvalRef))
	input["approval"] = map[string]string{"approval_ref": approvalRef}
	request.Input, err = json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	response, err := Dispatch(ctx, s, service, request, env)
	if err != nil || response.Outcome != OutcomeOK {
		if response.Error != nil {
			t.Fatalf("approved work removal outcome=%s kind=%s message=%s details=%v err=%v", response.Outcome, response.Error.Kind, response.Error.Message, response.Error.Details, err)
		}
		t.Fatalf("approved work removal response=%+v err=%v", response, err)
	}

	var payload string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT payload FROM domain_events WHERE event_id=?`, "work.removed:remove-linear-op").Scan(&payload); err != nil {
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
}
