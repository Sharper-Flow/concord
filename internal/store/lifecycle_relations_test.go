package store

import (
	"context"
	"fmt"
	"testing"
)

func workCreatedEvent(id, eventID string) Event {
	event := operationEvent(eventID, "work.created", SubjectWorkItem, id, map[string]any{
		"work_kind": "task", "title": id, "priority": 10,
	})
	event.PayloadVersion = 2
	return event
}

func workTransitionEvent(eventID, id, from, to string, expected, resulting int64) Event {
	return operationEvent(eventID, "work.transitioned", SubjectWorkItem, id, map[string]any{
		"from": from, "to": to, "reason": "test", "expected_version": expected, "resulting_version": resulting,
	})
}

func workReopenedEvent(eventID, id, from string, expected, resulting int64) Event {
	return operationEvent(eventID, "work.reopened", SubjectWorkItem, id, map[string]any{
		"from": from, "reason": "test", "expected_version": expected, "resulting_version": resulting,
	})
}

func workSupersededEvent(eventID, successor, predecessor string, expected, resulting int64) Event {
	return operationEvent(eventID, "work.superseded", SubjectWorkItem, predecessor, map[string]any{
		"successor": successor, "superseded": predecessor, "reason": "test", "expected_version": expected, "resulting_version": resulting,
	})
}

func relationAddedEvent(eventID, kind, from, to string, expected, resulting int64) Event {
	return operationEvent(eventID, "relation.added", SubjectWorkItem, from, map[string]any{
		"from": from, "to": to, "kind": kind, "reason": "test", "expected_version": expected, "resulting_version": resulting,
	})
}

func relationRemovedEvent(eventID, kind, from, to string, expected, resulting int64) Event {
	return operationEvent(eventID, "relation.removed", SubjectWorkItem, from, map[string]any{
		"from": from, "to": to, "kind": kind, "reason": "test", "expected_version": expected, "resulting_version": resulting,
	})
}

func workVersion(id string, version int64) map[SubjectRef]int64 {
	return map[SubjectRef]int64{VersionRef(SubjectWorkItem, id): version}
}

func applyWorkEvent(t *testing.T, s *Store, event Event, expected map[SubjectRef]int64) error {
	t.Helper()
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: expected})
	assertFoldGuardEmpty(t, s)
	return err
}

func seedWork(t *testing.T, s *Store, id string) {
	t.Helper()
	var projectCount int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM projects`).Scan(&projectCount); err != nil {
		t.Fatal(err)
	}
	if projectCount == 0 {
		if err := ApplyOperation(context.Background(), s, Operation{
			Events: []Event{
				productCreatedEvent("product", "create-product"),
				projectCreatedEvent("project", "create-project"),
				operationEvent("product-project", "product_project.added", SubjectProduct, "product", map[string]any{
					"product_id": "product", "project_id": "project", "role": "primary", "reason": "test",
					"expected_version": 1, "resulting_version": 2,
				}),
			},
			ExpectedVersions: map[SubjectRef]int64{
				VersionRef(SubjectProduct, "product"): 0,
				VersionRef(SubjectProject, "project"): 0,
			},
		}); err != nil {
			t.Fatalf("create test Project: %v", err)
		}
	}
	if err := ApplyOperation(context.Background(), s, Operation{
		Events: []Event{
			workCreatedEvent(id, "create-"+id),
			operationEvent("membership-"+id, "work_project.added", SubjectWorkItem, id, map[string]any{
				"work_id": id, "project_id": "project", "role": "secondary", "reason": "test",
				"expected_version": 1, "resulting_version": 2,
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, id): 0},
	}); err != nil {
		t.Fatalf("create work %s: %v", id, err)
	}
}

func seedState(t *testing.T, s *Store, id, state string) int64 {
	t.Helper()
	seedWork(t, s, id)
	switch state {
	case "needed":
		return 2
	case "in_progress":
		if err := applyWorkEvent(t, s, workTransitionEvent("start-"+id, id, "needed", "in_progress", 2, 3), workVersion(id, 2)); err != nil {
			t.Fatalf("start work %s: %v", id, err)
		}
		return 3
	case "completed", "cancelled":
		if err := applyWorkEvent(t, s, workTransitionEvent("finish-"+id, id, "needed", state, 2, 3), workVersion(id, 2)); err != nil {
			t.Fatalf("finish work %s: %v", id, err)
		}
		return 3
	case "superseded":
		seedWork(t, s, "successor-"+id)
		if err := applyWorkEvent(t, s, workSupersededEvent("supersede-"+id, "successor-"+id, id, 2, 3), workVersion(id, 2)); err != nil {
			t.Fatalf("supersede work %s: %v", id, err)
		}
		return 3
	default:
		t.Fatalf("unknown test state %q", state)
		return 0
	}
}

func TestLifecycleTransitionsAreClosedAndTyped(t *testing.T) {
	states := []string{"needed", "in_progress", "completed", "cancelled", "superseded"}
	allowedDirect := map[string]map[string]bool{
		"needed":      {"in_progress": true, "completed": true, "cancelled": true},
		"in_progress": {"needed": true, "completed": true, "cancelled": true},
		"completed":   {},
		"cancelled":   {},
		"superseded":  {},
	}

	for _, from := range states {
		for _, to := range states {
			name := fmt.Sprintf("%s-to-%s", from, to)
			t.Run(name, func(t *testing.T) {
				s := openTemp(t)
				version := seedState(t, s, "work", from)
				err := applyWorkEvent(t, s, workTransitionEvent("transition", "work", from, to, version, version+1), workVersion("work", version))
				if allowedDirect[from][to] {
					if err != nil {
						t.Fatalf("legal transition failed: %v", err)
					}
					return
				}
				assertFailureKind(t, err, KindIllegalLifecycleTransition)
			})
		}
	}

	for _, tc := range []struct {
		name string
		from string
	}{
		{"completed-reopen", "completed"},
		{"cancelled-reopen", "cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTemp(t)
			version := seedState(t, s, "work", tc.from)
			if err := applyWorkEvent(t, s, workReopenedEvent("reopen", "work", tc.from, version, version+1), workVersion("work", version)); err != nil {
				t.Fatalf("legal reopen failed: %v", err)
			}
			var lifecycle, terminalTime string
			if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle, coalesce(terminal_time, '') FROM work_items WHERE id = 'work'`).Scan(&lifecycle, &terminalTime); err != nil {
				t.Fatal(err)
			}
			if lifecycle != "needed" || terminalTime != "" {
				t.Fatalf("reopened projection = lifecycle %q, terminal_time %q; want needed and empty terminal time", lifecycle, terminalTime)
			}
		})
	}
}

func TestWorkLifecycleRejectsStaleExpectedVersionWithoutMutation(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "work")
	err := applyWorkEvent(t, s, workTransitionEvent("stale", "work", "needed", "in_progress", 1, 2), workVersion("work", 0))
	assertFailureKind(t, err, KindVersionConflict)
	assertTableCount(t, s, "domain_events", 5)
	var lifecycle string
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle, version FROM work_items WHERE id = 'work'`).Scan(&lifecycle, &version); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "needed" || version != 2 {
		t.Fatalf("stale lifecycle mutation = %q version %d, want needed version 2", lifecycle, version)
	}
}

func TestPM4EvidenceFieldsRequireReasons(t *testing.T) {
	t.Run("supersession", func(t *testing.T) {
		s := openTemp(t)
		seedWork(t, s, "a")
		seedWork(t, s, "b")
		before := fullPM4Snapshot(t, s)
		err := applyWorkEvent(t, s, operationEvent("missing-reason", "work.superseded", SubjectWorkItem, "b", map[string]any{
			"successor": "a", "superseded": "b", "reason": "", "expected_version": 1, "resulting_version": 2,
		}), workVersion("b", 2))
		assertFailureKind(t, err, KindInvalidPayload)
		if got := fullPM4Snapshot(t, s); got != before {
			t.Fatalf("missing supersession reason mutated projection =\n%s\nwant\n%s", got, before)
		}
	})

	t.Run("reopen from superseded", func(t *testing.T) {
		s := openTemp(t)
		seedState(t, s, "b", "superseded")
		before := fullPM4Snapshot(t, s)
		err := applyWorkEvent(t, s, operationEvent("missing-reason", "work.reopened_from_superseded", SubjectWorkItem, "b", map[string]any{
			"superseded": "b", "replacement_successor": "replacement", "reason": "", "expected_version": 2, "resulting_version": 3,
		}), workVersion("b", 3))
		assertFailureKind(t, err, KindInvalidPayload)
		if got := fullPM4Snapshot(t, s); got != before {
			t.Fatalf("missing reopen reason mutated projection =\n%s\nwant\n%s", got, before)
		}
	})

	t.Run("relation added", func(t *testing.T) {
		s := openTemp(t)
		seedWork(t, s, "a")
		seedWork(t, s, "b")
		before := fullPM4Snapshot(t, s)
		err := applyWorkEvent(t, s, operationEvent("missing-reason", "relation.added", SubjectWorkItem, "a", map[string]any{
			"from": "a", "to": "b", "kind": "blocks", "reason": "", "expected_version": 1, "resulting_version": 2,
		}), nil)
		assertFailureKind(t, err, KindInvalidPayload)
		if got := fullPM4Snapshot(t, s); got != before {
			t.Fatalf("missing relation-add reason mutated projection =\n%s\nwant\n%s", got, before)
		}
	})

	t.Run("relation removed", func(t *testing.T) {
		s := openTemp(t)
		seedWork(t, s, "a")
		seedWork(t, s, "b")
		if err := applyWorkEvent(t, s, relationAddedEvent("add", "blocks", "a", "b", 2, 3), nil); err != nil {
			t.Fatal(err)
		}
		before := fullPM4Snapshot(t, s)
		err := applyWorkEvent(t, s, operationEvent("missing-reason", "relation.removed", SubjectWorkItem, "a", map[string]any{
			"from": "a", "to": "b", "kind": "blocks", "reason": "", "expected_version": 2, "resulting_version": 3,
		}), nil)
		assertFailureKind(t, err, KindInvalidPayload)
		if got := fullPM4Snapshot(t, s); got != before {
			t.Fatalf("missing relation-remove reason mutated projection =\n%s\nwant\n%s", got, before)
		}
	})
}

func TestRelationAddedRejectsStaleExpectedVersionWithoutMutation(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "a")
	seedWork(t, s, "b")
	err := applyWorkEvent(t, s, relationAddedEvent("stale", "blocks", "a", "b", 0, 1), nil)
	assertFailureKind(t, err, KindVersionConflict)
	assertTableCount(t, s, "domain_events", 7)
	assertTableCount(t, s, "relations", 0)
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id = 'a'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("stale relation mutation version = %d, want 2", version)
	}
}

func TestRelationRemovedRejectsStaleExpectedVersionWithoutMutation(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "a")
	seedWork(t, s, "b")
	if err := applyWorkEvent(t, s, relationAddedEvent("add", "blocks", "a", "b", 2, 3), nil); err != nil {
		t.Fatal(err)
	}
	before := fullPM4Snapshot(t, s)
	err := applyWorkEvent(t, s, relationRemovedEvent("stale", "blocks", "a", "b", 2, 3), nil)
	assertFailureKind(t, err, KindVersionConflict)
	if got := fullPM4Snapshot(t, s); got != before {
		t.Fatalf("stale relation removal mutated projection =\n%s\nwant\n%s", got, before)
	}
}

func TestSupersessionIsAtomicAndRejectsCyclesAndSecondSuccessors(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "a")
	seedWork(t, s, "b")
	seedWork(t, s, "c")

	if err := applyWorkEvent(t, s, workSupersededEvent("a-b", "a", "b", 2, 3), workVersion("b", 2)); err != nil {
		t.Fatalf("supersession: %v", err)
	}
	var lifecycle string
	var relationCount int
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle FROM work_items WHERE id = 'b'`).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM relations WHERE work_id_from = 'a' AND work_id_to = 'b' AND kind = 'supersedes'`).Scan(&relationCount); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "superseded" || relationCount != 1 {
		t.Fatalf("atomic supersession = lifecycle %q, relation count %d", lifecycle, relationCount)
	}

	err := applyWorkEvent(t, s, workSupersededEvent("b-a", "b", "a", 2, 3), workVersion("a", 2))
	assertFailureKind(t, err, KindCycleDetected)
	assertTableCount(t, s, "domain_events", 10)
	assertTableCount(t, s, "relations", 1)

	err = applyWorkEvent(t, s, workSupersededEvent("c-b", "c", "b", 3, 4), workVersion("b", 3))
	assertFailureKind(t, err, KindSupersessionTargetAlreadySuperseded)
	assertTableCount(t, s, "domain_events", 10)
}

func TestSupersessionRejectsAlreadySupersededAndReopenRequiresCompositeEvent(t *testing.T) {
	s := openTemp(t)
	seedState(t, s, "b", "superseded")
	seedWork(t, s, "c")

	err := applyWorkEvent(t, s, workSupersededEvent("again", "c", "b", 3, 4), workVersion("b", 3))
	assertFailureKind(t, err, KindSupersessionTargetAlreadySuperseded)

	err = applyWorkEvent(t, s, workTransitionEvent("direct-reopen", "b", "superseded", "needed", 3, 4), workVersion("b", 3))
	assertFailureKind(t, err, KindIllegalLifecycleTransition)

	if err := applyWorkEvent(t, s, workReopenedFromSupersededEvent("composite-reopen", "b", "successor-b", 3, 4), workVersion("b", 3)); err != nil {
		t.Fatalf("composite reopen: %v", err)
	}
	var lifecycle string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle FROM work_items WHERE id = 'b'`).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "needed" {
		t.Fatalf("reopened lifecycle = %q, want needed", lifecycle)
	}
	assertTableCount(t, s, "relations", 0)
}

func workReopenedFromSupersededEvent(eventID, id, replacement string, expected, resulting int64) Event {
	return operationEvent(eventID, "work.reopened_from_superseded", SubjectWorkItem, id, map[string]any{
		"superseded": id, "replacement_successor": replacement, "reason": "test", "expected_version": expected, "resulting_version": resulting,
	})
}

func TestRelationKindsEnforceTheirOwnGraphRules(t *testing.T) {
	for _, kind := range []string{"parent", "blocks"} {
		t.Run(kind+" cycle", func(t *testing.T) {
			s := openTemp(t)
			for _, id := range []string{"a", "b", "c"} {
				seedWork(t, s, id)
			}
			for i, edge := range [][2]string{{"a", "b"}, {"b", "c"}} {
				if err := applyWorkEvent(t, s, relationAddedEvent(fmt.Sprintf("edge-%d", i), kind, edge[0], edge[1], 2, 3), nil); err != nil {
					t.Fatalf("add edge %v: %v", edge, err)
				}
			}
			assertFailureKind(t, applyWorkEvent(t, s, relationAddedEvent("cycle", kind, "c", "a", 2, 3), nil), KindCycleDetected)
			assertTableCount(t, s, "relations", 2)
		})
	}

	s := openTemp(t)
	seedWork(t, s, "a")
	seedWork(t, s, "b")
	for _, edge := range [][2]string{{"a", "b"}, {"b", "a"}} {
		if err := applyWorkEvent(t, s, relationAddedEvent(edge[0]+edge[1], "implements", edge[0], edge[1], 2, 3), nil); err != nil {
			t.Fatalf("implements edge %v: %v", edge, err)
		}
	}
	assertTableCount(t, s, "relations", 2)
}

func TestCycleCheckRejectsSimpleTwoNodeCycle(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "a")
	seedWork(t, s, "b")
	if err := applyWorkEvent(t, s, relationAddedEvent("a-b", "blocks", "a", "b", 2, 3), nil); err != nil {
		t.Fatal(err)
	}
	assertFailureKind(t, applyWorkEvent(t, s, relationAddedEvent("b-a", "blocks", "b", "a", 2, 3), nil), KindCycleDetected)
}

func TestRelationsRejectSelfDuplicateAndSupersedesContract(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "a")
	seedWork(t, s, "b")

	assertFailureKind(t, applyWorkEvent(t, s, relationAddedEvent("self", "parent", "a", "a", 2, 3), nil), KindRelationConflict)
	if err := applyWorkEvent(t, s, relationAddedEvent("ab", "parent", "a", "b", 2, 3), nil); err != nil {
		t.Fatal(err)
	}
	assertFailureKind(t, applyWorkEvent(t, s, relationAddedEvent("ab-again", "parent", "a", "b", 3, 4), nil), KindRelationConflict)
	for _, kind := range []string{"supersedes", "compatible_with", "merged_into"} {
		assertFailureKind(t, applyWorkEvent(t, s, relationAddedEvent("bad-"+kind, kind, "a", "b", 3, 4), nil), KindRelationContractViolation)
	}
}

func TestRebuildRestoresWorkAndRelationProjectionsByteForByte(t *testing.T) {
	s := openTemp(t)
	for _, id := range []string{"a", "b", "c"} {
		seedWork(t, s, id)
	}
	operations := []Operation{
		{Events: []Event{relationAddedEvent("parent", "parent", "a", "b", 2, 3)}},
		{Events: []Event{relationRemovedEvent("remove-parent", "parent", "a", "b", 3, 4)}},
		{Events: []Event{relationAddedEvent("parent-again", "parent", "a", "b", 4, 5)}},
		{Events: []Event{relationAddedEvent("blocks", "blocks", "b", "c", 2, 3)}},
		{Events: []Event{relationAddedEvent("implements", "implements", "c", "a", 2, 3)}},
		{Events: []Event{workSupersededEvent("supersede", "a", "c", 3, 4)}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "c"): 3}},
	}
	for _, operation := range operations {
		if err := ApplyOperation(context.Background(), s, operation); err != nil {
			t.Fatalf("ApplyOperation() error = %v", err)
		}
		assertFoldGuardEmpty(t, s)
	}
	want := fullPM4Snapshot(t, s)
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("RebuildFromLog() error = %v", err)
	}
	assertFoldGuardEmpty(t, s)
	if got := fullPM4Snapshot(t, s); got != want {
		t.Fatalf("PM4 snapshot after rebuild =\n%s\nwant\n%s", got, want)
	}
}

func TestRelationRemovalAndFoldGuard(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "a")
	seedWork(t, s, "b")
	if err := applyWorkEvent(t, s, relationAddedEvent("ab", "blocks", "a", "b", 2, 3), nil); err != nil {
		t.Fatal(err)
	}
	if err := applyWorkEvent(t, s, relationRemovedEvent("remove-ab", "blocks", "a", "b", 3, 4), nil); err != nil {
		t.Fatal(err)
	}
	assertTableCount(t, s, "relations", 0)
	assertFoldGuardEmpty(t, s)
	if err := applyWorkEvent(t, s, relationAddedEvent("ab-again", "blocks", "a", "b", 4, 5), nil); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		stmt string
	}{
		{"work insert", `INSERT INTO work_items (id, kind, title, lifecycle, priority, version, created_at, updated_at) VALUES ('x', 'task', 'x', 'needed', 1, 1, 'now', 'now')`},
		{"work update", `UPDATE work_items SET title = 'changed' WHERE id = 'a'`},
		{"work delete", `DELETE FROM work_items`},
		{"relation insert", `INSERT INTO relations (work_id_from, work_id_to, kind, created_at) VALUES ('a', 'b', 'parent', 'now')`},
		{"relation delete", `DELETE FROM relations`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.DatabaseForTesting().ExecContext(context.Background(), tc.stmt); err == nil {
				t.Fatalf("direct write succeeded: %s", tc.name)
			}
		})
	}
}

func fullPM4Snapshot(t *testing.T, s *Store) string {
	t.Helper()
	var out string
	for _, tc := range []struct {
		query string
		work  bool
	}{
		{`SELECT id, kind, title, lifecycle, priority, version, created_at, updated_at, coalesce(terminal_time, '') FROM work_items ORDER BY id`, true},
		{`SELECT id, work_id_from, work_id_to, kind, created_at FROM relations ORDER BY id`, false},
	} {
		rows, err := s.DatabaseForTesting().Query(tc.query)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var id, from, to, kind, title, lifecycle, createdAt, updatedAt, terminal string
			var priority, version int64
			if tc.work {
				if err := rows.Scan(&id, &kind, &title, &lifecycle, &priority, &version, &createdAt, &updatedAt, &terminal); err != nil {
					t.Fatal(err)
				}
				out += fmt.Sprintf("work|%s|%s|%s|%s|%d|%d|%s|%s|%s\n", id, kind, title, lifecycle, priority, version, createdAt, updatedAt, terminal)
			} else {
				if err := rows.Scan(&id, &from, &to, &kind, &createdAt); err != nil {
					t.Fatal(err)
				}
				out += fmt.Sprintf("relation|%s|%s|%s|%s|%s\n", id, from, to, kind, createdAt)
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
	}
	return out
}
