package agent

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestSameSessionServiceRestartRefusesUnresolvedStartedAssignment(t *testing.T) {
	f := newSpineExecutionFixture(t)
	f.verify(t)
	f.recordJob(t)
	lane, _, _ := f.startWorker(t, "implement", "attempt:started-before-restart")
	before := f.read(t).Pinned.Pin
	var unresolved int
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_attempts WHERE work_id=? AND dispatch_seq>0 AND terminal_seq=0`, f.work).Scan(&unresolved); err != nil || unresolved != 1 {
		t.Fatalf("started unresolved attempts = %d, error=%v", unresolved, err)
	}
	var beforeAttempts, beforeAuthorizations int
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT
	 (SELECT count(*) FROM worker_attempts WHERE work_id=?),
	 (SELECT count(*) FROM worker_authorizations WHERE work_id=?)`, f.work, f.work).Scan(&beforeAttempts, &beforeAuthorizations); err != nil {
		t.Fatal(err)
	}

	// Reopen persisted authority without changing the authenticated caller or
	// carrying any adapter-local dispatch window into the new service.
	database := f.s.Path()
	if err := f.s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	f.s, f.service = reopened, NewService(reopened)
	f.service.Now = time.Now
	resolveProjectAuthority(f.service, store.ProjectResolution{ProjectID: "project-1"})

	for _, replacement := range []string{"attempt:fresh-after-restart", "attempt:another-fresh-key"} {
		packet := bindPacketToRecordedState(t, f.s, map[string]any{
			"schema_version": store.WorkerPacketSchemaVersion, "attempt_id": replacement,
			"lane_id": lane.ID, "lane_digest": lane.Digest,
			"work_id": f.work, "step_id": f.read(t).Pinned.Step, "inputs": map[string]any{},
		})
		refused := f.action(t, "dispatch_worker", map[string]any{"attempt_id": replacement, "worker_packet": packet})
		if refused.Outcome != agentcontract.OutcomeError || refused.Error == nil || refused.Error.Kind != "operation_conflict" {
			t.Fatalf("fresh dispatch while the same caller's started assignment is unresolved: outcome=%s error=%+v", refused.Outcome, refused.Error)
		}
		after := f.read(t).Pinned.Pin
		if after.Version != before.Version || !reflect.DeepEqual(after.Attempt, before.Attempt) || !reflect.DeepEqual(after.Review, before.Review) {
			t.Fatal("refused restarted dispatch changed original attempt, accepted progress, or work version")
		}
		var attempts, authorizations int
		if err := f.s.DatabaseForTesting().QueryRow(`SELECT
		 (SELECT count(*) FROM worker_attempts WHERE work_id=?),
		 (SELECT count(*) FROM worker_authorizations WHERE work_id=?)`, f.work, f.work).Scan(&attempts, &authorizations); err != nil || attempts != beforeAttempts || authorizations != beforeAuthorizations {
			t.Fatalf("refused dispatch minted attempts=%d authorizations=%d, error=%v", attempts, authorizations, err)
		}
	}
}
