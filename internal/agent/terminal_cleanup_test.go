package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func holdCompactionCleanup(t *testing.T, s *store.Store) {
	t.Helper()
	_, err := s.DatabaseForTesting().Exec(`INSERT INTO worktree_claims(op_id,work_id,project_id,set_id,pinned_branch,pinned_base_sha,pinned_path,state,principal_ref,request_id,observed_at,updated_at) VALUES('cleanup-claim','work-cancelled','proj-api',?,'work/closed',?,'/synthetic/work','pending','operator','fixture','now','now')`, store.WorktreeSetID("work-cancelled"), strings.Repeat("1", 40))
	if err != nil {
		t.Fatal(err)
	}
}

func TestTerminalCleanupAfterCompactionSettlement(t *testing.T) {
	for _, route := range []string{"publish", "reconcile", "cached_publish", "claimed_publish", "completed_reconcile"} {
		t.Run(route, func(t *testing.T) {
			s, service, grant, key, home := agentJobsCompactionFixture(t)
			initializeMutationPinWorkflow(t, s, "work-cancelled", store.BuiltinWorkflowDefinitions()[0])
			publication := newCancelledNotePublication(t, s, grant, home, "compact-cancelled-approval-roundtrip-1")
			if route != "publish" && route != "reconcile" {
				holdCompactionCleanup(t, s)
			}
			if route == "reconcile" {
				service.publicationObserver = func(phase string) error {
					if phase == "verify_commit" {
						return context.Canceled
					}
					return nil
				}
			}
			response := publishCancelledNote(t, s, service, grant, key, home)
			if route == "publish" {
				if response.Outcome != OutcomeOK {
					t.Fatalf("publication settlement: %+v", response)
				}
			} else {
				var operationID string
				if err := s.DatabaseForTesting().QueryRow(`SELECT op_id FROM durable_operations WHERE work_id='work-cancelled'`).Scan(&operationID); err != nil {
					t.Fatal(err)
				}
				if route != "reconcile" && (response.Outcome != OutcomeOK || len(response.Warnings) == 0 || response.Warnings[0].Kind != "resource_busy") {
					t.Fatalf("deferred cleanup must preserve publication success and typed blocker: %+v", response)
				}
				if _, err := s.DatabaseForTesting().Exec(`DELETE FROM worktree_claims WHERE op_id='cleanup-claim'`); err != nil {
					t.Fatal(err)
				}
				service.publicationObserver = nil
				env := agentJobsMutationEnvelope(t, s, grant, "proj-api")
				input, _ := json.Marshal(map[string]any{"operation_id": operationID, "expected_operation_version": 1, "idempotency_key": "cleanup-reconcile"})
				if route == "cached_publish" || route == "claimed_publish" {
					var approvalRef string
					if err := s.DatabaseForTesting().QueryRow(`SELECT challenge_ref FROM agent_approval_challenges WHERE consequence='publication'`).Scan(&approvalRef); err != nil {
						t.Fatal(err)
					}
					request, approvedEnvelope := publication.approvedBy(t, key, approvalRef)
					input, env = request.Input, approvedEnvelope
					if route == "cached_publish" {
						response = dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_compact", Operation: "publish", Input: input}, env)
					} else {
						r := runtime{Store: s, Authority: service, Envelope: env, Tool: "concord_work_compact", Operation: "publish"}
						op, _ := ValidateContractOperation(r.Tool, r.Operation)
						var err error
						response, err = r.mutateCompaction(t.Context(), NewBase(env.RequestID, r.Tool, r.Operation), input, mutationDigest(r.Tool, r.Operation, env, input), op)
						if err != nil {
							t.Fatal(err)
						}
					}
				} else {
					response = dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_compact", Operation: "reconcile", Input: input}, env)
				}
				if response.Outcome != OutcomeOK {
					t.Fatalf("settled %s must succeed: %+v", route, response)
				}
			}
			var count int
			if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM work_items WHERE id='work-cancelled'`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("%s retained execution: count=%d err=%v warnings=%+v", route, count, err, response.Warnings)
			}
			var result struct {
				Pins []store.WorkPin `json:"work_pins"`
			}
			if err := json.Unmarshal(response.Result, &result); err != nil || len(result.Pins) != 0 || len(response.Omissions) != 1 || response.Omissions[0].Kind != "work_pin_unavailable" {
				t.Fatalf("cleaned result must omit live pins: result=%s omissions=%+v err=%v", response.Result, response.Omissions, err)
			}
			for _, operation := range []string{"publish", "reconcile"} {
				request := publication.request()
				if operation == "reconcile" {
					input, _ := json.Marshal(map[string]any{"work_id": "work-cancelled", "expected_work_version": publication.versions["work"], "idempotency_key": "collected-reconcile"})
					request = InvokeRequest{Tool: "concord_work_compact", Operation: "reconcile", Input: input}
				}
				late := dispatchMutation(t, s, service, request, publication.env)
				if late.Outcome != OutcomeError || late.Error == nil || late.Error.Kind != "unknown_scope" || !strings.Contains(late.Error.Message, "canonical outcome:") {
					t.Fatalf("late %s lost canonical absence: %+v", operation, late)
				}
			}
		})
	}
}
