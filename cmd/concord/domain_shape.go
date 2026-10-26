package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"
	"unicode/utf8"

	"github.com/sharper-flow/concord/internal/store"
)

func designateOperatorDomainShape(ctx context.Context, s *store.Store, request store.ProductKnowledgeHomeDesignation) (store.ApplyOperationResult, error) {
	result, err := s.DesignateProductDomainShape(ctx, request)
	if err != nil {
		// Include the paths even on failure: valid files can remain without a
		// designation. No rollback may delete a user's authored files.
		raw, marshalErr := json.Marshal(result.Setup)
		if marshalErr != nil {
			return result.ApplyOperationResult, fmt.Errorf("%w; cannot render setup effects: %v", err, marshalErr)
		}
		return result.ApplyOperationResult, fmt.Errorf("%w; filesystem effects: %s", err, raw)
	}
	return result.ApplyOperationResult, nil
}

func runDomainShapeCheck(raw []byte, s *store.Store, out, errOut io.Writer) int {
	var request struct {
		ProductID string `json:"product_id"`
		Checkout  string `json:"checkout,omitempty"`
	}
	if err := decodeObject(raw, &request); err != nil {
		writeDomainShapeDiagnostic(errOut, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	result, err := s.RunProductDomainShapeChecks(ctx, request.ProductID, request.Checkout)
	return writeDomainShapeReport(result, err, out, errOut)
}

func writeDomainShapeReport(result store.DomainShapeOperatorCheck, err error, out, errOut io.Writer) int {
	encoded, encodeErr := json.Marshal(result)
	if encodeErr != nil || len(encoded) > store.DomainShapeMaxBytes {
		writeDomainShapeDiagnostic(errOut, fmt.Errorf("domain-shape-check report exceeds 32768 bytes or cannot be encoded"))
		return 1
	}
	if err != nil {
		// Reports with completed checks are useful diagnostics, not a successful
		// check. A partial or outstanding result still exits nonzero.
		if len(result.Checks) != 0 {
			if code := writeJSON(out, result, errOut); code != 0 {
				return code
			}
		}
		writeDomainShapeDiagnostic(errOut, err)
		return 1
	}
	return writeJSON(out, result, errOut)
}

func writeDomainShapeDiagnostic(out io.Writer, err error) {
	message := diagnosticText(err)
	const maxDiagnostic = 4095 // The trailing newline also fits the 4096-byte bound.
	if len(message) > maxDiagnostic {
		message = message[:maxDiagnostic-3]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
		message += "..."
	}
	writeDiagnostic(out, message)
}
