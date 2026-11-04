package agent

import (
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

func TestGeneratedOperationsCarryManifestOwnedBoundaries(t *testing.T) {
	t.Parallel()
	for _, operation := range agentcontract.ContractOperations {
		if operation.Consequence == "" || operation.InputSchema == "" || operation.ResultSchema == "" {
			t.Fatalf("operation %s omitted generated boundary metadata: %+v", operation.ID, operation)
		}
	}
}
