package store

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func seedQ7CurrentRows(t *testing.T) (*Store, string) {
	t.Helper()
	const work = "q7-all-current-rows"
	s, _ := seedCompletionGateCase(t, work, completionGateCase{})
	seedWork(t, s, "q7-related")
	version := readWorkVersion(t, s, work)
	if err := applyWorkEvent(t, s, relationAddedEvent("q7-related-edge", "depends_on", work, "q7-related", version, version+1), workVersion(work, version)); err != nil {
		t.Fatal(err)
	}
	seedWorkflowAuthority(t, s, "q7-proof-producer", work, "principal/q7-proof", "request/q7-proof", []string{"evidence:q7-proof"})
	version = readWorkVersion(t, s, work)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{workflowEvent("q7-proof-binding", WorkflowEvidenceBound, work, map[string]any{
		"work_id": work, "expected_version": version, "resulting_version": version + 1,
		"evidence_kind": "verification", "immutable_subject_ref": "evidence:q7-proof", "producer_id": "principal/q7-proof",
		"producer_run_ref": "q7-proof-producer", "producer_watermark": "request/q7-proof", "observed_at": "2026-10-01T00:00:00Z",
	})}}); err != nil {
		t.Fatal(err)
	}
	return s, work
}

func TestQ7CurrentRowsWithoutLog(t *testing.T) {
	t.Parallel()
	s, work := seedQ7CurrentRows(t)
	before, err := s.QueryQ7(t.Context(), Q7Request{Work: work, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, entry := range before.Entries {
		kinds[entry.EntryKind] = true
	}
	for _, kind := range q7RecordKinds {
		if !kinds[kind] {
			t.Fatalf("fixture lacks %s", kind)
		}
	}
	var logTables int
	if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='domain_events'`).Scan(&logTables); err != nil || logTables != 0 {
		t.Fatalf("current store retained event log: count=%d err=%v", logTables, err)
	}
	after, err := s.QueryQ7(t.Context(), Q7Request{Work: work, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Result, after.Result) {
		t.Fatalf("log removal changed current history")
	}
}
func TestQ7WorkflowLifecycleStartAndSupersessionMetadata(t *testing.T) {
	t.Parallel()
	s, work := seedQ7CurrentRows(t)
	read := func() HistoryEntry {
		t.Helper()
		page, err := s.QueryQ7(t.Context(), Q7Request{Work: work, RecordKinds: []string{"lifecycle"}})
		if err != nil || len(page.Entries) != 1 {
			t.Fatalf("lifecycle page: %+v, %v", page.Entries, err)
		}
		return page.Entries[0]
	}
	started := read()
	if started.Lifecycle != "in_progress" || started.From == nil || *started.From != "needed" || started.Version <= 1 {
		t.Fatalf("first workflow action lost lifecycle metadata: %+v", started)
	}
	assertQ7Corpus150(t)
	v := readWorkVersion(t, s, work)
	if err := applyWorkEvent(t, s, workTransitionEvent("q7-active-terminal", work, "in_progress", "cancelled", v, v+1), workVersion(work, v)); err != nil {
		t.Fatal(err)
	}
	want := read()
	if want.From == nil || *want.From != "in_progress" {
		t.Fatalf("terminal transition lost prior lifecycle: %+v", want)
	}
}

func TestQ7RemovedRelationDoesNotBecomeHistory(t *testing.T) {
	t.Parallel()
	s, work := seedQ7CurrentRows(t)
	v := readWorkVersion(t, s, work)
	if err := applyWorkEvent(t, s, relationRemovedEvent("q7-remove-edge", "depends_on", work, "q7-related", v, v+1), workVersion(work, v)); err != nil {
		t.Fatal(err)
	}
	out, err := s.QueryQ7(t.Context(), Q7Request{Work: work, RecordKinds: []string{"relation"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Entries == nil || len(out.Entries) != 0 || out.NextCursor != nil {
		t.Fatalf("removed relation survived through its receipt: %+v", out)
	}
}

func TestQ7CreationMetadataDoesNotUseUnrelatedFrom(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	const work = "q7-created-from"
	event := workCreatedEvent(work, "q7-created-from-event")
	var fields map[string]any
	if err := json.Unmarshal(event.Payload, &fields); err != nil {
		t.Fatal(err)
	}
	parents := []Event{locatorProductEvent("product"), locatorProjectEvent("project"), locatorMembershipEvent("product", "project")}
	if err := ApplyOperation(t.Context(), s, Operation{Events: parents}); err != nil {
		t.Fatal(err)
	}
	events := []Event{event, operationEvent("q7-created-membership", "work_project.added", SubjectWorkItem, work, map[string]any{
		"work_id": work, "project_id": "project", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2,
	})}
	if err := ApplyOperation(t.Context(), s, Operation{
		Events:           events,
		ExpectedVersions: workVersion(work, 0),
	}); err != nil {
		t.Fatal(err)
	}
	want, err := s.QueryQ7(t.Context(), Q7Request{Work: work})
	if err != nil || len(want.Entries) != 1 || want.Entries[0].From != nil {
		t.Fatalf("creation metadata: %+v, %v", want.Entries, err)
	}
}
