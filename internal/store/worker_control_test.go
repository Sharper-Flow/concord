package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func workerControlFixture(t *testing.T, dispatched bool) (*nativeOracleFixture, WorkerControlRequest) {
	t.Helper()
	f := newNativeOracleFixture(t)
	packet := mustJSONValue(map[string]any{"work_id": f.work, "attempt_id": f.a.AttemptID, "job": f.a.WorkerJobBinding.Digest, "subject_commit": f.head})
	if dispatched {
		lv, ld := implementLaneIdentity()
		lane, err := LookupLane("implement", lv, ld)
		if err != nil {
			t.Fatal(err)
		}
		event := workerDispatchEvent(f.work, f.a.AttemptID, lane, map[string]any{
			"packet_digest": f.a.WorkerPacketDigest, "worker_job": f.a.WorkerJobBinding,
			"host_provenance": histDeclaredHostProvenance(),
		})
		event.PayloadVersion = WorkerEvidenceEventPayloadVersion(WorkerDispatched)
		if err := f.s.Transact(context.Background(), func(tx *Transaction) error {
			events, err := PrepareLaneActorDispatch(context.Background(), tx, event, f.actor.PrincipalRef, f.actor.ClientRef)
			if err != nil {
				return err
			}
			_, err = AppendLaneActorDispatchTx(context.Background(), tx, events)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	return f, WorkerControlRequest{WorkID: f.work, ProjectID: "project", AttemptID: f.a.AttemptID, WorktreeIdentity: workerWorktreeIdentity(f.repo), Packet: packet, ControlID: f.a.ControlID}
}

func TestResolveWorkerControlUsesRecordedProducerBindingAndLaneActor(t *testing.T) {
	f, req := workerControlFixture(t, true)
	var eventsBefore, leasesBefore int
	if err := f.s.db.QueryRow(`SELECT count(*) FROM domain_events`).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	if err := f.s.db.QueryRow(`SELECT count(*) FROM worktree_verify_leases`).Scan(&leasesBefore); err != nil {
		t.Fatal(err)
	}
	plan, err := f.s.ResolveWorkerControl(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !oracleEntryIdentical(plan.Oracle, f.a) || plan.SubjectCommit != f.head {
		t.Fatalf("plan = %+v, want the exact recorded oracle request and raw subject", plan)
	}
	actor := plan.Actor
	if actor.PrincipalRef != f.actor.PrincipalRef || actor.ClientRef != f.actor.ClientRef || actor.AgentRef != "agent/lane:implement" || actor.SessionRef != "session/"+req.AttemptID || actor.ActorClass != ActorAgent || actor.ActorRef == "" || actor.ActorRef == f.actor.ActorRef {
		t.Fatalf("execution actor = %+v, want recorded lane identity, not the coordinator", actor)
	}
	if ref, err := WorkflowActorRef(actor); err != nil || ref != actor.ActorRef {
		t.Fatalf("lane actor does not match its recorded identity: %+v %v", actor, err)
	}
	if err := f.s.Transact(context.Background(), func(tx *Transaction) error {
		inTx, err := ResolveWorkerControlTx(context.Background(), tx, req)
		if err != nil {
			return err
		}
		if !oracleEntryIdentical(inTx, plan) {
			t.Fatalf("transaction-bound resolution differs: %+v", inTx)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var eventsAfter, leasesAfter int
	if err := f.s.db.QueryRow(`SELECT count(*) FROM domain_events`).Scan(&eventsAfter); err != nil {
		t.Fatal(err)
	}
	if err := f.s.db.QueryRow(`SELECT count(*) FROM worktree_verify_leases`).Scan(&leasesAfter); err != nil {
		t.Fatal(err)
	}
	if eventsBefore != eventsAfter || leasesBefore != leasesAfter {
		t.Fatalf("resolution changed events or leases: %d/%d -> %d/%d", eventsBefore, leasesBefore, eventsAfter, leasesAfter)
	}
	f.assertNoLaunch(t)
}

func TestResolveWorkerControlRefusesUnboundSelectors(t *testing.T) {
	f, original := workerControlFixture(t, true)
	for name, change := range map[string]func(*WorkerControlRequest){
		"missing work":       func(r *WorkerControlRequest) { r.WorkID = "" },
		"foreign work":       func(r *WorkerControlRequest) { r.WorkID = "another-work" },
		"missing project":    func(r *WorkerControlRequest) { r.ProjectID = "" },
		"foreign project":    func(r *WorkerControlRequest) { r.ProjectID = "another-project" },
		"foreign attempt":    func(r *WorkerControlRequest) { r.AttemptID = f.b.AttemptID },
		"foreign worktree":   func(r *WorkerControlRequest) { r.WorktreeIdentity = "sha256:" + strings.Repeat("f", 64) },
		"missing control":    func(r *WorkerControlRequest) { r.ControlID = "" },
		"unrecorded control": func(r *WorkerControlRequest) { r.ControlID = "go test ./..." },
		"changed packet":     func(r *WorkerControlRequest) { r.Packet = json.RawMessage(`{"work_id":"foreign"}`) },
	} {
		t.Run(name, func(t *testing.T) {
			req := original
			change(&req)
			plan, err := f.s.ResolveWorkerControl(context.Background(), req)
			if err == nil || !oracleEntryIdentical(plan, WorkerControlPlan{}) {
				t.Fatalf("unbound selector returned a plan: %+v %v", plan, err)
			}
		})
	}
	f.assertNoLaunch(t)
}

func TestResolveWorkerControlRequiresRecordedLaneDispatch(t *testing.T) {
	f, req := workerControlFixture(t, false)
	plan, err := f.s.ResolveWorkerControl(context.Background(), req)
	if !hasFailureKind(err, KindUnauthorizedDispatch) || !oracleEntryIdentical(plan, WorkerControlPlan{}) {
		t.Fatalf("undispatched attempt returned a plan: %+v %v", plan, err)
	}
	f.assertNoLaunch(t)
}

func TestResolveWorkerControlRefusesRetiredAttempt(t *testing.T) {
	f, req := workerControlFixture(t, true)
	if err := f.s.Transact(context.Background(), func(tx *Transaction) error {
		mustTxExec(t, tx, `INSERT INTO fold_guard(active) VALUES(1)`)
		mustTxExec(t, tx, `UPDATE worker_attempts SET lifecycle_state='failed',failed_at='2030-01-01T00:00:00Z',failure_kind='abandoned' WHERE work_id=? AND attempt_id=?`, req.WorkID, req.AttemptID)
		mustTxExec(t, tx, `DELETE FROM fold_guard`)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := f.s.ResolveWorkerControl(context.Background(), req)
	if !hasFailureKind(err, KindUnauthorizedDispatch) || !oracleEntryIdentical(plan, WorkerControlPlan{}) {
		t.Fatalf("retired attempt returned a plan: %+v %v", plan, err)
	}
	f.assertNoLaunch(t)
}
