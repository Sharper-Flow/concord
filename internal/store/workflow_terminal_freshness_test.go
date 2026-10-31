package store

import "context"

// prepareTerminalTestContext is the production pre-transaction proof
// preparation (PrepareWorkflowActionProof) for a raw test transaction.
func prepareTerminalTestContext(ctx context.Context, s *Store, workID, action string) (context.Context, error) {
	return s.PrepareWorkflowActionProof(ctx, action, workID)
}
