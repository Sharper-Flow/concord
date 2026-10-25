package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
)

func workerTerminalCurrentDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`
CREATE TABLE domain_events(seq INTEGER PRIMARY KEY,event_id TEXT,subject_type TEXT,subject_id TEXT,kind TEXT,payload TEXT,occurred_at TEXT);
CREATE TABLE worker_attempts(work_id TEXT,attempt_id TEXT,terminal_kind TEXT,terminal_event_id TEXT,terminal_payload TEXT,terminal_payload_version INTEGER,terminal_seq INTEGER,terminal_occurred_at TEXT);
INSERT INTO domain_events VALUES(10,'completed','work_item','work','worker.completed','{"attempt_id":"attempt"}','2026-01-01T00:00:00Z');
INSERT INTO worker_attempts VALUES('work','attempt','worker.completed','completed','{"attempt_id":"attempt"}',1,10,'2026-01-01T00:00:00Z');
ALTER TABLE domain_events RENAME TO unavailable_domain_events;`); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestWorkerCompletedTerminalSourceWithoutLog(t *testing.T) {
	ctx := context.Background()
	db := workerTerminalCurrentDB(t)
	for _, tc := range []struct {
		name, work, event string
		before            int64
		found             bool
	}{
		{name: "current", work: "work", event: "completed", found: true},
		{name: "earlier source", work: "work", event: "completed", before: 11, found: true},
		{name: "equal sequence", work: "work", event: "completed", before: 10},
		{name: "future source", work: "work", event: "completed", before: 9},
		{name: "wrong work", work: "other", event: "completed"},
		{name: "missing source", work: "work", event: "absent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, attempt, found, err := workerFailureCompletedEventSource(ctx, db, tc.work, tc.event, tc.before)
			if err != nil || found != tc.found {
				t.Fatalf("source = (%q,%q,%v,%v), want found=%v", payload, attempt, found, err, tc.found)
			}
			if found && (payload != `{"attempt_id":"attempt"}` || attempt != "attempt") {
				t.Fatalf("source bytes/identity = (%q,%q)", payload, attempt)
			}
		})
	}
	source := WorkerFailureSource{Kind: WorkerFailureSourceResult, Locator: "completed", Digest: workerFailurePayloadDigest(`{"attempt_id":"attempt"}`)}
	if err := verifyWorkerFailureReviewResultSourceTx(ctx, db, "work", "attempt", source, 11); err != nil {
		t.Fatal(err)
	}
	if err := verifyWorkerFailureReviewResultSourceTx(ctx, db, "work", "other", source, 11); err == nil {
		t.Fatal("wrong attempt admitted")
	}
	source.Digest = testDigestC
	if err := verifyWorkerFailureReviewResultSourceTx(ctx, db, "work", "attempt", source, 11); err == nil {
		t.Fatal("wrong payload digest admitted")
	}
	if _, err := db.Exec(`UPDATE worker_attempts SET terminal_payload='malformed'`); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := workerFailureCompletedEventSource(ctx, db, "work", "completed", 0); err == nil {
		t.Fatal("malformed terminal source admitted")
	}
}

func TestDerivedFailureTerminalReferenceWithoutLog(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, eventID, ref, attempt, work, kind, raw string
		before                                       int64
		wantKind                                     FailureKind
	}{
		{name: "exact source", before: 11},
		{name: "unsequenced record", wantKind: KindMissingEvidence},
		{name: "equal sequence", before: 10, wantKind: KindMissingEvidence},
		{name: "future source", before: 9, wantKind: KindMissingEvidence},
		{name: "missing source", ref: "absent", eventID: "absent:workflow-failure", before: 11, wantKind: KindMissingEvidence},
		{name: "wrong work", work: "other", before: 11, wantKind: KindMissingEvidence},
		{name: "wrong kind", kind: WorkerCompleted, before: 11, wantKind: KindMissingEvidence},
		{name: "wrong attempt", attempt: "other", before: 11, wantKind: KindInvalidPayload},
		{name: "unpaired identity", eventID: "authored", before: 11, wantKind: KindInvalidPayload},
		{name: "different reference", ref: "different", before: 11, wantKind: KindInvalidPayload},
		{name: "malformed attribution", raw: `{"attempt_id":"attempt","failure_attribution":{"unexpected":true}}`, before: 11, wantKind: KindInvalidPayload},
		{name: "malformed source", raw: `malformed`, before: 11, wantKind: KindUnavailable},
		{name: "mismatched owner", raw: `{"attempt_id":"other"}`, before: 11, wantKind: KindUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := workerTerminalCurrentDB(t)
			kind, raw := WorkerFailed, `{"attempt_id":"attempt"}`
			if tc.kind != "" {
				kind = tc.kind
			}
			if tc.raw != "" {
				raw = tc.raw
			}
			if _, err := db.Exec(`UPDATE worker_attempts SET terminal_kind=?,terminal_event_id='failed',terminal_payload=?`, kind, raw); err != nil {
				t.Fatal(err)
			}
			event := Event{EventID: "failed:workflow-failure", SubjectID: "work", Seq: tc.before}
			p := workflowActionCompletedPayload{WorkerAttemptID: "attempt", FailureAttributionRef: "failed"}
			if tc.eventID != "" {
				event.EventID = tc.eventID
			}
			if tc.ref != "" {
				p.FailureAttributionRef = tc.ref
			}
			if tc.attempt != "" {
				p.WorkerAttemptID = tc.attempt
			}
			if tc.work != "" {
				event.SubjectID = tc.work
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			err = verifyDerivedFailureAttributionReferenceTx(ctx, tx, event, p)
			if tc.wantKind == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var failure *Failure
				if !failureAs(err, &failure) || failure.Kind != tc.wantKind {
					t.Fatalf("error = %v, want %s", err, tc.wantKind)
				}
			}
		})
	}
}

func TestDerivedFailureTerminalPairWithoutLog(t *testing.T) {
	const work = "terminal-pair-current"
	f := seedWorkflowReturnRouteFixture(t, work, "workflow.break_fix", "repair")
	s := f.store
	defer s.Close()
	lane := reviewGateLane(t, "implementation")
	attempt := "attempt:" + work
	dispatchJobBoundAttempt(t, s, work, "repair", attempt, nil, f.owner, 0, "terminal-pair-dispatch", &WorkerJobBinding{JobID: "job:" + work})
	if err := s2WorkerFailedLive(t, s, attributionWorkerFailedEvent(work, attempt, "terminal-pair-failed", preferredModelForLane(lane), attributionHostObservation())); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var raw string
	var event Event
	event.SubjectID = work
	if err := s.db.QueryRow(`SELECT event_id,seq,payload FROM worker_attempt_dispositions WHERE work_id=? AND event_id='terminal-pair-failed:workflow-failure' AND action_id='record_worker_failure'`, work).Scan(&event.EventID, &event.Seq, &raw); err != nil {
		t.Fatal(err)
	}
	var p workflowActionCompletedPayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	assertEventLogAbsent(t, s)
	if err := s.Transact(ctx, func(transaction *Transaction) error {
		tx := transaction.tx
		definition, err := VerifyWorkflowInstanceDefinitionTx(ctx, tx, BuiltinWorkflowRegistry(), work)
		if err != nil {
			return err
		}
		admitted, err := workflowDerivedWorkerFailurePairAdmitsFold(ctx, tx, definition.Definition, event, p)
		if err != nil || !admitted {
			t.Fatalf("pair = (%v,%v), want admitted", admitted, err)
		}
		for _, tc := range []struct {
			name   string
			event  Event
			modify func(*workflowActionCompletedPayload)
		}{
			{name: "unpaired identity", event: Event{EventID: "authored", SubjectID: work, Seq: event.Seq}},
			{name: "missing terminal", event: Event{EventID: "absent:workflow-failure", SubjectID: work, Seq: event.Seq}},
			{name: "wrong work", event: Event{EventID: event.EventID, SubjectID: "other", Seq: event.Seq}},
			{name: "future terminal", event: Event{EventID: event.EventID, SubjectID: work, Seq: 1}},
			{name: "unsequenced record", event: Event{EventID: event.EventID, SubjectID: work}},
			{name: "wrong attempt", event: event, modify: func(p *workflowActionCompletedPayload) { p.WorkerAttemptID = "other" }},
			{name: "wrong step", event: event, modify: func(p *workflowActionCompletedPayload) { p.StepID = "other" }},
			{name: "wrong epoch", event: event, modify: func(p *workflowActionCompletedPayload) { p.AttemptEpoch++ }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				candidate := p
				if tc.modify != nil {
					tc.modify(&candidate)
				}
				admitted, err := workflowDerivedWorkerFailurePairAdmitsFold(ctx, tx, definition.Definition, tc.event, candidate)
				if err != nil || admitted {
					t.Fatalf("pair = (%v,%v), want refused without error", admitted, err)
				}
			})
		}
		return verifyDerivedFailureAttributionReferenceTx(ctx, tx, event, p)
	}); err != nil {
		t.Fatal(err)
	}
}
