package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func seedEpicForNarrative(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	events := []Event{
		{EventID: "p", Kind: "product.created", SubjectType: SubjectProduct, SubjectID: "p", Actor: "test", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1, Payload: []byte(`{"display_name":"P","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "pr", Kind: "project.created", SubjectType: SubjectProject, SubjectID: "pr", Actor: "test", OccurredAt: time.Unix(1, 1).UTC(), PayloadVersion: 1, Payload: []byte(`{"display_name":"PR"}`)},
		{EventID: "pp", Kind: "product_project.added", SubjectType: SubjectProduct, SubjectID: "p", Actor: "test", OccurredAt: time.Unix(1, 2).UTC(), PayloadVersion: 1, Payload: []byte(`{"product_id":"p","project_id":"pr","role":"primary","reason":"test","expected_version":1,"resulting_version":2}`)},
		{EventID: "epic", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "epic", Actor: "test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 2, Payload: mustJSONBytes(map[string]any{"work_kind": "epic", "title": "Epic", "priority": 1})},
		{EventID: "task", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "task", Actor: "test", OccurredAt: time.Unix(2, 1).UTC(), PayloadVersion: 2, Payload: mustJSONBytes(map[string]any{"work_kind": "task", "title": "Task", "priority": 1})},
		{EventID: "epic-project", Kind: "work_project.added", SubjectType: SubjectWorkItem, SubjectID: "epic", Actor: "test", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: mustJSONBytes(map[string]any{"work_id": "epic", "project_id": "pr", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2})},
		{EventID: "task-project", Kind: "work_project.added", SubjectType: SubjectWorkItem, SubjectID: "task", Actor: "test", OccurredAt: time.Unix(3, 1).UTC(), PayloadVersion: 1, Payload: mustJSONBytes(map[string]any{"work_id": "task", "project_id": "pr", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2})},
	}
	expected := map[SubjectRef]int64{VersionRef(SubjectProduct, "p"): 0, VersionRef(SubjectProject, "pr"): 0, VersionRef(SubjectWorkItem, "epic"): 0, VersionRef(SubjectWorkItem, "task"): 0}
	if err := ApplyOperation(ctx, s, Operation{Events: events, ExpectedVersions: expected}); err != nil {
		t.Fatal(err)
	}
}

func TestEpicNarrativeRevisedFoldAndAudit(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedEpicForNarrative(t, s)

	var initial string
	if err := s.DatabaseForTesting().QueryRow(`SELECT narrative FROM work_items WHERE id='epic'`).Scan(&initial); err != nil {
		t.Fatal(err)
	}
	if initial != "" {
		t.Fatalf("initial narrative = %q, want empty", initial)
	}

	event, err := EpicNarrativeEvent("narrative-1", "epic", "Seven entries: capture model at position 3; friction telemetry direction cancelled.", "entry set reordered", "operator", time.Unix(4, 0).UTC(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "epic"): 2}}); err != nil {
		t.Fatal(err)
	}
	var narrative string
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT narrative, version FROM work_items WHERE id='epic'`).Scan(&narrative, &version); err != nil {
		t.Fatal(err)
	}
	if narrative != "Seven entries: capture model at position 3; friction telemetry direction cancelled." || version != 3 {
		t.Fatalf("narrative=%q version=%d", narrative, version)
	}
	var actor, kind, payload string
	if err := s.DatabaseForTesting().QueryRow(`SELECT actor, kind, payload FROM domain_events WHERE event_id='narrative-1'`).Scan(&actor, &kind, &payload); err != nil {
		t.Fatal(err)
	}
	if actor != "operator" || kind != "epic.narrative_revised" {
		t.Fatalf("audit event actor=%q kind=%q", actor, kind)
	}
	if !strings.Contains(payload, "entry set reordered") || !strings.Contains(payload, "capture model at position 3") {
		t.Fatalf("audit payload missing reason or narrative text: %s", payload)
	}

	second, err := EpicNarrativeEvent("narrative-2", "epic", "Revised again after scope settled.", "second revision", "operator", time.Unix(4, 1).UTC(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{second}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "epic"): 3}}); err != nil {
		t.Fatal(err)
	}
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT narrative, version FROM work_items WHERE id='epic'`).Scan(&narrative, &version); err != nil {
		t.Fatal(err)
	}
	if narrative != "Revised again after scope settled." || version != 4 {
		t.Fatalf("rebuilt narrative=%q version=%d", narrative, version)
	}
}

func TestEpicNarrativeRejectsNonEpic(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedEpicForNarrative(t, s)
	event, err := EpicNarrativeEvent("narrative-task", "task", "not an initiative", "wrong kind", "test", time.Unix(4, 0).UTC(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "task"): 2}}); err == nil {
		t.Fatal("narrative revision on non-Epic work item succeeded")
	} else {
		assertFailureKind(t, err, KindEpicScopeViolation)
	}
}

func TestEpicNarrativeVersionFenceAndPayload(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedEpicForNarrative(t, s)

	stale, err := EpicNarrativeEvent("narrative-stale", "epic", "stale write", "stale", "test", time.Unix(4, 0).UTC(), 9)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{stale}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "epic"): 9}}); err == nil {
		t.Fatal("narrative revision with stale expected version succeeded")
	} else {
		assertFailureKind(t, err, KindVersionConflict)
	}

	for name, narrative := range map[string]string{
		"empty narrative": "",
		"oversize":        strings.Repeat("n", maxEpicNarrativeLength+1),
		"multibyte over":  strings.Repeat("界", maxEpicNarrativeLength+1),
	} {
		event, buildErr := EpicNarrativeEvent("narrative-bad", "epic", narrative, "reason", "test", time.Unix(4, 1).UTC(), 2)
		if buildErr != nil {
			continue
		}
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "epic"): 2}}); err == nil {
			t.Fatalf("%s accepted", name)
		} else {
			assertFailureKind(t, err, KindInvalidPayload)
		}
	}
	emptyReason, err := EpicNarrativeEvent("narrative-no-reason", "epic", "text", "", "test", time.Unix(4, 2).UTC(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{emptyReason}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "epic"): 2}}); err == nil {
		t.Fatal("empty revision reason accepted")
	} else {
		assertFailureKind(t, err, KindInvalidPayload)
	}

	// A full-length multibyte narrative is 3x the byte length yet valid: the
	// fold, the SQLite CHECK, and the JSON contract share character semantics.
	multibyte := strings.Repeat("界", maxEpicNarrativeLength)
	event, err := EpicNarrativeEvent("narrative-multibyte", "epic", multibyte, "boundary", "test", time.Unix(4, 3).UTC(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "epic"): 2}}); err != nil {
		t.Fatalf("full-length multibyte narrative rejected: %v", err)
	}
}
