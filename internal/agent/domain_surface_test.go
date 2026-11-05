package agent

import (
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

// CD-0222 prohibits component as an authority identity on the live agent
// surface. These assertions bind that prohibition at the
// structural seam: every input that once carried component_id/component_ids
// is a closed schema, so the retired identity cannot re-enter as an unknown
// field, and the Domain replacements validate.
func TestComponentInputsAreProhibitedOnTheAgentSurface(t *testing.T) {
	t.Parallel()
	prohibited := []struct {
		tool, operation, payload string
	}{
		{"concord_work_browse", "list", `{"page":{"cursor":null,"limit":20},"component_id":"auth"}`},
		{"concord_work_define", "capture", `{"title":"T","kind":"task","project_ids":["project-1"],"human_key":"ZZ-1","remote_issue_uuid":"probe","url":"https://linear.app/example/issue/ZZ-1","component_id":"auth","idempotency_key":"probe"}`},
		{"concord_work_define", "revise_intent", `{"work_id":"work-1","expected_version":1,"title":"T","kind":"task","reason":"probe","component_id":"auth","idempotency_key":"probe"}`},
	}
	for _, probe := range prohibited {
		if err := agentcontract.ValidateOperationPayload(probe.tool, probe.operation, []byte(probe.payload), false); err == nil {
			t.Fatalf("%s.%s must reject the retired component identity; the closed schema is the structural guarantee", probe.tool, probe.operation)
		}
	}
}

// Domain reads carry a closed-surface guarantee.
func TestDomainReadInputsValidate(t *testing.T) {
	t.Parallel()
	valid := []struct {
		tool, operation, payload string
	}{
		{"concord_domain", "list", `{"product_id":"product-1","page":{"cursor":null,"limit":20}}`},
		{"concord_domain", "detail", `{"product_id":"product-1","domain_id":"root"}`},
	}
	for _, probe := range valid {
		if err := agentcontract.ValidateOperationPayload(probe.tool, probe.operation, []byte(probe.payload), false); err != nil {
			t.Fatalf("%s.%s must validate: %v", probe.tool, probe.operation, err)
		}
	}
	if err := agentcontract.ValidateOperationPayload("concord_domain", "list", []byte(`{"product_id":"product-1","component_id":"auth"}`), false); err == nil {
		t.Fatal("concord_domain must not accept component identity")
	}
}
