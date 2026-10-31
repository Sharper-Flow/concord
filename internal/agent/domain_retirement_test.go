package agent

import (
	"context"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

func TestDomainReadsRejectRetiredOperations(t *testing.T) {
	for _, operation := range []string{"shape", "footprint"} {
		if _, found := agentcontract.ValidateContractOperation("concord_domain", operation); found {
			t.Fatalf("retired Domain operation %q is declared", operation)
		}
		r := runtime{Tool: "concord_domain", Operation: operation}
		base := agentcontract.NewBase("retired-domain-operation", r.Tool, r.Operation)
		if _, err := r.readResult(context.Background(), base, []byte(`{"product_id":"product"}`), ""); err == nil {
			t.Fatalf("retired Domain operation %q reached a read handler", operation)
		}
	}
}
