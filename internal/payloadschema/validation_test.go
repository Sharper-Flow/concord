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
// the dispatch_worker boundary. The mirror carries the same closed inputs.binding
// the lane contract declares, so a packet with missing, unknown, or
// contradictory binding data refuses here too.
func TestWorkerPacketTypedPredicatesEnforcePerKindFieldSets(t *testing.T) {
	packet := func(predicates string) []byte {
		return []byte(`{
			"schema_version": "1.0", "attempt_id": "attempt-1", "lane_id": "implement",
			"lane_version": 1, "lane_digest": "sha256:` + string(make([]byte, 0)) + `0000000000000000000000000000000000000000000000000000000000000000",
			"work_id": "work-1", "step_id": "execution",
			"inputs": {"task": "t", "binding": {"objective_source": "contract_premise", "work_version": 1, "contract_version": 1, "assigned_result": "files_touched"}, "outcome_predicates": ` + predicates + `}
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

// The worker_packet mirror enforces the closed inputs.binding: the field is
// required, the objective source is a closed enum, and each source demands
// its matching contract_version shape — the same fail-closed rejection the
// adapter's closed validator applies.
func TestWorkerPacketBindingFailsClosed(t *testing.T) {
	packet := func(binding string) []byte {
		return []byte(`{
			"schema_version": "1.0", "attempt_id": "attempt-1", "lane_id": "implement",
			"lane_version": 1, "lane_digest": "sha256:0000000000000000000000000000000000000000000000000000000000000000",
			"work_id": "work-1", "step_id": "execution",
			"inputs": {"task": "t", "binding": ` + binding + `}
		}`)
	}
	valid := packet(`{"objective_source": "contract_premise", "work_version": 1, "contract_version": 1, "assigned_result": "files_touched"}`)
	if err := Validate("worker_packet", valid); err != nil {
		t.Fatalf("valid binding refused: %v", err)
	}
	version := []byte(`2147483648`)
	if err := Validate("version", version); err != nil {
		t.Fatalf("recorded version refused: %v", err)
	}
	wideVersion := packet(`{"objective_source": "contract_premise", "work_version": 2147483648, "contract_version": 2147483648, "assigned_result": "files_touched"}`)
	if err := Validate("worker_packet", wideVersion); err != nil {
		t.Fatalf("binding narrowed the recorded version domain: %v", err)
	}
	readOnly := packet(`{"objective_source": "work_question", "work_version": 1, "contract_version": null, "assigned_result": "bounded_findings"}`)
	if err := Validate("worker_packet", readOnly); err != nil {
		t.Fatalf("valid read-only binding refused: %v", err)
	}
	missing := []byte(`{
		"schema_version": "1.0", "attempt_id": "attempt-1", "lane_id": "implement",
		"lane_version": 1, "lane_digest": "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		"work_id": "work-1", "step_id": "execution",
		"inputs": {"task": "t"}
	}`)
	if err := Validate("worker_packet", missing); err == nil {
		t.Fatal("packet without a binding was admitted")
	}
	for name, binding := range map[string]string{
		"unknown source":                     `{"objective_source": "coordinator_prompt", "work_version": 1, "contract_version": 1, "assigned_result": "files_touched"}`,
		"premise without a contract version": `{"objective_source": "contract_premise", "work_version": 1, "contract_version": null, "assigned_result": "files_touched"}`,
		"question with a contract version":   `{"objective_source": "work_question", "work_version": 1, "contract_version": 1, "assigned_result": "files_touched"}`,
		"undeclared field":                   `{"objective_source": "contract_premise", "work_version": 1, "contract_version": 1, "assigned_result": "files_touched", "step": "repair"}`,
		"missing assignment":                 `{"objective_source": "contract_premise", "work_version": 1, "contract_version": 1}`,
	} {
		if err := Validate("worker_packet", packet(binding)); err == nil {
			t.Fatalf("%s was admitted", name)
		}
	}
}

// Length and item-count refusals name the field, the count unit, the actual
// count, and the limit, so a caller can correct an oversize value without
// guessing how it was measured. String lengths count Unicode code points.
func TestBoundRefusalsNameUnitActualAndLimit(t *testing.T) {
	cases := []struct {
		value  any
		schema map[string]any
		want   string
	}{
		{"é🙂x", map[string]any{"type": "string", "maxLength": json.Number("2")}, "maxLength at $.task: carries 3 Unicode code points against a limit of 2"},
		{"", map[string]any{"type": "string", "minLength": json.Number("1")}, "minLength at $.task: carries 0 Unicode code points against a minimum of 1"},
		{[]any{"a", "b", "c"}, map[string]any{"type": "array", "maxItems": json.Number("2")}, "maxItems at $.task: carries 3 item(s) against a limit of 2"},
		{[]any{}, map[string]any{"type": "array", "minItems": json.Number("1")}, "minItems at $.task: carries 0 item(s) against a minimum of 1"},
	}
	for _, tc := range cases {
		err := ValidateValue(tc.value, tc.schema, nil, "$.task")
		if err == nil || err.Error() != tc.want {
			t.Errorf("refusal = %v, want %q", err, tc.want)
		}
	}
}
