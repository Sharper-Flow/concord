package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDecisionRecordCurrentBoundsMatchFold(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedStepWork(t, s, "work-decision-bounds")
	d := architectureSpikeDecisionBoundsV6()
	initializeStepWorkflow(t, s, "work-decision-bounds", d)
	fields := map[string]any{"question": "question", "options_considered": []string{strings.Repeat("é", 128)}, "decision": "accepted_decision", "rationale": "rationale", "consequences": []string{"consequence"}, "inputs": []string{"input"}, "poc_findings": "findings"}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateWorkflowActionPayload(d, "record_decision", raw); err != nil {
		t.Fatal(err)
	}
	envelope, _ := json.Marshal(map[string]any{"action_id": "record_decision", "fields": fields})
	if err := s.Transact(ctx, func(tx *Transaction) error {
		if err := enterFold(ctx, tx.tx); err != nil {
			return err
		}
		if err := foldWorkflowDecisionRecord(ctx, tx.tx, Event{SubjectID: "work-decision-bounds", OccurredAt: time.Unix(100, 0)}, envelope); err != nil {
			return err
		}
		return leaveFold(ctx, tx.tx)
	}); err != nil {
		t.Fatalf("schema-valid current payload refused by fold: %v", err)
	}
	for _, text := range []string{"x", strings.Repeat("x", 129)} {
		fields["options_considered"] = []string{text}
		raw, _ := json.Marshal(fields)
		if err := validateWorkflowActionPayload(d, "record_decision", raw); err == nil {
			t.Errorf("accepted out-of-bounds item length %d", len(text))
		}
	}
}

func TestNativeTimestampOptionalButValidatedWhenPresent(t *testing.T) {
	d := opsRunbookTimestampV7()
	fields := map[string]any{"run_id": "run-test", "native_subject_ref": "native:test", "status": "started", "evidence_ref": "artifact:test", "evidence_digest": "sha256:" + strings.Repeat("a", 64)}
	raw, _ := json.Marshal(fields)
	if err := validateWorkflowActionPayload(d, "start_run", raw); err != nil {
		t.Fatalf("omitted timestamp refused: %v", err)
	}
	for _, text := range []string{"invalid", "2026-02-30T00:00:00Z"} {
		fields["asserted_at"] = text
		raw, _ := json.Marshal(fields)
		if err := validateWorkflowActionPayload(d, "start_run", raw); err == nil {
			t.Errorf("accepted %q", text)
		}
	}
	fields["asserted_at"] = "2026-09-14T00:00:00.123456789+02:30"
	raw, _ = json.Marshal(fields)
	if err := validateWorkflowActionPayload(d, "start_run", raw); err != nil {
		t.Fatal(err)
	}
}

func TestNativeTimestampDefaultAndSkewBoundary(t *testing.T) {
	now := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	raw := json.RawMessage(`{"run_id":"run-ts","native_subject_ref":"native:test","status":"started","evidence_ref":"artifact:test","evidence_digest":"sha256:` + strings.Repeat("a", 64) + `"}`)
	events, err := workflowSemanticActionEvents(context.Background(), nil, opsRunbookTimestampV7(), WorkflowActionExecutionRequest{WorkID: "work-ts", OperationID: "op-ts", ActionID: "start_run", Actor: livenessActor(), Now: now}, "execute", "actor-ts", raw, 1, false)
	if err != nil || len(events) != 1 {
		t.Fatalf("native event: %v %+v", err, events)
	}
	var payload nativeRunPayload
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.AssertedAt != now.Format(time.RFC3339Nano) {
		t.Fatalf("wrong omission default: %s", payload.AssertedAt)
	}
	for _, extra := range []time.Duration{0, time.Nanosecond} {
		_, err := buildNativeRunEvent("event-ts", "work-ts", livenessActor(), now, 1, "start", "run-ts", "native:test", "started", "artifact:test", "sha256:"+strings.Repeat("a", 64), now.Add(nativeRunAssertedSkewBound+extra).Format(time.RFC3339Nano))
		if (extra == 0) != (err == nil) {
			t.Fatalf("skew boundary extra=%s err=%v", extra, err)
		}
	}
}

func TestHistoricalDecisionRecordShapesReplay(t *testing.T) {
	fields := map[string]any{"question": "question", "options_considered": []string{"option"}, "decision": "accepted_decision", "rationale": "rationale", "consequences": []string{"consequence"}, "inputs": []string{"input"}, "poc_findings": "findings"}
	for _, shape := range []string{"top", "checkpoint", "fields"} {
		t.Run(shape, func(t *testing.T) {
			ctx := context.Background()
			s := openTemp(t)
			workID := "work-history-" + shape
			seedStepWork(t, s, workID)
			initializeStepWorkflow(t, s, workID, releasedArchitectureSpikeV5())
			payload := map[string]any{}
			for key, value := range fields {
				payload[key] = value
			}
			payload["action_id"] = "record_decision"
			if shape == "checkpoint" {
				payload = map[string]any{"checkpoint": payload}
			}
			if shape == "fields" {
				payload = map[string]any{"action_id": "record_decision", "fields": fields}
			}
			raw, _ := json.Marshal(payload)
			if err := s.Transact(ctx, func(tx *Transaction) error {
				if err := enterFold(ctx, tx.tx); err != nil {
					return err
				}
				if err := foldWorkflowDecisionRecord(ctx, tx.tx, Event{SubjectID: workID, OccurredAt: time.Unix(100, 0)}, raw); err != nil {
					return err
				}
				return leaveFold(ctx, tx.tx)
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
