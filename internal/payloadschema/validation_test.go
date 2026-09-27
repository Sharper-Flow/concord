package payloadschema

import (
	"encoding/json"
	"testing"
)

func TestReferenceKeepsSiblingConstraints(t *testing.T) {
	root := map[string]any{"$defs": map[string]any{"text": map[string]any{"type": "string"}}}
	schema := map[string]any{"$ref": "#/$defs/text", "maxLength": json.Number("2")}
	if err := ValidateValue("abc", schema, root, "$"); err == nil {
		t.Fatal("reference discarded its sibling bound")
	}
	if err := ValidateValue("ab", schema, root, "$"); err != nil {
		t.Fatal(err)
	}
}

func TestLessonPublishIDMeetsCoverageMinimum(t *testing.T) {
	input := func(id string) []byte {
		value, err := json.Marshal(map[string]string{
			"work_id": "work-1", "lesson_id": id, "title": "Lesson", "summary": "Summary",
			"content": "Body", "idempotency_key": "lesson-key",
		})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	if err := Validate("work_compact_lesson_publish_input", input("x")); err == nil {
		t.Fatal("one-character lesson ID would create a coverage shard that CI refuses")
	}
	if err := Validate("work_compact_lesson_publish_input", input("xy")); err != nil {
		t.Fatalf("two-character lesson ID refused: %v", err)
	}
}

func TestDateTimeFormatIsEnforced(t *testing.T) {
	schema := map[string]any{"type": "string", "format": "date-time"}
	for _, text := range []string{"2026-09-14T12:00:00Z", "2026-09-14T12:00:00.123456789+02:30"} {
		if err := ValidateValue(text, schema, nil, "$"); err != nil {
			t.Errorf("%s: %v", text, err)
		}
	}
	for _, text := range []string{"not-a-time", "2026-02-30T00:00:00Z", "2026-09-14", ""} {
		if err := ValidateValue(text, schema, nil, "$"); err == nil {
			t.Errorf("accepted %q", text)
		}
	}
}

// The worker_packet mirror must refuse a typed outcome predicate whose
// outcome_payload does not match its declared outcome_kind, so a packet the
// lane contract admits cannot diverge from the strict per-kind field sets at
// the dispatch_worker boundary.
func TestWorkerPacketTypedPredicatesEnforcePerKindFieldSets(t *testing.T) {
	packet := func(predicates string) []byte {
		return []byte(`{
			"schema_version": "1.0", "attempt_id": "attempt-1", "lane_id": "implement",
			"lane_version": 1, "lane_digest": "sha256:` + string(make([]byte, 0)) + `0000000000000000000000000000000000000000000000000000000000000000",
			"work_id": "work-1", "step_id": "execution",
			"inputs": {"task": "t", "outcome_predicates": ` + predicates + `}
		}`)
	}
	valid := packet(`[{"predicate_id":"predicate:one","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:one","immutable_subject_ref":"contracts/x.json","expected_result":"pass"}}]`)
	if err := Validate("worker_packet", valid); err != nil {
		t.Fatalf("valid typed packet refused: %v", err)
	}
	kindMismatch := packet(`[{"predicate_id":"predicate:one","ordinal":0,"outcome_kind":"exists","outcome_payload":{"kind":"check","check_ref":"check:one","immutable_subject_ref":"contracts/x.json","expected_result":"pass"}}]`)
	if err := Validate("worker_packet", kindMismatch); err == nil {
		t.Fatal("kind mismatch between outcome_kind and outcome_payload was admitted")
	}
	incompleteSet := packet(`[{"predicate_id":"predicate:one","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:one"}}]`)
	if err := Validate("worker_packet", incompleteSet); err == nil {
		t.Fatal("check payload with an incomplete strict field set was admitted")
	}
	foreignField := packet(`[{"predicate_id":"predicate:one","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:one","immutable_subject_ref":"contracts/x.json","expected_result":"pass","allowed":["resolved"]}}]`)
	if err := Validate("worker_packet", foreignField); err == nil {
		t.Fatal("payload carrying another kind's field was admitted")
	}
}
