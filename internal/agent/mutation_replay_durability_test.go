package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// An old read snapshot permits WAL writes but prevents their TRUNCATE barrier.
// Only this synthetic store shortens the wait; production settings stay fixed.
func holdReplayDurabilityReader(t *testing.T, s *store.Store) func() {
	t.Helper()
	ctx := context.Background()
	if _, err := s.DatabaseForTesting().ExecContext(ctx, "PRAGMA busy_timeout=100"); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncDurable(ctx); err != nil {
		t.Fatal(err)
	}
	reader, err := sql.Open("sqlite", "file:"+s.Path()+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	snapshot, err := reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = snapshot.Rollback() })
	var count int
	if err := snapshot.QueryRowContext(ctx, "SELECT count(*) FROM domain_events").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := snapshot.Rollback(); err != nil {
			t.Fatal(err)
		}
	}
}

func requireReplayDurabilityFailure(t *testing.T, response Envelope) {
	t.Helper()
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "unreachable" || response.Error.EffectState != EffectPossible || !strings.Contains(response.Error.Message, "durability checkpoint did not complete") {
		t.Fatalf("unconfirmed durability returned outcome=%s error=%+v", response.Outcome, response.Error)
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("durability refusal envelope: %v", err)
	}
}

func TestWorkflowReplayRequiresDurabilityAfterFailedBarrier(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
	if version := seedAgentWorkflow(t, s, grant); version != 4 {
		t.Fatalf("fixture workflow version=%d, want 4", version)
	}
	env := grantRequestEnvelope(t, s, grant)
	release := holdReplayDurabilityReader(t, s)
	request := InvokeRequest{
		Tool: "concord_work_transition", Operation: "workflow_action",
		Input: json.RawMessage(`{"work_id":"work-1","expected_version":4,"action_id":"record_proposal","fields":{"problem":"The bounded problem statement.","affected":["The affected system."],"stakes":"The bounded stakes statement.","user_outcomes":["The expected user outcome."]},"idempotency_key":"durability-replay"}`),
	}
	first, err := Dispatch(ctx, s, service, request, env)
	if err != nil {
		t.Fatal(err)
	}
	requireReplayDurabilityFailure(t, first)
	beforeEvents := countWorkflowEvents(t, s)
	beforeVersion := workflowReplayWorkVersion(t, s)
	beforeChallenges := countWorkflowApprovalChallenges(t, s)
	replay, err := Dispatch(ctx, s, service, request, env)
	if err != nil {
		t.Fatal(err)
	}
	requireReplayDurabilityFailure(t, replay)
	if countWorkflowEvents(t, s) != beforeEvents || workflowReplayWorkVersion(t, s) != beforeVersion || countWorkflowApprovalChallenges(t, s) != beforeChallenges {
		t.Fatal("failed-barrier replay repeated the workflow action or approval")
	}
	release()
	afterRelease, err := Dispatch(ctx, s, service, request, env)
	if err != nil || afterRelease.Outcome != OutcomeOK || !afterRelease.Replayed {
		t.Fatalf("replay after reader release: outcome=%s error=%+v err=%v", afterRelease.Outcome, afterRelease.Error, err)
	}
	if countWorkflowEvents(t, s) != beforeEvents || workflowReplayWorkVersion(t, s) != beforeVersion {
		t.Fatal("successful replay repeated the committed workflow action")
	}
}

func TestCompactionReplayRequiresDurability(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"publish", "reconcile"} {
		for _, kind := range []store.ResultKind{store.ResultCompleted, store.ResultPending} {
			t.Run(operation+"/"+string(kind), func(t *testing.T) {
				ctx := context.Background()
				s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_compact"})
				env := grantRequestEnvelope(t, s, grant)
				release := holdReplayDurabilityReader(t, s)
				raw := json.RawMessage(`{"work_id":"work-1","idempotency_key":"compaction-replay"}`)
				const scope = `{"product_id":"product-1","project_ids":["project-1"],"work_ids":["work-1"]}`
				claim, err := store.ClaimStep(ctx, s, store.ClaimRequest{
					OpID: "compaction-replay", WorkID: "work-1", WorkflowTypeRef: "concord.pm6.compaction", WorkflowTypeVersion: 1,
					StepID: "git_proof", StepKind: store.StepCrossAuthority, AcceptedInputsDigest: mutationDigest("concord_work_compact", operation, env, raw),
					AcceptedScopeSnapshot: scope, PrincipalRef: grant.PrincipalRef, Tool: "concord_work_compact", IdempotencyKey: "compaction-replay",
					RequestID: env.RequestID, ObservedAt: fixedTime(), ContractDigest: ManifestDigest,
				})
				if err != nil {
					t.Fatal(err)
				}
				if kind == store.ResultCompleted {
					if _, err := store.CompleteStep(ctx, s, store.CompleteRequest{
						OpID: claim.OpID, AttemptEpoch: claim.AttemptEpoch, ResultKind: kind, ResultPayload: `{"changed_refs":[],"next_valid_intents":[]}`,
						PrincipalRef: grant.PrincipalRef, Tool: "concord_work_compact", IdempotencyKey: "compaction-replay", RequestID: env.RequestID, ObservedAt: fixedTime(),
					}); err != nil {
						t.Fatal(err)
					}
				}
				beforeStep, err := store.Step(ctx, s, claim.OpID)
				if err != nil {
					t.Fatal(err)
				}
				r := runtime{Store: s, Authority: service, Envelope: env, Tool: "concord_work_compact", Operation: operation}
				op, ok := ValidateContractOperation(r.Tool, r.Operation)
				if !ok {
					t.Fatal("fixture operation is not registered")
				}
				base := NewBase(env.RequestID, r.Tool, r.Operation)
				response, handled, replayErr := r.replayMutationBeforeScope(ctx, base, raw, grant, op)
				if replayErr == nil {
					t.Fatalf("unconfirmed %s replay acknowledged handled=%t outcome=%s", kind, handled, response.Outcome)
				}
				requireReplayDurabilityFailure(t, failureEnvelope(base, replayErr))
				release()
				response, handled, replayErr = r.replayMutationBeforeScope(ctx, base, raw, grant, op)
				want := OutcomeOK
				if kind == store.ResultPending {
					want = OutcomePending
				}
				if replayErr != nil || !handled || response.Outcome != want || !response.Replayed {
					t.Fatalf("released-reader replay: handled=%t outcome=%s err=%v", handled, response.Outcome, replayErr)
				}
				step, err := store.Step(ctx, s, claim.OpID)
				if err != nil || step.AttemptEpoch != beforeStep.AttemptEpoch || step.ResultKind != beforeStep.ResultKind {
					t.Fatalf("replay changed the fenced attempt: %+v err=%v", step, err)
				}
			})
		}
	}
}

func TestOrdinaryMutationReplayDoesNotRequireDurability(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_define"})
	env := grantRequestEnvelope(t, s, grant)
	release := holdReplayDurabilityReader(t, s)
	request := InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: json.RawMessage(`{"title":"Replay","value_statement":"Replay value","kind":"task","project_ids":["project-1"],"idempotency_key":"ordinary-replay"}`)}
	first, err := Dispatch(ctx, s, service, request, env)
	if err != nil || first.Outcome != OutcomeOK {
		t.Fatalf("ordinary mutation: outcome=%s error=%+v err=%v", first.Outcome, first.Error, err)
	}
	before := countWorkflowEvents(t, s)
	env.ScopeVersion = "drifted-scope-version"
	replay, err := Dispatch(ctx, s, service, request, env)
	if err != nil || replay.Outcome != OutcomeOK || !replay.Replayed {
		t.Fatalf("ordinary replay: outcome=%s error=%+v err=%v", replay.Outcome, replay.Error, err)
	}
	if countWorkflowEvents(t, s) != before {
		t.Fatal("ordinary replay duplicated events")
	}
	if err := s.SyncDurable(ctx); err == nil || !strings.Contains(err.Error(), "durability checkpoint did not complete") {
		t.Fatalf("fixture did not retain its blocked barrier: %v", err)
	}
	release()
}
