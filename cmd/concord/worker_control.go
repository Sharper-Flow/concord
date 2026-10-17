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
		writeOperatorDiagnostic(errOut, "worker-control", err.Error())
		return inputRefusalExit
	}
	result, err := execute(ctx, request.WorkerControlRequest, request.Assertion)
	if err != nil {
		writeOperatorDiagnostic(errOut, "worker-control", err.Error())
		return 1
	}
	return writeJSON(out, result, errOut)
}
