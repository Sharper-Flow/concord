package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

func TestWorktreeVerifyNewRequestRunsAgain(t *testing.T) {
	t.Parallel()
	for _, newEpoch := range []bool{false, true} {
		name := "same_refine_epoch"
		if newEpoch {
			name = "new_refine_epoch"
		}
		t.Run(name, func(t *testing.T) {
			s, service, grant, _, _, _ := tiersFixture(t)
			now := fixedTime().Add(time.Second)
			service.Now = func() time.Time { return now }
			seedContinuityWorkflow(t, s, "work-2")
			if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE workflow_instances SET current_step='refine' WHERE work_id='work-2'; DELETE FROM fold_guard`); err != nil {
				t.Fatal(err)
			}
			startRefine := func(key string) {
				t.Helper()
				var version int64
				if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-2'`).Scan(&version); err != nil {
					t.Fatal(err)
				}
				response := tiersInvoke(t, s, service, grant, "concord_work_transition", "workflow_action", map[string]any{
					"work_id": "work-2", "expected_version": version, "action_id": "start_refine", "idempotency_key": key,
				})
				if response.Outcome != OutcomeOK {
					t.Fatalf("start refine: %+v", response.Error)
				}
			}
			startRefine("verify-refine-1")
			now = now.Add(time.Second)
			counter := filepath.Join(t.TempDir(), "executions")
			command := []string{"sh", "-c", `printf 'run\n' >> "$1"`, "verify-counter", counter}
			firstEnvelope, first := invokeVerifyIdentity(t, s, service, grant, command, "verify-request-1")
			assertVerifyExecutionCount(t, counter, 1)
			if newEpoch {
				now = now.Add(time.Second)
				startRefine("verify-refine-2")
			}
			now = now.Add(time.Second)
			secondEnvelope, second := invokeVerifyIdentity(t, s, service, grant, command, "verify-request-2")
			assertVerifyExecutionCount(t, counter, 2)
			if second.LeaseID == first.LeaseID || second.OperationRef == first.OperationRef || secondEnvelope.Replayed {
				t.Fatalf("new request reused the first run: first=%+v second=%+v replayed=%v", first, second, secondEnvelope.Replayed)
			}
			firstRecord, found, err := s.LookupMutationIdempotency(context.Background(), store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: "concord_work_transition", OperationKind: "worktree_verify", IdempotencyKey: "verify-request-1"})
			if err != nil || !found {
				t.Fatalf("first request record found=%v: %v", found, err)
			}
			secondRecord, found, err := s.LookupMutationIdempotency(context.Background(), store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: "concord_work_transition", OperationKind: "worktree_verify", IdempotencyKey: "verify-request-2"})
			if err != nil || !found {
				t.Fatalf("second request record found=%v: %v", found, err)
			}
			if firstRecord.CanonicalDigest != secondRecord.CanonicalDigest || firstRecord.OperationID == secondRecord.OperationID {
				t.Fatalf("equal intent must retain its digest but name distinct requests: first=%+v second=%+v", firstRecord, secondRecord)
			}
			var acquiredAt, observedAt string
			if err := s.DatabaseForTesting().QueryRow(`SELECT acquired_at FROM worktree_verify_leases WHERE lease_id=?`, second.LeaseID).Scan(&acquiredAt); err != nil {
				t.Fatal(err)
			}
			if err := s.DatabaseForTesting().QueryRow(`SELECT observed_at FROM durable_operations WHERE op_id=?`, second.OperationRef).Scan(&observedAt); err != nil {
				t.Fatal(err)
			}
			wantTime := now.UTC().Format(time.RFC3339Nano)
			if acquiredAt != wantTime || observedAt != wantTime {
				t.Fatalf("new run timestamps acquired=%q observed=%q, want %q", acquiredAt, observedAt, wantTime)
			}
			if newEpoch {
				var startedAt string
				if err := s.DatabaseForTesting().QueryRow(`SELECT occurred_at FROM domain_events WHERE subject_id='work-2' AND kind=? AND json_extract(payload,'$.step_id')='refine' ORDER BY seq DESC LIMIT 1`, store.WorkflowActionStarted).Scan(&startedAt); err != nil {
					t.Fatal(err)
				}
				started, err := time.Parse(time.RFC3339Nano, startedAt)
				if err != nil || !now.After(started) {
					t.Fatalf("fresh run time %s must follow new refine start %s: %v", wantTime, startedAt, err)
				}
			}
			// A replay stays the original run even after another refine epoch.
			replay, replayResult := invokeVerifyIdentity(t, s, service, grant, command, "verify-request-1")
			if !replay.Replayed || replayResult.OperationRef != first.OperationRef || !bytes.Equal(replay.Result, firstEnvelope.Result) {
				t.Fatalf("same-key replay changed its outcome: first=%s replay=%s replayed=%v", firstEnvelope.Result, replay.Result, replay.Replayed)
			}
			assertVerifyExecutionCount(t, counter, 2)
		})
	}
}

func TestWorktreeVerifyRequestIdentityIncludesPrincipal(t *testing.T) {
	t.Parallel()
	s, service, grant, secondService, secondGrant, _ := tiersFixture(t)
	counter := filepath.Join(t.TempDir(), "executions")
	command := []string{"sh", "-c", `printf 'run\n' >> "$1"`, "verify-counter", counter}
	_, first := invokeVerifyIdentity(t, s, service, grant, command, "shared-verify-key")
	_, second := invokeVerifyIdentity(t, s, secondService, secondGrant, command, "shared-verify-key")
	assertVerifyExecutionCount(t, counter, 2)
	if first.LeaseID == second.LeaseID || first.OperationRef == second.OperationRef {
		t.Fatalf("distinct principals shared verification identity: first=%+v second=%+v", first, second)
	}
}

func TestWorktreeVerifySameRequestRefusesChangedCommand(t *testing.T) {
	t.Parallel()
	s, service, grant, _, _, _ := tiersFixture(t)
	counter := filepath.Join(t.TempDir(), "executions")
	command := []string{"sh", "-c", `printf 'run\n' >> "$1"`, "verify-counter", counter}
	invokeVerifyIdentity(t, s, service, grant, command, "verify-pinned-command")
	refused := tiersInvoke(t, s, service, grant, "concord_work_transition", "worktree_verify", map[string]any{
		"work_id": "work-2", "command": []string{"true"}, "idempotency_key": "verify-pinned-command",
	})
	if refused.Outcome != OutcomeError || refused.Error == nil || refused.Error.Kind != "idempotency_conflict" || refused.Error.EffectState != EffectNone {
		t.Fatalf("changed command under the same key: %+v", refused)
	}
	assertVerifyExecutionCount(t, counter, 1)
}

func TestWorktreeVerifyReplaysReleasedLeaseWithoutIdempotencyRecord(t *testing.T) {
	t.Parallel()
	s, service, grant, _, _, _ := tiersFixture(t)
	counter := filepath.Join(t.TempDir(), "executions")
	command := []string{"sh", "-c", `printf 'run\n' >> "$1"`, "verify-counter", counter}
	firstEnvelope, first := invokeVerifyIdentity(t, s, service, grant, command, "verify-release-recovery")
	// Model the crash window after lease release but before result persistence.
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM idempotency_records WHERE principal_ref=? AND tool='concord_work_transition' AND operation_kind='worktree_verify' AND idempotency_key='verify-release-recovery'`, grant.PrincipalRef); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.LookupMutationIdempotency(context.Background(), store.MutationIdempotencyKey{PrincipalRef: grant.PrincipalRef, Tool: "concord_work_transition", OperationKind: "worktree_verify", IdempotencyKey: "verify-release-recovery"}); err != nil || found {
		t.Fatalf("crash-window fixture still has a replay record: found=%v err=%v", found, err)
	}
	recovered, result := invokeVerifyIdentity(t, s, service, grant, command, "verify-release-recovery")
	if result.LeaseID != first.LeaseID || result.OperationRef != first.OperationRef || !bytes.Equal(recovered.Result, firstEnvelope.Result) {
		t.Fatalf("released-lease recovery changed the original result: first=%s recovered=%s", firstEnvelope.Result, recovered.Result)
	}
	assertVerifyExecutionCount(t, counter, 1)
}

func invokeVerifyIdentity(t *testing.T, s *store.Store, service *Service, grant Authority, command []string, key string) (Envelope, store.WorktreeVerifyResult) {
	t.Helper()
	response := tiersInvoke(t, s, service, grant, "concord_work_transition", "worktree_verify", map[string]any{
		"work_id": "work-2", "command": command, "idempotency_key": key,
	})
	if response.Outcome != OutcomeOK {
		t.Fatalf("verify %s: %+v", key, response.Error)
	}
	var result store.WorktreeVerifyResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.TrackedFilesChanged || result.LeaseID == "" || result.OperationRef == "" {
		t.Fatalf("verify %s result=%+v", key, result)
	}
	return response, result
}

func assertVerifyExecutionCount(t *testing.T, path string, want int) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := bytes.Count(data, []byte("run\n")); got != want {
		t.Fatalf("verification command executions=%d, want %d", got, want)
	}
}
