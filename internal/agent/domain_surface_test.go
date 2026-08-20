package agent

import "testing"

// CD-0041 retires component as an authority identity and #197 removes it from
// the live agent surface. These assertions bind that prohibition at the
// structural seam: every input that once carried component_id/component_ids
// is a closed schema, so the retired identity cannot re-enter as an unknown
// field, and the Domain replacements validate.
func TestComponentInputsAreProhibitedOnTheAgentSurface(t *testing.T) {
	prohibited := []struct {
		tool, operation, payload string
	}{
		{"concord_work_browse", "list", `{"page":{"cursor":null,"limit":20},"component_id":"auth"}`},
		{"concord_work_define", "capture", `{"title":"T","value_statement":"V","kind":"task","project_ids":["project-1"],"component_id":"auth","idempotency_key":"probe"}`},
		{"concord_work_define", "revise_intent", `{"work_id":"work-1","expected_version":1,"title":"T","value_statement":"V","kind":"task","reason":"probe","component_id":"auth","idempotency_key":"probe"}`},
		{"concord_work_initiative", "create", `{"title":"T","value_statement":"V","project_ids":["project-1"],"component_id":"auth","idempotency_key":"probe"}`},
		{"concord_knowledge", "search", `{"page":{"cursor":null,"limit":20},"component_ids":["auth"]}`},
		{"concord_research", "record_finding", `{"pack_id":"pack-1","revision":1,"finding":{"kind":"observation","statement":"s","scopes":{"mode":"explicit","component_ids":["auth"]}},"idempotency_key":"probe"}`},
	}
	for _, probe := range prohibited {
		if err := ValidateOperationPayload(probe.tool, probe.operation, []byte(probe.payload), false); err == nil {
			t.Fatalf("%s.%s must reject the retired component identity; the closed schema is the structural guarantee", probe.tool, probe.operation)
		}
	}
}

// The Domain replacements carry the same closed-surface guarantee: the five
// concord_domain reads validate, and knowledge search accepts domain_id.
func TestDomainReadInputsValidate(t *testing.T) {
	valid := []struct {
		tool, operation, payload string
	}{
		{"concord_domain", "list", `{"product_id":"product-1","page":{"cursor":null,"limit":20}}`},
		{"concord_domain", "detail", `{"product_id":"product-1","domain_id":"root"}`},
		{"concord_domain", "active_work", `{"product_id":"product-1","domain_id":"root","page":{"cursor":null,"limit":20}}`},
		{"concord_domain", "attachments", `{"product_id":"product-1","domain_id":"root"}`},
		{"concord_domain", "overlaps", `{"product_id":"product-1","domain_id":"root"}`},
		{"concord_knowledge", "search", `{"product_id":"product-1","domain_id":"root","page":{"cursor":null,"limit":20}}`},
	}
	for _, probe := range valid {
		if err := ValidateOperationPayload(probe.tool, probe.operation, []byte(probe.payload), false); err != nil {
			t.Fatalf("%s.%s must validate: %v", probe.tool, probe.operation, err)
		}
	}
	if err := ValidateOperationPayload("concord_domain", "list", []byte(`{"product_id":"product-1","component_id":"auth"}`), false); err == nil {
		t.Fatal("concord_domain must not accept component identity")
	}
}
