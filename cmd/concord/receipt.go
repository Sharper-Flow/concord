package main

import (
	"context"
	"fmt"
	"io"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/receipt"
	"github.com/sharper-flow/concord/internal/store"
)

// runReceipt prints the product-owned closure receipt for one work item.
// The receipt's bytes are owned by internal/receipt (CD-0169); this verb is
// the render surface every agent and the adapter shell, so no caller ever
// formats the receipt itself. A work item outside the completed lifecycle
// prints nothing and exits 0: the receipt is a completion receipt, and the
// omission is the degrade path CD-0170 binds. An unknown work item or an
// unreadable projection is a diagnostic and exit 1.
func runReceipt(raw []byte, s *store.Store, out, errOut io.Writer) int {
	var request struct {
		WorkID string `json:"work_id"`
	}
	if err := decodeObject(raw, &request); err != nil {
		writeOperatorDiagnostic(errOut, "receipt", err.Error())
		return 1
	}
	if request.WorkID == "" {
		writeOperatorDiagnostic(errOut, "receipt", "work_id is required")
		return 1
	}
	receiptText, err := receipt.RenderForWork(context.Background(), s, request.WorkID)
	if err != nil {
		writeOperatorDiagnostic(errOut, "receipt", err.Error())
		return 1
	}
	if receiptText == "" {
		return 0
	}
	if len(receiptText) > agent.MaxEnvelopeBytes {
		writeOperatorDiagnostic(errOut, "receipt", "receipt exceeds the bounded output size")
		return 1
	}
	if _, err := fmt.Fprintln(out, receiptText); err != nil {
		writeDiagnostic(errOut, err.Error())
		return 1
	}
	return 0
}
