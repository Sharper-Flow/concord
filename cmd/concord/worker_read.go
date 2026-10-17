package main

import (
	"context"
	"io"

	"github.com/sharper-flow/concord/internal/store"
)

func runWorkerRead(raw []byte, read func(context.Context, store.WorkerReadingRequest) (store.WorkerReadingPage, error), out, errOut io.Writer) int {
	var request store.WorkerReadingRequest
	if err := decodeObject(raw, &request); err != nil {
		writeOperatorDiagnostic(errOut, "worker-read", err.Error())
		return 1
	}
	page, err := read(context.Background(), request)
	if err != nil {
		writeOperatorDiagnostic(errOut, "worker-read", err.Error())
		return 1
	}
	return writeJSON(out, page, errOut)
}
