package agentcontract_test

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/payloadschema"
)

func TestValidateOperationPayloadJSONPrecedesOperationLookup(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, payload, want string }{
		{"empty", "", "EOF"},
		{"malformed", "!", "invalid character '!' looking for beginning of value"},
		{"truncated", `{"a":`, "EOF"},
		{"trailing value", `{} []`, "trailing JSON"},
		{"trailing garbage", `{} garbage`, "trailing JSON"},
		{"escaped duplicate", `{"a":1,"\u0061":2}`, `duplicate JSON object field "a"`},
		{"nested duplicate", `{"nested":[{"a":1,"a":2}]}`, `duplicate JSON object field "a"`},
		{"duplicate before trailing", `{"a":1,"a":2} []`, `duplicate JSON object field "a"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, operation := range []struct{ tool, name string }{
				{"unknown", "unknown"},
				{"concord_work_browse", "list"},
			} {
				for _, result := range []bool{false, true} {
					err := agentcontract.ValidateOperationPayload(operation.tool, operation.name, []byte(test.payload), result)
					if err == nil || err.Error() != test.want {
						t.Fatalf("%s.%s result=%t: error = %v, want %q before lookup or schema checks", operation.tool, operation.name, result, err, test.want)
					}
				}
			}
		})
	}
}

func TestValidateOperationPayloadUnknownOperation(t *testing.T) {
	t.Parallel()
	for _, operation := range []struct{ tool, name string }{
		{"unknown", "search"},
		{"concord_knowledge", "search"},
	} {
		for _, result := range []bool{false, true} {
			err := agentcontract.ValidateOperationPayload(operation.tool, operation.name, []byte(`{}`), result)
			want := "unknown operation payload " + operation.tool + "." + operation.name
			if err == nil || err.Error() != want {
				t.Fatalf("result=%t: error = %v, want %q", result, err, want)
			}
		}
	}
}

func TestValidateOperationPayloadSelectsInputOrResultSchema(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, payload, want string
		result              bool
	}{
		{name: "input", payload: `{"work_id":"work-1"}`},
		{name: "result", payload: `{"observations":[]}`, result: true},
		{name: "input as result", payload: `{"work_id":"work-1"}`, result: true, want: "unknown payload field work_id"},
		{name: "result as input", payload: `{"observations":[]}`, want: "unknown payload field observations"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := agentcontract.ValidateOperationPayload("concord_work_trace", "observations", []byte(test.payload), test.result)
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.HasPrefix(err.Error(), test.want) {
				t.Fatalf("error = %v, want prefix %q", err, test.want)
			}
		})
	}
}

func TestValidateOperationPayloadOrdersMissingUnknownAndSiblingFields(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"zeta":1,"path":"src/example.go","limit":10,"alpha":2}`)
	want := "missing payload field product_id; missing payload field domain_id; unknown payload field alpha; unknown payload field limit for operation concord_domain.detail; field is declared by operation(s) concord_domain.list; unknown payload field path; unknown payload field zeta"
	for range 50 {
		err := agentcontract.ValidateOperationPayload("concord_domain", "detail", payload, false)
		if err == nil || err.Error() != want {
			t.Fatalf("error = %v, want %q", err, want)
		}
	}
}

func TestValidateOperationPayloadEnforcesDetailedSchema(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ payload, want string }{
		{`[]`, "payload is not an object"},
		{`{"title":"T","value_statement":"V","kind":"bogus","project_ids":["project-1"],"idempotency_key":"probe"}`, "enum mismatch at $.kind"},
	} {
		err := agentcontract.ValidateOperationPayload("concord_work_define", "capture", []byte(test.payload), false)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("payload %s: error = %v, want %q", test.payload, err, test.want)
		}
	}
}

func TestValidateOperationPayloadPreservesSelectedGuidance(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, payload string }{
		{
			name:    "missing outer field",
			payload: `{"expected_version":1,"action_id":"approve_contract","idempotency_key":"guidance","fields":{"premise":"A bounded change.","decision_domains":[],"outcome_predicates":[{"predicate_id":"predicate:one","ordinal":0,"outcome_kind":"exists","outcome_payload":{"kind":"exists","surface":"go_source","subjects":["internal-example"]}}]}}`,
		},
		{
			name:    "selected nested field",
			payload: `{"work_id":"work-1","expected_version":1,"action_id":"approve_contract","idempotency_key":"guidance","fields":{"premise":"A bounded change.","decision_domains":["bogus"],"outcome_predicates":[{"predicate_id":"predicate:one","ordinal":0,"outcome_kind":"exists","outcome_payload":{"kind":"exists","surface":"go_source","subjects":["internal-example"]}}]}}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := []byte(test.payload)
			detail := payloadschema.Validate("work_transition_action_input", raw)
			if !payloadschema.HasSelectedGuidance(detail) {
				t.Fatalf("test input must select canonical guidance: %v", detail)
			}
			err := agentcontract.ValidateOperationPayload("concord_work_transition", "workflow_action", raw, false)
			if !payloadschema.HasSelectedGuidance(err) || err.Error() != detail.Error() {
				t.Fatalf("error = %v, want selected guidance %v", err, detail)
			}
			if !strings.Contains(err.Error(), "Accepted shape:") {
				t.Fatalf("refusal lost schema teaching: %v", err)
			}
		})
	}
}

func TestGeneratedContractDeclarations(t *testing.T) {
	t.Parallel()
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(agentcontract.ManifestDigest) {
		t.Fatalf("invalid manifest digest %q", agentcontract.ManifestDigest)
	}
	for _, want := range []agentcontract.ContractOperation{
		{
			ID: "concord_work_transition.workflow_action", Tool: "concord_work_transition", Operation: "workflow_action",
			Kind: agentcontract.OperationMutation, Capability: "work_transition",
			Consequence: "workflow_action", Availability: agentcontract.AvailabilityWorkflowDefinition,
			SupportedBudgetSeconds: 30, InputSchema: "work_transition_action_input", ResultSchema: "projectable_mutation_result",
		},
	} {
		got, ok := agentcontract.ValidateContractOperation(want.Tool, want.Operation)
		if !ok || got != want {
			t.Fatalf("operation %s = %+v, found=%t; want %+v", want.ID, got, ok, want)
		}
	}
	for _, operation := range agentcontract.ContractOperations {
		for _, name := range []string{operation.InputSchema, operation.ResultSchema} {
			if _, ok := agentcontract.GeneratedPayloadRules[name]; !ok {
				t.Fatalf("operation %s has no generated rule for %q", operation.ID, name)
			}
		}
	}
	wantRule := agentcontract.GeneratedPayloadRule{
		Required: []string{"product_id", "domain_id"}, Properties: []string{"product_id", "domain_id", "requested_budget_seconds"},
	}
	gotRule := agentcontract.GeneratedPayloadRules["domain_detail_input"]
	if !slices.Equal(gotRule.Required, wantRule.Required) || !slices.Equal(gotRule.Properties, wantRule.Properties) {
		t.Fatalf("domain detail rule = %+v, want %+v", gotRule, wantRule)
	}
	if owners := agentcontract.GeneratedPayloadFieldOwners["concord_domain/input"]["limit"]; !slices.Equal(owners, []string{"concord_domain.list"}) {
		t.Fatalf("limit field owners = %v", owners)
	}
	if owners := agentcontract.GeneratedPayloadFieldOwners["concord_domain/input"]["path"]; len(owners) != 0 {
		t.Fatalf("path field owners = %v", owners)
	}
}
