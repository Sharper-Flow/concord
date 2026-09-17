package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestBootstrapPersistsTaskAndFullReadReturnsIt(t *testing.T) {
	t.Parallel()
	repo := initBootstrapStoreRepo(t)
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedBootstrapStoreAuthority(t, s, repo)

	request := bootstrapStoreRequest()
	request.Task = "persist this task"
	request.IdempotencyKey = "bootstrap-task-persisted"
	result, err := s.BootstrapWorktree(context.Background(), request, nil)
	if err != nil {
		t.Fatal(err)
	}

	var stored string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(intent_json, '$.task') FROM work_items WHERE id=?`, result.WorkID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != request.Task {
		t.Fatalf("stored task=%q, want %q", stored, request.Task)
	}
	full, err := s.QueryQ3(context.Background(), Q3Request{Product: request.ProductID, WorkIDs: []string{result.WorkID}, Detail: "full", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Items) != 1 || full.Items[0].Task != request.Task {
		t.Fatalf("full listing=%+v, want task %q", full.Items, request.Task)
	}
	summary, err := s.QueryQ3(context.Background(), Q3Request{Product: request.ProductID, WorkIDs: []string{result.WorkID}, Detail: "summary", Limit: 20})
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
	create := Event{EventID: "task-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "task-work", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 2, Payload: []byte(`{"work_kind":"task","title":"Original","task":"initial task","value_statement":"Original statement","priority":2}`)}
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
	if err := RebuildFromLog(context.Background(), s); err != nil {
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
