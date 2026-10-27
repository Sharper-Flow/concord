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
				{"concord_knowledge", "search"},
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
		{"concord_knowledge", "unknown"},
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
		{name: "input", payload: `{}`},
		{name: "result", payload: `{"items":[]}`, result: true},
		{name: "input as result", payload: `{}`, result: true, want: "missing payload field items"},
		{name: "result as input", payload: `{"items":[]}`, want: "unknown payload field items"},
		{name: "result sibling field", payload: `{"items":[],"locator_id":"locator"}`, result: true, want: "unknown payload field locator_id for operation concord_knowledge.search; field is declared by operation(s) concord_knowledge.observe_publication"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := agentcontract.ValidateOperationPayload("concord_knowledge", "search", []byte(test.payload), test.result)
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || err.Error() != test.want {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateOperationPayloadOrdersMissingUnknownAndSiblingFields(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"zeta":1,"path":"src/example.go","alpha":2}`)
	want := "missing payload field product_id; missing payload field domain_id; unknown payload field alpha; unknown payload field path for operation concord_domain.detail; field is declared by operation(s) concord_domain.shape; unknown payload field zeta"
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
		{`{"kinds":["bogus"]}`, "enum mismatch at $.kinds[0]"},
	} {
		err := agentcontract.ValidateOperationPayload("concord_knowledge", "search", []byte(test.payload), false)
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
			ID: "concord_knowledge.search", Tool: "concord_knowledge", Operation: "search",
			Kind: agentcontract.OperationRead, QueryID: "PM1.Q9", Capability: "product_read",
			Consequence: "read", Approval: "none", Availability: agentcontract.AvailabilityAlways,
			SupportedBudgetSeconds: 300, InputSchema: "knowledge_search_input", ResultSchema: "knowledge_page",
		},
		{
			ID: "concord_work_transition.workflow_action", Tool: "concord_work_transition", Operation: "workflow_action",
			Kind: agentcontract.OperationMutation, Capability: "work_transition",
			Consequence: "workflow_action", Approval: "conditional", Availability: agentcontract.AvailabilityWorkflowDefinition,
			SupportedBudgetSeconds: 30, InputSchema: "work_transition_action_input", ResultSchema: "mutation_result",
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
	if owners := agentcontract.GeneratedPayloadFieldOwners["concord_domain/input"]["path"]; !slices.Equal(owners, []string{"concord_domain.shape"}) {
		t.Fatalf("path field owners = %v", owners)
	}
}
