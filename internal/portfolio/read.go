// Package portfolio owns the application Product-portfolio read/result
// boundary. Agent and launcher adapters consume this package so neither can
// select a different Product-row query or wire payload.
package portfolio

import (
	"context"

	"github.com/sharper-flow/concord/internal/store"
)

// Result is the canonical C14 Product-row result returned by the store.
// Keeping the store result intact preserves row groups and read metadata for
// both transport and terminal consumers.
type Result = store.ProductRowResult

// Read executes the single bounded Product-row projection used by all
// Product-portfolio consumers.
func Read(ctx context.Context, authority *store.Store, request store.ProductRowRequest) (Result, error) {
	return authority.QueryProductRows(ctx, request)
}

// Map normalizes nil row slices once at the application boundary. It does not
// derive or reinterpret any Product-row field.
func Map(result Result) Result {
	if result.Rows == nil {
		result.Rows = []store.ProductRow{}
	}
	return result
}

// Payload encodes the exact result body used by the agent envelope.
func Payload(result Result) ([]byte, error) {
	return store.ProductRowPagePayload(Map(result))
}
