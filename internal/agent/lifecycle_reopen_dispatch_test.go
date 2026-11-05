package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestReopenRestoresPlanAndAdmitsInsertStep(t *testing.T) {
	for _, terminal := range []string{"cancelled", "completed"} {
		t.Run(terminal, func(t *testing.T) {
			ctx := context.Background()
			s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define", "work_transition"})
			scope, _, err := s.ScopeVersion(ctx, "project-1")
			if err != nil {
				t.Fatal(err)
			}
			env := mutationEnvelope(grant, scope)
			id := captureCompositionWork(t, ctx, s, service, env, "Reopen plan", "task", "", "reopen-plan")
			compositionAction(t, ctx, s, service, env, id, "record_intake", map[string]any{"outcome": "proceed", "open_questions_found": false})
			compositionAction(t, ctx, s, service, env, id, "pick_type", map[string]any{"type": "change", "reason": "fixture"})
			var planBefore string
			if err := s.DatabaseForTesting().QueryRow(`SELECT step_plan FROM workflow_instances WHERE work_id=?`, id).Scan(&planBefore); err != nil {
				t.Fatal(err)
			}
			transition := func(target, key string) agentcontract.Envelope {
				t.Helper()
				raw, err := json.Marshal(map[string]any{"work_id": id, "expected_version": agentCompositionWorkVersion(t, s, id), "target": target, "reason": "test", "evidence": []map[string]string{{"kind": "verification", "authority": "test", "locator_kind": "file", "locator": "README.md"}}, "idempotency_key": key})
				if err != nil {
					t.Fatal(err)
				}
				env.RequestID = key
				out, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: raw}, env)
				if err != nil || out.Outcome != agentcontract.OutcomeOK {
					t.Fatalf("%s: %+v, %v", target, out.Error, err)
				}
				return out
			}
			transition("cancelled", "reopen-plan-cancel")
			if terminal == "completed" {
				tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if _, err = tx.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
					t.Fatal(err)
				}
				if _, err = tx.Exec(`UPDATE work_items SET lifecycle='completed' WHERE id=?`, id); err != nil {
					t.Fatal(err)
				}
				if _, err = tx.Exec(`UPDATE workflow_instances SET instance_state='completed',completion_payload='{}',completion_occurred_at=completed_at WHERE work_id=?`, id); err != nil {
					t.Fatal(err)
				}
				if _, err = tx.Exec(`DELETE FROM write_guard`); err != nil {
					t.Fatal(err)
				}
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			reopenVersion := agentCompositionWorkVersion(t, s, id)
			reopened := transition("reopen", "reopen-plan-reopen")
			var state, planAfter string
			var completedAt, completionPayload, completionOccurredAt *string
			if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state,step_plan,completed_at,completion_payload,completion_occurred_at FROM workflow_instances WHERE work_id=?`, id).Scan(&state, &planAfter, &completedAt, &completionPayload, &completionOccurredAt); err != nil {
				t.Fatal(err)
			}
			if state != "running" || planAfter != planBefore || completedAt != nil || completionPayload != nil || completionOccurredAt != nil {
				t.Fatalf("state=%s plan retained=%v completed_at=%v completion_payload=%v completion_occurred_at=%v", state, planAfter == planBefore, completedAt, completionPayload, completionOccurredAt)
			}
			replayInput, err := json.Marshal(map[string]any{"work_id": id, "expected_version": reopenVersion, "target": "reopen", "reason": "test", "evidence": []map[string]string{{"kind": "verification", "authority": "test", "locator_kind": "file", "locator": "README.md"}}, "idempotency_key": "reopen-plan-reopen"})
			if err != nil {
				t.Fatal(err)
			}
			replay, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: replayInput}, env)
			if err != nil || replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed || agentCompositionWorkVersion(t, s, id) != reopenVersion+1 {
				t.Fatalf("reopen replay=%+v err=%v", replay, err)
			}
			pin, err := store.ReadWorkPin(ctx, s, id)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, intent := range pin.NextValidIntents {
				if intent.ActionID == "insert_step" {
					found = true
				}
			}
			if !found {
				t.Fatalf("reopen intents missing insert_step: %+v response=%+v", pin.NextValidIntents, reopened)
			}
			compositionAction(t, ctx, s, service, env, id, "insert_step", map[string]any{"id": "follow_up", "after": []string{"plan"}, "reason": "additional work"})
		})
	}
}

func TestReopenDispatch(t *testing.T) {
	for _, name := range []string{"exact", "stale", "live", "holder"} {
		t.Run(name, func(t *testing.T) {
			s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define", "work_transition"})
			scope, _, err := s.ScopeVersion(context.Background(), "project-1")
			if err != nil {
				t.Fatal(err)
			}
			capture := `{"title":"Original","kind":"task","project_ids":["project-1"],"human_key":"ZZ-9010","remote_issue_uuid":"reopen-uuid","url":"https://example.com/issue/ZZ-9010","idempotency_key":"original"}`
			created := dispatchCapture(t, s, service, grant, scope, "original", capture)
			if created.Outcome != agentcontract.OutcomeOK || created.ChangedRefs == nil {
				t.Fatalf("capture: %+v", created)
			}
			id := (*created.ChangedRefs)[0].ID
			env := mutationEnvelope(grant, scope)
			dispatch := func(target string, version int, key string) agentcontract.Envelope {
				t.Helper()
				input := fmt.Sprintf(`{"work_id":%q,"expected_version":%d,"target":%q,"reason":"test","evidence":[{"kind":"verification","authority":"test","locator_kind":"file","locator":"README.md"}],"idempotency_key":%q}`, id, version, target, key)
				env.RequestID = key
				response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: json.RawMessage(input)}, env)
				if err != nil {
					t.Fatal(err)
				}
				return response
			}
			if name != "live" {
				cancelled := dispatch("cancelled", 3, "cancel")
				if cancelled.Outcome != agentcontract.OutcomeOK {
					t.Fatalf("cancel: %+v", cancelled.Error)
				}
			}
			if name == "holder" {
				holder := dispatchCapture(t, s, service, grant, scope, "new-holder", `{"title":"New holder","kind":"task","project_ids":["project-1"],"human_key":"ZZ-9010","remote_issue_uuid":"reopen-uuid","url":"https://example.com/issue/ZZ-9010","idempotency_key":"new-holder"}`)
				if holder.Outcome != agentcontract.OutcomeOK || holder.ChangedRefs == nil || len(*holder.ChangedRefs) == 0 {
					t.Fatalf("holder: %+v", holder.Error)
				}
			}
			version := 4
			if name == "stale" || name == "live" {
				version = 3
			}
			before := captureStateSnapshot(t, s)
			response := dispatch("reopen", version, "reopen")
			if name == "exact" {
				if response.Outcome != agentcontract.OutcomeOK {
					t.Fatalf("reopen: %+v", response.Error)
				}
				var lifecycle, evidence string
				var actual int
				if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle,version,transition_evidence_refs FROM work_items WHERE id=?`, id).Scan(&lifecycle, &actual, &evidence); err != nil {
					t.Fatal(err)
				}
				if lifecycle != "needed" || actual != 5 || evidence != `["README.md"]` {
					t.Fatalf("lifecycle=%s version=%d evidence=%s", lifecycle, actual, evidence)
				}
				var state string
				if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, id).Scan(&state); err != nil {
					t.Fatal(err)
				}
				t.Logf("workflow instance after reopen: %s", state)
			} else {
				if response.Error == nil || response.Outcome != agentcontract.OutcomeError {
					t.Fatalf("expected refusal: %+v", response)
				}
				var kind, message string
				switch name {
				case "stale":
					kind, message = "version_conflict", fmt.Sprintf("work_item %s has version 4, want 3", id)
				case "live":
					kind, message = "invalid_input", "work.reopened must name a terminal prior state and reason"
				case "holder":
					kind, message = "invalid_relation", "another live work item already holds that Linear issue UUID"
				}
				if response.Error.Kind != kind || response.Error.Message != message || response.Error.EffectState != "none" {
					t.Fatalf("refusal = %+v, want kind=%s message=%q effect_state=none", response.Error, kind, message)
				}
				after := captureStateSnapshot(t, s)
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("refusal changed state: before=%v after=%v", before, after)
				}
			}
		})
	}
}
