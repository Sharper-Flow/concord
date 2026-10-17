package main

import (
	"context"
	"io"

	"github.com/sharper-flow/concord/internal/store"
)

func runWorkerRead(raw []byte, read func(context.Context, store.WorkerReadingRequest) (store.WorkerReadingPage, error), out, errOut io.Writer) int {
	return runWorkerPage("worker-read", raw, read, out, errOut)
}

func runWorkerDiff(raw []byte, read func(context.Context, store.WorkerDiffRequest) (store.WorkerDiffPage, error), out, errOut io.Writer) int {
	return runWorkerPage("worker-diff", raw, read, out, errOut)
}

func runWorkerPage[Request, Page any](command string, raw []byte, read func(context.Context, Request) (Page, error), out, errOut io.Writer) int {
	var request Request
	if err := decodeObject(raw, &request); err != nil {
		writeOperatorDiagnostic(errOut, command, err.Error())
		return inputRefusalExit
	}
	page, err := read(context.Background(), request)
	if err != nil {
		writeOperatorDiagnostic(errOut, command, err.Error())
		return 1
	}
	return writeJSON(out, page, errOut)
}
