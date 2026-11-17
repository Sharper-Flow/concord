package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestCancellationWithoutEvidenceAllowsFreshCapture(t *testing.T) {
	for _, main := range []bool{true, false} {
		t.Run(fmt.Sprintf("main=%t", main), func(t *testing.T) {
			ctx := context.Background()
			s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define", "work_transition"})
			resolveProjectAuthority(service, store.ProjectResolution{ProjectID: "project-1", MainWorktree: main})
			scope, _, err := s.ScopeVersion(ctx, "project-1")
			if err != nil {
				t.Fatal(err)
			}
			capture := func(issueKey, issueUUID, key string) agentcontract.Envelope {
				return dispatchCapture(t, s, service, grant, scope, key, fmt.Sprintf(`{"title":"Correct a wrong issue link","task":"Recapture against the correct issue.","kind":"task","project_ids":["project-1"],"human_key":%q,"remote_issue_uuid":%q,"url":"https://example.com/issue/%s","idempotency_key":%q}`, issueKey, issueUUID, issueKey, key))
			}
			linkedIssue := func(workID string) string {
				var humanKey string
				if err := s.DatabaseForTesting().QueryRow(`SELECT human_key FROM linear_issue_links WHERE work_id=?`, workID).Scan(&humanKey); err != nil {
					t.Fatal(err)
				}
				return humanKey
			}
			original := capture("ZZ-9010", "wrong-issue-uuid", "wrong-link-capture")
			if original.Outcome != agentcontract.OutcomeOK || original.ChangedRefs == nil {
				t.Fatalf("capture: %+v", original.Error)
			}
			id := (*original.ChangedRefs)[0].ID
			env := mutationEnvelope(grant, scope)
			transition := func(version int, reason, key string) agentcontract.Envelope {
				raw, err := json.Marshal(map[string]any{"work_id": id, "expected_version": version, "target": "cancelled", "reason": reason, "idempotency_key": key})
				if err != nil {
					t.Fatal(err)
				}
				env.RequestID = key
				out, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: raw}, env)
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			stale := transition(2, "incorrect issue link", "stale-cancel")
			if stale.Error == nil || stale.Error.Kind != "version_conflict" || stale.Error.EffectState != "none" {
				t.Fatalf("stale: %+v", stale)
			}
			missingReason := transition(3, "", "no-reason-cancel")
			if missingReason.Error == nil || missingReason.Error.EffectState != "none" {
				t.Fatalf("missing reason: %+v", missingReason)
			}
			cancelled := transition(3, "incorrect issue link", "cancel-original")
			if cancelled.Outcome != agentcontract.OutcomeOK {
				t.Fatalf("cancel: %+v", cancelled.Error)
			}
			var lifecycle, state, reason, evidence string
			var version int
			if err := s.DatabaseForTesting().QueryRow(`SELECT w.lifecycle,i.instance_state,w.transition_reason,w.transition_evidence_refs,w.version FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, id).Scan(&lifecycle, &state, &reason, &evidence, &version); err != nil {
				t.Fatal(err)
			}
			if lifecycle != "cancelled" || state != "cancelled" || reason != "incorrect issue link" || evidence != "[]" || version != 4 {
				t.Fatalf("cancellation: %s %s %q %s v%d", lifecycle, state, reason, evidence, version)
			}
			corrected := capture("ZZ-9011", "correct-issue-uuid", "corrected-link-capture")
			if corrected.Outcome != agentcontract.OutcomeOK || corrected.ChangedRefs == nil {
				t.Fatalf("corrected capture: %+v", corrected.Error)
			}
			correctedID := (*corrected.ChangedRefs)[0].ID
			if correctedID == id || linkedIssue(id) != "ZZ-9010" || linkedIssue(correctedID) != "ZZ-9011" {
				t.Fatalf("corrected capture %s links %s; cancelled %s links %s", correctedID, linkedIssue(correctedID), id, linkedIssue(id))
			}
		})
	}
}
