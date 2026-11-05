package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func replayFixtureEnvelope(t *testing.T, s *store.Store, grant Authority) CallEnvelope {
	t.Helper()
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	return mutationEnvelope(grant, scopeVersion)
}

func prepareCurrentImplement(t *testing.T, s *store.Store, service *Service, grant Authority, env CallEnvelope, prefix string) int64 {
	t.Helper()
	seedCurrentWorkflowDomainFixture(t, s)
	ctx := agentWorkflowSourceProof(t, context.Background(), s, "work-1")
	actor := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-1", Actor: actor, Now: fixedTime()})
	}); err != nil {
		t.Fatal(err)
	}
	advanceCapturedWorkToContract(t, s, service, env, "work-1", prefix)
	for _, action := range []struct {
		id     string
		fields map[string]any
	}{
		{"approve_contract", workflowContractFieldsForSeededRegistry(t, s)},
		{"complete_step", map[string]any{"step_id": "plan", "note": "Plan is complete."}},
	} {
		response := invokeWorkflowBoundary(t, s, service, env, map[string]any{"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": action.id, "fields": action.fields, "idempotency_key": prefix + "-" + action.id})
		if response.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("%s: %+v", action.id, response.Error)
		}
	}
	return workVersion(t, s, "work-1")
}

// A separate read snapshot pins the WAL while replay writes append to it.
// Only this synthetic store shortens lock waits; production settings stay fixed.
func holdReplayDurabilityReader(t *testing.T, s *store.Store) func() {
	t.Helper()
	ctx := context.Background()
	if _, err := s.DatabaseForTesting().ExecContext(ctx, "PRAGMA busy_timeout=100"); err != nil {
		t.Fatal(err)
	}
	reader, err := sql.Open("sqlite", store.SQLiteFileURI(s.Path())+"?mode=ro")
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
	if err := snapshot.QueryRowContext(ctx, "SELECT count(*) FROM idempotency_records").Scan(&count); err != nil {
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
	wantMetadata := metadataBefore
	if durable {
		want++
		wantMetadata++
	}
	if got := s.DurableCommits(); got != want {
		t.Fatalf("replay durable commits=%d, want %d (before=%d)", got, want, before)
	}
	if got := replayMetadataCount(t, s, key); got != wantMetadata {
		t.Fatalf("replay metadata count=%d, want %d", got, wantMetadata)
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
	for _, normalCommit := range []bool{false, true} {
		name := "durable_result"
		if normalCommit {
			name = "normal_result"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
			env := replayFixtureEnvelope(t, s, grant)
			version := prepareCurrentImplement(t, s, service, grant, env, "durability-setup")
			release := holdReplayDurabilityReader(t, s)
			request := InvokeRequest{
				Tool: "concord_work_transition", Operation: "workflow_action",
				Input: retryJSON(map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "start_execution", "fields": map[string]any{}, "idempotency_key": "durability-replay"}),
			}
			var originalPayload string
			if normalCommit {
				before := s.DurableCommits()
				seedCurrentWorkflowActionReplay(t, s, env, request.Input, "completed")
				if s.DurableCommits() != before {
					t.Fatal("NORMAL response fixture committed durably")
				}
				if err := s.DatabaseForTesting().QueryRow(`SELECT result_payload FROM idempotency_records WHERE idempotency_key='durability-replay'`).Scan(&originalPayload); err != nil {
					t.Fatal(err)
				}
			} else {
				first, err := Dispatch(ctx, s, service, request, env)
				if err != nil || first.Outcome != agentcontract.OutcomeOK || first.Error != nil {
					t.Fatalf("workflow under pinned reader: response=%+v error=%+v err=%v", first, first.Error, err)
				}
				originalPayload = string(first.Result)
			}
			beforeEvents := countWorkflowEvents(t, s)
			beforeVersion := workOneVersion(t, s)
			for _, pinned := range []bool{true, false} {
				if !pinned {
					release()
				}
				before := s.DurableCommits()
				metadataBefore := replayMetadataCount(t, s, "durability-replay")
				replay, err := Dispatch(ctx, s, service, request, env)
				if err != nil || replay.Outcome != agentcontract.OutcomeOK || replay.Error != nil || !replay.Replayed {
					t.Fatalf("workflow replay pinned=%t: response=%+v error=%+v err=%v", pinned, replay, replay.Error, err)
				}
				if string(replay.Result) != originalPayload {
					t.Fatalf("durable replay changed the committed response: got=%s want=%s", replay.Result, originalPayload)
				}
				if err := replay.Validate(); err != nil {
					t.Fatal(err)
				}
				requireReplayCommit(t, s, "durability-replay", before, metadataBefore, true)
				if countWorkflowEvents(t, s) != beforeEvents || workOneVersion(t, s) != beforeVersion {
					t.Fatal("replay repeated the workflow action")
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
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
	env := replayFixtureEnvelope(t, s, grant)
	version := prepareCurrentImplement(t, s, service, grant, env, "failure-setup")
	release := holdReplayDurabilityReader(t, s)
	request := InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(map[string]any{"work_id": "work-1", "expected_version": version, "action_id": "start_execution", "fields": map[string]any{}, "idempotency_key": "durability-replay"})}
	seedCurrentWorkflowActionReplay(t, s, env, request.Input, "completed")
	var originalPayload string
	if err := s.DatabaseForTesting().QueryRow(`SELECT result_payload FROM idempotency_records WHERE idempotency_key='durability-replay'`).Scan(&originalPayload); err != nil {
		t.Fatal(err)
	}
	before := s.DurableCommits()
	metadataBefore := replayMetadataCount(t, s, "durability-replay")
	beforeEvents := countWorkflowEvents(t, s)
	beforeVersion := workOneVersion(t, s)
	writer, err := sql.Open("sqlite", store.SQLiteFileURI(s.Path()))
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
	if err != nil || response.Outcome != agentcontract.OutcomeError || response.Error == nil || response.Error.Kind != "unreachable" || !response.Error.RetrySafe {
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
	if err != nil || response.Outcome != agentcontract.OutcomeOK || !response.Replayed || string(response.Result) != originalPayload {
		t.Fatalf("replay after writer release: response=%+v error=%+v err=%v", response, response.Error, err)
	}
	requireReplayCommit(t, s, "durability-replay", before, metadataBefore, true)
	if countWorkflowEvents(t, s) != beforeEvents || workOneVersion(t, s) != beforeVersion {
		t.Fatal("retry repeated the business effect")
	}
	release()
}

func TestOrdinaryMutationReplayDoesNotRequireDurability(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define"})
	env := replayFixtureEnvelope(t, s, grant)
	release := holdReplayDurabilityReader(t, s)
	request := InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: json.RawMessage(`{"title":"Replay","kind":"task","project_ids":["project-1"],"idempotency_key":"ordinary-replay","human_key":"ZZ-9111","remote_issue_uuid":"fixture-ordinary-replay","url":"https://linear.app/example/issue/ZZ-9111"}`)}
	first, err := Dispatch(ctx, s, service, request, env)
	if err != nil || first.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("ordinary mutation: outcome=%s error=%+v err=%v", first.Outcome, first.Error, err)
	}
	if first.ChangedRefs == nil || len(*first.ChangedRefs) != 2 {
		t.Fatalf("capture did not report both work and link: %+v", first.ChangedRefs)
	}
	work, link := (*first.ChangedRefs)[0], (*first.ChangedRefs)[1]
	if work.EntityKind != "work_item" || work.ID == "" || work.Version == "" || link.EntityKind != "linear_issue_link" || link.ID != work.ID || link.Version != "1" {
		t.Fatalf("unexpected capture refs: %+v", first.ChangedRefs)
	}
	driftWorkPinVersion(t, s, work.ID)
	beforeEvents := countWorkflowEvents(t, s)
	before := s.DurableCommits()
	metadataBefore := replayMetadataCount(t, s, "ordinary-replay")
	env.ScopeVersion = "drifted-scope-version"
	writer, err := sql.Open("sqlite", store.SQLiteFileURI(s.Path()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	writeTx, err := writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writeTx.Rollback() })
	if _, err := writeTx.ExecContext(ctx, `UPDATE idempotency_records SET replayed_count=replayed_count+1 WHERE idempotency_key='ordinary-replay'`); err != nil {
		t.Fatal(err)
	}
	replay, err := Invoke(ctx, s, service, mustMarshalInvoke(t, request, env))
	if err != nil || replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed {
		t.Fatalf("ordinary replay while another connection holds the writer: outcome=%s error=%+v err=%v", replay.Outcome, replay.Error, err)
	}
	if string(replay.Result) != string(first.Result) || !reflect.DeepEqual(replay.ChangedRefs, first.ChangedRefs) || !reflect.DeepEqual(replay.NextValidIntents, first.NextValidIntents) || !reflect.DeepEqual(replay.Omissions, first.Omissions) || !reflect.DeepEqual(replay.ResolvedScope, first.ResolvedScope) {
		t.Fatalf("ordinary replay changed the retained response: first=%+v replay=%+v", first, replay)
	}
	if err := writeTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	requireReplayCommit(t, s, "ordinary-replay", before, metadataBefore, false)
	if countWorkflowEvents(t, s) != beforeEvents {
		t.Fatal("ordinary replay duplicated events")
	}
	requireReplayReaderPinsWAL(t, s)
	release()
}
