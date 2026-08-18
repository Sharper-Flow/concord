package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// CD-0030: observations are durable, non-authoritative, visible at resume.

func observationFixture(t *testing.T) *Store {
	t.Helper()
	s := openTemp(t)
	ctx := context.Background()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{locatorProductEvent("p99"), locatorProjectEvent("pr99"), locatorMembershipEvent("p99", "pr99")}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "p99"): 0, VersionRef(SubjectProject, "pr99"): 0}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		{EventID: "w99-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "work-99", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"99","priority":1}`)},
		{EventID: "w99-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: "work-99", Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"pr99","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-99"): 0}}); err != nil {
		t.Fatal(err)
	}
	return s
}

func recordObservation(t *testing.T, s *Store, event Event) error {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := applyWorkflowOperationTx(ctx, tx, Operation{Events: []Event{event}}); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := leaveFold(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	return tx.Commit()
}

func TestObservationRecordsSurviveRebuildAndStayVisible(t *testing.T) {
	s := observationFixture(t)
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	payload, _ := json.Marshal(map[string]any{
		"observation_id": "obs:" + strings.Repeat("1", 16), "statement": "Five sibling provider services carry the same misalignment.",
		"refs": []string{"service:cards-a", "service:cards-b"}, "tags": []string{"systemic"},
	})
	if err := recordObservation(t, s, Event{EventID: "w99-obs-1", Kind: "work.observation_recorded", SubjectType: SubjectWorkItem, SubjectID: "work-99", Actor: "principal/agent", OccurredAt: now, PayloadVersion: 1, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	observations, err := s.ObservationsForWork(context.Background(), "work-99", 10)
	if err != nil || len(observations) != 1 {
		t.Fatalf("observations=%+v err=%v", observations, err)
	}
	if observations[0].Statement == "" || len(observations[0].Refs) != 2 || len(observations[0].Tags) != 1 {
		t.Fatalf("observation=%+v", observations[0])
	}
	// Duplicate id refuses.
	if err := recordObservation(t, s, Event{EventID: "w99-obs-2", Kind: "work.observation_recorded", SubjectType: SubjectWorkItem, SubjectID: "work-99", Actor: "principal/agent", OccurredAt: now, PayloadVersion: 1, Payload: payload}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate observation id must refuse: %v", err)
	}
}

func TestObservationRefusesTerminalWorkAndBadShapes(t *testing.T) {
	s := observationFixture(t)
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	// Oversized statement.
	long, _ := json.Marshal(map[string]any{"observation_id": "obs:" + strings.Repeat("2", 16), "statement": strings.Repeat("x", 513)})
	if err := recordObservation(t, s, Event{EventID: "w99-obs-long", Kind: "work.observation_recorded", SubjectType: SubjectWorkItem, SubjectID: "work-99", Actor: "principal/agent", OccurredAt: now, PayloadVersion: 1, Payload: long}); err == nil || !strings.Contains(err.Error(), "bounded") {
		t.Fatalf("oversized statement must refuse: %v", err)
	}
	// Terminal work stops recording.
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{{EventID: "w99-terminal", Kind: "work.transitioned", SubjectType: SubjectWorkItem, SubjectID: "work-99", Actor: "operator", OccurredAt: now, PayloadVersion: 1, Payload: json.RawMessage(`{"from":"needed","to":"completed","reason":"fixture","expected_version":2,"resulting_version":3}`)}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-99"): 2}}); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"observation_id": "obs:" + strings.Repeat("3", 16), "statement": "post-terminal"})
	if err := recordObservation(t, s, Event{EventID: "w99-obs-terminal", Kind: "work.observation_recorded", SubjectType: SubjectWorkItem, SubjectID: "work-99", Actor: "principal/agent", OccurredAt: now, PayloadVersion: 1, Payload: payload}); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("terminal work must refuse observations: %v", err)
	}
}
