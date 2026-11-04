package store

import (
	"encoding/json"
	"reflect"
	"testing"
)

func sixClassActionField(t *testing.T) WorkflowPayloadField {
	t.Helper()
	action, err := WorkflowPlanActionContract("approve_contract")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range action.Payload.Fields {
		if field.Name == "decision_domains" {
			return field
		}
	}
	t.Fatal("approve_contract has no decision_domains field")
	return WorkflowPayloadField{}
}

func TestSixClassDecisionDomainsAuthoring(t *testing.T) {
	t.Parallel()
	field := sixClassActionField(t)
	for _, domains := range authoredDecisionDomainSets() {
		if err := validateWorkflowAuthoredDecisionDomains(domains); err != nil {
			t.Fatalf("canonical set %v refused: %v", domains, err)
		}
	}
	for _, raw := range []string{
		`["preference"]`, `["preference","product"]`, `["product","preference"]`,
		`["legal","legal"]`, `["security","legal"]`, `["economics"]`,
		`["increased_spending","legal","product","production_data","security","ui_ux","unknown"]`,
		`null`, `{}`, `"security"`, `[true]`, `[1]`, `[null]`,
	} {
		if !validateWorkflowPayloadValue(field, json.RawMessage(raw)) {
			continue
		}
		var domains []string
		if err := json.Unmarshal([]byte(raw), &domains); err != nil {
			t.Fatal(err)
		}
		if err := validateWorkflowAuthoredDecisionDomains(domains); !hasFailureKind(err, KindInvalidPayload) {
			t.Errorf("authored %s returned %v, want invalid_payload", raw, err)
		}
	}
	for _, domains := range [][]string{{"preference"}, {"preference", "product", "ui_ux"}} {
		if err := validateWorkflowDecisionDomains(domains); err != nil {
			t.Errorf("stored historical fact %v refused: %v", domains, err)
		}
	}
	if !reflect.DeepEqual(field.Enum, []string{"increased_spending", "legal", "product", "production_data", "security", "ui_ux"}) || field.MaxItems == nil || *field.MaxItems != 6 {
		t.Fatalf("authoring field = %+v, want exactly the six approved classes", field)
	}
	if validateWorkflowPayloadValue(field, json.RawMessage(`["preference"]`)) {
		t.Fatal("new action schema permits historical preference")
	}
}

func TestSixClassDecisionDomainsRoundTrip(t *testing.T) {
	t.Parallel()
	sets := [][]string{
		{"increased_spending"}, {"legal"}, {"product"}, {"production_data"}, {"security"}, {"ui_ux"},
		{"increased_spending", "legal", "product", "production_data", "security", "ui_ux"},
	}
	for _, domains := range sets {
		t.Run(workflowJSON(domains), func(t *testing.T) {
			field := sixClassActionField(t)
			if !validateWorkflowPayloadValue(field, json.RawMessage(workflowJSON(domains))) {
				t.Fatal("approved six-class fact refused by action payload")
			}
			var authored []string
			if err := json.Unmarshal([]byte(workflowJSON(domains)), &authored); err != nil {
				t.Fatal(err)
			}
			if err := validateWorkflowAuthoredDecisionDomains(authored); err != nil {
				t.Fatalf("approved six-class fact refused by authoring: %v", err)
			}
			if !reflect.DeepEqual(authored, domains) {
				t.Fatalf("authored %v, want %v", authored, domains)
			}
			s := openTemp(t)
			defer s.Close()
			seedDecisionDomainsItemFatal(t, s, "six-class-contract", authored)
			if got := storedDecisionDomains(t, s, "six-class-contract", 1); got != workflowJSON(domains) {
				t.Fatalf("stored %s, want %s", got, workflowJSON(domains))
			}
			if got := projectedDecisionDomains(t, s, "six-class-contract"); !reflect.DeepEqual(got, domains) {
				t.Fatalf("projected %v, want %v", got, domains)
			}
		})
	}
}
