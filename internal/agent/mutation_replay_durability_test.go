package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// A separate read snapshot pins the WAL while replay writes append to it.
// Only this synthetic store shortens lock waits; production settings stay fixed.
func holdReplayDurabilityReader(t *testing.T, s *store.Store) func() {
	t.Helper()
	ctx := context.Background()
	if _, err := s.DatabaseForTesting().ExecContext(ctx, "PRAGMA busy_timeout=100"); err != nil {
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

func replayMetadataCount(t *testing.T, s *store.Store, key string) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT replayed_count FROM idempotency_records WHERE idempotency_key=?`, key).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func requireReplayCommit(t *testing.T, s *store.Store, key string, before uint64, metadataBefore int, durable bool) {
	t.Helper()
	want := before
	if durable {
		want++
	}
	if got := s.DurableCommits(); got != want {
		t.Fatalf("replay durable commits=%d, want %d (before=%d)", got, want, before)
	}
	if got := replayMetadataCount(t, s, key); got != metadataBefore+1 {
		t.Fatalf("replay metadata count=%d, want %d", got, metadataBefore+1)
	}
	var level int
	if err := s.DatabaseForTesting().QueryRow("PRAGMA synchronous").Scan(&level); err != nil {
		t.Fatal(err)
	}
	if level != 1 {
		t.Fatalf("replay left pooled synchronous=%d, want NORMAL (1)", level)
	}
}

func requireReplayReaderPinsWAL(t *testing.T, s *store.Store) {
	t.Helper()
	var busy, log, checkpointed int
	if err := s.DatabaseForTesting().QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checkpointed); err != nil {
		t.Fatal(err)
	}
	if busy != 1 || log <= checkpointed {
		t.Fatalf("reader did not pin replay frames: busy=%d log=%d checkpointed=%d", busy, log, checkpointed)
	}
}

func TestWorkflowReplayCommitsDurablyUnderPinnedReader(t *testing.T) {
	t.Parallel()
	for _, legacy := range []bool{false, true} {
		name := "durable_result"
		if legacy {
			name = "normal_result"
		}
		t.Run(name, func(t *testing.T) {
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
			if legacy {
				before := s.DurableCommits()
				seedCurrentWorkflowActionReplay(t, s, env, request.Input, "completed")
				if s.DurableCommits() != before {
					t.Fatal("legacy result fixture committed durably")
				}
			} else {
				first, err := Dispatch(ctx, s, service, request, env)
				if err != nil || first.Outcome != OutcomeOK || first.Error != nil {
					t.Fatalf("workflow under pinned reader: response=%+v error=%+v err=%v", first, first.Error, err)
				}
			}
			beforeEvents := countWorkflowEvents(t, s)
			beforeVersion := workflowReplayWorkVersion(t, s)
			beforeChallenges := countWorkflowApprovalChallenges(t, s)
			for _, pinned := range []bool{true, false} {
				if !pinned {
					release()
				}
				before := s.DurableCommits()
				metadataBefore := replayMetadataCount(t, s, "durability-replay")
				replay, err := Dispatch(ctx, s, service, request, env)
				if err != nil || replay.Outcome != OutcomeOK || replay.Error != nil || !replay.Replayed {
					t.Fatalf("workflow replay pinned=%t: response=%+v error=%+v err=%v", pinned, replay, replay.Error, err)
				}
				if err := replay.Validate(); err != nil {
					t.Fatal(err)
				}
				requireReplayCommit(t, s, "durability-replay", before, metadataBefore, true)
				if countWorkflowEvents(t, s) != beforeEvents || workflowReplayWorkVersion(t, s) != beforeVersion || countWorkflowApprovalChallenges(t, s) != beforeChallenges {
					t.Fatal("replay repeated the workflow action or approval")
				}
				if pinned {
					requireReplayReaderPinsWAL(t, s)
				}
			}
		})
	}
}

func TestWorkflowReplayDurableWriteFailureDoesNotAcknowledge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
	seedAgentWorkflow(t, s, grant)
	env := grantRequestEnvelope(t, s, grant)
	release := holdReplayDurabilityReader(t, s)
	request := InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: json.RawMessage(`{"work_id":"work-1","expected_version":4,"action_id":"record_proposal","fields":{},"idempotency_key":"durability-replay"}`)}
	seedCurrentWorkflowActionReplay(t, s, env, request.Input, "completed")
	before := s.DurableCommits()
	metadataBefore := replayMetadataCount(t, s, "durability-replay")
	beforeEvents := countWorkflowEvents(t, s)
	beforeVersion := workflowReplayWorkVersion(t, s)
	writer, err := sql.Open("sqlite", "file:"+s.Path())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	writeTx, err := writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writeTx.Rollback() })
	if _, err := writeTx.ExecContext(ctx, `UPDATE idempotency_records SET replayed_count=replayed_count+1 WHERE idempotency_key='durability-replay'`); err != nil {
		t.Fatal(err)
	}
	response, err := Dispatch(ctx, s, service, request, env)
	if err != nil || response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "unreachable" || !response.Error.RetrySafe {
		t.Fatalf("replay under writer contention: response=%+v error=%+v err=%v", response, response.Error, err)
	}
	if err := response.Validate(); err != nil {
		t.Fatal(err)
	}
	if s.DurableCommits() != before || replayMetadataCount(t, s, "durability-replay") != metadataBefore {
		t.Fatal("failed durable replay changed committed metadata")
	}
	if err := writeTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	response, err = Dispatch(ctx, s, service, request, env)
	if err != nil || response.Outcome != OutcomeOK || !response.Replayed {
		t.Fatalf("replay after writer release: response=%+v error=%+v err=%v", response, response.Error, err)
	}
	requireReplayCommit(t, s, "durability-replay", before, metadataBefore, true)
	if countWorkflowEvents(t, s) != beforeEvents || workflowReplayWorkVersion(t, s) != beforeVersion {
		t.Fatal("retry repeated the business effect")
	}
	release()
}

func TestCompactionReplayCommitsDurablyUnderPinnedReader(t *testing.T) {
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
				beforeEvents := countWorkflowEvents(t, s)
				beforeVersion := workflowReplayWorkVersion(t, s)
				r := runtime{Store: s, Authority: service, Envelope: env, Tool: "concord_work_compact", Operation: operation}
				op, ok := ValidateContractOperation(r.Tool, r.Operation)
				if !ok {
					t.Fatal("fixture operation is not registered")
				}
				for _, pinned := range []bool{true, false} {
					if !pinned {
						release()
					}
					before := s.DurableCommits()
					metadataBefore := replayMetadataCount(t, s, "compaction-replay")
					response, handled, replayErr := r.replayMutationBeforeScope(ctx, NewBase(env.RequestID, r.Tool, r.Operation), raw, grant, op)
					want := OutcomeOK
					if kind == store.ResultPending {
						want = OutcomePending
					}
					if replayErr != nil || !handled || response.Outcome != want || !response.Replayed {
						t.Fatalf("compaction replay pinned=%t: handled=%t outcome=%s err=%v", pinned, handled, response.Outcome, replayErr)
					}
					if err := response.Validate(); err != nil {
						t.Fatal(err)
					}
					requireReplayCommit(t, s, "compaction-replay", before, metadataBefore, true)
					step, err := store.Step(ctx, s, claim.OpID)
					if err != nil || step.AttemptEpoch != beforeStep.AttemptEpoch || step.ResultKind != beforeStep.ResultKind || step.ResultPayload != beforeStep.ResultPayload || countWorkflowEvents(t, s) != beforeEvents || workflowReplayWorkVersion(t, s) != beforeVersion {
						t.Fatalf("replay changed the business effect or fenced attempt: %+v err=%v", step, err)
					}
					if pinned {
						requireReplayReaderPinsWAL(t, s)
					}
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
	beforeEvents := countWorkflowEvents(t, s)
	before := s.DurableCommits()
	metadataBefore := replayMetadataCount(t, s, "ordinary-replay")
	env.ScopeVersion = "drifted-scope-version"
	replay, err := Dispatch(ctx, s, service, request, env)
	if err != nil || replay.Outcome != OutcomeOK || !replay.Replayed {
		t.Fatalf("ordinary replay: outcome=%s error=%+v err=%v", replay.Outcome, replay.Error, err)
	}
	requireReplayCommit(t, s, "ordinary-replay", before, metadataBefore, false)
	if countWorkflowEvents(t, s) != beforeEvents {
		t.Fatal("ordinary replay duplicated events")
	}
	requireReplayReaderPinsWAL(t, s)
	release()
}
