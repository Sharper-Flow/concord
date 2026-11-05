package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

func TestPublicBugCaptureAndKindRevision(t *testing.T) {
	t.Parallel()
	s, service, grant, scopeVersion := captureTaskFixture(t)
	env := mutationEnvelope(grant, scopeVersion)
	var firstID string
	for i := range 2 {
		env.RequestID = fmt.Sprintf("bug-capture-%d", i)
		response := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: retryJSON(map[string]any{
			"title": fmt.Sprintf("Checksum mismatch %d", i), "kind": "bug", "project_ids": []string{"project-1"}, "human_key": "ZZ-8100", "remote_issue_uuid": fmt.Sprintf("fixture-bug-capture-%d", i), "url": "https://linear.app/example/issue/ZZ-8100", "idempotency_key": env.RequestID,
		})}, env)
		if response.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("bug capture %d: %+v", i, response.Error)
		}
		if i == 0 {
			firstID = (*response.ChangedRefs)[0].ID
		}
	}
	for i, kind := range []string{"task", "bug", "research"} {
		env.RequestID = fmt.Sprintf("bug-revise-%d", i)
		response := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "revise_intent", Input: retryJSON(map[string]any{
			"work_id": firstID, "expected_version": workVersion(t, s, firstID), "title": "Checksum mismatch", "kind": kind, "reason": "Correct the work intent", "idempotency_key": env.RequestID,
		})}, env)
		if response.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("revision to %s: %+v", kind, response.Error)
		}
		var stored string
		if err := s.DatabaseForTesting().QueryRow(`SELECT kind FROM work_items WHERE id=?`, firstID).Scan(&stored); err != nil || stored != kind {
			t.Fatalf("stored kind=%s, want %s: %v", stored, kind, err)
		}
	}
	for _, input := range []map[string]any{
		{"product_id": "product-1", "detail": "full"},
		{"product_id": "product-1", "detail": "full", "work_ids": []string{firstID}},
	} {
		response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_browse", Operation: "list", Input: retryJSON(input)}, env)
		if err != nil || response.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("list: %+v %v", response.Error, err)
		}
		if strings.Contains(string(response.Result), "defect_intake") {
			t.Fatalf("list exposes retired classification: %s", response.Result)
		}
		if !json.Valid(response.Result) {
			t.Fatal("invalid list JSON")
		}
	}
}

func TestPublicCaptureRejectsRetiredIntake(t *testing.T) {
	t.Parallel()
	s, service, grant, scopeVersion := captureTaskFixture(t)
	for _, kind := range []string{"bug", "research", "task"} {
		env := mutationEnvelope(grant, scopeVersion)
		env.RequestID = "retired-intake-" + kind
		response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: retryJSON(map[string]any{
			"title": "Checksum mismatch", "kind": kind, "project_ids": []string{"project-1"}, "human_key": "ZZ-8101", "remote_issue_uuid": "fixture-bug-public", "url": "https://linear.app/example/issue/ZZ-8101", "idempotency_key": env.RequestID,
			"defect_intake": map[string]any{"failure_shape": "checksum-mismatch", "reproduction": "Compare checksums", "searched": "Fixture", "related_defect_ids": []string{}},
		})}, env)
		if err == nil && (response.Outcome != agentcontract.OutcomeError || response.Error == nil || response.Error.EffectState != "none") {
			t.Fatalf("%s accepted retired input: %+v", kind, response)
		}
	}
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM work_items`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("retired input left work: %d %v", count, err)
	}
}
