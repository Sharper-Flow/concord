package payloadschema

import (
	"encoding/json"
	"strings"
	"testing"
)

// contractGuidanceInput builds one work_transition_action_input payload for
// the given action and fields, so a test states only the fieldset it studies.
func contractGuidanceInput(t *testing.T, action, idempotencyKey, fields string) []byte {
	t.Helper()
	raw := []byte(`{
		"work_id": "work-1",
		"expected_version": 1,
		"action_id": "` + action + `",
		"idempotency_key": "` + idempotencyKey + `",
		"fields": ` + fields + `
	}`)
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("test payload is not JSON: %v", err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func approveFieldsWithPredicate(payload string) string {
	return `{
		"premise": "The premise states the bounded change.",
		"decision_domains": [],
		"outcome_predicates": [
			{"predicate_id": "predicate:one", "ordinal": 0, "outcome_kind": "exists",
			 "outcome_payload": {"kind": "exists", "surface": "go_source", "subjects": [` + payload + `]}}
		]
	}`
}

func supersedeFieldsWithPredicate(payload string) string {
	return `{
		"contract_version": 1,
		"premise": "The premise states the bounded replacement.",
		"required_evidence": [],
		"route_conventions": [],
		"spec_mandate": [],
		"law_modifies": [],
		"rigor_class": "prototype_internal",
		"supersede_reason": "The reason states the bounded replacement.",
		"audit_evidence": ["audit-reference-1"],
		"decision_domains": [],
		"outcome_predicates": [
			{"predicate_id": "predicate:two", "ordinal": 0, "outcome_kind": "absent",
			 "outcome_payload": ` + payload + `}
		]
	}`
}

// A refused contract request names the one true leaf failure. The legacy
// fields alternative forbids decision_domains through its own closed shape,
// so its "unknown property" refusal is local to that branch and must never
// reach the caller as a rule about the whole request; the sibling variant
// declares and accepts the field. Unrelated action variants are ruled out by
// the supplied action_id discriminator, so their constants never consume the
// message budget either.
func TestContractRequestFailureSelectsTheApplicableVariant(t *testing.T) {
	data := contractGuidanceInput(t, "approve_contract", "select-approve-1",
		approveFieldsWithPredicate(`"The acceptance sentence has spaces"`))

	err := ValidateInDocument(Document(), "work_transition_action_input", data)
	if err == nil {
		t.Fatal("a predicate subject with spaces was accepted")
	}
	got := err.Error()
	if !strings.Contains(got, "pattern at $.fields.outcome_predicates[0].outcome_payload.subjects[0]") {
		t.Errorf("refusal does not select the true leaf failure: %s", got)
	}
	if strings.Contains(got, "unknown property $.fields.decision_domains") {
		t.Errorf("a branch-local unknown-property refusal reached the caller as global: %s", got)
	}
	if strings.Contains(got, "supersede_contract") {
		t.Errorf("an unrelated action variant consumes the message budget: %s", got)
	}
	if len(got) > 1000 {
		t.Errorf("selected refusal carries %d bytes against the 1000-byte message budget: %s", len(got), got)
	}
}

func TestSupersedeRequestFailureSelectsTheApplicableVariant(t *testing.T) {
	data := contractGuidanceInput(t, "supersede_contract", "select-supersede-1",
		supersedeFieldsWithPredicate(`{"kind": "absent", "surface": "go_source", "subjects": ["internal-store-workpin"], "distinguish_from": ["deleted"]}`))

	err := ValidateInDocument(Document(), "work_transition_action_input", data)
	if err == nil {
		t.Fatal("a distinguish_from value outside its enum was accepted")
	}
	got := err.Error()
	if !strings.Contains(got, "enum mismatch at $.fields.outcome_predicates[0].outcome_payload.distinguish_from[0]") {
		t.Errorf("refusal does not select the true leaf failure: %s", got)
	}
	if strings.Contains(got, "unknown property $.fields.decision_domains") {
		t.Errorf("a branch-local unknown-property refusal reached the caller as global: %s", got)
	}
	if strings.Contains(got, "approve_contract") {
		t.Errorf("an unrelated action variant consumes the message budget: %s", got)
	}
	if len(got) > 1000 {
		t.Errorf("selected refusal carries %d bytes against the 1000-byte message budget: %s", len(got), got)
	}
}

// Each outcome kind's own invalid member is the leaf failure when the
// supplied outcome_kind discriminator selects that kind's variant alone.
func TestOutcomeKindDiscriminatorSelectsTheKindLeafFailure(t *testing.T) {
	cases := []struct {
		name    string
		kind    string
		payload string
		want    string
		absent  []string
	}{
		{
			name:    "exists bad subject pattern",
			kind:    "exists",
			payload: `{"kind": "exists", "surface": "go_source", "subjects": ["has spaces"]}`,
			want:    "pattern at $.fields.outcome_predicates[0].outcome_payload.subjects[0]",
			absent:  []string{"distinguish_from", "expected_result", `allowed`},
		},
		{
			name:    "absent bad distinguish_from enum",
			kind:    "absent",
			payload: `{"kind": "absent", "surface": "go_source", "subjects": ["internal-store-workpin"], "distinguish_from": ["deleted"]}`,
			want:    "enum mismatch at $.fields.outcome_predicates[0].outcome_payload.distinguish_from[0]: accepted values are",
			absent:  []string{"expected_result", `"exists"`},
		},
		{
			name:    "outcome bad allowed enum",
			kind:    "outcome",
			payload: `{"kind": "outcome", "allowed": ["unknown"]}`,
			want:    "enum mismatch at $.fields.outcome_predicates[0].outcome_payload.allowed[0]: accepted values are",
			absent:  []string{"distinguish_from", "check_ref"},
		},
		{
			name:    "check bad check_ref pattern",
			kind:    "check",
			payload: `{"kind": "check", "check_ref": "not a check", "immutable_subject_ref": "src-example", "expected_result": "pass"}`,
			want:    "pattern at $.fields.outcome_predicates[0].outcome_payload.check_ref",
			absent:  []string{"distinguish_from", `"outcome"`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			fields := `{
				"premise": "The premise states the bounded change.",
				"decision_domains": [],
				"outcome_predicates": [
					{"predicate_id": "predicate:one", "ordinal": 0, "outcome_kind": "` + tc.kind + `",
					 "outcome_payload": ` + tc.payload + `}
				]
			}`
			data := contractGuidanceInput(t, "approve_contract", "select-kind-"+tc.kind, fields)
			err := ValidateInDocument(Document(), "work_transition_action_input", data)
			if err == nil {
				t.Fatal("an invalid outcome payload was accepted")
			}
			got := err.Error()
			if !strings.Contains(got, tc.want) {
				t.Errorf("refusal does not select the %s leaf failure: %s", tc.kind, got)
			}
			for _, noise := range tc.absent {
				if strings.Contains(got, noise) {
					t.Errorf("refusal carries unrelated branch noise %q: %s", noise, got)
				}
			}
			if len(got) > 1000 {
				t.Errorf("selected refusal carries %d bytes against the 1000-byte message budget: %s", len(got), got)
			}
		})
	}
}

// A genuinely invalid decision_domains member is refused through the variant
// that declares the field, not through the legacy alternative that forbids it.
func TestInvalidDecisionDomainsNamesItsEnumThroughTheDeclaringVariant(t *testing.T) {
	fields := `{
		"premise": "The premise states the bounded change.",
		"decision_domains": ["bogus"],
		"outcome_predicates": [
			{"predicate_id": "predicate:one", "ordinal": 0, "outcome_kind": "exists",
			 "outcome_payload": {"kind": "exists", "surface": "go_source", "subjects": ["internal-store-workpin"]}}
		]
	}`
	data := contractGuidanceInput(t, "approve_contract", "select-domains-1", fields)

	err := ValidateInDocument(Document(), "work_transition_action_input", data)
	if err == nil {
		t.Fatal("an unknown decision domain was accepted")
	}
	got := err.Error()
	if !strings.Contains(got, `enum mismatch at $.fields.decision_domains[0]: accepted values are ["preference","product","ui_ux"]`) {
		t.Errorf("refusal does not name the invalid decision domain through its declaring variant: %s", got)
	}
	if strings.Contains(got, "unknown property $.fields.decision_domains") {
		t.Errorf("a branch-local unknown-property refusal reached the caller as global: %s", got)
	}
}

// An outcome_payload contradicted by its own kind constant is refused through
// the variant the declared outcome_kind selects, without sibling kinds'
// missing-field noise.
func TestContradictoryOutcomeKindSelectsTheDeclaredKind(t *testing.T) {
	fields := `{
		"premise": "The premise states the bounded change.",
		"decision_domains": [],
		"outcome_predicates": [
			{"predicate_id": "predicate:one", "ordinal": 0, "outcome_kind": "exists",
			 "outcome_payload": {"kind": "check", "surface": "go_source", "subjects": ["internal-store-workpin"]}}
		]
	}`
	data := contractGuidanceInput(t, "approve_contract", "select-kind-contradiction", fields)

	err := ValidateInDocument(Document(), "work_transition_action_input", data)
	if err == nil {
		t.Fatal("an outcome_kind contradicted by its payload kind was accepted")
	}
	got := err.Error()
	if !strings.Contains(got, "const mismatch at $.fields.outcome_predicates[0].outcome_payload.kind") {
		t.Errorf("refusal does not name the contradicted kind constant: %s", got)
	}
	for _, noise := range []string{"distinguish_from", "expected_result"} {
		if strings.Contains(got, noise) {
			t.Errorf("refusal carries unrelated kind noise %q: %s", noise, got)
		}
	}
}

// Two alternatives with no discriminator between them genuinely remain: the
// refusal labels both variants instead of promoting one branch's local
// restriction to a rule about the whole request.
func TestNonDiscriminatedAlternativesStayLabeled(t *testing.T) {
	root := decodeSchema(t, `{
		"$defs": {
			"legacy": {"type": "object", "additionalProperties": false, "required": ["premise"], "properties": {"premise": {"type": "string"}}},
			"extended": {"type": "object", "additionalProperties": false, "required": ["decision_domains"], "properties": {"decision_domains": {"items": {"enum": ["a", "b"], "type": "string"}, "type": "array"}}}
		},
		"oneOf": [{"$ref": "#/$defs/legacy"}, {"$ref": "#/$defs/extended"}]
	}`)
	value := decodeValue(t, `{"decision_domains": ["bogus"]}`)

	err := ValidateValue(value, root, root, "$")
	if err == nil {
		t.Fatal("a fieldset matching no variant was accepted")
	}
	got := err.Error()
	if !strings.Contains(got, "enum mismatch at $.decision_domains[0]: accepted values are [\"a\",\"b\"]") {
		t.Errorf("the closed fieldset's declaring variant does not carry the refusal: %s", got)
	}
	if strings.Contains(got, "missing required $.premise") || strings.Contains(got, "unknown property $.decision_domains") {
		t.Errorf("the forbidden alternative's local refusal reached the caller: %s", got)
	}
}

// Without any contradiction to select on, oneOf keeps today's rendering: the
// joined branch failures and the variant list that labels the alternatives.
func TestUnfilteredOneOfKeepsItsVariantList(t *testing.T) {
	root := decodeSchema(t, `{
		"$defs": {
			"left": {"type": "object", "additionalProperties": false, "required": ["premise"], "properties": {"premise": {"type": "string"}}},
			"right": {"type": "object", "additionalProperties": false, "required": ["digest"], "properties": {"digest": {"type": "string"}}}
		},
		"oneOf": [{"$ref": "#/$defs/left"}, {"$ref": "#/$defs/right"}]
	}`)
	value := decodeValue(t, `{"middle": 1}`)

	err := ValidateValue(value, root, root, "$")
	if err == nil {
		t.Fatal("a value matching no variant was accepted")
	}
	got := err.Error()
	if !strings.Contains(got, "oneOf mismatch at $") || !strings.Contains(got, "expected exactly one accepted variant") {
		t.Errorf("unfiltered oneOf lost its framing: %s", got)
	}
}

// A value two alternatives both accept keeps the exactly-one refusal with the
// variant list, and never embeds instance values in the message.
func TestMultipleOneOfMatchesKeepTheVariantList(t *testing.T) {
	err := ValidateInDocument(Document(), "product_view_resolve_input", []byte(`{"product_id": "p-1", "project_id": "pr-1"}`))
	if err == nil {
		t.Fatal("resolve accepted both union alternatives")
	}
	got := err.Error()
	if !strings.Contains(got, "oneOf mismatch at $") || !strings.Contains(got, "product_id") || !strings.Contains(got, "project_id") {
		t.Errorf("union refusal lost its variant labels: %s", got)
	}
	if strings.Contains(got, "p-1") || strings.Contains(got, "pr-1") {
		t.Errorf("union refusal embedded instance values: %s", got)
	}
}
