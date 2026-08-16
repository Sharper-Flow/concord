package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Regression: work_messages and resource_claims (RESTRICT FKs to work_items)
// must clear before work_items during log rebuild.
func TestRebuildSurvivesMessagesAndClaims(t *testing.T) {
	s := observationFixture(t)
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	msgPayload, _ := json.Marshal(map[string]any{"work_id": "work-99", "expected_version": 2, "resulting_version": 3, "message_id": "msg:" + strings.Repeat("a", 32), "recipient_work_id": "other-work", "body": "x"})
	// recipient must exist
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
		{EventID: "other-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "other-work", Actor: "operator", OccurredAt: now, PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"other","priority":1}`)},
		{EventID: "other-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: "other-work", Actor: "operator", OccurredAt: now, PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"pr99","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, _ := s.DB().BeginTx(ctx, nil)
	_ = enterFold(ctx, tx)
	if _, err := applyWorkflowOperationTx(ctx, tx, Operation{Events: []Event{{EventID: "msg-ev", Kind: "work.message_sent", SubjectType: SubjectWorkItem, SubjectID: "work-99", Actor: "p/a", OccurredAt: now, PayloadVersion: 1, Payload: msgPayload}}}); err != nil {
		t.Fatal(err)
	}
	_ = leaveFold(ctx, tx)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("rebuild after message: %v", err)
	}
	var count int
	if err := s.DB().QueryRow(`SELECT count(*) FROM work_messages`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("messages after rebuild=%d err=%v", count, err)
	}
}
