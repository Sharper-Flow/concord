package store

import (
	"encoding/json"
	"testing"
)

func TestWorkflowDocumentsReadAdmittedReplacements(t *testing.T) {
	for _, tc := range []struct {
		name, step, action, first, second string
	}{
		{"diagnosis", "diagnose", "record_diagnosis", `{"reproduction":"first reproduction","root_cause":"first cause"}`, `{"reproduction":"second reproduction","root_cause":"second cause"}`},
		{"clarify", "clarify", "record_clarify", `{"resolved_questions":[],"note":"first note"}`, `{"resolved_questions":[],"note":"second note"}`},
		{"plan", "plan", "record_plan", `{"outcomes":"first outcome","approach":"bounded change","decisions":[{"id":"decision:one","question":"which?","choice":"one","rationale":"fits","rejected":[]}],"touches":["file:one"],"merge_split":"one item","risks":"bounded risk"}`, `{"outcomes":"second outcome","approach":"bounded change","decisions":[{"id":"decision:one","question":"which?","choice":"one","rationale":"fits","rejected":[]}],"touches":["file:one"],"merge_split":"one item","risks":"bounded risk","reason":"new evidence"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			work := "documents-replacement-" + tc.name
			f := seedWorkflowReturnRouteFixture(t, work, WorkflowStepPlanFix, tc.step)
			for i, payload := range []string{tc.first, tc.second} {
				if err := runWorkflowActionWithIdentity(t, f.store, work, tc.action, json.RawMessage(payload), f.owner, []string{"first", "second"}[i]); err != nil {
					t.Fatalf("write %d: %v", i, err)
				}
				got, err := ReadWorkflowDocuments(t.Context(), f.store, work)
				if err != nil {
					t.Fatal(err)
				}
				record := map[string]*WorkflowRetainedDocument{"diagnosis": got.Documents.Diagnosis, "clarify": got.Documents.Clarify, "plan": got.Documents.Plan}[tc.name]
				if record == nil || record.RecordedWorkVersion < 1 || got.Version != readWorkVersion(t, f.store, work) {
					t.Fatalf("result: %+v", got)
				}
				var actual, expected map[string]any
				if json.Unmarshal([]byte(record.Document), &actual) != nil || json.Unmarshal([]byte(payload), &expected) != nil {
					t.Fatal("invalid document JSON")
				}
				key := map[string]string{"diagnosis": "reproduction", "clarify": "note", "plan": "outcomes"}[tc.name]
				if actual[key] != expected[key] {
					t.Fatalf("document %s=%v want %v", key, actual[key], expected[key])
				}
			}
		})
	}
}

func TestWorkflowDocumentsInvalidOrUnreadableRefuses(t *testing.T) {
	for _, variant := range []string{"not an object", "invalid UTF-8", "unreadable"} {
		t.Run(variant, func(t *testing.T) {
			const work = "documents-invalid"
			f := seedWorkflowReturnRouteFixture(t, work, WorkflowStepPlanFix, "diagnose")
			db := f.store.DatabaseForTesting()
			if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
				t.Fatal(err)
			}
			if variant != "unreadable" {
				if _, err := db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
					t.Fatal(err)
				}
				payload := `[]`
				if variant == "invalid UTF-8" {
					payload = "{\"root_cause\":\"\xff\"}"
				}
				if _, err := db.Exec(`INSERT INTO workflow_diagnosis_records(work_id,work_version,payload) VALUES(?,1,?)`, work, payload); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`PRAGMA ignore_check_constraints=OFF`); err != nil {
					t.Fatal(err)
				}
			} else if _, err := db.Exec(`DROP TABLE workflow_diagnosis_records`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
				t.Fatal(err)
			}
			_, err := ReadWorkflowDocuments(t.Context(), f.store, work)
			want := KindInvariantViolation
			if variant == "unreadable" {
				want = KindUnavailable
			}
			assertFailureKind(t, err, want)
		})
	}
}
