package main

import (
	"context"
	"io"

	"github.com/sharper-flow/concord/internal/store"
)

func runResearchExport(raw []byte, path string, out, errOut io.Writer) int {
	const command = "research-export"
	var request struct {
		Destination string `json:"destination"`
	}
	if err := decodeObject(raw, &request); err != nil {
		writeOperatorDiagnostic(errOut, command, diagnosticText(err))
		return 1
	}
	result, err := store.ExportResearchDatabase(context.Background(), path, request.Destination)
	if err != nil {
		writeOperatorDiagnostic(errOut, command, diagnosticText(err))
		return 1
	}
	return writeJSON(out, result, errOut)
}
