package main

import (
	"context"
	"io"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/store"
)

func runWorkerControl(ctx context.Context, raw []byte, execute func(context.Context, store.WorkerControlRequest, agent.WorkerControlAssertion) (store.WorktreeVerifyResult, error), out, errOut io.Writer) int {
	var request struct {
		store.WorkerControlRequest
		Assertion agent.WorkerControlAssertion `json:"assertion"`
	}
	if err := decodeObject(raw, &request); err != nil {
		writeOperatorDiagnostic(errOut, "worker-control", diagnosticText(err))
		return inputRefusalExit
	}
	result, err := execute(ctx, request.WorkerControlRequest, request.Assertion)
	if err != nil {
		writeOperatorDiagnostic(errOut, "worker-control", diagnosticText(err))
		if result.Oracle != nil && result.OperationRef != "" {
			// A producer may return retained facts beside its refusal. Never
			// qualify a partial or failed finalization as accepted evidence.
			oracle := *result.Oracle
			result.Oracle = &oracle
			result.Oracle.Qualification = "unavailable"
			disposition := result.LeaseDisposition
			if disposition == nil {
				disposition = &store.WorktreeVerifyLeaseDisposition{State: "unknown"}
			}
			writeJSON(out, struct {
				store.WorktreeVerifyResult
				Outcome          string                                `json:"outcome"`
				Diagnostic       string                                `json:"diagnostic"`
				LeaseDisposition *store.WorktreeVerifyLeaseDisposition `json:"lease_disposition"`
			}{result, "refused", diagnosticText(err), disposition}, errOut)
		}
		return 1
	}
	return writeJSON(out, result, errOut)
}
