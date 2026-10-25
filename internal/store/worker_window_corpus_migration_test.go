package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func workerWindowExplicitCorpus(t *testing.T) historicalFactCorpus {
	t.Helper()
	const work = "worker-window-corpus"
	base := seedWorkflowReturnRouteFixture(t, work, "workflow.break_fix", "repair")
	actor, err := WorkflowActorRef(base.owner)
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, base.store, work)
	lane := reviewGateLane(t, "implementation")
	c := historicalFactCorpus{}
	add := func(kind string, p map[string]any) {
		p["work_id"], p["step_id"], p["actor_ref"] = work, "repair", actor
		if kind == WorkerJobRecorded {
			delete(p, "step_id")
			delete(p, "actor_ref")
		}
		e := corpusEvent(fmt.Sprintf("window-corpus:%d", len(c.events)+1), kind, work, p)
		e.Actor = actor
		e.OccurredAt = time.Unix(int64(100-len(c.events)), 0).UTC()
		c.events = append(c.events, e)
	}
	add(WorkflowContractApproved, map[string]any{"contract_version": 1})
	for epoch := int64(1); epoch <= 3; epoch++ {
		action := "dispatch_worker"
		if epoch == 3 {
			action = "checkpoint_context"
		}
		add(WorkflowActionStarted, map[string]any{"expected_version": version, "resulting_version": version + 1, "attempt_epoch": epoch, "action_id": action, "accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": fmt.Sprintf("corpus-start:%d", epoch)})
		version++
		if epoch == 3 {
			break
		}
		add(WorkflowActionCompleted, map[string]any{"action_id": "dispatch_worker", "attempt_epoch": epoch, "worker_attempt_id": "attempt:success"})
	}
	for _, attempt := range []string{"attempt:success", "attempt:failed"} {
		add(WorkerDispatched, map[string]any{"attempt_id": attempt, "lane_actor_ref": actor, "packet_digest": "sha256:" + strings.Repeat("b", 64)})
		kind := WorkerCompleted
		if attempt == "attempt:failed" {
			kind = WorkerFailed
		}
		add(kind, map[string]any{"attempt_id": attempt, "readback_model": preferredModelForLane(lane)})
	}
	jobDigest := "sha256:" + strings.Repeat("c", 64)
	for range 2 {
		add(WorkerJobRecorded, map[string]any{"expected_version": version, "resulting_version": version + 1, "job_id": "job:window", "revision": 1, "contract_version": 1, "digest": jobDigest, "objective": "Retain the complete descriptor", "stopping_condition": "The selected descriptor is preserved"})
		version++
	}
	for _, action := range []string{"accept_worker_result", "accept_worker_evidence", "reject_worker_result", "record_worker_failure", "record_worker_job"} {
		for n := range 2 {
			attempt := "attempt:success"
			if action == "record_worker_failure" {
				attempt = "attempt:failed"
			}
			p := map[string]any{"action_id": action, "attempt_epoch": 3, "worker_attempt_id": attempt, "result_evidence_refs": []string{fmt.Sprintf("evidence:%d", n)}}
			if action == "accept_worker_result" {
				p["worker_job"] = map[string]any{"job_id": "job:window", "revision": 1, "digest": jobDigest}
			}
			add(WorkflowActionCompleted, p)
		}
	}
	for _, kind := range []string{"work.message_sent", "work.message_withdrawn", "workflow.overlap_resolved"} {
		add(kind, map[string]any{})
		c.events[len(c.events)-1].OccurredAt = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	c.parents = func(t *testing.T, s *Store, guard string) {
		copyFixtureDataToPrefix(t, base.store, s.db, []string{"products", "projects", "product_projects", "work_items", "work_projects", "workflow_actors", "workflow_instances", "workflow_contracts"}, map[string]map[string]string{
			"work_items":         {"effect_seq": "0", "last_progress_at": "''", "transition_from": "''", "transition_reason": "''", "transition_actor": "''", "transition_evidence_refs": "'[]'", "transition_seq": "0", "transition_version": "1"},
			"workflow_instances": {"definition_version": "26", "result_disposition": "''", "latest_non_dispatch_start_seq": "0", "completion_payload": "NULL", "completion_occurred_at": "NULL", "unhealthy_verdict_seq": "0"},
			"workflow_contracts": {"definition_version": "26", "supersession_seq": "0", "last_healthy_seq": "0"},
		}, guard)
		if _, err := s.db.Exec(`INSERT INTO ` + guard + ` VALUES(1)`); err != nil {
			t.Fatal(err)
		}
		for _, attempt := range []string{"attempt:success", "attempt:failed"} {
			columns := "work_id,attempt_id,lane_id,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,dispatched_at"
			values := "?,?,?,?,?,'','2.0','2.0','dispatched','2026-01-01T00:00:00Z'"
			args := []any{work, attempt, lane.ID, lane.Digest, lane.CapabilityClass}
			if guard == "fold_guard" {
				columns += ",lane_version"
				values += ",19"
			}
			if _, err := s.db.Exec(`INSERT INTO worker_attempts(`+columns+`) VALUES(`+values+`)`, args...); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.db.Exec(`INSERT INTO worker_job_revisions(work_id,job_id,revision,contract_version,objective,stopping_condition,path_scope,predicate_ids,checks,prerequisites,unresolved_refs,digest,state,recorded_at) VALUES(?,'job:window',1,1,'Retain the complete descriptor','The selected descriptor is preserved','[]','[]','[]','[]','[]',?,'recorded','2026-01-01T00:00:00Z')`, work, jobDigest); err != nil {
			t.Fatal(err)
		}
		if guard == "fold_guard" {
			// Existing job state is an input to the window migration. Its exact
			// satisfaction source is authored above, not recovered from an output.
			var lastAcceptance string
			for _, e := range c.events {
				var p workflowActionCompletedPayload
				_ = json.Unmarshal(e.Payload, &p)
				if e.Kind == WorkflowActionCompleted && p.ActionID == "accept_worker_result" {
					lastAcceptance = e.EventID
				}
			}
			if _, err := s.db.Exec(`UPDATE worker_job_revisions SET satisfied_result_ref=? WHERE work_id=?`, lastAcceptance, work); err != nil {
				t.Fatal(err)
			}
			// 143 consumes the retained 139 descriptors, not a reconstructed schema.
			var v int
			if err := s.db.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&v); err != nil {
				t.Fatal(err)
			}
			if v >= 139 {
				for i, e := range c.events {
					var p workflowActionCompletedPayload
					_ = json.Unmarshal(e.Payload, &p)
					if e.Kind == WorkflowActionCompleted && p.ActionID == "dispatch_worker" {
						if _, err := s.db.Exec(`INSERT INTO worker_authorizations(work_id,attempt_id,seq,payload) VALUES(?,?,?,?) ON CONFLICT(work_id,attempt_id) DO UPDATE SET seq=excluded.seq,payload=excluded.payload`, work, p.WorkerAttemptID, i+1, string(e.Payload)); err != nil {
							t.Fatal(err)
						}
					}
					if e.Kind == WorkerCompleted || e.Kind == WorkerFailed {
						var p struct {
							Attempt string `json:"attempt_id"`
						}
						_ = json.Unmarshal(e.Payload, &p)
						if _, err := s.db.Exec(`UPDATE worker_attempts SET terminal_event_id=? WHERE work_id=? AND attempt_id=?`, e.EventID, work, p.Attempt); err != nil {
							t.Fatal(err)
						}
					}
					if e.Kind == WorkerJobRecorded {
						if _, err := s.db.Exec(`UPDATE worker_job_revisions SET recorded_payload=? WHERE work_id=?`, string(e.Payload), work); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
		}
		if _, err := s.db.Exec(`DELETE FROM ` + guard); err != nil {
			t.Fatal(err)
		}
	}
	c.write = func(ctx context.Context, tx *sql.Tx, e Event) error {
		switch e.Kind {
		case WorkflowActionStarted:
			return foldWorkflowActionStarted(ctx, tx, e, nil)
		case WorkflowActionCompleted:
			var p workflowActionCompletedPayload
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return err
			}
			if err := writeWorkerAuthorizationTx(ctx, tx, e, p); err != nil {
				return err
			}
			if p.ActionID == "accept_worker_result" {
				return markWorkerJobSatisfiedTx(ctx, tx, e, p.WorkerJob)
			}
		case WorkerDispatched, WorkerCompleted, WorkerFailed:
			var p struct {
				Attempt string `json:"attempt_id"`
			}
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return err
			}
			if e.Kind == WorkerDispatched {
				return writeWorkerDispatchFactsTx(ctx, tx, e, p.Attempt)
			}
			return writeWorkerTerminalFactsTx(ctx, tx, e, p.Attempt)
		case WorkerJobRecorded:
			return foldWorkerJobRecorded(ctx, tx, e)
		}
		return nil
	}
	return c
}

func assertWorkerWindowCorpus143(t *testing.T) {
	t.Helper()
	c := workerWindowExplicitCorpus(t)
	live := c.current(t)
	old := c.prefix(t, 142)
	queries := []string{
		`SELECT json_array(id,effect_seq,last_progress_at) FROM work_items ORDER BY id`,
		`SELECT json_array(work_id,attempt_id,event_id,seq,start_seq,start_payload,contract_version) FROM worker_authorizations ORDER BY work_id,attempt_id`,
		`SELECT json_array(work_id,attempt_id,terminal_seq,terminal_occurred_at,failure_recorded_seq) FROM worker_attempts ORDER BY work_id,attempt_id`,
		`SELECT json_array(work_id,job_id,revision,recorded_seq,recorded_event_id,satisfied_seq) FROM worker_job_revisions ORDER BY work_id,job_id,revision`,
		`SELECT json_array(work_id,attempt_id,action_id,seq,event_id,payload,first_acceptance_seq,first_acceptance_payload) FROM worker_attempt_dispositions ORDER BY work_id,attempt_id,action_id`,
		`SELECT json_array(work_id,latest_non_dispatch_start_seq) FROM workflow_instances ORDER BY work_id`,
	}
	want := workerFactsQueryRows(t, live, queries)
	if err := Migrate(t.Context(), old.db); err != nil {
		t.Fatal(err)
	}
	if got := workerFactsQueryRows(t, old, queries); !reflect.DeepEqual(got, want) {
		for i := range queries {
			if !reflect.DeepEqual(got[i], want[i]) {
				t.Errorf("143 %s: got %v; want %v", queries[i], got[i], want[i])
			}
		}
	}
	assertCorpusSelectionRed(t, c, 143, "THEN first.seq ELSE 0 END", "THEN e.seq ELSE 0 END", queries, want)
}

func assertWorkerFactsCorpus139(t *testing.T) {
	t.Helper()
	c := workerWindowExplicitCorpus(t)
	live := c.current(t)
	queries := []string{
		`SELECT json_array(work_id,job_id,revision,recorded_payload) FROM worker_job_revisions ORDER BY work_id,job_id,revision`,
		`SELECT json_array(work_id,attempt_id,dispatch_payload,dispatch_event_id,dispatch_actor,dispatch_seq,dispatch_payload_version,terminal_payload,terminal_event_id,terminal_payload_version,terminal_kind,failure_recorded) FROM worker_attempts ORDER BY attempt_id`,
		`SELECT json_array(work_id,attempt_id,seq,payload) FROM worker_authorizations ORDER BY work_id,attempt_id`,
		`SELECT json_array(work_id,step_id,attempt_epoch) FROM workflow_step_epochs ORDER BY work_id,step_id`,
		`SELECT json_array(work_id,result_disposition) FROM workflow_instances ORDER BY work_id`,
	}
	want := workerFactsQueryRows(t, live, queries)
	old := c.prefix(t, 138)
	if err := Migrate(t.Context(), old.db); err != nil {
		t.Fatal(err)
	}
	if got := workerFactsQueryRows(t, old, queries); !reflect.DeepEqual(got, want) {
		t.Fatalf("139 retained facts=%v; current writers=%v", got, want)
	}
	assertCorpusSelectionRed(t, c, 139, "seq=(SELECT max(d.seq)", "seq=(SELECT min(d.seq)", queries, want)
	// Archived work owns neither an authorization nor a step epoch after upgrade.
	orphans := c
	orphans.events = append(append([]Event(nil), c.events...),
		corpusEvent("removed-auth", WorkflowActionCompleted, "removed-work", json.RawMessage(`{"action_id":"dispatch_worker","worker_attempt_id":"attempt:removed","step_id":"repair","attempt_epoch":1}`)),
		corpusEvent("removed-start", WorkflowActionStarted, "removed-work", json.RawMessage(`{"step_id":"repair","attempt_epoch":1}`)),
	)
	removed := orphans.prefix(t, 138)
	if err := Migrate(t.Context(), removed.db); err != nil {
		t.Fatal(err)
	}
	if got := workerFactsQueryRows(t, removed, queries); !reflect.DeepEqual(got, want) {
		t.Fatalf("removed source changed retained139 facts: %v; want %v", got, want)
	}
}
