package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestCapturePersistsTaskAndFullReadReturnsIt(t *testing.T) {
	t.Parallel()
	repo := initBootstrapStoreRepo(t)
	s := openTemp(t)
	seedBootstrapStoreAuthority(t, s, repo)

	const workID, task = "captured-task", "persist this task"
	create := operationEvent("captured-task-create", "work.created", SubjectWorkItem, workID, map[string]any{"work_kind": "task", "title": "Task", "task": task, "value_statement": "Task round trip", "priority": 1})
	create.PayloadVersion = CurrentEventPayloadVersion("work.created")
	membership := operationEvent("captured-task-membership", "work.memberships_replaced", SubjectWorkItem, workID, map[string]any{"memberships": []workMembershipPayload{{ProjectID: "project-bootstrap", Role: "primary"}}, "expected_version": 1, "resulting_version": 2})
	if err := ApplyOperation(t.Context(), s, Operation{Events: []Event{create, membership}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 0}}); err != nil {
		t.Fatal(err)
	}

	var stored string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(intent_json, '$.task') FROM work_items WHERE id=?`, workID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != task {
		t.Fatalf("stored task=%q, want %q", stored, task)
	}
	full, err := s.QueryQ3(context.Background(), Q3Request{Product: "product-bootstrap", WorkIDs: []string{workID}, Detail: "full", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Items) != 1 || full.Items[0].Task != task {
		t.Fatalf("full listing=%+v, want task %q", full.Items, task)
	}
	summary, err := s.QueryQ3(context.Background(), Q3Request{Product: "product-bootstrap", WorkIDs: []string{workID}, Detail: "summary", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Items) != 1 || summary.Items[0].Task != "" {
		t.Fatalf("summary listing=%+v, want no task", summary.Items)
	}
}

func TestIntentRevisionTaskRoundTripPreservesAbsentAndReplacesPresent(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedSchemaEvolutionBase(t, s)
	create := Event{EventID: "task-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "task-work", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion("work.created"), Payload: []byte(`{"work_kind":"task","title":"Original","task":"initial task","value_statement":"Original statement","priority":2}`)}
	membership := Event{EventID: "task-membership", Kind: "work_project.added", SubjectType: SubjectWorkItem, SubjectID: "task-work", Actor: "operator", OccurredAt: time.Unix(1, 1).UTC(), PayloadVersion: 1, Payload: []byte(`{"work_id":"task-work","project_id":"schema-project","role":"primary","reason":"test","expected_version":1,"resulting_version":2}`)}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{create, membership}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "task-work"): 0}}); err != nil {
		t.Fatal(err)
	}
	withoutTask := Event{EventID: "task-revise-preserve", Kind: "work.intent_revised", SubjectType: SubjectWorkItem, SubjectID: "task-work", Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: []byte(`{"title":"Revised","value_statement":"Revised statement","kind":"task","priority":3,"tags":[],"reason":"clarified","expected_version":2,"resulting_version":3}`)}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{withoutTask}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "task-work"): 2}}); err != nil {
		t.Fatal(err)
	}
	assertStoredTask(t, s, "task-work", "initial task")

	updatedTask := "updated task"
	withTaskPayload, err := json.Marshal(map[string]any{"title": "Revised again", "value_statement": "Revised statement again", "kind": "task", "task": updatedTask, "priority": 4, "tags": []string{}, "reason": "clarified again", "expected_version": 3, "resulting_version": 4})
	if err != nil {
		t.Fatal(err)
	}
	withTask := Event{EventID: "task-revise-replace", Kind: "work.intent_revised", SubjectType: SubjectWorkItem, SubjectID: "task-work", Actor: "operator", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: withTaskPayload}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{withTask}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "task-work"): 3}}); err != nil {
		t.Fatal(err)
	}
	assertStoredTask(t, s, "task-work", updatedTask)
}

func assertStoredTask(t *testing.T, s *Store, workID, want string) {
	t.Helper()
	var got string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(intent_json, '$.task') FROM work_items WHERE id=?`, workID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("stored task=%q, want %q", got, want)
	}
}
