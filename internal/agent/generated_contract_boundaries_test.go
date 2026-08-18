package agent

import "testing"

func TestGeneratedOperationsCarryManifestOwnedBoundaries(t *testing.T) {
	for _, operation := range ContractOperations {
		if operation.Consequence == "" || operation.InputSchema == "" || operation.ResultSchema == "" {
			t.Fatalf("operation %s omitted generated boundary metadata: %+v", operation.ID, operation)
		}
	}
	research, ok := ValidateContractOperation("concord_work_define", "research_pack_create")
	if !ok || research.Consequence != OperationConsequence("research") {
		t.Fatalf("research consequence = %q, found=%t", research.Consequence, ok)
	}
	claim, ok := ValidateContractOperation("concord_work_relate", "resource_claim")
	if !ok || claim.Consequence != OperationConsequence("claim") {
		t.Fatalf("claim consequence = %q, found=%t", claim.Consequence, ok)
	}
}
