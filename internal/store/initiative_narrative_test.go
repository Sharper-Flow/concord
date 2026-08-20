package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func seedInitiativeForNarrative(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	events := []Event{
		{EventID: "p", Kind: "product.created", SubjectType: SubjectProduct, SubjectID: "p", Actor: "test", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1, Payload: []byte(`{"display_name":"P","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "pr", Kind: "project.created", SubjectType: SubjectProject, SubjectID: "pr", Actor: "test", OccurredAt: time.Unix(1, 1).UTC(), PayloadVersion: 1, Payload: []byte(`{"display_name":"PR"}`)},
		{EventID: "pp", Kind: "product_project.added", SubjectType: SubjectProduct, SubjectID: "p", Actor: "test", OccurredAt: time.Unix(1, 2).UTC(), PayloadVersion: 1, Payload: []byte(`{"product_id":"p","project_id":"pr","role":"primary","reason":"test","expected_version":1,"resulting_version":2}`)},
		{EventID: "initiative", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "initiative", Actor: "test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 2, Payload: mustJSONBytes(map[string]any{"work_kind": "initiative", "title": "Initiative", "priority": 1})},
		{EventID: "task", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "task", Actor: "test", OccurredAt: time.Unix(2, 1).UTC(), PayloadVersion: 2, Payload: mustJSONBytes(map[string]any{"work_kind": "task", "title": "Task", "priority": 1})},
		{EventID: "initiative-project", Kind: "work_project.added", SubjectType: SubjectWorkItem, SubjectID: "initiative", Actor: "test", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: mustJSONBytes(map[string]any{"work_id": "initiative", "project_id": "pr", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2})},
		{EventID: "task-project", Kind: "work_project.added", SubjectType: SubjectWorkItem, SubjectID: "task", Actor: "test", OccurredAt: time.Unix(3, 1).UTC(), PayloadVersion: 1, Payload: mustJSONBytes(map[string]any{"work_id": "task", "project_id": "pr", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2})},
	}
	expected := map[SubjectRef]int64{VersionRef(SubjectProduct, "p"): 0, VersionRef(SubjectProject, "pr"): 0, VersionRef(SubjectWorkItem, "initiative"): 0, VersionRef(SubjectWorkItem, "task"): 0}
	if err := ApplyOperation(ctx, s, Operation{Events: events, ExpectedVersions: expected}); err != nil {
		t.Fatal(err)
	}
}

func TestInitiativeNarrativeRevisedFoldAndAudit(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedInitiativeForNarrative(t, s)

	var initial string
	if err := s.DatabaseForTesting().QueryRow(`SELECT narrative FROM work_items WHERE id='initiative'`).Scan(&initial); err != nil {
		t.Fatal(err)
	}
	if initial != "" {
		t.Fatalf("initial narrative = %q, want empty", initial)
	}

	event, err := InitiativeNarrativeEvent("narrative-1", "initiative", "Seven entries: capture model at position 3; friction telemetry direction cancelled.", "entry set reordered", "operator", time.Unix(4, 0).UTC(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "initiative"): 2}}); err != nil {
		t.Fatal(err)
	}
	var narrative string
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT narrative, version FROM work_items WHERE id='initiative'`).Scan(&narrative, &version); err != nil {
		t.Fatal(err)
	}
	if narrative != "Seven entries: capture model at position 3; friction telemetry direction cancelled." || version != 3 {
		t.Fatalf("narrative=%q version=%d", narrative, version)
	}
	var actor, kind, payload string
	if err := s.DatabaseForTesting().QueryRow(`SELECT actor, kind, payload FROM domain_events WHERE event_id='narrative-1'`).Scan(&actor, &kind, &payload); err != nil {
		t.Fatal(err)
	}
	if actor != "operator" || kind != "initiative.narrative_revised" {
		t.Fatalf("audit event actor=%q kind=%q", actor, kind)
	}
	if !strings.Contains(payload, "entry set reordered") || !strings.Contains(payload, "capture model at position 3") {
		t.Fatalf("audit payload missing reason or narrative text: %s", payload)
	}

	second, err := InitiativeNarrativeEvent("narrative-2", "initiative", "Revised again after scope settled.", "second revision", "operator", time.Unix(4, 1).UTC(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{second}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "initiative"): 3}}); err != nil {
		t.Fatal(err)
	}
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT narrative, version FROM work_items WHERE id='initiative'`).Scan(&narrative, &version); err != nil {
		t.Fatal(err)
	}
	if narrative != "Revised again after scope settled." || version != 4 {
		t.Fatalf("rebuilt narrative=%q version=%d", narrative, version)
	}
}

func TestInitiativeNarrativeRejectsNonInitiative(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedInitiativeForNarrative(t, s)
	event, err := InitiativeNarrativeEvent("narrative-task", "task", "not an initiative", "wrong kind", "test", time.Unix(4, 0).UTC(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "task"): 2}}); err == nil {
		t.Fatal("narrative revision on non-Initiative work item succeeded")
	} else {
		assertFailureKind(t, err, KindInitiativeScopeViolation)
	}
}

func TestInitiativeNarrativeVersionFenceAndPayload(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedInitiativeForNarrative(t, s)

	stale, err := InitiativeNarrativeEvent("narrative-stale", "initiative", "stale write", "stale", "test", time.Unix(4, 0).UTC(), 9)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{stale}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "initiative"): 9}}); err == nil {
		t.Fatal("narrative revision with stale expected version succeeded")
	} else {
		assertFailureKind(t, err, KindVersionConflict)
	}

	for name, narrative := range map[string]string{
		"empty narrative": "",
		"oversize":        strings.Repeat("n", maxInitiativeNarrativeLength+1),
		"multibyte over":  strings.Repeat("界", maxInitiativeNarrativeLength+1),
	} {
		event, buildErr := InitiativeNarrativeEvent("narrative-bad", "initiative", narrative, "reason", "test", time.Unix(4, 1).UTC(), 2)
		if buildErr != nil {
			continue
		}
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "initiative"): 2}}); err == nil {
			t.Fatalf("%s accepted", name)
		} else {
			assertFailureKind(t, err, KindInvalidPayload)
		}
	}
	emptyReason, err := InitiativeNarrativeEvent("narrative-no-reason", "initiative", "text", "", "test", time.Unix(4, 2).UTC(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{emptyReason}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "initiative"): 2}}); err == nil {
		t.Fatal("empty revision reason accepted")
	} else {
		assertFailureKind(t, err, KindInvalidPayload)
	}

	multibyte := strings.Repeat("界", maxInitiativeNarrativeLength)
	event, err := InitiativeNarrativeEvent("narrative-multibyte", "initiative", multibyte, "boundary", "test", time.Unix(4, 3).UTC(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "initiative"): 2}}); err != nil {
		t.Fatalf("full-length multibyte narrative rejected: %v", err)
	}
}

func TestInitiativeNarrativeRevisionRefusesTerminalInitiative(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedInitiativeForNarrative(t, s)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{workTransitionEvent("cancel-initiative", "initiative", "needed", "cancelled", 2, 3)}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "initiative"): 2}}); err != nil {
		t.Fatal(err)
	}
	event, err := InitiativeNarrativeEvent("narrative-terminal", "initiative", "late narrative", "too late", "operator", time.Unix(5, 0).UTC(), 3)
	if err != nil {
		t.Fatal(err)
	}
	err = ApplyOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "initiative"): 3}})
	assertFailureKind(t, err, KindIllegalLifecycleTransition)
}
