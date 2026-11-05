package payloadschema

import (
	"encoding/json"
	"strings"
	"testing"
)

// contractGuidanceInput builds one approve_contract input so a test states
// only the fieldset it studies.
func contractGuidanceInput(t *testing.T, idempotencyKey, fields string) []byte {
	t.Helper()
	raw := []byte(`{
		"work_id": "work-1",
		"expected_version": 1,
		"action_id": "approve_contract",
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

func approveFieldsWithSubject(payload string) string {
	return `{
		"premise": "The premise states the bounded change.",
		"decision_domains": [],
		"legislative_subjects": [` + payload + `]
	}`
}

func revisionContractWithSubject(payload string) string {
	return `{
		"contract_version": 2,
		"premise": "The premise states the bounded replacement.",
		"route_conventions": [],
		"spec_mandate": [],
		"law_modifies": [],
		"rigor_class": "prototype_internal",
		"decision_domains": [],
		"legislative_subjects": [` + payload + `]
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
	data := contractGuidanceInput(t, "select-approve-1",
		approveFieldsWithSubject(`"The subject has spaces"`))

	err := ValidateInDocument(Document(), "work_transition_action_input", data)
	if err == nil {
		t.Fatal("a legislative subject with spaces was accepted")
	}
	got := err.Error()
	if !strings.Contains(got, "pattern at $.fields.legislative_subjects[0]") {
		t.Errorf("refusal does not select the true leaf failure: %s", got)
	}
	if strings.Contains(got, "unknown property $.fields.decision_domains") {
		t.Errorf("a branch-local unknown-property refusal reached the caller as global: %s", got)
	}
	if strings.Contains(got, "record_verdict") {
		t.Errorf("an unrelated action variant consumes the message budget: %s", got)
	}
	if len(got) > 1000 {
		t.Errorf("selected refusal carries %d bytes against the 1000-byte message budget: %s", len(got), got)
	}
}

func TestContractRevisionFailureSelectsTheApplicableVariant(t *testing.T) {
	data := []byte(`{"work_id":"work-1","expected_version":1,"title":"Updated plan","value_statement":"Current criteria","kind":"task","reason":"Revise the plan","idempotency_key":"select-revision-1","contract":` +
		revisionContractWithSubject(`"subject has spaces"`) + `}`)

	err := ValidateInDocument(Document(), "work_define_revise_input", data)
	if err == nil {
		t.Fatal("a legislative subject with spaces was accepted")
	}
	got := err.Error()
	if !strings.Contains(got, "pattern at $.contract.legislative_subjects[0]") {
		t.Errorf("refusal does not select the true leaf failure: %s", got)
	}
	if strings.Contains(got, "unknown property $.contract.decision_domains") {
		t.Errorf("a branch-local unknown-property refusal reached the caller as global: %s", got)
	}
	if strings.Contains(got, "approve_contract") {
		t.Errorf("an unrelated action variant consumes the message budget: %s", got)
	}
	if len(got) > 1000 {
		t.Errorf("selected refusal carries %d bytes against the 1000-byte message budget: %s", len(got), got)
	}
}

// A genuinely invalid decision_domains member is refused through the variant
// that declares the field, not through the legacy alternative that forbids it.
func TestInvalidDecisionDomainsNamesItsEnumThroughTheDeclaringVariant(t *testing.T) {
	fields := `{
		"premise": "The premise states the bounded change.",
		"decision_domains": ["bogus"]
	}`
	data := contractGuidanceInput(t, "select-domains-1", fields)

	err := ValidateInDocument(Document(), "work_transition_action_input", data)
	if err == nil {
		t.Fatal("an unknown decision domain was accepted")
	}
	got := err.Error()
	if !strings.Contains(got, `enum mismatch at $.fields.decision_domains[0]: accepted values are ["increased_spending","legal","product","production_data","security","ui_ux"]`) {
		t.Errorf("refusal does not name the invalid decision domain through its declaring variant: %s", got)
	}
	if strings.Contains(got, "unknown property $.fields.decision_domains") {
		t.Errorf("a branch-local unknown-property refusal reached the caller as global: %s", got)
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
